package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/railse/railse-opc/internal/adapter"
	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hookclient"
	"github.com/railse/railse-opc/internal/proto"
)

// stdinReader 让测试能替换 stdin。
var stdinReader io.Reader = os.Stdin

const hookUsage = `用法:
  railse hook claude|codex|gemini [--socket 路径]      读 stdin 的钩子 JSON
  railse hook codex-notify [--socket 路径] <json>      Codex notify 的最后一个参数`

// maxStdin 是钩子 stdin 的读取上限，超出部分丢弃，截断后的 JSON 解析失败即不作决定。
const maxStdin = 4 << 20

// runHook 永远以“不作决定”收场（stdout 为空、退出 0），除非守护进程明确给了 allow 或 deny。
// 这里绝不读取或打印环境变量：CLI 会往钩子环境里注入令牌。
func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer) (code int) {
	defer func() {
		if r := recover(); r != nil {
			code = passthrough(stderr, fmt.Sprintf("内部错误: %v", r))
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
		raw, err := io.ReadAll(io.LimitReader(stdin, maxStdin))
		if err != nil {
			return passthrough(stderr, "读 stdin: "+err.Error())
		}
		in, err = ad.Parse(raw)
		if err != nil {
			return passthrough(stderr, err.Error())
		}
	}

	switch in.Kind {
	case adapter.KindPermission:
		if ad == nil {
			return passthrough(stderr, "审批请求没有对应的适配器")
		}
		return hookPermission(ad, client, in, stdout, stderr)
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

// hookPermission 把审批请求交给守护进程，最多等 DeadlineSeconds+10 秒；
// 收到 SIGTERM、SIGINT 或 SIGHUP 时放弃等待并关掉连接，不作决定。
func hookPermission(ad adapter.Adapter, client hookclient.Client, in adapter.Input, stdout, stderr io.Writer) int {
	ctx, cancelSig := context.WithCancelCause(context.Background())
	defer cancelSig(nil)
	deadline := time.Duration(ad.DeadlineSeconds()) * time.Second
	ctx, cancel := context.WithTimeout(ctx, deadline+10*time.Second)
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs) // 先于 cancel 执行；cancel 后下面的 goroutine 随 ctx 结束退出
	go func() {
		select {
		case sig := <-sigs:
			cancelSig(fmt.Errorf("收到信号 %v", sig))
		case <-ctx.Done():
		}
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
	return out.ExitCode
}

// passthrough 在 stderr 说明原因，stdout 不写，退出 0。
func passthrough(stderr io.Writer, why string) int {
	fmt.Fprintln(stderr, "railse hook: "+why+"，交回终端处理")
	return 0
}
