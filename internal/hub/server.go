// Package hub 是跑在云服务器上的中心服务：存请求、规则、机器与审计，接守护进程的 WebSocket，
// 给手机网页和 webhook 来源提供 HTTP 接口。hub 只能对请求回 allow 或 deny，没有向机器下发命令的通道。
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

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
}

// New 组装 hub：确保有内置规则，注册路由。
func New(cfg Config, st *store.Store, logger *log.Logger) (*Server, error) {
	if _, err := st.EnsureDefaultRules(); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: st, log: logger, mux: http.NewServeMux(), limiter: newLimiter(), agents: newAgentHub(), clients: newClientHub(), start: time.Now()}
	s.routes()
	return s, nil
}

// routes 注册全部路由；后面的 Task 往里加。
func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)
	s.mux.HandleFunc("POST /api/enroll", s.handleEnroll)
	s.mux.HandleFunc("GET /api/status", s.requireClient(s.handleStatus))
	s.mux.HandleFunc("GET /ws/agent", s.handleAgentWS)
}

// Handler 返回根处理器。
func (s *Server) Handler() http.Handler { return s.mux }

// Run 监听直到 ctx 取消。
func (s *Server) Run(ctx context.Context) error {
	hs := &http.Server{Addr: s.cfg.Listen, Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	s.log.Printf("hub 监听 %s", ln.Addr())
	errCh := make(chan error, 1)
	go func() { errCh <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.agents.closeAll()
		return hs.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// handleStatus 在 Task 9 扩充；这里先给认证测试一个受保护的端点。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	pending, err := s.store.CountPending()
	if err != nil {
		s.internalErr(w, "统计待审批", err)
		return
	}
	version, err := s.store.RulesVersion()
	if err != nil {
		s.internalErr(w, "读规则版本", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": pending, "rules_version": version, "uptime": time.Since(s.start).Round(time.Second).String()})
}

// decodeJSON 把限长 limit 字节的请求体解码进 v；超长或不是 JSON 都返回错误。所有 JSON 请求体都经它读。
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, v any) error {
	return json.NewDecoder(http.MaxBytesReader(w, r.Body, limit)).Decode(v)
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

// agentHub 与 clientHub 的完整实现见 Task 7、8。
type agentHub struct{}

func newAgentHub() *agentHub  { return &agentHub{} }
func (h *agentHub) closeAll() {}
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "未实现")
}

type clientHub struct{}

func newClientHub() *clientHub { return &clientHub{} }
