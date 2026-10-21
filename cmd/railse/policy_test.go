package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyEvalDefaultRules(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"policy", "eval", "--cwd", t.TempDir(), "git status"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("退出码 %d, stderr=%s", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "allow\tread\t") {
		t.Fatalf("stdout = %q", out.String())
	}
	// 不给 --project 时项目根取 cwd 的绝对路径：引擎对空项目根 fail closed，CLI 不能留空
	out.Reset()
	code = run([]string{"policy", "eval", "--cwd", t.TempDir(), "touch a"}, &out, &errOut)
	if code != 0 || !strings.HasPrefix(out.String(), "allow\twrite_in_project\t") {
		t.Fatalf("code=%d stdout=%q", code, out.String())
	}
	out.Reset()
	code = run([]string{"policy", "eval", "--cwd", t.TempDir(), "git push origin main"}, &out, &errOut)
	if code != 0 || !strings.HasPrefix(out.String(), "ask\toutward\t") {
		t.Fatalf("code=%d stdout=%q", code, out.String())
	}
}

func TestPolicyEvalWithRulesFile(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, "rules.yaml")
	if err := os.WriteFile(rules, []byte("version: 1\nrules:\n  - id: allow-push\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: \"git push\"}\n    action: allow\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	code := run([]string{"policy", "eval", "--rules", rules, "--cwd", dir, "git push origin main"}, &out, &errOut)
	if code != 0 || !strings.HasPrefix(out.String(), "allow\toutward\t命中规则 allow-push") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestPolicyEvalOtherTypes(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"policy", "eval", "--type", "file_write", "--cwd", dir, "notes.md"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "allow\twrite_in_project\t") {
		t.Fatalf("file_write: code=%d stdout=%q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"policy", "eval", "--type", "mcp", "--cwd", dir, "github/get_issue"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "ask\tnetwork\t") {
		t.Fatalf("mcp: code=%d stdout=%q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"policy", "eval", "--type", "bogus", "--cwd", dir, "x"}, &out, &errOut); code != 2 {
		t.Fatalf("未知类型应退出码 2, 得到 %d", code)
	}
}

func TestPolicyEvalUsageErrors(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	// --type 在读规则文件之前校验：规则文件不存在时照样是用法错误
	var out, errOut bytes.Buffer
	if code := run([]string{"policy", "eval", "--type", "bogus", "--rules", missing, "--cwd", dir, "x"}, &out, &errOut); code != 2 {
		t.Fatalf("未知类型应先于读规则文件报用法错误, 退出码 %d, stderr=%q", code, errOut.String())
	}
	if !strings.Contains(errOut.String(), "bogus") {
		t.Fatalf("stderr 应指出未知类型, 得到 %q", errOut.String())
	}
	// mcp 参数必须是 服务/工具
	for _, arg := range []string{"github", "github/", "/get_issue"} {
		out.Reset()
		errOut.Reset()
		if code := run([]string{"policy", "eval", "--type", "mcp", "--cwd", dir, arg}, &out, &errOut); code != 2 || out.Len() != 0 {
			t.Fatalf("mcp 参数 %q 应是用法错误, 退出码 %d, stdout=%q", arg, code, out.String())
		}
	}
	out.Reset()
	if code := run([]string{"policy", "eval", "--type", "mcp", "--cwd", dir, "github/get_issue"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "ask\tnetwork\t") {
		t.Fatalf("mcp: code=%d stdout=%q", code, out.String())
	}
}

func TestPolicyCheck(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(good, []byte("version: 1\nrules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("version: 9\nrules: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"policy", "check", good}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "有效") {
		t.Fatalf("good: code=%d stdout=%q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"policy", "check", bad}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "version") {
		t.Fatalf("bad: code=%d stderr=%q", code, errOut.String())
	}
	// 读不到文件同样以 无效: 开头
	errOut.Reset()
	if code := run([]string{"policy", "check", filepath.Join(dir, "missing.yaml")}, &out, &errOut); code != 1 || !strings.HasPrefix(errOut.String(), "无效: ") {
		t.Fatalf("missing: code=%d stderr=%q", code, errOut.String())
	}
	if code := run([]string{"policy", "check"}, &out, &errOut); code != 2 {
		t.Fatalf("缺参数应退出码 2, 得到 %d", code)
	}
	if code := run([]string{"policy"}, &out, &errOut); code != 2 {
		t.Fatalf("无子命令应退出码 2, 得到 %d", code)
	}
}
