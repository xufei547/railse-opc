package adapter

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/proto"
)

// 2026-10-31 在 Claude Code 2.1.280 真机抓到的 AskUserQuestion 的 tool_input。
const askToolInput = `{"questions":[{"question":"你更喜欢苹果还是香蕉？","header":"水果","options":[{"label":"Apple","description":"苹果"},{"label":"Banana","description":"香蕉"}],"multiSelect":false}]}`

func askHook(event, tool, toolInput string) []byte {
	return []byte(`{"session_id":"s1","prompt_id":"p1","cwd":"/p","hook_event_name":"` + event + `","tool_name":"` + tool + `","tool_use_id":"tu1","tool_input":` + toolInput + `}`)
}

func TestClaudeParseAsk(t *testing.T) {
	a, _ := Get("claude")
	in, err := a.Parse(askHook("PreToolUse", "AskUserQuestion", askToolInput))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != KindAsk || in.ToolName != "AskUserQuestion" {
		t.Fatalf("Kind=%s Tool=%s", in.Kind, in.ToolName)
	}
	if in.Session.Tool != "claude" || in.Session.CLISessionID != "s1" || in.Session.TurnID != "p1" || in.Session.Cwd != "/p" {
		t.Fatalf("会话解析错误: %+v", in.Session)
	}
	want := &proto.Ask{Questions: []proto.Question{{
		Question: "你更喜欢苹果还是香蕉？", Header: "水果",
		Options: []proto.Option{{Label: "Apple", Description: "苹果"}, {Label: "Banana", Description: "香蕉"}},
	}}}
	if !reflect.DeepEqual(in.Ask, want) {
		t.Fatalf("Ask = %+v, 想要 %+v", in.Ask, want)
	}
	if string(in.RawToolInput) != askToolInput {
		t.Fatalf("RawToolInput 应是原文: %s", in.RawToolInput)
	}
}

func TestClaudeParseAskVariants(t *testing.T) {
	a, _ := Get("claude")
	multi := `{"questions":[{"question":"q1","options":[{"label":"a"}]},{"question":"q2","header":"h","options":[{"label":"b"},{"label":"c"}]}]}`
	in, err := a.Parse(askHook("PreToolUse", "AskUserQuestion", multi))
	if err != nil || in.Kind != KindAsk || len(in.Ask.Questions) != 2 || in.Ask.Questions[1].Header != "h" {
		t.Fatalf("多题: %v %+v", err, in)
	}
	ms := `{"questions":[{"question":"选几个","options":[{"label":"x"},{"label":"y"}],"multiSelect":true}]}`
	in, err = a.Parse(askHook("PreToolUse", "AskUserQuestion", ms))
	if err != nil || in.Kind != KindAsk || !in.Ask.Questions[0].MultiSelect {
		t.Fatalf("多选题: %v %+v", err, in)
	}
	for name, raw := range map[string]string{
		"空数组 options": `{"questions":[{"question":"q","options":[]}]}`,
		"没有 options":  `{"questions":[{"question":"q"}]}`,
	} {
		in, err = a.Parse(askHook("PreToolUse", "AskUserQuestion", raw))
		if err != nil || in.Kind != KindAsk || len(in.Ask.Questions[0].Options) != 0 {
			t.Errorf("%s 应合规: %v %+v", name, err, in)
		}
	}
}

func TestClaudeParseAskRejected(t *testing.T) {
	a, _ := Get("claude")
	q := func(n int) string {
		items := make([]string, n)
		for i := range items {
			items[i] = `{"question":"q","options":[{"label":"a"}]}`
		}
		return `{"questions":[` + strings.Join(items, ",") + `]}`
	}
	manyOpts := make([]string, 9)
	for i := range manyOpts {
		manyOpts[i] = `{"label":"a"}`
	}
	cases := map[string]string{
		"5 题":             q(5),
		"0 题":             `{"questions":[]}`,
		"没有 questions":    `{}`,
		"question 为空":     `{"questions":[{"question":"","options":[{"label":"a"}]}]}`,
		"question 过长":     `{"questions":[{"question":"` + strings.Repeat("中", 2001) + `","options":[]}]}`,
		"9 个选项":           `{"questions":[{"question":"q","options":[` + strings.Join(manyOpts, ",") + `]}]}`,
		"label 为空":        `{"questions":[{"question":"q","options":[{"label":""}]}]}`,
		"label 过长":        `{"questions":[{"question":"q","options":[{"label":"` + strings.Repeat("a", 201) + `"}]}]}`,
		"description 过长":  `{"questions":[{"question":"q","options":[{"label":"a","description":"` + strings.Repeat("中", 2001) + `"}]}]}`,
		"header 过长":       `{"questions":[{"question":"q","header":"` + strings.Repeat("h", 61) + `","options":[]}]}`,
		"tool_input 不是对象": `"oops"`,
		"questions 类型不对":  `{"questions":"x"}`,
	}
	for name, raw := range cases {
		if _, err := a.Parse(askHook("PreToolUse", "AskUserQuestion", raw)); err == nil {
			t.Errorf("%s 应返回错误", name)
		}
	}
	if _, err := a.Parse([]byte(`{"hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":` + askToolInput + `}`)); err == nil {
		t.Error("缺 session_id 的 ask 应报错")
	}
	// 上限本身合规：4 题、8 个选项、question 2000 字符、label 200、description 2000、header 60。
	ok := `{"questions":[{"question":"` + strings.Repeat("中", 2000) + `","header":"` + strings.Repeat("h", 60) + `","options":[{"label":"` + strings.Repeat("a", 200) + `","description":"` + strings.Repeat("中", 2000) + `"},{"label":"b"},{"label":"c"},{"label":"d"},{"label":"e"},{"label":"f"},{"label":"g"},{"label":"h"}]}]}`
	if in, err := a.Parse(askHook("PreToolUse", "AskUserQuestion", ok)); err != nil || in.Kind != KindAsk {
		t.Errorf("上限值应合规: %v %+v", err, in)
	}
	if in, err := a.Parse(askHook("PreToolUse", "AskUserQuestion", q(4))); err != nil || in.Kind != KindAsk {
		t.Errorf("4 题应合规: %v %+v", err, in)
	}
}

func TestClaudeParseAskIgnored(t *testing.T) {
	a, _ := Get("claude")
	for name, raw := range map[string][]byte{
		"PreToolUse 别的工具":                   askHook("PreToolUse", "Bash", `{"command":"ls"}`),
		"PermissionRequest+AskUserQuestion": askHook("PermissionRequest", "AskUserQuestion", askToolInput),
	} {
		in, err := a.Parse(raw)
		if err != nil || in.Kind != KindIgnore {
			t.Errorf("%s 应忽略: %v %+v", name, err, in)
		}
	}
}

func TestClaudeRenderAnswer(t *testing.T) {
	a, _ := Get("claude")
	out := a.Render(Decision{Action: "answer", RawToolInput: json.RawMessage(askToolInput), Answers: map[string]string{"你更喜欢苹果还是香蕉？": "Banana"}})
	if out.ExitCode != 0 {
		t.Fatalf("退出码 = %d", out.ExitCode)
	}
	want := `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"questions":[{"question":"你更喜欢苹果还是香蕉？","header":"水果","options":[{"label":"Apple","description":"苹果"},{"label":"Banana","description":"香蕉"}],"multiSelect":false}],"answers":{"你更喜欢苹果还是香蕉？":"Banana"}}}}`
	var got, exp any
	if err := json.Unmarshal(out.Stdout, &got); err != nil {
		t.Fatalf("输出不是 JSON: %v: %s", err, out.Stdout)
	}
	_ = json.Unmarshal([]byte(want), &exp)
	if !reflect.DeepEqual(got, exp) {
		t.Fatalf("输出 = %s, 想要 %s", out.Stdout, want)
	}
}

func TestClaudeRenderAnswerPassthrough(t *testing.T) {
	a, _ := Get("claude")
	for name, d := range map[string]Decision{
		"没有 RawToolInput":      {Action: "answer", Answers: map[string]string{"q": "a"}},
		"RawToolInput 不是对象":    {Action: "answer", RawToolInput: json.RawMessage(`[1]`), Answers: map[string]string{"q": "a"}},
		"RawToolInput 不是 JSON": {Action: "answer", RawToolInput: json.RawMessage(`{oops`), Answers: map[string]string{"q": "a"}},
	} {
		if out := a.Render(d); len(out.Stdout) != 0 || out.ExitCode != 0 {
			t.Errorf("%s 应 passthrough, 得到 %q/%d", name, out.Stdout, out.ExitCode)
		}
	}
	// 非 answer 的动作照旧走 PermissionRequest 渲染，不看 RawToolInput。
	out := a.Render(Decision{Action: "allow", RawToolInput: json.RawMessage(askToolInput)})
	if string(out.Stdout) != `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` {
		t.Errorf("allow 输出 = %s", out.Stdout)
	}
}

// rawFields 取出 JSON 对象各键的原始字节。
func rawFields(t *testing.T, b []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPreToolUseAnswerKeepsBytes(t *testing.T) {
	// questions 里有 < > &、中文、转义引号与 U+2028；extra 是 Go 不认识的多余字段。
	questions := `[{"question":"a<b & c>d \"引用\"   完","options":[{"label":"<x>","description":"&amp;"}]}]`
	extra := `{"a":[1,2]}`
	raw := `{"questions":` + questions + `,"extra":` + extra + `,"answers":{"旧":"旧答案"}}`
	out, err := preToolUseAnswer(json.RawMessage(raw), map[string]string{"a<b & c>d": "<x> & 中文"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.ContainsAny(out, "\n") {
		t.Fatalf("输出不应带换行: %q", out)
	}
	var top struct {
		HSO struct {
			HookEventName string          `json:"hookEventName"`
			Decision      string          `json:"permissionDecision"`
			Updated       json.RawMessage `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	if top.HSO.HookEventName != "PreToolUse" || top.HSO.Decision != "allow" {
		t.Fatalf("外层字段错误: %+v", top.HSO)
	}
	upd, orig := rawFields(t, top.HSO.Updated), rawFields(t, []byte(raw))
	if !bytes.Equal(upd["questions"], orig["questions"]) || string(upd["questions"]) != questions {
		t.Fatalf("questions 不是逐字节原样:\n得到 %s\n想要 %s", upd["questions"], questions)
	}
	if !bytes.Equal(upd["extra"], orig["extra"]) {
		t.Fatalf("extra 不是逐字节原样: %s", upd["extra"])
	}
	var answers map[string]string
	if err := json.Unmarshal(upd["answers"], &answers); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(answers, map[string]string{"a<b & c>d": "<x> & 中文"}) {
		t.Fatalf("answers 应覆盖旧值: %v", answers)
	}
	if len(upd) != 3 {
		t.Fatalf("updatedInput 键数 = %d, 想要 3", len(upd))
	}
}

func TestPreToolUseAnswerErrors(t *testing.T) {
	for name, raw := range map[string]string{"数组": `[1]`, "字符串": `"x"`, "坏 JSON": `{oops`, "空": ``} {
		if _, err := preToolUseAnswer(json.RawMessage(raw), map[string]string{"q": "a"}); err == nil {
			t.Errorf("%s 应返回错误", name)
		}
	}
}
