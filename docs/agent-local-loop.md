# 本机闭环：守护进程与钩子

安装与接入（一行安装、`railse setup all`、开机自启、`railse doctor`）见 [setup.md](setup.md)。本文讲守护进程与钩子本身。

没有 hub 时，Railse 在本机这样工作：CLI 的钩子把审批请求经 Unix socket 交给守护进程，守护进程用规则引擎自动放行或拒绝，判不了的交回终端，每一步都写本地审计。Claude Code 的选择题（`AskUserQuestion`）是另一条路，见下面“手机上答选择题”。

## 起守护进程

```sh
go build -o ~/bin/railse ./cmd/railse
railse agent run --foreground    # 前台运行，--foreground 让日志同时打到 stderr；Ctrl-C 退出
railse agent status              # 另开终端查看：paused、规则条数、待决请求数、hub 连接
```

`railse agent status` 的 `hub:` 行：没接入 hub（没有 `machine.json`，或接入后还没重启守护进程）时是 `hub: none`；升级前起的旧守护进程不报这一项，显示 `hub: 未知（守护进程版本较旧？重启它）`；接入后是 `hub: connecting <地址>`（还没连上或等着重连）、`hub: connected <地址>`，或 `hub: disabled <地址>`（连续 3 次被 hub 以 401 拒绝，已停止连接，要 `railse agent enroll --force` 后重启守护进程，见 `docs/hub.md`）。

配置目录取 `$RAILSE_HOME`，没设就是 `~/.railse`（权限 0700），里面有 `agent.sock`、`rules.yaml`、`audit.jsonl`、`state.json`、`agent.log`，socket 与审计为 0600。`agent` 子命令也接受 `--dir 目录`。钩子不带 `--socket` 时连 `$RAILSE_HOME` 或 `~/.railse` 里的 `agent.sock`，看的是 CLI 起钩子时的环境，那里不一定有 `RAILSE_HOME`。所以守护进程的数据目录（`--dir`，没给时取 `$RAILSE_HOME`）不是 `~/.railse` 时，`railse setup hooks` 在钩子命令里写上 `--socket <目录>/agent.sock`（是 `~/.railse` 时不写，命令不变）；只设了 `RAILSE_HOME` 的也一样。以前手工换过目录、钩子是按 `~/.railse` 装的，重跑一次 `setup hooks --dir <目录>`；`railse doctor --dir <目录>` 会在 `hooks.*` 报出对不上的 socket（它把不带 `--socket` 的钩子当作连 `~/.railse/agent.sock`）。开机自启与退出后自动拉起用 `railse setup autostart`（macOS launchd、Linux systemd 用户单元，见 [setup.md](setup.md)）；`railse doctor` 只读地检查目录与文件权限、守护进程在不在跑、钩子装没装好。

## 装钩子

```sh
railse setup hooks [--project <目录>] [--claude] [--codex] [--gemini] [--dir 目录] [--bin 路径] [--dry-run]
```

不带 CLI 开关时三个都装，默认都是用户级（对所有项目生效）：Claude 写 `~/.claude/settings.json`，Gemini 写 `~/.gemini/settings.json`，Codex 写 `~/.codex/hooks.json`（`--codex-file` 可改）。`~/.claude/settings.json`、`~/.gemini/settings.json` 里多是你自己的设置：第一次改它们之前各备份一次到同目录的 `<文件>.railse-bak-<时间>`（0600，是链接时备份链接指向的内容；原来不存在、是空的、内容没变或已有备份时不备份），打印“已备份 … 到 …”。给了 `--project <目录>` 时 Claude 改写项目级的 `<目录>/.claude/settings.local.json`，Gemini 改写 `<目录>/.gemini/settings.json`（不备份；项目里的文件就是用户级那份时，如 `--project ~` 的 Gemini，照样按用户级备份），Codex 照旧是用户级。用户级与项目级都装了：Gemini 的钩子会触发两次；Claude Code 对写在多个配置文件里的相同处理器只跑一次，两处的命令、超时等完全相同时不重复，不同（如程序路径、`--socket` 不同）时也会触发两次；装用户级时当前目录的项目级文件里有 Railse 的条目，会在 stderr 逐个提醒（见 [setup.md](setup.md)）。`--bin` 是写进钩子命令的 railse 路径，默认当前程序；`--dir` 是守护进程的数据目录（默认 `$RAILSE_HOME` 或 `~/.railse`），不是 `~/.railse` 时钩子命令变成 `<railse> hook <cli> --socket <目录>/agent.sock`；`--dry-run` 只打印不写。重复执行只替换 Railse 自己的条目，别的钩子原样保留。

Claude Code 装的钩子有五个事件：

| 事件 | matcher | 用途 | 超时 |
| --- | --- | --- | --- |
| `PermissionRequest` | 全部 | 审批的兜底：MCP、Task 等 `PreToolUse` 不管的工具，以及 Claude 自己决定要问人时 | 86400 秒 |
| `PreToolUse` | `AskUserQuestion\|Bash\|Edit\|Write\|MultiEdit\|NotebookEdit\|WebFetch` | 审批（0.2.1 起，任何权限模式都触发）；手机上答选择题（0.1.5 起） | 86400 秒 |
| `Notification` | 全部 | 映射为 `question` 事件 | 默认 |
| `Stop` | 全部 | 映射为 `done` 事件 | 默认 |
| `SessionEnd` | 全部 | 会话结束 | 默认 |

0.1.5 之前装过钩子的，重跑一次 `railse setup hooks --claude` 补上 `PreToolUse`；不补不影响别的功能，`railse doctor` 的 `hooks.claude` 会报 warn“缺 PreToolUse：Claude Code 的选择题进不了手机”。0.2.0 装的 `PreToolUse` 只匹配 `AskUserQuestion`，同样重跑一次换成上表的匹配器；不换的话 auto 模式下审批全绕过 Railse，doctor 报 warn“PreToolUse 只匹配 AskUserQuestion：auto 模式下审批进不了 Railse”。

Codex 的完成与提问事件走 `~/.codex/config.toml` 顶层的 `notify`（不是 hooks.json），Codex 只认一条，所以另用 `railse setup codex-notify [--dir 目录] [--bin 路径] [--dry-run]`（`setup all` 装完钩子会自动做这一步，单独跑 `setup hooks` 装 Codex 时会提示）：它在数据目录生成 `codex-notify.sh`，依次调用原来的 `notify`（有的话，原样保留）与 `railse hook codex-notify`（数据目录不是 `~/.railse` 时带 `--socket`），再把 `notify` 换成 `["<数据目录>/codex-notify.sh"]`，改之前备份到 `config.toml.railse-bak-<时间>`。重跑不会重复串接，详见 [setup.md](setup.md) 的 Codex 一条。

装项目级时 Claude 的钩子放在 `settings.local.json`，因为钩子命令里是本机的 railse 路径，不该随项目入库；按 Claude Code 的约定这个文件不入库，若 `git status` 里看到它，把它加进 `.gitignore`。旧版写在 `.claude/settings.json` 的 Railse 钩子会在装 Claude 钩子时迁走：只删 Railse 自己的条目（`hooks` 删空了连键一起去掉），别人的钩子与其他设置保留，打印“已从 … 迁走 Railse 钩子”；`--dry-run` 两份都打印。`settings.json` 里没有 Railse 条目时不碰它；它（解开链接后）就是用户级的 `~/.claude/settings.json` 时（`--project` 是家目录，或项目的 `settings.json` 链到它）也不迁，只警告：那里的钩子对所有项目生效，要迁请手动删；它不是合法 JSON 时只警告、不迁移，照样装好 `settings.local.json`，这时请手动删掉里面旧的 Railse 钩子，否则同一个事件会触发两次。

## 看审计

`~/.railse/audit.jsonl` 只追加，一行一条 JSON，例如 `tail -f ~/.railse/audit.jsonl | jq .`。

| 字段 | 含义 |
| --- | --- |
| `ts`、`request_id` | 时间；审批请求的 ID（事件没有） |
| `kind` | `permission`、`ask`、`question`、`done`、`session_end`、`control` |
| `session` | `tool`、`cli_session_id`、`turn_id`、`cwd` |
| `tool_name`、`payload`、`risk` | 工具名；脱敏后的统一载荷；风险档（`read` 到 `destructive`）。`ask` 不适用：它的 `tool_name` 固定是 `AskUserQuestion`，没有 `payload` 与风险档 |
| `ask`、`answers` | 仅 `ask`：脱敏后的题目；`answers` 仅手机上答了的，脱敏后的题面 → 脱敏后的答案 |
| `action` | `allow`、`deny`、`passthrough`、`answer`（`ask` 在手机上答了） |
| `state` | 见下表 |
| `rule_id`、`reason`、`decided_by` | 命中的规则与理由；谁定的：`rule:<id>`、`no_hub`、`paused`、`timeout`、`terminal`、`shutdown`、`error` |
| `message` | 事件文本，如最后一条回复 |

| `state` | 含义 |
| --- | --- |
| `auto_allowed` / `auto_denied` | 规则自动放行 / 拒绝 |
| `passthrough` | 交回终端：没有规则命中且没有 hub、已暂停、守护进程正在停止或出错 |
| `cancelled` | 决定前钩子进程已退出（多半是终端里先处理了） |
| `expired` | 等待过期，交回终端 |
| `approved` / `denied` | 由 hub 批准 / 拒绝，接入 hub 后才会出现 |
| `answered` | `ask` 在手机上答了，接入 hub 后才会出现；审计里的题目和答案已脱敏（答案本身原样交给 Claude Code） |
| `recorded` | 事件或管理命令已记录 |
| `suppressed` | 同一会话有待决审批时来的 `question`，不重复提醒 |

审计里的命令已脱敏，且不记录钩子进程的环境变量。

## 改规则

首次启动会写出内置规则包（只读和项目内改动放行），格式见设计文档 4.3。编辑 `~/.railse/rules.yaml` 后执行 `railse agent reload`，或给守护进程发 `SIGHUP`。文件写坏时保留旧规则：`reload` 直接报错，`SIGHUP` 把错误写进 `agent.log`；但启动时文件是坏的，守护进程会报错退出。例如拒绝一切 `rm`，追加到 `rules:` 下，缩进与文件里已有条目对齐：

```yaml
    - id: user-deny-rm
      source: user
      scope: global
      match: {payload: shell, command_prefix: rm}
      action: deny
```

## 项目根

内置规则放行的“项目内改动”（`write_in_project`）以项目根为界，项目根之外的写入（`write_outside`）要审批。守护进程从请求的工作目录向上找 `.git`（目录，或 worktree 的 `.git` 文件），找到的那一层就是项目根。系统目录之外的 git 仓库都算（`/tmp`、`~/.config/nvim` 下的仓库也是）。

系统目录是 `/opt`、`/usr`、`/etc`、`/var`、`/System`、`/Library`、`/private` 和它们下面的目录；第一段以点开头的路径也算（macOS 的 `/.nofollow/opt/homebrew` 就是 `/opt/homebrew`）。工作目录在系统目录里时没有项目根，是 git 仓库也一样：`/opt/homebrew` 归用户可写、又在 PATH 上，当项目根会让 agent 不经审批改写装好的程序。判断用解析过链接的路径，与列出的写法和它解析后的写法都比（macOS 上 `/etc`、`/var`、`/tmp` 都在 `/private` 下），按整段比较（`/optional` 不算 `/opt`），macOS 上不分大小写。macOS 数据卷上的写法 `/System/Volumes/Data/…` 在 `/System` 下，也算系统目录：从这种写法进入的家目录和项目同样没有项目根。Linux 上 `/opt`、`/var/www` 下的仓库同样没有项目根，所有写入都要审批。这时在 `agent.log` 里提示“目录 "X" 在系统目录里，不按项目根处理；要自动放行请换到系统目录之外的项目目录”。

系统目录里有三处照常判断：

- **`/private/tmp`**：macOS 上的 `/tmp`。
- **守护进程的临时目录**：`$TMPDIR`（macOS 上是 `/var/folders/…/T`），要它归你、权限 700，也不是某个系统目录本身或它的上级。
- **家目录**：解析后落在系统目录里时（如 Fedora Silverblue 的 `/home` 链到 `/var/home`），要它归你、不是某个系统目录本身或它的上级、在系统目录下至少两层（`/var/home/x` 可以，macOS root 的 `/var/root` 不行）。

不在 git 仓库里时，只有家目录下的普通目录按工作目录本身当项目根（不取它的上级）：要在家目录之下（不是家目录本身），从家目录往下的每一段都不以点开头（`~/.config`、`~/.local`、`~/dev/.hidden/x` 都不算），也不在 `~/Library` 之下（不分大小写）；符号链接按它指向的目标判断。这时在 `agent.log` 里提示“目录 "X" 不是 git 仓库，按当前目录当项目根；建议 git init”。不满足这三条的（`/tmp` 下、`~/Library/LaunchAgents`、`~/.config/x` 等）没有项目根，所有写入都要审批，`agent.log` 里提示“目录 "X" 不在家目录下的普通目录里，不按项目根处理；要自动放行请 git init 或换到项目目录”。三种提示的路径都带引号、转义，每个目录只提示一次。

家目录、它的上级与根目录永远不当项目根：工作目录就是它们时没有项目根，所有写入都算项目外；用 git 管 dotfiles、家目录里有 `.git` 的，家目录下的子目录照上面三条判断。规则的 `scope: {project: …}` 与 hub 上“记住”的项目范围也按这个项目根（引擎与 hub 分不出 git 仓库根与按工作目录定的根），项目根是家目录、它的上级或根目录时一律按没有项目根处理。`railse doctor --project <目录>` 的 `project.root` 一项显示这个目录会被当成哪个项目根（不给 `--project` 时看当前目录，它不是 git 仓库或在系统目录里时只告知、不给修复提示）。

## 暂停与恢复

`railse agent pause` 后，除命中用户 deny 规则的请求外一律交回终端（Claude Code 的选择题不受暂停影响）；`railse agent resume` 恢复。暂停状态存在 `state.json`，重启守护进程后保持。

## 三家 CLI 的差异

| | Claude Code | Codex | Gemini CLI |
| --- | --- | --- | --- |
| 自动放行 | 能 | 能 | 不能，放行一律渲染为不作决定 |
| 自动拒绝 | 能 | 能 | 能 |
| 事件 | question、done、session_end | done、session_end | question、done、session_end |
| 手机上答问题 | 能（`AskUserQuestion`，需要 `PreToolUse` 钩子和 hub） | 不能 | 不能 |
| auto / yolo 模式下 | 生效（`PreToolUse`；对外与不可逆任何模式都到手机，中间三级先交给 Claude 自己） | 看 Codex 自己的 `approval_policy` | yolo 跳过钩子 |
| 钩子配置 | 用户级 `~/.claude/settings.json`（`--project` 时项目级 `.claude/settings.local.json`） | 用户级 `~/.codex/hooks.json` | 用户级 `~/.gemini/settings.json`（`--project` 时项目级 `.gemini/settings.json`） |
| 注意 | 全功能；agent 停下来等你说下一句话（`question`）仍只提醒，要回终端 | 只在交互会话里审批（`codex exec` 不触发）；首次使用在 Codex 里用 `/hooks` 审核；notify 用 `railse setup codex-notify` 写进 `~/.codex/config.toml`（串接脚本，保留已有的 notify；`setup all` 会自动做） | 需要 0.65.0 以上；不要开 yolo 模式 |

## Claude Code 的审批走 PreToolUse（0.2.1 起）

`PermissionRequest` 只在 Claude Code 决定要问人时触发；auto 模式（2.1.283 起的默认）由它的分类器直接放行，连 `git push`、`rm -rf` 都不问，钩子根本不触发。所以 0.2.1 起 Bash、Edit、Write、MultiEdit、NotebookEdit、WebFetch 在 `PreToolUse` 审批，它在任何权限模式下都触发，输入带 `permission_mode` 与 `tool_use_id`。答复是 `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow|deny","permissionDecisionReason":"Railse：…"}}`，不表态时 stdout 为空。

| 引擎结果 | 任何权限模式 |
| --- | --- |
| 规则放行（内置只读、项目内改动；你记住的放行） | allow |
| 规则拒绝 | deny |
| 定不了，对外或不可逆 | 送手机；没接 hub、hub 不可达、等待过期时回 `ask` |
| 定不了，联网、项目外改动、无法判断 | 不表态（审计 `passthrough`、`decided_by: classifier`），先让 Claude 自己的 allow 列表、auto 的分类器处理；Claude 要问人时 `PermissionRequest` 再送手机。`~/.railse/settings.yaml` 写 `claude_undecided: phone` 则在这里直接送手机 |

- **本机设置 `~/.railse/settings.yaml`。** 目前只有 `claude_undecided: classifier | phone`（默认 `classifier`）。不放 `rules.yaml`：那份由 hub 下发覆盖，解析时也拒绝未知字段。守护进程启动、`railse agent reload` 与 SIGHUP 时读；读不了、有未知字段或值不对时按默认值跑，reload 回报错。
- **`ask` 只给送不到手机的硬底线。** 实测 `permissionDecision: "ask"` 在 auto 模式下也强制让人确认，不回分类器（[findings](superpowers/specs/2026-11-01-pretooluse-ask-findings.md)）。对外与不可逆两档在没接 hub、hub 不可达、等待过期时输出 `ask`（理由“Railse：连不上 hub，这类操作要你亲自确认”），任何模式都如此，`claude -p` 下等于拒绝；其余交回终端一律输出空，免得比 auto 模式更吵。
- **同一调用不问两次。** 守护进程记 10 分钟内 `PreToolUse` 处理过的调用（键是 `tool_use_id`，以及会话 + 工具 + 载荷的哈希；`PermissionRequest` 不带 `tool_use_id`，靠后者对上）。`PreToolUse` 送过手机而交回了终端（hub 不可达、过期等）的，随后同一调用的 `PermissionRequest` 直接不表态（审计 `decided_by: dedup`），不再等第二次；中间三级不表态的，`PermissionRequest` 是 Claude 自己升级成要问人，照常送手机。
- **放行是终局。** `PreToolUse` 回 allow 时 Claude Code 跳过它自己的权限检查（含 auto 的分类器），所以内置放行只覆盖只读与项目内改动两档；`settings.json` 里你写的 deny 规则仍先于钩子生效。
- **失败即不表态。** 守护进程不在、hub 30 秒不可达、解析失败、超时，钩子都输出空（硬底线例外，见上一条），Claude 照自己的流程走；hub 不可达时 stderr 多一行 `railse hook: 连不上 hub，交回 Claude 自己的确认`。
- 审计的 Claude 审批条目多三个字段：`source`（`pretooluse` / `permissionrequest`）、`permission_mode`、`tool_use_id`。

## 手机上答选择题

Claude Code 用 `AskUserQuestion` 问你选哪个方案时，`PreToolUse` 钩子把题目交给守护进程，守护进程作为 `ask` 请求送 hub，手机上选答案或自己填一句，答案回到钩子，钩子按 Claude Code 的格式把原来的 `questions` 连同 `answers` 交回（`permissionDecision: allow` 加 `updatedInput`），agent 立刻继续，终端不再弹选项。用法和网页见 [web.md](web.md)，接口见 [hub.md](hub.md)。

- **不过引擎，不看暂停。** 题目不是命令，没有风险档，不匹配规则；`railse agent pause` 与 hub 的暂停开关都不影响它，也不能“记住”。
- **题目在本机先脱敏。** 题面、标题、选项文字过一遍 `redact.Text` 再上报、写审计；答案是你自己打的字，原样交给 Claude Code，审计里再脱敏。
- **没有 hub 就不进手机。** 没接 hub 时直接交回终端，审计 `passthrough`、`decided_by: no_hub`。
- **任何一环不通，终端照常问。** hub 连不上超过 30 秒或等满期限（审计 `expired`，`hub_unreachable` 或 `timeout`）、hub 太旧不认识提问（0.1.4 及更早，welcome 没声明；不送、立即交回，审计 `expired`、`hub_unreachable`）、手机上选了“让它在终端问我”（`passthrough`，`decided_by: user`）、脱敏后有题面相同的题或超出上限（脱敏会让短值变长），或决定来源给的答案不合规（`passthrough`，`decided_by: error`）、钩子在决定前退出（`cancelled`，`terminal`），钩子都不输出，Claude Code 在终端里问，agent 不会因此卡住。题目本身不合规（钩子解析就不过，或守护进程校验不过）时不写审计：前者钩子往 stderr 写一句、根本不发给守护进程，后者守护进程只记日志并回 `passthrough`，终端照常问。
- **`PermissionRequest` 对 `AskUserQuestion` 一律交回终端**：Claude Code 的文档明示这条路上回 allow 无效，答案只能走 `PreToolUse`。
- 手机上答了的，本机审计是 `kind: ask`、`action: answer`、`state: answered`、`decided_by: user`。同一会话有选择题在等时，同会话的 `question` 提醒会被压成 `suppressed`。Codex、Gemini 没有这个口子，问题照旧只提醒。

## 交回终端的两种情况

- **没有 hub 时 ask 一律交回终端。** 引擎答 ask（没有规则命中，如 `git push`、`curl`、`npm install`）时，守护进程立即回“不作决定”，CLI 弹出自己的原生审批框，由你在终端里定；审计记 `passthrough`，`decided_by` 为 `no_hub`。
- **钩子任何异常都不作决定。** 守护进程没起、socket 连不上、输入损坏、不认识的 CLI、收到 SIGTERM/SIGINT/SIGHUP、内部出错，钩子都是 stdout 为空、退出 0，原因写到 stderr。最坏情况就是回到没装 Railse 时的样子。

## 冒烟

`scripts/smoke-claude.sh` 在临时目录里起守护进程、装 Claude 钩子，再让 `claude -p` 执行一条 `touch`，最后打印审计。需要本机 Claude Code 已登录，会消耗模型额度，不进 CI。脚本用 `--setting-sources project,local`：钩子在 `settings.local.json`（`local` 来源），只写 `project` 读不到；不加 `user`，免得用户级设置替人审批。接入 hub 的那条链路由 `scripts/smoke-claude-hub.sh` 冒烟：临时的 hub 与守护进程，`claude -p` 跑一条 `git push`（引擎定不了，送 hub），脚本用 REST 当手机批准，断言 hub 里该请求是 `approved`、`decided_by: user` 且已送达，本机审计有 `approved`；发布前手动跑。

## 接入 hub 后

有 `~/.railse/machine.json` 时，引擎定不了的请求不再直接交回终端，而是送到 hub 等手机决定；规则以 hub 为准，本机 `rules.yaml` 只是缓存。总开关的生效值是本机与 hub 两者取或：`railse agent pause`/`resume` 只改本机这一侧，不会被 hub 覆盖，也不同步到 hub，`railse agent status` 显示暂停来自 `local`、`hub` 还是 `local+hub`。连不上 hub 时最多等 30 秒就交回终端（审计 `expired`、`decided_by: hub_unreachable`）；Gemini 的请求永远不进 hub。接入后本机审计的 `decided_by` 还会出现 `user`（手机上的决定）、`hub_unreachable`、`gemini`；手机上的备注记在 `note` 字段，钩子与模型只看到“已在手机上批准/拒绝”这类固定文案。接入步骤、行为变化与 API 见 `docs/hub.md`。
