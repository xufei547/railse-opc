package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/xufei547/railse-opc/internal/doctor"
)

const doctorUsage = `用法:
  railse doctor [--project 目录] [--dir 目录] [--hub-dir 目录] [--bin 路径] [--json]
    检查守护进程、hub 连接、三个 CLI 的钩子与版本、文件权限；这台机器有 hub 数据目录时还检查数据库、密码与推送密钥。
    只读：不改配置、不建文件；唯一会起的进程是 claude、codex、gemini 的 --version（带超时，sudo 下不跑）。
    hub 数据目录默认 $RAILSE_HUB_HOME，否则 ~/.railse-hub，Linux 上两者都没有时再看系统单元的 ` + systemHubDir + `
    （只在它归当前用户或以 root 运行时）；默认目录都不在就跳过 hub 侧，显式给的 --hub-dir 不在则报失败。
    有任一项失败退出码 1。`

// doctorOptions 在 doctor.Run 之前最后改一次检查的输入。是变量只为测试能把 LookPath、RunCmd 换成假的，
// 不在开发机上真的跑 claude、codex、gemini 的 --version（经 runDoctor 体检的 setup all 也走这里）。
var doctorOptions = func(*doctor.Options) {}

// doctorGOOS 与 doctorSystemHubDir 决定不给 --hub-dir 时要不要回退到系统单元的 hub 数据目录（空目录表示不回退）；
// doctorEuid 与 doctorDirOwner 用来看那个目录归不归当前用户（查不到属主时 ok 为 false）。
// 是变量只为测试能模拟 Linux 与别的属主，且不看本机真实的系统目录。
var (
	doctorGOOS         = runtime.GOOS
	doctorSystemHubDir = systemHubDir
	doctorEuid         = os.Geteuid
	doctorDirOwner     = func(path string) (int, bool) {
		st, err := os.Stat(path)
		if err != nil {
			return 0, false
		}
		return fileOwner(st)
	}
)

// runDoctor 是 railse doctor：只读检查（不改配置、不建文件），会跑三个 CLI 的 --version（sudo 下不跑）。
func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, doctorUsage)
		fs.PrintDefaults()
	}
	project := fs.String("project", ".", "项目目录（检查项目级钩子）")
	dir := fs.String("dir", "", "守护进程数据目录，默认 $RAILSE_HOME 或 ~/.railse")
	hubDir := fs.String("hub-dir", "", "hub 数据目录，默认 $RAILSE_HUB_HOME 或 ~/.railse-hub，Linux 上再看 "+systemHubDir+"（默认目录都不在则跳过 hub 侧）")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "多余的参数 %q\n%s\n", fs.Args(), doctorUsage)
		return 2
	}
	cfg, err := agentConfigFor(*dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// 细节与修复提示里都用绝对路径（--dir、$RAILSE_HOME 可能是相对路径）
	agentDir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// 显式给的 --hub-dir 必须可用（读不到多半是路径写错了，报 fail）；默认目录不在就是这台机器不跑 hub，跳过 hub 侧
	hd, hubRequired, hubSkip := *hubDir, *hubDir != "", ""
	if !hubRequired {
		hd, hubSkip = doctorHubDir()
	}
	if hd != "" {
		if hd, err = filepath.Abs(hd); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	// 与 setup hooks 的默认一致，钩子里的程序路径才对得上
	b := *bin
	if b == "" {
		if exe, err := os.Executable(); err == nil {
			b, _ = filepath.Abs(exe)
		}
	}
	proj, err := filepath.Abs(*project)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	home, _ := os.UserHomeDir()
	o := doctor.Options{AgentDir: agentDir, Project: proj, HubDir: hd, HubDirRequired: hubRequired, HubSkip: hubSkip, Bin: b, Version: version, Home: home}
	doctorOptions(&o)
	rep := doctor.Run(o)
	if *asJSON {
		j, err := rep.JSON()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, string(j))
	} else {
		rep.Write(stdout)
	}
	if rep.Failed() {
		return 1
	}
	return 0
}

// doctorHubDir 是不给 --hub-dir 时 doctor 看的 hub 数据目录：设了 $RAILSE_HUB_HOME 就只看它；否则 ~/.railse-hub，
// Linux 上它不在时再看系统单元的数据目录（以 root 跑 railse setup autostart --hub --system 装的 hub，RAILSE_HUB_HOME
// 只写在单元的 Environment= 里，doctor 看不到）。系统目录只在归当前用户（就是服务用户）或以 root 跑时才看：别人的
// 目录读不到 hub.db，与其报一串读不到，不如跳过 hub 侧并说清楚（skip 非空、dir 为空）。Linux 上两处都没有时也跳过，
// skip 里列出查过的位置；别的系统上 ~/.railse-hub 不在时仍返回它，报告里写明看的是哪里。连家目录都取不到又没有系统目录
// 时两个都为空。是不是目录由 doctor 判断（不是目录就跳过 hub 侧）。
func doctorHubDir() (dir, skip string) {
	if d := os.Getenv("RAILSE_HUB_HOME"); d != "" {
		return d, ""
	}
	isDir := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && st.IsDir()
	}
	user, err := defaultHubDir()
	if err == nil && isDir(user) {
		return user, ""
	}
	if sys := doctorSystemHubDir; doctorGOOS == "linux" && sys != "" {
		if isDir(sys) {
			if owner, ok := doctorDirOwner(sys); ok && (owner == doctorEuid() || doctorEuid() == 0) {
				return sys, ""
			}
			return "", "系统 hub 目录 " + sys + " 属于别的用户；要检查请用那个用户运行 doctor 或给 --hub-dir"
		}
		if err != nil {
			return "", "没有 hub 数据目录（查过 " + sys + "）"
		}
		return "", "没有 hub 数据目录（查过 " + user + " 与 " + sys + "）"
	}
	if err != nil {
		return "", ""
	}
	return user, ""
}
