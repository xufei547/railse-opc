# Railse OPC v0.2 发布素材包（launch kit）实施计划

> **For agentic workers:** 本计划是素材线，不是代码线。由主会话自己做，或用 superpowers:executing-plans 逐条执行。每条都有明确产物与验收，步骤用 `- [ ]` 勾选。

**Goal:** 11 月 12 日（周三，北京时间 20 到 22 点）以作者本人的账号发 Show HN，同日 X 英文串、r/ClaudeAI、V2EX、即刻、小红书跟上；所有素材发布前一天定稿并核对事实。

**Architecture:** 素材全部进仓库 `docs/launch/`（Show HN 正文、作者首评、预备问答、X 串、Reddit 帖、发布日流程单、事实核对记录）；README.en 在仓库根；英文动图 `assets/demo.en.gif` 由 `make demo` 在英文界面下自动录。素材线与两条代码线并行，只有动图一项依赖网页英文化完成。

**Tech Stack:** Markdown；`scripts/demo.sh` + ffmpeg；发帖用 Claude in Chrome（X）与 Tabbit（V2EX、即刻）；小红书笔记用 build-in-public 技能起草。

## Global Constraints

- 设计见 `docs/superpowers/specs/2026-11-01-railse-opc-v0.2-english-launch-design.md` §4、§5。
- 所有英文素材语气与 README.en 一致：短句、说人话、不吹。中文版同日翻，语气与 README.md 一致。
- 关于 Claude Code Remote Control 的每一句话都要在 `docs/launch/facts.md` 里有出处（官方文档 URL 与引用日期）；拿不准的句子删掉。
- HN 账号由作者本人注册（助手不代注册账号），发帖前账号年龄 ≥ 7 天。
- 诚实写限制：macOS 与 Linux、iPhone 要加到主屏幕、Codex 与 Gemini 的提问只提醒不能答、项目从中文起家刚做英文。

---

### Task 1: 事实核对记录 `docs/launch/facts.md`

**Files:**
- Create: `docs/launch/facts.md`

- [ ] **Step 1: 列出要核对的论断**

写入以下表格骨架，每行一条论断、出处 URL、核对日期、结论（成立/改写/删除）：

```markdown
# 发布素材事实核对

| 论断 | 出处 | 核对日期 | 结论 |
|---|---|---|---|
| Claude Code Remote Control 只覆盖 Claude Code 一个 CLI | | | |
| Remote Control 需要 Anthropic 账号并把会话流到 claude.ai | | | |
| Remote Control 没有"按风险自动放行"的规则层 | | | |
| Codex CLI 没有官方的手机审批通道 | | | |
| Gemini CLI 没有官方的手机审批通道 | | | |
| Claude Code 的 PreToolUse 钩子可以用 updatedInput.answers 代答 AskUserQuestion | 已在 v0.1.5 真机验证（2026-11-01） | 2026-11-01 | 成立 |
| iOS 的 Web Push 要求网页加到主屏幕 | | | |
```

- [ ] **Step 2: 逐条查官方文档并填表**

用浏览器打开 Claude Code 官方文档的 Remote Control 页、钩子页，Codex 与 Gemini CLI 的 README 或文档，把 URL 与结论填进去。任何一条"不成立"或"不确定"的，在 Task 2、4、5 的文案里删掉对应句子。

- [ ] **Step 3: 提交**

```bash
git add docs/launch/facts.md
git commit -m "docs(launch): 发布素材事实核对记录"
```

### Task 2: README.en 首屏重写

**Files:**
- Modify: `README.en.md`（首屏到"它怎么做"之前；新增一节 Why not Claude Code's Remote Control?）

- [ ] **Step 1: 改首屏**

标题下第一段改为：

```markdown
A self-hosted approval inbox for people running Claude Code, Codex and Gemini CLI at once. Rules auto-allow the boring stuff; everything else goes to your phone.

- **Rules first.** Seven risk levels. Reads and in-project edits are allowed automatically; pushes, network and destructive commands are never auto-allowed, no matter what you "remember".
- **Three CLIs, one inbox.** Approvals, Claude Code's multiple-choice questions, "waiting for you" and "finished" from every agent on every machine, in one place.
- **One machine is enough.** `railse setup all --local-hub` runs the hub on your laptop and exposes it with Tailscale Funnel. No cloud account, no vendor lock-in.

![demo](assets/demo.en.gif)

```sh
curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | sh
railse setup all --local-hub
```
```

- [ ] **Step 2: 加 Why not Remote Control 一节**

放在"它怎么做"之后、限制之前：

```markdown
## Why not Claude Code's Remote Control?

Remote Control is great if Claude Code is the only agent you run. Railse is for the rest of us.

| | Claude Code Remote Control | Railse |
|---|---|---|
| CLIs covered | Claude Code | Claude Code, Codex, Gemini CLI |
| Where it runs | Anthropic's servers relay your session | Your own hub (laptop, VPS or Fly) |
| Rules | — | Risk levels, auto-allow, "remember", never-auto-allow for destructive actions |
| Questions on the phone | Yes | Yes (Claude Code's `AskUserQuestion`) |
| Account needed | Anthropic account | None beyond your own hub password |
```

每个单元格的说法必须和 Task 1 的 facts.md 一致；facts.md 里"删除"的行从表里拿掉。

- [ ] **Step 3: 把版本行改为 v0.2.0，限制里加"Chinese-first project, English UI since 0.2"**

- [ ] **Step 4: 自查**

```bash
grep -nE '[一-鿿]' README.en.md && echo "README.en 里还有中文" || echo "OK: 无中文"
```
Expected: `OK: 无中文`（代码块里的中文命令名除外，逐条看）。

- [ ] **Step 5: 提交**

```bash
git add README.en.md
git commit -m "docs: README.en 首屏重写，加 Why not Remote Control 对比"
```

### Task 3: 英文演示动图 `assets/demo.en.gif`（依赖网页与 hub 英文化合并）

**Files:**
- Modify: `scripts/demo.sh`（加语言参数）
- Create: `assets/demo.en.gif`

- [ ] **Step 1: 让 demo.sh 接受 `DEMO_LANG=en`**

在脚本里：启动 hub 与浏览器时带 `RAILSE_LANG=$DEMO_LANG`，Playwright 的 `locale` 按 `DEMO_LANG` 选 `en-US`/`zh-CN`，输出文件名 `assets/demo.gif`（zh）或 `assets/demo.en.gif`（en）。脚本开头：

```sh
DEMO_LANG="${DEMO_LANG:-zh}"
case "$DEMO_LANG" in
  en) OUT=assets/demo.en.gif; LOCALE=en-US ;;
  *)  OUT=assets/demo.gif;    LOCALE=zh-CN ;;
esac
export RAILSE_LANG="$DEMO_LANG"
```

- [ ] **Step 2: 录制**

```bash
DEMO_LANG=en make demo
```
Expected: 生成 `assets/demo.en.gif`，开场是设置页 "Send a test notification"，随后一条审批、一道题；时长 45 到 60 秒；文件 < 3 MB（README 首屏加载）。

- [ ] **Step 3: 人工看一遍**，画面里不得出现中文。

- [ ] **Step 4: 提交**

```bash
git add scripts/demo.sh assets/demo.en.gif
git commit -m "demo: 英文版演示动图（DEMO_LANG=en）"
```

### Task 4: Show HN 正文与作者首评 `docs/launch/show-hn.md`

**Files:**
- Create: `docs/launch/show-hn.md`

- [ ] **Step 1: 标题（≤ 80 字符，三选一，发帖前定）**

```text
Show HN: Railse – approve Claude Code/Codex/Gemini from your phone, rules first
Show HN: Railse – a self-hosted approval inbox for your coding agents
Show HN: Railse – stop babysitting Claude Code: rules auto-allow, your phone gets the rest
```

```bash
awk '{print length($0)": "$0}' <<'T'
Show HN: Railse – approve Claude Code/Codex/Gemini from your phone, rules first
T
```
Expected: 长度 ≤ 80。

- [ ] **Step 2: 正文**（HN 的 Show HN 正文只放链接与两三句）

```text
Railse is a self-hosted approval inbox for people running Claude Code, Codex and Gemini CLI at the same time. A rules engine auto-allows low-risk tool calls (reads, in-project edits); anything it can't decide is pushed to your phone, where you approve, deny, or answer Claude Code's multiple-choice questions. One Go binary + a PWA; your own laptop can be the hub via Tailscale Funnel.

Repo: https://github.com/xufei547/railse-opc
```

- [ ] **Step 3: 作者首评**（发帖后立刻以同一账号发）

```text
Author here. I run several coding agents at once for a one-person company, and the thing that actually broke my days wasn't the agents' mistakes — it was not knowing which terminal was waiting for me. "Allow this?" prompts sat there for an hour while I was away.

What Railse does:
- Hooks into Claude Code, Codex and Gemini CLI's permission events (no forks, no patched binaries).
- A policy engine classifies each tool call into seven risk levels. Reads and in-project edits are allowed automatically. Pushes, network, writes outside the project and destructive commands are never auto-allowed — "remember this" can't override that.
- Everything undecidable goes to a small self-hosted hub and from there to your phone as a web push. You approve/deny there; for Claude Code's AskUserQuestion you pick an option or type a line and the answer goes straight back — the terminal never asks.

Honest limits: macOS and Linux only; on iPhone the PWA must be added to the home screen for push; Codex and Gemini questions can only be shown, not answered (their hooks don't support it); the project started in Chinese and the English UI is new as of 0.2 — tell me where it reads oddly.

Why not Claude Code's Remote Control: it's great if Claude Code is your only agent. I needed one inbox for three CLIs, a rules layer, and no third-party relay. Comparison table in the README.

Apache 2.0. Happy to answer anything about the hook mechanics or the policy engine.
```

- [ ] **Step 4: 校对**：首评里每个事实对照 `docs/launch/facts.md`；没有一句超过 25 个词；提交。

```bash
git add docs/launch/show-hn.md
git commit -m "docs(launch): Show HN 标题、正文与作者首评"
```

### Task 5: 预备问答 `docs/launch/qa.md`

**Files:**
- Create: `docs/launch/qa.md`

- [ ] **Step 1: 写十条，每条问题一行、回答 ≤ 80 词**

```markdown
# 评论区预备问答

**Is it safe to let a web page approve tool calls?** The hub can only say approve/deny (or supply the text of an option you picked). It never sends commands. The daemon only makes outbound connections to the hub; nothing listens on your machine. Approvals for destructive and network actions always need a human tap — rules can't auto-allow them.

**Why not Claude Code's Remote Control?** One CLI vs three, a relay through Anthropic vs your own hub, and no rules layer. If you only run Claude Code and don't mind the relay, Remote Control is fine.

**Windows?** Not yet. The daemon talks to hooks over a Unix socket. WSL works.

**Can I answer Codex / Gemini questions from the phone?** No — their hook APIs can't inject an answer. You get a notification telling you which terminal to go to.

**Where does my data live?** In a SQLite file on your hub. Payloads are redacted on your machine before they leave it (env values, tokens, home paths).

**Do I need Tailscale?** No. `--local-hub` uses Tailscale Funnel because it's the fastest zero-config way to get HTTPS on a laptop. Any reverse proxy, or the hub's built-in ACME on a VPS, works.

**License?** Apache 2.0, with a NOTICE that keeps the name and logo.

**How is this different from `--dangerously-skip-permissions`?** YOLO mode allows everything. Railse allows the boring 80% and makes you tap for the rest — and it's the same tap whether you're at the desk or not.

**Teams?** It works for several machines per hub today, but it's designed around one person. No per-user roles yet.

**Uninstall?** `railse setup autostart --uninstall` stops the daemon (and `--hub --uninstall` the local hub), then remove the `railse hook …` entries from each CLI's settings and delete the binary and `~/.railse`. There's no hook-removal command yet — on the list.
```

- [ ] **Step 2: 校对与提交**（每条对照 facts.md；卸载步骤已按 docs/setup.md 核对：没有删钩子的命令，要手动删条目）

```bash
git add docs/launch/qa.md
git commit -m "docs(launch): 评论区预备问答十条"
```

### Task 6: X 英文串与 Reddit 帖 `docs/launch/social.md`

**Files:**
- Create: `docs/launch/social.md`

- [ ] **Step 1: X 串六条，每条 ≤ 280 字符，第一条带动图**

```text
1/ I run Claude Code, Codex and Gemini CLI at once. The problem was never their mistakes — it was not knowing which terminal was waiting for me. So I built Railse: rules auto-allow the boring stuff, my phone gets the rest. Self-hosted, Apache 2.0. [demo.en.gif]

2/ A policy engine sorts every tool call into 7 risk levels. Reads and in-project edits: allowed. Pushes, network, writes outside the project, destructive commands: never auto-allowed. "Remember this" can't override that.

3/ Everything undecidable goes to a tiny hub and from there to your phone as a web push. Two taps to approve. Claude Code's multiple-choice questions too — pick an option on the phone and the terminal never asks.

4/ One laptop is enough: `railse setup all --local-hub` runs the hub locally and exposes it with Tailscale Funnel. No cloud account.

5/ Why not Claude Code's Remote Control? One CLI vs three, a relay vs your own hub, and no rules layer. Table in the README.

6/ Show HN is live today: [HN link]. Repo: github.com/xufei547/railse-opc — tell me where the English reads oddly, the project started in Chinese.
```

- [ ] **Step 2: Reddit r/ClaudeAI 帖**：标题 `I built a self-hosted "approval inbox" so Claude Code (and Codex/Gemini) stop waiting on me – rules auto-allow, phone gets the rest`，正文复用 Task 4 首评的前三段加限制段，附动图。

- [ ] **Step 3: 提交**

```bash
git add docs/launch/social.md
git commit -m "docs(launch): X 英文串与 Reddit 帖"
```

### Task 7: 国内同日素材（中文）

**Files:**
- Create: `docs/launch/cn.md`

- [ ] **Step 1: V2EX 追加回复**（原帖 https://www.v2ex.com/t/1252390）：三段，v0.2 做了什么（英文界面、答题、本机 hub）、今天上了 Show HN、求反馈。
- [ ] **Step 2: 即刻一条**：150 字内，配英文动图截图，讲"一个人把产品推上 Show HN"。
- [ ] **Step 3: 小红书两篇**：用 build-in-public 技能，主题一"两周把中文产品做成英文上 Show HN 的过程"，主题二"让 Claude Code 在手机上问我选择题"。产物是标题与正文草稿，发布由作者决定。
- [ ] **Step 4: 提交**

```bash
git add docs/launch/cn.md
git commit -m "docs(launch): 国内同日中文素材"
```

### Task 8: 发布日流程单 `docs/launch/runbook.md`

**Files:**
- Create: `docs/launch/runbook.md`

- [ ] **Step 1: 写流程单**

```markdown
# 发布日流程（2026-11-12 周三）

前一天（11-11）
- [ ] v0.2.0 已发布，README.en 指向 demo.en.gif，`railse version` 为 0.2.0
- [ ] facts.md 全部"成立"，Show HN 首评、问答、X 串终稿
- [ ] 作者 HN 账号可登录，年龄 ≥ 7 天
- [ ] 作者 Mac 切回中文（`railse lang zh`，手机设置切回中文）

当天 20:00（北京）
- [ ] 作者发 Show HN（标题、正文按 show-hn.md），拿到链接
- [ ] 2 分钟内发作者首评
- [ ] 20:10 X 串（Claude in Chrome），第 6 条放 HN 链接
- [ ] 20:20 V2EX 回复、即刻（Tabbit）
- [ ] 21:00 r/ClaudeAI

20:00 到 02:00
- [ ] 每 15 分钟看一次 HN 评论与 GitHub issue；每条评论 30 分钟内回（助手起草、作者过目后发）
- [ ] bug 报告当天建 issue 并回复预计修复时间

次日
- [ ] 记录：HN 分数与名次、star、下载、issue 数、评论里的高频问题，写进 docs/launch/retro.md
```

- [ ] **Step 2: 提交**

```bash
git add docs/launch/runbook.md
git commit -m "docs(launch): 发布日流程单"
```

### Task 9: 复盘模板 `docs/launch/retro.md`（11-16 填）

- [ ] 表格：日期、HN 分数/名次、star、各版本下载、issue、评论高频问题、下一版三件事。
- [ ] 提交：`git commit -m "docs(launch): 复盘模板"`。

## 自查

- 覆盖设计 §4 的七样素材：1 README.en（Task 2）、2 动图（Task 3）、3 Show HN（Task 4）、4 问答（Task 5）、5 X 与 Reddit（Task 6）、6 国内（Task 7）、7 流程单（Task 8）；事实核对（Task 1）与复盘（Task 9）来自 §4 与 §5。
- 依赖：Task 3 依赖网页与 hub 英文化合并；其余可在代码线进行时并行。
- 无占位：所有文案给出初稿；需要事实核对的句子都指向 facts.md。
