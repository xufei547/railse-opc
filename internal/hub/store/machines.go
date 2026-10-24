package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// ErrInvalidToken 表示接入码不存在、已用或过期。
var ErrInvalidToken = errors.New("接入码无效或已过期")

// errEmptySecret：空串的哈希是公开常量，登记了就能被空 Bearer 认证，机器密钥一律不许为空。
var errEmptySecret = errors.New("机器密钥不能为空")

// execer 是 *sql.DB 与 *sql.Tx 共有的写方法：同一段写入既能在事务外用 s.db，也能在 Tx 回调里只用 tx。
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// Machine 是一台接入的机器。
type Machine struct {
	ID           string
	Name         string
	HomeDir      string
	AgentVersion string
	EnrolledAt   string
	LastSeen     *string
	Revoked      bool
}

// NewID 生成 32 个十六进制字符的随机 id。
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("随机数不可用: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// RandomToken 生成 32 字节随机数的 base64url。
func RandomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("随机数不可用: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// HashToken 返回 SHA-256 十六进制，数据库里只存哈希。
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// CreateEnrollToken 生成一次性接入码。
func (s *Store) CreateEnrollToken(ttl time.Duration) (string, error) {
	raw := RandomToken()
	if _, err := s.db.Exec("INSERT INTO enroll_tokens(token_hash, expires_at) VALUES(?, ?)", HashToken(raw), fmtTime(time.Now().Add(ttl))); err != nil {
		return "", err
	}
	return raw, nil
}

// ConsumeEnrollToken 校验并作废接入码。一条带条件的 UPDATE 以受影响行数判定，并发消费只有一次成功。
func (s *Store) ConsumeEnrollToken(raw string) error { return consumeEnrollToken(s.db, raw) }

func consumeEnrollToken(e execer, raw string) error {
	res, err := e.Exec("UPDATE enroll_tokens SET used = 1 WHERE token_hash = ? AND used = 0 AND expires_at > ?", HashToken(raw), Now())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalidToken
	}
	return nil
}

// CreateMachine 登记机器，存密钥哈希；密钥为空时报错。
func (s *Store) CreateMachine(name, rawSecret, homeDir string) (Machine, error) {
	return insertMachine(s.db, name, rawSecret, homeDir)
}

// EnrollMachine 在一个事务里消费接入码并登记机器：接入码不存在、已用或过期返回 ErrInvalidToken；
// 登记失败（含密钥为空）整体回滚，接入码仍可用。并发用同一接入码只有一次成功。
// 自开事务，不得在 Tx 回调里调用（单连接会永久自锁）。
func (s *Store) EnrollMachine(rawToken, name, rawSecret, homeDir string) (Machine, error) {
	var m Machine
	err := s.Tx(func(tx *sql.Tx) error {
		if err := consumeEnrollToken(tx, rawToken); err != nil {
			return err
		}
		var err error
		m, err = insertMachine(tx, name, rawSecret, homeDir)
		return err
	})
	if err != nil {
		return Machine{}, err
	}
	return m, nil
}

// insertMachine 插入一台机器，只存密钥哈希。失败时返回零值，不给出看似可用的机器。
func insertMachine(e execer, name, rawSecret, homeDir string) (Machine, error) {
	if rawSecret == "" {
		return Machine{}, errEmptySecret
	}
	m := Machine{ID: NewID(), Name: name, HomeDir: homeDir, EnrolledAt: Now()}
	if _, err := e.Exec("INSERT INTO machines(id, name, secret_hash, home_dir, enrolled_at) VALUES(?, ?, ?, ?, ?)", m.ID, m.Name, HashToken(rawSecret), homeDir, m.EnrolledAt); err != nil {
		return Machine{}, err
	}
	return m, nil
}

const machineCols = "id, name, home_dir, agent_version, enrolled_at, last_seen, revoked"

func scanMachine(row interface{ Scan(...any) error }) (Machine, error) {
	var m Machine
	var revoked int
	err := row.Scan(&m.ID, &m.Name, &m.HomeDir, &m.AgentVersion, &m.EnrolledAt, &m.LastSeen, &revoked)
	m.Revoked = revoked != 0
	return m, err
}

// AuthMachine 按密钥找机器，已吊销视为不存在。
func (s *Store) AuthMachine(rawSecret string) (Machine, error) {
	m, err := scanMachine(s.db.QueryRow("SELECT "+machineCols+" FROM machines WHERE secret_hash = ? AND revoked = 0", HashToken(rawSecret)))
	if errors.Is(err, sql.ErrNoRows) {
		return Machine{}, ErrNotFound
	}
	return m, err
}

// GetMachine 按 id 取机器。
func (s *Store) GetMachine(id string) (Machine, error) {
	m, err := scanMachine(s.db.QueryRow("SELECT "+machineCols+" FROM machines WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Machine{}, ErrNotFound
	}
	return m, err
}

// TouchMachine 更新心跳、版本与家目录。
func (s *Store) TouchMachine(id, agentVersion, homeDir string) error {
	_, err := s.db.Exec("UPDATE machines SET last_seen = ?, agent_version = ?, home_dir = ? WHERE id = ?", Now(), agentVersion, homeDir, id)
	return err
}

// ListMachines 按接入时间列出全部机器。
func (s *Store) ListMachines() ([]Machine, error) {
	rows, err := s.db.Query("SELECT " + machineCols + " FROM machines ORDER BY enrolled_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RevokeMachine 吊销机器，其密钥立即失效。
func (s *Store) RevokeMachine(id string) error {
	_, err := s.db.Exec("UPDATE machines SET revoked = 1 WHERE id = ?", id)
	return err
}

// CreateClientSession 新建登录会话 token。
func (s *Store) CreateClientSession(ttl time.Duration) (string, error) {
	raw := RandomToken()
	if _, err := s.db.Exec("INSERT INTO client_sessions(token_hash, created_at, expires_at) VALUES(?, ?, ?)", HashToken(raw), Now(), fmtTime(time.Now().Add(ttl))); err != nil {
		return "", err
	}
	return raw, nil
}

// ValidateClientSession 判断 token 是否有效（存在且未过期），顺手清理过期行。
func (s *Store) ValidateClientSession(raw string) (bool, error) {
	now := Now()
	// 清理尽力而为：失败不影响结果（下面的 SELECT 自己按 expires_at 判定），错误有意不返回，过期行留到下次再清。
	_, _ = s.db.Exec("DELETE FROM client_sessions WHERE expires_at <= ?", now)
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM client_sessions WHERE token_hash = ? AND expires_at > ?", HashToken(raw), now).Scan(&n); err != nil {
		return false, err
	}
	return n == 1, nil
}

// PasswordChange 是换密码时一并删掉的登录会话与推送订阅的条数。
type PasswordChange struct {
	Sessions          int
	PushSubscriptions int
}

// SetPasswordHash 在一个事务里写入网页登录密码哈希（settings 的 password_hash）、删掉全部登录会话与全部推送订阅，
// 并记审计 push_subscriptions_cleared（actor system，detail 只有 {"reason":"password_changed","count":n}，
// 不含端点与密钥）：换密码后旧 cookie 立即失效，所有设备也不再收到推送——设备丢了时换密码就切断它，
// 其余设备重新登录后在网页里重新开启通知。任一步失败整体回滚，密码、会话、订阅都不变。
// 自开事务，不得在 Tx 回调里调用（单连接会永久自锁）。
func (s *Store) SetPasswordHash(encoded string) (PasswordChange, error) {
	var ch PasswordChange
	err := s.Tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO settings(key, value) VALUES('password_hash', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", encoded); err != nil {
			return err
		}
		var err error
		if ch.Sessions, err = deleteAll(tx, "client_sessions"); err != nil {
			return err
		}
		if ch.PushSubscriptions, err = deleteAll(tx, "push_subscriptions"); err != nil {
			return err
		}
		detail, err := json.Marshal(struct {
			Reason string `json:"reason"`
			Count  int    `json:"count"`
		}{"password_changed", ch.PushSubscriptions})
		if err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO audit_events(ts, actor, action, detail) VALUES(?, 'system', 'push_subscriptions_cleared', ?)", Now(), string(detail))
		return err
	})
	if err != nil {
		return PasswordChange{}, err
	}
	return ch, nil
}

// deleteAll 删光表 table（只用于代码里的常量表名）并返回删了几行。
func deleteAll(tx *sql.Tx, table string) (int, error) {
	res, err := tx.Exec("DELETE FROM " + table)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// DeleteClientSession 登出。
func (s *Store) DeleteClientSession(raw string) error {
	_, err := s.db.Exec("DELETE FROM client_sessions WHERE token_hash = ?", HashToken(raw))
	return err
}
