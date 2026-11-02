# 评论区预备问答（英文，每条 ≤ 80 词）

**Is it safe to let a web page approve tool calls?** The hub can only say approve or deny, or hand back the text of an option you picked. It never sends commands. The daemon only makes outbound connections to the hub; nothing listens on your machine. Destructive and outward actions always need a human tap; rules can't auto-allow them.

**Why not Claude Code's Remote Control?** One CLI vs three, a relay through Anthropic's servers vs your own hub, and no risk-based rules layer. If you only run Claude Code and don't mind the relay, Remote Control is fine.

**Doesn't Codex already do this?** Codex has phone approvals through the ChatGPT desktop and mobile apps. The standalone Codex CLI doesn't, and nothing spans CLIs. Railse hooks all three at the CLI level with one set of rules.

**Windows?** Not yet. The daemon talks to the hooks over a Unix socket. WSL works.

**Can I answer Codex or Gemini questions from the phone?** No. Their hook APIs can't inject an answer. You get a notification telling you which terminal to go to.

**Where does my data live?** In a SQLite file on your hub. Payloads are redacted on your machine before they leave it: env values, tokens, home paths.

**Do I need Tailscale?** No. `--local-hub` uses Tailscale Funnel because it's the fastest zero-config way to get HTTPS on a laptop. Any reverse proxy, or the hub's built-in ACME on a VPS, works.

**License?** Apache 2.0, with a NOTICE that keeps the name and logo.

**How is this different from `--dangerously-skip-permissions`?** YOLO mode allows everything. Railse allows the boring 80% and makes you tap for the rest, and it's the same tap whether you're at the desk or not.

**Too noisy? Can I just let one window run for a while?** Yes, with a time-limited trust (0.2.3). Tick "Trust this for a while" when you approve on the phone, run `railse trust 2h` in a project directory, or start with `RAILSE_TRUST=2h claude` for just that session. Inside the scope Railse stops gating: it's bypassPermissions plus an audit log. It expires on its own, after 24 hours at most, and you can revoke it from the phone or the terminal.

**Teams?** Several machines per hub work today, but it's designed around one person. No per-user roles yet.

**Uninstall?** `railse setup autostart --uninstall` stops the daemon (and `--hub --uninstall` the local hub), then remove the `railse hook …` entries from each CLI's settings and delete the binary and `~/.railse`. There's no hook-removal command yet; it's on the list.
