/** 是否从主屏幕图标启动（iOS 只有这时才有推送能力）。 */
export function isStandalone(): boolean {
  const nav = navigator as Navigator & { standalone?: boolean }
  return nav.standalone === true || (typeof window.matchMedia === 'function' && window.matchMedia('(display-mode: standalone)').matches)
}

/** iPhone、iPod，或 iPadOS（Safari 自称 Mac，但有多点触控）。 */
export function isIOS(): boolean {
  return /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
}

/** 能否订阅推送：看 PushManager 是否存在，不看 Service Worker 状态（iOS 在 Safari 标签页里 SW 能注册但没有 PushManager）。 */
export function pushSupported(): boolean {
  return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
}

/** VAPID 公钥（base64url）转成 subscribe 要的字节。 */
export function urlBase64ToUint8Array(s: string): Uint8Array<ArrayBuffer> {
  const padded = s + '='.repeat((4 - (s.length % 4)) % 4)
  const b64 = padded.replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(b64)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

export async function registerServiceWorker(): Promise<ServiceWorkerRegistration | null> {
  if (!('serviceWorker' in navigator)) return null
  try {
    return await navigator.serviceWorker.register('/sw.js')
  } catch {
    return null
  }
}

/**
 * 本机现有的推送订阅；不支持推送或还没注册 Service Worker 时回 null。
 * 用 getRegistration 而不是 ready：注册失败时 ready 永远不会 resolve，页面会一直卡在“检查中”。
 */
export async function getSubscription(): Promise<PushSubscription | null> {
  if (!pushSupported()) return null
  const reg = await navigator.serviceWorker.getRegistration()
  if (!reg) return null
  return reg.pushManager.getSubscription()
}

/**
 * 订阅推送。必须在用户点击的处理函数里第一个调用，且调用前不能有任何 await：iOS 只认直接来自用户手势的授权请求。
 * 所以这里第一句就是 Notification.requestPermission()，公钥由调用方事先拉好传进来。
 */
export async function subscribe(publicKey: string): Promise<PushSubscription> {
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') throw new Error('没有获得通知权限')
  const reg = (await navigator.serviceWorker.getRegistration()) ?? (await registerServiceWorker())
  if (!reg) throw new Error('Service Worker 没有注册成功，刷新页面后再试')
  // pushManager.subscribe 要求有激活的 worker：刚注册的要等它激活
  const ready = await navigator.serviceWorker.ready
  return ready.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(publicKey) })
}
