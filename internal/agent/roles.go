package agent

import (
	"errors"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// RolesState 是某一刻 roles.yaml 的状态。Exists 为假表示文件不存在（没有角色，与 0.2 完全一样）；Err 非空表示文件有误
// （读不了、解析或校验失败），这时 Set 为空，会话一律按 policy.BrokenRole 判定（fail closed）。
type RolesState struct {
	Exists bool
	Set    policy.RoleSet
	Err    error
}

// rolesStamp 是判断文件变没变的依据：存在与否、mtime 与大小（设计 §2：每次判定前比对，文件小，开销可忽略）。
type rolesStamp struct {
	exists bool
	mtime  time.Time
	size   int64
	errMsg string // stat 本身失败（权限等）时的错误，变了也算变
}

// Roles 持有 roles.yaml 的当前状态，并发安全。每次 Current 都先 stat 一次，mtime 或大小变了就重读。
type Roles struct {
	path string

	mu     sync.Mutex
	loaded bool
	stamp  rolesStamp
	state  RolesState

	changed atomic.Pointer[func(RolesState)]
}

// NewRoles 只记下路径，第一次 Current 时才读。
func NewRoles(path string) *Roles { return &Roles{path: path} }

// Path 返回 roles.yaml 的路径。
func (r *Roles) Path() string { return r.path }

// SetChanged 装上重读成功后的回调（hub 客户端发 roles 帧用）；nil 表示不通知。重读成功指文件变了且读出合法内容，
// 包括文件被删掉（变成没有角色）；第一次读、读到坏文件都不通知（坏文件时 hub 那边保留上一份摘要，规格 §2）。
// fn 在调用 Current 的 goroutine 里同步调用、不持锁，必须不阻塞。
func (r *Roles) SetChanged(fn func(RolesState)) {
	if fn == nil {
		r.changed.Store(nil)
		return
	}
	r.changed.Store(&fn)
}

// Current 返回当前状态，文件变了先重读。
func (r *Roles) Current() RolesState {
	st, notify := r.refresh(false)
	if notify {
		if fn := r.changed.Load(); fn != nil {
			(*fn)(st)
		}
	}
	return st
}

// Reload 不看 mtime 强制重读（reload 管理命令用），返回新状态。
func (r *Roles) Reload() RolesState {
	st, notify := r.refresh(true)
	if notify {
		if fn := r.changed.Load(); fn != nil {
			(*fn)(st)
		}
	}
	return st
}

func (r *Roles) refresh(force bool) (RolesState, bool) {
	stamp := statRoles(r.path)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded && !force && stamp == r.stamp {
		return r.state, false
	}
	first := !r.loaded
	r.loaded, r.stamp = true, stamp
	st := readRoles(r.path, stamp)
	r.state = st
	return st, !first && st.Err == nil
}

func statRoles(path string) rolesStamp {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return rolesStamp{}
	case err != nil:
		return rolesStamp{exists: true, errMsg: err.Error()}
	}
	return rolesStamp{exists: true, mtime: fi.ModTime(), size: fi.Size()}
}

func readRoles(path string, stamp rolesStamp) RolesState {
	if !stamp.exists {
		return RolesState{}
	}
	if stamp.errMsg != "" {
		return RolesState{Exists: true, Err: errors.New(stamp.errMsg)}
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return RolesState{} // stat 与读之间被删掉了
	}
	if err != nil {
		return RolesState{Exists: true, Err: err}
	}
	set, err := policy.ParseRoleSet(data)
	if err != nil {
		return RolesState{Exists: true, Err: err}
	}
	return RolesState{Exists: true, Set: set}
}

// LoadRolesFile 读一次 roles.yaml（命令行在守护进程没起时用）。
func LoadRolesFile(path string) RolesState { return readRoles(path, statRoles(path)) }

// 会话角色是怎么定下来的（RoleWhy.Kind）。
const (
	RoleAbsent = "absent" // 没有 roles.yaml
	RoleBroken = "broken" // roles.yaml 有误，按 BrokenRole
	RoleByEnv  = "env"    // RAILSE_ROLE 指名的角色
	RoleByDir  = "dir"    // 项目根（没有时 cwd）命中某个角色的 dirs
	RoleNone   = "none"   // 有角色文件，但没有命中：未分配
)

// RoleWhy 说明会话角色的由来：Kind 见上；Pattern 与 Dir 是按目录绑定时命中的模式与被比较的目录；
// EnvUnknown 为真表示 RAILSE_ROLE 指了一个不存在的名字（之后按目录走）。
type RoleWhy struct {
	Kind       string
	Pattern    string
	Dir        string
	EnvUnknown bool
}

// ResolveRole 按设计 §3.1 定会话的角色：文件有误 → BrokenRole（不认 RAILSE_ROLE：文件里的名字已不可信）；没有文件 → nil；
// env 是存在的角色名 → 它；否则按项目根（root 为空时按 cwd）匹配各角色的 dirs，文件顺序第一个命中的；都没有 → nil（未分配）。
// root 应是守护进程算出、经 policy.Context.Root 规范化（不含角色地盘那一步）的项目根。纯函数，守护进程与命令行回退共用。
func ResolveRole(st RolesState, env, root, cwd, home string) (*policy.Role, RoleWhy) {
	if st.Err != nil {
		return policy.BrokenRole(st.Err), RoleWhy{Kind: RoleBroken}
	}
	if !st.Exists {
		return nil, RoleWhy{Kind: RoleAbsent}
	}
	why := RoleWhy{Kind: RoleNone}
	if env != "" {
		if r := st.Set.Find(env); r != nil {
			return r, RoleWhy{Kind: RoleByEnv}
		}
		why.EnvUnknown = true
	}
	dir := root
	if dir == "" {
		dir = cwd
	}
	for i := range st.Set.Roles {
		r := &st.Set.Roles[i]
		if p := r.CoveringDir(dir, home); p != "" {
			why.Kind, why.Pattern, why.Dir = RoleByDir, p, dir
			return r, why
		}
	}
	return nil, why
}

// RoleSummaries 是报给 hub 的角色摘要（hello 与 roles 帧），按文件顺序；没有角色返回 nil（hello 里省略）。
func RoleSummaries(set policy.RoleSet) []proto.RoleSummary {
	if len(set.Roles) == 0 {
		return nil
	}
	out := make([]proto.RoleSummary, 0, len(set.Roles))
	for _, r := range set.Roles {
		s := proto.RoleSummary{Name: r.Name, Note: r.Note, ForbidCount: len(r.Forbid), TrustMaxMins: r.TrustMax.Minutes(), Dirs: slices.Clone(r.Dirs)}
		if r.Ceiling != nil {
			s.Ceiling = r.Ceiling.String()
		}
		out = append(out, s)
	}
	return out
}

// trustAllowedFor 判断限时放行 tr 能不能用在角色 role 的请求上（设计 §3.2）：trust_max 为 none 时一律不能；设了时长时，
// 只在 CreatedAt + trust_max 之前能用。没有角色、没设 trust_max 时照旧。CreatedAt 缺失（不该发生）按不能用，偏保守。
func trustAllowedFor(role *policy.Role, tr proto.Trust, now time.Time) bool {
	if role == nil || !role.TrustMax.Set {
		return true
	}
	if role.TrustMax.None || tr.CreatedAt.IsZero() {
		return false
	}
	return now.Before(tr.CreatedAt.Add(role.TrustMax.D))
}

// RoleTemplate 是 railse role init 写出的起步模板（开发 / 写作 / 运维；英文界面为 dev / writer / ops）。
// dirs 全部注释掉：写下模板不改变任何会话，只有用 RAILSE_ROLE 指名或自己填上 dirs 之后才生效。
func RoleTemplate(en bool) []byte {
	if en {
		return []byte(roleTemplateEN)
	}
	return []byte(roleTemplateZH)
}

const roleTemplateZH = `# Railse OPC 角色文件（0.3 起）。一个角色是一道权限边界；会话靠环境变量 RAILSE_ROLE 或项目目录绑定角色。
# 改完不用重启：守护进程每次判定前看文件变没变。写错了所有审批按“只读之外一律问人”处理，railse role check 可以先查。
#
# 字段：
#   name       角色名，1～32 个字符，不含空白
#   note       一行说明，手机总台上看得到
#   dirs       路径模式（~ 与 ** 通配，同规则的 scope.project）：按目录绑定，也划定角色的地盘（地盘外的写入一律算项目外）
#   ceiling    风险上限：read、write_in_project、unknown、write_outside、network、outward、destructive；超过的一律问人
#   forbid     禁用清单，写法同规则的 match；命中直接拒，不进手机
#   trust_max  限时放行最长多久（5m～24h），none 表示这个角色从不走限时放行
version: 1
roles:
  - name: 开发
    note: 写代码、跑测试
    # dirs: ["~/code/**"]
    ceiling: network
    trust_max: 24h
  - name: 写作
    note: 只改文档
    # dirs: ["~/code/*/docs"]
    ceiling: write_in_project
    forbid:
      - {command_prefix: "git push"}
      - {payload: mcp}
    trust_max: 2h
  - name: 运维
    note: 发版、部署、装机
    ceiling: destructive
    trust_max: none
`

const roleTemplateEN = `# Railse OPC roles file (0.3+). A role is a permission boundary; a session gets its role from the RAILSE_ROLE
# environment variable or from its project directory.
# No restart needed: the agent checks the file before every decision. If the file is broken, everything except reads
# goes to a human; run railse role check first.
#
# Fields:
#   name       role name, 1-32 characters, no spaces
#   note       one line, shown on the phone's Desk
#   dirs       path patterns (~ and ** globs, same as a rule's scope.project): bind sessions by directory, and mark the
#              role's turf (writes outside it count as outside the project)
#   ceiling    risk ceiling: read, write_in_project, unknown, write_outside, network, outward, destructive; anything above asks you
#   forbid     forbidden list, written like a rule's match; a hit is denied outright and never reaches the phone
#   trust_max  longest a timed trust may apply (5m-24h); none means this role never uses timed trust
version: 1
roles:
  - name: dev
    note: write code, run tests
    # dirs: ["~/code/**"]
    ceiling: network
    trust_max: 24h
  - name: writer
    note: docs only
    # dirs: ["~/code/*/docs"]
    ceiling: write_in_project
    forbid:
      - {command_prefix: "git push"}
      - {payload: mcp}
    trust_max: 2h
  - name: ops
    note: release, deploy, install
    ceiling: destructive
    trust_max: none
`

// roleName 是审计与上报用的角色名：nil 与坏文件角色都是空串（未分配）。
func roleName(r *policy.Role) string {
	if r == nil {
		return ""
	}
	return r.Name
}
