package adapter

import (
	"testing"

	"github.com/railse/railse-opc/internal/policy"
)

func TestCodexParsePermissionRequest(t *testing.T) {
	a, ok := Get("codex")
	if !ok {
		t.Fatal("没有 codex 适配器")
	}
	in, err := a.Parse(sample(t, "codex/0.145.0/PermissionRequest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != KindPermission || in.ToolName != "Bash" {
		t.Fatalf("Kind=%s Tool=%s", in.Kind, in.ToolName)
	}
	if in.Session.Tool != "codex" || in.Session.CLISessionID != "01a15d8c-e149-77e3-94fc-1b693482f9b7" || in.Session.TurnID != "01a15d8c-e330-7c93-8bed-9553cbb63f95" {
		t.Fatalf("会话解析错误: %+v", in.Session)
	}
	if in.Payload.Type != policy.PayloadShell || in.Payload.Command != "touch spike-proof.txt" || in.Payload.Cwd != in.Session.Cwd {
		t.Fatalf("载荷错误: %+v", in.Payload)
	}
}

func TestCodexParseEvents(t *testing.T) {
	a, _ := Get("codex")
	cases := []struct {
		file string
		kind Kind
		msg  string
	}{
		{"codex/0.145.0/Stop.json", KindDone, "DONE"},
		{"codex/0.145.0/SessionEnd.json", KindSessionEnd, ""},
		{"codex/0.145.0/PreToolUse.json", KindIgnore, ""},
	}
	for _, c := range cases {
		in, err := a.Parse(sample(t, c.file))
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		if in.Kind != c.kind || in.Message != c.msg {
			t.Errorf("%s: Kind=%s Message=%q", c.file, in.Kind, in.Message)
		}
	}
}

func TestCodexNotify(t *testing.T) {
	in, err := ParseCodexNotify(sample(t, "codex/0.145.0/notify.agent-turn-complete.json"))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != KindDone || in.Message != "DONE" {
		t.Fatalf("Kind=%s Message=%q", in.Kind, in.Message)
	}
	if in.Session.Tool != "codex" || in.Session.CLISessionID != "01a15d8c-e149-77e3-94fc-1b693482f9b7" || in.Session.TurnID != "01a15d8c-e330-7c93-8bed-9553cbb63f95" || in.Session.Cwd == "" {
		t.Fatalf("会话解析错误: %+v", in.Session)
	}
	if in, err := ParseCodexNotify([]byte(`{"type":"something-else"}`)); err != nil || in.Kind != KindIgnore {
		t.Fatalf("其他 notify 类型应为 ignore: %v %+v", err, in)
	}
}

func TestCodexRender(t *testing.T) {
	a, _ := Get("codex")
	if got := string(a.Render(Decision{Action: "allow"}).Stdout); got != `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` {
		t.Fatalf("allow = %s", got)
	}
	if got := string(a.Render(Decision{Action: "deny", Reason: "r"}).Stdout); got != `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Railse：r"}}}` {
		t.Fatalf("deny = %s", got)
	}
	if out := a.Render(Decision{Action: "passthrough"}); len(out.Stdout) != 0 || out.ExitCode != 0 {
		t.Fatalf("passthrough = %+v", out)
	}
	if a.DeadlineSeconds() != 86390 {
		t.Fatalf("期限 = %d", a.DeadlineSeconds())
	}
}
