package hub

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/proto"
)

// 限时放行在 hub 这一侧：守护进程是执行者，hub 存一份副本给手机看、建、撤，并与守护进程双向同步——
// hub → 守护进程是整台机器的快照（welcome 的 trusts、trusts 帧），守护进程 → hub 是逐条变更（trust 帧）。
// 建放行（手机批准时顺带）、撤销、到期在 hub 记审计 trust_set / trust_revoke / trust_expired；守护进程报来的变更它自己审计，hub 不重复记。

// errBadTrust 是批准时顺带的放行不合规：接口回 400，文案说明原因，决定不落库。
var errBadTrust = errors.New("放行不合规")

// trustRefused 是给用户看的拒建原因，errors.Is 认作 errBadTrust。
type trustRefused string

func (e trustRefused) Error() string        { return string(e) }
func (e trustRefused) Is(target error) bool { return target == errBadTrust }

const (
	errTrustNotAllow  = trustRefused("只有批准审批时才能顺带放行")
	errTrustScope     = trustRefused("放行范围只能是 session 或 project")
	errTrustTooShort  = trustRefused("放行时长至少 5 分钟")
	errTrustOldAgent  = trustRefused("这台机器的守护进程太旧，不支持限时放行，请升级 railse")
	errTrustOffline   = trustRefused("这台机器不在线，不能限时放行")
	errTrustNoSession = trustRefused("这条请求没有会话编号，只能放行这个项目")
	errTrustNoProject = trustRefused("这条请求没有项目根，只能放行这个会话")
	errTrustTooMany   = trustRefused("这台机器同时生效的放行已有 20 条，先撤掉一些")
)

const (
	trustSweepInterval = time.Minute
	maxTrustNoteLen    = 200 // 守护进程报来的备注存库前截到这么多字符
)

// TrustInput 是批准时顺带的放行：Scope 为 session 或 project，Minutes 为 0 时按默认 2 小时，超过 24 小时截断、短于 5 分钟拒建。
type TrustInput struct {
	Scope   string `json:"scope"`
	Minutes int    `json:"minutes"`
}

// trustSupported 报告这台机器当前连接的守护进程声明了 trust 能力。不在线为否。
func (s *Server) trustSupported(machineID string) bool {
	return s.agents.supports(machineID, wire.FeatureTrust)
}

// trustsSnapshot 是发给守护进程的快照帧。读库失败返回 false，调用方不发（宁可不发，也不发一个空快照让守护进程以为全被撤了）。
func (s *Server) trustsSnapshot(machineID string) (wire.Trusts, bool) {
	list, err := s.store.ListTrusts(machineID)
	if err != nil {
		s.log.Printf("列机器 %s 的放行: %v", machineID, err)
		return wire.Trusts{}, false
	}
	return wire.Trusts{T: wire.TTrusts, Trusts: list}, true
}

// pushTrustsLocked 把这台机器的放行快照排进它当前连接的发送队列（调用方持 trustMu）。不在线或守护进程没声明 trust 时不发：
// 重连的 welcome 会带快照。
func (s *Server) pushTrustsLocked(machineID string) {
	if !s.trustSupported(machineID) {
		return
	}
	if snap, ok := s.trustsSnapshot(machineID); ok {
		s.agents.send(machineID, snap)
	}
}

// broadcastTrusts 告诉网页生效中的放行条数变了（status 帧带 trusts）。读不出时不广播。
func (s *Server) broadcastTrusts() {
	list, err := s.store.ListTrusts("")
	if err != nil {
		s.log.Printf("统计放行: %v", err)
		return
	}
	s.clients.broadcast("status", map[string]int{"trusts": len(list)})
}

// trustDetail 是放行审计的 detail：不带备注之外的内容（备注已脱敏）。
func trustDetail(t proto.Trust) map[string]string {
	return map[string]string{"trust_id": t.ID, "scope": t.Scope, "tool": t.Tool, "cli_session_id": t.CLISession, "project_root": t.Project,
		"until": store.FormatTime(t.Until), "created_by": t.CreatedBy}
}

// reserveTrustLocked 为批准请求 r 时顺带的放行做校验并入库（调用方持 trustMu），返回入库的条目；还没审计、没下发，
// 批准落库失败时由调用方 DeleteTrust 收回。校验：只有 allow 且请求是审批（kind permission）；机器在线且守护进程声明了 trust；
// 范围与目标（session 要请求有 CLI 会话，project 要有项目根）；时长 0 按默认、短于 5 分钟拒、超 24 小时截；这台机器生效中的未满 20 条。
func (s *Server) reserveTrustLocked(r store.Request, action string, in TrustInput) (proto.Trust, error) {
	if action != "allow" || r.Kind != "permission" {
		return proto.Trust{}, errTrustNotAllow
	}
	d := proto.TrustDefault
	if in.Minutes != 0 {
		d = time.Duration(in.Minutes) * time.Minute
	}
	if d < proto.TrustMin {
		return proto.Trust{}, errTrustTooShort
	}
	d = min(d, proto.TrustMax)
	now := time.Now()
	t := proto.Trust{ID: store.NewID(), MachineID: r.MachineID, Scope: in.Scope, Until: now.Add(d), CreatedAt: now, CreatedBy: "phone",
		Project: r.ProjectRoot}
	switch in.Scope {
	case proto.TrustScopeSession:
		tool := ""
		if r.SessionID != "" && r.CLISessionID != "" {
			var err error
			if tool, err = s.store.SessionTool(r.SessionID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return proto.Trust{}, err
			}
		}
		if tool == "" {
			return proto.Trust{}, errTrustNoSession
		}
		// 会话放行也记下项目根，只给手机显示用；守护进程只按 Tool + CLI 会话认
		t.Tool, t.CLISession = tool, r.CLISessionID
	case proto.TrustScopeProject:
		if r.ProjectRoot == "" {
			return proto.Trust{}, errTrustNoProject
		}
	default:
		return proto.Trust{}, errTrustScope
	}
	if !s.agents.connected(r.MachineID) {
		return proto.Trust{}, errTrustOffline
	}
	if !s.trustSupported(r.MachineID) {
		return proto.Trust{}, errTrustOldAgent
	}
	live, err := s.store.ListTrusts(r.MachineID)
	if err != nil {
		return proto.Trust{}, err
	}
	if len(live) >= proto.TrustPerMachine {
		return proto.Trust{}, errTrustTooMany
	}
	if _, err := s.store.UpsertTrust(t); err != nil {
		return proto.Trust{}, err
	}
	return t, nil
}

// unreserveTrust 收回 reserveTrustLocked 入库、却因批准没落库而作废的放行（它从没下发过）。
func (s *Server) unreserveTrust(t proto.Trust) {
	if err := s.store.DeleteTrust(t.ID); err != nil {
		s.log.Printf("收回作废的放行 %s: %v", t.ID, err)
	}
}

// revokeTrust 是手机上的撤销：标撤、审计 trust_revoke、推快照给那台机器（离线时重连的 welcome 带）、通知网页。
// 没有或已撤返回 store.ErrNotFound。
func (s *Server) revokeTrust(id string) error {
	s.trustMu.Lock()
	t, err := s.store.RevokeTrust("", id, store.TrustRevokeUser)
	if err == nil {
		s.pushTrustsLocked(t.MachineID)
	}
	s.trustMu.Unlock()
	if err != nil {
		return err
	}
	s.audit(t.MachineID, "user", "", "trust_revoke", trustDetail(t))
	s.broadcastTrusts()
	return nil
}

// revokeMachineTrusts 撤掉已吊销机器的全部放行，逐条审计 trust_revoke：它连不回来了，这些放行留着只会让手机上的条数不对。
func (s *Server) revokeMachineTrusts(machineID string) {
	s.trustMu.Lock()
	list, err := s.store.ListTrusts(machineID)
	var gone []proto.Trust
	for _, t := range list {
		if _, err := s.store.RevokeTrust(machineID, t.ID, store.TrustRevokeUser); err != nil {
			s.log.Printf("撤吊销机器 %s 的放行 %s: %v", machineID, t.ID, err)
			continue
		}
		gone = append(gone, t)
	}
	s.trustMu.Unlock()
	if err != nil {
		s.log.Printf("列吊销机器 %s 的放行: %v", machineID, err)
		return
	}
	for _, t := range gone {
		s.audit(machineID, "user", "", "trust_revoke", trustDetail(t))
	}
	if len(gone) > 0 {
		s.broadcastTrusts()
	}
}

// cleanAgentTrust 校验并整理守护进程报来的一条放行：机器以连接为准；id 要合规；范围与目标要齐，目标字段超长的拒（截断会让它
// 对不上守护进程手里的那条）；已到期的拒；超过 24 小时的截；备注脱敏、截断。created_by 照来的（截断）。
func cleanAgentTrust(machineID string, t proto.Trust, now time.Time) (proto.Trust, bool) {
	t.MachineID = machineID
	if !validRequestID(t.ID) || len(t.Tool) > maxFieldLen || len(t.CLISession) > maxFieldLen || len(t.Project) > maxPathLen {
		return proto.Trust{}, false
	}
	switch t.Scope {
	case proto.TrustScopeSession:
		if t.Tool == "" || t.CLISession == "" {
			return proto.Trust{}, false
		}
	case proto.TrustScopeProject:
		if t.Project == "" {
			return proto.Trust{}, false
		}
	default:
		return proto.Trust{}, false
	}
	if !t.Until.After(now) {
		return proto.Trust{}, false
	}
	if limit := now.Add(proto.TrustMax); t.Until.After(limit) {
		t.Until = limit
	}
	t.CreatedAt = hubTime(t.CreatedAt)
	t.CreatedBy = truncate(t.CreatedBy, maxFieldLen)
	t.Note = redactText(t.Note, maxTrustNoteLen)
	return t, true
}

// onTrustChange 处理守护进程从连接 c 报来的放行变更：set 入库（这台机器生效中的已满 20 条且不是改已有的，不收），revoke、expired
// 标撤（只撤这台机器自己的）。真的改了库才回推快照（守护进程据此把本机建的标为已同步）并通知网页；不合规的、没改动的只记日志：
// 回推快照会让守护进程再补发同一条，来回不停。
func (s *Server) onTrustChange(c *agentConn, ch wire.TrustChange) {
	m := c.machine
	s.trustMu.Lock()
	changed, why := s.applyAgentTrustLocked(m.ID, ch)
	if changed {
		s.pushTrustsLocked(m.ID)
	}
	s.trustMu.Unlock()
	if why != "" {
		s.log.Printf("机器 %s 报来的放行变更（%q）不收：%s", m.ID, clip(ch.Op), why)
	}
	if changed {
		s.broadcastTrusts()
	}
}

// applyAgentTrustLocked 是 onTrustChange 的库操作（调用方持 trustMu）。返回是否改了库；没改时 why 说明原因（空表示无需说明）。
func (s *Server) applyAgentTrustLocked(machineID string, ch wire.TrustChange) (changed bool, why string) {
	switch ch.Op {
	case wire.TrustOpSet:
		t, ok := cleanAgentTrust(machineID, ch.Trust, time.Now())
		if !ok {
			return false, "放行不合规"
		}
		live, err := s.store.ListTrusts(machineID)
		if err != nil {
			return false, "列放行: " + err.Error()
		}
		if len(live) >= proto.TrustPerMachine && !slices.ContainsFunc(live, func(x proto.Trust) bool { return x.ID == t.ID }) {
			return false, "同时生效的放行已满 20 条"
		}
		ok, err = s.store.UpsertTrust(t)
		if err != nil {
			return false, "入库: " + err.Error()
		}
		if !ok {
			return false, "已撤或属于别的机器"
		}
		return true, ""
	case wire.TrustOpRevoke, wire.TrustOpExpired:
		reason := store.TrustRevokeAgent
		if ch.Op == wire.TrustOpExpired {
			reason = store.TrustRevokeExpired
		}
		_, err := s.store.RevokeTrust(machineID, ch.Trust.ID, reason)
		if errors.Is(err, store.ErrNotFound) {
			return false, "" // hub 已撤过或从没收过，正常
		}
		if err != nil {
			return false, "标撤: " + err.Error()
		}
		return true, ""
	}
	return false, "不认识的操作"
}

// expireTrusts 把到期的放行标撤、逐条审计 trust_expired；有到期的就通知网页。守护进程自己也会按到期时间失效，不推快照。
func (s *Server) expireTrusts() {
	gone, err := s.store.ExpireTrusts()
	if err != nil {
		s.log.Printf("清理到期的放行: %v", err)
		return
	}
	for _, t := range gone {
		s.audit(t.MachineID, "system", "", "trust_expired", trustDetail(t))
	}
	if len(gone) > 0 {
		s.broadcastTrusts()
	}
}

// sweepTrusts 启动时清一次，之后每分钟清一次，ctx 结束时返回。
func (s *Server) sweepTrusts(ctx context.Context) {
	s.expireTrusts()
	t := time.NewTicker(trustSweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.expireTrusts()
		}
	}
}

// trustView 是 GET /api/trusts 的一项：放行本身加机器名、项目名（项目根的末段）与剩余秒数。
type trustView struct {
	proto.Trust
	MachineName      string `json:"machine_name"`
	ProjectName      string `json:"project"`
	RemainingSeconds int64  `json:"remaining_seconds"`
}

// handleTrusts 按到期先后列生效中的放行（不含已吊销机器的）。没有时是空数组。
func (s *Server) handleTrusts(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListTrusts("")
	if err != nil {
		s.internalErr(w, "列放行", err)
		return
	}
	machines := map[string]*store.Machine{}
	now := time.Now()
	out := make([]trustView, 0, len(list))
	for _, t := range list {
		m, ok := machines[t.MachineID]
		if !ok {
			if got, err := s.store.GetMachine(t.MachineID); err == nil {
				m = &got
			} else if !errors.Is(err, store.ErrNotFound) {
				s.log.Printf("读机器 %s: %v", t.MachineID, err)
			}
			machines[t.MachineID] = m
		}
		name := ""
		if m != nil {
			if m.Revoked {
				continue
			}
			name = m.Name
		}
		out = append(out, trustView{Trust: t, MachineName: name, ProjectName: projectName(t.Project),
			RemainingSeconds: int64(t.Until.Sub(now).Round(time.Second) / time.Second)})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTrustRevoke 撤销一条放行：204；没有或已撤 404。
func (s *Server) handleTrustRevoke(w http.ResponseWriter, r *http.Request) {
	err := s.revokeTrust(r.PathValue("id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "没有这条放行")
	case err != nil:
		s.internalErr(w, "撤销放行", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
