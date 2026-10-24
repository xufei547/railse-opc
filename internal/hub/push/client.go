package push

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ErrGone 表示端点回 404 或 410：订阅已失效，应删除。
var ErrGone = errors.New("订阅已失效")

// ErrLocal 标记 Send 在发出请求之前就失败了：载荷过大、Topic/Urgency/TTL 不合法、订阅或端点不合法、
// 没有 VAPID 密钥、subject 不规范、签名出错。这类错误与端点无关，调用方不应据此给订阅累计失败。
// 用 errors.Is(err, ErrLocal) 判断；具体原因仍可 errors.Is 取到（如 ErrTooLarge）。
var ErrLocal = errors.New("推送没有发出")

func localErr(err error) error { return fmt.Errorf("%w: %w", ErrLocal, err) }

// StatusError 是端点返回的其他非 2xx 状态。
type StatusError struct {
	Status int
	// Reason 是响应体的开头（去掉控制字符，至多 200 个字符），如 Apple 的 {"reason":"BadJwtToken"}，排查用。
	Reason string
}

func (e *StatusError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("推送端点返回 %d", e.Status)
	}
	return fmt.Sprintf("推送端点返回 %d: %s", e.Status, e.Reason)
}

// Client 向推送服务发消息。可以多个协程同时 Send；建好后按指针用，不要拷贝。
//
// 签好的 VAPID 令牌按 (密钥, aud, sub) 缓存复用，见 authorization。换钥就是换一个 VAPID 值，
// 缓存按公钥认，不会拿旧钥签的令牌去发；VAPID、Subject 不要与 Send 同时改。
type Client struct {
	HTTP    *http.Client // nil 时用 10 秒超时、不跟随重定向的客户端
	VAPID   *VAPID
	Subject string // VAPID 的 sub：hub 的 https 源或 mailto:

	now func() time.Time // 包内测试注入的时钟；nil 时用 time.Now

	mu     sync.Mutex  // 护 tokens，也护读时钟（见 authorization）
	tokens *tokenCache // 懒建
}

// tokenCache 存签好的令牌。令牌串离 Client 隔着两层指针（Client.tokens 与 map 的值）：fmt 碰到不支持的动词
// （%s %q %t %e……）会把深层指针按 %v 解引用一层再打，更深的指针只打地址，所以用任何动词打印 Client 都带不出令牌。
type tokenCache struct {
	m map[tokenKey]*vapidToken
}

// tokenKey 定一个可复用的令牌：同一把密钥（按公钥认）、同一 aud（端点 origin）、同一 sub。
type tokenKey struct{ k, aud, sub string }

// vapidToken 是一个签好的令牌。时效只拿它自己的 exp 与此刻的墙钟（都是 Unix 秒）比，不存 time.Time：
// 两个都带单调读数的 time.Time 相比只看单调时钟，而系统睡眠时单调时钟不走（macOS、Linux 挂起），NTP 跳变也只改墙钟；
// exp 却是墙钟，推送服务也按墙钟验。按单调时钟判断，睡醒后会接着发已过期的令牌。
type vapidToken struct {
	header string // 整个 Authorization 头
	exp    int64  // JWT 的 exp，Unix 秒
}

// usable 判断令牌在墙钟时刻 now（Unix 秒）能不能发：离 exp 还剩超过 tokenRefreshMargin，
// 且 exp 离此刻不超过 tokenMaxAhead（墙钟往回调过就可能超）。
func (t *vapidToken) usable(now int64) bool {
	left := t.exp - now
	return left > int64(tokenRefreshMargin/time.Second) && left <= int64(tokenMaxAhead/time.Second)
}

// Options 是一条推送的投递参数（RFC 8030 第 5.2–5.4 节）。
type Options struct {
	TTL     time.Duration // 零为 24 小时；负数或不足 1 秒是本地错误（按整秒发会成 TTL: 0，Apple 回 BadTtl）；整秒以下的零头舍去
	Urgency string        // very-low、low、normal、high；空不发
	Topic   string        // 1 到 32 个 base64url 字符（ValidTopic）；同 topic 的未投递消息被替换；空不发
}

const (
	defaultTTL     = 24 * time.Hour
	defaultTimeout = 10 * time.Second
	respMax        = 4 << 10 // 响应体至多读这么多再关
	reasonMax      = 200     // StatusError.Reason 的字符上限
	maxTopicLen    = 32

	// tokenRefreshMargin：令牌离 exp 不到这么久就重签，保证发出去的令牌至少还有这么久有效。
	// 墙钟正常走时，同一 aud 每 jwtLifetime − tokenRefreshMargin（11 小时）签一次。
	tokenRefreshMargin = time.Hour
	// tokenMaxAhead：复用的令牌 exp 离此刻至多这么久。RFC 8292 第 2 节要求不超过 24 小时（Apple 超过一天判 BadJwtToken），
	// 留 1 小时余量防对端时钟差。
	tokenMaxAhead = 24*time.Hour - tokenRefreshMargin
)

// 编译期检查：重签间隔不短于 1 小时（Apple 的 Web Push 文档：“Don't refresh your JWT more frequently than once per hour.”）；
// 刚签的令牌本身可用。
const (
	_ = uint(jwtLifetime - tokenRefreshMargin - time.Hour)
	_ = uint(tokenMaxAhead - jwtLifetime)
)

var defaultHTTP = &http.Client{
	Timeout:       defaultTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// ValidTopic 判断 s 能否作 Topic 头：RFC 8030 第 5.4 节要求 1 到 32 个 URL 安全 base64 字母表里的字符（不带补位）。
// 调用方用它决定是否带 Topic（例如请求 id 只有合法时才拿来当 Topic）。
func ValidTopic(s string) bool {
	if len(s) == 0 || len(s) > maxTopicLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		b64url := 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_'
		if !b64url {
			return false
		}
	}
	return true
}

func validUrgency(u string) bool {
	switch u {
	case "very-low", "low", "normal", "high":
		return true
	}
	return false
}

// Send 加密并发送。返回状态码与错误：
//   - 2xx：成功，err 为 nil；
//   - 404、410：ErrGone，订阅已失效；
//   - 其余非 2xx：*StatusError；
//   - 请求发出之前失败：状态码 0，errors.Is(err, ErrLocal)；
//   - 连不上、超时等传输错误：状态码 0，原样返回；调用方 ctx 取消时 errors.Is(err, context.Canceled)。
func (c *Client) Send(ctx context.Context, sub Subscription, payload []byte, o Options) (int, error) {
	req, err := c.newRequest(ctx, sub, payload, o)
	if err != nil {
		return 0, localErr(err)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	head, _ := io.ReadAll(io.LimitReader(resp.Body, respMax))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp.StatusCode, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return resp.StatusCode, ErrGone
	default:
		return resp.StatusCode, &StatusError{Status: resp.StatusCode, Reason: reasonOf(head)}
	}
}

// authorization 返回用密钥 v、以 subject 为 sub 发往 aud 的 Authorization 头。缓存里有同一把钥、同一 aud 与 sub 签的、
// 此刻仍可用（usable）的令牌就复用，否则现签。墙钟正常走时同一 aud 约 11 小时签一次（Apple 要求至多一小时一次），
// 发出去的令牌离 exp 在 (1h, 23h] 内；睡眠或 NTP 跳变后按墙钟重新判断。
// 时钟在锁里读、持锁签名：并发的 Send 对同一 aud 只签一次，也不会有协程拿着抢锁之前读到的时刻去判断。
// 签名出错（如 subject 不规范）不缓存，每次都报。
func (c *Client) authorization(v *VAPID, subject, aud string) (string, error) {
	key := tokenKey{k: v.PublicKeyB64(), aud: aud, sub: subject}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	wall := now.Unix() // 墙钟；不拿 time.Time 比较，见 vapidToken
	if c.tokens == nil {
		c.tokens = &tokenCache{m: make(map[tokenKey]*vapidToken)}
	}
	if tok := c.tokens.m[key]; tok != nil && tok.usable(wall) {
		return tok.header, nil
	}
	exp := jwtExp(now)
	hdr, err := v.header(aud, subject, exp)
	if err != nil {
		return "", err
	}
	for k, tok := range c.tokens.m { // 顺手清掉此刻用不上的（过了窗口的、换钥前的、墙钟跳出界的），缓存不会越积越多
		if !tok.usable(wall) {
			delete(c.tokens.m, k)
		}
	}
	c.tokens.m[key] = &vapidToken{header: hdr, exp: exp}
	return hdr, nil
}

// httpClient 是 Send 用的 HTTP 客户端：调用方给的，或包内默认的。
func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

// newRequest 校验参数、加密、签名并组好请求。这里的错误都是本地错误。
func (c *Client) newRequest(ctx context.Context, sub Subscription, payload []byte, o Options) (*http.Request, error) {
	v, subject := c.VAPID, c.Subject // 各读一次：判空、缓存键、签名用的是同一组值
	if v == nil {
		return nil, errors.New("没有 VAPID 密钥")
	}
	if o.Topic != "" && !ValidTopic(o.Topic) {
		return nil, errors.New("选项 Topic 须是 1 到 32 个 base64url 字符")
	}
	if o.Urgency != "" && !validUrgency(o.Urgency) {
		return nil, fmt.Errorf("选项 Urgency 只能是 very-low、low、normal、high，得到 %q", o.Urgency)
	}
	ttl := o.TTL
	switch {
	case ttl < 0:
		return nil, errors.New("TTL 不能为负")
	case ttl == 0:
		ttl = defaultTTL
	case ttl < time.Second:
		return nil, fmt.Errorf("TTL 至少 1 秒（按整秒发，不足 1 秒会成 TTL: 0，Apple 回 BadTtl），得到 %v", ttl)
	}
	aud, err := endpointOrigin(sub.Endpoint)
	if err != nil {
		return nil, err
	}
	body, err := Encrypt(payload, sub)
	if err != nil {
		return nil, err
	}
	auth, err := c.authorization(v, subject, aud)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.FormatInt(int64(ttl/time.Second), 10))
	req.Header.Set("Authorization", auth)
	if o.Urgency != "" {
		req.Header.Set("Urgency", o.Urgency)
	}
	if o.Topic != "" {
		req.Header.Set("Topic", o.Topic)
	}
	return req, nil
}

// reasonOf 把响应体开头整理成一行可打印文本：去掉控制字符与非法 UTF-8，空白并成一个空格，至多 reasonMax 个字符。
func reasonOf(b []byte) string {
	s := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' '
		case !unicode.IsPrint(r):
			return -1
		}
		return r
	}, strings.ToValidUTF8(string(b), ""))
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > reasonMax {
		s = string(r[:reasonMax]) + "…"
	}
	return s
}
