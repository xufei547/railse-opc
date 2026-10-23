// Railse Service Worker：只管推送、通知点击与订阅更换。
// - 不缓存任何资源，没有 fetch 处理器：页面永远从 hub 取最新的，不会卡在旧壳子上（hub 以 no-cache 提供本文件）。
// - 纯 ES、不经构建、不从别处加载脚本；src/sw.test.ts 原样加载本文件测试。
'use strict'

const ICON = '/icons/icon-192.png'
const DEFAULT_TITLE = 'Railse'
const FALLBACK_BODY = '有新消息，点开查看'
const DEFAULT_TAG = 'railse'
const TEST_TAG = 'railse-test'

self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()))

/**
 * 只收本站的路径：以 / 开头、解析后同源，且规范化之后不以 // 或 /\ 开头（/..//evil、/%2e%2e//evil 消掉点段后是 //evil，
 * 那是协议相对的跨源地址）。其余（绝对地址、跨源、javascript: 等）一律换成首页。结果再过一次不变。
 */
function safePath(u) {
  if (typeof u !== 'string' || !u.startsWith('/')) return '/'
  let url
  try {
    url = new URL(u, self.location.origin)
  } catch (e) {
    return '/'
  }
  if (url.origin !== self.location.origin) return '/'
  const out = url.pathname + url.search + url.hash
  if (out.startsWith('//') || out.startsWith('/\\')) return '/'
  return out
}

/** 点通知要去的完整地址：通知里的 url 过 safePath 后，再确认解析出来的地址同源；不同源就去首页。 */
function clickTarget(data) {
  const home = new URL('/', self.location.origin).href
  let url
  try {
    url = new URL(safePath(data && data.url), self.location.origin)
  } catch (e) {
    return home
  }
  return url.origin === self.location.origin ? url.href : home
}

function text(v) {
  return typeof v === 'string' ? v : ''
}

function fallbackNotification() {
  return { title: DEFAULT_TITLE, options: { body: FALLBACK_BODY, tag: DEFAULT_TAG, renotify: true, icon: ICON, data: { url: '/' } } }
}

/** hub 的推送载荷 {id, kind, title, body, url} 转成通知；读不出时用兜底文案。 */
function notificationFor(data) {
  let msg = null
  try {
    msg = data ? data.json() : null
  } catch (e) {
    msg = null
  }
  if (!msg || typeof msg !== 'object' || Array.isArray(msg)) return fallbackNotification()
  const tag = msg.kind === 'test' ? TEST_TAG : text(msg.id) || DEFAULT_TAG
  return {
    title: text(msg.title).trim() || DEFAULT_TITLE,
    options: {
      body: text(msg.body),
      // 同一请求只留一条；同 tag 的新通知默认不再提醒，renotify 让它照样提醒（连按两次“测试”也看得到第二条）
      tag,
      renotify: true,
      icon: ICON,
      data: { url: safePath(msg.url) },
    },
  }
}

/** 弹通知；被拒（选项不被接受等）时再弹一条最简的。 */
async function show(n) {
  try {
    await self.registration.showNotification(n.title, n.options)
  } catch (e) {
    await self.registration.showNotification(DEFAULT_TITLE, { body: FALLBACK_BODY, tag: DEFAULT_TAG })
  }
}

// 每条推送都必须弹通知（含测试推送、正文为空、载荷读不出、组通知时出错的）：iOS 对不弹通知的推送会吊销订阅。
self.addEventListener('push', (event) => {
  let n
  try {
    n = notificationFor(event.data)
  } catch (e) {
    n = fallbackNotification()
  }
  event.waitUntil(show(n))
})

/** 让已打开的同源窗口跳过去并前置；没有窗口、跳不了（navigate 回空或抛错）时开新窗口。两条路在 iOS 上都实测走到过。 */
async function openTarget(target) {
  const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
  for (const w of wins) {
    let same = false
    try {
      same = new URL(w.url).origin === self.location.origin
    } catch (e) {
      same = false
    }
    if (!same || typeof w.navigate !== 'function') continue
    let nav = null
    try {
      nav = await w.navigate(target)
    } catch (e) {
      nav = null // 不受本 worker 控制的窗口不能 navigate，试下一个
    }
    if (nav) {
      try {
        await nav.focus()
      } catch (e) {
        // 已经跳过去了：前置失败也不再开一个窗口
      }
      return
    }
  }
  await self.clients.openWindow(target)
}

// 点通知：打开哪条请求由页面读 URL（/r/<id>）决定，这里点击后不再发请求、不给页面发消息（iOS 上点击后 worker 很快被挂起）。
self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  event.waitUntil(openTarget(clickTarget(event.notification.data)))
})

function decodeKey(s) {
  const b64 = (s + '='.repeat((4 - (s.length % 4)) % 4)).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(b64)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

/** 订阅用的公钥字节；浏览器不给时为 null。 */
function keyOf(sub) {
  const k = sub && sub.options && sub.options.applicationServerKey
  if (!k) return null
  return ArrayBuffer.isView(k) ? new Uint8Array(k.buffer, k.byteOffset, k.byteLength) : new Uint8Array(k)
}

/** 两把公钥相同；任一拿不到时当作不同（宁可重订）。 */
function sameKey(a, b) {
  if (!a || !b || a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false
  return true
}

/** hub 现在的公钥：字节；hub 停用推送为 null；拉不到（断网、登录失效、坏响应）为 undefined。 */
async function hubKey() {
  try {
    const r = await fetch('/api/push/vapid', { credentials: 'same-origin' })
    if (!r.ok) return undefined
    const v = await r.json()
    if (!v || typeof v !== 'object') return undefined
    if (v.enabled === false) return null
    if (typeof v.public_key !== 'string' || !v.public_key) return undefined
    return decodeKey(v.public_key)
  } catch (e) {
    return undefined
  }
}

/**
 * 浏览器换了订阅：先拉 hub 现在的公钥（hub 换过钥时，按旧公钥订的订阅 hub 照收却永远推不通）；拉不到就退回旧订阅的公钥。
 * 浏览器给的新订阅公钥对得上就直接登记，否则退掉、用要用的公钥重订（公钥不同的现有订阅不先退，subscribe 会抛 InvalidStateError）。
 * 带 cookie 登记到 hub，成功后让 hub 删掉旧端点。
 */
async function resubscribe(event) {
  const old = event.oldSubscription || null
  const current = await hubKey()
  if (current === null) return // hub 停用推送：登记也会回 503
  const key = current || keyOf(old)
  let sub = event.newSubscription || null
  if (sub && current && !sameKey(keyOf(sub), current)) {
    await sub.unsubscribe().catch(() => false)
    sub = null
  }
  if (!sub) {
    if (!key) return
    const existing = await self.registration.pushManager.getSubscription()
    if (existing && !sameKey(keyOf(existing), key)) await existing.unsubscribe().catch(() => false)
    sub = await self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })
  }
  const json = { 'Content-Type': 'application/json' }
  const r = await fetch('/api/push/subscribe', { method: 'POST', credentials: 'same-origin', headers: json, body: JSON.stringify(sub.toJSON()) })
  if (!r.ok) return
  if (old && old.endpoint && old.endpoint !== sub.endpoint) {
    await fetch('/api/push/subscribe', { method: 'DELETE', credentials: 'same-origin', headers: json, body: JSON.stringify({ endpoint: old.endpoint }) })
  }
}

// 失败（登录已失效、推送停用、网络不通）就算了：下次打开页面时检查会发现订阅失效并提示重新开启。
self.addEventListener('pushsubscriptionchange', (event) => {
  event.waitUntil(resubscribe(event).catch(() => undefined))
})
