package hub

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func pendingFrame(id, session, turn, cmd string) wire.PendingRequest {
	return wire.PendingRequest{T: wire.TPending, RequestID: id,
		Session:  proto.Session{Tool: "claude", CLISessionID: session, TurnID: turn, Cwd: "/p"},
		ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: cmd, Cwd: "/p"},
		Risk: "outward", ProjectRoot: "/p", EngineReason: "无规则命中", DeadlineSeconds: 86390, CreatedAt: time.Now().UTC()}
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }

// nextBroadcast 读订阅通道的下一帧，5 秒超时。
func nextBroadcast(t *testing.T, sub chan []byte) []byte {
	t.Helper()
	select {
	case b := <-sub:
		return b
	case <-time.After(5 * time.Second):
		t.Fatal("等待广播超时")
		return nil
	}
}

// clientFrame 是广播给网页的帧。
type clientFrame struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

// waitBroadcast 一直读到 data.id 为 id 的 kind 帧并返回它的 data；skip 非空时，期间每个跳过的帧都交给它检查。
func waitBroadcast(t *testing.T, sub chan []byte, kind, id string, skip func(clientFrame)) map[string]any {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case b := <-sub:
			var f clientFrame
			if err := json.Unmarshal(b, &f); err != nil {
				t.Fatalf("广播帧不是 JSON: %s", b)
			}
			if f.Type == kind && f.Data["id"] == id {
				return f.Data
			}
			if skip != nil {
				skip(f)
			}
		case <-deadline:
			t.Fatalf("等待广播 %s %s 超时", kind, id)
			return nil
		}
	}
}

// waitDelivered 一直读到 data.id 为 id、delivered_at 非空的 request_updated 帧并返回它的 data（之前的帧跳过）。
func waitDelivered(t *testing.T, sub chan []byte, id string) map[string]any {
	t.Helper()
	for {
		if f := waitBroadcast(t, sub, "request_updated", id, nil); f["delivered_at"] != nil {
			return f
		}
	}
}

// wantView 要求推送的 data 是请求视图（与 /api/inbox 同形状）：带 machine_name、project、cli、has_redacted。
func wantView(t *testing.T, what string, f map[string]any, machineName string) {
	t.Helper()
	_, hasProject := f["project"]
	_, hasCLI := f["cli"]
	_, hasRedacted := f["has_redacted"]
	if f["machine_name"] != machineName || !hasProject || !hasCLI || !hasRedacted {
		t.Fatalf("%s 的推送应是请求视图（machine_name %q、project、cli、has_redacted）: %+v", what, machineName, f)
	}
}

// quietHub 起一个 hub 并返回它的日志缓冲，用于要断言日志的用例。
func quietHub(t *testing.T) (*testHub, *syncBuf) {
	t.Helper()
	h := newTestHub(t)
	return h, h.logs
}

// insertReq 直接往库里插一条请求。
func insertReq(t *testing.T, st *store.Store, r store.Request) {
	t.Helper()
	if r.Kind == "" {
		r.Kind = "permission"
	}
	if r.Payload.Type == "" {
		r.Payload = policy.Payload{Type: policy.PayloadShell, Command: "echo " + r.ID}
	}
	if r.State != "pending" && r.State != "recorded" && r.DecidedAt == nil {
		now := store.Now()
		r.DecidedAt = &now
	}
	if err := st.InsertRequest(r); err != nil {
		t.Fatal(err)
	}
}

// execDB 用另一条连接对 hub 的库执行 SQL（测试里装触发器制造写入失败）。
func execDB(t *testing.T, h *testHub, stmt string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(h.dir, "hub.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatal(err)
	}
}

func getReq(t *testing.T, st *store.Store, id string) store.Request {
	t.Helper()
	r, err := st.GetRequest(id)
	if err != nil {
		t.Fatalf("取请求 %s: %v", id, err)
	}
	return r
}

func auditActions(t *testing.T, st *store.Store, requestID string) []string {
	t.Helper()
	events, err := st.ListAudit(0, requestID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		out = append(out, e.Action)
	}
	return out
}

func countOf(xs []string, x string) int {
	n := 0
	for _, v := range xs {
		if v == x {
			n++
		}
	}
	return n
}

func ruleHits(t *testing.T, st *store.Store, id string) int {
	t.Helper()
	rules, err := st.ListRules()
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range rules {
		if x.ID == id {
			return x.HitCount
		}
	}
	t.Fatalf("没有规则 %s", id)
	return 0
}

func auditFrame(t *testing.T, entries ...any) wire.Audit {
	t.Helper()
	a := wire.Audit{T: wire.TAudit}
	for _, e := range entries {
		if raw, ok := e.(json.RawMessage); ok {
			a.Entries = append(a.Entries, raw)
			continue
		}
		a.Entries = append(a.Entries, mustJSON(t, e))
	}
	return a
}

func TestPendingThenDecideDeliversToAgent(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second) // welcome
	sub, stop := h.srv.clients.subscribe()
	defer stop()

	c.send(pendingFrame("r1", "s1", "t1", "git push origin main"))
	waitFor(t, "r1 登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	if msg := nextBroadcast(t, sub); !contains(msg, `"type":"request_new"`) || !contains(msg, "r1") {
		t.Fatalf("应广播 request_new: %s", msg)
	}
	req, _ := h.st.GetRequest("r1")
	if req.MachineID != id || req.State != "pending" || req.SessionID == "" || req.ExpiresAt == nil {
		t.Fatalf("登记的请求: %+v", req)
	}

	got, err := h.srv.Decide(DecideInput{RequestID: "r1", Action: "allow", Note: "ok"})
	if err != nil || got.State != "approved" || got.DecidedBy != "user" {
		t.Fatalf("Decide = %+v, %v", got, err)
	}
	kind, b := c.recv(5 * time.Second)
	var d wire.Decision
	wire.Unmarshal(b, &d)
	if kind != wire.TDecision || d.RequestID != "r1" || d.Action != "allow" || d.DecidedBy != "user" || d.Reason != "ok" {
		t.Fatalf("守护进程应收到决定: %s %+v", kind, d)
	}
	if f := waitBroadcast(t, sub, "request_updated", "r1", nil); f["state"] != "approved" {
		t.Fatalf("应广播决定后的请求: %+v", f)
	}
	c.send(wire.Delivered{T: wire.TDelivered, RequestID: "r1"})
	waitFor(t, "r1 送达", func() bool { r, _ := h.st.GetRequest("r1"); return r.DeliveredAt != nil })
	if _, err := h.srv.Decide(DecideInput{RequestID: "r1", Action: "deny"}); !errors.Is(err, store.ErrNotPending) {
		t.Fatalf("二次决定应 ErrNotPending: %v", err)
	}
	if _, err := h.srv.Decide(DecideInput{RequestID: "r1", Action: "maybe"}); !errors.Is(err, errBadAction) {
		t.Fatalf("坏 action: %v", err)
	}
	if _, err := h.srv.Decide(DecideInput{RequestID: "nope", Action: "allow"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在: %v", err)
	}
	if acts := auditActions(t, h.st, "r1"); countOf(acts, "request_pending") != 1 || countOf(acts, "allow") != 1 {
		t.Fatalf("审计应有登记与决定: %v", acts)
	}
}

func TestResendCancelsOld(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "touch a"))
	waitFor(t, "r1 登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	c.send(pendingFrame("r2", "s1", "t1", "touch a"))
	waitFor(t, "r2 登记", func() bool { r, _ := h.st.GetRequest("r2"); return r.State == "pending" })
	old, _ := h.st.GetRequest("r1")
	if old.State != "cancelled" || old.DecidedBy != store.DecidedByResend {
		t.Fatalf("旧请求应被标 resend: %+v", old)
	}
	if n, _ := h.st.CountPending(); n != 1 {
		t.Fatalf("pending 应只剩一条, 得到 %d", n)
	}
	if acts := auditActions(t, h.st, "r1"); countOf(acts, "cancelled_resend") != 1 {
		t.Fatalf("旧请求应记 cancelled_resend: %v", acts)
	}
}

// 同 id 重发只广播不重复插入；cancel 的原因只认 terminal、shutdown，其余记 terminal；expired 记 timeout。
func TestCancelExpiredAndDuplicatePending(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	c.send(pendingFrame("r1", "s1", "t1", "a"))
	c.send(pendingFrame("r2", "s2", "t2", "b"))
	c.send(pendingFrame("r3", "s3", "t3", "c"))
	waitFor(t, "三条 pending", func() bool { n, _ := h.st.CountPending(); return n == 3 })
	before := getReq(t, h.st, "r1")
	c.send(pendingFrame("r1", "s1", "t1", "a")) // 重连后重发同 id
	if f := waitBroadcast(t, sub, "request_updated", "r1", nil); f["state"] != "pending" {
		t.Fatalf("重发同 id 应广播现有的行: %+v", f)
	}
	if n, _ := h.st.CountPending(); n != 3 {
		t.Fatalf("同 id 重发不应重复插入, 得到 %d", n)
	}
	if after := getReq(t, h.st, "r1"); after.CreatedAt != before.CreatedAt || countOf(auditActions(t, h.st, "r1"), "request_pending") != 1 {
		t.Fatalf("同 id 重发不应改动现有的行: %+v", after)
	}
	c.send(wire.Cancel{T: wire.TCancel, RequestID: "r1", Reason: "rm -rf /"})
	c.send(wire.Expired{T: wire.TExpired, RequestID: "r2"})
	c.send(wire.Cancel{T: wire.TCancel, RequestID: "r3", Reason: "shutdown"})
	waitFor(t, "全部结束", func() bool { n, _ := h.st.CountPending(); return n == 0 })
	r1, r2, r3 := getReq(t, h.st, "r1"), getReq(t, h.st, "r2"), getReq(t, h.st, "r3")
	if r1.State != "cancelled" || r1.DecidedBy != "terminal" || r2.State != "expired" || r2.DecidedBy != "timeout" ||
		r3.State != "cancelled" || r3.DecidedBy != "shutdown" {
		t.Fatalf("r1=%+v r2=%+v r3=%+v", r1, r2, r3)
	}
	waitBroadcast(t, sub, "request_updated", "r3", nil)
}

// 守护进程只能动自己机器的请求：别的机器的同 id 待决请求忽略（不广播），cancel、expired、delivered 不生效。
func TestAgentFramesScopedToMachine(t *testing.T) {
	h, logs := quietHub(t)
	idA, secretA := enrollMachine(t, h, "a")
	idB, secretB := enrollMachine(t, h, "b")
	a := dialAgent(t, h, secretA, wire.Hello{Proto: wire.Proto, MachineID: idA})
	defer a.close()
	a.recv(5 * time.Second)
	b := dialAgent(t, h, secretB, wire.Hello{Proto: wire.Proto, MachineID: idB})
	defer b.close()
	b.recv(5 * time.Second)
	sub, stop := h.srv.clients.subscribe()
	defer stop()

	a.send(pendingFrame("r1", "s1", "t1", "a"))
	a.send(pendingFrame("r2", "s2", "t2", "b"))
	waitFor(t, "a 的两条 pending", func() bool { n, _ := h.st.CountPending(); return n == 2 })
	if _, err := h.srv.Decide(DecideInput{RequestID: "r2", Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	b.send(pendingFrame("r1", "sx", "tx", "evil"))
	b.send(wire.Cancel{T: wire.TCancel, RequestID: "r1", Reason: "terminal"})
	b.send(wire.Expired{T: wire.TExpired, RequestID: "r1"})
	b.send(wire.Delivered{T: wire.TDelivered, RequestID: "r2"})
	b.send(pendingFrame("rb", "sb", "tb", "ok")) // 同一连接按序处理：它登记了，前面几帧也处理完了
	waitBroadcast(t, sub, "request_new", "rb", func(f clientFrame) {
		if f.Data["id"] == "r1" && f.Data["machine_id"] != idA {
			t.Errorf("不应广播别的机器的同 id 请求: %+v", f)
		}
		if f.Type == "request_updated" && f.Data["id"] == "r1" {
			t.Errorf("机器 b 的帧不应改动 r1: %+v", f)
		}
	})
	kind, raw := b.recv(5 * time.Second)
	var e wire.Error
	wire.Unmarshal(raw, &e)
	if kind != wire.TError || e.RequestID != "r1" {
		t.Fatalf("别的机器的同 id 应回 Error: %s %+v", kind, e)
	}
	r1, r2 := getReq(t, h.st, "r1"), getReq(t, h.st, "r2")
	if r1.MachineID != idA || r1.State != "pending" || r1.Payload.Command != "a" {
		t.Fatalf("r1 不应被机器 b 改动: %+v", r1)
	}
	if r2.DeliveredAt != nil {
		t.Fatalf("机器 b 的送达回执不应记到 a 的请求上: %+v", r2)
	}
	if !strings.Contains(logs.String(), "同 id") {
		t.Fatalf("应记日志: %q", logs.String())
	}
}

func TestResyncOnReconnect(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "a"))
	c.send(pendingFrame("r2", "s2", "t2", "b"))
	waitFor(t, "两条 pending", func() bool { n, _ := h.st.CountPending(); return n == 2 })
	c.close()
	waitFor(t, "断开", func() bool { return !h.srv.agents.connected(id) })
	// 断线期间用户决定了 r1
	if _, err := h.srv.Decide(DecideInput{RequestID: "r1", Action: "deny", Note: "no"}); err != nil {
		t.Fatal(err)
	}
	// 重连：守护进程只还持有 r1，r2 已经随钩子消失
	c2 := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, Pending: []wire.PendingSummary{{RequestID: "r1", CreatedAt: time.Now()}}})
	defer c2.close()
	c2.recv(5 * time.Second) // welcome
	kind, b := c2.recv(5 * time.Second)
	var d wire.Decision
	wire.Unmarshal(b, &d)
	if kind != wire.TDecision || d.RequestID != "r1" || d.Action != "deny" || d.Reason != "no" {
		t.Fatalf("重连后应补发 r1 的决定: %s %+v", kind, d)
	}
	r2, _ := h.st.GetRequest("r2")
	if r2.State != "cancelled" || r2.DecidedBy != store.DecidedByAgentRestart {
		t.Fatalf("守护进程不再持有的 pending 应被取消: %+v", r2)
	}
	if r1 := getReq(t, h.st, "r1"); r1.DeliveredAt != nil {
		t.Fatalf("补发入队不等于送达: %+v", r1)
	}
}

// 重连对齐：hub 多出的 pending 取消；已决定未送达的只补发守护进程仍持有的；不再持有的记一次 undeliverable 并广播，
// 不标送达；再次对齐不重复记。
func TestAgentHelloAlign(t *testing.T) {
	h := newTestHub(t)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/h")
	if err != nil {
		t.Fatal(err)
	}
	other, _ := h.st.CreateMachine("o", store.RandomToken(), "/h")
	delivered := store.Now()
	insertReq(t, h.st, store.Request{ID: "p1", MachineID: m.ID, State: "pending"})
	insertReq(t, h.st, store.Request{ID: "p2", MachineID: m.ID, State: "pending"})
	insertReq(t, h.st, store.Request{ID: "d1", MachineID: m.ID, State: "approved", DecidedBy: "user", DecisionNote: "好"})
	insertReq(t, h.st, store.Request{ID: "d2", MachineID: m.ID, State: "denied", DecidedBy: "user"})
	insertReq(t, h.st, store.Request{ID: "d3", MachineID: m.ID, State: "approved", DecidedBy: "user", DeliveredAt: &delivered})
	insertReq(t, h.st, store.Request{ID: "o1", MachineID: other.ID, State: "pending"})
	// 守护进程自己报的、或挂在别的机器上的 undeliverable 行不算数
	for _, forged := range []store.AuditEvent{{MachineID: m.ID, Actor: "agent", RequestID: "d2", Action: "undeliverable"},
		{MachineID: other.ID, Actor: "system", RequestID: "d2", Action: "undeliverable"}} {
		if err := h.st.InsertAudit(forged); err != nil {
			t.Fatal(err)
		}
	}
	c := newFakeConn(m.ID, 16)
	h.srv.agents.register(c)
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	hello := wire.Hello{T: wire.THello, Proto: wire.Proto, MachineID: m.ID, Pending: []wire.PendingSummary{{RequestID: "p1"}, {RequestID: "d1"}, {RequestID: "d3"}, {RequestID: "o1"}}}

	h.srv.onAgentHello(c, hello)
	if p1, p2 := getReq(t, h.st, "p1"), getReq(t, h.st, "p2"); p1.State != "pending" || p2.State != "cancelled" || p2.DecidedBy != store.DecidedByAgentRestart {
		t.Fatalf("p1=%+v p2=%+v", p1, p2)
	}
	if o1 := getReq(t, h.st, "o1"); o1.State != "pending" {
		t.Fatalf("别的机器的请求不受影响: %+v", o1)
	}
	if len(c.out) != 1 {
		t.Fatalf("只应补发 d1, 队列 %d 帧", len(c.out))
	}
	var d wire.Decision
	if err := wire.Unmarshal(<-c.out, &d); err != nil || d.T != wire.TDecision || d.RequestID != "d1" || d.Action != "allow" || d.Reason != "好" || d.DecidedBy != "user" {
		t.Fatalf("补发的决定: %+v, %v", d, err)
	}
	if n := systemUndeliverable(t, h.st, m.ID, "d2"); n != 1 {
		t.Fatalf("d2 应记一次 undeliverable, 得到 %d", n)
	}
	if d2 := getReq(t, h.st, "d2"); d2.DeliveredAt != nil || d2.State != "denied" {
		t.Fatalf("undeliverable 不标送达: %+v", d2)
	}
	waitBroadcast(t, sub, "request_updated", "d2", nil)
	if acts := auditActions(t, h.st, "d1"); countOf(acts, "undeliverable") != 0 {
		t.Fatalf("仍持有的不记 undeliverable: %v", acts)
	}

	h.srv.onAgentHello(c, hello)
	if len(c.out) != 1 {
		t.Fatalf("再次对齐仍只补发 d1, 队列 %d 帧", len(c.out))
	}
	if n := systemUndeliverable(t, h.st, m.ID, "d2"); n != 1 {
		t.Fatalf("undeliverable 同一请求只记一次, 得到 %d", n)
	}
}

// systemUndeliverable 数 hub 自己（actor system、该机器）给请求记的 undeliverable。
func systemUndeliverable(t *testing.T, st *store.Store, machineID, id string) int {
	t.Helper()
	events, err := st.ListAudit(0, id)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range events {
		if e.Action == "undeliverable" && e.Actor == "system" && e.MachineID == machineID {
			n++
		}
	}
	return n
}

// 旧连接（已被替换或已断开）的对齐不执行，不会覆盖新连接的对齐结果。
func TestAgentHelloSkipsStaleConn(t *testing.T) {
	h := newTestHub(t)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	insertReq(t, h.st, store.Request{ID: "p1", MachineID: m.ID, State: "pending"})
	insertReq(t, h.st, store.Request{ID: "d1", MachineID: m.ID, State: "approved", DecidedBy: "user"})
	old, cur := newFakeConn(m.ID, 16), newFakeConn(m.ID, 16)
	h.srv.agents.register(old)
	h.srv.agents.register(cur) // 替换并关掉 old
	h.srv.onAgentHello(old, wire.Hello{T: wire.THello, MachineID: m.ID, Pending: []wire.PendingSummary{{RequestID: "d1"}}})
	if p1 := getReq(t, h.st, "p1"); p1.State != "pending" {
		t.Fatalf("旧连接的对齐不应取消请求: %+v", p1)
	}
	if len(old.out)+len(cur.out) != 0 {
		t.Fatal("旧连接的对齐不应补发")
	}
	unregistered := newFakeConn(m.ID, 16)
	h.srv.onAgentHello(unregistered, wire.Hello{T: wire.THello, MachineID: m.ID})
	if p1 := getReq(t, h.st, "p1"); p1.State != "pending" {
		t.Fatalf("不是当前连接的对齐不应执行: %+v", p1)
	}
}

// 补发数有上限：超出的留到下次对齐，记日志。
func TestAgentHelloResendCapped(t *testing.T) {
	h, logs := quietHub(t)
	old := helloResendMax
	helloResendMax = 2
	t.Cleanup(func() { helloResendMax = old })
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	var held []wire.PendingSummary
	for i := range 3 {
		id := fmt.Sprintf("d%d", i)
		insertReq(t, h.st, store.Request{ID: id, MachineID: m.ID, State: "approved", DecidedBy: "user"})
		held = append(held, wire.PendingSummary{RequestID: id})
	}
	c := newFakeConn(m.ID, 16)
	h.srv.agents.register(c)
	h.srv.onAgentHello(c, wire.Hello{T: wire.THello, MachineID: m.ID, Pending: held})
	if len(c.out) != 2 {
		t.Fatalf("应只补发 2 条, 得到 %d", len(c.out))
	}
	if !strings.Contains(logs.String(), "补发") {
		t.Fatalf("超出上限应记日志: %q", logs.String())
	}
}

func TestAuditIngestCreatesHistoryRows(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	p := policy.Payload{Type: policy.PayloadShell, Command: "ls", Cwd: "/p"}
	e1, _ := json.Marshal(agent.AuditEntry{TS: time.Now(), RequestID: "auto1", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"}, ToolName: "Bash", Payload: &p, Risk: "read", Action: "allow", State: "auto_allowed", RuleID: "builtin-readonly", DecidedBy: "rule:builtin-readonly"})
	e2, _ := json.Marshal(agent.AuditEntry{TS: time.Now(), Kind: "done", Session: proto.Session{Tool: "claude", CLISessionID: "s1"}, State: "recorded", Message: "DONE"})
	c.send(wire.Audit{T: wire.TAudit, Entries: []json.RawMessage{e1, e2}})
	waitFor(t, "历史行", func() bool { _, err := h.st.GetRequest("auto1"); return err == nil })
	r, _ := h.st.GetRequest("auto1")
	if r.State != "auto_allowed" || r.RuleID != "builtin-readonly" || r.Payload.Command != "ls" || r.MachineID != id || r.SessionID == "" || r.DecidedAt == nil {
		t.Fatalf("历史行: %+v", r)
	}
	if hits := ruleHits(t, h.st, "builtin-readonly"); hits != 1 {
		t.Fatalf("应记一次命中, 得到 %d", hits)
	}
	waitFor(t, "两条审计", func() bool { events, _ := h.st.ListAudit(10, ""); return len(events) >= 2 })
}

// 本机终态覆盖：hello 把不再持有的 pending 标 cancelled/agent_restart，随后到达的审计里的本机终态把它改正；
// 已被用户决定的不覆盖；白名单外的状态不改 hub；已有的 id 不再补 auto_* 历史行，也不记命中。
func TestAuditAgentFinalAfterHello(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "a"))
	c.send(pendingFrame("r2", "s2", "t2", "b"))
	c.send(pendingFrame("r3", "s3", "t3", "c"))
	waitFor(t, "三条 pending", func() bool { n, _ := h.st.CountPending(); return n == 3 })
	if _, err := h.srv.Decide(DecideInput{RequestID: "r3", Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	c.close()
	waitFor(t, "断开", func() bool { return !h.srv.agents.connected(id) })

	c2 := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c2.close()
	c2.recv(5 * time.Second)
	waitFor(t, "对齐取消 r1、r2", func() bool { n, _ := h.st.CountPending(); return n == 0 })
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	sess := proto.Session{Tool: "claude", CLISessionID: "s1"}
	c2.send(auditFrame(t,
		agent.AuditEntry{TS: time.Now(), RequestID: "r2", Kind: "permission", Session: sess, State: "approved", Action: "allow", DecidedBy: "hub"},
		agent.AuditEntry{TS: time.Now(), RequestID: "r3", Kind: "permission", Session: sess, State: "cancelled", DecidedBy: "terminal"},
		agent.AuditEntry{TS: time.Now(), RequestID: "r4", Kind: "permission", Session: sess, State: "approved", DecidedBy: "hub"},
		agent.AuditEntry{TS: time.Now(), RequestID: "r2", Kind: "permission", Session: sess, State: "auto_allowed", RuleID: "builtin-readonly"},
		agent.AuditEntry{TS: time.Now(), RequestID: "r1", Kind: "permission", Session: sess, State: "expired", DecidedBy: "hub_unreachable", Reason: "hub 不通，交回终端"},
	))
	waitFor(t, "r1 被本机终态改正", func() bool { r, _ := h.st.GetRequest("r1"); return r.State == "expired" })
	if r1 := getReq(t, h.st, "r1"); r1.DecidedBy != "hub_unreachable" || r1.DecisionNote != "hub 不通，交回终端" {
		t.Fatalf("r1 = %+v", r1)
	}
	waitBroadcast(t, sub, "request_updated", "r1", nil)
	if r2 := getReq(t, h.st, "r2"); r2.State != "cancelled" || r2.DecidedBy != store.DecidedByAgentRestart {
		t.Fatalf("白名单外的状态不应改 hub: %+v", r2)
	}
	if r3 := getReq(t, h.st, "r3"); r3.State != "approved" || r3.DecidedBy != "user" {
		t.Fatalf("用户的决定不应被覆盖: %+v", r3)
	}
	if _, err := h.st.GetRequest("r4"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("approved 不在补行白名单里: %v", err)
	}
	if hits := ruleHits(t, h.st, "builtin-readonly"); hits != 0 {
		t.Fatalf("已有的 id 不应记命中, 得到 %d", hits)
	}
}

// question、done 的 recorded 条目进 requests 表：新 id、state recorded、标题为脱敏消息并截到 500 字符、刷新会话，广播 request_new；
// suppressed 的跳过；时间零值或超前 1 分钟以上用 hub 时间，其余照用条目时间。
func TestAuditQuestionDoneRecorded(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	past := time.Now().Add(-time.Hour).UTC()
	sess := proto.Session{Tool: "claude", CLISessionID: "s1", TurnID: "t9", Cwd: "/p"}
	before := time.Now()
	c.send(auditFrame(t,
		agent.AuditEntry{TS: past, Kind: "done", Session: sess, State: "suppressed", Message: "不该进表"},
		agent.AuditEntry{TS: time.Now().Add(time.Hour), Kind: "question", Session: sess, State: "recorded", Message: strings.Repeat("问", 600)},
		agent.AuditEntry{TS: past, Kind: "done", Session: sess, State: "recorded", Message: "完成了 mysql --password hunter22"},
	))
	waitFor(t, "两条 recorded", func() bool { rs, _ := h.st.ListRequests("recorded", 10); return len(rs) == 2 })
	after := time.Now()
	rs, _ := h.st.ListRequests("recorded", 10)
	byKind := map[string]store.Request{}
	for _, r := range rs {
		byKind[r.Kind] = r
	}
	done, q := byKind["done"], byKind["question"]
	if done.ID == "" || q.ID == "" || done.ID == q.ID || len(done.ID) != 32 {
		t.Fatalf("应各一条、用新 id: %+v", rs)
	}
	if strings.Contains(done.Title, "hunter22") || !strings.Contains(done.Title, "完成了") || done.MachineID != id || done.SessionID == "" ||
		done.CLISessionID != "s1" || done.TurnID != "t9" || done.ExpiresAt != nil || done.CreatedAt != store.FormatTime(past) {
		t.Fatalf("done 行: %+v", done)
	}
	if n := utf8.RuneCountInString(q.Title); n != 500 {
		t.Fatalf("标题应截到 500 字符, 得到 %d", n)
	}
	if q.CreatedAt < store.FormatTime(before) || q.CreatedAt > store.FormatTime(after) {
		t.Fatalf("超前的时间应换成 hub 当前时间: %s 不在 [%s, %s]", q.CreatedAt, store.FormatTime(before), store.FormatTime(after))
	}
	if q.SessionID != done.SessionID {
		t.Fatalf("同一会话应复用会话行: %s vs %s", q.SessionID, done.SessionID)
	}
	waitBroadcast(t, sub, "request_new", q.ID, nil)
}

// 审计 detail 兜底脱敏：解码成 AuditEntry 后再编码（未知字段如 env 被丢弃），载荷与消息再脱敏一次；坏条目跳过并汇总记日志。
func TestAuditDetailRedacted(t *testing.T) {
	h, logs := quietHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	raw := json.RawMessage(`{"ts":"2026-01-02T08:00:00Z","request_id":"a1","kind":"permission","session":{"tool":"claude","cli_session_id":"s1"},` +
		`"tool_name":"Bash","payload":{"type":"shell","command":"mysql --password hunter22 db"},"risk":"read","action":"allow","state":"auto_allowed",` +
		`"rule_id":"builtin-readonly","reason":"token=abcdefsecret","env":{"AWS_SECRET":"SECRET_ENV_VALUE"}}`)
	c.send(auditFrame(t, json.RawMessage(`"不是对象"`), raw))
	waitFor(t, "a1 历史行", func() bool { _, err := h.st.GetRequest("a1"); return err == nil })
	events, err := h.st.ListAudit(10, "a1")
	if err != nil || len(events) != 1 {
		t.Fatalf("审计: %+v, %v", events, err)
	}
	e := events[0]
	for _, bad := range []string{"hunter22", "SECRET_ENV_VALUE", `"env"`, "abcdefsecret"} {
		if strings.Contains(e.Detail, bad) {
			t.Fatalf("detail 不应含 %q: %s", bad, e.Detail)
		}
	}
	if e.Actor != "agent" || e.Action != "auto_allowed" || e.MachineID != id || e.TS != store.FormatTime(time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)) ||
		!strings.Contains(e.Detail, "redacted") {
		t.Fatalf("审计行: %+v", e)
	}
	if r := getReq(t, h.st, "a1"); strings.Contains(r.Payload.Command, "hunter22") || strings.Contains(r.EngineReason, "abcdefsecret") {
		t.Fatalf("历史行应脱敏: %+v", r)
	}
	if !strings.Contains(logs.String(), "无法解析") {
		t.Fatalf("坏条目应记日志: %q", logs.String())
	}
}

// 终审 I1：redact 补的写法（URL 的 user:pass、curl -u、API key 头、MYSQL_PWD、mysql -p）在 hub 兜底脱敏时同样生效：
// 守护进程送来未脱敏的 pending 与审计条目，库里的请求、审计 detail 都不含原文。
func TestHubRedactsCredentialGaps(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	secrets := []string{"S3cretPass", "abcdef123456", "PwdS3cret", "MyPS3cret", "UrlS3cret"}
	c.send(pendingFrame("p1", "s1", "t1", "curl -u alice:S3cretPass -H 'X-API-Key: abcdef123456' https://x"))
	entry := agent.AuditEntry{TS: time.Now().UTC(), RequestID: "a1", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s2"},
		ToolName: "Bash", Payload: &policy.Payload{Type: policy.PayloadShell, Command: "MYSQL_PWD=PwdS3cret mysql -uroot -pMyPS3cret app"},
		Risk: "read", Action: "allow", State: "auto_allowed", RuleID: "builtin-readonly", Reason: "git clone https://bob:UrlS3cret@git.example.com/r.git"}
	c.send(auditFrame(t, entry))
	waitFor(t, "p1、a1 入库", func() bool {
		_, e1 := h.st.GetRequest("p1")
		_, e2 := h.st.GetRequest("a1")
		return e1 == nil && e2 == nil
	})
	p1, a1 := getReq(t, h.st, "p1"), getReq(t, h.st, "a1")
	if p1.Payload.Command != "curl -u alice:<redacted> -H 'X-API-Key: <redacted>' https://x" {
		t.Fatalf("pending 的载荷应再脱敏: %q", p1.Payload.Command)
	}
	if a1.Payload.Command != "MYSQL_PWD=<redacted> mysql -uroot -p<redacted> app" ||
		a1.EngineReason != "git clone https://bob:<redacted>@git.example.com/r.git" {
		t.Fatalf("审计补的历史行应再脱敏: %q %q", a1.Payload.Command, a1.EngineReason)
	}
	events, err := h.st.ListAudit(10, "a1")
	if err != nil || len(events) != 1 {
		t.Fatalf("审计: %+v, %v", events, err)
	}
	for _, s := range secrets {
		if strings.Contains(events[0].Detail, s) || strings.Contains(p1.Payload.Command, s) {
			t.Fatalf("不应含 %q: %s", s, events[0].Detail)
		}
	}
}

// 终审 M1：/ws/client 的 request_new、request_updated 对守护进程的请求一律推送请求视图（与 /api/inbox、webhook 请求同形状），
// 不再推 store.Request：登记、重发、决定、取消、question/done、决定未送达都是。
func TestClientPushUsesView(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	c.recv(5 * time.Second)
	c.send(pendingFrame("v1", "s1", "t1", "npm run build"))
	f := waitBroadcast(t, sub, "request_new", "v1", nil)
	wantView(t, "登记", f, "mac")
	if f["project"] != "p" || f["has_redacted"] != false {
		t.Fatalf("登记的推送: %+v", f)
	}
	c.send(pendingFrame("v1", "s1", "t1", "npm run build")) // 重连后重发同一条
	wantView(t, "重发", waitBroadcast(t, sub, "request_updated", "v1", nil), "mac")
	if _, err := h.srv.Decide(DecideInput{RequestID: "v1", Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	wantView(t, "决定", waitBroadcast(t, sub, "request_updated", "v1", nil), "mac")
	c.send(pendingFrame("v2", "s2", "t2", "git push"))
	waitBroadcast(t, sub, "request_new", "v2", nil)
	c.send(wire.Cancel{T: wire.TCancel, RequestID: "v2", Reason: "terminal"})
	wantView(t, "取消", waitBroadcast(t, sub, "request_updated", "v2", nil), "mac")
	sess := proto.Session{Tool: "claude", CLISessionID: "s3"}
	c.send(auditFrame(t, agent.AuditEntry{TS: time.Now(), Kind: "done", Session: sess, State: "recorded", Message: "完成"}))
	var doneID string
	for doneID == "" {
		var fr clientFrame
		json.Unmarshal(nextBroadcast(t, sub), &fr)
		if fr.Type == "request_new" && fr.Data["kind"] == "done" {
			wantView(t, "done", fr.Data, "mac")
			doneID, _ = fr.Data["id"].(string)
		}
	}
	// 已决定未送达、守护进程重连后不再持有：记 undeliverable 并推送
	c.close()
	c = dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	wantView(t, "决定未送达", waitBroadcast(t, sub, "request_updated", "v1", nil), "mac")
}

// 终审 M2：送达时间真的更新了才推送 request_updated（delivered_at 非空）：守护进程的 delivered 回执、审计里的送达对账。
func TestDeliveredBroadcast(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("d1", "s1", "t1", "git push"))
	waitBroadcast(t, sub, "request_new", "d1", nil)
	if _, err := h.srv.Decide(DecideInput{RequestID: "d1", Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	if f := waitBroadcast(t, sub, "request_updated", "d1", nil); f["delivered_at"] != nil {
		t.Fatalf("决定时还没送达: %+v", f)
	}
	c.recv(5 * time.Second) // decision
	c.send(wire.Delivered{T: wire.TDelivered, RequestID: "d1"})
	wantView(t, "送达回执", waitDelivered(t, sub, "d1"), "mac")

	// 送达对账：本机审计记 approved、hub 未记送达
	insertReq(t, h.st, store.Request{ID: "d2", MachineID: id, State: "approved", DecidedBy: "user"})
	c.send(auditFrame(t, agent.AuditEntry{TS: time.Now(), RequestID: "d2", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s2"},
		State: "approved", DecidedBy: "user"}))
	wantView(t, "送达对账", waitDelivered(t, sub, "d2"), "mac")
}

// 终审 M3：hub 自己结束守护进程的 pending（过期扫描、决定时判过期、被重发顶掉）时，对该机器发带 request_id 的 Error 帧
// “请求已在 hub 结束”，守护进程据此立即把请求交回终端，不必挂满本机期限。
func TestHubEndedPendingNotifiesAgent(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	wantEnded := func(what, requestID string) {
		t.Helper()
		kind, b := c.recv(5 * time.Second)
		var e wire.Error
		wire.Unmarshal(b, &e)
		if kind != wire.TError || e.RequestID != requestID || e.Message != "请求已在 hub 结束" {
			t.Fatalf("%s：守护进程应收到 %s 的结束通知，得到 %s %+v", what, requestID, kind, e)
		}
	}
	// 守护进程的时钟比 hub 慢：hub 按自己的时间早已过期
	skewed := func(rid, sess string) wire.PendingRequest {
		f := pendingFrame(rid, sess, "t1", "git push "+rid)
		f.CreatedAt, f.DeadlineSeconds = time.Now().Add(-2*time.Hour).UTC(), 60
		return f
	}
	c.send(skewed("x1", "s1"))
	waitFor(t, "x1 登记", func() bool { _, err := h.st.GetRequest("x1"); return err == nil })
	h.srv.expireDue(context.Background(), time.Now())
	wantEnded("过期扫描", "x1")
	c.send(skewed("x2", "s2"))
	waitFor(t, "x2 登记", func() bool { _, err := h.st.GetRequest("x2"); return err == nil })
	if _, err := h.srv.Decide(DecideInput{RequestID: "x2", Action: "allow"}); !errors.Is(err, errExpired) {
		t.Fatalf("x2 应判过期: %v", err)
	}
	wantEnded("决定时判过期", "x2")
	c.send(pendingFrame("x3", "s3", "t3", "touch a"))
	waitFor(t, "x3 登记", func() bool { _, err := h.st.GetRequest("x3"); return err == nil })
	c.send(pendingFrame("x4", "s3", "t3", "touch a"))
	wantEnded("被重发顶掉", "x3")
}

// Gemini 的请求不进 hub 等待；请求 id 无效的拒收；都回带 request_id 的 Error 帧，不登记。
func TestPendingRejected(t *testing.T) {
	h, logs := quietHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	g := pendingFrame("g1", "s1", "t1", "ls")
	g.Session.Tool = "gemini"
	c.send(g)
	kind, b := c.recv(5 * time.Second)
	var e wire.Error
	wire.Unmarshal(b, &e)
	if kind != wire.TError || e.RequestID != "g1" || !strings.Contains(e.Message, "gemini") {
		t.Fatalf("应回 Error: %s %+v", kind, e)
	}
	long := strings.Repeat("x", 200)
	for _, bad := range []string{"", long, "r 1", "r1\n伪造的日志行", "../r1", "r1\x00"} {
		c.send(pendingFrame(bad, "s1", "t1", "ls"))
		kind, b = c.recv(5 * time.Second)
		e = wire.Error{}
		wire.Unmarshal(b, &e)
		if kind != wire.TError || e.RequestID != bad || e.Message != "登记失败" {
			t.Fatalf("无效 id 应回 Error: %s %+v", kind, e)
		}
	}
	if n, _ := h.st.CountPending(); n != 0 {
		t.Fatalf("不应登记, 得到 %d", n)
	}
	if !strings.Contains(logs.String(), "gemini") {
		t.Fatalf("应记日志: %q", logs.String())
	}
}

// 查重时读库失败（这里关掉存储）回带 request_id 的 Error 帧，守护进程据此按 hub_unreachable 交回终端。
func TestPendingLookupErrorRepliesError(t *testing.T) {
	h, logs := quietHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	h.st.Close()
	c.send(pendingFrame("r1", "s1", "t1", "ls"))
	kind, b := c.recv(5 * time.Second)
	var e wire.Error
	wire.Unmarshal(b, &e)
	if kind != wire.TError || e.RequestID != "r1" || e.Message != "登记失败" {
		t.Fatalf("应回 Error: %s %+v", kind, e)
	}
	if !strings.Contains(logs.String(), "r1") {
		t.Fatalf("应记日志: %q", logs.String())
	}
}

// hub 不信任守护进程：时间零值或超前用 hub 时间，期限夹到 [1, 86400] 秒，载荷再脱敏，长字段截断。
func TestPendingSanitized(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	past := time.Now().Add(-2 * time.Hour).UTC()
	f1 := pendingFrame("f1", "s1", "t1", "curl --password hunter22 https://x")
	f1.CreatedAt, f1.DeadlineSeconds = time.Time{}, 0
	f2 := pendingFrame("f2", "s2", "t2", "b")
	f2.CreatedAt, f2.DeadlineSeconds = time.Now().Add(time.Hour), 1_000_000_000
	f3 := pendingFrame("f3", strings.Repeat("s", 300), strings.Repeat("t", 300), "c")
	f3.CreatedAt, f3.DeadlineSeconds = past, 600
	f3.ToolName, f3.Risk = strings.Repeat("工", 300), strings.Repeat("r", 300)
	f3.EngineReason, f3.ProjectRoot = strings.Repeat("理", 1200), "/"+strings.Repeat("p", 5000)
	before := time.Now()
	c.send(f1)
	c.send(f2)
	c.send(f3)
	waitFor(t, "三条登记", func() bool { n, _ := h.st.CountPending(); return n == 3 })
	after := time.Now()
	span := func(r store.Request) (time.Time, time.Duration) {
		t.Helper()
		created, err1 := time.Parse(time.RFC3339Nano, r.CreatedAt)
		expires, err2 := time.Parse(time.RFC3339Nano, *r.ExpiresAt)
		if err1 != nil || err2 != nil {
			t.Fatalf("时间解析: %v %v", err1, err2)
		}
		return created, expires.Sub(created)
	}
	r1, r2, r3 := getReq(t, h.st, "f1"), getReq(t, h.st, "f2"), getReq(t, h.st, "f3")
	if created, d := span(r1); created.Before(before) || created.After(after) || d != time.Second {
		t.Fatalf("零值时间用 hub 时间、期限至少 1 秒: %s %s", created, d)
	}
	if created, d := span(r2); created.Before(before) || created.After(after) || d != 86400*time.Second {
		t.Fatalf("超前时间用 hub 时间、期限至多 86400 秒: %s %s", created, d)
	}
	if created, d := span(r3); !created.Equal(past) || d != 600*time.Second {
		t.Fatalf("合理的时间照用: %s %s", created, d)
	}
	if strings.Contains(r1.Payload.Command, "hunter22") {
		t.Fatalf("载荷应再脱敏: %+v", r1.Payload)
	}
	if utf8.RuneCountInString(r3.ToolName) != 256 || len(r3.Risk) != 256 || len(r3.CLISessionID) != 256 || len(r3.TurnID) != 256 ||
		utf8.RuneCountInString(r3.EngineReason) != 1000 || len(r3.ProjectRoot) != 4096 {
		t.Fatalf("长字段应截断: tool %d risk %d sess %d turn %d reason %d root %d", utf8.RuneCountInString(r3.ToolName), len(r3.Risk),
			len(r3.CLISessionID), len(r3.TurnID), utf8.RuneCountInString(r3.EngineReason), len(r3.ProjectRoot))
	}
}

// hub 自己执行过期：Decide 遇到已过期的先改 expired 再回 ErrNotPending；expireDue 把到期的 pending 改 expired 并广播。
func TestHubExpiry(t *testing.T) {
	h := newTestHub(t)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	past, future := store.FormatTime(time.Now().Add(-time.Second)), store.FormatTime(time.Now().Add(time.Hour))
	insertReq(t, h.st, store.Request{ID: "e1", MachineID: m.ID, State: "pending", ExpiresAt: &past})
	insertReq(t, h.st, store.Request{ID: "e2", MachineID: m.ID, State: "pending", ExpiresAt: &future})
	insertReq(t, h.st, store.Request{ID: "e3", MachineID: m.ID, State: "pending", ExpiresAt: &past})
	sub, stop := h.srv.clients.subscribe()
	defer stop()

	_, err := h.srv.Decide(DecideInput{RequestID: "e1", Action: "allow"})
	if !errors.Is(err, store.ErrNotPending) || !errors.Is(err, errExpired) {
		t.Fatalf("过期的请求应回 ErrNotPending（已过期）: %v", err)
	}
	if e1 := getReq(t, h.st, "e1"); e1.State != "expired" || e1.DecidedBy != "timeout" {
		t.Fatalf("e1 = %+v", e1)
	}
	waitBroadcast(t, sub, "request_updated", "e1", nil)
	if acts := auditActions(t, h.st, "e1"); countOf(acts, "expired") != 1 || countOf(acts, "allow") != 0 {
		t.Fatalf("e1 审计: %v", acts)
	}

	h.srv.expireDue(context.Background(), time.Now())
	if e2, e3 := getReq(t, h.st, "e2"), getReq(t, h.st, "e3"); e2.State != "pending" || e3.State != "expired" || e3.DecidedBy != "timeout" {
		t.Fatalf("e2=%+v e3=%+v", e2, e3)
	}
	waitBroadcast(t, sub, "request_updated", "e3", nil)
	h.srv.expireDue(context.Background(), time.Now().Add(2*time.Hour))
	if e2 := getReq(t, h.st, "e2"); e2.State != "expired" {
		t.Fatalf("e2 = %+v", e2)
	}
	if acts := auditActions(t, h.st, "e3"); countOf(acts, "expired") != 1 {
		t.Fatalf("e3 审计: %v", acts)
	}
}

// 终审 M9：过期扫描看 ctx：关停（ctx 已结束）时不再逐条处理，剩下的留给下次扫描（hub 重启后启动时那一次）。
func TestExpireDueStopsOnContext(t *testing.T) {
	h := newTestHub(t)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	past := store.FormatTime(time.Now().Add(-time.Second))
	for _, id := range []string{"q1", "q2", "q3"} {
		insertReq(t, h.st, store.Request{ID: id, MachineID: m.ID, State: "pending", ExpiresAt: &past})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.srv.expireDue(ctx, time.Now())
	if n, _ := h.st.CountPending(); n != 3 {
		t.Fatalf("ctx 已结束时不应再过期请求，pending 剩 %d", n)
	}
	h.srv.expireDue(context.Background(), time.Now())
	if n, _ := h.st.CountPending(); n != 0 {
		t.Fatalf("下次扫描应补上，pending 剩 %d", n)
	}
}

// 终审 M5：守护进程审计条目里的备注（note）也先全文脱敏再截到 1000 字符，与 Decide 的备注上限一致。
func TestAuditEntryNoteClamped(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	note := "password=hunter22x " + strings.Repeat("备", 1500)
	c.send(auditFrame(t, agent.AuditEntry{TS: time.Now(), RequestID: "n1", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1"},
		ToolName: "Bash", State: "approved", DecidedBy: "user", Note: note}))
	waitFor(t, "n1 的审计", func() bool { es, _ := h.st.ListAudit(10, "n1"); return len(es) == 1 })
	es, _ := h.st.ListAudit(10, "n1")
	var e agent.AuditEntry
	if err := json.Unmarshal([]byte(es[0].Detail), &e); err != nil {
		t.Fatal(err)
	}
	if n := utf8.RuneCountInString(e.Note); n != 1000 || strings.Contains(e.Note, "hunter22x") || !strings.HasPrefix(e.Note, "password=<redacted> ") {
		t.Fatalf("备注应脱敏并截到 1000 字符: %d %q…", n, e.Note[:40])
	}
}

// 并发两次决定同一请求（都带 Remember，两种都能学）：一次成功、一次 ErrNotPending，只学一条规则。
func TestDecideConcurrent(t *testing.T) {
	h := newTestHub(t)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	insertReq(t, h.st, store.Request{ID: "c1", MachineID: m.ID, State: "pending", ProjectRoot: "/p",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "echo c1", Cwd: "/p"}})
	errs := make(chan error, 2)
	for _, a := range []string{"allow", "deny"} {
		go func() {
			_, err := h.srv.Decide(DecideInput{RequestID: "c1", Action: a, Remember: true})
			errs <- err
		}()
	}
	var ok, notPending int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrNotPending):
			notPending++
		default:
			t.Fatalf("意外的错误: %v", err)
		}
	}
	if ok != 1 || notPending != 1 {
		t.Fatalf("应一次成功一次 ErrNotPending: ok=%d notPending=%d", ok, notPending)
	}
	if acts := auditActions(t, h.st, "c1"); countOf(acts, "allow")+countOf(acts, "deny") != 1 || countOf(acts, "rule_learned") != 1 {
		t.Fatalf("只应有一次决定审计、一次学规则: %v", acts)
	}
	if rules, _ := h.st.ListRules(); len(rules) != 3 {
		t.Fatalf("只应学一条规则: %+v", rules)
	}
}

// 用户备注至多 1000 字符，原样（截断后）放进下发的决定。
func TestDecideNoteTruncated(t *testing.T) {
	h := newTestHub(t)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	insertReq(t, h.st, store.Request{ID: "n1", MachineID: m.ID, State: "pending"})
	c := newFakeConn(m.ID, 4)
	h.srv.agents.register(c)
	got, err := h.srv.Decide(DecideInput{RequestID: "n1", Action: "deny", Note: strings.Repeat("备", 1500)})
	if err != nil || got.State != "denied" || utf8.RuneCountInString(got.DecisionNote) != 1000 {
		t.Fatalf("Decide = %+v, %v", got, err)
	}
	var d wire.Decision
	if err := wire.Unmarshal(<-c.out, &d); err != nil || d.Action != "deny" || d.Reason != got.DecisionNote {
		t.Fatalf("下发的决定: %+v, %v", d, err)
	}
}

// 广播帧为 {"type","data"}；慢的订阅者丢帧不阻塞；取消订阅后不再收到；编码失败的载荷不发。
func TestClientHubBroadcast(t *testing.T) {
	h := newClientHub()
	slow, stopSlow := h.subscribe()
	fast, stopFast := h.subscribe()
	h.broadcast("x", map[string]int{"n": 1})
	if b := <-fast; string(b) != `{"data":{"n":1},"type":"x"}` {
		t.Fatalf("帧: %s", b)
	}
	h.broadcast("bad", make(chan int))
	if len(fast) != 0 || len(slow) != 1 {
		t.Fatalf("编码失败的载荷不应发出: fast %d slow %d", len(fast), len(slow))
	}
	stopFast()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			h.broadcast("x", 1)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("慢的订阅者不应阻塞广播")
	}
	if len(slow) != cap(slow) || len(fast) != 0 {
		t.Fatalf("慢订阅者应收满后丢帧, 已取消的不再收到: slow %d fast %d", len(slow), len(fast))
	}
	stopSlow()
	h.broadcast("x", 2)
	if len(slow) != cap(slow) {
		t.Fatal("取消订阅后不应再收到")
	}
}

// 插入新请求失败（触发器让 r2 插不进去）：回带 request_id 的 Error 帧；与它同键的旧请求不被顶掉，仍是 pending。
func TestPendingInsertFailureKeepsOld(t *testing.T) {
	h, logs := quietHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "touch a"))
	waitFor(t, "r1 登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	execDB(t, h, `CREATE TRIGGER no_r2 BEFORE INSERT ON requests WHEN NEW.id = 'r2' BEGIN SELECT RAISE(ABORT, 'no insert'); END`)
	c.send(pendingFrame("r2", "s1", "t1", "touch a")) // 与 r1 同键，插入成功本会顶掉 r1
	kind, b := c.recv(5 * time.Second)
	var e wire.Error
	wire.Unmarshal(b, &e)
	if kind != wire.TError || e.RequestID != "r2" || e.Message != "登记失败" {
		t.Fatalf("插入失败应回 Error: %s %+v", kind, e)
	}
	if r1 := getReq(t, h.st, "r1"); r1.State != "pending" || r1.DecidedBy != "" {
		t.Fatalf("新请求没登记上，旧请求不应被顶掉: %+v", r1)
	}
	if _, err := h.st.GetRequest("r2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("r2 不应存在: %v", err)
	}
	if !strings.Contains(logs.String(), `"r2"`) {
		t.Fatalf("应记日志: %q", logs.String())
	}
}

// 守护进程再次送来 hub 已有的 id：已决定的、内容（载荷哈希、工具名、CLI 会话）与库里一致才再发决定，
// 对齐时已补发过的不再发，已送达的照发并记日志，内容不符回 Error 并记日志；其他终态回 Error。
func TestDuplicatePendingReplies(t *testing.T) {
	old := helloResendMax
	helloResendMax = 1 // 在 hub 起服务之前改、在 hub 关停之后恢复（t.Cleanup 后进先出）
	t.Cleanup(func() { helloResendMax = old })
	h, logs := quietHub(t)
	id, secret := enrollMachine(t, h, "mac")
	same := func(rid, state string, delivered bool) store.Request {
		f := pendingFrame(rid, "s1", "t1", "echo "+rid)
		r := store.Request{ID: rid, MachineID: id, State: state, DecidedBy: "user", ToolName: f.ToolName, CLISessionID: "s1", Payload: f.Payload}
		if delivered {
			now := store.Now()
			r.DeliveredAt = &now
		}
		return r
	}
	insertReq(t, h.st, same("d1", "approved", false)) // 对齐时补发
	insertReq(t, h.st, same("d4", "approved", false)) // 超过补发上限 1，留给重发的 pending 带出
	insertReq(t, h.st, same("d2", "denied", true))    // 已送达
	insertReq(t, h.st, same("d3", "approved", false)) // 重发的内容不符
	insertReq(t, h.st, store.Request{ID: "x1", MachineID: id, State: "cancelled", DecidedBy: "terminal"})
	insertReq(t, h.st, store.Request{ID: "x2", MachineID: id, State: "expired", DecidedBy: "timeout"})
	held := []wire.PendingSummary{{RequestID: "d1"}, {RequestID: "d4"}, {RequestID: "d2"}, {RequestID: "x1"}, {RequestID: "x2"}}
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, Pending: held})
	defer c.close()
	c.recv(5 * time.Second) // welcome
	recvDecision := func(want, action string) {
		t.Helper()
		kind, b := c.recv(5 * time.Second)
		var d wire.Decision
		wire.Unmarshal(b, &d)
		if kind != wire.TDecision || d.RequestID != want || d.Action != action {
			t.Fatalf("应收到 %s 的决定 %s: %s %+v", want, action, kind, d)
		}
	}
	recvError := func(want string) {
		t.Helper()
		kind, b := c.recv(5 * time.Second)
		var e wire.Error
		wire.Unmarshal(b, &e)
		if kind != wire.TError || e.RequestID != want {
			t.Fatalf("应收到 %s 的 Error: %s %+v", want, kind, e)
		}
	}
	recvDecision("d1", "allow") // 对齐补发（上限 1）
	for _, x := range []string{"d1", "d4", "d2", "x1", "x2"} {
		c.send(pendingFrame(x, "s1", "t1", "echo "+x))
	}
	c.send(pendingFrame("d3", "s1", "t1", "rm -rf /")) // 同 id、不同命令
	// d1 对齐时已补发过，不再发；按序应依次收到：
	recvDecision("d4", "allow")
	recvDecision("d2", "deny")
	recvError("x1")
	recvError("x2")
	recvError("d3")
	if d1 := getReq(t, h.st, "d1"); d1.State != "approved" || d1.DeliveredAt != nil {
		t.Fatalf("再发决定不改状态、不标送达: %+v", d1)
	}
	for _, want := range []string{`"d2"`, "已送达", `"d3"`, "不符"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("日志应含 %s: %q", want, logs.String())
		}
	}
}

// 审计条目指向别的机器的请求：审计行不挂到那条请求上（request_id 置空，detail 保留原值），也不动它；汇总记日志。
func TestAuditForeignRequestID(t *testing.T) {
	h, logs := quietHub(t)
	idA, _ := enrollMachine(t, h, "a")
	idB, secretB := enrollMachine(t, h, "b")
	insertReq(t, h.st, store.Request{ID: "r1", MachineID: idA, State: "pending"})
	b := dialAgent(t, h, secretB, wire.Hello{Proto: wire.Proto, MachineID: idB})
	defer b.close()
	b.recv(5 * time.Second)
	sess := proto.Session{Tool: "claude", CLISessionID: "s1"}
	b.send(auditFrame(t,
		agent.AuditEntry{TS: time.Now(), RequestID: "r1", Kind: "permission", Session: sess, State: "expired", DecidedBy: "timeout"},
		agent.AuditEntry{TS: time.Now(), RequestID: "rb", Kind: "permission", Session: sess, State: "auto_allowed", RuleID: "builtin-readonly"},
	))
	waitFor(t, "rb 历史行", func() bool { _, err := h.st.GetRequest("rb"); return err == nil })
	if acts := auditActions(t, h.st, "r1"); len(acts) != 0 {
		t.Fatalf("别的机器的审计不应挂到 r1: %v", acts)
	}
	events, _ := h.st.ListAudit(10, "")
	var foreign *store.AuditEvent
	for i, e := range events {
		if e.MachineID == idB && e.Action == "expired" {
			foreign = &events[i]
		}
	}
	if foreign == nil || foreign.RequestID != "" || !strings.Contains(foreign.Detail, `"request_id":"r1"`) {
		t.Fatalf("审计行应置空 request_id、detail 保留原值: %+v", foreign)
	}
	if r1 := getReq(t, h.st, "r1"); r1.State != "pending" {
		t.Fatalf("r1 不应被改动: %+v", r1)
	}
	if acts := auditActions(t, h.st, "rb"); len(acts) != 1 {
		t.Fatalf("本机器的请求照常挂审计: %v", acts)
	}
	if !strings.Contains(logs.String(), "别的机器") {
		t.Fatalf("应记日志: %q", logs.String())
	}
}

// 守护进程重发同一批审计：question/done 用确定性 id，只登记一行；撞上已有的行静默跳过，不记错误日志。
func TestAuditEventsDedup(t *testing.T) {
	h, logs := quietHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	sess := proto.Session{Tool: "claude", CLISessionID: "s1"}
	ts := time.Now().Add(-time.Minute)
	batch := auditFrame(t,
		agent.AuditEntry{TS: ts, Kind: "done", Session: sess, State: "recorded", Message: "完成"},
		agent.AuditEntry{TS: ts, Kind: "question", Session: sess, State: "recorded", Message: "要继续吗"},
	)
	c.send(batch)
	c.send(batch)
	c.send(auditFrame(t, agent.AuditEntry{TS: ts, Kind: "done", Session: sess, State: "recorded", Message: "标记"}))
	waitFor(t, "标记行", func() bool { rs, _ := h.st.ListRequests("recorded", 10); return len(rs) >= 3 })
	rs, _ := h.st.ListRequests("recorded", 10)
	if len(rs) != 3 {
		t.Fatalf("重发的批次不应重复登记, 得到 %d 行: %+v", len(rs), rs)
	}
	for _, r := range rs {
		if len(r.ID) != 32 || strings.Trim(r.ID, "0123456789abcdef") != "" {
			t.Fatalf("id 应为 32 个十六进制字符: %q", r.ID)
		}
	}
	if logs.String() != "" {
		t.Fatalf("重复的条目不应记日志: %q", logs.String())
	}
}

// 送达对账：守护进程本机审计里的 approved/denied 与 hub 行一致且 hub 未记送达时补记送达时间，不改状态；
// 不一致或别的机器的不记。
func TestAuditReconcilesDelivery(t *testing.T) {
	h, logs := quietHub(t)
	id, secret := enrollMachine(t, h, "mac")
	idB, _ := enrollMachine(t, h, "b")
	insertReq(t, h.st, store.Request{ID: "d1", MachineID: id, State: "approved", DecidedBy: "user"})
	insertReq(t, h.st, store.Request{ID: "d2", MachineID: id, State: "denied", DecidedBy: "user"})
	insertReq(t, h.st, store.Request{ID: "d3", MachineID: idB, State: "approved", DecidedBy: "user"})
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, Pending: []wire.PendingSummary{{RequestID: "d1"}, {RequestID: "d2"}}})
	defer c.close()
	c.recv(5 * time.Second) // welcome；随后是对齐补发的 d1、d2，这里不读
	sess := proto.Session{Tool: "claude", CLISessionID: "s1"}
	c.send(auditFrame(t,
		agent.AuditEntry{TS: time.Now(), RequestID: "d2", Kind: "permission", Session: sess, State: "approved", DecidedBy: "user"},
		agent.AuditEntry{TS: time.Now(), RequestID: "d3", Kind: "permission", Session: sess, State: "approved", DecidedBy: "user"},
		agent.AuditEntry{TS: time.Now(), RequestID: "d1", Kind: "permission", Session: sess, State: "approved", DecidedBy: "user"},
	))
	waitFor(t, "d1 补记送达", func() bool { r, _ := h.st.GetRequest("d1"); return r.DeliveredAt != nil })
	if d1 := getReq(t, h.st, "d1"); d1.State != "approved" || d1.DecidedBy != "user" {
		t.Fatalf("对账不改状态: %+v", d1)
	}
	if d2 := getReq(t, h.st, "d2"); d2.DeliveredAt != nil {
		t.Fatalf("状态不一致不记送达: %+v", d2)
	}
	if d3 := getReq(t, h.st, "d3"); d3.DeliveredAt != nil {
		t.Fatalf("别的机器的请求不记送达: %+v", d3)
	}
	if !strings.Contains(logs.String(), "别的机器") {
		t.Fatalf("指向别的机器的条目应汇总记日志: %q", logs.String())
	}
}

// redactText 先截（留余量）再脱敏再截：跨截断点的凭据仍被整体抹掉，不会留下不够长、漏过裸 token 规则的半截。
func TestRedactTextStraddlingCut(t *testing.T) {
	in := strings.Repeat("a", 989) + " ghp_ABCDEFGHIJKLMNOPQRST tail"
	out := redactText(in, 1000)
	if utf8.RuneCountInString(out) != 1000 || strings.Contains(out, "ABCDEF") {
		t.Fatalf("跨截断点的 token 不应漏出: %q", out[980:])
	}
	if got := redactText("mysql --password hunter22", 1000); got != "mysql --password <redacted>" {
		t.Fatalf("短文本照常脱敏: %q", got)
	}
}

// 先全文脱敏再截断（审查复现的两种泄漏）：
// A：引号括起、长于余量的秘密，右引号在预截点之后——预截会让引号规则匹配不上；
// B：前面的长 Bearer token 换成 <redacted> 后文本左移，预截时被截成不足 16 字符的裸 token 被拉进前 1000 字符。
func TestRedactTextNoLeakAcrossCut(t *testing.T) {
	a := strings.Repeat("a", 897) + ` SECRET_KEY="` + strings.Repeat("Q", 450) + `" tail`
	if out := redactText(a, 1000); strings.Contains(out, "QQQQ") {
		t.Errorf("A：引号括起的长秘密漏出: %q", out[880:])
	}
	b := "Bearer " + strings.Repeat("t", 1000) + " " + strings.Repeat("a", 240) + " ghp_ABCDEFGHIJKLMNOPQRSTUV tail"
	if out := redactText(b, 1000); strings.Contains(out, "ghp_ABC") || strings.Contains(out, "tttt") {
		t.Errorf("B：左移后的半截裸 token 漏出: %q", out[250:])
	}
}

// 同一机器重发仍 pending 的请求：载荷哈希、工具名、CLI 会话与库里一致才广播；不一致回 Error（请求内容不符）并记日志，
// 库里那行不动、不广播。
func TestPendingAgainPendingMismatch(t *testing.T) {
	h, logs := quietHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	c.send(pendingFrame("r1", "s1", "t1", "echo a"))
	waitBroadcast(t, sub, "request_new", "r1", nil)
	before := getReq(t, h.st, "r1")
	otherTool := pendingFrame("r1", "s1", "t1", "echo a")
	otherTool.ToolName = "Write"
	for _, f := range []wire.PendingRequest{pendingFrame("r1", "s1", "t1", "rm -rf /"), otherTool, pendingFrame("r1", "s2", "t1", "echo a")} {
		c.send(f)
		kind, b := c.recv(5 * time.Second)
		var e wire.Error
		wire.Unmarshal(b, &e)
		if kind != wire.TError || e.RequestID != "r1" || e.Message != "请求内容不符" {
			t.Fatalf("内容不符应回 Error: %s %+v", kind, e)
		}
	}
	c.send(pendingFrame("r1", "s1", "t1", "echo a")) // 内容一致：照常只广播
	waitBroadcast(t, sub, "request_updated", "r1", nil)
	c.send(pendingFrame("r2", "s9", "t9", "echo b"))
	waitBroadcast(t, sub, "request_new", "r2", func(f clientFrame) {
		if f.Type == "request_updated" && f.Data["id"] == "r1" {
			t.Errorf("一致的重发只应广播一次: %+v", f)
		}
	})
	if after := getReq(t, h.st, "r1"); after.State != "pending" || after.Payload.Command != "echo a" || after.ToolName != before.ToolName ||
		after.CLISessionID != "s1" || after.CreatedAt != before.CreatedAt {
		t.Fatalf("库里那行不应改动: %+v", after)
	}
	if !strings.Contains(logs.String(), "不符") {
		t.Fatalf("应记日志: %q", logs.String())
	}
}

// pendingAgain 的三种 Error（别的机器的同 id、内容不符、已结束）只回给送来 pending 的那条连接：
// 它已不是该机器的当前连接时不发，帧也不落到新连接上。
func TestPendingAgainRepliesOnlyToSender(t *testing.T) {
	h, _ := quietHub(t)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	other, _ := h.st.CreateMachine("o", store.RandomToken(), "/h")
	f := pendingFrame("p1", "s1", "t1", "echo p1")
	insertReq(t, h.st, store.Request{ID: "p1", MachineID: m.ID, State: "pending", ToolName: f.ToolName, CLISessionID: "s1", Payload: f.Payload})
	insertReq(t, h.st, store.Request{ID: "x1", MachineID: m.ID, State: "cancelled", DecidedBy: "terminal"})
	insertReq(t, h.st, store.Request{ID: "f1", MachineID: other.ID, State: "pending"})
	cases := []struct {
		id  string
		cmd string
	}{{"f1", "echo f1"}, {"p1", "rm -rf /"}, {"x1", "echo x1"}}
	cur, stale := newFakeConn(m.ID, 8), newFakeConn(m.ID, 8)
	h.srv.agents.register(cur)
	for _, tc := range cases {
		h.srv.pendingAgain(stale, getReq(t, h.st, tc.id), pendingFrame(tc.id, "s1", "t1", tc.cmd))
		if len(cur.out) != 0 || len(stale.out) != 0 {
			t.Fatalf("%s: 送来 pending 的连接已不是当前连接，Error 不应发出、也不应落到当前连接", tc.id)
		}
		h.srv.pendingAgain(cur, getReq(t, h.st, tc.id), pendingFrame(tc.id, "s1", "t1", tc.cmd))
		var e wire.Error
		if len(cur.out) != 1 || wire.Unmarshal(<-cur.out, &e) != nil || e.T != wire.TError || e.RequestID != tc.id {
			t.Fatalf("%s: 应只给送来的连接回 Error: %+v", tc.id, e)
		}
	}
}

// askFrame 是守护进程送来的答题请求（kind ask，题目已脱敏），每道题两个选项。
func askFrame(id, session, turn string, questions ...string) wire.PendingRequest {
	return wire.PendingRequest{T: wire.TPending, RequestID: id, Kind: "ask", Ask: testAsk(questions...),
		Session: proto.Session{Tool: "claude", CLISessionID: session, TurnID: turn, Cwd: "/p"}, ToolName: "AskUserQuestion",
		DeadlineSeconds: 86390, CreatedAt: time.Now().UTC()}
}

// testAsk 给每道题配“方案”标题与 A、B 两个选项。
func testAsk(questions ...string) *proto.Ask {
	a := &proto.Ask{}
	for _, q := range questions {
		a.Questions = append(a.Questions, proto.Question{Question: q, Header: "方案", Options: []proto.Option{{Label: "A", Description: "快"}, {Label: "B"}}})
	}
	return a
}

// insertAsk 直接往库里插一条属于 machineID、仍 pending 的提问（会话 s1），返回它的题目。
func insertAsk(t *testing.T, st *store.Store, id, machineID string, questions ...string) *proto.Ask {
	t.Helper()
	a := testAsk(questions...)
	insertReq(t, st, store.Request{ID: id, MachineID: machineID, Kind: "ask", ToolName: "AskUserQuestion", Ask: a, State: "pending", CLISessionID: "s1"})
	return a
}

// answersFor 给每道题一个答案。
func answersFor(a *proto.Ask, ans ...string) map[string]string {
	out := map[string]string{}
	for i, q := range a.Questions {
		out[q.Question] = ans[i]
	}
	return out
}

// recvDecision 读守护进程收到的下一帧，要求是 id 的决定。
func recvDecision(t *testing.T, c *agentClient, id string) wire.Decision {
	t.Helper()
	kind, b := c.recv(5 * time.Second)
	var d wire.Decision
	if err := wire.Unmarshal(b, &d); err != nil || kind != wire.TDecision || d.RequestID != id {
		t.Fatalf("应收到 %s 的决定: %s %s", id, kind, b)
	}
	return d
}

// 重发去重按题目认：同一回合两道不同的题都保持 pending；同一道题换了 id 在窗口内重发，顶掉旧的（cancelled/resend）。
func TestAskResendDedup(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(askFrame("a1", "s1", "t1", "用 A 还是 B？"))
	c.send(askFrame("a2", "s1", "t1", "要不要写测试？"))
	waitFor(t, "两道题都登记", func() bool { n, _ := h.st.CountPending(); return n == 2 })
	if a1 := getReq(t, h.st, "a1"); a1.State != "pending" {
		t.Fatalf("同一回合另一道题不应顶掉 a1: %+v", a1)
	}
	c.send(askFrame("a3", "s1", "t1", "用 A 还是 B？"))
	waitFor(t, "a3 登记", func() bool { r, err := h.st.GetRequest("a3"); return err == nil && r.State == "pending" })
	if a1 := getReq(t, h.st, "a1"); a1.State != "cancelled" || a1.DecidedBy != store.DecidedByResend {
		t.Fatalf("同一道题重发应顶掉 a1: %+v", a1)
	}
	if a2 := getReq(t, h.st, "a2"); a2.State != "pending" {
		t.Fatalf("a2 不受影响: %+v", a2)
	}
	if acts := auditActions(t, h.st, "a1"); countOf(acts, "cancelled_resend") != 1 {
		t.Fatalf("a1 应记 cancelled_resend: %v", acts)
	}
}

// 端到端：守护进程送来 ask → 答题接口 → 守护进程收到 answer 决定（键是 hub 收到的题面，decided_by user）→ delivered →
// 库里记下送达时间。库里的答案是原文；hub 自己的审计里答案脱敏。
func TestAnswerDeliversToAgent(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	f := askFrame("a1", "s1", "t1", "  用 A 还是 B？ ", "要不要写测试？")
	c.send(f)
	waitBroadcast(t, sub, "request_new", "a1", nil)
	token := "ghp_" + strings.Repeat("A", 36)
	answers := answersFor(f.Ask, "B", "要，token "+token)
	resp := apiPost(t, h, ck, "/api/requests/a1/answer", map[string]any{"answers": answers, "passthrough": false})
	var view map[string]any
	decodeBody(t, resp, &view)
	if resp.StatusCode != http.StatusOK || view["state"] != "answered" || view["kind"] != "ask" || view["decided_by"] != "user" || view["ask"] == nil {
		t.Fatalf("作答 = %d %+v", resp.StatusCode, view)
	}
	wantView(t, "答题的返回", view, "mac")
	d := recvDecision(t, c, "a1")
	if d.Action != "answer" || d.DecidedBy != "user" || !reflect.DeepEqual(d.Answers, answers) {
		t.Fatalf("守护进程应收到以收到的题面为键的答案: %+v", d)
	}
	if u := waitBroadcast(t, sub, "request_updated", "a1", nil); u["state"] != "answered" {
		t.Fatalf("应广播作答后的请求: %+v", u)
	}
	c.send(wire.Delivered{T: wire.TDelivered, RequestID: "a1"})
	waitDelivered(t, sub, "a1")
	r := getReq(t, h.st, "a1")
	if r.DeliveredAt == nil || !reflect.DeepEqual(r.Answers, answers) {
		t.Fatalf("库里应记送达、存答案原文: %+v", r)
	}
	ev, ok := lastAudit(t, h.st, "answer")
	if !ok || ev.Actor != "user" || ev.RequestID != "a1" || strings.Contains(ev.Detail, token) || !strings.Contains(ev.Detail, "redacted") ||
		!strings.Contains(ev.Detail, "要不要写测试？") {
		t.Fatalf("审计 answer 应带脱敏的答案: %+v %v", ev, ok)
	}
}

// 断线期间作答：重连时守护进程仍持有就补发 answer 决定（带答案），不再持有的记 undeliverable。交回终端的不补发，
// 它的 delivered 回执 hub 端无事可做；守护进程再送它的 pending 回“请求已结束”。
func TestAnswerResentAfterReconnect(t *testing.T) {
	h, _ := quietHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	c.recv(5 * time.Second)
	frames := map[string]wire.PendingRequest{"a1": askFrame("a1", "s1", "t1", "用 A 还是 B？"), "a2": askFrame("a2", "s2", "t1", "要不要写测试？"),
		"a3": askFrame("a3", "s3", "t1", "部署到哪？")}
	for _, f := range frames {
		c.send(f)
	}
	waitFor(t, "三道题登记", func() bool { n, _ := h.st.CountPending(); return n == 3 })
	c.close()
	waitFor(t, "断开", func() bool { return !h.srv.agents.connected(id) })
	a1 := answersFor(frames["a1"].Ask, "A")
	if _, err := h.srv.Answer(AnswerInput{RequestID: "a1", Answers: a1}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.srv.Answer(AnswerInput{RequestID: "a2", Passthrough: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.srv.Answer(AnswerInput{RequestID: "a3", Answers: answersFor(frames["a3"].Ask, "测试环境")}); err != nil {
		t.Fatal(err)
	}
	c2 := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, Pending: []wire.PendingSummary{{RequestID: "a1"}, {RequestID: "a2"}}})
	defer c2.close()
	c2.recv(5 * time.Second) // welcome
	if d := recvDecision(t, c2, "a1"); d.Action != "answer" || d.DecidedBy != "user" || !reflect.DeepEqual(d.Answers, a1) {
		t.Fatalf("重连应补发 a1 的答案: %+v", d)
	}
	// a2 交回终端，不补发：守护进程重发它的 pending，下一帧就是“请求已结束”
	c2.send(frames["a2"])
	kind, b := c2.recv(5 * time.Second)
	var e wire.Error
	wire.Unmarshal(b, &e)
	if kind != wire.TError || e.RequestID != "a2" || e.Message != "请求已结束" {
		t.Fatalf("交回终端的不补发、重发回请求已结束: %s %s", kind, b)
	}
	if n := systemUndeliverable(t, h.st, id, "a3"); n != 1 {
		t.Fatalf("不再持有的 a3 应记 undeliverable, 得到 %d", n)
	}
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	c2.send(wire.Delivered{T: wire.TDelivered, RequestID: "a2"})
	c2.send(wire.Delivered{T: wire.TDelivered, RequestID: "a1"})
	// 同一连接按序处理：a1 的送达广播到了，a2 的回执也处理完了；交回终端的回执不记送达，所以不该有 a2 的 request_updated
	waitBroadcast(t, sub, "request_updated", "a1", func(f clientFrame) {
		if f.Type == "request_updated" && f.Data["id"] == "a2" {
			t.Errorf("交回终端的回执不应广播 a2 的更新: %+v", f)
		}
	})
	waitFor(t, "a1 送达", func() bool { r, _ := h.st.GetRequest("a1"); return r.DeliveredAt != nil })
	if a2 := getReq(t, h.st, "a2"); a2.State != "passthrough" || a2.DecidedBy != "user" || a2.DeliveredAt != nil {
		t.Fatalf("交回终端的回执不记: %+v", a2)
	}
}

// 重发已登记的 ask：内容一致时 pending 只广播、answered 再发决定（带答案）；题目不同或种类不同回“请求内容不符”；
// 交回终端的回“请求已结束”。
func TestAskPendingAgain(t *testing.T) {
	h, _ := quietHub(t)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	insertAsk(t, h.st, "p1", m.ID, "用 A 还是 B？")
	ans := insertAsk(t, h.st, "d1", m.ID, "要不要写测试？")
	insertAsk(t, h.st, "x1", m.ID, "部署到哪？")
	if _, err := h.st.AnswerRequest("d1", answersFor(ans, "要"), false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.AnswerRequest("x1", nil, true, time.Now()); err != nil {
		t.Fatal(err)
	}
	c := newFakeConn(m.ID, 8)
	h.srv.agents.register(c)
	again := func(f wire.PendingRequest) []byte {
		t.Helper()
		h.srv.pendingAgain(c, getReq(t, h.st, f.RequestID), f)
		switch len(c.out) {
		case 0:
			return nil
		case 1:
			return <-c.out
		}
		t.Fatalf("%s: 应至多回一帧, 得到 %d", f.RequestID, len(c.out))
		return nil
	}
	if b := again(askFrame("p1", "s1", "t1", "用 A 还是 B？")); b != nil {
		t.Fatalf("内容一致的 pending 只广播: %s", b)
	}
	perm := pendingFrame("p1", "s1", "t1", "echo p1")
	perm.ToolName = "AskUserQuestion"
	for _, f := range []wire.PendingRequest{askFrame("p1", "s1", "t1", "用 B 还是 C？"), perm} {
		var e wire.Error
		if b := again(f); wire.Unmarshal(b, &e) != nil || e.RequestID != "p1" || e.Message != "请求内容不符" {
			t.Fatalf("内容不符应回 Error: %s", b)
		}
	}
	var d wire.Decision
	if b := again(askFrame("d1", "s1", "t1", "要不要写测试？")); wire.Unmarshal(b, &d) != nil || d.T != wire.TDecision || d.Action != "answer" ||
		!reflect.DeepEqual(d.Answers, answersFor(ans, "要")) {
		t.Fatalf("已回答的应再发答案: %s", b)
	}
	var e wire.Error
	if b := again(askFrame("x1", "s1", "t1", "部署到哪？")); wire.Unmarshal(b, &e) != nil || e.RequestID != "x1" || e.Message != "请求已结束" {
		t.Fatalf("交回终端的回请求已结束: %s", b)
	}
}

// 提问不能用决定接口，也不能“记住”：Decide 与 propose 回 errBadAction（“这是提问，用答题接口”），不学规则、不改状态、不审计。
func TestDecideRejectsAsk(t *testing.T) {
	h := newTestHub(t)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	insertAsk(t, h.st, "a1", m.ID, "用 A 还是 B？")
	rulesBefore, _ := h.st.ListRules()
	for _, in := range []DecideInput{{RequestID: "a1", Action: "allow"}, {RequestID: "a1", Action: "deny", Remember: true}, {RequestID: "a1", Action: "allow", Remember: true, Scope: "global"}} {
		if _, err := h.srv.Decide(in); !errors.Is(err, errBadAction) || err.Error() != "这是提问，用答题接口" {
			t.Fatalf("Decide(%+v) 应拒绝提问: %v", in, err)
		}
	}
	if _, err := h.srv.propose("a1", "allow", ""); !errors.Is(err, errBadAction) || err.Error() != "这是提问，用答题接口" {
		t.Fatalf("propose 应拒绝提问: %v", err)
	}
	if a1 := getReq(t, h.st, "a1"); a1.State != "pending" {
		t.Fatalf("不应改状态: %+v", a1)
	}
	if rules, _ := h.st.ListRules(); len(rules) != len(rulesBefore) {
		t.Fatalf("不应学规则: %+v", rules)
	}
	if acts := auditActions(t, h.st, "a1"); len(acts) != 0 {
		t.Fatalf("不应审计: %v", acts)
	}
}

// Answer 的拒绝：不是提问 errBadAction（“这不是提问…”）；没有这条 ErrNotFound；答案不合规 errBadAnswer（缺题、空、纯空白、超长、多余键）；
// 已回答 ErrNotPending；已过期 errExpired（改成 expired、通知守护进程）；机器已吊销 errRevoked（改成 cancelled/machine_revoked）。
// 交回终端时忽略答案（有也不校验）；1000 字符的答案接受。
func TestAnswerRefusals(t *testing.T) {
	h := newTestHub(t)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	c := newFakeConn(m.ID, 8)
	h.srv.agents.register(c)
	a := insertAsk(t, h.st, "a1", m.ID, "用 A 还是 B？", "要不要写测试？")
	insertReq(t, h.st, store.Request{ID: "p1", MachineID: m.ID, State: "pending"})
	if _, err := h.srv.Answer(AnswerInput{RequestID: "p1", Answers: map[string]string{"x": "y"}}); !errors.Is(err, errBadAction) || !strings.Contains(err.Error(), "不是提问") {
		t.Fatalf("审批不能作答: %v", err)
	}
	if _, err := h.srv.Answer(AnswerInput{RequestID: "nope", Answers: answersFor(a, "A", "要")}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在: %v", err)
	}
	q1, q2 := a.Questions[0].Question, a.Questions[1].Question
	for name, ans := range map[string]map[string]string{
		"没有答案": nil,
		"缺题":   {q1: "A"},
		"空值":   {q1: "A", q2: ""},
		"纯空白":  {q1: "A", q2: " \t\n　"},
		"超长":   {q1: "A", q2: strings.Repeat("长", 1001)},
		"多余键":  {q1: "A", q2: "要", "别的题": "x"},
	} {
		if _, err := h.srv.Answer(AnswerInput{RequestID: "a1", Answers: ans}); !errors.Is(err, errBadAnswer) {
			t.Fatalf("%s: 应 errBadAnswer: %v", name, err)
		}
	}
	if a1 := getReq(t, h.st, "a1"); a1.State != "pending" || len(c.out) != 0 {
		t.Fatalf("不合规的答案不改状态、不下发: %+v", a1)
	}
	long := strings.Repeat("长", proto.AnswerMax)
	got, err := h.srv.Answer(AnswerInput{RequestID: "a1", Answers: map[string]string{q1: " A ", q2: long}})
	if err != nil || got.State != "answered" || got.Answers[q1] != " A " || got.Answers[q2] != long {
		t.Fatalf("1000 字符的答案应接受、答案不改写: %+v, %v", got, err)
	}
	if _, err := h.srv.Answer(AnswerInput{RequestID: "a1", Answers: map[string]string{q1: "B", q2: "不要"}}); !errors.Is(err, store.ErrNotPending) {
		t.Fatalf("已回答应 ErrNotPending: %v", err)
	}
	<-c.out // a1 的 answer 决定
	// 交回终端：答案有也不校验
	insertAsk(t, h.st, "a2", m.ID, "部署到哪？")
	if got, err := h.srv.Answer(AnswerInput{RequestID: "a2", Answers: map[string]string{"乱": ""}, Passthrough: true}); err != nil ||
		got.State != "passthrough" || got.Answers != nil {
		t.Fatalf("交回终端 = %+v, %v", got, err)
	}
	var d wire.Decision
	if err := wire.Unmarshal(<-c.out, &d); err != nil || d.RequestID != "a2" || d.Action != "passthrough" || d.DecidedBy != "user" || d.Answers != nil {
		t.Fatalf("应下发 passthrough: %+v, %v", d, err)
	}
	if ev, ok := lastAudit(t, h.st, "passthrough"); !ok || ev.Actor != "user" || ev.RequestID != "a2" {
		t.Fatalf("应审计 passthrough: %+v", ev)
	}
	// 已过期
	past := store.FormatTime(time.Now().Add(-time.Minute))
	insertReq(t, h.st, store.Request{ID: "e1", MachineID: m.ID, Kind: "ask", ToolName: "AskUserQuestion", Ask: testAsk("过期的题"), State: "pending", ExpiresAt: &past})
	if _, err := h.srv.Answer(AnswerInput{RequestID: "e1", Answers: map[string]string{"过期的题": "A"}}); !errors.Is(err, errExpired) {
		t.Fatalf("已过期: %v", err)
	}
	if e1 := getReq(t, h.st, "e1"); e1.State != "expired" || e1.DecidedBy != "timeout" {
		t.Fatalf("过期的应改 expired: %+v", e1)
	}
	var ended wire.Error
	if err := wire.Unmarshal(<-c.out, &ended); err != nil || ended.RequestID != "e1" {
		t.Fatalf("过期应通知守护进程: %+v, %v", ended, err)
	}
	// 机器已吊销
	insertAsk(t, h.st, "r1", m.ID, "吊销后的题")
	if err := h.st.RevokeMachine(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.srv.Answer(AnswerInput{RequestID: "r1", Answers: map[string]string{"吊销后的题": "A"}}); !errors.Is(err, errRevoked) {
		t.Fatalf("机器已吊销: %v", err)
	}
	if r1 := getReq(t, h.st, "r1"); r1.State != "cancelled" || r1.DecidedBy != decidedByRevoked {
		t.Fatalf("吊销的应改 cancelled: %+v", r1)
	}
}

// 两次并发作答只有一次成功，另一次 ErrNotPending；只有一条作答审计、一个下发的决定。
func TestAnswerConcurrent(t *testing.T) {
	h := newTestHub(t)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	c := newFakeConn(m.ID, 8)
	h.srv.agents.register(c)
	a := insertAsk(t, h.st, "a1", m.ID, "用 A 还是 B？")
	errs := make(chan error, 2)
	for _, in := range []AnswerInput{{RequestID: "a1", Answers: answersFor(a, "A")}, {RequestID: "a1", Passthrough: true}} {
		go func() {
			_, err := h.srv.Answer(in)
			errs <- err
		}()
	}
	var ok, notPending int
	for range 2 {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, store.ErrNotPending):
			notPending++
		default:
			t.Fatalf("意外的错误: %v", err)
		}
	}
	if ok != 1 || notPending != 1 {
		t.Fatalf("应一次成功一次 ErrNotPending: ok=%d notPending=%d", ok, notPending)
	}
	if acts := auditActions(t, h.st, "a1"); countOf(acts, "answer")+countOf(acts, "passthrough") != 1 || len(c.out) != 1 {
		t.Fatalf("只应有一次作答审计与一个决定: %v, %d 帧", acts, len(c.out))
	}
}

// 守护进程审计里的 ask 条目同口径对账：answered 补记送达；expired、cancelled、passthrough 覆盖仍 pending 的行；hub 没有的补
// kind ask 的历史行（题目再脱敏、载荷 {}）；auto_* 与 hub 没有的 answered 不补行。审计 detail 里的题目与答案再脱敏。
func TestAuditAskEntries(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	f1, f2 := askFrame("a1", "s1", "t1", "用 A 还是 B？"), askFrame("a2", "s2", "t1", "要不要写测试？")
	c.send(f1)
	c.send(f2)
	waitFor(t, "两道题登记", func() bool { n, _ := h.st.CountPending(); return n == 2 })
	if _, err := h.srv.Answer(AnswerInput{RequestID: "a1", Answers: answersFor(f1.Ask, "A")}); err != nil {
		t.Fatal(err)
	}
	recvDecision(t, c, "a1")
	leak := "mysql --password hunter22"
	sess := proto.Session{Tool: "claude", CLISessionID: "s3", Cwd: "/p"}
	raw := testAsk("连库用 " + leak + " 吗？")
	c.send(auditFrame(t,
		agent.AuditEntry{TS: time.Now(), RequestID: "a1", Kind: "ask", Session: sess, ToolName: "AskUserQuestion", Ask: f1.Ask, Action: "answer", State: "answered",
			DecidedBy: "user", Answers: map[string]string{"用 A 还是 B？": "A " + leak}},
		agent.AuditEntry{TS: time.Now(), RequestID: "a2", Kind: "ask", Session: sess, ToolName: "AskUserQuestion", State: "expired", DecidedBy: "timeout", Reason: "本机等待过期"},
		agent.AuditEntry{TS: time.Now(), RequestID: "a3", Kind: "ask", Session: sess, ToolName: "AskUserQuestion", Ask: raw, Action: "passthrough", State: "passthrough",
			DecidedBy: "error", Reason: "脱敏后有题面相同的题，分不清答的是哪道，交回终端处理"},
		agent.AuditEntry{TS: time.Now(), RequestID: "a4", Kind: "ask", Session: sess, ToolName: "AskUserQuestion", State: "auto_allowed", RuleID: "builtin-readonly"},
		agent.AuditEntry{TS: time.Now(), RequestID: "a5", Kind: "ask", Session: sess, ToolName: "AskUserQuestion", State: "answered", DecidedBy: "user"},
	))
	waitFor(t, "a3 补历史行", func() bool { _, err := h.st.GetRequest("a3"); return err == nil })
	waitFor(t, "a1 补记送达", func() bool { r, _ := h.st.GetRequest("a1"); return r.DeliveredAt != nil })
	if a2 := getReq(t, h.st, "a2"); a2.State != "expired" || a2.DecidedBy != "timeout" {
		t.Fatalf("a2 应被本机终态覆盖: %+v", a2)
	}
	a3 := getReq(t, h.st, "a3")
	if a3.Kind != "ask" || a3.State != "passthrough" || a3.DecidedBy != "error" || a3.ToolName != "AskUserQuestion" || a3.Ask == nil || len(a3.Ask.Questions) != 1 ||
		strings.Contains(a3.Ask.Questions[0].Question, "hunter22") || !strings.Contains(a3.Ask.Questions[0].Question, "<redacted>") || a3.Risk != "" ||
		a3.SessionID == "" || a3.DecidedAt == nil {
		t.Fatalf("a3 历史行: %+v", a3)
	}
	if got := queryDB(t, h, "SELECT payload FROM requests WHERE id = 'a3'"); got != "{}" {
		t.Fatalf("a3 的载荷列应为 {}: %q", got)
	}
	for _, rid := range []string{"a4", "a5"} {
		if _, err := h.st.GetRequest(rid); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("%s 不应补行: %v", rid, err)
		}
	}
	if hits := ruleHits(t, h.st, "builtin-readonly"); hits != 0 {
		t.Fatalf("ask 不记规则命中, 得到 %d", hits)
	}
	for _, rid := range []string{"a1", "a3"} {
		events, _ := h.st.ListAudit(10, rid)
		for _, e := range events {
			if e.Actor == "agent" && strings.Contains(e.Detail, "hunter22") {
				t.Fatalf("%s 的审计 detail 应再脱敏: %s", rid, e.Detail)
			}
		}
	}
}

// 过期扫描与吊销取消对 ask 一视同仁：过期的改 expired/timeout 并通知守护进程；吊销机器时仍 pending 的改 cancelled/machine_revoked。
func TestAskExpiryAndRevoke(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/h")
	c := newFakeConn(m.ID, 8)
	h.srv.agents.register(c)
	past := store.FormatTime(time.Now().Add(-time.Minute))
	insertReq(t, h.st, store.Request{ID: "e1", MachineID: m.ID, Kind: "ask", ToolName: "AskUserQuestion", Ask: testAsk("过期的题"), State: "pending", ExpiresAt: &past})
	insertAsk(t, h.st, "r1", m.ID, "还在等的题")
	h.srv.expireDue(context.Background(), time.Now())
	if e1 := getReq(t, h.st, "e1"); e1.State != "expired" || e1.DecidedBy != "timeout" {
		t.Fatalf("过期扫描应改 e1: %+v", e1)
	}
	var e wire.Error
	if err := wire.Unmarshal(<-c.out, &e); err != nil || e.RequestID != "e1" {
		t.Fatalf("应通知守护进程: %+v, %v", e, err)
	}
	if resp := doReq(t, "DELETE", h.http.URL+"/api/machines/"+m.ID, nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("吊销 = %d", resp.StatusCode)
	}
	if r1 := getReq(t, h.st, "r1"); r1.State != "cancelled" || r1.DecidedBy != decidedByRevoked {
		t.Fatalf("吊销应取消 r1: %+v", r1)
	}
}

// queryDB 用另一条连接查 hub 的库，返回一个文本值。
func queryDB(t *testing.T, h *testHub, q string) string {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(h.dir, "hub.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var s string
	if err := db.QueryRow(q).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// ask 的工具名恒为 AskUserQuestion，不照收帧里的；题目的 options 为 null 时规整成空数组，视图 JSON 里总是数组。
func TestAskToolNameFixedAndOptionsNormalized(t *testing.T) {
	h, _ := quietHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	f := askFrame("a1", "s1", "t1", "自由回答？")
	f.ToolName = "别的名字"
	f.Ask.Questions[0].Options = nil
	c.send(f)
	waitFor(t, "ask 登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	r := getReq(t, h.st, "a1")
	if r.ToolName != "AskUserQuestion" {
		t.Fatalf("工具名 = %q", r.ToolName)
	}
	b, err := json.Marshal(h.srv.view(r))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"options":[]`) || strings.Contains(string(b), `"options":null`) {
		t.Fatalf("视图里 options 应是空数组: %s", b)
	}
}

// 对不是提问的请求作答在存储层报 ErrNotAsk 时，接口回 400 与“不是提问”同口径，不落到 500。
func TestWriteDecideErrNotAsk(t *testing.T) {
	h, _ := quietHub(t)
	w := httptest.NewRecorder()
	h.srv.writeDecideErr(w, "作答", fmt.Errorf("包一层: %w", store.ErrNotAsk))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), errNotAsk.Error()) {
		t.Fatalf("ErrNotAsk 应回 400 与不是提问: %d %s", w.Code, w.Body)
	}
}

// 守护进程送来的触发点：段再脱敏后入库，请求视图带 trigger；审计补的历史行也带。
func TestPendingAndHistoryCarryTrigger(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	f := pendingFrame("tr1", "s1", "t1", "deploy")
	f.Trigger = &policy.Trigger{Segment: "deploy token=abcdef0123456789abcdef", Why: policy.WhyDeploy}
	c.send(f)
	c.send(pendingFrame("tr2", "s2", "t2", "old"))
	waitFor(t, "两条登记", func() bool { n, _ := h.st.CountPending(); return n == 2 })
	var detail struct {
		Request struct {
			Trigger *policy.Trigger `json:"trigger"`
		} `json:"request"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/requests/tr1", nil, "", ck), &detail)
	tr := detail.Request.Trigger
	if tr == nil || tr.Why != policy.WhyDeploy || !strings.Contains(tr.Segment, "<redacted>") || strings.Contains(tr.Segment, "abcdef0123456789") {
		t.Fatalf("视图里的触发点: %+v", tr)
	}
	var raw struct {
		Request map[string]any `json:"request"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/requests/tr2", nil, "", ck), &raw)
	if _, ok := raw.Request["trigger"]; ok {
		t.Fatalf("老守护进程没有触发点，视图不该有 trigger: %+v", raw.Request)
	}

	p := policy.Payload{Type: policy.PayloadShell, Command: "echo hi > /tmp/x", Cwd: "/p"}
	e, _ := json.Marshal(agent.AuditEntry{TS: time.Now(), RequestID: "auto-tr", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"},
		ToolName: "Bash", Payload: &p, Risk: "write_outside", Action: "allow", State: "auto_allowed", RuleID: "u1", DecidedBy: "rule:u1",
		Trigger: &policy.Trigger{Segment: "echo hi > /tmp/x", Why: policy.WhyWriteOutside}})
	c.send(wire.Audit{T: wire.TAudit, Entries: []json.RawMessage{e}})
	waitFor(t, "历史行", func() bool { _, err := h.st.GetRequest("auto-tr"); return err == nil })
	if r := getReq(t, h.st, "auto-tr"); r.Trigger == nil || r.Trigger.Why != policy.WhyWriteOutside || r.Trigger.Segment != "echo hi > /tmp/x" {
		t.Fatalf("历史行的触发点: %+v", r.Trigger)
	}
}
