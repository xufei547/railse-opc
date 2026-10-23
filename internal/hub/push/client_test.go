package push

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
