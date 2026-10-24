package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func built() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte("<!doctype html><title>Railse</title><script type=module src=/assets/app.abc123.js></script>")},
		"assets/app.abc123.js":  {Data: []byte("console.log(1)")},
		"assets/app.abc123.css": {Data: []byte("body{}")},
		"sw.js":                 {Data: []byte("self.addEventListener('push',()=>{})")},
		"manifest.webmanifest":  {Data: []byte(`{"name":"Railse"}`)},
		"icons/icon-192.png":    {Data: []byte("\x89PNG")},
		".gitkeep":              {Data: nil},
	}
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

// checkCommonHeaders 断言所有响应都带的三个安全头。
func checkCommonHeaders(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	h := rec.Header()
	if h.Get("X-Content-Type-Options") != "nosniff" || h.Get("Referrer-Policy") != "same-origin" || h.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("%s 缺安全头: %v", what, h)
	}
}

func TestServesFilesAndFallsBackToIndex(t *testing.T) {
	h := Handler(built())
	for _, path := range []string{"/", "/r/abc", "/rules", "/assets/missing.js", "/index.html", "/assets/", "/icons", "/.gitkeep", "/assets/../index.html"} {
		rec := get(t, h, "GET", path)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != 200 || !strings.Contains(string(body), "<title>Railse</title>") {
			t.Fatalf("%s: %d %q", path, rec.Code, body)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s 头: %v", path, rec.Header())
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") || rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatalf("%s 安全头: %v", path, rec.Header())
		}
		checkCommonHeaders(t, path, rec)
	}
	rec := get(t, h, "GET", "/assets/app.abc123.js")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("资源: %d %v", rec.Code, rec.Header())
	}
	if rec.Body.String() != "console.log(1)" || rec.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("资源内容或头: %q %v", rec.Body.String(), rec.Header())
	}
	rec = get(t, h, "GET", "/assets/app.abc123.css")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") {
		t.Fatalf("样式: %d %v", rec.Code, rec.Header())
	}
	rec = get(t, h, "GET", "/sw.js")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("sw.js: %d %v", rec.Code, rec.Header())
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("sw.js 类型: %v", rec.Header())
	}
	rec = get(t, h, "GET", "/manifest.webmanifest")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/manifest+json" || rec.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("manifest: %d %v", rec.Code, rec.Header())
	}
	rec = get(t, h, "HEAD", "/icons/icon-192.png")
	if rec.Code != 200 || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("HEAD: %d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("Cache-Control") != "no-cache" || rec.Body.Len() != 0 {
		t.Fatalf("HEAD 图标: %v body=%d", rec.Header(), rec.Body.Len())
	}
}

func TestAPIAndWSPathsNeverGetIndex(t *testing.T) {
	h := Handler(built())
	for _, path := range []string{"/api/nope", "/ws/nope", "/api/", "/api", "/ws", "/api/../api/x"} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			rec := get(t, h, method, path)
			body, _ := io.ReadAll(rec.Body)
			if rec.Code != 404 || !strings.Contains(string(body), "没有这个接口") || rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("%s %s: %d %q %v", method, path, rec.Code, body, rec.Header())
			}
			checkCommonHeaders(t, path, rec)
		}
	}
	// 只是前缀相像的网页路径照常回 index.html
	if rec := get(t, h, "GET", "/apis"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>Railse</title>") {
		t.Fatalf("/apis: %d", rec.Code)
	}
	for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
		rec := get(t, h, method, "/")
		if rec.Code != 405 || rec.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("%s: %d %v", method, rec.Code, rec.Header())
		}
		checkCommonHeaders(t, method, rec)
	}
}

func TestNotBuilt(t *testing.T) {
	h := Handler(fstest.MapFS{".gitkeep": {Data: nil}})
	for _, path := range []string{"/", "/r/abc", "/.gitkeep", "/sw.js"} {
		rec := get(t, h, "GET", path)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != 503 || !strings.Contains(string(body), "make web") || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("未构建 %s: %d %q", path, rec.Code, body)
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("未构建的提示不该被缓存: %v", rec.Header())
		}
		checkCommonHeaders(t, path, rec)
	}
	// 没构建也不把接口路径说成“网页未构建”
	if rec := get(t, h, "GET", "/api/nope"); rec.Code != 404 {
		t.Fatalf("未构建时的接口路径: %d", rec.Code)
	}
}

// cspDirectives 把 CSP 拆成 指令 → 来源列表。
func cspDirectives(t *testing.T, csp string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	for _, part := range strings.Split(csp, ";") {
		f := strings.Fields(part)
		if len(f) == 0 {
			continue
		}
		if _, dup := out[f[0]]; dup {
			t.Fatalf("CSP 指令重复 %s: %q", f[0], csp)
		}
		out[f[0]] = f[1:]
	}
	return out
}

func TestCSP(t *testing.T) {
	h := Handler(built())
	req := httptest.NewRequest("GET", "/r/abc", nil)
	req.Host = "Hub.Example.com:8443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	csp := rec.Header().Get("Content-Security-Policy")
	d := cspDirectives(t, csp)
	want := map[string]string{
		"default-src":     "'self'",
		"script-src":      "'self'",
		"style-src":       "'self'",
		"img-src":         "'self'",
		"connect-src":     "'self' wss://hub.example.com:8443 ws://hub.example.com:8443",
		"manifest-src":    "'self'",
		"worker-src":      "'self'",
		"object-src":      "'none'",
		"base-uri":        "'none'",
		"form-action":     "'self'",
		"frame-ancestors": "'none'",
	}
	if len(d) != len(want) {
		t.Fatalf("CSP 指令: %q", csp)
	}
	for k, v := range want {
		if got := strings.Join(d[k], " "); got != v {
			t.Fatalf("%s = %q，应为 %q（%q）", k, got, v, csp)
		}
	}
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "*", "data:", "blob:"} {
		for k, srcs := range d {
			for _, s := range srcs {
				if strings.Contains(s, bad) {
					t.Fatalf("%s 不该有 %s: %q", k, bad, csp)
				}
			}
		}
	}

	// Host 不是“域名或 IPv4 加可选端口”时不往 CSP 里写它，connect-src 只留 'self'
	for _, host := range []string{"", "evil.com;script-src *", "a b", "[::1]:8787", "h:", "h:123456", "h:8a", "*.example.com", "h'x", "h,x"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		csp := rec.Header().Get("Content-Security-Policy")
		d := cspDirectives(t, csp)
		if got := strings.Join(d["connect-src"], " "); got != "'self'" || len(d) != len(want) {
			t.Fatalf("Host %q: %q", host, csp)
		}
	}
	for host, src := range map[string]string{"127.0.0.1:8787": "127.0.0.1:8787", "localhost": "localhost", "hub.example.com": "hub.example.com"} {
		req := httptest.NewRequest("GET", "/", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		d := cspDirectives(t, rec.Header().Get("Content-Security-Policy"))
		if got := strings.Join(d["connect-src"], " "); got != "'self' wss://"+src+" ws://"+src {
			t.Fatalf("Host %q: connect-src %q", host, got)
		}
	}
}

func TestEmbeddedFSHasDist(t *testing.T) {
	// 没构建时 dist 里只有 .gitkeep；构建后有 index.html。两种都能打开。
	if _, err := FS().Open("."); err != nil {
		t.Fatal(err)
	}
}
