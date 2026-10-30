package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// realStdinIsTerminal 是 TestMain 换掉之前的 stdinIsTerminal，只给测它本身的测试用。
var realStdinIsTerminal func() bool

// errUnstubbedTailscale 是 TestMain 换上的 tailscaleFind、tailscaleRun 的报错。
var errUnstubbedTailscale = errors.New("测试没给 tailscale 打桩（用 fakeTailscale、noTailscale 或 tailscaleFound）")

// unstubbedTailscale 记下打到 TestMain 兜底上的调用：全部测试跑完后有记录，整个包判失败。
var unstubbedTailscale struct {
	mu    sync.Mutex
	calls []string
}

func recordUnstubbedTailscale(call string) {
	unstubbedTailscale.mu.Lock()
	defer unstubbedTailscale.mu.Unlock()
	unstubbedTailscale.calls = append(unstubbedTailscale.calls, call)
}

// takeUnstubbedTailscale 取走并清空记录。
func takeUnstubbedTailscale() []string {
	unstubbedTailscale.mu.Lock()
	defer unstubbedTailscale.mu.Unlock()
	calls := unstubbedTailscale.calls
	unstubbedTailscale.calls = nil
	return calls
}

// TestMain 在任何测试之前把 HOME 换成新建的临时目录（结束时删掉），并清掉 RAILSE_HOME、RAILSE_HUB_HOME：
// 哪个测试漏设了它们，也写不到真实家目录下的 ~/.claude、~/.gemini、~/.codex、~/.railse。
// setup 的向导一律当 stdin 不是终端、内容为空（要测向导的用 fakeStdin 换掉）：哪个测试都不会读真实的 os.Stdin。
// 找 tailscale、跑 tailscale 默认直接报错并记下来（开发机上装着真的 tailscale）：哪个测试忘了打桩，也不会真去找、真去跑它，
// 跑完时整个包判失败并列出那些调用。
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "rs")
	if err != nil {
		fmt.Fprintln(os.Stderr, "建临时 HOME:", err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	os.Unsetenv("RAILSE_HOME")
	os.Unsetenv("RAILSE_HUB_HOME")
	realStdinIsTerminal, stdinIsTerminal = stdinIsTerminal, func() bool { return false }
	setupStdin = strings.NewReader("")
	tailscaleFind = func() (string, error) {
		recordUnstubbedTailscale("找 tailscale")
		return "", errUnstubbedTailscale
	}
	tailscaleRun = func(_ context.Context, path string, args ...string) (string, error) {
		recordUnstubbedTailscale(path + " " + strings.Join(args, " "))
		return "", errUnstubbedTailscale
	}
	code := m.Run()
	if calls := takeUnstubbedTailscale(); len(calls) > 0 {
		fmt.Fprintf(os.Stderr, "有测试没给 tailscale 打桩就找了或跑了它（用 fakeTailscale、noTailscale 或 tailscaleFound）：%q\n", calls)
		if code == 0 {
			code = 1
		}
	}
	os.RemoveAll(home)
	os.Exit(code)
}

// TestMain 换上的兜底：没打桩时找 tailscale、跑 tailscale 都直接报错，不碰本机真的 tailscale（路径故意给不存在的），并记下来。
func TestTailscaleUnstubbedFailsByDefault(t *testing.T) {
	_, ferr := tailscaleFind()
	_, rerr := tailscaleRun(context.Background(), "/nonexistent/railse-test/tailscale", "status", "--self", "--json")
	calls := takeUnstubbedTailscale() // 这两次是故意的：取走，不让 TestMain 判失败
	if !errors.Is(ferr, errUnstubbedTailscale) || !errors.Is(rerr, errUnstubbedTailscale) {
		t.Fatalf("没打桩时找、跑 tailscale 都应报错: %v %v", ferr, rerr)
	}
	if want := []string{"找 tailscale", "/nonexistent/railse-test/tailscale status --self --json"}; strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("应记下这两次调用: %q", calls)
	}
}

// TestMain 换上的 HOME 是临时目录：漏设 HOME 的测试也碰不到真实家目录。
func TestHomeIsTemporary(t *testing.T) {
	if home := os.Getenv("HOME"); home == "" || !strings.HasPrefix(home, filepath.Clean(os.TempDir())+string(filepath.Separator)) {
		t.Fatalf("HOME = %q，应是 %s 下的临时目录", home, os.TempDir())
	}
}

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"version"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("退出码 = %d, 想要 0, stderr=%s", code, errOut.String())
	}
	if got := out.String(); !strings.HasPrefix(got, "railse ") {
		t.Fatalf("stdout = %q, 想要以 railse 开头", got)
	}
}

func TestRunUnknownSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"nope"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("退出码 = %d, 想要 2", code)
	}
	if !strings.Contains(errOut.String(), "nope") {
		t.Fatalf("stderr 应提到未知子命令, 得到 %q", errOut.String())
	}
}

func TestRunNoArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 {
		t.Fatalf("退出码 = %d, 想要 2", code)
	}
}
