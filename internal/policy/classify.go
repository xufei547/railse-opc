package policy

// Classify 给统一载荷定风险等级，不看任何规则。
func Classify(p Payload, ctx Context) Risk {
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
