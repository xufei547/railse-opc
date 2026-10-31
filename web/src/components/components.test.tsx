import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { initAsk, type AskAction, type AskState } from '../ask'
import { setLang } from '../i18n'
import type { AuditEvent, Question, RequestView } from '../types'
import { AskFormView } from './AskForm'
import { ConfirmSheet } from './Confirm'
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

// 真机上 AskUserQuestion 的题（Claude Code 2.1.280），外加一道多选、一道没有选项的
const fruit: Question = {
  question: '你更喜欢苹果还是香蕉？',
  header: '水果',
  options: [{ label: 'Apple', description: '苹果' }, { label: 'Banana', description: '香蕉' }],
}
const many: Question = { question: '要哪些？', options: [{ label: 'x' }, { label: 'y' }], multiSelect: true }
const free: Question = { question: '还有什么要说的？', options: null }

const ask = (over: Partial<RequestView> = {}) =>
  req({ kind: 'ask', tool_name: 'AskUserQuestion', payload: { type: '' }, risk: '', engine_reason: '', ask: { questions: [fruit] }, ...over })

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

describe('RequestCard：提问', () => {
  it('第一行在问你与第一题、“提问”标签，没有风险标签；第二行第一题的选项用“ / ”连起来；第三行 CLI · 机器 · 项目', () => {
    const html = card(ask({ ask: { questions: [fruit, many] } }))
    expect(html).toContain('<span class="what">在问你：你更喜欢苹果还是香蕉？</span>')
    expect(html).toContain('<span class="tag plain">提问</span>')
    expect(html).not.toMatch(/class="tag (low|mid|high)"/)
    expect(html).not.toContain('未分级')
    expect(html).toContain('<pre class="cmd">Apple / Banana</pre>')
    expect(html).toContain('<div class="where">Claude Code · mac · blog</div>')
  })

  it('没有选项（options 为 null 或缺失）时第二行为空；题目里的 <redacted> 照常显示并有“含隐藏部分”', () => {
    expect(card(ask({ ask: { questions: [free] } }))).toContain('<pre class="cmd"></pre>')
    expect(card(ask({ ask: { questions: [{ question: 'q' }] } }))).toContain('<pre class="cmd"></pre>')
    const r = card(ask({ has_redacted: true, ask: { questions: [{ question: 'token=<redacted> 还能用吗？', options: [] }] } }))
    expect(r).toContain('在问你：token=&lt;redacted&gt; 还能用吗？')
    expect(r).toContain('<span class="tag high">含隐藏部分</span>')
    expect(r).not.toContain('style=')
  })
})

describe('AskFormView', () => {
  const form = (questions: Question[], state: AskState = initAsk(questions)) =>
    renderToStaticMarkup(<AskFormView questions={questions} state={state} dispatch={noop as (a: AskAction) => void} onAnswer={noop} onPassthrough={noop} />)
  const st = (questions: Question[], over: Partial<AskState>): AskState => ({ ...initAsk(questions), ...over })
  /** 页面上的 input 标签（属性顺序由 React 定，断言不依赖它）。 */
  const inputTags = (html: string, type: string) => [...html.matchAll(/<input [^>]*\/>/g)].map((m) => m[0]).filter((t) => t.includes(`type="${type}"`))

  it('每题一组（role=group，名字是题面）：标题小标签、题面、选项（单选 radio、带说明）、自己填', () => {
    const html = form([fruit])
    expect(html).toContain('<span class="tag plain">水果</span>')
    const group = /<div class="question" role="group" aria-labelledby="([^"]+)">/.exec(html)
    expect(group).not.toBeNull()
    expect(html).toContain(`id="${group![1]}">你更喜欢苹果还是香蕉？</p>`)
    expect(html.match(/type="radio"/g)).toHaveLength(2)
    expect(html).not.toContain('type="checkbox"')
    // 选项的名字是标签、说明是 aria-describedby
    const textOf = (id: string | undefined) => (id ? new RegExp(`id="${id}">([^<]*)<`).exec(html)?.[1] : undefined)
    const named = inputTags(html, 'radio').map((t) => [textOf(/aria-labelledby="([^"]+)"/.exec(t)?.[1]), textOf(/aria-describedby="([^"]+)"/.exec(t)?.[1])])
    expect(named).toEqual([
      ['Apple', '苹果'],
      ['Banana', '香蕉'],
    ])
    // 同一题的单选同名，自己填有可读的名字、限 1000 字
    const names = inputTags(html, 'radio').map((t) => /name="([^"]+)"/.exec(t)?.[1])
    expect(names[0]).toBeTruthy()
    expect(new Set(names).size).toBe(1)
    expect(html).toMatch(/<label class="field"><span>自己填<\/span><input class="input" type="text" aria-describedby="[^"]+" maxLength="1000"/i)
    expect(html).not.toContain('style=')
  })

  it('多选题是 checkbox，并提示可多选；没有选项的题只有自己填', () => {
    const html = form([many, free])
    expect(html.match(/type="checkbox"/g)).toHaveLength(2)
    expect(html).toContain('可多选')
    expect(html).not.toContain('type="radio"')
    expect(html.match(/type="text"/g)).toHaveLength(2)
    expect(html).toContain('还有什么要说的？')
  })

  it('选中的、填了的照状态显示', () => {
    const html = form([fruit, free], st([fruit, free], { picked: [[1], []], custom: ['', '都不喜欢'] }))
    expect(inputTags(html, 'radio').map((t) => [/value="(\d)"/.exec(t)?.[1], t.includes('checked=""')])).toEqual([
      ['0', false],
      ['1', true],
    ])
    expect(html).toContain('value="都不喜欢"')
  })

  it('“回答”在每题都有答案才可点；“让它在终端问我”是次要的链接样式按钮，随时可点', () => {
    const answer = (h: string) => /<button type="button" class="btn primary block"([^>]*)>([^<]*)<\/button>/.exec(h)
    const link = (h: string) => /<button type="button" class="link-btn"([^>]*)>([^<]*)<\/button>/.exec(h)
    const qs = [fruit, free]
    let html = form(qs, st(qs, { picked: [[1], []] }))
    expect(answer(html)?.[1]).toContain('disabled=""')
    expect(answer(html)?.[2]).toBe('回答')
    expect(link(html)?.[1]).not.toContain('disabled')
    expect(link(html)?.[2]).toBe('让它在终端问我')
    html = form(qs, st(qs, { picked: [[1], []], custom: ['', ' 好 '] }))
    expect(answer(html)?.[1]).not.toContain('disabled')
    html = form(qs, st(qs, { custom: ['', '  '] }))
    expect(answer(html)?.[1]).toContain('disabled=""')
  })

  it('多选题时“回答”不可点，旁边小字说还有几题没答；全答了不显示；只有一题也说', () => {
    const qs = [fruit, many, free]
    const hint = (h: string) => /<p class="sub ask-hint" role="status">([^<]*)<\/p>/.exec(h)?.[1]
    expect(hint(form(qs))).toBe('还有 3 题没答')
    expect(hint(form(qs, st(qs, { picked: [[1], [], []], custom: ['', '', ' '] })))).toBe('还有 2 题没答')
    expect(hint(form(qs, st(qs, { picked: [[1], [0], []], custom: ['', '', '无'] })))).toBeUndefined()
    expect(hint(form([fruit]))).toBe('还有 1 题没答')
    expect(hint(form([fruit], st([fruit], { picked: [[0]] })))).toBeUndefined()
  })

  it('“自己填”输入框用 aria-describedby 关联到这题的题面', () => {
    const html = form([fruit, many])
    const inputs = inputTags(html, 'text')
    expect(inputs).toHaveLength(2)
    const texts = inputs.map((t) => {
      const id = /aria-describedby="([^"]+)"/.exec(t)?.[1]
      return id ? new RegExp(`id="${id}">([^<]*)<`).exec(html)?.[1] : undefined
    })
    expect(texts).toEqual(['你更喜欢苹果还是香蕉？', '要哪些？'])
  })

  it('提交中：所有选项、输入框与两个按钮都 disabled，按钮文案说在做什么', () => {
    const qs = [fruit, free]
    const busy = form(qs, st(qs, { picked: [[1], []], custom: ['', 'x'], busy: 'answer' }))
    expect(busy).toContain('正在提交…')
    expect(busy.match(/<(input|button) /g)?.length).toBe(busy.match(/disabled=""/g)?.length)
    const passing = form(qs, st(qs, { busy: 'passthrough' }))
    expect(passing).toContain('正在交回终端…')
    expect(passing.match(/<(input|button) /g)?.length).toBe(passing.match(/disabled=""/g)?.length)
  })

  it('失败时在按钮上方显示 hub 的原文（role=alert），可以改了再试', () => {
    const html = form([fruit], st([fruit], { picked: [[0]], error: '连不上 hub，请检查网络' }))
    expect(html).toContain('<div class="error" role="alert">连不上 hub，请检查网络</div>')
    expect(html.indexOf('role="alert"')).toBeLessThan(html.indexOf('class="btn primary block"'))
    expect(html).not.toMatch(/<button[^>]*disabled/)
  })

  it('题面、选项里的 HTML 与 <redacted> 原样转义显示', () => {
    const html = form([{ question: '<b>token=<redacted></b>？', options: [{ label: '<i>a</i>' }] }])
    expect(html).toContain('&lt;b&gt;token=&lt;redacted&gt;&lt;/b&gt;？')
    expect(html).toContain('&lt;i&gt;a&lt;/i&gt;')
    expect(html).not.toContain('<b>')
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

describe('提问的说明与结果', () => {
  const summary = (r: RequestView) => renderToStaticMarkup(<RequestSummary request={r} />)
  const line = (r: RequestView, audit: AuditEvent[] = []) => renderToStaticMarkup(<StatusLine request={r} audit={audit} />)

  it('待答：说明卡“在问你”、next 三行；没有批准、拒绝、记住的话，也不列答案（题目在答题表单里）', () => {
    const html = summary(ask())
    expect(html).toContain('<p class="headline">Claude Code 在问你</p>')
    expect(html).toContain('<p class="where">项目 blog，机器 mac</p>')
    expect(html).not.toContain('class="why"')
    for (const s of ['选一个答案，agent 立刻继续。', '也可以自己填一句。', '「让它在终端问我」会把问题交回终端。']) expect(html).toContain(`<li>${s}</li>`)
    expect(html).not.toMatch(/批准|拒绝|记住/)
    expect(html).not.toContain('class="answers"')
    expect(renderToStaticMarkup(<RequestContent request={ask()} />)).toBe('')
  })

  it('答了：每题一行“问题 → 你的答案”', () => {
    const r = ask({ state: 'answered', decided_by: 'user', ask: { questions: [fruit, many] }, answers: { '你更喜欢苹果还是香蕉？': 'Banana', '要哪些？': 'x, y' } })
    const html = summary(r)
    expect(html).toContain('<ul class="answers">')
    expect(html).toContain('<li><span class="q">你更喜欢苹果还是香蕉？</span> → <strong class="a">Banana</strong></li>')
    expect(html).toContain('<li><span class="q">要哪些？</span> → <strong class="a">x, y</strong></li>')
    expect(html).not.toContain('class="next"')
  })

  it('交回终端、过期：只列问题，不带箭头', () => {
    const html = summary(ask({ state: 'passthrough', decided_by: 'user' }))
    expect(html).toContain('<li><span class="q">你更喜欢苹果还是香蕉？</span></li>')
    expect(html).not.toContain('→')
  })

  it('含隐藏部分：醒目告警在说明卡之前，提示去终端核对；不提“记住”', () => {
    const html = summary(ask({ has_redacted: true, ask: { questions: [{ question: 'token=<redacted> 还能用吗？', options: [] }] } }))
    expect(html.indexOf('不是原文')).toBeLessThan(html.indexOf('class="headline"'))
    expect(html).toContain('去终端核对原文')
    expect(html).not.toContain('记住')
  })

  it('状态行：已回答（绿）· 你答了 · 时间 · 送达；交回终端 · 交回终端问了；待答不显示', () => {
    expect(line(ask())).toBe('')
    const answered = line(ask({ state: 'answered', decided_by: 'user', decided_at: '2026-10-31T10:01:00.000000000Z', delivered_at: '2026-10-31T10:01:01.000000000Z' }))
    expect(answered).toContain('<span class="tag low">已回答</span>')
    expect(answered).toContain('你答了')
    expect(answered).toContain('已送达 agent')
    const passed = line(ask({ state: 'passthrough', decided_by: 'user', decided_at: '2026-10-31T10:01:00.000000000Z' }))
    expect(passed).toContain('<span class="tag plain">交回终端</span>')
    expect(passed).toContain('交回终端问了')
    expect(passed).not.toContain('送达')
  })
})

describe('StatusLine', () => {
  const line = (r: RequestView, audit: AuditEvent[] = []) => renderToStaticMarkup(<StatusLine request={r} audit={audit} />)

  it('待审批、question 不显示', () => {
    expect(line(req())).toBe('')
    expect(line(req({ kind: 'question', state: 'recorded', title: 'q' }))).toBe('')
  })

  it('已处理：状态标签、一句人话、决定时间、送达，一行', () => {
    const html = line(req({ state: 'approved', decided_by: 'user', decided_at: '2026-10-23T10:01:00.000000000Z', delivered_at: '2026-10-23T10:01:01.000000000Z' }))
    expect(html).toMatch(/^<p class="status-line">/)
    expect(html).toContain('<span class="tag low">已批准</span>')
    expect(html).toContain('你批准了')
    expect(html).not.toContain('手机上决定')
    expect(html).toContain('已送达 agent')
    expect(html).toContain('<time dateTime="2026-10-23T10:01:00.000000000Z">')
    const denied = line(req({ state: 'denied', decided_by: 'user' }), [
      { id: 2, ts: '2026-10-23T10:02:00Z', machine_id: 'm', actor: 'system', request_id: 'r1', action: 'undeliverable', detail: '' },
    ])
    expect(denied).toContain('<span class="tag high">已拒绝</span>')
    expect(denied).toContain('决定未送达')
    const gone = line(req({ state: 'cancelled', decided_by: 'terminal', decided_at: '2026-10-31T08:22:00.000000000Z' }))
    expect(gone).toContain('<span class="tag plain">已取消</span>')
    expect(gone).toContain('agent 那边先结束了，不用你处理')
    expect(gone).not.toContain('终端里处理了')
    // 来源不认识时只有标签与时间，不露原始值
    expect(line(req({ state: 'cancelled', decided_by: 'something_new' }))).not.toContain('something_new')
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

describe('英文（SSR）：组件里没有中文', () => {
  beforeEach(() => setLang('en'))
  afterEach(() => setLang('zh'))
  const cjk = /[　-〿一-鿿＀-￯]/
  // 英文用例用英文的题目、标题；引擎理由是引擎写的中文原文（英文下由 engineReasonLabel 整句换掉）
  const en = (over: Partial<RequestView> = {}) => req({ engine_reason: '无规则命中，按 outward 默认询问', ...over })
  const hook = (over: Partial<RequestView> = {}) =>
    webhook({ engine_reason: '', title: 'Deploy v1.2 to production', payload: { type: 'other', tool: 'webhook', diff_preview: 'Canary 10% first' }, ...over })
  const apple: Question = {
    question: 'Apple or banana?',
    header: 'Fruit',
    options: [{ label: 'Apple', description: 'The red one' }, { label: 'Banana', description: 'The yellow one' }],
  }
  const which: Question = { question: 'Which ones?', options: [{ label: 'x' }, { label: 'y' }], multiSelect: true }
  const other: Question = { question: 'Anything else?', options: null }
  const question = (over: Partial<RequestView> = {}) => ask({ ask: { questions: [apple] }, ...over })

  it('RequestCard：审批、含隐藏部分、webhook、问题、提问', () => {
    const html = card(en({ has_redacted: true }))
    expect(html).toContain('<span class="tag high">Hidden parts</span>')
    expect(html).toContain('Outward')
    for (const h of [
      html,
      card(hook()),
      card(en({ kind: 'question', state: 'recorded', title: 'Delete the old branch?', payload: { type: '' } })),
      card(question({ ask: { questions: [apple, which] } })),
    ]) {
      expect(h).not.toMatch(cjk)
    }
  })

  it('AskFormView：可多选、自己填、还有几题没答、让它在终端问我；提交中的两种文案', () => {
    const qs = [apple, which, other]
    const html = renderToStaticMarkup(<AskFormView questions={qs} state={initAsk(qs)} dispatch={noop as (a: AskAction) => void} onAnswer={noop} onPassthrough={noop} />)
    expect(html).toContain('<span class="sub">Pick one or more</span>')
    expect(html).toContain('<span>Your own answer</span>')
    expect(html).toContain('3 left to answer')
    expect(html).toContain('>Ask me in the terminal instead</button>')
    // “回答”按钮的标签由 Task 8 改（answerLabel），这里先把它剔掉再查
    expect(html.replace('>回答</button>', '></button>')).not.toMatch(cjk)
    const busy = (s: Partial<AskState>) =>
      renderToStaticMarkup(<AskFormView questions={qs} state={{ ...initAsk(qs), ...s }} dispatch={noop as (a: AskAction) => void} onAnswer={noop} onPassthrough={noop} />)
    const answering = busy({ picked: [[0], [1], []], custom: ['', '', 'no'], busy: 'answer' })
    expect(answering).toContain('Submitting…')
    expect(answering).not.toMatch(cjk)
    const passing = busy({ busy: 'passthrough' })
    expect(passing).toContain('Handing back to the terminal…')
    expect(passing.replace('>回答</button>', '></button>')).not.toMatch(cjk)
  })

  it('DecideSheet：标题按动作与载荷、记住开关、取消与确认', () => {
    const sheet = (r: RequestView, action: 'allow' | 'deny') => renderToStaticMarkup(<DecideSheet request={r} action={action} onClose={noop} onDone={noop} />)
    const allow = sheet(en(), 'allow')
    expect(allow).toContain('<h2 id="sheet-title">Approve this command</h2>')
    expect(allow).toContain('Remember: auto-approve similar requests from now on')
    expect(allow).toMatch(/<button type="button" class="btn">Cancel<\/button>/)
    expect(allow).toMatch(/data-testid="confirm"[^>]*>Approve<\/button>/)
    const deny = sheet(en(), 'deny')
    expect(deny).toContain('<h2 id="sheet-title">Deny this command</h2>')
    expect(deny).toContain('Remember: auto-deny similar requests from now on')
    expect(deny).toMatch(/data-testid="confirm"[^>]*>Deny<\/button>/)
    expect(sheet(en({ payload: { type: 'file_write', path: '/p/blog/a.go' } }), 'allow')).toContain('>Approve this file edit</h2>')
    expect(sheet(en({ payload: { type: 'network', url: 'https://x.dev' } }), 'deny')).toContain('>Deny this network access</h2>')
    expect(sheet(en({ payload: { type: 'mcp', server: 'fs', tool: 'write' } }), 'allow')).toContain('>Approve this MCP call</h2>')
    expect(sheet(hook(), 'allow')).toContain('>Approve this request</h2>')
    expect(sheet(question(), 'deny')).toContain('>Deny this action</h2>')
    for (const h of [allow, deny, sheet(hook(), 'deny'), sheet(en({ has_redacted: true }), 'allow'), sheet(question(), 'allow')]) expect(h).not.toMatch(cjk)
  })

  it('RequestSummary：说明卡、含隐藏部分的告警（命令、webhook、提问）', () => {
    const summary = (r: RequestView) => renderToStaticMarkup(<RequestSummary request={r} />)
    const shell = summary(en({ has_redacted: true, payload: { type: 'shell', command: 'curl -H "Authorization: <redacted>" https://x' } }))
    expect(shell).toContain('<section class="explain" aria-label="What this request does">')
    expect(shell).toContain("Parts are hidden; what&#x27;s shown below isn&#x27;t the original")
    expect(shell).toContain('heredoc')
    expect(shell).toContain('Outward')
    expect(shell).toContain('Reason: No rule matched')
    expect(shell).toContain('check the original in the terminal')
    expect(shell).toContain('remembered')
    const w = summary(hook({ has_redacted: true }))
    expect(w).toContain('in the source system')
    expect(w).not.toContain('remembered')
    const q = summary(question({ has_redacted: true }))
    expect(q).toContain('in the terminal')
    expect(q).not.toContain('remembered')
    for (const h of [shell, w, q, summary(en()), summary(question())]) expect(h).not.toMatch(cjk)
  })

  it('RequestContent：文件改动、联网、MCP 的字段名', () => {
    const content = (r: RequestView) => renderToStaticMarkup(<RequestContent request={r} />)
    const fw = content(en({ payload: { type: 'file_write', path: '/p/a.go', diff_preview: '+x' } }))
    expect(fw).toContain('<dt>Path</dt>')
    const nw = content(en({ payload: { type: 'network', method: 'POST', url: 'https://api.example.com' } }))
    expect(nw).toContain('<dt>Request</dt>')
    const mcp = content(en({ payload: { type: 'mcp', server: 'fs', tool: 'write', args: { path: '/a' } } }))
    expect(mcp).toContain('<dt>Server</dt>')
    expect(mcp).toContain('<dt>Tool</dt>')
    for (const h of [fw, nw, mcp, content(hook())]) expect(h).not.toMatch(cjk)
  })

  it('RequestAttrs、RequestDetails、AuditList、StatusLine', () => {
    const audit: AuditEvent[] = [
      { id: 2, ts: '2026-10-23T10:01:00Z', machine_id: 'm', actor: 'user', request_id: 'r1', action: 'allow', detail: '{"note":"","remember":true,"rule_id":"learned-x"}' },
      { id: 1, ts: '2026-10-23T10:00:00Z', machine_id: 'm', actor: 'agent', request_id: 'r1', action: 'request_pending', detail: '{"risk":"network","tool":"Bash"}' },
    ]
    const decided = en({
      state: 'approved', decided_by: 'user', rule_id: 'learned-x', decision_note: 'fine this once',
      decided_at: '2026-10-23T10:01:00.000000000Z', payload: { type: 'shell', command: 'git push', cwd: '/p/blog/sub' },
    })
    const attrs = renderToStaticMarkup(<RequestAttrs request={decided} audit={audit} />)
    for (const label of ['Directory', 'Machine', 'Project', 'Tool', 'Risk', 'Engine', 'Status', 'Decided by', 'Delivery', 'Note', 'Created', 'Decided']) {
      expect(attrs).toContain(`<dt>${label}</dt>`)
    }
    const pending = renderToStaticMarkup(<RequestAttrs request={en({ expires_at: '2026-10-23T10:30:00Z' })} audit={[]} />)
    expect(pending).toContain('<dt>Expires</dt>')
    const details = renderToStaticMarkup(<RequestDetails request={decided} audit={audit} />)
    expect(details).toMatch(/^<details class="more"><summary>Details<\/summary>/)
    expect(details).toContain('Audit log')
    const line = renderToStaticMarkup(<StatusLine request={{ ...decided, delivered_at: '2026-10-23T10:01:01Z' }} audit={audit} />)
    for (const h of [attrs, pending, details, line]) expect(h).not.toMatch(cjk)
  })

  it('ResultToast 与 ConfirmSheet', () => {
    const toast = renderToStaticMarkup(<ResultToast view={{ tone: 'ok', title: 'Done', text: 'Approved; delivered to the agent' }} onClose={noop} />)
    expect(toast).toContain('>Got it</button>')
    expect(toast).not.toMatch(cjk)
    const ask = { title: 'Delete rule user-1?', body: 'This takes effect on every machine right away.', confirmLabel: 'Delete', danger: true }
    const idle = renderToStaticMarkup(<ConfirmSheet state={{ req: ask, busy: false, error: null }} onCancel={noop} onConfirm={noop} />)
    expect(idle).toContain('>Cancel</button>')
    expect(idle).toMatch(/data-testid="confirm"[^>]*>Delete<\/button>/)
    const busy = renderToStaticMarkup(<ConfirmSheet state={{ req: ask, busy: true, error: null }} onCancel={noop} onConfirm={noop} />)
    expect(busy).toContain('Submitting…')
    for (const h of [idle, busy]) expect(h).not.toMatch(cjk)
  })
})
