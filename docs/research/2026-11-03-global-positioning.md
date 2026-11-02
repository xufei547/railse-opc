# Railse 面向全球用户的定位判断（2026-11-03）

> 背景：v0.2.3 发布后，作者觉得 Railse 还远不是"一人公司"，怀疑是否有更好的路没走。这份备忘回答三个问题：全球市场上这块地今天被谁占着、Railse 真正独有的是什么、下一层该往哪里建。结论在最后一节。所有外部数字都来自二手来源，星数和价格未逐一核实，用作量级判断。

## 1 市场上现在有四条线

### 1.1 托管的"AI 员工" SaaS
Lindy、Relevance AI、11x 一类。无代码、按席位或按积分收费、面向销售/客服/行政职能。Relevance AI 2026 年已转为只卖企业版。这条线资金雄厚、和开发者工具没有交集，Railse 不该进去。

### 1.2 自托管的个人 agent：OpenClaw
2026-01-29 公开，约 37–39 万 star，是这一代增长最快的开源项目。cron 调度、50 多个 skills、手机上聊天驱动、有 coding-agent skill 把仓库活交给 Claude Code / Codex / OpenCode 在 worktree 里跑。市面上"一个人用 agent 跑公司"的教程，大半是在讲 OpenClaw 加一堆 cron。

它的另一面是安全：CVE-2026-25253（WebSocket 劫持，8.8）、CVE-2026-32025（ClawJacked）、据报道 4.2 万个实例裸露在公网、Cisco 发现第三方 skill 偷数据、邮件注入让 agent 转走私钥。OpenClaw 自己的文档把系统提示词里的规则称为"建议而非强制"。每一篇加固指南的结论都一样：**把工具调用拿到模型外面按策略校验，高风险动作必须有人确认**。这正是 Railse 做的事，而且没有人在 OpenClaw 生态里做成产品。

### 1.3 "agent 组织架构 / 零人公司"编排器
- **Paperclip**（paperclip.ing，Paperclip Labs Inc，MIT）：Node 服务 + React 界面，自带 agent 接入，有组织架构和汇报线、每个 agent 的月度预算（超了就停）、审计日志、治理审批、暂停/接管/重派。口号是 "orchestration for zero-human companies"。
- **Alook**（alookai/alook）：把本机的 Claude Code / Codex / Cursor / OpenCode / Pi 会话编成组织架构，角色 dev/ops/research，每个 agent 有邮箱、房间、频道，托管版加自托管版。
- **OneManCompany**（约 268 star）、**OpenOPC**（HKUDS 研究项目）：同一方向，更早期。

也就是说，"一个人加一群有角色的 agent 员工"这个画面，已经至少有两家在做产品，其中 Paperclip 连预算和审批都有了。

### 1.4 编码 agent 的驾驶舱
- **Anthropic 官方 Agent View**（2026-05-11，`claude agents`）：本机所有 Claude Code 后台会话一张表，分 Working / Needs input / Ready for review / Completed，可以回话。只限 Claude、只限本机、只在终端。
- **herdr 0.9**：跨机器 TUI，下一步是跨机器 agent 协作，已进 YC。
- **Agent Cockpit**（MIT，hook 拦截工具调用、2D 办公室地图）、**agentglass**（Claude hooks + Codex OpenTelemetry，雷达图）、**CloudCLI / Claude Code UI**、**agent-dashboard**（tmux + PWA 批准权限）。

"看见所有会话谁在等我"这件事，官方给 Claude 做了内置版，开源界有四五个变体。

## 2 对照 Railse

| 能力 | Railse 今天 | 市场上谁有 |
|---|---|---|
| 跨 CLI 的手机审批收件箱 | 有（Claude / Codex / Gemini） | Happy、cc-connect、各家官方 Remote Control（单 CLI） |
| 工具调用级策略引擎（7 个风险等级、用户规则优先、fail-closed） | 有 | Pushary、Greenlight 有简单规则层；OpenClaw 没有；Paperclip 在任务级而非工具调用级 |
| 限时放行、带作用域的 remember | 有 | 没人有 |
| 自托管、单二进制、不经厂商中继、API key 用户可用 | 有 | OpenClaw 自托管但没策略层；Paperclip/Alook 自托管但是编排器 |
| 所有会话的总台 | 没有 | Agent View（Claude）、herdr、Agent Cockpit 等 |
| 角色 / 组织架构 / 派工 | 没有 | Paperclip、Alook |
| 预算与成本 | 没有 | Paperclip |
| 定时任务 | 没有 | OpenClaw |

两条结论：
1. 我们打算往上建的两层，"总台"和"角色编排"，都已经有人占着，其中一个还是官方内置。
2. 我们已经建好的那层，"工具调用级的策略 + 手机上的人"，在整个自托管 agent 市场里是空的，而且 OpenClaw 的安全事故正在替我们教育市场。

## 3 三个可选方向

**A. 继续只做审批收件箱。** 厂商下半年都做了单 CLI 手机审批，这层会被持续蚕食，剩下跨 CLI、自托管、API key 用户三条缝。能活，长不大。

**B. 进组织架构编排。** 和 Paperclip、Alook 正面竞争，对方有融资、有先发、已经有预算和审批。我们从零开始做编排器，没有优势，而且会把策略引擎稀释成一个功能项。

**C. 做"自托管 agent 的治理层"。** 不管 agent 是谁起的，Claude Code、Codex、OpenClaw 的 cron、Paperclip 派出去的员工，它们的每一次危险动作都经过 Railse 的策略，决定不了的到手机上找那个唯一的人。Railse 不派活，Railse 划边界。"一人公司"在这个叙事里的意思是：公司里只有一个人，所有 agent 做的事最终都要对这个人负责，Railse 是这个人的合规官和值班电话。

推荐 C。理由：它建立在我们唯一独有的东西上；它不和任何一家编排器冲突，反而是它们的"works with"；市场正在因为 OpenClaw 的事故主动寻找它；一个人维护得动。

## 4 C 方向下，"角色"是什么

作者 11-03 认定需要"团队角色"（agent 角色，不是人的角色）。在 C 里角色仍然成立，但含义变了：

- 角色不是"谁去干活"，而是**一份权限边界**：允许的项目目录、允许的风险等级、哪些工具永远不行、限时放行的上限、月度花费上限（能拿到的话）。
- 角色绑定到会话的方式：环境变量（`RAILSE_ROLE=content claude`）、项目目录映射、或由 Paperclip / Alook / OpenClaw 启动时传进来。谁起会话不重要。
- 总台上按角色聚合"谁在等我、今天各花了多少、刚做了什么"。这个总台是策略视角的总台（谁越界了、谁在放行期），不是运行时视角的（输出是什么），所以不和 Agent View、herdr 重叠。
- 不做：角色之间派活、多 agent 协作、工作流引擎、人的多账号权限。

## 5 对现有计划的影响

- 11-12 的 Show HN 照常，讲"跨 CLI 审批收件箱"，一句话带出"也是自托管 agent 的策略层"。不要把 C 的全貌提前说出去。
- v0.3 的第一件事不是总台，而是**接入 OpenClaw 的工具调用**（需要先做一次 spike 确认它有没有可拦截的 hook / plugin 点；没有的话退而接它的 gateway 审计流）。接上 OpenClaw，等于接上最大的那批自托管用户，而且是他们正在被教育要买的东西。
- v0.3 的第二件事是角色作为权限边界 + 按角色的总台。
- Paperclip 的"治理审批"和 Alook 的 org chart 都可能成为集成对象，而不是对手：它们派活，我们把关。各写一篇 works-with。
- 成本与预算：Paperclip 已有，我们只在能从 hook 或 CLI 拿到真实用量时再做，不自己估算。

## 6 还没核实、会影响判断的事
1. OpenClaw 有没有稳定的工具调用拦截点（hook / plugin / gateway 中间件）。这决定 C 的第一步能不能走。
2. Paperclip 和 Alook 的真实活跃度（star、提交频率、有没有真用户）。
3. Anthropic Agent View 会不会扩到跨机器和手机。会的话 Claude 侧的总台价值更低，策略层价值不变。
4. 作者自己的"员工"清单（角色名、机器、CLI、目录）。C 方向下仍然需要它来定第一批角色模板。

## 来源
- https://blog.elest.io/openclaw-explained-how-the-fastest-growing-open-source-project-became-a-self-hosted-ai-agent-for-everyone/
- https://railway.com/deploy/openclaw--self-hosted-openclaw
- https://www.firecrawl.dev/blog/secure-openclaw
- https://www.techradar.com/pro/here-are-the-openclaw-security-risks-you-should-know-about
- https://wz-it.com/en/blog/openclaw-secure-deployment-security-hardening-guide/
- https://vallettasoftware.com/blog/post/openclaw-security-2026-best-practices-risks-hardening-guide
- https://agentfactory.panaversity.org/docs/openclaw-with-coding-agents
- https://paperclip.ing/ 、https://paperclip.ing/docs 、https://betterstack.com/community/guides/ai/paperclip-multi-agent/
- https://skillsllm.com/skill/alook 、https://www.trendingaitools.com/ai-tools/alook/
- https://www.sourcepulse.org/projects/30037525 (OneManCompany)
- https://claude.com/blog/agent-view-in-claude-code 、https://cobusgreyling.substack.com/p/claude-code-agent-view
- https://github.com/herdrdev/herdr/releases 、https://ecosistemastartup.com/herdr-0-9-une-tus-agentes-ia-en-varias-maquinas-desde-un-tui/
- https://www.producthunt.com/products/agent-cockpit 、https://github.com/SirAllap/agentglass/releases/tag/v0.1.0
- https://pinggy.io/blog/remotely_manage_claude_code_from_phone/
- https://www.eesel.ai/blog/lindy-vs-relevance-ai 、https://www.11x.ai/guides/relevance-ai-pricing
- https://fortune.com/2026/05/18/solo-founders-ai-automation-entire-teams-entrepreneurs/
- https://aibusiness.vc/solo/one-person-company-ai-agents-limits-2026
