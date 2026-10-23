import type { ReactNode } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { ConfirmSheet } from '../components/Confirm'
import { StoreProvider } from '../StoreProvider'
import type { Machine, RequestView, Rule } from '../types'
import { History, HistoryCard } from './History'
import { MachineRow, Machines } from './Machines'
import { RuleCard, Rules } from './Rules'
import { Settings } from './Settings'

const noop = () => {}
const inRouter = (node: ReactNode, path = '/') => renderToStaticMarkup(<MemoryRouter initialEntries={[path]}>{node}</MemoryRouter>)

function req(over: Partial<RequestView> = {}): RequestView {
  return {
    id: 'r1', machine_id: 'm', session_id: 's', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'shell', command: 'git push origin main' }, risk: 'outward', project_root: '/p/blog', engine_reason: '',
    state: 'approved', decided_by: 'user', decision_note: '', rule_id: '', turn_id: '', cli_session_id: '', title: '',
    created_at: '2026-10-23T10:00:00.000000000Z', expires_at: null, decided_at: null, delivered_at: null,
    machine_name: 'mac', project: 'blog', has_redacted: false, ...over,
  }
}

function rule(over: Partial<Rule> = {}): Rule {
  return {
    id: 'user-allow-deploy', source: 'user', enabled: true, elevated: false, hit_count: 0, last_hit_at: null,
    created_at: '2026-10-20T00:00:00.000000000Z', created_from: '', yaml: 'id: user-allow-deploy\naction: allow\n', ...over,
  }
}

function machine(over: Partial<Machine> = {}): Machine {
  return {
    id: 'm1', name: 'mac-mini', online: true, last_seen: '2026-10-23T10:00:00.000000000Z', agent_version: '0.4.0',
    enrolled_at: '2026-10-20T00:00:00.000000000Z', revoked: false, ...over,
  }
}

describe('HistoryCard', () => {
  it('链接到详情；机器 · 项目、状态标签（按色调）、摘要与依据', () => {
    const html = inRouter(<HistoryCard request={req({ id: 'a/b', rule_id: 'learned-git-push' })} />)
    expect(html).toContain('href="/r/a%2Fb"')
    expect(html).toContain('mac · blog')
    expect(html).toContain('<span class="tag low">已批准</span>')
    expect(html).toContain('git push origin main')
    expect(html).toContain('手机上决定（并记住为 learned-git-push）')
    expect(html).not.toContain('含隐藏部分')
  })

  it('拒绝红、含隐藏部分有标签；webhook 显示 webhook 与标题', () => {
    expect(inRouter(<HistoryCard request={req({ state: 'auto_denied', decided_by: 'rule:user-deny', rule_id: 'user-deny' })} />)).toContain(
      '<span class="tag high">自动拒绝</span>',
    )
    expect(inRouter(<HistoryCard request={req({ has_redacted: true })} />)).toContain('含隐藏部分')
    const w = inRouter(<HistoryCard request={req({ kind: 'webhook', machine_name: '', project: '', title: '部署 v1.2', state: 'expired', decided_by: 'timeout' })} />)
    expect(w).toContain('webhook')
    expect(w).toContain('部署 v1.2')
    expect(w).toContain('已过期')
    expect(w).toContain('等待过期')
  })

  it('问题没有依据行；HTML 原样转义；超长摘要截断；没有内联样式', () => {
    const q = inRouter(<HistoryCard request={req({ kind: 'question', state: 'recorded', decided_by: '', title: '要删旧分支吗？' })} />)
    expect(q).toContain('问题')
    expect(q).not.toContain('class="sub line"')
    const html = inRouter(<HistoryCard request={req({ payload: { type: 'shell', command: '<img src=x onerror=alert(1)>' + 'a'.repeat(1000) } })} />)
    expect(html).toContain('&lt;img src=x onerror=alert(1)&gt;')
    expect(html).not.toContain('<img')
    expect(html.length).toBeLessThan(1500)
    expect(html).not.toContain('style=')
  })
})

describe('RuleCard', () => {
  const card = (r: Rule, busy = false) => inRouter(<RuleCard rule={r} busy={busy} onToggle={noop} onDelete={noop} />)

  it('elevated 规则：红色标签“可放行高风险操作”，启用着时整张卡片红框', () => {
    const html = card(rule({ elevated: true }))
    expect(html).toContain('<span class="tag high">可放行高风险操作</span>')
    expect(html).toMatch(/class="card rule elevated"/)
    // 停用着：标签还在（重新启用就会生效），不画红框，标“已停用”并灰显
    const off = card(rule({ elevated: true, enabled: false }))
    expect(off).toContain('可放行高风险操作')
    expect(off).toMatch(/class="card rule off"/)
    expect(off).toContain('已停用')
  })

  it('来源标签、命中次数与来自哪条请求；YAML 在可展开的块里（转义）', () => {
    const html = card(rule({ source: 'learned', hit_count: 3, created_from: 'abcdef0123456789', yaml: 'id: x\nmatch: "<b>"\n' }))
    expect(html).toContain('<span class="tag plain">学来的</span>')
    expect(html).toContain('命中 3 次')
    expect(html).toContain('href="/r/abcdef0123456789"')
    expect(html).toContain('>abcdef01</a>')
    expect(html).toMatch(/<details><summary>规则原文<\/summary><pre class="block mono">id: x\nmatch: &quot;&lt;b&gt;&quot;\n<\/pre><\/details>/)
  })

  it('启停开关是 role=switch 的复选框，名字带规则 id；忙时 disabled', () => {
    const on = card(rule())
    expect(on).toMatch(/<input type="checkbox" role="switch" aria-label="启用规则 user-allow-deploy" checked=""/)
    expect(card(rule({ enabled: false }))).not.toMatch(/role="switch"[^>]*checked/)
    expect(card(rule(), true)).toMatch(/role="switch"[^>]*disabled=""/)
  })

  it('内置规则不能删；其余有删除按钮', () => {
    expect(card(rule({ source: 'builtin', id: 'builtin-read' }))).not.toContain('删除')
    expect(card(rule())).toContain('aria-label="删除规则 user-allow-deploy"')
  })

  it('没有内联样式', () => {
    expect(card(rule({ elevated: true, created_from: 'r1' }))).not.toContain('style=')
  })
})

describe('MachineRow', () => {
  const row = (m: Machine, busy = false) => renderToStaticMarkup(<MachineRow machine={m} busy={busy} onRevoke={noop} />)

  it('在线：绿点（有读屏文字）、名字、版本、在线；可吊销', () => {
    const html = row(machine())
    expect(html).toMatch(/<span class="dot on" role="img" aria-label="在线"/)
    expect(html).toContain('mac-mini')
    expect(html).toContain('版本 0.4.0')
    expect(html).toContain('在线')
    expect(html).toContain('aria-label="吊销 mac-mini"')
  })

  it('离线写最近在线；没有版本写版本未知', () => {
    const html = row(machine({ online: false, agent_version: '' }))
    expect(html).toMatch(/<span class="dot" role="img" aria-label="离线"/)
    expect(html).toContain('最近在线')
    expect(html).toContain('版本未知')
  })

  it('已吊销：灰显、标签、没有吊销按钮，也不显示在线', () => {
    const html = row(machine({ revoked: true }))
    expect(html).toMatch(/class="list-item revoked"/)
    expect(html).toContain('已吊销')
    expect(html).not.toContain('aria-label="吊销')
    expect(html).toMatch(/<span class="dot" role="img" aria-label="已吊销"/)
  })

  it('名字里的 HTML 转义；没有内联样式', () => {
    const html = row(machine({ name: '<b>x</b>' }))
    expect(html).toContain('&lt;b&gt;x&lt;/b&gt;')
    expect(html).not.toContain('style=')
  })
})

describe('ConfirmSheet', () => {
  const sheet = (busy: boolean, error: string | null, danger = true) =>
    renderToStaticMarkup(
      <ConfirmSheet
        state={{ req: { title: '删除这条规则？', body: 'user-x\n不能撤销', confirmLabel: '删除', danger }, busy, error }}
        onCancel={noop}
        onConfirm={noop}
      />,
    )

  it('是带标题与说明的模态 alertdialog；确认按钮按 danger 着色', () => {
    const html = sheet(false, null)
    expect(html).toContain('role="alertdialog"')
    expect(html).toContain('aria-modal="true"')
    expect(html).toContain('aria-labelledby="confirm-title"')
    expect(html).toContain('aria-describedby="confirm-body"')
    expect(html).toContain('删除这条规则？')
    expect(html).toContain('user-x\n不能撤销')
    expect(html).toMatch(/class="btn danger"[^>]*>删除<\/button>/)
    expect(sheet(false, null, false)).toMatch(/class="btn primary"[^>]*>删除<\/button>/)
    expect(html).toContain('class="sheet-backdrop"')
  })

  it('失败时显示 hub 的原文（role=alert），可以再点；提交中两个按钮都 disabled', () => {
    const failed = sheet(false, 'rules[0]: 缺少 id')
    expect(failed).toMatch(/<div class="error pre-wrap" role="alert">rules\[0\]: 缺少 id<\/div>/)
    expect(failed).not.toContain('disabled')
    const busy = sheet(true, null)
    expect(busy).toContain('正在提交…')
    expect(busy.match(/disabled=""/g)?.length).toBe(2)
  })

  it('没有内联样式', () => {
    expect(sheet(false, 'x')).not.toContain('style=')
  })
})

describe('四个页面的首屏（还没数据）', () => {
  const page = (node: ReactNode, path: string) => inRouter(<StoreProvider onUnauthorized={noop}>{node}</StoreProvider>, path)

  it('历史：有筛选（选中地址栏里的）与加载中', () => {
    const html = page(<History />, '/history?state=denied')
    expect(html).toContain('<h1>历史</h1>')
    expect(html).toMatch(/<option value="denied" selected="">已拒绝<\/option>/)
    expect(html).toContain('问题与完成')
    expect(html).toContain('加载中…')
  })

  it('规则：导出是指向 hub 导出接口的同源下载链接，导入是按钮', () => {
    const html = page(<Rules />, '/rules')
    expect(html).toContain('<a class="btn" href="/api/rules/export" download="railse-rules.yaml">导出</a>')
    expect(html).toContain('导入')
    expect(html).toContain('type="file"')
    expect(html).toContain('加载中…')
  })

  it('机器：加载中', () => {
    expect(page(<Machines />, '/machines')).toContain('加载中…')
  })

  it('设置：没拿到状态时总开关 disabled；hub 信息与退出登录', () => {
    const html = page(<Settings />, '/settings')
    expect(html).toMatch(/<input type="checkbox" role="switch" disabled=""/)
    expect(html).toContain('规则版本')
    expect(html).toContain('在线机器')
    expect(html).toContain('退出登录')
  })

  it('都没有内联样式', () => {
    for (const [node, path] of [
      [<History key="h" />, '/history'],
      [<Rules key="r" />, '/rules'],
      [<Machines key="m" />, '/machines'],
      [<Settings key="s" />, '/settings'],
    ] as const) {
      expect(page(node, path)).not.toContain('style=')
    }
  })
})
