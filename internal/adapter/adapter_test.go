package adapter

import (
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
	"github.com/xufei547/railse-opc/internal/proto"
)

func TestDenyMessageEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	if got := denyMessage(""); got != "Railse: denied" {
		t.Fatalf("got %q", got)
	}
	if got := denyMessage("Matched rule deny-rm"); got != "Railse: Matched rule deny-rm" {
		t.Fatalf("got %q", got)
	}
}

// 英文下 ValidateAsk 与各家 Parse 的报错不带中文（钩子把它们写进 stderr）。
func TestErrorsEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	q := func(s string, opts ...proto.Option) proto.Question { return proto.Question{Question: s, Options: opts} }
	long := strings.Repeat("x", 2001)
	asks := map[string]*proto.Ask{
		"invalid AskUserQuestion: no questions":                                            {},
		"invalid AskUserQuestion: 5 questions, at most 4":                                  {Questions: []proto.Question{q("a"), q("b"), q("c"), q("d"), q("e")}},
		"invalid AskUserQuestion: question 1 is empty":                                     {Questions: []proto.Question{q("")}},
		"invalid AskUserQuestion: question 1 is over 2000 characters":                      {Questions: []proto.Question{q(long)}},
		"invalid AskUserQuestion: question 2 is the same as question 1":                    {Questions: []proto.Question{q("a"), q("a")}},
		"invalid AskUserQuestion: question 1 header is over 60 characters":                 {Questions: []proto.Question{{Question: "a", Header: strings.Repeat("h", 61)}}},
		"invalid AskUserQuestion: question 1 has 9 options, at most 8":                     {Questions: []proto.Question{q("a", make([]proto.Option, 9)...)}},
		"invalid AskUserQuestion: question 1 option 1 has an empty label":                  {Questions: []proto.Question{q("a", proto.Option{})}},
		"invalid AskUserQuestion: question 1 option 1 label is over 200 characters":        {Questions: []proto.Question{q("a", proto.Option{Label: strings.Repeat("l", 201)})}},
		"invalid AskUserQuestion: question 1 option 1 description is over 2000 characters": {Questions: []proto.Question{q("a", proto.Option{Label: "l", Description: long})}},
	}
	for want, a := range asks {
		if err := ValidateAsk(a); err == nil || err.Error() != want {
			t.Errorf("ValidateAsk 报错 = %v，想要 %q", err, want)
		}
	}
	parses := []struct {
		cli, stdin, want string
	}{
		{"claude", "x", "can't parse the Claude hook input: "},
		{"claude", `{"hook_event_name":"PermissionRequest"}`, "PermissionRequest is missing session_id or tool_name"},
		{"claude", `{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion"}`, "PreToolUse is missing session_id"},
		{"claude", `{"session_id":"s","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":"x"}`, "invalid AskUserQuestion: can't parse tool_input: "},
		{"claude", `{"session_id":"s","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":1}}`, "can't parse tool_input: "},
		{"codex", "x", "can't parse the Codex hook input: "},
		{"codex", `{"hook_event_name":"PermissionRequest"}`, "PermissionRequest is missing session_id or tool_name"},
		{"gemini", "x", "can't parse the Gemini hook input: "},
		{"gemini", `{"hook_event_name":"BeforeTool"}`, "BeforeTool is missing session_id or tool_name"},
	}
	for _, p := range parses {
		a, _ := Get(p.cli)
		_, err := a.Parse([]byte(p.stdin))
		if err == nil || !strings.HasPrefix(err.Error(), p.want) || hasCJK(err.Error()) {
			t.Errorf("%s 解析 %s 的报错 = %v，想要以 %q 开头且不带中文", p.cli, p.stdin, err, p.want)
		}
	}
	if _, err := ParseCodexNotify([]byte("x")); err == nil || !strings.HasPrefix(err.Error(), "can't parse the Codex notify argument: ") {
		t.Errorf("ParseCodexNotify 的报错 = %v", err)
	}
}

func hasCJK(s string) bool {
	for _, r := range s {
		if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0xFF00 && r <= 0xFFEF) || (r >= 0x3000 && r <= 0x303F) {
			return true
		}
	}
	return false
}
