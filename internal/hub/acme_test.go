package hub

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/acme/autocert"

	"github.com/railse/railse-opc/internal/hub/store"
)

// 这些测试绝不连 Let's Encrypt：证书用现场生成的自签证书注入（tlsConfigFn），真实的 autocert 只对不在白名单的域名调用
// （HostPolicy 在任何网络请求之前拒绝）。万一走到签发，acme 客户端也指向本机一个没人监听的端口，立即失败。
const deadACMEDirectory = "http://127.0.0.1:1/directory"

// newACMETestHub 起一个启用 ACME（域名 hub.example.com）的测试 hub，mod 在此之后再改配置。
func newACMETestHub(t *testing.T, mod func(*Config), srvMod func(*Server)) *testHub {
	t.Helper()
	return newTestHubWith(t, func(c *Config) {
		c.ACMEDomain = "hub.example.com"
		if mod != nil {
			mod(c)
		}
	}, func(s *Server) {
		if m := s.acmeManager(); m != nil {
			m.Client = &acme.Client{DirectoryURL: deadACMEDirectory}
		}
		if srvMod != nil {
			srvMod(s)
		}
	})
}

// newHubForConfig 用给定配置调 New（不起 HTTP 服务），返回 New 的错误；成功时登记收尾。
func newHubForConfig(t *testing.T, cfg Config) (*Server, error) {
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
	srv, err := New(cfg, st, log.New(io.Discard, "", 0))
	t.Cleanup(func() {
		if srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
			srv.stopBackground(ctx)
			cancel()
		}
		st.Close()
	})
	return srv, err
}

func tempDirT(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// selfSignedCert 现场生成 hub.example.com 与 127.0.0.1 的自签证书（只在测试里用，不入库任何私钥）。
func selfSignedCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "railse test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true,
		DNSNames: []string{"hub.example.com"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

// injectCert 让 serveTLS 用自签证书（NextProtos 与生产一样给 h2、http/1.1），不经 autocert。
func injectCert(t *testing.T) (func(*Server), *x509.CertPool) {
	cert, pool := selfSignedCert(t)
	return func(s *Server) {
		s.tlsConfigFn = func() *tls.Config {
			return &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12}
		}
	}, pool
}

func freeLocalAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestACMEConfigDefaultsAndValidation(t *testing.T) {
	h := newACMETestHub(t, nil, nil)
	if c := h.srv.cfg; c.PublicURL != "https://hub.example.com" || !c.SecureCookies || c.ACMECacheDir != filepath.Join(h.dir, "acme") || c.ACMEHTTPListen != ":80" {
		t.Fatalf("默认值: %+v", c)
	}
	m := h.srv.acmeManager()
	if m == nil || m.Email != "" || m.HostPolicy == nil || m.Cache == nil || m.Prompt == nil {
		t.Fatal("acmeManager")
	}
	if m != h.srv.acmeManager() {
		t.Fatal("acmeManager 应是同一个（80 端口的挑战处理与 TLS 共用一个 Manager）")
	}
	if m.Cache != autocert.DirCache(h.srv.cfg.ACMECacheDir) {
		t.Fatalf("Cache 应是证书目录: %#v", m.Cache)
	}
	if err := m.HostPolicy(context.Background(), "hub.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := m.HostPolicy(context.Background(), "evil.example.com"); err == nil {
		t.Fatal("别的域名应被拒")
	}
	tc := h.srv.tlsConfig()
	if tc.MinVersion != tls.VersionTLS12 || tc.GetCertificate == nil || !containsStr(tc.NextProtos, "acme-tls/1") || !containsStr(tc.NextProtos, "h2") {
		t.Fatalf("tlsConfig: %+v", tc)
	}

	// 不启用 ACME 时不动 SecureCookies、PublicURL，也没有 Manager 与直出 TLS 的配置
	plain, err := newHubForConfig(t, DefaultConfig(tempDirT(t)))
	if err != nil || plain.acmeManager() != nil || plain.cfg.SecureCookies || plain.cfg.PublicURL != "" || plain.cfg.ACMECacheDir != "" {
		t.Fatalf("没启用 ACME: %v %+v", err, plain.cfg)
	}
	if plain.tlsConfigFn != nil {
		t.Fatal("没启用 ACME 时不应有 tlsConfigFn")
	}

	// 显式的 PublicURL（主机名与域名一致，端口可不同）保留；邮箱、挑战端口照给的
	cfg := DefaultConfig(tempDirT(t))
	cfg.ACMEDomain, cfg.PublicURL, cfg.ACMEEmail, cfg.ACMEHTTPListen, cfg.ACMECacheDir = "hub.example.com", "https://hub.example.com:8443", "me@example.com", "none", filepath.Join(cfg.Dir, "certs")
	s, err := newHubForConfig(t, cfg)
	if err != nil || s.cfg.PublicURL != "https://hub.example.com:8443" || s.cfg.ACMEHTTPListen != "none" || s.cfg.ACMECacheDir != filepath.Join(cfg.Dir, "certs") || s.acmeManager().Email != "me@example.com" {
		t.Fatalf("显式配置: %v %+v", err, cfg)
	}

	for _, bad := range []string{"https://hub.example.com", "hub.example.com:443", "Hub.Example.com", "localhost", "hub.example.com/x", "-bad.example.com",
		"hub.example.com.", "1.2.3.4", "hub..example.com", "hub_x.example.com", " hub.example.com", strings.Repeat("a", 64) + ".example.com",
		// 正则放得过、autocert 的 HostWhitelist（IDNA 规则）却永远不认的：第 3、4 位是 --，或不合法的 punycode
		"ab--cd.example.com", "xn--zz.example.com"} {
		cfg := DefaultConfig(tempDirT(t))
		cfg.ACMEDomain = bad
		if _, err := newHubForConfig(t, cfg); err == nil || !strings.Contains(err.Error(), "acme-domain") {
			t.Errorf("%q 应被拒: %v", bad, err)
		}
		if err := CheckACMEDomain(bad); err == nil {
			t.Errorf("CheckACMEDomain(%q) 应报错", bad)
		}
	}
	if err := CheckACMEDomain("a-1.b2.example.com"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		mod  func(*Config)
		want string
	}{
		{func(c *Config) { c.PublicURL = "https://other.example.com" }, "public-url"},
		{func(c *Config) { c.PublicURL = "https://www.hub.example.com" }, "public-url"},
		{func(c *Config) { c.ACMEHTTPListen = "80" }, "acme-http"},
		{func(c *Config) { c.ACMEEmail = "not-an-email" }, "acme-email"},
		{func(c *Config) { c.ACMEEmail = "Me <me@example.com>" }, "acme-email"},
	} {
		cfg := DefaultConfig(tempDirT(t))
		cfg.ACMEDomain = "hub.example.com"
		c.mod(&cfg)
		if _, err := newHubForConfig(t, cfg); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v 应被拒并点名 %s: %v", cfg, c.want, err)
		}
		if _, err := cfg.Effective(); err == nil {
			t.Errorf("Effective 也应拒绝 %+v", cfg)
		}
	}
}

// Effective 不碰磁盘：命令行在建数据目录之前用它，被拒时不留下目录。
func TestConfigEffectiveTouchesNoDisk(t *testing.T) {
	dir := filepath.Join(tempDirT(t), "hub")
	cfg := DefaultConfig(dir)
	cfg.ACMEDomain = "hub.example.com"
	got, err := cfg.Effective()
	if err != nil || got.ACMECacheDir != filepath.Join(dir, "acme") || got.PublicURL != "https://hub.example.com" || !got.SecureCookies || got.ACMEHTTPListen != ":80" {
		t.Fatalf("Effective: %v %+v", err, got)
	}
	if cfg.PublicURL != "" || cfg.SecureCookies {
		t.Fatal("Effective 不应改调用方的值")
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("Effective 不应建目录")
	}
}

// 白名单之外的 SNI：autocert 在任何网络请求之前由 HostPolicy 拒绝，证书目录里什么也不写（没生成账户密钥）。
func TestACMEGetCertificateRejectsOtherHosts(t *testing.T) {
	h := newACMETestHub(t, nil, nil)
	m := h.srv.acmeManager()
	for _, name := range []string{"evil.example.com", "www.hub.example.com", "example.com"} {
		if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: name}); err == nil || !strings.Contains(err.Error(), "not configured") {
			t.Fatalf("%s 应被 HostPolicy 拒绝: %v", name, err)
		}
	}
	if _, err := m.GetCertificate(&tls.ClientHelloInfo{}); err == nil {
		t.Fatal("没有 SNI（按 IP 访问）应被拒")
	}
	if ents, _ := os.ReadDir(h.srv.cfg.ACMECacheDir); len(ents) != 0 {
		t.Fatalf("被拒的 SNI 不应写证书目录: %v", ents)
	}
}

// 生产的 TLS 配置（autocert 的 GetCertificate）经真实握手：别的 SNI、没有 SNI 都握手失败，不签发、不写证书目录。
func TestServeTLSRealConfigRejectsOtherSNI(t *testing.T) {
	h := newACMETestHub(t, func(c *Config) { c.ACMEHTTPListen = "none" }, nil)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.srv.serveTLS(ctx, ln) }()
	for _, sni := range []string{"evil.example.com", ""} {
		d := &net.Dialer{Timeout: 5 * time.Second}
		conn, err := tls.DialWithDialer(d, "tcp", ln.Addr().String(), &tls.Config{ServerName: sni, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // 只看握手是否被拒
		if err == nil {
			conn.Close()
			t.Fatalf("SNI %q 应握手失败", sni)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(h.srv.cfg.ACMECacheDir)
	if err != nil || len(ents) != 0 {
		t.Fatalf("证书目录应已建好且为空: %v %v", err, ents)
	}
}

// 80 端口的处理器：GET、HEAD 302 到对外的源（不反射请求的 Host），别的方法 400；挑战路径只认配置的域名，
// 没有令牌时 404（只查内存与证书目录，不联网）。
func TestACMERedirectHandler(t *testing.T) {
	h := newACMETestHub(t, nil, nil)
	srv := httptest.NewServer(h.srv.acmeHTTPHandler())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second}
	get := func(method, path, host string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Host = host
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	for _, host := range []string{"hub.example.com", "hub.example.com:80", "evil.example.com"} {
		resp := get("GET", "/r/abc?x=1", host)
		if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://hub.example.com/r/abc?x=1" {
			t.Fatalf("Host %s 跳转: %d %s", host, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if resp := get("HEAD", "/", "hub.example.com"); resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://hub.example.com/" {
		t.Fatalf("HEAD 跳转: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp := get("POST", "/api/login", "hub.example.com"); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST 应 400: %d", resp.StatusCode)
	}
	if resp := get("GET", "/.well-known/acme-challenge/tok", "evil.example.com"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("别的域名的挑战应 403: %d", resp.StatusCode)
	}
	if resp := get("GET", "/.well-known/acme-challenge/tok", "hub.example.com"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("没有令牌应 404: %d", resp.StatusCode)
	}

	// 对外的源带端口（--listen 不是 443）时跳到那个端口
	h2 := newACMETestHub(t, func(c *Config) { c.PublicURL = "https://hub.example.com:8443" }, nil)
	srv2 := httptest.NewServer(h2.srv.acmeHTTPHandler())
	defer srv2.Close()
	req, _ := http.NewRequest("GET", srv2.URL+"/x", nil)
	req.Host = "hub.example.com"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.Header.Get("Location") != "https://hub.example.com:8443/x" {
		t.Fatalf("带端口的源: %s", resp.Header.Get("Location"))
	}
}

func TestServeTLSWithInjectedCert(t *testing.T) {
	inject, pool := injectCert(t)
	h := newACMETestHub(t, func(c *Config) { c.ACMEHTTPListen = "none" }, inject)
	if _, err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	// 已有的证书目录（权限过宽）由 serveTLS 收紧
	if err := os.MkdirAll(h.srv.cfg.ACMECacheDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(h.srv.cfg.ACMECacheDir, 0o755)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.srv.serveTLS(ctx, ln) }()
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "hub.example.com", MinVersion: tls.VersionTLS12}, ForceAttemptHTTP2: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}
	base := "https://" + ln.Addr().String()
	waitFor(t, "TLS 服务起来", func() bool {
		resp, err := client.Get(base + "/api/version")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	resp, err := client.Post(base+"/api/login", "application/json", strings.NewReader(`{"password":"hunter22"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login over TLS: %v %v", err, resp)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 2 || resp.TLS == nil || resp.TLS.Version < tls.VersionTLS12 {
		t.Fatalf("应经 TLS 1.2+ 走 HTTP/2: %s %+v", resp.Proto, resp.TLS)
	}
	var session *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "railse_session" {
			session = c
		}
	}
	if session == nil || !session.Secure {
		t.Fatal("经 TLS 的 cookie 应带 Secure")
	}
	// WebSocket（网页的 /ws/client）在直出 TLS 时照常可用：客户端为升级请求另开 HTTP/1.1 连接
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	conn, _, err := websocket.Dial(wctx, "wss://"+ln.Addr().String()+"/ws/client", &websocket.DialOptions{HTTPClient: &http.Client{Transport: tr}, HTTPHeader: http.Header{"Cookie": {session.Name + "=" + session.Value}}})
	if err != nil {
		t.Fatalf("wss /ws/client: %v", err)
	}
	defer conn.CloseNow()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("没有停下")
	}
	if st, err := os.Stat(h.srv.cfg.ACMECacheDir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("证书目录应 0700: %v %v", st, err)
	}
	if h.srv.ws.count() != 0 {
		t.Fatal("WebSocket 处理应已退出")
	}
}

// 80 端口随 serveTLS 起停：明文 http 跳到 https；serveTLS 返回时 80 端口已关。
func TestServeTLSHTTPListener(t *testing.T) {
	inject, _ := injectCert(t)
	httpAddr := freeLocalAddr(t)
	h := newACMETestHub(t, func(c *Config) { c.ACMEHTTPListen = httpAddr }, inject)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.srv.serveTLS(ctx, ln) }()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 5 * time.Second}
	var loc string
	waitFor(t, "80 端口起来", func() bool {
		req, _ := http.NewRequest("GET", "http://"+httpAddr+"/r/abc?x=1", nil)
		req.Host = "hub.example.com"
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		resp.Body.Close()
		loc = resp.Header.Get("Location")
		return resp.StatusCode == http.StatusFound
	})
	if loc != "https://hub.example.com/r/abc?x=1" {
		t.Fatalf("跳转: %s", loc)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("没有停下")
	}
	if c, err := net.DialTimeout("tcp", httpAddr, time.Second); err == nil {
		c.Close()
		t.Fatal("serveTLS 返回后 80 端口应已关闭")
	}
}

// Run 直出 TLS 时没能开始服务的每条路径（主端口被占、证书目录建不了、80 端口被占）都先停下推送工作协程，
// 返回 ErrListen（调用方照常关库），并放开已经占上的端口。
func TestRunTLSNotStartedStopsBackground(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	notDir := filepath.Join(tempDirT(t), "file")
	if err := os.WriteFile(notDir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		mod  func(c *Config, listen string)
	}{
		{"主端口被占", func(c *Config, _ string) { c.Listen = busy.Addr().String(); c.ACMEHTTPListen = "none" }},
		{"证书目录建不了", func(c *Config, listen string) {
			c.Listen = listen
			c.ACMEHTTPListen = "none"
			c.ACMECacheDir = filepath.Join(notDir, "acme")
		}},
		{"80 端口被占", func(c *Config, listen string) { c.Listen = listen; c.ACMEHTTPListen = busy.Addr().String() }},
	} {
		t.Run(c.name, func(t *testing.T) {
			listen := freeLocalAddr(t)
			cfg := DefaultConfig(tempDirT(t))
			cfg.ACMEDomain = "hub.example.com"
			c.mod(&cfg, listen)
			srv, err := newHubForConfig(t, cfg)
			if err != nil {
				t.Fatal(err)
			}
			srv.acmeManager().Client = &acme.Client{DirectoryURL: deadACMEDirectory}
			if !srv.push.enabled {
				t.Fatal("ACME 默认的对外源应启用推送")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err = srv.Run(ctx)
			if !errors.Is(err, ErrListen) || errors.Is(err, ErrShutdownTimeout) {
				t.Fatalf("应返回 ErrListen: %v", err)
			}
			select {
			case <-srv.push.done:
			default:
				t.Fatal("ErrListen 时推送工作协程应已退出")
			}
			if cfg.Listen == listen {
				l, err := net.Listen("tcp", listen)
				if err != nil {
					t.Fatalf("主端口应已放开: %v", err)
				}
				l.Close()
			}
		})
	}
}

// 证书与账户私钥经 autocert.DirCache 写进证书目录：目录 0700，文件 0600（临时文件加 rename）。
func TestACMECacheFilesPrivate(t *testing.T) {
	h := newACMETestHub(t, func(c *Config) { c.ACMECacheDir = filepath.Join(c.Dir, "fresh", "acme") }, nil)
	if err := h.srv.acmeManager().Cache.Put(context.Background(), "probe+key", []byte("not a real key")); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(h.srv.cfg.ACMECacheDir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("证书目录应 0700: %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Join(h.srv.cfg.ACMECacheDir, "probe+key")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("证书文件应 0600: %v %v", st, err)
	}
	ents, _ := os.ReadDir(h.srv.cfg.ACMECacheDir)
	if len(ents) != 1 {
		t.Fatalf("不应留下临时文件: %v", ents)
	}
}

// 证书目录已存在（如旧版建的、手工拷来的）：目录收紧为 0700，里面已有的普通文件收紧为 0600；符号链接不跟随，指向的文件不动。
func TestEnsurePrivateDirTightensExistingFiles(t *testing.T) {
	base := tempDirT(t)
	dir := filepath.Join(base, "acme")
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o755)
	key := filepath.Join(dir, "acme_account+key")
	cert := filepath.Join(dir, "hub.example.com")
	outside := filepath.Join(base, "outside")
	for _, p := range []string{key, cert, outside} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{dir: 0o700, key: 0o600, cert: 0o600, outside: 0o644, filepath.Join(dir, "sub"): 0o755} {
		if st, err := os.Stat(p); err != nil || st.Mode().Perm() != want {
			t.Errorf("%s 权限应为 %04o: %v %v", p, want, st, err)
		}
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
