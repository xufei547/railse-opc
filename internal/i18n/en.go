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
	"未知子命令 %q\n\n%s\n": "Unknown command %q\n\n%s\n",
}
