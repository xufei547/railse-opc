package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
	"github.com/xufei547/railse-opc/internal/i18n"
	"github.com/xufei547/railse-opc/internal/proto"
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

func TestLocalizeReason(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	// proto.Response 带 map 字段，不能当 map 的键，用切片列用例。
	cases := []struct {
		resp proto.Response
		want string
	}{
		{proto.Response{Reason: "命中规则 deny-rm", RuleID: "deny-rm"}, "Matched rule deny-rm"},
		{proto.Response{Reason: "已在手机上拒绝"}, "Denied on your phone"},
		// 手机上拒绝并记住：带规则号，但理由不是“命中规则 …”，照固定句子查
		{proto.Response{Reason: "已在手机上拒绝", RuleID: "user-1"}, "Denied on your phone"},
		{proto.Response{Reason: "自动审批已暂停，交回终端处理"}, "Auto-approval is paused; handed back to the terminal"},
		{proto.Response{Reason: "一句没见过的话"}, "一句没见过的话"},
	}
	for _, c := range cases {
		if got := localizeReason(c.resp); got != c.want {
			t.Errorf("localizeReason(%+v) = %q，想要 %q", c.resp, got, c.want)
		}
	}
	i18n.Set(i18n.ZH)
	if got := localizeReason(proto.Response{Reason: "命中规则 deny-rm", RuleID: "deny-rm"}); got != "命中规则 deny-rm" {
		t.Errorf("中文下应原样: %q", got)
	}
}

// 守护进程回传给钩子的固定理由（localizeReason 用 i18n.Lookup 查，完整性测试查不到）：英文下逐个核对都有译文、译文不带中文。
// 原文逐字抄自：internal/agent/hubdecider.go 的 reason* 常量；internal/agent/server.go 与 requests.go 写进 proto.Response.Reason 的
// 固定句子；internal/policy/engine.go 的暂停理由。那边的句子改了，这里与 internal/i18n/en.go 要一起改。
func TestLocalizeReasonFixedSentences(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	for _, s := range []string{
		// internal/agent/hubdecider.go
		"Gemini 只能拒绝不能放行，交回原生确认框",
		"没有配置 hub，交回终端处理",
		"连不上 hub，交回终端处理",
		"hub 拒绝了本机密钥，交回终端处理",
		"无法送往 hub，交回终端处理",
		"与 hub 断线超过宽限，交回终端处理",
		"hub 没有给出有效决定，交回终端处理",
		"hub 版本太旧，不认识提问，交回终端处理",
		"已在手机上批准",
		"已在手机上拒绝",
		"已在手机上回答",
		"在手机上选了在终端回答",
		// internal/agent/server.go
		"请求格式错误",
		"自动审批已暂停，交回终端处理",
		"脱敏后有题面相同的题，分不清答的是哪道，交回终端处理",
		"决定来源给的答案不完整，交回终端处理",
		"守护进程正在停止，交回终端处理",
		"等待过期，交回终端处理",
		"决定来源出错，交回终端处理",
		// internal/agent/requests.go
		"本机没有连接 hub，交回终端处理",
		// internal/policy/engine.go
		"自动审批已暂停",
	} {
		if got := localizeReason(proto.Response{Reason: s}); got == s || hasCJK(got) {
			t.Errorf("固定理由 %q 没有英文: %q", s, got)
		}
	}
}

func TestPassthroughMessageEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	var b bytes.Buffer
	passthrough(&b, i18n.T("请求没有对应的适配器"))
	if got := b.String(); got != "railse hook: no adapter for this request; handed back to the terminal\n" {
		t.Fatalf("got %q", got)
	}
}

// 英文下命中规则的拒绝：守护进程回传中文理由与规则号，钩子按规则号重拼，JSON 里给模型看的理由是英文。
func TestHookDenyWithRuleEnglish(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	os.WriteFile(cfg.RulesPath, []byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"), 0o600)
	if reply, err := (hookclient.Client{SocketPath: sock}).Control("reload"); err != nil || !reply.OK {
		t.Fatalf("reload: %+v %v", reply, err)
	}
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	got, _, code := runHookWith(t, claudePermission(repo, "rm x"), "claude", "--socket", sock)
	if want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Railse: Matched rule deny-rm"}}}` + "\n"; code != 0 || got != want {
		t.Fatalf("code=%d out=%q", code, got)
	}
}

// fixedDecider 立即给出固定的结论。
type fixedDecider agent.Outcome

func (d fixedDecider) Decide(ctx context.Context, pr agent.PendingRequest) (agent.Outcome, error) {
	return agent.Outcome(d), nil
}

// 英文下手机上拒绝（含拒绝并记住，带规则号）：Claude 与 Gemini 的拒绝 JSON 里都是英文理由。
func TestHookDenyOnPhoneEnglish(t *testing.T) {
	for _, ruleID := range []string{"", "user-1"} {
		sock, repo, _ := startAgentWith(t, fixedDecider{Action: "deny", Reason: "已在手机上拒绝", RuleID: ruleID, DecidedBy: "user"})
		i18n.Set(i18n.EN)
		t.Cleanup(func() { i18n.Set(i18n.ZH) })
		got, _, code := runHookWith(t, claudePermission(repo, "curl https://x.y"), "claude", "--socket", sock)
		if want := `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Railse: Denied on your phone"}}}` + "\n"; code != 0 || got != want {
			t.Fatalf("Claude（规则号 %q）: code=%d out=%q", ruleID, code, got)
		}
		stdin := `{"session_id":"g","cwd":"` + repo + `","hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"curl https://x.y"}}`
		got, _, code = runHookWith(t, stdin, "gemini", "--socket", sock)
		if want := `{"decision":"deny","reason":"Railse: Denied on your phone"}` + "\n"; code != 0 || got != want {
			t.Fatalf("Gemini（规则号 %q）: code=%d out=%q", ruleID, code, got)
		}
	}
}

// 英文下钩子留在 stderr 的句子：用法、参数错误、不认识的 CLI、缺参数、坏输入、题目不合规、连不上守护进程，都不带中文。
func TestHookStderrEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	dir, err := os.MkdirTemp("", "rs") // 短路径：unix socket 路径有长度上限
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	none := filepath.Join(dir, "none.sock")
	badAsk := `{"session_id":"s1","cwd":"/p","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_input":{"questions":[]}}`
	cases := []struct {
		name  string
		stdin string
		args  []string
		want  string
	}{
		{"用法", "", nil, "Usage:\n  railse hook claude|codex|gemini [--socket path]"},
		{"参数错误", "", []string{"claude", "--bogus"}, "railse hook: bad arguments: flag provided but not defined: -bogus; handed back to the terminal\n"},
		{"不认识的 CLI", "{}", []string{"nope", "--socket", none}, "railse hook: unknown CLI nope; handed back to the terminal\n"},
		{"codex-notify 缺参数", "", []string{"codex-notify", "--socket", none}, "railse hook: codex-notify needs a JSON argument; handed back to the terminal\n"},
		{"坏输入", "not json", []string{"claude", "--socket", none}, "railse hook: can't parse the Claude hook input: "},
		{"缺字段", `{"hook_event_name":"BeforeTool"}`, []string{"gemini", "--socket", none}, "railse hook: BeforeTool is missing session_id or tool_name; handed back to the terminal\n"},
		{"题目不合规", badAsk, []string{"claude", "--socket", none}, "railse hook: invalid AskUserQuestion: no questions; handed back to the terminal\n"},
		{"连不上守护进程", claudePermission("/p", "git status"), []string{"claude", "--socket", none}, "railse hook: can't connect to the daemon: dial unix " + none},
	}
	for _, c := range cases {
		out, errOut, code := runHookWith(t, c.stdin, c.args...)
		if code != 0 || out != "" || !strings.Contains(errOut, c.want) || hasCJK(errOut) {
			t.Errorf("%s: code=%d out=%q err=%q，想要含 %q 且不带中文", c.name, code, out, errOut, c.want)
		}
	}
}

// 哨兵错误在 Error() 时才翻译：英文下不带中文，errors.Is 照常认得出。
func TestErrParentGoneEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	if got := errParentGone.Error(); got != "the parent process (the CLI) has exited" {
		t.Fatalf("got %q", got)
	}
	if !errors.Is(fmt.Errorf("x: %w", errParentGone), errParentGone) {
		t.Fatal("errors.Is 应认得出包了一层的 errParentGone")
	}
	i18n.Set(i18n.ZH)
	if got := errParentGone.Error(); got != "父进程（CLI）已退出" {
		t.Fatalf("中文下应原样: %q", got)
	}
}

// claudePreToolUse 按 pretooluse-bash-*.json 的形状造 PreToolUse 输入。
func claudePreToolUse(cwd, cmd, mode string) string {
	return `{"session_id":"s1","prompt_id":"p1","cwd":"` + cwd + `","permission_mode":"` + mode + `","hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"` + cmd + `","description":"d"},"tool_use_id":"toolu_1"}`
}

// PreToolUse 的 Bash 经钩子得到 PreToolUse 格式的决定：只读放行、auto 下的联网不表态、硬底线照规则拒。
func TestHookClaudePreToolUse(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	out, _, code := runHookWith(t, claudePreToolUse(repo, "git status", "auto"), "claude", "--socket", sock)
	if code != 0 || !strings.HasPrefix(out, `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"Railse：`) {
		t.Fatalf("只读: code=%d out=%s", code, out)
	}
	out, _, code = runHookWith(t, claudePreToolUse(repo, "curl https://x.y", "auto"), "claude", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("auto 下的联网应不表态: code=%d out=%q", code, out)
	}
	// 没接 hub（LocalDecider）：硬底线要 Claude 在终端里强制问（ask），不说“连不上 hub”的回退提示
	out, errOut, code := runHookWith(t, claudePreToolUse(repo, "git push origin main", "auto"), "claude", "--socket", sock)
	if code != 0 || out != `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"Railse：连不上 hub，这类操作要你亲自确认"}}`+"\n" ||
		strings.Contains(errOut, "hub") {
		t.Fatalf("没接 hub: code=%d out=%q err=%q", code, out, errOut)
	}
	var sources []string
	b, _ := os.ReadFile(cfg.AuditPath)
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var e agent.AuditEntry
		json.Unmarshal([]byte(l), &e)
		sources = append(sources, e.Source+"/"+e.PermissionMode+"/"+e.ToolUseID)
	}
	if len(sources) != 3 || sources[0] != "pretooluse/auto/toolu_1" {
		t.Fatalf("审计应带来源、模式与编号: %q", sources)
	}
}

// 钩子把来源、模式、编号发给守护进程；守护进程拒绝时渲染成 PreToolUse 的 deny。
func TestHookClaudePreToolUseRequest(t *testing.T) {
	sock, lines := fakeDaemon(t, `{"v":1,"decision":"deny","reason":"已在手机上拒绝","decided_by":"user"}`)
	out, _, _ := runHookWith(t, claudePreToolUse("/p", "git push origin main", "default"), "claude", "--socket", sock)
	if out != `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Railse：已在手机上拒绝"}}`+"\n" {
		t.Fatalf("out=%s", out)
	}
	var req proto.Request
	json.Unmarshal([]byte(lines()[0]), &req)
	if req.Kind != "permission" || req.Source != "pretooluse" || req.PermissionMode != "default" || req.ToolUseID != "toolu_1" || req.Payload.Command != "git push origin main" {
		t.Fatalf("请求: %+v", req)
	}
}

// hub 不可达时钩子不表态，并在 stderr 提示回退；PermissionRequest 同样提示，Codex（没有来源）不提。
func TestHookHubUnreachableNote(t *testing.T) {
	reply := `{"v":1,"decision":"passthrough","decided_by":"hub_unreachable","reason":"连不上 hub，交回终端处理"}`
	sock, _ := fakeDaemon(t, reply)
	stdout, stderr, _ := runHookWith(t, claudePreToolUse("/tmp/p", "curl https://x.y", "auto"), "claude", "--socket", sock)
	if stdout != "" || stderr != "railse hook: 连不上 hub，交回 Claude 自己的确认\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
	// 硬底线：守护进程回 ask，钩子输出 ask，stderr 提示照旧
	sock, _ = fakeDaemon(t, `{"v":1,"decision":"ask","decided_by":"hub_unreachable","reason":"连不上 hub，这类操作要你亲自确认"}`)
	stdout, stderr, _ = runHookWith(t, claudePreToolUse("/tmp/p", "git push origin main", "auto"), "claude", "--socket", sock)
	if !strings.Contains(stdout, `"permissionDecision":"ask"`) || !strings.Contains(stderr, "连不上 hub") {
		t.Fatalf("硬底线 ask: stdout=%q stderr=%q", stdout, stderr)
	}
	sock, _ = fakeDaemon(t, reply)
	_, stderr, _ = runHookWith(t, claudePermission("/tmp/p", "git push origin main"), "claude", "--socket", sock)
	if !strings.Contains(stderr, "hub") {
		t.Fatalf("PermissionRequest 也该提示: %q", stderr)
	}
	sock, _ = fakeDaemon(t, reply)
	codex := `{"session_id":"s","turn_id":"t","cwd":"/p","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"git push"}}`
	_, stderr, _ = runHookWith(t, codex, "codex", "--socket", sock)
	if stderr != "" {
		t.Fatalf("Codex 不提 Claude: %q", stderr)
	}
	// 别的交回终端（过期等）不提示
	sock, _ = fakeDaemon(t, `{"v":1,"decision":"passthrough","decided_by":"timeout"}`)
	_, stderr, _ = runHookWith(t, claudePreToolUse("/tmp/p", "git push origin main", "auto"), "claude", "--socket", sock)
	if stderr != "" {
		t.Fatalf("过期不提示: %q", stderr)
	}
}
