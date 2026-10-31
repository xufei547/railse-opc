// 英文表：键是代码里的中文原文（含 {name} 占位与标点），值是英文。语气与 README.en 一致：短句、说人话、不吹。
// 术语表（与 internal/i18n/en.go 相同）：批准 approve · 拒绝 deny · 记住 remember · 收件箱 inbox · hub hub ·
// 守护进程 daemon · 钩子 hook · 规则 rule · 项目 project · 机器 machine · 接入码 enroll token · 体检 health check ·
// 暂停自动审批 auto-approval paused · 交回终端 handed back to the terminal · 等你回答 waiting for your answer ·
// 自己填 Your own answer · 让它在终端问我 Ask me in the terminal instead
export const en: Record<string, string> = {
  正在连接hub: 'Connecting to the hub…',
}
