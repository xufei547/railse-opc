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
}
