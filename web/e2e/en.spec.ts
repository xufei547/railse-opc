import { expect, login, test } from './fixtures'
import type { Page } from '@playwright/test'

const cjk = /[　-〿一-鿿＀-￯]/

/** 整页文字里不得有中文；调用前须先等到该页真正的内容，免得在空白加载页上空过 */
async function noCjk(page: Page) {
  await expect(page.locator('body')).not.toContainText(cjk)
}

/**
 * 历史页：卡片里唯一的用户数据是请求摘要（pre.cmd）。两个项目共用一个 hub，
 * 中文用例留下的记录（如中文题目）会出现在这里，不是界面文案。
 * 所以只去掉 .card pre.cmd 的文字，其余整页（标签、位置、时间、结果句、页面框架）照常不得有中文。
 */
async function noCjkChrome(page: Page) {
  const text = await page.evaluate(() => {
    const c = document.body.cloneNode(true) as HTMLElement
    c.querySelectorAll('.card pre.cmd').forEach((el) => {
      el.textContent = ''
    })
    return c.textContent || ''
  })
  expect(text).not.toMatch(cjk)
}

// 英文系统的手机：登录 → 一条审批 → 一道题 → 历史/规则/机器/设置，每一面都不见中文。
// 命令用别的主机（approve.spec 学了 api.example.com 的放行规则，两个项目共用一个 hub，免得被自动放行）。
test('English end to end', async ({ page, env, hook, ask }) => {
  // 登录页（提交前）
  await page.goto(env.baseURL + '/')
  await expect(page.getByLabel('Password')).toBeVisible()
  await noCjk(page)
  await login(page, env, 'en')
  await expect(page.locator('html')).toHaveAttribute('lang', 'en')
  await noCjk(page)

  // 审批
  const h = hook('curl https://en.example.net/ping')
  const card = page.locator('.card', { hasText: 'curl https://en.example.net/ping' })
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(card.locator('.tag')).toHaveText('Network')
  await noCjk(page)
  await card.click()
  await expect(page.getByRole('button', { name: 'Approve', exact: true })).toBeVisible()
  await expect(page.locator('section.explain .headline')).toBeVisible()
  await noCjk(page)
  await page.getByRole('button', { name: 'Approve', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  await noCjk(page)
  await dialog.getByTestId('confirm').click()
  await expect(page.locator('.toast')).toContainText('Approved')
  expect(JSON.parse((await h).stdout).hookSpecificOutput.decision.behavior).toBe('allow')
  await noCjk(page)

  // 答题
  const a = ask(JSON.stringify({ questions: [{ question: 'Apple or banana?', header: 'Fruit', options: [{ label: 'Apple' }, { label: 'Banana' }], multiSelect: false }] }))
  await page.goto(env.baseURL + '/')
  const q = page.locator('.card', { hasText: 'Apple or banana?' })
  await expect(q).toBeVisible({ timeout: 15_000 })
  await q.click()
  const group = page.getByRole('group', { name: 'Apple or banana?' })
  await expect(group).toBeVisible()
  await expect(page.getByRole('button', { name: 'Answer', exact: true })).toBeDisabled()
  await noCjk(page)
  await group.getByRole('radio', { name: 'Banana', exact: true }).check()
  const answer = page.getByRole('button', { name: 'Answer: Banana' })
  await expect(answer).toBeEnabled()
  await answer.click()
  await expect(page.getByText('You answered')).toBeVisible()
  expect(JSON.parse((await a).stdout).hookSpecificOutput.updatedInput.answers).toEqual({ 'Apple or banana?': 'Banana' })
  await noCjk(page)

  // 其余各页：先等到页面标题
  for (const [path, title] of [
    ['/history', 'History'],
    ['/rules', 'Rules'],
    ['/machines', 'Machines'],
    ['/settings', 'Settings'],
  ] as const) {
    await page.goto(env.baseURL + path)
    await expect(page.getByRole('heading', { level: 1, name: title, exact: true })).toBeVisible()
    if (path === '/history') await expect(page.locator('.card').first()).toBeVisible()
    // 两条内置规则永远在（与是否先跑过中文用例无关），等到内置卡片即规则列表已加载
    if (path === '/rules') await expect(page.locator('.card.rule', { hasText: 'Built-in' }).first()).toBeVisible()
    if (path === '/machines') await expect(page.locator('.list-item', { hasText: 'e2e-mac' })).toBeVisible()
    if (path === '/settings') {
      await expect(page.getByText('Language').first()).toBeVisible()
      // 异步块都加载完：推送状态有文字、hub 规则版本不再是 '-'
      await expect(page.locator('.push-state')).not.toBeEmpty()
      await expect(page.locator('dl.kv dd').first()).not.toHaveText('-')
    }
    if (path === '/history') {
      await noCjkChrome(page)
      // 本用例自己的两条记录：完全不剥离，整张卡片的文字都不得有中文
      for (const needle of ['en.example.net', 'Apple or banana?']) {
        const own = page.locator('.card', { hasText: needle })
        await expect(own.first()).toBeVisible()
        for (const el of await own.all()) await expect(el).not.toContainText(cjk)
      }
    }
    else await noCjk(page)
  }
})
