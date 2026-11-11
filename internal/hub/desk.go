package hub

import (
	"cmp"
	"net/http"
	"slices"
	"time"

	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/proto"
)

// 总台（0.3 起）：GET /api/desk 按角色聚合每台机器的等我、放行中、拦下、自动放行、会话数与最近动作。口径（规格第 6 节）：
//   - 行 = 角色表的每一行（按机器、文件里的先后），加上每台机器一行“未分配”：该机器有未分配的待批（不论多旧）、24 小时内有未分配的请求
//     或有未分配的生效放行时才出现。
//     库里有请求、会话、放行但角色已从文件里删掉的，并进未分配。已吊销的机器与 webhook 不进总台。
//   - waiting：pending 的 permission 与 ask。denied：24 小时内（按创建时间）permission 的 denied + auto_denied。
//     auto_allowed：24 小时内 permission 的 auto_allowed。sessions：24 小时内有事件的会话数（按会话当前的角色）。
//   - trusts_active：生效中的放行。会话放行算到该会话当前的角色；项目放行算到这台机器上该项目根最近一条请求的角色，
//     没有请求记录的算未分配。hub 不做目录模式匹配。
//   - recent：最近 5 条 permission，summary 是收件箱卡片那一行摘要（载荷已脱敏）。未分配一行的最近动作取角色为空的，加上 24 小时内
//     计数、会话或放行里出现过、文件里已没有的角色名的；更早就不再活动的已删角色不再翻出来（每条都按索引取，不扫全部历史）。

const (
	deskWindow    = 24 * time.Hour
	deskRecentMax = 5
)

// deskRecent 是总台一行里的一条最近动作。
type deskRecent struct {
	ID         string `json:"id"`
	ToolName   string `json:"tool_name"`
	Summary    string `json:"summary"`
	Risk       string `json:"risk"`
	State      string `json:"state"`
	BeyondRole bool   `json:"beyond_role"`
	At         string `json:"at"`
}

// deskStats 是一行的数字与最近动作，角色行与未分配行共用。
type deskStats struct {
	Waiting      int          `json:"waiting"`
	TrustsActive int          `json:"trusts_active"`
	Denied       int          `json:"denied"`
	AutoAllowed  int          `json:"auto_allowed"`
	Sessions     int          `json:"sessions"`
	Recent       []deskRecent `json:"recent"`
}

// deskMachine 是一行所属的机器。
type deskMachine struct {
	MachineID   string `json:"machine_id"`
	MachineName string `json:"machine_name"`
	Online      bool   `json:"online"`
}

// deskRole 是一个角色的一行：角色摘要（同 proto.RoleSummary，但每个键都给出）加数字。
type deskRole struct {
	deskMachine
	Name         string   `json:"name"`
	Note         string   `json:"note"`
	Ceiling      string   `json:"ceiling"`
	ForbidCount  int      `json:"forbid_count"`
	TrustMaxMins int      `json:"trust_max_mins"`
	Dirs         []string `json:"dirs"`
	deskStats
}

// deskUnassigned 是一台机器“未分配”的一行。
type deskUnassigned struct {
	deskMachine
	deskStats
}

type deskView struct {
	WindowHours int              `json:"window_hours"`
	Roles       []deskRole       `json:"roles"`
	Unassigned  []deskUnassigned `json:"unassigned"`
}

// deskAgg 是组装中的一行：数字、24 小时内的请求数（决定未分配一行出不出现）与最近动作。
type deskAgg struct {
	deskStats
	requests int
	recent   []store.Request
}

// buildDesk 按口径组装总台。
func (s *Server) buildDesk(now time.Time) (deskView, error) {
	since := now.Add(-deskWindow)
	machines, err := s.store.ListMachines()
	if err != nil {
		return deskView{}, err
	}
	live := map[string]store.Machine{}
	for _, m := range machines {
		if !m.Revoked {
			live[m.ID] = m
		}
	}
	roles, err := s.store.ListMachineRoles()
	if err != nil {
		return deskView{}, err
	}
	known := map[store.DeskKey]bool{}
	byMachine := map[string][]proto.RoleSummary{}
	for _, r := range roles {
		if _, ok := live[r.MachineID]; ok {
			known[store.DeskKey{MachineID: r.MachineID, Role: r.Name}] = true
			byMachine[r.MachineID] = append(byMachine[r.MachineID], r.RoleSummary)
		}
	}
	agg := map[store.DeskKey]*deskAgg{}
	// at 把 (机器, 角色) 归到总台的一行：不在库里的机器（webhook、吊销的）回 nil，文件里已没有的角色并进未分配
	at := func(machineID, role string) *deskAgg {
		if _, ok := live[machineID]; !ok {
			return nil
		}
		k := store.DeskKey{MachineID: machineID, Role: role}
		if !known[k] {
			k.Role = ""
		}
		a := agg[k]
		if a == nil {
			a = &deskAgg{}
			agg[k] = a
		}
		return a
	}
	// unknown 记下每台机器上出现过、但文件里已没有的角色名（并进未分配），最近动作要按它们逐个再取
	unknown := map[string]map[string]bool{}
	note := func(machineID, role string) {
		if _, ok := live[machineID]; ok && role != "" && !known[store.DeskKey{MachineID: machineID, Role: role}] {
			if unknown[machineID] == nil {
				unknown[machineID] = map[string]bool{}
			}
			unknown[machineID][role] = true
		}
	}
	counts, err := s.store.DeskCounts(since)
	if err != nil {
		return deskView{}, err
	}
	for _, c := range counts {
		note(c.MachineID, c.Role)
		if a := at(c.MachineID, c.Role); a != nil {
			a.Waiting += c.Waiting
			a.Denied += c.Denied
			a.AutoAllowed += c.AutoAllowed
			a.requests += c.Requests
		}
	}
	sessions, err := s.store.DeskSessions(since)
	if err != nil {
		return deskView{}, err
	}
	for k, n := range sessions {
		note(k.MachineID, k.Role)
		if a := at(k.MachineID, k.Role); a != nil {
			a.Sessions += n
		}
	}
	trusts, err := s.store.ListTrusts("")
	if err != nil {
		return deskView{}, err
	}
	for _, t := range trusts {
		role, err := s.trustRole(t)
		if err != nil {
			return deskView{}, err
		}
		note(t.MachineID, role)
		if a := at(t.MachineID, role); a != nil {
			a.TrustsActive++
		}
	}
	// 最近动作：每个 (机器, 角色) 走索引取最近 5 条；未分配取空串与那些已删的角色名，合并后再取前 5 条（见 finishStats）
	for _, m := range machines {
		if m.Revoked {
			continue
		}
		names := []string{""}
		for _, r := range byMachine[m.ID] {
			names = append(names, r.Name)
		}
		for name := range unknown[m.ID] {
			names = append(names, name)
		}
		for _, name := range names {
			recent, err := s.store.RecentPermissions(m.ID, name, deskRecentMax)
			if err != nil {
				return deskView{}, err
			}
			if len(recent) > 0 {
				a := at(m.ID, name)
				a.recent = append(a.recent, recent...)
			}
		}
	}

	out := deskView{WindowHours: int(deskWindow / time.Hour), Roles: []deskRole{}, Unassigned: []deskUnassigned{}}
	for _, m := range machines {
		if m.Revoked {
			continue
		}
		dm := deskMachine{MachineID: m.ID, MachineName: m.Name, Online: s.agents.connected(m.ID)}
		for _, r := range byMachine[m.ID] {
			dirs := r.Dirs
			if dirs == nil {
				dirs = []string{}
			}
			out.Roles = append(out.Roles, deskRole{deskMachine: dm, Name: r.Name, Note: r.Note, Ceiling: r.Ceiling, ForbidCount: r.ForbidCount,
				TrustMaxMins: r.TrustMaxMins, Dirs: dirs, deskStats: finishStats(agg[store.DeskKey{MachineID: m.ID, Role: r.Name}])})
		}
		if a := agg[store.DeskKey{MachineID: m.ID}]; a != nil && (a.Waiting > 0 || a.requests > 0 || a.TrustsActive > 0) {
			out.Unassigned = append(out.Unassigned, deskUnassigned{deskMachine: dm, deskStats: finishStats(a)})
		}
	}
	return out, nil
}

// trustRole 是一条生效放行归属的角色：会话放行取该会话当前的角色，项目放行取这台机器上该项目根最近一条请求的角色；找不到为空串（未分配）。
func (s *Server) trustRole(t proto.Trust) (string, error) {
	var role string
	var err error
	if t.Scope == proto.TrustScopeSession {
		role, _, err = s.store.SessionRoleByCLI(t.MachineID, t.Tool, t.CLISession)
	} else {
		role, _, err = s.store.LatestProjectRole(t.MachineID, t.Project)
	}
	return role, err
}

// finishStats 把组装中的一行收尾：最近动作（几组并进未分配时可能多于 5 条）按时间倒序取前 5 条，编成 deskRecent。a 为 nil 时全是 0。
func finishStats(a *deskAgg) deskStats {
	if a == nil {
		return deskStats{Recent: []deskRecent{}}
	}
	st := a.deskStats
	slices.SortStableFunc(a.recent, func(x, y store.Request) int {
		return cmp.Or(cmp.Compare(y.CreatedAt, x.CreatedAt), cmp.Compare(y.ID, x.ID))
	})
	st.Recent = make([]deskRecent, 0, min(len(a.recent), deskRecentMax))
	for _, r := range a.recent[:min(len(a.recent), deskRecentMax)] {
		summary := clean(payloadSummary(r.Payload), pushSummaryLen)
		if summary == "" {
			summary = clean(r.ToolName, pushSummaryLen)
		}
		st.Recent = append(st.Recent, deskRecent{ID: r.ID, ToolName: r.ToolName, Summary: summary, Risk: r.Risk, State: r.State, BeyondRole: r.BeyondRole, At: r.CreatedAt})
	}
	return st
}

// handleDesk 是 GET /api/desk：总台（见本文件开头的口径）。没有任何行时 roles、unassigned 是空数组。
func (s *Server) handleDesk(w http.ResponseWriter, r *http.Request) {
	d, err := s.buildDesk(time.Now())
	if err != nil {
		s.internalErr(w, "组装总台", err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}
