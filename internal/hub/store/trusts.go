package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/xufei547/railse-opc/internal/proto"
)

// 放行在库里只标撤、不删行（DeleteTrust 除外：那是收回一条从没生效过的），revoke_reason 记为什么撤。
const (
	TrustRevokeUser    = "user"    // 手机上撤的
	TrustRevokeExpired = "expired" // 到期清理（hub 的扫描或守护进程报来的）
	TrustRevokeAgent   = "agent"   // 守护进程报来的撤销（railse trust revoke）
)

const trustCols = "id, machine_id, scope, tool, cli_session_id, project_root, until_at, created_at, created_by, note"

func scanTrust(row interface{ Scan(...any) error }) (proto.Trust, error) {
	var t proto.Trust
	var until, created string
	if err := row.Scan(&t.ID, &t.MachineID, &t.Scope, &t.Tool, &t.CLISession, &t.Project, &until, &created, &t.CreatedBy, &t.Note); err != nil {
		return proto.Trust{}, err
	}
	var err error
	if t.Until, err = time.Parse(time.RFC3339Nano, until); err != nil {
		return proto.Trust{}, fmt.Errorf("放行 %s 的 until_at %q: %w", t.ID, until, err)
	}
	if t.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return proto.Trust{}, fmt.Errorf("放行 %s 的 created_at %q: %w", t.ID, created, err)
	}
	return t, nil
}

// UpsertTrust 登记一条放行，同 id 已有时覆盖。只覆盖同一台机器、还没撤的：别的机器占不了这个 id，撤过（含到期）的不会被
// 守护进程重发的 set 复活。返回是否真的写了。校验（范围、目标、时长、条数）由调用方做，这里只存。
func (s *Store) UpsertTrust(t proto.Trust) (bool, error) {
	if t.ID == "" || t.MachineID == "" {
		return false, errors.New("放行缺 id 或机器")
	}
	created := t.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	res, err := s.db.Exec(`INSERT INTO trusts(`+trustCols+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET scope = excluded.scope, tool = excluded.tool, cli_session_id = excluded.cli_session_id,
  project_root = excluded.project_root, until_at = excluded.until_at, created_by = excluded.created_by, note = excluded.note
WHERE trusts.machine_id = excluded.machine_id AND trusts.revoked_at IS NULL`,
		t.ID, t.MachineID, t.Scope, t.Tool, t.CLISession, t.Project, fmtTime(t.Until), fmtTime(created), t.CreatedBy, t.Note)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RevokeTrust 把一条还没撤的放行标撤（revoked_at 为此刻，revoke_reason 为 reason），返回原条目。machineID 非空时只撤这台机器的。
// 没有、已撤或不是这台机器的返回 ErrNotFound。
func (s *Store) RevokeTrust(machineID, id, reason string) (proto.Trust, error) {
	var t proto.Trust
	err := s.Tx(func(tx *sql.Tx) error {
		var err error
		t, err = scanTrust(tx.QueryRow("SELECT "+trustCols+" FROM trusts WHERE id = ? AND revoked_at IS NULL AND (? = '' OR machine_id = ?)", id, machineID, machineID))
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE trusts SET revoked_at = ?, revoke_reason = ? WHERE id = ?", Now(), reason, id)
		return err
	})
	if err != nil {
		return proto.Trust{}, err
	}
	return t, nil
}

// ListTrusts 按到期先后列生效中（没撤、没过期）的放行；machineID 为空列全部机器的。没有时是空切片。
func (s *Store) ListTrusts(machineID string) ([]proto.Trust, error) {
	rows, err := s.db.Query("SELECT "+trustCols+" FROM trusts WHERE revoked_at IS NULL AND until_at > ? AND (? = '' OR machine_id = ?) ORDER BY until_at, id",
		Now(), machineID, machineID)
	if err != nil {
		return nil, err
	}
	return collectTrusts(rows)
}

// ExpireTrusts 把已到期（until_at 不晚于此刻）、还没撤的放行标撤（原因 expired），返回它们（调用方据此审计）。
func (s *Store) ExpireTrusts() ([]proto.Trust, error) {
	var out []proto.Trust
	err := s.Tx(func(tx *sql.Tx) error {
		now := Now()
		rows, err := tx.Query("SELECT "+trustCols+" FROM trusts WHERE revoked_at IS NULL AND until_at <= ? ORDER BY until_at, id", now)
		if err != nil {
			return err
		}
		if out, err = collectTrusts(rows); err != nil {
			return err
		}
		_, err = tx.Exec("UPDATE trusts SET revoked_at = ?, revoke_reason = ? WHERE revoked_at IS NULL AND until_at <= ?", now, TrustRevokeExpired, now)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteTrust 删掉一条放行的整行，只用来收回刚建、却没能生效的（批准落库失败）：它从没下发过，不留记录。
func (s *Store) DeleteTrust(id string) error {
	_, err := s.db.Exec("DELETE FROM trusts WHERE id = ?", id)
	return err
}

// collectTrusts 读完并关闭 rows。
func collectTrusts(rows *sql.Rows) ([]proto.Trust, error) {
	defer rows.Close()
	out := []proto.Trust{}
	for rows.Next() {
		t, err := scanTrust(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
