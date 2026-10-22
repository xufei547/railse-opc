package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/wire"
)

const (
	helloTimeout       = 5 * time.Second  // 连上后等 hello 的上限
	agentWriteTimeout  = 10 * time.Second // 写出一帧的上限
	agentQueueLen      = 256              // 每条连接的发送队列长度
	maxAgentVersionLen = 64               // hello 里 agent_version 的字节上限；home_dir 沿用 maxHomeDirLen
)

// agentConn 是一台机器的活动连接。conn 只由 handleAgentWS 及其写 goroutine 使用；
// 别处只经 out 排队发帧（agentHub.send）、经 close 要求断开。
type agentConn struct {
	machine store.Machine
	conn    *websocket.Conn
	out     chan []byte
	closed  chan struct{}
	once    sync.Once
}

// close 要求断开这条连接：只关 closed，不碰 conn，不阻塞，可重复调用。关闭握手由连接自己的写 goroutine 完成，
// 调用方（例如登记新连接时替换旧连接）不会被不响应的对端拖住。
func (c *agentConn) close() { c.once.Do(func() { close(c.closed) }) }

// agentHub 按机器 id 保存连接，每台机器只保留最新的一条。
type agentHub struct {
	mu    sync.Mutex
	conns map[string]*agentConn
	log   *log.Logger
}

func newAgentHub(logger *log.Logger) *agentHub {
	return &agentHub{conns: map[string]*agentConn{}, log: logger}
}

// register 登记连接；同一机器已有连接时关掉旧的。
func (h *agentHub) register(c *agentConn) {
	h.mu.Lock()
	old := h.conns[c.machine.ID]
	h.conns[c.machine.ID] = c
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
}

// unregister 注销并关闭连接。只有表里仍是这条连接时才删除：被替换的旧连接退出时不会删掉新连接。
func (h *agentHub) unregister(c *agentConn) {
	h.mu.Lock()
	if h.conns[c.machine.ID] == c {
		delete(h.conns, c.machine.ID)
	}
	h.mu.Unlock()
	c.close()
}

// send 把帧排进机器的发送队列，从不阻塞。未连接、连接已关闭、编码失败返回 false；
// 队列满说明对端读不动，记日志、断开它（重连后由 hello 对齐、补发）并返回 false。
func (h *agentHub) send(machineID string, frame any) bool {
	b, err := wire.Marshal(frame)
	if err != nil {
		h.log.Printf("编码发往机器 %s 的帧: %v", machineID, err)
		return false
	}
	h.mu.Lock()
	c := h.conns[machineID]
	h.mu.Unlock()
	if c == nil {
		return false
	}
	select {
	case <-c.closed:
		return false
	default:
	}
	select {
	case c.out <- b:
		return true
	default:
		h.log.Printf("机器 %s（%s）的发送队列已满，断开连接", c.machine.Name, machineID)
		c.close()
		return false
	}
}

func (h *agentHub) connected(machineID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[machineID]
	return ok
}

func (h *agentHub) connectedIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.conns))
	for id := range h.conns {
		ids = append(ids, id)
	}
	return ids
}

// closeAll 清空连接表并要求每条连接断开（hub 关停时用）。
func (h *agentHub) closeAll() {
	h.mu.Lock()
	conns := make([]*agentConn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.conns = map[string]*agentConn{}
	h.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// handleAgentWS 是守护进程的入口：Bearer 机器密钥认证 → 升级 → 读 hello 并校验 → 刷新机器 → 登记 →
// 发 welcome → onAgentHello → 读循环分发；写 goroutine 负责发帧、ping 与 pong 超时。
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	machine, err := s.authMachine(r)
	if err != nil {
		// 守护进程把 401 当作密钥永久失效，存储出错只能回 500
		if errors.Is(err, errNoBearer) || errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusUnauthorized, "机器密钥无效")
		} else {
			s.internalErr(w, "认证机器", err)
		}
		return
	}
	// 守护进程不是浏览器，认证靠 Authorization 头（跨站页面发不出），不做 Origin 检查
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return // Accept 已回了 HTTP 错误
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wire.MaxFrame) // 库默认只有 32768 字节
	hello, ok := s.readHello(r.Context(), conn, machine)
	if !ok {
		return
	}
	home := machine.HomeDir // hello 没报家目录时保留接入时登记的
	if hello.HomeDir != "" {
		home = hello.HomeDir
	}
	if err := s.store.TouchMachine(machine.ID, hello.AgentVersion, home); err != nil {
		s.log.Printf("刷新机器 %s: %v", machine.ID, err)
	}
	machine.HomeDir, machine.AgentVersion = home, hello.AgentVersion

	c := &agentConn{machine: machine, conn: conn, out: make(chan []byte, agentQueueLen), closed: make(chan struct{})}
	// 先登记再读快照、写 welcome：登记之后的规则或暂停变更，广播一定排进 out、在 welcome 之后写出；
	// 登记之前的变更已在快照里。welcome 永远是第一帧，也不会漏掉变更。
	s.agents.register(c)
	defer s.agents.unregister(c)
	welcome, err := s.welcomeFrame()
	if err != nil {
		s.log.Printf("组装 welcome: %v", err)
		conn.Close(websocket.StatusInternalError, "hub 内部错误")
		return
	}
	if writeAgentFrame(r.Context(), conn, welcome) != nil {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	start := time.Now()
	var lastPong atomic.Int64 // 最近一次 pong 距 start 的纳秒数（单调时钟）
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer cancel() // 写 goroutine 结束（写失败、pong 超时、被要求断开）时读循环随之结束
		s.agentWriteLoop(ctx, c, start, &lastPong)
	}()
	defer func() {
		cancel()
		<-writerDone
	}()
	s.onAgentHello(machine, hello)
	s.agentReadLoop(ctx, c, start, &lastPong)
}

// readHello 在 helloTimeout 内读第一帧并校验：必须是 hello、协议版本与 hub 一致、机器 id 与认证出的机器相同、
// 字段不超长。不合格时关闭连接（协议版本不一致先回 Error 帧并记审计）并返回 false。
func (s *Server) readHello(ctx context.Context, conn *websocket.Conn, m store.Machine) (wire.Hello, bool) {
	hctx, cancel := context.WithTimeout(ctx, helloTimeout)
	_, first, err := conn.Read(hctx)
	cancel()
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "没有收到 hello")
		return wire.Hello{}, false
	}
	var hello wire.Hello
	if k, _ := wire.Kind(first); k != wire.THello || wire.Unmarshal(first, &hello) != nil {
		conn.Close(websocket.StatusPolicyViolation, "第一帧应是 hello")
		return wire.Hello{}, false
	}
	if hello.Proto != wire.Proto {
		s.rejectProto(ctx, conn, m, hello.Proto)
		return wire.Hello{}, false
	}
	reason := ""
	switch {
	case hello.MachineID != m.ID:
		reason = "hello 的机器 id 与密钥不符"
	case len(hello.AgentVersion) > maxAgentVersionLen:
		reason = "agent_version 超长"
	case len(hello.HomeDir) > maxHomeDirLen:
		reason = "home_dir 超长"
	}
	if reason != "" {
		conn.Close(websocket.StatusPolicyViolation, reason)
		return wire.Hello{}, false
	}
	return hello, true
}

// rejectProto 拒绝协议版本不一致的守护进程：记审计，回 Error 帧说明双方版本，再以 PolicyViolation 关闭。
func (s *Server) rejectProto(ctx context.Context, conn *websocket.Conn, m store.Machine, got int) {
	detail, _ := json.Marshal(map[string]int{"agent_proto": got, "hub_proto": wire.Proto}) // map[string]int 编码不会失败
	if err := s.store.InsertAudit(store.AuditEvent{MachineID: m.ID, Actor: "agent", Action: "agent_proto_mismatch", Detail: string(detail)}); err != nil {
		s.log.Printf("记协议版本审计 %s: %v", m.ID, err)
	}
	if b, err := wire.Marshal(wire.Error{T: wire.TError, Message: fmt.Sprintf("协议版本 %d，hub 需要 %d", got, wire.Proto)}); err == nil {
		writeAgentFrame(ctx, conn, b)
	}
	conn.Close(websocket.StatusPolicyViolation, "协议版本不一致")
}

// welcomeFrame 组装 welcome：规则快照（规则与版本号同一事务读出）与全局暂停状态。
func (s *Server) welcomeFrame() ([]byte, error) {
	rs, version, err := s.store.RuleSet()
	if err != nil {
		return nil, fmt.Errorf("读规则: %w", err)
	}
	y, err := rs.Marshal()
	if err != nil {
		return nil, fmt.Errorf("编码规则: %w", err)
	}
	return wire.Marshal(wire.Welcome{T: wire.TWelcome, RulesVersion: version, RulesYAML: string(y), Paused: s.paused(), ServerTime: time.Now().UTC()})
}

func writeAgentFrame(ctx context.Context, conn *websocket.Conn, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, agentWriteTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}

// agentWriteLoop 是连接上唯一的数据写者：写出队列里的帧，每 PingInterval 发 ping，PongTimeout 内没有 pong 就返回；
// 被要求断开（替换、吊销、关停、队列满）时走关闭握手后返回。写失败或 ctx 结束也返回。
func (s *Server) agentWriteLoop(ctx context.Context, c *agentConn, start time.Time, lastPong *atomic.Int64) {
	ping, _ := wire.Marshal(wire.Ping{T: wire.TPing}) // 定长小帧，编码不会失败
	ticker := time.NewTicker(s.cfg.PingInterval)
	defer ticker.Stop()
	pongCheck := time.NewTimer(s.cfg.PongTimeout)
	defer pongCheck.Stop()
	for {
		select {
		case b := <-c.out:
			if writeAgentFrame(ctx, c.conn, b) != nil {
				return
			}
		case <-ticker.C:
			if writeAgentFrame(ctx, c.conn, ping) != nil {
				return
			}
		case <-pongCheck.C:
			idle := time.Since(start) - time.Duration(lastPong.Load())
			if idle >= s.cfg.PongTimeout {
				s.log.Printf("机器 %s（%s）超过 %s 没有 pong，断开", c.machine.Name, c.machine.ID, s.cfg.PongTimeout)
				return
			}
			pongCheck.Reset(s.cfg.PongTimeout - idle)
		case <-c.closed:
			c.conn.Close(websocket.StatusNormalClosure, "hub 断开连接")
			return
		case <-ctx.Done():
			return
		}
	}
}

// agentReadLoop 读守护进程的帧并分发，连接出错或 ctx 结束时返回。解析不了的帧与未知种类忽略。
func (s *Server) agentReadLoop(ctx context.Context, c *agentConn, start time.Time, lastPong *atomic.Int64) {
	m := c.machine
	for {
		_, b, err := c.conn.Read(ctx)
		if err != nil {
			return
		}
		kind, err := wire.Kind(b)
		if err != nil {
			continue
		}
		switch kind {
		case wire.TPong:
			lastPong.Store(int64(time.Since(start)))
		case wire.TPending:
			var p wire.PendingRequest
			if wire.Unmarshal(b, &p) == nil {
				s.onPending(m, p)
			}
		case wire.TCancel:
			var f wire.Cancel
			if wire.Unmarshal(b, &f) == nil {
				s.onCancel(m, f)
			}
		case wire.TExpired:
			var f wire.Expired
			if wire.Unmarshal(b, &f) == nil {
				s.onExpired(m, f)
			}
		case wire.TDelivered:
			var f wire.Delivered
			if wire.Unmarshal(b, &f) == nil {
				s.onDelivered(m, f)
			}
		case wire.TAudit:
			var f wire.Audit
			if wire.Unmarshal(b, &f) == nil {
				s.onAudit(m, f)
			}
		}
	}
}

// paused 读全局总开关（settings 的 paused 为 "1"）。读失败记日志、按未暂停处理。
func (s *Server) paused() bool {
	v, ok, err := s.store.GetSetting("paused")
	if err != nil {
		s.log.Printf("读暂停设置: %v", err)
		return false
	}
	return ok && v == "1"
}

// 以下六个在 Task 8 实现。
func (s *Server) onAgentHello(m store.Machine, h wire.Hello)       {}
func (s *Server) onPending(m store.Machine, p wire.PendingRequest) {}
func (s *Server) onCancel(m store.Machine, c wire.Cancel)          {}
func (s *Server) onExpired(m store.Machine, e wire.Expired)        {}
func (s *Server) onDelivered(m store.Machine, d wire.Delivered)    {}
func (s *Server) onAudit(m store.Machine, a wire.Audit)            {}
