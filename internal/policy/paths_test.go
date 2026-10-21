package policy

import "testing"

var pathCtx = Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}

func TestResolvePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"src/a.go", "/home/u/proj/src/a.go"},
		{"./b", "/home/u/proj/b"},
		{"../x", "/home/u/x"},
		{"/tmp/x", "/tmp/x"},
		{"~/.ssh/config", "/home/u/.ssh/config"},
		{"~", "/home/u"},
	}
	for _, c := range cases {
		if got := resolvePath(c.in, "/home/u/proj", "/home/u"); got != c.want {
			t.Errorf("resolvePath(%q) = %q, 想要 %q", c.in, got, c.want)
		}
	}
}

func TestIsWithin(t *testing.T) {
	cases := []struct {
		abs, root string
		want      bool
	}{
		{"/home/u/proj/a", "/home/u/proj", true},
		{"/home/u/proj", "/home/u/proj", true},
		{"/home/u/projx/a", "/home/u/proj", false},
		{"/home/u/proj/..foo", "/home/u/proj", true},
		{"/home/u", "/home/u/proj", false},
		{"/x", "", false},
	}
	for _, c := range cases {
		if got := isWithin(c.abs, c.root); got != c.want {
			t.Errorf("isWithin(%q, %q) = %v", c.abs, c.root, got)
		}
	}
}

func TestIsSensitive(t *testing.T) {
	yes := []string{
		"/home/u/proj/.env", "/home/u/proj/.env.local", "/home/u/proj/certs/server.pem",
		"/home/u/.ssh/config", "/home/u/.aws/credentials", "/home/u/.claude/settings.json",
		"/home/u/proj/.claude/settings.local.json", "/home/u/.codex/config.toml", "/home/u/.npmrc",
	}
	no := []string{"/home/u/proj/src/a.go", "/home/u/proj/README.md", "/home/u/proj/.envrc.example"}
	for _, p := range yes {
		if !isSensitive(p, "/home/u") {
			t.Errorf("%s 应是敏感路径", p)
		}
	}
	for _, p := range no {
		if isSensitive(p, "/home/u") {
			t.Errorf("%s 不应是敏感路径", p)
		}
	}
}

func TestPathsRisk(t *testing.T) {
	cases := []struct {
		paths []string
		want  Risk
	}{
		{[]string{"src/a.go"}, RiskWriteInProject},
		{[]string{"-p", "build"}, RiskWriteInProject},
		{[]string{"/tmp/x"}, RiskWriteOutside},
		{[]string{"src/a.go", "/tmp/x"}, RiskWriteOutside},
		{[]string{".env"}, RiskDestructive},
		{[]string{"~/.ssh/id_rsa"}, RiskDestructive},
		{[]string{"/dev/null"}, RiskRead},
		{[]string{"&1"}, RiskRead},
		{nil, RiskRead},
	}
	for _, c := range cases {
		if got := pathsRisk(c.paths, "/home/u/proj", pathCtx); got != c.want {
			t.Errorf("pathsRisk(%q) = %s, 想要 %s", c.paths, got, c.want)
		}
	}
	// 项目根未知时以 cwd 为根
	noRoot := Context{HomeDir: "/home/u"}
	if got := pathsRisk([]string{"a"}, "/home/u/other", noRoot); got != RiskWriteInProject {
		t.Errorf("无 ProjectRoot 时应以 cwd 为根, 得到 %s", got)
	}
}
