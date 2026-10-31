// 决定面板（批准、拒绝、备注、记住）的状态与规则。纯函数，便于不带浏览器环境测试；DecideSheet 只负责渲染与发请求。

import { ApiError } from './api'
import { isRedacted, outcomeText, stateLabel } from './format'
import type { AuditEvent, DecideBody, Draft, RequestView } from './types'

export type Verdict = 'allow' | 'deny'
export type Scope = 'project' | 'global'

export interface SheetState {
  action: Verdict
  /** 请求有没有项目根：没有时不能选“项目”范围（hub 会回 422）。 */
  hasProject: boolean
  /** 这条请求能不能“记住”（见 canRemember）。 */
  rememberable: boolean
  remember: boolean
  scope: Scope
  /**
   * 草案序号。每次需要新草案（打开记住、换范围、重试）加一，同时清掉旧草案；只收序号等于当前的回应，
   * 乱序晚到的旧回应丢掉。所以不会出现“屏上是旧范围的草案、选中的却是新范围”还能确认的一刻。
   */
  seq: number
  draft: Draft | null
  /** draft 回应的是哪个序号。 */
  draftSeq: number
  draftError: string | null
  /** draftError 能不能重试：记不住（422）等 4xx 是确定的结果，重试没用。 */
  draftRetryable: boolean
  busy: boolean
  error: string | null
}

export type SheetAction =
  | { type: 'remember'; on: boolean }
  | { type: 'scope'; scope: Scope }
  /** 草案出错后再取一次。 */
  | { type: 'retry' }
  | { type: 'draft'; seq: number; draft: Draft }
  | { type: 'draft_error'; seq: number; message: string; retryable: boolean }
  | { type: 'submit' }
  | { type: 'failed'; message: string }

/** 只有不含隐藏部分的审批能“记住”：webhook 不学，含 <redacted> 的学出来匹配的是占位符（hub 也会 422）。 */
export function canRemember(r: RequestView): boolean {
  return rememberBlock(r) === null
}

/** 不能“记住”的原因（给开关下方的说明）；能记住时回 null。 */
export function rememberBlock(r: RequestView): string | null {
  if (r.kind === 'webhook') return 'webhook 请求不能记住'
  if (r.kind !== 'permission') return '这类请求不能记住'
  if (isRedacted(r)) return '含被隐藏部分的请求不能记住'
  return null
}

export function initSheet(r: RequestView, action: Verdict): SheetState {
  const hasProject = r.project_root !== ''
  return {
    action,
    hasProject,
    rememberable: canRemember(r),
    remember: false,
    // 与 hub 缺省时一致：有项目根记成项目，没有记成全局
    scope: hasProject ? 'project' : 'global',
    seq: 0,
    draft: null,
    draftSeq: -1,
    draftError: null,
    draftRetryable: false,
    busy: false,
    error: null,
  }
}

/** 换一个序号、清掉旧草案与错误：之后只认新序号的回应。 */
function renew(s: SheetState): SheetState {
  return { ...s, seq: s.seq + 1, draft: null, draftSeq: -1, draftError: null, draftRetryable: false, error: null }
}

export function sheetReducer(s: SheetState, a: SheetAction): SheetState {
  switch (a.type) {
    case 'remember':
      if (s.busy || a.on === s.remember || (a.on && !s.rememberable)) return s
      return { ...renew(s), remember: a.on }
    case 'scope':
      if (s.busy || a.scope === s.scope || (a.scope === 'project' && !s.hasProject)) return s
      return { ...renew(s), scope: a.scope }
    case 'retry':
      if (s.busy || !s.remember || s.draftError === null || !s.draftRetryable) return s
      return renew(s)
    case 'draft':
      if (a.seq !== s.seq || !s.remember) return s
      return { ...s, draft: a.draft, draftSeq: a.seq, draftError: null }
    case 'draft_error':
      if (a.seq !== s.seq || !s.remember) return s
      return { ...s, draft: null, draftSeq: -1, draftError: a.message, draftRetryable: a.retryable }
    case 'submit':
      if (!canConfirm(s)) return s
      return { ...s, busy: true, error: null }
    case 'failed':
      return { ...s, busy: false, error: a.message }
    default:
      return s
  }
}

/**
 * 当前该展示的草案：开着“记住”、回应的是当前序号，而且 hub 给的范围与动作就是所选的。否则回 null（显示“正在生成”或错误）。
 */
export function draftShown(s: SheetState): Draft | null {
  if (!s.remember || !s.draft || s.draftSeq !== s.seq) return null
  if (s.draft.scope !== s.scope || s.draft.summary.action !== s.action) return null
  return s.draft
}

/** 能不能点确认：不在提交中；开着“记住”时还要屏上的草案正是所选的 (动作, 范围)。 */
export function canConfirm(s: SheetState): boolean {
  if (s.busy) return false
  return !s.remember || draftShown(s) !== null
}

/**
 * decide 的请求体；不能确认时回 null。“记住”时 scope 取屏上草案的范围（hub 在 decide 时按它重新生成规则），
 * 不取组件里的选择，保证存下的就是用户看到的那条。备注去掉首尾空白，空的不带。
 */
export function decideBody(s: SheetState, note: string): DecideBody | null {
  if (!canConfirm(s)) return null
  const n = note.trim()
  const body: DecideBody = { action: s.action }
  if (n) body.note = n
  const d = draftShown(s)
  if (s.remember && d) {
    body.remember = true
    body.scope = d.scope
  }
  return body
}

/**
 * propose、decide 出错时怎么办。409（已处理、已过期、机器已吊销）与 404（没有这条）说明这条已经决定不了：
 * gone 为真，关面板、把 hub 的文案告诉用户并重新加载。其余（422 记不住、400、500、连不上）留在面板里显示 hub 的文案，
 * 不解析文案。不是 ApiError 的（程序错误，多半是英文）用兜底文案。
 */
export function classifyError(e: unknown, fallback: string): { gone: boolean; message: string } {
  if (e instanceof ApiError) return { gone: e.status === 409 || e.status === 404, message: e.message }
  return { gone: false, message: fallback }
}

/** 出错后值不值得重试：4xx（记不住、坏请求）是确定的结果；连不上、超时（status 0）、5xx 与其他错误可以再试。 */
export function retryable(e: unknown): boolean {
  return !(e instanceof ApiError && e.status >= 400 && e.status < 500)
}

/** 备注框的说明：审批的备注不会给 agent（钩子只看到固定文案）；webhook 的备注随回调发给来源。 */
export function noteHint(r: RequestView): string {
  return r.kind === 'webhook' ? '备注（进审计，并随回调发给来源系统）' : '备注（只进审计，不会给 agent 看）'
}

export interface ScopeOption {
  scope: Scope
  label: string
  disabled: boolean
}

/** 范围按钮：没有项目根时“项目”不可选（hub 缺省就记成全局）。 */
export function scopeOptions(r: RequestView): ScopeOption[] {
  return [
    { scope: 'project', label: r.project ? `项目 ${r.project}` : '项目', disabled: r.project_root === '' },
    { scope: 'global', label: '全局', disabled: false },
  ]
}

/** 决定成功后的提示。 */
export function doneMessage(r: RequestView): string {
  const verb = r.state === 'approved' ? '已批准' : r.state === 'denied' ? '已拒绝' : stateLabel(r.state)
  const learned = r.rule_id ? `，并记住为规则 ${r.rule_id}；` : '，'
  let tail: string
  if (r.kind === 'webhook') tail = r.delivered_at ? '已回调来源' : '正在回调来源'
  else tail = r.delivered_at ? '已送达 agent' : '决定正在送回 agent'
  return verb + learned + tail
}

/**
 * r 是不是用户在手机上做的、与这次要做的（attempted）同样的批准或拒绝：多半就是这次（上次提交超时，其实已经生效）。
 * 不知道这次要做什么时不认。
 */
function sameUserDecision(r: RequestView, attempted?: Verdict): boolean {
  if (r.decided_by !== 'user' || !attempted) return false
  return (r.state === 'approved' && attempted === 'allow') || (r.state === 'denied' && attempted === 'deny')
}

/**
 * 面板开着时请求有了结果（终端里处理了、过期、机器吊销……）。正是用户这次要做的决定时说“你的决定已生效”，不说成“别处”；
 * 另一个页面做了相反的决定时仍是“别处”的结果（括号里是 outcomeText 的一句人话，如“你批准了”）。
 */
export function goneMessage(r: RequestView, attempted?: Verdict): string {
  const state = stateLabel(r.state)
  if (sameUserDecision(r, attempted)) return `你的决定已生效：${state}`
  const outcome = outcomeText(r)
  return `这条请求在别处有了结果：${state}${outcome ? `（${outcome}）` : ''}`
}

/**
 * 一次决定的结果：成功（带 decide 的返回），或决定不了（409、404、面板开着时有了结果）。决定不了时带要告诉用户的文案，
 * 以及这次要做的动作（不知道时不带）。
 */
export type Outcome = { kind: 'done'; request: RequestView } | { kind: 'gone'; message: string; attempted?: Verdict }

export interface OutcomeView {
  tone: 'ok' | 'warn'
  title: string
  text: string
}

/**
 * 结果提示的内容，按当前（重新拉到的）请求算：
 *   - 成功：绿色“已生效”，送达后文案跟着变成“已送达”；
 *   - 决定不了：红色“没有生效”，带 hub 的文案（请求已过期、已经处理过、机器已吊销……）；
 *     重新拉到的正是用户这次要做的批准或拒绝时（提交超时其实已生效，再点得到 409），改成绿色“你的决定已生效”；
 *     是另一个页面做的相反决定时，说明现在的结果。
 */
export function outcomeView(o: Outcome, current: RequestView | null): OutcomeView {
  if (o.kind === 'done') {
    const r = current && current.id === o.request.id && current.state !== 'pending' ? current : o.request
    return { tone: 'ok', title: '已生效', text: doneMessage(r) }
  }
  if (current && sameUserDecision(current, o.attempted)) return { tone: 'ok', title: '你的决定已生效', text: doneMessage(current) }
  if (current && o.attempted && current.decided_by === 'user' && current.state !== 'pending') {
    return { tone: 'warn', title: '没有生效', text: goneMessage(current, o.attempted) }
  }
  return { tone: 'warn', title: '没有生效', text: o.message }
}

/** 按键是不是要关面板的 Esc。输入法组字时的 Esc 是取消候选（isComposing，或 Safari 的 keyCode 229），不能关面板丢掉备注。 */
export function isEscape(e: { key: string; isComposing?: boolean; keyCode?: number }): boolean {
  return e.key === 'Escape' && !e.isComposing && e.keyCode !== 229
}

export interface Detail {
  request: RequestView
  audit: AuditEvent[]
}

/**
 * 详情页收到新数据时用哪份。请求只会从 pending 变成终态、不会回去：决定成功后先就地换成 decide 的返回，
 * 这之前发出的拉取晚到时带的是旧的“等待决定”，不能把它盖回去（否则批准、拒绝按钮会闪回来）。
 */
export function keepDecided(prev: Detail | null, next: Detail): Detail {
  if (prev && prev.request.id === next.request.id && prev.request.state !== 'pending' && next.request.state === 'pending') return prev
  return next
}
