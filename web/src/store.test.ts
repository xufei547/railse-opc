import { describe, expect, it } from 'vitest'
import { groupByProject, initialState, reduce, type State } from './store'
import type { Frame } from './ws'
import type { RequestView, Status } from './types'

function req(over: Partial<RequestView>): RequestView {
  return {
    id: 'r', machine_id: 'm', session_id: 's', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'shell', command: 'git push' }, risk: 'outward', project_root: '/p/blog', engine_reason: '无规则命中',
    state: 'pending', decided_by: '', decision_note: '', rule_id: '', turn_id: 't', cli_session_id: 's', title: '',
    created_at: '2026-10-23T10:00:00.000000000Z', expires_at: null, decided_at: null, delivered_at: null,
    machine_name: 'mac', project: 'blog', has_redacted: false, ...over,
  }
}
const status: Status = { paused: false, pending: 0, rules_version: 1, machines: [], uptime: '1s', push_enabled: false, push_subscriptions: 0 }

function loaded(questions: RequestView[] = []): State {
  return reduce(initialState, { type: 'loaded', inbox: { pending: [req({ id: 'b', created_at: '2026-10-23T10:00:02Z' }), req({ id: 'a', created_at: '2026-10-23T10:00:01Z' })], questions }, status })
}

const frame = (f: Frame) => ({ type: 'frame' as const, frame: f })

describe('reduce', () => {
  it('loaded 按创建时间排序', () => {
    const s = loaded()
    expect(s.loaded).toBe(true)
    expect(s.pending.map((r) => r.id)).toEqual(['a', 'b'])
  })

  it('loaded 清掉错误、保留 hub 给的问题顺序', () => {
    let s = reduce(initialState, { type: 'error', error: '连不上 hub，请检查网络' })
    expect(s.error).toBe('连不上 hub，请检查网络')
    s = reduce(s, { type: 'loaded', inbox: { pending: [], questions: [req({ id: 'q2', kind: 'question' }), req({ id: 'q1', kind: 'question' })] }, status })
    expect(s.error).toBeNull()
    expect(s.questions.map((q) => q.id)).toEqual(['q2', 'q1'])
  })

  it('request_new 的 pending 进收件箱并去重', () => {
    let s = loaded()
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'c', created_at: '2026-10-23T10:00:03Z' }) }))
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'c', created_at: '2026-10-23T10:00:03Z' }) }))
    expect(s.pending.map((r) => r.id)).toEqual(['a', 'b', 'c'])
    expect(s.tick).toBe(2)
  })

  it('request_updated 为终态时移出收件箱', () => {
    let s = loaded()
    s = reduce(s, frame({ type: 'request_updated', data: req({ id: 'a', state: 'approved' }) }))
    expect(s.pending.map((r) => r.id)).toEqual(['b'])
    expect(s.tick).toBe(1)
  })

  it('request_updated 仍是 pending 时更新这一条', () => {
    let s = loaded()
    s = reduce(s, frame({ type: 'request_updated', data: req({ id: 'a', created_at: '2026-10-23T10:00:01Z', delivered_at: '2026-10-23T10:00:05Z' }) }))
    expect(s.pending.map((r) => r.id)).toEqual(['a', 'b'])
    expect(s.pending[0]?.delivered_at).toBe('2026-10-23T10:00:05Z')
  })

  it('问题：同会话只留最新；done 或新审批收掉它', () => {
    let s = loaded()
    const q1 = req({ id: 'q1', kind: 'question', state: 'recorded', session_id: 's1', title: '删旧分支？' })
    const q2 = req({ id: 'q2', kind: 'question', state: 'recorded', session_id: 's1', title: '再问一次' })
    const q3 = req({ id: 'q3', kind: 'question', state: 'recorded', session_id: 's2', title: '用哪个库？' })
    s = reduce(s, frame({ type: 'request_new', data: q1 }))
    s = reduce(s, frame({ type: 'request_new', data: q2 }))
    s = reduce(s, frame({ type: 'request_new', data: q3 }))
    expect(s.questions.map((q) => q.id)).toEqual(['q3', 'q2'])
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'd', kind: 'done', state: 'recorded', session_id: 's1' }) }))
    expect(s.questions.map((q) => q.id)).toEqual(['q3'])
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'p', session_id: 's2' }) }))
    expect(s.questions).toEqual([])
    expect(s.pending.map((r) => r.id)).toContain('p')
  })

  it('问题按机器加会话匹配：别的机器同名会话不收掉它', () => {
    let s = loaded([req({ id: 'q1', kind: 'question', state: 'recorded', machine_id: 'm1', session_id: 's1' })])
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'd', kind: 'done', state: 'recorded', machine_id: 'm2', session_id: 's1' }) }))
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'q2', kind: 'question', state: 'recorded', machine_id: 'm2', session_id: 's1' }) }))
    expect(s.questions.map((q) => q.id)).toEqual(['q2', 'q1'])
  })

  it('没有会话 id 的事件不收掉任何问题', () => {
    let s = loaded([req({ id: 'q0', kind: 'question', state: 'recorded', session_id: '' })])
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'd', kind: 'done', state: 'recorded', session_id: '' }) }))
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'w', kind: 'webhook', session_id: '', machine_id: '' }) }))
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'q1', kind: 'question', state: 'recorded', session_id: '' }) }))
    expect(s.questions.map((q) => q.id)).toEqual(['q1', 'q0'])
  })

  it('不是 pending 的新审批不进收件箱，但同会话的问题照样收掉', () => {
    let s = loaded([req({ id: 'q1', kind: 'question', state: 'recorded', session_id: 's1' })])
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'x', state: 'auto_allowed', session_id: 's1' }) }))
    expect(s.pending.map((r) => r.id)).toEqual(['a', 'b'])
    expect(s.questions).toEqual([])
  })

  it('rules_updated 标记过期，status 帧改暂停', () => {
    let s = loaded()
    s = reduce(s, { type: 'rules', rules: [] })
    expect(s.rulesStale).toBe(false)
    s = reduce(s, frame({ type: 'rules_updated', data: { version: 2 } }))
    expect(s.rulesStale).toBe(true)
    s = reduce(s, frame({ type: 'status', data: { paused: true } }))
    expect(s.status?.paused).toBe(true)
  })

  it('rules_stale（重连后可能漏了 rules_updated）标记规则过期', () => {
    let s = reduce(loaded(), { type: 'rules', rules: [] })
    s = reduce(s, { type: 'rules_stale' })
    expect(s.rulesStale).toBe(true)
    expect(s.rules).toEqual([])
  })

  it('坏帧与不认识的帧不改状态', () => {
    const s = loaded()
    expect(reduce(s, frame({ type: 'request_new', data: 'nope' }))).toBe(s)
    expect(reduce(s, frame({ type: 'request_updated', data: { id: 1 } }))).toBe(s)
    expect(reduce(s, frame({ type: 'status', data: { paused: 'yes' } }))).toBe(s)
    expect(reduce(s, frame({ type: 'hello', data: {} } as unknown as Frame))).toBe(s)
  })

  it('每次全量拉取完成 loads 加一（详情、历史页据此重拉断线期间漏掉的变化）；帧不加 loads、全量拉取不加 tick', () => {
    let s = loaded()
    expect(s.loads).toBe(1)
    expect(s.tick).toBe(0)
    s = reduce(s, frame({ type: 'request_updated', data: req({ id: 'a', state: 'approved' }) }))
    expect(s.loads).toBe(1)
    s = reduce(s, { type: 'loaded', inbox: { pending: [], questions: [] }, status })
    expect(s.loads).toBe(2)
    expect(s.tick).toBe(1)
  })

  it('touched 记下每条请求最近一帧的 tick：一批里连着来的两帧都记得住', () => {
    let s = loaded()
    s = reduce(s, frame({ type: 'request_updated', data: req({ id: 'a', state: 'approved' }) }))
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'c', created_at: '2026-10-23T10:00:03Z' }) }))
    expect(s.touched).toEqual({ a: 1, c: 2 })
    s = reduce(s, frame({ type: 'request_updated', data: req({ id: 'a', state: 'approved', delivered_at: '2026-10-23T10:00:09Z' }) }))
    expect(s.touched.a).toBe(3)
    // 全量拉取、坏帧不动它
    const before = s.touched
    s = reduce(s, { type: 'loaded', inbox: { pending: [], questions: [] }, status })
    s = reduce(s, frame({ type: 'request_updated', data: { id: 1 } }))
    expect(s.touched).toBe(before)
  })

  it('touched 至多留最近 200 条，挤掉最旧的', () => {
    let s = loaded()
    for (let i = 0; i < 205; i++) s = reduce(s, frame({ type: 'request_updated', data: req({ id: `r${i}`, state: 'approved' }) }))
    expect(Object.keys(s.touched)).toHaveLength(200)
    expect(s.touched.r0).toBeUndefined()
    expect(s.touched.r4).toBeUndefined()
    expect(s.touched.r5).toBe(6)
    expect(s.touched.r204).toBe(205)
  })

  it('连接状态与错误', () => {
    let s = reduce(initialState, { type: 'connected', connected: true })
    expect(s.connected).toBe(true)
    s = reduce(s, { type: 'error', error: 'x' })
    expect(s.error).toBe('x')
    s = reduce(s, { type: 'error', error: null })
    expect(s.error).toBeNull()
  })
})

describe('groupByProject', () => {
  it('按项目分组，组内按时间，webhook 单独一组', () => {
    const g = groupByProject([
      req({ id: '1', project: 'blog', created_at: '2026-10-23T10:00:05Z' }),
      req({ id: '2', project: 'shop', created_at: '2026-10-23T10:00:01Z' }),
      req({ id: '3', project: 'blog', created_at: '2026-10-23T10:00:02Z' }),
      req({ id: '4', kind: 'webhook', project: '', machine_name: '', created_at: '2026-10-23T10:00:03Z' }),
      req({ id: '5', project: '', created_at: '2026-10-23T10:00:04Z' }),
    ])
    expect(g.map((x) => [x.project, x.items.map((r) => r.id)])).toEqual([
      ['shop', ['2']],
      ['blog', ['3', '1']],
      ['webhook', ['4']],
      ['其他', ['5']],
    ])
  })

  it('组有唯一的 key：项目恰好叫 webhook 或“其他”时不与 webhook 组、无项目组合并', () => {
    const g = groupByProject([
      req({ id: '1', project: 'webhook', created_at: '2026-10-23T10:00:01Z' }),
      req({ id: '2', kind: 'webhook', project: '', machine_name: '', created_at: '2026-10-23T10:00:02Z' }),
      req({ id: '3', project: '其他', created_at: '2026-10-23T10:00:03Z' }),
      req({ id: '4', project: '', created_at: '2026-10-23T10:00:04Z' }),
    ])
    expect(g.map((x) => [x.project, x.items.map((r) => r.id)])).toEqual([
      ['webhook', ['1']],
      ['webhook', ['2']],
      ['其他', ['3']],
      ['其他', ['4']],
    ])
    expect(new Set(g.map((x) => x.key)).size).toBe(4)
  })
})
