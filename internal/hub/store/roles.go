package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xufei547/railse-opc/internal/proto"
)

// MachineRole 是 machine_roles 的一行：某台机器 roles.yaml 里一个角色的摘要（只展示，hub 不拿它判定）。
type MachineRole struct {
	MachineID string
	proto.RoleSummary
}

// SessionRole 返回会话行当前的角色名（空串为未分配）；没有这一行时返回 ErrNotFound。
func (s *Store) SessionRole(id string) (string, error) {
	var role string
	err := s.db.QueryRow("SELECT role FROM sessions WHERE id = ?", id).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

// ReplaceMachineRoles 在一个事务里整份替换一台机器的角色表：先删光，再按 roles 的先后（position 从 0 起）插入。
// roles 为空即清空。名字重复会撞主键报错，调用方先去重（hub 的 cleanRoles）。
func (s *Store) ReplaceMachineRoles(machineID string, roles []proto.RoleSummary) error {
	return s.Tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM machine_roles WHERE machine_id = ?", machineID); err != nil {
			return err
		}
		for i, r := range roles {
			dirs := r.Dirs
			if dirs == nil {
				dirs = []string{}
			}
			b, err := json.Marshal(dirs)
			if err != nil {
				return fmt.Errorf("序列化角色 %q 的目录: %w", r.Name, err)
			}
			if _, err := tx.Exec(`INSERT INTO machine_roles(machine_id, position, name, note, ceiling, forbid_count, trust_max_mins, dirs)
				VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, machineID, i, r.Name, r.Note, r.Ceiling, r.ForbidCount, r.TrustMaxMins, string(b)); err != nil {
				return fmt.Errorf("写角色 %q: %w", r.Name, err)
			}
		}
		return nil
	})
}

// ListMachineRoles 列全部机器的角色表，按机器 id、文件里的先后排。没有目录的角色 Dirs 为 nil。
func (s *Store) ListMachineRoles() ([]MachineRole, error) {
	rows, err := s.db.Query(`SELECT machine_id, name, note, ceiling, forbid_count, trust_max_mins, dirs FROM machine_roles
		ORDER BY machine_id, position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MachineRole
	for rows.Next() {
		var r MachineRole
		var dirs string
		if err := rows.Scan(&r.MachineID, &r.Name, &r.Note, &r.Ceiling, &r.ForbidCount, &r.TrustMaxMins, &dirs); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(dirs), &r.Dirs); err != nil {
			return nil, fmt.Errorf("解析角色 %q 的目录: %w", r.Name, err)
		}
		if len(r.Dirs) == 0 {
			r.Dirs = nil
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
