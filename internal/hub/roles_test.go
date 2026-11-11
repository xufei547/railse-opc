package hub

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
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
		{Name: "a​"},      // 格式字符丢
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
	))
	c.expectNothing()
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
