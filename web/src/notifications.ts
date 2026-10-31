// 收掉通知栏里已处理请求的通知。iOS 对不弹通知的推送会吊销订阅，hub 不能发静默推送来收：只能由网页在拿到收件箱状态时
// 关掉 tag 不在待决集合里的那些（Service Worker 用请求 id 作通知的 tag，测试通知是 railse-test）。

/** 通知栏里的一条通知：只用到 tag 与 close。 */
export interface ShownNotification {
  readonly tag: string
  close(): void
}

/** 能列出本站通知的 Service Worker 注册。 */
export interface NotificationSource {
  getNotifications(): Promise<ShownNotification[]>
}

/**
 * 本站的 Service Worker 注册。用 getRegistration 而不是 ready：没有注册（注册失败、还没注册）时 ready 永远不 resolve。
 * 没有 Service Worker、没有注册或注册上没有 getNotifications 时回 null。
 */
export async function swNotifications(): Promise<NotificationSource | null> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return null
  const reg = await navigator.serviceWorker.getRegistration()
  if (!reg || typeof reg.getNotifications !== 'function') return null
  return reg
}

/** 关掉 tag 不在 waiting 里的通知，回关了几条。拿不到注册或 API、出错时静默跳过，不会 reject。 */
export async function closeStale(
  waiting: ReadonlySet<string>,
  source: () => Promise<NotificationSource | null> = swNotifications,
): Promise<number> {
  let shown: ShownNotification[]
  try {
    const src = await source()
    if (!src) return 0
    shown = await src.getNotifications()
  } catch {
    return 0
  }
  let n = 0
  for (const x of shown) {
    if (waiting.has(x.tag)) continue
    try {
      x.close()
      n++
    } catch {
      // 关不掉的跳过，下次状态更新再试
    }
  }
  return n
}

/** 收旧通知的节流间隔：同一秒内多次状态更新只收一次。 */
export const closeInterval = 1000

/**
 * 包成节流的“收掉旧通知”：空闲时立即收一次；间隔内再来的只记下最新的待决集合，间隔末尾用它补收一次（期间多次也只补一次）。
 * 补收用最新的集合，间隔里被批掉的那条也能收掉，不用等下一次状态变化。
 */
export function staleCloser(
  close: (waiting: ReadonlySet<string>) => Promise<unknown> = closeStale,
  interval = closeInterval,
): (pendingIDs: Iterable<string>) => void {
  let timer: ReturnType<typeof setTimeout> | null = null
  let queued: ReadonlySet<string> | null = null
  const fire = (ids: ReadonlySet<string>) => {
    void close(ids)
    timer = setTimeout(() => {
      timer = null
      const next = queued
      queued = null
      if (next) fire(next)
    }, interval)
  }
  return (pendingIDs) => {
    const ids = new Set(pendingIDs)
    if (timer) queued = ids
    else fire(ids)
  }
}

/** 网页拿到收件箱状态时调：关掉 tag 不在 pendingIDs（待审批与等你回答的请求 id）里的通知，同一秒内至多一次。 */
export const closeStaleNotifications = staleCloser()
