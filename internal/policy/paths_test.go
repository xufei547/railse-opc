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
		{"$HOME/x", "/home/u/x"},
		{"${HOME}/x", "/home/u/x"},
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
		"/home/u/.AWS/credentials", "/home/u/proj/.ENV",
		"/home/u/.git-credentials", "/home/u/.config/gcloud/credentials.db", "/home/u/.claude.json",
		"/home/u/.claude/.credentials.json", "/home/u/.codex/auth.json", "/home/u/.gemini/oauth_creds.json",
		"/home/u/.zshrc", "/home/u/proj/.pgpass", "/home/u/proj/.envrc", "/home/u/proj/.npmrc",
		"/home/u/proj/keys/id_ecdsa", "/home/u/proj/cert.p12", "/dev/disk0", "/dev/sda1",
	}
	no := []string{
		"/home/u/proj/src/a.go", "/home/u/proj/README.md", "/home/u/proj/.envrc.example",
		"/home/u/proj/src/env.go", "/home/u/projects/.ssh/x",
	}
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
	// 家目录含 glob 元字符时，~ 开头的规则按家目录内的相对路径匹配，不受影响
	weird := "/home/u[1]"
	for _, p := range []string{"/home/u[1]/.ssh/id_rsa", "/home/u[1]/.ssh/config"} {
		if !isSensitive(p, weird) {
			t.Errorf("家目录 %s 下 %s 应是敏感路径", weird, p)
		}
	}
	if isSensitive("/home/other/.ssh/config", weird) {
		t.Errorf("家目录 %s 时 /home/other/.ssh/config 不应是敏感路径", weird)
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
		{[]string{"/dev/stdout"}, RiskRead},
		{[]string{"/dev/fd/1"}, RiskRead},
		{[]string{"/dev/disk0"}, RiskDestructive},
		{[]string{"/dev/random"}, RiskWriteOutside},
		{[]string{"$DIR/x"}, RiskUnknown},
		{[]string{"~bob/x"}, RiskUnknown},
		{[]string{".ENV"}, RiskDestructive},
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
	// 家目录未知时 ~ 与 $HOME 开头的路径无法解析
	noHome := Context{HomeDir: ""}
	for _, p := range []string{"~/x", "$HOME/x"} {
		if got := pathsRisk([]string{p}, "/home/u/proj", noHome); got != RiskUnknown {
			t.Errorf("无 HomeDir 时 pathsRisk(%q) = %s, 想要 unknown", p, got)
		}
	}
}
