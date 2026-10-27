# Railse OPC v0.1.1：降低上手门槛（四项）

背景：作者把 hub 部署到 Fly（https://railse-hub.fly.dev）并把 Mac 接入后，发现小白用户卡在四处：每个项目都要装一次钩子、`setup all` 参数太多、Codex 只允许一个 notify 命令、非 git 目录里所有改动都算项目外。本计划逐项解决，四项依次做，每项先写失败测试再实现，`make test` 全过后单独提交，并经对抗性审查修完再做下一项。

## Global Constraints

- Go 不新增 require：`go.mod`、`go.sum` 不变（也不把 indirect 依赖改成直接依赖）。判断 stdin 是不是终端用 `os.Stdin.Stat()` 的 `os.ModeCharDevice`，不引 `golang.org/x/term`；不做二维码。
- 私密文件 0600、目录 0700、原子写（同目录临时文件 + rename，用 `cmd/railse` 现有的 `writeFileAtomic` 或同等实现）。备份文件一律 0600。别人的配置文件（CLI 的 settings.json、config.toml）沿用现有权限与符号链接处理（`resolveForWrite`：写到链接指向的文件、沿用原权限）。
- 中文文案、中文注释、中文提交信息；代码贴近周围写法（注释密度、命名、错误处理风格）。
- 测试一律用临时 HOME（`t.Setenv("HOME", 临时目录)`，临时目录用 `os.MkdirTemp("", "rs")`，macOS 上 unix socket 路径有长度限制），任何测试都不得读写真实的 `~/.claude`、`~/.gemini`、`~/.codex`、`~/.railse`。
- 每项先写失败测试（看到它因预期原因失败）再实现；`make test`（gofmt、golangci-lint、vet、`go test ./... -race`、网页检查）全过后提交；每项至少一个中文提交。不 push、不合并、不打 tag。
- 用户可见的行为改动同步到 `docs/setup.md`、`docs/agent-local-loop.md`、`README.md` 里对应的段落（只改相关段落）。

## Tasks

### Task 1: 钩子默认装用户级

**目标**：`railse setup hooks` 与 `railse setup all` 不给 `--project` 时，Claude 钩子写 `~/.claude/settings.json`、Gemini 钩子写 `~/.gemini/settings.json`（用户级，对所有项目生效）；Codex 不变（本来就是用户级 `~/.codex/hooks.json`）。`--project 目录` 保留为可选：给了才写项目级 `.claude/settings.local.json` 与 `.gemini/settings.json`（行为与现在完全相同，含旧 `settings.json` 的迁移）。

**文件**：`internal/hooksconf/hooksconf.go`、`cmd/railse/setup.go`、`internal/doctor/hooks.go`、对应测试、`docs/setup.md`、`docs/agent-local-loop.md`、`README.md`。

**要求**：

1. `internal/hooksconf` 新增 `ClaudeUserPath(home)`（`<home>/.claude/settings.json`）与 `GeminiUserPath(home)`（`<home>/.gemini/settings.json`）。写入仍用既有的 `Plan`/`Merge`（只改目标路径）：Merge 已保证只动 `hooks` 键里 Railse 的条目，别的设置原样保留。
2. `railse setup hooks`：`--project` 默认空。空时 Claude/Gemini 写用户级，不做 `claudeMigration`（它只用于项目级）；取不到家目录又没给 `--project` 时退出 1，提示用 `--project` 装项目级。给了 `--project` 时一切照旧。输出里用户级写入说明“用户级，对所有项目生效”。
3. 备份：用户级的 `~/.claude/settings.json` 与 `~/.gemini/settings.json`，在**第一次改它**之前做一次备份 `<文件>.railse-bak-<时间>`（时间格式 `20061027-150405`，本地时间）：
   - 只在文件已存在、非空且这次要写的内容与现有内容不同的时候备份；同目录里已有任何 `<文件名>.railse-bak-*` 就不再备份（“只备份一次”）。
   - 备份内容取解开符号链接后的真实文件；备份文件放在**显示路径**（`~/.claude/` 下）旁边，不放进链接指向的 dotfiles 目录。0600，用 O_EXCL 新建，不覆盖已有文件。
   - 备份在写入阶段、写这个文件之前做；备份失败则不写这个文件并退出 1。`--dry-run` 不备份，可以提示“会先备份到 …”。
   - 打印“已备份 <原文件> 到 <备份>”。项目级文件不备份（与现在一致）。
4. `railse setup all`：`--project` 改为可选，默认空 → 钩子装用户级；给了才校验目录存在并传给 `setup hooks --project`。第 4 步体检：给了 `--project` 才传 `--project` 给 doctor（不给时 doctor 用它自己的默认）；体检失败时提示的复查命令同理只在给了时带 `--project`。
5. 用法文本（`setupUsage`、`setupAllUsage`）与 `setup hooks` 的说明改成：默认 Claude、Gemini 写用户级，`--project` 写项目级。
6. `railse doctor` 的 `hooks.claude` 与 `hooks.gemini`：用户级（`Options.Home`）或项目级（`Options.Project`）任一装了就算装了。
   - 只有用户级装了：按用户级文件检查（`hookFileCheck` 的全部规则：事件齐全、超时下限、程序路径、socket），修复提示 `railse setup hooks --claude`（或 `--gemini`）加 `dirFlag`。
   - 只有项目级装了：沿用现在的项目级逻辑（Claude 含旧 `settings.json` 的迁移提示），修复提示带 `--project`。
   - 两处都装了：按用户级检查，状态至少 warn，细节加“项目级 <路径> 也装了，钩子会触发两次”，提示删掉项目级文件里 Railse 的钩子条目（用户级已对所有项目生效）。
   - 都没装：fail，细节列出看过的用户级与项目级路径，提示 `railse setup hooks --claude`（`--gemini`）。
   - 同一个文件不算两处：项目就是家目录时 Gemini 的项目级与用户级是同一个文件；Claude 项目级旧位置 `<项目>/.claude/settings.json` 可能就是用户级文件（同目录或链接）。按文件身份去重（`os.SameFile`）。
   - `Options.Project` 为空：只看用户级；`Options.Home` 为空：只看项目级；两者都空：skip。`hookChecks` 不再因为没有 `--project` 就把四项全部 skip（Codex 两项照旧只看 Home）。
   - 用户级文件是坏 JSON：fail，提示修好再装（与 `hookFileCheck` 现有写法一致）。
7. `cmd/railse` 加 `TestMain` 兜底：进程开始就把 `HOME` 设成新建的临时目录（结束时删掉），任何测试漏设 HOME 也写不到真实家目录。现有测试照旧通过。
8. 文档：`docs/setup.md`（setup all 第 2 步说明、`--project` 说明、“各 CLI 的钩子”一节、卸载段落列出用户级文件与备份、doctor 表格 `hooks.claude`/`hooks.gemini` 两行）、`docs/agent-local-loop.md`（setup hooks 一节与表格“钩子配置”一行）、`README.md`（第 53 行附近“Claude、Gemini 的钩子是项目级的”）。

**测试（先写，先看它们失败）**：
- 临时 HOME 下 `setup hooks`（不带 `--project`）写 `~/.claude/settings.json` 与 `~/.gemini/settings.json`，命令与 `Plan` 一致；已有的用户设置（别的键、别人的钩子）原样保留。
- 备份：已有非空用户级文件 → 生成一个备份，内容等于原文件，0600；再跑一次（内容不变）不备份；改了 `--bin` 再跑（内容变了）也不再备份（已有备份）；新建文件（原来不存在）不备份；`--dry-run` 不备份不写；符号链接的情况备份内容取目标、放在链接旁边。
- 带 `--project` 时仍写项目级，用户级文件不碰。
- `setup all` 不带 `--project` 装用户级；带 `--project` 装项目级。
- doctor：只用户级 ok；只项目级 ok；两处都装 warn 且细节含“触发两次”；都没装 fail；项目即家目录时 Gemini 不算两处；Project 为空只看用户级。

### Task 2: `railse setup` 问答式向导

**目标**：不带子命令运行 `railse setup`，或 `railse setup all` 需要接入却缺 `--hub`/`--token` 时，若 stdin 是终端，就逐个提问后执行 setup all 的流程；stdin 不是终端时保持现在的用法错误、退出 2。装完把 hub 地址用醒目的框线围起来打印。

**文件**：`cmd/railse/setup.go`（或新文件 `cmd/railse/setup_wizard.go`）、`cmd/railse/main.go`（如需）、测试、`docs/setup.md`、`README.md`。

**要求**：

1. 包级变量便于测试：`setupStdin io.Reader = os.Stdin`、`stdinIsTerminal = func() bool`（`os.Stdin.Stat()` 且 `Mode()&os.ModeCharDevice != 0`）。只用一个 `bufio.Reader` 读整个问答。
2. 触发条件：
   - `railse setup`（没有参数）：终端 → 等同 `railse setup all` 不带任何标志、走向导；非终端 → 打印 `setupUsage`，退出 2（同现在）。
   - `railse setup all`：需要接入（没有 machine.json，或 `--force`）且缺 `--hub` 或 `--token`：终端 → 只问缺的那几项（`--name` 没给时也问名字）；非终端 → 现在的用法错误，退出 2。
   - 已接入（有 machine.json 且没 `--force`）：不问 hub、接入码、名字，直接走 setup all（它会跳过接入）。
3. 提问的时机：在拒绝 root 与所有“一看就不对的输入”检查（`--bin` 可执行、`--project` 存在等）之后、第 1 步之前，免得用户答完才被拒。
4. 三个问题（提示写到 stdout）：
   - hub 地址，如 `https://hub.example.com`：用与 `--hub` 相同的校验（`parseHubURL`，以及 `agentEnroll` 对非本机 `http://` 的同一判定，抽成共用函数，不复制逻辑）；不合格打印原因并重问。
   - 接入码（提示“在 hub 机器上运行 railse hub token 生成”）：去掉首尾空白后不能为空，空则重问。输入回显（不引依赖，不做隐藏输入）。
   - 本机名字，默认主机名（提示里显示默认值，直接回车取默认）：用与 `--name` 相同的校验，不合格重问。
   - 任一问题读到 EOF（Ctrl-D）：stderr 打印“已取消，什么都没做”，退出 1，不写任何文件。
5. 答完后用这些值执行与 `setup all` 完全相同的流程（同一段代码，不复制步骤逻辑）。
6. setup all 结束时（不论是不是向导）把手机要打开的 hub 地址用框线围起来打印，替换现在的“下一步：手机上打开 … ”一行，例如：

   ```
   下一步：在手机上打开
   ╔══════════════════════════════╗
   ║  https://railse-hub.fly.dev  ║
   ╚══════════════════════════════╝
   手机 Safari 打开后分享→添加到主屏幕，再从图标打开；登录后在设置里开启通知（见 docs/web.md）。
   ```

   框宽按地址的字符数算，框里只放地址。地址为空时不画框。
7. 用法文本说明 `railse setup`（不带子命令，在终端里问答）。文档 `docs/setup.md` 的 setup all 一节与 `README.md` 的上手步骤写上向导。

**测试（先写）**：
- 终端 + 假 stdin：依次答 hub、接入码、名字（直接回车取主机名）→ 接入测试 hub 成功、machine.json 名字是主机名、钩子与托管照常；输出含框线与地址、含 Safari 提示。
- 不合格 hub 地址（`ftp://x`、带用户名、非本机 `http://`）会重问；空接入码重问。
- 中途 EOF → 退出 1、“已取消”、没有 machine.json、没写钩子。
- 非终端：`railse setup` 退出 2 打印用法；`setup all` 缺 `--token` 退出 2。
- `setup all --hub X` 在终端里缺接入码：只问接入码（和名字），不问 hub。
- 已接入：`railse setup` 不提问，直接跑完。
- root：先拒绝，不提问（读 stdin 0 字节）。

### Task 3: Codex notify 串接

**目标**：新增 `railse setup codex-notify [--dir 目录] [--bin 路径] [--dry-run]`：生成 `<数据目录>/codex-notify.sh`（数据目录默认 `$RAILSE_HOME` 或 `~/.railse`），脚本依次调用 `~/.codex/config.toml` 里已有的 notify 命令（已有且不是 railse 的，就作为前一跳保留）与 `railse hook codex-notify`；然后把 config.toml 顶层的 `notify = [...]` 改成 `["<数据目录>/codex-notify.sh"]`。幂等。

**文件**：新 `internal/hooksconf/codexnotify.go`（TOML notify 的定位、解析、改写，doctor 共用）及测试、新 `cmd/railse/setup_codex.go`（或并入 setup.go）及测试、`cmd/railse/setup.go`（分派、用法、`setup hooks` 的 Codex 提醒）、`internal/doctor/hooks.go`、`docs/setup.md`、`docs/agent-local-loop.md`。

**要求**：

1. 解析 `~/.codex/config.toml`（不存在当作空文件）。只认第一个表头（`[x]` 或 `[[x]]`）之前的顶层 `notify` 裸键。值是 TOML 数组，元素是基本字符串（`"..."`，按 TOML 转义）或字面字符串（`'...'`），可跨行，可有逗号结尾、`#` 注释。看不懂的写法（多行字符串 `"""`/`'''`、非字符串元素、数组没闭合等）：退出 1，说明请手动改，什么都不写。
2. 判定前一跳（prev）：
   - 没有顶层 notify：没有前一跳。
   - notify 就是我们的脚本（第一个元素与 `<数据目录>/codex-notify.sh` 是同一路径，或文件名是 `codex-notify.sh` 且文件里有我们的标记行）：前一跳取那个脚本标记行里记的值（读不到脚本时没有前一跳并警告）——不把自己当前一跳。
   - notify 是直接调 railse 的旧写法（形如 `[".../railse", "hook", "codex-notify", ...]`，程序文件名是 `railse`）：没有前一跳（替换掉，不串两个 railse）。
   - 其他：整个数组作为前一跳原样保留。
3. 脚本内容固定、可重复（同样的输入生成同样的字节），`#!/bin/sh`，大致：

   ```sh
   #!/bin/sh
   # 由 railse setup codex-notify 生成，重跑会重新生成，不要手动改。
   # Codex 把通知 JSON 作为最后一个参数传给 notify 命令；这里原样（"$@"）交给下面每个命令，一个失败不影响另一个。
   # railse-prev-notify: ["/usr/local/bin/notify-wrap","--quiet"]
   '/usr/local/bin/notify-wrap' '--quiet' "$@"
   '/Users/me/.local/bin/railse' hook codex-notify "$@"
   exit 0
   ```

   - 标记行 `# railse-prev-notify: <JSON 数组>` 记前一跳（没有时是 `[]`，也不写前一跳那一行），JSON 单行、不转义 HTML 字符。
   - 每个参数经 `hooksconf.ShellQuote`；不用 `set -e`，前一跳失败不影响 railse 那一行；最后 `exit 0`。
   - railse 那一行：数据目录不是 `~/.railse` 时带 `--socket <socket>`（与 `setup hooks` 的 `hookSocket` 同一判定）；程序路径默认当前程序（`--bin` 可改）。
   - 脚本 0755，原子写；数据目录不存在时按 0700 建。
4. 改写 config.toml：
   - 把顶层 notify 的整段（从键到 `]`）换成 `notify = ["<脚本绝对路径>"]`（TOML 基本字符串，正确转义 `\`、`"` 与控制字符）；没有顶层 notify 时在文件最前面插一行。只在某个表下面有 notify（Codex 读不到）时：照样在最前面插顶层的，并警告那个表下面的 notify 被忽略，请手动删。
   - 其余内容逐字节不变。
   - 内容没变：不写、不备份，打印“已是最新”。
   - 要改且文件已存在：先备份 `config.toml.railse-bak-<时间>`（时间格式同 Task 1，0600，O_EXCL，放在显示路径旁边，内容取解开链接后的文件），再原子写（沿用权限与符号链接处理）；文件原来不存在：新建 0600，不备份。
   - 先写脚本再改 config.toml（config 永远不会指向不存在的脚本）。
   - `--dry-run`：打印脚本内容与改写后的 config.toml，什么都不写。
5. 幂等：再跑一次脚本字节相同、config.toml 不变、不新增备份、不重复串接、不把自己当前一跳。换了 `--bin` 或 `--dir` 再跑：脚本更新，前一跳保留。
6. `setup hooks` 打印的 Codex 提醒里“notify 需手动写在 ~/.codex/config.toml”改成提示运行 `railse setup codex-notify`（会保留已有的 notify）。用法文本加上 `setup codex-notify`。`setup all` 不自动调用它。
7. doctor `hooks.codex_notify`：顶层 notify 是脚本写法（单元素数组，文件名 `codex-notify.sh`）时，脚本存在、可执行、里面有 railse 的 `hook codex-notify` 行 → ok；脚本里 railse 那一行的 socket（带不带 `--socket`）与 `--dir` 守护进程对不上 → warn（与旧写法的 socket 比对同一规则）；脚本不存在或不可执行 → warn，提示 `railse setup codex-notify`。没有 notify、notify 在表下面等现有的 warn 提示改成运行 `railse setup codex-notify`（加 `dirFlag`）。旧写法 `[..., "hook", "codex-notify"]` 照旧 ok。现有 doctor 测试照旧通过（按新提示更新期望文字）。
8. 文档：`docs/setup.md` 的 Codex 一条、doctor 表格 `hooks.codex_notify` 一行、卸载段落（删脚本，notify 从备份恢复或删掉）；`docs/agent-local-loop.md` 提到 notify 的地方。

**测试（先写；临时 HOME）**：
- 没有 config.toml：新建 0600，只有 notify 一行；脚本 0755，标记行 `[]`，没有前一跳行。
- 已有别人的 notify（跨行、带注释与逗号结尾、单双引号混用），文件里还有别的顶层键与表：脚本前一跳参数逐个正确；config.toml 只有 notify 那段变了；备份逐字节等于原文件、0600。
- 再跑一次：脚本字节相同、config 不变、备份数不变。换 `--bin` 再跑：前一跳仍在。
- 旧写法 railse notify：没有前一跳。
- 非默认 `--dir`：railse 行带 `--socket`，config 指向 `<dir>/codex-notify.sh`。
- `--dry-run` 不写任何文件。
- notify 只在表下面：插入顶层的并警告。看不懂的写法：退出 1，文件不变。
- 真跑脚本（需要 /bin/sh）：假前一跳与假 railse 各自把收到的参数写到文件，两者都收到同一个 JSON 参数；前一跳 `exit 1` 时 railse 仍被调用。
- doctor：脚本写法 ok；脚本不存在 warn；不可执行 warn；socket 对不上 warn；旧写法仍 ok。

### Task 4: 非 git 目录的项目根

**目标**：`internal/agent` 找不到合适的 `.git` 时把 cwd 自身当项目根（现在返回空，导致所有改动都算项目外），并在守护进程日志（每个目录只记一次）和 doctor 里提示“目录 X 不是 git 仓库，按当前目录当项目根；建议 git init”。cwd 是家目录、家目录的祖先或根目录时仍返回空。确认 `internal/policy` 的规则范围匹配与学习不会把家目录当项目根。

**文件**：`internal/agent/project.go`、`internal/agent/server.go`、`internal/agent/project_test.go`、server 测试、`internal/doctor/`（新检查）、`internal/policy`（家目录守卫）及测试、`internal/hub/rules.go`（与守卫对齐）、相关文档。

**要求**：

1. `internal/agent` 新增 `ResolveProjectRoot(cwd string) (root string, git bool)`：
   - 从清理后的 cwd 向上找 `.git`（目录或 worktree 的 `.git` 文件），找到且不 `tooBroad` → `(那个目录, true)`。
   - 找不到，或找到的仓库根 `tooBroad`（用 git 管 dotfiles 的人家目录里有 `.git`）→ cwd 自身是存在的目录且不 `tooBroad` 时 `(cwd, false)`，否则 `("", false)`。
   - `FindProjectRoot(cwd) string` 保留，返回 `ResolveProjectRoot` 的 root。注释同步改。
2. 守护进程 `evaluate`：用 `ResolveProjectRoot`；`git == false && root != ""` 时 `s.log.Printf("目录 %s 不是 git 仓库，按当前目录当项目根；建议 git init", root)`，同一目录只记一次（`Server` 上加互斥锁保护的集合，最多记 1024 个目录，满了不再记，不影响判定）。并发安全（`-race`）。
3. doctor 新检查 `project.root`（名字“项目根”），放在钩子检查之后：`Options.Project` 为空 skip；git 仓库 → ok（细节写项目根）；非 git 的回退 → warn，细节“目录 X 不是 git 仓库，按当前目录当项目根”，提示“建议在 X 里 git init（项目根决定哪些写入算项目内）”；返回空（家目录、它的上级、根目录、不存在）→ skip，细节说明这里不当项目根、在这里跑 agent 时所有写入都算项目外。用 `agent.ResolveProjectRoot`（doctor 已依赖 internal/agent）。
4. `internal/policy` 守卫：`Context` 的项目根是家目录、家目录的祖先或根目录时一律按空处理（只比较清理后的路径字符串，本包不读环境）。在所有用到 `ctx.ProjectRoot` 的入口统一处理（`Evaluate`、`Classify`、`ProposeRule` 等导出入口先规范化 Context），导出一个小函数（如 `(Context).Root()` 或 `EffectiveProjectRoot`）供 hub 用。`HomeDir` 为空时只挡根目录。
5. `internal/hub/rules.go` 里直接用 `r.ProjectRoot` 判断范围的地方（“没有项目根，只能记成全局”、`denyRule` 的 `Scope.Project`、file_write 的 cwd 回退）改用同一个规范化后的根，免得 hub 记下一条范围是家目录、守卫后永远匹配不上的 deny 规则。
6. 文档：`docs/agent-local-loop.md`（项目根怎么定）、设计文档里描述项目根的那句（若有）、`docs/setup.md` 的 doctor 表格加 `project.root` 一行。

**测试（先写）**：
- `ResolveProjectRoot`：git 仓库子目录 → 仓库根、true；非 git 目录 → 它自己、false；非 git 的深层子目录 → 子目录自己（不是上级）；家目录 → 空；家目录的祖先 → 空；`/` → 空；不存在的路径 → 空；家目录有 `.git` 而 cwd 是家目录下的非 git 子目录 → 子目录、false。测试都用临时 HOME。
- 守护进程：非 git cwd 的项目内写入按 write_in_project 处理（不是 write_outside）；同一 cwd 两次请求日志只一行，换一个 cwd 再一行；git 仓库里不记日志。
- doctor `project.root`：git ok、非 git warn（细节含“不是 git 仓库”）、家目录 skip、Project 空 skip。
- policy：ProjectRoot = HomeDir 时家目录下的写入是 write_outside、范围是家目录的规则不匹配、`ProposeRule` 对 shell 与 file_write 报“没有项目根”；ProjectRoot 是家目录的祖先、`/` 同理；正常项目根不受影响。
- hub：请求的 ProjectRoot 是机器家目录时“拒绝并记住”记成全局规则、按项目范围记住报“没有项目根”。

### Task 5: 收紧非 git 回退（终审 I3 拍板）

**背景**：Task 4 让非 git 的 cwd 都当项目根，终审指出 `~/Library/LaunchAgents`、`~/.config`、`/usr/local`、`/etc` 这类目录也会因此自动放行写入。发起会话 2026-10-27 拍板：收紧。

**目标**：非 git 目录按当前目录当项目根，只在同时满足下面条件时生效；不满足时保持原行为（没有项目根，改动都走 ask），并在守护进程日志（每个目录只记一次）与 doctor 的 `project.root` 里说明“目录 X 不在家目录下的普通目录里，不按项目根处理；要自动放行请 git init 或换到项目目录”。

1. 路径在家目录之下（不是家目录本身，也不是它的祖先）。家目录之外一律不回退：`/usr`、`/etc`、`/opt`、`/var`、`/System`、`/Library`、`/tmp`、`/private/tmp` 等都算。
2. 从家目录往下的每一段都不以点开头（排除 `~/.config`、`~/.local`、`~/.cache`、`~/dev/.hidden/x` 等）。
3. 不在 `~/Library` 之下（macOS 文件系统默认不分大小写，`~/library/x` 也算）。

**文件**：`internal/agent/project.go`、`internal/agent/server.go`、`internal/doctor/agent.go`、对应测试；`internal/policy/payload.go`（`Context.ProjectRoot` 注释）、`docs/agent-local-loop.md`（项目根一节）、`docs/setup.md`（doctor 表 `project.root` 一行）、设计文档里描述项目根的那句。

**要求**：

1. `ResolveProjectRoot` 区分四种结果（返回值改成带类别的形式，导出类别供 doctor 用）：git 仓库根；回退到 cwd；cwd 存在、不是 git、不太宽、但不满足上面三条（“不是普通目录”）；空（家目录、它的祖先、根目录、不存在、相对路径）。git 仓库根的判定不变（`/tmp`、`/opt` 下的 git 仓库照常当项目根，冒烟与测试不受影响）；用 git 管 dotfiles（家目录有 `.git`）时的回退也按这三条判断。
2. “在家目录之下”按文件身份判断：从解析链接后的 cwd 逐级往上，直到某一级与家目录（`homeSpellings` 的任一写法）是同一个文件；到根都没碰到就是在家目录之外。经过的各段名字用来判断点开头；紧挨家目录的那一段与 `<家目录>/Library` 是同一个文件，或名字等于 `Library`（darwin 上不分大小写），就算在 `~/Library` 之下。指向家目录之外或点目录里的符号链接，解析后按目标判断。
3. 守护进程：“不是普通目录”时记 `目录 %q 不在家目录下的普通目录里，不按项目根处理；要自动放行请 git init 或换到项目目录`，与“不是 git 仓库”共用每目录只记一次的集合；项目根为空。
4. doctor `project.root`：“不是普通目录”时，显式 `--project` → warn，细节写上面那句的前半，提示“要自动放行请 git init 或换到项目目录”；隐式默认（当前目录）→ skip，细节同样说明，不给提示。其余情况不变。
5. policy 与 hub：项目根是否回退只有守护进程知道（它们分不出 git 根与回退根），继续共用 `Context.Root()` 的家目录守卫，不额外改判断；只同步注释与文档的说法。
6. 测试（临时 HOME）：`~/dev/foo` 回退；`~/Library/LaunchAgents`、`~/library/x`（darwin、不分大小写的卷上）、`~/.config/x`、`~/dev/.hidden/x`、`/usr/local`、`/etc`（只 stat）、`/tmp` 下新建的目录、darwin 上 `/private/tmp` 下的目录都不回退；`~/dev/link` 指向 `/etc` 或 `~/.config/x` 不回退；家目录有 `.git` 时 `~/dev/foo` 回退、`~/.config/nvim` 不回退；`~/.config/nvim` 自己是 git 仓库时照常是项目根。守护进程日志与 doctor（显式/隐式）各一组。Task 4 里用家目录之外的临时目录当非 git cwd 的测试改到临时 HOME 之下。

**追加（用户 2026-10-27 在对话里直接要求）**：

7. 敏感路径表（`internal/policy/paths.go` 的 `sensitivePatterns`）加：`~/Library/LaunchAgents/**`、`/Library/LaunchAgents/**`、`/Library/LaunchDaemons/**`、`.config/systemd`、`.config/autostart` 下的一切、`.codex/hooks.json`。写法沿用表里的惯例：家目录规则同时给 `**/` 版本兜住 firmlink 与别人的家目录（例如 `**/Library/LaunchAgents/**`、`**/Library/LaunchDaemons/**`、`**/.config/systemd/**`、`**/.config/autostart/**`、`**/.codex/hooks.json`）。命中即 destructive（读也算，这是表的既有语义）。测试：在这些位置写文件、`cat` 读文件都是 destructive；相邻的普通路径（`~/Library/Caches/x`、`~/.config/nvim/init.lua`、`~/.codex/notes.md`）不受影响。railse 自己写 `~/.codex/hooks.json` 不经过策略引擎，不受影响。
8. 用户还列了“非 git 回退时排除 ~/Library、家目录下的点目录和 /etc、/usr、/opt、/Library、/Applications、/System、/private 这些系统前缀”：上面 1–3 条（家目录之外一律不回退）已全部覆盖且更严，按 1–3 条做；测试里加上 `/Applications/x`（只 stat 一个现成目录，如 `/Applications`下任一子目录，没有就跳过）与 `/opt` 下的目录（有才测）。
