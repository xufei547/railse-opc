package adapter

import (
	"testing"

	"github.com/xufei547/railse-opc/internal/policy"
)

func TestGeminiParseBeforeTool(t *testing.T) {
	a, ok := Get("gemini")
	if !ok {
		t.Fatal("没有 gemini 适配器")
	}
	for _, f := range []string{"gemini/0.65.0/BeforeTool.json", "gemini/0.28.0/BeforeTool.json"} {
		in, err := a.Parse(sample(t, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if in.Kind != KindPermission || in.ToolName != "run_shell_command" || in.Session.Tool != "gemini" || in.Session.CLISessionID == "" {
			t.Fatalf("%s: %+v", f, in)
		}
		if in.Payload.Type != policy.PayloadShell || in.Payload.Command != "touch spike-proof.txt" || in.Payload.Cwd != in.Session.Cwd {
			t.Fatalf("%s 载荷错误: %+v", f, in.Payload)
		}
	}
}

func TestGeminiParseEvents(t *testing.T) {
	a, _ := Get("gemini")
	cases := []struct {
		file string
		kind Kind
		msg  string
	}{
		{"gemini/0.65.0/Notification.ToolPermission.json", KindQuestion, "Tool Confirm Shell Command requires execution: touch spike-proof.txt"},
		{"gemini/0.65.0/AfterAgent.json", KindDone, "DONE"},
		{"gemini/0.65.0/SessionEnd.json", KindSessionEnd, ""},
		{"gemini/0.65.0/AfterTool.json", KindIgnore, ""},
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
	}
}

func TestGeminiToolMapping(t *testing.T) {
	a, _ := Get("gemini")
	mk := func(tool, input string) []byte {
		return []byte(`{"session_id":"s","cwd":"/p","hook_event_name":"BeforeTool","tool_name":"` + tool + `","tool_input":` + input + `}`)
	}
	cases := []struct {
		tool, input string
		typ         policy.PayloadType
		check       func(policy.Payload) bool
	}{
		{"write_file", `{"file_path":"/p/a.txt","content":"hi"}`, policy.PayloadFileWrite, func(p policy.Payload) bool { return p.Path == "/p/a.txt" && p.DiffPreview == "hi" }},
		{"replace", `{"file_path":"b.go","old_string":"x","new_string":"y"}`, policy.PayloadFileWrite, func(p policy.Payload) bool { return p.Path == "b.go" && p.DiffPreview == "y" }},
		{"web_fetch", `{"prompt":"fetch https://example.com/a and summarize"}`, policy.PayloadNetwork, func(p policy.Payload) bool { return p.URL == "https://example.com/a" }},
		{"google_web_search", `{"query":"q"}`, policy.PayloadNetwork, func(p policy.Payload) bool { return p.URL == "" }},
		{"mcp_github_create_issue", `{"t":1}`, policy.PayloadMCP, func(p policy.Payload) bool { return p.Server == "github" && p.Tool == "create_issue" }},
		{"github__create_issue", `{"t":1}`, policy.PayloadMCP, func(p policy.Payload) bool { return p.Server == "github" && p.Tool == "create_issue" }},
		{"ask_user", `{"q":"?"}`, PayloadOther, func(p policy.Payload) bool { return p.Tool == "ask_user" }},
	}
	for _, c := range cases {
		in, err := a.Parse(mk(c.tool, c.input))
		if err != nil {
			t.Errorf("%s: %v", c.tool, err)
			continue
		}
		if in.Payload.Type != c.typ || !c.check(in.Payload) {
			t.Errorf("%s: %+v", c.tool, in.Payload)
		}
	}
}

func TestGeminiRenderNeverAllowsOrAsks(t *testing.T) {
	a, _ := Get("gemini")
	if got := string(a.Render(Decision{Action: "deny", Reason: "r"}).Stdout); got != `{"decision":"deny","reason":"Railse：r"}` {
		t.Fatalf("deny = %s", got)
	}
	for _, act := range []string{"allow", "passthrough", "ask", "bogus"} {
		out := a.Render(Decision{Action: act})
		if len(out.Stdout) != 0 || out.ExitCode != 0 {
			t.Fatalf("%s 应渲染为不作决定, 得到 %+v", act, out)
		}
	}
	if a.DeadlineSeconds() != 86390 {
		t.Fatalf("期限 = %d", a.DeadlineSeconds())
	}
}
