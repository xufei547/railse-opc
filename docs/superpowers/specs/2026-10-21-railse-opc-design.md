# Railse OPC 第一版设计：统一审批收件箱

- 日期：2026-10-21
- 状态：已通过作者审阅（2026-10-21）
- 产品名：Railse OPC，中文名 Railse 一人公司
- 许可证：Apache 2.0

## 0. 背景与目标

### 0.1 定位

Railse OPC 是给“用 AI agent 经营一人公司的人”用的公司层驾驶舱。目标用户同时跑着写代码、做内容、做运营的多个 agent，编程只是业务之一。

参照物是 herdr.dev，一个围绕 coding agent 重做的终端复用器加运行时。Railse OPC 与它不在一个层：herdr 的单位是终端窗格，Railse OPC 的单位是任务和决定。本产品不依赖 herdr，不使用其 API 或插件机制，也永远不做终端复用和读屏幕猜状态这类功能。

### 0.2 第一版要解决的痛点

同时跑多个 agent 时，不知道哪个在等人回答或审批，只能一个个窗口翻。

### 0.3 第一版范围一句话

策略引擎通过各 CLI 的权限钩子自动审批工具调用；引擎决定不了的进手机优先的网页收件箱，在手机上批准或拒绝；给 agent 回自由文本放第二期。

### 0.4 目标

- 任何 agent 停下来等审批，手机在线时一分钟内能看到。
- 用一周后，八成以上的审批由规则自动决定，不打扰人。
- 从推送到 agent 继续跑，中位数十秒以内。
- Railse 自身任何故障都不会卡住 agent，也不会误批。

### 0.5 非目标（第一版不做）

给 agent 回话、小模型判定灰色地带、飞书和 Telegram 渠道、费用和额度面板、定时任务、交付看板、多用户、Windows、托管云。终端复用、PTY 控制、读屏幕判状态永远不做。

### 0.6 已定决策

| 决策 | 结论 |
|---|---|
| 目标用户 | 用 AI agent 经营一人公司的人，第一个用户是作者本人 |
| 与 herdr 的关系 | 完全独立，不依赖、不集成 |
| 第一刀 | 统一的“等我处理”收件箱，核心是自动审批策略引擎 |
| 手机端形态 | 网页 PWA 加 Web Push，聊天机器人只是以后的可选渠道 |
| 技术栈 | 守护进程 Go 单二进制；网页 TypeScript，Vite 加 React |
| 拓扑 | 一个 hub 跑在作者的云服务器上，各机器的守护进程向外连，手机只连 hub |
| 作者现有环境 | Claude Code、Codex CLI、Gemini CLI 写代码；Claude Code 换 prompt、GPT-6、n8n 做内容运营；一台 Mac 加一台云服务器；手动开终端 |

## 1. 架构与组件

一个 Go 二进制 `railse`，四个子命令。

| 子命令 | 跑在哪 | 职责 |
|---|---|---|
| `railse hub` | 云服务器 | 给 PWA 的 HTTP 和 WebSocket；给守护进程的 WebSocket 入口；Web Push 发送；SQLite 存储；PWA 静态资源在构建时用 go:embed 嵌入 |
| `railse agent` | 每台跑 agent 的机器 | 常驻守护进程；到 hub 的出站 WebSocket 长连接，断线自动重连；本地 Unix socket 供钩子调用；本地缓存规则快照和未完成请求 |
| `railse hook <claude|codex|gemini>` | 由 CLI 钩子拉起的瞬时进程 | 读 stdin JSON，翻成统一载荷，连本地 socket，阻塞等决定，按该 CLI 的输出格式写回 stdout。只做格式翻译，不含策略 |
| `railse setup` | 每台机器一次 | 写钩子配置到 Claude Code、Codex、Gemini 的配置文件；用一次性接入码向 hub 注册本机；安装 launchd 或 systemd 服务并启动 |

辅助命令：`railse doctor` 自检，`railse policy export|import` 规则 YAML 导入导出，`railse hub token` 生成接入码。

网页：TypeScript，Vite 加 React，手机优先，PWA 可加到主屏幕并接收 Web Push。只需配置 hub 地址。

### 1.1 三条原则

1. 审批逻辑只存在于守护进程和 hub，适配器不做判断。加第四个 CLI 只是加一个翻译。
2. 守护进程永远不能让 agent 卡死。hub 不通或超时，钩子返回“不作决定”，CLI 退回终端原生审批框，收件箱里那条标过期。
3. hub 是唯一真相，守护进程只缓存。换 hub、换机器，数据不丢。

### 1.2 外部来源

- n8n 的人工审批节点通过 hub 的通用 webhook 接口进收件箱，第一版包含。
- GPT-6 网页对话没有“等人”的概念，不接。

### 1.3 仓库布局（建议）

```
仓库根目录/            # 本地目录为 RailseWork，GitHub 仓库名用 railse-opc
  cmd/railse/          # main 与子命令
  internal/policy/     # 纯引擎：分类器、规则匹配，无 IO
  internal/adapter/    # claude、codex、gemini、webhook 翻译
  internal/agent/      # 守护进程
  internal/hub/        # 服务端、存储、推送
  internal/proto/      # 守护进程与 hub 共用的消息类型
  web/                 # Vite + React PWA
  docs/
```

## 2. 数据模型

全部存在 hub 的 SQLite，守护进程只缓存规则快照和未完成请求。

### 2.1 实体

**Machine 机器**：id、name、enrolled_at、last_seen、agent_version、status（online、offline）、secret_hash。

**Session 会话**：id、machine_id、tool（claude、codex、gemini、webhook）、cli_session_id、cwd、project_name（git 根目录名）、started_at、last_event_at。会话只从钩子事件得知，不扫进程。

**Request 请求**：

| 字段 | 说明 |
|---|---|
| id | UUID，由守护进程生成 |
| session_id、machine_id | 归属 |
| kind | `permission`、`question`、`done`、`webhook` |
| payload | 统一载荷，见 2.2 |
| risk | 风险等级，见 4.1 |
| state | `pending`、`auto_allowed`、`auto_denied`、`approved`、`denied`、`expired`、`cancelled` |
| decided_by | 命中的规则 id、`user`、`timeout`、`terminal`（终端里处理了）、`hub_unreachable` |
| decision_note | 用户备注，可空 |
| created_at、expires_at、decided_at、delivered_at | 时间戳 |
| callback | 仅 webhook 类：回调 URL 与令牌 |

第一版只有 `permission` 和 `webhook` 可操作；`question` 和 `done` 只展示，详情页告诉用户去哪台机器哪个目录的终端处理。

**Rule 规则**：id、scope（`global`，或 `machine: <id>`，或 `project: <路径模式>`）、match（见 4.3）、action（`allow`、`deny`、`ask`）、source（`builtin`、`user`、`learned`）、enabled、hit_count、last_hit_at、created_at、created_from_request_id。

**AuditEvent 审计事件**：id、ts、actor（`user`、`rule:<id>`、`system`）、request_id（可空）、action、detail。只追加。规则的增删改也记这里。

**PushSubscription 推送订阅**：id、endpoint、keys、created_at、last_ok_at。

第一版单用户，无用户表。登录为一个密码换一个长期 token。

### 2.2 统一载荷

适配器把各家工具调用翻成四种规范形态，策略引擎不认识任何具体 CLI。

| type | 字段 | 来源举例 |
|---|---|---|
| `shell` | command、cwd | Claude Bash、Codex shell、Gemini run_shell_command |
| `file_write` | path、diff_preview（截断到 4KB） | Claude Edit/Write、Codex apply_patch、Gemini write_file/replace |
| `mcp` | server、tool、args | 各家 MCP 工具调用 |
| `network` | url、method | Claude WebFetch 等 |

示例：

```json
{
  "id": "6f1c…",
  "machine": "mac-studio",
  "session": {"tool": "claude", "cli_session_id": "…", "cwd": "/Users/xufei/Projects/blog", "project": "blog"},
  "kind": "permission",
  "payload": {"type": "shell", "command": "git push origin main", "cwd": "/Users/xufei/Projects/blog"},
  "risk": "outward",
  "created_at": "2026-10-21T14:00:00+08:00",
  "expires_at": "2026-10-22T13:59:50+08:00"
}
```

### 2.3 请求状态机

```
pending ──本地规则命中──▶ auto_allowed | auto_denied
pending ──用户在 hub 决定──▶ approved | denied
pending ──到期──▶ expired
pending ──钩子进程消失（终端里处理了、Ctrl-C、CLI 退出）──▶ cancelled
```

过期或取消后到达的用户决定一律拒绝并提示。

## 3. 审批数据流

### 3.1 正常路径

1. CLI 遇到需审批的工具调用，按钩子配置拉起 `railse hook <cli>`，把会话号、cwd、工具名和参数以 JSON 写到 stdin。
2. 钩子进程翻成统一载荷，连本地 Unix socket 交给守护进程，阻塞等答案。
3. 守护进程生成 id，算风险等级，用本地规则快照跑策略。命中 allow 或 deny 立刻答复钩子；钩子按该 CLI 格式写 stdout；CLI 继续。该决定异步补发 hub 记审计，收件箱历史可见，不推送。
4. 结果为 ask：守护进程把请求记为 pending 存本地，经 WebSocket 送 hub。
5. hub 落库，向所有推送订阅发 Web Push，并经 WebSocket 推给已打开的 PWA。
6. 用户在 PWA 点批准、拒绝或“批准并记住”（同时生成一条 learned 规则）。PWA 调 hub 接口；hub 记决定、写审计，经 WebSocket 把决定发给持有该请求的守护进程。
7. 守护进程把决定交给等待中的钩子进程；钩子写 stdout；CLI 继续。守护进程向 hub 回已送达。
8. 规则变更时，hub 把带版本号的完整规则快照推给所有守护进程，守护进程整体替换缓存。

### 3.2 边界情况

**超时过期。** `railse setup` 把各 CLI 的钩子超时设为该 CLI 允许的最大值，目标 24 小时。守护进程的期限为 CLI 超时减 10 秒。到期钩子返回“不作决定”，CLI 退回原生审批框；守护进程把请求标 expired 并通知 hub；收件箱显示“已过期，去终端处理”。

**hub 不通。** 本地策略为 ask 但 WebSocket 断开时，守护进程最多等 30 秒重连；仍不通则钩子返回“不作决定”，请求标 expired，decided_by 为 `hub_unreachable`。该请求留本地队列，重连后补发 hub 仅做审计，不再进收件箱。原则：hub 故障最多少一次推送，永不卡住 agent。

**终端里先处理了。** 用户直接在终端批了或 Ctrl-C，钩子进程退出，守护进程检测到 socket 断开，标 cancelled，通知 hub 收掉收件箱条目。

**断线重连。** 请求有 UUID，决定携带请求 id，重复决定忽略。守护进程重连后重新上报全部本地 pending，hub 以此对齐（hub 侧多出的标 cancelled，守护进程侧多出的补录）。

### 3.3 只展示的两类

Claude Code 的 Notification 钩子映射为 `question`，Stop 钩子映射为 `done`。Codex 的 notify 事件和 Gemini 的对应事件按同样方式映射，具体覆盖范围在动手前的实验里确认（见第 8 节）。这两类钩子发送后立即退出，不阻塞。同一会话已有 pending 的 permission 时，守护进程压掉 question 事件，避免一件事进两条。

### 3.4 webhook 来源

外部系统向 `POST /api/webhook/approvals` 提交，携带来源令牌、标题、说明、回调 URL 与回调令牌。用户决定后 hub 调回调。走同一收件箱与审计。

## 4. 策略引擎

`internal/policy` 是纯 Go 包，无 IO。输入统一载荷和规则集，输出 allow、deny 或 ask 及依据。守护进程和 hub 跑同一份代码，hub 上可做“这条规则会怎么判”的试算。

### 4.1 风险分类

每条请求先定级，不看规则。七档由低到高：`read`、`write_in_project`、`unknown`、`write_outside`、`network`、`outward`、`destructive`。`unknown` 是命令表之外无法判断的命令，默认询问。

- `shell`：命令按 `&&`、`;`、`||`、`|` 拆段，每段按内置表定级，取最高档。只读表含 ls、cat、grep、find、git status、git diff、git log、go test、npm test 等；对外表含 git push、gh pr create、gh release、npm publish、各云 deploy、发邮件与付款 CLI；不可逆表含 rm -rf、git reset --hard、git push --force、DROP TABLE、docker system prune。安装类命令（npm install、pip install、brew install）归 `network`。表外命令一律 ask。读敏感路径与写一样按 `destructive`。
- `file_write`：路径在项目根目录内为 `write_in_project`，之外为 `write_outside`。敏感路径表（`.env*`、`*.pem`、`*.key`、`~/.ssh/**`、`~/.aws/**` 等）按 `destructive` 处理。
- `mcp`：默认 `network`，即默认 ask，按服务名与工具名放行。
- `network`：默认 `network`，按域名放行。

### 4.2 匹配优先级

1. **内置底线。** `outward` 和 `destructive` 只有 `source: user` 的规则能放行，`learned` 规则永远不能。引擎在生成和导入规则时都强制校验。
2. **用户规则与学来的规则。** 范围越具体越优先：project 压 machine，machine 压 global。同一范围内 deny 压 allow，allow 压 ask。
3. **默认值。** 无规则命中时按风险等级：`read` 和 `write_in_project` 放行，其余 ask。

### 4.3 规则格式

匹配条件为结构化字段，不是 DSL。YAML 导入导出，内置一个默认规则包。

```yaml
version: 1
rules:
  - id: builtin-readonly
    source: builtin
    scope: global
    match: {risk: read}
    action: allow
  - id: user-push-railse
    source: user
    scope: {project: "~/Projects/railse-opc"}
    match: {payload: shell, command_prefix: "git push origin"}
    action: allow
  - id: learned-0042
    source: learned
    scope: {project: "~/Projects/blog"}
    match: {payload: shell, command_prefix: "npm run"}
    action: allow
    created_from: "6f1c…"
```

`match` 支持的字段：`payload`、`risk`、`command_prefix`、`command_regex`、`path_glob`、`mcp_server`、`mcp_tool`、`domain`。多个字段为与关系。

网页第一版对规则只做查看、启停、删除；新增规则靠“批准并记住”或 YAML 导入。

### 4.4 从决定里学

“批准并记住”从当前请求生成规则草案：`shell` 取命令前两个词作 `command_prefix`；`file_write` 取所在目录作 `path_glob`；`mcp` 取服务加工具名。默认 scope 为当前项目，可一键改为 global。保存前展示草案。

同一项目内同一 `command_prefix` 七天内手动批准三次，收件箱显示建议建规则的条目。

### 4.5 总开关

网页上“暂停自动审批”开关，打开后所有请求走 ask；可按机器单独暂停。每条自动决定记录命中的规则 id，收件箱历史中可见依据。

### 4.6 预留

规则 `action` 预留第四个值 `judge`，第二期接小模型判定灰色地带。第一版不实现。

## 5. 安全与错误处理

### 5.1 三段链路的认证

- 钩子到守护进程：用户家目录下的 Unix socket，权限 0600。机器上不开网络端口。
- 守护进程到 hub：`railse setup` 用 hub 生成的一次性接入码（`railse hub token`，10 分钟有效）换取本机长期密钥，存 0600 配置文件，WebSocket 握手出示。网页可按机器吊销。
- 手机到 hub：首次启动 hub 设密码；登录换长期 token 放 httpOnly cookie；登录接口限速。TLS：给 `--domain` 时 hub 自行向 Let's Encrypt 申请证书；否则 `--listen` 监听明文，供反向代理或只绑 Tailscale 地址使用。

### 5.2 hub 的权力被限死

hub 只能对 agent 已提出的请求回 allow 或 deny，没有任何向机器下发命令的通道。`outward` 与 `destructive` 只认手写规则，hub 侧也无法批量放行它们。

### 5.3 数据最小化

请求只存命令、路径、参数和截断到 4KB 的改动预览，不存文件全文。守护进程发往 hub 前脱敏：形如 token、API key、`Authorization:` 头、`password=` 的片段替换为占位符。审计表只追加，无删除接口，只有导出。

### 5.4 错误处理总原则

钩子遇到任何异常都返回“不作决定”，让 CLI 弹原生审批框，并写本地日志。覆盖：守护进程未运行、socket 连不上、输入格式不认识、守护进程中途崩溃。agent 永远不会因 Railse 故障而卡死或被误批。

其他情况：

- 推送端点返回 410 或连续失败：删除该订阅，网页顶部提示重新开启通知。
- 协议版本不一致：握手拒绝并给明确提示，网页显示哪台机器需升级。
- hub 数据库写失败：WebSocket 回 nack，守护进程保持本地 pending 直到期限，按过期处理。
- 守护进程由 launchd 或 systemd 托管，崩溃自动拉起；未完成请求在本地 SQLite，重连后对齐。

### 5.5 `railse doctor`

检查：守护进程是否在跑、hub 是否可达、三个 CLI 配置中钩子是否装好、CLI 版本是否支持对应钩子事件、钩子超时是否设到位、socket 与配置文件权限。

## 6. 测试与第一版边界

### 6.1 测试分层

1. **策略引擎**：表驱动单元测试，维护一份真实命令语料，每条标注期望风险等级与决定。每次误判先加语料再改代码。
2. **适配器**：从三个 CLI 的真实版本录制钩子输入 JSON 作为样本，按 CLI 版本标注入库。断言统一载荷正确，以及 allow、deny、不作决定三种输出格式正确。CLI 升级时重录样本。
3. **守护进程**：进程内集成测试。假钩子客户端连 Unix socket，httptest 起假 hub。覆盖 3.2 的四种边界，加故障注入验证 5.4 的总原则。
4. **hub 与网页**：hub 接口用临时 SQLite 测。网页一条 Playwright 端到端：登录、看到请求、批准、“批准并记住”后规则列表多一条。
5. **真实 CLI 冒烟**：脚本以 headless 方式起 Claude Code 并触发一次 Bash 调用，跑完整链路。需本机 CLI 与登录态，不进 CI，发布前手动跑。

CI：GitHub Actions，Go 测试带 `-race`、golangci-lint、网页构建、交叉编译 darwin/arm64 与 linux/amd64、linux/arm64，goreleaser 出包。

### 6.2 第一版包含

- 适配器：Claude Code、Codex CLI、Gemini CLI、通用 webhook。
- 请求种类：`permission` 与 `webhook` 可操作；`question` 与 `done` 只展示。
- 策略：风险分类器、三层规则、默认规则包、YAML 导入导出、批准并记住、规则建议、暂停总开关。
- 网页：登录、按项目分组的收件箱、请求详情、批准/拒绝/记住、带依据的历史、规则列表（看、启停、删）、机器列表与吊销、开启通知。
- 推送：仅 Web Push。
- 平台：macOS arm64、Linux amd64 与 arm64。
- 安装：curl 一行装二进制、`railse setup`、`railse doctor`。

### 6.3 第一版不含

见 0.5。

### 6.4 验收标准

- Mac 与服务器同时跑五个会话一整天，手机在线时没有任何一条待审批超过一分钟未被看到。
- 使用一周后，审计中八成以上的 `permission` 请求由规则自动决定。
- 从推送到 agent 继续，中位数十秒以内。
- 故障注入测试全部通过。

## 7. 动手顺序

1. 实验：在作者当前版本的三个 CLI 上实测钩子超时上限、输入输出格式、`question`/`done` 事件覆盖。一到两天，产出录制样本。
2. 策略引擎与分类器，带语料测试。
3. 三个适配器与守护进程，本地闭环（无 hub 时按原生审批框兜底）。
4. hub、存储、WebSocket 协议、守护进程接入。
5. PWA 收件箱与 Web Push。
6. `setup`、`doctor`、发布流水线、安装脚本。
7. 真实 CLI 冒烟，作者自用一周，对照 6.4 验收。

细化到任务级别由实施计划负责。

## 8. 动手前必须验证的事项

| 事项 | 为什么 | 不成立时的替代 |
|---|---|---|
| Claude Code `PermissionRequest` 钩子能否返回 allow/deny/不作决定，超时可否设到 24 小时 | 第 3 节的过期策略依赖它 | 用 CLI 允许的最大超时，期限相应缩短 |
| Codex `PermissionRequest` 钩子的输入输出 schema | 适配器依赖 | 退回 `approval_policy` 加 notify 只展示，Codex 的自动审批第二期补 |
| Gemini `BeforeTool` 能否表达 allow，还是只能 block/继续 | 决定 Gemini 适配器能否自动放行 | 只实现 deny 与 ask，allow 交给 Gemini 自身的审批模式 |
| 三个 CLI 的 `question`/`done` 事件是否都有 | 3.3 的覆盖范围 | 没有的 CLI 第一版只有 permission |
| iOS 上 PWA 的 Web Push 在作者手机上可用 | 推送是核心路径 | 第一版先加一个 Bark 渠道 |

## 附录：参考

- Claude Code hooks 文档：https://code.claude.com/docs/en/hooks
- Codex CLI hooks GA：https://codex.danielvaughan.com/2026/05/25/codex-cli-hooks-after-ga-event-model-trust-verification-production-patterns/
- 三家 CLI 钩子对比：https://lilting.ch/en/articles/gemini-cli-hooks-research
- herdr 对比页（仅作定位参照）：https://herdr.dev/compare/
