package proto

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/railse/railse-opc/internal/policy"
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
