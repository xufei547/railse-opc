// README 里的演示：对着真实的 hub、守护进程与钩子走一遍“登录 → 请求送到手机 → 批准并记住 → 手机上答一道题 → 规则多一条”，录成视频。
// 只由 record-demo.config.ts 收进来（make demo → scripts/demo.sh），make e2e 不跑它；它也不是 vitest 的测试。
// 中英两版同一条脚本：RAILSE_DEMO_LANG=en 时页面是英文（config 的 locale 跟着变），字幕与选择器从 texts 表取。
// 步骤之间停顿，给看 gif 的人留出读的时间。字幕用 page.evaluate 插进页面，样式走 CSSOM（不写 style 属性，不触发 CSP）；
// 夹具照端到端的要求在结束时检查页面上没有 CSP 违规与未捕获的异常。

import { writeFileSync } from 'node:fs'
import type { Page } from '@playwright/test'
import { behavior, expect, test } from '../e2e/fixtures'

const command = 'curl https://api.example.com/v1/x'
const lang = process.env.RAILSE_DEMO_LANG === 'en' ? 'en' : 'zh'

const texts = {
  zh: {
    password: '密码',
    login: '登录',
    empty: '没有等你处理的事',
    live: '实时连接正常',
    inbox: /收件箱/,
    approvalHeading: '审批',
    approve: '批准',
    remember: /记住/,
    willAllow: '将放行',
    approvedToast: '已批准',
    question: '苹果还是香蕉？',
    answerDisabled: '回答',
    answerBanana: '回答：Banana',
    answered: '你答了',
    rules: '规则',
    learned: '学来的',
    cap1: '手机上打开 hub 托管的网页，登录',
    cap2: 'Claude Code 要跑 curl，规则定不了，送到手机',
    cap3: '批准时勾上「记住」，存成一条规则',
    cap4: 'Claude Code 问选择题，也到手机上答',
    cap5: '规则页多了一条「学来的」，同类请求以后自动放行',
  },
  en: {
    password: 'Password',
    login: 'Log in',
    empty: 'Nothing waiting for you',
    live: 'Live updates connected',
    inbox: /Inbox/,
    approvalHeading: 'Approval',
    approve: 'Approve',
    remember: /Remember/,
    willAllow: 'Will allow',
    approvedToast: 'Approved',
    question: 'Apple or banana?',
    answerDisabled: 'Answer',
    answerBanana: 'Answer: Banana',
    answered: 'You answered',
    rules: 'Rules',
    learned: 'Learned',
    cap1: 'Open the hub’s web app on your phone, log in',
    cap2: 'curl: no rule decides it, so it goes to your phone',
    cap3: 'Approve with “Remember” on: it becomes a rule',
    cap4: 'Claude Code’s questions come to the phone too',
    cap5: 'New “Learned” rule: similar requests auto-allow',
  },
}[lang]

const askInput = JSON.stringify({
  questions: [
    {
      question: texts.question,
      header: lang === 'en' ? 'Fruit' : '水果',
      options: [
        { label: 'Apple', description: lang === 'en' ? 'the red one' : '苹果' },
        { label: 'Banana', description: lang === 'en' ? 'the yellow one' : '香蕉' },
      ],
      multiSelect: false,
    },
  ],
})

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
      // 英文字幕字号小一号：同样的意思英文更长
      s.font = (document.documentElement.lang.startsWith('zh') ? '600 15px/44px' : '600 14px/44px') + ' -apple-system, BlinkMacSystemFont, "PingFang SC", sans-serif'
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

test('演示：登录、请求送到手机、批准并记住、手机上答题、规则多一条', async ({ page, env, hook, ask }) => {
  const pause = (ms: number) => page.waitForTimeout(ms)
  const nav = page.locator('nav.nav')
  const dialog = page.getByRole('dialog')

  // 录像在开页之前就开始了，开头是空白帧：记下登录页与第一条字幕都出现了的时刻，scripts/demo.sh 从这里截起
  const t0 = Date.now()
  await page.goto(env.baseURL + '/')
  const password = page.getByLabel(texts.password)
  await expect(password).toBeVisible()
  await say(page, texts.cap1)
  await pause(200)
  const start = Date.now()

  await pause(1500)
  await password.pressSequentially(env.password, { delay: 40 })
  await pause(500)
  await page.getByRole('button', { name: texts.login, exact: true }).click()
  await expect(page.getByText(texts.empty)).toBeVisible()
  await expect(page.getByRole('img', { name: texts.live })).toBeVisible()
  await pause(2000)

  // 真实的钩子：喂录制的 Claude Code PermissionRequest，引擎定不了（联网要问），经守护进程送到 hub
  const hooked = hook(command)
  await say(page, texts.cap2)
  const card = page.locator('.card', { hasText: command })
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(nav.getByRole('link', { name: texts.inbox }).locator('.badge')).toHaveText('1')
  await pause(3000)

  await card.click()
  await expect(page.getByRole('heading', { name: texts.approvalHeading, exact: true })).toBeVisible()
  await pause(2500)
  await page.getByRole('button', { name: texts.approve, exact: true }).click()
  await expect(dialog).toBeVisible()
  await pause(1000)
  await say(page, texts.cap3)
  await dialog.getByRole('checkbox', { name: texts.remember }).check()
  await expect(dialog.locator('.draft-summary')).toContainText(texts.willAllow)
  await pause(3000)
  await dialog.getByTestId('confirm').click()
  const out = await hooked
  expect(behavior(out), `钩子输出: ${out.stdout}${out.stderr}`).toBe('allow')
  await expect(page.locator('.toast')).toContainText(texts.approvedToast)
  await pause(2500)

  // 真实的 PreToolUse/AskUserQuestion 钩子：题目直接到手机，选一个答案回去
  const asked = ask(askInput)
  await say(page, texts.cap4)
  await nav.getByRole('link', { name: texts.inbox }).click()
  const q = page.locator('.card', { hasText: texts.question })
  await expect(q).toBeVisible({ timeout: 15_000 })
  await pause(2000)
  await q.click()
  await expect(page.getByRole('button', { name: texts.answerDisabled, exact: true })).toBeDisabled()
  await pause(1500)
  await page.getByRole('radio', { name: 'Banana', exact: true }).check()
  const answer = page.getByRole('button', { name: texts.answerBanana })
  await expect(answer).toBeEnabled()
  await pause(1200)
  await answer.click()
  const askOut = await asked
  expect(JSON.parse(askOut.stdout).hookSpecificOutput.updatedInput.answers).toEqual({ [texts.question]: 'Banana' })
  await expect(page.getByText(texts.answered)).toBeVisible()
  await pause(2500)

  await nav.getByRole('link', { name: texts.rules, exact: true }).click()
  const learned = page.locator('.card.rule').filter({ has: page.locator('.tag', { hasText: texts.learned }) })
  await expect(learned).toHaveCount(1)
  await say(page, texts.cap5)
  await pause(4500)

  const end = Date.now()
  expect(end - start, '演示超过 60 秒').toBeLessThan(60_000)
  // 截取的起点与时长（秒），scripts/demo.sh 读
  writeFileSync(test.info().outputPath('trim.txt'), `${((start - t0) / 1000).toFixed(2)} ${((end - start) / 1000).toFixed(2)}\n`)
})
