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
      return p.type
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
