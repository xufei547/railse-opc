<p align="center"><img src="assets/logo.svg" width="112" alt="Railse"></p>

# Railse 一人公司（Railse OPC）

[English](README.en.md)

一个人同时开几个 AI 编程 agent 时用的审批工具。Claude Code、Codex CLI、Gemini CLI 每次要跑命令、改文件都会停下来问你「允许吗」，Railse 把这一步接过来：规则定得了的直接批或拒，定不了的推到手机，你在手机上点一下，agent 接着跑。Claude Code 用选择题问你「用方案 A 还是 B」时也一样：题目推到手机，选一个或自己填一句，它立刻继续。

## 它解决什么问题

agent 写代码的时候，真正要你做的只有一件事：它每次要动真格（跑命令、改文件、联网、推代码）之前都会停下来等你点「允许」。一个 agent 还好，三个终端同时开着，情况就变成这样：

- **哪个在等你看不出来。** 三个窗口轮流翻，经常是翻到的时候它已经等了十分钟。
- **离开电脑就全停。** 出去吃个饭，回来发现每个 agent 都停在第一条 `git push` 上。
- **全放行又不敢。** `--dangerously-skip-permissions`、`--yolo` 这类模式能让它不问，但一句 `git push --force` 或 `rm -rf` 出去了就没有后悔药。
- **各家的放行名单各写一套。** 三家 CLI 各有一套权限配置，格式互不相通；名单上的能放行，名单外的还是只能回终端按 y，人不在就等着。

Railse 把这些归成一件事：**每条审批先过一遍规则，规则定不了的才找人，找人就找手机。**

## 它怎么做

1. **三家 CLI 的权限钩子接到同一个守护进程。** 每台写代码的机器跑一个 `railse agent`。Claude Code 的每个 Bash、文件写、联网调用在执行前（`PreToolUse`）先经过它，不论哪种权限模式，auto 模式也一样；Codex 与 Gemini CLI 走各自的权限事件（Gemini CLI 的钩子只能拒绝不能放行，见下面的限制）。再加一家 CLI 只是加一个翻译，规则不用动。
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
3. **定不了的推到手机。** 守护进程把请求送到你自己部署的 hub，hub 推一条通知到手机，点开是一个网页：先用一句人话说谁要做什么、在哪、为什么问你，下面是原样的命令，批准和拒绝各一个按钮。
4. **批一次，以后不用再批。** 批准或拒绝时勾上「记住」，当场生成一条规则，范围默认是这个项目，也可以改成全局，存之前先给你看草案。规则存在 hub 上，换机器也在。
5. **底线写死在引擎里。** 对外和不可逆两档永远不能通过「记住」变成放行，只能自己手写规则，而且必须写明风险档。引擎在生成和导入规则时都校验，网页上改不掉。
6. **判不了的交人处理。** hub 连不上超过 30 秒时交回 CLI 自己的确认；越界与触及 Railse 配置的 Gemini 调用直接拒绝。守护进程启用过却连不上、请求超限等拿不到策略结论时的处理，见“现状与限制”。

各家 CLI 能做到哪一步：

| | 能批 | 只能拒 | 只能提醒 | 能在手机上答题 | auto / yolo 模式下生效 | 限时放行 |
|---|---|---|---|---|---|---|
| Claude Code | 是 | — | 等你说话、完成 | 是（`AskUserQuestion`） | 是：对外与不可逆任何模式都先到手机，终端同时弹确认框，谁先答谁算；联网、项目外改动、无法判断先交给 Claude 自己，它要问时再到手机 | 会话或项目；手机、终端、`RAILSE_TRUST` 三个入口 |
| Codex | 是（交互会话） | — | 完成 | 否 | 看 Codex 自己的 `approval_policy` | 项目（手机、终端）；会话只能从手机建 |
| Gemini CLI | 否 | 是 | 问题、完成 | 否 | 否（yolo 跳过钩子） | 不适用（钩子不能放行） |

7. **Claude Code 的选择题可以在手机上答。** 它用 `AskUserQuestion` 问你时，题目不过规则，直接推到手机，选一个选项或自己填一句，agent 立刻继续，终端不再弹选项；也可以点「让它在终端问我」，交回终端。没接 hub、hub 连不上、等过期，终端照常问。

除了审批和选择题，agent 停下来等你说下一句话、以及完成，也会进收件箱，告诉你去哪台机器哪个目录的终端处理；n8n 这类工作流的人工审批节点可以通过 hub 的 webhook 接进来，和 agent 的审批排在同一个收件箱里。做法见 [docs/integrations/n8n.md](docs/integrations/n8n.md)。

<p align="center"><img src="assets/demo.gif" width="390" alt="演示：手机网页上登录；Claude Code 要跑 curl，规则定不了，请求出现在收件箱；批准时勾上「记住」；规则页多了一条「学来的」规则"></p>

## 限时放行

盯着某个窗口干活、不想一条条批的时候，可以把一个会话或一个项目在一段时间内整体交出去（0.2.3 起）。三个入口：

- **手机**：批准一条审批时打开「接下来一段时间都放行」，范围选这个会话或这个项目，时长选 30 分钟、2 小时或 24 小时。
- **终端**：在项目目录里 `railse trust 2h`（默认 2 小时）放行这个项目，这台机器上各个 CLI 在里面的请求都算；`railse trust list` 看，`railse trust revoke <id>` 或 `--all` 撤。`--session <会话 id>` 放行一个 Claude Code 会话。
- **启动命令**：`RAILSE_TRUST=2h claude`，只放行这一个窗口，从它第一次碰上规则定不了的操作时起算。

**放行期间，范围内规则定不了的请求自动放行，到期失效，最长 24 小时。** 时长截到 5 分钟～24 小时，每台机器同时至多 20 条。例外：角色边界与 Railse 配置目录保护仍生效，拒绝规则照样拒绝，其余请求在总开关暂停时交回终端，选择题照样进手机。手机收件箱顶部有横幅提示，设置页「限时放行」一节列出全部、随时撤销；建、撤、到期都记审计。细节见 [docs/agent-local-loop.md](docs/agent-local-loop.md) 的「限时放行」。

## 角色

角色给 agent 会话划权限边界，在本机 `~/.railse/roles.yaml`：

```yaml
version: 1
roles:
  - {name: 开发, ceiling: network}
  - {name: 写作, ceiling: write_in_project, forbid: [{command_prefix: "git push"}]}
  - {name: 运维, ceiling: destructive}
```

用 `RAILSE_ROLE=写作 claude` 或 `dirs` 按项目目录绑定；接了钩子的嵌套会话继承上层约束。超过 `ceiling` 必须人定，不能记成放行、不能限时放行；`forbid` 直接拒。总台分角色看等我、放行中、24 小时内拦下与自动放行、最近动作。

`railse role init` 建模板，不覆盖。`railse role list` 列角色。`railse role check` 校验文件。`railse role which` 查当前目录的绑定及依据。

**它不是沙箱。** 它约束正常接入钩子、Railse 能识别的工具调用，挡误操作与明显越权；不保证挡住被注入后刻意规避的 agent。硬隔离请配合系统级沙箱或容器。[限制清单](docs/agent-local-loop.md#角色防什么不防什么)。

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

## 上手

**一台机器就够。** 工作机上装好 [Tailscale](https://tailscale.com/download) 并登录，然后：

```sh
railse setup all --local-hub
```

它在本机起 hub，用 Tailscale Funnel 给手机一个固定的 https 地址（不用服务器、域名、证书），接入本机守护进程，装钩子，设开机自启，跑体检，最后打印手机要打开的地址。第一次会让你设手机登录的密码；tailnet 没开通 Funnel 时它会打印开通链接，开通后重跑。机器睡眠时 hub 也不在，但那时 agent 也停了，单机用户没损失；几台机器的，把 hub 放在常开的那台。手机挂着代理打不开地址时，关掉代理，或在代理规则里把 `ts.net` 设为直连。细节见 [docs/setup.md](docs/setup.md) 的“一台机器就够：本机 hub”。

**有服务器的。** 三步：hub 机器、每台工作机、手机。

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

- **守护进程只向外连 hub。** hub 对审批只能答批准或拒绝，对 Claude Code 的选择题只能给出答案（填进选项的文字，不是命令）或交回终端，没有向机器下命令的通道。不过自己填的答案是自由文本，Claude 会把它当作你的回答照着做，所以能登进 hub 网页的人可以借答题引导 agent：这与「hub 密码等于全部审批权」是同一个信任边界。
- **载荷在本机先脱敏。** 命令里规则认得出的令牌、密码在本机就换成 `<redacted>` 再上报；认不出的会原样进 hub 和手机，见 [SECURITY.md](SECURITY.md)。
- **hub 只存哈希。** 机器密钥和登录会话在 hub 里只存哈希；推送签名密钥、webhook 回调令牌这几样不得不明文的例外，在 SECURITY.md 里列了。
- **hub 自己部署。** 手机网页由 hub 托管，一个密码登录，数据在你自己的服务器上。

## 部署到 Fly.io

`deploy/fly/` 里有现成配置，部署前改 `fly.toml` 三处：`app` 改成你的应用名；`RAILSE_PUBLIC_URL` 改成 `https://<应用名>.fly.dev`，定下后不要再换；`primary_region` 改成建卷的区域。命令见 [docs/hub.md](docs/hub.md) 的「部署到 Fly.io」。

## 文档

- [docs/setup.md](docs/setup.md)：安装、本机 hub（一台机器就够）、hub 部署（Linux 服务、反向代理）、接入、开机自启、`railse doctor` 各检查项、升级
- [docs/hub.md](docs/hub.md)：hub 的接口、总台、推送、安全、Fly.io、故障排查
- [docs/web.md](docs/web.md)：手机网页
- [docs/agent-local-loop.md](docs/agent-local-loop.md)：守护进程、钩子、规则、角色、限时放行、审计
- [与 Paperclip 一起用](docs/integrations/paperclip.md)
- [与 herdr 一起用](docs/integrations/herdr.md)
- [设计文档](docs/superpowers/specs/2026-10-21-railse-opc-design.md)
- [CONTRIBUTING.md](CONTRIBUTING.md)、[SECURITY.md](SECURITY.md)

## 现状与限制

- v0.3.0，作者自用中。
- **从 0.2.3 升级的**：hub 的库升到 v7（会话与请求加角色列、请求加越界标记，新表 `machine_roles`），第一次打开就升、不能降级，先备份 hub 数据目录。守护进程与 hub 都要升、都要重启，手机上才有总台。**必须重跑一次 `railse setup hooks`（不带 CLI 开关会重装三家，也可分别用 `--claude`、`--codex`、`--gemini`）**：钩子命令现在写死守护进程 socket 的绝对路径，Claude 的 `PreToolUse` 匹配器加了 MCP 工具与 WebSearch，不重跑角色就管不到这两类不需要 Claude 原生确认的调用。`railse doctor` 的 `hooks.claude` 会提示“PreToolUse 的匹配器是旧版（不含 MCP 工具与 WebSearch）：角色的禁用与上限管不到它们”；`hooks.claude`、`hooks.codex`、`hooks.gemini` 对旧命令提示“钩子命令没带 --socket：改了 HOME 再起的会话会连不上守护进程”。步骤见 [升级](docs/setup.md#升级)。
- **相对 0.2.3 的有意收紧，有没有角色都生效**：限时放行不再覆盖 Railse 自己的配置目录（`~/.railse`、任何 `.railse` 目录、实际守护进程目录），识别为读写这些目录的请求不接受任何自动放行。守护进程目录有 `roles.yaml`、`machine.json` 或残留 `agent.sock` 却连不上，或请求超限、答复读不懂等拿不到策略结论时，Claude 的 `PreToolUse` 改为在终端问你、Gemini 改为拒绝，不再静默交回；没有 `roles.yaml` 时 MCP／WebSearch 的 `PreToolUse` 仍交回。恢复守护进程用 `railse agent run` 或 `railse setup autostart`；彻底停用要按 [安装中的卸载说明](docs/setup.md#安装) 卸掉钩子，只停服务不算。超过 128 KiB 的命令不再解析，一律必须你来定（有没有角色都一样，放行规则与限时放行都不适用；不依赖解析的 deny 照常拒）；带包装的命令（`command`、`env`、`sudo`、`xargs` 等）按包装与内层的较高风险定级。
- 角色管不到 Codex 的免确认模式与 `codex exec`：只接了 `PermissionRequest`。Gemini 经过钩子的调用遇到越界或触及 Railse 配置一律拒绝，它没有可靠的问人通道。
- 不越界、不触及 Railse 配置且未命中限时放行时，Claude Code 的联网、项目外改动、没见过的命令，Railse 不论哪种权限模式都先交给 Claude 自己：你在 Claude 里配的 allow 列表、auto 模式的分类器放行的就直接跑，Claude 要问人时才到手机；对外与不可逆一律到手机，终端同时弹 Claude 自己的确认框（理由以「Railse：」开头，写明命令里哪一段、什么性质），两边任选其一答；连不上 hub 时只剩终端那一个。想让它们也一律到手机，在 `~/.railse/settings.yaml` 写 `claude_undecided: phone`，再 `railse agent reload`。
- Railse 在 `PreToolUse` 放行（内置的只读、项目内改动，或你「记住」的放行规则）时，Claude Code 会跳过它自己的权限检查；你在 Claude 的 `settings.json` 里写的 deny 规则仍先生效。
- 从 0.2.0 升级的：重跑一次 `railse setup hooks --claude`，Claude 的 `PreToolUse` 钩子才会匹配 Bash 等工具（`railse doctor` 的 `hooks.claude` 会提示）；不重跑的话 auto 模式下的审批照旧绕过 Railse。
- 从 0.2.1 升级的：hub 的库升到 v5（请求多一列触发点），守护进程与 hub 都要升；升级后 `git push`、`rm -rf` 这类操作终端会弹 Claude 自己的确认框，理由以「Railse：」开头，手机上同一条也在，两边任选其一答。重跑一次 `railse setup hooks --claude` 补上 `PostToolUse` 钩子（`railse doctor` 的 `hooks.claude` 会提示）：Claude Code 桌面应用里在本机处理掉的审批，靠它把手机上的卡片收掉，不补的话要等过期。
- 从 0.2.2 升级的：hub 的库升到 v6（多一张 `trusts` 表放限时放行），第一次打开就升、不能降级，升级前先备份 hub 数据目录。守护进程与 hub 都要升、都要重启，手机上才有限时放行的开关；只升守护进程时 `railse trust` 与 `RAILSE_TRUST` 在本机照样生效，手机上看不到。升级到 0.2.3 时钩子不用重装；升到 0.3.0 仍须按上面的说明重装。
- `RAILSE_TRUST` 只认 Claude Code，每个会话只建一次放行：到期或被撤后不会再建，要继续放行就换个窗口重开，或用 `railse trust`。
- Gemini CLI 的钩子只能拒绝不能放行：规则拒绝、越界、触及 Railse 配置与拿不到策略结论时拒绝，其余交回终端，不进手机。要 0.65.0 以上。
- 手机上只能答 Claude Code 的选择题（`AskUserQuestion`）。Codex、Gemini 的提问，以及 agent 停下来等你说下一句话的，仍只提醒，要回终端。
- 从 0.2 之前升级的：命令行、手机网页、推送文案都跟系统语言走（`RAILSE_LANG` → 数据目录的 `lang` 文件 → `LANG`，zh 开头算中文，其余英文）。**终端里没有设 `LANG` 的，升级后先跑一次 `railse lang zh`**，不然命令行会变英文；手机网页在设置里可以单独切。hub 的库第一次被 0.2 打开时从 v3 升到 v4（推送订阅加语言列），不能降级，升级前先备份 hub 数据目录。
- 从 0.1.5 之前升级的：已经装过钩子的机器要重跑一次 `railse setup hooks --claude`，补上选择题用的 `PreToolUse` 钩子（`railse doctor` 的 `hooks.claude` 会提示）；hub 要先于或与守护进程同时升级，hub 还是旧版时选择题直接交回终端。本机 hub 与守护进程是分别托管的两个进程，重跑 `railse setup` 不会重启已在跑的本机 hub，升级后要自己重启它（macOS `launchctl kickstart -k gui/$(id -u)/dev.railse.hub`，Linux `systemctl --user restart railse-hub`），见 [docs/setup.md](docs/setup.md) 的「升级」。
- Codex 的钩子只在交互会话里生效，`codex exec` 不触发；第一次使用前在 Codex 里跑 `/hooks` 审核一遍。
- hub 单机单库（SQLite），不能多实例部署。
- 项目在 `/opt`、`/usr`、`/var` 这类系统目录里时没有项目根（`/opt/homebrew` 是 git 仓库也一样），所有写入都要审批；家目录、`/tmp` 和 macOS 的 `$TMPDIR` 下的项目不受影响。
- iPhone 要 iOS 16.4 以上，先加到主屏幕、从图标打开才能收推送。划掉应用、锁屏、几小时后还能不能收到，还没在真机上完整测过。
- hub 的域名第一天就定下：推送订阅和主屏幕图标都绑在域名上，换域名要每台手机删掉图标、重新添加、重新开启通知。
- 本机 hub 的地址绑在 Tailscale 机器名上，改名等于换域名，手机要重新添加、重新开启通知。

## 开发

```sh
make test    # gofmt、golangci-lint、go vet、go test -race、网页检查
make e2e     # 端到端：真实的 hub、守护进程与钩子（Playwright）
make demo    # 重录 assets/demo.gif；DEMO_LANG=en make demo 录英文版 assets/demo.en.gif（要装 ffmpeg）
```

要装的工具和提交方式见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 请我喝杯咖啡

这个工具是我自己每天在用才开源的。要是它也帮到了你，可以在爱发电上请我喝杯咖啡。

<a href="https://afdian.com/a/railse"><img src="https://img.shields.io/badge/%E7%88%B1%E5%8F%91%E7%94%B5-%E8%AF%B7%E6%88%91%E5%96%9D%E6%9D%AF%E5%92%96%E5%95%A1-946ce6?style=for-the-badge" alt="爱发电" height="36"></a>

## 许可证

[Apache License 2.0](LICENSE)，版权归 xufei547。「Railse」名称和道岔图标不在授权范围内，再分发请保留 [NOTICE](NOTICE)。
