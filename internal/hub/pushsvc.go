package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xufei547/railse-opc/internal/hub/push"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/policy"
)

const (
	// vapidSetting 是 settings 里 VAPID 私钥的键：32 字节标量的 base64url（无补位），明文（设计 5.3 例外），永不经接口返回。
	vapidSetting    = "vapid_private"
	pushQueueSize   = 256 // 推送队列满了就丢，记日志
	pushWorkers     = 4   // 一条请求同时推给几条订阅
	pushMaxFailures = 5   // 删订阅要连续失败至少这么多次（计数的失败，见 sendOne）……
	// pushFailureSpan ……且这一轮的第一次失败已过去这么久：几秒、几小时的推送服务故障或 hub 断网不会删光好订阅。
	pushFailureSpan = 24 * time.Hour
	pushTitleLen    = 80             // 通知标题的字符上限
	pushSummaryLen  = 160            // permission 正文里“工具名: 载荷摘要”的字符上限
	pushRiskLen     = 32             // permission 正文里风险的字符上限
	pushBodyLen     = 200            // 通知正文的字符上限
	pushTTL         = 24 * time.Hour // 投递 TTL 的上限；有期限的请求用剩下的期限（pushTTLFor）
	pushTestTTL     = time.Minute
	// pushQuestionMaxAge：事件时间早于此刻这么久的 question 不推（守护进程重连后补发的旧审计批次，见 pushWorthy）。
	pushQuestionMaxAge     = 2 * time.Minute
	defaultPushSendTimeout = 10 * time.Second
	pushDisabledReason     = "hub 没有配置 --public-url，推送停用"
)

// CheckPublicURL 检查 hub 对外的源（--public-url、Config.PublicURL）：必须正好是 https://host[:port]，
// 不带路径（结尾的 / 也不行）、查询、片段与用户信息。它就是 VAPID 的 sub：Apple 对不规范的 sub 回 BadJwtToken；
// 浏览器也只在 https 源上开放推送，所以 localhost 也要 https。
func CheckPublicURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Hostname() == "" || strings.HasSuffix(u.Host, ":") ||
		u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsFunc(s, unicode.IsSpace) {
		return fmt.Errorf("public-url 必须是 https://host[:port] 形式的源（不带路径、结尾的 /、查询与片段），得到 %q", s)
	}
	return nil
}

func encodeVAPID(v *push.VAPID) string { return base64.RawURLEncoding.EncodeToString(v.Raw()) }

// parseVAPIDSetting 解库里存的私钥。错误里不带密钥内容。
func parseVAPIDSetting(raw string) (*push.VAPID, error) {
	b, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errors.New("库里的 VAPID 密钥损坏（不是 base64url）；可运行 railse hub vapid --rotate 换新密钥")
	}
	v, err := push.ParseVAPID(b)
	if err != nil {
		return nil, fmt.Errorf("库里的 VAPID 密钥损坏（%v）；可运行 railse hub vapid --rotate 换新密钥", err)
	}
	return v, nil
}

// loadVAPIDSetting 读库里的 VAPID 私钥，没有就生成并存。存用“没有才写”，写完再读一次、以库里的为准：
// 新库首次启动时 hub 与 railse hub vapid 两个进程同时生成，也只会留下一把。返回库里的原值（比对换钥用）与解析出的密钥。
func loadVAPIDSetting(st *store.Store) (string, *push.VAPID, error) {
	raw, ok, err := st.GetSetting(vapidSetting)
	if err != nil {
		return "", nil, fmt.Errorf("读 VAPID 密钥: %w", err)
	}
	if !ok {
		nv, err := push.GenerateVAPID()
		if err != nil {
			return "", nil, fmt.Errorf("生成 VAPID 密钥: %w", err)
		}
		if _, err := st.SetSettingIfAbsent(vapidSetting, encodeVAPID(nv)); err != nil {
			return "", nil, fmt.Errorf("存 VAPID 密钥: %w", err)
		}
		if raw, ok, err = st.GetSetting(vapidSetting); err != nil {
			return "", nil, fmt.Errorf("读 VAPID 密钥: %w", err)
		}
		if !ok {
			return "", nil, errors.New("VAPID 密钥存了却读不到")
		}
	}
	v, err := parseVAPIDSetting(raw)
	if err != nil {
		return "", nil, err
	}
	return raw, v, nil
}

// LoadOrCreateVAPID 读 settings 里的 VAPID 私钥（键 vapid_private），没有就生成并存（明文，设计 5.3 的例外）。
func LoadOrCreateVAPID(st *store.Store) (*push.VAPID, error) {
	_, v, err := loadVAPIDSetting(st)
	return v, err
}

// RotateVAPID 换新密钥并删掉全部订阅（一个事务）：旧订阅是对旧公钥建立的，新密钥签的推送会被推送服务拒绝。
// 返回新密钥与删了几条。在跑的 hub 下一次发推送或回 /api/push/vapid 时从库里读到新密钥，不用重启。
func RotateVAPID(st *store.Store) (*push.VAPID, int, error) {
	v, err := push.GenerateVAPID()
	if err != nil {
		return nil, 0, fmt.Errorf("生成 VAPID 密钥: %w", err)
	}
	n, err := st.ReplacePushKey(vapidSetting, encodeVAPID(v))
	if err != nil {
		return nil, 0, fmt.Errorf("换 VAPID 密钥: %w", err)
	}
	return v, n, nil
}

// pushService 把新请求推到手机：一个队列加一个工作协程，推送失败不影响请求主流程。工作协程在 New 里起（推送启用时），
// 不依赖 Run；stop（经 Server.stopBackground）停下并等它退出。
type pushService struct {
	s       *Server
	enabled bool // 配了 PublicURL
	// client 是唯一的发送客户端（含 /api/push/test）：签好的 VAPID 令牌缓存在它上面。HTTP 为空时用 push 包的默认客户端
	// （10 秒超时、不跟随重定向），测试换成信任假端点证书的客户端。
	client      *push.Client
	queue       chan store.Request
	sendTimeout time.Duration    // 每次发送的时限，测试可缩短
	now         func() time.Time // 失败计时与 TTL 用的时钟，测试可拨
	cancel      context.CancelFunc
	done        chan struct{} // 工作协程退出时关闭；没起工作协程时一开始就是关的

	// keyMu 串行化“读库里的密钥 → 比对 → 换进 client”。keySum 是 client.VAPID 对应的库里原值的 SHA-256：
	// 只存摘要，不存私钥原文，打印 pushService 也带不出私钥。
	keyMu  sync.Mutex
	keySum [sha256.Size]byte
	vapid  *push.VAPID
	// sendMu：Send 持读锁，换 client.VAPID 持写锁（push.Client 要求 VAPID 不与 Send 同时改）。
	sendMu sync.RWMutex
	// subMu 串行化订阅登记的“查条数上限 → 入库”（handlePushSubscribe）。
	subMu sync.Mutex
}

func newPushService(s *Server) (*pushService, error) {
	raw, v, err := loadVAPIDSetting(s.store)
	if err != nil {
		return nil, err
	}
	p := &pushService{s: s, enabled: s.cfg.PublicURL != "", client: &push.Client{VAPID: v, Subject: s.cfg.PublicURL},
		queue: make(chan store.Request, pushQueueSize), sendTimeout: defaultPushSendTimeout, now: time.Now, done: make(chan struct{}),
		keySum: sha256.Sum256([]byte(raw)), vapid: v}
	if !p.enabled {
		close(p.done)
		return p, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	go func() {
		defer close(p.done)
		p.run(ctx)
	}()
	return p, nil
}

// stop 取消工作协程并等它退出，至多等到 ctx 结束；ctx 已结束时再给 callbackStopGrace（被取消的那批推送要写完审计）。
// 宽限用完仍没退出时返回 false。推送停用时没有工作协程，立即返回 true。重复调用无害。
func (p *pushService) stop(ctx context.Context) bool {
	if p.cancel != nil {
		p.cancel()
	}
	select {
	case <-p.done:
		return true
	case <-ctx.Done():
	}
	timer := time.NewTimer(callbackStopGrace)
	defer timer.Stop()
	select {
	case <-p.done:
		return true
	case <-timer.C:
		p.s.log.Printf("hub 关停超时，推送工作协程未退出")
		return false
	}
}

// stopBackground 停下 New 里起的后台工作：webhook 回调（stopCallbacks）与推送工作协程，两者同时取消、并行等待，
// 各自至多等到 ctx 结束再加 callbackStopGrace，所以总共也不超过这个时限。Run 返回前与测试收尾都调它；重复调用无害。
func (s *Server) stopBackground(ctx context.Context) bool {
	pushOK := make(chan bool, 1)
	go func() { pushOK <- s.push.stop(ctx) }()
	cbOK := s.stopCallbacks(ctx)
	return <-pushOK && cbOK
}

// key 读库里当前的 VAPID 私钥：与手上的不同（另一个进程 railse hub vapid --rotate 换过）就解析并换进 client，
// 在跑的 hub 不用重启。每批推送、每次测试推送与 GET /api/push/vapid 各调一次；同一个值只解析一次。
// 调用方不得持有 sendMu。
func (p *pushService) key() (*push.VAPID, error) {
	p.keyMu.Lock()
	defer p.keyMu.Unlock()
	raw, ok, err := p.s.store.GetSetting(vapidSetting)
	if err != nil {
		return nil, fmt.Errorf("读 VAPID 密钥: %w", err)
	}
	if !ok {
		return nil, errors.New("库里没有 VAPID 密钥；运行 railse hub vapid 生成，或重启 hub")
	}
	sum := sha256.Sum256([]byte(raw))
	if sum == p.keySum {
		return p.vapid, nil
	}
	v, err := parseVAPIDSetting(raw)
	if err != nil {
		return nil, err
	}
	p.sendMu.Lock()
	p.client.VAPID = v
	p.sendMu.Unlock()
	p.keySum, p.vapid = sum, v
	p.s.log.Printf("VAPID 密钥已换新（公钥 %s），之后的推送用新密钥", v.PublicKeyB64())
	return v, nil
}

// announceNew 把新请求广播给网页（request_new），并在推送启用、值得推时入队推送。新请求出现的每一处都经它。
func (s *Server) announceNew(r store.Request) {
	s.clients.broadcast("request_new", s.view(r))
	s.push.enqueue(r)
}

// pushWorthy 判断一条新请求要不要推：permission、webhook 推（只有新登记的 pending 走到这里）；question 只推事件时间在
// pushQuestionMaxAge 之内的——守护进程断线重连后补发的旧审计批次，不该一下子弹出一串过时的“等你回答”；done 不推。
func pushWorthy(r store.Request, now time.Time) bool {
	switch r.Kind {
	case "permission", "webhook":
		return true
	case "question":
		t, err := time.Parse(time.RFC3339Nano, r.CreatedAt)
		return err == nil && now.Sub(t) <= pushQuestionMaxAge
	}
	return false
}

// enqueue 入队一条推送；队列满时丢弃并记日志，不阻塞调用方（它们在处理守护进程的帧或 HTTP 请求）。
func (p *pushService) enqueue(r store.Request) {
	if !p.enabled || !pushWorthy(r, time.Now()) {
		return
	}
	select {
	case p.queue <- r:
	default:
		p.s.log.Printf("推送队列已满（%d 条），丢弃请求 %s 的推送", pushQueueSize, clip(r.ID))
	}
}

// run 逐条推送直到 ctx 取消。
func (p *pushService) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-p.queue:
			p.deliver(ctx, r)
		}
	}
}

// deliver 把一条请求推给全部订阅（至多 pushWorkers 路并行，一个慢端点不拖住其余订阅），按结果维护订阅（sendOne 里
// 按端点的单条库操作，并行安全），全部发完后记一条审计 push_sent {"sent","gone","failed"}（没有订阅时不记）。
// 先列订阅、再读密钥：列出的订阅若是换钥之后登记的，读到的一定是新密钥。ctx 取消（hub 关停）时在途的中止、
// 还没轮到的不再发，都算 failed。
func (p *pushService) deliver(ctx context.Context, r store.Request) {
	if ctx.Err() != nil {
		return
	}
	subs, err := p.s.store.ListPushSubscriptions()
	if err != nil {
		p.s.log.Printf("列推送订阅: %v", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	if _, err := p.key(); err != nil {
		p.s.log.Printf("请求 %s 的推送没有发出: %v", clip(r.ID), err)
		return
	}
	msg, err := p.message(r)
	if err != nil {
		p.s.log.Printf("组请求 %s 的推送载荷: %v", clip(r.ID), err)
		return
	}
	o := push.Options{TTL: pushTTLFor(r, p.now()), Urgency: "high"}
	if push.ValidTopic(r.ID) {
		o.Topic = r.ID // 同一请求的未投递推送被替换
	}
	jobs := make(chan store.PushSubscription)
	results := make(chan pushOutcome, len(subs))
	var wg sync.WaitGroup
	for range min(pushWorkers, len(subs)) {
		wg.Go(func() {
			for sub := range jobs {
				if ctx.Err() != nil { // hub 关停：没轮到的不再发
					results <- pushFailed
					continue
				}
				out, _ := p.sendOne(ctx, sub, msg, o)
				results <- out
			}
		})
	}
	for _, sub := range subs {
		jobs <- sub
	}
	close(jobs)
	wg.Wait()
	close(results)
	var sent, gone, failed int
	for out := range results {
		switch out {
		case pushSent:
			sent++
		case pushGone:
			gone++
		default:
			failed++
		}
	}
	p.s.audit(r.MachineID, "system", r.ID, "push_sent", map[string]int{"sent": sent, "gone": gone, "failed": failed})
}

// pushTTLFor 是一条请求的投递 TTL：请求剩下的期限（expires_at − now，舍去不足 1 秒的零头），至多 pushTTL、至少 1 秒
// （过期后才送到的通知没有用；push 包要求 TTL 至少 1 秒）。没有期限（question）或期限读不出时用 pushTTL。
func pushTTLFor(r store.Request, now time.Time) time.Duration {
	if r.ExpiresAt == nil {
		return pushTTL
	}
	exp, err := time.Parse(time.RFC3339Nano, *r.ExpiresAt)
	if err != nil {
		return pushTTL
	}
	return min(pushTTL, max(exp.Sub(now).Truncate(time.Second), time.Second))
}

// pushOutcome 是推给一条订阅的结果。
type pushOutcome int

const (
	pushSent   pushOutcome = iota // 送达（2xx）
	pushGone                      // 订阅已删：端点回 404/410、密钥坏、连续失败到上限
	pushFailed                    // 没送达，订阅留着
)

// sendOne 用当前的 client 推给一条订阅，并按结果维护它：
//   - 2xx：MarkPushOK（连续失败归零）；
//   - 404、410（push.ErrGone）：立即删；库里的密钥解析不了：删；
//   - 本地错误（push.ErrLocal：载荷过大、Topic 不合法、签名出错等）、调用方 ctx 已取消（hub 关停、测试推送的请求断开）、
//     413、429：只记日志、不计失败——问题不在订阅；
//   - 其余（连不上、超时、5xx、其他 4xx）：MarkPushFailure 计一次；连续失败至少 pushMaxFailures 次、且这一轮的第一次失败
//     已过 pushFailureSpan 时在同一事务里删（只看次数的话，一阵短暂故障就能删光所有好订阅）。
//
// 删订阅都审计 push_subscription_removed {"id","origin","reason"}。日志只记订阅 id、端点的源与原因（含 StatusError.Reason），
// 不记端点路径与密钥；返回的错误文本同样不带端点地址（仍可 errors.Is 判断）。调用方不得持有 sendMu。
func (p *pushService) sendOne(ctx context.Context, sub store.PushSubscription, msg []byte, o push.Options) (pushOutcome, error) {
	origin := pushOrigin(sub.Endpoint)
	if sub.Broken {
		p.s.log.Printf("推送订阅 %s（%s）的密钥损坏，删除", sub.ID, origin)
		p.remove(sub, "invalid")
		return pushGone, errors.New("订阅的密钥损坏")
	}
	ps, err := push.ParseSubscription(sub.Endpoint, sub.P256dh, sub.Auth)
	if err != nil {
		p.s.log.Printf("推送订阅 %s（%s）无法解析，删除: %v", sub.ID, origin, err)
		p.remove(sub, "invalid")
		return pushGone, err
	}
	sctx, cancel := context.WithTimeout(ctx, p.sendTimeout)
	defer cancel()
	p.sendMu.RLock()
	status, err := p.client.Send(sctx, ps, msg, o)
	p.sendMu.RUnlock()
	err = redactURL(err)
	switch {
	case err == nil:
		if err := p.s.store.MarkPushOK(sub.Endpoint); err != nil {
			p.s.log.Printf("记推送订阅 %s 的成功: %v", sub.ID, err)
		}
		return pushSent, nil
	case errors.Is(err, push.ErrGone):
		p.s.log.Printf("推送订阅 %s（%s）已失效（端点回 %d），删除", sub.ID, origin, status)
		p.remove(sub, "gone")
		return pushGone, err
	case errors.Is(err, push.ErrLocal):
		p.s.log.Printf("推送到 %s（订阅 %s）没有发出，不计失败: %v", origin, sub.ID, err)
		return pushFailed, err
	case ctx.Err() != nil:
		p.s.log.Printf("推送到 %s（订阅 %s）中止（hub 关停或请求已取消），不计失败", origin, sub.ID)
		return pushFailed, err
	case status == 413 || status == 429:
		p.s.log.Printf("推送到 %s（订阅 %s）被拒，不计失败: %v", origin, sub.ID, err)
		return pushFailed, err
	}
	n, removed, merr := p.s.store.MarkPushFailure(sub.Endpoint, p.now(), store.PushFailureRule{Count: pushMaxFailures, Span: pushFailureSpan})
	if merr != nil {
		p.s.log.Printf("推送到 %s（订阅 %s）失败: %v；记失败次数出错: %v", origin, sub.ID, err, merr)
		return pushFailed, err
	}
	p.s.log.Printf("推送到 %s（订阅 %s）失败（连续第 %d 次）: %v", origin, sub.ID, n, err)
	if removed {
		p.s.log.Printf("推送订阅 %s（%s）连续失败 %d 次、已超过 %s，删除", sub.ID, origin, n, pushFailureSpan)
		p.s.audit("", "system", "", "push_subscription_removed", map[string]string{"id": sub.ID, "origin": origin, "reason": "failures"})
		return pushGone, err
	}
	return pushFailed, err
}

// remove 删一条订阅；真删了才审计。
func (p *pushService) remove(sub store.PushSubscription, reason string) {
	removed, err := p.s.store.DeletePushSubscription(sub.Endpoint)
	if err != nil {
		p.s.log.Printf("删推送订阅 %s: %v", sub.ID, err)
		return
	}
	if removed {
		p.s.audit("", "system", "", "push_subscription_removed", map[string]string{"id": sub.ID, "origin": pushOrigin(sub.Endpoint), "reason": reason})
	}
}

// pushOrigin 是推送端点的 scheme://host，日志与审计只记它：端点路径是订阅凭据的一部分。
func pushOrigin(endpoint string) string { return callbackTarget(endpoint) }

// redactedErr 的文本去掉了端点地址，Unwrap 仍给出原错误，errors.Is/As 照常判断。
type redactedErr struct {
	msg string
	err error
}

func (e *redactedErr) Error() string { return e.msg }
func (e *redactedErr) Unwrap() error { return e.err }

// redactURL 让错误文本不带端点地址：*url.Error 的文本带完整端点，换成它的底层原因。
func redactURL(err error) error {
	var ue *url.Error
	if err == nil || !errors.As(err, &ue) {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), ue.Error(), ue.Err.Error())
	if ue.URL != "" && strings.Contains(msg, ue.URL) {
		msg = ue.Err.Error()
	}
	return &redactedErr{msg: msg, err: err}
}

// pushPayload 是推送的明文：网页的 service worker 拿它 showNotification，点开去 url。
type pushPayload struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

// message 组一条请求的推送载荷（机器名、项目名取自请求视图）。
func (p *pushService) message(r store.Request) ([]byte, error) {
	v := p.s.view(r)
	return pushMessage(r, v.MachineName, v.Project)
}

// pushMessage 组推送载荷 {"id","kind","title","body","url":"/r/<id>"}：
//   - permission：标题“机器名 · 项目”（项目空时只有机器名），正文“工具名: 载荷摘要 · 风险 <risk>”；
//   - question：标题“机器名 · 项目：等你回答”，正文是问题；
//   - webhook：标题“webhook”，正文是来源给的标题。
//
// 标题、正文、摘要与风险都先并成一行（控制字符、换行当空白，去掉不可见的格式字符）再按字符截断；
// 编码后仍保证不超过 push.MaxPlaintext（见 encodePushPayload）。
func pushMessage(r store.Request, machine, project string) ([]byte, error) {
	where := joinNonEmpty(" · ", clean(machine, pushTitleLen), clean(project, pushTitleLen))
	if where == "" {
		where = "Railse"
	}
	var title, body string
	switch r.Kind {
	case "permission":
		title = where
		body = clean(r.ToolName+": "+payloadSummary(r.Payload), pushSummaryLen) + " · 风险 " + clean(r.Risk, pushRiskLen)
	case "question":
		const suffix = "：等你回答"
		title = truncate(where, pushTitleLen-utf8.RuneCountInString(suffix)) + suffix
		body = clean(r.Title, pushBodyLen)
	default:
		title = "webhook"
		body = clean(r.Title, pushBodyLen)
	}
	m := pushPayload{ID: r.ID, Kind: r.Kind, Title: truncate(title, pushTitleLen), Body: truncate(body, pushBodyLen), URL: "/r/" + url.PathEscape(r.ID)}
	return encodePushPayload(m, push.MaxPlaintext)
}

// payloadSummary 取载荷里最能说明这次操作的一段：shell 取命令、file_write 取路径、network 取 URL、mcp 取“服务/工具”。
func payloadSummary(pl policy.Payload) string {
	switch pl.Type {
	case policy.PayloadShell:
		return pl.Command
	case policy.PayloadFileWrite:
		return pl.Path
	case policy.PayloadNetwork:
		return pl.URL
	case policy.PayloadMCP:
		return strings.Trim(pl.Server+"/"+pl.Tool, "/")
	}
	return string(pl.Type)
}

// clean 把 s 并成一行并截到至多 n 个字符：控制字符与各种空白（含换行、U+2028）当空白，连续的并成一个空格，去掉首尾空白；
// 不可见的格式字符（零宽空格、双向覆盖等，ZWNJ、ZWJ 除外，与 ValidName 同一规则）去掉，免得通知文字被伪装。
// 先规整再截断：边走边数，凑够 n 个字符就停，开头塞满空白或零宽字符的命令照样显示出真正的内容；截断处不留尾随空格。
func clean(s string, n int) string {
	var b strings.Builder
	count, space := 0, false
	for _, c := range s { // 非法 UTF-8 按 U+FFFD 读出，输出总是合法 UTF-8
		switch {
		case unicode.IsControl(c) || unicode.IsSpace(c):
			space = count > 0
			continue
		case unicode.Is(unicode.Cf, c) && c != 0x200c && c != 0x200d:
			continue
		}
		need := 1
		if space {
			need = 2
		}
		if count+need > n {
			break
		}
		if space {
			b.WriteByte(' ')
			count++
			space = false
		}
		b.WriteRune(c)
		count++
	}
	return b.String()
}

func joinNonEmpty(sep string, parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
}

// encodePushPayload 编码载荷并保证不超过 limit 字节：JSON 转义能把一个字节涨到 6 个，所以量编码后的长度，
// 超了先缩正文，正文缩没了再缩标题；只剩 id、种类与 url 还放不下时报错（请求 id 至多 128 字节，实际走不到）。
func encodePushPayload(m pushPayload, limit int) ([]byte, error) {
	for {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(m); err != nil {
			return nil, err
		}
		b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
		if len(b) <= limit {
			return b, nil
		}
		switch {
		case m.Body != "":
			m.Body = shrink(m.Body)
		case m.Title != "":
			m.Title = shrink(m.Title)
		default:
			return nil, fmt.Errorf("推送载荷超过 %d 字节", limit)
		}
	}
}

// shrink 去掉 s 末尾四分之一的字符（至少一个）。
func shrink(s string) string {
	n := utf8.RuneCountInString(s)
	return truncate(s, n-max(n/4, 1))
}
