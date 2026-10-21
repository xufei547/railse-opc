// railse 是 Railse OPC 的单二进制入口。
package main

import (
	"fmt"
	"io"
	"os"
)

// version 由构建时 -ldflags "-X main.version=…" 注入。
var version = "dev"

const usage = `用法: railse <子命令> [参数]

子命令:
  version   打印版本
  policy    策略引擎工具（eval、check）`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 分发子命令，返回进程退出码。拆成函数是为了可测。
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, "railse "+version)
		return 0
	default:
		fmt.Fprintf(stderr, "未知子命令 %q\n\n%s\n", args[0], usage)
		return 2
	}
}
