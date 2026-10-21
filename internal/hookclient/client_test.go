package hookclient

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

func startAgent(t *testing.T) (string, string) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := agent.ConfigIn(filepath.Join(dir, "h"))
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
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	return cfg.SocketPath, repo
}

func TestPermissionEventControl(t *testing.T) {
	sock, repo := startAgent(t)
	c := Client{SocketPath: sock, ConnectTimeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.Permission(ctx, proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s", Cwd: repo}, ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "ls", Cwd: repo}, DeadlineSeconds: 60})
	if err != nil || resp.Decision != "allow" {
		t.Fatalf("Permission: %+v, %v", resp, err)
	}
	if err := c.Event(proto.Event{V: proto.Version, Kind: "done", Session: proto.Session{Tool: "claude", CLISessionID: "s"}, Message: "x"}); err != nil {
		t.Fatalf("Event: %v", err)
	}
	reply, err := c.Control("status")
	if err != nil || !reply.OK || reply.Status == nil {
		t.Fatalf("Control: %+v, %v", reply, err)
	}
}

func TestNoDaemon(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	c := Client{SocketPath: filepath.Join(dir, "none.sock"), ConnectTimeout: 500 * time.Millisecond}
	start := time.Now()
	if _, err := c.Permission(context.Background(), proto.Request{V: proto.Version, Kind: "permission"}); err == nil {
		t.Fatal("没有守护进程应报错")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("连不上时应很快返回")
	}
	if err := c.Event(proto.Event{V: proto.Version, Kind: "done"}); err == nil {
		t.Fatal("Event 也应报错")
	}
}
