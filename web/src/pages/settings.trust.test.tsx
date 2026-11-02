// 设置页的“限时放行”一节。与 inbox.test.tsx 一样用 SSR：在真实 store 的 state 上盖 status 与 trusts；
// 撤销的点击路径从设置页返回的元素树里找按钮（函数组件就地展开）直接调用 onClick。
import { isValidElement, type ReactElement, type ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { hub } from '../api'
import { setLang } from '../i18n'
import type { State } from '../store'
import { StoreProvider } from '../StoreProvider'
import type { Status, TrustView } from '../types'
import { Settings } from './Settings'

const store = vi.hoisted(() => ({ over: null as Partial<State> | null }))

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

afterEach(() => {
  store.over = null
  setLang('zh')
  vi.restoreAllMocks()
})

const status = (trusts?: number): Status => ({
  paused: false, pending: 0, rules_version: 1, machines: [], uptime: '1s', push_enabled: false, push_subscriptions: 0,
  ...(trusts === undefined ? {} : { trusts }),
})

function tv(over: Partial<TrustView> = {}): TrustView {
  return {
    id: 't1', machine_id: 'm', scope: 'session', tool: 'claude', cli_session_id: 's1', project_root: '/p/proj',
    until: '2026-11-02T12:30:00.000000000Z', created_at: '2026-11-02T10:30:00.000000000Z', created_by: 'phone', note: '',
    machine_name: 'mac', project: 'proj', remaining_seconds: 7080, ...over,
  }
}

const two = [tv(), tv({ id: 't2', scope: 'project', tool: '', cli_session_id: '', created_by: 'cli', note: '跑迁移', remaining_seconds: 1500 })]

const render = () =>
  renderToStaticMarkup(
    <MemoryRouter initialEntries={['/settings']}>
      <StoreProvider onUnauthorized={() => {}}>
        <Settings />
      </StoreProvider>
    </MemoryRouter>,
  )

type Btn = ReactElement<{ onClick: () => void; disabled?: boolean; children?: ReactNode }>

/** 元素树里第一个文字是 label 的按钮；函数组件（不带 hook 的展示组件）就地调用展开。 */
function findButton(node: ReactNode, label: string): Btn | null {
  if (Array.isArray(node)) {
    for (const n of node) {
      const b = findButton(n, label)
      if (b) return b
    }
    return null
  }
  if (!isValidElement(node)) return null
  if (typeof node.type === 'function') return findButton((node.type as (p: unknown) => ReactNode)(node.props), label)
  const props = node.props as { children?: ReactNode }
  if (node.type === 'button' && props.children === label) return node as Btn
  return findButton(props.children, label)
}

describe('设置页：限时放行', () => {
  it('列出每条：范围一行，剩余 · 谁建的 · 备注一行，右侧撤销', () => {
    store.over = { status: status(2), trusts: two }
    const html = render()
    expect(html).toContain('<h2 class="section-title">限时放行</h2>')
    expect(html).toContain('会话 Claude Code · 项目 proj · 机器 mac')
    expect(html).toContain('剩 1 小时 58 分 · 手机建')
    expect(html).toContain('项目 proj · 机器 mac')
    expect(html).toContain('剩 25 分 · 终端建 · 跑迁移')
    expect(html.match(/>撤销<\/button>/g)).toHaveLength(2)
    // 在 hub 一节之后
    expect(html.indexOf('>hub</h2>')).toBeLessThan(html.indexOf('>限时放行</h2>'))
  })

  it('空列表一句“没有生效中的放行”', () => {
    store.over = { status: status(0), trusts: [] }
    const html = render()
    expect(html).toContain('>限时放行</h2>')
    expect(html).toContain('没有生效中的放行')
    expect(html).not.toContain('>撤销</button>')
  })

  it('老 hub（status 没有 trusts）不显示这一节', () => {
    store.over = { status: status() }
    expect(render()).not.toContain('限时放行')
  })

  it('点撤销：hub.revokeTrust(id)，再重拉（放行列表跟着重拉）', async () => {
    store.over = { status: status(2), trusts: two }
    const log: string[] = []
    vi.spyOn(hub, 'revokeTrust').mockImplementation(async (id: string) => {
      log.push(`revoke ${id}`)
    })
    vi.spyOn(hub, 'inbox').mockImplementation(async () => ({ pending: [], questions: [] }))
    vi.spyOn(hub, 'status').mockImplementation(async () => status(1))
    vi.spyOn(hub, 'trusts').mockImplementation(async () => {
      log.push('trusts')
      return [two[1]!]
    })
    let tree: ReactNode = null
    function H() {
      tree = Settings()
      return null
    }
    renderToStaticMarkup(
      <MemoryRouter initialEntries={['/settings']}>
        <StoreProvider onUnauthorized={() => {}}>
          <H />
        </StoreProvider>
      </MemoryRouter>,
    )
    const btn = findButton(tree, '撤销')
    expect(btn).not.toBeNull()
    btn!.props.onClick()
    await vi.waitFor(() => expect(log).toContain('trusts'))
    expect(log[0]).toBe('revoke t1')
  })

  it('英文没有中文', () => {
    setLang('en')
    store.over = { status: status(2), trusts: [tv(), tv({ id: 't2', scope: 'project', created_by: 'env' })] }
    const html = render()
    expect(html).toContain('>Time-limited trusts</h2>')
    expect(html).toContain('Session Claude Code · Project proj · Machine mac')
    expect(html).toContain('1 hr 58 min left · From the phone')
    expect(html).toContain('>Revoke</button>')
    expect(html.replace(/中文/g, '')).not.toMatch(/[　-〿一-鿿＀-￯]/)
    store.over = { status: status(0), trusts: [] }
    expect(render()).toContain('No active trusts')
  })
})
