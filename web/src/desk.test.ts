import { afterEach, describe, expect, it } from 'vitest'
import { applyFilter, boundaryLine, deskGroups, filterLabel, inboxHref, initialRoles, knownRoles, parseFilter, roleText, rolesReducer, unassignedLists, type KnownRoles } from './desk'
import { setLang } from './i18n'
import type { Desk, DeskRow } from './types'

function row(over: Partial<DeskRow> = {}): DeskRow {
  return { machine_id: 'm1', machine_name: 'mac', online: true, waiting: 0, trusts_active: 0, denied: 0, auto_allowed: 0, sessions: 0, recent: [], ...over }
}

afterEach(() => setLang('zh'))

describe('boundaryLine', () => {
  it('上限、禁用、放行上限，没设的不显示', () => {
    expect(boundaryLine(row({ name: '写作', ceiling: 'write_in_project', forbid_count: 2, trust_max_mins: 120 }))).toBe('上限 项目内改动 · 禁用 2 项 · 放行最长 2 小时')
    expect(boundaryLine(row({ name: '开发', ceiling: 'network', trust_max_mins: 1440 }))).toBe('上限 联网 · 放行最长 24 小时')
    expect(boundaryLine(row({ name: '运维', ceiling: 'destructive', trust_max_mins: 45 }))).toBe('上限 不可逆 · 放行最长 45 分钟')
    expect(boundaryLine(row({ name: '只读', forbid_count: 1, trust_max_mins: -1 }))).toBe('禁用 1 项 · 不许限时放行')
    expect(boundaryLine(row({ name: '空' }))).toBe('')
  })
  it('英文', () => {
    setLang('en')
    expect(boundaryLine(row({ name: 'writer', ceiling: 'write_in_project', forbid_count: 2, trust_max_mins: 120 }))).toBe(
      'Up to Write in project · 2 forbidden · Trust up to 2 hours',
    )
    expect(boundaryLine(row({ name: 'x', forbid_count: 1, trust_max_mins: -1 }))).toBe('1 forbidden · No time-limited trust')
    expect(boundaryLine(row({ name: 'x', trust_max_mins: 180 }))).toBe('Trust up to 3 hr')
  })
})

describe('deskGroups', () => {
  it('按机器分组，角色在前、未分配在后；机器按第一次出现的先后', () => {
    const d: Desk = {
      window_hours: 24,
      roles: [row({ name: '开发' }), row({ name: '写作' }), row({ machine_id: 'm2', machine_name: 'box', online: false, name: '运维' })],
      unassigned: [row(), row({ machine_id: 'm3', machine_name: 'old' })],
    }
    const g = deskGroups(d)
    expect(g.map((x) => [x.machine_id, x.machine_name, x.online, x.rows.map((r) => r.name ?? '')])).toEqual([
      ['m1', 'mac', true, ['开发', '写作', '']],
      ['m2', 'box', false, ['运维']],
      ['m3', 'old', true, ['']],
    ])
  })
})

describe('筛选', () => {
  it('inboxHref：角色名与机器进查询串，未分配的角色为空串', () => {
    expect(inboxHref(row({ name: '写 作&x' }))).toBe('/inbox?role=%E5%86%99+%E4%BD%9C%26x&machine=m1')
    expect(inboxHref(row())).toBe('/inbox?role=&machine=m1')
  })
  it('parseFilter：没有参数为 null；role 在但为空是未分配', () => {
    expect(parseFilter(new URLSearchParams(''))).toBeNull()
    expect(parseFilter(new URLSearchParams('role=&machine=m1'))).toEqual({ role: '', machine: 'm1' })
    expect(parseFilter(new URLSearchParams('role=写作'))).toEqual({ role: '写作', machine: null })
    expect(parseFilter(new URLSearchParams('machine=m2'))).toEqual({ role: null, machine: 'm2' })
    expect(parseFilter(new URLSearchParams(inboxHref(row({ name: '写作' })).split('?')[1]))).toEqual({ role: '写作', machine: 'm1' })
  })
  it('applyFilter：按角色（没有键的算未分配）与机器', () => {
    const list = [
      { id: 'a', role: '写作', machine_id: 'm1' },
      { id: 'b', role: '', machine_id: 'm1' },
      { id: 'c', machine_id: 'm1' },
      { id: 'd', role: '写作', machine_id: 'm2' },
    ]
    const ids = (f: ReturnType<typeof parseFilter>) => applyFilter(list, f).map((x) => x.id)
    expect(ids(null)).toEqual(['a', 'b', 'c', 'd'])
    expect(ids({ role: '写作', machine: 'm1' })).toEqual(['a'])
    expect(ids({ role: '', machine: 'm1' })).toEqual(['b', 'c'])
    expect(ids({ role: '写作', machine: null })).toEqual(['a', 'd'])
    expect(ids({ role: null, machine: 'm2' })).toEqual(['d'])
  })
  it('filterLabel', () => {
    expect(filterLabel({ role: '写作', machine: 'm1' }, 'mac')).toBe('角色「写作」 · mac')
    expect(filterLabel({ role: '', machine: 'm1' }, '')).toBe('未分配')
    expect(filterLabel({ role: null, machine: 'm1' }, 'mac')).toBe('mac')
    setLang('en')
    expect(filterLabel({ role: '', machine: 'm1' }, 'mac')).toBe('Unassigned · mac')
  })
})

describe('roleText', () => {
  it('越界、有角色、没有', () => {
    expect(roleText({ role: '写作', beyond_role: true })).toEqual({ text: '超出角色「写作」的边界', beyond: true })
    expect(roleText({ role: '', beyond_role: true })).toEqual({ text: '超出角色的边界', beyond: true })
    expect(roleText({ role: '开发', beyond_role: false })).toEqual({ text: '角色「开发」', beyond: false })
    expect(roleText({ role: '' })).toBeNull()
    expect(roleText({})).toBeNull()
    setLang('en')
    expect(roleText({ role: 'writer', beyond_role: true })).toEqual({ text: 'Beyond the limits of role "writer"', beyond: true })
  })
})

describe('未分配 = 角色为空，或不在该机器当前角色表里', () => {
  const list = [
    { id: 'a', role: '写作', machine_id: 'm1' },
    { id: 'b', role: '', machine_id: 'm1' },
    { id: 'c', role: '已删', machine_id: 'm1' },
    { id: 'd', role: '写作', machine_id: 'm2' }, // m2 没有“写作”：对 m2 也是未分配
    { id: 'e', role: '运维', machine_id: 'm2' },
  ]
  const known = { m1: ['写作'], m2: ['运维'] }
  const ids = (f: Parameters<typeof applyFilter>[1], k?: Record<string, string[]>) => applyFilter(list, f, k).map((x) => x.id)
  it('有角色表时按它判', () => {
    expect(ids({ role: '', machine: 'm1' }, known)).toEqual(['b', 'c'])
    expect(ids({ role: '', machine: null }, known)).toEqual(['b', 'c', 'd'])
    expect(ids({ role: '写作', machine: 'm1' }, known)).toEqual(['a'])
  })
  it('还没有角色表时只认空串', () => {
    expect(ids({ role: '', machine: 'm1' })).toEqual(['b'])
  })
  it('knownRoles 从总台取每台机器的角色名', () => {
    expect(knownRoles({ window_hours: 24, roles: [row({ name: '写作' }), row({ name: '开发' }), row({ machine_id: 'm2', name: '运维' })], unassigned: [row({ machine_id: 'm3' })] })).toEqual({
      m1: ['写作', '开发'],
      m2: ['运维'],
      m3: [],
    })
  })
})

describe('收件箱“未分配”筛选的角色表快照', () => {
  const list = [
    { id: 'a', role: '写作', machine_id: 'm1' },
    { id: 'b', role: '', machine_id: 'm1' },
    { id: 'c', role: '已删', machine_id: 'm1' },
    { id: 'n1', role: '新角色', machine_id: 'm9' }, // 快照之后才接入的机器
    { id: 'n2', role: '', machine_id: 'm9' },
  ]
  const ids = (known: KnownRoles) => applyFilter(list, { role: '', machine: null }, known).map((x) => x.id)

  it('多机器：快照里没有的机器不当成空角色表，它的请求只按“角色为空”判未分配', () => {
    expect(ids({ m1: ['写作'] })).toEqual(['b', 'c', 'n2'])
  })

  it('陈旧：角色新增、删除后按最新的快照判', () => {
    let s = rolesReducer(initialRoles, { type: 'enter' })
    s = rolesReducer(s, { type: 'data', known: { m1: ['写作'] } })
    expect(unassignedLists(s, list, { role: '', machine: 'm1' })).toEqual({ kind: 'list', items: [list[1], list[2]] })
    // 新增了“已删”这个角色（比如改名回来）、删掉了“写作”
    s = rolesReducer(s, { type: 'data', known: { m1: ['已删'] } })
    expect(unassignedLists(s, list, { role: '', machine: 'm1' })).toEqual({ kind: 'list', items: [list[0], list[1]] })
  })

  it('进入筛选时先显示加载中，取到完整快照前不展示部分结果', () => {
    const s = rolesReducer(rolesReducer(initialRoles, { type: 'data', known: { m1: [] } }), { type: 'enter' })
    expect(unassignedLists(s, list, { role: '', machine: 'm1' })).toEqual({ kind: 'loading' })
  })

  it('取失败：显示错误，不展示部分结果；重试回到加载中；之后成功照常', () => {
    let s = rolesReducer(initialRoles, { type: 'enter' })
    s = rolesReducer(s, { type: 'error', message: '连不上 hub' })
    expect(unassignedLists(s, list, { role: '', machine: 'm1' })).toEqual({ kind: 'error', message: '连不上 hub' })
    // 成功过之后再失败也一样不展示（快照可能已过时）
    s = rolesReducer(rolesReducer(s, { type: 'data', known: { m1: [] } }), { type: 'error', message: '超时' })
    expect(unassignedLists(s, list, { role: '', machine: 'm1' }).kind).toBe('error')
    s = rolesReducer(s, { type: 'retry' })
    expect(unassignedLists(s, list, { role: '', machine: 'm1' })).toEqual({ kind: 'loading' })
    s = rolesReducer(s, { type: 'data', known: { m1: ['写作', '已删'] } })
    expect(unassignedLists(s, list, { role: '', machine: 'm1' })).toEqual({ kind: 'list', items: [list[1]] })
  })

  it('老 hub（没有总台）：没有角色，只认空串', () => {
    const s = rolesReducer(rolesReducer(initialRoles, { type: 'enter' }), { type: 'unsupported' })
    expect(unassignedLists(s, list, { role: '', machine: null })).toEqual({ kind: 'list', items: [list[1], list[4]] })
  })

  it('不是“未分配”筛选时不看快照', () => {
    expect(unassignedLists(initialRoles, list, { role: '写作', machine: null })).toEqual({ kind: 'list', items: [list[0]] })
    expect(unassignedLists(initialRoles, list, null)).toEqual({ kind: 'list', items: list })
  })
})
