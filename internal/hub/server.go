// Package hub 是跑在云服务器上的中心服务：存请求、规则、机器与审计，接守护进程的 WebSocket，
// 给手机网页和 webhook 来源提供 HTTP 接口。hub 只能对请求回 allow 或 deny，没有向机器下发命令的通道。
package hub

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/acme/autocert"

	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/webui"
	"github.com/xufei547/railse-opc/internal/hub/wire"
)

// ErrShutdownTimeout 表示 Run 关停时没在时限内等到全部在途请求、WebSocket 处理（/ws/agent、/ws/client）、webhook 回调或推送工作协程退出。
// Run 仍尽力收尾后才返回它；那些还在跑的可能继续碰存储，调用方不宜随即关库（进程即将退出时直接退出即可）。
var ErrShutdownTimeout = errors.New("hub 关停超时：仍有请求处理、WebSocket 处理、webhook 回调或推送没有退出")

// ErrListen 表示 Run 没能开始服务（端口被占、地址无效等；直出 TLS 时还有证书目录建不了、80 端口被占）：
// 什么都还没开始跑，New 里起的推送工作协程也已停下，调用方可以照常关库。
var ErrListen = errors.New("hub 监听失败")

// Config 是 hub 的配置。
type Config struct {
	Dir           string
	DBPath        string
	Listen        string
	SecureCookies bool
	SessionTTL    time.Duration
	PingInterval  time.Duration
	PongTimeout   time.Duration
	ResendWindow  time.Duration
	// TrustedProxies 是可信反向代理的地址前缀。只有 TCP 对端落在其中时才看 X-Forwarded-For；默认空，一律用对端地址。
	TrustedProxies []netip.Prefix
	// PublicURL 是 hub 对外的源（https://host[:port]，见 CheckPublicURL），也是 VAPID 的 sub。空则 Web Push 停用。
	PublicURL string
	// Version 是 hub 的版本号（cmd 注入 main.version），GET /api/version 回它；空时显示 dev。
	Version string
	// ACMEDomain 非空时 hub 自己向 Let's Encrypt 申请证书并直出 TLS（见 CheckACMEDomain）；PublicURL 默认 https://<域名>，
	// 给了的话主机名必须就是这个域名；cookie 一律 Secure。
	ACMEDomain string
	// ACMEEmail 是给 Let's Encrypt 的账户联系邮箱，只用于账户与政策通知（它已于 2025 年停发到期提醒邮件），可空。
	ACMEEmail string
	// ACMECacheDir 存 ACME 账户私钥与证书私钥（明文，设计 5.3 的例外），默认 Dir/acme，目录 0700、文件 0600。
	ACMECacheDir string
	// ACMEHTTPListen 是 HTTP-01 挑战与 http→https 跳转的监听地址，默认 :80；"none" 不监听（只靠 443 上的 TLS-ALPN-01 签发）。
	ACMEHTTPListen string
}

// acmeHTTPNone 是 ACMEHTTPListen 表示“不监听 80”的值。
const acmeHTTPNone = "none"

// Effective 填上默认值并校验，返回 New 实际使用的配置。ACMEDomain 非空时：PublicURL 为空则取 https://<域名>，
// SecureCookies 置真，ACMECacheDir 为空取 Dir/acme，ACMEHTTPListen 为空取 :80。不碰磁盘、不改调用方的值：
// 命令行在建数据目录之前用它报用法错。
func (c Config) Effective() (Config, error) {
	if c.ACMEDomain != "" {
		if c.PublicURL == "" {
			c.PublicURL = "https://" + c.ACMEDomain
		}
		c.SecureCookies = true
		if c.ACMECacheDir == "" {
			c.ACMECacheDir = filepath.Join(c.Dir, "acme")
		}
		if c.ACMEHTTPListen == "" {
			c.ACMEHTTPListen = ":80"
		}
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// validate 检查时长为正、可信代理前缀有效、PublicURL 为空或是 https 源；启用 ACME 时还查域名、邮箱、80 端口地址，
// 以及 PublicURL 的主机名就是这个域名（否则 cookie、同源检查与推送的源对不上证书）。
func (c Config) validate() error {
	for _, d := range []struct {
		name string
		v    time.Duration
	}{{"SessionTTL", c.SessionTTL}, {"PingInterval", c.PingInterval}, {"PongTimeout", c.PongTimeout}, {"ResendWindow", c.ResendWindow}} {
		if d.v <= 0 {
			return fmt.Errorf("配置 %s 必须为正，得到 %s", d.name, d.v)
		}
	}
	for _, p := range c.TrustedProxies {
		if !p.IsValid() {
			return fmt.Errorf("可信代理前缀无效: %q", p)
		}
	}
	// 先查域名：PublicURL 的默认值由它拼成，域名坏了报的应是域名
	if c.ACMEDomain != "" {
		if err := CheckACMEDomain(c.ACMEDomain); err != nil {
			return err
		}
		if c.ACMEHTTPListen != acmeHTTPNone {
			if _, _, err := net.SplitHostPort(c.ACMEHTTPListen); err != nil {
				return fmt.Errorf("acme-http 应是 host:port（如 :80）或 none，得到 %q", c.ACMEHTTPListen)
			}
		}
		if c.ACMEEmail != "" {
			if a, err := mail.ParseAddress(c.ACMEEmail); err != nil || a.Name != "" || a.Address != c.ACMEEmail {
				return fmt.Errorf("acme-email 应是邮箱地址（如 me@example.com），得到 %q", c.ACMEEmail)
			}
		}
		if c.ACMECacheDir == "" {
			return errors.New("启用 ACME 时证书目录不能为空")
		}
	}
	if c.PublicURL != "" {
		if err := CheckPublicURL(c.PublicURL); err != nil {
			return err
		}
	}
	if c.ACMEDomain != "" {
		if u, err := url.Parse(c.PublicURL); err != nil || u.Hostname() != c.ACMEDomain {
			return fmt.Errorf("public-url 的主机名应与 acme-domain 一致（cookie、同源检查与推送的源要对得上证书），得到 %q 与 %q", c.PublicURL, c.ACMEDomain)
		}
	}
	return nil
}

// acmeDomainRe 是 ACMEDomain 的写法：小写字母、数字与中间的 -，至少两段，不带协议、端口、路径与结尾的点。
var acmeDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// CheckACMEDomain 检查直出 TLS 的域名：小写主机名（见 acmeDomainRe），不是 IP 地址，总长至多 253、每段至多 63 个字符。
// Config 的校验（New 与命令行在建目录之前调的 Effective）用它，正则只此一份。
func CheckACMEDomain(s string) error {
	ok := len(s) <= 253 && acmeDomainRe.MatchString(s)
	if ok {
		for _, label := range strings.Split(s, ".") {
			if len(label) > 63 {
				ok = false
			}
		}
		if _, err := netip.ParseAddr(s); err == nil {
			ok = false
		}
	}
	if !ok {
		return fmt.Errorf("acme-domain 必须是小写主机名（如 hub.example.com，不带协议、端口、路径，不能是 IP），得到 %q", s)
	}
	// 再让 autocert 自己的 HostWhitelist（按 IDNA 规则规整）认一次：ab--cd、xn--zz 这类过得了上面的检查，却永远签不到证书
	if autocert.HostWhitelist(s)(context.Background(), s) != nil {
		return fmt.Errorf("acme-domain %q 不合 IDNA 规则，autocert 不会给它签证书（标签的第 3、4 位不能是 --，xn-- 开头的要是合法的 punycode）", s)
	}
	return nil
}

// normPrefixes 规整可信代理前缀：去掉主机位，IPv4-mapped 的前缀换成对应的 IPv4 前缀（与 clientIP 归一后的地址对得上）。
func normPrefixes(ps []netip.Prefix) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ps))
	for _, p := range ps {
		if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
		}
		out = append(out, p.Masked())
	}
	return out
}

// DefaultConfig 给出目录下的默认配置。
func DefaultConfig(dir string) Config {
	return Config{
		Dir: dir, DBPath: filepath.Join(dir, "hub.db"), Listen: "127.0.0.1:8787",
		SessionTTL: 30 * 24 * time.Hour, PingInterval: 30 * time.Second, PongTimeout: 90 * time.Second, ResendWindow: 15 * time.Minute,
	}
}

// Server 组装存储、连接表与路由。
type Server struct {
	cfg     Config
	store   *store.Store
	log     *log.Logger
	mux     *http.ServeMux
	limiter *limiter
	agents  *agentHub
	clients *clientHub
	start   time.Time
	// verify 与 verifyWait 是登录校验密码的函数与等名额的上限，测试可替换。
	verify     func(encoded, pw string) bool
	verifyWait time.Duration
	// helloTimeout 是守护进程连上后发 hello 的期限，测试可缩短。
	helloTimeout time.Duration
	// decideMu 串行化用户决定（取请求、校验、学规则、改状态）；helloLocks 让同一机器的重连对齐串行执行。
	decideMu   sync.Mutex
	helloLocks machineLocks
	// rulesMu 串行化“读规则现状 → 改 → 记审计”（启停、导入），审计里的“从停用改为启用”与提升规则列表不被并发改动打乱；
	// pauseMu 让总开关的写库与下发按同一顺序进行，守护进程最后收到的就是库里的值。
	rulesMu sync.Mutex
	pauseMu sync.Mutex
	// beforeSettle 非空时在“记住”学完规则之后、改请求状态之前调用（测试用它制造请求先被别处结束的竞态）。
	beforeSettle func(store.Request)
	// ws 记在跑的 WebSocket handler（/ws/agent、/ws/client）：关停开始后不再接纳新的（trackWS 回 503）。
	ws tracker
	// reqs 记 Run 里在跑的全部 HTTP 处理（普通请求与 WebSocket handler，见 countRequests）：http.Server.Shutdown 不管已劫持的连接，
	// 超时后的 Close 也只关连接、不停下还在跑的处理，Run 靠它判断是否全部退出。开着却没有处理在跑的连接不算。
	reqs tracker
	// beforeWSExit 非空时在 WebSocket handler 退出前调用（测试用它让退出变慢）。
	beforeWSExit func()
	// clientRechecks 计 /ws/client 复核登录会话的次数（测试观测用）。
	clientRechecks atomic.Int64
	// cbCtx 是 webhook 回调的运行期：New 里建，stopCallbacks（Run 返回前）取消，重试等待与在途回调随之结束；
	// callbacks 记在跑的回调 goroutine，Run 返回前等它们退出。callbackRetryWait 是回调失败后重试前的等待，测试可缩短；
	// callbackClient 发回调（超时 10 秒、不跟随重定向）。afterCallbackPost 非空时在每次回调请求返回之后调用
	// （测试用它在拿到 2xx 之后取消回调运行期）。
	cbCtx             context.Context
	cbCancel          context.CancelFunc
	callbacks         tracker
	callbackRetryWait time.Duration
	callbackClient    *http.Client
	afterCallbackPost func()
	// push 是 Web Push：VAPID 密钥、发送客户端与推送队列。New 里建，推送启用时工作协程随之起，stopBackground 停。
	push *pushService
	// web 托管网页（webui）：s.mux 没有路由的路径都交给它，见 serveRoot。New 里建，测试可在开始服务前换成自己的构建产物。
	web http.Handler
	// acme 是直出 TLS 的证书管理（ACMEDomain 为空时为 nil）。New 里建一个，TLS 握手与 80 端口的挑战处理共用它
	// （HTTP-01 的令牌记在它的内存里，分成两个 Manager 就答不上挑战）。见 acme.go。
	acme *autocert.Manager
	// tlsConfigFn 给直出 TLS 的 tls.Config：启用 ACME 时是 defaultTLSConfig（autocert），没启用时为空；测试换成自签证书，不经 Let's Encrypt。
	tlsConfigFn func() *tls.Config
}

// tracker 计在跑的后台工作（WebSocket handler、webhook 回调）。shutdown 之后不再接纳新的，并给出全部退出时关闭的通道。
// 不用 sync.WaitGroup：关停超时后不再等，已开始但还没登记的工作可能在 Wait 时才 Add（计数为零时与 Wait 并发 Add 是误用）。
type tracker struct {
	mu      sync.Mutex
	n       int
	closing bool
	idle    chan struct{} // closing 之后 n 归零时关闭
}

// enter 登记一项工作；已在关停时返回 false，调用方不该再开始它。
func (t *tracker) enter() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closing {
		return false
	}
	t.n++
	return true
}

func (t *tracker) leave() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.n--
	if t.n == 0 && t.idle != nil {
		close(t.idle)
		t.idle = nil
	}
}

// shutdown 不再接纳新的工作，返回在跑的工作全部结束时关闭的通道。
func (t *tracker) shutdown() <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closing = true
	ch := make(chan struct{})
	if t.n == 0 {
		close(ch)
	} else {
		t.idle = ch
	}
	return ch
}

func (t *tracker) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.n
}

// trackWS 在 WebSocket handler 开头调用：关停中回 503 并返回 false；返回 true 时调用方须 defer s.untrackWS()。
func (s *Server) trackWS(w http.ResponseWriter) bool {
	if !s.ws.enter() {
		writeErr(w, http.StatusServiceUnavailable, "hub 正在关停")
		return false
	}
	return true
}

func (s *Server) untrackWS() {
	if s.beforeWSExit != nil {
		s.beforeWSExit()
	}
	s.ws.leave()
}

// New 组装 hub：校验配置，确保有内置规则，读或生成 VAPID 密钥（推送启用时起推送工作协程），注册路由。
// 起了后台工作（推送工作协程、之后的 webhook 回调）的 Server 由 Run 收尾；不调 Run 的调用方（测试）用 stopBackground。
func New(cfg Config, st *store.Store, logger *log.Logger) (*Server, error) {
	cfg, err := cfg.Effective()
	if err != nil {
		return nil, err
	}
	cfg.TrustedProxies = normPrefixes(cfg.TrustedProxies)
	if _, err := st.EnsureDefaultRules(); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: st, log: logger, mux: http.NewServeMux(), limiter: newLimiter(), agents: newAgentHub(logger), clients: newClientHub(), start: time.Now(),
		verify: VerifyPassword, verifyWait: loginVerifyWait, helloTimeout: defaultHelloTimeout,
		callbackRetryWait: defaultCallbackRetryWait, callbackClient: newCallbackClient()}
	if cfg.ACMEDomain != "" { // 只有启用 ACME 才直出 TLS（Run 只在这时走 serveTLS），tlsConfigFn 也只在这时给
		s.acme = newACMEManager(cfg)
		s.tlsConfigFn = s.defaultTLSConfig
	}
	ps, err := newPushService(s)
	if err != nil {
		return nil, err
	}
	s.push = ps
	s.cbCtx, s.cbCancel = context.WithCancel(context.Background())
	s.web = webui.Handler(webui.FS())
	s.routes()
	return s, nil
}

// routes 注册认证、接入与守护进程入口，再注册网页用的接口（apiRoutes）；后面的 Task 往里加。
// 网页（s.web）不挂在 s.mux 上，由根处理器 serveRoot 兜底，见那里。
func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", sameOrigin(s.handleLogout))
	s.mux.HandleFunc("POST /api/enroll", s.handleEnroll)
	s.mux.HandleFunc("GET /ws/agent", s.handleAgentWS)
	s.mux.HandleFunc("POST /api/webhook/approvals", s.handleWebhookIngest)
	s.mux.HandleFunc("GET /api/version", s.handleVersion)
	s.apiRoutes()
	s.pushRoutes()
}

// handleVersion 无需登录：doctor 与安装脚本用它确认 hub 可达、版本与守护进程协议号。回 {"version","agent_proto"}，
// 没注入版本（开发构建）时 version 是 dev。
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	v := s.cfg.Version
	if v == "" {
		v = "dev"
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v, "agent_proto": wire.Proto})
}

// Handler 返回根处理器（serveRoot）。
func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveRoot) }

// serveRoot 是根处理器：s.mux 对这个路径有路由（不论方法）就交给它，方法不对时由它回 405 并带 Allow；没有路由的交给网页：
// 网页路径回文件或 index.html，/api/、/ws/ 下的回 404 JSON。
// 不在 s.mux 上挂 "/"：挂了的话方法不对的接口请求（如 GET /api/requests/x/decide）会匹配到 "/"，405 变成 404。
func (s *Server) serveRoot(w http.ResponseWriter, r *http.Request) {
	if s.routed(r) {
		s.mux.ServeHTTP(w, r)
		return
	}
	s.web.ServeHTTP(w, r)
}

// routeProbeMethods 是 routed 判断“路径有路由、只是方法不对”时试的方法（GET 路由同时匹配 HEAD）。
var routeProbeMethods = []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}

// routed 报告 s.mux 对 r 的路径有路由：r 的方法有匹配（含 mux 会重定向到规范路径的写法），或换成别的方法有匹配（mux 会回 405）。
// 只查不处理：真正的处理仍走 s.mux.ServeHTTP，路径参数（r.PathValue）由它填。
func (s *Server) routed(r *http.Request) bool {
	if _, p := s.mux.Handler(r); p != "" {
		return true
	}
	probe := *r
	for _, m := range routeProbeMethods {
		if m == r.Method {
			continue
		}
		probe.Method = m
		if _, p := s.mux.Handler(&probe); p != "" {
			return true
		}
	}
	return false
}

// Run 监听直到 ctx 取消，或 HTTP 服务自己出错结束（如监听器坏了）。请求的 context 派生自 ctx，两种情况下都会被取消，
// 长连接与在途请求都能看到。两种情况走同一套关停，各步共用一个截止时间（shutdownTimeout）：先等在途请求，超时就强制断开；
// 再等 WebSocket handler（/ws/agent、/ws/client）全部退出；然后停下过期扫描；最后同时取消在跑的 webhook 回调与推送工作协程
// 并等它们退出（见 stopBackground）。截止时间被前面的步骤耗尽时，它们仍有 callbackStopGrace 的收尾宽限，总关停不超过
// shutdownTimeout + callbackStopGrace。返回 nil 时 WebSocket 处理、回调与推送都已退出，调用方可以关库；没能开始监听时立即返回
// 满足 errors.Is(err, ErrListen) 的错误（服务没开始，New 里起的推送工作协程也已停下，可以关库）；服务自己出错结束时返回那个错误；
// 有没等到退出的（含 HTTP 收尾超时被强制断开的普通请求：断开连接不会停下它们的处理；开着却没有处理在跑的连接不算），
// 收尾完照样返回，错误满足 errors.Is(err, ErrShutdownTimeout)。
// 配了 ACMEDomain 时直出 TLS（serveTLS：证书目录、80 端口的挑战与跳转，再走同一个 serve）。
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return s.notStarted(err) // net 的错误已带地址
	}
	if s.cfg.ACMEDomain != "" {
		return s.serveTLS(ctx, ln)
	}
	return s.serve(ctx, ln)
}

// notStarted 是没能开始服务时的收尾：推送工作协程在 New 里就起了，没开始服务也要停下它，调用方随后照常关库
// （此时没有请求在跑，也就没有回调，立即返回）。返回满足 errors.Is(err, ErrListen) 的错误。
func (s *Server) notStarted(err error) error {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	s.stopBackground(ctx)
	cancel()
	return fmt.Errorf("%w: %w", ErrListen, err)
}

// serve 是 Run 在监听之后的部分，测试可传入自己的监听器。
func (s *Server) serve(parent context.Context, ln net.Listener) error {
	// 请求的 context 派生自 ctx：parent 取消或 HTTP 服务自己出错结束时都取消它。后一种情况没有人取消 parent，
	// 不取消的话 /ws/client 的 handler 一直挂着，关停等不到它们退出。
	ctx, cancelRun := context.WithCancel(parent)
	defer cancelRun()
	// ReadTimeout：直出 TLS 时没有反向代理替 hub 缓冲慢速请求，请求体迟迟发不完的连接到期断开（处理器不读请求体时，
	// net/http 回应前要读掉剩下的）。不影响长连接与慢处理器：WebSocket 在 Hijack 时清掉期限；请求体读完（或没有请求体）时
	// net/http 开后台读也清掉期限；decodeJSON 自己设、自己清读截止。
	hs := &http.Server{Addr: s.cfg.Listen, Handler: s.countRequests(s.Handler()), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: readTimeout,
		IdleTimeout: idleTimeout, BaseContext: func(net.Listener) context.Context { return ctx }}
	s.log.Printf("hub 监听 %s", ln.Addr())
	// 过期扫描随 Run 结束：Run 返回前等它退出，调用方随后关库也不会撞上它
	sweepCtx, stopSweep := context.WithCancel(ctx)
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		s.expireLoop(sweepCtx)
	}()
	errCh := make(chan error, 1)
	go func() { errCh <- hs.Serve(ln) }()
	var runErr error
	select {
	case <-ctx.Done():
	case err := <-errCh:
		// 服务自己结束（还没开始关停，不会是 ErrServerClosed）：在跑的 WebSocket handler 与回调照样要收尾，
		// 先取消请求的 context，再走同一套关停
		if !errors.Is(err, http.ErrServerClosed) {
			runErr = err
		}
		cancelRun()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	clean := s.shutdownHTTP(shutdownCtx, hs)
	// 过期扫描先停（它会给过期的 webhook 请求发起回调），再停回调与推送
	stopSweep()
	<-sweepDone
	if !s.stopBackground(shutdownCtx) {
		clean = false
	}
	if !clean {
		return errors.Join(runErr, ErrShutdownTimeout)
	}
	return runErr
}

// countRequests 把每个 HTTP 处理记进 s.reqs；关停开始后到来的请求回 503。
func (s *Server) countRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.reqs.enter() {
			writeErr(w, http.StatusServiceUnavailable, "hub 正在关停")
			return
		}
		defer s.reqs.leave()
		next.ServeHTTP(w, r)
	})
}

// shutdownHTTP 停止接纳新的 WebSocket handler 与请求、断开守护进程，等在途请求到 ctx 结束（超时就强制断开），
// 再在同一截止前等全部处理（普通请求与 WebSocket handler）退出。截止前没等到它们全部退出时返回 false：
// 强制断开只关连接，被断开的连接上处理仍在跑，可能碰存储。开着却没有处理在跑的连接（浏览器的预连接、
// 客户端多拨的连接）会让 Shutdown 等到截止，但不碰存储，不算没收尾干净。
func (s *Server) shutdownHTTP(ctx context.Context, hs *http.Server) bool {
	s.ws.shutdown() // 先停止接纳新的 WebSocket handler；它们同时记在 reqs 里，下面一并等
	reqsIdle := s.reqs.shutdown()
	s.agents.closeAll()
	if err := hs.Shutdown(ctx); err != nil {
		s.log.Printf("hub 关停超时，强制断开: %v", err)
		hs.Close()
	}
	// Shutdown 不管已劫持的 WebSocket 连接：它们的 handler 经 BaseContext 看到取消后自行退出，这里等到全部处理都退出。
	// 截止已到（HTTP 收尾耗尽了它）而处理也都已退出时，两个分支同时就绪：先看处理，免得随机误报超时
	select {
	case <-reqsIdle:
		return true
	default:
	}
	select {
	case <-reqsIdle:
		return true
	case <-ctx.Done():
		ws := s.ws.count()
		s.log.Printf("hub 关停超时，仍有 %d 个 WebSocket 连接的处理、%d 个请求的处理未退出", ws, max(s.reqs.count()-ws, 0))
		return false
	}
}

const idleTimeout = 120 * time.Second

// shutdownTimeout 是关停的共用截止时间（见 Run）。callbackStopGrace 是截止已到时，取消回调后仍给它们收尾
// （callbackAborted：清令牌、写审计）的最小宽限：前面的 HTTP、WebSocket 收尾可能已把截止时间耗尽。
// 总关停不超过 shutdownTimeout + callbackStopGrace。两者是变量只为测试能缩短。
var (
	shutdownTimeout   = 3 * time.Second
	callbackStopGrace = time.Second
)

// readTimeout 是 http.Server.ReadTimeout（见 serve）。是变量只为测试能缩短它。
var readTimeout = 30 * time.Second

// bodyReadTimeout 是读 JSON 请求体的截止。是变量只为测试能缩短它。
var bodyReadTimeout = 10 * time.Second

// decodeJSON 把限长 limit 字节的请求体解码进 v，读请求体有 bodyReadTimeout 的截止（慢速发送占不住连接），
// 返回前清掉截止：否则截止一到，net/http 的后台读超时会取消 r.Context()（空请求体时后台读在处理器开始前就开着）。
// 超长、超时、不是合法 UTF-8（encoding/json 会把非法字节悄悄换成 U+FFFD，这里先拒）、不是 JSON 都返回错误。
// 所有 JSON 请求体都经它读。
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	data, err := readBody(w, r, limit)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// readBody 读至多 limit 字节的请求体（截止与清除同 decodeJSON），并要求是合法 UTF-8。
// 超长时错误是 *http.MaxBytesError。非 JSON 的请求体（如规则导入的 YAML）经它读。
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	// 不支持设截止的 ResponseWriter（如测试里的 ResponseRecorder）忽略错误，照常读
	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(bodyReadTimeout))
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	_ = rc.SetReadDeadline(time.Time{})
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, errors.New("请求体不是合法的 UTF-8")
	}
	return data, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // 状态码已发出，写失败（客户端已断开）或编码失败（现有调用方都传可编码的 map/struct）都无从补救
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// internalErr 记日志并回 500。内部错误原文（可能含路径、SQL）只进日志，不回给客户端。
func (s *Server) internalErr(w http.ResponseWriter, what string, err error) {
	s.log.Printf("%s: %v", what, err)
	writeErr(w, http.StatusInternalServerError, "内部错误")
}
