package policy

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// sensitivePatterns 命中即按 destructive 处理。以 ~ 开头的在匹配前展开。
var sensitivePatterns = []string{
	"**/.env",
	"**/.env.*",
	"**/*.pem",
	"**/*.key",
	"**/id_rsa*",
	"**/id_ed25519*",
	"**/.claude/settings*.json",
	"~/.ssh/**",
	"~/.aws/**",
	"~/.gnupg/**",
	"~/.kube/**",
	"~/.config/gh/**",
	"~/.docker/config.json",
	"~/.netrc",
	"~/.npmrc",
	"~/.pypirc",
	"~/.codex/config.toml",
	"~/.gemini/settings.json",
	"~/.railse/**",
}

// expandHome 把开头的 ~ 换成家目录。
func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// resolvePath 把路径变成以 cwd 为基准的干净绝对路径。
func resolvePath(p, cwd, home string) string {
	p = expandHome(p, home)
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// isSensitive 判断绝对路径是否命中敏感路径表。
func isSensitive(abs, home string) bool {
	for _, pat := range sensitivePatterns {
		if ok, _ := doublestar.Match(expandHome(pat, home), abs); ok {
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

// pathsRisk 给一组路径参数定级：敏感路径 destructive，项目内 write_in_project，
// 其余 write_outside。跳过 - 开头的选项、& 开头的描述符、空串和 /dev/ 下的路径。
// 全部跳过时返回 RiskRead，调用方自行与基础等级取最大值。
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
		abs := resolvePath(p, cwd, ctx.HomeDir)
		if strings.HasPrefix(abs, "/dev/") {
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
