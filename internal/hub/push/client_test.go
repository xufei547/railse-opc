package push

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
)

func TestClientSend(t *testing.T) {
	var status atomic.Int32
	status.Store(201)
	var got http.Header
	var bodyLen int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		bodyLen = len(b)
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	v, _ := GenerateVAPID()
	c := &Client{HTTP: srv.Client(), VAPID: v, Subject: "https://hub.example.com"}
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := Subscription{Endpoint: srv.URL + "/p/abc", P256dh: uaPriv.PublicKey().Bytes(), Auth: make([]byte, 16)}
	payload := []byte(`{"id":"r1"}`)
	code, err := c.Send(context.Background(), sub, payload, Options{TTL: time.Hour, Urgency: "high", Topic: "r1"})
	if err != nil || code != 201 {
		t.Fatalf("send: %d %v", code, err)
	}
	if got.Get("Content-Encoding") != "aes128gcm" || got.Get("TTL") != "3600" || got.Get("Urgency") != "high" || got.Get("Topic") != "r1" ||
		got.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(got.Get("Authorization"), "vapid t=") {
		t.Fatalf("头: %v", got)
	}
	if bodyLen != 86+len(payload)+1+16 {
		t.Fatalf("body %d 字节", bodyLen)
	}
	// 默认 TTL 24 小时，不发 Urgency、Topic
	c.Send(context.Background(), sub, payload, Options{})
	if got.Get("TTL") != "86400" || got.Get("Urgency") != "" || got.Get("Topic") != "" {
		t.Fatalf("默认头: %v", got)
	}
	status.Store(410)
	if code, err := c.Send(context.Background(), sub, payload, Options{}); !errors.Is(err, ErrGone) || code != 410 {
		t.Fatalf("410: %d %v", code, err)
	}
	status.Store(404)
	if _, err := c.Send(context.Background(), sub, payload, Options{}); !errors.Is(err, ErrGone) {
		t.Fatalf("404: %v", err)
	}
	status.Store(500)
	var se *StatusError
	if code, err := c.Send(context.Background(), sub, payload, Options{}); !errors.As(err, &se) || se.Status != 500 || code != 500 {
		t.Fatalf("500: %d %v", code, err)
	}
	if _, err := c.Send(context.Background(), sub, payload, Options{Topic: "not base64url!"}); err == nil {
		t.Fatal("坏 Topic 应报错")
	}
	if _, err := c.Send(context.Background(), sub, make([]byte, MaxPlaintext+1), Options{}); !errors.Is(err, ErrTooLarge) {
		t.Fatal("超长应 ErrTooLarge")
	}
}

// 以下是请求形状与错误分类的补充测试。

// recorder 冒充推送服务，记下每个请求。
type recorder struct {
	srv    *httptest.Server
	status atomic.Int32
	reply  atomic.Value // string：响应体
	mu     sync.Mutex
	reqs   []recorded
}

type recorded struct {
	method, path string
	header       http.Header
	body         []byte
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	rec.status.Store(201)
	rec.reply.Store("")
	rec.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.reqs = append(rec.reqs, recorded{r.Method, r.URL.Path, r.Header.Clone(), b})
		rec.mu.Unlock()
		w.WriteHeader(int(rec.status.Load()))
		io.WriteString(w, rec.reply.Load().(string))
	}))
	t.Cleanup(rec.srv.Close)
	return rec
}

func (rec *recorder) all() []recorded {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]recorded(nil), rec.reqs...)
}

func testClient(t *testing.T, rec *recorder) (*Client, Subscription, *ecdh.PrivateKey) {
	t.Helper()
	v, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	ua := mustKey(t)
	auth := make([]byte, 16)
	rand.Read(auth)
	return &Client{HTTP: rec.srv.Client(), VAPID: v, Subject: "mailto:push@example.com"},
		Subscription{Endpoint: rec.srv.URL + "/p/abc?x=1", P256dh: ua.PublicKey().Bytes(), Auth: auth}, ua
}

// 请求：POST 到端点原样的 URL；头齐全；body 用订阅的密钥解得开；Authorization 的 aud 是端点的 origin（不带路径），
// sub 是 Client.Subject，k 是 VAPID 公钥。
func TestClientRequestShape(t *testing.T) {
	rec := newRecorder(t)
	c, sub, ua := testClient(t, rec)
	payload := []byte(`{"id":"0123456789abcdef0123456789abcdef","title":"Mac · blog"}`)
	before := time.Now()
	code, err := c.Send(context.Background(), sub, payload, Options{TTL: 90 * time.Second, Urgency: "normal", Topic: "0123456789abcdef0123456789abcdef"})
	if err != nil || code != 201 {
		t.Fatalf("send: %d %v", code, err)
	}
	reqs := rec.all()
	if len(reqs) != 1 {
		t.Fatalf("请求数 %d", len(reqs))
	}
	r := reqs[0]
	if r.method != http.MethodPost || r.path != "/p/abc" {
		t.Fatalf("方法与路径: %s %s", r.method, r.path)
	}
	h := r.header
	if h.Get("Content-Encoding") != "aes128gcm" || h.Get("Content-Type") != "application/octet-stream" || h.Get("TTL") != "90" ||
		h.Get("Urgency") != "normal" || h.Get("Topic") != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("头: %v", h)
	}
	if len(r.body) != headerLen+len(payload)+1+16 {
		t.Fatalf("body %d 字节，应为单记录 %d", len(r.body), headerLen+len(payload)+1+16)
	}
	if got := decrypt(t, r.body, ua, sub.Auth); !bytes.Equal(got, payload) {
		t.Fatalf("端点收到的 body 解不出原文: %q", got)
	}
	auth := h.Get("Authorization")
	jwt, k, ok := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
	if !ok || k != c.VAPID.PublicKeyB64() {
		t.Fatalf("Authorization: %s", auth)
	}
	parts := strings.Split(jwt, ".")
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(cb, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Aud != rec.srv.URL || strings.Count(claims.Aud, "/") != 2 || claims.Sub != "mailto:push@example.com" {
		t.Fatalf("aud 应是端点 origin %s: %s", rec.srv.URL, cb)
	}
	if claims.Exp <= before.Unix() || claims.Exp > time.Now().Add(24*time.Hour).Unix() {
		t.Fatalf("exp 超出 24 小时: %d", claims.Exp)
	}
}

// 本地错误：请求还没发出就失败，code 为 0，errors.Is(err, ErrLocal)，端点一次也没被访问；原因仍能 errors.Is 取到。
func TestClientLocalErrors(t *testing.T) {
	rec := newRecorder(t)
	c, sub, _ := testClient(t, rec)
	httpSub := sub
	httpSub.Endpoint = strings.Replace(sub.Endpoint, "https://", "http://", 1)
	badKey := sub
	badKey.Auth = make([]byte, 15)
	noHost := sub
	noHost.Endpoint = "https:///p/abc"
	cases := []struct {
		name string
		c    *Client
		sub  Subscription
		pl   []byte
		o    Options
		is   error
	}{
		{"载荷过大", c, sub, make([]byte, MaxPlaintext+1), Options{}, ErrTooLarge},
		{"Topic 有非法字符", c, sub, []byte("x"), Options{Topic: "a+b"}, nil},
		{"Topic 有补位", c, sub, []byte("x"), Options{Topic: "YQ=="}, nil},
		{"Topic 33 字符", c, sub, []byte("x"), Options{Topic: strings.Repeat("a", 33)}, nil},
		{"Urgency 不认识", c, sub, []byte("x"), Options{Urgency: "urgent"}, nil},
		{"TTL 为负", c, sub, []byte("x"), Options{TTL: -time.Second}, nil},
		{"端点不是 https", c, httpSub, []byte("x"), Options{}, nil},
		{"端点没有主机", c, noHost, []byte("x"), Options{}, nil},
		{"订阅密钥坏", c, badKey, []byte("x"), Options{}, nil},
		{"subject 不规范", &Client{HTTP: c.HTTP, VAPID: c.VAPID, Subject: "hub.example.com"}, sub, []byte("x"), Options{}, nil},
		{"subject 是 http", &Client{HTTP: c.HTTP, VAPID: c.VAPID, Subject: "http://hub.example.com"}, sub, []byte("x"), Options{}, nil},
		{"没有 VAPID", &Client{HTTP: c.HTTP, Subject: c.Subject}, sub, []byte("x"), Options{}, nil},
	}
	for _, tc := range cases {
		code, err := tc.c.Send(context.Background(), tc.sub, tc.pl, tc.o)
		if code != 0 || !errors.Is(err, ErrLocal) {
			t.Errorf("%s: 应为本地错误，得到 %d %v", tc.name, code, err)
		}
		if errors.Is(err, ErrGone) {
			t.Errorf("%s: 本地错误不应是 ErrGone", tc.name)
		}
		var se *StatusError
		if errors.As(err, &se) {
			t.Errorf("%s: 本地错误不应是 StatusError", tc.name)
		}
		if tc.is != nil && !errors.Is(err, tc.is) {
			t.Errorf("%s: 应能 errors.Is 到 %v: %v", tc.name, tc.is, err)
		}
	}
	if n := len(rec.all()); n != 0 {
		t.Fatalf("本地错误时端点不应被访问，收到 %d 个请求", n)
	}
	// 合法的边界值不算错：32 字符 Topic、四种 Urgency
	for _, u := range []string{"very-low", "low", "normal", "high"} {
		if _, err := c.Send(context.Background(), sub, []byte("x"), Options{Urgency: u, Topic: strings.Repeat("A", 32)}); err != nil {
			t.Errorf("Urgency %s: %v", u, err)
		}
	}
}

// 端点的回应：2xx 成功（含 200、202），404/410 是 ErrGone，其他是 *StatusError（不是本地错误），并带上端点给的原因便于排查。
func TestClientStatusClassification(t *testing.T) {
	rec := newRecorder(t)
	c, sub, _ := testClient(t, rec)
	for _, st := range []int{200, 201, 202} {
		rec.status.Store(int32(st))
		if code, err := c.Send(context.Background(), sub, []byte("x"), Options{}); err != nil || code != st {
			t.Errorf("%d: %v", st, err)
		}
	}
	for _, st := range []int{404, 410} {
		rec.status.Store(int32(st))
		code, err := c.Send(context.Background(), sub, []byte("x"), Options{})
		if !errors.Is(err, ErrGone) || errors.Is(err, ErrLocal) || code != st {
			t.Errorf("%d: %d %v", st, code, err)
		}
	}
	rec.status.Store(403)
	rec.reply.Store("{\"reason\":\"BadJwtToken\"}\n\x00\x1b[31m")
	code, err := c.Send(context.Background(), sub, []byte("x"), Options{})
	var se *StatusError
	if !errors.As(err, &se) || se.Status != 403 || code != 403 || errors.Is(err, ErrLocal) || errors.Is(err, ErrGone) {
		t.Fatalf("403: %d %v", code, err)
	}
	if !strings.Contains(se.Reason, "BadJwtToken") || strings.ContainsAny(se.Reason, "\x00\x1b\n") || !strings.HasPrefix(err.Error(), "推送端点返回 403") {
		t.Fatalf("原因: %q / %q", se.Reason, err.Error())
	}
	rec.reply.Store(strings.Repeat("长", 1000))
	if _, err := c.Send(context.Background(), sub, []byte("x"), Options{}); !errors.As(err, &se) || len([]rune(se.Reason)) > 201 {
		t.Fatalf("原因应截断: %d", len([]rune(se.Reason)))
	}
	rec.reply.Store("")
	rec.status.Store(500)
	if _, err := c.Send(context.Background(), sub, []byte("x"), Options{}); !errors.As(err, &se) || err.Error() != "推送端点返回 500" {
		t.Fatalf("500: %v", err)
	}
}

// 连不上端点是端点侧失败：不是本地错误、不是 ErrGone、也没有状态码。
func TestClientTransportErrorIsNotLocal(t *testing.T) {
	rec := newRecorder(t)
	c, sub, _ := testClient(t, rec)
	rec.srv.Close()
	code, err := c.Send(context.Background(), sub, []byte("x"), Options{})
	var se *StatusError
	if err == nil || code != 0 || errors.Is(err, ErrLocal) || errors.Is(err, ErrGone) || errors.As(err, &se) {
		t.Fatalf("连不上: %d %v", code, err)
	}
	// 调用方取消 ctx：错误里带 context.Canceled，调用方据此判断不是订阅的问题
	live := newRecorder(t)
	c2, sub2, _ := testClient(t, live)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if code, err := c2.Send(ctx, sub2, []byte("x"), Options{}); !errors.Is(err, context.Canceled) || errors.Is(err, ErrLocal) || code != 0 {
		t.Fatalf("取消: %d %v", code, err)
	}
}

func TestValidTopic(t *testing.T) {
	for _, s := range []string{"r1", "0123456789abcdef0123456789abcdef", strings.Repeat("_", 32), "A-z_9"} {
		if !ValidTopic(s) {
			t.Errorf("%q 应合法", s)
		}
	}
	for _, s := range []string{"", strings.Repeat("a", 33), "a+b", "a/b", "a=b", "a b", "中文", "0123456789abcdef0123456789abcdef0"} {
		if ValidTopic(s) {
			t.Errorf("%q 应不合法", s)
		}
	}
}

// aud 用端点 origin 的规范串行化：小写主机，https 默认端口 443 省掉，其他端口保留。
func TestEndpointOrigin(t *testing.T) {
	for in, want := range map[string]string{
		"https://web.push.apple.com/QAbc":            "https://web.push.apple.com",
		"https://Web.Push.Apple.com/QAbc":            "https://web.push.apple.com",
		"https://fcm.googleapis.com:443/fcm/send/x":  "https://fcm.googleapis.com",
		"https://push.example.net:8443/p?x=1#y":      "https://push.example.net:8443",
		"https://[::1]:8443/p":                       "https://[::1]:8443",
		"https://[::1]:443/p":                        "https://[::1]",
		"https://updates.push.services.mozilla.com/": "https://updates.push.services.mozilla.com",
	} {
		got, err := endpointOrigin(in)
		if err != nil || got != want {
			t.Errorf("%s: 得到 %q %v，想要 %q", in, got, err, want)
		}
	}
	for _, in := range []string{"http://push.example.net/p", "https:///p", "push.example.net/p", "https://u@push.example.net/p", "://"} {
		if _, err := endpointOrigin(in); err == nil {
			t.Errorf("%q 应拒绝", in)
		}
	}
}

// TTL 按整秒发。0 < TTL < 1s 会发成 `TTL: 0`，Apple 回 BadTtl（400），所以和负数一样是本地错误，端点不被访问；
// 1 秒以上的零头舍去；零仍是 24 小时。
func TestClientTTL(t *testing.T) {
	rec := newRecorder(t)
	c, sub, _ := testClient(t, rec)
	for _, ttl := range []time.Duration{1, 500 * time.Millisecond, time.Second - 1} {
		code, err := c.Send(context.Background(), sub, []byte("x"), Options{TTL: ttl})
		if code != 0 || !errors.Is(err, ErrLocal) {
			t.Errorf("TTL %v: 应为本地错误，得到 %d %v", ttl, code, err)
		}
	}
	if n := len(rec.all()); n != 0 {
		t.Fatalf("TTL 不足 1 秒时端点不应被访问，收到 %d 个请求", n)
	}
	for _, tc := range []struct {
		ttl  time.Duration
		want string
	}{{time.Second, "1"}, {1999 * time.Millisecond, "1"}, {time.Minute, "60"}, {0, "86400"}} {
		if _, err := c.Send(context.Background(), sub, []byte("x"), Options{TTL: tc.ttl}); err != nil {
			t.Fatalf("TTL %v: %v", tc.ttl, err)
		}
		reqs := rec.all()
		if got := reqs[len(reqs)-1].header.Get("TTL"); got != tc.want {
			t.Errorf("TTL %v: 发出 %q，想要 %q", tc.ttl, got, tc.want)
		}
	}
}

// Client.HTTP 为 nil 时用包内默认客户端：10 秒超时，不跟随任何重定向（原样回 3xx，重定向目标一次也不访问）。
func TestDefaultHTTPClient(t *testing.T) {
	if (&Client{}).httpClient() != defaultHTTP {
		t.Fatal("HTTP 为 nil 时应用包内默认客户端")
	}
	custom := &http.Client{}
	if (&Client{HTTP: custom}).httpClient() != custom {
		t.Fatal("给了 HTTP 就用调用方的客户端")
	}
	if defaultHTTP.Timeout != 10*time.Second {
		t.Fatalf("默认客户端应有 10 秒超时，得到 %v", defaultHTTP.Timeout)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			hits.Add(1)
			w.WriteHeader(http.StatusCreated)
			return
		}
		code, _ := strconv.Atoi(r.URL.Query().Get("code"))
		http.Redirect(w, r, "/target", code)
	}))
	defer srv.Close()
	for _, code := range []int{301, 302, 303, 307, 308} {
		resp, err := defaultHTTP.Post(srv.URL+"/p?code="+strconv.Itoa(code), "application/octet-stream", strings.NewReader("x"))
		if err != nil {
			t.Fatalf("%d: %v", code, err)
		}
		resp.Body.Close()
		if resp.StatusCode != code {
			t.Errorf("%d: 默认客户端不应跟随重定向，得到 %d", code, resp.StatusCode)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("重定向目标被访问了 %d 次", n)
	}
}

// 以下是 VAPID 令牌缓存的测试：Apple 要求 JWT 一小时内至多刷新一次。

// fakeClock 是可拨的时钟，并发安全。它给出的时间和 time.Now() 一样带单调时钟读数，所以缓存若按单调时钟比较，
// 测试里也是这样比较，测到的就是生产上的行为：
//   - Advance：墙钟与单调时钟一起走（正常流逝）；
//   - JumpWall：只动墙钟——系统睡眠（macOS、Linux 挂起时单调时钟不走）或 NTP 跳变，两个方向都行。
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(t *testing.T) *fakeClock {
	t.Helper()
	now := time.Now()
	now = now.Add(-time.Duration(now.Nanosecond())) // 整秒，便于核对 exp；Add 保留单调读数
	if !hasMono(now) {
		t.Fatal("time.Now() 应带单调时钟读数")
	}
	return &fakeClock{t: now}
}

func hasMono(t time.Time) bool { return strings.Contains(t.String(), " m=") }

// monoShifted 返回墙钟与 t 相同、单调读数加 d 的时间。time 包没有公开办法让两者脱节，只能改未导出字段 ext：
// t 带单调读数时 ext 就是单调读数（见 time.Time 的注释）。布局变了就直接报错，不悄悄退化成只有墙钟的测试。
func monoShifted(t time.Time, d time.Duration) time.Time {
	f := reflect.ValueOf(&t).Elem().FieldByName("ext")
	if !hasMono(t) || !f.IsValid() || f.Kind() != reflect.Int64 {
		panic("time.Time 的内部布局变了，fakeClock 模拟不了单调时钟")
	}
	f = reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
	f.SetInt(f.Int() + int64(d))
	return t
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) JumpWall(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = monoShifted(c.t.Add(d), -d)
}

// 假时钟本身：JumpWall 之后墙钟变了、单调时钟没变（Sub 用单调读数），Advance 两者一起变。
func TestFakeClockSimulatesMonotonic(t *testing.T) {
	clk := newFakeClock(t)
	t0 := clk.Now()
	clk.JumpWall(13 * time.Hour)
	t1 := clk.Now()
	if !hasMono(t1) || t1.Sub(t0) != 0 || t1.Unix()-t0.Unix() != 13*3600 || !t1.Equal(t0) {
		t.Fatalf("睡眠 13 小时：单调差 %v、墙钟差 %d 秒，应为 0 与 46800", t1.Sub(t0), t1.Unix()-t0.Unix())
	}
	clk.JumpWall(-20 * time.Hour)
	clk.Advance(time.Hour)
	t2 := clk.Now()
	if t2.Sub(t0) != time.Hour || t2.Unix()-t0.Unix() != -6*3600 {
		t.Fatalf("单调差 %v、墙钟差 %d 秒，应为 1h 与 -21600", t2.Sub(t0), t2.Unix()-t0.Unix())
	}
}

// authTransport 冒充推送服务但不走网络：记下每个请求的 Authorization，一律回 201。端点可以是任意 https 主机。
type authTransport struct {
	mu    sync.Mutex
	auths []string
}

func (a *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	io.Copy(io.Discard, r.Body)
	r.Body.Close()
	a.mu.Lock()
	a.auths = append(a.auths, r.Header.Get("Authorization"))
	a.mu.Unlock()
	return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
}

func (a *authTransport) all() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.auths...)
}

// vapidOf 拆出 Authorization 头里 JWT 的 claims 与 k。
func vapidOf(t *testing.T, auth string) (claims vapidClaims, k string) {
	t.Helper()
	jwt, k, ok := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
	parts := strings.Split(jwt, ".")
	if !ok || len(parts) != 3 {
		t.Fatalf("Authorization: %q", auth)
	}
	cb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err == nil {
		err = json.Unmarshal(cb, &claims)
	}
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	return claims, k
}

func newTokenClient(t *testing.T) (*Client, *fakeClock, *authTransport) {
	t.Helper()
	v, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	clk := newFakeClock(t)
	tr := &authTransport{}
	return &Client{HTTP: &http.Client{Transport: tr}, VAPID: v, Subject: "mailto:push@example.com", now: clk.Now}, clk, tr
}

func subFor(t *testing.T, endpoint string) Subscription {
	return Subscription{Endpoint: endpoint, P256dh: mustKey(t).PublicKey().Bytes(), Auth: make([]byte, 16)}
}

// sendAt 往 endpoint 发一条，返回发出去的 Authorization。顺带核对时效：aud 是端点 origin、k 是当前密钥，
// exp 离此刻（墙钟）至少 1 小时（推送服务收到时仍有效）、至多 24 小时（RFC 8292 第 2 节；Apple 判 BadJwtToken）。
func sendAt(t *testing.T, c *Client, clk *fakeClock, tr *authTransport, endpoint string) string {
	t.Helper()
	n := len(tr.all())
	if code, err := c.Send(context.Background(), subFor(t, endpoint), []byte("x"), Options{}); err != nil || code != 201 {
		t.Fatalf("send %s: %d %v", endpoint, code, err)
	}
	auths := tr.all()
	if len(auths) != n+1 {
		t.Fatalf("请求数 %d，应为 %d", len(auths), n+1)
	}
	auth := auths[n]
	claims, k := vapidOf(t, auth)
	origin, _ := endpointOrigin(endpoint)
	if claims.Aud != origin || claims.Sub != c.Subject || k != c.VAPID.PublicKeyB64() {
		t.Fatalf("令牌是给 aud %q sub %q k %s 的，应为 %q %q %s", claims.Aud, claims.Sub, k, origin, c.Subject, c.VAPID.PublicKeyB64())
	}
	if left := claims.Exp - clk.Now().Unix(); left < 3600 || left > 24*3600 {
		t.Fatalf("发出去的令牌还剩 %d 秒有效（%v），应在 [1h, 24h] 内", left, time.Duration(left)*time.Second)
	}
	return auth
}

func expOf(t *testing.T, auth string) int64 {
	t.Helper()
	claims, _ := vapidOf(t, auth)
	return claims.Exp
}

// 同一来源在窗口内复用同一个令牌（规范化后同 origin 的不同端点也算同一来源）；不同来源各签各的；
// 签发后满 11 小时（exp − 1 小时）重签。
func TestClientReusesVAPIDTokenPerOrigin(t *testing.T) {
	c, clk, tr := newTokenClient(t)
	t0 := clk.Now()
	const apple, apple2, fcm = "https://web.push.apple.com/QAbc", "https://Web.Push.Apple.com:443/QOther", "https://fcm.googleapis.com/fcm/send/x"

	a1 := sendAt(t, c, clk, tr, apple)
	if expOf(t, a1) != t0.Add(12*time.Hour).Unix() {
		t.Fatalf("exp %d，应为签发时刻 + 12 小时", expOf(t, a1))
	}
	clk.Advance(30 * time.Minute)
	if got := sendAt(t, c, clk, tr, apple2); got != a1 {
		t.Fatal("同一来源 30 分钟后应复用令牌，不该重签")
	}
	f1 := sendAt(t, c, clk, tr, fcm)
	if f1 == a1 {
		t.Fatal("不同来源应各用各的令牌")
	}
	clk.Advance(10*time.Hour + 29*time.Minute + 59*time.Second) // t0 + 11h − 1s
	if got := sendAt(t, c, clk, tr, apple); got != a1 {
		t.Fatal("签发后不到 11 小时应复用令牌")
	}
	clk.Advance(time.Second) // t0 + 11h
	a2 := sendAt(t, c, clk, tr, apple)
	if a2 == a1 {
		t.Fatal("签发满 11 小时（exp − 1 小时）应重签")
	}
	if expOf(t, a2) != t0.Add(23*time.Hour).Unix() {
		t.Fatalf("重签的 exp %d，应为此刻 + 12 小时", expOf(t, a2))
	}
	if got := sendAt(t, c, clk, tr, fcm); got != f1 {
		t.Fatal("fcm 的令牌签于 t0+30min，此刻仍在窗口内，应复用")
	}
}

// 墙钟单独跳变（系统睡眠时单调时钟不走；NTP 跳变只改墙钟），按令牌自己的 exp 与此刻墙钟判断：
// 剩余有效期不足 1 小时就重签，exp 离此刻超过 23 小时（离 RFC 8292 的 24 小时留 1 小时余量）也重签，其余照常复用。
func TestClientVAPIDTokenWallClockJumps(t *testing.T) {
	c, clk, tr := newTokenClient(t)
	t0 := clk.Now()
	const ep = "https://web.push.apple.com/QAbc"
	a := sendAt(t, c, clk, tr, ep) // exp = t0 + 12h

	// 合上盖子睡了一夜：墙钟走了 13 小时，单调时钟没走。旧令牌已过期，必须重签。
	clk.JumpWall(13 * time.Hour)
	b := sendAt(t, c, clk, tr, ep) // sendAt 会抓“已过期”
	if b == a || expOf(t, b) != t0.Add(25*time.Hour).Unix() {
		t.Fatalf("睡眠 13 小时后应重签，exp 为此刻 + 12 小时: %d", expOf(t, b))
	}
	// 睡到离 exp 只剩 1 小时 + 1 秒：仍可复用；再睡 1 秒（只剩 1 小时）：重签。
	clk.JumpWall(10*time.Hour + 59*time.Minute + 59*time.Second)
	if got := sendAt(t, c, clk, tr, ep); got != b {
		t.Fatal("离 exp 还剩 1 小时 1 秒，应复用")
	}
	clk.JumpWall(time.Second)
	d := sendAt(t, c, clk, tr, ep) // now = t0 + 37h，exp = t0 + 49h
	if d == b {
		t.Fatal("离 exp 只剩 1 小时，应重签")
	}
	// NTP 往回调 13 小时：exp 离此刻 25 小时，超过 24 小时，Apple 会判 BadJwtToken，必须重签。
	clk.JumpWall(-13 * time.Hour)
	e := sendAt(t, c, clk, tr, ep) // sendAt 会抓“超过 24 小时”；now = t0 + 24h，exp = t0 + 36h
	if e == d {
		t.Fatal("墙钟回拨 13 小时后应重签")
	}
	// 往回调 11 小时：exp 离此刻 23 小时，仍在界内，复用；再往回 1 秒：超过 23 小时，重签。
	clk.JumpWall(-11 * time.Hour)
	if got := sendAt(t, c, clk, tr, ep); got != e {
		t.Fatal("exp 离此刻 23 小时，应复用")
	}
	clk.JumpWall(-time.Second)
	if got := sendAt(t, c, clk, tr, ep); got == e {
		t.Fatal("exp 离此刻超过 23 小时，应重签")
	}
}

// 每 10 分钟往同一来源发一条、连发 48 小时：两次签发至少隔 1 小时（实际 11 小时），一共只签 5 次（0、11、22、33、44 小时）。
func TestClientVAPIDTokenRefreshRate(t *testing.T) {
	c, clk, tr := newTokenClient(t)
	var exps []int64
	last := ""
	for at := time.Duration(0); at <= 48*time.Hour; at += 10 * time.Minute {
		if at > 0 {
			clk.Advance(10 * time.Minute)
		}
		if auth := sendAt(t, c, clk, tr, "https://web.push.apple.com/QAbc"); auth != last {
			exps = append(exps, expOf(t, auth))
			last = auth
		}
	}
	for i := 1; i < len(exps); i++ {
		if gap := exps[i] - exps[i-1]; gap < 3600 {
			t.Fatalf("第 %d 次签发离上一次只隔 %d 秒", i, gap)
		}
	}
	if len(exps) != 5 {
		t.Fatalf("48 小时里签了 %d 次，应为 5 次", len(exps))
	}
}

// 换钥（新的 VAPID 值）或换 subject 后立即用新的现签，不复用旧钥或旧 sub 的令牌。
func TestClientVAPIDTokenFollowsKeyAndSubject(t *testing.T) {
	c, clk, tr := newTokenClient(t)
	const ep = "https://web.push.apple.com/QAbc"
	a1 := sendAt(t, c, clk, tr, ep)
	v2, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	c.VAPID = v2
	a2 := sendAt(t, c, clk, tr, ep) // sendAt 核对了 k 是新钥
	if a2 == a1 {
		t.Fatal("换钥后不应复用旧钥签的令牌")
	}
	jwt, _, _ := strings.Cut(strings.TrimPrefix(a2, "vapid t="), ", k=")
	parts := strings.Split(jwt, ".")
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(&v2.priv.PublicKey, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("换钥后的令牌应由新钥签")
	}
	c.Subject = "https://hub.example.com"
	if a3 := sendAt(t, c, clk, tr, ep); a3 == a2 { // sendAt 核对了 sub 是新的
		t.Fatal("换 subject 后不应复用旧令牌")
	}
}

// 并发发送（Task 5 的工作协程与 /api/push/test 的 HTTP 处理同时发）：同一时刻每个来源只签一次；过了窗口每个来源再只签一次。
func TestClientVAPIDTokenConcurrentSends(t *testing.T) {
	c, clk, tr := newTokenClient(t)
	endpoints := []string{"https://web.push.apple.com/QAbc", "https://fcm.googleapis.com/fcm/send/x"}
	subs := []Subscription{subFor(t, endpoints[0]), subFor(t, endpoints[1])}
	round := func() map[string]map[string]bool {
		n := len(tr.all())
		var wg sync.WaitGroup
		for i := range 32 {
			wg.Go(func() {
				if code, err := c.Send(context.Background(), subs[i%2], []byte("x"), Options{}); err != nil || code != 201 {
					t.Errorf("send: %d %v", code, err)
				}
			})
		}
		wg.Wait()
		byAud := map[string]map[string]bool{}
		for _, auth := range tr.all()[n:] {
			claims, _ := vapidOf(t, auth)
			if byAud[claims.Aud] == nil {
				byAud[claims.Aud] = map[string]bool{}
			}
			byAud[claims.Aud][auth] = true
		}
		return byAud
	}
	first := round()
	clk.Advance(11 * time.Hour)
	second := round()
	for _, ep := range endpoints {
		aud, _ := endpointOrigin(ep)
		if len(first[aud]) != 1 || len(second[aud]) != 1 {
			t.Fatalf("%s: 两轮各签了 %d、%d 个令牌，应各 1 个", aud, len(first[aud]), len(second[aud]))
		}
		for auth := range second[aud] {
			if first[aud][auth] {
				t.Fatalf("%s: 过了窗口应重签", aud)
			}
		}
	}
}

// 真实时钟下多个协程同时首发（缓存还空着）：每轮只签一个令牌。时钟要在锁里读，否则先读了时钟、后抢到锁的协程
// 拿着更早的时刻去判断，会把别人刚签的令牌当成“签发在未来”而重签。
func TestClientVAPIDTokenConcurrentFirstSendRealClock(t *testing.T) {
	sub := subFor(t, "https://web.push.apple.com/QAbc")
	for round := range 100 {
		v, err := GenerateVAPID()
		if err != nil {
			t.Fatal(err)
		}
		tr := &authTransport{}
		c := &Client{HTTP: &http.Client{Transport: tr}, VAPID: v, Subject: "mailto:push@example.com"}
		var wg sync.WaitGroup
		for range 16 {
			wg.Go(func() {
				if _, err := c.Send(context.Background(), sub, []byte("x"), Options{}); err != nil {
					t.Errorf("send: %v", err)
				}
			})
		}
		wg.Wait()
		distinct := map[string]bool{}
		for _, a := range tr.all() {
			distinct[a] = true
		}
		if len(distinct) != 1 {
			t.Fatalf("第 %d 轮：16 个协程同时首发签了 %d 个令牌，应为 1 个", round, len(distinct))
		}
	}
}

// gatedClock 把“先读了时钟、后抢到锁”的交错固定下来：第一次 Now() 取到 t0 后先不返回，等第二个 Send 发完
// （或 300 毫秒）再返回；之后的调用都返回 t0 + 1 秒。它还记下有没有在 Client 的锁外被调用。
type gatedClock struct {
	c       *Client
	t0      time.Time
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	outside atomic.Bool
}

func (g *gatedClock) Now() time.Time {
	if g.c.mu.TryLock() {
		g.c.mu.Unlock()
		g.outside.Store(true)
	}
	if g.calls.Add(1) == 1 {
		close(g.entered)
		select {
		case <-g.release:
		case <-time.After(300 * time.Millisecond):
		}
		return g.t0
	}
	return g.t0.Add(time.Second)
}

func TestClientVAPIDTokenClockReadUnderLock(t *testing.T) {
	c, clk, tr := newTokenClient(t)
	g := &gatedClock{c: c, t0: clk.Now(), entered: make(chan struct{}), release: make(chan struct{})}
	c.now = g.Now
	sub := subFor(t, "https://web.push.apple.com/QAbc")
	var wg sync.WaitGroup
	wg.Go(func() { // A：先读时钟
		if _, err := c.Send(context.Background(), sub, []byte("x"), Options{}); err != nil {
			t.Errorf("A: %v", err)
		}
	})
	<-g.entered
	wg.Go(func() { // B：A 读完时钟之后才开始
		defer close(g.release)
		if _, err := c.Send(context.Background(), sub, []byte("x"), Options{}); err != nil {
			t.Errorf("B: %v", err)
		}
	})
	wg.Wait()
	auths := tr.all()
	if len(auths) != 2 || auths[0] != auths[1] {
		t.Fatalf("两个并发首发应共用一个令牌，签了 %d 个", len(map[string]bool{auths[0]: true, auths[len(auths)-1]: true}))
	}
	if g.outside.Load() {
		t.Fatal("时钟应在 Client 的锁里读")
	}
}

// 用任何动词打印 Client（指针、嵌在各种字段与容器里）都带不出缓存的令牌。
// fmt 碰到不支持的动词（%s %q %t %e……）会把深层指针按 %v 解引用一层再打，所以令牌至少要隔两层指针。
func TestClientFormatHidesCachedToken(t *testing.T) {
	c, clk, tr := newTokenClient(t)
	auth := sendAt(t, c, clk, tr, "https://web.push.apple.com/QAbc")
	jwt, _, _ := strings.Cut(strings.TrimPrefix(auth, "vapid t="), ", k=")
	sig := jwt[strings.LastIndexByte(jwt, '.')+1:]
	needles := map[string]string{
		"JWT": jwt, "签名段": sig,
		"签名段 hex": hex.EncodeToString([]byte(sig)), "签名段 HEX": strings.ToUpper(hex.EncodeToString([]byte(sig))),
		"签名段 % x": fmt.Sprintf("% x", sig),
	}
	leaks := func(s string) string {
		for name, n := range needles {
			if strings.Contains(s, n) {
				return name
			}
		}
		return ""
	}
	verbs := []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "% x", "%d", "%t", "%e", "%E", "%f", "%g", "%b", "%o", "%O", "%c", "%U", "%p", "%w"}
	// 对照：令牌只隔一层指针时，%s 会把它打出来，说明下面的检查不是空的。
	if leaks(fmt.Sprintf(verbs[3], struct{ p *struct{ h string } }{&struct{ h string }{auth}})) == "" {
		t.Fatal("对照失败：一层指针后的令牌应被 s 动词打出并命中")
	}
	values := map[string]any{
		"指针":        c,
		"未导出字段":     struct{ c *Client }{c},
		"导出字段":      struct{ C *Client }{c},
		"切片":        []*Client{c},
		"map 值":     map[string]*Client{"c": c},
		"any 字段":    struct{ x any }{c},
		"结构体指针":     &struct{ c *Client }{c},
		"两层未导出字段":   struct{ in struct{ c *Client } }{struct{ c *Client }{c}},
		"指针的指针":     &c,
		"未导出字段里的切片": struct{ x []*Client }{[]*Client{c}},
	}
	for name, val := range values {
		for _, verb := range verbs {
			if s := fmt.Sprintf(verb, val); leaks(s) != "" {
				t.Errorf("%s 用 %s 打印带出了令牌（%s）: %.300s", name, verb, leaks(s), s)
			}
		}
	}
}
