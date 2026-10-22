package hub

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/redact"
)

// webhookMachineID 是 webhook 请求的 machine_id。它不是任何机器（机器 id 是 32 个十六进制字符，撞不上）：
// 守护进程的帧、重连对齐、送达回执、吊销都按机器 id 过滤，碰不到 webhook 请求。
const webhookMachineID = "webhook"

const (
	webhookBodyMax      = 64 << 10
	maxWebhookTitle     = 200  // 字符
	maxWebhookDesc      = 4096 // 字符
	maxCallbackURL      = 2048 // 字节
	maxCallbackToken    = 512  // 字节
	webhookDefaultTTL   = 24 * time.Hour
	maxWebhookExpiresIn = 86400 // 秒
	callbackAttempts    = 3
	callbackTimeout     = 10 * time.Second
	callbackRespMax     = 64 << 10 // 回调响应体至多读这么多再关
	// defaultCallbackRetryWait 是回调失败后重试前的等待（Server.callbackRetryWait 的默认值）。
	defaultCallbackRetryWait = 2 * time.Second
)

// webhookCallback 是 requests.callback 里存的 JSON。Token 是出站回调令牌，hub 唯一明文存的凭据（用户决定 D）：
// 回调成功或重试用尽后由 store.ClearCallbackToken 删掉 token 键，URL 留着。
type webhookCallback struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

// newCallbackClient 给出发回调的 HTTP 客户端：整体超时 callbackTimeout，不跟随重定向（3xx 原样返回，按失败算）。
func newCallbackClient() *http.Client {
	return &http.Client{Timeout: callbackTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// authWebhook 校验来源令牌（settings 的 webhook_token_hash，由 railse hub webhook-token 生成，只存哈希）。
// 未配置令牌回 503，读设置出错回 500，令牌缺失或不对回 401；返回 false 时已写好响应。
func (s *Server) authWebhook(w http.ResponseWriter, r *http.Request) bool {
	want, ok, err := s.store.GetSetting("webhook_token_hash")
	if err != nil {
		s.internalErr(w, "读 webhook 令牌哈希", err)
		return false
	}
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "未配置 webhook 令牌，请在服务器上运行 railse hub webhook-token")
		return false
	}
	tok, ok := bearerToken(r)
	if !ok || subtle.ConstantTimeCompare([]byte(store.HashToken(tok)), []byte(want)) != 1 {
		writeErr(w, http.StatusUnauthorized, "来源令牌无效")
		return false
	}
	return true
}

// webhookBody 是 POST /api/webhook/approvals 的请求体。ExpiresIn 可选（秒），不给时 24 小时后过期。
type webhookBody struct {
	Title         string `json:"title"`
	Description   string `json:"description"`
	CallbackURL   string `json:"callback_url"`
	CallbackToken string `json:"callback_token"`
	ExpiresIn     *int64 `json:"expires_in"`
}

// validate 检查字段，返回给来源看的错误说明；合格时返回空串。回调地址只允许 http、https，不做私网限制（n8n 常与 hub 在同一台服务器）。
func (b webhookBody) validate() string {
	switch {
	case b.Title == "" || b.CallbackURL == "":
		return "需要 title 与 callback_url"
	case utf8.RuneCountInString(b.Title) > maxWebhookTitle:
		return fmt.Sprintf("title 至多 %d 个字符", maxWebhookTitle)
	case !validName(b.Title):
		return "title 不能含控制字符"
	case utf8.RuneCountInString(b.Description) > maxWebhookDesc:
		return fmt.Sprintf("description 至多 %d 个字符", maxWebhookDesc)
	case len(b.CallbackURL) > maxCallbackURL:
		return fmt.Sprintf("callback_url 至多 %d 字节", maxCallbackURL)
	case len(b.CallbackToken) > maxCallbackToken:
		return fmt.Sprintf("callback_token 至多 %d 字节", maxCallbackToken)
	case !visibleASCII(b.CallbackToken):
		return "callback_token 只能含可见 ASCII 字符"
	case b.ExpiresIn != nil && (*b.ExpiresIn < 1 || *b.ExpiresIn > maxWebhookExpiresIn):
		return fmt.Sprintf("expires_in 须在 1 到 %d 秒之间", maxWebhookExpiresIn)
	}
	u, err := url.Parse(b.CallbackURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return "callback_url 必须是 http 或 https 地址"
	}
	return ""
}

// visibleASCII 报告 s 是否只含可见 ASCII 字符（0x21–0x7e）。回调令牌要放进 Authorization 头，空白与控制字符会让每次回调都失败。
func visibleASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// callbackTarget 给出回调地址的 scheme://host，日志与审计只记它（路径、查询串里可能有秘密）。
func callbackTarget(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// handleWebhookIngest 收外部系统（如 n8n）的审批请求：{"title","description","callback_url","callback_token","expires_in"}。
// 登记一条 kind webhook 的 pending（标题与描述先脱敏），审计、广播 request_new，回 {"id"}。
// 坏请求体或字段不合格 400，超过 64 KiB 413。
func (s *Server) handleWebhookIngest(w http.ResponseWriter, r *http.Request) {
	if !s.authWebhook(w, r) {
		return
	}
	var body webhookBody
	if err := decodeJSON(w, r, webhookBodyMax, &body); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "请求体至多 64 KiB")
			return
		}
		writeErr(w, http.StatusBadRequest, "请求体不是合法 JSON，或字段类型不对")
		return
	}
	if msg := body.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	now := time.Now()
	ttl := webhookDefaultTTL
	if body.ExpiresIn != nil {
		ttl = time.Duration(*body.ExpiresIn) * time.Second
	}
	expires := store.FormatTime(now.Add(ttl))
	title := redact.Text(body.Title)
	cb, _ := json.Marshal(webhookCallback{URL: body.CallbackURL, Token: body.CallbackToken}) // 两个字符串，编码不会失败
	req := store.Request{ID: store.NewID(), MachineID: webhookMachineID, Kind: "webhook", ToolName: "webhook",
		Payload: policy.Payload{Type: "other", Tool: "webhook", DiffPreview: redact.Text(body.Description)}, Risk: "unknown",
		State: "pending", Title: title, Callback: string(cb), CreatedAt: store.FormatTime(now), ExpiresAt: &expires}
	if err := s.store.InsertRequest(req); err != nil {
		s.internalErr(w, "登记 webhook 请求", err)
		return
	}
	s.audit(webhookMachineID, "system", req.ID, "request_pending", map[string]string{"title": title, "callback": callbackTarget(body.CallbackURL)})
	s.clients.broadcast("request_new", s.view(req))
	writeJSON(w, http.StatusOK, map[string]string{"id": req.ID})
}

// callbackWebhook 在后台把 webhook 请求 r 的结果回传给来源：POST callback_url，带 Authorization: Bearer <callback_token>（有令牌时），
// body {"id","decision":"allow|deny","note"}；hub 判定过期时 decision 为 deny、note 为 expired。回调由 runCallback 执行。
// hub 正在关停时不再发起，只记日志（hub 重启后不恢复未完成的回调）。
func (s *Server) callbackWebhook(r store.Request) {
	var cb webhookCallback
	if err := json.Unmarshal([]byte(r.Callback), &cb); err != nil || cb.URL == "" {
		s.log.Printf("webhook 请求 %s 没有可用的回调地址，不回调", r.ID)
		return
	}
	decision, note := "deny", r.DecisionNote
	switch r.State {
	case "approved":
		decision = "allow"
	case "expired":
		note = "expired"
	}
	body, _ := json.Marshal(map[string]string{"id": r.ID, "decision": decision, "note": note}) // 只有字符串，编码不会失败
	if !s.callbacks.enter() {
		s.log.Printf("hub 正在关停，webhook 请求 %s 的回调（%s）未发出；hub 重启后不会补发", r.ID, callbackTarget(cb.URL))
		return
	}
	go func() {
		defer s.callbacks.leave()
		s.runCallback(r.ID, cb, body)
	}()
}

// runCallback 最多试 callbackAttempts 次，失败后等 callbackRetryWait 再试；只有 2xx 算成功。
// 成功：清掉库里的回调令牌、记送达时间，审计 webhook_callback ok:true。重试用尽：记日志、清令牌，审计 ok:false。
// 审计在最后写，看到审计时库里的改动已经做完。
// cbCtx 取消（hub 关停）时立即中止：之后只记日志与审计（ok:false、aborted:true），令牌留在库里（hub 重启后不恢复未完成的回调）。
// 日志与审计只记回调地址的 scheme://host，不记令牌。
func (s *Server) runCallback(id string, cb webhookCallback, body []byte) {
	ctx := s.cbCtx
	target := callbackTarget(cb.URL)
	var lastErr string
	for attempt := 1; attempt <= callbackAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
			case <-time.After(s.callbackRetryWait):
			}
		}
		if ctx.Err() != nil {
			s.callbackAborted(id, target, attempt-1)
			return
		}
		err := s.postCallback(ctx, cb, body)
		if err == nil {
			if ctx.Err() == nil {
				s.clearCallbackToken(id)
				if err := s.store.MarkDelivered(webhookMachineID, id); err != nil {
					s.log.Printf("标记 webhook 请求 %s 已回调: %v", id, err)
				}
			}
			s.audit(webhookMachineID, "system", id, "webhook_callback", map[string]any{"ok": true, "attempt": attempt, "target": target})
			return
		}
		if ctx.Err() != nil {
			s.callbackAborted(id, target, attempt)
			return
		}
		lastErr = truncate(err.Error(), maxTextLen)
	}
	s.log.Printf("webhook 请求 %s 回调 %s 失败 %d 次，放弃: %s", id, target, callbackAttempts, lastErr)
	s.clearCallbackToken(id)
	s.audit(webhookMachineID, "system", id, "webhook_callback", map[string]any{"ok": false, "attempts": callbackAttempts, "error": lastErr, "target": target})
}

// callbackAborted 记下因 hub 关停而中止的回调（已试 attempts 次）。只写日志与审计。
func (s *Server) callbackAborted(id, target string, attempts int) {
	s.log.Printf("hub 关停，webhook 请求 %s 回调 %s 中止（已试 %d 次）；hub 重启后不会补发", id, target, attempts)
	s.audit(webhookMachineID, "system", id, "webhook_callback", map[string]any{"ok": false, "aborted": true, "attempts": attempts, "target": target})
}

func (s *Server) clearCallbackToken(id string) {
	if err := s.store.ClearCallbackToken(id); err != nil {
		s.log.Printf("清除 webhook 请求 %s 的回调令牌: %v", id, err)
	}
}

// postCallback 发一次回调。只有 2xx 算成功；响应体至多读 callbackRespMax 字节后关闭。
// 返回的错误不带 URL：*url.Error 会把完整地址（查询串里可能有秘密）写进错误文本，这里只留底层原因。
func (s *Server) postCallback(ctx context.Context, cb webhookCallback, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cb.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("回调地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	if cb.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cb.Token)
	}
	resp, err := s.callbackClient.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return err
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, callbackRespMax))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// stopCallbacks 不再接纳新的回调，取消在跑的（重试等待与在途请求立即结束），至多等 shutdownTimeout 让它们退出。
// Run 返回前调用；重复调用无害。
func (s *Server) stopCallbacks() {
	idle := s.callbacks.shutdown()
	s.cbCancel()
	timer := time.NewTimer(shutdownTimeout)
	defer timer.Stop()
	select {
	case <-idle:
	case <-timer.C:
		s.log.Printf("hub 关停超时，仍有 %d 个 webhook 回调未退出", s.callbacks.count())
	}
}
