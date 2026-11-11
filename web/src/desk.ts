// 总台（0.3 起）与收件箱按角色筛选的纯函数。口径都在 hub（GET /api/desk）算好，这里只管怎么说、怎么分组、怎么跳。

import { minutesLabel } from './decide'
import { riskLabel } from './format'
import { t } from './i18n'
import type { Desk, DeskRow, Inbox, RequestView } from './types'

/** 放行上限的时长：30 分钟、2 小时、24 小时用面板上那几个按钮的说法；整小时说小时，其余说分钟。 */
function durationLabel(mins: number): string {
  if (mins === 30 || mins === 120 || mins === 1440) return minutesLabel(mins)
  if (mins % 60 === 0) return t('{n} 小时', { n: mins / 60 })
  return t('{n} 分钟', { n: mins })
}

/** 角色卡上的一行边界：上限 · 禁用几项 · 放行最长多久（-1 为不许限时放行）；没设的段不显示，都没设为空串。 */
export function boundaryLine(r: DeskRow): string {
  const parts: string[] = []
  if (r.ceiling) parts.push(t('上限 {risk}', { risk: riskLabel(r.ceiling) }))
  if (r.forbid_count) parts.push(t('禁用 {n} 项', { n: r.forbid_count }))
  if (r.trust_max_mins === -1) parts.push(t('不许限时放行'))
  else if (r.trust_max_mins && r.trust_max_mins > 0) parts.push(t('放行最长 {d}', { d: durationLabel(r.trust_max_mins) }))
  return parts.join(' · ')
}

export interface DeskGroup {
  machine_id: string
  machine_name: string
  online: boolean
  rows: DeskRow[]
}

/** 按机器分组：机器按第一次出现的先后（hub 给的角色行已按机器排），组内角色行在前、未分配在后。 */
export function deskGroups(d: Desk): DeskGroup[] {
  const groups = new Map<string, DeskGroup>()
  for (const r of [...d.roles, ...d.unassigned]) {
    let g = groups.get(r.machine_id)
    if (!g) {
      g = { machine_id: r.machine_id, machine_name: r.machine_name, online: r.online, rows: [] }
      groups.set(r.machine_id, g)
    }
    g.rows.push(r)
  }
  return [...groups.values()]
}

/** 收件箱的筛选：role 为 null 表示不按角色筛，空串表示未分配；machine 为 null 表示不按机器筛。 */
export interface InboxFilter {
  role: string | null
  machine: string | null
}

/** 总台一行“等我”跳去的收件箱：按这一行的角色（未分配为空串）与机器筛选。 */
export function inboxHref(r: Pick<DeskRow, 'name' | 'machine_id'>): string {
  return '/inbox?' + new URLSearchParams({ role: r.name ?? '', machine: r.machine_id }).toString()
}

/** 从查询串读筛选；role、machine 都没有时为 null（不筛）。 */
export function parseFilter(q: URLSearchParams): InboxFilter | null {
  if (!q.has('role') && !q.has('machine')) return null
  return { role: q.get('role'), machine: q.get('machine') }
}

/** 收件箱筛选的查询串（GET /api/inbox?role=…&machine=…）：role 为空串也带上（表示未分配，由 hub 判）；没有筛选为空串。 */
export function inboxQuery(f: InboxFilter | null): string {
  if (!f) return ''
  const q = new URLSearchParams()
  if (f.role !== null) q.set('role', f.role)
  if (f.machine !== null) q.set('machine', f.machine)
  return '?' + q.toString()
}

/**
 * 收件箱有筛选时的列表：由 hub 筛好（在截断之前，“未分配”也由 hub 判），网页不拿全局列表自己筛。
 * 进入筛选或重试为 loading（不展示列表），取到为 ok，取失败为 error（给重试）。筛选开着期间随事件重取，成功就换成最新的。
 */
export type FilterState = { status: 'loading' } | { status: 'ok'; inbox: Inbox } | { status: 'error'; message: string }
export type FilterAction = { type: 'enter' } | { type: 'retry' } | { type: 'data'; inbox: Inbox } | { type: 'error'; message: string }
export const initialFilter: FilterState = { status: 'loading' }

export function filterReducer(_s: FilterState, a: FilterAction): FilterState {
  switch (a.type) {
    case 'enter':
    case 'retry':
      return { status: 'loading' }
    case 'data':
      return { status: 'ok', inbox: a.inbox }
    case 'error':
      return { status: 'error', message: a.message }
  }
}

/** 筛选条上的说明：角色「写作」或“未分配”，有机器名时接在后面。 */
export function filterLabel(f: InboxFilter, machineName: string): string {
  const parts: string[] = []
  if (f.role !== null) parts.push(f.role === '' ? t('未分配') : t('角色「{role}」', { role: f.role }))
  if (f.machine !== null && machineName) parts.push(machineName)
  return parts.join(' · ')
}

/** 卡片与详情页上的角色一行：越界时“超出角色「写作」的边界”（beyond 为真，按高风险配色），有角色时“角色「写作」”，都没有为 null。 */
export function roleText(r: Pick<RequestView, 'role' | 'beyond_role'>): { text: string; beyond: boolean } | null {
  const role = r.role ?? ''
  if (r.beyond_role) return { text: role ? t('超出角色「{role}」的边界', { role }) : t('超出角色的边界'), beyond: true }
  if (role) return { text: t('角色「{role}」', { role }), beyond: false }
  return null
}
