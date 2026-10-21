package agent

import (
	"bufio"
	"context"
	"encoding/json"
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
	srv := New(cfg, rules, audit, decider, log.New(os.Stderr, "test ", 0))
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
	os.WriteFile(cfg.SocketPath, nil, 0o600) // 假装上次崩溃留下的文件
	rules, _ := LoadRules(cfg.RulesPath)
	audit, _ := OpenAudit(cfg.AuditPath)
	srv := New(cfg, rules, audit, LocalDecider{}, log.New(os.Stderr, "", 0))
	if err := srv.Listen(); err != nil {
		t.Fatalf("应替换过期 socket 文件: %v", err)
	}
	srv.Close()
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
