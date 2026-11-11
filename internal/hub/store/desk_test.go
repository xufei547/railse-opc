package store

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// deskReq 插一条请求：ago 是距现在多久创建的。
func deskReq(t *testing.T, s *Store, id, machine, role, kind, state string, ago time.Duration) {
	t.Helper()
	r := Request{ID: id, MachineID: machine, Kind: kind, ToolName: "Bash", State: state, Role: role,
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "cmd " + id}, CreatedAt: FormatTime(time.Now().Add(-ago))}
	if err := s.InsertRequest(r); err != nil {
		t.Fatal(err)
	}
}

// DeskCounts 的口径：waiting 是 pending 的 permission 与 ask（不限时间）；denied 是 24 小时内 permission 的 denied + auto_denied；
// auto_allowed 是 24 小时内 permission 的 auto_allowed；requests 是 24 小时内的请求（任意种类）。webhook 不算。按 (机器, 角色) 分组。
func TestDeskCounts(t *testing.T) {
	s := openTemp(t)
	h := time.Hour
	deskReq(t, s, "p1", "m1", "写作", "permission", "pending", 30*h) // 等我不限 24 小时
	deskReq(t, s, "p2", "m1", "写作", "ask", "pending", 0)
	deskReq(t, s, "q1", "m1", "写作", "question", "recorded", 0) // 问题不算等我，但算 24 小时内的请求
	deskReq(t, s, "d1", "m1", "写作", "permission", "denied", h)
	deskReq(t, s, "d2", "m1", "写作", "permission", "auto_denied", 0)
	deskReq(t, s, "d3", "m1", "写作", "permission", "denied", 25*h) // 超出 24 小时
	deskReq(t, s, "a1", "m1", "写作", "permission", "auto_allowed", 0)
	deskReq(t, s, "a2", "m1", "写作", "permission", "auto_allowed", 23*h)
	deskReq(t, s, "a3", "m1", "写作", "permission", "auto_allowed", 25*h)
	deskReq(t, s, "x1", "m1", "写作", "permission", "approved", 0)
	deskReq(t, s, "u1", "m1", "", "permission", "auto_allowed", 0)
	deskReq(t, s, "o1", "m2", "", "done", "recorded", 26*h) // 只有 24 小时外的行：不出现（没有等我）
	deskReq(t, s, "w1", "webhook", "", "webhook", "pending", 0)
	got, err := s.DeskCounts(time.Now().Add(-24 * h))
	if err != nil {
		t.Fatal(err)
	}
	want := []DeskCount{
		{MachineID: "m1", Role: "", AutoAllowed: 1, Requests: 1},
		{MachineID: "m1", Role: "写作", Waiting: 2, Denied: 2, AutoAllowed: 2, Requests: 7},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("DeskCounts:\n%+v\n想要\n%+v", got, want)
	}
}

// DeskSessions：24 小时内有事件的会话数，按会话当前的角色。
func TestDeskSessions(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("mac", "sec", "")
	for _, c := range []struct{ cli, role string }{{"c1", "写作"}, {"c2", "写作"}, {"c3", "写作"}, {"c4", ""}} {
		if _, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: c.cli, Role: c.role}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("UPDATE sessions SET last_event_at = ? WHERE cli_session_id = 'c3'", FormatTime(time.Now().Add(-25*time.Hour))); err != nil {
		t.Fatal(err)
	}
	got, err := s.DeskSessions(time.Now().Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got[DeskKey{m.ID, "写作"}] != 2 || got[DeskKey{m.ID, ""}] != 1 || len(got) != 2 {
		t.Fatalf("DeskSessions: %v", got)
	}
}

// RecentPermissions：一台机器上某个角色最近 n 条 permission，新的在前；别的角色、别的机器、别的种类不算。
func TestRecentPermissions(t *testing.T) {
	s := openTemp(t)
	for i := range 7 {
		deskReq(t, s, "r"+string(rune('0'+i)), "m1", "写作", "permission", "approved", time.Duration(7-i)*time.Minute)
	}
	deskReq(t, s, "k1", "m1", "写作", "ask", "pending", 0)
	deskReq(t, s, "u1", "m1", "", "permission", "denied", 0)
	deskReq(t, s, "x1", "m2", "写作", "permission", "denied", 0)
	ids := func(rs []Request) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.ID)
		}
		return out
	}
	got, err := s.RecentPermissions("m1", "写作", 5)
	if err != nil || !slices.Equal(ids(got), []string{"r6", "r5", "r4", "r3", "r2"}) {
		t.Fatalf("写作: %v %v", ids(got), err)
	}
	if got, _ := s.RecentPermissions("m1", "", 5); !slices.Equal(ids(got), []string{"u1"}) {
		t.Fatalf("未分配: %v", ids(got))
	}
}

// 放行归属用的两个查询：会话放行按会话当前的角色，项目放行按该项目最近一条请求的角色。
func TestTrustRoleLookups(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("mac", "sec", "")
	if _, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "c1", Role: "开发"}, ""); err != nil {
		t.Fatal(err)
	}
	if role, ok, err := s.SessionRoleByCLI(m.ID, "claude", "c1"); err != nil || !ok || role != "开发" {
		t.Fatalf("会话角色: %q %v %v", role, ok, err)
	}
	if _, ok, err := s.SessionRoleByCLI(m.ID, "codex", "c1"); err != nil || ok {
		t.Fatalf("没有的会话: %v %v", ok, err)
	}
	for _, r := range []Request{
		{ID: "a", MachineID: m.ID, Kind: "permission", State: "approved", ProjectRoot: "/p", Role: "开发", CreatedAt: FormatTime(time.Now().Add(-time.Hour))},
		{ID: "b", MachineID: m.ID, Kind: "permission", State: "approved", ProjectRoot: "/p", Role: "写作", CreatedAt: FormatTime(time.Now())},
		{ID: "c", MachineID: "other", Kind: "permission", State: "approved", ProjectRoot: "/p", Role: "运维", CreatedAt: FormatTime(time.Now().Add(time.Minute))},
	} {
		if err := s.InsertRequest(r); err != nil {
			t.Fatal(err)
		}
	}
	if role, ok, err := s.LatestProjectRole(m.ID, "/p"); err != nil || !ok || role != "写作" {
		t.Fatalf("项目角色: %q %v %v", role, ok, err)
	}
	if _, ok, err := s.LatestProjectRole(m.ID, "/q"); err != nil || ok {
		t.Fatalf("没有请求的项目: %v %v", ok, err)
	}
}

// 总台的每条查询都要走索引：EXPLAIN QUERY PLAN 里不得出现对 requests、sessions 的全表扫（SCAN requests / SCAN sessions，
// 含“SCAN … USING … INDEX”这种整个索引扫一遍）。Store 只有一个连接，慢查询会堵住审批登记与决定落库。
func TestDeskQueriesUseIndexes(t *testing.T) {
	s := openTemp(t)
	cut := FormatTime(time.Now().Add(-24 * time.Hour))
	for _, q := range []struct {
		name    string
		sql     string
		args    []any
		limited bool
	}{
		{"计数", deskCountsSQL, []any{cut}, false},
		{"会话", deskSessionsSQL, []any{cut}, false},
		{"最近动作", recentPermissionsSQL, []any{"m1", "写作", 5}, true},
		{"项目最近的角色", latestProjectRoleSQL, []any{"m1", "/p"}, true},
		{"会话的角色", sessionRoleByCLISQL, []any{"m1", "claude", "c1"}, false},
	} {
		rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q.sql, q.args...)
		if err != nil {
			t.Fatalf("%s: %v", q.name, err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		for _, d := range plan {
			if strings.HasPrefix(d, "SCAN requests") || strings.HasPrefix(d, "SCAN sessions") {
				t.Errorf("%s 有全表扫: %q\n整个计划: %q", q.name, d, plan)
			}
			// 带 LIMIT 的“最近”查询要按索引顺序取，不能先把这台机器的全部历史排一遍
			if q.limited && strings.Contains(d, "TEMP B-TREE") {
				t.Errorf("%s 要临时排序（会读完这台机器的全部历史）: %q\n整个计划: %q", q.name, d, plan)
			}
		}
	}
}

// “24 小时内”含恰好 24 小时前那一刻（created_at >= since）：差 1 纳秒的不算。
func TestDeskWindowBoundary(t *testing.T) {
	s := openTemp(t)
	since := time.Now().Add(-24 * time.Hour).UTC().Truncate(time.Second)
	for _, r := range []Request{
		{ID: "at", MachineID: "m1", Kind: "permission", State: "denied", CreatedAt: FormatTime(since)},
		{ID: "before", MachineID: "m1", Kind: "permission", State: "denied", CreatedAt: FormatTime(since.Add(-time.Nanosecond))},
	} {
		if err := s.InsertRequest(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.DeskCounts(since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Denied != 1 || got[0].Requests != 1 {
		t.Fatalf("恰好 24 小时前的算、早 1 纳秒的不算: %+v", got)
	}
	m, _ := s.CreateMachine("mac", "sec", "")
	if _, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "c1"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE sessions SET last_event_at = ?", FormatTime(since)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.DeskSessions(since); n[DeskKey{m.ID, ""}] != 1 {
		t.Fatalf("恰好 24 小时前有事件的会话应算: %v", n)
	}
	if n, _ := s.DeskSessions(since.Add(time.Nanosecond)); len(n) != 0 {
		t.Fatalf("早于窗口的会话不算: %v", n)
	}
}

// 计数里 pending 那一支走状态索引（requests_state_created），不论多旧都查得到又不全表扫。
func TestDeskPendingBranchUsesStateIndex(t *testing.T) {
	s := openTemp(t)
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+deskCountsSQL, FormatTime(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var d string
		if err := rows.Scan(&id, &parent, &notused, &d); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, d)
	}
	if !slices.ContainsFunc(plan, func(d string) bool { return strings.Contains(d, "requests_state_created") }) {
		t.Fatalf("pending 那一支应走 requests_state_created: %q", plan)
	}
	if !slices.ContainsFunc(plan, func(d string) bool { return strings.Contains(d, "requests_window") }) {
		t.Fatalf("窗口那一支应走 requests_window: %q", plan)
	}
}
