import { afterEach, describe, expect, it, vi } from 'vitest'
import { closeStale, staleCloser, swNotifications, type NotificationSource } from './notifications'

/** 假的注册：getNotifications 回给定 tag 的通知，close 记进 closed。 */
function fakeSource(tags: string[]) {
  const closed: string[] = []
  const src: NotificationSource = {
    getNotifications: () => Promise.resolve(tags.map((tag) => ({ tag, close: () => void closed.push(tag) }))),
  }
  return { src, closed }
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('closeStale', () => {
  it('只关 tag 不在待决集合里的（含测试通知与兜底通知）', async () => {
    const { src, closed } = fakeSource(['a', 'b', 'railse-test', 'c', 'railse'])
    expect(await closeStale(new Set(['a', 'c']), () => Promise.resolve(src))).toBe(3)
    expect(closed).toEqual(['b', 'railse-test', 'railse'])
  })

  it('待决集合为空：全部关掉；全在集合里：一条不关', async () => {
    const all = fakeSource(['a', 'b'])
    expect(await closeStale(new Set(), () => Promise.resolve(all.src))).toBe(2)
    expect(all.closed).toEqual(['a', 'b'])
    const none = fakeSource(['a', 'b'])
    expect(await closeStale(new Set(['a', 'b', 'z']), () => Promise.resolve(none.src))).toBe(0)
    expect(none.closed).toEqual([])
  })

  it('没有 Service Worker、没有注册、没有 getNotifications：静默跳过', async () => {
    await expect(closeStale(new Set(), () => Promise.resolve(null))).resolves.toBe(0)
    vi.stubGlobal('navigator', {})
    await expect(swNotifications()).resolves.toBeNull()
    await expect(closeStale(new Set())).resolves.toBe(0)
    vi.stubGlobal('navigator', { serviceWorker: { getRegistration: () => Promise.resolve(undefined) } })
    await expect(swNotifications()).resolves.toBeNull()
    vi.stubGlobal('navigator', { serviceWorker: { getRegistration: () => Promise.resolve({}) } })
    await expect(swNotifications()).resolves.toBeNull()
    await expect(closeStale(new Set())).resolves.toBe(0)
  })

  it('出错不抛：getRegistration、getNotifications 被拒，某条 close 抛错时其余照关', async () => {
    vi.stubGlobal('navigator', { serviceWorker: { getRegistration: () => Promise.reject(new Error('SecurityError')) } })
    await expect(closeStale(new Set())).resolves.toBe(0)
    const broken: NotificationSource = { getNotifications: () => Promise.reject(new Error('no')) }
    await expect(closeStale(new Set(), () => Promise.resolve(broken))).resolves.toBe(0)
    const closed: string[] = []
    const mixed: NotificationSource = {
      getNotifications: () =>
        Promise.resolve([
          {
            tag: 'x',
            close: () => {
              throw new Error('gone')
            },
          },
          { tag: 'y', close: () => void closed.push('y') },
        ]),
    }
    await expect(closeStale(new Set(), () => Promise.resolve(mixed))).resolves.toBe(1)
    expect(closed).toEqual(['y'])
  })

  it('默认经 navigator.serviceWorker.getRegistration 拿注册', async () => {
    const { src, closed } = fakeSource(['old', 'live'])
    vi.stubGlobal('navigator', { serviceWorker: { getRegistration: () => Promise.resolve(src) } })
    expect(await closeStale(new Set(['live']))).toBe(1)
    expect(closed).toEqual(['old'])
  })
})

describe('staleCloser：同一秒内至多收一次', () => {
  it('空闲时立即收；间隔内再来的只在间隔末尾用最新的集合补收一次', () => {
    vi.useFakeTimers()
    const calls: string[][] = []
    const run = staleCloser((w) => {
      calls.push([...w].sort())
      return Promise.resolve()
    }, 1000)
    run(['a'])
    expect(calls).toEqual([['a']])
    run(['a', 'b'])
    run(['b'])
    expect(calls).toHaveLength(1)
    vi.advanceTimersByTime(999)
    expect(calls).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(calls).toEqual([['a'], ['b']])
    // 补收之后的一秒内仍然节流
    run(['c'])
    expect(calls).toHaveLength(2)
    vi.advanceTimersByTime(1000)
    expect(calls).toEqual([['a'], ['b'], ['c']])
    // 间隔里没人再要：到点不补收；空闲之后再来的立即收
    vi.advanceTimersByTime(5000)
    expect(calls).toHaveLength(3)
    run([])
    expect(calls).toEqual([['a'], ['b'], ['c'], []])
  })
})
