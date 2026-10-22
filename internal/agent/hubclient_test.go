package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hub"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

// syncBuf 是并发安全的日志缓冲：日志不进测试输出，测试失败时才打印。
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

type hubEnv struct {
	srv  *hub.Server
	st   *store.Store
	http *httptest.Server
}

func startHub(t *testing.T) *hubEnv {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := hub.DefaultConfig(dir)
	cfg.PingInterval = 200 * time.Millisecond
	cfg.PongTimeout = 2 * time.Second
	logs := &syncBuf{}
	srv, err := hub.New(cfg, st, log.New(logs, "hub ", 0))
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		hs.Close()
		st.Close()
		if t.Failed() {
			t.Logf("hub 日志:\n%s", logs.String())
		}
	})
	return &hubEnv{srv: srv, st: st, http: hs}
}

func enroll(t *testing.T, h *hubEnv) agent.MachineConfig {
	t.Helper()
	tok, err := h.st.CreateEnrollToken(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": tok, "name": "mac", "home_dir": "/home/u"})
	resp, err := http.Post(h.http.URL+"/api/enroll", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("enroll: %v %v", err, resp)
	}
	defer resp.Body.Close()
	var out struct {
		MachineID string `json:"machine_id"`
		Secret    string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return agent.MachineConfig{HubURL: h.http.URL, MachineID: out.MachineID, Secret: out.Secret, Name: "mac"}
}

type clientEnv struct {
	c       *agent.HubClient
	cfg     agent.Config
	rules   *agent.Rules
	pending *agent.Pending
	logs    *syncBuf
	cancel  context.CancelFunc
	done    chan struct{} // Run 返回时关闭
}

// newClient 组装守护进程（agent.Server 提供 SetHubPaused 与 Pending）和 hub 客户端并开始 Run。
// before 非空时在 Run 之前调用（例如先改本机规则）。
func newClient(t *testing.T, h *hubEnv, m agent.MachineConfig, before func(env clientEnv)) clientEnv {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := agent.ConfigIn(dir)
	cfg.HomeDir = "/home/u"
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	rules, err := agent.LoadRules(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := agent.OpenAudit(cfg.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	logger := log.New(logs, "agent ", 0)
	srv := agent.New(cfg, rules, audit, agent.LocalDecider{}, logger)
	c := agent.NewHubClient(cfg, m, rules, srv.Pending(), srv.SetHubPaused, logger)
	// 与 cmd 的接线一致：hub 客户端也是审计转发目标。SetHubPaused 写审计时会同步调 c.Forward，
	// 这里顺带验证客户端调 SetHubPaused 时没有持着自己的锁（否则自锁挂死）。
	srv.SetAuditSink(c)
	ctx, cancel := context.WithCancel(context.Background())
	env := clientEnv{c: c, cfg: cfg, rules: rules, pending: srv.Pending(), logs: logs, cancel: cancel, done: make(chan struct{})}
	if before != nil {
		before(env)
	}
	go func() {
		defer close(env.done)
		c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-env.done:
		case <-time.After(5 * time.Second):
			t.Error("取消后 Run 没有返回")
		}
		audit.Close()
		if strings.Contains(logs.String(), m.Secret) {
			t.Error("日志里出现了机器密钥")
		}
		if t.Failed() {
			t.Logf("守护进程日志:\n%s", logs.String())
		}
	})
	return env
}

func waitConnected(t *testing.T, c *agent.HubClient) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.WaitConnected(ctx); err != nil {
		t.Fatal("没有连上 hub")
	}
}

// eventually 轮询 cond 直到为真，超时失败。
func eventually(t *testing.T, d time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func shellPending(id, tool string) agent.PendingRequest {
	now := time.Now()
	return agent.PendingRequest{ID: id, Req: proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: tool, CLISessionID: "s-" + id, TurnID: "t1", Cwd: "/p"}, ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push", Cwd: "/p"}, DeadlineSeconds: 60},
		Risk: policy.RiskOutward, ProjectRoot: "/p", Redacted: policy.Payload{Type: policy.PayloadShell, Command: "git push", Cwd: "/p"}, EngineReason: "无规则命中", Created: now, Deadline: now.Add(time.Minute)}
}

func TestHubClientConnectsAndAppliesRules(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c, rules, cfg := env.c, env.rules, env.cfg
	waitConnected(t, c)
	if rules.Version() != 1 || rules.Count() != 2 {
		t.Fatalf("welcome 应下发规则: v=%d n=%d", rules.Version(), rules.Count())
	}
	rs, err := policy.ParseRuleSet([]byte("version: 1\nrules:\n  - id: only\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.st.ReplaceRules(rs); err != nil {
		t.Fatal(err)
	}
	h.srv.PushRulesForTest()
	eventually(t, 5*time.Second, "规则推送未生效", func() bool { return rules.Version() == 2 })
	if rules.Count() != 1 {
		t.Fatalf("规则推送未生效: v=%d n=%d", rules.Version(), rules.Count())
	}
	if err := h.srv.SetPausedForTest(true); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "hub 暂停应下发到守护进程", rules.Paused)
	// 只改 hub 这一侧：state.json 里 hub_paused 为真，本机的 paused 不动
	b, _ := os.ReadFile(cfg.StatePath)
	if !strings.Contains(string(b), `"hub_paused":true`) || !strings.Contains(string(b), `"paused":false`) {
		t.Fatalf("state.json 应只记下 hub 暂停: %q", b)
	}
	if err := h.srv.SetPausedForTest(false); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "hub 恢复应下发到守护进程", func() bool { return !rules.Paused() })
}

// welcome 一律以 hub 为准：本机版本比 hub 的大也替换。
func TestHubClientWelcomeAlwaysReplacesRules(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, func(env clientEnv) {
		rs, err := policy.ParseRuleSet([]byte("version: 1\nrules:\n  - id: local\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n"))
		if err != nil {
			t.Fatal(err)
		}
		if err := env.rules.Replace(rs, 99); err != nil {
			t.Fatal(err)
		}
	})
	waitConnected(t, env.c)
	if env.rules.Version() != 1 || env.rules.Count() != 2 {
		t.Fatalf("welcome 应替换本机规则（哪怕版本更小）: v=%d n=%d", env.rules.Version(), env.rules.Count())
	}
}

func TestHubClientPendingDecisionAndDelivered(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c, pending := env.c, env.pending
	waitConnected(t, c)
	pr := shellPending("r1", "claude")
	pending.Add(&pr)
	ch := c.Await("r1")
	if err := c.SendPending(pr); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "hub 没有登记请求", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: "r1", Action: "allow", Note: "ok"}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.Action != "allow" || d.Reason != "ok" || d.RequestID != "r1" {
			t.Fatalf("decision = %+v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没有收到决定")
	}
	if err := c.SendDelivered("r1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "hub 没有记录送达", func() bool {
		r, err := h.st.GetRequest("r1")
		return err == nil && r.DeliveredAt != nil
	})
	c.Forget("r1")
}

func TestHubClientCancelAndExpired(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c := env.c
	waitConnected(t, c)
	for _, id := range []string{"c1", "e1"} {
		pr := shellPending(id, "codex")
		env.pending.Add(&pr)
		if err := c.SendPending(pr); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 5*time.Second, "hub 没有登记请求", func() bool { n, _ := h.st.CountPending(); return n == 2 })
	if err := c.SendCancel("c1", "terminal"); err != nil {
		t.Fatal(err)
	}
	if err := c.SendExpired("e1"); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "hub 没有结束请求", func() bool {
		a, err1 := h.st.GetRequest("c1")
		b, err2 := h.st.GetRequest("e1")
		return err1 == nil && err2 == nil && a.State == "cancelled" && b.State == "expired"
	})
}

func TestHubClientReconnectsAndResyncs(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c, pending := env.c, env.pending
	waitConnected(t, c)
	pr := shellPending("r1", "claude")
	pending.Add(&pr)
	ch := c.Await("r1")
	if err := c.SendPending(pr); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "hub 没有登记请求", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	h.srv.DisconnectForTest(m.MachineID) // 模拟网络断开
	// 断线期间决定
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: "r1", Action: "deny"}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.Action != "deny" {
			t.Fatalf("重连后应补到决定: %+v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("重连后没有补到决定")
	}
}

func TestHubClientConnState(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c := env.c
	waitConnected(t, c)
	connected, changed := c.ConnState()
	if !connected || !c.Connected() {
		t.Fatal("连上后 ConnState 应报已连接")
	}
	select {
	case <-changed:
		t.Fatal("状态没变时通道不应关闭")
	default:
	}
	h.srv.DisconnectForTest(m.MachineID)
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("断开时通道应关闭")
	}
	connected, changed = c.ConnState()
	if connected {
		// 已经重连上也算一次变化，这里只要求断开那次关了通道；再等下一次变化
		t.Log("断开后很快重连上了")
	} else {
		select {
		case <-changed:
		case <-time.After(10 * time.Second):
			t.Fatal("重连时通道应关闭")
		}
	}
	waitConnected(t, c)
}

func TestHubClientForwardsAudit(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	c := newClient(t, h, m, nil).c
	waitConnected(t, c)
	p := policy.Payload{Type: policy.PayloadShell, Command: "ls"}
	c.Forward(agent.AuditEntry{TS: time.Now(), RequestID: "auto1", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1"}, ToolName: "Bash", Payload: &p, Risk: "read", Action: "allow", State: "auto_allowed", RuleID: "builtin-readonly"})
	eventually(t, 5*time.Second, "审计没有到达 hub", func() bool { _, err := h.st.GetRequest("auto1"); return err == nil })
}

// 未连接时上报的审计积压，连上后补发。
func TestHubClientForwardsAuditBacklog(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, func(env clientEnv) {
		p := policy.Payload{Type: policy.PayloadShell, Command: "ls"}
		env.c.Forward(agent.AuditEntry{TS: time.Now(), RequestID: "early1", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1"}, ToolName: "Bash", Payload: &p, Risk: "read", Action: "allow", State: "auto_allowed", RuleID: "builtin-readonly"})
	})
	waitConnected(t, env.c)
	eventually(t, 5*time.Second, "积压的审计没有补发", func() bool { _, err := h.st.GetRequest("early1"); return err == nil })
}

// hub 拒收某条 pending（id 不合规）时，等待者收到 hub_error 的合成决定。
func TestHubClientHubErrorForRequest(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c := env.c
	waitConnected(t, c)
	pr := shellPending("bad id", "claude")
	ch := c.Await("bad id")
	if err := c.SendPending(pr); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.Action != "" || d.DecidedBy != "hub_error" || d.RequestID != "bad id" || d.Reason == "" {
			t.Fatalf("应收到 hub_error 的合成决定: %+v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hub 拒收后等待者没有收到结论")
	}
}

// Gemini 请求不进 hub 等待：SendPending 直接报错，hub 里没有它。
func TestHubClientRefusesGeminiPending(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	waitConnected(t, env.c)
	if err := env.c.SendPending(shellPending("g1", "gemini")); err == nil || errors.Is(err, agent.ErrHubDisconnected) {
		t.Fatalf("gemini 请求应被拒绝送往 hub: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := h.st.GetRequest("g1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("hub 不应有 gemini 请求: %v", err)
	}
}

// 机器被吊销后重连被 401 拒绝：客户端标成永久不可用，Run 不再重连。
func TestHubClientDisabledOnRevoke(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c := env.c
	waitConnected(t, c)
	if c.Disabled() {
		t.Fatal("正常连接时不应停用")
	}
	_, changed := c.ConnState()
	if err := h.st.RevokeMachine(m.MachineID); err != nil {
		t.Fatal(err)
	}
	h.srv.DisconnectForTest(m.MachineID)
	select {
	case <-env.done:
	case <-time.After(10 * time.Second):
		t.Fatal("被 401 拒绝后 Run 应返回，不再重连")
	}
	if !c.Disabled() || c.Connected() {
		t.Fatalf("被 401 拒绝后应停用: disabled=%v connected=%v", c.Disabled(), c.Connected())
	}
	select {
	case <-changed:
	default:
		t.Fatal("停用应关闭状态变化通道")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.WaitConnected(ctx); !errors.Is(err, agent.ErrHubDisabled) {
		t.Fatalf("停用后 WaitConnected 应立即报 ErrHubDisabled: %v", err)
	}
	if err := c.SendPending(shellPending("r9", "claude")); !errors.Is(err, agent.ErrHubDisconnected) {
		t.Fatalf("停用后发送应报未连接: %v", err)
	}
	if !strings.Contains(env.logs.String(), "401") {
		t.Fatalf("应醒目记下 401: %s", env.logs.String())
	}
}

func TestHubClientRunStopsOnCancel(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	waitConnected(t, env.c)
	_, changed := env.c.ConnState()
	env.cancel()
	select {
	case <-env.done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 Run 应返回")
	}
	if env.c.Connected() {
		t.Fatal("Run 返回后不应仍报已连接")
	}
	select {
	case <-changed:
	default:
		t.Fatal("断开应关闭状态变化通道")
	}
}

func TestHubClientSendsWithoutConnection(t *testing.T) {
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cfg := agent.ConfigIn(dir)
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	rules, err := agent.LoadRules(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	noPause := func(bool) error { return nil }
	c := agent.NewHubClient(cfg, agent.MachineConfig{HubURL: "http://127.0.0.1:1", MachineID: "m", Secret: "s"}, rules, agent.NewPendingForTest(), noPause, log.New(&syncBuf{}, "", 0))
	if c.Connected() || c.Disabled() {
		t.Fatal("未运行时不应已连接或已停用")
	}
	if err := c.SendPending(agent.PendingRequest{ID: "x"}); !errors.Is(err, agent.ErrHubDisconnected) {
		t.Fatalf("未连接应报 ErrHubDisconnected: %v", err)
	}
	for _, err := range []error{c.SendCancel("x", "terminal"), c.SendExpired("x"), c.SendDelivered("x")} {
		if !errors.Is(err, agent.ErrHubDisconnected) {
			t.Fatalf("未连接应报 ErrHubDisconnected: %v", err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.WaitConnected(ctx); err == nil {
		t.Fatal("WaitConnected 应超时")
	}
}
