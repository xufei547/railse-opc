package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"gopkg.in/yaml.v3"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/webui"
	"github.com/railse/railse-opc/internal/hub/wire"
	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
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

	var body struct {
		Pending []map[string]any `json:"pending"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &body)
	inbox := body.Pending
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
		{"POST", "/api/requests/r1/propose", "application/json", `{"action":"deny"}`},
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
	var inbox struct {
		Pending []map[string]any `json:"pending"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
	if len(inbox.Pending) != 259 {
		t.Fatalf("收件箱应列出全部 pending, 得到 %d", len(inbox.Pending))
	}
	if resp := doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录应 401, 得到 %d", resp.StatusCode)
	}
}

// decide 接口的错误码：坏 JSON、坏 action、备注超过 1000 字符 400；不存在 404；已过期 409。
// 终审 I4：请求视图带派生字段 has_redacted（载荷任一字段含脱敏占位符时为 true，MCP 参数里存库时转义的写法也算），
// /api/inbox、/api/requests、/api/requests/{id}、decide 的返回与 /ws/client 推送都带：网页据此醒目告警，展示的不是原文。
func TestRequestViewHasRedacted(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("h1", "s1", "t1", "curl -u alice:S3cret https://x")) // hub 兜底脱敏后含占位符
	c.send(pendingFrame("h2", "s2", "t2", "git push origin main"))
	if f := waitBroadcast(t, sub, "request_new", "h1", nil); f["has_redacted"] != true {
		t.Fatalf("h1 的推送应 has_redacted: %+v", f)
	}
	if f := waitBroadcast(t, sub, "request_new", "h2", nil); f["has_redacted"] != false {
		t.Fatalf("h2 的推送不应 has_redacted: %+v", f)
	}
	insertReq(t, h.st, store.Request{ID: "h3", MachineID: id, State: "pending",
		Payload: policy.Payload{Type: policy.PayloadMCP, Server: "db", Tool: "q", Args: json.RawMessage(`{"password":"<redacted>","sql":"select 1"}`)}})
	insertReq(t, h.st, store.Request{ID: "h4", MachineID: id, State: "pending",
		Payload: policy.Payload{Type: policy.PayloadNetwork, URL: "https://x.example.com/?token=<redacted>&a=1"}})
	insertReq(t, h.st, store.Request{ID: "h5", MachineID: id, State: "pending",
		Payload: policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/notes.txt", DiffPreview: "redacted 这个词本身不算"}})
	want := map[string]bool{"h1": true, "h2": false, "h3": true, "h4": true, "h5": false}
	var inbox struct {
		Pending []map[string]any `json:"pending"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
	var requests []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/requests?limit=500", nil, "", ck), &requests)
	for path, list := range map[string][]map[string]any{"/api/inbox": inbox.Pending, "/api/requests": requests} {
		if len(list) != len(want) {
			t.Fatalf("%s: %d 条", path, len(list))
		}
		for _, r := range list {
			if v, ok := r["has_redacted"].(bool); !ok || v != want[r["id"].(string)] {
				t.Fatalf("%s 里 %s 的 has_redacted 应为 %v: %+v", path, r["id"], want[r["id"].(string)], r)
			}
		}
	}
	var detail struct {
		Request map[string]any `json:"request"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/requests/h3", nil, "", ck), &detail)
	if detail.Request["has_redacted"] != true {
		t.Fatalf("详情应 has_redacted: %+v", detail.Request)
	}
	var decided map[string]any
	decodeBody(t, doReq(t, "POST", h.http.URL+"/api/requests/h1/decide", decideBody(t, "deny", false, ""), "application/json", ck), &decided)
	if decided["has_redacted"] != true || decided["state"] != "denied" {
		t.Fatalf("decide 的返回应 has_redacted: %+v", decided)
	}
	if f := waitBroadcast(t, sub, "request_updated", "h1", nil); f["has_redacted"] != true {
		t.Fatalf("决定后的推送应 has_redacted: %+v", f)
	}
}

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
		{"POST", "/api/requests/r1/propose", "application/json", `{"action":"deny"}`},
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
		// 预览也如实说放不下，免得用户看过草案、点保存才被拒
		if _, err := h.srv.propose(tc.id, tc.action, ""); !errors.Is(err, errLearn) || !strings.Contains(err.Error(), "上限") {
			t.Fatalf("%s: propose 放不下应 errLearn（说明接近上限）, 得到 %v", tc.action, err)
		}
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

// 终审 M11：普通 HTTP 请求没在关停时限内处理完（hs.Shutdown 超时、强制断开）也算没收尾干净：Run 返回 ErrShutdownTimeout，
// 调用方据此不关库（那个处理还可能碰存储）。
func TestRunReportsHTTPShutdownTimeout(t *testing.T) {
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
	release, entered := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	letGo := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(letGo)
	srv.mux.HandleFunc("GET /test/slow", func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release // 不看请求的 ctx：模拟处理得慢的普通请求
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	const prefix = "hub 监听 "
	waitFor(t, "Run 开始监听", func() bool { return strings.Contains(logs.String(), prefix) })
	line := logs.String()
	addr := strings.TrimSpace(strings.SplitN(line[strings.Index(line, prefix)+len(prefix):], "\n", 2)[0])
	go func() {
		if resp, err := http.Get("http://" + addr + "/test/slow"); err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("慢请求没有开始处理")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, ErrShutdownTimeout) {
			t.Fatalf("普通请求没处理完时 Run 应返回 ErrShutdownTimeout，得到 %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 Run 未返回")
	}
	if !strings.Contains(logs.String(), "强制断开") {
		t.Fatalf("应记日志: %s", logs.String())
	}
}

// 终审复审：M11 只把“还有处理在跑”算作没收尾干净。客户端（浏览器的预连接、HTTP 客户端多拨的连接）开着却没发请求的连接
// 会让 hs.Shutdown 等到截止（net/http 对没发过请求的新连接要等 5 秒才当空闲），但没有处理在跑、不会碰存储，Run 应返回 nil。
func TestRunIgnoresIdleNewConnections(t *testing.T) {
	old := shutdownTimeout
	shutdownTimeout = 200 * time.Millisecond
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	const prefix = "hub 监听 "
	waitFor(t, "Run 开始监听", func() bool { return strings.Contains(logs.String(), prefix) })
	line := logs.String()
	addr := strings.TrimSpace(strings.SplitN(line[strings.Index(line, prefix)+len(prefix):], "\n", 2)[0])
	conn, err := net.Dial("tcp", addr) // 只连不发请求
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// 一次完整的请求，确认服务在跑、上面的连接已被接受
	if resp := doReq(t, "GET", "http://"+addr+"/api/status", nil, "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("没有处理在跑时 Run 应返回 nil，得到 %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("取消后 Run 未返回")
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

// 修复轮 1：监听失败（端口被占等）时 Run 立即返回满足 ErrListen 的错误，什么都没开始跑，调用方可以照常关库。
func TestRunReportsListenFailure(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
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
	cfg.Listen = busy.Addr().String()
	var logs syncBuf
	srv, err := New(cfg, st, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	err = srv.Run(context.Background())
	if !errors.Is(err, ErrListen) || errors.Is(err, ErrShutdownTimeout) || !strings.Contains(err.Error(), cfg.Listen) {
		t.Fatalf("端口被占时应返回 ErrListen 并带上地址: %v", err)
	}
}

// 收件箱是 {pending, questions}：questions 是最近 24 小时还在等回答的 question，同一会话之后有了新事件（这里是 done）就不再列出。
func TestInboxQuestions(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s3", "t1", "git push origin main"))
	sess := func(cli string) proto.Session { return proto.Session{Tool: "claude", CLISessionID: cli, Cwd: "/p"} }
	c.send(auditFrame(t,
		agent.AuditEntry{TS: time.Now().Add(-25 * time.Hour), Kind: "question", Session: sess("s4"), State: "recorded", Message: "太旧，不列"},
		agent.AuditEntry{TS: time.Now(), Kind: "question", Session: sess("s1"), State: "recorded", Message: "要不要删掉旧分支？"},
		agent.AuditEntry{TS: time.Now(), Kind: "question", Session: sess("s2"), State: "recorded", Message: "用哪个数据库？"},
	))
	var inbox struct {
		Pending   []map[string]any `json:"pending"`
		Questions []map[string]any `json:"questions"`
	}
	waitFor(t, "两条问题与一条审批进收件箱", func() bool {
		decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
		return len(inbox.Questions) == 2 && len(inbox.Pending) == 1
	})
	q := inbox.Questions[0]
	if q["kind"] != "question" || q["state"] != "recorded" || q["title"] == "" || q["machine_name"] != "mac" || inbox.Pending[0]["id"] != "r1" {
		t.Fatalf("inbox = %+v", inbox)
	}
	c.send(auditFrame(t, agent.AuditEntry{TS: time.Now().Add(time.Second), Kind: "done", Session: sess("s1"), State: "recorded", Message: "DONE"}))
	waitFor(t, "s1 的问题被 done 收掉", func() bool {
		decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
		return len(inbox.Questions) == 1 && inbox.Questions[0]["cli_session_id"] == "s2"
	})
	// 空收件箱两个字段都是数组，不是 null
	h2 := newTestHub(t)
	resp := doReq(t, "GET", h2.http.URL+"/api/inbox", nil, "", loggedIn(t, h2))
	raw, _ := io.ReadAll(resp.Body)
	if got := strings.TrimSpace(string(raw)); got != `{"pending":[],"questions":[]}` {
		t.Fatalf("空收件箱 = %s", got)
	}
}

// apiPost 以登录 cookie 向 hub 的 path 发 JSON 的 POST。
func apiPost(t *testing.T, h *testHub, ck *http.Cookie, path string, body any) *http.Response {
	t.Helper()
	return postJSON(t, h.http.URL+path, body, ck)
}

// proposeDraft 是 propose 的成功返回。
type proposeDraft struct {
	RuleID  string `json:"rule_id"`
	Scope   string `json:"scope"`
	YAML    string `json:"yaml"`
	Summary struct {
		Action  string `json:"action"`
		Payload string `json:"payload"`
		Anchor  string `json:"anchor"`
	} `json:"summary"`
}

// propose 只算草案（项目或全局），decide 按 scope 入库。
func TestProposeAndDecideWithScope(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	c.recv(5 * time.Second)
	fr := pendingFrame("r1", "s1", "t1", "curl https://api.example.com/v1/x")
	fr.Risk = "network"
	c.send(fr)
	waitFor(t, "r1 登记", func() bool { _, err := h.st.GetRequest("r1"); return err == nil })
	var draft proposeDraft
	resp := apiPost(t, h, ck, "/api/requests/r1/propose", map[string]string{"action": "allow"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("propose: %d", resp.StatusCode)
	}
	decodeBody(t, resp, &draft)
	plain := strings.ReplaceAll(draft.YAML, `\.`, ".") // 整条命令锚定的正则里点号被转义
	if draft.Scope != "project" || !strings.Contains(plain, "api.example.com") || !strings.Contains(draft.YAML, "project: /p") ||
		draft.Summary.Action != "allow" || draft.Summary.Payload != "shell" || draft.Summary.Anchor == "" || draft.RuleID == "" {
		t.Fatalf("草案: %+v", draft)
	}
	var rule policy.Rule
	if err := yaml.Unmarshal([]byte(draft.YAML), &rule); err != nil || rule.ID != draft.RuleID || rule.Source != policy.SourceLearned {
		t.Fatalf("草案 YAML 应是一条规则: %+v %v", rule, err)
	}
	resp = apiPost(t, h, ck, "/api/requests/r1/propose", map[string]string{"action": "allow", "scope": "global"})
	draft = proposeDraft{}
	decodeBody(t, resp, &draft)
	if resp.StatusCode != http.StatusOK || draft.Scope != "global" || strings.Contains(draft.YAML, "project:") || !strings.Contains(draft.YAML, "scope: global") {
		t.Fatalf("全局草案: %d %+v", resp.StatusCode, draft)
	}
	// 预览不入库
	var rules []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/rules", nil, "", ck), &rules)
	if len(rules) != 2 {
		t.Fatalf("预览不应入库，规则数 %d", len(rules))
	}
	if resp := apiPost(t, h, ck, "/api/requests/r1/propose", map[string]string{"action": "maybe"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏 action: %d", resp.StatusCode)
	}
	if resp := apiPost(t, h, ck, "/api/requests/r1/propose", map[string]string{"action": "allow", "scope": "machine"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏 scope: %d", resp.StatusCode)
	}
	if resp := doReq(t, "POST", h.http.URL+"/api/requests/r1/propose", []byte(`{"action":`), "application/json", ck); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏 JSON: %d", resp.StatusCode)
	}
	if resp := apiPost(t, h, ck, "/api/requests/nope/propose", map[string]string{"action": "allow"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("没有的请求: %d", resp.StatusCode)
	}
	// 记成全局
	resp = apiPost(t, h, ck, "/api/requests/r1/decide", map[string]any{"action": "allow", "remember": true, "scope": "global"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decide: %d", resp.StatusCode)
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/rules", nil, "", ck), &rules)
	if len(rules) != 3 {
		t.Fatalf("记住后规则数 %d", len(rules))
	}
	var learned map[string]any
	for _, r := range rules {
		if r["source"] == "learned" {
			learned = r
		}
	}
	if learned == nil || learned["id"] != draft.RuleID || strings.Contains(learned["yaml"].(string), "project:") {
		t.Fatalf("学到的规则应为全局、id 同草案: %+v", learned)
	}
	// 全局规则在别的项目里也放行同一条命令
	d := evalWith(t, h.st, fr.Payload, policy.Context{ProjectRoot: "/elsewhere", HomeDir: "/home/u", MachineID: id})
	if d.Action != policy.ActionAllow || d.RuleID != draft.RuleID {
		t.Fatalf("全局规则应在别的项目命中: %+v", d)
	}
	// 已处理的请求不能再预览
	var e map[string]string
	resp = apiPost(t, h, ck, "/api/requests/r1/propose", map[string]string{"action": "allow"})
	decodeBody(t, resp, &e)
	if resp.StatusCode != http.StatusConflict || e["error"] != "请求已经处理过" {
		t.Fatalf("已处理: %d %v", resp.StatusCode, e)
	}
}

// propose 与 decide 的前置条件一致：记不住 422（文案以“不能记住：”开头）；已处理、已过期、机器已吊销 409（文案同 decide），
// 而且 propose 不像 decide 那样顺手把过期、吊销的请求改掉状态。
func TestProposeRefusals(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	c.recv(5 * time.Second)
	fr := pendingFrame("r1", "s1", "t1", "git push origin main")
	fr.Risk = "outward"
	c.send(fr)
	noRoot := pendingFrame("r2", "s1", "t2", "curl https://api.example.com/v1")
	noRoot.Risk, noRoot.ProjectRoot = "network", ""
	c.send(noRoot)
	waitFor(t, "登记", func() bool { _, err := h.st.GetRequest("r2"); return err == nil })
	m2, err := h.st.CreateMachine("old", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	past := store.FormatTime(time.Now().Add(-time.Minute))
	insertReq(t, h.st, store.Request{ID: "exp", MachineID: id, State: "pending", ProjectRoot: "/p", ExpiresAt: &past})
	insertReq(t, h.st, store.Request{ID: "rev", MachineID: m2.ID, State: "pending", ProjectRoot: "/p"})
	if err := h.st.RevokeMachine(m2.ID); err != nil {
		t.Fatal(err)
	}
	insertReq(t, h.st, store.Request{ID: "done", MachineID: id, State: "denied", ProjectRoot: "/p"})
	insertReq(t, h.st, store.Request{ID: "sec", MachineID: id, State: "pending", ProjectRoot: "/p",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "npm login --password <redacted>", Cwd: "/p"}})
	insertReq(t, h.st, store.Request{ID: "wh", MachineID: webhookMachineID, Kind: "webhook", State: "pending",
		Payload: policy.Payload{Type: policy.PayloadType("other")}})
	for _, tc := range []struct {
		name, id, action, scope string
		code                    int
		msg                     string // 422 时要以“不能记住：”开头并含它；409 时要等于它
	}{
		{"outward 不能记成放行", "r1", "allow", "", http.StatusUnprocessableEntity, "不能记住"},
		{"没有项目根不能显式记成项目范围", "r2", "deny", "project", http.StatusUnprocessableEntity, "不能记住：没有项目根，只能记成全局"},
		{"含凭据", "sec", "deny", "", http.StatusUnprocessableEntity, "凭据"},
		{"webhook", "wh", "deny", "", http.StatusUnprocessableEntity, "webhook"},
		{"已过期", "exp", "deny", "", http.StatusConflict, "请求已过期"},
		{"机器已吊销", "rev", "deny", "", http.StatusConflict, "机器已吊销，请求已取消"},
		{"已处理", "done", "deny", "", http.StatusConflict, "请求已经处理过"},
	} {
		body := map[string]string{"action": tc.action}
		if tc.scope != "" {
			body["scope"] = tc.scope
		}
		resp := apiPost(t, h, ck, "/api/requests/"+tc.id+"/propose", body)
		var e map[string]string
		decodeBody(t, resp, &e)
		ok := resp.StatusCode == tc.code
		if tc.code == http.StatusUnprocessableEntity {
			ok = ok && strings.HasPrefix(e["error"], "不能记住：") && strings.Contains(e["error"], tc.msg)
		} else {
			ok = ok && e["error"] == tc.msg
		}
		if !ok {
			t.Fatalf("%s: %d %v", tc.name, resp.StatusCode, e)
		}
	}
	for _, id := range []string{"exp", "rev"} {
		if r := getReq(t, h.st, id); r.State != "pending" {
			t.Fatalf("propose 不应改 %s 的状态: %+v", id, r)
		}
	}
	// 没有项目根：显式 global 与缺省都记成全局
	for _, scope := range []string{"global", ""} {
		body := map[string]string{"action": "deny"}
		if scope != "" {
			body["scope"] = scope
		}
		resp := apiPost(t, h, ck, "/api/requests/r2/propose", body)
		var draft proposeDraft
		decodeBody(t, resp, &draft)
		if resp.StatusCode != http.StatusOK || draft.Scope != "global" || !strings.Contains(draft.YAML, "scope: global") ||
			draft.Summary.Action != "deny" || draft.Summary.Anchor != "^"+regexp.QuoteMeta("curl https://api.example.com/v1")+"$" {
			t.Fatalf("没有项目根、scope %q: %d %+v", scope, resp.StatusCode, draft)
		}
	}
}

// propose 什么都不写：不入规则、不加审计、不改请求、不下发、不广播，无论成功、记不住还是 409。
func TestProposeWritesNothing(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	c := newFakeConn(m.ID, 16)
	h.srv.agents.register(c)
	past := store.FormatTime(time.Now().Add(-time.Minute))
	insertReq(t, h.st, store.Request{ID: "ok", MachineID: m.ID, State: "pending", ProjectRoot: "/p",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "npm run build", Cwd: "/p"}})
	insertReq(t, h.st, store.Request{ID: "exp", MachineID: m.ID, State: "pending", ProjectRoot: "/p", ExpiresAt: &past})
	insertReq(t, h.st, store.Request{ID: "out", MachineID: m.ID, State: "pending", ProjectRoot: "/p",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push origin main", Cwd: "/p"}})
	snapshot := func() string {
		t.Helper()
		rows, _ := ruleRows(t, h.st)
		version, err := h.st.RulesVersion()
		if err != nil {
			t.Fatal(err)
		}
		events, err := h.st.ListAudit(0, "")
		if err != nil {
			t.Fatal(err)
		}
		var reqs []store.Request
		for _, id := range []string{"ok", "exp", "out"} {
			reqs = append(reqs, getReq(t, h.st, id))
		}
		return string(mustJSON(t, map[string]any{"rules": rows, "version": version, "audit": events, "requests": reqs}))
	}
	before := snapshot()
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	for _, tc := range []struct {
		id, action, scope string
		code              int
	}{
		{"ok", "allow", "", http.StatusOK},
		{"ok", "allow", "global", http.StatusOK},
		{"ok", "deny", "project", http.StatusOK},
		{"exp", "deny", "", http.StatusConflict},
		{"out", "allow", "", http.StatusUnprocessableEntity},
	} {
		if resp := apiPost(t, h, ck, "/api/requests/"+tc.id+"/propose", map[string]string{"action": tc.action, "scope": tc.scope}); resp.StatusCode != tc.code {
			t.Fatalf("%s %s %q: 应 %d, 得到 %d", tc.id, tc.action, tc.scope, tc.code, resp.StatusCode)
		}
	}
	if after := snapshot(); after != before {
		t.Fatalf("propose 不应写库:\n前 %s\n后 %s", before, after)
	}
	if len(sub) != 0 || len(c.out) != 0 {
		t.Fatalf("propose 不应广播或下发: 网页 %d 帧，守护进程 %d 帧", len(sub), len(c.out))
	}
}

// decide 的 scope：缺省时有项目根记成项目、没有记成全局（与旧行为一致）；显式 project 而没有项目根 422；
// 坏 scope 一律 400；不记住时 scope 不起作用。
func TestDecideScope(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("m", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	deploy := func(root string) policy.Payload {
		return policy.Payload{Type: policy.PayloadShell, Command: "make deploy", Cwd: root}
	}
	for _, r := range []struct{ id, root string }{{"rooted", "/p"}, {"bare", ""}, {"bare2", ""}, {"rooted2", "/p"}, {"rooted3", "/p"}} {
		insertReq(t, h.st, store.Request{ID: r.id, MachineID: m.ID, State: "pending", ProjectRoot: r.root, Payload: deploy(r.root)})
	}
	decide := func(id string, body map[string]any) (int, map[string]any) {
		t.Helper()
		resp := apiPost(t, h, ck, "/api/requests/"+id+"/decide", body)
		var out map[string]any
		decodeBody(t, resp, &out)
		return resp.StatusCode, out
	}
	lastRule := func() policy.Rule {
		t.Helper()
		_, rules := ruleRows(t, h.st)
		return rules[len(rules)-1]
	}
	if code, out := decide("bare", map[string]any{"action": "deny", "remember": true, "scope": "project"}); code != http.StatusUnprocessableEntity ||
		out["error"] != "不能记住：没有项目根，只能记成全局" {
		t.Fatalf("没有项目根显式记成项目: %d %v", code, out)
	}
	for _, body := range []map[string]any{
		{"action": "deny", "remember": true, "scope": "machine"},
		{"action": "deny", "remember": false, "scope": "Global"},
	} {
		if code, out := decide("bare", body); code != http.StatusBadRequest || out["error"] != "scope 只能是 project 或 global" {
			t.Fatalf("坏 scope %v: %d %v", body, code, out)
		}
	}
	if r := getReq(t, h.st, "bare"); r.State != "pending" {
		t.Fatalf("出错的决定不应改状态: %+v", r)
	}
	if rows, _ := ruleRows(t, h.st); len(rows) != 2 {
		t.Fatalf("出错的决定不应生成规则: %+v", rows)
	}
	// 不记住时 scope 不起作用：没有项目根写 project 也照常决定
	if code, out := decide("bare2", map[string]any{"action": "deny", "scope": "project"}); code != http.StatusOK || out["rule_id"] != "" {
		t.Fatalf("不记住时 scope 不起作用: %d %v", code, out)
	}
	// 缺省：没有项目根记成全局（旧客户端不带 scope）
	if code, out := decide("bare", map[string]any{"action": "deny", "remember": true}); code != http.StatusOK || !lastRule().Scope.IsGlobal() {
		t.Fatalf("缺省、没有项目根应记成全局: %d %v %+v", code, out, lastRule())
	}
	// 缺省：有项目根记成项目
	if code, _ := decide("rooted", map[string]any{"action": "deny", "remember": true}); code != http.StatusOK || lastRule().Scope.Project != "/p" {
		t.Fatalf("缺省、有项目根应记成项目: %d %+v", code, lastRule())
	}
	if code, _ := decide("rooted2", map[string]any{"action": "deny", "remember": true, "scope": "global"}); code != http.StatusOK || !lastRule().Scope.IsGlobal() {
		t.Fatalf("显式 global: %d %+v", code, lastRule())
	}
	if code, _ := decide("rooted3", map[string]any{"action": "deny", "remember": true, "scope": "project"}); code != http.StatusOK || lastRule().Scope.Project != "/p" {
		t.Fatalf("显式 project: %d %+v", code, lastRule())
	}
	// 记成全局的拒绝在任何项目里都拒这条命令
	d := evalWith(t, h.st, deploy("/q"), policy.Context{ProjectRoot: "/q", HomeDir: "/home/u", MachineID: m.ID})
	if d.Action != policy.ActionDeny {
		t.Fatalf("全局拒绝应在别的项目命中: %+v", d)
	}
}

// ruleAnchor 取草案里起匹配作用的锚点。
func TestRuleAnchor(t *testing.T) {
	for _, tc := range []struct {
		m    policy.Match
		want string
	}{
		{policy.Match{CommandPrefix: "npm run build", CommandRegex: `^npm run build$`}, "npm run build"},
		{policy.Match{CommandRegex: `^make deploy$`}, `^make deploy$`},
		{policy.Match{PathGlob: "/p/src/**"}, "/p/src/**"},
		{policy.Match{MCPServer: "github", MCPTool: "create_issue"}, "github/create_issue"},
		{policy.Match{MCPServer: "github"}, "github"},
		{policy.Match{Domain: "api.example.com"}, "api.example.com"},
		{policy.Match{}, ""},
	} {
		if got := ruleAnchor(policy.Rule{Match: tc.m}); got != tc.want {
			t.Fatalf("ruleAnchor(%+v) = %q, 应 %q", tc.m, got, tc.want)
		}
	}
}

func TestRootServesWebUIOrNotBuilt(t *testing.T) {
	h := newTestHub(t)
	resp := doReq(t, "GET", h.http.URL+"/r/abc", nil, "", nil)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("根路径: %d", resp.StatusCode)
	}
	if resp := doReq(t, "GET", h.http.URL+"/api/nope", nil, "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知接口应 404: %d", resp.StatusCode)
	}
}

// 挂上网页之后：网页路径回页面（单页回退）；/api/、/ws/ 下没有的接口回 404 JSON；路径对、方法不对的接口仍回 405 并带 Allow；
// 已有接口照常处理。
func TestWebUIMountKeepsAPIRouting(t *testing.T) {
	built := fstest.MapFS{
		"index.html": {Data: []byte("<!doctype html><title>Railse</title>")},
		"sw.js":      {Data: []byte("self.addEventListener('push',()=>{})")},
	}
	h := newTestHubWith(t, nil, func(s *Server) { s.web = webui.Handler(built) })

	for _, path := range []string{"/", "/r/abc", "/rules"} {
		resp := doReq(t, "GET", h.http.URL+path, nil, "", nil)
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<title>Railse</title>") {
			t.Fatalf("%s: %d %q", path, resp.StatusCode, body)
		}
		if resp.Header.Get("X-Frame-Options") != "DENY" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatalf("%s 安全头: %v", path, resp.Header)
		}
	}
	if resp := doReq(t, "GET", h.http.URL+"/sw.js", nil, "", nil); resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Fatalf("sw.js: %d %v", resp.StatusCode, resp.Header)
	}

	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/nope"}, {"POST", "/api/nope"}, {"GET", "/ws/nope"}, {"GET", "/api/requests/x/y/z"}, {"GET", "/api"},
	} {
		resp := doReq(t, tc.method, h.http.URL+tc.path, nil, "", nil)
		var e struct{ Error string }
		decodeBody(t, resp, &e)
		if resp.StatusCode != http.StatusNotFound || e.Error != "没有这个接口" {
			t.Fatalf("%s %s: %d %q", tc.method, tc.path, resp.StatusCode, e.Error)
		}
	}

	for _, tc := range []struct{ method, path, allow string }{
		{"GET", "/api/requests/x/decide", "POST"},
		{"DELETE", "/api/status", "GET, HEAD"},
		{"POST", "/ws/client", "GET, HEAD"},
		{"GET", "/api/login", "POST"},
		{"PUT", "/api/push/subscribe", "DELETE, POST"},
	} {
		resp := doReq(t, tc.method, h.http.URL+tc.path, nil, "", nil)
		if resp.StatusCode != http.StatusMethodNotAllowed || resp.Header.Get("Allow") != tc.allow {
			t.Fatalf("%s %s 应 405 且 Allow %q: %d %q", tc.method, tc.path, tc.allow, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}

	// 已有接口照常处理：没登录回 401；HEAD 走 GET 路由
	for _, method := range []string{"GET", "HEAD"} {
		if resp := doReq(t, method, h.http.URL+"/api/status", nil, "", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s /api/status 未登录应 401: %d", method, resp.StatusCode)
		}
	}
	ck := loggedIn(t, h)
	if resp := doReq(t, "GET", h.http.URL+"/api/status", nil, "", ck); resp.StatusCode != http.StatusOK {
		t.Fatalf("/api/status: %d", resp.StatusCode)
	}
}
