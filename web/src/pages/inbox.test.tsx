import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { RequireAuth, Shell } from '../App'
import { setLang } from '../i18n'
import type { PushControl } from '../push'
import type { State } from '../store'
import { StoreProvider } from '../StoreProvider'
import type { RequestView } from '../types'
import { Inbox } from './Inbox'

const push = vi.hoisted(() => ({ status: 'off' as PushControl['status'] }))
// SSR 不跑 effect，拿不到 hub 的数据：要渲染有数据的收件箱时，在真实 store 的 state 上盖几个字段
const store = vi.hoisted(() => ({ over: null as Partial<State> | null }))

vi.mock('../push', () => ({
  usePush: (): PushControl => ({
    status: push.status,
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

const render = () =>
  renderToStaticMarkup(
    <MemoryRouter>
      <StoreProvider onUnauthorized={() => {}}>
        <Inbox />
      </StoreProvider>
    </MemoryRouter>,
  )

const mac = { userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)', platform: 'MacIntel', maxTouchPoints: 0 }

beforeEach(() => {
  // 收件箱渲染时判断要不要显示 iOS 加到主屏幕的引导：给个电脑浏览器
  vi.stubGlobal('navigator', mac)
  vi.stubGlobal('window', { matchMedia: () => ({ matches: false }) })
})

afterEach(() => {
  push.status = 'off'
  store.over = null
  setLang('zh')
  vi.unstubAllGlobals()
})

function req(over: Partial<RequestView> = {}): RequestView {
  return {
    id: 'r1', machine_id: 'm', session_id: 's', cli: 'claude', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'shell', command: 'git push origin main' }, risk: 'outward', project_root: '/p/blog', engine_reason: '',
    state: 'pending', decided_by: '', decision_note: '', rule_id: '', turn_id: '', cli_session_id: '', title: '',
    created_at: '2026-10-23T10:00:00.000000000Z', expires_at: null, decided_at: null, delivered_at: null,
    machine_name: 'mac', project: 'blog', has_redacted: false, ...over,
  }
}

const cjk = /[　-〿一-鿿＀-￯]/

describe('收件箱的通知失效横幅', () => {
  it('本机有订阅但 hub 没有（stale）：红色横幅，链接去设置', () => {
    push.status = 'stale'
    const html = render()
    expect(html).toMatch(/<div class="banner warn" role="status">通知已失效，新审批不会推送到这台设备。<a href="\/settings"[^>]*>去设置重新开启<\/a><\/div>/)
  })

  it('其余状态不显示', () => {
    for (const s of ['checking', 'unsupported', 'need_standalone', 'hub_disabled', 'denied', 'off', 'on', 'unknown'] as const) {
      push.status = s
      expect(render(), s).not.toContain('通知已失效')
    }
  })
})

describe('英文（SSR）：收件箱没有中文', () => {
  it('空的收件箱', () => {
    setLang('en')
    store.over = { loaded: true, connected: true }
    const html = render()
    expect(html).toContain('<h1>Inbox</h1>')
    expect(html).toContain('aria-label="Live updates connected"')
    expect(html).toContain('<div class="empty">Nothing waiting for you</div>')
    expect(html).not.toMatch(cjk)
  })

  it('加载中', () => {
    setLang('en')
    const html = render()
    expect(html).toContain('<div class="empty">Loading…</div>')
    expect(html).toContain('aria-label="Live updates disconnected"')
    expect(html).not.toMatch(cjk)
  })

  it('断线、出错、已暂停、通知失效、iPhone 引导；等你回答与待处理（含没有项目的一组）', () => {
    setLang('en')
    vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)', platform: 'iPhone', maxTouchPoints: 5 })
    push.status = 'stale'
    store.over = {
      loaded: true,
      connected: false,
      error: 'hub said no',
      status: { paused: true, pending: 2, rules_version: 1, machines: [], uptime: '1h0m0s', push_enabled: true, push_subscriptions: 1 },
      questions: [req({ id: 'q1', kind: 'question', tool_name: '', payload: { type: '' }, risk: '', state: 'recorded', title: 'Delete the old branch?' })],
      pending: [
        req({ id: 'p1' }),
        req({ id: 'p2', kind: 'ask', tool_name: 'AskUserQuestion', payload: { type: '' }, risk: '', project: '', project_root: '', ask: { questions: [{ question: 'Apple or banana?', options: [{ label: 'Apple' }] }] } }),
      ],
    }
    const html = render()
    expect(html).toContain('Live updates disconnected. Reconnecting; new requests may show up late.')
    expect(html).toContain('>Refresh</button>')
    expect(html).toContain('hub said no')
    expect(html).toContain('>Retry</button>')
    // 句子与链接之间英文要空格
    expect(html).toMatch(/still come here\)\. <a href="\/settings"[^>]*>Go to Settings<\/a>/)
    expect(html).toMatch(/this device\. <a href="\/settings"[^>]*>Turn them back on in Settings<\/a><\/div>/)
    expect(html).toContain('Add to Home Screen')
    expect(html).toContain('>Got it</button>')
    expect(html).toContain('<h2 class="section-title">Waiting for your answer</h2>')
    expect(html).toContain('<h2 class="section-title">Pending</h2>')
    expect(html).toContain('Other<span class="count">1</span>')
    expect(html).not.toMatch(cjk)
  })
})

describe('外壳：连接中、导航、没有这个页面', () => {
  const shell = () =>
    renderToStaticMarkup(
      <MemoryRouter initialEntries={['/nope']}>
        <StoreProvider onUnauthorized={() => {}}>
          <Shell />
        </StoreProvider>
      </MemoryRouter>,
    )
  const connecting = () =>
    renderToStaticMarkup(
      <MemoryRouter>
        <RequireAuth>
          <div />
        </RequireAuth>
      </MemoryRouter>,
    )

  it('中文', () => {
    store.over = { pending: [req({ id: 'a' }), req({ id: 'b' })] }
    const html = shell()
    expect(html).toContain('<div class="empty">没有这个页面</div>')
    expect(html).toMatch(/收件箱<span class="badge" aria-label="2 条等你处理">2<\/span><\/a>/)
    for (const label of ['历史', '规则', '机器', '设置']) expect(html).toContain(`>${label}</a>`)
    expect(connecting()).toContain('<div class="empty">正在连接 hub…</div>')
  })

  it('英文：没有中文；导航里的机器是复数', () => {
    setLang('en')
    store.over = { pending: [req({ id: 'a' }), req({ id: 'b' })] }
    const html = shell()
    expect(html).toContain('<div class="empty">Page not found</div>')
    expect(html).toMatch(/Inbox<span class="badge" aria-label="2 waiting for you">2<\/span><\/a>/)
    for (const label of ['History', 'Rules', 'Machines', 'Settings']) expect(html).toContain(`>${label}</a>`)
    expect(html).not.toMatch(cjk)
    const c = connecting()
    expect(c).toContain('<div class="empty">Connecting to the hub…</div>')
    expect(c).not.toMatch(cjk)
  })
})

describe('收件箱的限时放行横幅', () => {
  const status = { paused: false, pending: 0, rules_version: 1, machines: [], uptime: '1s', push_enabled: true, push_subscriptions: 1 }
  // 今天本地 12:30 与 14:00 到期：横幅只写时分
  const today = (h: number, m: number) => {
    const d = new Date()
    d.setHours(h, m, 0, 0)
    return d.toISOString()
  }
  const tv = (id: string, until: string) => ({
    id, machine_id: 'm', scope: 'project', tool: '', cli_session_id: '', project_root: '/p/blog', until, created_at: until,
    created_by: 'phone', note: '', machine_name: 'mac', project: 'blog', remaining_seconds: 600,
  })

  it('有生效中的放行：横幅写条数与最近的到期时间，链接去设置', () => {
    store.over = { loaded: true, connected: true, status: { ...status, trusts: 2 }, trusts: [tv('a', today(14, 0)), tv('b', today(12, 30))] }
    expect(render()).toMatch(/<div class="banner info" role="status">2 个放行生效中，最近的 12:30 到期。<a href="\/settings"[^>]*>去设置<\/a><\/div>/)
  })

  it('列表还没拉到时只说条数；没有放行或老 hub 不显示', () => {
    store.over = { loaded: true, connected: true, status: { ...status, trusts: 1 }, trusts: null }
    expect(render()).toContain('1 个放行生效中。<a href="/settings"')
    for (const s of [{ ...status, trusts: 0 }, status]) {
      store.over = { loaded: true, connected: true, status: s }
      expect(render()).not.toContain('放行生效中')
    }
  })

  it('英文：句子与链接之间有空格', () => {
    setLang('en')
    store.over = { loaded: true, connected: true, status: { ...status, trusts: 2 }, trusts: [tv('b', today(12, 30))] }
    const html = render()
    expect(html).toMatch(/2 trusts active, the soonest ends at 12:30 <a href="\/settings"[^>]*>Go to Settings<\/a>/)
    expect(html).not.toMatch(cjk)
  })
})
