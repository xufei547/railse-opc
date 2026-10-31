import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import type { AuditEvent, RequestView } from '../types'
import { DecideSheet } from './DecideSheet'
import { RequestContent } from './Payload'
import { AuditList, RequestAttrs } from './RequestAttrs'
import { RequestCard } from './RequestCard'
import { ResultToast } from './ResultToast'

function req(over: Partial<RequestView> = {}): RequestView {
  return {
    id: 'r1', machine_id: 'm', session_id: 's', cli: 'claude', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'shell', command: 'git push origin main', cwd: '/p/blog' }, risk: 'outward', project_root: '/p/blog', engine_reason: '无规则命中',
    state: 'pending', decided_by: '', decision_note: '', rule_id: '', turn_id: 't', cli_session_id: 's', title: '',
    created_at: '2026-10-23T10:00:00.000000000Z', expires_at: null, decided_at: null, delivered_at: null,
    machine_name: 'mac', project: 'blog', has_redacted: false, ...over,
  }
}

const card = (r: RequestView) =>
  renderToStaticMarkup(
    <MemoryRouter>
      <RequestCard request={r} />
    </MemoryRouter>,
  )

const webhook = (over: Partial<RequestView> = {}) =>
  req({
    kind: 'webhook', machine_id: 'webhook', machine_name: '', tool_name: 'webhook', project_root: '', project: '', risk: 'unknown',
    title: '部署 v1.2 到生产', payload: { type: 'other', tool: 'webhook', diff_preview: '先灰度 10%，半小时后全量' }, ...over,
  })

const noop = () => {}

describe('RequestCard', () => {
  it('审批卡片：链接到详情、机器 · 项目、风险标签、命令摘要', () => {
    const html = card(req({ id: 'a/b' }))
    expect(html).toContain('href="/r/a%2Fb"')
    expect(html).toContain('mac · blog')
    expect(html).toContain('对外')
    expect(html).toMatch(/<pre class="cmd">git push origin main<\/pre>/)
    expect(html).not.toContain('含隐藏部分')
  })

  it('含隐藏部分时有标签', () => {
    expect(card(req({ has_redacted: true }))).toContain('含隐藏部分')
  })

  it('webhook 卡片显示 webhook 与标题；问题卡片显示“等你回答”与问题', () => {
    const w = card(webhook())
    expect(w).toContain('webhook')
    expect(w).toContain('部署 v1.2 到生产')
    const q = card(req({ kind: 'question', state: 'recorded', title: '要删旧分支吗？', payload: { type: '' } }))
    expect(q).toContain('等你回答')
    expect(q).toContain('要删旧分支吗？')
  })

  it('命令里的 HTML 原样转义，超长摘要截断', () => {
    const html = card(req({ payload: { type: 'shell', command: '<img src=x onerror=alert(1)>' + 'a'.repeat(1000) } }))
    expect(html).toContain('&lt;img src=x onerror=alert(1)&gt;')
    expect(html).not.toContain('<img')
    expect(html).toContain('…')
    expect(html.length).toBeLessThan(1500)
  })

  it('没有内联样式', () => {
    expect(card(req({ has_redacted: true }))).not.toContain('style=')
    expect(card(webhook())).not.toContain('style=')
  })
})

describe('RequestContent', () => {
  it('shell 命令放在会换行的 pre 里，HTML 转义', () => {
    const html = renderToStaticMarkup(<RequestContent request={req({ payload: { type: 'shell', command: 'echo "<b>x</b>" && rm -rf /tmp/x', cwd: '/p' } })} />)
    expect(html).toMatch(/<pre class="block mono">echo &quot;&lt;b&gt;x&lt;\/b&gt;&quot; &amp;&amp; rm -rf \/tmp\/x<\/pre>/)
    expect(html).toContain('/p')
    expect(html).not.toContain('<b>')
  })

  it('has_redacted：醒目告警在载荷之前，说明不是原文、裸值与引号值的隐藏范围、突出风险与引擎理由', () => {
    const html = renderToStaticMarkup(
      <RequestContent request={req({ has_redacted: true, payload: { type: 'shell', command: 'curl -H "Authorization: <redacted>" https://x' } })} />,
    )
    const warn = html.indexOf('不是原文')
    expect(warn).toBeGreaterThan(-1)
    expect(warn).toBeLessThan(html.indexOf('curl -H'))
    expect(html).toContain('role="alert"')
    expect(html).toContain('&lt;redacted&gt;')
    expect(html).toContain('引号')
    expect(html).toContain('heredoc')
    expect(html).toContain('对外')
    expect(html).toContain('无规则命中')
  })

  it('webhook：标题与描述都显示；含隐藏部分时同样告警', () => {
    const html = renderToStaticMarkup(<RequestContent request={webhook()} />)
    expect(html).toContain('部署 v1.2 到生产')
    expect(html).toContain('先灰度 10%，半小时后全量')
    expect(html).not.toContain('不是原文')
    const red = renderToStaticMarkup(<RequestContent request={webhook({ has_redacted: true, payload: { type: 'other', tool: 'webhook', diff_preview: 'token=<redacted>' } })} />)
    expect(red).toContain('不是原文')
    expect(red).toContain('role="alert"')
    // 标题里有占位符（has_redacted 只看载荷）也要告警
    expect(renderToStaticMarkup(<RequestContent request={webhook({ title: '用 <redacted> 部署' })} />)).toContain('不是原文')
  })

  it('文件改动、联网、MCP 各有展示', () => {
    const fw = renderToStaticMarkup(<RequestContent request={req({ payload: { type: 'file_write', path: '/p/a.go', diff_preview: '+x' } })} />)
    expect(fw).toContain('/p/a.go')
    expect(fw).toContain('+x')
    const nw = renderToStaticMarkup(<RequestContent request={req({ payload: { type: 'network', method: 'POST', url: 'https://api.example.com' } })} />)
    expect(nw).toContain('POST https://api.example.com')
    const mcp = renderToStaticMarkup(<RequestContent request={req({ payload: { type: 'mcp', server: 'fs', tool: 'write', args: { path: '/a' } } })} />)
    expect(mcp).toContain('fs')
    expect(mcp).toContain('write')
    expect(mcp).toContain('&quot;path&quot;: &quot;/a&quot;')
  })

  it('没有内联样式', () => {
    expect(renderToStaticMarkup(<RequestContent request={req({ has_redacted: true })} />)).not.toContain('style=')
    expect(renderToStaticMarkup(<RequestContent request={webhook({ has_redacted: true })} />)).not.toContain('style=')
  })
})

describe('RequestAttrs', () => {
  it('依据用 decidedByLabel：手机上决定并记住时不显示成“规则 …”', () => {
    const html = renderToStaticMarkup(
      <RequestAttrs request={req({ state: 'approved', decided_by: 'user', rule_id: 'learned-net-1', decided_at: '2026-10-23T10:01:00Z' })} audit={[]} />,
    )
    expect(html).toContain('已批准')
    expect(html).toContain('手机上决定（并记住为 learned-net-1）')
    expect(html).toContain('还没送达 agent')
  })

  it('属性：机器、项目、工具、风险、引擎理由、状态、备注', () => {
    const html = renderToStaticMarkup(<RequestAttrs request={req({ decision_note: '不推主分支', state: 'denied', decided_by: 'user' })} audit={[]} />)
    for (const s of ['mac', '/p/blog', 'Bash', '对外', '无规则命中', '已拒绝', '不推主分支']) expect(html).toContain(s)
    expect(html).not.toContain('style=')
  })

  it('审计里有 undeliverable 时标出决定未送达', () => {
    const audit: AuditEvent[] = [{ id: 2, ts: '2026-10-23T10:02:00Z', machine_id: 'm', actor: 'system', request_id: 'r1', action: 'undeliverable', detail: '{"state":"approved"}' }]
    const html = renderToStaticMarkup(<RequestAttrs request={req({ state: 'approved', decided_by: 'user' })} audit={audit} />)
    expect(html).toContain('决定未送达')
  })
})

describe('AuditList', () => {
  it('动作与谁做的译成中文，detail 展开', () => {
    const audit: AuditEvent[] = [
      { id: 2, ts: '2026-10-23T10:01:00Z', machine_id: 'm', actor: 'user', request_id: 'r1', action: 'allow', detail: '{"note":"","remember":true,"rule_id":"learned-x"}' },
      { id: 1, ts: '2026-10-23T10:00:00Z', machine_id: 'm', actor: 'agent', request_id: 'r1', action: 'request_pending', detail: '{"risk":"network","tool":"Bash"}' },
    ]
    const html = renderToStaticMarkup(<AuditList audit={audit} />)
    expect(html).toContain('批准')
    expect(html).toContain('你')
    expect(html).toContain('remember=true rule_id=learned-x')
    expect(html).toContain('登记')
    expect(html).toContain('守护进程')
  })
  it('没有审计时什么也不渲染', () => {
    expect(renderToStaticMarkup(<AuditList audit={[]} />)).toBe('')
  })
})

describe('DecideSheet（初始渲染）', () => {
  it('确认按钮带 data-testid，文案随动作；备注限 1000 字', () => {
    const html = renderToStaticMarkup(<DecideSheet request={req()} action="deny" onClose={noop} onDone={noop} />)
    expect(html).toContain('role="dialog"')
    expect(html).toContain('aria-modal="true"')
    expect(html).toMatch(/data-testid="confirm"[^>]*>拒绝<\/button>/)
    expect(html).toMatch(/maxlength="1000"/i)
    expect(html).toContain('不会给 agent 看')
    expect(html).not.toContain('style=')
  })

  it('webhook 与含隐藏部分的请求：记住开关不可用并说明原因', () => {
    const w = renderToStaticMarkup(<DecideSheet request={webhook()} action="allow" onClose={noop} onDone={noop} />)
    expect(w).toMatch(/<input[^>]*type="checkbox"[^>]*disabled=""/)
    expect(w).toContain('webhook 请求不能记住')
    const r = renderToStaticMarkup(<DecideSheet request={req({ has_redacted: true })} action="allow" onClose={noop} onDone={noop} />)
    expect(r).toMatch(/<input[^>]*type="checkbox"[^>]*disabled=""/)
    expect(r).toContain('含被隐藏部分的请求不能记住')
    const ok = renderToStaticMarkup(<DecideSheet request={req()} action="allow" onClose={noop} onDone={noop} />)
    expect(ok).not.toMatch(/<input[^>]*type="checkbox"[^>]*disabled=""/)
  })
})

describe('ResultToast', () => {
  it('成功：绿色、role=status、标题与文案、可关闭；没有内联样式', () => {
    const html = renderToStaticMarkup(<ResultToast view={{ tone: 'ok', title: '已生效', text: '已批准，决定正在送回 agent' }} onClose={noop} />)
    expect(html).toMatch(/class="toast banner ok"/)
    expect(html).toContain('role="status"')
    expect(html).toContain('aria-live="polite"')
    expect(html).toContain('<strong class="banner-title">已生效</strong>')
    expect(html).toContain('已批准，决定正在送回 agent')
    expect(html).toContain('知道了')
    expect(html).not.toContain('style=')
  })
  it('没有生效：红色、role=alert', () => {
    const html = renderToStaticMarkup(<ResultToast view={{ tone: 'warn', title: '没有生效', text: '请求已过期' }} onClose={noop} />)
    expect(html).toMatch(/class="toast banner warn"/)
    expect(html).toContain('role="alert"')
    expect(html).toContain('没有生效')
    expect(html).toContain('请求已过期')
  })
  it('页面还有批准、拒绝栏时浮在它上面', () => {
    expect(renderToStaticMarkup(<ResultToast view={{ tone: 'warn', title: '没有生效', text: 'x' }} onClose={noop} raised />)).toMatch(/class="toast banner warn raised"/)
  })
})
