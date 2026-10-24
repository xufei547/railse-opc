# 安装、接入与开机自启

一台有公网域名的机器跑 hub，每台写代码的机器跑守护进程并装钩子，手机打开 hub 的网页。下面的命令都是完整形式，`hub.example.com`、`me@example.com` 换成你自己的。hub 的接口与行为见 [hub.md](hub.md)，本机闭环（规则、审计）见 [agent-local-loop.md](agent-local-loop.md)，手机网页见 [web.md](web.md)。

## 安装

```sh
curl -fsSL https://raw.githubusercontent.com/railse/railse-opc/main/install.sh | sh
```

- **平台。** macOS（Apple Silicon）、Linux amd64 与 arm64；不支持 Windows 与 Intel Mac。
- **校验和。** 脚本从 GitHub Releases 下载 `railse_<版本>_<系统>_<架构>.tar.gz`，按同一发布的 `checksums.txt` 校验 sha256，不符就不装（只有校验和，没有签名：两者来自同一处）。新文件先写到安装目录里的临时名再改名替换，正在运行的旧二进制不受影响。
- **安装目录。** 脚本不调用 sudo：`/usr/local/bin` 可写就装那里，否则装 `~/.local/bin`（不存在就建），它不在 `PATH` 里时打印要加进 shell 配置的那一行。
- **环境变量**（经管道运行时写在 `sh` 前面）：`RAILSE_VERSION`（如 `0.1.0`，带 `v` 也行，默认最新正式版）、`RAILSE_INSTALL_DIR`（安装目录，不存在就建）、`RAILSE_REPO`（默认 `railse/railse-opc`）。例如：
  ```sh
  curl -fsSL https://raw.githubusercontent.com/railse/railse-opc/main/install.sh | RAILSE_VERSION=0.1.0 RAILSE_INSTALL_DIR="$HOME/bin" sh
  ```
- **Linux 上的 hub 机器装到系统目录。** 系统单元（见下文）要用 `sudo` 跑 railse，而 sudo 的 `secure_path` 里没有 `~/.local/bin`（`sudo: railse: command not found`）；服务用户也必须能读、能执行这个二进制。所以 hub 机器用 root 装，脚本以 root 运行时 `/usr/local/bin` 可写，就装在那里（属主 root、0755，任何用户都能执行）：
  ```sh
  curl -fsSL https://raw.githubusercontent.com/railse/railse-opc/main/install.sh | sudo sh
  ```
  sudo 会清掉环境变量，要指定版本时写在 `sudo` 后面：`… | sudo RAILSE_VERSION=0.1.0 sh`；极少数没有 `/usr/local/bin` 的系统上，脚本会退到 root 的 `~/.local/bin`，这时加 `sudo RAILSE_INSTALL_DIR=/usr/local/bin sh`。
- **自己编译。** `go install github.com/railse/railse-opc/cmd/railse@latest` 装出的二进制不带手机网页（网页路径回 503“网页未构建”），只适合工作机；hub 用安装脚本，或在仓库里 `make build`。
- **升级**就是重跑一遍安装脚本，再重启守护进程与 hub（见“升级”）。
- **卸载。** 先停掉托管（数据目录保留），再删钩子与二进制：
  ```sh
  railse setup autostart --uninstall                 # 守护进程
  railse setup autostart --hub --uninstall           # 用户级托管的 hub
  sudo railse setup autostart --hub --system --uninstall   # Linux 系统单元的 hub
  ```
  没有卸载钩子的命令：删二进制之前，手动删掉各 CLI 配置里 Railse 的条目（命令是 `<railse 路径> hook claude|codex|gemini` 的那几条，在项目的 `.claude/settings.local.json`、`.gemini/settings.json` 与 `~/.codex/hooks.json` 里；`~/.codex/config.toml` 里的 `notify` 那一行也删掉），否则 CLI 每次调用钩子都会报错。最后 `rm "$(command -v railse)"`（以 root 装进 `/usr/local/bin` 的加 sudo）；不要的数据目录（`~/.railse`、`~/.railse-hub`，系统单元的 `/var/lib/railse-hub` 要 sudo）自己删。

## hub 机器

前提：域名的 A/AAAA 记录已指向这台机器，443 与 80 对公网开放（云厂商的安全组与防火墙都要放行）。三条命令：

```sh
railse hub password    # 从标准输入读一行设为网页登录密码（至少 8 个字符）；不回显：read -s PW; printf '%s\n' "$PW" | railse hub password; unset PW
railse hub token       # 打印一次性接入码，默认 10 分钟有效（--ttl 至多 24h），用一次即作废
railse hub run --acme-domain hub.example.com --acme-email me@example.com
```

数据目录取 `--dir`，否则 `$RAILSE_HUB_HOME`，否则 `~/.railse-hub`（0700）。macOS 上绑定 443/80 不需要特权，上面的命令直接跑；要开机自启就把最后一条换成 `railse setup autostart --hub -- --acme-domain hub.example.com --acme-email me@example.com`。

### TLS 的两种方式

**A. 内置 ACME（推荐自用）。** `--acme-domain` 让 hub 自己向 Let's Encrypt（只用生产目录）申请证书并直出 TLS，`--listen` 默认 `:443`；`--acme-http`（默认 `:80`）答 HTTP-01 挑战并把 http 跳到 https，`--acme-http none` 不监听 80、只靠 443 上的 TLS-ALPN-01。只给这一个域名签：别的 SNI 与按 IP 访问在握手时被拒。证书在第一次 https 访问时签发（约 1 到 2 分钟），到期前 30 天自动续期。对外源默认 `https://<域名>`，手机推送随之启用，cookie 一律 Secure。账户私钥与证书私钥明文存在数据目录的 `acme/`（目录 0700、文件 0600），当私密数据备份。`--acme-email` 只用于 Let's Encrypt 的账户与政策通知，可不填：Let's Encrypt 已于 2025 年停发证书到期提醒邮件，续期失败不会有人发信提醒。签发、续期出错时，TLS 握手的报错（形如 `http: TLS handshake error … acme/autocert: …`）写在 hub 的 stderr：`--foreground` 时在终端，系统单元看 `journalctl -u railse-hub`，launchd 托管时看数据目录的 `launchd.log`；证书真过期了，工作机上 `railse doctor` 的 `hub.reachable` 会报证书错误。同一域名有签发频率限制，不要反复删 `acme/` 重来。Linux 上绑定 443/80 需要 `CAP_NET_BIND_SERVICE`，用下面的系统单元。

**B. 反向代理。** 已有 nginx、Caddy 终止 TLS 时，hub 只监听本机：

```sh
railse hub run --listen 127.0.0.1:8787 --public-url https://hub.example.com --trusted-proxy 127.0.0.1
```

`--public-url` 是手机打开网页的 https 源（不带路径），设了才启用推送，cookie 也随之加 Secure；`--trusted-proxy` 让 hub 只信任代理给的 `X-Forwarded-For`。代理必须保留原 Host、转发 WebSocket，配置示例见 [hub.md](hub.md) 的“部署与反向代理”。

### Linux：装成系统服务

普通用户绑不了 443/80，hub 也不该以 root 跑：系统单元以普通用户运行，只多给一项 `CAP_NET_BIND_SERVICE`（`AmbientCapabilities=`），整个文件系统只读、只有数据目录可写。

```sh
curl -fsSL https://raw.githubusercontent.com/railse/railse-opc/main/install.sh | sudo sh    # 装到 /usr/local/bin（见“安装”）
sudo railse setup autostart --hub --system -- --acme-domain hub.example.com --acme-email me@example.com
railse hub password --dir /var/lib/railse-hub     # 不加 sudo：以服务用户（默认就是执行 sudo 的你）运行
railse hub token --dir /var/lib/railse-hub
```

- 写 `/etc/systemd/system/railse-hub.service` 并 `daemon-reload`、`enable`、`restart`。`--system` 只给 `--hub` 用，只在 Linux 上有效；需要 systemd 240 以上（`Type=exec`）。
- `User=` 默认取 `$SUDO_USER`，用 `--user 名字` 指定别的普通用户；root 与全数字的名字一律拒绝。用了别的用户时，后两条命令以那个用户身份跑：`sudo -u 名字 railse hub password --dir /var/lib/railse-hub`。
- 数据目录默认 `/var/lib/railse-hub`，由 systemd 按 `StateDirectory=railse-hub` 在服务第一次启动时建好（0700）并交给服务用户，所以 `password`、`token` 要在 `setup autostart` 之后跑。显式给 `--dir` 时，目录要么已经属于服务用户（不替你 chown，只收紧为 0700），要么还不存在、由这次新建并交给服务用户：这要求它已有的那段祖先都属于 root 且别人不能写（如 `/srv/railse-hub`）。祖先不满足（例如在家目录下）时，先以服务用户身份建好再跑：`sudo -u 名字 mkdir -p -m 700 /home/名字/railse-hub`。
- 单元里的 `ExecStart` 是 railse 的绝对路径（`--bin`，默认就是这次运行的程序），服务用户必须能读、能执行它。railse 已经装在 `~/.local/bin` 时，`sudo railse` 找不到命令，可以写成 `sudo "$(command -v railse)" setup autostart --hub --system -- --acme-domain hub.example.com --acme-email me@example.com`（`--bin` 随之取这个路径）；但只有服务用户就是你自己时才这样做：用 `--user` 换成别的用户而程序在你的家目录（常是 0750）下时，服务用户执行不了它，单元会以 `203/EXEC` 反复重启。
- 日志：`journalctl -u railse-hub`，数据目录里还有 `hub.log`。状态：`railse setup autostart --hub --system --status`（不用 sudo；或 `systemctl status railse-hub`）。卸载：`sudo railse setup autostart --hub --system --uninstall`（数据目录保留）。

## 工作机

在一个项目目录里跑一次：

```sh
railse setup all --hub https://hub.example.com --token <接入码>
```

依次做四步，任一步失败就停下，说明停在哪一步、哪些已做完，修好后重跑（已接入的会跳过接入）：

1. **接入 hub**（同 `railse agent enroll`）：用接入码换本机长期密钥，写 `~/.railse/machine.json`（0600）。已有 `machine.json` 时跳过，这时可以不给 `--hub`、`--token`；`--force` 重新接入。
2. **装钩子**（同 `railse setup hooks --project <项目> --bin <程序>`）：三个 CLI 都装。
3. **开机自启**（同 `railse setup autostart`）：托管守护进程并等它应答。已经有手动 `railse agent run` 起的守护进程占着 socket 时跳过托管并提示：先在它的终端里 Ctrl-C 停掉，再 `railse setup autostart`。
4. **体检**（同 `railse doctor`）：有失败项时照常展示，但不让 `setup all` 失败（刚托管的守护进程可能还没起来），按提示处理后再跑 `railse doctor` 复查。

其余标志：`--name 名字`（在 hub 里的名字，默认主机名）、`--project 目录`（默认当前目录）、`--dir 目录`（守护进程数据目录，默认 `$RAILSE_HOME` 或 `~/.railse`）、`--bin 路径`、`--no-hooks`、`--no-autostart`。`setup all` 要用平时的用户身份跑，以 root 运行直接拒绝（否则会用掉接入码，`machine.json` 与钩子配置也成了 root 的文件）。

**各 CLI 的钩子。** Claude 与 Gemini 的钩子是项目级的：别的项目再跑 `railse setup hooks --project <目录>`。

- **Claude Code**：写 `<项目>/.claude/settings.local.json`（钩子命令里是本机的 railse 路径，不该入库）；旧版写在 `.claude/settings.json` 的 Railse 条目会自动迁走，别的设置原样保留。railse 新建的 `settings.local.json` 不一定被 git 忽略，`git status` 里看到它就加进 `.gitignore`（或全局的 `~/.config/git/ignore`）：`echo '.claude/settings.local.json' >> .gitignore`。
- **Codex CLI**：写用户级 `~/.codex/hooks.json`；只在交互会话里审批（`codex exec` 不触发）；首次使用在 Codex 里用 `/hooks` 审核；`notify` 要手动加到 `~/.codex/config.toml` 开头（第一个 `[表]` 之前），`setup hooks` 会打印那一行。
- **Gemini CLI**：写 `<项目>/.gemini/settings.json`；需要 0.65.0 以上，不要开 yolo 模式。Gemini 的钩子只能拒绝不能放行：规则拒绝的照常自动拒绝，其余（规则放行的与定不了的）都交回终端，由 Gemini 自己的确认框处理，永远不进 hub 与手机。

**单独用子命令。**

```sh
railse agent enroll --hub https://hub.example.com --token <接入码> [--name 名字] [--dir 目录] [--force]
railse setup hooks [--project 目录] [--claude] [--codex] [--gemini] [--codex-file 路径] [--bin 路径] [--dry-run]
railse setup autostart [--hub] [--uninstall] [--status] [--dir 目录] [--bin 路径] [--system] [--user 名字] [--dry-run] [-- hub run 的额外参数]
```

`enroll` 之后要重启守护进程才接上 hub（它只在启动时读 `machine.json`）：托管的重跑 `railse setup autostart`，前台跑的停掉后重新 `railse agent run`。`setup hooks` 不带 CLI 开关时三个都装，`--dry-run` 只打印不写，重复执行只替换 Railse 自己的条目。`setup autostart` 的 `--dry-run` 只打印要写的文件与要跑的命令。

## 开机自启

`railse setup autostart` 托管守护进程，`--hub` 托管 hub；`--` 之后的参数原样追加到 `hub run` 后面（数据目录用 `setup autostart` 自己的 `--dir`，不要放在 `--` 之后）。退出后自动拉起，数据目录保留。

| | 托管文件 | 日志 |
| --- | --- | --- |
| macOS | `~/Library/LaunchAgents/dev.railse.agent.plist`、`dev.railse.hub.plist`（launchd 用户代理，登录即起） | 数据目录的 `launchd.log`，以及 `agent.log`、`hub.log` |
| Linux 用户单元 | `~/.config/systemd/user/railse-agent.service`、`railse-hub.service` | `journalctl --user -u railse-agent`（hub 换成 `railse-hub`） |
| Linux 系统单元（只有 hub） | `/etc/systemd/system/railse-hub.service` | `journalctl -u railse-hub` |

- 看状态：`railse setup autostart --status`（hub 加 `--hub`，系统单元再加 `--system`），在跑退出 0，否则退出 1 并说明。
- 卸载：同一条命令加 `--uninstall`；托管文件在而停不下来时报错、不删文件。
- Linux 用户单元默认只在你登录期间跑；要开机即起、退出登录也不停，执行一次 `loginctl enable-linger $USER`。
- macOS 与 Linux 用户级托管不要用 sudo 跑（直接拒绝）；只有 hub 的系统单元用 sudo。

## railse doctor

```sh
railse doctor [--project 目录] [--dir 目录] [--hub-dir 目录] [--bin 路径] [--json]
```

只读：不改配置、不建文件，数据库按只读打开；网络只访问 hub 的 `GET /api/version`（不带凭据）；唯一会起的进程是 `claude`、`codex`、`gemini` 的 `--version`（各 10 秒超时），sudo 下（euid 是 root，或家目录不归当前用户）不跑，`cli.*` 报 skip。每项结果是 `ok`、`warn`、`fail`、`skip` 之一，warn 与 fail 下一行给“修复：”；有任一项 fail 退出码 1；`--json` 输出机器可读的结果（ID 固定不变）。

hub 侧：数据目录默认 `$RAILSE_HUB_HOME`，否则 `~/.railse-hub`；Linux 上两者都没有时再看 `/var/lib/railse-hub`（系统单元的）。默认目录都不在就跳过 hub 侧；显式给的 `--hub-dir` 不存在时报 fail。系统单元的 hub 要以服务用户身份检查（`railse doctor`，或 `sudo -u 名字 railse doctor --hub-dir /var/lib/railse-hub`），别的用户读不到 `hub.db`。

| ID | 看什么 | 不过时 |
| --- | --- | --- |
| `agent.dir` | 守护进程数据目录在、是 0700 | 先 `railse agent run` 建好；权限不对按提示 `chmod 700` |
| `agent.files` | `rules.yaml`、`machine.json`、`audit.jsonl`、`state.json`（存在的）都是 0600 | 按提示 `chmod 600` |
| `agent.socket` | `agent.sock` 在、是 socket、0600 | 守护进程没在跑：`railse agent run` 或 `railse setup autostart`；不是 socket 就移走它再重启 |
| `agent.running` | 守护进程应答 status | 同上 |
| `agent.hub` | 守护进程连 hub 的状态：`connected` 为 ok，`connecting`、`none`（未接入）为 warn，`disabled`（连续 3 次被 401 拒绝）为 fail | `connecting` 看 `agent.log`；`disabled` 要 `railse agent enroll --force` 后重启守护进程；没有报状态的是升级前起的旧进程，重启它 |
| `agent.autostart` | 有没有守护进程的托管文件（只看文件，不问 launchd/systemd） | `railse setup autostart`（warn） |
| `hub.reachable` | `machine.json` 里 hub 的 `/api/version` 答得上、协议号与本机一致；没接入时 skip | 连不上：看 hub 在不在跑、地址、证书；不是 200：hub 太旧或地址被重定向；协议不一致：升级较旧的一方 |
| `hooks.claude` | `.claude/settings.local.json` 里四个事件都在，`PermissionRequest` 超时不低于 86400 秒，程序路径是当前程序 | 缺了或超时不够：`railse setup hooks --project <目录> --claude`；只在旧位置 `settings.json`，或两处都装了（会触发两次）：同一条命令迁走（warn） |
| `hooks.codex` | `~/.codex/hooks.json` 同样检查（`PermissionRequest` ≥ 86400 秒） | `railse setup hooks --codex` |
| `hooks.codex_notify` | `~/.codex/config.toml` 顶层有 `notify = [..., "hook", "codex-notify"]` | 把 `setup hooks` 打印的那一行加到文件开头（warn） |
| `hooks.gemini` | `.gemini/settings.json` 四个事件都在，`BeforeTool` 超时不低于 86400000 毫秒 | `railse setup hooks --project <目录> --gemini` |
| `cli.claude`、`cli.codex` | 版本是否与录制钩子样本时一致（Claude Code 2.1.280、Codex 0.145.0）；不在 PATH 里 skip | 不同时 warn：试一次需要审批的操作，确认请求经过 Railse |
| `cli.gemini` | 版本不低于 0.65.0（更早的版本钩子在确认之后才触发） | 升级 Gemini CLI（fail） |
| `hubsrv.dir` | hub 数据目录在、是 0700 | 显式 `--hub-dir` 写错了就改对；不跑 hub 的机器去掉它 |
| `hubsrv.db` | `hub.db` 是 0600，库版本与程序一致 | 库比程序新（fail）：升级 railse；库旧（warn）：下次 `hub run` 自动升级，先备份；被锁：稍后重跑；hub 异常退出留下未恢复的 WAL（warn）：启动一次 `railse hub run`；真损坏才从备份恢复 |
| `hubsrv.password` | 设过网页密码 | `railse hub password`（warn） |
| `hubsrv.vapid` | 推送密钥在、解得开 | 还没有（warn）：首次 `railse hub run` 会生成；损坏（fail）：`railse hub vapid --rotate`，所有手机重新开启通知 |
| `hubsrv.acme` | `acme/` 是 0700、里面的文件 0600；没有这个目录（没用 ACME）时 skip | 按提示 `chmod` |
| `hubsrv.autostart` | 有没有 hub 的托管文件（plist、用户单元或系统单元） | `railse setup autostart --hub`（warn） |

## 升级

1. 重跑安装脚本（与当初同样的方式：Linux 的 hub 机器用 `| sudo sh`）。
2. 重启守护进程：托管的重跑 `railse setup autostart`（当初给过 `--dir`、`--bin` 的照样带上；macOS 会重新 bootout、bootstrap，Linux 会 `daemon-reload` 并 `restart`）；前台跑的停掉后重新 `railse agent run`。
3. 重启 hub：系统单元 `sudo systemctl restart railse-hub`，Linux 用户单元 `systemctl --user restart railse-hub`，macOS `launchctl kickstart -k gui/$(id -u)/dev.railse.hub`。也可以重跑 `railse setup autostart --hub`，但要带上当初 `--` 之后的全部参数，否则托管文件会被改写成不带它们的。
4. hub 与各工作机升级到同一版本：协议号不一致时 hub 拒绝守护进程的握手。升级后在工作机上跑 `railse doctor` 确认 `hub.reachable` 与 `agent.hub` 都是 ok。

## 发布（维护者）

1. `main` 上的 CI（Go、网页、e2e、三平台交叉编译四个作业）是绿的；本地 `make test`、`make e2e`、`make release-check`、`scripts/test-install.sh` 都过，再手动跑一次 `scripts/smoke-claude-hub.sh`（真实 Claude Code 经 hub 审批，要已登录，会消耗额度）。想先看一眼包：`GOTOOLCHAIN=local go run github.com/goreleaser/goreleaser/v2@v2.19.0 release --snapshot --clean --skip=publish`（产物在 `dist/`，不入库）。
2. 打 tag 并推送，release 工作流（它不等 CI，所以第 1 步要先确认）用 goreleaser v2.19.0 出包并建 GitHub Release：
   ```sh
   git tag v0.1.0 && git push origin v0.1.0
   ```
3. 产物是 `railse_<版本>_darwin_arm64.tar.gz`、`railse_<版本>_linux_amd64.tar.gz`、`railse_<版本>_linux_arm64.tar.gz`（各含 `railse`、`LICENSE`、`README.md` 与 `docs/*.md`）和 `checksums.txt`；版本号取自 tag（去掉 `v`），经 `-X main.version` 注入。带后缀的 tag（如 `v0.2.0-rc.1`）发成预发布，安装脚本默认的“最新正式版”不会取到它。发布后在一台干净的机器上跑一行安装，确认打印出新版本。
