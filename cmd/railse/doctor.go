package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/railse/railse-opc/internal/doctor"
)

const doctorUsage = `用法:
  railse doctor [--project 目录] [--dir 目录] [--hub-dir 目录] [--bin 路径] [--json]
    只读检查：守护进程、hub 连接、三个 CLI 的钩子与版本、文件权限；这台机器有 hub 数据目录时还检查数据库、密码与推送密钥。
    有任一项失败退出码 1。`

// doctorOptions 在 doctor.Run 之前最后改一次检查的输入。是变量只为测试能把 LookPath、RunCmd 换成假的，
// 不在开发机上真的跑 claude、codex、gemini 的 --version（经 runDoctor 体检的 setup all 也走这里）。
var doctorOptions = func(*doctor.Options) {}

// runDoctor 是 railse doctor：只读检查，不建目录与文件。
func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, doctorUsage)
		fs.PrintDefaults()
	}
	project := fs.String("project", ".", "项目目录（检查项目级钩子）")
	dir := fs.String("dir", "", "守护进程数据目录，默认 $RAILSE_HOME 或 ~/.railse")
	hubDir := fs.String("hub-dir", "", "hub 数据目录，默认 $RAILSE_HUB_HOME 或 ~/.railse-hub（默认目录不存在则跳过 hub 侧）")
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
	// 默认的 hub 目录不存在就是这台机器不跑 hub，跳过 hub 侧；显式给的 --hub-dir 读不到多半是路径写错了，要报出来
	hd := *hubDir
	var hubErr error
	if hd != "" {
		if _, err := os.Stat(hd); err != nil {
			hubErr = err
		}
	} else if d, err := defaultHubDir(); err == nil {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			hd = d
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
	o := doctor.Options{AgentDir: cfg.Dir, Project: proj, HubDir: hd, Bin: b, Version: version, Home: home}
	doctorOptions(&o)
	rep := doctor.Run(o)
	if hubErr != nil {
		markHubDirMissing(&rep, hd, hubErr)
	}
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

// markHubDirMissing 把显式 --hub-dir 读不到的情况报成 fail：doctor 对读不到的 hub 目录只会把 hub 侧整个跳过。
func markHubDirMissing(rep *doctor.Report, dir string, err error) {
	c := doctor.Check{ID: "hubsrv.dir", Name: "hub 数据目录", Status: doctor.Fail}
	if errors.Is(err, os.ErrNotExist) {
		c.Detail = "--hub-dir 指定的 " + dir + " 不存在"
		c.Hint = "确认路径写对了（railse hub run --dir 用的那个目录）；这台机器不跑 hub 就去掉 --hub-dir"
	} else {
		why := err
		var pe *os.PathError
		if errors.As(err, &pe) { // 不重复路径
			why = pe.Err
		}
		c.Detail = "--hub-dir 指定的 " + dir + " 读不到：" + why.Error()
		c.Hint = "确认用跑 hub 的同一个用户运行 doctor"
	}
	for i := range rep.Checks {
		if rep.Checks[i].ID == c.ID {
			c.Name = rep.Checks[i].Name
			rep.Checks[i] = c
			return
		}
	}
	rep.Checks = append(rep.Checks, c)
}
