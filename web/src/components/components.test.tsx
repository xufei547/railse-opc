import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import type { AuditEvent, RequestView } from '../types'
import { DecideSheet } from './DecideSheet'
import { RequestSummary, StatusLine } from './Explain'
import { RequestContent } from './Payload'
import { AuditList, RequestAttrs, RequestDetails } from './RequestAttrs'
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
    kind: 'webhook', cli: '', machine_id: 'webhook', machine_name: '', tool_name: 'webhook', project_root: '', project: '', risk: 'unknown',
    title: '部署 v1.2 到生产', payload: { type: 'other', tool: 'webhook', diff_preview: '先灰度 10%，半小时后全量' }, ...over,
  })

const noop = () => {}

describe('RequestCard', () => {
  it('审批卡片：第一行做什么（不带 CLI 名）、风险标签；第二行命令；第三行 CLI · 机器 · 项目', () => {
    const html = card(req({ id: 'a/b' }))
    expect(html).toContain('href="/r/a%2Fb"')
    expect(html).toContain('<span class="what">把代码推到远程仓库（origin 的 main）</span>')
    expect(html).toContain('对外')
    expect(html).toMatch(/<pre class="cmd">git push origin main<\/pre>/)
    expect(html).toContain('<div class="where">Claude Code · mac · blog</div>')
    expect(html.indexOf('class="what"')).toBeLessThan(html.indexOf('class="cmd"'))
    expect(html.indexOf('class="cmd"')).toBeLessThan(html.indexOf('class="where"'))
    expect(html).not.toContain('含隐藏部分')
  })

  it('cli 为空时第三行不写 Agent；机器名去 .local', () => {
    expect(card(req({ cli: '', machine_name: 'mac.local' }))).toContain('<div class="where">mac · blog</div>')
  })

  it('含隐藏部分时有标签', () => {
    expect(card(req({ has_redacted: true }))).toContain('含隐藏部分')
  })

  it('文件改动的第二行是路径', () => {
    const html = card(req({ risk: 'write_outside', payload: { type: 'file_write', path: '/etc/hosts' } }))
    expect(html).toContain('<span class="what">修改项目外的文件 /etc/hosts</span>')
    expect(html).toContain('<pre class="cmd">/etc/hosts</pre>')
  })

  it('webhook 卡片：工作流等你批准、标题、第三行 webhook；问题卡片：在终端等你回答、问题、“等你回答”标签', () => {
    const w = card(webhook())
    expect(w).toContain('<span class="what">工作流等你批准</span>')
    expect(w).toContain('<pre class="cmd">部署 v1.2 到生产</pre>')
    expect(w).toContain('<div class="where">webhook</div>')
    const q = card(req({ kind: 'question', state: 'recorded', title: '要删旧分支吗？', payload: { type: '' } }))
    expect(q).toContain('<span class="what">在终端等你回答</span>')
    expect(q).toContain('<span class="tag plain">等你回答</span>')
    expect(q).toContain('<pre class="cmd">要删旧分支吗？</pre>')
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
  it('shell 命令放在会换行的 pre 里，HTML 转义；目录不在这里（在详情里）', () => {
    const html = renderToStaticMarkup(<RequestContent request={req({ payload: { type: 'shell', command: 'echo "<b>x</b>" && rm -rf /tmp/x', cwd: '/p/elsewhere' } })} />)
    expect(html).toMatch(/<pre class="block mono">echo &quot;&lt;b&gt;x&lt;\/b&gt;&quot; &amp;&amp; rm -rf \/tmp\/x<\/pre>/)
    expect(html).not.toContain('/p/elsewhere')
    expect(html).not.toContain('<b>')
  })

  it('webhook：标题与描述都显示', () => {
    const html = renderToStaticMarkup(<RequestContent request={webhook()} />)
    expect(html).toContain('部署 v1.2 到生产')
    expect(html).toContain('先灰度 10%，半小时后全量')
    expect(html).not.toContain('不是原文')
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

describe('RequestSummary', () => {
  const summary = (r: RequestView) => renderToStaticMarkup(<RequestSummary request={r} />)

  it('说明卡在最上面：headline、where、why；命令框在卡片下面；next 三行在命令框下面', () => {
    const html = summary(req({ machine_name: 'mac.local', engine_reason: '无规则命中，按 outward 默认询问' }))
    expect(html).toContain('<section class="explain" aria-label="这条请求要做什么">')
    expect(html).toContain('<p class="headline">Claude Code 想把代码推到远程仓库（origin 的 main）</p>')
    expect(html).toContain('<p class="where">项目 blog，机器 mac</p>')
    expect(html).toContain('<p class="why">这会影响仓库之外的人和系统（推送、发布、部署）。规则永远不会自动放行这类操作，要你决定。</p>')
    const head = html.indexOf('class="headline"')
    const cmd = html.indexOf('<pre class="block mono">git push origin main</pre>')
    const next = html.indexOf('<ul class="next">')
    expect(head).toBeGreaterThan(-1)
    expect(head).toBeLessThan(cmd)
    expect(cmd).toBeLessThan(next)
    for (const s of ['批准：agent 执行这一条，只这一次。', '拒绝：agent 收到拒绝，会换个做法或停下来。', '勾「记住」只能存成拒绝规则，下次同类的直接拒绝；这类操作不能记成放行。']) {
      expect(html).toContain(`<li>${s}</li>`)
    }
    expect(html).not.toMatch(/outward|write_in_project|destructive/)
    expect(html).not.toContain('style=')
  })

  it('has_redacted：醒目告警在说明卡之前，说明不是原文、隐藏范围、突出风险与理由（中文）；没有“记住”那行', () => {
    const html = summary(req({ has_redacted: true, engine_reason: '无规则命中，按 outward 默认询问', payload: { type: 'shell', command: 'curl -H "Authorization: <redacted>" https://x' } }))
    const warn = html.indexOf('不是原文')
    expect(warn).toBeGreaterThan(-1)
    expect(warn).toBeLessThan(html.indexOf('class="headline"'))
    expect(html.indexOf('class="headline"')).toBeLessThan(html.indexOf('curl -H'))
    expect(html).toContain('role="alert"')
    expect(html).toContain('&lt;redacted&gt;')
    expect(html).toContain('引号')
    expect(html).toContain('heredoc')
    expect(html).toContain('对外')
    expect(html).toContain('无规则命中，按「对外」默认询问')
    expect(html).not.toContain('勾「记住」')
  })

  it('webhook：含隐藏部分时同样告警；说明卡、标题与描述、一行 next', () => {
    const html = summary(webhook({ has_redacted: true }))
    expect(html.indexOf('不是原文')).toBeLessThan(html.indexOf('class="headline"'))
    expect(html).toContain('<p class="headline">工作流等你批准：部署 v1.2 到生产</p>')
    expect(html).toContain('先灰度 10%，半小时后全量')
    expect(html).toContain('<li>批准或拒绝会回调给发起它的工作流。</li>')
    expect(html).not.toContain('class="where"')
    expect(html).not.toContain('class="why"')
  })

  it('标题里有占位符（has_redacted 为 false）时同样告警，且在 headline 之前', () => {
    const html = summary(webhook({ title: '用 <redacted> 部署' }))
    expect(html).toContain('不是原文')
    expect(html.indexOf('不是原文')).toBeLessThan(html.indexOf('class="headline"'))
  })

  it('已自动处理的请求没有“为什么”和 next；还等你决定的有', () => {
    const done = summary(req({ state: 'auto_allowed', risk: 'read', engine_reason: '命中规则 builtin-read', decided_by: 'rule' }))
    expect(done).not.toContain('class="why"')
    expect(done).not.toContain('class="next"')
    expect(summary(req())).toContain('class="why"')
    expect(summary(req({ risk: 'outward' }))).toContain('勾「记住」只能存成拒绝规则')
  })

  it('question：去终端回答的提醒；已处理的审批没有 next', () => {
    const q = summary(req({ kind: 'question', state: 'recorded', title: '要删旧分支吗？', payload: { type: '' } }))
    expect(q).toContain('<p class="headline">Claude Code 在终端等你回答</p>')
    expect(q).toContain('要删旧分支吗？')
    expect(q).toContain('<li>去那台机器的终端回答，这里只是提醒。</li>')
    expect(summary(req({ state: 'approved', decided_by: 'user' }))).not.toContain('class="next"')
  })
})

describe('StatusLine', () => {
  const line = (r: RequestView, audit: AuditEvent[] = []) => renderToStaticMarkup(<StatusLine request={r} audit={audit} />)

  it('待审批、question 不显示', () => {
    expect(line(req())).toBe('')
    expect(line(req({ kind: 'question', state: 'recorded', title: 'q' }))).toBe('')
  })

  it('已处理：状态标签、依据、决定时间、送达，一行', () => {
    const html = line(req({ state: 'approved', decided_by: 'user', decided_at: '2026-10-23T10:01:00.000000000Z', delivered_at: '2026-10-23T10:01:01.000000000Z' }))
    expect(html).toMatch(/^<p class="status-line">/)
    expect(html).toContain('<span class="tag low">已批准</span>')
    expect(html).toContain('手机上决定')
    expect(html).toContain('已送达 agent')
    expect(html).toContain('<time dateTime="2026-10-23T10:01:00.000000000Z">')
    const denied = line(req({ state: 'denied', decided_by: 'user' }), [
      { id: 2, ts: '2026-10-23T10:02:00Z', machine_id: 'm', actor: 'system', request_id: 'r1', action: 'undeliverable', detail: '' },
    ])
    expect(denied).toContain('<span class="tag high">已拒绝</span>')
    expect(denied).toContain('决定未送达')
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

  it('属性：目录、机器、项目、工具、风险、引擎理由（中文）、状态、备注', () => {
    const html = renderToStaticMarkup(
      <RequestAttrs request={req({ engine_reason: '无规则命中，按 outward 默认询问', decision_note: '不推主分支', state: 'denied', decided_by: 'user' })} audit={[]} />,
    )
    for (const s of ['<dt>目录</dt>', '/p/blog', 'mac', 'Bash', '对外', '无规则命中，按「对外」默认询问', '已拒绝', '不推主分支']) expect(html).toContain(s)
    expect(html).not.toContain('outward')
    expect(html).not.toContain('style=')
  })

  it('审计里有 undeliverable 时标出决定未送达', () => {
    const audit: AuditEvent[] = [{ id: 2, ts: '2026-10-23T10:02:00Z', machine_id: 'm', actor: 'system', request_id: 'r1', action: 'undeliverable', detail: '{"state":"approved"}' }]
    const html = renderToStaticMarkup(<RequestAttrs request={req({ state: 'approved', decided_by: 'user' })} audit={audit} />)
    expect(html).toContain('决定未送达')
  })
})

describe('RequestDetails', () => {
  it('默认折叠的“详情”，里面是字段表与审计', () => {
    const audit: AuditEvent[] = [{ id: 1, ts: '2026-10-23T10:00:00Z', machine_id: 'm', actor: 'system', request_id: 'r1', action: 'push_sent', detail: '' }]
    const html = renderToStaticMarkup(<RequestDetails request={req()} audit={audit} />)
    expect(html).toMatch(/^<details class="more"><summary>详情<\/summary>/)
    expect(html).not.toMatch(/<details[^>]* open/)
    expect(html).toContain('<dl class="kv">')
    expect(html).toContain('审计')
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
