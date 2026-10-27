# Railse OPC：开源门面与工程债（v0.1.1 之后）

**目标：** 推广前把仓库门面补齐（中英 README、贡献指南、issue 模板、安全说明、60 秒演示 gif），顺手还四笔工程债（vitest 4 与 npm audit 清零、CI Action 升级、setup 自动接 Codex notify 与重跑确认、系统目录里的 git 仓库不当项目根）。

**执行方式：** superpowers:subagent-driven-development。每个 Task 一个实现子代理、一轮对抗性审查（实现与审查都用 Opus 5.5），审查到 Approved、`make test` 全过才进下一个。顺序 A1 → A2 → A3 → A4 → B1 → B2 → B3 → B4。

## Global Constraints

- Go 不新增 require：`go.mod`、`go.sum` 不变。
- 每个 Task 提交前跑 `make test`（gofmt、golangci-lint、go vet、go test -race、网页 tsc 与 vitest）且全过；动到网页或钩子流程的 Task 再跑 `make e2e`。
- 提交信息用中文；每个 Task 至少一个提交，审查后的修复另起提交。不加 `--no-verify`。
- 不合并、不 push；不碰用户目录下的真实配置（`~/.claude`、`~/.codex`、`~/.gemini`、`~/.railse` 等），测试与录制一律用临时 HOME、临时数据目录。
- 代码注释、日志、终端提示用中文，跟周围代码的密度与写法一致。
- 文档文风（A 组所有 Markdown）：照作者本人的 README 写（样本 `/Users/xufei/dev/huanchuang/README.md`，次要样本 `/private/tmp/claude-501/-Users-xufei-RailseWork/4d46d2f9-9961-4cd1-9d27-51402e55d81d/scratchpad/style-samples/tuoguan-homework.md`）。第一段一两句话说清是什么、给谁用；要点是“加粗短语 + 一句具体的话”；安装和上手是编号步骤，命令直接给、复制就能跑；限制照实说；短句、具体数字、不堆形容词。不用“赋能、强大、无缝、一站式、让我们、探索、轻松、优雅、革命性”这类词，不用感叹号，不用 emoji，不用破折号式修辞（“——”“ — ”），不写“为什么选择我们”；中文用「」引用界面文字。英文同样朴素，像工程师自己写的，不用 “Welcome to”“Features at a glance”“seamless”“powerful”“effortless”“supercharge” 这类开头和词。
- 文档里的命令、参数、路径、版本号、行为描述必须与仓库现状一致（以代码与 `docs/*.md` 为准），不许写还没有的功能。

---

### Task 1: A1 中英 README

**文件：** 重写 `README.md`（中文），新增 `README.en.md`（英文）。

**要求：**
- 两个文件开头（logo 之后、标题之前或紧跟标题）互相链接：中文版 `[English](README.en.md)`，英文版 `[中文](README.md)`。保留顶部 logo：`<p align="center"><img src="assets/logo.svg" width="112" alt="Railse"></p>`。
- 先把两个文风样本从头读完，再动笔。写完自己逐句对照 Global Constraints 的文风条目检查一遍。
- 内容与顺序（标题可以按文风自己定，不要“Features”式标题）：
  1. 第一段：一两句话说清这是什么、给谁用（用策略引擎自动审批 Claude Code、Codex CLI、Gemini CLI 的工具调用，定不了的进手机收件箱批准或拒绝；给一个人同时开几个 AI 编程 agent 的人）。
  2. 三到五个要点，每个“加粗短语 + 一句具体的话”。可选素材（以代码与文档为准）：规则先定，定不了才问手机；出故障不卡 agent 也不误批（守护进程不在、hub 最多等 30 秒连不上时交回终端）；批准时可以「记住」成规则；hub 自托管、只存哈希；载荷先脱敏再出机器。
  3. 怎么工作：保留现有 ASCII 流程图（可精简），配两三句话，不要长段落。
  4. 安装：一行脚本 `curl -fsSL https://raw.githubusercontent.com/xufei547/railse-opc/main/install.sh | sh`，一两句说明支持的平台与装到哪里；`go install` 一句话带过（不带网页，只适合工作机）。
  5. 三步上手，编号步骤：hub 机器（尽量两条命令；做不到就如实写最少的那几条，准确优先于条数；Linux 装成系统服务的细节不展开，指向 `docs/setup.md`）、每台工作机一条 `railse setup`（也给出 `railse setup all --hub … --token …` 的一次给全写法）、手机打开地址登录后加到主屏幕并开通知（iPhone 用 Safari「分享」→「添加到主屏幕」）。
  6. Fly 部署一段：几行命令或一句话加链接，指向 `docs/hub.md` 的「部署到 Fly.io」一节，说清 `fly.toml` 里要改什么。
  7. 文档索引：`docs/setup.md`、`docs/hub.md`、`docs/web.md`、`docs/agent-local-loop.md`、设计文档；另加 `CONTRIBUTING.md`、`SECURITY.md`（这两个文件在后面的 Task 里加；链接先写上）。
  8. 现状与限制：v0.1.1，第一版，作者自用中；Gemini CLI 只能拒不能批（定不了的交回终端）；Codex 钩子只在交互会话里生效（`codex exec` 不触发）；hub 单机单库；Web Push 在 iPhone 上要先加到主屏幕；英文版要写明手机网页和命令行提示目前只有中文。其他限制以 `docs/*.md` 里写明的为准，挑对用户有用的写，不要罗列内部细节。
  9. 开发：两三行，指向 `CONTRIBUTING.md`。
  10. 许可证：Apache License 2.0，链接 `LICENSE`。
- 不要放演示 gif（Task 4 加），不要留空的图片链接或占位符。
- 英文版与中文版内容对应，但按英文习惯写，不逐句直译。
- 篇幅：中文版控制在现有 README 的六成以内；把现有 README 里大段的 Linux 系统服务细节移交给 `docs/setup.md`（那里已有，不用改 docs）。

**验证：** 文档里出现的每条命令、参数在代码里能找到（`railse … -h` 或源码 usage 字符串）；链接的文件都存在（CONTRIBUTING.md、SECURITY.md 除外）。`make test` 全过。

**审查重点：** 除常规外，审查子代理专门按“像不像作者本人写的、有没有 AI 味、和 huanchuang README 风格是否一致”判一轮，逐条列出有 AI 味的句子与改法；不像就改，改到审查通过。

**提交：** `docs: 重写中文 README，新增英文 README`

---

### Task 2: A2 CONTRIBUTING.md

**文件：** 新增 `CONTRIBUTING.md`（中英双语，同一文件，中文在前、英文在后），全文不超过 60 行。

**内容：**
- 跑测试：`make test`（gofmt、golangci-lint、go vet、go test -race、网页 tsc 与 vitest），`make e2e`（真实 hub、守护进程与钩子，Playwright；首次会下载 Chromium）。需要的工具：Go（版本见 `go.mod`）、Node（与 CI 一致；B2 之后是 22，写“Node 22”）。
- 提交信息用中文为主，不会写中文的贡献者可以写英文。
- 改规则引擎（`internal/policy`）要往 `internal/policy/testdata/shell_corpus.tsv` 加语料（说清格式：一行一条命令、制表符、期望的风险档；以文件实际格式为准）。
- 改脱敏（`internal/redact`）：新输入加进 `internal/redact/shell_test.go` 的用例；跑模糊测试 `go test ./internal/redact -run '^$' -fuzz FuzzString -fuzztime 1m`；再做 bash 差分核对（脱敏前后交给 bash 执行的命令要一致，不能把会执行的部分藏进 `<redacted>`）。差分核对的脚本目前不在仓库里，照实写“合并前由维护者跑”，不要编造命令。
- PR 要过 CI（`.github/workflows/ci.yml` 的各个作业）。
- 安全问题不要开 issue，见 `SECURITY.md`。

**提交：** `docs: 加贡献指南（中英）`

---

### Task 3: A3 issue 模板与 SECURITY.md

**文件：** 新增 `.github/ISSUE_TEMPLATE/bug.md`、`.github/ISSUE_TEMPLATE/feature.md`、`SECURITY.md`。

**issue 模板：** GitHub issue 模板格式（YAML front matter：`name`、`about`、`labels`，`name`/`about` 中英各写一份或并排写）。正文每个小标题中英并排一行（如 `### 发生了什么 / What happened`），下面留空或一行提示。bug 模板要有：版本（`railse version` 的输出）、系统、用的哪个 CLI 及版本、复现步骤、期望与实际、相关日志（`~/.railse/agent.log`、hub 的 `hub.log`，提醒先删掉密钥）。feature 模板：想解决什么问题、设想的做法、其他做法。两个模板都短。

**SECURITY.md（中英双语，同一文件，中文在前）：**
- 报告方式：发邮件到 xufei547@gmail.com，不要开公开 issue；说清会多久回复（写“一周内回复”这种具体但不过度承诺的话）。
- 支持的版本：只修最新发布版。
- 设计内的取舍（以 `docs/hub.md` 的安全一节与代码为准，逐条核对）：hub 对机器密钥、登录会话、webhook 来源令牌、接入码只存 SHA-256，网页密码用 argon2id；例外：VAPID 私钥明文存在 hub 的数据目录（推送签名要用），webhook 的出站回调令牌明文存到回调完成后清空；脱敏的残余风险（按规则脱敏，规则没认出的密钥会原样进 hub 与手机，`has_redacted` 只表示认出了一些；本机审计里存的是什么，以代码为准）；hub 只能回答放行或拒绝，没有向机器下发命令的通道。
- 不写赏金，不写 PGP（没有就不写）。

**提交：** `docs: 加 issue 模板与安全说明`

---

### Task 4: A4 60 秒演示

**文件：** 新增 `web/scripts/record-demo.ts`（录制脚本）、录制用的 Playwright 配置（如 `web/scripts/record-demo.config.ts`）、必要的 shell 脚本（如 `scripts/demo.sh`），`Makefile` 加 `demo` 目标，产物 `assets/demo.gif`；两个 README 嵌入 gif。

**流程（照 `web/e2e/approve.spec.ts` 与 `web/e2e/global-setup.ts` 起真实 hub 与守护进程的方式）：** 登录 → 收件箱空（「没有等你处理的事」）→ 一条审批请求实时出现，导航上的收件箱角标变成 1 → 点开 → 批准并勾「记住」→ 结果提示 → 规则页多一条「学来的」。

**控制者裁定：**
- 演示里的审批请求用真实钩子（`railse hook claude --socket …` 喂录制的 Claude PermissionRequest 样本，命令如 `curl https://api.example.com/v1/x`），不用 webhook：hub 不允许把 webhook 请求「记住」成规则（会回 422），而流程要求「批准并记住 → 规则多一条」。
- Web Push 在无头浏览器里录不出来（要真实推送服务，通知是系统层，不在页面视频里），不伪造通知横幅。用收件箱实时出现与角标表现“送到了手机”。可以在页面顶部或底部叠一条很短的中文字幕说明这一步在发生什么（如「终端里 Claude Code 要跑 curl，规则定不了，送到手机」），字幕用 `page.evaluate` 插 DOM、用 CSSOM 设样式（不能触发 CSP 违规），文字照 Global Constraints 的文风。字幕不超过 4 条。
- 视口 390×844，`isMobile`、`hasTouch`，`locale: 'zh-CN'`；录像尺寸显式设成 390×844（Playwright 默认会缩到 800×800 以内）。
- 节奏：每步之间留够看清的停顿，总时长不超过 60 秒；录像开头的空白帧剪掉。
- 转 gif：用本机 `/opt/homebrew/bin/ffmpeg`（Makefile 里用 `FFMPEG ?= ffmpeg`，没有时给出明确报错），两遍调色板（palettegen/paletteuse），限宽 390，10 fps，循环播放；目标小于 3 MB（调色板与抖动参数可调）。转不出 gif 就留 `assets/demo.webm` 并在报告里说明原因。
- `make demo`：从头构建网页与二进制（临时目录，同 `scripts/e2e-web.sh`），起 hub 与守护进程（临时目录、随机端口、临时 HOME），录制，转 gif 写到 `assets/demo.gif`，退出时停掉进程、删掉临时目录。可以复用 `scripts/e2e-web.sh` 与 `web/e2e/global-setup.ts`（例如 e2e-web.sh 把额外参数原样传给 playwright test），但 `make e2e` 不能跑到录制脚本，`make demo` 也不能跑 e2e 用例。
- 新的 TypeScript 文件要被 `npm run check` 的 `tsc --noEmit` 检查到（必要时改 `web/tsconfig.json` 的 include）；vitest 不能把录制脚本当测试收进去。
- 两个 README 在第一段与要点之后（或紧跟第一段）嵌入 `assets/demo.gif`，alt 文字写清是什么；英文版说明界面是中文。

**验证：** 跑 `make demo` 两次都成功，`assets/demo.gif` 存在，报告里写大小（字节与 MB）与时长（`ffprobe` 或 ffmpeg 输出）；`make test`、`make e2e` 全过；`git status` 干净（录像中间产物不入库，必要时加进 `.gitignore`）。

**提交：** `demo: 加 60 秒演示录制（make demo）与 README 里的 gif`

---

### Task 5: B1 vitest 4 与 npm audit 清零

**文件：** `web/package.json`、`web/package-lock.json`，以及 vitest 4 不兼容时要改的配置或测试。

**要求：**
- `vitest` 升到 4.x 最新（写这份计划时是 4.1.12，`^4.1.12`），不升到 5。`npm audit` 输出 0 个漏洞（现状：`@vitest/mocker`、`tinypool` 3 个，来自 vitest 3）。
- 只动开发依赖；`dependencies`（react、react-dom、react-router）版本不变。其他开发依赖只在 vitest 4 要求或 audit 要求时才动，动了在报告里说明原因。
- 按 vitest 4 的迁移说明处理不兼容（配置、`vi` API、类型）；测试数量与升级前一致（报告里给出前后的文件数与用例数）。
- 本机 Node 是 20.20，CI 在 B2 之后用 22；vitest 4 要求 Node 20 或 22 以上，两边都要能跑。

**验证：** `cd web && npm ci && npm audit`（0 vulnerabilities）、`npm run check`、`npm run build`；`make test`、`make e2e` 全过。

**提交：** `web: vitest 升到 4，npm audit 清零`

---

### Task 6: B2 CI 工作流升级

**文件：** `.github/workflows/ci.yml`、`.github/workflows/release.yml`；文档里提到 Node 版本的地方（`grep -rn "Node 20\|node 20\|node-version"` 找）。

**要求：**
- 所有 `setup-node` 的 `node-version` 改成 22。
- Action 升到当前主版本（2026-10-27 查 GitHub 最新发布）：`actions/checkout@v7`、`actions/setup-go@v7`、`actions/setup-node@v7`、`actions/upload-artifact@v7`、`goreleaser/goreleaser-action@v7`。每个都读一遍从现用主版本到新主版本之间的发布说明（`gh api repos/<owner>/<repo>/releases --paginate --jq …` 或 `gh release view`），确认我们用到的输入（`go-version-file`、`go-version`、`check-latest`、`cache`、`cache-dependency-path`、`fetch-depth`、`name`、`path`、`distribution`、`version`、`args`）语义没变；有变化就照新语义改，并在报告里列出每个主版本的破坏性变更与处理。
- 不改作业结构与步骤，只改版本与因版本变化必须改的输入。
- 本地校验：`ruby -ryaml -e 'ARGV.each { |f| YAML.load_file(f) }' .github/workflows/*.yml`；`make release-check` 通过。
- 文档里写“Node 20”的改成 22（README、CONTRIBUTING、docs）。

**提交：** `ci: Node 升到 22，各 Action 升到当前主版本`

---

### Task 7: B3 setup 自动接 Codex notify，重跑先确认

**文件：** `cmd/railse/setup.go`、`cmd/railse/setup_wizard.go`、`cmd/railse/setup_codex.go`（按需）、对应测试；`docs/setup.md`（与 README 里提到这一步的地方）。

**现状：** `runSetupAllAs` 第 2 步调 `runSetup(["hooks", …])` 装三个 CLI 的钩子；`setup hooks` 装 Codex 时末尾打印“notify 请运行 railse setup codex-notify …”，要用户自己再跑一次 `runSetupCodexNotify`。问答式 `railse setup`（不带子命令、终端里）问完缺的值后走 `runSetupAll`。已接入过（有 `machine.json`）时第 1 步跳过，第 2、3 步照样重装钩子、重装托管并重启守护进程，不问。

**要求：**
1. `setup all`（含问答式 `railse setup`）第 2 步装完钩子后，自动做 `setup codex-notify` 的事：同一个 `--dir`（`cfg.Dir`），用户给了 `--bin` 才带 `--bin`（与钩子同一口径）。已有的 notify 由 codex-notify 现有逻辑保留为前一跳，不重复串接。`--no-hooks` 时不做。
2. 自动做了 notify 时，setup all 的输出里不再出现“请运行 railse setup codex-notify”的提醒（Codex 的其余提醒照旧）；单独跑 `railse setup hooks` 的输出不变。不要靠截输出实现，改成调用方能选的参数或拆出内部函数。
3. codex-notify 失败（如 `config.toml` 看不懂）不让 setup all 失败：打印原因，在最后的“注意”里加一条，带上修好后要跑的完整命令（含 `--dir`、`--bin`）。
4. 已接入过的机器（不加 `--force`、`machine.json` 已在）在终端里重跑 `railse setup` 或 `railse setup all` 时，在重装钩子与重启守护进程之前问一次，例如：
   `本机已接入 <hub 地址>。接下来重装钩子、重启守护进程，继续吗？[Y/n] `
   回车或 y/yes 继续；n/no 打印“已取消，钩子与守护进程都没动”退出 1；EOF 同取消；其他输入重问。`--no-hooks` 与 `--no-autostart` 都给了时不问（没有要重做的）。只重做其中一项时问句只提那一项。
5. stdin 不是终端（脚本、管道、`< /dev/null`）时不问，直接做。首次接入、`--force` 重新接入时不问。
6. 问答用 `setupStdin`、`stdinIsTerminal`（已有的可替换变量），写在 `setup_wizard.go` 里与 `askSetupAll` 放一起。
7. 更新 `setupUsage`/`setupAllUsage` 的说明与 `docs/setup.md`，README 里如有相关描述一并改。

**测试（TDD，先写失败的测试）：** 临时 HOME（`cmd/railse` 已有 TestMain 设临时 HOME）；setup all 之后 `~/.codex/config.toml` 的 notify 指向生成的脚本，原有 notify 记为前一跳；重跑不重复串接；`--no-hooks` 不碰 `config.toml`；`config.toml` 坏了时 setup all 仍退出 0 且“注意”里有修复命令；setup all 输出里没有“请运行 railse setup codex-notify”；已接入 + 终端：回答 y 继续、n 退出 1 且钩子文件与托管未变、EOF 退出 1、乱答重问；已接入 + 非终端：不问；首次接入、`--force`：不问。开机自启部分沿用现有测试替身，不能真的去装 launchd/systemd。

**验证：** `go test ./cmd/railse -race -count=1`、`make test`、`make e2e` 全过。

**提交：** `setup: setup all 装完钩子自动接 Codex notify；已接入的机器重跑前先确认`

---

### Task 8: B4 系统目录里的 git 仓库不当项目根

**文件：** `internal/agent/project.go`、`internal/agent/project_test.go`、`internal/agent/server.go`（日志）、`internal/doctor/agent.go`（体检说明）与测试、`docs/agent-local-loop.md`/`docs/setup.md` 里讲项目根判定的地方。

**现状：** `ResolveProjectRoot` 找到 git 仓库根（不太宽）就返回 `RootGit`，不论在哪里，所以 `/opt/homebrew` 这类在 PATH 上、归用户可写的 git 仓库会被当项目根，项目内可写的放行会让 agent 改写它。非 git 回退在同一函数里由 `plainUnderHome` 判断（v0.1.1）。

**要求：**
1. 系统目录：`/opt`、`/usr`、`/etc`、`/var`、`/System`、`/Library`、`/private` 及其下面，但 `/private/tmp` 及其下面除外。按路径分段比较（`/optional` 不算 `/opt`），比较的是 `CleanCwd` 解析过链接的路径（macOS 上 `/var`、`/etc`、`/tmp` 解析成 `/private/...`）；darwin 上不分大小写。
2. 控制者裁定：本进程的临时目录（`os.TempDir()`，解析链接后）及其下面也除外，前提是它不是 `/` 也不是上面列出的某个系统目录本身。理由：macOS 的 `$TMPDIR` 是 `/var/folders/…/T`（解析后在 `/private/var` 下），是每个用户私有的临时目录，与 `/tmp` 同类；不除外的话测试（`t.TempDir()`、`os.MkdirTemp("")`）与 `make e2e`（临时目录在 `$TMPDIR` 下）里的 git 仓库都会失去项目根。
3. 与非 git 回退放到同一处判断：一个函数决定一个候选目录（git 仓库根，或不在 git 仓库里时的 cwd）能不能当项目根、是哪一类。顺序：太宽（`tooBroad`）→ `RootNone`；在系统目录里 → 新类别 `RootSystem`（空项目根）；是 git 仓库根 → `RootGit`；家目录下的普通目录 → `RootCwd`；否则 `RootNotPlain`。git 仓库根在系统目录里时直接返回 `("", RootSystem)`，不再回退到 cwd。不在 git 仓库里的 `/etc`、`/usr/local`、`/opt` 这类目录由 `RootNotPlain` 改为 `RootSystem`；`/tmp`、`/private/tmp` 下的非 git 目录仍是 `RootNotPlain`。
4. `RootKind.String()` 加 `"system"`；`server.go` 的 `noteRoot` 对 `RootSystem` 记一行（如“目录 %q 在系统目录里，不按项目根处理”，同一目录只记一次，沿用现有集合与上限）；`doctor` 的 `projectRootCheck` 对 `RootSystem` 给出说明（显式给的项目 warn，隐式的 skip，与 `RootNotPlain` 同一口径），文案说清是系统目录。
5. 更新 `ResolveProjectRoot`、`RootKind` 的注释与 `policy/payload.go` 里描述项目根的注释，以及 docs 里讲项目根判定的段落。

**测试（TDD）：**
- 系统目录判定本身：用可替换的系统目录列表或临时目录造出“系统目录里的 git 仓库”（测试时把临时目录除外关掉或指到别处，例如 `t.Setenv("TMPDIR", …)`），断言 `("", RootSystem)`；`/private/tmp` 下的 git 仓库仍是 `RootGit`（`tmpUnder(t, "/private/tmp")`，不在时跳过）；`/tmp` 下同理；临时目录除外生效（`$TMPDIR` 在 `/private/var` 下时，那里的 git 仓库是 `RootGit`）；分段比较（造一个名叫 `opt-x` 之类的兄弟目录不受影响，可用替换列表测）；darwin 上大小写不同的写法也算。
- 真实路径只 stat 不写：`/etc`、`/usr/local`（存在时）→ `RootSystem`；`/opt/homebrew` 存在且含 `.git` 时 → `RootSystem`（不存在就跳过）。
- 现有 `TestResolveProjectRootNotPlain` 里 `/etc`、`/usr/local`、`/opt` 的期望改成 `RootSystem`。
- doctor 与 server 日志各加一个用例。

**验证：** `go test ./internal/agent ./internal/doctor -race -count=1`、`make test`、`make e2e` 全过。

**提交：** `agent: 系统目录（/opt、/usr、/etc、/var、/System、/Library、/private，/private/tmp 与本进程临时目录除外）里的 git 仓库不当项目根`

---

## 完成标准

1. 8 个 Task 各自审查 Approved，提交都在本分支上。
2. `make test`、`make e2e`、`make release-check` 在 HEAD 上全过；`go.mod`、`go.sum` 未变；`cd web && npm audit` 0 个漏洞。
3. 整条分支（main..HEAD）终审通过。
4. 汇报：分支名、提交数、测试结果、演示 gif 的大小与时长、遗留项。
