package hub

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/proto"
)

// 角色（0.3 起）在 hub 这一侧：守护进程是判定者，hub 只存、聚合、展示。守护进程在 hello 与 roles 帧里报 roles.yaml 的角色摘要，
// hub 整份替换这台机器的角色表（machine_roles）；会话上报时带生效的角色名（proto.Session.Role），越界的审批带 BeyondRole。
// 两样都只认声明了 FeatureRole 的守护进程的（越界标记除外，见 onPending），老守护进程接入时一切照旧，总台只有“未分配”。

// tRoles 是守护进程发的角色摘要帧的种类（wire.Roles 的 t）。帧种类名定义在这里而不是 wire：机器侧分支可能也要加这个常量，合并时统一。
const tRoles = "roles"

// 守护进程报来的角色摘要在 hub 存储前的上限。
const (
	maxRoles       = 64  // 一台机器至多这么多个角色，多的丢弃
	maxRoleNameLen = 32  // 角色名的字符上限（同规格：1～32 个字符）
	maxRoleNoteLen = 120 // 备注的字符上限
	maxRoleDirs    = 32  // 每个角色至多这么多条目录模式
)

// has 报告这条连接的守护进程在 hello 里声明了能力 f。
func (c *agentConn) has(f string) bool { return slices.Contains(c.features, f) }

// validRoleName 报告角色名是否合规：1～32 个字符，合法 UTF-8，不含空白、控制字符与不可见的格式字符（ZWNJ、ZWJ 除外，同 ValidName）。
func validRoleName(s string) bool {
	if s == "" || !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxRoleNameLen || !ValidName(s) {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsSpace)
}

// sessionRole 是 hub 记下的会话角色：连接的守护进程声明了 role、且名字合规时取它报来的生效角色名，否则为空串（未分配）。
func sessionRole(c *agentConn, sess proto.Session) string {
	if c == nil || !c.has(wire.FeatureRole) || !validRoleName(sess.Role) {
		return ""
	}
	return sess.Role
}

// cleanRoles 给守护进程报来的角色摘要兜底，返回副本：名字不合规的、重名的（留第一个）丢弃，至多 maxRoles 个；备注脱敏后截断，
// 上限截断；禁用条数为负按 0；放行上限小于 -1 按 0（-1 是“不允许限时放行”）；目录去掉空的与超长的（截断会改变模式的意思），至多 maxRoleDirs 条。
func cleanRoles(in []proto.RoleSummary) []proto.RoleSummary {
	out := make([]proto.RoleSummary, 0, min(len(in), maxRoles))
	seen := map[string]bool{}
	for _, r := range in {
		if len(out) == maxRoles {
			break
		}
		if !validRoleName(r.Name) || seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		r.Note = redactText(r.Note, maxRoleNoteLen)
		r.Ceiling = truncate(r.Ceiling, maxFieldLen)
		r.ForbidCount = max(r.ForbidCount, 0)
		if r.TrustMaxMins < -1 {
			r.TrustMaxMins = 0
		}
		var dirs []string
		for _, d := range r.Dirs {
			if d == "" || len(d) > maxPathLen {
				continue
			}
			if len(dirs) == maxRoleDirs {
				break
			}
			dirs = append(dirs, d)
		}
		r.Dirs = dirs
		out = append(out, r)
	}
	return out
}

// storeHelloRoles 按连接 c 的 hello 存这台机器的角色表：声明了 role 时整份替换为 hello 带的（兜底后），没声明时清空。
func (s *Server) storeHelloRoles(c *agentConn, declared bool, roles []proto.RoleSummary) {
	if !declared {
		roles = nil
	}
	s.replaceRoles(c, roles)
}

// onRoles 收守护进程重载 roles.yaml 后整份重发的角色摘要。没声明 role 的连接发来的不收（记日志）。
func (s *Server) onRoles(c *agentConn, f wire.Roles) {
	if !c.has(wire.FeatureRole) {
		s.log.Printf("机器 %s 没声明 role 能力却发来 roles 帧，不收", c.machine.ID)
		return
	}
	s.replaceRoles(c, f.Roles)
}

// replaceRoles 整份替换这台机器的角色表，只认当前连接：同一机器快速重连时，旧连接晚到的 hello 或 roles 帧不得覆盖新连接写下的。
// 在这台机器的锁（roleLocks）里再确认 c 仍是当前连接才写；新连接登记（顶替旧连接）之后，旧连接的写入一律丢弃（记日志）。
func (s *Server) replaceRoles(c *agentConn, roles []proto.RoleSummary) {
	unlock := s.roleLocks.lock(c.machine.ID)
	defer unlock()
	if !s.agents.current(c) {
		s.log.Printf("机器 %s 的角色摘要来自已不是这台机器的当前连接，丢弃", c.machine.ID)
		return
	}
	if err := s.store.ReplaceMachineRoles(c.machine.ID, cleanRoles(roles)); err != nil {
		s.log.Printf("存机器 %s 的角色表: %v", c.machine.ID, err)
	}
}
