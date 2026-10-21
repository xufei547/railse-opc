package policy

// cmdPattern 是命令表的一行。按表中顺序匹配，第一条命中生效，所以危险的排前面。
type cmdPattern struct {
	name  string   // argv[0] 的 basename
	sub   string   // 可选：要求 argv[1] 等于它
	any   []string // 可选：argv[1:] 里出现任一即命中
	bare  bool     // 可选：只在没有任何参数时命中
	risk  Risk
	paths bool // 命中后对 argv[1:] 做路径分析，取较大值
}

func (p cmdPattern) matches(name string, argv []string) bool {
	if p.name != name {
		return false
	}
	if p.bare && len(argv) != 1 {
		return false
	}
	if p.sub != "" && (len(argv) < 2 || argv[1] != p.sub) {
		return false
	}
	if len(p.any) > 0 {
		found := false
		for _, a := range argv[1:] {
			for _, want := range p.any {
				if a == want {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// cloudMutations 是云和集群 CLI 里会改变远端状态的子命令词。
var cloudMutations = []string{
	"deploy", "apply", "up", "push", "create", "delete", "update", "destroy", "run",
	"install", "upgrade", "set", "rollout", "scale", "exec", "put-object", "cp", "sync", "rm", "mb", "rb",
}

// testScripts 是 JS 包管理器 run 之后按只读处理的测试与检查脚本名。
var testScripts = []string{"test", "lint", "typecheck", "check", "test:unit", "test:e2e", "format:check", "lint:check"}

var readOnlyCommands = []string{
	"ls", "cat", "head", "tail", "less", "more", "grep", "rg", "ag", "fd", "wc", "pwd", "which", "whereis",
	"type", "env", "printenv", "date", "whoami", "uname", "file", "stat", "du", "df", "tree", "diff", "cmp",
	"sort", "uniq", "cut", "awk", "jq", "yq", "man", "true", "false", "sleep", "test", "[", "basename",
	"dirname", "realpath", "readlink", "md5", "shasum", "sha256sum", "base64", "hexdump", "xxd", "strings",
	"column", "tr", "seq", "expr", "bc", "echo", "printf", "cd", "export", "alias", "set", "unset", "read",
	"exit", "sed", "find", "pytest", "jest", "vitest", "mocha", "rspec", "phpunit", "eslint",
	"golangci-lint", "mypy", "git-lfs",
}

var commandTable = buildCommandTable()

func buildCommandTable() []cmdPattern {
	t := []cmdPattern{
		// ---- 不可逆 ----
		{name: "sudo", risk: RiskDestructive},
		{name: "rm", any: []string{"-r", "-rf", "-fr", "-R", "-Rf", "-fR", "-rF", "--recursive"}, risk: RiskDestructive},
		{name: "git", sub: "reset", any: []string{"--hard"}, risk: RiskDestructive},
		{name: "git", sub: "push", any: []string{"-f", "--force", "--force-with-lease", "-d", "--delete"}, risk: RiskDestructive},
		{name: "git", sub: "clean", risk: RiskDestructive},
		{name: "git", sub: "branch", any: []string{"-D"}, risk: RiskDestructive},
		{name: "git", sub: "checkout", any: []string{"--"}, risk: RiskDestructive},
		{name: "git", sub: "restore", risk: RiskDestructive},
		{name: "git", sub: "stash", any: []string{"drop", "clear"}, risk: RiskDestructive},
		{name: "docker", sub: "system", any: []string{"prune"}, risk: RiskDestructive},
		{name: "docker", sub: "volume", any: []string{"rm", "prune"}, risk: RiskDestructive},
		{name: "docker", sub: "rm", any: []string{"-f", "--force"}, risk: RiskDestructive},
		{name: "kubectl", sub: "delete", risk: RiskDestructive},
		{name: "terraform", sub: "destroy", risk: RiskDestructive},
		{name: "dd", risk: RiskDestructive},
		{name: "shred", risk: RiskDestructive},
		{name: "mkfs", risk: RiskDestructive},
		{name: "diskutil", risk: RiskDestructive},
		{name: "chmod", any: []string{"-R", "--recursive"}, risk: RiskDestructive},
		{name: "chown", risk: RiskDestructive},
		{name: "killall", risk: RiskDestructive},
		{name: "pkill", risk: RiskDestructive},
		{name: "launchctl", risk: RiskDestructive},
		{name: "systemctl", risk: RiskDestructive},
		{name: "crontab", risk: RiskDestructive},
		// ---- 对外 ----
		{name: "git", sub: "push", risk: RiskOutward},
		{name: "gh", sub: "pr", any: []string{"create", "merge", "close", "comment", "review", "edit", "ready", "reopen"}, risk: RiskOutward},
		{name: "gh", sub: "issue", any: []string{"create", "close", "comment", "edit", "delete", "reopen"}, risk: RiskOutward},
		{name: "gh", sub: "release", risk: RiskOutward},
		{name: "gh", sub: "repo", any: []string{"create", "delete", "edit", "fork", "rename", "archive"}, risk: RiskOutward},
		{name: "gh", sub: "api", any: []string{"-X", "--method", "-f", "-F", "--field", "--raw-field", "--input"}, risk: RiskOutward},
		{name: "npm", sub: "publish", risk: RiskOutward},
		{name: "pnpm", sub: "publish", risk: RiskOutward},
		{name: "yarn", sub: "publish", risk: RiskOutward},
		{name: "cargo", sub: "publish", risk: RiskOutward},
		{name: "gem", sub: "push", risk: RiskOutward},
		{name: "twine", sub: "upload", risk: RiskOutward},
		{name: "poetry", sub: "publish", risk: RiskOutward},
		{name: "docker", sub: "push", risk: RiskOutward},
		{name: "vercel", risk: RiskOutward},
		{name: "netlify", sub: "deploy", risk: RiskOutward},
		{name: "fly", sub: "deploy", risk: RiskOutward},
		{name: "flyctl", sub: "deploy", risk: RiskOutward},
		{name: "wrangler", any: []string{"deploy", "publish"}, risk: RiskOutward},
		{name: "firebase", sub: "deploy", risk: RiskOutward},
		{name: "aws", any: cloudMutations, risk: RiskOutward},
		{name: "gcloud", any: cloudMutations, risk: RiskOutward},
		{name: "az", any: cloudMutations, risk: RiskOutward},
		{name: "kubectl", any: cloudMutations, risk: RiskOutward},
		{name: "helm", any: cloudMutations, risk: RiskOutward},
		{name: "terraform", any: cloudMutations, risk: RiskOutward},
		{name: "pulumi", any: cloudMutations, risk: RiskOutward},
		{name: "sam", sub: "deploy", risk: RiskOutward},
		{name: "serverless", sub: "deploy", risk: RiskOutward},
		{name: "cdk", sub: "deploy", risk: RiskOutward},
		{name: "heroku", risk: RiskOutward},
		{name: "railway", risk: RiskOutward},
		{name: "scp", risk: RiskOutward},
		{name: "rsync", risk: RiskOutward},
		{name: "mail", risk: RiskOutward},
		{name: "sendmail", risk: RiskOutward},
		{name: "msmtp", risk: RiskOutward},
		{name: "stripe", risk: RiskOutward},
		{name: "twilio", risk: RiskOutward},
		{name: "curl", any: []string{"-X", "--request", "-d", "--data", "--data-raw", "--data-binary", "--data-urlencode", "-F", "--form", "-T", "--upload-file"}, risk: RiskOutward},
		{name: "wget", any: []string{"--post-data", "--post-file", "--method"}, risk: RiskOutward},
		{name: "http", any: []string{"POST", "PUT", "PATCH", "DELETE"}, risk: RiskOutward},
		{name: "xh", any: []string{"POST", "PUT", "PATCH", "DELETE"}, risk: RiskOutward},
		// ---- 联网 ----
		{name: "curl", risk: RiskNetwork},
		{name: "wget", risk: RiskNetwork},
		{name: "http", risk: RiskNetwork},
		{name: "xh", risk: RiskNetwork},
		{name: "ssh", risk: RiskNetwork},
		{name: "nc", risk: RiskNetwork},
		{name: "ping", risk: RiskNetwork},
		{name: "dig", risk: RiskNetwork},
		{name: "nslookup", risk: RiskNetwork},
		{name: "host", risk: RiskNetwork},
		{name: "traceroute", risk: RiskNetwork},
		{name: "telnet", risk: RiskNetwork},
		{name: "npm", any: []string{"install", "i", "add", "ci", "update", "upgrade", "link", "audit", "view", "outdated"}, risk: RiskNetwork},
		{name: "npx", risk: RiskNetwork},
		{name: "pnpm", any: []string{"install", "i", "add", "update", "up", "dlx", "audit", "outdated"}, risk: RiskNetwork},
		{name: "yarn", any: []string{"install", "add", "upgrade", "up", "dlx", "audit", "outdated"}, risk: RiskNetwork},
		{name: "yarn", bare: true, risk: RiskNetwork},
		{name: "bun", any: []string{"install", "i", "add", "update", "x"}, risk: RiskNetwork},
		{name: "bunx", risk: RiskNetwork},
		{name: "pip", any: []string{"install", "download"}, risk: RiskNetwork},
		{name: "pip3", any: []string{"install", "download"}, risk: RiskNetwork},
		{name: "pipx", risk: RiskNetwork},
		{name: "uv", any: []string{"add", "sync", "pip", "lock", "tool", "run"}, risk: RiskNetwork},
		{name: "uvx", risk: RiskNetwork},
		{name: "poetry", any: []string{"add", "install", "update", "lock"}, risk: RiskNetwork},
		{name: "go", sub: "get", risk: RiskNetwork},
		{name: "go", sub: "install", risk: RiskNetwork},
		{name: "go", sub: "mod", any: []string{"download", "tidy"}, risk: RiskNetwork},
		{name: "cargo", any: []string{"add", "fetch", "install", "update"}, risk: RiskNetwork},
		{name: "brew", risk: RiskNetwork},
		{name: "apt", risk: RiskNetwork},
		{name: "apt-get", risk: RiskNetwork},
		{name: "gem", sub: "install", risk: RiskNetwork},
		{name: "composer", risk: RiskNetwork},
		{name: "mise", risk: RiskNetwork},
		{name: "git", sub: "fetch", risk: RiskNetwork},
		{name: "git", sub: "pull", risk: RiskNetwork},
		{name: "git", sub: "clone", risk: RiskNetwork},
		{name: "git", sub: "ls-remote", risk: RiskNetwork},
		{name: "git", sub: "submodule", any: []string{"update", "add"}, risk: RiskNetwork},
		{name: "docker", sub: "pull", risk: RiskNetwork},
		{name: "docker", sub: "build", risk: RiskNetwork},
		{name: "docker", sub: "login", risk: RiskNetwork},
		{name: "gh", sub: "api", risk: RiskNetwork},
		{name: "gh", sub: "pr", risk: RiskNetwork},
		{name: "gh", sub: "issue", risk: RiskNetwork},
		{name: "gh", sub: "repo", risk: RiskNetwork},
		{name: "gh", sub: "run", risk: RiskNetwork},
		{name: "gh", sub: "auth", risk: RiskNetwork},
		{name: "gh", sub: "search", risk: RiskNetwork},
		{name: "aws", risk: RiskNetwork},
		{name: "gcloud", risk: RiskNetwork},
		{name: "az", risk: RiskNetwork},
		{name: "kubectl", risk: RiskNetwork},
		{name: "helm", risk: RiskNetwork},
		{name: "terraform", risk: RiskNetwork},
		{name: "pulumi", risk: RiskNetwork},
		// ---- 明确无法判断 ----
		{name: "find", any: []string{"-delete", "-exec", "-execdir", "-ok", "-okdir"}, risk: RiskUnknown},
		{name: "xargs", risk: RiskUnknown},
		{name: "source", risk: RiskUnknown},
		{name: ".", risk: RiskUnknown},
		{name: "eval", risk: RiskUnknown},
		{name: "docker", sub: "run", risk: RiskUnknown},
		{name: "docker", sub: "compose", risk: RiskUnknown},
		{name: "docker-compose", risk: RiskUnknown},
		// ---- 项目内改动，带路径分析 ----
		{name: "mkdir", risk: RiskWriteInProject, paths: true},
		{name: "touch", risk: RiskWriteInProject, paths: true},
		{name: "cp", risk: RiskWriteInProject, paths: true},
		{name: "mv", risk: RiskWriteInProject, paths: true},
		{name: "ln", risk: RiskWriteInProject, paths: true},
		{name: "rm", risk: RiskWriteInProject, paths: true},
		{name: "rmdir", risk: RiskWriteInProject, paths: true},
		{name: "chmod", risk: RiskWriteInProject, paths: true},
		{name: "tee", risk: RiskWriteInProject, paths: true},
		{name: "patch", risk: RiskWriteInProject, paths: true},
		{name: "unzip", risk: RiskWriteInProject, paths: true},
		{name: "tar", risk: RiskWriteInProject, paths: true},
		{name: "sed", any: []string{"-i", "--in-place"}, risk: RiskWriteInProject, paths: true},
		// ---- git：只读子命令在前，其余项目内改动 ----
		{name: "git", sub: "stash", any: []string{"list", "show"}, risk: RiskRead},
		{name: "git", sub: "remote", any: []string{"add", "remove", "rm", "set-url", "rename"}, risk: RiskWriteInProject},
		{name: "git", sub: "config", any: []string{"--global", "--system"}, risk: RiskWriteOutside},
		{name: "git", sub: "status", risk: RiskRead},
		{name: "git", sub: "diff", risk: RiskRead},
		{name: "git", sub: "log", risk: RiskRead},
		{name: "git", sub: "show", risk: RiskRead},
		{name: "git", sub: "blame", risk: RiskRead},
		{name: "git", sub: "rev-parse", risk: RiskRead},
		{name: "git", sub: "ls-files", risk: RiskRead},
		{name: "git", sub: "remote", risk: RiskRead},
		{name: "git", sub: "describe", risk: RiskRead},
		{name: "git", sub: "shortlog", risk: RiskRead},
		{name: "git", sub: "reflog", risk: RiskRead},
		{name: "git", sub: "cat-file", risk: RiskRead},
		{name: "git", sub: "grep", risk: RiskRead},
		{name: "git", sub: "version", risk: RiskRead},
		{name: "git", sub: "help", risk: RiskRead},
		{name: "git", any: []string{"--version"}, risk: RiskRead},
		{name: "git", risk: RiskWriteInProject},
		// ---- go ----
		{name: "go", sub: "test", risk: RiskRead},
		{name: "go", sub: "vet", risk: RiskRead},
		{name: "go", sub: "version", risk: RiskRead},
		{name: "go", sub: "list", risk: RiskRead},
		{name: "go", sub: "env", risk: RiskRead},
		{name: "go", sub: "doc", risk: RiskRead},
		{name: "go", risk: RiskWriteInProject},
		// ---- JS 包管理器：测试与检查只读，其余脚本项目内改动 ----
		{name: "npm", sub: "test", risk: RiskRead},
		{name: "npm", sub: "run", any: testScripts, risk: RiskRead},
		{name: "npm", sub: "ls", risk: RiskRead},
		{name: "npm", any: []string{"--version", "-v"}, risk: RiskRead},
		{name: "npm", risk: RiskWriteInProject},
		{name: "pnpm", sub: "test", risk: RiskRead},
		{name: "pnpm", sub: "run", any: testScripts, risk: RiskRead},
		{name: "pnpm", sub: "lint", risk: RiskRead},
		{name: "pnpm", sub: "typecheck", risk: RiskRead},
		{name: "pnpm", risk: RiskWriteInProject},
		{name: "yarn", sub: "test", risk: RiskRead},
		{name: "yarn", sub: "run", any: testScripts, risk: RiskRead},
		{name: "yarn", sub: "lint", risk: RiskRead},
		{name: "yarn", sub: "typecheck", risk: RiskRead},
		{name: "yarn", risk: RiskWriteInProject},
		{name: "bun", sub: "test", risk: RiskRead},
		{name: "bun", sub: "run", any: testScripts, risk: RiskRead},
		{name: "bun", risk: RiskWriteInProject},
		// ---- 其他构建与检查工具 ----
		{name: "cargo", any: []string{"test", "check", "clippy", "--version"}, risk: RiskRead},
		{name: "cargo", risk: RiskWriteInProject},
		{name: "make", any: []string{"test", "check", "lint"}, risk: RiskRead},
		{name: "make", risk: RiskWriteInProject},
		{name: "just", any: []string{"test", "check", "lint"}, risk: RiskRead},
		{name: "just", risk: RiskWriteInProject},
		{name: "tsc", any: []string{"--noEmit"}, risk: RiskRead},
		{name: "tsc", risk: RiskWriteInProject},
		{name: "prettier", any: []string{"--write", "-w"}, risk: RiskWriteInProject},
		{name: "prettier", risk: RiskRead},
		{name: "ruff", sub: "format", any: []string{"--check"}, risk: RiskRead},
		{name: "ruff", sub: "format", risk: RiskWriteInProject},
		{name: "ruff", risk: RiskRead},
		{name: "black", any: []string{"--check"}, risk: RiskRead},
		{name: "black", risk: RiskWriteInProject},
		{name: "gofmt", any: []string{"-w"}, risk: RiskWriteInProject},
		{name: "gofmt", risk: RiskRead},
		{name: "biome", sub: "check", risk: RiskRead},
		{name: "biome", risk: RiskWriteInProject},
		// ---- 解释器：跑项目脚本算项目内改动 ----
		{name: "python", sub: "-m", any: []string{"pytest", "unittest"}, risk: RiskRead},
		{name: "python3", sub: "-m", any: []string{"pytest", "unittest"}, risk: RiskRead},
		{name: "python", any: []string{"--version", "-V"}, risk: RiskRead},
		{name: "python3", any: []string{"--version", "-V"}, risk: RiskRead},
		{name: "node", any: []string{"--version", "-v"}, risk: RiskRead},
		{name: "python", risk: RiskWriteInProject},
		{name: "python3", risk: RiskWriteInProject},
		{name: "node", risk: RiskWriteInProject},
		{name: "deno", risk: RiskWriteInProject},
		{name: "ruby", risk: RiskWriteInProject},
		{name: "php", risk: RiskWriteInProject},
		{name: "perl", risk: RiskWriteInProject},
		{name: "sh", risk: RiskWriteInProject},
		{name: "bash", risk: RiskWriteInProject},
		{name: "zsh", risk: RiskWriteInProject},
	}
	for _, name := range readOnlyCommands {
		t = append(t, cmdPattern{name: name, risk: RiskRead})
	}
	return t
}
