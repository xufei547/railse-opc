// 总台页与收件箱筛选结果的重取调度（纯逻辑，Desk.tsx、Inbox.tsx 原样使用；不依赖 React，便于用假定时器测）。
// 规则：同一时刻至多一次在取；在途期间来的事件只标“脏”，在途的结果照常采用，结束后若脏再取一次（事件再密也不会饿死）；
// 两次开始之间至少隔 throttleMs；开着时每 pollMs 再取一次（机器上下线、角色表变化没有实时事件）；
// stop（卸载）之后在途的结果丢弃；老 hub 回 404 时调 onUnsupported，停止轮询，之后的事件也不再取，再次 start 时再调一次 onUnsupported；401 不报（登录失效回调已在跳登录页）。

import { ApiError } from './api'

export interface DeskLoaderOptions<T> {
  fetch: () => Promise<T>
  onData: (d: T) => void
  onError: (e: unknown) => void
  onUnsupported: () => void
  throttleMs: number
  pollMs: number
}

export interface DeskLoader {
  /** 挂载时调：开始轮询并立即要一次。 */
  start(): void
  /** 有变化时调：空闲就（按节流）取一次，在途就标脏。 */
  request(): void
  /** 卸载时调：停掉定时器，在途的结果丢弃。可以再 start。 */
  stop(): void
}

export function createDeskLoader<T>(o: DeskLoaderOptions<T>): DeskLoader {
  let gen = 0 // 每次 stop 加一：上一轮挂载的在途结果据此丢弃
  let running = false
  let inflight = false
  let dirty = false
  let unsupported = false
  let last = -Infinity
  let timer: ReturnType<typeof setTimeout> | null = null
  let poll: ReturnType<typeof setInterval> | null = null

  const clearPoll = () => {
    if (poll !== null) clearInterval(poll)
    poll = null
  }

  const run = () => {
    timer = null
    if (!running || unsupported) return
    const mine = gen
    inflight = true
    dirty = false
    last = Date.now()
    o.fetch()
      .then(
        (d) => {
          if (mine === gen) o.onData(d)
        },
        (e: unknown) => {
          if (mine !== gen) return
          if (e instanceof ApiError && e.status === 404) {
            unsupported = true
            clearPoll()
            o.onUnsupported()
            return
          }
          if (e instanceof ApiError && e.status === 401) return
          o.onError(e)
        },
      )
      .finally(() => {
        if (mine !== gen) return
        inflight = false
        if (dirty) request()
      })
  }

  const request = () => {
    if (!running || unsupported) return
    if (inflight) {
      dirty = true
      return
    }
    if (timer !== null) return // 已排好，节流期内的请求并进这一次
    const wait = last + o.throttleMs - Date.now()
    if (wait <= 0) run()
    else timer = setTimeout(run, wait)
  }

  return {
    start() {
      if (running) return
      running = true
      inflight = false
      dirty = false
      last = -Infinity // 新挂载要立刻有数据，不跟上一轮的节流
      if (unsupported) {
        o.onUnsupported() // 已知不支持：再告诉页面一次（离开再回来的页面状态是新的），不再请求
        return
      }
      poll = setInterval(request, o.pollMs)
      request()
    },
    request,
    stop() {
      running = false
      gen++
      inflight = false
      dirty = false
      if (timer !== null) clearTimeout(timer)
      timer = null
      clearPoll()
    },
  }
}
