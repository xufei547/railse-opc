import { lang, t } from './i18n'
import type { Payload, RequestView } from './types'

/**
 * 解析 hub 的时间（定宽 9 位纳秒、UTC，如 2026-10-23T10:00:00.000000000Z），返回毫秒时间戳；解析不了回 NaN。
 * ECMAScript 的日期格式只规定到毫秒，多出的小数位各浏览器认不认不一，先截到 3 位。
 */
export function parseTime(iso: string): number {
  return Date.parse(iso.replace(/(\.\d{3})\d+/, '$1'))
}

export function relativeTime(iso: string, now: Date = new Date()): string {
  const ms = parseTime(iso)
  if (Number.isNaN(ms)) return ''
  const sec = Math.round((now.getTime() - ms) / 1000)
  if (sec < 45) return t('刚刚')
  if (sec < 3600) return t('{n} 分钟前', { n: Math.max(1, Math.round(sec / 60)) })
  if (sec < 86400) return t('{n} 小时前', { n: Math.round(sec / 3600) })
  if (sec < 172800) return t('昨天')
  const d = new Date(ms)
  return `${d.getMonth() + 1}-${String(d.getDate()).padStart(2, '0')}`
}

// 词条表都是函数：模块级常量在 import 时就把语言定死了（App 先于 initLang 被 import）。
const risks = (): Record<string, [string, 'low' | 'mid' | 'high']> => ({
  read: [t('只读'), 'low'],
  write_in_project: [t('项目内改动'), 'low'],
  unknown: [t('无法判断'), 'mid'],
  write_outside: [t('项目外改动'), 'mid'],
  network: [t('联网'), 'mid'],
  outward: [t('对外'), 'high'],
  destructive: [t('不可逆'), 'high'],
})

export function riskLabel(risk: string): string {
  return risks()[risk]?.[0] ?? (risk || t('未分级'))
}

/** 引擎理由里只把“按 <风险> 默认询问”里认识的风险词换成中文；规则 id 等其余文字原样。英文见 engineReasonEn。 */
export function engineReasonLabel(reason: string): string {
  if (lang() === 'en') return engineReasonEn(reason)
  const rs = risks()
  return reason.replace(/按 (\S+) 默认询问/, (m, risk: string) => (rs[risk] ? `按「${rs[risk][0]}」默认询问` : m))
}

// 守护进程写进本机审计、随历史行带上来的固定理由（internal/agent/server.go、requests.go）。
export const agentReasonsEn: Record<string, string> = {
  自动审批已暂停: 'Auto-approval is paused',
  '自动审批已暂停，交回终端处理': 'Auto-approval is paused; handed back to the terminal',
  '本机没有连接 hub，交回终端处理': "This machine isn't connected to a hub; handed back to the terminal",
  '等待过期，交回终端处理': 'Timed out; handed back to the terminal',
  '守护进程正在停止，交回终端处理': 'The daemon is stopping; handed back to the terminal',
  钩子进程在决定前退出: 'The hook exited before a decision was made',
  // internal/agent/hubdecider.go:18-29
  'Gemini 只能拒绝不能放行，交回原生确认框': "Gemini hooks can deny but not approve; handed back to Gemini's own prompt",
  '没有配置 hub，交回终端处理': 'No hub is set up; handed back to the terminal',
  '连不上 hub，交回终端处理': "Couldn't reach the hub; handed back to the terminal",
  'hub 拒绝了本机密钥，交回终端处理': "The hub rejected this machine's key; handed back to the terminal",
  '无法送往 hub，交回终端处理': "Couldn't send it to the hub; handed back to the terminal",
  '与 hub 断线超过宽限，交回终端处理': 'Lost the hub connection for too long; handed back to the terminal',
  'hub 没有给出有效决定，交回终端处理': "The hub didn't return a valid decision; handed back to the terminal",
  'hub 版本太旧，不认识提问，交回终端处理': 'The hub is too old to handle questions; handed back to the terminal',
  已在手机上批准: 'Approved on your phone',
  已在手机上拒绝: 'Denied on your phone',
  已在手机上回答: 'Answered on your phone',
  在手机上选了在终端回答: 'You chose on your phone to answer in the terminal',
  // internal/agent/server.go:433
  '决定来源给的答案不完整，交回终端处理': 'The answer was incomplete; handed back to the terminal',
  '决定来源出错，交回终端处理': 'Something went wrong while deciding; handed back to the terminal',
  '脱敏后有题面相同的题，分不清答的是哪道，交回终端处理':
    "Two questions read the same after redaction, so answers can't be matched; handed back to the terminal",
}

/**
 * 引擎理由的英文：engine_reason 原文是中文（引擎与守护进程写的），照中文前缀解析后整句换成英文；
 * 认不出的（多半是带错误原文的）原样。
 */
function engineReasonEn(reason: string): string {
  if (reason.startsWith('命中规则 ')) return `Matched rule ${reason.slice('命中规则 '.length)}`
  const m = /^无规则命中，按 (\S+) 默认询问$/.exec(reason)
  if (m) {
    const risk = m[1] ?? ''
    return `No rule matched; ${risks()[risk]?.[0] ?? risk} asks by default`
  }
  const fixed = agentReasonsEn[reason]
  if (fixed) return fixed
  // 带 Go 错误原文的理由（脱敏后题目超出上限等）：认前后缀，整句换成英文
  if (reason.startsWith('答案不合规')) return "The answer didn't fit the questions; handed back to the terminal"
  if (reason.endsWith('，交回终端处理')) return 'Handed back to the terminal'
  return reason
}

export function riskTone(risk: string): 'low' | 'mid' | 'high' {
  return risks()[risk]?.[1] ?? 'mid'
}

export function payloadSummary(p: Payload): string {
  switch (p.type) {
    case 'shell':
      return (p.command ?? '').trim()
    case 'file_write':
      return p.path ?? ''
    case 'network':
      return `${p.method ? p.method + ' ' : ''}${p.url ?? ''}`
    case 'mcp':
      return [p.server, p.tool].filter(Boolean).join('/')
    default:
      return p.tool || p.type
  }
}

const states = (): Record<string, string> => ({
  pending: t('等待决定'),
  approved: t('已批准'),
  denied: t('已拒绝'),
  cancelled: t('已取消'),
  expired: t('已过期'),
  auto_allowed: t('自动放行'),
  auto_denied: t('自动拒绝'),
  passthrough: t('交回终端'),
  recorded: t('已记录'),
  answered: t('已回答'),
})

export function stateLabel(state: string): string {
  return states()[state] ?? state
}

const kinds = (): Record<string, string> => ({ permission: t('审批'), question: t('等你回答'), done: t('完成'), webhook: lang() === 'en' ? 'Webhook' : 'webhook', ask: t('提问') })

export function kindLabel(kind: string): string {
  return kinds()[kind] ?? kind
}

const decidedBy = (): Record<string, string> => ({
  terminal: t('终端里处理了'),
  timeout: t('等待过期'),
  hub_unreachable: t('连不上 hub，交回终端'),
  gemini: t('Gemini 交回原生确认'),
  paused: t('已暂停自动审批'),
  shutdown: t('守护进程停止'),
  resend: t('被重发顶掉'),
  agent_restart: t('守护进程重连后不再持有'),
  machine_revoked: t('机器已吊销'),
  error: t('决定来源出错'),
})

/**
 * 一条记录的依据：谁决定的、按哪条规则。
 * 手机上“批准（或拒绝）并记住”时 hub 把学到的规则 id 写进 rule_id，但决定是用户做的，显示“手机上决定（并记住为 …）”；
 * 只有不是用户决定的（rule:… 自动放行或拒绝、暂停时照常生效的 deny 规则等）才按规则显示。
 */
export function decidedByLabel(r: Pick<RequestView, 'decided_by' | 'rule_id' | 'state'>): string {
  const by = r.decided_by
  if (by === 'user' && r.state === 'answered') return t('手机上回答')
  if (by === 'user') return r.rule_id ? t('手机上决定（并记住为 {rule}）', { rule: r.rule_id }) : t('手机上决定')
  if (by.startsWith('rule:') || r.rule_id) return t('规则 {rule}', { rule: r.rule_id || by.slice(5) })
  return decidedBy()[by] ?? (by || (r.state === 'pending' ? '' : stateLabel(r.state)))
}

/** 不是你、也不是规则决定时，按 decided_by 说一句人话（hub 与守护进程写出的来源，见 internal/hub/requests.go、internal/agent）。 */
const outcomeBy = (): Record<string, string> => ({
  hub_unreachable: t('当时连不上 hub，agent 在终端里自己处理了'),
  no_hub: t('当时本机没接 hub，agent 在终端里自己处理了'),
  agent_restart: t('守护进程重启，这条作废了'),
  shutdown: t('守护进程停止，这条作废了'),
  resend: t('agent 重发了一条新的，这条作废'),
  machine_revoked: t('这台机器已被吊销'),
  paused: t('自动审批暂停中，这条交回终端了'),
  gemini: t('交回 Gemini，在终端里让你确认'),
  error: t('处理出错，交回终端了'),
  hub_error: t('处理出错，交回终端了'),
})

/**
 * 处理结果的一句人话：审批页处理后的状态行与历史卡片共用，跟在状态标签后面。按 state 与 decided_by 的组合说，
 * 不露 decided_by 的原始值；还在等的、问题与完成、来源为空或不认识的回空串（状态标签已经说了）。
 * 提问在手机上答了说“你答了”，在手机上交回终端说“交回终端问了”；其余结束（过期、终端里先答了等）与审批同一套说法。
 * 详情里的“依据”仍用 decidedByLabel（给排查用）。
 */
export function outcomeText(r: Pick<RequestView, 'kind' | 'state' | 'decided_by' | 'rule_id'>): string {
  const { state, decided_by: by } = r
  if (r.kind === 'question' || r.kind === 'done' || state === 'pending' || state === 'recorded') return ''
  if (lang() === 'en') return outcomeTextEn(r)
  if (by === 'user' && state === 'answered') return '你答了'
  if (by === 'user' && state === 'passthrough' && r.kind === 'ask') return '交回终端问了'
  if (by === 'user' && (state === 'approved' || state === 'denied')) {
    const verb = state === 'approved' ? '批准' : '拒绝'
    return r.rule_id ? `你${verb}并记住了（规则 ${r.rule_id}）` : `你${verb}了`
  }
  if (state === 'auto_allowed' || state === 'auto_denied') {
    const rule = r.rule_id || (by.startsWith('rule:') ? by.slice(5) : '')
    return `规则${rule ? ` ${rule} ` : ''}自动${state === 'auto_allowed' ? '批准' : '拒绝'}了`
  }
  if (by === 'terminal') return state === 'cancelled' ? 'agent 那边先结束了，不用你处理' : '交回终端处理了'
  if (by === 'timeout') return r.kind === 'webhook' ? '等太久过期了，已按拒绝回调工作流' : '等太久过期了，agent 已交回终端'
  return outcomeBy()[by] ?? ''
}

/** outcomeText 的英文：分支与中文一一对应，只是整句换成英文（动词与规则 id 的语序不同，不查英文表）。 */
function outcomeTextEn(r: Pick<RequestView, 'kind' | 'state' | 'decided_by' | 'rule_id'>): string {
  const { state, decided_by: by } = r
  if (by === 'user' && state === 'answered') return 'You answered'
  if (by === 'user' && state === 'passthrough' && r.kind === 'ask') return 'Handed back to the terminal to ask'
  if (by === 'user' && (state === 'approved' || state === 'denied')) {
    const verb = state === 'approved' ? 'approved' : 'denied'
    return r.rule_id ? `You ${verb} and remembered it (rule ${r.rule_id})` : `You ${verb}`
  }
  if (state === 'auto_allowed' || state === 'auto_denied') {
    const rule = r.rule_id || (by.startsWith('rule:') ? by.slice(5) : '')
    return `${rule ? `Rule ${rule}` : 'A rule'} ${state === 'auto_allowed' ? 'allowed' : 'denied'} it automatically`
  }
  if (by === 'terminal') return state === 'cancelled' ? 'The agent moved on; nothing for you to do' : 'Handled in the terminal'
  if (by === 'timeout') return r.kind === 'webhook' ? 'Timed out; the workflow was told "denied"' : 'Timed out; the agent went back to the terminal'
  return outcomeBy()[by] ?? ''
}

// 引擎触发点的性质（internal/policy/trigger.go 的 Why 常量，中文原文作键）。英文是动词短语，接在引号括起的命令段后面；
// 与 internal/i18n/en.go 一字不差（i18n.sync.test.ts 核对）。
export const triggerWhysEn: Record<string, string> = {
  '删除文件，删了不可恢复': "deletes files and can't be undone",
  结束进程: 'kills processes',
  以管理员权限运行: 'runs with admin rights',
  '读写敏感文件（密钥、配置或开机自启）': 'touches a sensitive file (keys, config or startup items)',
  把脚本直接交给解释器执行: 'feeds a script straight into an interpreter',
  下载脚本并直接执行: 'downloads a script and runs it',
  删表或删库: 'drops a table or database',
  '强制推送，会覆盖远程历史': 'force-pushes and overwrites remote history',
  把代码推到远程仓库: 'pushes code to a remote',
  发布或部署到外部: 'publishes or deploys outside',
  向外发送数据: 'sends data out',
  要联网: 'needs the network',
  改项目外的文件: 'writes outside the project',
  这条命令不在已知名单里: "isn't on the known-command list",
  不可逆的操作: "can't be undone",
  影响仓库之外的人和系统: 'affects people and systems outside the repo',
}

/** 触发点的性质按语言取；英文表里没有的（更新的守护进程）原样。 */
export function triggerWhyLabel(why: string): string {
  return lang() === 'en' ? (triggerWhysEn[why] ?? why) : why
}

/** 截到至多 max 个 UTF-16 单元再加省略号；不把代理对切成半个字符。卡片摘要用它，免得超长命令撑出一大块 DOM。 */
export function clip(s: string, max: number): string {
  if (s.length <= max) return s
  let cut = s.slice(0, max)
  const last = cut.charCodeAt(cut.length - 1)
  if (last >= 0xd800 && last <= 0xdbff) cut = cut.slice(0, -1)
  return cut + '…'
}

const pad2 = (n: number) => String(n).padStart(2, '0')

/** 本地时间“10-03 08:05”；不是今年的带年份“2025-01-02 23:59”。解析不了回空串。 */
export function absoluteTime(iso: string, now: Date = new Date()): string {
  const t = parseTime(iso)
  if (Number.isNaN(t)) return ''
  const d = new Date(t)
  const hm = `${pad2(d.getHours())}:${pad2(d.getMinutes())}`
  const md = `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`
  return d.getFullYear() === now.getFullYear() ? `${md} ${hm}` : `${d.getFullYear()}-${md} ${hm}`
}

const auditActions = (): Record<string, string> => ({
  request_pending: t('登记'),
  // 英文时间线记的是“提交了什么”，不和按钮共用词；中文仍用共用键
  allow: lang() === 'en' ? 'Approval submitted' : t('批准'),
  deny: lang() === 'en' ? 'Denial submitted' : t('拒绝'),
  answer: lang() === 'en' ? 'Answer submitted' : t('回答'),
  rule_learned: t('学到规则'),
  rule_learn_rolled_back: t('撤销学到的规则'),
  push_sent: t('推送到手机'),
  webhook_callback: t('回调来源'),
  undeliverable: t('决定未送达'),
  cancelled_resend: t('被重发顶掉'),
})

/** 审计动作的中文名。hub 结束请求时用状态名作动作（approved、expired……），这些沿用状态文案；不认识的原样。 */
export function auditLabel(action: string): string {
  return auditActions()[action] ?? stateLabel(action)
}

const actors = (): Record<string, string> => ({ user: t('你'), agent: t('守护进程'), system: 'hub' })

export function actorLabel(actor: string): string {
  return actors()[actor] ?? actor
}

/** 审计 detail（JSON 对象）展开成“k=v k=v”；去掉空串、null 与 false（ok=false 是回调失败，保留）。不是 JSON 时原样。 */
export function auditDetail(detail: string): string {
  let d: unknown
  try {
    d = JSON.parse(detail)
  } catch {
    return detail
  }
  if (typeof d !== 'object' || d === null) return ''
  return Object.entries(d as Record<string, unknown>)
    .filter(([k, v]) => v !== '' && v !== null && v !== undefined && (v !== false || k === 'ok'))
    .map(([k, v]) => `${k}=${typeof v === 'string' ? v : JSON.stringify(v)}`)
    .join(' ')
}

/**
 * 展示的内容是不是含被脱敏抹掉的部分（不是原文）。hub 的 has_redacted 只看载荷；webhook 的标题、question 的问题文本
 * 也是脱敏过的，这里再查一下标题里的占位符。
 */
export function isRedacted(r: Pick<RequestView, 'has_redacted' | 'title'>): boolean {
  return r.has_redacted || r.title.includes('<redacted>')
}

/**
 * 手机上批准、拒绝、回答之后决定（答案）有没有送到：delivered_at 非空是已送达钩子（webhook 是回调成功）；
 * hub 记过 undeliverable（守护进程已不再持有这条）时是送不到了。其他状态（含交回终端，它不等送达）回空串。
 */
export function deliveryLabel(r: Pick<RequestView, 'state' | 'kind' | 'delivered_at'>, undeliverable: boolean): string {
  if (r.state === 'answered') {
    if (r.delivered_at) return t('已送达 agent')
    return undeliverable ? t('答案未送达：agent 已不再等这条（多半已在终端问了你）') : t('还没送达 agent')
  }
  if (r.state !== 'approved' && r.state !== 'denied') return ''
  const hook = r.kind === 'webhook'
  if (r.delivered_at) return hook ? t('已回调来源') : t('已送达 agent')
  if (undeliverable) return t('决定未送达：agent 已不再等这条（多半已交回终端）')
  return hook ? t('还没回调成功') : t('还没送达 agent')
}
