package policy

import (
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
)

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
		// U+017F ſ 与 s 大小写等价，APFS 上是同一个文件
		"/home/u/.ſsh/config", "/home/u/.awſ/credentials",
		// 别名与他人家目录由 **/ 规则兜住
		"/System/Volumes/Data/Users/u/.aws/credentials", "/home/u/projects/.ssh/x",
		"/home/u/proj/.git/hooks/pre-commit", "/home/u/.gitconfig", "/home/u/.zprofile", "/home/u/.bashrc",
		"/home/u/proj/.vault-token", "/home/u/proj/k.pfx", "/home/u/proj/keys/id_dsa",
		"/dev/nvme0n1", "/dev/mmcblk0",
		// 写入能设置 core.hooksPath、core.fsmonitor
		"/home/u/proj/.git/config", "/home/u/proj/.Git/CONFIG",
		"/home/u/proj/.git/modules/sub/config", "/home/u/proj/.git/modules/a/modules/b/config",
		// git 的 XDG 配置与凭据；terraform 状态里有明文的资源密钥
		"/home/u/.config/git/config", "/home/u/.config/git/credentials", "/home/u/.config/git/hooks/pre-commit",
		"/System/Volumes/Data/Users/u/.config/git/config",
		"/home/u/proj/terraform.tfstate", "/home/u/proj/infra/terraform.tfstate.backup",
		"/home/u/proj/infra/x.tfstate.1700000000.backup", "/home/u/proj/TERRAFORM.TFSTATE",
		// railse setup 留下的用户级配置备份（同一秒的第二个加 -2）
		"/home/u/.claude/settings.json.railse-bak-20261027-050729", "/home/u/.gemini/settings.json.railse-bak-20261027-050729",
		"/home/u/.codex/config.toml.railse-bak-20261027-050729-2", "/home/u/dotfiles/config.toml.RAILSE-BAK-20261027-050729",
		// 登录或开机时自动运行的配置与 Codex 的钩子：写进去之后会自动执行任意命令
		"/home/u/Library/LaunchAgents/com.x.plist", "/home/u/library/launchagents/com.x.plist",
		"/Library/LaunchAgents/com.x.plist", "/Library/LaunchDaemons/com.x.plist",
		"/home/u/.config/systemd/user/x.service", "/home/u/.config/autostart/x.desktop", "/home/u/.codex/hooks.json",
		"/System/Volumes/Data/Users/u/Library/LaunchAgents/com.x.plist", "/home/other/.config/systemd/user/y.service",
		"/home/other/.codex/hooks.json",
	}
	no := []string{
		"/home/u/proj/src/a.go", "/home/u/proj/README.md", "/home/u/proj/.envrc.example",
		"/home/u/proj/src/env.go", "/home/u/.sshx/config",
		"/home/u/.config/gitx/config", "/home/u/.config/github-copilot/x.json", "/home/u/proj/tfstate.go",
		"/home/u/proj/docs/tfstate.md", "/home/u/proj/docs/railse-bak.md",
		"/home/u/Library/Caches/x", "/Library/Caches/x", "/home/u/.config/nvim/init.lua", "/home/u/.codex/notes.md",
		"/home/u/proj/docs/LaunchAgents.md", "/home/u/.config/systemd-notes.md",
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
	// 家目录含 glob 元字符时，~ 开头的规则按家目录内的相对路径匹配，[1] 不会被当成字符类
	weird := "/home/u[1]"
	for _, p := range []string{
		"/home/u[1]/.ssh/id_rsa", "/home/u[1]/.ssh/config", "/home/u[1]/.zshrc", "/home/other/.ssh/config",
	} {
		if !isSensitive(p, weird) {
			t.Errorf("家目录 %s 时 %s 应是敏感路径", weird, p)
		}
	}
	for _, p := range []string{"/home/other/sshd/config", "/home/u1/notes.txt", "/home/u1/.zshrc"} {
		if isSensitive(p, weird) {
			t.Errorf("家目录 %s 时 %s 不应是敏感路径", weird, p)
		}
	}
}

func TestSensitivePatternsValid(t *testing.T) {
	for _, p := range sensitivePatterns {
		if !doublestar.ValidatePattern(strings.TrimPrefix(p, "~/")) {
			t.Errorf("敏感路径模式 %q 不合法", p)
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
		{[]string{"/dev/stdout"}, RiskRead},
		{[]string{"/dev/fd/1"}, RiskRead},
		{[]string{"/dev/disk0"}, RiskDestructive},
		{[]string{"/dev/random"}, RiskWriteOutside},
		{[]string{"$DIR/x"}, RiskUnknown},
		{[]string{"~bob/x"}, RiskUnknown},
		{[]string{".ENV"}, RiskDestructive},
		{[]string{".env*"}, RiskUnknown},
		{[]string{".en?"}, RiskUnknown},
		{[]string{"~/.aw?/credentials"}, RiskUnknown},
		{[]string{"--output=/home/u/.ssh/authorized_keys"}, RiskDestructive},
		{[]string{"--target-directory=/home/u/.ssh"}, RiskDestructive},
		{[]string{"-p"}, RiskRead},
		{[]string{"/dev/disk/by-id/x"}, RiskDestructive},
		{[]string{"/dev/mapper/root"}, RiskDestructive},
		{[]string{"/dev/mem"}, RiskDestructive},
		{[]string{"$HOME"}, RiskWriteOutside},
		{[]string{"/dev/stdin"}, RiskRead},
		{[]string{"/dev/stderr"}, RiskRead},
		{[]string{"/dev/tty"}, RiskRead},
	}
	for _, c := range cases {
		if got := pathsRisk(c.paths, "/home/u/proj", pathCtx); got != c.want {
			t.Errorf("pathsRisk(%q) = %s, 想要 %s", c.paths, got, c.want)
		}
	}
	// 项目根未知时没有路径在项目内：写入一律按项目外，敏感路径与无法解析的照旧
	noRoot := Context{HomeDir: "/home/u"}
	for _, c := range []struct {
		path string
		want Risk
	}{
		{"a", RiskWriteOutside},
		{"/home/u/other/a", RiskWriteOutside},
		{".env", RiskDestructive},
		{"$X/a", RiskUnknown},
		{"/dev/null", RiskRead},
	} {
		if got := pathsRisk([]string{c.path}, "/home/u/other", noRoot); got != c.want {
			t.Errorf("无 ProjectRoot 时 pathsRisk(%q) = %s, 想要 %s", c.path, got, c.want)
		}
	}
	// 家目录未知时 ~ 与 $HOME 开头的路径无法解析
	noHome := Context{HomeDir: ""}
	for _, p := range []string{"~/x", "$HOME/x"} {
		if got := pathsRisk([]string{p}, "/home/u/proj", noHome); got != RiskUnknown {
			t.Errorf("无 HomeDir 时 pathsRisk(%q) = %s, 想要 unknown", p, got)
		}
	}
}

func TestLiteralPathRisk(t *testing.T) {
	cases := []struct {
		path string
		want Risk
	}{
		{"src/a.go", RiskWriteInProject},
		{"/tmp/x", RiskWriteOutside},
		{".env", RiskDestructive},
		{"-key.pem", RiskDestructive},
		{"-x/../../../../home/u/.ssh/authorized_keys", RiskDestructive},
		{"-x/../../../../etc/hosts", RiskWriteOutside},
		{"&notes.txt", RiskWriteInProject},
		{"--output=/tmp/x", RiskWriteInProject},
		{"/dev/null", RiskRead},
		{"$FOO/x", RiskUnknown},
		{"~bob/x", RiskUnknown},
		{"*.go", RiskUnknown},
		{"", RiskUnknown},
	}
	for _, c := range cases {
		if got := literalPathRisk(c.path, "/home/u/proj", pathCtx); got != c.want {
			t.Errorf("literalPathRisk(%q) = %s, 想要 %s", c.path, got, c.want)
		}
	}
	// 项目根未知时没有路径在项目内
	noRoot := Context{HomeDir: "/home/u"}
	if got := literalPathRisk("a", "/home/u/other", noRoot); got != RiskWriteOutside {
		t.Errorf("无 ProjectRoot 时 literalPathRisk(a) = %s, 想要 write_outside", got)
	}
	// 家目录未知时 ~ 开头的路径无法解析
	noHome := Context{HomeDir: ""}
	if got := literalPathRisk("~/x", "/home/u/proj", noHome); got != RiskUnknown {
		t.Errorf("无 HomeDir 时 literalPathRisk(%q) = %s, 想要 unknown", "~/x", got)
	}
}
