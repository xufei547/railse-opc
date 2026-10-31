# Railse OPC v0.2：最小英文面与 Show HN 发布 — 设计

日期：2026-11-01。总设计见 `2026-10-21-railse-opc-design.md`（§0.6 有本版的决策行）。本文只写 v0.2 要做的事、不做的事和为什么。

## 0 背景、目标、不做什么

**现状（2026-11-01 的数据）**：v0.1.5 发布后 GitHub 上 2 star、2 fork，近 14 天 23 个访客，各版本下载量 4 到 6 次（基本是作者装机与 CI），V2EX 帖只有作者自己的回复。作者自用两天：到手机的 28 条审批里 6 条在手机上批掉，22 条最后在终端解决或因 agent 重启作废。官方工具正在收掉"把审批推到手机"这层（Claude Code 的 Remote Control），Railse 能守住的是三样：规则层（七级风险、自动放行、记住、永不自动放行破坏性操作）、跨 CLI 的统一收件箱、自托管。

**2026-11-01 与作者逐题拍板的目标**：

| 问题 | 结论 |
|---|---|
| 先攻哪群人 | 英文圈先（Show HN、r/ClaudeAI、X 英文），国内顺带同日发中文版 |
| 看哪个数字 | GitHub star。发布后四周目标 300 以上；同时记录下载量与 issue 数，作为"真有人在用"的旁证 |
| 时间 | 两周内上 Show HN：11 月 3 日起做，11 月 12 日（周三，北京时间 20 到 22 点）发 |
| 路线 | 最小英文面：一个英文用户从 README 到手机上批掉第一条请求、答掉第一道题，全程不见中文。其余保持中文，下版再补 |

**不做的（明确写下，免得做着做着就塞进来）**：hub 与守护进程的日志；hub 接口返回的错误句子（网页偶尔会弹出来）；中文文档的英文版（README.en 之外的 docs/ 不翻）；英文为默认、中文为选项的全量翻译；任何新的卖点级功能（Codex、Gemini 的提问上手机，规则可视化编辑等）；Windows。

## 1 语言机制

代码里现在没有任何多语言机制，中文直接写在每个输出点（Go 约五千处字符串、网页约一千七百处）。两周内不重构成按键名查字典，用**中文原文当键**的办法：

### 1.1 Go 侧：`internal/i18n`

- `i18n.T(zh string) string`：中文原文进、当前语言的文案出。英文表 `internal/i18n/en.go` 是 `map[string]string`，键是中文原文（含 `%s`、`%d` 这类占位符，英文值保持同样的占位符、同样的顺序）。查不到就原样返回中文：没翻到的地方只是显示中文，绝不会崩，也不用一次翻完。
- 调用方式：`fmt.Fprintf(w, i18n.T("已写入 %s\n"), path)`。多行长文案（向导的说明段、doctor 的建议）同样整段做键。
- `i18n.Lang() string` 返回 `"zh"` 或 `"en"`。语言在进程启动时定一次，顺序：环境变量 `RAILSE_LANG`（`zh`/`en`）→ 数据目录里的 `lang` 文件（和 `machine.json` 同目录，内容就是 `zh` 或 `en`）→ `LC_ALL`、`LC_MESSAGES`、`LANG` 里第一个非空的值，前缀 `zh` 算中文 → 其余（包括 `C`、`POSIX`、什么都没有）一律英文。
- 为什么"什么都没有"算英文：英文系统的终端几乎都带 `LANG`，容器里常常没有，Show HN 的读者会在容器里试；中文用户只有作者一人，机器上跑一次 `railse lang zh` 就好。
- 新命令 `railse lang [zh|en]`：不带参数打印当前语言和它从哪来；带参数写 `lang` 文件。`railse setup` 向导跑完时，如果 `lang` 文件还不存在，把本次解析到的语言写进去——这样 launchd/systemd 下没有 `LANG` 的守护进程与之后的钩子进程也用同一种语言。
- 测试里 `i18n.Set(lang)` 临时切换并用 `t.Cleanup` 还原。

### 1.2 钩子与守护进程

- 钩子进程（`railse hook claude|codex|gemini`）跑在用户终端里，知道语言。它把 `lang` 放进发给守护进程的请求（`proto` 里钩子请求结构加一个字段，缺省按 `zh` 处理，旧钩子对新守护进程照旧）。
- 规则：**任何最终出现在用户终端里的句子，都按钩子请求带的 `lang` 渲染**，不管它是钩子自己打印的，还是守护进程回传的 `Reason`/`Message`。守护进程自己的日志不动。
- 回给 CLI 的 JSON 里的字段名、`permissionDecision` 之类的值不受影响，只有给人看的句子变。

### 1.3 网页：`web/src/i18n.ts`

- `t(zh: string, vars?: Record<string, string | number>): string`，英文表 `web/src/locales/en.ts`，键是中文原文；带变量的句子用 `{name}` 占位，两种语言各自排版。查不到回退中文。
- 语言解析：`localStorage` 里的选择（`auto`/`zh`/`en`）→ `navigator.language` 前缀 `zh` 算中文 → 其余英文。解析结果写到 `document.documentElement.lang`。
- 设置页加一组开关"语言 / Language：跟随系统、中文、English"。切换后立即生效（整个页面重渲染），并重新提交推送订阅（见 1.4）。
- `format.ts`、`explain.ts`、`notifications.ts`、`history.ts` 里那些**拼句子**的函数（`outcomeText`、`explainRequest`、推送标题的本地复述、相对时间等）不能逐词替换，按语言各写一条分支，英文句子自然、短，语气与 README.en 一致。
- PWA manifest 的 `name`/`short_name` 保持 `Railse`，`description` 改成英文一句话；iOS 只显示名字，中文用户无感。

### 1.4 推送文案跟订阅走

- 推送文案是 hub 生成的（`internal/hub/pushsvc.go`：标题"<CLI> 要<动词> · 项目"、正文两行、提问"Claude Code 在问你"、完成与等你说话）。订阅时网页把当前语言带上：`POST /api/push/subscribe` 请求体加 `lang`（缺省 `zh`）。
- `push_subscriptions` 加一列 `lang TEXT NOT NULL DEFAULT 'zh'`，库升到第 4 版；已有订阅默认中文，作者那条不受影响。
- 推送时按每条订阅的 `lang` 选文案：`cliName`、`riskVerb`、`pushTitle`、`askSummary`、`payloadSummary` 以及 done/question 两类的标题正文都分语言。同一条请求推给多台手机时各用各的语言。
- 网页在用户切换语言时，若已订阅，重新 `subscribe` 一次把新 `lang` 送到 hub（同一 endpoint 覆盖）。
- 设置页已有的"发一条测试通知"（`POST /api/push/test`）照旧，只是文案英文化；它是演示动图的开场，也是英文用户装完两分钟内的第一条推送。

### 1.5 install.sh

- 85 行中文。脚本里放两套文案，一个 `msg` 函数按键取：`RAILSE_LANG` → `LC_ALL`/`LANG` 前缀 `zh` → 中文，否则英文。脚本结尾"下一步做什么"那段也分两套。

### 1.6 测试与门禁

- **现有测试一律保持中文断言**：`make test`、CI 的 Go 测试加 `RAILSE_LANG=zh`；vitest 的 setup 文件把网页语言固定为 `zh`。不改七十多个 Go 测试文件和二十多个网页测试文件的断言。
- **词典完整性测试**（"看不到中文"的硬保障）：`internal/i18n` 里一个 Go 测试，遍历仓库源码，抓出所有 `i18n.T("…")`（Go）与 `t('…')`/`t("…")`（TS/TSX）的字面量键，逐条核对 `en.go` 与 `en.ts` 里都有；缺一条测试就红。英文表里多余的键只打印警告。常量拼接、变量传入 T 的写法禁止，测试里也检查 `T(` 的参数必须是字面量。
- **英文渲染测试**：向导首屏与结尾、doctor 汇总行、钩子的四句提示、`outcomeText`、`explainRequest`、推送标题正文、答题表单按钮，各加英文用例。
- **e2e**：Playwright 增加一个 `en` 项目（`locale: 'en-US'`），把 `approve.spec.ts` 与 `ask.spec.ts` 跑一遍，并断言收件箱、请求页、答题页、设置页的可见文本不含中日韩字符。
- **CI 的 install-script 作业**改成 `LANG=en_US.UTF-8` 下跑，抓取 install.sh、`railse --help`、`railse setup --help`、`railse doctor`（未配置时允许非零退出）的输出，断言不含中日韩字符。

## 2 英文面覆盖范围

按"英文用户前十分钟会碰到的面"划。

**命令行（全英文）**：

- install.sh 全部输出。
- `railse setup all --local-hub` 向导的每个提问、步骤说明、端口与 Tailscale 与 Funnel 的提示和报错、结尾的地址与手机三步；`railse setup hooks --claude|--codex|--gemini`、`railse setup codex-notify`、`railse setup autostart` 的输出；`railse hub password`、`railse hub token`、`railse hub run` 的启动行（向导会调用）。
- `railse doctor` 的每一项名称、ok/warn/fail 说明、建议动作、汇总行。
- 钩子留在终端里的句子：等手机、已批准、已拒绝、交回终端、连不上守护进程、守护进程版本旧。
- `railse --help` 与各子命令的帮助、`railse version`、`railse agent status`、`railse lang`。

**手机网页（所有页面）**：登录；收件箱（引导卡、空状态、暂停横幅、分组标题、卡片第一行）；请求页（解释卡、状态行、字段表、批准/拒绝面板、记住的范围与规则草案说明、答题表单、结果提示、"别处有了结果"的提示）；历史；规则（含导入导出与错误提示）；机器；设置（推送、暂停、语言、退出、版本）；网页自己发的本地通知与收掉旧通知的逻辑不变；通用错误句（连不上 hub、登录过期、提交失败）。

**推送三类**：审批（标题 `Claude Code wants to run a command · project`，正文命令一行加 `Open to approve or deny`）、提问（`Claude Code is asking you`）、完成与等你说话（`Claude Code finished · project`、`Claude Code is waiting for you · project`）。英文里 CLI 名用 `Claude Code`、`Codex`、`Gemini CLI`；动词按风险级：`read`、`edit files`、`run a command`、`write outside the project`、`use the network`、`push or publish`、`run a destructive command`（具体对应表在实现时按 `riskVerb` 现有的七个中文动词一一对上）。

**顺手的一处小修（为演示服务）**：答题页选完选项后，底部按钮固定在屏幕下沿，文字带上所选内容（中文"回答：Banana"，英文"Answer: Banana"；多选列出所选，自己填显示"回答：你填的内容"的前 20 字）。不做选完自动发：手机误触常见，多选与自己填反正要按钮，行为统一。这是 2026-11-01 作者真机验收时踩的坑（点了选项没点"回答"）。

## 3 验收路径

**一个英文系统上走完，全程不见一个汉字**：

1. `curl -fsSL …/install.sh | sh`，输出英文。
2. `railse setup all --local-hub`，向导英文，结尾给出地址与手机三步。
3. 英文 iPhone 打开地址、登录、开通知、点"Send a test notification"，收到英文推送。
4. 在一个项目里让 Claude Code 跑一条需要审批的命令：手机收到英文推送，批掉，终端里出现英文提示。
5. 让 Claude Code 用 `AskUserQuestion` 问一道题：手机收到 `Claude Code is asking you`，答掉，Claude 继续，终端不再问。
6. `railse doctor` 全英文、全通过。

**在哪做**：作者的 Mac 上用 `RAILSE_LANG=en` 跑命令行部分、手机在设置里切 English 做第 3 到 5 步，做完切回（`railse lang zh`、手机切回中文、重新订阅）；命令行的第 1、2（到 Tailscale 之前）、6 步另在一个干净的 Linux 容器里跑一遍。验收人边走边记下看到的每一个中文，全部清零才算过。

## 4 发布素材包

全部英文；国内版同日翻中文。素材放 `docs/launch/`（Show HN 正文、作者首评、预备问答、X 串、Reddit 帖、发布日流程单），README 本身在仓库根。

1. **README.en 首屏重写**。一句话定位：*A self-hosted approval inbox for people running Claude Code, Codex and Gemini CLI at once. Rules auto-allow the boring stuff; everything else goes to your phone.* 往下三个要点（规则优先、三个 CLI 一个收件箱、一台机器加 Tailscale 就起来）、英文演示动图、一条安装命令。新增一节 **Why not Claude Code's Remote Control?**，表格对比：官方只管一个 CLI、依赖账号与会话流、没有规则层；Railse 三个 CLI、规则优先、自托管、Codex 与 Gemini 也能用。**发布前一天按官方文档逐条核对**，不能有一句说错；核对记录写进 `docs/launch/facts.md`。
2. **英文演示动图**。`make demo`（`scripts/demo.sh`）在英文界面下重录为 `assets/demo.en.gif`，开场是"Send a test notification"到手机，然后一条审批、一道题。中文 README 保留现在的 `assets/demo.gif`。
3. **Show HN 帖**。标题 80 字内，形如 `Show HN: Railse – approve Claude Code/Codex/Gemini from your phone, rules first, self-hosted`；正文链接到仓库。作者首评讲故事：一个人、三个 CLI、手机、规则；诚实写限制：macOS 与 Linux、iPhone 要加到主屏幕、项目从中文起家刚做英文、Codex 与 Gemini 的提问只提醒不能答。
4. **预备问答十条**：钩子的安全边界（hub 只能说是或否、守护进程只向外连、答案只能是选项文字）、为什么不用 Remote Control、Windows、Codex 与 Gemini 的提问能不能手机答、数据放哪（自托管、SQLite）、Tailscale 是不是必须（不是，任何反向代理或 ACME 都行）、许可证（Apache 2.0 加 NOTICE）、和 YOLO 模式的区别、团队能不能用（能，但第一版按一个人设计）、怎么卸载。
5. **X 英文串**五到七条，附动图；**r/ClaudeAI** 一帖，发布当天或次日。
6. **国内顺带**：V2EX 原帖追加回复、即刻一条、小红书用 build-in-public 技能出两篇笔记，内容是"一个人把产品推上 Show HN"的过程本身。
7. **发布日流程单**：发帖时间、前六小时每条 HN 评论三十分钟内回（我起草、作者过目后发）、issue 当天回、第二天汇总数据。

**账号**：HN 账号由作者本人注册（助手不能代注册），发帖前让账号有一周以上的年龄；X 与国内账号沿用现在的方式（作者登录着，助手来发）。

## 5 排期、分工、风险

**两周，从 2026-11-03（周一）起**：

| 天 | 做什么 |
|---|---|
| 1 到 3 | 两个 Opus 5.5 会话并行。**Go 侧**：`internal/i18n`、`railse lang`、向导/doctor/钩子/帮助/hub 启动行的 T 包裹与英文表、钩子请求带 lang、install.sh 双语、词典完整性测试、CI install-script 英文门禁。**网页与 hub 侧**：`i18n.ts` 与英文表、设置页语言开关、拼句子函数的英文分支、订阅带 lang 与库升第 4 版、推送文案分语言、答题底部按钮、vitest 固定中文、e2e 的 en 项目。主会话同时写 README.en、Show HN 文案、问答、X 串，并开始核对 Remote Control 的事实 |
| 4 到 5 | 合并两条分支、过门禁、审阅会话通读两份英文表（语气、术语一致）、录英文动图、在作者 Mac 与 Linux 容器里走 §3 的验收路径 |
| 6 到 7 | 修验收发现的问题；发 **v0.2.0**（跨度大，不叫 0.1.6）；作者 Mac 升级并切回中文 |
| 8 | 素材终审；Remote Control 事实复核 |
| 10（11-12 周三 20 到 22 点） | 发 Show HN；随后 X 英文串、r/ClaudeAI；国内三处同日 |
| 11 到 13 | 守评论区、修 bug，必要时发 0.2.1 |
| 14（11-16） | 复盘：star、下载、issue、评论里的高频问题；定 v0.3 |

**砍单顺序（Go 侧来不及时）**：先砍各子命令的帮助文本，再砍 `hub password`/`hub token`/`hub run` 的启动行；向导、doctor、钩子提示、install.sh、网页、推送一个都不砍。

**风险**：

- 英文语气参差：两份英文表由两个会话各写，合并前专门一个审阅会话通读，对照 README.en 的语气改齐；术语表（approve/deny/remember/inbox/hub/daemon/rule/project/machine）写在 `internal/i18n/en.go` 文件头，两侧共用。
- 关于 Remote Control 的事实说错：发布前一天按官方文档逐条核对，拿不准的句子删掉不写。
- HN 没上首页：不算白做，素材隔几周改了再发一次；Reddit 与 X 不依赖 HN。
- 中文用户升级后终端没有 `LANG` 导致变英文：README 升级说明写明 `railse lang zh`；作者 Mac 升级时由助手直接跑。

## 6 决策记录

| 决策 | 为什么 |
|---|---|
| 中文原文当键而不是键名 | 两周内不重构；缺译回退中文永不崩；词典完整性测试能机械地保证覆盖 |
| 什么都没有时算英文 | 容器与极简环境是 Show HN 读者的常见场景；中文用户只有作者一人且有 `railse lang zh` |
| 推送语言跟订阅而不是跟 hub | 一个 hub 可能推给多台手机；订阅是手机的属性 |
| 答题不做选完自动发 | 手机误触常见；多选与自己填反正要按钮；只把按钮做显眼 |
| 不写独立博客文章 | HN 读者看的是 Show HN 正文、作者首评与 README；故事放在这三处，省一份渠道 |
| 11-12 周三而不是 11-11 周二 | 11-11 是美国假日，HN 流量偏低 |
| 版本号 v0.2.0 | 多语言机制、库升第 4 版、钩子协议加字段，跨度大于补丁 |
