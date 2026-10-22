package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/railse/railse-opc/internal/hub/wire"
)

// 这里用假 hub 测真实 hub 发不出来的决定（非 allow/deny 的动作、超长字段），以及 SendPending 撞上坏连接时的宽限等待。
// 与真实 hub 的联调在 hubdecider_test.go（package agent_test）。

type decided struct {
	out Outcome
	err error
}

func decideInBackground(d *HubDecider, pr PendingRequest) <-chan decided {
	res := make(chan decided, 1)
	go func() {
		out, err := d.Decide(context.Background(), pr)
		res <- decided{out, err}
	}()
	return res
}

func (f *fakeHub) hasFrameFor(kind, id string) bool { return f.countFor(kind, id) > 0 }

// countFor 数假 hub 收到的某种、某个请求的帧。
func (f *fakeHub) countFor(kind, id string) int {
	n := 0
	for _, b := range f.framesOf(kind) {
		if strings.Contains(string(b), fmt.Sprintf(`"request_id":%q`, id)) {
			n++
		}
	}
	return n
}

// 决定 2：只认 allow、deny——钩子拿固定文案，备注进 Note，RuleID 带上，DecidedBy 截到 64 字符、为空记 user，报送达；
// 其余任何动作一律交回终端（hub_unreachable），hub 给的原因截到 200 字符进 Note，不带 RuleID，不报送达。
func TestHubDeciderNormalizesDecisions(t *testing.T) {
	f := newFakeHub(t, 0, welcomeThenHandOver)
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	conn := <-f.conns
	d := &HubDecider{Client: tc.c, Grace: time.Minute, Log: log.New(tc.logs, "decider ", 0)}
	long := strings.Repeat("长", 300)
	fallback := func(note string) Outcome {
		return Outcome{Action: "passthrough", Reason: reasonNoDecision, DecidedBy: hubUnreachable, Note: note}
	}
	cases := []struct {
		name      string
		dec       wire.Decision
		want      Outcome
		delivered bool
	}{
		{"allow", wire.Decision{Action: "allow", Reason: "看过了", RuleID: "learned-1", DecidedBy: "user"},
			Outcome{Action: "allow", Reason: "已在手机上批准", RuleID: "learned-1", DecidedBy: "user", Note: "看过了"}, true},
		{"deny 无 decided_by", wire.Decision{Action: "deny", Reason: "别推 main"},
			Outcome{Action: "deny", Reason: "已在手机上拒绝", DecidedBy: "user", Note: "别推 main"}, true},
		{"decided_by 超长", wire.Decision{Action: "allow", DecidedBy: strings.Repeat("u", 100)},
			Outcome{Action: "allow", Reason: "已在手机上批准", DecidedBy: strings.Repeat("u", 64)}, true},
		{"passthrough", wire.Decision{Action: "passthrough", Reason: long, RuleID: "x", DecidedBy: "user"}, fallback(strings.Repeat("长", 200)), false},
		{"大写", wire.Decision{Action: "ALLOW", Reason: "r", DecidedBy: "user"}, fallback("r"), false},
		{"带空格", wire.Decision{Action: "allow ", DecidedBy: "user"}, fallback(""), false},
		{"空动作", wire.Decision{Action: "", Reason: "登记失败", DecidedBy: "hub_error"}, fallback("登记失败"), false},
	}
	for i, c := range cases {
		id := fmt.Sprintf("n%d", i)
		res := decideInBackground(d, *pendingFor(id, "claude", time.Now()))
		waitFor(t, func() bool { return f.hasFrameFor(wire.TPending, id) })
		dec := c.dec
		dec.T, dec.RequestID = wire.TDecision, id
		f.send(conn, dec)
		var r decided
		select {
		case r = <-res:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Decide 没有返回", c.name)
		}
		if r.err != nil || r.out != c.want {
			t.Fatalf("%s: 结论 = %+v %v，应为 %+v", c.name, r.out, r.err, c.want)
		}
		// 送达回执在 Decide 返回前已写出；pong 到了说明它之前的帧都已读到
		f.pingAndWaitPong(t, conn)
		if got := f.hasFrameFor(wire.TDelivered, id); got != c.delivered {
			t.Fatalf("%s: 送达回执 = %v，应为 %v", c.name, got, c.delivered)
		}
	}
}

// 决定 1：送出后每次断线都起一次完整的 Grace 计时，Grace 内重连就停表。第二次断线的重连越过了第一次断线的期限，
// 但在它自己的 Grace 内，所以接着等，决定到了照常交给钩子（表若没在重连时停，第一次的期限一到就交回终端了）。
func TestHubDeciderGraceStopsOnReconnect(t *testing.T) {
	const grace = time.Second
	// 连接都不稳定（假 hub 不发 ping）：第一次断线 20 毫秒后重连，第二次退避升一档，500 毫秒后重连
	setHubTimings(t, []time.Duration{20 * time.Millisecond, 500 * time.Millisecond}, 0, 0, 0)
	f := newFakeHub(t, 0, welcomeThenHandOver)
	tc := newTestClient(t, f.http.URL)
	pr := pendingFor("g1", "claude", time.Now())
	tc.pending.Add(pr) // 守护进程里请求在未完成表里，重连后随对齐重发
	tc.run(t)
	tc.waitConnected(t)
	d := &HubDecider{Client: tc.c, Grace: grace, Log: log.New(tc.logs, "decider ", 0)}
	res := decideInBackground(d, *pr)
	waitFor(t, func() bool { return f.hasFrameFor(wire.TPending, "g1") })
	nextConn := func() *websocket.Conn {
		t.Helper()
		select {
		case c := <-f.conns:
			return c
		case <-time.After(5 * time.Second):
			t.Fatal("客户端没有重连")
		}
		return nil
	}
	conn := nextConn()
	conn.CloseNow()
	t0 := time.Now()
	conn = nextConn()
	// 以下只能按时间安排：第二次断线在第一次的期限前 300 毫秒，它的重连（500 毫秒后）越过第一次的期限
	time.Sleep(time.Until(t0.Add(grace - 300*time.Millisecond)))
	conn.CloseNow()
	conn = nextConn()
	tc.waitConnected(t)
	time.Sleep(time.Until(t0.Add(grace*2 - 200*time.Millisecond))) // 两次断线各自的期限都已过
	select {
	case r := <-res:
		t.Fatalf("每次都在 Grace 内重连，不应交回终端: %+v %v", r.out, r.err)
	default:
	}
	f.send(conn, wire.Decision{T: wire.TDecision, RequestID: "g1", Action: "allow", DecidedBy: "user"})
	select {
	case r := <-res:
		if r.err != nil || r.out.Action != "allow" {
			t.Fatalf("重连后应拿到决定: %+v %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Decide 没有返回")
	}
}

// 决定 7：WaitConnected 之后 SendPending 撞上坏掉的连接（报 ErrHubDisconnected）视同已送出后断线——不立即交回终端，
// 接着等（重连后 hub 补发的决定照样交给钩子）。
func TestHubDeciderKeepsWaitingAfterSendHitsBrokenConn(t *testing.T) {
	f := newFakeHub(t, 0, welcomeThenRecord)
	tc := newTestClient(t, f.http.URL) // 不 Run：连接状态由测试摆布
	target, err := wsURL(f.http.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	conn.CloseNow()    // 坏掉的连接：写一定失败
	tc.c.setConn(conn) // 而客户端还当它连着（读循环还没发现）
	d := &HubDecider{Client: tc.c, Grace: time.Minute, Log: log.New(tc.logs, "decider ", 0)}
	res := decideInBackground(d, *pendingFor("w1", "claude", time.Now()))
	// 等待者登记后投递决定，相当于重连后 hub 补发
	waitFor(t, func() bool {
		return tc.c.deliver(wire.Decision{T: wire.TDecision, RequestID: "w1", Action: "allow", DecidedBy: "user"})
	})
	select {
	case r := <-res:
		if r.err != nil || r.out.Action != "allow" || r.out.DecidedBy != "user" {
			t.Fatalf("SendPending 报断线后应接着等决定: %+v %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Decide 没有返回")
	}
}

// 决定 10d：hub 送来的 DecidedBy 若是本机保留值（本机审计里另有含义：引擎规则、暂停、钩子消失、hub 不可达等），一律记为 user。
func TestHubDeciderReservedDecidedByBecomesUser(t *testing.T) {
	f := newFakeHub(t, 0, welcomeThenHandOver)
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	conn := <-f.conns
	d := &HubDecider{Client: tc.c, Grace: time.Minute, Log: log.New(tc.logs, "decider ", 0)}
	for i, by := range []string{"hub_unreachable", "HUB_UNREACHABLE", "gemini", "terminal", "timeout", "shutdown", "error", "paused",
		"no_hub", "hub", "hub_error", "rule:builtin-read", "Rule:x"} {
		id := fmt.Sprintf("rv%d", i)
		res := decideInBackground(d, *pendingFor(id, "claude", time.Now()))
		waitFor(t, func() bool { return f.hasFrameFor(wire.TPending, id) })
		f.send(conn, wire.Decision{T: wire.TDecision, RequestID: id, Action: "allow", DecidedBy: by})
		select {
		case r := <-res:
			if r.err != nil || r.out.Action != "allow" || r.out.DecidedBy != "user" {
				t.Fatalf("decided_by %q 应记为 user: %+v %v", by, r.out, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: Decide 没有返回", by)
		}
	}
	// 不是保留值的照常保留
	res := decideInBackground(d, *pendingFor("ok1", "claude", time.Now()))
	waitFor(t, func() bool { return f.hasFrameFor(wire.TPending, "ok1") })
	f.send(conn, wire.Decision{T: wire.TDecision, RequestID: "ok1", Action: "deny", DecidedBy: "webhook"})
	select {
	case r := <-res:
		if r.err != nil || r.out.DecidedBy != "webhook" {
			t.Fatalf("非保留值应原样保留: %+v %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Decide 没有返回")
	}
}

// 决定 10e：同一 id 连发两帧决定（先 allow 后 deny），结论取第一帧，只发一次送达回执。
func TestHubDeciderDuplicateDecisionFrames(t *testing.T) {
	f := newFakeHub(t, 0, welcomeThenHandOver)
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	conn := <-f.conns
	d := &HubDecider{Client: tc.c, Grace: time.Minute, Log: log.New(tc.logs, "decider ", 0)}
	res := decideInBackground(d, *pendingFor("dup1", "claude", time.Now()))
	waitFor(t, func() bool { return f.hasFrameFor(wire.TPending, "dup1") })
	f.send(conn, wire.Decision{T: wire.TDecision, RequestID: "dup1", Action: "allow", Reason: "第一帧", DecidedBy: "user"})
	f.send(conn, wire.Decision{T: wire.TDecision, RequestID: "dup1", Action: "deny", Reason: "第二帧", DecidedBy: "user"})
	select {
	case r := <-res:
		if r.err != nil || r.out.Action != "allow" || r.out.Note != "第一帧" {
			t.Fatalf("应取第一帧: %+v %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Decide 没有返回")
	}
	// 两帧决定都已被读循环处理、送达回执（若有）也已写出：pong 到了说明之前的帧都处理完了
	f.pingAndWaitPong(t, conn)
	if n := f.countFor(wire.TDelivered, "dup1"); n != 1 {
		t.Fatalf("送达回执应只发一次，得到 %d", n)
	}
}

// recordUntilThenStall：发 welcome，把连接交给测试，读帧直到收到一帧 kind（kind 为空则一帧都不读），之后既不读也不写，
// 直到测试结束：客户端再写大帧就写不完、一直占着写锁。
func recordUntilThenStall(t *testing.T, kind string) func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
	stall := make(chan struct{})
	t.Cleanup(func() { close(stall) }) // 先于假 hub 的 Close 执行（后登记先执行），处理函数才能退出
	return func(f *fakeHub, conn *websocket.Conn, _ wire.Hello) {
		f.welcome(conn, 1, defaultRulesYAML(), false)
		f.conns <- conn
		for kind != "" {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			k, _ := wire.Kind(b)
			f.mu.Lock()
			f.frames = append(f.frames, fakeFrame{kind: k, raw: b})
			f.mu.Unlock()
			if k == kind {
				break
			}
		}
		<-stall
	}
}

// holdWriteLock 在客户端当前连接上写一个对端不读、写不完的大帧，占住写锁，确认锁已被占住才返回。
// 连接被关掉时那次写随之失败返回。
func holdWriteLock(t *testing.T, c *HubClient) {
	t.Helper()
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		t.Fatal("客户端没有连接")
	}
	go conn.Write(context.Background(), websocket.MessageText, make([]byte, 32<<20))
	// 探针拿不到锁（等锁超时，库不关连接）就说明大帧占着锁；探针抢先拿到锁时只是写出一个小帧，再试
	waitFor(t, func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		err := conn.Write(ctx, websocket.MessageText, []byte(`{"t":"probe"}`))
		return errors.Is(err, context.DeadlineExceeded)
	})
}

// 决定 10a：送达、取消、过期回执只是通知，写不出去时最多等 hubNoticeTimeout，不按 hubWriteTimeout 卡住钩子。
func TestHubDeciderNoticesUseShortTimeout(t *testing.T) {
	setVar(t, &hubWriteTimeout, 30*time.Second)
	setVar(t, &hubNoticeTimeout, 200*time.Millisecond)
	setHubTimings(t, []time.Duration{time.Minute}, 0, 0, 0) // 断开后不重连
	t.Run("delivered", func(t *testing.T) {
		f := newFakeHub(t, 0, recordUntilThenStall(t, wire.TPending))
		tc := startTestClient(t, f)
		tc.waitConnected(t)
		d := &HubDecider{Client: tc.c, Grace: time.Minute, Log: log.New(tc.logs, "decider ", 0)}
		res := decideInBackground(d, *pendingFor("n1", "claude", time.Now()))
		waitFor(t, func() bool { return f.hasFrameFor(wire.TPending, "n1") })
		holdWriteLock(t, tc.c)
		start := time.Now()
		tc.c.deliver(wire.Decision{T: wire.TDecision, RequestID: "n1", Action: "allow", DecidedBy: "user"})
		select {
		case r := <-res:
			if r.err != nil || r.out.Action != "allow" {
				t.Fatalf("结论 = %+v %v", r.out, r.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("送达回执写不出去时应很快放弃，不等 hubWriteTimeout")
		}
		if el := time.Since(start); el < 150*time.Millisecond {
			t.Fatalf("应等满 hubNoticeTimeout 才放弃: %s", el)
		}
		if !strings.Contains(tc.logs.String(), `报告请求 "n1" 已送达失败`) {
			t.Fatalf("应记日志: %s", tc.logs.String())
		}
	})
	t.Run("cancel", func(t *testing.T) {
		f := newFakeHub(t, 0, recordUntilThenStall(t, wire.TPending))
		tc := startTestClient(t, f)
		tc.waitConnected(t)
		d := &HubDecider{Client: tc.c, Grace: time.Minute, Log: log.New(tc.logs, "decider ", 0)}
		ctx, cancel := context.WithCancelCause(context.Background())
		res := make(chan decided, 1)
		go func() {
			out, err := d.Decide(ctx, *pendingFor("n2", "claude", time.Now()))
			res <- decided{out, err}
		}()
		waitFor(t, func() bool { return f.hasFrameFor(wire.TPending, "n2") })
		holdWriteLock(t, tc.c)
		cancel(errHookGone)
		select {
		case r := <-res:
			if r.err == nil {
				t.Fatalf("钩子消失应返回错误: %+v", r.out)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("取消回执写不出去时应很快放弃，不等 hubWriteTimeout")
		}
		if !strings.Contains(tc.logs.String(), `告诉 hub 请求 "n2" 已结束失败`) {
			t.Fatalf("应记日志: %s", tc.logs.String())
		}
	})
}

// 决定 10b、10c：SendPending 等写锁超时（库不会自己关连接）时，客户端关掉这条连接，状态变为断开；
// 决定方记一行日志并进入断线宽限，宽限到期仍未重连才交回终端。
func TestHubDeciderSendLockTimeoutEntersGrace(t *testing.T) {
	setVar(t, &hubWriteTimeout, 200*time.Millisecond)
	setHubTimings(t, []time.Duration{time.Minute}, 0, 0, 0) // 断开后不重连
	f := newFakeHub(t, 0, recordUntilThenStall(t, ""))
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	holdWriteLock(t, tc.c)
	const grace = 300 * time.Millisecond
	d := &HubDecider{Client: tc.c, Grace: grace, Log: log.New(tc.logs, "decider ", 0)}
	start := time.Now()
	res := decideInBackground(d, *pendingFor("lk1", "claude", time.Now()))
	waitFor(t, func() bool { return !tc.c.Connected() })
	select {
	case r := <-res:
		if r.err != nil || r.out.Reason != reasonLost || r.out.DecidedBy != hubUnreachable {
			t.Fatalf("应在宽限到期后交回终端: %+v %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Decide 没有返回")
	}
	if el := time.Since(start); el < 200*time.Millisecond+grace {
		t.Fatalf("应先等写锁超时、再等满宽限: %s", el)
	}
	if !strings.Contains(tc.logs.String(), `请求 "lk1" 送往 hub 时断线`) {
		t.Fatalf("报断线时应记日志: %s", tc.logs.String())
	}
}

// 审计上报等写锁超时同样关掉连接：否则上报循环退出后连接还当作连着，积压的审计要等到下次断线才补发。
func TestHubAuditLockTimeoutDisconnects(t *testing.T) {
	setVar(t, &hubWriteTimeout, 200*time.Millisecond)
	setHubTimings(t, []time.Duration{time.Minute}, 0, 0, 20*time.Millisecond)
	f := newFakeHub(t, 0, recordUntilThenStall(t, ""))
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	holdWriteLock(t, tc.c)
	tc.c.Forward(auditEntry("a1"))
	waitFor(t, func() bool { return !tc.c.Connected() })
	if q := queuedIDs(tc.c); len(q) != 1 || q[0] != "a1" {
		t.Fatalf("没发出去的审计应留在队里: %v", q)
	}
}

// 修复轮 1：回执只是通知，等写锁超时只报错、不关连接——不打断正在写的大帧，也不让在途请求进入断线宽限。
// “等锁超时就断开”只留给 SendPending 与审计上报。
func TestHubNoticeLockTimeoutKeepsConnection(t *testing.T) {
	setHubTimings(t, []time.Duration{time.Minute}, 0, 0, 0) // 断开后不重连：连接若被关，下面立刻看得出
	f := newFakeHub(t, 0, recordUntilThenStall(t, ""))
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	holdWriteLock(t, tc.c)
	_, changed := tc.c.ConnState()
	for name, send := range map[string]func(ctx context.Context) error{
		"delivered": func(ctx context.Context) error { return tc.c.SendDelivered(ctx, "x") },
		"cancel":    func(ctx context.Context) error { return tc.c.SendCancel(ctx, "x", "terminal") },
		"expired":   func(ctx context.Context) error { return tc.c.SendExpired(ctx, "x") },
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		err := send(ctx)
		cancel()
		if err == nil || errors.Is(err, ErrHubDisconnected) {
			t.Fatalf("%s: 等锁超时应报错，但不是断线: %v", name, err)
		}
	}
	select {
	case <-changed:
		t.Fatal("回执超时不应断开连接")
	default:
	}
	if !tc.c.Connected() {
		t.Fatal("连接应仍在")
	}
	// 大帧仍占着写锁、没被打断：探针照样拿不到锁
	tc.c.mu.Lock()
	conn := tc.c.conn
	tc.c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"t":"probe"}`)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("大帧应仍在写: %v", err)
	}
}
