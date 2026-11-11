package policy

import (
	"path/filepath"
	"slices"
	"strings"
)

// classifyShell 给一条 shell 命令定级，见 classifyShellTrace。
func classifyShell(command, cwd string, ctx Context) Risk {
	r, _ := classifyShellTrace(command, cwd, ctx)
	return r
}

// classifyShellTrace 给一条 shell 命令定级并记下触发点：拆段后逐段分类，取最高档；第一个把总档位抬到最终值的段就是触发段，
// 它的性质由 explainSegment 按这一段的内容说。空命令为 unknown（没有段，触发点为空段的 WhyUnknown）。定为 read、
// write_in_project 时没有触发点。跟踪 cd、pushd、popd 改变的工作目录，后面各段的相对路径按新目录解析；
// 工作目录无法静态确定之后，后面每一段至少为 unknown。
func classifyShellTrace(command, cwd string, ctx Context) (Risk, *Trigger) {
	segs := splitSegments(command)
	if len(segs) == 0 {
		return RiskUnknown, &Trigger{Why: WhyUnknown}
	}
	risk := RiskRead
	var trig *Trigger
	lost := false // 工作目录已无法静态确定
	piped := pipedSegments(command)
	for i, text := range segs {
		seg := parseSegment(text)
		r := classifySegment(seg, cwd, ctx)
		pipedScript := piped[text] && readsScriptFromStdin(seg)
		subst := runsSubstitution(seg)
		if pipedScript || subst {
			r = RiskDestructive
		}
		if lost {
			r = maxRisk(r, RiskUnknown)
		}
		if r > risk {
			risk = r
			trig = &Trigger{Segment: compactSegment(text), Why: explainSegment(seg, r, pipedScript, subst, downloadBefore(segs, i, piped), cwd, ctx)}
		}
		next, ok := nextCwd(seg, cwd, ctx)
		if !ok {
			lost = true
		}
		cwd = next
	}
	if risk <= RiskWriteInProject {
		return risk, nil
	}
	return risk, trig
}

// downloadBefore 报告第 i 段在管道里，且同一条管道往前有 curl 或 wget：curl … | sh 是"下载脚本并直接执行"。
func downloadBefore(segs []string, i int, piped map[string]bool) bool {
	for j := i - 1; j >= 0 && piped[segs[j+1]]; j-- {
		argv := stripWrappers(parseSegment(segs[j]).argv)
		if len(argv) > 0 {
			if n := filepath.Base(argv[0]); n == "curl" || n == "wget" {
				return true
			}
		}
	}
	return false
}

// deployNames 是 explainSegment 里说成"发布或部署到外部"的命令（与网页 explain.ts 的 deploy 列表一致）。
var deployNames = map[string]bool{"vercel": true, "fly": true, "flyctl": true, "kubectl": true, "terraform": true, "gh": true,
	"npm": true, "pnpm": true, "yarn": true, "bun": true, "cargo": true}

// explainSegment 用一句固定短语说这一段为什么定到 risk。按具体到笼统的顺序：管道脚本、命令替换、SQL、sudo、kill、
// 删除、敏感路径、git push、往外发、部署，再按档位兜底。只说性质，不复述命令（Segment 另给）。
func explainSegment(seg segment, risk Risk, pipedScript, subst, download bool, cwd string, ctx Context) string {
	argv, flags := commandWords(seg)
	name := ""
	if len(argv) > 0 {
		name = filepath.Base(argv[0])
	}
	words := map[string]bool{}
	for _, a := range argv {
		words[filepath.Base(a)] = true
	}
	destructive := risk == RiskDestructive
	switch {
	case pipedScript && download:
		return WhyDownloadRun
	case pipedScript, subst:
		return WhyPipedScript
	case hasSQLDrop(seg.text):
		return WhySQLDrop
	// 只认这一段本身是 sudo、doas：ssh 远端命令里的 sudo 分类器不看（只按联网定级），那时抬到 destructive 的另有原因（如 -i 的密钥文件）
	case destructive && (name == "sudo" || name == "doas"):
		return WhySudo
	case destructive && (name == "kill" || name == "pkill" || name == "killall"):
		return WhyKill
	case destructive && (name == "rm" || name == "rmdir" || name == "shred" || name == "unlink" ||
		(name == "git" && gitSubcommand(argv, flags, cwd, ctx) == "clean") || (name == "find" && words["-delete"])):
		return WhyDelete
	case destructive && sensitiveSegment(seg, argv, flags, cwd, ctx):
		return WhySensitive
	case name == "git" && gitSubcommand(argv, flags, cwd, ctx) == "push":
		if destructive {
			return WhyForcePush
		}
		return WhyPush
	case risk == RiskOutward && (name == "curl" || name == "wget"):
		return WhySendData
	case risk == RiskOutward && deployNames[name]:
		return WhyDeploy
	}
	switch risk {
	case RiskDestructive:
		return WhyDestructive
	case RiskOutward:
		return WhyOutward
	case RiskNetwork:
		return WhyNetwork
	case RiskWriteOutside:
		return WhyWriteOutside
	default:
		return WhyUnknown
	}
}

// sensitiveSegment 报告这一段的参数、输入重定向或输出重定向里有敏感路径。
func sensitiveSegment(seg segment, argv []string, flags []wordFlags, cwd string, ctx Context) bool {
	if len(argv) > 0 {
		args, argFlags := withoutMessages(argv[1:], flags[1:])
		if touchesSensitiveWords(args, argFlags, cwd, ctx) {
			return true
		}
	}
	return touchesSensitiveWords(seg.inputs, seg.inputFlags, cwd, ctx) ||
		touchesSensitiveWords(redirectPaths(seg.redirects), seg.redirectFlags, cwd, ctx)
}

// gitSubcommand 返回 git 的子命令：先剥掉子命令前的全局选项（git -C dir push 也认得出），没有子命令为空。
func gitSubcommand(argv []string, flags []wordFlags, cwd string, ctx Context) string {
	if spec, ok := leadingOptions["git"]; ok {
		argv, _, _, _ = stripLeading(argv, flags, spec, cwd, ctx)
	}
	if len(argv) < 2 {
		return ""
	}
	return argv[1]
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
	// 工作目录（跟踪 cd 之后，或载荷的 Cwd）在项目外时，按项目内改动定级的段一律按项目外：
	// 不做路径分析的写命令（git init、git commit、make）改的就是工作目录。项目根为空时没有目录在项目内
	if risk == RiskWriteInProject && !isWithin(cwd, ctx.ProjectRoot) {
		risk = RiskWriteOutside
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
	orig, origFlags := argv, flags
	if spec, ok := leadingOptions[name]; ok {
		argv, flags, floor, dirRisk = stripLeading(argv, flags, spec, cwd, ctx)
	}
	risk := tableRisk(name, argv, flags, cwd, ctx)
	// 包管理器子命令别名：两种写法都定级取较大值
	if alt, ok := canonicalSubcommand(name, argv); ok {
		risk = maxRisk(risk, tableRisk(name, alt, flags, cwd, ctx))
	}
	if f, ok := unknownIf[name]; ok && f(argv[1:]) {
		risk = maxRisk(risk, RiskUnknown)
	}
	if spec, ok := outputOptions[name]; ok {
		risk = maxRisk(risk, outputRisk(argv, flags, spec, cwd, ctx))
	}
	risk = maxRisk(risk, installRisk(name, orig, argv, origFlags, flags, cwd, ctx))
	// 在别的目录执行（git -C 等）：会写的命令按目录定级；只读命令只看目录是否无法解析或敏感
	if risk > RiskRead || dirRisk == RiskUnknown || dirRisk == RiskDestructive {
		risk = maxRisk(risk, dirRisk)
	}
	return maxRisk(risk, floor)
}

// tableRisk 按命令表第一条命中的行定级，没有命中为 unknown。
func tableRisk(name string, argv []string, flags []wordFlags, cwd string, ctx Context) Risk {
	p, ok := matchedRow(name, argv)
	if !ok {
		return RiskUnknown
	}
	risk := p.risk
	if p.interp {
		risk = interpRisk(name, argv, flags, cwd, ctx)
	}
	if p.paths {
		risk = maxRisk(risk, pathsRiskWords(argv[1:], flags[1:], cwd, ctx))
	}
	return risk
}

// matchedRow 返回命令表里第一条命中的行。
func matchedRow(name string, argv []string) (cmdPattern, bool) {
	for _, p := range commandTable {
		if p.matches(name, argv) {
			return p, true
		}
	}
	return cmdPattern{}, false
}

// hasPathsRow 判断命令表里是否有这个命令带路径分析的行。
func hasPathsRow(name string) bool {
	for _, p := range commandTable {
		if p.name == name && p.paths {
			return true
		}
	}
	return false
}

// shellWriteTargets 返回 shell 命令会写入的路径（已解析成绝对路径）：输出重定向的目标，
// curl、wget、git 等的输出文件与联网安装的目录，以及命令表里带路径分析的行的操作数（命中行不带路径分析、
// 但同名命令有这类行且命中行会写时也算，如 rm -rf）。此外，定级至少 write_in_project 的段按隐式写入处理：
// 它的工作目录与全部操作数、选项值（经 pathOperand）都算写入目标（go build -o、git init、git -C dir …）。
// 跟踪 cd，看穿 sh -c；无法解析的目标跳过。供 deny 与 ask 规则的 path_glob 匹配 shell 请求用，宁多勿漏。
func shellWriteTargets(command, cwd string, ctx Context) []string {
	var out []string
	lost := false
	for _, text := range splitSegments(command) {
		seg := parseSegment(text)
		risk := classifySegment(seg, cwd, ctx)
		if lost {
			risk = maxRisk(risk, RiskUnknown)
		}
		out = append(out, segmentWriteTargets(seg, cwd, lost, risk >= RiskWriteInProject, ctx)...)
		argv := stripWrappers(seg.argv)
		if len(argv) > 0 && isShell(filepath.Base(argv[0])) {
			if call, ok := parseShellCall(filepath.Base(argv[0]), argv); ok && !lost {
				for _, inner := range call.inline() {
					out = append(out, shellWriteTargets(inner, cwd, ctx)...)
				}
			}
		}
		next, ok := nextCwd(seg, cwd, ctx)
		if !ok {
			lost = true
		}
		cwd = next
	}
	return out
}

// segmentWriteTargets 返回单段的写入目标，见 shellWriteTargets。lost 为 true 时工作目录已无法确定，相对路径跳过；
// implicit 为 true 时再加上工作目录与全部操作数、选项值。
func segmentWriteTargets(seg segment, cwd string, lost, implicit bool, ctx Context) []string {
	raw := redirectPaths(seg.redirects)
	rflags := append([]wordFlags(nil), seg.redirectFlags...)
	add := func(ts []string, fs []wordFlags) {
		raw = append(raw, ts...)
		for i := range ts {
			rflags = append(rflags, flagAt(fs, i))
		}
	}
	argv, flags := commandWords(seg)
	if implicit {
		add([]string{"."}, nil)
		for i, a := range argv {
			if p, ok := pathOperand(a); ok && i > 0 {
				add([]string{p}, []wordFlags{flagAt(flags, i)})
			}
		}
	}
	if len(argv) > 0 {
		name := filepath.Base(argv[0])
		orig, origFlags := argv, flags
		if spec, ok := leadingOptions[name]; ok {
			argv, flags, _, _ = stripLeading(argv, flags, spec, cwd, ctx)
		}
		if spec, ok := outputOptions[name]; ok {
			add(outputTargets(argv, flags, spec))
		}
		add(installTargets(name, orig, argv, origFlags, flags))
		if row, ok := matchedRow(name, argv); ok && (row.paths || row.risk >= RiskWriteInProject && hasPathsRow(name)) {
			for i, a := range argv[1:] {
				if p, ok := pathOperand(a); ok {
					add([]string{p}, []wordFlags{flagAt(flags, i+1)})
				}
			}
		}
	}
	var out []string
	for i, p := range raw {
		f := flagAt(rflags, i)
		if p == "" || p == "-" || wordUnresolvable(p, f, ctx) {
			continue
		}
		if lost && !filepath.IsAbs(expandHome(p, ctx.HomeDir)) {
			continue
		}
		out = append(out, resolvePath(p, cwd, ctx.HomeDir))
	}
	return out
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

// stripWrappers 去掉开头的环境变量赋值和 env、time、nohup、nice、caffeinate 这类包装（按 basename 认，/usr/bin/env 等同 env），
// 连同它们的选项（env 的 -i、-u 名字、--unset=名字、-C 目录、-S 串等，nice 的 -n 数，time 的 -p）。
// 只剩一个 token 时不再剥，避免把 "env" 本身剥成空。总是返回 argv 的后缀（commandWords 靠这一点对齐标志）；
// env -S 的串要拆成新的词，不是后缀，由 peelWrappers 处理。
func stripWrappers(argv []string) []string {
	out, _ := peelWrappers(argv, false)
	return out
}

// deepWrappers 是只在“看穿”时剥掉的包装：定级时它们自有档位（sudo、doas 按 destructive，xargs 不在名单里按 unknown），
// 不能剥；禁用清单、改 Railse 设置、sh -c 识别与配置目录检查要看它们里面跑的是什么。
var deepWrappers = map[string]bool{"sudo": true, "doas": true, "xargs": true, "timeout": true, "command": true, "exec": true}

// wrapperArgOptions 是各包装带一个参数的选项（参数是下一个词，或以 = 连写）：剥选项时连参数一起跳过。
var wrapperArgOptions = map[string][]string{
	"env":     {"-u", "--unset", "-C", "--chdir", "-S", "--split-string"},
	"nice":    {"-n", "--adjustment"},
	"time":    {"-f", "--format", "-o", "--output"},
	"sudo":    {"-u", "--user", "-g", "--group", "-C", "--close-from", "-h", "--host", "-p", "--prompt", "-r", "--role", "-t", "--type", "-U", "--other-user", "-D", "--chdir", "-R", "--chroot", "-T", "--command-timeout"},
	"doas":    {"-u", "-C"},
	"xargs":   {"-I", "-i", "-n", "--max-args", "-P", "--max-procs", "-L", "--max-lines", "-d", "--delimiter", "-E", "-e", "--eof", "-s", "--max-chars", "-a", "--arg-file"},
	"timeout": {"-s", "--signal", "-k", "--kill-after"},
	"exec":    {"-a"},
}

// deepArgv 是一段剥掉全部包装（含 sudo、xargs、timeout 等，见 deepWrappers）之后的命令词，供 deny、ask 匹配与“看穿”用。
func deepArgv(seg segment) []string {
	out, _ := peelWrappers(seg.argv, true)
	return out
}

// peelWrappers 剥掉开头的赋值与包装（deep 为真时连 deepWrappers 一起），返回剩下的命令词；env -S（--split-string）的串按
// shell 规则拆成词接在前面（于是不再是 argv 的后缀，split 为真）。timeout 剥掉选项后还要跳过时长。
func peelWrappers(argv []string, deep bool) (out []string, split bool) {
	for len(argv) > 1 {
		a := argv[0]
		if isEnvAssignment(a) {
			argv = argv[1:]
			continue
		}
		name := filepath.Base(a)
		switch {
		case name == "env" || name == "time" || name == "nohup" || name == "nice" || name == "caffeinate":
		case deep && deepWrappers[name]:
		default:
			return argv, split
		}
		rest := argv[1:]
		for len(rest) > 0 && strings.HasPrefix(rest[0], "-") && rest[0] != "-" {
			opt := rest[0]
			rest = rest[1:]
			if opt == "--" {
				break
			}
			key, val, hasVal := strings.Cut(opt, "=")
			takes := slices.Contains(wrapperArgOptions[name], key)
			if !takes && len(key) > 2 && key[1] != '-' && slices.Contains(wrapperArgOptions[name], key[:2]) {
				key, val, hasVal, takes = key[:2], key[2:], true, true // -uNAME、-n5 这类连写
			}
			if !takes {
				continue
			}
			if !hasVal && len(rest) > 0 {
				val, rest = rest[0], rest[1:]
			}
			if name == "env" && (key == "-S" || key == "--split-string") {
				// 串里是命令（可带赋值）：拆成词，接在剩下的前面再接着剥
				words := parseSegment(val).argv
				rest = append(append([]string(nil), words...), rest...)
				split = true
				break
			}
		}
		if name == "timeout" && len(rest) > 0 {
			rest = rest[1:] // 时长
		}
		if len(rest) == 0 {
			return argv, split
		}
		argv = rest
	}
	return argv, split
}

func isEnvAssignment(s string) bool {
	k, _, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return false
	}
	for i, c := range k {
		nameChar := c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9')
		if !nameChar {
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

// touchesSensitiveWords 判断参数里是否有敏感路径，读敏感文件和写一样按 destructive；flags 与 args 一一对应。
// 自己不过滤含空白的词：命令参数由调用方先用 withoutMessages 去掉含空白的（那是提交说明之类的消息，不检查）；
// 输入重定向的来源（seg.inputs）不过滤：含空格的就是文件名，照样要查。--name=value 形式的选项按 value 判断；
// 无法静态解析的参数不在这里判断，由 hasUnresolvable 兜底。
func touchesSensitiveWords(args []string, flags []wordFlags, cwd string, ctx Context) bool {
	for i, a := range args {
		p, ok := pathOperand(a)
		if !ok || wordUnresolvable(p, flagAt(flags, i), ctx) {
			continue
		}
		if ctx.sensitive(resolvePath(p, cwd, ctx.HomeDir)) {
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

// effectiveSegments 返回命令的各段（同 splitSegments 加 parseSegment）；某段是 shell 带内联命令串的调用
// （sh -c、bash -lc、fish -C 等，判断与 shellRisk 相同）时，在它后面递归附上命令串拆出的各段。
// 供 deny 与 ask 规则匹配用：它们要看穿 shell 包装。命令串严格短于外层，递归必然结束。
func effectiveSegments(command string) []segment {
	out, _ := effectiveSegmentsLimited(command)
	return out
}

// maxShellNesting 是 effectiveSegments 看穿 sh -c 包装的层数上限：更深的不再展开（避免畸形输入递归过深），
// 角色的禁用检查据 truncated 按解析不了处理（见 forbidCommands）。
const maxShellNesting = 32

// effectiveSegmentsLimited 同 effectiveSegments，truncated 报告是否因层数上限少展开了内层。
func effectiveSegmentsLimited(command string) (out []segment, truncated bool) {
	return effectiveSegmentsDepth(command, 0)
}

func effectiveSegmentsDepth(command string, depth int) (out []segment, truncated bool) {
	for _, s := range splitSegments(command) {
		seg := parseSegment(s)
		out = append(out, seg)
		argv := deepArgv(seg) // sudo sh -c、timeout 5 bash -c 也看进去
		if len(argv) == 0 {
			continue
		}
		name := filepath.Base(argv[0])
		if !isShell(name) {
			continue
		}
		call, ok := parseShellCall(name, argv)
		if !ok {
			continue
		}
		if depth >= maxShellNesting {
			truncated = true
			continue
		}
		for _, inner := range call.inline() {
			segs, tr := effectiveSegmentsDepth(inner, depth+1)
			out = append(out, segs...)
			truncated = truncated || tr
		}
	}
	return out, truncated
}

// scriptInterpreters 是不带脚本参数（或参数是 -）时从标准输入读脚本来执行的解释器（shell 由 isShell 认）。
var scriptInterpreters = map[string]bool{"python": true, "python3": true, "node": true, "perl": true, "ruby": true, "php": true}

// pipedSegments 返回接在管道（| 或 |&，不含 ||）后面的段的原文（同 splitSegments 的段）。按原文记，
// 同一文本既出现在管道后又出现在别处时都算，宁严勿宽。
func pipedSegments(command string) map[string]bool {
	runes := []rune(command)
	segs, _, heredoc := scanShell(runes)
	out := map[string]bool{}
	if heredoc {
		return out
	}
	for i := 1; i < len(segs); i++ {
		sep := strings.TrimSpace(strings.ReplaceAll(string(runes[segs[i-1].end:segs[i].start]), "\\\n", ""))
		if sep == "|" || sep == "|&" {
			out[string(runes[segs[i].start:segs[i].end])] = true
		}
	}
	return out
}

// readsScriptFromStdin 报告这一段是不是从标准输入读脚本来执行的 shell 或解释器：shell 没有 -c 命令串（含 -s、
// 没有脚本参数、脚本参数是 -，以及认不出的选项），解释器没有参数或参数是 -。用在管道后面时，执行的就是管道前送来的内容：
// curl … | sh 执行的是网上下载的脚本，cat x | sh 执行的是没法静态审查的文件，一律按 destructive。
func readsScriptFromStdin(seg segment) bool {
	argv, _ := commandWords(seg)
	if len(argv) == 0 {
		return false
	}
	name := filepath.Base(argv[0])
	if isShell(name) {
		call, ok := parseShellCall(name, argv)
		if !ok {
			return true
		}
		if call.hasCommand {
			return false
		}
		return call.stdin || call.rest >= len(argv) || argv[call.rest] == "-"
	}
	if scriptInterpreters[name] {
		return len(argv) == 1 || argv[1] == "-"
	}
	return false
}

// runsSubstitution 报告这一段是不是把命令替换或进程替换的结果当脚本执行：bash -c "$(curl …)"、eval "$(curl …)"、
// sh <(curl …)、source <(…)。替换出来的内容无从审查，按 destructive。
func runsSubstitution(seg segment) bool {
	if !seg.subshell {
		return false
	}
	argv, _ := commandWords(seg)
	if len(argv) == 0 {
		return false
	}
	name := filepath.Base(argv[0])
	if !isShell(name) && name != "eval" && name != "source" && name != "." {
		return false
	}
	return strings.Contains(seg.text, "$(") || strings.Contains(seg.text, "`") || strings.Contains(seg.text, "<(")
}
