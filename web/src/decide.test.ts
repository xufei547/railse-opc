import { describe, expect, it } from 'vitest'
import { ApiError } from './api'
import {
  canConfirm,
  canRemember,
  classifyError,
  decideBody,
  doneMessage,
  draftShown,
  goneMessage,
  initSheet,
  isEscape,
  keepDecided,
  noteHint,
  outcomeView,
  rememberBlock,
  retryable,
  scopeOptions,
  sheetReducer,
  type SheetAction,
  type SheetState,
} from './decide'
import type { Draft, RequestView } from './types'

function req(over: Partial<RequestView> = {}): RequestView {
  return {
    id: 'r1', machine_id: 'm', session_id: 's', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'network', url: 'https://api.example.com/v1/x' }, risk: 'network', project_root: '/p/blog', engine_reason: '无规则命中',
    state: 'pending', decided_by: '', decision_note: '', rule_id: '', turn_id: 't', cli_session_id: 's', title: '',
    created_at: '2026-10-23T10:00:00.000000000Z', expires_at: null, decided_at: null, delivered_at: null,
    machine_name: 'mac', project: 'blog', has_redacted: false, ...over,
  }
}

function draft(scope: 'project' | 'global', action: 'allow' | 'deny' = 'allow'): Draft {
  return { rule_id: `learned-${scope}`, scope, yaml: `id: learned-${scope}\n`, summary: { action, payload: 'network', anchor: 'api.example.com' } }
}

function run(s: SheetState, ...actions: SheetAction[]): SheetState {
  return actions.reduce(sheetReducer, s)
}

describe('initSheet', () => {
  it('有项目根默认项目范围，没有默认全局；不记住', () => {
    const a = initSheet(req(), 'allow')
    expect(a).toMatchObject({ action: 'allow', remember: false, scope: 'project', draft: null, busy: false, error: null })
    expect(initSheet(req({ project_root: '', project: '' }), 'deny')).toMatchObject({ action: 'deny', scope: 'global' })
  })
})

describe('不记住', () => {
  it('可以直接确认；body 不带 remember 与 scope', () => {
    const s = initSheet(req(), 'allow')
    expect(canConfirm(s)).toBe(true)
    expect(decideBody(s, '')).toEqual({ action: 'allow' })
  })

  it('备注去掉首尾空白；全是空白时不带', () => {
    const s = initSheet(req(), 'deny')
    expect(decideBody(s, '  不该推主分支 \n')).toEqual({ action: 'deny', note: '不该推主分支' })
    expect(decideBody(s, ' \n\t ')).toEqual({ action: 'deny' })
  })
})

describe('记住：草案与所选范围一致才能确认', () => {
  it('打开记住后，草案到达前不能确认', () => {
    const s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    expect(s.remember).toBe(true)
    expect(s.draft).toBeNull()
    expect(canConfirm(s)).toBe(false)
    expect(decideBody(s, '')).toBeNull()
  })

  it('草案到达后可以确认，decide 带草案的范围', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    s = run(s, { type: 'draft', seq: s.seq, draft: draft('project') })
    expect(draftShown(s)?.rule_id).toBe('learned-project')
    expect(canConfirm(s)).toBe(true)
    expect(decideBody(s, '')).toEqual({ action: 'allow', remember: true, scope: 'project' })
  })

  it('切换范围在同一次状态更新里清掉旧草案：中间没有“旧草案 + 新范围”可以确认的一帧', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    s = run(s, { type: 'draft', seq: s.seq, draft: draft('project') })
    s = run(s, { type: 'scope', scope: 'global' })
    expect(s.scope).toBe('global')
    expect(draftShown(s)).toBeNull()
    expect(canConfirm(s)).toBe(false)
    expect(decideBody(s, '')).toBeNull()
  })

  it('旧范围的回应晚到时丢掉，等当前范围的回应', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    const seqProject = s.seq
    s = run(s, { type: 'scope', scope: 'global' })
    const seqGlobal = s.seq
    expect(seqGlobal).not.toBe(seqProject)
    s = run(s, { type: 'draft', seq: seqProject, draft: draft('project') })
    expect(draftShown(s)).toBeNull()
    expect(canConfirm(s)).toBe(false)
    s = run(s, { type: 'draft', seq: seqGlobal, draft: draft('global') })
    expect(canConfirm(s)).toBe(true)
    expect(decideBody(s, '')).toEqual({ action: 'allow', remember: true, scope: 'global' })
  })

  it('乱序：项目 → 全局 → 项目，第一次项目、全局的回应在最后一次之后到达也不覆盖它', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    const s1 = s.seq
    s = run(s, { type: 'scope', scope: 'global' })
    const s2 = s.seq
    s = run(s, { type: 'scope', scope: 'project' })
    const s3 = s.seq
    s = run(s, { type: 'draft', seq: s3, draft: draft('project') })
    s = run(s, { type: 'draft', seq: s2, draft: draft('global') }, { type: 'draft_error', seq: s1, message: '晚到的错误', retryable: true })
    expect(s.draftError).toBeNull()
    expect(draftShown(s)?.scope).toBe('project')
    expect(decideBody(s, '')).toEqual({ action: 'allow', remember: true, scope: 'project' })
  })

  it('再点当前范围不重新请求草案', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    s = run(s, { type: 'draft', seq: s.seq, draft: draft('project') })
    expect(run(s, { type: 'scope', scope: 'project' })).toBe(s)
  })

  it('hub 给的草案范围或动作与所选不符时不显示、不能确认', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    const a = run(s, { type: 'draft', seq: s.seq, draft: draft('global') })
    expect(draftShown(a)).toBeNull()
    expect(canConfirm(a)).toBe(false)
    s = run(s, { type: 'draft', seq: s.seq, draft: draft('project', 'deny') })
    expect(canConfirm(s)).toBe(false)
    expect(decideBody(s, '')).toBeNull()
  })

  it('关掉记住：丢掉在途的回应，可以直接确认、body 不带 remember', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    const seq = s.seq
    s = run(s, { type: 'remember', on: false }, { type: 'draft', seq, draft: draft('project') })
    expect(s.draft).toBeNull()
    expect(canConfirm(s)).toBe(true)
    expect(decideBody(s, '')).toEqual({ action: 'allow' })
  })

  it('再打开记住要重新取草案（旧草案不复用）', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    s = run(s, { type: 'draft', seq: s.seq, draft: draft('project') })
    s = run(s, { type: 'remember', on: false }, { type: 'remember', on: true })
    expect(draftShown(s)).toBeNull()
    expect(canConfirm(s)).toBe(false)
  })

  it('没有项目根时选不了项目范围', () => {
    let s = run(initSheet(req({ project_root: '', project: '' }), 'allow'), { type: 'remember', on: true })
    const seq = s.seq
    s = run(s, { type: 'scope', scope: 'project' })
    expect(s.scope).toBe('global')
    expect(s.seq).toBe(seq)
  })

  it('草案出错时不能“记住”确认；可重试的错误（连不上、5xx）重试换新序号、清掉错误', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    s = run(s, { type: 'draft_error', seq: s.seq, message: '连不上 hub，请检查网络', retryable: true })
    expect(s.draftError).toBe('连不上 hub，请检查网络')
    expect(s.draftRetryable).toBe(true)
    expect(canConfirm(s)).toBe(false)
    const seq = s.seq
    s = run(s, { type: 'retry' })
    expect(s.seq).not.toBe(seq)
    expect(s.draftError).toBeNull()
  })

  it('记不住（422）不给重试：结果是确定的，只能关掉记住或取消', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    s = run(s, { type: 'draft_error', seq: s.seq, message: '不能记住：只读、项目内改动和联网操作之外的不能记成放行', retryable: false })
    expect(s.draftRetryable).toBe(false)
    expect(run(s, { type: 'retry' })).toBe(s)
    s = run(s, { type: 'remember', on: false })
    expect(canConfirm(s)).toBe(true)
  })

  it('没出错时 retry 什么也不做', () => {
    const s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    expect(run(s, { type: 'retry' })).toBe(s)
  })
})

describe('提交', () => {
  it('提交中不能再提交，也不能改记住与范围；失败后恢复并显示文案', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    s = run(s, { type: 'draft', seq: s.seq, draft: draft('project') }, { type: 'submit' })
    expect(s.busy).toBe(true)
    expect(canConfirm(s)).toBe(false)
    expect(decideBody(s, '')).toBeNull()
    expect(run(s, { type: 'submit' })).toBe(s)
    expect(run(s, { type: 'scope', scope: 'global' })).toBe(s)
    expect(run(s, { type: 'remember', on: false })).toBe(s)
    s = run(s, { type: 'failed', message: '不能记住：规则集已接近下发上限' })
    expect(s.busy).toBe(false)
    expect(s.error).toBe('不能记住：规则集已接近下发上限')
    expect(canConfirm(s)).toBe(true)
  })

  it('不能确认时 submit 不改状态', () => {
    const s = run(initSheet(req(), 'allow'), { type: 'remember', on: true })
    expect(run(s, { type: 'submit' })).toBe(s)
  })

  it('改选项清掉上次提交的错误', () => {
    let s = run(initSheet(req(), 'allow'), { type: 'submit' }, { type: 'failed', message: 'x' })
    s = run(s, { type: 'remember', on: true })
    expect(s.error).toBeNull()
  })
})

describe('classifyError', () => {
  it('409（已处理、已过期、机器已吊销）与 404：关面板，带 hub 的文案', () => {
    for (const msg of ['请求已经处理过', '请求已过期', '机器已吊销，请求已取消']) {
      expect(classifyError(new ApiError(409, msg), '决定失败')).toEqual({ gone: true, message: msg })
    }
    expect(classifyError(new ApiError(404, '没有这条请求'), '决定失败')).toEqual({ gone: true, message: '没有这条请求' })
  })

  it('422、400、500、连不上：留在面板，显示 hub 的文案（不解析）', () => {
    expect(classifyError(new ApiError(422, '不能记住：含凭据的命令不能记住'), 'x')).toEqual({ gone: false, message: '不能记住：含凭据的命令不能记住' })
    expect(classifyError(new ApiError(400, '备注至多 1000 个字符'), 'x')).toEqual({ gone: false, message: '备注至多 1000 个字符' })
    expect(classifyError(new ApiError(0, '连不上 hub，请检查网络'), 'x')).toEqual({ gone: false, message: '连不上 hub，请检查网络' })
  })

  it('不是 ApiError（多半是英文的程序错误）用兜底文案', () => {
    expect(classifyError(new TypeError('x is undefined'), '决定失败')).toEqual({ gone: false, message: '决定失败' })
    expect(classifyError('boom', '生成草案失败')).toEqual({ gone: false, message: '生成草案失败' })
  })
})

describe('retryable', () => {
  it('4xx 是确定的结果不重试；连不上、超时、5xx 与其他错误可以重试', () => {
    expect(retryable(new ApiError(422, 'x'))).toBe(false)
    expect(retryable(new ApiError(400, 'x'))).toBe(false)
    expect(retryable(new ApiError(0, 'x'))).toBe(true)
    expect(retryable(new ApiError(500, 'x'))).toBe(true)
    expect(retryable(new Error('x'))).toBe(true)
  })
})

describe('备注说明', () => {
  it('审批的备注不给 agent；webhook 的备注随回调发给来源', () => {
    expect(noteHint(req())).toBe('备注（只进审计，不会给 agent 看）')
    expect(noteHint(req({ kind: 'webhook' }))).toBe('备注（进审计，并随回调发给来源系统）')
  })
})

describe('能否记住', () => {
  it('只有不含隐藏部分的审批能记住', () => {
    expect(canRemember(req())).toBe(true)
    expect(rememberBlock(req())).toBeNull()
    expect(canRemember(req({ kind: 'webhook', machine_name: '', project_root: '' }))).toBe(false)
    expect(rememberBlock(req({ kind: 'webhook' }))).toBe('webhook 请求不能记住')
    expect(canRemember(req({ has_redacted: true }))).toBe(false)
    expect(rememberBlock(req({ has_redacted: true }))).toBe('含被隐藏部分的请求不能记住')
    expect(canRemember(req({ kind: 'question' }))).toBe(false)
  })

  it('不能记住的请求 remember 打不开', () => {
    const s = run(initSheet(req({ kind: 'webhook' }), 'allow'), { type: 'remember', on: true })
    expect(s.remember).toBe(false)
    expect(decideBody(s, '')).toEqual({ action: 'allow' })
  })
})

describe('scopeOptions', () => {
  it('没有项目根时“项目”不可选', () => {
    expect(scopeOptions(req())).toEqual([
      { scope: 'project', label: '项目 blog', disabled: false },
      { scope: 'global', label: '全局', disabled: false },
    ])
    expect(scopeOptions(req({ project_root: '', project: '' }))[0]).toEqual({ scope: 'project', label: '项目', disabled: true })
  })
})

describe('结果文案', () => {
  it('决定成功', () => {
    expect(doneMessage(req({ state: 'approved' }))).toBe('已批准，决定正在送回 agent')
    expect(doneMessage(req({ state: 'approved', rule_id: 'learned-net-1' }))).toBe('已批准，并记住为规则 learned-net-1；决定正在送回 agent')
    expect(doneMessage(req({ state: 'denied' }))).toBe('已拒绝，决定正在送回 agent')
    expect(doneMessage(req({ state: 'approved', kind: 'webhook' }))).toBe('已批准，正在回调来源')
    expect(doneMessage(req({ state: 'approved', delivered_at: '2026-10-23T10:00:05Z' }))).toBe('已批准，已送达 agent')
  })

  it('打开面板时请求在别处有了结果', () => {
    expect(goneMessage(req({ state: 'cancelled', decided_by: 'terminal' }))).toBe('这条请求在别处有了结果：已取消（终端里处理了）')
    expect(goneMessage(req({ state: 'expired', decided_by: '' }))).toBe('这条请求在别处有了结果：已过期')
  })

  it('hub 已经采纳了你这次的决定（同样的批准或拒绝，多半是上次提交超时）时不说成“别处”', () => {
    expect(goneMessage(req({ state: 'approved', decided_by: 'user' }), 'allow')).toBe('你的决定已生效：已批准')
    expect(goneMessage(req({ state: 'denied', decided_by: 'user', rule_id: 'user-deny-x' }), 'deny')).toBe('你的决定已生效：已拒绝')
  })

  it('在另一个页面做了相反的决定、或不知道这次要做什么时，照旧说是别处的结果', () => {
    expect(goneMessage(req({ state: 'approved', decided_by: 'user' }), 'deny')).toBe('这条请求在别处有了结果：已批准（手机上决定）')
    expect(goneMessage(req({ state: 'approved', decided_by: 'user' }))).toBe('这条请求在别处有了结果：已批准（手机上决定）')
    expect(goneMessage(req({ state: 'cancelled', decided_by: 'machine_revoked' }), 'allow')).toBe('这条请求在别处有了结果：已取消（机器已吊销）')
  })
})

describe('outcomeView：决定结果的提示（固定在底部，始终可见）', () => {
  const approved = req({ state: 'approved', decided_by: 'user' })

  it('成功：绿色“已生效”，文案随最新数据（送达后变成已送达）', () => {
    expect(outcomeView({ kind: 'done', request: approved }, approved)).toEqual({ tone: 'ok', title: '已生效', text: '已批准，决定正在送回 agent' })
    const delivered = { ...approved, delivered_at: '2026-10-23T10:00:05Z' }
    expect(outcomeView({ kind: 'done', request: approved }, delivered).text).toBe('已批准，已送达 agent')
    // 当前数据还是旧的 pending（晚到的拉取被 keepDecided 挡住前的一刻）或已不在：用 decide 的返回
    expect(outcomeView({ kind: 'done', request: approved }, req({ state: 'pending' })).text).toBe('已批准，决定正在送回 agent')
    expect(outcomeView({ kind: 'done', request: approved }, null).text).toBe('已批准，决定正在送回 agent')
  })

  it('409 等决定不了：红色“没有生效”，带 hub 的文案', () => {
    expect(outcomeView({ kind: 'gone', message: '请求已过期' }, req({ state: 'pending' }))).toEqual({ tone: 'warn', title: '没有生效', text: '请求已过期' })
    expect(outcomeView({ kind: 'gone', message: '机器已吊销，请求已取消' }, req({ state: 'cancelled', decided_by: 'machine_revoked' }))).toEqual({
      tone: 'warn',
      title: '没有生效',
      text: '机器已吊销，请求已取消',
    })
    expect(outcomeView({ kind: 'gone', message: '这条请求已不存在' }, null)).toEqual({ tone: 'warn', title: '没有生效', text: '这条请求已不存在' })
  })

  it('409 但重新拉到的正是你这次的决定（如上次提交超时、其实已生效）：绿色“你的决定已生效”', () => {
    expect(outcomeView({ kind: 'gone', message: '请求已经处理过', attempted: 'allow' }, approved)).toEqual({
      tone: 'ok',
      title: '你的决定已生效',
      text: '已批准，决定正在送回 agent',
    })
    // 终端里处理掉的不算
    expect(outcomeView({ kind: 'gone', message: '请求已经处理过', attempted: 'allow' }, req({ state: 'cancelled', decided_by: 'terminal' })).tone).toBe('warn')
  })

  it('409 且重新拉到的是另一个页面做的相反决定：红色，说明现在的结果', () => {
    expect(outcomeView({ kind: 'gone', message: '请求已经处理过', attempted: 'deny' }, approved)).toEqual({
      tone: 'warn',
      title: '没有生效',
      text: '这条请求在别处有了结果：已批准（手机上决定）',
    })
    // 不知道这次要做什么（如面板开着时请求没了）：不认作你的决定
    expect(outcomeView({ kind: 'gone', message: '这条请求已不存在' }, approved).tone).toBe('warn')
  })
})

describe('isEscape', () => {
  it('Esc 关面板；输入法组字中的 Esc（取消候选）不算', () => {
    expect(isEscape({ key: 'Escape', isComposing: false, keyCode: 27 })).toBe(true)
    expect(isEscape({ key: 'Escape', isComposing: true, keyCode: 27 })).toBe(false)
    expect(isEscape({ key: 'Escape', isComposing: false, keyCode: 229 })).toBe(false)
    expect(isEscape({ key: 'Enter', isComposing: false, keyCode: 13 })).toBe(false)
  })
})

describe('keepDecided', () => {
  const a = (over: Partial<RequestView>) => ({ request: req(over), audit: [] })
  it('已有结果的请求不会被晚到的旧“等待决定”盖回去（请求只会从 pending 变成终态）', () => {
    const decided = a({ state: 'approved', decided_by: 'user' })
    const stale = a({ state: 'pending' })
    expect(keepDecided(decided, stale)).toBe(decided)
  })
  it('其余情况取新的', () => {
    const pending = a({ state: 'pending' })
    const approved = a({ state: 'approved' })
    const delivered = a({ state: 'approved', delivered_at: '2026-10-23T10:00:05Z' })
    expect(keepDecided(null, pending)).toBe(pending)
    expect(keepDecided(pending, approved)).toBe(approved)
    expect(keepDecided(approved, delivered)).toBe(delivered)
    expect(keepDecided(pending, pending)).toBe(pending)
  })
  it('换了一条请求时取新的', () => {
    const other = { request: req({ id: 'r2', state: 'pending' }), audit: [] }
    expect(keepDecided(a({ state: 'approved' }), other)).toBe(other)
  })
})
