package policy

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// sensitivePatterns 命中即按 destructive 处理，匹配不区分大小写（macOS 默认文件系统不区分）。
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
	// shell 启动文件：写入即可让任意命令在下次登录时执行
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

// unresolvable 判断路径能否在不读环境的前提下解析：家目录未知却以家目录写法开头、
// 展开家目录后仍含 $、或以 ~ 开头却不是 ~ 与 ~/（如 ~bob/x），都无法解析。
func unresolvable(p, home string) bool {
	if rest, ok := splitHome(p); ok {
		return home == "" || strings.Contains(rest, "$")
	}
	return strings.HasPrefix(p, "~") || strings.Contains(p, "$")
}

// resolvePath 把路径变成以 cwd 为基准的干净绝对路径。
func resolvePath(p, cwd, home string) string {
	p = expandHome(p, home)
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// isSensitive 判断绝对路径是否命中敏感路径表，不区分大小写。
// 家目录未知时跳过 ~ 开头的规则；家目录只做前缀比较，不拼进 glob，含元字符也不受影响。
func isSensitive(abs, home string) bool {
	abs = strings.ToLower(abs)
	home = strings.ToLower(home)
	rel, inHome := "", isWithin(abs, home)
	if inHome {
		rel, _ = filepath.Rel(home, abs)
	}
	for _, pat := range sensitivePatterns {
		pat = strings.ToLower(pat)
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
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

// harmlessDevice 判断是否为写入无害的伪设备：空设备、标准流、终端与 /dev/fd/ 下的描述符。
func harmlessDevice(abs string) bool {
	switch abs {
	case "/dev/null", "/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	return strings.HasPrefix(abs, "/dev/fd/")
}

// pathsRisk 给一组路径参数定级：无法解析的 unknown，敏感路径 destructive，
// 项目内 write_in_project，其余 write_outside。跳过 - 开头的选项、& 开头的描述符、
// 空串和无害的伪设备。全部跳过时返回 RiskRead，调用方自行与基础等级取最大值。
func pathsRisk(paths []string, cwd string, ctx Context) Risk {
	root := ctx.ProjectRoot
	if root == "" {
		root = cwd
	}
	risk := RiskRead
	for _, p := range paths {
		if p == "" || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "&") {
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
