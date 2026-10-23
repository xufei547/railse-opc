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

/** 只收同源的路径（以 / 开头、解析后仍是本站），其余（绝对地址、跨源、//host、javascript: 等）一律换成首页。 */
function safePath(u) {
  if (typeof u !== 'string' || !u.startsWith('/')) return '/'
  let url
  try {
    url = new URL(u, self.location.origin)
  } catch (e) {
    return '/'
  }
  if (url.origin !== self.location.origin) return '/'
  return url.pathname + url.search + url.hash
}

function text(v) {
  return typeof v === 'string' ? v : ''
}

/** hub 的推送载荷 {id, kind, title, body, url} 转成通知；读不出时用兜底文案。 */
function notificationFor(data) {
  let msg = null
  try {
    msg = data ? data.json() : null
  } catch (e) {
    msg = null
  }
  if (!msg || typeof msg !== 'object' || Array.isArray(msg)) {
    return { title: DEFAULT_TITLE, options: { body: FALLBACK_BODY, tag: DEFAULT_TAG, renotify: true, icon: ICON, data: { url: '/' } } }
  }
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

// 每条推送都必须弹通知（含测试推送、正文为空、载荷读不出的）：iOS 对不弹通知的推送会吊销订阅。
self.addEventListener('push', (event) => {
  const n = notificationFor(event.data)
  event.waitUntil(self.registration.showNotification(n.title, n.options))
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
  const data = event.notification.data
  const target = new URL(safePath(data && data.url), self.location.origin).href
  event.waitUntil(openTarget(target))
})

function decodeKey(s) {
  const b64 = (s + '='.repeat((4 - (s.length % 4)) % 4)).replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(b64)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

/** 浏览器换了订阅：用旧订阅的公钥（没有就拉 hub 现在的）重新订阅，带 cookie 登记到 hub，再让 hub 删掉旧端点。 */
async function resubscribe(event) {
  const old = event.oldSubscription
  let key = old && old.options && old.options.applicationServerKey
  if (!key) {
    const r = await fetch('/api/push/vapid', { credentials: 'same-origin' })
    if (!r.ok) return
    const v = await r.json()
    if (!v || !v.enabled || typeof v.public_key !== 'string' || !v.public_key) return
    key = decodeKey(v.public_key)
  }
  const sub = event.newSubscription || (await self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key }))
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
