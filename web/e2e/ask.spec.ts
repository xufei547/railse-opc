import { expect, login, rawAt, test, type HookOut } from './fixtures'

// 2026-10-31 在 Claude Code 2.1.280 真机抓到的 AskUserQuestion 的 tool_input（与 internal/adapter/claude_ask_test.go 同一份）
const fruitQuestions = `[{"question":"你更喜欢苹果还是香蕉？","header":"水果","options":[{"label":"Apple","description":"苹果"},{"label":"Banana","description":"香蕉"}],"multiSelect":false}]`
const fruitInput = `{"questions":${fruitQuestions}}`

/**
 * 钩子答了：stdout 是一行 PreToolUse 的 allow；updatedInput 只比 tool_input 多 answers，且 questions 逐字节是输入的原文
 * （从输出文本里截原始片段比对，不只是解析后深等）。
 */
function expectAnswered(out: HookOut, rawQuestions: string, answers: Record<string, string>) {
  const why = `钩子输出: ${out.stdout}${out.stderr}`
  expect(out.stdout.endsWith('\n'), why).toBe(true)
  const line = out.stdout.slice(0, -1)
  expect(line, why).not.toContain('\n')
  const doc = JSON.parse(line) as {
    hookSpecificOutput?: { hookEventName?: string; permissionDecision?: string; updatedInput?: Record<string, unknown> }
  }
  const hso = doc.hookSpecificOutput
  expect(hso?.hookEventName, why).toBe('PreToolUse')
  expect(hso?.permissionDecision, why).toBe('allow')
  expect(Object.keys(hso?.updatedInput ?? {}).sort(), why).toEqual(['answers', 'questions'])
  expect(hso?.updatedInput?.answers, why).toEqual(answers)
  expect(rawAt(line, ['hookSpecificOutput', 'updatedInput', 'questions']), why).toBe(rawQuestions)
}

test('提问：收件箱出现“在问你”，选 Banana 回答，钩子带着答案放行、题目逐字节原样', async ({ page, env, ask }) => {
  await login(page, env)
  const nav = page.locator('nav.nav')
  const inbox = nav.getByRole('link', { name: /收件箱/ })

  const hooked = ask(fruitInput)
  const card = page.locator('.card', { hasText: '你更喜欢苹果还是香蕉？' })
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(card.locator('.what')).toHaveText('在问你：你更喜欢苹果还是香蕉？')
  await expect(card.locator('.tag')).toHaveText('提问')
  await expect(card.locator('.cmd')).toHaveText('Apple / Banana')
  await expect(card.locator('.where')).toHaveText('Claude Code · e2e-mac · repo')
  await expect(inbox.locator('.badge')).toHaveText('1')
  await expect(page.getByRole('heading', { name: '待处理', exact: true })).toBeVisible()
  await expect(page.getByRole('heading', { name: '待审批' })).toHaveCount(0)

  await card.click()
  await expect(page.getByRole('heading', { name: '提问', exact: true })).toBeVisible()
  await expect(page.locator('header.topbar .tag')).toHaveCount(0)
  const explain = page.locator('section.explain')
  await expect(explain.locator('.headline')).toHaveText('Claude Code 在问你')
  await expect(explain.locator('.where')).toHaveText('项目 repo，机器 e2e-mac')
  await expect(page.locator('ul.next li')).toHaveText(['选一个答案，agent 立刻继续。', '也可以自己填一句。', '「让它在终端问我」会把问题交回终端。'])
  await expect(page.getByRole('button', { name: /批准|拒绝/ })).toHaveCount(0)
  const group = page.getByRole('group', { name: '你更喜欢苹果还是香蕉？' })
  await expect(group.getByText('水果', { exact: true })).toBeVisible()
  const answer = page.getByRole('button', { name: '回答', exact: true })
  await expect(answer).toBeDisabled()
  await expect(page.getByText('还有 1 题没答')).toBeVisible()
  await expect(page.getByRole('textbox', { name: '自己填' })).toHaveAccessibleDescription('你更喜欢苹果还是香蕉？')
  await group.getByRole('radio', { name: 'Banana', exact: true }).check()
  await expect(page.getByText(/题没答/)).toHaveCount(0)
  await expect(answer).toBeEnabled()
  await answer.click()

  const out = await hooked
  expectAnswered(out, fruitQuestions, { '你更喜欢苹果还是香蕉？': 'Banana' })
  await expect(page.locator('.status-line .tag')).toHaveText('已回答')
  await expect(page.locator('.status-line')).toContainText('你答了')
  await expect(page.locator('.status-line')).toContainText('已送达 agent')
  await expect(page.locator('ul.answers li')).toHaveText(['你更喜欢苹果还是香蕉？ → Banana'])
  await expect(page.getByRole('radio')).toHaveCount(0)
  const toast = page.locator('.toast')
  await expect(toast).toHaveClass(/\bok\b/)
  await expect(toast).toContainText('已回答')

  // 收件箱空了、角标没了；历史里这条是“已回答”，结果一句“你答了”
  await inbox.click()
  await expect(page.getByText('没有等你处理的事')).toBeVisible()
  await expect(inbox.locator('.badge')).toHaveCount(0)
  await nav.getByRole('link', { name: '历史', exact: true }).click()
  const hist = page.locator('.card', { hasText: '你更喜欢苹果还是香蕉？' }).first()
  await expect(hist.locator('.tag')).toHaveText('已回答')
  await expect(hist.locator('.sub')).toHaveText('你答了')
})

test('提问：点“让它在终端问我”，钩子不输出（Claude Code 在终端照常问）', async ({ page, env, ask }) => {
  await login(page, env)
  const hooked = ask(fruitInput)
  const card = page.locator('.card', { hasText: '你更喜欢苹果还是香蕉？' })
  await expect(card).toBeVisible({ timeout: 15_000 })
  await card.click()
  await expect(page.getByRole('heading', { name: '提问', exact: true })).toBeVisible()
  await page.getByRole('button', { name: '让它在终端问我', exact: true }).click()

  const out = await hooked
  expect(out.stdout, `钩子输出: ${out.stdout}${out.stderr}`).toBe('')
  await expect(page.locator('.status-line .tag')).toHaveText('交回终端')
  await expect(page.locator('.status-line')).toContainText('交回终端问了')
  await expect(page.locator('ul.answers li')).toHaveText(['你更喜欢苹果还是香蕉？'])
  await expect(page.getByRole('button', { name: '回答', exact: true })).toHaveCount(0)
  const toast = page.locator('.toast')
  await expect(toast).toHaveClass(/\bok\b/)
  await expect(toast).toContainText('已交回终端')
  await expect(page.locator('nav.nav').getByRole('link', { name: /收件箱/ }).locator('.badge')).toHaveCount(0)
})

// 多选（按选项顺序用“, ”连接）、自己填（清掉已选的选项，首尾空格原样给 Claude）、没有选项的题；
// questions 里故意带空格与 \u 转义：钩子回填的仍须逐字节是原文（Claude 发的是单行 JSON，这里也不带换行，钩子输出仍是一行）
const mixedQuestions = `[ {"question": "要哪些水果？", "options": [{"label": "Apple"}, {"label": "Banana"}, {"label": "Ch\\u0065rry"}], "multiSelect": true}, ` +
  `{"question":"几点送？","header":"时间","options":[{"label":"上午"},{"label":"下午"}]}, {"question":"还有什么要说的？","options":[]} ]`

test('提问：多选、自己填、没有选项的题，答案原样送到钩子', async ({ page, env, ask }) => {
  await login(page, env)
  const hooked = ask(`{"questions": ${mixedQuestions}}`)
  const card = page.locator('.card', { hasText: '要哪些水果？' })
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(card.locator('.cmd')).toHaveText('Apple / Banana / Cherry')
  await card.click()

  const answer = page.getByRole('button', { name: '回答', exact: true })
  const fruits = page.getByRole('group', { name: '要哪些水果？' })
  await expect(fruits.getByText('可多选')).toBeVisible()
  await fruits.getByRole('checkbox', { name: 'Cherry', exact: true }).check()
  await fruits.getByRole('checkbox', { name: 'Apple', exact: true }).check()

  const when = page.getByRole('group', { name: '几点送？' })
  const morning = when.getByRole('radio', { name: '上午', exact: true })
  await morning.check()
  await when.getByRole('textbox', { name: '自己填' }).fill('  晚上八点  ')
  await expect(morning).not.toBeChecked()

  const note = page.getByRole('group', { name: '还有什么要说的？' })
  await expect(note.getByRole('radio')).toHaveCount(0)
  await expect(answer).toBeDisabled()
  await note.getByRole('textbox', { name: '自己填' }).fill('无')
  await expect(answer).toBeEnabled()
  await answer.click()

  const out = await hooked
  expectAnswered(out, mixedQuestions, { '要哪些水果？': 'Apple, Cherry', '几点送？': '  晚上八点  ', '还有什么要说的？': '无' })
  await expect(page.locator('.status-line')).toContainText('你答了')
  await expect(page.locator('ul.answers li')).toHaveText(['要哪些水果？ → Apple, Cherry', '几点送？ →   晚上八点  ', '还有什么要说的？ → 无'])
})
