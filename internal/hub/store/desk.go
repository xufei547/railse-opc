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

// 总台的查询，都要走索引（TestDeskQueriesUseIndexes 用 EXPLAIN QUERY PLAN 核对）：Store 只有一个连接，慢查询会堵住审批登记与决定落库。
// 用到的索引（schema.sql 与迁移 7）：requests_state_created（pending）、requests_window（24 小时窗口，覆盖索引）、
// requests_role_recent（每个角色最近几条）、requests_project_recent（项目最近一条）、sessions_recent（窗口内的会话）。
const (
	// deskCountsSQL：pending 与 24 小时窗口各走各的索引，UNION ALL 后再按 (机器, 角色) 聚合。
	deskCountsSQL = `SELECT machine_id, role, SUM(w), SUM(d), SUM(a), SUM(n) FROM (
		SELECT machine_id, role, 1 AS w, 0 AS d, 0 AS a, 0 AS n FROM requests
			WHERE state = 'pending' AND kind IN ('permission', 'ask')
		UNION ALL
		SELECT machine_id, role, 0, kind = 'permission' AND state IN ('denied', 'auto_denied'), kind = 'permission' AND state = 'auto_allowed', 1
			FROM requests WHERE created_at >= ?1 AND kind <> 'webhook')
		GROUP BY machine_id, role ORDER BY machine_id, role`
	deskSessionsSQL      = "SELECT machine_id, role, COUNT(*) FROM sessions WHERE last_event_at >= ? GROUP BY machine_id, role"
	recentPermissionsSQL = "SELECT " + requestCols + " FROM requests WHERE machine_id = ? AND role = ? AND kind = 'permission' ORDER BY created_at DESC, id DESC LIMIT ?"
	latestProjectRoleSQL = "SELECT role FROM requests WHERE machine_id = ? AND project_root = ? ORDER BY created_at DESC, id DESC LIMIT 1"
	sessionRoleByCLISQL  = "SELECT role FROM sessions WHERE machine_id = ? AND tool = ? AND cli_session_id = ?"
)

// DeskCounts 按 (机器, 角色) 计数，只回有 pending 或 since 之后有请求的组，按机器 id、角色名排。
func (s *Store) DeskCounts(since time.Time) ([]DeskCount, error) {
	rows, err := s.db.Query(deskCountsSQL, fmtTime(since))
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
	rows, err := s.db.Query(deskSessionsSQL, fmtTime(since))
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

// RecentPermissions 列一台机器上某个角色（空串为未分配）最近 n 条 permission（任意状态），新的在前。按索引顺序取，不排全部历史。
func (s *Store) RecentPermissions(machineID, role string, n int) ([]Request, error) {
	return s.queryRequests(recentPermissionsSQL, machineID, role, n)
}

// SessionRoleByCLI 取一台机器上某个 CLI 会话当前的角色；没有这个会话时 ok 为假。会话放行据此归到角色。
func (s *Store) SessionRoleByCLI(machineID, tool, cliSession string) (role string, ok bool, err error) {
	err = s.db.QueryRow(sessionRoleByCLISQL, machineID, tool, cliSession).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return role, err == nil, err
}

// LatestProjectRole 取一台机器上该项目根最近一条请求的角色；没有请求时 ok 为假。项目放行据此归到角色（hub 不做目录模式匹配）。
func (s *Store) LatestProjectRole(machineID, projectRoot string) (role string, ok bool, err error) {
	err = s.db.QueryRow(latestProjectRoleSQL, machineID, projectRoot).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return role, err == nil, err
}
