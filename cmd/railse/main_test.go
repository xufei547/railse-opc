package main

import (
	"bytes"
	"strings"
	"testing"
)

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
