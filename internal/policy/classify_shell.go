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
		risk = classifyArgv(argv, flags, cwd, ctx)
		args, argFlags := withoutMessages(argv[1:], flags[1:])
		// 参数无法静态解析时，不论是否命中命令表，至少为 unknown
		if hasUnresolvable(args, argFlags, ctx) {
			risk = maxRisk(risk, RiskUnknown)
		}
		if touchesSensitiveWords(args, argFlags, cwd, ctx) {
			risk = maxRisk(risk, RiskDestructive)
		}
	}
	// 命令前的 VAR=value（含 env 之后的）设置了能注入代码的环境变量
	if assignsInjectedEnv(seg.argv[:len(seg.argv)-len(argv)]) {
		risk = maxRisk(risk, RiskUnknown)
	}
	if hasUnresolvable(seg.inputs, seg.inputFlags, ctx) {
		risk = maxRisk(risk, RiskUnknown)
	}
	// 输入重定向读敏感文件，与参数里出现敏感路径一样按 destructive
	if touchesSensitiveWords(seg.inputs, seg.inputFlags, cwd, ctx) {
		risk = maxRisk(risk, RiskDestructive)
	}
	// bash 的 /dev/tcp、/dev/udp：读是联网，写是往外发
	if onNetDevice(seg.inputs, seg.inputFlags, cwd, ctx) {
		risk = maxRisk(risk, RiskNetwork)
	}
	if onNetDevice(seg.redirects, seg.redirectFlags, cwd, ctx) {
		risk = maxRisk(risk, RiskOutward)
	}
	if seg.subshell {
		risk = maxRisk(risk, RiskUnknown)
	}
	// 输出重定向按目标路径定级，/dev/null 这类无害伪设备不算写
	risk = maxRisk(risk, pathsRiskWords(redirectPaths(seg.redirects), seg.redirectFlags, cwd, ctx))
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

// classifyArgv 按命令表给单条命令定级。shell 由 shellRisk 处理；有全局选项表的命令先剥掉子命令前的全局选项。
// 参数的可解析性与敏感路径由 classifySegment 检查。
func classifyArgv(argv []string, flags []wordFlags, cwd string, ctx Context) Risk {
	name := filepath.Base(argv[0])
	if isShell(name) {
		return shellRisk(name, argv, flags, cwd, ctx)
	}
	floor, dirRisk := RiskRead, RiskRead
	if spec, ok := leadingOptions[name]; ok {
		argv, flags, floor, dirRisk = stripLeading(argv, flags, spec, cwd, ctx)
	}
	risk := RiskUnknown
	for _, p := range commandTable {
		if !p.matches(name, argv) {
			continue
		}
		risk = p.risk
		if p.interp {
			risk = interpRisk(name, argv, flags, cwd, ctx)
		}
		if p.paths {
			risk = maxRisk(risk, pathsRiskWords(argv[1:], flags[1:], cwd, ctx))
		}
		break
	}
	// 在别的目录执行（git -C 等）：会写的命令按目录定级；只读命令只看目录是否无法解析或敏感
	if risk > RiskRead || dirRisk == RiskUnknown || dirRisk == RiskDestructive {
		risk = maxRisk(risk, dirRisk)
	}
	return maxRisk(risk, floor)
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

// withoutMessages 去掉含空白的参数及其标志：这样的参数是提交说明之类的消息，不当路径检查。
func withoutMessages(args []string, flags []wordFlags) ([]string, []wordFlags) {
	var outArgs []string
	var outFlags []wordFlags
	for i, a := range args {
		if strings.ContainsAny(a, " \t\n") {
			continue
		}
		outArgs = append(outArgs, a)
		outFlags = append(outFlags, flagAt(flags, i))
	}
	return outArgs, outFlags
}

// pathsRiskWords 同 pathsRisk，按扫描器的标志判断可解析性：引号里的 glob 元字符与 $ 是字面量。
func pathsRiskWords(paths []string, flags []wordFlags, cwd string, ctx Context) Risk {
	return pathsRiskFunc(paths, cwd, ctx, func(i int, p string) bool { return wordUnresolvable(p, flagAt(flags, i), ctx) })
}

// onNetDevice 判断重定向目标里是否有 bash 的网络伪设备 /dev/tcp/…、/dev/udp/…。
func onNetDevice(targets []string, flags []wordFlags, cwd string, ctx Context) bool {
	for i, t := range targets {
		if wordUnresolvable(t, flagAt(flags, i), ctx) {
			continue
		}
		abs := resolvePath(t, cwd, ctx.HomeDir)
		if strings.HasPrefix(abs, "/dev/tcp/") || strings.HasPrefix(abs, "/dev/udp/") {
			return true
		}
	}
	return false
}

// touchesSensitive 判断参数里是否有敏感路径，读敏感文件和写一样按 destructive。含空白的参数是消息，不检查。
// 供没有引号信息的调用方使用，所有标志按零值处理。
func touchesSensitive(args []string, cwd string, ctx Context) bool {
	args, flags := withoutMessages(args, nil)
	return touchesSensitiveWords(args, flags, cwd, ctx)
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
