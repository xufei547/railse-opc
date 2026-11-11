package hub

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// deskJSON 是 /api/desk 的回应（测试用，字段与 deskView 相同）。
type deskJSON struct {
	WindowHours int `json:"window_hours"`
	Roles       []struct {
		deskRowJSON
		Name         string   `json:"name"`
		Note         string   `json:"note"`
		Ceiling      string   `json:"ceiling"`
		ForbidCount  int      `json:"forbid_count"`
		TrustMaxMins int      `json:"trust_max_mins"`
		Dirs         []string `json:"dirs"`
	} `json:"roles"`
	Unassigned []deskRowJSON `json:"unassigned"`
}

type deskRowJSON struct {
	MachineID    string       `json:"machine_id"`
	MachineName  string       `json:"machine_name"`
	Online       bool         `json:"online"`
	Waiting      int          `json:"waiting"`
	TrustsActive int          `json:"trusts_active"`
	Denied       int          `json:"denied"`
	AutoAllowed  int          `json:"auto_allowed"`
	Sessions     int          `json:"sessions"`
	Recent       []deskRecent `json:"recent"`
}

// counts 是一行的五个数字，便于整体比较。
func (r deskRowJSON) counts() [5]int {
	return [5]int{r.Waiting, r.TrustsActive, r.Denied, r.AutoAllowed, r.Sessions}
}

func recentIDs(rs []deskRecent) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

// deskInsert 直接往库里插一条请求：ago 是距现在多久创建的。
func deskInsert(t *testing.T, h *testHub, r store.Request, ago time.Duration) {
	t.Helper()
	r.CreatedAt = store.FormatTime(time.Now().Add(-ago))
	if r.ToolName == "" {
		r.ToolName = "Bash"
	}
	if r.Payload.Type == "" && r.Kind == "permission" {
		r.Payload = policy.Payload{Type: policy.PayloadShell, Command: "cmd " + r.ID}
	}
	if err := h.st.InsertRequest(r); err != nil {
		t.Fatal(err)
	}
}

func deskSession(t *testing.T, h *testHub, machineID, cli, role string) {
	t.Helper()
	if _, err := h.st.UpsertSession(machineID, proto.Session{Tool: "claude", CLISessionID: cli, Role: role}, ""); err != nil {
		t.Fatal(err)
	}
}

func deskTrust(t *testing.T, h *testHub, tr proto.Trust) {
	t.Helper()
	tr.Until, tr.CreatedAt, tr.CreatedBy = time.Now().Add(time.Hour), time.Now(), "phone"
	if _, err := h.st.UpsertTrust(tr); err != nil {
		t.Fatal(err)
	}
}

// /api/desk 的各项口径。
func TestDeskAggregates(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	roles := []proto.RoleSummary{
		{Name: "开发", Note: "写代码", Ceiling: "network", ForbidCount: 1, TrustMaxMins: 1440, Dirs: []string{"~/RailseWork"}},
		{Name: "写作", Note: "只改文档", Ceiling: "write_in_project", ForbidCount: 2, TrustMaxMins: 120, Dirs: []string{"~/dev/*/docs"}},
	}
	a, _, _, _ := roleAgent(t, h, "mac", []string{wire.FeatureRole, wire.FeatureTrust}, roles)
	b, err := h.st.CreateMachine("old", "sb", "") // 老守护进程：没有角色，不在线
	if err != nil {
		t.Fatal(err)
	}
	c, _ := h.st.CreateMachine("gone", "sc", "") // 已吊销：不进总台
	d, _ := h.st.CreateMachine("idle", "sd", "") // 只有 24 小时外的请求：未分配一行不出现
	hr := time.Hour

	// 写作：等我 2（审批 + 提问；question 不算），拦下 2（24 小时外的不算），自动放行 1，越界的带标记
	deskInsert(t, h, store.Request{ID: "w1", MachineID: a, Kind: "permission", State: "pending", Role: "写作", BeyondRole: true, Risk: "outward",
		ProjectRoot: "/pw", Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push origin main"}}, 0)
	deskInsert(t, h, store.Request{ID: "w2", MachineID: a, Kind: "ask", State: "pending", Role: "写作", ToolName: proto.AskToolName}, 0)
	deskInsert(t, h, store.Request{ID: "w3", MachineID: a, Kind: "question", State: "recorded", Role: "写作"}, 0)
	deskInsert(t, h, store.Request{ID: "w4", MachineID: a, Kind: "permission", State: "denied", Role: "写作"}, 1*hr)
	deskInsert(t, h, store.Request{ID: "w5", MachineID: a, Kind: "permission", State: "auto_denied", Role: "写作", DecidedBy: "role:写作"}, 2*hr)
	deskInsert(t, h, store.Request{ID: "w6", MachineID: a, Kind: "permission", State: "denied", Role: "写作"}, 25*hr)
	deskInsert(t, h, store.Request{ID: "w7", MachineID: a, Kind: "permission", State: "auto_allowed", Role: "写作",
		Payload: policy.Payload{Type: policy.PayloadMCP, Server: "github", Tool: "create_issue"}}, 3*hr)
	// 开发：自动放行 6，最近动作只给 5 条
	for i := range 6 {
		deskInsert(t, h, store.Request{ID: "k" + string(rune('0'+i)), MachineID: a, Kind: "permission", State: "auto_allowed", Role: "开发"}, time.Duration(10-i)*time.Minute)
	}
	// 角色已从文件里删掉的请求与会话：并进未分配
	deskInsert(t, h, store.Request{ID: "x1", MachineID: a, Kind: "permission", State: "auto_allowed", Role: "已删"}, 4*hr)
	deskSession(t, h, a, "c1", "写作")
	deskSession(t, h, a, "c2", "开发")
	deskSession(t, h, a, "c3", "已删")
	// 放行：会话放行按会话当前的角色；项目放行按该项目最近一条请求的角色，没有请求记录的算未分配
	deskTrust(t, h, proto.Trust{ID: "t1", MachineID: a, Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "c1"})
	deskTrust(t, h, proto.Trust{ID: "t2", MachineID: a, Scope: proto.TrustScopeProject, Project: "/pw"})
	deskTrust(t, h, proto.Trust{ID: "t3", MachineID: a, Scope: proto.TrustScopeProject, Project: "/none"})
	deskTrust(t, h, proto.Trust{ID: "t4", MachineID: a, Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "unknown"})
	// 老机器：24 小时外的请求不让它出现，一条项目放行让它出现
	deskInsert(t, h, store.Request{ID: "b1", MachineID: b.ID, Kind: "permission", State: "approved"}, 30*hr)
	deskTrust(t, h, proto.Trust{ID: "t5", MachineID: b.ID, Scope: proto.TrustScopeProject, Project: "/b"})
	// 吊销的机器、只有旧请求的机器、webhook
	deskInsert(t, h, store.Request{ID: "c1", MachineID: c.ID, Kind: "permission", State: "pending"}, 0)
	if err := h.st.RevokeMachine(c.ID); err != nil {
		t.Fatal(err)
	}
	deskInsert(t, h, store.Request{ID: "d1", MachineID: d.ID, Kind: "done", State: "recorded"}, 26*hr)
	deskInsert(t, h, store.Request{ID: "h1", MachineID: webhookMachineID, Kind: "webhook", State: "pending", Title: "部署"}, 0)

	var got deskJSON
	getJSON(t, h, ck, "/api/desk", &got)
	if got.WindowHours != 24 || len(got.Roles) != 2 {
		t.Fatalf("总台: %+v", got)
	}
	dev, wr := got.Roles[0], got.Roles[1]
	if dev.Name != "开发" || wr.Name != "写作" || !dev.Online || dev.MachineName != "mac" || dev.MachineID != a {
		t.Fatalf("角色行的顺序与机器: %+v / %+v", dev, wr)
	}
	if wr.Note != "只改文档" || wr.Ceiling != "write_in_project" || wr.ForbidCount != 2 || wr.TrustMaxMins != 120 || !slices.Equal(wr.Dirs, []string{"~/dev/*/docs"}) {
		t.Fatalf("写作的摘要: %+v", wr)
	}
	// [等我, 放行中, 拦下, 自动放行, 会话]
	if c := wr.counts(); c != [5]int{2, 2, 2, 1, 1} {
		t.Fatalf("写作的数字 = %v", c)
	}
	if c := dev.counts(); c != [5]int{0, 0, 0, 6, 1} {
		t.Fatalf("开发的数字 = %v", c)
	}
	if ids := recentIDs(wr.Recent); !slices.Equal(ids, []string{"w1", "w4", "w5", "w7", "w6"}) {
		t.Fatalf("写作的最近动作 = %v", ids)
	}
	if r := wr.Recent[0]; r.Summary != "git push origin main" || !r.BeyondRole || r.State != "pending" || r.Risk != "outward" || r.ToolName != "Bash" || r.At == "" {
		t.Fatalf("越界的最近动作: %+v", r)
	}
	if r := wr.Recent[3]; r.Summary != "github/create_issue" || r.BeyondRole {
		t.Fatalf("MCP 的摘要: %+v", r)
	}
	if ids := recentIDs(dev.Recent); !slices.Equal(ids, []string{"k5", "k4", "k3", "k2", "k1"}) {
		t.Fatalf("开发的最近动作至多 5 条、新的在前: %v", ids)
	}
	if len(got.Unassigned) != 2 {
		t.Fatalf("未分配应有 mac 与 old 两行: %+v", got.Unassigned)
	}
	ua, ub := got.Unassigned[0], got.Unassigned[1]
	if ua.MachineID != a || ub.MachineID != b.ID || ub.Online || ub.MachineName != "old" {
		t.Fatalf("未分配的机器: %+v / %+v", ua, ub)
	}
	// mac 的未分配：已删角色的自动放行 1、会话 1，放行 t3（项目没有请求记录）与 t4（会话不存在）
	if c := ua.counts(); c != [5]int{0, 2, 0, 1, 1} || !slices.Equal(recentIDs(ua.Recent), []string{"x1"}) {
		t.Fatalf("mac 未分配: %v %v", c, recentIDs(ua.Recent))
	}
	if c := ub.counts(); c != [5]int{0, 1, 0, 0, 0} || len(ub.Recent) != 1 {
		t.Fatalf("old 未分配: %v %+v", c, ub.Recent)
	}
}

// 老守护进程（不声明 role）：总台没有角色行，只有未分配；什么都没有时是空数组。
func TestDeskOldAgentOnlyUnassigned(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	resp := doReq(t, "GET", h.http.URL+"/api/desk", nil, "", ck)
	var raw map[string]json.RawMessage
	decodeBody(t, resp, &raw)
	if string(raw["roles"]) != "[]" || string(raw["unassigned"]) != "[]" {
		t.Fatalf("空的总台: %s %s", raw["roles"], raw["unassigned"])
	}
	id, _, c, _ := roleAgent(t, h, "mac", []string{wire.FeatureTrust}, []proto.RoleSummary{{Name: "写作"}})
	p := pendingFrame("r1", "s1", "t1", "git push")
	p.Session.Role = "写作" // 老守护进程不会带；带了也不认
	c.send(p)
	waitFor(t, "登记", func() bool { n, _ := h.st.CountPending(); return n == 1 })
	var got deskJSON
	getJSON(t, h, ck, "/api/desk", &got)
	if len(got.Roles) != 0 || len(got.Unassigned) != 1 || got.Unassigned[0].MachineID != id || !got.Unassigned[0].Online ||
		got.Unassigned[0].counts() != [5]int{1, 0, 0, 0, 1} {
		t.Fatalf("老守护进程的总台: %+v", got)
	}
}

// 要登录。
func TestDeskRequiresLogin(t *testing.T) {
	h := newTestHub(t)
	if resp := doReq(t, "GET", h.http.URL+"/api/desk", nil, "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("没登录应 401: %d", resp.StatusCode)
	}
}

// 超过 24 小时的待批也要看得到：角色为空、角色已从文件里删掉的 permission 与 ask，不论多旧都并进未分配、计入等我，未分配一行照样出现。
func TestDeskOldPendingUnassigned(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	m, err := h.st.CreateMachine("mac", "sec", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.st.ReplaceMachineRoles(m.ID, []proto.RoleSummary{{Name: "写作"}}); err != nil {
		t.Fatal(err)
	}
	old := 30 * time.Hour
	deskInsert(t, h, store.Request{ID: "e1", MachineID: m.ID, Kind: "permission", State: "pending", Role: ""}, old)
	deskInsert(t, h, store.Request{ID: "e2", MachineID: m.ID, Kind: "ask", State: "pending", Role: "", ToolName: proto.AskToolName}, old)
	deskInsert(t, h, store.Request{ID: "g1", MachineID: m.ID, Kind: "permission", State: "pending", Role: "已删"}, old)
	deskInsert(t, h, store.Request{ID: "g2", MachineID: m.ID, Kind: "ask", State: "pending", Role: "已删", ToolName: proto.AskToolName}, old)
	deskInsert(t, h, store.Request{ID: "k1", MachineID: m.ID, Kind: "permission", State: "pending", Role: "写作"}, old)
	var got deskJSON
	getJSON(t, h, ck, "/api/desk", &got)
	if len(got.Roles) != 1 || got.Roles[0].Waiting != 1 {
		t.Fatalf("写作的等我: %+v", got.Roles)
	}
	if len(got.Unassigned) != 1 || got.Unassigned[0].Waiting != 4 {
		t.Fatalf("超过 24 小时的未分配与已删角色的待批应在未分配一行、等我 4: %+v", got.Unassigned)
	}
}
