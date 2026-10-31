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

	"github.com/xufei547/railse-opc/internal/adapter"
	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
	"github.com/xufei547/railse-opc/internal/i18n"
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
// 这里绝不打印环境变量，也只读界面语言用的几个（RAILSE_LANG、LC_ALL、LC_MESSAGES、LANG，见 internal/i18n）：CLI 会往钩子环境里注入令牌。
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
func hookPermission(ctx context.Context, ad adapter.Adapter, client hookclient.Client, in adapter.Input, stdout, stderr io.Writer) int {
	resp, err := awaitReply(ctx, ad, client, proto.Request{Kind: "permission", Session: in.Session, ToolName: in.ToolName, Payload: in.Payload})
	if err != nil {
		return passthrough(stderr, err.Error())
	}
	return writeOutput(stdout, ad.Render(adapter.Decision{Action: resp.Decision, Reason: resp.Reason}))
}

// localizeReason 把守护进程回传的拒绝原因翻成终端语言：守护进程（launchd、systemd 下）未必知道用户的语言，钩子知道，
// 所以协议不变、由钩子在本地重拼。命中规则的按规则号重拼（规则号本身不翻）；其余是守护进程里的固定句子，按英文表查
// （i18n.Lookup，句子都手工列在 en.go），查不到原样返回。只有拒绝原因会显示（passthrough 的原因不显示），其余照样翻、无害。
func localizeReason(resp proto.Response) string {
	if resp.RuleID != "" && strings.HasPrefix(resp.Reason, "命中规则 ") {
		return fmt.Sprintf(i18n.T("命中规则 %s"), resp.RuleID)
	}
	return i18n.Lookup(resp.Reason)
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
