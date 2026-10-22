package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/proto"
)

func TestAgentServeAndControl(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr) }()
	sock := filepath.Join(cfgDir, "agent.sock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket 未出现")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, f := range []string{"rules.yaml", "audit.jsonl", "agent.log"} {
		if _, err := os.Stat(filepath.Join(cfgDir, f)); err != nil {
			t.Fatalf("%s 应被创建: %v", f, err)
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "paused: false") || !strings.Contains(out.String(), "rules: 2") {
		t.Fatalf("status: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := run([]string{"agent", "pause", "--dir", cfgDir}, &out, &errOut); code != 0 {
		t.Fatalf("pause: %d %s", code, errOut.String())
	}
	out.Reset()
	run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut)
	if !strings.Contains(out.String(), "paused: true (local)\n") {
		t.Fatalf("status after pause: %q", out.String())
	}
	run([]string{"agent", "resume", "--dir", cfgDir}, &out, &errOut)
	os.WriteFile(filepath.Join(cfgDir, "rules.yaml"), []byte("version: 1\nrules: []\n"), 0o600)
	out.Reset()
	if code := run([]string{"agent", "reload", "--dir", cfgDir}, &out, &errOut); code != 0 {
		t.Fatalf("reload: %d %s", code, errOut.String())
	}
	out.Reset()
	run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut)
	if !strings.Contains(out.String(), "rules: 0") {
		t.Fatalf("reload 后 rules 应为 0: %q", out.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve 退出出错: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve 没有在 ctx 取消后退出")
	}
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("退出后应删除 socket 文件")
	}
}

func TestAgentControlWithoutDaemon(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "status", "--dir", filepath.Join(dir, "none")}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "连接守护进程") {
		t.Fatalf("没有守护进程时应退出 1 并说明: %d %q", code, errOut.String())
	}
	if code := run([]string{"agent"}, &out, &errOut); code != 2 {
		t.Fatalf("缺子命令应退出 2, 得到 %d", code)
	}
}

func TestAgentRefusesSecondInstance(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr)
	sock := filepath.Join(cfgDir, "agent.sock")
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := runAgentServe(context.Background(), agent.ConfigIn(cfgDir), false, os.Stderr); err == nil || !strings.Contains(err.Error(), "已有守护进程") {
		t.Fatalf("第二个实例应拒绝启动: %v", err)
	}
}

func TestAgentRefusesEmptyHomeDir(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfg := agent.ConfigIn(dir)
	cfg.HomeDir = ""
	errc := make(chan error, 1)
	go func() { errc <- runAgentServe(context.Background(), cfg, false, os.Stderr) }()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "家目录") {
			t.Fatalf("家目录为空时应拒绝启动并说明: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("家目录为空时应立即返回")
	}
	if _, err := os.Stat(cfg.SocketPath); err == nil {
		t.Fatal("家目录为空时不应创建 socket")
	}
}

func TestAgentLogIsPrivate(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	os.MkdirAll(cfgDir, 0o700)
	logPath := filepath.Join(cfgDir, "agent.log")
	os.WriteFile(logPath, nil, 0o644)
	os.Chmod(logPath, 0o644) // 不受 umask 影响，确保起点是 0644
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr) }()
	sock := filepath.Join(cfgDir, "agent.sock")
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("socket 未出现")
		}
	}
	st, err := os.Stat(logPath)
	cancel()
	if serr := <-done; serr != nil {
		t.Fatalf("serve 退出出错: %v", serr)
	}
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("已有的 agent.log 应收紧为 0600: %v %v", st.Mode().Perm(), err)
	}
}

func TestPausedLineShowsSource(t *testing.T) {
	for _, c := range []struct {
		st   proto.Status
		want string
	}{
		{proto.Status{}, "paused: false"},
		{proto.Status{Paused: true, LocalPaused: true}, "paused: true (local)"},
		{proto.Status{Paused: true, HubPaused: true}, "paused: true (hub)"},
		{proto.Status{Paused: true, LocalPaused: true, HubPaused: true}, "paused: true (local+hub)"},
	} {
		if got := pausedLine(&c.st); got != c.want {
			t.Fatalf("%+v: got %q want %q", c.st, got, c.want)
		}
	}
}
