# Railse 一人公司（Railse OPC）

Railse 一人公司（Railse OPC）用策略引擎自动审批 AI 编程 agent（Claude Code、Codex CLI、Gemini CLI）的工具调用，定不了的进手机收件箱，在手机上批准或拒绝。

状态：第一版自用中，未发布正式版。

## 怎么工作

1. **钩子**：`railse setup` 把 `railse hook <cli>` 装进三个 CLI 的权限钩子，每次工具调用前把请求经本机 Unix socket 交给守护进程。
2. **守护进程与策略引擎**：每台工作机跑一个 `railse agent`，按 `rules.yaml` 给请求定风险档并放行或拒绝，每一步写本地审计；定不了的送 hub。
3. **hub 与手机网页**：`railse hub` 自托管在一台有公网域名的机器上，同源托管手机网页（PWA，可收 Web Push）；你在手机上做的决定经 hub 回到守护进程，再由钩子答复 CLI。

```
Claude Code / Codex CLI / Gemini CLI
        │ 权限钩子 railse hook <cli>
        ▼
railse agent（守护进程 + 策略引擎）──规则定了──▶ 放行 / 拒绝，答复钩子
        │ 定不了（WebSocket，守护进程只向外连）
        ▼
railse hub（自托管）──Web Push──▶ 手机网页收件箱 ──批准 / 拒绝──▶ hub ▶ 守护进程 ▶ 钩子
```

Railse 自身出任何故障都不会卡住 agent，也不会误批：守护进程不在、hub 连不上（最多等 30 秒）时请求交回终端，由 CLI 自己的审批处理。

## 安装

```sh
curl -fsSL https://raw.githubusercontent.com/railse/railse-opc/main/install.sh | sh
```

支持 macOS（Apple Silicon）与 Linux（amd64、arm64）。脚本从 GitHub Releases 下载对应的包，按同一发布的 `checksums.txt` 校验 sha256，校验不过不安装；不调用 sudo：`/usr/local/bin` 可写就装那里，否则装到 `~/.local/bin`（不在 PATH 里时会提示）。指定版本或目录：`curl -fsSL … | RAILSE_VERSION=0.1.0 RAILSE_INSTALL_DIR=~/bin sh`。升级就是重跑一遍。

或者用 Go 自己编译：`go install github.com/railse/railse-opc/cmd/railse@latest`。这样装出的二进制不带手机网页（网页路径回 503“网页未构建”），只适合工作机；跑 hub 的机器请用上面的安装脚本，或在仓库里 `make build`。

## 三步上手

1. **hub 机器**（域名已解析到这台机器，443 与 80 对公网开放）：
   ```sh
   railse hub password    # 从标准输入读一行设为网页登录密码
   railse hub token       # 打印一次性接入码，默认 10 分钟有效
   railse hub run --acme-domain hub.example.com --acme-email me@example.com
   ```
   Linux 上要开机自启时改用系统服务：`sudo railse setup autostart --hub --system -- --acme-domain hub.example.com --acme-email me@example.com`（数据目录是 `/var/lib/railse-hub`，`password`、`token` 要加 `--dir /var/lib/railse-hub`）。
2. **每台工作机**：在项目目录里跑 `railse setup all --hub https://hub.example.com --token <接入码>`，一次做完接入 hub、装三个 CLI 的钩子、开机自启与体检（Claude、Gemini 的钩子是项目级的，别的项目再跑 `railse setup hooks --project <目录>`）。之后随时可以 `railse doctor` 检查。
3. **手机**：打开 `https://hub.example.com/`，用第 1 步的密码登录；iPhone 上加到主屏幕后在设置里开启通知。

## 文档

- `docs/setup.md`：安装、接入与开机自启的完整步骤
- [docs/agent-local-loop.md](docs/agent-local-loop.md)：本机闭环（守护进程、钩子、规则、审计）
- [docs/hub.md](docs/hub.md)：hub 的部署、接口、推送、安全与故障排查
- [docs/web.md](docs/web.md)：手机网页（PWA）
- [docs/superpowers/specs/2026-10-21-railse-opc-design.md](docs/superpowers/specs/2026-10-21-railse-opc-design.md)：第一版设计

## 开发

需要 Go（版本见 `go.mod`）与 Node 20。

```sh
make test    # gofmt、golangci-lint、go vet、go test -race、网页检查
make e2e     # 网页端到端（真实的 hub、守护进程与钩子，Playwright）
make lint    # 只跑 golangci-lint（配置见 .golangci.yml）
make build   # 构建网页并编译带网页的 ./railse
```

## 许可证

[Apache License 2.0](LICENSE)
