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
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xufei547/railse-opc/internal/adapter"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
	"github.com/xufei547/railse-opc/internal/redact"
)

// maxSocketPath 是 socket 路径的字节上限。macOS 的 sun_path 只有 104 字节，留些余量。
const maxSocketPath = 100

// maxNonGitLogged 是记住“已在日志里说明过项目根怎么定”的目录（非 git 目录与系统目录里的目录）数上限：满了不再说明新目录，
// 判定不受影响。
const maxNonGitLogged = 1024

// errHookGone 是钩子连接断开时取消请求 ctx 的原因，用来和守护进程关停区分开。
var errHookGone = errors.New("钩子连接已断开")

// errSettledElsewhere 是本机已经处理掉、不必再等的请求的取消原因：同一调用的工具已经跑完（tool_done），或这一轮、会话已经结束
// （done、session_end）。Claude Code 桌面应用（SDK 宿主）在 PreToolUse 回 ask 后用自己的权限回调裁决，却不结束同一调用的
// PermissionRequest 钩子，不清掉的话它会挂到过期，手机上一直留着一张卡片。
var errSettledElsewhere = errors.New("已在终端处理")

// reasonSettledElsewhere 是这类取消写进审计、回给钩子的理由。
const reasonSettledElsewhere = "已在终端处理，取消等待"

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
	// hubStatus 给 status 报 hub 连接状态与地址；nil 表示未接入 hub。Listen 之前装好，之后只读。
	hubStatus func() (state, url string)

	// 暂停分开存：本机 pause/resume 与 hub 下发的总开关，生效值取或交给 rules。
	// pauseMu 同时串行化 state.json 的写入，两个字段总是一起写。
	pauseMu     sync.Mutex
	localPaused bool
	hubPaused   bool

	// nonGitLogged 是已在日志里说明过项目根怎么定的目录（非 git 目录与系统目录里的目录，见 noteRoot），每个目录只说明一次，
	// 最多记 maxNonGitLogged 个；由 nonGitMu 保护。
	nonGitMu     sync.Mutex
	nonGitLogged map[string]struct{}

	// settings 是本机设置（settings.yaml），reload 时整份换掉。
	settings atomic.Pointer[Settings]

	// trusts 是限时放行表（trusts.json），命中即放行引擎答 ask 的审批；trustChanged 是放行变更的回调（见 SetTrustChanged）。
	trusts       *trusts
	trustChanged atomic.Pointer[func(proto.Trust, string)]
	// envSeen 是带过 RAILSE_TRUST 的会话（tool + cli_session_id → 第一次见到的时间），每个会话只建一次放行；由 envMu 保护。
	envMu   sync.Mutex
	envSeen map[string]time.Time

	// roles 是角色文件 roles.yaml（0.3 起），每次判定前看文件变没变（见 Roles）。
	roles *Roles
	// protected 是守护进程自己的数据目录（原样与解析符号链接后的两种写法），判定时作为受保护路径带给引擎（见 policy.Context.ProtectedDirs）。
	protected []string
	// unknownRoleLogged 是已在日志里说过“RAILSE_ROLE 指的角色不存在”的会话（tool + cli_session_id），每个会话只说一次，
	// 最多记 maxNonGitLogged 个；由 nonGitMu 保护。
	unknownRoleLogged map[string]struct{}
	// pinned 是会话钉住的角色（tool + cli_session_id → 角色名与最近一次用到的时间），由 pinMu 保护：会话解析出非空角色后，
	// 之后的请求不再因 cwd 变了而降为未分配或换成别的目录角色，只有指向存在角色的 RAILSE_ROLE 能改；角色从文件里删掉时解除。
	// 只在内存里，守护进程重启后重新解析。
	pinMu  sync.Mutex
	pinned map[string]rolePin
	// procRoles 是有角色的会话的 CLI 进程（进程号@启动时间）→ 约束它的角色名（自己的与继承的），由 pinMu 保护：嵌套会话的祖先链里
	// 有这些进程时继承它们（见 inheritRole）。随钉住表一起有上限、session_end 时释放。
	procRoles map[string]procRole
	// effective 是每个会话最近一次审批时的生效角色名（含从上层会话继承的），由 pinMu 保护：答题与事件没有祖先链，
	// 统一读这里，免得把继承来的角色报成未分配。随钉住表一起有上限、session_end 时释放。
	effective map[string]rolePin
	// sessionProc 是会话（tool + cli_session_id）→ 它的 CLI 进程（有祖先链时才知道），由 pinMu 保护：清理钉住、继承记录与生效角色时
	// 看这个进程还在不在（见 pruneSessions）。session_end 谁都能伪造，不据它清。
	sessionProc map[string]proto.Proc

	mu      sync.Mutex
	closed  bool
	stop    context.CancelFunc // Serve 运行时非空，取消全部连接
	serving chan struct{}      // Serve 运行时非空，Serve 返回时关闭
}

// New 组装服务。IO 只有读本机设置 settings.yaml 与放行表 trusts.json，读坏了记日志、按默认值（空表）跑。
// 角色文件 roles.yaml 在第一次判定时才读（见 Roles）。
func New(cfg Config, rules *Rules, audit *Audit, decider Decider, logger *log.Logger) *Server {
	s := &Server{cfg: cfg, rules: rules, audit: audit, decider: decider, pending: newPending(), log: logger, start: time.Now(), roles: newRolesIn(cfg.RolesPath, cfg.HomeDir)}
	s.protected = protectedDirs(cfg.Dir)
	_ = s.ReloadSettings() // 读坏了它自己记日志
	tr, err := newTrusts(cfg.TrustsPath, time.Now)
	if err != nil {
		logger.Printf("限时放行表读不了，按空表: %v", err)
	}
	s.trusts = tr
	return s
}

// ReloadSettings 重读本机设置；读坏了记日志、换成默认值并返回错误（reload 命令回给用户）。
func (s *Server) ReloadSettings() error {
	st, err := LoadSettings(s.cfg.SettingsPath)
	if err != nil {
		s.log.Printf("本机设置读不了，按默认值（claude_undecided: %s）: %v", st.ClaudeUndecided, err)
	}
	s.settings.Store(&st)
	return err
}

// decidedByAsk 与 reasonAsked：PreToolUse 来的硬底线不在守护进程里等，回 ask 让 Claude 在终端弹确认框并触发 PermissionRequest，
// 那一条再送手机；终端与手机同时显示，谁先答谁算。确认框里的理由由钩子按终端语言用触发点重拼（见 cmd/railse 的 askReason）。
const (
	decidedByAsk = "ask"
	reasonAsked  = "已交终端确认，同时送手机"
)

// SetAuditSink 设置审计转发目标；nil（包括装着 nil 指针的接口值）表示只写本地。可在 Serve 运行中随时调用。
func (s *Server) SetAuditSink(sink AuditSink) {
	if isNil(sink) {
		s.sink.Store(nil)
		return
	}
	s.sink.Store(&sinkBox{sink: sink})
}

// isNil 判断接口值本身为 nil，或装着 nil 的指针、map、切片、函数、通道。
func isNil(v any) bool {
	if v == nil {
		return true
	}
	switch rv := reflect.ValueOf(v); rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan, reflect.Interface, reflect.UnsafePointer:
		return rv.IsNil()
	}
	return false
}

// Pending 返回未完成请求表，hub 客户端重连时枚举用。只能在 Listen 之前调用（不加锁）。
func (s *Server) Pending() *Pending { return s.pending }

// SetHubStatus 装上 hub 连接状态的来源（守护进程里传 HubClient.State 与 hub 地址），status 每次现取；
// 没装时 status 报 none。只能在 Listen 之前调用（不加锁）；fn 会在处理连接的 goroutine 里并发调用。
func (s *Server) SetHubStatus(fn func() (state, url string)) { s.hubStatus = fn }

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
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.sweepTrusts(ctx)
	}()
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
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second)) // 只在连接已关时失败，随后的读会报错
	// 关停时不必等满读超时：ctx 取消就让正在进行的读立即返回。
	stopRead := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	line, err := proto.ReadLine(bufio.NewReader(conn))
	stopRead()
	if err != nil {
		s.log.Printf("读请求: %v", err)
		return
	}
	_ = conn.SetReadDeadline(time.Time{}) // 同上
	kind, err := proto.PeekKind(line)
	if err != nil {
		s.reply(conn, kind, proto.Response{V: proto.Version, Decision: "passthrough", Reason: "请求格式错误"})
		return
	}
	switch kind {
	case "permission", "ask":
		var req proto.Request
		if err := proto.Decode(line, &req); err != nil {
			s.reply(conn, kind, proto.Response{V: proto.Version, Decision: "passthrough", Reason: "请求格式错误: " + err.Error()})
			return
		}
		// 钩子不会再发数据；读到 EOF 或错误就表示它消失了。答复后连接关闭，这个 goroutine 随之退出。
		// 关停导致的取消由父 ctx 传下来，原因不是 errHookGone，waitDecision 据此区分。
		cctx, cancel := context.WithCancelCause(ctx)
		go func() {
			var b [1]byte
			_, _ = conn.Read(b[:]) // 读到什么都不要，只等它返回
			cancel(errHookGone)
		}()
		// 祖先链用连接对端的进程凭据自己重建，不信请求里自报的；取不到凭据时才退回用自报的，并在审计里标注
		switch chain, ok := peerAncestry(conn); {
		case ok:
			req.Ancestors, req.AncestrySource = chain, ancestryPeer
		case trustSelfReported && len(req.Ancestors) > 0:
			req.AncestrySource = ancestrySelfReported
		default:
			// 取不到凭据：不信自报的（自报一条没有上层的链就能放宽），该会话已有的约束照旧保留，见 handlePermission
			req.Ancestors, req.AncestrySource = nil, ancestryUnverified
		}
		handle := s.handlePermission
		if kind == "ask" {
			handle = s.handleAsk
		}
		resp := handle(cctx, req)
		cancel(nil)
		if resp.Decision != "" { // 钩子已消失时 resp 为空，不写回
			s.reply(conn, kind, resp)
		}
	case "question", "done", "session_end", "tool_done":
		var ev proto.Event
		if err := proto.Decode(line, &ev); err != nil {
			s.reply(conn, kind, proto.EventAck{V: proto.Version, OK: false, Error: err.Error()})
			return
		}
		s.reply(conn, kind, s.handleEvent(ev))
	case "control":
		var c proto.Control
		if err := proto.Decode(line, &c); err != nil {
			s.reply(conn, kind, proto.ControlReply{V: proto.Version, OK: false, Error: err.Error()})
			return
		}
		s.reply(conn, kind, s.handleControl(c))
	default:
		s.reply(conn, kind, proto.Response{V: proto.Version, Decision: "passthrough", Reason: "未知消息种类 " + kind})
	}
}

// reply 把一行答复写回钩子。写不出去（钩子已断开、答复超长）只记日志：钩子读不到答复就 passthrough，交回 CLI 自己审批。
// 日志带消息种类（kind 来自钩子，截断后按 %q 记）与审批请求的 request_id（有时），便于对上审计。
func (s *Server) reply(conn net.Conn, kind string, v any) {
	if err := proto.WriteLine(conn, v); err != nil {
		var id string
		if r, ok := v.(proto.Response); ok {
			id = r.RequestID
		}
		s.log.Printf("回写钩子（kind %q，request_id %q）: %v", clipText(kind, hubLogFieldMax), id, err)
	}
}

// evaluate 清理 cwd（载荷没带时取会话的 cwd，解析符号链接）、算项目根、解析会话角色并跑引擎，
// 返回上报用的载荷（带清理后的 cwd）与 Role 改写成生效值的会话。
// 审计与上报 hub 都用这份载荷：hub 侧“记住”按载荷 cwd 与项目根重新定级，两者必须同一口径
// （macOS 的 /var 是 /private/var 的链接，口径不一会把项目内的相对路径写入判成项目外）。
// 载荷与会话都没有 cwd 时，引擎照旧按 CleanCwd("") 得到的 "." 评估，上报的载荷保持空 cwd，不写这个 "."。
// 返回的 Context 带角色；它的 Root() 是生效的项目根（角色地盘外为空），限时放行与上报 hub 都用它。
func (s *Server) evaluate(req proto.Request) (policy.Payload, policy.Decision, policy.Context, proto.Session) {
	p := req.Payload
	cwd := p.Cwd
	if cwd == "" {
		cwd = req.Session.Cwd
	}
	// 只解析一次：两次之间符号链接改了指向时，上报的 cwd 与引擎用的就不一致了
	clean := CleanCwd(cwd)
	reported := p
	if cwd != "" {
		reported.Cwd = clean
	}
	cwd = clean
	p.Cwd = cwd
	root := s.projectRootFor(cwd)
	pctx := policy.Context{ProjectRoot: root, HomeDir: s.cfg.HomeDir, MachineID: s.cfg.MachineID, ProtectedDirs: s.protected}
	pctx.Role = s.sessionRole(&req.Session, pctx.Root(), cwd, true)
	pctx.Role = s.inheritRole(&req, pctx.Role)
	if req.AncestrySource != ancestryUnverified {
		s.recordEffective(req.Session, pctx.Role) // 核实不了祖先链时不动已记下的（含继承的）生效角色
	} else if name, ok := s.effectiveName(req.Session); ok {
		req.Session.Role = name
	}
	return reported, s.rules.Engine().Evaluate(p, pctx), pctx, req.Session
}

// protectedDirs 是守护进程数据目录的绝对路径，外加解析符号链接后的真实路径（不同时才加）；dir 为空返回 nil。
func protectedDirs(dir string) []string {
	if dir == "" {
		return nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil
	}
	out := []string{filepath.Clean(abs)}
	if real, err := filepath.EvalSymlinks(abs); err == nil && real != out[0] {
		out = append(out, real)
	}
	return out
}

// sessionRole 按设计 §3.1 解析会话的角色（钩子带来的 RAILSE_ROLE 在 sess.Role 里），把 sess.Role 改写成生效的角色名
// （未分配、角色文件有误时为空），返回给引擎的角色（未分配为 nil，有误时是 policy.BrokenRole）。
// root 是规范化后的项目根（不含地盘那一步），为空时按 cwd 匹配；cwd 不是绝对路径（钩子没给）时不按目录匹配。
// RAILSE_ROLE 指了不存在的角色时，每个会话在日志里记一行。
// pin 为真（判定审批时）才建立、更新会话的钉住；答题与事件只读取。
func (s *Server) sessionRole(sess *proto.Session, root, cwd string, pin bool) *policy.Role {
	if !filepath.IsAbs(cwd) {
		cwd = ""
	}
	env := sess.Role
	st := s.roles.Current()
	role, why := ResolveRole(st, env, root, cwd, s.cfg.HomeDir)
	if why.EnvUnknown {
		s.noteUnknownRole(*sess, env)
	}
	if st.Err == nil && st.Exists {
		role = s.pinRole(*sess, role, st.Set, pin)
	}
	sess.Role = roleName(role)
	// 答题与事件（pin 为假）没有祖先链：这个会话审批时继承过上层角色的，显示同一个生效角色
	if !pin && st.Err == nil && st.Exists {
		if name, ok := s.effectiveName(*sess); ok {
			sess.Role = name
		}
	}
	return role
}

// rolePin 是一个会话钉住的角色。
type rolePin struct {
	name string
	at   time.Time
}

// 钉住表的上限与过期：满了淘汰最久没用的再存（不放弃钉住：放弃等于让新会话在无角色目录里降为未分配），超过 pinTTL 没用到的
// 在新增时清掉。maxPinned 是变量只为测试能调。
var maxPinned = 4096

const pinTTL = 7 * 24 * time.Hour

// pinRole 按会话钉住角色（见 Server.pinned）。解析顺序：RAILSE_ROLE 指向存在的角色、当前目录命中的角色（role 非空，二者由
// ResolveRole 定）优先，并把钉住更新为它；都没有（当前目录不属于任何角色）时沿用钉住的、仍在文件里的角色，不降为未分配。
// 钉住的角色已从文件里删掉时解除。establish 为假时只读取，不建立、不更新。没有会话 id 的不钉。
func (s *Server) pinRole(sess proto.Session, role *policy.Role, set policy.RoleSet, establish bool) *policy.Role {
	if sess.CLISessionID == "" {
		return role
	}
	key := sess.Tool + "\x00" + sess.CLISessionID
	now := time.Now()
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	if role != nil {
		if establish {
			s.storePinLocked(key, role.Name, now)
		}
		return role
	}
	p, ok := s.pinned[key]
	if !ok {
		return nil
	}
	r := set.Find(p.name)
	if r == nil {
		delete(s.pinned, key) // 角色已从文件里删掉：解除钉住
		return nil
	}
	if establish {
		s.pinned[key] = rolePin{name: p.name, at: now}
	}
	return r
}

// recordEffective 记下会话这次审批的生效角色（含继承）；没有会话 id 的不记。满了淘汰最久没用的。
func (s *Server) recordEffective(sess proto.Session, role *policy.Role) {
	if sess.CLISessionID == "" {
		return
	}
	key := sess.Tool + "\x00" + sess.CLISessionID
	now := time.Now()
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	if role == nil || len(role.Inherited()) == 0 {
		delete(s.effective, key) // 没有继承时答题与事件自己解析，结果与审批一致
		return
	}
	if _, ok := s.effective[key]; !ok && len(s.effective) >= maxPinned {
		oldest, first := "", true
		for k, p := range s.effective {
			if first || p.at.Before(s.effective[oldest].at) {
				oldest, first = k, false
			}
		}
		delete(s.effective, oldest)
	}
	if s.effective == nil {
		s.effective = map[string]rolePin{}
	}
	s.effective[key] = rolePin{name: role.Name, at: now}
}

// effectiveName 返回会话记下的、含继承的生效角色名；没有时 ok 为假。
func (s *Server) effectiveName(sess proto.Session) (string, bool) {
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	p, ok := s.effective[sess.Tool+"\x00"+sess.CLISessionID]
	return p.name, ok
}

// pruneSessions 清掉不在了的会话：会话的 CLI 进程是已知 CLI（见 knownCLI）且已经退出（进程号加启动时间对不上）的，连同它的钉住、生效角色与继承记录；
// 不知道 CLI 进程的条目与取不到进程信息的，超过 pinTTL 没用到才清。到期清理循环每分钟调一次。
func (s *Server) pruneSessions() {
	now := time.Now()
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	for key, p := range s.sessionProc {
		if knownCLI(p) && !procAlive(p) {
			delete(s.sessionProc, key)
			delete(s.pinned, key)
			delete(s.effective, key)
		}
	}
	for k, rec := range s.procRoles {
		if (knownCLI(rec.proc) && !procAlive(rec.proc)) || now.Sub(rec.at) > pinTTL {
			delete(s.procRoles, k)
		}
	}
	for k, p := range s.pinned {
		if now.Sub(p.at) > pinTTL {
			delete(s.pinned, k)
		}
	}
	for k, p := range s.effective {
		if now.Sub(p.at) > pinTTL {
			delete(s.effective, k)
		}
	}
}

// procRole 是一个 CLI 进程上记的角色约束。
type procRole struct {
	names   []string
	session string
	proc    proto.Proc
	at      time.Time
}

// hookShells 是钩子与 CLI 之间常见的中间进程：CLI 多经 sh -c 起钩子，直接父进程是一次性的 shell，不是 CLI 本身。
var hookShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "fish": true, "ksh": true, "tcsh": true, "csh": true, "env": true}

// cliIndex 是祖先链里会话的 CLI 进程：第一个不是 shell、不是包装表里的启动器（timeout、env、nice、nohup、command、exec 等，
// 见 policy.IsWrapperCommand）、也不是 railse 自己的进程；名字都取不到时按直接父进程。钩子经 timeout 10 railse hook … 起来时，
// 最近的非 shell 进程是短命的启动器，拿它当 CLI 会让嵌套会话对不上、清理时误删真正 CLI 的记录。
func cliIndex(chain []proto.Proc) int {
	for i, p := range chain {
		if p.Name == "" {
			return i
		}
		n := filepath.Base(p.Name)
		if !hookShells[n] && !policy.IsWrapperCommand(n) && n != "railse" {
			return i
		}
	}
	return 0
}

// knownCLINames 是能按进程退出清理记录的进程名：各家 CLI 本身与它们的 node 宿主。别的名字（认不出的启动器等）拿不准是不是
// 会话本身，只靠 TTL 清，免得一个短命进程退出就删掉活着的会话的钉住。
var knownCLINames = map[string]bool{"claude": true, "codex": true, "gemini": true, "node": true}

// knownCLI 报告进程是不是已知的 CLI（见 knownCLINames）。
func knownCLI(p proto.Proc) bool { return knownCLINames[strings.ToLower(filepath.Base(p.Name))] }

func procKey(p proto.Proc) string { return fmt.Sprintf("%d@%s", p.PID, p.Start) }

// inheritRole 让嵌套会话继承上层会话的角色约束（设计之外的加固，见 docs/agent-local-loop.md“角色防什么、不防什么”）：
// 请求的祖先链里，在自己的 CLI 进程之上，若有另一个有角色的会话的 CLI 进程（进程号与启动时间都对上，防进程号复用），
// 生效约束取自己与它们中更严的一边（policy.CombineRoles）。然后把这个会话的 CLI 进程连同约束它的角色名记下，供更下层继承
// （三层嵌套靠这一步传递）。没有祖先链（老钩子、取不到）、没有角色文件或文件有误时原样返回。会话的 Role 改写成生效的名字。
func (s *Server) inheritRole(req *proto.Request, role *policy.Role) *policy.Role {
	chain := req.Ancestors
	if len(chain) == 0 {
		return role
	}
	st := s.roles.Current()
	if st.Err != nil || !st.Exists {
		return role
	}
	idx := cliIndex(chain)
	self := chain[idx]
	now := time.Now()
	s.pinMu.Lock()
	defer s.pinMu.Unlock()
	var ups []*policy.Role
	for _, p := range chain[idx+1:] {
		rec, ok := s.procRoles[procKey(p)]
		if !ok {
			continue
		}
		for _, n := range rec.names {
			if r := st.Set.Find(n); r != nil {
				ups = append(ups, r)
			}
		}
	}
	eff := policy.CombineRoles(role, ups)
	if eff != nil && self.Start != "" {
		names := append([]string{}, eff.Inherited()...)
		if role != nil {
			names = append([]string{role.Name}, names...)
		}
		key := procKey(self)
		if _, ok := s.procRoles[key]; !ok && len(s.procRoles) >= maxPinned {
			oldest, first := "", true
			for k, p := range s.procRoles {
				if first || p.at.Before(s.procRoles[oldest].at) {
					oldest, first = k, false
				}
			}
			delete(s.procRoles, oldest)
		}
		if s.procRoles == nil {
			s.procRoles = map[string]procRole{}
		}
		s.procRoles[key] = procRole{names: names, session: req.Session.Tool + "\x00" + req.Session.CLISessionID, proc: self, at: now}
	}
	if req.Session.CLISessionID != "" && self.Start != "" {
		if s.sessionProc == nil {
			s.sessionProc = map[string]proto.Proc{}
		}
		if _, ok := s.sessionProc[req.Session.Tool+"\x00"+req.Session.CLISessionID]; ok || len(s.sessionProc) < maxPinned {
			s.sessionProc[req.Session.Tool+"\x00"+req.Session.CLISessionID] = self
		}
	}
	req.Session.Role = roleName(eff)
	return eff
}

// storePinLocked 记下会话钉住的角色。新会话进表前清掉过期的；还是满的就淘汰最久没用的那个。调用方持 pinMu。
func (s *Server) storePinLocked(key, name string, now time.Time) {
	if _, ok := s.pinned[key]; !ok {
		for k, p := range s.pinned {
			if now.Sub(p.at) > pinTTL {
				delete(s.pinned, k)
			}
		}
		for len(s.pinned) >= maxPinned && len(s.pinned) > 0 {
			oldest, first := "", true
			for k, p := range s.pinned {
				if first || p.at.Before(s.pinned[oldest].at) {
					oldest, first = k, false
				}
			}
			delete(s.pinned, oldest)
		}
	}
	if s.pinned == nil {
		s.pinned = map[string]rolePin{}
	}
	s.pinned[key] = rolePin{name: name, at: now}
}

// noteUnknownRole 在日志里说一次：这个会话的 RAILSE_ROLE 指的角色不存在，按目录绑定。来自钩子的字段截断后按 %q 记。
func (s *Server) noteUnknownRole(sess proto.Session, env string) {
	key := sess.Tool + "\x00" + sess.CLISessionID
	s.nonGitMu.Lock()
	_, seen := s.unknownRoleLogged[key]
	first := !seen && len(s.unknownRoleLogged) < maxNonGitLogged
	if first {
		if s.unknownRoleLogged == nil {
			s.unknownRoleLogged = map[string]struct{}{}
		}
		s.unknownRoleLogged[key] = struct{}{}
	}
	s.nonGitMu.Unlock()
	if first {
		s.log.Printf("会话 %q 的 RAILSE_ROLE=%q 在 roles.yaml 里没有，按项目目录绑定角色", clipText(sess.CLISessionID, hubLogFieldMax), clipText(env, hubLogFieldMax))
	}
}

// projectRootFor 从清理后的 cwd 算项目根（审批与提问共用）：相对路径（含空 cwd 清理后的 "."）会按守护进程自己的工作目录解析，
// 不能拿来找项目根，返回空；绝对路径按 ResolveProjectRoot 定，并按各类别用 noteRoot 在日志里说明一次。只算路径，不跑引擎、不看规则。
func (s *Server) projectRootFor(cwd string) string {
	if !filepath.IsAbs(cwd) {
		return ""
	}
	root, kind := ResolveProjectRoot(cwd)
	switch kind {
	case RootCwd:
		s.noteRoot(root, kind)
	case RootNotPlain, RootSystem:
		s.noteRoot(cwd, kind)
	}
	return root
}

// noteRoot 在日志里说明目录 dir 的项目根怎么定：不在 git 仓库里时 RootCwd 按当前目录当项目根，RootNotPlain 不当项目根；
// RootSystem 是 dir 在系统目录里，git 仓库也不当项目根。
// 三种说明共用一个集合，同一目录只说明一次；记住的目录满了（maxNonGitLogged）就不再说明新目录，免得每条请求都记一行。
// 路径来自钩子，按 %q 记，换行伪造不出另一行。
func (s *Server) noteRoot(dir string, kind RootKind) {
	s.nonGitMu.Lock()
	_, seen := s.nonGitLogged[dir]
	first := !seen && len(s.nonGitLogged) < maxNonGitLogged
	if first {
		if s.nonGitLogged == nil {
			s.nonGitLogged = map[string]struct{}{}
		}
		s.nonGitLogged[dir] = struct{}{}
	}
	s.nonGitMu.Unlock()
	if !first {
		return
	}
	switch kind {
	case RootCwd:
		s.log.Printf("目录 %q 不是 git 仓库，按当前目录当项目根；建议 git init", dir)
	case RootSystem:
		s.log.Printf("目录 %q 在系统目录里，不按项目根处理；要自动放行请换到系统目录之外的项目目录", dir)
	default:
		s.log.Printf("目录 %q 不在家目录下的普通目录里，不按项目根处理；要自动放行请 git init 或换到项目目录", dir)
	}
}

// handlePermission 是审批主路径：引擎能定就定，定不了交给 Decider。
// 角色（0.3 起）：命中禁用（Forbidden）直接拒，decided_by role:<name>；越界（BeyondRole）跳过限时放行（包括 RAILSE_TRUST），
// PreToolUse 来的一律回 ask（不交 Claude 自己的分类器，它可能自动放行），PermissionRequest 来的送 Decider 并标明越界。
func (s *Server) handlePermission(ctx context.Context, req proto.Request) proto.Response {
	id := NewID()
	reported, d, pctx, sess := s.evaluate(req)
	req.Session = sess
	// 取不到对端凭据、又有角色文件：判断不了有没有上层会话的角色，这条按越界处理（拒绝照旧拒绝）
	if req.AncestrySource == ancestryUnverified && d.Action != policy.ActionDeny && s.roles.Current().Exists {
		d.Action, d.BeyondRole, d.Reason = policy.ActionAsk, true, unverifiedReason
	}
	// 0.3 新接进 PreToolUse 的工具（MCP、WebSearch）只为角色而来：会话没有生效角色（没有角色文件，或未分配且没继承）时
	// 一律忽略，与 0.2.3 完全相同（那时钩子根本不触发）：不写审计、不送 hub、钩子不输出
	if pctx.Role == nil && req.Source == proto.SourcePreToolUse && roleOnlyPreToolUse(req.ToolName) {
		return proto.Response{V: proto.Version, Decision: "passthrough", DecidedBy: "ignored"}
	}
	root := pctx.Root() // 生效的项目根：角色地盘外为空，限时放行与上报 hub 都用它
	redacted := redact.Payload(reported)
	entry := AuditEntry{TS: time.Now(), RequestID: id, Kind: "permission", Session: req.Session, ToolName: req.ToolName, Payload: &redacted, Risk: d.Risk.String(), RuleID: d.RuleID, Reason: d.Reason,
		Source: req.Source, PermissionMode: req.PermissionMode, ToolUseID: req.ToolUseID, Role: d.Role, BeyondRole: d.BeyondRole, Ancestry: req.AncestrySource}
	if !(len(req.Ancestors) > 0 || req.AncestrySource == ancestryUnverified) {
		entry.Ancestry = ""
	}
	resp := proto.Response{V: proto.Version, RequestID: id, Risk: d.Risk.String(), RuleID: d.RuleID, Reason: d.Reason}
	switch d.Action {
	case policy.ActionAllow:
		entry.Action, entry.State, entry.DecidedBy = "allow", "auto_allowed", "rule:"+d.RuleID
		resp.Decision, resp.DecidedBy = "allow", entry.DecidedBy
	case policy.ActionDeny:
		entry.Action, entry.State, entry.DecidedBy = "deny", "auto_denied", "rule:"+d.RuleID
		if d.Forbidden {
			entry.DecidedBy = "role:" + d.Role
		}
		resp.Decision, resp.DecidedBy = "deny", entry.DecidedBy
	default:
		// 必须人来定而又问不到人：Gemini 的钩子只能拒绝不能放行，交回终端等于继续执行（yolo、headless 下直接跑），也不进 hub。
		// 越界（含 roles.yaml 有误）与触及 Railse 自己的配置都必须人来定，这里问不到人，只能拒；暂停也不例外。
		// 其余来源能强制问人，见下与 docs/agent-local-loop.md。
		if (d.BeyondRole || d.ControlPlane) && strings.EqualFold(req.Session.Tool, "gemini") {
			entry.Action, entry.State = "deny", "auto_denied"
			if d.BeyondRole {
				entry.DecidedBy, entry.Reason = beyondDecidedBy(d), reasonGeminiBeyond(d)
			} else {
				entry.DecidedBy, entry.Reason = "control_plane", reasonGeminiControl
			}
			resp.Decision, resp.DecidedBy, resp.Reason = "deny", entry.DecidedBy, entry.Reason
			break
		}
		if s.rules.Paused() && !d.BeyondRole && !d.ControlPlane {
			// 越界与触及 Railse 配置的都不归暂停管（见下）。
			// 总开关打开：用户 deny 与角色禁用仍由引擎处理，其余一律交回终端，不去打扰 hub。
			// 越界（含 roles.yaml 有误）不归暂停管：交回终端在 auto、acceptEdits、bypassPermissions 下等于放行，
			// 它照下面的路走（PreToolUse 回 ask 强制终端确认，其余送人工决定），暂停只停自动决定、不放宽边界。
			// 只管审批：答题（handleAsk）不走这里，不看暂停开关，照样送 hub。
			entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "paused", "自动审批已暂停，交回终端处理"
			resp.Decision, resp.DecidedBy, resp.Reason = "passthrough", entry.DecidedBy, entry.Reason
			break
		}
		// 限时放行：只改引擎定不了的这一档（规则的 allow、deny 在上面已定，用户 deny 压过放行；暂停压过放行），
		// 命中就本地放行，不弹终端框、不送手机。RAILSE_TRUST 只看 PreToolUse 带来的（Claude 每次调用先经它）。
		// 越界的请求不碰放行：既不按 RAILSE_TRUST 建，也不匹配；角色的 trust_max 再限一道（见 trustAllowedFor）。
		// 读写 Railse 自己的配置（d.ControlPlane）也不走放行：放行期间 agent 改规则、角色、放行表等于改掉管它自己的东西。
		// 本来会命中放行时，理由换成 reasonControlPlane，审计、终端确认框与手机上都看得到为什么这次没放。
		if !d.BeyondRole {
			if req.TrustFor != "" && req.Source == proto.SourcePreToolUse {
				s.trustFromEnv(req)
			}
			now := s.trusts.clock()
			tr, ok := s.trusts.matchFiltered(req.Session, root, func(tr proto.Trust) bool { return trustAllowedFor(pctx.Role, tr, now) })
			if ok && d.ControlPlane {
				ok = false
				d.Reason = reasonControlPlane
				entry.Reason, resp.Reason = reasonControlPlane, reasonControlPlane
			}
			if ok {
				entry.Action, entry.State, entry.DecidedBy = "allow", "auto_allowed", "trust:"+tr.ID
				entry.Reason = fmt.Sprintf("限时放行（%s，剩 %s）", trustScopeLabel(tr), formatRemain(tr.Until.Sub(now)))
				resp.Decision, resp.DecidedBy, resp.Reason = "allow", entry.DecidedBy, reasonTrusted
				break
			}
		}
		if req.Source == proto.SourcePreToolUse {
			// 本来会命中限时放行、因触及 Railse 配置而没放的（d.Reason 为 reasonControlPlane）也回 ask：交给 Claude 自己的流程
			// 在 auto、bypassPermissions 下等于放行，放行本来要替人做的决定得交回人
			if d.BeyondRole || d.ControlPlane || d.Risk >= policy.RiskOutward || s.settings.Load().ClaudeUndecided == UndecidedPhone {
				// 硬底线、越界（或设置成一律送手机）：不在这里等手机。回 ask 让 Claude 弹终端确认框并触发 PermissionRequest，
				// 那一条按老路送 hub；终端里同时能看到命令与触发点，谁先答谁算。不看 hub 状态：hub 不可达时
				// PermissionRequest 交回终端，确认框还在（见 docs/superpowers/specs/2026-11-02-railse-opc-v0.2.2-two-screens-design.md §2.1）。
				// 越界时理由换成角色的那句（钩子按终端语言重拼后放进确认框）。
				reason := reasonAsked
				if d.BeyondRole || d.Reason == reasonControlPlane || d.Reason == "触及 Railse 自己的配置，不接受自动放行" {
					reason = d.Reason
				}
				entry.Action, entry.State, entry.DecidedBy, entry.Reason, entry.Trigger = "ask", "asked", decidedByAsk, reason, redactTrigger(d.Trigger)
				resp.Decision, resp.DecidedBy, resp.Reason, resp.Trigger = "ask", decidedByAsk, reason, entry.Trigger
				break
			}
			// 中间三级不看模式，先让 Claude 自己的 allow 列表与分类器（auto 模式）处理，Railse 不比它更吵；
			// Claude 若决定要问人，会再来一条 PermissionRequest，那时照常送手机。
			entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "classifier", "交给 Claude 自己的权限流程"
			resp.Decision, resp.DecidedBy, resp.Reason = "passthrough", entry.DecidedBy, entry.Reason
			break
		}
		entry.Trigger = redactTrigger(d.Trigger)
		return s.waitDecision(ctx, id, req, d, root, redacted, entry)
	}
	s.writeAudit(entry)
	return resp
}

// beyondDecidedBy 是越界被拒时的 decided_by：role:<角色名>；roles.yaml 有误（没有角色名）时为 roles_yaml。
func beyondDecidedBy(d policy.Decision) string {
	if d.Role == "" {
		return "roles_yaml"
	}
	return "role:" + d.Role
}

// reasonGeminiControl 是 Gemini 触及 Railse 自己的配置被拒时的理由（固定句子，钩子 i18n.Lookup）。
const reasonGeminiControl = "触及 Railse 自己的配置，Gemini 无法在这里问人：在别处执行"

// reasonGeminiBeyond 是 Gemini 越界被拒时给模型看的理由。格式固定：钩子按终端语言重拼（见 cmd/railse 的 localizeRoleReason）。
func reasonGeminiBeyond(d policy.Decision) string {
	if d.Role == "" {
		return "roles.yaml 有误，Gemini 无法在这里问人：修好文件后重试，或在别处执行"
	}
	return "超出角色「" + d.Role + "」的边界，Gemini 无法在这里问人：换个角色，或在别处执行"
}

// roleOnlyPreToolUse 报告工具是不是 0.3 只为角色接进 Claude PreToolUse 的（WebSearch 与 MCP 工具，见 hooksconf.ClaudePreToolUseMatcher）。
func roleOnlyPreToolUse(tool string) bool {
	return tool == "WebSearch" || strings.HasPrefix(tool, "mcp__")
}

// redactTrigger 对触发段脱敏（它是命令原文的一部分，可能带凭据），返回副本；nil 原样。
func redactTrigger(t *policy.Trigger) *policy.Trigger {
	if t == nil {
		return nil
	}
	return &policy.Trigger{Segment: redact.String(t.Segment), Why: t.Why}
}

// waitDecision 登记审批请求并等 Decider（见 await），把结论映射成 allow、deny 或交回终端。
// projectRoot、redacted 与引擎理由随请求登记，hub 客户端上报时只用脱敏后的载荷。
func (s *Server) waitDecision(ctx context.Context, id string, req proto.Request, d policy.Decision, projectRoot string, redacted policy.Payload, entry AuditEntry) proto.Response {
	pr := &PendingRequest{ID: id, Kind: "permission", Req: req, Risk: d.Risk, ProjectRoot: projectRoot, Redacted: redacted, EngineReason: d.Reason, Trigger: entry.Trigger,
		BeyondRole: d.BeyondRole}
	out, resp, ok := s.await(ctx, pr, entry, proto.Response{V: proto.Version, RequestID: id, Risk: d.Risk.String()})
	if !ok {
		return resp
	}
	applyOutcome(out, out.Action == "allow" || out.Action == "deny", &resp, &entry)
	if d.Reason == reasonControlPlane {
		entry.Reason = joinReason(reasonControlPlane, entry.Reason) // 审计里留下这次为什么没走放行
	}
	s.writeAudit(entry)
	return resp
}

// reasonControlPlane 是本来会命中限时放行、但请求读写 Railse 自己的配置而没放时的理由。固定句子：钩子按终端语言 i18n.Lookup。
const reasonControlPlane = "限时放行不覆盖 Railse 自己的配置"

// joinReason 把两句理由用“；”接起来，后一句为空时只留前一句。
func joinReason(a, b string) string {
	if b == "" {
		return a
	}
	return a + "；" + b
}

// handleAsk 是答题主路径：不跑引擎、不看规则与暂停开关（答题不是审批，也不能记住）。题目脱敏后登记、交给 Decider 等手机上作答；
// hub 与手机只见到脱敏后的题面，答案按题目序号换回原题面写给钩子。请求不合规、脱敏后题面重复或超出上限、答案不完整都交回终端。
func (s *Server) handleAsk(ctx context.Context, req proto.Request) proto.Response {
	if err := adapter.ValidateAsk(req.Ask); err != nil {
		s.log.Printf("答题请求不合规，交回终端: %v", err)
		return proto.Response{V: proto.Version, Decision: "passthrough", Reason: "请求格式错误: " + err.Error()}
	}
	id := NewID()
	// 答题不经判定，只带上生效的角色名（设计 §3.4）；项目根随上报帧带给 hub，推送标题与收件箱靠它显示项目名
	cwd := CleanCwd(req.Session.Cwd)
	root := s.projectRootFor(cwd)
	s.sessionRole(&req.Session, policy.Context{ProjectRoot: root, HomeDir: s.cfg.HomeDir}.Root(), cwd, false)
	red := redactAsk(req.Ask)
	entry := AuditEntry{TS: time.Now(), RequestID: id, Kind: "ask", Session: req.Session, ToolName: proto.AskToolName, Ask: red}
	resp := proto.Response{V: proto.Version, RequestID: id}
	if !distinctQuestions(red) {
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "error", "脱敏后有题面相同的题，分不清答的是哪道，交回终端处理"
		resp.Decision, resp.Reason = "passthrough", entry.Reason
		s.writeAudit(entry)
		return resp
	}
	// 脱敏会让短值变长（token=x 变成 token=<redacted>）：贴着上限的题脱敏后 hub 会拒收（它按同一套上限校验收到的题），在本机就交回终端
	if err := adapter.ValidateAsk(red); err != nil {
		s.log.Printf("答题请求 %q 脱敏后题目超出上限，交回终端: %v", id, err)
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "error", "脱敏后题目超出上限（"+err.Error()+"），交回终端处理"
		resp.Decision, resp.Reason = "passthrough", entry.Reason
		s.writeAudit(entry)
		return resp
	}
	pr := &PendingRequest{ID: id, Kind: "ask", Ask: red, Req: req, ProjectRoot: root}
	out, resp, ok := s.await(ctx, pr, entry, resp)
	if !ok {
		return resp
	}
	if applyOutcome(out, out.Action == "answer", &resp, &entry) == "answer" {
		answers, err := originalAnswers(req.Ask, red, out.Answers)
		if err != nil {
			s.log.Printf("答题请求 %q 的答案不合规（%v），交回终端", id, err)
			entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "error", "答案不合规: "+err.Error()
			resp.Decision, resp.Reason = "passthrough", "决定来源给的答案不完整，交回终端处理"
		} else {
			resp.Answers, entry.Answers = answers, auditAnswers(red, out.Answers)
		}
	}
	s.writeAudit(entry)
	return resp
}

// await 登记未完成请求 pr 并等 Decider，期限取配置与请求两者中较短的（pr 的 Created、Deadline 由这里填）。
// Decider 给出结论时返回它、resp 与 true，由调用方按请求种类映射并写审计。没给出结论时（钩子消失、守护进程停止、等待过期、出错）
// 按审批与答题共用的口径写好审计 entry，返回填好的 resp（交回终端）与 false；钩子已消失时返回空答复，不写回。
func (s *Server) await(ctx context.Context, pr *PendingRequest, entry AuditEntry, resp proto.Response) (Outcome, proto.Response, bool) {
	wait := s.cfg.Deadline
	if pr.Req.DeadlineSeconds > 0 && time.Duration(pr.Req.DeadlineSeconds)*time.Second < wait {
		wait = time.Duration(pr.Req.DeadlineSeconds) * time.Second
	}
	now := time.Now()
	pr.Created, pr.Deadline = now, now.Add(wait)
	// 取消句柄存进待决表：tool_done、done、session_end 事件靠它以 errSettledElsewhere 取消（见 handleEvent）
	sctx, settle := context.WithCancelCause(ctx)
	defer settle(nil)
	pr.settle = settle
	s.pending.Add(pr)
	defer s.pending.Remove(pr.ID)
	dctx, cancel := context.WithDeadline(sctx, pr.Deadline)
	defer cancel()
	out, err := s.decider.Decide(dctx, *pr)
	switch {
	case err == nil:
		return out, resp, true
	case errors.Is(context.Cause(ctx), errHookGone):
		entry.State, entry.DecidedBy, entry.Reason = "cancelled", "terminal", "钩子进程在决定前退出"
		s.writeAudit(entry)
		return Outcome{}, proto.Response{}, false // 对方已不在，不写回
	case errors.Is(context.Cause(sctx), errSettledElsewhere):
		// 本机已经处理掉了（宿主自己裁决、工具已跑完，或这一轮已结束）：钩子要收到答复才会退出，回 passthrough；
		// 宿主已经裁决过，不会再弹框。HubDecider 据同一原因告诉 hub cancel(terminal)。
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "cancelled", "terminal", reasonSettledElsewhere
		resp.Decision, resp.DecidedBy, resp.Reason = "passthrough", entry.DecidedBy, entry.Reason
	case ctx.Err() != nil:
		// 守护进程正在停止，钩子还在等：明确交回终端。
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "shutdown", "守护进程正在停止，交回终端处理"
		resp.Decision, resp.DecidedBy, resp.Reason = "passthrough", entry.DecidedBy, entry.Reason
	case errors.Is(dctx.Err(), context.DeadlineExceeded):
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "expired", "timeout", "等待过期，交回终端处理"
		resp.Decision, resp.DecidedBy, resp.Reason = "passthrough", entry.DecidedBy, entry.Reason
	default:
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "error", err.Error()
		resp.Decision, resp.DecidedBy, resp.Reason = "passthrough", entry.DecidedBy, "决定来源出错，交回终端处理"
	}
	s.writeAudit(entry)
	return Outcome{}, resp, false
}

// applyOutcome 把 Decider 的结论 out 写进答复与审计，返回最终动作。valid 报告 out.Action 对这类请求是否有效：
// 空的或不认识的结论按不作决定处理，钩子总能收到答复；hub 不可达时不可能有决定，只能交回终端，审计记 expired。
// 备注只进审计，不写进 resp：钩子与模型看不到手机上的自由文本。答案由调用方处理。
func applyOutcome(out Outcome, valid bool, resp *proto.Response, entry *AuditEntry) string {
	action := out.Action
	unreachable := out.DecidedBy == hubUnreachable
	if !valid || unreachable {
		action = "passthrough"
	}
	resp.Decision, resp.Reason, resp.RuleID, resp.DecidedBy = action, out.Reason, out.RuleID, out.DecidedBy
	entry.Action, entry.DecidedBy, entry.Reason, entry.RuleID, entry.Note = action, out.DecidedBy, out.Reason, out.RuleID, out.Note
	switch {
	case unreachable:
		entry.State = "expired"
	case action == "allow":
		entry.State = "approved"
	case action == "deny":
		entry.State = "denied"
	case action == "answer":
		entry.State = "answered"
	default:
		entry.State = "passthrough"
	}
	return action
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

// handleEvent 记录只展示的事件。同一会话有未完成的审批或答题时压掉 question，避免一件事进两条。
// 消息先按文本语境脱敏再写审计：Gemini 的 ToolPermission 通知会拼进完整命令，
// last_assistant_message、prompt_response 也可能原样带出凭据。
func (s *Server) handleEvent(ev proto.Event) proto.EventAck {
	switch ev.Kind {
	case "tool_done":
		// 工具已经跑完：同一调用还挂着的审批不用等了。按会话 + 工具 + 载荷对上（PermissionRequest 没有 tool_use_id）。
		// 事件本身不写审计（每次工具调用一条太多），只记它引起的取消。
		if ev.Payload != nil {
			if n := s.pending.CancelMatching(ev.Session.CLISessionID, payloadKey(ev.ToolName, *ev.Payload), errSettledElsewhere); n > 0 {
				s.log.Printf("会话 %q 的工具已跑完，取消 %d 条挂着的审批", clipText(ev.Session.CLISessionID, hubLogFieldMax), n)
			}
		}
		return proto.EventAck{V: proto.Version, OK: true}
	case "done", "session_end":
		// 这一轮或会话已经结束：这个会话里挂着的审批与答题都不会再有人等，取消掉
		if n := s.pending.CancelSession(ev.Session.CLISessionID, errSettledElsewhere); n > 0 {
			s.log.Printf("会话 %q 的这一轮已结束，取消 %d 条挂着的请求", clipText(ev.Session.CLISessionID, hubLogFieldMax), n)
		}
		// session_end 不释放钉住与继承记录：它可以伪造（agent 在一次 Bash 调用里给钩子喂一条，填上层会话的 id），
		// 拿不准是不是真的会话结束，就不释放；清理看进程是否还活着（见 pruneSessions）
	}
	// 事件只带上生效的角色名（设计 §3.4）
	cwd := CleanCwd(ev.Session.Cwd)
	if filepath.IsAbs(cwd) {
		s.sessionRole(&ev.Session, policy.Context{ProjectRoot: s.projectRootFor(cwd), HomeDir: s.cfg.HomeDir}.Root(), cwd, false)
	} else {
		s.sessionRole(&ev.Session, "", "", false)
	}
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
		ok.Status = s.status()
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
		// 回复带上状态：hub 仍暂停时本机 resume 不会解除生效的暂停，CLI 据此提示。
		ok.Status = s.status()
		return ok
	case "reload":
		if err := s.rules.Reload(); err != nil {
			return proto.ControlReply{V: proto.Version, OK: false, Error: err.Error()}
		}
		if err := s.ReloadSettings(); err != nil {
			return proto.ControlReply{V: proto.Version, OK: false, Error: "规则已重载；本机设置读不了，按默认值: " + err.Error()}
		}
		if st := s.roles.Reload(); st.Err != nil {
			return proto.ControlReply{V: proto.Version, OK: false, Error: "规则已重载；roles.yaml 有误，只读之外的审批一律问人: " + st.Err.Error()}
		}
		s.writeAudit(AuditEntry{TS: time.Now(), Kind: "control", State: "recorded", Message: "reload"})
		return ok
	case "trust_set":
		if c.Trust == nil {
			return proto.ControlReply{V: proto.Version, OK: false, Error: "缺少要建的放行"}
		}
		tr := *c.Trust
		if tr.Project != "" {
			// 与审批时算项目根同一口径（解析符号链接），否则 macOS 上 /var 与 /private/var 对不上
			tr.Project = CleanCwd(tr.Project)
		}
		if tr.CreatedBy == "" {
			tr.CreatedBy = "cli"
		}
		tr, err := s.trusts.add(tr)
		if tr.ID == "" {
			return proto.ControlReply{V: proto.Version, OK: false, Error: err.Error()}
		}
		// 落盘失败也已生效（与 pause 一样），写审计、通知，回复 OK:false 说明重启后不会保留
		s.trustEvent("trust_set", tr)
		if err != nil {
			s.log.Printf("写放行表: %v", err)
			return proto.ControlReply{V: proto.Version, OK: false, Error: "已放行，但未能持久化: " + err.Error(), Trusts: []proto.Trust{tr}}
		}
		ok.Trusts = []proto.Trust{tr}
		return ok
	case "trust_list":
		ok.Trusts = s.trusts.list()
		return ok
	case "trust_revoke":
		gone := s.trusts.revoke(c.TrustID)
		if len(gone) == 0 && c.TrustID != "all" {
			return proto.ControlReply{V: proto.Version, OK: false, Error: "没有这条放行"}
		}
		for _, tr := range gone {
			s.trustEvent("trust_revoke", tr)
		}
		ok.Trusts = s.trusts.list()
		return ok
	case "role_list":
		ok.Roles = s.rolesInfo(s.roles.Current())
		return ok
	case "role_which":
		st := s.roles.Current()
		ok.Roles = s.rolesInfo(st)
		w := WhichRole(st, c.Role, c.Cwd, s.cfg.HomeDir)
		ok.Roles.Which = &w
		return ok
	default:
		return proto.ControlReply{V: proto.Version, OK: false, Error: "未知的管理命令 " + c.Op}
	}
}

// rolesInfo 把角色文件的状态编成 role_list、role_which 的答复。
func (s *Server) rolesInfo(st RolesState) *proto.RolesInfo {
	return RolesInfoOf(s.roles.Path(), st)
}

// status 汇总当前状态；三个暂停字段在 pauseMu 内一次读出，彼此一致。
func (s *Server) status() *proto.Status {
	s.pauseMu.Lock()
	paused, local, hub := s.rules.Paused(), s.localPaused, s.hubPaused
	s.pauseMu.Unlock()
	hubState, hubURL := "none", ""
	if s.hubStatus != nil {
		hubState, hubURL = s.hubStatus()
	}
	return &proto.Status{Paused: paused, LocalPaused: local, HubPaused: hub, Rules: s.rules.Count(), Pending: s.pending.Count(), MachineID: s.cfg.MachineID, RulesPath: s.rules.Path(), Uptime: time.Since(s.start).Round(time.Second).String(),
		Hub: hubState, HubURL: hubURL, Trusts: len(s.trusts.list())}
}

// SetHubPaused 设置 hub 下发的总开关（hub 客户端收到 welcome、paused 帧时调用）。只改 hub 这一侧，
// 本机的 pause/resume 不受影响，生效值取两者的或。值没变时什么都不做；变化时写 state.json
// 和审计（kind control，decided_by hub）。落盘失败也照样切换并写审计，返回错误说明重启后不会保留。
// 写审计会同步调用 AuditSink.Forward：调用方（hub 客户端）不得持着 Forward 需要的锁调用本方法，否则自锁。
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

// HubPaused 报告 hub 那一侧的总开关（不含本机的 pause）。
func (s *Server) HubPaused() bool {
	s.pauseMu.Lock()
	defer s.pauseMu.Unlock()
	return s.hubPaused
}
