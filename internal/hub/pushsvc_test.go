package hub

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hub/push"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// fakePushEndpoint 冒充推送服务：记每次收到的请求头与路径，按 status 回应（reason 作响应体）；hang 为真时挂到请求被取消；
// delay 非零时每次先等这么久（纳秒）再回应。inflight 是正在处理的请求数，maxInflight 是它到过的最大值（看并行度）。
type fakePushEndpoint struct {
	srv         *httptest.Server
	status      atomic.Int32
	reason      atomic.Value // string
	hang        atomic.Bool
	delay       atomic.Int64
	inflight    atomic.Int32
	maxInflight atomic.Int32
	mu          sync.Mutex
	got         []http.Header
}

func newFakePushEndpoint(t *testing.T) *fakePushEndpoint {
	t.Helper()
	f := &fakePushEndpoint{}
	f.status.Store(201)
	f.reason.Store("")
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, io.LimitReader(r.Body, 8192))
		f.mu.Lock()
		f.got = append(f.got, r.Header.Clone())
		f.mu.Unlock()
		n := f.inflight.Add(1)
		defer f.inflight.Add(-1)
		for {
			m := f.maxInflight.Load()
			if n <= m || f.maxInflight.CompareAndSwap(m, n) {
				break
			}
		}
		if f.hang.Load() {
			<-r.Context().Done()
			return
		}
		time.Sleep(time.Duration(f.delay.Load()))
		w.WriteHeader(int(f.status.Load()))
		io.WriteString(w, f.reason.Load().(string))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePushEndpoint) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

func (f *fakePushEndpoint) header(i int) http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.got[i]
}

// testClock 是推送服务的可拨时钟：真实时间加一个可原子调整的偏移（工作协程与测试协程同时读写）。
type testClock struct{ off atomic.Int64 }

func (c *testClock) now() time.Time          { return time.Now().Add(time.Duration(c.off.Load())) }
func (c *testClock) advance(d time.Duration) { c.off.Add(int64(d)) }

// pushHub 起一个启用推送的 hub（--public-url https://hub.example.com），推送客户端信任假端点的证书；返回 hub 与登录 cookie。
func pushHub(t *testing.T, f *fakePushEndpoint) (*testHub, *http.Cookie) {
	t.Helper()
	h, ck, _ := pushHubClock(t, f)
	return h, ck
}

// pushHubClock 同 pushHub，另给推送服务装上可拨的时钟并返回它。
func pushHubClock(t *testing.T, f *fakePushEndpoint) (*testHub, *http.Cookie, *testClock) {
	t.Helper()
	clk := &testClock{}
	h := newTestHubWith(t, func(c *Config) { c.PublicURL = "https://hub.example.com" }, func(s *Server) {
		s.push.client.HTTP = f.srv.Client()
		s.push.now = clk.now
	})
	return h, loggedIn(t, h), clk
}

// ttlHeader 取第 i 次推送的 TTL 头（秒）。
func ttlHeader(t *testing.T, f *fakePushEndpoint, i int) int {
	t.Helper()
	var n int
	if _, err := fmt.Sscan(f.header(i).Get("TTL"), &n); err != nil {
		t.Fatalf("TTL 头: %v", err)
	}
	return n
}

// subKeys 是一条测试订阅的浏览器侧密钥（base64url），查泄露用。
type subKeys struct{ p256dh, auth string }

// subscribeJSON 编出浏览器 PushSubscription.toJSON() 形状的请求体：新生成的 P-256 公钥与随机 16 字节 auth。
func subscribeJSON(t *testing.T, endpoint string) ([]byte, subKeys) {
	t.Helper()
	ua, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	k := subKeys{p256dh: base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()), auth: base64.RawURLEncoding.EncodeToString(auth)}
	return mustJSON(t, map[string]any{"endpoint": endpoint, "expirationTime": nil, "keys": map[string]string{"p256dh": k.p256dh, "auth": k.auth}}), k
}

// subscribe 登记一条订阅，要求 204。
func subscribe(t *testing.T, h *testHub, ck *http.Cookie, body []byte) {
	t.Helper()
	if resp := doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck); resp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("subscribe: %d %s", resp.StatusCode, b)
	}
}

// vapidKey 取 GET /api/push/vapid 的公钥。
func vapidKey(t *testing.T, h *testHub, ck *http.Cookie) string {
	t.Helper()
	var v struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"public_key"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/push/vapid", nil, "", ck), &v)
	if !v.Enabled {
		t.Fatal("推送应启用")
	}
	return v.PublicKey
}

// agentEntry 是守护进程审计里的一条 question/done 事件（时间为此刻）。
func agentEntry(kind, session, msg string) agent.AuditEntry {
	return agent.AuditEntry{TS: time.Now(), Kind: kind, Session: proto.Session{Tool: "claude", CLISessionID: session, Cwd: "/p"}, State: "recorded", Message: msg}
}

func TestPushVapidAndSubscribe(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	var v map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/push/vapid", nil, "", ck), &v)
	if v["enabled"] != true || v["reason"] != "" || len(v) != 3 {
		t.Fatalf("vapid = %v", v)
	}
	key, _ := v["public_key"].(string)
	if len(key) != 87 {
		t.Fatalf("公钥长度 %d", len(key))
	}
	// 密钥持久化：同一个库再读一次公钥不变
	v2, err := LoadOrCreateVAPID(h.st)
	if err != nil || v2.PublicKeyB64() != key {
		t.Fatalf("公钥应持久: %v", err)
	}
	body, _ := subscribeJSON(t, f.srv.URL+"/p/one")
	subscribe(t, h, ck, body)
	// 重复订阅同一端点幂等
	subscribe(t, h, ck, body)
	if n, _ := h.st.CountPushSubscriptions(); n != 1 {
		t.Fatalf("订阅数 %d", n)
	}
	var st map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/status", nil, "", ck), &st)
	if st["push_enabled"] != true || st["push_subscriptions"] != float64(1) {
		t.Fatalf("status: %v", st)
	}
	chk := mustJSON(t, map[string]string{"endpoint": f.srv.URL + "/p/one"})
	var active map[string]bool
	decodeBody(t, doReq(t, "POST", h.http.URL+"/api/push/check", chk, "application/json", ck), &active)
	if !active["active"] || len(active) != 1 {
		t.Fatalf("check 应为 active: %v", active)
	}
	// 测试推送：同步发一条，不带 Topic
	if resp := doReq(t, "POST", h.http.URL+"/api/push/test", chk, "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("test push: %d", resp.StatusCode)
	}
	if f.count() != 1 {
		t.Fatalf("端点收到 %d 次", f.count())
	}
	if hd := f.header(0); hd.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(hd.Get("Authorization"), "vapid t=") ||
		!strings.Contains(hd.Get("Authorization"), "k="+key) || hd.Get("Topic") != "" {
		t.Fatalf("端点收到: %v", hd)
	}
	if resp := doReq(t, "DELETE", h.http.URL+"/api/push/subscribe", chk, "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unsubscribe: %d", resp.StatusCode)
	}
	// 再删一次照样 204
	if resp := doReq(t, "DELETE", h.http.URL+"/api/push/subscribe", chk, "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("再次 unsubscribe: %d", resp.StatusCode)
	}
	decodeBody(t, doReq(t, "POST", h.http.URL+"/api/push/check", chk, "application/json", ck), &active)
	if active["active"] {
		t.Fatal("删后 check 应为 false")
	}
	if resp := doReq(t, "POST", h.http.URL+"/api/push/test", chk, "application/json", ck); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删后 test push: %d", resp.StatusCode)
	}
	// 未登录
	for _, c := range []struct{ method, path string }{{"GET", "/api/push/vapid"}, {"POST", "/api/push/subscribe"}, {"DELETE", "/api/push/subscribe"}, {"POST", "/api/push/check"}, {"POST", "/api/push/test"}} {
		if resp := doReq(t, c.method, h.http.URL+c.path, chk, "application/json", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("未登录 %s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}
}

// 订阅先过 push.ParseSubscription 再入库；写接口跨站 403。
func TestPushSubscribeValidation(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	_, good := subscribeJSON(t, f.srv.URL+"/p/x")
	offCurve := base64.RawURLEncoding.EncodeToString(append([]byte{4}, make([]byte, 64)...))
	cases := []struct {
		name string
		body []byte
	}{
		{"http 端点", mustJSON(t, map[string]any{"endpoint": "http://insecure/p", "keys": map[string]string{"p256dh": good.p256dh, "auth": good.auth}})},
		{"不在曲线上的点", mustJSON(t, map[string]any{"endpoint": f.srv.URL + "/p", "keys": map[string]string{"p256dh": offCurve, "auth": good.auth}})},
		{"auth 15 字节", mustJSON(t, map[string]any{"endpoint": f.srv.URL + "/p", "keys": map[string]string{"p256dh": good.p256dh, "auth": base64.RawURLEncoding.EncodeToString(make([]byte, 15))}})},
		{"缺 keys", mustJSON(t, map[string]any{"endpoint": f.srv.URL + "/p"})},
		{"端点太长", mustJSON(t, map[string]any{"endpoint": f.srv.URL + "/" + strings.Repeat("a", 3000), "keys": map[string]string{"p256dh": good.p256dh, "auth": good.auth}})},
		{"不是 JSON", []byte("{")},
	}
	for _, c := range cases {
		resp := doReq(t, "POST", h.http.URL+"/api/push/subscribe", c.body, "application/json", ck)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: %d", c.name, resp.StatusCode)
		}
	}
	body, _ := subscribeJSON(t, f.srv.URL+"/p/x")
	for _, c := range []struct{ method, path string }{{"POST", "/api/push/subscribe"}, {"DELETE", "/api/push/subscribe"}, {"POST", "/api/push/check"}, {"POST", "/api/push/test"}} {
		resp := doReqHdr(t, c.method, h.http.URL+c.path, body, "application/json", ck, http.Header{"Origin": {"https://evil.example"}})
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("跨站 %s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}
	if n, _ := h.st.CountPushSubscriptions(); n != 0 {
		t.Fatalf("坏订阅不应入库: %d", n)
	}
}

func TestPushDisabledWithoutPublicURL(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	var v map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/push/vapid", nil, "", ck), &v)
	if v["enabled"] != false || v["public_key"] != "" || v["reason"] == "" {
		t.Fatalf("未配置 public-url: %v", v)
	}
	body, _ := subscribeJSON(t, "https://web.push.apple.com/x")
	if resp := doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未启用时订阅: %d", resp.StatusCode)
	}
	chk := mustJSON(t, map[string]string{"endpoint": "https://web.push.apple.com/x"})
	if resp := doReq(t, "POST", h.http.URL+"/api/push/test", chk, "application/json", ck); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未启用时测试推送: %d", resp.StatusCode)
	}
	var st map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/status", nil, "", ck), &st)
	if st["push_enabled"] != false || st["push_subscriptions"] != float64(0) {
		t.Fatalf("status: %v", st)
	}
	// 停用时新请求不入队
	h.srv.announceNew(store.Request{ID: "x1", Kind: "permission", CreatedAt: store.Now()})
	if n := len(h.srv.push.queue); n != 0 {
		t.Fatalf("停用时不应入队: %d", n)
	}
}

func TestPushOnNewRequestAndCleanup(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck, clk := pushHubClock(t, f)
	body, _ := subscribeJSON(t, f.srv.URL+"/p/one")
	subscribe(t, h, ck, body)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	c.send(pendingFrame("r1", "s1", "t1", "git push origin main"))
	waitFor(t, "新请求触发推送", func() bool { return f.count() == 1 })
	// TTL 是请求剩下的期限（pendingFrame 的期限是 86390 秒），不超过 24 小时
	if hd, ttl := f.header(0), ttlHeader(t, f, 0); hd.Get("Topic") != "r1" || hd.Get("Urgency") != "high" || ttl < 86380 || ttl > 86390 {
		t.Fatalf("投递参数: %v", hd)
	}
	waitFor(t, "审计 push_sent", func() bool {
		evs, _ := h.st.ListAudit(50, "r1")
		for _, e := range evs {
			if e.Action == "push_sent" && e.Actor == "system" && e.Detail == `{"failed":0,"gone":0,"sent":1}` {
				return true
			}
		}
		return false
	})
	// 410：订阅删除
	f.status.Store(410)
	c.send(pendingFrame("r2", "s1", "t2", "git push origin main"))
	waitFor(t, "410 后删订阅", func() bool { n, _ := h.st.CountPushSubscriptions(); return n == 0 })
	waitFor(t, "审计 gone", func() bool { return hasAudit(h, "push_subscription_removed", `"reason":"gone"`) })
	// 几秒内连续失败 5 次：只计数，订阅留着（短暂故障不删）
	f.status.Store(500)
	subscribe(t, h, ck, body)
	for i := 3; i <= 7; i++ {
		c.send(pendingFrame(fmt.Sprintf("r%d", i), "s1", fmt.Sprintf("t%d", i), "git push origin main"))
	}
	waitFor(t, "r7 推完", func() bool { return hasAuditFor(h, "r7", "push_sent") })
	if sub, err := h.st.GetPushSubscription(f.srv.URL + "/p/one"); err != nil || sub.Failures != 5 {
		t.Fatalf("5 次快速失败后订阅应留着: %+v %v", sub, err)
	}
	// 24 小时后又失败一次：连续失败 ≥ 5 次且跨度满 24 小时，删
	clk.advance(24 * time.Hour)
	c.send(pendingFrame("r8", "s1", "t8", "git push origin main"))
	waitFor(t, "跨度满 24 小时后删订阅", func() bool { n, _ := h.st.CountPushSubscriptions(); return n == 0 })
	waitFor(t, "审计 failures", func() bool { return hasAudit(h, "push_subscription_removed", `"reason":"failures"`) })
	// done 不推送
	f.status.Store(201)
	subscribe(t, h, ck, body)
	waitFor(t, "r8 推完", func() bool { return hasAuditFor(h, "r8", "push_sent") })
	before := f.count()
	c.send(auditFrame(t, agentEntry("done", "s1", "DONE")))
	waitFor(t, "done 入库", func() bool { rs, _ := h.st.ListRequests("recorded", 10); return len(rs) == 1 })
	time.Sleep(300 * time.Millisecond)
	if f.count() != before {
		t.Fatal("done 不应推送")
	}
}

func hasAudit(h *testHub, action, detail string) bool {
	evs, _ := h.st.ListAudit(500, "")
	for _, e := range evs {
		if e.Action == action && strings.Contains(e.Detail, detail) {
			return true
		}
	}
	return false
}

func hasAuditFor(h *testHub, requestID, action string) bool {
	evs, _ := h.st.ListAudit(50, requestID)
	for _, e := range evs {
		if e.Action == action {
			return true
		}
	}
	return false
}

// webhook 与新的 question 推送；守护进程补发的旧 question（事件时间早于 2 分钟前）不推，只进库。
func TestPushWebhookAndQuestions(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	tok := store.RandomToken()
	if err := h.st.SetSetting("webhook_token_hash", store.HashToken(tok)); err != nil {
		t.Fatal(err)
	}
	body, _ := subscribeJSON(t, f.srv.URL+"/p/one")
	subscribe(t, h, ck, body)
	wid := ingest(t, h, tok, map[string]any{"title": "部署到生产", "callback_url": "https://n8n.example/cb"})
	waitFor(t, "webhook 推送", func() bool { return f.count() == 1 })
	if f.header(0).Get("Topic") != wid {
		t.Fatalf("webhook 的 Topic: %v", f.header(0))
	}
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	stale := agentEntry("question", "s1", "旧问题")
	stale.TS = time.Now().Add(-pushQuestionMaxAge - time.Minute)
	c.send(auditFrame(t, stale))
	waitFor(t, "旧问题入库", func() bool { rs, _ := h.st.ListRequests("recorded", 10); return len(rs) == 1 })
	c.send(auditFrame(t, agentEntry("question", "s1", "新问题")))
	waitFor(t, "新问题推送", func() bool { return f.count() == 2 })
	time.Sleep(300 * time.Millisecond)
	if n := f.count(); n != 2 {
		t.Fatalf("旧问题不应推送: 端点收到 %d 次", n)
	}
}

// 失败怎么算：2xx 归零；404/410 删；本地错误、调用方取消（hub 关停）、413、429 只记日志不计；
// 超时、连不上、5xx、其他 4xx 计一次。日志带 StatusError.Reason 与端点的源，不带端点路径。
func TestPushFailureAccounting(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	endpoint := f.srv.URL + "/p/secret-path-xyz"
	body, _ := subscribeJSON(t, endpoint)
	subscribe(t, h, ck, body)
	sub, err := h.st.GetPushSubscription(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	p := h.srv.push
	msg := []byte(`{"id":"x"}`)
	o := push.Options{TTL: time.Minute}
	failures := func() int {
		t.Helper()
		s, err := h.st.GetPushSubscription(endpoint)
		if err != nil {
			t.Fatalf("订阅不应被删: %v", err)
		}
		return s.Failures
	}
	ctx := context.Background()
	for _, code := range []int{413, 429} {
		f.status.Store(int32(code))
		if out, err := p.sendOne(ctx, sub, msg, o); out != pushFailed || err == nil || failures() != 0 {
			t.Fatalf("%d 不应计失败: %v %v %d", code, out, err, failures())
		}
	}
	if out, err := p.sendOne(ctx, sub, msg, push.Options{Topic: "不合法"}); out != pushFailed || !errors.Is(err, push.ErrLocal) || failures() != 0 {
		t.Fatalf("本地错误不应计失败: %v %v", out, err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if out, err := p.sendOne(cctx, sub, msg, o); out != pushFailed || err == nil || failures() != 0 {
		t.Fatalf("取消不应计失败: %v %v", out, err)
	}
	f.status.Store(403)
	f.reason.Store(`{"reason":"BadJwtToken"}`)
	if out, _ := p.sendOne(ctx, sub, msg, o); out != pushFailed || failures() != 1 {
		t.Fatalf("403 应计失败: %v %d", out, failures())
	}
	f.status.Store(503)
	f.reason.Store("")
	if p.sendOne(ctx, sub, msg, o); failures() != 2 {
		t.Fatalf("503 应计失败: %d", failures())
	}
	f.hang.Store(true)
	p.sendTimeout = 100 * time.Millisecond
	if p.sendOne(ctx, sub, msg, o); failures() != 3 {
		t.Fatalf("超时应计失败: %d", failures())
	}
	f.hang.Store(false)
	f.status.Store(201)
	if out, err := p.sendOne(ctx, sub, msg, o); out != pushSent || err != nil || failures() != 0 {
		t.Fatalf("成功应归零: %v %v %d", out, err, failures())
	}
	// 连不上：计失败；错误里的完整地址不进日志
	dead := httptest.NewTLSServer(http.NotFoundHandler())
	deadURL := dead.URL + "/p/never-log-this-path"
	dead.Close()
	dbody, _ := subscribeJSON(t, deadURL)
	subscribe(t, h, ck, dbody)
	dsub, _ := h.st.GetPushSubscription(deadURL)
	if out, err := p.sendOne(ctx, dsub, msg, o); out != pushFailed || err == nil {
		t.Fatalf("连不上: %v %v", out, err)
	}
	if got, _ := h.st.GetPushSubscription(deadURL); got.Failures != 1 {
		t.Fatalf("连不上应计失败: %d", got.Failures)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "BadJwtToken") || !strings.Contains(logs, f.srv.URL) {
		t.Fatalf("日志应带 reason 与端点的源: %s", logs)
	}
	if strings.Contains(logs, "secret-path-xyz") || strings.Contains(logs, "never-log-this-path") {
		t.Fatalf("日志不应带端点路径: %s", logs)
	}
}

// keys 坏掉的订阅不挡别人的推送：推送时删掉它并审计 invalid。
func TestPushBrokenSubscriptionRemoved(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	body, _ := subscribeJSON(t, f.srv.URL+"/p/good")
	subscribe(t, h, ck, body)
	// 库里的 keys 是可解析的 JSON，但不是合法的 P-256 点
	if _, err := h.st.UpsertPushSubscription(f.srv.URL+"/p/bad", "AAAA", "AAAA", ""); err != nil {
		t.Fatal(err)
	}
	h.srv.announceNew(store.Request{ID: "b1", Kind: "webhook", Title: "x", CreatedAt: store.Now()})
	waitFor(t, "坏订阅删除", func() bool { n, _ := h.st.CountPushSubscriptions(); return n == 1 })
	waitFor(t, "好订阅照推", func() bool { return f.count() == 1 })
	waitFor(t, "审计", func() bool { return hasAudit(h, "push_subscription_removed", `"reason":"invalid"`) })
}

// pushMsg 是解出来的推送载荷。
type pushMsg struct{ ID, Kind, Title, Body, URL string }

// decodePush 返回把 pushMessage 的结果解成 pushMsg 的函数，便于 decode(pushMessage(...)) 直接套用。
func decodePush(t *testing.T) func([]byte, error) pushMsg {
	return func(b []byte, err error) pushMsg {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		var m pushMsg
		if err := json.Unmarshal(b, &m); err != nil {
			t.Fatalf("%s: %v", b, err)
		}
		return m
	}
}

// permission 的标题是“<CLI> 要<动词>”：动词按风险档，CLI 按 sessions.tool，没有会话、查不到或不认识的叫 Agent。
func TestPushMessagePermissionVerbs(t *testing.T) {
	decode := decodePush(t)
	perm := store.Request{ID: "r1", Kind: "permission", ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "ls"}}
	for _, c := range []struct{ risk, want string }{
		{"network", "Claude Code 要联网"},
		{"outward", "Claude Code 要往外发"},
		{"destructive", "Claude Code 要做不可逆的操作"},
		{"write_outside", "Claude Code 要改项目外的文件"},
		{"unknown", "Claude Code 要跑一个没见过的命令"},
		{"write_in_project", "Claude Code 要改项目里的文件"},
		{"read", "Claude Code 要读文件"},
		{"", "Claude Code 要做一件事"},
		{"weird", "Claude Code 要做一件事"},
	} {
		r := perm
		r.Risk = c.risk
		if m := decode(pushMessage(r, pushWho{Tool: "claude"})); m.Title != c.want {
			t.Errorf("风险 %q：标题 %q，要 %q", c.risk, m.Title, c.want)
		}
	}
	perm.Risk = "network"
	for tool, want := range map[string]string{"claude": "Claude Code 要联网", "codex": "Codex 要联网", "gemini": "Gemini CLI 要联网", "": "Agent 要联网", "cursor": "Agent 要联网"} {
		if m := decode(pushMessage(perm, pushWho{Tool: tool})); m.Title != want {
			t.Errorf("tool %q：标题 %q，要 %q", tool, m.Title, want)
		}
	}
}

// 有项目时标题追加项目；机器多于一台时再追加机器名（去掉结尾的 .local，不分大小写）。正文两行：摘要、“点开批准或拒绝”，
// 不再有“风险 network”与工具名。
func TestPushMessagePermissionWhereAndBody(t *testing.T) {
	decode := decodePush(t)
	perm := store.Request{ID: "r1", Kind: "permission", ToolName: "Bash", Risk: "network",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "  curl\n\thttps://example.com "}}
	m := decode(pushMessage(perm, pushWho{Tool: "claude", Project: "blog", Machine: "xufeideMacBook-Air.local"}))
	if m.ID != "r1" || m.Kind != "permission" || m.Title != "Claude Code 要联网 · blog" || m.Body != "curl https://example.com\n点开批准或拒绝" || m.URL != "/r/r1" {
		t.Fatalf("单机: %+v", m)
	}
	for _, c := range []struct {
		w    pushWho
		want string
	}{
		{pushWho{Tool: "claude"}, "Claude Code 要联网"},
		{pushWho{Tool: "claude", Machine: "mac.local"}, "Claude Code 要联网"},
		{pushWho{Tool: "codex", Project: "blog", Machine: "xufeideMacBook-Air.local", Many: true}, "Codex 要联网 · blog · xufeideMacBook-Air"},
		{pushWho{Tool: "codex", Project: "blog", Machine: "office.LOCAL", Many: true}, "Codex 要联网 · blog · office"},
		{pushWho{Tool: "codex", Project: "blog", Machine: "linux-box", Many: true}, "Codex 要联网 · blog · linux-box"},
		{pushWho{Tool: "codex", Machine: "mac.local", Many: true}, "Codex 要联网 · mac"},
		{pushWho{Tool: "codex", Project: "blog", Many: true}, "Codex 要联网 · blog"},
	} {
		if m := decode(pushMessage(perm, c.w)); m.Title != c.want {
			t.Errorf("%+v：标题 %q，要 %q", c.w, m.Title, c.want)
		}
	}
	for _, c := range []struct {
		pl   policy.Payload
		tool string
		want string
	}{
		{policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/a.go"}, "Edit", "/p/a.go\n点开批准或拒绝"},
		{policy.Payload{Type: policy.PayloadNetwork, URL: "https://x.example/a"}, "Edit", "https://x.example/a\n点开批准或拒绝"},
		{policy.Payload{Type: policy.PayloadMCP, Server: "gh", Tool: "create_issue"}, "Edit", "gh/create_issue\n点开批准或拒绝"},
		// 认不出的工具：适配器把工具名放在 Payload.Tool；没有就退回请求的工具名；不带网址的搜索同理
		{policy.Payload{Type: "other", Tool: "read_file"}, "read_file", "read_file\n点开批准或拒绝"},
		{policy.Payload{Type: "other"}, "ExitPlanMode", "ExitPlanMode\n点开批准或拒绝"},
		{policy.Payload{Type: policy.PayloadNetwork}, "WebSearch", "WebSearch\n点开批准或拒绝"},
		{policy.Payload{Type: "other"}, "", "点开批准或拒绝"},
	} {
		r := perm
		r.ToolName, r.Payload = c.tool, c.pl
		m := decode(pushMessage(r, pushWho{Tool: "claude"}))
		if m.Body != c.want || strings.Contains(m.Body, "other") {
			t.Errorf("%s：正文 %q，要 %q", c.pl.Type, m.Body, c.want)
		}
	}
	long := perm
	long.Payload.Command = strings.Repeat("x", 500)
	m = decode(pushMessage(long, pushWho{Tool: "claude"}))
	if first, second, ok := strings.Cut(m.Body, "\n"); !ok || first != strings.Repeat("x", pushSummaryLen) || second != "点开批准或拒绝" {
		t.Fatalf("摘要应截到 %d 个字符、第二行仍在: %q", pushSummaryLen, m.Body)
	}
}

// question：“<CLI> 在终端等你回答”，同样追加项目与（多机时）机器名，正文是问题；webhook：“工作流等你批准”，正文是来源给的标题。
func TestPushMessageQuestionAndWebhook(t *testing.T) {
	decode := decodePush(t)
	q := store.Request{ID: "q1", Kind: "question", Title: "要不要\n继续？"}
	m := decode(pushMessage(q, pushWho{Tool: "gemini", Project: "blog", Machine: "mac.local", Many: true}))
	if m.Title != "Gemini CLI 在终端等你回答 · blog · mac" || m.Body != "要不要 继续？" || m.URL != "/r/q1" || m.Kind != "question" {
		t.Fatalf("question: %+v", m)
	}
	if m := decode(pushMessage(q, pushWho{Tool: "claude", Project: "blog", Machine: "mac"})); m.Title != "Claude Code 在终端等你回答 · blog" {
		t.Fatalf("question 单机: %+v", m)
	}
	if m := decode(pushMessage(q, pushWho{})); m.Title != "Agent 在终端等你回答" {
		t.Fatalf("question 查不到会话: %+v", m)
	}
	wh := store.Request{ID: "w1", Kind: "webhook", Title: "部署到生产"}
	if m := decode(pushMessage(wh, pushWho{Project: "x", Machine: "mac", Many: true})); m.Title != "工作流等你批准" || m.Body != "部署到生产" || m.URL != "/r/w1" {
		t.Fatalf("webhook: %+v", m)
	}
	// id 里的特殊字符在 url 里转义
	odd := wh
	odd.ID = "a/b?c"
	if m := decode(pushMessage(odd, pushWho{})); m.ID != "a/b?c" || m.URL != "/r/a%2Fb%3Fc" {
		t.Fatalf("转义: %+v", m)
	}
}

// 标题超长：先把机器名从尾部截短（截没了连同分隔符去掉），再截项目名，最后整体截断；截断处不留尾随空格。
func TestPushTitleTrim(t *testing.T) {
	const head = "Claude Code 要联网" // 15 个字符
	proj := strings.Repeat("p", 20)
	mach := strings.Repeat("m", pushTitleLen-15-3-20-3) // 正好 80 个字符
	if got := pushTitle(head, proj, mach); got != head+" · "+proj+" · "+mach {
		t.Fatalf("放得下不应砍: %q", got)
	}
	if got := pushTitle(head, proj, mach+"M"); got != head+" · "+proj+" · "+mach {
		t.Fatalf("多一个字符应先砍机器名: %q", got)
	}
	if got := pushTitle(head, strings.Repeat("p", 100), "mac"); got != head+" · "+strings.Repeat("p", pushTitleLen-15-3) {
		t.Fatalf("机器名砍没了再砍项目名: %q", got)
	}
	if got := pushTitle(strings.Repeat("h", 100), "proj", "mac"); got != strings.Repeat("h", pushTitleLen) {
		t.Fatalf("最后整体截断: %q", got)
	}
	if got := pushTitle(head, strings.Repeat("p", 61)+" q", ""); got != head+" · "+strings.Repeat("p", 61) {
		t.Fatalf("截断处不留尾随空格: %q", got)
	}
	for _, c := range [][3]string{{head, strings.Repeat("项", 200), strings.Repeat("机", 200)}, {head, "", strings.Repeat("机", 200)}} {
		if n := utf8.RuneCountInString(pushTitle(c[0], c[1], c[2])); n > pushTitleLen {
			t.Fatalf("标题 %d 个字符", n)
		}
	}
}

// message 经真实的库：按 session_id 查 sessions.tool；单机不带机器名，第二台接入后带上（去 .local），吊销后又不带；
// 没有会话、会话查不到时 CLI 是 Agent。
func TestPushMessageLooksUpToolAndMachines(t *testing.T) {
	h := newTestHub(t)
	m1, err := h.st.CreateMachine("xufeideMacBook-Air.local", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := h.st.UpsertSession(m1.ID, proto.Session{Tool: "codex", CLISessionID: "s1", Cwd: "/p/blog"}, "blog")
	if err != nil {
		t.Fatal(err)
	}
	r := store.Request{ID: "r1", MachineID: m1.ID, SessionID: sid, Kind: "permission", ToolName: "shell", Risk: "network",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "curl https://example.com"}, ProjectRoot: "/p/blog"}
	title := func() string {
		t.Helper()
		b, err := h.srv.push.message(r)
		return decodePush(t)(b, err).Title
	}
	if got := title(); got != "Codex 要联网 · blog" {
		t.Fatalf("单机: %q", got)
	}
	// 同名重新接入（enroll --force）留下旧行、不吊销：仍是一个名字，不算多台
	if _, err := h.st.CreateMachine("xufeideMacBook-Air.local", store.RandomToken(), "/home/u"); err != nil {
		t.Fatal(err)
	}
	if got := title(); got != "Codex 要联网 · blog" {
		t.Fatalf("同名重接: %q", got)
	}
	m2, err := h.st.CreateMachine("linux-box", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if got := title(); got != "Codex 要联网 · blog · xufeideMacBook-Air" {
		t.Fatalf("两台: %q", got)
	}
	if err := h.st.RevokeMachine(m2.ID); err != nil {
		t.Fatal(err)
	}
	if got := title(); got != "Codex 要联网 · blog" {
		t.Fatalf("吊销一台后: %q", got)
	}
	r.SessionID = ""
	if got := title(); got != "Agent 要联网 · blog" {
		t.Fatalf("没有会话: %q", got)
	}
	r.SessionID = "missing"
	if got := title(); got != "Agent 要联网 · blog" {
		t.Fatalf("会话查不到: %q", got)
	}
}

// 最坏输入（128 字符的 id、超长且满是要转义字符的标题、命令、风险、项目路径、机器名）下载荷仍不超过 3 KiB、是合法 JSON，id 与 url 完整。
func TestPushMessageWorstCase(t *testing.T) {
	long := strings.Repeat("\x01\"\\ \U0001F600<&>\n", 1000)
	id := strings.Repeat("a", maxRequestIDLen)
	for _, kind := range []string{"permission", "question", "webhook"} {
		r := store.Request{ID: id, Kind: kind, ToolName: long, Payload: policy.Payload{Type: policy.PayloadShell, Command: long}, Risk: long, Title: long, ProjectRoot: "/" + long}
		b, err := pushMessage(r, pushWho{Tool: long, Project: projectName(r.ProjectRoot), Machine: long, Many: true})
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if len(b) > push.MaxPlaintext || !utf8.Valid(b) {
			t.Fatalf("%s: 载荷 %d 字节", kind, len(b))
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil || m["id"] != id || m["url"] != "/r/"+id || m["kind"] != kind {
			t.Fatalf("%s: %v %v", kind, m, err)
		}
		if strings.ContainsAny(m["title"], "\x01\n ") {
			t.Fatalf("%s: 标题不应带控制字符与换行: %q", kind, m["title"])
		}
		body := m["body"]
		if kind == "permission" {
			// 正文只有规整之后拼上的那一个换行，第二行是固定提示
			first, second, ok := strings.Cut(body, "\n")
			if !ok || second != "点开批准或拒绝" || strings.ContainsAny(first, "\x01\n ") {
				t.Fatalf("%s: 正文 %q", kind, body)
			}
		} else if strings.ContainsAny(body, "\x01\n ") {
			t.Fatalf("%s: 正文不应带控制字符与换行: %q", kind, body)
		}
	}
}

// encodePushPayload 量编码后的长度，超了先缩正文、再缩标题；id 本身就放不下时报错。
func TestEncodePushPayloadShrinks(t *testing.T) {
	m := pushPayload{ID: "x", Kind: "permission", Title: "标题", Body: strings.Repeat("\x01", 500), URL: "/r/x"}
	b, err := encodePushPayload(m, 300)
	var out pushPayload
	if err != nil || len(b) > 300 || json.Unmarshal(b, &out) != nil || out.Title != "标题" || out.Body == "" {
		t.Fatalf("缩正文: %d %v %+v", len(b), err, out)
	}
	m.Title, m.Body = strings.Repeat("\U0001F600", 200), ""
	if b, err := encodePushPayload(m, 300); err != nil || len(b) > 300 || !utf8.Valid(b) {
		t.Fatalf("缩标题: %d %v", len(b), err)
	}
	m.ID = strings.Repeat("\x01", 100)
	if _, err := encodePushPayload(m, 300); err == nil {
		t.Fatal("id 放不下应报错")
	}
}

// 请求 id 不是合法 Topic（超过 32 个字符）时不带 Topic，照样推。
func TestPushTopicOnlyWhenValid(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	body, _ := subscribeJSON(t, f.srv.URL+"/p/one")
	subscribe(t, h, ck, body)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	c.send(pendingFrame(strings.Repeat("a", 40), "s1", "t1", "git push"))
	waitFor(t, "推送", func() bool { return f.count() == 1 })
	if hd := f.header(0); hd.Get("Topic") != "" || hd.Get("Urgency") != "high" {
		t.Fatalf("不合法的 Topic 不应带: %v", hd)
	}
}

// 另一个进程（railse hub vapid --rotate）换钥：在跑的 hub 不用重启，GET /api/push/vapid 与之后的推送都用新钥；旧订阅全删。
func TestPushRotationPickedUpWithoutRestart(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	key1 := vapidKey(t, h, ck)
	body, _ := subscribeJSON(t, f.srv.URL+"/p/one")
	subscribe(t, h, ck, body)
	other, err := store.Open(filepath.Join(h.dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	v2, removed, err := RotateVAPID(other)
	if err != nil || removed != 1 || v2.PublicKeyB64() == key1 {
		t.Fatalf("rotate: %d %v", removed, err)
	}
	if n, _ := h.st.CountPushSubscriptions(); n != 0 {
		t.Fatalf("换钥应删全部订阅: %d", n)
	}
	if got := vapidKey(t, h, ck); got != v2.PublicKeyB64() {
		t.Fatal("在跑的 hub 应读到新公钥")
	}
	subscribe(t, h, ck, body)
	h.srv.announceNew(store.Request{ID: "w1", Kind: "webhook", Title: "x", CreatedAt: store.Now()})
	waitFor(t, "推送", func() bool { return f.count() == 1 })
	if a := f.header(0).Get("Authorization"); !strings.HasSuffix(a, "k="+v2.PublicKeyB64()) {
		t.Fatalf("应用新钥签名: %s", a)
	}
	// 测试推送也用新钥
	chk := mustJSON(t, map[string]string{"endpoint": f.srv.URL + "/p/one"})
	if resp := doReq(t, "POST", h.http.URL+"/api/push/test", chk, "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("test push: %d", resp.StatusCode)
	}
	if a := f.header(1).Get("Authorization"); !strings.HasSuffix(a, "k="+v2.PublicKeyB64()) {
		t.Fatalf("测试推送应用新钥: %s", a)
	}
}

// 新库首次启动时两个进程（hub 与 railse hub vapid）同时建密钥，只会有一把。
func TestLoadOrCreateVAPIDConcurrentFirstStart(t *testing.T) {
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "hub.db")
	var stores []*store.Store
	for i := 0; i < 4; i++ {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		stores = append(stores, st)
	}
	keys := make([]string, 16)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := LoadOrCreateVAPID(stores[i%len(stores)])
			if err != nil {
				t.Error(err)
				return
			}
			keys[i] = v.PublicKeyB64()
		}()
	}
	wg.Wait()
	for _, k := range keys {
		if k == "" || k != keys[0] {
			t.Fatalf("应只有一把密钥: %v", keys)
		}
	}
}

// 库里坏掉的 VAPID 密钥：New 报错，错误里不带密钥内容。
func TestLoadOrCreateVAPIDCorrupt(t *testing.T) {
	h := newTestHub(t)
	const bad = "not-a-valid-key-SECRETISH"
	h.st.SetSetting(vapidSetting, bad)
	_, err := LoadOrCreateVAPID(h.st)
	if err == nil || strings.Contains(err.Error(), bad) {
		t.Fatalf("坏密钥: %v", err)
	}
	if _, err := New(DefaultConfig(h.dir), h.st, log.New(io.Discard, "", 0)); err == nil {
		t.Fatal("坏密钥时 New 应报错")
	}
}

// /api/push/* 的响应、日志与审计里都不出现订阅密钥、VAPID 私钥与签好的 JWT。
func TestPushNeverLeaksSecrets(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	endpoint := f.srv.URL + "/p/one"
	body, keys := subscribeJSON(t, endpoint)
	chk := mustJSON(t, map[string]string{"endpoint": endpoint})
	var bodies []string
	call := func(method, path string, b []byte, want int) {
		t.Helper()
		resp := doReq(t, method, h.http.URL+path, b, "application/json", ck)
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
		}
		bodies = append(bodies, string(data))
	}
	call("GET", "/api/push/vapid", nil, 200)
	call("POST", "/api/push/subscribe", body, 204)
	badAuth := mustJSON(t, map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": keys.p256dh, "auth": keys.auth + "AAAA"}})
	call("POST", "/api/push/subscribe", badAuth, 400)
	badKey := mustJSON(t, map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": keys.p256dh[:40], "auth": keys.auth}})
	call("POST", "/api/push/subscribe", badKey, 400)
	call("POST", "/api/push/check", chk, 200)
	call("POST", "/api/push/test", chk, 204)
	f.status.Store(500)
	f.reason.Store(`{"reason":"InternalServerError"}`)
	call("POST", "/api/push/test", chk, 502)
	f.status.Store(410)
	call("POST", "/api/push/test", chk, 502)
	call("POST", "/api/push/subscribe", body, 204)
	call("DELETE", "/api/push/subscribe", chk, 204)
	call("GET", "/api/status", nil, 200)
	priv, ok, _ := h.st.GetSetting(vapidSetting)
	if !ok || len(priv) < 40 {
		t.Fatalf("私钥设置: %d", len(priv))
	}
	authz := f.header(0).Get("Authorization")
	jwt := strings.TrimSuffix(strings.SplitN(strings.TrimPrefix(authz, "vapid t="), ",", 2)[0], ",")
	if len(jwt) < 100 {
		t.Fatalf("没拿到 JWT: %q", authz)
	}
	sig := jwt[strings.LastIndexByte(jwt, '.')+1:]
	secrets := map[string]string{"p256dh": keys.p256dh, "auth": keys.auth, "VAPID 私钥": priv, "JWT 签名": sig}
	evs, _ := h.st.ListAudit(500, "")
	var audit strings.Builder
	for _, e := range evs {
		audit.WriteString(e.Detail)
	}
	for name, sec := range secrets {
		for i, b := range bodies {
			if strings.Contains(b, sec) {
				t.Fatalf("第 %d 个响应带了 %s: %s", i, name, b)
			}
		}
		if strings.Contains(h.logs.String(), sec) {
			t.Fatalf("日志带了 %s", name)
		}
		if strings.Contains(audit.String(), sec) {
			t.Fatalf("审计带了 %s", name)
		}
	}
	// 打印推送服务本身也带不出私钥与 JWT
	if s := fmt.Sprintf("%+v %#v %+v %#v", h.srv.push, h.srv.push, h.srv.push.client, h.srv.push.client); strings.Contains(s, priv) || strings.Contains(s, sig) {
		t.Fatal("打印 pushService 带出了秘密")
	}
}

// 队列满（256）时丢弃并记日志，不阻塞调用方。
func TestPushQueueFullDrops(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, _ := pushHub(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !h.srv.push.stop(ctx) { // 停掉工作协程，队列只进不出
		t.Fatal("工作协程应退出")
	}
	for i := 0; i <= pushQueueSize; i++ {
		h.srv.announceNew(store.Request{ID: fmt.Sprintf("q%d", i), Kind: "webhook", CreatedAt: store.Now()})
	}
	if !strings.Contains(h.logs.String(), "推送队列已满") {
		t.Fatalf("应记日志: %s", h.logs.String())
	}
}

// 工作协程在 New 里起（不依赖 Run），stopBackground 停下并等它退出；Run 返回前也等它退出。
func TestPushWorkerLifecycle(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, _ := pushHub(t, f)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !h.srv.stopBackground(ctx) {
		t.Fatal("stopBackground 应等到全部退出")
	}
	select {
	case <-h.srv.push.done:
	default:
		t.Fatal("工作协程应已退出")
	}
	if !h.srv.stopBackground(ctx) {
		t.Fatal("重复调用无害")
	}

	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := DefaultConfig(dir)
	cfg.Listen = "127.0.0.1:0"
	cfg.PublicURL = "https://hub.example.com"
	var logs syncBuf
	srv, err := New(cfg, st, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	rctx, rcancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(rctx) }()
	waitFor(t, "Run 开始监听", func() bool { return strings.Contains(logs.String(), "hub 监听 ") })
	rcancel()
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case <-srv.push.done:
	default:
		t.Fatal("Run 返回前推送工作协程应已退出")
	}
}

func TestConfigPublicURL(t *testing.T) {
	for _, u := range []string{"https://hub.example.com", "https://hub.example.com:8443", "https://localhost:8443", "https://127.0.0.1"} {
		if err := CheckPublicURL(u); err != nil {
			t.Fatalf("%q 应合法: %v", u, err)
		}
	}
	for _, u := range []string{"http://hub.example.com", "hub.example.com", "https://hub.example.com/", "https://hub.example.com/x", "https://h.example?x=1",
		"https://h.example?", "https://h.example#f", "https://u@h.example", "https://", "http://localhost:8787", "mailto:a@b.example", "https://h.example:x"} {
		if err := CheckPublicURL(u); err == nil {
			t.Fatalf("%q 应拒绝", u)
		}
		cfg := DefaultConfig("/tmp")
		cfg.PublicURL = u
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "public-url") {
			t.Fatalf("validate %q: %v", u, err)
		}
	}
}

// 推送服务短暂故障（几秒内连着 503）不删好订阅；中间成功一次就从头数；只有连续失败 ≥ 5 次且跨度满 24 小时才删。
func TestPushFailuresNeedDaySpan(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck, clk := pushHubClock(t, f)
	a, _ := subscribeJSON(t, f.srv.URL+"/p/a")
	b, _ := subscribeJSON(t, f.srv.URL+"/p/b")
	subscribe(t, h, ck, a)
	subscribe(t, h, ck, b)
	failures := func(ep string) int {
		t.Helper()
		s, err := h.st.GetPushSubscription(f.srv.URL + ep)
		if err != nil {
			return -1
		}
		return s.Failures
	}
	n := 0
	push := func() {
		t.Helper()
		n++
		id := fmt.Sprintf("w%d", n)
		h.srv.announceNew(store.Request{ID: id, Kind: "webhook", Title: "x", CreatedAt: store.Now()})
		waitFor(t, id+" 推完", func() bool { return hasAuditFor(h, id, "push_sent") })
	}
	f.status.Store(503)
	for i := 0; i < 6; i++ {
		push()
	}
	if failures("/p/a") != 6 || failures("/p/b") != 6 {
		t.Fatalf("几秒内 6 次 503 不应删订阅: %d %d", failures("/p/a"), failures("/p/b"))
	}
	// 成功一次：两条都归零
	f.status.Store(201)
	push()
	if failures("/p/a") != 0 || failures("/p/b") != 0 {
		t.Fatalf("成功后应归零: %d %d", failures("/p/a"), failures("/p/b"))
	}
	// 再失败 4 次，跨过 24 小时：次数不足 5，不删
	f.status.Store(503)
	for i := 0; i < 4; i++ {
		push()
		clk.advance(8 * time.Hour)
	}
	if failures("/p/a") != 4 {
		t.Fatalf("不足 5 次不应删: %d", failures("/p/a"))
	}
	// 第 5 次：这一轮从 32 小时前开始，删
	push()
	if failures("/p/a") != -1 || failures("/p/b") != -1 {
		t.Fatal("连续失败 5 次且跨度满 24 小时应删")
	}
	if !hasAudit(h, "push_subscription_removed", `"reason":"failures"`) {
		t.Fatal("应审计 failures")
	}
}

// 投递 TTL 是请求剩下的期限，至多 24 小时、至少 1 秒；没有期限（question）是 24 小时。
func TestPushTTLFollowsExpiry(t *testing.T) {
	now := time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *string { s := store.FormatTime(now.Add(d)); return &s }
	bad := "not a time"
	for _, c := range []struct {
		name    string
		expires *string
		want    time.Duration
	}{
		{"没有期限", nil, 24 * time.Hour},
		{"10 分钟后", at(10 * time.Minute), 10 * time.Minute},
		{"带零头", at(10*time.Minute + 700*time.Millisecond), 10 * time.Minute},
		{"48 小时后", at(48 * time.Hour), 24 * time.Hour},
		{"已过期", at(-time.Minute), time.Second},
		{"不到 1 秒", at(300 * time.Millisecond), time.Second},
		{"格式坏", &bad, 24 * time.Hour},
	} {
		if got := pushTTLFor(store.Request{ExpiresAt: c.expires}, now); got != c.want {
			t.Fatalf("%s: %v，应为 %v", c.name, got, c.want)
		}
	}

	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	tok := store.RandomToken()
	if err := h.st.SetSetting("webhook_token_hash", store.HashToken(tok)); err != nil {
		t.Fatal(err)
	}
	body, _ := subscribeJSON(t, f.srv.URL+"/p/one")
	subscribe(t, h, ck, body)
	ingest(t, h, tok, map[string]any{"title": "部署", "callback_url": "https://n8n.example/cb", "expires_in": 600})
	waitFor(t, "webhook 推送", func() bool { return f.count() == 1 })
	if ttl := ttlHeader(t, f, 0); ttl < 590 || ttl > 600 {
		t.Fatalf("webhook 的 TTL 应是剩下的期限: %d", ttl)
	}
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	c.send(auditFrame(t, agentEntry("question", "s1", "继续吗")))
	waitFor(t, "question 推送", func() bool { return f.count() == 2 })
	if ttl := ttlHeader(t, f, 1); ttl != 86400 {
		t.Fatalf("question 没有期限，TTL 应为 24 小时: %d", ttl)
	}
}

// Run 没能开始监听（ErrListen）时也停下 New 里起的推送工作协程，调用方随后可以照常关库。
func TestRunListenFailureStopsPushWorker(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := DefaultConfig(dir)
	cfg.Listen = busy.Addr().String()
	cfg.PublicURL = "https://hub.example.com"
	srv, err := New(cfg, st, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Run(context.Background()); !errors.Is(err, ErrListen) {
		t.Fatalf("端口被占应返回 ErrListen: %v", err)
	}
	select {
	case <-srv.push.done:
	default:
		t.Fatal("ErrListen 时推送工作协程应已退出")
	}
}

// 摘要先去掉空白与零宽字符再截断：命令前面塞满 U+200B 或空白，摘要照样显示真正的命令。
func TestPushMessageZeroWidthPadding(t *testing.T) {
	r := store.Request{ID: "r1", Kind: "permission", ToolName: "Bash", Risk: "destructive",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: strings.Repeat("\u200b", 640) + strings.Repeat(" \t\n", 700) + "rm -rf ~/x"}}
	b, err := pushMessage(r, pushWho{Tool: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil || m["body"] != "rm -rf ~/x\n点开批准或拒绝" {
		t.Fatalf("摘要: %q %v", m["body"], err)
	}
	// 截断后不留尾随空格
	if got := clean("abcd efgh", 5); got != "abcd" {
		t.Fatalf("clean = %q", got)
	}
	if got := clean("  a\u200b\u200bb  \n c ", 10); got != "ab c" {
		t.Fatalf("clean = %q", got)
	}
}

// 一条请求对多个订阅并行发送（pushWorkers 路）：假端点每次慢 300ms，看端点同时在处理的请求数——串行时恒为 1，
// 并行时大于 1 且不超过 pushWorkers（不量墙钟，负载高的机器上也稳）。结果照常汇总进 push_sent。
// 恰好 pushWorkers 路在途由 TestPushDeliverStopsOnCancel 用挂住的端点确定性地验证。
func TestPushDeliversInParallel(t *testing.T) {
	f := newFakePushEndpoint(t)
	f.delay.Store(int64(300 * time.Millisecond))
	h, ck := pushHub(t, f)
	for i := 0; i < 8; i++ {
		body, _ := subscribeJSON(t, fmt.Sprintf("%s/p/%d", f.srv.URL, i))
		subscribe(t, h, ck, body)
	}
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	c.send(pendingFrame("r1", "s1", "t1", "git push origin main"))
	waitFor(t, "审计 push_sent", func() bool { return hasAuditFor(h, "r1", "push_sent") })
	if m := f.maxInflight.Load(); m < 2 || m > pushWorkers {
		t.Fatalf("端点同时在处理的推送至多 %d 条，应在 2 到 %d 之间（串行时为 1）", m, pushWorkers)
	}
	if f.count() != 8 || !hasAudit(h, "push_sent", `{"failed":0,"gone":0,"sent":8}`) {
		t.Fatalf("应发出 8 条: 端点收到 %d 次", f.count())
	}
}

// 并行发送时 hub 关停（ctx 取消）：在途的中止、不计失败；还没轮到的不再发；全部算进 push_sent 的 failed。
func TestPushDeliverStopsOnCancel(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	const n = 3 * pushWorkers
	for i := 0; i < n; i++ {
		body, _ := subscribeJSON(t, fmt.Sprintf("%s/p/%d", f.srv.URL, i))
		subscribe(t, h, ck, body)
	}
	f.hang.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		h.srv.push.deliver(ctx, store.Request{ID: "w1", Kind: "webhook", Title: "部署", CreatedAt: store.Now()})
	}()
	waitFor(t, "每路各有一条在途", func() bool { return f.count() == pushWorkers })
	time.Sleep(100 * time.Millisecond) // 并行上限是 pushWorkers：在途的挂着时不会再多发
	if got := f.count(); got != pushWorkers {
		t.Fatalf("同时在途的推送应是 %d 条，得到 %d", pushWorkers, got)
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后 deliver 应返回")
	}
	if got := f.count(); got != pushWorkers {
		t.Fatalf("取消后不应再发: 端点收到 %d 次", got)
	}
	if !hasAudit(h, "push_sent", fmt.Sprintf(`{"failed":%d,"gone":0,"sent":0}`, n)) {
		t.Fatalf("取消后全部算 failed: %s", h.logs.String())
	}
	if n2, _ := h.st.CountPushSubscriptions(); n2 != n {
		t.Fatalf("取消不应删订阅: %d", n2)
	}
}
