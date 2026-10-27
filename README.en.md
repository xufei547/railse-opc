<p align="center"><img src="assets/logo.svg" width="112" alt="Railse"></p>

# Railse OPC

[中文](README.md)

Railse OPC uses a rule-based policy engine to approve or deny tool calls from Claude Code, Codex CLI and Gemini CLI, and sends the ones the rules can't decide to an inbox on your phone (for Gemini CLI they go back to the terminal). It's for one person running several AI coding agents at the same time.

OPC stands for "one-person company". The phone web app, the CLI messages and the docs are in Chinese only for now.

- **Rules first, phone second.** The built-in rules only allow read-only operations and edits inside the project, so things like `git push` and `curl` go to your phone.
- **Failures fall back to the terminal.** If the daemon isn't running or the hub can't be reached within 30 seconds, the request goes back to the CLI's own permission prompt, so a failure never blocks the agent or approves anything.
- **Decisions can become rules.** Turn on 记住 (remember) when you approve or deny, and the decision is saved as a rule for that project or for all projects; outward-facing and destructive actions can never be saved as allowed.
- **Self-hosted hub, hashes only.** Tokens and passwords in commands are replaced with `<redacted>` before they leave your machine, and the hub stores machine secrets and login sessions only as hashes.

## How it works

```
Claude Code / Codex CLI / Gemini CLI
   │ permission hook
   ▼
railse agent (policy engine) ──rule matched──▶ allow / deny
   │ undecided
   ▼
railse hub ──Web Push──▶ phone ──allow / deny──▶ hub ▶ agent ▶ hook
```

Each machine you write code on runs a daemon, `railse agent`. The CLIs' permission hooks pass each request to it over a local Unix socket, and anything the rules can't decide goes to the hub over a WebSocket that the daemon opens outbound. The hub can only answer allow or deny; it has no way to send commands to your machines. It also serves the phone web app (a PWA), so there is nothing else to deploy.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | sh
```

Supported: macOS on Apple Silicon, Linux on amd64 and arm64. The script downloads the release from GitHub, checks its sha256, and installs to `/usr/local/bin` if that's writable, otherwise to `~/.local/bin`. It never calls sudo. Run it again to upgrade.

`go install github.com/xufei547/railse-opc/cmd/railse@latest` also works, but that binary doesn't include the web app, so it's only good for work machines.

## Getting started

1. **Hub machine.** Point a domain at it and open ports 443 and 80 to the internet. On macOS:
   ```sh
   railse hub password    # reads the web login password from stdin, at least 8 characters
   railse hub run --acme-domain hub.example.com
   ```
   The hub runs in the foreground and gets its own Let's Encrypt certificate. On Linux a normal user can't bind 443/80, so run it as a systemd service or behind a reverse proxy; see [docs/setup.md](docs/setup.md).
2. **Each work machine.** On the hub machine, run `railse hub token` to get an enrollment code (valid for 10 minutes, one per machine). Then on the work machine:
   ```sh
   railse setup
   ```
   It asks for the hub URL, the code and a machine name (defaults to the hostname). To pass everything up front: `railse setup all --hub https://hub.example.com --token <code>`. It enrolls the machine, installs user-level hooks for all three CLIs, sets up autostart, runs `railse doctor`, and prints the URL to open on your phone.
3. **Phone.** Open that URL and log in with the password from step 1. On iPhone, open it in Safari, tap Share, then Add to Home Screen, open it from the home screen icon, and tap 开启通知 (enable notifications) under 设置 (settings).

## Running the hub on Fly.io

`deploy/fly/` has a Dockerfile and a `fly.toml`. Fly terminates TLS and the hub's data lives on a 1 GB volume. Before deploying, edit `fly.toml`: set `app` to your app name, `RAILSE_PUBLIC_URL` to `https://<app>.fly.dev` (push subscriptions are tied to this URL, so pick it once), and `primary_region` to the region you create the volume in. Leave `auto_stop_machines` set to `"off"`, because daemons and phones keep long-lived connections to the hub. The commands (create the app and volume, set the initial password secret, deploy, get an enrollment code) are in the "部署到 Fly.io" section of [docs/hub.md](docs/hub.md).

## Docs

- [docs/setup.md](docs/setup.md): install, hub deployment (Linux service, reverse proxy), enrolling machines, autostart, `railse doctor` checks, upgrades
- [docs/hub.md](docs/hub.md): hub API, push notifications, security, Fly.io, troubleshooting
- [docs/web.md](docs/web.md): the phone web app
- [docs/agent-local-loop.md](docs/agent-local-loop.md): daemon, hooks, rules, audit log
- [Design doc](docs/superpowers/specs/2026-10-21-railse-opc-design.md)
- [CONTRIBUTING.md](CONTRIBUTING.md) and [SECURITY.md](SECURITY.md)

## Status and limitations

- This is v0.1.1, the first version, and the author is dogfooding it.
- The phone web app and CLI output are Chinese only.
- Gemini CLI hooks can deny but can't approve. Rule denials still apply; everything else goes back to Gemini's own prompt and never reaches the phone. Needs Gemini CLI 0.65.0 or later.
- Codex hooks only fire in interactive sessions. `codex exec` doesn't trigger them.
- The hub is one process with one SQLite database and can't run as multiple instances.
- On iPhone, push notifications only work after you add the page to the Home Screen and open it from there (iOS 16.4 or later).
- Push subscriptions are tied to the hub's domain. Change the domain and every phone has to enable notifications again.

## Development

```sh
make test    # gofmt, golangci-lint, go vet, go test -race, web checks
make e2e     # end-to-end with a real hub, daemon and hooks (Playwright)
```

[CONTRIBUTING.md](CONTRIBUTING.md) covers the required tools and how to send changes.

## License

[Apache License 2.0](LICENSE)
