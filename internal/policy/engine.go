package policy

import (
	"net"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
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

// betterRule 判断 a 是否比 b 更优先：用户的 deny 压一切；其次用户规则整体压内置和学来的规则；
// 同一层内范围更具体者优先，再按 deny > allow > ask。
// 注意：中枢把用户在审批时选的"记住拒绝"存成规则时必须用 source: user；存成 learned 的 deny
// 会输给任何命中的用户 allow（哪怕是很宽的一条），记住的拒绝就失效了。
func betterRule(a, b *compiledRule) bool {
	if ua, ub := a.userDeny(), b.userDeny(); ua != ub {
		return ua
	}
	if ua, ub := a.Source == SourceUser, b.Source == SourceUser; ua != ub {
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
		glob := expandHome(m.PathGlob, ctx.HomeDir)
		switch {
		case p.Type == PayloadFileWrite:
			if !globMatch(glob, resolvePath(p.Path, cwd, ctx.HomeDir), anySeg) {
				return false
			}
		case p.Type == PayloadShell && anySeg:
			// deny 与 ask 规则也看 shell 命令的写入目标
			if !slices.ContainsFunc(shellWriteTargets(p.Command, cwd, ctx), func(abs string) bool { return globMatch(glob, abs, true) }) {
				return false
			}
		default:
			return false
		}
	}
	if m.MCPServer != "" && !(p.Type == PayloadMCP && p.Server == m.MCPServer) {
		return false
	}
	if m.MCPTool != "" && !(p.Type == PayloadMCP && p.Tool == m.MCPTool) {
		return false
	}
	if m.Domain != "" {
		switch {
		case p.Type == PayloadNetwork:
			// deny 与 ask 宽松解析主机名，allow 严格解析
			if anySeg && !hostMatchesLenient(p.URL, m.Domain) || !anySeg && !hostMatches(p.URL, m.Domain) {
				return false
			}
		case p.Type == PayloadShell && anySeg:
			// deny 与 ask 规则也看 shell 命令里出现的 URL
			if !shellMentionsHost(p.Command, m.Domain) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// globMatch 判断绝对路径是否命中通配模式。fold 为 true 时与敏感路径表一样按 foldPath 折叠大小写，
// 近似 APFS 不区分大小写；deny 与 ask 规则折叠，allow 规则精确匹配。
func globMatch(pattern, abs string, fold bool) bool {
	if fold {
		pattern, abs = foldPath(pattern), foldPath(abs)
	}
	ok, _ := doublestar.Match(pattern, abs)
	return ok
}

// fetchCommands 的参数不带协议时也按 URL 理解（curl evil.com/x 默认走 http）。
var fetchCommands = []string{"curl", "wget"}

// fetchHostOptions 是 curl 带主机的选项：值按 : 拆开，每一段都当主机名看（--connect-to H1:P1:H2:P2、
// --resolve H:P:ADDR、-x/--proxy [协议://]H[:P]）。
var fetchHostOptions = []string{"--connect-to", "--resolve", "--proxy", "-x"}

// fetchHostValues 取出 curl、wget 参数里带主机的选项值：--opt value、--opt=value、-x value、-xvalue（含选项簇）。
func fetchHostValues(argv []string) []string {
	spec := outputOptions["curl"]
	withArg := spec.short + spec.taint + spec.shortArg // 带参数的短选项：簇里遇到就停
	var out []string
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if strings.HasPrefix(a, "--") {
			name, val, hasVal := strings.Cut(a, "=")
			if !slices.Contains(fetchHostOptions, name) {
				continue
			}
			if hasVal {
				out = append(out, val)
			} else if i+1 < len(argv) {
				i++
				out = append(out, argv[i])
			}
			continue
		}
		if len(a) < 2 || a[0] != '-' {
			continue
		}
		for j := 1; j < len(a); j++ {
			if strings.IndexByte(withArg, a[j]) < 0 {
				continue
			}
			if a[j] == 'x' {
				if j+1 < len(a) {
					out = append(out, a[j+1:])
				} else if i+1 < len(argv) {
					i++
					out = append(out, argv[i])
				}
			} else if j+1 == len(a) {
				i++ // 其他带参数的短选项，参数是下一个词
			}
			break
		}
	}
	return out
}

// shellMentionsHost 判断命令（看穿 sh -c）里是否有词是主机名命中 domain 的 URL。
// 含 = 的词（--opt=value、VAR=value）也看 = 之后的部分；curl、wget 不带协议的操作数补上 http:// 再看。
func shellMentionsHost(command, domain string) bool {
	for _, seg := range effectiveSegments(command) {
		argv := stripWrappers(seg.argv)
		fetch := len(argv) > 0 && slices.Contains(fetchCommands, filepath.Base(argv[0]))
		for _, w := range seg.argv {
			cands := []string{w}
			if _, v, ok := strings.Cut(w, "="); ok {
				cands = append(cands, v)
			}
			for _, c := range cands {
				if hostMatchesLenient(c, domain) {
					return true
				}
				if fetch && !strings.HasPrefix(c, "-") && !strings.Contains(c, "://") && hostMatchesLenient("http://"+c, domain) {
					return true
				}
			}
		}
		if !fetch {
			continue
		}
		for _, v := range fetchHostValues(argv) {
			if hostMatchesLenient(v, domain) {
				return true
			}
			for _, part := range strings.Split(v, ":") {
				if part != "" && hostMatchesLenient("http://"+part, domain) {
					return true
				}
			}
		}
	}
	return false
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
// 有全局选项表的命令（git、npm 等）再剥掉子命令前的全局选项，包管理器的子命令别名换成正式名。没有命令时返回 nil。
func normalizedArgv(seg segment, cwd string, ctx Context) []string {
	argv, flags := commandWords(seg)
	if len(argv) == 0 {
		return nil
	}
	name := filepath.Base(argv[0])
	if spec, ok := leadingOptions[name]; ok {
		argv, _, _, _ = stripLeading(argv, flags, spec, cwd, ctx)
	}
	if alt, ok := canonicalSubcommand(name, argv); ok {
		argv = alt
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

// hostMatches 严格解析 URL，判断主机名等于 domain 或是它的子域（见 hostIn）。
func hostMatches(rawURL, domain string) bool {
	host, _ := parseHost(rawURL)
	return hostIn(host, domain)
}

// hostMatchesLenient 供 deny 与 ask 规则用：严格解析（含解码后重试）失败时，宽松取出 authority
// （:// 之后到第一个 /、?、# 为止），把最后一个 @ 之后的部分和第一个反斜杠之前的部分都当主机名看，
// 例如 https://good.com\@evil.com/。allow 规则不用它。
func hostMatchesLenient(rawURL, domain string) bool {
	host, parsed := parseHost(rawURL)
	if parsed {
		return hostIn(host, domain)
	}
	_, rest, ok := strings.Cut(rawURL, "://")
	if !ok {
		return false
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	afterAt := func(s string) string { return s[strings.LastIndex(s, "@")+1:] }
	beforeBackslash := func(s string) string {
		if i := strings.IndexByte(s, '\\'); i >= 0 {
			return s[:i]
		}
		return s
	}
	for _, c := range []string{afterAt(rest), beforeBackslash(rest), beforeBackslash(afterAt(rest)), afterAt(beforeBackslash(rest))} {
		if h, _, err := net.SplitHostPort(c); err == nil {
			c = h
		}
		if hostIn(c, domain) {
			return true
		}
	}
	return false
}

// parseHost 用 url.Parse 取主机名。Go 拒绝主机里的百分号编码（curl 会解码），解析失败时先整体解码再解析一次；
// parsed 为 false 表示两次都失败。
func parseHost(rawURL string) (host string, parsed bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		dec, derr := url.PathUnescape(rawURL)
		if derr != nil {
			return "", false
		}
		if u, err = url.Parse(dec); err != nil {
			return "", false
		}
	}
	return u.Hostname(), true
}

// hostIn 判断主机名等于 domain 或是它的子域，两边先按 normalizeHost 归一化。
func hostIn(host, domain string) bool {
	host, domain = normalizeHost(host), normalizeHost(domain)
	if host == "" || domain == "" {
		return false
	}
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// normalizeHost 把全角 ASCII（U+FF01–U+FF5E）换成半角，把 U+3002、U+FF0E、U+FF61 换成点
// （IDNA 映射后它们就是同一个主机名），再转小写并去掉末尾一个点。
func normalizeHost(h string) string {
	h = strings.Map(func(r rune) rune {
		switch {
		case r == '\u3002' || r == '\uff0e' || r == '\uff61':
			return '.'
		case r >= '\uff01' && r <= '\uff5e':
			return r - 0xfee0
		}
		return r
	}, h)
	return strings.TrimSuffix(strings.ToLower(h), ".")
}
