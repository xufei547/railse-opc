import { test as base, type Page } from '@playwright/test'
import { behavior, expect, test, watchPage } from './fixtures'
import { readEnv, type E2EEnv } from './env'

async function login(page: Page, env: E2EEnv) {
  const resp = await page.goto(env.baseURL + '/')
  // 网页由 hub 同源托管，HTML 带 CSP：下面“没有违规”的断言才有意义
  expect(resp?.headers()['content-security-policy']).toContain("script-src 'self'")
  await expect(page).toHaveURL(/\/login/)
  await page.getByLabel('密码').fill(env.password)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByText('没有等你处理的事')).toBeVisible()
}

test('登录、看到请求、批准并记住、规则多一条；再拒绝一条', async ({ page, env, hook }) => {
  await login(page, env)
  const nav = page.locator('nav.nav')
  const toast = page.locator('.toast')
  const dialog = page.getByRole('dialog')

  // 1. 联网命令：引擎定不了（network 要问），进收件箱；批准并记住
  const hook1 = hook('curl https://api.example.com/v1/x')
  const card = page.locator('.card', { hasText: 'curl https://api.example.com/v1/x' })
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(card.locator('.tag')).toHaveText('联网')
  await expect(card.locator('.where')).toHaveText('Claude Code · e2e-mac · repo')
  await expect(card.locator('.what')).toHaveText('访问网址 api.example.com')
  await expect(nav.getByRole('link', { name: /收件箱/ }).locator('.badge')).toHaveText('1')
  await card.click()
  await expect(page.getByRole('heading', { name: '审批', exact: true })).toBeVisible()
  const explain = page.locator('section.explain')
  await expect(explain.locator('.headline')).toHaveText('Claude Code 想访问网址 api.example.com')
  await expect(explain.locator('.where')).toHaveText('项目 repo，机器 e2e-mac')
  await expect(explain.locator('.why')).toHaveText('这条要联网。规则默认不自动放行联网操作，要你决定。')
  await expect(page.locator('ul.next li')).toHaveCount(3)
  await expect(page.locator('details.more')).not.toHaveAttribute('open')
  await expect(page.locator('details.more summary')).toHaveText('详情')
  await page.getByRole('button', { name: '批准', exact: true }).click()
  await expect(dialog).toBeVisible()
  await dialog.getByRole('checkbox', { name: /记住/ }).check()
  // 草案：将放行命令 <锚点>（仅项目 repo）；锚点是命令前缀或转义过的正则
  await expect(dialog.locator('.draft-summary')).toContainText('将放行')
  await expect(dialog.locator('.draft-summary')).toContainText('仅项目 repo')
  await expect(dialog.locator('.draft-summary code')).toHaveText(/api\\?\.example\\?\.com/)
  const confirm1 = dialog.getByTestId('confirm')
  await expect(confirm1).toHaveText('批准并记住')
  await confirm1.click()
  const out1 = await hook1
  expect(behavior(out1), `钩子输出: ${out1.stdout}${out1.stderr}`).toBe('allow')
  expect(out1.stdout).toContain('"behavior":"allow"')
  await expect(dialog).toHaveCount(0)
  await expect(toast).toHaveClass(/\bok\b/)
  await expect(toast).toContainText('已批准，并记住为规则')
  await expect(page.locator('.status-line .tag')).toHaveText('已批准')
  await expect(page.locator('ul.next')).toHaveCount(0)
  const learnedID = /并记住为规则 (\S+?)；/.exec((await toast.textContent()) ?? '')?.[1]
  expect(learnedID, '结果提示里应有学到的规则 id').toBeTruthy()

  // 规则页：两条内置之外恰好多一条学来的，就是刚才那条
  await nav.getByRole('link', { name: '规则', exact: true }).click()
  await expect(page.getByRole('heading', { name: '规则', exact: true })).toBeVisible()
  const rules = page.locator('.card.rule')
  await expect(rules).toHaveCount(3)
  const learned = rules.filter({ has: page.locator('.tag', { hasText: '学来的' }) })
  await expect(learned).toHaveCount(1)
  await expect(learned.locator('.rule-id')).toHaveText(learnedID!)
  await expect(rules.filter({ has: page.locator('.tag', { hasText: '内置' }) })).toHaveCount(2)

  // 2. 对外命令：拒绝（带备注，备注只进审计，不给 agent）
  await nav.getByRole('link', { name: /收件箱/ }).click()
  await expect(page.getByText('没有等你处理的事')).toBeVisible()
  const hook2 = hook('git push origin main')
  const card2 = page.locator('.card', { hasText: 'git push origin main' })
  await expect(card2).toBeVisible({ timeout: 15_000 })
  await expect(card2.locator('.tag')).toHaveText('对外')
  await expect(card2.locator('.what')).toHaveText('把代码推到远程仓库（origin 的 main）')
  await card2.click()
  await expect(page.locator('section.explain .headline')).toHaveText('Claude Code 想把代码推到远程仓库（origin 的 main）')
  await page.getByRole('button', { name: '拒绝', exact: true }).click()
  await expect(dialog).toBeVisible()
  await dialog.getByLabel(/备注/).fill('先别推')
  const confirm2 = dialog.getByTestId('confirm')
  await expect(confirm2).toHaveText('拒绝')
  await confirm2.click()
  const out2 = await hook2
  expect(behavior(out2), `钩子输出: ${out2.stdout}${out2.stderr}`).toBe('deny')
  expect(out2.stdout).toContain('"behavior":"deny"')
  expect(out2.stdout).not.toContain('先别推')
  await expect(dialog).toHaveCount(0)
  await expect(toast).toHaveClass(/\bok\b/)
  await expect(toast).toContainText('已拒绝')
  await expect(page.locator('.status-line .tag')).toHaveText('已拒绝')

  // 历史：这条是“已拒绝”，依据“手机上决定”
  await nav.getByRole('link', { name: '历史', exact: true }).click()
  const hist = page.locator('.card', { hasText: 'git push origin main' })
  await expect(hist).toBeVisible()
  await expect(hist.locator('.tag')).toHaveText('已拒绝')
  await expect(hist.locator('.sub')).toHaveText('手机上决定')
  const hist1 = page.locator('.card', { hasText: 'curl https://api.example.com/v1/x' })
  await expect(hist1.locator('.tag')).toHaveText('已批准')
  await expect(hist1.locator('.sub')).toHaveText(`手机上决定（并记住为 ${learnedID}）`)
  await expect(nav.getByRole('link', { name: /收件箱/ }).locator('.badge')).toHaveCount(0)

  // 规则仍只有一条学来的：拒绝没有勾“记住”
  await nav.getByRole('link', { name: '规则', exact: true }).click()
  await expect(page.locator('.card.rule')).toHaveCount(3)
})

// 检测器自检：故意塞一段内联脚本（CSP 不许），确认上面的“没有违规”断言不是空转
base('CSP 违规会被记下（检测器自检）', async ({ page }) => {
  const env = readEnv()
  const csp: string[] = []
  await watchPage(page, csp, [])
  await page.goto(env.baseURL + '/login')
  await expect(page.getByRole('button', { name: '登录', exact: true })).toBeVisible()
  expect(csp).toEqual([])
  await page.evaluate(() => {
    const s = document.createElement('script')
    s.textContent = 'window.name = "inline"'
    document.head.append(s)
  })
  await expect.poll(() => csp.length).toBeGreaterThan(0)
  expect(csp.join('\n')).toContain('script-src')
})
