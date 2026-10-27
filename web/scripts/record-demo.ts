// README 里的演示：对着真实的 hub、守护进程与钩子走一遍“登录 → 请求送到手机 → 批准并记住 → 规则多一条”，录成视频。
// 只由 record-demo.config.ts 收进来（make demo → scripts/demo.sh），make e2e 不跑它；它也不是 vitest 的测试。
// 步骤之间停顿，给看 gif 的人留出读的时间。字幕用 page.evaluate 插进页面，样式走 CSSOM（不写 style 属性，不触发 CSP）；
// 夹具照端到端的要求在结束时检查页面上没有 CSP 违规与未捕获的异常。

import { writeFileSync } from 'node:fs'
import type { Page } from '@playwright/test'
import { behavior, expect, test } from '../e2e/fixtures'

const command = 'curl https://api.example.com/v1/x'

/**
 * 页面顶部一行字幕：第一次调用时插进 body，并给 body 留出同样高的上边距，不挡住页面的标题栏；之后只换文字。
 * React 只管 #root，前端换页时它留着。回字幕是否一行放得下（放不下会被截断）。
 */
async function caption(page: Page, text: string): Promise<boolean> {
  return page.evaluate((t) => {
    const id = 'railse-demo-caption'
    let el = document.getElementById(id)
    if (!el) {
      el = document.createElement('div')
      el.id = id
      el.setAttribute('aria-hidden', 'true')
      const s = el.style
      s.position = 'fixed'
      s.left = '0'
      s.right = '0'
      s.top = '0'
      s.zIndex = '100'
      s.boxSizing = 'border-box'
      s.height = '44px'
      s.padding = '0 12px'
      s.background = '#1f2a44'
      s.color = '#ffffff'
      s.font = '600 15px/44px -apple-system, BlinkMacSystemFont, "PingFang SC", sans-serif'
      s.textAlign = 'center'
      s.whiteSpace = 'nowrap'
      s.overflow = 'hidden'
      s.textOverflow = 'ellipsis'
      s.pointerEvents = 'none'
      document.body.append(el)
      document.body.style.paddingTop = '44px'
    }
    el.textContent = t
    return el.scrollWidth <= el.clientWidth
  }, text)
}

async function say(page: Page, text: string) {
  expect(await caption(page, text), `字幕一行放不下: ${text}`).toBe(true)
}

test('演示：登录、请求送到手机、批准并记住、规则多一条', async ({ page, env, hook }) => {
  const pause = (ms: number) => page.waitForTimeout(ms)
  const nav = page.locator('nav.nav')
  const dialog = page.getByRole('dialog')

  // 录像在开页之前就开始了，开头是空白帧：记下登录页与第一条字幕都出现了的时刻，scripts/demo.sh 从这里截起
  const t0 = Date.now()
  await page.goto(env.baseURL + '/')
  const password = page.getByLabel('密码')
  await expect(password).toBeVisible()
  await say(page, '手机上打开 hub 托管的网页，登录')
  await pause(200)
  const start = Date.now()

  await pause(1500)
  await password.pressSequentially(env.password, { delay: 40 })
  await pause(500)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page.getByText('没有等你处理的事')).toBeVisible()
  await expect(page.getByRole('img', { name: '实时连接正常' })).toBeVisible()
  await pause(2500)

  // 真实的钩子：喂录制的 Claude Code PermissionRequest，引擎定不了（联网要问），经守护进程送到 hub
  const hooked = hook(command)
  await say(page, 'Claude Code 要跑 curl，规则定不了，送到手机')
  const card = page.locator('.card', { hasText: command })
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(nav.getByRole('link', { name: /收件箱/ }).locator('.badge')).toHaveText('1')
  await pause(3500)

  await card.click()
  await expect(page.getByRole('heading', { name: '审批', exact: true })).toBeVisible()
  await pause(3000)
  await page.getByRole('button', { name: '批准', exact: true }).click()
  await expect(dialog).toBeVisible()
  await pause(1200)
  await say(page, '批准时勾上「记住」，存成一条规则')
  await dialog.getByRole('checkbox', { name: /记住/ }).check()
  await expect(dialog.locator('.draft-summary')).toContainText('将放行')
  await pause(3500)
  await dialog.getByTestId('confirm').click()
  const out = await hooked
  expect(behavior(out), `钩子输出: ${out.stdout}${out.stderr}`).toBe('allow')
  await expect(page.locator('.toast')).toContainText('已批准，并记住为规则')
  await pause(3500)

  await nav.getByRole('link', { name: '规则', exact: true }).click()
  const learned = page.locator('.card.rule').filter({ has: page.locator('.tag', { hasText: '学来的' }) })
  await expect(learned).toHaveCount(1)
  await say(page, '规则页多了一条「学来的」，同类请求以后自动放行')
  await pause(5000)

  const end = Date.now()
  expect(end - start, '演示超过 60 秒').toBeLessThan(60_000)
  // 截取的起点与时长（秒），scripts/demo.sh 读
  writeFileSync(test.info().outputPath('trim.txt'), `${((start - t0) / 1000).toFixed(2)} ${((end - start) / 1000).toFixed(2)}\n`)
})
