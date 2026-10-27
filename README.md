<p align="center"><img src="assets/logo.svg" width="112" alt="Railse"></p>

# Railse 一人公司（Railse OPC）

[English](README.en.md)

一个人同时开几个 AI 编程 agent 时用的审批工具：Claude Code、Codex CLI、Gemini CLI 的工具调用先由策略引擎按规则批准或拒绝，定不了的进手机收件箱，在手机上批准或拒绝（Gemini CLI 定不了的交回终端）。

- **规则先定，定不了才问手机**：内置规则只放行只读和项目内改动，`git push`、`curl` 这类才推到手机。
- **出故障不卡 agent，也不误批**：守护进程不在、hub 连不上超过 30 秒，请求交回终端，由 CLI 自己审批。
- **可以「记住」**：批准或拒绝时打开「记住」，存成规则，下次同类请求自动处理；对外和不可逆的操作记不成放行。
- **hub 自托管，只存哈希**：命令里的 token、密码在本机就换成 `<redacted>`，hub 里的机器密钥和登录会话只存哈希。

## 怎么工作

```
Claude Code / Codex CLI / Gemini CLI
   │ 权限钩子
   ▼
railse agent（策略引擎）──规则定了──▶ 批准 / 拒绝
   │ 定不了
   ▼
railse hub ──Web Push──▶ 手机 ──批准 / 拒绝──▶ hub ▶ agent ▶ 钩子
```

每台写代码的机器跑一个守护进程，规则定不了的经 WebSocket 送到 hub，守护进程只向外连。手机网页由 hub 托管，不用另外部署。

## 安装

```sh
curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | sh
```

支持 macOS（Apple Silicon）和 Linux（amd64、arm64）。不调用 sudo，`/usr/local/bin` 可写就装那里，否则装 `~/.local/bin`；升级就是重跑一遍。也可以 `go install github.com/xufei547/railse-opc/cmd/railse@latest`，但不带手机网页，只适合工作机。

## 三步上手

1. **hub 机器**：域名已解析到这台机器，443、80 对公网开放。macOS 上：
   ```sh
   railse hub password    # 设网页登录密码
   railse hub run --acme-domain hub.example.com
   ```
   Linux 上要装成系统服务或用反向代理，见 [docs/setup.md](docs/setup.md)。
2. **每台工作机**：在 hub 机器上跑 `railse hub token` 拿接入码（10 分钟有效，一台一个），再在工作机上：
   ```sh
   railse setup
   ```
   按提问填 hub 地址、接入码和本机名字，或一次给全：`railse setup all --hub https://hub.example.com --token <接入码>`。它接入 hub、装钩子、设开机自启、跑 `railse doctor`，最后打印手机要打开的地址。
3. **手机**：打开这个地址，用第 1 步的密码登录。iPhone 用 Safari 点「分享」→「添加到主屏幕」，从图标打开，在「设置」里「开启通知」。

## 部署到 Fly.io

`deploy/fly/` 里有现成配置，先把 `fly.toml` 的 `app`、`RAILSE_PUBLIC_URL`（`https://<应用名>.fly.dev`）换成你的应用名，`primary_region` 换成建卷的区域。命令见 [docs/hub.md](docs/hub.md) 的「部署到 Fly.io」。

## 文档

- [docs/setup.md](docs/setup.md)：安装、部署、接入、开机自启、体检、升级
- [docs/hub.md](docs/hub.md)：接口、推送、安全、排错
- [docs/web.md](docs/web.md)：手机网页
- [docs/agent-local-loop.md](docs/agent-local-loop.md)：守护进程、钩子、规则、审计
- [设计文档](docs/superpowers/specs/2026-10-21-railse-opc-design.md)
- [CONTRIBUTING.md](CONTRIBUTING.md)、[SECURITY.md](SECURITY.md)：参与开发、报告安全问题

## 现状与限制

- v0.1.1，第一版，作者自用中。
- Gemini CLI 的钩子只能拒绝不能放行，其余都交回终端，不进手机；要 0.65.0 以上。
- Codex 的钩子只在交互会话里生效，`codex exec` 不触发。
- hub 单机单库（SQLite），不能多实例部署。
- iPhone 要先加到主屏幕、从图标打开才能收推送。
- 推送订阅绑在 hub 域名上，换域名要每台手机重新开启通知。

## 开发

测试用 `make test`，端到端用 `make e2e`，工具和提交方式见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 许可证

[Apache License 2.0](LICENSE)
