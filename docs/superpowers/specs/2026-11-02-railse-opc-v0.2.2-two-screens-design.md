# Railse OPC v0.2.2：终端与手机同屏、手机上说人话 — 设计

日期：2026-11-02。总设计见 `2026-10-21-railse-opc-design.md`（§0.6 有本版决策行）；上一版见 `2026-11-01-railse-opc-v0.2.1-pretooluse-design.md`。

## 0 为什么

v0.2.1 上线当天作者真用了一下午（4 个并行的 Claude Code 会话、auto 模式），报了两个问题：

1. **PC 端看不到手机上的内容，终端像卡住了。** v0.2.1 让硬底线（outward / destructive）在 `PreToolUse` 钩子里原地等手机。2026-11-02 用 expect 驱动交互式 Claude Code 2.1.280 复现：等待期间终端只有 `⏺ Bash(…) ⎿ Waiting… (running PreToolUse hook · 2s)`，不显示命令说明，也不说已发到手机；钩子回 deny 时显示 `Error: PreToolUse:Bash hook error: <理由>`。v0.1.x 走 `PermissionRequest` 时终端自己先弹确认框、钩子只是同时送手机，两边都看得到，所以这是 v0.2.1 的退步。
2. **手机上看不懂。** 三层原因：验收时界面切到了 English 没切回；送到手机的 12 条几乎全是别的会话里一两百字的复合命令，卡片"做什么"的解析器认不出整条时退化成"运行命令：S=/private/tmp/…"；引擎理由只有一句"无规则命中，按 destructive 默认询问"，不说是命令里哪一段触发的（真实触发点很具体：`rm -rf after`、`pkill`、ssh 里带 `sudo`、脚本经管道交给 python、heredoc 里有 `DROP TABLE`）。另外 12 条里 3 条是会话临时目录（scratchpad）里的 `rm -rf` / 写文件，纯噪音。

同一次实验给出了解法的依据（记录见 `docs/superpowers/specs/2026-11-01-pretooluse-ask-findings.md` 的 2026-11-02 补记）：**交互式终端里 PreToolUse 回 `ask`，终端弹出正常的确认框（命令 + "Hook PreToolUse:Bash requires confirmation for this command: <理由>" + Yes/No），并且 PermissionRequest 钩子照样触发**（输入同一份 `tool_input`，`permission_mode: auto`，无 `tool_use_id`）。11-01 那份"ask 后 PermissionRequest 不触发"的结论只在 `-p` 非交互下成立。

## 1 目标与不做

**目标**

- 硬底线到手机时，终端同时显示同一条请求和 Railse 的理由，谁先答谁算（恢复 v0.1 的体验，auto 模式也不漏）。
- 手机卡片一句话说清：Claude 想干什么（它自己写的说明）、命令里哪一段触发了硬底线、这一段什么性质。

**不做**：统计页、审计导出、"批三次建议建规则"、推送队列落库（挪到 v0.2.3）；Codex / Gemini 不动（它们没有 PreToolUse 等价物，v0.2.1 也没改它们）；手机语言不加自动切换（设置里手动切回中文即可）。

## 2 机制

### 2.1 硬底线：PreToolUse 回 `ask`，PermissionRequest 送手机

PreToolUse 对硬底线**不再等任何东西**，立刻输出 `ask`，理由见 2.3；Claude 随即弹终端确认框并触发 PermissionRequest，守护进程按 v0.1 的路径把这一条送 hub 等手机。两边谁先答：

| 先答的一方 | 结果 |
|---|---|
| 手机批准 / 拒绝 | PermissionRequest 钩子输出 allow / deny，终端确认框自动关闭（v0.1 既有行为） |
| 终端按 Yes / No | Claude 结束 PermissionRequest 钩子进程，守护进程照旧向 hub 取消这一条（现有 `cancelled` / `terminal` 路径），手机上的卡片消失 |
| hub 不可达 / 没接 hub / 等待过期 | PermissionRequest 不表态，终端确认框还在，由人在终端答。PreToolUse 一侧原来的"hub 不可达回 ask"分支因此不再需要 |

去重表（`internal/agent/recent.go`，v0.2.1 加的）**整个删掉**：PreToolUse 从此不再把任何请求送手机，PermissionRequest 来的每一条都该送（硬底线是约好的第二步，中间三级是 Claude 自己升级成提问），没有"同一件事问两次"的情形了。`claude_undecided: phone` 时中间三级走同一机制（ask + PermissionRequest），不再在 PreToolUse 里等。（计划写作时改定，原稿是加 `recentAsked` 状态。）

**模式的边界**（验收时逐一记录，写进 hub.md 的能力矩阵）：

- `default` / `acceptEdits` / `plan` / `auto`：如上。
- `-p` 非交互：`ask` 等于拒绝（11-01 已测），Claude 收到理由文本；与 v0.2.1 的"hub 不可达回 ask"一致，不退步。
- `bypassPermissions` / `dontAsk`：`ask` 的表现未测。验收第 6 条测出来：若弹框，照常；若等于拒绝，记为已知限制（用户自己选了跳过权限，Railse 不替它放行硬底线）。

审计里 PreToolUse 一条记 `action: ask`、`decided_by: terminal+phone`，PermissionRequest 一条记最终结果；`tool_use_id` 只在前者有，两条靠 `session + 载荷哈希` 对上（v0.2.1 的键不变）。

### 2.2 引擎给出触发点

`policy.Decision` 加 `Trigger`：

```go
type Trigger struct {
    Segment string `json:"segment,omitempty"` // 触发那一段的原文，去掉多余空白，超过 80 字截断；file_write / network 为路径或网址
    Why     string `json:"why"`               // 固定短语的键（中文原文），见下表
}
```

`classifyShell` 现在逐段取最高档；改成记住**第一个达到最终档位的段**和它在 `classifySegment` 里命中的检查。`Why` 取值固定（中文原文即键，英文在两侧目录里）：

| Why | 来源 |
|---|---|
| 删除文件，删了不可恢复 | rm、git clean、find -delete 等命令表里的破坏性行 |
| 结束进程 | kill / pkill / killall |
| 以管理员权限运行 | sudo / doas（含 ssh 远端命令里的） |
| 改敏感文件 / 读敏感文件 | touchesSensitiveWords、重定向到敏感路径 |
| 把脚本直接交给解释器执行 | 管道进 sh/bash/python/node 等从标准输入读脚本（readsScriptFromStdin）、`sh <(…)`、`eval "$(…)"` |
| 下载脚本并直接执行 | 同上且管道上游是 curl / wget |
| 删表或删库 | hasSQLDrop |
| 强制推送，会覆盖远程历史 / 删除远程分支 / 把代码推到远程仓库 | git push 各形态 |
| 发布或部署到外部 | vercel、fly deploy、gh release create、npm publish 等 |
| 向外发送数据 | curl -d/-X、/dev/tcp 写 |
| 要联网 | 联网档 |
| 改项目外的文件 | write_outside |
| 这条命令不在已知名单里 | unknown（Segment 为认不出的那段） |

现有 `Reason`（"无规则命中，按 destructive 默认询问"）保留给审计与老 hub，新字段并列。`proto.Response` / `proto.Request`（上报 hub 的那份）加 `trigger`；hub 存 `requests.trigger TEXT`（JSON，schema v5，老行为空）；审计行加 `trigger`。

### 2.3 理由文案（终端与手机共用）

终端确认框里 Claude 原样显示 `permissionDecisionReason`，所以它要自己能读：

- 中文：`Railse：命令里的「rm -rf after」会删除文件，删了不可恢复。已发到手机，也可以在这里直接批。`
- 英文：`Railse: "rm -rf after" deletes files and can't be undone. Sent to your phone; you can also answer here.`

组成 = 前缀 + Trigger（Segment + Why）+ 固定尾句；没有 Segment 时去掉引号那段。文案按守护进程语言（`railse lang`）出，Go `internal/i18n` 目录加这些键。手机卡片的"为什么要你决定"改用同一个 Trigger：`permissionWhy` 有 trigger 时第一句说触发点，第二句保留现在的"规则永远不会自动放行这类操作"；没有 trigger（老 hub、老守护进程）时维持现状。`web/src/i18n.sync.test.ts` 把 Why 短语的英文也纳入 Go 与网页一致性的检查。

### 2.4 Claude 自己的说明进手机

Claude 的 Bash 调用 `tool_input` 里带 `description`（它自己写的一句话，例如 "Print test string"）。适配器读出来放进 `Payload.Description`（新字段，`json:"description,omitempty"`），脱敏与 `Command` 同样处理（`redact.Payload` 对它跑一遍字符串脱敏），随载荷 JSON 上 hub，不需要 schema 改动。手机卡片：

- 有 description：第一行 `Claude Code 想：<description>`（原文，不翻译，Claude 通常用英文写；超过 100 字截断），第二行是现有解析器的"做什么"或触发点。
- 没有：维持现状。

Edit / Write / WebFetch 没有 description，不补。Codex / Gemini 的载荷没有这一项。

### 2.5 会话临时目录按项目内（本版不做）

原稿打算把 PreToolUse 输入里的 `scratchpad_dir` 交给引擎，其内路径按项目内定级。写计划时核查（2026-11-02，本机 0.2.1）：项目内的 `rm -rf src/old` 本来就是 destructive（引擎承诺递归删除永不自动放行，不按路径定级），`cat > x <<'EOF'` 是 unknown（heredoc 无法静态分析）。所以这条规则对今天的 3 条噪音（`rm -rf <scratch>/after`、heredoc 写临时文件）一条都帮不上；要降噪得给 `rm -rf` 开"全部目标都在临时目录内就放行"的例外，那是对硬底线承诺的改动，留 v0.2.3 单独决定。本版靠 2.2 与 2.3 让这类请求在终端一眼看清、一键批掉。

### 2.6 手机语言

不加东西。验收时把作者手机切回中文，推送订阅的语言随之更新（v0.2 既有机制）。

## 3 验收（真机，Claude Code 2.1.280，本机 hub）

1. `default` 与 `auto` 各一次 `git push --dry-run origin main`：终端弹确认框，理由含「git push」与"已发到手机"；手机同时收到卡片，第一行是 Claude 的说明，"为什么"一句写触发点；手机批准后终端框消失、命令执行。
2. 同上，改在终端按 Yes：命令执行，手机卡片消失，hub 里这条为 `cancelled`。
3. 停掉本机 hub 再跑一次：终端弹框、不卡，理由仍在；终端答后正常。
4. 把今天的四条真实命令（`rm -rf after …`、`pkill …`、`ssh … sudo …`、`python - <<'EOF' … DROP TABLE`）用 `railse policy eval` 跑一遍，触发点分别是删除文件、结束进程、管理员权限、把脚本直接交给解释器执行（或删表）。
5. （删，见 §2.5）
6. `--permission-mode bypassPermissions` 下 `git push --dry-run`：记录表现，写进 hub.md。
7. `claude -p` 下同一条：被拒，Claude 收到的文本含触发点。
8. 老 hub（0.2.1）配新守护进程：卡片照常，只是没有触发点与说明；新 hub 配老守护进程同理。

## 4 文档

README 两份能力矩阵"硬底线"一行改为"终端与手机同时显示，谁先答谁算"；`docs/hub.md` 加模式边界表与 `trigger` 字段；`docs/setup.md` 升级说明（hub 升到 schema v5，守护进程与 hub 都要升）；`2026-11-01-pretooluse-ask-findings.md` 加 2026-11-02 交互式补记；CHANGELOG。
