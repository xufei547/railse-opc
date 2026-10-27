<p align="center"><img src="assets/logo.svg" width="112" alt="Railse"></p>

# Railse 一人公司（Railse OPC）

[English](README.en.md)

一个人同时开几个 AI 编程 agent 时用的审批工具。Claude Code、Codex CLI、Gemini CLI 每次要跑命令、改文件都会停下来问你「允许吗」，Railse 把这一步接过来：规则定得了的直接批或拒，定不了的推到手机，你在手机上点一下，agent 接着跑。

## 它解决什么问题

agent 写代码的时候，真正要你做的只有一件事：它每次要动真格（跑命令、改文件、联网、推代码）之前都会停下来等你点「允许」。一个 agent 还好，三个终端同时开着，情况就变成这样：

- **哪个在等你看不出来。** 三个窗口轮流翻，经常是翻到的时候它已经等了十分钟。
- **离开电脑就全停。** 出去吃个饭，回来发现每个 agent 都停在第一条 `git push` 上。
- **全放行又不敢。** `--dangerously-skip-permissions`、`--yolo` 这类模式能让它不问，但一句 `git push --force` 或 `rm -rf` 出去了就没有后悔药。
- **各家的放行名单各写一套。** 三家 CLI 各有一套权限配置，格式互不相通；名单上的能放行，名单外的还是只能回终端按 y，人不在就等着。

Railse 把这些归成一件事：**每条审批先过一遍规则，规则定不了的才找人，找人就找手机。**

## 它怎么做

1. **三家 CLI 的权限钩子接到同一个守护进程。** 每台写代码的机器跑一个 `railse agent`，Claude Code、Codex、Gemini CLI 的每次工具调用都先经过它（Gemini CLI 的钩子只能拒绝不能放行，见下面的限制）。再加一家 CLI 只是加一个翻译，规则不用动。
2. **每条请求先定风险，再按规则。** 命令按内置表分七档，内置规则只放行两档：只读，和改项目里的文件。联网、改项目外的文件、推代码、删东西，都要问。

   | 风险档 | 例子 | 默认 |
   |---|---|---|
   | 只读 | `ls`、`cat`、`grep`、`git status`、`git diff`、`go test`、`npm test` | 放行 |
   | 项目内改动 | 改项目根目录里的文件 | 放行 |
   | 无法判断 | 命令表之外的命令；路径里带变量或通配符 | 问 |
   | 项目外改动 | 改项目根目录之外的文件 | 问 |
   | 联网 | `curl`、`npm install`、`pip install`、`brew install` | 问 |
   | 对外 | `git push`、`gh pr create`、`npm publish`、各家 `deploy`、发邮件与付款的 CLI | 问，不能被「记住」放行 |
   | 不可逆 | `rm -rf`、`git reset --hard`、`git push --force`、`DROP TABLE`、`docker system prune`；读写 `.env`、`*.pem`、`~/.ssh`、开机自启配置 | 问，不能被「记住」放行 |

   复合命令按 `&&`、`;`、`|` 拆开，每段各定一档，取最高的。放行规则要每一段都命中才生效，拒绝规则有一段命中就生效。
3. **定不了的推到手机。** 守护进程把请求送到你自己部署的 hub，hub 推一条通知到手机，点开是一个网页：命令、目录、哪台机器、风险档，批准和拒绝各一个按钮。
4. **批一次，以后不用再批。** 批准或拒绝时勾上「记住」，当场生成一条规则，范围默认是这个项目，也可以改成全局，存之前先给你看草案。规则存在 hub 上，换机器也在。
5. **底线写死在引擎里。** 对外和不可逆两档永远不能通过「记住」变成放行，只能自己手写规则，而且必须写明风险档。引擎在生成和导入规则时都校验，网页上改不掉。
6. **Railse 自己出故障不卡 agent，也不误批。** 守护进程没起、hub 连不上超过 30 秒、钩子出任何异常，都回「不作决定」，CLI 弹自己的审批框，最坏就是回到没装 Railse 时的样子。

除了审批，agent 的提问和完成也会进收件箱，告诉你去哪台机器哪个目录的终端处理；n8n 这类工作流的人工审批节点可以通过 hub 的 webhook 接进来，和 agent 的审批排在同一个收件箱里。

<p align="center"><img src="assets/demo.gif" width="390" alt="演示：手机网页上登录；Claude Code 要跑 curl，规则定不了，请求出现在收件箱；批准时勾上「记住」；规则页多了一条「学来的」规则"></p>

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
   Linux 上要装成系统服务或用反向代理，见 [docs/setup.md](docs/setup.md)。没有服务器的话，`deploy/fly/` 里有 Fly.io 的现成配置，见下面。
2. **每台工作机**：在 hub 机器上跑 `railse hub token` 拿接入码（10 分钟有效，一台一个），再在工作机上：
   ```sh
   railse setup
   ```
   按提问填 hub 地址、接入码和本机名字，或一次给全：`railse setup all --hub https://hub.example.com --token <接入码>`。它接入 hub、装钩子（连同 Codex 的 `notify`）、设开机自启、跑 `railse doctor`，最后打印手机要打开的地址。
3. **手机**：打开这个地址，用第 1 步的密码登录。iPhone 用 Safari 点「分享」→「添加到主屏幕」，从图标打开，到「设置」里点「开启通知」。

装完之后，平时照常用三家 CLI 就行。想看规则怎么判的，`railse doctor --project <目录>` 会告诉你这个目录被当成哪个项目根；想拒绝一切 `rm`，往 `~/.railse/rules.yaml` 里加一条，见 [docs/agent-local-loop.md](docs/agent-local-loop.md)。

## 数据怎么走

- **守护进程只向外连 hub。** hub 对每条请求只能答批准或拒绝，没有向机器下命令的通道。
- **载荷在本机先脱敏。** 命令里规则认得出的令牌、密码在本机就换成 `<redacted>` 再上报；认不出的会原样进 hub 和手机，见 [SECURITY.md](SECURITY.md)。
- **hub 只存哈希。** 机器密钥和登录会话在 hub 里只存哈希；推送签名密钥、webhook 回调令牌这几样不得不明文的例外，在 SECURITY.md 里列了。
- **hub 自己部署。** 手机网页由 hub 托管，一个密码登录，数据在你自己的服务器上。

## 部署到 Fly.io

`deploy/fly/` 里有现成配置，部署前改 `fly.toml` 三处：`app` 改成你的应用名；`RAILSE_PUBLIC_URL` 改成 `https://<应用名>.fly.dev`，定下后不要再换；`primary_region` 改成建卷的区域。命令见 [docs/hub.md](docs/hub.md) 的「部署到 Fly.io」。

## 文档

- [docs/setup.md](docs/setup.md)：安装、hub 部署（Linux 服务、反向代理）、接入、开机自启、`railse doctor` 各检查项、升级
- [docs/hub.md](docs/hub.md)：hub 的接口、推送、安全、Fly.io、故障排查
- [docs/web.md](docs/web.md)：手机网页
- [docs/agent-local-loop.md](docs/agent-local-loop.md)：守护进程、钩子、规则、审计
- [设计文档](docs/superpowers/specs/2026-10-21-railse-opc-design.md)
- [CONTRIBUTING.md](CONTRIBUTING.md)、[SECURITY.md](SECURITY.md)

## 现状与限制

- v0.1.2，第一版，作者自用中。
- Gemini CLI 的钩子只能拒绝不能放行：规则拒绝的照常拒绝，其余交回终端，不进手机。要 0.65.0 以上。
- Codex 的钩子只在交互会话里生效，`codex exec` 不触发；第一次使用前在 Codex 里跑 `/hooks` 审核一遍。
- hub 单机单库（SQLite），不能多实例部署。
- 项目在 `/opt`、`/usr`、`/var` 这类系统目录里时没有项目根（`/opt/homebrew` 是 git 仓库也一样），所有写入都要审批；家目录、`/tmp` 和 macOS 的 `$TMPDIR` 下的项目不受影响。
- iPhone 要 iOS 16.4 以上，先加到主屏幕、从图标打开才能收推送。划掉应用、锁屏、几小时后还能不能收到，还没在真机上完整测过。
- hub 的域名第一天就定下：推送订阅和主屏幕图标都绑在域名上，换域名要每台手机删掉图标、重新添加、重新开启通知。

## 开发

```sh
make test    # gofmt、golangci-lint、go vet、go test -race、网页检查
make e2e     # 端到端：真实的 hub、守护进程与钩子（Playwright）
make demo    # 重录 assets/demo.gif（要装 ffmpeg）
```

要装的工具和提交方式见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 请我喝杯咖啡

这个工具是我自己每天在用才开源的。要是它也帮到了你，可以在爱发电上请我喝杯咖啡。

<a href="https://afdian.com/a/railse"><img src="https://img.shields.io/badge/%E7%88%B1%E5%8F%91%E7%94%B5-%E8%AF%B7%E6%88%91%E5%96%9D%E6%9D%AF%E5%92%96%E5%95%A1-946ce6?style=for-the-badge" alt="爱发电" height="36"></a>

## 许可证

[Apache License 2.0](LICENSE)
