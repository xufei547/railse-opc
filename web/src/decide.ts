// 决定面板（批准、拒绝、备注、记住）的状态与规则。纯函数，便于不带浏览器环境测试；DecideSheet 只负责渲染与发请求。

import { ApiError } from './api'
import { isRedacted, outcomeText, stateLabel } from './format'
import { lang, t } from './i18n'
import type { AuditEvent, DecideBody, Draft, RequestView, TrustScope } from './types'

export type Verdict = 'allow' | 'deny'
export type Scope = 'project' | 'global'
/** 这次要做的事：审批的批准、拒绝，或提问的回答、交回终端（AskVerb）。结果提示据此认出“正是你这次做的”。 */
export type Attempt = Verdict | 'answer' | 'passthrough'

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
  /** 请求有没有 CLI 会话：没有时不能放行“这个会话”。 */
  hasSession: boolean
  /** 批准时能不能顺带限时放行（见 canTrust）。 */
  trustable: boolean
  trust: boolean
  trustScope: TrustScope
  trustMinutes: number
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
  | { type: 'trust'; on: boolean }
  | { type: 'trust_scope'; scope: TrustScope }
  | { type: 'trust_minutes'; minutes: number }
  | { type: 'submit' }
  | { type: 'failed'; message: string }

/** 只有不含隐藏部分的审批能“记住”：webhook 不学，含 <redacted> 的学出来匹配的是占位符（hub 也会 422）。 */
export function canRemember(r: RequestView): boolean {
  return rememberBlock(r) === null
}

/** 不能“记住”的原因（给开关下方的说明）；能记住时回 null。 */
export function rememberBlock(r: RequestView): string | null {
  if (r.kind === 'webhook') return t('webhook 请求不能记住')
  if (r.kind !== 'permission') return t('这类请求不能记住')
  if (isRedacted(r)) return t('含被隐藏部分的请求不能记住')
  return null
}

/** 放行的时长按钮（分钟）：30 分钟、2 小时（默认，同 hub 与守护进程）、24 小时（hub 与守护进程的上限）。 */
export const trustMinutesOptions = [30, 120, 1440] as const
const trustDefaultMinutes = 120

/**
 * 批准时能不能顺带限时放行：hub 说可以（can_trust：审批、机器在线、守护进程支持；老 hub 没有这个键）、是批准，
 * 且至少有会话或项目根之一可放行。拒绝不放行。
 */
export function canTrust(r: RequestView, action: Verdict): boolean {
  return action === 'allow' && r.kind === 'permission' && r.can_trust === true && (r.cli_session_id !== '' || r.project_root !== '')
}

export interface TrustScopeOption {
  scope: TrustScope
  label: string
  disabled: boolean
}

/** 放行的范围按钮：没有会话时“这个会话”不可选，没有项目根时“这个项目”不可选（hub 会回 400）。 */
export function trustScopeOptions(r: Pick<RequestView, 'cli_session_id' | 'project_root'>): TrustScopeOption[] {
  return [
    { scope: 'session', label: t('这个会话'), disabled: r.cli_session_id === '' },
    { scope: 'project', label: t('这个项目'), disabled: r.project_root === '' },
  ]
}

/** 时长按钮的文字。整句取（英文的单复数不同），不认识的按分钟说。 */
export function minutesLabel(m: number): string {
  switch (m) {
    case 30:
      return t('30 分钟')
    case 120:
      return t('2 小时')
    case 1440:
      return t('24 小时')
  }
  return t('{n} 分钟', { n: m })
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
    hasSession: r.cli_session_id !== '',
    trustable: canTrust(r, action),
    trust: false,
    // 有会话默认只放行这个会话（范围最小），没有才放行项目
    trustScope: r.cli_session_id !== '' ? 'session' : 'project',
    trustMinutes: trustDefaultMinutes,
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
    case 'trust':
      if (s.busy || a.on === s.trust || (a.on && !s.trustable)) return s
      return { ...s, trust: a.on, error: null }
    case 'trust_scope':
      if (s.busy || a.scope === s.trustScope || !(a.scope === 'session' ? s.hasSession : s.hasProject)) return s
      return { ...s, trustScope: a.scope, error: null }
    case 'trust_minutes':
      if (s.busy || a.minutes === s.trustMinutes || !(trustMinutesOptions as readonly number[]).includes(a.minutes)) return s
      return { ...s, trustMinutes: a.minutes, error: null }
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
 * decide 的请求体；不能确认时回 null。开着限时放行时带 trust（hub 先建放行再下发这条的 allow）。“记住”时 scope 取屏上草案的范围（hub 在 decide 时按它重新生成规则），
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
  if (s.trust && s.trustable) body.trust = { scope: s.trustScope, minutes: s.trustMinutes }
  return body
}

/**
 * propose、decide 出错时怎么办。409（已处理、已过期、机器已吊销）与 404（没有这条）说明这条已经决定不了：
 * gone 为真，关面板、把 hub 的文案告诉用户并重新加载。其余（422 记不住、400、500、连不上）留在面板里显示 hub 的文案，
 * 不解析文案。不是 ApiError 的（程序错误，多半是英文）用兜底文案。
 */
export function classifyError(e: unknown, fallback: string): { gone: boolean; message: string } {
  if (e instanceof ApiError) {
    const gone = e.status === 409 || e.status === 404
    return { gone, message: gone ? goneText(e.message) : e.message }
  }
  return { gone: false, message: fallback }
}

/**
 * “已经答不了”（409、404）时给用户看的文案。英文下 hub 的原文是中文（请求已过期、已经处理过、机器已吊销……），
 * 换成不带结果的一句（还没重拉到结果时用）；网页自己给的英文提示、中文界面下的原文照旧。
 */
function goneText(message: string): string {
  if (lang() === 'en' && /[\u3000-\u303f\u4e00-\u9fff\uff00-\uffef]/.test(message)) return t('这条请求已在别处处理')
  return message
}

/** 出错后值不值得重试：4xx（记不住、坏请求）是确定的结果；连不上、超时（status 0）、5xx 与其他错误可以再试。 */
export function retryable(e: unknown): boolean {
  return !(e instanceof ApiError && e.status >= 400 && e.status < 500)
}

/** 备注框的说明：审批的备注不会给 agent（钩子只看到固定文案）；webhook 的备注随回调发给来源。 */
export function noteHint(r: RequestView): string {
  return r.kind === 'webhook' ? t('备注（进审计，并随回调发给来源系统）') : t('备注（只进审计，不会给 agent 看）')
}

export interface ScopeOption {
  scope: Scope
  label: string
  disabled: boolean
}

/** 范围按钮：没有项目根时“项目”不可选（hub 缺省就记成全局）。 */
export function scopeOptions(r: RequestView): ScopeOption[] {
  return [
    { scope: 'project', label: r.project ? t('项目 {name}', { name: r.project }) : t('项目'), disabled: r.project_root === '' },
    { scope: 'global', label: t('全局'), disabled: false },
  ]
}

/**
 * 面板标题（批准这次命令）。按 (动作, 载荷种类) 整句取：英文的名词要小写、与“请求”“工具”这类标签的大小写不同，不拿短词拼。
 */
export function sheetTitle(action: Verdict, r: Pick<RequestView, 'kind' | 'payload'>): string {
  const allow = action === 'allow'
  switch (r.payload.type) {
    case 'shell':
      return allow ? t('批准这次命令') : t('拒绝这次命令')
    case 'file_write':
      return allow ? t('批准这次文件改动') : t('拒绝这次文件改动')
    case 'network':
      return allow ? t('批准这次访问') : t('拒绝这次访问')
    case 'mcp':
      return allow ? t('批准这次MCP 调用') : t('拒绝这次MCP 调用')
  }
  if (r.kind === 'webhook') return allow ? t('批准这次请求') : t('拒绝这次请求')
  return allow ? t('批准这次操作') : t('拒绝这次操作')
}

/** 草案摘要的开头（后面接一个空格与等宽的锚点）：“将放行命令”。同样整句取；不认识的载荷只说放行、拒绝。 */
export function draftLead(action: Verdict, payload: string): string {
  const allow = action === 'allow'
  switch (payload) {
    case 'shell':
      return allow ? t('将放行命令') : t('将拒绝命令')
    case 'file_write':
      return allow ? t('将放行文件改动') : t('将拒绝文件改动')
    case 'network':
      return allow ? t('将放行访问') : t('将拒绝访问')
    case 'mcp':
      return allow ? t('将放行MCP 调用') : t('将拒绝MCP 调用')
  }
  return allow ? t('将放行') : t('将拒绝')
}

/** 草案摘要的结尾：范围，紧跟在锚点后面（英文自带前导空格，中文是全角括号）。 */
export function draftScope(scope: Scope, project: string): string {
  return scope === 'project' ? t('（仅项目 {name}）', { name: project }) : t('（所有项目）')
}

/** 决定（或作答）成功后的提示。 */
export function doneMessage(r: RequestView): string {
  if (lang() === 'en') return doneMessageEn(r)
  if (r.kind === 'ask') {
    if (r.state === 'answered') return `已回答，${r.delivered_at ? '已送达 agent' : '答案正在送回 agent'}`
    if (r.state === 'passthrough') return '已交回终端，agent 会在终端里问你'
  }
  const verb = r.state === 'approved' ? '已批准' : r.state === 'denied' ? '已拒绝' : stateLabel(r.state)
  const learned = r.rule_id ? `，并记住为规则 ${r.rule_id}；` : '，'
  let tail: string
  if (r.kind === 'webhook') tail = r.delivered_at ? '已回调来源' : '正在回调来源'
  else tail = r.delivered_at ? '已送达 agent' : '决定正在送回 agent'
  return verb + learned + tail
}

/**
 * doneMessage 的英文：以 Approved / Denied（或状态名）开头，记住时接“, and remembered as rule …”，分号后说送没送到。
 * 句尾那半句是小写的从句，与 deliveryLabel 的独立标签（Delivered to the agent）大小写不同，所以不与它共用英文表的键。
 */
function doneMessageEn(r: RequestView): string {
  if (r.kind === 'ask') {
    if (r.state === 'answered') return `Answered; ${r.delivered_at ? 'delivered to the agent' : 'the answer is on its way to the agent'}`
    if (r.state === 'passthrough') return 'Handed back to the terminal; the agent will ask you there'
  }
  const verb = r.state === 'approved' ? 'Approved' : r.state === 'denied' ? 'Denied' : stateLabel(r.state)
  const learned = r.rule_id ? `, and remembered as rule ${r.rule_id}; ` : '; '
  let tail: string
  if (r.kind === 'webhook') tail = r.delivered_at ? 'sent back to the source' : 'sending it back to the source'
  else tail = r.delivered_at ? 'delivered to the agent' : 'the decision is on its way to the agent'
  return verb + learned + tail
}

/** 每种要做的事成功后请求的状态。 */
const attemptStates: Record<Attempt, RequestView['state']> = { allow: 'approved', deny: 'denied', answer: 'answered', passthrough: 'passthrough' }

/** 两份答案（题面 → 答案）逐题相同。 */
function sameAnswers(a: Record<string, string> | undefined, b: Record<string, string> | undefined): boolean {
  if (!a || !b) return false
  const keys = Object.keys(a)
  return keys.length === Object.keys(b).length && keys.every((k) => Object.prototype.hasOwnProperty.call(b, k) && a[k] === b[k])
}

/**
 * r 是不是用户在手机上做的、与这次要做的（attempted）同样的批准、拒绝、回答或交回终端：多半就是这次（上次提交超时，其实已经生效）。
 * 批准、拒绝、交回终端，同一个动作就是同一结果；回答还要答案与这次提交的（sent）完全相同——另一台设备可能答了不同内容。
 * 不知道这次要做什么时不认。
 */
function sameUserDecision(r: RequestView, attempted?: Attempt, sent?: Record<string, string>): boolean {
  if (r.decided_by !== 'user' || !attempted) return false
  if (r.state !== attemptStates[attempted]) return false
  return attempted !== 'answer' || sameAnswers(r.answers, sent)
}

/**
 * 面板开着时请求有了结果（终端里处理了、过期、机器吊销……）。正是用户这次要做的决定时说“你的决定已生效”，不说成“别处”；
 * 另一个页面做了相反的决定时仍是“别处”的结果（括号里是 outcomeText 的一句人话，如“你批准了”）。
 */
export function goneMessage(r: RequestView, attempted?: Attempt, sent?: Record<string, string>): string {
  const state = stateLabel(r.state)
  if (sameUserDecision(r, attempted, sent)) return t('你的决定已生效：{state}', { state })
  const outcome = outcomeText(r)
  return outcome ? t('这条请求在别处有了结果：{state}（{outcome}）', { state, outcome }) : t('这条请求在别处有了结果：{state}', { state })
}

/**
 * 一次决定的结果：成功（带 decide 的返回），或决定不了（409、404、面板开着时有了结果）。决定不了时带要告诉用户的文案，
 * 以及这次要做的动作（不知道时不带）。
 */
export type Outcome =
  | { kind: 'done'; request: RequestView }
  /** sent：这次回答提交的答案（只有回答带），用来认出“其实是我答的”与“别的设备答了不同内容”。 */
  | { kind: 'gone'; message: string; attempted?: Attempt; sent?: Record<string, string> }

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
    return { tone: 'ok', title: t('已生效'), text: doneMessage(r) }
  }
  if (current && sameUserDecision(current, o.attempted, o.sent)) return { tone: 'ok', title: t('你的决定已生效'), text: doneMessage(current) }
  // 英文时请求只要不是 pending 了，就用网页自己的英文句，不露 hub 的中文原文；中文仍只在用户自己做的决定上走 goneMessage
  const settled = current && current.state !== 'pending'
  if (settled && ((o.attempted && current.decided_by === 'user') || lang() === 'en')) {
    return { tone: 'warn', title: t('没有生效'), text: goneMessage(current, o.attempted, o.sent) }
  }
  return { tone: 'warn', title: t('没有生效'), text: goneText(o.message) }
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
