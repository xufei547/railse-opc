package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
)

// runTrustIn 在 cwd 里跑 railse trust（项目根按当前目录算），返回退出码与两路输出。
func runTrustIn(t *testing.T, cwd string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Chdir(cwd)
	var out, errb bytes.Buffer
	code = run(append([]string{"trust"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// railse trust：项目放行（默认 2 小时）、会话放行、列出、撤一条、撤全部；系统目录与格式错拒绝。
func TestTrustCommandSetListRevoke(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	dir := filepath.Dir(cfg.StatePath)

	code, out, errOut := runTrustIn(t, repo, "--dir", dir)
	if code != 0 || !strings.Contains(out, "项目 repo") || !strings.Contains(out, "2 小时") || !strings.Contains(out, "id ") {
		t.Fatalf("项目放行: code=%d out=%q err=%q", code, out, errOut)
	}
	// 放行生效：引擎定不了的联网命令在这个项目里直接放行
	hookOut, _, _ := runHookWith(t, claudePreToolUse(repo, "curl https://x.y", "default"), "claude", "--socket", sock)
	if !strings.Contains(hookOut, `"permissionDecision":"allow"`) || !strings.Contains(hookOut, "限时放行中") {
		t.Fatalf("项目放行后应命中: %q", hookOut)
	}

	code, out, errOut = runTrustIn(t, repo, "30m", "--session", "abc", "--note", "赶活", "--dir", dir)
	if code != 0 || !strings.Contains(out, "会话") || !strings.Contains(out, "abc") || !strings.Contains(out, "30 分钟") {
		t.Fatalf("会话放行: code=%d out=%q err=%q", code, out, errOut)
	}
	// 选项写在时长前面也认
	code, out, errOut = runTrustIn(t, repo, "--dir", dir, "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 2 || !strings.Contains(lines[0], "会话 claude abc") || !strings.Contains(lines[0], "赶活") ||
		!strings.Contains(lines[0], "cli") || !strings.Contains(lines[1], "项目 repo") {
		t.Fatalf("list 应两行、按到期先后: code=%d out=%q err=%q", code, out, errOut)
	}
	var stOut, stErr bytes.Buffer
	if code := run([]string{"agent", "status", "--dir", dir}, &stOut, &stErr); code != 0 || !strings.Contains(stOut.String(), "\ntrusts: 2\n") {
		t.Fatalf("status 应有 trusts 行: %q %q", stOut.String(), stErr.String())
	}

	id := strings.Fields(lines[0])[0]
	if code, out, errOut := runTrustIn(t, repo, "revoke", id, "--dir", dir); code != 0 || !strings.Contains(out, "已撤销") {
		t.Fatalf("revoke: code=%d out=%q err=%q", code, out, errOut)
	}
	if code, _, errOut := runTrustIn(t, repo, "revoke", id, "--dir", dir); code != 1 || !strings.Contains(errOut, "没有这条放行") {
		t.Fatalf("撤不存在的: code=%d err=%q", code, errOut)
	}
	if code, _, errOut := runTrustIn(t, repo, "revoke", "--all", "--dir", dir); code != 0 {
		t.Fatalf("revoke --all: %q", errOut)
	}
	// 超界截到 5 分钟～24 小时，输出里是截过的时长
	if code, out, errOut := runTrustIn(t, repo, "1m", "--dir", dir); code != 0 || !strings.Contains(out, "5 分钟") {
		t.Fatalf("1m 应截到 5 分钟: code=%d out=%q err=%q", code, out, errOut)
	}
	if code, out, errOut := runTrustIn(t, repo, "48h", "--session", "long", "--dir", dir); code != 0 || !strings.Contains(out, "24 小时") {
		t.Fatalf("48h 应截到 24 小时: code=%d out=%q err=%q", code, out, errOut)
	}
	runTrustIn(t, repo, "revoke", "--all", "--dir", dir)
	if _, out, _ := runTrustIn(t, repo, "list", "--dir", dir); !strings.Contains(out, "没有生效中的放行") {
		t.Fatalf("清空后: %q", out)
	}

	// 系统目录里不带 --session：报错，不放行
	if code, _, errOut := runTrustIn(t, "/usr/bin", "--dir", dir); code == 0 || !strings.Contains(errOut, "项目") {
		t.Fatalf("系统目录应拒绝: code=%d err=%q", code, errOut)
	}
	// 时长格式错、太短、用法错
	for _, args := range [][]string{
		{"2 hours", "--dir", dir},
		{"revoke", "--dir", dir},
		{"revoke", id, "--all", "--dir", dir},
		{"list", "extra", "--dir", dir},
		{"2h", "3h", "--dir", dir},
		{"list", "--session", "x", "--dir", dir},
		{"--bogus"},
	} {
		if code, out, errOut := runTrustIn(t, repo, args...); code == 0 || errOut == "" {
			t.Errorf("%q 应非零退出并说明: code=%d out=%q err=%q", args, code, out, errOut)
		}
	}
	if _, out, _ := runTrustIn(t, repo, "list", "--dir", dir); !strings.Contains(out, "没有生效中的放行") {
		t.Fatalf("出错的命令不该建放行: %q", out)
	}
}

// 守护进程没在跑时报连不上，退出码 1。
func TestTrustWithoutDaemon(t *testing.T) {
	_, repo, _ := startTestAgent(t)
	if code, _, errOut := runTrustIn(t, repo, "list", "--dir", filepath.Join(t.TempDir(), "none")); code != 1 || errOut == "" {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}

// 英文下用法、建、列、撤与守护进程回的固定错误都没有中文。
func TestTrustEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	_, repo, cfg := startTestAgent(t)
	dir := filepath.Dir(cfg.StatePath)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--dir", dir}, "2h"},
		{[]string{"90m", "--session", "abc", "--dir", dir}, "1h30m"},
		{[]string{"list", "--dir", dir}, "left"},
		{[]string{"revoke", "nope", "--dir", dir}, "No such trust"},
		{[]string{"2 hours", "--dir", dir}, "Bad duration"},
		{[]string{"revoke", "--all", "--dir", dir}, "Revoked"},
		{[]string{"list", "--dir", dir}, "No active trusts"},
		{[]string{"--help"}, "Usage"},
	} {
		_, out, errOut := runTrustIn(t, repo, c.args...)
		if got := out + errOut; !strings.Contains(got, c.want) || hasCJK(got) {
			t.Errorf("%q 英文输出要含 %q 且无中文: %q", c.args, c.want, got)
		}
	}
	if code, _, errOut := runTrustIn(t, "/usr/bin", "--dir", dir); code == 0 || hasCJK(errOut) {
		t.Errorf("系统目录的英文报错: %d %q", code, errOut)
	}
	if hasCJK(i18n.T(trustUsage)) || hasCJK(i18n.T(usage)) {
		t.Error("英文用法里有中文")
	}
}
