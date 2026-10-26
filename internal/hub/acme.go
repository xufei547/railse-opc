package hub

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// newACMEManager 组 autocert：只给配置的域名签（HostPolicy 在任何网络请求之前拒绝别的 SNI 与 Host），
// 用 Let's Encrypt 生产目录（Client 为空即 autocert 的默认），账户与证书私钥存证书目录（DirCache：目录 0700，文件 0600）。
func newACMEManager(cfg Config) *autocert.Manager {
	return &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(cfg.ACMECacheDir),
		HostPolicy: autocert.HostWhitelist(cfg.ACMEDomain),
		Email:      cfg.ACMEEmail,
	}
}

// acmeManager 返回 New 里建的那一个 Manager（没启用 ACME 时为 nil）。
func (s *Server) acmeManager() *autocert.Manager { return s.acme }

// defaultTLSConfig 是 autocert 的 TLS 配置（GetCertificate 按需签发与续期；NextProtos 含 h2、http/1.1 与 TLS-ALPN-01 挑战的
// acme-tls/1）加最低 TLS 1.2。
func (s *Server) defaultTLSConfig() *tls.Config {
	tc := s.acme.TLSConfig()
	tc.MinVersion = tls.VersionTLS12
	return tc
}

func (s *Server) tlsConfig() *tls.Config { return s.tlsConfigFn() }

// acmeHTTPHandler 是 80 端口的处理器：/.well-known/acme-challenge/ 交给 autocert 答 HTTP-01 挑战（只认配置的域名），
// 其余 GET、HEAD 302 跳到对外的源（PublicURL）加原路径与查询，别的方法回 400。跳转目标用配置的源而不用请求的 Host：
// --listen 不是 443（PublicURL 带端口）时也跳得对，也不把任意 Host 反射进 Location。
func (s *Server) acmeHTTPHandler() http.Handler {
	return s.acme.HTTPHandler(http.HandlerFunc(s.redirectToPublicURL))
}

func (s *Server) redirectToPublicURL(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "请用 HTTPS", http.StatusBadRequest)
		return
	}
	// RequestURI 对 "*" 之类的请求目标不以 / 开头：拼到源后面会变成另一个主机名，这类一律跳到根
	p := r.URL.RequestURI()
	if !strings.HasPrefix(p, "/") {
		p = "/"
	}
	http.Redirect(w, r, s.cfg.PublicURL+p, http.StatusFound)
}

// serveTLS 用 ln 直出 TLS（Run 在 ACMEDomain 非空时调它；测试传入自己的监听器，并用 tlsConfigFn 注入自签证书）：
// 先建证书目录（0700，已有的也收紧），ACMEHTTPListen 不是 none 时监听 80 端口（挑战与跳转），再在 ln 上走同一个 serve。
// 开始服务之前的每一步失败都关掉 ln、停下后台工作（notStarted），返回 ErrListen。80 端口随 serveTLS 一起关停，返回前已关。
func (s *Server) serveTLS(parent context.Context, ln net.Listener) error {
	if err := ensurePrivateDir(s.cfg.ACMECacheDir); err != nil {
		ln.Close()
		return s.notStarted(fmt.Errorf("建 ACME 证书目录: %w", err))
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	waitHTTP := func() {}
	if s.cfg.ACMEHTTPListen != acmeHTTPNone {
		hln, err := net.Listen("tcp", s.cfg.ACMEHTTPListen)
		if err != nil {
			ln.Close()
			return s.notStarted(fmt.Errorf("ACME 挑战与跳转端口: %w", err))
		}
		waitHTTP = s.serveACMEHTTP(ctx, hln)
		s.log.Printf("ACME 挑战（HTTP-01）与 http→https 跳转监听 %s", hln.Addr())
	} else {
		s.log.Printf("不监听 80 端口（acme-http none）：只能用 443 上的 TLS-ALPN-01 签发证书，也没有 http→https 跳转")
	}
	err := s.serve(ctx, tls.NewListener(ln, s.tlsConfig()))
	// serve 自己出错结束时 parent 没被取消：先取消，80 端口随之关停，再等它退出
	cancel()
	waitHTTP()
	return err
}

// serveACMEHTTP 在 ln 上起 80 端口的服务（acmeHTTPHandler），ctx 取消时关停（至多等 shutdownTimeout，到时强制断开）；
// 返回的函数等它完全退出。它只读写证书目录，不碰数据库。中途出错结束（监听器坏了）只记日志，hub 照常服务。
func (s *Server) serveACMEHTTP(ctx context.Context, ln net.Listener) (wait func()) {
	hs := &http.Server{Handler: s.acmeHTTPHandler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		IdleTimeout: idleTimeout, BaseContext: func(net.Listener) context.Context { return ctx }}
	served := make(chan struct{})
	go func() {
		defer close(served)
		if err := hs.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			s.log.Printf("ACME 挑战与跳转端口 %s 停止服务: %v（证书的 HTTP-01 签发与 http 跳转不可用，hub 本身照常）", ln.Addr(), err)
		}
	}()
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := hs.Shutdown(shutdownCtx); err != nil {
			hs.Close()
		}
		<-served
	}()
	return func() { <-stopped }
}

// ensurePrivateDir 建目录并收紧为 0700（已有的目录也收紧），目录里已有的普通文件（账户与证书私钥）收紧为 0600：
// 旧版建的、手工拷来的证书目录也合 doctor 的 hubsrv.acme。只看 Lstat 是普通文件的，符号链接与子目录不动、不跟随
// （目录已先收紧为 0700，别人换不了里面的条目）。
func ensurePrivateDir(d string) error {
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(d, 0o700); err != nil {
		return err
	}
	ents, err := os.ReadDir(d)
	if err != nil {
		return err
	}
	for _, e := range ents {
		p := filepath.Join(d, e.Name())
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if st.Mode().IsRegular() && st.Mode().Perm() != 0o600 {
			if err := os.Chmod(p, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}
