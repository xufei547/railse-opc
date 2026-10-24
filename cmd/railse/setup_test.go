package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hooksconf"
)

func TestSetupHooksWritesAllThree(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	project := filepath.Join(dir, "proj")
	os.MkdirAll(project, 0o755)
	codexFile := filepath.Join(dir, "codex", "hooks.json")
	var out, errOut bytes.Buffer
	code := run([]string{"setup", "hooks", "--project", project, "--codex-file", codexFile, "--bin", "/opt/railse"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	claude := readJSON(t, filepath.Join(project, ".claude", "settings.local.json"))
	pr := claude["hooks"].(map[string]any)["PermissionRequest"].([]any)[0].(map[string]any)
	hook := pr["hooks"].([]any)[0].(map[string]any)
	if pr["matcher"] != "" || hook["command"] != "/opt/railse hook claude" || hook["timeout"] != float64(86400) || hook["type"] != "command" {
		t.Fatalf("Claude PermissionRequest: %+v", pr)
	}
	for _, ev := range []string{"Notification", "Stop", "SessionEnd"} {
		if _, ok := claude["hooks"].(map[string]any)[ev]; !ok {
			t.Fatalf("Claude 缺 %s", ev)
		}
	}
	codex := readJSON(t, codexFile)
	se := codex["hooks"].(map[string]any)["SessionEnd"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if se["timeout"] != float64(3) || se["command"] != "/opt/railse hook codex" {
		t.Fatalf("Codex SessionEnd: %+v", se)
	}
	gemini := readJSON(t, filepath.Join(project, ".gemini", "settings.json"))
	bt := gemini["hooks"].(map[string]any)["BeforeTool"].([]any)[0].(map[string]any)
	bh := bt["hooks"].([]any)[0].(map[string]any)
	if bt["matcher"] != ".*" || bh["name"] != "railse-before-tool" || bh["timeout"] != float64(86400000) || bh["command"] != "/opt/railse hook gemini" {
		t.Fatalf("Gemini BeforeTool: %+v", bt)
	}
	if !strings.Contains(out.String(), "Codex") || !strings.Contains(out.String(), "Gemini") || !strings.Contains(out.String(), "yolo") {
		t.Fatalf("应打印提醒: %s", out.String())
	}
	if !strings.Contains(out.String(), `notify = ["/opt/railse", "hook", "codex-notify"]`) {
		t.Fatalf("Codex 提醒应给出 TOML 数组形式的 notify: %s", out.String())
	}
}

func TestSetupHooksIsIdempotentAndPreservesOthers(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	project := filepath.Join(dir, "proj")
	os.MkdirAll(filepath.Join(project, ".claude"), 0o755)
	existing := `{"permissions":{"defaultMode":"default"},"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"/other/tool"}]}],"PreToolUse":[{"hooks":[{"type":"command","command":"echo hi"}]}]}}`
	os.WriteFile(filepath.Join(project, ".claude", "settings.local.json"), []byte(existing), 0o644)
	args := []string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	first, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.local.json"))
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	second, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.local.json"))
	if !bytes.Equal(first, second) {
		t.Fatalf("两次运行结果应相同:\n%s\n---\n%s", first, second)
	}
	doc := readJSON(t, filepath.Join(project, ".claude", "settings.local.json"))
	if doc["permissions"] == nil {
		t.Fatal("其他顶层字段应保留")
	}
	hooks := doc["hooks"].(map[string]any)
	if len(hooks["PermissionRequest"].([]any)) != 2 {
		t.Fatalf("别人的 PermissionRequest 钩子应保留且只加一组: %+v", hooks["PermissionRequest"])
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Fatal("别人的 PreToolUse 应保留")
	}
	if _, err := os.Stat(filepath.Join(project, ".gemini", "settings.json")); err == nil {
		t.Fatal("只选 --claude 时不应写 Gemini")
	}
}

func TestSetupHooksDryRunWritesNothing(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	project := filepath.Join(dir, "proj")
	os.MkdirAll(project, 0o755)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--codex-file", filepath.Join(dir, "c.json"), "--bin", "/opt/railse", "--dry-run"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".claude")); err == nil {
		t.Fatal("dry-run 不应写文件")
	}
	if !strings.Contains(out.String(), "PermissionRequest") {
		t.Fatalf("dry-run 应打印将写入的内容: %s", out.String())
	}
}

func TestSetupHooksRejectsBadExisting(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	project := filepath.Join(dir, "proj")
	os.MkdirAll(filepath.Join(project, ".claude"), 0o755)
	os.WriteFile(filepath.Join(project, ".claude", "settings.local.json"), []byte("{not json"), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 1 {
		t.Fatalf("坏的现有文件应退出 1 并不覆盖, 得到 %d", code)
	}
	b, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.local.json"))
	if string(b) != "{not json" {
		t.Fatal("坏文件不应被覆盖")
	}
}

func TestSetupHooksRejectsMalformedExisting(t *testing.T) {
	// 合法 JSON 但形状不对（顶层或 hooks 不是对象、事件不是数组）同样视为损坏：退出 1 且不覆盖
	for _, existing := range []string{
		`{"hooks":[]}`,
		`{"hooks":"x"}`,
		`{"hooks":null}`,
		`{"hooks":{"Stop":"x"}}`,
		`[]`,
		`null`,
		`{} {}`,
	} {
		project := t.TempDir()
		path := filepath.Join(project, ".claude", "settings.local.json")
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(existing), 0o644)
		var out, errOut bytes.Buffer
		if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 1 {
			t.Errorf("%s: 想要退出 1, 得到 %d", existing, code)
		}
		if b, _ := os.ReadFile(path); string(b) != existing {
			t.Errorf("%s: 不应被覆盖, 现在是 %s", existing, b)
		}
	}
}

func TestSetupHooksBadFileStopsAllWrites(t *testing.T) {
	// 任何一个目标文件坏了，三家一个都不写
	project := t.TempDir()
	os.MkdirAll(filepath.Join(project, ".gemini"), 0o755)
	os.WriteFile(filepath.Join(project, ".gemini", "settings.json"), []byte("{not json"), 0o644)
	codexFile := filepath.Join(t.TempDir(), "hooks.json")
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--codex-file", codexFile, "--bin", "/opt/railse"}, &out, &errOut); code != 1 {
		t.Fatalf("想要退出 1, 得到 %d", code)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude")); err == nil {
		t.Fatal("Gemini 配置坏了时不应写 Claude")
	}
	if _, err := os.Stat(codexFile); err == nil {
		t.Fatal("Gemini 配置坏了时不应写 Codex")
	}
}

func TestSetupHooksKeepsModeAndLeavesNoTemp(t *testing.T) {
	project := t.TempDir()
	claudeDir := filepath.Join(project, ".claude")
	os.MkdirAll(claudeDir, 0o755)
	path := filepath.Join(claudeDir, "settings.local.json")
	os.WriteFile(path, []byte(`{}`), 0o600)
	os.Chmod(path, 0o600)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--gemini", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("原文件权限应保留 0600, 得到 %o", st.Mode().Perm())
	}
	if st, _ := os.Stat(filepath.Join(project, ".gemini", "settings.json")); st.Mode().Perm() != 0o644 {
		t.Fatalf("新文件权限应为 0644, 得到 %o", st.Mode().Perm())
	}
	if ents, _ := os.ReadDir(claudeDir); len(ents) != 1 {
		t.Fatalf("目录里不应留下临时文件: %v", ents)
	}
}

func TestSetupHooksReplacesOldBinAndQuotesSpaces(t *testing.T) {
	project := t.TempDir()
	binDir := filepath.Join(t.TempDir(), "my apps")
	os.MkdirAll(binDir, 0o755)
	bin := filepath.Join(binDir, "railse")
	os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\"\n"), 0o755)
	// 先用旧路径装一次，再换到含空格的新路径装两次
	for _, b := range []string{"/old/place/railse", bin, bin} {
		var out, errOut bytes.Buffer
		if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", b}, &out, &errOut); code != 0 {
			t.Fatal(errOut.String())
		}
	}
	hooks := readJSON(t, filepath.Join(project, ".claude", "settings.local.json"))["hooks"].(map[string]any)
	for _, ev := range []string{"PermissionRequest", "Notification", "Stop", "SessionEnd"} {
		if n := len(hooks[ev].([]any)); n != 1 {
			t.Fatalf("%s 应只剩一组（旧路径的条目要被替换）, 得到 %d: %+v", ev, n, hooks[ev])
		}
	}
	needUnixShell(t)
	cmd := hooks["PermissionRequest"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
	got, err := exec.Command("sh", "-c", cmd).Output()
	if err != nil || string(got) != "hook claude\n" {
		t.Fatalf("命令 %q 应能被 shell 正确执行, 得到 %q err=%v", cmd, got, err)
	}
}

func TestSetupHooksKeepsOthersVerbatim(t *testing.T) {
	project := t.TempDir()
	path := filepath.Join(project, ".claude", "settings.local.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	existing := `{"big":12345678901234567890,"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"npm test && echo ok > /tmp/x"}]}]}}`
	os.WriteFile(path, []byte(existing), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	b, _ := os.ReadFile(path)
	for _, want := range []string{`12345678901234567890`, `npm test && echo ok > /tmp/x`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("别人的内容应原样保留 %s:\n%s", want, b)
		}
	}
}

func TestSetupHooksRejectsBadArgs(t *testing.T) {
	tmp := t.TempDir()
	for _, args := range [][]string{
		{"setup"},
		{"setup", "nope"},
		{"setup", "hooks", "--project", tmp, "--codex-file", filepath.Join(tmp, "c.json"), "--bin", "/opt/railse", "extra"},
	} {
		var out, errOut bytes.Buffer
		if code := run(args, &out, &errOut); code != 2 {
			t.Errorf("%v: 想要退出 2, 得到 %d", args, code)
		}
	}
	if ents, _ := os.ReadDir(tmp); len(ents) != 0 {
		t.Fatalf("参数错误时不应写任何文件: %v", ents)
	}
}

func TestSetupHooksWritesThroughSymlink(t *testing.T) {
	// 配置是符号链接（如 dotfiles）时写到链接指向的文件，链接本身保留
	if runtime.GOOS == "windows" {
		t.Skip("符号链接用例只在 Unix 上跑")
	}
	project := t.TempDir()
	real := filepath.Join(t.TempDir(), "claude-settings.json")
	os.WriteFile(real, []byte(`{"permissions":{}}`), 0o644)
	link := filepath.Join(project, ".claude", "settings.local.json")
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("符号链接应保留: %v %v", st, err)
	}
	if _, ok := readJSON(t, real)["hooks"].(map[string]any)["PermissionRequest"]; !ok {
		t.Fatal("链接指向的文件应写入钩子")
	}
}

func TestSetupHooksRecognizesOnlyRailseHookCommands(t *testing.T) {
	// 只有形如“<一个词> hook claude|codex|gemini”且程序文件名是 railse 的旧条目才算我们的
	others := []string{
		"/opt/railse-old hook claude",
		"git -C /x/railse hook run x",
		"/my/tool --dir=/x/railse hook claude",
	}
	ours := []string{
		"/old/place/railse hook claude",
		`'/a b/railse' hook gemini`,
	}
	var groups []string
	for _, c := range append(append([]string{}, others...), ours...) {
		b, _ := json.Marshal(c)
		groups = append(groups, `{"hooks":[{"type":"command","command":`+string(b)+`}]}`)
	}
	project := t.TempDir()
	path := filepath.Join(project, ".claude", "settings.local.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"hooks":{"PermissionRequest":[`+strings.Join(groups, ",")+`]}}`), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	got := map[string]bool{}
	for _, g := range readJSON(t, path)["hooks"].(map[string]any)["PermissionRequest"].([]any) {
		got[g.(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)] = true
	}
	for _, c := range others {
		if !got[c] {
			t.Errorf("别人的钩子 %q 应保留", c)
		}
	}
	for _, c := range ours {
		if got[c] {
			t.Errorf("旧的 Railse 钩子 %q 应被替换", c)
		}
	}
	if !got["/opt/railse hook claude"] || len(got) != len(others)+1 {
		t.Errorf("应只剩别人的 %d 组加本次一组, 得到 %v", len(others), got)
	}
}

func TestSetupHooksKeepsNonArrayValuesWeDoNotTouch(t *testing.T) {
	// 只有本次要写的事件必须是数组；hooks 里其他非数组的值原样保留
	project := t.TempDir()
	path := filepath.Join(project, ".claude", "settings.local.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(`{"hooks":{"enabled":true,"PreToolUse":"x"}}`), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	hooks := readJSON(t, path)["hooks"].(map[string]any)
	if hooks["enabled"] != true || hooks["PreToolUse"] != "x" {
		t.Fatalf("enabled 与 PreToolUse 应原样保留: %+v", hooks)
	}
	if _, ok := hooks["PermissionRequest"]; !ok {
		t.Fatal("应写入 PermissionRequest")
	}
}

// needUnixShell 在找不到 sh 或非 Unix 时跳过依赖 shell 与 shebang 的断言。
func needUnixShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("需要 Unix 环境")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("找不到 sh")
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s 不是 JSON: %v", path, err)
	}
	return doc
}

func TestSetupHooksKeepsOthersEntriesInOurGroup(t *testing.T) {
	// 用户在 Railse 的条目组里追加了自己的钩子：重跑只删我们的条目，别人的条目留在原组里
	project := t.TempDir()
	path := filepath.Join(project, ".claude", "settings.local.json")
	os.MkdirAll(filepath.Dir(path), 0o755)
	existing := `{"hooks":{"PermissionRequest":[{"matcher":"","hooks":[` +
		`{"type":"command","command":"/opt/railse hook claude","timeout":86400},` +
		`{"type":"command","command":"my-notify-logger"}]}],` +
		`"Stop":[{"hooks":[{"type":"command","command":"/old/place/railse hook claude"}]}]}}`
	os.WriteFile(path, []byte(existing), 0o644)
	args := []string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	first, _ := os.ReadFile(path)
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	second, _ := os.ReadFile(path)
	if !bytes.Equal(first, second) {
		t.Fatalf("两次运行结果应相同:\n%s\n---\n%s", first, second)
	}
	hooks := readJSON(t, path)["hooks"].(map[string]any)
	commands := map[string]int{}
	for _, g := range hooks["PermissionRequest"].([]any) {
		for _, h := range g.(map[string]any)["hooks"].([]any) {
			commands[h.(map[string]any)["command"].(string)]++
		}
	}
	if commands["my-notify-logger"] != 1 {
		t.Fatalf("我们组里别人的条目应保留: %v", commands)
	}
	if commands["/opt/railse hook claude"] != 1 || len(commands) != 2 {
		t.Fatalf("我们的条目应只有一份: %v", commands)
	}
	// 只含我们条目的组删空后整组去掉，不留空组
	if n := len(hooks["Stop"].([]any)); n != 1 {
		t.Fatalf("Stop 应只剩本次写的一组, 得到 %d: %+v", n, hooks["Stop"])
	}
}

func TestSetupHooksWritesLocalAndMigratesShared(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	proj := filepath.Join(dir, "proj")
	os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)
	bin := filepath.Join(dir, "railse")
	// 旧版 setup 写进 settings.json 的条目，加一条别人的
	old, _ := hooksconf.Merge([]byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/opt/x","timeout":5}]}]}}`), bin, hooksconf.Plan("claude", bin))
	os.WriteFile(filepath.Join(proj, ".claude", "settings.json"), old, 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", proj, "--claude", "--bin", bin}, &out, &errOut); code != 0 {
		t.Fatalf("setup: %d %s", code, errOut.String())
	}
	local, _ := os.ReadFile(filepath.Join(proj, ".claude", "settings.local.json"))
	ins, _ := hooksconf.Inspect(local, bin)
	if !ins.Events["PermissionRequest"].Present {
		t.Fatalf("settings.local.json 应有钩子: %s", local)
	}
	shared, _ := os.ReadFile(filepath.Join(proj, ".claude", "settings.json"))
	ins2, _ := hooksconf.Inspect(shared, bin)
	if ins2.Events["PermissionRequest"].Present || !strings.Contains(string(shared), "/opt/x") {
		t.Fatalf("settings.json 应只剩别人的条目: %s", shared)
	}
	if !strings.Contains(out.String(), "迁走") {
		t.Fatalf("应提示迁移: %q", out.String())
	}
	// 再跑一次幂等
	out.Reset()
	run([]string{"setup", "hooks", "--project", proj, "--claude", "--bin", bin}, &out, &errOut)
	if strings.Contains(out.String(), "迁走") {
		t.Fatal("第二次不应再提示迁移")
	}
}

func TestSetupHooksMigrationKeepsOthersAndOtherKeys(t *testing.T) {
	// settings.json 里别人的钩子（含追加在我们组里的）与其他顶层键都保留；只剩我们条目的事件整个去掉；
	// 换过安装路径留下的旧条目（程序文件名是 railse）同样迁走
	project := t.TempDir()
	shared := filepath.Join(project, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(shared), 0o755)
	existing := `{"permissions":{"allow":["Bash(go test:*)"]},"env":{"BIG":12345678901234567890},"hooks":{` +
		`"PermissionRequest":[{"matcher":"","hooks":[{"type":"command","command":"/opt/railse hook claude","timeout":86400},{"type":"command","command":"my-logger"}]}],` +
		`"Notification":[{"hooks":[{"type":"command","command":"/old/place/railse hook claude"}]}],` +
		`"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"npm test && echo ok > /tmp/x"}]}]}}`
	os.WriteFile(shared, []byte(existing), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	b, _ := os.ReadFile(shared)
	for _, want := range []string{`Bash(go test:*)`, `12345678901234567890`, `npm test && echo ok > /tmp/x`, `my-logger`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("别人的内容应保留 %s:\n%s", want, b)
		}
	}
	if strings.Contains(string(b), "railse") || strings.Contains(string(b), "Notification") {
		t.Fatalf("我们的条目（含旧路径的）应迁走，只剩我们条目的事件应去掉:\n%s", b)
	}
	ins, _ := hooksconf.Inspect(readFile(t, filepath.Join(project, ".claude", "settings.local.json")), "/opt/railse")
	for ev := range hooksconf.Expected("claude") {
		if !ins.Events[ev].Present {
			t.Fatalf("settings.local.json 缺 %s", ev)
		}
	}
	if !strings.Contains(out.String(), "已从 "+shared+" 迁走 Railse 钩子") {
		t.Fatalf("应提示从哪个文件迁走: %q", out.String())
	}
}

func TestSetupHooksMigrationDropsEmptiedHooksKey(t *testing.T) {
	// settings.json 里只有我们的钩子：迁走后连 hooks 键一起去掉，其他键保留
	project := t.TempDir()
	shared := filepath.Join(project, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(shared), 0o755)
	old, _ := hooksconf.Merge([]byte(`{"model":"opus"}`), "/opt/railse", hooksconf.Plan("claude", "/opt/railse"))
	os.WriteFile(shared, old, 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	doc := readJSON(t, shared)
	if _, ok := doc["hooks"]; ok || doc["model"] != "opus" {
		t.Fatalf("应只剩 model: %+v", doc)
	}
}

func TestSetupHooksBadSharedWarnsButInstalls(t *testing.T) {
	// settings.json 已不是写入目标：坏 JSON 或形状不对时打印警告、不迁移、不改它，照样装好 settings.local.json 并退出 0
	for _, existing := range []string{
		"{not json",
		`{} {}`,
		`[]`,
		`null`,
		`{"hooks":[]}`,
		`{"hooks":"x"}`,
		`{"hooks":null}`,
	} {
		project := t.TempDir()
		shared := filepath.Join(project, ".claude", "settings.json")
		os.MkdirAll(filepath.Dir(shared), 0o755)
		os.WriteFile(shared, []byte(existing), 0o644)
		var out, errOut bytes.Buffer
		if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
			t.Errorf("%s: 想要退出 0, 得到 %d stderr=%s", existing, code, errOut.String())
			continue
		}
		if b, _ := os.ReadFile(shared); string(b) != existing {
			t.Errorf("%s: settings.json 不应被改, 现在是 %s", existing, b)
		}
		if !strings.Contains(errOut.String(), "警告") || !strings.Contains(errOut.String(), shared) {
			t.Errorf("%s: 应在 stderr 警告并指出文件, 得到 %q", existing, errOut.String())
		}
		if strings.Contains(out.String(), "迁走") {
			t.Errorf("%s: 不应提示迁移: %q", existing, out.String())
		}
		ins, _ := hooksconf.Inspect(readFile(t, filepath.Join(project, ".claude", "settings.local.json")), "/opt/railse")
		if !ins.Events["PermissionRequest"].Present {
			t.Errorf("%s: settings.local.json 应照样写好", existing)
		}
	}
}

func TestSetupHooksNothingToMigrateLeavesSharedUntouched(t *testing.T) {
	// settings.json 里没有我们的条目：字节与修改时间都不变（不重排、不重写）
	project := t.TempDir()
	shared := filepath.Join(project, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(shared), 0o755)
	existing := `{"permissions": {"allow": ["Bash"]},   "hooks": {"Stop": [{"hooks": [{"type": "command", "command": "/opt/x"}]}]}}`
	os.WriteFile(shared, []byte(existing), 0o644)
	past := time.Now().Add(-time.Hour).Truncate(time.Second)
	os.Chtimes(shared, past, past)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if b, _ := os.ReadFile(shared); string(b) != existing {
		t.Fatalf("settings.json 不应被改: %s", b)
	}
	if st, _ := os.Stat(shared); !st.ModTime().Equal(past) {
		t.Fatalf("settings.json 的修改时间不应变: %v != %v", st.ModTime(), past)
	}
	if strings.Contains(out.String(), "迁走") || errOut.Len() != 0 {
		t.Fatalf("不应提示迁移或警告: out=%q err=%q", out.String(), errOut.String())
	}
}

func TestSetupHooksDryRunShowsMigration(t *testing.T) {
	project := t.TempDir()
	shared := filepath.Join(project, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(shared), 0o755)
	old, _ := hooksconf.Merge([]byte(`{"model":"opus"}`), "/opt/railse", hooksconf.Plan("claude", "/opt/railse"))
	os.WriteFile(shared, old, 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse", "--dry-run"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	local := filepath.Join(project, ".claude", "settings.local.json")
	if _, err := os.Stat(local); err == nil {
		t.Fatal("dry-run 不应写 settings.local.json")
	}
	if b, _ := os.ReadFile(shared); !bytes.Equal(b, old) {
		t.Fatalf("dry-run 不应改 settings.json: %s", b)
	}
	s := out.String()
	li, si := strings.Index(s, "== "+local), strings.Index(s, "== "+shared)
	if li < 0 || si < 0 {
		t.Fatalf("dry-run 应打印两份: %s", s)
	}
	// settings.json 那份是迁走后的内容：只剩 model
	if !strings.Contains(s[si:], `"model": "opus"`) || strings.Contains(s[si:], "railse hook") || !strings.Contains(s[si:], "迁走") {
		t.Fatalf("settings.json 那份应是迁走后的内容并注明: %s", s[si:])
	}
	if !strings.Contains(s[li:si], "/opt/railse hook claude") {
		t.Fatalf("settings.local.json 那份应含钩子: %s", s[li:si])
	}
}

func TestSetupHooksMigrationWritesThroughSymlinkAndKeepsMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("符号链接用例只在 Unix 上跑")
	}
	project := t.TempDir()
	real := filepath.Join(t.TempDir(), "dotfiles-claude.json")
	old, _ := hooksconf.Merge([]byte(`{"model":"opus"}`), "/opt/railse", hooksconf.Plan("claude", "/opt/railse"))
	os.WriteFile(real, old, 0o600)
	os.Chmod(real, 0o600)
	link := filepath.Join(project, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(link), 0o755)
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if st, err := os.Lstat(link); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("符号链接应保留: %v %v", st, err)
	}
	if st, _ := os.Stat(real); st.Mode().Perm() != 0o600 {
		t.Fatalf("原文件权限应保留 0600, 得到 %o", st.Mode().Perm())
	}
	if b := readFile(t, real); strings.Contains(string(b), "railse") || !strings.Contains(string(b), "opus") {
		t.Fatalf("链接指向的文件应迁走我们的条目: %s", b)
	}
	if ents, _ := os.ReadDir(filepath.Dir(real)); len(ents) != 1 {
		t.Fatalf("目录里不应留下临时文件: %v", ents)
	}
}

func TestSetupHooksBadTargetStopsMigration(t *testing.T) {
	// 任何一个写入目标坏了：settings.local.json 不写，settings.json 也不迁
	project := t.TempDir()
	shared := filepath.Join(project, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(shared), 0o755)
	old, _ := hooksconf.Merge(nil, "/opt/railse", hooksconf.Plan("claude", "/opt/railse"))
	os.WriteFile(shared, old, 0o644)
	os.MkdirAll(filepath.Join(project, ".gemini"), 0o755)
	os.WriteFile(filepath.Join(project, ".gemini", "settings.json"), []byte("{not json"), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--gemini", "--bin", "/opt/railse"}, &out, &errOut); code != 1 {
		t.Fatalf("想要退出 1, 得到 %d", code)
	}
	if b, _ := os.ReadFile(shared); !bytes.Equal(b, old) {
		t.Fatalf("settings.json 不应被迁: %s", b)
	}
	if _, err := os.Stat(filepath.Join(project, ".claude", "settings.local.json")); err == nil {
		t.Fatal("不应写 settings.local.json")
	}
}

func TestSetupHooksOnlyCodexDoesNotMigrateClaude(t *testing.T) {
	project := t.TempDir()
	shared := filepath.Join(project, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(shared), 0o755)
	old, _ := hooksconf.Merge(nil, "/opt/railse", hooksconf.Plan("claude", "/opt/railse"))
	os.WriteFile(shared, old, 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--codex", "--codex-file", filepath.Join(t.TempDir(), "hooks.json"), "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if b, _ := os.ReadFile(shared); !bytes.Equal(b, old) {
		t.Fatalf("没选 Claude 时不应动 settings.json: %s", b)
	}
}

func TestSetupHooksLocalLinkedToSharedIsNotMigratedAway(t *testing.T) {
	// settings.local.json 是指向 settings.json 的符号链接（同一个文件）：只装不迁，否则刚装的钩子会被迁移删掉
	if runtime.GOOS == "windows" {
		t.Skip("符号链接用例只在 Unix 上跑")
	}
	project := t.TempDir()
	shared := filepath.Join(project, ".claude", "settings.json")
	os.MkdirAll(filepath.Dir(shared), 0o755)
	old, _ := hooksconf.Merge(nil, "/old/railse", hooksconf.Plan("claude", "/old/railse"))
	os.WriteFile(shared, old, 0o644)
	if err := os.Symlink("settings.json", filepath.Join(project, ".claude", "settings.local.json")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	ins, _ := hooksconf.Inspect(readFile(t, shared), "/opt/railse")
	if st := ins.Events["PermissionRequest"]; !st.Present || st.Program != "/opt/railse" {
		t.Fatalf("同一个文件里应装着新钩子: %+v\n%s", st, readFile(t, shared))
	}
	if strings.Contains(out.String(), "迁走") {
		t.Fatalf("同一个文件不应提示迁移: %q", out.String())
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// needUserAutostart 在装不了用户级托管的环境里跳过：开机自启只有 macOS 与 Linux，root 装用户级托管会被拒绝。
func needUserAutostart(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("开机自启只在 macOS 与 Linux 上有")
	}
	if os.Getuid() == 0 {
		t.Skip("root 装用户级托管会被拒绝")
	}
}

// setupAllFixture 是 setup all 测试的环境：假家目录 home（里面有可执行的假 railse）、带 .git 的项目、守护进程数据目录。
type setupAllFixture struct {
	home, proj, dir, bin string
	rec                  *cmdRec
}

// setupAllEnv 先换掉 doctor 看到的 CLI（不真跑 claude、codex、gemini 的 --version），再用 autostartEnv 给假家目录与
// 记录命令的 runCommand（不跑真实的 launchctl、systemctl），托管后不等守护进程应答。绝不碰真实的家目录。
func setupAllEnv(t *testing.T) setupAllFixture {
	t.Helper()
	fakeDoctorCLIs(t)
	home, rec := autostartEnv(t)
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(home, "nohub"))
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := setupDaemonWait
	setupDaemonWait = 0
	t.Cleanup(func() { setupDaemonWait = old })
	return setupAllFixture{home: home, proj: proj, dir: filepath.Join(home, "h"), bin: filepath.Join(home, "railse"), rec: rec}
}

// args 是带上本环境 --project、--dir、--bin 的 railse setup all 命令行。
func (f setupAllFixture) args(extra ...string) []string {
	return append([]string{"setup", "all", "--project", f.proj, "--dir", f.dir, "--bin", f.bin}, extra...)
}

// agentJobPath 是本系统上守护进程的托管文件（与 setup autostart 同一套路径函数）。
func (f setupAllFixture) agentJobPath() string {
	if runtime.GOOS == "darwin" {
		return launchdPlistPath(f.home, "agent")
	}
	return systemdUnitPath(f.home, "agent", false)
}

// isJobStatusQuery 认出问服务管理器“作业在不在跑”的命令：launchctl print、systemctl is-active。
func isJobStatusQuery(call []string) bool {
	return len(call) > 1 && (call[0] == "launchctl" && call[1] == "print" || call[0] == "systemctl" && slices.Contains(call, "is-active"))
}

// jobState 给 cmdRec.respond：问作业状态时按 running 回答，其余命令一律成功。
func jobState(running bool) func([]string) (string, error) {
	return func(call []string) (string, error) {
		switch {
		case !isJobStatusQuery(call):
			return "", nil
		case running && call[0] == "launchctl":
			return "\tstate = running\n", nil
		case running:
			return "active\n", nil
		case call[0] == "launchctl":
			return "Could not find service \"dev.railse.agent\" in domain for user gui: 501\n", exitErr(113)
		}
		return "inactive\n", exitErr(3)
	}
}

// installCalls 去掉作业状态查询，只留装托管的命令（bootout、bootstrap、daemon-reload、restart 等）。
func installCalls(calls [][]string) [][]string {
	var out [][]string
	for _, c := range calls {
		if !isJobStatusQuery(c) {
			out = append(out, c)
		}
	}
	return out
}

func newEnrollToken(t *testing.T, h *testHub) string {
	t.Helper()
	tok, err := h.st.CreateEnrollToken(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func TestSetupAllEnrollsHooksAutostartAndDoctor(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	var out, errOut bytes.Buffer
	code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h), "--name", "mac"), &out, &errOut)
	// doctor 会因守护进程没在跑（测试里只托管不真起）而 fail，setup all 把 doctor 结果当信息不当失败
	if code != 0 {
		t.Fatalf("setup all: %d\n%s\n%s", code, out.String(), errOut.String())
	}
	m, err := agent.LoadMachine(filepath.Join(f.dir, "machine.json"))
	if err != nil || m.Name != "mac" {
		t.Fatalf("应已接入: %+v %v", m, err)
	}
	local, _ := hooksconf.ClaudePaths(f.proj)
	if !strings.Contains(string(readFile(t, local)), f.bin+" hook claude") {
		t.Fatal("应已装 Claude 钩子，且指向 --bin")
	}
	for _, p := range []string{hooksconf.CodexPath(f.home), hooksconf.GeminiPath(f.proj)} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("应已装钩子 %s", p)
		}
	}
	if len(f.rec.calls) == 0 {
		t.Fatal("应已调用 launchctl/systemctl 托管")
	}
	for _, c := range f.rec.calls {
		if c[0] != "launchctl" && c[0] != "systemctl" {
			t.Fatalf("不该跑 %v", c)
		}
	}
	job := string(readFile(t, f.agentJobPath()))
	if !strings.Contains(job, f.dir) || !strings.Contains(job, f.bin) {
		t.Fatalf("托管文件应带上 --dir 与 --bin:\n%s", job)
	}
	s := out.String()
	for _, want := range []string{"已接入", "已写入", "已托管", "[ok]", "守护进程数据目录：" + f.dir, "[fail]", "体检有失败项", "下一步", h.http.URL, "docs/web.md"} {
		if !strings.Contains(s, want) {
			t.Fatalf("输出缺 %q:\n%s", want, s)
		}
	}
	last := -1
	for i := 1; i <= 4; i++ {
		at := strings.Index(s, fmt.Sprintf("== %d. ", i))
		if at <= last {
			t.Fatalf("第 %d 步的标题缺失或顺序不对:\n%s", i, s)
		}
		last = at
	}
	if strings.Index(s, "下一步") < last {
		t.Fatalf("下一步应在体检之后:\n%s", s)
	}
	// 再跑一次：已接入则跳过接入，不报错；--no-autostart 不再托管
	calls := len(f.rec.calls)
	out.Reset()
	if code := run(f.args("--hub", h.http.URL, "--token", "used", "--no-autostart"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "已接入过") {
		t.Fatalf("第二次: %d %q %q", code, out.String(), errOut.String())
	}
	if len(f.rec.calls) != calls || !strings.Contains(out.String(), "--no-autostart") {
		t.Fatalf("--no-autostart 不该托管: %v\n%s", f.rec.calls[calls:], out.String())
	}
	// 已接入时不必再给 --hub、--token；下一步里的地址取自 machine.json
	out.Reset()
	if code := run(f.args("--no-autostart"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "已接入过") || !strings.Contains(out.String(), h.http.URL) {
		t.Fatalf("已接入、不给 --hub: %d %q %q", code, out.String(), errOut.String())
	}
	if code := run([]string{"setup", "all", "--project", f.proj}, &out, &errOut); code != 2 {
		t.Fatalf("缺 --hub/--token 应退出 2: %d", code)
	}
}

func TestSetupAllUsageErrors(t *testing.T) {
	f := setupAllEnv(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{f.args(), "需要 --hub 与 --token"},
		{f.args("--hub", "https://hub.example.com"), "需要 --hub 与 --token"},
		{f.args("--token", "t"), "需要 --hub 与 --token"},
		{f.args("--hub", "ftp://hub.example.com", "--token", "t"), "--hub 必须是"},
		{f.args("--hub", "https://hub.example.com", "--token", "t", "extra"), "多余的参数"},
		{f.args("--bogus"), "railse setup all"},
		{[]string{"setup", "all", "-h"}, "railse setup all"},
	} {
		var out, errOut bytes.Buffer
		if code := run(tc.args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), tc.want) {
			t.Fatalf("%v: %d %q", tc.args, code, errOut.String())
		}
		if !strings.Contains(errOut.String(), "railse setup all --hub URL") {
			t.Fatalf("%v: 应打印 setup all 的用法: %q", tc.args, errOut.String())
		}
	}
	if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
		t.Fatal("用法错不该建数据目录")
	}
	if _, err := os.Stat(filepath.Join(f.proj, ".claude")); !os.IsNotExist(err) {
		t.Fatal("用法错不该装钩子")
	}
	if len(f.rec.calls) != 0 {
		t.Fatalf("用法错不该托管: %v", f.rec.calls)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"setup"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "railse setup all") {
		t.Fatalf("setup 的用法应列出 all: %d %q", code, errOut.String())
	}
}

// 输入一看就不对（--bin 不能执行、项目目录不存在）时一步都不做。
func TestSetupAllChecksInputsBeforeAnyStep(t *testing.T) {
	h := startTestHub(t)
	f := setupAllEnv(t)
	tok := newEnrollToken(t, h)
	var out, errOut bytes.Buffer
	if code := run(append(f.args("--hub", h.http.URL, "--token", tok), "--bin", filepath.Join(f.home, "nope")), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "不存在") {
		t.Fatalf("--bin 不存在: %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run(append(f.args("--hub", h.http.URL, "--token", tok), "--project", filepath.Join(f.home, "nope")), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "项目目录") {
		t.Fatalf("项目目录不存在: %d %q", code, errOut.String())
	}
	if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
		t.Fatal("输入不对时不该接入")
	}
	if strings.Contains(out.String(), "== 1.") || len(f.rec.calls) != 0 {
		t.Fatalf("输入不对时一步都不该做: %q %v", out.String(), f.rec.calls)
	}
}

func TestSetupAllEnrollFailureStopsAtStep1(t *testing.T) {
	h := startTestHub(t)
	f := setupAllEnv(t)
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", "not-a-token"), &out, &errOut); code != 1 {
		t.Fatalf("接入码不对应退出 1: %d %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "第 1 步（接入 hub）") || !strings.Contains(errOut.String(), "接入失败") {
		t.Fatalf("应说明停在第 1 步: %q", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.dir, "machine.json")); !os.IsNotExist(err) {
		t.Fatal("接入失败不该有 machine.json")
	}
	if _, err := os.Stat(filepath.Join(f.proj, ".claude")); !os.IsNotExist(err) {
		t.Fatal("接入失败后不该装钩子")
	}
	if len(f.rec.calls) != 0 || strings.Contains(out.String(), "下一步") || strings.Contains(out.String(), "项通过") {
		t.Fatalf("接入失败后不该托管、体检: %v %q", f.rec.calls, out.String())
	}
}

func TestSetupAllHooksFailureStopsAtStep2(t *testing.T) {
	h := startTestHub(t)
	f := setupAllEnv(t)
	local, _ := hooksconf.ClaudePaths(f.proj)
	os.MkdirAll(filepath.Dir(local), 0o755)
	os.WriteFile(local, []byte("{bad"), 0o644)
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h)), &out, &errOut); code != 1 {
		t.Fatalf("钩子装不了应退出 1: %d %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "第 2 步（安装钩子）") || !strings.Contains(errOut.String(), "已完成：接入 hub") {
		t.Fatalf("应说明停在第 2 步、已接入: %q", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.dir, "machine.json")); err != nil {
		t.Fatal("第 1 步应已完成")
	}
	if len(f.rec.calls) != 0 || strings.Contains(out.String(), "下一步") || strings.Contains(out.String(), "项通过") {
		t.Fatalf("钩子失败后不该托管、体检: %v %q", f.rec.calls, out.String())
	}
}

func TestSetupAllAutostartFailureStopsAtStep3(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	f.rec.respond = func([]string) (string, error) { return "boom", exitErr(1) }
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h)), &out, &errOut); code != 1 {
		t.Fatalf("托管失败应退出 1: %d %q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "第 3 步（开机自启）") || !strings.Contains(errOut.String(), "已完成：接入 hub、安装钩子") || !strings.Contains(errOut.String(), "railse agent run --dir "+f.dir) {
		t.Fatalf("应说明停在第 3 步: %q", errOut.String())
	}
	local, _ := hooksconf.ClaudePaths(f.proj)
	if _, err := os.Stat(local); err != nil {
		t.Fatal("第 2 步应已完成")
	}
	if strings.Contains(out.String(), "下一步") || strings.Contains(out.String(), "项通过") {
		t.Fatalf("托管失败后不该体检: %q", out.String())
	}
}

func TestSetupAllNoHooksAndNoAutostart(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h), "--no-hooks"), &out, &errOut); code != 0 {
		t.Fatalf("--no-hooks: %d %q", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.proj, ".claude")); !os.IsNotExist(err) {
		t.Fatal("--no-hooks 不该装 Claude 钩子")
	}
	if _, err := os.Stat(hooksconf.CodexPath(f.home)); !os.IsNotExist(err) {
		t.Fatal("--no-hooks 不该装 Codex 钩子")
	}
	if len(f.rec.calls) == 0 || !strings.Contains(out.String(), "--no-hooks") || !strings.Contains(out.String(), "项通过") {
		t.Fatalf("--no-hooks 仍应托管与体检: %v %q", f.rec.calls, out.String())
	}
	calls := len(f.rec.calls)
	out.Reset()
	if code := run(f.args("--no-hooks", "--no-autostart"), &out, &errOut); code != 0 {
		t.Fatalf("--no-hooks --no-autostart: %d %q", code, errOut.String())
	}
	if len(f.rec.calls) != calls || !strings.Contains(out.String(), "项通过") || !strings.Contains(out.String(), "下一步") {
		t.Fatalf("两个都跳过时只体检: %v %q", f.rec.calls[calls:], out.String())
	}
	if !strings.Contains(out.String(), "守护进程要自己 railse agent run --dir "+f.dir) {
		t.Fatalf("--no-autostart 的提示应带上 --dir: %q", out.String())
	}
	// 没托管时体检的失败不是“还没起来”
	if !strings.Contains(out.String(), "按上面的“修复：”处理") || strings.Contains(out.String(), "过几秒") {
		t.Fatalf("没托管时的体检说明不对: %q", out.String())
	}
}

func TestSetupAllSkipsEnrollWhenMachineExists(t *testing.T) {
	h := startTestHub(t)
	f := setupAllEnv(t)
	// 先接入一次
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h), "--no-autostart"), &out, &errOut); code != 0 {
		t.Fatalf("首次: %d %q", code, errOut.String())
	}
	mpath := filepath.Join(f.dir, "machine.json")
	before := readFile(t, mpath)
	// 已接入、--hub 换了地址：跳过接入、不动 machine.json，提醒地址不一样；下一步用 machine.json 里的地址
	out.Reset()
	if code := run(f.args("--hub", "https://other.example.com", "--token", "x", "--no-autostart"), &out, &errOut); code != 0 {
		t.Fatalf("已接入: %d %q", code, errOut.String())
	}
	s := out.String()
	if !strings.Contains(s, "已接入过") || !strings.Contains(s, "https://other.example.com") || !strings.Contains(s, "--force") || !strings.Contains(s[strings.Index(s, "下一步"):], h.http.URL) {
		t.Fatalf("已接入时应跳过并提醒地址不同: %q", s)
	}
	if !bytes.Equal(readFile(t, mpath), before) {
		t.Fatal("跳过接入不该改 machine.json")
	}
	// --force 重新接入
	out.Reset()
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h), "--force", "--no-autostart"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "已接入 hub") {
		t.Fatalf("--force: %d %q %q", code, out.String(), errOut.String())
	}
	if bytes.Equal(readFile(t, mpath), before) {
		t.Fatal("--force 应重新接入、换掉 machine.json")
	}
	// machine.json 坏了：不当已接入，停在第 1 步
	os.WriteFile(mpath, []byte("{"), 0o600)
	os.RemoveAll(filepath.Join(f.proj, ".claude"))
	errOut.Reset()
	if code := run(f.args("--no-autostart"), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "第 1 步") || !strings.Contains(errOut.String(), "--force") {
		t.Fatalf("坏的 machine.json: %d %q", code, errOut.String())
	}
	if _, err := os.Stat(filepath.Join(f.proj, ".claude")); !os.IsNotExist(err) {
		t.Fatal("第 1 步失败后不该装钩子")
	}
}

// 手动起的守护进程占着 socket、服务管理器里的作业又没在跑时不托管（第二个实例抢不到 socket，会被 launchd/systemd 反复拉起），
// 照常体检；作业在跑（托管的那个占着 socket）时照常重装（bootout+bootstrap、restart 会换掉在跑的那个）。
func TestSetupAllSkipsAutostartWhenUnmanagedDaemonRuns(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	f.rec.respond = jobState(false)
	serveAgent(t, agent.ConfigIn(f.dir))
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h)), &out, &errOut); code != 0 {
		t.Fatalf("setup all: %d %q %q", code, out.String(), errOut.String())
	}
	if c := installCalls(f.rec.calls); len(c) != 0 || len(f.rec.calls) == 0 {
		t.Fatalf("socket 被占时应只问作业状态、不托管: %v", f.rec.calls)
	}
	s := out.String()
	for _, want := range []string{"已在运行", "Ctrl-C", "railse setup autostart --dir " + f.dir + " --bin " + f.bin, "[ok]    守护进程在跑", "没有托管开机自启", "下一步"} {
		if !strings.Contains(s, want) {
			t.Fatalf("输出缺 %q:\n%s", want, s)
		}
	}
	if _, err := os.Stat(f.agentJobPath()); !os.IsNotExist(err) {
		t.Fatal("不该写托管文件")
	}
	// 作业文件在、作业却没在跑（例如上次托管失败留下的）：仍是手动那个占着 socket，照样不托管
	os.MkdirAll(filepath.Dir(f.agentJobPath()), 0o755)
	os.WriteFile(f.agentJobPath(), []byte("old"), 0o644)
	f.rec.calls = nil
	out.Reset()
	if code := run(f.args(), &out, &errOut); code != 0 || len(installCalls(f.rec.calls)) != 0 || !strings.Contains(out.String(), "Ctrl-C") {
		t.Fatalf("作业文件在但没在跑时不该托管: %d %v %q", code, f.rec.calls, out.String())
	}
	// 服务管理器说作业在跑：占着 socket 的是托管的那个，照常重装，并等到守护进程应答
	f.rec.respond = jobState(true)
	f.rec.calls = nil
	setupDaemonWait = 2 * time.Second
	out.Reset()
	if code := run(f.args(), &out, &errOut); code != 0 {
		t.Fatalf("已托管时重跑: %d %q %q", code, out.String(), errOut.String())
	}
	if len(installCalls(f.rec.calls)) == 0 || !strings.Contains(out.String(), "已托管") || !strings.Contains(out.String(), "守护进程已应答") {
		t.Fatalf("作业在跑时应重装: %v %q", f.rec.calls, out.String())
	}
}

// 审查探针：第 3 步失败（托管文件已写下）→ 照提示手动 railse agent run → 重跑 setup all，不能再 bootstrap 第二个守护进程。
func TestSetupAllRerunAfterAutostartFailureKeepsManualDaemon(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	f.rec.respond = func([]string) (string, error) { return "boom", exitErr(1) }
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h)), &out, &errOut); code != 1 {
		t.Fatalf("托管应失败: %d %q", code, errOut.String())
	}
	if _, err := os.Stat(f.agentJobPath()); err != nil {
		t.Fatalf("本例要的是托管失败后留下的托管文件: %v", err)
	}
	serveAgent(t, agent.ConfigIn(f.dir))
	f.rec.respond = jobState(false)
	f.rec.calls = nil
	out.Reset()
	if code := run(f.args(), &out, &errOut); code != 0 {
		t.Fatalf("重跑: %d %q %q", code, out.String(), errOut.String())
	}
	if c := installCalls(f.rec.calls); len(c) != 0 {
		t.Fatalf("手动守护进程在跑时不该再托管: %v", c)
	}
	if strings.Contains(out.String(), "守护进程已应答") || !strings.Contains(out.String(), "Ctrl-C") {
		t.Fatalf("应跳过托管: %q", out.String())
	}
}

// socket 上有监听、但不是能正常应答的守护进程（别的协议版本、卡住不答）：照样算被占，不托管。
func TestSetupAllSocketHeldByForeignListenerSkipsAutostart(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	f.rec.respond = jobState(false)
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(f.dir, "agent.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Write([]byte(`{"v":999,"ok":false,"error":"unsupported version"}` + "\n"))
			c.Close()
		}
	}()
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h)), &out, &errOut); code != 0 {
		t.Fatalf("setup all: %d %q %q", code, out.String(), errOut.String())
	}
	if c := installCalls(f.rec.calls); len(c) != 0 || !strings.Contains(out.String(), "Ctrl-C") {
		t.Fatalf("socket 被别的监听占着时不该托管: %v %q", f.rec.calls, out.String())
	}
}

// 上次崩溃留下的无人监听的 socket 文件不算被占：照常托管，也不必问作业状态（守护进程自己 Listen 时会替换它）。
func TestSetupAllStaleSocketStillInstalls(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	if err := os.MkdirAll(f.dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(f.dir, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if st, err := os.Lstat(sock); err != nil || st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("应留下陈旧的 socket 文件: %v", err)
	}
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h)), &out, &errOut); code != 0 {
		t.Fatalf("setup all: %d %q %q", code, out.String(), errOut.String())
	}
	if len(installCalls(f.rec.calls)) == 0 || len(installCalls(f.rec.calls)) != len(f.rec.calls) || !strings.Contains(out.String(), "已托管") {
		t.Fatalf("陈旧 socket 时应直接托管: %v %q", f.rec.calls, out.String())
	}
}

// --no-autostart 时刚（重新）接入，而守护进程正在跑：说清楚要怎样它才读到新的 machine.json。
func TestSetupAllNoAutostartReenrollWhileDaemonRuns(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	serveAgent(t, agent.ConfigIn(f.dir))
	f.rec.respond = jobState(true)
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h), "--no-autostart"), &out, &errOut); code != 0 {
		t.Fatalf("setup all: %d %q", code, errOut.String())
	}
	if c := installCalls(f.rec.calls); len(c) != 0 {
		t.Fatalf("--no-autostart 不该托管: %v", c)
	}
	if !strings.Contains(out.String(), "已托管的守护进程用 railse setup autostart --dir "+f.dir+" --bin "+f.bin+" 重装或重启后生效") {
		t.Fatalf("托管的守护进程在跑时应提示重装: %q", out.String())
	}
	f.rec.respond = jobState(false)
	out.Reset()
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h), "--no-autostart", "--force"), &out, &errOut); code != 0 {
		t.Fatalf("--force: %d %q", code, errOut.String())
	}
	if !strings.Contains(out.String(), "停掉后重新 railse agent run --dir "+f.dir) {
		t.Fatalf("手动的守护进程在跑时应提示重启它: %q", out.String())
	}
	// 没有新接入时不提示
	out.Reset()
	if code := run(f.args("--no-autostart"), &out, &errOut); code != 0 || strings.Contains(out.String(), "重装或重启后生效") || strings.Contains(out.String(), "停掉后重新") {
		t.Fatalf("没有新接入时不该提示: %d %q", code, out.String())
	}
}

// root 跑 setup all：一步都不做（不用掉一次性接入码、不写 root 属主的配置），退出 1。
func TestSetupAllRefusesRoot(t *testing.T) {
	h := startTestHub(t)
	f := setupAllEnv(t)
	tok := newEnrollToken(t, h)
	for _, extra := range [][]string{nil, {"--no-autostart"}, {"--no-autostart", "--no-hooks"}} {
		var out, errOut bytes.Buffer
		args := append(f.args("--hub", h.http.URL, "--token", tok), extra...)[2:] // 去掉 setup all
		if code := runSetupAllAs(0, args, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "不要 sudo") || !strings.Contains(errOut.String(), "sudo railse setup autostart --hub --system") {
			t.Fatalf("root %v: %d %q", extra, code, errOut.String())
		}
		if out.Len() != 0 {
			t.Fatalf("root 时不该开始任何一步: %q", out.String())
		}
	}
	for _, p := range []string{f.dir, filepath.Join(f.home, ".railse"), filepath.Join(f.proj, ".claude"), filepath.Join(f.proj, ".gemini"), filepath.Join(f.home, ".codex")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("root 时不该建 %s", p)
		}
	}
	if len(f.rec.calls) != 0 {
		t.Fatalf("root 时不该跑命令: %v", f.rec.calls)
	}
	// 接入码没被用掉
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--token", tok, "--no-autostart"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "已接入 hub") {
		t.Fatalf("root 被拒后接入码应仍可用: %d %q %q", code, out.String(), errOut.String())
	}
}

// 托管后守护进程迟迟不应答：等到上限就照常体检，并说明体检里的失败可能只是还没起来。
func TestSetupAllWaitsForDaemonThenReports(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	setupDaemonWait = 300 * time.Millisecond
	var out, errOut bytes.Buffer
	start := time.Now()
	if code := run(f.args("--hub", h.http.URL, "--token", newEnrollToken(t, h)), &out, &errOut); code != 0 {
		t.Fatalf("setup all: %d %q", code, errOut.String())
	}
	if time.Since(start) < 300*time.Millisecond {
		t.Fatal("应等守护进程应答到上限")
	}
	if !strings.Contains(out.String(), "还没有应答") || !strings.Contains(out.String(), "过几秒再跑 railse doctor --project "+f.proj+" --dir "+f.dir+" --bin "+f.bin) {
		t.Fatalf("应说明守护进程还没起来: %q", out.String())
	}
}
