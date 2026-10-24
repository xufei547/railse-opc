// 历史页的筛选、标签与分页。纯函数，便于不带浏览器环境测试。

import { decidedByLabel, stateLabel } from './format'
import type { RequestView } from './types'

/** [state 参数, 文案]。空串是全部；recorded 是问题与完成（hub 把 question、done 存成 recorded 行）。 */
export const historyFilters: readonly (readonly [string, string])[] = [
  ['', '全部'],
  ['approved', '已批准'],
  ['denied', '已拒绝'],
  ['auto_allowed', '自动放行'],
  ['auto_denied', '自动拒绝'],
  ['passthrough', '交回终端'],
  ['expired', '已过期'],
  ['cancelled', '已取消'],
  ['recorded', '问题与完成'],
]

const known = new Set(historyFilters.map(([v]) => v))

/** 地址栏 ?state= 的值：认得的原样，没有或不认得的当作全部。 */
export function parseFilter(v: string | null): string {
  return v !== null && known.has(v) ? v : ''
}

/** 先拉这么多条；“加载更多”一次升到 maxLimit（hub 的上限）。 */
export const pageSize = 100
export const maxLimit = 500

export type Tone = 'low' | 'mid' | 'high' | 'plain'

const stateTones: Record<string, Tone> = {
  approved: 'low',
  auto_allowed: 'low',
  denied: 'high',
  auto_denied: 'high',
  pending: 'mid',
}

/** 卡片上的标签：审批与 webhook 显示状态（放行绿、拒绝红、等待琥珀、其余灰）；问题与完成不是决定，显示种类。 */
export function historyTag(r: Pick<RequestView, 'kind' | 'state'>): { label: string; tone: Tone } {
  if (r.kind === 'question') return { label: '问题', tone: 'plain' }
  if (r.kind === 'done') return { label: '完成', tone: 'plain' }
  return { label: stateLabel(r.state), tone: stateTones[r.state] ?? 'plain' }
}

/** 卡片上的依据（谁决定、按哪条规则）；还在等的、问题与完成没有。 */
export function historyBasis(r: Pick<RequestView, 'kind' | 'state' | 'decided_by' | 'rule_id'>): string {
  if (r.kind === 'question' || r.kind === 'done' || r.state === 'pending') return ''
  return decidedByLabel(r)
}

export type More = 'more' | 'loading' | 'capped' | 'none'

/**
 * 列表底部显示什么：count 是拉回来的条数，loaded 是那次拉取的 limit，want 是现在要的 limit。
 * 满了且还没到上限给“加载更多”；点了之后、拉回来之前是 loading；到了上限还是满的，说明只显示最近 maxLimit 条。
 */
export function moreState(count: number, loaded: number, want: number): More {
  if (want > loaded) return 'loading'
  if (count < loaded) return 'none'
  return loaded < maxLimit ? 'more' : 'capped'
}
