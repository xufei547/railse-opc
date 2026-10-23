package wire

import (
	"strings"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
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
