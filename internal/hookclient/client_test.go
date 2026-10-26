package hookclient

import (
	"bufio"
	"context"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
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

func TestPermissionGivesUpOnUnresponsiveDaemon(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "stuck.sock")
	// 只 Listen 不 Accept：像被 SIGSTOP 的守护进程，内核照收连接和数据，但永远没人答复
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	c := Client{SocketPath: sock, ConnectTimeout: 500 * time.Millisecond, WatchInterval: 100 * time.Millisecond, WatchTimeout: 100 * time.Millisecond, WatchFailures: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err = c.Permission(ctx, proto.Request{V: proto.Version, Kind: "permission"})
	if !errors.Is(err, ErrUnresponsive) || time.Since(start) > time.Second {
		t.Fatalf("守护进程不响应时应在 1 秒内以 ErrUnresponsive 放弃: err=%v 用时=%v", err, time.Since(start))
	}
}

// fakeDaemon 起一个假守护进程：permission 等 delay 后答 allow，status 立即答 OK 并计数。
func fakeDaemon(t *testing.T, delay time.Duration) (string, *atomic.Int32) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	probes := new(atomic.Int32)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				line, err := proto.ReadLine(bufio.NewReader(conn))
				if err != nil {
					return
				}
				switch kind, _ := proto.PeekKind(line); kind {
				case "permission":
					time.Sleep(delay)
					proto.WriteLine(conn, proto.Response{V: proto.Version, Decision: "allow"})
				case "control":
					probes.Add(1)
					proto.WriteLine(conn, proto.ControlReply{V: proto.Version, OK: true, Status: &proto.Status{}})
				}
			}()
		}
	}()
	return sock, probes
}

func TestWatchdogStopsAfterAnswer(t *testing.T) {
	// 答复慢但守护进程一直响应 status：看门狗不误判；收到答复后看门狗退出，不再探测
	sock, probes := fakeDaemon(t, 400*time.Millisecond)
	c := Client{SocketPath: sock, WatchInterval: 50 * time.Millisecond, WatchTimeout: 100 * time.Millisecond, WatchFailures: 2}
	before := runtime.NumGoroutine()
	resp, err := c.Permission(context.Background(), proto.Request{V: proto.Version, Kind: "permission"})
	if err != nil || resp.Decision != "allow" {
		t.Fatalf("守护进程响应正常时应拿到答复: %+v, %v", resp, err)
	}
	// 答复到达时可能有一次探测已发出、假守护进程还没来得及计数，先等它落定再取基数
	time.Sleep(150 * time.Millisecond)
	n := probes.Load()
	if n < 3 {
		t.Fatalf("等答复期间应探测过多次, 得到 %d", n)
	}
	time.Sleep(300 * time.Millisecond) // 看门狗若还在跑，这段时间会再探测约 6 次
	if got := probes.Load(); got != n {
		t.Fatalf("收到答复后看门狗应退出, 探测次数从 %d 涨到 %d", n, got)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("Permission 返回后不应留下 goroutine: 之前 %d 之后 %d", before, after)
	}
}
