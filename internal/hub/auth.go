package hub

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/railse/railse-opc/internal/hub/store"
)

const (
	cookieName     = "railse_session"
	argonTime      = 1
	argonMemory    = 64 * 1024 // KiB，即 64 MiB
	argonThreads   = 4
	argonSaltLen   = 16
	argonKeyLen    = 32
	loginMax       = 5
	loginWindow    = time.Minute
	enrollTokenTTL = 10 * time.Minute
	loginBodyMax   = 4 << 10
	enrollBodyMax  = 16 << 10
	maxNameLen     = 128  // 字节
	maxHomeDirLen  = 4096 // 字节
	sweepEvery     = time.Minute
)

// argonHeader 是编码串里算法、版本与参数三段，HashPassword 写出的与 VerifyPassword 接受的完全一致。
var argonHeader = fmt.Sprintf("argon2id$v=%d$m=%d,t=%d,p=%d", argon2.Version, argonMemory, argonTime, argonThreads)

// b64 是编码串里盐与哈希的编码：base64 raw std，严格模式拒绝非规范的尾部比特。
var b64 = base64.RawStdEncoding.Strict()

// HashPassword 用 argon2id 哈希密码，编码为 argon2id$v=19$m=65536,t=1,p=4$<盐>$<哈希>。
func HashPassword(pw string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return argonHeader + "$" + b64.EncodeToString(salt) + "$" + b64.EncodeToString(key), nil
}

// VerifyPassword 校验密码。编码串严格解析：算法、版本、参数必须与 HashPassword 写出的完全一致，
// 盐 16 字节、哈希 32 字节，否则一律 false。不按串里的参数计算，畸形参数既不会让 argon2 panic，也不会耗尽内存。
func VerifyPassword(encoded, pw string) bool {
	rest, ok := strings.CutPrefix(encoded, argonHeader+"$")
	if !ok {
		return false
	}
	saltStr, keyStr, ok := strings.Cut(rest, "$")
	if !ok {
		return false
	}
	salt, err := b64.DecodeString(saltStr)
	if err != nil || len(salt) != argonSaltLen {
		return false
	}
	want, err := b64.DecodeString(keyStr)
	if err != nil || len(want) != argonKeyLen {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// SetPassword 设置或更换网页登录密码。settings 的 password_hash 只存 argon2id 编码串。
func (s *Server) SetPassword(pw string) error {
	if len(pw) < 8 {
		return errors.New("密码至少 8 个字符")
	}
	enc, err := HashPassword(pw)
	if err != nil {
		return err
	}
	return s.store.SetSetting("password_hash", enc)
}

// limiter 是按键的滑动窗口计数器。每个键只留窗口内的时间戳（至多 max 个）；
// 每隔 sweepEvery 顺带清掉已整段滑出窗口的键，来源 IP 再多，表也不会无限增长。
type limiter struct {
	mu        sync.Mutex
	now       func() time.Time
	hits      map[string]hitLog
	lastSweep time.Time
}

// hitLog 是一个键在窗口内的放行时间（按先后），连同该键的窗口长度，清理时按各自的窗口判断。
type hitLog struct {
	times  []time.Time
	window time.Duration
}

func newLimiter() *limiter { return &limiter{now: time.Now, hits: map[string]hitLog{}} }

// allow 报告 key 这次是否放行：窗口内已放行 max 次就拒绝。被拒的尝试不计数。
func (l *limiter) allow(key string, max int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)
	var kept []time.Time
	for _, t := range l.hits[key].times {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	ok := len(kept) < max
	if ok {
		kept = append(kept, now)
	}
	if len(kept) == 0 {
		delete(l.hits, key)
	} else {
		l.hits[key] = hitLog{times: kept, window: window}
	}
	return ok
}

// sweep 删掉最后一次放行已滑出各自窗口的键，至多每 sweepEvery 跑一次。调用方持有锁。
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < sweepEvery {
		return
	}
	l.lastSweep = now
	for k, h := range l.hits {
		if len(h.times) == 0 || now.Sub(h.times[len(h.times)-1]) >= h.window {
			delete(l.hits, k)
		}
	}
}

// clientIP 取 TCP 对端地址，不信任 X-Forwarded-For 之类可伪造的头。
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// sessionCookie 是登录会话 cookie：HttpOnly、SameSite=Lax、Path=/，配置要求或请求经 TLS 时加 Secure。
func (s *Server) sessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: cookieName, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.cfg.SecureCookies || r.TLS != nil, MaxAge: maxAge}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("login:"+clientIP(r), loginMax, loginWindow) {
		writeErr(w, http.StatusTooManyRequests, "尝试太频繁，一分钟后再试")
		return
	}
	enc, ok, err := s.store.GetSetting("password_hash")
	if err != nil {
		s.internalErr(w, "读密码哈希", err)
		return
	}
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "还没有设置密码，请在服务器上运行 railse hub password")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, loginBodyMax, &body); err != nil || body.Password == "" {
		writeErr(w, http.StatusBadRequest, "需要 password")
		return
	}
	if !VerifyPassword(enc, body.Password) {
		writeErr(w, http.StatusUnauthorized, "密码错误")
		return
	}
	tok, err := s.store.CreateClientSession(s.cfg.SessionTTL)
	if err != nil {
		s.internalErr(w, "建登录会话", err)
		return
	}
	http.SetCookie(w, s.sessionCookie(r, tok, int(s.cfg.SessionTTL.Seconds())))
	w.WriteHeader(http.StatusNoContent)
}

// handleLogout 删掉服务端会话并清除 cookie。删除失败时回 500 且不清 cookie，免得用户以为已登出而会话仍有效。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		if err := s.store.DeleteClientSession(c.Value); err != nil {
			s.internalErr(w, "删登录会话", err)
			return
		}
	}
	http.SetCookie(w, s.sessionCookie(r, "", -1))
	w.WriteHeader(http.StatusNoContent)
}

// requireClient 要求有效的登录 cookie。
func (s *Server) requireClient(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		ok, err := s.store.ValidateClientSession(c.Value)
		if err != nil {
			s.internalErr(w, "校验登录会话", err)
			return
		}
		if !ok {
			writeErr(w, http.StatusUnauthorized, "登录已过期")
			return
		}
		next(w, r)
	}
}

// handleEnroll 用一次性接入码换机器长期密钥。消费接入码与登记机器在同一事务里，登记失败时接入码仍可用。
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("enroll:"+clientIP(r), loginMax, loginWindow) {
		writeErr(w, http.StatusTooManyRequests, "尝试太频繁")
		return
	}
	var body struct {
		Token   string `json:"token"`
		Name    string `json:"name"`
		HomeDir string `json:"home_dir"`
	}
	if err := decodeJSON(w, r, enrollBodyMax, &body); err != nil || body.Token == "" || body.Name == "" {
		writeErr(w, http.StatusBadRequest, "需要 token 与 name")
		return
	}
	if len(body.Name) > maxNameLen || len(body.HomeDir) > maxHomeDirLen {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("name 至多 %d 字节，home_dir 至多 %d 字节", maxNameLen, maxHomeDirLen))
		return
	}
	secret := store.RandomToken()
	m, err := s.store.EnrollMachine(body.Token, body.Name, secret, body.HomeDir)
	if errors.Is(err, store.ErrInvalidToken) {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		s.internalErr(w, "登记机器", err)
		return
	}
	detail, _ := json.Marshal(map[string]string{"name": m.Name}) // map[string]string 编码不会失败
	if err := s.store.InsertAudit(store.AuditEvent{MachineID: m.ID, Actor: "system", Action: "machine_enrolled", Detail: string(detail)}); err != nil {
		s.log.Printf("记接入审计 %s: %v", m.ID, err)
	}
	writeJSON(w, http.StatusOK, map[string]string{"machine_id": m.ID, "secret": secret})
}

// authMachine 从 Authorization: Bearer 取机器密钥。
func (s *Server) authMachine(r *http.Request) (store.Machine, error) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	secret = strings.TrimSpace(secret)
	if !ok || secret == "" {
		return store.Machine{}, errors.New("缺少 Bearer 密钥")
	}
	return s.store.AuthMachine(secret)
}
