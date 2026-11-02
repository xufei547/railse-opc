import { afterEach, describe, expect, it } from 'vitest'
import { setLang } from './i18n'
import { createdByLabel, earliestUntil, remainingLabel, trustBanner, trustMeta, trustWhere, untilClock } from './trust'
import type { TrustView } from './types'

function tv(over: Partial<TrustView> = {}): TrustView {
  return {
    id: 't1', machine_id: 'm', scope: 'session', tool: 'claude', cli_session_id: 's1', project_root: '/p/proj',
    until: '2026-11-02T12:30:00.000000000Z', created_at: '2026-11-02T10:30:00.000000000Z', created_by: 'phone', note: '',
    machine_name: 'mac.local', project: 'proj', remaining_seconds: 7080, ...over,
  }
}

const cjk = /[　-〿一-鿿＀-￯]/

afterEach(() => setLang('zh'))

describe('放行的范围', () => {
  it('会话：CLI 名 · 项目 · 机器；项目：项目 · 机器；机器名去掉 .local', () => {
    expect(trustWhere(tv())).toBe('会话 Claude Code · 项目 proj · 机器 mac')
    expect(trustWhere(tv({ scope: 'project', tool: '', cli_session_id: '' }))).toBe('项目 proj · 机器 mac')
    // 会话的项目根不知道时只说会话与机器
    expect(trustWhere(tv({ project: '', project_root: '' }))).toBe('会话 Claude Code · 机器 mac')
  })

  it('英文', () => {
    setLang('en')
    expect(trustWhere(tv())).toBe('Session Claude Code · Project proj · Machine mac')
    expect(trustWhere(tv({ scope: 'project' }))).toBe('Project proj · Machine mac')
  })
})

describe('剩余时间', () => {
  it('中文：小时与分钟，分钟向上取整；不到 1 分钟单说', () => {
    expect(remainingLabel(7080)).toBe('剩 1 小时 58 分')
    expect(remainingLabel(7200)).toBe('剩 2 小时')
    expect(remainingLabel(299)).toBe('剩 5 分')
    expect(remainingLabel(30)).toBe('剩不到 1 分')
    expect(remainingLabel(0)).toBe('剩不到 1 分')
  })

  it('英文', () => {
    setLang('en')
    expect(remainingLabel(7080)).toBe('1 hr 58 min left')
    expect(remainingLabel(7200)).toBe('2 hr left')
    expect(remainingLabel(299)).toBe('5 min left')
    expect(remainingLabel(30)).toBe('less than 1 min left')
  })
})

describe('谁建的与第二行', () => {
  it('手机、终端、启动命令；不认识的原样', () => {
    expect(createdByLabel('phone')).toBe('手机建')
    expect(createdByLabel('cli')).toBe('终端建')
    expect(createdByLabel('env')).toBe('启动命令建')
    expect(createdByLabel('robot')).toBe('robot')
  })

  it('剩余 · 谁建的 · 备注（没有备注不带）', () => {
    expect(trustMeta(tv())).toBe('剩 1 小时 58 分 · 手机建')
    expect(trustMeta(tv({ note: '跑测试' }))).toBe('剩 1 小时 58 分 · 手机建 · 跑测试')
    setLang('en')
    expect(trustMeta(tv({ created_by: 'env' }))).toBe('1 hr 58 min left · From the launch command')
    expect(trustMeta(tv({ created_by: 'cli' }))).not.toMatch(cjk)
  })
})

describe('收件箱横幅', () => {
  const now = new Date(2026, 10, 2, 10, 0, 0)
  const at = (h: number, m: number, day = 2) => new Date(2026, 10, day, h, m, 0).toISOString()

  it('最早到期的那条；空列表回空串', () => {
    expect(earliestUntil([tv({ until: at(14, 0) }), tv({ until: at(12, 30) })])).toBe(at(12, 30))
    expect(earliestUntil([])).toBe('')
  })

  it('同一天只写时分，别的日子带日期', () => {
    expect(untilClock(at(12, 30), now)).toBe('12:30')
    expect(untilClock(at(9, 5, 3), now)).toBe('11-03 09:05')
    expect(untilClock('nope', now)).toBe('')
  })

  it('有列表时带最近的到期时间，没有列表时只说条数', () => {
    expect(trustBanner(2, [tv({ until: at(14, 0) }), tv({ until: at(12, 30) })], now)).toBe('2 个放行生效中，最近的 12:30 到期')
    expect(trustBanner(2, null, now)).toBe('2 个放行生效中')
    setLang('en')
    expect(trustBanner(1, [tv({ until: at(12, 30) })], now)).toBe('1 trust active, ends at 12:30')
    expect(trustBanner(2, [tv({ until: at(12, 30) })], now)).toBe('2 trusts active, the soonest ends at 12:30')
    expect(trustBanner(3, null, now)).toBe('3 trusts active')
  })
})
