import { describe, expect, it, vi } from 'vitest'
import { Live, nextDelay, wsURL, type SocketLike } from './ws'

class FakeSocket implements SocketLike {
  onopen: ((ev: unknown) => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onclose: ((ev: { code: number }) => void) | null = null
  onerror: ((ev: unknown) => void) | null = null
  closed = false
  constructor(public url: string) {}
  close() {
    this.closed = true
  }
}

vi.stubGlobal('window', { location: { protocol: 'https:', host: 'hub.example.com' } })

describe('nextDelay', () => {
  it('按退避序列并封顶', () => {
    expect([0, 1, 2, 5, 9].map((a) => nextDelay(a))).toEqual([1000, 2000, 4000, 30000, 30000])
  })
})

describe('wsURL', () => {
  it('按页面协议选 wss 或 ws', () => {
    expect(wsURL({ protocol: 'https:', host: 'h' })).toBe('wss://h/ws/client')
    expect(wsURL({ protocol: 'http:', host: 'h:8787' })).toBe('ws://h:8787/ws/client')
  })
})

describe('Live', () => {
  function setup(opts: { failMake?: number } = {}) {
    const sockets: FakeSocket[] = []
    const timers: { fn: () => void; ms: number; id: number }[] = []
    const cleared: unknown[] = []
    const onFrame = vi.fn()
    const onOpen = vi.fn()
    const onDown = vi.fn()
    let failMake = opts.failMake ?? 0
    const live = new Live({
      onFrame,
      onOpen,
      onDown,
      makeSocket: (url) => {
        if (failMake > 0) {
          failMake--
          throw new Error('SecurityError')
        }
        const s = new FakeSocket(url)
        sockets.push(s)
        return s
      },
      setTimer: (fn, ms) => {
        const id = timers.length + 1
        timers.push({ fn, ms, id })
        return id
      },
      clearTimer: (t) => {
        cleared.push(t)
      },
    })
    return { live, sockets, timers, cleared, onFrame, onOpen, onDown }
  }

  it('连上后分发帧，断开后按退避重连并在重连时再次 onOpen', () => {
    const { live, sockets, timers, onFrame, onOpen, onDown } = setup()
    live.start()
    expect(sockets).toHaveLength(1)
    expect(sockets[0]!.url).toBe('wss://hub.example.com/ws/client')
    sockets[0]!.onopen?.({})
    expect(onOpen).toHaveBeenCalledTimes(1)
    sockets[0]!.onmessage?.({ data: JSON.stringify({ type: 'status', data: { paused: true } }) })
    sockets[0]!.onmessage?.({ data: 'not json' })
    expect(onFrame).toHaveBeenCalledTimes(1)
    expect(onFrame.mock.calls[0]?.[0]).toEqual({ type: 'status', data: { paused: true } })
    sockets[0]!.onclose?.({ code: 1006 })
    expect(onDown).toHaveBeenCalledWith(1006)
    expect(timers[0]?.ms).toBe(1000)
    timers[0]!.fn()
    expect(sockets).toHaveLength(2)
    sockets[1]!.onclose?.({ code: 1006 })
    expect(timers[1]?.ms).toBe(2000)
    timers[1]!.fn()
    sockets[2]!.onopen?.({})
    expect(onOpen).toHaveBeenCalledTimes(2)
    sockets[2]!.onclose?.({ code: 1006 })
    expect(timers[2]?.ms).toBe(1000) // 连上过，退避归零
  })

  it('不认识的帧类型与缺 type 的帧不分发', () => {
    const { live, sockets, onFrame } = setup()
    live.start()
    sockets[0]!.onopen?.({})
    sockets[0]!.onmessage?.({ data: JSON.stringify({ type: 'hello', data: {} }) })
    sockets[0]!.onmessage?.({ data: JSON.stringify({ data: {} }) })
    sockets[0]!.onmessage?.({ data: 'null' })
    sockets[0]!.onmessage?.({ data: JSON.stringify({ type: 'rules_updated', data: { version: 2 } }) })
    expect(onFrame).toHaveBeenCalledTimes(1)
    expect(onFrame.mock.calls[0]?.[0]).toEqual({ type: 'rules_updated', data: { version: 2 } })
  })

  it('1008 不再重连', () => {
    const { live, sockets, timers, onDown } = setup()
    live.start()
    sockets[0]!.onclose?.({ code: 1008 })
    expect(onDown).toHaveBeenCalledWith(1008)
    expect(timers).toHaveLength(0)
    live.wake()
    expect(sockets).toHaveLength(1)
  })

  it('stop 后不再重连并关闭连接', () => {
    const { live, sockets, timers, onDown } = setup()
    live.start()
    live.stop()
    expect(sockets[0]!.closed).toBe(true)
    sockets[0]!.onclose?.({ code: 1000 })
    expect(timers).toHaveLength(0)
    expect(onDown).not.toHaveBeenCalled()
  })

  it('stop 取消等待中的重连', () => {
    const { live, sockets, timers, cleared } = setup()
    live.start()
    sockets[0]!.onclose?.({ code: 1006 })
    live.stop()
    expect(cleared).toEqual([timers[0]!.id])
  })

  it('建连接抛错时按断开处理并退避重试', () => {
    const { live, sockets, timers, onDown } = setup({ failMake: 1 })
    live.start()
    expect(sockets).toHaveLength(0)
    expect(onDown).toHaveBeenCalledWith(undefined)
    expect(timers[0]?.ms).toBe(1000)
    timers[0]!.fn()
    expect(sockets).toHaveLength(1)
  })

  it('wake：等重连时立即重连并把退避归零；连着时什么也不做', () => {
    const { live, sockets, timers, cleared } = setup()
    live.start()
    live.wake() // 连接中，不另起
    expect(sockets).toHaveLength(1)
    sockets[0]!.onclose?.({ code: 1006 })
    timers[0]!.fn()
    sockets[1]!.onclose?.({ code: 1006 })
    expect(timers[1]?.ms).toBe(2000)
    live.wake()
    expect(cleared).toEqual([timers[1]!.id])
    expect(sockets).toHaveLength(3)
    sockets[2]!.onclose?.({ code: 1006 })
    expect(timers[2]?.ms).toBe(1000)
  })
})
