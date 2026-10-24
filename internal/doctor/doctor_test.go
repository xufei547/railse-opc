package doctor

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/autostart"
	"github.com/railse/railse-opc/internal/hooksconf"
	"github.com/railse/railse-opc/internal/hub/push"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/wire"
	"github.com/railse/railse-opc/internal/proto"
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
		LookPath:    func(string) (string, error) { return "", errors.New("not found") },
		RunCmd:      func(string, ...string) (string, error) { return "", errors.New("不该跑") },
		AgentStatus: func(string) (proto.Status, error) { return proto.Status{Rules: 2, Hub: "none"}, nil },
	}, home
}

func TestAgentChecksHappyAndPermissions(t *testing.T) {
	o, _ := fakeOpts(t)
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
	if c := find(t, r, "hooks.claude"); c.Status != Skip {
		t.Fatalf("没给 Project 应 skip: %+v", c)
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
		}
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

// writeClaude 写一份由 hooksconf 生成的配置，返回内容。
func planDoc(t *testing.T, cli, bin string) []byte {
	t.Helper()
	doc, err := hooksconf.Merge(nil, bin, hooksconf.Plan(cli, bin))
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
	// 两处都装了
	writeFile(t, local, doc)
	writeFile(t, shared, doc)
	if c := find(t, Run(o), "hooks.claude"); c.Status != Warn || !strings.Contains(c.Detail, "两次") || !strings.Contains(c.Hint, "setup hooks") {
		t.Fatalf("两处都装: %+v", c)
	}
	os.Remove(shared)
	// 指向别的程序
	o.Bin = "/opt/new/railse"
	if c := find(t, Run(o), "hooks.claude"); c.Status != Warn || !strings.Contains(c.Detail, "/usr/local/bin/railse") || !strings.Contains(c.Detail, "/opt/new/railse") {
		t.Fatalf("程序路径不同: %+v", c)
	}
	o.Bin = bin
	// codex notify
	if c := find(t, Run(o), "hooks.codex_notify"); c.Status != Warn || c.Hint == "" {
		t.Fatalf("没有 notify: %+v", c)
	}
	cfg := filepath.Join(home, ".codex", "config.toml")
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
	if c := find(t, Run(o), "hooks.codex_notify"); c.Status != Warn || !strings.Contains(c.Detail, "[mcp_servers.foo]") {
		t.Fatalf("notify 在表下面: %+v", c)
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
	if c := find(t, r, "hubsrv.db"); c.Status != Fail || c.Hint == "" {
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
		s = append(s, k)
	}
	sort.Strings(s)
	return strings.Join(s, ",")
}

// hub 没在跑（WAL 已检查点、没有 -wal/-shm）：doctor 前后目录里的文件一个不多，大小、权限、mtime 都不变。
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

// hub 在跑（-wal 存在）：按普通只读打开，读得到还在 WAL 里、没检查点的数据，也不多建文件。
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
	if after["hub.db"] != before["hub.db"] {
		t.Fatalf("hub.db 变了：前 %+v，后 %+v", before["hub.db"], after["hub.db"])
	}
	// hub 还能照常写
	if err := st.SetSetting("x", "y"); err != nil {
		t.Fatal(err)
	}
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
