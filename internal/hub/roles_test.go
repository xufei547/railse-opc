package hub

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// roleAgent 接入一台机器，以 features 连上并在 hello 里带 roles，读掉 welcome。
func roleAgent(t *testing.T, h *testHub, name string, features []string, roles []proto.RoleSummary) (string, string, *agentClient, wire.Welcome) {
	t.Helper()
	id, secret := enrollMachine(t, h, name)
	c, w := roleRedial(t, h, id, secret, features, roles)
	return id, secret, c, w
}

func roleRedial(t *testing.T, h *testHub, id, secret string, features []string, roles []proto.RoleSummary) (*agentClient, wire.Welcome) {
	t.Helper()
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id, HomeDir: "/home/u", Features: features, Roles: roles})
	t.Cleanup(c.close)
	var w wire.Welcome
	c.expectFrame(wire.TWelcome, &w)
	c.expectNothing() // hello 已处理完（角色表已写）
	return c, w
}

// machineRoles 列某台机器的角色名，按文件里的先后。
func machineRoles(t *testing.T, h *testHub, id string) []string {
	t.Helper()
	all, err := h.st.ListMachineRoles()
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range all {
		if r.MachineID == id {
			out = append(out, r.Name)
		}
	}
	return out
}

var twoRoles = []proto.RoleSummary{
	{Name: "开发", Note: "写代码", Ceiling: "network", ForbidCount: 1, TrustMaxMins: 1440, Dirs: []string{"~/RailseWork"}},
	{Name: "写作", Note: "只改文档", Ceiling: "write_in_project", ForbidCount: 2, TrustMaxMins: 120},
}

// hub 在 welcome 与 /api/version 里声明 role。
func TestHubDeclaresRoleFeature(t *testing.T) {
	h := newTestHub(t)
	_, _, _, w := roleAgent(t, h, "mac", []string{wire.FeatureRole}, nil)
	if !w.Has(wire.FeatureRole) {
		t.Fatalf("welcome 应声明 role: %v", w.Features)
	}
	var v struct {
		Features []string `json:"features"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/version", nil, "", nil), &v)
	if !slices.Contains(v.Features, wire.FeatureRole) {
		t.Fatalf("/api/version 的 features = %v", v.Features)
	}
}

// 声明了 role 的 hello 带的角色存进角色表；roles 帧整份替换；不声明 role 重连时清空。
func TestRolesFromHelloAndFrame(t *testing.T) {
	h := newTestHub(t)
	id, secret, c, _ := roleAgent(t, h, "mac", []string{wire.FeatureRole}, twoRoles)
	if got := machineRoles(t, h, id); !slices.Equal(got, []string{"开发", "写作"}) {
		t.Fatalf("hello 后的角色表: %v", got)
	}
	c.send(wire.Roles{T: "roles", Roles: []proto.RoleSummary{{Name: "运维", Ceiling: "destructive"}}})
	c.expectNothing()
	if got := machineRoles(t, h, id); !slices.Equal(got, []string{"运维"}) {
		t.Fatalf("roles 帧后的角色表: %v", got)
	}
	c.send(wire.Roles{T: "roles", Roles: nil})
	c.expectNothing()
	if got := machineRoles(t, h, id); len(got) != 0 {
		t.Fatalf("空的 roles 帧应清空: %v", got)
	}
	c.send(wire.Roles{T: "roles", Roles: twoRoles})
	c.expectNothing()
	c.close()
	// 换成不声明 role 的守护进程（降级）：角色表清空
	roleRedial(t, h, id, secret, nil, twoRoles)
	if got := machineRoles(t, h, id); len(got) != 0 {
		t.Fatalf("不声明 role 时应清空角色表: %v", got)
	}
}

// 没声明 role 的连接发来的 roles 帧不收。
func TestRolesFrameIgnoredWithoutFeature(t *testing.T) {
	h := newTestHub(t)
	id, _, c, _ := roleAgent(t, h, "mac", []string{wire.FeatureTrust}, nil)
	c.send(wire.Roles{T: "roles", Roles: twoRoles})
	c.expectNothing()
	if got := machineRoles(t, h, id); len(got) != 0 {
		t.Fatalf("没声明 role 的 roles 帧不应入库: %v", got)
	}
	if !strings.Contains(h.logs.String(), "roles") {
		t.Fatalf("应记一行日志: %s", h.logs.String())
	}
}

// cleanRoles 给守护进程报来的角色摘要兜底。
func TestCleanRoles(t *testing.T) {
	long := strings.Repeat("长", 33)
	in := []proto.RoleSummary{
		{Name: ""},        // 空名丢
		{Name: "a b"},     // 含空白丢
		{Name: "a\u0007"}, // 控制字符丢
		{Name: "a\u200b"}, // 格式字符丢
		{Name: long},      // 超过 32 个字符丢
		{Name: strings.Repeat("长", 32), ForbidCount: -3, TrustMaxMins: -9}, // 恰好 32 个字符留；负数规整
		{Name: "开发", Note: "token=sk-ant-api03-" + strings.Repeat("x", 40) + " " + strings.Repeat("备", 200), Ceiling: strings.Repeat("c", 300),
			Dirs: append([]string{"", "~/a", strings.Repeat("/x", 3000)}, make40("~/d")...)},
		{Name: "开发", Note: "重名的丢"},
		{Name: "运维", TrustMaxMins: -1},
	}
	got := cleanRoles(in)
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	if !slices.Equal(names, []string{strings.Repeat("长", 32), "开发", "运维"}) {
		t.Fatalf("留下的角色: %v", names)
	}
	if got[0].ForbidCount != 0 || got[0].TrustMaxMins != 0 {
		t.Fatalf("负数应规整: %+v", got[0])
	}
	dev := got[1]
	if strings.Contains(dev.Note, "sk-ant") || len([]rune(dev.Note)) > maxRoleNoteLen {
		t.Fatalf("备注应脱敏、截断: %q", dev.Note)
	}
	if len(dev.Ceiling) > maxFieldLen {
		t.Fatalf("上限应截断: %d", len(dev.Ceiling))
	}
	if len(dev.Dirs) != maxRoleDirs || dev.Dirs[0] != "~/a" {
		t.Fatalf("目录: 空的、超长的丢，至多 %d 条: %d %v", maxRoleDirs, len(dev.Dirs), dev.Dirs[:2])
	}
	if got[2].TrustMaxMins != -1 {
		t.Fatalf("-1（不允许放行）应保留: %+v", got[2])
	}
	many := make([]proto.RoleSummary, 100)
	for i := range many {
		many[i].Name = "r" + strings.Repeat("x", i%30) + string(rune('a'+i%26)) + string(rune('A'+i/26))
	}
	if n := len(cleanRoles(many)); n != maxRoles {
		t.Fatalf("至多 %d 个角色: %d", maxRoles, n)
	}
}

func make40(prefix string) []string {
	out := make([]string, 40)
	for i := range out {
		out[i] = prefix + strings.Repeat("x", i)
	}
	return out
}

// 声明了 role 的守护进程送来的待决请求带角色与越界标记：视图里有 role、beyond_role，越界的 can_trust 为假。
func TestPendingCarriesRole(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	_, _, c, _ := roleAgent(t, h, "mac", []string{wire.FeatureTrust, wire.FeatureRole}, twoRoles)
	p := pendingFrame("r1", "s1", "t1", "git push origin main")
	p.Session.Role, p.BeyondRole = "写作", true
	c.send(p)
	p2 := pendingFrame("r2", "s2", "t1", "curl https://x.example")
	p2.Session.Role = "开发"
	c.send(p2)
	waitFor(t, "两条登记", func() bool { n, _ := h.st.CountPending(); return n == 2 })
	v := requestView1(t, h, ck, "r1")
	if v["role"] != "写作" || v["beyond_role"] != true || v["can_trust"] != false {
		t.Fatalf("越界请求的视图: role=%v beyond=%v can_trust=%v", v["role"], v["beyond_role"], v["can_trust"])
	}
	v = requestView1(t, h, ck, "r2")
	if v["role"] != "开发" || v["beyond_role"] != false || v["can_trust"] != true {
		t.Fatalf("普通请求的视图: role=%v beyond=%v can_trust=%v", v["role"], v["beyond_role"], v["can_trust"])
	}
	r, _ := h.st.GetRequest("r1")
	if role, err := h.st.SessionRole(r.SessionID); err != nil || role != "写作" {
		t.Fatalf("会话角色 = %q, %v", role, err)
	}
}

// 没声明 role 的守护进程：会话里的角色名不认（未分配）；越界标记照收（它只会让 hub 少给记住与放行）。
func TestPendingRoleIgnoredWithoutFeature(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	_, _, c, _ := roleAgent(t, h, "mac", []string{wire.FeatureTrust}, twoRoles)
	p := pendingFrame("r1", "s1", "t1", "git push origin main")
	p.Session.Role, p.BeyondRole = "写作", true
	c.send(p)
	waitFor(t, "登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	v := requestView1(t, h, ck, "r1")
	if v["role"] != "" || v["beyond_role"] != true || v["can_trust"] != false {
		t.Fatalf("视图: role=%v beyond=%v can_trust=%v", v["role"], v["beyond_role"], v["can_trust"])
	}
}

// 不合规的角色名（含空白、超长）按未分配处理。
func TestPendingBadRoleNameUnassigned(t *testing.T) {
	h := newTestHub(t)
	_, _, c, _ := roleAgent(t, h, "mac", []string{wire.FeatureRole}, nil)
	p := pendingFrame("r1", "s1", "t1", "git push")
	p.Session.Role = "写 作"
	c.send(p)
	waitFor(t, "登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	if r, _ := h.st.GetRequest("r1"); r.Role != "" {
		t.Fatalf("不合规的角色名应为未分配: %q", r.Role)
	}
}

// 审计补的自动放行、自动拒绝行与 question 行带会话的角色。
func TestAuditRowsCarryRole(t *testing.T) {
	h := newTestHub(t)
	_, _, c, _ := roleAgent(t, h, "mac", []string{wire.FeatureRole}, twoRoles)
	now := time.Now().UTC()
	sess := proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p", Role: "开发"}
	pl := policy.Payload{Type: policy.PayloadShell, Command: "ls"}
	c.send(auditFrame(t,
		agent.AuditEntry{TS: now, RequestID: "a1", Kind: "permission", Session: sess, ToolName: "Bash", Payload: &pl, Risk: "read", State: "auto_allowed"},
		agent.AuditEntry{TS: now, RequestID: "a2", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s2", Role: "写作"}, ToolName: "Bash", Payload: &pl, Risk: "outward", State: "auto_denied", DecidedBy: "role:写作"},
		agent.AuditEntry{TS: now, Kind: "question", Session: sess, State: "recorded", Message: "要继续吗？"},
		// 越界的请求没来得及登记待决就在本机了结（过期）：补的历史行要带越界标记，总台的最近动作才认得出
		agent.AuditEntry{TS: now, RequestID: "a3", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s2", Role: "写作"}, ToolName: "Bash", Payload: &pl, Risk: "outward", State: "expired", BeyondRole: true},
	))
	c.expectNothing()
	if r, err := h.st.GetRequest("a3"); err != nil || !r.BeyondRole || r.Role != "写作" {
		t.Fatalf("a3 应带越界标记与角色: %+v %v", r, err)
	}
	if r, _ := h.st.GetRequest("a2"); r.BeyondRole {
		t.Fatal("a2 不是越界，不应带标记")
	}
	for id, want := range map[string]string{"a1": "开发", "a2": "写作"} {
		if r, err := h.st.GetRequest(id); err != nil || r.Role != want {
			t.Fatalf("%s 的角色 = %q, %v", id, r.Role, err)
		}
	}
	qs, err := h.st.ListRequests("recorded", 10)
	if err != nil || len(qs) != 1 || qs[0].Role != "开发" {
		t.Fatalf("question 行: %+v %v", qs, err)
	}
}

// 越界的请求：批准时带 remember 或 trust 回 400、请求仍待决、不学规则不建放行；propose allow 400；拒绝并记住照常；单纯批准照常下发。
func TestDecideBeyondRoleRefusesRememberAndTrust(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	_, _, c, _ := roleAgent(t, h, "mac", []string{wire.FeatureTrust, wire.FeatureRole}, twoRoles)
	for _, id := range []string{"r1", "r2"} {
		p := pendingFrame(id, "s-"+id, "t1", "git push origin "+id)
		p.Session.Role, p.BeyondRole = "写作", true
		c.send(p)
	}
	waitFor(t, "登记", func() bool { n, _ := h.st.CountPending(); return n == 2 })
	rulesBefore, _ := h.st.ListRules()
	for _, body := range []map[string]any{
		{"action": "allow", "remember": true},
		{"action": "allow", "trust": map[string]any{"scope": "session", "minutes": 30}},
		{"action": "allow", "remember": true, "trust": map[string]any{"scope": "project"}},
	} {
		resp := apiPost(t, h, ck, "/api/requests/r1/decide", body)
		if resp.StatusCode != 400 {
			t.Fatalf("%v 应 400: %d", body, resp.StatusCode)
		}
		if msg := errText(t, resp); !strings.Contains(msg, "超出角色的边界") {
			t.Fatalf("文案: %q", msg)
		}
	}
	if r, _ := h.st.GetRequest("r1"); r.State != "pending" {
		t.Fatalf("被拒后请求应仍待决: %s", r.State)
	}
	if list, _ := h.st.ListTrusts(""); len(list) != 0 {
		t.Fatalf("不应建放行: %+v", list)
	}
	if rules, _ := h.st.ListRules(); len(rules) != len(rulesBefore) {
		t.Fatalf("不应学规则: %d → %d", len(rulesBefore), len(rules))
	}
	if resp := apiPost(t, h, ck, "/api/requests/r1/propose", map[string]any{"action": "allow"}); resp.StatusCode != 400 {
		t.Fatalf("propose allow 应 400: %d", resp.StatusCode)
	}
	c.expectNothing()
	// 单纯批准：照常下发 allow
	if resp := apiPost(t, h, ck, "/api/requests/r1/decide", map[string]any{"action": "allow"}); resp.StatusCode != 200 {
		t.Fatalf("单纯批准应 200: %d %s", resp.StatusCode, errText(t, resp))
	}
	var d wire.Decision
	c.expectFrame(wire.TDecision, &d)
	if d.RequestID != "r1" || d.Action != "allow" {
		t.Fatalf("决定: %+v", d)
	}
	// 拒绝并记住：学一条拒绝规则只会更严，照常
	if resp := apiPost(t, h, ck, "/api/requests/r2/propose", map[string]any{"action": "deny"}); resp.StatusCode != 200 {
		t.Fatalf("propose deny 应 200: %d", resp.StatusCode)
	}
	if resp := apiPost(t, h, ck, "/api/requests/r2/decide", map[string]any{"action": "deny", "remember": true}); resp.StatusCode != 200 {
		t.Fatalf("拒绝并记住应 200: %d %s", resp.StatusCode, errText(t, resp))
	}
	if rules, _ := h.st.ListRules(); len(rules) != len(rulesBefore)+1 {
		t.Fatalf("应学到一条拒绝规则: %d → %d", len(rulesBefore), len(rules))
	}
}

// 同一机器快速重连：旧连接的 hello 或 roles 帧晚于新连接落库时不得覆盖新连接的角色表（只认当前连接）。
func TestStaleConnectionCannotOverwriteRoles(t *testing.T) {
	h := newTestHub(t)
	m, err := h.st.CreateMachine("mac", "sec", "")
	if err != nil {
		t.Fatal(err)
	}
	conn := func() *agentConn {
		c := newFakeConn(m.ID, 16)
		c.features = []string{wire.FeatureRole}
		return c
	}
	old, cur := conn(), conn()
	h.srv.agents.register(old)
	h.srv.agents.register(cur) // 新连接顶替旧连接
	h.srv.storeHelloRoles(cur, true, []proto.RoleSummary{{Name: "新"}})
	// 旧连接晚到的 hello 与 roles 帧：都丢弃
	h.srv.storeHelloRoles(old, true, []proto.RoleSummary{{Name: "旧"}})
	h.srv.onRoles(old, wire.Roles{T: tRoles, Roles: []proto.RoleSummary{{Name: "旧2"}}})
	h.srv.storeHelloRoles(old, false, nil) // 旧连接是老守护进程时的“清空”也不算
	if got := machineRoles(t, h, m.ID); !slices.Equal(got, []string{"新"}) {
		t.Fatalf("旧连接覆盖了新连接的角色表: %v", got)
	}
	if !strings.Contains(h.logs.String(), "不是这台机器的当前连接") {
		t.Fatalf("应记日志: %s", h.logs.String())
	}

	// 并发交错：旧连接不停地发，新连接最后发的一份必须留下
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			h.srv.onRoles(old, wire.Roles{T: tRoles, Roles: []proto.RoleSummary{{Name: fmt.Sprintf("旧%d", i)}}})
		}()
		go func() {
			defer wg.Done()
			h.srv.onRoles(cur, wire.Roles{T: tRoles, Roles: []proto.RoleSummary{{Name: "新"}, {Name: fmt.Sprintf("新%d", i)}}})
		}()
	}
	wg.Wait()
	h.srv.onRoles(cur, wire.Roles{T: tRoles, Roles: []proto.RoleSummary{{Name: "最后"}}})
	if got := machineRoles(t, h, m.ID); !slices.Equal(got, []string{"最后"}) {
		t.Fatalf("并发之后应是新连接最后一份: %v", got)
	}
}

// 兜底再加严：备注与目录并成一行、去掉控制字符与双向控制符；上限只认空串或已知的风险名；禁用条数有上限；
// 放行上限只认 -1、0 或 5～1440。
func TestCleanRolesStrict(t *testing.T) {
	got := cleanRoles([]proto.RoleSummary{
		{Name: "a", Note: "第一行\n第二行\u202e反转\u0007", Ceiling: "network", ForbidCount: 5000, TrustMaxMins: 4, Dirs: []string{"~/a\nb", "~/\u2066x\u2069", "\u200b"}},
		{Name: "b", Ceiling: "everything", TrustMaxMins: 1441},
		{Name: "c", Ceiling: "destructive", TrustMaxMins: 5},
		{Name: "d", TrustMaxMins: 1440},
		{Name: "e", TrustMaxMins: -1, ForbidCount: maxRoleForbid},
	})
	a := got[0]
	if a.Note != "第一行 第二行反转" || a.Ceiling != "network" || a.ForbidCount != maxRoleForbid || a.TrustMaxMins != 0 ||
		!slices.Equal(a.Dirs, []string{"~/a b", "~/x"}) {
		t.Fatalf("a: %+v", a)
	}
	if got[1].Ceiling != "" || got[1].TrustMaxMins != 0 {
		t.Fatalf("不认识的上限、超出范围的放行上限应置空、置 0: %+v", got[1])
	}
	if got[2].Ceiling != "destructive" || got[2].TrustMaxMins != 5 || got[3].TrustMaxMins != 1440 || got[4].TrustMaxMins != -1 || got[4].ForbidCount != maxRoleForbid {
		t.Fatalf("合规的照留: %+v", got[2:])
	}
}

// 越界预检读库失败时不能当成通过：错误原样返回，decide 回 500，不建放行。
func TestRefuseBeyondRoleReadError(t *testing.T) {
	h := newTestHub(t)
	if err := h.srv.refuseBeyondRole("nope"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("没有这条请求应返回 ErrNotFound: %v", err)
	}
	h.st.Close()
	if err := h.srv.refuseBeyondRole("r1"); err == nil {
		t.Fatal("读库失败应返回错误，不能当成通过")
	}
}

// 预检之后才登记的越界请求（并发登记同 id）：真正建放行、学规则的那一步还要看 BeyondRole，不依赖预检。
func TestBeyondRoleCheckedAtSideEffect(t *testing.T) {
	h := newTestHub(t)
	id, _, _, _ := roleAgent(t, h, "mac", []string{wire.FeatureTrust, wire.FeatureRole}, twoRoles)
	if err := h.st.InsertRequest(store.Request{ID: "r1", MachineID: id, Kind: "permission", ToolName: "Bash", State: "pending", BeyondRole: true,
		Role: "写作", ProjectRoot: "/p", CLISessionID: "s1",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push"}}); err != nil {
		t.Fatal(err)
	}
	// 绕过预检，直接走建放行与学规则的那两步
	h.srv.trustMu.Lock()
	_, err := h.srv.reserveTrust(DecideInput{RequestID: "r1", Action: "allow", Trust: &TrustInput{Scope: "project", Minutes: 30}})
	h.srv.trustMu.Unlock()
	if !errors.Is(err, errBeyondRole) {
		t.Fatalf("reserveTrust 应拒越界: %v", err)
	}
	if list, _ := h.st.ListTrusts(""); len(list) != 0 {
		t.Fatalf("不应建放行: %+v", list)
	}
	before, _ := h.st.ListRules()
	if _, _, err := h.srv.commitDecision(DecideInput{RequestID: "r1", Action: "allow", Remember: true}, "approved", ""); !errors.Is(err, errBeyondRole) {
		t.Fatalf("commitDecision 应拒越界的记住: %v", err)
	}
	if after, _ := h.st.ListRules(); len(after) != len(before) {
		t.Fatalf("不应学规则: %d → %d", len(before), len(after))
	}
	if r, _ := h.st.GetRequest("r1"); r.State != "pending" {
		t.Fatalf("请求应仍待决: %s", r.State)
	}
}
