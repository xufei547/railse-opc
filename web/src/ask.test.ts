import { describe, expect, it, vi } from 'vitest'
import { ApiError } from './api'
import {
  answerFor,
  answers,
  askQuestions,
  askReducer,
  askSubmitter,
  canAnswer,
  initAsk,
  questionOptions,
  sendAsk,
  submitBody,
  unansweredCount,
  type AskAction,
  type AskState,
} from './ask'
import type { Question, RequestView } from './types'

function req(over: Partial<RequestView> = {}): RequestView {
  return {
    id: 'a1', machine_id: 'm', session_id: 's', cli: 'claude', kind: 'ask', tool_name: 'AskUserQuestion',
    payload: { type: '' }, risk: '', project_root: '/p/blog', engine_reason: '',
    state: 'pending', decided_by: '', decision_note: '', rule_id: '', turn_id: 't', cli_session_id: 's', title: '',
    created_at: '2026-10-31T10:00:00.000000000Z', expires_at: null, decided_at: null, delivered_at: null,
    machine_name: 'mac', project: 'blog', has_redacted: false, ...over,
  }
}

// 真机上 AskUserQuestion 的题（Claude Code 2.1.280），外加一道多选、一道没有选项的
const fruit: Question = {
  question: '你更喜欢苹果还是香蕉？',
  header: '水果',
  options: [{ label: 'Apple', description: '苹果' }, { label: 'Banana', description: '香蕉' }],
}
const many: Question = { question: '要哪些？', options: [{ label: 'x' }, { label: 'y' }, { label: 'z' }], multiSelect: true }
const free: Question = { question: '还有什么要说的？', options: [] }
const qs = [fruit, many, free]

function run(s: AskState, ...actions: AskAction[]): AskState {
  return actions.reduce(askReducer, s)
}
const pick = (question: number, option: number, multi = false, on = true): AskAction => ({ type: 'pick', question, option, multi, on })
const type = (question: number, text: string): AskAction => ({ type: 'custom', question, text })

describe('askQuestions 与 questionOptions', () => {
  it('题目取自 ask.questions；没有 ask 时为空', () => {
    expect(askQuestions(req({ ask: { questions: qs } }))).toEqual(qs)
    expect(askQuestions(req())).toEqual([])
  })

  it('options 为 null 或缺失时当作没有选项', () => {
    expect(questionOptions({ question: 'q', options: null })).toEqual([])
    expect(questionOptions({ question: 'q' })).toEqual([])
    expect(questionOptions(fruit).map((o) => o.label)).toEqual(['Apple', 'Banana'])
  })
})

describe('answerFor', () => {
  it('按题面取答案；没有答案、题面恰好是对象原型上的名字时也不取到别的东西', () => {
    const r = req({ state: 'answered', answers: { '你更喜欢苹果还是香蕉？': 'Banana' } })
    expect(answerFor(r, fruit)).toBe('Banana')
    expect(answerFor(r, many)).toBeUndefined()
    expect(answerFor(r, { question: 'toString' })).toBeUndefined()
    expect(answerFor(r, { question: '__proto__' })).toBeUndefined()
    expect(answerFor(req(), fruit)).toBeUndefined()
  })
})

describe('askReducer', () => {
  it('初始：每题都没选、没填，不在提交，没有错误', () => {
    expect(initAsk(qs)).toEqual({ picked: [[], [], []], custom: ['', '', ''], busy: null, error: null })
  })

  it('单选：点哪个就是哪个', () => {
    const s = run(initAsk(qs), pick(0, 0), pick(0, 1))
    expect(s.picked[0]).toEqual([1])
  })

  it('多选：可以选几个，按选项顺序记；再点一次取消', () => {
    let s = run(initAsk(qs), pick(1, 2, true), pick(1, 0, true))
    expect(s.picked[1]).toEqual([0, 2])
    s = run(s, pick(1, 2, true, false))
    expect(s.picked[1]).toEqual([0])
    s = run(s, pick(1, 0, true), pick(1, 0, true)) // 重复勾不重复记
    expect(s.picked[1]).toEqual([0])
  })

  it('填了“自己填”就清掉这一题选的选项；再点选项则清空自己填；别的题不动', () => {
    let s = run(initAsk(qs), pick(0, 1), pick(1, 0, true), type(0, '都不喜欢'))
    expect(s.picked[0]).toEqual([])
    expect(s.custom[0]).toBe('都不喜欢')
    expect(s.picked[1]).toEqual([0])
    s = run(s, pick(0, 0))
    expect(s.picked[0]).toEqual([0])
    expect(s.custom[0]).toBe('')
  })

  it('只打了空白不算填了：不清掉选项', () => {
    const s = run(initAsk(qs), pick(0, 1), type(0, '   '))
    expect(s.picked[0]).toEqual([1])
    expect(s.custom[0]).toBe('   ')
  })

  it('题号越界的动作不理', () => {
    const s0 = initAsk(qs)
    expect(run(s0, pick(5, 0), type(-1, 'x'))).toBe(s0)
  })

  it('提交中不能改；提交、失败的状态', () => {
    let s = run(initAsk(qs), pick(0, 1), pick(1, 0, true), type(2, 'x'), { type: 'submit', verb: 'answer' })
    expect(s.busy).toBe('answer')
    expect(run(s, pick(0, 0))).toBe(s)
    expect(run(s, type(0, 'y'))).toBe(s)
    expect(run(s, { type: 'submit', verb: 'passthrough' })).toBe(s)
    s = run(s, { type: 'failed', message: '答案不合规：第 3 题的答案超过 1000 个字符' })
    expect(s.busy).toBeNull()
    expect(s.error).toBe('答案不合规：第 3 题的答案超过 1000 个字符')
    s = run(s, { type: 'submit', verb: 'passthrough' })
    expect(s.busy).toBe('passthrough')
    expect(s.error).toBeNull()
  })

  it('还有题没答时提交“回答”不理；交回终端随时可以', () => {
    const s0 = run(initAsk(qs), pick(0, 1))
    expect(run(s0, { type: 'submit', verb: 'answer' })).toBe(s0)
    expect(run(s0, { type: 'submit', verb: 'passthrough' }).busy).toBe('passthrough')
  })
})

describe('answers 与 canAnswer', () => {
  it('每题都有答案才能回答：单选是所选标签，多选按选项顺序用“, ”连接，自己填用原文', () => {
    let s = run(initAsk(qs), pick(0, 1), pick(1, 2, true), pick(1, 0, true))
    expect(answers(qs, s)).toBeNull()
    expect(canAnswer(qs, s)).toBe(false)
    s = run(s, type(2, '  早点做完 \n'))
    expect(answers(qs, s)).toEqual({ '你更喜欢苹果还是香蕉？': 'Banana', '要哪些？': 'x, z', '还有什么要说的？': '  早点做完 \n' })
    expect(canAnswer(qs, s)).toBe(true)
    expect(canAnswer(qs, run(s, { type: 'submit', verb: 'answer' }))).toBe(false)
  })

  it('自己填的优先于选项（填了就清掉选项）；只有空白算没答', () => {
    const one = [fruit]
    expect(answers(one, run(initAsk(one), pick(0, 0), type(0, '都不喜欢，我只吃橙子')))).toEqual({ '你更喜欢苹果还是香蕉？': '都不喜欢，我只吃橙子' })
    expect(answers(one, run(initAsk(one), type(0, ' \t ')))).toBeNull()
  })

  it('键逐字节是题面（不 trim、不改写，含 <redacted> 也一样）；题面是 __proto__ 也是自己的键', () => {
    const odd: Question[] = [{ question: '  token=<redacted> 还能用吗？ ', options: [{ label: '能' }] }, { question: '__proto__', options: null }]
    const body = answers(odd, run(initAsk(odd), pick(0, 0), type(1, 'x')))
    expect(Object.keys(body!)).toEqual(['  token=<redacted> 还能用吗？ ', '__proto__'])
    expect(JSON.stringify(body)).toBe('{"  token=<redacted> 还能用吗？ ":"能","__proto__":"x"}')
  })

  it('没有题目时不能回答', () => {
    expect(answers([], initAsk([]))).toBeNull()
    expect(canAnswer([], initAsk([]))).toBe(false)
  })
})

describe('submitBody', () => {
  it('回答带 answers；交回终端只带 passthrough', () => {
    const one = [fruit]
    const s = run(initAsk(one), pick(0, 1))
    expect(submitBody('answer', one, s)).toEqual({ answers: { '你更喜欢苹果还是香蕉？': 'Banana' } })
    expect(submitBody('passthrough', one, s)).toEqual({ passthrough: true })
    expect(submitBody('passthrough', one, initAsk(one))).toEqual({ passthrough: true })
  })

  it('还有题没答时回答没有请求体', () => {
    expect(submitBody('answer', qs, initAsk(qs))).toBeNull()
  })
})

describe('sendAsk：结果的口径与决定面板一致', () => {
  it('成功带回作答后的请求', async () => {
    const r = req({ state: 'answered' })
    expect(await sendAsk(async () => r)).toEqual({ done: r })
  })

  it('409、404：这条已经答不了，带 hub 的文案（页面关表单、红色结果提示、重新加载）', async () => {
    expect(await sendAsk(() => Promise.reject(new ApiError(409, '请求已经处理过')))).toEqual({ gone: '请求已经处理过' })
    expect(await sendAsk(() => Promise.reject(new ApiError(409, '请求已过期')))).toEqual({ gone: '请求已过期' })
    expect(await sendAsk(() => Promise.reject(new ApiError(404, '没有这条请求')))).toEqual({ gone: '没有这条请求' })
  })

  it('400、网络错误等留在表单上显示 hub 的文案；不是 ApiError 的用兜底文案', async () => {
    expect(await sendAsk(() => Promise.reject(new ApiError(400, '答案不合规：第 1 题没有答案')))).toEqual({ failed: '答案不合规：第 1 题没有答案' })
    expect(await sendAsk(() => Promise.reject(new ApiError(0, '连不上 hub，请检查网络')))).toEqual({ failed: '连不上 hub，请检查网络' })
    expect(await sendAsk(() => Promise.reject(new TypeError('boom')))).toEqual({ failed: '提交失败' })
  })
})

describe('unansweredCount：还有几题没答', () => {
  it('选了或自己填了（非空白）的不算；全答了为 0', () => {
    expect(unansweredCount(qs, initAsk(qs))).toBe(3)
    expect(unansweredCount(qs, run(initAsk(qs), pick(0, 1)))).toBe(2)
    expect(unansweredCount(qs, run(initAsk(qs), pick(0, 1), type(2, '  ')))).toBe(2)
    expect(unansweredCount(qs, run(initAsk(qs), pick(0, 1), pick(1, 0, true), type(2, '无')))).toBe(0)
  })
})

describe('askSubmitter：AskForm 的提交逻辑（单飞、gone 交给页面、其余留在表单）', () => {
  const one = [fruit]
  const picked = run(initAsk(one), pick(0, 1))
  function setup(call: () => Promise<RequestView>) {
    const actions: AskAction[] = []
    const onDone = vi.fn()
    const callSpy = vi.fn(call)
    const submit = askSubmitter({ call: callSpy, dispatch: (a) => actions.push(a), onDone })
    return { actions, onDone, callSpy, submit }
  }

  it('回答：发 { answers }，成功带回作答后的请求', async () => {
    const done = req({ state: 'answered' })
    const t = setup(async () => done)
    await t.submit('answer', one, picked)
    expect(t.callSpy).toHaveBeenCalledWith({ answers: { '你更喜欢苹果还是香蕉？': 'Banana' } })
    expect(t.actions).toEqual([{ type: 'submit', verb: 'answer' }])
    expect(t.onDone).toHaveBeenCalledWith(done, undefined, 'answer', { '你更喜欢苹果还是香蕉？': 'Banana' })
  })

  it('交回终端：发 { passthrough: true }，不带答案', async () => {
    const t = setup(async () => req({ state: 'passthrough' }))
    await t.submit('passthrough', one, initAsk(one))
    expect(t.callSpy).toHaveBeenCalledWith({ passthrough: true })
    expect(t.onDone.mock.calls[0]?.[3]).toBeUndefined()
  })

  it('单飞：连点两次只发一次，结束后可以再提交', async () => {
    let finish!: (r: RequestView) => void
    let n = 0
    const t = setup(() => (n++ === 0 ? new Promise<RequestView>((res) => (finish = res)) : Promise.resolve(req({ state: 'answered' }))))
    const first = t.submit('answer', one, picked)
    await t.submit('answer', one, picked)
    await t.submit('passthrough', one, picked)
    expect(t.callSpy).toHaveBeenCalledTimes(1)
    finish(req({ state: 'answered' }))
    await first
    await t.submit('answer', one, picked)
    expect(t.callSpy).toHaveBeenCalledTimes(2)
  })

  it('还有题没答时回答不发请求', async () => {
    const t = setup(async () => req())
    await t.submit('answer', one, initAsk(one))
    expect(t.callSpy).not.toHaveBeenCalled()
    expect(t.actions).toEqual([])
  })

  it('400：留在表单显示 hub 的文案（failed），不调 onDone，可以改了再交', async () => {
    let n = 0
    const t = setup(() => (n++ === 0 ? Promise.reject(new ApiError(400, '答案不合规')) : Promise.resolve(req({ state: 'answered' }))))
    await t.submit('answer', one, picked)
    expect(t.actions).toEqual([{ type: 'submit', verb: 'answer' }, { type: 'failed', message: '答案不合规' }])
    expect(t.onDone).not.toHaveBeenCalled()
    await t.submit('answer', one, picked)
    expect(t.onDone).toHaveBeenCalledTimes(1)
  })

  it('409、404：只解除提交中、表单不写错误，原因交给页面的结果提示（onDone 带文案与这次的答案）', async () => {
    for (const status of [409, 404]) {
      const t = setup(() => Promise.reject(new ApiError(status, '请求已经处理过')))
      await t.submit('answer', one, picked)
      expect(t.actions).toEqual([{ type: 'submit', verb: 'answer' }, { type: 'settled' }])
      expect(t.onDone).toHaveBeenCalledWith(null, '请求已经处理过', 'answer', { '你更喜欢苹果还是香蕉？': 'Banana' })
    }
    const p = setup(() => Promise.reject(new ApiError(409, '请求已经处理过')))
    await p.submit('passthrough', one, picked)
    expect(p.onDone).toHaveBeenCalledWith(null, '请求已经处理过', 'passthrough', undefined)
  })

  it('网络错误：留在表单显示文案，不调 onDone', async () => {
    const t = setup(() => Promise.reject(new ApiError(0, '连不上 hub，请检查网络')))
    await t.submit('answer', one, picked)
    expect(t.actions).toEqual([{ type: 'submit', verb: 'answer' }, { type: 'failed', message: '连不上 hub，请检查网络' }])
    expect(t.onDone).not.toHaveBeenCalled()
  })
})

describe('askReducer settled', () => {
  it('只解除提交中，不写错误', () => {
    const s = run(initAsk([fruit]), pick(0, 0), { type: 'submit', verb: 'answer' })
    expect(s.busy).toBe('answer')
    expect(run(s, { type: 'settled' })).toMatchObject({ busy: null, error: null })
  })
})
