package agent_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hub"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

func pendingFor(tool, id string) agent.PendingRequest {
	return agent.PendingRequest{ID: id, Req: proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: tool, CLISessionID: "s1", TurnID: "t1", Cwd: "/p"}, ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push", Cwd: "/p"}, DeadlineSeconds: 60},
		Risk: policy.RiskOutward, ProjectRoot: "/p", Redacted: policy.Payload{Type: policy.PayloadShell, Command: "git push"}, EngineReason: "无规则命中", Created: time.Now(), Deadline: time.Now().Add(time.Minute)}
}

type decideResult struct {
	out agent.Outcome
	err error
}

// decideAsync 在后台调 Decide，结果从返回的通道读。
func decideAsync(ctx context.Context, d *agent.HubDecider, pr agent.PendingRequest) <-chan decideResult {
	done := make(chan decideResult, 1)
	go func() {
		out, err := d.Decide(ctx, pr)
		done <- decideResult{out, err}
	}()
	return done
}

func waitDecide(t *testing.T, done <-chan decideResult, d time.Duration, msg string) decideResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(d):
		t.Fatal(msg)
	}
	return decideResult{}
}

// isUnreachable 报告结论是不是“交回终端”（用户决定 A：DecidedBy hub_unreachable，本机审计记 expired）。
func isUnreachable(out agent.Outcome) bool {
	return out.Action == "passthrough" && out.DecidedBy == "hub_unreachable" && out.Reason != "" && out.RuleID == ""
}

// waitHubState 等 hub 里这条请求变成 state（decidedBy 非空时也要相符）。
func waitHubState(t *testing.T, h *hubEnv, id, state, decidedBy string) {
	t.Helper()
	var r store.Request
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for {
		r, err = h.st.GetRequest(id)
		if err == nil && r.State == state && (decidedBy == "" || r.DecidedBy == decidedBy) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub 应记 %s/%s，得到 %+v %v", state, decidedBy, r, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Gemini 只能拒绝不能放行：直接交回原生确认框，不碰 hub（Client 为 nil，碰了就 panic）；工具名不区分大小写。
func TestHubDeciderGeminiPassthrough(t *testing.T) {
	d := &agent.HubDecider{Client: nil, Grace: time.Second, Log: log.New(&syncBuf{}, "", 0)}
	for _, tool := range []string{"gemini", "Gemini", "GEMINI"} {
		out, err := d.Decide(context.Background(), pendingFor(tool, "g1"))
		if err != nil || out.Action != "passthrough" || out.DecidedBy != "gemini" {
			t.Fatalf("%s 应直接交回: %+v %v", tool, out, err)
		}
	}
}

func TestHubDeciderUnreachable(t *testing.T) {
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := agent.ConfigIn(dir)
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	rules, err := agent.LoadRules(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuf{}
	noPause := func(bool) error { return nil }
	c := agent.NewHubClient(cfg, agent.MachineConfig{HubURL: "http://127.0.0.1:1", MachineID: "m", Secret: "s"}, rules, agent.NewPendingForTest(), noPause, log.New(logs, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		c.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-ran
	})
	d := &agent.HubDecider{Client: c, Grace: 300 * time.Millisecond, Log: log.New(logs, "", 0)}
	start := time.Now()
	out, err := d.Decide(context.Background(), pendingFor("claude", "r1"))
	if err != nil || !isUnreachable(out) {
		t.Fatalf("连不上应交回终端: %+v %v", out, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("应在 Grace 后很快返回")
	}
	// 等连接受 ctx 约束：钩子先走了就返回 ctx 的错误，不当作连不上
	d.Grace = 20 * time.Second
	dctx, dcancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer dcancel()
	if _, err := d.Decide(dctx, pendingFor("claude", "r2")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等连接时 ctx 到期应返回 ctx 的错误: %v", err)
	}
}

func TestHubDeciderWaitsForUserDecision(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c, pending := env.c, env.pending
	waitConnected(t, c)
	d := &agent.HubDecider{Client: c, Grace: time.Second, Log: log.New(env.logs, "decider ", 0)}
	pr := pendingFor("claude", "r1")
	pending.Add(&pr)
	done := decideAsync(context.Background(), d, pr)
	eventually(t, 5*time.Second, "hub 没有登记请求", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: "r1", Action: "allow", Note: "手机批准"}); err != nil {
		t.Fatal(err)
	}
	r := waitDecide(t, done, 5*time.Second, "Decide 没有返回")
	// 用户决定 C：钩子拿固定文案，手机上的备注只进 Note（审计）
	if r.err != nil || r.out.Action != "allow" || r.out.Reason != "已在手机上批准" || r.out.Note != "手机批准" || r.out.DecidedBy != "user" {
		t.Fatalf("结论 = %+v %v", r.out, r.err)
	}
	eventually(t, 5*time.Second, "应报告送达", func() bool {
		r, err := h.st.GetRequest("r1")
		return err == nil && r.DeliveredAt != nil
	})
}

// 终审 M3：hub 自己结束了请求（被重发顶掉、按 hub 的时钟判过期）时通知守护进程，等待中的 Decide 立即交回终端，
// 不必挂满本机期限（这里给 Decide 的期限是 1 分钟，Grace 1 秒，连接一直在）。
func TestHubDeciderReleasedWhenHubEndsRequest(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c, pending := env.c, env.pending
	waitConnected(t, c)
	d := &agent.HubDecider{Client: c, Grace: time.Second, Log: log.New(env.logs, "decider ", 0)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// 同一回合同一命令重发：旧的 r1 被 hub 标 cancelled/resend
	pr1, pr2 := pendingFor("claude", "r1"), pendingFor("claude", "r2")
	pending.Add(&pr1)
	done1 := decideAsync(ctx, d, pr1)
	waitHubState(t, h, "r1", "pending", "")
	pending.Add(&pr2)
	done2 := decideAsync(ctx, d, pr2)
	waitHubState(t, h, "r1", "cancelled", store.DecidedByResend)
	if r := waitDecide(t, done1, 5*time.Second, "被重发顶掉的请求应立即交回终端"); r.err != nil || !isUnreachable(r.out) {
		t.Fatalf("r1 结论 = %+v %v", r.out, r.err)
	}
	// 守护进程的时钟比 hub 慢两小时：hub 按自己的时间判过期
	pr3 := pendingFor("claude", "r3")
	pr3.Req.Session.TurnID, pr3.Redacted.Command = "t3", "git push r3"
	pr3.Created = time.Now().Add(-2 * time.Hour)
	pr3.Deadline = pr3.Created.Add(time.Minute)
	pending.Add(&pr3)
	done3 := decideAsync(ctx, d, pr3)
	waitHubState(t, h, "r3", "pending", "")
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: "r3", Action: "allow"}); !errors.Is(err, store.ErrNotPending) {
		t.Fatalf("r3 应按 hub 的时钟判过期: %v", err)
	}
	if r := waitDecide(t, done3, 5*time.Second, "hub 判过期的请求应立即交回终端"); r.err != nil || !isUnreachable(r.out) {
		t.Fatalf("r3 结论 = %+v %v", r.out, r.err)
	}
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: "r2", Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	if r := waitDecide(t, done2, 5*time.Second, "r2 应拿到决定"); r.err != nil || r.out.Action != "allow" {
		t.Fatalf("r2 结论 = %+v %v", r.out, r.err)
	}
}

func TestHubDeciderCancelAndExpire(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c, pending := env.c, env.pending
	waitConnected(t, c)
	d := &agent.HubDecider{Client: c, Grace: time.Second, Log: log.New(env.logs, "decider ", 0)}
	// 取消：模拟钩子消失
	pr := pendingFor("claude", "r1")
	pending.Add(&pr)
	ctx, cancel := context.WithCancelCause(context.Background())
	done := decideAsync(ctx, d, pr)
	waitHubState(t, h, "r1", "pending", "")
	cancel(agent.ErrHookGoneForTest())
	if r := waitDecide(t, done, 5*time.Second, "取消后 Decide 没有返回"); r.err == nil {
		t.Fatal("取消应返回错误")
	}
	waitHubState(t, h, "r1", "cancelled", "terminal")
	// 过期
	pr2 := pendingFor("claude", "r2")
	pending.Add(&pr2)
	dctx, dcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer dcancel()
	if _, err := d.Decide(dctx, pr2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("过期应返回 ctx 的错误: %v", err)
	}
	waitHubState(t, h, "r2", "expired", "")
	// 守护进程停止（取消原因不是钩子消失）
	pr3 := pendingFor("claude", "r3")
	pending.Add(&pr3)
	sctx, scancel := context.WithCancel(context.Background())
	done = decideAsync(sctx, d, pr3)
	waitHubState(t, h, "r3", "pending", "")
	scancel()
	if r := waitDecide(t, done, 5*time.Second, "停止后 Decide 没有返回"); !errors.Is(r.err, context.Canceled) {
		t.Fatalf("停止应返回 ctx 的错误: %v", r.err)
	}
	waitHubState(t, h, "r3", "cancelled", "shutdown")
}

// 预检 12-10、决定 3 与 10：hub 以 401 停用本机后立即交回终端，不等 Grace——等待中被停用、停用后再来的请求都一样。
func TestHubDeciderDisabledFallsBackAtOnce(t *testing.T) {
	agent.SetHubBackoffForTest(t, 50*time.Millisecond) // 连续 3 次 401 才停用，默认退避下要 1+2+4 秒
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	c := env.c
	waitConnected(t, c)
	d := &agent.HubDecider{Client: c, Grace: time.Minute, Log: log.New(env.logs, "decider ", 0)}
	pr := pendingFor("claude", "r1")
	env.pending.Add(&pr)
	done := decideAsync(context.Background(), d, pr)
	waitHubState(t, h, "r1", "pending", "")
	if err := h.st.RevokeMachine(m.MachineID); err != nil {
		t.Fatal(err)
	}
	h.srv.DisconnectForTest(m.MachineID) // 重连时被 401 拒绝
	r := waitDecide(t, done, 15*time.Second, "停用后应立即交回终端，不等 Grace")
	if r.err != nil || !isUnreachable(r.out) {
		t.Fatalf("停用后应交回终端: %+v %v", r.out, r.err)
	}
	if !c.Disabled() {
		t.Fatal("客户端应已停用")
	}
	start := time.Now()
	out, err := d.Decide(context.Background(), pendingFor("claude", "r2"))
	if err != nil || !isUnreachable(out) {
		t.Fatalf("已停用时应交回终端: %+v %v", out, err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("已停用时应立即交回终端，不等 Grace")
	}
}

// 预检 8-5 与决定 2、4：hub 按 id 拒收（HubClient 合成 Action 为空、DecidedBy hub_error 的决定）不是有效决定，
// 立即交回终端；hub 给的原因只进 Note，不进给钩子的 Reason。
func TestHubDeciderHubRejectFallsBack(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	waitConnected(t, env.c)
	d := &agent.HubDecider{Client: env.c, Grace: time.Minute, Log: log.New(env.logs, "decider ", 0)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := d.Decide(ctx, pendingFor("claude", "bad id")) // id 不合规，hub 回“登记失败”
	if err != nil || !isUnreachable(out) {
		t.Fatalf("hub 拒收应立即交回终端: %+v %v", out, err)
	}
	if out.Note != "登记失败" || strings.Contains(out.Reason, "登记失败") {
		t.Fatalf("hub 的原因只进 Note: %+v", out)
	}
}

// 决定 7：SendPending 的非断线错误（编码后超过单帧上限）立即交回终端，不进断线宽限。
func TestHubDeciderOversizeFallsBack(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m, nil)
	waitConnected(t, env.c)
	d := &agent.HubDecider{Client: env.c, Grace: time.Minute, Log: log.New(env.logs, "decider ", 0)}
	pr := pendingFor("claude", "big1")
	pr.Redacted.DiffPreview = strings.Repeat("x", 2<<20)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := d.Decide(ctx, pr)
	if err != nil || !isUnreachable(out) {
		t.Fatalf("送不出去应立即交回终端: %+v %v", out, err)
	}
	if _, err := h.st.GetRequest("big1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("hub 不应有这条请求: %v", err)
	}
}

// 以下是钩子路径的联调：接上 HubDecider 的守护进程经 Unix socket 收请求，hub 客户端连真实 hub。

// startStack 组装并启动守护进程（HubDecider 用给定的 Grace），等 hub 客户端连上。
func startStack(t *testing.T, h *hubEnv, m agent.MachineConfig, grace time.Duration) clientEnv {
	t.Helper()
	env := newClient(t, h, m, func(env clientEnv) {
		env.srv.SetDecider(&agent.HubDecider{Client: env.c, Grace: grace, Log: log.New(env.logs, "decider ", 0)})
		if err := env.srv.Listen(); err != nil {
			t.Fatal(err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() {
		defer close(served)
		env.srv.Serve(ctx)
	}()
	// 后登记先执行：先停服务，newClient 的清理再停 hub 客户端、关审计
	t.Cleanup(func() {
		cancel()
		env.srv.Close()
		<-served
	})
	waitConnected(t, env.c)
	return env
}

type hookResult struct {
	line string
	resp proto.Response
	err  error
}

// askAsync 像钩子一样经 socket 发一条要人决定的请求（curl 是 network 风险，内置规则答 ask），在后台等答复。
func askAsync(sock, cwd string) <-chan hookResult {
	res := make(chan hookResult, 1)
	go func() {
		conn, err := net.DialTimeout("unix", sock, 2*time.Second)
		if err != nil {
			res <- hookResult{err: err}
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		req := proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1", TurnID: "t1", Cwd: cwd},
			ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "curl https://x.y", Cwd: cwd}, DeadlineSeconds: 600}
		if err := proto.WriteLine(conn, req); err != nil {
			res <- hookResult{err: err}
			return
		}
		line, err := proto.ReadLine(bufio.NewReader(conn))
		if err != nil {
			res <- hookResult{err: err}
			return
		}
		var resp proto.Response
		err = json.Unmarshal(line, &resp)
		res <- hookResult{line: string(line), resp: resp, err: err}
	}()
	return res
}

func waitHook(t *testing.T, res <-chan hookResult) hookResult {
	t.Helper()
	select {
	case r := <-res:
		if r.err != nil {
			t.Fatalf("钩子: %v", r.err)
		}
		return r
	case <-time.After(15 * time.Second):
		t.Fatal("钩子没有收到答复")
	}
	return hookResult{}
}

// hubPendingID 等 hub 登记这台机器的一条待决请求，返回它的 id。
func hubPendingID(t *testing.T, h *hubEnv, machineID string) string {
	t.Helper()
	var id string
	eventually(t, 5*time.Second, "hub 没有登记请求", func() bool {
		rs, err := h.st.ListPendingForMachine(machineID)
		if err != nil || len(rs) != 1 {
			return false
		}
		id = rs[0].ID
		return true
	})
	return id
}

// localEntries 读本机审计里这条请求的 permission 行。
func localEntries(t *testing.T, path, id string) []agent.AuditEntry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []agent.AuditEntry
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e agent.AuditEntry
		if l == "" || json.Unmarshal([]byte(l), &e) != nil {
			continue
		}
		if e.Kind == "permission" && e.RequestID == id {
			out = append(out, e)
		}
	}
	return out
}

// 用户决定 C：手机上的备注不交给钩子（答复是固定文案），只进本机审计；hub 记送达。
func TestHubDeciderNoteOnlyInAudit(t *testing.T) {
	const note = "别推 main，先开 PR"
	h := startHub(t)
	m := enroll(t, h)
	env := startStack(t, h, m, 10*time.Second)
	res := askAsync(env.cfg.SocketPath, env.cfg.Dir)
	id := hubPendingID(t, h, m.MachineID)
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: id, Action: "deny", Note: note}); err != nil {
		t.Fatal(err)
	}
	r := waitHook(t, res)
	if r.resp.Decision != "deny" || r.resp.Reason != "已在手机上拒绝" || r.resp.RequestID != id {
		t.Fatalf("答复: %s", r.line)
	}
	if strings.Contains(r.line, "先开 PR") {
		t.Fatalf("钩子不应收到备注: %s", r.line)
	}
	es := localEntries(t, env.cfg.AuditPath, id)
	if len(es) != 1 || es[0].State != "denied" || es[0].Action != "deny" || es[0].DecidedBy != "user" || es[0].Note != note || es[0].Reason != "已在手机上拒绝" {
		t.Fatalf("本机审计应记 denied/user 并带备注: %+v", es)
	}
	waitHubState(t, h, id, "denied", "user")
	eventually(t, 5*time.Second, "hub 应记送达", func() bool {
		r, err := h.st.GetRequest(id)
		return err == nil && r.DeliveredAt != nil
	})
}

// 用户决定 A：请求送到 hub 后断线，Grace 内重连；hub 在断线期间做的决定随重连对齐补发，钩子照常拿到。
func TestHubDeciderReconnectWithinGrace(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := startStack(t, h, m, 10*time.Second) // 客户端重连退避第一档 1 秒，远在 Grace 之内
	res := askAsync(env.cfg.SocketPath, env.cfg.Dir)
	id := hubPendingID(t, h, m.MachineID)
	_, changed := env.c.ConnState()
	h.srv.DisconnectForTest(m.MachineID)
	select {
	case <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("客户端没有发现断线")
	}
	// hub 此刻发不出决定，只能等重连对齐补发
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: id, Action: "allow", Note: "批"}); err != nil {
		t.Fatal(err)
	}
	r := waitHook(t, res)
	if r.resp.Decision != "allow" || r.resp.Reason != "已在手机上批准" {
		t.Fatalf("重连后钩子应拿到决定: %s", r.line)
	}
	es := localEntries(t, env.cfg.AuditPath, id)
	if len(es) != 1 || es[0].State != "approved" || es[0].DecidedBy != "user" || es[0].Note != "批" {
		t.Fatalf("本机审计应记 approved/user: %+v", es)
	}
	eventually(t, 5*time.Second, "hub 应记送达", func() bool {
		r, err := h.st.GetRequest(id)
		return err == nil && r.State == "approved" && r.DeliveredAt != nil
	})
}

// 用户决定 A：断线超过 Grace 仍未重连 → 交回终端；本机审计 expired/hub_unreachable，重连后上报，hub 以本机终态覆盖。
func TestHubDeciderGraceExpires(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := startStack(t, h, m, 300*time.Millisecond) // 客户端重连退避第一档 1 秒，300 毫秒内连不回来
	res := askAsync(env.cfg.SocketPath, env.cfg.Dir)
	id := hubPendingID(t, h, m.MachineID)
	h.srv.DisconnectForTest(m.MachineID)
	r := waitHook(t, res)
	if r.resp.Decision != "passthrough" || r.resp.Reason == "" {
		t.Fatalf("断线超过宽限应交回终端: %s", r.line)
	}
	es := localEntries(t, env.cfg.AuditPath, id)
	if len(es) != 1 || es[0].State != "expired" || es[0].Action != "passthrough" || es[0].DecidedBy != "hub_unreachable" {
		t.Fatalf("本机审计应记 expired/hub_unreachable: %+v", es)
	}
	// 重连后 hello 不再列这条：hub 先按 agent_restart 取消，本机审计到达后以本机终态覆盖
	waitHubState(t, h, id, "expired", "hub_unreachable")
}
