import type { AuditEvent, DecideBody, Draft, Inbox, Machine, RequestView, Rule, Status, VapidInfo } from './types'

/** hub 回了非 2xx（status 为 HTTP 状态码），或根本连不上（status 为 0）。message 是给人看的中文文案。 */
export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
    this.name = 'ApiError'
  }
}

let unauthorized: (() => void) | null = null

/** 401 时调用（跳登录页）。 */
export function setUnauthorizedHandler(fn: (() => void) | null) {
  unauthorized = fn
}

/** 一次请求（含读完响应体）的时限。网络黑洞时 fetch 可能一直不回，首屏会卡在“正在连接 hub…”。 */
export const requestTimeoutMs = 15_000
const importTimeoutMs = 75_000 // 导入规则的请求体至多 1 MiB，慢网络上要多给些时间

const timeoutMsg = '请求超时：hub 没有响应，请检查网络'

interface Options {
  body?: unknown
  contentType?: string
  raw?: BodyInit
  /** 401 时不调登录失效回调（登录接口的 401 是密码不对，不是登录失效）。 */
  noUnauthorizedHandler?: boolean
  /** 时限（毫秒），默认 requestTimeoutMs。 */
  timeoutMs?: number
}

export async function request<T>(method: string, path: string, opts: Options = {}): Promise<T> {
  const headers: Record<string, string> = {}
  let body: BodyInit | undefined
  if (opts.raw !== undefined) {
    body = opts.raw
    headers['Content-Type'] = opts.contentType ?? 'application/octet-stream'
  } else if (opts.body !== undefined) {
    body = JSON.stringify(opts.body)
    headers['Content-Type'] = 'application/json'
  }
  // 用 AbortController + setTimeout 而不是 AbortSignal.timeout：iOS 16 之前没有后者。中止同时打断读响应体。
  const ctrl = new AbortController()
  const timer = setTimeout(() => ctrl.abort(), opts.timeoutMs ?? requestTimeoutMs)
  try {
    let resp: Response
    try {
      resp = await fetch(path, { method, headers, body, credentials: 'same-origin', signal: ctrl.signal })
    } catch {
      if (ctrl.signal.aborted) throw new ApiError(0, timeoutMsg)
      // fetch 只在网络层失败时 reject（断网、hub 没开、被代理掐断），浏览器给的是英文的 TypeError
      throw new ApiError(0, '连不上 hub，请检查网络')
    }
    if (resp.status === 204) return undefined as T
    if (!resp.ok) {
      let msg = `请求失败（${resp.status}）`
      try {
        const j = (await resp.json()) as { error?: unknown }
        if (typeof j?.error === 'string' && j.error) msg = j.error
      } catch {
        // 不是 JSON（或读到一半超时），用默认文案
      }
      if (resp.status === 401 && !opts.noUnauthorizedHandler) unauthorized?.()
      throw new ApiError(resp.status, msg)
    }
    try {
      return (await resp.json()) as T
    } catch {
      if (ctrl.signal.aborted) throw new ApiError(0, timeoutMsg)
      throw new ApiError(resp.status, `hub 的响应不是 JSON（${resp.status}）`)
    }
  } finally {
    clearTimeout(timer)
  }
}

const get = <T>(p: string) => request<T>('GET', p)
const post = <T>(p: string, body?: unknown) => request<T>('POST', p, { body })
const del = <T>(p: string, body?: unknown) => request<T>('DELETE', p, { body })
const patch = <T>(p: string, body?: unknown) => request<T>('PATCH', p, { body })
const enc = encodeURIComponent

/** hub 的网页接口（docs/hub.md 的 API 表）。全部同源、带 cookie。 */
export const hub = {
  login: (password: string) => request<void>('POST', '/api/login', { body: { password }, noUnauthorizedHandler: true }),
  logout: () => post<void>('/api/logout'),
  status: () => get<Status>('/api/status'),
  inbox: () => get<Inbox>('/api/inbox'),
  requests: (state = '', limit = 50) => get<RequestView[]>(`/api/requests?state=${enc(state)}&limit=${limit}`),
  request: (id: string) => get<{ request: RequestView; audit: AuditEvent[] }>(`/api/requests/${enc(id)}`),
  decide: (id: string, body: DecideBody) => post<RequestView>(`/api/requests/${enc(id)}/decide`, body),
  propose: (id: string, body: { action: 'allow' | 'deny'; scope?: 'project' | 'global' }) => post<Draft>(`/api/requests/${enc(id)}/propose`, body),
  rules: () => get<Rule[]>('/api/rules'),
  setRuleEnabled: (id: string, enabled: boolean) => patch<void>(`/api/rules/${enc(id)}`, { enabled }),
  deleteRule: (id: string) => del<void>(`/api/rules/${enc(id)}`),
  importRules: (yaml: string) =>
    request<{ version: number }>('POST', '/api/rules/import', { raw: yaml, contentType: 'application/yaml', timeoutMs: importTimeoutMs }),
  exportRulesURL: '/api/rules/export',
  machines: () => get<Machine[]>('/api/machines'),
  revokeMachine: (id: string) => del<void>(`/api/machines/${enc(id)}`),
  pause: () => post<void>('/api/pause'),
  resume: () => post<void>('/api/resume'),
  pushVapid: () => get<VapidInfo>('/api/push/vapid'),
  /** sub 是 PushSubscription.toJSON() 的原样结果。 */
  pushSubscribe: (sub: unknown) => post<void>('/api/push/subscribe', sub),
  pushUnsubscribe: (endpoint: string) => del<void>('/api/push/subscribe', { endpoint }),
  pushCheck: (endpoint: string) => post<{ active: boolean }>('/api/push/check', { endpoint }),
  pushTest: (endpoint: string) => post<void>('/api/push/test', { endpoint }),
}
