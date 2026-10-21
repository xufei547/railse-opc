package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hookclient"
)

func startTestAgent(t *testing.T) (sock string, repo string, cfg agent.Config) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg = agent.ConfigIn(filepath.Join(dir, "h"))
	cfg.HomeDir = dir
	cfg.EnsureDirs()
	rules, err := agent.LoadRules(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	audit, _ := agent.OpenAudit(cfg.AuditPath)
	srv := agent.New(cfg, rules, audit, agent.LocalDecider{}, log.New(os.Stderr, "", 0))
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() { cancel(); srv.Close(); audit.Close() })
	repo = filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	return cfg.SocketPath, repo, cfg
}

func claudePermission(cwd, cmd string) string {
	return `{"session_id":"s1","prompt_id":"p1","cwd":"` + cwd + `","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"` + cmd + `"}}`
}

func runHookWith(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runHook(args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestHookClaudeAllow(t *testing.T) {
	sock, repo, _ := startTestAgent(t)
	out, _, code := runHookWith(t, claudePermission(repo, "git status"), "claude", "--socket", sock)
	if code != 0 || out != `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestHookClaudePassthroughOnAsk(t *testing.T) {
	sock, repo, _ := startTestAgent(t)
	out, _, code := runHookWith(t, claudePermission(repo, "curl https://x.y"), "claude", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("ask 应不作决定: code=%d out=%q", code, out)
	}
}

func TestHookDenyWithRule(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	os.WriteFile(cfg.RulesPath, []byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"), 0o600)
	if reply, err := (hookclient.Client{SocketPath: sock}).Control("reload"); err != nil || !reply.OK {
		t.Fatalf("reload: %+v %v", reply, err)
	}
	got, _, code := runHookWith(t, claudePermission(repo, "rm x"), "claude", "--socket", sock)
	if code != 0 || !strings.Contains(got, `"behavior":"deny"`) || !strings.Contains(got, "deny-rm") {
		t.Fatalf("code=%d out=%q", code, got)
	}
}

func TestHookGeminiNeverAllows(t *testing.T) {
	sock, repo, _ := startTestAgent(t)
	stdin := `{"session_id":"g","cwd":"` + repo + `","hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"git status"}}`
	out, _, code := runHookWith(t, stdin, "gemini", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("Gemini 的 allow 应渲染为不作决定: code=%d out=%q", code, out)
	}
}

func TestHookNoDaemonPassthrough(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	out, errOut, code := runHookWith(t, claudePermission("/p", "git status"), "claude", "--socket", filepath.Join(dir, "none.sock"))
	if code != 0 || out != "" || !strings.Contains(errOut, "railse hook") {
		t.Fatalf("没有守护进程应不作决定并在 stderr 说明: code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestHookEventsExitImmediately(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	stop := `{"session_id":"s1","cwd":"` + repo + `","hook_event_name":"Stop","last_assistant_message":"DONE"}`
	out, _, code := runHookWith(t, stop, "claude", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("事件应无输出退出 0: %d %q", code, out)
	}
	notify := `{"type":"agent-turn-complete","thread-id":"t","turn-id":"u","cwd":"` + repo + `","last-assistant-message":"DONE"}`
	out, _, code = runHookWith(t, "", "codex-notify", "--socket", sock, notify)
	if code != 0 || out != "" {
		t.Fatalf("notify 应无输出退出 0: %d %q", code, out)
	}
	b, _ := os.ReadFile(cfg.AuditPath)
	if strings.Count(string(b), `"kind":"done"`) != 2 {
		t.Fatalf("两条 done 事件应进审计:\n%s", b)
	}
}

func TestHookBadInputPassthrough(t *testing.T) {
	sock, _, _ := startTestAgent(t)
	out, errOut, code := runHookWith(t, "not json", "claude", "--socket", sock)
	if code != 0 || out != "" || errOut == "" {
		t.Fatalf("坏输入应不作决定: %d %q %q", code, out, errOut)
	}
	out, _, code = runHookWith(t, "{}", "nope", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("未知 CLI 也应不作决定: %d %q", code, out)
	}
}
