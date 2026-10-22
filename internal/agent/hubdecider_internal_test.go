package agent

import (
	"context"
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

func (f *fakeHub) hasFrameFor(kind, id string) bool {
	for _, b := range f.framesOf(kind) {
		if strings.Contains(string(b), fmt.Sprintf(`"request_id":%q`, id)) {
			return true
		}
	}
	return false
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
