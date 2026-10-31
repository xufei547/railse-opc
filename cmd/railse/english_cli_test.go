package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
)

// 英文下开机自启的典型输出（--dry-run、--status、用法错）没有中文；runCommand 已被 autostartEnv 打桩，不会真的调 launchctl/systemctl。
func TestAutostartEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	home, rec := autostartEnv(t)
	bin := filepath.Join(home, "railse")
	for _, c := range []struct {
		goos string
		uid  int
		args []string
		want int
	}{
		{"darwin", 501, []string{"--dry-run", "--bin", bin, "--dir", filepath.Join(home, "d")}, 0},
		{"linux", 1000, []string{"--dry-run", "--bin", bin}, 0},
		{"linux", 1000, []string{"--status", "--bin", bin}, 0},
		{"linux", 1000, []string{"--uninstall", "--dry-run", "--bin", bin}, 0},
		{"linux", 1000, []string{"--hub", "--dry-run", "--bin", bin, "--", "--listen", ":443"}, 0},
		{"linux", 1000, []string{"--status", "--uninstall"}, 2},
		{"linux", 1000, []string{"--system"}, 2},
		{"linux", 0, []string{"--bin", bin}, 1},
		{"freebsd", 1000, []string{}, 1},
		{"linux", 1000, []string{"extra"}, 2},
	} {
		var out, errOut bytes.Buffer
		if code := runAutostartOS(c.goos, c.uid, c.args, &out, &errOut); code != c.want {
			t.Errorf("%s %v: 想要退出 %d，得到 %d\n%s%s", c.goos, c.args, c.want, code, out.String(), errOut.String())
		}
		if got := out.String() + errOut.String(); got == "" || hasCJK(got) {
			t.Errorf("%s %v 英文输出要非空且无中文: %q", c.goos, c.args, got)
		}
	}
	// 没托管：systemctl is-active 失败、托管文件不在
	rec.respond = func([]string) (string, error) { return "inactive", errors.New("exit status 3") }
	var out, errOut bytes.Buffer
	if code := runAutostartOS("linux", 1000, []string{"--status", "--hub", "--bin", bin}, &out, &errOut); code != 1 || hasCJK(out.String()+errOut.String()) {
		t.Errorf("没托管时的英文 status: %d %q", code, out.String())
	}
	for _, call := range rec.calls {
		if call[0] != "launchctl" && call[0] != "systemctl" {
			t.Errorf("意外的命令 %v", call)
		}
	}
	if hasCJK(i18n.T(autostartUsage)) || hasCJK(i18n.T(hubUsage)) || hasCJK(i18n.T(agentUsage)) || hasCJK(i18n.T(policyUsage)) {
		t.Error("英文用法里有中文")
	}
}

// 英文下 hub token、hub password、policy 的输出没有中文；密码太短在命令行就被拦下（规则与 internal/hub/auth.go 同步）。
func TestHubAndPolicyEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	dir := tempDir(t)
	var out, errOut bytes.Buffer
	if code := runHub([]string{"token", "--dir", dir}, nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), "Enroll token: ") || hasCJK(out.String()+errOut.String()) {
		t.Fatalf("hub token 英文输出: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := runHub([]string{"webhook-token", "--dir", dir}, nil, &out, &errOut); code != 0 || hasCJK(out.String()+errOut.String()) {
		t.Fatalf("webhook-token 英文输出: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := runHub([]string{"vapid", "--dir", dir}, nil, &out, &errOut); code != 0 || hasCJK(out.String()+errOut.String()) {
		t.Fatalf("vapid 英文输出: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := runHub([]string{"password", "--dir", dir}, strings.NewReader("short\n"), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "at least 8 characters") || hasCJK(errOut.String()) {
		t.Fatalf("太短的密码要在英文下被拦: %d %q", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := runHub([]string{"password", "--dir", dir}, strings.NewReader("long enough password\n"), &out, &errOut); code != 0 || hasCJK(out.String()+errOut.String()) {
		t.Fatalf("设密码英文输出: %d %q %q", code, out.String(), errOut.String())
	}
	for _, args := range [][]string{{"hub"}, {"hub", "bogus"}, {"hub", "token", "--ttl", "0s"}, {"hub", "run", "--acme-email", "a@b.c"}, {"policy"}, {"policy", "bogus"}, {"policy", "eval", "--type", "x", "y"}, {"policy", "eval", "--type", "mcp", "nogood"}, {"policy", "check"}, {"policy", "check", filepath.Join(dir, "none.yaml")}} {
		out.Reset()
		errOut.Reset()
		run(args, &out, &errOut)
		if got := out.String() + errOut.String(); got == "" || hasCJK(got) {
			t.Errorf("%v 英文输出要非空且无中文: %q", args, got)
		}
	}
	out.Reset()
	errOut.Reset()
	if code := run([]string{"policy", "eval", "--cwd", dir, "git status"}, &out, &errOut); code != 0 || hasCJK(out.String()) {
		t.Errorf("policy eval 英文输出: %d %q %q", code, out.String(), errOut.String())
	}
}

// 中文下太短的网页密码在命令行就被拦下，句子与 internal/hub/auth.go 的 SetPassword 逐字相同。
func TestHubPasswordTooShortChinese(t *testing.T) {
	dir := tempDir(t)
	var out, errOut bytes.Buffer
	if code := runHub([]string{"password", "--dir", dir}, strings.NewReader("short\n"), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "密码至少 8 个字符\n") {
		t.Fatalf("太短的密码: %d %q", code, errOut.String())
	}
}
