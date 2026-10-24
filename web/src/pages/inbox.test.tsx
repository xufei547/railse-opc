import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { PushControl } from '../push'
import { StoreProvider } from '../StoreProvider'
import { Inbox } from './Inbox'

const push = vi.hoisted(() => ({ status: 'off' as PushControl['status'] }))

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

const render = () =>
  renderToStaticMarkup(
    <MemoryRouter>
      <StoreProvider onUnauthorized={() => {}}>
        <Inbox />
      </StoreProvider>
    </MemoryRouter>,
  )

beforeEach(() => {
  // 收件箱渲染时判断要不要显示 iOS 加到主屏幕的引导：给个电脑浏览器
  vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)', platform: 'MacIntel', maxTouchPoints: 0 })
  vi.stubGlobal('window', { matchMedia: () => ({ matches: false }) })
})

afterEach(() => {
  push.status = 'off'
  vi.unstubAllGlobals()
})

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
