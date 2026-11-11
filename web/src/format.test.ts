import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import {
  absoluteTime,
  actorLabel,
  auditDetail,
  auditLabel,
  clip,
  decidedByLabel,
  deliveryLabel,
  engineReasonLabel,
  isRedacted,
  kindLabel,
  outcomeText,
  parseTime,
  payloadSummary,
  relativeTime,
  riskLabel,
  riskTone,
  stateLabel,
} from './format'
import { setLang } from './i18n'
import type { RequestView } from './types'

describe('parseTime', () => {
  it('认 hub 的 9 位纳秒时间（截到毫秒，各浏览器都能解析）', () => {
    expect(parseTime('2026-10-23T10:00:00.000000000Z')).toBe(Date.UTC(2026, 9, 23, 10, 0, 0))
    expect(parseTime('2026-10-23T10:00:00.123456789Z')).toBe(Date.UTC(2026, 9, 23, 10, 0, 0, 123))
    expect(parseTime('2026-10-23T10:00:00Z')).toBe(Date.UTC(2026, 9, 23, 10, 0, 0))
    expect(parseTime('garbage')).toBeNaN()
    expect(parseTime('')).toBeNaN()
  })
})

describe('relativeTime', () => {
  const now = new Date('2026-10-23T12:00:00Z')
  it('分档', () => {
    expect(relativeTime('2026-10-23T11:59:50Z', now)).toBe('刚刚')
    expect(relativeTime('2026-10-23T11:57:00Z', now)).toBe('3 分钟前')
    expect(relativeTime('2026-10-23T11:57:00.123456789Z', now)).toBe('3 分钟前')
    expect(relativeTime('2026-10-23T09:00:00Z', now)).toBe('3 小时前')
    expect(relativeTime('2026-10-22T09:00:00Z', now)).toBe('昨天')
    expect(relativeTime('2026-10-01T09:00:00Z', now)).toMatch(/^10-01$/)
    expect(relativeTime('garbage', now)).toBe('')
  })
  it('稍晚于此刻（时钟偏差）也算刚刚', () => {
    expect(relativeTime('2026-10-23T12:00:20Z', now)).toBe('刚刚')
  })
})

describe('labels', () => {
  it('风险', () => {
    expect(riskLabel('outward')).toBe('对外')
    expect(riskTone('outward')).toBe('high')
    expect(riskTone('read')).toBe('low')
    expect(riskTone('weird')).toBe('mid')
    expect(riskLabel('')).toBe('未分级')
    expect(riskLabel('weird')).toBe('weird')
  })
  it('载荷摘要', () => {
    expect(payloadSummary({ type: 'shell', command: ' git push ' })).toBe('git push')
    expect(payloadSummary({ type: 'file_write', path: '/p/a.go' })).toBe('/p/a.go')
    expect(payloadSummary({ type: 'network', url: 'https://x', method: 'POST' })).toBe('POST https://x')
    expect(payloadSummary({ type: 'network', url: 'https://x' })).toBe('https://x')
    expect(payloadSummary({ type: 'mcp', server: 'fs', tool: 'write' })).toBe('fs/write')
    expect(payloadSummary({ type: 'other' })).toBe('other')
    expect(payloadSummary({ type: 'other', tool: 'read_file' })).toBe('read_file')
  })
  it('状态与种类', () => {
    expect(stateLabel('auto_allowed')).toBe('自动放行')
    expect(stateLabel('passthrough')).toBe('交回终端')
    expect(stateLabel('mystery')).toBe('mystery')
    expect(kindLabel('question')).toBe('等你回答')
    expect(kindLabel('webhook')).toBe('webhook')
    expect(kindLabel('ask')).toBe('提问')
    expect(stateLabel('answered')).toBe('已回答')
  })
  it('依据：规则自动决定', () => {
    expect(decidedByLabel({ decided_by: 'rule:builtin-readonly', rule_id: 'builtin-readonly', state: 'auto_allowed' })).toBe('规则 builtin-readonly')
    expect(decidedByLabel({ decided_by: 'rule:user-deny-x', rule_id: '', state: 'auto_denied' })).toBe('规则 user-deny-x')
    // 暂停时照常生效的用户 deny 规则：decided_by 不是 user，按规则显示
    expect(decidedByLabel({ decided_by: 'paused', rule_id: 'user-deny-x', state: 'auto_denied' })).toBe('规则 user-deny-x')
  })
  it('依据：手机上决定，记住时带上学到的规则，而不是显示成“规则 …”', () => {
    expect(decidedByLabel({ decided_by: 'user', rule_id: '', state: 'approved' })).toBe('手机上决定')
    expect(decidedByLabel({ decided_by: 'user', rule_id: 'learned-git-push', state: 'approved' })).toBe('手机上决定（并记住为 learned-git-push）')
    expect(decidedByLabel({ decided_by: 'user', rule_id: 'user-deny-rm', state: 'denied' })).toBe('手机上决定（并记住为 user-deny-rm）')
  })
  it('依据：提问在手机上作答说“手机上回答”；交回终端仍是“手机上决定”', () => {
    expect(decidedByLabel({ decided_by: 'user', rule_id: '', state: 'answered' })).toBe('手机上回答')
    expect(decidedByLabel({ decided_by: 'user', rule_id: '', state: 'passthrough' })).toBe('手机上决定')
  })
  it('依据：其他来源', () => {
    expect(decidedByLabel({ decided_by: 'hub_unreachable', rule_id: '', state: 'expired' })).toBe('连不上 hub，交回终端')
    expect(decidedByLabel({ decided_by: 'machine_revoked', rule_id: '', state: 'cancelled' })).toBe('机器已吊销')
    expect(decidedByLabel({ decided_by: 'something_new', rule_id: '', state: 'cancelled' })).toBe('something_new')
    expect(decidedByLabel({ decided_by: '', rule_id: '', state: 'recorded' })).toBe('已记录')
    expect(decidedByLabel({ decided_by: '', rule_id: '', state: 'pending' })).toBe('')
  })
})

describe('clip', () => {
  it('超长截断并加省略号，不长不动', () => {
    expect(clip('abcdef', 3)).toBe('abc…')
    expect(clip('abc', 3)).toBe('abc')
    expect(clip('', 3)).toBe('')
  })
  it('不把代理对切成半个字符', () => {
    const s = 'ab😀cd' // 😀 占两个 UTF-16 单元，下标 2、3
    expect(clip(s, 3)).toBe('ab…')
    expect(clip(s, 4)).toBe('ab😀…')
  })
})

describe('absoluteTime', () => {
  const now = new Date(2026, 9, 23, 12, 0, 0)
  it('本地时间：同年省略年份，补零到分钟', () => {
    const t = new Date(2026, 9, 3, 8, 5, 9)
    expect(absoluteTime(t.toISOString(), now)).toBe('10-03 08:05')
    const y = new Date(2025, 0, 2, 23, 59, 0)
    expect(absoluteTime(y.toISOString(), now)).toBe('2025-01-02 23:59')
  })
  it('认 9 位纳秒；解析不了回空串', () => {
    const t = new Date(Date.UTC(2026, 9, 23, 10, 0, 0))
    const want = `${t.getMonth() + 1}-${String(t.getDate()).padStart(2, '0')} ${String(t.getHours()).padStart(2, '0')}:00`
    expect(absoluteTime('2026-10-23T10:00:00.123456789Z', now)).toBe(want)
    expect(absoluteTime('garbage', now)).toBe('')
  })
})

describe('审计', () => {
  it('动作名译成中文；状态名沿用状态文案；不认识的原样', () => {
    expect(auditLabel('request_pending')).toBe('登记')
    expect(auditLabel('allow')).toBe('批准')
    expect(auditLabel('deny')).toBe('拒绝')
    expect(auditLabel('rule_learned')).toBe('学到规则')
    expect(auditLabel('push_sent')).toBe('推送到手机')
    expect(auditLabel('webhook_callback')).toBe('回调来源')
    expect(auditLabel('undeliverable')).toBe('决定未送达')
    expect(auditLabel('approved')).toBe('已批准')
    expect(auditLabel('auto_allowed')).toBe('自动放行')
    expect(auditLabel('brand_new')).toBe('brand_new')
  })
  it('提问：hub 记的作答是“回答”，守护进程转来的结束行沿用状态文案', () => {
    expect(auditLabel('answer')).toBe('回答')
    expect(auditLabel('answered')).toBe('已回答')
    expect(auditLabel('passthrough')).toBe('交回终端')
  })
  it('谁做的', () => {
    expect(actorLabel('user')).toBe('你')
    expect(actorLabel('agent')).toBe('守护进程')
    expect(actorLabel('system')).toBe('hub')
    expect(actorLabel('x')).toBe('x')
  })
  it('detail 展开成 k=v；去掉空值与 false（ok=false 保留）；不是 JSON 原样', () => {
    expect(auditDetail('{"note":"","remember":false,"rule_id":"learned-x"}')).toBe('rule_id=learned-x')
    expect(auditDetail('{"ok":false,"attempts":3,"error":"超时","target":"https://n8n.example.com"}')).toBe(
      'ok=false attempts=3 error=超时 target=https://n8n.example.com',
    )
    expect(auditDetail('{"elevated_rule_ids":["a","b"],"x":null}')).toBe('elevated_rule_ids=["a","b"]')
    expect(auditDetail('null')).toBe('')
    expect(auditDetail('not json')).toBe('not json')
  })
})

function view(over: Partial<RequestView>): RequestView {
  return {
    id: 'r', machine_id: 'm', session_id: 's', cli: 'claude', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'shell', command: 'git push' }, risk: 'outward', project_root: '/p/blog', engine_reason: '',
    state: 'pending', decided_by: '', decision_note: '', rule_id: '', turn_id: 't', cli_session_id: 's', title: '',
    created_at: '2026-10-23T10:00:00Z', expires_at: null, decided_at: null, delivered_at: null,
    machine_name: 'mac', project: 'blog', has_redacted: false, ...over,
  }
}

describe('isRedacted', () => {
  it('has_redacted 或标题里有占位符（webhook 标题、问题文本也是脱敏过的）', () => {
    expect(isRedacted(view({}))).toBe(false)
    expect(isRedacted(view({ has_redacted: true }))).toBe(true)
    expect(isRedacted(view({ kind: 'webhook', title: '部署 token=<redacted>' }))).toBe(true)
  })
})

describe('deliveryLabel', () => {
  it('只对手机上的批准、拒绝给出送达情况', () => {
    expect(deliveryLabel(view({ state: 'approved', delivered_at: '2026-10-23T10:00:05Z' }), false)).toBe('已送达 agent')
    expect(deliveryLabel(view({ state: 'denied' }), false)).toBe('还没送达 agent')
    expect(deliveryLabel(view({ state: 'approved', kind: 'webhook', delivered_at: '2026-10-23T10:00:05Z' }), false)).toBe('已回调来源')
    expect(deliveryLabel(view({ state: 'approved', kind: 'webhook' }), false)).toBe('还没回调成功')
    expect(deliveryLabel(view({ state: 'approved' }), true)).toBe('决定未送达：agent 已不再等这条（多半已交回终端）')
    expect(deliveryLabel(view({ state: 'pending' }), false)).toBe('')
    expect(deliveryLabel(view({ state: 'auto_allowed' }), false)).toBe('')
  })

  it('提问作答后同样给出答案送没送到；交回终端不等送达', () => {
    const ask = (over: Partial<RequestView>) => view({ kind: 'ask', tool_name: 'AskUserQuestion', payload: { type: '' }, risk: '', decided_by: 'user', ...over })
    expect(deliveryLabel(ask({ state: 'answered', delivered_at: '2026-10-31T10:00:05Z' }), false)).toBe('已送达 agent')
    expect(deliveryLabel(ask({ state: 'answered' }), false)).toBe('还没送达 agent')
    expect(deliveryLabel(ask({ state: 'answered' }), true)).toBe('答案未送达：agent 已不再等这条（多半已在终端问了你）')
    expect(deliveryLabel(ask({ state: 'passthrough' }), false)).toBe('')
    expect(deliveryLabel(ask({ state: 'pending', decided_by: '' }), false)).toBe('')
  })
})

describe('engineReasonLabel', () => {
  it('只把“按 <风险> 默认询问”里认识的风险词换成中文', () => {
    expect(engineReasonLabel('无规则命中，按 outward 默认询问')).toBe('无规则命中，按「对外」默认询问')
    expect(engineReasonLabel('无规则命中，按 write_in_project 默认询问')).toBe('无规则命中，按「项目内改动」默认询问')
    expect(engineReasonLabel('无规则命中，按 weird 默认询问')).toBe('无规则命中，按 weird 默认询问')
    expect(engineReasonLabel('命中规则 user-network-ask')).toBe('命中规则 user-network-ask')
    expect(engineReasonLabel('自动审批已暂停')).toBe('自动审批已暂停')
    expect(engineReasonLabel('')).toBe('')
  })
})

describe('outcomeText', () => {
  type R = Parameters<typeof outcomeText>[0]
  const r = (over: Partial<R>): R => ({ kind: 'permission', state: 'approved', decided_by: '', rule_id: '', ...over })
  // [state, decided_by, rule_id, 期望]：hub 与守护进程实际写出的组合（internal/hub/requests.go、internal/agent）
  const cases: [string, string, string, string][] = [
    ['approved', 'user', '', '你批准了'],
    ['approved', 'user', 'learned-net-1', '你批准并记住了（规则 learned-net-1）'],
    ['denied', 'user', '', '你拒绝了'],
    ['denied', 'user', 'user-deny-rm', '你拒绝并记住了（规则 user-deny-rm）'],
    ['auto_allowed', 'rule:builtin-read', 'builtin-read', '规则 builtin-read 自动批准了'],
    ['auto_allowed', 'rule:builtin-read', '', '规则 builtin-read 自动批准了'],
    ['auto_denied', 'rule:user-deny-x', 'user-deny-x', '规则 user-deny-x 自动拒绝了'],
    ['auto_denied', 'paused', 'user-deny-x', '规则 user-deny-x 自动拒绝了'],
    ['auto_allowed', '', '', '规则自动批准了'],
    ['cancelled', 'terminal', '', 'agent 那边先结束了，不用你处理'],
    ['passthrough', 'terminal', '', '交回终端处理了'],
    ['expired', 'timeout', '', '等太久过期了，agent 已交回终端'],
    ['expired', 'hub_unreachable', '', '当时连不上 hub，agent 在终端里自己处理了'],
    ['passthrough', 'no_hub', '', '当时本机没接 hub，agent 在终端里自己处理了'],
    ['cancelled', 'agent_restart', '', '守护进程重启，这条作废了'],
    ['cancelled', 'shutdown', '', '守护进程停止，这条作废了'],
    ['cancelled', 'resend', '', 'agent 重发了一条新的，这条作废'],
    ['cancelled', 'machine_revoked', '', '这台机器已被吊销'],
    ['passthrough', 'paused', '', '自动审批暂停中，这条交回终端了'],
    ['passthrough', 'gemini', '', '交回 Gemini，在终端里让你确认'],
    ['passthrough', 'error', '', '处理出错，交回终端了'],
    ['expired', 'hub_error', '', '处理出错，交回终端了'],
  ]
  it.each(cases)('%s + %s（%s）→ %s', (state, decided_by, rule_id, want) => {
    expect(outcomeText(r({ state: state as R['state'], decided_by, rule_id }))).toBe(want)
  })

  it('提问：手机上答了说“你答了”，手机上交回终端说“交回终端问了”；其余结束同审批的口径', () => {
    const ask = (over: Partial<R>) => r({ kind: 'ask', ...over })
    expect(outcomeText(ask({ state: 'answered', decided_by: 'user' }))).toBe('你答了')
    expect(outcomeText(ask({ state: 'passthrough', decided_by: 'user' }))).toBe('交回终端问了')
    expect(outcomeText(ask({ state: 'pending' }))).toBe('')
    expect(outcomeText(ask({ state: 'expired', decided_by: 'timeout' }))).toBe('等太久过期了，agent 已交回终端')
    expect(outcomeText(ask({ state: 'cancelled', decided_by: 'terminal' }))).toBe('agent 那边先结束了，不用你处理')
    expect(outcomeText(ask({ state: 'passthrough', decided_by: 'no_hub' }))).toBe('当时本机没接 hub，agent 在终端里自己处理了')
    expect(outcomeText(ask({ state: 'passthrough', decided_by: 'error' }))).toBe('处理出错，交回终端了')
    // 审批没有“手机上交回终端”这回事：不套用提问的说法
    expect(outcomeText(r({ state: 'passthrough', decided_by: 'user' }))).toBe('')
  })

  it('webhook：你的决定同样说；等太久过期时说已按拒绝回调工作流', () => {
    expect(outcomeText(r({ kind: 'webhook', state: 'approved', decided_by: 'user' }))).toBe('你批准了')
    expect(outcomeText(r({ kind: 'webhook', state: 'expired', decided_by: 'timeout' }))).toBe('等太久过期了，已按拒绝回调工作流')
  })

  it('还在等的、问题与完成、来源为空或不认识的：空串（状态标签已经说了，不露原始值）', () => {
    expect(outcomeText(r({ state: 'pending' }))).toBe('')
    expect(outcomeText(r({ kind: 'question', state: 'recorded' }))).toBe('')
    expect(outcomeText(r({ kind: 'done', state: 'recorded', decided_by: 'hub' }))).toBe('')
    expect(outcomeText(r({ state: 'expired', decided_by: '' }))).toBe('')
    expect(outcomeText(r({ state: 'cancelled', decided_by: 'something_new' }))).toBe('')
  })

  it('不出现行话', () => {
    const jargon = /终端里处理了|守护进程重连后不再持有|决定来源出错|手机上决定|等待过期|被重发顶掉|rule:|_/
    for (const [state, decided_by, rule_id] of cases) {
      const text = outcomeText(r({ state: state as R['state'], decided_by, rule_id })).replace(/（规则 [^）]+）|规则 \S+ 自动/g, '')
      expect(text).not.toMatch(jargon)
    }
    // 以后新增的、不认识的来源：任何状态下都不露原始值
    const states: R['state'][] = ['approved', 'denied', 'cancelled', 'expired', 'auto_allowed', 'auto_denied', 'passthrough']
    for (const decided_by of ['future_source', 'hub_restart', 'rule_engine_v2', 'Terminal']) {
      for (const state of states) expect(outcomeText(r({ state, decided_by }))).not.toContain(decided_by)
    }
  })
})

const han = /[　-〿一-鿿＀-￯]/

describe('outcomeText（英文）', () => {
  beforeEach(() => setLang('en'))
  afterEach(() => setLang('zh'))
  it('八个分支都是英文句子', () => {
    const cases: [Parameters<typeof outcomeText>[0], string][] = [
      [{ kind: 'ask', state: 'answered', decided_by: 'user', rule_id: '' }, 'You answered'],
      [{ kind: 'ask', state: 'passthrough', decided_by: 'user', rule_id: '' }, 'Handed back to the terminal to ask'],
      [{ kind: 'permission', state: 'approved', decided_by: 'user', rule_id: 'allow-curl' }, 'You approved and remembered it (rule allow-curl)'],
      [{ kind: 'permission', state: 'denied', decided_by: 'user', rule_id: '' }, 'You denied'],
      [{ kind: 'permission', state: 'auto_allowed', decided_by: 'rule:r1', rule_id: '' }, 'Rule r1 allowed it automatically'],
      [{ kind: 'permission', state: 'cancelled', decided_by: 'terminal', rule_id: '' }, 'The agent moved on; nothing for you to do'],
      [{ kind: 'permission', state: 'passthrough', decided_by: 'terminal', rule_id: '' }, 'Handled in the terminal'],
      [{ kind: 'webhook', state: 'expired', decided_by: 'timeout', rule_id: '' }, 'Timed out; the workflow was told "denied"'],
    ]
    for (const [r, want] of cases) expect(outcomeText(r)).toBe(want)
  })

  it('其余组合：规则自动拒绝、没有规则 id、等太久、各个来源都是英文；空串照旧', () => {
    type R = Parameters<typeof outcomeText>[0]
    const r = (over: Partial<R>): R => ({ kind: 'permission', state: 'approved', decided_by: '', rule_id: '', ...over })
    expect(outcomeText(r({ state: 'approved', decided_by: 'user' }))).toBe('You approved')
    expect(outcomeText(r({ state: 'denied', decided_by: 'user', rule_id: 'user-deny-rm' }))).toBe('You denied and remembered it (rule user-deny-rm)')
    expect(outcomeText(r({ state: 'auto_denied', decided_by: 'paused', rule_id: 'user-deny-x' }))).toBe('Rule user-deny-x denied it automatically')
    expect(outcomeText(r({ state: 'auto_allowed', decided_by: '' }))).toBe('A rule allowed it automatically')
    expect(outcomeText(r({ state: 'expired', decided_by: 'timeout' }))).toBe('Timed out; the agent went back to the terminal')
    expect(outcomeText(r({ state: 'pending' }))).toBe('')
    expect(outcomeText(r({ state: 'cancelled', decided_by: 'something_new' }))).toBe('')
    const sources = ['hub_unreachable', 'no_hub', 'agent_restart', 'shutdown', 'resend', 'machine_revoked', 'paused', 'gemini', 'error', 'hub_error']
    for (const decided_by of sources) {
      const text = outcomeText(r({ state: 'passthrough', decided_by }))
      expect(text, decided_by).not.toBe('')
      expect(text, decided_by).not.toMatch(han)
    }
    expect(outcomeText(r({ state: 'expired', decided_by: 'hub_unreachable' }))).toBe("Couldn't reach the hub; the agent handled it in the terminal")
    expect(outcomeText(r({ state: 'passthrough', decided_by: 'paused' }))).toBe('Auto-approval is paused; handed back to the terminal')
  })
})

describe('format 的英文', () => {
  beforeEach(() => setLang('en'))
  afterEach(() => setLang('zh'))

  it('relativeTime：just now、min ago、h ago、yesterday；更早的日期两种语言相同', () => {
    const now = new Date('2026-10-23T12:00:00Z')
    expect(relativeTime('2026-10-23T11:59:50Z', now)).toBe('just now')
    expect(relativeTime('2026-10-23T11:57:00Z', now)).toBe('3 min ago')
    expect(relativeTime('2026-10-23T09:00:00Z', now)).toBe('3 hr ago')
    expect(relativeTime('2026-10-22T09:00:00Z', now)).toBe('yesterday')
    expect(relativeTime('2026-10-01T09:00:00Z', now)).toMatch(/^10-01$/)
  })

  it('风险标签：短名词，与 hub 推送的动词一致', () => {
    expect(riskLabel('read')).toBe('Read-only')
    expect(riskLabel('write_in_project')).toBe('Write in project')
    expect(riskLabel('unknown')).toBe('Unknown')
    expect(riskLabel('write_outside')).toBe('Write outside project')
    expect(riskLabel('network')).toBe('Network')
    expect(riskLabel('outward')).toBe('Outward')
    expect(riskLabel('destructive')).toBe('Destructive')
    expect(riskLabel('')).toBe('Unrated')
    expect(riskLabel('weird')).toBe('weird')
    expect(riskTone('outward')).toBe('high')
  })

  it('状态、种类、审计动作、谁做的', () => {
    const states = ['pending', 'approved', 'denied', 'cancelled', 'expired', 'auto_allowed', 'auto_denied', 'passthrough', 'recorded', 'answered']
    for (const s of states) expect(stateLabel(s), s).not.toMatch(han)
    expect(stateLabel('approved')).toBe('Approved')
    expect(stateLabel('denied')).toBe('Denied')
    expect(stateLabel('auto_allowed')).toBe('Auto-approved')
    expect(stateLabel('passthrough')).toBe('Handed back')
    expect(stateLabel('mystery')).toBe('mystery')
    for (const k of ['permission', 'question', 'done', 'webhook', 'ask']) expect(kindLabel(k), k).not.toMatch(han)
    expect(kindLabel('permission')).toBe('Approval')
    expect(kindLabel('question')).toBe('Waiting for your answer')
    expect(kindLabel('ask')).toBe('Question')
    const actions = ['request_pending', 'allow', 'deny', 'answer', 'rule_learned', 'rule_learn_rolled_back', 'push_sent', 'webhook_callback', 'undeliverable', 'cancelled_resend', 'approved', 'expired']
    for (const a of actions) expect(auditLabel(a), a).not.toMatch(han)
    expect(auditLabel('allow')).toBe('Approval submitted')
    expect(auditLabel('deny')).toBe('Denial submitted')
    expect(auditLabel('answer')).toBe('Answer submitted')
    expect(kindLabel('webhook')).toBe('Webhook')
    expect(auditLabel('approved')).toBe('Approved')
    expect(auditLabel('brand_new')).toBe('brand_new')
    expect(actorLabel('user')).toBe('You')
    expect(actorLabel('agent')).toBe('Daemon')
    expect(actorLabel('system')).toBe('hub')
  })

  it('依据', () => {
    expect(decidedByLabel({ decided_by: 'user', rule_id: '', state: 'answered' })).toBe('Answered on your phone')
    expect(decidedByLabel({ decided_by: 'user', rule_id: '', state: 'approved' })).toBe('Decided on your phone')
    expect(decidedByLabel({ decided_by: 'user', rule_id: 'learned-git-push', state: 'approved' })).toBe('Decided on your phone (remembered as learned-git-push)')
    expect(decidedByLabel({ decided_by: 'rule:user-deny-x', rule_id: '', state: 'auto_denied' })).toBe('Rule user-deny-x')
    const sources = ['terminal', 'timeout', 'hub_unreachable', 'gemini', 'paused', 'shutdown', 'resend', 'agent_restart', 'machine_revoked', 'error']
    for (const decided_by of sources) expect(decidedByLabel({ decided_by, rule_id: '', state: 'cancelled' }), decided_by).not.toMatch(han)
    expect(decidedByLabel({ decided_by: '', rule_id: '', state: 'recorded' })).toBe('Recorded')
  })

  it('engineReasonLabel：照中文前缀解析，输出英文；不认识的原样', () => {
    expect(engineReasonLabel('无规则命中，按 outward 默认询问')).toBe('No rule matched; Outward asks by default')
    expect(engineReasonLabel('无规则命中，按 weird 默认询问')).toBe('No rule matched; weird asks by default')
    expect(engineReasonLabel('命中规则 user-network-ask')).toBe('Matched rule user-network-ask')
    expect(engineReasonLabel('自动审批已暂停')).toBe('Auto-approval is paused')
    expect(engineReasonLabel('自动审批已暂停，交回终端处理')).toBe('Auto-approval is paused; handed back to the terminal')
    expect(engineReasonLabel('等待过期，交回终端处理')).toBe('Timed out; handed back to the terminal')
    expect(engineReasonLabel('脱敏后题目超出上限（x），交回终端处理')).toBe('Handed back to the terminal')
    expect(engineReasonLabel('答案不合规: x')).toBe("The answer didn't fit the questions; handed back to the terminal")
    expect(engineReasonLabel('')).toBe('')
    expect(engineReasonLabel('something else')).toBe('something else')
  })

  it('engineReasonLabel：守护进程固定理由（hubdecider.go、server.go）逐条有英文且无汉字', () => {
    const cases: [string, string][] = [
      ['Gemini 只能拒绝不能放行，交回原生确认框', "Gemini hooks can deny but not approve; handed back to Gemini's own prompt"],
      ['没有配置 hub，交回终端处理', 'No hub is set up; handed back to the terminal'],
      ['连不上 hub，交回终端处理', "Couldn't reach the hub; handed back to the terminal"],
      ['hub 拒绝了本机密钥，交回终端处理', "The hub rejected this machine's key; handed back to the terminal"],
      ['无法送往 hub，交回终端处理', "Couldn't send it to the hub; handed back to the terminal"],
      ['与 hub 断线超过宽限，交回终端处理', 'Lost the hub connection for too long; handed back to the terminal'],
      ['hub 没有给出有效决定，交回终端处理', "The hub didn't return a valid decision; handed back to the terminal"],
      ['hub 版本太旧，不认识提问，交回终端处理', 'The hub is too old to handle questions; handed back to the terminal'],
      ['已在手机上批准', 'Approved on your phone'],
      ['已在手机上拒绝', 'Denied on your phone'],
      ['已在手机上回答', 'Answered on your phone'],
      ['在手机上选了在终端回答', 'You chose on your phone to answer in the terminal'],
      ['自动审批已暂停，交回终端处理', 'Auto-approval is paused; handed back to the terminal'],
      ['脱敏后有题面相同的题，分不清答的是哪道，交回终端处理', "Two questions read the same after redaction, so answers can't be matched; handed back to the terminal"],
      ['决定来源给的答案不完整，交回终端处理', 'The answer was incomplete; handed back to the terminal'],
      ['钩子进程在决定前退出', 'The hook exited before a decision was made'],
      ['守护进程正在停止，交回终端处理', 'The daemon is stopping; handed back to the terminal'],
      ['等待过期，交回终端处理', 'Timed out; handed back to the terminal'],
      ['决定来源出错，交回终端处理', 'Something went wrong while deciding; handed back to the terminal'],
    ]
    for (const [zh, en] of cases) {
      expect(engineReasonLabel(zh), zh).toBe(en)
      expect(engineReasonLabel(zh), zh).not.toMatch(han)
    }
  })

  it('deliveryLabel', () => {
    expect(deliveryLabel(view({ state: 'approved', delivered_at: '2026-10-23T10:00:05Z' }), false)).toBe('Delivered to the agent')
    expect(deliveryLabel(view({ state: 'denied' }), false)).toBe('Not delivered to the agent yet')
    expect(deliveryLabel(view({ state: 'approved', kind: 'webhook', delivered_at: '2026-10-23T10:00:05Z' }), false)).toBe('Sent back to the source')
    expect(deliveryLabel(view({ state: 'approved', kind: 'webhook' }), false)).toBe('Not sent back to the source yet')
    const lines = [
      deliveryLabel(view({ state: 'approved' }), true),
      deliveryLabel(view({ kind: 'ask', state: 'answered' }), true),
      deliveryLabel(view({ kind: 'ask', state: 'answered' }), false),
    ]
    expect(JSON.stringify(lines)).not.toMatch(han)
    expect(lines[0]).toMatch(/^Decision not delivered/)
    expect(lines[1]).toMatch(/^Answer not delivered/)
  })
})

// 角色（0.3 起）相关的决定来源与理由：文案以机器侧（internal/agent/server.go、internal/policy/engine.go、roles.go）的实现为准。
describe('角色相关的决定来源', () => {
  afterEach(() => setLang('zh'))
  const auto = (decided_by: string, engine_reason = '') =>
    ({ kind: 'permission', state: 'auto_denied', decided_by, rule_id: '', engine_reason }) as const
  it('依据（详情）：不露原始值', () => {
    expect(decidedByLabel(auto('role:写作'))).toBe('角色「写作」禁用')
    expect(decidedByLabel(auto('roles_yaml'))).toBe('角色文件有误')
    expect(decidedByLabel(auto('control_plane'))).toBe('触及 Railse 自己的配置')
  })
  it('一句人话的结果（历史、请求页）', () => {
    expect(outcomeText(auto('role:写作', '角色「写作」禁用：命令以 git push 开头'))).toBe('角色「写作」禁用了这个操作')
    expect(outcomeText(auto('role:写作', '超出角色「写作」的边界，Gemini 无法在这里问人：换个角色，或在别处执行'))).toBe(
      '超出角色「写作」的边界，这里问不到你，已拒绝',
    )
    expect(outcomeText(auto('roles_yaml', 'roles.yaml 有误，Gemini 无法在这里问人：修好文件后重试，或在别处执行'))).toBe('角色文件有误，暂按只读把关，已拒绝')
    expect(outcomeText(auto('control_plane'))).toBe('这会改动 Railse 自己的配置，必须你来定；这里问不到你，已拒绝')
  })
  it('英文', () => {
    setLang('en')
    expect(decidedByLabel(auto('role:写作'))).toBe('Forbidden by role "写作"')
    expect(decidedByLabel(auto('roles_yaml'))).toBe('Roles file is broken')
    expect(decidedByLabel(auto('control_plane'))).toBe("Touches Railse's own config")
    expect(outcomeText(auto('role:写作', '角色「写作」禁用：x'))).toBe('Role "写作" forbids this')
    expect(outcomeText(auto('role:写作', '超出角色「写作」的边界，Gemini 无法在这里问人：换个角色，或在别处执行'))).toBe(
      'Beyond role "写作"\'s boundary and there was no one to ask here, so it was denied',
    )
    expect(outcomeText(auto('roles_yaml'))).toBe('The roles file is broken, so only reads pass; denied')
    expect(outcomeText(auto('control_plane'))).toBe("This changes Railse's own config and needs you; there was no one to ask here, so it was denied")
  })
})

describe('角色相关的理由', () => {
  afterEach(() => setLang('zh'))
  it('中文：风险名换成中文叫法，其余原样', () => {
    expect(engineReasonLabel('超出角色「写作」的边界（上限 write_in_project，这次是 outward）')).toBe('超出角色「写作」的边界（上限「项目内改动」，这次是「对外」）')
    expect(engineReasonLabel('角色「写作」禁用：命令以 git push 开头')).toBe('角色「写作」禁用：命令以 git push 开头')
  })
  it('英文：整句换成英文，角色名与命令片段保持原文；继承的后缀与“；”接着的另一句分别处理', () => {
    setLang('en')
    const cases: [string, string][] = [
      ['角色「写作」禁用：命令以 git push 开头', 'Forbidden for role "写作": 命令以 git push 开头'],
      ['超出角色「写作」的边界（上限 write_in_project，这次是 outward）', 'Beyond role "写作"\'s boundary (ceiling Write in project, this is Outward)'],
      ['超出角色「写作」的边界：含无法检查的子命令', 'Beyond role "写作"\'s boundary: contains a subcommand that can\'t be checked'],
      ['超出角色「写作」的边界：试图更改 Railse 的角色或放行设置', 'Beyond role "写作"\'s boundary: tries to change Railse\'s role or trust settings'],
      ['超出角色「写作」的边界，Gemini 无法在这里问人：换个角色，或在别处执行', 'Beyond role "写作"\'s boundary, and Gemini can\'t ask a human here: switch roles, or run it somewhere else'],
      ['roles.yaml 有误：yaml: line 3: bad', 'roles.yaml is broken: yaml: line 3: bad'],
      ['roles.yaml 有误：x；且试图更改 Railse 的角色或放行设置', "roles.yaml is broken: x; and it tries to change Railse's role or trust settings"],
      ['roles.yaml 有误，Gemini 无法在这里问人：修好文件后重试，或在别处执行', "roles.yaml is broken, and Gemini can't ask a human here: fix the file and retry, or run it somewhere else"],
      ['限时放行不覆盖 Railse 自己的配置', "Time-limited trust doesn't cover Railse's own config"],
      ['触及 Railse 自己的配置，Gemini 无法在这里问人：在别处执行', "Touches Railse's own config, and Gemini can't ask a human here: run it somewhere else"],
      ['超出角色「开发」的边界：含无法检查的子命令（继承自上层会话的角色「写作」）', 'Beyond role "开发"\'s boundary: contains a subcommand that can\'t be checked (inherited from the outer session\'s role "写作")'],
      ['限时放行不覆盖 Railse 自己的配置；无规则命中，按 outward 默认询问', "Time-limited trust doesn't cover Railse's own config; No rule matched; Outward asks by default"],
    ]
    for (const [zh, want] of cases) expect(engineReasonLabel(zh), zh).toBe(want)
  })
})
