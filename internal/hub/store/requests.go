package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// ErrNotPending 表示请求已经不是待处理状态。
var ErrNotPending = errors.New("请求已不是待处理状态")

// ErrInvalidState 表示守护进程来源的写入给了不允许的状态（只允许 cancelled、expired、passthrough）。
var ErrInvalidState = errors.New("目标状态不是可写入的终态")

// decided_by 的取值里由存储层判断的几个。
const (
	DecidedByAgentRestart = "agent_restart" // 守护进程重启或重连对齐时取消的
	DecidedByResend       = "resend"        // 被同键重发顶掉的
)

// Request 是 requests 表的一行。question/done 也存成一行：Kind 为 question|done，State 为 recorded，
// Title 为脱敏后的消息，Payload 为空值，ExpiresAt 为 nil。
// JSON 为 snake_case，广播与 API 直接输出它；PayloadHash 与 Callback（webhook 回调，含明文回调令牌）不序列化。
type Request struct {
	ID           string         `json:"id"`
	MachineID    string         `json:"machine_id"`
	SessionID    string         `json:"session_id"`
	Kind         string         `json:"kind"`
	ToolName     string         `json:"tool_name"`
	Payload      policy.Payload `json:"payload"`
	PayloadHash  string         `json:"-"`
	Risk         string         `json:"risk"`
	ProjectRoot  string         `json:"project_root"`
	EngineReason string         `json:"engine_reason"`
	State        string         `json:"state"`
	DecidedBy    string         `json:"decided_by"`
	DecisionNote string         `json:"decision_note"`
	RuleID       string         `json:"rule_id"`
	TurnID       string         `json:"turn_id"`
	CLISessionID string         `json:"cli_session_id"`
	Title        string         `json:"title"`
	Callback     string         `json:"-"`
	CreatedAt    string         `json:"created_at"`
	ExpiresAt    *string        `json:"expires_at"`
	DecidedAt    *string        `json:"decided_at"`
	DeliveredAt  *string        `json:"delivered_at"`
}

// PayloadHash 是载荷 JSON 的 SHA-256，用于去重，与 InsertRequest 存的哈希一致。
// 载荷无法序列化（如 Args 不是合法 JSON）时返回空串，调用方据此不去重；同一载荷 InsertRequest 也会报错，
// 库里不会有空哈希的行。
func PayloadHash(p policy.Payload) string {
	b, err := payloadJSON(p)
	if err != nil {
		return ""
	}
	return hashJSON(b)
}

// payloadJSON 是载荷存库与求哈希共用的序列化。
func payloadJSON(p policy.Payload) ([]byte, error) { return json.Marshal(p) }

func hashJSON(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// checkTime 要求 v 是 fmtTime 的输出（定宽、UTC、以 Z 结尾），否则字典序比较会错。
func checkTime(col, v string) error {
	t, err := time.Parse(timeLayout, v)
	if err != nil || fmtTime(t) != v {
		return fmt.Errorf("%s %q 不是定宽 UTC 时间，应使用 store.FormatTime", col, v)
	}
	return nil
}

// UpsertSession 登记或刷新会话，返回会话行 id。已存在时刷新 last_event_at，cwd、project 非空才覆盖，started_at 不变。
func (s *Store) UpsertSession(machineID string, sess proto.Session, project string) (string, error) {
	now := Now()
	var id string
	err := s.db.QueryRow(`INSERT INTO sessions(id, machine_id, tool, cli_session_id, cwd, project, started_at, last_event_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(machine_id, tool, cli_session_id) DO UPDATE SET
			cwd = CASE WHEN excluded.cwd <> '' THEN excluded.cwd ELSE cwd END,
			project = CASE WHEN excluded.project <> '' THEN excluded.project ELSE project END,
			last_event_at = excluded.last_event_at
		RETURNING id`, NewID(), machineID, sess.Tool, sess.CLISessionID, sess.Cwd, project, now, now).Scan(&id)
	return id, err
}

const requestCols = "id, machine_id, session_id, kind, tool_name, payload, payload_hash, risk, project_root, engine_reason, state, decided_by, decision_note, rule_id, turn_id, cli_session_id, title, callback, created_at, expires_at, decided_at, delivered_at"

// InsertRequest 插入一条请求。PayloadHash 为空时按存下的载荷 JSON 计算；CreatedAt 为空时用 Now()。
// CreatedAt、ExpiresAt、DecidedAt、DeliveredAt 给了就必须是 FormatTime 的输出，否则返回错误。
func (s *Store) InsertRequest(r Request) error {
	_, err := s.insertRequest(r, "")
	return err
}

// InsertRequestIfAbsent 同 InsertRequest，但 id 已存在时什么也不做并返回 false（ON CONFLICT(id) DO NOTHING）。
// 用于可能重复到达的同一事件（如守护进程重发的审计批次）：并发撞主键也不报错。
func (s *Store) InsertRequestIfAbsent(r Request) (bool, error) {
	res, err := s.insertRequest(r, " ON CONFLICT(id) DO NOTHING")
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// insertRequest 校验并插入，suffix 接在 VALUES(...) 之后（冲突处理子句）。
func (s *Store) insertRequest(r Request, suffix string) (sql.Result, error) {
	payload, err := payloadJSON(r.Payload)
	if err != nil {
		return nil, fmt.Errorf("序列化载荷: %w", err)
	}
	if r.PayloadHash == "" {
		r.PayloadHash = hashJSON(payload)
	}
	if r.CreatedAt == "" {
		r.CreatedAt = Now()
	} else if err := checkTime("created_at", r.CreatedAt); err != nil {
		return nil, err
	}
	for _, c := range []struct {
		col string
		v   *string
	}{{"expires_at", r.ExpiresAt}, {"decided_at", r.DecidedAt}, {"delivered_at", r.DeliveredAt}} {
		if c.v != nil {
			if err := checkTime(c.col, *c.v); err != nil {
				return nil, err
			}
		}
	}
	var sessionID any
	if r.SessionID != "" {
		sessionID = r.SessionID
	}
	return s.db.Exec("INSERT INTO requests("+requestCols+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"+suffix,
		r.ID, r.MachineID, sessionID, r.Kind, r.ToolName, string(payload), r.PayloadHash, r.Risk, r.ProjectRoot, r.EngineReason,
		r.State, r.DecidedBy, r.DecisionNote, r.RuleID, r.TurnID, r.CLISessionID, r.Title, r.Callback, r.CreatedAt, r.ExpiresAt, r.DecidedAt, r.DeliveredAt)
}

func scanRequest(row interface{ Scan(...any) error }) (Request, error) {
	var r Request
	var payload string
	var sessionID sql.NullString
	err := row.Scan(&r.ID, &r.MachineID, &sessionID, &r.Kind, &r.ToolName, &payload, &r.PayloadHash, &r.Risk, &r.ProjectRoot, &r.EngineReason,
		&r.State, &r.DecidedBy, &r.DecisionNote, &r.RuleID, &r.TurnID, &r.CLISessionID, &r.Title, &r.Callback, &r.CreatedAt, &r.ExpiresAt, &r.DecidedAt, &r.DeliveredAt)
	if err != nil {
		return Request{}, err
	}
	r.SessionID = sessionID.String
	if err := json.Unmarshal([]byte(payload), &r.Payload); err != nil {
		return Request{}, err
	}
	return r, nil
}

func (s *Store) queryRequests(query string, args ...any) ([]Request, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRequest 按 id 取请求。
func (s *Store) GetRequest(id string) (Request, error) {
	r, err := scanRequest(s.db.QueryRow("SELECT "+requestCols+" FROM requests WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	return r, err
}

// ListRequests 按创建时间倒序列出；state 为空表示全部。
func (s *Store) ListRequests(state string, limit int) ([]Request, error) {
	if state == "" {
		return s.queryRequests("SELECT "+requestCols+" FROM requests ORDER BY created_at DESC LIMIT ?", limit)
	}
	return s.queryRequests("SELECT "+requestCols+" FROM requests WHERE state = ? ORDER BY created_at DESC LIMIT ?", state, limit)
}

// OpenQuestions 列还在等回答的 question，按创建时间倒序、至多 limit 条：state 为 recorded、不早于 since，
// 且同一会话之后没有任何新行。hub 存的会话行只有 question、done（recorded，见 hub 的 recordEvent）与 permission
// （待决与补的历史行）：之后来了 done、新的 permission 或新的 question，都说明 agent 已经不在等那个问题了。
// 没有会话 id 的 question 关联不到后续事件，视为仍在等（直到滑出 since）。已吊销机器的 question 不列：那台机器已断开，
// 不会再有后续事件让它“不再等”。
// 会话行属于一台机器，子查询加上 machine_id 走 (machine_id, created_at) 索引，不扫全表。
func (s *Store) OpenQuestions(since time.Time, limit int) ([]Request, error) {
	return s.queryRequests("SELECT "+requestCols+` FROM requests q
		WHERE q.kind = 'question' AND q.state = 'recorded' AND q.created_at >= ?
		  AND NOT EXISTS (SELECT 1 FROM requests r
		    WHERE q.session_id IS NOT NULL AND r.machine_id = q.machine_id AND r.created_at > q.created_at AND r.session_id = q.session_id)
		  AND NOT EXISTS (SELECT 1 FROM machines m WHERE m.id = q.machine_id AND m.revoked = 1)
		ORDER BY q.created_at DESC LIMIT ?`, fmtTime(since), limit)
}

// ListPendingForMachine 列出某台机器的待处理请求。
func (s *Store) ListPendingForMachine(machineID string) ([]Request, error) {
	return s.queryRequests("SELECT "+requestCols+" FROM requests WHERE machine_id = ? AND state = 'pending' ORDER BY created_at", machineID)
}

// FindResend 找同一会话、同一回合、同一命令、仍 pending 且不早于 since 的最近一条。
// cliSessionID 为空时直接返回 ErrNotFound（不去重，免得同机器不同会话互相顶掉）；
// turnID 为空时按空串参与匹配：同会话、都没有 turn_id、同工具同载荷的请求在窗口内视为重发。
func (s *Store) FindResend(machineID, cliSessionID, turnID, toolName, payloadHash string, since time.Duration) (Request, error) {
	if cliSessionID == "" {
		return Request{}, ErrNotFound
	}
	cutoff := fmtTime(time.Now().Add(-since))
	r, err := scanRequest(s.db.QueryRow("SELECT "+requestCols+` FROM requests
		WHERE machine_id = ? AND cli_session_id = ? AND turn_id = ? AND tool_name = ? AND payload_hash = ? AND state = 'pending' AND created_at >= ?
		ORDER BY created_at DESC LIMIT 1`, machineID, cliSessionID, turnID, toolName, payloadHash, cutoff))
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	return r, err
}

// ExpiredPending 列出 expires_at 不晚于 now 的待处理请求（恰好等于也算），按创建时间升序；expires_at 为空的不过期。
func (s *Store) ExpiredPending(now time.Time) ([]Request, error) {
	return s.queryRequests("SELECT "+requestCols+" FROM requests WHERE state = 'pending' AND expires_at IS NOT NULL AND expires_at <= ? ORDER BY created_at", fmtTime(now))
}

const setStateSQL = "UPDATE requests SET state = ?, decided_by = ?, decision_note = ?, rule_id = ?, decided_at = ? WHERE state = 'pending' AND id = ?"

// SetRequestState 把 pending 迁到终态并写 decided_at；非 pending 返回 ErrNotPending。供 hub 自己的决定、过期、去重取消用。
func (s *Store) SetRequestState(id, state, decidedBy, note, ruleID string) error {
	return oneRowOrNotPending(s.db.Exec(setStateSQL, state, decidedBy, note, ruleID, Now(), id))
}

// SetMachineRequestState 同 SetRequestState，但只改属于 machineID 的请求；不存在、属于别的机器或非 pending
// 都返回 ErrNotPending。供守护进程发来的 cancel/expired 帧用（hub 不信任守护进程报的请求 id）。
// state 不是 cancelled、expired、passthrough 时返回 ErrInvalidState。
func (s *Store) SetMachineRequestState(machineID, id, state, decidedBy, note, ruleID string) error {
	if err := checkAgentState(state); err != nil {
		return err
	}
	return oneRowOrNotPending(s.db.Exec(setStateSQL+" AND machine_id = ?", state, decidedBy, note, ruleID, Now(), id, machineID))
}

// checkAgentState 是守护进程来源写入的状态白名单：只有本机终态 cancelled、expired、passthrough。
// 守护进程不能把请求迁回 pending、写成 recorded，也不能冒充用户或规则的决定（approved、denied、auto_*）。
func checkAgentState(state string) error {
	switch state {
	case "cancelled", "expired", "passthrough":
		return nil
	}
	return fmt.Errorf("%w: %q", ErrInvalidState, state)
}

// oneRowOrNotPending 把“恰好更新一行”之外的结果转成 ErrNotPending。
func oneRowOrNotPending(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotPending
	}
	return nil
}

// ApplyAgentFinal 用守护进程审计里的本机终态覆盖 hub 状态，写 decided_at，返回是否更新了。
// 本机对钩子发生了什么是唯一真相，所以除 pending 外，也覆盖重连对齐时标的 cancelled/agent_restart；
// 其他终态（用户或规则的决定、去重取消等）不动。只改属于 machineID 的请求。
// state 不是 cancelled、expired、passthrough 时返回 ErrInvalidState（例如 pending 会把 cancelled/agent_restart 复活）。
func (s *Store) ApplyAgentFinal(machineID, id, state, decidedBy, note, ruleID string) (bool, error) {
	if err := checkAgentState(state); err != nil {
		return false, err
	}
	res, err := s.db.Exec(`UPDATE requests SET state = ?, decided_by = ?, decision_note = ?, rule_id = ?, decided_at = ?
		WHERE id = ? AND machine_id = ? AND (state = 'pending' OR (state = 'cancelled' AND decided_by = ?))`,
		state, decidedBy, note, ruleID, Now(), id, machineID, DecidedByAgentRestart)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// MarkDelivered 记录决定已送到 machineID 的守护进程，返回是否真的更新了这一行（调用方据此推送送达变化）。
// 只认该机器、已决定（approved、denied）的请求：对仍 pending 的回执不记，免得之后的决定不再补发；
// 重复送达不覆盖第一次的时间（返回 false）。
func (s *Store) MarkDelivered(machineID, id string) (bool, error) {
	res, err := s.db.Exec("UPDATE requests SET delivered_at = ? WHERE id = ? AND machine_id = ? AND state IN ('approved','denied') AND delivered_at IS NULL", Now(), id, machineID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// UndeliveredDecisions 列出已决定但未送达的请求，重连后补发。
func (s *Store) UndeliveredDecisions(machineID string) ([]Request, error) {
	return s.queryRequests("SELECT "+requestCols+" FROM requests WHERE machine_id = ? AND state IN ('approved','denied') AND delivered_at IS NULL ORDER BY created_at", machineID)
}

// CountPending 统计待处理数。
func (s *Store) CountPending() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM requests WHERE state = 'pending'").Scan(&n)
	return n, err
}

// ClearCallbackToken 去掉 webhook 请求 callback（JSON 对象，回调令牌在 token 键）里的回调令牌，URL 保留：
// 回调成功或重试用尽后调用，明文令牌不再留在库里。callback 不是合法 JSON 时整个清空（宁可丢 URL 也不留可能的令牌）。
// 可重复调用；没有这条请求时返回 ErrNotFound。
func (s *Store) ClearCallbackToken(id string) error {
	res, err := s.db.Exec(`UPDATE requests SET callback = CASE WHEN json_valid(callback) THEN json_remove(callback, '$.token') ELSE '' END
		WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}
