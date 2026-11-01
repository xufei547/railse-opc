# Show HN：标题、正文、作者首评

发帖人：作者本人的 HN 账号 `xufei547`。时间：2026-11-12（周三）北京时间 20:00–22:00。发完 2 分钟内发作者首评。

## 标题（≤ 80 个字符，按字符数不是字节数；发帖前三选一，推荐第二条最稳）

```text
Show HN: Railse – approve Claude Code/Codex/Gemini from your phone, rules first
Show HN: Railse – a self-hosted approval inbox for your coding agents
Show HN: Railse – rules auto-allow boring agent prompts, the phone gets the rest
```

## 正文（Show HN 正文只放链接与两三句）

```text
Railse is a self-hosted approval inbox for people running Claude Code, Codex CLI and Gemini CLI at the same time. A rules engine auto-allows low-risk tool calls (reads, in-project edits); anything it can't decide is pushed to your phone, where you approve, deny, or answer Claude Code's multiple-choice questions. One Go binary plus a PWA; your own laptop can be the hub via Tailscale Funnel.

Repo: https://github.com/xufei547/railse-opc
```

## 作者首评

```text
Author here. I run several coding agents at once for a one-person company, and the thing that actually broke my days wasn't the agents' mistakes. It was not knowing which terminal was waiting for me. "Allow this?" prompts sat there for an hour while I was out.

What Railse does:

- Hooks into Claude Code, Codex CLI and Gemini CLI's permission events. No forks, no patched binaries.
- A policy engine sorts each tool call into seven risk levels. Reads and in-project edits are allowed automatically. Pushes, writes outside the project and destructive commands are never auto-allowed, and "remember this" can't override that.
- Everything undecidable goes to a small self-hosted hub and from there to your phone as a web push. You approve or deny there. For Claude Code's AskUserQuestion you pick an option or type a line and the answer goes straight back; the terminal never asks.

Honest limits: macOS and Linux only. On iPhone the PWA must be added to the Home Screen for push to work. Codex and Gemini questions can only be shown, not answered (their hooks can't take an answer). The project started in Chinese; the English UI is new as of 0.2, so tell me where it reads oddly.

Why not Claude Code's Remote Control: it's great if Claude Code is your only agent and you're fine with the relay through Anthropic's servers. I wanted one inbox for three CLIs, a rules layer, and my own hub. There's a comparison table in the README.

Apache 2.0. Happy to answer anything about the hook mechanics or the policy engine.
```

校对清单：每个事实对照 `facts.md`；没有一句超过 25 个词；不出现感叹号。
