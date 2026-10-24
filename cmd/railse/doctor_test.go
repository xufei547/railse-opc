package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/railse/railse-opc/internal/doctor"
)

// fakeDoctorCLIs 让测试里的 doctor 不碰本机环境：看不到任何 CLI（LookPath 一律找不到，RunCmd 被调到就算测试失败），
// 也不回退到本机的系统 hub 目录。免得在开发机上真的跑 claude、codex、gemini 的 --version（慢，还可能往临时 HOME
// 里建目录），CI 上又是另一种结果。经 runDoctor 体检的测试（如 setup all）都先调它。
func fakeDoctorCLIs(t *testing.T) {
	t.Helper()
	setDoctorHubFallback(t, runtime.GOOS, "")
	setDoctorOptions(t, func(o *doctor.Options) {
		o.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
		o.RunCmd = func(name string, args ...string) (string, error) {
			t.Errorf("测试里的 doctor 不该跑 %s %v", name, args)
			return "", errors.New("测试里不跑 CLI")
		}
	})
}

// setDoctorOptions 在本测试期间换掉 doctorOptions，结束时换回。
func setDoctorOptions(t *testing.T, f func(*doctor.Options)) {
	t.Helper()
	old := doctorOptions
	doctorOptions = f
	t.Cleanup(func() { doctorOptions = old })
}

// setDoctorHubFallback 在本测试期间换掉认系统 hub 目录用的系统名与目录（空目录表示不回退），结束时换回。
func setDoctorHubFallback(t *testing.T, goos, dir string) {
	t.Helper()
	oldGOOS, oldDir := doctorGOOS, doctorSystemHubDir
	doctorGOOS, doctorSystemHubDir = goos, dir
	t.Cleanup(func() { doctorGOOS, doctorSystemHubDir = oldGOOS, oldDir })
}

type doctorCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint"`
}

// doctorJSON 跑 railse doctor --json，返回退出码与按 ID 索引的检查项；输出不是合法 JSON、没有检查项或 ID 重复时测试失败。
func doctorJSON(t *testing.T, args ...string) (int, map[string]doctorCheck) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(append(append([]string{"doctor"}, args...), "--json"), &out, &errOut)
	var rep struct {
		Version string        `json:"version"`
		Checks  []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("JSON 输出解析失败: %v\nstdout=%s\nstderr=%s", err, out.String(), errOut.String())
	}
	if len(rep.Checks) == 0 {
		t.Fatalf("JSON 输出没有检查项: %s", out.String())
	}
	if rep.Version != version {
		t.Fatalf("JSON 的 version = %q，想要 %q", rep.Version, version)
	}
	checks := map[string]doctorCheck{}
	for _, c := range rep.Checks {
		if _, dup := checks[c.ID]; dup {
			t.Fatalf("检查项 %s 出现了两次: %s", c.ID, out.String())
		}
		checks[c.ID] = c
	}
	return code, checks
}

// mustCheck 取一项检查；没有这一项就让测试失败，断言不会空过。
func mustCheck(t *testing.T, checks map[string]doctorCheck, id string) doctorCheck {
	t.Helper()
	c, found := checks[id]
	if !found {
		t.Fatalf("没有检查项 %s: %+v", id, checks)
	}
	return c
}

func TestDoctorRunsAndExitCodes(t *testing.T) {
	fakeDoctorCLIs(t)
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	t.Setenv("HOME", dir)
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(dir, "nohub"))
	agentDir := filepath.Join(dir, "h")
	var out, errOut bytes.Buffer
	// 什么都没有：守护进程没在跑 → fail → 退出 1
	code := run([]string{"doctor", "--dir", agentDir, "--project", dir}, &out, &errOut)
	if code != 1 || !strings.Contains(out.String(), "[fail]") || !strings.Contains(out.String(), "项失败") || !strings.Contains(out.String(), "修复：") {
		t.Fatalf("doctor: %d %q %q", code, out.String(), errOut.String())
	}
	code, checks := doctorJSON(t, "--dir", agentDir, "--project", dir)
	if code != 1 {
		t.Fatalf("json: %d", code)
	}
	if c := mustCheck(t, checks, "agent.running"); c.Status != "fail" {
		t.Fatalf("没起守护进程时 agent.running 应 fail: %+v", c)
	}
	if c := mustCheck(t, checks, "agent.dir"); c.Status != "fail" || !strings.Contains(c.Detail, agentDir) {
		t.Fatalf("--dir 应传给 doctor: %+v", c)
	}
	// 起一个守护进程后 agent.running 变 ok
	_, _, cfg := startTestAgent(t)
	_, checks = doctorJSON(t, "--dir", cfg.Dir, "--project", dir)
	if c := mustCheck(t, checks, "agent.running"); c.Status != "ok" {
		t.Fatalf("守护进程在跑时 agent.running 应 ok: %+v", checks)
	}
	if c := mustCheck(t, checks, "agent.dir"); c.Status != "ok" {
		t.Fatalf("守护进程目录在时 agent.dir 应 ok: %+v", c)
	}
	errOut.Reset()
	if code := run([]string{"doctor", "--bogus"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "railse doctor") {
		t.Fatalf("坏标志: %d %q", code, errOut.String())
	}
	// 用法如实说明会跑三个 CLI 的 --version（sudo 下不跑），以及 Linux 上回退的系统 hub 目录
	for _, want := range []string{"claude、codex、gemini 的 --version", "sudo 下不跑", systemHubDir} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("用法缺 %q: %s", want, errOut.String())
		}
	}
	errOut.Reset()
	if code := run([]string{"doctor", "extra"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "多余的参数") {
		t.Fatalf("多余参数: %d %q", code, errOut.String())
	}
}

// doctor 只读：HOME 不存在、用默认目录时，不建 ~/.railse、~/.railse-hub，也不建别的任何文件。
func TestDoctorCreatesNothing(t *testing.T) {
	fakeDoctorCLIs(t)
	base, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(base)
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	t.Setenv("RAILSE_HOME", "")
	t.Setenv("RAILSE_HUB_HOME", "")
	proj := filepath.Join(base, "proj")
	var out, errOut bytes.Buffer
	if code := run([]string{"doctor", "--project", proj}, &out, &errOut); code != 1 {
		t.Fatalf("doctor: %d %q %q", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), filepath.Join(home, ".railse")+" 不存在") {
		t.Fatalf("应按默认目录 ~/.railse 检查: %s", out.String())
	}
	_, checks := doctorJSON(t, "--project", proj)
	if c := mustCheck(t, checks, "agent.dir"); c.Status != "fail" || !strings.Contains(c.Detail, filepath.Join(home, ".railse")) {
		t.Fatalf("agent.dir: %+v", c)
	}
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "skip" {
		t.Fatalf("默认 hub 目录不存在时 hub 侧应跳过: %+v", c)
	}
	ents, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("doctor 不该建任何东西，却建了 %v", names)
	}
}

func TestDoctorHubDir(t *testing.T) {
	fakeDoctorCLIs(t)
	base, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(base)
	t.Setenv("HOME", base)
	agentDir := filepath.Join(base, "h")
	hubDir := filepath.Join(base, "hub")
	if err := os.Mkdir(hubDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// 默认目录（$RAILSE_HUB_HOME）不存在：hub 侧整个跳过，细节写明看的是哪里
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(base, "nohub"))
	_, checks := doctorJSON(t, "--dir", agentDir, "--project", base)
	for _, id := range []string{"hubsrv.dir", "hubsrv.db", "hubsrv.password", "hubsrv.vapid", "hubsrv.acme", "hubsrv.autostart"} {
		if c := mustCheck(t, checks, id); c.Status != "skip" || !strings.Contains(c.Detail, filepath.Join(base, "nohub")) {
			t.Fatalf("默认 hub 目录不存在时 %s 应 skip: %+v", id, c)
		}
	}

	// 显式给的 --hub-dir 不存在：多半是写错了路径，报 fail 并给提示，不悄悄跳过，也不建它
	missing := filepath.Join(base, "typo")
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", base, "--hub-dir", missing)
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "fail" || !strings.Contains(c.Hint, "--hub-dir") || c.Detail != missing+" 不存在" {
		t.Fatalf("显式 --hub-dir 不存在应 fail: %+v", c)
	}
	for _, id := range []string{"hubsrv.db", "hubsrv.password", "hubsrv.vapid", "hubsrv.acme"} {
		if c := mustCheck(t, checks, id); c.Status != "skip" || c.Detail != "hub 数据目录 "+missing+" 不存在" {
			t.Fatalf("hub 目录不存在时 %s 应按同一原因 skip: %+v", id, c)
		}
	}
	if c := mustCheck(t, checks, "hubsrv.autostart"); c.Status == "skip" {
		t.Fatalf("hub 开机自启不依赖数据目录，应照常检查: %+v", c)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("doctor 不该建 --hub-dir: %v", err)
	}
	var out, errOut bytes.Buffer
	run([]string{"doctor", "--dir", agentDir, "--project", base, "--hub-dir", missing}, &out, &errOut)
	if !strings.Contains(out.String(), "[fail]  hub 数据目录") {
		t.Fatalf("文本输出应把 hub 数据目录报成 fail: %s", out.String())
	}
	// 显式 --hub-dir 读不到（不是不存在）：同样 fail，细节不重复路径
	if os.Geteuid() != 0 {
		locked := filepath.Join(base, "locked")
		if err := os.Mkdir(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		os.Chmod(locked, 0)
		defer os.Chmod(locked, 0o700)
		_, checks = doctorJSON(t, "--dir", agentDir, "--project", base, "--hub-dir", filepath.Join(locked, "hub"))
		if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "fail" || !strings.Contains(c.Detail, "读不到") || strings.Count(c.Detail, locked) != 1 || !strings.Contains(c.Hint, "同一个用户") {
			t.Fatalf("显式 --hub-dir 读不到应 fail: %+v", c)
		}
		if c := mustCheck(t, checks, "hubsrv.db"); c.Status != "skip" || !strings.Contains(c.Detail, "读不到") {
			t.Fatalf("hub 目录读不到时其余几项应按同一原因 skip: %+v", c)
		}
	}

	// 默认目录存在：做 hub 侧检查（没有 hub.db → 数据库 fail），且不建 hub.db
	t.Setenv("RAILSE_HUB_HOME", hubDir)
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", base)
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "ok" {
		t.Fatalf("$RAILSE_HUB_HOME 存在时应检查它: %+v", c)
	}
	if c := mustCheck(t, checks, "hubsrv.db"); c.Status != "fail" || !strings.Contains(c.Detail, filepath.Join(hubDir, "hub.db")) {
		t.Fatalf("没有 hub.db 时 hubsrv.db 应 fail: %+v", c)
	}

	// 显式 --hub-dir 存在：优先于 $RAILSE_HUB_HOME
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(base, "nohub"))
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", base, "--hub-dir", hubDir)
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "ok" || c.Detail != hubDir {
		t.Fatalf("--hub-dir 应传给 doctor: %+v", c)
	}
	if ents, _ := os.ReadDir(hubDir); len(ents) != 0 {
		t.Fatalf("doctor 不该在 hub 目录里建文件: %v", ents)
	}
}

// --project、--bin 传给钩子检查，默认 --bin 与 setup hooks 的默认一致；CLI 版本只经 doctorOptions 换上的假实现查。
func TestDoctorProjectBinAndCLIHook(t *testing.T) {
	base, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(base)
	t.Setenv("HOME", base)
	t.Setenv("RAILSE_HOME", "")
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(base, "nohub"))
	agentDir := filepath.Join(base, "h") // 不是默认的 ~/.railse
	proj := filepath.Join(base, "proj")
	bin := filepath.Join(base, "bin", "railse")
	var out, errOut bytes.Buffer
	// 旧装法：不给 --dir，钩子连默认 socket，对不上 --dir 的守护进程 → warn，提示带 --dir 重装
	if code := run([]string{"setup", "hooks", "--project", proj, "--bin", bin}, &out, &errOut); code != 0 {
		t.Fatalf("setup hooks: %d %s", code, errOut.String())
	}
	_, checks := doctorJSON(t, "--dir", agentDir, "--project", proj, "--bin", bin)
	for _, id := range []string{"hooks.claude", "hooks.codex", "hooks.gemini"} {
		if c := mustCheck(t, checks, id); c.Status != "warn" || !strings.Contains(c.Detail, filepath.Join(base, ".railse", "agent.sock")) ||
			!strings.Contains(c.Hint, "setup hooks") || !strings.Contains(c.Hint, "--dir "+agentDir) {
			t.Fatalf("钩子连默认 socket、守护进程在 --dir 时 %s 应 warn: %+v", id, c)
		}
	}
	// 按提示带 --dir 重装：钩子命令带 --socket，对得上
	if code := run([]string{"setup", "hooks", "--project", proj, "--bin", bin, "--dir", agentDir}, &out, &errOut); code != 0 {
		t.Fatalf("setup hooks --dir: %d %s", code, errOut.String())
	}
	var ran []string
	setDoctorOptions(t, func(o *doctor.Options) {
		o.LookPath = func(file string) (string, error) {
			if file == "claude" {
				return "/fake/claude", nil
			}
			return "", exec.ErrNotFound
		}
		o.RunCmd = func(name string, args ...string) (string, error) {
			ran = append(ran, name+" "+strings.Join(args, " "))
			return "2.1.280 (Claude Code)\n", nil
		}
	})
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", proj, "--bin", bin)
	for _, id := range []string{"hooks.claude", "hooks.codex", "hooks.gemini"} {
		if c := mustCheck(t, checks, id); c.Status != "ok" {
			t.Fatalf("装好钩子后 %s 应 ok: %+v", id, c)
		}
	}
	if c := mustCheck(t, checks, "cli.claude"); c.Status != "ok" || c.Detail != "2.1.280" {
		t.Fatalf("cli.claude 应用假的 RunCmd: %+v", c)
	}
	if c := mustCheck(t, checks, "cli.codex"); c.Status != "skip" {
		t.Fatalf("cli.codex 应用假的 LookPath: %+v", c)
	}
	if len(ran) != 1 || ran[0] != "/fake/claude --version" {
		t.Fatalf("RunCmd 调用 = %q", ran)
	}

	// 换一个 --bin：钩子指向别的程序 → warn
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", proj, "--bin", filepath.Join(base, "other", "railse"))
	if c := mustCheck(t, checks, "hooks.claude"); c.Status != "warn" || !strings.Contains(c.Detail, bin) {
		t.Fatalf("--bin 不同时 hooks.claude 应 warn: %+v", c)
	}

	// 不给 --bin：两边都默认当前程序，对得上
	proj2 := filepath.Join(base, "proj2")
	if code := run([]string{"setup", "hooks", "--project", proj2, "--claude", "--dir", agentDir}, &out, &errOut); code != 0 {
		t.Fatalf("setup hooks: %d %s", code, errOut.String())
	}
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", proj2)
	if c := mustCheck(t, checks, "hooks.claude"); c.Status != "ok" {
		t.Fatalf("默认 --bin 应与 setup hooks 的默认一致: %+v", c)
	}
}

// Linux 上没设 $RAILSE_HUB_HOME、~/.railse-hub 也不在时，回退到系统单元的 hub 数据目录（以 root 跑 railse setup autostart
// --hub --system 装的 hub），输出里写明用的是哪个目录；~/.railse-hub 在时优先它；设了 $RAILSE_HUB_HOME 就只看它；别的系统不回退。
func TestDoctorLinuxSystemHubDir(t *testing.T) {
	fakeDoctorCLIs(t)
	base, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(base)
	t.Setenv("HOME", base)
	t.Setenv("RAILSE_HUB_HOME", "")
	agentDir := filepath.Join(base, "h")
	sys := filepath.Join(base, "var-lib-railse-hub")
	if err := os.Mkdir(sys, 0o700); err != nil {
		t.Fatal(err)
	}
	setDoctorHubFallback(t, "linux", sys)

	_, checks := doctorJSON(t, "--dir", agentDir, "--project", base)
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "ok" || c.Detail != sys {
		t.Fatalf("Linux 上应回退到系统 hub 目录: %+v", c)
	}
	if c := mustCheck(t, checks, "hubsrv.db"); c.Status != "fail" || !strings.Contains(c.Detail, filepath.Join(sys, "hub.db")) {
		t.Fatalf("应检查系统 hub 目录里的库: %+v", c)
	}
	var out, errOut bytes.Buffer
	run([]string{"doctor", "--dir", agentDir, "--project", base}, &out, &errOut)
	if !strings.Contains(out.String(), "[ok]    hub 数据目录："+sys+"\n") {
		t.Fatalf("文本输出应写明用的是哪个 hub 目录: %s", out.String())
	}

	// ~/.railse-hub 在：优先它
	userHub := filepath.Join(base, ".railse-hub")
	if err := os.Mkdir(userHub, 0o700); err != nil {
		t.Fatal(err)
	}
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", base)
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "ok" || c.Detail != userHub {
		t.Fatalf("~/.railse-hub 在时应优先它: %+v", c)
	}
	os.Remove(userHub)

	// 设了 $RAILSE_HUB_HOME（哪怕不存在）：只看它，不回退
	envHub := filepath.Join(base, "nohub")
	t.Setenv("RAILSE_HUB_HOME", envHub)
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", base)
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "skip" || !strings.Contains(c.Detail, envHub) {
		t.Fatalf("设了 $RAILSE_HUB_HOME 时不该回退: %+v", c)
	}
	t.Setenv("RAILSE_HUB_HOME", "")

	// 不是 Linux：不看系统目录，细节写明看的是 ~/.railse-hub
	setDoctorHubFallback(t, "darwin", sys)
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", base)
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "skip" || !strings.Contains(c.Detail, userHub) {
		t.Fatalf("非 Linux 不该回退: %+v", c)
	}
	// 显式 --hub-dir 优先于一切默认
	setDoctorHubFallback(t, "linux", sys)
	other := filepath.Join(base, "other-hub")
	os.Mkdir(other, 0o700)
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", base, "--hub-dir", other)
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "ok" || c.Detail != other {
		t.Fatalf("显式 --hub-dir 应优先: %+v", c)
	}
}

// 没有任何 fail 时退出 0：守护进程在跑、钩子按同一个 --bin 装好、没有 hub 数据目录、看不到 CLI。
func TestDoctorExitsZeroWithoutFailures(t *testing.T) {
	fakeDoctorCLIs(t)
	_, _, cfg := startTestAgent(t)
	home := cfg.HomeDir
	t.Setenv("HOME", home)
	t.Setenv("RAILSE_HOME", "")
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(home, "nohub"))
	proj := filepath.Join(home, "proj")
	bin := filepath.Join(home, "bin", "railse")
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", proj, "--bin", bin, "--dir", cfg.Dir}, &out, &errOut); code != 0 {
		t.Fatalf("setup hooks: %d %s", code, errOut.String())
	}
	out.Reset()
	code := run([]string{"doctor", "--dir", cfg.Dir, "--project", proj, "--bin", bin}, &out, &errOut)
	if code != 0 || !strings.Contains(out.String(), "，0 项失败") || strings.Contains(out.String(), "[fail]") {
		t.Fatalf("没有 fail 时应退出 0: %d\n%s", code, out.String())
	}
	code, checks := doctorJSON(t, "--dir", cfg.Dir, "--project", proj, "--bin", bin)
	if code != 0 {
		t.Fatalf("--json 没有 fail 时应退出 0: %d %+v", code, checks)
	}
	for _, id := range []string{"agent.running", "hooks.claude", "hooks.codex", "hooks.gemini"} {
		if c := mustCheck(t, checks, id); c.Status != "ok" {
			t.Fatalf("%s: %+v", id, c)
		}
	}
}

// --dir、显式 --hub-dir 与 $RAILSE_HOME、$RAILSE_HUB_HOME 给的相对路径都按当前目录取成绝对路径，细节里不出现相对路径。
func TestDoctorAbsolutePaths(t *testing.T) {
	fakeDoctorCLIs(t)
	base, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(base)
	t.Setenv("HOME", base)
	t.Chdir(base)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	_, checks := doctorJSON(t, "--dir", "a", "--hub-dir", "b", "--project", ".")
	if c := mustCheck(t, checks, "agent.dir"); c.Detail != filepath.Join(cwd, "a")+" 不存在" {
		t.Fatalf("--dir 应取绝对路径: %+v", c)
	}
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Detail != filepath.Join(cwd, "b")+" 不存在" {
		t.Fatalf("--hub-dir 应取绝对路径: %+v", c)
	}
	t.Setenv("RAILSE_HOME", "c")
	t.Setenv("RAILSE_HUB_HOME", "d")
	_, checks = doctorJSON(t, "--project", ".")
	if c := mustCheck(t, checks, "agent.dir"); c.Detail != filepath.Join(cwd, "c")+" 不存在" {
		t.Fatalf("$RAILSE_HOME 应取绝对路径: %+v", c)
	}
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "skip" || !strings.Contains(c.Detail, filepath.Join(cwd, "d")) {
		t.Fatalf("$RAILSE_HUB_HOME 应取绝对路径: %+v", c)
	}
}
