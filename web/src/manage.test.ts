import { describe, expect, it } from 'vitest'
import { ApiError } from './api'
import { elevatedEnabled, failure, fileSize, formatUptime, hitsLabel, importMax, machineSeen, onlineCount, sortMachines, sourceLabel } from './manage'
import type { Machine, Rule, Status } from './types'

const now = new Date('2026-10-23T12:00:00Z')

function rule(over: Partial<Rule> = {}): Rule {
  return {
    id: 'r', source: 'user', enabled: true, elevated: false, hit_count: 0, last_hit_at: null,
    created_at: '2026-10-20T00:00:00.000000000Z', created_from: '', yaml: 'id: r\n', ...over,
  }
}

function machine(over: Partial<Machine> = {}): Machine {
  return {
    id: 'm', name: 'mac', online: false, last_seen: null, agent_version: '0.4.0',
    enrolled_at: '2026-10-20T00:00:00.000000000Z', revoked: false, ...over,
  }
}

describe('sourceLabel', () => {
  it('来源标签', () => {
    expect(sourceLabel('builtin')).toBe('内置')
    expect(sourceLabel('user')).toBe('手写')
    expect(sourceLabel('learned')).toBe('学来的')
    expect(sourceLabel('other')).toBe('other')
  })
})

describe('hitsLabel', () => {
  it('命中次数与最近一次', () => {
    expect(hitsLabel(rule({ hit_count: 0 }), now)).toBe('还没命中过')
    expect(hitsLabel(rule({ hit_count: 3, last_hit_at: '2026-10-23T11:55:00.000000000Z' }), now)).toBe('命中 3 次，最近 5 分钟前')
    expect(hitsLabel(rule({ hit_count: 2, last_hit_at: null }), now)).toBe('命中 2 次')
  })
})

describe('elevatedEnabled', () => {
  it('只数启用着的、能放行高风险操作的规则', () => {
    expect(elevatedEnabled([])).toBe(0)
    expect(
      elevatedEnabled([
        rule({ id: 'a', elevated: true }),
        rule({ id: 'b', elevated: true, enabled: false }),
        rule({ id: 'c' }),
        rule({ id: 'd', elevated: true }),
      ]),
    ).toBe(2)
  })
})

describe('sortMachines', () => {
  it('已吊销的排到最后，其余保持 hub 的顺序；不改原数组', () => {
    const ms = [machine({ id: 'a', revoked: true }), machine({ id: 'b' }), machine({ id: 'c', online: true }), machine({ id: 'd', revoked: true })]
    expect(sortMachines(ms).map((m) => m.id)).toEqual(['b', 'c', 'a', 'd'])
    expect(ms.map((m) => m.id)).toEqual(['a', 'b', 'c', 'd'])
  })
})

describe('machineSeen', () => {
  it('在线、最近在线、从未连上；吊销的不说在线', () => {
    expect(machineSeen(machine({ online: true, last_seen: '2026-10-23T11:59:50.000000000Z' }), now)).toBe('在线')
    expect(machineSeen(machine({ last_seen: '2026-10-23T10:00:00.000000000Z' }), now)).toBe('最近在线 2 小时前')
    expect(machineSeen(machine(), now)).toBe('从未连上')
    expect(machineSeen(machine({ revoked: true, online: true, last_seen: '2026-10-23T10:00:00.000000000Z' }), now)).toBe('最近在线 2 小时前')
  })
})

describe('onlineCount', () => {
  it('在线数与总数都不算已吊销的', () => {
    const status = {
      machines: [
        { id: 'a', name: 'a', online: true, last_seen: null, revoked: false },
        { id: 'b', name: 'b', online: false, last_seen: null, revoked: false },
        { id: 'c', name: 'c', online: true, last_seen: null, revoked: true },
      ],
    } as Status
    expect(onlineCount(status)).toEqual({ online: 1, total: 2 })
    expect(onlineCount(null)).toEqual({ online: 0, total: 0 })
  })
})

describe('formatUptime', () => {
  it('把 Go 的时长文本（取整到秒）写成中文，只留两级', () => {
    expect(formatUptime('0s')).toBe('0 秒')
    expect(formatUptime('45s')).toBe('45 秒')
    expect(formatUptime('3m20s')).toBe('3 分钟')
    expect(formatUptime('1h2m3s')).toBe('1 小时 2 分钟')
    expect(formatUptime('5h0m9s')).toBe('5 小时')
    expect(formatUptime('26h5m0s')).toBe('1 天 2 小时')
    expect(formatUptime('72h0m5s')).toBe('3 天')
    expect(formatUptime('2400h0m0s')).toBe('100 天')
  })

  it('认不出的原样，空串是“-”', () => {
    expect(formatUptime('1.5s')).toBe('1.5s')
    expect(formatUptime('abc')).toBe('abc')
    expect(formatUptime('')).toBe('-')
  })
})

describe('failure', () => {
  it('hub 的错误原文照给（409、422 不解析）；401 回 null（已在跳登录页，不另提示）', () => {
    expect(failure(new ApiError(409, '请求已经处理过'), '操作失败')).toBe('请求已经处理过')
    expect(failure(new ApiError(400, 'rules[0]: 缺少 id\n第二行'), '导入失败')).toBe('rules[0]: 缺少 id\n第二行')
    expect(failure(new ApiError(0, '连不上 hub，请检查网络'), '操作失败')).toBe('连不上 hub，请检查网络')
    expect(failure(new ApiError(401, '请先登录'), '操作失败')).toBeNull()
  })

  it('不是 Error 或没有文案时用兜底文案', () => {
    expect(failure('boom', '操作失败')).toBe('操作失败')
    expect(failure(new Error(''), '加载失败')).toBe('加载失败')
  })
})

describe('fileSize 与 importMax', () => {
  it('导入确认里显示文件大小', () => {
    expect(fileSize(0)).toBe('0 字节')
    expect(fileSize(900)).toBe('900 字节')
    expect(fileSize(1024)).toBe('1 KB')
    expect(fileSize(1500)).toBe('2 KB')
    expect(fileSize(1024 * 1024)).toBe('1024 KB')
    expect(fileSize(3 * 1024 * 1024 + 1)).toBe('3.0 MB')
  })

  it('与 hub 的导入上限一致（1 MiB）', () => {
    expect(importMax).toBe(1 << 20)
  })
})
