package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// PushSubscription 是一条 Web Push 订阅。P256dh、Auth 是浏览器给的 base64url 串，明文存（设计 5.3 例外）：
// 它们不得进任何接口响应、日志或审计，hub 的接口用自己的 DTO，不直接编码这个类型。
type PushSubscription struct {
	ID        string
	Endpoint  string
	P256dh    string
	Auth      string
	UserAgent string
	CreatedAt string
	LastOkAt  *string
	Failures  int
	// Broken 表示 keys 列解不开（迁移来的旧行、手改的库）：P256dh、Auth 为空。这样的行照样列出、不让整个列表失败，
	// 由调用方记日志并删掉。
	Broken bool
}

type pushKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

const pushCols = "id, endpoint, keys, user_agent, created_at, last_ok_at, failures"

// UpsertPushSubscription 按端点登记或更新订阅：同一端点重新订阅时换 keys 与 UA，连续失败归零，id、created_at 不变。
// 一条语句写入并扫回写进去的行（RETURNING）：不会在写与读之间被别处删掉而返回 ErrNotFound。
func (s *Store) UpsertPushSubscription(endpoint, p256dh, auth, userAgent string) (PushSubscription, error) {
	if endpoint == "" || p256dh == "" || auth == "" {
		return PushSubscription{}, errors.New("订阅缺 endpoint 或 keys")
	}
	keys, err := json.Marshal(pushKeys{P256dh: p256dh, Auth: auth})
	if err != nil {
		return PushSubscription{}, err
	}
	p, err := scanPush(s.db.QueryRow(`INSERT INTO push_subscriptions(id, endpoint, keys, user_agent, created_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET keys = excluded.keys, user_agent = excluded.user_agent, failures = 0
		RETURNING `+pushCols, NewID(), endpoint, string(keys), userAgent, Now()))
	if err != nil {
		return PushSubscription{}, fmt.Errorf("登记推送订阅: %w", err)
	}
	return p, nil
}

// scanPush 扫一行订阅。keys 列解不开时不报错，标 Broken（见 PushSubscription）。错误里不带 keys 的内容。
func scanPush(row interface{ Scan(...any) error }) (PushSubscription, error) {
	var p PushSubscription
	var keys string
	if err := row.Scan(&p.ID, &p.Endpoint, &keys, &p.UserAgent, &p.CreatedAt, &p.LastOkAt, &p.Failures); err != nil {
		return PushSubscription{}, err
	}
	var k pushKeys
	if err := json.Unmarshal([]byte(keys), &k); err != nil {
		p.Broken = true
		return p, nil
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

// ListPushSubscriptions 按登记时间列全部订阅。keys 坏掉的行也列出（Broken），不让整个列表失败。
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

// MarkPushFailure 连续失败加一，返回新值；新值达到 limit（limit > 0）时在同一事务里删掉这条订阅，removed 为 true。
// 端点不存在时返回 0、false。同一事务里先加后删：用户在两次失败之间重新订阅（failures 归零）后，
// 不会因为之前数到的次数把刚登记的订阅删掉。
func (s *Store) MarkPushFailure(endpoint string, limit int) (failures int, removed bool, err error) {
	err = s.Tx(func(tx *sql.Tx) error {
		err := tx.QueryRow("UPDATE push_subscriptions SET failures = failures + 1 WHERE endpoint = ? RETURNING failures", endpoint).Scan(&failures)
		if errors.Is(err, sql.ErrNoRows) {
			failures = 0
			return nil
		}
		if err != nil {
			return err
		}
		if limit > 0 && failures >= limit {
			if _, err := tx.Exec("DELETE FROM push_subscriptions WHERE endpoint = ?", endpoint); err != nil {
				return err
			}
			removed = true
		}
		return nil
	})
	if err != nil {
		return 0, false, err
	}
	return failures, removed, nil
}

// ReplacePushKey 在一个事务里把设置 key 改成 value 并删掉全部推送订阅，返回删了几条。换 VAPID 密钥用：
// 旧订阅是对旧公钥建立的，新密钥签的推送会被推送服务拒绝。
func (s *Store) ReplacePushKey(key, value string) (int, error) {
	var n int64
	err := s.Tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value); err != nil {
			return err
		}
		res, err := tx.Exec("DELETE FROM push_subscriptions")
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// CountPushSubscriptions 返回订阅数。
func (s *Store) CountPushSubscriptions() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM push_subscriptions").Scan(&n)
	return n, err
}
