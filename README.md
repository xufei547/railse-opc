<p align="center"><img src="assets/logo.svg" width="112" alt="Railse"></p>

# Railse 一人公司（Railse OPC）

Railse 一人公司（Railse OPC）用策略引擎自动审批 AI 编程 agent（Claude Code、Codex CLI、Gemini CLI）的工具调用，定不了的进手机收件箱，在手机上批准或拒绝（Gemini CLI 只能自动拒绝，定不了的交回终端，不进手机）。

状态：第一版自用中，未发布正式版。

## 怎么工作

1. **钩子**：`railse setup` 把 `railse hook <cli>` 装进三个 CLI 的权限钩子，每次工具调用前把请求经本机 Unix socket 交给守护进程。
2. **守护进程与策略引擎**：每台工作机跑一个 `railse agent`，按 `rules.yaml` 给请求定风险档并放行或拒绝，每一步写本地审计；定不了的送 hub（Gemini CLI 只能自动拒绝，定不了的交回终端，不进手机）。
3. **hub 与手机网页**：`railse hub` 自托管在一台有公网域名的机器上，同源托管手机网页（PWA，可收 Web Push）；你在手机上做的决定经 hub 回到守护进程，再由钩子答复 CLI。

```
Claude Code / Codex CLI / Gemini CLI
        │ 权限钩子 railse hook <cli>
        ▼
railse agent（守护进程 + 策略引擎）──规则定了──▶ 放行 / 拒绝，答复钩子（Gemini CLI 只能自动拒绝）
        │ 定不了（Gemini CLI 的交回终端，不进手机；其余经 WebSocket，守护进程只向外连）
        ▼
railse hub（自托管）──Web Push──▶ 手机网页收件箱 ──批准 / 拒绝──▶ hub ▶ 守护进程 ▶ 钩子
```

Railse 自身出任何故障都不会卡住 agent，也不会误批：守护进程不在、hub 连不上（最多等 30 秒）时请求交回终端，由 CLI 自己的审批处理。

## 安装

```sh
curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | sh
```

支持 macOS（Apple Silicon）与 Linux（amd64、arm64）。脚本从 GitHub Releases 下载对应的包，按同一发布的 `checksums.txt` 校验 sha256，校验不过不安装；不调用 sudo：`/usr/local/bin` 可写就装那里，否则装到 `~/.local/bin`（不在 PATH 里时会提示）。指定版本或目录：`curl -fsSL … | RAILSE_VERSION=0.1.0 RAILSE_INSTALL_DIR=~/bin sh`。Linux 上跑 hub 的机器改用 `curl -fsSL … | sudo sh`，以 root 运行时装进 `/usr/local/bin`（看输出里的“已安装 …”确认，见下面第 1 步）。升级就是重跑一遍。

或者用 Go 自己编译：`go install github.com/xufei547/railse-opc/cmd/railse@latest`。这样装出的二进制不带手机网页（网页路径回 503“网页未构建”），只适合工作机；跑 hub 的机器请用上面的安装脚本，或在仓库里 `make build`。

## 三步上手

1. **hub 机器**（域名已解析到这台机器，443 与 80 对公网开放）。macOS 绑定 443/80 不需要特权，直接跑：
   ```sh
   railse hub password    # 从标准输入读一行设为网页登录密码
   railse hub token       # 打印一次性接入码，默认 10 分钟有效
   railse hub run --acme-domain hub.example.com --acme-email me@example.com
   ```
   Linux 上普通用户绑不了 443/80，hub 也不该以 root 跑：装成系统服务（以普通用户运行，只给绑定特权端口的 `CAP_NET_BIND_SERVICE`，开机自启）。服务用户要能读、能执行 railse 本身，所以 hub 机器上用 root 安装，装到 `/usr/local/bin`（属主 root、0755）；sudo 的 `secure_path` 不一定含它（RHEL 系默认只有 `/sbin:/bin:/usr/sbin:/usr/bin`），sudo 时一律写绝对路径：
   ```sh
   curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | sudo sh
   sudo /usr/local/bin/railse setup autostart --hub --system -- --acme-domain hub.example.com --acme-email me@example.com
   railse hub password --dir /var/lib/railse-hub    # 不加 sudo，以服务用户（默认就是执行 sudo 的你）运行
   railse hub token --dir /var/lib/railse-hub
   ```
   数据目录 `/var/lib/railse-hub` 由 systemd 在服务第一次启动时建好并交给服务用户，所以 `password`、`token` 要在 `setup autostart` 之后跑；用 `--user 名字` 指定了别的服务用户时，以那个用户身份（`sudo -u 名字 /usr/local/bin/railse hub …`）运行。railse 装在别处时写 `sudo "$(command -v railse)" setup autostart --hub --system -- …`（单元里的程序路径随之取它），但服务用户必须能执行那个路径：程序在你的家目录（常是 0750）下而 `--user` 换成了别的用户时，服务用户执行不了它，单元会以 `203/EXEC` 反复重启。详见 [docs/setup.md](docs/setup.md)。
2. **每台工作机**：在终端里跑一次 `railse setup`，按提问填 hub 地址、接入码（第 1 步 `railse hub token` 打印的）和本机名字（直接回车用主机名）；也可以一次给全：`railse setup all --hub https://hub.example.com --token <接入码>`。一次做完接入 hub、装三个 CLI 的钩子、开机自启与体检（钩子默认装用户级，对所有项目生效；只想让某个项目用的，加 `--project <目录>` 把 Claude、Gemini 的装成那个项目的项目级），最后把手机要打开的地址框起来打印。之后随时可以 `railse doctor` 检查。
3. **手机**：打开上一步框里的地址（如 `https://hub.example.com/`），用第 1 步的密码登录；iPhone 上用 Safari 打开后分享→添加到主屏幕，再从图标打开，在设置里开启通知。

## 文档

- [docs/setup.md](docs/setup.md)：安装、hub 部署、工作机接入、开机自启、`railse doctor` 各检查项、升级与发布
- [docs/agent-local-loop.md](docs/agent-local-loop.md)：本机闭环（守护进程、钩子、规则、审计）
- [docs/hub.md](docs/hub.md)：hub 的部署、接口、推送、安全与故障排查
- [docs/web.md](docs/web.md)：手机网页（PWA）
- [docs/superpowers/specs/2026-10-21-railse-opc-design.md](docs/superpowers/specs/2026-10-21-railse-opc-design.md)：第一版设计

## 开发

需要 Go（版本见 `go.mod`）与 Node 20。`make release-check`（校验 `.goreleaser.yaml`）要 Go ≥ 1.27.3 才编得出 goreleaser（`GOTOOLCHAIN=auto` 会自动取）；CI 发布用 goreleaser Action 的预编译二进制，不受影响。

```sh
make test    # gofmt、golangci-lint、go vet、go test -race、网页检查
make e2e     # 网页端到端（真实的 hub、守护进程与钩子，Playwright）
make lint    # 只跑 golangci-lint（配置见 .golangci.yml）
make build   # 构建网页并编译带网页的 ./railse
```

## 许可证

[Apache License 2.0](LICENSE)
