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

/** 假的推送订阅：带公钥（options.applicationServerKey）与 unsubscribe，退订记进 log。 */
function fakeSub(endpoint: string, key: number[] | null, log: string[]) {
  return {
    endpoint,
    options: { applicationServerKey: key ? new Uint8Array(key).buffer : null },
    toJSON: () => ({ endpoint, keys: { p256dh: 'p', auth: 'a' } }),
    unsubscribe: () => {
      log.push(`unsubscribe ${endpoint}`)
      return Promise.resolve(true)
    },
  }
}

function load(
  o: {
    clients?: FakeClient[]
    fetch?: (url: string, init?: RequestInit) => Promise<Response>
    subscribeFails?: boolean
    /** 前几次 showNotification 被拒。 */
    showFails?: number
    /** pushManager.getSubscription() 回的现有订阅（公钥）；undefined 为没有。 */
    existingKey?: number[] | null
  } = {},
) {
  const handlers: Record<string, (e: unknown) => void> = {}
  const log: string[] = []
  const shown: Shown[] = []
  const fetches: FetchCall[] = []
  const subscribeArgs: PushSubscriptionOptionsInit[] = []
  let showFails = o.showFails ?? 0
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
        if (showFails > 0) {
          showFails--
          log.push(`showNotification rejected ${title}`)
          return Promise.reject(new TypeError('bad options'))
        }
        shown.push({ title, options })
        return Promise.resolve()
      },
      pushManager: {
        getSubscription: () => {
          log.push('getSubscription')
          return Promise.resolve(o.existingKey === undefined ? null : fakeSub('https://push.example/existing', o.existingKey, log))
        },
        subscribe: (opts: PushSubscriptionOptionsInit) => {
          subscribeArgs.push(opts)
          log.push('subscribe')
          if (o.subscribeFails) return Promise.reject(new Error('AbortError'))
          const k = opts.applicationServerKey
          return Promise.resolve(fakeSub('https://push.example/new', k ? [...new Uint8Array(k as ArrayBuffer)] : null, log))
        },
      },
    },
  }
  const fetchFn = (url: string, init?: RequestInit) => {
    fetches.push({ url, init })
    return o.fetch ? o.fetch(url, init) : Promise.resolve(new Response(null, { status: 204 }))
  }
  // Service Worker 的全局就是 self：sw.js 里的 self、fetch、atob、URL 都按全局名字取。
  // 末尾附一句把内部的纯函数交出来测（只在测试里拼，sw.js 本身不变）
  const helpers = new Function(
    'self',
    'fetch',
    `${code}\n;return { safePath: typeof safePath === 'function' ? safePath : undefined, clickTarget: typeof clickTarget === 'function' ? clickTarget : undefined }`,
  )(self, fetchFn) as { safePath?: (u: unknown) => string; clickTarget?: (data: unknown) => string }

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

  return { handlers, log, shown, fetches, subscribeArgs, dispatch, push, pushText, helpers }
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

  it('组通知时出错（载荷字段的 getter 抛错）：照样弹兜底通知', async () => {
    const sw = load()
    const evil = {
      get title(): string {
        throw new Error('boom')
      },
    }
    expect(await sw.push({ json: () => evil })).toBe(true)
    expect(sw.shown).toHaveLength(1)
    expect(sw.shown[0]).toMatchObject({ title: 'Railse', options: { body: '有新消息，点开查看', tag: 'railse' } })
  })

  it('showNotification 被拒（选项不被接受等）：再弹一条最简的兜底通知', async () => {
    const sw = load({ showFails: 1 })
    await sw.pushText(JSON.stringify({ id: 'abc', kind: 'permission', title: 'mac · blog', body: 'x', url: '/r/abc' }))
    expect(sw.log).toContain('showNotification rejected mac · blog')
    expect(sw.shown).toEqual([{ title: 'Railse', options: { body: '有新消息，点开查看', tag: 'railse' } }])
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
      // 点段规范化之后变成 //host（协议相对、跨源）的
      ['/..//evil.example', '/'],
      ['/.//evil.example', '/'],
      ['/%2e%2e//evil.example', '/'],
      ['/%2E%2E//evil.example', '/'],
      ['/r/../..//evil.example', '/'],
      ['/./\\evil.example', '/'],
      ['/.\\\\evil.example', '/'],
      // 编码的斜杠、反斜杠不会被解码，留在本站路径里
      ['/%2F%2Fevil.example', '/%2F%2Fevil.example'],
      ['/%5C%5Cevil.example', '/%5C%5Cevil.example'],
      ['/r/../settings', '/settings'],
    ]
    for (const [url, want] of cases) {
      const sw = load()
      await sw.push({ json: () => ({ id: 'x', title: 't', body: 'b', url }) })
      expect(sw.shown[0]?.options.data, String(url)).toEqual({ url: want })
    }
  })
})

describe('sw.js：safePath 与 clickTarget', () => {
  const inputs: unknown[] = [
    '/', '/r/abc', '/r/a%2Fb?x=1#y', '/settings?tab=1',
    '/..//evil.example', '/.//evil.example', '/%2e%2e//evil.example', '/%2E%2e//evil.example', '/r/../..//evil.example',
    '/./\\evil.example', '/.\\\\evil.example', '/\\evil.example', '/\t/evil.example', '/\n/evil.example', '//evil.example',
    '///evil.example', '/%2F%2Fevil.example', '/%5C%5Cevil.example', '/..%2F..%2Fevil.example',
    '\\\\evil.example', 'https://evil.example/r/1', `${origin}/r/1`, 'javascript:alert(1)', 'data:text/html,x', 'r/1', '', ' /r/1',
    null, undefined, 42, {},
  ]

  it('结果总是本站路径：以 / 开头、不以 // 或 /\\ 开头、解析后同源，且再过一次不变（幂等）', () => {
    const { safePath } = load().helpers
    expect(safePath).toBeTypeOf('function')
    for (const u of inputs) {
      const out = safePath!(u)
      const label = JSON.stringify(u)
      expect(out.startsWith('/'), label).toBe(true)
      expect(out.startsWith('//') || out.startsWith('/\\'), label).toBe(false)
      expect(new URL(out, origin).origin, label).toBe(origin)
      expect(safePath!(out), label).toBe(out)
    }
  })

  it('clickTarget 自己再验一次同源：即使拿到的是 //host（没经过 safePath 的旧通知）也回首页', () => {
    const { clickTarget } = load().helpers
    expect(clickTarget).toBeTypeOf('function')
    expect(clickTarget!({ url: '/r/abc' })).toBe(`${origin}/r/abc`)
    for (const url of ['//evil.example', '/..//evil.example', '/./\\evil.example', 'https://evil.example/', 'javascript:alert(1)']) {
      expect(clickTarget!({ url }), url).toBe(`${origin}/`)
    }
    for (const data of [null, undefined, {}, { url: 5 }]) expect(clickTarget!(data)).toBe(`${origin}/`)
    for (const u of inputs) expect(new URL(clickTarget!({ url: u })).origin, JSON.stringify(u)).toBe(origin)
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
    for (const data of [
      { url: 'https://evil.example/' },
      { url: '//evil.example' },
      { url: '/..//evil.example' },
      { url: '/%2e%2e//evil.example' },
      { url: '/./\\evil.example' },
      null,
      undefined,
      {},
    ]) {
      sw = load()
      await click(sw, data).done
      expect(sw.log.at(-1)).toBe(`openWindow ${origin}/`)
    }
  })
})

describe('sw.js：订阅换了', () => {
  const ok = () => Promise.resolve(new Response(null, { status: 204 }))
  const hubKey = [4, 1, 2] // BAEC
  const vapid = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status }))
  /** /api/push/vapid 回 vapidResp（默认启用、公钥 BAEC），其余（登记、删除）回 rest（默认 204）。 */
  const hub =
    (vapidResp: () => Promise<Response> = () => vapid({ enabled: true, public_key: 'BAEC', reason: '' }), rest: () => Promise<Response> = ok) =>
    (url: string) =>
      url === '/api/push/vapid' ? vapidResp() : rest()
  const keyArg = (sw: ReturnType<typeof load>, i = 0) => [...new Uint8Array(sw.subscribeArgs[i]?.applicationServerKey as ArrayBuffer)]
  const old = (key: number[] | null, log: string[] = []) => fakeSub('https://push.example/old', key, log)

  it('先拉 hub 现在的公钥：旧订阅的公钥过时（hub 换过钥）也用新的订，带 cookie 登记，再删旧端点', async () => {
    const sw = load({ fetch: hub() })
    expect(await sw.dispatch('pushsubscriptionchange', { oldSubscription: old([9, 9, 9]) })).toBe(true)
    expect(sw.fetches[0]).toMatchObject({ url: '/api/push/vapid', init: { credentials: 'same-origin' } })
    expect(sw.subscribeArgs).toHaveLength(1)
    expect(sw.subscribeArgs[0]?.userVisibleOnly).toBe(true)
    expect(keyArg(sw)).toEqual(hubKey)
    expect(sw.fetches[1]?.url).toBe('/api/push/subscribe')
    expect(sw.fetches[1]?.init).toMatchObject({ method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' } })
    expect(JSON.parse(String(sw.fetches[1]?.init?.body))).toEqual({ endpoint: 'https://push.example/new', keys: { p256dh: 'p', auth: 'a' } })
    expect(sw.fetches[2]?.init).toMatchObject({ method: 'DELETE', credentials: 'same-origin' })
    expect(JSON.parse(String(sw.fetches[2]?.init?.body))).toEqual({ endpoint: 'https://push.example/old' })
    expect(sw.fetches).toHaveLength(3)
  })

  it('没有旧订阅或旧订阅不给公钥：照样用 hub 现在的公钥', async () => {
    for (const o of [null, undefined, old(null), { endpoint: 'https://push.example/old' }]) {
      const sw = load({ fetch: hub() })
      await sw.dispatch('pushsubscriptionchange', { oldSubscription: o })
      expect(keyArg(sw)).toEqual(hubKey)
      expect(sw.fetches[1]?.init).toMatchObject({ method: 'POST' })
    }
  })

  it('hub 停用推送：放弃，不订也不登记', async () => {
    const sw = load({ fetch: hub(() => vapid({ enabled: false, public_key: '', reason: 'x' })) })
    await expect(sw.dispatch('pushsubscriptionchange', { oldSubscription: old(hubKey) })).resolves.toBe(true)
    expect(sw.subscribeArgs).toEqual([])
    expect(sw.fetches.map((f) => f.url)).toEqual(['/api/push/vapid'])
  })

  it('拉不到 hub 的公钥（断网、登录失效、坏响应）：退回用旧订阅的公钥', async () => {
    for (const fail of [
      () => Promise.reject(new TypeError('Failed to fetch')),
      () => vapid({ error: '请先登录' }, 401),
      () => Promise.resolve(new Response('not json', { status: 200 })),
      () => vapid({ enabled: true, public_key: '' }),
    ]) {
      const sw = load({ fetch: hub(fail) })
      await sw.dispatch('pushsubscriptionchange', { oldSubscription: old([7, 7]) })
      expect(keyArg(sw)).toEqual([7, 7])
      expect(sw.fetches.some((f) => f.init?.method === 'POST')).toBe(true)
    }
  })

  it('拉不到公钥、旧订阅也没有公钥：放弃', async () => {
    const sw = load({ fetch: hub(() => Promise.reject(new TypeError('Failed to fetch'))) })
    await expect(sw.dispatch('pushsubscriptionchange', { oldSubscription: null })).resolves.toBe(true)
    expect(sw.subscribeArgs).toEqual([])
    expect(sw.fetches.some((f) => f.init?.method === 'POST')).toBe(false)
  })

  it('浏览器已订好的新订阅（newSubscription）公钥与 hub 一致：直接登记它', async () => {
    const sw = load({ fetch: hub() })
    const fresh = fakeSub('https://push.example/fresh', hubKey, sw.log)
    await sw.dispatch('pushsubscriptionchange', { oldSubscription: old([9]), newSubscription: fresh })
    expect(sw.subscribeArgs).toEqual([])
    expect(sw.log).not.toContain('unsubscribe https://push.example/fresh')
    expect(JSON.parse(String(sw.fetches[1]?.init?.body))).toMatchObject({ endpoint: 'https://push.example/fresh' })
  })

  it('newSubscription 是按旧公钥订的（或看不出公钥）：先退掉，再用 hub 现在的公钥订', async () => {
    for (const k of [[9, 9], null]) {
      const sw = load({ fetch: hub() })
      const stale = fakeSub('https://push.example/fresh', k, sw.log)
      await sw.dispatch('pushsubscriptionchange', { oldSubscription: old([9, 9]), newSubscription: stale })
      expect(sw.log.indexOf('unsubscribe https://push.example/fresh')).toBeGreaterThan(-1)
      expect(sw.log.indexOf('unsubscribe https://push.example/fresh')).toBeLessThan(sw.log.indexOf('subscribe'))
      expect(keyArg(sw)).toEqual(hubKey)
      expect(JSON.parse(String(sw.fetches[1]?.init?.body))).toMatchObject({ endpoint: 'https://push.example/new' })
    }
  })

  it('拉不到公钥时，浏览器给的 newSubscription 原样登记', async () => {
    const sw = load({ fetch: hub(() => Promise.reject(new TypeError('Failed to fetch'))) })
    const fresh = fakeSub('https://push.example/fresh', [9], sw.log)
    await sw.dispatch('pushsubscriptionchange', { oldSubscription: null, newSubscription: fresh })
    expect(sw.subscribeArgs).toEqual([])
    expect(JSON.parse(String(sw.fetches[1]?.init?.body))).toMatchObject({ endpoint: 'https://push.example/fresh' })
  })

  it('本机现有订阅的公钥与要用的不同：先退掉再订（否则 InvalidStateError）；相同就不退', async () => {
    let sw = load({ fetch: hub(), existingKey: [9, 9] })
    await sw.dispatch('pushsubscriptionchange', { oldSubscription: null })
    expect(sw.log.indexOf('unsubscribe https://push.example/existing')).toBeGreaterThan(-1)
    expect(sw.log.indexOf('unsubscribe https://push.example/existing')).toBeLessThan(sw.log.indexOf('subscribe'))
    sw = load({ fetch: hub(), existingKey: hubKey })
    await sw.dispatch('pushsubscriptionchange', { oldSubscription: null })
    expect(sw.log).not.toContain('unsubscribe https://push.example/existing')
    expect(sw.log).toContain('subscribe')
  })

  it('订阅失败、登记被拒：安静放弃（下次打开页面会提示失效），登记被拒时不删旧端点', async () => {
    const sub = load({ fetch: hub(), subscribeFails: true })
    await expect(sub.dispatch('pushsubscriptionchange', { oldSubscription: old([1]) })).resolves.toBe(true)
    expect(sub.fetches.map((f) => f.url)).toEqual(['/api/push/vapid'])
    const rejected = load({ fetch: hub(undefined, () => vapid({ error: '请先登录' }, 401)) })
    await expect(rejected.dispatch('pushsubscriptionchange', { oldSubscription: old([1]) })).resolves.toBe(true)
    expect(rejected.fetches.map((f) => f.init?.method ?? 'GET')).toEqual(['GET', 'POST'])
  })
})
