package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"gopkg.in/yaml.v3"

	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/wire"
	"github.com/railse/railse-opc/internal/policy"
)

// doReq 发一个请求；响应体读进内存后关掉原连接，调用方可再读 resp.Body。
func doReq(t *testing.T, method, url string, body []byte, ct string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return doReqHdr(t, method, url, body, ct, cookie, nil)
}

func doReqHdr(t *testing.T, method, url string, body []byte, ct string, cookie *http.Cookie, hdr http.Header) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for k, vs := range hdr {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return do(t, req)
}

// loggedIn 设密码并登录，返回会话 cookie。
func loggedIn(t *testing.T, h *testHub) *http.Cookie {
	t.Helper()
	if err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	return login(t, h, "hunter22")
}

// decideBody 编出 decide 接口的请求体。
func decideBody(t *testing.T, action string, remember bool, note string) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{"action": action, "remember": remember, "note": note})
}

// ruleRows 列出规则行，并把每行的 YAML 解码成 policy.Rule。
func ruleRows(t *testing.T, st *store.Store) ([]store.RuleRow, []policy.Rule) {
	t.Helper()
	rows, err := st.ListRules()
	if err != nil {
		t.Fatal(err)
	}
	rules := make([]policy.Rule, len(rows))
	for i, row := range rows {
		if err := yaml.Unmarshal([]byte(row.YAML), &rules[i]); err != nil {
			t.Fatalf("规则 %s 的 YAML: %v", row.ID, err)
		}
	}
	return rows, rules
}

// lastAudit 返回最近一条动作为 action 的审计（不限请求）。
func lastAudit(t *testing.T, st *store.Store, action string) (store.AuditEvent, bool) {
	t.Helper()
	events, err := st.ListAudit(1000, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events { // 倒序
		if e.Action == action {
			return e, true
		}
	}
	return store.AuditEvent{}, false
}

// evalWith 用库里的整套规则建引擎评估载荷。
func evalWith(t *testing.T, st *store.Store, p policy.Payload, ctx policy.Context) policy.Decision {
	t.Helper()
	rs, _, err := st.RuleSet()
	if err != nil {
		t.Fatal(err)
	}
	eng, err := policy.NewEngine(rs)
	if err != nil {
		t.Fatal(err)
	}
	return eng.Evaluate(p, ctx)
}

func TestInboxDecideRemember(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, HomeDir: "/home/u"})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "npm run build"))
	waitFor(t, "r1 登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })

	var inbox []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
	if len(inbox) != 1 || inbox[0]["id"] != "r1" || inbox[0]["machine_name"] != "mac" || inbox[0]["project"] != "p" {
		t.Fatalf("inbox = %+v", inbox)
	}
	if _, ok := inbox[0]["callback"]; ok {
		t.Fatalf("不应输出 callback: %+v", inbox[0])
	}
	resp := doReq(t, "POST", h.http.URL+"/api/requests/r1/decide", decideBody(t, "allow", true, "ok"), "application/json", ck)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decide = %d", resp.StatusCode)
	}
	var decided map[string]any
	decodeBody(t, resp, &decided)
	if decided["state"] != "approved" || decided["rule_id"] == "" || decided["machine_name"] != "mac" || decided["project"] != "p" {
		t.Fatalf("decided = %+v", decided)
	}
	// 守护进程收到规则快照与决定，两帧都要到
	seen := map[string]bool{}
	for range 2 {
		kind, _ := c.recv(5 * time.Second)
		seen[kind] = true
	}
	if !seen[wire.TDecision] || !seen[wire.TRules] {
		t.Fatalf("应收到 decision 与 rules: %+v", seen)
	}
	var rules []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/rules", nil, "", ck), &rules)
	if len(rules) != 3 || rules[2]["source"] != "learned" || rules[2]["id"] != decided["rule_id"] || rules[2]["elevated"] != false {
		t.Fatalf("rules = %+v", rules)
	}
	if acts := auditActions(t, h.st, "r1"); countOf(acts, "rule_learned") != 1 || countOf(acts, "allow") != 1 {
		t.Fatalf("审计应有学规则与决定: %v", acts)
	}
	resp = doReq(t, "POST", h.http.URL+"/api/requests/r1/decide", decideBody(t, "allow", true, "ok"), "application/json", ck)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("二次决定应 409, 得到 %d", resp.StatusCode)
	}
}

// 拒绝并记住：hub 直接构造 user 来源的 deny（不经 ProposeRule），不固定风险，shell 用锚定整条命令的正则。
func TestRememberDenyBecomesUserRule(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, HomeDir: "/home/u"})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "npm run deploy"))
	waitFor(t, "r1 登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	if resp := doReq(t, "POST", h.http.URL+"/api/requests/r1/decide", decideBody(t, "deny", true, ""), "application/json", ck); resp.StatusCode != http.StatusOK {
		t.Fatalf("decide = %d", resp.StatusCode)
	}
	rows, rules := ruleRows(t, h.st)
	last, rule := rows[len(rows)-1], rules[len(rules)-1]
	want := "^" + regexp.QuoteMeta("npm run deploy") + "$"
	if last.Source != "user" || rule.Action != policy.ActionDeny || rule.Match.CommandRegex != want || rule.Match.Payload != policy.PayloadShell ||
		rule.Match.Risk != nil || rule.Match.CommandPrefix != "" || rule.Scope.Project != "/p" || rule.CreatedFrom != "r1" {
		t.Fatalf("记住的拒绝应是 user 来源、锚定整条命令的 deny: %+v %+v", last, rule)
	}
	ctx := policy.Context{ProjectRoot: "/p", HomeDir: "/home/u", MachineID: id}
	deploy := policy.Payload{Type: policy.PayloadShell, Command: "npm run deploy", Cwd: "/p"}
	if d := evalWith(t, h.st, deploy, ctx); d.Action != policy.ActionDeny || d.RuleID != last.ID {
		t.Fatalf("同一命令应被拒绝: %+v", d)
	}
	build := policy.Payload{Type: policy.PayloadShell, Command: "npm run build", Cwd: "/p"}
	if d := evalWith(t, h.st, build, ctx); d.Action == policy.ActionDeny {
		t.Fatalf("拒绝不应连带别的命令: %+v", d)
	}
	compound := policy.Payload{Type: policy.PayloadShell, Command: "cd /p && npm run deploy", Cwd: "/p"}
	if d := evalWith(t, h.st, compound, ctx); d.Action != policy.ActionDeny {
		t.Fatalf("复合命令里含这条命令也应被拒绝: %+v", d)
	}
}

// 对外命令不能“批准并记住”，但能“拒绝并记住”：记住的拒绝压过内置放行，复合命令里出现也拒绝。
func TestRememberDenyOutward(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	p := policy.Payload{Type: policy.PayloadShell, Command: "git push origin main", Cwd: "/p"}
	insertReq(t, h.st, store.Request{ID: "r1", MachineID: m.ID, State: "pending", Payload: p, ProjectRoot: "/p"})
	if resp := doReq(t, "POST", h.http.URL+"/api/requests/r1/decide", decideBody(t, "deny", true, ""), "application/json", ck); resp.StatusCode != http.StatusOK {
		t.Fatalf("对外命令应能拒绝并记住, 得到 %d", resp.StatusCode)
	}
	_, rules := ruleRows(t, h.st)
	if len(rules) != 3 || rules[2].Source != policy.SourceUser || rules[2].Action != policy.ActionDeny {
		t.Fatalf("rules = %+v", rules)
	}
	ctx := policy.Context{ProjectRoot: "/p", HomeDir: "/home/u", MachineID: m.ID}
	sneaky := policy.Payload{Type: policy.PayloadShell, Command: "npm test && git push origin main", Cwd: "/p"}
	if d := evalWith(t, h.st, sneaky, ctx); d.Action != policy.ActionDeny || d.RuleID != rules[2].ID {
		t.Fatalf("复合命令里出现也应被拒绝: %+v", d)
	}
}

func TestRememberRefusedForOutward(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, HomeDir: "/home/u"})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "git push origin main"))
	waitFor(t, "r1 登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	resp := doReq(t, "POST", h.http.URL+"/api/requests/r1/decide", decideBody(t, "allow", true, ""), "application/json", ck)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("对外命令不能记住, 应 422, 得到 %d", resp.StatusCode)
	}
	if r, _ := h.st.GetRequest("r1"); r.State != "pending" {
		t.Fatal("记不住时不应改变请求状态")
	}
}

// learnRule 永远不产生能放行 outward、destructive、unknown、write_outside 的规则：批准并记住一律 422，不生成规则，请求保持 pending。
func TestRememberAllowRefusedForRiskyClasses(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	for i, cmd := range []string{"git push origin main", "rm -rf build", "frobnicate --now", "touch /etc/x"} {
		id := fmt.Sprintf("k%d", i)
		p := policy.Payload{Type: policy.PayloadShell, Command: cmd, Cwd: "/p"}
		insertReq(t, h.st, store.Request{ID: id, MachineID: m.ID, State: "pending", Payload: p, ProjectRoot: "/p"})
		resp := doReq(t, "POST", h.http.URL+"/api/requests/"+id+"/decide", decideBody(t, "allow", true, ""), "application/json", ck)
		var body map[string]string
		decodeBody(t, resp, &body)
		if resp.StatusCode != http.StatusUnprocessableEntity || body["error"] == "" {
			t.Fatalf("%q 批准并记住应 422, 得到 %d %+v", cmd, resp.StatusCode, body)
		}
		if r := getReq(t, h.st, id); r.State != "pending" || r.RuleID != "" {
			t.Fatalf("%q 记不住时请求应保持 pending: %+v", cmd, r)
		}
	}
	if rows, _ := ruleRows(t, h.st); len(rows) != 2 {
		t.Fatalf("不应生成规则: %+v", rows)
	}
}

// 载荷含 <redacted> 时，批准或拒绝都不能记住（422），不生成规则。
func TestRememberRefusedForRedacted(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	payloads := []policy.Payload{
		{Type: policy.PayloadShell, Command: "npm login --password <redacted>", Cwd: "/p"},
		{Type: policy.PayloadMCP, Server: "db", Tool: "query", Args: json.RawMessage(`{"password":"<redacted>"}`)},
		{Type: policy.PayloadNetwork, URL: "https://x.example.com/?api_key=<redacted>"},
	}
	for i, p := range payloads {
		for _, action := range []string{"allow", "deny"} {
			id := fmt.Sprintf("s%d%s", i, action)
			insertReq(t, h.st, store.Request{ID: id, MachineID: m.ID, State: "pending", Payload: p, ProjectRoot: "/p"})
			resp := doReq(t, "POST", h.http.URL+"/api/requests/"+id+"/decide", decideBody(t, action, true, ""), "application/json", ck)
			var body map[string]string
			decodeBody(t, resp, &body)
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body["error"], "凭据") {
				t.Fatalf("%+v %s: 含凭据应 422, 得到 %d %+v", p, action, resp.StatusCode, body)
			}
		}
	}
	if rows, _ := ruleRows(t, h.st); len(rows) != 2 {
		t.Fatalf("不应生成规则: %+v", rows)
	}
}

// 拒绝并记住按载荷类型构造：file_write 精确路径（通配元字符转义）、mcp 服务加工具、network 主机名；
// 没有项目根时 scope 为 global；构造出的规则一定能拒绝原请求，匹配不上（复合命令等）或缺锚点时回 errLearn。
func TestRememberDenyShapes(t *testing.T) {
	h := newTestHub(t)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	ok := []struct {
		name  string
		root  string
		p     policy.Payload
		check func(r policy.Rule) bool
	}{
		{"相对路径", "/p", policy.Payload{Type: policy.PayloadFileWrite, Path: "src/a[1].go", Cwd: "/p"},
			func(r policy.Rule) bool {
				return r.Match.PathGlob == `/p/src/a\[1\].go` && r.Match.Payload == policy.PayloadFileWrite
			}},
		{"家目录", "/p", policy.Payload{Type: policy.PayloadFileWrite, Path: "~/.ssh/config"},
			func(r policy.Rule) bool { return r.Match.PathGlob == "/home/u/.ssh/config" }},
		{"mcp", "/p", policy.Payload{Type: policy.PayloadMCP, Server: "github", Tool: "create_issue"},
			func(r policy.Rule) bool { return r.Match.MCPServer == "github" && r.Match.MCPTool == "create_issue" }},
		{"network", "/p", policy.Payload{Type: policy.PayloadNetwork, URL: "https://API.Example.com/x", Method: "POST"},
			func(r policy.Rule) bool { return r.Match.Domain == "api.example.com" }},
		{"无项目根", "", policy.Payload{Type: policy.PayloadShell, Command: "  make deploy  "},
			func(r policy.Rule) bool { return r.Scope.IsGlobal() && r.Match.CommandRegex == "^make deploy$" }},
		{"项目根含通配", "/w/a*b", policy.Payload{Type: policy.PayloadShell, Command: "make deploy", Cwd: "/w/a*b"},
			func(r policy.Rule) bool { return r.Scope.Project == `/w/a\*b` }},
	}
	for i, tc := range ok {
		id := fmt.Sprintf("ok%d", i)
		insertReq(t, h.st, store.Request{ID: id, MachineID: m.ID, State: "pending", Payload: tc.p, ProjectRoot: tc.root})
		got, err := h.srv.Decide(DecideInput{RequestID: id, Action: "deny", Remember: true})
		if err != nil || got.RuleID == "" {
			t.Fatalf("%s: Decide = %+v, %v", tc.name, got, err)
		}
		rows, rules := ruleRows(t, h.st)
		r := rules[len(rules)-1]
		if rows[len(rows)-1].ID != got.RuleID || r.Source != policy.SourceUser || r.Action != policy.ActionDeny || r.Match.Risk != nil ||
			r.Match.Payload != tc.p.Type || !tc.check(r) {
			t.Fatalf("%s: 规则 %+v", tc.name, r)
		}
		d := evalWith(t, h.st, tc.p, policy.Context{ProjectRoot: tc.root, HomeDir: "/home/u", MachineID: m.ID})
		if d.Action != policy.ActionDeny || d.RuleID != got.RuleID {
			t.Fatalf("%s: 规则应拒绝原请求: %+v", tc.name, d)
		}
	}
	before, _ := ruleRows(t, h.st)
	bad := []struct {
		name string
		root string
		p    policy.Payload
	}{
		{"复合命令", "/p", policy.Payload{Type: policy.PayloadShell, Command: "cd /p && npm run deploy", Cwd: "/p"}},
		{"空命令", "/p", policy.Payload{Type: policy.PayloadShell, Command: "   "}},
		{"mcp 缺工具", "/p", policy.Payload{Type: policy.PayloadMCP, Server: "github"}},
		{"坏 URL", "/p", policy.Payload{Type: policy.PayloadNetwork, URL: "::"}},
		{"相对路径无根", "", policy.Payload{Type: policy.PayloadFileWrite, Path: "a.go"}},
		{"未知类型", "/p", policy.Payload{Type: "weird", Command: "x"}},
	}
	for i, tc := range bad {
		id := fmt.Sprintf("bad%d", i)
		insertReq(t, h.st, store.Request{ID: id, MachineID: m.ID, State: "pending", Payload: tc.p, ProjectRoot: tc.root})
		if _, err := h.srv.Decide(DecideInput{RequestID: id, Action: "deny", Remember: true}); !errors.Is(err, errLearn) {
			t.Fatalf("%s: 应 errLearn, 得到 %v", tc.name, err)
		}
		if r := getReq(t, h.st, id); r.State != "pending" {
			t.Fatalf("%s: 应保持 pending: %+v", tc.name, r)
		}
	}
	if after, _ := ruleRows(t, h.st); len(after) != len(before) {
		t.Fatalf("记不住时不应生成规则: %d → %d", len(before), len(after))
	}
}

// 学规则之后、改状态之前请求已被别处结束（取消、过期、重发顶掉）：删掉刚学的规则、记 rule_learn_rolled_back，
// 不下发决定；下发的规则快照里没有那条规则。
func TestRememberRolledBackWhenRequestSettledMeanwhile(t *testing.T) {
	h := newTestHubWith(t, nil, func(s *Server) {
		s.beforeSettle = func(r store.Request) {
			if err := s.store.SetRequestState(r.ID, "cancelled", "terminal", "", ""); err != nil {
				t.Errorf("取消请求: %v", err)
			}
		}
	})
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	c := newFakeConn(m.ID, 16)
	h.srv.agents.register(c)
	p := policy.Payload{Type: policy.PayloadShell, Command: "npm run build", Cwd: "/p"}
	insertReq(t, h.st, store.Request{ID: "r1", MachineID: m.ID, State: "pending", Payload: p, ProjectRoot: "/p"})
	if _, err := h.srv.Decide(DecideInput{RequestID: "r1", Action: "allow", Remember: true}); !errors.Is(err, store.ErrNotPending) {
		t.Fatalf("应 ErrNotPending: %v", err)
	}
	if rows, _ := ruleRows(t, h.st); len(rows) != 2 {
		t.Fatalf("刚学的规则应删掉: %+v", rows)
	}
	acts := auditActions(t, h.st, "r1")
	if countOf(acts, "rule_learn_rolled_back") != 1 || countOf(acts, "allow") != 0 || countOf(acts, "rule_learned") != 0 {
		t.Fatalf("审计: %v", acts)
	}
	if r := getReq(t, h.st, "r1"); r.State != "cancelled" || r.RuleID != "" {
		t.Fatalf("请求: %+v", r)
	}
	for len(c.out) > 0 {
		b := <-c.out
		kind, _ := wire.Kind(b)
		if kind == wire.TDecision || strings.Contains(string(b), "learned-") {
			t.Fatalf("不应下发决定或含已撤销规则的快照: %s", b)
		}
	}
}

func TestRulesImportExportToggleDelete(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	y := "version: 1\nrules:\n  - id: only\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n"
	resp := doReq(t, "POST", h.http.URL+"/api/rules/import", []byte(y), "application/yaml", ck)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import = %d", resp.StatusCode)
	}
	var out map[string]int
	decodeBody(t, resp, &out)
	if v, _ := h.st.RulesVersion(); out["version"] != v || v < 2 {
		t.Fatalf("import 应返回新版本: %+v (库里 %d)", out, v)
	}
	if kind, b := c.recv(5 * time.Second); kind != wire.TRules || !strings.Contains(string(b), "only") {
		t.Fatalf("导入后应下发规则: %s %s", kind, b)
	}
	resp = doReq(t, "GET", h.http.URL+"/api/rules/export", nil, "", ck)
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if !strings.Contains(buf.String(), "id: only") || resp.Header.Get("Content-Type") != "application/yaml" {
		t.Fatalf("export = %s %s", resp.Header.Get("Content-Type"), buf.String())
	}
	if _, err := policy.ParseRuleSet(buf.Bytes()); err != nil {
		t.Fatalf("导出的应能再导入: %v", err)
	}
	if resp := doReq(t, "PATCH", h.http.URL+"/api/rules/only", []byte(`{"enabled":false}`), "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("patch = %d", resp.StatusCode)
	}
	if kind, b := c.recv(5 * time.Second); kind != wire.TRules || !strings.Contains(string(b), "enabled: false") {
		t.Fatalf("启停后应下发规则: %s %s", kind, b)
	}
	if resp := doReq(t, "PATCH", h.http.URL+"/api/rules/only", []byte(`{}`), "application/json", ck); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺 enabled 应 400, 得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "PATCH", h.http.URL+"/api/rules/nope", []byte(`{"enabled":true}`), "application/json", ck); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的规则应 404, 得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "DELETE", h.http.URL+"/api/rules/only", nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if kind, b := c.recv(5 * time.Second); kind != wire.TRules || strings.Contains(string(b), "only") {
		t.Fatalf("删除后应下发不含它的规则: %s %s", kind, b)
	}
	if resp := doReq(t, "DELETE", h.http.URL+"/api/rules/only", nil, "", ck); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("再删应 404, 得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "POST", h.http.URL+"/api/rules/import", []byte("version: 9\n"), "application/yaml", ck); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏 YAML 应 400, 得到 %d", resp.StatusCode)
	}
	for _, a := range []string{"rules_imported", "rule_disabled", "rule_deleted"} {
		if _, ok := lastAudit(t, h.st, a); !ok {
			t.Fatalf("应有审计 %s", a)
		}
	}
}

// elevated：user 来源、不限风险或风险不低于 outward 的 allow。/api/rules 每条带标记；导入时审计启用着的这类规则 id；
// 把这类规则从停用改为启用时单独审计；learned 来源放行 outward 的导入照旧 400。
func TestRulesElevated(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	y := `version: 1
rules:
  - id: push-ok
    source: user
    scope: global
    match: {payload: shell, command_prefix: git push}
    action: allow
  - id: nuke-ok
    source: user
    scope: global
    match: {risk: destructive, command_prefix: rm -rf build}
    action: allow
    enabled: false
  - id: read-ok
    source: user
    scope: global
    match: {risk: read}
    action: allow
  - id: deny-curl
    source: user
    scope: global
    match: {command_prefix: curl}
    action: deny
`
	resp := doReq(t, "POST", h.http.URL+"/api/rules/import", []byte(y), "text/yaml; charset=utf-8", ck)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import = %d", resp.StatusCode)
	}
	ev, ok := lastAudit(t, h.st, "rules_imported")
	var detail struct {
		Version  int      `json:"version"`
		Elevated []string `json:"elevated_rule_ids"`
	}
	if !ok || json.Unmarshal([]byte(ev.Detail), &detail) != nil || detail.Version == 0 || !slices.Equal(detail.Elevated, []string{"push-ok"}) {
		t.Fatalf("导入审计应列出启用着的提升规则: %+v %+v", ev, detail)
	}
	var rules []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/rules", nil, "", ck), &rules)
	want := map[string]bool{"push-ok": true, "nuke-ok": true, "read-ok": false, "deny-curl": false}
	if len(rules) != len(want) {
		t.Fatalf("rules = %+v", rules)
	}
	for _, r := range rules {
		if r["elevated"] != want[r["id"].(string)] {
			t.Fatalf("%s 的 elevated 应为 %v: %+v", r["id"], want[r["id"].(string)], r)
		}
	}
	patch := func(id string, enabled bool) {
		t.Helper()
		body := mustJSON(t, map[string]bool{"enabled": enabled})
		if resp := doReq(t, "PATCH", h.http.URL+"/api/rules/"+id, body, "application/json", ck); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("patch %s = %d", id, resp.StatusCode)
		}
	}
	lastRuleAudit := func() store.AuditEvent {
		t.Helper()
		events, err := h.st.ListAudit(1, "")
		if err != nil || len(events) != 1 {
			t.Fatalf("审计: %v %v", events, err)
		}
		return events[0]
	}
	patch("nuke-ok", true)
	if e := lastRuleAudit(); e.Action != "rule_enabled_elevated" || !strings.Contains(e.Detail, `"nuke-ok"`) {
		t.Fatalf("启用提升规则应单独审计: %+v", e)
	}
	patch("nuke-ok", true) // 已启用，不是“从停用改为启用”
	if e := lastRuleAudit(); e.Action != "rule_enabled" {
		t.Fatalf("已启用的再启用: %+v", e)
	}
	patch("read-ok", false)
	if e := lastRuleAudit(); e.Action != "rule_disabled" || !strings.Contains(e.Detail, `"read-ok"`) {
		t.Fatalf("停用: %+v", e)
	}
	patch("read-ok", true)
	if e := lastRuleAudit(); e.Action != "rule_enabled" {
		t.Fatalf("启用普通规则: %+v", e)
	}
	// 没有提升规则时照常记 rules_imported，列表为空
	plain := "version: 1\nrules:\n  - id: only\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n"
	if resp := doReq(t, "POST", h.http.URL+"/api/rules/import", []byte(plain), "application/yaml", ck); resp.StatusCode != http.StatusOK {
		t.Fatalf("import = %d", resp.StatusCode)
	}
	if ev, _ := lastAudit(t, h.st, "rules_imported"); !strings.Contains(ev.Detail, `"elevated_rule_ids":[]`) {
		t.Fatalf("没有提升规则时列表应为空: %+v", ev)
	}
	version, _ := h.st.RulesVersion()
	learned := `version: 1
rules:
  - id: bad
    source: learned
    scope: global
    match: {payload: shell, risk: outward, command_prefix: git push}
    action: allow
`
	if resp := doReq(t, "POST", h.http.URL+"/api/rules/import", []byte(learned), "application/yaml", ck); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("learned 放行 outward 应 400, 得到 %d", resp.StatusCode)
	}
	if v, _ := h.st.RulesVersion(); v != version {
		t.Fatalf("导入失败不应改规则: %d → %d", version, v)
	}
}

// 导入的规则集编成 welcome/rules 帧超过单帧上限时 413，不入库：否则守护进程永远连不上（welcome 编不出来）。
func TestRulesImportTooLargeForFrame(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	var b strings.Builder
	b.WriteString("version: 1\nrules:\n")
	re := strings.Repeat(`\\`, 500) // YAML 里原样，JSON 里每个反斜杠翻倍
	for i := 0; b.Len() < 800<<10; i++ {
		fmt.Fprintf(&b, "  - id: r%d\n    source: user\n    scope: global\n    match: {command_regex: '%s'}\n    action: deny\n", i, re)
	}
	version, _ := h.st.RulesVersion()
	resp := doReq(t, "POST", h.http.URL+"/api/rules/import", []byte(b.String()), "application/yaml", ck)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("超过单帧上限应 413, 得到 %d", resp.StatusCode)
	}
	if v, _ := h.st.RulesVersion(); v != version {
		t.Fatalf("不应入库: %d → %d", version, v)
	}
	big := bytes.Repeat([]byte("#"), 1<<20+1)
	if resp := doReq(t, "POST", h.http.URL+"/api/rules/import", big, "application/yaml", ck); resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("请求体超过 1 MiB 应 413, 得到 %d", resp.StatusCode)
	}
}

// CSRF 纵深防御：需要 cookie 的状态变更路由带了跨站 Origin（含 null）回 403，同源或不带 Origin 放行；
// 导入只收 YAML 类型，否则 415。
func TestStateChangingRoutesCheckOrigin(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	insertReq(t, h.st, store.Request{ID: "r1", MachineID: m.ID, State: "pending"})
	evil := http.Header{"Origin": {"https://evil.example"}}
	null := http.Header{"Origin": {"null"}}
	same := http.Header{"Origin": {h.http.URL}}
	routes := []struct{ method, path, ct, body string }{
		{"POST", "/api/pause", "", ""},
		{"POST", "/api/resume", "", ""},
		{"POST", "/api/requests/r1/decide", "application/json", `{"action":"deny"}`},
		{"PATCH", "/api/rules/builtin-readonly", "application/json", `{"enabled":false}`},
		{"DELETE", "/api/rules/builtin-readonly", "", ""},
		{"POST", "/api/rules/import", "application/yaml", "version: 1\nrules: []\n"},
		{"DELETE", "/api/machines/" + m.ID, "", ""},
		{"POST", "/api/logout", "", ""},
	}
	for _, rt := range routes {
		for _, hdr := range []http.Header{evil, null} {
			if resp := doReqHdr(t, rt.method, h.http.URL+rt.path, []byte(rt.body), rt.ct, ck, hdr); resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s %s 带 Origin %s 应 403, 得到 %d", rt.method, rt.path, hdr.Get("Origin"), resp.StatusCode)
			}
		}
	}
	if r := getReq(t, h.st, "r1"); r.State != "pending" {
		t.Fatalf("跨站请求不应生效: %+v", r)
	}
	if mm, _ := h.st.GetMachine(m.ID); mm.Revoked {
		t.Fatal("跨站请求不应吊销机器")
	}
	if resp := doReqHdr(t, "POST", h.http.URL+"/api/pause", nil, "", ck, same); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("同源应放行, 得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "POST", h.http.URL+"/api/resume", nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("不带 Origin 应放行, 得到 %d", resp.StatusCode)
	}
	for _, ct := range []string{"", "application/json", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		if resp := doReq(t, "POST", h.http.URL+"/api/rules/import", []byte("version: 1\nrules: []\n"), ct, ck); resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("Content-Type %q 应 415, 得到 %d", ct, resp.StatusCode)
		}
	}
	// 未登录照旧 401
	if resp := doReq(t, "POST", h.http.URL+"/api/pause", nil, "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, 得到 %d", resp.StatusCode)
	}
	// 跨站的 /ws/client 握手也拒
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{"Cookie": {ck.Name + "=" + ck.Value}, "Origin": {"https://evil.example"}}
	if _, resp, err := websocket.Dial(ctx, clientWSURL(h), &websocket.DialOptions{HTTPHeader: hdr}); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("跨站 /ws/client 应 403: %v", err)
	}
}

func TestPauseResumeAndMachines(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	if resp := doReq(t, "POST", h.http.URL+"/api/pause", nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("pause = %d", resp.StatusCode)
	}
	if kind, b := c.recv(5 * time.Second); kind != wire.TPaused || !strings.Contains(string(b), "true") {
		t.Fatalf("应下发 paused: %s %s", kind, b)
	}
	if b := nextBroadcast(t, sub); !contains(b, `"type":"status"`) || !contains(b, `"paused":true`) {
		t.Fatalf("应广播 status: %s", b)
	}
	if v, _, _ := h.st.GetSetting("paused"); v != "1" {
		t.Fatalf("settings paused = %q", v)
	}
	var st map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/status", nil, "", ck), &st)
	if st["paused"] != true || st["pending"] != float64(0) || st["uptime"] == nil || st["rules_version"] == nil {
		t.Fatalf("status = %+v", st)
	}
	machines := st["machines"].([]any)
	if len(machines) != 1 || machines[0].(map[string]any)["online"] != true || machines[0].(map[string]any)["name"] != "mac" {
		t.Fatalf("machines = %+v", machines)
	}
	doReq(t, "POST", h.http.URL+"/api/resume", nil, "", ck)
	if kind, b := c.recv(5 * time.Second); kind != wire.TPaused || !strings.Contains(string(b), "false") {
		t.Fatalf("应下发 paused=false: %s %s", kind, b)
	}
	if v, _, _ := h.st.GetSetting("paused"); v != "0" {
		t.Fatalf("settings paused = %q", v)
	}
	if _, ok := lastAudit(t, h.st, "paused=true"); !ok {
		t.Fatal("暂停应审计")
	}
	var ms []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/machines", nil, "", ck), &ms)
	if len(ms) != 1 || ms[0]["id"] != id || ms[0]["online"] != true || ms[0]["revoked"] != false {
		t.Fatalf("machines = %+v", ms)
	}
	if resp := doReq(t, "DELETE", h.http.URL+"/api/machines/nope", nil, "", ck); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知机器应 404, 得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "DELETE", h.http.URL+"/api/machines/"+id, nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}
	waitFor(t, "连接断开", func() bool { return !h.srv.agents.connected(id) })
	c.expectClosed(5 * time.Second)
	if _, err := h.st.AuthMachine(secret); err == nil {
		t.Fatal("吊销后密钥应失效")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.Dial(ctx, agentURL(h), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("吊销后重连应 401: %v", err)
	}
	if _, ok := lastAudit(t, h.st, "machine_revoked"); !ok {
		t.Fatal("吊销应审计")
	}
}

// 吊销与握手并发：守护进程认证之后、发 hello 之前被吊销（此时还没登记，disconnect 断不到它），登记后也要被断开，不发 welcome。
func TestRevokeDuringHandshake(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialRaw(t, h, secret)
	defer c.close()
	if resp := doReq(t, "DELETE", h.http.URL+"/api/machines/"+id, nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}
	c.send(wire.Hello{T: wire.THello, Proto: wire.Proto, MachineID: id})
	if code := c.expectClosed(5 * time.Second); code != websocket.StatusPolicyViolation {
		t.Fatalf("应以 PolicyViolation 断开, 得到 %v", code)
	}
	waitFor(t, "吊销的机器移出连接表", func() bool { return !h.srv.agents.connected(id) })
}

func TestStatusPausedCorrupt(t *testing.T) {
	h, logs := quietHub(t)
	ck := loggedIn(t, h)
	if err := h.st.SetSetting("paused", "maybe"); err != nil {
		t.Fatal(err)
	}
	if resp := doReq(t, "GET", h.http.URL+"/api/status", nil, "", ck); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("暂停设置损坏应 500, 得到 %d", resp.StatusCode)
	}
	if !strings.Contains(logs.String(), "暂停") {
		t.Fatalf("应记日志: %q", logs.String())
	}
}

// 请求列表：limit 默认 50、最大 500、≤0 当默认；按 state 过滤；详情返回 {"request","audit"}。
func TestRequestsListAndDetail(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("mac", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour)
	for i := range 520 {
		state := "pending"
		if i%2 == 1 {
			state = "denied"
		}
		insertReq(t, h.st, store.Request{ID: fmt.Sprintf("q%03d", i), MachineID: m.ID, State: state, ProjectRoot: "/w/proj",
			CreatedAt: store.FormatTime(base.Add(time.Duration(i) * time.Second))})
	}
	count := func(query string) int {
		t.Helper()
		var rs []map[string]any
		decodeBody(t, doReq(t, "GET", h.http.URL+"/api/requests"+query, nil, "", ck), &rs)
		return len(rs)
	}
	if n := count(""); n != 50 {
		t.Fatalf("默认 50, 得到 %d", n)
	}
	if n := count("?limit=0"); n != 50 {
		t.Fatalf("limit=0 当默认, 得到 %d", n)
	}
	if n := count("?limit=-3"); n != 50 {
		t.Fatalf("limit<0 当默认, 得到 %d", n)
	}
	if n := count("?limit=10000"); n != 500 {
		t.Fatalf("最大 500, 得到 %d", n)
	}
	if n := count("?state=denied&limit=500"); n != 260 {
		t.Fatalf("按 state 过滤, 得到 %d", n)
	}
	var rs []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/requests?limit=1", nil, "", ck), &rs)
	if len(rs) != 1 || rs[0]["id"] != "q519" || rs[0]["machine_name"] != "mac" || rs[0]["project"] != "proj" {
		t.Fatalf("最新一条: %+v", rs)
	}
	if _, err := h.srv.Decide(DecideInput{RequestID: "q000", Action: "deny", Note: "不行"}); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Request map[string]any     `json:"request"`
		Audit   []store.AuditEvent `json:"audit"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/requests/q000", nil, "", ck), &detail)
	if detail.Request["id"] != "q000" || detail.Request["state"] != "denied" || detail.Request["machine_name"] != "mac" ||
		len(detail.Audit) != 1 || detail.Audit[0].Action != "deny" {
		t.Fatalf("详情: %+v", detail)
	}
	if resp := doReq(t, "GET", h.http.URL+"/api/requests/nope", nil, "", ck); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在应 404, 得到 %d", resp.StatusCode)
	}
	var inbox []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
	if len(inbox) != 259 {
		t.Fatalf("收件箱应列出全部 pending, 得到 %d", len(inbox))
	}
	if resp := doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, 得到 %d", resp.StatusCode)
	}
}

// decide 接口的错误码：坏 JSON、坏 action、备注超过 1000 字符 400；不存在 404；已过期 409。
func TestDecideHTTPErrors(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	insertReq(t, h.st, store.Request{ID: "r1", MachineID: m.ID, State: "pending"})
	past := store.FormatTime(time.Now().Add(-time.Minute))
	insertReq(t, h.st, store.Request{ID: "old", MachineID: m.ID, State: "pending", ExpiresAt: &past})
	post := func(id string, body []byte) int {
		t.Helper()
		return doReq(t, "POST", h.http.URL+"/api/requests/"+id+"/decide", body, "application/json", ck).StatusCode
	}
	for name, tc := range map[string]struct {
		id   string
		body []byte
		want int
	}{
		"坏 JSON":   {"r1", []byte(`{"action":`), http.StatusBadRequest},
		"坏 action": {"r1", decideBody(t, "maybe", false, ""), http.StatusBadRequest},
		"备注过长":     {"r1", decideBody(t, "deny", false, strings.Repeat("备", 1001)), http.StatusBadRequest},
		"不存在":      {"nope", decideBody(t, "deny", false, ""), http.StatusNotFound},
		"已过期":      {"old", decideBody(t, "allow", false, ""), http.StatusConflict},
	} {
		if got := post(tc.id, tc.body); got != tc.want {
			t.Fatalf("%s: 应 %d, 得到 %d", name, tc.want, got)
		}
	}
	if r := getReq(t, h.st, "r1"); r.State != "pending" {
		t.Fatalf("出错的决定不应改状态: %+v", r)
	}
	if got := post("r1", decideBody(t, "deny", false, strings.Repeat("备", 1000))); got != http.StatusOK {
		t.Fatalf("1000 字符的备注应接受, 得到 %d", got)
	}
}

func clientWSURL(h *testHub) string {
	return "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws/client"
}

func TestClientWS(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, resp, err := websocket.Dial(ctx, clientWSURL(h), nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 cookie 应 401: %v", err)
	}
	conn, _, err := websocket.Dial(ctx, clientWSURL(h), &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {ck.Name + "=" + ck.Value}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	h.srv.clients.broadcast("request_new", map[string]string{"id": "x"})
	_, b, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(b), `"type":"request_new"`) {
		t.Fatalf("应收到广播: %s %v", b, err)
	}
}

// /ws/client 定时复核登录会话并 ping：会话有效时复核多轮、照常收帧；换密码（会话全部作废）后断开。
func TestClientWSRechecksSession(t *testing.T) {
	h := newTestHubWith(t, func(c *Config) { c.PingInterval = 20 * time.Millisecond }, nil)
	ck := loggedIn(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, clientWSURL(h), &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {ck.Name + "=" + ck.Value}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	frames, closed := readFrames(t, conn)
	base := h.srv.clientRechecks.Load()
	waitFor(t, "复核至少 3 轮", func() bool { return h.srv.clientRechecks.Load() >= base+3 })
	h.srv.clients.broadcast("status", map[string]bool{"paused": false})
	select {
	case b := <-frames:
		if !strings.Contains(string(b), `"type":"status"`) {
			t.Fatalf("帧: %s", b)
		}
	case err := <-closed:
		t.Fatalf("会话有效时连接不应断开: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("会话有效时应照常收到广播")
	}
	if err := h.srv.SetPassword("another22"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-closed:
		if code := websocket.CloseStatus(err); code != websocket.StatusPolicyViolation {
			t.Fatalf("会话作废后应以 PolicyViolation 断开: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("会话作废后应断开")
	}
}

// readFrames 在后台一直读 conn（顺带回 pong），数据帧送进 frames，读出错时把错误送进 closed 后退出。不调 t.Fatal。
// 测试结束时关掉 conn 并等后台 goroutine 退出：没人收的帧不会让它一直堵在 frames 上。
func readFrames(t *testing.T, conn *websocket.Conn) (<-chan []byte, <-chan error) {
	frames, closed := make(chan []byte, 16), make(chan error, 1)
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		for {
			_, b, err := conn.Read(context.Background())
			if err != nil {
				closed <- err
				return
			}
			select {
			case frames <- b:
			case <-done:
				return
			}
		}
	}()
	t.Cleanup(func() {
		close(done)
		conn.CloseNow()
		<-exited
	})
	return frames, closed
}

// 带同源 Origin 头的 /ws/client 握手照常建立并收到广播。
func TestClientWSSameOrigin(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hdr := http.Header{"Cookie": {ck.Name + "=" + ck.Value}, "Origin": {h.http.URL}}
	conn, _, err := websocket.Dial(ctx, clientWSURL(h), &websocket.DialOptions{HTTPHeader: hdr})
	if err != nil {
		t.Fatalf("同源握手应成功: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	h.srv.clients.broadcast("request_new", map[string]string{"id": "x"})
	if _, b, err := conn.Read(ctx); err != nil || !strings.Contains(string(b), `"type":"request_new"`) {
		t.Fatalf("应收到广播: %s %v", b, err)
	}
}

// 需要登录的路由（读与写）未登录或 cookie 无效时一律 401，且不生效。
func TestAPIRoutesRequireLogin(t *testing.T) {
	h := newTestHub(t)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	insertReq(t, h.st, store.Request{ID: "r1", MachineID: m.ID, State: "pending"})
	routes := []struct{ method, path, ct, body string }{
		{"GET", "/api/status", "", ""},
		{"POST", "/api/pause", "", ""},
		{"POST", "/api/resume", "", ""},
		{"GET", "/api/inbox", "", ""},
		{"GET", "/api/requests", "", ""},
		{"GET", "/api/requests/r1", "", ""},
		{"POST", "/api/requests/r1/decide", "application/json", `{"action":"deny"}`},
		{"GET", "/api/rules", "", ""},
		{"PATCH", "/api/rules/builtin-readonly", "application/json", `{"enabled":false}`},
		{"DELETE", "/api/rules/builtin-readonly", "", ""},
		{"POST", "/api/rules/import", "application/yaml", "version: 1\nrules: []\n"},
		{"GET", "/api/rules/export", "", ""},
		{"GET", "/api/machines", "", ""},
		{"DELETE", "/api/machines/" + m.ID, "", ""},
		{"GET", "/ws/client", "", ""},
	}
	bogus := &http.Cookie{Name: cookieName, Value: "not-a-session"}
	for _, rt := range routes {
		for _, ck := range []*http.Cookie{nil, bogus} {
			if resp := doReq(t, rt.method, h.http.URL+rt.path, []byte(rt.body), rt.ct, ck); resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("%s %s（cookie %v）应 401, 得到 %d", rt.method, rt.path, ck != nil, resp.StatusCode)
			}
		}
	}
	if r := getReq(t, h.st, "r1"); r.State != "pending" {
		t.Fatalf("未登录的决定不应生效: %+v", r)
	}
	if mm, _ := h.st.GetMachine(m.ID); mm.Revoked {
		t.Fatal("未登录不应吊销机器")
	}
	if rows, _ := ruleRows(t, h.st); len(rows) != 2 || !rows[0].Enabled {
		t.Fatalf("未登录不应改规则: %+v", rows)
	}
	if v, ok, _ := h.st.GetSetting("paused"); ok && v != "0" {
		t.Fatalf("未登录不应暂停: %q", v)
	}
}

// “记住”的锚点（命令、路径、MCP 服务与工具、主机名）超过 4 KiB 时回 422：不入库、不下发、请求保持 pending。
// 约 600 KB 的 heredoc 命令若学成规则，规则快照与 welcome 都会超过单帧上限，所有守护进程会连不上。
func TestRememberTooLong(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	c := newFakeConn(m.ID, 16)
	h.srv.agents.register(c)
	long := strings.Repeat("x", 4097)
	payloads := []policy.Payload{
		{Type: policy.PayloadShell, Command: "cat <<'EOF' > notes.txt\n" + strings.Repeat("x", 600<<10) + "\nEOF", Cwd: "/p"},
		{Type: policy.PayloadShell, Command: "echo " + long, Cwd: "/p"},
		{Type: policy.PayloadFileWrite, Path: "/p/" + long},
		{Type: policy.PayloadMCP, Server: long, Tool: "t"},
		{Type: policy.PayloadMCP, Server: "s", Tool: long},
		{Type: policy.PayloadNetwork, URL: "https://" + strings.Repeat("a.", 2100) + "example.com/"},
	}
	version, _ := h.st.RulesVersion()
	for i, p := range payloads {
		for _, action := range []string{"deny", "allow"} {
			id := fmt.Sprintf("big%d%s", i, action)
			insertReq(t, h.st, store.Request{ID: id, MachineID: m.ID, State: "pending", Payload: p, ProjectRoot: "/p"})
			resp := doReq(t, "POST", h.http.URL+"/api/requests/"+id+"/decide", decideBody(t, action, true, ""), "application/json", ck)
			var body map[string]string
			decodeBody(t, resp, &body)
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body["error"], "太长") {
				t.Fatalf("%d %s: 应 422 太长, 得到 %d %+v", i, action, resp.StatusCode, body)
			}
			if r := getReq(t, h.st, id); r.State != "pending" {
				t.Fatalf("%d %s: 应保持 pending: %+v", i, action, r)
			}
		}
	}
	if v, _ := h.st.RulesVersion(); v != version {
		t.Fatalf("不应入库: 版本 %d → %d", version, v)
	}
	if len(c.out) != 0 {
		t.Fatalf("不应下发任何帧: %d", len(c.out))
	}
}

// 规则集逼近单帧上限时，再记住一条（锚点虽不到 4 KiB）放不下也回 422，不入库：批准与拒绝都查。
func TestRememberRefusedNearFrameLimit(t *testing.T) {
	h := newTestHub(t)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	filler := func(n int) policy.RuleSet {
		rs := policy.RuleSet{Version: policy.RuleSetVersion}
		for i := range n {
			rs.Rules = append(rs.Rules, policy.Rule{ID: fmt.Sprintf("f%d", i), Source: policy.SourceUser, Action: policy.ActionDeny,
				Match: policy.Match{CommandRegex: strings.Repeat(`\\`, 500)}})
		}
		return rs
	}
	// 二分出放得下的最多条数：再加一条填充规则（约 2 KB）就放不下，剩余空间小于下面约 4 KB 的新规则
	lo, hi := 0, 2000
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if rulesFitFrame(filler(mid)) {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	if lo == 0 || lo == 2000 {
		t.Fatalf("填充条数异常: %d", lo)
	}
	if _, err := h.st.ReplaceRules(filler(lo)); err != nil {
		t.Fatal(err)
	}
	version, _ := h.st.RulesVersion()
	for _, tc := range []struct{ id, cmd, action string }{
		{"n1", "echo " + strings.Repeat("a", 4000), "deny"},
		{"n2", "ls " + strings.Repeat("a", 4000), "allow"},
	} {
		p := policy.Payload{Type: policy.PayloadShell, Command: tc.cmd, Cwd: "/p"}
		insertReq(t, h.st, store.Request{ID: tc.id, MachineID: m.ID, State: "pending", Payload: p, ProjectRoot: "/p"})
		if _, err := h.srv.Decide(DecideInput{RequestID: tc.id, Action: tc.action, Remember: true}); !errors.Is(err, errLearn) || !strings.Contains(err.Error(), "上限") {
			t.Fatalf("%s: 放不下应 errLearn（说明接近上限）, 得到 %v", tc.action, err)
		}
		if r := getReq(t, h.st, tc.id); r.State != "pending" {
			t.Fatalf("%s: 应保持 pending: %+v", tc.action, r)
		}
	}
	if v, _ := h.st.RulesVersion(); v != version {
		t.Fatalf("不应入库: 版本 %d → %d", version, v)
	}
}

// 撤销刚学的规则时它已经不在（期间被别处删掉）：当作已删除，审计如实标出，不记“删除失败”的日志。
func TestRememberRollbackRuleAlreadyGone(t *testing.T) {
	var logs syncBuf
	h := newTestHubWith(t, nil, func(s *Server) {
		s.log = log.New(&logs, "", 0)
		s.beforeSettle = func(r store.Request) {
			rows, _ := s.store.ListRules()
			if _, err := s.store.DeleteRule(rows[len(rows)-1].ID); err != nil {
				t.Errorf("删规则: %v", err)
			}
			if err := s.store.SetRequestState(r.ID, "cancelled", "terminal", "", ""); err != nil {
				t.Errorf("取消请求: %v", err)
			}
		}
	})
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	p := policy.Payload{Type: policy.PayloadShell, Command: "npm run build", Cwd: "/p"}
	insertReq(t, h.st, store.Request{ID: "r1", MachineID: m.ID, State: "pending", Payload: p, ProjectRoot: "/p"})
	if _, err := h.srv.Decide(DecideInput{RequestID: "r1", Action: "allow", Remember: true}); !errors.Is(err, store.ErrNotPending) {
		t.Fatalf("应 ErrNotPending: %v", err)
	}
	events, _ := h.st.ListAudit(0, "r1")
	var detail map[string]any
	for _, e := range events {
		if e.Action == "rule_learn_rolled_back" {
			json.Unmarshal([]byte(e.Detail), &detail)
		}
	}
	if detail["deleted"] != true || detail["already_gone"] != true {
		t.Fatalf("审计应如实标出规则已不在: %+v", detail)
	}
	if strings.Contains(logs.String(), "失败") {
		t.Fatalf("不应记删除失败: %q", logs.String())
	}
}

// 吊销机器时，它仍 pending 的请求改为 cancelled、decided_by machine_revoked，审计并广播；别的状态、别的机器的不动。
func TestRevokeCancelsPending(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	other, _ := h.st.CreateMachine("o", store.RandomToken(), "/home/u")
	insertReq(t, h.st, store.Request{ID: "p1", MachineID: m.ID, State: "pending"})
	insertReq(t, h.st, store.Request{ID: "p2", MachineID: m.ID, State: "pending"})
	insertReq(t, h.st, store.Request{ID: "a1", MachineID: m.ID, State: "approved", DecidedBy: "user"})
	insertReq(t, h.st, store.Request{ID: "o1", MachineID: other.ID, State: "pending"})
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	if resp := doReq(t, "DELETE", h.http.URL+"/api/machines/"+m.ID, nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}
	for _, id := range []string{"p1", "p2"} {
		if r := getReq(t, h.st, id); r.State != "cancelled" || r.DecidedBy != "machine_revoked" || r.DecidedAt == nil {
			t.Fatalf("%s 应被取消: %+v", id, r)
		}
		if acts := auditActions(t, h.st, id); countOf(acts, "cancelled") != 1 {
			t.Fatalf("%s 应审计: %v", id, acts)
		}
	}
	waitBroadcast(t, sub, "request_updated", "p1", nil)
	waitBroadcast(t, sub, "request_updated", "p2", nil)
	if r := getReq(t, h.st, "a1"); r.State != "approved" {
		t.Fatalf("已决定的不应改: %+v", r)
	}
	if r := getReq(t, h.st, "o1"); r.State != "pending" {
		t.Fatalf("别的机器的不应改: %+v", r)
	}
}

// 请求所属机器已吊销、请求却还 pending（吊销时没取消到，如与在途 pending 竞态）：决定时把它改为 cancelled/machine_revoked，
// 审计并广播，回 ErrNotPending，不学规则、不下发；HTTP 回 409。
func TestDecideRevokedMachineCancels(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, _ := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	insertReq(t, h.st, store.Request{ID: "v1", MachineID: m.ID, State: "pending", ProjectRoot: "/p",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "ls", Cwd: "/p"}})
	insertReq(t, h.st, store.Request{ID: "v2", MachineID: m.ID, State: "pending"})
	if err := h.st.RevokeMachine(m.ID); err != nil { // 直接改库：绕过吊销接口里的取消
		t.Fatal(err)
	}
	c := newFakeConn(m.ID, 4)
	h.srv.agents.register(c)
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	if _, err := h.srv.Decide(DecideInput{RequestID: "v1", Action: "allow", Remember: true}); !errors.Is(err, store.ErrNotPending) {
		t.Fatalf("机器已吊销应回 ErrNotPending: %v", err)
	}
	if r := getReq(t, h.st, "v1"); r.State != "cancelled" || r.DecidedBy != "machine_revoked" || r.DecidedAt == nil {
		t.Fatalf("v1 应被取消: %+v", r)
	}
	if acts := auditActions(t, h.st, "v1"); countOf(acts, "cancelled") != 1 || countOf(acts, "allow") != 0 || countOf(acts, "rule_learned") != 0 {
		t.Fatalf("v1 审计: %v", acts)
	}
	waitBroadcast(t, sub, "request_updated", "v1", nil)
	if rows, _ := ruleRows(t, h.st); len(rows) != 2 {
		t.Fatalf("不应学规则: %+v", rows)
	}
	if len(c.out) != 0 {
		t.Fatalf("不应下发: %d", len(c.out))
	}
	if resp := doReq(t, "POST", h.http.URL+"/api/requests/v2/decide", decideBody(t, "deny", false, ""), "application/json", ck); resp.StatusCode != http.StatusConflict {
		t.Fatalf("机器已吊销应 409, 得到 %d", resp.StatusCode)
	}
	if r := getReq(t, h.st, "v2"); r.State != "cancelled" || r.DecidedBy != "machine_revoked" {
		t.Fatalf("v2 应被取消: %+v", r)
	}
}

// Run 关停时等 /ws/client 与 /ws/agent 的 handler 都退出后才返回（handler 退出前的钩子故意慢一些）。
func TestRunWaitsForWebSocketHandlers(t *testing.T) {
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
	var exited atomic.Int32
	srv.beforeWSExit = func() {
		time.Sleep(200 * time.Millisecond)
		exited.Add(1)
	}
	if err := srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	secret := store.RandomToken()
	m, err := st.CreateMachine("m", secret, "/h")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	const prefix = "hub 监听 "
	waitFor(t, "Run 开始监听", func() bool { return strings.Contains(logs.String(), prefix) })
	line := logs.String()
	addr := strings.TrimSpace(strings.SplitN(line[strings.Index(line, prefix)+len(prefix):], "\n", 2)[0])
	resp := postJSON(t, "http://"+addr+"/api/login", map[string]string{"password": "hunter22"}, nil)
	var ck *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == cookieName {
			ck = c
		}
	}
	if ck == nil {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()
	client, _, err := websocket.Dial(dctx, "ws://"+addr+"/ws/client", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {ck.Name + "=" + ck.Value}}})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseNow()
	readFrames(t, client)
	agentConn, _, err := websocket.Dial(dctx, "ws://"+addr+"/ws/agent", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
	if err != nil {
		t.Fatal(err)
	}
	defer agentConn.CloseNow()
	agentConn.SetReadLimit(wire.MaxFrame)
	ac := &agentClient{conn: agentConn, t: t}
	ac.send(wire.Hello{T: wire.THello, Proto: wire.Proto, MachineID: m.ID})
	if kind, _ := ac.recv(5 * time.Second); kind != wire.TWelcome {
		t.Fatalf("应先收到 welcome, 得到 %s", kind)
	}
	readFrames(t, agentConn) // 关停时要读到关闭帧并回应，关闭握手才能完成
	waitFor(t, "两个 handler 在跑", func() bool { return srv.ws.count() == 2 })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run 返回 %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 Run 未返回")
	}
	if n, live := exited.Load(), srv.ws.count(); n != 2 || live != 0 {
		t.Fatalf("Run 返回时 handler 应都已退出: 已退出 %d, 仍在跑 %d", n, live)
	}
}

// 修复轮 1：WebSocket 处理没在关停时限内退出时，Run 仍尽力收尾（停过期扫描、停回调），之后返回 ErrShutdownTimeout，
// 调用方据此不关库（那个处理还可能碰存储）。
func TestRunReportsShutdownTimeout(t *testing.T) {
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
	release := make(chan struct{})
	var releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(letGo) // 后登记先执行：关库之前先放 handler 走
	srv.beforeWSExit = func() { <-release }
	secret := store.RandomToken()
	m, err := st.CreateMachine("m", secret, "/h")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	const prefix = "hub 监听 "
	waitFor(t, "Run 开始监听", func() bool { return strings.Contains(logs.String(), prefix) })
	line := logs.String()
	addr := strings.TrimSpace(strings.SplitN(line[strings.Index(line, prefix)+len(prefix):], "\n", 2)[0])
	dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dcancel()
	agentConn, _, err := websocket.Dial(dctx, "ws://"+addr+"/ws/agent", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
	if err != nil {
		t.Fatal(err)
	}
	defer agentConn.CloseNow()
	agentConn.SetReadLimit(wire.MaxFrame)
	ac := &agentClient{conn: agentConn, t: t}
	ac.send(wire.Hello{T: wire.THello, Proto: wire.Proto, MachineID: m.ID})
	if kind, _ := ac.recv(5 * time.Second); kind != wire.TWelcome {
		t.Fatalf("应先收到 welcome, 得到 %s", kind)
	}
	readFrames(t, agentConn)
	waitFor(t, "handler 在跑", func() bool { return srv.ws.count() == 1 })
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownTimeout) {
			t.Fatalf("handler 没退出时 Run 应返回 ErrShutdownTimeout: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 Run 未返回")
	}
	if el := time.Since(start); el > shutdownTimeout+callbackStopGrace+2*time.Second {
		t.Fatalf("关停超时也应在时限内返回: %s", el)
	}
	letGo()
	waitFor(t, "handler 退出", func() bool { return srv.ws.count() == 0 })
}

// 收尾修复 a：Serve 自己异常结束（监听器出错，不是取消）时也走完整关停：取消请求的 context、断开守护进程、等 WebSocket
// 处理退出后才返回 Serve 的错误；时限内等不到它们退出时错误同样满足 ErrShutdownTimeout，调用方据此不关库。
func TestServeEndedStillShutsDown(t *testing.T) {
	old := shutdownTimeout
	shutdownTimeout = 500 * time.Millisecond
	t.Cleanup(func() { shutdownTimeout = old })
	for _, stuck := range []bool{false, true} {
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
		var logs syncBuf
		srv, err := New(DefaultConfig(dir), st, log.New(&logs, "", 0))
		if err != nil {
			t.Fatal(err)
		}
		release := make(chan struct{})
		var releaseOnce sync.Once
		letGo := func() { releaseOnce.Do(func() { close(release) }) }
		t.Cleanup(letGo) // 后登记先执行：关库之前先放 handler 走
		var exited atomic.Int32
		srv.beforeWSExit = func() {
			if stuck {
				<-release
			} else {
				time.Sleep(100 * time.Millisecond)
			}
			exited.Add(1)
		}
		if err := srv.SetPassword("hunter22"); err != nil {
			t.Fatal(err)
		}
		secret := store.RandomToken()
		m, err := st.CreateMachine("m", secret, "/h")
		if err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- srv.serve(context.Background(), ln) }()
		addr := ln.Addr().String()
		resp := postJSON(t, "http://"+addr+"/api/login", map[string]string{"password": "hunter22"}, nil)
		var ck *http.Cookie
		for _, c := range resp.Cookies() {
			if c.Name == cookieName {
				ck = c
			}
		}
		if ck == nil {
			t.Fatalf("login = %d", resp.StatusCode)
		}
		dctx, dcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dcancel()
		client, _, err := websocket.Dial(dctx, "ws://"+addr+"/ws/client", &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {ck.Name + "=" + ck.Value}}})
		if err != nil {
			t.Fatal(err)
		}
		readFrames(t, client)
		agentConn, _, err := websocket.Dial(dctx, "ws://"+addr+"/ws/agent", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
		if err != nil {
			t.Fatal(err)
		}
		agentConn.SetReadLimit(wire.MaxFrame)
		ac := &agentClient{conn: agentConn, t: t}
		ac.send(wire.Hello{T: wire.THello, Proto: wire.Proto, MachineID: m.ID})
		if kind, _ := ac.recv(5 * time.Second); kind != wire.TWelcome {
			t.Fatalf("应先收到 welcome, 得到 %s", kind)
		}
		readFrames(t, agentConn) // 关停时要读到关闭帧并回应，关闭握手才能完成
		waitFor(t, "两个 handler 在跑", func() bool { return srv.ws.count() == 2 })
		ln.Close() // 监听器出错：Serve 以非 ErrServerClosed 的错误结束，没有谁取消 ctx
		select {
		case err := <-done:
			if !errors.Is(err, net.ErrClosed) {
				t.Fatalf("stuck=%v: 应交上 Serve 的错误: %v", stuck, err)
			}
			if errors.Is(err, ErrShutdownTimeout) != stuck {
				t.Fatalf("stuck=%v: 只有等不到 handler 退出时才带 ErrShutdownTimeout: %v", stuck, err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("stuck=%v: Serve 结束后 Run 未返回", stuck)
		}
		if n, live := exited.Load(), srv.ws.count(); !stuck && (n != 2 || live != 0) {
			t.Fatalf("Run 返回时 handler 应都已退出: 已退出 %d, 仍在跑 %d", n, live)
		}
		letGo()
		waitFor(t, "handler 退出", func() bool { return srv.ws.count() == 0 })
	}
}
