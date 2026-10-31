package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/xufei547/railse-opc/internal/adapter"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// askSecret 是 redact 认得的 GitHub 令牌形状；题目里夹着它时，上报 hub 与审计都只能见到 <redacted>。
const askSecret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"

// askReq 造一条 ask 请求，questions 依次是各题题面，每题两个选项。
func askReq(questions ...string) proto.Request {
	qs := make([]proto.Question, len(questions))
	for i, q := range questions {
		qs[i] = proto.Question{Question: q, Header: "选择", Options: []proto.Option{{Label: "A", Description: "甲"}, {Label: "B"}}}
	}
	return proto.Request{V: proto.Version, Kind: "ask", Session: proto.Session{Tool: "claude", CLISessionID: "s1", TurnID: "t1", Cwd: "/p"},
		ToolName: "AskUserQuestion", Ask: &proto.Ask{Questions: qs}, DeadlineSeconds: 86390}
}

// askDecider 记下收到的请求，结论由 answer 给出。
type askDecider struct {
	mu     sync.Mutex
	got    []PendingRequest
	answer func(pr PendingRequest) Outcome
}

func (d *askDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error) {
	d.mu.Lock()
	d.got = append(d.got, pr)
	d.mu.Unlock()
	return d.answer(pr), nil
}

func (d *askDecider) seen() []PendingRequest {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]PendingRequest(nil), d.got...)
}

// answerAll 给每道（脱敏后的）题都答 a。
func answerAll(a string) func(pr PendingRequest) Outcome {
	return func(pr PendingRequest) Outcome {
		ans := map[string]string{}
		for _, q := range pr.Ask.Questions {
			ans[q.Question] = a
		}
		return Outcome{Action: "answer", Answers: ans, Reason: "已在手机上回答", DecidedBy: "user"}
	}
}

func askOnce(t *testing.T, env *testEnv, req proto.Request) proto.Response {
	t.Helper()
	var resp proto.Response
	if err := json.Unmarshal(roundTrip(t, env.cfg.SocketPath, req), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// askAsync 在后台发一条请求，答复从返回的通道读（出错时读到零值）；不调 t.Fatal。
func askAsync(sock string, req proto.Request) <-chan proto.Response {
	res := make(chan proto.Response, 1)
	go func() {
		var resp proto.Response
		defer func() { res <- resp }()
		conn, err := net.DialTimeout("unix", sock, 2*time.Second)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		if proto.WriteLine(conn, req) != nil {
			return
		}
		line, err := proto.ReadLine(bufio.NewReader(conn))
		if err != nil {
			return
		}
		_ = json.Unmarshal(line, &resp)
	}()
	return res
}

// askText 把题目的各个文本字段连成一串，便于查脱敏。
func askText(a *proto.Ask) string {
	var b strings.Builder
	for _, q := range a.Questions {
		b.WriteString(q.Question + "\n" + q.Header + "\n")
		for _, o := range q.Options {
			b.WriteString(o.Label + "\n" + o.Description + "\n")
		}
	}
	return b.String()
}

func onlyAudit(t *testing.T, env *testEnv) AuditEntry {
	t.Helper()
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 1 {
		t.Fatalf("应只有一条审计: %+v", entries)
	}
	return entries[0]
}

// 题目、标题、选项都过 redact.Text 再交给 Decider 与审计；hub 按脱敏题面作答，钩子拿到的答案按序号换回原题面（含令牌原文）。
// 多余的键忽略；审计里的答案键是脱敏题面，值也过 redact.Text。
func TestAskAnswerUsesOriginalQuestions(t *testing.T) {
	q1, q2 := "用令牌 "+askSecret+" 推送吗？", "第二题"
	req := askReq(q1, q2)
	req.Ask.Questions[0].Header = "令牌 " + askSecret
	req.Ask.Questions[0].Options = []proto.Option{{Label: "用 " + askSecret, Description: "描述里也有 " + askSecret}, {Label: "B"}}
	d := &askDecider{answer: func(pr PendingRequest) Outcome {
		out := answerAll("A " + askSecret)(pr)
		out.Answers["不是题目"] = "多余"
		return out
	}}
	env := startServer(t, d, nil)
	resp := askOnce(t, env, req)
	want := map[string]string{q1: "A " + askSecret, q2: "A " + askSecret}
	if resp.Decision != "answer" || !reflect.DeepEqual(resp.Answers, want) || resp.RequestID == "" {
		t.Fatalf("答复应按原题面带答案: %+v", resp)
	}
	seen := d.seen()
	if len(seen) != 1 {
		t.Fatalf("Decider 应收到一条: %d", len(seen))
	}
	pr := seen[0]
	if text := askText(pr.Ask); pr.Kind != "ask" || strings.Contains(text, askSecret) || strings.Count(text, "<redacted>") != 4 {
		t.Fatalf("交给 Decider 的题目应已脱敏: kind=%q %s", pr.Kind, text)
	}
	if pr.Req.Ask.Questions[0].Question != q1 {
		t.Fatalf("原始请求应保留原题面: %+v", pr.Req.Ask)
	}
	e := onlyAudit(t, env)
	if e.Kind != "ask" || e.ToolName != "AskUserQuestion" || e.State != "answered" || e.Action != "answer" || e.DecidedBy != "user" ||
		e.Payload != nil || e.Risk != "" || e.RequestID != resp.RequestID {
		t.Fatalf("审计: %+v", e)
	}
	if !reflect.DeepEqual(e.Ask, pr.Ask) {
		t.Fatalf("审计应记脱敏后的题目: %+v", e.Ask)
	}
	rq1 := pr.Ask.Questions[0].Question
	if !reflect.DeepEqual(e.Answers, map[string]string{rq1: "A <redacted>", q2: "A <redacted>"}) {
		t.Fatalf("审计的答案应以脱敏题面为键、值也脱敏、不含多余键: %+v", e.Answers)
	}
	raw, _ := os.ReadFile(env.cfg.AuditPath)
	if strings.Contains(string(raw), askSecret) {
		t.Fatalf("审计里出现了令牌原文:\n%s", raw)
	}
}

// 没接 hub（LocalDecider）：立即交回终端，审计 passthrough/no_hub。
func TestAskNoHub(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	resp := askOnce(t, env, askReq("q"))
	if resp.Decision != "passthrough" || len(resp.Answers) != 0 {
		t.Fatalf("没接 hub 应交回终端: %+v", resp)
	}
	if e := onlyAudit(t, env); e.Kind != "ask" || e.Action != "passthrough" || e.State != "passthrough" || e.DecidedBy != "no_hub" || e.Ask == nil {
		t.Fatalf("审计应记 passthrough/no_hub: %+v", e)
	}
}

// 答案不完整（缺题、空答案、超过 1000 字符）：整条交回终端，日志说一句，审计 passthrough/error；1000 字符正好可以。
func TestAskBadAnswersPassthrough(t *testing.T) {
	answers := func(m map[string]string) func(PendingRequest) Outcome {
		return func(PendingRequest) Outcome { return Outcome{Action: "answer", Answers: m, DecidedBy: "user"} }
	}
	cases := map[string]func(PendingRequest) Outcome{
		"没有答案": answers(nil),
		"缺一题":  answers(map[string]string{"q1": "A"}),
		"空答案":  answers(map[string]string{"q1": "A", "q2": ""}),
		"超长":   answers(map[string]string{"q1": "A", "q2": strings.Repeat("长", 1001)}),
	}
	for name, answer := range cases {
		logs := &logBuf{}
		env := startServerLog(t, &askDecider{answer: answer}, nil, logs)
		resp := askOnce(t, env, askReq("q1", "q2"))
		if resp.Decision != "passthrough" || len(resp.Answers) != 0 {
			t.Fatalf("%s: 应交回终端: %+v", name, resp)
		}
		if e := onlyAudit(t, env); e.Action != "passthrough" || e.State != "passthrough" || e.DecidedBy != "error" || e.Answers != nil {
			t.Fatalf("%s: 审计应记 passthrough/error: %+v", name, e)
		}
		if !strings.Contains(logs.String(), "答案") {
			t.Fatalf("%s: 日志应说明答案不合规:\n%s", name, logs.String())
		}
	}
	env := startServer(t, &askDecider{answer: answers(map[string]string{"q1": strings.Repeat("长", 1000)})}, nil)
	if resp := askOnce(t, env, askReq("q1")); resp.Decision != "answer" {
		t.Fatalf("1000 字符的答案应接受: %+v", resp)
	}
}

// 请求本身不合规（没有题目、题面为空、题面重复）：立即交回终端，不交给 Decider。
func TestAskMalformedRequest(t *testing.T) {
	d := &askDecider{answer: answerAll("A")}
	env := startServer(t, d, nil)
	noAsk := askReq("q")
	noAsk.Ask = nil
	empty := askReq("q")
	empty.Ask.Questions = nil
	for name, req := range map[string]proto.Request{"没有 ask": noAsk, "没有题目": empty, "题面为空": askReq(""), "题面重复": askReq("q", "q")} {
		resp := askOnce(t, env, req)
		if resp.Decision != "passthrough" || !strings.Contains(resp.Reason, "请求格式错误") {
			t.Fatalf("%s: 应按请求格式错误交回终端: %+v", name, resp)
		}
	}
	if n := len(d.seen()); n != 0 {
		t.Fatalf("不合规的请求不应交给 Decider: %d", n)
	}
}

// 两道题脱敏后题面相同（答案按题面为键就分不开）：不送 hub，直接交回终端，审计 passthrough/error。
func TestAskDuplicateRedactedQuestions(t *testing.T) {
	d := &askDecider{answer: answerAll("A")}
	env := startServer(t, d, nil)
	resp := askOnce(t, env, askReq("令牌 "+askSecret+" 吗？", "令牌 ghp_ZYXWVUTSRQPONMLKJIHGFEDCBA9876543210 吗？"))
	if resp.Decision != "passthrough" {
		t.Fatalf("应交回终端: %+v", resp)
	}
	if n := len(d.seen()); n != 0 {
		t.Fatalf("脱敏后题面重复不应交给 Decider: %d", n)
	}
	if e := onlyAudit(t, env); e.Kind != "ask" || e.State != "passthrough" || e.DecidedBy != "error" || e.Reason == "" {
		t.Fatalf("审计: %+v", e)
	}
}

// 答题不是审批：暂停开关打开时照样交给 Decider。
func TestAskIgnoresPause(t *testing.T) {
	d := &askDecider{answer: answerAll("A")}
	env := startServer(t, d, nil)
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("pause")), &reply)
	if !reply.OK || !env.srv.rules.Paused() {
		t.Fatalf("暂停失败: %+v", reply)
	}
	if resp := askOnce(t, env, askReq("q")); resp.Decision != "answer" || len(d.seen()) != 1 {
		t.Fatalf("暂停时 ask 照样送 Decider: %+v", resp)
	}
}

// 钩子在答复前退出：审计 cancelled/terminal；ask 待答期间压掉同一会话的 question 事件。
func TestAskHookGoneCancelled(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{})}
	env := startServer(t, d, nil)
	conn, err := net.Dial("unix", env.cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	proto.WriteLine(conn, askReq("q"))
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	if l := env.srv.pending.List(); l[0].Kind != "ask" || l[0].Ask == nil {
		t.Fatalf("未完成表里应是 ask: %+v", l[0])
	}
	var ack proto.EventAck
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, event("question", "s1", "等你回答")), &ack)
	conn.Close()
	waitFor(t, func() bool { return env.srv.pending.Count() == 0 })
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 2 || entries[0].State != "suppressed" {
		t.Fatalf("ask 待答时 question 应被压掉: %+v", entries)
	}
	if e := entries[1]; e.Kind != "ask" || e.State != "cancelled" || e.DecidedBy != "terminal" || e.Action != "" {
		t.Fatalf("审计应记 cancelled/terminal: %+v", e)
	}
}

// 等待过期：交回终端，审计 expired/timeout。
func TestAskExpired(t *testing.T) {
	env := startServer(t, &blockingDecider{release: make(chan struct{})}, func(c *Config) { c.Deadline = 200 * time.Millisecond })
	if resp := askOnce(t, env, askReq("q")); resp.Decision != "passthrough" || !strings.Contains(resp.Reason, "过期") {
		t.Fatalf("到期应交回终端: %+v", resp)
	}
	if e := onlyAudit(t, env); e.Kind != "ask" || e.State != "expired" || e.DecidedBy != "timeout" {
		t.Fatalf("审计应记 expired/timeout: %+v", e)
	}
}

// Decider 的结论不是 answer（审批的 allow、手机上选了在终端回答、hub 不可达）：交回终端，审计口径与审批相同。
func TestAskNonAnswerOutcome(t *testing.T) {
	cases := []struct {
		out              Outcome
		state, decidedBy string
	}{
		{Outcome{Action: "allow", Reason: "已在手机上批准", DecidedBy: "user"}, "passthrough", "user"},
		{Outcome{Action: "passthrough", Reason: "在手机上选了在终端回答", DecidedBy: "user"}, "passthrough", "user"},
		{Outcome{Action: "answer", Answers: map[string]string{"q": "A"}, Reason: "连不上 hub，交回终端处理", DecidedBy: hubUnreachable}, "expired", hubUnreachable},
	}
	for _, c := range cases {
		env := startServer(t, fixedDecider{out: c.out}, nil)
		if resp := askOnce(t, env, askReq("q")); resp.Decision != "passthrough" || len(resp.Answers) != 0 {
			t.Fatalf("%+v: 应交回终端: %+v", c.out, resp)
		}
		if e := onlyAudit(t, env); e.Action != "passthrough" || e.State != c.state || e.DecidedBy != c.decidedBy || e.Answers != nil {
			t.Fatalf("%+v: 审计应记 %s/%s: %+v", c.out, c.state, c.decidedBy, e)
		}
	}
}

// toWire 对 ask：kind ask、脱敏后的题目、工具名 AskUserQuestion、空载荷、空风险（Risk 零值的 String 是 read，不能用）。
// 审批帧的 JSON 里没有 kind、ask 两个键。
func TestToWireAsk(t *testing.T) {
	red := &proto.Ask{Questions: []proto.Question{{Question: "用 <redacted> 吗？", Options: []proto.Option{{Label: "A"}}}}}
	now := time.Now()
	pr := PendingRequest{ID: "a1", Kind: "ask", Req: askReq("用 " + askSecret + " 吗？"), Ask: red, Created: now, Deadline: now.Add(time.Minute)}
	pr.Req.ToolName = "别的名字"
	w := toWire(pr)
	if w.Kind != "ask" || w.Ask != red || w.ToolName != "AskUserQuestion" || w.Risk != "" || w.EngineReason != "" ||
		!reflect.DeepEqual(w.Payload, policy.Payload{}) || w.DeadlineSeconds != 60 || w.RequestID != "a1" || w.Session.CLISessionID != "s1" {
		t.Fatalf("ask 帧字段不对: %+v", w)
	}
	b, err := wire.Marshal(w)
	if err != nil || strings.Contains(string(b), askSecret) || !strings.Contains(string(b), `"risk":""`) {
		t.Fatalf("ask 帧: %s %v", b, err)
	}
	b, err = wire.Marshal(toWire(*pendingFor("p1", "claude", now)))
	if err != nil || strings.Contains(string(b), `"kind"`) || strings.Contains(string(b), `"ask"`) || !strings.Contains(string(b), `"risk":"outward"`) {
		t.Fatalf("审批帧不应带 kind、ask: %s %v", b, err)
	}
}

// 真的 HubDecider 对着假 hub：上报帧与审计帧里题目已脱敏；hub 按脱敏题面作答，钩子按原题面拿到答案。
// hub 只认 answer（每道题都答了）与 passthrough（手机上选了在终端回答）两种结论，二者都报送达；其余（旧 hub 当审批批了 allow、
// 答案不完整）按 hub 没给出有效决定交回终端，不报送达。
func TestAskThroughHub(t *testing.T) {
	f := newFakeHub(t, 0, welcomeThenHandOver)
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	conn := <-f.conns
	d := &HubDecider{Client: tc.c, Grace: time.Minute, Log: log.New(tc.logs, "decider ", 0)}
	t.Cleanup(d.Wait)
	q1 := "用令牌 " + askSecret + " 推送吗？"
	cases := []struct {
		name             string
		dec              func(redQ string) wire.Decision
		want             proto.Response
		state, decidedBy string
		delivered        bool
	}{
		{"answer", func(redQ string) wire.Decision {
			return wire.Decision{Action: "answer", Answers: map[string]string{redQ: "Banana", "第二题": "都不要"}, DecidedBy: "user"}
		}, proto.Response{Decision: "answer", Answers: map[string]string{q1: "Banana", "第二题": "都不要"}}, "answered", "user", true},
		{"passthrough", func(string) wire.Decision {
			return wire.Decision{Action: "passthrough", DecidedBy: "user"}
		}, proto.Response{Decision: "passthrough"}, "passthrough", "user", true},
		{"旧 hub 批了 allow", func(string) wire.Decision {
			return wire.Decision{Action: "allow", DecidedBy: "user"}
		}, proto.Response{Decision: "passthrough"}, "expired", hubUnreachable, false},
		{"缺一题", func(redQ string) wire.Decision {
			return wire.Decision{Action: "answer", Answers: map[string]string{redQ: "Banana"}, DecidedBy: "user"}
		}, proto.Response{Decision: "passthrough"}, "expired", hubUnreachable, false},
	}
	for i, c := range cases {
		env := startServer(t, d, nil)
		env.srv.SetAuditSink(tc.c)
		res := askAsync(env.cfg.SocketPath, askReq(q1, "第二题"))
		waitFor(t, func() bool { return f.count(wire.TPending) == i+1 })
		frame := f.framesOf(wire.TPending)[i]
		for _, want := range []string{`"kind":"ask"`, `"tool_name":"AskUserQuestion"`, `"risk":""`, `"ask":{"questions":[`} {
			if !strings.Contains(string(frame), want) {
				t.Fatalf("%s: 上报帧缺 %s: %s", c.name, want, frame)
			}
		}
		if strings.Contains(string(frame), askSecret) {
			t.Fatalf("%s: 上报帧里出现了令牌原文: %s", c.name, frame)
		}
		var pw wire.PendingRequest
		if err := json.Unmarshal(frame, &pw); err != nil {
			t.Fatal(err)
		}
		redQ := pw.Ask.Questions[0].Question
		if !strings.Contains(redQ, "<redacted>") {
			t.Fatalf("%s: 上报的题面应已脱敏: %q", c.name, redQ)
		}
		dec := c.dec(redQ)
		dec.T, dec.RequestID = wire.TDecision, pw.RequestID
		f.send(conn, dec)
		var resp proto.Response
		select {
		case resp = <-res:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: 钩子没有拿到答复", c.name)
		}
		if resp.Decision != c.want.Decision || !reflect.DeepEqual(resp.Answers, c.want.Answers) {
			t.Fatalf("%s: 答复 = %+v，应为 %+v", c.name, resp, c.want)
		}
		if e := onlyAudit(t, env); e.Kind != "ask" || e.State != c.state || e.DecidedBy != c.decidedBy {
			t.Fatalf("%s: 审计应记 %s/%s: %+v", c.name, c.state, c.decidedBy, e)
		}
		// 本机审计也上报到了 hub，同样脱敏
		waitFor(t, func() bool { return f.hasFrameFor(wire.TAudit, pw.RequestID) })
		for _, b := range f.framesOf(wire.TAudit) {
			if strings.Contains(string(b), askSecret) {
				t.Fatalf("%s: 审计帧里出现了令牌原文: %s", c.name, b)
			}
		}
		d.Wait()
		f.pingAndWaitPong(t, conn)
		if got := f.hasFrameFor(wire.TDelivered, pw.RequestID); got != c.delivered {
			t.Fatalf("%s: 送达回执 = %v，应为 %v", c.name, got, c.delivered)
		}
	}
	if !strings.Contains(tc.logs.String(), "决定无效") {
		t.Fatal("无效决定应记日志")
	}
}

// List 返回的题目（脱敏的与原始的）也是深拷贝：改返回值不影响表里正在等待的请求。
func TestPendingListCopiesAsk(t *testing.T) {
	p := newPending()
	req := askReq("q")
	p.Add(&PendingRequest{ID: "a", Kind: "ask", Ask: redactAsk(req.Ask), Req: req})
	got := p.List()
	got[0].Ask.Questions[0].Question = "改了"
	got[0].Req.Ask.Questions[0].Options[0].Label = "改了"
	again := p.List()
	if again[0].Ask.Questions[0].Question != "q" || again[0].Req.Ask.Questions[0].Options[0].Label != "A" {
		t.Fatalf("改 List 返回的题目不应影响表: %+v %+v", again[0].Ask, again[0].Req.Ask)
	}
}

// ask 请求的会话 cwd 在 git 仓库里时，登记的 PendingRequest 与上报帧都带该仓库的项目根（只算路径，不看规则）；
// cwd 为空或不是绝对路径时项目根留空。
func TestAskProjectRoot(t *testing.T) {
	d := &askDecider{answer: answerAll("A")}
	env := startServer(t, d, nil)
	req := askReq("用 A 还是 B？")
	req.Session.Cwd = env.repo
	if resp := askOnce(t, env, req); resp.Decision != "answer" {
		t.Fatalf("答复: %+v", resp)
	}
	got := d.seen()
	if len(got) != 1 {
		t.Fatalf("应收到一条请求: %+v", got)
	}
	want := FindProjectRoot(CleanCwd(env.repo))
	if want == "" || got[0].ProjectRoot != want {
		t.Fatalf("ProjectRoot = %q，应为 %q", got[0].ProjectRoot, want)
	}
	if w := toWire(got[0]); w.ProjectRoot != want {
		t.Fatalf("上报帧 project_root = %q，应为 %q", w.ProjectRoot, want)
	}
	for _, cwd := range []string{"", "rel/dir"} {
		d2 := &askDecider{answer: answerAll("A")}
		env2 := startServer(t, d2, nil)
		r := askReq("用 A 还是 B？")
		r.Session.Cwd = cwd
		askOnce(t, env2, r)
		if g := d2.seen(); len(g) != 1 || g[0].ProjectRoot != "" {
			t.Fatalf("cwd %q 时项目根应为空: %+v", cwd, g)
		}
	}
}

// askPendingFor 造一条登记好的 ask 未完成请求（题目已脱敏），期限 10 分钟。
func askPendingFor(id string) *PendingRequest {
	req := askReq("用 A 还是 B？")
	now := time.Now()
	return &PendingRequest{ID: id, Kind: "ask", Ask: redactAsk(req.Ask), Req: req, Created: now, Deadline: now.Add(10 * time.Minute)}
}

// 旧 hub（0.1.4 及以前，welcome 不带 features）不认得提问：会把提问帧当作空载荷的审批登记、推送，终端一直卡到有人在手机上点。
// 所以守护进程不把提问送给它：立即交回终端（钩子收 passthrough，本机审计 expired/hub_unreachable，理由说 hub 太旧），
// 没有发出 pending 帧，日志说一句；SendPending 对提问也拒绝（ErrHubNoAsk）。审批照常送 hub。
func TestAskOldHubPassthrough(t *testing.T) {
	f := newFakeHub(t, 0, oldWelcomeThenHandOver)
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	conn := <-f.conns
	if tc.c.HasFeature(wire.FeatureAsk) {
		t.Fatal("旧 hub 没有声明认得提问")
	}
	d := &HubDecider{Client: tc.c, Grace: time.Minute, Log: log.New(tc.logs, "decider ", 0)}
	t.Cleanup(d.Wait)
	env := startServer(t, d, nil)
	env.srv.SetAuditSink(tc.c)
	var resp proto.Response
	select {
	case resp = <-askAsync(env.cfg.SocketPath, askReq("q")):
	case <-time.After(5 * time.Second):
		t.Fatal("旧 hub 时提问应立即交回终端，不该等")
	}
	if resp.Decision != "passthrough" || len(resp.Answers) != 0 || resp.Reason != reasonOldHub {
		t.Fatalf("应交回终端并说明 hub 太旧: %+v", resp)
	}
	e := onlyAudit(t, env)
	if e.Kind != "ask" || e.Action != "passthrough" || e.State != "expired" || e.DecidedBy != hubUnreachable || e.Reason != reasonOldHub {
		t.Fatalf("审计应记 expired/hub_unreachable: %+v", e)
	}
	// 认准 HubDecider.oldHub 自己那一行（带 decider 前缀与该请求 id）：HubClient 连上时那行日志也含“太旧”，不能拿它充数
	if want := fmt.Sprintf("decider hub 版本太旧，不认识提问，请求 %q 交回终端", e.RequestID); e.RequestID == "" || !strings.Contains(tc.logs.String(), want) {
		t.Fatalf("日志应有 decider 的一行 %q:\n%s", want, tc.logs.String())
	}
	if err := tc.c.SendPending(*askPendingFor("a2")); !errors.Is(err, ErrHubNoAsk) {
		t.Fatalf("SendPending 对提问应报 ErrHubNoAsk: %v", err)
	}
	// 审批照常送 hub、等手机上的决定
	res := decideInBackground(d, *pendingFor("p1", "claude", time.Now()))
	waitFor(t, func() bool { return f.hasFrameFor(wire.TPending, "p1") })
	f.send(conn, wire.Decision{T: wire.TDecision, RequestID: "p1", Action: "allow", DecidedBy: "user"})
	select {
	case r := <-res:
		if r.err != nil || r.out.Action != "allow" {
			t.Fatalf("审批应照常拿到决定: %+v %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("审批的 Decide 没有返回")
	}
	f.pingAndWaitPong(t, conn)
	if n := f.count(wire.TPending); n != 1 {
		t.Fatalf("只应发出审批的 pending 帧，提问的一帧都不该发: %d", n)
	}
}

// 等提问的答案时断线、重连到的 hub 若不声明认得提问（降级到旧 hub）：重连对齐不重发这条提问，等待者立即交回终端（不等 Grace）。
// 重连到的仍是新 hub：照常重发、接着等，答案到了交给钩子。
func TestAskReconnectHubFeatures(t *testing.T) {
	for _, old := range []bool{true, false} {
		setHubTimings(t, []time.Duration{20 * time.Millisecond}, 0, 0, 0)
		var dials atomic.Int32
		f := newFakeHub(t, 0, func(f *fakeHub, conn *websocket.Conn, h wire.Hello) {
			if dials.Add(1) > 1 && old {
				oldWelcomeThenHandOver(f, conn, h)
				return
			}
			welcomeThenHandOver(f, conn, h)
		})
		tc := startTestClient(t, f)
		tc.waitConnected(t)
		conn := <-f.conns
		pr := askPendingFor("k1")
		tc.pending.Add(pr) // 守护进程里请求在未完成表里，重连后随对齐重发
		d := &HubDecider{Client: tc.c, Grace: time.Minute, Log: log.New(tc.logs, "decider ", 0)}
		res := decideInBackground(d, *pr)
		waitFor(t, func() bool { return f.hasFrameFor(wire.TPending, "k1") })
		conn.CloseNow()
		var conn2 *websocket.Conn
		select {
		case conn2 = <-f.conns:
		case <-time.After(5 * time.Second):
			t.Fatal("客户端没有重连")
		}
		if !old {
			waitFor(t, func() bool { return f.countFor(wire.TPending, "k1") == 2 })
			f.send(conn2, wire.Decision{T: wire.TDecision, RequestID: "k1", Action: "answer", Answers: map[string]string{"用 A 还是 B？": "A"}, DecidedBy: "user"})
		}
		var r decided
		select {
		case r = <-res:
		case <-time.After(5 * time.Second):
			t.Fatalf("old=%v: Decide 没有返回", old)
		}
		switch {
		case old && (r.err != nil || r.out.Action != "passthrough" || r.out.DecidedBy != hubUnreachable || r.out.Reason != reasonOldHub):
			t.Fatalf("重连到旧 hub 应立即交回终端: %+v %v", r.out, r.err)
		case !old && (r.err != nil || r.out.Action != "answer" || r.out.Answers["用 A 还是 B？"] != "A"):
			t.Fatalf("重连到新 hub 应接着等到答案: %+v %v", r.out, r.err)
		}
		d.Wait()
		// pong 在读循环里回，读循环在重连对齐之后才开始：pong 到了说明对齐已发完
		f.pingAndWaitPong(t, conn2)
		want := 2
		if old {
			want = 1
		}
		if n := f.countFor(wire.TPending, "k1"); n != want {
			t.Fatalf("old=%v: 提问的 pending 帧 %d 条，应为 %d（旧 hub 不重发）", old, n, want)
		}
	}
}

// 脱敏会让短值变长（token=x 变成 token=<redacted>）：原题合规、脱敏后超出上限的题 hub 会拒收，守护进程在本机就交回终端，
// 不交给 Decider，审计 passthrough/error，理由说清是脱敏后超限。
func TestAskRedactedOverLimit(t *testing.T) {
	d := &askDecider{answer: answerAll("A")}
	logs := &logBuf{}
	env := startServerLog(t, d, nil, logs)
	req := askReq("q")
	req.Ask.Questions[0].Header = strings.Repeat("长", 52) + " token=x" // 正好 60 字符
	if err := adapter.ValidateAsk(req.Ask); err != nil {
		t.Fatalf("原题应合规: %v", err)
	}
	if err := adapter.ValidateAsk(redactAsk(req.Ask)); err == nil {
		t.Fatal("前提：脱敏后的标题应超出上限")
	}
	resp := askOnce(t, env, req)
	if resp.Decision != "passthrough" || !strings.Contains(resp.Reason, "脱敏后题目超出上限") {
		t.Fatalf("应交回终端并说明脱敏后超限: %+v", resp)
	}
	if n := len(d.seen()); n != 0 {
		t.Fatalf("脱敏后超限的题不应交给 Decider: %d", n)
	}
	if e := onlyAudit(t, env); e.Kind != "ask" || e.Action != "passthrough" || e.State != "passthrough" || e.DecidedBy != "error" ||
		!strings.Contains(e.Reason, "脱敏后题目超出上限") || e.Ask == nil {
		t.Fatalf("审计应记 passthrough/error: %+v", e)
	}
	if !strings.Contains(logs.String(), "脱敏后") {
		t.Fatalf("日志应说一句:\n%s", logs.String())
	}
}
