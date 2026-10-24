package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/policy"
)

// 本文件的测试用 t.Setenv，不得 t.Parallel。

// sampleWithCommand 在录制样本上改 cwd 与 tool_input.command。
func sampleWithCommand(t *testing.T, rel, cwd, command string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(sampleWithCwd(t, rel, cwd)), &doc); err != nil {
		t.Fatal(err)
	}
	doc["tool_input"] = map[string]any{"command": command}
	out, _ := json.Marshal(doc)
	return string(out)
}

// hookResult 是后台钩子的结果与拿到答复的时刻。
type hookResult struct {
	out  string
	code int
	at   time.Time
}

// runHookAsync 在后台跑钩子（不调 t.Fatal），结果送进返回的通道。
func runHookAsync(t *testing.T, stdin string, args ...string) <-chan hookResult {
	res := make(chan hookResult, 1)
	go func() {
		o, _, c := runHookWith(t, stdin, args...)
		res <- hookResult{o, c, time.Now()}
	}()
	return res
}

// onlyPending 等 hub 里恰好有一条 pending 并返回它。
func onlyPending(t *testing.T, h *testHub) store.Request {
	t.Helper()
	var pending []store.Request
	waitUntil(t, func() bool {
		pending, _ = h.st.ListRequests("pending", 10)
		return len(pending) == 1
	})
	return pending[0]
}

// phoneRequest 带登录 cookie 调 hub 的网页接口（不带 Origin，同非浏览器客户端），要求 200，响应体解进 out（非 nil 时）。
func phoneRequest(t *testing.T, h *testHub, ck *http.Cookie, method, path string, body any, out any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, h.http.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.AddCookie(ck)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v %s", method, path, err, b)
		}
	}
}

// decideOnPhone 走网页的决定入口 POST /api/requests/{id}/decide，返回发请求之前的时刻。
func decideOnPhone(t *testing.T, h *testHub, ck *http.Cookie, id, action, note string, remember bool) time.Time {
	t.Helper()
	at := time.Now()
	phoneRequest(t, h, ck, "POST", "/api/requests/"+id+"/decide", map[string]any{"action": action, "note": note, "remember": remember}, nil)
	return at
}

// hookAnswer 等钩子的答复，要求在决定发出后 1 秒内拿到（完成标准 4）。
func hookAnswer(t *testing.T, res <-chan hookResult, decidedAt time.Time) hookResult {
	t.Helper()
	select {
	case r := <-res:
		if d := r.at.Sub(decidedAt); d > time.Second {
			t.Fatalf("钩子在决定发出 %s 后才拿到答复，应在 1 秒内", d)
		}
		return r
	case <-time.After(10 * time.Second):
		t.Fatal("钩子没有拿到决定")
	}
	return hookResult{}
}

// settledRequest 等 hub 里的请求变成 state 且记下送达时间，返回它。
func settledRequest(t *testing.T, h *testHub, id, state string) store.Request {
	t.Helper()
	var r store.Request
	waitUntil(t, func() bool {
		r, _ = h.st.GetRequest(id)
		return r.State == state && r.DecidedBy == "user" && r.DeliveredAt != nil
	})
	return r
}

// mustAudit 要求本机审计里有一行同时含全部片段。
func mustAudit(t *testing.T, path, what string, parts ...string) {
	t.Helper()
	if !auditHas(t, path, parts...) {
		b, _ := os.ReadFile(path)
		t.Fatalf("本机审计应%s:\n%s", what, b)
	}
}

// rulesFromHub 等 hub 当前版本的规则在本机生效，再核对 rules.yaml 已被覆盖：文件头注明来源，且含 ruleID。
// 生效的信号是 agent.log 里的“规则更新到版本 N，”：它在 Rules.Replace 换好引擎之后才写。不能拿 rules.yaml 的内容当信号：
// Replace 先写缓存文件再换引擎，文件已新而引擎仍旧的窗口里跑钩子，请求会按旧规则进 hub 等决定。
func rulesFromHub(t *testing.T, h *testHub, cfg agent.Config, ruleID string) {
	t.Helper()
	v, err := h.st.RulesVersion()
	if err != nil {
		t.Fatal(err)
	}
	applied := fmt.Sprintf("规则更新到版本 %d，", v)
	waitUntil(t, func() bool {
		b, _ := os.ReadFile(cfg.LogPath)
		return strings.Contains(string(b), applied)
	})
	b, _ := os.ReadFile(cfg.RulesPath)
	if !strings.HasPrefix(string(b), fmt.Sprintf("# 由 hub 下发，版本 %d。", v)) || !strings.Contains(string(b), "id: "+ruleID+"\n") {
		t.Fatalf("本机 rules.yaml 应被 hub 下发的版本 %d 覆盖并含 %s:\n%s", v, ruleID, b)
	}
}

// runHookTimed 跑钩子并等答复，10 秒内没有就失败：本地应立即定下的请求若误进 hub 等决定，测试快速失败而不是挂到钩子期限。
func runHookTimed(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	select {
	case r := <-runHookAsync(t, stdin, args...):
		return r.out, r.code
	case <-time.After(10 * time.Second):
		t.Fatal("钩子 10 秒内没有拿到答复")
	}
	return "", 0
}

// hub + 守护进程 + 钩子在一个进程里跑完整条链路：引擎定不了 → hub 登记 → 手机上决定 → 钩子拿到答复。
func TestEndToEndHubApproval(t *testing.T) {
	const sentinel = "railse-sentinel-7b1c44e0"
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", sentinel)
	h := startTestHub(t)
	ck := hubLogin(t, h) // 密码 hunter22
	dir := tempDir(t)
	cfgDir := filepath.Join(dir, "h")
	m := enrollTestMachine(t, h, cfgDir)
	cfg := agent.ConfigIn(cfgDir)
	cfg.HubGrace = 5 * time.Second
	serveAgent(t, cfg)
	waitUntil(t, func() bool { ms, _ := h.st.ListMachines(); return len(ms) == 1 && ms[0].LastSeen != nil })
	// hub 记下 hello 时守护进程可能还没处理 welcome：等 status 显示已连上
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		var out, errOut bytes.Buffer
		run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut)
		if strings.Contains(out.String(), "\nhub: connected "+h.http.URL+"\n") {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("status 应显示已连上 hub: %q %q", out.String(), errOut.String())
		}
	}
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	gitPush := sampleWithCwd(t, "claude/2.1.280/PermissionRequest.json", repo)

	// 1. git push 是 outward，引擎定不了 → hub 收件箱 → 手机批准（带备注）→ 钩子 1 秒内得到 allow；
	//    钩子与模型看不到备注（固定文案），备注只进审计（用户决定 C）
	const allowNote = "备注-批准-a1f3"
	res := runHookAsync(t, gitPush, "claude", "--socket", cfg.SocketPath)
	p := onlyPending(t, h)
	if p.MachineID != m.MachineID || p.Kind != "permission" || p.ToolName != "Bash" || p.Risk != "outward" ||
		p.Payload.Command != "git push origin main" || p.ProjectRoot == "" || p.ExpiresAt == nil {
		t.Fatalf("hub 里的请求: %+v", p)
	}
	var inbox struct {
		Pending []map[string]any `json:"pending"`
	}
	phoneRequest(t, h, ck, "GET", "/api/inbox", nil, &inbox)
	if len(inbox.Pending) != 1 || inbox.Pending[0]["id"] != p.ID || inbox.Pending[0]["machine_name"] != "mac" {
		t.Fatalf("收件箱: %+v", inbox)
	}
	r := hookAnswer(t, res, decideOnPhone(t, h, ck, p.ID, "allow", allowNote, false))
	if r.code != 0 || !strings.Contains(r.out, `"behavior":"allow"`) || strings.Contains(r.out, allowNote) {
		t.Fatalf("钩子应得到 allow 且不含备注: %d %q", r.code, r.out)
	}
	if got := settledRequest(t, h, p.ID, "approved"); got.DecisionNote != allowNote || got.RuleID != "" {
		t.Fatalf("hub 应记 approved、备注与送达: %+v", got)
	}
	mustAudit(t, cfg.AuditPath, "记 approved/user，理由是固定文案、备注单列",
		`"request_id":"`+p.ID+`"`, `"state":"approved"`, `"decided_by":"user"`, `"reason":"已在手机上批准"`, `"note":"`+allowNote+`"`)

	// 2. Codex 的请求同样进 hub；手机拒绝（不记住）→ 钩子得到固定文案的 deny，不含备注；hub 记 denied 与送达
	const denyNote = "备注-拒绝-c2e8"
	res = runHookAsync(t, sampleWithCommand(t, "codex/0.145.0/PermissionRequest.json", repo, "curl https://example.com"), "codex", "--socket", cfg.SocketPath)
	p = onlyPending(t, h)
	if p.ToolName != "Bash" || p.Risk != "network" || !strings.Contains(p.Payload.Command, "curl") {
		t.Fatalf("hub 里的 Codex 请求: %+v", p)
	}
	r = hookAnswer(t, res, decideOnPhone(t, h, ck, p.ID, "deny", denyNote, false))
	if r.code != 0 || !strings.Contains(r.out, `"behavior":"deny"`) || !strings.Contains(r.out, "Railse：已在手机上拒绝") || strings.Contains(r.out, denyNote) {
		t.Fatalf("Codex 钩子应得到固定文案的 deny、不含备注: %d %q", r.code, r.out)
	}
	settledRequest(t, h, p.ID, "denied")
	mustAudit(t, cfg.AuditPath, "记 denied/user，备注单列",
		`"request_id":"`+p.ID+`"`, `"state":"denied"`, `"reason":"已在手机上拒绝"`, `"note":"`+denyNote+`"`)

	// 3. 自动放行的记录异步上报到 hub，hub 的 requests 里有历史行
	o, c := runHookTimed(t, sampleWithCwd(t, "claude/2.1.280/PermissionRequest.interactive.json", repo), "claude", "--socket", cfg.SocketPath)
	if c != 0 || !strings.Contains(o, `"behavior":"allow"`) {
		t.Fatalf("touch 应本地放行: %d %q", c, o)
	}
	waitUntil(t, func() bool {
		rs, _ := h.st.ListRequests("auto_allowed", 10)
		return len(rs) == 1 && rs[0].RuleID == "builtin-write-in-project"
	})

	// 4. Gemini 的请求不进 hub：直接交回原生确认框，hub 没有新的 pending，只有审计上报补的历史行
	o, c = runHookTimed(t, sampleWithCommand(t, "gemini/0.65.0/BeforeTool.json", repo, "git push origin main"), "gemini", "--socket", cfg.SocketPath)
	if c != 0 || o != "" {
		t.Fatalf("Gemini 应不作决定: %d %q", c, o)
	}
	mustAudit(t, cfg.AuditPath, "记 Gemini 直通", `"tool":"gemini"`, `"state":"passthrough"`, `"decided_by":"gemini"`)
	waitUntil(t, func() bool {
		rs, _ := h.st.ListRequests("passthrough", 10)
		return len(rs) == 1 && rs[0].DecidedBy == "gemini"
	})
	if n, _ := h.st.CountPending(); n != 0 {
		t.Fatalf("Gemini 的请求不应在 hub 等待，pending=%d", n)
	}

	// 5. 批准并记住：hub 学一条 learned 放行并下发，本机 rules.yaml 被覆盖、文件头注明来源；同一命令再来时本机直接放行
	npmInstall := sampleWithCommand(t, "claude/2.1.280/PermissionRequest.json", repo, "npm install left-pad")
	res = runHookAsync(t, npmInstall, "claude", "--socket", cfg.SocketPath)
	p = onlyPending(t, h)
	r = hookAnswer(t, res, decideOnPhone(t, h, ck, p.ID, "allow", "", true))
	if r.code != 0 || !strings.Contains(r.out, `"behavior":"allow"`) {
		t.Fatalf("钩子应得到 allow: %d %q", r.code, r.out)
	}
	learnedID := settledRequest(t, h, p.ID, "approved").RuleID
	if !strings.HasPrefix(learnedID, "learned-") {
		t.Fatalf("批准并记住应学到 learned 规则: %q", learnedID)
	}
	rulesFromHub(t, h, cfg, learnedID)
	o, c = runHookTimed(t, npmInstall, "claude", "--socket", cfg.SocketPath)
	if c != 0 || !strings.Contains(o, `"behavior":"allow"`) {
		t.Fatalf("学到规则后同一命令应本地放行: %d %q", c, o)
	}
	mustAudit(t, cfg.AuditPath, "记 auto_allowed/学到的规则", `"state":"auto_allowed"`, `"rule_id":"`+learnedID+`"`)

	// 6. 拒绝并记住：hub 直接构造 user 来源、锚定整条命令（command_regex ^…$）的 deny 并下发；同一命令再来时本机直接拒绝
	res = runHookAsync(t, gitPush, "claude", "--socket", cfg.SocketPath)
	p = onlyPending(t, h)
	r = hookAnswer(t, res, decideOnPhone(t, h, ck, p.ID, "deny", denyNote, true))
	if r.code != 0 || !strings.Contains(r.out, `"behavior":"deny"`) || !strings.Contains(r.out, "Railse：已在手机上拒绝") || strings.Contains(r.out, denyNote) {
		t.Fatalf("钩子应得到固定文案的 deny、不含备注: %d %q", r.code, r.out)
	}
	denyID := settledRequest(t, h, p.ID, "denied").RuleID
	rs, _, err := h.st.RuleSet()
	if err != nil {
		t.Fatal(err)
	}
	var denyRule *policy.Rule
	for i := range rs.Rules {
		if rs.Rules[i].ID == denyID {
			denyRule = &rs.Rules[i]
		}
	}
	if denyID == "" || denyRule == nil || denyRule.Source != policy.SourceUser || denyRule.Action != policy.ActionDeny ||
		denyRule.Match.Payload != policy.PayloadShell || denyRule.Match.CommandRegex != "^git push origin main$" || denyRule.Match.Risk != nil {
		t.Fatalf("拒绝并记住应是 user 来源、锚定整条命令的 deny: %q %+v", denyID, denyRule)
	}
	rulesFromHub(t, h, cfg, denyID)
	o, c = runHookTimed(t, gitPush, "claude", "--socket", cfg.SocketPath)
	if c != 0 || !strings.Contains(o, `"behavior":"deny"`) {
		t.Fatalf("记住的拒绝应本地生效: %d %q", c, o)
	}
	mustAudit(t, cfg.AuditPath, "记 auto_denied/记住的拒绝", `"state":"auto_denied"`, `"rule_id":"`+denyID+`"`)
	if n, _ := h.st.CountPending(); n != 0 {
		t.Fatalf("本地已定的请求不应进 hub 等待，pending=%d", n)
	}

	// 7. hub 整套替换规则并下发：用户 deny 立刻在本机生效
	repl, _ := policy.ParseRuleSet([]byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"))
	if _, err := h.st.ReplaceRules(repl); err != nil {
		t.Fatal(err)
	}
	h.srv.PushRulesForTest()
	rulesFromHub(t, h, cfg, "deny-rm")
	o, c = runHookTimed(t, sampleWithCommand(t, "claude/2.1.280/PermissionRequest.json", repo, "rm x"), "claude", "--socket", cfg.SocketPath)
	if c != 0 || !strings.Contains(o, `"behavior":"deny"`) {
		t.Fatalf("hub 下发的 deny 应生效: %d %q", c, o)
	}
	mustAudit(t, cfg.AuditPath, "记 auto_denied/deny-rm", `"state":"auto_denied"`, `"rule_id":"deny-rm"`)

	// 8. 钩子进程的环境变量不进本机审计与日志，也不进 hub 的审计、请求、规则与日志
	noSentinel := func(where, text string) {
		t.Helper()
		if strings.Contains(text, sentinel) || strings.Contains(text, "CLAUDE_CODE_MESSAGING") {
			t.Fatalf("%s里不能出现钩子进程的环境变量", where)
		}
	}
	waitUntil(t, func() bool { // 等最后一条本机审计也上报到 hub
		es, _ := h.st.ListAudit(1000, "")
		for _, e := range es {
			if e.Actor == "agent" && e.Action == "auto_denied" && strings.Contains(e.Detail, `"rule_id":"deny-rm"`) {
				return true
			}
		}
		return false
	})
	b, _ := os.ReadFile(cfg.AuditPath)
	noSentinel("本机审计", string(b))
	b, _ = os.ReadFile(cfg.LogPath)
	noSentinel("守护进程日志", string(b))
	events, _ := h.st.ListAudit(1000, "")
	for _, e := range events {
		eb, _ := json.Marshal(e)
		noSentinel("hub 审计", string(eb))
	}
	all, _ := h.st.ListRequests("", 100)
	if len(all) < 8 {
		t.Fatalf("hub 应有全部请求的记录: %d 条", len(all))
	}
	for _, r := range all {
		rb, _ := json.Marshal(r) // 载荷、标题、引擎理由、备注都在里面
		noSentinel("hub 请求", string(rb)+r.Payload.Command+r.Title+r.EngineReason)
	}
	rows, _ := h.st.ListRules()
	for _, row := range rows {
		noSentinel("hub 规则", row.YAML)
	}
	noSentinel("hub 日志", h.logs.String())
}

// hub 不在：守护进程等满 HubGrace 后交回终端，本机审计记 expired / hub_unreachable（用户决定 A）。
func TestEndToEndHubUnreachable(t *testing.T) {
	dir := tempDir(t)
	cfg := agent.ConfigIn(filepath.Join(dir, "h"))
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveMachine(cfg.MachinePath, agent.MachineConfig{HubURL: "http://127.0.0.1:1", MachineID: "m", Secret: "s", Name: "x"}); err != nil {
		t.Fatal(err)
	}
	cfg.HubGrace = 500 * time.Millisecond
	serveAgent(t, cfg)
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	start := time.Now()
	o, c := runHookTimed(t, sampleWithCwd(t, "claude/2.1.280/PermissionRequest.json", repo), "claude", "--socket", cfg.SocketPath)
	if c != 0 || o != "" {
		t.Fatalf("连不上 hub 应交回终端: %d %q", c, o)
	}
	if el := time.Since(start); el < cfg.HubGrace || el > 5*time.Second {
		t.Fatalf("应等满 HubGrace 后很快交回: 用时 %s", el)
	}
	mustAudit(t, cfg.AuditPath, "记 expired/hub_unreachable", `"action":"passthrough"`, `"state":"expired"`, `"decided_by":"hub_unreachable"`)
}

// 终审 I2：项目路径经过符号链接（macOS 的 /var → /private/var 同理）时，守护进程上报的载荷 cwd 与项目根同一口径，
// hub 的“批准并记住”对项目内的写操作能学成规则，不会误判成项目外（write_outside）而回 422。
func TestEndToEndRememberThroughSymlink(t *testing.T) {
	h := startTestHub(t)
	ck := hubLogin(t, h)
	dir := tempDir(t)
	cfgDir := filepath.Join(dir, "h")
	enrollTestMachine(t, h, cfgDir)
	cfg := agent.ConfigIn(cfgDir)
	cfg.HubGrace = 5 * time.Second
	serveAgent(t, cfg)
	waitUntil(t, func() bool { ms, _ := h.st.ListMachines(); return len(ms) == 1 && ms[0].LastSeen != nil })
	// 停用内置的项目内可写放行，项目内写入才会进 hub 等决定
	if _, err := h.st.SetRuleEnabled("builtin-write-in-project", false); err != nil {
		t.Fatal(err)
	}
	h.srv.PushRulesForTest()
	rulesFromHub(t, h, cfg, "builtin-write-in-project")
	real := filepath.Join(dir, "real")
	os.MkdirAll(filepath.Join(real, "proj", ".git"), 0o755)
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	realProj, err := filepath.EvalSymlinks(filepath.Join(real, "proj"))
	if err != nil {
		t.Fatal(err)
	}
	res := runHookAsync(t, sampleWithCommand(t, "claude/2.1.280/PermissionRequest.json", filepath.Join(dir, "link", "proj"), "mkdir build"), "claude", "--socket", cfg.SocketPath)
	p := onlyPending(t, h)
	if p.ProjectRoot != realProj || p.Payload.Cwd != realProj || p.Risk != "write_in_project" {
		t.Fatalf("hub 里的请求：项目根 %q、载荷 cwd %q 都应是 %q，风险 %q", p.ProjectRoot, p.Payload.Cwd, realProj, p.Risk)
	}
	r := hookAnswer(t, res, decideOnPhone(t, h, ck, p.ID, "allow", "", true)) // 记不住时 decide 回 422，这里失败
	if r.code != 0 || !strings.Contains(r.out, `"behavior":"allow"`) {
		t.Fatalf("钩子应得到 allow: %d %q", r.code, r.out)
	}
	if id := settledRequest(t, h, p.ID, "approved").RuleID; !strings.HasPrefix(id, "learned-") {
		t.Fatalf("批准并记住应学到 learned 规则: %q", id)
	}
}
