import { execFile, execFileSync } from 'node:child_process'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, login, test } from './fixtures'
import { childEnv, runBin } from './env'

// v0.3 角色：机器侧还没实现时，用假守护进程（e2e/fakeagent/main.go）按契约接入、报两个角色、发一条越界的审批（会话角色“写作”）。
// 手机上：总台看到“写作”等我 1 → 点进收件箱只看这个角色 → 卡片标出超出边界 → 批准面板没有“记住”与“接下来一段时间都放行”、原位说明
// → 单次批准，假守护进程收到 allow。推送在 e2e 环境里没有推送服务，从请求到达手机开始。

const repoRoot = fileURLToPath(new URL('../../', import.meta.url))
const fakeSrc = fileURLToPath(new URL('./fakeagent/main.go', import.meta.url))

test('越界请求：总台 → 按角色筛选的收件箱 → 批准面板只能单次批准', async ({ page, env }) => {
  // 先登录（login 要等到空的收件箱），再起假守护进程：它的请求先到会让登录的检查落空
  await login(page, env)
  const bin = join(env.base, 'fakeagent')
  execFileSync('go', ['build', '-o', bin, fakeSrc], { cwd: repoRoot, stdio: 'inherit', timeout: 120_000 })
  const tok = /(?:接入码|Enroll token): (\S+)/.exec(runBin(env, ['hub', 'token', '--dir', env.hubDir]))?.[1]
  expect(tok, 'railse hub token 的输出里应有接入码').toBeTruthy()
  const fake = new Promise<string>((res, rej) => {
    const child = execFile(bin, ['--hub', env.baseURL, '--token', tok!, '--name', 'e2e-role'], { timeout: 110_000, env: childEnv(env) }, (err, stdout, stderr) => {
      if (err) rej(new Error(`假守护进程失败: ${err.message}\n${stderr}`))
      else res(stdout)
    })
    child.stdin?.end()
  })
  fake.catch(() => {})

  const nav = page.locator('nav.nav')
  await nav.getByRole('link', { name: '总台', exact: true }).click()
  await expect(page.getByRole('heading', { level: 1, name: '总台', exact: true })).toBeVisible()
  const card = page.locator('.desk-card', { has: page.locator('.what', { hasText: /^写作$/ }) })
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(card.locator('.where')).toHaveText('只改文档')
  await expect(card).toContainText('上限 项目内改动 · 禁用 2 项 · 放行最长 2 小时')
  await expect(card.locator('.stat.hot b')).toHaveText('1', { timeout: 15_000 })
  await expect(page.locator('.desk-card', { has: page.locator('.what', { hasText: /^开发$/ }) })).toBeVisible()

  // 等我 > 0：整张卡链接到按角色筛选的收件箱
  await card.click()
  await expect(page).toHaveURL(/\/inbox\?role=%E5%86%99%E4%BD%9C&machine=/)
  await expect(page.locator('.filter-bar')).toContainText('只看：角色「写作」 · e2e-role')
  const req = page.locator('a.card', { hasText: 'git push origin main' })
  await expect(req).toHaveCount(1)
  await expect(req.locator('.role-line')).toHaveText('超出角色「写作」的边界')
  await expect(nav.getByRole('link', { name: /收件箱/ })).toHaveClass(/active/)

  await req.click()
  await expect(page.locator('dl.kv')).toContainText('写作')
  await page.getByRole('button', { name: '批准', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  await expect(dialog.getByTestId('beyond-note')).toHaveText('超出角色「写作」的边界：这次只能单次批准，不能记住，也不能限时放行。')
  await expect(dialog.getByRole('checkbox')).toHaveCount(0)
  await expect(dialog.getByText('接下来一段时间都放行')).toHaveCount(0)
  const confirm = dialog.getByTestId('confirm')
  await expect(confirm).toHaveText('批准')
  await confirm.click()
  const out = JSON.parse((await fake).trim()) as { machine_id: string; decision: { action: string; decided_by: string } }
  expect(out.decision.action).toBe('allow')
  expect(out.decision.decided_by).toBe('user')
  await expect(page.locator('.status-line .tag')).toHaveText('已批准')

  // 收尾：吊销这台假机器，后面的用例与英文项目只看到 e2e-mac 在线
  const resp = await page.request.delete(`${env.baseURL}/api/machines/${out.machine_id}`)
  expect(resp.status()).toBe(204)
})
