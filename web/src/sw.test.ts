// public/sw.js 原样（不经构建）在一个假的 Service Worker 全局里跑：self、clients、registration、fetch 都是假的，
// 事件处理函数直接调用，检查它交给 waitUntil 的结果。
import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const code = readFileSync(new URL('../public/sw.js', import.meta.url), 'utf8')
const origin = 'https://hub.example'

interface Shown {
  title: string
  options: NotificationOptions & { renotify?: boolean }
}

interface FakeClient {
  url: string
  navigate?: (url: string) => Promise<FakeClient | null>
  focus: () => Promise<FakeClient>
}

interface FetchCall {
  url: string
  init: RequestInit | undefined
}

function load(
  o: {
    clients?: FakeClient[]
    fetch?: (url: string, init?: RequestInit) => Promise<Response>
    subscribeFails?: boolean
  } = {},
) {
  const handlers: Record<string, (e: unknown) => void> = {}
  const log: string[] = []
  const shown: Shown[] = []
  const fetches: FetchCall[] = []
  const subscribeArgs: PushSubscriptionOptionsInit[] = []
  const self = {
    location: new URL(`${origin}/sw.js`),
    addEventListener: (type: string, fn: (e: unknown) => void) => {
      handlers[type] = fn
    },
    skipWaiting: () => {
      log.push('skipWaiting')
      return Promise.resolve()
    },
    clients: {
      claim: () => {
        log.push('claim')
        return Promise.resolve()
      },
      matchAll: (opts: unknown) => {
        log.push(`matchAll ${JSON.stringify(opts)}`)
        return Promise.resolve(o.clients ?? [])
      },
      openWindow: (url: string) => {
        log.push(`openWindow ${url}`)
        return Promise.resolve(null)
      },
    },
    registration: {
      showNotification: (title: string, options: Shown['options']) => {
        shown.push({ title, options })
        return Promise.resolve()
      },
      pushManager: {
        subscribe: (opts: PushSubscriptionOptionsInit) => {
          subscribeArgs.push(opts)
          log.push('subscribe')
          if (o.subscribeFails) return Promise.reject(new Error('AbortError'))
          return Promise.resolve({
            endpoint: 'https://push.example/new',
            toJSON: () => ({ endpoint: 'https://push.example/new', keys: { p256dh: 'p', auth: 'a' } }),
          })
        },
      },
    },
  }
  const fetchFn = (url: string, init?: RequestInit) => {
    fetches.push({ url, init })
    return o.fetch ? o.fetch(url, init) : Promise.resolve(new Response(null, { status: 204 }))
  }
  // Service Worker 的全局就是 self：sw.js 里的 self、fetch、atob、URL 都按全局名字取
  new Function('self', 'fetch', code)(self, fetchFn)

  /** 派发一个事件，等它交给 waitUntil 的 Promise 结束；没调 waitUntil 时回 false。 */
  async function dispatch(type: string, event: Record<string, unknown>): Promise<boolean> {
    let waited: Promise<unknown> | null = null
    const fn = handlers[type]
    if (!fn) throw new Error(`sw.js 没有处理 ${type}`)
    fn({
      ...event,
      waitUntil: (p: Promise<unknown>) => {
        waited = p
      },
    })
    if (!waited) return false
    await waited
    return true
  }

  const push = (data: { json(): unknown } | null) => dispatch('push', { data })
  const pushText = (text: string) =>
    push({
      json: () => JSON.parse(text) as unknown,
    })

  return { handlers, log, shown, fetches, subscribeArgs, dispatch, push, pushText }
}

describe('sw.js：安装与缓存', () => {
  it('装好立即接管；没有 fetch 处理器（不缓存，避免旧壳子）', async () => {
    const sw = load()
    expect(Object.keys(sw.handlers).sort()).toEqual(['activate', 'install', 'notificationclick', 'push', 'pushsubscriptionchange'])
    sw.handlers.install?.({})
    expect(sw.log).toContain('skipWaiting')
    await sw.dispatch('activate', {})
    expect(sw.log).toContain('claim')
  })

  it('纯 ES：没有 import、importScripts、eval', () => {
    expect(code).not.toMatch(/\bimport\b|importScripts|\beval\(|new Function/)
  })
})

describe('sw.js：push 一定弹通知', () => {
  it('hub 的审批推送：标题、正文、tag 为请求 id、点开去的 URL、图标', async () => {
    const sw = load()
    await sw.pushText(JSON.stringify({ id: 'abc123', kind: 'permission', title: 'mac · blog', body: 'Bash: git push · 风险 outward', url: '/r/abc123' }))
    expect(sw.shown).toHaveLength(1)
    expect(sw.shown[0]?.title).toBe('mac · blog')
    expect(sw.shown[0]?.options).toMatchObject({
      body: 'Bash: git push · 风险 outward',
      tag: 'abc123',
      data: { url: '/r/abc123' },
      icon: '/icons/icon-192.png',
    })
  })

  it('测试推送（id test、url /、没有 Topic）：照样弹，tag 用固定值', async () => {
    const sw = load()
    await sw.pushText('{"id":"test","kind":"test","title":"Railse","body":"通知正常","url":"/"}')
    expect(sw.shown[0]).toMatchObject({ title: 'Railse', options: { body: '通知正常', tag: 'railse-test', data: { url: '/' } } })
    // 连发两次测试也要再提醒（同 tag 的新通知默认不响）
    expect(sw.shown[0]?.options.renotify).toBe(true)
  })

  it('正文为空的问题：照样弹', async () => {
    const sw = load()
    await sw.pushText(JSON.stringify({ id: 'q1', kind: 'question', title: 'mac · blog：等你回答', body: '', url: '/r/q1' }))
    expect(sw.shown[0]).toMatchObject({ title: 'mac · blog：等你回答', options: { body: '', tag: 'q1', data: { url: '/r/q1' } } })
  })

  it('读不出的载荷（不是 JSON、不是对象、没有载荷）：用兜底的中文文案照样弹', async () => {
    for (const data of [
      { json: () => JSON.parse('not json') as unknown },
      { json: () => null },
      { json: () => [1, 2] },
      { json: () => 42 },
      { json: () => 'str' },
      null,
    ]) {
      const sw = load()
      expect(await sw.push(data)).toBe(true)
      expect(sw.shown).toHaveLength(1)
      expect(sw.shown[0]?.title).toBe('Railse')
      expect(sw.shown[0]?.options).toMatchObject({ body: '有新消息，点开查看', tag: 'railse', data: { url: '/' }, icon: '/icons/icon-192.png' })
    }
  })

  it('字段类型不对：各自用默认值，仍然弹', async () => {
    const sw = load()
    await sw.push({ json: () => ({ id: 7, title: { x: 1 }, body: ['b'], url: 5 }) })
    expect(sw.shown[0]).toMatchObject({ title: 'Railse', options: { body: '', tag: 'railse', data: { url: '/' } } })
    const blank = load()
    await blank.push({ json: () => ({ id: '', title: '   ', body: 'x', url: '/r/1' }) })
    expect(blank.shown[0]).toMatchObject({ title: 'Railse', options: { body: 'x', tag: 'railse', data: { url: '/r/1' } } })
  })

  it('url 只收同源路径：绝对地址、跨源、协议相对、反斜杠、javascript:、相对路径都换成 /', async () => {
    const cases: [unknown, string][] = [
      ['/r/a%2Fb?x=1#y', '/r/a%2Fb?x=1#y'],
      ['/settings', '/settings'],
      ['https://evil.example/r/1', '/'],
      [`${origin}/r/1`, '/'],
      ['//evil.example/r/1', '/'],
      ['/\\evil.example', '/'],
      ['/\t/evil.example', '/'],
      ['\\\\evil.example', '/'],
      ['javascript:alert(1)', '/'],
      ['r/1', '/'],
      ['', '/'],
      [null, '/'],
    ]
    for (const [url, want] of cases) {
      const sw = load()
      await sw.push({ json: () => ({ id: 'x', title: 't', body: 'b', url }) })
      expect(sw.shown[0]?.options.data, String(url)).toEqual({ url: want })
    }
  })
})

describe('sw.js：点通知', () => {
  function click(sw: ReturnType<typeof load>, data: unknown) {
    const closed: boolean[] = []
    return {
      closed,
      done: sw.dispatch('notificationclick', { notification: { data, close: () => closed.push(true) } }),
    }
  }

  function client(url: string, log: string[], nav: 'ok' | 'null' | 'throw' | 'none' = 'ok'): FakeClient {
    const c: FakeClient = {
      url,
      focus: () => {
        log.push(`focus ${c.url}`)
        return Promise.resolve(c)
      },
    }
    if (nav !== 'none') {
      c.navigate = (to: string) => {
        log.push(`navigate ${url} -> ${to}`)
        if (nav === 'throw') return Promise.reject(new TypeError('not controlled'))
        if (nav === 'null') return Promise.resolve(null)
        c.url = to
        return Promise.resolve(c)
      }
    }
    return c
  }

  it('有已打开的窗口：关掉通知，让它跳到请求详情并前置，不开新窗口', async () => {
    const log: string[] = []
    const sw = load({ clients: [client(`${origin}/`, log)] })
    const c = click(sw, { url: '/r/abc' })
    await c.done
    expect(c.closed).toEqual([true])
    expect(sw.log[0]).toBe('matchAll {"type":"window","includeUncontrolled":true}')
    expect(log).toEqual([`navigate ${origin}/ -> ${origin}/r/abc`, `focus ${origin}/r/abc`])
    expect(sw.log.some((l) => l.startsWith('openWindow'))).toBe(false)
  })

  it('navigate 回空、抛错或没有 navigate：开新窗口', async () => {
    for (const nav of ['null', 'throw', 'none'] as const) {
      const log: string[] = []
      const sw = load({ clients: [client(`${origin}/`, log, nav)] })
      await click(sw, { url: '/r/abc' }).done
      expect(sw.log.at(-1), nav).toBe(`openWindow ${origin}/r/abc`)
      expect(log.some((l) => l.startsWith('focus')), nav).toBe(false)
    }
  })

  it('第一个窗口不能跳就试下一个', async () => {
    const log: string[] = []
    const sw = load({ clients: [client(`${origin}/a`, log, 'throw'), client(`${origin}/b`, log)] })
    await click(sw, { url: '/r/abc' }).done
    expect(log).toEqual([`navigate ${origin}/a -> ${origin}/r/abc`, `navigate ${origin}/b -> ${origin}/r/abc`, `focus ${origin}/r/abc`])
    expect(sw.log.some((l) => l.startsWith('openWindow'))).toBe(false)
  })

  it('跨源的窗口不碰', async () => {
    const log: string[] = []
    const sw = load({ clients: [client('https://evil.example/', log)] })
    await click(sw, { url: '/r/abc' }).done
    expect(log).toEqual([])
    expect(sw.log.at(-1)).toBe(`openWindow ${origin}/r/abc`)
  })

  it('前置失败也不再开一个窗口（已经跳过去了）', async () => {
    const log: string[] = []
    const c = client(`${origin}/`, log)
    c.focus = () => Promise.reject(new Error('not allowed'))
    const sw = load({ clients: [c] })
    await click(sw, { url: '/r/abc' }).done
    expect(sw.log.some((l) => l.startsWith('openWindow'))).toBe(false)
  })

  it('没有窗口：开新窗口；目标不是同源路径时去首页', async () => {
    let sw = load()
    await click(sw, { url: '/r/abc' }).done
    expect(sw.log.at(-1)).toBe(`openWindow ${origin}/r/abc`)
    for (const data of [{ url: 'https://evil.example/' }, { url: '//evil.example' }, null, undefined, {}]) {
      sw = load()
      await click(sw, data).done
      expect(sw.log.at(-1)).toBe(`openWindow ${origin}/`)
    }
  })
})

describe('sw.js：订阅换了', () => {
  const ok = () => Promise.resolve(new Response(null, { status: 204 }))

  function oldSub(appKey: ArrayBuffer | null, endpoint = 'https://push.example/old') {
    return { endpoint, options: { applicationServerKey: appKey } }
  }

  it('用旧订阅的公钥重新订阅，并带 cookie 登记到 hub；旧端点让 hub 删掉', async () => {
    const appKey = new Uint8Array([4, 1, 2]).buffer
    const sw = load({ fetch: ok })
    expect(await sw.dispatch('pushsubscriptionchange', { oldSubscription: oldSub(appKey) })).toBe(true)
    expect(sw.subscribeArgs).toEqual([{ userVisibleOnly: true, applicationServerKey: appKey }])
    expect(sw.fetches[0]?.url).toBe('/api/push/subscribe')
    expect(sw.fetches[0]?.init).toMatchObject({ method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' } })
    expect(JSON.parse(String(sw.fetches[0]?.init?.body))).toEqual({ endpoint: 'https://push.example/new', keys: { p256dh: 'p', auth: 'a' } })
    expect(sw.fetches[1]?.url).toBe('/api/push/subscribe')
    expect(sw.fetches[1]?.init).toMatchObject({ method: 'DELETE', credentials: 'same-origin' })
    expect(JSON.parse(String(sw.fetches[1]?.init?.body))).toEqual({ endpoint: 'https://push.example/old' })
    expect(sw.fetches).toHaveLength(2)
  })

  it('旧订阅没有公钥（或没有旧订阅）：先拉 hub 现在的公钥', async () => {
    for (const old of [oldSub(null), null, { endpoint: 'https://push.example/old' }]) {
      const sw = load({
        fetch: (url) =>
          url === '/api/push/vapid'
            ? Promise.resolve(new Response(JSON.stringify({ enabled: true, public_key: 'BAEC', reason: '' }), { status: 200 }))
            : ok(),
      })
      await sw.dispatch('pushsubscriptionchange', { oldSubscription: old })
      expect(sw.fetches[0]?.url).toBe('/api/push/vapid')
      expect(sw.fetches[0]?.init).toMatchObject({ credentials: 'same-origin' })
      expect([...new Uint8Array(sw.subscribeArgs[0]?.applicationServerKey as ArrayBuffer)]).toEqual([4, 1, 2])
      expect(sw.fetches[1]?.init).toMatchObject({ method: 'POST' })
    }
  })

  it('浏览器已经订好新的（newSubscription）：直接登记它', async () => {
    const sw = load({ fetch: ok })
    const fresh = { endpoint: 'https://push.example/fresh', toJSON: () => ({ endpoint: 'https://push.example/fresh', keys: {} }) }
    await sw.dispatch('pushsubscriptionchange', { oldSubscription: oldSub(new Uint8Array([1]).buffer), newSubscription: fresh })
    expect(sw.subscribeArgs).toEqual([])
    expect(JSON.parse(String(sw.fetches[0]?.init?.body))).toEqual({ endpoint: 'https://push.example/fresh', keys: {} })
  })

  it('登录失效、推送停用、网络不通、订阅失败：安静放弃（下次打开页面会提示失效）', async () => {
    const fail = [
      load({ fetch: () => Promise.resolve(new Response('{"error":"请先登录"}', { status: 401 })) }),
      load({ fetch: () => Promise.resolve(new Response(JSON.stringify({ enabled: false, public_key: '', reason: 'x' }), { status: 200 })) }),
      load({ fetch: () => Promise.reject(new TypeError('Failed to fetch')) }),
    ]
    for (const sw of fail) {
      await expect(sw.dispatch('pushsubscriptionchange', { oldSubscription: null })).resolves.toBe(true)
      expect(sw.fetches.some((f) => f.init?.method === 'POST')).toBe(false)
    }
    const sub = load({ fetch: ok, subscribeFails: true })
    await expect(sub.dispatch('pushsubscriptionchange', { oldSubscription: oldSub(new Uint8Array([1]).buffer) })).resolves.toBe(true)
    expect(sub.fetches).toEqual([])
    // 登记被拒：不再删旧端点
    const rejected = load({ fetch: () => Promise.resolve(new Response('{"error":"请先登录"}', { status: 401 })) })
    await rejected.dispatch('pushsubscriptionchange', { oldSubscription: oldSub(new Uint8Array([1]).buffer) })
    expect(rejected.fetches.map((f) => f.init?.method)).toEqual(['POST'])
  })
})
