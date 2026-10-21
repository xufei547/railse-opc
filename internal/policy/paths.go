package policy

import (
	"path/filepath"
	"strings"
	"unicode"

	"github.com/bmatcuk/doublestar/v4"
)

// sensitivePatterns 命中即按 destructive 处理，比较前路径与模式都经 foldPath 折叠大小写
// （macOS 默认文件系统不区分大小写）。
// 以 ~/ 开头的只匹配家目录内的路径，用去掉家目录前缀后的相对路径匹配 ~/ 之后的部分；
// 其余（**/… 与 /dev/…）匹配完整绝对路径。
var sensitivePatterns = []string{
	"**/.env",
	"**/.env.*",
	"**/.envrc",
	"**/.npmrc",
	"**/.pgpass",
	"**/.vault-token",
	"**/*.pem",
	"**/*.key",
	"**/*.p12",
	"**/*.pfx",
	"**/id_rsa*",
	"**/id_ed25519*",
	"**/id_ecdsa*",
	"**/id_dsa*",
	"**/.claude/settings*.json",
	// 家目录规则的 **/ 版本：兜住 /System/Volumes/Data/Users/… 等别名和他人的家目录
	"**/.ssh/**",
	"**/.aws/**",
	"**/.gnupg/**",
	"**/.kube/**",
	"**/.config/gh/**",
	"**/.config/gcloud/**",
	"**/.azure/**",
	"**/.docker/config.json",
	"**/.netrc",
	"**/.pypirc",
	"**/.git-credentials",
	"**/.claude.json",
	"**/.claude/.credentials.json",
	"**/.codex/config.toml",
	"**/.codex/auth.json",
	"**/.gemini/settings.json",
	"**/.gemini/oauth_creds.json",
	"**/.railse/**",
	"~/.ssh/**",
	"~/.aws/**",
	"~/.gnupg/**",
	"~/.kube/**",
	"~/.config/gh/**",
	"~/.config/gcloud/**",
	"~/.azure/**",
	"~/.docker/config.json",
	"~/.git-credentials",
	"~/.netrc",
	"~/.npmrc",
	"~/.pypirc",
	"~/.claude.json",
	"~/.claude/.credentials.json",
	"~/.codex/config.toml",
	"~/.codex/auth.json",
	"~/.gemini/settings.json",
	"~/.gemini/oauth_creds.json",
	"~/.railse/**",
	"~/Library/Keychains/**",
	// 写入即可让任意命令在之后自动执行：shell 启动文件、git 钩子与全局配置
	"**/.git/hooks/**",
	"~/.gitconfig",
	"~/.zlogin",
	"~/.zshrc",
	"~/.zprofile",
	"~/.zshenv",
	"~/.bashrc",
	"~/.bash_profile",
	"~/.profile",
	// 块设备：直接写盘不可逆
	"/dev/disk*",
	"/dev/rdisk*",
	"/dev/sd*",
	"/dev/nvme*",
	"/dev/hd*",
	"/dev/vd*",
	"/dev/xvd*",
	"/dev/mmcblk*",
	"/dev/disk/**",
	"/dev/mapper/**",
	"/dev/md*",
	"/dev/dm-*",
	"/dev/loop*",
	"/dev/mem",
	"/dev/kmem",
}

// foldedSensitivePatterns 是 sensitivePatterns 经 foldPath 折叠后的副本，包初始化时算一次。
var foldedSensitivePatterns = func() []string {
	out := make([]string, len(sensitivePatterns))
	for i, pat := range sensitivePatterns {
		out[i] = foldPath(pat)
	}
	return out
}()

// foldPath 近似 APFS 不区分大小写的比较：每个字符先换成其 unicode.SimpleFold 轨道中
// 最小的字符再转小写，使 ſ（U+017F）、K（U+212A 开尔文符号）等与对应的 ASCII 字母等同。
func foldPath(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		lo := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			if f < lo {
				lo = f
			}
		}
		b.WriteRune(unicode.ToLower(lo))
	}
	return b.String()
}

// homePrefixes 是 expandHome 认识的家目录写法。
var homePrefixes = []string{"~", "$HOME", "${HOME}"}

// splitHome 判断 p 是否以家目录写法开头（单独出现或后跟 /），是则返回其后的部分。
func splitHome(p string) (rest string, ok bool) {
	for _, pre := range homePrefixes {
		if p == pre {
			return "", true
		}
		if strings.HasPrefix(p, pre+"/") {
			return p[len(pre)+1:], true
		}
	}
	return p, false
}

// expandHome 把开头的 ~、$HOME、${HOME} 换成家目录。家目录未知时原样返回。
func expandHome(p, home string) string {
	if home == "" {
		return p
	}
	if rest, ok := splitHome(p); ok {
		return filepath.Join(home, rest)
	}
	return p
}

// unresolvableChars 是展开家目录后仍出现就无法静态解析的字符：变量与 glob 元字符。
const unresolvableChars = "$*?[{"

// unresolvable 判断路径参数能否在不读环境的前提下解析，分类器也用它。以下情况无法解析：
// 家目录未知却以家目录写法开头；展开家目录后仍含 $ 或 glob 元字符 * ? [ {；
// 以 ~ 开头却不是 ~ 与 ~/（如 ~bob/x）。
func unresolvable(p, home string) bool {
	if rest, ok := splitHome(p); ok {
		return home == "" || strings.ContainsAny(rest, unresolvableChars)
	}
	return strings.HasPrefix(p, "~") || strings.ContainsAny(p, unresolvableChars)
}

// resolvePath 把路径变成以 cwd 为基准的干净绝对路径。
func resolvePath(p, cwd, home string) string {
	p = expandHome(p, home)
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// isSensitive 判断绝对路径是否命中敏感路径表，路径、家目录与模式都经 foldPath 折叠。
// 家目录未知时跳过 ~ 开头的规则；家目录只做前缀比较，不拼进 glob，含元字符也不受影响。
func isSensitive(abs, home string) bool {
	abs = foldPath(abs)
	rel, inHome := relWithin(abs, foldPath(home))
	for _, pat := range foldedSensitivePatterns {
		name := abs
		if strings.HasPrefix(pat, "~/") {
			if !inHome {
				continue
			}
			pat, name = pat[2:], rel
		}
		if ok, _ := doublestar.Match(pat, name); ok {
			return true
		}
	}
	return false
}

// isWithin 判断 abs 是否在 root 内，含 root 本身。root 为空时恒为 false。
func isWithin(abs, root string) bool {
	_, ok := relWithin(abs, root)
	return ok
}

// relWithin 在 abs 位于 root 内（含 root 本身）时返回相对路径与 true，否则返回 false。
// root 为空时恒为 false。
func relWithin(abs, root string) (string, bool) {
	if root == "" {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false
	}
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../")) {
		return rel, true
	}
	return "", false
}

// harmlessDevice 判断是否为写入无害的伪设备：空设备、标准流、终端与 /dev/fd/ 下的描述符。
func harmlessDevice(abs string) bool {
	switch abs {
	case "/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	return strings.HasPrefix(abs, "/dev/fd/")
}

// pathOperand 取出参数中要定级的路径：--name=value 与 -name=value 取 value；
// 其余 - 开头的选项、& 开头的描述符和空串返回 false，表示跳过。
func pathOperand(arg string) (string, bool) {
	if strings.HasPrefix(arg, "-") {
		i := strings.IndexByte(arg, '=')
		if i < 0 {
			return "", false
		}
		arg = arg[i+1:]
	}
	if arg == "" || strings.HasPrefix(arg, "&") {
		return "", false
	}
	return arg, true
}

// pathsRisk 给一组路径参数定级：无法解析的 unknown，敏感路径 destructive，
// 项目内 write_in_project，其余 write_outside。--name=value 形式的选项按 value 定级；
// 跳过其余 - 开头的选项、& 开头的描述符、空串和无害的伪设备。
// 全部跳过时返回 RiskRead，调用方自行与基础等级取最大值。
func pathsRisk(paths []string, cwd string, ctx Context) Risk {
	root := ctx.ProjectRoot
	if root == "" {
		root = cwd
	}
	risk := RiskRead
	for _, arg := range paths {
		p, ok := pathOperand(arg)
		if !ok {
			continue
		}
		if unresolvable(p, ctx.HomeDir) {
			risk = maxRisk(risk, RiskUnknown)
			continue
		}
		abs := resolvePath(p, cwd, ctx.HomeDir)
		if harmlessDevice(abs) {
			continue
		}
		switch {
		case isSensitive(abs, ctx.HomeDir):
			risk = maxRisk(risk, RiskDestructive)
		case isWithin(abs, root):
			risk = maxRisk(risk, RiskWriteInProject)
		default:
			risk = maxRisk(risk, RiskWriteOutside)
		}
	}
	return risk
}
