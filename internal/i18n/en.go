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
	"当前语言 %s（来源：%s）\n":            "Current language: %s (source: %s)\n",
	"语言只能是 zh 或 en，不是 %q\n\n%s\n": "Language must be zh or en, not %q\n\n%s\n",
	"找不到家目录，没法写 lang 文件":          "Can't find your home directory, so the lang file can't be written",
	"写不了 lang 文件：%v\n":            "Can't write the lang file: %v\n",
	"注意：环境变量 RAILSE_LANG=%s 优先于 lang 文件；要让 lang 文件生效，请去掉它。\n": "Note: RAILSE_LANG=%s is set and takes priority over the lang file. Unset it for the lang file to apply.\n",
	"已写入 %s\n":         "Wrote %s\n",
	"多余的参数 %q\n%s\n":   "Unexpected argument %q\n%s\n",
	"环境变量 RAILSE_LANG": "RAILSE_LANG environment variable",
	"lang 文件":          "lang file",
	"系统 LANG":          "system LANG",
	"命令指定":             "this command",
	"默认":               "default",
	`用法: railse lang [zh|en]
不带参数打印当前语言和它从哪来；带参数写进数据目录的 lang 文件（$RAILSE_HOME 或 ~/.railse）。
解析顺序：环境变量 RAILSE_LANG → lang 文件 → 系统 LANG（zh 开头算中文，其余英文）。`: `Usage: railse lang [zh|en]
Without an argument, prints the current language and where it came from. With one, writes it to the lang file in the data directory ($RAILSE_HOME or ~/.railse).
Resolution order: the RAILSE_LANG environment variable, then the lang file, then the system LANG (zh* means Chinese, anything else English).`,

	// cmd/railse/setup_wizard.go
	"接入 hub 还缺几项，逐个回答（Ctrl-D 取消，什么都不做）：":    "To enroll with the hub, answer a few questions (Ctrl-D cancels and changes nothing):",
	"hub 地址（如 https://hub.example.com）：":    "Hub address (e.g. https://hub.example.com): ",
	"接入码（在 hub 机器上运行 railse hub token 生成）：": "Enroll token (run railse hub token on the hub machine to get one): ",
	"接入码不能为空":                 "The enroll token can't be empty",
	"本机在 hub 里的名字（直接回车用 %s）：": "Name for this machine on the hub (Enter uses %s): ",
	"名字%w": "The name %w",
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
	"hub 地址%w": "The hub address %w",
	"%s；确在可信内网的，用 railse setup all --hub <地址> 给出":               "%s. If it really is on a trusted private network, pass it with railse setup all --hub <address>",
	"hub 地址是 http://，明文传输密钥，只适合本机或可信内网；公网请用 https://":           "The hub address is http://, which sends the key in plain text. That's only OK on this machine or a trusted private network; over the internet, use an https:// address",
	"\n下一步：在手机上打开 hub 的地址":                                      "\nNext: open the hub's address on your phone",
	"\n下一步：在手机上打开\n╔%s╗\n║  %s  ║\n╚%s╝\n":                      "\nNext: open this on your phone\n╔%s╗\n║  %s  ║\n╚%s╝\n",
	"这个地址由 Tailscale 提供；手机挂着代理打不开时关掉代理再试。":                      "This address comes from Tailscale. If your phone uses a proxy and the address won't open, turn the proxy off and try again.",
	"手机 Safari 打开后分享→添加到主屏幕，再从图标打开；登录后在设置里开启通知（见 docs/web.md）。": "In Safari on the phone: Share → Add to Home Screen, then open it from the icon; after logging in, turn on notifications in Settings (see docs/web.md).",
	"已取消，什么都没做":        "Cancelled; nothing was done",
	"读输入失败，什么都没做：%v\n": "Can't read input; nothing was done: %v\n",

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
    One step: enroll with the hub → install hooks for the three CLIs → autostart → health check (railse doctor). Hooks go in at user level by default (all projects);
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

	// cmd/railse/setup.go：setup all 的步骤名（变量，step、fail 里用 i18n.Lookup 翻；完整性测试查不到，TestSetupStepNamesEnglish 核对）。
	// 小写：它们也嵌在 fail 的句子里；step 打印标题时由 capFirst 把首字母转大写
	"本机 hub": "local hub",
	"接入 hub": "enroll with the hub",
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
	"== %s（迁走 Railse 钩子后）==\n%s":                         "== %s (after moving out the Railse hooks) ==\n%s",
	"== %s（会先备份到 %s）==\n%s":                              "== %s (will be backed up to %s first) ==\n%s",
	"备份 %s 失败，没有改它：%v\n":                                 "Can't back up %s, so it was not changed: %v\n",
	"已备份 %s 到 %s\n":                                      "Backed up %s to %s\n",
	"已从 %s 迁走 Railse 钩子（现在写在 settings.local.json，不入库）\n": "Moved the Railse hooks out of %s (they're now in settings.local.json, which isn't committed)\n",
	"已写入 %s（用户级，对所有项目生效）\n":                              "Wrote %s (user level, applies to all projects)\n",
	"~/.railse（取不到家目录）":                                  "~/.railse (home directory not found)",
	"钩子命令带 --socket %s：守护进程数据目录 %s 不是 %s，CLI 起钩子时的环境里不一定有指向它的 RAILSE_HOME\n":                                 "Hook commands include --socket %s: the daemon data directory %s isn't %s, and the environment the CLIs run hooks in may not have a RAILSE_HOME pointing to it\n",
	"提醒 Codex：钩子只在交互会话里审批（codex exec 不触发）；首次使用请在 Codex 里用 /hooks 审核；notify（完成与提问事件）请运行 %s（会保留已有的 notify）。\n": "Reminder for Codex: hooks only handle approvals in interactive sessions (codex exec doesn't trigger them). Review them with /hooks in Codex the first time. For notify (finished and question events), run %s (it keeps any existing notify).\n",
	"提醒 Codex：钩子只在交互会话里审批（codex exec 不触发）；首次使用请在 Codex 里用 /hooks 审核。":                                        "Reminder for Codex: hooks only handle approvals in interactive sessions (codex exec doesn't trigger them). Review them with /hooks in Codex the first time.",
	"提醒 Gemini：钩子只能拒绝不能放行，需要 Gemini CLI 0.65.0 以上，不要开 yolo 模式。":                                              "Reminder for Gemini: hooks can deny but not approve. It needs Gemini CLI 0.65.0 or later; don't turn on yolo mode.",
	"（用户级已对所有项目生效；在项目里跑 railse doctor 也会指出）":                                                                 " (the user-level hooks already apply to all projects; railse doctor in the project points this out too)",
	"提醒：%s 里也有 Railse 的钩子，与用户级的条目完全相同，Claude Code 只跑一次：其中 Railse 的条目可删可不删%s\n":                               "Reminder: %s also has Railse hooks, identical to the user-level ones, so Claude Code runs them once. You can delete the Railse entries there or leave them%s\n",
	"提醒：%s 里也有 Railse 的钩子，与用户级的条目不同，Claude Code 会触发两次：建议删掉其中 Railse 的条目%s\n":                                 "Reminder: %s also has Railse hooks that differ from the user-level ones, so Claude Code runs them twice. Delete the Railse entries there%s\n",
	"提醒：%s 里也有 Railse 的钩子，Gemini 会触发两次：建议删掉其中 Railse 的条目%s\n":                                                "Reminder: %s also has Railse hooks, so Gemini runs them twice. Delete the Railse entries there%s\n",

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
	"--local-hub 不和 --hub、--token 一起用：本机 hub 的接入码由 setup all 自己生成":                         "--local-hub doesn't work with --hub or --token: setup all creates the local hub's enroll token itself",
	"--hub-port、--hub-dir、--public-url 只配合 --local-hub 用":                                  "--hub-port, --hub-dir and --public-url only work with --local-hub",
	"--hub-port %d 不在 1..65535": "--hub-port %d is not in 1..65535",
	"需要 --hub 与 --token（接入码由 hub 上的 railse hub token 生成）": "--hub and --token are required (get an enroll token by running railse hub token on the hub)",
	"项目目录 %s 不存在或不是目录：--project 给要装项目级钩子的项目，不给则钩子装用户级\n":  "Project directory %s doesn't exist or isn't a directory: --project takes the project to install project-level hooks in; without it, hooks go in at user level\n",
	"重启守护进程":        "restart the daemon",
	"托管并启动守护进程":     "set up autostart for the daemon and start it",
	"读输入失败，%s：%v\n": "Can't read input; %s: %v\n",
	"已取消，%s\n":      "Cancelled; %s\n",
	"%v\nmachine.json 坏了：用 %s 重新接入本机 hub\n":                                                   "%v\nmachine.json is broken: enroll with the local hub again with %s\n",
	"本机接的是 127.0.0.1:%d 的 hub，这次的本机 hub 在 127.0.0.1:%d；要改接，加 --force 重新接入\n":                  "This machine is enrolled with the hub at 127.0.0.1:%d, but this local hub is at 127.0.0.1:%d. To switch, add --force to enroll again\n",
	"本机已接入 %s，不是本机 hub；要改接本机 hub，加 --force 重新接入（手机上旧 hub 的图标可以删掉）\n":                          "This machine is enrolled with %s, not the local hub. To switch to the local hub, add --force to enroll again (you can delete the old hub's icon on the phone)\n",
	"本机接的是本机 hub，但读不了 %s，核对不了这台机器在不在里面：%v\n稍后重跑；一直这样就加 --force 重新接入：%s\n":                     "This machine is enrolled with the local hub, but %s can't be read, so it can't check whether this machine is in it: %v\nRerun later. If it keeps happening, add --force to enroll again: %s\n",
	"本机接的是本机 hub，但 %s 里没有这台机器（数据目录换过或删过？）；要接到这个数据目录的 hub，加 --force 重新接入（手机上要重新添加、重新开通知）：%s\n": "This machine is enrolled with the local hub, but %s doesn't have this machine (was the data directory changed or deleted?). To enroll with the hub in this data directory, add --force (then add it on the phone again and turn notifications back on): %s\n",
	"本机接的是本机 hub，但这台机器在 %s 的 hub 里已被吊销；要重新接入，加 --force：%s\n":                                  "This machine is enrolled with the local hub, but it has been revoked in the hub at %s. To enroll again, add --force: %s\n",
	"、": ", ",
	"railse setup all 停在第 %d 步（%s）": "railse setup all stopped at step %d (%s)",
	"；已完成：%s":                       "; done: %s",
	"；没做：%s":                        "; not done: %s",
	"。%s修好后重跑 %s（已接入的会跳过接入）\n":                  ". %sOnce fixed, rerun %s (an enrolled machine skips enrolling).\n",
	"这个 tailnet 还没开通 Funnel：在浏览器里打开 %s 开通后重跑\n": "Funnel isn't enabled for this tailnet yet: open %s in a browser, enable it, then rerun\n",
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
	"没能记住界面语言（%v），需要时运行 railse lang zh 或 railse lang en":       "Can't save the UI language (%v); run railse lang zh or railse lang en if needed",
	"\n注意：%s。": "\nNote: %s.",

	// cmd/railse/setup_localhub.go
	"hub 端口 %d 不在 1..65535": "Hub port %d is not in 1..65535",
	"本机 hub 只支持 macOS（launchd）与 Linux（systemd）：要靠它们托管 hub": "The local hub only works on macOS (launchd) and Linux (systemd): it needs them to run the hub as a service",
	"对外地址%w": "The public address %w",
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
	"托管 hub 失败，见上面":                                                   "Installing the hub as a service failed, see above",
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
	"读 %s: %w":                             "can't read %s: %w",
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

	// cmd/railse/doctor.go（doctorUsage 里的 %s 是系统单元的 hub 数据目录）
	`用法:
  railse doctor [--project 目录] [--dir 目录] [--hub-dir 目录] [--bin 路径] [--json]
    检查守护进程、hub 连接、三个 CLI 的钩子与版本、项目根、文件权限；这台机器有 hub 数据目录时还检查数据库、密码与推送密钥，
    有 local-hub.json（setup all --local-hub 装的本机 hub）时再看对外地址：Funnel 配置，并从本机访问一次。
    只读：不改配置、不建文件；会起的进程只有 claude、codex、gemini 的 --version 与 tailscale serve status --json（带超时，sudo 下不跑）。
    hub 数据目录默认 $RAILSE_HUB_HOME，否则 ~/.railse-hub，Linux 上两者都没有时再看系统单元的 %s
    （只在它归当前用户或以 root 运行时）；默认目录都不在就跳过 hub 侧，显式给的 --hub-dir 不在则报失败。
    有任一项失败退出码 1。`: `Usage:
  railse doctor [--project DIR] [--dir DIR] [--hub-dir DIR] [--bin PATH] [--json]
    Checks the daemon, the hub connection, the hooks and versions of the three CLIs, the project root and file permissions. If this machine has a hub data directory,
    it also checks the database, the password and the push keys, and if there's a local-hub.json (a local hub installed by setup all --local-hub), the public address: the Funnel config, plus one request from this machine.
    Read-only: it changes no config and creates no files. The only processes it starts are claude, codex and gemini --version and tailscale serve status --json (with timeouts, and not under sudo).
    The hub data directory defaults to $RAILSE_HUB_HOME, then ~/.railse-hub; on Linux, if neither exists, it also looks at the system service's %s
    (only if that belongs to the current user or doctor runs as root). If no default directory exists, the hub checks are skipped; an explicit --hub-dir that doesn't exist is a failure.
    Exits with 1 if any check fails.`,
	"项目目录（检查项目级钩子与项目根）；不给时看当前目录，它不是 git 仓库或在系统目录里时项目根只告知":                         "project directory (for project-level hooks and the project root); defaults to the current directory, and if that isn't a git repo or is in a system directory, the project root is only reported",
	"hub 数据目录，默认 $RAILSE_HUB_HOME 或 ~/.railse-hub，Linux 上再看 %s（默认目录都不在则跳过 hub 侧）": "hub data directory, default $RAILSE_HUB_HOME or ~/.railse-hub, and %s on Linux (if no default directory exists, the hub checks are skipped)",
	"输出 JSON": "output JSON",
	"系统 hub 目录 %s 属于别的用户；要检查请用那个用户运行 doctor 或给 --hub-dir": "the system hub directory %s belongs to another user; to check it, run doctor as that user or pass --hub-dir",
	"没有 hub 数据目录（查过 %s）":      "no hub data directory (looked in %s)",
	"没有 hub 数据目录（查过 %s 与 %s）": "no hub data directory (looked in %s and %s)",

	// cmd/railse/hook.go（钩子留在用户终端 stderr 里的句子；都接在 "railse hook: " 后面，所以小写开头）
	`用法:
  railse hook claude|codex|gemini [--socket 路径]      读 stdin 的钩子 JSON
  railse hook codex-notify [--socket 路径] <json>      Codex notify 的最后一个参数`: `Usage:
  railse hook claude|codex|gemini [--socket path]      reads the hook JSON from stdin
  railse hook codex-notify [--socket path] <json>      the last argument Codex notify passes`,
	"父进程（CLI）已退出":               "the parent process (the CLI) has exited",
	"内部错误: %v":                  "internal error: %v",
	"收到信号 %v":                   "got signal %v",
	"守护进程 socket 路径":            "daemon socket path",
	"参数错误: %v":                  "bad arguments: %v",
	"codex-notify 需要一个 JSON 参数": "codex-notify needs a JSON argument",
	"不认识的 CLI %s":               "unknown CLI %s",
	"请求没有对应的适配器":                "no adapter for this request",
	"读 stdin: %w":               "reading stdin: %w",
	"，交回终端处理":                   "; handed back to the terminal",
	"命中规则 %s":                   "Matched rule %s",

	// cmd/railse/hook.go 的 localizeReason：守护进程回传的固定理由，用 i18n.Lookup 查（完整性测试查不到，TestLocalizeReasonFixedSentences 核对）。
	// 原文逐字抄自 internal/agent/hubdecider.go 的 reason* 常量、internal/agent/server.go 与 requests.go 写进答复的固定句子、
	// internal/policy/engine.go 的暂停理由；那边改了这里要一起改。只有拒绝理由会显示（接在 "Railse: " 后面，所以大写开头）。
	"已在手机上批准":                    "Approved on your phone",
	"已在手机上拒绝":                    "Denied on your phone",
	"已在手机上回答":                    "Answered on your phone",
	"在手机上选了在终端回答":                "You chose on your phone to answer in the terminal",
	"Gemini 只能拒绝不能放行，交回原生确认框":    "Gemini hooks can deny but not approve; handed back to Gemini's own prompt",
	"没有配置 hub，交回终端处理":            "No hub is set up; handed back to the terminal",
	"连不上 hub，交回终端处理":             "Couldn't reach the hub; handed back to the terminal",
	"hub 拒绝了本机密钥，交回终端处理":         "The hub rejected this machine's key; handed back to the terminal",
	"无法送往 hub，交回终端处理":            "Couldn't send it to the hub; handed back to the terminal",
	"与 hub 断线超过宽限，交回终端处理":        "Lost the hub connection for too long; handed back to the terminal",
	"hub 没有给出有效决定，交回终端处理":        "The hub didn't return a valid decision; handed back to the terminal",
	"hub 版本太旧，不认识提问，交回终端处理":      "The hub is too old to handle questions; handed back to the terminal",
	"请求格式错误":                     "Malformed request",
	"auto 模式下交给 Claude 的分类器":     "Left to Claude's classifier in auto mode",
	"刚才已交回终端，这次不再问":              "Already handed back to the terminal; not asking again",
	"自动审批已暂停，交回终端处理":             "Auto-approval is paused; handed back to the terminal",
	"自动审批已暂停":                    "Auto-approval is paused",
	"本机没有连接 hub，交回终端处理":          "This machine isn't connected to a hub; handed back to the terminal",
	"等待过期，交回终端处理":                "Timed out; handed back to the terminal",
	"守护进程正在停止，交回终端处理":            "The daemon is stopping; handed back to the terminal",
	"决定来源出错，交回终端处理":              "Something went wrong while deciding; handed back to the terminal",
	"决定来源给的答案不完整，交回终端处理":         "The answer was incomplete; handed back to the terminal",
	"脱敏后有题面相同的题，分不清答的是哪道，交回终端处理": "Two questions read the same after redaction, so answers can't be matched; handed back to the terminal",

	// internal/hookclient/client.go
	"守护进程无响应":      "the daemon isn't responding",
	"连接守护进程: %w":   "can't connect to the daemon: %w",
	"发送请求":         "sending the request",
	"等待答复":         "waiting for the reply",
	"守护进程拒绝事件: %s": "the daemon rejected the event: %s",

	// internal/adapter/adapter.go（ValidateAsk 也被守护进程与 hub 调用，那里随各自进程的语言）
	"Railse：已拒绝": "Railse: denied",
	"Railse：已放行": "Railse: allowed",
	"Railse：":    "Railse: ",
	"AskUserQuestion 的题目不合规: 没有题目":                                "invalid AskUserQuestion: no questions",
	"AskUserQuestion 的题目不合规: 有 %d 题，最多 %d 题":                      "invalid AskUserQuestion: %d questions, at most %d",
	"AskUserQuestion 的题目不合规: 第 %d 题 question 为空":                  "invalid AskUserQuestion: question %d is empty",
	"AskUserQuestion 的题目不合规: 第 %d 题 question 超过 %d 字符":            "invalid AskUserQuestion: question %d is over %d characters",
	"AskUserQuestion 的题目不合规: 第 %d 题与第 %d 题的 question 相同":          "invalid AskUserQuestion: question %d is the same as question %d",
	"AskUserQuestion 的题目不合规: 第 %d 题 header 超过 %d 字符":              "invalid AskUserQuestion: question %d header is over %d characters",
	"AskUserQuestion 的题目不合规: 第 %d 题有 %d 个选项，最多 %d 个":              "invalid AskUserQuestion: question %d has %d options, at most %d",
	"AskUserQuestion 的题目不合规: 第 %d 题第 %d 个选项 label 为空":             "invalid AskUserQuestion: question %d option %d has an empty label",
	"AskUserQuestion 的题目不合规: 第 %d 题第 %d 个选项 label 超过 %d 字符":       "invalid AskUserQuestion: question %d option %d label is over %d characters",
	"AskUserQuestion 的题目不合规: 第 %d 题第 %d 个选项 description 超过 %d 字符": "invalid AskUserQuestion: question %d option %d description is over %d characters",
	"解析 tool_input: %w": "can't parse tool_input: %w",

	// internal/adapter/claude.go、codex.go、gemini.go
	"解析 Claude 钩子输入: %w":                          "can't parse the Claude hook input: %w",
	"PermissionRequest 缺少 session_id 或 tool_name": "PermissionRequest is missing session_id or tool_name",
	"PreToolUse 缺少 session_id":                    "PreToolUse is missing session_id",
	"AskUserQuestion 的题目不合规: 解析 tool_input: %w":   "invalid AskUserQuestion: can't parse tool_input: %w",
	"解析 Codex 钩子输入: %w":                           "can't parse the Codex hook input: %w",
	"解析 Codex notify 参数: %w":                      "can't parse the Codex notify argument: %w",
	"解析 Gemini 钩子输入: %w":                          "can't parse the Gemini hook input: %w",
	"BeforeTool 缺少 session_id 或 tool_name":        "BeforeTool is missing session_id or tool_name",

	// internal/agent/config.go
	"找不到家目录: %w": "can't find your home directory: %w",

	// internal/doctor/report.go
	"：%s":             ": %s",
	"        修复：%s\n": "        Fix: %s\n",
	"\n%d 项通过，%d 项警告，%d 项失败，%d 项跳过（railse %s）\n": "\n%d passed, %d warned, %d failed, %d skipped (railse %s)\n",

	// internal/doctor/doctor.go
	"取不到家目录（$HOME 没设），跳过": "home directory not found ($HOME isn't set), skipped",
	"%s 超过 %s 没有结束，已终止":   "%s didn't finish within %s and was stopped",
	"守护进程答复失败: %s":        "the daemon replied with an error: %s",

	// internal/doctor/agent.go
	"%s 不存在": "%s doesn't exist",
	"确认用跑 railse 的同一个用户运行 doctor": "Run doctor as the same user that runs railse",
	"%s 不是目录":             "%s isn't a directory",
	"移走这个文件，%s":           "Move this file away. %s",
	"%s 不是普通文件":           "%s isn't a regular file",
	"移走它，%s":              "Move it away. %s",
	"%s 权限是 %04o，应为 %04o": "%s has mode %04o, should be %04o",
	"守护进程数据目录":            "Daemon data directory",
	"先运行一次 railse agent run，它会建好目录与文件": "Run railse agent run once; it creates the directory and files",
	"守护进程开机自启":                         "Daemon autostart",
	"项目根":                              "Project root",
	"没给项目目录":                           "no project directory given",
	"取不到 %s 的绝对路径: %v":                 "can't get the absolute path of %s: %v",
	"目录 %s 不是 git 仓库，在这里起 agent 会按当前目录当项目根":        "%s isn't a git repo; an agent started here uses the current directory as the project root",
	"目录 %s 不是 git 仓库，按当前目录当项目根":                    "%s isn't a git repo, so the current directory is the project root",
	"建议在 %s 里 git init（项目根决定哪些写入算项目内）":             "Run git init in %s (the project root decides which writes count as inside the project)",
	"目录 %s 不在家目录下的普通目录里，不按项目根处理":                   "%s isn't a regular directory under your home directory, so it gets no project root",
	"要自动放行请 git init 或换到项目目录":                      "For auto-approval, run git init or switch to a project directory",
	"目录 %s 在系统目录里，不按项目根处理（是 git 仓库也一样）":            "%s is inside a system directory, so it gets no project root (even if it's a git repo)",
	"要自动放行请换到系统目录之外的项目目录":                          "For auto-approval, use a project directory outside system directories",
	"%s 是家目录、它的上级或根目录，不当项目根：在这里跑 agent 时所有写入都算项目外": "%s is your home directory, one of its parents or the root, so it isn't a project root: an agent run here treats every write as outside the project",
	"%s 不是存在的目录，不当项目根：在这里跑 agent 时所有写入都算项目外":       "%s isn't an existing directory, so it isn't a project root: an agent run here treats every write as outside the project",
	"配置与审计文件权限":       "Config and audit file permissions",
	"不是 0600：%s":      "not 0600: %s",
	"%s（存在的）都是 0600":  "%s (those that exist) are all 0600",
	"socket 文件":       "Socket file",
	"%s 不存在（守护进程没在跑）": "%s doesn't exist (the daemon isn't running)",
	"railse agent run（前台）或 railse setup autostart（开机自启）": "Run railse agent run (foreground) or railse setup autostart (starts at boot)",
	"%s 不是 socket":            "%s isn't a socket",
	"移走这个文件后重启守护进程":           "Move this file away, then restart the daemon",
	"%s 权限是 %04o，应为 0600":     "%s has mode %04o, should be 0600",
	"chmod 600 %s，或重启守护进程":    "Run chmod 600 %s, or restart the daemon",
	"守护进程在跑":                  "Daemon status",
	"hub 连接":                  "Hub connection",
	"守护进程没在跑":                 "the daemon isn't running",
	"未暂停":                     "not paused",
	"已暂停":                     "paused",
	"未知":                      "unknown",
	"规则 %d 条，待决 %d，%s，hub %s": "%d rules, %d pending, %s, hub %s",
	"已连上 %s":                  "connected to %s",
	"正在重连 %s":                 "reconnecting to %s",
	"看 agent.log 里 hub 连接断开的原因；网络不通或 hub 没起来时都会这样":                              "Check agent.log for why the hub connection dropped. This happens when the network is down or the hub isn't up",
	"%s 连续以 401 拒绝本机密钥（机器被吊销或密钥无效），守护进程已停止重连":                                   "%s keeps rejecting this machine's key with 401 (the machine was revoked or the key is invalid), so the daemon stopped reconnecting",
	"railse agent enroll --hub … --token … --force 重新接入后重启守护进程":                 "Enroll again with railse agent enroll --hub … --token … --force, then restart the daemon",
	"未接入 hub，引擎定不了的请求会交回终端":                                                     "not enrolled with a hub, so requests the rules can't decide are handed back to the terminal",
	"要在手机上审批就接入：railse agent enroll --hub https://… --token …":                  "To approve on your phone, enroll: railse agent enroll --hub https://… --token …",
	"守护进程没接 hub，但 machine.json 已在：接入后还没重启守护进程（它只在启动时读 machine.json）":            "the daemon isn't connected to a hub, but machine.json exists: the daemon wasn't restarted after enrolling (it only reads machine.json at startup)",
	"接入后还没重启守护进程：托管的用 railse setup autostart%s 重装，前台跑的停掉后重新 railse agent run%s": "Restart the daemon after enrolling: if it runs as a service, reinstall it with railse setup autostart%s; if it runs in the foreground, stop it and run railse agent run%s again",
	"守护进程没有报告 hub 状态（%q），可能是升级前启动的旧进程":                                          "the daemon didn't report a hub state (%q); it may be an old process started before an upgrade",
	"重启守护进程：托管的重跑一次 railse setup autostart，前台跑的停掉后重新 railse agent run":          "Restart the daemon: if it runs as a service, rerun railse setup autostart; if it runs in the foreground, stop it and run railse agent run again",
	"不支持 %s 的开机自启": "autostart isn't supported on %s",
	"没有托管文件 %s":    "no service file %s",
	" 或 ":          " or ",
	"railse setup autostart%s：开机自启、退出后自动拉起": "Run railse setup autostart%s (starts at boot, restarts on exit)",

	// internal/doctor/hooks.go
	"Codex 钩子":       "Codex hooks",
	"Claude Code 钩子": "Claude Code hooks",
	"Gemini 钩子":      "Gemini hooks",
	"；":              "; ",
	"%s：%s":          "%s: %s",
	"%s；%s":          "%s — %s",
	"；守护进程确实在 %s 的话，railse doctor 加 --dir %s":      "; if the daemon really is in %s, add --dir %s to railse doctor",
	"%s 也装了相同的钩子（Claude Code 对相同的处理器只跑一次），可以删掉 %s": "%s has the same hooks too (Claude Code runs identical handlers once); you can delete %s",
	"取不到家目录（$HOME 没设），也没有指定项目目录（--project），跳过":     "home directory not found ($HOME isn't set) and no project directory given (--project), skipped",
	"%s 里 Railse 的条目（用户级已对所有项目生效）":                 "the Railse entries in %s (the user-level hooks already apply to all projects)",
	"；项目级 %s 也装了，钩子会触发两次":                          "; the project-level %s has them too, so the hooks fire twice",
	"删掉 %s 里 Railse 的钩子条目（用户级已对所有项目生效）":            "Delete the Railse hook entries in %s (the user-level hooks already apply to all projects)",
	"；用户级 %s 也没装":                                      "; the user-level %s doesn't have them either",
	"没装钩子（看过 %s）":                                      "no hooks installed (looked in %s)",
	"运行 %s 迁到 settings.local.json":                     "Run %s to move them to settings.local.json",
	"；钩子还在 .claude/settings.json（旧位置）":                 "; the hooks are still in .claude/settings.json (the old location)",
	"钩子还在 .claude/settings.json（旧位置），且不全：%s":           "the hooks are still in .claude/settings.json (the old location) and incomplete: %s",
	"钩子还在 .claude/settings.json（旧位置）":                  "the hooks are still in .claude/settings.json (the old location)",
	"%s 里 Railse 的条目（运行 %s 会迁走）":                       "the Railse entries in %s (running %s moves them out)",
	"；settings.json 与 settings.local.json 都装了，钩子会触发两次": "; both settings.json and settings.local.json have them, so the hooks fire twice",
	"（同一条命令也会把 settings.json 里的迁走）":                    " (the same command also moves the ones in settings.json out)",
	"%s 不存在，没装钩子":                                      "%s doesn't exist, no hooks installed",
	"确认用跑 CLI 的同一个用户运行 doctor":                         "Run doctor as the same user that runs the CLI",
	"修好 JSON 后再 %s":                                    "Fix the JSON, then run %s",
	"%s（超时 %d，要 ≥ %d）":                                 "%s (timeout %d, needs ≥ %d)",
	"缺 PreToolUse：Claude Code 的选择题进不了手机":               "missing PreToolUse: Claude Code's questions can't reach your phone",
	"缺 %s":                                                       "missing %s",
	"超时不够：%s":                                                    "timeout too short: %s",
	"换过安装路径就重新 %s":                                               "If you moved the install, run %s again",
	"钩子指向 %s，当前程序是 %s":                                           "the hooks point to %s, but this program is %s",
	"钩子连的是 %s，守护进程在 %s":                                          "the hooks connect to %s, but the daemon is at %s",
	"运行 %s 重装钩子，让它连 %s 里的守护进程":                                   "Run %s to reinstall the hooks so they connect to the daemon in %s",
	"运行 %s（会保留已有的 notify）":                                       "Run %s (it keeps any existing notify)",
	"%s 不存在，Codex 的完成与提问事件不会进收件箱":                                "%s doesn't exist, so Codex's finished and question events won't reach the inbox",
	"%s 读不到（%s），Codex 的完成与提问事件不会进收件箱":                            "%s can't be read (%s), so Codex's finished and question events won't reach the inbox",
	"确认用跑 Codex 的同一个用户运行 doctor":                                 "Run doctor as the same user that runs Codex",
	"%s 看不懂：%s":                                                  "can't parse %s: %s",
	"手动改好这一处，再运行 %s":                                             "Fix that part by hand, then run %s",
	"%s 里的 notify 写在了 %s 下面，Codex 读不到":                           "the notify in %s is under %s, where Codex can't see it",
	"运行 %s（在文件开头写顶层的 notify），再删掉 %s 下面那个":                        "Run %s (it writes a top-level notify at the start of the file), then delete the one under %s",
	"%s 里没有 notify，Codex 的完成与提问事件不会进收件箱":                         "%s has no notify, so Codex's finished and question events won't reach the inbox",
	"notify 连的是 %s，守护进程在 %s":                                     "notify connects to %s, but the daemon is at %s",
	"运行 %s 重写 notify，让它连 %s 里的守护进程":                              "Run %s to rewrite notify so it connects to the daemon in %s",
	"%s 的 notify 不调 railse（%s），Codex 的完成与提问事件不会进收件箱":             "the notify in %s doesn't call railse (%s), so Codex's finished and question events won't reach the inbox",
	"运行 %s 重新生成":                                                 "Run %s to regenerate it",
	"%s 的 notify 指向 %s，它不存在或读不到":                                 "the notify in %s points to %s, which doesn't exist or can't be read",
	"%s 的 notify 指向 %s，它不可执行":                                    "the notify in %s points to %s, which isn't executable",
	"运行 %s：会把这个脚本保留为前一跳，再串上 railse":                              "Run %s: it keeps this script as the previous step and chains railse after it",
	"%s 里没有 railse 的 hook codex-notify 那一行，Codex 的完成与提问事件不会进收件箱": "%s has no railse hook codex-notify line, so Codex's finished and question events won't reach the inbox",
	"notify 脚本里的 railse 连的是 %s，守护进程在 %s":                         "railse in the notify script connects to %s, but the daemon is at %s",
	"运行 %s 重新生成脚本，让它连 %s 里的守护进程":                                 "Run %s to regenerate the script so it connects to the daemon in %s",
	"%s（notify 经 %s）":                                            "%s (notify via %s)",

	// internal/doctor/cli.go
	"Claude Code 版本": "Claude Code version",
	"Codex CLI 版本":   "Codex CLI version",
	"Gemini CLI 版本":  "Gemini CLI version",
	"PATH 里没有 %s":    "%s isn't in PATH",
	"sudo 下不跑 CLI，免得在用户目录留下 root 属主的文件":      "CLIs aren't run under sudo, so no root-owned files end up in your home directory",
	"用平时的用户身份重跑 railse doctor":               "Rerun railse doctor as your normal user",
	"%s --version 失败：%v":                     "%s --version failed: %v",
	"；输出：%s":                                 "; output: %s",
	"在终端里手动跑一次 %s --version 确认它能用":           "Run %s --version in a terminal to check that it works",
	"%s --version 没有给出版本号":                   "%s --version printed no version number",
	"在终端里手动跑一次 %s --version 确认版本":            "Run %s --version in a terminal to check the version",
	"当前 %s，低于 %s：钩子在 %s 之前无法在审批前拦截（确认之后才触发）": "%s is older than %s: before %s, hooks can't step in before approval (they fire after you confirm)",
	"升级 Gemini CLI 到 %s 或更新":                 "Upgrade Gemini CLI to %s or later",
	"钩子格式按 %s 录制，当前 %s 未验证":                  "the hook format was recorded with %s; %s isn't verified",
	"试一次需要审批的操作，确认请求经过 Railse；不行就换回 %s 并反馈":  "Try an action that needs approval and check that the request goes through Railse. If it doesn't, go back to %s and report it",

	// internal/doctor/hub.go
	"hub 可达":                   "Hub reachability",
	"没有 machine.json（未接入 hub）": "no machine.json (not enrolled with a hub)",
	"railse agent enroll --hub … --token … --force 重新接入": "Enroll again with railse agent enroll --hub … --token … --force",
	"确认 hub 在跑、地址与端口对、这台机器访问得到它（证书错误时看 hub 的 TLS 配置）":    "Check that the hub is running, the address and port are right, and this machine can reach it (for certificate errors, check the hub's TLS setup)",
	"%s 回 %d": "%s returned %d",
	"hub 太旧（没有 /api/version）、地址指向了别的服务，或被重定向（hub_url 应写最终地址）；升级 hub 或 railse agent enroll --force 改地址": "The hub is too old (no /api/version), the address points to another service, or it was redirected (hub_url should be the final address). Upgrade the hub, or change the address with railse agent enroll --force",
	"%s 回的不是 hub 的版本信息":                "%s didn't return hub version info",
	"确认 hub_url 指向 railse hub，而不是别的服务": "Check that hub_url points to a railse hub, not another service",
	"hub 协议 %d，本机 %d（hub 版本 %s，本机 %s）": "hub protocol %d, this machine %d (hub version %s, this machine %s)",
	"请升级较旧的一方，让 hub 与本机是同一版本":          "Upgrade whichever side is older so the hub and this machine run the same version",
	"%s，hub 版本 %s":       "%s, hub version %s",
	"hub 数据目录":           "Hub data directory",
	"hub 数据库":            "Hub database",
	"网页密码":               "Web password",
	"推送密钥":               "Push keys",
	"ACME 证书目录":          "ACME certificate directory",
	"本机 hub 对外地址":        "Local hub public address",
	"hub 开机自启":           "Hub autostart",
	"这台机器没有 hub 数据目录":    "this machine has no hub data directory",
	"没有 hub 数据目录 %s":     "no hub data directory at %s",
	"hub 数据目录 %s %s":     "hub data directory %s %s",
	"railse hub run 会建好": "Run railse hub run to create it",
	"--hub-dir 要指向 hub 的数据目录（railse hub run --dir 用的那个）": "Point --hub-dir at the hub's data directory (the one railse hub run --dir uses)",
	"不是目录": "isn't a directory",
	"确认 --hub-dir 写对了（railse hub run --dir 用的那个目录）；这台机器不跑 hub 就去掉 --hub-dir": "Check that --hub-dir is right (the directory railse hub run --dir uses). If this machine doesn't run a hub, drop --hub-dir",
	"不存在":       "doesn't exist",
	"%s 读不到：%s": "%s can't be read: %s",
	"确认用跑 hub 的同一个用户运行 doctor": "Run doctor as the same user that runs the hub",
	"读不到": "can't be read",
	"WAL 里有未检查点的数据而 -shm 不在": "the WAL has uncheckpointed data and -shm is missing",
	"hub.db 损坏或不是 hub 的数据库：从备份恢复，或移走它让 railse hub run 新建（机器要重新接入）":                 "hub.db is damaged or isn't a hub database: restore it from a backup, or move it away so railse hub run creates a new one (machines have to enroll again)",
	"hub 正持着库的锁（关库或崩溃恢复中），稍后重跑 railse doctor":                                      "The hub is holding the database lock (closing it or recovering from a crash). Rerun railse doctor in a moment",
	"暂时读不了：确认用跑 hub 的同一个用户运行 doctor，hub.db 与 -wal、-shm 的属主和权限对，稍后重跑 railse doctor": "Can't read it right now. Run doctor as the same user that runs the hub, check the owner and permissions of hub.db, -wal and -shm, then rerun railse doctor in a moment",
	"库里没有 hub 的表（不是 hub 建的库，或建库中途断了）：railse hub run 会补建；不是 hub 的数据目录就换对 --hub-dir": "The database has no hub tables (the hub didn't create it, or creating it was cut short). railse hub run adds them; if this isn't the hub's data directory, fix --hub-dir",
	"暂时读不了，稍后重跑 railse doctor；一直这样就带上这条报错反馈":                                       "Can't read it right now. Rerun railse doctor in a moment; if it keeps happening, report it with this error",
	"railse hub run 会建好数据库": "Run railse hub run to create the database",
	"没有数据库":                 "no database",
	"hub 上次没正常退出且 -shm 不在，WAL 里有未检查点的数据，doctor 只读不做恢复": "the hub didn't shut down cleanly last time and -shm is missing, so the WAL has uncheckpointed data; doctor is read-only and doesn't recover it",
	"启动一次 railse hub run（会自动恢复）后重跑 railse doctor":      "Run railse hub run once (it recovers automatically), then rerun railse doctor",
	"WAL 里有未恢复的数据，不按旧数据下结论":                            "the WAL has unrecovered data, so nothing is concluded from stale data",
	"读不到 schema_version：%s":                            "can't read schema_version: %s",
	"数据库读不了":                                           "the database can't be read",
	"库版本 %d 比程序（%d）新，hub 起不来":                          "database version %d is newer than the program (%d), so the hub won't start",
	"升级 railse 到建这个库的版本或更新":                            "Upgrade railse to the version that created this database, or later",
	"库版本 %d，程序 %d":                                     "database version %d, program %d",
	"下次 railse hub run 会自动升级（建议先备份 hub.db）":            "The next railse hub run upgrades it automatically (back up hub.db first)",
	"%s，版本 %d":         "%s, version %d",
	"读 settings 出错：%s": "error reading settings: %s",
	"已设置":              "set",
	"还没设密码，网页登录会得到 503":                                          "no password yet, so web logins get a 503",
	"VAPID 密钥还没生成":                                               "VAPID keys not generated yet",
	"首次 railse hub run 会生成；也可以 railse hub vapid":                 "The first railse hub run generates them, or run railse hub vapid",
	"VAPID 密钥损坏，hub 起不来":                                         "VAPID keys are damaged, so the hub won't start",
	"railse hub vapid --rotate 重建（所有手机要重新开启通知）":                  "Rebuild them with railse hub vapid --rotate (every phone has to turn notifications on again)",
	"VAPID 密钥可用":                                                 "VAPID keys are usable",
	"没有 acme 目录（没用 --acme-domain 自动证书）":                          "no acme directory (automatic certificates with --acme-domain aren't used)",
	"railse hub run --acme-domain … 会建好":                         "Run railse hub run --acme-domain … to create it",
	"里面有私钥文件不是 0600：%s":                                          "private key files in it aren't 0600: %s",
	"不是带主机名的 https:// 地址":                                        "not an https:// address with a host name",
	"带了用户名或密码":                                                   "has a user name or password",
	"带了 ? 查询或 # 片段":                                              "has a ? query or # fragment",
	"不是 https:// 地址":                                             "not an https:// address",
	"带了路径，只能是 https://主机[:端口]":                                   "has a path; only https://host[:port] is allowed",
	"重跑 %s 重写它":                                                  "Rerun %s to rewrite it",
	"（用的是自己的隧道，再带上 --public-url <对外地址>）":                         " (you use your own tunnel, so also pass --public-url <public address>)",
	"没有 local-hub.json（没用 setup all --local-hub 装过本机 hub，或还没装完）": "no local-hub.json (no local hub was installed with setup all --local-hub, or it didn't finish)",
	"%s 不是合法 JSON":                                               "%s isn't valid JSON",
	"%s 里 port %d 不在 1..65535":                                   "%s: port %d is not in 1..65535",
	"%s 里 public_url 不合规：%s":                                     "%s: public_url is invalid: %s",
	"sudo 下不跑 tailscale":                                         "tailscale isn't run under sudo",
	"找不到 tailscale（%s 由 Tailscale Funnel 提供）":                    "tailscale not found (%s is served by Tailscale Funnel)",
	"装好并登录 Tailscale（https://tailscale.com/download）后重跑 railse doctor；不再用 Tailscale 就带 --public-url 重跑 %s": "Install Tailscale and log in (https://tailscale.com/download), then rerun railse doctor. If you no longer use Tailscale, rerun %s with --public-url",
	"%s serve status --json 失败：%v":                             "%s serve status --json failed: %v",
	"确认 Tailscale 在运行且已登录（tailscale status），再重跑 railse doctor": "Check that Tailscale is running and logged in (tailscale status), then rerun railse doctor",
	"在终端里跑一次 tailscale serve status --json 看看输出":               "Run tailscale serve status --json in a terminal and look at the output",
	"Funnel 没把 %s 转到 127.0.0.1:%d":                             "Funnel doesn't forward %s to 127.0.0.1:%d",
	"tailscale funnel --bg %d，或重跑 %s":                          "Run tailscale funnel --bg %d, or rerun %s",
	"从本机访问不到对外地址：可能是本机代理拦截了（见 setup 结尾的提醒），用手机 4G 打开验证；Funnel 入口 TLS 握手失败时先关掉代理再试": "This machine can't reach the public address. A proxy on this machine may be blocking it (see the reminder at the end of setup); check by opening it on your phone over cellular. If the TLS handshake at the Funnel entry fails, turn the proxy off and retry",
	"从本机访问 %s 失败：%s": "can't reach %s from this machine: %s",
	"对外地址没转到本机 hub：确认它转到 127.0.0.1:%d（那里的 hub 看 hub.reachable）；也可能是本机代理拦截了，用手机 4G 打开验证": "The public address doesn't lead to the local hub. Check that it forwards to 127.0.0.1:%d (hub.reachable covers the hub there). A proxy on this machine may also be blocking it; check by opening it on your phone over cellular",
	"（从本机经 tailnet 访问到，没经过 Funnel 的公网入口；公网上打不打得开用手机 4G 验证）":                             " (reached from this machine over the tailnet, not through Funnel's public entry; check on your phone over cellular whether it opens from the internet)",

	// cmd/railse/autostart.go、autostart_other.go
	"Railse agent（审批守护进程）": "Railse agent (approval daemon)",
	"Railse hub（审批中心）":     "Railse hub (approval hub)",
	"托管 hub 而不是守护进程":       "manage the hub instead of the daemon",
	"停止并移除托管（数据目录保留）":      "stop and remove the managed service (the data directory is kept)",
	"只查看是否在跑":              "only show whether it is running",
	"数据目录，默认 $RAILSE_HOME 或 ~/.railse；--hub 时 $RAILSE_HUB_HOME 或 ~/.railse-hub；--system 时 /var/lib/railse-hub": "data directory, default $RAILSE_HOME or ~/.railse; with --hub, $RAILSE_HUB_HOME or ~/.railse-hub; with --system, /var/lib/railse-hub",
	"Linux：hub 用系统单元（须 root，hub 以 --user 运行）":                                                                  "Linux: run the hub as a system unit (needs root; the hub runs as --user)",
	"系统单元以哪个用户运行，默认 $SUDO_USER；不能是 root":                                                                       "user the system unit runs as, default $SUDO_USER; can't be root",
	"只打印要写的文件与要跑的命令，不写不跑":                                                                                      "only print the files it would write and the commands it would run; write and run nothing",
	"多余的参数 %q（给 hub run 的参数放在 -- 之后）":                                                                          "Unexpected argument %q (put arguments for hub run after --)",
	"开机自启只支持 macOS（launchd）与 Linux（systemd）":                                                                   "Autostart only works on macOS (launchd) and Linux (systemd)",
	"--status 与 --uninstall 只能选一个":                                                                             "Use only one of --status and --uninstall",
	"-- 之后的额外参数只给 hub（--hub）；守护进程不收":                                                                           "Extra arguments after -- are only for the hub (--hub); the daemon takes none",
	"--system 只给 hub（--hub）用；守护进程用用户级托管：railse setup autostart":                                                "--system is only for the hub (--hub); the daemon uses the user-level service: railse setup autostart",
	"--system 只在 Linux 上有效":                                                                                    "--system only works on Linux",
	"--user 只和 --system 一起用":                                                                                   "--user only works with --system",
	"数据目录用 setup autostart 自己的 --dir 指定，不要放在 -- 之后（托管文件里的环境变量、日志与可写目录都按它算）":                                    "Set the data directory with setup autostart's own --dir, not after -- (the environment variable, log and writable directory in the service file all follow it)",
	"路径与参数里不能有换行等控制字符（包括 RAILSE_HOME、RAILSE_HUB_HOME 给的默认值）: %q":                                               "Paths and arguments can't contain newlines or other control characters (this includes the defaults from RAILSE_HOME and RAILSE_HUB_HOME): %q",
	"macOS 上不要用 sudo 跑：launchd 用户代理装在当前登录用户下，直接 railse setup autostart":                                        "Don't run this with sudo on macOS: the launchd user agent is installed for the logged-in user. Just run railse setup autostart",
	"不要以 root 管用户级托管：守护进程与 hub 都以普通用户运行，请用普通用户跑本命令；hub 要直出 443/80 用 %s --user 名字（只给绑定特权端口的权限）\n": "Don't manage the user-level service as root: the daemon and the hub both run as a normal user, so run this command as a normal user. To serve the hub directly on 443/80, use %s --user NAME (it only grants the right to bind privileged ports)\n",
	"--system 要写 /etc/systemd/system，需要 root：%s …\n": "--system writes to /etc/systemd/system and needs root: %s ...\n",
	"警告：%v\n": "Warning: %v\n",
	"警告：托管文件会指向临时路径 %s，之后它可能就没了（go run 的产物？）；用 --bin 指定装好的 railse\n":                                                                                  "Warning: the service file would point at the temporary path %s, which may disappear later (a go run build?). Use --bin to point at an installed railse\n",
	"注意：Tailscale Funnel 可能还开着（tailscale funnel status 看），%s 可能仍转到 127.0.0.1:%d；不用就 tailscale funnel --https=443 off（会关掉 443 上的整个 Funnel，有别的配置先核对）\n": "Note: Tailscale Funnel may still be on (check with tailscale funnel status), so %s may still forward to 127.0.0.1:%d. If you don't need it, run tailscale funnel --https=443 off (this turns off all of Funnel on 443, so check any other setup first)\n",
	`
!!! 警告：Linux 的用户单元以你的身份运行，没有绑定 1024 以下端口的权限，hub 绑不了 %s，会起不来并被 systemd 反复拉起。
    照样装了；要直出 443/80 改用系统单元（只多给 CAP_NET_BIND_SERVICE，hub 不以 root 运行），先卸掉这个用户单元：
      railse setup autostart --hub --uninstall
      %s -- %s
    用反向代理时改成 --listen 127.0.0.1:8787（不要 --acme-domain），见 docs/setup.md。

`: `
!!! Warning: a Linux user unit runs as you and has no permission to bind ports below 1024, so the hub can't bind %s. It will fail to start and systemd will keep restarting it.
    It was installed anyway. To serve directly on 443/80, use a system unit instead (it only adds CAP_NET_BIND_SERVICE; the hub does not run as root). First remove this user unit:
      railse setup autostart --hub --uninstall
      %s -- %s
    Behind a reverse proxy, use --listen 127.0.0.1:8787 instead (and no --acme-domain); see docs/setup.md.

`,
	"--system 要指定 hub 的运行用户：--user 名字（默认取 $SUDO_USER，现在没有）": "--system needs the user the hub runs as: --user NAME (default $SUDO_USER, which is not set)",
	"用户名 %q 不合法": "Invalid user name %q",
	"用户名 %s 全是数字：systemd 会把 User=%s 当成 uid，请用用户名": "The user name %s is all digits: systemd would read User=%s as a uid. Use the user name",
	"找不到用户 %s: %v":           "Can't find user %s: %v",
	"用户 %s 的 uid %q 不是合法的数字": "User %s has an invalid uid %q",
	"hub 不以 root 运行（用户 %s 的 uid 是 0）：用 --user 指定一个普通用户":           "The hub doesn't run as root (user %s has uid 0). Use --user to name a normal user",
	"用户 %s 的 gid %q 不是合法的数字":                                      "User %s has an invalid gid %q",
	"railse 可执行文件 %s 不存在：用 --bin 指定装好的 railse":                    "The railse executable %s doesn't exist. Use --bin to point at an installed railse",
	"railse 可执行文件 %s 用不了：%v":                                      "The railse executable %s can't be used: %v",
	"%s 不是可执行文件：用 --bin 指定装好的 railse":                             "%s is not an executable file. Use --bin to point at an installed railse",
	"会建数据目录 %s（0700）\n":                                           "Would create the data directory %s (0700)\n",
	"会准备数据目录 %s（已有的须已属于 %s；没有的新建为 0700 并交给 %s）\n":                 "Would prepare the data directory %s (an existing one must already belong to %s; a missing one is created as 0700 and handed to %s)\n",
	"（--dry-run：以上没有写文件，也没有跑命令）":                                  "(--dry-run: nothing above was written or run)",
	"登录后由 launchd 启动、退出后自动拉起":                                     "started by launchd at login and restarted if it exits",
	"开机由 systemd 启动、以 %s 运行、退出后自动拉起":                              "started by systemd at boot, runs as %s, and is restarted if it exits",
	"由 systemd 用户实例启动、退出后自动拉起":                                    "started by the systemd user instance and restarted if it exits",
	"已托管 %s：%s；日志：%s；查看状态：%s\n":                                   "Now managing %s: %s; log: %s; check status: %s\n",
	"提示：要在没人登录时也跑（开机即起、退出登录不停），执行一次 loginctl enable-linger $USER": "Tip: to keep it running when nobody is logged in (start at boot, survive logout), run loginctl enable-linger $USER once",
	"%s 是符号链接：--dir 要给真实的目录":                                      "%s is a symbolic link. Give --dir a real directory",
	"%s 已存在但不属于 %s，不替它 chown：换一个以 %s 身份建好的目录（sudo -u %s mkdir -p -m 700 <目录>）用 --dir 指向它，或不给 --dir 用默认的 %s": "%s already exists but doesn't belong to %s, so it won't be chowned. Create a directory as %s (sudo -u %s mkdir -p -m 700 <dir>) and point --dir at it, or leave out --dir to use the default %s",
	"不替你新建 %s：%v（运行用户可能在 root 建目录与 chown 之间把路径换掉）；先以 %s 身份建好再重跑：sudo -u %s mkdir -p -m 700 %s":              "Not creating %s for you: %v (the run-as user could swap the path between root creating the directory and the chown). Create it as %s first, then rerun: sudo -u %s mkdir -p -m 700 %s",
	"%s 不是刚建的目录（属主不对），不 chown": "%s is not the directory just created (wrong owner), so it won't be chowned",
	"%s 不属于 root": "%s isn't owned by root",
	"%s 别人也能写":    "%s is writable by others",
	"停止 %s 失败，托管文件 %s 没删：\n%v\n": `Stopping %s failed; the service file %s was not removed:
%v
`,
	"会删除 %s\n": "Would delete %s\n",
	"（--dry-run：以上没有删文件，也没有跑命令）":     "(--dry-run: no file above was deleted and no command was run)",
	"已停止并移除 %s 的托管（删除了 %s）；数据目录保留\n": "Stopped and removed the managed %s (deleted %s); the data directory is kept\n",
	"没有找到 %s：%s 本来就没托管\n":            "%s not found: %s wasn't being managed\n",
	"launchd 里没有加载 %s":               "%s is not loaded in launchd",
	"%s 已托管并在跑（%s）\n":                "%s is managed and running (%s)\n",
	"%s 没有托管（没有 %s）；托管：%s\n":         "%s is not managed (no %s); to manage it: %s\n",
	"%s 有托管文件 %s，但没在跑（%s）；日志：%s\n":   "%s has a service file %s but is not running (%s); log: %s\n",
	"开机自启只支持 macOS 与 Linux":          "Autostart only works on macOS and Linux",
	`用法:
  railse setup autostart [--hub] [--uninstall] [--status] [--dir 目录] [--bin 路径] [--system] [--user 名字] [--dry-run] [-- hub run 的额外参数]
    开机自启并在退出后自动拉起。默认托管守护进程（agent run）；--hub 托管 hub（hub run），-- 之后的参数原样追加到 hub run 后面，例如：
      railse setup autostart --hub -- --public-url https://hub.example.com
    macOS 写 ~/Library/LaunchAgents/dev.railse.<agent|hub>.plist（launchd 用户代理，日志在数据目录的 launchd.log）；
    Linux 写 ~/.config/systemd/user/railse-<agent|hub>.service（用户单元，日志：journalctl --user -u railse-<agent|hub>）。
    hub 要直出 443/80 时在 Linux 上用系统单元（只给绑定特权端口的权限，hub 不以 root 运行）：
      sudo "$(command -v railse)" setup autostart --hub --system [--user 名字] -- --acme-domain hub.example.com --acme-email me@example.com
      写 /etc/systemd/system/railse-hub.service，以 --user（默认 $SUDO_USER）运行，数据目录默认 /var/lib/railse-hub。
    --uninstall 停止并移除托管（数据目录保留）；--status 看是否在跑；--dry-run 只打印要写的文件与要跑的命令。`: `Usage:
  railse setup autostart [--hub] [--uninstall] [--status] [--dir DIR] [--bin PATH] [--system] [--user NAME] [--dry-run] [-- extra hub run args]
    Starts at login or boot and restarts after it exits. By default it manages the daemon (agent run); --hub manages the hub (hub run). Arguments after -- are appended to hub run as they are, for example:
      railse setup autostart --hub -- --public-url https://hub.example.com
    On macOS this writes ~/Library/LaunchAgents/dev.railse.<agent|hub>.plist (a launchd user agent; the log is launchd.log in the data directory).
    On Linux it writes ~/.config/systemd/user/railse-<agent|hub>.service (a user unit; logs: journalctl --user -u railse-<agent|hub>).
    To serve the hub directly on 443/80 on Linux, use a system unit (it only grants the right to bind privileged ports; the hub does not run as root):
      sudo "$(command -v railse)" setup autostart --hub --system [--user NAME] -- --acme-domain hub.example.com --acme-email me@example.com
      This writes /etc/systemd/system/railse-hub.service, runs as --user (default $SUDO_USER), and uses /var/lib/railse-hub as the data directory by default.
    --uninstall stops and removes the managed service (the data directory is kept); --status shows whether it is running; --dry-run only prints the files it would write and the commands it would run.`,

	// cmd/railse/agent.go（含 hub 与 setup 共用的校验报错）
	"未知的 agent 子命令 %q\n\n%s\n": `Unknown agent command %q

%s
`,
	"配置目录，默认 $RAILSE_HOME 或 ~/.railse": "daemon data directory, default $RAILSE_HOME or ~/.railse",
	"日志同时写到 stderr":                    "also write the log to stderr",
	"覆盖已有的 machine.json":               "overwrite an existing machine.json",
	"railse agent %s 不认 %s\n%s\n": `railse agent %s doesn't take %s
%s
`,
	"失败:": "Failed:",
	"本机已恢复，hub 仍暂停：自动审批仍未生效，需在 hub 上恢复":                              "This machine is resumed, but the hub is still paused: auto-approval is still off. Resume it on the hub",
	"hub: 未知（守护进程版本较旧？重启它）":                                          "hub: unknown (older daemon version? restart it)",
	"hub: disabled %s（本机密钥被拒，请 railse agent enroll --force 后重启守护进程）": "hub: disabled %s (this machine's key was rejected; run railse agent enroll --force, then restart the daemon)",
	"警告: ": "Warning: ",
	"%s 已存在，本机已接入过；要换 hub 或重新接入请加 --force": "%s already exists, so this machine is already enrolled. To switch hubs or enroll again, add --force",
	"连接 hub %s 失败: %w": "Can't connect to hub %s: %w",
	"读 hub %s 的答复: %w": "Can't read the reply from hub %s: %w",
	"hub 返回重定向（HTTP %d），不跟随：接入码与密钥只发给 --hub 指定的地址，请把 --hub 改成最终地址": "The hub returned a redirect (HTTP %d) and it was not followed: the enroll token and key are only sent to the address given in --hub. Change --hub to the final address",
	"hub 拒绝接入（HTTP %d）%s":             "The hub refused the enrollment (HTTP %d)%s",
	"hub 的答复不完整或不合规，没有写 machine.json": "The hub's reply was incomplete or invalid, so machine.json was not written",
	"写 %s: %w": "Can't write %s: %w",
	"已接入 hub %s，机器 id %s，名字 %s\n已写入 %s；重启 railse agent run 后生效\n": `Enrolled with hub %s. Machine id %s, name %s
Wrote %s; restart the daemon (railse agent run) for it to take effect
`,
	"注意：首次连上 hub 时本机 rules.yaml 会被 hub 规则覆盖（会自动备份），如有手写规则请先导入。\n  本机规则 %s，备份为同目录的 rules.local-<时间>.yaml；覆盖后其中手写的规则（例如 deny）不再生效\n  导入：POST %s/api/rules/import（整套替换 hub 的规则，见 docs/hub.md）\n": `Note: the first time this machine connects to the hub, its rules.yaml is overwritten by the hub's rules (with an automatic backup). Import any hand-written rules first.
  Local rules: %s, backed up as rules.local-<time>.yaml in the same directory; hand-written rules in it (deny, for example) stop applying after the overwrite
  Import: POST %s/api/rules/import (this replaces the hub's whole rule set; see docs/hub.md)
`,
	"必须是 http:// 或 https:// 开头、带主机名的地址": "must be an address that starts with http:// or https:// and has a host name",
	"不能带用户名或密码":                         "can't include a user name or password",
	"不能带 ? 查询或 # 片段":                    "can't include a ? query or a # fragment",
	"至多 %d 字节":                          "must be at most %d bytes",
	"必须是合法 UTF-8，且不能含控制字符或不可见的格式字符":     "must be valid UTF-8 and can't contain control or invisible formatting characters",
	"名字 %w（用 --name 另取一个）":              "The name %w (pick another with --name)",
	"<接入码>":                             "<enroll token>",
	"找不到家目录，拒绝启动：家目录为空会让 ~/ 开头的敏感路径规则失效": "Can't find your home directory, so it won't start: with an empty home directory, the sensitive-path rules that start with ~/ would not apply",
	"收紧日志文件权限: %w": "Can't tighten the log file permissions: %w",
	"读 machine.json 失败，拒绝启动（修好它、删掉它改回本地模式，或 railse agent enroll --force 重新接入）: %w": "Can't read machine.json, so it won't start (fix it, delete it to go back to local mode, or enroll again with railse agent enroll --force): %w",
	`用法:
  railse agent run [--dir 目录] [--foreground]   启动守护进程（前台运行；有 machine.json 时接 hub；开机自启用 railse setup autostart）
  railse agent status|pause|resume|reload [--dir 目录]
  railse agent enroll --hub URL --token 接入码 [--name 名字] [--dir 目录] [--force]   接入 hub，写 machine.json`: `Usage:
  railse agent run [--dir DIR] [--foreground]   start the daemon (runs in the foreground; connects to the hub when machine.json exists; for autostart use railse setup autostart)
  railse agent status|pause|resume|reload [--dir DIR]
  railse agent enroll --hub URL --token TOKEN [--name NAME] [--dir DIR] [--force]   enroll with a hub and write machine.json`,

	// cmd/railse/hub.go
	"警告：hub 正以 root 运行。hub 不需要 root：Linux 上用 %s 装成系统服务（以普通用户运行，只给绑定 443/80 的 CAP_NET_BIND_SERVICE）；macOS 绑定 443/80 不需要特权": "Warning: the hub is running as root. It doesn't need root: on Linux, install it as a system service with %s (it runs as a normal user and is only given CAP_NET_BIND_SERVICE to bind 443/80); macOS needs no privilege to bind 443/80",
	"可信代理地址为空":                    "the trusted proxy address is empty",
	"可信代理 %q 不是合法的 CIDR":          "trusted proxy %q is not a valid CIDR",
	"可信代理 %q 不是合法的 IP 或 CIDR":     "trusted proxy %q is not a valid IP or CIDR",
	"可信代理 %q 覆盖全部地址，太宽；请写具体的代理地址": "trusted proxy %q covers every address, which is too wide; give a specific proxy address",
	"未知的 hub 子命令 %q\n\n%s\n": `Unknown hub command %q

%s
`,
	"数据目录，默认 $RAILSE_HUB_HOME 或 ~/.railse-hub":                                           "data directory, default $RAILSE_HUB_HOME or ~/.railse-hub",
	"监听地址，默认 127.0.0.1:8787（给了 --acme-domain 时默认 :443）":                                  "listen address, default 127.0.0.1:8787 (:443 when --acme-domain is given)",
	"cookie 加 Secure（hub 在 TLS 反向代理后面而没设 --public-url 时打开；设了 https 的 --public-url 已自动加）": "add Secure to cookies (turn on when the hub is behind a TLS reverse proxy and --public-url is not set; an https --public-url already adds it)",
	"接入码有效期": "how long the enroll token is valid",
	"hub 对外的 https 源，如 https://hub.example.com；不设则停用推送":  "the hub's public https origin, e.g. https://hub.example.com; push is disabled if not set",
	"换新的 VAPID 密钥并删除全部推送订阅":                              "switch to a new VAPID key and delete all push subscriptions",
	"hub 自己向 Let's Encrypt 申请证书并直出 TLS 的域名（要 443/80 端口）": "domain the hub gets a Let's Encrypt certificate for and serves TLS on directly (needs ports 443/80)",
	"给 Let's Encrypt 的联系邮箱（可选）":                          "contact email for Let's Encrypt (optional)",
	"HTTP-01 挑战与 http→https 跳转的监听地址；none 不监听":            "listen address for the HTTP-01 challenge and the http-to-https redirect; none does not listen",
	"可信反向代理的地址（CIDR 或单个 IP），可重复或逗号分隔":                    "address of a trusted reverse proxy (CIDR or a single IP); repeat the flag or separate with commas",
	"railse hub %s 不认 %s\n%s\n": `railse hub %s doesn't take %s
%s
`,
	"--ttl 应在 0 到 %s 之间（不含 0），得到 %s\n":       "--ttl must be between 0 and %s (not including 0), got %s\n",
	"%s 要和 --acme-domain 一起用\n":              "%s only works with --acme-domain\n",
	"密码已设置（已登录的网页需要重新登录）":                    "Password set (web sessions that are already logged in have to log in again)",
	"删除了 %d 条推送订阅：各设备重新登录后，要在网页设置里重新开启通知。\n": "Deleted %d push subscriptions: after logging in again, each device has to turn notifications back on in the web settings.\n",
	"接入码: %s\n有效期: %s（只能用一次）\n在要接入的机器上、项目目录里运行（接入、装钩子、开机自启、体检一次做完）:\n  railse setup all --hub https://<hub 地址> --token %s\n只接入（钩子与开机自启自己装）:\n  railse agent enroll --hub https://<hub 地址> --token %s\n": `Enroll token: %s
Valid for: %s (one use only)
On the machine you want to enroll, run this in the project directory (it enrolls, installs hooks and autostart, and runs the health check):
  railse setup all --hub https://<hub address> --token %s
To only enroll (you install hooks and autostart yourself):
  railse agent enroll --hub https://<hub address> --token %s
`,
	"令牌: %s\n来源调用 POST /api/webhook/approvals 时带 Authorization: Bearer <令牌>；hub 只存哈希，丢了就重新生成（旧令牌随之失效）。\n": `Token: %s
When a source calls POST /api/webhook/approvals, it sends Authorization: Bearer <token>. The hub stores only a hash; if you lose it, create a new one (the old token stops working).
`,
	"公钥: %s\n": "Public key: %s\n",
	"公钥: %s\n已换新密钥，删除了 %d 条推送订阅；所有手机要在网页设置里重新开启通知。在跑的 hub 不用重启，下一次推送起用新密钥。\n": `Public key: %s
Switched to a new key and deleted %d push subscriptions. Every phone has to turn notifications on again in the web settings. A running hub needs no restart; it uses the new key from the next push.
`,
	"输入新密码后回车（屏幕会回显；要避免回显可用 read -s 读进变量再通过管道传入）: ": "Type the new password and press Enter (the screen will show it; to avoid that, read it with read -s into a variable and pipe it in): ",
	"没有读到密码":     "No password was read",
	"读密码: %w":    "Can't read the password: %w",
	"密码至多 %d 字节": "The password can be at most %d bytes",
	`用法:
  railse hub run [--dir 目录] [--listen 地址] [--secure-cookies] [--trusted-proxy 地址] [--public-url https://hub.example.com] [--foreground]
                                              启动 hub（前台运行）
  railse hub run --acme-domain hub.example.com [--acme-email me@example.com] [--acme-http :80] [其余标志同上]
                                              直出 TLS（Let's Encrypt 自动签发与续期），--listen 默认 :443
  railse hub password [--dir 目录]            从标准输入读一行设为网页登录密码（至少 8 个字符）
  railse hub token [--dir 目录] [--ttl 10m]   生成一次性接入码，给 railse setup all（或 railse agent enroll）用
  railse hub webhook-token [--dir 目录]       生成 webhook 来源令牌（明文只显示这一次）
  railse hub vapid [--dir 目录] [--rotate]    打印推送公钥；--rotate 换新密钥并删除全部推送订阅（所有手机要重新开启通知）
  目录默认 $RAILSE_HUB_HOME，否则 ~/.railse-hub
  --public-url 是 hub 对外的 https 源（不带路径），设了才启用手机推送（Web Push）；域名定下后不要再换
  --trusted-proxy 可重复或逗号分隔（CIDR 或单个 IP）：只有来自这些地址的连接才看 X-Forwarded-For。
  反向代理须追加 X-Forwarded-For（nginx: proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for）
  并传原 Host（proxy_set_header Host $host）
  --acme-domain：域名先解析到这台机器，443 与 80 对公网开放；对外源默认 https://<域名>（--public-url 的主机名须与它一致），
    cookie 一律 Secure；账户与证书私钥明文存在数据目录的 acme/（0700）。--acme-http 是 HTTP-01 挑战与 http→https 跳转的
    监听地址，none 不监听（只靠 443 上的 TLS-ALPN-01 签发）。Linux 上监听 443/80 需要 CAP_NET_BIND_SERVICE
    （sudo "$(command -v railse)" setup autostart --hub --system 会加）；macOS 不需要特权。hub 不要以 root 运行`: `Usage:
  railse hub run [--dir DIR] [--listen ADDR] [--secure-cookies] [--trusted-proxy ADDR] [--public-url https://hub.example.com] [--foreground]
                                              start the hub (runs in the foreground)
  railse hub run --acme-domain hub.example.com [--acme-email me@example.com] [--acme-http :80] [other flags as above]
                                              serve TLS directly (Let's Encrypt issues and renews the certificate); --listen defaults to :443
  railse hub password [--dir DIR]             read one line from stdin and set it as the web login password (at least 8 characters)
  railse hub token [--dir DIR] [--ttl 10m]    create a one-time enroll token, for railse setup all (or railse agent enroll)
  railse hub webhook-token [--dir DIR]        create a webhook source token (the plain text is shown only once)
  railse hub vapid [--dir DIR] [--rotate]     print the push public key; --rotate switches to a new key and deletes all push subscriptions (every phone has to turn notifications on again)
  DIR defaults to $RAILSE_HUB_HOME, otherwise ~/.railse-hub
  --public-url is the hub's public https origin (no path). Phone push (Web Push) is only enabled when it is set; don't change the domain once it is chosen
  --trusted-proxy can be repeated or comma-separated (CIDR or a single IP): only connections from these addresses are trusted for X-Forwarded-For.
  The reverse proxy must append X-Forwarded-For (nginx: proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for)
  and pass the original Host (proxy_set_header Host $host)
  --acme-domain: point the domain at this machine first and open 443 and 80 to the internet; the public origin defaults to https://<domain> (the host name of --public-url must match it),
    cookies are always Secure; the account and certificate private key are stored in plain text in acme/ in the data directory (0700). --acme-http is the
    listen address for the HTTP-01 challenge and the http-to-https redirect; none does not listen (issuance then relies on TLS-ALPN-01 on 443 only). On Linux, listening on 443/80 needs CAP_NET_BIND_SERVICE
    (sudo "$(command -v railse)" setup autostart --hub --system adds it); macOS needs no privilege. Don't run the hub as root`,

	// cmd/railse/policy.go
	"未知的 policy 子命令 %q\n\n%s\n": `Unknown policy command %q

%s
`,
	"规则 YAML 文件，不给则用内置规则包": "rules YAML file, default the built-in rules",
	"命令的工作目录":              "working directory of the command",
	"项目根目录，不给则用 cwd":       "project root, default the working directory",
	"载荷类型":                 "payload type",
	"mcp 的参数应为 服务/工具，得到 %q\n\n%s\n": `The mcp argument must be server/tool, got %q

%s
`,
	"未知类型 %q\n\n%s\n": `Unknown type %q

%s
`,
	"用法: railse policy check <规则文件>": "Usage: railse policy check <rules file>",
	"无效:":         "Invalid:",
	"有效，%d 条规则\n": "Valid: %d rules\n",
	`用法:
  railse policy eval [--rules 文件] [--cwd 目录] [--project 目录] [--type shell|file_write|mcp|network] <命令|路径|服务/工具|URL>
  railse policy check <规则文件>`: `Usage:
  railse policy eval [--rules FILE] [--cwd DIR] [--project DIR] [--type shell|file_write|mcp|network] <command|path|server/tool|URL>
  railse policy check <rules file>`,

	// cmd/railse/localhub.go（与 internal/hub/auth.go 的 SetPassword 同步）
	"密码至少 8 个字符": "The password must be at least 8 characters",
}
