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
	"time"

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
