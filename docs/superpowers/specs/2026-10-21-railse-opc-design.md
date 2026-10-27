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
| 第 2 份计划执行中的偏离（2026-10-22 已拍板） | 接受：暂停自动审批时用户写的 deny 仍生效；审计与请求状态增加 `passthrough`；`setup hooks` 删旧钩子按条目而非按组。已改（2026-10-25，第 5 份）：Claude 的钩子改写到 `.claude/settings.local.json`，旧版写在 `settings.json` 的由 `setup hooks` 迁走 |
| 第 3 份计划执行前预检的五处取舍（2026-10-22 已拍板） | 请求已送到 hub 后断线：再等 30 秒，重连上继续等，否则交回终端，审计记 `expired`/`hub_unreachable`（守护进程侧加连接存活检测）。hub 下发的 `source: user` 规则可以放行 `outward`/`destructive`（网页导入即手写，见 5.2 的说明），守护进程不降级；hub 的“记住”永远不能生成这类放行，导入含这类放行时审计记录并在网页标出。手机上的备注第一版不回给 agent：钩子只收固定文案，备注只进审计。webhook 的回调令牌是出站令牌，明文存库作为“只存哈希”的例外，回调完成或重试用尽后清空。总开关生效值 = 本机暂停 ‖ hub 暂停，两者分开存，hub 的值不覆盖本机的。 |
| 第 4 份计划动笔前的三件事（2026-10-23 已拍板） | 脱敏吞掉命令后半截要彻底修：shell 语境里不在引号与 heredoc 内的裸值遇到 `;`、`\|`、`&&`、反引号、`$(` 即停（裸词本就到此结束，不会多漏秘密），`has_redacted` 标记与网页告警保留做兜底，作为第 4 份的第一个 Task。出站与签名类密钥（webhook 回调令牌、VAPID 私钥、推送订阅密钥、第 5 份的 ACME 账户私钥与证书私钥）明文存储，作为“只存哈希”的通用例外，见 5.3。“批准并记住”的草案预览与范围选择按 4.4 在第 4 份实现；“七天三次建议建规则”延后到自用之后再定。 |
| 第 5 份计划执行前的决策（2026-10-24） | 推送订阅删除要连续失败且满 24 小时（第 4 份）；连续 3 次 401 才停用守护进程的 hub 连接；Claude 钩子写 .claude/settings.local.json（已改）；ACME 只用 autocert 直出，证书与账户私钥明文存数据目录 acme/（出站与签名密钥例外）；hub 机器的系统单元用 AmbientCapabilities 绑 443/80，不以 root 运行 hub；MCP 参数里的命令字段按命令行语境脱敏。 |
| 第 5 份计划执行中的决策（2026-10-25） | `railse doctor` 唯一会起的进程是三个 CLI 的 `--version`（10 秒超时，sudo 下不跑）。`--system` 只配 `--hub`，默认数据目录 `/var/lib/railse-hub`（`StateDirectory=railse-hub`），doctor 的默认 hub 目录在 Linux 上回退到它。hub 不以 root 运行：文案只提 `CAP_NET_BIND_SERVICE`，hub 以 euid 0 启动时警告不拒绝，`railse setup all` 拒绝 root。golangci-lint 的 errcheck 只对 `_test.go` 排除。发布归档带 `docs/*.md`。GitHub 仓库 `xufei547/railse-opc`（原定 `railse/railse-opc`，2026-10-26 因 GitHub 上没有 railse 组织改到作者名下，模块路径随之改为 `github.com/xufei547/railse-opc`）。MCP 命令键下的 argv 数组与 shell 解释器的 args 按命令行语境脱敏。为 autocert 引入 `golang.org/x/net` 与 `golang.org/x/text` 作 indirect（`x/crypto` 的传递依赖，不算新增第三方依赖）。 |
| Gemini CLI 最低版本 | 0.65.0。更早的版本在用户确认之后才触发 `BeforeTool`，钩子无法在审批前拦截（2026-10-21 实验）；`railse doctor` 对低于 0.65.0 的版本报警 |

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
  internal/autostart/  # launchd/systemd 的命名与路径
  internal/doctor/     # railse doctor 的检查项
  internal/hookclient/ # 钩子侧客户端
  internal/hooksconf/  # 三个 CLI 钩子配置的生成、合并与检查
  internal/hub/        # hub 服务端：REST、/ws/agent、/ws/client、webhook、推送队列
  internal/hub/push/   # Web Push：RFC 8291 加密、RFC 8292 VAPID
  internal/hub/store/  # SQLite 存储
  internal/hub/webui/  # 嵌入并托管网页
  internal/hub/wire/   # 守护进程与 hub 之间的 WebSocket 帧
  internal/proto/      # 钩子与守护进程之间的一行 JSON 协议（守护进程与 hub 的帧在 internal/hub/wire）
  internal/redact/     # 凭据脱敏
  web/                 # Vite + React PWA，构建产物嵌入 internal/hub/webui
  scripts/             # 冒烟脚本（smoke-claude.sh、smoke-claude-hub.sh）、网页端到端（e2e-web.sh）、安装脚本测试（test-install.sh）
  install.sh           # 一行安装
  .github/workflows/   # CI 与发布
  .goreleaser.yaml     # 三平台出包
  Makefile             # make web、build、test、lint、e2e、release-check
  docs/
```

## 2. 数据模型

全部存在 hub 的 SQLite，守护进程只缓存规则快照和未完成请求。

### 2.1 实体

**Machine 机器**：id、name、enrolled_at、last_seen、agent_version、status（online、offline）、secret_hash。

**Session 会话**：id、machine_id、tool（claude、codex、gemini、webhook）、cli_session_id、cwd、project_name（项目根目录名）、started_at、last_event_at。会话只从钩子事件得知，不扫进程。

**Request 请求**：

| 字段 | 说明 |
|---|---|
| id | UUID，由守护进程生成 |
| session_id、machine_id | 归属 |
| kind | `permission`、`question`、`done`、`webhook` |
| payload | 统一载荷，见 2.2 |
| risk | 风险等级，见 4.1 |
| state | `pending`、`auto_allowed`、`auto_denied`、`approved`、`denied`、`passthrough`、`expired`、`cancelled` |
| decided_by | 命中的规则 id、`user`、`timeout`、`terminal`（终端里处理了）、`hub_unreachable`、`no_hub`、`paused`、`error` |
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
pending ──没有 hub、总开关暂停、或决定来源出错，交回终端──▶ passthrough（decided_by 为 no_hub、paused、error）
```

`passthrough` 与 `expired`、`hub_unreachable` 的区别：`expired` 是等满期限；`hub_unreachable` 是本机守护进程连不上 hub 等 30 秒后放弃（见 3.2）；`passthrough` 是根本没去等。三者对 CLI 的效果相同，都是让终端弹原生框。

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
- `file_write`：路径在项目根目录内为 `write_in_project`，之外为 `write_outside`。项目根是工作目录所在的 git 仓库根；不在 git 仓库里时，工作目录是家目录下的普通目录（不在点开头的目录或 `~/Library` 之下）才是工作目录本身，否则没有项目根；家目录、它的上级与根目录不当项目根。没有项目根时没有路径在项目内。敏感路径表（`.env*`、`*.pem`、`*.key`、`~/.ssh/**`、`~/.aws/**`，以及登录或开机时自动运行的配置如 `~/Library/LaunchAgents/**`、`~/.config/systemd/**` 等）按 `destructive` 处理。路径为空或无法解析（含变量、通配符、`~user`）时为 `unknown`。
- `mcp`：默认 `network`，即默认 ask，按服务名与工具名放行。
- `network`：默认 `network`，按域名放行。

### 4.2 匹配优先级

1. **内置底线。** `outward` 和 `destructive` 只有 `source: user` 的规则能放行，`learned` 规则永远不能。引擎在生成和导入规则时都强制校验。
2. **用户规则与学来的规则。** 用户规则整体优先于内置和学来的规则。范围越具体越优先：project 压 machine，machine 压 global（用户 deny 除外）。同一范围内 deny 压 allow，allow 压 ask。
3. **默认值。** 无规则命中时按风险等级：`read` 和 `write_in_project` 放行，其余 ask。

放行 `destructive` 还必须在规则里显式写 `risk: destructive`，仅 `source: user` 不够。用户的 deny 规则优先于一切，且复合命令中任一段命中即生效；allow 规则要求每一段都命中，且遇到无法分析的段、注入型环境变量、含 `$` 展开或未加引号通配的词，或指向空设备或标准流（/dev/null、/dev/stdout、/dev/stderr、/dev/stdin、/dev/tty、/dev/fd/*）以外的重定向时不命中。

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
    match: {payload: shell, command_prefix: "npm run", risk: write_in_project}
    action: allow
    created_from: "6f1c…"
```

`match` 支持的字段：`payload`、`risk`、`command_prefix`、`command_regex`、`path_glob`、`mcp_server`、`mcp_tool`、`domain`。多个字段为与关系。

网页第一版对规则只做查看、启停、删除；新增规则靠“批准并记住”或 YAML 导入。

### 4.4 从决定里学

“批准并记住”从当前请求生成规则草案：`shell` 取命令前两个词作 `command_prefix`；文件操作、网络与远程命令、shell 包装和云/集群/包管理这类按子命令分发的 CLI 取整条命令并用锚定正则限定；`file_write` 取所在目录作 `path_glob`；`mcp` 取服务加工具名。默认 scope 为当前项目，可一键改为 global。保存前展示草案。

同一项目内同一 `command_prefix` 七天内手动批准三次，收件箱显示建议建规则的条目。

### 4.5 总开关

网页上“暂停自动审批”开关，打开后所有请求走 ask，但用户手写的 deny 规则仍然生效（暂停是为了少放行，不是为了多放行）；可按机器单独暂停。每条自动决定记录命中的规则 id，收件箱历史中可见依据。

### 4.6 预留

规则 `action` 预留第四个值 `judge`，第二期接小模型判定灰色地带。第一版不实现。

## 5. 安全与错误处理

### 5.1 三段链路的认证

- 钩子到守护进程：用户家目录下的 Unix socket，权限 0600。机器上不开网络端口。
- 守护进程到 hub：`railse setup` 用 hub 生成的一次性接入码（`railse hub token`，10 分钟有效）换取本机长期密钥，存 0600 配置文件，WebSocket 握手出示。网页可按机器吊销。
- 手机到 hub：首次启动 hub 设密码；登录换长期 token 放 httpOnly cookie；登录接口限速。TLS：给 `--domain` 时 hub 自行向 Let's Encrypt 申请证书；否则 `--listen` 监听明文，供反向代理或只绑 Tailscale 地址使用。

### 5.2 hub 的权力被限死

hub 只能对 agent 已提出的请求回 allow 或 deny，没有任何向机器下发命令的通道。`outward` 与 `destructive` 只认手写规则，hub 侧也无法批量放行它们：这里的“手写”包括用户在网页上导入的 `source: user` 规则（规则以 hub 为唯一真相，网页导入就是手写的途径）；“无法批量放行”指 hub 的“记住”功能永远不生成这两类的放行规则，decide 接口只能逐条回答，导入含这类放行的规则集时写审计并在网页上标出。

### 5.3 数据最小化

请求只存命令、路径、参数和截断到 4KB 的改动预览，不存文件全文。守护进程发往 hub 前脱敏：形如 token、API key、`Authorization:` 头、`password=` 的片段替换为占位符；shell 语境下的脱敏按 shell 词法边界停止，不把紧随其后的命令一起抹掉；交给 shell 解释执行的引号内容（`sh -c`、`eval`、`ssh` 的远端命令，以及喂给它们的 heredoc 正文）本身是 shell 代码，按 shell 语境递归处理，其余引号内与 heredoc 正文整段抹掉；含占位符的请求在网页上带告警。审计表只追加，无删除接口，只有导出。

hub 需要验证的入站凭据（机器密钥、登录会话 token、webhook 来源令牌、登录密码）只存哈希。hub 需要原样使用的出站与签名类密钥（webhook 回调令牌、VAPID 私钥、推送订阅密钥、ACME 账户私钥与证书私钥）明文存储，但只放在数据目录 0600 的文件或数据库里，永不经 API 返回，可轮换，用完即清的（回调令牌）用完即清。

### 5.4 错误处理总原则

钩子遇到任何异常都返回“不作决定”，让 CLI 弹原生审批框，并写本地日志。覆盖：守护进程未运行、socket 连不上、输入格式不认识、守护进程中途崩溃。agent 永远不会因 Railse 故障而卡死或被误批。

其他情况：

- 推送端点返回 410 或连续失败：删除该订阅，网页顶部提示重新开启通知。
- 协议版本不一致：握手拒绝并给明确提示，网页显示哪台机器需升级。
- hub 数据库写失败：WebSocket 回 nack，守护进程保持本地 pending 直到期限，按过期处理。
- 守护进程由 launchd 或 systemd 托管，崩溃自动拉起；未完成请求在本地 SQLite，重连后对齐。

### 5.5 `railse doctor`

检查：守护进程是否在跑、hub 是否可达、三个 CLI 配置中钩子是否装好、CLI 版本是否支持对应钩子事件、钩子超时是否设到位、socket 与配置文件权限。检查项的完整清单与修复提示见 `docs/setup.md`。

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

| 事项 | 为什么 | 不成立时的替代 | 实测结果（2026-10-21，详见 `2026-10-21-hook-spike-findings.md`） |
|---|---|---|---|
| Claude Code `PermissionRequest` 钩子能否返回 allow/deny/不作决定，超时可否设到 24 小时 | 第 3 节的过期策略依赖它 | 用 CLI 允许的最大超时，期限相应缩短 | 成立（2.1.280）：三种输出都生效，`timeout: 86400` 被接受且 660 秒后作答仍生效，到期按不作决定处理（交互弹原生框，`-p` 自动拒绝）。 |
| Codex `PermissionRequest` 钩子的输入输出 schema | 适配器依赖 | 退回 `approval_policy` 加 notify 只展示，Codex 的自动审批第二期补 | 部分成立（0.145.0）：schema 已录制，输出与 Claude 同形，但只在交互会话触发（`codex exec` 强制 never），且挂起约 9 分钟后会被模型在代码模式里主动放弃并重发。 |
| Gemini `BeforeTool` 能否表达 allow，还是只能 block/继续 | 决定 Gemini 适配器能否自动放行 | 只实现 deny 与 ask，allow 交给 Gemini 自身的审批模式 | 不成立，采用替代方案：0.28.0 在确认后才触发，0.65.0 在确认前触发但 allow 不跳过原生框；yolo 下钩子超时或崩溃会放行。 |
| 三个 CLI 的 `question`/`done` 事件是否都有 | 3.3 的覆盖范围 | 没有的 CLI 第一版只有 permission | done 三家都有（`Stop`、`Stop`/notify、`AfterAgent`）；question 只有 Claude（`Notification` 的 `permission_prompt`/`idle_prompt`）和 Gemini（`Notification` 的 `ToolPermission`），Codex 没有。 |
| iOS 上 PWA 的 Web Push 在作者手机上可用 | 推送是核心路径 | 第一版先加一个 Bark 渠道 | 部分成立（iOS 26.3，详见 `2026-10-21-ios-webpush-findings.md`）：主屏幕 PWA 在前台、后台都能收到，延迟 1.4 至 2.5 秒，点通知能打开指定页面；完全关闭、锁屏、数小时后订阅是否有效未验证，暂不加 Bark，留到 PWA 真机联调补测。 |

## 附录：参考

- Claude Code hooks 文档：https://code.claude.com/docs/en/hooks
- Codex CLI hooks GA：https://codex.danielvaughan.com/2026/05/25/codex-cli-hooks-after-ga-event-model-trust-verification-production-patterns/
- 三家 CLI 钩子对比：https://lilting.ch/en/articles/gemini-cli-hooks-research
- herdr 对比页（仅作定位参照）：https://herdr.dev/compare/
