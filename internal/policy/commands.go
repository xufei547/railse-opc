package policy

import (
	"slices"
	"strings"
)

// cmdPattern 是命令表的一行。按表中顺序匹配，第一条命中生效，所以危险的排前面。
// any、anyPrefix、longAbbrev、flagChars、contains、when 设了任意一个时，至少要满足其中之一才命中。
// 有全局选项表的命令（git、npm 等，见 leadingOptions）先剥掉子命令前的全局选项再匹配。
type cmdPattern struct {
	name       string                   // argv[0] 的 basename
	sub        string                   // 可选：要求 argv[1] 等于它
	any        []string                 // 可选：参数等于其中之一
	anyPrefix  []string                 // 可选：参数以其中之一开头
	longAbbrev []string                 // 可选：参数是其中某个长选项或它的缩写（git 接受无歧义的前缀），见 hitsLongAbbrev
	flagChars  string                   // 可选：单横线参数（不含 -- 开头的）含其中任一字符，如 -rfv 含 r
	contains   []string                 // 可选：参数含其中任一子串
	when       func(args []string) bool // 可选：对 argv[1:] 整体判断
	bare       bool                     // 可选：只在没有任何参数时命中
	risk       Risk
	paths      bool // 命中后对 argv[1:] 做路径分析，取较大值
	interp     bool // 解释器：命中后按 interpRisk 定级（选项表见 cmdoptions.go）
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
	if len(p.any) == 0 && len(p.anyPrefix) == 0 && len(p.longAbbrev) == 0 && p.flagChars == "" && len(p.contains) == 0 && p.when == nil {
		return true
	}
	if p.when != nil && p.when(argv[1:]) {
		return true
	}
	for _, a := range argv[1:] {
		if p.hits(a) {
			return true
		}
	}
	return false
}

// hits 判断单个参数是否满足 any、anyPrefix、longAbbrev、flagChars、contains 中的任一条件。
func (p cmdPattern) hits(a string) bool {
	if hitsLongAbbrev(a, p.longAbbrev) {
		return true
	}
	for _, want := range p.any {
		if a == want {
			return true
		}
	}
	for _, s := range p.contains {
		if strings.Contains(a, s) {
			return true
		}
	}
	for _, pre := range p.anyPrefix {
		if strings.HasPrefix(a, pre) {
			return true
		}
	}
	return p.flagChars != "" && len(a) > 1 && a[0] == '-' && a[1] != '-' && strings.ContainsAny(a[1:], p.flagChars)
}

// hitsLongAbbrev 判断参数 --xyz（可带 =value）是否为 opts 中某个长选项的缩写：xyz 至少两个字符，
// 是该选项名的前缀。git 接受无歧义的前缀；有歧义时 git 报错不执行，这里多判也无害。
func hitsLongAbbrev(a string, opts []string) bool {
	if !strings.HasPrefix(a, "--") {
		return false
	}
	name, _, _ := strings.Cut(a[2:], "=")
	if len(name) < 2 {
		return false
	}
	for _, o := range opts {
		if strings.HasPrefix(strings.TrimPrefix(o, "--"), name) {
			return true
		}
	}
	return false
}

// cloudMutations 是云和集群 CLI 里会改变远端状态的子命令词。
var cloudMutations = []string{
	"deploy", "apply", "up", "push", "create", "delete", "update", "destroy", "run",
	"install", "upgrade", "set", "rollout", "scale", "exec", "put-object", "cp", "sync", "rm", "mb", "rb",
}

// argIs 返回一个 when 判断：argv[1:] 的第 i 个词是 words 之一。i 为 0 时看子命令，git lfs 这类两级的看 1。
func argIs(i int, words ...string) func(args []string) bool {
	return func(args []string) bool { return i < len(args) && slices.Contains(words, args[i]) }
}

// lfsOutward、lfsNetwork、lfsUnknown、lfsReadOnly 是 git-lfs（与 git lfs）的子命令分组。
// 上传、预推送钩子与服务端的锁是对外；下载是联网；装卸钩子（--system、--global 写到仓库之外）、
// 改写历史（migrate）、改 .gitattributes 按无法判断；只有查询是只读，其余写工作区或 .git 的按项目内改动。
var (
	lfsOutward  = []string{"push", "pre-push", "lock", "unlock"}
	lfsNetwork  = []string{"pull", "fetch", "clone", "smudge", "filter-process"}
	lfsUnknown  = []string{"install", "uninstall", "migrate", "track", "untrack", "update"}
	lfsReadOnly = []string{"ls-files", "status", "env", "version", "--version", "fsck", "pointer", "locks", "ext", "help", "--help"}
)

// pmConfigWrites 是 JS 包管理器 config 之后会改配置的动作；pmConfigReads 是只读的查询。
var (
	pmConfigWrites = []string{"set", "delete", "del", "rm", "unset", "edit", "fix"}
	pmConfigReads  = []string{"get", "list", "ls"}
)

// pmUserCommands 是 JS 包管理器里改用户级配置（set 是 config set 的别名）、登录与令牌，
// 以及改包的所有者、标签与访问权限的子命令：都不是项目内改动，按无法判断处理。
var pmUserCommands = []string{
	"set", "login", "logout", "adduser", "add-user", "token", "whoami",
	"owner", "author", "dist-tag", "dist-tags", "tag", "access", "team", "org", "hook", "profile",
}

// pmRegistryOutward 是改动注册表上已发布的包的子命令：撤回与弃用对所有用户可见。
var pmRegistryOutward = []string{"unpublish", "deprecate"}

// configSecretWords 出现在包管理器 config 的参数里时，读到的可能是令牌、口令或私钥：
// 新版 npm 会打码，老版本与 yarn 的 --no-redacted 会原样打印。比较时不区分大小写。
var configSecretWords = []string{"auth", "token", "password", "secret", "redact", "key", "cert"}

// mentionsSecret 判断参数里是否提到 configSecretWords 中的词。
func mentionsSecret(args []string) bool {
	for _, a := range args {
		a = strings.ToLower(a)
		for _, w := range configSecretWords {
			if strings.Contains(a, w) {
				return true
			}
		}
	}
	return false
}

// gitCredential 判断 git 的子命令是不是 credential 或 credential-store、credential-osxkeychain 这类凭据助手：
// fill、get 打印保存的口令与令牌，approve、store、erase 改动凭据存储。
func gitCredential(args []string) bool {
	return len(args) > 0 && (args[0] == "credential" || strings.HasPrefix(args[0], "credential-"))
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
	"golangci-lint", "mypy",
}

var commandTable = buildCommandTable()

func buildCommandTable() []cmdPattern {
	t := []cmdPattern{
		// ---- 不可逆 ----
		{name: "sudo", risk: RiskDestructive},
		{name: "rm", flagChars: "rR", any: []string{"--recursive"}, risk: RiskDestructive},
		// git 的长选项可以缩写（longAbbrev），短选项可以成簇（flagChars）
		{name: "git", sub: "reset", longAbbrev: []string{"--hard", "--merge"}, risk: RiskDestructive},
		// + 开头的 refspec 是强推，: 开头的删除远端引用；--force 前缀兜住 --force-with-lease=<ref> 与 --force-if-includes。
		// --receive-pack、--exec（以及 clone、fetch、pull 的 --upload-pack，clone 的 -u）指定远端执行的程序，
		// 执行任意命令，本地路径的远端就在本机执行：按不可逆处理
		{name: "git", sub: "push", flagChars: "fd", longAbbrev: []string{"--force", "--force-with-lease", "--delete", "--mirror", "--prune", "--receive-pack", "--exec"}, anyPrefix: []string{"+", ":", "--force"}, risk: RiskDestructive},
		{name: "git", sub: "clone", flagChars: "u", longAbbrev: []string{"--upload-pack"}, risk: RiskDestructive},
		{name: "git", sub: "fetch", longAbbrev: []string{"--upload-pack"}, risk: RiskDestructive},
		{name: "git", sub: "pull", longAbbrev: []string{"--upload-pack"}, risk: RiskDestructive},
		{name: "git", sub: "clean", risk: RiskDestructive},
		// --delete 只有和 --force 一起才强删，--force 单独就命中
		{name: "git", sub: "branch", flagChars: "Df", longAbbrev: []string{"--force"}, risk: RiskDestructive},
		{name: "git", sub: "checkout", flagChars: "f", longAbbrev: []string{"--force"}, any: []string{".", "--"}, risk: RiskDestructive},
		{name: "git", sub: "switch", flagChars: "f", longAbbrev: []string{"--discard-changes", "--force"}, risk: RiskDestructive},
		{name: "git", sub: "config", when: gitConfigInjects, risk: RiskDestructive},
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
		{name: "chmod", flagChars: "R", any: []string{"--recursive"}, risk: RiskDestructive},
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
		{name: "yarn", sub: "npm", any: []string{"publish"}, risk: RiskOutward},
		{name: "bun", sub: "publish", risk: RiskOutward},
		{name: "npm", when: argIs(0, pmRegistryOutward...), risk: RiskOutward},
		{name: "pnpm", when: argIs(0, pmRegistryOutward...), risk: RiskOutward},
		{name: "yarn", when: argIs(0, pmRegistryOutward...), risk: RiskOutward},
		{name: "git-lfs", any: lfsOutward, risk: RiskOutward},
		{name: "git", sub: "lfs", any: lfsOutward, risk: RiskOutward},
		{name: "cargo", sub: "publish", risk: RiskOutward},
		{name: "cargo", sub: "yank", risk: RiskOutward},
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
		{name: "curl", flagChars: "XdTF", any: []string{"--request", "--data", "--data-raw", "--data-binary", "--data-urlencode", "--data-ascii", "--json", "--form", "--form-string", "--upload-file"}, risk: RiskOutward},
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
		{name: "go", sub: "run", contains: []string{"@"}, risk: RiskNetwork}, // 远程模块 path@version
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
		{name: "git", sub: "remote", any: []string{"update", "prune", "show"}, risk: RiskNetwork},
		{name: "git-lfs", any: lfsNetwork, risk: RiskNetwork},
		{name: "git", sub: "lfs", any: lfsNetwork, risk: RiskNetwork},
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
		{name: "find", any: []string{"-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls"}, risk: RiskUnknown},
		// awk 调外部命令、读命令输出或重定向；-f、-E、-i、-l 从文件加载程序或扩展
		{name: "awk", contains: []string{"system", "getline", "|", ">"}, flagChars: "fEil", anyPrefix: []string{"--file", "--exec", "--include", "--load"}, risk: RiskUnknown},
		// make、just 的命令行变量能覆盖配方
		{name: "make", contains: []string{"="}, any: []string{"--eval", "-E"}, risk: RiskUnknown},
		{name: "just", contains: []string{"="}, any: []string{"--eval", "-E"}, risk: RiskUnknown},
		{name: "fd", flagChars: "xX", anyPrefix: []string{"--exec"}, risk: RiskUnknown},
		// go 的 -exec、-toolexec 用任意程序运行或包装构建出的二进制
		{name: "go", anyPrefix: []string{"-exec", "-toolexec", "--exec", "--toolexec"}, risk: RiskUnknown},
		{name: "rg", any: []string{"--pre"}, anyPrefix: []string{"--pre="}, risk: RiskUnknown},
		// 不带命令的 env、printenv 打印全部环境变量
		{name: "env", bare: true, risk: RiskUnknown},
		{name: "printenv", bare: true, risk: RiskUnknown},
		{name: "export", when: exportsInjected, risk: RiskUnknown},
		{name: "npm", sub: "exec", risk: RiskUnknown},
		{name: "pnpm", sub: "exec", risk: RiskUnknown},
		{name: "yarn", sub: "exec", risk: RiskUnknown},
		{name: "xargs", risk: RiskUnknown},
		{name: "source", risk: RiskUnknown},
		{name: ".", risk: RiskUnknown},
		{name: "eval", risk: RiskUnknown},
		{name: "docker", sub: "run", risk: RiskUnknown},
		{name: "docker", sub: "compose", risk: RiskUnknown},
		{name: "docker-compose", risk: RiskUnknown},
		// 写用户级配置、登录与令牌、读写保存的凭据：写到哪由 GOENV、npm 的 userconfig 与 --location 等决定，
		// 不是项目内改动。go env -w 持久化 GOFLAGS=-toolexec 这类注入，与命令前设置注入型环境变量同级；-u 同样改写配置文件
		{name: "go", sub: "env", any: []string{"-w", "--w", "-u", "--u"}, anyPrefix: []string{"-w=", "--w=", "-u=", "--u="}, risk: RiskUnknown},
		{name: "npm", sub: "config", any: pmConfigWrites, when: mentionsSecret, risk: RiskUnknown},
		{name: "pnpm", sub: "config", any: pmConfigWrites, when: mentionsSecret, risk: RiskUnknown},
		{name: "yarn", sub: "config", any: pmConfigWrites, when: mentionsSecret, risk: RiskUnknown},
		{name: "bun", sub: "config", any: pmConfigWrites, when: mentionsSecret, risk: RiskUnknown},
		{name: "npm", when: argIs(0, pmUserCommands...), risk: RiskUnknown},
		{name: "pnpm", when: argIs(0, pmUserCommands...), risk: RiskUnknown},
		{name: "yarn", when: argIs(0, pmUserCommands...), risk: RiskUnknown},
		{name: "bun", when: argIs(0, pmUserCommands...), risk: RiskUnknown},
		{name: "yarn", sub: "npm", any: []string{"login", "logout", "whoami"}, risk: RiskUnknown},
		{name: "cargo", when: argIs(0, "login", "logout", "owner"), risk: RiskUnknown},
		{name: "git", when: gitCredential, risk: RiskUnknown},
		{name: "git-lfs", any: lfsUnknown, risk: RiskUnknown},
		{name: "git", sub: "lfs", any: lfsUnknown, risk: RiskUnknown},
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
		// -i.bak 带后缀；flagChars 兜住 -Ei 这类选项簇与 BSD 的 -I
		{name: "sed", anyPrefix: []string{"-i", "--in-place"}, flagChars: "iI", risk: RiskWriteInProject, paths: true},
		{name: "sort", flagChars: "o", anyPrefix: []string{"--output"}, risk: RiskWriteInProject, paths: true},
		{name: "yq", flagChars: "i", anyPrefix: []string{"--inplace"}, risk: RiskWriteInProject, paths: true},
		// ---- git：只读子命令在前，其余项目内改动 ----
		{name: "git", sub: "stash", any: []string{"list", "show"}, risk: RiskRead},
		{name: "git", sub: "remote", any: []string{"add", "remove", "rm", "set-url", "rename"}, risk: RiskWriteInProject},
		{name: "git", sub: "config", any: []string{"--global", "--system"}, risk: RiskWriteOutside},
		{name: "git", sub: "checkout", any: []string{"-b", "-B", "--orphan"}, risk: RiskWriteInProject},
		{name: "git", sub: "checkout", risk: RiskUnknown},
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
		{name: "git", sub: "lfs", when: argIs(1, lfsReadOnly...), risk: RiskRead},
		{name: "git", risk: RiskWriteInProject},
		{name: "git-lfs", bare: true, risk: RiskRead},
		{name: "git-lfs", when: argIs(0, lfsReadOnly...), risk: RiskRead},
		{name: "git-lfs", risk: RiskWriteInProject},
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
		{name: "npm", sub: "config", any: pmConfigReads, risk: RiskRead},
		{name: "npm", any: []string{"--version", "-v"}, risk: RiskRead},
		{name: "npm", risk: RiskWriteInProject},
		{name: "pnpm", sub: "test", risk: RiskRead},
		{name: "pnpm", sub: "run", any: testScripts, risk: RiskRead},
		{name: "pnpm", sub: "lint", risk: RiskRead},
		{name: "pnpm", sub: "typecheck", risk: RiskRead},
		{name: "pnpm", sub: "config", any: pmConfigReads, risk: RiskRead},
		{name: "pnpm", risk: RiskWriteInProject},
		{name: "yarn", sub: "test", risk: RiskRead},
		{name: "yarn", sub: "run", any: testScripts, risk: RiskRead},
		{name: "yarn", sub: "lint", risk: RiskRead},
		{name: "yarn", sub: "typecheck", risk: RiskRead},
		{name: "yarn", sub: "config", any: pmConfigReads, risk: RiskRead},
		{name: "yarn", risk: RiskWriteInProject},
		{name: "bun", sub: "test", risk: RiskRead},
		{name: "bun", sub: "run", any: testScripts, risk: RiskRead},
		{name: "bun", risk: RiskWriteInProject, interp: true},
		// ---- 其他构建与检查工具 ----
		{name: "cargo", any: []string{"test", "check", "clippy", "--version"}, risk: RiskRead},
		{name: "cargo", risk: RiskWriteInProject},
		{name: "make", any: []string{"test", "check", "lint"}, risk: RiskRead},
		{name: "make", risk: RiskWriteInProject},
		{name: "just", any: []string{"test", "check", "lint"}, risk: RiskRead},
		{name: "just", risk: RiskWriteInProject},
		{name: "tsc", any: []string{"--noEmit"}, risk: RiskRead},
		{name: "tsc", risk: RiskWriteInProject},
		{name: "prettier", any: []string{"--write", "-w"}, risk: RiskWriteInProject, paths: true},
		{name: "prettier", risk: RiskRead},
		{name: "ruff", sub: "format", any: []string{"--check"}, risk: RiskRead},
		{name: "ruff", sub: "format", risk: RiskWriteInProject, paths: true},
		{name: "ruff", any: []string{"--fix", "--fix-only"}, risk: RiskWriteInProject},
		{name: "ruff", risk: RiskRead},
		{name: "black", any: []string{"--check"}, risk: RiskRead},
		{name: "black", risk: RiskWriteInProject, paths: true},
		{name: "gofmt", any: []string{"-w"}, risk: RiskWriteInProject, paths: true},
		{name: "gofmt", risk: RiskRead},
		{name: "biome", sub: "check", any: []string{"--write", "--fix", "--apply", "--apply-unsafe"}, risk: RiskWriteInProject},
		{name: "biome", sub: "check", risk: RiskRead},
		{name: "eslint", any: []string{"--fix"}, risk: RiskWriteInProject},
		{name: "biome", risk: RiskWriteInProject},
		// ---- 解释器：跑文件算项目内改动并看文件路径，内联代码与 stdin 无法判断 ----
		{name: "python", sub: "-m", any: []string{"pytest", "unittest"}, risk: RiskRead},
		{name: "python3", sub: "-m", any: []string{"pytest", "unittest"}, risk: RiskRead},
		{name: "python", any: []string{"--version", "-V"}, risk: RiskRead},
		{name: "python3", any: []string{"--version", "-V"}, risk: RiskRead},
		{name: "node", any: []string{"--version", "-v"}, risk: RiskRead},
		{name: "python", risk: RiskWriteInProject, interp: true},
		{name: "python3", risk: RiskWriteInProject, interp: true},
		{name: "node", risk: RiskWriteInProject, interp: true},
		{name: "deno", risk: RiskWriteInProject, interp: true},
		{name: "ruby", risk: RiskWriteInProject, interp: true},
		{name: "php", risk: RiskWriteInProject, interp: true},
		{name: "perl", risk: RiskWriteInProject, interp: true},
		// sh、bash、zsh、dash、fish 由 shellRisk 处理，不进命令表
	}
	for _, name := range readOnlyCommands {
		t = append(t, cmdPattern{name: name, risk: RiskRead})
	}
	return t
}
