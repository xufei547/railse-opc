package hub

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/proto"
)

// trustAgent 接入一台机器并以 features 连上，读掉 welcome 并返回它。
func trustAgent(t *testing.T, h *testHub, name string, features []string) (string, string, *agentClient, wire.Welcome) {
	t.Helper()
	id, secret := enrollMachine(t, h, name)
	c, w := redial(t, h, id, secret, features)
	return id, secret, c, w
}

// redial 用已有的机器密钥再连一次，读掉 welcome 并返回它。
func redial(t *testing.T, h *testHub, id, secret string, features []string) (*agentClient, wire.Welcome) {
	t.Helper()
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, HomeDir: "/home/u", Features: features})
	t.Cleanup(c.close)
	kind, b := c.recv(5 * time.Second)
	if kind != wire.TWelcome {
		t.Fatalf("第一帧应是 welcome, 得到 %s", kind)
	}
	var w wire.Welcome
	if err := wire.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "对齐完成", func() bool { return h.srv.trustSupported(id) == slices.Contains(features, wire.FeatureTrust) })
	return c, w
}

// expectFrame 读下一帧（跳过 ping），断言种类并解码进 v。
func (c *agentClient) expectFrame(kind string, v any) {
	c.t.Helper()
	got, b := c.recv(5 * time.Second)
	if got != kind {
		c.t.Fatalf("应收到 %s, 得到 %s: %s", kind, got, b)
	}
	if v != nil {
		if err := wire.Unmarshal(b, v); err != nil {
			c.t.Fatal(err)
		}
	}
}

// expectNothing 断言此前没有排给这台机器的帧：发一个 id 不合规的 pending 当屏障，hub 按序处理、经同一队列回 Error，
// 下一帧就该是这个 Error（读超时会关掉连接，不能靠干等）。
func (c *agentClient) expectNothing() {
	c.t.Helper()
	c.send(wire.PendingRequest{T: wire.TPending, RequestID: "barrier!"})
	var e wire.Error
	c.expectFrame(wire.TError, &e)
	if e.RequestID != "barrier!" {
		c.t.Fatalf("应只收到屏障的回应: %+v", e)
	}
}

// getJSON 以登录 cookie GET path，断言 200 并解码。
func getJSON(t *testing.T, h *testHub, ck *http.Cookie, path string, v any) {
	t.Helper()
	resp := doReq(t, "GET", h.http.URL+path, nil, "", ck)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d", path, resp.StatusCode)
	}
	decodeBody(t, resp, v)
}

// errText 读错误回应的 error 字段。
func errText(t *testing.T, resp *http.Response) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	decodeBody(t, resp, &e)
	return e.Error
}

func requestView1(t *testing.T, h *testHub, ck *http.Cookie, id string) map[string]any {
	t.Helper()
	var out struct {
		Request map[string]any `json:"request"`
	}
	getJSON(t, h, ck, "/api/requests/"+id, &out)
	return out.Request
}

// 手机批准时带 trust：hub 建放行、先推快照再下发 allow；GET /api/trusts 与 /api/status 看得到；DELETE 后守护进程收到不含它的快照。
func TestDecideWithTrustAndRevoke(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, _, c, w := trustAgent(t, h, "mac", []string{wire.FeatureTrust})
	if !w.Has(wire.FeatureTrust) || len(w.Trusts) != 0 {
		t.Fatalf("welcome 应声明 trust、快照为空: %+v", w)
	}
	c.send(pendingFrame("r1", "sess-1", "t1", "git push origin main"))
	waitFor(t, "r1 登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	if v := requestView1(t, h, ck, "r1"); v["can_trust"] != true {
		t.Fatalf("守护进程声明了 trust，视图应 can_trust: %v", v["can_trust"])
	}
	var inbox struct {
		Pending []map[string]any `json:"pending"`
	}
	getJSON(t, h, ck, "/api/inbox", &inbox)
	if len(inbox.Pending) != 1 || inbox.Pending[0]["can_trust"] != true {
		t.Fatalf("收件箱也应 can_trust: %v", inbox.Pending)
	}

	before := time.Now()
	resp := apiPost(t, h, ck, "/api/requests/r1/decide", map[string]any{"action": "allow", "trust": map[string]any{"scope": "session", "minutes": 120}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decide = %d: %s", resp.StatusCode, errText(t, resp))
	}
	var snap wire.Trusts
	c.expectFrame(wire.TTrusts, &snap) // 快照先到
	if len(snap.Trusts) != 1 {
		t.Fatalf("快照: %+v", snap.Trusts)
	}
	tr := snap.Trusts[0]
	if tr.ID == "" || tr.MachineID != id || tr.Scope != "session" || tr.Tool != "claude" || tr.CLISession != "sess-1" || tr.CreatedBy != "phone" ||
		tr.Until.Before(before.Add(2*time.Hour-time.Second)) || tr.Until.After(time.Now().Add(2*time.Hour)) {
		t.Fatalf("快照里的放行: %+v", tr)
	}
	var d wire.Decision
	c.expectFrame(wire.TDecision, &d) // 再是 allow
	if d.RequestID != "r1" || d.Action != "allow" {
		t.Fatalf("决定: %+v", d)
	}
	if e, ok := lastAudit(t, h.st, "trust_set"); !ok || e.Actor != "user" || e.MachineID != id || !strings.Contains(e.Detail, tr.ID) || e.RequestID != "r1" {
		t.Fatalf("应审计 trust_set: %+v", e)
	}

	var list []map[string]any
	getJSON(t, h, ck, "/api/trusts", &list)
	if len(list) != 1 || list[0]["id"] != tr.ID || list[0]["machine_name"] != "mac" || list[0]["project"] != "p" || list[0]["cli_session_id"] != "sess-1" {
		t.Fatalf("GET /api/trusts: %v", list)
	}
	if rem, _ := list[0]["remaining_seconds"].(float64); rem < 7100 || rem > 7200 {
		t.Fatalf("remaining_seconds = %v", list[0]["remaining_seconds"])
	}
	var st map[string]any
	getJSON(t, h, ck, "/api/status", &st)
	if st["trusts"] != float64(1) {
		t.Fatalf("status.trusts = %v", st["trusts"])
	}

	if resp := doReq(t, "DELETE", h.http.URL+"/api/trusts/"+tr.ID, nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE = %d", resp.StatusCode)
	}
	c.expectFrame(wire.TTrusts, &snap)
	if len(snap.Trusts) != 0 {
		t.Fatalf("撤销后快照应为空: %+v", snap.Trusts)
	}
	getJSON(t, h, ck, "/api/trusts", &list)
	if len(list) != 0 {
		t.Fatalf("撤销后列表应为空: %v", list)
	}
	if e, ok := lastAudit(t, h.st, "trust_revoke"); !ok || e.Actor != "user" || e.MachineID != id || !strings.Contains(e.Detail, tr.ID) {
		t.Fatalf("应审计 trust_revoke: %+v", e)
	}
	if resp := doReq(t, "DELETE", h.http.URL+"/api/trusts/"+tr.ID, nil, "", ck); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("再撤应 404: %d", resp.StatusCode)
	}
	// 跨站的 DELETE 被拒
	if resp := doReqHdr(t, "DELETE", h.http.URL+"/api/trusts/x", nil, "", ck, http.Header{"Origin": {"https://evil.example"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("跨站 DELETE 应 403: %d", resp.StatusCode)
	}
}

// 项目范围：放行的是请求的项目根；时长超 24 小时截断，缺省为 2 小时。
func TestDecideTrustProjectClamped(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	_, _, c, _ := trustAgent(t, h, "mac", []string{wire.FeatureTrust})
	c.send(pendingFrame("r1", "s1", "t1", "git push origin main"))
	c.send(pendingFrame("r2", "s1", "t2", "git push origin dev"))
	waitFor(t, "登记", func() bool { n, _ := h.st.CountPending(); return n == 2 })

	resp := apiPost(t, h, ck, "/api/requests/r1/decide", map[string]any{"action": "allow", "trust": map[string]any{"scope": "project", "minutes": 100000}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decide = %d: %s", resp.StatusCode, errText(t, resp))
	}
	var snap wire.Trusts
	c.expectFrame(wire.TTrusts, &snap)
	if len(snap.Trusts) != 1 || snap.Trusts[0].Scope != "project" || snap.Trusts[0].Project != "/p" || snap.Trusts[0].CLISession != "" ||
		snap.Trusts[0].Until.After(time.Now().Add(proto.TrustMax)) || snap.Trusts[0].Until.Before(time.Now().Add(proto.TrustMax-time.Minute)) {
		t.Fatalf("应截到 24 小时: %+v", snap.Trusts)
	}
	c.expectFrame(wire.TDecision, nil)

	before := time.Now()
	resp = apiPost(t, h, ck, "/api/requests/r2/decide", map[string]any{"action": "allow", "trust": map[string]any{"scope": "session"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decide = %d: %s", resp.StatusCode, errText(t, resp))
	}
	c.expectFrame(wire.TTrusts, &snap)
	var sess proto.Trust
	for _, tr := range snap.Trusts {
		if tr.Scope == "session" {
			sess = tr
		}
	}
	if len(snap.Trusts) != 2 || sess.Until.Before(before.Add(proto.TrustDefault-time.Second)) || sess.Until.After(time.Now().Add(proto.TrustDefault)) {
		t.Fatalf("缺省时长应为 2 小时: %+v", snap.Trusts)
	}
}

// 守护进程不声明 trust：视图 can_trust 为 false，decide 带 trust 回 400（文案说守护进程太旧），决定不落库、什么也不下发；
// 不带 trust 照常批准。别的不合规（拒绝时带、范围不对、时长太短、缺会话或项目根、机器不在线、满 20 条）同样 400 且不落库。
func TestDecideTrustRefusedForOldAgent(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	oldID, _, old, _ := trustAgent(t, h, "old", nil)
	old.send(pendingFrame("r1", "s1", "t1", "git push origin main"))
	waitFor(t, "r1 登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	if v := requestView1(t, h, ck, "r1"); v["can_trust"] != false {
		t.Fatalf("老守护进程的请求 can_trust 应为 false: %v", v["can_trust"])
	}
	resp := apiPost(t, h, ck, "/api/requests/r1/decide", map[string]any{"action": "allow", "trust": map[string]any{"scope": "session", "minutes": 120}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("老守护进程带 trust 应 400: %d", resp.StatusCode)
	}
	if msg := errText(t, resp); !strings.Contains(msg, "守护进程太旧") {
		t.Fatalf("文案应说守护进程太旧: %q", msg)
	}
	if r := getReq(t, h.st, "r1"); r.State != "pending" {
		t.Fatalf("决定不应落库: %s", r.State)
	}
	old.expectNothing()
	if l, _ := h.st.ListTrusts(""); len(l) != 0 {
		t.Fatalf("不应建放行: %+v", l)
	}
	if resp := apiPost(t, h, ck, "/api/requests/r1/decide", map[string]any{"action": "allow"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("不带 trust 应照常批准: %d", resp.StatusCode)
	}
	old.expectFrame(wire.TDecision, nil)

	// 声明了 trust 的机器上的各种不合规
	newID, _, c, _ := trustAgent(t, h, "new", []string{wire.FeatureTrust})
	insertReq(t, h.st, store.Request{ID: "nosess", MachineID: newID, State: "pending", ProjectRoot: "/p"})
	insertReq(t, h.st, store.Request{ID: "noroot", MachineID: newID, State: "pending", CLISessionID: "s9",
		SessionID: h.srv.upsertSession(newID, proto.Session{Tool: "claude", CLISessionID: "s9"}, "")})
	old.close()
	waitFor(t, "老机器断开", func() bool { return !h.srv.agents.connected(oldID) })
	insertReq(t, h.st, store.Request{ID: "offline", MachineID: oldID, State: "pending", ProjectRoot: "/p"})
	c.send(pendingFrame("r2", "s1", "t2", "git push origin main"))
	waitFor(t, "r2 登记", func() bool { _, err := h.st.GetRequest("r2"); return err == nil })
	for name, tc := range map[string]struct {
		id   string
		body map[string]any
		want string
	}{
		"拒绝时带":   {"r2", map[string]any{"action": "deny", "trust": map[string]any{"scope": "session"}}, "只有批准"},
		"范围不对":   {"r2", map[string]any{"action": "allow", "trust": map[string]any{"scope": "global"}}, "session 或 project"},
		"时长太短":   {"r2", map[string]any{"action": "allow", "trust": map[string]any{"scope": "session", "minutes": 4}}, "至少 5 分钟"},
		"负数时长":   {"r2", map[string]any{"action": "allow", "trust": map[string]any{"scope": "session", "minutes": -10}}, "至少 5 分钟"},
		"没有会话编号": {"nosess", map[string]any{"action": "allow", "trust": map[string]any{"scope": "session"}}, "没有会话编号"},
		"没有项目根":  {"noroot", map[string]any{"action": "allow", "trust": map[string]any{"scope": "project"}}, "没有项目根"},
		"机器离线":   {"offline", map[string]any{"action": "allow", "trust": map[string]any{"scope": "project"}}, "不能限时放行"},
	} {
		resp := apiPost(t, h, ck, "/api/requests/"+tc.id+"/decide", tc.body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: 应 400, 得到 %d", name, resp.StatusCode)
		}
		if msg := errText(t, resp); !strings.Contains(msg, tc.want) {
			t.Fatalf("%s: 文案 %q 应含 %q", name, msg, tc.want)
		}
		if r := getReq(t, h.st, tc.id); r.State != "pending" {
			t.Fatalf("%s: 决定不应落库: %s", name, r.State)
		}
	}
	// 满 20 条：拒建，决定不落库
	for i := range proto.TrustPerMachine {
		if _, err := h.st.UpsertTrust(proto.Trust{ID: fmt.Sprintf("x%d", i), MachineID: newID, Scope: "project", Project: fmt.Sprintf("/q%d", i),
			Until: time.Now().Add(time.Hour), CreatedBy: "cli"}); err != nil {
			t.Fatal(err)
		}
	}
	resp = apiPost(t, h, ck, "/api/requests/r2/decide", map[string]any{"action": "allow", "trust": map[string]any{"scope": "session"}})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("满 20 条应 400: %d", resp.StatusCode)
	}
	if msg := errText(t, resp); !strings.Contains(msg, "20") {
		t.Fatalf("满 20 条的文案: %q", msg)
	}
	if r := getReq(t, h.st, "r2"); r.State != "pending" {
		t.Fatalf("满 20 条时决定不应落库: %s", r.State)
	}
	c.expectNothing()
	// 已处理过的请求带 trust：照旧 409，不建放行
	resp = apiPost(t, h, ck, "/api/requests/r1/decide", map[string]any{"action": "allow", "trust": map[string]any{"scope": "session"}})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("已处理的应 409: %d", resp.StatusCode)
	}
}

// 守护进程报来的 trust 帧（cli/env 建的）入库、网页看得到、hub 回推快照；expired/revoke 帧让它消失。时长超界由 hub 截；
// 不合规的与超过 20 条的不收。机器 id 以连接为准，不照帧里的。
func TestAgentTrustFramesStored(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, _, c, _ := trustAgent(t, h, "mac", []string{wire.FeatureTrust})
	set := func(tr proto.Trust) { c.send(wire.TrustChange{T: wire.TTrust, Op: "set", Trust: tr}) }
	far := time.Now().Add(48 * time.Hour)
	set(proto.Trust{ID: "c1", MachineID: "someone-else", Scope: "project", Project: "/home/u/proj", Until: far, CreatedAt: time.Now(), CreatedBy: "cli", Note: "部署"})
	var snap wire.Trusts
	c.expectFrame(wire.TTrusts, &snap) // 收下后回推快照：守护进程据此标已同步
	if len(snap.Trusts) != 1 || snap.Trusts[0].ID != "c1" || snap.Trusts[0].MachineID != id || snap.Trusts[0].CreatedBy != "cli" ||
		snap.Trusts[0].Until.After(time.Now().Add(proto.TrustMax)) {
		t.Fatalf("快照: %+v", snap.Trusts)
	}
	set(proto.Trust{ID: "e1", Scope: "session", Tool: "claude", CLISession: "s1", Until: time.Now().Add(time.Hour), CreatedBy: "env"})
	c.expectFrame(wire.TTrusts, &snap)
	if len(snap.Trusts) != 2 {
		t.Fatalf("快照: %+v", snap.Trusts)
	}
	var list []map[string]any
	getJSON(t, h, ck, "/api/trusts", &list)
	if len(list) != 2 {
		t.Fatalf("GET /api/trusts: %v", list)
	}
	byID := map[string]map[string]any{}
	for _, x := range list {
		byID[x["id"].(string)] = x
	}
	if byID["c1"]["project"] != "proj" || byID["c1"]["machine_name"] != "mac" || byID["c1"]["note"] != "部署" || byID["e1"]["created_by"] != "env" {
		t.Fatalf("列表: %v", list)
	}

	// 不合规的不收，也不回推快照
	for _, bad := range []proto.Trust{
		{ID: "b1", Scope: "global", Until: far, CreatedBy: "cli"},
		{ID: "b2", Scope: "session", Tool: "claude", Until: far, CreatedBy: "env"},
		{ID: "b3", Scope: "project", Until: far, CreatedBy: "cli"},
		{ID: "b4", Scope: "project", Project: "/p", Until: time.Now().Add(-time.Minute), CreatedBy: "cli"},
		{ID: "", Scope: "project", Project: "/p", Until: far, CreatedBy: "cli"},
		{ID: strings.Repeat("x", 200), Scope: "project", Project: "/p", Until: far, CreatedBy: "cli"},
		{ID: "b5", Scope: "session", Tool: "claude", CLISession: strings.Repeat("s", 300), Until: far, CreatedBy: "env"},
	} {
		set(bad)
	}
	c.send(wire.TrustChange{T: wire.TTrust, Op: "bogus", Trust: proto.Trust{ID: "c1"}})
	c.expectNothing()
	if l, _ := h.st.ListTrusts(id); len(l) != 2 {
		t.Fatalf("不合规的不应入库: %+v", l)
	}

	// expired 与 revoke 帧
	c.send(wire.TrustChange{T: wire.TTrust, Op: "expired", Trust: proto.Trust{ID: "e1"}})
	c.expectFrame(wire.TTrusts, &snap)
	c.send(wire.TrustChange{T: wire.TTrust, Op: "revoke", Trust: proto.Trust{ID: "c1"}})
	c.expectFrame(wire.TTrusts, &snap)
	if len(snap.Trusts) != 0 {
		t.Fatalf("撤后快照应为空: %+v", snap.Trusts)
	}
	getJSON(t, h, ck, "/api/trusts", &list)
	if len(list) != 0 {
		t.Fatalf("撤后列表应为空: %v", list)
	}

	// 超过 20 条的不收；同 id 的更新照收
	for i := range proto.TrustPerMachine {
		if _, err := h.st.UpsertTrust(proto.Trust{ID: fmt.Sprintf("x%d", i), MachineID: id, Scope: "project", Project: fmt.Sprintf("/q%d", i),
			Until: time.Now().Add(time.Hour), CreatedBy: "cli"}); err != nil {
			t.Fatal(err)
		}
	}
	set(proto.Trust{ID: "over", Scope: "project", Project: "/p", Until: far, CreatedBy: "cli"})
	c.expectNothing()
	if l, _ := h.st.ListTrusts(id); len(l) != proto.TrustPerMachine {
		t.Fatalf("超过 20 条不应入库: %d", len(l))
	}
	set(proto.Trust{ID: "x0", Scope: "project", Project: "/q0", Until: time.Now().Add(3 * time.Hour), CreatedBy: "cli"})
	c.expectFrame(wire.TTrusts, &snap)
	if len(snap.Trusts) != proto.TrustPerMachine {
		t.Fatalf("同 id 更新后仍是 20 条: %d", len(snap.Trusts))
	}

	// 别的机器的放行，这台撤不掉
	otherID, _, _, _ := trustAgent(t, h, "other", []string{wire.FeatureTrust})
	if _, err := h.st.UpsertTrust(proto.Trust{ID: "o1", MachineID: otherID, Scope: "project", Project: "/p", Until: far, CreatedBy: "cli"}); err != nil {
		t.Fatal(err)
	}
	c.send(wire.TrustChange{T: wire.TTrust, Op: "revoke", Trust: proto.Trust{ID: "o1"}})
	c.expectNothing()
	if l, _ := h.st.ListTrusts(otherID); len(l) != 1 {
		t.Fatalf("别的机器的放行不应被撤: %+v", l)
	}
}

// 机器离线时 DELETE 也成功，重连后 welcome 的快照里没有它（另一条还在）。
func TestTrustRevokeOffline(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret, c, _ := trustAgent(t, h, "mac", []string{wire.FeatureTrust})
	for _, tid := range []string{"keep", "gone"} {
		c.send(wire.TrustChange{T: wire.TTrust, Op: "set", Trust: proto.Trust{ID: tid, Scope: "project", Project: "/p/" + tid,
			Until: time.Now().Add(time.Hour), CreatedBy: "cli"}})
		c.expectFrame(wire.TTrusts, nil)
	}
	c.close()
	waitFor(t, "断开", func() bool { return !h.srv.agents.connected(id) })
	if resp := doReq(t, "DELETE", h.http.URL+"/api/trusts/gone", nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("离线时 DELETE = %d", resp.StatusCode)
	}
	_, w := redial(t, h, id, secret, []string{wire.FeatureTrust})
	if len(w.Trusts) != 1 || w.Trusts[0].ID != "keep" || w.Trusts[0].MachineID != id {
		t.Fatalf("重连后 welcome 的快照: %+v", w.Trusts)
	}
}

// 每分钟的清理：到期的标撤、审计 trust_expired，并向网页广播 status（带 trusts 条数）。
func TestSweepTrusts(t *testing.T) {
	h := newTestHub(t)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	for id, until := range map[string]time.Time{"old": time.Now().Add(-time.Second), "live": time.Now().Add(time.Hour)} {
		if _, err := h.st.UpsertTrust(proto.Trust{ID: id, MachineID: m.ID, Scope: "project", Project: "/p", Until: until, CreatedBy: "cli"}); err != nil {
			t.Fatal(err)
		}
	}
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	h.srv.expireTrusts()
	if e, ok := lastAudit(t, h.st, "trust_expired"); !ok || e.Actor != "system" || e.MachineID != m.ID || !strings.Contains(e.Detail, `"old"`) {
		t.Fatalf("应审计 trust_expired: %+v", e)
	}
	b := nextBroadcast(t, sub)
	if !contains(b, `"type":"status"`) || !contains(b, `"trusts":1`) {
		t.Fatalf("应广播 status: %s", b)
	}
	// 没有到期的不广播
	h.srv.expireTrusts()
	select {
	case b := <-sub:
		t.Fatalf("没有到期的不应广播: %s", b)
	case <-time.After(100 * time.Millisecond):
	}
}

// /api/version 声明 hub 的能力（网页据此决定显不显示放行相关的界面）。
func TestVersionFeatures(t *testing.T) {
	var v struct {
		Features []string `json:"features"`
	}
	decodeBody(t, doReq(t, "GET", newTestHub(t).http.URL+"/api/version", nil, "", nil), &v)
	if !slices.Contains(v.Features, wire.FeatureTrust) || !slices.Contains(v.Features, wire.FeatureAsk) {
		t.Fatalf("features = %v", v.Features)
	}
}

// bigTrustSource 给出一条大到 welcome 装不下的放行。
type bigTrustSource struct{ fakeWelcomeSource }

func (bigTrustSource) ListTrusts(string) ([]proto.Trust, error) {
	return []proto.Trust{{ID: "big", Scope: "project", Project: strings.Repeat("p", wire.MaxFrame), Until: time.Now().Add(time.Hour)}}, nil
}

// welcome 带上放行就超过单帧上限时不带，告诉调用方单发；没要放行（守护进程没声明 trust）时不读放行。
func TestWelcomeFrameTrusts(t *testing.T) {
	b, dropped, err := welcomeFrame(bigTrustSource{}, "m1")
	if err != nil || !dropped || strings.Contains(string(b), `"trusts"`) {
		t.Fatalf("超限时应不带放行: dropped=%v err=%v", dropped, err)
	}
	if _, dropped, err := welcomeFrame(fakeWelcomeSource{trustsErr: errors.New("坏了")}, ""); err != nil || dropped {
		t.Fatalf("没要放行时不应读它: %v %v", dropped, err)
	}
}

// 吊销机器时撤掉它的放行（审计 trust_revoke），列表与 status 里都不再有。
func TestMachineRevokeRevokesTrusts(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.UpsertTrust(proto.Trust{ID: "t1", MachineID: m.ID, Scope: "project", Project: "/p", Until: time.Now().Add(time.Hour), CreatedBy: "cli"}); err != nil {
		t.Fatal(err)
	}
	if resp := doReq(t, "DELETE", h.http.URL+"/api/machines/"+m.ID, nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("吊销 = %d", resp.StatusCode)
	}
	if l, _ := h.st.ListTrusts(""); len(l) != 0 {
		t.Fatalf("吊销后放行应撤掉: %+v", l)
	}
	if e, ok := lastAudit(t, h.st, "trust_revoke"); !ok || e.MachineID != m.ID || !strings.Contains(e.Detail, `"t1"`) {
		t.Fatalf("应审计 trust_revoke: %+v", e)
	}
}
