package wire

import (
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func TestRoundTripAndKind(t *testing.T) {
	in := PendingRequest{T: TPending, RequestID: "r1",
		Session:  proto.Session{Tool: "claude", CLISessionID: "s", Cwd: "/p"},
		ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push"},
		Risk: "outward", ProjectRoot: "/p", EngineReason: "无规则命中", DeadlineSeconds: 86390, CreatedAt: time.Unix(1, 0).UTC()}
	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	kind, err := Kind(b)
	if err != nil || kind != TPending {
		t.Fatalf("Kind = %q, %v", kind, err)
	}
	var out PendingRequest
	if err := Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.RequestID != "r1" || out.Payload.Command != "git push" || out.Risk != "outward" || !out.CreatedAt.Equal(in.CreatedAt) {
		t.Fatalf("往返不一致: %+v", out)
	}
}

func TestMarshalRejectsHugeFrame(t *testing.T) {
	_, err := Marshal(Error{T: TError, Message: strings.Repeat("x", MaxFrame)})
	if err == nil {
		t.Fatal("超过 MaxFrame 应报错")
	}
}

func TestKindErrors(t *testing.T) {
	if _, err := Kind([]byte("nope")); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if _, err := Kind([]byte(`{"x":1}`)); err == nil {
		t.Fatal("缺 t 应报错")
	}
}

// hello 带协议版本，编码为 proto 字段并能往返。
func TestHelloProtoRoundTrip(t *testing.T) {
	if Proto != 1 {
		t.Fatalf("Proto = %d，应为 1", Proto)
	}
	b, err := Marshal(Hello{T: THello, Proto: Proto, MachineID: "m1", AgentVersion: "0.1.0", HomeDir: "/home/u"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"proto":1`) {
		t.Fatalf("hello 应带 proto 字段: %s", b)
	}
	var out Hello
	if err := Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Proto != Proto || out.MachineID != "m1" || out.AgentVersion != "0.1.0" || out.HomeDir != "/home/u" {
		t.Fatalf("往返不一致: %+v", out)
	}
}

// Error 可带 request_id（按请求拒收或登记失败），往返不变；不带时省略该字段。
func TestErrorRequestIDRoundTrip(t *testing.T) {
	b, err := Marshal(Error{T: TError, RequestID: "r1", Message: "登记失败"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"request_id":"r1"`) {
		t.Fatalf("应带 request_id: %s", b)
	}
	var out Error
	if err := Unmarshal(b, &out); err != nil || out != (Error{T: TError, RequestID: "r1", Message: "登记失败"}) {
		t.Fatalf("往返不一致: %+v, %v", out, err)
	}
	b, err = Marshal(Error{T: TError, Message: "x"})
	if err != nil || strings.Contains(string(b), "request_id") {
		t.Fatalf("没有 request_id 时应省略: %s, %v", b, err)
	}
}

// ask 帧带 kind 与题目，往返不变；审批帧不带 kind、ask 两个键（旧 hub、旧守护进程看到的审批帧与以前完全一样）。
func TestPendingAskAndPermissionFrames(t *testing.T) {
	ask := &proto.Ask{Questions: []proto.Question{{Question: "用 A 还是 B？", Header: "方案", Options: []proto.Option{{Label: "A", Description: "快"}, {Label: "B"}}, MultiSelect: true}}}
	b, err := Marshal(PendingRequest{T: TPending, RequestID: "a1", Kind: "ask", Ask: ask, ToolName: "AskUserQuestion", DeadlineSeconds: 60, CreatedAt: time.Unix(1, 0).UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"kind":"ask"`) || !strings.Contains(string(b), `"ask":{"questions":[`) {
		t.Fatalf("ask 帧应带 kind 与 ask: %s", b)
	}
	var out PendingRequest
	if err := Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Kind != "ask" || out.Ask == nil || len(out.Ask.Questions) != 1 || out.Ask.Questions[0].Options[0].Description != "快" || !out.Ask.Questions[0].MultiSelect {
		t.Fatalf("ask 帧往返不一致: %+v", out)
	}
	b, err = Marshal(PendingRequest{T: TPending, RequestID: "p1", ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push"}, Risk: "outward"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"kind"`) || strings.Contains(string(b), `"ask"`) {
		t.Fatalf("审批帧不应带 kind、ask 键: %s", b)
	}
}

// 决定可带答案（answer），往返不变；没有答案时省略 answers 键。
func TestDecisionAnswersRoundTrip(t *testing.T) {
	in := Decision{T: TDecision, RequestID: "a1", Action: "answer", Answers: map[string]string{"用 A 还是 B？": "A, B"}, DecidedBy: "user"}
	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Decision
	if err := Unmarshal(b, &out); err != nil || out.Action != "answer" || out.Answers["用 A 还是 B？"] != "A, B" {
		t.Fatalf("往返不一致: %+v, %v", out, err)
	}
	b, err = Marshal(Decision{T: TDecision, RequestID: "p1", Action: "allow", DecidedBy: "user"})
	if err != nil || strings.Contains(string(b), "answers") {
		t.Fatalf("没有答案时应省略 answers: %s, %v", b, err)
	}
}
