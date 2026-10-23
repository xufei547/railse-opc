// 规则、机器与设置页的小工具。纯函数，便于不带浏览器环境测试。

import { ApiError } from './api'
import { relativeTime } from './format'
import type { Machine, Rule, Status } from './types'

const sources: Record<string, string> = { builtin: '内置', user: '手写', learned: '学来的' }

export function sourceLabel(source: string): string {
  return sources[source] ?? source
}

export function hitsLabel(r: Pick<Rule, 'hit_count' | 'last_hit_at'>, now: Date = new Date()): string {
  if (r.hit_count <= 0) return '还没命中过'
  const last = r.last_hit_at ? relativeTime(r.last_hit_at, now) : ''
  return last ? `命中 ${r.hit_count} 次，最近 ${last}` : `命中 ${r.hit_count} 次`
}

/** 启用着的、能自动放行对外与不可逆操作（elevated）的规则条数。 */
export function elevatedEnabled(rules: readonly Rule[]): number {
  return rules.filter((r) => r.enabled && r.elevated).length
}

/** 已吊销的排到最后，其余保持 hub 的顺序（上下线时行不跳来跳去）。 */
export function sortMachines(ms: readonly Machine[]): Machine[] {
  return [...ms.filter((m) => !m.revoked), ...ms.filter((m) => m.revoked)]
}

export function machineSeen(m: Pick<Machine, 'online' | 'last_seen' | 'revoked'>, now: Date = new Date()): string {
  if (m.online && !m.revoked) return '在线'
  return m.last_seen ? `最近在线 ${relativeTime(m.last_seen, now)}` : '从未连上'
}

/** 在线机器数与机器总数，都不算已吊销的。 */
export function onlineCount(status: Pick<Status, 'machines'> | null): { online: number; total: number } {
  const active = status?.machines.filter((m) => !m.revoked) ?? []
  return { online: active.filter((m) => m.online).length, total: active.length }
}

/** hub 的运行时长是 Go 的 Duration 文本（取整到秒，如 26h5m0s），写成中文、只留两级；认不出的原样。 */
export function formatUptime(s: string): string {
  if (!s) return '-'
  const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)$/.exec(s)
  if (!m) return s
  const [h, min, sec] = [m[1], m[2], m[3]].map((x) => Number(x ?? 0)) as [number, number, number]
  const days = Math.floor(h / 24)
  const hours = h % 24
  if (days > 0) return hours > 0 ? `${days} 天 ${hours} 小时` : `${days} 天`
  if (h > 0) return min > 0 ? `${h} 小时 ${min} 分钟` : `${h} 小时`
  if (min > 0) return `${min} 分钟`
  return `${sec} 秒`
}

/** hub 收的规则文件至多这么大（与 hub 的 rulesImportMax 一致），超了不发，免得白传。 */
export const importMax = 1 << 20

/** 导入确认里的文件大小：不到 1 KB 写字节，到 1 MiB 为止写 KB（向上取整），再大写 MB。 */
export function fileSize(n: number): string {
  if (n < 1024) return `${n} 字节`
  if (n <= importMax) return `${Math.ceil(n / 1024)} KB`
  return `${(n / importMax).toFixed(1)} MB`
}

/**
 * 操作或加载失败时给人看的文案：hub 的错误原文照给（409、422 等不解析）；401 回 null——登录失效回调已在跳登录页，不另提示。
 */
export function failure(e: unknown, fallback: string): string | null {
  if (e instanceof ApiError && e.status === 401) return null
  return e instanceof Error && e.message ? e.message : fallback
}
