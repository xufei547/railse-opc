# Railse OPC v0.3 hub 与总台 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**目标：** 实现 [v0.3 规格](../specs/2026-11-11-railse-opc-v0.3-roles-design.md) 里 hub 与网页的一侧：hub 收角色摘要与会话角色、存储 v7、请求视图带角色与越界、`GET /api/desk`、推送正文带角色；PWA 加总台页、收件箱按角色筛选、卡片与批准面板的角色与越界表现。

**架构：** 照限时放行（0.2.3）的先例自下而上：存储（schema、迁移、存取函数）→ hub 收帧与视图 → 新接口 → 网页纯函数（`desk.ts`、`decide.ts`）→ 页面组件。总台的口径全部在 hub 用 SQL 聚合算好，网页只展示；hub 不做目录模式匹配。

**技术栈：** Go 1.26（modernc.org/sqlite、coder/websocket）；Vite + React 19 + react-router 7；vitest；Playwright。

## 全局约束

- 契约字段（`proto.Session.Role`、`proto.RoleSummary`、`wire.FeatureRole`、`wire.Hello.Roles`、`wire.Roles`、`wire.PendingRequest.BeyondRole`）形状不改。
- 只动 `internal/hub/**`、`web/**`、`internal/i18n/en.go`（新词条加在所属类别末尾）、`docs/hub.md`、本计划文件；不动 `internal/policy`、`internal/agent`、`internal/proto`、`cmd/railse`、版本号、CHANGELOG。
- 迁移只加不改：v6 → v7，带迁移测试与 `testdata`。
- 老守护进程（不声明 `FeatureRole`）接入时一切照旧，总台只有“未分配”。
- `beyond_role` 的请求：`can_trust` 为假；decide 对带 `remember` 或 `trust` 的批准回 400；批准面板不出现这两个开关并原位说明原因。
- 总台口径按规格第 6 节逐字实现，各有测试。
- 页面沿用 `app.css` 变量与卡片、风险配色；深浅色过 `contrast.test.ts`；新字符串都有英文，过 `i18n.catalog`、`i18n.sync`。
- 测试只用临时目录，不碰真实的 `~/.railse*`、`~/.claude`、`~/.codex`、`~/.gemini`，不连本机正在运行的 hub 与守护进程。
- 提交信息、注释、文档用中文。

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/hub/store/schema.sql` | v7：`sessions.role`、`requests.role`、`requests.beyond_role`、新表 `machine_roles` |
| `internal/hub/store/store.go` | `SchemaVersion = 7`，迁移 7 |
| `internal/hub/store/testdata/schema_v6.sql` | 冻结的 v6 建表语句（迁移测试用） |
| `internal/hub/store/requests.go` | `Request.Role`、`Request.BeyondRole` 入库读回；`UpsertSession` 更新角色 |
| `internal/hub/store/roles.go`（新） | `MachineRole`、`ReplaceMachineRoles`、`ListMachineRoles` |
| `internal/hub/store/desk.go`（新） | 总台的聚合查询 `DeskCounts`、`DeskRecent`、`SessionRoles`、`ProjectRoles` |
| `internal/hub/roles.go`（新） | hub 收角色：`cleanRoles`、`applyRoles`、`onRoles`、会话角色的取舍 `sessionRole` |
| `internal/hub/desk.go`（新） | `GET /api/desk`：按口径组装 |
| `internal/hub/agents.go` | `hubFeatures` 加 `FeatureRole`；hello 后存角色表；分发 `roles` 帧 |
| `internal/hub/requests.go` | pending、审计补行、question 带角色与越界；Decide/propose 拒越界的记住与放行 |
| `internal/hub/api.go` | `can_trust` 计入越界；`errBeyondRole` → 400；注册 `/api/desk` |
| `internal/hub/pushsvc.go` | 推送正文的角色行 |
| `web/src/types.ts`、`api.ts` | 新字段与 `hub.desk()` |
| `web/src/desk.ts`（新） | 总台的纯函数：边界一行、分组、筛选解析与应用、跳转链接 |
| `web/src/decide.ts` | 越界时不能记住与放行（`beyondBlock`） |
| `web/src/pages/Desk.tsx`（新） | 总台页 |
| `web/src/pages/Inbox.tsx`、`App.tsx` | 筛选条、`/inbox` 路由、导航加总台 |
| `web/src/components/RequestCard.tsx`、`RequestAttrs.tsx`、`DecideSheet.tsx` | 角色名与越界表现 |
| `web/src/app.css`、`locales/en.ts` | 样式与英文 |
| `web/e2e/fakeagent/main.go`（新，`//go:build ignore`）、`web/e2e/role.spec.ts`（新） | e2e：假守护进程按契约发越界请求 |
| `docs/hub.md` | “角色与总台（0.3 起）”一节与 API |

## 已定的取舍（规格未写死处，取保守做法）

1. **会话角色只认声明了 `FeatureRole` 的守护进程**：没声明的连接上 `Session.Role` 一律当空串（规格第 5 节“守护进程声明了，它上报的会话才带生效的角色名”）。`beyond_role` 则无论声明与否都收：它只会让 hub 少给“记住”“放行”，方向是安全的。
2. **角色摘要 hub 侧兜底**：至多 64 个角色；名字去首尾空白后为空、超过 32 个字符、含控制字符的丢弃；同名只留第一个；备注脱敏并截到 120 字符；上限截到 `maxFieldLen`；每个角色至多 32 条目录、每条至多 `maxPathLen` 字节；`forbid_count` 负数按 0；`trust_max_mins` 小于 -1 按 0。
3. **“24 小时内”按 `created_at`** 判定（有索引；`decided_at` 对自动放行与拒绝与创建同刻）。
4. **`denied`、`auto_allowed` 只数 `kind = permission`**（提问没有拒绝与自动放行；webhook 不属于机器）。
5. **已吊销的机器不进总台**（它连不回来，与 `/api/trusts` 不列已吊销机器一致）；webhook 不进总台。
6. **`recent` 不限 24 小时**，就是该行最近 5 条 permission（任意状态）。
7. **角色名大小写敏感**、逐字节比较（与守护进程按名字找角色一致）。
8. **“未分配”行**：该机器 24 小时内有归入未分配的请求（任意种类）或有归入未分配的生效放行时出现；角色已从文件里删掉的请求与放行归入未分配。
9. **会话放行的角色**取 `sessions.role`（按机器、tool、cli_session_id 找会话），找不到会话算未分配；**项目放行**取这台机器上该 `project_root` 最近一条请求的 `role`，没有算未分配。
10. **决定接口**：`beyond_role` 的请求上，`allow` 带 `remember` 或 `trust` 回 400；`propose` 的 `allow` 也回 400（同一理由，网页本就不会调）。拒绝时“记住”照常（学一条拒绝规则只会更严）。
11. **网页实时**：WebSocket 事件不改（规格第 6 节）。机器上下线、角色表变化没有现成事件，总台页开着时另外每 30 秒重取一次；其余在收到任一帧、重连、回到前台时重取，节流 1 秒。
12. **收件箱路由**：现有收件箱在 `/`，新增 `/inbox` 指向同一页（规格的 `/inbox?role=…&machine=…` 能直接用），导航“收件箱”在两个路径上都高亮。筛选参数里 `role` 存在且为空串表示“未分配”。
13. **推送正文**：permission 与 ask 在摘要与“点开…”之间加一行，question 在问题后加一行：有角色时 `角色「写作」`，越界时 `超出角色「写作」的边界`（en：`Role "writer"`、`Beyond role "writer"`）；没有角色时与现在一字不差。
14. **e2e**：机器侧还没实现，真守护进程发不出越界请求；用 `web/e2e/fakeagent/main.go`（`//go:build ignore`，不进 `go build ./...`）按契约接入、报角色、发越界请求、等决定。规格里“从推送到批准”的推送在 e2e 环境不可测（没有推送服务），从请求到达手机开始。

---

### Task 1：存储 v7（schema、迁移、testdata、迁移测试）

**Files:** Modify `internal/hub/store/schema.sql`、`store.go`；Create `internal/hub/store/testdata/schema_v6.sql`；Test `internal/hub/store/migrate_test.go`

- [ ] **Step 1：冻结 v6** —— 把当前 `schema.sql` 原样复制为 `testdata/schema_v6.sql`（不含版本行）。
- [ ] **Step 2：写失败的测试**（`migrate_test.go`）：
  - `TestFreshDBIsLatest` 改为版本 7，并检查 `sessions.role`、`requests.role`、`requests.beyond_role`、`machine_roles.trust_max_mins` 存在。
  - `TestMigrateV6ToV7KeepsData`：`schema_v6.sql` + 各表一行数据（含 trusts、sessions、requests）建 v6 库 → `Open` → 版本 7；`dumpTables` 按 v6 的列比对前后逐字段相同；新列默认 `''` / `0`；`machine_roles` 为空表。
  - `TestMigratedV7SchemaMatchesFresh`：从 v6 迁移得到的 `sessions`、`requests`、`machine_roles` 与新库 `tableSchema` 一致。
- [ ] **Step 3：跑** `go test ./internal/hub/store -run 'Migrat|Fresh'` → 失败。
- [ ] **Step 4：实现**：schema.sql 头注释改版本 7；`sessions` 表尾加 `role TEXT NOT NULL DEFAULT ''`；`requests` 表尾加 `role TEXT NOT NULL DEFAULT ''`、`beyond_role INTEGER NOT NULL DEFAULT 0`；新表

```sql
CREATE TABLE IF NOT EXISTS machine_roles (
  machine_id     TEXT NOT NULL REFERENCES machines(id),
  position       INTEGER NOT NULL,
  name           TEXT NOT NULL,
  note           TEXT NOT NULL DEFAULT '',
  ceiling        TEXT NOT NULL DEFAULT '',
  forbid_count   INTEGER NOT NULL DEFAULT 0,
  trust_max_mins INTEGER NOT NULL DEFAULT 0,
  dirs           TEXT NOT NULL DEFAULT '[]',
  PRIMARY KEY (machine_id, name)
);
```

  `store.go`：`SchemaVersion = 7`，`migrations[7]` 是三条 `ALTER TABLE … ADD COLUMN` 加同样的 `CREATE TABLE machine_roles`。
- [ ] **Step 5：跑** `go test ./internal/hub/store` → 全过。
- [ ] **Step 6：提交** `存储 v7：会话与请求带角色、越界标记，新表 machine_roles（迁移 6 → 7）`

### Task 2：请求与会话的角色存取、角色表存取

**Files:** Modify `internal/hub/store/requests.go`；Create `internal/hub/store/roles.go`；Test `requests_test.go`、新 `roles_test.go`

**Produces：** `Request.Role string \`json:"role"\``、`Request.BeyondRole bool \`json:"beyond_role"\``；`UpsertSession` 每次把 `role` 更新为 `sess.Role`；`SessionRole(id string) (string, error)`；

```go
type MachineRole struct {
	MachineID string
	proto.RoleSummary
}
func (s *Store) ReplaceMachineRoles(machineID string, roles []proto.RoleSummary) error // 一个事务里删光再按 position 插
func (s *Store) ListMachineRoles() ([]MachineRole, error)                              // 按 machine_id、position
```

- [ ] **Step 1：失败测试**：插入带 Role/BeyondRole 的请求读回相同；`UpsertSession` 第二次用新角色（含空串）覆盖；`ReplaceMachineRoles` 两次调用后只剩第二份、顺序按 position、dirs 往返、传空切片清空、别的机器不受影响。
- [ ] **Step 2：跑失败** → **Step 3：实现**（`requestCols` 末尾加 `role, beyond_role`，scan/insert 对应；`UpsertSession` 的 `ON CONFLICT … role = excluded.role`）→ **Step 4：跑过** `go test ./internal/hub/store`
- [ ] **Step 5：提交** `store：请求与会话的角色存取，角色表整份替换`

### Task 3：hub 收角色（features、hello、roles 帧、会话角色、越界标记）

**Files:** Create `internal/hub/roles.go`、`roles_test.go`；Modify `agents.go`、`requests.go`、`api.go`

**Produces：** `hubFeatures()` = `[ask, trust, role]`；`agentConn.has(f string) bool`；`cleanRoles([]proto.RoleSummary) []proto.RoleSummary`（取舍 2）；`(s *Server) storeRoles(machineID string, declared bool, roles []proto.RoleSummary)`；`(s *Server) onRoles(c *agentConn, f wire.Roles)`；`sessionRole(c *agentConn, sess proto.Session) string`。

- [ ] **Step 1：失败测试**（假守护进程，沿用 `enrollMachine`、`dialAgent`、`pendingFrame`）：
  - welcome 声明 `role`；`/api/version` 的 features 含 `role`。
  - 声明 `role` 的 hello 带两个角色 → `ListMachineRoles` 两行顺序对；再发 `roles` 帧只剩新的一份；没声明 `role` 的重连 → 角色表清空。
  - 没声明 `role` 的连接发 `roles` 帧 → 不入库（记坏帧）。
  - `cleanRoles`：表驱动（空名、超长名、重名、超量、备注脱敏截断、目录截断、负数）。
  - pending 带 `Session.Role="写作"`、`BeyondRole=true`：声明了 role 时视图 `role=写作`、`beyond_role=true`、`can_trust=false`（即使声明了 trust）；没声明 role 时 `role=""`、`beyond_role=true`。
  - 审计补的 auto_allowed 行、question 行带会话的角色；`sessions.role` 随之更新。
- [ ] **Step 2：跑失败** → **Step 3：实现**：`readHello` 之后、welcome 之前（已过吊销核对）调 `storeRoles(m.ID, hello.Has(FeatureRole), hello.Roles)`；`handleAgentFrame` 加 `wire.TRoles` 分支（帧种类名 `"roles"` 定义在 hub 包里 `tRoles`，不改 `wire.go`：机器侧分支可能也要这个名字，两边都往 `wire.go` 加同名常量会冲突，合并时再由主会话统一）；`onPending` 用 `sessionRole` 改写 `sess.Role`、`pendingBody` 带 `BeyondRole`（只认 permission）；`cleanSession` 截断 Role；`onAudit` 改为接收 `*agentConn`（为了 `sessionRole`），`recordEvent`、`applyAgentFinal` 的行带 `Role`；`views` 里 `CanTrust` 加 `&& !r.BeyondRole`。
- [ ] **Step 4：跑过** `go test ./internal/hub -run 'Role|Trust|Agent|Pending|Audit'`，再全量 `go test ./internal/hub/...`
- [ ] **Step 5：提交** `hub：声明 role 能力，收 hello 与 roles 帧的角色摘要，请求与会话带角色、越界标记`

### Task 4：决定接口拒绝越界请求的“记住”与放行

**Files:** Modify `internal/hub/requests.go`、`api.go`；Test `roles_test.go`

**Produces：** `errBeyondRole = wrongKind("超出角色的边界：只能单次批准，不能记住，也不能限时放行")`（`Is(errBadAction)`，回 400）。

- [ ] **Step 1：失败测试**：越界请求 `allow+remember` 400 且请求仍 pending、规则数不变；`allow+trust` 400 且没有放行；`propose allow` 400；`deny+remember` 200 学到拒绝规则；单纯 `allow` 200、守护进程收到 allow。
- [ ] **Step 2：跑失败** → **Step 3：实现**：`Decide` 开头与 `propose` 开头：`action == allow && (remember || trust != nil)`（propose 只看 allow）时读请求，`BeyondRole` 就回 `errBeyondRole`。
- [ ] **Step 4：跑过** → **Step 5：提交** `hub：越界请求的批准不能记住、不能限时放行（400）`

### Task 5：总台接口 `GET /api/desk`

**Files:** Create `internal/hub/store/desk.go`、`internal/hub/desk.go`、`internal/hub/desk_test.go`；Modify `api.go`

**Produces（store）：**

```go
type DeskCount struct{ MachineID, Role string; Waiting, Denied, AutoAllowed, Recent24h int } // Recent24h：24 小时内的请求数（任意种类）
func (s *Store) DeskCounts(since time.Time) ([]DeskCount, error)
func (s *Store) DeskSessions(since time.Time) (map[[2]string]int, error)        // (machine, role) → 24 小时内有事件的会话数
func (s *Store) DeskRecent(perGroup int) ([]Request, error)                       // 每个 (machine, role) 最近 perGroup 条 permission
func (s *Store) SessionRoleByCLI(machineID, tool, cliSession string) (string, bool, error)
func (s *Store) LatestProjectRole(machineID, projectRoot string) (string, bool, error)
```

**Produces（hub）：** JSON 形状同规格第 6 节；`deskWindow = 24 * time.Hour`、`deskRecentMax = 5`；`recent` 项 `{id, tool_name, summary, risk, state, beyond_role, at}`，`summary` 用 `payloadSummary`（空时工具名），经 `clean(…, pushSummaryLen)`。

- [ ] **Step 1：失败测试**（直接往库里插请求、会话、放行、角色表，再 `GET /api/desk`）逐条钉口径：
  - 行 = 角色表的每一行（按机器、position）+ 未分配行出现条件（24 小时内有未分配请求 / 有未分配放行；两者都没有时不出现）。
  - `waiting` 只数 pending 的 permission 与 ask（不数 question、不受 24 小时限）。
  - `denied` = 24 小时内 denied + auto_denied；25 小时前的不算。`auto_allowed` 同理。
  - `sessions` = 24 小时内有事件的会话数（按 `sessions.role`）。
  - `trusts_active`：会话放行按会话当前角色；项目放行按该项目最近一条请求的角色；没有记录 → 未分配；已撤、已过期的不算。
  - 角色从文件删掉后，旧请求并进未分配。
  - `recent` 至多 5 条、新的在前、只含 permission、带 `beyond_role`，`summary` 已脱敏。
  - 老守护进程（不声明 role）：`roles` 为空、`unassigned` 有该机器。
  - 已吊销机器不出现；`online` 反映连接；要登录（401）。
- [ ] **Step 2：跑失败** → **Step 3：实现** → **Step 4：跑过**
- [ ] **Step 5：提交** `hub：GET /api/desk 按角色聚合等我、放行中、拦下、自动放行与最近动作`

### Task 6：推送正文带角色

**Files:** Modify `internal/hub/pushsvc.go`；Test `pushsvc_test.go`

- [ ] **Step 1：失败测试**：`pushMessage` 对带角色的 permission / ask / question 正文多一行（zh、en 各一）；越界时是“超出角色「X」的边界”；不带角色的与原来逐字相同（原有测试不动即可证明）。
- [ ] **Step 2–4：实现** `roleLine(r, lang)`，加进 `pushCopy`（`role func(string) string`、`beyond func(string) string`）。
- [ ] **Step 5：提交** `推送：有角色时正文带角色名，越界时说明超出边界`

### Task 7：网页数据层（types、api、desk.ts、decide.ts）

**Files:** Modify `web/src/types.ts`、`api.ts`、`decide.ts`；Create `web/src/desk.ts`、`web/src/desk.test.ts`；Test `decide.test.ts`

**Produces：**

```ts
// types.ts
interface RequestView { role?: string; beyond_role?: boolean }
interface DeskRecent { id: string; tool_name: string; summary: string; risk: string; state: RequestState; beyond_role: boolean; at: string }
interface DeskRow { machine_id: string; machine_name: string; online: boolean; name?: string; note?: string; ceiling?: string; forbid_count?: number; trust_max_mins?: number; dirs?: string[]; waiting: number; trusts_active: number; denied: number; auto_allowed: number; sessions: number; recent: DeskRecent[] }
interface Desk { window_hours: number; roles: DeskRow[]; unassigned: DeskRow[] }
// api.ts
hub.desk(): Promise<Desk>
// desk.ts
boundaryLine(row: DeskRow): string               // “上限 项目内写 · 禁用 2 项 · 放行最长 2 小时”，没设的段不显示；-1 为“不允许限时放行”
deskGroups(d: Desk): { machine_id; machine_name; online; rows: DeskRow[] }[]   // 按机器分组，角色在前、未分配在后
inboxHref(row: DeskRow): string                  // /inbox?role=<name 或空>&machine=<id>
parseFilter(search: URLSearchParams): { role: string; machine: string } | null
applyFilter<T extends Pick<RequestView,'role'|'machine_id'>>(list: T[], f): T[]
roleText(r: Pick<RequestView,'role'|'beyond_role'>): string | null   // 卡片上的角色行
// decide.ts
beyondBlock(r: RequestView, action: Verdict): string | null  // 越界批准时不能记住与放行的原因
```

- [ ] **Step 1：失败测试**（`desk.test.ts`、`decide.test.ts`）→ **Step 2：跑** `npx vitest run src/desk.test.ts src/decide.test.ts` 失败 → **Step 3：实现**（`initSheet` 越界批准时 `rememberable=false`、`trustable=false`）→ **Step 4：跑过**
- [ ] **Step 5：提交** `网页：总台与角色筛选的数据层；越界批准不能记住与放行`

### Task 8：网页页面（总台页、导航、收件箱筛选、卡片、详情、批准面板）

**Files:** Create `web/src/pages/Desk.tsx`、`web/src/pages/desk.test.tsx`；Modify `App.tsx`、`pages/Inbox.tsx`、`components/RequestCard.tsx`、`components/RequestAttrs.tsx`、`components/DecideSheet.tsx`、`app.css`、`locales/en.ts`；Test `components.test.tsx`、`inbox.test.tsx`

- [ ] **Step 1：失败测试**：总台渲染角色卡（名字、备注、边界一行、四个数、最近动作与“超出边界”标记）、离线机器卡片带 `offline` 类、多台机器有机器小标题、等我 > 0 的卡是链接到 `/inbox?role=…&machine=…`、空状态有 `railse role init`、未分配一栏照常；收件箱带 `?role=写作&machine=m1` 只显示匹配的、筛选条能清除；卡片显示角色与越界一行；面板越界批准无两个开关、有说明，拒绝面板照常有“记住”。
- [ ] **Step 2：跑失败** → **Step 3：实现**，新 CSS 只用现有变量（`.desk-card`、`.desk-card.offline`、`.stats`、`.filter-bar`），英文进 `en.ts` → **Step 4：** `npm run check` 全过（含 `contrast`、`i18n.catalog`、`i18n.sync`）
- [ ] **Step 5：提交** `网页：总台页、收件箱按角色筛选、卡片与批准面板的角色与越界`

### Task 9：e2e（越界请求从到达到批准）

**Files:** Create `web/e2e/fakeagent/main.go`（`//go:build ignore`）、`web/e2e/role.spec.ts`

- [ ] **Step 1：** 假守护进程：参数 `--hub --token --name`，`POST /api/enroll` 拿密钥，连 `/ws/agent`（Bearer），hello 声明 `role`、带两个角色，发 `BeyondRole=true`、`Session.Role=写作` 的 pending（`git push origin main`），回 pong，收到 decision 后打印 JSON、回 delivered、退出。
- [ ] **Step 2：** 用例：`runBin hub token` 拿接入码 → `go build` 假守护进程 → 起它 → 登录 → 总台看到“写作”卡、等我 1 → 点进收件箱带筛选 → 卡片有越界一行 → 批准面板没有“记住”和“接下来一段时间都放行”、有说明 → 批准 → 假守护进程打印 allow → 吊销这台机器收尾。
- [ ] **Step 3：** `make e2e` 全过 → **Step 4：提交** `e2e：假守护进程发越界请求，手机上只能单次批准`

### Task 10：文档与收尾

- [ ] `docs/hub.md`：加“角色与总台（0.3 起）”一节；API 一节补 `/api/desk` 与请求视图的 `role`、`beyond_role`，`can_trust` 说明补越界。
- [ ] `go build ./...`、`RAILSE_LANG=zh go test ./... -race`、`make lint`、`cd web && npm run check && npm run build`、`make e2e`。
- [ ] dev 服务器加假数据截总台页手机宽度深浅色两张图。
- [ ] 提交 `docs: hub.md 补角色与总台（0.3 起）与 /api/desk`
