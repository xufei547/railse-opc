package hooksconf

import (
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestInspectAndExpected(t *testing.T) {
	bin := "/usr/local/bin/railse"
	doc, err := Merge(nil, bin, Plan("claude", bin))
	if err != nil {
		t.Fatal(err)
	}
	ins, err := Inspect(doc, bin)
	if err != nil {
		t.Fatal(err)
	}
	for ev, minTimeout := range Expected("claude") {
		st := ins.Events[ev]
		if !st.Present || st.Timeout < minTimeout || st.Program != bin {
			t.Fatalf("%s: %+v（要求超时 ≥ %d）", ev, st, minTimeout)
		}
	}
	// 别人的条目不算
	other := []byte(`{"hooks":{"PermissionRequest":[{"hooks":[{"type":"command","command":"/opt/x hook claude","timeout":60}]}]}}`)
	ins, _ = Inspect(other, bin)
	if ins.Events["PermissionRequest"].Present {
		t.Fatal("别的程序的条目不应算作已安装")
	}
	// bin 为空时按程序文件名 railse 认（换过安装路径）
	ins, _ = Inspect([]byte(`{"hooks":{"PermissionRequest":[{"hooks":[{"type":"command","command":"/old/railse hook claude","timeout":86400}]}]}}`), "")
	if !ins.Events["PermissionRequest"].Present || ins.Events["PermissionRequest"].Program != "/old/railse" {
		t.Fatalf("按文件名识别: %+v", ins.Events["PermissionRequest"])
	}
	if _, err := Inspect([]byte("{bad"), bin); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if Expected("gemini")["BeforeTool"] != 86400000 || Expected("codex")["PermissionRequest"] != 86400 {
		t.Fatalf("Expected: %v %v", Expected("gemini"), Expected("codex"))
	}
}

func TestMergeRemoveOnly(t *testing.T) {
	bin := "/usr/local/bin/railse"
	doc, _ := Merge([]byte(`{"permissions":{"allow":["Bash"]}}`), bin, Plan("claude", bin))
	out, err := Merge(doc, bin, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "railse") || strings.Contains(string(out), `"hooks"`) || !strings.Contains(string(out), `"permissions"`) {
		t.Fatalf("只删我们的条目后: %s", out)
	}
}

func TestPaths(t *testing.T) {
	local, shared := ClaudePaths("/p")
	if local != "/p/.claude/settings.local.json" || shared != "/p/.claude/settings.json" || GeminiPath("/p") != "/p/.gemini/settings.json" || CodexPath("/h") != "/h/.codex/hooks.json" {
		t.Fatal("路径不对")
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/bin/railse":  "/usr/local/bin/railse",
		"/a/b-c_d.e+f:g@h%i,j=k": "/a/b-c_d.e+f:g@h%i,j=k",
		"/my apps/railse":        `'/my apps/railse'`,
		"/it's/railse":           `'/it'\''s/railse'`,
		"/a$b/railse":            `'/a$b/railse'`,
		"":                       `''`,
	} {
		if got := ShellQuote(in); got != want {
			t.Errorf("ShellQuote(%q) = %q, 想要 %q", in, got, want)
		}
	}
}

func TestShellQuoteRunsInShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("需要 Unix 环境")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("找不到 sh")
	}
	for _, s := range []string{"/my apps/railse", "/it's/rail se", `/a"b$c\d/railse`} {
		got, err := exec.Command("sh", "-c", "printf %s "+ShellQuote(s)).Output()
		if err != nil || string(got) != s {
			t.Errorf("shell 里 %s 应还原为 %q, 得到 %q err=%v", ShellQuote(s), s, got, err)
		}
	}
}

func TestCommandProgram(t *testing.T) {
	for cmd, want := range map[string]string{
		"/usr/local/bin/railse hook claude": "/usr/local/bin/railse",
		"/old/railse hook codex":            "/old/railse",
		"railse hook gemini":                "railse",
		`'/my apps/railse' hook claude`:     "/my apps/railse",
		`'/it'\''s/railse' hook gemini`:     "/it's/railse",
	} {
		got, ok := CommandProgram(cmd)
		if !ok || got != want {
			t.Errorf("CommandProgram(%q) = %q,%v, 想要 %q", cmd, got, ok, want)
		}
	}
	for _, cmd := range []string{
		"",
		"/opt/railse",
		"/opt/railse hook",
		"/opt/railse hook claude --x",
		"/opt/railse hook other",
		"/opt/railse  hook claude",
		" /opt/railse hook claude",
		"git -C /x/railse hook run x",
		"/my/tool --dir=/x/railse hook claude",
		`'/unterminated hook claude`,
		`'/a b/railse'x hook claude`,
	} {
		if got, ok := CommandProgram(cmd); ok {
			t.Errorf("CommandProgram(%q) 不应认出, 得到 %q", cmd, got)
		}
	}
	// ShellQuote 写出的命令都能认回原路径
	for _, bin := range []string{"/opt/railse", "/my apps/railse", "/it's/railse"} {
		if got, ok := CommandProgram(ShellQuote(bin) + " hook claude"); !ok || got != bin {
			t.Errorf("往返 %q 得到 %q,%v", bin, got, ok)
		}
	}
}

func TestIsOurs(t *testing.T) {
	entry := func(name, cmd string) any {
		m := map[string]any{"type": "command", "command": cmd}
		if name != "" {
			m["name"] = name
		}
		return m
	}
	bin := "/usr/local/bin/railse"
	for _, c := range []struct {
		entry any
		bin   string
		want  bool
	}{
		{entry("", "/usr/local/bin/railse hook claude"), bin, true},
		{entry("", "/usr/local/bin/railse hook codex-notify"), bin, true}, // 本次 bin 的任何 hook 子命令
		{entry("railse-before-tool", "/elsewhere/x"), bin, true},          // Gemini 的条目按名字认
		{entry("", "/old/place/railse hook gemini"), bin, true},           // 换过安装路径的旧条目
		{entry("", `'/a b/railse' hook claude`), bin, true},
		{entry("", "/opt/railse-old hook claude"), bin, false},
		{entry("", "/opt/x hook claude"), bin, false},
		{entry("my-hook", "echo railse hook claude"), bin, false},
		{entry("", "/usr/local/bin/railse"), bin, false},
		{"not a map", bin, false},
		{nil, bin, false},
		// bin 为空：只按名字前缀与程序文件名认
		{entry("", "/old/railse hook claude"), "", true},
		{entry("railse-notification", "whatever"), "", true},
		{entry("", "'' hook claude"), "", false},
		{entry("", "/opt/x hook claude"), "", false},
	} {
		if got := IsOurs(c.entry, c.bin); got != c.want {
			t.Errorf("IsOurs(%v, %q) = %v, 想要 %v", c.entry, c.bin, got, c.want)
		}
	}
}

func TestPlan(t *testing.T) {
	bin := "/my apps/railse"
	quoted := `'/my apps/railse'`
	claude := Plan("claude", bin)
	pr := claude["PermissionRequest"]
	if len(claude) != 4 || len(pr) != 1 || pr[0].Matcher == nil || *pr[0].Matcher != "" ||
		pr[0].Hooks[0].Command != quoted+" hook claude" || pr[0].Hooks[0].Timeout != 86400 || pr[0].Hooks[0].Type != "command" {
		t.Fatalf("Claude: %+v", claude)
	}
	codex := Plan("codex", bin)
	if len(codex) != 3 || codex["SessionEnd"][0].Hooks[0].Timeout != 3 || codex["Stop"][0].Matcher != nil {
		t.Fatalf("Codex: %+v", codex)
	}
	gemini := Plan("gemini", bin)
	bt := gemini["BeforeTool"][0]
	if len(gemini) != 4 || *bt.Matcher != ".*" || bt.Hooks[0].Name != "railse-before-tool" || bt.Hooks[0].Timeout != 86400000 ||
		gemini["AfterAgent"][0].Hooks[0].Name != "railse-after-agent" {
		t.Fatalf("Gemini: %+v", gemini)
	}
	if Plan("other", bin) != nil {
		t.Fatal("不认识的 CLI 应返回 nil")
	}
	// JSON 形状与旧版 setup 写出的一致：matcher 只在设了时出现，name、timeout 为零值时省略
	b, _ := json.Marshal(Plan("claude", "/opt/railse")["Stop"])
	if string(b) != `[{"hooks":[{"type":"command","command":"/opt/railse hook claude"}]}]` {
		t.Fatalf("Stop 的 JSON: %s", b)
	}
	b, _ = json.Marshal(Plan("claude", "/opt/railse")["PermissionRequest"])
	if string(b) != `[{"matcher":"","hooks":[{"type":"command","command":"/opt/railse hook claude","timeout":86400}]}]` {
		t.Fatalf("PermissionRequest 的 JSON: %s", b)
	}
}

func TestExpectedMatchesPlan(t *testing.T) {
	// Expected 是 doctor 的检查标准：事件集合与 Plan 写出的一致，Plan 写的超时满足下限
	for _, cli := range []string{"claude", "codex", "gemini"} {
		plan, exp := Plan(cli, "/opt/railse"), Expected(cli)
		if len(plan) != len(exp) {
			t.Fatalf("%s: Plan 有 %d 个事件, Expected 有 %d 个", cli, len(plan), len(exp))
		}
		for ev, min := range exp {
			groups, ok := plan[ev]
			if !ok {
				t.Fatalf("%s: Plan 缺 %s", cli, ev)
			}
			if got := groups[0].Hooks[0].Timeout; got < min {
				t.Fatalf("%s.%s: Plan 的超时 %d 低于下限 %d", cli, ev, got, min)
			}
		}
	}
	if Expected("other") != nil {
		t.Fatal("不认识的 CLI 应返回 nil")
	}
}

func TestMergeIntoEmptyAndIdempotent(t *testing.T) {
	bin := "/opt/railse"
	for _, empty := range [][]byte{nil, []byte(""), []byte("  \n")} {
		first, err := Merge(empty, bin, Plan("gemini", bin))
		if err != nil {
			t.Fatal(err)
		}
		second, err := Merge(first, bin, Plan("gemini", bin))
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(second) {
			t.Fatalf("重复合并结果应相同:\n%s\n---\n%s", first, second)
		}
		ins, _ := Inspect(second, bin)
		for ev := range Expected("gemini") {
			if !ins.Events[ev].Present {
				t.Fatalf("缺 %s: %s", ev, second)
			}
		}
	}
}

func TestMergeKeepsOthersAndReplacesOldBin(t *testing.T) {
	existing := `{"big":12345678901234567890,"hooks":{"enabled":true,` +
		`"PermissionRequest":[{"matcher":"","hooks":[{"type":"command","command":"/old/railse hook claude","timeout":86400},{"type":"command","command":"my-logger"}]}],` +
		`"PreToolUse":[{"hooks":[{"type":"command","command":"npm test && echo ok > /tmp/x"}]}]}}`
	out, err := Merge([]byte(existing), "/opt/railse", Plan("claude", "/opt/railse"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{`12345678901234567890`, `npm test && echo ok > /tmp/x`, `"enabled": true`, `my-logger`, `/opt/railse hook claude`} {
		if !strings.Contains(s, want) {
			t.Fatalf("缺 %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, "/old/railse") {
		t.Fatalf("旧路径的条目应被替换:\n%s", s)
	}
}

func TestMergeRejectsBroken(t *testing.T) {
	plan := Plan("claude", "/opt/railse")
	for _, existing := range []string{
		`{not json`,
		`{} {}`,
		`[]`,
		`null`,
		`{"hooks":[]}`,
		`{"hooks":"x"}`,
		`{"hooks":null}`,
		`{"hooks":{"Stop":"x"}}`,
	} {
		if out, err := Merge([]byte(existing), "/opt/railse", plan); err == nil {
			t.Errorf("%s: 应报错, 得到 %s", existing, out)
		}
	}
	// 只删不加时，不碰的事件不必是数组
	out, err := Merge([]byte(`{"hooks":{"Stop":"x","PermissionRequest":[{"hooks":[{"type":"command","command":"/opt/railse hook claude"}]}]}}`), "/opt/railse", nil)
	if err != nil || !strings.Contains(string(out), `"Stop": "x"`) || strings.Contains(string(out), "PermissionRequest") {
		t.Fatalf("只删: %s err=%v", out, err)
	}
}

func TestMergeRemoveOnlyKeepsOthers(t *testing.T) {
	bin := "/opt/railse"
	// 别人的条目还在：hooks 键保留，只去掉我们的
	doc, _ := Merge([]byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/opt/x","timeout":5}]}]}}`), bin, Plan("claude", bin))
	out, err := Merge(doc, bin, nil)
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	json.Unmarshal(out, &v)
	hooks := v["hooks"].(map[string]any)
	if len(hooks) != 1 || len(hooks["Stop"].([]any)) != 1 || !strings.Contains(string(out), "/opt/x") {
		t.Fatalf("应只剩别人的 Stop: %s", out)
	}
	// 没有 hooks 键时只删不加，不凭空加出 "hooks"
	out, err = Merge([]byte(`{"permissions":{}}`), bin, nil)
	if err != nil || strings.Contains(string(out), "hooks") {
		t.Fatalf("不应加出 hooks: %s err=%v", out, err)
	}
	// 原本就是空的 hooks 对象：不是我们删空的，原样保留
	out, err = Merge([]byte(`{"hooks":{}}`), bin, nil)
	if err != nil || !strings.Contains(string(out), `"hooks": {}`) {
		t.Fatalf("原本的空 hooks 应保留: %s err=%v", out, err)
	}
	// hooks 里只剩我们不碰的非数组值：不算删空
	out, err = Merge([]byte(`{"hooks":{"enabled":true,"Stop":[{"hooks":[{"type":"command","command":"/opt/railse hook claude"}]}]}}`), bin, nil)
	if err != nil || !strings.Contains(string(out), `"enabled": true`) || strings.Contains(string(out), "Stop") {
		t.Fatalf("应保留 enabled、删掉 Stop: %s err=%v", out, err)
	}
}

func TestInspectDetails(t *testing.T) {
	bin := "/my apps/railse"
	// 空文档：没有任何事件，不是错误
	for _, empty := range []string{"", "  ", "{}", `{"hooks":{}}`} {
		ins, err := Inspect([]byte(empty), bin)
		if err != nil || len(ins.Events) != 0 {
			t.Fatalf("%q: %+v err=%v", empty, ins, err)
		}
	}
	// 形状不对（读不出 hooks）报错；不碰的事件不是数组只是跳过
	for _, bad := range []string{`{} {}`, `[]`, `null`, `{"hooks":[]}`, `{"hooks":"x"}`, `{"hooks":null}`} {
		if _, err := Inspect([]byte(bad), bin); err == nil {
			t.Errorf("%s: 应报错", bad)
		}
	}
	ins, err := Inspect([]byte(`{"hooks":{"Stop":"x","enabled":true}}`), bin)
	if err != nil || len(ins.Events) != 0 {
		t.Fatalf("非数组事件应跳过: %+v err=%v", ins, err)
	}
	// 引号路径解出原路径；Gemini 按名字认；超时取我们条目里最小的
	doc, _ := Merge(nil, bin, Plan("gemini", bin))
	ins, err = Inspect(doc, bin)
	if err != nil {
		t.Fatal(err)
	}
	if st := ins.Events["BeforeTool"]; !st.Present || st.Timeout != 86400000 || st.Program != bin {
		t.Fatalf("BeforeTool: %+v", st)
	}
	if st := ins.Events["AfterAgent"]; !st.Present || st.Timeout != 0 {
		t.Fatalf("AfterAgent: %+v", st)
	}
	two := `{"hooks":{"PermissionRequest":[` +
		`{"hooks":[{"type":"command","command":"/opt/railse hook claude","timeout":86400}]},` +
		`{"hooks":[{"type":"command","command":"/opt/railse hook claude","timeout":60.0}]},` +
		`{"hooks":[{"type":"command","command":"/opt/x","timeout":1}]}]}}`
	ins, _ = Inspect([]byte(two), "/opt/railse")
	if st := ins.Events["PermissionRequest"]; !st.Present || st.Timeout != 60 || st.Program != "/opt/railse" {
		t.Fatalf("两份我们的条目时应报告较小的超时: %+v", st)
	}
}
