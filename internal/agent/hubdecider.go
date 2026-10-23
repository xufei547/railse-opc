package agent

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/railse/railse-opc/internal/hub/wire"
)

// 交给钩子的理由一律是这里的固定文案。hub 送来的自由文本（手机上的备注、hub 拒收的原因）只进 Outcome.Note，
// 只写审计，不交给适配器和模型（用户决定 C）。
const (
	reasonGemini      = "Gemini 只能拒绝不能放行，交回原生确认框"
	reasonNoHub       = "没有配置 hub，交回终端处理"
	reasonUnreachable = "连不上 hub，交回终端处理"
	reasonHubDisabled = "hub 拒绝了本机密钥，交回终端处理"
	reasonSendFailed  = "无法送往 hub，交回终端处理"
	reasonLost        = "与 hub 断线超过宽限，交回终端处理"
	reasonNoDecision  = "hub 没有给出有效决定，交回终端处理"
	reasonAllowed     = "已在手机上批准"
	reasonDenied      = "已在手机上拒绝"
)

// hub 决定里的字段写进 Outcome 前的字符上限（非 allow/deny 时 hub 给的原因截到 hubErrorReasonMax）。
const (
	decidedByMax = 64   // Decision.DecidedBy
	ruleIDMax    = 256  // Decision.RuleID，与 hub 的短字段上限一致
	noteMax      = 1000 // 用户备注，与 hub 的备注上限一致
)

// hubNoticeTimeout 是送达、取消、过期回执的写上限。它们只是通知（写不出去时重连对齐会补上），不值得让钩子按
// hubWriteTimeout 多等。是变量只为测试能调。
var hubNoticeTimeout = 2 * time.Second

// reservedDecidedBy 是本机审计里另有含义的 decided_by（引擎规则 rule:…、暂停、钩子消失、hub 不可达等）。hub 送来的决定
// 若带这些值（不区分大小写），一律记为 user：否则本机审计会把手机上的决定记成别的来源，hub_unreachable 还会让守护进程
// 把真决定当作交回终端。
var reservedDecidedBy = map[string]bool{
	hubUnreachable: true, "gemini": true, "terminal": true, "timeout": true, "shutdown": true, "error": true, "paused": true,
	"no_hub": true, "hub": true, "hub_error": true,
}

// hubDecidedBy 把 hub 送来的 decided_by 截到 decidedByMax 字符；为空或是本机保留值时记为 user。
func hubDecidedBy(s string) string {
	by := clipText(s, decidedByMax)
	if l := strings.ToLower(by); by == "" || reservedDecidedBy[l] || strings.HasPrefix(l, "rule:") {
		return "user"
	}
	return by
}

// HubDecider 把引擎答 ask 的请求经 HubClient 送到 hub，等人在手机上决定。它永不让 agent 卡住：Gemini 直接交回
// 原生确认框；连不上 hub、hub 停用本机、送不出去、hub 没给出有效决定，或送出后断线超过 Grace，都交回终端
// （DecidedBy hub_unreachable，钩子收 passthrough，本机审计记 expired）。
type HubDecider struct {
	Client *HubClient
	Grace  time.Duration // 等连接、断线后等重连的上限，取 Config.HubGrace（设计文档 3.2 的 30 秒）；≤0 表示不等
	Log    *log.Logger
	// notices 记在后台发的送达回执（见 settle），Wait 等它们结束。含 WaitGroup，HubDecider 不得复制。
	notices sync.WaitGroup
}

// Wait 等后台的送达回执发完（每条至多 hubNoticeTimeout）。只在不会再有新决定时调用（守护进程在审批服务停下之后、
// 关日志之前；测试结束时），不留 goroutine。
func (d *HubDecider) Wait() { d.notices.Wait() }

func unreachable(reason, note string) Outcome {
	return Outcome{Action: "passthrough", Reason: reason, DecidedBy: hubUnreachable, Note: note}
}

// Decide 实现 Decider。
func (d *HubDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error) {
	if isGemini(pr) {
		// Gemini 的钩子只能拒绝不能放行：不进 hub 等待，只作审计上报
		return Outcome{Action: "passthrough", Reason: reasonGemini, DecidedBy: "gemini"}, nil
	}
	if d.Client == nil {
		return unreachable(reasonNoHub, ""), nil
	}
	gctx, cancel := context.WithTimeout(ctx, d.Grace)
	err := d.Client.WaitConnected(gctx)
	cancel()
	switch {
	case err == nil:
	case ctx.Err() != nil:
		return Outcome{}, ctx.Err() // 还没送出，hub 不知道这条，不必通知
	case errors.Is(err, ErrHubDisabled):
		return unreachable(reasonHubDisabled, ""), nil
	default:
		return unreachable(reasonUnreachable, ""), nil
	}
	ch := d.Client.Await(pr.ID)
	defer d.Client.Forget(pr.ID)
	if err := d.Client.SendPending(pr); errors.Is(err, ErrHubDisconnected) {
		d.logf("请求 %q 送往 hub 时断线，等重连（宽限 %s）: %v", pr.ID, d.Grace, err)
	} else if err != nil {
		d.logf("请求 %q 交回终端: %v", pr.ID, err)
		return unreachable(reasonSendFailed, ""), nil
	}
	// 报断线（未连接或写连接失败）视同已送出后断线：请求仍在未完成表里，重连后随对齐重发，按宽限等。
	return d.wait(ctx, pr.ID, ch)
}

// wait 等 hub 的决定。断线时起 Grace 计时，期间重连上就停表接着等（重连对齐会补发决定，重发的 pending 也会让
// hub 再发）；到期仍未连上，或客户端被 401 停用，交回终端。
func (d *HubDecider) wait(ctx context.Context, id string, ch <-chan wire.Decision) (Outcome, error) {
	var grace *time.Timer
	defer func() {
		if grace != nil {
			grace.Stop()
		}
	}()
	for {
		// 先取通道再查停用（见 ConnState）：停用也会关闭这个通道
		connected, changed := d.Client.ConnState()
		if d.Client.Disabled() {
			return unreachable(reasonHubDisabled, ""), nil
		}
		switch {
		case connected && grace != nil:
			grace.Stop()
			grace = nil
		case !connected && grace == nil:
			grace = time.NewTimer(d.Grace)
		}
		var expired <-chan time.Time
		if grace != nil {
			expired = grace.C
		}
		select {
		case dec := <-ch:
			return d.settle(id, dec), nil
		case <-ctx.Done():
			d.abandon(ctx, id)
			return Outcome{}, ctx.Err()
		case <-changed:
		case <-expired:
			grace = nil
			if c, _ := d.Client.ConnState(); c {
				continue // 到期的同时重连上了：接着等
			}
			d.logf("与 hub 断线超过 %s，请求 %q 交回终端", d.Grace, id)
			return unreachable(reasonLost, ""), nil
		}
	}
}

// settle 把 hub 的决定换成给钩子的结论，并在是真决定时在后台报送达（最多等 hubNoticeTimeout，报不出去只记日志）：
// 写锁被大审计帧占着时回执可能要等，不能让钩子跟着等（完成标准 4 要求决定后 1 秒内答复）。
// 只认 allow、deny：钩子拿固定文案，用户备注只进 Note，DecidedBy 经 hubDecidedBy 过滤。其余任何动作（含 hub 拒收
// 这条请求时 HubClient 合成的 hub_error 决定、空串）一律交回终端，hub 给的原因只进 Note。
func (d *HubDecider) settle(id string, dec wire.Decision) Outcome {
	var reason string
	switch dec.Action {
	case "allow":
		reason = reasonAllowed
	case "deny":
		reason = reasonDenied
	default:
		d.logf("hub 对请求 %q 的决定无效（action %q，decided_by %q），交回终端",
			id, clipText(dec.Action, hubLogFieldMax), clipText(dec.DecidedBy, hubLogFieldMax))
		return unreachable(reasonNoDecision, clipText(dec.Reason, hubErrorReasonMax))
	}
	out := Outcome{Action: dec.Action, Reason: reason, RuleID: clipText(dec.RuleID, ruleIDMax), DecidedBy: hubDecidedBy(dec.DecidedBy), Note: clipText(dec.Reason, noteMax)}
	d.notices.Add(1)
	go func() {
		defer d.notices.Done()
		nctx, cancel := context.WithTimeout(context.Background(), hubNoticeTimeout)
		defer cancel()
		if err := d.Client.SendDelivered(nctx, id); err != nil {
			d.logf("报告请求 %q 已送达失败: %v", id, err)
		}
	}()
	return out
}

// abandon 在 ctx 结束时告诉 hub：钩子消失发 cancel(terminal)，等待到期发 expired，其余（守护进程停止）发
// cancel(shutdown)，最多等 hubNoticeTimeout。断线或写不出去时只记日志：重连对齐会按 agent_restart 取消，随后本机终态审计覆盖。
func (d *HubDecider) abandon(ctx context.Context, id string) {
	nctx, cancel := context.WithTimeout(context.Background(), hubNoticeTimeout)
	defer cancel()
	var err error
	switch {
	case errors.Is(context.Cause(ctx), errHookGone):
		err = d.Client.SendCancel(nctx, id, "terminal")
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		err = d.Client.SendExpired(nctx, id)
	default:
		err = d.Client.SendCancel(nctx, id, "shutdown")
	}
	if err != nil {
		d.logf("告诉 hub 请求 %q 已结束失败: %v", id, err)
	}
}

func (d *HubDecider) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log.Printf(format, args...)
	}
}
