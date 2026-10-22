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
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
	"github.com/railse/railse-opc/internal/redact"
)

// maxSocketPath 是 socket 路径的字节上限。macOS 的 sun_path 只有 104 字节，留些余量。
const maxSocketPath = 100

// errHookGone 是钩子连接断开时取消请求 ctx 的原因，用来和守护进程关停区分开。
var errHookGone = errors.New("钩子连接已断开")

// hubUnreachable 是 Decider 在 hub 连不上（HubGrace 内没重连上）时填的 DecidedBy。
// 这时钩子收 passthrough，本机审计记 expired（设计 3.2：等待作废，不是“根本没去等”）。
const hubUnreachable = "hub_unreachable"

// AuditSink 接收每条审计的副本（hub 上报用）。Forward 在处理请求的路径上同步调用，必须不阻塞：
// 实现只把条目放进自己的队列就返回，发送在别的 goroutine 里做。
type AuditSink interface{ Forward(e AuditEntry) }

// sinkBox 让接口值能放进 atomic.Pointer。
type sinkBox struct{ sink AuditSink }

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
	wg      sync.WaitGroup          // 进行中的连接
	sink    atomic.Pointer[sinkBox] // 审计转发目标，nil 表示只写本地；可在 Serve 运行中替换

	// 暂停分开存：本机 pause/resume 与 hub 下发的总开关，生效值取或交给 rules。
	// pauseMu 同时串行化 state.json 的写入，两个字段总是一起写。
	pauseMu     sync.Mutex
	localPaused bool
	hubPaused   bool

	mu      sync.Mutex
	closed  bool
	stop    context.CancelFunc // Serve 运行时非空，取消全部连接
	serving chan struct{}      // Serve 运行时非空，Serve 返回时关闭
}

// New 组装服务，不做 IO。
func New(cfg Config, rules *Rules, audit *Audit, decider Decider, logger *log.Logger) *Server {
	return &Server{cfg: cfg, rules: rules, audit: audit, decider: decider, pending: newPending(), log: logger, start: time.Now()}
}

// SetAuditSink 设置审计转发目标；nil 表示只写本地。可在 Serve 运行中随时调用。
func (s *Server) SetAuditSink(sink AuditSink) {
	if sink == nil {
		s.sink.Store(nil)
		return
	}
	s.sink.Store(&sinkBox{sink: sink})
}

// Pending 返回未完成请求表，hub 客户端重连时枚举用。只能在 Listen 之前调用（不加锁）。
func (s *Server) Pending() *Pending { return s.pending }

// SetDecider 换决定来源（装 hub 客户端用）。只能在 Listen 之前调用（不加锁）。
func (s *Server) SetDecider(d Decider) { s.decider = d }

// Listen 建 socket。上次崩溃留下的无人监听的 socket 文件会被替换；已有守护进程在监听时报错；
// 路径上是普通文件、符号链接等非 socket 时不删，报错。
func (s *Server) Listen() error {
	if n := len(s.cfg.SocketPath); n > maxSocketPath {
		return fmt.Errorf("socket 路径太长（%d 字节，上限 %d）: %s", n, maxSocketPath, s.cfg.SocketPath)
	}
	if st, err := os.Lstat(s.cfg.SocketPath); err == nil {
		if st.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("%s 已存在且不是 socket（%v），不删除，请确认后手动移走", s.cfg.SocketPath, st.Mode().Type())
		}
		if c, err := net.DialTimeout("unix", s.cfg.SocketPath, 500*time.Millisecond); err == nil {
			c.Close()
			return fmt.Errorf("已有守护进程在 %s 监听", s.cfg.SocketPath)
		}
		if err := os.Remove(s.cfg.SocketPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除陈旧 socket: %w", err)
		}
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
			// Decider 或引擎 panic 只丢掉这一条：handleConn 的 defer 已关连接，钩子读到 EOF 交回终端。
			defer func() {
				if r := recover(); r != nil {
					s.log.Printf("处理连接时 panic: %v\n%s", r, debug.Stack())
				}
			}()
			s.handleConn(ctx, conn)
		}()
	}
}

// Close 关闭监听，取消进行中的请求，等 Serve 返回。
// socket 文件由 ln.Close 删除（net.Listen 建的 Unix 监听关闭时会 unlink）；这里不再删一次，
// 因为等 Serve 的期间新守护进程可能已在同一路径监听，再删就删掉了它的 socket。
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
	return nil
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	// 关停时不必等满读超时：ctx 取消就让正在进行的读立即返回。
	stopRead := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Now()) })
	line, err := proto.ReadLine(bufio.NewReader(conn))
	stopRead()
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
		// 关停导致的取消由父 ctx 传下来，原因不是 errHookGone，waitDecision 据此区分。
		cctx, cancel := context.WithCancelCause(ctx)
		go func() {
			var b [1]byte
			conn.Read(b[:])
			cancel(errHookGone)
		}()
		resp := s.handlePermission(cctx, req)
		cancel(nil)
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
	d, pctx := s.evaluate(req)
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
		return s.waitDecision(ctx, id, req, d, pctx.ProjectRoot, redacted, entry)
	}
	s.writeAudit(entry)
	return resp
}

// waitDecision 登记未完成请求并等 Decider，期限取配置与请求两者中较短的。
// projectRoot、redacted 与引擎理由随请求登记，hub 客户端上报时只用脱敏后的载荷。
func (s *Server) waitDecision(ctx context.Context, id string, req proto.Request, d policy.Decision, projectRoot string, redacted policy.Payload, entry AuditEntry) proto.Response {
	wait := s.cfg.Deadline
	if req.DeadlineSeconds > 0 && time.Duration(req.DeadlineSeconds)*time.Second < wait {
		wait = time.Duration(req.DeadlineSeconds) * time.Second
	}
	now := time.Now()
	pr := &PendingRequest{ID: id, Req: req, Risk: d.Risk, ProjectRoot: projectRoot, Redacted: redacted, EngineReason: d.Reason, Created: now, Deadline: now.Add(wait)}
	s.pending.Add(pr)
	defer s.pending.Remove(id)
	dctx, cancel := context.WithDeadline(ctx, pr.Deadline)
	defer cancel()
	out, err := s.decider.Decide(dctx, *pr)
	resp := proto.Response{V: proto.Version, RequestID: id, Risk: d.Risk.String()}
	switch {
	case err != nil && errors.Is(context.Cause(ctx), errHookGone):
		entry.State, entry.DecidedBy, entry.Reason = "cancelled", "terminal", "钩子进程在决定前退出"
		s.writeAudit(entry)
		return proto.Response{} // 对方已不在，不写回
	case err != nil && ctx.Err() != nil:
		// 守护进程正在停止，钩子还在等：明确交回终端。
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "shutdown", "守护进程正在停止，交回终端处理"
		resp.Decision, resp.Reason = "passthrough", entry.Reason
	case err != nil && errors.Is(dctx.Err(), context.DeadlineExceeded):
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "expired", "timeout", "等待过期，交回终端处理"
		resp.Decision, resp.Reason = "passthrough", entry.Reason
	case err != nil:
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "error", err.Error()
		resp.Decision, resp.Reason = "passthrough", "决定来源出错，交回终端处理"
	default:
		action := out.Action
		unreachable := out.DecidedBy == hubUnreachable
		if (action != "allow" && action != "deny") || unreachable {
			// 空的或不认识的结论按不作决定处理，钩子总能收到答复；hub 不可达时不可能有决定，只能交回终端。
			action = "passthrough"
		}
		// 备注只进审计，不写进 resp：钩子与模型看不到手机上的自由文本。
		resp.Decision, resp.Reason, resp.RuleID = action, out.Reason, out.RuleID
		entry.Action, entry.DecidedBy, entry.Reason, entry.RuleID, entry.Note = action, out.DecidedBy, out.Reason, out.RuleID, out.Note
		switch {
		case unreachable:
			entry.State = "expired"
		case action == "allow":
			entry.State = "approved"
		case action == "deny":
			entry.State = "denied"
		default:
			entry.State = "passthrough"
		}
	}
	s.writeAudit(entry)
	return resp
}

// writeAudit 写本地审计，再把副本交给转发目标（若有）。本地写失败也照样转发。
func (s *Server) writeAudit(e AuditEntry) {
	if err := s.audit.Write(e); err != nil {
		s.log.Printf("写审计: %v", err)
	}
	if b := s.sink.Load(); b != nil {
		b.sink.Forward(e)
	}
}

// handleEvent 记录只展示的事件。同一会话有未完成审批时压掉 question，避免一件事进两条。
// 消息先按文本语境脱敏再写审计：Gemini 的 ToolPermission 通知会拼进完整命令，
// last_assistant_message、prompt_response 也可能原样带出凭据。
func (s *Server) handleEvent(ev proto.Event) proto.EventAck {
	state := "recorded"
	if ev.Kind == "question" && s.pending.HasSession(ev.Session.CLISessionID) {
		state = "suppressed"
	}
	s.writeAudit(AuditEntry{TS: time.Now(), Kind: ev.Kind, Session: ev.Session, Message: redact.Text(ev.Message), State: state})
	return proto.EventAck{V: proto.Version, OK: true}
}

// handleControl 处理本机管理命令。
func (s *Server) handleControl(c proto.Control) proto.ControlReply {
	ok := proto.ControlReply{V: proto.Version, OK: true}
	switch c.Op {
	case "status":
		s.pauseMu.Lock()
		paused, local, hub := s.rules.Paused(), s.localPaused, s.hubPaused
		s.pauseMu.Unlock()
		ok.Status = &proto.Status{Paused: paused, LocalPaused: local, HubPaused: hub, Rules: s.rules.Count(), Pending: s.pending.Count(), MachineID: s.cfg.MachineID, RulesPath: s.rules.Path(), Uptime: time.Since(s.start).Round(time.Second).String()}
		return ok
	case "pause", "resume":
		// 只改本机这一侧；hub 暂停时本机 resume 不会解除生效的暂停。
		// 落盘失败也照样切换并写审计，只是回复 OK:false 说明重启后不会保留。
		pause := c.Op == "pause"
		s.pauseMu.Lock()
		err := s.setPauseLocked(pause, s.hubPaused)
		s.pauseMu.Unlock()
		entry := AuditEntry{TS: time.Now(), Kind: "control", State: "recorded", Message: c.Op}
		if err != nil {
			done := "已恢复"
			if pause {
				done = "已暂停"
			}
			entry.Reason = done + "，但未能持久化: " + err.Error()
			s.log.Printf("写状态文件: %v", err)
			s.writeAudit(entry)
			return proto.ControlReply{V: proto.Version, OK: false, Error: entry.Reason}
		}
		s.writeAudit(entry)
		return ok
	case "reload":
		if err := s.rules.Reload(); err != nil {
			return proto.ControlReply{V: proto.Version, OK: false, Error: err.Error()}
		}
		s.writeAudit(AuditEntry{TS: time.Now(), Kind: "control", State: "recorded", Message: "reload"})
		return ok
	default:
		return proto.ControlReply{V: proto.Version, OK: false, Error: "未知的管理命令 " + c.Op}
	}
}

// SetHubPaused 设置 hub 下发的总开关（hub 客户端收到 welcome、paused 帧时调用）。只改 hub 这一侧，
// 本机的 pause/resume 不受影响，生效值取两者的或。值没变时什么都不做；变化时写 state.json
// 和审计（kind control，decided_by hub）。落盘失败也照样切换并写审计，返回错误说明重启后不会保留。
func (s *Server) SetHubPaused(p bool) error {
	s.pauseMu.Lock()
	if s.hubPaused == p {
		s.pauseMu.Unlock()
		return nil
	}
	err := s.setPauseLocked(s.localPaused, p)
	s.pauseMu.Unlock()
	op, done := "resume", "hub 总开关已恢复"
	if p {
		op, done = "pause", "hub 总开关已暂停"
	}
	entry := AuditEntry{TS: time.Now(), Kind: "control", State: "recorded", Message: op, DecidedBy: "hub"}
	if err != nil {
		err = fmt.Errorf("%s，但未能持久化: %w", done, err)
		entry.Reason = err.Error()
		s.log.Printf("写状态文件: %v", err)
	}
	s.writeAudit(entry)
	return err
}

// setPauseLocked 把两个开关改成 local、hub，生效值（两者取或）交给引擎，并把两者一起原子写进 state.json。
// 顺序保证过渡期间运行中的守护进程总处在更保守的一边：生效值为暂停时先改内存立即生效再落盘，
// 解除暂停时先落盘再改内存。落盘失败也照样切换，错误交调用方说明。调用方持有 pauseMu。
func (s *Server) setPauseLocked(local, hub bool) error {
	st := state{Paused: local, HubPaused: hub}
	if local || hub {
		s.applyPauseLocked(local, hub)
		return saveState(s.cfg.StatePath, st)
	}
	err := saveState(s.cfg.StatePath, st)
	s.applyPauseLocked(local, hub)
	return err
}

func (s *Server) applyPauseLocked(local, hub bool) {
	s.localPaused, s.hubPaused = local, hub
	s.rules.SetPaused(local || hub)
}

// LoadState 从 state.json 恢复本机与 hub 两个暂停开关，启动时调用（要在 hub 客户端开始收帧之前）。
// 状态文件读不了或损坏时按本机暂停启动并返回错误，调用方记日志即可，不必退出。
func (s *Server) LoadState() error {
	st, err := loadState(s.cfg.StatePath)
	s.pauseMu.Lock()
	s.applyPauseLocked(st.Paused, st.HubPaused)
	s.pauseMu.Unlock()
	return err
}
