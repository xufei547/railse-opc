import { describe, expect, it } from 'vitest'
import {
  absoluteTime,
  actorLabel,
  auditDetail,
  auditLabel,
  clip,
  decidedByLabel,
  deliveryLabel,
  isRedacted,
  kindLabel,
  parseTime,
  payloadSummary,
  relativeTime,
  riskLabel,
  riskTone,
  stateLabel,
} from './format'
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
  })
  it('状态与种类', () => {
    expect(stateLabel('auto_allowed')).toBe('自动放行')
    expect(stateLabel('passthrough')).toBe('交回终端')
    expect(stateLabel('mystery')).toBe('mystery')
    expect(kindLabel('question')).toBe('等你回答')
    expect(kindLabel('webhook')).toBe('webhook')
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
})
