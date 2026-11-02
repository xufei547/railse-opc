// 限时放行的展示：设置页一节的每行文字与收件箱横幅。纯函数，便于不带浏览器环境测试。

import { cliLabel, machineLabel } from './explain'
import { absoluteTime, parseTime } from './format'
import { lang, t } from './i18n'
import type { TrustView } from './types'

/** 第一行：放行的范围。会话：CLI 名 · 项目 · 机器（会话没有项目根时不写项目）；项目：项目 · 机器。 */
export function trustWhere(v: TrustView): string {
  const parts: string[] = []
  if (v.scope === 'session') parts.push(t('会话 {cli}', { cli: cliLabel(v.tool ?? '') }))
  if (v.project) parts.push(t('项目 {name}', { name: v.project }))
  parts.push(t('机器 {name}', { name: machineLabel(v.machine_name) }))
  return parts.join(' · ')
}

/** 剩余时间：小时与分钟，分钟向上取整（还剩 30 秒不说成“剩 0 分”）；不到 1 分钟单说。 */
export function remainingLabel(seconds: number): string {
  if (seconds < 60) return lang() === 'en' ? 'less than 1 min left' : '剩不到 1 分'
  const total = Math.ceil(seconds / 60)
  const h = Math.floor(total / 60)
  const m = total % 60
  if (lang() === 'en') {
    if (h === 0) return `${m} min left`
    return m > 0 ? `${h} hr ${m} min left` : `${h} hr left`
  }
  if (h === 0) return `剩 ${m} 分`
  return m > 0 ? `剩 ${h} 小时 ${m} 分` : `剩 ${h} 小时`
}

// 是函数而不是常量：文案按调用时的语言取
const creators = (): Record<string, string> => ({ phone: t('手机建'), cli: t('终端建'), env: t('启动命令建') })

/** 谁建的：手机批准时、railse trust、RAILSE_TRUST 启动命令；不认识的原样。 */
export function createdByLabel(by: string): string {
  return creators()[by] ?? by
}

/** 第二行：剩余时间 · 谁建的 · 备注（没有备注不带）。 */
export function trustMeta(v: TrustView): string {
  return [remainingLabel(v.remaining_seconds), createdByLabel(v.created_by), v.note ?? ''].filter((x) => x !== '').join(' · ')
}

/** 最早到期的那条的 until；hub 的时间小数位不定宽，按解析后的时刻比。空列表回空串。 */
export function earliestUntil(list: readonly TrustView[]): string {
  let best = ''
  let bestMs = Infinity
  for (const v of list) {
    const ms = parseTime(v.until)
    if (ms < bestMs) {
      best = v.until
      bestMs = ms
    }
  }
  return best
}

const pad2 = (n: number) => String(n).padStart(2, '0')

/** 到期时刻：今天的只写“12:30”，别的日子同 absoluteTime（“11-03 09:05”）。解析不了回空串。 */
export function untilClock(iso: string, now: Date = new Date()): string {
  const ms = parseTime(iso)
  if (Number.isNaN(ms)) return ''
  const d = new Date(ms)
  if (d.toDateString() === now.toDateString()) return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`
  return absoluteTime(iso, now)
}

/** 收件箱横幅：n 个放行生效中，有列表时带最近的到期时刻（列表还没拉到时只说条数）。英文按单复数整句写。 */
export function trustBanner(n: number, list: readonly TrustView[] | null, now: Date = new Date()): string {
  const time = list ? untilClock(earliestUntil(list), now) : ''
  if (lang() === 'en') {
    const head = n === 1 ? '1 trust active' : `${n} trusts active`
    if (!time) return head
    return n === 1 ? `${head}, ends at ${time}` : `${head}, the soonest ends at ${time}`
  }
  return time ? `${n} 个放行生效中，最近的 ${time} 到期` : `${n} 个放行生效中`
}
