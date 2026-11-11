package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xufei547/railse-opc/internal/adapter"
	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
	"github.com/xufei547/railse-opc/internal/i18n"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// stdinReader 让测试能替换 stdin。
var stdinReader io.Reader = os.Stdin

const hookUsage = `用法:
  railse hook claude|codex|gemini [--socket 路径]      读 stdin 的钩子 JSON
  railse hook codex-notify [--socket 路径] <json>      Codex notify 的最后一个参数`

// maxStdin 是钩子 stdin 的读取上限，超出部分丢弃，截断后的 JSON 解析失败即不作决定。
const maxStdin = 4 << 20

// parentCheckInterval 是等审批答复期间检查父进程的间隔；getppid 取父进程号。两者都让测试能替换。
var (
	parentCheckInterval = 5 * time.Second
	getppid             = os.Getppid
)

// errParentGone 是 CLI 已退出（钩子被过继）时取消等待的原因。包级变量不能在初始化时翻译（那时语言还没定），
// 所以是在 Error() 时才翻译的小类型；errors.Is 照常可用。
var errParentGone error = parentGoneError{}

type parentGoneError struct{}

func (parentGoneError) Error() string { return i18n.T("父进程（CLI）已退出") }

// runHook 永远以“不作决定”收场（stdout 为空、退出 0），除非守护进程明确给了 allow、deny 或答题的答案。
// 这里绝不打印任何环境变量，只读：找守护进程 socket 与 lang 文件用的 RAILSE_HOME、HOME，以及界面语言用的 RAILSE_LANG、LC_ALL、LC_MESSAGES、LANG（见 internal/i18n），以及 Claude 的 PreToolUse 请求原样带给守护进程的 RAILSE_TRUST（见 hookPermission）与所有请求、事件都带的 RAILSE_ROLE：CLI 会往钩子环境里注入令牌。
// 从入口起就接管 SIGTERM、SIGINT、SIGHUP：任何阶段收到信号都不作决定并退出 0，进程不会被信号杀掉。
func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			code = passthrough(stderr, fmt.Sprintf(i18n.T("内部错误: %v"), r))
		}
	}()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs) // 先于 cancel 执行；cancel 后下面的 goroutine 随 ctx 结束退出
	go func() {
		select {
		case sig := <-sigs:
			cancel(fmt.Errorf(i18n.T("收到信号 %v"), sig))
		case <-ctx.Done():
		}
	}()
	if len(args) == 0 {
		fmt.Fprintln(stderr, i18n.T(hookUsage))
		return 0
	}
	cli := args[0]
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", i18n.T("守护进程 socket 路径"))
	if err := fs.Parse(args[1:]); err != nil {
		return passthrough(stderr, fmt.Sprintf(i18n.T("参数错误: %v"), err))
	}
	if *socket == "" {
		cfg, err := agent.DefaultConfig()
		if err != nil {
			return passthrough(stderr, err.Error())
		}
		*socket = cfg.SocketPath
	}
	client := hookclient.Client{SocketPath: *socket, ConnectTimeout: 2 * time.Second}

	var in adapter.Input
	var ad adapter.Adapter
	switch cli {
	case "codex-notify":
		// Codex 把 JSON 追加为最后一个参数。
		if fs.NArg() == 0 {
			return passthrough(stderr, i18n.T("codex-notify 需要一个 JSON 参数"))
		}
		var err error
		in, err = adapter.ParseCodexNotify([]byte(fs.Arg(fs.NArg() - 1)))
		if err != nil {
			return passthrough(stderr, err.Error())
		}
	default:
		var ok bool
		ad, ok = adapter.Get(cli)
		if !ok {
			return passthrough(stderr, fmt.Sprintf(i18n.T("不认识的 CLI %s"), cli))
		}
		raw, err := readStdin(ctx, stdin)
		if err != nil {
			return passthrough(stderr, err.Error())
		}
		in, err = ad.Parse(raw)
		if err != nil {
			return passthrough(stderr, err.Error())
		}
	}
	// 会话的角色（0.3 起）：RAILSE_ROLE 原样带给守护进程，由它解析成生效的角色（不存在的名字它按目录绑定）。三家 CLI 的所有事件都带。
	in.Session.Role = os.Getenv("RAILSE_ROLE")
	// 读输入期间收到信号：审批不发、事件也不发。
	if ctx.Err() != nil {
		return passthrough(stderr, context.Cause(ctx).Error())
	}

	switch in.Kind {
	case adapter.KindPermission, adapter.KindAsk:
		if ad == nil {
			return passthrough(stderr, i18n.T("请求没有对应的适配器"))
		}
		if in.Kind == adapter.KindAsk {
			return hookAsk(ctx, ad, client, in, stdout, stderr)
		}
		return hookPermission(ctx, ad, client, in, stdout, stderr)
	case adapter.KindToolDone:
		// 工具跑完了：告诉守护进程清掉同一调用还挂着的审批。每次工具调用都有一条，什么都不输出；
		// 发不出去（守护进程不在、老守护进程不认识）也不说，挂着的审批只是照旧等到过期。
		p := in.Payload
		_ = client.Event(proto.Event{V: proto.Version, Kind: string(in.Kind), Session: in.Session, ToolName: in.ToolName, Payload: &p, ToolUseID: in.ToolUseID})
		return 0
	case adapter.KindQuestion, adapter.KindDone, adapter.KindSessionEnd:
		// 事件只展示：发完就退，连不上也只在 stderr 说一句。
		if err := client.Event(proto.Event{V: proto.Version, Kind: string(in.Kind), Session: in.Session, Message: in.Message}); err != nil {
			fmt.Fprintln(stderr, "railse hook: "+err.Error())
		}
		return 0
	default:
		return 0
	}
}

// readStdin 读钩子输入，最多 maxStdin 字节。ctx 取消（收到信号）时不再等 stdin 关闭，立即返回；
// 留下的读 goroutine 随进程退出而结束。
func readStdin(ctx context.Context, stdin io.Reader) ([]byte, error) {
	type result struct {
		b   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b, err := io.ReadAll(io.LimitReader(stdin, maxStdin))
		ch <- result{b, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, fmt.Errorf(i18n.T("读 stdin: %w"), r.err)
		}
		return r.b, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// hookPermission 把审批请求交给守护进程等答复（见 awaitReply），按答复渲染 allow、deny，其余不作决定。
// Claude 的请求带上来源钩子事件、权限模式与工具调用编号；连不上 hub 而不作决定时在 stderr 说一句，
// 让人知道这次是 Claude 自己在把关。
func hookPermission(ctx context.Context, ad adapter.Adapter, client hookclient.Client, in adapter.Input, stdout, stderr io.Writer) int {
	req := proto.Request{Kind: "permission", Session: in.Session, ToolName: in.ToolName, Payload: in.Payload,
		Source: in.Source, PermissionMode: in.PermissionMode, ToolUseID: in.ToolUseID}
	if in.Source == proto.SourcePreToolUse {
		// RAILSE_TRUST=2h claude：原文照带，格式由守护进程判断（不对只记它的日志）；Claude 每次调用先经 PreToolUse，只带这一路
		req.TrustFor = os.Getenv("RAILSE_TRUST")
	}
	resp, err := awaitReply(ctx, ad, client, req)
	if err != nil {
		return passthrough(stderr, err.Error())
	}
	if (resp.Decision == "passthrough" || resp.Decision == "ask") && resp.DecidedBy == "hub_unreachable" && in.Source != "" {
		fmt.Fprintln(stderr, "railse hook: "+i18n.T("连不上 hub，交回 Claude 自己的确认"))
	}
	reason := resp.Reason
	if resp.Decision == "ask" && in.Source == proto.SourcePreToolUse {
		// 只重拼本版守护进程“交终端确认、同时送手机”的 ask（decided_by 为 ask）；升级后还没重启的老守护进程
		// 回的是“连不上 hub”的 ask，照它的原话翻，不能说已发到手机
		if resp.DecidedBy == "ask" {
			reason = askReason(resp)
		} else {
			reason = localizeReason(resp)
		}
	}
	return writeOutput(stdout, ad.Render(adapter.Decision{Source: in.Source, Action: resp.Decision, Reason: reason}))
}

// askReason 把守护进程回的 ask 拼成 Claude 确认框里给人看的一句话（渲染时加 "Railse：" 前缀）：触发段、性质、已发手机。
// Why 是中文键，按终端语言查英文表（i18n.Lookup，查不到原样）；没有段时性质打头，英文首字母大写。
func askReason(resp proto.Response) string {
	tail := i18n.T("已发到手机，也可以在这里直接批")
	// 越界（角色上限、roles.yaml 有误）的 ask：理由换成角色的那句，比触发点更说明问题。resp.Reason 已由 awaitReply 按终端语言重拼过
	if isRoleReason(resp.Reason) {
		return fmt.Sprintf(i18n.T("%s。%s"), resp.Reason, tail)
	}
	if resp.Trigger == nil {
		return tail
	}
	why := i18n.Lookup(resp.Trigger.Why)
	if resp.Trigger.Segment == "" {
		r, n := utf8.DecodeRuneInString(why)
		return fmt.Sprintf(i18n.T("%s。%s"), string(unicode.ToUpper(r))+why[n:], tail)
	}
	// 中文里多数性质是动作，前面加“会”；联网、不在名单、不可逆三条不是动作，换个说法。英文的性质都是动词短语，两种模板一样。
	switch resp.Trigger.Why {
	case policy.WhyNetwork, policy.WhyUnknown, policy.WhyDestructive:
		if i18n.Lang() == i18n.ZH {
			why = map[string]string{policy.WhyNetwork: "要联网", policy.WhyUnknown: "不在已知名单里", policy.WhyDestructive: "是不可逆的操作"}[resp.Trigger.Why]
		}
		return fmt.Sprintf(i18n.T("命令里的「%s」%s。%s"), resp.Trigger.Segment, why, tail)
	}
	return fmt.Sprintf(i18n.T("命令里的「%s」会%s。%s"), resp.Trigger.Segment, why, tail)
}

// localizeReason 把守护进程回传的拒绝原因翻成终端语言：守护进程（launchd、systemd 下）未必知道用户的语言，钩子知道，
// 所以协议不变、由钩子在本地重拼。命中规则的按规则号重拼（规则号本身不翻）；其余是守护进程里的固定句子，按英文表查
// （i18n.Lookup，句子都手工列在 en.go），查不到原样返回。只有拒绝原因会显示（passthrough 的原因不显示），其余照样翻、无害。
func localizeReason(resp proto.Response) string {
	if resp.RuleID != "" && strings.HasPrefix(resp.Reason, "命中规则 ") {
		return fmt.Sprintf(i18n.T("命中规则 %s"), resp.RuleID)
	}
	if r, ok := localizeRoleReason(resp.Reason); ok {
		return r
	}
	return i18n.Lookup(resp.Reason)
}

// 角色（0.3 起）的三种理由，由守护进程的引擎（internal/policy 的 Evaluate）按这几个格式拼出；那边改了这里要一起改。
const (
	roleForbidPrefix = "角色「"
	roleForbidMid    = "」禁用："
	roleBeyondPrefix = "超出角色「"
	roleBeyondMid    = "」的边界（上限 "
	roleBeyondRisk   = "，这次是 "
	roleBrokenPrefix = "roles.yaml 有误："
)

// roleReasonFormats 是角色理由按终端语言重拼用的三个模板（见 localizeRoleReason）。
var roleReasonFormats = []string{"角色「%s」禁用：%s", "超出角色「%s」的边界（上限 %s，这次是 %s）", "roles.yaml 有误：%s"}

// isRoleReason 判断已按终端语言重拼过的理由是不是角色的理由：以某个模板（当前语言）第一个占位符之前的部分开头。
func isRoleReason(localized string) bool {
	for _, f := range roleReasonFormats {
		prefix, _, _ := strings.Cut(i18n.Lookup(f), "%s")
		if prefix != "" && strings.HasPrefix(localized, prefix) {
			return true
		}
	}
	return false
}

// localizeRoleReason 认出角色的理由并按终端语言重拼：禁用（角色名与禁用条目的简述原样）、越界（角色名与两档风险名原样）、
// roles.yaml 有误（错误原文原样）。不是这三种返回 false。
func localizeRoleReason(reason string) (string, bool) {
	switch {
	case strings.HasPrefix(reason, roleForbidPrefix):
		name, brief, ok := strings.Cut(strings.TrimPrefix(reason, roleForbidPrefix), roleForbidMid)
		if ok {
			return fmt.Sprintf(i18n.T("角色「%s」禁用：%s"), name, brief), true
		}
	case strings.HasPrefix(reason, roleBeyondPrefix):
		name, rest, ok := strings.Cut(strings.TrimPrefix(reason, roleBeyondPrefix), roleBeyondMid)
		if !ok {
			break
		}
		ceiling, rest, ok := strings.Cut(rest, roleBeyondRisk)
		if ok && strings.HasSuffix(rest, "）") {
			return fmt.Sprintf(i18n.T("超出角色「%s」的边界（上限 %s，这次是 %s）"), name, ceiling, strings.TrimSuffix(rest, "）")), true
		}
	case strings.HasPrefix(reason, roleBrokenPrefix):
		return fmt.Sprintf(i18n.T("roles.yaml 有误：%s"), strings.TrimPrefix(reason, roleBrokenPrefix)), true
	}
	return "", false
}

// hookAsk 把答题请求交给守护进程等答案（见 awaitReply）。只有答复是 answer 才输出：答案合进原 tool_input（questions 等字段
// 逐字节原样）写成 PreToolUse 的 allow，Claude Code 就不再在终端弹题。其余答复（没接 hub、hub 不可达、过期、手机上选了在终端回答、
// 不认识 ask 的旧守护进程）一律不输出，Claude Code 在终端照常问；绝不对 PreToolUse 写出审批的输出。
func hookAsk(ctx context.Context, ad adapter.Adapter, client hookclient.Client, in adapter.Input, stdout, stderr io.Writer) int {
	resp, err := awaitReply(ctx, ad, client, proto.Request{Kind: "ask", Session: in.Session, ToolName: in.ToolName, Ask: in.Ask})
	if err != nil {
		return passthrough(stderr, err.Error())
	}
	if resp.Decision != "answer" {
		return 0
	}
	return writeOutput(stdout, ad.Render(adapter.Decision{Action: "answer", Answers: resp.Answers, RawToolInput: in.RawToolInput}))
}

// awaitReply 把请求（填上协议版本与适配器的期限）交给守护进程并等答复，最多等 DeadlineSeconds+10 秒；
// ctx 因信号取消、CLI 已退出或守护进程无响应时放弃等待并关掉连接，返回说明原因的错误，调用方不作决定。
func awaitReply(ctx context.Context, ad adapter.Adapter, client hookclient.Client, req proto.Request) (proto.Response, error) {
	deadline := time.Duration(ad.DeadlineSeconds()) * time.Second
	tctx, cancelTimeout := context.WithTimeout(ctx, deadline+10*time.Second)
	defer cancelTimeout()
	ctx, cancel := context.WithCancelCause(tctx)
	parentDone := make(chan struct{})
	go func() {
		defer close(parentDone)
		watchParent(ctx, cancel, getppid, parentCheckInterval)
	}()
	defer func() {
		cancel(nil)
		<-parentDone
	}()
	req.V, req.DeadlineSeconds = proto.Version, ad.DeadlineSeconds()
	resp, err := client.Permission(ctx, req)
	if err != nil {
		return resp, err
	}
	resp.Reason = localizeReason(resp)
	return resp, nil
}

// writeOutput 把适配器的输出（非空时）写成一行。适配器约定只用退出码 0，决定全靠 stdout 表达；这里再兜一层，钩子进程恒以 0 退出。
func writeOutput(stdout io.Writer, out adapter.Output) int {
	if len(out.Stdout) > 0 {
		stdout.Write(append(out.Stdout, '\n'))
	}
	return 0
}

// watchParent 每隔 interval 看一次父进程号，与开始时不同就以 errParentGone 取消 ctx。
// CLI 被 SIGKILL 或崩溃时不会给钩子发信号，钩子会被过继给 init/launchd（父进程号变为 1）或 subreaper，
// 不检查就会一直等到 24 小时期限。开始时父进程已是 1 的无从判断，不检查。ctx 结束时返回。
func watchParent(ctx context.Context, cancel context.CancelCauseFunc, ppid func() int, interval time.Duration) {
	orig := ppid()
	if orig == 1 {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if ppid() != orig {
				cancel(errParentGone)
				return
			}
		}
	}
}

// passthrough 在 stderr 说明原因，stdout 不写，退出 0。why 由调用方翻好。
func passthrough(stderr io.Writer, why string) int {
	fmt.Fprintln(stderr, "railse hook: "+why+i18n.T("，交回终端处理"))
	return 0
}
