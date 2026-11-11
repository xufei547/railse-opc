// 总台（0.3 起）与收件箱按角色筛选的纯函数。口径都在 hub（GET /api/desk）算好，这里只管怎么说、怎么分组、怎么跳。

import { minutesLabel } from './decide'
import { riskLabel } from './format'
import { t } from './i18n'
import type { Desk, DeskRow, RequestView } from './types'

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

/** 每台机器当前角色表里的角色名（来自 GET /api/desk）；只出现在未分配一栏的机器为空数组。收件箱的“未分配”筛选据此判断。 */
export type KnownRoles = Record<string, string[]>

export function knownRoles(d: Desk): KnownRoles {
  const out: KnownRoles = {}
  for (const r of [...d.roles, ...d.unassigned]) {
    out[r.machine_id] ??= []
    if (r.name !== undefined) out[r.machine_id]!.push(r.name)
  }
  return out
}

/**
 * 按筛选留下匹配的请求；没有 role 键的（老 hub）算空串。“未分配”（role 为空串）与总台同口径：角色为空，或不在该机器当前的角色表里
 * （角色已从 roles.yaml 删掉的）；还没拿到角色表（known 为空）时只认空串。
 */
export function applyFilter<T extends Pick<RequestView, 'machine_id'> & { role?: string }>(list: T[], f: InboxFilter | null, known?: KnownRoles | null): T[] {
  if (!f) return list
  const roleOK = (r: T) => {
    const role = r.role ?? ''
    if (f.role === null) return true
    if (f.role !== '') return role === f.role
    return role === '' || (!!known && !(known[r.machine_id] ?? []).includes(role))
  }
  return list.filter((r) => roleOK(r) && (f.machine === null || r.machine_id === f.machine))
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
