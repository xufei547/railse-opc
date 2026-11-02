# Railse OPC v0.2.3：限时放行（按会话 / 项目整体交出去） — 设计

日期：2026-11-02。总设计见 `2026-10-21-railse-opc-design.md`（§0.6 有本版决策行）；上一版见 `2026-11-02-railse-opc-v0.2.2-two-screens-design.md`。

## 0 为什么

v0.2.1 把 Claude 的硬底线搬到 PreToolUse 之后，auto 模式下原本不会打扰人的操作开始进手机。作者 2026-11-01 16 点后的 4 小时：hub 处理 269 条，107 条规则放行、150 条交给 Claude 自己，12 条到手机。这 12 条按规则都"该问"（`pkill`、删会话临时目录、`grep .env`、浏览器扩展的 MCP 调用），但来源很集中：两个作者正在盯着的项目窗口。对这种窗口，每一条都是打断，等不到人批时整个会话还会被结束（v0.2.2 的实施会话就是这样死在自检脚本上的）。

现有手段都不对口：`railse agent pause` 是全局的，所有机器所有项目一起停，硬底线也一起没了；"记住"只能学只读、项目内写、联网三级，而且是永久规则；手写 `risk: destructive` 的 allow 规则又太重。缺的是一个**显式、限时、有范围**的"这一段时间这个窗口我全兜着"。Claude Code 自己有"本会话总是允许"，Railse 要有同量级的操作，并且多出有效期、范围与审计。

## 1 目标与不做

**目标**：用户能在手机、终端或启动命令里，把一个 CLI 会话或一台机器上的一个项目在一段时间内整体放行；放行期间该范围内的请求全部本地 allow，不弹终端框、不进手机；到期自动失效；随时可见、随时可撤；审计可查。

**不做**：按风险档位的部分放行（"只放行 destructive 以下"），那是规则的事；跨机器的项目放行（范围永远带 machine）；永久放行（上限 24 小时）；Codex / Gemini 的会话级放行先不做入口但机制通用（它们的请求经 PermissionRequest / 原生钩子进守护进程，项目放行对它们同样生效；Gemini 只能拒绝不能放行，照旧不受影响）。

## 2 机制

### 2.1 放行记录

```go
// policy 包不碰它；放在 agent 包与 hub 共用的 proto 里。
type Trust struct {
    ID        string    `json:"id"`                    // 随机 8 字节 hex
    MachineID string    `json:"machine_id"`
    Scope     string    `json:"scope"`                 // session | project
    Tool      string    `json:"tool,omitempty"`        // session：claude/codex/gemini
    CLISession string   `json:"cli_session_id,omitempty"` // session
    Project   string    `json:"project_root,omitempty"`  // project：项目根绝对路径（按 agent.ResolveProjectRoot 的口径）
    Until     time.Time `json:"until"`
    CreatedAt time.Time `json:"created_at"`
    CreatedBy string    `json:"created_by"`            // phone | cli | env
    Note      string    `json:"note,omitempty"`        // 手机上的备注；cli 的 --note
}
```

时长：默认 2 小时，最长 24 小时，最短 5 分钟。**守护进程是执行者**，放行表在它内存里，同时落 `~/.railse/trusts.json`（独立于 state.json；重启不丢，过期的启动时清掉）。（计划写作时改定：原稿写进 state.json。）hub 是**展示与远程控制**：存一份副本，手机上看、撤、建。两边同步见 2.5。

### 2.2 判定

`handlePermission` 在跑引擎**之后**、送手机之前加一步：

| 引擎结论 | 命中放行 | 结果 |
|---|---|---|
| 用户手写的 deny 规则（`source: user`，`action: deny`） | 任意 | 仍 deny（放行不压用户的拒绝） |
| 其余 allow / deny 规则 | 任意 | 照旧（放行不需要出手） |
| ask（定不了） | 有，且未过期 | `allow`，`decided_by: trust:<id>`，审计 `state: auto_allowed`，`reason: 限时放行（<范围>，剩 <时长>）` |
| ask | 无 | 照旧（v0.2.2：硬底线回 ask + PermissionRequest，中间三级交 Claude，PermissionRequest 送手机） |
| 总开关暂停 | 任意 | 照旧交回终端（暂停压一切，与现在一致） |

命中规则：`session` 范围要 `Tool`、`CLISession` 与请求的 `Session.Tool`、`Session.CLISessionID` 相同；`project` 范围要请求算出的项目根等于 `Project`（路径字符串比较，经 `CleanCwd`）。同一请求命中多条取最晚到期的那条记 `decided_by`。

PreToolUse 来的请求放行后输出 `allow`，Claude 会跳过自己的权限检查（v0.2.1 §2.5 的后果）；PermissionRequest 来的同理。这是放行的本意。提问（`handleAsk`）不受放行影响，选择题照样进手机。

### 2.3 入口

**手机**（主入口）：审批面板（`DecideSheet`）批准时多一个开关"接下来 {时长} 都放行"，范围分段选"这个会话 / 这个项目"，时长分段选 30 分钟 / 2 小时 / 8 小时（默认 2 小时）。打开开关后 `POST /api/requests/{id}/decide` 的 body 多 `trust: {scope, minutes}`；hub 先建放行记录并推给守护进程，再下发这条的 allow。请求没有 `cli_session_id` 时只有"这个项目"；没有项目根时只有"这个会话"；都没有就不显示开关。拒绝时不显示。

**终端**：

```
railse trust [时长] [--session <cli_session_id>] [--note 文字]   # 时长如 30m、2h、8h，默认 2h；不带 --session 就是当前目录所在项目
railse trust list
railse trust revoke <id> | --all
```

`railse trust` 在项目目录里跑：项目根按守护进程同一套 `ResolveProjectRoot`（不是 git 仓库、在系统目录里时报错，不放行）。经本机 socket 发给守护进程（新增 `proto.Control` 子命令 `trust_set` / `trust_list` / `trust_revoke`，与 pause/resume 同一通道）。

**启动命令**：`RAILSE_TRUST=2h claude`。钩子进程继承这个环境变量，在每条 `proto.Request` 里带 `TrustFor: "2h"`；守护进程第一次见到带它的某个会话时建一条 `session` 范围、`created_by: env` 的放行（起算时间是第一次见到，不是启动时），之后的请求不再看这个字段。写错格式或超过 24 小时：钩子在 stderr 说一句、按没有处理。这是"只放行这一个窗口"的最省事写法，不用知道 session id。

### 2.4 可见与撤销

- 手机收件箱顶部：有生效中的放行时显示横幅"N 个放行生效中，最近的 HH:MM 到期"，点进设置页。
- 设置页新增"限时放行"一节：每条显示范围（会话：CLI 名 + 项目名 + 机器；项目：项目名 + 机器）、谁建的、剩余时间、备注，按钮"撤销"。空时一句"没有生效中的放行"。
- 推送：建放行时**不推**（是你自己建的）；到期**不推**；没有新的通知类型。
- 终端：`railse agent status` 多一行 `trusts: N`；`railse doctor` 新增 `agent.trusts`（只读，有生效中的报 ok 并在细节里列出，没有 skip）。
- 审计：建、撤、到期清理各记一条 `kind: control`（`trust_set` / `trust_revoke` / `trust_expired`），随审计上报 hub，hub 的审计表里能查。

### 2.5 守护进程与 hub 的同步

照"总开关"的样子做，不照"规则"的样子（规则是 hub 单向下发，放行要双向）：

- wire 新增 `trusts` 帧（hub → 守护进程，整台机器的放行快照）和 `trust` 帧（守护进程 → hub，一条变更：set / revoke / expired）。`welcome` 带快照。
- 守护进程收到快照（计划写作时精确化）：快照里有的条目写入/覆盖并标"已同步"；本机**已同步**却不在快照里的，删掉（被手机撤了）；本机**未同步**的（断线期间 cli / env 建的）保留，并逐条补发给 hub。本机建的条目只有等 hub 的快照回来才算同步。
- 没接 hub / hub 不可达：本机条目照常生效；手机建的条目在断线期间仍按守护进程手里的副本生效直到过期（不因断线失效，也不因断线延长）。
- hub 存 `trusts` 表（schema v6：`id, machine_id, scope, tool, cli_session_id, project_root, until, created_at, created_by, note, revoked_at`），每分钟清过期的；`GET /api/trusts`、`DELETE /api/trusts/{id}` 给网页用；hub 收到 `DELETE` 就推新快照给那台机器。
- 旧 hub 配新守护进程：welcome 里没有 `trusts` 能力声明，守护进程不发 `trust` 帧，只用本机条目，手机上看不到也建不了（面板不显示开关：网页看 `/api/version` 的能力）。新 hub 配旧守护进程：hub 不下发快照，手机上的开关不显示（按机器的 `agent_version` 判断）。

### 2.6 上限与安全

- 时长 5 分钟 ～ 24 小时；守护进程与 hub 都校验，超出按 24 小时截。
- 每台机器同时生效的放行至多 20 条，超出拒建并报错。
- 放行只影响 ask 一档的结论，用户 deny 规则、总开关、提问都不受影响。
- `RAILSE_TRUST` 只能建 session 范围；project 范围只能从终端（在那个目录里）或手机建。
- 放行记录不出这台机器与它的 hub；审计里不记请求内容之外的东西。
- README 与 hub.md 写清："放行期间 Railse 对该范围完全不把关，等同于 Claude Code 的 bypassPermissions 加上审计。"

## 3 验收（真机）

1. 手机上批准一条 destructive，勾"这个会话 2 小时"：同一窗口再跑 `rm -rf <临时目录>`，终端不弹框、手机无卡片、命令直接跑，本机审计 `decided_by: trust:<id>`；另一个窗口同样命令仍弹框。
2. 手机设置页看到这条放行并撤销：原窗口下一条 destructive 又弹框。
3. 项目目录里 `railse trust 30m`：该项目所有窗口（含新开的）都放行；`railse trust list` 看得到；手机上也看得到。
4. `RAILSE_TRUST=5m claude`：只这一个窗口放行，5 分钟后再跑硬底线又弹框。
5. 停掉本机 hub：手机建的放行仍生效到过期；本机 `railse trust` 照常。
6. 放行期间 `AskUserQuestion` 仍进手机；用户 deny 规则仍拒绝。
7. 老 hub（0.2.2）配新守护进程：本机 `railse trust` 生效，手机上没有开关、不报错。

### 真机验收记录（2026-11-02，rc1，Mac 本机 hub v6）

安装：`railse 0.2.3-rc1` 换到 `/opt/homebrew/bin`，hub 库 v5 → v6，doctor 20 通过 1 警告。终端侧用真实守护进程、把 PreToolUse 样本直接喂 `railse hook claude` 模拟各个"窗口"（不同 `session_id`、`cwd`），另起一个真的 `claude --permission-mode auto` 窗口做对照；手机侧由作者操作 iPhone PWA。

| # | 验收项 | 结果 |
|---|---|---|
| 1 | 手机批准带"这个会话 2 小时" | 通过。同窗口 `rm -rf` 当即 allow（"限时放行中"），放行表多一条 `created_by: phone`；2 小时后被到期清理，hub 库标 `expired` |
| 2 | 手机设置页撤销 | 通过（第二次）。撤后守护进程表为空、同窗口回 ask、hub 库 `revoked_at` + `revoke_reason: user`、审计 `trust_revoke`。第一次点撤销没有任何请求到 hub，原因未查明（当时页面可能是旧状态）；电脑浏览器上同一页撤销一次成功 |
| 3 | 项目目录 `railse trust 30m` | 通过。同项目两个窗口 `git push`、`rm -rf` 都 allow，别的目录仍 ask；真窗口 `git push --dry-run` 不弹框直接跑，审计 `decided_by: trust:<id>`；`railse trust list` 与手机横幅、设置页都看得到 |
| 4 | `RAILSE_TRUST=5m` | 通过。当条放行并建一条 `env` 来源的会话放行，同窗口后续不带变量也放行，别的窗口不受影响；5 分钟后两条被清（审计 2 条 `trust_expired`），同窗口回 ask，hub 库标 `expired` |
| 5 | 停 hub | 通过。`launchctl bootout` 后放行仍 allow，`railse trust` 照常建；`bootstrap` 回来后停机期间建的条目补发进 hub 库 |
| 6 | 提问与 deny 规则 | 通过。放行中的项目里 `AskUserQuestion` 仍进手机并带回答案；临时守护进程加一条用户 deny（`command_prefix: rm -rf`）后放行期间仍 deny、`git push` allow |
| 7 | 老 hub 配新守护进程 | 未在真机做（Fly 上的 0.2.2 hub 停着），靠单元测试与 hello 能力声明兜底 |

顺带发现，均不阻塞发布：

- 手机建的放行 id 是 32 位十六进制（hub `store.NewID`），本机建的是 16 位（设计 §2.1 写 8 字节），列表里长短不一。
- 收件箱横幅"到期"与"去设置"之间少一个空格。
- 收件箱挂着多张"在终端等你回答"（Claude Code 空闲提示）的旧卡，最早 22 小时前；v0.2.2 的收尾取消只覆盖审批与提问。
- `railse trust --dir` 是守护进程数据目录（与 `agent --dir` 一致），不是项目目录；用法说明写得对，但容易看错。
- 没接 hub 时 PreToolUse 的 ask 理由仍写"已发到手机"（v0.2.2 起就有）。

## 4 文档

README 两份加"限时放行"一节与一行命令；`docs/hub.md` 加 `trusts` 表、帧、接口；`docs/agent-local-loop.md` 加判定表；`docs/setup.md` 升级说明（schema v6）；launch 的 `qa.md` 加一问"太吵怎么办"。
