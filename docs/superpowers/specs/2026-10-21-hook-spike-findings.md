# Railse OPC 钩子实验结果（设计文档第 8 节前四项）

- 日期：2026-10-21
- 机器：作者的 Mac（Darwin 25.6.0，arm64），Node 20.20.0
- 对应设计：`2026-10-21-railse-opc-design.md` 第 8 节第 1 至 4 行；第 5 行（iOS Web Push）不在本次范围
- 录制样本：`internal/adapter/testdata/<cli>/<版本号>/`

| CLI | 实测版本 | 登录态 | 模式 |
|---|---|---|---|
| Claude Code | 2.1.280 | 本机已有登录，可用 | headless（`claude -p`）与交互都测了 |
| Codex CLI | 0.145.0 | 本机 ChatGPT 登录，可用 | `codex exec` 与交互都测了 |
| Gemini CLI | 0.28.0，实验中途自动升级到 0.65.0 | 不可用，模型改用本地桩服务 | 两个版本的 headless，0.65.0 的交互 |

## 0. 一页结论

| 第 8 节事项 | Claude Code 2.1.280 | Codex CLI 0.145.0 | Gemini CLI 0.28.0 / 0.65.0 |
|---|---|---|---|
| 权限钩子能否返回 allow / deny / 不作决定 | 能。`PermissionRequest` 三种都实测生效 | 能，但只在交互模式。`codex exec` 强制 `approval_policy=never`，权限钩子从不触发 | 不能 allow。`BeforeTool` 只能 deny；0.65.0 多了 ask。allow 不会跳过原生确认框 |
| 超时可否设到 24 小时、到期行为 | `timeout: 86400` 被接受；660 秒后作答仍生效。到期发 SIGTERM，按不作决定处理：交互弹原生框，`-p` 自动拒绝 | `timeout: 86400` 被接受，`Stop` 钩子 660 秒后作答仍生效。到期杀进程（推断 SIGKILL）后弹原生框。代码模式下模型约 8 到 10 分钟主动放弃等待，长等待实际不可靠 | 0.65.0 下 `timeout: 86400000`（毫秒）被接受，660 秒后作答仍生效。到期发 SIGTERM，按不作决定处理：交互弹原生框，yolo 模式直接执行 |
| 等人回话事件 | 有：`Notification`，类型 `permission_prompt`（约 6 秒后）与 `idle_prompt`（约 60 秒后） | 没有专门事件；只有 `PermissionRequest` 本身 | 有：`Notification`，类型 `ToolPermission`，仅权限确认；没有空闲事件 |
| 任务结束事件 | 有：`Stop`，带 `last_assistant_message` | 有：`Stop` 钩子，另有 `notify` 的 `agent-turn-complete` | 有：`AfterAgent`，带 `prompt` 与 `prompt_response` |

对设计最重要的四点：

1. Claude Code 完全符合设计假设，可以按第 3 节原样实现。
2. Codex 的自动审批只在交互会话里可行。长时间挂起审批会被模型的代码模式轮询打断，并以同一命令重新发起一次新请求。
3. Gemini 无法由钩子自动放行。按第 8 节的替代方案，Gemini 只实现 deny 与 ask。在 yolo 模式下把钩子当唯一闸门不安全，因为钩子超时、非 0 非 2 退出码、无输出都会放行。
4. Gemini CLI 默认会自动升级，本次实验中途就从 0.28.0 升到了 0.65.0，钩子触发顺序随之改变。适配器必须按版本区分，`railse doctor` 要报告 Gemini 版本。

## 1. 实验方法

### 1.1 记录脚本

三个 CLI 共用一个 Node 脚本作为钩子命令（`node hook-logger.mjs <cli> <事件名>`），它做四件事：

- 把 stdin 的完整 JSON、命令行参数、所有以 `CLAUDE`、`CODEX`、`GEMINI` 开头的环境变量写到一个文件；变量名含 TOKEN、KEY、SECRET、AUTH、EMAIL 的值替换为 `<redacted>`。
- 按控制文件读取本次要返回的模式（allow、deny、不作决定、exit 2、原样 JSON）和要 sleep 的秒数。
- 捕获 SIGTERM、SIGINT、SIGHUP，记录被杀时间，用于判断 CLI 超时后如何结束钩子。
- 按该 CLI 的格式写 stdout。

Codex 的 `notify` 把 JSON 放在最后一个命令行参数里而不是 stdin，脚本对此单独处理。

### 1.2 隔离措施

- 每个 CLI 一个临时项目目录 `spike/<cli>/`，各自 `git init`，只用项目级配置。`spike/` 已加入 `.gitignore`，不入库。
- 三个用户级配置文件 `~/.claude/settings.json`、`~/.codex/config.toml`、`~/.gemini/settings.json` 均未修改，实验前后修改时间不变。
- 被测 CLI 都在 `env -i` 的干净环境里启动，只保留 HOME、PATH、LANG、TERM、TMPDIR。原因是宿主 Claude Code 会话的环境里有 `CLAUDE_CODE_MESSAGING_TOKEN` 等变量，不隔离就会泄漏进被测 CLI 和录制样本。
- Claude 一律加 `--setting-sources project`，避免用户级的 `defaultMode: auto` 让分类器替人审批。
- Codex 的项目信任通过命令行覆盖注入，不写用户配置：`-c 'projects={"<路径>"={trust_level="trusted"}}'`。注意不能写成 `projects."<路径>".trust_level=...`：路径里的 `.claude` 会被点号路径解析拆开，项目层就不被信任，项目钩子全部静默不加载。另加 `--dangerously-bypass-hook-trust` 跳过钩子哈希审核。
- Gemini 0.65.0 的所有运行都设 `GEMINI_CLI_HOME` 指向临时目录，在那里放信任目录列表并关闭自动升级，不碰 `~/.gemini`。

### 1.3 交互模式怎么测

交互模式用 macOS 自带的 `expect` 在伪终端里驱动 TUI：等待对话框文字出现，按键回答，再解析录下的屏幕输出。这只是实验手段，产品仍然不碰 PTY。

### 1.4 Gemini 为什么用桩服务

`~/.gemini/.env` 的 `GOOGLE_GEMINI_BASE_URL` 指向 `127.0.0.1:15721` 上的本地网关，实验时该端口没有进程监听；`GEMINI_API_KEY` 是 13 个字符的网关占位符，不是 Google 的 key；也没有 OAuth 凭据。所以 Gemini 的登录态不可用。

钩子的输入、输出和超时处理全部发生在 CLI 本地，与模型无关。因此另写了一个约 50 行的本地桩服务 `stub-server.mjs`，冒充 Gemini API：第一轮回一个 `run_shell_command` 调用（`touch spike-proof.txt`），收到工具结果后回 `DONE`。运行时只在该进程的环境里设 `GOOGLE_GEMINI_BASE_URL=http://127.0.0.1:18765` 和一个假 key，不改 `.env`。以下 Gemini 结论都基于真实 CLI 加桩模型；真实模型下的行为未验证。

### 1.5 触发命令

让 agent 执行 `touch spike-proof.txt`，Claude 另录了一条 `git push origin main`。两者都不具破坏性，`spike/claude` 没有远程仓库，且钩子返回了拒绝。

## 2. Claude Code 2.1.280

### 2.1 权限事件的输入

事件名 `PermissionRequest`，只在 Claude 准备向人要权限时触发，读操作不触发。headless 下 `git push origin main` 的真实输入如下，见样本 `claude/2.1.280/PermissionRequest.json`：

```json
{
  "session_id": "…",
  "transcript_path": "/Users/xufei/.claude/projects/-Users-xufei-RailseWork--claude-worktrees-dreamy-poitras-68f89d-spike-claude/….jsonl",
  "cwd": "/Users/xufei/RailseWork/.claude/worktrees/dreamy-poitras-68f89d/spike/claude",
  "prompt_id": "…",
  "permission_mode": "default",
  "effort": {"level": "medium"},
  "hook_event_name": "PermissionRequest",
  "tool_name": "Bash",
  "tool_input": {"command": "git push origin main", "description": "Push main branch to origin"},
  "permission_suggestions": [
    {"type": "addRules", "rules": [{"toolName": "Bash", "ruleContent": "git push *"}], "behavior": "allow", "destination": "localSettings"}
  ]
}
```

要点：

- 没有 `tool_use_id`，同一调用的 `PreToolUse` 里有。两者靠 `session_id` 加 `prompt_id` 加 `tool_input` 关联。
- `permission_suggestions` 每次不同。`touch` 那次给的是 `addDirectories` 和 `setMode: acceptEdits`。原因是工作目录路径里含 `.claude/`，Claude 把它当受保护目录处理。见 `PermissionRequest.interactive.json`。
- 交互模式多一个 `scratchpad_dir` 字段。
- 钩子进程的环境里，Claude 自己会注入 `CLAUDE_PROJECT_DIR`、`CLAUDE_CODE_SESSION_ID`、`CLAUDE_PID`、`CLAUDE_EFFORT`、`CLAUDE_CODE_ENTRYPOINT`，`-p` 下为 `sdk-cli`。另外还有 `CLAUDE_CODE_MESSAGING_SOCKET` 和 `CLAUDE_CODE_MESSAGING_TOKEN`。所以 `railse hook` 不能把自己的环境原样写日志或上报。

### 2.2 三种输出与 CLI 的实际反应

| 钩子输出 | headless `claude -p` | 交互 |
|---|---|---|
| `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`，退出 0 | 命令执行，文件被创建，结果 `DONE` | 命令执行，不出原生框 |
| `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"…"}}}`，退出 0 | 不执行。`message` 原文作为工具错误交给模型，进入结果 JSON 的 `permission_denials` | 未单独测；按文档与 headless 一致 |
| stdout 为空，退出 0（不作决定） | 自动拒绝，进入 `permission_denials`。模型看到的是 Claude 自己的拒绝文案 | 弹原生框“Do you want to proceed?”，选项为 Yes、Yes and always allow…、Yes and switch to auto mode、No |

实测命令：

```bash
claude -p "Run exactly this shell command with the Bash tool and nothing else: touch spike-proof.txt …" --setting-sources project --output-format json
```

### 2.3 超时

- 配置项是钩子条目里的 `timeout`，单位秒，command 类型默认 600。文档没有写上限。
- `timeout: 600` 与 `timeout: 86400` 都被接受，调试日志里没有任何校验报错。
- 实测超过 600 秒：设 `timeout: 86400`，钩子 sleep 660 秒后返回 allow。Claude 等满 660 秒，命令执行，结果 `DONE`，总耗时 665 秒。说明大于默认值的超时确实生效，没有被截到 600。
- 到期行为：设 `timeout: 5`、钩子 sleep 30 秒。第 5 秒钩子收到 SIGTERM，调试日志写 `Hook PermissionRequest:Bash … timed out after 5000ms`，然后按不作决定处理：
  - headless：自动拒绝。
  - 交互：弹原生审批框。
- 真正等满 24 小时到期：未验证。

### 2.4 等人回话与任务结束

| 事件 | 何时触发（实测） | 关键输入字段 |
|---|---|---|
| `Notification`，`notification_type: "permission_prompt"` | 原生审批框出现后约 6 秒，实测 6.0 秒 | `message: "Claude needs your permission"` |
| `Notification`，`notification_type: "idle_prompt"` | 回答结束约 60 秒后无人输入，实测 60 秒 | `message: "Claude is waiting for your input"` |
| `Stop` | 每轮回答结束；headless 与交互都触发 | `last_assistant_message`、`stop_hook_active`、`background_tasks`、`session_crons`、`permission_mode` |
| `SessionEnd` | 会话退出 | `reason`，实测为 `other` |

- 用户在审批框按 Esc 中断时，`Stop` 不触发。
- `idle_prompt` 不是“有问题要问”的专门信号，只表示“停下等输入”。
- 设计里的 `question` 可以用 `idle_prompt` 和 `permission_prompt` 映射，`done` 用 `Stop` 映射。
- Claude 的 `AskUserQuestion` 工具能否作为 `question` 信号：未验证。

### 2.5 其他观察

- 交互会话里，`Stop` 之后 2 秒又出现了一次 `PreToolUse`，命令是 `ls -l spike-proof.txt`，此时没有任何新输入。推测来自 Claude 的提示建议或预执行功能，未验证。它是只读命令，没有触发权限事件，但 `PreToolUse` 消费方需要容忍这种“回合外”的工具事件。
- `PermissionDenied` 钩子在以上所有拒绝场景都没有触发。按文档，它只用于 auto 模式分类器的拒绝。

## 3. Codex CLI 0.145.0

### 3.1 权限事件的输入

事件名 `PermissionRequest`。交互模式下，`touch` 在只读沙箱失败后，模型以 `sandbox_permissions: "require_escalated"` 申请提权，此时触发。真实输入见样本 `codex/0.145.0/PermissionRequest.json`：

```json
{
  "session_id": "01a15d8c-…",
  "turn_id": "01a15d8c-…",
  "transcript_path": "/Users/xufei/.codex/sessions/2026/10/21/rollout-….jsonl",
  "cwd": "/Users/xufei/RailseWork/.claude/worktrees/dreamy-poitras-68f89d/spike/codex",
  "hook_event_name": "PermissionRequest",
  "model": "gpt-5.6-sol",
  "permission_mode": "default",
  "tool_name": "Bash",
  "tool_input": {
    "command": "touch spike-proof.txt",
    "description": "Allow the requested command to create spike-proof.txt in the workspace?"
  }
}
```

要点：

- `tool_input.description` 是模型写的提权理由，不是命令描述。
- 没有 `tool_use_id` 和 `permission_suggestions`。
- 同一命令会先有一次 `PreToolUse`（沙箱内尝试），提权时再有一次 `PreToolUse` 和一次 `PermissionRequest`。
- 钩子进程的环境里只有 `CODEX_MANAGED_BY_NPM` 和 `CODEX_MANAGED_PACKAGE_ROOT`，没有会话号，会话信息只能从 stdin 取。

### 3.2 三种输出与 CLI 的实际反应

输出格式与 Claude 完全相同：`hookSpecificOutput.hookEventName = "PermissionRequest"`，`decision.behavior` 为 `allow` 或 `deny`，deny 时带 `message`。

| 钩子输出 | 交互模式实际反应 |
|---|---|
| allow | 不出原生框，命令在沙箱外执行，文件被创建，回答 `DONE` |
| deny | TUI 显示 `PermissionRequest hook (blocked)` 与 `feedback: railse spike: deny`。模型收到 `exec_command failed … Rejected("railse spike: deny")` |
| stdout 为空，退出 0（不作决定） | 弹原生框“Would you like to run the following command?”，选项为 Yes, proceed (y)、Yes, and don't ask again…（p）、No, and tell Codex what to do differently（esc） |

`codex exec`（headless）：

```bash
codex exec --ignore-user-config --dangerously-bypass-hook-trust --skip-git-repo-check --json -s read-only \
  -c 'approval_policy="on-request"' -c 'projects={"<spike/codex>"={trust_level="trusted"}}' "…touch spike-proof.txt…"
```

- 即使传了 `-c approval_policy="on-request"`，会话记录里的 `approval_policy` 仍是 `never`，钩子输入里 `permission_mode` 是 `bypassPermissions`。
- 沙箱拒绝直接交给模型，`PermissionRequest` 从不触发；`PreToolUse`、`Stop`、`SessionEnd` 正常触发。
- `codex exec` 没有 `-a` 参数。
- 所以 Codex 的权限审批只能覆盖交互会话。

另外两个容易踩的坑：

- `codex exec` 在 stdin 不是终端时会读 stdin 直到 EOF，后台运行必须加 `< /dev/null`，否则永远挂住。
- 用户配置里的模型 `gpt-6.1-sol` 在 0.145.0 下被服务端拒绝（ChatGPT 账号不支持），交互实验用 `-m gpt-5.6-sol`。

### 3.3 超时与长时间等待

- 配置项是钩子条目里的 `timeout`，单位秒，默认 600。`SessionEnd` 和 `Interrupt` 限 1 到 3 秒。
- `timeout: 600` 与 `timeout: 86400` 都被接受，没有报错。
- 到期行为：设 `timeout: 5`、钩子 sleep 30 秒。第 5 秒钩子被杀，记录脚本没有捕获到 SIGTERM，推断为 SIGKILL。TUI 显示 `PermissionRequest hook (failed)` 与 `error: hook timed out after 5s`，随后弹出原生审批框。
- 超过 600 秒被遵守：在 `codex exec` 里给 `Stop` 钩子设 `timeout: 86400`，钩子 sleep 660 秒后返回 `{"systemMessage":…}`。Codex 等满 660 秒，钩子没有被杀，进程总耗时 11 分 12 秒后正常退出。选 `Stop` 是因为它在回合结束后运行，不受下面说的模型轮询干扰；钩子执行器对各事件是同一套超时逻辑，但 `PermissionRequest` 本身等满 600 秒以上没有实测成功。

**长时间等待在实际使用里不可靠。** 两次实测设 `timeout: 86400`、钩子 sleep 660 秒：

- 0.145.0 搭配 `gpt-5.6-sol` 走“代码模式”：模型在一个 JS 单元里调用 `tools.exec_command`，单元因审批挂起。模型每 10 到 30 秒调用一次 `wait` 轮询，每次都是一轮模型请求。
- 第一次在约 570 秒后，模型以 `terminate: true` 结束单元，挂起中的钩子进程被杀，没有 SIGTERM 记录。随后模型用同一命令重新发起，又产生一次新的 `PreToolUse` 和 `PermissionRequest`。
- 第二次加了 `--disable code_mode_host`，没能关掉代码模式。约 516 秒、轮询 19 次后，模型结束单元，把早先的沙箱错误当作最终回答，回合结束。

对设计的含义：Codex 的审批挂起时会持续消耗模型调用，并可能在 10 分钟内被模型放弃。守护进程必须把“钩子进程消失”当作 `cancelled`，并接受同一命令紧接着以新请求重来。

### 3.4 等人回话与任务结束

| 事件 | 来源 | 关键字段 |
|---|---|---|
| `Stop` 钩子 | 每回合结束，headless 与交互都触发 | `turn_id`、`last_assistant_message`、`stop_hook_active`、`permission_mode`、`model` |
| `notify` 的 `agent-turn-complete` | 配置 `notify = [程序, 参数…]`，JSON 作为最后一个命令行参数 | `thread-id`、`turn-id`、`cwd`、`client`（`codex_exec` 等）、`input-messages`、`last-assistant-message` |
| `SessionEnd` 钩子 | 会话退出 | `reason`，实测为 `other` |

- Codex 没有“等人回话”的专门钩子。`notify` 目前只有 `agent-turn-complete` 一种。TUI 内置通知里的 `approval-requested` 只发终端转义序列，不调外部程序。
- 要知道“在等审批”，只能靠 `PermissionRequest` 本身。
- `notify` 只认用户级配置，项目级 `.codex/config.toml` 里的 `notify` 会被忽略。实验中用 `-c notify=[…]` 临时覆盖，这也顺带避免触发作者现有的 notify 程序。
- 交互里按 Esc 中断回合时，`Stop` 不触发；`Interrupt` 钩子未配置，未验证。

## 4. Gemini CLI 0.28.0 与 0.65.0

### 4.1 版本变化

实验开始时是 0.28.0。第一次交互运行（08:38:45 UTC）触发了 Gemini 内置的自动升级，08:38:52 包已被替换为 0.65.0，旧的 `dist/` 源码目录消失。本节 0.28.0 的结论来自自动升级前的 headless 运行和当时读过的源码，0.65.0 的结论来自之后的运行和新包的源码。用户的全局安装现在是 0.65.0，实验没有把它降回去。

### 4.2 权限事件的输入

Gemini 没有专门的权限事件，最接近的是 `BeforeTool`（每次工具调用前）。0.65.0 的真实输入如下，见 `gemini/0.65.0/BeforeTool.json`：

```json
{
  "session_id": "477e9d84-…",
  "transcript_path": "…/.gemini/tmp/gemini/chats/session-2026-10-21T08-52-477e9d84.jsonl",
  "cwd": "/Users/xufei/RailseWork/.claude/worktrees/dreamy-poitras-68f89d/spike/gemini",
  "hook_event_name": "BeforeTool",
  "timestamp": "2026-10-21T08:52:03.248Z",
  "tool_name": "run_shell_command",
  "tool_input": {"command": "touch spike-proof.txt", "description": "Create spike-proof.txt for the hook spike"}
}
```

- 0.28.0 的形状相同，transcript 是 `.json` 而不是 `.jsonl`。
- MCP 工具名在 0.65.0 文档里改成 `mcp_<server>_<tool>`，0.28.0 是双下划线。
- 真正表示“要人确认”的是 `Notification`，样本 `gemini/0.65.0/Notification.ToolPermission.json`：

```json
{"hook_event_name": "Notification", "notification_type": "ToolPermission",
 "message": "Tool Confirm Shell Command requires execution",
 "details": {"type": "exec", "title": "Confirm Shell Command", "command": "touch spike-proof.txt", "rootCommand": "touch"}}
```

### 4.3 BeforeTool 能否表达 allow

不能。两个版本原因不同，结论相同：钩子无法替用户跳过 Gemini 的原生确认框。

**0.28.0（源码）。** 调度器先查策略。策略要求确认时，先触发 `Notification` 并弹框，用户批准后进入执行阶段，`BeforeTool` 在执行函数里才触发。所以 `BeforeTool` 看到的都是已经批准的调用，只能事后拦截。

**0.65.0（源码与实测）。** 顺序改为先 `BeforeTool`，再查策略和确认：

- 钩子返回 `deny`（或 `block`，或退出 2）：直接报错 `Tool execution blocked: <reason>`，不弹框。
- 钩子返回 `ask`：强制弹框。
- 钩子返回 `allow`：源码里不改变策略决定。实测交互模式仍然弹出“Allow execution of [Shell] ?”，选项为 Allow once、Allow for this session、No, suggest changes。

三种输出的实测反应：

| 钩子输出 | headless 默认模式 | headless `--approval-mode yolo` | 交互默认模式（0.65.0） |
|---|---|---|---|
| 无输出，退出 0 | 不触发：非交互默认模式根本不注册 `run_shell_command`，模型调用得到 `Tool "run_shell_command" not found` | 执行 | 弹原生框 |
| `{"decision":"allow"}` | 同上，不触发 | 执行 | 仍弹原生框 |
| `{"decision":"deny","reason":"…"}` | 同上，不触发 | 拦截，`Tool execution blocked: …` | 拦截，不弹框 |
| 退出 2，stderr 为理由 | 不触发 | 拦截 | 未单独测，按源码同 deny |
| 退出 1 | 不触发 | **执行**，只记警告 | 未测 |
| `{"decision":"ask"}`（0.65.0） | 不触发 | **永久挂起**：触发 `Notification` 后等待一个不可能出现的确认，8 分钟后人工结束 | 弹原生框 |

### 4.4 超时

- 配置项是钩子条目里的 `timeout`，单位毫秒，默认 60000。
- 0.65.0 的实现用 Node 的 `setTimeout`，上限约 24.8 天，86400000（24 小时）在范围内。
- 到期行为（两个版本都实测）：第 5 秒钩子收到 SIGTERM，日志 `Hook timed out after 5000ms`，按不作决定处理：
  - yolo 模式：工具直接执行。0.28.0 和 0.65.0 都是如此，即使钩子原本打算 deny。
  - 交互默认模式（0.65.0）：回到策略，弹原生框。
- 超过 600 秒被遵守（0.65.0）：headless yolo 下设 `timeout: 86400000`，钩子 sleep 660 秒后返回 deny。Gemini 等满 660 秒，钩子没有被杀，deny 生效，工具被拦截，文件未创建。

### 4.5 等人回话与任务结束

| 事件 | 何时触发 | 关键字段 |
|---|---|---|
| `Notification`，`notification_type: "ToolPermission"` | 需要人确认工具时。0.65.0 实测：原生框要等这个钩子返回后才出现，钩子 sleep 15 秒，框就晚 15 秒 | `message`、`details.type/title/command/rootCommand` |
| `AfterAgent` | 每回合结束 | `prompt`、`prompt_response`、`stop_hook_active` |
| `SessionEnd` | 退出；0.28.0 headless 与 0.65.0 交互每次触发两次，0.65.0 headless 一次 | `reason`，实测为 `exit` |

- 没有“空闲等输入”事件。
- 0.65.0 有 `ask_user` 工具，可能经 `BeforeTool` 以 `tool_name: "ask_user"` 出现，未验证。
- `Notification` 钩子同步阻塞原生框，所以 Railse 的 `Notification` 钩子必须立即退出。设计里“question/done 类钩子发送后立即退出”的原则对 Gemini 是硬要求。

### 4.6 项目钩子的信任

- 0.28.0 headless 首次遇到项目钩子时打印警告，并自动把它们写入 `~/.gemini/trusted_hooks.json`。这是用户级文件，实验后已删除。
- 交互模式启用了文件夹信任：未受信任的文件夹会弹“Do you trust this folder?”，并且在信任前不加载项目设置。0.28.0 交互那次就因此一个钩子都没触发，原生框照样出现。
- headless 模式下项目钩子照常加载。

## 5. 对设计的影响

1. **Claude 适配器按原设计实现。**
   - 只挂 `PermissionRequest`，`timeout` 设 86400，守护进程期限为 86390 秒。
   - 不作决定就输出空 stdout 并退出 0。
   - `question` 用 `Notification`（`permission_prompt`、`idle_prompt`）映射，`done` 用 `Stop` 映射。
   - 同一会话已有 pending 的 permission 时，压掉随后约 6 秒到达的 `permission_prompt`，与设计文档 3.3 节一致。
2. **Codex 适配器只覆盖交互会话。**
   - `railse setup` 写钩子时要提醒：`codex exec` 和非交互调用没有审批，相当于第 8 节的替代方案“退回 approval_policy 加 notify 只展示”。
   - 守护进程必须处理“钩子进程被 SIGKILL 后同一命令再来一次”，把前一条标 `cancelled`、新一条照常进收件箱。去重可按 `session_id`、`turn_id`、`tool_input.command`。
   - 收件箱要提示：Codex 审批挂起时模型在持续轮询，最好尽快处理。
   - 项目级钩子需要项目被信任，且钩子需要在 `/hooks` 里审核；`railse setup` 写在用户级 `~/.codex/hooks.json` 更稳，审核一步仍需用户手动完成。
3. **Gemini 适配器采用第 8 节的替代方案：只实现 deny 与 ask。**
   - 0.65.0 起挂 `BeforeTool`，命中 deny 规则返回 `{"decision":"deny"}`，其余一律不输出，让 Gemini 自己的确认框和策略引擎处理。
   - 不要建议用户开 yolo 来换取 Railse 审批：钩子超时、崩溃（退出 1）、无输出都会放行，违反“永不误批”。
   - 不要在 headless 下返回 `ask`，会永久挂起。
   - `question` 用 `Notification`（`ToolPermission`）映射，钩子必须立刻退出；`done` 用 `AfterAgent` 映射。
   - Gemini 自动升级会改变钩子语义（0.28 到 0.65 就改了 `BeforeTool` 的位置），`railse doctor` 要检查 Gemini 版本并对低于 0.65 的版本报警。
4. **`railse hook` 的通用要求。**
   - 不得记录或上报自身环境变量：Claude 会注入 `CLAUDE_CODE_MESSAGING_TOKEN`。
   - 必须容忍 SIGTERM（Claude、Gemini）和 SIGKILL（Codex）。
   - stdout 只能有一个 JSON 对象。

## 6. 未验证与遗留

- 三个 CLI 都没有真正等满 24 小时观察到期行为；结论来自“配置被接受”加“大于 600 秒的等待被遵守”加“短超时到期行为”的组合。
- Gemini 的全部结论基于本地桩模型；真实模型下是否有额外的模型侧超时，未验证。
- Gemini 0.28.0 的交互模式没测成（文件夹未受信任，项目钩子未加载）；其 `Notification` 样本缺失。
- Claude `AskUserQuestion`、Gemini `ask_user`、Codex `request_user_input` 能否作为 `question` 信号，未验证。
- Codex 的代码模式能否关闭、关闭后长等待是否可靠，未验证；`--disable code_mode_host` 无效。
- Codex `Interrupt` 钩子、Claude 交互模式下的 deny 输出，未单独测。

## 7. 实验对本机的副作用

- 用户级配置 `~/.claude/settings.json`、`~/.codex/config.toml`、`~/.gemini/settings.json`、`~/.gemini/.env` 均未修改。
- **Gemini CLI 被它自己的自动升级从 0.28.0 升到 0.65.0。** 如需回到旧版：

  ```bash
  npm install -g @google/gemini-cli@0.28.0
  ```

- 实验在 `~/.gemini` 下产生了 `trusted_hooks.json`、两个空的 `projects.json.*.tmp`、`tmp/8435bff4…/` 会话目录。四者都只含本次实验数据，已删除。
- Claude 的交互实验在信任对话框里信任了 `spike/claude` 目录，记录在 `~/.claude.json`。Claude、Codex 在各自的会话目录（`~/.claude/projects/`、`~/.codex/sessions/`）留下了实验会话记录。这些是 CLI 正常使用都会写的位置，未清理。
