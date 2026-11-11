import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from './api'
import { createDeskLoader } from './deskLoader'
import type { Desk } from './types'

// 总台的重取：事件密集时不能饿死（在途的结果照常采用，期间来的事件合并成结束后的一次），节流 1 秒，30 秒轮询，卸载才丢弃；
// 老 hub（404）说一次版本过旧并停止轮询。

function deferred<T>() {
  let resolve!: (v: T) => void
  let reject!: (e: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

const desk = (n: number): Desk => ({ window_hours: 24, roles: [], unassigned: [{ machine_id: `m${n}`, machine_name: '', online: true, waiting: n, trusts_active: 0, denied: 0, auto_allowed: 0, sessions: 0, recent: [] }] })

function harness() {
  const calls: ReturnType<typeof deferred<Desk>>[] = []
  const got: Desk[] = []
  const errors: unknown[] = []
  let unsupported = 0
  const loader = createDeskLoader({
    fetch: () => {
      const d = deferred<Desk>()
      calls.push(d)
      return d.promise
    },
    onData: (d) => got.push(d),
    onError: (e) => errors.push(e),
    onUnsupported: () => unsupported++,
    throttleMs: 1000,
    pollMs: 30_000,
  })
  return { loader, calls, got, errors, unsupported: () => unsupported }
}

beforeEach(() => vi.useFakeTimers())
afterEach(() => vi.useRealTimers())

describe('createDeskLoader', () => {
  it('事件不停地来：在途的结果照常采用，期间的事件合并成结束后的一次', async () => {
    const h = harness()
    h.loader.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(h.calls).toHaveLength(1)
    // 在途期间来了一串事件
    for (let i = 0; i < 20; i++) {
      h.loader.request()
      await vi.advanceTimersByTimeAsync(100)
    }
    expect(h.calls).toHaveLength(1)
    h.calls[0]!.resolve(desk(1))
    await vi.advanceTimersByTimeAsync(0)
    expect(h.got).toEqual([desk(1)]) // 没有被作废
    await vi.advanceTimersByTimeAsync(1000)
    expect(h.calls).toHaveLength(2) // 只补一次
    // 第二次在途时又来事件：照样采用，再补一次
    h.loader.request()
    h.calls[1]!.resolve(desk(2))
    await vi.advanceTimersByTimeAsync(1000)
    expect(h.got).toEqual([desk(1), desk(2)])
    expect(h.calls).toHaveLength(3)
    h.calls[2]!.resolve(desk(3))
    await vi.advanceTimersByTimeAsync(0)
    expect(h.got.at(-1)).toEqual(desk(3))
    h.loader.stop()
  })

  it('节流：空闲时一阵事件只取一次，两次之间至少隔 1 秒', async () => {
    const h = harness()
    h.loader.start()
    await vi.advanceTimersByTimeAsync(0)
    h.calls[0]!.resolve(desk(1))
    await vi.advanceTimersByTimeAsync(0)
    h.loader.request()
    h.loader.request()
    h.loader.request()
    await vi.advanceTimersByTimeAsync(999)
    expect(h.calls).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(h.calls).toHaveLength(2)
    h.loader.stop()
  })

  it('每 30 秒轮询一次；卸载后在途结果丢弃、不再轮询', async () => {
    const h = harness()
    h.loader.start()
    await vi.advanceTimersByTimeAsync(0)
    h.calls[0]!.resolve(desk(1))
    await vi.advanceTimersByTimeAsync(30_000)
    expect(h.calls).toHaveLength(2)
    h.loader.stop()
    h.calls[1]!.resolve(desk(2))
    await vi.advanceTimersByTimeAsync(120_000)
    expect(h.got).toEqual([desk(1)])
    expect(h.calls).toHaveLength(2)
  })

  it('出错照常报，下次事件再取', async () => {
    const h = harness()
    h.loader.start()
    await vi.advanceTimersByTimeAsync(0)
    h.calls[0]!.reject(new ApiError(0, '连不上'))
    await vi.advanceTimersByTimeAsync(0)
    expect(h.errors).toHaveLength(1)
    h.loader.request()
    await vi.advanceTimersByTimeAsync(1000)
    expect(h.calls).toHaveLength(2)
    h.loader.stop()
  })

  it('老 hub 回 404：说一次不支持，停止轮询，之后的事件也不再取', async () => {
    const h = harness()
    h.loader.start()
    await vi.advanceTimersByTimeAsync(0)
    h.calls[0]!.reject(new ApiError(404, '没有这个接口'))
    await vi.advanceTimersByTimeAsync(0)
    expect(h.unsupported()).toBe(1)
    expect(h.errors).toHaveLength(0)
    h.loader.request()
    await vi.advanceTimersByTimeAsync(120_000)
    expect(h.calls).toHaveLength(1)
    h.loader.stop()
  })

  it('401 不报错（登录失效回调已在跳登录页）', async () => {
    const h = harness()
    h.loader.start()
    await vi.advanceTimersByTimeAsync(0)
    h.calls[0]!.reject(new ApiError(401, '要登录'))
    await vi.advanceTimersByTimeAsync(0)
    expect(h.errors).toHaveLength(0)
    h.loader.stop()
  })

  it('stop 之后再 start（React 严格模式的卸载再挂载）照常工作', async () => {
    const h = harness()
    h.loader.start()
    h.loader.stop()
    h.loader.start()
    await vi.advanceTimersByTimeAsync(0)
    expect(h.calls).toHaveLength(2) // 第一轮挂载的那次作废，新挂载立即再取
    h.calls[0]!.resolve(desk(4))
    h.calls[1]!.resolve(desk(5))
    await vi.advanceTimersByTimeAsync(0)
    expect(h.got).toEqual([desk(5)])
    h.loader.stop()
  })
})
