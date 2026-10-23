export type FrameType = 'request_new' | 'request_updated' | 'rules_updated' | 'status'
export interface Frame {
  type: FrameType
  data: unknown
}

const frameTypes: ReadonlySet<string> = new Set<FrameType>(['request_new', 'request_updated', 'rules_updated', 'status'])

/** 与浏览器 WebSocket 兼容的最小接口，测试用假对象替换。 */
export interface SocketLike {
  onopen: ((ev: unknown) => void) | null
  onmessage: ((ev: { data: unknown }) => void) | null
  onclose: ((ev: { code: number }) => void) | null
  onerror: ((ev: unknown) => void) | null
  close(): void
}

export interface LiveOptions {
  onFrame: (f: Frame) => void
  /** 每次连上（含重连）都调，调用方应全量重新拉取。 */
  onOpen: () => void
  /** 断开时调；code 为 1008 表示登录失效，不再重连。建连接本身失败时 code 为 undefined。 */
  onDown: (code?: number) => void
  makeSocket?: (url: string) => SocketLike
  backoff?: number[]
  setTimer?: (fn: () => void, ms: number) => unknown
  clearTimer?: (t: unknown) => void
}

export const defaultBackoff = [1000, 2000, 4000, 8000, 16000, 30000]

/** hub 以 1008（Policy Violation）关闭 /ws/client 表示登录已失效。 */
export const closeSessionInvalid = 1008

export function nextDelay(attempt: number, backoff: number[] = defaultBackoff): number {
  return backoff[Math.min(attempt, backoff.length - 1)] ?? 30000
}

export function wsURL(loc: { protocol: string; host: string } = window.location): string {
  return (loc.protocol === 'https:' ? 'wss://' : 'ws://') + loc.host + '/ws/client'
}

function parseFrame(data: unknown): Frame | null {
  try {
    const f = JSON.parse(String(data)) as unknown
    if (typeof f !== 'object' || f === null) return null
    const type = (f as { type?: unknown }).type
    if (typeof type !== 'string' || !frameTypes.has(type)) return null
    return { type: type as FrameType, data: (f as { data?: unknown }).data }
  } catch {
    return null
  }
}

/** 维护到 /ws/client 的长连接：断开后按退避重连，1008 停止。 */
export class Live {
  private sock: SocketLike | null = null
  private attempt = 0
  private timer: unknown = null
  private stopped = false
  private readonly make: (url: string) => SocketLike
  private readonly backoff: number[]
  private readonly setTimer: (fn: () => void, ms: number) => unknown
  private readonly clearTimer: (t: unknown) => void

  constructor(private readonly opts: LiveOptions) {
    this.make = opts.makeSocket ?? ((url) => new WebSocket(url) as unknown as SocketLike)
    this.backoff = opts.backoff ?? defaultBackoff
    this.setTimer = opts.setTimer ?? ((fn, ms) => setTimeout(fn, ms))
    this.clearTimer = opts.clearTimer ?? ((t) => clearTimeout(t as ReturnType<typeof setTimeout>))
  }

  start() {
    this.stopped = false
    this.connect()
  }

  stop() {
    this.stopped = true
    this.cancelTimer()
    const s = this.sock
    this.sock = null // 先清掉：随后到来的 onclose 认出不是当前连接，不再回调、不再重连
    s?.close()
  }

  /**
   * 页面回到前台或网络恢复时调：正在等退避重连就立即重连并把退避归零；连着（或正在连）、已停止或登录失效时什么也不做。
   * iOS 把后台页面的连接断掉，回到前台时不该再等最长 30 秒的退避。
   */
  wake() {
    if (this.stopped || this.sock !== null || this.timer === null) return
    this.cancelTimer()
    this.attempt = 0
    this.connect()
  }

  private cancelTimer() {
    if (this.timer !== null) {
      this.clearTimer(this.timer)
      this.timer = null
    }
  }

  private connect() {
    if (this.stopped) return
    let s: SocketLike
    try {
      s = this.make(wsURL())
    } catch {
      // 构造就抛（如 CSP 拦下、地址非法）：当作断开，照样退避重试
      this.opts.onDown(undefined)
      this.retry()
      return
    }
    this.sock = s
    s.onopen = () => {
      if (this.sock !== s) return
      this.attempt = 0
      this.opts.onOpen()
    }
    s.onmessage = (ev) => {
      if (this.sock !== s) return
      const f = parseFrame(ev.data)
      if (f) this.opts.onFrame(f) // 坏帧、不认识的帧忽略
    }
    s.onerror = () => {
      // 随后会触发 onclose
    }
    s.onclose = (ev) => {
      if (this.sock !== s) return
      this.sock = null
      if (ev.code === closeSessionInvalid) this.stopped = true
      this.opts.onDown(ev.code)
      this.retry()
    }
  }

  private retry() {
    if (this.stopped) return
    const delay = nextDelay(this.attempt, this.backoff)
    this.attempt++
    this.timer = this.setTimer(() => {
      this.timer = null
      this.connect()
    }, delay)
  }
}
