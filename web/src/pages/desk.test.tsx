import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { DecideSheet } from '../components/DecideSheet'
import { RequestAttrs } from '../components/RequestAttrs'
import { RequestCard } from '../components/RequestCard'
import { setLang } from '../i18n'
import type { PushControl } from '../push'
import type { State } from '../store'
import { StoreProvider } from '../StoreProvider'
import type { Desk, DeskRow, RequestView } from '../types'
import { DeskBody, DeskUnsupported, RoleCard } from './Desk'
import { Inbox } from './Inbox'
import type { FilterState } from '../desk'

const store = vi.hoisted(() => ({ over: null as Partial<State> | null }))

vi.mock('../push', () => ({
  usePush: (): PushControl => ({
    status: 'off',
    reason: '',
    publicKey: '',
    busy: false,
    enable: async () => null,
    disable: async () => null,
    test: async () => null,
    refresh: async () => {},
  }),
}))

vi.mock('../StoreProvider', async (importOriginal) => {
  const mod = await importOriginal<typeof import('../StoreProvider')>()
  return {
    ...mod,
    useStore: () => {
      const s = mod.useStore()
      return store.over ? { ...s, state: { ...s.state, ...store.over } } : s
    },
  }
})

beforeEach(() => {
  vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (Macintosh)', platform: 'MacIntel', maxTouchPoints: 0 })
  vi.stubGlobal('window', { matchMedia: () => ({ matches: false }) })
})

afterEach(() => {
  store.over = null
  setLang('zh')
  vi.unstubAllGlobals()
})

function row(over: Partial<DeskRow> = {}): DeskRow {
  return { machine_id: 'm1', machine_name: 'mac', online: true, waiting: 0, trusts_active: 0, denied: 0, auto_allowed: 0, sessions: 0, recent: [], ...over }
}

function req(over: Partial<RequestView> = {}): RequestView {
  return {
    id: 'r1', machine_id: 'm1', session_id: 's', cli: 'claude', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'shell', command: 'git push origin main' }, risk: 'outward', project_root: '/p/blog', engine_reason: '',
    state: 'pending', decided_by: '', decision_note: '', rule_id: '', turn_id: '', cli_session_id: 's', title: '',
    created_at: '2026-10-23T10:00:00.000000000Z', expires_at: null, decided_at: null, delivered_at: null,
    machine_name: 'mac', project: 'blog', has_redacted: false, ...over,
  }
}

const html = (node: React.ReactNode, url = '/') => renderToStaticMarkup(<MemoryRouter initialEntries={[url]}>{node}</MemoryRouter>)

const writer = row({
  name: '写作', note: '只改文档', ceiling: 'write_in_project', forbid_count: 2, trust_max_mins: 120, dirs: ['~/dev/*/docs'],
  waiting: 1, trusts_active: 1, denied: 3, auto_allowed: 41, sessions: 2,
  recent: [{ id: 'r9', tool_name: 'Bash', summary: 'git push origin main', risk: 'outward', state: 'denied', beyond_role: true, at: '2026-10-23T10:00:00.000000000Z' }],
})

describe('总台的角色卡', () => {
  it('名字、备注、边界一行、四个数字、最近动作与越界标记；等我 > 0 时整张卡链接到按角色筛选的收件箱', () => {
    const h = html(<RoleCard row={writer} />)
    expect(h).toMatch(/^<a class="card desk-card" href="\/inbox\?role=%E5%86%99%E4%BD%9C&amp;machine=m1"/)
    expect(h).toContain('<span class="what">写作</span>')
    expect(h).toContain('<div class="where">只改文档</div>')
    expect(h).toContain('上限 项目内改动 · 禁用 2 项 · 放行最长 2 小时')
    expect(h).toContain('<div class="stat hot"><b>1</b><span>等我</span></div>')
    expect(h).toContain('<b>1</b><span>放行中</span>')
    expect(h).toContain('<b>3</b><span>拦下</span>')
    expect(h).toContain('<b>41</b><span>自动放行</span>')
    expect(h).not.toContain('只数') // 口径只在页面顶部说一次
    expect(h).toContain('<code class="mono">git push origin main</code>')
    expect(h).toContain('已拒绝')
    expect(h).toContain('<span class="tag high">超出边界</span>')
  })
  it('等我为 0 时不是链接；离线的机器卡片变灰；未分配没有边界一行', () => {
    const h = html(<RoleCard row={row({ online: false })} />)
    expect(h).toMatch(/^<section class="card desk-card offline">/)
    expect(h).toContain('<span class="what">未分配</span>')
    expect(h).toContain('<span class="tag plain">离线</span>')
    expect(h).not.toContain('上限')
    expect(h).not.toContain('<ul class="recent">')
  })
})

describe('总台页', () => {
  it('没有任何角色：说明角色是什么，给出 railse role init；未分配一栏照常', () => {
    const d: Desk = { window_hours: 24, roles: [], unassigned: [row({ auto_allowed: 3 })] }
    const h = html(<DeskBody desk={d} />)
    expect(h).toContain('还没有角色')
    expect(h).toContain('<code class="mono">railse role init</code>')
    expect(h).toContain('<span class="what">未分配</span>')
    expect(h).not.toContain('class="group-title"') // 只有一台机器：不分组
  })
  it('多台机器按机器分组，离线的标出来', () => {
    const d: Desk = { window_hours: 24, roles: [writer, row({ machine_id: 'm2', machine_name: 'box', online: false, name: '运维' })], unassigned: [] }
    const h = html(<DeskBody desk={d} />)
    expect(h).not.toContain('还没有角色')
    expect(h).toContain('<h2 class="group-title">mac</h2>')
    expect(h).toContain('<h2 class="group-title">box<span class="count">离线</span></h2>')
    expect(h.indexOf('写作')).toBeLessThan(h.indexOf('运维'))
    // 口径只在页面顶部说一次
    expect(h.match(/拦下与自动放行只数 24 小时内/g)).toHaveLength(1)
    expect(h.indexOf('拦下与自动放行只数')).toBeLessThan(h.indexOf('写作'))
  })
  it('什么都没有：说没有动静', () => {
    expect(html(<DeskBody desk={{ window_hours: 24, roles: [], unassigned: [] }} />)).toContain('最近 24 小时没有动静')
  })
  it('英文没有中文', () => {
    setLang('en')
    const h = html(<DeskBody desk={{ window_hours: 24, roles: [{ ...writer, name: 'writer', note: 'docs only' }], unassigned: [row()] }} />)
    expect(h).toContain('Up to Write in project · 2 forbidden · Trust up to 2 hours')
    expect(h).toContain('Unassigned')
    expect(h).not.toMatch(/[　-〿一-鿿＀-￯]/)
  })
})

describe('老 hub', () => {
  it('不支持总台：说明版本过旧（有英文）', () => {
    const zh = html(<DeskUnsupported />)
    expect(zh).toContain('hub 版本过旧，不支持总台。升级 hub 后刷新本页。')
    expect(zh).toMatch(/<button type="button" class="btn">刷新<\/button>/)
    setLang('en')
    const en = html(<DeskUnsupported />)
    expect(en).toContain('This hub is too old for the desk. Upgrade the hub, then refresh this page.')
    expect(en).toContain('>Refresh</button>')
    expect(en).not.toMatch(/[　-〿一-鿿＀-￯]/)
  })
})

describe('收件箱按角色筛选', () => {
  const render = (url: string) =>
    html(
      <StoreProvider onUnauthorized={() => {}}>
        <Inbox />
      </StoreProvider>,
      url,
    )
  beforeEach(() => {
    store.over = {
      loaded: true,
      pending: [
        req({ id: 'a', role: '写作', payload: { type: 'shell', command: 'cmd-a' } }),
        req({ id: 'b', role: '', payload: { type: 'shell', command: 'cmd-b' } }),
        req({ id: 'c', role: '写作', machine_id: 'm2', machine_name: 'box', payload: { type: 'shell', command: 'cmd-c' } }),
      ],
      questions: [],
    }
  })
  const filtered = (url: string, f: FilterState) =>
    html(
      <StoreProvider onUnauthorized={() => {}}>
        <Inbox initialFiltered={f} />
      </StoreProvider>,
      url,
    )
  it('有筛选时不拿全局列表在前端筛：先显示加载中，顶部有可清除的筛选条', () => {
    const h = render('/inbox?role=&machine=m1')
    expect(h).toContain('只看：未分配 · mac')
    expect(h).toContain('清除筛选')
    expect(h).toContain('加载中…')
    for (const c of ['cmd-a', 'cmd-b', 'cmd-c']) expect(h).not.toContain(c)
  })
  it('筛选结果来自后端（hub 已在截断之前筛好）', () => {
    const h = filtered('/inbox?role=&machine=m1', {
      status: 'ok',
      inbox: { pending: [req({ id: 'g', role: '已删', payload: { type: 'shell', command: 'cmd-gone' } })], questions: [] },
    })
    expect(h).toContain('cmd-gone')
    expect(h).not.toContain('cmd-a')
    expect(h).toContain('只看：未分配 · mac')
  })
  it('筛选结果取失败：错误与重试，不展示列表', () => {
    const h = filtered('/inbox?machine=m1', { status: 'error', message: '连不上 hub' })
    expect(h).toContain('连不上 hub')
    expect(h).toContain('>重试</button>')
    expect(h).not.toContain('cmd-a')
    expect(h).not.toContain('没有等你处理的事')
  })
  it('筛选后什么都没有：照常说没有等你处理的事', () => {
    expect(filtered('/inbox?role=%E8%BF%90%E7%BB%B4', { status: 'ok', inbox: { pending: [], questions: [] } })).toContain('没有等你处理的事')
  })
  it('没有筛选参数时全显示，没有筛选条', () => {
    const h = render('/')
    for (const c of ['cmd-a', 'cmd-b', 'cmd-c']) expect(h).toContain(c)
    expect(h).not.toContain('filter-bar')
    expect(h).not.toContain('还有更早的待批')
  })
  it('全局列表被截断：底部提示还有更早的待批，给出按机器查看的入口', () => {
    store.over = {
      ...store.over,
      pendingTruncated: true,
      status: { paused: false, pending: 600, rules_version: 1, uptime: '1m', push_enabled: false, push_subscriptions: 0,
        machines: [{ id: 'm1', name: 'mac', online: true, last_seen: null, revoked: false }, { id: 'm2', name: 'box', online: false, last_seen: null, revoked: false }, { id: 'm3', name: 'gone', online: false, last_seen: null, revoked: true }] },
    }
    const h = render('/')
    expect(h).toContain('还有更早的待批没显示。按机器查看：')
    expect(h).toContain('<a href="/inbox?machine=m1"')
    expect(h).toContain('<a href="/inbox?machine=m2"')
    expect(h).not.toContain('machine=m3')
    setLang('en')
    expect(render('/')).toContain('Older pending requests aren&#x27;t shown. View by machine:')
  })
})

describe('卡片、详情与批准面板的角色与越界', () => {
  it('卡片：有角色写在最后一行；越界时多一行红色标签', () => {
    const plain = html(<RequestCard request={req({ role: '开发' })} />)
    expect(plain).toContain('<div class="where">Claude Code · mac · blog · 角色「开发」</div>')
    expect(plain).not.toContain('role-line')
    const beyond = html(<RequestCard request={req({ role: '写作', beyond_role: true })} />)
    expect(beyond).toContain('<div class="where">Claude Code · mac · blog</div>')
    expect(beyond).toContain('<div class="role-line"><span class="tag high">超出角色「写作」的边界</span></div>')
    expect(html(<RequestCard request={req()} />)).toContain('<div class="where">Claude Code · mac · blog</div>')
  })
  it('详情属性：角色一行，越界带标签；没有角色不显示', () => {
    expect(renderToStaticMarkup(<RequestAttrs request={req({ role: '写作', beyond_role: true })} audit={[]} />)).toContain(
      '<dt>角色</dt><dd>写作<span class="tag high attr-tag">超出边界</span></dd>',
    )
    expect(renderToStaticMarkup(<RequestAttrs request={req()} audit={[]} />)).not.toContain('<dt>角色</dt>')
  })
  it('批准面板：越界时没有“记住”与“接下来一段时间都放行”，原位说明；拒绝面板照常能记住', () => {
    const r = req({ role: '写作', beyond_role: true, can_trust: true })
    const allow = renderToStaticMarkup(<DecideSheet request={r} action="allow" onClose={() => {}} onDone={() => {}} />)
    expect(allow).toContain('超出角色「写作」的边界：这次只能单次批准，不能记住，也不能限时放行。')
    expect(allow).not.toContain('记住：')
    expect(allow).not.toContain('接下来一段时间都放行')
    expect(allow).not.toContain('type="checkbox"')
    const deny = renderToStaticMarkup(<DecideSheet request={r} action="deny" onClose={() => {}} onDone={() => {}} />)
    expect(deny).toContain('记住：以后同类的自动拒绝')
    expect(deny).not.toContain('beyond-note')
    // 没越界、能放行：两个开关都在
    const ok = renderToStaticMarkup(<DecideSheet request={req({ can_trust: true })} action="allow" onClose={() => {}} onDone={() => {}} />)
    expect(ok).toContain('记住：以后同类的自动放行')
    expect(ok).toContain('接下来一段时间都放行')
  })
})
