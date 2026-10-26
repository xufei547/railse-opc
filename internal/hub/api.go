package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"gopkg.in/yaml.v3"

	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/policy"
)

const (
	decideBodyMax    = 16 << 10 // decide 请求体：备注至多 1000 字符，JSON 转义后也远小于它
	proposeBodyMax   = 1 << 10  // propose 请求体只有 action 与 scope
	patchBodyMax     = 4 << 10
	rulesImportMax   = 1 << 20 // 导入的 YAML 至多 1 MiB
	requestsLimit    = 50      // GET /api/requests 的默认条数
	requestsLimitMax = 500
	inboxLimit       = 500
	questionsLimit   = 100            // 收件箱里“等你回答”的条数上限
	questionsWindow  = 24 * time.Hour // 收件箱只列这么久之内还在等回答的 question
	requestAuditMax  = 200            // GET /api/requests/{id} 带的审计条数
)

// apiRoutes 注册网页用的接口。全部要登录 cookie；改状态的（POST、PATCH、DELETE）先过 sameOrigin。
func (s *Server) apiRoutes() {
	read := s.requireClient
	write := func(h http.HandlerFunc) http.HandlerFunc { return sameOrigin(s.requireClient(h)) }
	s.mux.HandleFunc("GET /api/status", read(s.handleStatus))
	s.mux.HandleFunc("POST /api/pause", write(func(w http.ResponseWriter, r *http.Request) { s.handlePause(w, true) }))
	s.mux.HandleFunc("POST /api/resume", write(func(w http.ResponseWriter, r *http.Request) { s.handlePause(w, false) }))
	s.mux.HandleFunc("GET /api/inbox", read(s.handleInbox))
	s.mux.HandleFunc("GET /api/requests", read(s.handleRequests))
	s.mux.HandleFunc("GET /api/requests/{id}", read(s.handleRequest))
	s.mux.HandleFunc("POST /api/requests/{id}/propose", write(s.handlePropose))
	s.mux.HandleFunc("POST /api/requests/{id}/decide", write(s.handleDecide))
	s.mux.HandleFunc("GET /api/rules", read(s.handleRules))
	s.mux.HandleFunc("PATCH /api/rules/{id}", write(s.handleRulePatch))
	s.mux.HandleFunc("DELETE /api/rules/{id}", write(s.handleRuleDelete))
	s.mux.HandleFunc("POST /api/rules/import", write(s.handleRulesImport))
	s.mux.HandleFunc("GET /api/rules/export", read(s.handleRulesExport))
	s.mux.HandleFunc("GET /api/machines", read(s.handleMachines))
	s.mux.HandleFunc("DELETE /api/machines/{id}", write(s.handleMachineRevoke))
	// WebSocket 握手是 GET，但浏览器跨站也能发起（带 cookie），同样查 Origin
	s.mux.HandleFunc("GET /ws/client", sameOrigin(s.requireClient(s.handleClientWS)))
}

// sameOrigin 是 SameSite=Lax 之外的 CSRF 纵深防御：请求带 Origin 头而其 host 与 r.Host 不同（含 Origin: null）时回 403；
// 不带 Origin 的放行（非浏览器客户端，如 curl）。反向代理须把原 Host 头转给 hub（nginx 要 proxy_set_header Host $host），
// 否则同源的浏览器请求也会被拒。
func sameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
				writeErr(w, http.StatusForbidden, "跨站请求被拒绝")
				return
			}
		}
		next(w, r)
	}
}

// requestView 是接口与网页看到的请求：请求本身（不含载荷哈希与 webhook 回调）加机器名、项目名与 has_redacted。
// REST（/api/inbox 的 pending 与 questions、/api/requests、/api/requests/{id}、decide 的返回）与 /ws/client 的 request_new、
// request_updated 都用它。
// HasRedacted 为 true 表示载荷里有被脱敏抹掉的部分：网页展示的不是原文（命令行语境的脱敏可能连带吞掉紧贴的后半截命令），
// 必须醒目告警，建议去终端核对。
type requestView struct {
	store.Request
	MachineName string `json:"machine_name"`
	Project     string `json:"project"`
	HasRedacted bool   `json:"has_redacted"`
}

// payloadHasRedacted 报告库里的载荷有没有脱敏占位符。库里的载荷入库前都经 redact.Payload，字符串字段里的占位符是字面；
// MCP 参数是原始 JSON，存库时 < > 被转义成 \u003c \u003e，两种写法都认。只做字面查找，不再跑脱敏正则（列表一次至多 500 条）。
// 编码失败当作有（宁可多告警）。
func payloadHasRedacted(p policy.Payload) bool {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return true
	}
	b := buf.Bytes()
	return bytes.Contains(b, []byte(redactedMark)) || bytes.Contains(b, []byte(`\u003credacted\u003e`))
}

// requestHasRedacted 报告请求展示给人看的内容有没有脱敏占位符：载荷，或标题（question 的消息、webhook 的标题，入库前都已脱敏）。
func requestHasRedacted(r store.Request) bool {
	return strings.Contains(r.Title, redactedMark) || payloadHasRedacted(r.Payload)
}

// views 给请求配上机器名（每台机器只查一次；查不到时留空，读库出错记日志）、项目名与 has_redacted。
func (s *Server) views(rs []store.Request) []requestView {
	names := map[string]string{}
	out := make([]requestView, 0, len(rs))
	for _, r := range rs {
		name, ok := names[r.MachineID]
		if !ok {
			if m, err := s.store.GetMachine(r.MachineID); err == nil {
				name = m.Name
			} else if !errors.Is(err, store.ErrNotFound) {
				s.log.Printf("读机器 %s 的名字: %v", r.MachineID, err)
			}
			names[r.MachineID] = name
		}
		out = append(out, requestView{Request: r, MachineName: name, Project: projectName(r.ProjectRoot), HasRedacted: requestHasRedacted(r)})
	}
	return out
}

func (s *Server) view(r store.Request) requestView { return s.views([]store.Request{r})[0] }

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	paused, err := s.paused()
	if err != nil {
		s.internalErr(w, "读暂停状态", err)
		return
	}
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
	machines, err := s.store.ListMachines()
	if err != nil {
		s.internalErr(w, "列机器", err)
		return
	}
	ms := make([]map[string]any, 0, len(machines))
	for _, m := range machines {
		ms = append(ms, map[string]any{"id": m.ID, "name": m.Name, "online": s.agents.connected(m.ID), "last_seen": m.LastSeen, "revoked": m.Revoked})
	}
	subs, err := s.store.CountPushSubscriptions()
	if err != nil {
		s.log.Printf("统计推送订阅: %v", err) // 不影响其余状态，按 0 回
		subs = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{"paused": paused, "pending": pending, "rules_version": version, "machines": ms,
		"uptime": time.Since(s.start).Round(time.Second).String(), "push_enabled": s.push.enabled, "push_subscriptions": subs})
}

func (s *Server) handlePause(w http.ResponseWriter, paused bool) {
	if err := s.setPaused(paused); err != nil {
		s.internalErr(w, "改总开关", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleInbox 返回 {"pending":[…],"questions":[…]}，都是请求视图、按创建时间倒序：pending 是全部待审批（至多 500 条），
// questions 是最近 24 小时还在等回答的 question（至多 100 条，见 store.OpenQuestions）。没有时是空数组，不是 null。
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	pending, err := s.store.ListRequests("pending", inboxLimit)
	if err != nil {
		s.internalErr(w, "列待审批", err)
		return
	}
	questions, err := s.store.OpenQuestions(time.Now().Add(-questionsWindow), questionsLimit)
	if err != nil {
		s.internalErr(w, "列待回答", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": s.views(pending), "questions": s.views(questions)})
}

// handleRequests 按创建时间倒序列请求：state 为空表示全部；limit 默认 50、最大 500，≤0 或不是数字时按默认。
func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = requestsLimit
	}
	limit = min(limit, requestsLimitMax)
	rs, err := s.store.ListRequests(r.URL.Query().Get("state"), limit)
	if err != nil {
		s.internalErr(w, "列请求", err)
		return
	}
	writeJSON(w, http.StatusOK, s.views(rs))
}

// handleRequest 返回 {"request": 请求, "audit": 该请求最近的审计（倒序，至多 200 条）}。
func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	req, err := s.store.GetRequest(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "没有这条请求")
		return
	}
	if err != nil {
		s.internalErr(w, "读请求", err)
		return
	}
	events, err := s.store.ListAudit(requestAuditMax, req.ID)
	if err != nil {
		s.internalErr(w, "读请求审计", err)
		return
	}
	if events == nil {
		events = []store.AuditEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"request": s.view(req), "audit": events})
}

// handleDecide 是网页的决定入口：{"action":"allow|deny","note":"…","remember":true,"scope":"project|global"}。
// scope 只在 remember 时起作用，缺省时有项目根记成项目、没有记成全局。
// 200 返回决定后的请求；坏请求体、坏 action、坏 scope、备注超过 1000 字符 400；其余错误见 writeDecideErr。
func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action   string `json:"action"`
		Note     string `json:"note"`
		Remember bool   `json:"remember"`
		Scope    string `json:"scope"`
	}
	if err := decodeJSON(w, r, decideBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	if utf8.RuneCountInString(body.Note) > maxTextLen {
		writeErr(w, http.StatusBadRequest, "备注至多 1000 个字符")
		return
	}
	req, err := s.Decide(DecideInput{RequestID: r.PathValue("id"), Action: body.Action, Note: body.Note, Remember: body.Remember, Scope: body.Scope})
	if err != nil {
		s.writeDecideErr(w, "决定请求", err)
		return
	}
	writeJSON(w, http.StatusOK, s.view(req))
}

// handlePropose 只计算“记住”会生成的规则，不入库、不审计、不改请求：网页在保存前展示草案并让用户选范围。
// 请求体 {"action":"allow|deny","scope":"project|global"}，scope 缺省时有项目根为 project、没有为 global。
// 200 返回 {"rule_id","scope","yaml","summary":{"action","payload","anchor"}}，scope 是草案实际的范围，yaml 与入库后
// /api/rules 里这条的 yaml 相同（rule_id 与已有规则重名时入库会加 -2 之类的后缀）。坏请求体、坏 action、坏 scope 400；
// 其余错误与 decide 相同（见 writeDecideErr）。
func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		Scope  string `json:"scope"`
	}
	if err := decodeJSON(w, r, proposeBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	rule, err := s.propose(r.PathValue("id"), body.Action, body.Scope)
	if err != nil {
		s.writeDecideErr(w, "生成规则草案", err)
		return
	}
	y, err := yaml.Marshal(rule)
	if err != nil {
		s.internalErr(w, "编码规则草案", err)
		return
	}
	scope := scopeProject
	if rule.Scope.IsGlobal() {
		scope = scopeGlobal
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule_id": rule.ID, "scope": scope, "yaml": string(y),
		"summary": map[string]string{"action": string(rule.Action), "payload": string(rule.Match.Payload), "anchor": ruleAnchor(rule)}})
}

// writeDecideErr 把 Decide 与 propose 的错误写成 HTTP 回应：坏 action、坏 scope 400；没有这条请求 404；
// 机器已吊销、已过期、已处理 409；记不住 422（文案以“不能记住：”开头）；其余记日志回 500（what 说明在做什么）。
func (s *Server) writeDecideErr(w http.ResponseWriter, what string, err error) {
	switch {
	case errors.Is(err, errBadAction), errors.Is(err, errBadScope):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "没有这条请求")
	case errors.Is(err, errRevoked):
		writeErr(w, http.StatusConflict, "机器已吊销，请求已取消")
	case errors.Is(err, errExpired):
		writeErr(w, http.StatusConflict, "请求已过期")
	case errors.Is(err, store.ErrNotPending):
		writeErr(w, http.StatusConflict, "请求已经处理过")
	case errors.Is(err, errLearn):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
	default:
		s.internalErr(w, what, err)
	}
}

// decodeRule 把规则行的 YAML 严格解码成 policy.Rule（与存储层读回时一样拒绝未知字段）。
func decodeRule(row store.RuleRow) (policy.Rule, error) {
	var rule policy.Rule
	dec := yaml.NewDecoder(strings.NewReader(row.YAML))
	dec.KnownFields(true)
	err := dec.Decode(&rule)
	return rule, err
}

// handleRules 按入库顺序列规则，每条带 elevated（能否自动放行 outward、destructive，网页标红）。
func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListRules()
	if err != nil {
		s.internalErr(w, "列规则", err)
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		rule, err := decodeRule(x)
		if err != nil {
			s.internalErr(w, "解码规则 "+x.ID, err)
			return
		}
		out = append(out, map[string]any{"id": x.ID, "source": x.Source, "enabled": x.Enabled, "elevated": elevated(rule), "hit_count": x.HitCount,
			"last_hit_at": x.LastHitAt, "created_at": x.CreatedAt, "created_from": x.CreatedFrom, "yaml": x.YAML})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRulePatch 启停一条规则：{"enabled":bool}。把 elevated 规则从停用改为启用时审计 rule_enabled_elevated，
// 其余审计 rule_enabled 或 rule_disabled；之后下发规则快照。
func (s *Server) handleRulePatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(w, r, patchBodyMax, &body); err != nil || body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "需要 enabled")
		return
	}
	id, enabled := r.PathValue("id"), *body.Enabled
	action, what, err := s.toggleRule(id, enabled)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "没有这条规则")
		return
	case err != nil:
		s.internalErr(w, what, err)
		return
	}
	s.audit("", "user", "", action, map[string]string{"rule_id": id})
	s.pushRules()
	w.WriteHeader(http.StatusNoContent)
}

// toggleRule 在 rulesMu 下读规则现状、改启停，返回该记的审计动作。出错时 what 说明是哪一步。
func (s *Server) toggleRule(id string, enabled bool) (action, what string, err error) {
	s.rulesMu.Lock()
	defer s.rulesMu.Unlock()
	rows, err := s.store.ListRules()
	if err != nil {
		return "", "列规则", err
	}
	var row *store.RuleRow
	for i := range rows {
		if rows[i].ID == id {
			row = &rows[i]
		}
	}
	if row == nil {
		return "", "", store.ErrNotFound
	}
	rule, err := decodeRule(*row)
	if err != nil {
		return "", "解码规则 " + id, err
	}
	if _, err := s.store.SetRuleEnabled(id, enabled); err != nil {
		return "", "启停规则", err
	}
	switch {
	case enabled && !row.Enabled && elevated(rule):
		return "rule_enabled_elevated", "", nil
	case enabled:
		return "rule_enabled", "", nil
	default:
		return "rule_disabled", "", nil
	}
}

func (s *Server) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.DeleteRule(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "没有这条规则")
			return
		}
		s.internalErr(w, "删规则", err)
		return
	}
	s.audit("", "user", "", "rule_deleted", map[string]string{"rule_id": id})
	s.pushRules()
	w.WriteHeader(http.StatusNoContent)
}

// handleRulesImport 用请求体里的 YAML（Content-Type 为 application/yaml 或 text/yaml，至多 1 MiB）整套替换规则，回 {version}。
// 只收 YAML 类型：跨站的普通表单发不出这种类型（415）。解析或校验不过 400（learned 来源放行 outward、destructive 也在此被拒）；
// 编成下发帧超过单帧上限 413。成功后审计 rules_imported，detail 带启用着的 elevated 规则 id 列表（没有时为空列表），再下发快照。
func (s *Server) handleRulesImport(w http.ResponseWriter, r *http.Request) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mt != "application/yaml" && mt != "text/yaml") {
		writeErr(w, http.StatusUnsupportedMediaType, "请求体须是 YAML（Content-Type: application/yaml）")
		return
	}
	data, err := readBody(w, r, rulesImportMax)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeErr(w, http.StatusRequestEntityTooLarge, "规则文件至多 1 MiB")
			return
		}
		writeErr(w, http.StatusBadRequest, "读请求体失败")
		return
	}
	rs, err := policy.ParseRuleSet(data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !rulesFitFrame(rs) {
		writeErr(w, http.StatusRequestEntityTooLarge, "规则集太大，超过下发给守护进程的单帧上限")
		return
	}
	s.rulesMu.Lock()
	version, err := s.store.ReplaceRules(rs)
	s.rulesMu.Unlock()
	if err != nil {
		s.internalErr(w, "导入规则", err)
		return
	}
	ids := []string{}
	for _, rule := range rs.Rules {
		if rule.IsEnabled() && elevated(rule) {
			ids = append(ids, rule.ID)
		}
	}
	s.audit("", "user", "", "rules_imported", map[string]any{"version": version, "count": len(rs.Rules), "elevated_rule_ids": ids})
	s.pushRules()
	writeJSON(w, http.StatusOK, map[string]int{"version": version})
}

func (s *Server) handleRulesExport(w http.ResponseWriter, r *http.Request) {
	rs, _, err := s.store.RuleSet()
	if err != nil {
		s.internalErr(w, "读规则", err)
		return
	}
	b, err := rs.Marshal()
	if err != nil {
		s.internalErr(w, "编码规则", err)
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("Content-Disposition", `attachment; filename="railse-rules.yaml"`)
	_, _ = w.Write(b) // 写失败只会是客户端已断开，无从补救
}

func (s *Server) handleMachines(w http.ResponseWriter, r *http.Request) {
	machines, err := s.store.ListMachines()
	if err != nil {
		s.internalErr(w, "列机器", err)
		return
	}
	out := make([]map[string]any, 0, len(machines))
	for _, m := range machines {
		out = append(out, map[string]any{"id": m.ID, "name": m.Name, "online": s.agents.connected(m.ID), "last_seen": m.LastSeen,
			"agent_version": m.AgentVersion, "enrolled_at": m.EnrolledAt, "revoked": m.Revoked})
	}
	writeJSON(w, http.StatusOK, out)
}

// decidedByRevoked 是吊销机器时取消其待决请求记的 decided_by。
const decidedByRevoked = "machine_revoked"

// handleMachineRevoke 吊销机器（密钥立即失效）、断开它的在线连接，再取消它仍 pending 的请求。
// 握手进行中的连接由 handleAgentWS 登记后再核对一次吊销。
func (s *Server) handleMachineRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetMachine(id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "没有这台机器")
			return
		}
		s.internalErr(w, "读机器", err)
		return
	}
	if err := s.store.RevokeMachine(id); err != nil {
		s.internalErr(w, "吊销机器", err)
		return
	}
	s.agents.disconnect(id)
	s.audit(id, "user", "", "machine_revoked", nil)
	s.cancelRevokedPending(id)
	w.WriteHeader(http.StatusNoContent)
}

// cancelRevokedPending 把吊销机器仍 pending 的请求改为 cancelled、decided_by machine_revoked，逐条审计并广播 request_updated：
// 它的守护进程已断开且连不回来，这些请求不会再有人等，不必挂到过期。
func (s *Server) cancelRevokedPending(machineID string) {
	pend, err := s.store.ListPendingForMachine(machineID)
	if err != nil {
		s.log.Printf("列吊销机器 %s 的待决请求: %v", machineID, err)
		return
	}
	for _, r := range pend {
		err := s.store.SetMachineRequestState(machineID, r.ID, "cancelled", decidedByRevoked, "机器已吊销", "")
		if errors.Is(err, store.ErrNotPending) {
			continue
		}
		if err != nil {
			s.log.Printf("取消吊销机器 %s 的请求 %q: %v", machineID, r.ID, err)
			continue
		}
		s.settled(machineID, "user", r.ID, "cancelled", decidedByRevoked)
	}
}

// handleClientWS 把 clientHub 的广播（{"type","data"} 文本帧）推给网页。cookie 认证与 Origin 检查在握手之前
// （requireClient、sameOrigin；Accept 不传 OriginPatterns，库默认也只接受与 r.Host 同源的 Origin）。
// 先订阅再 Accept：Dial 一返回就广播的帧也不会丢。
//
// 慢的订阅者（缓冲 64 帧满）会被静默丢帧，推送只是“有变化”的提示：网页每次（重新）连上、或怀疑漏了帧时，
// 必须全量重新拉取（/api/inbox、/api/status、/api/rules 等），不能只靠推送增量维护状态。
//
// 网页不该发数据帧（发了就断开）。每 PingInterval 复核一次登录会话（登出、换密码、过期后断开）并 ping，
// PongTimeout 内没有 pong 就断开。handler 返回前等这些都停下，返回之后不再碰存储；Run 关停时等 handler 返回（见 tracker）。
func (s *Server) handleClientWS(w http.ResponseWriter, r *http.Request) {
	if !s.trackWS(w) {
		return
	}
	defer s.untrackWS()
	ck, err := r.Cookie(cookieName)
	if err != nil { // requireClient 已查过，防御
		writeErr(w, http.StatusUnauthorized, "请先登录")
		return
	}
	ch, stop := s.clients.subscribe()
	defer stop()
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return // Accept 已回了 HTTP 错误
	}
	defer func() { _ = conn.CloseNow() }() // 已关过时 CloseNow 报错，无害
	ctx, cancel := context.WithCancel(conn.CloseRead(r.Context()))
	defer cancel()
	keepDone := make(chan struct{})
	go func() {
		defer close(keepDone)
		defer cancel()
		s.clientKeepalive(ctx, conn, ck.Value)
	}()
	defer func() {
		cancel()
		<-keepDone
	}()
	for {
		select {
		case b := <-ch:
			wctx, wcancel := context.WithTimeout(ctx, agentWriteTimeout)
			err := conn.Write(wctx, websocket.MessageText, b)
			wcancel()
			if err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// clientKeepalive 每 PingInterval 先复核登录会话（失效就以 PolicyViolation 关闭），再 ping 并等 pong 至多 PongTimeout；
// 任一失败或 ctx 结束时返回。
func (s *Server) clientKeepalive(ctx context.Context, conn *websocket.Conn, token string) {
	ticker := time.NewTicker(s.cfg.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if ctx.Err() != nil {
			return
		}
		ok, err := s.store.ValidateClientSession(token)
		s.clientRechecks.Add(1)
		if err != nil {
			s.log.Printf("复核网页连接的登录会话: %v", err)
			conn.Close(websocket.StatusInternalError, "hub 内部错误")
			return
		}
		if !ok {
			conn.Close(websocket.StatusPolicyViolation, "登录已失效")
			return
		}
		pctx, pcancel := context.WithTimeout(ctx, s.cfg.PongTimeout)
		err = conn.Ping(pctx)
		pcancel()
		if err != nil {
			return
		}
	}
}
