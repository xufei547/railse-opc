package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

// blockingDecider 等到 release 被关闭才返回，用来模拟 hub 等待。
type blockingDecider struct {
	release chan struct{}
	mu      sync.Mutex
	ctxErr  error
	outcome Outcome
}

func (d *blockingDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error) {
	select {
	case <-d.release:
		return d.outcome, nil
	case <-ctx.Done():
		d.mu.Lock()
		d.ctxErr = ctx.Err()
		d.mu.Unlock()
		return Outcome{}, ctx.Err()
	}
}

type testEnv struct {
	cfg    Config
	srv    *Server
	repo   string // 一个带 .git 的目录
	cancel context.CancelFunc
}

func startServer(t *testing.T, decider Decider, mutate func(*Config)) *testEnv {
	t.Helper()
	return startServerLog(t, decider, mutate, os.Stderr)
}

// startServerLog 同 startServer，守护进程的日志写到 w。
func startServerLog(t *testing.T, decider Decider, mutate func(*Config), w io.Writer) *testEnv {
	t.Helper()
	dir := shortTempDir(t)
	cfg := ConfigIn(filepath.Join(dir, "h"))
	cfg.HomeDir = filepath.Join(dir, "home")
	os.MkdirAll(cfg.HomeDir, 0o755)
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	rules, err := LoadRules(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := OpenAudit(cfg.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(cfg, rules, audit, decider, log.New(w, "test ", 0))
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	t.Cleanup(func() { cancel(); srv.Close(); audit.Close() })
	return &testEnv{cfg: cfg, srv: srv, repo: repo, cancel: cancel}
}

// roundTrip 发一行，读一行。
func roundTrip(t *testing.T, sock string, msg any) []byte {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := proto.WriteLine(conn, msg); err != nil {
		t.Fatal(err)
	}
	line, err := proto.ReadLine(bufio.NewReader(conn))
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func permission(cwd, cmd string) proto.Request {
	return proto.Request{V: proto.Version, Kind: "permission",
		Session:  proto.Session{Tool: "claude", CLISessionID: "s1", TurnID: "t1", Cwd: cwd},
		ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: cmd, Cwd: cwd},
		DeadlineSeconds: 86390}
}

func readAudit(t *testing.T, path string) []AuditEntry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []AuditEntry
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("审计行不是 JSON: %q", l)
		}
		out = append(out, e)
	}
	return out
}

func TestPermissionAllowDenyPassthrough(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	os.WriteFile(env.cfg.RulesPath, []byte("version: 1\nrules:\n  - id: builtin-readonly\n    source: builtin\n    scope: global\n    match: {risk: read}\n    action: allow\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"), 0o600)
	if err := env.srv.rules.Reload(); err != nil {
		t.Fatal(err)
	}
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
	if resp.Decision != "allow" || resp.RuleID != "builtin-readonly" || resp.Risk != "read" || resp.RequestID == "" {
		t.Fatalf("allow: %+v", resp)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "rm x")), &resp)
	if resp.Decision != "deny" || resp.RuleID != "deny-rm" {
		t.Fatalf("deny: %+v", resp)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl https://x.y")), &resp)
	if resp.Decision != "passthrough" || resp.Risk != "network" {
		t.Fatalf("passthrough: %+v", resp)
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 3 || entries[0].State != "auto_allowed" || entries[1].State != "auto_denied" || entries[2].State != "passthrough" || entries[2].DecidedBy != "no_hub" {
		t.Fatalf("审计: %+v", entries)
	}
	if entries[0].Payload == nil || entries[0].Payload.Command != "git status" {
		t.Fatalf("审计应带载荷: %+v", entries[0])
	}
}

func TestPermissionRedactsAudit(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl -H 'Authorization: Bearer secrettoken' https://x.y"))
	entries := readAudit(t, env.cfg.AuditPath)
	if strings.Contains(entries[0].Payload.Command, "secrettoken") {
		t.Fatalf("审计未脱敏: %s", entries[0].Payload.Command)
	}
}

func TestProjectRootFailsClosed(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	plain := filepath.Join(filepath.Dir(env.repo), "plain")
	os.MkdirAll(plain, 0o755)
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(plain, "touch a")), &resp)
	if resp.Decision != "passthrough" || resp.Risk != "write_outside" {
		t.Fatalf("没有 .git 的目录里写文件应按项目外: %+v", resp)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "touch a")), &resp)
	if resp.Decision != "allow" || resp.Risk != "write_in_project" {
		t.Fatalf("仓库内写文件应放行: %+v", resp)
	}
}

func TestConnDropCancelsPending(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{})}
	env := startServer(t, d, nil)
	conn, err := net.Dial("unix", env.cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	proto.WriteLine(conn, permission(env.repo, "curl https://x.y"))
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	conn.Close()
	waitFor(t, func() bool { return env.srv.pending.Count() == 0 })
	d.mu.Lock()
	err = d.ctxErr
	d.mu.Unlock()
	if err == nil {
		t.Fatal("钩子断开后 Decider 的 ctx 应被取消")
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 1 || entries[0].State != "cancelled" {
		t.Fatalf("审计应记 cancelled: %+v", entries)
	}
}

func TestDeadlineExpires(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{})}
	env := startServer(t, d, func(c *Config) { c.Deadline = 200 * time.Millisecond })
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl https://x.y")), &resp)
	if resp.Decision != "passthrough" || !strings.Contains(resp.Reason, "过期") {
		t.Fatalf("到期应不作决定: %+v", resp)
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 1 || entries[0].State != "expired" || entries[0].DecidedBy != "timeout" {
		t.Fatalf("审计应记 expired: %+v", entries)
	}
}

func TestDeciderOutcomeIsHonored(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{}), outcome: Outcome{Action: "allow", Reason: "手机批准", DecidedBy: "user"}}
	env := startServer(t, d, nil)
	done := make(chan proto.Response)
	go func() {
		var resp proto.Response
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl https://x.y")), &resp)
		done <- resp
	}()
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	close(d.release)
	resp := <-done
	if resp.Decision != "allow" || resp.Reason != "手机批准" {
		t.Fatalf("应采用 Decider 的结论: %+v", resp)
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if entries[0].State != "approved" || entries[0].DecidedBy != "user" {
		t.Fatalf("审计: %+v", entries[0])
	}
}

func TestSocketPermissions(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	st, err := os.Stat(env.cfg.SocketPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket 权限 = %v, %v", st.Mode().Perm(), err)
	}
}

func TestStaleSocketIsReplaced(t *testing.T) {
	dir := shortTempDir(t)
	cfg := ConfigIn(filepath.Join(dir, "h"))
	cfg.EnsureDirs()
	// 上次崩溃留下的真 socket 文件：监听已关但文件还在，没人在听
	old, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	old.(*net.UnixListener).SetUnlinkOnClose(false)
	old.Close()
	if st, err := os.Lstat(cfg.SocketPath); err != nil || st.Mode()&os.ModeSocket == 0 {
		t.Fatalf("前置条件：应留下一个 socket 文件: %v %v", st, err)
	}
	rules, _ := LoadRules(cfg.RulesPath)
	audit, _ := OpenAudit(cfg.AuditPath)
	defer audit.Close()
	srv := New(cfg, rules, audit, LocalDecider{}, log.New(os.Stderr, "", 0))
	if err := srv.Listen(); err != nil {
		t.Fatalf("应替换过期 socket 文件: %v", err)
	}
	srv.Close()
}

func TestListenRefusesNonSocketFile(t *testing.T) {
	dir := shortTempDir(t)
	cfg := ConfigIn(filepath.Join(dir, "h"))
	cfg.EnsureDirs()
	os.WriteFile(cfg.SocketPath, []byte("not a socket"), 0o600)
	rules, _ := LoadRules(cfg.RulesPath)
	audit, _ := OpenAudit(cfg.AuditPath)
	defer audit.Close()
	srv := New(cfg, rules, audit, LocalDecider{}, log.New(os.Stderr, "", 0))
	if err := srv.Listen(); err == nil {
		srv.Close()
		t.Fatal("socket 路径上是普通文件时应报错，不能当陈旧 socket 删掉")
	}
	if b, err := os.ReadFile(cfg.SocketPath); err != nil || string(b) != "not a socket" {
		t.Fatalf("普通文件应原样保留: %q %v", b, err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

// stubbornDecider 不理会 ctx，等 release 才返回，用来把旧实例的 Close 卡在等挂起请求的阶段。
type stubbornDecider struct{ release chan struct{} }

func (d stubbornDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error) {
	<-d.release
	return Outcome{}, ctx.Err()
}

func TestOldCloseKeepsNewInstanceSocket(t *testing.T) {
	d := stubbornDecider{release: make(chan struct{})}
	env := startServer(t, d, nil)
	conn, err := net.Dial("unix", env.cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	proto.WriteLine(conn, permission(env.repo, "curl https://x.y"))
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	closed := make(chan struct{})
	go func() { env.srv.Close(); close(closed) }()
	// 旧实例关掉监听后路径就空出来了，但 Close 还在等挂起的请求。
	waitFor(t, func() bool { _, err := os.Stat(env.cfg.SocketPath); return os.IsNotExist(err) })
	rules, err := LoadRules(env.cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := OpenAudit(filepath.Join(env.cfg.Dir, "b.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	srvB := New(env.cfg, rules, audit, LocalDecider{}, log.New(os.Stderr, "b ", 0))
	if err := srvB.Listen(); err != nil {
		t.Fatalf("新实例应能接管空出来的路径: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srvB.Serve(ctx)
	t.Cleanup(func() { cancel(); srvB.Close(); audit.Close() })
	close(d.release)
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("旧实例的 Close 没返回")
	}
	if _, err := os.Stat(env.cfg.SocketPath); err != nil {
		t.Fatalf("旧实例的 Close 删掉了新实例的 socket: %v", err)
	}
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
	if resp.Decision != "allow" {
		t.Fatalf("新实例应能处理请求: %+v", resp)
	}
}

func TestShutdownRepliesPassthrough(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{})}
	env := startServer(t, d, nil)
	conn, err := net.Dial("unix", env.cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	proto.WriteLine(conn, permission(env.repo, "curl https://x.y"))
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	env.cancel()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := proto.ReadLine(bufio.NewReader(conn))
	if err != nil {
		t.Fatalf("守护进程关停时应写回答复: %v", err)
	}
	var resp proto.Response
	json.Unmarshal(line, &resp)
	if resp.Decision != "passthrough" || !strings.Contains(resp.Reason, "停止") {
		t.Fatalf("关停时应交回终端: %+v", resp)
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 1 || entries[0].State != "passthrough" || entries[0].DecidedBy != "shutdown" {
		t.Fatalf("审计应记 passthrough/shutdown: %+v", entries)
	}
}

func TestShutdownDoesNotWaitForIdleConn(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	idle, err := net.Dial("unix", env.cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	// 连接按顺序接受：这条有了答复，说明空闲连接已经在处理中。
	roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status"))
	start := time.Now()
	env.cancel()
	env.srv.Close()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("关停不应等空闲连接的读超时: 用时 %v", d)
	}
}

type panicDecider struct{}

func (panicDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error) {
	panic("决定来源崩溃")
}

// 预期中的 panic 堆栈写进测试缓冲（收尾修复 c），只在测试失败时打印，不打到 stderr 盖住别的失败。
func TestDeciderPanicDoesNotStopServer(t *testing.T) {
	logs := &logBuf{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("守护进程日志:\n%s", logs.String())
		}
	})
	env := startServerLog(t, panicDecider{}, nil, logs)
	conn, err := net.Dial("unix", env.cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	proto.WriteLine(conn, permission(env.repo, "curl https://x.y"))
	if line, err := proto.ReadLine(bufio.NewReader(conn)); err == nil {
		t.Fatalf("panic 后应直接关连接: %s", line)
	}
	waitFor(t, func() bool { return env.srv.pending.Count() == 0 })
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
	if resp.Decision != "allow" {
		t.Fatalf("panic 之后服务应正常: %+v", resp)
	}
	// 日志在 recover 里写，晚于连接关闭与请求出表：轮询等它
	waitFor(t, func() bool { return strings.Contains(logs.String(), "处理连接时 panic: 决定来源崩溃") })
	if !strings.Contains(logs.String(), "goroutine") {
		t.Fatalf("panic 的堆栈应记进守护进程日志:\n%s", logs.String())
	}
}

// fixedDecider 立即返回固定的结论或错误。
type fixedDecider struct {
	out Outcome
	err error
}

func (d fixedDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error) {
	return d.out, d.err
}

func TestDeciderBadOutcomeIsPassthrough(t *testing.T) {
	for _, action := range []string{"", "maybe"} {
		env := startServer(t, fixedDecider{out: Outcome{Action: action, DecidedBy: "user"}}, nil)
		var resp proto.Response
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl https://x.y")), &resp)
		if resp.Decision != "passthrough" {
			t.Fatalf("Action=%q 应写回 passthrough: %+v", action, resp)
		}
		entries := readAudit(t, env.cfg.AuditPath)
		if len(entries) != 1 || entries[0].Action != "passthrough" || entries[0].State != "passthrough" {
			t.Fatalf("Action=%q 审计: %+v", action, entries)
		}
	}
}

func TestDeciderErrorIsPassthrough(t *testing.T) {
	env := startServer(t, fixedDecider{err: errors.New("hub 断线")}, nil)
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl https://x.y")), &resp)
	if resp.Decision != "passthrough" || !strings.Contains(resp.Reason, "出错") {
		t.Fatalf("Decider 出错应交回终端: %+v", resp)
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 1 || entries[0].State != "passthrough" || entries[0].DecidedBy != "error" {
		t.Fatalf("审计应记 passthrough/error: %+v", entries)
	}
}

// symlinkedProject 在临时目录里建 real/proj（带 .git）和指向 real 的 link，返回 link 写法的项目路径与真实路径。
// shortTempDir 在 macOS 上位于 /var/folders，本身也经过 /var -> /private/var 的链接。
func symlinkedProject(t *testing.T, base string) (linkProj, realProj string) {
	t.Helper()
	real := filepath.Join(base, "real")
	if err := os.MkdirAll(filepath.Join(real, "proj", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(base, "link")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	realProj, err := filepath.EvalSymlinks(filepath.Join(real, "proj"))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "link", "proj"), realProj
}

const builtinWriteInProject = "  - id: builtin-write-in-project\n    source: builtin\n    scope: global\n    match: {risk: write_in_project}\n    action: allow\n"

func TestProjectScopeRuleMatchesThroughSymlink(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	linkProj, realProj := symlinkedProject(t, filepath.Dir(env.repo))
	rules := "version: 1\nrules:\n" + builtinWriteInProject +
		"  - id: deny-rm-proj\n    source: user\n    scope: {project: \"" + linkProj + "\"}\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"
	os.WriteFile(env.cfg.RulesPath, []byte(rules), 0o600)
	if err := env.srv.rules.Reload(); err != nil {
		t.Fatal(err)
	}
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(realProj, "rm notes.txt")), &resp)
	if resp.Decision != "deny" || resp.RuleID != "deny-rm-proj" {
		t.Fatalf("scope.project 写成符号链接时也应命中真实路径的项目: %+v", resp)
	}
}

func TestDenyPathGlobMatchesThroughSymlink(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	linkProj, realProj := symlinkedProject(t, filepath.Dir(env.repo))
	rules := "version: 1\nrules:\n" + builtinWriteInProject +
		"  - id: deny-secret\n    source: user\n    scope: global\n    match: {payload: file_write, path_glob: \"" + linkProj + "/secret/**\"}\n    action: deny\n"
	os.WriteFile(env.cfg.RulesPath, []byte(rules), 0o600)
	if err := env.srv.rules.Reload(); err != nil {
		t.Fatal(err)
	}
	write := func(path string) proto.Response {
		req := proto.Request{V: proto.Version, Kind: "permission",
			Session:  proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: realProj},
			ToolName: "Write", Payload: policy.Payload{Type: policy.PayloadFileWrite, Path: path, Cwd: realProj}, DeadlineSeconds: 60}
		var resp proto.Response
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, req), &resp)
		return resp
	}
	// 真实路径与链接写法都要命中：deny 是收紧，改写后不能漏掉原来能命中的写法
	for _, p := range []string{filepath.Join(realProj, "secret", "a.txt"), "secret/a.txt", filepath.Join(linkProj, "secret", "a.txt")} {
		if resp := write(p); resp.Decision != "deny" || resp.RuleID != "deny-secret" {
			t.Fatalf("写 %s 应命中 deny-secret: %+v", p, resp)
		}
	}
	if resp := write(filepath.Join(realProj, "ok.txt")); resp.Decision != "allow" {
		t.Fatalf("规则之外的项目内写入应放行: %+v", resp)
	}
}

// 终审 I2：项目路径经过符号链接（macOS 的 /var → /private/var 同理）时，交给 Decider（上报 hub）与写审计的载荷 Cwd
// 与项目根同一口径，都是解析链接后的路径。hub 侧“记住”按载荷与项目根重新定级（policy.ProposeRule），
// 两者口径不一会把项目内的相对路径写入误判成 write_outside，学不了规则。
func TestReportedPayloadUsesCleanedCwd(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{}), outcome: Outcome{Action: "allow", DecidedBy: "user"}}
	env := startServer(t, d, nil)
	linkProj, realProj := symlinkedProject(t, filepath.Dir(env.repo))
	if linkProj == realProj {
		t.Fatal("测试前提：链接写法与真实路径应不同")
	}
	// 不放行任何东西，请求才会交给 Decider
	os.WriteFile(env.cfg.RulesPath, []byte("version: 1\nrules: []\n"), 0o600)
	if err := env.srv.rules.Reload(); err != nil {
		t.Fatal(err)
	}
	reqs := []proto.Request{permission(linkProj, "mkdir build")}
	// 载荷没带 cwd 时取会话的 cwd，同样清理
	noCwd := permission(linkProj, "touch notes.txt")
	noCwd.Payload.Cwd = ""
	reqs = append(reqs, noCwd)
	for _, req := range reqs {
		conn, err := net.Dial("unix", env.cfg.SocketPath)
		if err != nil {
			t.Fatal(err)
		}
		proto.WriteLine(conn, req)
		waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
		pr := env.srv.pending.List()[0]
		if pr.ProjectRoot != realProj || pr.Redacted.Cwd != realProj {
			t.Fatalf("%q：上报的项目根 %q 与载荷 cwd %q 都应是 %q", req.Payload.Command, pr.ProjectRoot, pr.Redacted.Cwd, realProj)
		}
		// hub 的 learnRule 就是拿这两样调 ProposeRule：项目内写入应能学成规则
		rule, err := policy.ProposeRule(pr.Redacted, policy.Context{ProjectRoot: pr.ProjectRoot, HomeDir: env.cfg.HomeDir}, pr.ID)
		if err != nil {
			t.Fatalf("%q 在 hub 侧应能记住: %v", req.Payload.Command, err)
		}
		if rule.Match.Risk == nil || *rule.Match.Risk != policy.RiskWriteInProject {
			t.Fatalf("%q 应按项目内写入记住: %+v", req.Payload.Command, rule.Match)
		}
		d.release <- struct{}{}
		var resp proto.Response
		json.NewDecoder(conn).Decode(&resp)
		conn.Close()
		if resp.Decision != "allow" {
			t.Fatalf("%q 应得到 allow: %+v", req.Payload.Command, resp)
		}
		waitFor(t, func() bool { return env.srv.pending.Count() == 0 })
	}
	// 本机审计（也会转发给 hub）里的载荷 cwd 同样是清理后的
	for _, e := range readAudit(t, env.cfg.AuditPath) {
		if e.Payload == nil || e.Payload.Cwd != realProj {
			t.Fatalf("审计里的载荷 cwd 应是 %q: %+v", realProj, e.Payload)
		}
	}
}
