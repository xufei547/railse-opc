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

// Client 向推送服务发消息。
type Client struct {
	HTTP    *http.Client // nil 时用 10 秒超时、不跟随重定向的客户端
	VAPID   *VAPID
	Subject string // VAPID 的 sub：hub 的 https 源或 mailto:
}

// Options 是一条推送的投递参数（RFC 8030 第 5.2–5.4 节）。
type Options struct {
	TTL     time.Duration // 零为 24 小时；负数是本地错误；按整秒发
	Urgency string        // very-low、low、normal、high；空不发
	Topic   string        // 1 到 32 个 base64url 字符（ValidTopic）；同 topic 的未投递消息被替换；空不发
}

const (
	defaultTTL     = 24 * time.Hour
	defaultTimeout = 10 * time.Second
	respMax        = 4 << 10 // 响应体至多读这么多再关
	reasonMax      = 200     // StatusError.Reason 的字符上限
	maxTopicLen    = 32
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
		if !('A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
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
	hc := c.HTTP
	if hc == nil {
		hc = defaultHTTP
	}
	resp, err := hc.Do(req)
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

// newRequest 校验参数、加密、签名并组好请求。这里的错误都是本地错误。
func (c *Client) newRequest(ctx context.Context, sub Subscription, payload []byte, o Options) (*http.Request, error) {
	if c.VAPID == nil {
		return nil, errors.New("没有 VAPID 密钥")
	}
	if o.Topic != "" && !ValidTopic(o.Topic) {
		return nil, errors.New("Topic 须是 1 到 32 个 base64url 字符")
	}
	if o.Urgency != "" && !validUrgency(o.Urgency) {
		return nil, fmt.Errorf("Urgency 只能是 very-low、low、normal、high，得到 %q", o.Urgency)
	}
	if o.TTL < 0 {
		return nil, errors.New("TTL 不能为负")
	}
	ttl := o.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	aud, err := endpointOrigin(sub.Endpoint)
	if err != nil {
		return nil, err
	}
	body, err := Encrypt(payload, sub)
	if err != nil {
		return nil, err
	}
	auth, err := c.VAPID.Authorization(aud, c.Subject, time.Now())
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
