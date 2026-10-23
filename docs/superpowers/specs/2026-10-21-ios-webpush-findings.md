# Railse OPC iOS Web Push 实验结果（设计文档第 8 节第 5 项）

- 日期：2026-10-21
- 手机：作者的 iPhone，iOS 26.3（作者确认是 26.x；小版本由 Safari UA 里的 `Version/26.3` 推断，UA 里的 `iPhone OS 18_7` 是 Safari 冻结的假值，见第 4 节）
- 托管：cloudflared 临时隧道（`https://<随机词>.trycloudflare.com`）转发到 Mac 上的 Node 服务；发送端同在 Mac 上，用 `web-push` 3.6.7
- 对应设计：`2026-10-21-railse-opc-design.md` 第 8 节第 5 行
- 实验代码：`spike/webpush/`（已在 `.gitignore`，未入库）；VAPID 私钥只在本机 `spike/webpush/data/vapid.json`

## 0. 一页结论

| 状态 | 是否送达 | 发送到手机回执的延迟 | 点通知后是否打开到指定页面 | 依据 |
|---|---|---|---|---|
| 前台（应用在屏幕上） | 送达，系统横幅照常弹出 | 1.35 至 1.61 秒（5 次） | 是，打开 `landing.html?id=…` | 服务器回执显示页面 `visible/focused`；作者确认看到横幅并点开 |
| 后台（应用在后台、未被划掉） | 送达 | 2.21 秒、2.50 秒（2 次） | 是 | 回执显示页面 `hidden`；两次点开都落到对应 id 的落地页 |
| 完全关闭（多任务里划掉） | 未验证 | | | 见 3.3 |
| 锁屏 | 未单独验证 | | | 18:18 那次回执为 `hidden`，分不清是后台还是锁屏；见 3.3 |
| 订阅数小时后仍有效 | 未验证 | | | 订阅后约 17 分钟内七次发送全部成功；见 3.3 |

结论：**部分成立。** iOS 26.3 上，加到主屏幕的 PWA 在前台和后台都能收到 Web Push，从发送到手机回执 1.4 至 2.5 秒，点通知能打开到指定页面；推送链路本身（VAPID、aes128gcm 加密、`web.push.apple.com`）没有问题。完全关闭、锁屏、数小时后订阅是否仍有效三项没有拿到证据，作者决定不再继续测。

建议：第一版仍然只做 Web Push，暂不加 Bark；把这三项列为 PWA 收件箱里程碑（设计文档第 7 节第 5 步）真机联调的验收项。如果届时完全关闭或锁屏收不到，再按第 8 节的替代方案加 Bark 渠道。

## 1. 实验方法

### 1.1 测试页

`spike/webpush/public/` 下一个不依赖任何框架的最小 PWA：

- `manifest.webmanifest`：`display: standalone`，`start_url: /?source=homescreen`，`scope: /`，192 与 512 两个图标；页面另带 180 像素的 `apple-touch-icon`。
- `sw.js`：
  - `push`：先 `showNotification`（标题、正文、`tag` 为推送 id，`data` 里带目标 URL），再把“收到了”连同 `clients.matchAll()` 看到的窗口列表（URL、`visibilityState`、`focused`）回传服务器。
  - `notificationclick`：先尝试对已有窗口 `navigate()` 到目标 URL 并 `focus()`；拿不到窗口或 `navigate` 返回空时退回 `clients.openWindow()`；回传走了哪条路。
  - `pushsubscriptionchange`：只回传事件。
- `index.html`：显示模式、Service Worker、PushManager、通知权限、订阅状态和 UA；“开启通知”按钮里 `Notification.requestPermission()` 是点击处理函数的第一个调用，授权后用 VAPID 公钥 `pushManager.subscribe({userVisibleOnly: true})`，把订阅 JSON 显示在页面上并 POST 回服务器。
- `landing.html`：通知的目标页，读 URL 里的 id，打开即回传 `landed`。

### 1.2 服务端与发送端

`server.js` 用 Node 自带 `http`：

- 公开端口（进隧道）：静态文件、`/api/vapid`、`/api/subscribe`、`/api/event`，以及 `/api/schedule`（手机上点按钮后延迟 15 至 20 秒再发，给作者时间切状态）。
- 管理端口只绑 127.0.0.1，不进隧道：`/send` 立即发一条、`/log`、`/subs`。
- 每条推送 `TTL: 3600`、`urgency: high`；载荷为 JSON（id、标签、标题、正文、目标 URL、发送时刻）。VAPID `subject` 用页面自己的 https 源。

### 1.3 怎么判定状态和延迟

- 延迟 = 服务器收到 Service Worker 回执的时刻 − 服务器开始调用 Apple 推送接口的时刻，两端都是 Mac 的时钟，不受手机时钟偏差影响。它包含 Apple 接口本身的耗时（实测 0.72 至 1.10 秒，全部返回 201）和回执经隧道回来的时间，所以是上限。
- 状态不靠口头约定，看回执里的窗口列表：`visible/focused` 是前台，`hidden` 是后台或锁屏，空列表是进程已不在（完全关闭）。下表按回执判定，而不是按按钮上的标签，因为实测中有四次推送到达时手机并不在标签所说的状态（例如作者刚点开上一条通知，应用又回到了前台）。

## 2. 安装与订阅

| 步骤 | 实测 |
|---|---|
| 相机扫码在 Safari 标签页打开 | 显示模式 `browser`；`PushManager` 不存在，`Notification` 不存在，`ServiceWorkerRegistration.showNotification` 也是 `undefined`，但 Service Worker 本身能注册且为 active |
| 分享 → 添加到主屏幕 → 从图标启动 | 显示模式 `standalone`，三者都有，`Notification.permission` 为 `default` |
| 点“开启通知” | 系统弹授权框，选允许后 0.9 秒内拿到订阅；endpoint 主机 `web.push.apple.com` |

## 3. 各状态实测

### 3.1 前台与后台（全部记录）

时间为北京时间。

| 发出 | 按钮标签 | 回执判定的实际状态 | 延迟 | 点通知 |
|---|---|---|---|---|
| 18:04:50 | 前台（Mac 上手动发） | 后台：`hidden`，停在首页 | 2213 ms | 点开，落到该 id 的落地页 |
| 18:11:18 | 前台 | 前台 | 1441 ms | 作者看到横幅，未点 |
| 18:12:13 | 锁屏 | 前台（推送到时尚未锁屏） | 1352 ms | 未点 |
| 18:15:27 | 前台 | 前台 | 1612 ms | 点开，落到该 id 的落地页 |
| 18:15:40 | 后台 | 前台（作者刚点开上一条，应用回到前台） | 1549 ms | 点开，`navigate` 路径，落到该 id 的落地页 |
| 18:18:38 | 闲置（Mac 上手动发） | 后台：`hidden` | 2496 ms | 点开，落到该 id 的落地页 |
| 18:19:28 | 关闭+锁屏 | 前台（作者停在上一条的落地页） | 1469 ms | 点开，`openWindow` 路径，落到该 id 的落地页 |

七条全部送达，没有丢失；点开的五次全部落到正确的落地页。

### 3.2 应用在前台时

iOS 照常弹出系统横幅，不会因为应用在前台而吞掉通知。Railse 的网页在前台时同时会经 WebSocket 收到同一条审批，等于提醒两次；可以接受，必要时用同一个 `tag` 让通知中心只留一条。

### 3.3 完全关闭、锁屏、数小时后：未验证

- 完全关闭：按“完全关闭”和“关闭+锁屏”标签发的两次，推送到达时应用都还在前台（见 3.1），没有一次回执是空窗口列表，所以没有证据。
- 锁屏：18:18 那次回执为 `hidden`，但 `visibilityState` 区分不了“在后台”和“锁屏”；按“锁屏”标签发的那次到达时屏幕还亮着。
- 数小时后：订阅于 18:02 建立，最后一次成功发送在 18:19，只覆盖了 17 分钟。

原计划改由 Mac 在 18:20 之后 30 分钟、1 小时、2 小时、3 小时各自动发一条（不需要作者操作，回执里窗口列表为空即可证明应用已不在），作者在 18:27 决定不再继续，自动发送在第一条发出前已停止。

补测方法：`spike/webpush/` 可以原样复用，见本文第 7 节。

## 4. 遇到的坑

1. **只有从主屏幕图标启动才有推送能力。** 在 Safari 标签页里，`PushManager`、`Notification` 都不存在，连 `registration.showNotification` 都是 `undefined`；Service Worker 却能正常注册，所以“SW 是 active”不能当作“能推送”的信号。PWA 首屏必须先判断 `display-mode: standalone`，不是就引导用户加到主屏幕。
2. **UA 里的 iOS 版本不可信。** iOS 26 的 Safari UA 固定写 `CPU iPhone OS 18_7`，真实大版本只能从 `Version/26.3` 推断。`railse doctor` 或 hub 不要按 UA 判断 iOS 版本。
3. **从通知回传“已点击”不可靠。** `notificationclick` 里先跳转、后 `fetch` 回传：五次点开只收到两次回传，而落地页自己的回传五次全到。没回传的三次里两次应用原在后台、一次原在前台，和状态没有明显关系，多半是页面接管后 Service Worker 很快被挂起。结论：“点通知后打开了哪条审批”由目标页面读 URL 自己处理，不要依赖 Service Worker 在点击后再做网络请求或给页面发消息。
4. **`navigate` 不总能用。** 同样有一个已存在的窗口，一次 `client.navigate()` 成功，一次返回空、退回 `openWindow()` 才打开。两条路最终都落到正确页面，实现时两条都要写。
5. **点开通知会把应用拉回前台。** 连续做多个状态的测试时，上一条通知一点，下一条就变成前台测试。判定状态要看回执里的 `visibilityState`，不能信操作约定。
6. **订阅和主屏幕应用都绑在源（域名）上。** 临时隧道每次重启域名都会变，变了就要重新加主屏幕、重新授权。正式的 hub 从第一天起就要用固定域名，换域名等于让所有用户重装。
7. **VAPID `subject` 用 https 源可用。** 本次用页面自己的 https 源，七次全部 201。社区反馈 Apple 会以 `BadJwtToken` 拒绝 `mailto:…@localhost` 一类的 subject，本次没有实测；hub 用自己的正式域名即可。

## 5. 对设计的影响

1. **hub 从第一天起用固定域名。** 订阅和主屏幕应用都绑在源上，换域名等于所有用户重装、重新授权（第 4 节第 6 条）。
2. **PWA 首屏先判断是否从主屏幕启动。** 不是 `standalone` 就只显示“添加到主屏幕”的引导，不显示“开启通知”按钮；能否推送看 `'PushManager' in window`，不看 Service Worker 状态（第 4 节第 1 条）。
3. **“开启通知”必须是用户点击里的第一个调用**，本次按此实现，一次成功。
4. **推送载荷带审批 id 和目标 URL**，Service Worker 收到后必须 `showNotification`；点击时 `navigate` 与 `openWindow` 两条路都要写；打开哪条审批由页面读 URL 决定，不依赖点击后的 Service Worker 回传（第 4 节第 3、4 条）。
5. **应用在前台时 iOS 照样弹横幅。** 与 WebSocket 推到页面的同一条审批会重复提醒，用审批 id 作 `tag`；是否在前台时压掉横幅不可控，因为 iOS 要求每条推送都显示通知。
6. **第 8 节第 5 行的替代方案（Bark）暂不启用**，等三项补测结果再定（第 0 节）。

## 6. 未验证与遗留

- 完全关闭、锁屏、数小时后订阅是否仍有效（第 3.3 节）。
- 专注模式、通知摘要、低电量模式下的表现没有测。
- 只测了一台手机、一个 iOS 版本、一条网络（作者当时的 Wi-Fi）。
- 声明式推送（Declarative Web Push，iOS 18.4 起）没有测，本次全部走 Service Worker 的 `push` 事件。

## 7. 补测方法

实验产物留在本机 `spike/webpush/`（不入库），复用步骤：

1. `cd spike/webpush && cloudflared tunnel --url http://127.0.0.1:8787`，记下 `trycloudflare.com` 地址写入 `data/tunnel-url.txt`。
2. `VAPID_SUBJECT=$(cat data/tunnel-url.txt) node server.js`。
3. 隧道地址变了，所以手机上要删掉旧的“Railse推送”图标，用 Safari 打开新地址重新加到主屏幕、开启通知。
4. 作者把应用从多任务里划掉并锁屏后，在 Mac 上 `curl 'http://127.0.0.1:8788/send?label=closed-locked'`；`curl http://127.0.0.1:8788/log` 里 `push-received` 的 `clients` 为空列表即证明“完全关闭”下送达。
5. 数小时后再发一次同样的命令，返回 201 且有 `push-received` 即订阅仍有效；返回 404 或 410 表示订阅已失效。

## 8. PWA 真机联调（第 4 份）

第 4 份计划 Task 10 把推送接进了正式的 PWA：`web/public/sw.js`（每条推送都弹通知，点通知让已打开的窗口跳到 `/r/<id>`、跳不了就开新窗口，订阅更换时重新登记）、设置页的通知区块（开启、关闭、发一条测试通知；iOS 的 Safari 标签页只显示加到主屏幕的步骤）、收件箱的“通知已失效”横幅。下面的真机检查由作者在 iPhone 上做，做完把结果填进 8.2 的表。

### 8.1 步骤

1. 准备固定的 https 域名。没有正式域名时用临时隧道：`cloudflared tunnel --url http://127.0.0.1:8787`，记下 `https://<随机词>.trycloudflare.com`。临时域名每次重启隧道都会变，变了要删掉主屏幕上的旧图标重来（第 4 节第 6 条）。
2. 起 hub：`railse hub run --public-url https://<域名> --secure-cookies --trusted-proxy 127.0.0.1`。日志里应有“Web Push 已启用，对外源 https://<域名>”。没设过网页密码的先 `railse hub password`。
3. iPhone 上用 Safari 打开 `https://<域名>`：设置页“通知”一栏应只显示加到主屏幕的三步，没有“开启通知”按钮；收件箱顶部有加到主屏幕的引导。
4. 分享 → 添加到主屏幕 → 从主屏幕图标打开 → 登录。
5. 设置 → “开启通知”：系统弹授权框，选允许；底部提示“通知已开启”，状态变成“已开启”。
6. 点“发一条测试通知”：底部提示“测试通知已发出”，几秒内手机弹出横幅“Railse / 通知正常”；点它回到应用首页。
7. 触发一次真实审批。最简单是在另一台机器上发 webhook（先 `railse hub webhook-token` 拿令牌）：

   ```sh
   curl -sS https://<域名>/api/webhook/approvals \
     -H "Authorization: Bearer <令牌>" -H 'Content-Type: application/json' \
     -d '{"title":"真机联调","callback_url":"https://example.com/x"}'
   ```

   也可以在接入了守护进程的机器上让 Claude Code 跑一条需要审批的命令。手机应弹出推送（webhook 的标题是“webhook”，正文是上面的 title），点开直达这条请求的详情页。
8. 完全关闭：在多任务里把 Railse 划掉、锁屏，再按第 7 步触发一次，看是否收到、点开是否直达详情。
9. 锁屏：应用留在后台（不划掉）、锁屏，再触发一次。
10. 数小时后：不碰手机，三小时以上后再触发一次。

每次触发后可以在 hub 上看 `GET /api/requests/<id>` 的审计：`push_sent` 的 `sent` 为 1 说明推送服务收下了；`failed` 不为 0 时看 `hub.log` 的“推送到 … 失败”。写接口回 403“跨站请求被拒绝”时，检查隧道或反向代理有没有把原 Host 头转给 hub。

### 8.2 结果

| 项目 | 期望 | 结果 | 备注 |
|---|---|---|---|
| 主屏幕打开 | 从图标打开是独立窗口；设置页出现“开启通知” | 待作者 iPhone 真机检查 | |
| 开启通知授权 | 点按钮即弹系统授权框；允许后显示“已开启” | 待作者 iPhone 真机检查 | |
| 测试通知 | 几秒内弹出“Railse / 通知正常” | 待作者 iPhone 真机检查 | |
| 真实审批推送并点开直达 | 收到推送；点开落到 `/r/<id>` 详情页（记下走的是已开窗口跳转还是新开窗口） | 待作者 iPhone 真机检查 | |
| 完全关闭 | 划掉应用后仍收到；点开直达详情 | 待作者 iPhone 真机检查 | |
| 锁屏 | 锁屏时收到，锁屏界面有通知 | 待作者 iPhone 真机检查 | |
| 数小时后 | 三小时以上后仍收到（订阅没被吊销；设置页仍是“已开启”，收件箱没有失效横幅） | 待作者 iPhone 真机检查 | |

### 8.3 已在桌面 Chrome 上自动验证的部分

本机真 hub（`--public-url` 填一个假的 https 源，只用作 VAPID 的 sub）加系统 Chrome（Playwright 驱动、无头、持久的用户数据目录，经 FCM 收推送）跑过一遍，36 项全过，全程没有 CSP 违规：开启时点击里第一个调用是授权请求；打开页面只用只读的检查、不重新登记；测试推送与 webhook 推送都到达并弹出通知（标题、正文、tag、`/r/<id>`、图标都对）；在 Service Worker 里派发点击事件，已打开的窗口跳到了详情页；Service Worker 发的登记请求带 cookie 与同源 Origin，hub 收下；hub 删掉订阅或换钥后收件箱出现失效横幅，重新开启会先退旧订阅、换钥后不报 InvalidStateError；开启时 hub 已停用推送回 503，提示 hub 原文并撤掉本机订阅；关闭通知；没给权限、不支持推送、iOS 标签页、连不上 hub 各自的提示。

没法在桌面上验证、要靠真机的：iOS 的授权手势规则、点通知时“开新窗口”这条路（需要真实点击）、`pushsubscriptionchange` 真的触发（Chrome 不发这个事件；处理函数用手动派发的事件跑过），以及 8.2 的全部项目。
