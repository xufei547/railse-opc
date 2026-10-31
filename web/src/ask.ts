// 答题表单（单选、多选、自己填、交回终端）的状态与规则。纯函数，便于不带浏览器环境测试；AskForm 只负责渲染与发请求。

import { classifyError } from './decide'
import type { AnswerBody, Option, Question, RequestView } from './types'

/** 一道题的答法：回答（带答案），或把问题交回终端。 */
export type AskVerb = 'answer' | 'passthrough'

/** 提问的题目；不是提问（没有 ask）时为空。 */
export function askQuestions(r: Pick<RequestView, 'ask'>): Question[] {
  return r.ask?.questions ?? []
}

/** 一道题的选项；options 为 null 或缺失时当作没有选项（只能自己填）。 */
export function questionOptions(q: Question): Option[] {
  return q.options ?? []
}

/** 已作答的提问里这道题的答案；没有时 undefined。按自己的键取：题面恰好叫 toString、__proto__ 时也不会取到原型上的东西。 */
export function answerFor(r: Pick<RequestView, 'answers'>, q: Question): string | undefined {
  const a = r.answers
  if (!a || !Object.prototype.hasOwnProperty.call(a, q.question)) return undefined
  const v = a[q.question]
  return typeof v === 'string' ? v : undefined
}

export interface AskState {
  /** 每题选中的选项序号，按选项顺序；单选至多一个。 */
  picked: number[][]
  /** 每题“自己填”的原文（不 trim，提交的就是它）。 */
  custom: string[]
  /** 在提交什么；null 是没在提交。提交中不能改答案，也不能再提交。 */
  busy: AskVerb | null
  /** 上次提交失败的原因（hub 的原文或兜底文案）；留在表单上，可以改了再试。 */
  error: string | null
}

export type AskAction =
  /** 点选项：单选时 on 为真就只选它；多选时 on 决定勾上还是取消。都会清掉这一题自己填的。 */
  | { type: 'pick'; question: number; option: number; multi: boolean; on: boolean }
  /** 改这一题自己填的；填了（去掉首尾空白后非空）就清掉这一题选的选项。 */
  | { type: 'custom'; question: number; text: string }
  | { type: 'submit'; verb: AskVerb }
  | { type: 'failed'; message: string }
  /** 只解除提交中、不写错误：原因由页面的结果提示说（409、404），表单里不再重复。 */
  | { type: 'settled' }

export function initAsk(questions: Question[]): AskState {
  return { picked: questions.map(() => []), custom: questions.map(() => ''), busy: null, error: null }
}

/** 换掉第 i 项，返回新数组。 */
function put<T>(list: T[], i: number, v: T): T[] {
  return list.map((x, j) => (j === i ? v : x))
}

/** 只用来判断“填了没有”：只有空白不算。 */
const filled = (text: string) => text.trim() !== ''

/** 每题都有答案：选了选项，或自己填了。 */
function complete(s: AskState): boolean {
  return s.picked.length > 0 && s.picked.every((p, i) => p.length > 0 || filled(s.custom[i] ?? ''))
}

export function askReducer(s: AskState, a: AskAction): AskState {
  switch (a.type) {
    case 'pick': {
      const cur = s.picked[a.question]
      if (s.busy || !cur) return s
      let next: number[]
      if (!a.on) next = cur.filter((j) => j !== a.option)
      else if (a.multi) next = [...new Set([...cur, a.option])].sort((x, y) => x - y)
      else next = [a.option]
      return { ...s, picked: put(s.picked, a.question, next), custom: put(s.custom, a.question, '') }
    }
    case 'custom': {
      if (s.busy || s.custom[a.question] === undefined) return s
      const picked = filled(a.text) ? put(s.picked, a.question, []) : s.picked
      return { ...s, picked, custom: put(s.custom, a.question, a.text) }
    }
    case 'submit':
      if (s.busy || (a.verb === 'answer' && !complete(s))) return s
      return { ...s, busy: a.verb, error: null }
    case 'failed':
      return { ...s, busy: null, error: a.message }
    case 'settled':
      return { ...s, busy: null }
    default:
      return s
  }
}

/** 一道题的答案：自己填了用原文；否则所选选项的标签按选项顺序用“, ”连接（多选题，官方口径是逗号连接）；都没有为 null。 */
function answerOf(q: Question, picked: number[], custom: string): string | null {
  if (filled(custom)) return custom
  const opts = questionOptions(q)
  const labels = picked.flatMap((j) => {
    const o = opts[j]
    return o ? [o.label] : []
  })
  return labels.length > 0 ? labels.join(', ') : null
}

/**
 * 题面 → 答案；还有题没答（或根本没有题）时 null。键逐字节是题面（hub 按它对题，不 trim、不改写）；
 * 用 Object.fromEntries 建：题面恰好是 __proto__ 时也是自己的键，JSON 里不会丢。
 */
export function answers(questions: Question[], s: AskState): Record<string, string> | null {
  if (questions.length === 0) return null
  const entries: [string, string][] = []
  for (const [i, q] of questions.entries()) {
    const a = answerOf(q, s.picked[i] ?? [], s.custom[i] ?? '')
    if (a === null) return null
    entries.push([q.question, a])
  }
  return Object.fromEntries(entries)
}

/** 还有几题没答（选了选项或自己填了才算答）；全答了为 0。 */
export function unansweredCount(questions: Question[], s: AskState): number {
  return questions.filter((_, i) => (s.picked[i] ?? []).length === 0 && !filled(s.custom[i] ?? '')).length
}

/** “回答”能不能点：不在提交中，且每题都有答案。 */
export function canAnswer(questions: Question[], s: AskState): boolean {
  return s.busy === null && answers(questions, s) !== null
}

/** 答题接口的请求体：回答带 answers（还有题没答时 null），交回终端只带 passthrough。 */
export function submitBody(verb: AskVerb, questions: Question[], s: AskState): AnswerBody | null {
  if (verb === 'passthrough') return { passthrough: true }
  const a = answers(questions, s)
  return a ? { answers: a } : null
}

/**
 * 提交一次（回答或交回终端），结果的口径与决定面板一致（classifyError）：成功带回作答后的请求；409、404 说明这条已经答不了（gone，
 * 页面显示红色结果提示并重新加载）；其余（400 答案不合规、连不上、超时、5xx）留在表单上显示 hub 的文案。
 */
export async function sendAsk(run: () => Promise<RequestView>): Promise<{ done: RequestView } | { gone: string } | { failed: string }> {
  try {
    return { done: await run() }
  } catch (e) {
    const c = classifyError(e, '提交失败')
    return c.gone ? { gone: c.message } : { failed: c.message }
  }
}

interface SubmitDeps {
  call: (body: AnswerBody) => Promise<RequestView>
  dispatch: (a: AskAction) => void
  /** 提交有了结果：成功带作答后的请求；答不了（gone）时带 null 与 hub 的文案。sent 是这次回答提交的答案（交回终端不带）。 */
  onDone: (updated: RequestView | null, message: string | undefined, attempted: AskVerb, sent?: Record<string, string>) => void
}

/**
 * 答题表单的提交：同一时刻只发一次（连点、提交中再点都不发）；成功交给 onDone；409、404（gone）只解除提交中、
 * 原因交给页面的结果提示（onDone），表单里不再写同一条错误；400、连不上等留在表单上显示 hub 的文案，可以改了再试。
 * 返回的函数每次用最新的 (题目, 状态) 调。
 */
export function askSubmitter({ call, dispatch, onDone }: SubmitDeps): (verb: AskVerb, questions: Question[], s: AskState) => Promise<void> {
  let flying = false
  return async (verb, questions, s) => {
    const body = submitBody(verb, questions, s)
    if (!body || s.busy || flying) return
    flying = true
    dispatch({ type: 'submit', verb })
    const r = await sendAsk(() => call(body))
    flying = false
    if ('done' in r) onDone(r.done, undefined, verb, body.answers)
    else if ('gone' in r) {
      dispatch({ type: 'settled' })
      onDone(null, r.gone, verb, body.answers)
    } else dispatch({ type: 'failed', message: r.failed })
  }
}
