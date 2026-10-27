package policy

// Classify 给统一载荷定风险等级，不看任何规则。总是返回七档之一，空载荷与未知类型为 unknown。
// 没有项目根时 fail closed（见 Context.ProjectRoot，项目根太宽时按没有处理，见 Context.Root）：没有改动在项目内，
// write_in_project 一律按 write_outside，包括不做路径分析、默认改动工作目录的命令（git commit、make）。
func Classify(p Payload, ctx Context) Risk {
	ctx = ctx.normalized()
	risk := classify(p, ctx)
	if ctx.ProjectRoot == "" && risk == RiskWriteInProject {
		return RiskWriteOutside
	}
	return risk
}

func classify(p Payload, ctx Context) Risk {
	cwd := p.Cwd
	if cwd == "" {
		cwd = ctx.ProjectRoot
	}
	switch p.Type {
	case PayloadShell:
		return classifyShell(p.Command, cwd, ctx)
	case PayloadFileWrite:
		if p.Path == "" {
			return RiskUnknown
		}
		return maxRisk(RiskWriteInProject, literalPathRisk(p.Path, cwd, ctx))
	case PayloadMCP, PayloadNetwork:
		return RiskNetwork
	default:
		return RiskUnknown
	}
}
