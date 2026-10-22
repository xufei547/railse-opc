package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
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

// UpsertSession 非空才覆盖 cwd、project；每次刷新 last_event_at，started_at 不变；hub 用它记会话最近活动。
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
	s.SetRequestState("r1", "cancelled", DecidedByResend, "", "")
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
		{"cancelled", DecidedByAgentRestart, true},
		{"approved", "user", false},
		{"denied", "user", false},
		{"cancelled", DecidedByResend, false},
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

// 去重键（机器、cli_session_id、turn_id、工具名、载荷哈希）任一不同都不算重发；窗口按 created_at 判定；同键取较新的。
func TestFindResendKeyAndWindow(t *testing.T) {
	s := openTemp(t)
	m1, _ := s.CreateMachine("m1", RandomToken(), "/h")
	m2, _ := s.CreateMachine("m2", RandomToken(), "/h")
	now := time.Now()
	add := func(id, sess, turn string, age time.Duration) {
		t.Helper()
		r := newReq(m1.ID, id, "touch a")
		r.CLISessionID = sess
		r.TurnID = turn
		r.CreatedAt = FormatTime(now.Add(-age))
		if err := s.InsertRequest(r); err != nil {
			t.Fatal(err)
		}
	}
	add("base", "s1", "t1", time.Minute)
	add("old", "s1", "t-old", 16*time.Minute)       // 窗口外
	add("recent", "s1", "t-recent", 14*time.Minute) // 窗口内
	add("dup-old", "s1", "t-dup", 10*time.Minute)
	add("dup-new", "s1", "t-dup", 5*time.Minute)
	add("noturn", "s1", "", time.Minute)
	add("nosess", "", "t-nosess", time.Minute)
	h := PayloadHash(newReq("", "", "touch a").Payload)
	other := PayloadHash(newReq("", "", "touch b").Payload)
	cases := []struct{ name, machine, sess, turn, tool, hash, want string }{
		{"同键命中", m1.ID, "s1", "t1", "Bash", h, "base"},
		{"别的机器", m2.ID, "s1", "t1", "Bash", h, ""},
		{"别的会话", m1.ID, "s2", "t1", "Bash", h, ""},
		{"别的回合", m1.ID, "s1", "t2", "Bash", h, ""},
		{"别的工具", m1.ID, "s1", "t1", "Write", h, ""},
		{"别的载荷", m1.ID, "s1", "t1", "Bash", other, ""},
		{"窗口外 16 分钟", m1.ID, "s1", "t-old", "Bash", h, ""},
		{"窗口内 14 分钟", m1.ID, "s1", "t-recent", "Bash", h, "recent"},
		{"同键两条取较新", m1.ID, "s1", "t-dup", "Bash", h, "dup-new"},
		{"turn_id 为空按空串匹配", m1.ID, "s1", "", "Bash", h, "noturn"},
		{"cli_session_id 为空不去重", m1.ID, "", "t-nosess", "Bash", h, ""},
	}
	for _, c := range cases {
		got, err := s.FindResend(c.machine, c.sess, c.turn, c.tool, c.hash, 15*time.Minute)
		if c.want == "" {
			if !errors.Is(err, ErrNotFound) {
				t.Errorf("%s: 应 ErrNotFound，得到 %+v, %v", c.name, got.ID, err)
			}
			continue
		}
		if err != nil || got.ID != c.want {
			t.Errorf("%s: 得到 %q, %v，应为 %q", c.name, got.ID, err, c.want)
		}
	}
}

// 守护进程来源的写入只能写 cancelled、expired、passthrough：不能迁回 pending，也不能写成 recorded、空状态，
// 或冒充用户与规则的决定（approved、denied、auto_*）。
func TestAgentWritesRejectNonFinalState(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	if err := s.InsertRequest(newReq(m.ID, "p", "touch a")); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertRequest(newReq(m.ID, "c", "touch b")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRequestState("c", "cancelled", DecidedByAgentRestart, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"pending", "recorded", "", "approved", "denied", "auto_allowed", "auto_denied", "weird"} {
		if err := s.SetMachineRequestState(m.ID, "p", bad, "agent", "", ""); !errors.Is(err, ErrInvalidState) {
			t.Errorf("SetMachineRequestState(%q) = %v，应 ErrInvalidState", bad, err)
		}
		for _, id := range []string{"p", "c"} {
			if ok, err := s.ApplyAgentFinal(m.ID, id, bad, "agent", "", ""); ok || !errors.Is(err, ErrInvalidState) {
				t.Errorf("ApplyAgentFinal(%s, %q) = %v, %v，应 ErrInvalidState", id, bad, ok, err)
			}
		}
	}
	if got, err := s.GetRequest("p"); err != nil || got.State != "pending" || got.DecidedBy != "" || got.DecidedAt != nil {
		t.Fatalf("pending 行不应变: %+v, %v", got, err)
	}
	if got, err := s.GetRequest("c"); err != nil || got.State != "cancelled" || got.DecidedBy != DecidedByAgentRestart {
		t.Fatalf("cancelled/agent_restart 行不应被复活: %+v, %v", got, err)
	}
}

// 送达只记已决定（approved、denied）的请求：对 pending 或其他终态的回执不写 delivered_at，之后的决定仍会补发。
func TestMarkDeliveredOnlyDecided(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	for _, id := range []string{"r1", "r2"} {
		if err := s.InsertRequest(newReq(m.ID, id, "cmd "+id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetRequestState("r2", "cancelled", DecidedByResend, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"r1", "r2"} {
		if err := s.MarkDelivered(m.ID, id); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetRequest(id); err != nil || got.DeliveredAt != nil {
			t.Fatalf("%s 未决定不应记送达: %+v, %v", id, got, err)
		}
	}
	if err := s.SetRequestState("r1", "denied", "user", "", ""); err != nil {
		t.Fatal(err)
	}
	if und, err := s.UndeliveredDecisions(m.ID); err != nil || len(und) != 1 || und[0].ID != "r1" {
		t.Fatalf("决定后应待补发: %+v, %v", und, err)
	}
	if err := s.MarkDelivered(m.ID, "r1"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetRequest("r1"); err != nil || got.DeliveredAt == nil {
		t.Fatalf("决定后应记送达: %+v, %v", got, err)
	}
}

// InsertRequest 存的哈希就是 PayloadHash；载荷无法序列化时插入报错、PayloadHash 返回空串。
func TestPayloadHashAndMarshalError(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	r := newReq(m.ID, "r1", "touch a")
	if err := s.InsertRequest(r); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetRequest("r1"); err != nil || len(got.PayloadHash) != 64 || got.PayloadHash != PayloadHash(r.Payload) {
		t.Fatalf("存的哈希 = %q, %v，应为 %q", got.PayloadHash, err, PayloadHash(r.Payload))
	}
	bad := newReq(m.ID, "r2", "x")
	bad.Payload = policy.Payload{Type: policy.PayloadMCP, Server: "s", Tool: "t", Args: json.RawMessage("{bad")}
	if h := PayloadHash(bad.Payload); h != "" {
		t.Fatalf("无法序列化的载荷 PayloadHash = %q，应为空串", h)
	}
	if err := s.InsertRequest(bad); err == nil {
		t.Fatal("无法序列化的载荷应插入失败")
	}
	if _, err := s.GetRequest("r2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("失败的插入不应留行: %v", err)
	}
}

// created_at、expires_at、decided_at、delivered_at 必须是定宽 UTC 格式（FormatTime 的输出），否则字典序比较会错。
func TestInsertRequestRejectsNonCanonicalTime(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	ts := time.Date(2026, 10, 22, 8, 0, 0, 120000000, time.UTC)
	bad := []string{
		ts.Format(time.RFC3339),
		ts.Format(time.RFC3339Nano),
		ts.In(time.FixedZone("CST", 8*3600)).Format(timeLayout), // 位数对但不是 UTC
		"yesterday",
	}
	for i, v := range bad {
		c := newReq(m.ID, fmt.Sprintf("c%d", i), "x")
		c.CreatedAt = v
		if err := s.InsertRequest(c); err == nil {
			t.Errorf("CreatedAt %q 应被拒绝", v)
		}
		e := newReq(m.ID, fmt.Sprintf("e%d", i), "x")
		e.ExpiresAt = &v
		if err := s.InsertRequest(e); err == nil {
			t.Errorf("ExpiresAt %q 应被拒绝", v)
		}
		d := newReq(m.ID, fmt.Sprintf("d%d", i), "x")
		d.DecidedAt = &v
		if err := s.InsertRequest(d); err == nil {
			t.Errorf("DecidedAt %q 应被拒绝", v)
		}
		l := newReq(m.ID, fmt.Sprintf("l%d", i), "x")
		l.DeliveredAt = &v
		if err := s.InsertRequest(l); err == nil {
			t.Errorf("DeliveredAt %q 应被拒绝", v)
		}
	}
	if all, err := s.ListRequests("", 100); err != nil || len(all) != 0 {
		t.Fatalf("被拒绝的插入不应留行: %d, %v", len(all), err)
	}
	ok := newReq(m.ID, "ok", "x")
	ok.CreatedAt = FormatTime(ts)
	exp := FormatTime(ts.Add(time.Minute))
	ok.ExpiresAt = &exp
	ok.DecidedAt = &exp
	ok.DeliveredAt = &exp
	if err := s.InsertRequest(ok); err != nil {
		t.Fatal(err)
	}
}

// 会话再次上报时 cwd、project 为空不覆盖已知值。
func TestSessionUpsertKeepsKnownCwdProject(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	id, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"}, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "s1"}, ""); err != nil {
		t.Fatal(err)
	}
	var cwd, project string
	if err := s.db.QueryRow("SELECT cwd, project FROM sessions WHERE id = ?", id).Scan(&cwd, &project); err != nil {
		t.Fatal(err)
	}
	if cwd != "/p" || project != "proj" {
		t.Fatalf("空值不应覆盖: cwd=%q project=%q", cwd, project)
	}
}

// Request 序列化为 snake_case；载荷哈希与 webhook 回调（含明文回调令牌）不出现在 JSON 里，广播与 API 不会外泄。
func TestRequestJSONHidesCallback(t *testing.T) {
	exp := FormatTime(time.Now())
	r := newReq("m", "r1", "ls")
	r.PayloadHash = "hash-value-xyz"
	r.Callback = `{"url":"https://example.test/cb","callback_token":"tok-secret-123"}`
	r.ExpiresAt = &exp
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"tok-secret-123", "callback", "Callback", "hash-value-xyz", "payload_hash", "PayloadHash"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("序列化结果不应含 %q: %s", bad, b)
		}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "machine_id", "session_id", "kind", "tool_name", "payload", "risk", "project_root", "engine_reason", "state",
		"decided_by", "decision_note", "rule_id", "turn_id", "cli_session_id", "title", "created_at", "expires_at", "decided_at", "delivered_at"}
	for _, k := range want {
		if _, ok := m[k]; !ok {
			t.Fatalf("缺字段 %s: %s", k, b)
		}
	}
	if len(m) != len(want) || m["expires_at"] != exp || m["machine_id"] != "m" {
		t.Fatalf("字段不符: %s", b)
	}
}

// InsertRequestIfAbsent：id 已存在时不插入、不报错、返回 false，原行不变；时间校验与 InsertRequest 相同。
func TestInsertRequestIfAbsent(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	r := newReq(m.ID, "e1", "first")
	ok, err := s.InsertRequestIfAbsent(r)
	if err != nil || !ok {
		t.Fatalf("首次应插入: %v, %v", ok, err)
	}
	ok, err = s.InsertRequestIfAbsent(newReq(m.ID, "e1", "second"))
	if err != nil || ok {
		t.Fatalf("已存在时应返回 false 且不报错: %v, %v", ok, err)
	}
	if got, _ := s.GetRequest("e1"); got.Payload.Command != "first" {
		t.Fatalf("原行不应变: %+v", got)
	}
	bad := newReq(m.ID, "e2", "x")
	bad.CreatedAt = "2026-10-22T00:00:00Z"
	if ok, err := s.InsertRequestIfAbsent(bad); ok || err == nil {
		t.Fatalf("非定宽时间应报错: %v, %v", ok, err)
	}
}
