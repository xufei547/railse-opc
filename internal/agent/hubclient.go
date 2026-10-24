package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"

	"github.com/railse/railse-opc/internal/hub/wire"
	"github.com/railse/railse-opc/internal/policy"
)

// ErrHubDisconnected 表示此刻没有到 hub 的连接。
var ErrHubDisconnected = errors.New("未连接 hub")

// ErrHubDisabled 表示 hub 连续 hubUnauthorizedMax 次以 401 拒绝了本机密钥（机器被吊销或密钥无效），客户端已停止连接，直到进程重启。
var ErrHubDisabled = errors.New("hub 拒绝了本机密钥，已停止连接 hub")

// ErrGeminiNotForHub 表示试图把 Gemini 会话的请求送去 hub 等待（Gemini 只作审计上报）。
var ErrGeminiNotForHub = errors.New("gemini 请求不进 hub 等待")

// 以下是包级变量只为测试能调；NewHubClient 时复制进客户端。
var (
	// hubBackoff 是断线后的重连退避，逐档变长，最后一档封顶；连接稳定（见 hubStableAfter）后归零。
	hubBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	// hubStableAfter：连上后先收到 hub 的第一个 ping，或连接持续这么久，才算稳定、退避归零。
	// 握手后立即被断开（例如两台守护进程共用 machine.json 互相顶替）不算，退避照常变长，不会每秒重连。
	// 必须小于 hubIdleTimeout：否则收不到任何帧的连接总在算作稳定之前就因 idle 断开，退避永远不归零。
	hubStableAfter = 30 * time.Second
	// hubAuditMaxBytes 是审计积压的总字节上限（与 auditQueueMax 条数上限同时生效），超了丢最旧的。
	hubAuditMaxBytes = 32 << 20
	// hubIdleTimeout：超过这么久没收到 hub 的任何帧就断开重连（hub 每 30 秒 ping，取两倍）。必须大于 hubStableAfter（见上）。
	hubIdleTimeout = 2 * 30 * time.Second
	// hubProtoBackoff 是协议版本与 hub 不一致时的重连间隔：升级之前重连也没用，不必每 30 秒刷一次。
	hubProtoBackoff = 10 * time.Minute
	// hubAuditInterval 是审计批量上报的周期（攒够 auditBatchMax 条时不等它）。
	hubAuditInterval = time.Second
	// hubWriteTimeout 是写出一帧的上限（含等写锁），见 writeRaw。写时读取，不复制进客户端。
	hubWriteTimeout = 10 * time.Second
)

const (
	hubHandshakeTimeout = 10 * time.Second // 拨号握手与等 welcome 的上限
	hubErrorReasonMax   = 200              // hub 拒收某条请求时，转给等待者的文案字符上限
	hubLogFieldMax      = 200              // 日志里来自 hub 的字段的字符上限

	// hubUnauthorizedMax：握手连续被 401 拒绝（中间没有成功握手）这么多次才停用。hub 重启、反向代理切换时
	// 偶发的一两次 401 不该让守护进程断开到下次重启；机器真被吊销时，按默认退避几秒内就停用。
	hubUnauthorizedMax = 3

	auditQueueMax = 2000 // 审计积压上限，满了丢最旧
	auditBatchMax = 50   // 每帧至多这么多条；攒够就立即发
	// auditFrameBudget 是一帧审计编码后的字节上限：单帧上限留 4 KiB 余量。
	auditFrameBudget = wire.MaxFrame - 4<<10
	// auditFieldMax 是超大条目截断时每个大字段保留的字节数。
	auditFieldMax = 16 << 10
)

// auditFrameOverhead 是空审计帧 {"t":"audit","entries":[]} 的字节数；单条条目至多 auditFrameBudget 减去它。
var auditFrameOverhead = len(`{"t":"` + wire.TAudit + `","entries":[]}`)

// auditItem 是审计队列的一项：seq 递增，发送成功后按序号出队。
type auditItem struct {
	seq uint64
	b   json.RawMessage
}

// HubClient 维护到 hub 的一条 WebSocket 长连接：只向外连 hub 的 /ws/agent，用 Bearer 机器密钥认证。
// hub 只能经它下发规则、总开关与对请求的 allow/deny 决定，没有让本机执行任何东西的帧。
type HubClient struct {
	cfg          Config
	machine      MachineConfig
	rules        *Rules
	pending      *Pending
	setHubPaused func(bool) error
	log          *log.Logger
	addr         string // 日志里用的 hub 地址，只有 scheme://host

	backoff       []time.Duration
	stableAfter   time.Duration
	idleTimeout   time.Duration
	protoBackoff  time.Duration
	auditInterval time.Duration
	auditMaxBytes int

	mu       sync.Mutex
	conn     *websocket.Conn
	changed  chan struct{} // 下一次状态变化（连上、断开、停用）时关闭，随即换新的
	disabled bool
	waiters  map[string]chan wire.Decision

	auditQ        []auditItem
	auditBytes    int // auditQ 里条目的字节总数
	auditSeq      uint64
	auditDropping bool          // 本轮积压已记过“丢弃最旧”的日志
	auditKick     chan struct{} // 攒够一批时叫醒上报循环，容量 1
}

// NewHubClient 组装客户端，不发起连接。setHubPaused 是设置 hub 总开关的入口（守护进程里传 Server.SetHubPaused）：
// welcome 与 paused 帧只经它改 hub 那一侧，不直接改规则引擎或 state.json；不能为 nil。
// pending 是守护进程的未完成请求表（Server.Pending()），重连对齐时枚举。
func NewHubClient(cfg Config, m MachineConfig, rules *Rules, pending *Pending, setHubPaused func(bool) error, logger *log.Logger) *HubClient {
	if setHubPaused == nil {
		panic("agent.NewHubClient: setHubPaused 不能为 nil")
	}
	return &HubClient{cfg: cfg, machine: m, rules: rules, pending: pending, setHubPaused: setHubPaused, log: logger, addr: hubOrigin(m.HubURL),
		backoff: append([]time.Duration(nil), hubBackoff...), stableAfter: hubStableAfter, idleTimeout: hubIdleTimeout, protoBackoff: hubProtoBackoff,
		auditInterval: hubAuditInterval, auditMaxBytes: hubAuditMaxBytes,
		changed: make(chan struct{}), waiters: map[string]chan wire.Decision{}, auditKick: make(chan struct{}, 1)}
}

// hubOrigin 给出日志里显示的 hub 地址：只有 scheme://host，不带 userinfo、路径与 query。
func hubOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "（无效的 hub 地址）"
	}
	return u.Scheme + "://" + u.Host
}

// wsURL 由 hub 地址推出 /ws/agent 的地址：http→ws、https→wss，丢掉 userinfo、query 与片段（凭据只走 Authorization 头）。
func wsURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("hub 地址无效")
	}
	switch u.Scheme {
	case "http", "ws":
		u.Scheme = "ws"
	case "https", "wss":
		u.Scheme = "wss"
	default:
		return "", errors.New("hub 地址应以 http:// 或 https:// 开头")
	}
	if u.Host == "" {
		return "", errors.New("hub 地址缺少主机名")
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
	u.Path = strings.TrimRight(u.Path, "/") + "/ws/agent"
	u.RawPath = ""
	return u.String(), nil
}

// noRedirectClient 拨号不跟随重定向：Authorization 头里的机器密钥只发给配置的 hub。
var noRedirectClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// ConnState 返回当前是否连着 hub，以及一个在下一次状态变化（连上、断开，或连续被 401 拒绝而停用）时关闭的通道。
// 要同时关心停用的调用方，先调 ConnState 取通道、再查 Disabled：停用也会关闭这个通道，按这个顺序不会漏掉
// 两次调用之间发生的停用（反过来先查 Disabled 再取通道，拿到的可能是停用之后的新通道，永远不会关闭）。
func (h *HubClient) ConnState() (connected bool, changed <-chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn != nil, h.changed
}

// Connected 报告当前是否连着。
func (h *HubClient) Connected() bool {
	c, _ := h.ConnState()
	return c
}

// Disabled 报告客户端是否已被永久停用（握手连续 hubUnauthorizedMax 次被 401 拒绝：机器被吊销或密钥无效）。
// 停用后不再重连，直到进程重启。
func (h *HubClient) Disabled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.disabled
}

// State 报告到 hub 的连接状态，给 railse agent status 显示：disabled（已停用）、connected（连着），
// 其余都是 connecting（还没连上、断线后等着重连，或握手被 401 拒绝但还没到停用的次数）。
func (h *HubClient) State() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch {
	case h.disabled:
		return "disabled"
	case h.conn != nil:
		return "connected"
	}
	return "connecting"
}

// WaitConnected 等到连上或 ctx 结束；客户端已停用时立即返回 ErrHubDisabled。
func (h *HubClient) WaitConnected(ctx context.Context) error {
	for {
		// 先取通道再查停用（见 ConnState）
		connected, changed := h.ConnState()
		if h.Disabled() {
			return ErrHubDisabled
		}
		if connected {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// setConn 换连接状态并通知等状态变化的人。调用方不持 mu。
func (h *HubClient) setConn(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conn = c
	close(h.changed)
	h.changed = make(chan struct{})
}

// disable 永久停用并通知等状态变化的人。
func (h *HubClient) disable() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.disabled = true
	close(h.changed)
	h.changed = make(chan struct{})
}

// sessionEnd 说明一次连接为什么结束。
type sessionEnd struct {
	welcomed      bool  // 握手成功过（收到 welcome、标为已连接）：连续 401 的计数归零
	stable        bool  // 连接稳定过（收到 hub 的 ping，或持续了 stableAfter）：之后退避归零
	unauthorized  bool  // 握手被 401 拒绝
	protoMismatch bool  // hub 回 Error 帧后以 PolicyViolation 关闭（协议版本不一致）
	err           error // 其他原因；ctx 取消时不看
}

// Run 一直重连直到 ctx 取消；握手连续 hubUnauthorizedMax 次被 401 拒绝时停用并返回，之前的 401 照常退避重连。
// 同一客户端只能有一个 Run。
func (h *HubClient) Run(ctx context.Context) {
	// unauthorized 是连续被 401 拒绝的次数，只在成功握手（welcome）后归零；连不上、5xx、超时、协议版本不一致
	// 都不改它（否则 401 与 502 交替时永远停不下来）。只在本 goroutine 里读写，不加锁。
	attempt, unauthorized := 0, 0
	for {
		end := h.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if end.unauthorized {
			unauthorized++
		} else if end.welcomed {
			unauthorized = 0
		}
		if unauthorized >= hubUnauthorizedMax {
			h.disable()
			h.log.Printf("！！hub %s 以 HTTP 401 拒绝了本机密钥：本机可能已被吊销，或 machine.json 里的密钥无效。"+
				"已停止连接 hub，请求一律交回终端；请重新 railse agent enroll 后重启守护进程", h.addr)
			return
		}
		if end.stable {
			attempt = 0
		}
		var wait time.Duration
		if end.protoMismatch {
			wait = h.protoBackoff
		} else {
			wait = h.backoff[min(attempt, len(h.backoff)-1)]
			attempt++
			switch {
			case end.unauthorized:
				h.log.Printf("hub %s 回 HTTP 401（第 %d/%d 次），稍后重试；若持续，本机可能已被吊销或 machine.json 里的密钥无效",
					h.addr, unauthorized, hubUnauthorizedMax)
			case end.err != nil:
				h.log.Printf("与 hub %s 的连接结束: %q，%s 后重连", h.addr, clipText(end.err.Error(), hubLogFieldMax), wait)
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return
		}
	}
}

// scrubDialErr 去掉拨号错误里的 URL（net/http 的 *url.Error 会带上完整地址），只留底层原因。
func scrubDialErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("连接失败: %w", ue.Err)
	}
	return err
}

// session 跑一次连接的完整生命周期：拨号 → hello → welcome（应用规则与 hub 暂停）→ 标为已连接 → 重发 pending →
// 读循环。返回前停掉审计上报循环并关闭连接。
func (h *HubClient) session(ctx context.Context) sessionEnd {
	target, err := wsURL(h.machine.HubURL)
	if err != nil {
		return sessionEnd{err: err}
	}
	dialCtx, cancel := context.WithTimeout(ctx, hubHandshakeTimeout)
	conn, resp, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{HTTPClient: noRedirectClient, HTTPHeader: http.Header{"Authorization": {"Bearer " + h.machine.Secret}}})
	cancel()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return sessionEnd{unauthorized: true}
		}
		return sessionEnd{err: scrubDialErr(err)}
	}
	defer conn.CloseNow()
	conn.SetReadLimit(wire.MaxFrame)

	var summaries []wire.PendingSummary
	for _, pr := range h.pending.List() {
		if isGemini(pr) {
			continue
		}
		summaries = append(summaries, wire.PendingSummary{RequestID: pr.ID, CreatedAt: pr.Created.UTC()})
	}
	hello := wire.Hello{T: wire.THello, Proto: wire.Proto, MachineID: h.machine.MachineID, AgentVersion: h.cfg.AgentVersion, HomeDir: h.cfg.HomeDir, Pending: summaries}
	if err := writeFrame(ctx, conn, hello); err != nil {
		return sessionEnd{err: err}
	}
	welcome, end := h.readWelcome(ctx, conn)
	if end != nil {
		return *end
	}
	h.applyWelcome(welcome)

	h.setConn(conn)
	defer h.setConn(nil)
	h.log.Printf("已连接 hub %s，规则版本 %d", h.addr, welcome.RulesVersion)
	connectedAt, pinged := time.Now(), false
	// setConn 之后的每条返回路径都经 ended，都算握手成功过
	ended := func(err error) sessionEnd {
		return sessionEnd{welcomed: true, stable: pinged || time.Since(connectedAt) >= h.stableAfter, err: err}
	}

	// 重发本机全部 pending（Gemini 除外）；hub 对已知 id 不重复登记，已决定的再发一次决定。
	// 编不进一帧的请求（大 heredoc、大 MCP 参数）跳过并让它的等待者立即交回终端，不当作连接错误：
	// 否则每次重连都在这里断开，后面的 pending 与审计永远发不出去。
	for _, pr := range h.pending.List() {
		if isGemini(pr) {
			continue
		}
		b, err := wire.Marshal(toWire(pr))
		if err != nil {
			h.log.Printf("请求 %q 无法送往 hub（%v），跳过，交回终端", clipText(pr.ID, hubLogFieldMax), err)
			h.failWaiter(pr.ID, "请求太大，无法送往 hub")
			continue
		}
		if err := writeRaw(ctx, conn, b); err != nil {
			return ended(err)
		}
	}

	sctx, stop := context.WithCancel(ctx)
	auditDone := make(chan struct{})
	go func() {
		defer close(auditDone)
		h.auditLoop(sctx, conn)
	}()
	defer func() {
		stop()
		<-auditDone
	}()
	return ended(h.readLoop(sctx, conn, &pinged))
}

// readWelcome 读 hub 的第一帧。是 welcome 时返回它；hub 回 Error 帧并以 PolicyViolation 关闭时记下 hub 的文案、
// 标为协议版本不一致；其余情况返回错误。
func (h *HubClient) readWelcome(ctx context.Context, conn *websocket.Conn) (wire.Welcome, *sessionEnd) {
	first, err := readFrame(ctx, conn, hubHandshakeTimeout)
	if err != nil {
		return wire.Welcome{}, &sessionEnd{err: err}
	}
	kind, _ := wire.Kind(first)
	switch kind {
	case wire.TWelcome:
		var w wire.Welcome
		if err := wire.Unmarshal(first, &w); err != nil {
			return wire.Welcome{}, &sessionEnd{err: fmt.Errorf("解析 welcome: %w", err)}
		}
		return w, nil
	case wire.TError:
		var e wire.Error
		wire.Unmarshal(first, &e)
		msg := clipText(e.Message, hubLogFieldMax)
		_, err := readFrame(ctx, conn, hubHandshakeTimeout)
		if websocket.CloseStatus(err) == websocket.StatusPolicyViolation {
			h.log.Printf("hub %s 拒绝了握手: %q。多半是守护进程与 hub 的协议版本不一致，请升级较旧的一方；%s 后再试",
				h.addr, msg, h.protoBackoff)
			return wire.Welcome{}, &sessionEnd{protoMismatch: true}
		}
		return wire.Welcome{}, &sessionEnd{err: fmt.Errorf("hub 在握手时报错: %q", msg)}
	}
	return wire.Welcome{}, &sessionEnd{err: errors.New("hub 的第一帧不是 welcome")}
}

// applyWelcome 以 hub 为准：规则无论版本都替换（hub 重建库后版本可能变小或巧合相等），总开关交给 setHubPaused。
func (h *HubClient) applyWelcome(w wire.Welcome) {
	h.replaceRules(w.RulesVersion, w.RulesYAML)
	h.applyHubPaused(w.Paused)
}

// readLoop 读 hub 的帧并逐帧分发，连接出错、ctx 结束或超过 idleTimeout 没收到任何帧时返回。
// 收到 ping 时把 *pinged 置真（连接稳定的标志）。只有这里读连接（库不允许并发读）。
func (h *HubClient) readLoop(ctx context.Context, conn *websocket.Conn, pinged *bool) error {
	for {
		b, err := readFrame(ctx, conn, h.idleTimeout)
		if err != nil {
			if errors.Is(err, errIdle) {
				return fmt.Errorf("超过 %s 没有收到 hub 的任何帧", h.idleTimeout)
			}
			return err
		}
		kind, err := wire.Kind(b)
		if err != nil {
			continue
		}
		switch kind {
		case wire.TPing:
			*pinged = true
			if err := writeFrame(ctx, conn, wire.Pong{T: wire.TPong}); err != nil {
				return err
			}
		case wire.TDecision:
			var d wire.Decision
			if wire.Unmarshal(b, &d) == nil {
				if !h.deliver(d) {
					// 钩子已不在等（或从没等过）：只记日志。送达回执只在真交给钩子之后由决定方发出
					h.log.Printf("收到请求 %q 的决定，但没有等待者，忽略", clipText(d.RequestID, hubLogFieldMax))
				}
			}
		case wire.TRules:
			var r wire.Rules
			if wire.Unmarshal(b, &r) == nil {
				// pushRules 并发时快照可能乱序到达：只接受比本机新的
				if r.Version > h.rules.Version() {
					h.replaceRules(r.Version, r.YAML)
				}
			}
		case wire.TPaused:
			var p wire.Paused
			if wire.Unmarshal(b, &p) == nil {
				h.applyHubPaused(p.Paused)
			}
		case wire.TError:
			var e wire.Error
			if wire.Unmarshal(b, &e) == nil {
				h.onError(e)
			}
		}
	}
}

// errIdle 表示读等满了 idleTimeout。
var errIdle = errors.New("读超时")

// readFrame 读一帧，最多等 d；等满时返回 errIdle（库随之关闭连接）。
func readFrame(ctx context.Context, conn *websocket.Conn, d time.Duration) ([]byte, error) {
	rctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	_, b, err := conn.Read(rctx)
	if err != nil && ctx.Err() == nil && errors.Is(rctx.Err(), context.DeadlineExceeded) {
		return nil, errIdle
	}
	return b, err
}

// deliver 把决定交给等待者，不阻塞：通道（容量 1）已有决定时丢弃后来的（hub 对重发的 pending 会再发一次）。
// 没有等待者时返回 false。
func (h *HubClient) deliver(d wire.Decision) bool {
	h.mu.Lock()
	ch := h.waiters[d.RequestID]
	h.mu.Unlock()
	if ch == nil {
		return false
	}
	select {
	case ch <- d:
	default:
	}
	return true
}

// onError 处理 hub 的 Error 帧：针对某条请求（hub 拒收）且有等待者时，投递一个 hub_error 的合成决定，
// 决定方据此把请求交回终端；其余只记日志。
func (h *HubClient) onError(e wire.Error) {
	msg := clipText(e.Message, hubErrorReasonMax)
	if e.RequestID == "" {
		h.log.Printf("hub 报错: %q", msg)
		return
	}
	if !h.failWaiter(e.RequestID, msg) {
		h.log.Printf("hub 拒收请求 %q: %q（没有等待者）", clipText(e.RequestID, hubLogFieldMax), msg)
	}
}

// failWaiter 给请求的等待者投递 hub_error 的合成决定（Action 为空，理由截到 hubErrorReasonMax 字符），
// 决定方据此把请求立即交回终端。没有等待者时返回 false。
func (h *HubClient) failWaiter(id, reason string) bool {
	return h.deliver(wire.Decision{T: wire.TDecision, RequestID: id, Action: "", DecidedBy: "hub_error", Reason: clipText(reason, hubErrorReasonMax)})
}

// replaceRules 解析并换上 hub 下发的规则；坏规则集保留本地并记日志。
func (h *HubClient) replaceRules(version int, yamlText string) {
	rs, err := policy.ParseRuleSet([]byte(yamlText))
	if err != nil {
		h.log.Printf("hub 下发的规则（版本 %d）无效，保留本地: %v", version, err)
		return
	}
	if err := h.rules.Replace(rs, version); err != nil {
		h.log.Printf("应用 hub 规则（版本 %d）失败，保留本地: %v", version, err)
		return
	}
	h.log.Printf("规则更新到版本 %d，%d 条", version, len(rs.Rules))
}

// applyHubPaused 把 hub 的总开关交给守护进程（只改 hub 那一侧，生效值 = 本机 || hub）。
// setHubPaused 会写审计、同步调用 Forward（要拿 h.mu），所以这里绝不能持着 h.mu。
func (h *HubClient) applyHubPaused(p bool) {
	if err := h.setHubPaused(p); err != nil {
		h.log.Printf("应用 hub 总开关（paused=%v）: %v", p, err)
	}
}

// isGemini 报告请求是否来自 Gemini 会话：Gemini 的请求不进 hub 等待，只作审计上报。
func isGemini(pr PendingRequest) bool { return strings.EqualFold(pr.Req.Session.Tool, "gemini") }

// clipText 把来自对端的文本截到至多 n 个字符，不切断多字节字符。
func clipText(s string, n int) string {
	i := 0
	for j := range s {
		if i == n {
			return s[:j]
		}
		i++
	}
	return s
}

func writeFrame(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := wire.Marshal(v)
	if err != nil {
		return err
	}
	return writeRaw(ctx, conn, b)
}

// writeRaw 写一帧，最多等 hubWriteTimeout（ctx 更早结束时以 ctx 为准）。超时分两种：写帧中途超时，库会关闭连接，
// 读循环随之结束；还在等写锁（另一帧正写着）时超时，库不关连接，连接仍会被当作连着——要按断线处理的调用方必须
// 自己关掉它（SendPending、审计上报循环都这么做；会话里的写失败直接结束会话，由 session 的 defer 关）。
// 回执（送达、取消、过期）例外：写不出去只报错、不关连接，见 sendNotice。
func writeRaw(ctx context.Context, conn *websocket.Conn, b []byte) error {
	wctx, cancel := context.WithTimeout(ctx, hubWriteTimeout)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

// sendNotice 编码并发出一帧回执（送达、取消、过期），写的上限取 ctx 与 hubWriteTimeout 中较早的。编码错误原样返回；
// 未连接返回 ErrHubDisconnected。写不出去（多半是另一帧正写着、等写锁超时）只返回错误、不关连接：回执只是通知，
// 发不出去重连对齐会补上；为它断开整条连接会打断正在写的大帧，并让所有在途请求进入断线宽限。
// （写帧中途超时时库会自己关连接，那是连接本身出了问题。）
func (h *HubClient) sendNotice(ctx context.Context, v any) error {
	b, err := wire.Marshal(v)
	if err != nil {
		return err
	}
	return h.sendRaw(ctx, b, false)
}

// sendRaw 发出编好的一帧。未连接返回 ErrHubDisconnected。closeOnFail 为真时写失败就关掉这条连接并返回包着
// ErrHubDisconnected 的错误：等写锁超时时库不会自己关，连接还会被当作连着；关掉后读循环随之结束、状态变为断开，
// 调用方按 ErrHubDisconnected 等重连才成立（重连后 pending 会随对齐重发）。closeOnFail 为假时（回执）只返回写错误，
// 不关连接；文案不说连接是否还在：写帧中途超时时库已自己关了连接。
func (h *HubClient) sendRaw(ctx context.Context, b []byte, closeOnFail bool) error {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return ErrHubDisconnected
	}
	if err := writeRaw(ctx, conn, b); err != nil {
		if !closeOnFail {
			return fmt.Errorf("回执没写出去: %w", err)
		}
		conn.CloseNow()
		return fmt.Errorf("%w: %v", ErrHubDisconnected, err)
	}
	return nil
}

// toWire 把未完成请求编成 pending 帧。载荷只用脱敏后的 Redacted，永远不用 Req 里的原始载荷；
// DeadlineSeconds 是从 CreatedAt 起算的总时长（hub 按 CreatedAt+DeadlineSeconds 算过期），不是剩余秒数。
func toWire(pr PendingRequest) wire.PendingRequest {
	return wire.PendingRequest{T: wire.TPending, RequestID: pr.ID, Session: pr.Req.Session, ToolName: pr.Req.ToolName, Payload: pr.Redacted,
		Risk: pr.Risk.String(), ProjectRoot: pr.ProjectRoot, EngineReason: pr.EngineReason,
		DeadlineSeconds: int(pr.Deadline.Sub(pr.Created).Seconds()), CreatedAt: pr.Created.UTC()}
}

// SendPending 送一条待决请求。返回满足 errors.Is(err, ErrHubDisconnected) 的错误（未连接，或写连接失败）时，
// 调用方可以在宽限期内等重连（请求仍在未完成表里，重连后随对齐重发）；非 ErrHubDisconnected 的错误一律交回终端：
// Gemini 会话的请求（ErrGeminiNotForHub）、编码后超过单帧上限（errors.Is(err, wire.ErrFrameTooLarge)）等。
func (h *HubClient) SendPending(pr PendingRequest) error {
	if isGemini(pr) {
		return ErrGeminiNotForHub
	}
	b, err := wire.Marshal(toWire(pr))
	if err != nil {
		return fmt.Errorf("请求 %q 无法送往 hub: %w", pr.ID, err)
	}
	return h.sendRaw(context.Background(), b, true)
}

// SendCancel 告诉 hub 钩子已消失。写的上限取 ctx 与 hubWriteTimeout 中较早的；写不出去不关连接（见 sendNotice，下同）。
func (h *HubClient) SendCancel(ctx context.Context, id, reason string) error {
	return h.sendNotice(ctx, wire.Cancel{T: wire.TCancel, RequestID: id, Reason: reason})
}

// SendExpired 告诉 hub 本机等待已过期。
func (h *HubClient) SendExpired(ctx context.Context, id string) error {
	return h.sendNotice(ctx, wire.Expired{T: wire.TExpired, RequestID: id})
}

// SendDelivered 告诉 hub 决定已交给钩子。
func (h *HubClient) SendDelivered(ctx context.Context, id string) error {
	return h.sendNotice(ctx, wire.Delivered{T: wire.TDelivered, RequestID: id})
}

// Await 登记一个等待者；决定到达时从返回的通道读到（只投递第一个，重复的丢弃）。
// hub 拒收这条请求时读到 DecidedBy 为 hub_error、Action 为空的合成决定。用完要 Forget。
func (h *HubClient) Await(id string) <-chan wire.Decision {
	ch := make(chan wire.Decision, 1)
	h.mu.Lock()
	h.waiters[id] = ch
	h.mu.Unlock()
	return ch
}

// Forget 注销等待者。
func (h *HubClient) Forget(id string) {
	h.mu.Lock()
	delete(h.waiters, id)
	h.mu.Unlock()
}

// Forward 实现 AuditSink：编码后进队列就返回，不阻塞。积压超过 auditQueueMax 条或 auditMaxBytes 字节时丢最旧的并记一次日志；
// 攒够 auditBatchMax 条时叫醒上报循环立即发，否则等定时。未连接时积压，连上后补发。
func (h *HubClient) Forward(e AuditEntry) {
	b, err := encodeAuditEntry(e)
	if err != nil {
		h.log.Printf("审计条目 %q（%s）无法上报 hub，丢弃: %v", clipText(e.RequestID, hubLogFieldMax), clipText(e.State, hubLogFieldMax), err)
		return
	}
	h.mu.Lock()
	// 放进新条目后条数不超过 auditQueueMax、字节不超过 auditMaxBytes：从队头丢
	n := 0
	for n < len(h.auditQ) && (len(h.auditQ)-n >= auditQueueMax || h.auditBytes+len(b) > h.auditMaxBytes) {
		h.auditBytes -= len(h.auditQ[n].b)
		n++
	}
	logDrop := false
	if n > 0 {
		h.auditQ = h.auditQ[n:]
		logDrop = !h.auditDropping
		h.auditDropping = true
	}
	h.auditSeq++
	h.auditQ = append(h.auditQ, auditItem{seq: h.auditSeq, b: b})
	h.auditBytes += len(b)
	full := len(h.auditQ) >= auditBatchMax
	h.mu.Unlock()
	if logDrop {
		h.log.Printf("审计积压超过上限（%d 条或 %d 字节；hub 未连接或上报太慢），开始丢弃最旧的", auditQueueMax, h.auditMaxBytes)
	}
	if full {
		select {
		case h.auditKick <- struct{}{}:
		default:
		}
	}
}

// errAuditTooLarge 表示审计条目截断大字段后仍放不进一帧。
var errAuditTooLarge = errors.New("截断后仍超过单帧上限")

// encodeAuditEntry 编码一条审计；超过单帧可容纳的大小时先截断载荷与文本里的大字段再编码（条目里的载荷与文本
// 已在写审计前脱敏，截断发生在脱敏之后），仍超限返回 errAuditTooLarge。
func encodeAuditEntry(e AuditEntry) ([]byte, error) {
	limit := auditFrameBudget - auditFrameOverhead
	b, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(b) <= limit {
		return b, nil
	}
	if b, err = json.Marshal(shrinkAuditEntry(e)); err != nil {
		return nil, err
	}
	if len(b) > limit {
		return nil, fmt.Errorf("%w（%d 字节）", errAuditTooLarge, len(b))
	}
	return b, nil
}

// shrinkAuditEntry 返回把大字段截到 auditFieldMax 字节的副本：载荷的各文本字段、MCP 参数（截断后存成 JSON 字符串），
// 以及理由、备注、消息。不动调用方的条目。
func shrinkAuditEntry(e AuditEntry) AuditEntry {
	if e.Payload != nil {
		p := *e.Payload
		p.Command, p.Cwd, p.Path, p.DiffPreview = cutField(p.Command), cutField(p.Cwd), cutField(p.Path), cutField(p.DiffPreview)
		p.Server, p.Tool, p.URL, p.Method = cutField(p.Server), cutField(p.Tool), cutField(p.URL), cutField(p.Method)
		if len(p.Args) > auditFieldMax {
			s, _ := json.Marshal(cutField(string(p.Args))) // 字符串编码不会失败
			p.Args = s
		}
		e.Payload = &p
	}
	e.Reason, e.Note, e.Message = cutField(e.Reason), cutField(e.Note), cutField(e.Message)
	return e
}

// cutField 把超过 auditFieldMax 字节的字符串截短（不切断多字节字符）并标上已截断。
func cutField(s string) string {
	if len(s) <= auditFieldMax {
		return s
	}
	i := auditFieldMax
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + fmt.Sprintf("…[已截断，原 %d 字节]", len(s))
}

// auditBatch 从队头取一批：至多 auditBatchMax 条，且整帧编码后不超过 auditFrameBudget。只复制，不出队。
func (h *HubClient) auditBatch() []auditItem {
	h.mu.Lock()
	defer h.mu.Unlock()
	size, n := auditFrameOverhead, 0
	for n < len(h.auditQ) && n < auditBatchMax {
		add := len(h.auditQ[n].b)
		if n > 0 {
			add++ // 逗号
		}
		if size+add > auditFrameBudget {
			break
		}
		size += add
		n++
	}
	return append([]auditItem(nil), h.auditQ[:n]...)
}

// ackAudit 把序号不超过 seq 的条目出队（发送期间新增的序号更大，不会被删；被挤掉的已不在队里）。
func (h *HubClient) ackAudit(seq uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	i := 0
	for i < len(h.auditQ) && h.auditQ[i].seq <= seq {
		h.auditBytes -= len(h.auditQ[i].b)
		i++
	}
	h.auditQ = h.auditQ[i:]
	h.auditDropping = false
}

// auditLoop 每 auditInterval 或被 Forward 叫醒时把队列发空。写连接失败时关掉连接并结束（本次连接作废，条目留在队里，
// 重连后补发；等写锁超时库不会自己关连接，不关的话读循环还在、连接还当作连着，积压要等到下次断线才补发）；
// ctx 结束时返回。
func (h *HubClient) auditLoop(ctx context.Context, conn *websocket.Conn) {
	t := time.NewTicker(h.auditInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-h.auditKick:
		case <-ctx.Done():
			return
		}
		if err := h.flushAudit(ctx, conn); err != nil {
			if ctx.Err() == nil {
				h.log.Printf("上报审计失败，断开重连后补发: %q", clipText(err.Error(), hubLogFieldMax))
				conn.CloseNow()
			}
			return
		}
	}
}

// flushAudit 一批批发送，直到队列为空。编码失败（不该发生：单条已限长、批次按字节累计）不算连接错误：
// 退到只发队头一条，仍编不出就丢弃它并记日志，保证前进。只有写连接失败才返回错误。
func (h *HubClient) flushAudit(ctx context.Context, conn *websocket.Conn) error {
	for {
		batch := h.auditBatch()
		if len(batch) == 0 {
			return nil
		}
		b, err := encodeAuditBatch(batch)
		if err != nil && len(batch) > 1 {
			batch = batch[:1]
			b, err = encodeAuditBatch(batch)
		}
		if err != nil {
			h.log.Printf("编码审计批次失败，丢弃 1 条: %v", err)
			h.ackAudit(batch[0].seq)
			continue
		}
		if err := writeRaw(ctx, conn, b); err != nil {
			return err
		}
		h.ackAudit(batch[len(batch)-1].seq)
	}
}

func encodeAuditBatch(batch []auditItem) ([]byte, error) {
	entries := make([]json.RawMessage, len(batch))
	for i, it := range batch {
		entries[i] = it.b
	}
	b, err := wire.Marshal(wire.Audit{T: wire.TAudit, Entries: entries})
	if err == nil && len(b) > auditFrameBudget {
		err = wire.ErrFrameTooLarge
	}
	return b, err
}
