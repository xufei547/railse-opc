# PreToolUse `ask` 语义与 PermissionRequest 是否带 `tool_use_id`：实测

日期：2026-11-01。Claude Code 2.1.280（`claude --version`），作者的 Mac。v0.2.1 实施计划 Task 1。

## 做法

临时目录（`git init` 过的空项目），`claude -p --setting-sources project --settings <临时 settings.json>`：`--setting-sources project` 让用户级 `~/.claude/settings.json`（里面装着真实的 Railse 钩子）不加载，只有临时 settings 里的钩子生效，测试请求不会进真实的守护进程与手机。钩子都是 shell 脚本，把 stdin 追加到日志。

## 结论一：`permissionDecision: "ask"` 在 auto 与 default 下都强制弹确认，不回分类器

PreToolUse 匹配 `Bash`，钩子输出 `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"railse test"}}`；同时挂一个只记录的 PermissionRequest 钩子。

| 模式 | 命令 | 结果 |
|---|---|---|
| `--permission-mode auto` | `echo hi-auto` | 没执行。`permission_denials` 有这条 Bash；Claude 收到的错误正文就是 `railse test` |
| `--permission-mode default` | `echo hi-default` | 同上 |
| 对照：auto，PreToolUse 钩子不输出 | `echo hi-control` | 照常执行，输出 `hi-control`，`permission_denials` 为空 |

`-p` 下没人能答，弹框等于拒绝。对照说明 auto 的分类器本会放行 `echo`，所以 `ask` **不是**回到分类器，而是强制要人确认，`permissionDecisionReason` 作为提示给到 Claude。另外：PreToolUse 返回 `ask` 后，**PermissionRequest 钩子没有触发**（记录日志为空）——至少在 `-p` 下，`ask` 直接进 Claude 自己的确认，不经 PermissionRequest；交互式终端是否相同未测，由真机验收第 4 条覆盖。

据此计划里的 `ASK_OR_EMPTY` 取 **`ask`**，但只用于“手机上选了交回终端”（`decided_by: terminal`/`user` 的 passthrough）：

- 因模式不表态（auto 下中间三级交给分类器）必须输出**空**，输出 `ask` 会让 auto 模式下每个联网命令都弹框，比原来更吵。
- hub 不可达、超时等失败按设计 §2.6 仍输出**空**（fail-open）。实施报告里另列一条产品建议：硬底线在 hub 不可达时输出 `ask` 更稳（auto 下 `git push` 不会自己跑掉，人仍在终端里能批），待主会话定。

## 结论二：PermissionRequest 的输入没有 `tool_use_id`

PreToolUse 钩子不输出（不表态），PermissionRequest 钩子记录，`--permission-mode default` 跑 `curl -sI https://example.com/`：

- PreToolUse 输入带 `"permission_mode":"default"`、`"tool_use_id":"toolu_01Q3b2duFQSYPazYR6tUeKVz"`。
- 随后 PermissionRequest 触发，输入键：`session_id、transcript_path、cwd、scratchpad_dir、prompt_id、permission_mode、effort、hook_event_name、tool_name、tool_input、permission_suggestions`，**没有 `tool_use_id`**。两次事件的 `session_id`、`prompt_id`、`tool_input` 相同。
- `-p` 下没人批，命令未执行。

所以去重键按计划：有 `tool_use_id` 用它（只有 PreToolUse 有，等于 PreToolUse 与 PermissionRequest 永远对不上），否则 `session_id + 载荷哈希`。实现上 PreToolUse 一侧要**同时**按两种键记，PermissionRequest 一侧只能按会话+载荷查（见实施报告）。

## 原始记录

PreToolUse（default，curl）：

```json
{"session_id":"7e01f306-…","cwd":"…/t1/proj","prompt_id":"435e1fc3-…","permission_mode":"default","effort":{"level":"medium"},"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"curl -sI https://example.com/","description":"Fetch HTTP headers from example.com"},"tool_use_id":"toolu_01Q3b2duFQSYPazYR6tUeKVz"}
```

PermissionRequest（同一调用）：

```json
{"session_id":"7e01f306-…","cwd":"…/t1/proj","prompt_id":"435e1fc3-…","permission_mode":"default","effort":{"level":"medium"},"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"curl -sI https://example.com/","description":"Fetch HTTP headers from example.com"},"permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"curl -sI https://example.com/"}],"behavior":"allow","destination":"localSettings"}]}
```

auto + `ask` 的结果（节选）：`"permission_denials":[{"tool_name":"Bash","tool_use_id":"toolu_017CB1nP5FKw6ZctCM8TyRSw","tool_input":{"command":"echo hi-auto",…}}]`，`"result":"The command didn't print hi-auto. The Bash tool returned this error instead, verbatim: railse test …"`。
