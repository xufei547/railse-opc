// 英文表：键是代码里的中文原文（含 {name} 占位与标点），值是英文。语气与 README.en 一致：短句、说人话、不吹。
// 术语表（与 internal/i18n/en.go 相同）：批准 approve · 拒绝 deny · 记住 remember · 收件箱 inbox · hub hub ·
// 守护进程 daemon · 钩子 hook · 规则 rule · 项目 project · 机器 machine · 接入码 enroll token · 体检 health check ·
// 暂停自动审批 auto-approval paused · 交回终端 handed back to the terminal · 等你回答 waiting for your answer ·
// 自己填 Your own answer · 让它在终端问我 Ask me in the terminal instead
export const en: Record<string, string> = {
  正在连接hub: 'Connecting to the hub…',
  跟随系统: 'Follow the system',
  中文: 'Chinese',
  语言: 'Language',
  '通知的文字也跟着这个语言；换了之后这台设备的订阅会重新登记。': 'Notifications use this language too; changing it re-registers this device.',
  通知已开启: 'Notifications on',
  '新的审批与提问会推送到这台设备。可以发一条测试通知，确认收得到。': 'New approvals and questions will be pushed to this device. Send a test notification to check.',
  通知已关闭: 'Notifications off',
  '这台设备不再收到推送；随时可以重新开启。': 'This device will no longer get pushes. You can turn them back on any time.',
  测试通知已发出: 'Test notification sent',
  '几秒内这台设备应弹出“Railse：通知正常”。没有弹出的话，看看系统的专注模式与通知设置。': 'Within a few seconds this device should show "Railse: Notifications are working". If it doesn\'t, check Focus mode and the system notification settings.',
}
