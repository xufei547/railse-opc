# 清单与文档入口（挂到别人的列表里）

调研日期 2026-11-01。三份值得挂的清单，各自的规矩不同。

## 1. hesreallyhim/awesome-claude-code（5.7 万 star，最值钱）

- 规矩：**只能由人本人用网页上的 issue 表单提交**，明确禁止 PR 与 `gh` CLI，代理代提会被限制。
- 门槛：仓库建立满 14 天且持续有提交，或 100 star。仓库 2026-10-26 建立 → **11 月 9 日之后**再提，正好在 Show HN 前。
- 一次只能提一个资源；描述要写"它是什么"，不能像广告，一行。
- 表单地址：https://github.com/hesreallyhim/awesome-claude-code/issues/new?template=recommend-resource.yml
- 建议分类：Remote Control, Notifications & Voice I/O
- 填表用的英文描述（一行）：

  > Railse – Self-hosted approval inbox for Claude Code, Codex CLI and Gemini CLI: a policy engine rates each tool call by risk and auto-allows the low-risk ones; the rest is pushed to your phone, where you approve, deny, or answer Claude Code's AskUserQuestion prompts. Single Go binary plus a PWA; your laptop can be the hub via Tailscale Funnel.

- 作者栏：xufei547。许可证由机器人自动识别（Apache-2.0）。

## 2. RoggeOhta/awesome-codex-cli（561 star）

- 规矩：fork 后改 README.md 提 PR；条目必须直接和 Codex 相关、活跃维护、一句话说清价值、带 star 徽章。
- 放在 **Remote Access** 一节末尾（那一节都是从手机或 IM 控制 CLI 的工具）。
- 条目：

  ```markdown
  - [xufei547/railse-opc](https://github.com/xufei547/railse-opc) - Self-hosted approval inbox for Codex, Claude Code and Gemini CLI: a rules engine auto-allows low-risk tool calls, the rest is pushed to your phone to approve or deny. ![GitHub stars](https://img.shields.io/github/stars/xufei547/railse-opc?style=flat-square)
  ```

## 3. Piebald-AI/awesome-gemini-cli（573 star）

- 规矩：fork 改 README 提 PR，用他们的 PR 模板；条目加在该节**末尾**；格式 `- [Name](URL) - Description`。模板里有一项"我已 star 这个仓库"，维护者要求代理在开 PR 前问用户要不要 star。
- 放在 **Development Tools & Utilities** 一节末尾。
- 条目（Gemini 的钩子只能拒绝不能放行，描述里如实写）：

  ```markdown
  - [Railse](https://github.com/xufei547/railse-opc) - Self-hosted approval inbox for Gemini CLI, Claude Code and Codex: hooks route every tool call through risk-based rules; for Gemini CLI that means deny rules plus phone notifications for questions and completion, with approvals from the phone for Claude Code and Codex.
  ```

## 跳过的

- ithiria894/awesome-claude-code-hooks：29 star，今年 3 月后没更新。
- ahoo-wang/awesome-codex-cli：RoggeOhta 那份的零 star 复制。

## 之后

- 挂上后在 README 加对应的 "Mentioned in Awesome …" 徽章。
- Claude Code 官方文档的 hooks 页没有第三方列表，不用提。
