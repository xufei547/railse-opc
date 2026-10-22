package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/wire"
	"github.com/railse/railse-opc/internal/proto"
)

const webhookPath = "/api/webhook/approvals"

// webhookHub 起一个配好来源令牌的 hub：回调重试间隔缩到 10 毫秒。返回 hub、它的日志缓冲与来源令牌。
func webhookHub(t *testing.T) (*testHub, *syncBuf, string) {
	t.Helper()
	h := newTestHubWith(t, nil, func(s *Server) { s.callbackRetryWait = 10 * time.Millisecond })
	tok := store.RandomToken()
	if err := h.st.SetSetting("webhook_token_hash", store.HashToken(tok)); err != nil {
		t.Fatal(err)
	}
	return h, h.logs, tok
}

// ingestRaw 原样提交 body：auth 非空时作 Authorization 头；不设 Content-Type。
func ingestRaw(t *testing.T, h *testHub, auth string, body []byte) *http.Response {
	t.Helper()
	hdr := http.Header{}
	if auth != "" {
		hdr.Set("Authorization", auth)
	}
	return doReqHdr(t, "POST", h.http.URL+webhookPath, body, "", nil, hdr)
}

// ingest 用来源令牌提交一条审批请求，要求 200，返回新请求的 id。
func ingest(t *testing.T, h *testHub, tok string, body map[string]any) string {
	t.Helper()
	resp := ingestRaw(t, h, "Bearer "+tok, mustJSON(t, body))
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("ingest = %d %s", resp.StatusCode, b)
	}
	var out struct {
		ID string `json:"id"`
	}
	decodeBody(t, resp, &out)
	if out.ID == "" {
		t.Fatal("ingest 没回 id")
	}
	return out.ID
}

// cbCall 是测试服务收到的一次回调。
type cbCall struct {
	Path, Auth string
	Body       map[string]any
}

// cbTarget 是接收回调的测试服务，记下每次回调。
type cbTarget struct {
	*httptest.Server
	mu    sync.Mutex
	calls []cbCall
}

// newCBTarget 起一个收回调的服务：第 n 次（从 1 起）回调回 status(n)，3xx 时带 Location: /redirected。
func newCBTarget(t *testing.T, status func(n int) int) *cbTarget {
	t.Helper()
	c := &cbTarget{}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		c.mu.Lock()
		c.calls = append(c.calls, cbCall{Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Body: body})
		n := len(c.calls)
		c.mu.Unlock()
		code := status(n)
		if code >= 300 && code < 400 {
			w.Header().Set("Location", "/redirected")
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(c.Close)
	return c
}

func (c *cbTarget) got() []cbCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// callbackAudits 返回请求的 webhook_callback 审计的 detail（倒序）。
func callbackAudits(t *testing.T, st *store.Store, id string) []map[string]any {
	t.Helper()
	events, err := st.ListAudit(0, id)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, e := range events {
		if e.Action != "webhook_callback" {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal([]byte(e.Detail), &d); err != nil {
			t.Fatalf("审计 detail 不是 JSON: %q", e.Detail)
		}
		out = append(out, d)
	}
	return out
}

// allAuditText 把库里全部审计行拼成一段文本，用来查有没有泄漏。
func allAuditText(t *testing.T, st *store.Store) string {
	t.Helper()
	events, err := st.ListAudit(10000, "")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range events {
		fmt.Fprintf(&b, "%s %s %s %s\n", e.MachineID, e.Actor, e.Action, e.Detail)
	}
	return b.String()
}

// 来源用令牌提交审批请求：进收件箱、广播 request_new（不带回调）；用户决定后 hub 带回调令牌回调来源，
// 成功后清掉库里的令牌、记送达时间与审计。
func TestWebhookIngestAndCallback(t *testing.T) {
	h, logs, tok := webhookHub(t)
	ck := loggedIn(t, h)
	target := newCBTarget(t, func(int) int { return http.StatusOK })
	sub, stop := h.srv.clients.subscribe()
	defer stop()

	body := map[string]any{"title": "发布周报", "description": "n8n 等待批准", "callback_url": target.URL + "/cb", "callback_token": "cbtok"}
	if resp := ingestRaw(t, h, "", mustJSON(t, body)); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401, 得到 %d", resp.StatusCode)
	}
	before := time.Now()
	id := ingest(t, h, tok, body)
	r := getReq(t, h.st, id)
	if r.Kind != "webhook" || r.State != "pending" || r.Title != "发布周报" || r.MachineID != "webhook" || r.ToolName != "webhook" ||
		r.Payload.Type != "other" || r.Payload.Tool != "webhook" || r.Payload.DiffPreview != "n8n 等待批准" {
		t.Fatalf("请求 = %+v", r)
	}
	if r.ExpiresAt == nil || *r.ExpiresAt < store.FormatTime(before.Add(24*time.Hour)) || *r.ExpiresAt > store.FormatTime(time.Now().Add(24*time.Hour)) {
		t.Fatalf("默认应 24 小时后过期: %v", r.ExpiresAt)
	}
	if !strings.Contains(r.Callback, "cbtok") {
		t.Fatalf("回调前库里应有回调令牌: %q", r.Callback)
	}
	data := waitBroadcast(t, sub, "request_new", id, nil)
	if _, ok := data["machine_name"]; !ok || data["title"] != "发布周报" || data["kind"] != "webhook" {
		t.Fatalf("request_new 应是请求视图: %+v", data)
	}
	if b := mustJSON(t, data); strings.Contains(string(b), "cbtok") || strings.Contains(string(b), "callback") {
		t.Fatalf("广播不应带回调: %s", b)
	}

	resp := doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck)
	raw, _ := io.ReadAll(resp.Body)
	var inbox []map[string]any
	if err := json.Unmarshal(raw, &inbox); err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0]["title"] != "发布周报" || inbox[0]["id"] != id {
		t.Fatalf("inbox = %+v", inbox)
	}
	if strings.Contains(string(raw), "cbtok") {
		t.Fatalf("收件箱不应带回调令牌: %s", raw)
	}

	if resp := doReq(t, "POST", h.http.URL+"/api/requests/"+id+"/decide", []byte(`{"action":"allow","note":"去吧"}`), "application/json", ck); resp.StatusCode != http.StatusOK {
		t.Fatalf("decide = %d", resp.StatusCode)
	}
	waitFor(t, "回调审计", func() bool { return len(callbackAudits(t, h.st, id)) == 1 })
	// 终审 M2：回调成功记下送达时间后广播 request_updated，网页上的“已送达”随之更新
	if f := waitDelivered(t, sub, id); f["machine_name"] != "" || f["state"] != "approved" {
		t.Fatalf("送达后的推送应是请求视图: %+v", f)
	}
	calls := target.got()
	if len(calls) != 1 {
		t.Fatalf("应回调一次: %+v", calls)
	}
	cb := calls[0]
	if cb.Path != "/cb" || cb.Auth != "Bearer cbtok" || len(cb.Body) != 3 || cb.Body["id"] != id || cb.Body["decision"] != "allow" || cb.Body["note"] != "去吧" {
		t.Fatalf("回调 = %+v", cb)
	}
	if d := callbackAudits(t, h.st, id)[0]; d["ok"] != true || d["attempts"] != float64(1) || d["target"] != target.URL {
		t.Fatalf("回调审计 = %+v", d)
	}
	r = getReq(t, h.st, id)
	if strings.Contains(r.Callback, "cbtok") || !strings.Contains(r.Callback, target.URL+"/cb") {
		t.Fatalf("回调成功后应清掉令牌、保留 URL: %q", r.Callback)
	}
	if r.State != "approved" || r.DeliveredAt == nil {
		t.Fatalf("回调成功应记送达: %+v", r)
	}
	if strings.Contains(allAuditText(t, h.st), "cbtok") || strings.Contains(logs.String(), "cbtok") {
		t.Fatal("审计与日志不应含回调令牌")
	}
}

// 标题与描述入库前脱敏（与守护进程来源一致），审计与广播里也只有脱敏后的文本。
func TestWebhookRedactsText(t *testing.T) {
	h, _, tok := webhookHub(t)
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	id := ingest(t, h, tok, map[string]any{"title": "部署 password=hunter22x", "description": "用 --token tok-in-desc-123 推送",
		"callback_url": "https://n8n.example/cb"})
	r := getReq(t, h.st, id)
	if r.Title != "部署 password=<redacted>" || r.Payload.DiffPreview != "用 --token <redacted> 推送" {
		t.Fatalf("应脱敏: title=%q desc=%q", r.Title, r.Payload.DiffPreview)
	}
	data := waitBroadcast(t, sub, "request_new", id, nil)
	for what, text := range map[string]string{"审计": allAuditText(t, h.st), "广播": string(mustJSON(t, data))} {
		if strings.Contains(text, "hunter22x") || strings.Contains(text, "tok-in-desc-123") {
			t.Fatalf("%s不应含原文凭据: %s", what, text)
		}
	}
}

// 坏请求体一律 400，不登记请求；各字段的上限本身可以接受。
func TestWebhookRejectsBadBody(t *testing.T) {
	h, _, tok := webhookHub(t)
	ok := func(mod func(map[string]any)) map[string]any {
		b := map[string]any{"title": "t", "callback_url": "https://n8n.example/cb"}
		mod(b)
		return b
	}
	bad := []string{`{}`, `{"title":"x","callback_url":"ftp://x"}`, `not json`, "\xff\xfe"}
	for _, b := range []map[string]any{
		ok(func(b map[string]any) { delete(b, "callback_url") }),
		ok(func(b map[string]any) { delete(b, "title") }),
		ok(func(b map[string]any) { b["callback_url"] = "https://" }),
		ok(func(b map[string]any) { b["callback_url"] = "http://:80/cb" }),
		ok(func(b map[string]any) { b["callback_url"] = "/relative" }),
		ok(func(b map[string]any) { b["callback_url"] = "javascript:alert(1)" }),
		ok(func(b map[string]any) { b["title"] = strings.Repeat("标", 201) }),
		ok(func(b map[string]any) { b["title"] = "a\nb" }),
		ok(func(b map[string]any) { b["title"] = "a\x1b[31m" }),
		ok(func(b map[string]any) { b["title"] = "a\u0085b" }), // C1
		ok(func(b map[string]any) { b["title"] = "批准\u202egnp.exe" }),
		ok(func(b map[string]any) { b["title"] = "a\u2066b\u2069" }),
		ok(func(b map[string]any) { b["title"] = "a\u200bb" }),
		ok(func(b map[string]any) { b["title"] = "a\u200fb" }),
		ok(func(b map[string]any) { b["title"] = "\ufeffab" }),
		ok(func(b map[string]any) { b["description"] = strings.Repeat("述", 4097) }),
		ok(func(b map[string]any) {
			b["callback_url"] = "https://n8n.example/" + strings.Repeat("a", 2049-len("https://n8n.example/"))
		}),
		ok(func(b map[string]any) { b["callback_token"] = strings.Repeat("t", 513) }),
		ok(func(b map[string]any) { b["callback_token"] = "a b" }),
		ok(func(b map[string]any) { b["callback_token"] = "a\r\nX-Evil: 1" }),
		ok(func(b map[string]any) { b["expires_in"] = 0 }),
		ok(func(b map[string]any) { b["expires_in"] = -1 }),
		ok(func(b map[string]any) { b["expires_in"] = 86401 }),
		ok(func(b map[string]any) { b["expires_in"] = 1.5 }),
		ok(func(b map[string]any) { b["title"] = 5 }),
	} {
		bad = append(bad, string(mustJSON(t, b)))
	}
	for _, body := range bad {
		if resp := ingestRaw(t, h, "Bearer "+tok, []byte(body)); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%q 应 400, 得到 %d", body, resp.StatusCode)
		}
	}
	big := `{"title":"t","callback_url":"https://n8n.example/cb","description":"` + strings.Repeat("x", 64<<10) + `"}`
	if resp := ingestRaw(t, h, "Bearer "+tok, []byte(big)); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("超过 64 KiB 应 413, 得到 %d", resp.StatusCode)
	}
	if rs, _ := h.st.ListRequests("", 100); len(rs) != 0 {
		t.Fatalf("坏请求不应登记: %+v", rs)
	}
	// 上限本身可以接受
	for _, b := range []map[string]any{
		ok(func(b map[string]any) { b["title"] = strings.Repeat("标", 200) }),
		ok(func(b map[string]any) { b["description"] = "第一行\n第二行\t" + strings.Repeat("述", 4000) }),
		ok(func(b map[string]any) { b["description"] = strings.Repeat("述", 4096) }),
		ok(func(b map[string]any) {
			b["callback_url"] = "HTTP://n8n.example/" + strings.Repeat("a", 2048-len("http://n8n.example/"))
		}),
		ok(func(b map[string]any) { b["callback_token"] = strings.Repeat("t", 512) }),
		ok(func(b map[string]any) { b["expires_in"] = 1 }),
		ok(func(b map[string]any) { b["expires_in"] = 86400 }),
	} {
		ingest(t, h, tok, b)
	}
}

// 来源令牌：未配置 503；缺失、不对、空值、别的方案 401；方案名大小写不敏感、值去首尾空白；读设置出错 500 且不外露原文。
func TestWebhookAuth(t *testing.T) {
	h, logs := quietHub(t)
	body := mustJSON(t, map[string]any{"title": "t", "callback_url": "https://n8n.example/cb"})
	if resp := ingestRaw(t, h, "Bearer whatever", body); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未配置令牌应 503, 得到 %d", resp.StatusCode)
	}
	tok := store.RandomToken()
	if err := h.st.SetSetting("webhook_token_hash", store.HashToken(tok)); err != nil {
		t.Fatal(err)
	}
	for _, auth := range []string{"", "Bearer wrong", "Bearer ", "Bearer    ", "Basic " + tok, "Bearer" + tok, tok, "Bearer " + tok + "x"} {
		if resp := ingestRaw(t, h, auth, body); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Authorization %q 应 401, 得到 %d", auth, resp.StatusCode)
		}
	}
	for _, auth := range []string{"Bearer " + tok, "bearer " + tok, "BEARER   " + tok + "  "} {
		if resp := ingestRaw(t, h, auth, body); resp.StatusCode != http.StatusOK {
			t.Fatalf("Authorization %q 应 200, 得到 %d", auth, resp.StatusCode)
		}
	}
	execDB(t, h, "ALTER TABLE settings RENAME TO settings_gone")
	resp := ingestRaw(t, h, "Bearer "+tok, body)
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusInternalServerError || strings.Contains(string(raw), "settings") {
		t.Fatalf("读设置出错应 500 且不外露原文: %d %s", resp.StatusCode, raw)
	}
	if !strings.Contains(logs.String(), "webhook") {
		t.Fatalf("应记日志: %q", logs.String())
	}
}

// 回调失败重试：500、302（不跟随重定向）都算失败，第 3 次成功即止；3 次都失败时放弃、清掉令牌、不记送达、审计 ok:false；
// 连不上时错误里不带 URL。审计与日志里不出现回调令牌与 URL 的路径、查询串。
func TestWebhookCallbackRetries(t *testing.T) {
	h, logs, tok := webhookHub(t)
	statuses := []int{http.StatusInternalServerError, http.StatusFound, http.StatusOK}
	flaky := newCBTarget(t, func(n int) int { return statuses[min(n, 3)-1] })
	id := ingest(t, h, tok, map[string]any{"title": "t", "callback_url": flaky.URL + "/cb?k=qsecret", "callback_token": "cbtok1"})
	if _, err := h.srv.Decide(DecideInput{RequestID: id, Action: "deny", Note: "不行"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "flaky 回调审计", func() bool { return len(callbackAudits(t, h.st, id)) == 1 })
	calls := flaky.got()
	if len(calls) != 3 {
		t.Fatalf("应回调 3 次: %+v", calls)
	}
	for _, c := range calls {
		if c.Path != "/cb" || c.Auth != "Bearer cbtok1" || c.Body["decision"] != "deny" || c.Body["note"] != "不行" || c.Body["id"] != id {
			t.Fatalf("回调（不应跟随重定向）: %+v", c)
		}
	}
	if d := callbackAudits(t, h.st, id)[0]; d["ok"] != true || d["attempts"] != float64(3) {
		t.Fatalf("回调审计 = %+v", d)
	}
	if r := getReq(t, h.st, id); strings.Contains(r.Callback, "cbtok1") || r.DeliveredAt == nil {
		t.Fatalf("第 3 次成功后应清令牌、记送达: %+v", r)
	}

	down := newCBTarget(t, func(int) int { return http.StatusServiceUnavailable })
	id2 := ingest(t, h, tok, map[string]any{"title": "t", "callback_url": down.URL + "/cb?k=qsecret", "callback_token": "cbtok2"})
	if _, err := h.srv.Decide(DecideInput{RequestID: id2, Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "down 回调审计", func() bool { return len(callbackAudits(t, h.st, id2)) == 1 })
	if d := callbackAudits(t, h.st, id2)[0]; d["ok"] != false || d["attempts"] != float64(3) || d["error"] != "HTTP 503" || d["target"] != down.URL {
		t.Fatalf("回调审计 = %+v", d)
	}
	if n := len(down.got()); n != 3 {
		t.Fatalf("应恰好试 3 次: %d", n)
	}
	if r := getReq(t, h.st, id2); strings.Contains(r.Callback, "cbtok2") || !strings.Contains(r.Callback, down.URL) || r.DeliveredAt != nil {
		t.Fatalf("重试用尽应清令牌、保留 URL、不记送达: %+v", r)
	}

	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	id3 := ingest(t, h, tok, map[string]any{"title": "t", "callback_url": deadURL + "/cb?k=qsecret", "callback_token": "cbtok3"})
	if _, err := h.srv.Decide(DecideInput{RequestID: id3, Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "dead 回调审计", func() bool { return len(callbackAudits(t, h.st, id3)) == 1 })
	d := callbackAudits(t, h.st, id3)[0]
	if e, _ := d["error"].(string); d["ok"] != false || e == "" || strings.Contains(e, "/cb") || strings.Contains(e, "qsecret") {
		t.Fatalf("连不上的审计 = %+v", d)
	}
	for what, text := range map[string]string{"审计": allAuditText(t, h.st), "日志": logs.String()} {
		for _, leak := range []string{"cbtok", "qsecret", "/cb"} {
			if strings.Contains(text, leak) {
				t.Fatalf("%s不应含 %q: %s", what, leak, text)
			}
		}
	}
	if !strings.Contains(logs.String(), "放弃") {
		t.Fatalf("重试用尽应记日志: %q", logs.String())
	}
}

// expires_in 覆盖默认期限；hub 判定过期（扫描或决定时）也回调来源：deny、备注 expired，只有三个字段。
func TestWebhookExpiryCallsBack(t *testing.T) {
	h, _, tok := webhookHub(t)
	target := newCBTarget(t, func(int) int { return http.StatusOK })
	before := time.Now()
	id := ingest(t, h, tok, map[string]any{"title": "t", "callback_url": target.URL + "/cb", "expires_in": 60})
	r := getReq(t, h.st, id)
	if r.ExpiresAt == nil || *r.ExpiresAt < store.FormatTime(before.Add(time.Minute)) || *r.ExpiresAt > store.FormatTime(time.Now().Add(time.Minute)) {
		t.Fatalf("expires_in 60 应一分钟后过期: %v", r.ExpiresAt)
	}
	h.srv.expireDue(time.Now().Add(30 * time.Second))
	if r := getReq(t, h.st, id); r.State != "pending" {
		t.Fatalf("未到期不应过期: %+v", r)
	}
	h.srv.expireDue(time.Now().Add(2 * time.Minute))
	if r := getReq(t, h.st, id); r.State != "expired" {
		t.Fatalf("应过期: %+v", r)
	}
	waitFor(t, "过期回调", func() bool { return len(callbackAudits(t, h.st, id)) == 1 })
	want := map[string]any{"id": id, "decision": "deny", "note": "expired"}
	if calls := target.got(); len(calls) != 1 || !mapsEqual(calls[0].Body, want) || calls[0].Auth != "" {
		t.Fatalf("过期回调 = %+v", calls)
	}
	if r := getReq(t, h.st, id); r.DeliveredAt != nil {
		t.Fatalf("过期请求不记送达时间（以审计为准）: %+v", r)
	}

	id2 := ingest(t, h, tok, map[string]any{"title": "t2", "callback_url": target.URL + "/cb", "callback_token": "cbtok"})
	execDB(t, h, fmt.Sprintf("UPDATE requests SET expires_at = '%s' WHERE id = '%s'", store.FormatTime(time.Now().Add(-time.Second)), id2))
	if _, err := h.srv.Decide(DecideInput{RequestID: id2, Action: "allow"}); !errors.Is(err, errExpired) {
		t.Fatalf("已过期的决定应 errExpired: %v", err)
	}
	waitFor(t, "决定时过期的回调", func() bool { return len(callbackAudits(t, h.st, id2)) == 1 })
	calls := target.got()
	want["id"] = id2
	if len(calls) != 2 || !mapsEqual(calls[1].Body, want) || calls[1].Auth != "Bearer cbtok" {
		t.Fatalf("决定时过期的回调 = %+v", calls)
	}
	if r := getReq(t, h.st, id2); r.State != "expired" || strings.Contains(r.Callback, "cbtok") || r.DeliveredAt != nil {
		t.Fatalf("过期回调成功后应清令牌、不记送达时间: %+v", r)
	}
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Run 关停时取消在跑的回调（重试等待立即结束）并等它退出：清掉令牌、不记送达、审计标 aborted、日志说明中止；
// 关停后才做出的决定不再发起回调，同样清令牌、审计 aborted（attempts 0）。
func TestWebhookCallbackAbortedOnShutdown(t *testing.T) {
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := DefaultConfig(dir)
	cfg.Listen = "127.0.0.1:0"
	var logs syncBuf
	srv, err := New(cfg, st, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	srv.callbackRetryWait = time.Hour
	target := newCBTarget(t, func(int) int { return http.StatusInternalServerError })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	waitFor(t, "Run 开始监听", func() bool { return strings.Contains(logs.String(), "hub 监听 ") })
	cb := mustJSON(t, map[string]string{"url": target.URL + "/cb", "token": "cbtok"})
	for _, id := range []string{"w1", "w2"} {
		insertReq(t, st, store.Request{ID: id, MachineID: "webhook", Kind: "webhook", State: "pending", Title: "t", Callback: string(cb)})
	}
	if _, err := srv.Decide(DecideInput{RequestID: "w1", Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "第一次回调", func() bool { return len(target.got()) == 1 })
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回 %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 Run 未返回")
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("重试等待应被取消, Run 用了 %s", el)
	}
	if n := srv.callbacks.count(); n != 0 {
		t.Fatalf("Run 返回时回调应都已退出: %d", n)
	}
	ds := callbackAudits(t, st, "w1")
	if len(ds) != 1 || ds[0]["ok"] != false || ds[0]["aborted"] != true || ds[0]["attempts"] != float64(1) {
		t.Fatalf("中止的回调审计 = %+v", ds)
	}
	if r := getReq(t, st, "w1"); strings.Contains(r.Callback, "cbtok") || !strings.Contains(r.Callback, target.URL) || r.DeliveredAt != nil {
		t.Fatalf("中止的回调应清令牌、保留 URL、不记送达: %+v", r)
	}
	if !strings.Contains(logs.String(), "中止") {
		t.Fatalf("应记中止日志: %q", logs.String())
	}
	if _, err := srv.Decide(DecideInput{RequestID: "w2", Action: "deny"}); err != nil {
		t.Fatal(err)
	}
	if n := len(target.got()); n != 1 || srv.callbacks.count() != 0 {
		t.Fatalf("关停后不应再发起回调: %d 次", n)
	}
	ds = callbackAudits(t, st, "w2")
	if len(ds) != 1 || ds[0]["ok"] != false || ds[0]["aborted"] != true || ds[0]["attempts"] != float64(0) {
		t.Fatalf("关停后没发出的回调审计 = %+v", ds)
	}
	if r := getReq(t, st, "w2"); strings.Contains(r.Callback, "cbtok") || r.DeliveredAt != nil {
		t.Fatalf("关停后没发出的回调也应清令牌: %+v", r)
	}
	if !strings.Contains(logs.String(), "已试 0 次") {
		t.Fatalf("关停后的回调应记日志: %q", logs.String())
	}
}

// 回调已拿到 2xx 时恰好开始关停（回调运行期已取消）：照常收尾，清令牌、记送达、审计 ok:true。
func TestWebhookCallbackSuccessDuringShutdown(t *testing.T) {
	h, _, tok := webhookHub(t)
	target := newCBTarget(t, func(int) int { return http.StatusOK })
	h.srv.afterCallbackPost = func() { h.srv.cbCancel() }
	id := ingest(t, h, tok, map[string]any{"title": "t", "callback_url": target.URL + "/cb", "callback_token": "cbtok"})
	if _, err := h.srv.Decide(DecideInput{RequestID: id, Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "回调审计", func() bool { return len(callbackAudits(t, h.st, id)) == 1 })
	if d := callbackAudits(t, h.st, id)[0]; d["ok"] != true || d["attempts"] != float64(1) {
		t.Fatalf("回调审计 = %+v", d)
	}
	if r := getReq(t, h.st, id); strings.Contains(r.Callback, "cbtok") || r.DeliveredAt == nil {
		t.Fatalf("拿到 2xx 后应清令牌、记送达: %+v", r)
	}
}

// 决定与过期先后发生：只有先结束请求的一方回调，来源只收到一次。这里是顺序执行，不是真并发：守护的是
// 状态迁移用条件 UPDATE（WHERE state = 'pending'）只会成功一次，所以回调也只发起一次。
func TestWebhookDecideExpireOnlyOneCallback(t *testing.T) {
	h, _, tok := webhookHub(t)
	target := newCBTarget(t, func(int) int { return http.StatusOK })
	callsFor := func(id string) []cbCall {
		var out []cbCall
		for _, c := range target.got() {
			if c.Body["id"] == id {
				out = append(out, c)
			}
		}
		return out
	}
	settle := func(what, id string) {
		t.Helper()
		waitFor(t, what, func() bool { return len(callbackAudits(t, h.st, id)) >= 1 && h.srv.callbacks.count() == 0 })
	}
	body := map[string]any{"title": "t", "callback_url": target.URL + "/cb"}

	// 先过期再决定：决定回 ErrNotPending，不再回调
	id1 := ingest(t, h, tok, body)
	h.srv.expireDue(time.Now().Add(25 * time.Hour))
	settle("过期回调", id1)
	if _, err := h.srv.Decide(DecideInput{RequestID: id1, Action: "allow"}); !errors.Is(err, store.ErrNotPending) {
		t.Fatalf("已过期的请求再决定应 ErrNotPending: %v", err)
	}
	settle("决定之后", id1)
	if calls := callsFor(id1); len(calls) != 1 || calls[0].Body["note"] != "expired" || len(callbackAudits(t, h.st, id1)) != 1 {
		t.Fatalf("先过期：来源应只收到一次过期回调: %+v", calls)
	}

	// 先决定再过期：扫描不再动它，不再回调
	id2 := ingest(t, h, tok, body)
	if _, err := h.srv.Decide(DecideInput{RequestID: id2, Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	settle("决定回调", id2)
	h.srv.expireDue(time.Now().Add(25 * time.Hour))
	settle("扫描之后", id2)
	if calls := callsFor(id2); len(calls) != 1 || calls[0].Body["decision"] != "allow" || len(callbackAudits(t, h.st, id2)) != 1 {
		t.Fatalf("先决定：来源应只收到一次决定回调: %+v", calls)
	}
	if r := getReq(t, h.st, id2); r.State != "approved" {
		t.Fatalf("已决定的请求不应被判过期: %+v", r)
	}
}

// webhook 请求（machine_id 为 webhook）不受机器侧流程影响：不在机器列表里；守护进程的 hello 对齐、cancel、expired、
// 同 id 的 pending 与审计都改不了它；“记住”回 422。
func TestWebhookRequestIsolatedFromMachines(t *testing.T) {
	h, _, tok := webhookHub(t)
	ck := loggedIn(t, h)
	id := ingest(t, h, tok, map[string]any{"title": "t", "callback_url": "https://n8n.example/cb", "callback_token": "cbtok"})
	mid, secret := enrollMachine(t, h, "m")
	a := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: mid})
	defer a.close()
	a.recv(5 * time.Second) // welcome；hello 里没有 pending，对齐不应取消 webhook 请求
	sess := proto.Session{Tool: "claude", CLISessionID: "s1"}
	a.send(pendingFrame(id, "s1", "t1", "evil"))
	if kind, raw := a.recv(5 * time.Second); kind != wire.TError || !strings.Contains(string(raw), id) {
		t.Fatalf("同 id 的 pending 应回 Error: %s %s", kind, raw)
	}
	a.send(wire.Cancel{T: wire.TCancel, RequestID: id, Reason: "terminal"})
	a.send(wire.Expired{T: wire.TExpired, RequestID: id})
	a.send(auditFrame(t, agent.AuditEntry{TS: time.Now(), RequestID: id, Kind: "permission", Session: sess, State: "cancelled", DecidedBy: "terminal"}))
	a.send(pendingFrame("rb", "s2", "t2", "ok")) // 同一连接按序处理：它登记了，前面几帧也处理完了
	waitFor(t, "rb 登记", func() bool { _, err := h.st.GetRequest("rb"); return err == nil })
	if r := getReq(t, h.st, id); r.State != "pending" || r.MachineID != "webhook" || !strings.Contains(r.Callback, "cbtok") {
		t.Fatalf("守护进程的帧不应改动 webhook 请求: %+v", r)
	}
	if acts := auditActions(t, h.st, id); slices.Contains(acts, "cancelled") {
		t.Fatalf("守护进程的审计不应挂到 webhook 请求上: %v", acts)
	}

	var status struct {
		Pending  int              `json:"pending"`
		Machines []map[string]any `json:"machines"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/status", nil, "", ck), &status)
	if status.Pending != 2 || len(status.Machines) != 1 || status.Machines[0]["id"] != mid {
		t.Fatalf("status = %+v", status)
	}
	var machines []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/machines", nil, "", ck), &machines)
	if len(machines) != 1 {
		t.Fatalf("machines = %+v", machines)
	}

	resp := doReq(t, "POST", h.http.URL+"/api/requests/"+id+"/decide", decideBody(t, "allow", true, ""), "application/json", ck)
	var e map[string]string
	decodeBody(t, resp, &e)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(e["error"], "webhook") {
		t.Fatalf("记住 webhook 请求应 422: %d %+v", resp.StatusCode, e)
	}
	if r := getReq(t, h.st, id); r.State != "pending" {
		t.Fatalf("记不住时应保持 pending: %+v", r)
	}
	if rows, _ := ruleRows(t, h.st); len(rows) != 2 {
		t.Fatalf("不应学规则: %+v", rows)
	}
}

// 关停时前面的 HTTP 收尾（一个不理会取消的慢请求）把共用截止时间耗尽：取消回调后仍给 callbackStopGrace 收尾，
// Run 返回时回调已退出、令牌已清、审计标 aborted；总关停不超过 shutdownTimeout + callbackStopGrace。
func TestWebhookCallbackGraceAfterSlowShutdown(t *testing.T) {
	old := shutdownTimeout
	shutdownTimeout = 100 * time.Millisecond
	t.Cleanup(func() { shutdownTimeout = old })
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := DefaultConfig(dir)
	cfg.Listen = "127.0.0.1:0"
	var logs syncBuf
	srv, err := New(cfg, st, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	srv.callbackRetryWait = time.Hour
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	srv.mux.HandleFunc("GET /test/slow", func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-release // 不理会取消：hs.Shutdown 只能等到截止再强制断开
	})
	target := newCBTarget(t, func(int) int { return http.StatusInternalServerError })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	const prefix = "hub 监听 "
	waitFor(t, "Run 开始监听", func() bool { return strings.Contains(logs.String(), prefix) })
	line := logs.String()
	addr := strings.TrimSpace(strings.SplitN(line[strings.Index(line, prefix)+len(prefix):], "\n", 2)[0])

	cb := mustJSON(t, map[string]string{"url": target.URL + "/cb", "token": "cbtok"})
	insertReq(t, st, store.Request{ID: "w1", MachineID: "webhook", Kind: "webhook", State: "pending", Title: "t", Callback: string(cb)})
	if _, err := srv.Decide(DecideInput{RequestID: "w1", Action: "allow"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "第一次回调（之后进入一小时的重试等待）", func() bool { return len(target.got()) == 1 })
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		if resp, err := http.Get("http://" + addr + "/test/slow"); err == nil {
			resp.Body.Close()
		}
	}()
	defer func() {
		close(release)
		<-clientDone
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("慢请求没有进到处理器")
	}

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回 %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 Run 未返回")
	}
	el := time.Since(start)
	if n := srv.callbacks.count(); n != 0 {
		t.Fatalf("Run 返回时回调应都已退出: %d", n)
	}
	if r := getReq(t, st, "w1"); strings.Contains(r.Callback, "cbtok") {
		t.Fatalf("Run 返回时令牌应已清: %q", r.Callback)
	}
	if ds := callbackAudits(t, st, "w1"); len(ds) != 1 || ds[0]["aborted"] != true {
		t.Fatalf("中止的回调审计 = %+v", ds)
	}
	if !strings.Contains(logs.String(), "强制断开") || el < shutdownTimeout {
		t.Fatalf("前面的 HTTP 收尾应耗尽截止时间（用时 %s）: %q", el, logs.String())
	}
	if el > shutdownTimeout+callbackStopGrace+2*time.Second {
		t.Fatalf("总关停应不超过 shutdownTimeout + callbackStopGrace: 用时 %s", el)
	}
}
