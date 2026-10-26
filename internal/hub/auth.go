package hub

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

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
	maxHomeDirLen  = 4096 // 字节
	sweepEvery     = time.Minute
	// argonConcurrency 是同时进行的登录密码校验上限（每次 64 MiB），loginVerifyWait 是等名额的上限。
	argonConcurrency = 2
	loginVerifyWait  = 2 * time.Second
)

// argonSlots 是登录密码校验的包级信号量：多个来源同时猛试也只有 argonConcurrency 个 argon2 在算，内存与 CPU 有上限。
// 不另加全局登录限速（那会让攻击者远程锁死主人）；暴力破解靠按来源（IPv6 按 /64）限速。
var argonSlots = make(chan struct{}, argonConcurrency)

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

// SetPassword 设置或更换网页登录密码（至少 8 个字符，按 rune 计）。settings 的 password_hash 只存 argon2id 编码串；
// 写入密码、删光已有登录会话与全部推送订阅在同一事务里（审计 push_subscriptions_cleared）：换密码后旧 cookie 立即失效，
// 任何设备都不再收到推送，各设备重新登录后在网页里重新开启通知。返回删掉的会话与订阅数；日志只记条数。
func (s *Server) SetPassword(pw string) (store.PasswordChange, error) {
	if utf8.RuneCountInString(pw) < 8 {
		return store.PasswordChange{}, errors.New("密码至少 8 个字符")
	}
	enc, err := HashPassword(pw)
	if err != nil {
		return store.PasswordChange{}, err
	}
	ch, err := s.store.SetPasswordHash(enc)
	if err != nil {
		return store.PasswordChange{}, err
	}
	s.log.Printf("已换网页登录密码：删除了 %d 个登录会话、%d 条推送订阅", ch.Sessions, ch.PushSubscriptions)
	return ch, nil
}

// verifyLogin 在信号量名额内校验登录密码。等 verifyWait 仍拿不到名额（或请求已取消）时 busy 为 true，不做校验。
func (s *Server) verifyLogin(ctx context.Context, encoded, pw string) (ok, busy bool) {
	timer := time.NewTimer(s.verifyWait)
	defer timer.Stop()
	select {
	case argonSlots <- struct{}{}:
	case <-timer.C:
		return false, true
	case <-ctx.Done():
		return false, true
	}
	defer func() { <-argonSlots }()
	return s.verify(encoded, pw), false
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

// undo 退还 key 最近一次放行（这次请求没有真正尝试，如服务器忙回 503）。没有记录时什么也不做，退到空时删掉键。
func (l *limiter) undo(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.hits[key]
	if !ok {
		return
	}
	if len(h.times) <= 1 {
		delete(l.hits, key)
		return
	}
	h.times = h.times[:len(h.times)-1]
	l.hits[key] = h
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

// clientIP 给出限速用的客户端键。默认取 TCP 对端地址；只有对端落在 trusted 前缀内时才看 X-Forwarded-For，
// 从右往左跳过可信地址，取第一个不可信的（再往左的条目客户端可以伪造）；遇到解析不了的条目就停在上一跳。
// 键做归一：IPv4-mapped 换成 IPv4，IPv6 取所在 /64（一台主机通常握有整段 /64）。对端地址解析不了时原样返回。
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	addr := normAddr(ap.Addr())
	if inPrefixes(addr, trusted) {
		hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			a, ok := parseHop(hops[i])
			if !ok {
				break
			}
			addr = a
			if !inPrefixes(a, trusted) {
				break
			}
		}
	}
	if addr.Is4() {
		return addr.String()
	}
	p, err := addr.Prefix(64)
	if err != nil {
		return addr.String()
	}
	return p.String()
}

// normAddr 去掉 IPv6 zone，IPv4-mapped 换成 IPv4。
func normAddr(a netip.Addr) netip.Addr { return a.WithZone("").Unmap() }

// parseHop 解析 X-Forwarded-For 的一项，接受带或不带端口的写法。
func parseHop(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return normAddr(a), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return normAddr(ap.Addr()), true
	}
	return netip.Addr{}, false
}

func inPrefixes(a netip.Addr, ps []netip.Prefix) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// sessionCookie 是登录会话 cookie：HttpOnly、SameSite=Lax、Path=/。配置要求（--secure-cookies）、配了对外源
// （--public-url，hub 在终止 TLS 的反向代理后面时自己收到的是明文 HTTP）或请求经 TLS 时加 Secure。
// 非空的 PublicURL 已经 validate（CheckPublicURL）确认是 https 源（scheme 大小写不论），这里只看是否配了，不比前缀。
func (s *Server) sessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	secure := s.cfg.SecureCookies || s.cfg.PublicURL != "" || r.TLS != nil
	return &http.Cookie{Name: cookieName, Value: value, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: secure, MaxAge: maxAge}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	key := "login:" + clientIP(r, s.cfg.TrustedProxies)
	if !s.limiter.allow(key, loginMax, loginWindow) {
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
	ok, busy := s.verifyLogin(r.Context(), enc, body.Password)
	if busy {
		s.limiter.undo(key) // 没有真正校验密码，不占这个来源的尝试名额
		writeErr(w, http.StatusServiceUnavailable, "服务器忙，请稍后再试")
		return
	}
	if !ok {
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
	w.Header().Set("Cache-Control", "no-store") // 响应里有机器密钥
	if !s.limiter.allow("enroll:"+clientIP(r, s.cfg.TrustedProxies), loginMax, loginWindow) {
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
	if len(body.Name) > MaxNameLen || len(body.HomeDir) > maxHomeDirLen {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("name 至多 %d 字节，home_dir 至多 %d 字节", MaxNameLen, maxHomeDirLen))
		return
	}
	if !ValidName(body.Name) {
		writeErr(w, http.StatusBadRequest, "name 必须是合法 UTF-8，且不能含控制字符或不可见的格式字符")
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

// MaxNameLen 是机器名的字节上限（接入时检查；railse agent enroll 先挡一道）。
const MaxNameLen = 128

// ValidName 报告名字（机器名、webhook 标题）能否安全显示，不管长度（由调用方按各自上限检查）：合法 UTF-8，
// 不含控制字符（C0、DEL、C1），也不含格式字符（unicode.Cf：bidi 覆盖与隔离、LRM/RLM、U+061C、零宽空格、U+2060–U+2064、软连字符、U+180E、BOM 等）——
// 它们不可见，却能让名字在网页、终端里显示成另一个样子。
// 取舍：放行 U+200C、U+200D（ZWNJ、ZWJ），组合 emoji（👨‍👩‍👧）与波斯语、印地语等文字的正确拼写需要它们，
// 单独出现也只影响字形连写，不改变显示顺序。代价是 Cf 里的 emoji 标签字符（U+E0020–U+E007F）被拒，英格兰等地区旗帜用不了。
func ValidName(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if unicode.IsControl(c) || (unicode.Is(unicode.Cf, c) && c != 0x200c && c != 0x200d) {
			return false
		}
	}
	return true
}

// errNoBearer 表示请求没带 Bearer 机器密钥。
var errNoBearer = errors.New("缺少 Bearer 密钥")

// bearerToken 从 Authorization: Bearer 取凭据。方案名大小写不敏感，值去首尾空白后不能为空，否则 ok 为 false。
// 机器密钥（/ws/agent）与 webhook 来源令牌共用。
func bearerToken(r *http.Request) (string, bool) {
	const scheme = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(scheme) || !strings.EqualFold(h[:len(scheme)], scheme) {
		return "", false
	}
	tok := strings.TrimSpace(h[len(scheme):])
	return tok, tok != ""
}

// authMachine 从 Authorization: Bearer 取机器密钥（见 bearerToken）。
// 没带密钥返回 errNoBearer，密钥不对或已吊销返回 store.ErrNotFound，其余是存储错误。
func (s *Server) authMachine(r *http.Request) (store.Machine, error) {
	secret, ok := bearerToken(r)
	if !ok {
		return store.Machine{}, errNoBearer
	}
	return s.store.AuthMachine(secret)
}
