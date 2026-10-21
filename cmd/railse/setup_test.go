package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	claude := readJSON(t, filepath.Join(project, ".claude", "settings.json"))
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
	os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte(existing), 0o644)
	args := []string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	first, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	second, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	if !bytes.Equal(first, second) {
		t.Fatalf("两次运行结果应相同:\n%s\n---\n%s", first, second)
	}
	doc := readJSON(t, filepath.Join(project, ".claude", "settings.json"))
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
	os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte("{not json"), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 1 {
		t.Fatalf("坏的现有文件应退出 1 并不覆盖, 得到 %d", code)
	}
	b, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
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
		path := filepath.Join(project, ".claude", "settings.json")
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
	path := filepath.Join(claudeDir, "settings.json")
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
	hooks := readJSON(t, filepath.Join(project, ".claude", "settings.json"))["hooks"].(map[string]any)
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
	path := filepath.Join(project, ".claude", "settings.json")
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
	path := filepath.Join(project, ".claude", "settings.json")
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
	path := filepath.Join(project, ".claude", "settings.json")
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
