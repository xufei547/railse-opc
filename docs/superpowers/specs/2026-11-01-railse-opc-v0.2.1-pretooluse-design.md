# Railse OPC v0.2.1：审批搬到 PreToolUse，auto 模式下也生效 — 设计

日期：2026-11-01。总设计见 `2026-10-21-railse-opc-design.md`（§0.6 有本版决策行）；调研依据见 `docs/research/2026-11-01-approval-pain-points.md`。

## 0 为什么

2026-11-01 在作者的 Mac 上实测（Claude Code 2.1.280，用户级 `defaultMode: auto`）：让 Claude 依次跑 `cat`、项目内 `echo >`、`curl`、`git push origin main`、`rm -rf scratch` 五条命令，**五条全部直接执行，Railse 的钩子一次都没触发**（本机 `audit.jsonl` 与 hub 都无记录）。原因：Railse 的审批挂在 `PermissionRequest` 上，该事件只在 Claude Code 决定要问人时触发；auto 模式由其分类器直接放行，连往外发与破坏性操作都没问。auto 模式自 2.1.283 起是官方默认，所以对多数 Claude Code 用户，Railse 的规则与硬底线目前**形同虚设**，README 里"每次要跑命令都先过规则"的说法不成立。

同日实测 `PreToolUse`：`claude -p --permission-mode default|auto` 下 Bash 调用都触发，输入带 `permission_mode`、`tool_use_id`、`tool_input.command`（样本已存 `internal/adapter/testdata/pretooluse-bash-{default,auto}.json`）。v0.1.5 的选择题就靠它在 auto 模式下进了手机。

## 1 目标与不做

**目标**：Claude Code 不论什么权限模式，每个 Bash / 文件写 / 联网类工具调用都先经过 Railse 的规则；规则定得了的立刻 allow 或 deny，往外发与破坏性的一律到手机等人，其余按模式处理；不比 auto 模式更吵。

**顺带**（都是小件）：`curl … | sh` 一类定为破坏性；手机网页回到前台时发现 hub 升级过就自动重载；README 两份改准说法并补能力矩阵（含 README.en 第 151 行"界面只有中文"的遗留错误）。

**不做（挪到 v0.2.2 或更后）**：统计页与审计导出；"同一前缀 7 天批 3 次建议建规则"；推送队列落库；Codex 的 PreToolUse（官方文档写 `ask` "parsed but not supported yet"，先不动）；Antigravity 适配。

## 2 机制

### 2.1 钩子安装

`railse setup hooks --claude` 把 Claude 的 `PreToolUse` 匹配器从 `AskUserQuestion` 改为 `AskUserQuestion|Bash|Edit|Write|MultiEdit|NotebookEdit|WebFetch`（正则匹配器），超时 86400 秒不变；`PermissionRequest` 钩子保留作兜底（覆盖 MCP 工具、Task 等 PreToolUse 没匹配到的工具）。老安装重跑一次 `setup hooks --claude` 即升级；`doctor` 的 `hooks.claude` 检查匹配器含 `Bash`，缺了报 warn 并给命令。

### 2.2 适配器：PreToolUse 的工具 → 载荷

| 工具 | 载荷 | 取自 |
|---|---|---|
| Bash | shell | `tool_input.command` |
| Edit / Write / MultiEdit / NotebookEdit | file_write | `tool_input.file_path`（`notebook_path`） |
| WebFetch | network | `tool_input.url` |
| AskUserQuestion | ask（不变） | `tool_input.questions` |

与现有 `PermissionRequest` 的映射保持同一套函数，不另写一份。请求 `Kind` 仍是 `permission`，新增字段 `Source: "pretooluse" | "permissionrequest"` 与 `PermissionMode`（`default`/`acceptEdits`/`plan`/`auto`/`bypassPermissions`/`dontAsk`，原样透传，审计与网页"详情"里显示），`ToolUseID` 用于 2.4 的去重。

### 2.3 决策与输出

PreToolUse 的输出格式：`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow|deny|ask","permissionDecisionReason":"…"}}`；不表态时输出空（exit 0），Claude 走自己的流程。

| 引擎结果 | 任何模式 |
|---|---|
| 规则 allow（内建只读、项目内写；用户学来的 allow） | `allow`，理由 `Railse：命中规则 X` |
| 规则 deny | `deny`，理由同现在 |
| 定不了，风险为 outward / destructive（硬底线） | 送 hub 等手机：批 → `allow`，拒 → `deny`；没接 hub / hub 不可达 / 等待过期 → `ask`（见 2.6） |
| 定不了，风险为 network / write_outside / unknown | 看 2.3.1 |

**2.3.1 中间三级不看模式（本版的取舍，2026-11-01 实施后改定）**：Railse 不能比 Claude 自己更吵。PreToolUse 来的中间三级（network / write_outside / unknown）**任何模式都不表态**：default、acceptEdits、plan 下由 Claude 自己的 allow 列表（`settings.json` 的 `permissions.allow`）先走，auto 下由分类器先走，`bypassPermissions` / `dontAsk` 按用户要求不问；Claude 若决定要问人，会触发 `PermissionRequest`，那时再按现有流程送手机。（初稿是“auto 下不表态、其余模式送手机”，实施后发现那样会让 default 模式比装 Railse 前更吵，用户在 Claude 里配的 allow 列表也失效，故改。）守护进程加一个本机开关：`~/.railse/settings.yaml` 里 `claude_undecided: classifier | phone`（默认 `classifier`；不放 rules.yaml，因为那份文件由 hub 下发覆盖、且解析时拒绝未知字段），`railse agent reload` 或重启后生效；改成 `phone` 时 PreToolUse 直接把中间三级送手机，任何模式。

**为什么硬底线不看模式**：这正是 auto 模式放行了 `git push` 与 `rm -rf` 的那一类；Railse 的承诺是"这两级永远不自动过"，模式不能改变承诺。

**`ask` 的语义要实测**：`permissionDecision: "ask"` 在 auto 模式下是强制弹框还是回到分类器，官方文档没写清；实施第一个任务就是录样本定下来，并写进 hub.md。两种结果都可接受：弹框 = 交回终端；回分类器 = 等同不表态。

### 2.4 同一调用不问两次

PreToolUse 不表态后，Claude 若要问人会再触发 `PermissionRequest`。2.1.280 的 PermissionRequest 样本里没有 `tool_use_id`，所以去重键用：有 `tool_use_id` 就用它，否则用 `session_id + 载荷哈希`，窗口 10 分钟。守护进程记最近 10 分钟内 PreToolUse 已处理过的键与结果：

- PreToolUse 已送手机且得到交回终端（passthrough）→ 同 id 的 `PermissionRequest` 直接不表态，不再送手机。
- PreToolUse 因中间三级不表态（2.3.1）→ 同 id 的 `PermissionRequest` 按现有流程送手机（这是 Claude 自己升级成提问，正该问）。
- 其余情况（allow/deny 已回）不会再有 `PermissionRequest`。

审计里两次事件都记，`tool_use_id` 相同可对上。

### 2.5 PreToolUse allow 的后果要想清

PreToolUse 返回 `allow` 会让 Claude **跳过自己的权限系统**（包括 auto 模式的分类器与用户的 deny 列表之外的检查）。所以：

- 内建 allow 只覆盖只读与项目内写两级，不扩。
- 用户 `settings.json` 里的 deny 规则 Claude 仍会先于钩子生效（deny 优先），不受影响。
- "记住"学来的 allow 规则现在在 auto 模式下也是终局决定，网页上"记住"的说明加一句"记住后 Claude 自己的检查也会跳过"。

### 2.6 失败即不表态；硬底线送不到手机时要终端确认

守护进程不在、hub 30 秒不可达、解析失败、超时：PreToolUse 输出空，Claude 照常自己处理；与现在 `PermissionRequest` 的 fail-open 一致。

例外（2026-11-01 实施后改定）：对外（outward）与不可逆（destructive）两档在**没接 hub、hub 不可达、等待过期**这三种情况下，PreToolUse 输出 `permissionDecision: "ask"`，理由 `Railse：连不上 hub，这类操作要你亲自确认`，任何模式都如此。原因：Task 1 实测 `ask` 在 auto 模式下也强制让人确认；不表态的话 auto 的分类器会直接放行 `git push`、`rm -rf`（实测没接 hub 时 `rm -rf` 被执行）。`ask` 不把人挡在终端外，人在就能批；`-p` 下等于拒绝，可以接受。守护进程不在、钩子解析失败、守护进程出错或停止时仍输出空（钩子拿不到守护进程的判断）。hub 不可达时终端照旧打一行 `railse hook: hub unreachable, falling back to Claude's own prompt`（为此 `proto.Response` 加 `decided_by` 字段，钩子据此判断）。

## 3 顺带的三件小事

- **`curl | sh` 定级**：管道或命令替换把远端内容喂给 `sh`/`bash`/`zsh`/`python`/`node`（`| sh`、`| bash -s`、`bash -c "$(curl …)"`、`eval "$(curl …)"`、`sh <(curl …)`）定为 destructive；语料 `shell_corpus.tsv` 补行。
- **网页回到前台自动重载**：`visibilitychange` 为 visible 时 GET `/api/version`，与页面构建时嵌入的版本不同就 `location.reload()`，同一次打开只重载一次；开发版（`dev`）不比较。
- **README 两份**："它怎么做"第 1 条改为"Claude Code 的每个工具调用先经 Railse（PreToolUse），不论哪种权限模式"；限制里写明 auto 模式下中间三级默认交给 Claude 的分类器；加一张能力矩阵（每家 CLI：能批 / 只能拒 / 只能提醒 / 能答题 / auto 模式下是否生效）；删掉 README.en 第 151 行"UI is Chinese only"。

## 4 验收

1. 作者 Mac，`defaultMode: auto`，重跑 2026-11-01 的五条命令：`cat` 与项目内 `echo >` 本地放行（审计 `auto_allowed`）；`curl` 不表态、由 Claude 分类器处理；`git push` 与 `rm -rf` 推到手机，拒绝后终端显示 `Railse: Denied on your phone`，命令没有执行。
2. `--permission-mode default` 下同五条：`curl` 在 PreToolUse 不表态，Claude 弹自己的确认前触发 PermissionRequest，进手机（只进一次）。
3. `claude_undecided: phone` 时 auto 模式下 `curl` 在 PreToolUse 直接进手机。
4. 手机上对 `git push` 选"让它在终端问我"后，Claude 只问一次，不会再来一条推送。
5. hub 停掉：`git push`、`rm -rf` 约 30 秒后终端强制确认（PreToolUse 回 `ask`），其余照常由 Claude 处理；终端有一行回退提示。
6. `echo 'rm -rf ~' | sh` 定为 destructive（`railse policy eval`）。
7. 升级 hub 后，手机上开着的旧页面切出再切回会自动重载。

## 5 决策记录

| 决策 | 为什么 |
|---|---|
| 审批搬到 PreToolUse 而不是要求用户关掉 auto 模式 | auto 是官方默认，要求用户改模式等于放弃多数用户 |
| 硬底线不看模式，中间三级任何模式都先交给 Claude 自己 | 守住承诺的同时不比 Claude 自己更吵、不让用户的 allow 列表失效；开关留给想全管的人 |
| 硬底线送不到手机时回 `ask` | 不表态会让 auto 的分类器直接放行；`ask` 让人在终端确认，不把人挡在外面 |
| 保留 PermissionRequest 作兜底 | MCP、Task 等工具 PreToolUse 不匹配；Claude 自己升级成提问时仍该到手机 |
| 不做 Codex 的 PreToolUse | 官方文档写 `ask` 尚不支持，Codex 的 PermissionRequest 现状可用 |
| 统计页与审计导出挪到 v0.2.2 | 本版先把承诺做实，再算账 |
