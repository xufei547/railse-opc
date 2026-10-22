package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/wire"
	"github.com/railse/railse-opc/internal/proto"
	"github.com/railse/railse-opc/internal/redact"
)

var errBadAction = errors.New("action 只能是 allow 或 deny")

// errExpired 表示决定时请求已过 expires_at（hub 已把它改成 expired）。它也满足 errors.Is(err, store.ErrNotPending)。
var errExpired = fmt.Errorf("%w：已过期", store.ErrNotPending)

// 守护进程送来的字段在 hub 存储前的上限（按字符计，超出截断）。请求 id 为空或超长直接拒收、不截断（截断会变成另一个 id）。
const (
	maxRequestIDLen     = 128  // 请求 id 的字节上限
	maxFieldLen         = 256  // 工具名、风险、规则 id、会话 id、回合 id、decided_by 等短字段
	maxPathLen          = 4096 // 项目根、cwd
	maxTextLen          = 1000 // 引擎理由、审计里的理由与消息、用户备注
	maxTitleLen         = 500  // question/done 的标题
	maxDeadlineSeconds  = 86400
	maxClockSkew        = time.Minute // 守护进程报的时间比 hub 当前时间晚这么多以上就不信
	expireSweepInterval = time.Minute
)

// helloResendMax 是一次重连对齐最多补发的决定数，给发送队列留余量，免得“队列满断开 → 重连 → 再补发”循环。
// 是变量只为测试能调小。
var helloResendMax = agentQueueLen / 2

// DecideInput 是网页或 CLI 的一次决定。
type DecideInput struct {
	RequestID string
	Action    string
	Note      string
	Remember  bool
}

// machineLocks 给每台机器一把锁，让同一机器的重连对齐串行执行。零值可用。
type machineLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (l *machineLocks) lock(id string) (unlock func()) {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*sync.Mutex{}
	}
	mu := l.m[id]
	if mu == nil {
		mu = &sync.Mutex{}
		l.m[id] = mu
	}
	l.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}

// truncate 把 s 截到至多 n 个字符，不切断多字节字符。
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

func validRequestID(id string) bool { return id != "" && len(id) <= maxRequestIDLen }

// hubTime 是守护进程报来的时间在 hub 的取值：零值或比 hub 当前时间晚 maxClockSkew 以上时用 hub 当前时间。
func hubTime(t time.Time) time.Time {
	now := time.Now()
	if t.IsZero() || t.After(now.Add(maxClockSkew)) {
		return now
	}
	return t
}

// cleanSession 截断会话里来自守护进程的字段。
func cleanSession(sess proto.Session) proto.Session {
	sess.Tool = truncate(sess.Tool, maxFieldLen)
	sess.CLIVersion = truncate(sess.CLIVersion, maxFieldLen)
	sess.CLISessionID = truncate(sess.CLISessionID, maxFieldLen)
	sess.TurnID = truncate(sess.TurnID, maxFieldLen)
	sess.Cwd = truncate(sess.Cwd, maxPathLen)
	return sess
}

// cleanEntry 给守护进程的审计条目兜底：载荷再脱敏，理由与消息跑 redact.Text，各字段截断。
func cleanEntry(e agent.AuditEntry) agent.AuditEntry {
	e.RequestID = truncate(e.RequestID, maxRequestIDLen)
	e.Kind = truncate(e.Kind, maxFieldLen)
	e.Session = cleanSession(e.Session)
	e.ToolName = truncate(e.ToolName, maxFieldLen)
	if e.Payload != nil {
		p := redact.Payload(*e.Payload)
		e.Payload = &p
	}
	e.Risk = truncate(e.Risk, maxFieldLen)
	e.Action = truncate(e.Action, maxFieldLen)
	e.State = truncate(e.State, maxFieldLen)
	e.RuleID = truncate(e.RuleID, maxFieldLen)
	e.DecidedBy = truncate(e.DecidedBy, maxFieldLen)
	e.Reason = truncate(redact.Text(e.Reason), maxTextLen)
	e.Message = truncate(redact.Text(e.Message), maxTextLen)
	return e
}

func (s *Server) audit(machineID, actor, requestID, action string, detail any) {
	b, _ := json.Marshal(detail) // 调用方只传字符串与布尔组成的 map，编码不会失败
	if err := s.store.InsertAudit(store.AuditEvent{MachineID: machineID, Actor: actor, RequestID: requestID, Action: action, Detail: string(b)}); err != nil {
		s.log.Printf("写审计 %s: %v", action, err)
	}
}

// broadcastRequest 读出请求的当前行并广播 request_updated。
func (s *Server) broadcastRequest(id string) {
	r, err := s.store.GetRequest(id)
	if err != nil {
		s.log.Printf("读请求 %q: %v", clip(id), err)
		return
	}
	s.clients.broadcast("request_updated", r)
}

// settled 在一次状态迁移成功后写审计并广播。
func (s *Server) settled(machineID, actor, id, state, decidedBy string) {
	s.audit(machineID, actor, id, state, map[string]string{"decided_by": decidedBy})
	s.broadcastRequest(id)
}

func (s *Server) upsertSession(machineID string, sess proto.Session, project string) string {
	id, err := s.store.UpsertSession(machineID, sess, project)
	if err != nil {
		s.log.Printf("登记机器 %s 的会话: %v", machineID, err)
		return ""
	}
	return id
}

// rejectPending 回该机器一个带 request_id 的 Error 帧：守护进程据此把这条请求立即按 hub_unreachable 交回终端。
func (s *Server) rejectPending(machineID, requestID, msg string) {
	s.agents.send(machineID, wire.Error{T: wire.TError, RequestID: requestID, Message: msg})
}

// onPending 登记守护进程送来的待决请求。hub 不信任守护进程：id 无效或 Gemini 会话的拒收，
// 时间与期限按 hub 的时钟夹紧，载荷再脱敏，字段截断；同 id 已存在（重连后重发）只广播不重复插入。
func (s *Server) onPending(m store.Machine, p wire.PendingRequest) {
	if !validRequestID(p.RequestID) {
		s.log.Printf("机器 %s 送来的待决请求 id 无效（%d 字节），拒收", m.ID, len(p.RequestID))
		s.rejectPending(m.ID, p.RequestID, "登记失败")
		return
	}
	if strings.EqualFold(p.Session.Tool, "gemini") {
		s.log.Printf("机器 %s 送来 gemini 会话的待决请求 %q，拒收：gemini 请求不进 hub 等待", m.ID, p.RequestID)
		s.rejectPending(m.ID, p.RequestID, "gemini 请求不进 hub")
		return
	}
	existing, err := s.store.GetRequest(p.RequestID)
	switch {
	case err == nil:
		if existing.MachineID != m.ID {
			s.log.Printf("机器 %s 送来的待决请求 %q 与另一台机器的请求同 id，忽略", m.ID, p.RequestID)
			return
		}
		s.clients.broadcast("request_updated", existing) // 重连后重发，不重复插入
		return
	case !errors.Is(err, store.ErrNotFound):
		s.log.Printf("登记请求 %q 前查重: %v", p.RequestID, err)
		s.rejectPending(m.ID, p.RequestID, "登记失败")
		return
	}
	sess := cleanSession(p.Session)
	toolName := truncate(p.ToolName, maxFieldLen)
	payload := redact.Payload(p.Payload) // 守护进程已脱敏，hub 再跑一次兜底（幂等）
	hash := store.PayloadHash(payload)
	// 去重必须在插入新请求之前：窗口内同会话、同回合、同命令仍 pending 的旧请求标 cancelled/resend
	old, err := s.store.FindResend(m.ID, sess.CLISessionID, sess.TurnID, toolName, hash, s.cfg.ResendWindow)
	switch {
	case err == nil:
		err := s.store.SetRequestState(old.ID, "cancelled", store.DecidedByResend, "同一命令重新发起", "")
		if err == nil {
			s.audit(m.ID, "system", old.ID, "cancelled_resend", map[string]string{"replaced_by": p.RequestID})
			s.broadcastRequest(old.ID)
		} else if !errors.Is(err, store.ErrNotPending) {
			s.log.Printf("取消重发前的请求 %q: %v", old.ID, err)
		}
	case !errors.Is(err, store.ErrNotFound):
		s.log.Printf("查重发 %q: %v", p.RequestID, err)
	}
	root := truncate(p.ProjectRoot, maxPathLen)
	created := hubTime(p.CreatedAt)
	// DeadlineSeconds 是从 CreatedAt 起算的总时长
	deadline := min(max(p.DeadlineSeconds, 1), maxDeadlineSeconds)
	expires := store.FormatTime(created.Add(time.Duration(deadline) * time.Second))
	r := store.Request{ID: p.RequestID, MachineID: m.ID, SessionID: s.upsertSession(m.ID, sess, projectName(root)), Kind: "permission",
		ToolName: toolName, Payload: payload, PayloadHash: hash, Risk: truncate(p.Risk, maxFieldLen), ProjectRoot: root,
		EngineReason: truncate(redact.Text(p.EngineReason), maxTextLen), State: "pending", TurnID: sess.TurnID, CLISessionID: sess.CLISessionID,
		CreatedAt: store.FormatTime(created), ExpiresAt: &expires}
	if err := s.store.InsertRequest(r); err != nil {
		s.log.Printf("登记请求 %q: %v", p.RequestID, err)
		s.rejectPending(m.ID, p.RequestID, "登记失败")
		return
	}
	s.audit(m.ID, "agent", r.ID, "request_pending", map[string]string{"risk": r.Risk, "tool": r.ToolName})
	s.clients.broadcast("request_new", r)
}

func projectName(root string) string {
	if root == "" {
		return ""
	}
	for i := len(root) - 1; i >= 0; i-- {
		if root[i] == '/' {
			return root[i+1:]
		}
	}
	return root
}

// onCancel 处理钩子在决定前消失。原因只认 terminal、shutdown，其余一律记 terminal。
func (s *Server) onCancel(m store.Machine, c wire.Cancel) {
	reason, note := "terminal", "钩子进程在决定前退出"
	if c.Reason == "shutdown" {
		reason, note = "shutdown", "守护进程正在停止"
	}
	s.agentFinish(m, c.RequestID, "cancelled", reason, note)
}

func (s *Server) onExpired(m store.Machine, e wire.Expired) {
	s.agentFinish(m, e.RequestID, "expired", "timeout", "守护进程等待过期")
}

// agentFinish 按守护进程的 cancel/expired 帧结束该机器的一条 pending；不属于该机器或已不是 pending 的忽略。
func (s *Server) agentFinish(m store.Machine, id, state, decidedBy, note string) {
	err := s.store.SetMachineRequestState(m.ID, id, state, decidedBy, note, "")
	if errors.Is(err, store.ErrNotPending) {
		return
	}
	if err != nil {
		s.log.Printf("更新请求 %q: %v", clip(id), err)
		return
	}
	s.settled(m.ID, "agent", id, state, decidedBy)
}

// onDelivered 记录决定已交给钩子。只认该机器、已决定未送达的请求（见 store.MarkDelivered）。
func (s *Server) onDelivered(m store.Machine, d wire.Delivered) {
	if err := s.store.MarkDelivered(m.ID, d.RequestID); err != nil {
		s.log.Printf("标记送达 %q: %v", clip(d.RequestID), err)
	}
}

// onAgentHello 用守护进程 hello 里的 pending 列表对齐：hub 多出来的 pending 取消（agent_restart）；已决定未送达的，
// 守护进程仍持有的补发（至多 helloResendMax 条），不再持有的记一次 undeliverable。
// 同一机器的对齐串行执行；c 已不是该机器的当前连接（被替换或已断开）时不执行，旧连接的对齐不会覆盖新连接的。
func (s *Server) onAgentHello(c *agentConn, h wire.Hello) {
	m := c.machine
	unlock := s.helloLocks.lock(m.ID)
	defer unlock()
	if !s.agents.current(c) {
		return
	}
	held := make(map[string]bool, len(h.Pending))
	for _, p := range h.Pending {
		held[p.RequestID] = true
	}
	pend, err := s.store.ListPendingForMachine(m.ID)
	if err != nil {
		s.log.Printf("对齐机器 %s：列待决请求: %v", m.ID, err)
	}
	for _, r := range pend {
		if held[r.ID] {
			continue
		}
		err := s.store.SetMachineRequestState(m.ID, r.ID, "cancelled", store.DecidedByAgentRestart, "守护进程重连后不再持有该请求", "")
		if errors.Is(err, store.ErrNotPending) {
			continue
		}
		if err != nil {
			s.log.Printf("对齐机器 %s：取消请求 %s: %v", m.ID, r.ID, err)
			continue
		}
		s.settled(m.ID, "system", r.ID, "cancelled", store.DecidedByAgentRestart)
	}
	und, err := s.store.UndeliveredDecisions(m.ID)
	if err != nil {
		s.log.Printf("对齐机器 %s：列未送达的决定: %v", m.ID, err)
		return
	}
	resent, skipped := 0, 0
	for _, r := range und {
		switch {
		case !held[r.ID]:
			s.markUndeliverable(r)
		case resent >= helloResendMax:
			skipped++
		default:
			s.sendDecision(r) // 入队不等于送达：送达只认 delivered 回执
			resent++
		}
	}
	if skipped > 0 {
		s.log.Printf("机器 %s 重连时有 %d 条已决定未送达的请求超过补发上限 %d，留待下次重连补发", m.ID, skipped, helloResendMax)
	}
}

// markUndeliverable 给已决定、但守护进程已不再持有的请求记一次 undeliverable 审计（同一请求只记一次）并广播，
// 网页据此标出“决定未送达”；不标送达。
func (s *Server) markUndeliverable(r store.Request) {
	events, err := s.store.ListAudit(0, r.ID)
	if err != nil {
		s.log.Printf("读请求 %s 的审计: %v", r.ID, err)
		return
	}
	for _, e := range events {
		if e.Action == "undeliverable" {
			return
		}
	}
	s.audit(r.MachineID, "system", r.ID, "undeliverable", map[string]string{"state": r.State})
	s.clients.broadcast("request_updated", r)
}

// sendDecision 把已决定的请求排进守护进程的发送队列。返回 true 只表示入队，不表示送达。
func (s *Server) sendDecision(r store.Request) bool {
	action := "deny"
	if r.State == "approved" {
		action = "allow"
	}
	return s.agents.send(r.MachineID, wire.Decision{T: wire.TDecision, RequestID: r.ID, Action: action, Reason: r.DecisionNote, RuleID: r.RuleID, DecidedBy: r.DecidedBy})
}

// Decide 是用户的决定入口（网页或 CLI）：只能 allow 或 deny，请求必须仍 pending 且未过期。
// Remember 时先学规则（失败则请求保持 pending，错误带“生成规则”前缀），再改状态、审计、下发（未连接时留给重连对齐补发）、广播。
// 错误：errBadAction、store.ErrNotFound、store.ErrNotPending（已过期时为 errExpired，也满足 errors.Is(err, store.ErrNotPending)）。
func (s *Server) Decide(in DecideInput) (store.Request, error) {
	if in.Action != "allow" && in.Action != "deny" {
		return store.Request{}, errBadAction
	}
	note := truncate(in.Note, maxTextLen)
	state := "denied"
	if in.Action == "allow" {
		state = "approved"
	}
	r, ruleID, err := s.commitDecision(in, state, note)
	if errors.Is(err, errExpired) {
		s.settled(r.MachineID, "system", r.ID, "expired", "timeout")
		return store.Request{}, err
	}
	if err != nil {
		return store.Request{}, err
	}
	if fresh, err := s.store.GetRequest(r.ID); err == nil {
		r = fresh
	} else {
		// 决定已落库，读回失败也照常下发与广播
		s.log.Printf("读回请求 %s: %v", r.ID, err)
		now := store.Now()
		r.State, r.DecidedBy, r.DecisionNote, r.RuleID, r.DecidedAt = state, "user", note, ruleID, &now
	}
	s.audit(r.MachineID, "user", r.ID, in.Action, map[string]any{"note": note, "remember": in.Remember, "rule_id": ruleID})
	if r.Kind == "webhook" {
		s.callbackWebhook(r)
	} else {
		s.sendDecision(r) // 未连接时留给重连对齐补发
	}
	s.clients.broadcast("request_updated", r)
	return r, nil
}

// commitDecision 在 decideMu 下完成“取请求 → 校验 pending 与过期 → 学规则 → 改状态”，并发的两次决定不会都去学规则。
// 已过期时把请求改成 expired/timeout，返回请求与 errExpired，由调用方审计并广播。
func (s *Server) commitDecision(in DecideInput, state, note string) (store.Request, string, error) {
	s.decideMu.Lock()
	defer s.decideMu.Unlock()
	r, err := s.store.GetRequest(in.RequestID)
	if err != nil {
		return store.Request{}, "", err
	}
	if r.State != "pending" {
		return store.Request{}, "", store.ErrNotPending
	}
	if r.ExpiresAt != nil && *r.ExpiresAt <= store.FormatTime(time.Now()) {
		if err := s.store.SetRequestState(r.ID, "expired", "timeout", "hub 判定过期", ""); err != nil {
			return store.Request{}, "", err
		}
		return r, "", errExpired
	}
	ruleID := ""
	if in.Remember {
		id, err := s.learnRule(r, in.Action)
		if err != nil {
			return store.Request{}, "", fmt.Errorf("生成规则: %w", err)
		}
		ruleID = id
	}
	if err := s.store.SetRequestState(r.ID, state, "user", note, ruleID); err != nil {
		return store.Request{}, "", err
	}
	return r, ruleID, nil
}

// expireDue 把 expires_at 已到的 pending 改成 expired/timeout，审计并广播：hub 自己执行过期，
// 守护进程掉线或崩溃时 pending 也不会一直挂着。
func (s *Server) expireDue(now time.Time) {
	due, err := s.store.ExpiredPending(now)
	if err != nil {
		s.log.Printf("查过期请求: %v", err)
		return
	}
	for _, r := range due {
		err := s.store.SetRequestState(r.ID, "expired", "timeout", "hub 判定过期", "")
		if errors.Is(err, store.ErrNotPending) {
			continue
		}
		if err != nil {
			s.log.Printf("过期请求 %s: %v", r.ID, err)
			continue
		}
		s.settled(r.MachineID, "system", r.ID, "expired", "timeout")
	}
}

// expireLoop 启动时扫一次，之后每 expireSweepInterval 扫一次，ctx 结束时返回。
func (s *Server) expireLoop(ctx context.Context) {
	s.expireDue(time.Now())
	t := time.NewTicker(expireSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.expireDue(now)
		}
	}
}

// onAudit 收守护进程的审计批次：每条兜底脱敏后入库；question/done 存成 recorded 行；permission 的本机终态按白名单更新 hub。
func (s *Server) onAudit(m store.Machine, a wire.Audit) {
	bad := 0
	for _, raw := range a.Entries {
		var e agent.AuditEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			bad++
			continue
		}
		idOK := validRequestID(e.RequestID)
		e = cleanEntry(e)
		ts := hubTime(e.TS)
		// detail 用解码后的结构重新编码：未知字段（如钩子进程的环境变量）被丢弃，载荷与文本已再脱敏
		detail, err := json.Marshal(e)
		if err != nil {
			bad++
			continue
		}
		if err := s.store.InsertAudit(store.AuditEvent{TS: store.FormatTime(ts), MachineID: m.ID, Actor: "agent", RequestID: e.RequestID, Action: e.State, Detail: string(detail)}); err != nil {
			s.log.Printf("写机器 %s 的审计: %v", m.ID, err)
		}
		switch {
		case (e.Kind == "question" || e.Kind == "done") && e.State == "recorded":
			s.recordEvent(m, e, ts)
		case e.Kind == "permission" && idOK:
			s.applyAgentPermission(m, e, ts)
		}
	}
	if bad > 0 {
		s.log.Printf("机器 %s 的审计批次里 %d 条无法解析，已跳过", m.ID, bad)
	}
}

// recordEvent 把 question/done 存成一行 recorded（第 4 份的收件箱靠它列出谁在等回答），刷新会话并广播 request_new。
// e 已经过 cleanEntry：Message 已脱敏。
func (s *Server) recordEvent(m store.Machine, e agent.AuditEntry, ts time.Time) {
	r := store.Request{ID: store.NewID(), MachineID: m.ID, SessionID: s.upsertSession(m.ID, e.Session, ""), Kind: e.Kind, ToolName: e.ToolName,
		State: "recorded", Title: truncate(e.Message, maxTitleLen), TurnID: e.Session.TurnID, CLISessionID: e.Session.CLISessionID, CreatedAt: store.FormatTime(ts)}
	if err := s.store.InsertRequest(r); err != nil {
		s.log.Printf("记机器 %s 的 %s 事件: %v", m.ID, e.Kind, err)
		return
	}
	s.clients.broadcast("request_new", r)
}

// applyAgentPermission 按白名单用守护进程审计里的 permission 条目更新 hub。本机对钩子发生了什么是唯一真相：
// expired、cancelled、passthrough 在 hub 有该请求时覆盖（ApplyAgentFinal 只改 pending 或重连对齐标的 cancelled/agent_restart），
// 没有时补历史行；auto_allowed、auto_denied 只在 hub 没有该请求时补历史行并记规则命中；其余状态不改 hub。
func (s *Server) applyAgentPermission(m store.Machine, e agent.AuditEntry, ts time.Time) {
	final := e.State == "expired" || e.State == "cancelled" || e.State == "passthrough"
	auto := e.State == "auto_allowed" || e.State == "auto_denied"
	if !final && !auto {
		return
	}
	_, err := s.store.GetRequest(e.RequestID)
	switch {
	case err == nil:
		if !final {
			return
		}
		updated, err := s.store.ApplyAgentFinal(m.ID, e.RequestID, e.State, e.DecidedBy, e.Reason, e.RuleID)
		if err != nil {
			s.log.Printf("以本机终态更新请求 %s: %v", e.RequestID, err)
			return
		}
		if updated {
			s.settled(m.ID, "agent", e.RequestID, e.State, e.DecidedBy)
		}
		return
	case !errors.Is(err, store.ErrNotFound):
		s.log.Printf("读请求 %s: %v", e.RequestID, err)
		return
	}
	created := store.FormatTime(ts)
	r := store.Request{ID: e.RequestID, MachineID: m.ID, SessionID: s.upsertSession(m.ID, e.Session, ""), Kind: "permission", ToolName: e.ToolName,
		Risk: e.Risk, EngineReason: e.Reason, State: e.State, DecidedBy: e.DecidedBy, RuleID: e.RuleID, TurnID: e.Session.TurnID,
		CLISessionID: e.Session.CLISessionID, CreatedAt: created, DecidedAt: &created}
	if e.Payload != nil {
		r.Payload = *e.Payload
	}
	if err := s.store.InsertRequest(r); err != nil {
		s.log.Printf("补历史行 %s: %v", e.RequestID, err)
		return
	}
	if auto && e.RuleID != "" {
		if err := s.store.RecordHit(e.RuleID); err != nil {
			s.log.Printf("记规则 %s 命中: %v", e.RuleID, err)
		}
	}
}

// learnRule 与 callbackWebhook 在 Task 9、10 实现。
func (s *Server) learnRule(r store.Request, action string) (string, error) { return "", nil }
func (s *Server) callbackWebhook(r store.Request)                          {}
