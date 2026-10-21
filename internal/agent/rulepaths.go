package agent

import (
	"path/filepath"
	"strings"

	"github.com/railse/railse-opc/internal/policy"
)

// normalizeRulePaths 把规则里写成符号链接形式的路径换成真实路径，与守护进程对 cwd、项目根的处理
// （EvalSymlinks）保持同一种写法；macOS 上 /tmp、/var/folders 本身就是链接，写物理路径也要能命中。
// 不含符号链接的路径原样保留（包括 ~ 写法），行为与规范化之前完全一致。
//
//   - scope.project：项目根总是解析过链接的真实路径，直接换成真实路径。
//   - deny、ask 规则的 match.path_glob：引擎拿来比较的目标路径可能是真实路径（相对路径按解析过的 cwd 拼接），
//     也可能是请求原样给出的链接写法（绝对路径不解析），所以改写成 {原写法,真实路径} 两者都命中，只收紧不放宽。
//     allow 规则的 path_glob 不动，免得放宽。
//
// logf 在改写或放弃改写时记一条。
func normalizeRulePaths(rs *policy.RuleSet, home string, logf func(format string, args ...any)) {
	for i := range rs.Rules {
		r := &rs.Rules[i]
		if r.Scope.Project != "" {
			if _, real, rest, ok := resolvePatternBase(r.Scope.Project, home); ok {
				p := real + rest
				logf("规则 %s 的 scope.project 按真实路径改写: %q -> %q", r.ID, r.Scope.Project, p)
				r.Scope.Project = p
			}
		}
		if r.Match.PathGlob != "" && (r.Action == policy.ActionDeny || r.Action == policy.ActionAsk) {
			if base, real, rest, ok := resolvePatternBase(r.Match.PathGlob, home); ok {
				if strings.ContainsAny(base+real, ",{}") {
					logf("规则 %s 的 path_glob %q 经过符号链接，但路径含 , { } 无法写成二选一，保持原样", r.ID, r.Match.PathGlob)
					continue
				}
				p := "{" + base + "," + real + "}" + rest
				logf("规则 %s 的 path_glob 加上真实路径写法: %q -> %q", r.ID, r.Match.PathGlob, p)
				r.Match.PathGlob = p
			}
		}
	}
}

// globMeta 是 doublestar 的通配元字符。
const globMeta = "*?[{"

// resolvePatternBase 把路径模式拆成“第一个通配符之前的目录前缀”与其余部分，前缀先展开开头的家目录写法，
// 再按最长的已存在前缀解析符号链接，拼回不存在的部分。返回展开并清理后的前缀 base、解析后的前缀 real
// 与其余部分 rest（以 / 开头或为空）。以下情况 ok 为 false，调用方保留原样：
// 展开后不是绝对路径；含反斜杠转义；前缀为空（模式以 /通配 开头）；解析失败；解析结果与 base 相同。
func resolvePatternBase(pattern, home string) (base, real, rest string, ok bool) {
	p := pattern
	if after, isHome := splitHomePrefix(p); isHome {
		if home == "" {
			return "", "", "", false
		}
		p = filepath.Join(home, after) // 与引擎的 expandHome 相同
	}
	if !filepath.IsAbs(p) || strings.ContainsRune(p, '\\') {
		return "", "", "", false
	}
	if i := strings.IndexAny(p, globMeta); i >= 0 {
		cut := strings.LastIndexByte(p[:i], '/')
		p, rest = p[:cut], p[cut:]
	}
	if p == "" {
		return "", "", "", false
	}
	base = filepath.Clean(p)
	for cand := base; ; {
		if r, err := filepath.EvalSymlinks(cand); err == nil {
			real = filepath.Join(r, base[len(cand):]) // 拼回还不存在的部分
			break
		}
		parent := filepath.Dir(cand)
		if parent == cand {
			return "", "", "", false
		}
		cand = parent
	}
	if real == base {
		return "", "", "", false
	}
	return base, real, rest, true
}

// splitHomePrefix 认出引擎 expandHome 认识的家目录写法：~、$HOME、${HOME}，单独出现或后跟 /。
func splitHomePrefix(p string) (rest string, ok bool) {
	for _, pre := range []string{"~", "$HOME", "${HOME}"} {
		if p == pre {
			return "", true
		}
		if strings.HasPrefix(p, pre+"/") {
			return p[len(pre)+1:], true
		}
	}
	return p, false
}
