package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/xufei547/railse-opc/internal/adapter"
	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
	"github.com/xufei547/railse-opc/internal/redact"
)

var errBadAction = errors.New("action 只能是 allow 或 deny")

// wrongKind 是对请求用错了接口的错误：文案说明该怎么办，errors.Is 认作 errBadAction（接口回 400）。
type wrongKind string

func (e wrongKind) Error() string        { return string(e) }
func (e wrongKind) Is(target error) bool { return target == errBadAction }

var (
	// errIsAsk 表示对提问（kind ask）调了 decide 或 propose：提问只能作答，不能批准、拒绝或“记住”。
	errIsAsk = wrongKind("这是提问，用答题接口")
	// errNotAsk 表示对不是提问的请求作答。
	errNotAsk = wrongKind("这不是提问，不能作答")
	// errBeyondRole 表示对超出角色边界的请求批准时要“记住”或限时放行（0.3 起）：越界的只能单次批准，规则与放行都不适用。
	errBeyondRole = wrongKind("超出角色的边界：只能单次批准，不能记住，也不能限时放行")
)

// errBadAnswer 表示答案不合规（缺题、空、超长、多余的键），具体是哪道题包在它后面。
var errBadAnswer = errors.New("答案不合规")

// errBadScope 表示“记住”的范围不是 project、global 或空。
var errBadScope = errors.New("scope 只能是 project 或 global")

// errExpired 表示决定时请求已过 expires_at（hub 已把它改成 expired）。它也满足 errors.Is(err, store.ErrNotPending)。
var errExpired = fmt.Errorf("%w：已过期", store.ErrNotPending)

// errRevoked 表示决定时请求所属机器已吊销（hub 已把它改成 cancelled/machine_revoked）。它也满足 errors.Is(err, store.ErrNotPending)。
var errRevoked = fmt.Errorf("%w：机器已吊销", store.ErrNotPending)

// 守护进程送来的字段在 hub 存储前的上限（按字符计，超出截断）。请求 id 不合规（见 validRequestID）直接拒收、不截断
// （截断会变成另一个 id）。
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
// 截下的决定在守护进程重发该请求的 pending 时随之再发（见 pendingAgain），或在下次重连时补发。是变量只为测试能调小。
var helloResendMax = agentQueueLen / 2

// DecideInput 是网页或 CLI 的一次决定。
type DecideInput struct {
	RequestID string
	Action    string
	Note      string
	Remember  bool
	// Scope 是“记住”的范围：project、global，或空（有项目根记成项目，没有记成全局）。只在 Remember 时起作用，
	// 但不认识的取值无论记不记住都回 errBadScope。
	Scope string
	// Trust 非空时批准顺带限时放行（见 Decide）。
	Trust *TrustInput
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

// validRequestID 要求请求 id 非空、不超过 maxRequestIDLen 字节、只含 [A-Za-z0-9_-]。
// 守护进程用 agent.NewID 生成 32 个十六进制字符，满足此限制；这样 id 进日志、审计时不会夹带控制字符或伪造的行。
func validRequestID(id string) bool {
	if id == "" || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		idChar := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
		if !idChar {
			return false
		}
	}
	return true
}

// hubTime 是守护进程报来的时间在 hub 的取值：零值或比 hub 当前时间晚 maxClockSkew 以上时用 hub 当前时间。
func hubTime(t time.Time) time.Time {
	now := time.Now()
	if t.IsZero() || t.After(now.Add(maxClockSkew)) {
		return now
	}
	return t
}

// redactText 先对全文脱敏，再截到至多 n 个字符。不能先截再脱敏：截断会让引号括起的长秘密丢了右引号而匹配不上，
// 脱敏后文本变短又会把截断点之后的半截凭据拉进保留部分。全文脱敏的开销受单帧 1 MiB 上限约束（约百毫秒量级）。
func redactText(s string, n int) string { return truncate(redact.Text(s), n) }

// cleanSession 截断会话里来自守护进程的字段。
func cleanSession(sess proto.Session) proto.Session {
	sess.Tool = truncate(sess.Tool, maxFieldLen)
	sess.CLIVersion = truncate(sess.CLIVersion, maxFieldLen)
	sess.CLISessionID = truncate(sess.CLISessionID, maxFieldLen)
	sess.TurnID = truncate(sess.TurnID, maxFieldLen)
	sess.Cwd = truncate(sess.Cwd, maxPathLen)
	sess.Role = truncate(sess.Role, maxFieldLen) // 记进 hub 前还要过 sessionRole
	return sess
}

// maxAskTextLen 是审计条目里题目各段文字（题面、标题、选项）的字符上限，取 AskUserQuestion 单段文字的最大上限。
// 只用于审计与补的历史行（展示用，可以截断）；守护进程送来待答的题目 hub 不截断（见 checkAsk）。
const maxAskTextLen = 2000

// cleanEntry 给守护进程的审计条目兜底：载荷与题目再脱敏，理由、消息、备注与答案跑 redact.Text（先全文脱敏再截断），各字段截断。
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
	e.Reason = redactText(e.Reason, maxTextLen)
	e.Trigger = cleanTrigger(e.Trigger)
	e.Message = redactText(e.Message, maxTextLen)
	e.Note = redactText(e.Note, maxTextLen) // 与 Decide 的备注上限一致
	e.Ask = e.Ask.MapText(func(s string) string { return redactText(s, maxAskTextLen) })
	if e.Answers != nil {
		answers := make(map[string]string, len(e.Answers))
		for q, a := range e.Answers {
			answers[redactText(q, maxAskTextLen)] = redactText(a, proto.AnswerMax)
		}
		e.Answers = answers
	}
	return e
}

func (s *Server) audit(machineID, actor, requestID, action string, detail any) {
	b, _ := json.Marshal(detail) // 调用方只传 nil，或值为字符串、数字、布尔、字符串切片、map[string]string（answer 的答案）的 map，编码不会失败
	if err := s.store.InsertAudit(store.AuditEvent{MachineID: machineID, Actor: actor, RequestID: requestID, Action: action, Detail: string(b)}); err != nil {
		s.log.Printf("写审计 %q: %v", action, err)
	}
}

// broadcastRequest 读出请求的当前行并广播 request_updated（请求视图，见 view）。
func (s *Server) broadcastRequest(id string) {
	r, err := s.store.GetRequest(id)
	if err != nil {
		s.log.Printf("读请求 %q: %v", clip(id), err)
		return
	}
	s.clients.broadcast("request_updated", s.view(r))
}

// msgEndedByHub 是 hub 自己结束守护进程的 pending 时发给它的 Error 帧文案。
const msgEndedByHub = "请求已在 hub 结束"

// notifyEnded 在 hub 自己结束了守护进程的一条 pending（过期、被重发顶掉）后，对该机器发带 request_id 的 Error 帧：
// 守护进程据此把请求立即交回终端，不必挂满本机期限（两端时钟不一致时 hub 可能早得多判过期）。经 send 的下发白名单；
// 机器不在线时不发（它重连后重发 pending，pendingAgain 回“请求已结束”）。webhook 请求没有机器，不发。
func (s *Server) notifyEnded(r store.Request) {
	if r.Kind == "webhook" || r.MachineID == webhookMachineID {
		return
	}
	s.agents.send(r.MachineID, wire.Error{T: wire.TError, RequestID: r.ID, Message: msgEndedByHub})
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

// rejectPending 在连接 c 上回一个带 request_id 的 Error 帧：守护进程据此把这条请求立即按 hub_unreachable 交回终端。
// 只发给送来 pending 的那条连接（sendTo）：它已被替换或断开时不发，不落到同一机器的新连接上。
func (s *Server) rejectPending(c *agentConn, requestID, msg string) {
	s.agents.sendTo(c, wire.Error{T: wire.TError, RequestID: requestID, Message: msg})
}

// onPending 登记守护进程从连接 c 送来的待决请求（审批，或 kind ask 的提问）。hub 不信任守护进程：id 不合规、Gemini 会话、
// 种类不认识、提问的题目没脱敏干净或不合规的拒收（见 readPending），时间与期限按 hub 的时钟夹紧，载荷再脱敏，字段截断；
// hub 已有的 id（重连后重发）交给 pendingAgain。
func (s *Server) onPending(c *agentConn, p wire.PendingRequest) {
	m := c.machine
	if !validRequestID(p.RequestID) {
		s.log.Printf("机器 %s 送来的待决请求 id 不合规（%d 字节），拒收", m.ID, len(p.RequestID))
		s.rejectPending(c, p.RequestID, "登记失败")
		return
	}
	if strings.EqualFold(p.Session.Tool, "gemini") {
		s.log.Printf("机器 %s 送来 gemini 会话的待决请求 %q，拒收：gemini 请求不进 hub 等待", m.ID, p.RequestID)
		s.rejectPending(c, p.RequestID, "gemini 请求不进 hub")
		return
	}
	existing, err := s.store.GetRequest(p.RequestID)
	switch {
	case err == nil:
		s.pendingAgain(c, existing, p)
		return
	case !errors.Is(err, store.ErrNotFound):
		s.log.Printf("登记请求 %q 前查重: %v", p.RequestID, err)
		s.rejectPending(c, p.RequestID, "登记失败")
		return
	}
	body, err := readPending(p)
	if err != nil {
		s.log.Printf("机器 %s 送来的待决请求 %q 拒收：%v", m.ID, p.RequestID, err)
		s.rejectPending(c, p.RequestID, err.Error())
		return
	}
	sess := cleanSession(p.Session)
	sess.Role = sessionRole(c, sess)
	toolName := truncate(p.ToolName, maxFieldLen)
	if body.Kind == "ask" {
		toolName = proto.AskToolName // 提问只有这一个工具，不照收帧里的（sameRequest 同口径）
	}
	// 去重查询必须在插入新请求之前（否则会查到新请求自己）；旧请求等新请求登记成功后才标 cancelled/resend，
	// 新请求插不进去时旧请求仍是 pending
	old, err := s.store.FindResend(m.ID, sess.CLISessionID, sess.TurnID, toolName, body.Hash, s.cfg.ResendWindow)
	resend := err == nil
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Printf("查重发 %q: %v", p.RequestID, err)
	}
	root := truncate(p.ProjectRoot, maxPathLen)
	created := hubTime(p.CreatedAt)
	// DeadlineSeconds 是从 CreatedAt 起算的总时长
	deadline := min(max(p.DeadlineSeconds, 1), maxDeadlineSeconds)
	expires := store.FormatTime(created.Add(time.Duration(deadline) * time.Second))
	r := store.Request{ID: p.RequestID, MachineID: m.ID, SessionID: s.upsertSession(m.ID, sess, projectName(root)), Kind: body.Kind,
		ToolName: toolName, Payload: body.Payload, Ask: body.Ask, PayloadHash: body.Hash, Risk: body.Risk, ProjectRoot: root,
		EngineReason: body.EngineReason, Trigger: body.Trigger, State: "pending", TurnID: sess.TurnID, CLISessionID: sess.CLISessionID,
		CreatedAt: store.FormatTime(created), ExpiresAt: &expires, Role: sess.Role, BeyondRole: body.BeyondRole}
	if err := s.store.InsertRequest(r); err != nil {
		s.log.Printf("登记请求 %q: %v", p.RequestID, err)
		s.rejectPending(c, p.RequestID, "登记失败")
		return
	}
	if resend {
		err := s.store.SetRequestState(old.ID, "cancelled", store.DecidedByResend, "同一命令重新发起", "")
		switch {
		case err == nil:
			s.audit(m.ID, "system", old.ID, "cancelled_resend", map[string]string{"replaced_by": p.RequestID})
			s.broadcastRequest(old.ID)
			s.notifyEnded(old) // 旧的等待者（若还在）立即交回终端
		case !errors.Is(err, store.ErrNotPending):
			s.log.Printf("取消重发前的请求 %q: %v", old.ID, err)
		}
	}
	s.audit(m.ID, "agent", r.ID, "request_pending", map[string]string{"risk": r.Risk, "tool": r.ToolName})
	s.announceNew(r)
}

// pendingAgain 处理守护进程从连接 c 再次送来的、hub 已有的请求 id（重连后重发）。回的 Error 帧只发给 c
// （c 已不是该机器的当前连接时不发，不落到新连接上）：
//   - 属于别的机器 → 回 Error；
//   - 仍 pending 或已决定（approved、denied、answered，见 store.Deliverable），但内容（种类、载荷或题目的哈希、工具名、
//     CLI 会话）与库里不符 → 回 Error 并记日志，库里那行不动：免得把另一条命令当成这条重发，或把用户对另一条命令的决定套到这条上；
//   - 仍 pending → 只广播；
//   - 已决定 → 在 c 上再发决定（answered 带答案），守护进程还在等它（补发上限截下的决定也由此带出）；对齐时已在 c 上补发过的不再发；
//     已送达的照发并记日志；
//   - 其他终态（含手机上选了交回终端的 passthrough）→ 回 Error，守护进程把它交回终端。
func (s *Server) pendingAgain(c *agentConn, r store.Request, p wire.PendingRequest) {
	m := c.machine
	reject := func(msg string) { s.rejectPending(c, r.ID, msg) }
	live := r.State == "pending" || store.Deliverable(r.State)
	switch {
	case r.MachineID != m.ID:
		s.log.Printf("机器 %s 送来的待决请求 %q 与另一台机器的请求同 id，拒收", m.ID, r.ID)
		reject("登记失败")
	case live && !sameRequest(r, p):
		s.log.Printf("机器 %s 重发的待决请求 %q 与库里的请求（%s）内容不符，拒收", m.ID, r.ID, r.State)
		reject("请求内容不符")
	case r.State == "pending":
		s.clients.broadcast("request_updated", s.view(r))
	case store.Deliverable(r.State):
		if c.wasResent(r.ID) {
			return // 对齐时已在这条连接上补发过
		}
		if r.DeliveredAt != nil {
			s.log.Printf("机器 %s 又送来已送达的请求 %q 的 pending，再发一次决定", m.ID, r.ID)
		}
		s.agents.sendTo(c, decisionFrame(r))
	default:
		reject("请求已结束")
	}
}

// sameRequest 报告重发的 pending 与库里的请求是否同一条：按登记时的方式读出（readPending）后种类与哈希（审批是再脱敏的载荷、
// 提问是题目）相同，工具名、CLI 会话也相同。读不出（如题目不合规）的不是同一条。
func sameRequest(r store.Request, p wire.PendingRequest) bool {
	body, err := readPending(p)
	toolName := truncate(p.ToolName, maxFieldLen)
	if err == nil && body.Kind == "ask" {
		toolName = proto.AskToolName
	}
	return err == nil && body.Kind == r.Kind && body.Hash == r.PayloadHash && toolName == r.ToolName &&
		truncate(p.Session.CLISessionID, maxFieldLen) == r.CLISessionID
}

// cleanTrigger 给守护进程送来的触发点兜底：段再脱敏、截断，性质截断（它是固定短语，网页按它查英文）；nil 原样。返回副本。
func cleanTrigger(t *policy.Trigger) *policy.Trigger {
	if t == nil {
		return nil
	}
	return &policy.Trigger{Segment: redactText(t.Segment, maxFieldLen), Why: truncate(t.Why, maxFieldLen)}
}

// pendingBody 是待决请求里按种类不同的部分：审批的载荷、风险与引擎理由，或提问的题目；Hash 是去重与重发比对用的哈希。
type pendingBody struct {
	Kind         string
	Payload      policy.Payload
	Ask          *proto.Ask
	Hash         string
	Risk         string
	EngineReason string
	Trigger      *policy.Trigger
	// BeyondRole 是守护进程说的“超出角色的边界”，只有审批有。不论守护进程声明没声明 role 都照收：它只会让 hub 少给“记住”与“限时放行”。
	BeyondRole bool
}

// readPending 按种类读出守护进程送来的待决请求：没有 kind（旧守护进程）或 permission 是审批，载荷再脱敏（守护进程已脱敏，
// hub 再跑一次兜底，幂等），风险截断、引擎理由脱敏截断；ask 是提问，题目见 checkAsk，没有载荷、风险与引擎理由。
// 其余种类不认识，返回错误（文案回给守护进程、写进日志，不含请求内容）。
func readPending(p wire.PendingRequest) (pendingBody, error) {
	switch p.Kind {
	case "", "permission":
		payload := redact.Payload(p.Payload)
		return pendingBody{Kind: "permission", Payload: payload, Hash: store.PayloadHash(payload), Risk: truncate(p.Risk, maxFieldLen),
			EngineReason: redactText(p.EngineReason, maxTextLen), Trigger: cleanTrigger(p.Trigger), BeyondRole: p.BeyondRole}, nil
	case "ask":
		if err := checkAsk(p.Ask); err != nil {
			return pendingBody{}, err
		}
		// options 为 null（JSON 里缺省）的题规整成空数组，入库、哈希、视图都一致；重发时同样规整，哈希才对得上
		for i := range p.Ask.Questions {
			if p.Ask.Questions[i].Options == nil {
				p.Ask.Questions[i].Options = []proto.Option{}
			}
		}
		return pendingBody{Kind: "ask", Ask: p.Ask, Hash: store.AskHash(p.Ask)}, nil
	}
	return pendingBody{}, fmt.Errorf("不认识的请求种类 %q", truncate(p.Kind, maxFieldLen))
}

// errAskUnredacted 表示守护进程送来的题目里还有该脱敏的内容。
var errAskUnredacted = errors.New("题目未脱敏干净")

// checkAsk 校验守护进程送来的题目：结构与上限合规（adapter.ValidateAsk：有题、题面非空且互不相同、题数选项数与各段长度不超限），
// 且题面、标题、选项的标签与说明都已脱敏干净（再跑一次 redact.Text 不变；它对已脱敏的文字是幂等的）。
// hub 不截断、不去空白、不改写题目：守护进程按 hub 回来的答案键（即 hub 收到的题面）逐字节找题，改了就对不上。
// 所以没脱敏干净的整条拒收（守护进程交回终端），而不是在 hub 改写。
func checkAsk(a *proto.Ask) error {
	if err := adapter.ValidateAsk(a); err != nil {
		return err
	}
	clean := true
	a.MapText(func(s string) string {
		if redact.Text(s) != s {
			clean = false
		}
		return s
	})
	if !clean {
		return errAskUnredacted
	}
	return nil
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

// onDelivered 记录决定已交给钩子，真记下了送达时间时广播 request_updated。只认该机器、已决定未送达的请求
// （见 store.MarkDelivered）。
func (s *Server) onDelivered(m store.Machine, d wire.Delivered) {
	updated, err := s.store.MarkDelivered(m.ID, d.RequestID)
	if err != nil {
		s.log.Printf("标记送达 %q: %v", clip(d.RequestID), err)
		return
	}
	if updated {
		s.broadcastRequest(d.RequestID)
	}
}

// onAgentHello 用守护进程 hello 里的 pending 列表对齐：hub 多出来的 pending 取消（agent_restart）；已决定未送达的，
// 守护进程仍持有的补发（至多 helloResendMax 条），不再持有的记一次 undeliverable。
// 同一机器的对齐串行执行；c 已不是该机器的当前连接（被替换或已断开）时不执行，补发也只发给 c，
// 旧连接的对齐不会覆盖新连接的，帧也不会落到新连接上。
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
			s.log.Printf("对齐机器 %s：取消请求 %q: %v", m.ID, r.ID, err)
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
			if s.agents.sendTo(c, decisionFrame(r)) { // 入队不等于送达：送达只认 delivered 回执
				c.markResent(r.ID)
			}
			resent++
		}
	}
	if skipped > 0 {
		s.log.Printf("机器 %s 重连时有 %d 条已决定未送达的请求超过补发上限 %d，留待守护进程重发 pending 或下次重连", m.ID, skipped, helloResendMax)
	}
}

// markUndeliverable 给已决定、但守护进程已不再持有的请求记一次 undeliverable 审计并广播，网页据此标出“决定未送达”；
// 不标送达。去重只认 hub 自己写的行（actor system、该请求的机器），守护进程报来的或挂在别的机器上的同名行不算。
func (s *Server) markUndeliverable(r store.Request) {
	events, err := s.store.ListAudit(0, r.ID)
	if err != nil {
		s.log.Printf("读请求 %q 的审计: %v", r.ID, err)
		return
	}
	for _, e := range events {
		if e.Action == "undeliverable" && e.Actor == "system" && e.MachineID == r.MachineID {
			return
		}
	}
	s.audit(r.MachineID, "system", r.ID, "undeliverable", map[string]string{"state": r.State})
	s.clients.broadcast("request_updated", s.view(r))
}

// decisionFrame 把已决定的请求编成 decision 帧：approved → allow，answered → answer（带库里的答案原文，键是 hub 收到的题面），
// passthrough（手机上选了交回终端）→ passthrough，其余 → deny。
func decisionFrame(r store.Request) wire.Decision {
	d := wire.Decision{T: wire.TDecision, RequestID: r.ID, Action: "deny", Reason: r.DecisionNote, RuleID: r.RuleID, DecidedBy: r.DecidedBy}
	switch r.State {
	case "approved":
		d.Action = "allow"
	case "answered":
		d.Action, d.Answers = "answer", r.Answers
	case "passthrough":
		d.Action = "passthrough"
	}
	return d
}

// sendDecision 把已决定的请求排进其机器当前连接的发送队列。返回 true 只表示入队，不表示送达。
func (s *Server) sendDecision(r store.Request) bool {
	return s.agents.send(r.MachineID, decisionFrame(r))
}

// Decide 是用户的决定入口（网页或 CLI）：只能 allow 或 deny，请求不能是提问（kind ask，走 Answer），必须仍 pending、
// 所属机器未吊销且未过期。
// Remember 时先学规则（学不了时请求保持 pending，错误包 errLearn），再改状态；成功后审计学到的规则并下发规则快照，
// 再审计决定、下发决定（未连接时留给重连对齐补发；webhook 请求改为回调来源）、广播。快照先于决定排进同一连接的队列，守护进程先拿到规则。
// Trust 非空时在改状态之前就校验并入库这条放行（reserveTrustLocked），不合规时整个决定不落库、回 errBadTrust；
// 批准落库失败时收回它；成功后审计 trust_set，并在下发决定之前把放行快照排进同一连接的队列：守护进程先有放行、再收到 allow。
// 请求已决定不了（不是 pending、机器已吊销、已过期）时不建放行，错误照旧由改状态那一步给出。
// 超出角色边界的请求（BeyondRole）批准时不能带 Remember 或 Trust，回 errBeyondRole（满足 errors.Is(err, errBadAction)，接口回 400）。
// 错误：errBadAction（对提问为 errIsAsk）、errBadScope、errBadTrust、store.ErrNotFound、store.ErrNotPending（机器已吊销时为 errRevoked、
// 已过期时为 errExpired，都满足 errors.Is(err, store.ErrNotPending)）、errLearn；其余是存储错误。
func (s *Server) Decide(in DecideInput) (store.Request, error) {
	if in.Action != "allow" && in.Action != "deny" {
		return store.Request{}, errBadAction
	}
	if !validScope(in.Scope) {
		return store.Request{}, errBadScope
	}
	if in.Action == "allow" && (in.Remember || in.Trust != nil) {
		if err := s.refuseBeyondRole(in.RequestID); err != nil {
			return store.Request{}, err
		}
	}
	var trust *proto.Trust
	if in.Trust != nil {
		s.trustMu.Lock()
		defer s.trustMu.Unlock()
		t, err := s.reserveTrust(in)
		if err != nil {
			return store.Request{}, err
		}
		trust = t
	}
	note := truncate(in.Note, maxTextLen)
	state := "denied"
	if in.Action == "allow" {
		state = "approved"
	}
	r, ruleID, err := s.commitDecision(in, state, note)
	if err != nil {
		if trust != nil {
			s.unreserveTrust(*trust)
		}
		return store.Request{}, s.settleRefused(r, err)
	}
	if fresh, err := s.store.GetRequest(r.ID); err == nil {
		r = fresh
	} else {
		// 决定已落库，读回失败也照常下发与广播
		s.log.Printf("读回请求 %q: %v", r.ID, err)
		now := store.Now()
		r.State, r.DecidedBy, r.DecisionNote, r.RuleID, r.DecidedAt = state, "user", note, ruleID, &now
	}
	if ruleID != "" {
		s.audit(r.MachineID, "user", r.ID, "rule_learned", map[string]string{"rule_id": ruleID, "action": in.Action})
		s.pushRules()
	}
	if trust != nil {
		s.audit(r.MachineID, "user", r.ID, "trust_set", trustDetail(*trust))
		s.pushTrustsLocked(r.MachineID)
	}
	// 审计动作是 allow 或 deny，与 wire.Decision 的 action 一致（简报接口文字里的 approve 不用）
	s.audit(r.MachineID, "user", r.ID, in.Action, map[string]any{"note": note, "remember": in.Remember, "rule_id": ruleID})
	if r.Kind == "webhook" {
		s.callbackWebhook(r)
	} else {
		s.sendDecision(r) // 未连接时留给重连对齐补发
	}
	s.clients.broadcast("request_updated", s.view(r))
	if trust != nil {
		s.broadcastTrusts()
	}
	return r, nil
}

// refuseBeyondRole 是批准带“记住”或放行时的预检：请求超出了角色的边界就回 errBeyondRole；读不出请求（含没有这条）原样返回错误，
// 不当成通过。它只是早一点给出明确的错误：真正建放行、学规则之前，reserveTrust 与 commitDecision 在各自用的那个请求对象上还会再查一次
// （预检时还没登记、随后才并发登记的越界请求也挡得住）。
func (s *Server) refuseBeyondRole(id string) error {
	r, err := s.store.GetRequest(id)
	if err != nil {
		return err
	}
	if r.BeyondRole {
		return errBeyondRole
	}
	return nil
}

// beyondSideEffect 报告对请求 r 的这次决定是不是要给越界的请求建放行或学“放行”规则（不允许）。
func beyondSideEffect(r store.Request, in DecideInput) bool {
	return r.BeyondRole && in.Action == "allow" && (in.Remember || in.Trust != nil)
}

// reserveTrust 是 Decide 带放行时的前置（调用方持 trustMu）：取请求，提问回 errIsAsk；请求已决定不了（decidable 不过）时
// 返回 nil、不建放行，交给改状态那一步给出该给的错误；否则校验并入库（reserveTrustLocked）。
func (s *Server) reserveTrust(in DecideInput) (*proto.Trust, error) {
	r, err := s.store.GetRequest(in.RequestID)
	if err != nil {
		return nil, err
	}
	if err := refuseAsk(r); err != nil {
		return nil, err
	}
	if beyondSideEffect(r, in) { // 建放行用的就是这个对象：在这里（持 trustMu）再查一次，不依赖预检
		return nil, errBeyondRole
	}
	if s.decidable(r) != nil {
		return nil, nil
	}
	t, err := s.reserveTrustLocked(r, in.Action, *in.Trust)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// commitDecision 在 decideMu 下完成“取请求 → 校验（takePending：不是提问、仍 pending、机器未吊销、未过期）→ 学规则 → 改状态”，
// 并发的两次决定不会都去学规则。已吊销、已过期时 takePending 已改了状态，返回请求与 errRevoked/errExpired，由调用方经 settleRefused 收尾。
// 学完规则后改状态失败（期间守护进程取消、hub 判过期、重发顶掉——这些不经 decideMu）时撤销刚学的规则，不留下已学未决的规则。
func (s *Server) commitDecision(in DecideInput, state, note string) (store.Request, string, error) {
	s.decideMu.Lock()
	defer s.decideMu.Unlock()
	r, err := s.takePending(in.RequestID, refuseAsk)
	if err != nil {
		return r, "", err
	}
	if beyondSideEffect(r, in) { // 学规则、改状态用的就是这个对象：在 decideMu 里再查一次（放行已预留时由 Decide 收回）
		return store.Request{}, "", errBeyondRole
	}
	ruleID := ""
	if in.Remember {
		id, err := s.learnRule(r, in.Action, in.Scope)
		if err != nil {
			return store.Request{}, "", err
		}
		ruleID = id
		if s.beforeSettle != nil {
			s.beforeSettle(r)
		}
	}
	if err := s.store.SetRequestState(r.ID, state, "user", note, ruleID); err != nil {
		if ruleID != "" {
			s.rollbackLearned(r, ruleID)
		}
		return store.Request{}, "", err
	}
	return r, ruleID, nil
}

// propose 算出“记住”会生成的规则草案（draftRule），不入库。前置条件与 Decide 一致、错误也一样：没有这条请求
// store.ErrNotFound，是提问 errIsAsk（提问不能记住），已不是 pending store.ErrNotPending，机器已吊销 errRevoked，已过期 errExpired，
// 记不住 errLearn（含规则集放不下这条，见 checkFits），action、scope 不认识 errBadAction、errBadScope。
// 与 commitDecision 不同，它只读不写：已吊销、已过期的请求不在这里改状态（吊销时 hub 已取消该机器的 pending，
// 过期留给过期扫描或之后的 decide），不审计、不广播；也不拿 decideMu、rulesMu（预览可能与之后的保存结果不同，保存时会重查）。
func (s *Server) propose(id, action, scope string) (policy.Rule, error) {
	if action != "allow" && action != "deny" {
		return policy.Rule{}, errBadAction
	}
	if !validScope(scope) {
		return policy.Rule{}, errBadScope
	}
	r, err := s.store.GetRequest(id)
	if err != nil {
		return policy.Rule{}, err
	}
	if err := refuseAsk(r); err != nil {
		return policy.Rule{}, err
	}
	if action == "allow" && r.BeyondRole {
		return policy.Rule{}, errBeyondRole
	}
	if err := s.decidable(r); err != nil {
		return policy.Rule{}, err
	}
	rule, err := s.draftRule(r, action, scope)
	if err != nil {
		return policy.Rule{}, err
	}
	if err := s.checkFits(rule); err != nil {
		return policy.Rule{}, err
	}
	return rule, nil
}

// refuseAsk 与 onlyAsk 是按种类挑接口的检查（种类不会变，先于状态判断）：决定（含“记住”）不收提问，作答只收提问。
func refuseAsk(r store.Request) error {
	if r.Kind == "ask" {
		return errIsAsk
	}
	return nil
}

func onlyAsk(r store.Request) error {
	if r.Kind != "ask" {
		return errNotAsk
	}
	return nil
}

// decidable 只读地判断请求 r 现在能否由用户决定或作答：已不是 pending 返回 store.ErrNotPending，所属机器已吊销 errRevoked，
// 已过 expires_at errExpired；读机器出错原样返回。
func (s *Server) decidable(r store.Request) error {
	if r.State != "pending" {
		return store.ErrNotPending
	}
	revoked, err := s.machineRevoked(r.MachineID)
	if err != nil {
		return err
	}
	if revoked {
		return errRevoked
	}
	if r.ExpiresAt != nil && *r.ExpiresAt <= store.FormatTime(time.Now()) {
		return errExpired
	}
	return nil
}

// takePending 是决定与作答共同的前置，调用方持 decideMu：取请求，按 kind 检查种类，再看 decidable。
// 所属机器已吊销时把请求改成 cancelled/machine_revoked，返回请求与 errRevoked（吊销时与在途 pending 竞态漏取消的，在这里收尾）；
// 已过期时把请求改成 expired/timeout，返回请求与 errExpired。两者都由调用方经 settleRefused 审计并广播。
// 其余错误返回零值请求。
func (s *Server) takePending(id string, kind func(store.Request) error) (store.Request, error) {
	r, err := s.store.GetRequest(id)
	if err != nil {
		return store.Request{}, err
	}
	if err := kind(r); err != nil {
		return store.Request{}, err
	}
	switch err := s.decidable(r); {
	case errors.Is(err, errRevoked):
		if err := s.store.SetMachineRequestState(r.MachineID, r.ID, "cancelled", decidedByRevoked, "机器已吊销", ""); err != nil {
			return store.Request{}, err
		}
		return r, errRevoked
	case errors.Is(err, errExpired):
		if err := s.store.SetRequestState(r.ID, "expired", "timeout", "hub 判定过期", ""); err != nil {
			return store.Request{}, err
		}
		return r, errExpired
	case err != nil:
		return store.Request{}, err
	}
	return r, nil
}

// settleRefused 给被拒的决定或作答收尾并原样返回 err：takePending 已把请求 r 改成 expired 或 cancelled/machine_revoked 时，
// 在这里审计并广播（过期的还通知守护进程，webhook 请求回调来源，见 settleExpired）。其余错误什么也不做。
func (s *Server) settleRefused(r store.Request, err error) error {
	switch {
	case errors.Is(err, errExpired):
		s.settleExpired(r)
	case errors.Is(err, errRevoked):
		s.settled(r.MachineID, "system", r.ID, "cancelled", decidedByRevoked)
	}
	return err
}

// AnswerInput 是网页的一次作答。Answers 的键是题面（hub 存的、逐字节相同），值是答案（多选题的各个答案用逗号连接，由网页拼）；
// Passthrough 为真表示让 agent 在终端问，这时 Answers 不看。
type AnswerInput struct {
	RequestID   string
	Answers     map[string]string
	Passthrough bool
}

// Answer 是用户对提问（kind ask）的作答入口：请求必须是提问、仍 pending、所属机器未吊销且未过期，不交回终端时答案要合规（checkAnswers）。
// 与 Decide 同在 decideMu 下、共用前置（takePending），两次并发作答只有一次成功；不学规则、不计规则命中。
// 成功后审计（answer 带脱敏后的答案，或 passthrough）、下发决定（answer 带答案原文，键是 hub 收到的题面；未连接时 answered 留给重连对齐补发，
// passthrough 是终态、不补发，守护进程重连重发 pending 时回“请求已结束”，一样交回终端）、广播。
// 错误：errNotAsk（满足 errors.Is(err, errBadAction)）、errBadAnswer、store.ErrNotFound、store.ErrNotPending
// （机器已吊销 errRevoked、已过期 errExpired，同 Decide）；其余是存储错误。
func (s *Server) Answer(in AnswerInput) (store.Request, error) {
	r, err := s.commitAnswer(in)
	if err != nil {
		return store.Request{}, s.settleRefused(r, err)
	}
	if r.State == "answered" {
		s.audit(r.MachineID, "user", r.ID, "answer", map[string]any{"answers": redactAnswers(r.Answers)})
	} else {
		s.audit(r.MachineID, "user", r.ID, "passthrough", nil)
	}
	s.sendDecision(r) // 未连接时 answered 留给重连对齐补发
	s.clients.broadcast("request_updated", s.view(r))
	return r, nil
}

// commitAnswer 在 decideMu 下完成“取请求 → 校验（takePending：是提问、仍 pending、机器未吊销、未过期）→ 校验答案 → 改状态”，
// 返回改后的行。已吊销、已过期时同 commitDecision。
func (s *Server) commitAnswer(in AnswerInput) (store.Request, error) {
	s.decideMu.Lock()
	defer s.decideMu.Unlock()
	r, err := s.takePending(in.RequestID, onlyAsk)
	if err != nil {
		return r, err
	}
	if !in.Passthrough {
		if err := checkAnswers(r.Ask, in.Answers); err != nil {
			return store.Request{}, err
		}
	}
	return s.store.AnswerRequest(r.ID, in.Answers, in.Passthrough, time.Now())
}

// checkAnswers 校验作答：a 的每道题（按题面逐字节）都要有答案，答案去掉首尾空白后非空、不超过 proto.AnswerMax 个字符；
// 不属于这些题的键不收（网页拿着对不上的题面在答）。答案本身不改写，原样存、原样下发。错误都包 errBadAnswer，说明是第几题。
// 口径比守护进程的 checkAnswers（internal/agent/ask.go，空白答案与多余的键都放过）严，是有意的：这里是答案的入口，
// 宁可让网页重答也不下发空白答案或对不上的键，更严是安全的一边；守护进程只兜底。
func checkAnswers(a *proto.Ask, answers map[string]string) error {
	if a == nil {
		return fmt.Errorf("%w：请求没有题目", errBadAnswer)
	}
	for i, q := range a.Questions {
		ans, ok := answers[q.Question]
		switch {
		case !ok:
			return fmt.Errorf("%w：第 %d 题没有答案", errBadAnswer, i+1)
		case strings.TrimSpace(ans) == "":
			return fmt.Errorf("%w：第 %d 题的答案为空", errBadAnswer, i+1)
		case utf8.RuneCountInString(ans) > proto.AnswerMax:
			return fmt.Errorf("%w：第 %d 题的答案超过 %d 个字符", errBadAnswer, i+1, proto.AnswerMax)
		}
	}
	// 题面互不相同（入库前 checkAsk 校验过），每道题都有答案时键数多于题数就是有多余的键
	if len(answers) != len(a.Questions) {
		return fmt.Errorf("%w：有不属于这些题的答案", errBadAnswer)
	}
	return nil
}

// redactAnswers 是 hub 审计里记的答案：值过 redact.Text（答案是用户打的字，也可能贴进凭据；与守护进程审计的口径一致），
// 键是题面（入库前已确认脱敏干净）。库里与下发的仍是原文。
func redactAnswers(answers map[string]string) map[string]string {
	out := make(map[string]string, len(answers))
	for q, a := range answers {
		out[q] = redact.Text(a)
	}
	return out
}

// machineRevoked 报告请求所属机器是否已吊销。webhook 请求与库里没有的机器不算。
func (s *Server) machineRevoked(machineID string) (bool, error) {
	if machineID == webhookMachineID {
		return false, nil
	}
	m, err := s.store.GetMachine(machineID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("读机器 %s: %w", machineID, err)
	}
	return m.Revoked, nil
}

// settleExpired 在 hub 把请求 r（改状态前读出的行）判为过期后审计并广播；webhook 请求还要回调来源（deny，备注 expired），
// 守护进程的请求通知该机器（notifyEnded）。
func (s *Server) settleExpired(r store.Request) {
	s.settled(r.MachineID, "system", r.ID, "expired", "timeout")
	if r.Kind == "webhook" {
		r.State = "expired"
		s.callbackWebhook(r)
		return
	}
	s.notifyEnded(r)
}

// expireDue 把 expires_at 已到的 pending 改成 expired/timeout，审计并广播（webhook 请求还回调来源）：hub 自己执行过期，
// 守护进程掉线或崩溃时 pending 也不会一直挂着。每条之前看一眼 ctx：关停时立即返回，剩下的留给下次扫描（重启后启动时那次），
// hub 停机很久后重启又立刻关停时也不会因逐条处理拖过关停时限。
func (s *Server) expireDue(ctx context.Context, now time.Time) {
	due, err := s.store.ExpiredPending(now)
	if err != nil {
		s.log.Printf("查过期请求: %v", err)
		return
	}
	for _, r := range due {
		if ctx.Err() != nil {
			return
		}
		err := s.store.SetRequestState(r.ID, "expired", "timeout", "hub 判定过期", "")
		if errors.Is(err, store.ErrNotPending) {
			continue
		}
		if err != nil {
			s.log.Printf("过期请求 %q: %v", r.ID, err)
			continue
		}
		s.settleExpired(r)
	}
}

// expireLoop 启动时扫一次，之后每 expireSweepInterval 扫一次，ctx 结束时返回。
func (s *Server) expireLoop(ctx context.Context) {
	s.expireDue(ctx, time.Now())
	t := time.NewTicker(expireSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.expireDue(ctx, now)
		}
	}
}

// onAudit 收守护进程从连接 c 送来的审计批次：每条兜底脱敏后入库；question/done 存成 recorded 行；permission、ask 条目按白名单更新 hub。
// 条目指向别的机器的请求时，审计行不挂到那条请求上（request_id 置空，detail 保留原值），也不据它改 hub。
// 补的行与会话的角色按 sessionRole（守护进程没声明 role 时为未分配）。
func (s *Server) onAudit(c *agentConn, a wire.Audit) {
	m := c.machine
	bad, foreign := 0, 0
	for _, raw := range a.Entries {
		var e agent.AuditEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			bad++
			continue
		}
		idOK := validRequestID(e.RequestID)
		e = cleanEntry(e)
		e.Session.Role = sessionRole(c, e.Session)
		ts := hubTime(e.TS)
		// detail 用解码后的结构重新编码：未知字段（如钩子进程的环境变量）被丢弃，载荷与文本已再脱敏
		detail, err := json.Marshal(e)
		if err != nil {
			bad++
			continue
		}
		var existing store.Request
		found, auditID := false, ""
		if idOK {
			r, err := s.store.GetRequest(e.RequestID)
			switch {
			case err == nil && r.MachineID != m.ID:
				foreign++
				idOK = false
			case err == nil:
				existing, found, auditID = r, true, e.RequestID
			case errors.Is(err, store.ErrNotFound):
				auditID = e.RequestID
			default:
				s.log.Printf("读请求 %q: %v", e.RequestID, err)
				idOK = false
			}
		}
		if err := s.store.InsertAudit(store.AuditEvent{TS: store.FormatTime(ts), MachineID: m.ID, Actor: "agent", RequestID: auditID, Action: e.State, Detail: string(detail)}); err != nil {
			s.log.Printf("写机器 %s 的审计: %v", m.ID, err)
		}
		switch {
		case (e.Kind == "question" || e.Kind == "done") && e.State == "recorded":
			s.recordEvent(m, e, ts, detail)
		case (e.Kind == "permission" || e.Kind == "ask") && idOK:
			s.applyAgentFinal(m, e, ts, existing, found)
		}
	}
	if bad > 0 {
		s.log.Printf("机器 %s 的审计批次里 %d 条无法解析，已跳过", m.ID, bad)
	}
	if foreign > 0 {
		s.log.Printf("机器 %s 的审计批次里 %d 条指向别的机器的请求，审计行未挂到这些请求上", m.ID, foreign)
	}
}

// eventID 由机器 id、条目时间、种类、CLI 会话 id 与重编码后的 detail 派生出 32 个十六进制字符：
// 守护进程重发同一批审计时 question/done 不会重复登记。
func eventID(machineID string, e agent.AuditEntry, detail []byte) string {
	h := sha256.New()
	for _, part := range []string{machineID, store.FormatTime(e.TS), e.Kind, e.Session.CLISessionID} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	h.Write(detail)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// recordEvent 把 question/done 存成一行 recorded（第 4 份的收件箱靠它列出谁在等回答），刷新会话并广播 request_new
// （新 question 还推到手机，见 announceNew）。
// e 已经过 cleanEntry：Message 已脱敏。同一条目再来（批次重发，含两条连接并发处理同一批）时该行已存在，静默跳过。
func (s *Server) recordEvent(m store.Machine, e agent.AuditEntry, ts time.Time, detail []byte) {
	r := store.Request{ID: eventID(m.ID, e, detail), MachineID: m.ID, SessionID: s.upsertSession(m.ID, e.Session, ""), Kind: e.Kind, ToolName: e.ToolName,
		State: "recorded", Title: truncate(e.Message, maxTitleLen), TurnID: e.Session.TurnID, CLISessionID: e.Session.CLISessionID, CreatedAt: store.FormatTime(ts),
		Role: e.Session.Role}
	inserted, err := s.store.InsertRequestIfAbsent(r)
	if err != nil {
		s.log.Printf("记机器 %s 的 %q 事件: %v", m.ID, e.Kind, err)
		return
	}
	if inserted {
		s.announceNew(r)
	}
}

// applyAgentFinal 按白名单用本机器的 permission、ask 条目更新 hub（existing/found 是 hub 里该 id 的行，已确认属于本机器）。
// 本机对钩子发生了什么是唯一真相：
//   - approved、denied、answered（store.Deliverable）：送达对账。hub 行状态相同且未记送达时补记送达时间（delivered 回执可能随断线丢失），不改状态。
//   - expired、cancelled、passthrough：hub 有该请求时覆盖（ApplyAgentFinal 只改 pending 或重连对齐标的 cancelled/agent_restart），
//     没有时补历史行，种类同条目：ask 的行带条目里的题目（cleanEntry 已再脱敏）、没有载荷与风险——例如守护进程没送到 hub
//     就交回终端的提问（脱敏后题面重复、hub 不可达）。
//   - auto_allowed、auto_denied：只认 permission（提问不经规则），只在 hub 没有该请求时补历史行并记规则命中。
//   - 其余状态不改 hub。
func (s *Server) applyAgentFinal(m store.Machine, e agent.AuditEntry, ts time.Time, existing store.Request, found bool) {
	switch {
	case store.Deliverable(e.State):
		if found && existing.State == e.State && existing.DeliveredAt == nil {
			updated, err := s.store.MarkDelivered(m.ID, e.RequestID)
			if err != nil {
				s.log.Printf("对账送达 %q: %v", e.RequestID, err)
			} else if updated {
				s.broadcastRequest(e.RequestID)
			}
		}
		return
	case e.State == "expired" || e.State == "cancelled" || e.State == "passthrough":
		if found {
			updated, err := s.store.ApplyAgentFinal(m.ID, e.RequestID, e.State, e.DecidedBy, e.Reason, e.RuleID)
			if err != nil {
				s.log.Printf("以本机终态更新请求 %q: %v", e.RequestID, err)
				return
			}
			if updated {
				s.settled(m.ID, "agent", e.RequestID, e.State, e.DecidedBy)
			}
			return
		}
	case (e.State == "auto_allowed" || e.State == "auto_denied") && e.Kind == "permission":
		if found {
			return
		}
	default:
		return
	}
	created := store.FormatTime(ts)
	r := store.Request{ID: e.RequestID, MachineID: m.ID, SessionID: s.upsertSession(m.ID, e.Session, ""), Kind: e.Kind, ToolName: e.ToolName,
		Risk: e.Risk, EngineReason: e.Reason, Trigger: e.Trigger, State: e.State, DecidedBy: e.DecidedBy, RuleID: e.RuleID, TurnID: e.Session.TurnID,
		CLISessionID: e.Session.CLISessionID, CreatedAt: created, DecidedAt: &created, Role: e.Session.Role, BeyondRole: e.BeyondRole}
	switch {
	case e.Kind == "ask":
		r.Ask, r.Risk = e.Ask, ""
	case e.Payload != nil:
		r.Payload = *e.Payload
	}
	if err := s.store.InsertRequest(r); err != nil {
		s.log.Printf("补历史行 %q: %v", e.RequestID, err)
		return
	}
	if e.RuleID != "" && (e.State == "auto_allowed" || e.State == "auto_denied") {
		if err := s.store.RecordHit(e.RuleID); err != nil {
			s.log.Printf("记规则 %q 命中: %v", e.RuleID, err)
		}
	}
}
