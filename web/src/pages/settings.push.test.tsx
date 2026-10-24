// 设置页通知区块的点击路径：从按钮的 onClick 经 useActions().act、usePush().enable、enablePush 到 pwa.subscribe 里的
// Notification.requestPermission()，必须同步发出（iOS 只认直接来自用户点击的授权请求）。不用 jsdom：用 SSR 跑一次组件拿到
// hook 返回的函数（SSR 下 setState 是空操作，ref 照常），再直接调用；onClick 从 NotificationView 返回的元素树里找。
import { isValidElement, type ReactElement, type ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, hub } from '../api'
import { useActions, type Actions } from '../components/Confirm'
import { browserEnv, usePush, type PushApi, type PushControl, type PushEnv } from '../push'
import { subscribe } from '../pwa'
import { StoreProvider } from '../StoreProvider'
import { NotificationView, Settings } from './Settings'

let log: string[] = []

beforeEach(() => {
  log = []
  // pwa.subscribe 用到的浏览器全局：授权请求记一笔后回“拒绝”，让流程在这里结束
  vi.stubGlobal('Notification', {
    permission: 'default',
    requestPermission: () => {
      log.push('requestPermission')
      return Promise.resolve('denied')
    },
  })
  vi.stubGlobal('navigator', { serviceWorker: {} })
  vi.stubGlobal('window', { PushManager: function PushManager() {}, Notification: function Notification() {} })
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const env: PushEnv = {
  supported: () => true,
  ios: () => false,
  standalone: () => true,
  permission: () => 'default',
  getSubscription: async () => null,
  subscribe, // 真的 pwa.subscribe：第一句是 Notification.requestPermission()
}

const api: PushApi = {
  pushVapid: async () => {
    log.push('vapid')
    return { enabled: true, public_key: 'BAEC', reason: '' }
  },
  pushSubscribe: async () => {
    log.push('hub.subscribe')
  },
  pushUnsubscribe: async () => {
    log.push('hub.unsubscribe')
  },
  pushCheck: async () => {
    log.push('hub.check')
    return { active: false }
  },
  pushTest: async () => {
    log.push('hub.test')
  },
}

/** SSR 跑一次，拿到 usePush 与 useActions 返回的东西；再 refresh 一次，让 enable 手里有公钥（挂载时的检查在 effect 里，SSR 不跑）。 */
async function harness(over: Partial<PushEnv> = {}): Promise<{ push: PushControl; actions: Actions }> {
  let push: PushControl | null = null
  let actions: Actions | null = null
  const e = { ...env, ...over }
  function H() {
    push = usePush(e, api)
    actions = useActions()
    return null
  }
  renderToStaticMarkup(<H />)
  await push!.refresh()
  log.length = 0
  return { push: push!, actions: actions! }
}

function findButton(node: ReactNode, label: string): ReactElement<{ onClick: () => void; disabled?: boolean }> | null {
  if (Array.isArray(node)) {
    for (const n of node) {
      const b = findButton(n, label)
      if (b) return b
    }
    return null
  }
  if (!isValidElement(node)) return null
  const props = node.props as { children?: ReactNode }
  if (node.type === 'button' && props.children === label) return node as ReactElement<{ onClick: () => void; disabled?: boolean }>
  return findButton(props.children, label)
}

describe('开启通知：授权请求在点击里同步发出', () => {
  it('usePush().enable() 返回之前，Notification.requestPermission() 已经调用（之前没有网络请求）', async () => {
    const { push } = await harness()
    const pending = push.enable()
    expect(log).toEqual(['requestPermission'])
    const v = await pending
    expect(v).toMatchObject({ tone: 'warn', title: '没有开启通知', text: '没有获得通知权限' })
  })

  it('“开启通知”按钮的 onClick（经 useActions().act）返回之前，授权请求已经发出', async () => {
    const { push, actions } = await harness()
    for (const status of ['off', 'stale'] as const) {
      log.length = 0
      const tree = NotificationView({ push: { ...push, status }, act: actions.act, busy: false })
      const btn = findButton(tree, status === 'off' ? '开启通知' : '重新开启通知')
      expect(btn, status).not.toBeNull()
      btn!.props.onClick()
      expect(log, status).toEqual(['requestPermission'])
      // 等这次操作结束，下一轮才能开始（act 同一时刻只做一件）
      await vi.waitFor(() => expect(log).toContain('vapid'))
      await new Promise((r) => setTimeout(r, 0))
    }
  })
})

describe('通知操作经 useActions 互斥', () => {
  it('通知操作进行中，别的操作（恢复自动审批等经 act 的）不会开始', async () => {
    const { push, actions } = await harness()
    const tree = NotificationView({ push: { ...push, status: 'off' }, act: actions.act, busy: false })
    findButton(tree, '开启通知')!.props.onClick()
    let ran = false
    void actions.act(async () => {
      ran = true
      return null
    }, '恢复失败')
    expect(ran).toBe(false)
  })

  it('测试、关闭也走 act：测试还在等 hub 时点“关闭”不会开始', async () => {
    const sub = { endpoint: 'https://push.example/a', unsubscribe: async () => true } as unknown as PushSubscription
    const { push, actions } = await harness({ getSubscription: async () => sub })
    const tree = NotificationView({ push: { ...push, status: 'on' }, act: actions.act, busy: false })
    findButton(tree, '发一条测试通知')!.props.onClick()
    findButton(tree, '关闭通知')!.props.onClick()
    await vi.waitFor(() => expect(log).toContain('vapid'))
    expect(log).toContain('hub.test')
    expect(log).not.toContain('hub.unsubscribe')
  })
})

describe('退出登录先关本机推送', () => {
  /** SSR 跑一次设置页（在路由与 store 里），拿到它返回的元素树，从中找“退出登录”按钮。 */
  function logoutButton() {
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
    const btn = findButton(tree, '退出登录')
    expect(btn).not.toBeNull()
    return btn!
  }

  function spies(unsubscribe: () => Promise<void>) {
    const sub = {
      endpoint: 'https://push.example/a',
      unsubscribe: async () => {
        log.push('local.unsubscribe')
        return true
      },
    } as unknown as PushSubscription
    vi.spyOn(browserEnv, 'getSubscription').mockImplementation(async () => {
      log.push('getSubscription')
      return sub
    })
    vi.spyOn(hub, 'pushUnsubscribe').mockImplementation(async (endpoint: string) => {
      log.push(`hub.unsubscribe ${endpoint}`)
      return unsubscribe()
    })
    vi.spyOn(hub, 'logout').mockImplementation(async () => {
      log.push('hub.logout')
    })
    // 退出后跳登录页（SSR 下 navigate 不生效，只会告警）
    vi.spyOn(console, 'warn').mockImplementation(() => {})
  }

  it('点“退出登录”：先让 hub 删掉本机订阅并退掉本机订阅，再 hub.logout()', async () => {
    spies(async () => {})
    logoutButton().props.onClick()
    await vi.waitFor(() => expect(log).toContain('hub.logout'))
    expect(log).toEqual(['getSubscription', 'hub.unsubscribe https://push.example/a', 'local.unsubscribe', 'hub.logout'])
  })

  it('hub 删订阅失败也照常退出登录', async () => {
    spies(async () => {
      throw new ApiError(0, '连不上 hub，请检查网络')
    })
    logoutButton().props.onClick()
    await vi.waitFor(() => expect(log).toContain('hub.logout'))
    expect(log).toEqual(['getSubscription', 'hub.unsubscribe https://push.example/a', 'local.unsubscribe', 'hub.logout'])
  })

  it('hub 删订阅一直不回：等满时限后照常退出登录', async () => {
    vi.useFakeTimers()
    try {
      spies(() => new Promise<void>(() => {}))
      logoutButton().props.onClick()
      await vi.advanceTimersByTimeAsync(10_000)
      expect(log).toEqual(['getSubscription', 'hub.unsubscribe https://push.example/a', 'local.unsubscribe', 'hub.logout'])
    } finally {
      vi.useRealTimers()
    }
  })
})
