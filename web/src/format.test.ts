import { describe, expect, it } from 'vitest'
import { decidedByLabel, kindLabel, parseTime, payloadSummary, relativeTime, riskLabel, riskTone, stateLabel } from './format'

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
