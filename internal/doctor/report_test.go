package doctor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/hooksconf"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/i18n"
	"github.com/xufei547/railse-opc/internal/proto"
)

// hasCJK 报告字符串里有没有中日韩统一表意文字（U+4E00–U+9FFF）、中文标点（U+3000–U+303F）或全角符号（U+FF00–U+FFEF）。
// 与 cmd/railse 测试里的同名函数口径相同。
func hasCJK(s string) bool {
	for _, r := range s {
		if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0xFF00 && r <= 0xFFEF) || (r >= 0x3000 && r <= 0x303F) {
			return true
		}
	}
	return false
}

// 英文的汇总行与“修复：”。汇总行用全动词（passed、warned、failed、skipped），不涉单复数。
// 项名、细节、提示只用英文表里真有的键（或不需翻译的命令），整段输出不得有中文。
func TestReportWriteEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	r := Report{Version: "0.2.0", Checks: []Check{
		{ID: "agent.socket", Name: i18n.T("socket 文件"), Status: OK, Detail: "/home/u/.railse/agent.sock"},
		{ID: "hooks.claude", Name: i18n.T("Claude Code 钩子"), Status: Warn, Detail: fmt.Sprintf(i18n.T("缺 %s"), "PreToolUse"), Hint: "railse setup hooks --claude"},
	}}
	var b bytes.Buffer
	r.Write(&b)
	got := b.String()
	for _, want := range []string{"[ok]", "[warn]", "Socket file: ", "missing PreToolUse", "        Fix: railse setup hooks --claude\n", "1 passed, 1 warned, 0 failed, 0 skipped (railse 0.2.0)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q：\n%s", want, got)
		}
	}
	if hasCJK(got) {
		t.Fatalf("英文体检输出里有中文：\n%s", got)
	}
	if !strings.Contains(got, "Claude Code hooks: ") {
		t.Fatalf("项名与细节之间应是英文冒号：\n%s", got)
	}
}

// checkEnglish 把报告按文本与 JSON 各输出一遍：都不带中文；每个 warn、fail 的修复提示都印出来（前缀 Fix:）；汇总行是英文。
func checkEnglish(t *testing.T, r Report) string {
	t.Helper()
	var b bytes.Buffer
	r.Write(&b)
	text := b.String()
	j, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if hasCJK(text) || hasCJK(string(j)) {
		t.Fatalf("英文体检里有中文:\n%s\n%s", text, j)
	}
	if !strings.Contains(text, " passed, ") || !strings.Contains(text, " skipped (railse ") {
		t.Fatalf("汇总行不是英文:\n%s", text)
	}
	for _, c := range r.Checks {
		if (c.Status == Fail || c.Status == Warn) && (c.Hint == "" || !strings.Contains(text, "        Fix: "+c.Hint+"\n")) {
			t.Fatalf("%s 的修复提示没印出来: %+v\n%s", c.ID, c, text)
		}
	}
	return text
}

// 还没装过的机器（只下了二进制就跑 railse doctor）：多数项 fail、skip 或 warn，项名、细节、修复提示全是英文。
// 问守护进程状态走真实的 internal/hookclient（不打桩）：socket 在临时目录里、不存在，连不上的报错也是英文。
func TestRunEnglishUnconfigured(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	base := t.TempDir()
	home := filepath.Join(base, "home")
	work := filepath.Join(home, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home) // 项目根按 $HOME 认家目录
	opts := func() Options {
		return Options{
			AgentDir: filepath.Join(home, ".railse"), Home: home, GOOS: "darwin", Version: "0.2.0", Project: work,
			HubDir:        filepath.Join(home, ".railse-hub"),
			LookPath:      func(string) (string, error) { return "", errors.New("not found") },
			RunCmd:        func(string, ...string) (string, error) { return "", errors.New("not run in tests") },
			TailscalePath: func() (string, error) { return "", errors.New("not looked up in tests") },
			TailscaleRun: func(context.Context, string, ...string) (string, error) {
				return "", errors.New("not run in tests")
			},
			HTTP:      loopbackOnlyClient(t),
			Geteuid:   func() int { return 1000 },
			FileOwner: func(string) (int, bool) { return 1000, true },
		}
	}

	// 默认：当前目录不是 git 仓库（只告知），默认的 hub 数据目录不在（跳过 hub 侧）
	text := checkEnglish(t, Run(opts()))
	for _, want := range []string{
		"[fail]  Daemon data directory: " + filepath.Join(home, ".railse") + " doesn't exist\n",
		"[fail]  Daemon status: can't connect to the daemon: dial unix ",
		"[skip]  Hub connection: the daemon isn't running\n",
		"[skip]  Time-limited trusts: the daemon isn't running\n",
		"[skip]  Hub reachability: no machine.json (not enrolled with a hub)\n",
		"[fail]  Claude Code hooks: no hooks installed (looked in ",
		"        Fix: railse setup hooks --claude\n",
		"[warn]  Codex notify: ",
		"[skip]  Project root: ",
		"[skip]  Gemini CLI version: gemini isn't in PATH\n",
		"[skip]  Hub data directory: no hub data directory at ",
		"\n0 passed, 2 warned, 6 failed, 15 skipped (railse 0.2.0)\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q:\n%s", want, text)
		}
	}

	// 显式给的项目目录不是 git 仓库（warn），显式给的 hub 数据目录不在（fail，依赖它的几项跳过）
	o := opts()
	o.ProjectExplicit, o.HubDirRequired = true, true
	text = checkEnglish(t, Run(o))
	for _, want := range []string{"[warn]  Project root: ", "        Fix: Run git init in ", "[fail]  Hub data directory: ", "[skip]  Hub database: hub data directory "} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q:\n%s", want, text)
		}
	}

	// 取不到家目录、也没给项目目录：用到家目录的几项跳过
	t.Setenv("HOME", "")
	o = opts()
	o.Home, o.Project, o.HubDir = "", "", ""
	text = checkEnglish(t, Run(o))
	for _, want := range []string{"[skip]  Codex hooks: home directory not found ($HOME isn't set), skipped\n", "[skip]  Project root: no project directory given\n", "[skip]  Hub data directory: this machine has no hub data directory\n"} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q:\n%s", want, text)
		}
	}
}

// 处处有毛病的机器：尽量走到 doctor 自己的每类说法（权限、socket、hub 状态、钩子缺事件与超时、程序路径与 socket 对不上、
// notify 写错位置、CLI 版本、项目根、hub 侧的库版本、密码、推送密钥、ACME、local-hub.json），英文下都不带中文。
// 不碰会冒出范围外中文报错的路径（坏 machine.json、坏钩子 JSON 的解析错误等）。
func TestRunEnglishManyProblems(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	o, home := fakeOpts(t)
	t.Setenv("HOME", home)
	os.Chmod(o.AgentDir, 0o755)
	os.WriteFile(filepath.Join(o.AgentDir, "audit.jsonl"), nil, 0o644)
	os.Chmod(filepath.Join(o.AgentDir, "agent.sock"), 0o644)
	srv := httptest.NewServer(http.NotFoundHandler()) // 不是 hub：/api/version 回 404
	defer srv.Close()
	os.WriteFile(filepath.Join(o.AgentDir, "machine.json"), []byte(`{"hub_url":"`+srv.URL+`","machine_id":"m","secret":"s","name":"mac"}`), 0o600)
	o.AgentStatus = func(string) (proto.Status, error) { return proto.Status{Hub: "none", Rules: 3}, nil }
	o.Project, o.ProjectExplicit, o.Bin = filepath.Join(home, "proj"), true, "/opt/railse"
	os.MkdirAll(o.Project, 0o755)
	// Claude：用户级的钩子指向别的程序、连别的 socket；项目级旧位置也有一份不同的
	other := filepath.Join(home, "other data")
	writeFile(t, hooksconf.ClaudeUserPath(home), planDoc(t, "claude", "/x/railse", filepath.Join(other, "agent.sock")))
	_, shared := hooksconf.ClaudePaths(o.Project)
	writeFile(t, shared, planDoc(t, "claude", "/y/railse"))
	// Codex：钩子文件没有；notify 写在了表下面
	writeFile(t, hooksconf.CodexConfigPath(home), []byte("[tui]\nnotify = [\"/opt/railse\", \"hook\", \"codex-notify\"]\n"))
	// Gemini：项目级的钩子超时不够
	writeFile(t, hooksconf.GeminiPath(o.Project), []byte(`{"hooks":{"BeforeTool":[{"hooks":[{"name":"railse-before-tool","type":"command","command":"/opt/railse hook gemini","timeout":5}]}]}}`))
	o.LookPath = func(name string) (string, error) { return "/bin/" + name, nil }
	o.RunCmd = func(name string, _ ...string) (string, error) {
		switch filepath.Base(name) {
		case "gemini":
			return "0.1.0", nil
		case "codex":
			return "boom", errors.New("exit status 1")
		}
		return "9.9.9", nil
	}
	hubDir := filepath.Join(home, ".railse-hub")
	dbPath := newHubDB(t, hubDir, nil)
	setSchemaVersion(t, dbPath, store.SchemaVersion-1)
	os.Chmod(hubDir, 0o755)
	os.Mkdir(filepath.Join(hubDir, "acme"), 0o700)
	os.WriteFile(filepath.Join(hubDir, "acme", "acme_account+key"), nil, 0o644)
	os.WriteFile(filepath.Join(hubDir, "local-hub.json"), []byte("{"), 0o600)
	o.HubDir = hubDir

	r := Run(o)
	text := checkEnglish(t, r)
	var problems int
	for _, c := range r.Checks {
		if c.Status == Fail || c.Status == Warn {
			problems++
		}
	}
	if problems < 15 {
		t.Fatalf("场景不够坏，只有 %d 项问题:\n%s", problems, text)
	}
	for _, want := range []string{
		"has mode 0755, should be 0700", "not 0600: audit.jsonl(0644)", "has mode 0644, should be 0600",
		"Hub connection: the daemon isn't connected to a hub, but machine.json exists", " returned 404\n",
		"the hooks point to /x/railse, but this program is /opt/railse", "the hooks connect to ", "add --dir ",
		"the notify in ", " is under [tui], where Codex can't see it", "timeout too short: BeforeTool (timeout 5, needs ≥ ",
		"hook format was recorded with ", "--version failed: exit status 1; output: boom", "is older than 0.65.0",
		"[warn]  Project root: ", "database version ", "no password yet", "VAPID keys not generated yet",
		"private key files in it aren't 0600: acme_account+key(0644)", "isn't valid JSON",
		"so they connect to the daemon in ", "to railse doctor — Delete the Railse hook entries in ",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("缺 %q:\n%s", want, text)
		}
	}
}
