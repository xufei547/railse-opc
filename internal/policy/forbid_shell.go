package policy

import (
	"github.com/bmatcuk/doublestar/v4"
	"path/filepath"
	"slices"
	"strings"
)

// 角色禁用清单看穿命令替换（设计 §3.2 的禁用之后，审查补充）：`echo "$(git push)"`、反引号、进程替换 <(…) >(…) 与 eval 里的命令
// 同样参与 command_prefix、command_regex 的匹配。规则引擎的 deny 不做这一步（0.2 的语义不动），只有角色的 forbid 用。

// maxForbidDepth 是递归展开的层数上限；更深的按解析不了处理（fail closed：越界问人）。
const maxForbidDepth = 8

// 展开的工作量上限：累计处理的命令文本字节数与收集的命令条数。超了就停下、按解析不了处理（越界问人），
// 免得 1 MiB 的 eval 链或几十万个反引号把一次判定拖到几十秒。
const (
	maxForbidBytes = 2 << 20
	maxForbidCmds  = 1024
)

// subPlaceholder 是把命令替换挖掉之后填回外层的占位词：外层仍是能扫描的普通命令，挖出的命令单独匹配。
const subPlaceholder = "RAILSE_SUBST"

// forbidCommands 返回 shell 命令里要逐条参与禁用匹配的命令文本（外层、sh -c 的内联、eval 的参数、命令替换的内部，见 shellViews），
// 以及有没有解析不了、可能藏着命令的结构。工作目录与禁用无关，按空算。
func forbidCommands(cmd string) (cmds []string, opaque bool) {
	_, texts, op := shellViews(cmd, "", Context{})
	return texts, op
}

// extractSubstitutions 挖出引号外与双引号里的 $(…)、反引号，以及引号外的 <(…)、>(…)，返回换成占位词的外层与各段内部原文。
// 单引号里的不算（那是字面量）。括号按层数配对，内部的引号照样跳过。没闭合时 ok 为假。$(( 算术展开也按 $( 处理：
// 内部的 ( … ) 是裸括号，扫描时按解析不了处理，偏严。
func extractSubstitutions(cmd string) (outer string, bodies []string, ok bool) {
	r := []rune(cmd)
	var b strings.Builder
	inDouble := false
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == '\\' && i+1 < len(r):
			b.WriteRune(c)
			b.WriteRune(r[i+1])
			i++
		case c == '\'' && !inDouble:
			j := indexRune(r, i+1, '\'')
			if j < 0 {
				return cmd, bodies, false
			}
			b.WriteString(string(r[i : j+1]))
			i = j
		case c == '"':
			inDouble = !inDouble
			b.WriteRune(c)
		case c == '`':
			j := i + 1
			for j < len(r) && r[j] != '`' {
				if r[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(r) {
				return cmd, bodies, false
			}
			bodies = append(bodies, string(r[i+1:j]))
			b.WriteString(subPlaceholder)
			i = j
		case i+1 < len(r) && r[i+1] == '(' && (c == '$' || (!inDouble && (c == '<' || c == '>'))):
			end := matchParen(r, i+1)
			if end < 0 {
				return cmd, bodies, false
			}
			bodies = append(bodies, string(r[i+2:end]))
			b.WriteString(subPlaceholder)
			i = end
		default:
			b.WriteRune(c)
		}
	}
	if inDouble {
		return cmd, bodies, false
	}
	return b.String(), bodies, true
}

// matchParen 返回与 r[open]（一个左括号）配对的右括号下标，内部的引号与转义照样跳过；没有返回 -1。
func matchParen(r []rune, open int) int {
	depth := 0
	inDouble := false
	for i := open; i < len(r); i++ {
		switch c := r[i]; {
		case c == '\\':
			i++
		case c == '\'' && !inDouble:
			j := indexRune(r, i+1, '\'')
			if j < 0 {
				return -1
			}
			i = j
		case c == '"':
			inDouble = !inDouble
		case inDouble:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func indexRune(r []rune, from int, c rune) int {
	for i := from; i < len(r); i++ {
		if r[i] == c {
			return i
		}
	}
	return -1
}

// hasCommandForbid 报告禁用清单里有没有按命令匹配的条目（command_prefix、command_regex）：只有这类条目才会被藏在
// 看不见的结构里绕过。
func (r *Role) hasCommandForbid() bool {
	for _, m := range r.Forbid {
		if m.CommandPrefix != "" || m.CommandRegex != "" {
			return true
		}
	}
	return false
}

// railseEnvNames 与 railseEnvPrefixes 是改了就能换掉会话的角色、放行或配置位置的环境变量：RAILSE_*（角色、放行、守护进程目录等），
// HOME 与 XDG_*（钩子按它们找守护进程与配置；CLI 也按它们找自己的设置与钩子配置），CLAUDE_CONFIG_DIR、CODEX_HOME、GEMINI_*
// （各家 CLI 的配置目录：换了就等于换了一套没装钩子的配置）。有角色的会话改它们再起一个 CLI，就逃出了角色。
var (
	railseEnvNames    = []string{"HOME", "CLAUDE_CONFIG_DIR", "CODEX_HOME"}
	railseEnvPrefixes = []string{"RAILSE_", "XDG_", "GEMINI_"}
)

// isRailseEnvName 报告环境变量名 n 是不是上面这些。
func isRailseEnvName(n string) bool {
	if slices.Contains(railseEnvNames, n) {
		return true
	}
	for _, p := range railseEnvPrefixes {
		if strings.HasPrefix(n, p) && len(n) > len(p) {
			return true
		}
	}
	return false
}

// envSetters 是能设置或删除环境变量的命令：它们的参数里出现变量名本身（export X、unset X）也算改。
var envSetters = map[string]bool{"export": true, "unset": true, "declare": true, "typeset": true, "readonly": true, "local": true, "env": true}

// changesRailseEnv 判断命令是否设置、删除或清空 railseEnvNames 这些变量：每一段的每一层（见 walkLayers）——命令前的赋值、
// env NAME=、env -u、env --unset=、env -i、env -、exec -c，以及最内层的 export、declare、typeset、readonly、local、unset；
// sh -c、eval、命令替换里的照样（见 shellViews）。设置类命令的参数含变量展开（export "$V"=x）时看不出改的是谁，也算。
// 只看写，不看读（echo $RAILSE_ROLE 不算）。
func changesRailseEnv(cmd string) bool {
	views, _, _ := shellViews(cmd, "", Context{})
	for _, v := range views {
		for _, l := range v.layers {
			if l.envClear {
				return true
			}
			for _, n := range append(append([]string{}, l.envSet...), l.envUnset...) {
				if isRailseEnvName(n) {
					return true
				}
			}
			if l.wrapper == "" && len(l.argv) > 0 && envSetters[filepath.Base(l.argv[0])] {
				for i, w := range l.argv[1:] {
					name, _, _ := strings.Cut(w, "=")
					if isRailseEnvName(strings.TrimSuffix(name, "+")) || flagAt(l.flags, i+1).expand {
						return true
					}
				}
			}
		}
	}
	return false
}

// controlPatterns 是 Railse 自己的配置目录在敏感路径表里的那部分（~/.railse 与任何 .railse 目录），比较同 isSensitive（折叠大小写）。
// 加上 Context.ProtectedDirs（实际的守护进程目录），就是限时放行不覆盖的“控制面”：放行期间 agent 改规则、角色、放行表等于改掉
// 管它自己的东西。别的敏感路径（.env、ssh 密钥）不在此列，放行期间的行为照旧。
var controlPatterns = []string{"**/.railse/**", "**/.railse"}

// isControlPath 判断绝对路径是否在 Railse 自己的配置目录里。
func (c Context) isControlPath(abs string) bool {
	fa := foldPath(abs)
	for _, pat := range controlPatterns {
		if ok, _ := doublestar.Match(pat, fa); ok {
			return true
		}
	}
	for _, d := range c.ProtectedDirs {
		if d != "" && isWithin(fa, foldPath(filepath.Clean(d))) {
			return true
		}
	}
	return false
}

// touchesControlPlane 判断请求是否读写 Railse 自己的配置（见 controlPatterns）：文件写入看目标路径；shell 命令看每一段
// （看穿 sh -c、命令替换、进程替换与 eval）的参数与重定向，能解析的按路径判断，任何词里出现 .railse（不分大小写）也算，
// 偏严：变量拼出来的路径看不出来，至少字面上提到的都拦下。
func touchesControlPlane(p Payload, ctx Context) bool {
	switch p.Type {
	case PayloadFileWrite:
		return p.Path != "" && (ctx.isControlPath(resolvePath(p.Path, p.Cwd, ctx.HomeDir)) || mentionsRailseDir(p.Path))
	case PayloadShell:
		if tooLongToAnalyze(p) {
			// 不解析，只看字面：提到 .railse 或受保护目录的就算
			folded := foldPath(p.Command)
			if strings.Contains(folded, ".railse") {
				return true
			}
			for _, d := range ctx.ProtectedDirs {
				if d != "" && strings.Contains(folded, foldPath(filepath.Clean(d))) {
					return true
				}
			}
			return false
		}
		// 每一段的每一层（见 shellViews、walkLayers）：参数按这一层的工作目录解析（env -C、sudo -D、cd 之后，sh -c 里继承外层的），
		// 重定向由外层 shell 在这一段开始时的目录做；工作目录算不清之后的相对路径一律算触及
		views, _, _ := shellViews(p.Command, p.Cwd, ctx)
		for _, v := range views {
			for _, w := range v.seg.argv {
				if mentionsRailseDir(w) {
					return true
				}
			}
			for _, l := range v.layers {
				if len(l.argv) > 1 && touchesControlWords(l.argv[1:], l.cwd, l.lost, ctx) {
					return true
				}
			}
			if touchesControlWords(append(redirectPaths(v.seg.redirects), v.seg.inputs...), v.cwd, v.lost, ctx) {
				return true
			}
		}
	}
	return false
}

// touchesControlWords 看一组参数或重定向目标：字面提到 .railse 的、解析后在配置目录里的算；工作目录算不清（lost）时相对路径也算。
// URL 不当路径。
func touchesControlWords(words []string, cwd string, lost bool, ctx Context) bool {
	for _, w := range words {
		if mentionsRailseDir(w) {
			return true
		}
		q, ok := pathOperand(w)
		if !ok || strings.Contains(q, "://") || unresolvable(q, ctx.HomeDir) {
			continue
		}
		if lost && !filepath.IsAbs(expandHome(q, ctx.HomeDir)) {
			return true
		}
		if ctx.isControlPath(resolvePath(q, cwd, ctx.HomeDir)) {
			return true
		}
	}
	return false
}

// mentionsRailseDir 判断词里有没有 .railse 这一段（不分大小写）。
func mentionsRailseDir(w string) bool {
	f := foldPath(w)
	i := strings.Index(f, ".railse")
	for i >= 0 {
		end := i + len(".railse")
		if (i == 0 || f[i-1] == '/' || f[i-1] == '=' || f[i-1] == '~') && (end == len(f) || f[end] == '/') {
			return true
		}
		next := strings.Index(f[end:], ".railse")
		if next < 0 {
			break
		}
		i = end + next
	}
	return false
}
