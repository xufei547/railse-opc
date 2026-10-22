package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hookclient"
)

func startTestAgent(t *testing.T) (sock string, repo string, cfg agent.Config) {
	t.Helper()
	return startAgentWith(t, agent.LocalDecider{})
}

// startAgentWith 起一个用指定 Decider 的守护进程。
func startAgentWith(t *testing.T, decider agent.Decider) (sock string, repo string, cfg agent.Config) {
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
	srv := agent.New(cfg, rules, audit, decider, log.New(os.Stderr, "", 0))
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

// blockingDecider 模拟没人处理的审批：通知 started 后一直等到 ctx 结束。
type blockingDecider struct{ started chan struct{} }

func (d blockingDecider) Decide(ctx context.Context, pr agent.PendingRequest) (agent.Outcome, error) {
	select {
	case d.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return agent.Outcome{}, ctx.Err()
}

// TestHelperHook 不是独立的测试：TestHookSignalPassthrough 把测试二进制当子进程跑它，好对一个真实进程发信号。
func TestHelperHook(t *testing.T) {
	if os.Getenv("RAILSE_TEST_HELPER") != "1" {
		return
	}
	os.Exit(run([]string{"hook", "claude", "--socket", os.Getenv("RAILSE_TEST_SOCK")}, os.Stdout, os.Stderr))
}

func TestHookSignalPassthrough(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			started := make(chan struct{}, 1)
			sock, repo, cfg := startAgentWith(t, blockingDecider{started: started})
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHook$")
			cmd.Env = append(os.Environ(), "RAILSE_TEST_HELPER=1", "RAILSE_TEST_SOCK="+sock)
			cmd.Stdin = strings.NewReader(claudePermission(repo, "curl https://x.y"))
			var out, errOut bytes.Buffer
			cmd.Stdout, cmd.Stderr = &out, &errOut
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			guard := time.AfterFunc(10*time.Second, func() { cmd.Process.Kill() })
			defer guard.Stop()
			select {
			case <-started: // 钩子已在等答复
			case <-time.After(10 * time.Second):
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatalf("守护进程没收到请求: stderr=%q", errOut.String())
			}
			cmd.Process.Signal(sig)
			err := cmd.Wait()
			if err != nil || out.Len() != 0 || !strings.Contains(errOut.String(), "railse hook") || !strings.Contains(errOut.String(), "收到信号") {
				t.Fatalf("收到 %v 应不作决定并退出 0: err=%v stdout=%q stderr=%q", sig, err, out.String(), errOut.String())
			}
			// 钩子关掉连接后，守护进程把请求记为 cancelled。
			for end := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				b, _ := os.ReadFile(cfg.AuditPath)
				if strings.Contains(string(b), `"state":"cancelled"`) {
					break
				}
				if time.Now().After(end) {
					t.Fatalf("审计里应有 cancelled:\n%s", b)
				}
			}
		})
	}
}

// blockingReader 模拟永不关闭的 stdin：第一次 Read 时通知 reading，然后阻塞到 release 关闭。
type blockingReader struct{ reading, release chan struct{} }

func (r blockingReader) Read(p []byte) (int, error) {
	select {
	case r.reading <- struct{}{}:
	default:
	}
	<-r.release
	return 0, io.EOF
}

func TestHookSignalWhileReadingStdin(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	r := blockingReader{reading: make(chan struct{}, 1), release: make(chan struct{})}
	defer close(r.release)
	// runHook 入口已接管信号，阻塞在读 stdin 时给本进程发 SIGTERM 不会杀掉测试进程。
	go func() {
		<-r.reading
		syscall.Kill(os.Getpid(), syscall.SIGTERM)
	}()
	var out, errOut bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runHook([]string{"claude", "--socket", filepath.Join(dir, "none.sock")}, r, &out, &errOut)
	}()
	select {
	case code := <-done:
		if code != 0 || out.Len() != 0 || !strings.Contains(errOut.String(), "收到信号") {
			t.Fatalf("读 stdin 时收到信号应不作决定: code=%d out=%q err=%q", code, out.String(), errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("读 stdin 时收到信号应立即返回，不等 stdin 关闭")
	}
}

func TestHookPassthroughWhenParentDies(t *testing.T) {
	// CLI 被 SIGKILL 时不会给钩子发信号；钩子被过继后父进程号会变，应放弃等待、不作决定
	started := make(chan struct{}, 1)
	sock, repo, cfg := startAgentWith(t, blockingDecider{started: started})
	var ppid atomic.Int32
	ppid.Store(4242)
	oldGet, oldInterval := getppid, parentCheckInterval
	getppid = func() int { return int(ppid.Load()) }
	parentCheckInterval = 20 * time.Millisecond
	defer func() { getppid, parentCheckInterval = oldGet, oldInterval }()
	go func() {
		<-started // 钩子已在等答复
		ppid.Store(1)
	}()
	start := time.Now()
	out, errOut, code := runHookWith(t, claudePermission(repo, "curl https://x.y"), "claude", "--socket", sock)
	if code != 0 || out != "" || !strings.Contains(errOut, "父进程") || time.Since(start) > 5*time.Second {
		t.Fatalf("父进程退出后应不作决定并退出 0: code=%d out=%q err=%q 用时=%v", code, out, errOut, time.Since(start))
	}
	for end := time.Now().Add(2 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		b, _ := os.ReadFile(cfg.AuditPath)
		if strings.Contains(string(b), `"state":"cancelled"`) {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("钩子放弃后守护进程应把请求记为 cancelled:\n%s", b)
		}
	}
}
