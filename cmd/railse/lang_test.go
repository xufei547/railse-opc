package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
)

func TestLangWriteAndShow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAILSE_HOME", dir)
	t.Setenv("RAILSE_LANG", "")
	i18n.Reset()
	t.Cleanup(func() { i18n.Set(i18n.ZH) })

	var out, errOut bytes.Buffer
	if code := run([]string{"lang", "en"}, &out, &errOut); code != 0 {
		t.Fatalf("退出码 %d, stderr=%s", code, errOut.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, i18n.FileName))
	if err != nil || string(b) != "en\n" {
		t.Fatalf("lang 文件 = %q, %v", b, err)
	}
	out.Reset()
	i18n.Reset()
	if code := run([]string{"lang"}, &out, &errOut); code != 0 {
		t.Fatalf("退出码 %d", code)
	}
	if got := out.String(); !strings.Contains(got, "en") || !strings.Contains(got, "lang file") {
		t.Fatalf("stdout = %q，想要说明当前是 en 且来自 lang 文件", got)
	}
}

func TestLangRejectsOther(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAILSE_HOME", dir)
	var out, errOut bytes.Buffer
	if code := run([]string{"lang", "fr"}, &out, &errOut); code != 2 {
		t.Fatalf("退出码 %d，想要 2", code)
	}
	if !strings.Contains(errOut.String(), "zh") {
		t.Fatalf("stderr 要提示只能 zh 或 en：%s", errOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, i18n.FileName)); !os.IsNotExist(err) {
		t.Fatalf("非法语言不该写出 lang 文件: %v", err)
	}
	errOut.Reset()
	if code := run([]string{"lang", "en", "x"}, &out, &errOut); code != 2 {
		t.Fatalf("多余参数应退出 2，实际 %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, i18n.FileName)); !os.IsNotExist(err) {
		t.Fatalf("多余参数不该写出 lang 文件: %v", err)
	}
}

func TestLangWarnsWhenEnvOverrides(t *testing.T) {
	t.Setenv("RAILSE_HOME", t.TempDir())
	t.Setenv("RAILSE_LANG", "zh")
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	var out, errOut bytes.Buffer
	if code := run([]string{"lang", "en"}, &out, &errOut); code != 0 {
		t.Fatalf("退出码 %d", code)
	}
	if !strings.Contains(out.String(), "RAILSE_LANG=zh") || !strings.Contains(out.String(), "takes priority") {
		t.Fatalf("环境变量与写入值不同时要提示：%q", out.String())
	}
	out.Reset()
	t.Setenv("RAILSE_LANG", "en")
	run([]string{"lang", "en"}, &out, &errOut)
	if strings.Contains(out.String(), "RAILSE_LANG=") {
		t.Fatalf("相同时不该提示：%q", out.String())
	}
}

func TestHelpFlag(t *testing.T) {
	for _, a := range []string{"--help", "-h", "help"} {
		var out, errOut bytes.Buffer
		if code := run([]string{a}, &out, &errOut); code != 0 {
			t.Fatalf("%s: 退出码 %d", a, code)
		}
		if !strings.Contains(out.String(), "railse") || !strings.Contains(out.String(), "lang") || errOut.Len() != 0 {
			t.Fatalf("%s: 用法要在 stdout 且有 lang 子命令：%s", a, out.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 || out.Len() != 0 || !strings.Contains(errOut.String(), "lang") {
		t.Fatalf("无参数应打到 stderr 退出 2: %d %q %q", code, out.String(), errOut.String())
	}
}

func TestUsageEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	var out, errOut bytes.Buffer
	run([]string{"--help"}, &out, &errOut)
	if strings.ContainsAny(out.String(), "用法子命令") {
		t.Fatalf("英文用法里不该有中文：%s", out.String())
	}
	if !strings.Contains(out.String(), "Usage: railse <command>") {
		t.Fatalf("英文用法 = %s", out.String())
	}
}
