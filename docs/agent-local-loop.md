# 本机闭环：守护进程与钩子

安装与接入（一行安装、`railse setup all`、开机自启、`railse doctor`）见 [setup.md](setup.md)。本文讲守护进程与钩子本身。

没有 hub 时，Railse 在本机这样工作：CLI 的钩子把审批请求经 Unix socket 交给守护进程，守护进程用规则引擎自动放行或拒绝，判不了的交回终端，每一步都写本地审计。Claude Code 的选择题（`AskUserQuestion`）是另一条路，见下面“手机上答选择题”。

## 起守护进程

```sh
go build -o ~/bin/railse ./cmd/railse
railse agent run --foreground    # 前台运行，--foreground 让日志同时打到 stderr；Ctrl-C 退出
railse agent status              # 另开终端查看：paused、规则条数、待决请求数、生效中的限时放行条数（trusts: N）、hub 连接
```

`railse agent status` 的 `hub:` 行：没接入 hub（没有 `machine.json`，或接入后还没重启守护进程）时是 `hub: none`；升级前起的旧守护进程不报这一项，显示 `hub: 未知（守护进程版本较旧？重启它）`；接入后是 `hub: connecting <地址>`（还没连上或等着重连）、`hub: connected <地址>`，或 `hub: disabled <地址>`（连续 3 次被 hub 以 401 拒绝，已停止连接，要 `railse agent enroll --force` 后重启守护进程，见 `docs/hub.md`）。

配置目录取 `$RAILSE_HOME`，没设就是 `~/.railse`（权限 0700），里面有 `agent.sock`、`rules.yaml`、`audit.jsonl`、`state.json`、`agent.log`，以及建过限时放行后的 `trusts.json`，socket、审计与 `trusts.json` 为 0600。`agent` 子命令也接受 `--dir 目录`。钩子不带 `--socket` 时连 `$RAILSE_HOME` 或 `~/.railse` 里的 `agent.sock`，看的是 CLI 起钩子时的环境，那里不一定有 `RAILSE_HOME`。所以守护进程的数据目录（`--dir`，没给时取 `$RAILSE_HOME`）不是 `~/.railse` 时，`railse setup hooks` 在钩子命令里写上 `--socket <目录>/agent.sock`（是 `~/.railse` 时不写，命令不变）；只设了 `RAILSE_HOME` 的也一样。以前手工换过目录、钩子是按 `~/.railse` 装的，重跑一次 `setup hooks --dir <目录>`；`railse doctor --dir <目录>` 会在 `hooks.*` 报出对不上的 socket（它把不带 `--socket` 的钩子当作连 `~/.railse/agent.sock`）。开机自启与退出后自动拉起用 `railse setup autostart`（macOS launchd、Linux systemd 用户单元，见 [setup.md](setup.md)）；`railse doctor` 只读地检查目录与文件权限、守护进程在不在跑、钩子装没装好。

## 装钩子

```sh
railse setup hooks [--project <目录>] [--claude] [--codex] [--gemini] [--dir 目录] [--bin 路径] [--dry-run]
```

不带 CLI 开关时三个都装，默认都是用户级（对所有项目生效）：Claude 写 `~/.claude/settings.json`，Gemini 写 `~/.gemini/settings.json`，Codex 写 `~/.codex/hooks.json`（`--codex-file` 可改）。`~/.claude/settings.json`、`~/.gemini/settings.json` 里多是你自己的设置：第一次改它们之前各备份一次到同目录的 `<文件>.railse-bak-<时间>`（0600，是链接时备份链接指向的内容；原来不存在、是空的、内容没变或已有备份时不备份），打印“已备份 … 到 …”。给了 `--project <目录>` 时 Claude 改写项目级的 `<目录>/.claude/settings.local.json`，Gemini 改写 `<目录>/.gemini/settings.json`（不备份；项目里的文件就是用户级那份时，如 `--project ~` 的 Gemini，照样按用户级备份），Codex 照旧是用户级。用户级与项目级都装了：Gemini 的钩子会触发两次；Claude Code 对写在多个配置文件里的相同处理器只跑一次，两处的命令、超时等完全相同时不重复，不同（如程序路径、`--socket` 不同）时也会触发两次；装用户级时当前目录的项目级文件里有 Railse 的条目，会在 stderr 逐个提醒（见 [setup.md](setup.md)）。`--bin` 是写进钩子命令的 railse 路径，默认当前程序；`--dir` 是守护进程的数据目录（默认 `$RAILSE_HOME` 或 `~/.railse`），不是 `~/.railse` 时钩子命令变成 `<railse> hook <cli> --socket <目录>/agent.sock`；`--dry-run` 只打印不写。重复执行只替换 Railse 自己的条目，别的钩子原样保留。

Claude Code 装的钩子有五个事件：

| 事件 | matcher | 用途 | 超时 |
| --- | --- | --- | --- |
| `PermissionRequest` | 全部 | 审批的兜底：Task 等 `PreToolUse` 不管的工具，以及 Claude 自己决定要问人时 | 86400 秒 |
| `PreToolUse` | `AskUserQuestion\|Bash\|Edit\|Write\|MultiEdit\|NotebookEdit\|WebFetch\|WebSearch\|mcp__.*` | 审批（0.2.1 起，任何权限模式都触发；0.3 起含 WebSearch 与全部 MCP 工具）；手机上答选择题（0.1.5 起） | 86400 秒 |
| `Notification` | 全部 | 映射为 `question` 事件 | 默认 |
| `PostToolUse` | 同 `PreToolUse` | 工具已跑完，映射为 `tool_done` 事件：取消同一调用还挂着的审批（0.2.2 起；不写审计，只记它引起的取消） | 默认 |
| `Stop` | 全部 | 映射为 `done` 事件；同时取消这个会话里还挂着的审批与答题（0.2.2 起） | 默认 |
| `SessionEnd` | 全部 | 会话结束 | 默认 |

0.1.5 之前装过钩子的，重跑一次 `railse setup hooks --claude` 补上 `PreToolUse`；不补不影响别的功能，`railse doctor` 的 `hooks.claude` 会报 warn“缺 PreToolUse：Claude Code 的选择题进不了手机”。0.2.0 装的 `PreToolUse` 只匹配 `AskUserQuestion`，同样重跑一次换成上表的匹配器；不换的话 auto 模式下审批全绕过 Railse，doctor 报 warn“PreToolUse 只匹配 AskUserQuestion：auto 模式下审批进不了 Railse”。0.2.3 及以前装的匹配器不含 `WebSearch` 与 MCP 工具：Claude 不需要原生确认时（已允许的 MCP 工具、auto、bypassPermissions）它们进不了 Railse，角色的禁用（如 `{payload: mcp}`）与上限管不到；重跑一次 `railse setup hooks --claude`（幂等，原地换掉旧匹配器），doctor 报 warn“PreToolUse 的匹配器是旧版”。这两类只为角色而来：会话没有生效角色（没有 `roles.yaml`，或会话未分配且没有从上层会话继承角色）时一律忽略，与 0.2.3 完全相同——钩子不输出、不写审计、不送手机，`claude_undecided: phone` 与你手写的 deny 规则都不对它们起作用（`PermissionRequest` 来的照旧受规则管）。纯读取的 `Read`、`Glob`、`Grep` 不进 `PreToolUse`（每次读都过一遍钩子太慢，读也越不过任何上限）。

Codex 的完成与提问事件走 `~/.codex/config.toml` 顶层的 `notify`（不是 hooks.json），Codex 只认一条，所以另用 `railse setup codex-notify [--dir 目录] [--bin 路径] [--dry-run]`（`setup all` 装完钩子会自动做这一步，单独跑 `setup hooks` 装 Codex 时会提示）：它在数据目录生成 `codex-notify.sh`，依次调用原来的 `notify`（有的话，原样保留）与 `railse hook codex-notify`（数据目录不是 `~/.railse` 时带 `--socket`），再把 `notify` 换成 `["<数据目录>/codex-notify.sh"]`，改之前备份到 `config.toml.railse-bak-<时间>`。重跑不会重复串接，详见 [setup.md](setup.md) 的 Codex 一条。

装项目级时 Claude 的钩子放在 `settings.local.json`，因为钩子命令里是本机的 railse 路径，不该随项目入库；按 Claude Code 的约定这个文件不入库，若 `git status` 里看到它，把它加进 `.gitignore`。旧版写在 `.claude/settings.json` 的 Railse 钩子会在装 Claude 钩子时迁走：只删 Railse 自己的条目（`hooks` 删空了连键一起去掉），别人的钩子与其他设置保留，打印“已从 … 迁走 Railse 钩子”；`--dry-run` 两份都打印。`settings.json` 里没有 Railse 条目时不碰它；它（解开链接后）就是用户级的 `~/.claude/settings.json` 时（`--project` 是家目录，或项目的 `settings.json` 链到它）也不迁，只警告：那里的钩子对所有项目生效，要迁请手动删；它不是合法 JSON 时只警告、不迁移，照样装好 `settings.local.json`，这时请手动删掉里面旧的 Railse 钩子，否则同一个事件会触发两次。

## 看审计

`~/.railse/audit.jsonl` 只追加，一行一条 JSON，例如 `tail -f ~/.railse/audit.jsonl | jq .`。

| 字段 | 含义 |
| --- | --- |
| `ts`、`request_id` | 时间；审批请求的 ID（事件没有） |
| `kind` | `permission`、`ask`、`question`、`done`、`session_end`、`control` |
| `session` | `tool`、`cli_session_id`、`turn_id`、`cwd`；有角色时还有 `role`（生效的角色名，见“角色”） |
| `tool_name`、`payload`、`risk` | 工具名；脱敏后的统一载荷；风险档（`read` 到 `destructive`）。`ask` 不适用：它的 `tool_name` 固定是 `AskUserQuestion`，没有 `payload` 与风险档 |
| `ask`、`answers` | 仅 `ask`：脱敏后的题目；`answers` 仅手机上答了的，脱敏后的题面 → 脱敏后的答案 |
| `action` | `allow`、`deny`、`passthrough`、`answer`（`ask` 在手机上答了） |
| `state` | 见下表 |
| `rule_id`、`reason`、`decided_by` | 命中的规则与理由；谁定的：`rule:<id>`、`trust:<放行 id>`（限时放行命中）、`role:<角色名>`（角色禁用）、`no_hub`、`paused`、`timeout`、`terminal`、`shutdown`、`error`；管理命令里限时放行的建、撤、到期记建的人 `cli`、`env`、`phone`，被手机撤掉的记 `hub` |
| `message` | 事件文本，如最后一条回复 |
| `role`、`beyond_role` | 仅审批（0.3 起）：判定时的角色名，与是否超出角色的上限。没有 `roles.yaml` 时不出现 |

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

## 限时放行（0.2.3 起）

把一个 CLI 会话或这台机器上的一个项目在一段时间内整体放行。**放行期间 Railse 对该范围完全不把关，等同于 Claude Code 的 bypassPermissions 加上审计，到期自动失效，最长 24 小时。** 守护进程是执行者：放行表在它内存里，同时落 `trusts.json`，重启不丢；接了 hub 时与 hub 双向同步（见下），hub 断线期间照常生效到期。

### 判定

`handlePermission` 跑完引擎、在送手机之前看放行表：

| 引擎结论 | 命中放行 | 结果 |
| --- | --- | --- |
| 规则 allow / deny（含你手写的 deny） | 任意 | 照旧：放行不压拒绝规则，也不需要出手 |
| 定不了，总开关暂停 | 任意 | 照旧交回终端（暂停压过放行） |
| 定不了 | 有，且未过期 | `allow`，审计 `state: auto_allowed`、`decided_by: trust:<id>`、`reason` 形如“限时放行（会话 claude，剩 1 小时 5 分）”；钩子看到的理由是固定的“限时放行中” |
| 定不了 | 无 | 照旧（硬底线回 ask，中间三级交 Claude，`PermissionRequest` 送手机） |
| 定不了，读写 Railse 自己的配置（0.3 起） | 任意 | 不走放行，照常进人工决定路径（`PreToolUse` 回 ask，其余送手机，Gemini 一律拒）；本来会命中放行时理由写明“限时放行不覆盖 Railse 自己的配置” |

- **怎么算命中。** 会话范围要请求的 CLI（`tool`）与会话 id（`cli_session_id`）都相同，请求没有会话 id 不命中；项目范围要请求算出的项目根（见“项目根”）与放行的项目根字符串相等，没有项目根不命中。同时命中几条记最晚到期的那条。`PreToolUse`、`PermissionRequest` 与 Codex 的请求都看；Gemini 的钩子不能放行，命中了也只是渲染成不作决定，照旧交回终端。
- **不覆盖 Railse 自己的配置（0.3 起）。** 请求读写 `~/.railse`、任何 `.railse` 目录或实际的守护进程目录（`--dir`、`RAILSE_HOME`）时，会话放行、项目放行与 `RAILSE_TRUST` 都不适用：放行期间 agent 改规则、角色、放行表，等于改掉管它自己的东西。文件写入看目标路径；shell 命令按段走、跟踪 `cd`、`pushd`、`popd` 改变的工作目录（与定级同一套），看每一段（含 `sh -c`、命令替换与 `eval` 里的）的参数与重定向，`cat`、`cp`、`mv`、`ln` 的目标都在内；字面上提到 `.railse` 的也算；命令名与 URL 不当路径。工作目录算不出之后（`cd "$X"`、`cd -`、`popd`），相对路径一律按触及处理，不走放行。本来会命中放行而因此没放的，Claude 的 `PreToolUse` 一律回 ask（交给 Claude 自己的流程在 bypassPermissions 下等于放行）。有没有 `roles.yaml`、会话有没有角色都一样；别的敏感路径（`.env`、ssh 密钥等）在放行期间的行为与 0.2.3 相同。
- **后果。** `PreToolUse` 回 allow 时 Claude Code 跳过它自己的权限检查（含 auto 的分类器）；`settings.json` 里你写的 deny 仍先生效。选择题（`AskUserQuestion`）不看放行，照样进手机。
- **上限。** 时长 5 分钟～24 小时，超界截到边上（`railse trust 1m` 建成 5 分钟，`48h` 建成 24 小时）；每台机器同时生效至多 20 条，超出拒建。

### 三个入口

```sh
railse trust [时长] [--session <会话 id>] [--note 文字] [--dir 目录]   # 时长如 30m、2h、8h，默认 2h
railse trust list [--dir 目录]                                    # 每条一行：id、范围、剩余时长、谁建的、备注
railse trust revoke <id> | --all [--dir 目录]
```

- **终端。** 不带 `--session` 放行当前目录所在的项目，项目根与守护进程同一套算法（git 仓库根，或家目录下的普通目录）；定不出项目根（家目录及其上级、系统目录、点开头的目录等）时报错、不放行。这台机器上各个 CLI 在这个项目里的请求都算，含之后新开的窗口。`--session` 放行一个 Claude Code 会话（钩子输入里的 `session_id`），只对 Claude 会话有意义。命令经本机 socket 发给守护进程（管理命令 `trust_set`、`trust_list`、`trust_revoke`，与 pause、resume 同一通道）；升级后没重启的旧守护进程不认，提示重启它。
- **启动命令。** `RAILSE_TRUST=2h claude`：Claude 的 `PreToolUse` 钩子把这个环境变量原样带给守护进程（钩子不判断格式、不在 stderr 说话），守护进程第一次在这个会话里碰上定不了的请求时建一条会话放行（`created_by: env`，从那一刻起算）。只认 Claude `PreToolUse` 带来的、且要有会话 id；格式不对（不是 `30m`、`2h` 这类 Go 时长写法，或不是正数）只记 `agent.log`，按没有处理。**每个会话只建一次**：过期或被撤之后不再重建，要继续放行用 `railse trust --session` 或重开窗口；守护进程重启会忘掉见过哪些会话，那时还带着这个变量的会话会再建一条。
- **手机。** 批准时打开“接下来一段时间都放行”，选这个会话或这个项目、30 分钟 / 2 小时 / 24 小时。hub 建好放行，先把快照推给守护进程、再下发 allow。要守护进程与 hub 都是 0.2.3 以上，见 [hub.md](hub.md) 的“限时放行”。

看得见、撤得掉：`railse trust list`、`railse agent status` 的 `trusts: N`、`railse doctor` 的 `agent.trusts`（有生效中的报 ok、列条数，没有或守护进程没在跑 skip）；手机收件箱横幅与设置页“限时放行”一节。建、撤、到期各记一条审计 `kind: control`，`message` 为 `trust_set`、`trust_revoke`、`trust_expired`，`reason` 形如“项目 blog，到 11-02 15:30，cli 建”，随审计上报 hub。守护进程每分钟清一次到期的。

### `trusts.json`

数据目录里的 JSON 数组，每项一条放行：`id、scope、tool、cli_session_id、project_root、until、created_at、created_by、note`，外加 `synced`（hub 已收下）与 `revoked`（墓碑：本机撤掉、hub 可能还没收下的，只有 `id` 与原到期时间，不生效，过了原到期时间清掉）。原子写入、0600；读不了或损坏时记日志、按空表跑。不要手改：守护进程只在启动时读。

### 与 hub 同步

hello 里声明 `trust` 能力；连着同样声明了 `trust` 的 hub 时才同步，否则只用本机条目，不发、不标同步、hub 的快照也不看。

- **本机 → hub。** 本机建、撤、到期都经 `trust` 帧（`op` 为 `set`、`revoke`、`expired`）报给 hub，走单独的发送队列，不拖慢审批。
- **hub → 本机。** welcome 与之后的 `trusts` 帧带这台机器的全部放行。合并：快照里有的写入并标已同步；已同步却不在快照里的删掉（被手机撤了，审计 `trust_revoke`、`decided_by: hub`）；未同步的（断线期间或老 hub 时建的）保留并补发 `set`；本机撤掉的有墓碑，快照里还带着时不复活、重发 `revoke`。本机建的条目等 hub 的快照回来才算同步。welcome 不带 `trusts` 键时：welcome 远小于单帧上限就当空快照；否则可能是 hub 带不下、紧跟着单发，按不完整处理，不删任何条目。

## 角色（0.3 起）

角色是一道权限边界：给每个 agent 会话一个角色，角色限定它能自动做到哪一步（风险上限）、哪些事一律不许做（禁用清单）、限时放行最长管多久，以及它的地盘在哪些目录。

**定位：** 角色对“正常接入钩子、Railse 能识别的工具调用”提供约束，用来挡住 agent 的误操作和明显越权。它不是沙箱，不能保证一个被注入、刻意规避的 agent 无法用可见的命令绕过（下面“角色防什么、不防什么”列了已知的绕法）。要硬隔离，请配合操作系统级的沙箱或容器。设计见 [v0.3 角色设计](superpowers/specs/2026-11-11-railse-opc-v0.3-roles-design.md)。**没有 `roles.yaml` 时，除两处对 Railse 自己配置的保护外一切与 0.2.3 一样**：守护进程目录用 `--dir` 或 `RAILSE_HOME` 指到 `~/.railse` 之外时，里面的读写现在按 `destructive`（见“文件有误时”）；限时放行不再覆盖对 `~/.railse` 与守护进程目录的读写（见“限时放行”）。两处都是有意的收紧。差分测试（`internal/policy/nofile_diff_test.go`、`internal/agent/nofile_diff_test.go`）拿 main 上的代码逐条核对。

### `roles.yaml`

在守护进程的数据目录里（随 `--dir` 走，默认 `~/.railse/roles.yaml`），一个文件装全部角色。`railse role init` 写一份带注释的起步模板（开发 / 写作 / 运维；英文界面为 dev / writer / ops），模板里的 `dirs` 都注释着，写下它不改变任何会话。

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
```

| 字段 | 必填 | 含义 |
| --- | --- | --- |
| `name` | 是 | 1～32 个字符，不含空白与控制字符，文件内唯一；可以是中文 |
| `note` | 否 | 一行说明（≤120 字符），手机总台上看得到 |
| `dirs` | 否 | 路径模式（`~` 与 `**` 通配，同规则的 `scope.project`：等于或通配命中项目根）。按目录绑定会话；也划定角色的地盘 |
| `ceiling` | 否 | 风险上限，`read` 到 `destructive` 之一；风险**高于**它的请求超出边界。不写不设上限 |
| `forbid` | 否 | 禁用清单，每条的写法与规则的 `match` 相同（同一套匹配：任一段命中即算、看穿 `sh -c`）；不能有空条目 |
| `trust_max` | 否 | 限时放行最长多久（`30m`、`2h`，5m～24h，超界报错不截断），或 `none`：这个角色的请求从不走限时放行 |

校验与规则文件一样严格：未知字段、拼错的键、重名、坏正则、坏时长都报错。两个角色的 `dirs` 重叠不算错，按文件先后取第一个，`railse role check` 会提醒。

### 会话绑到哪个角色

钩子（三家 CLI 的所有事件）把环境变量 `RAILSE_ROLE` 原样带给守护进程，守护进程按这个顺序定生效的角色，并把它写进审计与上报 hub 的 `session.role`：

1. `RAILSE_ROLE` 是 `roles.yaml` 里有的名字 → 就是它（`RAILSE_ROLE=写作 claude`）。
2. 否则按项目根（没有项目根时按 cwd）匹配各角色的 `dirs`，文件顺序第一个命中的。
3. 都没有 → 未分配，按规则走。

`RAILSE_ROLE` 指了不存在的名字时按第 2 步走，`agent.log` 里每个会话记一行。

- **`dirs` 与 cwd 同一口径。** cwd 是解析过符号链接的真实路径，`dirs` 里通配之前的那部分也解析符号链接（原样的写法照样算）；macOS 上比较不分大小写，同敏感路径表。通配段本身（如 `~/dev/*`）匹配的是链接名还是真实名不读目录分不出，不做这一步。
- **按会话钉住。** 解析顺序是：`RAILSE_ROLE` 指向存在的角色 → 当前目录命中的角色（哪怕与之前的不同）→ 这个会话（`tool` + `cli_session_id`）钉住的角色。前两步得出角色时把钉住更新成它；当前目录不属于任何角色时沿用钉住的，不降为未分配，所以从严格角色的目录走到无角色的目录仍按严格角色判，走进宽松角色的目录则按那个角色判。钉住只在判定审批时建立与更新，选择题与事件只读取；角色从 `roles.yaml` 里删掉时解除。钉住表只在内存里，至多 4096 个会话（满了淘汰最久没用的）、7 天没用的清掉、`session_end` 时释放；守护进程重启或过期后回来的会话按上面的顺序重新解析。

### 判定顺序

| 顺序 | 条件 | 结果 |
| --- | --- | --- |
| 1 | 你手写的 deny 规则（`source: user`）命中 | `deny`，压过一切 |
| 2 | 角色的 `forbid` 命中 | `deny`，审计 `auto_denied`、`decided_by: role:<角色名>`，理由“角色「写作」禁用：command_prefix "git push"”原样给模型看；不进手机 |
| 3 | 按优先级选出的规则是其余来源（内置、学来的）的 deny，且没暂停 | `deny`（比越界的 ask 更严，不被放宽）。学来的 deny 输给命中的用户 allow 是 0.2 的既定语义，那时选中的是 allow，照样走第 4 步 |
| 4 | 风险高于 `ceiling`，或角色有按命令的禁用而命令里有解析不了的结构 | 越界：规则的 allow（含写了 `risk: destructive` 的 user allow）与限时放行都不适用，必须人来定。审计 `beyond_role: true`，理由“超出角色「写作」的边界（上限 write_in_project，这次是 outward）” |
| 5 | 其余 | 照旧：暂停、规则、底线、限时放行、默认 ask |

- **改 Railse 设置的命令。** 有角色的会话（含 `roles.yaml` 有误时）里，给 `RAILSE_*`、`HOME`、`XDG_*`、`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`GEMINI_*` 赋值或删除它们的命令（前缀赋值、`env`、`env -u`、`export`、`declare`、`typeset`、`readonly`、`local`、`unset`，以及 `sh -c`、命令替换与 `eval` 里的同样写法；设置类命令的参数含变量展开时看不出改的是谁，也算）按越界处理，理由“试图更改 Railse 的角色或放行设置”。典型是嵌套起一个换了角色的 CLI（`RAILSE_ROLE=运维 claude …`）。读取（`echo $RAILSE_ROLE`）不算；未分配的会话照旧。
- **禁用看穿命令替换。** `$(…)`、反引号、进程替换 `<(…)` `>(…)` 与 `eval` 里的命令逐层参与 `forbid` 的匹配（`echo "$(git push)"` 照样拒）。角色带有按命令的禁用（`command_prefix`、`command_regex`）而命令里有解析不了的结构（heredoc、没闭合的引号或括号、裸括号、带转义的 `$'…'`、嵌套过深）时，看不见里面有没有禁用的命令，按越界问人（理由“含无法检查的子命令”），不直接拒、也不走限时放行。`eval` 剥掉 `--` 后再看；`eval` 与 `sh -c` 类的命令参数含变量展开或通配（`eval "git ${OP}"`、`bash -c "$CMD"`）时运行时才知道是什么命令，同样算解析不了。展开有上限（`sh -c` 嵌套 32 层、命令替换 8 层、累计 2 MiB 文本或 1024 条命令），超了也按解析不了处理。超过 128 KiB 的 shell 命令根本不解析：直接定为 `unknown`、规则不匹配（没有角色时问人），有角色时按“含无法检查的子命令”越界；字面上提到 `.railse` 或守护进程目录的照样算触及配置。
- **暂停时**第 1、2、3 步照常生效（拒绝不受暂停影响）；越界（第 4 步，含文件有误）也不归暂停管：`PreToolUse` 照旧回 `ask`，其余照旧送人工决定——交回终端在 auto、acceptEdits、bypassPermissions 下等于放行。其余交回终端。
- **越界的去向。** Claude 的 `PreToolUse` 一律回 `ask`：Claude 在终端弹确认框（理由是角色那句）并触发 `PermissionRequest` 送手机；中间几档（项目内写、联网）也一样，不交 Claude 自己的分类器——它可能自动放行。`PermissionRequest` 与 Codex 的请求送 hub，`pending` 帧带 `beyond_role: true`，手机上不出现“记住”与“接下来一段时间都放行”。越界的请求也不按 `RAILSE_TRUST` 建放行。
- **越界在各来源上的去向。** 越界必须人来定；问不到人的地方一律拒，绝不交回终端了事：

  | 来源 | 没有 hub / hub 不可达 | hub 可达 | 暂停 |
  | --- | --- | --- | --- |
  | Claude `PreToolUse` | `ask`：终端确认框 | `ask`，随后的 `PermissionRequest` 送手机 | 同左，暂停不管越界 |
  | Claude `PermissionRequest`、Codex | 交回终端：这类钩子只在 CLI 本来就要问人时触发，交回去就是 CLI 自己的确认框 | 送手机，手机定 | 同左 |
  | Gemini | `deny`：它的钩子只能拒不能放，交回终端等于继续执行（yolo、headless 下直接跑），也不进 hub | `deny`（同左） | `deny` |

  Gemini 被拒时 `decided_by` 为 `role:<角色名>`（文件有误时为 `roles_yaml`），理由写明“Gemini 无法在这里问人：换个角色，或在别处执行”。

  触及 Railse 自己的配置（见“限时放行”）同样必须人来定：Gemini 在任何 hub 状态、暂停与否下都 `deny`（`decided_by: control_plane`）；Claude 与 Codex 照越界的路走，只是暂停时按 0.2.3 交回终端。整张表（越界、文件有误、触及配置 × 来源 × hub × 暂停）有走完整钩子链路的测试（`cmd/railse/roles_chain_test.go`）。
- **`trust_max`。** 限时放行命中时再看会话角色：设了时长的，这条放行只在它建立后 `trust_max` 之内对这个角色有效；`none` 一律不放行。不改放行本身。
- **地盘。** 角色写了 `dirs`、而项目根不在任何一条 `dirs` 内（典型：用 `RAILSE_ROLE` 在别处启动）时，按没有项目根处理：写入一律 `write_outside`，项目放行也不命中。地盘内项目根不变，不扩大到角色的全部目录。没写 `dirs` 的角色不做这一步。
- **选择题与事件**（`AskUserQuestion`、question、done、tool_done）不经判定，只带上角色名。

### 文件有误时

守护进程每次判定前读一遍 `roles.yaml` 全文、按内容比较（不靠修改时间），变了就重新解析，改完不用重启（`railse agent reload` 也会重读）。读到坏文件（解析失败、校验不过、读不了、空文件、不是普通文件、超过 256 KiB）就 **fail closed**：所有会话按上限 `read` 处理——只读照旧自动放行，其余一律越界问人，理由“roles.yaml 有误：<错误>”；不沿用上一版好文件，免得一次手误悄悄放宽或悄悄失效。修好后下一条请求就恢复。

`roles.yaml` 在 `~/.railse` 下，本来就是敏感路径：agent 改它按 `destructive` 定级，除非角色的上限就是 `destructive`，否则越界问人。数据目录用 `--dir` 或 `RAILSE_HOME` 指到别处时，守护进程把它（原样与解析符号链接后的真实路径）作为受保护目录带进定级，里面的 `roles.yaml`、`rules.yaml`、`trusts.json` 等的读写同样按 `destructive`。

已知限制见下一节“角色防什么、不防什么”。

### 嵌套会话

有角色的会话里再起一个接了钩子的 CLI（`claude -p …`、`gemini -p …`），不管有没有改 `RAILSE_ROLE`、在不在别的目录、是不是写在脚本里，新会话的审批请求继承上层会话的角色约束，看的是进程树，不是环境变量。Codex 只接了 `PermissionRequest`：`codex exec` 与免确认模式下钩子根本不触发，嵌套起的 Codex 不受任何角色约束（见“不防什么”）。做法：

- 守护进程用连接对端的进程凭据（macOS `LOCAL_PEERPID`、Linux `SO_PEERCRED`）取钩子的进程号，自己重建祖先进程链（进程号、启动时间、名字，从钩子的父进程往上到 1 号进程，至多 32 层；macOS 用 `sysctl kern.proc.pid`、Linux 读 `/proc`，不跑外部命令），不用请求里自报的。取不到凭据的平台才退回用钩子自报的那份，审计 `ancestry` 标 `self_reported`（重建的标 `peer`）。
- 守护进程把链里第一个不是 shell 的进程当作这个会话的 CLI（各家 CLI 多经 `sh -c` 起钩子，直接父进程是一次性的 shell），记下约束它的角色。
- 新请求的祖先链里、在它自己的 CLI 之上，若有另一个有角色的会话的 CLI（进程号与启动时间都对上，防进程号复用），生效约束取两边更严的：上限取低的，禁用取并集，放行时长上限取短的（`none` 最短），地盘要同时在两边的 `dirs` 里。角色名显示为自己的（自己未分配时显示上层的），理由注明“继承自上层会话的角色「写作」”。三层嵌套照样一路传下去。
- 取不到祖先链时按没有上层处理。`session_end` 不释放记录（它可以伪造：agent 在一次 Bash 调用里给钩子喂一条），记录在那个 CLI 进程退出（进程号加启动时间对不上）后由每分钟一次的清理清掉，另有 7 天 TTL 兜底。
- 生效角色（含继承）记在会话上，答题与事件也报同一个角色。

另有尽力而为的字符串一层：有角色的会话里，给 `RAILSE_*`、`HOME`、`XDG_*`、`CLAUDE_CONFIG_DIR`、`CODEX_HOME`、`GEMINI_*` 赋值或删除它们的命令按越界处理（见“改 Railse 设置的命令”）；包装命令按 basename 认（`/usr/bin/env` 等同 `env`），`env`、`sudo`、`doas`、`xargs`、`timeout`、`nohup`、`nice`、`caffeinate`、`time`、`command`、`exec` 的选项按表解析：`env -C`、`sudo -D` 改了目录的，内层按改后的目录定级；`sudo -R`、`sudo -e`、`time -o`、`xargs -a` 这类改根目录、写输出、读参数文件的选项，以及表里没有的选项，整段至少 `unknown`。定级取包装自身与内层两者较高的；`deny` 规则与禁用对内层也做 basename、git 全局选项等归一化。

### 角色防什么、不防什么

一句话：角色对“正常接入钩子、Railse 能识别的工具调用”提供约束，用来挡住 agent 的误操作和明显越权；它不是沙箱，不能保证一个被注入、刻意规避的 agent 无法用可见的命令绕过。要硬隔离，请配合操作系统级的沙箱或容器。

**会拦下的（前提：调用经过了装好的钩子）：**

- 风险高于角色上限的调用，按越界交人（Claude `PreToolUse` 回 ask；`PermissionRequest` 与 Codex 送手机，没接 hub 时交回 CLI 自己的确认框；Gemini 拒）。0.3 起 Claude 的 MCP 工具与 WebSearch 也经过 `PreToolUse`（会话有生效角色时）。
- 角色禁用的命令，包括写在 `sh -c`、命令替换、进程替换、`eval`、表里那些包装命令里面的。
- 有角色的会话里改 `RAILSE_*`、`HOME`、`XDG_*` 等变量（嵌套起一个换了配置的 CLI），按越界交人；从有角色的 Claude、Gemini 会话里嵌套起的、接了钩子的 CLI 继承上层的约束（看进程树）。
- 读写 Railse 自己的配置（`~/.railse`、任何 `.railse` 目录、实际的守护进程目录）：不接受任何自动放行（规则、限时放行都不行），暂停也不例外。
- 审批请求拿不到策略结论（超长载荷、读不懂的答复、启用过却连不上守护进程）：Claude `PreToolUse` 回 ask、Gemini 拒。

**看不清的，不一定交人：** 只有角色带按命令的禁用（`command_prefix`、`command_regex`）时，解析不了的结构（heredoc、没闭合的引号、`eval "$X"`、`bash -c "$CMD"` 等）才强制越界。其余情况下这类命令定为 `unknown`：放行规则不认它，但上限在 `unknown` 以上的角色、没有角色的会话、限时放行期间，它都可能被放过。超过 128 KiB 的命令同理：有角色时越界，没有角色时只是 `unknown`。

**不防的（已知限制，五轮审查里定为不修的与本轮新确认的都在这里）：**

- **脚本与解释器里的内容。** Railse 看到的是 agent 要跑的那一行命令，看不到 `bash deploy.sh`、`make release`、`npm run x`、`python -c "…"` 里面做了什么；这些里面的 `git push` 不会被 `git push` 的禁用拦下，只按外层那一行定级。
- **变量拼出来的命令与路径。** `X=git; $X push`、`cat "$D/rules.yaml"` 这类，命令名或路径要运行时才知道：定为 `unknown`，禁用与配置目录检查看不出来（工作目录在 `cd "$X"` 之后算不出时，相对路径一律按触及配置处理，但绝对路径里的变量仍看不出）。
- **脱离进程树的子进程。** `setsid`、`nohup … &` 后父进程退出、经 launchd / cron / systemd、ssh 到别的主机启动的 CLI，祖先链里没有上层会话，继承不到它的角色。
- **Codex 的免确认模式与 `codex exec`。** Railse 只接了 Codex 的 `PermissionRequest`，它只在 Codex 本来就要问人时触发；Codex 自己不问的调用，角色管不到，嵌套起的 Codex 也继承不了。
- **钩子之外的工具。** Claude 的 `Read`、`Glob`、`Grep` 等纯读取工具不过钩子；MCP 服务器自己在里面跑命令的，只能按 `{payload: mcp}` 整类禁用，看不到它跑了什么。
- **共享宿主进程。** 一个进程里跑着几个会话时（如桌面应用托管多个会话），它们共用同一个 CLI 进程记录：嵌套在其中任一会话下的 CLI 继承的是最近一次审批写下的那个会话的角色，不一定是真正的上层。
- **钩子启动器不是 shell 时可能继承不到。** 守护进程把祖先链里第一个不是 shell（`sh`、`bash`、`zsh`、`dash`、`fish`、`ksh`、`tcsh`、`csh`、`env`）的进程当作会话的 CLI；CLI 经别的启动器（如 `npx`、`uv run`）起钩子时，记下的是一次性的启动器进程，嵌套会话对不上它。
- **表满淘汰。** 钉住、生效角色与继承记录各至多 4096 个会话，满了淘汰最久没用的；被淘汰的上层会话，其下新起的 CLI 继承不到。
- **伪造 `session_end` 不再有用，但清理有延迟。** 记录在 CLI 进程退出后至多一分钟内清掉；取不到进程信息的平台只靠 7 天 TTL。
- **符号链接。** 写入目标经项目里的符号链接指向受保护目录或敏感路径时，定级只看字面路径、不解析链接（所有敏感路径共有的老问题）；建这种链接的 `ln -s` 本身因目标是敏感路径而按 `destructive` 定级，会被问到。
- **大小写敏感的卷。** `dirs` 与敏感路径在 macOS 上按不分大小写比较；装在大小写敏感的 APFS 卷上时，大小写不同的两个目录会被当成同一个。`dirs` 里通配段（`~/dev/*`）匹配的是链接名还是真实名不解析。
- **限时放行期间的别的敏感路径。** 放行期间，除 Railse 自己的配置外，范围内引擎定不了的请求一律放过（0.2.3 的语义）：没有角色、或角色上限是 `destructive` 时，`.env`、ssh 密钥等的读写照样被放过。
- **暂停与 Gemini 的旧口径。** 暂停时，不越界、不碰 Railse 配置的请求照 0.2.3 交回终端；Gemini 不越界、不碰配置的请求照 0.2.3 交回它自己的确认。
- **学来的 deny 输给用户 allow。** 学来的 `deny` 输给命中的用户 `allow` 是 0.2 的既定语义，这时按选中的 allow 走（有角色时照样受上限约束）。
- **换目录换角色、删掉角色。** 会话从严格角色的目录走进宽松角色的目录后按宽松的判（角色约束的是会话在哪干活）；只有走到不属于任何角色的目录时才沿用原来的。角色从 `roles.yaml` 里删掉后，钉住它的会话解除钉住；文件被删等于没有角色。
- **选择题。** `AskUserQuestion` 不经判定，只带角色名。

### 命令

```sh
railse role list  [--dir 目录]   # 每个角色一行：名字、上限、禁用条数、放行上限、目录、备注
railse role check [--dir 目录]   # 校验；提醒目录重叠、指向不存在目录的模式；有误退出 1
railse role which [--dir 目录]   # 当前目录（与当前终端的 RAILSE_ROLE）会解析成哪个角色、凭什么；地盘外会说明
railse role init  [--dir 目录]   # 文件不存在时写起步模板（0600），已存在不覆盖
```

`list`、`which` 经本机 socket 问守护进程（管理命令 `role_list`、`role_which`）；守护进程没在跑、或是升级前启动的旧进程时直接读文件并在 stderr 注明。`railse doctor` 在 `roles.yaml` 存在时多一项 `agent.roles`：合法 ok，有误 fail（不存在时不出这一项）。

### 与 hub

hello 里声明 `role` 能力并带角色摘要（名字、备注、上限、禁用条数、放行上限分钟数——不设为 0，`none` 为 -1——与目录模式）；文件有误时不带摘要。重读成功、且这条连接的 hub 也声明了 `role` 时，整份重发一帧 `roles`（删掉文件就发空表），hub 整份替换这台机器的角色表。去旧只在守护进程这一侧：重读与通知串行、带单调代数，比已交出的旧的不发；hub 不看代数，按收到的顺序整份替换。老 hub 配新守护进程：角色照样在本机生效，只是手机上看不到；新 hub 配老守护进程：总台只有“未分配”。

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
| 定不了，命中限时放行（0.2.3 起） | allow，理由“Railse：限时放行中”（见“限时放行”） |
| 定不了，对外或不可逆 | 立刻回 `ask`（0.2.2 起不在这里等手机）：终端弹确认框，随后的 `PermissionRequest` 送手机，谁先答谁算 |
| 定不了，联网、项目外改动、无法判断 | 不表态（审计 `passthrough`、`decided_by: classifier`），先让 Claude 自己的 allow 列表、auto 的分类器处理；Claude 要问人时 `PermissionRequest` 再送手机。`~/.railse/settings.yaml` 写 `claude_undecided: phone` 则与上一行一样回 `ask` |

- **本机设置 `~/.railse/settings.yaml`。** 目前只有 `claude_undecided: classifier | phone`（默认 `classifier`）。不放 `rules.yaml`：那份由 hub 下发覆盖，解析时也拒绝未知字段。守护进程启动、`railse agent reload` 与 SIGHUP 时读；读不了、有未知字段或值不对时按默认值跑，reload 回报错。
- **硬底线：终端与手机同屏（0.2.2 起）。** 实测（[findings](superpowers/specs/2026-11-01-pretooluse-ask-findings.md)）`permissionDecision: "ask"` 在 auto 模式下也强制让人确认，不回分类器；交互式终端里弹出带理由的确认框，并且照样触发 `PermissionRequest`。所以对外与不可逆两档在 `PreToolUse` 不等任何东西，立刻输出 `ask`，不看 hub 状态；理由由钩子按终端语言拼：“Railse：命令里的「rm -rf after」会删除文件，删了不可恢复。已发到手机，也可以在这里直接批”（引擎给出触发点：哪一段、什么性质）。随后的 `PermissionRequest` 按老路送手机：手机先答 → 输出 allow/deny，终端框关闭；终端先答 → Claude 结束钩子，守护进程向 hub 取消这一条；hub 不可达等 → 不表态，框还在。`claude -p` 下 `ask` 等于拒绝，Claude 收到理由文本。中间三级交回终端一律输出空，免得比 auto 模式更吵。
  各权限模式下硬底线的表现：

  | 权限模式 / 宿主 | 硬底线的表现 |
  |---|---|
  | 终端 CLI：default / acceptEdits / plan / auto | 终端确认框 + 手机卡片，谁先答谁算；终端先答时 Claude 结束 `PermissionRequest` 钩子 |
  | `-p` 非交互 | `ask` 等于拒绝，Claude 收到理由文本 |
  | bypassPermissions / dontAsk | 待真机验收第 6 条测出来后填 |
  | Claude Code 桌面应用 / SDK 宿主 | `ask` 由宿主的权限回调裁决（auto 模式是宿主的分类器，不一定经人）；`PermissionRequest` 钩子不随之结束，由 `PostToolUse`（工具跑完）与 `Stop`、`SessionEnd`（一轮或会话结束）事件清理：守护进程取消同会话里挂着的请求，hub 记 `cancelled`、`decided_by: terminal`（取消原因“已在终端处理，取消等待”，钩子收到 passthrough 退出） |
- **不再去重。** 0.2.1 的去重表已删：`PreToolUse` 从此不把任何请求送手机，`PermissionRequest` 来的每一条都该送（硬底线是约好的第二步，中间三级是 Claude 自己升级成要问人）。两条审计靠会话 + 载荷对上（`tool_use_id` 只有 `PreToolUse` 有）。
- **放行是终局。** `PreToolUse` 回 allow 时 Claude Code 跳过它自己的权限检查（含 auto 的分类器），所以内置放行只覆盖只读与项目内改动两档；`settings.json` 里你写的 deny 规则仍先于钩子生效。
- **失败即不表态。** 守护进程不在、hub 30 秒不可达、解析失败、超时，钩子都输出空（硬底线例外，见上一条），Claude 照自己的流程走；hub 不可达时 stderr 多一行 `railse hook: 连不上 hub，交回 Claude 自己的确认`。
- 审计的 Claude 审批条目多三个字段：`source`（`pretooluse` / `permissionrequest`）、`permission_mode`、`tool_use_id`。0.2.2 起硬底线的 `PreToolUse` 条目为 `action: ask`、`state: asked`、`decided_by: ask`；引擎定级有触发点的审批条目多一个 `trigger`（`{segment, why}`，段已脱敏），随审计上报 hub。

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
- **钩子异常时多数不作决定。** 守护进程没起、socket 连不上、输入损坏、不认识的 CLI、收到 SIGTERM/SIGINT/SIGHUP、内部出错，钩子都是 stdout 为空、退出 0，原因写到 stderr。最坏情况就是回到没装 Railse 时的样子。
- **拿不到策略结论时不放过（0.3 起，相对 0.2.3 的有意收紧）。** 守护进程在，但审批请求拿不到结论——编码后超过 1 MiB 发不出去（如在 `git push origin main #` 后面垫一大段注释）、发送或等待中出错、守护进程回了读不懂的东西或不认识的决定、无响应，以及钩子输入超过 4 MiB 读不全——时：Claude 的 `PreToolUse` 回 `ask` 并写明“拿不到策略结论（原因）”，Gemini 的 `BeforeTool` 回 `deny`；Claude 的 `PermissionRequest` 与 Codex 照旧交回（它们本来就在问人）。以前这些情况一律交回，在 auto、bypassPermissions、yolo 下等于放行。连不上 socket 时再分两种：这台机器没启用 Railse（守护进程目录不在，或目录在但没有 `machine.json`、也没有残留的 `agent.sock`）不变，仍然交回，没装守护进程的人不该被卡住；启用过却连不上（接入过 hub，或 socket 文件还留着——守护进程正常退出时会删掉它）按拿不到结论处理，同上。

## 冒烟

`scripts/smoke-claude.sh` 在临时目录里起守护进程、装 Claude 钩子，再让 `claude -p` 执行一条 `touch`，最后打印审计。需要本机 Claude Code 已登录，会消耗模型额度，不进 CI。脚本用 `--setting-sources project,local`：钩子在 `settings.local.json`（`local` 来源），只写 `project` 读不到；不加 `user`，免得用户级设置替人审批。接入 hub 的那条链路由 `scripts/smoke-claude-hub.sh` 冒烟：临时的 hub 与守护进程，`claude -p` 跑一条 `git push`（引擎定不了，送 hub），脚本用 REST 当手机批准，断言 hub 里该请求是 `approved`、`decided_by: user` 且已送达，本机审计有 `approved`；发布前手动跑。

## 接入 hub 后

有 `~/.railse/machine.json` 时，引擎定不了的请求不再直接交回终端，而是送到 hub 等手机决定；规则以 hub 为准，本机 `rules.yaml` 只是缓存。总开关的生效值是本机与 hub 两者取或：`railse agent pause`/`resume` 只改本机这一侧，不会被 hub 覆盖，也不同步到 hub，`railse agent status` 显示暂停来自 `local`、`hub` 还是 `local+hub`。连不上 hub 时最多等 30 秒就交回终端（审计 `expired`、`decided_by: hub_unreachable`）；Gemini 的请求永远不进 hub。接入后本机审计的 `decided_by` 还会出现 `user`（手机上的决定）、`hub_unreachable`、`gemini`；手机上的备注记在 `note` 字段，钩子与模型只看到“已在手机上批准/拒绝”这类固定文案。接入步骤、行为变化与 API 见 `docs/hub.md`。
