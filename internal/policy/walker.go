package policy

import (
	"path/filepath"
	"slices"
	"strings"
)

// 统一的包装遍历器（收尾轮）：一条命令段按层剥开，每层产出这一层的包装、它带来的工作目录变化、它对环境变量的设置、删除、清空，
// 以及剥开后的内层，直到最内层；sh -c、eval 与命令替换展开出的内层命令接着用同一个遍历器，继承外层累积的工作目录与
// “工作目录已算不清”的状态。定级（classifySegment）、禁用与 deny 匹配（matchRequest、forbidCommands）、配置目录检查
// （touchesControlPlane）、环境变量检查（changesRailseEnv）全部用它，层与层之间的状态不再各剥各的、各丢各的。

// layer 是一条命令段剥开后的一层。wrapper 非空表示这一层是包装（basename），否则是最内层的命令。
type layer struct {
	argv     []string    // 这一层的词：包装层是包装名与它自己的选项，最内层是整条命令
	flags    []wordFlags // 与 argv 一一对应（env -S 拆出来的词没有引号信息，为零值）
	start    int         // argv 在原始 argv 里的起点；env -S 拆出来的为 -1
	wrapper  string
	cwd      string // 这一层运行时的工作目录
	lost     bool   // 工作目录已算不清（cd "$X"、env -C "$D" 之后）
	envSet   []string
	envUnset []string
	envClear bool // env -i、env -、exec -c：清空了环境
	opaque   bool // 带了认不出、或改根目录、写输出文件之类后果算不清的选项
	split    bool // 词来自 env -S 拆串
}

// maxLayers 是一条命令段剥包装的层数上限；超了最后一层按不透明处理。
const maxLayers = 32

// walkLayers 把 argv 一层层剥开。cwd、lost 是这一段开始时的工作目录状态。开头的环境变量赋值记在它们所修饰的那一层的 envSet。
func walkLayers(argv []string, flags []wordFlags, cwd string, lost bool, ctx Context) []layer {
	var out []layer
	start := 0
	split := false
	// flagAtIdx 是当前 argv 从第 i 个词起对应的标志；拆串出来的、或调用方没给标志的，是同样长度的零值
	flagAtIdx := func(i int) []wordFlags {
		if split || start < 0 || start+len(argv) > len(flags) {
			return make([]wordFlags, len(argv)-i)
		}
		return flags[start+i : start+len(argv)]
	}
	for len(out) < maxLayers {
		var sets []string
		for len(argv) > 0 && isAssignWord(argv[0]) {
			k, _, _ := strings.Cut(argv[0], "=")
			sets = append(sets, strings.TrimSuffix(k, "+"))
			argv = argv[1:]
			if !split {
				start++
			}
		}
		st := start
		if split {
			st = -1
		}
		if len(argv) == 0 {
			out = append(out, layer{start: st, cwd: cwd, lost: lost, envSet: sets, split: split})
			return out
		}
		name := filepath.Base(argv[0])
		spec, known := wrapperOptions[name]
		if !known || len(argv) == 1 {
			out = append(out, layer{argv: argv, flags: flagAtIdx(0), start: st, cwd: cwd, lost: lost, envSet: sets, split: split})
			return out
		}
		l := layer{start: st, wrapper: name, cwd: cwd, lost: lost, envSet: sets, split: split}
		rest := argv[1:]
		var chdir []string
		var splitWords []string
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			opt := rest[0]
			rest = rest[1:]
			if opt == "--" {
				break
			}
			if opt == "-" {
				if name == "env" {
					l.envClear = true // env - 等同 env -i
					continue
				}
				l.opaque = true
				continue
			}
			key, val, hasVal := strings.Cut(opt, "=")
			takes := slices.Contains(spec.args, key)
			if !takes && len(key) > 2 && key[1] != '-' && slices.Contains(spec.args, key[:2]) {
				key, val, hasVal, takes = key[:2], key[2:], true, true // -uNAME、-n5 这类连写
			}
			switch {
			case takes:
				if !hasVal && len(rest) > 0 {
					val, rest = rest[0], rest[1:]
				}
			case slices.Contains(spec.flags, key) && !hasVal:
			case spec.numeric && len(key) > 1 && strings.Trim(key[1:], "0123456789") == "":
			default:
				l.opaque = true // 认不出的选项：不知道它对内层做了什么
				continue
			}
			if slices.Contains(spec.opaque, key) {
				l.opaque = true
			}
			if slices.Contains(spec.chdir, key) {
				chdir = append(chdir, val)
			}
			switch {
			case name == "env" && (key == "-u" || key == "--unset"):
				l.envUnset = append(l.envUnset, val)
			case name == "env" && (key == "-i" || key == "--ignore-environment"):
				l.envClear = true
			case name == "exec" && key == "-c":
				l.envClear = true
			case name == "env" && (key == "-S" || key == "--split-string"):
				splitWords = parseSegment(val).argv
			}
		}
		if spec.durationArg && len(rest) > 0 {
			rest = rest[1:] // 时长
		}
		consumed := len(argv) - len(rest)
		l.argv, l.flags = argv[:consumed], flagAtIdx(0)[:consumed]
		out = append(out, l)
		for _, d := range chdir {
			if d == "" || unresolvable(d, ctx.HomeDir) {
				lost = true
				continue
			}
			cwd = resolvePath(d, cwd, ctx.HomeDir)
		}
		if splitWords != nil {
			rest = append(append([]string(nil), splitWords...), rest...)
			split = true
		}
		if !split {
			start += consumed
		}
		if len(rest) == 0 {
			return out // 只有包装自己（env、env -u X 打印环境）：包装层就是最后一层
		}
		argv = rest
	}
	if len(out) > 0 {
		out[len(out)-1].opaque = true
	}
	return out
}

// isAssignWord 认命令前的赋值：NAME=value 与追加的 NAME+=value。
func isAssignWord(w string) bool {
	if isEnvAssignment(w) {
		return true
	}
	k, v, ok := strings.Cut(w, "+=")
	return ok && isEnvAssignment(k+"="+v)
}

// innermost 是最后一层（最内层的命令，或只有包装自己时的包装层）。
func innermost(ls []layer) layer { return ls[len(ls)-1] }

// stripWrappers 去掉开头的环境变量赋值和不影响定级的包装（env、time、nohup、nice、caffeinate，按 basename 认），连同它们的选项；
// 遇到自有档位的包装（sudo、xargs、timeout、command、exec 等，见 deepWrappers）或最内层命令就停，返回原始 argv 的后缀
// （commandWords 靠这一点对齐标志）。只剩一个 token 时不再剥。包装的副作用与更里面的层见 walkLayers。
func stripWrappers(argv []string) []string {
	if len(argv) <= 1 {
		return argv
	}
	for _, l := range walkLayers(argv, nil, "", false, Context{}) {
		if l.start < 0 {
			break // env -S 拆出来的词不是原始 argv 的后缀：停在拆串之前
		}
		if l.wrapper == "" || deepWrappers[l.wrapper] {
			if l.start >= len(argv) {
				return argv[len(argv)-1:]
			}
			return argv[l.start:]
		}
	}
	// 拆串或层数超限：退回只剥开头的赋值
	i := 0
	for i < len(argv)-1 && isEnvAssignment(argv[i]) {
		i++
	}
	return argv[i:]
}

// wrapperRisk 是包装层自身的风险：sudo、doas 是 destructive，其余自有档位的包装（xargs、timeout、command、exec）按命令表
// （不在表里的是 unknown），不影响定级的包装（env、nice 等）是 read；带了算不清后果的选项的至少 unknown。
func wrapperRisk(l layer, ctx Context) Risk {
	r := RiskRead
	switch {
	case l.wrapper == "sudo" || l.wrapper == "doas":
		r = RiskDestructive
	case deepWrappers[l.wrapper]:
		r = classifyArgv([]string{l.wrapper}, []wordFlags{{}}, l.cwd, ctx)
	}
	if l.opaque || l.split {
		r = maxRisk(r, RiskUnknown)
	}
	return r
}

// view 是 shellViews 看到的一条命令段：它的各层，以及这一段开始时（重定向由外层 shell 在这里做）的工作目录状态。
type view struct {
	seg    segment
	layers []layer
	cwd    string
	lost   bool
}

// shellViews 把整条 shell 命令展开成逐段、逐层的视图：外层（命令替换换成占位词）按段走、跟踪 cd；sh -c 的内联命令与 eval 的参数
// 按最内层当时的工作目录状态递归；命令替换、进程替换的内部按外层开始时的目录递归，外层改过目录时按算不清处理。
// texts 是参与禁用匹配的各条命令文本（外层、各层内联与替换的内部）。opaque 报告有解析不了、可能藏着命令的结构：heredoc、
// 没闭合的引号或括号、裸括号、带转义的 $'…'、eval 与 sh -c 的参数含变量或通配、env -S 拆串、包装带了算不清的选项、
// 层数或工作量超限。
func shellViews(command, cwd string, ctx Context) (views []view, texts []string, opaque bool) {
	work := 0
	var walk func(c, cwd string, lost bool, depth int)
	walk = func(c, cwd string, lost bool, depth int) {
		work += len(c)
		if depth > maxForbidDepth || work > maxForbidBytes || len(texts) >= maxForbidCmds {
			opaque = true
			return
		}
		outer, bodies, ok := extractSubstitutions(c)
		if !ok {
			opaque = true
		}
		texts = append(texts, outer)
		if _, op, heredoc := scanShell([]rune(outer)); op || heredoc {
			opaque = true
		}
		cur, curLost, changed := cwd, lost, false
		for _, text := range splitSegments(outer) {
			seg := parseSegment(text)
			ls := walkLayers(seg.argv, seg.argvFlags, cur, curLost, ctx)
			views = append(views, view{seg: seg, layers: ls, cwd: cur, lost: curLost})
			for _, l := range ls {
				if l.opaque || l.split {
					opaque = true
				}
			}
			in := innermost(ls)
			if len(in.argv) > 0 {
				name := filepath.Base(in.argv[0])
				if name == "eval" || isShell(name) {
					for i := 1; i < len(in.argv); i++ {
						if f := flagAt(in.flags, i); f.expand || f.glob {
							opaque = true // 运行时才知道是什么命令
						}
					}
				}
				if isShell(name) {
					if depth >= maxShellNesting {
						opaque = true
					} else if call, ok := parseShellCall(name, in.argv); ok {
						for _, inline := range call.inline() {
							walk(inline, in.cwd, in.lost, depth+1)
						}
					}
				}
				if name == "eval" {
					args := in.argv[1:]
					if len(args) > 0 && args[0] == "--" {
						args = args[1:]
					}
					if len(args) > 0 {
						walk(strings.Join(args, " "), in.cwd, in.lost, depth+1)
					}
				}
			}
			next, ok := nextCwd(seg, cur, ctx)
			if !ok {
				curLost = true
			}
			if next != cur || !ok {
				changed = true
			}
			cur = next
		}
		for _, b := range bodies {
			walk(b, cwd, lost || changed, depth+1)
		}
	}
	walk(command, cwd, false, 0)
	return views, texts, opaque
}

// IsWrapperCommand 报告进程名是不是包装表里的启动器（env、timeout、nice、nohup、command、exec、sudo 等，按 basename 认）。
// 守护进程在祖先链里认会话的 CLI 时跳过它们：钩子经 timeout 10 railse hook … 起来时，最近的非 shell 进程是短命的启动器。
func IsWrapperCommand(name string) bool {
	_, ok := wrapperOptions[filepath.Base(name)]
	return ok
}
