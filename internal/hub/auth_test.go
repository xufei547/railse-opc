package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/hub/store"
)

type testHub struct {
	srv  *Server
	st   *store.Store
	http *httptest.Server
	dir  string
	logs *syncBuf // hub 的日志（含 agents 的），测试失败时才打印出来
}

func newTestHub(t *testing.T) *testHub { return newTestHubWith(t, nil, nil) }

// newTestHubWith 用默认配置起一个 hub：mod 非空时先改配置，srvMod 非空时在开始服务前改 Server（测试钩子）。
// hub 的日志写进按测试分配的缓冲（h.logs），只在测试失败时 t.Log 出来，测试输出里没有游离的日志行。
func newTestHubWith(t *testing.T, mod func(*Config), srvMod func(*Server)) *testHub {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig(dir)
	if mod != nil {
		mod(&cfg)
	}
	logs := &syncBuf{}
	srv, err := New(cfg, st, log.New(logs, "hub ", 0))
	if err != nil {
		t.Fatal(err)
	}
	if srvMod != nil {
		srvMod(srv)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		hs.Close()
		// 先停后台工作（取消并等回调与推送工作协程退出），再关库：它们不会在测试结束后还碰存储
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		srv.stopBackground(ctx)
		cancel()
		st.Close()
		if t.Failed() {
			t.Logf("hub 日志:\n%s", logs.String())
		}
	})
	return &testHub{srv: srv, st: st, http: hs, dir: dir, logs: logs}
}

// postJSON 发 JSON 请求；响应体读进内存后关掉原连接，调用方可再读 resp.Body。
func postJSON(t *testing.T, url string, body any, cookie *http.Cookie) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return postRaw(t, url, b, cookie)
}

func postRaw(t *testing.T, url string, body []byte, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return do(t, req)
}

func do(t *testing.T, req *http.Request) *http.Response {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(b))
	return resp
}

func login(t *testing.T, h *testHub, pw string) *http.Cookie {
	t.Helper()
	resp := postJSON(t, h.http.URL+"/api/login", map[string]string{"password": pw}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "railse_session" {
			return c
		}
	}
	t.Fatal("没有 railse_session cookie")
	return nil
}

func TestPasswordHashRoundTrip(t *testing.T) {
	enc, err := HashPassword("hunter2")
	if err != nil || !VerifyPassword(enc, "hunter2") || VerifyPassword(enc, "hunter3") || VerifyPassword("garbage", "hunter2") {
		t.Fatalf("enc=%s err=%v", enc, err)
	}
}

// 编码串固定为 argon2id$v=19$m=65536,t=1,p=4$<盐>$<哈希>：盐 16 字节、哈希 32 字节，base64 raw std；每次盐不同。
func TestHashPasswordFormat(t *testing.T) {
	a, err := HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	b, err := HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("两次哈希应因盐不同而不同")
	}
	parts := strings.Split(a, "$")
	if len(parts) != 5 || strings.Join(parts[:3], "$") != "argon2id$v=19$m=65536,t=1,p=4" {
		t.Fatalf("编码 = %s", a)
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[3])
	key, err2 := base64.RawStdEncoding.DecodeString(parts[4])
	if err1 != nil || err2 != nil || len(salt) != 16 || len(key) != 32 {
		t.Fatalf("盐 %d 字节、哈希 %d 字节: %v %v", len(salt), len(key), err1, err2)
	}
}

// 编码串严格解析：任何一处不符都返回 false，不 panic（t=0、p=0 会让 argon2 panic），也不按串里的参数去算。
func TestVerifyPasswordStrict(t *testing.T) {
	enc, err := HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(enc, "$")
	with := func(i int, v string) string {
		p := append([]string(nil), parts...)
		p[i] = v
		return strings.Join(p, "$")
	}
	short := base64.RawStdEncoding.EncodeToString(make([]byte, 8))
	cases := map[string]string{
		"空串":          "",
		"垃圾":          "garbage",
		"前导 $":        "$" + enc,
		"多一段":         enc + "$x",
		"少一段":         strings.Join(parts[:4], "$"),
		"算法 argon2i":  with(0, "argon2i"),
		"版本 18":       with(1, "v=18"),
		"t=0":         with(2, "m=65536,t=0,p=4"),
		"p=0":         with(2, "m=65536,t=1,p=0"),
		"内存不同":        with(2, "m=1048576,t=1,p=4"),
		"参数尾随垃圾":      with(2, "m=65536,t=1,p=4x"),
		"盐不是 base64":  with(3, "!!!"),
		"盐带填充":        with(3, parts[3]+"=="),
		"盐太短":         with(3, short),
		"哈希太短":        with(4, short),
		"哈希不是 base64": with(4, "@@@"),
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if VerifyPassword(c, "hunter22") {
				t.Fatalf("%q 应校验失败", c)
			}
		})
	}
	if !VerifyPassword(enc, "hunter22") {
		t.Fatal("原编码应通过")
	}
}

// password_hash 只存 argon2id 编码串，不存原文；太短的密码被拒且不改动已有密码。
func TestSetPasswordStoresHashOnly(t *testing.T) {
	h := newTestHub(t)
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	enc, ok, err := h.st.GetSetting("password_hash")
	if err != nil || !ok || !strings.HasPrefix(enc, "argon2id$v=19$m=65536,t=1,p=4$") || strings.Contains(enc, "hunter22") || !VerifyPassword(enc, "hunter22") {
		t.Fatalf("password_hash = %q, %v, %v", enc, ok, err)
	}
	if _, err := h.srv.SetPassword("short"); err == nil {
		t.Fatal("太短的密码应被拒")
	}
	if got, _, _ := h.st.GetSetting("password_hash"); got != enc {
		t.Fatal("被拒的密码不应改动已有密码")
	}
}

func TestLoginFlow(t *testing.T) {
	h := newTestHub(t)
	if resp := postJSON(t, h.http.URL+"/api/login", map[string]string{"password": "x"}, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未设密码应 503, 得到 %d", resp.StatusCode)
	}
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	if resp := postJSON(t, h.http.URL+"/api/login", map[string]string{"password": "wrong"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错密码应 401, 得到 %d", resp.StatusCode)
	}
	c := login(t, h, "hunter22")
	if !c.HttpOnly || c.Path != "/" || c.SameSite != http.SameSiteLaxMode || c.Secure || c.MaxAge != 30*24*3600 {
		t.Fatalf("cookie 属性: %+v", c)
	}
	// 受保护接口
	req, _ := http.NewRequest("GET", h.http.URL+"/api/status", nil)
	if resp := do(t, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 cookie 应 401, 得到 %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", h.http.URL+"/api/status", nil)
	req.AddCookie(&http.Cookie{Name: "railse_session", Value: "forged"})
	if resp := do(t, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("伪造 cookie 应 401, 得到 %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", h.http.URL+"/api/status", nil)
	req.AddCookie(c)
	if resp := do(t, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("有 cookie 应 200, 得到 %d", resp.StatusCode)
	}
	// 登出：服务端删会话，并下发清除 cookie（属性与登录时一致）
	resp := postJSON(t, h.http.URL+"/api/logout", nil, c)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	var cleared *http.Cookie
	for _, x := range resp.Cookies() {
		if x.Name == "railse_session" {
			cleared = x
		}
	}
	if cleared == nil || cleared.Value != "" || cleared.MaxAge >= 0 || !cleared.HttpOnly || cleared.Path != "/" || cleared.SameSite != http.SameSiteLaxMode {
		t.Fatalf("登出应清除 cookie: %+v", cleared)
	}
	req, _ = http.NewRequest("GET", h.http.URL+"/api/status", nil)
	req.AddCookie(c)
	if resp := do(t, req); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("登出后应 401, 得到 %d", resp.StatusCode)
	}
}

// 请求体不是 JSON、缺 password 或超长时回 400。
func TestLoginBadBody(t *testing.T) {
	h := newTestHub(t)
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	big, _ := json.Marshal(map[string]string{"password": strings.Repeat("a", 5000)})
	for name, body := range map[string][]byte{"非 JSON": []byte("{"), "缺 password": []byte(`{}`), "超长": big} {
		if resp := postRaw(t, h.http.URL+"/api/login", body, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s 应 400, 得到 %d", name, resp.StatusCode)
		}
	}
}

// SecureCookies 打开或请求经 TLS 时，登录与登出的 cookie 都带 Secure。
func TestSessionCookieSecure(t *testing.T) {
	h := newTestHubWith(t, func(c *Config) { c.SecureCookies = true }, nil)
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	if c := login(t, h, "hunter22"); !c.Secure {
		t.Fatalf("SecureCookies 时应带 Secure: %+v", c)
	}

	h = newTestHub(t)
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/login", "/api/logout"} {
		req := httptest.NewRequest("POST", "https://hub.example"+path, strings.NewReader(`{"password":"hunter22"}`))
		rec := httptest.NewRecorder()
		h.srv.Handler().ServeHTTP(rec, req)
		cs := rec.Result().Cookies()
		if rec.Code != http.StatusNoContent || len(cs) != 1 || !cs[0].Secure {
			t.Fatalf("%s 经 TLS = %d, cookie %+v", path, rec.Code, cs)
		}
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := newTestHub(t)
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if code := postJSON(t, h.http.URL+"/api/login", map[string]string{"password": "wrong"}, nil).StatusCode; code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次应 401, 得到 %d", i+1, code)
		}
	}
	if code := postJSON(t, h.http.URL+"/api/login", map[string]string{"password": "hunter22"}, nil).StatusCode; code != http.StatusTooManyRequests {
		t.Fatalf("第 6 次（即使密码正确）应 429, 得到 %d", code)
	}
}

// limiter 每个键在窗口内最多放行 max 次；窗口滑过后最早的时间戳被丢弃，再次放行。
func TestLimiterWindowSlides(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	t0 := now
	for i := 0; i < 5; i++ {
		if !l.allow("k", 5, time.Minute) {
			t.Fatalf("第 %d 次应放行", i+1)
		}
		now = now.Add(time.Second)
	}
	if l.allow("k", 5, time.Minute) {
		t.Fatal("第 6 次应拒绝")
	}
	if !l.allow("other", 5, time.Minute) {
		t.Fatal("别的键不受影响")
	}
	now = t0.Add(59 * time.Second)
	if l.allow("k", 5, time.Minute) {
		t.Fatal("窗口未滑过，仍应拒绝")
	}
	now = t0.Add(time.Minute) // 第 1 次滑出窗口
	if !l.allow("k", 5, time.Minute) {
		t.Fatal("最早一次滑出窗口后应再放行")
	}
	if l.allow("k", 5, time.Minute) {
		t.Fatal("窗口内又满 5 次，应拒绝")
	}
	if n := len(l.hits["k"].times); n != 5 {
		t.Fatalf("窗口外的时间戳应已丢弃，留 %d 个", n)
	}
	now = t0.Add(time.Minute + 4*time.Second) // 第 2 到 5 次滑出
	for i := 0; i < 4; i++ {
		if !l.allow("k", 5, time.Minute) {
			t.Fatalf("滑出 4 次后第 %d 次应放行", i+1)
		}
	}
}

// 整段滑出窗口的键会被清掉：来源 IP 再多，表也不会无限增长。
func TestLimiterDropsStaleKeys(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < 100; i++ {
		l.allow("ip-"+string(rune('a'+i%26))+string(rune('a'+i/26)), 5, time.Minute)
	}
	if len(l.hits) != 100 {
		t.Fatalf("hits = %d", len(l.hits))
	}
	now = now.Add(2 * time.Minute)
	l.allow("fresh", 5, time.Minute)
	if len(l.hits) != 1 {
		t.Fatalf("过期的键应已清掉，剩 %d 个", len(l.hits))
	}
	if l.allow("never", 0, time.Minute) || len(l.hits) != 1 {
		t.Fatalf("被拒且没有时间戳的键不应留在表里: %d", len(l.hits))
	}
}

func TestEnroll(t *testing.T) {
	h := newTestHub(t)
	if resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": "bad", "name": "mac"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("坏 token 应 401, 得到 %d", resp.StatusCode)
	}
	tok, err := h.st.CreateEnrollToken(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": tok, "name": "mac", "home_dir": "/Users/u"}, nil)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("enroll = %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	var out struct {
		MachineID string `json:"machine_id"`
		Secret    string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.MachineID == "" || out.Secret == "" {
		t.Fatalf("enroll 返回 %+v, %v", out, err)
	}
	req, _ := http.NewRequest("GET", "/ws/agent", nil)
	// 方案名大小写不敏感，值去首尾空白
	for _, v := range []string{"Bearer " + out.Secret, "bearer " + out.Secret, "BEARER  " + out.Secret + " "} {
		req.Header.Set("Authorization", v)
		if m, err := h.srv.authMachine(req); err != nil || m.ID != out.MachineID || m.HomeDir != "/Users/u" {
			t.Fatalf("Authorization %q: authMachine = %+v, %v", v, m, err)
		}
	}
	for _, v := range []string{"Bearer nope", "", "Bearer ", "Bearer    ", "Bearer", "Bearer" + out.Secret, "Basic " + out.Secret, out.Secret} {
		req.Header.Set("Authorization", v)
		if _, err := h.srv.authMachine(req); err == nil {
			t.Fatalf("Authorization %q 应失败", v)
		}
	}
	if resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": tok, "name": "mac"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("接入码只能用一次")
	}
}

// 不合法的接入请求回 400 且不消费接入码；name 128 字节、home_dir 4096 字节为上限。
func TestEnrollRejectsBadInput(t *testing.T) {
	h := newTestHub(t)
	tok, err := h.st.CreateEnrollToken(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	big, _ := json.Marshal(map[string]string{"token": tok, "name": "m", "pad": strings.Repeat("a", 20000)})
	cases := map[string][]byte{
		"缺 name":      mustJSON(t, map[string]string{"token": tok}),
		"name 太长":     mustJSON(t, map[string]string{"token": tok, "name": strings.Repeat("n", 129)}),
		"home_dir 太长": mustJSON(t, map[string]string{"token": tok, "name": "m", "home_dir": "/" + strings.Repeat("d", 4096)}),
		"请求体超长":       big,
	}
	for name, body := range cases {
		if resp := postRaw(t, h.http.URL+"/api/enroll", body, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s 应 400, 得到 %d", name, resp.StatusCode)
		}
	}
	resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": tok, "name": strings.Repeat("n", 128), "home_dir": "/" + strings.Repeat("d", 4095)}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("恰好到上限应接入成功且接入码未被消费, 得到 %d", resp.StatusCode)
	}
	if list, err := h.st.ListMachines(); err != nil || len(list) != 1 {
		t.Fatalf("应只有一台机器: %+v, %v", list, err)
	}
}

// 接入审计的 detail 是合法 JSON，名字里的引号、反斜杠、非 ASCII（含 %q 会转成 \U 的不可打印字符）原样往返。
// \U000F0000 是私用区字符（Co）：不可打印，但不是 ValidName 拒绝的控制或格式字符。
func TestEnrollAuditDetail(t *testing.T) {
	h := newTestHub(t)
	tok, err := h.st.CreateEnrollToken(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	name := "mac \"pro\" \\ 工作站 \u2028 \U000F0000"
	resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": tok, "name": name}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll = %d", resp.StatusCode)
	}
	var out struct {
		MachineID string `json:"machine_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	events, err := h.st.ListAudit(10, "")
	if err != nil || len(events) != 1 {
		t.Fatalf("审计 = %+v, %v", events, err)
	}
	e := events[0]
	var detail map[string]string
	if err := json.Unmarshal([]byte(e.Detail), &detail); err != nil || detail["name"] != name || len(detail) != 1 {
		t.Fatalf("detail = %s, %v", e.Detail, err)
	}
	if e.MachineID != out.MachineID || e.Actor != "system" || e.Action != "machine_enrolled" {
		t.Fatalf("审计 = %+v", e)
	}
}

// 接入接口同样按来源 IP 限速，第 6 次 429。
func TestEnrollRateLimit(t *testing.T) {
	h := newTestHub(t)
	for i := 0; i < 5; i++ {
		if code := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": "bad", "name": "m"}, nil).StatusCode; code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次应 401, 得到 %d", i+1, code)
		}
	}
	if code := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": "bad", "name": "m"}, nil).StatusCode; code != http.StatusTooManyRequests {
		t.Fatalf("第 6 次应 429, 得到 %d", code)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// waitFor 轮询 cond 直到为真，5 秒超时。只在测试 goroutine 里调用。
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("等待超时: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// loginCode 发一次登录，返回状态码；出错返回 -1。可在 goroutine 里调用（不调 t.Fatal）。
func loginCode(url, pw string) int {
	b, _ := json.Marshal(map[string]string{"password": pw})
	resp, err := http.Post(url+"/api/login", "application/json", bytes.NewReader(b))
	if err != nil {
		return -1
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

// 同时在算 argon2 的登录不超过 2 个：名额占满时新的登录等 verifyWait 后回 503，不进入校验；名额归还后照常。
func TestLoginArgonConcurrencyCap(t *testing.T) {
	var inFlight, peak, calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	h := newTestHubWith(t, nil, func(s *Server) {
		s.verifyWait = 100 * time.Millisecond
		s.verify = func(enc, pw string) bool {
			calls.Add(1)
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			<-release
			return VerifyPassword(enc, pw)
		}
	})
	// 在 hub 的清理（hs.Close 会等在途请求）之后注册，先于它运行：测试中途失败也不会卡在被阻塞的校验上
	t.Cleanup(unblock)
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { codes <- loginCode(h.http.URL, "hunter22") }()
	}
	waitFor(t, "两个登录进入校验", func() bool { return inFlight.Load() == 2 })
	third := make(chan int, 1)
	go func() { third <- loginCode(h.http.URL, "hunter22") }()
	select {
	case code := <-third:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("名额占满时应 503, 得到 %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("名额占满时第三个登录应在 verifyWait 后回 503，却一直没返回（同时校验 %d 个）", inFlight.Load())
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("等不到名额的登录不应进入校验: 校验 %d 次", n)
	}
	unblock()
	for i := 0; i < 2; i++ {
		if code := <-codes; code != http.StatusNoContent {
			t.Fatalf("被放行的登录应 204, 得到 %d", code)
		}
	}
	if code := loginCode(h.http.URL, "wrong-pass"); code != http.StatusUnauthorized {
		t.Fatalf("名额归还后应照常校验, 得到 %d", code)
	}
	if p := peak.Load(); p != 2 {
		t.Fatalf("同时校验峰值 = %d，应为 2", p)
	}
}

// 换密码后已有会话全部失效：旧 cookie 访问需要登录的接口得 401；旧密码登录 401，新密码 204。
func TestSetPasswordRevokesSessions(t *testing.T) {
	h := newTestHub(t)
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	c := login(t, h, "hunter22")
	status := func() int {
		req, _ := http.NewRequest("GET", h.http.URL+"/api/status", nil)
		req.AddCookie(c)
		return do(t, req).StatusCode
	}
	if code := status(); code != http.StatusOK {
		t.Fatalf("换密码前应 200, 得到 %d", code)
	}
	if _, err := h.srv.SetPassword("another-pass"); err != nil {
		t.Fatal(err)
	}
	if code := status(); code != http.StatusUnauthorized {
		t.Fatalf("换密码后旧 cookie 应 401, 得到 %d", code)
	}
	if code := loginCode(h.http.URL, "hunter22"); code != http.StatusUnauthorized {
		t.Fatalf("旧密码应 401, 得到 %d", code)
	}
	login(t, h, "another-pass")
}

// 换密码删掉全部推送订阅（丢了的设备随之切断），返回删掉的会话与订阅数，日志只记条数（不记端点）。
func TestSetPasswordClearsPushSubscriptions(t *testing.T) {
	h := newTestHub(t)
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	login(t, h, "hunter22")
	for _, ep := range []string{"https://web.push.apple.com/lost-phone", "https://fcm.googleapis.com/fcm/send/other"} {
		if _, err := h.st.UpsertPushSubscription(ep, "p", "a", ""); err != nil {
			t.Fatal(err)
		}
	}
	ch, err := h.srv.SetPassword("another-pass")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Sessions != 1 || ch.PushSubscriptions != 2 {
		t.Fatalf("换密码删掉的 = %+v", ch)
	}
	if n, _ := h.st.CountPushSubscriptions(); n != 0 {
		t.Fatalf("换密码后订阅数 = %d，应为 0", n)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "已换网页登录密码：删除了 1 个登录会话、2 条推送订阅") {
		t.Fatalf("日志应记条数: %q", logs)
	}
	if strings.Contains(logs, "lost-phone") || strings.Contains(logs, "another-pass") {
		t.Fatalf("日志不能有端点或密码: %q", logs)
	}
}

// 密码长度按字符（rune）计，不按字节：7 个汉字 21 字节也不够 8 个字符。
func TestSetPasswordCountsRunes(t *testing.T) {
	h := newTestHub(t)
	if _, err := h.srv.SetPassword("密码密码密码密"); err == nil {
		t.Fatal("7 个字符应被拒")
	}
	if _, err := h.srv.SetPassword("密码密码密码密码"); err != nil {
		t.Fatalf("8 个字符应通过: %v", err)
	}
}

// 过期的会话在 HTTP 层同样 401。
func TestExpiredSessionHTTP(t *testing.T) {
	h := newTestHub(t)
	raw, err := h.st.CreateClientSession(-time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", h.http.URL+"/api/status", nil)
	req.AddCookie(&http.Cookie{Name: "railse_session", Value: raw})
	if code := do(t, req).StatusCode; code != http.StatusUnauthorized {
		t.Fatalf("过期会话应 401, 得到 %d", code)
	}
}

// clientIP：IPv4-mapped 归一为 IPv4，IPv6 归一到 /64；只有对端在可信前缀内才看 X-Forwarded-For，
// 从右往左取第一个不可信的地址。
func TestClientIP(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	cases := []struct {
		name    string
		remote  string
		xff     []string
		trusted []netip.Prefix
		want    string
	}{
		{"IPv4", "192.0.2.1:1234", nil, nil, "192.0.2.1"},
		{"IPv4-mapped", "[::ffff:192.0.2.1]:1234", nil, nil, "192.0.2.1"},
		{"IPv6 归一到 /64", "[2001:db8:1:2:3:4:5:6]:1", nil, nil, "2001:db8:1:2::/64"},
		{"IPv6 带 zone", "[fe80::1%en0]:1", nil, nil, "fe80::/64"},
		{"无法解析的对端原样", "pipe", nil, nil, "pipe"},
		{"未配置可信代理时忽略 XFF", "192.0.2.1:1", []string{"203.0.113.5"}, nil, "192.0.2.1"},
		{"不可信对端伪造 XFF", "192.0.2.1:1", []string{"203.0.113.5"}, trusted, "192.0.2.1"},
		{"可信对端取 XFF", "10.0.0.2:5", []string{"203.0.113.5"}, trusted, "203.0.113.5"},
		{"跳过可信的中间代理", "10.0.0.2:5", []string{"198.51.100.1, 203.0.113.5, 10.0.0.3"}, trusted, "203.0.113.5"},
		{"多行 XFF 按顺序拼接", "10.0.0.2:5", []string{"198.51.100.1", "203.0.113.5"}, trusted, "203.0.113.5"},
		{"全是可信地址取最左", "10.0.0.2:5", []string{"10.0.0.7, 10.0.0.3"}, trusted, "10.0.0.7"},
		{"可信对端没有 XFF", "10.0.0.2:5", nil, trusted, "10.0.0.2"},
		{"最右是垃圾时停在最后一个可信跳", "10.0.0.2:5", []string{"203.0.113.5, garbage"}, trusted, "10.0.0.2"},
		{"XFF 里的 IPv6 带端口", "10.0.0.2:5", []string{"[2001:db8::1]:443"}, trusted, "2001:db8::/64"},
		{"XFF 里的 IPv4-mapped", "10.0.0.2:5", []string{"::ffff:203.0.113.5"}, trusted, "203.0.113.5"},
		{"IPv6 可信对端", "[fd00::1]:5", []string{"203.0.113.5"}, trusted, "203.0.113.5"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/login", nil)
			r.RemoteAddr = c.remote
			for _, v := range c.xff {
				r.Header.Add("X-Forwarded-For", v)
			}
			if got := clientIP(r, c.trusted); got != c.want {
				t.Fatalf("clientIP = %q，应为 %q", got, c.want)
			}
		})
	}
}

// 不可信对端换 X-Forwarded-For 换不出新的限速键；可信对端时按 XFF 里的客户端地址分别限速，
// 左侧伪造的地址不影响取到的客户端地址。
func TestLoginRateLimitBehindProxy(t *testing.T) {
	try := func(h *testHub, xff string) int {
		req, _ := http.NewRequest("POST", h.http.URL+"/api/login", strings.NewReader(`{"password":"x"}`))
		req.Header.Set("X-Forwarded-For", xff)
		return do(t, req).StatusCode
	}
	h := newTestHub(t) // 未设密码：放行的登录回 503，被限速的回 429，不必跑 argon2
	for i := 0; i < 5; i++ {
		try(h, fmt.Sprintf("203.0.113.%d", i))
	}
	if code := try(h, "203.0.113.99"); code != http.StatusTooManyRequests {
		t.Fatalf("不可信对端换 XFF 也应被限速, 得到 %d", code)
	}

	h = newTestHubWith(t, func(c *Config) {
		c.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	}, nil)
	for i := 0; i < 6; i++ {
		if code := try(h, fmt.Sprintf("203.0.113.%d", i)); code != http.StatusServiceUnavailable {
			t.Fatalf("可信代理后不同客户端各自计数，第 %d 个得到 %d", i+1, code)
		}
	}
	for i := 0; i < 5; i++ {
		try(h, fmt.Sprintf("198.51.100.%d, 192.0.2.9", i))
	}
	if code := try(h, "198.51.100.200, 192.0.2.9"); code != http.StatusTooManyRequests {
		t.Fatalf("同一客户端左侧伪造地址也应被限速, 得到 %d", code)
	}
}

// 不合法的机器名（非法 UTF-8、C0 控制字符、DEL）回 400，不消费接入码。其余被拒的字符见 TestValidName
// （接入接口每来源每分钟限 5 次，这里只发 5 个请求）。
func TestEnrollRejectsBadName(t *testing.T) {
	h := newTestHub(t)
	tok, err := h.st.CreateEnrollToken(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"C0 控制字符":  mustJSON(t, map[string]string{"token": tok, "name": "mac\x01"}),
		"制表符":      mustJSON(t, map[string]string{"token": tok, "name": "mac\tpro"}),
		"DEL":      mustJSON(t, map[string]string{"token": tok, "name": "mac\x7f"}),
		"非法 UTF-8": []byte(`{"token":"` + tok + `","name":"mac` + "\xff" + `"}`),
	}
	for name, body := range cases {
		if resp := postRaw(t, h.http.URL+"/api/enroll", body, nil); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s 应 400, 得到 %d", name, resp.StatusCode)
		}
	}
	if resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": tok, "name": "工作站 mac"}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("合法名字应接入成功且接入码未被消费, 得到 %d", resp.StatusCode)
	}
}

// ValidName（机器名与 webhook 标题共用）拒绝非法 UTF-8、C0、DEL、C1 控制字符和 unicode.Cf 格式字符（ZWNJ、ZWJ 除外），
// 其余可显示的字符（含中文、emoji 与 ZWJ 组合、带 ZWNJ 的波斯语、不换行空格）照常接受。
func TestValidName(t *testing.T) {
	bad := map[string]string{
		"C0": "mac\x01", "制表符": "mac\tpro", "换行": "a\nb", "DEL": "mac\x7f", "C1 NEL": "a\u0085b", "C1 CSI": "mac\u009b31m",
		"LRE": "a\u202ab", "RLO": "批准\u202egnp.exe", "PDF": "a\u202cb", "LRI": "a\u2066b", "PDI": "a\u2069b",
		"零宽空格": "ma\u200bc", "LRM": "a\u200eb", "RLM": "a\u200fb", "BOM": "\ufeffmac",
		"ALM": "a\u061cb", "WJ": "a\u2060b", "不可见加号": "a\u2064b", "软连字符": "pay\u00adpal", "蒙古文元音分隔": "a\u180eb",
		"已废弃格式字符": "a\u206ab", "emoji 标签": "a\U000e0067b",
		"非法 UTF-8": "mac\xff",
	}
	for name, s := range bad {
		if ValidName(s) {
			t.Errorf("%s（%q）应拒绝", name, s)
		}
	}
	for _, s := range []string{"工作站 mac", "MacBook-Pro.local", "发布周报 🚀", "a\u00a0b", "a\u200ab", "a\u2065b",
		"👨\u200d👩\u200d👧", "می\u200cخواهم", "क्\u200dष"} {
		if !ValidName(s) {
			t.Errorf("%q 应接受", s)
		}
	}
}

// New 拒绝非正的时长与无效的可信代理前缀。
func TestNewRejectsBadConfig(t *testing.T) {
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
	mods := map[string]func(*Config){
		"SessionTTL":     func(c *Config) { c.SessionTTL = 0 },
		"PingInterval":   func(c *Config) { c.PingInterval = -time.Second },
		"PongTimeout":    func(c *Config) { c.PongTimeout = 0 },
		"ResendWindow":   func(c *Config) { c.ResendWindow = 0 },
		"TrustedProxies": func(c *Config) { c.TrustedProxies = []netip.Prefix{{}} },
	}
	for name, mod := range mods {
		cfg := DefaultConfig(dir)
		mod(&cfg)
		if _, err := New(cfg, st, log.New(io.Discard, "", 0)); err == nil {
			t.Errorf("%s 不合法时 New 应报错", name)
		}
	}
	if _, err := New(DefaultConfig(dir), st, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("默认配置应可用: %v", err)
	}
}

// syncBuf 是并发安全的日志缓冲。
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Run 监听并服务；取消 ctx 后很快返回 nil。在途请求经 BaseContext 看到取消，不必等关停超时。
func TestRunShutsDownOnCancel(t *testing.T) {
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
	var logs syncBuf
	srv, err := New(cfg, st, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	var waiting atomic.Bool
	srv.mux.HandleFunc("GET /test/wait", func(w http.ResponseWriter, r *http.Request) {
		waiting.Store(true)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	const prefix = "hub 监听 "
	waitFor(t, "Run 开始监听", func() bool { return strings.Contains(logs.String(), prefix) })
	line := logs.String()
	addr := strings.TrimSpace(strings.SplitN(line[strings.Index(line, prefix)+len(prefix):], "\n", 2)[0])
	req, _ := http.NewRequest("GET", "http://"+addr+"/api/status", nil)
	if code := do(t, req).StatusCode; code != http.StatusUnauthorized {
		t.Fatalf("Run 应在服务, /api/status = %d", code)
	}
	go func() {
		if resp, err := http.Get("http://" + addr + "/test/wait"); err == nil {
			resp.Body.Close()
		}
	}()
	waitFor(t, "在途请求进入处理器", waiting.Load)
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回 %v", err)
		}
		if d := time.Since(start); d >= 2*time.Second {
			t.Fatalf("Run 用了 %s 才返回，在途请求应经 BaseContext 看到取消", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 Run 未返回")
	}
}

func decodeBody(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
}

// limiter.undo 退还最近一次放行；没有记录的键什么也不做，退到空时删掉键。
func TestLimiterUndo(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if !l.allow("k", 2, time.Minute) {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if l.allow("k", 2, time.Minute) {
		t.Fatal("满额后应拒绝")
	}
	l.undo("k")
	if !l.allow("k", 2, time.Minute) {
		t.Fatal("退还一次后应再放行一次")
	}
	if l.allow("k", 2, time.Minute) {
		t.Fatal("只退还了一次")
	}
	l.undo("nobody")
	l.undo("k")
	l.undo("k")
	if _, ok := l.hits["k"]; ok {
		t.Fatal("退到空时应删掉键")
	}
	if _, ok := l.hits["nobody"]; ok {
		t.Fatal("没有记录的键不应新建")
	}
}

// 等不到 argon2 名额回 503 的登录不占限速名额；真正校验过的登录照常计数。
func TestLoginBusyDoesNotCountTowardRateLimit(t *testing.T) {
	var inFlight atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	h := newTestHubWith(t, nil, func(s *Server) {
		s.verifyWait = 50 * time.Millisecond
		s.verify = func(enc, pw string) bool {
			inFlight.Add(1)
			defer inFlight.Add(-1)
			<-release
			return VerifyPassword(enc, pw)
		}
	})
	t.Cleanup(unblock) // 先于 hub 的清理运行，测试中途失败也不会卡在被阻塞的校验上
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		go func() { codes <- loginCode(h.http.URL, "hunter22") }()
	}
	waitFor(t, "两个登录占满名额", func() bool { return inFlight.Load() == 2 })
	// 已计 2 次；若 503 也计数，第 4 次（累计第 6 次）会是 429
	for i := 0; i < 4; i++ {
		if code := loginCode(h.http.URL, "hunter22"); code != http.StatusServiceUnavailable {
			t.Fatalf("名额占满时第 %d 次应 503, 得到 %d", i+1, code)
		}
	}
	unblock()
	for i := 0; i < 2; i++ {
		if code := <-codes; code != http.StatusNoContent {
			t.Fatalf("被放行的登录应 204, 得到 %d", code)
		}
	}
	for i := 0; i < 3; i++ {
		if code := loginCode(h.http.URL, "wrong-pass"); code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次错密码应 401, 得到 %d", i+1, code)
		}
	}
	if code := loginCode(h.http.URL, "wrong-pass"); code != http.StatusTooManyRequests {
		t.Fatalf("真正校验的第 6 次应 429, 得到 %d", code)
	}
}

// decodeJSON 返回后不留读截止：否则截止一到，net/http 的后台读超时会取消 r.Context()，
// 之后还要干活的处理器被无故打断。有请求体时 net/http 读到 EOF 开后台读会自己清掉截止；
// 空请求体时后台读在处理器开始前就已开着，只能由 decodeJSON 清。两种都测，处理器忽略解码错误继续干活。
func TestDecodeJSONClearsReadDeadline(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 100 * time.Millisecond
	t.Cleanup(func() { bodyReadTimeout = old }) // 在 hub 关停之后才恢复
	h := newTestHub(t)
	h.srv.mux.HandleFunc("POST /test/after-body", func(w http.ResponseWriter, r *http.Request) {
		var v map[string]string
		_ = decodeJSON(w, r, 1<<10, &v)
		select {
		case <-r.Context().Done():
			writeErr(w, http.StatusInternalServerError, "decodeJSON 之后 context 被取消")
		case <-time.After(3 * bodyReadTimeout):
			w.WriteHeader(http.StatusNoContent)
		}
	})
	for name, body := range map[string]string{"有请求体": `{"a":"b"}`, "空请求体": ""} {
		resp, err := http.Post(h.http.URL+"/test/after-body", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("%s: 请求失败（连接被断开？）: %v", name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("%s: decodeJSON 之后 context 不应因读截止被取消, 得到 %d", name, resp.StatusCode)
		}
	}
}
