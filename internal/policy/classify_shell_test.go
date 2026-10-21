package policy

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

var shellCtx = Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u", MachineID: "m1"}

func TestClassifyShellCorpus(t *testing.T) {
	f, err := os.Open("testdata/shell_corpus.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	line := 0
	n := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		cmd, wantName, ok := strings.Cut(text, "\t")
		if !ok {
			t.Fatalf("第 %d 行缺少制表符: %q", line, text)
		}
		want, err := ParseRisk(strings.TrimSpace(wantName))
		if err != nil {
			t.Fatalf("第 %d 行: %v", line, err)
		}
		if got := classifyShell(cmd, "/home/u/proj", shellCtx); got != want {
			t.Errorf("第 %d 行 %q = %s, 想要 %s", line, cmd, got, want)
		}
		n++
	}
	if n < 60 {
		t.Fatalf("语料只有 %d 条，至少要 60 条", n)
	}
}

func TestClassifyShellEmpty(t *testing.T) {
	if got := classifyShell("   ", "/home/u/proj", shellCtx); got != RiskUnknown {
		t.Fatalf("空命令 = %s, 想要 unknown", got)
	}
}

func TestStripWrappers(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"FOO=1", "go", "test"}, []string{"go", "test"}},
		{[]string{"env", "A=b", "npm", "test"}, []string{"npm", "test"}},
		{[]string{"time", "ls"}, []string{"ls"}},
		{[]string{"env"}, []string{"env"}},
		{[]string{"make", "VAR=1"}, []string{"make", "VAR=1"}},
	}
	for _, c := range cases {
		got := stripWrappers(c.in)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("stripWrappers(%q) = %q, 想要 %q", c.in, got, c.want)
		}
	}
}

func TestInjectsEnvNames(t *testing.T) {
	for _, name := range []string{
		"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_SEQUENCE_EDITOR",
		"GIT_TEMPLATE_DIR", "GIT_TRACE", "GIT_TRACE2_EVENT", "GIT_TRACE_PACKET", "NPM_CONFIG_PREFIX", "PYTHONHOME",
		"GOFLAGS", "CC", "CXX", "MAKEFLAGS", "DOCKER_HOST", "KUBECONFIG",
	} {
		if !injectsEnv(name) {
			t.Errorf("%s 应算注入型环境变量", name)
		}
	}
	for _, name := range []string{"FOO", "CI", "NODE_ENV", "CCACHE"} {
		if injectsEnv(name) {
			t.Errorf("%s 不应算注入型环境变量", name)
		}
	}
}
