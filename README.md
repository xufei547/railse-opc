<p align="center"><img src="assets/logo.svg" width="112" alt="Railse"></p>

# Railse 一人公司（Railse OPC）

[English](README.en.md)

一个人同时开几个 AI 编程 agent 时用的审批工具：Claude Code、Codex CLI、Gemini CLI 的工具调用先由策略引擎按规则处理，定不了的进手机收件箱，你在手机上批准或拒绝（Gemini CLI 定不了的交回终端）。

- **先按规则定**：内置规则只放行只读和项目内改动，`git push`、`curl` 这类才推到手机。
- **出故障就交回终端**：守护进程不在、hub 连不上超过 30 秒，CLI 照常弹自己的审批框，最坏就是回到没装 Railse 时的样子。
- **可以「记住」**：批准或拒绝时打开「记住」，存成规则，下次同类请求自动处理；对外和不可逆的操作记不成放行。
- **hub 自己部署**：命令里的 token、密码在本机就换成 `<redacted>` 再上报，hub 里的机器密钥和登录会话只存哈希。

## 怎么工作

```
Claude Code / Codex CLI / Gemini CLI
   │ 权限钩子
   ▼
railse agent（策略引擎）──规则定了──▶ 批准 / 拒绝
   │ 定不了
   ▼
railse hub ──Web Push──▶ 手机 ──批准 / 拒绝──▶ hub ▶ 守护进程 ▶ 钩子
```

每台写代码的机器跑一个守护进程，它主动连 hub（WebSocket），把规则定不了的送过去。手机网页由 hub 托管，不用另外部署。

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
3. **手机**：打开这个地址，用第 1 步的密码登录。iPhone 用 Safari 点「分享」→「添加到主屏幕」，从图标打开，到「设置」里点「开启通知」。

## 部署到 Fly.io

`deploy/fly/` 里有现成配置，部署前改 `fly.toml` 三处：`app` 改成你的应用名；`RAILSE_PUBLIC_URL` 改成 `https://<应用名>.fly.dev`，定下后不要再换；`primary_region` 改成建卷的区域。命令见 [docs/hub.md](docs/hub.md) 的「部署到 Fly.io」。

## 文档

- [docs/setup.md](docs/setup.md)：安装与部署
- [docs/hub.md](docs/hub.md)：hub 的接口与安全
- [docs/web.md](docs/web.md)：手机网页
- [docs/agent-local-loop.md](docs/agent-local-loop.md)：守护进程与规则
- [设计文档](docs/superpowers/specs/2026-10-21-railse-opc-design.md)
- [CONTRIBUTING.md](CONTRIBUTING.md)、[SECURITY.md](SECURITY.md)

## 现状与限制

- v0.1.1，第一版，作者自用中。
- Gemini CLI 的钩子只能拒绝不能放行：规则拒绝的照常拒绝，其余交回终端，不进手机。要 0.65.0 以上。
- Codex 的钩子只在交互会话里生效，`codex exec` 不触发；第一次使用前在 Codex 里跑 `/hooks` 审核一遍。
- hub 单机单库（SQLite），不能多实例部署。
- iPhone 要 iOS 16.4 以上，先加到主屏幕、从图标打开才能收推送。划掉应用、锁屏、几小时后还能不能收到，还没在真机上完整测过。
- hub 的域名第一天就定下：推送订阅和主屏幕图标都绑在域名上，换域名要每台手机删掉图标、重新添加、重新开启通知。

## 开发

测试用 `make test`，端到端用 `make e2e`，工具和提交方式见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 许可证

[Apache License 2.0](LICENSE)
