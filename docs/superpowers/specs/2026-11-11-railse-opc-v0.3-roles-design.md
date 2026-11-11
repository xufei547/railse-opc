# Railse OPC v0.3 设计：角色（权限边界）与总台

日期：2026-11-11。前置：[定位备忘](../../research/2026-11-03-global-positioning.md)、[OpenClaw / Paperclip spike](../../research/2026-11-03-openclaw-paperclip-spike.md)。

## 1 目标与不做

一句话：给每个 agent 会话一个**角色**，角色是一道权限边界；手机上多一页**总台**，按角色看谁在等我、谁被拦了、谁在放行中。

已定的决定（2026-11-04 与作者逐条确认）：

1. v0.3 只做角色与总台。OpenClaw 插件留到 v0.4。
2. 绑定：环境变量 `RAILSE_ROLE` 优先，其次按项目目录匹配角色的 `dirs`，都没有就是“未分配”，按今天的规则走。
3. 越界两档：`ceiling`（风险上限，超过必问人）与 `forbid`（禁用，直接拒、不进手机）。两者都压过规则与限时放行。
4. 角色定义在机器上的 `~/.railse/roles.yaml`（一个文件装全部角色），守护进程把摘要报给 hub，手机只看不改。
5. 做法 A：角色进 `internal/policy`，判定仍只有 `Engine.Evaluate` 一个入口。

不做：派活、角色互派、工作流、人的角色、手机上改角色、规则的 role 范围、成本统计、OpenClaw / Paperclip 适配器。

## 2 角色文件

`~/.railse/roles.yaml`（守护进程目录下，随 `--dir` 走）。文件不存在等于没有角色，行为与 0.2.x 完全一样。

```yaml
version: 1
roles:
  - name: 开发
    note: 写代码、跑测试
    dirs: ["~/RailseWork", "~/我的程序/**"]
    ceiling: network          # 超过这一档（outward、destructive）一律问人
    forbid:
      - {command_prefix: "fly "}
    trust_max: 24h
  - name: 写作
    note: 只改文档
    dirs: ["~/dev/*/docs"]
    ceiling: write_in_project
    forbid:
      - {command_prefix: "git push"}
      - {payload: mcp}
    trust_max: 2h
  - name: 运维
    note: 发版、部署、装机
    ceiling: destructive
```

字段：

| 字段 | 必填 | 含义 |
|---|---|---|
| `name` | 是 | 角色名。1～32 个字符，不含空白与控制字符，文件内唯一；可以是中文。 |
| `note` | 否 | 一行说明，≤120 字符，给总台看。 |
| `dirs` | 否 | 路径模式列表（`~` 与 doublestar，同规则的 `scope.project`）。两个用途：按目录绑定；划定角色的地盘（见 3.3）。 |
| `ceiling` | 否 | `policy.Risk` 的名字。风险**高于**它的请求超出边界。不写等于不设上限。 |
| `forbid` | 否 | `policy.Match` 列表，每条不能为空，字段与规则的 `match` 完全相同（复用同一套匹配与校验）。 |
| `trust_max` | 否 | 时长（`30m`、`2h`，5m～24h）或 `none`。不写等于不设。 |

校验（`policy.RoleSet.Validate`，未知字段一律报错，同规则文件的严格程度）：version 必须是 1；名字合法且不重复；ceiling 能解析；forbid 每条通过 `Match` 的校验（正则可编译等）；trust_max 能解析。两个角色的 `dirs` 重叠不算错，绑定按文件里的先后取第一个，`railse role check` 给出提醒。

文件有误时 **fail closed**：守护进程启动或重载读到坏文件，进入“角色文件有误”状态，所有审批按上限 `read` 的越界处理（只读照旧自动放行，其余一律问人），理由写明 `roles.yaml 有误：<错误>`，直到文件修好。不沿用旧的好版本，免得一次手误悄悄放宽或悄悄失效。

重载：守护进程在每次判定前比对文件的 mtime 与大小，变了就重读（文件小，开销可忽略）；重读成功且 hub 声明了 `FeatureRole` 时重发 `Roles` 帧。

写保护：`~/.railse/**` 已是敏感路径（`internal/policy/paths.go`），agent 改 `roles.yaml` 按 destructive 定级，本设计不新增保护，但要有一条测试钉住它。

## 3 绑定与判定

### 3.1 会话的角色

钩子（三家 CLI 的所有事件）把 `os.Getenv("RAILSE_ROLE")` 原样填进 `proto.Session.Role`。守护进程收到后解析成生效的角色并改写这个字段：

1. `Session.Role` 非空且 `roles.yaml` 里有同名角色 → 就是它。
2. 否则按项目根（没有项目根时按 cwd）匹配各角色的 `dirs`，文件顺序第一个命中的。
3. 都没有 → 空串，“未分配”。

`RAILSE_ROLE` 指了一个不存在的名字：走第 2 步，并在守护进程日志里记一行（每个会话只记一次），`railse doctor` 不管这个。

之后审计、上报 hub、推送都带生效值。

### 3.2 `Engine.Evaluate` 的新顺序

`policy.Context` 增加 `Role *Role`（nil 为未分配）。`policy.Decision` 增加 `Role string`、`BeyondRole bool`、`Forbidden bool`。

1. 定级（不变，但见 3.3 的项目根调整）。
2. **用户 deny 规则命中 → deny**（不变，仍压一切）。
3. **forbid**：角色的任一条 `forbid` 命中 → `deny`，`Forbidden=true`，Reason `角色「写作」禁用：<那一条的简述>`。
4. **ceiling**：风险 > ceiling → `ask`，`BeyondRole=true`，Reason `超出角色「写作」的边界（上限 write_in_project，这次是 outward）`。任何 allow 规则都不适用。
5. 其余照旧：规则、底线（`mayAllow`）、默认 ask。

总开关暂停时：第 2、3 步照常生效（拒绝不受暂停影响），其余按现状 ask。

守护进程（`handlePermission`）：

- `Forbidden` → 审计 `auto_denied`，`DecidedBy` 为 `role:<name>`，回 deny；理由原样给模型看。
- `BeyondRole` → **跳过限时放行**（包括 `RAILSE_TRUST`），其余走现有的 ask 路径；送 hub 的 `PendingRequest.BeyondRole=true`。
- 限时放行命中时再看 `trust_max`：角色设了 `trust_max`，这条放行只在 `CreatedAt + trust_max` 之前对该角色的请求有效；`none` 表示这个角色的请求从不走限时放行。不改放行本身，只在匹配时判。
- `AuditEntry` 增加 `Role`、`BeyondRole`。

### 3.3 角色的地盘

角色写了 `dirs`，而当前项目根（没有时为 cwd）不在任何一条 `dirs` 内（典型：用 `RAILSE_ROLE` 在别处启动）：`Context.ProjectRoot` 按空处理，现有逻辑即 fail closed，写入一律 `write_outside`。项目根在地盘内：项目根语义不变（只有这个项目内的写算 `write_in_project`，不扩大到角色的全部目录）。没写 `dirs` 的角色不做这一步。

### 3.4 答题与事件

`AskUserQuestion`（kind ask）与 question / done / tool_done 事件不经判定，只带上生效的角色名。

## 4 命令行

```
railse role list  [--dir 目录]          列出角色：名字、上限、禁用条数、放行上限、目录、备注
railse role check [--dir 目录]          校验 roles.yaml；报错误、目录重叠、指向不存在目录的模式
railse role which [--dir 目录]          当前目录（与当前 RAILSE_ROLE）会解析成哪个角色，以及凭什么
railse role init  [--dir 目录]          文件不存在时写一份带注释的起步模板（开发 / 写作 / 运维；英文界面为 dev / writer / ops）
```

`--dir` 与其余子命令一样指守护进程目录。`list`、`which` 走本机 socket 问守护进程（新消息 `role_list`、`role_which`），守护进程没起时直接读文件并注明。`railse doctor` 增加一项：`roles.yaml` 存在时是否合法。`railse --help` 与 `main.go` 的命令表加一行。所有新字符串进 `internal/i18n/en.go`。

## 5 守护进程与 hub 的契约

已提交在 main（本设计的同一次提交），两边照此实现，不要改形状：

- `proto.Session.Role`：见 3.1。
- `proto.RoleSummary{Name, Note, Ceiling, ForbidCount, TrustMaxMins, Dirs}`：`TrustMaxMins` 为 0 表示不设，`none` 用 -1。
- `wire.FeatureRole = "role"`：两边各自在 features 里声明。
- `wire.Hello.Roles`：连上时的角色摘要。
- `wire.Roles{T: "roles", Roles}`：重载后整份重发，hub 整份替换该机器的角色表。
- `wire.PendingRequest.BeyondRole`。

兼容：老 hub 配新守护进程，角色照样在本机生效，只是手机上看不到；新 hub 配老守护进程，总台只有“未分配”一栏。不提协议版本号。

## 6 hub

存储（schema v7，迁移只加不改，带 v6 → v7 的迁移测试与 testdata）：

- `sessions.role TEXT NOT NULL DEFAULT ''`，每次上报时更新为最新值。
- `requests.role TEXT NOT NULL DEFAULT ''`、`requests.beyond_role INTEGER NOT NULL DEFAULT 0`。自动放行、自动拒绝补的历史行同样带角色。
- 新表 `machine_roles(machine_id, position, name, note, ceiling, forbid_count, trust_max_mins, dirs)`，主键 `(machine_id, name)`，`dirs` 存 JSON。收到 hello 或 `roles` 帧时在一个事务里整份替换；机器没声明 `FeatureRole` 时清空。

请求视图（`requestView`）增加 `role`、`beyond_role`。`beyond_role` 为真时 `can_trust` 为假，决定接口拒绝带 `remember` 或 `trust` 的批准（400）。推送通知正文在有角色时带上角色名。

新接口 `GET /api/desk`（要登录，同其余接口）：

```json
{
  "window_hours": 24,
  "roles": [
    {
      "machine_id": "…", "machine_name": "…", "online": true,
      "name": "写作", "note": "只改文档",
      "ceiling": "write_in_project", "forbid_count": 2, "trust_max_mins": 120, "dirs": ["~/dev/*/docs"],
      "waiting": 1, "trusts_active": 1, "denied": 3, "auto_allowed": 41, "sessions": 2,
      "recent": [ {"id": "…", "tool_name": "Bash", "summary": "…", "risk": "outward", "state": "denied", "beyond_role": true, "at": "…"} ]
    }
  ],
  "unassigned": [ { "machine_id": "…", "machine_name": "…", "online": true, "waiting": 0, "trusts_active": 2, "denied": 0, "auto_allowed": 12, "sessions": 1, "recent": [] } ]
}
```

口径：

- 行 = `machine_roles` 的每一行（按机器、position 排序），加上每台机器一行“未分配”（该机器 24 小时内有未分配的请求或有未分配的生效放行时才出现）。库里有请求但角色已从文件里删掉的，并进未分配。
- `waiting`：`state = pending` 的 permission 与 ask。`denied`：24 小时内 `denied` + `auto_denied`。`auto_allowed`：24 小时内 `auto_allowed`。`sessions`：24 小时内有事件的会话数。
- `trusts_active`：生效中的放行。会话放行算到该会话当前的角色；项目放行算到这台机器上该 `project_root` 最近一条请求的角色（没有请求记录的算未分配）。hub 不做目录模式匹配。
- `recent`：最近 5 条 permission 请求，`summary` 复用收件箱卡片已有的一行摘要（已脱敏）。

实时：现有的 WebSocket 事件不变；网页在收到请求、放行、机器上下线事件时重取 `/api/desk`（节流 1 秒）。

## 7 网页（PWA）

- 底部导航加一项**总台**（英文 Desk），放在收件箱之后。
- 每个角色一张卡：名字与备注；一行边界（`上限 项目内写 · 禁用 2 项 · 放行最长 2 小时`，没设的不显示）；四个数字（等我 / 放行中 / 拦下 / 自动放行，后两个注明“24 小时内”）；最近动作列表（越界的带“超出边界”标记）。多台机器时按机器分组，离线的机器卡片变灰。
- “等我”大于 0 时整块可点，进收件箱并按角色筛选（`/inbox?role=…&machine=…`，收件箱顶部出现可清除的筛选条）。
- 收件箱卡片与请求页显示角色名；`beyond_role` 的请求在卡片上显示“超出角色「写作」的边界”，批准面板不出现“记住”与“接下来一段时间都放行”，原位说明原因。
- 空状态：没有任何角色时，总台说明角色是什么，给出 `railse role init` 一行命令；未分配一栏照常显示。
- 风险等级沿用现有的中文叫法与配色；所有新字符串进 `web/src/locales/en.ts`，过 `i18n.catalog` / `i18n.sync` 测试；对比度过 `contrast.test.ts`。

## 8 测试

- `internal/policy`：角色文件解析与校验（表驱动，含未知字段、重名、坏正则、坏时长）；`Evaluate` 的顺序表（用户 deny > forbid > ceiling > 规则；ceiling 压过 user allow；暂停下 forbid 仍拒）；地盘内外的定级；`roles.yaml` 自身按 destructive 定级。
- `internal/agent`：解析角色（环境变量、目录、不存在的名字、重叠取先）；越界跳过放行与 `RAILSE_TRUST`；`trust_max` 与 `none`；坏文件 fail closed 与修好后恢复；mtime 重载；审计字段；hello 与 `roles` 帧（对假 hub）。
- `cmd/railse`：四个子命令的输出与退出码，守护进程不在时的回退。
- `internal/hub`：迁移；整份替换角色表；`/api/desk` 各口径；`beyond_role` 时拒绝 remember / trust；老守护进程不带角色时的表现。
- `web`：总台页与筛选的 vitest，Playwright e2e 一条（越界请求从推送到批准，面板没有记住与放行开关）。
- 一律用临时目录，不碰真实的 `~/.claude`、`~/.codex`、`~/.gemini`、`~/.railse`、`~/.railse-hub`。`make lint` 与全部测试通过才算完。

## 9 分工与落地

| 分支 | 范围（只动这些） | 谁 |
|---|---|---|
| 机器侧 | `internal/policy`、`internal/agent`、`internal/adapter`、`internal/doctor`、`cmd/railse`、`docs/agent-local-loop.md` | Claude Opus 5.5，思考 high |
| hub 与总台 | `internal/hub/**`（`wire` 的契约字段除外，不改）、`web/**`、`docs/hub.md` | Claude Opus 5.5，思考 medium |
| 审查 | 两条分支各一遍对抗式审查，只读 | Codex，思考 high |
| 周边文档 | `docs/works-with/paperclip.md`、`docs/works-with/herdr.md`、README 中英文的角色一节 | Codex，思考 medium，合并后做 |

`internal/i18n/en.go` 两条分支都会加词条：各自加在文件里自己那一类的末尾，合并冲突由主会话解。版本号、CHANGELOG、打 tag 都不在分支里做。Show HN（2026-11-12）用 0.2.3，v0.3 不赶这个日子。
