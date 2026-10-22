package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/wire"
	"github.com/railse/railse-opc/internal/policy"
)

type agentClient struct {
	conn *websocket.Conn
	t    *testing.T
}

func agentURL(h *testHub) string { return "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws/agent" }

func enrollMachine(t *testing.T, h *testHub, name string) (string, string) {
	t.Helper()
	tok, err := h.st.CreateEnrollToken(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": tok, "name": name, "home_dir": "/home/u"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll = %d", resp.StatusCode)
	}
	var out struct {
		MachineID string `json:"machine_id"`
		Secret    string `json:"secret"`
	}
	decodeBody(t, resp, &out)
	return out.MachineID, out.Secret
}

// dialRaw 带机器密钥连上 /ws/agent，不发任何帧。
func dialRaw(t *testing.T, h *testHub, secret string) *agentClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, agentURL(h), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial: %v (http %d)", err, code)
	}
	conn.SetReadLimit(wire.MaxFrame)
	return &agentClient{conn: conn, t: t}
}

func dialAgent(t *testing.T, h *testHub, secret string, hello wire.Hello) *agentClient {
	t.Helper()
	c := dialRaw(t, h, secret)
	hello.T = wire.THello
	c.send(hello)
	return c
}

func (c *agentClient) send(v any) {
	c.t.Helper()
	b, err := wire.Marshal(v)
	if err != nil {
		c.t.Fatalf("编码帧: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// sendRaw 原样发一个文本帧（可以不是合法 JSON）。
func (c *agentClient) sendRaw(b []byte) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// recv 读下一帧，跳过 ping（并回 pong）。
func (c *agentClient) recv(timeout time.Duration) (string, []byte) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		_, b, err := c.conn.Read(ctx)
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		kind, _ := wire.Kind(b)
		if kind == wire.TPing {
			c.send(wire.Pong{T: wire.TPong})
			continue
		}
		return kind, b
	}
}

// expectClosed 一直读到连接关闭（其间只许收到 ping，不回 pong），返回关闭状态码；不是关闭帧时为 -1。
func (c *agentClient) expectClosed(timeout time.Duration) websocket.StatusCode {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		_, b, err := c.conn.Read(ctx)
		if err != nil {
			return websocket.CloseStatus(err)
		}
		if k, _ := wire.Kind(b); k != wire.TPing {
			c.t.Fatalf("关闭前不应收到 %s", k)
		}
	}
}

// pingPongFor 在 d 内只收 ping 并回 pong；连接被断开或收到别的帧就失败。
func (c *agentClient) pingPongFor(d time.Duration) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d+5*time.Second)
	defer cancel()
	for end := time.Now().Add(d); time.Now().Before(end); {
		_, b, err := c.conn.Read(ctx)
		if err != nil {
			c.t.Fatalf("连接被断开: %v", err)
		}
		if k, _ := wire.Kind(b); k != wire.TPing {
			c.t.Fatalf("只应收到 ping, 得到 %s", k)
		}
		c.send(wire.Pong{T: wire.TPong})
	}
}

func (c *agentClient) close() { c.conn.Close(websocket.StatusNormalClosure, "bye") }

func TestAgentWSRejectsBadSecret(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for name, hdr := range map[string]http.Header{
		"错密钥":  {"Authorization": {"Bearer nope"}},
		"没有密钥": {},
	} {
		_, resp, err := websocket.Dial(ctx, agentURL(h), &websocket.DialOptions{HTTPHeader: hdr})
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s 应 401: %v %v", name, err, resp)
		}
	}
}

// 认证时存储出错回 500 而不是 401：守护进程把 401 当作密钥永久失效。
func TestAgentWSAuthStoreErrorIs500(t *testing.T) {
	h := newTestHub(t)
	_, secret := enrollMachine(t, h, "mac")
	h.st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, agentURL(h), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("存储出错应 500: %v %v", err, resp)
	}
	if !strings.Contains(h.logs.String(), "认证机器") {
		t.Fatalf("应记日志: %q", h.logs.String())
	}
}

func TestAgentWSHelloWelcome(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, AgentVersion: "0.1.0", HomeDir: "/home/u"})
	defer c.close()
	kind, b := c.recv(5 * time.Second)
	if kind != wire.TWelcome {
		t.Fatalf("第一帧应是 welcome, 得到 %s", kind)
	}
	var w wire.Welcome
	wire.Unmarshal(b, &w)
	if w.RulesVersion != 1 || !strings.Contains(w.RulesYAML, "builtin-readonly") || w.Paused {
		t.Fatalf("welcome = %+v", w)
	}
	if !h.srv.agents.connected(id) {
		t.Fatal("应登记为已连接")
	}
	m, _ := h.st.GetMachine(id)
	if m.LastSeen == nil || m.AgentVersion != "0.1.0" {
		t.Fatalf("hello 应刷新机器: %+v", m)
	}
}

// settings 的 paused 为 "1" 时 welcome.Paused 为真。
func TestAgentWSWelcomePaused(t *testing.T) {
	h := newTestHub(t)
	if err := h.st.SetSetting("paused", "1"); err != nil {
		t.Fatal(err)
	}
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	kind, b := c.recv(5 * time.Second)
	var w wire.Welcome
	if kind != wire.TWelcome || wire.Unmarshal(b, &w) != nil || !w.Paused {
		t.Fatalf("应收到 paused 的 welcome: %s %s", kind, b)
	}
}

// hello 不带 home_dir 时保留接入时登记的家目录；带了就更新。
func TestAgentWSHelloKeepsHomeDir(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, AgentVersion: "0.2.0"})
	c.recv(5 * time.Second)
	c.close()
	if m, _ := h.st.GetMachine(id); m.HomeDir != "/home/u" || m.AgentVersion != "0.2.0" {
		t.Fatalf("空 home_dir 不应覆盖已登记的家目录: %+v", m)
	}
	c = dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, AgentVersion: "0.2.0", HomeDir: "/Users/x"})
	defer c.close()
	c.recv(5 * time.Second)
	if m, _ := h.st.GetMachine(id); m.HomeDir != "/Users/x" {
		t.Fatalf("hello 的 home_dir 应更新家目录: %+v", m)
	}
}

func TestAgentWSReplacesOldConnection(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	old := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	old.recv(5 * time.Second)
	neu := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer neu.close()
	neu.recv(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := old.conn.Read(ctx); err == nil {
		t.Fatal("旧连接应被关闭")
	}
	if !h.srv.agents.connected(id) {
		t.Fatal("新连接应仍登记")
	}
	// 登记的是新连接：发给这台机器的帧到了新连接
	if !h.srv.agents.send(id, wire.Paused{T: wire.TPaused, Paused: true}) {
		t.Fatal("应能发给新连接")
	}
	if kind, _ := neu.recv(5 * time.Second); kind != wire.TPaused {
		t.Fatalf("新连接应收到 paused, 得到 %s", kind)
	}
}

func TestAgentWSPingPong(t *testing.T) {
	h := newTestHubWith(t, func(c *Config) {
		c.PingInterval = 100 * time.Millisecond
		c.PongTimeout = 400 * time.Millisecond
	}, nil)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	c.recv(2 * time.Second) // welcome
	// 不回 pong，应在 PongTimeout 后被断开
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, b, err := c.conn.Read(ctx)
		if err != nil {
			waitFor(t, "断开后注销", func() bool { return !h.srv.agents.connected(id) })
			if !strings.Contains(h.logs.String(), "没有 pong") {
				t.Fatalf("应记日志: %q", h.logs.String())
			}
			return
		}
		if k, _ := wire.Kind(b); k != wire.TPing {
			t.Fatalf("只应收到 ping, 得到 %s", k)
		}
	}
	t.Fatal("不回 pong 也没被断开")
}

// 不发 hello：helloTimeout 后以 PolicyViolation 关闭，关闭帧真正发到对端；不登记。默认 5 秒，测试缩短。
func TestAgentWSHelloTimeout(t *testing.T) {
	if h := newTestHub(t); h.srv.helloTimeout != 5*time.Second {
		t.Fatalf("默认 helloTimeout = %s，应为 5s", h.srv.helloTimeout)
	}
	const wait = 300 * time.Millisecond
	h := newTestHubWith(t, nil, func(s *Server) { s.helloTimeout = wait })
	id, secret := enrollMachine(t, h, "mac")
	start := time.Now()
	c := dialRaw(t, h, secret)
	// 不发 hello
	if code := c.expectClosed(5 * time.Second); code != websocket.StatusPolicyViolation {
		t.Fatalf("不发 hello 应收到 PolicyViolation 关闭帧, 得到 %v", code)
	}
	if d := time.Since(start); d < wait {
		t.Fatalf("应等满 helloTimeout 才关闭, 只等了 %s", d)
	}
	if h.srv.agents.connected(id) {
		t.Fatal("没 hello 不应登记")
	}
}

// 协议版本不一致：先回 Error 帧说明双方版本，再以 PolicyViolation 关闭；不登记、不刷新机器。
// 审计按（机器、守护进程版本、hub 版本）去重，一小时内只记一次；Error 帧与关闭照常。
func TestAgentWSProtoMismatch(t *testing.T) {
	var clock atomic.Int64
	clock.Store(time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC).UnixNano())
	h := newTestHubWith(t, nil, func(s *Server) { s.agents.now = func() time.Time { return time.Unix(0, clock.Load()) } })
	id, secret := enrollMachine(t, h, "mac")
	reject := func(proto int) {
		t.Helper()
		c := dialAgent(t, h, secret, wire.Hello{Proto: proto, MachineID: id, AgentVersion: "9.9.9"})
		kind, b := c.recv(5 * time.Second)
		var e wire.Error
		if kind != wire.TError || wire.Unmarshal(b, &e) != nil {
			t.Fatalf("proto %d: 第一帧应是 error, 得到 %s %s", proto, kind, b)
		}
		if want := fmt.Sprintf("协议版本 %d，hub 需要 %d", proto, wire.Proto); e.Message != want {
			t.Fatalf("proto %d: error = %q，应为 %q", proto, e.Message, want)
		}
		if code := c.expectClosed(5 * time.Second); code != websocket.StatusPolicyViolation {
			t.Fatalf("proto %d: 应以 PolicyViolation 关闭, 得到 %v", proto, code)
		}
		if h.srv.agents.connected(id) {
			t.Fatalf("proto %d: 不应登记", proto)
		}
	}
	audits := func() []map[string]int {
		t.Helper()
		evs, err := h.st.ListAudit(0, "")
		if err != nil {
			t.Fatal(err)
		}
		var got []map[string]int
		for _, e := range evs {
			if e.Action != "agent_proto_mismatch" {
				continue
			}
			if e.Actor != "agent" || e.MachineID != id {
				t.Fatalf("审计 actor/machine 不对: %+v", e)
			}
			var d map[string]int
			if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
				t.Fatalf("detail 不是 JSON: %q", e.Detail)
			}
			got = append(got, d)
		}
		return got
	}
	newer := map[string]int{"agent_proto": wire.Proto + 1, "hub_proto": wire.Proto}
	older := map[string]int{"agent_proto": 0, "hub_proto": wire.Proto}
	reject(0)
	reject(wire.Proto + 1)
	reject(wire.Proto + 1) // 同一对版本，一小时内不再记
	reject(0)
	if got, want := audits(), []map[string]int{newer, older}; fmt.Sprint(got) != fmt.Sprint(want) { // ListAudit 倒序
		t.Fatalf("审计 detail = %v，应为 %v", got, want)
	}
	clock.Add(int64(59 * time.Minute))
	reject(wire.Proto + 1)
	if n := len(audits()); n != 2 {
		t.Fatalf("不到一小时不应再记, 共 %d 条", n)
	}
	clock.Add(int64(2 * time.Minute))
	reject(wire.Proto + 1)
	if got, want := audits(), []map[string]int{newer, newer, older}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("满一小时后应再记一次: %v", got)
	}
	if m, _ := h.st.GetMachine(id); m.LastSeen != nil || m.AgentVersion != "" {
		t.Fatalf("版本不一致不应刷新机器: %+v", m)
	}
}

// 去重表顺带清掉满一小时的记录，对端乱报版本也不会让表无限增长。
func TestAgentHubProtoAuditPrunes(t *testing.T) {
	h := newAgentHub(log.New(io.Discard, "", 0))
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	for p := 2; p < 12; p++ {
		if !h.shouldAuditProto("m1", p) {
			t.Fatalf("proto %d 第一次应记", p)
		}
	}
	if h.shouldAuditProto("m1", 2) || !h.shouldAuditProto("m2", 2) {
		t.Fatal("同一机器同一版本不应重复记，不同机器各自记")
	}
	now = now.Add(time.Hour)
	if !h.shouldAuditProto("m1", 2) {
		t.Fatal("满一小时应再记")
	}
	if n := len(h.protoAudited); n != 1 {
		t.Fatalf("过期记录应被清掉, 剩 %d 条", n)
	}
}

// 吊销的机器握手回 401。
func TestAgentWSRejectsRevokedMachine(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	if err := h.st.RevokeMachine(id); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, agentURL(h), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("吊销的机器应 401: %v %v", err, resp)
	}
}

// 解析不了的帧记日志（机器 id、种类、错误，不记内容），每条连接只逐条记前 badFrameLogMax 个，断开时汇总；连接不断。
func TestAgentWSLogsBadFramesLimited(t *testing.T) {
	var logs syncBuf
	h := newTestHubWith(t, nil, func(s *Server) {
		s.log = log.New(&logs, "", 0)
		s.agents.log = s.log
	})
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	c.recv(5 * time.Second) // welcome
	const secretish = "SECRET-CONTENT-zz9"
	bad := []string{
		"nope " + secretish,
		`{"x":"` + secretish + `"}`,
		`{"t":"pending","request_id":5,"tool_name":"` + secretish + `"}`,
		`{"t":"cancel","request_id":["` + secretish + `"]}`,
		`{"t":"delivered","request_id":{}}`,
		`{"t":"expired","request_id":1}`,
		`{"t":"audit","entries":"` + secretish + `"}`,
	}
	for _, f := range bad {
		c.sendRaw([]byte(f))
	}
	lines := func() int { return strings.Count(logs.String(), "发来无法解析的帧") }
	waitFor(t, "坏帧记日志", func() bool { return lines() >= badFrameLogMax })
	if !h.srv.agents.connected(id) {
		t.Fatal("坏帧不应断开连接")
	}
	if !h.srv.agents.send(id, wire.Paused{T: wire.TPaused, Paused: true}) {
		t.Fatal("连接应仍可发帧")
	}
	if kind, _ := c.recv(5 * time.Second); kind != wire.TPaused {
		t.Fatalf("连接应仍可用, 收到 %s", kind)
	}
	c.close()
	waitFor(t, "断开时汇总", func() bool {
		return strings.Contains(logs.String(), fmt.Sprintf("共 %d 个无法解析的帧", len(bad)))
	})
	out := logs.String()
	if n := lines(); n != badFrameLogMax {
		t.Fatalf("逐条日志应只有 %d 条, 得到 %d:\n%s", badFrameLogMax, n, out)
	}
	if !strings.Contains(out, id) || !strings.Contains(out, `"pending"`) {
		t.Fatalf("日志应含机器 id 与帧种类:\n%s", out)
	}
	if strings.Contains(out, secretish) {
		t.Fatalf("日志不应含帧内容:\n%s", out)
	}
}

// 不合格的 hello 以 PolicyViolation 关闭，不登记、不刷新机器：第一帧不是 hello、机器 id 与密钥不符、字段超长。
func TestAgentWSRejectsBadHello(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	other, _ := enrollMachine(t, h, "other")
	cases := map[string]any{
		"第一帧不是 hello":      wire.Pong{T: wire.TPong},
		"机器 id 与密钥不符":      wire.Hello{T: wire.THello, Proto: wire.Proto, MachineID: other},
		"agent_version 超长": wire.Hello{T: wire.THello, Proto: wire.Proto, MachineID: id, AgentVersion: strings.Repeat("v", maxAgentVersionLen+1)},
		"home_dir 超长":      wire.Hello{T: wire.THello, Proto: wire.Proto, MachineID: id, HomeDir: "/" + strings.Repeat("d", maxHomeDirLen)},
	}
	for name, first := range cases {
		c := dialRaw(t, h, secret)
		c.send(first)
		if code := c.expectClosed(5 * time.Second); code != websocket.StatusPolicyViolation {
			t.Fatalf("%s: 应以 PolicyViolation 关闭, 得到 %v", name, code)
		}
		if h.srv.agents.connected(id) || h.srv.agents.connected(other) {
			t.Fatalf("%s: 不应登记", name)
		}
	}
	for _, mid := range []string{id, other} {
		if m, _ := h.st.GetMachine(mid); m.LastSeen != nil || m.HomeDir != "/home/u" {
			t.Fatalf("不合格的 hello 不应刷新机器: %+v", m)
		}
	}
	// 恰好在上限内的照常接受
	ver, home := strings.Repeat("v", maxAgentVersionLen), "/"+strings.Repeat("d", maxHomeDirLen-1)
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, AgentVersion: ver, HomeDir: home})
	defer c.close()
	if kind, _ := c.recv(5 * time.Second); kind != wire.TWelcome {
		t.Fatalf("上限内的 hello 应收到 welcome, 得到 %s", kind)
	}
	if m, _ := h.st.GetMachine(id); m.AgentVersion != ver || m.HomeDir != home {
		t.Fatal("上限内的 hello 应刷新机器")
	}
}

// 接近 1 MiB 的合法帧能收下（库默认读上限只有 32 KiB）：发完大帧后连接撑过两倍 PongTimeout，
// 说明服务端读过了大帧之后的 pong；大帧被拒时连接会立即关闭。
func TestAgentWSAcceptsLargeFrame(t *testing.T) {
	h := newTestHubWith(t, func(c *Config) {
		c.PingInterval = 20 * time.Millisecond
		// 处理这一帧要对近 1 MiB 的消息全文脱敏，-race 下要几秒，期间读循环读不到 pong
		c.PongTimeout = 5 * time.Second
	}, nil)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second) // welcome
	entry := `{"ts":"2026-01-02T00:00:00Z","kind":"control","state":"recorded","message":"` + strings.Repeat("x", wire.MaxFrame-200) + `"}`
	frame := wire.Audit{T: wire.TAudit, Entries: []json.RawMessage{json.RawMessage(entry)}}
	b, err := wire.Marshal(frame)
	if err != nil || len(b) <= wire.MaxFrame-200 {
		t.Fatalf("测试帧应接近 MaxFrame: %d %v", len(b), err)
	}
	c.send(frame)
	c.pingPongFor(time.Second)
	if !h.srv.agents.connected(id) {
		t.Fatal("收下大帧后连接应仍在")
	}
}

func newFakeConn(id string, queue int) *agentConn {
	return &agentConn{machine: store.Machine{ID: id, Name: "m-" + id}, out: make(chan []byte, queue), closed: make(chan struct{})}
}

// 同一机器再登记会关掉旧连接；旧连接退出时的注销不能删掉新连接。
func TestAgentHubRegisterReplace(t *testing.T) {
	h := newAgentHub(log.New(io.Discard, "", 0))
	a, b, other := newFakeConn("m1", 1), newFakeConn("m1", 1), newFakeConn("m2", 1)
	h.register(a)
	h.register(other)
	h.register(b)
	if !a.isClosed() || b.isClosed() || other.isClosed() {
		t.Fatal("登记新连接应只关掉同一机器的旧连接")
	}
	ids := h.connectedIDs()
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"m1", "m2"}) {
		t.Fatalf("connectedIDs = %v", ids)
	}
	h.unregister(a)
	if !h.connected("m1") {
		t.Fatal("旧连接注销不应删掉新连接")
	}
	if !h.send("m1", wire.Ping{T: wire.TPing}) || len(b.out) != 1 || len(a.out) != 0 {
		t.Fatal("帧应排进新连接")
	}
	h.unregister(b)
	if h.connected("m1") || !b.isClosed() {
		t.Fatal("注销后应未连接且已关闭")
	}
	if h.send("m1", wire.Ping{T: wire.TPing}) {
		t.Fatal("未连接时 send 应返回 false")
	}
	h.closeAll()
	if !other.isClosed() || h.connected("m2") || len(h.connectedIDs()) != 0 {
		t.Fatal("closeAll 应关掉并清空全部连接")
	}
}

// 队列满时 send 不阻塞：返回 false、记日志并断开连接；已关闭的连接 send 返回 false，不 panic。
func TestAgentHubSendFullQueue(t *testing.T) {
	var logs syncBuf
	h := newAgentHub(log.New(&logs, "", 0))
	c := newFakeConn("m1", 1)
	h.register(c)
	if !h.send("m1", wire.Ping{T: wire.TPing}) {
		t.Fatal("队列有空位时应发出")
	}
	done := make(chan bool, 1)
	go func() { done <- h.send("m1", wire.Ping{T: wire.TPing}) }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("队列满时应返回 false")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("队列满时 send 不应阻塞")
	}
	if !c.isClosed() {
		t.Fatal("队列满应断开连接")
	}
	if !strings.Contains(logs.String(), "发送队列已满") {
		t.Fatalf("应记日志: %q", logs.String())
	}
	<-c.out // 腾出空位：已关闭的连接也不再收帧
	if h.send("m1", wire.Ping{T: wire.TPing}) {
		t.Fatal("已关闭的连接 send 应返回 false")
	}
	c.close() // 重复关闭不 panic

	d := newFakeConn("m2", 4)
	h.register(d)
	if h.send("m2", wire.Error{T: wire.TError, Message: strings.Repeat("x", wire.MaxFrame)}) {
		t.Fatal("超过 MaxFrame 的帧应返回 false")
	}
	if d.isClosed() || len(d.out) != 0 {
		t.Fatal("编码失败不应断开连接，也不应排进队列")
	}
}

// hub 只能下发 welcome、decision、rules、paused、ping、pong、error；别的类型、t 与类型不符、指针一律拒绝并记日志。
func TestAgentHubSendWhitelist(t *testing.T) {
	var logs syncBuf
	h := newAgentHub(log.New(&logs, "", 0))
	c := newFakeConn("m1", 16)
	h.register(c)
	allowed := []any{
		wire.Welcome{T: wire.TWelcome}, wire.Decision{T: wire.TDecision, RequestID: "r", Action: "allow"},
		wire.Rules{T: wire.TRules}, wire.Paused{T: wire.TPaused}, wire.Ping{T: wire.TPing}, wire.Pong{T: wire.TPong},
		wire.Error{T: wire.TError, Message: "x"},
	}
	for _, f := range allowed {
		if !h.send("m1", f) {
			t.Fatalf("%T 应允许下发", f)
		}
	}
	denied := []any{
		wire.PendingRequest{T: wire.TPending}, wire.Hello{T: wire.THello}, wire.Audit{T: wire.TAudit},
		wire.Cancel{T: wire.TCancel}, wire.Expired{T: wire.TExpired}, wire.Delivered{T: wire.TDelivered},
		wire.Decision{T: wire.TPending}, wire.Paused{}, &wire.Decision{T: wire.TDecision},
		map[string]string{"t": wire.TDecision}, struct {
			T   string `json:"t"`
			Cmd string `json:"cmd"`
		}{"exec", "rm -rf /"},
	}
	for _, f := range denied {
		if h.send("m1", f) {
			t.Fatalf("%T %+v 不应允许下发", f, f)
		}
	}
	if len(c.out) != len(allowed) || c.isClosed() {
		t.Fatalf("被拒的帧不应入队，也不应断开连接: 队列 %d", len(c.out))
	}
	if n := strings.Count(logs.String(), "拒绝下发"); n != len(denied) {
		t.Fatalf("每个被拒的帧应记一条日志, 得到 %d:\n%s", n, logs.String())
	}
}

// 连接已被要求断开（被替换、吊销、关停）后读到的帧不再分发，坏帧也不记；未断开时照常处理并更新 pong 时间。
func TestHandleAgentFrameSkipsClosedConn(t *testing.T) {
	var logs syncBuf
	s := &Server{log: log.New(&logs, "", 0)}
	c := newFakeConn("m1", 1)
	pc := newPongClock()
	bad := &badFrameLog{log: s.log, machineID: "m1"}
	if !s.handleAgentFrame(c, []byte("nope"), pc, bad) || !strings.Contains(logs.String(), "无法解析的帧") {
		t.Fatal("未断开的连接上坏帧应记日志并继续")
	}
	time.Sleep(time.Millisecond)
	if !s.handleAgentFrame(c, []byte(`{"t":"pong"}`), pc, bad) || pc.last.Load() == 0 {
		t.Fatal("pong 应更新时间")
	}
	c.close()
	before := logs.String()
	if s.handleAgentFrame(c, []byte("nope"), pc, bad) {
		t.Fatal("已断开的连接应停止读循环")
	}
	if logs.String() != before || bad.n != 1 {
		t.Fatal("已断开的连接上的帧不应再处理")
	}
}

// fakeWelcomeSource 是可注入错误的 welcome 数据源。
type fakeWelcomeSource struct {
	paused     string
	hasPaused  bool
	settingErr error
	rulesErr   error
}

func (f fakeWelcomeSource) RuleSet() (policy.RuleSet, int, error) {
	return policy.RuleSet{Version: policy.RuleSetVersion}, 3, f.rulesErr
}

func (f fakeWelcomeSource) GetSetting(key string) (string, bool, error) {
	if key != "paused" {
		return "", false, fmt.Errorf("意外的设置 %s", key)
	}
	return f.paused, f.hasPaused, f.settingErr
}

// 暂停设置读失败或值损坏时 welcome 组装失败（握手以 InternalError 关闭，宁可不连也不当作未暂停）。
func TestWelcomeFramePaused(t *testing.T) {
	for name, c := range map[string]struct {
		src    fakeWelcomeSource
		paused bool
		err    bool
	}{
		"没有设置":  {fakeWelcomeSource{}, false, false},
		"暂停":    {fakeWelcomeSource{paused: "1", hasPaused: true}, true, false},
		"未暂停":   {fakeWelcomeSource{paused: "0", hasPaused: true}, false, false},
		"值损坏":   {fakeWelcomeSource{paused: "yes", hasPaused: true}, false, true},
		"读设置失败": {fakeWelcomeSource{settingErr: errors.New("磁盘坏了")}, false, true},
		"读规则失败": {fakeWelcomeSource{rulesErr: errors.New("磁盘坏了")}, false, true},
	} {
		b, err := welcomeFrame(c.src)
		if c.err {
			if err == nil {
				t.Fatalf("%s: 应报错", name)
			}
			continue
		}
		var w wire.Welcome
		if err != nil || wire.Unmarshal(b, &w) != nil || w.T != wire.TWelcome || w.RulesVersion != 3 || w.Paused != c.paused {
			t.Fatalf("%s: welcome = %+v, %v", name, w, err)
		}
	}
}

// sendTo 只发给仍是该机器当前连接的那一条：被替换、已关闭、未登记的连接都返回 false；下发白名单照旧。
func TestAgentHubSendTo(t *testing.T) {
	var logs syncBuf
	h := newAgentHub(log.New(&logs, "", 0))
	old, cur := newFakeConn("m1", 4), newFakeConn("m1", 4)
	h.register(old)
	if !h.sendTo(old, wire.Ping{T: wire.TPing}) || len(old.out) != 1 {
		t.Fatal("当前连接应能发")
	}
	h.register(cur) // 替换并关掉 old
	if h.sendTo(old, wire.Ping{T: wire.TPing}) || len(old.out) != 1 || len(cur.out) != 0 {
		t.Fatal("被替换的连接不应收帧，帧也不应落到新连接")
	}
	if h.sendTo(newFakeConn("m1", 4), wire.Ping{T: wire.TPing}) || len(cur.out) != 0 {
		t.Fatal("未登记的连接不应发")
	}
	if h.sendTo(cur, wire.Hello{T: wire.THello}) || len(cur.out) != 0 {
		t.Fatal("白名单外的帧不应发")
	}
	if !h.sendTo(cur, wire.Decision{T: wire.TDecision, RequestID: "r", Action: "allow"}) || len(cur.out) != 1 {
		t.Fatal("当前连接应能发决定")
	}
}
