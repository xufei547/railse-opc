package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/doctor"
	"github.com/xufei547/railse-opc/internal/proto"
	"github.com/xufei547/railse-opc/internal/tailscale"
)

// fakeDoctorCLIs 让测试里的 doctor 不碰本机环境，经 runDoctor 体检的测试（如 setup all）都先调它：
//   - 看不到任何 CLI（LookPath 一律找不到，RunCmd 被调到就算测试失败），免得在开发机上真的跑 claude、codex、gemini 的
//     --version（慢，还可能往临时 HOME 里建目录），CI 上又是另一种结果；
//   - tailscale 只认 fakeTailscale 记下的那一个假脚本（fakeTailscaleBin）：TailscalePath 找到的不是它（没换上假的，找到的是
//     本机真的 tailscale）时当作找不到，结果不随开发机变；TailscaleRun 只放行这个假脚本的 serve status --json（交给 runDoctor
//     接上的 tailscaleRun 跑），别的调用都算测试失败；
//   - HTTP 只放行回环 IP 的字面地址（hub.reachable 要连 127.0.0.1 上的测试 hub），别的主机（连 localhost 这样要解析的名字）
//     直接报错，不做 DNS、不拨号，记进返回的 doctorGuard：本机 hub 的对外地址（如假 tailscale 给的 ts.net 域名，可能真有其机）
//     绝不真的去连；
//   - 不回退到本机的系统 hub 目录。
func fakeDoctorCLIs(t *testing.T) *doctorGuard {
	t.Helper()
	g := &doctorGuard{}
	// 不用 http.DefaultTransport：有的测试会把它换成别的；也不走环境里的代理
	tr := &http.Transport{DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, TLSHandshakeTimeout: 5 * time.Second}
	t.Cleanup(tr.CloseIdleConnections)
	setDoctorHubFallback(t, runtime.GOOS, "")
	setDoctorOptions(t, func(o *doctor.Options) {
		o.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
		o.RunCmd = func(name string, args ...string) (string, error) {
			t.Errorf("测试里的 doctor 不该跑 %s %v", name, args)
			return "", errors.New("测试里不跑 CLI")
		}
		find, run := o.TailscalePath, o.TailscaleRun
		if find == nil || run == nil {
			t.Fatal("runDoctor 应接上 tailscaleFind 与 tailscaleRun")
		}
		o.TailscalePath = func() (string, error) {
			p, err := find()
			if err != nil {
				return "", err
			}
			if fakeTailscaleBin == "" || p != fakeTailscaleBin {
				return "", tailscale.ErrNotFound
			}
			return p, nil
		}
		o.TailscaleRun = func(ctx context.Context, path string, args ...string) (string, error) {
			if fakeTailscaleBin != "" && path == fakeTailscaleBin && slices.Equal(args, []string{"serve", "status", "--json"}) {
				g.add(&g.tailscale, path+" "+strings.Join(args, " "))
				return run(ctx, path, args...)
			}
			t.Errorf("测试里的 doctor 不该跑 %s %v", path, args)
			return "", errors.New("测试里不跑 tailscale")
		}
		o.HTTP = &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, // 同 doctor 的默认
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if h := r.URL.Hostname(); !loopbackLiteral(h) {
					g.add(&g.blocked, h)
					return nil, errors.New("测试里不出网")
				}
				return tr.RoundTrip(r)
			}),
		}
	})
	return g
}

// doctorGuard 记下测试里 doctor 想连而被拦下的主机，与放行了的假 tailscale 命令。
type doctorGuard struct {
	mu                 sync.Mutex
	blocked, tailscale []string
}

func (g *doctorGuard) add(list *[]string, s string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	*list = append(*list, s)
}

// blockedHosts 是到现在为止被拦下的主机（按请求的先后）。
func (g *doctorGuard) blockedHosts() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.blocked)
}

// tailscaleRuns 是到现在为止放行了的假 tailscale 命令（“路径 参数”）。
func (g *doctorGuard) tailscaleRuns() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.tailscale)
}

// loopbackLiteral 报告主机是不是回环 IP 的字面地址（127.0.0.0/8、::1、映射的 v4 回环）。只看字面，不做 DNS：
// localhost 这样的名字也不算。
func loopbackLiteral(host string) bool {
	a, err := netip.ParseAddr(host)
	return err == nil && a.Unmap().IsLoopback()
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

// setDoctorOwner 在本测试期间换掉 doctor 认系统 hub 目录属主用的 euid 与属主（owner 为负表示查不到），结束时换回。
func setDoctorOwner(t *testing.T, euid, owner int) {
	t.Helper()
	oldEuid, oldOwner := doctorEuid, doctorDirOwner
	doctorEuid = func() int { return euid }
	doctorDirOwner = func(string) (int, bool) { return owner, owner >= 0 }
	t.Cleanup(func() { doctorEuid, doctorDirOwner = oldEuid, oldOwner })
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

// 不给 --project：project.root 看当前目录，不是 git 仓库时只告知（skip，不建议 git init）；显式给了 --project（哪怕是 .）
// 才按要检查的项目报 warn、建议 git init。
func TestDoctorImplicitProjectOnlyInforms(t *testing.T) {
	fakeDoctorCLIs(t)
	base, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(base)
	t.Setenv("HOME", base)
	t.Setenv("RAILSE_HOME", "")
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(base, "nohub"))
	plain := filepath.Join(base, "Downloads")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(plain)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root, _ := filepath.EvalSymlinks(cwd)
	_, checks := doctorJSON(t)
	if c := mustCheck(t, checks, "project.root"); c.Status != "skip" || c.Detail != "目录 "+root+" 不是 git 仓库，在这里起 agent 会按当前目录当项目根" || c.Hint != "" {
		t.Fatalf("不给 --project 时应只告知: %+v", c)
	}
	for _, p := range []string{".", plain} {
		_, checks = doctorJSON(t, "--project", p)
		if c := mustCheck(t, checks, "project.root"); c.Status != "warn" || !strings.Contains(c.Hint, "git init") {
			t.Fatalf("显式 --project %s 应 warn: %+v", p, c)
		}
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
	// 系统 hub 目录属于别的用户（不是 root 跑的）：不回退，hub 侧 skip 并说明
	setDoctorHubFallback(t, "linux", sys)
	setDoctorOwner(t, 1000, 2000)
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", base)
	for _, id := range []string{"hubsrv.dir", "hubsrv.db", "hubsrv.autostart"} {
		if c := mustCheck(t, checks, id); c.Status != "skip" || !strings.Contains(c.Detail, sys) || !strings.Contains(c.Detail, "属于别的用户") || !strings.Contains(c.Detail, "--hub-dir") {
			t.Fatalf("系统 hub 目录属于别人时 %s 应 skip 并说明: %+v", id, c)
		}
	}
	// 属主查不到也一样不回退
	setDoctorOwner(t, 1000, -1)
	if _, checks = doctorJSON(t, "--dir", agentDir, "--project", base); mustCheck(t, checks, "hubsrv.dir").Status != "skip" {
		t.Fatalf("属主查不到时不该回退: %+v", checks["hubsrv.dir"])
	}
	// 以 root 跑：读得了别人的目录，照常回退
	setDoctorOwner(t, 0, 2000)
	if _, checks = doctorJSON(t, "--dir", agentDir, "--project", base); mustCheck(t, checks, "hubsrv.dir").Detail != sys {
		t.Fatalf("root 跑时应回退到系统 hub 目录: %+v", checks["hubsrv.dir"])
	}
	// 属主就是自己：回退
	setDoctorOwner(t, 2000, 2000)
	if _, checks = doctorJSON(t, "--dir", agentDir, "--project", base); mustCheck(t, checks, "hubsrv.dir").Detail != sys {
		t.Fatalf("属主是自己时应回退: %+v", checks["hubsrv.dir"])
	}
	// Linux 上两处都没有：skip 的细节列出查过的两个位置
	os.Remove(sys)
	_, checks = doctorJSON(t, "--dir", agentDir, "--project", base)
	if c := mustCheck(t, checks, "hubsrv.dir"); c.Status != "skip" || !strings.Contains(c.Detail, userHub) || !strings.Contains(c.Detail, sys) {
		t.Fatalf("两处都没有时应列出查过的位置: %+v", c)
	}
	os.Mkdir(sys, 0o700)

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

// local-hub.json 的校验口径在两处各写了一份：setup 读写它用 normalizeLocalHub（parseHubAddr + parsePublicOrigin），doctor 的
// hubsrv.local 不 import cmd/railse、自己再写一遍。这里用同一批端口、对外地址、provider 钉住两边：setup 收的，doctor 不报不合规；
// setup 不收的，doctor 报不合规（fail）。免得以后改了一边，doctor 对 setup 刚写的文件报“坏了，重跑 setup”，重跑也绕不出去。
// doctor 的输入全部注入：不找、不跑真的 tailscale，不出网。
func TestLocalHubFileRulesMatchDoctor(t *testing.T) {
	base := t.TempDir()
	hubDir := filepath.Join(base, "hub")
	if err := os.MkdirAll(hubDir, 0o700); err != nil {
		t.Fatal(err)
	}
	o := doctor.Options{
		AgentDir: filepath.Join(base, "agent"), Home: base, HubDir: hubDir, HubDirRequired: true, Version: version, GOOS: runtime.GOOS,
		DefaultSocket: filepath.Join(base, "agent", "agent.sock"),
		LookPath:      func(string) (string, error) { return "", exec.ErrNotFound },
		RunCmd: func(name string, args ...string) (string, error) {
			t.Errorf("不该跑 %s %v", name, args)
			return "", errors.New("测试里不跑 CLI")
		},
		TailscalePath: func() (string, error) { return "/fake/bin/tailscale", nil },
		TailscaleRun: func(_ context.Context, path string, args ...string) (string, error) {
			if path != "/fake/bin/tailscale" || !slices.Equal(args, []string{"serve", "status", "--json"}) {
				t.Errorf("不该跑 %s %v", path, args)
			}
			return "No serve config\n", nil
		},
		HTTP:        &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("测试里不出网") })},
		AgentStatus: func(string) (proto.Status, error) { return proto.Status{}, errors.New("测试里没有守护进程") },
		Geteuid:     func() int { return 1000 },
		FileOwner:   func(string) (int, bool) { return 1000, true },
	}
	urls := []string{
		// setup 收的
		"https://mac.example.com", "https://mac.example.com/", "https://mac.example.com//", "https://mac.example.com:8443",
		"https://mac.example.com:8443/", "HTTPS://mac.example.com", " https://mac.example.com ", "https://[::1]:8443", "https://127.0.0.1",
		"https://mac.example.com#",
		// 不收的
		"https://u:pw@mac.example.com", "https://u@mac.example.com", "https://mac.example.com?a=1", "https://mac.example.com?",
		"https://mac.example.com#f", "https://mac.example.com/hub", "https://mac.example.com/hub/", "https://mac.example.com/%2F",
		"http://mac.example.com", "ftp://mac.example.com", "mac.example.com", "//mac.example.com", "", "https:mac.example.com",
		"https://", "https://:8443", "https://mac.example.com:abc", "https://mac example.com",
	}
	ports := []int{-1, 0, 1, 8787, 65535, 65536}
	providers := []string{"", "custom", "tailscale", "other"}
	var accepted, rejected int
	for _, u := range urls {
		for _, port := range ports {
			for _, provider := range providers {
				lh := localHub{Port: port, PublicURL: u, Provider: provider}
				_, err := normalizeLocalHub(lh)
				setupOK := err == nil
				data, merr := json.Marshal(lh)
				if merr != nil {
					t.Fatal(merr)
				}
				if err := os.WriteFile(localHubPath(hubDir), data, 0o600); err != nil {
					t.Fatal(err)
				}
				var c doctor.Check
				for _, x := range doctor.Run(o).Checks {
					if x.ID == "hubsrv.local" {
						c = x
					}
				}
				if c.ID == "" {
					t.Fatalf("没有检查项 hubsrv.local")
				}
				doctorBad := strings.Contains(c.Detail, "local-hub.json 里 ") || strings.Contains(c.Detail, "不是合法 JSON")
				if setupOK == doctorBad || doctorBad && c.Status != doctor.Fail {
					t.Errorf("%s：setup 收=%v（%v），doctor 报 %s：%s", data, setupOK, err, c.Status, c.Detail)
				}
				if setupOK {
					accepted++
				} else {
					rejected++
				}
			}
		}
	}
	if accepted == 0 || rejected == 0 {
		t.Fatalf("样本应有收有不收：收 %d、不收 %d", accepted, rejected)
	}
}
