package policy

import "strings"

// Classify 给统一载荷定风险等级，不看任何规则，见 ClassifyTrace。
func Classify(p Payload, ctx Context) Risk {
	r, _ := ClassifyTrace(p, ctx)
	return r
}

// ClassifyTrace 定级并给出触发点（nil 表示 read 或 write_in_project，没有可说的）。总是返回七档之一，空载荷与未知类型为 unknown。
// 没有项目根时 fail closed（见 Context.ProjectRoot，项目根太宽时按没有处理，见 Context.Root）：没有改动在项目内，
// write_in_project 一律按 write_outside，包括不做路径分析、默认改动工作目录的命令（git commit、make），触发点也说"改项目外的文件"。
func ClassifyTrace(p Payload, ctx Context) (Risk, *Trigger) {
	ctx = ctx.normalized()
	if tooLongToAnalyze(p) {
		return RiskUnknown, &Trigger{Segment: compactSegment(p.Command), Why: WhyUnknown}
	}
	risk, trig := classifyTrace(p, ctx)
	if ctx.ProjectRoot == "" && risk == RiskWriteInProject {
		return RiskWriteOutside, &Trigger{Segment: segmentFor(p), Why: WhyWriteOutside}
	}
	return risk, trig
}

// segmentFor 是非 shell 载荷（或 shell 没有触发段时）给人看的那一处：路径、网址或 server/tool。
func segmentFor(p Payload) string {
	switch p.Type {
	case PayloadShell:
		return compactSegment(p.Command)
	case PayloadFileWrite:
		return p.Path
	case PayloadNetwork:
		return p.URL
	case PayloadMCP:
		return strings.TrimSuffix(p.Server+"/"+p.Tool, "/")
	}
	return ""
}

func classifyTrace(p Payload, ctx Context) (Risk, *Trigger) {
	cwd := p.Cwd
	if cwd == "" {
		cwd = ctx.ProjectRoot
	}
	switch p.Type {
	case PayloadShell:
		return classifyShellTrace(p.Command, cwd, ctx)
	case PayloadFileWrite:
		if p.Path == "" {
			return RiskUnknown, &Trigger{Why: WhyUnknown}
		}
		r := maxRisk(RiskWriteInProject, literalPathRisk(p.Path, cwd, ctx))
		switch r {
		case RiskDestructive:
			return r, &Trigger{Segment: p.Path, Why: WhySensitive}
		case RiskWriteOutside:
			return r, &Trigger{Segment: p.Path, Why: WhyWriteOutside}
		case RiskUnknown:
			return r, &Trigger{Segment: p.Path, Why: WhyUnknown}
		}
		return r, nil
	case PayloadMCP, PayloadNetwork:
		return RiskNetwork, &Trigger{Segment: segmentFor(p), Why: WhyNetwork}
	default:
		return RiskUnknown, &Trigger{Why: WhyUnknown}
	}
}

// MaxAnalyzedCommand 是 shell 命令做静态分析的总长度上限：超过的不解析（分词、看穿包装、展开替换都可能在畸形输入上很慢），
// 直接定为 unknown，规则不匹配；有角色时按“含无法检查的子命令”越界（见 Engine.Evaluate）。正常命令远小于它。
const MaxAnalyzedCommand = 128 << 10

// tooLongToAnalyze 判断载荷是不是超过分析上限的 shell 命令。
func tooLongToAnalyze(p Payload) bool {
	return p.Type == PayloadShell && len(p.Command) > MaxAnalyzedCommand
}
