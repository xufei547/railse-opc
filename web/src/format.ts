import type { Payload, RequestView } from './types'

/**
 * 解析 hub 的时间（定宽 9 位纳秒、UTC，如 2026-10-23T10:00:00.000000000Z），返回毫秒时间戳；解析不了回 NaN。
 * ECMAScript 的日期格式只规定到毫秒，多出的小数位各浏览器认不认不一，先截到 3 位。
 */
export function parseTime(iso: string): number {
  return Date.parse(iso.replace(/(\.\d{3})\d+/, '$1'))
}

export function relativeTime(iso: string, now: Date = new Date()): string {
  const t = parseTime(iso)
  if (Number.isNaN(t)) return ''
  const sec = Math.round((now.getTime() - t) / 1000)
  if (sec < 45) return '刚刚'
  if (sec < 3600) return `${Math.max(1, Math.round(sec / 60))} 分钟前`
  if (sec < 86400) return `${Math.round(sec / 3600)} 小时前`
  if (sec < 172800) return '昨天'
  const d = new Date(t)
  return `${d.getMonth() + 1}-${String(d.getDate()).padStart(2, '0')}`
}

const risks: Record<string, [string, 'low' | 'mid' | 'high']> = {
  read: ['只读', 'low'],
  write_in_project: ['项目内改动', 'low'],
  unknown: ['无法判断', 'mid'],
  write_outside: ['项目外改动', 'mid'],
  network: ['联网', 'mid'],
  outward: ['对外', 'high'],
  destructive: ['不可逆', 'high'],
}

export function riskLabel(risk: string): string {
  return risks[risk]?.[0] ?? (risk || '未分级')
}

/** 引擎理由里只把“按 <风险> 默认询问”里认识的风险词换成中文；规则 id 等其余文字原样。 */
export function engineReasonLabel(reason: string): string {
  return reason.replace(/按 (\S+) 默认询问/, (m, risk: string) => (risks[risk] ? `按「${risks[risk][0]}」默认询问` : m))
}

export function riskTone(risk: string): 'low' | 'mid' | 'high' {
  return risks[risk]?.[1] ?? 'mid'
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

const states: Record<string, string> = {
  pending: '等待决定',
  approved: '已批准',
  denied: '已拒绝',
  cancelled: '已取消',
  expired: '已过期',
  auto_allowed: '自动放行',
  auto_denied: '自动拒绝',
  passthrough: '交回终端',
  recorded: '已记录',
}

export function stateLabel(state: string): string {
  return states[state] ?? state
}

const kinds: Record<string, string> = { permission: '审批', question: '等你回答', done: '完成', webhook: 'webhook' }

export function kindLabel(kind: string): string {
  return kinds[kind] ?? kind
}

const decidedBy: Record<string, string> = {
  terminal: '终端里处理了',
  timeout: '等待过期',
  hub_unreachable: '连不上 hub，交回终端',
  gemini: 'Gemini 交回原生确认',
  paused: '已暂停自动审批',
  shutdown: '守护进程停止',
  resend: '被重发顶掉',
  agent_restart: '守护进程重连后不再持有',
  machine_revoked: '机器已吊销',
  error: '决定来源出错',
}

/**
 * 一条记录的依据：谁决定的、按哪条规则。
 * 手机上“批准（或拒绝）并记住”时 hub 把学到的规则 id 写进 rule_id，但决定是用户做的，显示“手机上决定（并记住为 …）”；
 * 只有不是用户决定的（rule:… 自动放行或拒绝、暂停时照常生效的 deny 规则等）才按规则显示。
 */
export function decidedByLabel(r: Pick<RequestView, 'decided_by' | 'rule_id' | 'state'>): string {
  const by = r.decided_by
  if (by === 'user') return r.rule_id ? `手机上决定（并记住为 ${r.rule_id}）` : '手机上决定'
  if (by.startsWith('rule:') || r.rule_id) return `规则 ${r.rule_id || by.slice(5)}`
  return decidedBy[by] ?? (by || (r.state === 'pending' ? '' : stateLabel(r.state)))
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

const auditActions: Record<string, string> = {
  request_pending: '登记',
  allow: '批准',
  deny: '拒绝',
  rule_learned: '学到规则',
  rule_learn_rolled_back: '撤销学到的规则',
  push_sent: '推送到手机',
  webhook_callback: '回调来源',
  undeliverable: '决定未送达',
  cancelled_resend: '被重发顶掉',
}

/** 审计动作的中文名。hub 结束请求时用状态名作动作（approved、expired……），这些沿用状态文案；不认识的原样。 */
export function auditLabel(action: string): string {
  return auditActions[action] ?? stateLabel(action)
}

const actors: Record<string, string> = { user: '你', agent: '守护进程', system: 'hub' }

export function actorLabel(actor: string): string {
  return actors[actor] ?? actor
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
 * 手机上批准、拒绝之后决定有没有送到：delivered_at 非空是已送达钩子（webhook 是回调成功）；
 * hub 记过 undeliverable（守护进程已不再持有这条）时是送不到了。其他状态回空串。
 */
export function deliveryLabel(r: Pick<RequestView, 'state' | 'kind' | 'delivered_at'>, undeliverable: boolean): string {
  if (r.state !== 'approved' && r.state !== 'denied') return ''
  const hook = r.kind === 'webhook'
  if (r.delivered_at) return hook ? '已回调来源' : '已送达 agent'
  if (undeliverable) return '决定未送达：agent 已不再等这条（多半已交回终端）'
  return hook ? '还没回调成功' : '还没送达 agent'
}
