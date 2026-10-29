# 安装、接入与开机自启

一台有公网域名的机器跑 hub，每台写代码的机器跑守护进程并装钩子，手机打开 hub 的网页。下面的命令都是完整形式，`hub.example.com`、`me@example.com` 换成你自己的。hub 的接口与行为见 [hub.md](hub.md)，本机闭环（规则、审计）见 [agent-local-loop.md](agent-local-loop.md)，手机网页见 [web.md](web.md)。

## 安装

```sh
curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | sh
```

- **平台。** macOS（Apple Silicon）、Linux amd64 与 arm64；不支持 Windows 与 Intel Mac。
- **校验和。** 脚本从 GitHub Releases 下载 `railse_<版本>_<系统>_<架构>.tar.gz`，按同一发布的 `checksums.txt` 校验 sha256，不符就不装（只有校验和，没有签名：两者来自同一处）。新文件先写到安装目录里的临时名再改名替换，正在运行的旧二进制不受影响。
- **安装目录。** 脚本不调用 sudo：`/usr/local/bin` 可写就装那里，否则装 `~/.local/bin`（不存在时等下载与校验都过了才建），它不在 `PATH` 里时打印要加进 shell 配置的那一行。
- **环境变量**（经管道运行时写在 `sh` 前面）：`RAILSE_VERSION`（如 `0.1.0`，带 `v` 也行，默认最新正式版）、`RAILSE_INSTALL_DIR`（安装目录，不存在就建）、`RAILSE_REPO`（默认 `xufei547/railse-opc`）。例如：
  ```sh
  curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | RAILSE_VERSION=0.1.0 RAILSE_INSTALL_DIR="$HOME/bin" sh
  ```
- **Linux 上的 hub 机器装到系统目录。** 系统单元（见下文）的服务用户必须能读、能执行这个二进制，所以 hub 机器用 root 装：脚本以 root 运行时 `/usr/local/bin` 可写，就装在那里（属主 root、0755，任何用户都能执行）。
  ```sh
  curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | sudo sh
  ```
  看输出里的“已安装 /usr/local/bin/railse”确认位置。sudo 会清掉环境变量，要指定版本时写在 `sudo` 后面：`… | sudo RAILSE_VERSION=0.1.0 sh`。没有 `/usr/local/bin` 的系统上脚本会退到 `$HOME/.local/bin`，而 sudo 下的 `HOME` 是 root 的还是你的取决于 sudo 的配置（老版本 Ubuntu 保留你的 `HOME`，会在你的 `~/.local/bin` 里留下 root 属主的文件），这时显式指定：`… | sudo RAILSE_INSTALL_DIR=/usr/local/bin sh`。
- **sudo 时写绝对路径。** sudo 只在 `secure_path` 里找命令：它没有 `~/.local/bin`，RHEL 系（RHEL、CentOS、Rocky、Alma、Amazon Linux）默认连 `/usr/local/bin` 都没有（只有 `/sbin:/bin:/usr/sbin:/usr/bin`），`sudo railse …` 会报 `command not found`。所以下文凡要 sudo 的都写 `sudo /usr/local/bin/railse …`；railse 装在别处时写 `sudo "$(command -v railse)" …`。
- **自己编译。** `go install github.com/xufei547/railse-opc/cmd/railse@latest` 装出的二进制不带手机网页（网页路径回 503“网页未构建”），只适合工作机；hub 用安装脚本，或在仓库里 `make build`。
- **升级**就是重跑一遍安装脚本，再重启守护进程与 hub（见“升级”）。
- **卸载。** 先停掉托管（数据目录保留），再删钩子与二进制：
  ```sh
  railse setup autostart --uninstall                 # 守护进程
  railse setup autostart --hub --uninstall           # 用户级托管的 hub
  sudo /usr/local/bin/railse setup autostart --hub --system --uninstall   # Linux 系统单元的 hub
  ```
  没有卸载钩子的命令：删二进制之前，手动删掉各 CLI 配置里 Railse 的条目（命令是 `<railse 路径> hook claude|codex|gemini` 的那几条，数据目录不是 `~/.railse` 的后面还带 `--socket …`，在用户级的 `~/.claude/settings.json`、`~/.gemini/settings.json`、`~/.codex/hooks.json` 里，用 `--project` 装过的还在那些项目的 `.claude/settings.local.json`、`.gemini/settings.json` 里），否则 CLI 每次调用钩子都会报错。跑过 `railse setup`、`setup all`（没加 `--no-hooks`）或 `setup codex-notify` 的，删掉数据目录里的 `codex-notify.sh`，再把 `~/.codex/config.toml` 顶层的 `notify` 换回原来的（原来的写在脚本的标记行 `# railse-prev-notify: [...]` 里，最早的备份 `~/.codex/config.toml.railse-bak-<时间>` 里也有；原来没有就把那一行删掉）；直接写过 `notify = [".../railse", "hook", "codex-notify"]` 的也删掉那一行。`setup hooks`（`railse setup`、`setup all` 的第 2 步同样）第一次改 `~/.claude/settings.json`、`~/.gemini/settings.json` 之前留下的备份 `~/.claude/settings.json.railse-bak-<时间>`、`~/.gemini/settings.json.railse-bak-<时间>` 是装钩子之前的原样（`setup codex-notify` 与 `railse setup`、`setup all` 的第 2 步每次改 `config.toml` 之前留下的 `~/.codex/config.toml.railse-bak-<时间>` 是那一次之前的样子）：不要了就删掉（拿它覆盖回去也能回到装之前，但之后对那个文件的改动会一起丢掉）。这些备份是原样副本，可能带着 API key、MCP 的 token，策略引擎按敏感文件处理（任何目录下的 `*.railse-bak-*`）：agent 读写它们都按 destructive 交给你审批。最后删掉二进制：`rm "$(command -v railse)"`，以 root 装进去的用 `sudo rm /usr/local/bin/railse`；不要的数据目录（`~/.railse`、`~/.railse-hub`，系统单元的 `/var/lib/railse-hub` 要 sudo）自己删。

## hub 机器

前提：域名的 A/AAAA 记录已指向这台机器，443 与 80 对公网开放（云厂商的安全组与防火墙都要放行）。

**Linux 上直出 443/80 的，不要照下面三条跑，直接看后面的“Linux：装成系统服务”。** 下面三条适用于 macOS（绑定 443/80 不需要特权）；用反向代理时（Linux 上也一样，hub 只监听本机、不需要特权）把第三条换成 B 里的命令：

```sh
railse hub password    # 从标准输入读一行设为网页登录密码（至少 8 个字符）；不回显：read -s PW; printf '%s\n' "$PW" | railse hub password; unset PW
railse hub token       # 打印一次性接入码，默认 10 分钟有效（--ttl 至多 24h），用一次即作废
railse hub run --acme-domain hub.example.com --acme-email me@example.com
```

数据目录取 `--dir`，否则 `$RAILSE_HUB_HOME`，否则 `~/.railse-hub`（0700）。要开机自启就把最后一条换成 `railse setup autostart --hub -- --acme-domain hub.example.com --acme-email me@example.com`（用户级托管，不用 sudo）。这条只适用于 macOS：Linux 上的用户单元绑不了 1024 以下的端口，hub 会起不来、被 systemd 反复拉起，`setup autostart` 照装但会醒目地警告，并给出改用系统单元的命令。

### TLS 的两种方式

**A. 内置 ACME（推荐自用）。** `--acme-domain` 让 hub 自己向 Let's Encrypt（只用生产目录）申请证书并直出 TLS，`--listen` 默认 `:443`；`--acme-http`（默认 `:80`）答 HTTP-01 挑战并把 http 跳到 https，`--acme-http none` 不监听 80、只靠 443 上的 TLS-ALPN-01。只给这一个域名签：别的 SNI 与按 IP 访问在握手时被拒。证书在第一次 https 访问时签发（约 1 到 2 分钟），之后由 autocert 在到期前自动续期（默认提前 30 天或有效期的 1/3，取较小者），不用重启。对外源默认 `https://<域名>`，手机推送随之启用，cookie 一律 Secure。账户私钥与证书私钥明文存在数据目录的 `acme/`（目录 0700、文件 0600），当私密数据备份。`--acme-email` 只用于 Let's Encrypt 的账户与政策通知，可不填：Let's Encrypt 已于 2025 年停发证书到期提醒邮件，续期失败不会有人发信提醒。签发、续期出错时，TLS 握手的报错（形如 `http: TLS handshake error … acme/autocert: …`）由 Go 标准库的 logger 写到 hub 进程的 stderr（不进 `hub.log`）：前台跑时就在终端里（与 `--foreground` 无关，它只决定 hub 自己的日志是否也打到终端），系统单元看 `journalctl -u railse-hub`，launchd 托管时看数据目录的 `launchd.log`；证书真过期了，工作机上 `railse doctor` 的 `hub.reachable` 会报证书错误。同一域名有签发频率限制，不要反复删 `acme/` 重来。Linux 上绑定 443/80 需要 `CAP_NET_BIND_SERVICE`，用下面的系统单元。

**B. 反向代理。** 已有 nginx、Caddy 终止 TLS 时，hub 只监听本机：

```sh
railse hub run --listen 127.0.0.1:8787 --public-url https://hub.example.com --trusted-proxy 127.0.0.1
```

`--public-url` 是手机打开网页的 https 源（不带路径），设了才启用推送，cookie 也随之加 Secure；`--trusted-proxy` 让 hub 只信任代理给的 `X-Forwarded-For`。代理必须保留原 Host、转发 WebSocket，配置示例见 [hub.md](hub.md) 的“部署与反向代理”。

### Linux：装成系统服务

普通用户绑不了 443/80，hub 也不该以 root 跑：系统单元以普通用户运行，只多给一项 `CAP_NET_BIND_SERVICE`（`AmbientCapabilities=`），整个文件系统只读、只有数据目录可写。

```sh
curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | sudo sh    # 装到 /usr/local/bin（见“安装”）
sudo /usr/local/bin/railse setup autostart --hub --system -- --acme-domain hub.example.com --acme-email me@example.com
railse hub password --dir /var/lib/railse-hub     # 不加 sudo：以服务用户（默认就是执行 sudo 的你）运行
railse hub token --dir /var/lib/railse-hub
```

- 写 `/etc/systemd/system/railse-hub.service` 并 `daemon-reload`、`enable`、`restart`。`--system` 只给 `--hub` 用，只在 Linux 上有效；需要 systemd 240 以上（`Type=exec`）。
- `User=` 默认取 `$SUDO_USER`，用 `--user 名字` 指定别的普通用户；root 与全数字的名字一律拒绝。用了别的用户时，后两条命令以那个用户身份跑：`sudo -u 名字 /usr/local/bin/railse hub password --dir /var/lib/railse-hub`。
- 数据目录默认 `/var/lib/railse-hub`，由 systemd 按 `StateDirectory=railse-hub` 在服务第一次启动时建好（0700）并交给服务用户，所以 `password`、`token` 要在 `setup autostart` 之后跑，而且都要带 `--dir /var/lib/railse-hub`：不带时会在服务用户的家目录里多建一个 `~/.railse-hub`，在跑的 hub 用不到它，`railse doctor` 默认也会改看它、不再回退到 `/var/lib/railse-hub`（多出来的删掉即可，或给 doctor 加 `--hub-dir /var/lib/railse-hub`）。显式给 `--dir` 时，目录要么已经属于服务用户（不替你 chown，只收紧为 0700），要么还不存在、由这次新建并交给服务用户：这要求它已有的那段祖先都属于 root 且别人不能写（如 `/srv/railse-hub`）。祖先不满足（例如在家目录下）时，先以服务用户身份建好再跑：`sudo -u 名字 mkdir -p -m 700 /home/名字/railse-hub`。
- 单元里的 `ExecStart` 是 railse 的绝对路径（`--bin`，默认就是这次运行的程序），服务用户必须能读、能执行它。`/usr/local/bin/railse`（root 属主、0755）任何服务用户都能执行。railse 装在别处时写 `sudo "$(command -v railse)" setup autostart --hub --system -- --acme-domain hub.example.com --acme-email me@example.com`（`--bin` 随之取这个路径），但要确认服务用户执行得了它：程序在你的家目录（常是 0750）下而 `--user` 换成了别的用户时，服务用户执行不了，单元会以 `203/EXEC` 反复重启。
- 日志：`journalctl -u railse-hub`，数据目录里还有 `hub.log`。状态：`railse setup autostart --hub --system --status`（不用 sudo；或 `systemctl status railse-hub`）。卸载：`sudo /usr/local/bin/railse setup autostart --hub --system --uninstall`（数据目录保留）。

## 工作机

在终端里跑一次（钩子默认装用户级，在哪个目录里跑都行）：

```sh
railse setup                                                     # 问答式：先问 hub 在哪里，已有 hub 的再问地址、接入码、名字
railse setup all --hub https://hub.example.com --token <接入码>   # 或者一次给全
```

**问答式向导。** 不带子命令的 `railse setup` 在没接入过（没有 `machine.json`）时先问 hub 在哪里：`1` 本机（照 `railse setup all --local-hub` 做，不问地址与接入码），`2` 已有 hub；找得到 `tailscale` 时直接回车选 `1`，否则选 `2`，答别的说明后再问。答 `2` 的，或 `railse setup all` 要接入却没给 `--hub`、`--token` 时，stdin 是终端就逐个问缺的：hub 地址（与 `--hub` 同样校验；非本机的 `http://` 也不收，确在可信内网的用 `--hub` 给出）、接入码（在 hub 机器上运行 `railse hub token` 生成；输入会回显）、本机名字（直接回车用主机名；给了 `--name` 不问）。答得不合格就说明原因再问；Ctrl-D 取消，什么都不做（退出 1）。答完照 `setup all` 做下面四步。已接入过（有 `machine.json`、没加 `--force`）时不问这些，跳过接入，只在重装之前确认一次（见下面的“重跑”）。stdin 不是终端（如管道、`< /dev/null`）时不问：`railse setup` 打印用法，`setup all` 缺 `--hub`、`--token` 是用法错误，都退出 2，与原来一样排在其余检查之前。拒绝 root、检查 `--bin`、`--project` 与 `--name` 都在提问之前，不会答完才被拒。

依次做四步，任一步失败就停下，说明停在哪一步、哪些已做完，修好后重跑（已接入的会跳过接入）：

1. **接入 hub**（同 `railse agent enroll`）：用接入码换本机长期密钥，写 `~/.railse/machine.json`（0600）。已有 `machine.json` 时跳过，这时可以不给 `--hub`、`--token`；`--force` 重新接入。
2. **装钩子**（同 `railse setup hooks --dir <目录>`，给了 `--bin`、`--project` 时带上它们）：三个 CLI 都装，默认用户级、对所有项目生效（见下面的“各 CLI 的钩子”）。接着接 Codex 的 `notify`（同 `railse setup codex-notify --dir <目录>`，给了 `--bin` 时带上它）：已有的 `notify` 保留为前一跳，重跑不重复串接。`notify` 没接上（如 `~/.codex/config.toml` 看不懂）不让 `setup all` 失败：原因照常打印，最后的“注意”里给处理好后要跑的完整命令。
3. **开机自启**（同 `railse setup autostart`）：托管守护进程并等它应答。已经有手动 `railse agent run` 起的守护进程占着 socket 时跳过托管并提示：先在它的终端里 Ctrl-C 停掉，再 `railse setup autostart`。
4. **体检**（同 `railse doctor`，给了 `--project` 时带上它）：不给 `--project` 时 doctor 看当前目录，它不是 git 仓库（如在 `~/Downloads` 里跑）或在系统目录里时项目根一项只告知（skip），不给修复提示。有失败项时照常展示，但不让 `setup all` 失败（刚托管的守护进程可能还没起来），按提示处理后再跑 `railse doctor` 复查。

做完后把手机要打开的 hub 地址（守护进程实际接的那个）用框线围起来打印，并提示：手机 Safari 打开后分享→添加到主屏幕，再从图标打开；登录后在设置里开启通知（见 [web.md](web.md)）。

其余标志：`--name 名字`（在 hub 里的名字，默认主机名）、`--project 目录`（给了才把 Claude、Gemini 的钩子装成那个项目的项目级，默认装用户级；目录要已存在）、`--dir 目录`（守护进程数据目录，默认 `$RAILSE_HOME` 或 `~/.railse`）、`--bin 路径`、`--no-hooks`（不装钩子，也不接 Codex 的 `notify`）、`--no-autostart`（只管守护进程的开机自启；`--local-hub` 的本机 hub 总是托管，不然没人起它）。`setup all` 要用平时的用户身份跑，以 root 运行直接拒绝（否则会用掉接入码，`machine.json` 与钩子配置也成了 root 的文件）。

**重跑。** 已接入过的机器（有 `machine.json`、没加 `--force`）在终端里重跑 `railse setup` 或 `railse setup all`，第 1 步说完「已接入过…跳过」后先问一次：

```text
本机已接入 https://hub.example.com。接下来重装钩子（连同 Codex 的 notify）、重启守护进程，继续吗？[Y/n]
```

直接回车或 `y`、`yes` 继续；`n`、`no` 或 Ctrl-D 打印「已取消，钩子与守护进程都没动」，退出 1；别的答案说明怎么答再问。问句只提这次真要做的：托管的守护进程没在跑时是「托管并启动守护进程」；`--no-hooks` 时只提守护进程，`--no-autostart` 时只提钩子，取消时的说明也一样；手动 `railse agent run` 起的守护进程占着 socket 时第 3 步不碰它，只提钩子。没有要重做的（如 `--no-hooks` 加 `--no-autostart`）时不问。首次接入、`--force` 重新接入、stdin 不是终端（脚本、管道、`< /dev/null`）时都不问，直接做。

接的是 `setup all --local-hub` 装的本机 hub 时（hub 数据目录在默认位置，即 `$RAILSE_HUB_HOME` 或 `~/.railse-hub` 时，不带 `--local-hub` 重跑也认得出），第 1 步“本机 hub”会复查 Funnel、可能重新托管 hub，所以这一问挪到第 1 步之前，问句里是手机打开的对外地址（不是 `127.0.0.1`），要做的事前面多一项：

```text
本机已接入 https://<机器名>.<tailnet>.ts.net。接下来检查本机 hub（必要时重新托管）、重装钩子（连同 Codex 的 notify）、重启守护进程，继续吗？[Y/n]
```

答 `n` 什么都不动（不碰 Tailscale、不托管、不装钩子），打印「已取消，本机 hub、钩子与守护进程都没动」，退出 1。

**数据目录不是 `~/.railse` 时。** 托管的守护进程带着 `RAILSE_HOME=<目录>` 跑，CLI 起钩子时的环境里却不一定有它（只在跑 setup 的那条命令上设的 `RAILSE_HOME` 也进不去），不带 `--socket` 的钩子就会去连 `~/.railse/agent.sock`，请求全都交回终端、进不了 hub。所以守护进程的数据目录（`--dir`，没给时取 `$RAILSE_HOME`）不是 `~/.railse` 时，`setup all` 与 `setup hooks` 在钩子命令里写上 `--socket <目录>/agent.sock`（`setup codex-notify` 生成的脚本里 railse 那一行也带上）；是 `~/.railse` 时命令不变。以前换过数据目录、钩子却是按 `~/.railse` 装的，`railse doctor --dir <目录>` 的 `hooks.*` 会报 warn（doctor 把不带 `--socket` 的钩子一律当作连 `~/.railse/agent.sock`），照提示重跑 `railse setup hooks --dir <目录>`，Codex 的 `notify` 用 `railse setup codex-notify --dir <目录>` 重新生成。

**各 CLI 的钩子。** 默认都装用户级，对所有项目生效，装一次就够：合并时只动 `hooks` 里 Railse 自己的条目，别的设置与别人的钩子原样保留。用户级的 `~/.claude/settings.json`、`~/.gemini/settings.json` 里多是你自己的设置，`setup hooks` 第一次改它们之前各备份一次到同目录的 `<文件>.railse-bak-<时间>`（0600；是符号链接时备份链接指向的内容，备份放在链接旁边；原来不存在、是空的、这次内容没变或已有任何备份时不备份），打印“已备份 … 到 …”；`--dry-run` 不备份，只说会备份到哪里；备份失败就不改那个文件、退出 1。只想让某个项目用 Railse 的，给 `--project <目录>` 把 Claude、Gemini 的钩子装成那个项目的项目级（下面写的项目级位置，不备份；项目里的文件就是用户级那份时，如 `--project ~` 的 Gemini，照样按用户级备份）。用户级与项目级都装了：Gemini 的钩子会触发两次（`railse doctor` 报 warn）；Claude Code 对写在多个配置文件里的相同处理器只跑一次，两处 Railse 的条目完全相同时不重复（doctor 只在细节里说可以删掉项目级的），不同（如程序路径、`--socket` 不同）时也会触发两次（warn）。装用户级时，当前目录的项目级文件（`.claude/settings.local.json`、`.claude/settings.json`、`.gemini/settings.json`；在家目录里跑时与用户级是同一个文件的不算）里有 Railse 的条目，`setup hooks` 在 stderr 逐个文件提醒一行：Gemini 与条目不同的 Claude 建议删掉其中 Railse 的条目，Claude 两边完全相同的说明只跑一次、可删可不删；项目里的文件不改。

- **Claude Code**：用户级写 `~/.claude/settings.json`。`--project` 时写 `<项目>/.claude/settings.local.json`（钩子命令里是本机的 railse 路径，不该入库）；旧版写在 `.claude/settings.json` 的 Railse 条目会自动迁走，别的设置原样保留；`--project` 是家目录（或项目的 `settings.json` 链到 `~/.claude/settings.json`）时不迁：那是用户级配置，迁走会让所有项目失去钩子，只警告，要迁请手动删。railse 新建的 `settings.local.json` 不一定被 git 忽略，`git status` 里看到它就加进 `.gitignore`（或全局的 `~/.config/git/ignore`）：`echo '.claude/settings.local.json' >> .gitignore`。
- **Codex CLI**：写用户级 `~/.codex/hooks.json`；只在交互会话里审批（`codex exec` 不触发）；首次使用在 Codex 里用 `/hooks` 审核。完成与提问事件走 `~/.codex/config.toml` 顶层的 `notify`，Codex 只认一条：运行 `railse setup codex-notify [--dir 目录] [--bin 路径] [--dry-run]`（`setup all` 装完钩子会自动做这一步，单独跑 `setup hooks` 时会提示），它生成 `<数据目录>/codex-notify.sh`（0755；数据目录默认 `$RAILSE_HOME` 或 `~/.railse`，不存在时按 0700 建），脚本把 Codex 给的参数（通知 JSON 是最后一个）原样依次交给原来的 `notify` 与 `railse hook codex-notify`（数据目录不是 `~/.railse` 时带 `--socket`），一个失败不影响另一个；然后把 `config.toml` 顶层的 `notify` 换成 `["<数据目录>/codex-notify.sh"]`，原来没有就在文件最前面插一行，其余内容逐字节不变。原来的 `notify` 是别的命令时原样保留为前一跳（记在脚本的标记行 `# railse-prev-notify: [...]` 里）；是直接调 railse 的旧写法 `[".../railse", "hook", "codex-notify", ...]` 时换掉，不串两个 railse；已经是这个脚本（或别的数据目录里生成的 `codex-notify.sh`）时沿用它记的前一跳，所以重跑、换 `--dir` 或 `--bin` 重跑都不会重复串接，原有的 `notify` 照样保留；那个脚本读不到（如旧的数据目录删了）时不串前一跳，警告去备份里找回原来的 `notify`；前一跳里有这个脚本自己（如 `["/bin/sh", "<脚本>"]`）时也不串并警告。认不出的别名（大小写不同的路径、硬链接、firmlink、相对路径）经前一跳又调到脚本自己时，由脚本的重入保护挡住：脚本在标记行之后设上并导出 `RAILSE_CODEX_NOTIFY=1`，发现它已设就直接退出，所以不会一层层嵌套下去，railse 也只被调一次（这个变量会传给前一跳与 railse）。每次真的改 `config.toml` 之前备份到 `config.toml.railse-bak-<时间>`（同一秒里再改一次时名字后面加 `-2`、`-3`…；0600，是符号链接时备份链接指向的内容、写到链接指向的文件、沿用原权限），内容没变时不写不备份、打印“已是最新”；`config.toml` 原来不存在时新建 0600。只在某个 `[表]` 下面有 `notify`（Codex 读不到）时照样在最前面插顶层的，并警告那个要手动删；`notify` 的写法看不懂（多行字符串、不是字符串的元素、数组没闭合等）时退出 1、什么都不写，请手动改。`--dry-run` 只打印脚本与改写后的 `config.toml`。
- **Gemini CLI**：用户级写 `~/.gemini/settings.json`，`--project` 时写 `<项目>/.gemini/settings.json`；需要 0.65.0 以上，不要开 yolo 模式。Gemini 的钩子只能拒绝不能放行：规则拒绝的照常自动拒绝，其余（规则放行的与定不了的）都交回终端，由 Gemini 自己的确认框处理，永远不进 hub 与手机。

**单独用子命令。**

```sh
railse agent enroll --hub https://hub.example.com --token <接入码> [--name 名字] [--dir 目录] [--force]
railse setup hooks [--project 目录] [--claude] [--codex] [--gemini] [--codex-file 路径] [--dir 目录] [--bin 路径] [--dry-run]
railse setup autostart [--hub] [--uninstall] [--status] [--dir 目录] [--bin 路径] [--system] [--user 名字] [--dry-run] [-- hub run 的额外参数]
railse setup codex-notify [--dir 目录] [--bin 路径] [--dry-run]
```

`enroll` 之后要重启守护进程才接上 hub（它只在启动时读 `machine.json`）：托管的重跑 `railse setup autostart`，前台跑的停掉后重新 `railse agent run`。`setup hooks` 不带 CLI 开关时三个都装，Claude、Gemini 默认写用户级、给了 `--project` 才写项目级（见上面的“各 CLI 的钩子”），`--dry-run` 只打印不写，重复执行只替换 Railse 自己的条目；`--dir` 是守护进程的数据目录（默认 `$RAILSE_HOME` 或 `~/.railse`），不是 `~/.railse` 时钩子命令带 `--socket`（见上面的“数据目录不是 `~/.railse` 时”）。`setup autostart` 的 `--dry-run` 只打印要写的文件与要跑的命令。

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

只读：不改配置、不建文件，数据库按只读打开；网络只访问 hub 的 `GET /api/version`（不带凭据；`machine.json` 里的地址，本机 hub 还有它的对外地址）；会起的进程只有 `claude`、`codex`、`gemini` 的 `--version`（各 10 秒超时）与本机 hub 由 Tailscale 提供时的 `tailscale serve status --json`（15 秒超时），sudo 下（euid 是 root，或家目录不归当前用户）都不跑，`cli.*` 与 `hubsrv.local` 报 skip。每项结果是 `ok`、`warn`、`fail`、`skip` 之一，warn 与 fail 下一行给“修复：”；有任一项 fail 退出码 1；`--json` 输出机器可读的结果（ID 固定不变）。

hub 侧：数据目录默认 `$RAILSE_HUB_HOME`，否则 `~/.railse-hub`；Linux 上两者都没有时再看 `/var/lib/railse-hub`（系统单元的），但只在它归当前用户（即服务用户）或以 root 跑 doctor 时才看，属于别的用户时 hub 侧整段 skip 并说明（换那个用户跑，或给 `--hub-dir`）。默认目录都不在就跳过 hub 侧（Linux 上 skip 的说明列出查过的两个位置）；显式给的 `--hub-dir` 不存在时报 fail。系统单元的 hub 要以服务用户身份检查（`railse doctor`，或 `sudo -u 名字 /usr/local/bin/railse doctor --hub-dir /var/lib/railse-hub`），别的用户读不到 `hub.db`；服务用户的家目录里另有 `~/.railse-hub` 时，doctor 默认看的是它，这时显式给 `--hub-dir`。

| ID | 名称（输出里的） | 看什么 | 不过时 |
| --- | --- | --- | --- |
| `agent.dir` | 守护进程数据目录 | 目录在、是 0700 | 先 `railse agent run` 建好；权限不对按提示 `chmod 700` |
| `agent.files` | 配置与审计文件权限 | `rules.yaml`、`machine.json`、`audit.jsonl`、`state.json`（存在的）都是 0600 | 按提示 `chmod 600` |
| `agent.socket` | socket 文件 | `agent.sock` 在、是 socket、0600 | 守护进程没在跑：`railse agent run` 或 `railse setup autostart`；不是 socket 就移走它再重启 |
| `agent.running` | 守护进程在跑 | 守护进程应答 status | 同上 |
| `agent.hub` | hub 连接 | 守护进程连 hub 的状态：`connected` 为 ok，`connecting`、`none`（未接入）为 warn，`disabled`（连续 3 次被 401 拒绝）为 fail | `connecting` 看 `agent.log`；`none` 而 `machine.json` 已在：接入后还没重启守护进程，按提示重启（不用再接入）；`disabled` 要 `railse agent enroll --force` 后重启守护进程；没有报状态的是升级前起的旧进程，重启它 |
| `agent.autostart` | 守护进程开机自启 | 有没有守护进程的托管文件（只看文件，不问 launchd/systemd） | `railse setup autostart`（warn） |
| `hub.reachable` | hub 可达 | `machine.json` 里 hub 的 `/api/version` 答得上、协议号与本机一致；没接入时 skip | 连不上：看 hub 在不在跑、地址、证书；不是 200：hub 太旧或地址被重定向；协议不一致：升级较旧的一方 |
| `hooks.claude` | Claude Code 钩子 | 用户级 `~/.claude/settings.json` 或项目级（`--project`，默认当前目录）的 `.claude/settings.local.json`（含旧位置 `.claude/settings.json`）任一装了就算装了，按装了的那份查：`PermissionRequest`、`Notification`、`Stop`、`SessionEnd` 四个事件都在，`PermissionRequest` 超时不低于 86400 秒；程序路径不是当前程序时 warn；钩子连的 socket（命令里的 `--socket`，没有就按 `~/.railse/agent.sock` 算）不是 `--dir` 那个守护进程的时 warn；用户级与项目级都装了按用户级查：两处 Railse 的条目（整条连同 matcher）完全相同时 Claude Code 只跑一次，不降级、细节里说可以删掉项目级的，不同时会触发两次、至少 warn；项目级的旧位置 `settings.json` 与 `settings.local.json` 都装了同理；同一个文件（项目就是家目录、链到用户级文件）不算两处；都没装时 fail，列出看过的位置 | 都没装、缺了或超时不够：`railse setup hooks --claude`（只装在项目级的，提示带 `--project <目录>`；守护进程不在 `~/.railse` 时带 `--dir`）；用户级与项目级都装了、条目不同（会触发两次）：删掉项目级文件里 Railse 的条目（用户级已对所有项目生效）；项目级只在旧位置 `settings.json`，或它与 `settings.local.json` 都装了、条目不同（会触发两次）：`railse setup hooks --project <目录> --claude` 迁走（warn）；用户级文件不是合法 JSON：修好再装；都没装而项目级文件不是合法 JSON：修好它再 `railse setup hooks --project <目录> --claude`；socket 对不上：带 `--dir` 重装，或是 doctor 自己少给了 `--dir` |
| `hooks.codex` | Codex 钩子 | `~/.codex/hooks.json` 里 `PermissionRequest`、`Stop`、`SessionEnd` 三个事件都在，`PermissionRequest` 超时不低于 86400 秒；socket 同上 | `railse setup hooks --codex` |
| `hooks.codex_notify` | Codex notify | `~/.codex/config.toml` 顶层（第一个 `[表]` 之前）的 `notify`：是 `setup codex-notify` 写的脚本（`["…/codex-notify.sh"]`）时，脚本要在、可执行、里面有 railse 的 `hook codex-notify` 那一行；或是直接调 railse 的旧写法 `[..., "hook", "codex-notify"]`。railse 带 `--socket <路径>` 时按它、不带时按 `~/.railse/agent.sock` 与 `--dir` 比对 | `config.toml` 不存在、没有 `notify`、只写在某个表下面、不调 railse、脚本不在或不可执行：`railse setup codex-notify`（守护进程不在 `~/.railse` 时带 `--dir`；会保留已有的 `notify`）；socket 对不上：带 `--dir` 重跑它，或是 doctor 自己少给了 `--dir`；写法看不懂：手动改好再跑；`config.toml` 读不到（权限等）：用跑 Codex 的同一个用户运行 doctor（都是 warn） |
| `hooks.gemini` | Gemini 钩子 | 用户级 `~/.gemini/settings.json` 或项目级 `.gemini/settings.json` 任一装了就算装了（用户级、项目级的规则同 `hooks.claude`；两处都装时不管条目同不同都按会触发两次报 warn，Gemini 的文档没说去重）：`BeforeTool`、`Notification`、`AfterAgent`、`SessionEnd` 四个事件都在，`BeforeTool` 超时不低于 86400000 毫秒；socket 同上 | `railse setup hooks --gemini`（只装在项目级的带 `--project <目录>`）；两处都装了：删掉项目级文件里 Railse 的条目 |
| `project.root` | 项目根 | `--project`（默认当前目录）会被守护进程当成哪个项目根（决定哪些写入算项目内）：在系统目录之外的 git 仓库里 ok，细节是仓库根；在系统目录里（`/opt`、`/usr`、`/etc`、`/var`、`/System`、`/Library`、`/private` 和它们下面，第一段以点开头的路径也算；`/private/tmp`、归你且权限 700 的临时目录 `$TMPDIR`、家目录除外，细则见 [agent-local-loop.md](agent-local-loop.md) 的“项目根”）时没有项目根，是 git 仓库（如 `/opt/homebrew`）也一样，细节“目录 X 在系统目录里，不按项目根处理（是 git 仓库也一样）”；不是 git 仓库、是家目录下的普通目录（不在点开头的目录或 `~/Library` 之下）时按目录本身当项目根；不是 git 仓库、也不是家目录下的普通目录（如 `/tmp` 下、`~/.config/x`、`~/Library/LaunchAgents`）时没有项目根，细节“目录 X 不在家目录下的普通目录里，不按项目根处理”。这三种情况显式给了 `--project` 的 warn，不给时（看的是当前目录）只告知，skip。是家目录、它的上级、根目录或不存在时没有项目根，在这里跑 agent 时所有写入都算项目外，skip | 显式给的按目录本身当项目根：建议在那个目录里 `git init`；显式给的不是普通目录：要自动放行请 `git init` 或换到项目目录；显式给的在系统目录里：要自动放行请换到系统目录之外的项目目录（都是 warn） |
| `cli.claude` | Claude Code 版本 | 版本是否与录制钩子样本时（2.1.280）一致；不在 PATH 里 skip | 不同时 warn：试一次需要审批的操作，确认请求经过 Railse |
| `cli.codex` | Codex CLI 版本 | 版本是否与录制钩子样本时（0.145.0）一致；不在 PATH 里 skip | 同上 |
| `cli.gemini` | Gemini CLI 版本 | 版本不低于 0.65.0（更早的版本钩子在确认之后才触发）；不在 PATH 里 skip | 升级 Gemini CLI（fail） |
| `hubsrv.dir` | hub 数据目录 | 目录在、是 0700 | 显式 `--hub-dir` 写错了就改对；不跑 hub 的机器去掉它 |
| `hubsrv.db` | hub 数据库 | `hub.db` 是 0600，库版本与程序一致 | 库比程序新（fail）：升级 railse；库旧（warn）：下次 `hub run` 自动升级，先备份；被锁：稍后重跑；hub 异常退出留下未恢复的 WAL（warn）：启动一次 `railse hub run`；真损坏才从备份恢复 |
| `hubsrv.password` | 网页密码 | 设过网页密码 | `railse hub password`（warn） |
| `hubsrv.vapid` | 推送密钥 | 推送密钥在、解得开 | 还没有（warn）：首次 `railse hub run` 会生成；损坏（fail）：`railse hub vapid --rotate`，所有手机重新开启通知 |
| `hubsrv.acme` | ACME 证书目录 | `acme/` 是 0700、里面的文件 0600；没有这个目录（没用 ACME）时 skip | 按提示 `chmod` |
| `hubsrv.local` | 本机 hub 对外地址 | 读 hub 数据目录里 `setup all --local-hub` 写的 `local-hub.json`（没有时 skip）：Tailscale 提供的先看 `tailscale serve status --json` 里 Funnel 是否把 `<机器名>:443` 转到 `127.0.0.1:<端口>`（sudo 下整项 skip）；不论哪种都从本机 GET 一次 `<对外地址>/api/version`，答得上是 hub 为 ok。端口上的 hub 本身由 `hub.reachable` 看 | `local-hub.json` 坏了（只说哪一项不合规，不回显内容）：重跑 `railse setup all --local-hub --hub-dir <目录>`（守护进程不在 `~/.railse` 时带 `--dir`，用自己隧道的再带 `--public-url`）；找不到 tailscale、Funnel 没转到这个端口（fail）：装好并登录 Tailscale，`tailscale funnel --bg <端口>` 或重跑同一条 setup；从本机访问不到（warn）：常是本机代理拦着 `*.ts.net`，用手机 4G 打开验证，Funnel 入口 TLS 握手失败时先关掉代理再试 |
| `hubsrv.autostart` | hub 开机自启 | 有没有 hub 的托管文件（plist、用户单元或系统单元） | `railse setup autostart --hub`（warn） |

## 升级

1. 重跑安装脚本（与当初同样的方式：Linux 的 hub 机器用 `| sudo sh`）。
2. 重启守护进程：托管的重跑 `railse setup autostart`（当初给过 `--dir`、`--bin` 的照样带上；macOS 会重新 bootout、bootstrap，Linux 会 `daemon-reload` 并 `restart`）；前台跑的停掉后重新 `railse agent run`。也可以重跑 `railse setup`（当初给过 `--dir`、`--bin`、`--project` 的改用 `railse setup all` 带上它们）：跳过接入，重装钩子、接 Codex 的 `notify`、重装托管并重启守护进程，再体检；在终端里动手之前会先确认一次。
3. 重启 hub：系统单元 `sudo systemctl restart railse-hub`，Linux 用户单元 `systemctl --user restart railse-hub`，macOS `launchctl kickstart -k gui/$(id -u)/dev.railse.hub`。也可以重跑 `railse setup autostart --hub`，但要带上当初 `--` 之后的全部参数，否则托管文件会被改写成不带它们的。
4. hub 与各工作机升级到同一版本：协议号不一致时 hub 拒绝守护进程的握手。升级后在工作机上跑 `railse doctor` 确认 `hub.reachable` 与 `agent.hub` 都是 ok。
5. 从 0.1.0 升级：0.1.0 的 Claude、Gemini 钩子只能用 `--project` 装成项目级。不打算改的什么都不用做。想改成用户级（对所有项目生效）的，跑完 `railse setup hooks`（或 `railse setup`）后，在每个用 `--project` 装过的项目里删掉 `.claude/settings.local.json`、`.gemini/settings.json` 里 Railse 的条目（在项目里跑 `railse doctor` 会逐个指出；在项目目录里跑 `setup hooks` 时它也会在 stderr 提醒）。不删也能用，但 Gemini 的钩子会触发两次，Claude 两处的条目不同（如换过程序路径或 `--dir`）时也会两次。

## 发布（维护者）

1. `main` 上的 CI（`go`、`install-script`、`web`、`e2e`、`cross` 五个作业：Go 竞态测试与 lint、安装脚本测试、网页检查、Playwright、三平台交叉编译）是绿的；本地 `make test`、`make e2e`、`make release-check`、`scripts/test-install.sh` 都过，再手动跑一次 `scripts/smoke-claude-hub.sh`（真实 Claude Code 经 hub 审批，要已登录，会消耗额度）。想先看一眼包：`go run github.com/goreleaser/goreleaser/v2@v2.19.0 release --snapshot --clean --skip=publish`（产物在 `dist/`，不入库；goreleaser v2.19.0 要 Go ≥ 1.27.3 才编得出来，本机 Go 更旧时靠默认的 `GOTOOLCHAIN=auto` 自动取，同 `make release-check`）。
2. 打 tag 并推送，release 工作流（它不等 CI，所以第 1 步要先确认）用 Go 1.26 系列的最新补丁版与 goreleaser v2.19.0 出包并建 GitHub Release：
   ```sh
   git tag v0.1.0 && git push origin v0.1.0
   ```
3. 产物是 `railse_<版本>_darwin_arm64.tar.gz`、`railse_<版本>_linux_amd64.tar.gz`、`railse_<版本>_linux_arm64.tar.gz`（各含 `railse`、`LICENSE`、`README.md`、`README.en.md` 与 `docs/*.md`）和 `checksums.txt`；版本号取自 tag（去掉 `v`），经 `-X main.version` 注入。带后缀的 tag（如 `v0.2.0-rc.1`）发成预发布，安装脚本默认的“最新正式版”不会取到它。发布后在一台干净的机器上跑一行安装，确认打印出新版本。
