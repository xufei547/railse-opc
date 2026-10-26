package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain 在任何测试之前把 HOME 换成新建的临时目录（结束时删掉），并清掉 RAILSE_HOME、RAILSE_HUB_HOME：
// 哪个测试漏设了它们，也写不到真实家目录下的 ~/.claude、~/.gemini、~/.codex、~/.railse。
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "rs")
	if err != nil {
		fmt.Fprintln(os.Stderr, "建临时 HOME:", err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	os.Unsetenv("RAILSE_HOME")
	os.Unsetenv("RAILSE_HUB_HOME")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
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
