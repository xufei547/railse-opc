package policy

import (
	"net/url"
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

// NewEngine 校验规则集并预编译正则。
func NewEngine(rs RuleSet) (*Engine, error) {
	if err := rs.Validate(); err != nil {
		return nil, err
	}
	e := &Engine{}
	for _, r := range rs.Rules {
		cr := compiledRule{Rule: r}
		if r.Match.CommandRegex != "" {
			cr.re = regexp.MustCompile(r.Match.CommandRegex) // Validate 已保证可编译
		}
		e.rules = append(e.rules, cr)
	}
	return e, nil
}

// SetPaused 打开总开关后一切请求都 ask。
func (e *Engine) SetPaused(p bool) { e.paused.Store(p) }

func (e *Engine) Paused() bool { return e.paused.Load() }

// Evaluate 先定级，再按范围、条件、优先级和底线选出一条规则；没有则 ask。
func (e *Engine) Evaluate(p Payload, ctx Context) Decision {
	risk := Classify(p, ctx)
	if e.paused.Load() {
		return Decision{Action: ActionAsk, Risk: risk, Reason: "自动审批已暂停"}
	}
	var best *compiledRule
	for i := range e.rules {
		r := &e.rules[i]
		if !r.IsEnabled() || !scopeMatches(r.Scope, ctx) || !matchRequest(r, p, risk, ctx) {
			continue
		}
		if r.Action == ActionAllow && risk >= RiskOutward && r.Source != SourceUser {
			continue // 底线：只有手写规则能放行对外与不可逆操作
		}
		if best == nil || betterRule(r, best) {
			best = r
		}
	}
	if best != nil {
		return Decision{Action: best.Action, Risk: risk, RuleID: best.ID, Reason: "命中规则 " + best.ID}
	}
	return Decision{Action: ActionAsk, Risk: risk, Reason: "无规则命中，按 " + risk.String() + " 默认询问"}
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

// betterRule 判断 a 是否比 b 更优先：范围更具体，其次 deny > allow > ask。
func betterRule(a, b *compiledRule) bool {
	sa, sb := a.Scope.specificity(), b.Scope.specificity()
	if sa != sb {
		return sa > sb
	}
	return actionRank(a.Action) > actionRank(b.Action)
}

func scopeMatches(s Scope, ctx Context) bool {
	switch {
	case s.Project != "":
		if ctx.ProjectRoot == "" {
			return false
		}
		pat := expandHome(s.Project, ctx.HomeDir)
		if pat == ctx.ProjectRoot {
			return true
		}
		ok, _ := doublestar.Match(pat, ctx.ProjectRoot)
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
	if m.CommandPrefix != "" && !(p.Type == PayloadShell && everySegmentHasPrefix(p.Command, m.CommandPrefix)) {
		return false
	}
	if r.re != nil && !(p.Type == PayloadShell && r.re.MatchString(p.Command)) {
		return false
	}
	if m.PathGlob != "" {
		if p.Type != PayloadFileWrite {
			return false
		}
		cwd := p.Cwd
		if cwd == "" {
			cwd = ctx.ProjectRoot
		}
		abs := resolvePath(p.Path, cwd, ctx.HomeDir)
		if ok, _ := doublestar.Match(expandHome(m.PathGlob, ctx.HomeDir), abs); !ok {
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

// everySegmentHasPrefix 要求复合命令的每一段（剥掉包装后）都以 prefix 开头，按词边界比较。
// 任何一段含无法静态分析的写法（命令替换、进程替换、heredoc、未闭合引号等）都不算匹配；
// 这类命令 splitSegments 先返回整条，它的 subshell 为 true，于是整条命令都不匹配。
func everySegmentHasPrefix(command, prefix string) bool {
	segs := splitSegments(command)
	if len(segs) == 0 {
		return false
	}
	for _, s := range segs {
		seg := parseSegment(s)
		if seg.subshell {
			return false
		}
		joined := strings.Join(stripWrappers(seg.argv), " ")
		if joined != prefix && !strings.HasPrefix(joined, prefix+" ") {
			return false
		}
	}
	return true
}

// hostMatches 判断 URL 的主机名等于 domain 或是它的子域。
func hostMatches(rawURL, domain string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	return host == domain || strings.HasSuffix(host, "."+domain)
}
