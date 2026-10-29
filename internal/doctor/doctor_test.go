package doctor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/autostart"
	"github.com/xufei547/railse-opc/internal/hooksconf"
	"github.com/xufei547/railse-opc/internal/hub/push"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/proto"
)

func find(t *testing.T, r Report, id string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("没有检查项 %s", id)
	return Check{}
}

// fakeOpts 给出一套全在临时目录里的输入：假 HOME、守护进程目录（含 0600 的 rules.yaml 与 socket），
// 不跑任何真实 CLI（LookPath 一律找不到），守护进程状态是注入的。
func fakeOpts(t *testing.T) (Options, string) {
	t.Helper()
	base, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	home := filepath.Join(base, "home")
	agentDir := filepath.Join(home, ".railse")
	if err := os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentDir, "rules.yaml"), []byte("version: 1\nrules: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(agentDir, "agent.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("socket %s: %v", sock, err)
	}
	t.Cleanup(func() { l.Close() })
	if err := os.Chmod(sock, 0o600); err != nil {
		t.Fatal(err)
	}
	return Options{
		AgentDir: agentDir, Home: home, GOOS: "darwin", Version: "1.0.0", Now: time.Now(),
		DefaultSocket: sock, // 不看测试进程的 $RAILSE_HOME 与真实家目录：默认目录就是这个守护进程目录
		LookPath:      func(string) (string, error) { return "", errors.New("not found") },
		RunCmd:        func(string, ...string) (string, error) { return "", errors.New("不该跑") },
		// 不找、不跑本机真的 tailscale：要用它的测试自己换上假的
		TailscalePath: func() (string, error) { return "", errors.New("测试里不找 tailscale") },
		TailscaleRun: func(context.Context, string, ...string) (string, error) {
			return "", errors.New("测试里不跑 tailscale")
		},
		HTTP:        loopbackOnlyClient(t), // 测试忘了换 HTTP 也不出网
		AgentStatus: func(string) (proto.Status, error) { return proto.Status{Rules: 2, Hub: "none"}, nil },
		// 固定成普通用户、家目录归自己：测试在 root 下跑也照常跑 --version 的分支
		Geteuid:   func() int { return 1000 },
		FileOwner: func(string) (int, bool) { return 1000, true },
	}, home
}

// loopbackOnlyClient 只连回环 IP 的字面地址（测试里的 httptest 服务），别的主机直接报错，不做 DNS、不拨号；不跟重定向，同 doctor 的默认。
func loopbackOnlyClient(t *testing.T) *http.Client {
	tr := &http.Transport{} // 不走环境里的代理
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			if a, err := netip.ParseAddr(r.URL.Hostname()); err != nil || !a.Unmap().IsLoopback() {
				return nil, errors.New("测试里不出网：" + r.URL.Hostname())
			}
			return tr.RoundTrip(r)
		}),
	}
}

func TestAgentChecksHappyAndPermissions(t *testing.T) {
	o, home := fakeOpts(t)
	// 钩子都装在用户级：没给 Project 时只看用户级，也全部正常
	writeFile(t, hooksconf.ClaudeUserPath(home), planDoc(t, "claude", "/usr/local/bin/railse"))
	writeFile(t, hooksconf.GeminiUserPath(home), planDoc(t, "gemini", "/usr/local/bin/railse"))
	writeFile(t, hooksconf.CodexPath(home), planDoc(t, "codex", "/usr/local/bin/railse"))
	r := Run(o)
	for _, id := range []string{"agent.dir", "agent.files", "agent.socket", "agent.running"} {
		if c := find(t, r, id); c.Status != OK {
			t.Fatalf("%s = %+v", id, c)
		}
	}
	if c := find(t, r, "agent.running"); !strings.Contains(c.Detail, "规则 2 条") {
		t.Fatalf("agent.running 细节: %+v", c)
	}
	if c := find(t, r, "agent.hub"); c.Status != Warn || !strings.Contains(c.Detail, "未接入") {
		t.Fatalf("agent.hub = %+v", c)
	}
	for _, id := range []string{"hooks.claude", "hooks.codex", "hooks.gemini"} {
		if c := find(t, r, id); c.Status != OK {
			t.Fatalf("没给 Project 时按用户级检查 %s: %+v", id, c)
		}
	}
	if r.Failed() {
		t.Fatalf("全部正常时不应失败: %+v", r.Checks)
	}
	os.Chmod(filepath.Join(o.AgentDir, "rules.yaml"), 0o644)
	os.Chmod(o.AgentDir, 0o755)
	r = Run(o)
	if c := find(t, r, "agent.files"); c.Status != Fail || !strings.Contains(c.Detail, "rules.yaml") || !strings.Contains(c.Hint, "chmod 600") {
		t.Fatalf("权限检查: %+v", c)
	}
	if c := find(t, r, "agent.dir"); c.Status != Fail || !strings.Contains(c.Hint, "chmod 700") {
		t.Fatalf("目录权限: %+v", c)
	}
	if !r.Failed() {
		t.Fatal("应标记失败")
	}
}

func TestAgentSocketAndMissingDir(t *testing.T) {
	o, home := fakeOpts(t)
	sock := filepath.Join(o.AgentDir, "agent.sock")
	os.Chmod(sock, 0o644)
	if c := find(t, Run(o), "agent.socket"); c.Status != Fail || !strings.Contains(c.Hint, "chmod 600") {
		t.Fatalf("socket 权限: %+v", c)
	}
	os.Remove(sock)
	os.WriteFile(sock, nil, 0o600)
	if c := find(t, Run(o), "agent.socket"); c.Status != Fail || !strings.Contains(c.Detail, "不是 socket") {
		t.Fatalf("普通文件冒充 socket: %+v", c)
	}
	os.Remove(sock)
	if c := find(t, Run(o), "agent.socket"); c.Status != Fail || !strings.Contains(c.Hint, "railse agent run") {
		t.Fatalf("没有 socket: %+v", c)
	}
	// 数据目录不存在：目录 fail，文件权限无从谈起 → skip
	o.AgentDir = filepath.Join(home, "nope")
	r := Run(o)
	if c := find(t, r, "agent.dir"); c.Status != Fail || !strings.Contains(c.Detail, "不存在") {
		t.Fatalf("目录不存在: %+v", c)
	}
	if c := find(t, r, "agent.files"); c.Status != Skip {
		t.Fatalf("目录不存在时文件检查应 skip: %+v", c)
	}
}

func TestAgentNotRunning(t *testing.T) {
	o, _ := fakeOpts(t)
	o.AgentStatus = func(string) (proto.Status, error) { return proto.Status{}, errors.New("connect: no such file") }
	r := Run(o)
	if c := find(t, r, "agent.running"); c.Status != Fail || !strings.Contains(c.Hint, "railse agent run") {
		t.Fatalf("%+v", c)
	}
	if c := find(t, r, "agent.hub"); c.Status != Skip {
		t.Fatalf("守护进程没在跑时 hub 连接应 skip: %+v", c)
	}
}

func TestAgentHubStates(t *testing.T) {
	o, _ := fakeOpts(t)
	for hub, want := range map[string]Status{"connected": OK, "connecting": Warn, "disabled": Fail, "none": Warn, "": Warn} {
		o.AgentStatus = func(string) (proto.Status, error) { return proto.Status{Hub: hub, HubURL: "https://hub.example"}, nil }
		c := find(t, Run(o), "agent.hub")
		if c.Status != want {
			t.Fatalf("hub=%q: %+v", hub, c)
		}
		switch hub {
		case "disabled":
			if !strings.Contains(c.Hint, "enroll") || !strings.Contains(c.Hint, "--force") {
				t.Fatalf("disabled 的提示: %+v", c)
			}
		case "":
			// 旧守护进程不报 hub 状态：别误报“未接入”
			if strings.Contains(c.Detail, "未接入") || !strings.Contains(c.Hint, "重启") {
				t.Fatalf("旧守护进程: %+v", c)
			}
		case "none":
			if !strings.Contains(c.Hint, "enroll") {
				t.Fatalf("没有 machine.json 时提示接入: %+v", c)
			}
		}
	}
	// 守护进程报 none 但 machine.json 已在：接入后还没重启守护进程，提示重启而不是再接入
	writeFile(t, filepath.Join(o.AgentDir, "machine.json"), []byte(`{}`))
	o.AgentStatus = func(string) (proto.Status, error) { return proto.Status{Hub: "none"}, nil }
	c := find(t, Run(o), "agent.hub")
	if c.Status != Warn || strings.Contains(c.Hint, "enroll") || !strings.Contains(c.Hint, "railse setup autostart") ||
		!strings.Contains(c.Hint, "railse agent run") || !strings.Contains(c.Detail, "machine.json") {
		t.Fatalf("接入后没重启: %+v", c)
	}
	// 守护进程不在默认目录：提示里的命令带 --dir
	o.DefaultSocket = "/elsewhere/agent.sock"
	if c := find(t, Run(o), "agent.hub"); !strings.Contains(c.Hint, "railse setup autostart --dir "+hooksconf.ShellQuote(o.AgentDir)) {
		t.Fatalf("非默认目录时提示带 --dir: %+v", c)
	}
}

func TestAutostartChecks(t *testing.T) {
	o, home := fakeOpts(t)
	old := autostart.SystemUnitDir
	autostart.SystemUnitDir = filepath.Join(home, "etc-systemd-system")
	t.Cleanup(func() { autostart.SystemUnitDir = old })
	hubDir := filepath.Join(home, ".railse-hub")
	os.MkdirAll(hubDir, 0o700)
	o.HubDir = hubDir

	// macOS：launchd 用户代理
	r := Run(o)
	if c := find(t, r, "agent.autostart"); c.Status != Warn || !strings.Contains(c.Hint, "railse setup autostart") {
		t.Fatalf("没有 plist: %+v", c)
	}
	if c := find(t, r, "hubsrv.autostart"); c.Status != Warn || !strings.Contains(c.Hint, "--hub") {
		t.Fatalf("hub 没有 plist: %+v", c)
	}
	for _, kind := range []string{"agent", "hub"} {
		p := autostart.PlistPath(home, kind)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("<plist/>"), 0o644)
	}
	r = Run(o)
	if c := find(t, r, "agent.autostart"); c.Status != OK || c.Detail != autostart.PlistPath(home, "agent") {
		t.Fatalf("有 plist: %+v", c)
	}
	if c := find(t, r, "hubsrv.autostart"); c.Status != OK {
		t.Fatalf("hub 有 plist: %+v", c)
	}

	// Linux：用户单元；hub 的系统单元也算
	o.GOOS = "linux"
	r = Run(o)
	if c := find(t, r, "agent.autostart"); c.Status != Warn {
		t.Fatalf("linux 没有用户单元: %+v", c)
	}
	if c := find(t, r, "hubsrv.autostart"); c.Status != Warn {
		t.Fatalf("linux hub 没有单元: %+v", c)
	}
	os.MkdirAll(autostart.SystemUnitDir, 0o755)
	os.WriteFile(autostart.UnitPath(home, "hub", true), []byte("[Unit]\n"), 0o644)
	if c := find(t, Run(o), "hubsrv.autostart"); c.Status != OK || c.Detail != autostart.UnitPath(home, "hub", true) {
		t.Fatalf("hub 系统单元: %+v", c)
	}
	u := autostart.UnitPath(home, "agent", false)
	os.MkdirAll(filepath.Dir(u), 0o755)
	os.WriteFile(u, []byte("[Unit]\n"), 0o644)
	if c := find(t, Run(o), "agent.autostart"); c.Status != OK || c.Detail != u {
		t.Fatalf("agent 用户单元: %+v", c)
	}

	o.GOOS = "windows"
	if c := find(t, Run(o), "agent.autostart"); c.Status != Skip {
		t.Fatalf("不支持的系统应 skip: %+v", c)
	}
}

func TestHubReachableAndProto(t *testing.T) {
	o, _ := fakeOpts(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"version": "1.0.0", "agent_proto": wire.Proto})
	}))
	defer srv.Close()
	if c := find(t, Run(o), "hub.reachable"); c.Status != Skip {
		t.Fatalf("没有 machine.json 应 skip: %+v", c)
	}
	os.WriteFile(filepath.Join(o.AgentDir, "machine.json"), []byte(`{"hub_url":"`+srv.URL+`","machine_id":"m","secret":"s","name":"mac"}`), 0o600)
	o.AgentStatus = func(string) (proto.Status, error) { return proto.Status{Hub: "connected", HubURL: srv.URL}, nil }
	r := Run(o)
	if c := find(t, r, "hub.reachable"); c.Status != OK || !strings.Contains(c.Detail, "1.0.0") {
		t.Fatalf("%+v", c)
	}
	if c := find(t, r, "agent.hub"); c.Status != OK {
		t.Fatalf("%+v", c)
	}
	srv.Close()
	r = Run(o)
	if c := find(t, r, "hub.reachable"); c.Status != Fail || c.Hint == "" {
		t.Fatalf("连不上应 fail: %+v", c)
	}
	o.AgentStatus = func(string) (proto.Status, error) { return proto.Status{Hub: "disabled", HubURL: srv.URL}, nil }
	if c := find(t, Run(o), "agent.hub"); c.Status != Fail || !strings.Contains(c.Hint, "enroll") || !strings.Contains(c.Hint, "--force") {
		t.Fatalf("%+v", c)
	}
}

func TestHubReachableProblems(t *testing.T) {
	o, _ := fakeOpts(t)
	machine := filepath.Join(o.AgentDir, "machine.json")
	writeMachine := func(hubURL string) {
		os.WriteFile(machine, []byte(`{"hub_url":"`+hubURL+`","machine_id":"m","secret":"top-secret","name":"mac"}`), 0o600)
	}
	// 协议不同
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"version": "9.0.0", "agent_proto": wire.Proto + 1})
	}))
	defer other.Close()
	writeMachine(other.URL)
	if c := find(t, Run(o), "hub.reachable"); c.Status != Fail || !strings.Contains(c.Detail, "协议") || !strings.Contains(c.Hint, "升级") {
		t.Fatalf("协议不同: %+v", c)
	}
	// 没有 /api/version（旧 hub 或别的服务）
	old := httptest.NewServer(http.NotFoundHandler())
	defer old.Close()
	writeMachine(old.URL)
	if c := find(t, Run(o), "hub.reachable"); c.Status != Fail || !strings.Contains(c.Detail, "404") {
		t.Fatalf("404: %+v", c)
	}
	// 重定向不跟：doctor 只访问 hub 的 /api/version
	var hit bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer elsewhere.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/api/version", http.StatusFound)
	}))
	defer redirect.Close()
	writeMachine(redirect.URL)
	if c := find(t, Run(o), "hub.reachable"); c.Status != Fail || !strings.Contains(c.Detail, "302") || hit {
		t.Fatalf("重定向: %+v hit=%v", c, hit)
	}
	// machine.json 坏了：fail，且不把内容（可能含密钥）带进报告
	os.WriteFile(machine, []byte(`{"hub_url":"x","secret":top-secret}`), 0o600)
	r := Run(o)
	c := find(t, r, "hub.reachable")
	if c.Status != Fail || !strings.Contains(c.Hint, "enroll") {
		t.Fatalf("坏 machine.json: %+v", c)
	}
	j, _ := r.JSON()
	if strings.Contains(string(j), "top-secret") {
		t.Fatalf("报告带出了密钥: %s", j)
	}
}

// planDoc 返回 hooksconf 为 cli 生成的整份钩子配置；socket 可省（不带 --socket）。
func planDoc(t *testing.T, cli, bin string, socket ...string) []byte {
	t.Helper()
	doc, err := hooksconf.Merge(nil, bin, hooksconf.Plan(cli, bin, strings.Join(socket, "")))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestHookChecks(t *testing.T) {
	o, home := fakeOpts(t)
	proj := filepath.Join(home, "proj")
	bin := "/usr/local/bin/railse"
	o.Project, o.Bin = proj, bin
	local, shared := hooksconf.ClaudePaths(proj)
	doc := planDoc(t, "claude", bin)
	writeFile(t, local, doc)
	writeFile(t, hooksconf.GeminiPath(proj), planDoc(t, "gemini", bin))
	r := Run(o)
	if c := find(t, r, "hooks.claude"); c.Status != OK {
		t.Fatalf("claude: %+v", c)
	}
	if c := find(t, r, "hooks.gemini"); c.Status != OK {
		t.Fatalf("gemini: %+v", c)
	}
	if c := find(t, r, "hooks.codex"); c.Status != Fail || !strings.Contains(c.Hint, "setup hooks") {
		t.Fatalf("codex 未装应 fail: %+v", c)
	}
	writeFile(t, hooksconf.CodexPath(home), planDoc(t, "codex", bin))
	if c := find(t, Run(o), "hooks.codex"); c.Status != OK {
		t.Fatalf("codex 装了: %+v", c)
	}
	// 超时不够
	bad := strings.Replace(string(doc), `"timeout": 86400`, `"timeout": 60`, 1)
	writeFile(t, local, []byte(bad))
	if c := find(t, Run(o), "hooks.claude"); c.Status != Fail || !strings.Contains(c.Detail, "PermissionRequest") || !strings.Contains(c.Detail, "60") || !strings.Contains(c.Hint, "setup hooks") {
		t.Fatalf("超时不够: %+v", c)
	}
	// 缺事件
	var m map[string]any
	json.Unmarshal(doc, &m)
	delete(m["hooks"].(map[string]any), "Stop")
	partial, _ := json.Marshal(m)
	writeFile(t, local, partial)
	if c := find(t, Run(o), "hooks.claude"); c.Status != Fail || !strings.Contains(c.Detail, "缺 Stop") {
		t.Fatalf("缺事件: %+v", c)
	}
	// 坏 JSON
	writeFile(t, local, []byte("{bad"))
	if c := find(t, Run(o), "hooks.claude"); c.Status != Fail || !strings.Contains(c.Detail, "JSON") || c.Hint == "" {
		t.Fatalf("坏 JSON: %+v", c)
	}
	// 两处都装了、Railse 的条目完全相同：Claude Code 对相同的处理器只跑一次，不因两处降级，细节说可以删掉旧位置的
	writeFile(t, local, doc)
	writeFile(t, shared, doc)
	if c := find(t, Run(o), "hooks.claude"); c.Status != OK || strings.Contains(c.Detail, "两次") || !strings.Contains(c.Detail, shared+" 也装了相同的钩子") ||
		!strings.Contains(c.Detail, "只跑一次") || !strings.Contains(c.Detail, "可以删掉 "+shared) {
		t.Fatalf("两处都装、条目相同: %+v", c)
	}
	// 两处的条目不同（旧位置是别的程序路径）：会触发两次
	writeFile(t, shared, planDoc(t, "claude", "/old/railse"))
	if c := find(t, Run(o), "hooks.claude"); c.Status != Warn || !strings.Contains(c.Detail, "两次") || !strings.Contains(c.Hint, "setup hooks") {
		t.Fatalf("两处都装、条目不同: %+v", c)
	}
	os.Remove(shared)
	// 指向别的程序
	o.Bin = "/opt/new/railse"
	if c := find(t, Run(o), "hooks.claude"); c.Status != Warn || !strings.Contains(c.Detail, "/usr/local/bin/railse") || !strings.Contains(c.Detail, "/opt/new/railse") {
		t.Fatalf("程序路径不同: %+v", c)
	}
	o.Bin = bin
	// codex notify：没有 config.toml 时说它不存在（不是权限问题），提示运行 railse setup codex-notify
	cfg := filepath.Join(home, ".codex", "config.toml")
	if c := find(t, Run(o), "hooks.codex_notify"); c.Status != Warn || !strings.Contains(c.Hint, "railse setup codex-notify") ||
		!strings.Contains(c.Detail, cfg+" 不存在") || strings.Contains(c.Detail, "读不到") {
		t.Fatalf("没有 config.toml: %+v", c)
	}
	// 读不了（权限）才说读不到
	if os.Geteuid() != 0 {
		writeFile(t, cfg, []byte("model = \"x\"\n"))
		os.Chmod(cfg, 0)
		c := find(t, Run(o), "hooks.codex_notify")
		os.Chmod(cfg, 0o600)
		os.Remove(cfg)
		if c.Status != Warn || !strings.Contains(c.Detail, cfg+" 读不到") || strings.Contains(c.Detail, "不存在") {
			t.Fatalf("config.toml 读不了: %+v", c)
		}
	}
	writeFile(t, cfg, []byte("model = \"x\"\nnotify = [\"/usr/local/bin/railse\", \"hook\", \"codex-notify\"]\n"))
	if c := find(t, Run(o), "hooks.codex_notify"); c.Status != OK {
		t.Fatalf("notify: %+v", c)
	}
	// 多行数组也认
	writeFile(t, cfg, []byte("notify = [\n  \"/usr/local/bin/railse\",\n  \"hook\",\n  \"codex-notify\",\n]\n[tui]\nx = 1\n"))
	if c := find(t, Run(o), "hooks.codex_notify"); c.Status != OK {
		t.Fatalf("多行 notify: %+v", c)
	}
	// 写在某个表下面：Codex 读不到顶层的 notify
	writeFile(t, cfg, []byte("model = \"x\"\n[mcp_servers.foo]\ncommand = \"foo\"\nnotify = [\"/usr/local/bin/railse\", \"hook\", \"codex-notify\"]\n"))
	if c := find(t, Run(o), "hooks.codex_notify"); c.Status != Warn || !strings.Contains(c.Detail, "[mcp_servers.foo]") || !strings.Contains(c.Hint, "railse setup codex-notify") {
		t.Fatalf("notify 在表下面: %+v", c)
	}
	// 顶层的 notify 是别人的命令：warn，提示运行 railse setup codex-notify（会保留它）
	// 参数逐个 ShellQuote，带空格的不会看不清边界
	writeFile(t, cfg, []byte("notify = [\"/usr/local/bin/notify wrap\", \"--title=a b\"]\n"))
	if c := find(t, Run(o), "hooks.codex_notify"); c.Status != Warn || !strings.Contains(c.Detail, `（'/usr/local/bin/notify wrap' '--title=a b'）`) || !strings.Contains(c.Hint, "railse setup codex-notify") {
		t.Fatalf("别人的 notify: %+v", c)
	}
	// 看不懂的写法：warn，说明看不懂
	writeFile(t, cfg, []byte("notify = \"\"\"x\"\"\"\n"))
	if c := find(t, Run(o), "hooks.codex_notify"); c.Status != Warn || !strings.Contains(c.Detail, "第 1 行") || c.Hint == "" {
		t.Fatalf("看不懂的 notify: %+v", c)
	}
}

// notify 是 railse setup codex-notify 写的串接脚本：脚本在、可执行、里面有 railse 那一行才 ok；脚本里 railse 那一行连的
// socket 与 --dir 的守护进程对不上时 warn（与旧写法同一规则）；脚本不在、不可执行、没有 railse 那一行都 warn，提示重跑。
// 用户自己的 codex-notify.sh（不在数据目录里、没有标记行）不说“重新生成”，说重跑会把它保留为前一跳。
func TestCodexNotifyScript(t *testing.T) {
	o, home := fakeOpts(t)
	bin := "/usr/local/bin/railse"
	o.Bin = bin
	cfg := hooksconf.CodexConfigPath(home)
	script := filepath.Join(o.AgentDir, hooksconf.NotifyScriptName)
	writeFile(t, cfg, []byte(fmt.Sprintf("model = \"x\"\nnotify = [%q]\n[tui]\nx = 1\n", script)))
	check := func() Check { t.Helper(); return find(t, Run(o), "hooks.codex_notify") }
	chmod := func(p string, mode os.FileMode) {
		t.Helper()
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	if c := check(); c.Status != Warn || !strings.Contains(c.Detail, script) || !strings.Contains(c.Hint, "railse setup codex-notify") {
		t.Fatalf("脚本不在: %+v", c)
	}
	writeFile(t, script, hooksconf.NotifyScript([]string{"/usr/local/bin/notify-wrap"}, bin, ""))
	chmod(script, 0o644)
	if c := check(); c.Status != Warn || !strings.Contains(c.Detail, "不可执行") || !strings.Contains(c.Hint, "railse setup codex-notify") {
		t.Fatalf("脚本不可执行: %+v", c)
	}
	chmod(script, 0o755)
	if c := check(); c.Status != OK || !strings.Contains(c.Detail, script) {
		t.Fatalf("脚本写法: %+v", c)
	}
	// 数据目录里的脚本没有 railse 那一行（被改过）：重跑会重新生成它
	writeFile(t, script, []byte("#!/bin/sh\n/usr/local/bin/notify-wrap \"$@\"\n"))
	chmod(script, 0o755)
	if c := check(); c.Status != Warn || !strings.Contains(c.Hint, "railse setup codex-notify") || !strings.Contains(c.Hint, "重新生成") {
		t.Fatalf("脚本里没有 railse: %+v", c)
	}
	// 用户自己的 codex-notify.sh（别处、没有标记行）：重跑会把它保留为前一跳，不说重新生成
	own := filepath.Join(home, "bin", hooksconf.NotifyScriptName)
	writeFile(t, own, []byte("#!/bin/sh\nterminal-notifier -message \"$1\"\n"))
	chmod(own, 0o755)
	writeFile(t, cfg, []byte(fmt.Sprintf("notify = [%q]\n", own)))
	if c := check(); c.Status != Warn || !strings.Contains(c.Hint, "railse setup codex-notify") || !strings.Contains(c.Hint, "保留为前一跳") || strings.Contains(c.Hint, "重新生成") {
		t.Fatalf("用户自己的 codex-notify.sh: %+v", c)
	}
	writeFile(t, cfg, []byte(fmt.Sprintf("notify = [%q]\n", script)))
	// railse 那一行带 --socket 指向别处：warn，提示重跑或给 doctor 加 --dir
	data := filepath.Join(home, "data dir")
	dataSock := filepath.Join(data, "agent.sock")
	writeFile(t, script, hooksconf.NotifyScript(nil, bin, dataSock))
	chmod(script, 0o755)
	if c := check(); c.Status != Warn || !strings.Contains(c.Detail, "连的是 "+dataSock) || !strings.Contains(c.Hint, "doctor 加 --dir "+hooksconf.ShellQuote(data)) {
		t.Fatalf("socket 对不上: %+v", c)
	}
	// 守护进程确实在 data：脚本在 data 里、带 --socket 的 ok；不带 --socket 的旧脚本 warn，重跑命令带 --dir
	o.AgentDir = data
	script = filepath.Join(data, hooksconf.NotifyScriptName)
	writeFile(t, cfg, []byte(fmt.Sprintf("notify = [%q]\n", script)))
	writeFile(t, script, hooksconf.NotifyScript(nil, bin, dataSock))
	chmod(script, 0o755)
	if c := check(); c.Status != OK {
		t.Fatalf("--dir 的脚本: %+v", c)
	}
	writeFile(t, script, hooksconf.NotifyScript(nil, bin, ""))
	if c := check(); c.Status != Warn || !strings.Contains(c.Detail, "连的是 "+o.DefaultSocket) || !strings.Contains(c.Hint, "railse setup codex-notify --dir "+hooksconf.ShellQuote(data)) {
		t.Fatalf("不带 --socket 的脚本: %+v", c)
	}
	// 旧写法仍 ok
	writeFile(t, cfg, []byte(fmt.Sprintf("notify = [%q, \"hook\", \"codex-notify\", \"--socket\", %q]\n", bin, dataSock)))
	if c := check(); c.Status != OK {
		t.Fatalf("旧写法: %+v", c)
	}
}

// 钩子连的 socket（命令里的 --socket，没有就是默认 socket）与 --dir 的守护进程对不上：warn 并给出带 --dir 的重装命令。
func TestHookSocketMismatch(t *testing.T) {
	o, home := fakeOpts(t)
	proj := filepath.Join(home, "proj")
	bin := "/usr/local/bin/railse"
	o.Project, o.Bin = proj, bin
	local, _ := hooksconf.ClaudePaths(proj)
	codexCfg := filepath.Join(home, ".codex", "config.toml")
	defSock := o.DefaultSocket
	data := filepath.Join(home, "data dir")
	os.MkdirAll(data, 0o700)
	dataSock := filepath.Join(data, "agent.sock")
	install := func(socket string) {
		writeFile(t, local, planDoc(t, "claude", bin, socket))
		writeFile(t, hooksconf.GeminiPath(proj), planDoc(t, "gemini", bin, socket))
		writeFile(t, hooksconf.CodexPath(home), planDoc(t, "codex", bin, socket))
		notify := fmt.Sprintf("notify = [%q, \"hook\", \"codex-notify\"", bin)
		if socket != "" {
			notify += fmt.Sprintf(", \"--socket\", %q", socket)
		}
		writeFile(t, codexCfg, []byte(notify+"]\n"))
	}
	ids := []string{"hooks.claude", "hooks.codex", "hooks.codex_notify", "hooks.gemini"}
	expect := func(status Status, parts ...string) {
		t.Helper()
		r := Run(o)
		for _, id := range ids {
			c := find(t, r, id)
			if c.Status != status {
				t.Fatalf("%s 应为 %s: %+v", id, status, c)
			}
			for _, p := range parts {
				if !strings.Contains(c.Detail+"\n"+c.Hint, p) {
					t.Fatalf("%s 应提到 %q: %+v", id, p, c)
				}
			}
		}
	}
	// 默认目录、不带 --socket：对得上
	install("")
	expect(OK)
	// 守护进程在非默认目录，钩子还是旧的（不带 --socket，连默认 socket）：warn，提示带 --dir 重装
	o.AgentDir = data
	expect(Warn, "连的是 "+defSock, dataSock, "--dir "+hooksconf.ShellQuote(data))
	// 按 --dir 重装（带 --socket）：ok
	install(dataSock)
	expect(OK)
	// 带 --socket 指向 data，doctor 看的却是默认目录：warn，提示重装或给 doctor 加 --dir
	o.AgentDir = filepath.Dir(defSock)
	expect(Warn, "连的是 "+dataSock, defSock, "doctor 加 --dir "+hooksconf.ShellQuote(data))
	if c := find(t, Run(o), "hooks.claude"); strings.Contains(c.Hint, "setup hooks --project "+hooksconf.ShellQuote(proj)+" --claude --dir") {
		t.Fatalf("守护进程在默认目录时重装命令不带 --dir: %+v", c)
	}
	// 缺事件仍是 fail，不被 socket 的 warn 盖住
	writeFile(t, local, []byte(`{"hooks":{}}`))
	if c := find(t, Run(o), "hooks.claude"); c.Status != Fail {
		t.Fatalf("缺事件应 fail: %+v", c)
	}
	// 程序路径与 socket 都不对：两条都说，仍是 warn
	writeFile(t, local, planDoc(t, "claude", "/old/railse", dataSock))
	if c := find(t, Run(o), "hooks.claude"); c.Status != Warn || !strings.Contains(c.Detail, "/old/railse") || !strings.Contains(c.Detail, dataSock) {
		t.Fatalf("程序与 socket 都不对: %+v", c)
	}
	// 不给 DefaultSocket：基准是 Home 下的 ~/.railse/agent.sock，不看 $RAILSE_HOME（钩子进程的环境里不一定有它，按最坏情况算）
	o.AgentDir, o.DefaultSocket = data, ""
	t.Setenv("RAILSE_HOME", data)
	install("")
	if c := find(t, Run(o), "hooks.gemini"); c.Status != Warn || !strings.Contains(c.Detail, "连的是 "+filepath.Join(home, ".railse", "agent.sock")) {
		t.Fatalf("不带 --socket 的钩子应按 ~/.railse/agent.sock 比对，不看 $RAILSE_HOME: %+v", c)
	}
	// 取不到家目录：推不出基准，不带 --socket 的不比对
	o.Home = ""
	t.Setenv("RAILSE_HOME", "")
	t.Setenv("HOME", "")
	if c := find(t, Run(o), "hooks.gemini"); c.Status != OK {
		t.Fatalf("推不出基准 socket 时不比对: %+v", c)
	}
}

func TestClaudeHooksOldLocation(t *testing.T) {
	o, home := fakeOpts(t)
	proj := filepath.Join(home, "proj")
	bin := "/usr/local/bin/railse"
	o.Project, o.Bin = proj, bin
	local, shared := hooksconf.ClaudePaths(proj)
	doc := planDoc(t, "claude", bin)

	// 两处都没有：fail
	if c := find(t, Run(o), "hooks.claude"); c.Status != Fail || !strings.Contains(c.Hint, "setup hooks") {
		t.Fatalf("没装: %+v", c)
	}
	// 只在旧位置（settings.local.json 不存在）：钩子在生效，只 warn，不提“不存在”“两次”
	writeFile(t, shared, doc)
	c := find(t, Run(o), "hooks.claude")
	if c.Status != Warn || !strings.Contains(c.Detail, "旧位置") || !strings.Contains(c.Hint, "setup hooks") ||
		strings.Contains(c.Detail, "不存在") || strings.Contains(c.Detail, "两次") {
		t.Fatalf("只在旧位置: %+v", c)
	}
	// settings.local.json 存在但没有我们的条目：同样是旧位置
	writeFile(t, local, []byte(`{"permissions":{"allow":[]}}`))
	if c := find(t, Run(o), "hooks.claude"); c.Status != Warn || !strings.Contains(c.Detail, "旧位置") || strings.Contains(c.Detail, "两次") {
		t.Fatalf("local 无条目、旧位置有: %+v", c)
	}
	// 旧位置的条目本身不全：如实 fail，并说明在旧位置
	bad := strings.Replace(string(doc), `"timeout": 86400`, `"timeout": 60`, 1)
	writeFile(t, shared, []byte(bad))
	if c := find(t, Run(o), "hooks.claude"); c.Status != Fail || !strings.Contains(c.Detail, "旧位置") || !strings.Contains(c.Detail, "PermissionRequest") {
		t.Fatalf("旧位置不全: %+v", c)
	}
	// settings.local.json 是坏 JSON、钩子在旧位置：报坏 JSON（setup 也要往里写），并提旧位置
	writeFile(t, shared, doc)
	writeFile(t, local, []byte("{bad"))
	if c := find(t, Run(o), "hooks.claude"); c.Status != Fail || !strings.Contains(c.Detail, "JSON") || !strings.Contains(c.Detail, "旧位置") {
		t.Fatalf("local 坏、旧位置有: %+v", c)
	}
	// settings.local.json 是 settings.json 的链接：同一个文件，不算两处
	os.Remove(local)
	if err := os.Symlink("settings.json", local); err != nil {
		t.Fatal(err)
	}
	if c := find(t, Run(o), "hooks.claude"); c.Status != OK {
		t.Fatalf("同一文件: %+v", c)
	}
}

// Claude、Gemini 的钩子：用户级（Home 下）或项目级（Project 下）任一装了就算装了。只有用户级：按用户级文件查，修复提示
// 不带 --project；只有项目级：沿用项目级的检查，提示带 --project；两处都装：按用户级查，Gemini 至少 warn、说会触发两次，
// Claude 两处 Railse 的条目完全相同时只跑一次、不降级，不同才 warn；都没装：fail，列出看过的位置（项目级文件坏了时
// 报它，提示修好它的 JSON）；用户级是坏 JSON：fail。
func TestHookChecksUserOrProject(t *testing.T) {
	o, home := fakeOpts(t)
	proj := filepath.Join(home, "proj")
	bin := "/usr/local/bin/railse"
	o.Bin = bin
	user := map[string]string{"claude": hooksconf.ClaudeUserPath(home), "gemini": hooksconf.GeminiUserPath(home)}
	local, shared := hooksconf.ClaudePaths(proj)
	project := map[string]string{"claude": local, "gemini": hooksconf.GeminiPath(proj)}
	ids := map[string]string{"claude": "hooks.claude", "gemini": "hooks.gemini"}
	check := func(cli string) Check { t.Helper(); return find(t, Run(o), ids[cli]) }
	userHint := func(cli string) string { return "railse setup hooks --" + cli }

	// Project 为空、用户级没装：fail，细节是看过的用户级文件，提示装用户级；Codex 两项照样检查
	for cli := range ids {
		if c := check(cli); c.Status != Fail || !strings.Contains(c.Detail, user[cli]) || c.Hint != userHint(cli) {
			t.Fatalf("%s 没装: %+v", cli, c)
		}
	}
	if c := find(t, Run(o), "hooks.codex"); c.Status == Skip {
		t.Fatalf("没给 Project 时 Codex 照样检查: %+v", c)
	}
	// 只有用户级装了：ok
	for cli, p := range user {
		writeFile(t, p, planDoc(t, cli, bin))
	}
	for cli := range ids {
		if c := check(cli); c.Status != OK || c.Detail != user[cli] {
			t.Fatalf("%s 只有用户级: %+v", cli, c)
		}
	}
	// 用户级不全：fail，提示装用户级
	writeFile(t, user["claude"], []byte(strings.Replace(string(planDoc(t, "claude", bin)), `"timeout": 86400`, `"timeout": 60`, 1)))
	if c := check("claude"); c.Status != Fail || !strings.Contains(c.Detail, user["claude"]) || c.Hint != userHint("claude") {
		t.Fatalf("用户级不全: %+v", c)
	}
	writeFile(t, user["claude"], planDoc(t, "claude", bin))
	// 给了 Project、项目级没装：仍按用户级，ok
	o.Project = proj
	for cli := range ids {
		if c := check(cli); c.Status != OK || c.Detail != user[cli] {
			t.Fatalf("%s 给了 Project、只有用户级: %+v", cli, c)
		}
	}
	// 两处都装了、条目相同：Gemini 仍 warn（文档没说去重），说会触发两次，提示删掉项目级的条目；
	// Claude 对相同的处理器只跑一次：不降级，细节说项目级也装了相同的钩子、可以删掉
	for cli, p := range project {
		writeFile(t, p, planDoc(t, cli, bin))
	}
	if c := check("gemini"); c.Status != Warn || !strings.Contains(c.Detail, user["gemini"]) || !strings.Contains(c.Detail, project["gemini"]) ||
		!strings.Contains(c.Detail, "触发两次") || !strings.Contains(c.Hint, "删掉") || !strings.Contains(c.Hint, project["gemini"]) {
		t.Fatalf("gemini 两处都装: %+v", c)
	}
	if c := check("claude"); c.Status != OK || !strings.HasPrefix(c.Detail, user["claude"]) || strings.Contains(c.Detail, "两次") ||
		!strings.Contains(c.Detail, local+" 也装了相同的钩子") || !strings.Contains(c.Detail, "只跑一次") || !strings.Contains(c.Detail, "可以删掉 "+local) {
		t.Fatalf("claude 两处都装、条目相同: %+v", c)
	}
	// Claude 两处的条目不同（项目级是别的程序路径）：会触发两次，warn，提示删掉项目级的条目
	writeFile(t, local, planDoc(t, "claude", "/old/railse"))
	if c := check("claude"); c.Status != Warn || !strings.Contains(c.Detail, local) || !strings.Contains(c.Detail, "触发两次") ||
		!strings.Contains(c.Hint, "删掉") || !strings.Contains(c.Hint, local) {
		t.Fatalf("claude 两处都装、条目不同: %+v", c)
	}
	// Claude 项目级的旧位置也算：相同只跑一次，不同触发两次
	os.Remove(local)
	writeFile(t, shared, planDoc(t, "claude", bin))
	if c := check("claude"); c.Status != OK || !strings.Contains(c.Detail, shared+" 也装了相同的钩子") || strings.Contains(c.Detail, "两次") {
		t.Fatalf("用户级与项目级旧位置都装、条目相同: %+v", c)
	}
	writeFile(t, shared, planDoc(t, "claude", bin, "/x/agent.sock"))
	if c := check("claude"); c.Status != Warn || !strings.Contains(c.Detail, shared) || !strings.Contains(c.Detail, "触发两次") {
		t.Fatalf("用户级与项目级旧位置都装、条目不同: %+v", c)
	}
	os.Remove(shared)
	writeFile(t, local, planDoc(t, "claude", bin))
	// 只有项目级装了（用户级文件在，但没有 Railse 的条目）：沿用项目级的检查，ok；不全时 fail，提示带 --project
	writeFile(t, user["claude"], []byte(`{"model":"opus"}`))
	os.Remove(user["gemini"])
	for cli := range ids {
		if c := check(cli); c.Status != OK || c.Detail != project[cli] {
			t.Fatalf("%s 只有项目级: %+v", cli, c)
		}
	}
	writeFile(t, project["gemini"], []byte(strings.Replace(string(planDoc(t, "gemini", bin)), `"timeout": 86400000`, `"timeout": 5`, 1)))
	if c := check("gemini"); c.Status != Fail || c.Hint != "railse setup hooks --project "+hooksconf.ShellQuote(proj)+" --gemini" {
		t.Fatalf("只有项目级且不全: %+v", c)
	}
	// 都没装：fail，细节列出看过的用户级与项目级位置，提示装用户级
	os.Remove(local)
	os.Remove(project["gemini"])
	for cli := range ids {
		c := check(cli)
		if c.Status != Fail || !strings.Contains(c.Detail, user[cli]) || !strings.Contains(c.Detail, project[cli]) || c.Hint != userHint(cli) {
			t.Fatalf("%s 都没装: %+v", cli, c)
		}
	}
	// 都没装、项目级文件是坏 JSON：报那个文件，提示修好它的 JSON（带 --project 的命令；装用户级不碰它），细节里也说看过的用户级
	writeFile(t, local, []byte("{bad"))
	if c := check("claude"); c.Status != Fail || !strings.Contains(c.Detail, local) || !strings.Contains(c.Detail, "JSON") || !strings.Contains(c.Detail, user["claude"]) ||
		c.Hint != "修好 JSON 后再 railse setup hooks --project "+hooksconf.ShellQuote(proj)+" --claude" {
		t.Fatalf("都没装、项目级坏 JSON: %+v", c)
	}
	// 用户级是坏 JSON：fail，提示修好再装（项目级装着也一样）
	writeFile(t, local, planDoc(t, "claude", bin))
	writeFile(t, user["claude"], []byte("{bad"))
	if c := check("claude"); c.Status != Fail || !strings.Contains(c.Detail, user["claude"]) || !strings.Contains(c.Detail, "JSON") ||
		!strings.Contains(c.Hint, "修好 JSON") || !strings.Contains(c.Hint, userHint("claude")) {
		t.Fatalf("用户级坏 JSON: %+v", c)
	}
}

// 同一个文件不算两处：项目就是家目录时 Gemini 的项目级就是用户级；Claude 项目级的旧位置 <项目>/.claude/settings.json
// 可能就是用户级文件（项目就是家目录，或链过去）。Home 为空时只看项目级，两者都空时 skip。
func TestHookChecksSameFileAndMissingLevels(t *testing.T) {
	o, home := fakeOpts(t)
	bin := "/usr/local/bin/railse"
	o.Bin, o.Project = bin, home
	writeFile(t, hooksconf.ClaudeUserPath(home), planDoc(t, "claude", bin))
	writeFile(t, hooksconf.GeminiUserPath(home), planDoc(t, "gemini", bin))
	for _, id := range []string{"hooks.claude", "hooks.gemini"} {
		if c := find(t, Run(o), id); c.Status != OK {
			t.Fatalf("项目就是家目录时 %s 不算两处: %+v", id, c)
		}
	}
	// 项目就是家目录、又装了 ~/.claude/settings.local.json（在 ~ 里是项目级的本机配置），条目不同：真是两处
	local, _ := hooksconf.ClaudePaths(home)
	writeFile(t, local, planDoc(t, "claude", "/old/railse"))
	if c := find(t, Run(o), "hooks.claude"); c.Status != Warn || !strings.Contains(c.Detail, local) || !strings.Contains(c.Detail, "触发两次") {
		t.Fatalf("~/.claude/settings.local.json 也装了: %+v", c)
	}
	os.Remove(local)
	// 项目的 .claude/settings.json 链到用户级文件：同一个文件
	proj := filepath.Join(home, "proj")
	_, shared := hooksconf.ClaudePaths(proj)
	os.MkdirAll(filepath.Dir(shared), 0o755)
	if err := os.Symlink(hooksconf.ClaudeUserPath(home), shared); err != nil {
		t.Fatal(err)
	}
	o.Project = proj
	if c := find(t, Run(o), "hooks.claude"); c.Status != OK || c.Detail != hooksconf.ClaudeUserPath(home) {
		t.Fatalf("项目的 settings.json 链到用户级文件不算两处: %+v", c)
	}
	// Home 为空：只看项目级（沿用项目级的检查与提示）
	t.Setenv("HOME", "")
	o.Home = ""
	if c := find(t, Run(o), "hooks.gemini"); c.Status != Fail || !strings.Contains(c.Detail, hooksconf.GeminiPath(proj)) || !strings.Contains(c.Hint, "--project") {
		t.Fatalf("Home 为空时只看项目级: %+v", c)
	}
	writeFile(t, hooksconf.GeminiPath(proj), planDoc(t, "gemini", bin))
	if c := find(t, Run(o), "hooks.gemini"); c.Status != OK {
		t.Fatalf("Home 为空、项目级装了: %+v", c)
	}
	// 两者都空：skip
	o.Project = ""
	r := Run(o)
	for _, id := range []string{"hooks.claude", "hooks.codex", "hooks.codex_notify", "hooks.gemini"} {
		if c := find(t, r, id); c.Status != Skip {
			t.Fatalf("Home 与 Project 都空时 %s 应 skip: %+v", id, c)
		}
	}
}

func TestCLIVersions(t *testing.T) {
	o, _ := fakeOpts(t)
	o.LookPath = func(name string) (string, error) { return "/bin/" + name, nil }
	var ran []string
	o.RunCmd = func(name string, args ...string) (string, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		switch filepath.Base(name) {
		case "claude":
			return "2.1.280 (Claude Code)\n", nil
		case "codex":
			return "codex-cli 0.150.0\n", nil
		case "gemini":
			return "0.60.0\n", nil
		}
		return "", errors.New("no")
	}
	r := Run(o)
	if c := find(t, r, "cli.claude"); c.Status != OK {
		t.Fatalf("claude: %+v", c)
	}
	if c := find(t, r, "cli.codex"); c.Status != Warn || !strings.Contains(c.Detail, "0.145.0") || !strings.Contains(c.Detail, "0.150.0") || c.Hint == "" {
		t.Fatalf("codex 版本不同应 warn: %+v", c)
	}
	if c := find(t, r, "cli.gemini"); c.Status != Fail || !strings.Contains(c.Detail, "0.65.0") {
		t.Fatalf("gemini 太旧应 fail: %+v", c)
	}
	sort.Strings(ran)
	if want := []string{"/bin/claude --version", "/bin/codex --version", "/bin/gemini --version"}; strings.Join(ran, "|") != strings.Join(want, "|") {
		t.Fatalf("只应跑 --version: %q", ran)
	}
	o.RunCmd = func(name string, args ...string) (string, error) {
		switch filepath.Base(name) {
		case "gemini":
			return "0.65.0\n", nil
		case "claude":
			return "", errors.New("signal: killed")
		}
		return "no version here", nil
	}
	r = Run(o)
	if c := find(t, r, "cli.gemini"); c.Status != OK {
		t.Fatalf("gemini 0.65.0 应 ok: %+v", c)
	}
	if c := find(t, r, "cli.claude"); c.Status != Warn || !strings.Contains(c.Detail, "killed") || c.Hint == "" {
		t.Fatalf("--version 出错: %+v", c)
	}
	if c := find(t, r, "cli.codex"); c.Status != Warn || c.Hint == "" {
		t.Fatalf("没有版本号: %+v", c)
	}
	o.LookPath = func(string) (string, error) { return "", errors.New("nope") }
	if c := find(t, Run(o), "cli.claude"); c.Status != Skip {
		t.Fatalf("没装应 skip: %+v", c)
	}
}

// TestHelperSlowCLI 不是真的测试：被 TestDefaultRunCmdTimeout 当作“卡住的 CLI”起成子进程。
func TestHelperSlowCLI(t *testing.T) {
	if os.Getenv("RAILSE_DOCTOR_SLOW_CLI") != "1" {
		t.Skip("只在子进程里跑")
	}
	time.Sleep(time.Minute)
}

func TestDefaultRunCmdTimeout(t *testing.T) {
	oldTimeout, oldCmd := cliTimeout, commandContext
	t.Cleanup(func() { cliTimeout, commandContext = oldTimeout, oldCmd })
	cliTimeout = 300 * time.Millisecond
	var got []string
	commandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		got = append([]string{name}, args...)
		// 换成本测试程序自己扮演的慢 CLI：不跑任何真实 CLI
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperSlowCLI$")
		cmd.Env = append(os.Environ(), "RAILSE_DOCTOR_SLOW_CLI=1")
		return cmd
	}
	var o Options
	o.defaults()
	start := time.Now()
	_, err := o.RunCmd("/bin/claude", "--version")
	if err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("应超时: %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("超时没生效，等了 %s", d)
	}
	if strings.Join(got, " ") != "/bin/claude --version" {
		t.Fatalf("命令: %q", got)
	}
}

// newHubDB 在 hubDir 建一个 hub.db（与 hub 一样经 store.Open），fn 在关库之前对库做事。
func newHubDB(t *testing.T, hubDir string, fn func(*store.Store)) string {
	t.Helper()
	if err := os.MkdirAll(hubDir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(hubDir, "hub.db")
	st, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if fn != nil {
		fn(st)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func goodVAPID(t *testing.T) string {
	t.Helper()
	v, err := push.GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(v.Raw())
}

func TestHubServerChecks(t *testing.T) {
	o, home := fakeOpts(t)
	hubDir := filepath.Join(home, ".railse-hub")
	vapid := goodVAPID(t)
	dbPath := newHubDB(t, hubDir, func(st *store.Store) { st.SetSetting("vapid_private", vapid) })
	o.HubDir = hubDir
	r := Run(o)
	for _, id := range []string{"hubsrv.dir", "hubsrv.db", "hubsrv.vapid"} {
		if c := find(t, r, id); c.Status != OK {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	if c := find(t, r, "hubsrv.password"); c.Status != Warn || !strings.Contains(c.Hint, "hub password") {
		t.Fatalf("没设密码: %+v", c)
	}
	if c := find(t, r, "hubsrv.acme"); c.Status != Skip {
		t.Fatalf("没有 acme 目录应 skip: %+v", c)
	}
	// 设了密码
	st, _ := store.Open(dbPath)
	st.SetSetting("password_hash", "$argon2id$x")
	st.Close()
	if c := find(t, Run(o), "hubsrv.password"); c.Status != OK {
		t.Fatalf("设了密码: %+v", c)
	}
	// VAPID 损坏
	st, _ = store.Open(dbPath)
	st.SetSetting("vapid_private", "not-base64!!")
	st.Close()
	if c := find(t, Run(o), "hubsrv.vapid"); c.Status != Fail || !strings.Contains(c.Hint, "vapid --rotate") {
		t.Fatalf("坏 VAPID: %+v", c)
	}
	// base64 对但不是合法标量
	st, _ = store.Open(dbPath)
	st.SetSetting("vapid_private", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	st.Close()
	if c := find(t, Run(o), "hubsrv.vapid"); c.Status != Fail {
		t.Fatalf("零标量: %+v", c)
	}
	// ACME 目录
	acme := filepath.Join(hubDir, "acme")
	os.Mkdir(acme, 0o755)
	os.Chmod(acme, 0o755)
	if c := find(t, Run(o), "hubsrv.acme"); c.Status != Fail || !strings.Contains(c.Hint, "chmod 700") {
		t.Fatalf("acme 权限: %+v", c)
	}
	os.Chmod(acme, 0o700)
	if c := find(t, Run(o), "hubsrv.acme"); c.Status != OK {
		t.Fatalf("acme: %+v", c)
	}
	// 目录与库的权限
	os.Chmod(hubDir, 0o755)
	os.Chmod(dbPath, 0o644)
	r = Run(o)
	if c := find(t, r, "hubsrv.dir"); c.Status != Fail || !strings.Contains(c.Hint, "chmod 700") {
		t.Fatalf("hub 目录权限: %+v", c)
	}
	if c := find(t, r, "hubsrv.db"); c.Status != Fail || !strings.Contains(c.Hint, "chmod 600") {
		t.Fatalf("hub.db 权限: %+v", c)
	}
	if c := find(t, r, "hubsrv.password"); c.Status != OK {
		t.Fatalf("权限不对也照样读库: %+v", c)
	}
	os.Chmod(hubDir, 0o700)
	os.Chmod(dbPath, 0o600)
	// 没有 HubDir：整段 skip
	o.HubDir = ""
	r = Run(o)
	for _, id := range []string{"hubsrv.dir", "hubsrv.db", "hubsrv.password", "hubsrv.vapid", "hubsrv.acme", "hubsrv.autostart"} {
		if c := find(t, r, id); c.Status != Skip {
			t.Fatalf("%s: %+v", id, c)
		}
	}
}

func setSchemaVersion(t *testing.T, dbPath string, v int) {
	t.Helper()
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE schema_version SET v = ?", v); err != nil {
		t.Fatal(err)
	}
}

func TestHubServerSchemaAndBrokenDB(t *testing.T) {
	o, home := fakeOpts(t)
	hubDir := filepath.Join(home, ".railse-hub")
	dbPath := newHubDB(t, hubDir, nil)
	o.HubDir = hubDir
	setSchemaVersion(t, dbPath, store.SchemaVersion+1)
	if c := find(t, Run(o), "hubsrv.db"); c.Status != Fail || !strings.Contains(c.Hint, "升级") {
		t.Fatalf("库比程序新: %+v", c)
	}
	setSchemaVersion(t, dbPath, store.SchemaVersion-1)
	if c := find(t, Run(o), "hubsrv.db"); c.Status != Warn || c.Hint == "" {
		t.Fatalf("库比程序旧: %+v", c)
	}
	// 不是 SQLite
	os.WriteFile(dbPath, []byte("garbage, not a database at all...................................................................................."), 0o600)
	r := Run(o)
	if c := find(t, r, "hubsrv.db"); c.Status != Fail || !strings.Contains(c.Hint, "损坏") || !strings.Contains(c.Hint, "移走") {
		t.Fatalf("坏库: %+v", c)
	}
	for _, id := range []string{"hubsrv.password", "hubsrv.vapid"} {
		if c := find(t, r, id); c.Status != Skip {
			t.Fatalf("库读不了时 %s 应 skip: %+v", id, c)
		}
	}
	find(t, r, "hubsrv.autostart")
	// 没有 hub.db
	os.Remove(dbPath)
	r = Run(o)
	if c := find(t, r, "hubsrv.db"); c.Status != Fail || !strings.Contains(c.Hint, "railse hub run") {
		t.Fatalf("没有库: %+v", c)
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("doctor 不该建出 hub.db: %v", err)
	}
}

type fileState struct {
	size  int64
	mode  os.FileMode
	mtime time.Time
}

func snapshot(t *testing.T, dir string) map[string]fileState {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]fileState{}
	dst, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	out["."] = fileState{0, dst.Mode(), dst.ModTime()} // 目录自身：建了又删的文件也会改它的 mtime
	for _, e := range ents {
		st, err := os.Lstat(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = fileState{st.Size(), st.Mode(), st.ModTime()}
	}
	return out
}

func names(m map[string]fileState) string {
	var s []string
	for k := range m {
		if k != "." {
			s = append(s, k)
		}
	}
	sort.Strings(s)
	return strings.Join(s, ",")
}

// hub 没在跑（WAL 已检查点、没有 -wal/-shm）：doctor 前后目录里的文件一个不多，大小、权限、mtime（含目录自身的）都不变。
func TestHubDBReadLeavesNoSideFiles(t *testing.T) {
	o, home := fakeOpts(t)
	hubDir := filepath.Join(home, ".railse-hub")
	vapid := goodVAPID(t)
	newHubDB(t, hubDir, func(st *store.Store) {
		st.SetSetting("vapid_private", vapid)
		st.SetSetting("password_hash", "$argon2id$x")
	})
	o.HubDir = hubDir
	before := snapshot(t, hubDir)
	if names(before) != "hub.db" {
		t.Fatalf("前提：关库后应只剩 hub.db，实际 %s", names(before))
	}
	time.Sleep(20 * time.Millisecond) // 让可能的写入在 mtime 上看得出来
	r := Run(o)
	for _, id := range []string{"hubsrv.db", "hubsrv.password", "hubsrv.vapid"} {
		if c := find(t, r, id); c.Status != OK {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	after := snapshot(t, hubDir)
	if names(after) != names(before) {
		t.Fatalf("doctor 留下了文件：前 %s，后 %s", names(before), names(after))
	}
	for name, b := range before {
		if a := after[name]; a != b {
			t.Fatalf("%s 变了：前 %+v，后 %+v", name, b, a)
		}
	}
}

// hub 在跑（-wal 存在）：只读打开（readonly_shm），读得到还在 WAL 里、没检查点的数据，也不多建文件。
func TestHubDBReadWhileHubRunning(t *testing.T) {
	o, home := fakeOpts(t)
	hubDir := filepath.Join(home, ".railse-hub")
	os.MkdirAll(hubDir, 0o700)
	dbPath := filepath.Join(hubDir, "hub.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSetting("password_hash", "$argon2id$x"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dbPath + "-wal"); err != nil {
		t.Fatalf("前提：hub 开着时应有 -wal: %v", err)
	}
	o.HubDir = hubDir
	before := snapshot(t, hubDir)
	r := Run(o)
	if c := find(t, r, "hubsrv.password"); c.Status != OK {
		t.Fatalf("应读到 WAL 里的密码: %+v", c)
	}
	after := snapshot(t, hubDir)
	if names(after) != names(before) {
		t.Fatalf("doctor 多建了文件：前 %s，后 %s", names(before), names(after))
	}
	for _, name := range []string{".", "hub.db"} {
		if after[name] != before[name] {
			t.Fatalf("%s 变了：前 %+v，后 %+v", name, before[name], after[name])
		}
	}
	// hub 还能照常写
	if err := st.SetSetting("x", "y"); err != nil {
		t.Fatal(err)
	}
}

// HubMachine 只读地查 hub 库里的机器（setup all 重跑本机 hub 时核对）：没有 hub.db 不算错、找不到；id 与密钥都对上才算找到，
// 另报是否已吊销。hub 没在跑（没有 -wal）与在跑（库开着）两种都读得到，也都不多建文件、不改库。
func TestHubMachine(t *testing.T) {
	hubDir := filepath.Join(t.TempDir(), "hub")
	if found, revoked, err := HubMachine(hubDir, "m", "s"); found || revoked || err != nil {
		t.Fatalf("没有 hub 数据目录: %v %v %v", found, revoked, err)
	}
	var mine, gone string
	newHubDB(t, hubDir, func(st *store.Store) {
		m, err := st.CreateMachine("mac", "s-mine", "")
		if err != nil {
			t.Fatal(err)
		}
		mine = m.ID
		g, err := st.CreateMachine("old", "s-gone", "")
		if err == nil {
			err = st.RevokeMachine(g.ID)
		}
		if err != nil {
			t.Fatal(err)
		}
		gone = g.ID
	})
	check := func(when string) {
		t.Helper()
		before := snapshot(t, hubDir)
		for _, c := range []struct {
			id, secret     string
			found, revoked bool
		}{
			{mine, "s-mine", true, false},
			{mine, "s-other", false, false}, // 密钥对不上：守护进程拿它连 hub 也会被拒
			{"no-such", "s-mine", false, false},
			{gone, "s-gone", true, true},
		} {
			found, revoked, err := HubMachine(hubDir, c.id, c.secret)
			if err != nil || found != c.found || revoked != c.revoked {
				t.Fatalf("%s %s/%s: found=%v revoked=%v err=%v，想要 %v %v", when, c.id, c.secret, found, revoked, err, c.found, c.revoked)
			}
		}
		after := snapshot(t, hubDir)
		if names(after) != names(before) || after["hub.db"] != before["hub.db"] {
			t.Fatalf("%s：只读地查，不该多建文件、改库：前 %s，后 %s", when, names(before), names(after))
		}
	}
	check("hub 没在跑")
	st, err := store.Open(filepath.Join(hubDir, "hub.db")) // hub 在跑：库开着，有 -wal、-shm
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	check("hub 在跑")
}

// 每个 warn 与 fail 都带修复提示，每项都有名称，ID 不重复。
func TestEveryProblemHasHint(t *testing.T) {
	o, home := fakeOpts(t)
	os.Chmod(o.AgentDir, 0o755)
	os.WriteFile(filepath.Join(o.AgentDir, "audit.jsonl"), nil, 0o644)
	os.WriteFile(filepath.Join(o.AgentDir, "machine.json"), []byte("{"), 0o600)
	o.AgentStatus = func(string) (proto.Status, error) { return proto.Status{Hub: "connecting"}, nil }
	o.Project, o.Bin = filepath.Join(home, "proj"), "/opt/railse"
	local, _ := hooksconf.ClaudePaths(o.Project)
	writeFile(t, local, []byte("[]"))
	writeFile(t, hooksconf.GeminiPath(o.Project), []byte(`{"hooks":{"BeforeTool":[{"hooks":[{"name":"railse-before-tool","type":"command","command":"/x/railse hook gemini","timeout":5}]}]}}`))
	o.LookPath = func(name string) (string, error) { return "/bin/" + name, nil }
	o.RunCmd = func(name string, _ ...string) (string, error) {
		if filepath.Base(name) == "gemini" {
			return "0.1.0", nil
		}
		return "9.9.9", nil
	}
	hubDir := filepath.Join(home, ".railse-hub")
	dbPath := newHubDB(t, hubDir, nil)
	setSchemaVersion(t, dbPath, store.SchemaVersion-1)
	os.Chmod(hubDir, 0o755)
	os.Mkdir(filepath.Join(hubDir, "acme"), 0o755)
	os.WriteFile(filepath.Join(hubDir, "local-hub.json"), []byte("{"), 0o600)
	o.HubDir = hubDir
	r := Run(o)
	seen := map[string]bool{}
	var problems int
	for _, c := range r.Checks {
		if c.Name == "" || c.ID == "" || seen[c.ID] {
			t.Fatalf("名称为空或 ID 重复: %+v", c)
		}
		seen[c.ID] = true
		switch c.Status {
		case Fail, Warn:
			problems++
			if c.Hint == "" {
				t.Fatalf("%s 没有修复提示: %+v", c.ID, c)
			}
		case OK, Skip:
		default:
			t.Fatalf("未知状态: %+v", c)
		}
	}
	if problems < 15 {
		var b strings.Builder
		r.Write(&b)
		t.Fatalf("场景不够坏，只有 %d 项问题:\n%s", problems, b.String())
	}
}

func TestReportOutput(t *testing.T) {
	r := Report{Version: "1.0.0", Checks: []Check{
		{ID: "a", Name: "甲", Status: OK, Detail: "好"},
		{ID: "b", Name: "乙", Status: Fail, Detail: "坏", Hint: "修它"},
		{ID: "c", Name: "丙", Status: Skip},
		{ID: "d", Name: "丁", Status: Warn, Detail: "有点怪", Hint: "看看"},
	}}
	var b strings.Builder
	r.Write(&b)
	out := b.String()
	for _, want := range []string{"[ok]", "甲：好", "[fail]", "乙：坏\n", "修复：修它", "[skip]", "[warn]", "修复：看看", "1 项通过，1 项警告，1 项失败，1 项跳过"} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺 %q:\n%s", want, out)
		}
	}
	j, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Report
	if json.Unmarshal(j, &back) != nil || len(back.Checks) != 4 || back.Checks[1].Hint != "修它" || back.Checks[1].Status != Fail || back.Version != "1.0.0" {
		t.Fatalf("JSON: %s", j)
	}
	if !strings.Contains(string(j), `"status": "fail"`) {
		t.Fatalf("JSON 字段名: %s", j)
	}
	if !r.Failed() {
		t.Fatal("有 fail 时 Failed 应为真")
	}
	r.Checks[1].Status = Warn
	if r.Failed() {
		t.Fatal("只有 warn 时 Failed 应为假")
	}
}

// hub 异常退出后 -shm 被删、只剩 -wal：doctor 不建 -shm（readonly_shm 打不开时回退 immutable），照样读得出库。
func TestHubDBOnlyWALNoShm(t *testing.T) {
	o, home := fakeOpts(t)
	hubDir := filepath.Join(home, ".railse-hub")
	dbPath := newHubDB(t, hubDir, func(st *store.Store) { st.SetSetting("password_hash", "$argon2id$x") })
	if err := os.WriteFile(dbPath+"-wal", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	o.HubDir = hubDir
	before := snapshot(t, hubDir)
	if names(before) != "hub.db,hub.db-wal" {
		t.Fatalf("前提: %s", names(before))
	}
	time.Sleep(20 * time.Millisecond)
	r := Run(o)
	for _, id := range []string{"hubsrv.db", "hubsrv.password"} {
		if c := find(t, r, id); c.Status != OK {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	after := snapshot(t, hubDir)
	if names(after) != names(before) {
		t.Fatalf("doctor 多建了文件：前 %s，后 %s", names(before), names(after))
	}
	for name, b := range before {
		if a := after[name]; a != b {
			t.Fatalf("%s 变了：前 %+v，后 %+v", name, b, a)
		}
	}
}

// 另一个连接以独占方式持着 hub.db（hub 关库、崩溃恢复时都会短暂这样）：报“正忙、稍后重跑”，不能叫人移走数据库。
func TestHubDBLockedIsNotCorrupt(t *testing.T) {
	o, home := fakeOpts(t)
	hubDir := filepath.Join(home, ".railse-hub")
	dbPath := newHubDB(t, hubDir, func(st *store.Store) { st.SetSetting("password_hash", "$argon2id$x") })
	o.HubDir = hubDir
	locker, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	locker.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA locking_mode=EXCLUSIVE", "INSERT INTO settings(key, value) VALUES('lock', 'x')"} {
		if _, err := locker.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	r := Run(o)
	c := find(t, r, "hubsrv.db")
	if c.Status != Fail || strings.Contains(c.Hint, "移走") || strings.Contains(c.Hint, "损坏") || !strings.Contains(c.Hint, "稍后") {
		t.Fatalf("被锁: %+v", c)
	}
	if c := find(t, r, "hubsrv.password"); c.Status != Skip {
		t.Fatalf("被锁时密码应 skip: %+v", c)
	}
	locker.Close()
	if c := find(t, Run(o), "hubsrv.db"); c.Status != OK {
		t.Fatalf("锁放开后: %+v", c)
	}
}

// 锁只持一小会儿（hub 关库时的检查点）：doctor 等一等就读到，不报错。
func TestHubDBWaitsForBriefLock(t *testing.T) {
	o, home := fakeOpts(t)
	hubDir := filepath.Join(home, ".railse-hub")
	dbPath := newHubDB(t, hubDir, func(st *store.Store) { st.SetSetting("password_hash", "$argon2id$x") })
	o.HubDir = hubDir
	locker, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String())
	if err != nil {
		t.Fatal(err)
	}
	locker.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA locking_mode=EXCLUSIVE", "INSERT INTO settings(key, value) VALUES('lock', 'x')"} {
		if _, err := locker.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	released := make(chan struct{})
	go func() {
		time.Sleep(300 * time.Millisecond)
		locker.Close()
		close(released)
	}()
	r := Run(o)
	<-released
	for _, id := range []string{"hubsrv.db", "hubsrv.password"} {
		if c := find(t, r, id); c.Status != OK {
			t.Fatalf("%s: %+v", id, c)
		}
	}
}

func TestParseVersion(t *testing.T) {
	for in, want := range map[string]string{
		"2.1.280 (Claude Code)\n":       "2.1.280",
		"codex-cli 0.150.0\n":           "0.150.0",
		"0.65.0":                        "0.65.0",
		"v1.2.3\n":                      "1.2.3",
		"0.66.0-nightly.20261020.abc\n": "0.66.0",
		"(node:123) Warning: requires node v18.19.0\n0.60.0\n": "0.60.0",
		"0.60.0\n(node:123) Warning: requires node v18.19.0\n": "0.60.0",
		"update available: 1.0.0 -> 1.2.0":                     "1.2.0",
		// 别的程序的版本行（如 Node.js）不算：前缀词只认 CLI 自己的名字与 version
		"0.60.0\nNode.js v18.19.0\n":                                      "0.60.0",
		"gemini version 0.60.0\n(node:1) Warning: requires node v18.19.0": "0.60.0",
		"Gemini Version v0.66.1\n":                                        "0.66.1",
		"codex 0.150.0\n":                                                 "0.150.0",
		"claude 2.1.280\n":                                                "2.1.280",
	} {
		v, ok := parseVersion(in)
		if got := fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2]); !ok || got != want {
			t.Fatalf("parseVersion(%q) = %s, %v；要 %s", in, got, ok, want)
		}
	}
	if _, ok := parseVersion("no version"); ok {
		t.Fatal("没有版本号应返回 false")
	}
}

func TestCLIVersionAfterWarningsAndFailureOutput(t *testing.T) {
	o, _ := fakeOpts(t)
	o.LookPath = func(name string) (string, error) { return "/bin/" + name, nil }
	long := strings.Repeat("长", 300)
	o.RunCmd = func(name string, _ ...string) (string, error) {
		switch filepath.Base(name) {
		case "gemini":
			return "(node:123) Warning: requires node v18.19.0\n0.60.0\n", nil
		case "claude":
			return "Error: config is broken\n    at stack line\n", errors.New("exit status 1")
		}
		return long + "\nsecond", errors.New("exit status 2")
	}
	r := Run(o)
	if c := find(t, r, "cli.gemini"); c.Status != Fail || !strings.Contains(c.Detail, "0.60.0") {
		t.Fatalf("node 警告里的版本不该算: %+v", c)
	}
	if c := find(t, r, "cli.claude"); c.Status != Warn || !strings.Contains(c.Detail, "Error: config is broken") || strings.Contains(c.Detail, "stack") || !strings.Contains(c.Detail, "exit status 1") {
		t.Fatalf("失败输出首行: %+v", c)
	}
	c := find(t, r, "cli.codex")
	if strings.Contains(c.Detail, "second") || !strings.Contains(c.Detail, strings.Repeat("长", 200)+"…") || strings.Contains(c.Detail, strings.Repeat("长", 201)) {
		t.Fatalf("首行应截到 200 个字符: %+v", c)
	}
}

// 修复提示里的路径按 shell 引起来，带空格也能直接复制运行。
func TestHintsQuotePaths(t *testing.T) {
	base, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	agentDir := filepath.Join(base, "a b", ".railse")
	os.MkdirAll(agentDir, 0o700)
	os.Chmod(agentDir, 0o755)
	rules := filepath.Join(agentDir, "rules.yaml")
	os.WriteFile(rules, nil, 0o644)
	sock := filepath.Join(agentDir, "agent.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	os.Chmod(sock, 0o644)
	hubDir := filepath.Join(base, "h b")
	acme := filepath.Join(hubDir, "acme")
	os.MkdirAll(acme, 0o700)
	key := filepath.Join(acme, "acme_account+key")
	os.WriteFile(key, nil, 0o644)
	o := Options{
		AgentDir: agentDir, HubDir: hubDir, Home: base, GOOS: "darwin",
		LookPath:    func(string) (string, error) { return "", errors.New("no") },
		RunCmd:      func(string, ...string) (string, error) { return "", errors.New("不该跑") },
		AgentStatus: func(string) (proto.Status, error) { return proto.Status{Hub: "none"}, nil },
	}
	r := Run(o)
	q := hooksconf.ShellQuote
	for id, want := range map[string]string{
		"agent.dir":    "chmod 700 " + q(agentDir),
		"agent.files":  "chmod 600 " + q(rules),
		"agent.socket": "chmod 600 " + q(sock),
		"hubsrv.acme":  "chmod 600 " + q(key),
	} {
		if c := find(t, r, id); !strings.Contains(c.Hint, want) {
			t.Fatalf("%s 的提示要含 %q: %+v", id, want, c)
		}
	}
	os.Chmod(acme, 0o755)
	if c := find(t, Run(o), "hubsrv.acme"); !strings.Contains(c.Hint, "chmod 700 "+q(acme)) {
		t.Fatalf("acme 目录: %+v", c)
	}
}

// 取不到家目录：用到家目录的检查 skip 并说明，不拼出相对路径去读当前目录下的文件。
func TestNoHomeDir(t *testing.T) {
	o, home := fakeOpts(t)
	t.Setenv("HOME", "")
	o.Home = ""
	o.Project = filepath.Join(home, "proj")
	r := Run(o)
	for _, id := range []string{"agent.autostart", "hooks.codex", "hooks.codex_notify"} {
		if c := find(t, r, id); c.Status != Skip || !strings.Contains(c.Detail, "家目录") {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	for _, c := range r.Checks {
		if strings.HasPrefix(c.Detail, "Library") || strings.HasPrefix(c.Detail, ".codex") || strings.Contains(c.Detail, "没有托管文件 Library") {
			t.Fatalf("拼出了相对路径: %+v", c)
		}
	}
}

// sudo 下（euid 是 root，或家目录不归当前用户）不跑 CLI：它们的 --version 可能在用户目录写缓存，留下 root 属主的文件。
func TestCLISkippedUnderSudo(t *testing.T) {
	const reason, hint = "sudo 下不跑 CLI，免得在用户目录留下 root 属主的文件", "用平时的用户身份重跑 railse doctor"
	o, home := fakeOpts(t)
	o.LookPath = func(name string) (string, error) { return "/bin/" + name, nil }
	ran := 0
	o.RunCmd = func(string, ...string) (string, error) { ran++; return "2.1.280\n", nil }
	var asked []string
	for name, set := range map[string]func(){
		"euid 是 root": func() { o.Geteuid = func() int { return 0 }; o.FileOwner = func(string) (int, bool) { return 0, true } },
		"家目录归别的用户": func() {
			o.Geteuid = func() int { return 1000 }
			o.FileOwner = func(p string) (int, bool) { asked = append(asked, p); return 1001, true }
		},
	} {
		set()
		r := Run(o)
		for _, id := range []string{"cli.claude", "cli.codex", "cli.gemini"} {
			if c := find(t, r, id); c.Status != Skip || c.Detail != reason || c.Hint != hint {
				t.Fatalf("%s：%s = %+v", name, id, c)
			}
		}
		if ran != 0 {
			t.Fatalf("%s：不该跑 --version（跑了 %d 次）", name, ran)
		}
	}
	if len(asked) == 0 || asked[0] != home {
		t.Fatalf("应查家目录 %s 的属主，实际 %q", home, asked)
	}
	// 普通情况：照常跑
	o.Geteuid = func() int { return 1000 }
	o.FileOwner = func(string) (int, bool) { return 1000, true }
	if c := find(t, Run(o), "cli.claude"); c.Status != OK || ran != 3 {
		t.Fatalf("普通用户: %+v ran=%d", c, ran)
	}
	// 查不到家目录属主时只看 euid
	o.FileOwner = func(string) (int, bool) { return 0, false }
	if c := find(t, Run(o), "cli.claude"); c.Status != OK {
		t.Fatalf("属主查不到: %+v", c)
	}
}

// 默认的 Geteuid 与 FileOwner 是真的：自己建的临时目录归自己。
func TestDefaultOwnerLookup(t *testing.T) {
	var o Options
	o.defaults()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	uid, ok := o.FileOwner(dir)
	if !ok || uid != o.Geteuid() || o.Geteuid() != os.Geteuid() {
		t.Fatalf("属主 %d %v，euid %d", uid, ok, o.Geteuid())
	}
	if _, ok := o.FileOwner(filepath.Join(dir, "nope")); ok {
		t.Fatal("不存在的路径应查不到属主")
	}
}

// HubDirRequired（显式给的 --hub-dir）而目录用不了：hubsrv.dir 按原因 fail 并给对得上的提示，依赖数据目录的四项
// 用同一个原因 skip，hub 开机自启不依赖数据目录、照常检查；没要求时照旧整段 skip。
func TestHubDirRequired(t *testing.T) {
	o, home := fakeOpts(t)
	o.HubDirRequired = true
	file := filepath.Join(home, "plain")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, dir, detail, hint, cause string
	}{
		{"不存在", filepath.Join(home, "typo"), "不存在", "这台机器不跑 hub 就去掉 --hub-dir", "不存在"},
		{"路径中间是文件", filepath.Join(file, "hub"), "不存在", "这台机器不跑 hub 就去掉 --hub-dir", "不存在"},
		{"是文件", file, "不是目录", "railse hub run --dir", "不是目录"},
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(home, "locked")
		if err := os.Mkdir(locked, 0o700); err != nil {
			t.Fatal(err)
		}
		os.Chmod(locked, 0)
		t.Cleanup(func() { os.Chmod(locked, 0o700) })
		cases = append(cases, struct{ name, dir, detail, hint, cause string }{"读不到", filepath.Join(locked, "hub"), "读不到", "同一个用户", "读不到"})
	}
	for _, tc := range cases {
		o.HubDir = tc.dir
		r := Run(o)
		c := find(t, r, "hubsrv.dir")
		if c.Status != Fail || !strings.Contains(c.Detail, tc.detail) || strings.Count(c.Detail, tc.dir) != 1 || !strings.Contains(c.Hint, tc.hint) {
			t.Fatalf("%s: hubsrv.dir = %+v", tc.name, c)
		}
		for _, id := range []string{"hubsrv.db", "hubsrv.password", "hubsrv.vapid", "hubsrv.acme"} {
			if c := find(t, r, id); c.Status != Skip || c.Detail != "hub 数据目录 "+tc.dir+" "+tc.cause {
				t.Fatalf("%s: %s = %+v", tc.name, id, c)
			}
		}
		if c := find(t, r, "hubsrv.autostart"); c.Status != Warn || !strings.Contains(c.Detail, autostart.PlistPath(home, "hub")) {
			t.Fatalf("%s: hub 开机自启应照常检查: %+v", tc.name, c)
		}
		if _, err := os.Stat(filepath.Join(home, "typo")); !os.IsNotExist(err) {
			t.Fatalf("doctor 不该建目录: %v", err)
		}
	}
	// 没要求（默认目录的情形）：不存在或不是目录就整段 skip，细节写明看的是哪里
	o.HubDirRequired = false
	for _, dir := range []string{filepath.Join(home, "typo"), file} {
		o.HubDir = dir
		r := Run(o)
		for _, id := range []string{"hubsrv.dir", "hubsrv.db", "hubsrv.password", "hubsrv.vapid", "hubsrv.acme", "hubsrv.autostart"} {
			if c := find(t, r, id); c.Status != Skip || !strings.Contains(c.Detail, dir) {
				t.Fatalf("没要求时 %s 应 skip: %+v", id, c)
			}
		}
	}
}

// hub 上次没正常退出（WAL 里有没检查点的数据）且 -shm 被删了：immutable 只读得到最后一次检查点的数据，
// 从没检查点的库会被误报成“没有 schema_version 表”、只写在 WAL 里的密码会被误报成“还没设”。这时报 warn 叫人先起一次 hub，
// 读库内容的几项跳过，目录原封不动。
func TestHubDBUncheckpointedWALWithoutShm(t *testing.T) {
	o, home := fakeOpts(t)
	src := filepath.Join(home, "src")
	if err := os.MkdirAll(src, 0o700); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(src, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SetSetting("password_hash", "$argon2id$x"); err != nil {
		t.Fatal(err)
	}
	// store 还开着：数据都在 -wal 里，没检查点。只拷 hub.db 与 -wal，模拟崩溃后 -shm 被删
	hubDir := filepath.Join(home, ".railse-hub")
	if err := os.MkdirAll(hubDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"hub.db", "hub.db-wal"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(hubDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if fi, err := os.Stat(filepath.Join(hubDir, "hub.db-wal")); err != nil || fi.Size() == 0 {
		t.Fatalf("前提：-wal 应非空: %v %v", fi, err)
	}
	o.HubDir = hubDir
	before := snapshot(t, hubDir)
	time.Sleep(20 * time.Millisecond)
	r := Run(o)
	if c := find(t, r, "hubsrv.db"); c.Status != Warn || !strings.Contains(c.Detail, "WAL 里有未检查点的数据") || !strings.Contains(c.Hint, "railse hub run") {
		t.Fatalf("hubsrv.db 应 warn: %+v", c)
	}
	for _, id := range []string{"hubsrv.password", "hubsrv.vapid"} {
		if c := find(t, r, id); c.Status != Skip {
			t.Fatalf("%s 读的是库内容，应 skip: %+v", id, c)
		}
	}
	after := snapshot(t, hubDir)
	if names(after) != names(before) {
		t.Fatalf("doctor 多建了文件：前 %s，后 %s", names(before), names(after))
	}
	for name, b := range before {
		if a := after[name]; a != b {
			t.Fatalf("%s 变了：前 %+v，后 %+v", name, b, a)
		}
	}
}

// project.root：项目目录会被守护进程当成哪个项目根，排在钩子检查之后。git 仓库 ok（细节是仓库根）；不是 git 仓库时
// 按目录本身当项目根：显式给的（--project）warn 并建议 git init，隐式默认的当前目录只告知（skip，不建议 git init）；
// 不是家目录下的普通目录与在系统目录里的没有项目根，显式给的 warn，隐式的只告知；
// 家目录、它的上级、根目录与不存在的目录不当项目根，skip 并说明写入都算项目外（显式与否一样）；没给 Project 时 skip。
func TestProjectRootCheck(t *testing.T) {
	o, home := fakeOpts(t)
	t.Setenv("HOME", home) // 守护进程按 $HOME 认家目录
	resolved := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
	repo := filepath.Join(home, "repo")
	writeFile(t, filepath.Join(repo, ".git", "HEAD"), []byte("ref: refs/heads/main\n"))
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	plain := filepath.Join(home, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, explicit := range []bool{true, false} {
		o.ProjectExplicit = explicit
		o.Project = filepath.Join(repo, "src")
		r := Run(o)
		if c := find(t, r, "project.root"); c.Status != OK || c.Name != "项目根" || c.Detail != resolved(repo) {
			t.Fatalf("显式 %v，git 仓库: %+v", explicit, c)
		}
		for i, c := range r.Checks {
			if c.ID == "hooks.gemini" && (i+1 == len(r.Checks) || r.Checks[i+1].ID != "project.root") {
				t.Fatalf("project.root 应紧跟在钩子检查之后: %+v", r.Checks)
			}
		}
		for _, tc := range []struct{ dir, why string }{
			{home, "是家目录"}, {filepath.Dir(home), "它的上级"}, {"/", "根目录"}, {filepath.Join(home, "gone"), "不是存在的目录"},
		} {
			o.Project = tc.dir
			if c := find(t, Run(o), "project.root"); c.Status != Skip || !strings.HasPrefix(c.Detail, tc.dir+" ") || !strings.Contains(c.Detail, tc.why) ||
				!strings.Contains(c.Detail, "不当项目根") || !strings.Contains(c.Detail, "所有写入都算项目外") {
				t.Fatalf("显式 %v，%s: %+v", explicit, tc.dir, c)
			}
		}
	}

	// 显式给的非 git 目录：warn，建议 git init
	o.Project, o.ProjectExplicit = plain, true
	if c := find(t, Run(o), "project.root"); c.Status != Warn || c.Detail != "目录 "+resolved(plain)+" 不是 git 仓库，按当前目录当项目根" ||
		c.Hint != "建议在 "+resolved(plain)+" 里 git init（项目根决定哪些写入算项目内）" {
		t.Fatalf("显式的非 git 目录: %+v", c)
	}
	// 隐式默认的当前目录（没给 --project，如在 ~/Downloads 里跑 railse setup）不是 git 仓库：只告知，不建议 git init
	o.ProjectExplicit = false
	if c := find(t, Run(o), "project.root"); c.Status != Skip || c.Detail != "目录 "+resolved(plain)+" 不是 git 仓库，在这里起 agent 会按当前目录当项目根" ||
		c.Hint != "" {
		t.Fatalf("隐式的非 git 目录应只告知: %+v", c)
	}
	o.ProjectExplicit = true

	// 不是家目录下的普通目录（点目录里、家目录之外）的非 git 目录：没有项目根。显式给的 warn，提示 git init 或换到项目目录；
	// 隐式默认的当前目录只告知（skip），细节相同、不给提示
	dot := filepath.Join(home, ".config", "x")
	outside := filepath.Join(filepath.Dir(home), "outside") // 临时目录里，不算系统目录
	for _, d := range []string{dot, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []string{dot, outside} {
		want := "目录 " + resolved(d) + " 不在家目录下的普通目录里，不按项目根处理"
		o.Project, o.ProjectExplicit = d, true
		if c := find(t, Run(o), "project.root"); c.Status != Warn || c.Detail != want || c.Hint != "要自动放行请 git init 或换到项目目录" {
			t.Fatalf("显式的 %s: %+v", d, c)
		}
		o.ProjectExplicit = false
		if c := find(t, Run(o), "project.root"); c.Status != Skip || c.Detail != want || c.Hint != "" {
			t.Fatalf("隐式的 %s 应只告知: %+v", d, c)
		}
	}
	// 系统目录里的目录（/etc；/opt/homebrew 这类 git 仓库也一样，有就测）：没有项目根，与上面同一口径，细节说清是系统目录，
	// 显式给的提示换到系统目录之外。只 stat
	sysDirs := []string{"/etc"}
	if _, err := os.Stat("/opt/homebrew/.git"); err == nil {
		sysDirs = append(sysDirs, "/opt/homebrew")
	}
	for _, d := range sysDirs {
		want := "目录 " + resolved(d) + " 在系统目录里，不按项目根处理（是 git 仓库也一样）"
		o.Project, o.ProjectExplicit = d, true
		if c := find(t, Run(o), "project.root"); c.Status != Warn || c.Detail != want || c.Hint != "要自动放行请换到系统目录之外的项目目录" {
			t.Fatalf("显式的 %s: %+v", d, c)
		}
		o.ProjectExplicit = false
		if c := find(t, Run(o), "project.root"); c.Status != Skip || c.Detail != want || c.Hint != "" {
			t.Fatalf("隐式的 %s 应只告知: %+v", d, c)
		}
	}
	o.ProjectExplicit = true

	o.Project = ""
	if c := find(t, Run(o), "project.root"); c.Status != Skip {
		t.Fatalf("没给 Project: %+v", c)
	}

	// 相对的 Project 按 doctor 的工作目录取绝对路径再定
	t.Chdir(repo)
	o.Project = "src"
	if c := find(t, Run(o), "project.root"); c.Status != OK || c.Detail != resolved(repo) {
		t.Fatalf("相对的 Project: %+v", c)
	}
}

// 本机 hub 测试里的对外地址与端口。主机在 example.com 下：httptest 的证书认 *.example.com，publicHub 的客户端把它连到本机的测试服务。
const (
	lhHost   = "mac.example.com"
	lhOrigin = "https://" + lhHost
)

// lhServeOn 是 Funnel 已把 https://<lhHost> 转到 127.0.0.1:<port> 时 tailscale serve status --json 的输出（前面混着一行警告）。
func lhServeOn(port int) string {
	return "Warning: client version \"1.80.0\" != tailscaled server version \"1.78.1\"\n" +
		`{"TCP":{"443":{"HTTPS":true}},"Web":{"` + lhHost + `:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:` + fmt.Sprint(port) + `"}}}},"AllowFunnel":{"` + lhHost + `:443":true}}`
}

// localHubOpts 在 fakeOpts 的基础上建 hub 数据目录并写 local-hub.json（content 为空则不写）。
func localHubOpts(t *testing.T, content string) Options {
	t.Helper()
	o, home := fakeOpts(t)
	o.HubDir = filepath.Join(home, ".railse-hub")
	if err := os.MkdirAll(o.HubDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if content != "" {
		if err := os.WriteFile(filepath.Join(o.HubDir, "local-hub.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return o
}

// publicHub 起一个 TLS 的测试服务充当对外地址：返回的客户端不论 URL 里是什么主机都连到它（不做 DNS、不出网），
// 证书照常校验（httptest 的证书认 *.example.com）。h 为 nil 时在 /api/version 答 hub 的版本信息。hits 给出收到的“主机+路径”。
func publicHub(t *testing.T, h http.HandlerFunc) (c *http.Client, hits func() []string) {
	t.Helper()
	if h == nil {
		h = func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/version" {
				http.NotFound(w, r)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"version": "1.2.3", "agent_proto": wire.Proto})
		}
	}
	var mu sync.Mutex
	var got []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Host+r.URL.Path)
		mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	c = srv.Client()
	tr := c.Transport.(*http.Transport).Clone()
	addr := srv.Listener.Addr().String()
	tr.Proxy = nil
	tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	c.Transport = tr
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// fakeServe 让 TailscalePath 给出假路径，TailscaleRun 只认 `<假路径> serve status --json` 并答 out（err 非空时连同输出一起报错），
// 别的调用判测试失败；tailscale 不经 RunCmd 跑（RunCmd 被调到也判失败）。返回 TailscaleRun 的调用次数。
func fakeServe(t *testing.T, o *Options, out string, err error) *int {
	t.Helper()
	const ts = "/fake/bin/tailscale"
	n := new(int)
	o.TailscalePath = func() (string, error) { return ts, nil }
	o.RunCmd = func(name string, args ...string) (string, error) {
		t.Errorf("tailscale 不该经 RunCmd 跑: %s %q", name, args)
		return "", errors.New("不该跑")
	}
	o.TailscaleRun = func(ctx context.Context, name string, args ...string) (string, error) {
		if ctx == nil || name != ts || strings.Join(args, " ") != "serve status --json" {
			t.Errorf("不该跑 %s %q", name, args)
			return "", errors.New("不该跑")
		}
		*n++
		return out, err
	}
	return n
}

// 没有 local-hub.json：不是 setup all --local-hub 装的（或还没装完），skip，不找、不跑 tailscale，也不发请求。
func TestLocalHubNoFileSkips(t *testing.T) {
	o := localHubOpts(t, "")
	o.TailscalePath = func() (string, error) { t.Fatal("不该找 tailscale"); return "", nil }
	o.TailscaleRun = func(_ context.Context, name string, args ...string) (string, error) {
		t.Fatalf("不该跑 %s %q", name, args)
		return "", nil
	}
	o.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("不该请求 %s", r.URL)
		return nil, nil
	})}
	c := find(t, Run(o), "hubsrv.local")
	if c.Status != Skip || !strings.Contains(c.Detail, "没有 local-hub.json") || !strings.Contains(c.Detail, "setup all --local-hub") {
		t.Fatalf("%+v", c)
	}
	if c.Name != "本机 hub 对外地址" {
		t.Fatalf("名称: %+v", c)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Tailscale 提供的对外地址：serve status 说 Funnel 把 <主机>:443 转到 127.0.0.1:<端口>，从本机 GET <对外地址>/api/version 答得上 → ok。
func TestLocalHubTailscaleOK(t *testing.T) {
	o := localHubOpts(t, `{"port":8787,"public_url":"`+lhOrigin+`","provider":"tailscale"}`)
	ran := fakeServe(t, &o, lhServeOn(8787), nil)
	var hits func() []string
	o.HTTP, hits = publicHub(t, nil)
	c := find(t, Run(o), "hubsrv.local")
	if c.Status != OK || c.Detail != lhOrigin+"，hub 版本 1.2.3" {
		t.Fatalf("%+v", c)
	}
	if *ran != 1 {
		t.Fatalf("serve status 应跑一次，跑了 %d 次", *ran)
	}
	if h := hits(); len(h) != 1 || h[0] != lhHost+"/api/version" {
		t.Fatalf("应 GET 一次对外地址的 /api/version: %q", h)
	}
}

// Funnel 没把对外地址转到这个端口（没有 serve 配置、转到别的端口、没开 Funnel）：fail，提示 tailscale funnel --bg <端口>
// 或重跑 setup（带上 hub 数据目录）；不再请求对外地址。
func TestLocalHubFunnelOffFails(t *testing.T) {
	o := localHubOpts(t, `{"port":8787,"public_url":"`+lhOrigin+`/","provider":"tailscale"}`)
	var hits func() []string
	o.HTTP, hits = publicHub(t, nil)
	noFunnel := `{"Web":{"` + lhHost + `:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8787"}}}}}`
	for _, out := range []string{"No serve config\n", "", lhServeOn(8788), noFunnel} {
		fakeServe(t, &o, out, nil)
		c := find(t, Run(o), "hubsrv.local")
		if c.Status != Fail || c.Detail != "Funnel 没把 "+lhHost+" 转到 127.0.0.1:8787" ||
			!strings.Contains(c.Hint, "tailscale funnel --bg 8787") || !strings.Contains(c.Hint, "railse setup all --local-hub --hub-dir "+o.HubDir) {
			t.Fatalf("%q: %+v", out, c)
		}
	}
	if h := hits(); len(h) != 0 {
		t.Fatalf("Funnel 没开时不该再请求对外地址: %q", h)
	}
}

// 找不到 tailscale、serve status 出错、输出是坏 JSON：fail，细节带原因与输出摘要。
func TestLocalHubTailscaleErrors(t *testing.T) {
	o := localHubOpts(t, `{"port":8787,"public_url":"`+lhOrigin+`","provider":"tailscale"}`)
	var hits func() []string
	o.HTTP, hits = publicHub(t, nil)
	o.TailscalePath = func() (string, error) { return "", errors.New("没有找到 tailscale") }
	if c := find(t, Run(o), "hubsrv.local"); c.Status != Fail || !strings.Contains(c.Detail, "找不到 tailscale") || c.Hint == "" {
		t.Fatalf("找不到 tailscale: %+v", c)
	}
	// 摘要跳过 Warning: 开头的行（版本不一致之类），取真正的报错
	fakeServe(t, &o, "Warning: client version \"1.80.0\" != tailscaled server version \"1.78.1\"\n\nfailed to connect to local tailscaled; it doesn't appear to be running\n", errors.New("exit status 1"))
	if c := find(t, Run(o), "hubsrv.local"); c.Status != Fail || !strings.Contains(c.Detail, "serve status --json 失败：exit status 1") ||
		!strings.Contains(c.Detail, "输出：failed to connect to local tailscaled") || strings.Contains(c.Detail, "Warning") || !strings.Contains(c.Hint, "tailscale status") {
		t.Fatalf("serve status 出错: %+v", c)
	}
	fakeServe(t, &o, `{"Web": oops}`, nil)
	if c := find(t, Run(o), "hubsrv.local"); c.Status != Fail || !strings.Contains(c.Detail, "不是 JSON") || c.Hint == "" {
		t.Fatalf("坏 JSON: %+v", c)
	}
	if h := hits(); len(h) != 0 {
		t.Fatalf("tailscale 那边出错时不该再请求对外地址: %q", h)
	}
}

// Funnel 配置对，但从本机访问不到对外地址（连不上、不是 200、不是 hub 的版本信息）：只 warn，提示可能是本机代理拦截了、用手机 4G 验证。
func TestLocalHubUnreachableWarns(t *testing.T) {
	o := localHubOpts(t, `{"port":8787,"public_url":"`+lhOrigin+`","provider":"tailscale"}`)
	fakeServe(t, &o, lhServeOn(8787), nil)
	// 连不上：TLS 握手失败（本机代理拦截 *.ts.net 时常见）
	o.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return nil, errors.New("remote error: tls: handshake failure")
	})}
	c := find(t, Run(o), "hubsrv.local")
	if c.Status != Warn || !strings.Contains(c.Detail, lhOrigin+"/api/version") || !strings.Contains(c.Detail, "handshake failure") ||
		!strings.Contains(c.Hint, "代理") || !strings.Contains(c.Hint, "4G") {
		t.Fatalf("连不上: %+v", c)
	}
	for name, h := range map[string]http.HandlerFunc{
		"502":        func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"不是 hub":     func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>hello</html>")) },
		"没有 version": func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) },
	} {
		o.HTTP, _ = publicHub(t, h)
		if c := find(t, Run(o), "hubsrv.local"); c.Status != Warn || !strings.Contains(c.Detail, lhOrigin+"/api/version") || !strings.Contains(c.Hint, "代理") {
			t.Fatalf("%s: %+v", name, c)
		}
	}
}

// 自己的隧道（provider custom，或没写 provider）：不找、不跑 tailscale，只从本机 GET 一次对外地址。
func TestLocalHubCustomSkipsTailscale(t *testing.T) {
	for _, provider := range []string{`,"provider":"custom"`, ""} {
		o := localHubOpts(t, `{"port":8787,"public_url":"https://hub.example.com:8443"`+provider+`}`)
		o.TailscalePath = func() (string, error) { t.Fatal("不该找 tailscale"); return "", nil }
		o.TailscaleRun = func(_ context.Context, name string, args ...string) (string, error) {
			t.Fatalf("不该跑 %s %q", name, args)
			return "", nil
		}
		var hits func() []string
		o.HTTP, hits = publicHub(t, nil)
		if c := find(t, Run(o), "hubsrv.local"); c.Status != OK || c.Detail != "https://hub.example.com:8443，hub 版本 1.2.3" {
			t.Fatalf("%q: %+v", provider, c)
		}
		if h := hits(); len(h) != 1 || h[0] != "hub.example.com:8443/api/version" {
			t.Fatalf("%q: 应 GET 一次对外地址: %q", provider, h)
		}
		// 自己的隧道访问不到同样只 warn
		o.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) { return nil, errors.New("connection refused") })}
		if c := find(t, Run(o), "hubsrv.local"); c.Status != Warn || !strings.Contains(c.Hint, "代理") {
			t.Fatalf("%q: 访问不到: %+v", provider, c)
		}
	}
}

// sudo 下（euid 是 root，或家目录不归当前用户）Tailscale 提供的整项 skip：不跑 tailscale，也不发请求。
func TestLocalHubUnderSudoSkips(t *testing.T) {
	o := localHubOpts(t, `{"port":8787,"public_url":"`+lhOrigin+`","provider":"tailscale"}`)
	o.TailscalePath = func() (string, error) { t.Fatal("不该找 tailscale"); return "", nil }
	o.TailscaleRun = func(_ context.Context, name string, args ...string) (string, error) {
		t.Fatalf("不该跑 %s %q", name, args)
		return "", nil
	}
	var hits func() []string
	o.HTTP, hits = publicHub(t, nil)
	for name, set := range map[string]func(){
		"euid 是 root": func() { o.Geteuid = func() int { return 0 } },
		"家目录归别的用户": func() {
			o.Geteuid = func() int { return 1000 }
			o.FileOwner = func(string) (int, bool) { return 1001, true }
		},
	} {
		set()
		if c := find(t, Run(o), "hubsrv.local"); c.Status != Skip || c.Detail != "sudo 下不跑 tailscale" {
			t.Fatalf("%s: %+v", name, c)
		}
	}
	if h := hits(); len(h) != 0 {
		t.Fatalf("sudo 下不该请求对外地址: %q", h)
	}
}

// local-hub.json 坏了（不是 JSON、端口不对、对外地址不合规）：fail，提示重跑 setup all --local-hub（带上 hub 数据目录）；
// 细节只说哪一项不合规，不带文件内容与地址原文（里面可能有密码）；不碰 tailscale、不发请求。
func TestLocalHubBrokenFileFails(t *testing.T) {
	const secret = "hunter2-secret"
	// custom：文件解得开且不是 Tailscale 提供的（provider 空当 custom），提示里要说再带上 --public-url
	for _, tc := range []struct {
		content, want string
		custom        bool
	}{
		{`{"port":8787,"public_url":"https://u:` + secret + `@x.example.com"`, "不是合法 JSON", false},
		{`{"port":"8787","public_url":"` + lhOrigin + `","provider":"custom"}`, "不是合法 JSON", false},
		{`{"port":0,"public_url":"` + lhOrigin + `"}`, "port 0 不在 1..65535", true},
		{`{"port":70000,"public_url":"` + lhOrigin + `","provider":"custom"}`, "port 70000 不在 1..65535", true},
		{`{"port":70000,"public_url":"` + lhOrigin + `","provider":"tailscale"}`, "port 70000 不在 1..65535", false},
		{`{"port":8787,"public_url":"https://u:` + secret + `@x.example.com","provider":"tailscale"}`, "public_url 不合规", false},
		{`{"port":8787,"public_url":"http://x.example.com/` + secret + `"}`, "public_url 不合规", true},
		{`{"port":8787,"public_url":"https://x.example.com/` + secret + `"}`, "public_url 不合规", true},
		{`{"port":8787,"public_url":"https://x.example.com/?` + secret + `"}`, "public_url 不合规", true},
		{`{"port":8787,"public_url":"https://x.example.com#` + secret + `"}`, "public_url 不合规", true},
		{`{"port":8787,"public_url":"` + secret + `"}`, "public_url 不合规", true},
		{`{"port":8787}`, "public_url 不合规", true},
	} {
		o := localHubOpts(t, tc.content)
		o.TailscalePath = func() (string, error) { t.Fatal("不该找 tailscale"); return "", nil }
		o.TailscaleRun = func(_ context.Context, name string, args ...string) (string, error) {
			t.Fatalf("不该跑 %s %q", name, args)
			return "", nil
		}
		o.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			t.Fatalf("不该请求 %s", r.URL)
			return nil, nil
		})}
		r := Run(o)
		c := find(t, r, "hubsrv.local")
		if c.Status != Fail || !strings.Contains(c.Detail, tc.want) || !strings.Contains(c.Detail, "local-hub.json") ||
			!strings.Contains(c.Hint, "railse setup all --local-hub --hub-dir "+o.HubDir+" 重写它") ||
			strings.Contains(c.Hint, "--public-url") != tc.custom {
			t.Fatalf("%s: %+v", tc.content, c)
		}
		if j, _ := r.JSON(); strings.Contains(string(j), secret) {
			t.Fatalf("%s: 报告带出了文件内容: %+v", tc.content, c)
		}
	}
}

// 守护进程不在 ~/.railse（--dir 的不是默认目录）时，重跑 setup 的命令带上 --dir。
func TestLocalHubRerunHintCarriesDir(t *testing.T) {
	o := localHubOpts(t, `{"port":0,"public_url":"`+lhOrigin+`","provider":"tailscale"}`)
	if c := find(t, Run(o), "hubsrv.local"); strings.Contains(c.Hint, "--dir") {
		t.Fatalf("默认目录不该带 --dir: %+v", c)
	}
	o.DefaultSocket = "/elsewhere/.railse/agent.sock"
	if c := find(t, Run(o), "hubsrv.local"); !strings.Contains(c.Hint, "railse setup all --local-hub --hub-dir "+o.HubDir+" --dir "+o.AgentDir+" 重写它") {
		t.Fatalf("坏文件: %+v", c)
	}
	writeFile(t, filepath.Join(o.HubDir, "local-hub.json"), []byte(`{"port":8787,"public_url":"`+lhOrigin+`","provider":"tailscale"}`))
	fakeServe(t, &o, "No serve config\n", nil)
	if c := find(t, Run(o), "hubsrv.local"); c.Status != Fail || !strings.Contains(c.Hint, "或重跑 railse setup all --local-hub --hub-dir "+o.HubDir+" --dir "+o.AgentDir) {
		t.Fatalf("Funnel 没开: %+v", c)
	}
}

// hub 数据目录用不了或没有时，hubsrv.local 与其余依赖数据目录的几项一样按同一个原因 skip。
func TestLocalHubFollowsHubDir(t *testing.T) {
	o, home := fakeOpts(t)
	o.HubDir = ""
	if c := find(t, Run(o), "hubsrv.local"); c.Status != Skip {
		t.Fatalf("没有 HubDir: %+v", c)
	}
	o.HubDir, o.HubDirRequired = filepath.Join(home, "typo"), true
	if c := find(t, Run(o), "hubsrv.local"); c.Status != Skip || c.Detail != "hub 数据目录 "+o.HubDir+" 不存在" {
		t.Fatalf("hub 数据目录不存在: %+v", c)
	}
	// 顺序：在 ACME 证书目录之后、hub 开机自启之前
	var ids []string
	for _, c := range Run(o).Checks {
		if strings.HasPrefix(c.ID, "hubsrv.") {
			ids = append(ids, c.ID)
		}
	}
	if got := strings.Join(ids, " "); got != "hubsrv.dir hubsrv.db hubsrv.password hubsrv.vapid hubsrv.acme hubsrv.local hubsrv.autostart" {
		t.Fatalf("hub 侧的顺序: %s", got)
	}
}
