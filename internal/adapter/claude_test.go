package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/railse/railse-opc/internal/policy"
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
		{"claude/2.1.280/PreToolUse.json", KindIgnore, ""},
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
