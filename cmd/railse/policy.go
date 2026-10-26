package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xufei547/railse-opc/internal/policy"
)

const policyUsage = `用法:
  railse policy eval [--rules 文件] [--cwd 目录] [--project 目录] [--type shell|file_write|mcp|network] <命令|路径|服务/工具|URL>
  railse policy check <规则文件>`

func runPolicy(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, policyUsage)
		return 2
	}
	switch args[0] {
	case "eval":
		return runPolicyEval(args[1:], stdout, stderr)
	case "check":
		return runPolicyCheck(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知的 policy 子命令 %q\n\n%s\n", args[0], policyUsage)
		return 2
	}
}

func runPolicyEval(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("policy eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rulesPath := fs.String("rules", "", "规则 YAML 文件，不给则用内置规则包")
	cwd := fs.String("cwd", ".", "命令的工作目录")
	project := fs.String("project", "", "项目根目录，不给则用 cwd")
	payloadType := fs.String("type", "shell", "载荷类型")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, policyUsage)
		return 2
	}
	// 用法错误先于读任何文件报出
	p := policy.Payload{Type: policy.PayloadType(*payloadType)}
	switch p.Type {
	case policy.PayloadShell:
		p.Command = fs.Arg(0)
	case policy.PayloadFileWrite:
		p.Path = fs.Arg(0)
	case policy.PayloadNetwork:
		p.URL = fs.Arg(0)
	case policy.PayloadMCP:
		var ok bool
		if p.Server, p.Tool, ok = strings.Cut(fs.Arg(0), "/"); !ok || p.Server == "" || p.Tool == "" {
			fmt.Fprintf(stderr, "mcp 的参数应为 服务/工具，得到 %q\n\n%s\n", fs.Arg(0), policyUsage)
			return 2
		}
	default:
		fmt.Fprintf(stderr, "未知类型 %q\n\n%s\n", *payloadType, policyUsage)
		return 2
	}
	absCwd, err := filepath.Abs(*cwd)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	absProject := absCwd
	if *project != "" {
		if absProject, err = filepath.Abs(*project); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	home, _ := os.UserHomeDir()

	rs := policy.DefaultRuleSet()
	if *rulesPath != "" {
		data, err := os.ReadFile(*rulesPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if rs, err = policy.ParseRuleSet(data); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	eng, err := policy.NewEngine(rs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	p.Cwd = absCwd
	d := eng.Evaluate(p, policy.Context{ProjectRoot: absProject, HomeDir: home, MachineID: "local"})
	fmt.Fprintf(stdout, "%s\t%s\t%s\n", d.Action, d.Risk, d.Reason)
	return 0
}

func runPolicyCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "用法: railse policy check <规则文件>")
		return 2
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(stderr, "无效:", err)
		return 1
	}
	rs, err := policy.ParseRuleSet(data)
	if err != nil {
		fmt.Fprintln(stderr, "无效:", err)
		return 1
	}
	fmt.Fprintf(stdout, "有效，%d 条规则\n", len(rs.Rules))
	return 0
}
