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
  /** 断开时调；code 为 1008 表示登录失效，不再重连。建连接本身失败时 code 为 undefined。recycle 主动换连接时不调。 */
  onDown: (code?: number) => void
  makeSocket?: (url: string) => SocketLike
  backoff?: number[]
  setTimer?: (fn: () => void, ms: number) => unknown
  clearTimer?: (t: unknown) => void
  /** 毫秒时钟，测试注入。 */
  now?: () => number
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

/** 连接保持这么久（或收到过任一帧）才算可用，断开时退避归零。 */
export const stableAfterMs = 10_000
/** 页面在后台超过这么久，回到前台时重建连接：手机切网络、长时间后台后可能留下看似 OPEN、实际已死的连接。 */
export const recycleAfterHiddenMs = 30_000

/**
 * 维护到 /ws/client 的长连接：断开后按退避重连，1008 停止。
 * 退避只在连接确实可用过（保持 stableAfterMs 以上或收到过帧）之后才归零：中间层接受升级后立刻关闭时不会每秒重连一次。
 */
export class Live {
  private sock: SocketLike | null = null
  private attempt = 0
  private timer: unknown = null
  private running = false
  private openedAt: number | null = null
  private proven = false
  private hiddenAt: number | null = null
  private readonly make: (url: string) => SocketLike
  private readonly backoff: number[]
  private readonly setTimer: (fn: () => void, ms: number) => unknown
  private readonly clearTimer: (t: unknown) => void
  private readonly now: () => number

  constructor(private readonly opts: LiveOptions) {
    this.make = opts.makeSocket ?? ((url) => new WebSocket(url) as unknown as SocketLike)
    this.backoff = opts.backoff ?? defaultBackoff
    this.setTimer = opts.setTimer ?? ((fn, ms) => setTimeout(fn, ms))
    this.clearTimer = opts.clearTimer ?? ((t) => clearTimeout(t as ReturnType<typeof setTimeout>))
    this.now = opts.now ?? (() => Date.now())
  }

  /** 开始维护连接。已在运行（连着、正在连或等退避）时什么也不做；stop 或 1008 之后可以再 start。 */
  start() {
    if (this.running) return
    this.running = true
    this.attempt = 0
    this.connect()
  }

  stop() {
    this.running = false
    this.cancelTimer()
    this.detach()
  }

  /**
   * 页面回到前台或网络恢复时调：正在等退避重连就立即重连并把退避归零；连着（或正在连）、已停止或登录失效时什么也不做。
   * iOS 把后台页面的连接断掉，回到前台时不该再等最长 30 秒的退避。
   */
  wake() {
    if (!this.running || this.sock !== null || this.timer === null) return
    this.cancelTimer()
    this.attempt = 0
    this.connect()
  }

  /**
   * 换一条新连接：关掉现有的（之后它的回调一律忽略，也不调 onDown）、取消等待中的重连，立即重连并把退避归零。
   * 网络恢复（online）时、长时间后台回到前台时调，回收可能半死的连接；新连接连上时调用方照常全量重拉。
   * 已停止或登录失效时什么也不做。
   */
  recycle() {
    if (!this.running) return
    this.cancelTimer()
    this.detach()
    this.attempt = 0
    this.connect()
  }

  /** 页面转到后台时调（记下时刻）。 */
  hidden() {
    this.hiddenAt = this.now()
  }

  /** 页面回到前台时调：在后台超过 recycleAfterHiddenMs 就 recycle，否则同 wake。 */
  visible() {
    const at = this.hiddenAt
    this.hiddenAt = null
    if (at !== null && this.now() - at >= recycleAfterHiddenMs) this.recycle()
    else this.wake()
  }

  private cancelTimer() {
    if (this.timer !== null) {
      this.clearTimer(this.timer)
      this.timer = null
    }
  }

  /** 丢开当前连接：先清掉引用，随后到来的回调认出不是当前连接，不再回调、不再重连。 */
  private detach() {
    const s = this.sock
    this.sock = null
    s?.close()
  }

  private connect() {
    if (!this.running) return
    this.openedAt = null
    this.proven = false
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
      this.openedAt = this.now()
      this.opts.onOpen()
    }
    s.onmessage = (ev) => {
      if (this.sock !== s) return
      this.proven = true // 收到任何数据都说明这条连接真的通
      const f = parseFrame(ev.data)
      if (f) this.opts.onFrame(f) // 坏帧、不认识的帧忽略
    }
    s.onerror = () => {
      // 随后会触发 onclose
    }
    s.onclose = (ev) => {
      if (this.sock !== s) return
      this.sock = null
      if (this.proven || (this.openedAt !== null && this.now() - this.openedAt >= stableAfterMs)) this.attempt = 0
      if (ev.code === closeSessionInvalid) this.running = false
      this.opts.onDown(ev.code)
      this.retry()
    }
  }

  private retry() {
    if (!this.running) return
    const delay = nextDelay(this.attempt, this.backoff)
    this.attempt++
    this.timer = this.setTimer(() => {
      this.timer = null
      this.connect()
    }, delay)
  }
}
