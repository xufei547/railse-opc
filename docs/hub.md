# hub：请求、规则、机器与审计的中心

## 是什么

`railse hub` 跑在云服务器上，是请求、规则、机器与审计的唯一真相。各机器的守护进程只向外连它（`/ws/agent`），手机网页（第 4 份计划）也连它（REST 与 `/ws/client`），外部系统（如 n8n）经 webhook 往收件箱里提请求。hub 对每条请求只能回答放行（allow）或拒绝（deny），没有任何向机器下发命令的通道：发给守护进程的帧只有 welcome、decision、rules、paused、ping、pong、error 七种。

本计划不含 TLS、Web Push、PWA 页面与开机自启（见文末“已知限制”）。

## 启动

```bash
railse hub password        # 从标准输入读一行设为网页登录密码（至少 8 个字符），终端下会回显
read -s PW; printf '%s\n' "$PW" | railse hub password; unset PW   # 不回显的写法
railse hub run --listen 127.0.0.1:8787
```

- 数据目录取 `--dir`，否则 `$RAILSE_HUB_HOME`，否则 `~/.railse-hub`；目录 0700，`hub.db`（SQLite，WAL 模式，`-wal`、`-shm` 沿用 0600；开 `secure_delete`，删改掉的内容如清空的回调令牌用零覆盖）与 `hub.log` 都是 0600，已有的文件启动时收紧。
- `railse hub run` 的标志：`--listen`（默认 `127.0.0.1:8787`）、`--secure-cookies`（cookie 加 Secure，hub 在 TLS 反向代理后面时打开）、`--trusted-proxy <CIDR 或 IP>`（可重复或逗号分隔，见“部署与反向代理”）、`--foreground`（日志同时写到 stderr）。子命令不认的标志会报错退出 2。
- 没设密码也能启动，日志提示“还没有设置网页密码”，网页登录回 503。换密码会让所有已登录的网页立即失效。
- 收到 SIGINT、SIGTERM 后 5 秒内退出：断开守护进程、等在途请求、长连接处理与 webhook 回调收尾；等不到它们退出时（含被强制断开、处理还没结束的普通请求）退出码 1，日志写“不关数据库”。
- `railse hub webhook-token` 生成 webhook 来源令牌，明文只显示这一次，再生成一次旧的就失效。

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
4. 重启 `railse agent run`。日志出现“已连接 hub”即接上。

`~/.railse/machine.json`（0600）存 `hub_url`、`machine_id`、`secret`（机器长期密钥）、`name`。已存在时 `enroll` 拒绝覆盖，换 hub 或重新接入要加 `--force`。文件损坏时守护进程拒绝启动（不会悄悄退回本地模式）；要退回本地闭环就删掉它再重启，残留的 hub 暂停随之清除。

吊销：`DELETE /api/machines/{id}`（网页在第 4 份）。密钥立即失效，在线连接被断开，它仍 pending 的请求改为 `cancelled`（`decided_by: machine_revoked`）。

## 接入后的行为

- **规则以 hub 为准。** 每次连上，welcome 里的规则快照一律替换本机规则（不比版本：hub 重建库后版本可能变小）；连接期间的 `rules` 帧只收比本机新的版本。本机 `rules.yaml` 只是缓存，文件头写着“# 由 hub 下发，版本 N。本机修改会在下次下发时被覆盖。”；第一次覆盖本机原有的规则文件前先备份为 `rules.local-<UTC 时间>.yaml`（见“接入一台机器”第 2 步）。`railse agent reload`（或 SIGHUP）仍可临时加载本机改动，下一次下发或重连时被覆盖。
- **引擎定不了的请求进 hub 等手机决定**（`outward`、`network`、`unknown` 等没有规则命中的 Claude、Codex 请求）。钩子一直等到手机上的决定、本机期限（约 24 小时）或下面的交回终端。规则能定的照旧在本机立即放行或拒绝，记录异步上报 hub。
- **手机上的备注不会给到 agent。** 钩子只看到固定文案：放行“已在手机上批准”，拒绝时 Claude、Codex 看到“Railse：已在手机上拒绝”。备注只进本机审计的 `note` 字段与 hub 的 `decision_note`、审计。
- **总开关：生效值 = 本机 || hub。** 两边分开存在 `state.json`（`paused`、`hub_paused`）。`railse agent pause`/`resume` 只改本机这一侧，不会被 hub 覆盖，也不反向同步到 hub；hub 的暂停（`POST /api/pause`，下发 paused 帧、写进 welcome）只改 hub 那一侧。`railse agent status` 显示 `paused: true (local)`、`(hub)` 或 `(local+hub)`；本机 resume 后 hub 仍暂停会提示。暂停时只有用户 deny 规则照常生效，其余一律交回终端，不送 hub。
- **连不上 hub 时最多等 HubGrace（30 秒）就交回终端。** 送出之前没连上，等 30 秒仍连不上就交回；已送到 hub 后断线，同样起 30 秒计时，期间重连上就接着等（hub 会补发已做的决定），超时交回。两种情况钩子都是“不作决定”，CLI 弹原生审批框，本机审计记 `state: expired`、`decided_by: hub_unreachable`，随后上报的这条审计让 hub 里的请求也变成 expired。
- **Gemini 的请求永远不进 hub。** Gemini 的钩子只能拒绝不能放行，守护进程直接交回原生确认框（`decided_by: gemini`），只作审计上报；hub 也拒收 Gemini 会话的 pending。
- **hub 回 401（机器被吊销或密钥无效）后**，守护进程停止重连 hub，请求一律立即交回终端，日志提示重新接入；要重新 `railse agent enroll --force` 并重启守护进程。
- **协议版本不一致**时 hub 拒绝握手（Error 帧“协议版本 X，hub 需要 Y”，以 1008 关闭，审计 `agent_proto_mismatch`），守护进程日志提示升级较旧的一方，每 10 分钟再试。
- **断线与重连。** 守护进程重连退避 1、2、4、8、16、30 秒，连接稳定后归零；超过 60 秒收不到 hub 的任何帧就主动断开重连。重连时 hello 带上本机仍在等的请求：hub 补发其中已决定的；hub 里多出来的 pending 改为 `cancelled`（`decided_by: agent_restart`）。钩子在决定前退出 → hub 记 `cancelled/terminal`；本机等满期限 → `expired/timeout`；hub 自己每分钟把过了 `expires_at` 的 pending 标为 `expired/timeout`。Codex 15 分钟内重发同一请求（同会话、同回合、同工具、同载荷）时，旧的那条改为 `cancelled/resend`，收件箱只剩新的。
- **hub 结束请求会通知守护进程。** hub 自己结束一条守护进程的 pending（过期扫描、手机决定时发现已过期、被重发顶掉）时，立即对该机器发带 `request_id` 的 Error 帧“请求已在 hub 结束”，守护进程把它交回终端（本机审计记 `expired/hub_unreachable`），不再挂满本机期限。守护进程的时钟比 hub 慢时，hub 会按自己的时间更早判过期，也由此及时收尾。机器当时不在线的，重连后重发 pending 时 hub 回“请求已结束”，同样交回终端。

## API

认证有三种：**cookie**（`POST /api/login` 换来的 `railse_session`）、**Bearer 机器密钥**（`machine.json` 的 `secret`）、**Bearer 来源令牌**（`railse hub webhook-token`）。标“同源”的接口在请求带 `Origin` 头时要求它的 host 与请求的 Host 相同，否则 403；不带 `Origin` 的（curl 等非浏览器客户端）放行。请求体一律限长、须是合法 UTF-8，且要在 10 秒内传完。

| 方法与路径 | 认证 | 用途与返回 |
| --- | --- | --- |
| `POST /api/login` | 无（每 IP 每分钟 5 次） | `{"password"}` → 204 并设 cookie；密码错 401，没设密码或忙 503，限速 429 |
| `POST /api/logout` | 同源 | 删服务端会话、清 cookie → 204 |
| `POST /api/enroll` | 接入码（每 IP 每分钟 5 次） | `{"token","name","home_dir"}` → `{"machine_id","secret"}`；接入码无效或过期 401 |
| `GET /ws/agent` | Bearer 机器密钥 | 守护进程的 WebSocket；密钥无效或已吊销 401 |
| `POST /api/webhook/approvals` | Bearer 来源令牌 | 见下文 webhook；没配令牌 503，令牌不对 401 |
| `GET /api/status` | cookie | `{"paused","pending","rules_version","machines":[{"id","name","online","last_seen","revoked"}],"uptime"}` |
| `POST /api/pause`、`/api/resume` | cookie + 同源 | 改 hub 总开关并下发 → 204 |
| `GET /api/inbox` | cookie | 全部 pending（至多 500 条），每条是请求加 `machine_name`、`project` |
| `GET /api/requests?state=&limit=` | cookie | 按创建时间倒序；`state` 为空表示全部，`limit` 默认 50、最大 500 |
| `GET /api/requests/{id}` | cookie | `{"request":…,"audit":[…]}`，审计倒序至多 200 条；没有 404 |
| `POST /api/requests/{id}/decide` | cookie + 同源 | `{"action":"allow|deny","note":"…","remember":bool}` → 决定后的请求；坏 action 或备注超过 1000 字符 400，没有 404，已处理、已过期或机器已吊销 409，记不住 422 |
| `GET /api/rules` | cookie | 每条 `id、source、enabled、elevated、hit_count、last_hit_at、created_at、created_from、yaml` |
| `PATCH /api/rules/{id}` | cookie + 同源 | `{"enabled":bool}` → 204 并下发 |
| `DELETE /api/rules/{id}` | cookie + 同源 | → 204 并下发 |
| `POST /api/rules/import` | cookie + 同源 | 请求体是整套规则 YAML（`Content-Type: application/yaml` 或 `text/yaml`，至多 1 MiB）→ `{"version"}`；别的类型 415，解析或校验失败 400，编成下发帧超过 1 MiB 413 |
| `GET /api/rules/export` | cookie | 整套规则 YAML（附件 `railse-rules.yaml`） |
| `GET /api/machines` | cookie | 每台 `id、name、online、last_seen、agent_version、enrolled_at、revoked` |
| `DELETE /api/machines/{id}` | cookie + 同源 | 吊销 → 204；没有 404 |
| `GET /ws/client` | cookie + 同源 | 网页的推送通道，见下 |

请求的 JSON 字段：`id、machine_id、session_id、kind（permission|question|done|webhook）、tool_name、payload（已脱敏）、risk、project_root、engine_reason、state、decided_by、decision_note、rule_id、turn_id、cli_session_id、title、created_at、expires_at、decided_at、delivered_at`；`/api/inbox`、`/api/requests`、`/api/requests/{id}` 的 `request`、decide 的返回与 `/ws/client` 推送的请求（统称“请求视图”）还带 `machine_name`（webhook 请求为空）、`project`（项目根的最后一段）与 `has_redacted`（载荷任一字段含脱敏占位符 `<redacted>` 时为 true）。`state` 取 `pending、approved、denied、cancelled、expired、auto_allowed、auto_denied、passthrough、recorded`；`delivered_at` 非空表示决定已交到钩子（或 webhook 回调已成功）。

**`has_redacted` 为 true 的请求，网页必须醒目告警。** 这时网页展示的不是原文：凭据被换成了 `<redacted>`；裸值在 shell 元字符处停止，引号内与 heredoc 正文整段抹掉，所以显示的仍不是原文（少数写法仍会多抹，例如引号或注释里的 `<<` 也当 heredoc 起点，其后几行按文本语境抹到空白），引擎却是按原文定级的。告警文案例如“命令含被隐藏的部分，展示的不是原文，建议去终端核对”，并突出 `risk` 与 `engine_reason`。这类请求也不能“记住”（见下）。

**“记住”。** `remember: true` 批准时生成一条 `learned` 放行规则，拒绝时 hub 直接构造 `source: user`、`action: deny` 的规则（shell 命令用锚定整条命令字面原文的 `command_regex: ^…$`，只拒这一条，不连带同前缀的命令），都立即下发给所有在线守护进程。记不住时请求保持 pending，回 422，文案以“不能记住：”开头，例如：只读、项目内改动和联网操作之外的不能记成放行（`outward`、`destructive` 永远不行）；含凭据的命令（载荷里有 `<redacted>`）；命令、路径等超过 4 KiB；规则集已接近下发上限；拒绝匹配不上这条请求（例如 `a && b` 这样的复合命令，请手写规则）；webhook 请求。

**`/ws/client` 帧**：每帧 `{"type":"request_new|request_updated|rules_updated|status","data":…}`。`request_new`、`request_updated` 的 `data` 是请求视图（与 `/api/inbox` 每条同形状，守护进程、webhook、question/done 的请求都一样），`rules_updated` 是 `{"version"}`，`status` 是 `{"paused"}`。请求的状态变化、决定未送达（undeliverable），以及 `delivered_at` 从空变成有值（守护进程的送达回执、送达对账、webhook 回调成功）都推 `request_updated`。推送只是“有变化”的提示：慢的订阅者（积压 64 帧）会被丢帧，网页每次连上或怀疑漏帧时必须全量重新拉取（`/api/inbox`、`/api/status`、`/api/rules` 等）。hub 每 30 秒复核一次登录并 ping，登录失效（登出、换密码、过期）时以 1008 关闭；网页不该发数据帧，发了就断开。

**webhook。** `POST /api/webhook/approvals`，`Authorization: Bearer <来源令牌>`，请求体至多 64 KiB：

```json
{"title": "部署 v1.2 到生产", "description": "可选，至多 4096 字符", "callback_url": "https://n8n.example.com/webhook/x",
 "callback_token": "可选，回调时原样带上", "expires_in": 3600}
```

`title` 必填、至多 200 字符、不能含控制字符或不可见的格式字符；`callback_url` 必填、只收 http/https；`callback_token` 至多 512 字节、只能是可见 ASCII；`expires_in` 可选，1 到 86400 秒，默认 24 小时。返回 `{"id"}`，请求以 `kind: webhook` 进收件箱（标题与描述先脱敏）。决定后 hub 回调：`POST callback_url`，头 `Authorization: Bearer <callback_token>`（给了令牌才带），body `{"id","decision":"allow|deny","note"}`；hub 判过期时回调 `decision: deny`、`note: expired`。每次超时 10 秒，最多 3 次、间隔 2 秒，不跟随重定向，只认 2xx；每次回调（成功或放弃）记审计 `webhook_callback`，只记回调地址的 scheme://host。hub 关停时正在进行的回调中止，重启后不恢复（令牌已清空，需来源重新提交）。

## 安全要点

- **只存哈希。** 机器密钥、登录会话 token、webhook 来源令牌、接入码存 SHA-256；网页密码用 argon2id（time 1、memory 64 MiB、threads 4、盐 16 字节）。**例外**：webhook 的出站回调令牌（`callback_token`）回调时要原样带上，明文存在 `requests.callback`，回调成功、3 次重试用尽或因关停中止后立即清空（用户决定 D）。
- **登录。** 每个来源 IP 每分钟 5 次（IPv6 按 /64 算一个来源），同时校验密码的上限 2 个，等不到名额回 503 且不占限速名额。cookie `railse_session`：HttpOnly、SameSite=Lax、Path=/、30 天，请求经 TLS 或开了 `--secure-cookies` 时加 Secure。换密码使所有已登录会话失效。
- **跨站。** 写接口、`/api/logout` 与 `/ws/client` 校验同源 Origin，是 SameSite 之外的第二道防线。
- **hub 的权力被限死。** 对请求只能回 allow 或 deny，没有向机器下发命令的通道。“记住”永远不会生成放行 `outward`、`destructive`（以及 `unknown`、`write_outside`）的规则，`learned` 来源的这类放行在导入时也被拒绝。网页导入的 `user` 规则可以放行 `outward`、`destructive`（用户决定 B）：这类规则在 `/api/rules` 标 `elevated: true`，导入时审计 `rules_imported` 带上它们的 id，从停用改为启用时审计 `rule_enabled_elevated`。
- **数据最小化。** 守护进程上报前对载荷与事件文本脱敏，hub 收到后再脱敏一次并截断各字段；hub 不信任守护进程送来的路径与命令，只存储与展示。审计里不含钩子进程的环境变量。
- **明文接入会警告。** `railse agent enroll --hub http://…`（非本机地址）提示明文传输密钥。

## 部署与反向代理

本计划不含 TLS（第 5 份计划提供内置 ACME）。公网部署时让 hub 只监听本机，由反向代理终止 TLS，并给 hub 加 `--secure-cookies`。

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

配套：`railse hub run --listen 127.0.0.1:8787 --secure-cookies --trusted-proxy 127.0.0.1`（nginx 经 IPv6 回环连 hub 时再加 `::1`）。用 Caddy 时 `reverse_proxy 127.0.0.1:8787` 即可（按 Caddy v2 文档：默认透传 Host、覆盖 X-Forwarded-For、自动代理 WebSocket）。

## 故障排查

| 现象 | 原因与处理 |
| --- | --- |
| 网页写操作或 `/ws/client` 得到 403 | 代理没传原 Host，或页面与 hub 不同源。见“部署与反向代理” |
| 导入规则 415 | `Content-Type` 要是 `application/yaml` 或 `text/yaml` |
| 登录或接入 429 | 同一来源一分钟内超过 5 次；在代理后面且没配 `--trusted-proxy` 时所有人共用一个来源 |
| 登录 503 | 还没 `railse hub password`，或同时校验密码的人太多（稍后重试） |
| webhook 503 | 还没 `railse hub webhook-token` |
| 守护进程日志“以 HTTP 401 拒绝了本机密钥” | 机器被吊销或密钥不对：`railse agent enroll --force` 后重启守护进程。停用前 hub 若处于暂停，本机会一直按 hub 暂停，重新接入后由 welcome 更新 |
| 守护进程日志“拒绝了握手…协议版本不一致” | 升级守护进程与 hub 中较旧的一方 |
| 请求不进收件箱、立即交回终端 | 看 `railse agent status` 是否暂停；看 `agent.log` 是否连着 hub；Gemini 的请求本来就不进 |
| `railse hub password` 回显密码 | 用 `read -s PW; printf '%s\n' "$PW" \| railse hub password` |
| hub 退出码 1、日志“不关数据库” | 关停时有在途请求、长连接处理或回调没在时限内退出，或服务出错结束；直接重启即可 |
| `railse hub run` 启动即退出 1、日志“监听失败” | 端口被占或 `--listen` 地址无效，换个地址 |

日志：`hub.log` 里有“hub 监听 …”、“可信代理: …”、“机器 … 超过 … 没有 pong，断开”、“webhook 请求 … 回调 … 失败 3 次，放弃”、“hub 已停止”。守护进程的 `agent.log` 里有“接入 hub …”、“已连接 hub …，规则版本 N”、“与 hub … 的连接结束: …，N 后重连”、“规则更新到版本 N”、“与 hub 断线超过 30s，请求 … 交回终端”。`GET /api/status` 的 `machines[].online` 看各机器是否连着；`GET /api/requests/{id}` 的 `audit` 看一条请求经历了什么。

## 已知限制（后续计划，不算未完成）

- TLS/ACME、Web Push、PWA 页面、开机自启、`railse doctor`、`railse agent status` 显示 hub 连接状态、本机 pause 反向同步到 hub。
- 复合命令不能“拒绝并记住”（422），需要手写规则。
- 吊销机器的瞬间正在登记的请求可能没被取消，按期限过期。
- hub 重启不恢复关停时正在进行的 webhook 回调，来源需重新提交。
