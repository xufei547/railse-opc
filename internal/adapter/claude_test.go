package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xufei547/railse-opc/internal/policy"
)

func sample(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestClaudeParsePermissionRequest(t *testing.T) {
	a, ok := Get("claude")
	if !ok {
		t.Fatal("没有 claude 适配器")
	}
	in, err := a.Parse(sample(t, "claude/2.1.280/PermissionRequest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != KindPermission || in.ToolName != "Bash" {
		t.Fatalf("Kind=%s Tool=%s", in.Kind, in.ToolName)
	}
	if in.Session.Tool != "claude" || in.Session.CLISessionID != "62ca6ca9-7d34-4035-8b37-d6eae531ec2c" || in.Session.TurnID != "d90923b6-0fe8-4ee1-a729-967ccd797f73" {
		t.Fatalf("会话解析错误: %+v", in.Session)
	}
	wantCwd := "/Users/xufei/RailseWork/.claude/worktrees/dreamy-poitras-68f89d/spike/claude"
	if in.Session.Cwd != wantCwd || in.Payload.Cwd != wantCwd {
		t.Fatalf("cwd 错误: %q / %q", in.Session.Cwd, in.Payload.Cwd)
	}
	if in.Payload.Type != policy.PayloadShell || in.Payload.Command != "git push origin main" {
		t.Fatalf("载荷错误: %+v", in.Payload)
	}
}

func TestClaudeParseEvents(t *testing.T) {
	a, _ := Get("claude")
	cases := []struct {
		file string
		kind Kind
		msg  string
	}{
		{"claude/2.1.280/Notification.permission_prompt.json", KindQuestion, "Claude needs your permission"},
		{"claude/2.1.280/Notification.idle_prompt.json", KindQuestion, "Claude is waiting for your input"},
		{"claude/2.1.280/Stop.json", KindDone, "DONE"},
		{"claude/2.1.280/SessionEnd.json", KindSessionEnd, ""},
		{"claude/2.1.280/PreToolUse.json", KindPermission, ""},
	}
	for _, c := range cases {
		in, err := a.Parse(sample(t, c.file))
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		if in.Kind != c.kind || in.Message != c.msg {
			t.Errorf("%s: Kind=%s Message=%q, 想要 %s/%q", c.file, in.Kind, in.Message, c.kind, c.msg)
		}
		if in.Session.CLISessionID == "" {
			t.Errorf("%s: 会话号为空", c.file)
		}
	}
}

func TestClaudeToolMapping(t *testing.T) {
	a, _ := Get("claude")
	mk := func(tool string, input string) []byte {
		return []byte(`{"session_id":"s","prompt_id":"p","cwd":"/p","hook_event_name":"PermissionRequest","tool_name":"` + tool + `","tool_input":` + input + `}`)
	}
	cases := []struct {
		tool, input string
		want        policy.Payload
	}{
		{"Edit", `{"file_path":"/p/a.go","old_string":"x","new_string":"yy"}`, policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/a.go", DiffPreview: "yy", Cwd: "/p"}},
		{"Write", `{"file_path":"notes.md","content":"hello"}`, policy.Payload{Type: policy.PayloadFileWrite, Path: "notes.md", DiffPreview: "hello", Cwd: "/p"}},
		{"MultiEdit", `{"file_path":"/p/b.go","edits":[{"old_string":"a","new_string":"b"}]}`, policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/b.go", DiffPreview: "b", Cwd: "/p"}},
		{"NotebookEdit", `{"notebook_path":"/p/n.ipynb","new_source":"print(1)"}`, policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/n.ipynb", DiffPreview: "print(1)", Cwd: "/p"}},
		{"WebFetch", `{"url":"https://example.com/x","prompt":"summarize"}`, policy.Payload{Type: policy.PayloadNetwork, URL: "https://example.com/x", Method: "GET"}},
		{"WebSearch", `{"query":"golang"}`, policy.Payload{Type: policy.PayloadNetwork, URL: ""}},
		{"mcp__github__create_issue", `{"title":"t"}`, policy.Payload{Type: policy.PayloadMCP, Server: "github", Tool: "create_issue", Args: json.RawMessage(`{"title":"t"}`)}},
		{"Task", `{"prompt":"x"}`, policy.Payload{Type: PayloadOther, Tool: "Task"}},
	}
	for _, c := range cases {
		in, err := a.Parse(mk(c.tool, c.input))
		if err != nil {
			t.Errorf("%s: %v", c.tool, err)
			continue
		}
		got := in.Payload
		if got.Type != c.want.Type || got.Path != c.want.Path || got.DiffPreview != c.want.DiffPreview || got.URL != c.want.URL || got.Server != c.want.Server || got.Tool != c.want.Tool || string(got.Args) != string(c.want.Args) || got.Cwd != c.want.Cwd {
			t.Errorf("%s: 得到 %+v, 想要 %+v", c.tool, got, c.want)
		}
	}
}

func TestClaudeDiffPreviewClipped(t *testing.T) {
	a, _ := Get("claude")
	big := strings.Repeat("x", 10000)
	in, err := a.Parse([]byte(`{"session_id":"s","cwd":"/p","hook_event_name":"PermissionRequest","tool_name":"Write","tool_input":{"file_path":"a","content":"` + big + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Payload.DiffPreview) != 4096 {
		t.Fatalf("预览应截断到 4096 字节, 得到 %d", len(in.Payload.DiffPreview))
	}
}

func TestClaudeParseErrors(t *testing.T) {
	a, _ := Get("claude")
	if _, err := a.Parse([]byte(`not json`)); err == nil {
		t.Error("坏 JSON 应报错")
	}
	if _, err := a.Parse([]byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`)); err == nil {
		t.Error("缺 session_id 的审批请求应报错")
	}
	in, err := a.Parse([]byte(`{"session_id":"s","cwd":"/p","hook_event_name":"SomethingNew"}`))
	if err != nil || in.Kind != KindIgnore {
		t.Errorf("未知事件应为 ignore: %v %+v", err, in)
	}
}

func TestClaudeRender(t *testing.T) {
	a, _ := Get("claude")
	cases := []struct {
		d        Decision
		wantOut  string
		wantCode int
	}{
		{Decision{Action: "allow"}, `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`, 0},
		{Decision{Action: "deny", Reason: "命中规则 x"}, `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Railse：命中规则 x"}}}`, 0},
		{Decision{Action: "passthrough"}, ``, 0},
		{Decision{Action: "bogus"}, ``, 0},
	}
	for _, c := range cases {
		out := a.Render(c.d)
		if string(out.Stdout) != c.wantOut || out.ExitCode != c.wantCode {
			t.Errorf("Render(%+v) = %q/%d, 想要 %q/%d", c.d, out.Stdout, out.ExitCode, c.wantOut, c.wantCode)
		}
	}
	if a.DeadlineSeconds() != 86390 {
		t.Fatalf("期限 = %d", a.DeadlineSeconds())
	}
}

func TestClaudeToolInputTypeCollision(t *testing.T) {
	a, _ := Get("claude")
	mk := func(tool string, input string) []byte {
		return []byte(`{"session_id":"s","cwd":"/p","hook_event_name":"PermissionRequest","tool_name":"` + tool + `","tool_input":` + input + `}`)
	}
	in, err := a.Parse(mk("mcp__fs__write", `{"content":[1,2]}`))
	if err != nil {
		t.Fatalf("MCP 参数里的同名异型字段不应报错: %v", err)
	}
	if in.Payload.Type != policy.PayloadMCP || in.Payload.Server != "fs" || in.Payload.Tool != "write" || string(in.Payload.Args) != `{"content":[1,2]}` {
		t.Fatalf("MCP 载荷错误: %+v", in.Payload)
	}
	in, err = a.Parse(mk("Task", `{"prompt":{"a":1}}`))
	if err != nil {
		t.Fatalf("未知工具不应解析 tool_input: %v", err)
	}
	if in.Payload.Type != PayloadOther || in.Payload.Tool != "Task" {
		t.Fatalf("未知工具载荷错误: %+v", in.Payload)
	}
}

func TestClaudeClipKeepsUTF8(t *testing.T) {
	a, _ := Get("claude")
	content := strings.Repeat("x", 4095) + strings.Repeat("中", 10)
	in, err := a.Parse([]byte(`{"session_id":"s","cwd":"/p","hook_event_name":"PermissionRequest","tool_name":"Write","tool_input":{"file_path":"a","content":"` + content + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	p := in.Payload.DiffPreview
	if !utf8.ValidString(p) || len(p) > 4096 || len(p) <= 4096-utf8.UTFMax {
		t.Errorf("预览应退到字符边界: 合法=%v 长度=%d", utf8.ValidString(p), len(p))
	}

	msg := strings.Repeat("已完成重构", 50) // 750 字节
	in, err = a.Parse([]byte(`{"session_id":"s","cwd":"/p","hook_event_name":"Stop","last_assistant_message":"` + msg + `"}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != KindDone || !utf8.ValidString(in.Message) || len(in.Message) > 500 || len(in.Message) <= 500-utf8.UTFMax {
		t.Fatalf("Stop 消息应退到字符边界: Kind=%s 合法=%v 长度=%d", in.Kind, utf8.ValidString(in.Message), len(in.Message))
	}
}

func TestClaudeRenderDenyEmptyReason(t *testing.T) {
	a, _ := Get("claude")
	want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Railse：已拒绝"}}}`
	if out := a.Render(Decision{Action: "deny"}); string(out.Stdout) != want || out.ExitCode != 0 {
		t.Fatalf("空理由的拒绝 = %q/%d, 想要 %q/0", out.Stdout, out.ExitCode, want)
	}
}

func TestClaudeParsePreToolUseBash(t *testing.T) {
	for _, f := range []string{"claude/pretooluse-bash-default.json", "claude/pretooluse-bash-auto.json"} {
		in, err := claude{}.Parse(sample(t, f))
		if err != nil {
			t.Fatal(f, err)
		}
		if in.Kind != KindPermission || in.ToolName != "Bash" || in.Payload.Type != policy.PayloadShell {
			t.Fatalf("%s: kind=%s tool=%s type=%s", f, in.Kind, in.ToolName, in.Payload.Type)
		}
		if in.Source != SourcePreToolUse || in.ToolUseID == "" || (in.PermissionMode != "default" && in.PermissionMode != "auto") {
			t.Fatalf("%s: source=%s id=%q mode=%q", f, in.Source, in.ToolUseID, in.PermissionMode)
		}
	}
	// PermissionRequest 的来源与模式也要带上；2.1.280 的 PermissionRequest 没有 tool_use_id
	in, err := claude{}.Parse(sample(t, "claude/2.1.280/PermissionRequest.json"))
	if err != nil || in.Source != SourcePermissionRequest || in.PermissionMode != "default" || in.ToolUseID != "" {
		t.Fatalf("PermissionRequest: %+v %v", in, err)
	}
}

func TestClaudeParsePreToolUseTools(t *testing.T) {
	mk := func(tool, input string) []byte {
		return []byte(`{"session_id":"s","cwd":"/p","permission_mode":"acceptEdits","hook_event_name":"PreToolUse","tool_name":"` + tool + `","tool_input":` + input + `,"tool_use_id":"toolu_x"}`)
	}
	cases := []struct {
		tool, input string
		typ         policy.PayloadType
		path, url   string
	}{
		{"Edit", `{"file_path":"/p/a.go","old_string":"a","new_string":"b"}`, policy.PayloadFileWrite, "/p/a.go", ""},
		{"Write", `{"file_path":"/p/b.go","content":"x"}`, policy.PayloadFileWrite, "/p/b.go", ""},
		{"MultiEdit", `{"file_path":"/p/c.go","edits":[{"old_string":"a","new_string":"b"}]}`, policy.PayloadFileWrite, "/p/c.go", ""},
		{"NotebookEdit", `{"notebook_path":"/p/n.ipynb","new_source":"x"}`, policy.PayloadFileWrite, "/p/n.ipynb", ""},
		{"WebFetch", `{"url":"https://x.example/","prompt":"p"}`, policy.PayloadNetwork, "", "https://x.example/"},
	}
	for _, c := range cases {
		in, err := claude{}.Parse(mk(c.tool, c.input))
		if err != nil {
			t.Fatal(c.tool, err)
		}
		if in.Kind != KindPermission || in.Payload.Type != c.typ || in.Payload.Path != c.path || in.Payload.URL != c.url {
			t.Errorf("%s: %+v", c.tool, in)
		}
		if in.Source != SourcePreToolUse || in.PermissionMode != "acceptEdits" || in.ToolUseID != "toolu_x" {
			t.Errorf("%s: source=%s mode=%s id=%s", c.tool, in.Source, in.PermissionMode, in.ToolUseID)
		}
	}
	a := claude{}
	if _, err := a.Parse([]byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`)); err == nil {
		t.Fatal("缺 session_id 应报错")
	}
}

func TestClaudeRenderPreToolUse(t *testing.T) {
	cases := map[string]string{
		"allow":       `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"Railse：命中规则 r1"}}`,
		"deny":        `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Railse：已在手机上拒绝"}}`,
		"ask":         `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"Railse：连不上 hub"}}`,
		"passthrough": ``, // 不表态：ask 在 auto 下会强制弹框，见 Task 1 实测
	}
	for action, want := range cases {
		reason := map[string]string{"allow": "命中规则 r1", "deny": "已在手机上拒绝", "ask": "连不上 hub"}[action]
		out := claude{}.Render(Decision{Source: SourcePreToolUse, Action: action, Reason: reason})
		if string(out.Stdout) != want || out.ExitCode != 0 {
			t.Errorf("%s: got %s want %s", action, out.Stdout, want)
		}
	}
	// 没有来源（PermissionRequest）照旧
	out := claude{}.Render(Decision{Action: "allow"})
	if !strings.Contains(string(out.Stdout), `"hookEventName":"PermissionRequest"`) {
		t.Errorf("PermissionRequest 输出变了: %s", out.Stdout)
	}
	// 放行理由为空时有固定文案
	out = claude{}.Render(Decision{Source: SourcePreToolUse, Action: "allow"})
	if !strings.Contains(string(out.Stdout), `"permissionDecisionReason":"Railse：已放行"`) {
		t.Errorf("空理由: %s", out.Stdout)
	}
}

func TestClaudePreToolUseCarriesDescription(t *testing.T) {
	a, _ := Get("claude")
	raw := sample(t, "claude/pretooluse-bash-default.json")
	in, err := a.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if in.Payload.Description != "Print hi-default" {
		t.Fatalf("description 没带上：%q", in.Payload.Description)
	}
	// 超长的说明截断到 200 字节内，且不切坏字符
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["tool_input"] = map[string]any{"command": "echo", "description": strings.Repeat("长", 200)}
	raw, _ = json.Marshal(m)
	in, err = a.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(in.Payload.Description); n > 200 || n == 0 || !utf8.ValidString(in.Payload.Description) {
		t.Fatalf("说明应截到 200 字节内：%d", n)
	}
}

// PostToolUse 是事件：工具跑完了，带会话、工具名、与审批同一算法的载荷和 tool_use_id；AskUserQuestion 与别的工具忽略。
func TestClaudeParsePostToolUse(t *testing.T) {
	a, _ := Get("claude")
	raw := `{"session_id":"s1","prompt_id":"p1","cwd":"/p","permission_mode":"auto","hook_event_name":"PostToolUse","tool_name":"Bash",` +
		`"tool_input":{"command":"git push origin main","description":"Push"},"tool_response":{"stdout":"ok"},"tool_use_id":"toolu_9"}`
	in, err := a.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	want := policy.Payload{Type: policy.PayloadShell, Command: "git push origin main", Cwd: "/p", Description: "Push"}
	if in.Kind != KindToolDone || in.ToolName != "Bash" || in.ToolUseID != "toolu_9" || in.Session.CLISessionID != "s1" || !reflect.DeepEqual(in.Payload, want) {
		t.Fatalf("PostToolUse: %+v", in)
	}
	// 与同一调用的 PermissionRequest 算出的载荷相同（守护进程靠它对上）
	pr := strings.Replace(raw, `"hook_event_name":"PostToolUse"`, `"hook_event_name":"PermissionRequest"`, 1)
	if p, err := a.Parse([]byte(pr)); err != nil || !reflect.DeepEqual(p.Payload, in.Payload) {
		t.Fatalf("载荷应与 PermissionRequest 一致: %+v %v", p.Payload, err)
	}
	for _, tool := range []string{"AskUserQuestion", "mcp__github__get_issue"} {
		other := strings.Replace(raw, `"tool_name":"Bash"`, `"tool_name":"`+tool+`"`, 1)
		if in, err := a.Parse([]byte(other)); err != nil || in.Kind != KindIgnore {
			t.Fatalf("%s 应忽略: %+v %v", tool, in, err)
		}
	}
}
