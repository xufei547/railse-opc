package agent

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
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
	// Gen 是这份状态的代数：每次内容（或读的结果）变了加一，单调递增。通知按代数顺序送达（见 SetChanged）。
	Gen uint64
}

// rolesKey 是判断文件变没变的依据：存在与否、读的错误与全文的哈希。每次判定都读全文比较（文件很小）：只比 mtime 与大小时，
// 内容变了而两者不变、或文件变得读不了，都会继续沿用旧的角色。
type rolesKey struct {
	exists bool
	errMsg string
	sum    [sha256.Size]byte
}

// Roles 持有 roles.yaml 的当前状态，并发安全。每次 Current 都读一次全文，内容（或读的结果）变了就重新解析。
// 重读与变更通知在同一把锁里串行进行，通知按代数顺序送达，roles 帧不会把旧摘要发在新摘要之后。
type Roles struct {
	path string

	mu     sync.Mutex
	loaded bool
	key    rolesKey
	state  RolesState

	changed atomic.Pointer[func(RolesState)]
}

// NewRoles 只记下路径，第一次 Current 时才读。
func NewRoles(path string) *Roles { return &Roles{path: path} }

// Path 返回 roles.yaml 的路径。
func (r *Roles) Path() string { return r.path }

// SetChanged 装上重读成功后的回调（hub 客户端发 roles 帧用）；nil 表示不通知。重读成功指内容变了且读出合法内容，
// 包括文件被删掉（变成没有角色）；第一次读、读到坏文件都不通知（坏文件时 hub 那边保留上一份摘要，规格 §2）。
// fn 在调用 Current 的 goroutine 里、持着 Roles 的锁同步调用（为了按代数顺序送达），必须不阻塞，也不能回调 Roles。
func (r *Roles) SetChanged(fn func(RolesState)) {
	if fn == nil {
		r.changed.Store(nil)
		return
	}
	r.changed.Store(&fn)
}

// Current 返回当前状态，文件内容变了先重读。
func (r *Roles) Current() RolesState { return r.refresh(false) }

// Reload 不看内容变没变强制重新解析（reload 管理命令用），返回新状态。
func (r *Roles) Reload() RolesState { return r.refresh(true) }

func (r *Roles) refresh(force bool) RolesState {
	r.mu.Lock()
	defer r.mu.Unlock()
	data, key := readRolesKey(r.path)
	if r.loaded && !force && key == r.key {
		return r.state
	}
	first := !r.loaded
	r.loaded, r.key = true, key
	st := parseRoles(data, key)
	st.Gen = r.state.Gen + 1
	r.state = st
	if !first && st.Err == nil {
		if fn := r.changed.Load(); fn != nil {
			(*fn)(st)
		}
	}
	return st
}

// readRolesKey 读全文。不存在返回 exists 为假；其余读错误（权限、是目录等）记在 errMsg，按有误处理。
func readRolesKey(path string) ([]byte, rolesKey) {
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, rolesKey{}
	case err != nil:
		return nil, rolesKey{exists: true, errMsg: err.Error()}
	}
	return data, rolesKey{exists: true, sum: sha256.Sum256(data)}
}

func parseRoles(data []byte, key rolesKey) RolesState {
	if !key.exists {
		return RolesState{}
	}
	if key.errMsg != "" {
		return RolesState{Exists: true, Err: errors.New(key.errMsg)}
	}
	set, err := policy.ParseRoleSet(data)
	if err != nil {
		return RolesState{Exists: true, Err: err}
	}
	return RolesState{Exists: true, Set: set}
}

// LoadRolesFile 读一次 roles.yaml（命令行在守护进程没起时用）。
func LoadRolesFile(path string) RolesState { return parseRoles(readRolesKey(path)) }

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

// RolesInfoOf 把角色文件的状态编成 role_list、role_which 的答复（守护进程与命令行回退共用）。
func RolesInfoOf(path string, st RolesState) *proto.RolesInfo {
	info := &proto.RolesInfo{Path: path, Exists: st.Exists, Roles: RoleSummaries(st.Set)}
	if st.Err != nil {
		info.Error = st.Err.Error()
	}
	return info
}

// WhichRole 说明目录 cwd（与 RAILSE_ROLE 的值 env）会解析成哪个角色、凭什么，口径与守护进程判定时相同：cwd 先清理（解析符号链接），
// 项目根按 ResolveProjectRoot 定并经 policy.Context.Root 规范化；ProjectRoot 是再按角色地盘处理后的生效项目根。
func WhichRole(st RolesState, env, cwd, home string) proto.RoleWhich {
	w := proto.RoleWhich{Env: env}
	clean := CleanCwd(cwd)
	root := ""
	if filepath.IsAbs(clean) {
		root, _ = ResolveProjectRoot(clean)
	} else {
		clean = ""
	}
	norm := policy.Context{ProjectRoot: root, HomeDir: home}.Root()
	role, why := ResolveRole(st, env, norm, clean, home)
	w.Role, w.Kind, w.Pattern, w.Dir, w.EnvUnknown = roleName(role), why.Kind, why.Pattern, why.Dir, why.EnvUnknown
	w.ProjectRoot = policy.Context{ProjectRoot: norm, HomeDir: home, Role: role}.Root()
	w.OutsideDirs = role != nil && len(role.Dirs) > 0 && norm != "" && w.ProjectRoot == ""
	return w
}
