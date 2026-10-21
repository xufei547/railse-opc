# 钩子输入样本

每个文件都是对应 CLI 真实传给钩子 stdin 的 JSON，录制于 2026-10-21，方法与结论见 `docs/superpowers/specs/2026-10-21-hook-spike-findings.md`。

- 目录：`<cli>/<CLI 版本号>/`；文件名为事件名，同一事件的变体用点号加后缀，例如 `Notification.idle_prompt.json`。
- 家目录路径保留真实用户名；值里不含 token 或密钥，录制时另做过一遍脱敏扫描。
- 所有样本来自临时项目 `spike/<cli>/`，触发命令是 `touch spike-proof.txt`。Claude 的 `PermissionRequest.json` 是 `git push origin main`。

| 目录 | 来源 | 备注 |
|---|---|---|
| `claude/2.1.280/` | 真实模型；`PermissionRequest.json`、`PreToolUse.json`、`SessionEnd.json` 来自 `claude -p`，其余来自交互会话 | `PermissionRequest.interactive.json` 的 `permission_suggestions` 是 `addDirectories`，因为工作目录路径含 `.claude/` |
| `codex/0.145.0/` | 真实模型 `gpt-5.6-sol`，交互会话 | `notify.agent-turn-complete.json` 是 `notify` 程序收到的最后一个命令行参数，不是 stdin |
| `gemini/0.28.0/` | 真实 CLI，模型为本地桩服务，`--approval-mode yolo` 的 headless 运行 | 本机登录态不可用 |
| `gemini/0.65.0/` | 真实 CLI，模型为本地桩服务；`BeforeTool.json` 与 `Notification.ToolPermission.json` 来自交互会话，其余来自 headless yolo | `transcript_path` 指向实验用的临时 `GEMINI_CLI_HOME` |
