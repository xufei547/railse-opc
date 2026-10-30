# hub：请求、规则、机器与审计的中心

## 是什么

`railse hub` 跑在云服务器上，是请求、规则、机器与审计的唯一真相。各机器的守护进程只向外连它（`/ws/agent`），手机网页（PWA，由 hub 同源托管，见“网页”与 [web.md](web.md)）也连它（REST 与 `/ws/client`），外部系统（如 n8n）经 webhook 往收件箱里提请求。hub 对每条请求只能回答放行（allow）或拒绝（deny），没有任何向机器下发命令的通道：发给守护进程的帧只有 welcome、decision、rules、paused、ping、pong、error 七种。

hub 还托管手机网页（见“网页”），并能把新请求推到手机（Web Push，见“手机推送”）；TLS 可由 hub 内置的 ACME（Let's Encrypt）直出，也可交给反向代理（见“部署与反向代理”）；开机自启用 `railse setup autostart --hub`，安装、托管与体检的完整步骤见 [setup.md](setup.md)。

## 启动

```bash
railse hub password        # 从标准输入读一行设为网页登录密码（至少 8 个字符），终端下会回显
read -s PW; printf '%s\n' "$PW" | railse hub password; unset PW   # 不回显的写法
railse hub run --listen 127.0.0.1:8787 --public-url https://hub.example.com
```

- 数据目录取 `--dir`，否则 `$RAILSE_HUB_HOME`，否则 `~/.railse-hub`；目录 0700，`hub.db`（SQLite，WAL 模式，`-wal`、`-shm` 沿用 0600；开 `secure_delete`，删改掉的内容如清空的回调令牌用零覆盖）与 `hub.log` 都是 0600，已有的文件启动时收紧。
- `railse hub run` 的标志：`--listen`（默认 `127.0.0.1:8787`）、`--secure-cookies`（cookie 加 Secure，hub 在 TLS 反向代理后面而没设 `--public-url` 时打开；设了 https 的 `--public-url` 已自动加 Secure）、`--trusted-proxy <CIDR 或 IP>`（可重复或逗号分隔，见“部署与反向代理”）、`--public-url https://host[:port]`（hub 对外的 https 源，设了才启用手机推送，见“手机推送”）、`--foreground`（日志同时写到 stderr）、`--acme-domain`、`--acme-email`、`--acme-http`（hub 自己申请证书直出 TLS，见“部署与反向代理”的 A）。子命令不认的标志会报错退出 2；`--public-url` 不是 https 源（`http://`、带路径或结尾的 `/`、查询、片段、用户信息）也退出 2，localhost 也要 https。
- 没设密码也能启动，日志提示“还没有设置网页密码”，网页登录回 503。换密码会让所有已登录的网页立即失效，并在同一事务里删除全部推送订阅（打印删了几条；各设备重新登录后要在设置里重新开启通知），见“安全要点”的“退出登录与设备丢失”。
- 收到 SIGINT、SIGTERM 后 5 秒内退出：断开守护进程、等在途请求、长连接处理、webhook 回调与推送收尾（正在发的推送中止，不计失败）；等不到它们退出时（含被强制断开、处理还没结束的普通请求）退出码 1，日志写“不关数据库”。
- `railse hub webhook-token` 生成 webhook 来源令牌，明文只显示这一次，再生成一次旧的就失效。
- `railse hub vapid [--rotate]` 打印推送公钥（库里没有就生成，hub 第一次启动时也会生成）；`--rotate` 换新密钥并在同一事务里删除全部推送订阅，提示所有手机要重新开启通知。在跑的 hub 不用重启：下一次推送、下一次网页取公钥时就从库里读到新密钥。
- **开机自启。** `railse setup autostart --hub -- <hub run 的参数>` 用 launchd（macOS）或 systemd 用户单元（Linux）托管 hub，退出后自动拉起。Linux 上直出 443/80 时装成系统单元，以普通用户运行、只给 `CAP_NET_BIND_SERVICE`，数据目录默认 `/var/lib/railse-hub`：`sudo /usr/local/bin/railse setup autostart --hub --system -- --acme-domain hub.example.com --acme-email me@example.com`（railse 先用 `curl … | sudo sh` 装进 `/usr/local/bin`，服务用户才执行得了；sudo 时写绝对路径，RHEL 系的 sudo 默认不在 `/usr/local/bin` 里找命令，见 [setup.md](setup.md)）。`--status` 看是否在跑，`--uninstall` 移除托管（数据目录保留），`--dry-run` 只打印。
- **体检。** `railse doctor --hub-dir <数据目录>`（只读）检查数据目录与 `hub.db` 的权限、库版本、网页密码、推送密钥、`acme/` 的权限与托管文件；不给 `--hub-dir` 时看 `$RAILSE_HUB_HOME`、`~/.railse-hub`，Linux 上再看 `/var/lib/railse-hub`。以跑 hub 的用户身份运行。各检查项见 [setup.md](setup.md) 的“railse doctor”。

## 网页

hub 同源托管手机网页（PWA）：浏览器打开 `https://<hub 域名>/` 就是收件箱，用 `railse hub password` 设的密码登录。页面、通知与开发方式见 [web.md](web.md)。

- **构建。** 网页的构建产物嵌在二进制里：在仓库里 `make web` 构建网页（产物进 `internal/hub/webui/dist/`，不入库），`make build` 先构建网页再编译 `./railse`。直接 `go build` 出来的二进制没有网页：网页路径回 503 与提示“网页未构建：在仓库里运行 make web 后重新构建 railse”，接口照常可用。
- **路由。** `/api/`、`/ws/` 之外的路径都归网页：是构建产物里的文件就原样返回（`assets/` 下带哈希的长期缓存，其余 no-cache），别的路径（`/`、`/r/<id>`、`/rules` 等）回 `index.html`，由网页自己路由。HTML 带 CSP（只许同源，不许内联脚本与样式）与 `X-Frame-Options: DENY`。网页和接口同源，不用配置 CORS；hub 不回 CORS 头、写接口校验同源，别的源的网页调不了接口。
- **推送。** 要手机推送须加 `--public-url https://<hub 域名>`（见“手机推送”）；订阅与主屏幕应用都绑在这个源上，域名从第一天起就定下，不要再换。

## 接入一台机器

1. hub 上：`railse hub token [--ttl 10m]` 打印一次性接入码，默认 10 分钟有效，`--ttl` 至多 24 小时，用一次即作废。
2. **先导入本机规则**（本地闭环时手写过 `~/.railse/rules.yaml` 的才需要）。首次连上 hub 时，hub 的规则会整套覆盖本机 `rules.yaml`，其中手写的规则（例如禁止改 `migrations/**` 的 deny）随之失效。导入是整套替换 hub 的规则，第一次接入时 hub 里只有两条内置规则，直接导入本机文件即可（多台机器各有手写规则时先合并成一份）：

   ```bash
   CK=$(mktemp)   # 0600 的临时文件，放登录会话 cookie，别人读不到
   read -s PW; printf '{"password":"%s"}' "$PW" | curl -sf -c "$CK" -H 'Content-Type: application/json' --data-binary @- https://hub.example.com/api/login; unset PW
   curl -sf -b "$CK" -H 'Content-Type: application/yaml' --data-binary @"$HOME/.railse/rules.yaml" https://hub.example.com/api/rules/import
   rm -f "$CK"
   ```

   （密码里有 `"` 或 `\` 时要按 JSON 转义；有 jq 时可用 `jq -n --arg p "$PW" '{password:$p}'` 生成请求体。）

   **限定机器的规则要先改写。** 本地闭环时本机的机器 id 是主机名，接入后换成 hub 分配的 id（第 3 步 enroll 输出里的“机器 id”）。本机规则里写着 `scope: {machine: <主机名>}` 的，导入前要把它改成这个 id，否则接入后不再命中（用户 deny 会失效）。这种情况先做第 3 步拿到 id，改写后导入，再做第 4 步。

   忘了也不会丢：守护进程第一次用 hub 规则覆盖不是 hub 下发的 `rules.yaml` 之前，先把它原样备份为同目录的 `rules.local-<UTC 时间>.yaml`（0600），`agent.log` 记一条“已备份为 …”，之后把备份文件按上面的方法导入即可。只备份这一次，之后的 `rules.yaml` 都是 hub 的缓存。
3. 机器上：`railse agent enroll --hub https://hub.example.com --token <接入码> [--name 名字] [--force]`。名字默认主机名。`--hub` 只收 `http://`、`https://`，不能带 `user:pass`、`?` 查询或 `#` 片段；接入请求不跟随重定向；`http://` 且不是本机地址时警告明文传输密钥。本机已有不是 hub 下发的 `rules.yaml` 时，成功后提示“首次连上 hub 时本机 rules.yaml 会被 hub 规则覆盖（会自动备份），如有手写规则请先导入”。
4. 重启守护进程（它只在启动时读 `machine.json`）：前台跑的停掉后重新 `railse agent run`，托管的重跑 `railse setup autostart`。日志出现“已连接 hub”即接上。第 3、4 步与装钩子、开机自启可以用 `railse setup all --hub … --token …` 一次做完（见 [setup.md](setup.md)）。

`~/.railse/machine.json`（0600）存 `hub_url`、`machine_id`、`secret`（机器长期密钥）、`name`。已存在时 `enroll` 拒绝覆盖，换 hub 或重新接入要加 `--force`。文件损坏时守护进程拒绝启动（不会悄悄退回本地模式）；要退回本地闭环就删掉它再重启，残留的 hub 暂停随之清除。

吊销：`DELETE /api/machines/{id}`（网页在第 4 份）。密钥立即失效，在线连接被断开，它仍 pending 的请求改为 `cancelled`（`decided_by: machine_revoked`），它的 question 不再列在收件箱里。

## 接入后的行为

- **规则以 hub 为准。** 每次连上，welcome 里的规则快照一律替换本机规则（不比版本：hub 重建库后版本可能变小）；连接期间的 `rules` 帧只收比本机新的版本。本机 `rules.yaml` 只是缓存，文件头写着“# 由 hub 下发，版本 N。本机修改会在下次下发时被覆盖。”；第一次覆盖本机原有的规则文件前先备份为 `rules.local-<UTC 时间>.yaml`（见“接入一台机器”第 2 步）。`railse agent reload`（或 SIGHUP）仍可临时加载本机改动，下一次下发或重连时被覆盖。
- **引擎定不了的请求进 hub 等手机决定**（`outward`、`network`、`unknown` 等没有规则命中的 Claude、Codex 请求）。钩子一直等到手机上的决定、本机期限（约 24 小时）或下面的交回终端。规则能定的照旧在本机立即放行或拒绝，记录异步上报 hub。
- **手机上的备注不会给到 agent。** 钩子只看到固定文案：放行“已在手机上批准”，拒绝时 Claude、Codex 看到“Railse：已在手机上拒绝”。备注只进本机审计的 `note` 字段与 hub 的 `decision_note`、审计。
- **总开关：生效值 = 本机 || hub。** 两边分开存在 `state.json`（`paused`、`hub_paused`）。`railse agent pause`/`resume` 只改本机这一侧，不会被 hub 覆盖，也不反向同步到 hub；hub 的暂停（`POST /api/pause`，下发 paused 帧、写进 welcome）只改 hub 那一侧。`railse agent status` 显示 `paused: true (local)`、`(hub)` 或 `(local+hub)`；本机 resume 后 hub 仍暂停会提示。暂停时只有用户 deny 规则照常生效，其余一律交回终端，不送 hub。
- **连不上 hub 时最多等 HubGrace（30 秒）就交回终端。** 送出之前没连上，等 30 秒仍连不上就交回；已送到 hub 后断线，同样起 30 秒计时，期间重连上就接着等（hub 会补发已做的决定），超时交回。两种情况钩子都是“不作决定”，CLI 弹原生审批框，本机审计记 `state: expired`、`decided_by: hub_unreachable`，随后上报的这条审计让 hub 里的请求也变成 expired。
- **Gemini 的请求永远不进 hub。** Gemini 的钩子只能拒绝不能放行，守护进程直接交回原生确认框（`decided_by: gemini`），只作审计上报；hub 也拒收 Gemini 会话的 pending。
- **连续 3 次握手被 hub 以 401 拒绝（机器被吊销或密钥无效）后**，守护进程停止重连 hub（`railse agent status` 显示 `hub: disabled`），请求一律立即交回终端，日志提示重新接入；要重新 `railse agent enroll --force` 并重启守护进程。前两次 401 照常按退避重连，日志记“回 HTTP 401（第 N/3 次）”，期间状态是 `hub: connecting`。只有成功握手（收到 hub 的 welcome）才重新计数；连不上、5xx、超时、协议版本不一致都不清零，所以 401 与 502 交替时照样会停用，而 hub 重启或代理切换时偶发的一两次 401 只要随后连上过就不累计。按默认退避，吊销后至少约 7 秒才停用（断线后等 1 秒，前两次 401 后再等 2、4 秒；断线前连接没稳定过、或中间夹着连不上时退避更长，停用更晚），启动时就被拒至少约 3 秒。
- **协议版本不一致**时 hub 拒绝握手（Error 帧“协议版本 X，hub 需要 Y”，以 1008 关闭，审计 `agent_proto_mismatch`），守护进程日志提示升级较旧的一方，每 10 分钟再试。
- **断线与重连。** 守护进程重连退避 1、2、4、8、16、30 秒，连接稳定后归零；超过 60 秒收不到 hub 的任何帧就主动断开重连。重连时 hello 带上本机仍在等的请求：hub 补发其中已决定的；hub 里多出来的 pending 改为 `cancelled`（`decided_by: agent_restart`）。钩子在决定前退出 → hub 记 `cancelled/terminal`；本机等满期限 → `expired/timeout`；hub 自己每分钟把过了 `expires_at` 的 pending 标为 `expired/timeout`。Codex 15 分钟内重发同一请求（同会话、同回合、同工具、同载荷）时，旧的那条改为 `cancelled/resend`，收件箱只剩新的。
- **hub 结束请求会通知守护进程。** hub 自己结束一条守护进程的 pending（过期扫描、手机决定时发现已过期、被重发顶掉）时，立即对该机器发带 `request_id` 的 Error 帧“请求已在 hub 结束”，守护进程把它交回终端（本机审计记 `expired/hub_unreachable`），不再挂满本机期限。守护进程的时钟比 hub 慢时，hub 会按自己的时间更早判过期，也由此及时收尾。机器当时不在线的，重连后重发 pending 时 hub 回“请求已结束”，同样交回终端。

## API

认证有三种：**cookie**（`POST /api/login` 换来的 `railse_session`）、**Bearer 机器密钥**（`machine.json` 的 `secret`）、**Bearer 来源令牌**（`railse hub webhook-token`）。标“同源”的接口在请求带 `Origin` 头时要求它的 host 与请求的 Host 相同，否则 403；不带 `Origin` 的（curl 等非浏览器客户端）放行。请求体一律限长、须是合法 UTF-8，且要在 10 秒内传完；整个请求（请求头加请求体）至多 30 秒收完（WebSocket 握手之后的长连接不受此限）。

| 方法与路径 | 认证 | 用途与返回 |
| --- | --- | --- |
| `GET /api/version` | 无 | `{"version","agent_proto"}`：hub 的版本（开发构建为 `dev`）与守护进程帧协议号；doctor 与安装脚本用它确认 hub 可达、版本对得上 |
| `POST /api/login` | 无（每 IP 每分钟 5 次） | `{"password"}` → 204 并设 cookie；密码错 401，没设密码或忙 503，限速 429 |
| `POST /api/logout` | 同源 | 删服务端会话、清 cookie → 204 |
| `POST /api/enroll` | 接入码（每 IP 每分钟 5 次） | `{"token","name","home_dir"}` → `{"machine_id","secret"}`；接入码无效或过期 401 |
| `GET /ws/agent` | Bearer 机器密钥 | 守护进程的 WebSocket；密钥无效或已吊销 401 |
| `POST /api/webhook/approvals` | Bearer 来源令牌 | 见下文 webhook；没配令牌 503，令牌不对 401 |
| `GET /api/status` | cookie | `{"paused","pending","rules_version","machines":[{"id","name","online","last_seen","revoked"}],"uptime","push_enabled","push_subscriptions"}`：`push_enabled` 是否配了 `--public-url`，`push_subscriptions` 订阅条数 |
| `POST /api/pause`、`/api/resume` | cookie + 同源 | 改 hub 总开关并下发 → 204 |
| `GET /api/inbox` | cookie | `{"pending":[…],"questions":[…]}`，都是请求视图、按创建时间倒序，没有时是空数组：`pending` 是全部待审批（至多 500 条）；`questions` 是最近 24 小时还在等回答的 `question`（至多 100 条），同一会话之后有了任何新事件（`done`、新的 `permission` 或 `question`）就不再列出，没有会话 id 的一直列到滑出 24 小时；已吊销机器的不列 |
| `GET /api/requests?state=&limit=` | cookie | 按创建时间倒序；`state` 为空表示全部，`limit` 默认 50、最大 500 |
| `GET /api/requests/{id}` | cookie | `{"request":…,"audit":[…]}`，审计倒序至多 200 条；没有 404 |
| `POST /api/requests/{id}/propose` | cookie + 同源 | `{"action":"allow|deny","scope":"project|global"}` → `{"rule_id","scope","yaml","summary":{"action","payload","anchor"}}`：只计算“记住”会生成的规则，不入库、不审计、不改请求。`scope` 是草案实际的范围（缺省时按下文规则选定），`yaml` 是这条规则的 YAML（与入库后 `/api/rules` 里的相同，只有一处例外：`rule_id` 与已有规则撞了时，入库时 id 追加 `-2`、`-3`…，存下的 YAML 里的 id 与预览的不同，以 decide 返回的请求里的 `rule_id` 和 `/api/rules` 为准），`summary.payload` 取 `shell|file_write|network|mcp`，`anchor` 是起匹配作用的命令前缀、命令正则、路径、`服务/工具` 或主机名。错误码与 decide 相同：坏请求体、坏 action、坏 scope 400，没有 404，已处理、已过期或机器已吊销 409（文案同 decide，但 propose 不改请求状态），记不住 422 |
| `POST /api/requests/{id}/decide` | cookie + 同源 | `{"action":"allow|deny","note":"…","remember":bool,"scope":"project|global"}` → 决定后的请求；`scope` 只在 `remember: true` 时起作用。坏 action、坏 scope（不论是否 remember）或备注超过 1000 字符 400，没有 404，已处理、已过期或机器已吊销 409，记不住 422 |
| `GET /api/rules` | cookie | 每条 `id、source、enabled、elevated、hit_count、last_hit_at、created_at、created_from、yaml` |
| `PATCH /api/rules/{id}` | cookie + 同源 | `{"enabled":bool}` → 204 并下发 |
| `DELETE /api/rules/{id}` | cookie + 同源 | → 204 并下发 |
| `POST /api/rules/import` | cookie + 同源 | 请求体是整套规则 YAML（`Content-Type: application/yaml` 或 `text/yaml`，至多 1 MiB）→ `{"version"}`；别的类型 415，解析或校验失败 400，编成下发帧超过 1 MiB 413 |
| `GET /api/rules/export` | cookie | 整套规则 YAML（附件 `railse-rules.yaml`） |
| `GET /api/machines` | cookie | 每台 `id、name、online、last_seen、agent_version、enrolled_at、revoked` |
| `DELETE /api/machines/{id}` | cookie + 同源 | 吊销 → 204；没有 404 |
| `GET /ws/client` | cookie + 同源 | 网页的推送通道，见下 |
| `GET /api/push/vapid` | cookie | `{"enabled","public_key","reason"}`：启用时 `public_key` 是 VAPID 公钥（base64url，87 个字符，给 `applicationServerKey`）、`reason` 为空；没配 `--public-url` 时 `enabled: false`、`public_key` 为空、`reason` 说明原因。每次从库里读，换过钥就是新的 |
| `POST /api/push/subscribe` | cookie + 同源 | 请求体是浏览器 `PushSubscription.toJSON()` 原样（`{"endpoint","keys":{"p256dh","auth"}}`，多余字段忽略）→ 204；同一端点再登记是更新（换密钥、失败归零）。端点须是 https、至多 2048 字节，`p256dh` 须是 P-256 曲线上的点，`auth` 须 16 字节，否则 400；已有 20 条订阅时登记新端点回 409 `{"error":"推送订阅已达上限 20 个，请先在别的设备上关闭通知"}`（同一端点再登记不算新增）；推送停用 503 |
| `DELETE /api/push/subscribe` | cookie + 同源 | `{"endpoint"}` → 204，没有这条也 204 |
| `POST /api/push/check` | cookie + 同源 | `{"endpoint"}` → `{"active":bool}`：hub 还有这条订阅时为 true；false 说明被清掉了（失效、换钥），网页应提示重新开启 |
| `POST /api/push/test` | cookie + 同源 | `{"endpoint"}` → 同步发一条测试通知（标题 Railse、正文“通知正常”）：成功 204；hub 没有这条订阅 404；推送服务没收下 502（订阅已失效时 hub 已删掉它，文案提示重新开启）；推送停用 503 |
| `GET /`（及 `/api/`、`/ws/` 以外的一切路径） | 无 | 网页：构建产物里的文件原样返回，其余路径回 `index.html`（单页回退）；只接受 GET、HEAD，其余 405；没构建时 503 提示。`/api/`、`/ws/` 下没有的接口不论方法都回 404 `{"error":"没有这个接口"}`，不回网页 |

请求的 JSON 字段：`id、machine_id、session_id、kind（permission|question|done|webhook）、tool_name、payload（已脱敏）、risk、project_root、engine_reason、state、decided_by、decision_note、rule_id、turn_id、cli_session_id、title、created_at、expires_at、decided_at、delivered_at`；`/api/inbox` 的 `pending` 与 `questions`、`/api/requests`、`/api/requests/{id}` 的 `request`、decide 的返回与 `/ws/client` 推送的请求（统称“请求视图”）还带 `machine_name`（webhook 请求为空）、`project`（项目根的最后一段）与 `has_redacted`（载荷任一字段或 `title` 含脱敏占位符 `<redacted>` 时为 true；question 的 `title` 是事件消息、webhook 的是来源给的标题，入库前都已脱敏）。`state` 取 `pending、approved、denied、cancelled、expired、auto_allowed、auto_denied、passthrough、recorded`；`delivered_at` 非空表示决定已交到钩子（或 webhook 回调已成功）。

**`has_redacted` 为 true 的请求，网页必须醒目告警。** 这时网页展示的不是原文：凭据被换成了 `<redacted>`。命令里不在引号内的裸值在 shell 元字符处停止；引号里的值、注释和不交给解释器的 heredoc 正文整段抹掉（分隔词不带引号的正文里的 `$(…)`、反引号会执行，按命令处理）；交给 `sh`/`bash`/`zsh`/`dash`/`ksh -c`、`eval`、`ssh` 执行的参数，以及喂给它们的 heredoc 正文与 here-string，按命令行语境递归处理，其中引号里的值仍整段抹。所以显示的仍不是原文，引擎却是按原文定级的。告警文案例如“命令含被隐藏的部分，展示的不是原文，建议去终端核对”，并突出 `risk` 与 `engine_reason`。这类请求也不能“记住”（见下）。

**“记住”。** `remember: true` 批准时生成一条 `learned` 放行规则，拒绝时 hub 直接构造 `source: user`、`action: deny` 的规则（shell 命令用锚定整条命令字面原文的 `command_regex: ^…$`，只拒这一条，不连带同前缀的命令），都立即下发给所有在线守护进程。保存前网页先调 `propose` 拿到同一条规则的草案（YAML 与摘要）给用户看，用户选好范围后再带同样的 `action`、`scope` 调 decide。

范围 `scope`：`global` 去掉项目范围，规则对所有项目生效（放行规则仍限定原来的风险等级，换了项目定级不同就不命中）；`project` 限定请求的项目根，请求没有项目根时 422（文案以“不能记住：”开头，通常是“没有项目根，只能记成全局”；但先碰到别的记不住的原因时报的是那个原因，例如批准一条没有项目根的命令时说的是不能记成放行，客户端不要按文案判断）；缺省（不带或空串）沿用旧行为——有项目根记成项目，没有记成全局，所以不带 `scope` 的旧客户端（curl 等）行为不变。`project`、`global` 以外的取值一律 400。

记不住时请求保持 pending，回 422，文案以“不能记住：”开头，例如：只读、项目内改动和联网操作之外的不能记成放行（`outward`、`destructive` 永远不行）；没有项目根的命令或文件写入不能记成放行（全局也不行：定级依赖项目根；项目根是家目录、它的上级或根目录时也算没有）；含凭据的命令（载荷里有 `<redacted>`）；命令、路径等超过 4 KiB；规则集已接近下发上限；拒绝匹配不上这条请求（例如 `a && b` 这样的复合命令，请手写规则）；webhook 请求；显式 `scope: project` 而没有项目根。

**`/ws/client` 帧**：每帧 `{"type":"request_new|request_updated|rules_updated|status","data":…}`。`request_new`、`request_updated` 的 `data` 是请求视图（与 `/api/inbox` 里每条同形状，守护进程、webhook、question/done 的请求都一样），`rules_updated` 是 `{"version"}`，`status` 是 `{"paused"}`。请求的状态变化、决定未送达（undeliverable），以及 `delivered_at` 从空变成有值（守护进程的送达回执、送达对账、webhook 回调成功）都推 `request_updated`。question 不再等（同一会话来了新事件）时不单独推 `request_updated`：之后的 question、done 与待审批本身以 `request_new` 推出，网页收到 `request_new` 时应重新拉 `/api/inbox`，`questions` 随之更新；由守护进程审计补进的历史行（本机自动放行、自动拒绝等）不推送，它们让 question 不再等的变化要到下一次全量拉取才看得到。推送只是“有变化”的提示：慢的订阅者（积压 64 帧）会被丢帧，网页每次连上或怀疑漏帧时必须全量重新拉取（`/api/inbox`、`/api/status`、`/api/rules` 等）。hub 每 30 秒复核一次登录并 ping，登录失效（登出、换密码、过期）时以 1008 关闭；网页不该发数据帧，发了就断开。

**webhook。** `POST /api/webhook/approvals`，`Authorization: Bearer <来源令牌>`，请求体至多 64 KiB：

```json
{"title": "部署 v1.2 到生产", "description": "可选，至多 4096 字符", "callback_url": "https://n8n.example.com/webhook/x",
 "callback_token": "可选，回调时原样带上", "expires_in": 3600}
```

`title` 必填、至多 200 字符、不能含控制字符或不可见的格式字符；`callback_url` 必填、只收 http/https；`callback_token` 至多 512 字节、只能是可见 ASCII；`expires_in` 可选，1 到 86400 秒，默认 24 小时。返回 `{"id"}`，请求以 `kind: webhook` 进收件箱（标题与描述先脱敏）。决定后 hub 回调：`POST callback_url`，头 `Authorization: Bearer <callback_token>`（给了令牌才带），body `{"id","decision":"allow|deny","note"}`；hub 判过期时回调 `decision: deny`、`note: expired`。每次超时 10 秒，最多 3 次、间隔 2 秒，不跟随重定向，只认 2xx；每次回调（成功或放弃）记审计 `webhook_callback`，只记回调地址的 scheme://host。hub 关停时正在进行的回调中止，重启后不恢复（令牌已清空，需来源重新提交）。

## 手机推送（Web Push）

配了 `--public-url` 才启用。它必须是 hub 对外的 https 源（`https://host[:port]`，与手机打开网页的地址同源），也用作 VAPID 签名的 `sub`（Apple 对不规范的 `sub` 回 `BadJwtToken`）。订阅与主屏幕应用都绑在这个源上，域名定下后不要再换。

- **什么时候推。** 只在新请求产生时推：新登记的待审批（`permission`）、webhook 请求，以及新的 `question`（Agent 在等你回答）。`done` 不推。守护进程断线重连后补发的旧审计里的 `question`（事件时间早于此刻 2 分钟以上）照样进收件箱，但不推，免得一下子弹出一串过时的通知。推送不影响请求主流程：在队列里排队（至多 256 条，满了丢弃并记日志），由一个后台协程逐条处理：每条请求并行（至多 4 路）发给全部订阅，一个慢端点不拖住其余手机，全部发完才处理下一条。订阅至多 20 条（每条新请求都要推给全部订阅），满了网页开启通知时会提示先在别的设备上关闭。
- **推什么。** 明文载荷 `{"id","kind","title","body","url":"/r/<id>"}`，编码后不超过 3 KiB（Apple 上限 4 KiB）：标题至多 80 个字符、正文至多 200 个字符，控制字符与换行并成空格（permission 正文两行之间的换行除外），转义后仍超就继续截正文。文案说人话：permission 的标题是“<CLI> 要<动词>”，CLI 按请求所属会话的 `sessions.tool`（`claude`→Claude Code、`codex`→Codex、`gemini`→Gemini CLI，没有会话或查不到时为 Agent），动词按风险（`network` 联网、`outward` 往外发、`destructive` 做不可逆的操作、`write_outside` 改项目外的文件、`unknown` 跑一个没见过的命令、`write_in_project` 改项目里的文件、`read` 读文件，其余“做一件事”），有项目时追加“ · 项目”，接入且未吊销的机器多于一台时再追加“ · 机器名”（去掉结尾的 `.local`），超长时先截机器名、再截项目名；正文第一行是载荷摘要（命令、路径、URL 或 `服务/工具`，至多 160 个字符），第二行“点开批准或拒绝”。question 的标题是“<CLI> 在终端等你回答”（同样追加项目与机器名），正文是问题；webhook 的标题是“工作流等你批准”，正文是来源给的标题。 投递参数：`TTL` 是请求剩下的期限（`expires_at` 减此刻，至多 24 小时、至少 1 秒；没有期限的 question 为 24 小时），手机离线到请求过期后才上线就不再收到它；`Urgency: high`、`Topic` 为请求 id（只在 id 是 1 到 32 个 base64url 字符时带，同一请求的未投递推送被替换）。
- **失败怎么算。** 推送服务回 2xx 算成功，连续失败归零；回 404 或 410 说明订阅已失效，立即删。下面这些只记日志、不计失败（问题不在订阅）：hub 本地出错（载荷、签名等）、hub 关停或测试推送的请求断开时被取消、413、429。其余失败（连不上、超时、5xx、其他 4xx 如 401/403）记一次；同一订阅**连续失败至少 5 次、且这一轮的第一次失败已过去 24 小时**才删。所以推送服务或 hub 自己的短暂故障（一阵 503、hub 断网、时钟跳变导致 `BadJwtToken`）不会删掉好订阅，故障恢复后第一次成功就把次数与起始时刻一起清零；真正失效的订阅（端点一直不收）要满一天才清掉，期间每次推送它都记一次失败。计数与删除在同一事务里、删除带条件：你在两次失败之间重新开启通知（次数与起始时刻归零），不会被之前的计数误删。库里密钥解不开的订阅推送时直接删。
- **换钥。** `railse hub vapid --rotate` 生成新密钥并删除全部订阅（旧订阅对着旧公钥，新密钥签的推送会被拒），所有手机都要在网页里重新开启通知。在跑的 hub 不用重启，下一次推送或网页取公钥时就用新密钥。私钥疑似泄露、设备丢失时换钥（见“安全要点”）。
- **退出登录、换密码。** 网页“退出登录”先关闭这台设备的通知再删会话；`railse hub password` 换密码时删除全部订阅（与写密码、删会话同一事务）。见“安全要点”的“退出登录与设备丢失”。
- **审计。** 每条请求推完记 `push_sent`（actor `system`，`{"sent","gone","failed"}`；没有订阅时不记）；删订阅记 `push_subscription_removed`（`{"id","origin","reason"}`，`reason` 是 `gone`、`failures` 或 `invalid`）；网页登记、删除、测试分别记 `push_subscribed`、`push_unsubscribed`、`push_test`；换密码清空订阅记 `push_subscriptions_cleared`（actor `system`，`{"reason":"password_changed","count"}`，没有订阅时 `count` 为 0）。审计与日志只记推送端点的 scheme://host，不记完整端点；清空订阅只记条数。

## 安全要点

- **只存哈希。** 机器密钥、登录会话 token、webhook 来源令牌、接入码存 SHA-256；网页密码用 argon2id（time 1、memory 64 MiB、threads 4、盐 16 字节）。**例外**：webhook 的出站回调令牌（`callback_token`）回调时要原样带上，明文存在 `requests.callback`，回调成功、3 次都失败或因关停中止后立即清空（用户决定 D）。
- **登录。** 每个来源 IP 每分钟 5 次（IPv6 按 /64 算一个来源），同时校验密码的上限 2 个，等不到名额回 503 且不占限速名额。cookie `railse_session`：HttpOnly、SameSite=Lax、Path=/、30 天，请求经 TLS、开了 `--secure-cookies` 或配了 https 的 `--public-url` 时加 Secure（hub 在终止 TLS 的反向代理后面时自己收到的是明文 HTTP，靠后两者判断）；所以配了 `--public-url` 后要经它的 https 地址登录，浏览器一般不接受明文 http 地址（如局域网 IP）上设的 Secure cookie。换密码使所有已登录会话失效，并删除全部推送订阅。
- **退出登录与设备丢失。** 推送订阅与登录会话是分开存的：只删会话的话，这台设备的锁屏上照样会收到谁要做什么、项目与命令摘要。所以网页的“退出登录”先尽力关闭这台设备的通知（让 hub 删掉它的订阅，同时退掉浏览器里的订阅；最多等 3 秒，关不掉也照常退出），再删会话。设备丢了、或在别人的浏览器上开过通知，在服务器上按顺序做：
  1. 先 `railse hub password` 换密码：同一事务里删掉全部登录会话与全部推送订阅（审计 `push_subscriptions_cleared`），丢掉的设备立即登不进、也不再收到推送；
  2. 再 `railse hub vapid --rotate` 换推送密钥：旧公钥作废，并再删一次订阅（两步之间若有设备重新登记，也一并删掉）；
  3. 每台还在用的手机用新密码重新登录，到设置里重新开启通知。
- **跨站。** 写接口、`/api/logout` 与 `/ws/client` 校验同源 Origin，是 SameSite 之外的第二道防线。
- **hub 的权力被限死。** 对请求只能回 allow 或 deny，没有向机器下发命令的通道。“记住”永远不会生成放行 `outward`、`destructive`（以及 `unknown`、`write_outside`）的规则，`learned` 来源的这类放行在导入时也被拒绝。网页导入的 `user` 规则可以放行 `outward`、`destructive`（用户决定 B）：这类规则在 `/api/rules` 标 `elevated: true`，导入时审计 `rules_imported` 带上它们的 id，从停用改为启用时审计 `rule_enabled_elevated`。
- **数据最小化。** 守护进程上报前对载荷与事件文本脱敏，hub 收到后再脱敏一次并截断各字段；hub 不信任守护进程送来的路径与命令，只存储与展示。审计里不含钩子进程的环境变量。
- **明文接入会警告。** `railse agent enroll --hub http://…`（非本机地址）提示明文传输密钥。
- **ACME 私钥明文存盘（设计 5.3 例外）。** 直出 TLS 时 Let's Encrypt 的账户私钥与证书私钥（autocert 的格式）明文存在数据目录的 `acme/`：目录 0700（hub 启动时建好并收紧），文件 0600（写临时文件再改名），`railse doctor` 检查两者的权限。它们只给 TLS 握手与 ACME 请求用，不经任何接口、日志或审计出现。拿到证书私钥就能冒充 hub：备份数据目录时把它当私密数据保管；疑似泄露时停掉 hub、删掉 `acme/` 再启动，重新签发（注意频率限制）。
- **推送密钥明文存库（设计 5.3 例外）。** VAPID 私钥（settings 的 `vapid_private`）与订阅的 `keys.p256dh`、`keys.auth` 发推送时要用原值，明文存在 `hub.db`；它们与签好的 VAPID 令牌永不经任何接口、日志或审计出现：`/api/push/vapid` 只回公钥，`railse hub vapid` 只打印公钥。推送端点的路径本身也是凭据的一部分，日志与审计只记它的 scheme://host。订阅只有登录的用户能登记，端点只校验是 https，不限制内网地址。

## 部署与反向代理

公网部署有三种方式：A. hub 内置 ACME 直出 TLS（推荐自用，一台机器只跑 hub 时最省事）；B. hub 只监听本机，由反向代理终止 TLS；C. 本机 hub 加 Tailscale Funnel（没有服务器、域名时用）。

### A. 内置 ACME 直出 TLS（推荐自用）

```bash
railse hub run --acme-domain hub.example.com --acme-email me@example.com
```

- **前提。** 域名的 A/AAAA 记录已指向这台机器，443 与 80 对公网开放（云厂商的安全组、防火墙都要放行）：Let's Encrypt 从公网连进来验证域名，用 443 上的 TLS-ALPN-01 或 80 上的 HTTP-01。
- **监听。** hub 在 `--listen`（给了 `--acme-domain` 时默认 `:443`）直出 TLS（最低 TLS 1.2，支持 HTTP/2；WebSocket 照常）。`--acme-http`（默认 `:80`）答 HTTP-01 挑战，并把明文 http 的 GET、HEAD 302 跳到对外源加原路径与查询（不看请求的 Host），别的方法回 400；`--acme-http none` 不监听 80，只能靠 443 上的 TLS-ALPN-01 签发，也没有 http 跳转。443 或 80 被占（比如还跑着 nginx）、证书目录建不了时启动即退出 1，日志“hub 监听失败”。
- **签发与续期。** 证书在第一次 https 访问时签发，约需 1 到 2 分钟，这一次访问可能超时，稍后刷新即可；之后由 autocert 在到期前自动续期（默认提前 30 天或有效期的 1/3，取较小者），不用重启。只给 `--acme-domain` 这一个域名签：别的 SNI 与按 IP 访问在握手时就被拒，不会替它们申请。只用 Let's Encrypt 的生产目录，它对同一域名的签发有频率限制，不要反复删掉 `acme/` 重来。
- **对外源与 cookie。** 对外源（`--public-url`）默认 `https://<域名>`，手机推送随之启用；要显式写时主机名必须就是这个域名，否则退出 2（端口可以不同，例如 `--listen :8443 --public-url https://hub.example.com:8443`，这时 Let's Encrypt 只能经 80 端口验证）。cookie 一律加 Secure。
- **私钥。** 账户私钥与证书私钥明文存在数据目录的 `acme/`（目录 0700、文件 0600，启动时收紧目录），`--acme-email` 是 Let's Encrypt 账户的联系邮箱，只用于账户与政策通知，可不填：Let's Encrypt 已于 2025 年停发证书到期提醒邮件，续期失败没有邮件提醒，要看 hub 的 stderr（见“故障排查”），证书过期后工作机上 `railse doctor` 的 `hub.reachable` 会报证书错误。见“安全要点”。
- **权限。** Linux 上绑定 443/80 需要 `CAP_NET_BIND_SERVICE`：用 `sudo /usr/local/bin/railse setup autostart --hub --system -- --acme-domain hub.example.com --acme-email me@example.com` 装成系统服务，以普通用户运行、只给这一项能力（railse 先用 `curl … | sudo sh` 装进 `/usr/local/bin`，服务用户才执行得了；sudo 的 `secure_path` 不含 `~/.local/bin`，RHEL 系连 `/usr/local/bin` 也不含，所以写绝对路径，见 [setup.md](setup.md)）；macOS 绑定 443/80 不需要特权。hub 不要以 root 运行，以 root 启动时 hub 会醒目警告（不拒绝）。
- 前面已有反向代理终止 TLS 时不要用它，用 B。

### B. 反向代理

让 hub 只监听本机，由反向代理终止 TLS；配了 `--public-url` 时 cookie 自动加 Secure，没配才要给 hub 加 `--secure-cookies`。

- **必须保留原 Host。** 同源检查拿浏览器的 Origin 与请求的 Host 比较，代理把 Host 改成 `127.0.0.1:8787` 时写接口与 `/ws/client` 全部 403。nginx 用 `proxy_set_header Host $host;`（对外不是 443/80 端口时用 `$http_host`，它带端口）。
- **声明可信代理。** 只有来自 `--trusted-proxy` 的连接，hub 才看 `X-Forwarded-For`（从右往左取第一个不可信的地址）。不声明时所有请求都算代理的地址，按 IP 的登录限速退化成全局限速。代理必须追加而不是透传客户端给的 `X-Forwarded-For`（nginx：`$proxy_add_x_forwarded_for`）。`--trusted-proxy` 拒绝 `0.0.0.0/0` 这类覆盖全部地址的前缀。
- **转发 WebSocket。** `/ws/agent` 与 `/ws/client` 要求代理转发 Upgrade。hub 与守护进程每 30 秒都有帧，代理默认 60 秒的读超时够用。

```nginx
map $http_upgrade $connection_upgrade { default upgrade; '' close; }
server {
    listen 443 ssl;
    server_name hub.example.com;
    # ssl_certificate …
    location / {
        proxy_pass http://127.0.0.1:8787;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
    }
}
```

配套：`railse hub run --listen 127.0.0.1:8787 --public-url https://hub.example.com --trusted-proxy 127.0.0.1`（`--public-url` 是手机打开网页的源，不加推送就是停用的；nginx 经 IPv6 回环连 hub 时再加 `::1`）。用 Caddy 时 `reverse_proxy 127.0.0.1:8787` 即可（按 Caddy v2 文档：默认透传 Host、覆盖 X-Forwarded-For、自动代理 WebSocket）。

### C. 本机 hub + Tailscale Funnel

没有服务器和域名、hub 就跑在工作机上时用。一条命令 `railse setup all --local-hub` 全做了（见 [setup.md](setup.md) 的“一台机器就够：本机 hub”）；等价的手动做法：

```sh
railse hub password    # 先设网页密码（至少 8 个字符）
railse hub run --listen 127.0.0.1:8787 --public-url https://<机器名>.<tailnet>.ts.net --trusted-proxy 127.0.0.1,::1
tailscale funnel --bg 8787    # 在另一个终端里，hub 应答之后再开
```

`hub run` 是前台进程，关了终端 hub 就停了；长期用把它换成 `railse setup autostart --hub -- --listen 127.0.0.1:8787 --public-url https://<机器名>.<tailnet>.ts.net --trusted-proxy 127.0.0.1,::1`（托管、开机自启）。Funnel 放在最后：先确认 `curl http://127.0.0.1:8787/api/version` 回的是 hub 的版本，免得把 8787 上别的程序（如 wrangler dev、RStudio Server 的默认端口也是它）开到公网。

- **Funnel 的事实。** railse 只用 443。Funnel 按整个 `<机器名>:443` 开：443 上已有的别的 `tailscale serve` 路径会一起公开（`setup all --local-hub` 见到这种情况会停下）。证书由 Tailscale 签发。入口在 Tailscale 的服务器上，公网流量先到那里再转进本机，所以本机不用开端口、不用公网 IP。机器名就是域名：`https://<机器名>.<tailnet>.ts.net`，改机器名等于换域名，推送订阅与主屏幕图标要重来。tailnet 要先在管理台开通 HTTPS 证书与 Funnel。
- **同 B。** Funnel 就是一个把 HTTPS 转到 `127.0.0.1:8787` 的反向代理：hub 只监听本机，所以 `--trusted-proxy 127.0.0.1,::1` 让 hub 认它送来的 `X-Forwarded-For`；`--public-url` 写 Funnel 的地址，推送随之启用，cookie 自动加 Secure。
- **自己的隧道。** `railse setup all --local-hub --public-url https://…` 不开 Funnel，只托管 hub、接入守护进程（不需要 Tailscale）。隧道要自己转到 `127.0.0.1:<端口>`，保留原 Host、转发 WebSocket、追加 `X-Forwarded-For`（同 B）。
- **卸载。** `railse setup autostart --hub --uninstall`（数据目录不在默认位置的加 `--dir <目录>`），`tailscale funnel --https=443 off`（关的是 443 上的整个 Funnel，有别的配置先核对），数据目录 `~/.railse-hub` 不要了再删（含网页密码与推送密钥）。

## 部署到 Fly.io

仓库 `deploy/fly/` 里有现成的 `Dockerfile`（从 GitHub Release 下载并校验二进制）、`entrypoint.sh` 与 `fly.toml`。Fly 的边缘代理终止 TLS 并转发 WebSocket，hub 在容器里监听明文 8787，用 `--trusted-proxy fdaa::/16` 认 Fly 代理送来的 `X-Forwarded-For`。数据目录挂在持久卷 `/data`。

```bash
fly apps create <应用名> --org <组织>
fly volumes create railse_hub --region <区域> --size 1 -a <应用名>
fly secrets set RAILSE_HUB_INIT_PASSWORD='<网页密码，至少 8 个字符>' -a <应用名>   # 首次启动时写入，之后用 railse hub password 改
cd deploy/fly && fly deploy --app <应用名> --ha=false        # 单机加单卷，不要开高可用
fly ssh console -a <应用名> -C "railse hub token"             # 接入码，给工作机 railse setup all 用
```

`fly.toml` 里的 `app` 与 `RAILSE_PUBLIC_URL` 改成自己的应用名（`https://<应用名>.fly.dev`，域名一旦定下不要再换，推送订阅绑在它上面），`primary_region` 改成上面建卷的区域（卷只能挂到同一区域的机器上）。`auto_stop_machines` 必须是 `off`：守护进程与手机的长连接和推送都要求机器常驻。升级就改 `RAILSE_VERSION` 再 `fly deploy`。

## 故障排查

先跑 `railse doctor`：工作机上直接跑（检查守护进程、hub 连接、钩子与 CLI 版本），hub 机器上以跑 hub 的用户身份加 `--hub-dir <数据目录>`。每个 warn、fail 下面都有一行“修复：”，各检查项的含义见 [setup.md](setup.md) 的“railse doctor”。doctor 说不清的再对照下表。

| 现象 | 原因与处理 |
| --- | --- |
| 打开 hub 地址看到“网页未构建”（503） | 二进制是直接 `go build` 的，没有网页构建：在仓库里 `make build`（或先 `make web` 再 `go build`）后换上新二进制 |
| 网页写操作或 `/ws/client` 得到 403 | 代理没传原 Host，或页面与 hub 不同源。见“部署与反向代理” |
| 导入规则 415 | `Content-Type` 要是 `application/yaml` 或 `text/yaml` |
| 登录或接入 429 | 同一来源一分钟内超过 5 次；在代理后面且没配 `--trusted-proxy` 时所有人共用一个来源 |
| 登录 503 | 还没 `railse hub password`，或同时校验密码的人太多（稍后重试） |
| webhook 503 | 还没 `railse hub webhook-token` |
| `railse agent status` 的 `hub:` 行 | `none`：没有 machine.json，未接入 hub，引擎定不了的请求直接交回终端。`connecting <地址>`：还没连上、断线后等着重连，或握手被 401 拒绝但还不到 3 次，看 `agent.log` 的“与 hub … 的连接结束”“回 HTTP 401（第 N/3 次）”。`connected <地址>`：连着。`disabled <地址>`：连续 3 次被 401 拒绝、已停止连接，处理见下一行 |
| 守护进程日志“以 HTTP 401 拒绝了本机密钥”，`railse agent status` 显示 `hub: disabled` | 机器被吊销或密钥不对：`railse agent enroll --force` 后重启守护进程。停用前 hub 若处于暂停，本机会一直按 hub 暂停，重新接入后由 welcome 更新 |
| 守护进程日志“拒绝了握手…协议版本不一致” | 升级守护进程与 hub 中较旧的一方 |
| 请求不进收件箱、立即交回终端 | 看 `railse agent status` 是否暂停；看 `agent.log` 是否连着 hub；Gemini 的请求本来就不进 |
| `railse hub password` 回显密码 | 用 `read -s PW; printf '%s\n' "$PW" \| railse hub password` |
| hub 退出码 1、日志“不关数据库” | 关停时有在途请求、长连接处理或回调没在时限内退出，或服务出错结束；直接重启即可 |
| `railse hub run` 启动即退出 1、日志“监听失败” | 端口被占或 `--listen` 地址无效，换个地址。用 `--acme-domain` 时还可能是 80 端口被占（`--acme-http` 换地址或 `none`）、证书目录建不了，或 Linux 上没有 `CAP_NET_BIND_SERVICE`（`permission denied`，见“部署与反向代理”的 A） |
| `--acme-domain` 时网页打不开、证书签不下来 | 域名没解析到这台机器，或 443、80 没对公网开放。签发失败的原因在 hub 进程的 stderr（不进 `hub.log`；前台跑时就在终端里，与 `--foreground` 无关；systemd 系统服务看 `journalctl -u railse-hub`，launchd 看数据目录的 `launchd.log`）里，形如 “http: TLS handshake error … acme/autocert: …”；撞上 Let's Encrypt 的频率限制时只能等，别反复删 `acme/` |
| `railse hub run` 退出 2、提示 acme-domain、acme-http 或 acme-email | `--acme-domain` 要写小写主机名（`hub.example.com`，不带 `https://`、端口、路径，不能是 IP）；`--acme-http` 写 `host:port` 或 `none`；`--acme-email`、`--acme-http` 要和 `--acme-domain` 一起用；同时给 `--public-url` 时主机名要与域名一致 |
| 日志或终端“hub 正以 root 运行” | hub 不需要 root：Linux 用 `sudo /usr/local/bin/railse setup autostart --hub --system -- …` 装成以普通用户运行的系统服务（railse 装在别处时 `sudo "$(command -v railse)" …`） |
| `railse hub run` 退出 2、提示 public-url | `--public-url` 要写成 `https://hub.example.com`（不带路径、结尾的 `/`、查询），http 不行 |
| 网页说推送停用、`/api/push/vapid` 的 `enabled` 为 false | hub 没加 `--public-url` |
| 收不到推送 | 看 `GET /api/requests/{id}` 的审计：没有 `push_sent` 是没有订阅、推送停用、或是事件时间太旧的 question；`push_sent` 里 `failed` 不为 0 时看 `hub.log` 的“推送到 … 失败（连续第 N 次）”（带推送服务给的原因，如 `BadJwtToken`；同一原因持续一天以上订阅会被删）；有 `push_subscription_removed` 说明订阅已失效被删，在网页里重新开启通知 |
| 换密码或换钥后手机收不到推送 | 两者都删除了全部推送订阅：用新密码登录后，到设置里重新开启通知 |
| 启动报“库里的 VAPID 密钥损坏” | `railse hub vapid --rotate` 换新密钥（所有手机重新开启通知） |
| 换钥之后手机收不到 | 换钥删了全部订阅，每台手机都要在网页里重新开启通知 |

日志：`hub.log` 里有“hub 监听 …”、“直出 TLS（ACME，Let's Encrypt）：域名 …，证书目录 …”、“ACME 挑战（HTTP-01）与 http→https 跳转监听 …”、“Web Push 已启用，对外源 …”（或“没有配置 --public-url，Web Push 停用”）、“推送到 https://… 失败（连续第 N 次）: …”、“推送订阅 … 已失效，删除”、“VAPID 密钥已换新”、“可信代理: …”、“机器 … 超过 … 没有 pong，断开”、“webhook 请求 … 回调 … 失败 3 次，放弃”、“hub 已停止”。守护进程的 `agent.log` 里有“接入 hub …”、“已连接 hub …，规则版本 N”、“与 hub … 的连接结束: …，N 后重连”、“hub … 回 HTTP 401（第 N/3 次），稍后重试…”、“规则更新到版本 N”、“与 hub 断线超过 30s，请求 … 交回终端”。`GET /api/status` 的 `machines[].online` 看各机器是否连着；`GET /api/requests/{id}` 的 `audit` 看一条请求经历了什么。

## 已知限制（后续计划，不算未完成）

- 本机 hub（`setup all --local-hub`）跟着机器走：睡眠、关机、断网时手机连不上 hub，也收不到推送；几台工作机共用一个 hub 的，放在常开的那台。
- 本机 hub 的地址绑在 Tailscale 机器名上，改名等于换域名，手机上的主屏幕图标与推送订阅要重新添加。
- 手机挂着代理时 `*.ts.net` 可能打不开：关掉代理，或在代理规则里把 `ts.net` 设为直连。本机的代理还可能让 `railse doctor` 的 `hubsrv.local` 报 warn（从本机访问不到对外地址），用手机 4G 验证。

- 本机 pause 反向同步到 hub。
- 复合命令不能“拒绝并记住”（422），需要手写规则。
- 吊销机器的瞬间正在登记的请求可能没被取消，按期限过期。
- hub 重启不恢复关停时正在进行的 webhook 回调，来源需重新提交。
- 推送队列在内存里：hub 重启时还没发出的推送丢失，队列满（256 条）时新的推送丢弃（请求本身照常进收件箱）。
