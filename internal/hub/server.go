// Package hub 是跑在云服务器上的中心服务：存请求、规则、机器与审计，接守护进程的 WebSocket，
// 给手机网页和 webhook 来源提供 HTTP 接口。hub 只能对请求回 allow 或 deny，没有向机器下发命令的通道。
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/railse/railse-opc/internal/hub/store"
)

// ErrShutdownTimeout 表示 Run 关停时没在时限内等到全部 WebSocket 处理（/ws/agent、/ws/client）或 webhook 回调退出。
// Run 仍尽力收尾后才返回它；那些还在跑的可能继续碰存储，调用方不宜随即关库（进程即将退出时直接退出即可）。
var ErrShutdownTimeout = errors.New("hub 关停超时：仍有 WebSocket 处理或 webhook 回调没有退出")

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
}

// validate 检查时长为正、可信代理前缀有效。
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
	// ws 记在跑的 WebSocket handler（/ws/agent、/ws/client）：http.Server.Shutdown 不管已劫持的连接，Run 靠它等这些 handler 退出。
	ws tracker
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

// New 组装 hub：校验配置，确保有内置规则，注册路由。
func New(cfg Config, st *store.Store, logger *log.Logger) (*Server, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg.TrustedProxies = normPrefixes(cfg.TrustedProxies)
	if _, err := st.EnsureDefaultRules(); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: st, log: logger, mux: http.NewServeMux(), limiter: newLimiter(), agents: newAgentHub(logger), clients: newClientHub(), start: time.Now(),
		verify: VerifyPassword, verifyWait: loginVerifyWait, helloTimeout: defaultHelloTimeout,
		callbackRetryWait: defaultCallbackRetryWait, callbackClient: newCallbackClient()}
	s.cbCtx, s.cbCancel = context.WithCancel(context.Background())
	s.routes()
	return s, nil
}

// routes 注册认证、接入与守护进程入口，再注册网页用的接口（apiRoutes）；后面的 Task 往里加。
func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", sameOrigin(s.handleLogout))
	s.mux.HandleFunc("POST /api/enroll", s.handleEnroll)
	s.mux.HandleFunc("GET /ws/agent", s.handleAgentWS)
	s.mux.HandleFunc("POST /api/webhook/approvals", s.handleWebhookIngest)
	s.apiRoutes()
}

// Handler 返回根处理器。
func (s *Server) Handler() http.Handler { return s.mux }

// Run 监听直到 ctx 取消，或 HTTP 服务自己出错结束（如监听器坏了）。请求的 context 派生自 ctx，两种情况下都会被取消，
// 长连接与在途请求都能看到。两种情况走同一套关停，各步共用一个截止时间（shutdownTimeout）：先等在途请求，超时就强制断开；
// 再等 WebSocket handler（/ws/agent、/ws/client）全部退出；然后停下过期扫描；最后取消在跑的 webhook 回调并等它们退出
// （见 stopCallbacks）。截止时间被前面的步骤耗尽时，回调仍有 callbackStopGrace 的收尾宽限，总关停不超过
// shutdownTimeout + callbackStopGrace。返回 nil 时 WebSocket 处理与回调都已退出，调用方可以关库；服务自己出错结束时返回
// 那个错误；有没等到退出的，收尾完照样返回，错误满足 errors.Is(err, ErrShutdownTimeout)。
// （HTTP 收尾超时被强制断开的普通请求不在其列：它们碰到已关的库只会得到错误。）
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	return s.serve(ctx, ln)
}

// serve 是 Run 在监听之后的部分，测试可传入自己的监听器。
func (s *Server) serve(parent context.Context, ln net.Listener) error {
	// 请求的 context 派生自 ctx：parent 取消或 HTTP 服务自己出错结束时都取消它。后一种情况没有人取消 parent，
	// 不取消的话 /ws/client 的 handler 一直挂着，关停等不到它们退出。
	ctx, cancelRun := context.WithCancel(parent)
	defer cancelRun()
	hs := &http.Server{Addr: s.cfg.Listen, Handler: s.mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: idleTimeout,
		BaseContext: func(net.Listener) context.Context { return ctx }}
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
	// 过期扫描先停（它会给过期的 webhook 请求发起回调），再停回调
	stopSweep()
	<-sweepDone
	if !s.stopCallbacks(shutdownCtx) {
		clean = false
	}
	if !clean {
		return errors.Join(runErr, ErrShutdownTimeout)
	}
	return runErr
}

// shutdownHTTP 停止接纳新的 WebSocket handler、断开守护进程，等在途请求到 ctx 结束（超时就强制断开），
// 再在同一截止前等 WebSocket handler 全部退出。截止前没等到它们全部退出时返回 false。
func (s *Server) shutdownHTTP(ctx context.Context, hs *http.Server) bool {
	wsIdle := s.ws.shutdown() // 先停止接纳新的 WebSocket handler
	s.agents.closeAll()
	if err := hs.Shutdown(ctx); err != nil {
		s.log.Printf("hub 关停超时，强制断开: %v", err)
		hs.Close()
	}
	// Shutdown 不管已劫持的 WebSocket 连接：它们的 handler 经 BaseContext 看到取消后自行退出，这里等到它们都退出
	// 截止已到（HTTP 收尾耗尽了它）而 handler 也都已退出时，两个分支同时就绪：先看 handler，免得随机误报超时
	select {
	case <-wsIdle:
		return true
	default:
	}
	select {
	case <-wsIdle:
		return true
	case <-ctx.Done():
		s.log.Printf("hub 关停超时，仍有 %d 个 WebSocket 连接的处理未退出", s.ws.count())
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
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// internalErr 记日志并回 500。内部错误原文（可能含路径、SQL）只进日志，不回给客户端。
func (s *Server) internalErr(w http.ResponseWriter, what string, err error) {
	s.log.Printf("%s: %v", what, err)
	writeErr(w, http.StatusInternalServerError, "内部错误")
}
