package i18n

// 英文表：键是代码里的中文原文（含占位符与标点），值是英文。语气与 README.en 一致：短句、说人话、不吹。
//
// 术语表（网页的 web/src/locales/en.ts 同此）：
//
//	批准 approve · 拒绝 deny · 记住 remember · 收件箱 inbox · hub hub · 守护进程 daemon · 钩子 hook · 规则 rule
//	项目 project · 机器 machine · 接入码 enroll token · 体检 health check · 开机自启 autostart · 本机 hub local hub
//	交回终端处理 handed back to the terminal · 审批 approval · 选择题/提问 question · 暂停自动审批 auto-approval paused
//	等你回答 waiting for your answer
var en = map[string]string{
	// cmd/railse/main.go
	`用法: railse <子命令> [参数]

子命令:
  version   打印版本
  policy    策略引擎工具（eval、check）
  hook      各 CLI 钩子调用的翻译器（claude、codex、codex-notify、gemini）
  agent     守护进程（run、status、pause、resume、reload、enroll）
  hub       云端服务（run、password、token、webhook-token）
  setup     一键接入与安装（all）、钩子（hooks）、开机自启（autostart）、Codex notify（codex-notify）
  doctor    检查安装与运行状态（只读）
  lang      查看或设定界面语言（zh|en）`: `Usage: railse <command> [args]

Commands:
  version   print the version
  policy    rule engine tools (eval, check)
  hook      translators the CLI hooks call (claude, codex, codex-notify, gemini)
  agent     daemon (run, status, pause, resume, reload, enroll)
  hub       the hub server (run, password, token, webhook-token)
  setup     one-step enroll and install (all), hooks, autostart, Codex notify (codex-notify)
  doctor    check the install and what is running (read-only)
  lang      show or set the UI language (zh|en)`,
	"未知子命令 %q\n\n%s\n": "Unknown command %q\n\n%s\n",

	// cmd/railse/lang.go
	"当前语言 %s（来源：%s）\n":            "Current language: %s (from %s)\n",
	"语言只能是 zh 或 en，不是 %q\n\n%s\n": "Language must be zh or en, not %q\n\n%s\n",
	"找不到家目录，没法写 lang 文件":          "Can't find your home directory, so the lang file can't be written",
	"写不了 lang 文件：%v\n":            "Can't write the lang file: %v\n",
	"注意：环境变量 RAILSE_LANG=%s 优先于 lang 文件；要让 lang 文件生效，请去掉它。\n": "Note: the RAILSE_LANG=%s environment variable takes priority over the lang file. Remove it for the lang file to apply.\n",
	"已写入 %s\n":         "Wrote %s\n",
	"多余的参数 %q\n%s\n":   "Unexpected argument %q\n%s\n",
	"环境变量 RAILSE_LANG": "the RAILSE_LANG environment variable",
	"lang 文件":          "the lang file",
	"系统 LANG":          "the system LANG",
	"命令指定":             "this command",
	"默认":               "the default",
	`用法: railse lang [zh|en]
不带参数打印当前语言和它从哪来；带参数写进数据目录的 lang 文件（$RAILSE_HOME 或 ~/.railse）。
解析顺序：环境变量 RAILSE_LANG → lang 文件 → 系统 LANG（zh 开头算中文，其余英文）。`: `Usage: railse lang [zh|en]
Without an argument, prints the current language and where it came from. With one, writes it to the lang file in the data directory ($RAILSE_HOME or ~/.railse).
Resolution order: the RAILSE_LANG environment variable, then the lang file, then the system LANG (zh* means Chinese, anything else English).`,

	// cmd/railse/setup_wizard.go
	"接入 hub 还缺几项，逐个回答（Ctrl-D 取消，什么都不做）：":    "A few things are needed to connect to the hub. Answer them one at a time (Ctrl-D cancels and changes nothing):",
	"hub 地址（如 https://hub.example.com）：":    "Hub address (e.g. https://hub.example.com): ",
	"接入码（在 hub 机器上运行 railse hub token 生成）：": "Enroll token (run railse hub token on the hub machine to get one): ",
	"接入码不能为空":                 "The enroll token can't be empty",
	"本机在 hub 里的名字（直接回车用 %s）：": "Name for this machine on the hub (Enter uses %s): ",
	"名字%w": "Name: %w",
	"检查本机 hub（必要时重新托管）":         "check the local hub (reinstalling its service if needed)",
	"重装钩子（连同 Codex 的 notify）":   "reinstall hooks (and Codex notify)",
	"本机 hub、钩子与守护进程都没动":         "the local hub, hooks and daemon were not touched",
	"本机 hub 与钩子都没动":             "the local hub and hooks were not touched",
	"本机 hub 与守护进程都没动":           "the local hub and daemon were not touched",
	"本机 hub 没动":                 "the local hub was not touched",
	"钩子与守护进程都没动":                "hooks and daemon were not touched",
	"钩子没动":                      "hooks were not touched",
	"守护进程没动":                    "the daemon was not touched",
	"本机已接入 %s。接下来%s，继续吗？[Y/n] ": "This machine is enrolled with %s. Next: %s. Continue? [Y/n] ",
	"请回答 y 或 n（直接回车算 y）":        "Answer y or n (Enter means y)",
	"hub 在哪里？\n  1) 本机：用 Tailscale 给手机一个地址，不用服务器（要先装好并登录 Tailscale）\n  2) 已有 hub：填它的地址和接入码\n": "Where is your hub?\n  1) This machine: Tailscale gives your phone an address, no server needed (install Tailscale and log in first)\n  2) An existing hub: enter its address and an enroll token\n",
	"选 [%s]：": "Choose [%s]: ",
	"请答 1 或 2（直接回车选 %s）\n": "Answer 1 or 2 (Enter picks %s)\n",
	"hub 地址%w": "Hub address: %w",
	"%s；确在可信内网的，用 railse setup all --hub <地址> 给出":               "%s. If it really is on a trusted private network, pass it with railse setup all --hub <address>",
	"hub 地址是 http://，明文传输密钥，只适合本机或可信内网；公网请用 https://":           "The hub address is http://, which sends the key in plain text. That's only OK on this machine or a trusted private network; over the internet use https://",
	"\n下一步：在手机上打开 hub 的地址":                                      "\nNext: open this on your phone: the hub's address",
	"\n下一步：在手机上打开\n╔%s╗\n║  %s  ║\n╚%s╝\n":                      "\nNext: open this on your phone\n╔%s╗\n║  %s  ║\n╚%s╝\n",
	"这个地址由 Tailscale 提供；手机挂着代理打不开时关掉代理再试。":                      "This address is served by Tailscale; if your phone is on a proxy and it won't open, turn the proxy off and retry.",
	"手机 Safari 打开后分享→添加到主屏幕，再从图标打开；登录后在设置里开启通知（见 docs/web.md）。": "In Safari on the phone: Share → Add to Home Screen, then open it from the icon; after logging in, turn on notifications in Settings (see docs/web.md).",
	"已取消，什么都没做":        "Cancelled, nothing was done",
	"读输入失败，什么都没做：%v\n": "Couldn't read input, nothing was done: %v\n",

	// cmd/railse/setup.go：用法（setupUsageText 按这四段拼；setupCodexNotifyUsage 在 setup_codex.go 一段）
	`用法:
  railse setup
    不带子命令、在终端里运行：先问 hub 在哪里（本机，或已有 hub）。本机的照 setup all --local-hub 做完；已有 hub 的逐个问 hub 地址、
    接入码、本机名字，再照 setup all 做完。已接入过的不问这些，照 setup all 重跑（只确认一次再重装）。`: `Usage:
  railse setup
    With no subcommand, in a terminal: first asks where your hub is (this machine, or an existing hub). This machine runs setup all --local-hub;
    an existing hub asks for its address, an enroll token and this machine's name, then runs setup all. Already enrolled: no questions, it reruns setup all (asks once before reinstalling).`,
	`  railse setup all --hub URL --token 接入码 [--name 名字] [--project 目录] [--dir 目录] [--bin 路径] [--no-autostart] [--no-hooks] [--force]
  railse setup all --local-hub [--hub-port 8787] [--hub-dir 目录] [--public-url https://…] [--name 名字] [--project 目录] [--dir 目录] [--bin 路径] [--no-autostart] [--no-hooks] [--force]
    一键：接入 hub → 装三个 CLI 的钩子 → 开机自启 → 体检（railse doctor）。钩子默认装用户级（对所有项目生效），
    给了 --project 才把 Claude、Gemini 的装成那个项目的项目级（同 setup hooks）；装完钩子接着接 Codex 的 notify
    （同 setup codex-notify，没接上不算 setup all 失败，最后的“注意”里给命令）。--no-hooks 两样都不做。
    已接入过（有 machine.json）时跳过接入，这时可以不给 --hub 与 --token；--force 重新接入。体检的失败只展示，不算 setup all 失败。
    已接入过的在终端里重跑，重装钩子、重启守护进程之前先问一次（回车继续）；不在终端里不问。
    要接入却缺 --hub 或 --token 时，在终端里逐个问缺的（--name 没给时也问名字）；不在终端里是用法错误。
    --local-hub：一台机器就够：本机起 hub，Tailscale Funnel 给手机地址，守护进程接本机 hub；--public-url 换成自己的隧道。
    接入之前多一步“本机 hub”：没设过网页密码时在终端里问、托管 hub（只监听 127.0.0.1:<--hub-port>）并等它应答，
    最后开 Funnel（tailnet 没开通时打印开通链接），再用它生成的一次性接入码接入（不显示；不问 hub 地址、接入码与名字，名字默认主机名）。
    --hub-dir 默认 $RAILSE_HUB_HOME 或 ~/.railse-hub；--hub-port、--public-url 没给时沿用上次装的（hub 数据目录里的 local-hub.json），
    从自己的隧道换回 Tailscale 给空的 --public-url=。
    --no-autostart 只管守护进程，本机 hub 总是托管。本机已接入别的 hub 时停下，加 --force 改接本机 hub；
    接的就是本机 hub 时不带 --local-hub 重跑也按本机 hub 走（在终端里先问一次再复查 Funnel、托管）。`: `  railse setup all --hub URL --token TOKEN [--name NAME] [--project DIR] [--dir DIR] [--bin PATH] [--no-autostart] [--no-hooks] [--force]
  railse setup all --local-hub [--hub-port 8787] [--hub-dir DIR] [--public-url https://…] [--name NAME] [--project DIR] [--dir DIR] [--bin PATH] [--no-autostart] [--no-hooks] [--force]
    One step: connect to the hub → install hooks for the three CLIs → autostart → health check (railse doctor). Hooks go in at user level by default (all projects);
    with --project, the Claude and Gemini hooks go in at that project's level instead (same as setup hooks). After the hooks it wires up Codex notify
    (same as setup codex-notify; if that fails, setup all doesn't, and the final "Note" gives the command). --no-hooks skips both.
    Already enrolled (machine.json exists): enrolling is skipped and --hub and --token can be left out; --force enrolls again. Health check failures are shown but don't fail setup all.
    Rerunning in a terminal on an enrolled machine asks once (Enter continues) before reinstalling hooks and restarting the daemon; outside a terminal it doesn't ask.
    If enrolling needs --hub or --token and one is missing, a terminal asks for each (and for the name if --name is missing); outside a terminal it's a usage error.
    --local-hub: one machine is enough. The hub runs on this machine, Tailscale Funnel gives the phone an address, and the daemon connects to the local hub; --public-url uses your own tunnel instead.
    It adds a "local hub" step before enrolling: asks for a web password in the terminal if none is set, installs the hub as a service (listening only on 127.0.0.1:<--hub-port>) and waits for it to answer,
    then turns on Funnel (printing the enable link if your tailnet hasn't enabled it), and enrolls with a one-time enroll token it creates (not shown; no questions for hub address, token or name; the name defaults to the hostname).
    --hub-dir defaults to $RAILSE_HUB_HOME or ~/.railse-hub; --hub-port and --public-url default to the last install (local-hub.json in the hub data directory).
    To switch from your own tunnel back to Tailscale, pass an empty --public-url=.
    --no-autostart only covers the daemon; the local hub is always installed as a service. If this machine is enrolled with another hub, it stops; add --force to switch to the local hub.
    If this machine is enrolled with the local hub, rerunning without --local-hub still uses the local hub (in a terminal it asks once, then rechecks Funnel and the service).`,
	`  railse setup hooks [--project 目录] [--claude] [--codex] [--gemini] [--codex-file 路径] [--dir 目录] [--bin 路径] [--dry-run]
    默认三个都装，都是用户级（对所有项目生效）：Claude 写 ~/.claude/settings.json，Gemini 写 ~/.gemini/settings.json
    （第一次改这两个文件之前各备份一次，<文件>.railse-bak-<时间>），Codex 写 ~/.codex/hooks.json。
    --project 目录：Claude、Gemini 改写那个项目的项目级 .claude/settings.local.json（不入库；旧版写在 settings.json 的会迁走）
    与 .gemini/settings.json，Codex 照旧是用户级。
    Claude 的选择题（AskUserQuestion）也能在手机上答（由 PreToolUse 钩子转交，不想答时手机上可交回终端）。
    --dir 是守护进程的数据目录（默认 $RAILSE_HOME 或 ~/.railse）：不是 ~/.railse 时钩子命令带 --socket 指向它。
  railse setup autostart [--hub] [--uninstall] [--status] [--dir 目录] [--bin 路径] [--system] [--user 名字] [--dry-run] [-- hub run 的额外参数]
    开机自启并在退出后自动拉起（macOS launchd、Linux systemd），默认托管守护进程，--hub 托管 hub；详见 railse setup autostart -h。`: `  railse setup hooks [--project DIR] [--claude] [--codex] [--gemini] [--codex-file PATH] [--dir DIR] [--bin PATH] [--dry-run]
    Installs all three by default, all at user level (all projects): Claude writes ~/.claude/settings.json, Gemini writes ~/.gemini/settings.json
    (each is backed up once before its first change, to <file>.railse-bak-<time>), Codex writes ~/.codex/hooks.json.
    --project DIR: Claude and Gemini write that project's .claude/settings.local.json (not committed; hooks an older version wrote to settings.json are moved out)
    and .gemini/settings.json; Codex stays at user level.
    Claude's multiple-choice questions (AskUserQuestion) can be answered on the phone too (passed on by the PreToolUse hook; you can hand one back to the terminal from the phone).
    --dir is the daemon's data directory (default $RAILSE_HOME or ~/.railse); if it isn't ~/.railse, hook commands include --socket pointing to it.
  railse setup autostart [--hub] [--uninstall] [--status] [--dir DIR] [--bin PATH] [--system] [--user NAME] [--dry-run] [-- extra hub run args]
    Starts at boot and restarts on exit (macOS launchd, Linux systemd). Manages the daemon by default, the hub with --hub; see railse setup autostart -h.`,
	"用法:\n%s\n":   "Usage:\n%s\n",
	"\n用法:\n%s\n": "\nUsage:\n%s\n",

	// cmd/railse/setup.go：setup all 的步骤名（变量，step、fail 里用 i18n.Lookup 翻；完整性测试查不到，TestSetupStepNamesEnglish 核对）
	"本机 hub": "local hub",
	"接入 hub": "connect to the hub",
	"安装钩子":   "install hooks",
	"开机自启":   "autostart",
	"体检":     "health check",

	// cmd/railse/setup.go：setup hooks
	"警告：读不了 %s，没有检查里面有没有旧版写的 Railse 钩子：%v\n": "Warning: can't read %s, so it wasn't checked for Railse hooks written by an older version: %v\n",
	"警告：没有迁走 %s 里的 Railse 钩子：它就是 %s，这是用户级配置，迁走会让所有项目失去钩子；如确要迁走请手动删（不删的话，在这个目录里它会和 settings.local.json 里的一起触发两次）\n": "Warning: the Railse hooks in %s were not moved out: it is %s, the user-level config, and moving them would take the hooks away from every project. If you really want them gone, delete them by hand (otherwise, in this directory they fire twice along with the ones in settings.local.json)\n",
	"警告：%s：%v，跳过迁移；里面若有旧版写的 Railse 钩子请手动删掉，否则会和 settings.local.json 里的一起触发两次\n":                                    "Warning: %s: %v, skipping the move. If it has Railse hooks written by an older version, delete them by hand, or they fire twice along with the ones in settings.local.json\n",
	"项目目录：给了才把 Claude、Gemini 的钩子写成这个项目的项目级，默认写用户级":                                                                 "project directory: if given, the Claude and Gemini hooks are written at this project's level; by default they go in at user level",
	"只装 Claude Code": "install only Claude Code",
	"只装 Codex":       "install only Codex",
	"只装 Gemini":      "install only Gemini",
	"Codex 钩子文件，默认 ~/.codex/hooks.json":                                 "Codex hooks file, default ~/.codex/hooks.json",
	"守护进程数据目录，默认 $RAILSE_HOME 或 ~/.railse；不是 ~/.railse 时钩子命令带 --socket": "daemon data directory, default $RAILSE_HOME or ~/.railse; if it isn't ~/.railse, hook commands include --socket",
	"railse 可执行文件路径，默认当前程序":                                             "path to the railse executable, default this program",
	"只打印不写": "print only, don't write",
	"取不到家目录（%v），装不了用户级的 Claude、Gemini 钩子：用 --project 目录 装成那个项目的项目级\n": "Can't find your home directory (%v), so the user-level Claude and Gemini hooks can't be installed: use --project DIR to install them at that project's level\n",
	"== %s（迁走 Railse 钩子后）==\n%s": "== %s (after moving out the Railse hooks) ==\n%s",
	"== %s（会先备份到 %s）==\n%s":      "== %s (will be backed up to %s first) ==\n%s",
	"备份 %s 失败，没有改它：%v\n":         "Couldn't back up %s, so it was not changed: %v\n",
	"已备份 %s 到 %s\n":              "Backed up %s to %s\n",
	"已从 %s 迁走 Railse 钩子（现在写在 settings.local.json，不入库）\n": "Moved the Railse hooks out of %s (they're now in settings.local.json, which isn't committed)\n",
	"已写入 %s（用户级，对所有项目生效）\n":                              "Wrote %s (user level, applies to all projects)\n",
	"~/.railse（取不到家目录）":                                  "~/.railse (home directory not found)",
	"钩子命令带 --socket %s：守护进程数据目录 %s 不是 %s，CLI 起钩子时的环境里不一定有指向它的 RAILSE_HOME\n":                                 "Hook commands include --socket %s: the daemon data directory %s isn't %s, and the environment the CLIs run hooks in may not have a RAILSE_HOME pointing to it\n",
	"提醒 Codex：钩子只在交互会话里审批（codex exec 不触发）；首次使用请在 Codex 里用 /hooks 审核；notify（完成与提问事件）请运行 %s（会保留已有的 notify）。\n": "Reminder for Codex: hooks only handle approvals in interactive sessions (codex exec doesn't trigger them). Review them with /hooks in Codex the first time. For notify (finished and question events), run %s (it keeps any existing notify).\n",
	"提醒 Codex：钩子只在交互会话里审批（codex exec 不触发）；首次使用请在 Codex 里用 /hooks 审核。":                                        "Reminder for Codex: hooks only handle approvals in interactive sessions (codex exec doesn't trigger them). Review them with /hooks in Codex the first time.",
	"提醒 Gemini：钩子只能拒绝不能放行，需要 Gemini CLI 0.65.0 以上，不要开 yolo 模式。":                                              "Reminder for Gemini: hooks can deny but not approve. It needs Gemini CLI 0.65.0 or later; don't turn on yolo mode.",
	"（用户级已对所有项目生效；在项目里跑 railse doctor 也会指出）":                                                                 " (the user-level hooks already apply to all projects; railse doctor in the project points this out too)",
	"提醒：%s 里也有 Railse 的钩子，与用户级的条目完全相同，Claude Code 只跑一次：其中 Railse 的条目可删可不删%s\n":                               "Reminder: %s also has Railse hooks, identical to the user-level ones, so Claude Code runs them once. You can delete the Railse entries there or leave them%s\n",
	"提醒：%s 里也有 Railse 的钩子，与用户级的条目不同，Claude Code 会触发两次：建议删掉其中 Railse 的条目%s\n":                                 "Reminder: %s also has Railse hooks that differ from the user-level ones, so Claude Code fires twice. Delete the Railse entries there%s\n",
	"提醒：%s 里也有 Railse 的钩子，Gemini 会触发两次：建议删掉其中 Railse 的条目%s\n":                                                "Reminder: %s also has Railse hooks, so Gemini fires twice. Delete the Railse entries there%s\n",

	// cmd/railse/setup.go：setup all
	"hub 地址，如 https://hub.example.com":                                  "hub address, e.g. https://hub.example.com",
	"railse hub token 生成的接入码":                                           "enroll token from railse hub token",
	"在本机起 hub（Tailscale Funnel 给手机地址）并接入它，不用 --hub、--token":             "run a hub on this machine (Tailscale Funnel gives the phone an address) and enroll with it; no --hub or --token",
	"本机 hub 监听 127.0.0.1 的端口（只配合 --local-hub；不给时沿用上次装的）":                "port the local hub listens on at 127.0.0.1 (only with --local-hub; defaults to the last install)",
	"本机 hub 的数据目录（只配合 --local-hub），默认 $RAILSE_HUB_HOME 或 ~/.railse-hub": "local hub data directory (only with --local-hub), default $RAILSE_HUB_HOME or ~/.railse-hub",
	"本机 hub 的对外 https 地址（只配合 --local-hub）：用自己的隧道，不开 Tailscale Funnel；不给时沿用上次装的，给空值 --public-url= 换回 Tailscale": "public https address of the local hub (only with --local-hub): use your own tunnel instead of Tailscale Funnel; defaults to the last install, an empty --public-url= switches back to Tailscale",
	"本机在 hub 里的名字，默认主机名":                           "name for this machine on the hub, default the hostname",
	"项目目录：给了才把 Claude、Gemini 的钩子装成这个项目的项目级，默认装用户级": "project directory: if given, the Claude and Gemini hooks are installed at this project's level; by default they go in at user level",
	"守护进程数据目录，默认 $RAILSE_HOME 或 ~/.railse":         "daemon data directory, default $RAILSE_HOME or ~/.railse",
	"不托管守护进程的开机自启（--local-hub 的本机 hub 照样托管）":       "don't set up autostart for the daemon (the --local-hub hub is still installed as a service)",
	"不装钩子，也不接 Codex 的 notify":                      "don't install hooks or wire up Codex notify",
	"已接入过也重新接入，覆盖 machine.json":                    "enroll again even if already enrolled, overwriting machine.json",
	"多余的参数 %q":                                          "Unexpected arguments %q",
	"hub 机器（Linux）用 %s 装成系统服务":                          "on the hub machine (Linux), install it as a system service with %s",
	"macOS 上 hub 也不用 sudo：railse setup autostart --hub": "on macOS the hub doesn't need sudo either: railse setup autostart --hub",
	"setup all 要用平时的用户身份运行，不要 sudo：以 root 跑会用掉一次性接入码，machine.json 与钩子配置也会成为 root 的文件；%s\n": "Run setup all as your normal user, not with sudo: as root it would use up the one-time enroll token, and machine.json and the hook configs would end up owned by root; %s\n",
	"--local-hub 不和 --hub、--token 一起用：本机 hub 的接入码由 setup all 自己生成":                         "--local-hub doesn't go with --hub or --token: setup all creates the local hub's enroll token itself",
	"--hub-port、--hub-dir、--public-url 只配合 --local-hub 用":                                  "--hub-port, --hub-dir and --public-url only work with --local-hub",
	"--hub-port %d 不在 1..65535": "--hub-port %d is not in 1..65535",
	"需要 --hub 与 --token（接入码由 hub 上的 railse hub token 生成）": "Needs --hub and --token (get an enroll token with railse hub token on the hub)",
	"项目目录 %s 不存在或不是目录：--project 给要装项目级钩子的项目，不给则钩子装用户级\n":  "Project directory %s doesn't exist or isn't a directory: --project takes the project to install project-level hooks in; without it, hooks go in at user level\n",
	"重启守护进程":                                "restart the daemon",
	"托管并启动守护进程":                             "set up autostart for the daemon and start it",
	"读输入失败，%s：%v\n":                         "Couldn't read input; %s: %v\n",
	"已取消，%s\n":                              "Cancelled; %s\n",
	"%v\nmachine.json 坏了：用 %s 重新接入本机 hub\n": "%v\nmachine.json is broken: enroll with the local hub again with %s\n",
	"本机接的是 127.0.0.1:%d 的 hub，这次的本机 hub 在 127.0.0.1:%d；要改接，加 --force 重新接入\n":                  "This machine is enrolled with the hub at 127.0.0.1:%d, but this local hub is at 127.0.0.1:%d. To switch, add --force to enroll again\n",
	"本机已接入 %s，不是本机 hub；要改接本机 hub，加 --force 重新接入（手机上旧 hub 的图标可以删掉）\n":                          "This machine is enrolled with %s, not the local hub. To switch to the local hub, add --force to enroll again (you can delete the old hub's icon on the phone)\n",
	"本机接的是本机 hub，但读不了 %s，核对不了这台机器在不在里面：%v\n稍后重跑；一直这样就加 --force 重新接入：%s\n":                     "This machine is enrolled with the local hub, but %s can't be read, so it can't check whether this machine is in it: %v\nRerun later. If it keeps happening, add --force to enroll again: %s\n",
	"本机接的是本机 hub，但 %s 里没有这台机器（数据目录换过或删过？）；要接到这个数据目录的 hub，加 --force 重新接入（手机上要重新添加、重新开通知）：%s\n": "This machine is enrolled with the local hub, but %s doesn't have this machine (was the data directory changed or deleted?). To enroll with the hub in this data directory, add --force (then add it on the phone again and turn notifications back on): %s\n",
	"本机接的是本机 hub，但这台机器在 %s 的 hub 里已被吊销；要重新接入，加 --force：%s\n":                                  "This machine is enrolled with the local hub, but it has been revoked in the hub at %s. To enroll again, add --force: %s\n",
	"、": ", ",
	"railse setup all 停在第 %d 步（%s）": "railse setup all stopped at step %d (%s)",
	"；已完成：%s":                       "; done: %s",
	"；没做：%s":                        "; not done: %s",
	"。%s修好后重跑 %s（已接入的会跳过接入）\n":      ". %sOnce fixed, rerun %s (an enrolled machine skips enrolling).\n",
	"这个 tailnet 还没开通 Funnel：在浏览器里打开 %s 开通后重跑\n":                                                  "Funnel isn't enabled for this tailnet yet: open %s in a browser, enable it, then rerun\n",
	"本机 hub 已托管、只监听 127.0.0.1，开好 Funnel 之前手机连不上；在浏览器里开通 Funnel 后重跑同一条命令；不打算用本机 hub 了，用 %s 撤掉托管。": "The local hub is installed as a service and only listens on 127.0.0.1, so the phone can't reach it until Funnel is on. Enable Funnel in the browser and rerun the same command. If you no longer want the local hub, remove its service with %s. ",
	"没有找到 tailscale：请先安装并登录（https://tailscale.com/download）":                                     "Tailscale not found: install it and log in first (https://tailscale.com/download)",
	"已接入过（%s，接的就是本机 hub），跳过；要重新接入：%s\n":                                                          "Already enrolled (%s, with the local hub), skipping. To enroll again: %s\n",
	"machine.json 坏了：用 %s 重新接入（不在终端里时连同 --hub、--token）。":                                         "machine.json is broken: enroll again with %s (outside a terminal, add --hub and --token). ",
	"已接入过（%s），跳过；要换 hub 或重新接入：%s\n":                                                              "Already enrolled (%s), skipping. To switch hubs or enroll again: %s\n",
	"手机要打开 hub 的对外地址（不是本机接的 %s：它只在这台机器上打得开）":                                                     "On the phone, open the hub's public address (not %s, which this machine uses: it only opens on this machine)",
	"，这次没按本机 hub 复查（Funnel、托管）":                                                                  ", so this run didn't recheck it as a local hub (Funnel, service)",
	"%s 坏了":                     "%s is broken",
	"%s 说本机 hub 在 127.0.0.1:%d": "%s says the local hub is at 127.0.0.1:%d",
	"注意：本机接的是 127.0.0.1:%d 上的 hub，%s%s；看起来是 railse setup all --local-hub 装的，用 railse setup all --local-hub%s%s 复查\n":                                                                               "Note: this machine is enrolled with the hub at 127.0.0.1:%d and %s%s. It looks like railse setup all --local-hub installed it; recheck with railse setup all --local-hub%s%s\n",
	"注意：本机接的是 127.0.0.1:%d 上的 hub，%s 里没有 local-hub.json%s。如果是 railse setup all --local-hub 装的、数据目录不在默认位置，用 railse setup all --local-hub --hub-dir <它的数据目录>%s%s 复查；自己用 railse hub run 起的 hub 不用管\n": "Note: this machine is enrolled with the hub at 127.0.0.1:%d and %s has no local-hub.json%s. If railse setup all --local-hub installed it with its data directory elsewhere, recheck with railse setup all --local-hub --hub-dir <its data directory>%s%s. A hub you started yourself with railse hub run needs nothing\n",
	"注意：本机接的是 %s，不是 --hub 给的 %s；要改接那个 hub，加 --force 重新接入\n":                                                                                                                                        "Note: this machine is enrolled with %s, not %s from --hub. To switch to that hub, add --force to enroll again\n",
	"接入失败：%v\n":       "Enrolling failed: %v\n",
	"已跳过（--no-hooks）": "Skipped (--no-hooks)",
	"钩子文件的问题见上面（已写入的会列出来）。":                                                     "See above for the problem with the hook files (files already written are listed). ",
	"Codex 的 notify 没有接上（原因见第 %d 步），完成与提问事件进不了收件箱；处理好后运行 %s（会保留已有的 notify）":     "Codex notify isn't wired up (see step %d for why), so finished and question events won't reach the inbox. Once that's sorted, run %s (it keeps any existing notify)",
	"已跳过（--no-autostart）；守护进程要自己 railse agent run%s\n":                          "Skipped (--no-autostart). Start the daemon yourself with railse agent run%s\n",
	"守护进程正在跑，读不到刚写的 machine.json：已托管的守护进程用 railse setup autostart%s 重装或重启后生效\n": "The daemon is running and won't see the new machine.json: if it runs as a service, it takes effect after railse setup autostart%s or a restart\n",
	"守护进程正在跑，读不到刚写的 machine.json：手动起的停掉后重新 railse agent run%s 才接上 hub\n":        "The daemon is running and won't see the new machine.json: if you started it by hand, stop it and run railse agent run%s again to connect to the hub\n",
	"守护进程已在运行，但不是托管的那个（launchd/systemd 里的托管作业没在跑；应是手动 railse agent run 起的），跳过托管：再托管一个会抢不到 socket、被反复拉起；在跑的这个也只在启动时读 machine.json。\n先停掉它（在它的终端里 Ctrl-C），再运行：railse setup autostart%s\n": "A daemon is already running, but not the autostart one (the launchd/systemd job isn't running; it was probably started by hand with railse agent run), so autostart is skipped: a second daemon couldn't get the socket and would be restarted over and over, and the running one only reads machine.json at startup.\nStop it first (Ctrl-C in its terminal), then run: railse setup autostart%s\n",
	"没有托管开机自启（守护进程在手动运行），见第 %d 步":      "Autostart isn't set up (the daemon is running by hand), see step %d",
	"可以先手动 railse agent run%s 跑起守护进程。": "Meanwhile you can start the daemon by hand with railse agent run%s. ",
	"守护进程已应答": "The daemon answered",
	"守护进程等了 %s 还没有应答，下面的体检可能显示它没在跑\n":                          "The daemon hasn't answered after %s; the health check below may show it as not running\n",
	"体检有失败项；setup all 不因体检失败而失败：刚托管的守护进程可能还没起来，过几秒再跑 %s 复查。\n": "Some health checks failed. They don't fail setup all: the daemon may not be up yet, so run %s again in a few seconds.\n",
	"体检有失败项；setup all 不因体检失败而失败：按上面的“修复：”处理后再跑 %s 复查。\n":       "Some health checks failed. They don't fail setup all: follow the \"Fix:\" lines above, then run %s again.\n",
	"没能记住界面语言（%v），需要时运行 railse lang zh 或 railse lang en":       "Couldn't save the UI language (%v); run railse lang zh or railse lang en if needed",
	"\n注意：%s。": "\nNote: %s.",

	// cmd/railse/setup_localhub.go
	"hub 端口 %d 不在 1..65535": "Hub port %d is not in 1..65535",
	"本机 hub 只支持 macOS（launchd）与 Linux（systemd）：要靠它们托管 hub": "The local hub only works on macOS (launchd) and Linux (systemd): it needs them to run the hub as a service",
	"对外地址%w": "Public address: %w",
	"%s 已有一个不是托管的 hub 在跑（手动起的？）：先停掉它，或用 --hub-port 换端口": "A hub that isn't the managed service is already running on %s (started by hand?): stop it first, or pick another port with --hub-port",
	"%s 被别的程序占用：用 --hub-port 换一个":                       "%s is taken by another program: pick another port with --hub-port",
	"对外地址用你给的 %s（自己的隧道要把它转到 %s）\n":                      "Public address: %s, as given (your tunnel needs to forward it to %s)\n",
	"对外地址 %s（Tailscale Funnel 已把它转到 %s）\n":              "Public address: %s (Tailscale Funnel already forwards it to %s)\n",
	"对外地址 %s（hub 应答之后再开 Tailscale Funnel 把它转到 %s）\n":    "Public address: %s (Tailscale Funnel will forward it to %s once the hub answers)\n",
	"网页密码已设好（hub 数据目录 %s）\n":                            "Web password set (hub data directory %s)\n",
	"网页密码已设过，沿用（hub 数据目录 %s）\n":                         "Web password already set, keeping it (hub data directory %s)\n",
	"警告：%v；按这次的设置重写\n":                                  "Warning: %v; rewriting it with this run's settings\n",
	"对外地址从 %s 变成 %s：手机上旧的图标要删掉重新添加、重新开启通知":              "The public address changed from %s to %s: on the phone, delete the old icon, add it again and turn notifications back on",
	"之前用的 Tailscale Funnel 可能还开着（tailscale funnel status 看），%s 可能仍是公网入口（转到本机 127.0.0.1:%d）；不用就 tailscale funnel --https=443 off": "The Tailscale Funnel used before may still be on (check with tailscale funnel status), so %s may still be open to the internet (forwarding to 127.0.0.1:%d on this machine). If you don't need it, run tailscale funnel --https=443 off",
	"本机 hub 已在跑（版本 %s），跳过托管\n":                                        "The local hub is already running (version %s), leaving its service as is\n",
	"本机 hub 还是旧版本（%s，本程序是 %s）：Claude Code 的选择题会直接交回终端，请重启本机 hub：%s\n": "The local hub is still the old version (%s, this program is %s): Claude Code questions will go straight back to the terminal. Restart the local hub: %s\n",
	"托管并启动 hub（只监听 %s）":                                               "Installing the hub as a service and starting it (listening only on %s)",
	"托管的 hub 不是按这次的对外地址与端口装的，重新托管":                                    "The hub service wasn't installed with this public address and port, reinstalling it",
	"托管 hub 失败，见上面":                                                   "Couldn't install the hub as a service, see above",
	"hub 托管了但 %s 内没应答：看 %s":                                           "The hub service is installed but didn't answer within %s: see %s",
	"本机 hub 已应答（版本 %s）\n":                                             "The local hub answered (version %s)\n",
	"hub 应答过后又没了应答，没开 Funnel：看 %s":                                    "The hub answered, then stopped answering, so Funnel was not turned on: see %s",
	"%s 上应答的不是托管的 hub（托管作业没在跑，端口被别的程序抢了？），没开 Funnel：先停掉占着端口的程序，或用 --hub-port 换端口": "Whatever answers on %s isn't the hub service (the service job isn't running; did another program take the port?), so Funnel was not turned on: stop the program on that port, or pick another port with --hub-port",
	"已生成一次性接入码（10 分钟内有效，只交给下一步接入本机守护进程，不显示）":                                      "Created a one-time enroll token (valid for 10 minutes, only passed to the next step to enroll this machine's daemon, not shown)",
	"本机 Tailscale 没在运行或没登录（状态 %s）：打开 Tailscale 登录后重跑":                             "Tailscale isn't running or isn't logged in on this machine (state %s): open Tailscale, log in, then rerun",
	"这台机器在 Tailscale 里没有域名：在管理台开启 MagicDNS 后重跑":                                   "This machine has no domain name in Tailscale: turn on MagicDNS in the admin console, then rerun",
	"这个 tailnet 没开 HTTPS 证书：管理台 DNS 页开启 HTTPS Certificates 后重跑":                   "HTTPS certificates are off for this tailnet: turn on HTTPS Certificates on the DNS page of the admin console, then rerun",
	"别的内容（不是反向代理）": "other content (not a reverse proxy)",
	"https://%s 上除了 / 还有 serve 配置：%s。Funnel 按 %s 整体开，开了它们会一起公开到公网，所以没开：先用 tailscale serve 撤掉它们（tailscale serve status 看现有配置）后重跑，或用 --public-url 走自己的隧道": "https://%s has serve config besides /: %s. Funnel turns on for all of %s at once, which would put them on the internet too, so it was not turned on. Remove them with tailscale serve (see the current config with tailscale serve status) and rerun, or use your own tunnel with --public-url",
	"将替换 https://%s 原来指向 %s 的 Funnel/serve 配置\n":                          "Replacing the Funnel/serve config for https://%s, which pointed to %s\n",
	"开 Tailscale Funnel：https://%s 转到 %s\n":                               "Turning on Tailscale Funnel: https://%s to %s\n",
	"tailnet 没开通 Funnel 时 tailscale 会等你去浏览器开通，最多等 %s，之后这里会打印开通链接\n":       "If Funnel isn't enabled for your tailnet, tailscale waits for you to enable it in a browser, for up to %s; then the link is printed here\n",
	"Tailscale Funnel 已把 https://%s 转到 %s\n":                              "Tailscale Funnel now forwards https://%s to %s\n",
	"开了 Funnel 但 serve 配置里没看到 https://%s 转到 %s，看 tailscale funnel status": "Funnel was turned on, but the serve config doesn't show https://%s forwarding to %s: check tailscale funnel status",

	// cmd/railse/localhub.go
	"必须是 https:// 地址（手机加到主屏幕、开通知都要 https）": "must be an https:// address (the phone needs https to add it to the Home Screen and get notifications)",
	"不能带路径，只要 https://主机[:端口]":             "can't have a path, only https://host[:port]",
	"port %d 不在 1..65535":                  "port %d is not in 1..65535",
	"public_url 不合规：%w":                    "public_url is invalid: %w",
	"读 %s: %w":                             "read %s: %w",
	"%s 不是合法 JSON: %w":                     "%s is not valid JSON: %w",
	"本机 hub 描述不合规: %w":                     "invalid local hub description: %w",
	"hub 还没设网页密码：先运行 railse hub password（--dir %s），或在终端里跑 setup": "The hub has no web password yet: run railse hub password (--dir %s) first, or run setup in a terminal",
	"本机 hub 还没设网页密码，在这里输入一个后回车（至少 8 个字符，手机登录用）。屏幕会回显；不想回显就 Ctrl-C，先这样设好再重跑：\n  read -rs PW; printf '%%s\\n' \"$PW\" | railse hub password --dir %s; unset PW\n": "The local hub has no web password yet. Type one here and press Enter (at least 8 characters, used to log in on the phone). It shows on screen; to avoid that, press Ctrl-C, set it like this and rerun:\n  read -rs PW; printf '%%s\\n' \"$PW\" | railse hub password --dir %s; unset PW\n",
	"本机开着代理；手机若也经同一代理打不开 %s，关掉代理再试，或在代理规则里把 ts.net 设为直连":                                                                                                        "This machine is using a proxy. If your phone goes through the same proxy and can't open %s, turn the proxy off and retry, or set ts.net to connect directly in the proxy rules",

	// cmd/railse/setup_codex.go
	`  railse setup codex-notify [--dir 目录] [--bin 路径] [--dry-run]
    Codex 只认一条 notify：生成 <数据目录>/codex-notify.sh（数据目录默认 $RAILSE_HOME 或 ~/.railse），依次调用 ~/.codex/config.toml
    里原有的 notify 与 railse hook codex-notify，再把 config.toml 顶层的 notify 换成这个脚本（每次改之前备份到 config.toml.railse-bak-<时间>）。
    重跑不会重复串接；换了 --dir 或 --bin 重跑，原有的 notify 照样保留。数据目录不是 ~/.railse 时 railse 那一行带 --socket。`: `  railse setup codex-notify [--dir DIR] [--bin PATH] [--dry-run]
    Codex takes only one notify command. This writes <data dir>/codex-notify.sh (data dir defaults to $RAILSE_HOME or ~/.railse), which calls the notify
    already in ~/.codex/config.toml and then railse hook codex-notify, and points the top-level notify in config.toml at the script (backed up to config.toml.railse-bak-<time> before every change).
    Rerunning doesn't chain twice; rerunning with a different --dir or --bin still keeps the original notify. If the data dir isn't ~/.railse, the railse line includes --socket.`,
	"守护进程数据目录（脚本放在这里），默认 $RAILSE_HOME 或 ~/.railse；不是 ~/.railse 时 railse 那一行带 --socket": "daemon data directory (the script goes here), default $RAILSE_HOME or ~/.railse; if it isn't ~/.railse, the railse line includes --socket",
	"多余的参数 %q\n用法:\n%s\n":                   "Unexpected arguments %q\nUsage:\n%s\n",
	"取不到家目录（%v），找不到 ~/.codex/config.toml\n": "Can't find your home directory (%v), so ~/.codex/config.toml can't be found\n",
	"%s 看不懂（%v），什么都没改：请手动改好这一处（notify 要写成第一个 [表] 之前的字符串数组，如 notify = [\"/path/to/cmd\", \"--flag\"]）再重跑 %s\n": "Can't parse %s (%v), nothing was changed. Fix that part by hand (notify must be an array of strings before the first [table], e.g. notify = [\"/path/to/cmd\", \"--flag\"]), then rerun %s\n",
	"警告：%s 里 %s 下面的 notify 是那个表的键，Codex 读不到，已忽略（没有串进脚本）；请手动删掉它\n":                                             "Warning: in %s, the notify under %s belongs to that table, so Codex doesn't read it. It was ignored (not chained into the script); delete it by hand\n",
	"（已是最新）":                              " (up to date)",
	"（会先备份到 %s）":                          " (will be backed up to %s first)",
	"%s 已是最新\n":                           "%s is up to date\n",
	"已写入 %s：notify 指向 %s\n":               "Wrote %s: notify points to %s\n",
	"Codex 的 notify 先调原来的 %s，再调 railse\n": "Codex notify calls the previous %s first, then railse\n",
	"Codex 的 notify 只调 railse（没有前一跳）":     "Codex notify calls only railse (there was no previous notify)",
	"没有标记行":                               "no marker line",
	"警告：原来的 notify 记在 %s 的标记行里，读不到（%v），前一跳没有保留；要找回请看 %s*\n":                              "Warning: the previous notify is recorded in the marker line of %s, which can't be read (%v), so it wasn't kept. To recover it, look at %s*\n",
	"警告：原来的 notify（%s）会调到这次的脚本 %s 自己，没有串成前一跳（串上也只是又进到脚本自己、什么都不做）；要找回原来的 notify 请看 %s*\n": "Warning: the previous notify (%s) would call this script %s itself, so it wasn't chained (that would only re-enter the script and do nothing). To recover the previous notify, look at %s*\n",
}
