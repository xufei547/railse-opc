# Railse OPC v0.3 角色：机器侧实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**目标：** 实现规格 `docs/superpowers/specs/2026-11-11-railse-opc-v0.3-roles-design.md` 的机器侧：角色文件（§2）、绑定与判定（§3）、命令行（§4）、守护进程一侧的 hub 契约（§5），以及 §8 里 policy、agent、cmd/railse 的测试。

**架构：** 角色的解析、校验与判定进 `internal/policy`（不做 IO），判定仍只有 `Engine.Evaluate` 一个入口，角色经 `Context.Role` 传入。`internal/agent` 新增 `Roles`（读 `roles.yaml`、按 mtime 与大小重读、坏文件 fail closed）与纯函数 `ResolveRole`（守护进程与命令行回退共用）；`Server` 在每条审批、答题、事件前解析会话角色并改写 `Session.Role`；`HubClient` 在 hello 里声明 `FeatureRole` 并带角色摘要，重载后发 `roles` 帧。`cmd/railse role` 四个子命令经本机 socket 的 `role_list`、`role_which` 管理命令问守护进程，守护进程不在时直接读文件。

**技术栈：** Go 1.26、gopkg.in/yaml.v3（KnownFields 严格解析）、doublestar v4。

## 全局约束

- 用户 deny > forbid > ceiling > 规则；ceiling 压过 user allow；越界跳过限时放行与 `RAILSE_TRUST`；坏的 `roles.yaml` fail closed（上限按 read）；`roles.yaml` 自身的写入按 destructive 定级；文件不存在时与 0.2.3 完全一致（现有测试不改语义）。
- `policy` 包不做 IO；`Risk` 零值是最宽松的 read，新字段不留未赋值的 Risk（`Role.Ceiling` 用 `*Risk`，nil 为不设）。
- 契约字段（`proto.Session.Role`、`proto.RoleSummary`、`wire.FeatureRole`、`wire.Hello.Roles`、`wire.Roles`、`wire.PendingRequest.BeyondRole`）形状不改。
- 只动：`internal/policy`、`internal/agent`、`internal/adapter`、`internal/doctor`、`internal/proto`（只加本机 socket 用的新字段）、`cmd/railse`、`internal/i18n/en.go`（新词条加在所属类别末尾）、`docs/agent-local-loop.md`、本计划。
- 测试一律用临时目录；不跑连真实守护进程的 `railse`。
- 歧义时取更保守的做法，最终汇报列出取舍。

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/policy/roles.go`（新） | `Role`、`RoleSet`、`TrustLimit`、`ParseRoleSet`、`Validate`、`Covers`、`BrokenRole`、`Match.Brief`、`Summary` |
| `internal/policy/rules.go` | 把 `Match` 的共用校验抽成 `Match.validate()`，`Rule.validate` 调它（语义不变） |
| `internal/policy/engine.go` | `Decision` 加 `Role`、`BeyondRole`、`Forbidden`；`Evaluate` 新顺序 |
| `internal/policy/payload.go` | `Context` 加 `Role *Role`；`Root()` 加地盘判断 |
| `internal/agent/roles.go`（新） | `Roles` 文件存储（mtime/大小重读、坏文件状态、变更回调）、`ResolveRole`、`RoleTemplate` |
| `internal/agent/server.go` | 解析角色、改写 `Session.Role`、Forbidden / BeyondRole 分支、`role_list` / `role_which`、reload 重读角色 |
| `internal/agent/trust.go` | `match` 加角色的 `trust_max` 过滤 |
| `internal/agent/audit.go`、`requests.go` | `AuditEntry.Role`、`BeyondRole`；`PendingRequest.BeyondRole` |
| `internal/agent/hubclient.go` | `SyncRoles`、hello 的 `FeatureRole` 与 `Roles`、`roles` 帧发送循环、`toWire` 带 `BeyondRole` |
| `internal/agent/config.go` | `Config.RolesPath` |
| `internal/proto/proto.go` | `Control.Role`、`Control.Cwd`；`ControlReply.Roles`、`RolesPath`、`RolesError`、`Which`；`RoleWhich` |
| `cmd/railse/role.go`（新） | `railse role list|check|which|init` |
| `cmd/railse/hook.go` | 填 `Session.Role = $RAILSE_ROLE`；角色理由本地化 |
| `cmd/railse/main.go`、`agent.go` | 命令表一行；守护进程接上 `Roles` 与 `SyncRoles` |
| `internal/doctor/agent.go` | `agent.roles` 检查 |
| `internal/i18n/en.go` | 新词条 |
| `docs/agent-local-loop.md` | “角色（0.3 起）”一节 |

---

### Task 1：policy 角色文件解析与校验

**Files:** Create `internal/policy/roles.go`、`internal/policy/roles_test.go`；Modify `internal/policy/rules.go`

**Produces:**
```go
type TrustLimit struct{ Set, None bool; D time.Duration }      // 不写：Set=false；none：Set、None；时长：Set、D
type Role struct {
    Name string `yaml:"name"`; Note string `yaml:"note,omitempty"`; Dirs []string `yaml:"dirs,omitempty"`
    Ceiling *Risk `yaml:"ceiling,omitempty"`; Forbid []Match `yaml:"forbid,omitempty"`; TrustMax TrustLimit `yaml:"trust_max,omitempty"`
    // 未导出：compiled []compiledRule（ParseRoleSet 预编译）、broken string（BrokenRole）
}
type RoleSet struct{ Version int `yaml:"version"`; Roles []Role `yaml:"roles"` }
const RoleSetVersion = 1
func ParseRoleSet(data []byte) (RoleSet, error)   // 严格：未知字段、多文档都报错
func (rs RoleSet) Validate() error
func (rs RoleSet) Find(name string) *Role
func (r *Role) Covers(dir, home string) bool        // 同 scope.project：模式展开 ~ 后等于 dir 或 doublestar 命中
func BrokenRole(err error) *Role                   // 上限 read、理由 roles.yaml 有误：<err>
func (r *Role) Broken() string
func (m Match) Brief() string                      // 一条 forbid 的简述：command_prefix "fly "、payload mcp
func (r *Role) Summary() proto 无关：RoleSummaryFields（agent 里转 proto.RoleSummary）
```

- [ ] 表驱动测试：合法样例（规格里的三个角色）、未知字段（顶层、角色、forbid 条目）、重名、名字空/超 32 字符/含空白或控制字符、note 超 120 或含换行、坏 ceiling、空 forbid 条目、坏正则、坏 path_glob、坏 payload、trust_max 坏格式 / 4m / 25h / `none` / `2h`、version 不是 1、空文件、多文档、dirs 坏模式或空串。
- [ ] 跑 `go test ./internal/policy -run Role`，确认失败。
- [ ] 实现；`Rule.validate` 改调 `Match.validate()`（payload、path_glob、command_regex 三项），跑现有 policy 测试确认不变。
- [ ] 提交：`policy: 角色文件解析与校验（roles.yaml）`

### Task 2：policy 判定顺序与地盘

**Files:** Modify `internal/policy/engine.go`、`payload.go`；Create `internal/policy/roles_engine_test.go`

`Evaluate` 的新顺序（`ctx.normalized()` 已含地盘：角色写了 dirs 而项目根不在其内时项目根按空）：

```go
// 1 定级；2 用户 deny → deny；3 forbid → deny, Forbidden；
// 3' 其余来源的 deny 规则仍是 deny（比 ceiling 的 ask 更严，保守取舍）；
// 4 风险 > ceiling（坏文件按 read）→ ask, BeyondRole（暂停时同样 ask）；5 暂停 → ask；6 规则 / 底线 / 默认 ask。
// 有角色时 Decision.Role = 角色名。
```

- [ ] 测试：用户 deny 压 forbid；forbid 压 user allow；ceiling 压 user allow（含 `risk: destructive` 的 allow）；ceiling 内照旧走规则；暂停下 forbid 仍拒、用户 deny 仍拒；坏文件角色：read 照旧放行、write_in_project 越界、理由带“roles.yaml 有误”；地盘内写项目内文件是 write_in_project，地盘外同一请求是 write_outside；没写 dirs 的角色不改项目根；`~/.railse/roles.yaml` 的写入按 destructive；`Role` 为 nil 时与原来逐条相同（复用现有测试即证）。
- [ ] 实现，跑 `go test ./internal/policy`。
- [ ] 提交：`policy: Evaluate 加入角色（forbid、ceiling、地盘）`

### Task 3：agent 角色存储与解析

**Files:** Create `internal/agent/roles.go`、`internal/agent/roles_test.go`；Modify `config.go`

```go
type Roles struct{ … }                                  // NewRoles(path, home string, logger *log.Logger) *Roles
func (r *Roles) Current() RolesState                     // 先 stat 比对 mtime 与大小，变了重读；成功重读后调 onChange
type RolesState struct{ Exists bool; Set policy.RoleSet; Err error }
func (r *Roles) SetChanged(fn func(RolesState))
func ResolveRole(st RolesState, env, root, cwd, home string) (role *policy.Role, why RoleWhy)
type RoleWhy struct{ Kind string /* env|dir|none|broken|absent */; Pattern string; EnvUnknown bool }
func Summaries(set policy.RoleSet) []proto.RoleSummary   // trust_max none → -1，不设 → 0
func RoleTemplate(lang i18n.Language) []byte              // 起步模板，dirs 全部注释掉
```

- [ ] 测试：不存在 → Exists=false；合法 → Set；坏文件 → Err；改文件（mtime/大小变）后重读；坏 → 修好恢复；删除 → 回到不存在；`ResolveRole` 环境变量命中、环境变量不存在走目录、目录按先后取第一、无项目根按 cwd、坏文件给 BrokenRole；模板能被 `ParseRoleSet` 解析且没有角色带 dirs。
- [ ] 实现，跑测试，提交：`agent: roles.yaml 的读取、重读与角色解析`

### Task 4：守护进程判定路径

**Files:** Modify `server.go`、`trust.go`、`audit.go`、`requests.go`；Create `internal/agent/roles_server_test.go`

- `evaluate` 解析角色、改写 `req.Session.Role`，`pctx.Role` 传给引擎，上报与限时放行用 `pctx.Root()`（含地盘）。
- `Forbidden`：审计 `auto_denied`，`DecidedBy` `role:<name>`，回 deny。
- `BeyondRole`：不建 `RAILSE_TRUST`、不匹配放行；PreToolUse 一律回 ask（保守：不交 Claude 自己的分类器）；PermissionRequest 送 Decider，`PendingRequest.BeyondRole=true`。
- 放行命中再看 `trust_max`：`none` 不放行；设了时长时 `CreatedAt + trust_max` 之后不算。
- 答题与事件只带角色名；`RAILSE_ROLE` 指不存在的名字时每个会话只记一行日志。
- `reload` 管理命令同时重读角色。

- [ ] 测试（真 socket + 临时目录，参照 `server_test.go` 的 `startServer`）：环境变量绑定、目录绑定、不存在的名字（日志一次）、重叠取先；forbid 拒绝与审计字段；越界跳过会话放行与 `RAILSE_TRUST`；`trust_max` 2h 对 3 小时前建的放行无效、`none` 无效；坏文件 fail closed、修好后恢复；mtime 重载；答题、事件带角色；没有 roles.yaml 时审计里没有 role 字段。
- [ ] 实现，跑 `go test ./internal/agent`，提交：`agent: 审批按角色判定（forbid、越界、trust_max）`

### Task 5：hub 契约（守护进程一侧）

**Files:** Modify `hubclient.go`、`server.go`；Test `internal/agent/roles_hub_test.go`

- `SyncRoles(s *Server)`：hello 声明 `FeatureRole`，带 `Summaries`；角色重读成功时若这条连接的 welcome 声明了 `FeatureRole`，发一帧 `wire.Roles{T: "roles"}`（后台循环只发最新一份）。
- `toWire` 带 `BeyondRole`。

- [ ] 测试（假 hub，参照 `hubclient_test.go`）：hello 的 features 含 role、roles 摘要正确；改 roles.yaml 后下一条审批触发 roles 帧；hub 没声明 role 时不发；越界请求的 pending 帧 beyond_role 为真。
- [ ] 实现，跑测试，提交：`agent: hello 带角色摘要，重载后发 roles 帧`

### Task 6：命令行、钩子与 doctor

**Files:** Create `cmd/railse/role.go`、`cmd/railse/role_test.go`；Modify `hook.go`、`main.go`、`agent.go`、`internal/proto/proto.go`、`internal/doctor/agent.go`、`internal/doctor/doctor_test.go`、`internal/i18n/en.go`

- `railse role list|check|which|init [--dir]`；`list`、`which` 先走 socket（`role_list`、`role_which`），连不上时读文件并注明“守护进程没在运行，直接读文件”。退出码：成功 0、用法错 2、文件有误或 init 时已存在 1。
- 钩子：`in.Session.Role = os.Getenv("RAILSE_ROLE")`；`localizeReason` 与 `askReason` 认出角色理由并按终端语言重拼。
- doctor：`agent.roles` 不存在 skip、合法 ok、坏 fail。

- [ ] 测试：四个子命令的输出与退出码（含守护进程不在时的回退、起了测试守护进程时走 socket）；钩子把 `RAILSE_ROLE` 填进请求；英文界面下角色理由译出；doctor 三种状态。
- [ ] 实现，跑测试，提交：`cmd: railse role 子命令、钩子带 RAILSE_ROLE、doctor 检查 roles.yaml`

### Task 7：文档与全量验证

- [ ] `docs/agent-local-loop.md` 加“角色（0.3 起）”一节。
- [ ] `go build ./...`、`RAILSE_LANG=zh go test ./... -race`、`make lint`、`make gofmt` 全过。
- [ ] 提交：`docs: 本地闭环加角色一节`
