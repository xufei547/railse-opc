package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/hookclient"
)

// 用真实录制的样本走完整链路，只把 cwd 改到临时仓库。
func sampleWithCwd(t *testing.T, rel, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "adapter", "testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	doc["cwd"] = cwd
	out, _ := json.Marshal(doc)
	return string(out)
}

func TestEndToEndClaudeSamples(t *testing.T) {
	// Claude 往钩子环境里注入令牌；钩子在本进程里跑，令牌绝不能进审计
	const sentinel = "railse-sentinel-9d3f61c2"
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", sentinel)
	sock, repo, cfg := startTestAgent(t)
	// git push origin main 是 outward，没有用户规则时交回终端
	out, _, code := runHookWith(t, sampleWithCwd(t, "claude/2.1.280/PermissionRequest.json", repo), "claude", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("git push 应不作决定: %d %q", code, out)
	}
	// touch spike-proof.txt 在仓库内是 write_in_project，内置规则放行
	out, _, code = runHookWith(t, sampleWithCwd(t, "claude/2.1.280/PermissionRequest.interactive.json", repo), "claude", "--socket", sock)
	if code != 0 || !strings.Contains(out, `"behavior":"allow"`) {
		t.Fatalf("touch 应放行: %d %q", code, out)
	}
	for _, f := range []string{"claude/2.1.280/Notification.permission_prompt.json", "claude/2.1.280/Stop.json", "claude/2.1.280/SessionEnd.json", "claude/2.1.280/PreToolUse.json"} {
		out, _, code = runHookWith(t, sampleWithCwd(t, f, repo), "claude", "--socket", sock)
		if code != 0 || out != "" {
			t.Fatalf("%s: %d %q", f, code, out)
		}
	}
	b, _ := os.ReadFile(cfg.AuditPath)
	audit := string(b)
	for _, want := range []string{`"state":"passthrough"`, `"state":"auto_allowed"`, `"kind":"question"`, `"kind":"done"`, `"kind":"session_end"`} {
		if !strings.Contains(audit, want) {
			t.Fatalf("审计缺 %s:\n%s", want, audit)
		}
	}
	if strings.Contains(audit, "CLAUDE_CODE_MESSAGING") || strings.Contains(audit, sentinel) {
		t.Fatal("审计里不能出现钩子进程的环境变量")
	}
}

// auditHas 判断审计里是否有一行同时含全部片段。
func auditHas(t *testing.T, path string, parts ...string) bool {
	t.Helper()
	b, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(b), "\n") {
		ok := line != ""
		for _, p := range parts {
			ok = ok && strings.Contains(line, p)
		}
		if ok {
			return true
		}
	}
	return false
}

func TestEndToEndCodexAndGeminiSamples(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	out, _, code := runHookWith(t, sampleWithCwd(t, "codex/0.145.0/PermissionRequest.json", repo), "codex", "--socket", sock)
	if code != 0 || !strings.Contains(out, `"behavior":"allow"`) {
		t.Fatalf("Codex touch 应放行: %d %q", code, out)
	}
	out, _, code = runHookWith(t, sampleWithCwd(t, "gemini/0.65.0/BeforeTool.json", repo), "gemini", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("Gemini 的放行必须渲染为不作决定: %d %q", code, out)
	}
	// 守护进程那边是放行的，只是 Gemini 侧渲染成不作决定
	if !auditHas(t, cfg.AuditPath, `"tool":"gemini"`, `"state":"auto_allowed"`) {
		b, _ := os.ReadFile(cfg.AuditPath)
		t.Fatalf("审计应有 Gemini 的 auto_allowed 记录:\n%s", b)
	}
}

func TestEndToEndGeminiDeny(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	os.WriteFile(cfg.RulesPath, []byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"), 0o600)
	if reply, err := (hookclient.Client{SocketPath: sock}).Control("reload"); err != nil || !reply.OK {
		t.Fatalf("reload: %+v %v", reply, err)
	}
	var doc map[string]any
	json.Unmarshal([]byte(sampleWithCwd(t, "gemini/0.65.0/BeforeTool.json", repo)), &doc)
	doc["tool_input"] = map[string]any{"command": "rm x"}
	stdin, _ := json.Marshal(doc)
	out, _, code := runHookWith(t, string(stdin), "gemini", "--socket", sock)
	if code != 0 || !strings.HasPrefix(out, `{"decision":"deny",`) || !strings.Contains(out, "deny-rm") {
		t.Fatalf("Gemini 命中用户 deny 应输出拒绝 JSON: %d %q", code, out)
	}
	if !auditHas(t, cfg.AuditPath, `"tool":"gemini"`, `"state":"auto_denied"`, `"rule_id":"deny-rm"`) {
		b, _ := os.ReadFile(cfg.AuditPath)
		t.Fatalf("审计应有 Gemini 的 auto_denied 记录:\n%s", b)
	}
}
