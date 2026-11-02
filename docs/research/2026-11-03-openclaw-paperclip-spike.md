# Spike：Railse 能不能接到 OpenClaw 与 Paperclip 下面（2026-11-03，仅文档层）

> 目的：验证 [定位备忘](2026-11-03-global-positioning.md) 第 5 节的第一步是否可行。只读官方文档，没有装 OpenClaw 实机跑。实机验证项列在第 4 节。

## 1 OpenClaw：可以，而且接口正合适

**拦截点。** OpenClaw 插件系统有 `before_tool_call` hook（类型 Modify / gate）：可以改参数、`block: true` 直接拦下、或返回 `requireApproval{title, description, scope, severity info|warning|critical, timeoutMs, allowedDecisions, onResolution}` 让 OpenClaw 自己的审批面去问人。另有 `after_tool_call`、`session_start/end`、`run_stopped`、`before_agent_run`（可 block 整次运行）、`cron_changed/reconciled`，够喂总台。

**对 Railse 关键的几条规则。**
- 默认每个 handler 15 s 超时，超时或抛错 → **工具调用被拦下**（fail-closed，和我们一致）。超时可按 hook 在插件配置里调，上限 600 000 ms（10 分钟），手机审批够用。
- `matcher` 只接受标准工具 id 列表（`exec`、`apply_patch`、`spawn_agent` …），不支持通配；不填即匹配全部工具。
- 执行顺序：内置 trusted policy → 已安装插件的 trusted policy（须在 manifest `contracts.trustedToolPolicies` 声明）→ 普通 `before_tool_call`。外部插件官方建议走普通 hook。
- hook 只能否决，不能新增工具，不能越过 sandbox、exec approvals、owner-only、channel policy。Codex 原生工具不允许改参数，但可以拦和看。
- `before_tool_call` **不需要** `allowConversationAccess` 授权；`agent_end`、`llm_input` 等会话内容类 hook 才需要。

**OpenClaw 自带的 exec approvals 现状。** `tools.exec.security` 在 gateway/node 主机上默认 `full`，`ask` 默认 `off`，也就是默认不问人；`askFallback` 默认 `deny`（没有 UI 可达时拒绝）。审批请求以 `exec.approval.requested` 广播给声明了 `approvals` / `exec-approvals` 能力的操作端（Control UI、macOS/iOS/Android app、API client），`/approve` 或 `openclaw approvals resolve <id> allow-always --expires-in-days n` 解决。只覆盖 `exec`，不覆盖发邮件、发消息、浏览器等真正出事的工具。这就是空档。

**插件形态。** TypeScript ESM，Node ≥ 24.22；`package.json` 带 `openclaw` 块，manifest `openclaw.plugin.json`（id、contracts、activation、configSchema）；`openclaw plugins install npm:<pkg>` 安装，发布走 ClawHub。宿主 API 标注为实验性，要 pin 宿主版本。

**Railse 的接法（建议）。**
1. `@railse/openclaw` 插件，注册 `before_tool_call`（不填 matcher，全部工具），把 `{tool, params, agentId, sessionKey, runId}` 发给本机 Railse 守护进程（unix socket，复用现有 hookclient 协议，新增 adapter `openclaw`）。
2. 守护进程走现有流程：策略引擎 → 规则 allow/deny 直接回；决定不了 → hub → 手机。插件等待，配置该 hook 超时为 10 分钟。
3. 回 `{block:true}` 或放行。可选同时返回 `requireApproval` 并让守护进程以 API client 身份 `approval.resolve`，做到 OpenClaw 自己的面板和手机"同屏"（对应 v0.2.2 的终端同屏）。
4. `session_start/end`、`run_stopped`、`after_tool_call` 作为总台的数据源；`before_agent_run` 用来实现"按角色拒绝整次运行"。
5. 策略引擎要新增的工具类别：OpenClaw 的工具 id 空间（exec、apply_patch、browser、message/email/channel 发送、spawn_agent、cron）与风险等级的映射表。外发消息类工具应直接落在 `outward`。

## 2 Paperclip：不需要接，已经在我们下面

- 110k star、18k fork，MIT，Paperclip Labs Inc。adapter 覆盖 Claude Code、Codex、Gemini CLI、Cursor、OpenCode、Pi、Hermes、Kimi、OpenClaw gateway（标"即将上线"）、任意进程与 HTTP。
- 它的审批是组织级：招聘、CEO 策略、预算超限、董事会；在 web 的 Approvals 页回答。**没有工具调用级的审批**，文档未提到手机或推送。
- Claude Code / Codex adapter 默认 `engine: auto`：沙箱提供双向进程会话时走 ACP，否则走 CLI。无论哪条，Claude Code 的用户级 hooks 仍在 Claude 进程内生效，所以 **Paperclip 派出的 Claude Code 员工已经受 Railse 的 PreToolUse 硬底线约束**（v0.2.1 起任何权限模式下 outward/destructive 都到手机）。
- 结论：Paperclip 是 works-with 关系。要做的只是一篇文档 + 一个角色绑定方式（Paperclip 的 agent 名 → Railse 角色），不需要新 adapter。

## 3 对 v0.3 的范围建议

| 件 | 内容 | 依赖 |
|---|---|---|
| 1 | `railse hook openclaw` adapter + `@railse/openclaw` 插件（npm 包，仓库内 `plugins/openclaw/`） | 实机 spike 通过 |
| 2 | 角色 = 权限边界：`~/.railse/roles/*.yaml`（允许目录、风险上限、禁用工具、放行上限、备注），绑定方式 `RAILSE_ROLE` 环境变量 / 目录映射 / OpenClaw agentId 映射 / Paperclip agent 名映射 | 无 |
| 3 | 总台（策略视角）：按角色聚合 等我 / 放行中 / 今日拦下 / 今日自动放行 / 最近动作；hub 新表 + PWA 一页 | 2 |
| 4 | 文档：works-with OpenClaw / Paperclip / herdr 三篇 | 1 |

不做：派活、角色互派、工作流、人的权限、成本估算（只在能拿到真实用量时显示）。

## 4 实机要验证的事（需要作者在自己终端装 OpenClaw）
1. `before_tool_call` 事件的实际字段名（文档没列），尤其工具参数的形状和 agentId / sessionKey 是否总是存在。
2. 把该 hook 超时调到 10 分钟后，agent 运行是否真的挂起等待，Control UI 有没有"正在等待"提示（`approvalRunningNoticeMs`）。
3. `block: true` 后 agent 看到的错误文案能否自定义（要像 Claude Code 那样把"手机上拒绝了"告诉模型）。
4. OpenClaw 各工具的 id 全集与参数结构，用来做风险映射。
5. Paperclip 的 ACP 通道下 Claude Code 用户级 hooks 是否照常触发（CLI 通道基本确定）。
6. 一个外部插件能否同时作为 Gateway API client 调 `approval.resolve`（同屏方案）。

## 来源
- https://docs.openclaw.ai/plugins/hooks 、https://docs.openclaw.ai/plugins/hooks/tool-policy 、https://docs.openclaw.ai/plugins/hooks/reference
- https://docs.openclaw.ai/plugins/building-plugins 、https://docs.openclaw.ai/tools/exec-approvals 、https://docs.openclaw.ai/gateway/protocol
- https://github.com/paperclipai/paperclip 、https://docs.paperclip.ing/reference/adapters/overview/ 、https://docs.paperclip.ing/guides/day-to-day/approvals/
