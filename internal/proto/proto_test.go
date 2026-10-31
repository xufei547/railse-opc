package proto

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/policy"
)

func TestRequestRoundTrip(t *testing.T) {
	in := Request{V: Version, Kind: "permission",
		Session:         Session{Tool: "claude", CLISessionID: "s1", TurnID: "p1", Cwd: "/p"},
		ToolName:        "Bash",
		Payload:         policy.Payload{Type: policy.PayloadShell, Command: "git status", Cwd: "/p"},
		DeadlineSeconds: 86390}
	var buf bytes.Buffer
	if err := WriteLine(&buf, in); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "\n") || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("应恰好一行并以换行结尾: %q", buf.String())
	}
	line, err := ReadLine(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	kind, err := PeekKind(line)
	if err != nil || kind != "permission" {
		t.Fatalf("PeekKind = %q, %v", kind, err)
	}
	var out Request
	if err := Decode(line, &out); err != nil {
		t.Fatal(err)
	}
	if out.Session.CLISessionID != "s1" || out.Payload.Command != "git status" || out.DeadlineSeconds != 86390 {
		t.Fatalf("往返不一致: %+v", out)
	}
}

func TestReadLineTooLong(t *testing.T) {
	big := strings.Repeat("x", MaxLine+1) + "\n"
	_, err := ReadLine(bufio.NewReader(strings.NewReader(big)))
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, 想要 ErrLineTooLong", err)
	}
}

func TestReadLineEOFWithoutNewline(t *testing.T) {
	line, err := ReadLine(bufio.NewReader(strings.NewReader(`{"v":1,"kind":"control","op":"status"}`)))
	if err != nil {
		t.Fatalf("无换行的最后一行也应能读: %v", err)
	}
	if kind, _ := PeekKind(line); kind != "control" {
		t.Fatalf("kind = %q", kind)
	}
}

func TestDecodeRejectsWrongVersion(t *testing.T) {
	var c Control
	err := Decode([]byte(`{"v":2,"kind":"control","op":"status"}`), &c)
	if err == nil || !strings.Contains(err.Error(), "协议版本") {
		t.Fatalf("应拒绝版本 2: %v", err)
	}
}

func TestPeekKindBadJSON(t *testing.T) {
	if _, err := PeekKind([]byte("not json")); err == nil {
		t.Fatal("应报错")
	}
}

func TestAskRoundTrip(t *testing.T) {
	in := Request{V: Version, Kind: "ask",
		Session:  Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"},
		ToolName: "AskUserQuestion",
		Ask: &Ask{Questions: []Question{{
			Question: "你更喜欢苹果还是香蕉？", Header: "水果", MultiSelect: true,
			Options: []Option{{Label: "Apple", Description: "苹果"}, {Label: "Banana"}},
		}}},
		DeadlineSeconds: 86390}
	var buf bytes.Buffer
	if err := WriteLine(&buf, in); err != nil {
		t.Fatal(err)
	}
	line, err := ReadLine(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	var out Request
	if err := Decode(line, &out); err != nil {
		t.Fatal(err)
	}
	q := out.Ask.Questions[0]
	if out.Kind != "ask" || q.Question != "你更喜欢苹果还是香蕉？" || q.Header != "水果" || !q.MultiSelect || len(q.Options) != 2 || q.Options[0].Description != "苹果" || q.Options[1].Description != "" {
		t.Fatalf("往返不一致: %+v", out)
	}
	// 非 ask 请求不带 ask 键。
	b, _ := json.Marshal(Request{V: Version, Kind: "permission"})
	if strings.Contains(string(b), `"ask"`) {
		t.Fatalf("permission 请求不应有 ask 键: %s", b)
	}
}

func TestResponseAnswers(t *testing.T) {
	b, err := json.Marshal(Response{V: Version, Decision: "answer", Answers: map[string]string{"问": "答"}})
	if err != nil {
		t.Fatal(err)
	}
	var out Response
	if err := Decode(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Decision != "answer" || out.Answers["问"] != "答" {
		t.Fatalf("往返不一致: %+v", out)
	}
	b, _ = json.Marshal(Response{V: Version, Decision: "allow"})
	if strings.Contains(string(b), "answers") {
		t.Fatalf("allow 应答不应有 answers 键: %s", b)
	}
}

// MapText 换掉题面、标题、选项的标签与说明四处文字，别的不动；返回深拷贝，没有选项的题得到 []；nil 进 nil 出。
func TestAskMapText(t *testing.T) {
	if (*Ask)(nil).MapText(strings.ToUpper) != nil {
		t.Fatal("nil 应得 nil")
	}
	in := &Ask{Questions: []Question{
		{Question: "q", Header: "h", Options: []Option{{Label: "l", Description: "d"}}, MultiSelect: true},
		{Question: "q2"},
	}}
	out := in.MapText(strings.ToUpper)
	b, _ := json.Marshal(out)
	if want := `{"questions":[{"question":"Q","header":"H","options":[{"label":"L","description":"D"}],"multiSelect":true},{"question":"Q2","options":[]}]}`; string(b) != want {
		t.Fatalf("MapText = %s，要 %s", b, want)
	}
	out.Questions[0].Options[0].Label = "改"
	if in.Questions[0].Options[0].Label != "l" || in.Questions[0].Question != "q" {
		t.Fatalf("应是深拷贝: %+v", in)
	}
}
