package policy

import (
	"github.com/bmatcuk/doublestar/v4"
	"path/filepath"
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

// forbidCommands 返回 shell 命令里要逐条参与禁用匹配的命令文本：外层（命令替换换成占位词）、每个命令替换的内部（递归），
// 以及 eval 的参数拼成的命令（递归）。opaque 为真表示有解析不了的结构：heredoc、没闭合的引号或括号、裸括号、带转义的 $'…'、
// 层数超限等——这些里面可能藏着命令，看不见。sh -c 这类包装由 matchRequest（effectiveSegments）照常看穿。
func forbidCommands(cmd string) (cmds []string, opaque bool) {
	work := 0
	var walk func(c string, depth int)
	walk = func(c string, depth int) {
		work += len(c)
		if depth > maxForbidDepth || work > maxForbidBytes || len(cmds) >= maxForbidCmds {
			opaque = true
			return
		}
		outer, bodies, ok := extractSubstitutions(c)
		if !ok {
			opaque = true
		}
		cmds = append(cmds, outer)
		if _, op, heredoc := scanShell([]rune(outer)); op || heredoc {
			opaque = true
		}
		for _, b := range bodies {
			walk(b, depth+1)
		}
		segs, truncated := effectiveSegmentsLimited(outer)
		if truncated {
			opaque = true
		}
		for _, seg := range segs {
			argv, split := peelWrappers(seg.argv, true)
			if len(argv) == 0 {
				continue
			}
			flags := seg.argvFlags
			if off := len(seg.argv) - len(argv); !split && off >= 0 && off <= len(flags) {
				flags = flags[off:]
			} else if split {
				opaque = true // env -S 拆出来的词没有引号信息，看不出是不是字面量
				flags = nil
			}
			name := filepath.Base(argv[0])
			// eval 与 sh -c 类的命令参数要是完整的字面量才看得见：含变量展开、通配（eval "git ${OP}"、bash -c "$CMD"）时，
			// 运行时才知道是什么命令
			if name == "eval" || isShell(name) {
				for i := 1; i < len(argv); i++ {
					if f := flagAt(flags, i); f.expand || f.glob {
						opaque = true
					}
				}
			}
			if name == "eval" {
				args := argv[1:]
				if len(args) > 0 && args[0] == "--" {
					args = args[1:]
				}
				if len(args) > 0 {
					walk(strings.Join(args, " "), depth+1)
				}
			}
		}
	}
	walk(cmd, 0)
	return cmds, opaque
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

// railseEnvNames 是钩子从环境里读、决定会话角色与放行的变量：RAILSE_ROLE（角色）、RAILSE_TRUST（建会话放行）、RAILSE_HOME
// （钩子连哪个守护进程）。有角色的会话改它们再起一个 CLI，就换到了别的角色、放行或守护进程。
var railseEnvNames = []string{"RAILSE_ROLE", "RAILSE_TRUST", "RAILSE_HOME"}

// envSetters 是能设置或删除环境变量的命令：它们的参数里出现变量名本身（export X、unset X）也算改。
var envSetters = map[string]bool{"export": true, "unset": true, "declare": true, "typeset": true, "readonly": true, "local": true, "env": true}

// changesRailseEnv 判断命令是否给 railseEnvNames 赋值或删除它们：前缀赋值、env、export、declare、typeset、readonly、local、unset、
// env -u，以及 sh -c、命令替换、进程替换与 eval 里的同样写法。设置类命令的参数含变量展开（export "$V"=x）时看不出改的是谁，也算。
// 只看赋值，不看读取（echo $RAILSE_ROLE 不算）。
func changesRailseEnv(cmd string) bool {
	cmds, _ := forbidCommands(cmd)
	for _, c := range cmds {
		for _, seg := range effectiveSegments(c) {
			if segChangesRailseEnv(seg) {
				return true
			}
		}
	}
	return false
}

func segChangesRailseEnv(seg segment) bool {
	// 设置类命令可能是命令本身（export X），也可能是被剥掉的包装（env -u X claude 剥成 claude）：两处都看
	argv := stripWrappers(seg.argv)
	setter := false
	for _, w := range seg.argv[:len(seg.argv)-len(argv)] {
		setter = setter || envSetters[filepath.Base(w)]
	}
	if len(argv) > 0 {
		setter = setter || envSetters[filepath.Base(argv[0])]
	}
	for i, w := range seg.argv {
		for _, n := range railseEnvNames {
			if strings.HasPrefix(w, n+"=") || strings.HasPrefix(w, n+"+=") || strings.HasPrefix(w, "-u"+n) {
				return true
			}
			if setter && w == n {
				return true
			}
		}
		if setter && i > 0 && flagAt(seg.argvFlags, i).expand {
			return true
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
		// 外层按段走、跟踪 cd（与定级同一个 nextCwd）；命令替换、eval 等内层的命令在哪个目录跑不好确定：
		// 外层有切换目录的段时，内层的相对路径按算不出处理
		cmds, _ := forbidCommands(p.Command)
		changes := cdChanges(cmds[0], p.Cwd, ctx)
		for i, c := range cmds {
			if controlWalk(c, p.Cwd, i > 0 && changes, ctx, 0) {
				return true
			}
		}
	}
	return false
}

// controlWalk 按段走命令（跟踪 cd、pushd、popd 改变的工作目录，同定级的 classifyShellTrace），看有没有段读写 Railse 自己的配置；
// sh -c 这类包装按当时的工作目录递归。lost 为真表示工作目录已算不出：之后的相对路径一律算触及（算不出实际目标的不得走放行）。
func controlWalk(command, cwd string, lost bool, ctx Context, depth int) bool {
	for _, text := range splitSegments(command) {
		seg := parseSegment(text)
		if segTouchesControl(seg, cwd, lost, ctx) {
			return true
		}
		if argv := deepArgv(seg); len(argv) > 0 && isShell(filepath.Base(argv[0])) {
			if depth >= maxShellNesting {
				return true // 嵌套过深，看不完，偏严
			}
			if call, ok := parseShellCall(filepath.Base(argv[0]), argv); ok {
				for _, inner := range call.inline() {
					if controlWalk(inner, cwd, lost, ctx, depth+1) {
						return true
					}
				}
			}
		}
		next, ok := nextCwd(seg, cwd, ctx)
		if !ok {
			lost = true
		}
		cwd = next
	}
	return false
}

// cdChanges 报告命令的外层有没有改变工作目录的段（cd、pushd、popd）。
func cdChanges(command, cwd string, ctx Context) bool {
	for _, text := range splitSegments(command) {
		if next, ok := nextCwd(parseSegment(text), cwd, ctx); !ok || next != cwd {
			return true
		}
	}
	return false
}

// segTouchesControl 看一段的参数（不含命令名）、输出与输入重定向：字面提到 .railse 的、解析后在配置目录里的算；
// 工作目录算不出（lost）时相对路径也算。URL 不当路径。
func segTouchesControl(seg segment, cwd string, lost bool, ctx Context) bool {
	for _, w := range seg.argv {
		if mentionsRailseDir(w) {
			return true
		}
	}
	var words []string
	if argv := stripWrappers(seg.argv); len(argv) > 1 {
		words = append(words, argv[1:]...)
	}
	words = append(append(words, redirectPaths(seg.redirects)...), seg.inputs...)
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
