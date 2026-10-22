package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

// ErrNotPending 表示请求已经不是待处理状态。
var ErrNotPending = errors.New("请求已不是待处理状态")

// Request 是 requests 表的一行。question/done 也存成一行：Kind 为 question|done，State 为 recorded，
// Title 为脱敏后的消息，Payload 为空值，ExpiresAt 为 nil。
type Request struct {
	ID           string
	MachineID    string
	SessionID    string
	Kind         string
	ToolName     string
	Payload      policy.Payload
	PayloadHash  string
	Risk         string
	ProjectRoot  string
	EngineReason string
	State        string
	DecidedBy    string
	DecisionNote string
	RuleID       string
	TurnID       string
	CLISessionID string
	Title        string
	Callback     string
	CreatedAt    string
	ExpiresAt    *string
	DecidedAt    *string
	DeliveredAt  *string
}

// PayloadHash 是载荷 JSON 的 SHA-256，用于去重。
func PayloadHash(p policy.Payload) string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// UpsertSession 登记或刷新会话，返回会话行 id。已存在时刷新 cwd、project 与 last_event_at，started_at 不变。
func (s *Store) UpsertSession(machineID string, sess proto.Session, project string) (string, error) {
	now := Now()
	var id string
	err := s.db.QueryRow(`INSERT INTO sessions(id, machine_id, tool, cli_session_id, cwd, project, started_at, last_event_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(machine_id, tool, cli_session_id) DO UPDATE SET cwd = excluded.cwd, project = excluded.project, last_event_at = excluded.last_event_at
		RETURNING id`, NewID(), machineID, sess.Tool, sess.CLISessionID, sess.Cwd, project, now, now).Scan(&id)
	return id, err
}

const requestCols = "id, machine_id, session_id, kind, tool_name, payload, payload_hash, risk, project_root, engine_reason, state, decided_by, decision_note, rule_id, turn_id, cli_session_id, title, callback, created_at, expires_at, decided_at, delivered_at"

// InsertRequest 插入一条请求。
func (s *Store) InsertRequest(r Request) error {
	if r.PayloadHash == "" {
		r.PayloadHash = PayloadHash(r.Payload)
	}
	if r.CreatedAt == "" {
		r.CreatedAt = Now()
	}
	payload, err := json.Marshal(r.Payload)
	if err != nil {
		return err
	}
	var sessionID any
	if r.SessionID != "" {
		sessionID = r.SessionID
	}
	_, err = s.db.Exec("INSERT INTO requests("+requestCols+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		r.ID, r.MachineID, sessionID, r.Kind, r.ToolName, string(payload), r.PayloadHash, r.Risk, r.ProjectRoot, r.EngineReason,
		r.State, r.DecidedBy, r.DecisionNote, r.RuleID, r.TurnID, r.CLISessionID, r.Title, r.Callback, r.CreatedAt, r.ExpiresAt, r.DecidedAt, r.DeliveredAt)
	return err
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

// ListPendingForMachine 列出某台机器的待处理请求。
func (s *Store) ListPendingForMachine(machineID string) ([]Request, error) {
	return s.queryRequests("SELECT "+requestCols+" FROM requests WHERE machine_id = ? AND state = 'pending' ORDER BY created_at", machineID)
}

// FindResend 找同一会话、同一回合、同一命令、仍 pending 且不早于 since 的最近一条。
func (s *Store) FindResend(machineID, cliSessionID, turnID, toolName, payloadHash string, since time.Duration) (Request, error) {
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
func (s *Store) SetMachineRequestState(machineID, id, state, decidedBy, note, ruleID string) error {
	return oneRowOrNotPending(s.db.Exec(setStateSQL+" AND machine_id = ?", state, decidedBy, note, ruleID, Now(), id, machineID))
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
func (s *Store) ApplyAgentFinal(machineID, id, state, decidedBy, note, ruleID string) (bool, error) {
	res, err := s.db.Exec(`UPDATE requests SET state = ?, decided_by = ?, decision_note = ?, rule_id = ?, decided_at = ?
		WHERE id = ? AND machine_id = ? AND (state = 'pending' OR (state = 'cancelled' AND decided_by = 'agent_restart'))`,
		state, decidedBy, note, ruleID, Now(), id, machineID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

// MarkDelivered 记录决定已送到 machineID 的守护进程；只认该机器的请求，重复送达不覆盖第一次的时间。
func (s *Store) MarkDelivered(machineID, id string) error {
	_, err := s.db.Exec("UPDATE requests SET delivered_at = ? WHERE id = ? AND machine_id = ? AND delivered_at IS NULL", Now(), id, machineID)
	return err
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
