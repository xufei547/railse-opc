package policy

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// ErrCannotLearn 表示请求的风险等级不能生成 learned 放行规则。只有 read、write_in_project 与 network 可以：
// unknown 与 write_outside 涉及项目外的路径或无法解析的参数，两个词的前缀表达不了；
// outward 与 destructive 只能由用户手写规则放行。
var ErrCannotLearn = errors.New("只有只读、项目内改动和联网操作能记住为自动放行规则，其余请手写规则")

// ProposeRule 从一条用户已批准的请求生成 learned 规则草案：action 为 allow，risk 固定为请求当前的风险等级，
// 范围为当前项目，没有项目时为 global。风险等级不是 read、write_in_project 或 network 时返回 ErrCannotLearn。
// shell 取剥掉包装后的前两个词做前缀，只接受单段命令，并且不能含无法静态分析的写法、引号外的通配或 $ 展开、
// 指向文件的重定向：引擎的放行检查（segmentAllowable）不会命中这样的命令，学出来的规则是死的。
// file_write 取所在目录；mcp 取服务加工具；network 取主机名。路径按字面转义通配元字符。
// 草案在返回前经过 Validate。
func ProposeRule(p Payload, ctx Context, requestID string) (Rule, error) {
	r := Rule{
		ID:          "learned-" + shortID(requestID),
		Source:      SourceLearned,
		Action:      ActionAllow,
		CreatedFrom: requestID,
		Match:       Match{Payload: p.Type},
	}
	if ctx.ProjectRoot != "" {
		r.Scope = Scope{Project: escapeGlob(ctx.ProjectRoot)}
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd = ctx.ProjectRoot
	}
	switch p.Type {
	case PayloadShell:
		prefix, err := learnablePrefix(p.Command)
		if err != nil {
			return Rule{}, err
		}
		r.Match.CommandPrefix = prefix
	case PayloadFileWrite:
		dir := filepath.Dir(resolvePath(p.Path, cwd, ctx.HomeDir))
		r.Match.PathGlob = filepath.Join(escapeGlob(dir), "**")
	case PayloadMCP:
		r.Match.MCPServer = p.Server
		r.Match.MCPTool = p.Tool
	case PayloadNetwork:
		// 与引擎的 hostMatches 用同一个解析
		host, ok := parseHost(p.URL)
		if !ok || host == "" {
			return Rule{}, errors.New("URL 无效，不能生成规则")
		}
		r.Match.Domain = normalizeHost(host)
	default:
		return Rule{}, fmt.Errorf("未知载荷类型 %q", p.Type)
	}
	// 命令前设置能注入代码的环境变量时 Classify 至少为 unknown，在这里拒绝
	risk := Classify(p, ctx)
	switch risk {
	case RiskRead, RiskWriteInProject, RiskNetwork:
	default:
		return Rule{}, ErrCannotLearn
	}
	r.Match.Risk = &risk
	if err := (RuleSet{Version: RuleSetVersion, Rules: []Rule{r}}).Validate(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// learnablePrefix 检查单段 shell 命令能否被前缀规则放行，能则返回剥掉包装后的前两个词。
func learnablePrefix(command string) (string, error) {
	segs := splitSegments(command)
	if len(segs) > 1 {
		return "", errors.New("复合命令不能生成规则，请手写")
	}
	if len(segs) == 0 {
		return "", errors.New("空命令不能生成规则")
	}
	seg := parseSegment(segs[0])
	if seg.subshell {
		return "", errors.New("命令含命令替换、子 shell、heredoc 等无法静态分析的写法，不能生成规则")
	}
	if slices.ContainsFunc(seg.argvFlags, func(f wordFlags) bool { return f.glob || f.expand }) {
		return "", errors.New("命令含引号外的通配或 $ 展开，不能生成规则")
	}
	if !harmlessTargets(seg.redirects, seg.redirectFlags) || !harmlessTargets(seg.inputs, seg.inputFlags) {
		return "", errors.New("命令含指向文件的重定向，不能生成规则")
	}
	argv := stripWrappers(seg.argv)
	if len(argv) == 0 {
		return "", errors.New("空命令不能生成规则")
	}
	return strings.Join(argv[:min(2, len(argv))], " "), nil
}

// escapeGlob 给字面路径里的 doublestar 元字符加反斜杠，让它在规则里只匹配自己。
func escapeGlob(s string) string {
	var b strings.Builder
	for _, c := range s {
		if strings.ContainsRune(`\*?[]{}`, c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
