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
	"github.com/railse/railse-opc/internal/policy"
)

const (
	defaultHelloTimeout = 5 * time.Second  // 连上后等 hello 的上限（Server.helloTimeout 的默认值）
	agentWriteTimeout   = 10 * time.Second // 写出一帧的上限
	agentQueueLen       = 256              // 每条连接的发送队列长度
	maxAgentVersionLen  = 64               // hello 里 agent_version 的字节上限；home_dir 沿用 maxHomeDirLen
	protoAuditEvery     = time.Hour        // 同一机器、同一对协议版本的不一致审计至多这么久记一次
	badFrameLogMax      = 5                // 每条连接逐条记日志的坏帧数，之后只计数，断开时汇总
	logFieldMax         = 200              // 日志里来自对端的字段（帧种类、解码错误）的字节上限
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

// isClosed 报告是否已被要求断开（被替换、吊销、关停、队列满）。
func (c *agentConn) isClosed() bool {
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

// protoAuditKey 是协议版本不一致审计的去重键。
type protoAuditKey struct {
	machineID            string
	agentProto, hubProto int
}

// agentHub 按机器 id 保存连接，每台机器只保留最新的一条；顺带记协议版本不一致审计的去重表。
type agentHub struct {
	mu           sync.Mutex
	conns        map[string]*agentConn
	protoAudited map[protoAuditKey]time.Time
	now          func() time.Time
	log          *log.Logger
}

func newAgentHub(logger *log.Logger) *agentHub {
	return &agentHub{conns: map[string]*agentConn{}, protoAudited: map[protoAuditKey]time.Time{}, now: time.Now, log: logger}
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

// outboundKind 给出 hub 允许下发的帧的种类：只有 welcome、decision、rules、paused、ping、pong、error 这几种值类型，
// 且 t 字段与类型相符。纵深防御：hub 没有让机器执行任何东西的帧，别的一律不发。
func outboundKind(frame any) (string, bool) {
	var want, got string
	switch f := frame.(type) {
	case wire.Welcome:
		want, got = wire.TWelcome, f.T
	case wire.Decision:
		want, got = wire.TDecision, f.T
	case wire.Rules:
		want, got = wire.TRules, f.T
	case wire.Paused:
		want, got = wire.TPaused, f.T
	case wire.Ping:
		want, got = wire.TPing, f.T
	case wire.Pong:
		want, got = wire.TPong, f.T
	case wire.Error:
		want, got = wire.TError, f.T
	default:
		return "", false
	}
	return want, got == want
}

// send 把帧排进机器当前连接的发送队列，从不阻塞。不在下发白名单里的帧、未连接、连接已关闭、编码失败返回 false；
// 队列满说明对端读不动，记日志、断开它（重连后由 hello 对齐、补发）并返回 false。返回 true 只表示入队，不表示送达。
func (h *agentHub) send(machineID string, frame any) bool { return h.enqueue(machineID, nil, frame) }

// sendTo 同 send，但只发给 c：c 已不是其机器的当前连接（被替换、已断开）时返回 false，帧不会落到新连接上。
func (h *agentHub) sendTo(c *agentConn, frame any) bool { return h.enqueue(c.machine.ID, c, frame) }

// enqueue 是 send 与 sendTo 的共同实现；want 非 nil 时要求机器的当前连接就是 want。
func (h *agentHub) enqueue(machineID string, want *agentConn, frame any) bool {
	if _, ok := outboundKind(frame); !ok {
		h.log.Printf("拒绝下发 %T 帧给机器 %s：不在 hub 允许下发的种类里", frame, machineID)
		return false
	}
	b, err := wire.Marshal(frame)
	if err != nil {
		h.log.Printf("编码发往机器 %s 的帧: %v", machineID, err)
		return false
	}
	h.mu.Lock()
	c := h.conns[machineID]
	h.mu.Unlock()
	if c == nil || c.isClosed() || (want != nil && c != want) {
		return false
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

// current 报告 c 是否仍是其机器的当前连接且未被要求断开。
func (h *agentHub) current(c *agentConn) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conns[c.machine.ID] == c && !c.isClosed()
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

// shouldAuditProto 报告这次协议版本不一致要不要写审计：同一机器、同一对版本 protoAuditEvery 内只写一次
// （旧守护进程会按退避不停重连）。顺带清掉满期的记录，对端乱报版本也不会让表无限增长。
func (h *agentHub) shouldAuditProto(machineID string, agentProto int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	for k, t := range h.protoAudited {
		if now.Sub(t) >= protoAuditEvery {
			delete(h.protoAudited, k)
		}
	}
	k := protoAuditKey{machineID: machineID, agentProto: agentProto, hubProto: wire.Proto}
	if _, ok := h.protoAudited[k]; ok {
		return false
	}
	h.protoAudited[k] = now
	return true
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
	welcome, err := welcomeFrame(s.store)
	if err != nil {
		s.log.Printf("组装 welcome: %v", err)
		conn.Close(websocket.StatusInternalError, "hub 内部错误")
		return
	}
	if writeAgentFrame(r.Context(), conn, welcome) != nil {
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	pongs := newPongClock()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		defer cancel() // 写 goroutine 结束（写失败、pong 超时、被要求断开）时读循环随之结束
		s.agentWriteLoop(ctx, c, pongs)
	}()
	defer func() {
		cancel()
		<-writerDone
	}()
	if c.isClosed() { // 已被同一机器的新连接替换：对齐交给新连接
		return
	}
	s.onAgentHello(c, hello)
	s.agentReadLoop(ctx, c, pongs)
}

// readHello 在 s.helloTimeout 内读第一帧并校验：必须是 hello、协议版本与 hub 一致、机器 id 与认证出的机器相同、
// 字段不超长。不合格时关闭连接（协议版本不一致先回 Error 帧并记审计）并返回 false。
func (s *Server) readHello(ctx context.Context, conn *websocket.Conn, m store.Machine) (wire.Hello, bool) {
	// 超时由定时器主动走关闭握手，对端能收到 PolicyViolation 与原因；带超时的 ctx 读超时时库会直接断开 TCP，对端看不到原因。
	timer := time.AfterFunc(s.helloTimeout, func() { conn.Close(websocket.StatusPolicyViolation, "没有收到 hello") })
	_, first, err := conn.Read(ctx)
	if !timer.Stop() || err != nil {
		return wire.Hello{}, false // 定时器已在关闭连接，或对端已断开
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

// rejectProto 拒绝协议版本不一致的守护进程：记审计（同一对版本一小时一次），回 Error 帧说明双方版本，
// 再以 PolicyViolation 关闭。
func (s *Server) rejectProto(ctx context.Context, conn *websocket.Conn, m store.Machine, got int) {
	if s.agents.shouldAuditProto(m.ID, got) {
		detail, _ := json.Marshal(map[string]int{"agent_proto": got, "hub_proto": wire.Proto}) // map[string]int 编码不会失败
		if err := s.store.InsertAudit(store.AuditEvent{MachineID: m.ID, Actor: "agent", Action: "agent_proto_mismatch", Detail: string(detail)}); err != nil {
			s.log.Printf("记协议版本审计 %s: %v", m.ID, err)
		}
	}
	if b, err := wire.Marshal(wire.Error{T: wire.TError, Message: fmt.Sprintf("协议版本 %d，hub 需要 %d", got, wire.Proto)}); err == nil {
		writeAgentFrame(ctx, conn, b)
	}
	conn.Close(websocket.StatusPolicyViolation, "协议版本不一致")
}

// welcomeSource 是组装 welcome 要读的存储（*store.Store 实现），测试可替换。
type welcomeSource interface {
	RuleSet() (policy.RuleSet, int, error)
	GetSetting(key string) (string, bool, error)
}

// welcomeFrame 组装 welcome：规则快照（规则与版本号同一事务读出）与全局暂停状态。任一读失败都返回错误，
// 握手随之以 InternalError 关闭：宁可不连，也不把读不出的暂停当成未暂停。
func welcomeFrame(src welcomeSource) ([]byte, error) {
	rs, version, err := src.RuleSet()
	if err != nil {
		return nil, fmt.Errorf("读规则: %w", err)
	}
	y, err := rs.Marshal()
	if err != nil {
		return nil, fmt.Errorf("编码规则: %w", err)
	}
	paused, err := pausedSetting(src)
	if err != nil {
		return nil, err
	}
	return wire.Marshal(wire.Welcome{T: wire.TWelcome, RulesVersion: version, RulesYAML: string(y), Paused: paused, ServerTime: time.Now().UTC()})
}

// pausedSetting 读全局总开关：settings 的 paused 为 "1" 是暂停，"0" 或没有是未暂停；读失败或别的值返回错误。
func pausedSetting(src interface {
	GetSetting(key string) (string, bool, error)
}) (bool, error) {
	v, ok, err := src.GetSetting("paused")
	if err != nil {
		return false, fmt.Errorf("读暂停设置: %w", err)
	}
	switch {
	case !ok || v == "0":
		return false, nil
	case v == "1":
		return true, nil
	}
	return false, fmt.Errorf("暂停设置 %q 损坏", v)
}

// paused 读全局总开关，见 pausedSetting。调用方必须处理错误，不能把读不出当成未暂停。
func (s *Server) paused() (bool, error) { return pausedSetting(s.store) }

func writeAgentFrame(ctx context.Context, conn *websocket.Conn, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, agentWriteTimeout)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}

// pongClock 记最近一次 pong，按单调时钟计：读循环 mark，写循环查 idle。
type pongClock struct {
	start time.Time
	last  atomic.Int64 // 最近一次 pong 距 start 的纳秒数
}

func newPongClock() *pongClock { return &pongClock{start: time.Now()} }

func (p *pongClock) mark() { p.last.Store(int64(time.Since(p.start))) }

func (p *pongClock) idle() time.Duration { return time.Since(p.start) - time.Duration(p.last.Load()) }

// agentWriteLoop 是连接上唯一的数据写者：写出队列里的帧，每 PingInterval 发 ping，PongTimeout 内没有 pong 就返回；
// 被要求断开（替换、吊销、关停、队列满）时走关闭握手后返回。写失败或 ctx 结束也返回。
func (s *Server) agentWriteLoop(ctx context.Context, c *agentConn, pongs *pongClock) {
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
			idle := pongs.idle()
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

// agentReadLoop 读守护进程的帧并逐帧处理，连接出错、ctx 结束或连接被要求断开时返回。
func (s *Server) agentReadLoop(ctx context.Context, c *agentConn, pongs *pongClock) {
	bad := &badFrameLog{log: s.log, machineID: c.machine.ID}
	defer bad.summary()
	for {
		_, b, err := c.conn.Read(ctx)
		if err != nil || !s.handleAgentFrame(c, b, pongs, bad) {
			return
		}
	}
}

// errUnknownKind 表示帧种类不是守护进程该发的。
var errUnknownKind = errors.New("不是守护进程该发的帧种类")

// handleAgentFrame 处理一帧并分发。连接已被要求断开（被替换、吊销、关停）时不再分发，返回 false 让读循环结束；
// 解析不了的帧与不认识的种类记进 bad，连接照常。
func (s *Server) handleAgentFrame(c *agentConn, b []byte, pongs *pongClock, bad *badFrameLog) bool {
	if c.isClosed() {
		return false
	}
	kind, err := wire.Kind(b)
	if err != nil {
		bad.record("", err)
		return true
	}
	m := c.machine
	switch kind {
	case wire.TPong:
		pongs.mark()
	case wire.TPending:
		dispatchFrame(bad, kind, b, func(f wire.PendingRequest) { s.onPending(m, f) })
	case wire.TCancel:
		dispatchFrame(bad, kind, b, func(f wire.Cancel) { s.onCancel(m, f) })
	case wire.TExpired:
		dispatchFrame(bad, kind, b, func(f wire.Expired) { s.onExpired(m, f) })
	case wire.TDelivered:
		dispatchFrame(bad, kind, b, func(f wire.Delivered) { s.onDelivered(m, f) })
	case wire.TAudit:
		dispatchFrame(bad, kind, b, func(f wire.Audit) { s.onAudit(m, f) })
	default:
		bad.record(kind, errUnknownKind)
	}
	return true
}

// dispatchFrame 把帧解码成 T 再交给 fn；解码失败记进 bad。
func dispatchFrame[T any](bad *badFrameLog, kind string, b []byte, fn func(T)) {
	var f T
	if err := wire.Unmarshal(b, &f); err != nil {
		bad.record(kind, err)
		return
	}
	fn(f)
}

// badFrameLog 按连接限量记坏帧：前 badFrameLogMax 个逐条记（机器 id、帧种类、错误，不记帧内容），
// 之后只计数，连接结束时汇总一次。已认证的守护进程也刷不爆日志。
type badFrameLog struct {
	log       *log.Logger
	machineID string
	n         int
}

func (b *badFrameLog) record(kind string, err error) {
	b.n++
	if b.n > badFrameLogMax {
		return
	}
	k := "未知"
	if kind != "" {
		k = fmt.Sprintf("%q", clip(kind))
	}
	// 错误串用 %q 并截断：自定义解码器的错误可能带上对端给的值
	b.log.Printf("机器 %s 发来无法解析的帧（种类 %s）: %q", b.machineID, k, clip(err.Error()))
}

func (b *badFrameLog) summary() {
	if b.n > badFrameLogMax {
		b.log.Printf("机器 %s 本次连接共 %d 个无法解析的帧，只逐条记了前 %d 个", b.machineID, b.n, badFrameLogMax)
	}
}

// clip 把来自对端的字符串截到 logFieldMax 字节。
func clip(s string) string {
	if len(s) > logFieldMax {
		return s[:logFieldMax] + "…"
	}
	return s
}
