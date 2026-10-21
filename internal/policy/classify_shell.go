package policy

import (
	"path/filepath"
	"strings"
)

// classifyShell 给一条 shell 命令定级：拆段后逐段分类，取最高档。空命令为 unknown。
func classifyShell(command, cwd string, ctx Context) Risk {
	segs := splitSegments(command)
	if len(segs) == 0 {
		return RiskUnknown
	}
	risk := RiskRead
	for _, text := range segs {
		risk = maxRisk(risk, classifySegment(parseSegment(text), cwd, ctx))
	}
	return risk
}

func classifySegment(seg segment, cwd string, ctx Context) Risk {
	argv := stripWrappers(seg.argv)
	if len(argv) == 0 {
		return RiskUnknown
	}
	risk := classifyArgv(argv, seg.inputs, cwd, ctx)
	if seg.subshell {
		risk = maxRisk(risk, RiskUnknown)
	}
	if len(seg.redirects) > 0 {
		risk = maxRisk(risk, maxRisk(RiskWriteInProject, pathsRisk(seg.redirects, cwd, ctx)))
	}
	// 输入重定向读敏感文件，与参数里出现敏感路径一样按 destructive
	if touchesSensitive(seg.inputs, cwd, ctx) {
		risk = maxRisk(risk, RiskDestructive)
	}
	if hasSQLDrop(seg.text) {
		risk = maxRisk(risk, RiskDestructive)
	}
	return risk
}

// classifyArgv 按命令表给单条命令定级。sh -c "…" 递归分析内层命令。
// inputs 是该段输入重定向的来源。参数或 inputs 里有无法静态解析的路径时，
// 不论是否命中命令表，结果至少为 unknown。
func classifyArgv(argv, inputs []string, cwd string, ctx Context) Risk {
	floor := RiskRead
	if hasUnresolvable(argv[1:], ctx) || hasUnresolvable(inputs, ctx) {
		floor = RiskUnknown
	}
	name := filepath.Base(argv[0])
	if isShell(name) && len(argv) >= 3 && argv[1] == "-c" {
		return maxRisk(floor, classifyShell(argv[2], cwd, ctx))
	}
	for _, p := range commandTable {
		if !p.matches(name, argv) {
			continue
		}
		r := maxRisk(floor, p.risk)
		if p.paths {
			r = maxRisk(r, pathsRisk(argv[1:], cwd, ctx))
		}
		if touchesSensitive(argv[1:], cwd, ctx) {
			r = maxRisk(r, RiskDestructive)
		}
		return r
	}
	if touchesSensitive(argv[1:], cwd, ctx) {
		return RiskDestructive
	}
	return RiskUnknown
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

// touchesSensitive 判断参数里是否有敏感路径，读敏感文件和写一样按 destructive。
// --name=value 形式的选项按 value 判断；无法静态解析的参数不在这里判断，由 hasUnresolvable 兜底。
func touchesSensitive(args []string, cwd string, ctx Context) bool {
	for _, a := range args {
		p, ok := pathOperand(a)
		if !ok || unresolvable(p, ctx.HomeDir) {
			continue
		}
		if isSensitive(resolvePath(p, cwd, ctx.HomeDir), ctx.HomeDir) {
			return true
		}
	}
	return false
}

// hasUnresolvable 判断参数里是否有不读环境就无法解析的路径：变量、~user、glob 元字符，
// 或家目录未知时的家目录写法。--name=value 形式的选项按 value 判断。
func hasUnresolvable(args []string, ctx Context) bool {
	for _, a := range args {
		if p, ok := pathOperand(a); ok && unresolvable(p, ctx.HomeDir) {
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
