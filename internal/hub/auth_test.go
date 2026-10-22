package hub

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/hub/store"
)

type testHub struct {
	srv  *Server
	st   *store.Store
	http *httptest.Server
	dir  string
}

func newTestHub(t *testing.T) *testHub { return newTestHubWith(t, nil) }

// newTestHubWith 用默认配置起一个 hub，mod 非空时先改配置。
func newTestHubWith(t *testing.T, mod func(*Config)) *testHub {
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
	srv, err := New(cfg, st, log.New(os.Stderr, "hub ", 0))
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hs.Close(); st.Close() })
	return &testHub{srv: srv, st: st, http: hs, dir: dir}
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
	if err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	enc, ok, err := h.st.GetSetting("password_hash")
	if err != nil || !ok || !strings.HasPrefix(enc, "argon2id$v=19$m=65536,t=1,p=4$") || strings.Contains(enc, "hunter22") || !VerifyPassword(enc, "hunter22") {
		t.Fatalf("password_hash = %q, %v, %v", enc, ok, err)
	}
	if err := h.srv.SetPassword("short"); err == nil {
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
	if err := h.srv.SetPassword("hunter22"); err != nil {
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
	if err := h.srv.SetPassword("hunter22"); err != nil {
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
	h := newTestHubWith(t, func(c *Config) { c.SecureCookies = true })
	if err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	if c := login(t, h, "hunter22"); !c.Secure {
		t.Fatalf("SecureCookies 时应带 Secure: %+v", c)
	}

	h = newTestHub(t)
	if err := h.srv.SetPassword("hunter22"); err != nil {
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
	if err := h.srv.SetPassword("hunter22"); err != nil {
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
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll = %d", resp.StatusCode)
	}
	var out struct {
		MachineID string `json:"machine_id"`
		Secret    string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.MachineID == "" || out.Secret == "" {
		t.Fatalf("enroll 返回 %+v, %v", out, err)
	}
	req, _ := http.NewRequest("GET", "/ws/agent", nil)
	req.Header.Set("Authorization", "Bearer "+out.Secret)
	if m, err := h.srv.authMachine(req); err != nil || m.ID != out.MachineID || m.HomeDir != "/Users/u" {
		t.Fatalf("authMachine = %+v, %v", m, err)
	}
	for _, v := range []string{"Bearer nope", "", "Bearer ", "Basic " + out.Secret, out.Secret} {
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

// 接入审计的 detail 是合法 JSON，名字里的引号、控制字符、非 ASCII 原样往返。
func TestEnrollAuditDetail(t *testing.T) {
	h := newTestHub(t)
	tok, err := h.st.CreateEnrollToken(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	name := "mac \"pro\" \x01\x7f 工作站  "
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
