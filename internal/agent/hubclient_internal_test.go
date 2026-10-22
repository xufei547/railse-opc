package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/railse/railse-opc/internal/hub/wire"
	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

// 这里用一个假 hub 测真实 hub 造不出来的情形（协议版本不符、只握手不发帧、乱序的规则帧等），
// 以及审计队列的内部细节。与真实 hub 的联调在 hubclient_test.go（package agent_test）。

const fakeSecret = "sekrit-machine-key-0123456789"

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type fakeFrame struct {
	kind string
	raw  []byte
}

// fakeHub 是只认 /ws/agent 协议外形的假 hub：status 非零时直接回该 HTTP 状态；否则升级、读 hello、交给 script。
type fakeHub struct {
	http   *httptest.Server
	status int
	script func(f *fakeHub, conn *websocket.Conn, hello wire.Hello)
	conns  chan *websocket.Conn // script 可把连接交给测试，由测试发帧

	dials  atomic.Int32
	mu     sync.Mutex
	hellos []wire.Hello
	frames []fakeFrame
	uris   []string
	auths  []string
}

func newFakeHub(t *testing.T, status int, script func(f *fakeHub, conn *websocket.Conn, hello wire.Hello)) *fakeHub {
	t.Helper()
	f := &fakeHub{status: status, script: script, conns: make(chan *websocket.Conn, 16)}
	f.http = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.http.Close)
	return f
}

func (f *fakeHub) serve(w http.ResponseWriter, r *http.Request) {
	f.dials.Add(1)
	f.mu.Lock()
	f.uris = append(f.uris, r.URL.RequestURI())
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	f.mu.Unlock()
	if f.status != 0 {
		http.Error(w, "拒绝", f.status)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wire.MaxFrame)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_, b, err := conn.Read(ctx)
	cancel()
	if err != nil {
		return
	}
	var hello wire.Hello
	if json.Unmarshal(b, &hello) != nil {
		return
	}
	f.mu.Lock()
	f.hellos = append(f.hellos, hello)
	f.mu.Unlock()
	f.script(f, conn, hello)
}

func (f *fakeHub) send(conn *websocket.Conn, v any) {
	b, err := wire.Marshal(v)
	if err != nil {
		panic(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn.Write(ctx, websocket.MessageText, b)
}

func defaultRulesYAML() string {
	b, err := policy.DefaultRuleSet().Marshal()
	if err != nil {
		panic(err)
	}
	return string(b)
}

func (f *fakeHub) welcome(conn *websocket.Conn, version int, yaml string, paused bool) {
	f.send(conn, wire.Welcome{T: wire.TWelcome, RulesVersion: version, RulesYAML: yaml, Paused: paused, ServerTime: time.Now().UTC()})
}

// record 读帧并记下，直到连接断开。
func (f *fakeHub) record(conn *websocket.Conn) {
	for {
		_, b, err := conn.Read(context.Background())
		if err != nil {
			return
		}
		k, _ := wire.Kind(b)
		f.mu.Lock()
		f.frames = append(f.frames, fakeFrame{kind: k, raw: b})
		f.mu.Unlock()
	}
}

func (f *fakeHub) framesOf(kind string) [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out [][]byte
	for _, fr := range f.frames {
		if fr.kind == kind {
			out = append(out, fr.raw)
		}
	}
	return out
}

func (f *fakeHub) count(kind string) int { return len(f.framesOf(kind)) }

// welcomeThenRecord：发 welcome 后只读不发。
func welcomeThenRecord(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
	f.welcome(conn, 1, defaultRulesYAML(), false)
	f.record(conn)
}

// welcomeThenHandOver：发 welcome，把连接交给测试，自己读帧直到断开。
func welcomeThenHandOver(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
	f.welcome(conn, 1, defaultRulesYAML(), false)
	f.conns <- conn
	f.record(conn)
}

// pingAndWaitPong 发一个 ping 并等到多一个 pong：客户端读循环按顺序处理帧，pong 到了说明之前的帧都处理完了。
func (f *fakeHub) pingAndWaitPong(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	n := f.count(wire.TPong)
	f.send(conn, wire.Ping{T: wire.TPing})
	waitFor(t, func() bool { return f.count(wire.TPong) > n })
}

// setHubTimings 临时改包级时长（NewHubClient 时复制进客户端），测试结束时还原。零值或 nil 表示不改。
func setHubTimings(t *testing.T, backoff []time.Duration, idle, protoWait, auditEvery time.Duration) {
	t.Helper()
	ob, oi, op, oa := hubBackoff, hubIdleTimeout, hubProtoBackoff, hubAuditInterval
	if backoff != nil {
		hubBackoff = backoff
	}
	if idle > 0 {
		hubIdleTimeout = idle
	}
	if protoWait > 0 {
		hubProtoBackoff = protoWait
	}
	if auditEvery > 0 {
		hubAuditInterval = auditEvery
	}
	t.Cleanup(func() { hubBackoff, hubIdleTimeout, hubProtoBackoff, hubAuditInterval = ob, oi, op, oa })
}

type testClient struct {
	c       *HubClient
	cfg     Config
	rules   *Rules
	pending *Pending
	logs    *logBuf
	done    chan struct{}
	cancel  context.CancelFunc

	pauseMu sync.Mutex
	pauses  []bool // setHubPaused 收到的值
}

func (tc *testClient) setHubPaused(p bool) error {
	tc.pauseMu.Lock()
	defer tc.pauseMu.Unlock()
	tc.pauses = append(tc.pauses, p)
	return nil
}

func (tc *testClient) pausesSeen() []bool {
	tc.pauseMu.Lock()
	defer tc.pauseMu.Unlock()
	return append([]bool(nil), tc.pauses...)
}

// newTestClient 组装客户端但不运行。
func newTestClient(t *testing.T, hubURL string) *testClient {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := ConfigIn(dir)
	cfg.HomeDir = "/home/u"
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	rules, err := LoadRules(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	tc := &testClient{cfg: cfg, rules: rules, pending: newPending(), logs: &logBuf{}, done: make(chan struct{})}
	tc.c = NewHubClient(cfg, MachineConfig{HubURL: hubURL, MachineID: "m1", Secret: fakeSecret, Name: "mac"}, rules, tc.pending, tc.setHubPaused, log.New(tc.logs, "", 0))
	t.Cleanup(func() {
		if strings.Contains(tc.logs.String(), fakeSecret) {
			t.Error("日志里出现了机器密钥")
		}
		if t.Failed() {
			t.Logf("客户端日志:\n%s", tc.logs.String())
		}
	})
	return tc
}

// run 开始 Run，测试结束时取消并等它返回。
func (tc *testClient) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	tc.cancel = cancel
	go func() {
		defer close(tc.done)
		tc.c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-tc.done:
		case <-time.After(5 * time.Second):
			t.Error("取消后 Run 没有返回")
		}
	})
}

func startTestClient(t *testing.T, f *fakeHub) *testClient {
	t.Helper()
	tc := newTestClient(t, f.http.URL)
	tc.run(t)
	return tc
}

func (tc *testClient) waitConnected(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tc.c.WaitConnected(ctx); err != nil {
		t.Fatalf("没有连上假 hub: %v", err)
	}
}

func pendingFor(id, tool string, created time.Time) *PendingRequest {
	return &PendingRequest{ID: id, Req: proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: tool, CLISessionID: "s-" + id, Cwd: "/p"}, ToolName: "Bash",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push", Cwd: "/p"}}, Risk: policy.RiskOutward, ProjectRoot: "/p",
		Redacted: policy.Payload{Type: policy.PayloadShell, Command: "git push", Cwd: "/p"}, EngineReason: "无规则命中", Created: created, Deadline: created.Add(10 * time.Minute)}
}

func TestToWireUsesRedactedAndTotalDeadline(t *testing.T) {
	const secret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	loc := time.FixedZone("CST", 8*3600)
	created := time.Now().Add(-5 * time.Minute).In(loc)
	pr := PendingRequest{ID: "r1", Req: proto.Request{Session: proto.Session{Tool: "claude", CLISessionID: "s1"}, ToolName: "Bash",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "curl -H 'Authorization: token " + secret + "' https://api.github.com"}},
		Risk: policy.RiskOutward, ProjectRoot: "/p", Redacted: policy.Payload{Type: policy.PayloadShell, Command: "curl -H 'Authorization: token [REDACTED]' https://api.github.com"},
		EngineReason: "无规则命中", Created: created, Deadline: created.Add(10 * time.Minute)}
	w := toWire(pr)
	if w.T != wire.TPending || w.RequestID != "r1" || w.ToolName != "Bash" || w.Risk != "outward" || w.ProjectRoot != "/p" || w.EngineReason != "无规则命中" {
		t.Fatalf("toWire 字段不对: %+v", w)
	}
	if w.Payload.Command != pr.Redacted.Command {
		t.Fatalf("toWire 应只用脱敏后的载荷: %q", w.Payload.Command)
	}
	b, err := wire.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("pending 帧里出现了原始秘密: %s", b)
	}
	// 从 CreatedAt 起算的总时长，不是剩余秒数（已过去 5 分钟，剩余约 300）
	if w.DeadlineSeconds != 600 {
		t.Fatalf("DeadlineSeconds 应为总时长 600，得到 %d", w.DeadlineSeconds)
	}
	if w.CreatedAt.Location() != time.UTC || !w.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt 应为 UTC 的创建时间: %v", w.CreatedAt)
	}
}

// Gemini 请求不进 hub：hello 的摘要与连上后的重发都跳过它。
func TestHubClientSkipsGeminiInHelloAndResend(t *testing.T) {
	f := newFakeHub(t, 0, welcomeThenHandOver)
	tc := newTestClient(t, f.http.URL)
	now := time.Now()
	tc.pending.Add(pendingFor("r1", "claude", now.Add(-2*time.Second)))
	tc.pending.Add(pendingFor("g1", "gemini", now.Add(-time.Second)))
	tc.pending.Add(pendingFor("g2", "Gemini", now))
	tc.run(t)
	tc.waitConnected(t)
	conn := <-f.conns
	f.pingAndWaitPong(t, conn) // 重发在读循环之前，pong 到了说明重发已经发完
	f.mu.Lock()
	hello := f.hellos[0]
	f.mu.Unlock()
	if hello.Proto != wire.Proto || hello.MachineID != "m1" || hello.HomeDir != "/home/u" || hello.AgentVersion != "dev" {
		t.Fatalf("hello 字段不对: %+v", hello)
	}
	if len(hello.Pending) != 1 || hello.Pending[0].RequestID != "r1" {
		t.Fatalf("hello 只应带非 gemini 的 pending: %+v", hello.Pending)
	}
	frames := f.framesOf(wire.TPending)
	if len(frames) != 1 || !strings.Contains(string(frames[0]), `"request_id":"r1"`) {
		t.Fatalf("重发只应有 r1: %q", frames)
	}
	f.mu.Lock()
	auth := f.auths[0]
	f.mu.Unlock()
	if auth != "Bearer "+fakeSecret {
		t.Fatalf("握手应带 Bearer 机器密钥: %q", auth)
	}
}

// hub 回 Error 帧并以 PolicyViolation 关闭（协议版本不一致）：打印 hub 的文案，改用长退避。
func TestHubClientProtoMismatchLongBackoff(t *testing.T) {
	setHubTimings(t, []time.Duration{20 * time.Millisecond}, 0, time.Hour, 0)
	f := newFakeHub(t, 0, func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
		f.send(conn, wire.Error{T: wire.TError, Message: "协议版本 1，hub 需要 2"})
		conn.Close(websocket.StatusPolicyViolation, "协议版本不一致")
	})
	tc := startTestClient(t, f)
	waitFor(t, func() bool { return strings.Contains(tc.logs.String(), "hub 需要 2") })
	time.Sleep(500 * time.Millisecond)
	if n := f.dials.Load(); n != 1 {
		t.Fatalf("协议版本不一致后应长退避，不该很快重连: 拨号 %d 次", n)
	}
	if tc.c.Disabled() {
		t.Fatal("协议版本不一致不是永久停用")
	}
}

// 只以 PolicyViolation 关闭、没有 Error 帧的，照常按短退避重连。
func TestHubClientPolicyCloseWithoutErrorRetriesNormally(t *testing.T) {
	setHubTimings(t, []time.Duration{20 * time.Millisecond}, 0, time.Hour, 0)
	f := newFakeHub(t, 0, func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
		conn.Close(websocket.StatusPolicyViolation, "机器已吊销")
	})
	startTestClient(t, f)
	waitFor(t, func() bool { return f.dials.Load() >= 3 })
}

// 握手被 401 拒绝：醒目记日志，永久停用，Run 返回且不再拨号。
func TestHubClientUnauthorizedDisables(t *testing.T) {
	setHubTimings(t, []time.Duration{20 * time.Millisecond}, 0, 0, 0)
	f := newFakeHub(t, http.StatusUnauthorized, nil)
	tc := startTestClient(t, f)
	select {
	case <-tc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("401 后 Run 应返回")
	}
	time.Sleep(200 * time.Millisecond)
	if n := f.dials.Load(); n != 1 {
		t.Fatalf("401 后不应再拨号: %d 次", n)
	}
	if !tc.c.Disabled() || tc.c.Connected() {
		t.Fatal("401 后应停用")
	}
	if !strings.Contains(tc.logs.String(), "railse agent enroll") {
		t.Fatalf("应提示重新接入: %s", tc.logs.String())
	}
}

// 其他 HTTP 错误不停用，照常重连。
func TestHubClientServerErrorRetries(t *testing.T) {
	setHubTimings(t, []time.Duration{20 * time.Millisecond}, 0, 0, 0)
	f := newFakeHub(t, http.StatusServiceUnavailable, nil)
	tc := startTestClient(t, f)
	waitFor(t, func() bool { return f.dials.Load() >= 3 })
	if tc.c.Disabled() {
		t.Fatal("503 不应停用")
	}
}

// 连不上时退避逐档变长。
func TestHubClientBackoffGrowsWithoutWelcome(t *testing.T) {
	setHubTimings(t, []time.Duration{30 * time.Millisecond, 5 * time.Second}, 0, 0, 0)
	f := newFakeHub(t, http.StatusServiceUnavailable, nil)
	startTestClient(t, f)
	waitFor(t, func() bool { return f.dials.Load() >= 2 })
	time.Sleep(time.Second)
	if n := f.dials.Load(); n != 2 {
		t.Fatalf("第二次失败后应等第二档（5 秒）: 拨号 %d 次", n)
	}
}

// setVar 临时改一个包级变量（NewHubClient 时复制进客户端），测试结束时还原。
func setVar[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// 握手后立即被断开（例如两台守护进程共用 machine.json 互相顶替）不算稳定：退避照常变长，不会每秒重连。
func TestHubClientBackoffNotResetOnImmediateClose(t *testing.T) {
	setHubTimings(t, []time.Duration{30 * time.Millisecond, 5 * time.Second}, 0, 0, 0)
	f := newFakeHub(t, 0, func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
		f.welcome(conn, 1, defaultRulesYAML(), false)
		conn.Close(websocket.StatusNormalClosure, "bye")
	})
	startTestClient(t, f)
	waitFor(t, func() bool { return f.dials.Load() >= 2 })
	time.Sleep(time.Second)
	if n := f.dials.Load(); n != 2 {
		t.Fatalf("握手后立即断开不应把退避归零: 拨号 %d 次", n)
	}
}

// 收到 hub 的第一个 ping 就算稳定：之后断开按第一档重连，连上又断开多次仍是第一档。
func TestHubClientBackoffResetsAfterPing(t *testing.T) {
	setHubTimings(t, []time.Duration{30 * time.Millisecond, 5 * time.Second}, 0, 0, 0)
	f := newFakeHub(t, 0, func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
		f.welcome(conn, 1, defaultRulesYAML(), false)
		f.send(conn, wire.Ping{T: wire.TPing})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn.Read(ctx) // 等到 pong：客户端已处理过 ping
		conn.Close(websocket.StatusNormalClosure, "bye")
	})
	startTestClient(t, f)
	waitFor(t, func() bool { return f.dials.Load() >= 4 })
}

// 没有 ping、但连接持续超过 stableAfter 也算稳定。
func TestHubClientBackoffResetsAfterStableDuration(t *testing.T) {
	setHubTimings(t, []time.Duration{30 * time.Millisecond, 5 * time.Second}, 0, 0, 0)
	setVar(t, &hubStableAfter, 100*time.Millisecond)
	f := newFakeHub(t, 0, func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
		f.welcome(conn, 1, defaultRulesYAML(), false)
		time.Sleep(200 * time.Millisecond)
		conn.Close(websocket.StatusNormalClosure, "bye")
	})
	startTestClient(t, f)
	waitFor(t, func() bool { return f.dials.Load() >= 4 })
}

// 重连后重发 pending 时，编不进一帧的请求跳过：连接不断、其余照常重发、它的等待者立即收到 hub_error。
func TestHubClientResendSkipsOversizePending(t *testing.T) {
	setHubTimings(t, []time.Duration{20 * time.Millisecond}, 0, 0, 0)
	f := newFakeHub(t, 0, welcomeThenHandOver)
	tc := newTestClient(t, f.http.URL)
	now := time.Now()
	huge := pendingFor("huge", "claude", now.Add(-2*time.Second))
	huge.Redacted.DiffPreview = strings.Repeat("x", 2<<20)
	tc.pending.Add(huge)
	tc.pending.Add(pendingFor("small", "claude", now.Add(-time.Second)))
	ch := tc.c.Await("huge")
	tc.run(t)
	tc.waitConnected(t)
	conn := <-f.conns
	f.pingAndWaitPong(t, conn) // 重发在读循环之前：pong 到了说明重发已经走完
	frames := f.framesOf(wire.TPending)
	if len(frames) != 1 || !strings.Contains(string(frames[0]), `"request_id":"small"`) {
		t.Fatalf("超大请求应跳过、其余照常重发: %d 帧", len(frames))
	}
	select {
	case d := <-ch:
		if d.RequestID != "huge" || d.Action != "" || d.DecidedBy != "hub_error" {
			t.Fatalf("超大请求的等待者应收到 hub_error: %+v", d)
		}
	default:
		t.Fatal("超大请求的等待者没有收到结论")
	}
	if n := f.dials.Load(); n != 1 || !tc.c.Connected() {
		t.Fatalf("超大请求不应让连接断开: 拨号 %d 次，connected=%v", n, tc.c.Connected())
	}
	if !strings.Contains(tc.logs.String(), `"huge"`) {
		t.Fatalf("应记下跳过的请求: %s", tc.logs.String())
	}
	// 直接 SendPending 也只报编码错误，不当作连接问题
	err := tc.c.SendPending(*huge)
	if !errors.Is(err, wire.ErrFrameTooLarge) || errors.Is(err, ErrHubDisconnected) {
		t.Fatalf("超大请求应报 ErrFrameTooLarge 而非未连接: %v", err)
	}
	if n := f.dials.Load(); n != 1 || !tc.c.Connected() {
		t.Fatal("SendPending 的编码错误不应影响连接")
	}
}

// hub 连上后不再发任何帧：超过 idle 超时主动断开并重连。
func TestHubClientIdleTimeoutReconnects(t *testing.T) {
	setHubTimings(t, []time.Duration{20 * time.Millisecond}, 200*time.Millisecond, 0, 0)
	f := newFakeHub(t, 0, welcomeThenRecord)
	tc := startTestClient(t, f)
	waitFor(t, func() bool { return f.dials.Load() >= 2 })
	if !strings.Contains(tc.logs.String(), "没有收到 hub 的任何帧") {
		t.Fatalf("应记下 idle 断开: %s", tc.logs.String())
	}
}

// hub 按时 ping：连接保持，客户端回 pong。
func TestHubClientPingKeepsConnection(t *testing.T) {
	setHubTimings(t, []time.Duration{20 * time.Millisecond}, 600*time.Millisecond, 0, 0)
	f := newFakeHub(t, 0, func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
		f.welcome(conn, 1, defaultRulesYAML(), false)
		done := make(chan struct{})
		go func() {
			defer close(done)
			f.record(conn)
		}()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				f.send(conn, wire.Ping{T: wire.TPing})
			case <-done:
				return
			}
		}
	})
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	time.Sleep(time.Second)
	if n := f.dials.Load(); n != 1 {
		t.Fatalf("有 ping 时不应断开重连: 拨号 %d 次", n)
	}
	if n := f.count(wire.TPong); n < 5 {
		t.Fatalf("应对每个 ping 回 pong: %d", n)
	}
}

const rulesV = "version: 1\nrules:\n  - id: %s\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n"

// 会话内的 rules 帧只在版本更大时应用；坏规则集保留本地并记日志。
func TestHubClientRulesFrames(t *testing.T) {
	f := newFakeHub(t, 0, func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
		f.welcome(conn, 3, strings.Replace(rulesV, "%s", "v3", 1), false)
		f.conns <- conn
		f.record(conn)
	})
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	conn := <-f.conns
	if tc.rules.Version() != 3 || tc.rules.Count() != 1 {
		t.Fatalf("welcome 规则: v=%d n=%d", tc.rules.Version(), tc.rules.Count())
	}
	f.send(conn, wire.Rules{T: wire.TRules, Version: 2, YAML: defaultRulesYAML()}) // 乱序到达的旧版本
	f.send(conn, wire.Rules{T: wire.TRules, Version: 3, YAML: defaultRulesYAML()}) // 同版本
	f.pingAndWaitPong(t, conn)
	if tc.rules.Version() != 3 || tc.rules.Count() != 1 {
		t.Fatalf("不大于本机版本的 rules 帧应忽略: v=%d n=%d", tc.rules.Version(), tc.rules.Count())
	}
	f.send(conn, wire.Rules{T: wire.TRules, Version: 4, YAML: defaultRulesYAML()})
	f.pingAndWaitPong(t, conn)
	if tc.rules.Version() != 4 || tc.rules.Count() != 2 {
		t.Fatalf("更新的 rules 帧应应用: v=%d n=%d", tc.rules.Version(), tc.rules.Count())
	}
	f.send(conn, wire.Rules{T: wire.TRules, Version: 5, YAML: "version: 1\nrules:\n  - id: bad\n    action: explode\n"})
	f.pingAndWaitPong(t, conn)
	if tc.rules.Version() != 4 || tc.rules.Count() != 2 {
		t.Fatalf("坏规则集应保留本地: v=%d n=%d", tc.rules.Version(), tc.rules.Count())
	}
	if !strings.Contains(tc.logs.String(), "保留本地") {
		t.Fatalf("坏规则集应记日志: %s", tc.logs.String())
	}
}

// 坏的 welcome 规则保留本地，连接照常。
func TestHubClientBadWelcomeRulesKeepsLocal(t *testing.T) {
	f := newFakeHub(t, 0, func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
		f.welcome(conn, 7, "not: [valid", false)
		f.record(conn)
	})
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	if tc.rules.Version() != 0 || tc.rules.Count() != 2 {
		t.Fatalf("坏的 welcome 规则应保留本地: v=%d n=%d", tc.rules.Version(), tc.rules.Count())
	}
}

// welcome.Paused 与 paused 帧只经 setHubPaused，不直接改 rules、不写 state.json。
func TestHubClientPausedUsesSetHubPaused(t *testing.T) {
	f := newFakeHub(t, 0, func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
		f.welcome(conn, 1, defaultRulesYAML(), true)
		f.conns <- conn
		f.record(conn)
	})
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	conn := <-f.conns
	f.send(conn, wire.Paused{T: wire.TPaused, Paused: false})
	f.pingAndWaitPong(t, conn)
	if got := tc.pausesSeen(); len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("setHubPaused 应依次收到 true、false: %v", got)
	}
	if tc.rules.Paused() {
		t.Fatal("hub 客户端不应直接改 rules 的暂停")
	}
	if _, err := os.Stat(tc.cfg.StatePath); !os.IsNotExist(err) {
		t.Fatalf("hub 客户端不应自己写 state.json: %v", err)
	}
}

// 决定：有等待者交给它，重复的丢弃且不阻塞读循环；没有等待者只记日志，不回 delivered。
func TestHubClientDecisionDispatch(t *testing.T) {
	f := newFakeHub(t, 0, welcomeThenHandOver)
	tc := newTestClient(t, f.http.URL)
	ch := tc.c.Await("r1")
	tc.run(t)
	tc.waitConnected(t)
	conn := <-f.conns
	f.send(conn, wire.Decision{T: wire.TDecision, RequestID: "r1", Action: "allow", Reason: "第一次", DecidedBy: "user"})
	f.send(conn, wire.Decision{T: wire.TDecision, RequestID: "r1", Action: "deny", Reason: "重复", DecidedBy: "user"})
	f.send(conn, wire.Decision{T: wire.TDecision, RequestID: "ghost", Action: "allow", DecidedBy: "user"})
	f.pingAndWaitPong(t, conn)
	select {
	case d := <-ch:
		if d.Action != "allow" || d.Reason != "第一次" {
			t.Fatalf("应收到第一次的决定: %+v", d)
		}
	default:
		t.Fatal("等待者没有收到决定")
	}
	select {
	case d := <-ch:
		t.Fatalf("重复的决定应丢弃: %+v", d)
	default:
	}
	if n := f.count(wire.TDelivered); n != 0 {
		t.Fatalf("hub 客户端自己不回 delivered: %d", n)
	}
	if !strings.Contains(tc.logs.String(), `"ghost"`) {
		t.Fatalf("没有等待者的决定应记日志: %s", tc.logs.String())
	}
	tc.c.Forget("r1")
	f.send(conn, wire.Decision{T: wire.TDecision, RequestID: "r1", Action: "allow", DecidedBy: "user"})
	f.pingAndWaitPong(t, conn)
	select {
	case d := <-ch:
		t.Fatalf("Forget 之后不应再投递: %+v", d)
	default:
	}
}

// 按 id 的 Error 帧：有等待者时投递 hub_error 的合成决定（文案截到 200 字符）；其余只记日志。
func TestHubClientErrorFrames(t *testing.T) {
	f := newFakeHub(t, 0, welcomeThenHandOver)
	tc := newTestClient(t, f.http.URL)
	ch := tc.c.Await("r2")
	tc.run(t)
	tc.waitConnected(t)
	conn := <-f.conns
	f.send(conn, wire.Error{T: wire.TError, RequestID: "r2", Message: strings.Repeat("错", 300)})
	f.send(conn, wire.Error{T: wire.TError, Message: "整体错误"})
	f.send(conn, wire.Error{T: wire.TError, RequestID: "nobody", Message: "拒收"})
	f.pingAndWaitPong(t, conn)
	select {
	case d := <-ch:
		if d.RequestID != "r2" || d.Action != "" || d.DecidedBy != "hub_error" || utf8.RuneCountInString(d.Reason) != 200 || !strings.HasPrefix(d.Reason, "错错") {
			t.Fatalf("合成决定不对: %+v（%d 字符）", d, utf8.RuneCountInString(d.Reason))
		}
	default:
		t.Fatal("等待者没有收到 hub_error")
	}
	logs := tc.logs.String()
	if !strings.Contains(logs, "整体错误") || !strings.Contains(logs, `"nobody"`) {
		t.Fatalf("其余 Error 帧应记日志: %s", logs)
	}
}

// 日志里的 hub 地址只有 scheme://host；拨号用的 URL 不带 query、userinfo。
func TestHubClientLogsOnlyHubOrigin(t *testing.T) {
	f := newFakeHub(t, 0, welcomeThenRecord)
	tc := newTestClient(t, f.http.URL+"/base/?k=secretquery#frag")
	tc.run(t)
	tc.waitConnected(t)
	f.mu.Lock()
	uri := f.uris[0]
	f.mu.Unlock()
	if uri != "/base/ws/agent" {
		t.Fatalf("拨号路径应为 /base/ws/agent 且不带 query: %q", uri)
	}
	logs := tc.logs.String()
	if !strings.Contains(logs, f.http.URL) || strings.Contains(logs, "secretquery") || strings.Contains(logs, "/base") {
		t.Fatalf("日志里的 hub 地址只应有 scheme://host: %s", logs)
	}

	setHubTimings(t, []time.Duration{20 * time.Millisecond}, 0, 0, 0)
	bad := newTestClient(t, "http://127.0.0.1:1/base?k=secretquery")
	bad.run(t)
	waitFor(t, func() bool { return strings.Contains(bad.logs.String(), "127.0.0.1:1") })
	if l := bad.logs.String(); strings.Contains(l, "secretquery") || strings.Contains(l, "/base") {
		t.Fatalf("拨号失败的日志不应带路径与 query: %s", l)
	}
}

func auditEntry(id string) AuditEntry {
	p := policy.Payload{Type: policy.PayloadShell, Command: "ls"}
	return AuditEntry{TS: time.Now(), RequestID: id, Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1"}, ToolName: "Bash", Payload: &p, Risk: "read", Action: "allow", State: "auto_allowed"}
}

func queuedIDs(h *HubClient) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, 0, len(h.auditQ))
	for _, it := range h.auditQ {
		var e AuditEntry
		json.Unmarshal(it.b, &e)
		out = append(out, e.RequestID)
	}
	return out
}

// 队列满（2000）丢最旧，只记一次日志。
func TestAuditQueueDropsOldest(t *testing.T) {
	tc := newTestClient(t, "http://127.0.0.1:1")
	for i := 0; i < auditQueueMax+5; i++ {
		tc.c.Forward(auditEntry("a" + itoa(i)))
	}
	ids := queuedIDs(tc.c)
	if len(ids) != auditQueueMax || ids[0] != "a5" || ids[len(ids)-1] != "a"+itoa(auditQueueMax+4) {
		t.Fatalf("应丢最旧的 5 条: len=%d first=%s last=%s", len(ids), ids[0], ids[len(ids)-1])
	}
	if n := strings.Count(tc.logs.String(), "丢弃最旧"); n != 1 {
		t.Fatalf("队列满只记一次日志: %d\n%s", n, tc.logs.String())
	}
}

// 发送期间新增（甚至挤掉了旧的）的条目不会在确认时被误删。
func TestAuditAckKeepsEntriesAddedDuringSend(t *testing.T) {
	tc := newTestClient(t, "http://127.0.0.1:1")
	h := tc.c
	for i := 0; i < 3; i++ {
		h.Forward(auditEntry("b" + itoa(i)))
	}
	batch := h.auditBatch()
	if len(batch) != 3 {
		t.Fatalf("批次应有 3 条: %d", len(batch))
	}
	h.Forward(auditEntry("b3"))
	h.Forward(auditEntry("b4"))
	h.ackAudit(batch[len(batch)-1].seq)
	if ids := queuedIDs(h); len(ids) != 2 || ids[0] != "b3" || ids[1] != "b4" {
		t.Fatalf("确认后应只剩发送期间新增的: %v", ids)
	}

	// 队列满时发送期间又挤掉了批次里的条目
	h.ackAudit(h.auditBatch()[1].seq)
	for i := 0; i < auditQueueMax; i++ {
		h.Forward(auditEntry("c" + itoa(i)))
	}
	batch = h.auditBatch()
	if len(batch) != auditBatchMax {
		t.Fatalf("批次至多 %d 条: %d", auditBatchMax, len(batch))
	}
	for i := 0; i < 10; i++ {
		h.Forward(auditEntry("d" + itoa(i)))
	}
	h.ackAudit(batch[len(batch)-1].seq)
	// 新增的 10 条挤掉了 c0–c9，确认再删 c10–c49：剩 c50–c1999 与 d0–d9
	ids := queuedIDs(h)
	if len(ids) != auditQueueMax-auditBatchMax+10 || ids[0] != "c"+itoa(auditBatchMax) || ids[len(ids)-1] != "d9" {
		t.Fatalf("确认只删已发送的: len=%d first=%s last=%s", len(ids), ids[0], ids[len(ids)-1])
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}

// 超大的单条先截断载荷大字段；截断后仍超限的丢弃并记日志；后面的条目照常上报，每帧不超过上限。
func TestAuditOversizeEntry(t *testing.T) {
	setHubTimings(t, nil, 0, 0, 50*time.Millisecond)
	f := newFakeHub(t, 0, welcomeThenRecord)
	tc := newTestClient(t, f.http.URL)
	big := auditEntry("big")
	big.Payload = &policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/a.txt", DiffPreview: strings.Repeat("x", 2<<20), Args: json.RawMessage(`{"k":"` + strings.Repeat("y", 2<<20) + `"}`)}
	tc.c.Forward(big)
	huge := auditEntry("huge")
	huge.ToolName = strings.Repeat("z", 2<<20) // 不是载荷字段，截不掉
	tc.c.Forward(huge)
	tc.c.Forward(auditEntry("after"))
	if ids := queuedIDs(tc.c); len(ids) != 2 || ids[0] != "big" || ids[1] != "after" {
		t.Fatalf("应截断 big、丢弃 huge: %v", ids)
	}
	if !strings.Contains(tc.logs.String(), `"huge"`) {
		t.Fatalf("丢弃应记日志: %s", tc.logs.String())
	}
	tc.run(t)
	waitFor(t, func() bool {
		var got []string
		for _, fr := range f.framesOf(wire.TAudit) {
			var a wire.Audit
			json.Unmarshal(fr, &a)
			for _, raw := range a.Entries {
				var e AuditEntry
				json.Unmarshal(raw, &e)
				got = append(got, e.RequestID)
			}
		}
		return len(got) == 2 && got[0] == "big" && got[1] == "after"
	})
	for _, fr := range f.framesOf(wire.TAudit) {
		if len(fr) > wire.MaxFrame {
			t.Fatalf("帧超过上限: %d", len(fr))
		}
		var a wire.Audit
		json.Unmarshal(fr, &a)
		for _, raw := range a.Entries {
			var e AuditEntry
			json.Unmarshal(raw, &e)
			if e.RequestID == "big" && (!strings.Contains(e.Payload.DiffPreview, "已截断") || e.Payload.Path != "/p/a.txt") {
				t.Fatalf("big 应被截断后上报: diff 长 %d", len(e.Payload.DiffPreview))
			}
		}
	}
}

// 组批按字节：几条大条目分成多帧，每帧不超过上限，条目一条不少。
func TestAuditBatchesByBytes(t *testing.T) {
	setHubTimings(t, nil, 0, 0, 50*time.Millisecond)
	f := newFakeHub(t, 0, welcomeThenRecord)
	tc := newTestClient(t, f.http.URL)
	for i := 0; i < 5; i++ {
		e := auditEntry("big" + itoa(i))
		e.Payload = &policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/a.txt", DiffPreview: strings.Repeat("x", 300<<10)}
		tc.c.Forward(e)
	}
	tc.run(t)
	total := func() int {
		n := 0
		for _, fr := range f.framesOf(wire.TAudit) {
			var a wire.Audit
			json.Unmarshal(fr, &a)
			n += len(a.Entries)
		}
		return n
	}
	waitFor(t, func() bool { return total() == 5 })
	frames := f.framesOf(wire.TAudit)
	if len(frames) < 2 {
		t.Fatalf("1.5 MB 的条目应分多帧: %d 帧", len(frames))
	}
	for _, fr := range frames {
		if len(fr) > wire.MaxFrame-4<<10 {
			t.Fatalf("帧应留 4 KiB 余量: %d", len(fr))
		}
	}
}

// 攒到 50 条立即发，不等定时。
func TestAuditKickAt50(t *testing.T) {
	setHubTimings(t, nil, 0, 0, time.Hour)
	f := newFakeHub(t, 0, welcomeThenHandOver)
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	conn := <-f.conns
	for i := 0; i < auditBatchMax-1; i++ {
		tc.c.Forward(auditEntry("k" + itoa(i)))
	}
	time.Sleep(300 * time.Millisecond)
	f.pingAndWaitPong(t, conn)
	if n := f.count(wire.TAudit); n != 0 {
		t.Fatalf("不满 50 条时不该在定时之前发: %d 帧", n)
	}
	tc.c.Forward(auditEntry("k49"))
	waitFor(t, func() bool { return f.count(wire.TAudit) == 1 })
	var a wire.Audit
	json.Unmarshal(f.framesOf(wire.TAudit)[0], &a)
	if len(a.Entries) != auditBatchMax {
		t.Fatalf("应一次发 50 条: %d", len(a.Entries))
	}
}

// 积压超过总字节上限时丢最旧，只记一次日志；字节计数与队列一致。
func TestAuditQueueByteCap(t *testing.T) {
	setVar(t, &hubAuditMaxBytes, 10_000)
	tc := newTestClient(t, "http://127.0.0.1:1")
	h := tc.c
	for i := 0; i < 30; i++ {
		e := auditEntry("m" + itoa(i))
		e.Payload.Command = strings.Repeat("c", 1000)
		h.Forward(e)
	}
	sum := func() (n, size, counted int) {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, it := range h.auditQ {
			size += len(it.b)
		}
		return len(h.auditQ), size, h.auditBytes
	}
	n, size, counted := sum()
	ids := queuedIDs(h)
	if size > 10_000 || size != counted || n >= 30 || ids[len(ids)-1] != "m29" || ids[0] != "m"+itoa(30-n) {
		t.Fatalf("应按字节丢最旧: n=%d size=%d counted=%d first=%s last=%s", n, size, counted, ids[0], ids[len(ids)-1])
	}
	if c := strings.Count(tc.logs.String(), "丢弃最旧"); c != 1 {
		t.Fatalf("超限只记一次日志: %d", c)
	}
	h.ackAudit(h.auditBatch()[2].seq)
	if n2, size2, counted2 := sum(); n2 != n-3 || size2 != counted2 {
		t.Fatalf("确认后字节计数应随之减少: n=%d size=%d counted=%d", n2, size2, counted2)
	}
}

// 批次编码失败（理论上不会发生）不结束上报：退到只发队头一条，仍编不出就丢弃它并记日志，其余照常上报。
func TestAuditFlushFallsBackOnEncodeError(t *testing.T) {
	setHubTimings(t, nil, 0, 0, 50*time.Millisecond)
	f := newFakeHub(t, 0, welcomeThenRecord)
	tc := newTestClient(t, f.http.URL)
	h := tc.c
	good := func(id string) json.RawMessage {
		b, err := json.Marshal(auditEntry(id))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	h.mu.Lock()
	for _, raw := range []json.RawMessage{good("g0"), json.RawMessage(`{bad`), good("g2"), good("g3")} {
		h.auditSeq++
		h.auditQ = append(h.auditQ, auditItem{seq: h.auditSeq, b: raw})
		h.auditBytes += len(raw)
	}
	h.mu.Unlock()
	tc.run(t)
	got := func() []string {
		var ids []string
		for _, fr := range f.framesOf(wire.TAudit) {
			var a wire.Audit
			json.Unmarshal(fr, &a)
			for _, raw := range a.Entries {
				var e AuditEntry
				json.Unmarshal(raw, &e)
				ids = append(ids, e.RequestID)
			}
		}
		return ids
	}
	waitFor(t, func() bool { return len(got()) == 3 })
	if ids := got(); ids[0] != "g0" || ids[1] != "g2" || ids[2] != "g3" {
		t.Fatalf("坏条目应丢弃、其余按序上报: %v", ids)
	}
	if !strings.Contains(tc.logs.String(), "丢弃 1 条") {
		t.Fatalf("丢弃应记日志: %s", tc.logs.String())
	}
	waitFor(t, func() bool { return len(queuedIDs(h)) == 0 })
	if !tc.c.Connected() || f.dials.Load() != 1 {
		t.Fatal("编码失败不应断开连接")
	}
}
