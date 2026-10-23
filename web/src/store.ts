import type { Frame } from './ws'
import type { Inbox, Machine, RequestView, Rule, Status } from './types'

export interface State {
  loaded: boolean
  connected: boolean
  /** 待审批，按创建时间升序（等得最久的在前）。 */
  pending: RequestView[]
  /** 还在等回答的问题，按创建时间倒序（与 hub 一致）。 */
  questions: RequestView[]
  status: Status | null
  rules: Rule[] | null
  rulesStale: boolean
  machines: Machine[] | null
  error: string | null
  /** 每处理一帧有效的请求变化就加一；历史页、详情页据此重新拉取。 */
  tick: number
  /**
   * 每完成一次全量拉取（连上与重连、回到前台、网络恢复、收到 request_new……）加一。全量拉取不加 tick：
   * 详情、历史页要同时盯它，断线期间漏掉的变化才会被重拉。
   */
  loads: number
}

export const initialState: State = {
  loaded: false,
  connected: false,
  pending: [],
  questions: [],
  status: null,
  rules: null,
  rulesStale: false,
  machines: null,
  error: null,
  tick: 0,
  loads: 0,
}

export type Action =
  | { type: 'loaded'; inbox: Inbox; status: Status }
  | { type: 'rules'; rules: Rule[] }
  /** 规则可能变了（实时通道断开期间漏掉的 rules_updated）：规则页据此重新拉取。 */
  | { type: 'rules_stale' }
  | { type: 'machines'; machines: Machine[] }
  | { type: 'status'; status: Status }
  | { type: 'connected'; connected: boolean }
  | { type: 'error'; error: string | null }
  | { type: 'frame'; frame: Frame }

/** hub 的时间是定宽 UTC 文本，字典序即时间先后。 */
function byCreated(a: RequestView, b: RequestView) {
  return a.created_at < b.created_at ? -1 : a.created_at > b.created_at ? 1 : 0
}

function upsert(list: RequestView[], r: RequestView): RequestView[] {
  const rest = list.filter((x) => x.id !== r.id)
  return [...rest, r].sort(byCreated)
}

function isRequestView(x: unknown): x is RequestView {
  return typeof x === 'object' && x !== null && typeof (x as RequestView).id === 'string' && typeof (x as RequestView).kind === 'string'
}

/**
 * r 是不是问题 q 所在会话之后的事件。与 hub 的 OpenQuestions 一致：同一台机器、同一个非空会话 id。
 * 没有会话 id 的问题关联不到后续事件，一直等到滑出 24 小时（下一次全量拉取时消失）。
 */
function sameSession(q: RequestView, r: RequestView): boolean {
  return q.session_id !== '' && q.session_id === r.session_id && q.machine_id === r.machine_id
}

/**
 * 处理 /ws/client 的一帧。推送只是“有变化”的提示：这里先就地更新，让界面立即反映；
 * 调用方在收到 request_new、重连、回到前台时仍会全量拉取 /api/inbox 校正（守护进程补进的历史行不推送）。
 */
function applyFrame(state: State, frame: Frame): State {
  switch (frame.type) {
    case 'request_new': {
      if (!isRequestView(frame.data)) return state
      const r = frame.data
      const next = { ...state, tick: state.tick + 1 }
      // 同一会话来了任何新事件（question、done、permission），之前的问题就不再等了
      const questions = state.questions.filter((q) => !sameSession(q, r))
      if (r.kind === 'question') return { ...next, questions: [r, ...questions.filter((q) => q.id !== r.id)] }
      if (r.kind === 'done' || r.state !== 'pending') return { ...next, questions }
      return { ...next, questions, pending: upsert(state.pending, r) }
    }
    case 'request_updated': {
      if (!isRequestView(frame.data)) return state
      const r = frame.data
      const next = { ...state, tick: state.tick + 1 }
      if (r.state === 'pending') return { ...next, pending: upsert(state.pending, r) }
      return { ...next, pending: state.pending.filter((x) => x.id !== r.id) }
    }
    case 'rules_updated':
      return { ...state, rulesStale: true }
    case 'status': {
      const d = frame.data as { paused?: unknown } | null
      if (!state.status || typeof d?.paused !== 'boolean') return state
      return { ...state, status: { ...state.status, paused: d.paused } }
    }
    default:
      return state // 不认识的帧（ws.ts 已滤掉，这里再兜一层）
  }
}

export function reduce(state: State, action: Action): State {
  switch (action.type) {
    case 'loaded':
      return {
        ...state,
        loaded: true,
        error: null,
        pending: [...action.inbox.pending].sort(byCreated),
        questions: action.inbox.questions,
        status: action.status,
        loads: state.loads + 1,
      }
    case 'rules':
      return { ...state, rules: action.rules, rulesStale: false }
    case 'rules_stale':
      return { ...state, rulesStale: true }
    case 'machines':
      return { ...state, machines: action.machines }
    case 'status':
      return { ...state, status: action.status }
    case 'connected':
      return { ...state, connected: action.connected }
    case 'error':
      return { ...state, error: action.error }
    case 'frame':
      return applyFrame(state, action.frame)
    default:
      return state
  }
}

/**
 * 收件箱按项目分组，组内按创建时间升序（等得最久的在前），组按各自等得最久的那条排序；没有项目的归到“其他”，webhook 单独一组。
 * project 是组标题；key 唯一（项目恰好叫“webhook”或“其他”时也不与那两组合并），给 React 作 key。
 */
export function groupByProject(list: RequestView[]): { key: string; project: string; items: RequestView[] }[] {
  const groups = new Map<string, { project: string; items: RequestView[] }>()
  for (const r of list) {
    const [key, project] = r.kind === 'webhook' ? ['w', 'webhook'] : r.project ? [`p:${r.project}`, r.project] : ['n', '其他']
    const g = groups.get(key)
    if (g) g.items.push(r)
    else groups.set(key, { project, items: [r] })
  }
  return [...groups.entries()]
    .map(([key, g]) => ({ key, project: g.project, items: g.items.sort(byCreated) }))
    .sort((a, b) => byCreated(a.items[0]!, b.items[0]!))
}
