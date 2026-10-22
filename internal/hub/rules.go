package hub

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/wire"
	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/redact"
)

// errLearn 表示“记住”失败：这条请求生成不了规则（风险等级不许自动放行、载荷含凭据、形状表达不了等）。
// 请求保持 pending，REST 回 422。读机器、存规则这类数据库错误不包它（回 500）。
var errLearn = errors.New("不能记住")

// redactedMark 是 redact 包写进载荷的脱敏占位符。
const redactedMark = "<redacted>"

const (
	// maxLearnAnchor 是“记住”时命令、路径、MCP 服务名与工具名、主机名的字节上限：规则要随快照整套下发，单帧至多 1 MiB。
	maxLearnAnchor = 4 << 10
	// frameHeadroom 是规则快照编成帧后距单帧上限至少留的余量（入库时 id 冲突会追加 -2 之类的后缀）。
	frameHeadroom = 1 << 10
)

// pushRules 把当前规则快照发给所有守护进程并通知网页。快照在任何事务之外读（RuleSet 自开事务）。
// 编不成帧（超过单帧上限）时记日志、不发给守护进程；网页照常收到 rules_updated。
// 并发的两次下发可能乱序到达，守护进程忽略版本号不大于当前的快照，hub 不另加序。
func (s *Server) pushRules() {
	rs, version, err := s.store.RuleSet()
	if err != nil {
		s.log.Printf("读规则快照: %v", err)
		return
	}
	y, err := rs.Marshal()
	if err != nil {
		s.log.Printf("编码规则快照: %v", err)
		return
	}
	if err := s.agents.broadcast(wire.Rules{T: wire.TRules, Version: version, YAML: string(y)}); err != nil {
		s.log.Printf("下发规则快照（版本 %d）: %v", version, err)
	}
	s.clients.broadcast("rules_updated", map[string]int{"version": version})
}

// learnRule 从一条请求生成规则并入库，返回实际的规则 id。它不下发：调用方在请求状态改成功之后再 pushRules，
// 改状态失败时用 rollbackLearned 删掉。
//   - 批准：policy.ProposeRule 生成 learned 放行；outward、destructive、unknown、write_outside 返回 ErrCannotLearn，
//     所以“记住”永远产生不了能放行这几类的规则。
//   - 拒绝：denyRule 直接构造 user 来源的 deny，压过一切放行（见 policy.betterRule）。
//
// 载荷含脱敏占位符时不学：学出来的规则匹配的是占位符，不是原命令，用户却以为记住了。
// 锚点超过 maxLearnAnchor，或现有规则加上这条编成快照会超过单帧上限（减去余量）时不学：否则快照与 welcome 都发不出去，
// 所有守护进程会连不上。“读现有规则 → 试编 → 入库”在 rulesMu 下进行，与导入、启停串行。
// 学不了的错误包 errLearn；读机器、读存规则的错误不包（其中的 store.ErrNotFound 也不外露，免得被当成“没有这条请求”）。
func (s *Server) learnRule(r store.Request, action string) (string, error) {
	// 先查长度（便宜），超长的大载荷不必再跑脱敏正则
	if what, long := anchorTooLong(r.Payload); long {
		return "", fmt.Errorf("%w：%s太长不能记住（超过 4 KiB）", errLearn, what)
	}
	if hasRedacted(r.Payload) {
		return "", fmt.Errorf("%w：含凭据的命令不能记住", errLearn)
	}
	m, err := s.store.GetMachine(r.MachineID)
	if err != nil {
		return "", fmt.Errorf("读机器 %s: %v", r.MachineID, err)
	}
	ctx := policy.Context{ProjectRoot: r.ProjectRoot, HomeDir: m.HomeDir, MachineID: r.MachineID}
	var rule policy.Rule
	if action == "deny" {
		rule, err = denyRule(r, ctx)
	} else {
		rule, err = policy.ProposeRule(r.Payload, ctx, r.ID)
	}
	if err != nil {
		return "", fmt.Errorf("%w：%w", errLearn, err)
	}
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	rs, _, err := s.store.RuleSet()
	if err != nil {
		return "", fmt.Errorf("读规则: %v", err)
	}
	rs.Rules = append(rs.Rules, rule)
	if !rulesFitFrame(rs) {
		return "", fmt.Errorf("%w：规则集已接近下发上限，放不下这条规则，请先清理不用的规则", errLearn)
	}
	id, _, err := s.store.InsertRule(rule)
	if err != nil {
		return "", fmt.Errorf("存规则: %v", err)
	}
	return id, nil
}

// anchorTooLong 报告载荷里会成为规则锚点的部分是否超过 maxLearnAnchor，并说明是哪一部分。
func anchorTooLong(p policy.Payload) (string, bool) {
	switch p.Type {
	case policy.PayloadShell:
		return "命令", len(strings.TrimSpace(p.Command)) > maxLearnAnchor
	case policy.PayloadFileWrite:
		return "路径", len(p.Path) > maxLearnAnchor
	case policy.PayloadMCP:
		return "MCP 服务名或工具名", len(p.Server) > maxLearnAnchor || len(p.Tool) > maxLearnAnchor
	case policy.PayloadNetwork:
		u, err := url.Parse(p.URL)
		return "主机名", err == nil && len(u.Hostname()) > maxLearnAnchor
	}
	return "", false
}

// rollbackLearned 撤销刚学的规则：请求在学规则之后、改状态之前已被别处结束（取消、过期、重发顶掉），规则不该留下。
// 删掉后照样下发一次快照：期间别的改动若已把它带出去，守护进程随之收回。规则已不在（期间被别处删掉）当作已删除，
// 审计标 already_gone；删除失败只能记日志，审计里标 deleted false。
func (s *Server) rollbackLearned(r store.Request, ruleID string) {
	_, err := s.store.DeleteRule(ruleID)
	gone := errors.Is(err, store.ErrNotFound)
	if err != nil && !gone {
		s.log.Printf("撤销请求 %q 学到的规则 %s 失败，需在网页里手动删除: %v", r.ID, ruleID, err)
	}
	s.audit(r.MachineID, "user", r.ID, "rule_learn_rolled_back", map[string]any{"rule_id": ruleID, "deleted": err == nil || gone, "already_gone": gone})
	s.pushRules()
}

// denyRule 构造“拒绝并记住”的规则：user 来源的 deny，有项目根时限定该项目、否则 global；不限风险等级
// （引擎按整条请求定级，限了会被风险更高的复合命令绕过）。按载荷类型锚定：
//   - shell：command_regex 锚定整条命令（去掉首尾空白）的字面原文，只拒这条命令，不连带同前缀的命令；
//   - file_write：精确路径（相对路径接在 cwd 或项目根后，~ 按家目录展开），通配元字符转义；
//   - mcp：服务加工具；network：主机名。
//
// 锚点缺失、类型不认识，或构造出的规则拒绝不了原请求（例如复合命令：引擎对 deny 正则按段匹配，整条复合命令的正则哪段都对不上）
// 时返回错误：不学死规则。
func denyRule(r store.Request, ctx policy.Context) (policy.Rule, error) {
	p := r.Payload
	rule := policy.Rule{ID: "deny-" + shortRuleID(r.ID), Source: policy.SourceUser, Action: policy.ActionDeny, CreatedFrom: r.ID,
		Match: policy.Match{Payload: p.Type}}
	if r.ProjectRoot != "" {
		rule.Scope = policy.Scope{Project: escapeGlob(r.ProjectRoot)}
	}
	switch p.Type {
	case policy.PayloadShell:
		cmd := strings.TrimSpace(p.Command)
		if cmd == "" {
			return policy.Rule{}, errors.New("空命令不能记住")
		}
		rule.Match.CommandRegex = "^" + regexp.QuoteMeta(cmd) + "$"
	case policy.PayloadFileWrite:
		cwd := p.Cwd
		if cwd == "" {
			cwd = r.ProjectRoot
		}
		abs, ok := absPath(p.Path, cwd, ctx.HomeDir)
		if !ok {
			return policy.Rule{}, errors.New("路径解析不成绝对路径，不能记住")
		}
		rule.Match.PathGlob = escapeGlob(abs)
	case policy.PayloadMCP:
		if p.Server == "" || p.Tool == "" {
			return policy.Rule{}, errors.New("MCP 请求缺服务名或工具名，不能记住")
		}
		rule.Match.MCPServer, rule.Match.MCPTool = p.Server, p.Tool
	case policy.PayloadNetwork:
		u, err := url.Parse(p.URL)
		if err != nil || u.Hostname() == "" {
			return policy.Rule{}, errors.New("URL 无效，不能记住")
		}
		rule.Match.Domain = strings.ToLower(u.Hostname())
	default:
		return policy.Rule{}, fmt.Errorf("载荷类型 %q 不能记住", p.Type)
	}
	// NewEngine 先跑 RuleSet.Validate；再用只含这条规则的引擎评估原请求，必须命中它并拒绝
	eng, err := policy.NewEngine(policy.RuleSet{Version: policy.RuleSetVersion, Rules: []policy.Rule{rule}})
	if err != nil {
		return policy.Rule{}, err
	}
	if d := eng.Evaluate(p, ctx); d.Action != policy.ActionDeny || d.RuleID != rule.ID {
		return policy.Rule{}, errors.New("记住的拒绝匹配不上这条请求（例如复合命令），请手写规则")
	}
	return rule, nil
}

// homePrefixes 与 policy 引擎认识的家目录写法一致。
var homePrefixes = []string{"~", "$HOME", "${HOME}"}

// absPath 按引擎解析载荷路径的方式（policy 的 resolvePath）给出绝对路径：家目录写法开头的按家目录展开，
// 相对路径接在 cwd 后，最后 Clean。解析不出（空路径、没有家目录、cwd 不是绝对路径）时 ok 为 false。
func absPath(p, cwd, home string) (string, bool) {
	if p == "" {
		return "", false
	}
	for _, pre := range homePrefixes {
		if p == pre || strings.HasPrefix(p, pre+"/") {
			if home == "" {
				return "", false
			}
			p = filepath.Join(home, strings.TrimPrefix(p, pre))
			break
		}
	}
	if !filepath.IsAbs(p) {
		if !filepath.IsAbs(cwd) {
			return "", false
		}
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p), true
}

// escapeGlob 给字面路径里的 doublestar 元字符加反斜杠，让它在规则里只匹配自己（同 policy 包的转义）。
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

// shortRuleID 取请求 id 的前 8 个字节作规则 id 的后缀（请求 id 只含 ASCII，见 validRequestID）。
func shortRuleID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// hasRedacted 报告载荷里有没有脱敏占位符。先经 redact.Payload 规整（MCP 参数重新编码、不转义 < >，
// 转义写法的占位符也还原成字面），再不转义 HTML 地编码整个载荷来找；编码失败当作有（宁可不记）。
func hasRedacted(p policy.Payload) bool {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redact.Payload(p)); err != nil {
		return true
	}
	return strings.Contains(buf.String(), redactedMark)
}

// elevated 报告规则能否自动放行 outward 或 destructive（与 policy 引擎的 mayAllow 一致）：user 来源的 allow，
// 且不限风险等级或限定的等级不低于 outward。/api/rules 据此给每条标 elevated，导入、启用这类规则时单独审计。
func elevated(r policy.Rule) bool {
	return r.Source == policy.SourceUser && r.Action == policy.ActionAllow && (r.Match.Risk == nil || *r.Match.Risk >= policy.RiskOutward)
}

// rulesFitFrame 报告整套规则入库后编成 welcome 帧是否不超过单帧上限减 frameHeadroom。超过的规则集既下发不了，
// welcome 也编不出来（守护进程会一直连不上），所以导入与“记住”前先拒。
func rulesFitFrame(rs policy.RuleSet) bool {
	rules := make([]policy.Rule, len(rs.Rules))
	for i, r := range rs.Rules {
		e := r.IsEnabled() // 入库后读回的快照每条都带 enabled
		r.Enabled = &e
		rules[i] = r
	}
	y, err := policy.RuleSet{Version: rs.Version, Rules: rules}.Marshal()
	if err != nil {
		return false
	}
	b, err := wire.Marshal(wire.Welcome{T: wire.TWelcome, RulesVersion: math.MaxInt, RulesYAML: string(y), Paused: true, ServerTime: time.Now().UTC()})
	return err == nil && len(b) <= wire.MaxFrame-frameHeadroom
}

// setPaused 改全局总开关：写 settings 的 paused（"1"/"0"）、审计、向所有守护进程发 paused 帧、向网页广播 status。
// 写库与下发在 pauseMu 下按同一顺序进行，并发的暂停与恢复不会让守护进程最后收到的与库里的不一致。
func (s *Server) setPaused(p bool) error {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	v := "0"
	if p {
		v = "1"
	}
	if err := s.store.SetSetting("paused", v); err != nil {
		return err
	}
	s.audit("", "user", "", fmt.Sprintf("paused=%v", p), nil)
	if err := s.agents.broadcast(wire.Paused{T: wire.TPaused, Paused: p}); err != nil {
		s.log.Printf("下发总开关: %v", err)
	}
	s.clients.broadcast("status", map[string]bool{"paused": p})
	return nil
}
