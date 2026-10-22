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
