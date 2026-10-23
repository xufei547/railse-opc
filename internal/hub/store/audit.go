package store

// AuditEvent 是 audit_events 表的一行，只追加。API 直接输出它，json 字段为 snake_case。
type AuditEvent struct {
	ID        int64  `json:"id"`
	TS        string `json:"ts"`
	MachineID string `json:"machine_id"`
	Actor     string `json:"actor"`
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	Detail    string `json:"detail"`
}

// defaultAuditLimit 是 ListAudit 的 limit ≤ 0 时的条数上限（SQLite 的 LIMIT -1 等于不限）。
const defaultAuditLimit = 200

// InsertAudit 追加一条审计。TS 为空时用 Now()；给了就必须是 FormatTime 的输出，否则返回错误。
func (s *Store) InsertAudit(e AuditEvent) error {
	if e.TS == "" {
		e.TS = Now()
	} else if err := checkTime("ts", e.TS); err != nil {
		return err
	}
	_, err := s.db.Exec("INSERT INTO audit_events(ts, machine_id, actor, request_id, action, detail) VALUES(?, ?, ?, ?, ?, ?)",
		e.TS, e.MachineID, e.Actor, e.RequestID, e.Action, e.Detail)
	return err
}

// ListAudit 倒序列出审计，requestID 为空表示全部；limit ≤ 0 时按 defaultAuditLimit。
func (s *Store) ListAudit(limit int, requestID string) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = defaultAuditLimit
	}
	q := "SELECT id, ts, machine_id, actor, request_id, action, detail FROM audit_events"
	var args []any
	if requestID != "" {
		q += " WHERE request_id = ?"
		args = append(args, requestID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.TS, &e.MachineID, &e.Actor, &e.RequestID, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
