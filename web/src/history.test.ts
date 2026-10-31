import { describe, expect, it } from 'vitest'
import { historyFilters, historyTag, maxLimit, moreState, pageSize, parseFilter } from './history'
import type { RequestView } from './types'

function req(over: Partial<RequestView> = {}): RequestView {
  return {
    id: 'r1', machine_id: 'm', session_id: 's', cli: 'claude', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'shell', command: 'git push' }, risk: 'outward', project_root: '/p/blog', engine_reason: '',
    state: 'approved', decided_by: 'user', decision_note: '', rule_id: '', turn_id: '', cli_session_id: '', title: '',
    created_at: '2026-10-23T10:00:00.000000000Z', expires_at: null, decided_at: null, delivered_at: null,
    machine_name: 'mac', project: 'blog', has_redacted: false, ...over,
  }
}

describe('historyFilters 与 parseFilter', () => {
  it('筛选项：全部在前，覆盖 pending 以外的每个状态，问题与完成用 recorded', () => {
    expect(historyFilters[0]).toEqual(['', '全部'])
    expect(historyFilters.map(([v]) => v)).toEqual([
      '', 'approved', 'denied', 'auto_allowed', 'auto_denied', 'passthrough', 'expired', 'cancelled', 'recorded',
    ])
    expect(Object.fromEntries(historyFilters)['recorded']).toBe('问题与完成')
  })

  it('地址栏里的 state：认得的原样，没有或不认得的当作全部', () => {
    expect(parseFilter('denied')).toBe('denied')
    expect(parseFilter('recorded')).toBe('recorded')
    expect(parseFilter(null)).toBe('')
    expect(parseFilter('')).toBe('')
    expect(parseFilter('pending')).toBe('')
    expect(parseFilter('<script>')).toBe('')
  })
})

describe('historyTag', () => {
  it('审批与 webhook 显示状态：放行绿、拒绝红、等待琥珀、其余灰', () => {
    expect(historyTag(req({ state: 'approved' }))).toEqual({ label: '已批准', tone: 'low' })
    expect(historyTag(req({ state: 'auto_allowed' }))).toEqual({ label: '自动放行', tone: 'low' })
    expect(historyTag(req({ state: 'denied' }))).toEqual({ label: '已拒绝', tone: 'high' })
    expect(historyTag(req({ state: 'auto_denied' }))).toEqual({ label: '自动拒绝', tone: 'high' })
    expect(historyTag(req({ state: 'pending' }))).toEqual({ label: '等待决定', tone: 'mid' })
    expect(historyTag(req({ state: 'passthrough' }))).toEqual({ label: '交回终端', tone: 'plain' })
    expect(historyTag(req({ state: 'expired' }))).toEqual({ label: '已过期', tone: 'plain' })
    expect(historyTag(req({ state: 'cancelled' }))).toEqual({ label: '已取消', tone: 'plain' })
    expect(historyTag(req({ kind: 'webhook', state: 'denied' }))).toEqual({ label: '已拒绝', tone: 'high' })
  })

  it('问题与完成不是决定，显示种类（历史里的问题不说“等你回答”）', () => {
    expect(historyTag(req({ kind: 'question', state: 'recorded' }))).toEqual({ label: '问题', tone: 'plain' })
    expect(historyTag(req({ kind: 'done', state: 'recorded' }))).toEqual({ label: '完成', tone: 'plain' })
  })
})

describe('moreState', () => {
  it('先拉 100 条，满了才给“加载更多”，一次升到 500', () => {
    expect(pageSize).toBe(100)
    expect(maxLimit).toBe(500)
    expect(moreState(37, 100, 100)).toBe('none')
    expect(moreState(100, 100, 100)).toBe('more')
  })

  it('点了之后到拉回来之前是 loading', () => {
    expect(moreState(100, 100, 500)).toBe('loading')
  })

  it('500 条也满了：说明只显示最近 500 条；不满就是全部', () => {
    expect(moreState(500, 500, 500)).toBe('capped')
    expect(moreState(321, 500, 500)).toBe('none')
  })
})
