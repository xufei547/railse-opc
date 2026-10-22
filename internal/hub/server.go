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
	"time"
	"unicode/utf8"

	"github.com/railse/railse-opc/internal/hub/store"
)

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
		verify: VerifyPassword, verifyWait: loginVerifyWait, helloTimeout: defaultHelloTimeout}
	s.routes()
	return s, nil
}

// routes 注册认证、接入与守护进程入口，再注册网页用的接口（apiRoutes）；后面的 Task 往里加。
func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", sameOrigin(s.handleLogout))
	s.mux.HandleFunc("POST /api/enroll", s.handleEnroll)
	s.mux.HandleFunc("GET /ws/agent", s.handleAgentWS)
	s.apiRoutes()
}

// Handler 返回根处理器。
func (s *Server) Handler() http.Handler { return s.mux }

// Run 监听直到 ctx 取消。请求的 context 派生自 ctx，取消时长连接与在途请求都能看到；
// 关停先等在途请求至多 shutdownTimeout，超时就强制断开。
func (s *Server) Run(ctx context.Context) error {
	hs := &http.Server{Addr: s.cfg.Listen, Handler: s.mux, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: idleTimeout,
		BaseContext: func(net.Listener) context.Context { return ctx }}
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	s.log.Printf("hub 监听 %s", ln.Addr())
	// 过期扫描随 Run 结束：Run 返回前等它退出，调用方随后关库也不会撞上它
	sweepCtx, stopSweep := context.WithCancel(ctx)
	sweepDone := make(chan struct{})
	go func() {
		defer close(sweepDone)
		s.expireLoop(sweepCtx)
	}()
	defer func() {
		stopSweep()
		<-sweepDone
	}()
	errCh := make(chan error, 1)
	go func() { errCh <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		s.agents.closeAll()
		if err := hs.Shutdown(shutdownCtx); err != nil {
			s.log.Printf("hub 关停超时，强制断开: %v", err)
			hs.Close()
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

const (
	idleTimeout     = 120 * time.Second
	shutdownTimeout = 3 * time.Second
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
