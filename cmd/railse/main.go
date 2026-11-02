// railse 是 Railse OPC 的单二进制入口。
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/xufei547/railse-opc/internal/i18n"
)

// version 由构建时 -ldflags "-X main.version=…" 注入。
var version = "dev"

const usage = `用法: railse <子命令> [参数]

子命令:
  version   打印版本
  policy    策略引擎工具（eval、check）
  hook      各 CLI 钩子调用的翻译器（claude、codex、codex-notify、gemini）
  agent     守护进程（run、status、pause、resume、reload、enroll）
  trust     限时放行（当前项目或某个 Claude 会话，默认 2 小时）
  hub       云端服务（run、password、token、webhook-token）
  setup     一键接入与安装（all）、钩子（hooks）、开机自启（autostart）、Codex notify（codex-notify）
  doctor    检查安装与运行状态（只读）
  lang      查看或设定界面语言（zh|en）`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 分发子命令，返回进程退出码。拆成函数是为了可测。
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, i18n.T(usage))
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, i18n.T(usage))
		return 0
	case "version":
		fmt.Fprintln(stdout, "railse "+version)
		return 0
	case "policy":
		return runPolicy(args[1:], stdout, stderr)
	case "hook":
		return runHook(args[1:], stdinReader, stdout, stderr)
	case "agent":
		return runAgent(args[1:], stdout, stderr)
	case "trust":
		return runTrust(args[1:], stdout, stderr)
	case "hub":
		return runHub(args[1:], stdinReader, stdout, stderr)
	case "setup":
		return runSetup(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "lang":
		return runLang(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, i18n.T("未知子命令 %q\n\n%s\n"), args[0], i18n.T(usage))
		return 2
	}
}

// foreignFlags 返回命令行上出现、但不在 allowed 里的标志（形如 --name）。几个子命令共用一个标志集，解析后用它挡住
// 不属于当前子命令的标志，免得被悄悄忽略（例如 railse agent run --hub X 以为接上了 hub）。
func foreignFlags(fs *flag.FlagSet, allowed []string) string {
	var bad []string
	fs.Visit(func(f *flag.Flag) {
		if !slices.Contains(allowed, f.Name) {
			bad = append(bad, "--"+f.Name)
		}
	})
	return strings.Join(bad, i18n.T("、"))
}
