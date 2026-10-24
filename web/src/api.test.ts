import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, hub, request, requestTimeoutMs, setUnauthorizedHandler } from './api'

type Call = { url: string; init: RequestInit }
function stubFetch(status: number, body: unknown, calls: Call[] = []) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => {
    calls.push({ url, init })
    const text = body === undefined ? '' : typeof body === 'string' ? body : JSON.stringify(body)
    return new Response(status === 204 ? null : text, { status, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)
  return calls
}

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
  setUnauthorizedHandler(null)
})

/** 像真的 fetch 一样：一直不回，直到 signal 中止时以 AbortError reject。 */
function stubHangingFetch() {
  const fetchMock = vi.fn(
    (_url: string, init: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        init.signal?.addEventListener('abort', () => reject(new DOMException('The operation was aborted.', 'AbortError')))
      }),
  )
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('request', () => {
  it('发 JSON 并解析响应', async () => {
    const calls = stubFetch(200, { pending: [], questions: [] })
    const inbox = await hub.inbox()
    expect(inbox.pending).toEqual([])
    expect(inbox.questions).toEqual([])
    expect(calls[0]?.url).toBe('/api/inbox')
    expect(calls[0]?.init.credentials).toBe('same-origin')
  })

  it('204 返回 undefined', async () => {
    stubFetch(204, undefined)
    await expect(hub.pause()).resolves.toBeUndefined()
  })

  it('POST 带 JSON 体与 Content-Type', async () => {
    const calls = stubFetch(200, { id: 'r1' })
    await hub.decide('r 1', { action: 'allow', note: 'ok', remember: true, scope: 'global' })
    expect(calls[0]?.url).toBe('/api/requests/r%201/decide')
    expect(calls[0]?.init.method).toBe('POST')
    expect((calls[0]?.init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect(JSON.parse(calls[0]?.init.body as string)).toEqual({ action: 'allow', note: 'ok', remember: true, scope: 'global' })
  })

  it('propose 带 action 与 scope，回规则草案', async () => {
    const draft = { rule_id: 'learned-x', scope: 'project', yaml: 'id: learned-x\n', summary: { action: 'allow', payload: 'shell', anchor: 'git push' } }
    const calls = stubFetch(200, draft)
    const d = await hub.propose('r1', { action: 'allow', scope: 'project' })
    expect(d).toEqual(draft)
    expect(calls[0]?.url).toBe('/api/requests/r1/propose')
    expect(JSON.parse(calls[0]?.init.body as string)).toEqual({ action: 'allow', scope: 'project' })
  })

  it('导入规则用 YAML 原文', async () => {
    const calls = stubFetch(200, { version: 3 })
    const r = await hub.importRules('version: 1\nrules: []\n')
    expect(r.version).toBe(3)
    expect((calls[0]?.init.headers as Record<string, string>)['Content-Type']).toBe('application/yaml')
    expect(calls[0]?.init.body).toBe('version: 1\nrules: []\n')
  })

  it('导入规则可以直接发选中的文件：原样字节、YAML 类型（不先按 UTF-8 解码再编码）', async () => {
    const calls = stubFetch(200, { version: 4 })
    const file = new Blob([new Uint8Array([0x69, 0x64, 0x3a, 0xff])], { type: 'application/octet-stream' })
    const r = await hub.importRules(file)
    expect(r.version).toBe(4)
    expect((calls[0]?.init.headers as Record<string, string>)['Content-Type']).toBe('application/yaml')
    expect(calls[0]?.init.body).toBe(file)
  })

  it('列请求带 state 与 limit', async () => {
    const calls = stubFetch(200, [])
    await hub.requests('auto_allowed', 20)
    await hub.requests()
    expect(calls[0]?.url).toBe('/api/requests?state=auto_allowed&limit=20')
    expect(calls[1]?.url).toBe('/api/requests?state=&limit=50')
  })

  it('推送接口的路径、方法与请求体', async () => {
    const calls = stubFetch(204, undefined)
    const sub = { endpoint: 'https://push.example/x', keys: { p256dh: 'p', auth: 'a' } }
    await hub.pushSubscribe(sub)
    await hub.pushUnsubscribe('https://push.example/x')
    await hub.pushTest('https://push.example/x')
    expect(calls.map((c) => [c.init.method, c.url])).toEqual([
      ['POST', '/api/push/subscribe'],
      ['DELETE', '/api/push/subscribe'],
      ['POST', '/api/push/test'],
    ])
    expect(JSON.parse(calls[0]?.init.body as string)).toEqual(sub)
    expect(JSON.parse(calls[1]?.init.body as string)).toEqual({ endpoint: 'https://push.example/x' })
  })

  it('非 2xx 抛 ApiError 并带服务端文案', async () => {
    stubFetch(422, { error: '不能记住：含凭据的命令不能记住' })
    const err = await hub.propose('r1', { action: 'allow' }).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(422)
    expect((err as ApiError).message).toBe('不能记住：含凭据的命令不能记住')
  })

  it('响应不是 JSON 时用默认文案', async () => {
    stubFetch(502, 'bad gateway')
    const err = await request('GET', '/api/x').catch((e: unknown) => e)
    expect((err as ApiError).message).toBe('请求失败（502）')
  })

  it('2xx 但响应不是 JSON 时抛 ApiError 而不是英文的解析错误', async () => {
    stubFetch(200, '<html></html>')
    const err = await request('GET', '/api/x').catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(200)
    expect((err as ApiError).message).toBe('hub 的响应不是 JSON（200）')
  })

  it('网络不通时抛 status 为 0 的中文 ApiError', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new TypeError('Failed to fetch')
    }))
    const err = await hub.inbox().catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(0)
    expect((err as ApiError).message).toBe('连不上 hub，请检查网络')
  })

  it('请求超时（网络黑洞）时中止并抛中文 ApiError，而不是一直挂着', async () => {
    vi.useFakeTimers()
    const fetchMock = stubHangingFetch()
    const p = hub.status().catch((e: unknown) => e)
    await vi.advanceTimersByTimeAsync(requestTimeoutMs - 1)
    expect(fetchMock.mock.calls[0]?.[1].signal?.aborted).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    const err = await p
    expect(requestTimeoutMs).toBe(15_000)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(0)
    expect((err as ApiError).message).toBe('请求超时：hub 没有响应，请检查网络')
  })

  it('响应体迟迟读不完也算超时', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn(async (_url: string, init: RequestInit) => {
      const body = new ReadableStream({
        start(c) {
          c.enqueue(new TextEncoder().encode('{"pending":'))
          init.signal?.addEventListener('abort', () => c.error(new DOMException('The operation was aborted.', 'AbortError')))
        },
      })
      return new Response(body, { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    const p = hub.inbox().catch((e: unknown) => e)
    await vi.advanceTimersByTimeAsync(requestTimeoutMs)
    const err = await p
    expect((err as ApiError).message).toBe('请求超时：hub 没有响应，请检查网络')
  })

  it('请求完成后清掉超时定时器', async () => {
    vi.useFakeTimers()
    stubFetch(200, { pending: [], questions: [] })
    await hub.inbox()
    stubFetch(204, undefined)
    await hub.pause()
    stubFetch(500, { error: 'x' })
    await hub.status().catch(() => undefined)
    expect(vi.getTimerCount()).toBe(0)
  })

  it('导入规则（至多 1 MiB）的超时更长', async () => {
    vi.useFakeTimers()
    stubHangingFetch()
    const p = hub.importRules('version: 1\nrules: []\n').catch((e: unknown) => e)
    let settled = false
    void p.then(() => {
      settled = true
    })
    await vi.advanceTimersByTimeAsync(requestTimeoutMs)
    expect(settled).toBe(false)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(((await p) as ApiError).message).toBe('请求超时：hub 没有响应，请检查网络')
  })

  it('401 先调登录失效回调', async () => {
    stubFetch(401, { error: '请先登录' })
    const onUnauthorized = vi.fn()
    setUnauthorizedHandler(onUnauthorized)
    await expect(hub.status()).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })

  it('登录密码错（401）不调登录失效回调', async () => {
    stubFetch(401, { error: '密码不对' })
    const onUnauthorized = vi.fn()
    setUnauthorizedHandler(onUnauthorized)
    const err = await hub.login('x').catch((e: unknown) => e)
    expect((err as ApiError).status).toBe(401)
    expect(onUnauthorized).not.toHaveBeenCalled()
  })
})
