package store

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

func newReq(machine, id, cmd string) Request {
	return Request{ID: id, MachineID: machine, Kind: "permission", ToolName: "Bash",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: cmd, Cwd: "/p"},
		Risk:    "outward", ProjectRoot: "/p", State: "pending", TurnID: "t1", CLISessionID: "s1"}
}

func TestSessionUpsert(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	id1, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"}, "proj")
	if err != nil || id1 == "" {
		t.Fatal(err)
	}
	id2, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p/sub"}, "proj")
	if err != nil || id2 != id1 {
		t.Fatalf("同一会话应复用行: %s vs %s, %v", id1, id2, err)
	}
	id3, _ := s.UpsertSession(m.ID, proto.Session{Tool: "codex", CLISessionID: "s1"}, "proj")
	if id3 == id1 {
		t.Fatal("不同工具应是不同行")
	}
}

// UpsertSession 每次都刷新 last_event_at、cwd、project，started_at 不变；hub 用它记会话最近活动。
func TestSessionUpsertRefreshesLastEvent(t *testing.T) {
	s := openTemp(t)
	m, err := s.CreateMachine("m", RandomToken(), "/h")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"}, "proj")
	if err != nil {
		t.Fatal(err)
	}
	old := FormatTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := s.db.Exec("UPDATE sessions SET started_at = ?, last_event_at = ? WHERE id = ?", old, old, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p/sub"}, "proj2"); err != nil {
		t.Fatal(err)
	}
	var started, last, cwd, project string
	if err := s.db.QueryRow("SELECT started_at, last_event_at, cwd, project FROM sessions WHERE id = ?", id).Scan(&started, &last, &cwd, &project); err != nil {
		t.Fatal(err)
	}
	if started != old || !(last > old) || cwd != "/p/sub" || project != "proj2" {
		t.Fatalf("再次 upsert 后 started=%s last=%s cwd=%s project=%s", started, last, cwd, project)
	}
}

func TestRequestLifecycle(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	r := newReq(m.ID, "r1", "git push origin main")
	if err := s.InsertRequest(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRequest("r1")
	if err != nil || got.State != "pending" || got.Payload.Command != "git push origin main" || got.PayloadHash == "" || got.CreatedAt == "" {
		t.Fatalf("GetRequest = %+v, %v", got, err)
	}
	if n, _ := s.CountPending(); n != 1 {
		t.Fatalf("CountPending = %d", n)
	}
	pend, _ := s.ListPendingForMachine(m.ID)
	if len(pend) != 1 || pend[0].ID != "r1" {
		t.Fatalf("ListPendingForMachine = %+v", pend)
	}
	if err := s.SetRequestState("r1", "approved", "user", "ok", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRequestState("r1", "denied", "user", "", ""); !errors.Is(err, ErrNotPending) {
		t.Fatalf("二次决定应 ErrNotPending: %v", err)
	}
	got, _ = s.GetRequest("r1")
	if got.State != "approved" || got.DecidedBy != "user" || got.DecidedAt == nil || got.DeliveredAt != nil {
		t.Fatalf("决定后: %+v", got)
	}
	und, _ := s.UndeliveredDecisions(m.ID)
	if len(und) != 1 {
		t.Fatalf("未送达 = %+v", und)
	}
	if err := s.MarkDelivered(m.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if und, _ := s.UndeliveredDecisions(m.ID); len(und) != 0 {
		t.Fatal("送达后不应再列出")
	}
	all, _ := s.ListRequests("", 10)
	if len(all) != 1 || all[0].ID != "r1" {
		t.Fatalf("ListRequests = %+v", all)
	}
	if _, err := s.GetRequest("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestFindResend(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	first := newReq(m.ID, "r1", "touch a")
	s.InsertRequest(first)
	got, err := s.FindResend(m.ID, "s1", "t1", "Bash", PayloadHash(first.Payload), 15*time.Minute)
	if err != nil || got.ID != "r1" {
		t.Fatalf("应找到同键的 pending: %+v, %v", got, err)
	}
	if _, err := s.FindResend(m.ID, "s1", "t2", "Bash", PayloadHash(first.Payload), 15*time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不同 turn 不算重发: %v", err)
	}
	s.SetRequestState("r1", "cancelled", "resend", "", "")
	if _, err := s.FindResend(m.ID, "s1", "t1", "Bash", PayloadHash(first.Payload), 15*time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("非 pending 不算: %v", err)
	}
}

func TestListRequestsOrderAndLimit(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	for i, id := range []string{"a", "b", "c"} {
		r := newReq(m.ID, id, "cmd")
		r.CreatedAt = FormatTime(time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC))
		s.InsertRequest(r)
	}
	got, _ := s.ListRequests("pending", 2)
	if len(got) != 2 || got[0].ID != "c" || got[1].ID != "b" {
		t.Fatalf("应倒序取前两条: %+v", got)
	}
}

// 守护进程发来的 cancel/expired 帧只能改本机、仍 pending 的请求。
func TestSetMachineRequestState(t *testing.T) {
	s := openTemp(t)
	m1, _ := s.CreateMachine("m1", RandomToken(), "/h")
	m2, _ := s.CreateMachine("m2", RandomToken(), "/h")
	if err := s.InsertRequest(newReq(m1.ID, "r1", "touch a")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMachineRequestState(m2.ID, "r1", "cancelled", "agent", "", ""); !errors.Is(err, ErrNotPending) {
		t.Fatalf("别的机器的请求应 ErrNotPending: %v", err)
	}
	if got, err := s.GetRequest("r1"); err != nil || got.State != "pending" || got.DecidedAt != nil {
		t.Fatalf("别的机器不应改动: %+v, %v", got, err)
	}
	if err := s.SetMachineRequestState(m1.ID, "missing", "cancelled", "agent", "", ""); !errors.Is(err, ErrNotPending) {
		t.Fatalf("不存在的请求应 ErrNotPending: %v", err)
	}
	if err := s.SetMachineRequestState(m1.ID, "r1", "expired", "agent", "本机超时", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRequest("r1")
	if err != nil || got.State != "expired" || got.DecidedBy != "agent" || got.DecisionNote != "本机超时" || got.DecidedAt == nil {
		t.Fatalf("本机改动后: %+v, %v", got, err)
	}
	if err := s.SetMachineRequestState(m1.ID, "r1", "cancelled", "agent", "", ""); !errors.Is(err, ErrNotPending) {
		t.Fatalf("非 pending 应 ErrNotPending: %v", err)
	}
}

// 送达标记只认本机的请求；重复送达不覆盖第一次的时间。
func TestMarkDeliveredScopedAndOnce(t *testing.T) {
	s := openTemp(t)
	m1, _ := s.CreateMachine("m1", RandomToken(), "/h")
	m2, _ := s.CreateMachine("m2", RandomToken(), "/h")
	if err := s.InsertRequest(newReq(m1.ID, "r1", "touch a")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRequestState("r1", "approved", "user", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDelivered(m2.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetRequest("r1"); err != nil || got.DeliveredAt != nil {
		t.Fatalf("别的机器不能标送达: %+v, %v", got, err)
	}
	if und, err := s.UndeliveredDecisions(m1.ID); err != nil || len(und) != 1 {
		t.Fatalf("应仍未送达: %+v, %v", und, err)
	}
	if err := s.MarkDelivered(m1.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetRequest("r1"); err != nil || got.DeliveredAt == nil {
		t.Fatalf("本机送达后: %+v, %v", got, err)
	}
	// 把第一次送达时间换成固定的旧值，再送达一次：若被覆盖就会变成当前时间
	old := FormatTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	if _, err := s.db.Exec("UPDATE requests SET delivered_at = ? WHERE id = ?", old, "r1"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDelivered(m1.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetRequest("r1"); err != nil || got.DeliveredAt == nil || *got.DeliveredAt != old {
		t.Fatalf("重复送达不应覆盖第一次时间: %+v, %v", got, err)
	}
}

// 本机审计里的终态能覆盖 pending 与 cancelled/agent_restart，不能覆盖其他终态，也不能改别的机器的请求。
func TestApplyAgentFinal(t *testing.T) {
	s := openTemp(t)
	m1, _ := s.CreateMachine("m1", RandomToken(), "/h")
	m2, _ := s.CreateMachine("m2", RandomToken(), "/h")
	old := FormatTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	cases := []struct {
		state, decidedBy string
		want             bool
	}{
		{"pending", "", true},
		{"cancelled", "agent_restart", true},
		{"approved", "user", false},
		{"denied", "user", false},
		{"cancelled", "resend", false},
	}
	for i, c := range cases {
		id := fmt.Sprintf("r%d", i)
		if err := s.InsertRequest(newReq(m1.ID, id, "cmd "+id)); err != nil {
			t.Fatal(err)
		}
		if c.state != "pending" {
			if err := s.SetRequestState(id, c.state, c.decidedBy, "", ""); err != nil {
				t.Fatal(err)
			}
			// 决定时间换成固定旧值，好判断有没有被重写
			if _, err := s.db.Exec("UPDATE requests SET decided_at = ? WHERE id = ?", old, id); err != nil {
				t.Fatal(err)
			}
		}
		if ok, err := s.ApplyAgentFinal(m2.ID, id, "expired", "hub_unreachable", "", ""); err != nil || ok {
			t.Fatalf("%s/%s: 别的机器不应更新: %v, %v", c.state, c.decidedBy, ok, err)
		}
		ok, err := s.ApplyAgentFinal(m1.ID, id, "expired", "hub_unreachable", "本机超时", "")
		if err != nil || ok != c.want {
			t.Fatalf("%s/%s: ApplyAgentFinal = %v, %v，应为 %v", c.state, c.decidedBy, ok, err, c.want)
		}
		got, err := s.GetRequest(id)
		if err != nil {
			t.Fatal(err)
		}
		if c.want {
			if got.State != "expired" || got.DecidedBy != "hub_unreachable" || got.DecisionNote != "本机超时" || got.DecidedAt == nil || !(*got.DecidedAt > old) {
				t.Fatalf("%s/%s: 应被本机终态覆盖: %+v", c.state, c.decidedBy, got)
			}
		} else if got.State != c.state || got.DecidedBy != c.decidedBy || got.DecidedAt == nil || *got.DecidedAt != old {
			t.Fatalf("%s/%s: 不应被覆盖: %+v", c.state, c.decidedBy, got)
		}
	}
	if ok, err := s.ApplyAgentFinal(m1.ID, "missing", "expired", "hub_unreachable", "", ""); err != nil || ok {
		t.Fatalf("不存在的请求: %v, %v", ok, err)
	}
}

// hub 自己执行过期：pending 且 expires_at 不晚于 now（恰好等于也算），按创建时间升序。
func TestExpiredPending(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *string {
		v := FormatTime(now.Add(d))
		return &v
	}
	add := func(id string, created time.Duration, expires *string, state string) {
		t.Helper()
		r := newReq(m.ID, id, "cmd "+id)
		r.CreatedAt = FormatTime(now.Add(created))
		r.ExpiresAt = expires
		r.State = state
		if err := s.InsertRequest(r); err != nil {
			t.Fatal(err)
		}
	}
	add("late", -1*time.Minute, at(-time.Second), "pending")
	add("early", -2*time.Minute, at(-time.Minute), "pending")
	add("edge", -30*time.Second, at(0), "pending")                // 恰好等于 now
	add("future", -3*time.Minute, at(time.Nanosecond), "pending") // 还差 1 纳秒
	add("never", -4*time.Minute, nil, "pending")                  // 无过期时间
	add("decided", -5*time.Minute, at(-time.Hour), "approved")    // 已决定
	got, err := s.ExpiredPending(now.In(time.FixedZone("CST", 8*3600)))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if want := []string{"early", "late", "edge"}; !reflect.DeepEqual(ids, want) {
		t.Fatalf("ExpiredPending = %v，应为 %v", ids, want)
	}
}

// question/done 作为 recorded 行进 requests 表：载荷为空值、无过期时间，往返无损，不算待处理。
func TestRecordedEventRoundTrip(t *testing.T) {
	s := openTemp(t)
	m, err := s.CreateMachine("m", RandomToken(), "/h")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"}, "proj")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	q := Request{ID: "q1", MachineID: m.ID, SessionID: sid, Kind: "question", State: "recorded",
		Title: "要不要先跑迁移？", ProjectRoot: "/p", CLISessionID: "s1", CreatedAt: FormatTime(base)}
	d := Request{ID: "d1", MachineID: m.ID, SessionID: sid, Kind: "done", State: "recorded",
		Title: "完成", ProjectRoot: "/p", CLISessionID: "s1", CreatedAt: FormatTime(base.Add(time.Second))}
	p := newReq(m.ID, "p1", "touch a")
	p.CreatedAt = FormatTime(base.Add(2 * time.Second))
	for _, r := range []Request{q, d, p} {
		if err := s.InsertRequest(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetRequest("q1")
	if err != nil {
		t.Fatal(err)
	}
	want := q
	want.PayloadHash = PayloadHash(policy.Payload{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("往返不一致:\n got %+v\nwant %+v", got, want)
	}
	rec, err := s.ListRequests("recorded", 10)
	if err != nil || len(rec) != 2 || rec[0].ID != "d1" || rec[1].ID != "q1" || rec[0].ExpiresAt != nil || rec[0].Kind != "done" || rec[0].Title != "完成" {
		t.Fatalf("ListRequests(recorded) = %+v, %v", rec, err)
	}
	if all, err := s.ListRequests("", 10); err != nil || len(all) != 3 || all[0].ID != "p1" {
		t.Fatalf("ListRequests(全部) = %+v, %v", all, err)
	}
	if n, err := s.CountPending(); err != nil || n != 1 {
		t.Fatalf("CountPending = %d, %v，recorded 行不应计入", n, err)
	}
	if pend, err := s.ListPendingForMachine(m.ID); err != nil || len(pend) != 1 || pend[0].ID != "p1" {
		t.Fatalf("ListPendingForMachine = %+v, %v", pend, err)
	}
}
