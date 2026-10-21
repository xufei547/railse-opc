package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
	"github.com/railse/railse-opc/internal/redact"
)

// maxSocketPath 是 socket 路径的字节上限。macOS 的 sun_path 只有 104 字节，留些余量。
const maxSocketPath = 100

// Server 监听 Unix socket，一次连接处理一条消息。
type Server struct {
	cfg     Config
	rules   *Rules
	audit   *Audit
	decider Decider
	pending *Pending
	log     *log.Logger
	ln      net.Listener
	start   time.Time
	wg      sync.WaitGroup // 进行中的连接

	mu      sync.Mutex
	closed  bool
	stop    context.CancelFunc // Serve 运行时非空，取消全部连接
	serving chan struct{}      // Serve 运行时非空，Serve 返回时关闭
}

// New 组装服务，不做 IO。
func New(cfg Config, rules *Rules, audit *Audit, decider Decider, logger *log.Logger) *Server {
	return &Server{cfg: cfg, rules: rules, audit: audit, decider: decider, pending: newPending(), log: logger, start: time.Now()}
}

// Listen 建 socket。上次崩溃留下的无人监听的 socket 文件会被替换；已有守护进程在监听时报错。
func (s *Server) Listen() error {
	if n := len(s.cfg.SocketPath); n > maxSocketPath {
		return fmt.Errorf("socket 路径太长（%d 字节，上限 %d）: %s", n, maxSocketPath, s.cfg.SocketPath)
	}
	if _, err := os.Stat(s.cfg.SocketPath); err == nil {
		if c, err := net.DialTimeout("unix", s.cfg.SocketPath, 500*time.Millisecond); err == nil {
			c.Close()
			return fmt.Errorf("已有守护进程在 %s 监听", s.cfg.SocketPath)
		}
		os.Remove(s.cfg.SocketPath)
	}
	ln, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.cfg.SocketPath, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.ln = ln
	return nil
}

// Serve 接受连接直到 ctx 取消或 Close，返回前等所有连接处理完。
func (s *Server) Serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s.mu.Lock()
	if s.ln == nil {
		s.mu.Unlock()
		return errors.New("Serve 之前要先 Listen")
	}
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	s.stop, s.serving = cancel, done
	s.mu.Unlock()
	defer close(done)
	stopWatch := context.AfterFunc(ctx, func() { s.ln.Close() })
	defer stopWatch()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				cancel()
				s.wg.Wait()
				return nil
			}
			s.log.Printf("accept: %v", err)
			// 文件描述符耗尽之类的错误会立刻重现，稍等再试，免得空转刷日志。
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	}
}

// Close 关闭监听，取消进行中的请求，等 Serve 返回后删除 socket 文件。
func (s *Server) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	stop, done := s.stop, s.serving
	s.mu.Unlock()
	if s.ln == nil {
		return nil // 没监听过，socket 文件可能属于别的守护进程，不能删
	}
	if stop != nil {
		stop()
	}
	s.ln.Close()
	if done != nil {
		<-done
	}
	os.Remove(s.cfg.SocketPath)
	return nil
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := proto.ReadLine(bufio.NewReader(conn))
	if err != nil {
		s.log.Printf("读请求: %v", err)
		return
	}
	conn.SetReadDeadline(time.Time{})
	kind, err := proto.PeekKind(line)
	if err != nil {
		proto.WriteLine(conn, proto.Response{V: proto.Version, Decision: "passthrough", Reason: "请求格式错误"})
		return
	}
	switch kind {
	case "permission":
		var req proto.Request
		if err := proto.Decode(line, &req); err != nil {
			proto.WriteLine(conn, proto.Response{V: proto.Version, Decision: "passthrough", Reason: "请求格式错误: " + err.Error()})
			return
		}
		// 钩子不会再发数据；读到 EOF 或错误就表示它消失了。答复后连接关闭，这个 goroutine 随之退出。
		cctx, cancel := context.WithCancel(ctx)
		go func() {
			var b [1]byte
			conn.Read(b[:])
			cancel()
		}()
		resp := s.handlePermission(cctx, req)
		cancel()
		if resp.Decision != "" { // 钩子已消失时 resp 为空，不写回
			proto.WriteLine(conn, resp)
		}
	case "question", "done", "session_end":
		var ev proto.Event
		if err := proto.Decode(line, &ev); err != nil {
			proto.WriteLine(conn, proto.EventAck{V: proto.Version, OK: false, Error: err.Error()})
			return
		}
		proto.WriteLine(conn, s.handleEvent(ev))
	case "control":
		var c proto.Control
		if err := proto.Decode(line, &c); err != nil {
			proto.WriteLine(conn, proto.ControlReply{V: proto.Version, OK: false, Error: err.Error()})
			return
		}
		proto.WriteLine(conn, s.handleControl(c))
	default:
		proto.WriteLine(conn, proto.Response{V: proto.Version, Decision: "passthrough", Reason: "未知消息种类 " + kind})
	}
}

// evaluate 算项目根并跑引擎。
func (s *Server) evaluate(req proto.Request) (policy.Decision, policy.Context) {
	cwd := req.Payload.Cwd
	if cwd == "" {
		cwd = req.Session.Cwd
	}
	cwd = CleanCwd(cwd)
	req.Payload.Cwd = cwd
	// 相对路径（含空 cwd 清理后的 "."）会按守护进程自己的工作目录解析，不能拿来找项目根。
	var root string
	if filepath.IsAbs(cwd) {
		root = FindProjectRoot(cwd)
	}
	pctx := policy.Context{ProjectRoot: root, HomeDir: s.cfg.HomeDir, MachineID: s.cfg.MachineID}
	return s.rules.Engine().Evaluate(req.Payload, pctx), pctx
}

// handlePermission 是审批主路径：引擎能定就定，定不了交给 Decider。
func (s *Server) handlePermission(ctx context.Context, req proto.Request) proto.Response {
	id := NewID()
	d, _ := s.evaluate(req)
	redacted := redact.Payload(req.Payload)
	entry := AuditEntry{TS: time.Now(), RequestID: id, Kind: "permission", Session: req.Session, ToolName: req.ToolName, Payload: &redacted, Risk: d.Risk.String(), RuleID: d.RuleID, Reason: d.Reason}
	resp := proto.Response{V: proto.Version, RequestID: id, Risk: d.Risk.String(), RuleID: d.RuleID, Reason: d.Reason}
	switch d.Action {
	case policy.ActionAllow:
		entry.Action, entry.State, entry.DecidedBy = "allow", "auto_allowed", "rule:"+d.RuleID
		resp.Decision = "allow"
	case policy.ActionDeny:
		entry.Action, entry.State, entry.DecidedBy = "deny", "auto_denied", "rule:"+d.RuleID
		resp.Decision = "deny"
	default:
		if s.rules.Paused() {
			// 总开关打开：用户 deny 仍由引擎处理，其余一律交回终端，不去打扰 hub。
			entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "paused", "自动审批已暂停，交回终端处理"
			resp.Decision, resp.Reason = "passthrough", entry.Reason
			break
		}
		return s.waitDecision(ctx, id, req, d, entry)
	}
	s.writeAudit(entry)
	return resp
}

// waitDecision 登记未完成请求并等 Decider，期限取配置与请求两者中较短的。
func (s *Server) waitDecision(ctx context.Context, id string, req proto.Request, d policy.Decision, entry AuditEntry) proto.Response {
	wait := s.cfg.Deadline
	if req.DeadlineSeconds > 0 && time.Duration(req.DeadlineSeconds)*time.Second < wait {
		wait = time.Duration(req.DeadlineSeconds) * time.Second
	}
	pr := &PendingRequest{ID: id, Req: req, Risk: d.Risk, Created: time.Now(), Deadline: time.Now().Add(wait)}
	s.pending.Add(pr)
	defer s.pending.Remove(id)
	dctx, cancel := context.WithDeadline(ctx, pr.Deadline)
	defer cancel()
	out, err := s.decider.Decide(dctx, *pr)
	resp := proto.Response{V: proto.Version, RequestID: id, Risk: d.Risk.String()}
	switch {
	case err != nil && ctx.Err() != nil:
		entry.State, entry.DecidedBy, entry.Reason = "cancelled", "terminal", "钩子进程在决定前退出"
		s.writeAudit(entry)
		return proto.Response{} // 对方已不在，不写回
	case err != nil && errors.Is(dctx.Err(), context.DeadlineExceeded):
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "expired", "timeout", "等待过期，交回终端处理"
		resp.Decision, resp.Reason = "passthrough", entry.Reason
	case err != nil:
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "error", err.Error()
		resp.Decision, resp.Reason = "passthrough", "决定来源出错，交回终端处理"
	default:
		action := out.Action
		if action != "allow" && action != "deny" {
			action = "passthrough" // 空的或不认识的结论按不作决定处理，钩子总能收到答复
		}
		resp.Decision, resp.Reason, resp.RuleID = action, out.Reason, out.RuleID
		entry.Action, entry.DecidedBy, entry.Reason, entry.RuleID = action, out.DecidedBy, out.Reason, out.RuleID
		switch action {
		case "allow":
			entry.State = "approved"
		case "deny":
			entry.State = "denied"
		default:
			entry.State = "passthrough"
		}
	}
	s.writeAudit(entry)
	return resp
}

func (s *Server) writeAudit(e AuditEntry) {
	if err := s.audit.Write(e); err != nil {
		s.log.Printf("写审计: %v", err)
	}
}

// handleEvent 与 handleControl 在 Task 9 实现；这里先放占位让包能编译。
func (s *Server) handleEvent(ev proto.Event) proto.EventAck {
	return proto.EventAck{V: proto.Version, OK: false, Error: "未实现"}
}

func (s *Server) handleControl(c proto.Control) proto.ControlReply {
	return proto.ControlReply{V: proto.Version, OK: false, Error: "未实现"}
}
