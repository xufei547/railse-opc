package policy

import (
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"

	"github.com/bmatcuk/doublestar/v4"
)

// Decision 是引擎对一条请求的结论。
type Decision struct {
	Action Action `json:"action"`
	Risk   Risk   `json:"risk"`
	RuleID string `json:"rule_id,omitempty"` // 命中的规则，默认决定时为空
	Reason string `json:"reason"`            // 给人看的依据
}

// Engine 持有编译好的规则。规则在 NewEngine 之后不再改动，Evaluate 可并发调用；
// paused 用原子变量，SetPaused 可与 Evaluate 在不同 goroutine 并发调用。
type Engine struct {
	rules  []compiledRule
	paused atomic.Bool
}

type compiledRule struct {
	Rule
	re *regexp.Regexp
}

// NewEngine 校验规则集并预编译正则。规则里的指针字段深拷贝，调用方之后改动规则集不影响引擎。
func NewEngine(rs RuleSet) (*Engine, error) {
	if err := rs.Validate(); err != nil {
		return nil, err
	}
	e := &Engine{}
	for _, r := range rs.Rules {
		if r.Enabled != nil {
			v := *r.Enabled
			r.Enabled = &v
		}
		if r.Match.Risk != nil {
			v := *r.Match.Risk
			r.Match.Risk = &v
		}
		cr := compiledRule{Rule: r}
		if r.Match.CommandRegex != "" {
			cr.re = regexp.MustCompile(r.Match.CommandRegex) // Validate 已保证可编译
		}
		e.rules = append(e.rules, cr)
	}
	return e, nil
}

// SetPaused 打开总开关后，除命中用户 deny 的请求外一切都 ask。
func (e *Engine) SetPaused(p bool) { e.paused.Store(p) }

func (e *Engine) Paused() bool { return e.paused.Load() }

// Evaluate 先定级，再按范围、条件、底线和优先级选出一条规则；没有则 ask。
func (e *Engine) Evaluate(p Payload, ctx Context) Decision {
	risk := Classify(p, ctx)
	var best *compiledRule
	for i := range e.rules {
		r := &e.rules[i]
		if !r.IsEnabled() || !scopeMatches(r.Scope, ctx) || !matchRequest(r, p, risk, ctx) {
			continue
		}
		if r.Action == ActionAllow && !mayAllow(r, risk) {
			continue
		}
		if best == nil || betterRule(r, best) {
			best = r
		}
	}
	// 暂停只停自动决定，命中的用户 deny 仍然生效；它压过一切，命中时必是 best。
	if e.paused.Load() && (best == nil || !best.userDeny()) {
		return Decision{Action: ActionAsk, Risk: risk, Reason: "自动审批已暂停"}
	}
	if best != nil {
		return Decision{Action: best.Action, Risk: risk, RuleID: best.ID, Reason: "命中规则 " + best.ID}
	}
	return Decision{Action: ActionAsk, Risk: risk, Reason: "无规则命中，按 " + risk.String() + " 默认询问"}
}

// mayAllow 是底线：outward 只有 user 规则能放行；destructive 还要求规则显式写 risk: destructive。
func mayAllow(r *compiledRule, risk Risk) bool {
	switch {
	case risk >= RiskDestructive:
		return r.Source == SourceUser && r.Match.Risk != nil && *r.Match.Risk == RiskDestructive
	case risk >= RiskOutward:
		return r.Source == SourceUser
	default:
		return true
	}
}

func actionRank(a Action) int {
	switch a {
	case ActionDeny:
		return 3
	case ActionAllow:
		return 2
	default:
		return 1
	}
}

// betterRule 判断 a 是否比 b 更优先：用户的 deny 压一切；其余按范围更具体，其次 deny > allow > ask。
func betterRule(a, b *compiledRule) bool {
	if ua, ub := a.userDeny(), b.userDeny(); ua != ub {
		return ua
	}
	sa, sb := a.Scope.specificity(), b.Scope.specificity()
	if sa != sb {
		return sa > sb
	}
	return actionRank(a.Action) > actionRank(b.Action)
}

func (r *compiledRule) userDeny() bool { return r.Source == SourceUser && r.Action == ActionDeny }

func scopeMatches(s Scope, ctx Context) bool {
	switch {
	case s.Project != "":
		if ctx.ProjectRoot == "" {
			return false
		}
		pat := filepath.Clean(expandHome(s.Project, ctx.HomeDir))
		root := filepath.Clean(ctx.ProjectRoot)
		if pat == root {
			return true
		}
		ok, _ := doublestar.Match(pat, root)
		return ok
	case s.Machine != "":
		return s.Machine == ctx.MachineID
	default:
		return true
	}
}

func matchRequest(r *compiledRule, p Payload, risk Risk, ctx Context) bool {
	m := r.Match
	if m.Payload != "" && m.Payload != p.Type {
		return false
	}
	if m.Risk != nil && *m.Risk != risk {
		return false
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd = ctx.ProjectRoot
	}
	// allow 规则要求每一段都命中；deny 与 ask 是收紧，任一段命中即可，且同时按归一化的命令比较。
	anySeg := r.Action != ActionAllow
	if m.CommandPrefix != "" {
		if p.Type != PayloadShell || !segmentsMatch(p.Command, anySeg, func(seg segment) bool {
			if segmentHasPrefix(seg, m.CommandPrefix) {
				return true
			}
			return anySeg && wordsHavePrefix(normalizedArgv(seg, cwd, ctx), m.CommandPrefix)
		}) {
			return false
		}
	}
	if r.re != nil {
		if p.Type != PayloadShell || !segmentsMatch(p.Command, anySeg, func(seg segment) bool {
			if r.re.MatchString(seg.text) {
				return true
			}
			norm := normalizedArgv(seg, cwd, ctx)
			return anySeg && len(norm) > 0 && r.re.MatchString(strings.Join(norm, " "))
		}) {
			return false
		}
	}
	if m.PathGlob != "" {
		if p.Type != PayloadFileWrite {
			return false
		}
		// 与敏感路径表一样按 foldPath 折叠大小写，近似 APFS 不区分大小写
		abs := foldPath(resolvePath(p.Path, cwd, ctx.HomeDir))
		if ok, _ := doublestar.Match(foldPath(expandHome(m.PathGlob, ctx.HomeDir)), abs); !ok {
			return false
		}
	}
	if m.MCPServer != "" && !(p.Type == PayloadMCP && p.Server == m.MCPServer) {
		return false
	}
	if m.MCPTool != "" && !(p.Type == PayloadMCP && p.Tool == m.MCPTool) {
		return false
	}
	if m.Domain != "" && !(p.Type == PayloadNetwork && hostMatches(p.URL, m.Domain)) {
		return false
	}
	return true
}

// everySegmentHasPrefix 要求复合命令的每一段（剥掉包装后）都以 prefix 开头，按词边界比较，
// 且每一段都能放行（见 segmentAllowable）。
func everySegmentHasPrefix(command, prefix string) bool {
	return segmentsMatch(command, false, func(seg segment) bool { return segmentHasPrefix(seg, prefix) })
}

// segmentsMatch 按段匹配，pred 收到解析后的段（seg.text 是原文）。
// anySeg 为 true 时按 effectiveSegments 取段，看穿 sh -c 这类包装，任一段命中即可；
// 否则按 splitSegments 取段，不看穿包装，每一段都要命中且都能放行。
// 无法静态分析的命令 splitSegments 先返回整条，它的 subshell 为 true，于是按每段匹配时整条都不命中。
func segmentsMatch(command string, anySeg bool, pred func(seg segment) bool) bool {
	if anySeg {
		for _, seg := range effectiveSegments(command) {
			if pred(seg) {
				return true
			}
		}
		return false
	}
	segs := splitSegments(command)
	if len(segs) == 0 {
		return false
	}
	for _, s := range segs {
		seg := parseSegment(s)
		if !segmentAllowable(seg) || !pred(seg) {
			return false
		}
	}
	return true
}

// segmentHasPrefix 判断剥掉包装后的 argv 是否以 prefix 开头，按词边界比较。
func segmentHasPrefix(seg segment, prefix string) bool {
	return wordsHavePrefix(stripWrappers(seg.argv), prefix)
}

func wordsHavePrefix(words []string, prefix string) bool {
	joined := strings.Join(words, " ")
	return joined == prefix || strings.HasPrefix(joined, prefix+" ")
}

// normalizedArgv 返回 deny 与 ask 规则额外比较的归一化命令：剥掉包装，argv[0] 取 basename，
// 有全局选项表的命令（git、npm 等）再剥掉子命令前的全局选项。没有命令时返回 nil。
func normalizedArgv(seg segment, cwd string, ctx Context) []string {
	argv, flags := commandWords(seg)
	if len(argv) == 0 {
		return nil
	}
	name := filepath.Base(argv[0])
	if spec, ok := leadingOptions[name]; ok {
		argv, _, _, _ = stripLeading(argv, flags, spec, cwd, ctx)
	}
	return append([]string{name}, argv[1:]...)
}

// segmentAllowable 判断一段能否被 allow 规则按前缀或正则放行：不含无法静态分析的写法，
// 命令前没有注入型环境变量，命令的词里没有引号外的 glob 或 $ 展开，输出与输入重定向只指向无害伪设备。
func segmentAllowable(seg segment) bool {
	if seg.subshell {
		return false
	}
	argv, flags := commandWords(seg)
	if assignsInjectedEnv(seg.argv[:len(seg.argv)-len(argv)]) {
		return false
	}
	for _, f := range flags {
		if f.glob || f.expand {
			return false
		}
	}
	return harmlessTargets(seg.redirects, seg.redirectFlags) && harmlessTargets(seg.inputs, seg.inputFlags)
}

// harmlessTargets 判断重定向目标是否都是无害伪设备：不含引号外的 glob、$ 与 ~ 的字面绝对路径，清理后命中 harmlessDevice。
func harmlessTargets(targets []string, flags []wordFlags) bool {
	for i, t := range targets {
		f := flagAt(flags, i)
		if f.glob || f.expand || f.tilde || !filepath.IsAbs(t) || !harmlessDevice(filepath.Clean(t)) {
			return false
		}
	}
	return true
}

// hostMatches 判断 URL 的主机名等于 domain 或是它的子域。两边都转小写并去掉末尾一个点。
func hostMatches(rawURL, domain string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host, domain := normalizeHost(u.Hostname()), normalizeHost(domain)
	if domain == "" {
		return false
	}
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func normalizeHost(h string) string {
	return strings.TrimSuffix(strings.ToLower(h), ".")
}
