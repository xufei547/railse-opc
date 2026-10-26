package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xufei547/railse-opc/internal/adapter"
	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
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

// errParentGone 是 CLI 已退出（钩子被过继）时取消等待的原因。
var errParentGone = errors.New("父进程（CLI）已退出")

// runHook 永远以“不作决定”收场（stdout 为空、退出 0），除非守护进程明确给了 allow 或 deny。
// 这里绝不读取或打印环境变量：CLI 会往钩子环境里注入令牌。
// 从入口起就接管 SIGTERM、SIGINT、SIGHUP：任何阶段收到信号都不作决定并退出 0，进程不会被信号杀掉。
func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			code = passthrough(stderr, fmt.Sprintf("内部错误: %v", r))
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
			cancel(fmt.Errorf("收到信号 %v", sig))
		case <-ctx.Done():
		}
	}()
	if len(args) == 0 {
		fmt.Fprintln(stderr, hookUsage)
		return 0
	}
	cli := args[0]
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "守护进程 socket 路径")
	if err := fs.Parse(args[1:]); err != nil {
		return passthrough(stderr, "参数错误: "+err.Error())
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
			return passthrough(stderr, "codex-notify 需要一个 JSON 参数")
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
			return passthrough(stderr, "不认识的 CLI "+cli)
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
	case adapter.KindPermission:
		if ad == nil {
			return passthrough(stderr, "审批请求没有对应的适配器")
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
			return nil, fmt.Errorf("读 stdin: %w", r.err)
		}
		return r.b, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

// hookPermission 把审批请求交给守护进程，最多等 DeadlineSeconds+10 秒；
// ctx 因信号取消、CLI 已退出或守护进程无响应时放弃等待并关掉连接，不作决定。
func hookPermission(ctx context.Context, ad adapter.Adapter, client hookclient.Client, in adapter.Input, stdout, stderr io.Writer) int {
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
	req := proto.Request{V: proto.Version, Kind: "permission", Session: in.Session, ToolName: in.ToolName, Payload: in.Payload, DeadlineSeconds: ad.DeadlineSeconds()}
	resp, err := client.Permission(ctx, req)
	if err != nil {
		return passthrough(stderr, err.Error())
	}
	out := ad.Render(adapter.Decision{Action: resp.Decision, Reason: resp.Reason})
	if len(out.Stdout) > 0 {
		stdout.Write(append(out.Stdout, '\n'))
	}
	// 适配器约定只用退出码 0，决定全靠 stdout 表达；这里再兜一层，钩子进程恒以 0 退出。
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

// passthrough 在 stderr 说明原因，stdout 不写，退出 0。
func passthrough(stderr io.Writer, why string) int {
	fmt.Fprintln(stderr, "railse hook: "+why+"，交回终端处理")
	return 0
}
