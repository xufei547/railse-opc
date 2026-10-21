package policy

import (
	"path/filepath"
	"strings"
)

// classifyShell 给一条 shell 命令定级：拆段后逐段分类，取最高档。空命令为 unknown。
// 跟踪 cd、pushd、popd 改变的工作目录，后面各段的相对路径按新目录解析；
// 工作目录无法静态确定之后，后面每一段至少为 unknown。
func classifyShell(command, cwd string, ctx Context) Risk {
	segs := splitSegments(command)
	if len(segs) == 0 {
		return RiskUnknown
	}
	risk := RiskRead
	lost := false // 工作目录已无法静态确定
	for _, text := range segs {
		seg := parseSegment(text)
		r := classifySegment(seg, cwd, ctx)
		if lost {
			r = maxRisk(r, RiskUnknown)
		}
		risk = maxRisk(risk, r)
		next, ok := nextCwd(seg, cwd, ctx)
		if !ok {
			lost = true
		}
		cwd = next
	}
	return risk
}

func classifySegment(seg segment, cwd string, ctx Context) Risk {
	argv, flags := commandWords(seg)
	risk := RiskUnknown // 没有命令、只有重定向时也要看重定向与输入
	if len(argv) > 0 {
		risk = classifyArgv(argv, cwd, ctx)
		// 参数无法静态解析时，不论是否命中命令表，至少为 unknown
		if hasUnresolvable(argv[1:], flags[1:], ctx) {
			risk = maxRisk(risk, RiskUnknown)
		}
		if touchesSensitiveWords(argv[1:], flags[1:], cwd, ctx) {
			risk = maxRisk(risk, RiskDestructive)
		}
	}
	if hasUnresolvable(seg.inputs, seg.inputFlags, ctx) {
		risk = maxRisk(risk, RiskUnknown)
	}
	// 输入重定向读敏感文件，与参数里出现敏感路径一样按 destructive
	if touchesSensitiveWords(seg.inputs, seg.inputFlags, cwd, ctx) {
		risk = maxRisk(risk, RiskDestructive)
	}
	if seg.subshell {
		risk = maxRisk(risk, RiskUnknown)
	}
	// 输出重定向按目标路径定级，/dev/null 这类无害伪设备不算写
	risk = maxRisk(risk, pathsRisk(redirectPaths(seg.redirects), cwd, ctx))
	if hasSQLDrop(seg.text) {
		risk = maxRisk(risk, RiskDestructive)
	}
	return risk
}

// redirectPaths 把以 - 或 & 开头的重定向目标改写成 ./ 开头。重定向目标总是路径，
// 不能让 pathsRisk 当成选项或描述符跳过。
func redirectPaths(targets []string) []string {
	out := make([]string, len(targets))
	for i, t := range targets {
		if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "&") {
			t = "./" + t
		}
		out[i] = t
	}
	return out
}

// commandWords 返回去掉包装后的 argv 与对应的标志。stripWrappers 返回的总是 argv 的后缀。
func commandWords(seg segment) ([]string, []wordFlags) {
	argv := stripWrappers(seg.argv)
	return argv, seg.argvFlags[len(seg.argv)-len(argv):]
}

// classifyArgv 按命令表给单条命令定级。shell 由 shellRisk 处理，-c 的命令串递归分析。
// 参数的可解析性与敏感路径由 classifySegment 检查。
func classifyArgv(argv []string, cwd string, ctx Context) Risk {
	name := filepath.Base(argv[0])
	if isShell(name) {
		return shellRisk(argv, cwd, ctx)
	}
	for _, p := range commandTable {
		if !p.matches(name, argv) {
			continue
		}
		r := p.risk
		if p.interp {
			file, ok := scriptOperand(argv)
			if !ok {
				return RiskUnknown
			}
			r = maxRisk(r, pathsRisk([]string{file}, cwd, ctx))
		}
		if p.paths {
			r = maxRisk(r, pathsRisk(argv[1:], cwd, ctx))
		}
		return r
	}
	return RiskUnknown
}

// shellRisk 给 sh、bash 这类 shell 的调用定级。选项里有 -c 或含 c 的单横线选项簇（-lc、-xc、-ec）时，
// 其后第一个非选项参数是命令串，递归分析；含 s 的选项簇或没有非选项参数时从 stdin 读命令，为 unknown；
// 否则第一个非选项参数是脚本路径，取项目内改动与脚本路径定级的较大值。
// 选项簇里每个 o、O（set -o、shopt -O）吃掉后面一个参数，如 bash -euo pipefail -c '…'；
// --rcfile、--init-file 也吃掉后面一个参数。
func shellRisk(argv []string, cwd string, ctx Context) Risk {
	command, stdin := false, false
	i := 1
	for ; i < len(argv); i++ {
		a := argv[i]
		if a == "--" || a == "-" {
			i++
			break
		}
		if len(a) < 2 || (a[0] != '-' && a[0] != '+') {
			break
		}
		if strings.HasPrefix(a, "--") {
			if a == "--rcfile" || a == "--init-file" {
				i++
			}
			continue
		}
		if a[0] == '-' {
			command = command || strings.ContainsRune(a, 'c')
			stdin = stdin || strings.ContainsRune(a, 's')
		}
		i += strings.Count(a, "o") + strings.Count(a, "O")
	}
	switch {
	case i >= len(argv), stdin && !command:
		return RiskUnknown
	case command:
		return classifyShell(argv[i], cwd, ctx)
	default:
		return maxRisk(RiskWriteInProject, pathsRisk(argv[i:i+1], cwd, ctx))
	}
}

// inlineCodeChars 是解释器单横线选项簇里表示内联或预加载代码的字符，取各解释器的并集，宁可多判 unknown：
// python -c，node -e、-p、-r，ruby -e、-r，perl -e、-E，php -r。
const inlineCodeChars = "ceprE"

// scriptOperand 找出解释器要运行的文件：选项之后的第一个参数，-- 之后的第一个参数也算。
// 遇到内联代码选项（含 inlineCodeChars 的单横线选项簇、--eval、--print）、- 或没有文件时返回 false。
func scriptOperand(argv []string) (string, bool) {
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "-":
			return "", false
		case a == "--":
			if i+1 < len(argv) {
				return argv[i+1], true
			}
			return "", false
		case strings.HasPrefix(a, "--"):
			if name, _, _ := strings.Cut(a, "="); name == "--eval" || name == "--print" {
				return "", false
			}
		case strings.HasPrefix(a, "-"):
			if strings.ContainsAny(a[1:], inlineCodeChars) {
				return "", false
			}
		default:
			return a, true
		}
	}
	return "", false
}

// nextCwd 返回这一段执行之后的工作目录：cd、pushd 到可解析的目录时返回新目录，其余命令原样返回。
// 目录无法静态确定时 ok 为 false：目标无法解析、cd -、家目录未知时的 cd、popd、
// pushd 不带目录或带选项（交换、轮转目录栈或 -n 不切换），以及无法分析的段里的 cd。
func nextCwd(seg segment, cwd string, ctx Context) (string, bool) {
	argv, flags := commandWords(seg)
	if len(argv) == 0 {
		return cwd, true
	}
	switch argv[0] {
	case "cd", "pushd":
	case "popd":
		return cwd, false
	default:
		return cwd, true
	}
	if seg.subshell {
		return cwd, false
	}
	args, argFlags := argv[1:], flags[1:]
	if argv[0] == "pushd" {
		if len(args) != 1 || strings.HasPrefix(args[0], "-") || strings.HasPrefix(args[0], "+") {
			return cwd, false
		}
	}
	// 跳过 cd 的 -L、-P、-e、-@ 选项
	for len(args) > 0 && len(args[0]) > 1 && args[0][0] == '-' {
		done := args[0] == "--"
		args, argFlags = args[1:], argFlags[1:]
		if done {
			break
		}
	}
	if len(args) == 0 {
		if ctx.HomeDir == "" {
			return cwd, false
		}
		return ctx.HomeDir, true
	}
	if args[0] == "-" || wordUnresolvable(args[0], argFlags[0], ctx) {
		return cwd, false
	}
	return resolvePath(args[0], cwd, ctx.HomeDir), true
}

// stripWrappers 去掉开头的环境变量赋值和 env、time、nohup、nice、caffeinate 这类包装。
// 只剩一个 token 时不再剥，避免把 "env" 本身剥成空。
func stripWrappers(argv []string) []string {
	for len(argv) > 1 {
		a := argv[0]
		switch {
		case isEnvAssignment(a):
			argv = argv[1:]
		case a == "env" || a == "time" || a == "nohup" || a == "nice" || a == "caffeinate":
			argv = argv[1:]
		default:
			return argv
		}
	}
	return argv
}

func isEnvAssignment(s string) bool {
	k, _, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return false
	}
	for i, c := range k {
		if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func isShell(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash", "fish":
		return true
	}
	return false
}

// wordUnresolvable 判断一个参数值能否在不读环境的前提下解析成路径，f 是扫描器给这个词记的标志。
// 以下情况无法解析：引号外的 glob；展开开头的 $HOME、${HOME} 后仍有引号外的 $；
// 以引号外的 ~ 开头却不是 ~ 或 ~/…（如 ~bob）；家目录未知时以 ~ 或 $HOME 开头。
// 引号里的 glob 元字符与 $ 是字面量，不影响解析。
func wordUnresolvable(w string, f wordFlags, ctx Context) bool {
	if f.glob {
		return true
	}
	rest, home := splitHome(w)
	tildeHome := home && strings.HasPrefix(w, "~")
	varHome := home && !tildeHome
	if f.expand {
		s := w
		if varHome {
			s = rest
		}
		if strings.Contains(s, "$") {
			return true
		}
	}
	if f.tilde && !tildeHome {
		return true
	}
	return (f.tilde || varHome) && ctx.HomeDir == ""
}

// flagAt 返回第 i 个词的标志，没有标志（调用方不知道引号信息）时为零值。
func flagAt(flags []wordFlags, i int) wordFlags {
	if i < len(flags) {
		return flags[i]
	}
	return wordFlags{}
}

// touchesSensitive 判断参数里是否有敏感路径，读敏感文件和写一样按 destructive。
// 供没有引号信息的调用方使用，所有标志按零值处理。
func touchesSensitive(args []string, cwd string, ctx Context) bool {
	return touchesSensitiveWords(args, nil, cwd, ctx)
}

// touchesSensitiveWords 同 touchesSensitive，flags 与 args 一一对应。--name=value 形式的选项按 value 判断；
// 无法静态解析的参数不在这里判断，由 hasUnresolvable 兜底。
func touchesSensitiveWords(args []string, flags []wordFlags, cwd string, ctx Context) bool {
	for i, a := range args {
		p, ok := pathOperand(a)
		if !ok || wordUnresolvable(p, flagAt(flags, i), ctx) {
			continue
		}
		if isSensitive(resolvePath(p, cwd, ctx.HomeDir), ctx.HomeDir) {
			return true
		}
	}
	return false
}

// hasUnresolvable 判断参数里是否有不读环境就无法解析的路径，flags 与 args 一一对应。
// --name=value 形式的选项按 value 判断。
func hasUnresolvable(args []string, flags []wordFlags, ctx Context) bool {
	for i, a := range args {
		if p, ok := pathOperand(a); ok && wordUnresolvable(p, flagAt(flags, i), ctx) {
			return true
		}
	}
	return false
}

func hasSQLDrop(text string) bool {
	t := strings.ToLower(text)
	for _, kw := range []string{"drop table", "drop database", "drop schema", "truncate table"} {
		if strings.Contains(t, kw) {
			return true
		}
	}
	return false
}
