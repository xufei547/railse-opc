package store

import (
	"database/sql"
	"errors"
	"time"
)

// 总台（0.3 起）的聚合查询。口径见规格第 6 节与 hub 的 desk.go；这里只按 (机器, 角色) 分组计数，
// 角色已从文件里删掉的并进未分配、哪些行出现，由 hub 组装时决定。

// DeskKey 是总台分组的键：机器 id 与角色名（空串为未分配）。
type DeskKey struct{ MachineID, Role string }

// DeskCount 是一个 (机器, 角色) 的计数：Waiting 是 pending 的 permission 与 ask（不限时间）；Denied 是 since 之后创建的
// permission 里 denied 与 auto_denied 的条数；AutoAllowed 是 since 之后创建的 permission 里 auto_allowed 的条数；
// Requests 是 since 之后创建的请求条数（任意种类，hub 据此决定“未分配”一行出不出现）。webhook 不算。
type DeskCount struct {
	MachineID, Role                        string
	Waiting, Denied, AutoAllowed, Requests int
}

// DeskCounts 按 (机器, 角色) 计数，只回有 pending 或 since 之后有请求的组，按机器 id、角色名排。
func (s *Store) DeskCounts(since time.Time) ([]DeskCount, error) {
	cut := fmtTime(since)
	rows, err := s.db.Query(`SELECT machine_id, role,
		SUM(state = 'pending' AND kind IN ('permission', 'ask')),
		SUM(kind = 'permission' AND state IN ('denied', 'auto_denied') AND created_at >= ?1),
		SUM(kind = 'permission' AND state = 'auto_allowed' AND created_at >= ?1),
		SUM(created_at >= ?1)
		FROM requests WHERE kind <> 'webhook' AND (state = 'pending' OR created_at >= ?1)
		GROUP BY machine_id, role ORDER BY machine_id, role`, cut)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeskCount
	for rows.Next() {
		var c DeskCount
		if err := rows.Scan(&c.MachineID, &c.Role, &c.Waiting, &c.Denied, &c.AutoAllowed, &c.Requests); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeskSessions 数 since 之后有事件（last_event_at）的会话，按 (机器, 会话当前的角色) 分组。
func (s *Store) DeskSessions(since time.Time) (map[DeskKey]int, error) {
	rows, err := s.db.Query("SELECT machine_id, role, COUNT(*) FROM sessions WHERE last_event_at >= ? GROUP BY machine_id, role", fmtTime(since))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[DeskKey]int{}
	for rows.Next() {
		var k DeskKey
		var n int
		if err := rows.Scan(&k.MachineID, &k.Role, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// DeskRecent 列每个 (机器, 角色) 最近 n 条 permission（任意状态），按机器 id、角色名、创建时间倒序排。
func (s *Store) DeskRecent(n int) ([]Request, error) {
	return s.queryRequests(`SELECT `+requestCols+` FROM (
		SELECT *, ROW_NUMBER() OVER (PARTITION BY machine_id, role ORDER BY created_at DESC, id DESC) AS rn
		FROM requests WHERE kind = 'permission')
		WHERE rn <= ? ORDER BY machine_id, role, created_at DESC, id DESC`, n)
}

// SessionRoleByCLI 取一台机器上某个 CLI 会话当前的角色；没有这个会话时 ok 为假。会话放行据此归到角色。
func (s *Store) SessionRoleByCLI(machineID, tool, cliSession string) (role string, ok bool, err error) {
	err = s.db.QueryRow("SELECT role FROM sessions WHERE machine_id = ? AND tool = ? AND cli_session_id = ?", machineID, tool, cliSession).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return role, err == nil, err
}

// LatestProjectRole 取一台机器上该项目根最近一条请求的角色；没有请求时 ok 为假。项目放行据此归到角色（hub 不做目录模式匹配）。
func (s *Store) LatestProjectRole(machineID, projectRoot string) (role string, ok bool, err error) {
	err = s.db.QueryRow("SELECT role FROM requests WHERE machine_id = ? AND project_root = ? ORDER BY created_at DESC, id DESC LIMIT 1",
		machineID, projectRoot).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return role, err == nil, err
}
