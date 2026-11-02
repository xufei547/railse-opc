package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/xufei547/railse-opc/internal/proto"
)

// trusts 是本机的限时放行表：守护进程是执行者，hub 只是展示与远程控制。内存里一份，改动即原子落盘到 trusts.json。
// 过期的条目只在 sweep 时删掉（调用方据此写 trust_expired 审计），其余读路径（list、match、add 的计数）只是跳过它们；
// 启动时读回的过期条目也留到第一次 sweep，停机期间到期的同样有审计。
// synced 记哪些条目 hub 已经收下（见 applySnapshot：已同步的条目不在 hub 快照里，就是被手机撤了）；tomb 是墓碑：本机撤掉、
// hub 可能还没收下的 id → 原到期时间，hub 快照里还带着它们时不复活、要重发撤销。两者都随表落盘，守护进程重启后照样认。
type trusts struct {
	mu     sync.Mutex
	path   string
	now    func() time.Time
	items  map[string]proto.Trust
	synced map[string]bool
	tomb   map[string]time.Time
}

// trustRecord 是 trusts.json 的一项：放行本身，加 hub 是否已收下（synced）。revoked 为真的是墓碑，只有 id 与到期时间，不生效。
type trustRecord struct {
	proto.Trust
	Synced  bool `json:"synced,omitempty"`
	Revoked bool `json:"revoked,omitempty"`
}

// 管理命令回给 railse trust 的错误。都是固定句子：命令行按终端语言用 i18n.Lookup 翻（英文在 internal/i18n/en.go），改了那边要一起改。
var (
	errTrustScope    = errors.New("放行范围只能是 session 或 project")
	errTrustTarget   = errors.New("session 范围要有 tool 与 cli_session_id，project 范围要有项目根")
	errTrustTooMany  = errors.New("同时生效的放行至多 20 条，先撤掉一些")
	errTrustTooShort = errors.New("放行时长至少 5 分钟")
	errTrustFormat   = errors.New("时长格式不对（如 30m、2h、8h）")
)

// newTrusts 读回 path 里的放行表（不存在按空表）。读不了或损坏时返回空表与错误，调用方记日志后照常用这张空表。
func newTrusts(path string, now func() time.Time) (*trusts, error) {
	t := &trusts{path: path, now: now, items: map[string]proto.Trust{}, synced: map[string]bool{}, tomb: map[string]time.Time{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, fmt.Errorf("读放行表 %s: %w", path, err)
	}
	var list []trustRecord
	if err := json.Unmarshal(b, &list); err != nil {
		return t, fmt.Errorf("放行表 %s 损坏，按空表处理: %w", path, err)
	}
	for _, r := range list {
		switch {
		case r.ID == "":
		case r.Revoked:
			t.tomb[r.ID] = r.Until
		default:
			t.items[r.ID] = r.Trust
			if r.Synced {
				t.synced[r.ID] = true
			}
		}
	}
	return t, nil
}

// ParseTrustFor 解析时长原文（Go 的 time.ParseDuration 写法）：空为默认 2 小时；超界截到 5 分钟～24 小时；格式不对或不是正数报错。
func ParseTrustFor(s string) (time.Duration, error) {
	if s == "" {
		return proto.TrustDefault, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, errTrustFormat
	}
	return clampTrust(d), nil
}

func clampTrust(d time.Duration) time.Duration {
	if d < proto.TrustMin {
		return proto.TrustMin
	}
	if d > proto.TrustMax {
		return proto.TrustMax
	}
	return d
}

// add 校验并登记：补 ID 与 CreatedAt，Until 短于 5 分钟（留一分钟余量）报错、长于 24 小时截断，生效中的已满 20 条报错（同 ID 覆盖不算新增）。
// 落盘失败返回错误，但条目已在内存里生效。
func (t *trusts) add(tr proto.Trust) (proto.Trust, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if err := checkTrustTarget(tr); err != nil {
		return proto.Trust{}, err
	}
	// 下限留 trustMinGrace 的余量：调用方（命令行、手机经 hub、RAILSE_TRUST）按“此刻 + 时长”算 Until，送到这里已过去一点时间
	if tr.Until.Before(now.Add(proto.TrustMin - trustMinGrace)) {
		return proto.Trust{}, errTrustTooShort
	}
	if tr.Until.After(now.Add(proto.TrustMax)) {
		tr.Until = now.Add(proto.TrustMax)
	}
	if old, exists := t.items[tr.ID]; (!exists || !old.Until.After(now)) && t.liveLocked(now) >= proto.TrustPerMachine {
		return proto.Trust{}, errTrustTooMany
	}
	if tr.ID == "" {
		tr.ID = newTrustID()
	}
	if tr.CreatedAt.IsZero() {
		tr.CreatedAt = now
	}
	t.items[tr.ID] = tr
	return tr, t.saveLocked()
}

// checkTrustTarget 校验范围与目标：session 要有 tool 与 cli_session_id，project 要有项目根。
func checkTrustTarget(tr proto.Trust) error {
	switch tr.Scope {
	case proto.TrustScopeSession:
		if tr.Tool == "" || tr.CLISession == "" {
			return errTrustTarget
		}
	case proto.TrustScopeProject:
		if tr.Project == "" {
			return errTrustTarget
		}
	default:
		return errTrustScope
	}
	return nil
}

// newTrustID 与 hub 用同一个生成器（proto.NewTrustID），两边建的 id 一样长。
func newTrustID() string { return proto.NewTrustID() }

// liveLocked 是生效中（未过期）的条数。
func (t *trusts) liveLocked(now time.Time) int {
	n := 0
	for _, tr := range t.items {
		if tr.Until.After(now) {
			n++
		}
	}
	return n
}

// revoke 撤掉一条，"all" 撤全部；返回撤掉的（生效中的与已过期未清的都算）。撤掉的留墓碑到原到期时间（见 applySnapshot）。
// 落盘失败只能等下次改动再写，不影响内存里已撤。
func (t *trusts) revoke(id string) []proto.Trust {
	t.mu.Lock()
	defer t.mu.Unlock()
	var gone []proto.Trust
	for k, tr := range t.items {
		if id == "all" || k == id {
			gone = append(gone, tr)
			delete(t.items, k)
			delete(t.synced, k)
			t.tomb[k] = tr.Until
		}
	}
	if len(gone) > 0 {
		_ = t.saveLocked()
	}
	sortByUntil(gone)
	return gone
}

// list 返回生效中的全部，按到期先后。
func (t *trusts) list() []proto.Trust {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	out := make([]proto.Trust, 0, len(t.items))
	for _, tr := range t.items {
		if tr.Until.After(now) {
			out = append(out, tr)
		}
	}
	sortByUntil(out)
	return out
}

// hasSession 报告这个会话有没有生效中的会话放行。
func (t *trusts) hasSession(tool, cliSession string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	for _, tr := range t.items {
		if tr.Scope == proto.TrustScopeSession && tr.Tool == tool && tr.CLISession == cliSession && tr.Until.After(now) {
			return true
		}
	}
	return false
}

// match 找命中的放行：session 范围按 Tool + CLISessionID，project 范围按项目根字符串相等（空项目根不命中）；多条取 Until 最晚。
func (t *trusts) match(sess proto.Session, projectRoot string) (proto.Trust, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	var best proto.Trust
	found := false
	for _, tr := range t.items {
		if !tr.Until.After(now) {
			continue
		}
		hit := (tr.Scope == proto.TrustScopeSession && sess.CLISessionID != "" && tr.Tool == sess.Tool && tr.CLISession == sess.CLISessionID) ||
			(tr.Scope == proto.TrustScopeProject && projectRoot != "" && tr.Project == projectRoot)
		if hit && (!found || tr.Until.After(best.Until)) {
			best, found = tr, true
		}
	}
	return best, found
}

// clock 是放行表用的此刻（测试可注入）。
func (t *trusts) clock() time.Time {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.now()
}

// sweep 清掉过期的并返回它们（按到期先后），调用方写审计；过了原到期时间的墓碑一并清掉（hub 那边也已到期）。
func (t *trusts) sweep() []proto.Trust {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	var gone []proto.Trust
	for id, tr := range t.items {
		if !tr.Until.After(now) {
			gone = append(gone, tr)
			delete(t.items, id)
			delete(t.synced, id)
		}
	}
	pruned := false
	for id, until := range t.tomb {
		if !until.After(now) {
			delete(t.tomb, id)
			pruned = true
		}
	}
	if len(gone) > 0 || pruned {
		_ = t.saveLocked()
	}
	sortByUntil(gone)
	return gone
}

// unsynced 返回生效中、hub 还没收下的（本机建的，或 hub 不认得放行时建的），按到期先后。
func (t *trusts) unsynced() []proto.Trust {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	var out []proto.Trust
	for id, tr := range t.items {
		if !t.synced[id] && tr.Until.After(now) {
			out = append(out, tr)
		}
	}
	sortByUntil(out)
	return out
}

// applySnapshot 按 hub 的放行快照合并（设计 §2.5 的精确化）：
//   - 快照里有的写入/覆盖本机表并标已同步；已过期的、范围或目标不合规的不收，生效中的已满 20 条时不收新的；
//   - 快照里有、本机留着墓碑的（本机撤了、hub 还没收下）不复活，放进 resend，调用方重发撤销；
//   - complete 为真时，本机已同步、还没过期却不在快照里的删掉（被手机撤了），放进 revoked，调用方写审计；
//     不在快照里的墓碑清掉（hub 已收下撤销）。complete 为假（快照可能不完整）时这两步都不做。
//
// 本机未同步的条目一律保留，由调用方补发；已过期的留给 sweep（审计记到期，不记被撤）。
func (t *trusts) applySnapshot(snap []proto.Trust, complete bool) (revoked []proto.Trust, resend []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	in := make(map[string]bool, len(snap))
	changed := false
	for _, tr := range snap {
		if tr.ID == "" {
			continue
		}
		in[tr.ID] = true
		if _, dead := t.tomb[tr.ID]; dead {
			resend = append(resend, tr.ID)
			continue
		}
		if !tr.Until.After(now) || checkTrustTarget(tr) != nil {
			continue
		}
		if limit := now.Add(proto.TrustMax); tr.Until.After(limit) {
			tr.Until = limit
		}
		if _, exists := t.items[tr.ID]; !exists && t.liveLocked(now) >= proto.TrustPerMachine {
			continue
		}
		tr.MachineID = "" // 机器不言自明，与本机建的一样不存
		t.items[tr.ID] = tr
		t.synced[tr.ID] = true
		changed = true
	}
	if complete {
		for id, tr := range t.items {
			if t.synced[id] && !in[id] && tr.Until.After(now) {
				revoked = append(revoked, tr)
				delete(t.items, id)
				delete(t.synced, id)
				changed = true
			}
		}
		for id := range t.tomb {
			if !in[id] {
				delete(t.tomb, id)
				changed = true
			}
		}
	}
	if changed {
		_ = t.saveLocked() // 落盘失败只能等下次改动再写，内存里已生效
	}
	sortByUntil(revoked)
	sort.Strings(resend)
	return revoked, resend
}

func sortByUntil(l []proto.Trust) {
	sort.Slice(l, func(i, j int) bool { return l[i].Until.Before(l[j].Until) })
}

// saveLocked 把全表（连同已同步标记与墓碑）按建立先后原子写进 trusts.json（0600）；墓碑没有建立时间，排在最前。
func (t *trusts) saveLocked() error {
	list := make([]trustRecord, 0, len(t.items)+len(t.tomb))
	for id, tr := range t.items {
		list = append(list, trustRecord{Trust: tr, Synced: t.synced[id]})
	}
	for id, until := range t.tomb {
		list = append(list, trustRecord{Trust: proto.Trust{ID: id, Until: until}, Revoked: true})
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return writeFileAtomic(t.path, b)
}

// trustMinGrace 是时长下限的余量，见 add。
const trustMinGrace = time.Minute

// trustSweepEvery 是到期清理的间隔；测试里改短。
var trustSweepEvery = time.Minute

// reasonTrusted 是放行命中时回给钩子的理由（Claude 里显示成 "Railse：限时放行中"）。固定句子：钩子按终端语言 i18n.Lookup，
// 范围与剩余时长只写进审计。
const reasonTrusted = "限时放行中"

// trustScopeLabel 是审计里的范围说明：会话 <tool>，或项目 <项目目录名>。
func trustScopeLabel(tr proto.Trust) string {
	if tr.Scope == proto.TrustScopeSession {
		return "会话 " + tr.Tool
	}
	return "项目 " + filepath.Base(tr.Project)
}

// trustAuditReason 是建、撤、到期审计的理由：范围、到期时间（本地时间）、谁建的。
func trustAuditReason(tr proto.Trust) string {
	return fmt.Sprintf("%s，到 %s，%s 建", trustScopeLabel(tr), tr.Until.Local().Format("01-02 15:04"), tr.CreatedBy)
}

// formatRemain 把剩余时长写成“1 小时 5 分”或“12 分钟”（不足一分钟按一分钟）。
func formatRemain(d time.Duration) string {
	m := int((d + time.Minute - 1) / time.Minute)
	if m < 1 {
		m = 1
	}
	if m < 60 {
		return fmt.Sprintf("%d 分钟", m)
	}
	if m%60 == 0 {
		return fmt.Sprintf("%d 小时", m/60)
	}
	return fmt.Sprintf("%d 小时 %d 分", m/60, m%60)
}

// maxEnvSeen 与 envSeenTTL：RAILSE_TRUST 只在第一次见到某会话时建放行，见过的会话记在内存里（守护进程重启会忘）。
// 超过 envSeenTTL 的记录在新增时清掉（放行最长 24 小时，留足余量）；记满 maxEnvSeen 时不再为新会话建放行，只记日志。
const (
	maxEnvSeen = 4096
	envSeenTTL = 7 * 24 * time.Hour
)

// trustFromEnv 处理钩子带来的 RAILSE_TRUST：这个会话第一次带它时建一条 session 范围、created_by env 的放行（起算是此刻），
// 之后同一会话的请求不再看它（过期或被撤后也不重建）。格式不对只记日志。
func (s *Server) trustFromEnv(req proto.Request) {
	if req.Session.CLISessionID == "" {
		return
	}
	key := req.Session.Tool + "\x00" + req.Session.CLISessionID
	now := time.Now()
	s.envMu.Lock()
	if _, seen := s.envSeen[key]; seen {
		s.envMu.Unlock()
		return
	}
	for k, at := range s.envSeen {
		if now.Sub(at) > envSeenTTL {
			delete(s.envSeen, k)
		}
	}
	if len(s.envSeen) >= maxEnvSeen {
		s.envMu.Unlock()
		s.log.Printf("RAILSE_TRUST 记住的会话已满 %d 个，不再为会话 %q 建放行", maxEnvSeen, clipText(req.Session.CLISessionID, hubLogFieldMax))
		return
	}
	if s.envSeen == nil {
		s.envSeen = map[string]time.Time{}
	}
	s.envSeen[key] = now
	s.envMu.Unlock()

	d, err := ParseTrustFor(req.TrustFor)
	if err != nil {
		s.log.Printf("会话 %q 的 RAILSE_TRUST=%q 不认识，不放行: %v", clipText(req.Session.CLISessionID, hubLogFieldMax), clipText(req.TrustFor, hubLogFieldMax), err)
		return
	}
	if s.trusts.hasSession(req.Session.Tool, req.Session.CLISessionID) {
		return
	}
	tr, err := s.trusts.add(proto.Trust{Scope: proto.TrustScopeSession, Tool: req.Session.Tool, CLISession: req.Session.CLISessionID,
		Until: s.trusts.clock().Add(d), CreatedBy: "env"})
	if err != nil {
		s.log.Printf("按 RAILSE_TRUST 建会话放行: %v", err)
		if tr.ID == "" {
			return
		}
	}
	s.trustEvent("trust_set", tr)
}

// trustEvent 为一条放行的建（trust_set）、撤（trust_revoke）、到期（trust_expired）写 control 审计，并通知 trustChanged（若装了）。
func (s *Server) trustEvent(msg string, tr proto.Trust) {
	s.writeAudit(AuditEntry{TS: time.Now(), Kind: "control", State: "recorded", Message: msg, Reason: trustAuditReason(tr), DecidedBy: tr.CreatedBy})
	if fn := s.trustChanged.Load(); fn != nil {
		op := map[string]string{"trust_set": "set", "trust_revoke": "revoke", "trust_expired": "expired"}[msg]
		(*fn)(tr, op)
	}
}

// applyHubTrusts 合并 hub 的放行快照（见 trusts.applySnapshot）：被手机撤掉的逐条写 trust_revoke 审计（decided_by hub），
// 不通知 trustChanged（hub 那边已经撤了）。返回仍要报给 hub 的：本机撤了、hub 还带着的 id，与本机未同步的条目。
// 写审计会同步调 AuditSink.Forward，调用方不得持着它用到的锁。
func (s *Server) applyHubTrusts(snap []proto.Trust, complete bool) (revokes []string, unsynced []proto.Trust) {
	gone, revokes := s.trusts.applySnapshot(snap, complete)
	for _, tr := range gone {
		s.writeAudit(AuditEntry{TS: time.Now(), Kind: "control", State: "recorded", Message: "trust_revoke", Reason: trustAuditReason(tr), DecidedBy: "hub"})
	}
	return revokes, s.trusts.unsynced()
}

// SetTrustChanged 装上放行变更的回调（hub 客户端用来发 trust 帧）：op 为 set、revoke、expired。nil 表示不通知。可随时调用；
// fn 在处理连接或到期清理的 goroutine 里同步调用，必须不阻塞，也不得持着会被写审计（AuditSink.Forward）用到的锁。
func (s *Server) SetTrustChanged(fn func(tr proto.Trust, op string)) {
	if fn == nil {
		s.trustChanged.Store(nil)
		return
	}
	s.trustChanged.Store(&fn)
}

// sweepTrusts 每隔 trustSweepEvery 清一次过期的放行（启动时先清一次，停机期间到期的也有审计），ctx 结束时返回。
func (s *Server) sweepTrusts(ctx context.Context) {
	every := trustSweepEvery
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		for _, tr := range s.trusts.sweep() {
			s.trustEvent("trust_expired", tr)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
