package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// PushSubscription 是一条 Web Push 订阅。P256dh、Auth 是浏览器给的 base64url 串，明文存（设计 5.3 例外）。
type PushSubscription struct {
	ID        string
	Endpoint  string
	P256dh    string
	Auth      string
	UserAgent string
	CreatedAt string
	LastOkAt  *string
	Failures  int
}

type pushKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

const pushCols = "id, endpoint, keys, user_agent, created_at, last_ok_at, failures"

// UpsertPushSubscription 按端点登记或更新订阅：同一端点重新订阅时换 keys 与 UA，连续失败归零，id 不变。
func (s *Store) UpsertPushSubscription(endpoint, p256dh, auth, userAgent string) (PushSubscription, error) {
	if endpoint == "" || p256dh == "" || auth == "" {
		return PushSubscription{}, errors.New("订阅缺 endpoint 或 keys")
	}
	keys, err := json.Marshal(pushKeys{P256dh: p256dh, Auth: auth})
	if err != nil {
		return PushSubscription{}, err
	}
	_, err = s.db.Exec(`INSERT INTO push_subscriptions(id, endpoint, keys, user_agent, created_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET keys = excluded.keys, user_agent = excluded.user_agent, failures = 0`,
		NewID(), endpoint, string(keys), userAgent, Now())
	if err != nil {
		return PushSubscription{}, fmt.Errorf("登记推送订阅: %w", err)
	}
	return s.GetPushSubscription(endpoint)
}

func scanPush(row interface{ Scan(...any) error }) (PushSubscription, error) {
	var p PushSubscription
	var keys string
	if err := row.Scan(&p.ID, &p.Endpoint, &keys, &p.UserAgent, &p.CreatedAt, &p.LastOkAt, &p.Failures); err != nil {
		return PushSubscription{}, err
	}
	var k pushKeys
	if err := json.Unmarshal([]byte(keys), &k); err != nil {
		return PushSubscription{}, fmt.Errorf("订阅 %s 的 keys 不是 JSON: %w", p.ID, err)
	}
	p.P256dh, p.Auth = k.P256dh, k.Auth
	return p, nil
}

// GetPushSubscription 按端点取订阅。
func (s *Store) GetPushSubscription(endpoint string) (PushSubscription, error) {
	p, err := scanPush(s.db.QueryRow("SELECT "+pushCols+" FROM push_subscriptions WHERE endpoint = ?", endpoint))
	if errors.Is(err, sql.ErrNoRows) {
		return PushSubscription{}, ErrNotFound
	}
	return p, err
}

// ListPushSubscriptions 按登记时间列全部订阅。
func (s *Store) ListPushSubscriptions() ([]PushSubscription, error) {
	rows, err := s.db.Query("SELECT " + pushCols + " FROM push_subscriptions ORDER BY created_at, rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		p, err := scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePushSubscription 删订阅，返回是否真的删了一行。
func (s *Store) DeletePushSubscription(endpoint string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM push_subscriptions WHERE endpoint = ?", endpoint)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkPushOK 记一次成功：last_ok_at 更新，连续失败归零。
func (s *Store) MarkPushOK(endpoint string) error {
	_, err := s.db.Exec("UPDATE push_subscriptions SET last_ok_at = ?, failures = 0 WHERE endpoint = ?", Now(), endpoint)
	return err
}

// MarkPushFailure 连续失败加一，返回新值（端点不存在时返回 0）。
func (s *Store) MarkPushFailure(endpoint string) (int, error) {
	var n int
	err := s.db.QueryRow("UPDATE push_subscriptions SET failures = failures + 1 WHERE endpoint = ? RETURNING failures", endpoint).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// CountPushSubscriptions 返回订阅数。
func (s *Store) CountPushSubscriptions() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM push_subscriptions").Scan(&n)
	return n, err
}
