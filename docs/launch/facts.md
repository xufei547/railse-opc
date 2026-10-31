# 发布素材事实核对

发布前一天再核对一遍"核对日期"早于 11-11 的行。表里"结论"是成立 / 改写 / 删除；"改写"的行写出允许使用的说法。

| 论断 | 出处 | 核对日期 | 结论 |
|---|---|---|---|
| Claude Code 的 Remote Control 只覆盖 Claude Code 一个 CLI | https://code.claude.com/docs/en/remote-control ："Remote Control is a Claude Code feature." 全文未提 Codex、Gemini | 2026-11-01 | 成立 |
| Remote Control 经 Anthropic 的服务器中转，会话记录存在 Anthropic | 同上："the server routes messages between the web or mobile client and your local session"；"the session transcript … is stored on Anthropic servers"；"Execution and filesystem access stay on your machine" | 2026-11-01 | 成立。说法：relays through Anthropic's servers; the transcript is stored there; execution stays local |
| Remote Control 需要 Anthropic 账号与订阅，API key 不行 | 同上："available on Pro, Max, Ultra, Team, and Enterprise plans. API keys are not supported." | 2026-11-01 | 成立 |
| Remote Control 要求电脑开着、claude 进程一直跑 | 同上："your computer has to stay on and the `claude` process has to keep running." | 2026-11-01 | 成立（Railse 同样要求守护进程在跑，表里别拿这条当差异） |
| Remote Control 能在手机上批准权限、答问题 | 同上：推送设置 "Push when actions required" 覆盖 "permission prompts and questions" | 2026-11-01 | 成立。表里 Questions on the phone 两边都写 Yes |
| Remote Control 没有按风险自动放行的规则层 | 同上：全文未提；最接近的是 `--permission-mode <mode>`。Claude Code 自身有 settings 里的 allow/deny 列表 | 2026-11-01 | 改写：不能说"没有规则"。说法：Rules — Claude Code's own allow/deny lists; nothing risk-based, nothing you can teach from the phone, nothing shared across CLIs |
| Codex CLI 没有官方的手机审批通道 | https://learn.chatgpt.com/codex/mobile ："Review commands and requested actions before Codex continues working on your connected computer"，按钮 Approve / Always approve / Deny；配对经 ChatGPT desktop app（Mac 或 Windows）扫码 | 2026-11-01 | 改写：Codex 有官方手机审批，但走 ChatGPT 桌面应用加 ChatGPT 手机应用；单独的 Codex CLI 没有。说法：Codex's own phone approvals go through the ChatGPT desktop and mobile apps; the standalone CLI has none |
| Gemini CLI 没有官方的手机审批通道 | 搜索只见第三方（Pushary、Happier），Google 文档未见 | 2026-11-01 | 改写：说 "nothing official that I know of"，不说 "none" |
| Claude Code 的 PreToolUse 钩子可以用 updatedInput.answers 代答 AskUserQuestion | v0.1.5 真机验证（Claude Code 2.1.280），记录在 `docs/superpowers/plans/2026-10-31-railse-opc-v0.1.5-answer-questions.md` | 2026-11-01 | 成立 |
| iOS 的 Web Push 要求网页加到主屏幕 | https://webkit.org/blog/13878/web-push-for-web-apps-on-ios-and-ipados/ ：iOS/iPadOS 16.4 起 Home Screen web apps 支持 Web Push | 2026-11-01 | 成立 |
| Railse 的 hub 对审批只能回答批准或拒绝，对提问只能给选项文字或一句话 | 代码：`internal/hub/api.go` 的 decide 与 answer 接口；`internal/agent` 的 `handleAsk` 只把答案填进 `updatedInput.answers` | 2026-11-01 | 成立 |
| Railse 守护进程只向外连 hub，本机不监听端口 | 代码：`internal/agent/hubclient.go`（WebSocket 外连）；本机只有 Unix socket `~/.railse/agent.sock` | 2026-11-01 | 成立 |
| 规则永远不能自动放行 outward / destructive | 代码：`internal/policy` 的内建规则与"记住"的限制；README "它怎么做" 第 6 条 | 2026-11-01 | 成立 |

## 对比表允许的说法（README.en 与 Show HN 首评共用）

| | Claude Code Remote Control | Railse |
|---|---|---|
| CLIs covered | Claude Code | Claude Code, Codex CLI, Gemini CLI |
| Where it runs | Relays through Anthropic's servers; the transcript is stored there; execution stays local | Your own hub (laptop with Tailscale Funnel, a VPS, or Fly); nothing leaves your machines except what you choose to self-host |
| Rules | Claude Code's own allow/deny lists; nothing risk-based, nothing taught from the phone | Seven risk levels; low-risk calls auto-allowed; "remember" from the phone; destructive and outward actions never auto-allowed |
| Questions on the phone | Yes | Yes (Claude Code's `AskUserQuestion`; Codex and Gemini questions are shown, not answered) |
| Account needed | Pro, Max, Ultra, Team or Enterprise plan; API keys not supported | None beyond your own hub password |
