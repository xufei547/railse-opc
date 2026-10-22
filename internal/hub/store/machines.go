package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// ErrInvalidToken 表示接入码不存在、已用或过期。
var ErrInvalidToken = errors.New("接入码无效或已过期")

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
	_, err := s.db.Exec("INSERT INTO enroll_tokens(token_hash, expires_at) VALUES(?, ?)", HashToken(raw), fmtTime(time.Now().Add(ttl)))
	return raw, err
}

// ConsumeEnrollToken 校验并作废接入码。一条带条件的 UPDATE 以受影响行数判定，并发消费只有一次成功。
func (s *Store) ConsumeEnrollToken(raw string) error {
	res, err := s.db.Exec("UPDATE enroll_tokens SET used = 1 WHERE token_hash = ? AND used = 0 AND expires_at > ?", HashToken(raw), Now())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrInvalidToken
	}
	return nil
}

// CreateMachine 登记机器，存密钥哈希。
func (s *Store) CreateMachine(name, rawSecret, homeDir string) (Machine, error) {
	m := Machine{ID: NewID(), Name: name, HomeDir: homeDir, EnrolledAt: Now()}
	_, err := s.db.Exec("INSERT INTO machines(id, name, secret_hash, home_dir, enrolled_at) VALUES(?, ?, ?, ?, ?)", m.ID, m.Name, HashToken(rawSecret), homeDir, m.EnrolledAt)
	return m, err
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
	_, err := s.db.Exec("INSERT INTO client_sessions(token_hash, created_at, expires_at) VALUES(?, ?, ?)", HashToken(raw), Now(), fmtTime(time.Now().Add(ttl)))
	return raw, err
}

// ValidateClientSession 判断 token 是否有效，顺手清理过期行。
func (s *Store) ValidateClientSession(raw string) (bool, error) {
	if _, err := s.db.Exec("DELETE FROM client_sessions WHERE expires_at <= ?", Now()); err != nil {
		return false, err
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM client_sessions WHERE token_hash = ?", HashToken(raw)).Scan(&n); err != nil {
		return false, err
	}
	return n == 1, nil
}

// DeleteClientSession 登出。
func (s *Store) DeleteClientSession(raw string) error {
	_, err := s.db.Exec("DELETE FROM client_sessions WHERE token_hash = ?", HashToken(raw))
	return err
}
