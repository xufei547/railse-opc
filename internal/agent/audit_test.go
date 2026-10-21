package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

func TestAuditAppendsJSONL(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "audit.jsonl")
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	p := policy.Payload{Type: policy.PayloadShell, Command: "ls"}
	for i := 0; i < 2; i++ {
		if err := a.Write(AuditEntry{TS: time.Now(), RequestID: "r", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s"}, Payload: &p, Risk: "read", Action: "allow", State: "auto_allowed", RuleID: "builtin-readonly"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("审计文件权限 = %v", st.Mode().Perm())
	}
	f, _ := os.Open(path)
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var e AuditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("第 %d 行不是 JSON: %v", n+1, err)
		}
		if e.State != "auto_allowed" || e.Payload == nil || e.Payload.Command != "ls" {
			t.Fatalf("第 %d 行内容错误: %+v", n+1, e)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("行数 = %d", n)
	}
	// 再次打开应追加而不是覆盖
	a2, _ := OpenAudit(path)
	a2.Write(AuditEntry{TS: time.Now(), Kind: "done", State: "recorded"})
	a2.Close()
	b, _ := os.ReadFile(path)
	if lines := len(splitLines(b)); lines != 3 {
		t.Fatalf("追加后行数 = %d", lines)
	}
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	for _, l := range bytesSplit(b) {
		if len(l) > 0 {
			out = append(out, l)
		}
	}
	return out
}

func bytesSplit(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	return append(out, b[start:])
}
