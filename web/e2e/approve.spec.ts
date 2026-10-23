import { test as base, expect, type Page } from '@playwright/test'
import { execFile, type ChildProcess } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { childEnv, readEnv, type E2EEnv } from './env'

const samplePath = fileURLToPath(new URL('../../internal/adapter/testdata/claude/2.1.280/PermissionRequest.json', import.meta.url))

interface HookOut {
  stdout: string
  stderr: string
}

/**
 * 用录制的 Claude PermissionRequest 样本跑一次真实的钩子（railse hook claude --socket …）：改 cwd 与命令。
 * 它会阻塞到手机上做了决定为止，返回钩子退出后的输出。
 */
function runHook(env: E2EEnv, command: string, children: ChildProcess[]): Promise<HookOut> {
  const sample = JSON.parse(readFileSync(samplePath, 'utf8')) as Record<string, unknown>
  sample.cwd = env.repo
  sample.transcript_path = join(env.base, 'transcript.jsonl')
  sample.tool_input = { command }
  const p = new Promise<HookOut>((res, rej) => {
    const child = execFile(env.bin, ['hook', 'claude', '--socket', env.socket], { timeout: 110_000, env: childEnv(env) }, (err, stdout, stderr) => {
      if (err) rej(new Error(`钩子失败: ${err.message}\n${stderr}`))
      else res({ stdout, stderr })
    })
    children.push(child)
    child.stdin?.end(JSON.stringify(sample))
  })
  p.catch(() => {}) // 由用例 await 时再抛；这里只免得提前失败时成为未处理的拒绝
  return p
}

/** 钩子 stdout 里 Claude 认的决定（hookSpecificOutput.decision.behavior）；不作决定时 stdout 为空，回 ''。 */
function behavior(out: HookOut): string {
  const s = out.stdout.trim()
  if (!s) return ''
  const doc = JSON.parse(s) as { hookSpecificOutput?: { decision?: { behavior?: string } } }
  return doc.hookSpecificOutput?.decision?.behavior ?? ''
}

/**
 * 记下页面上的 CSP 违规：页面里的 securitypolicyviolation 事件（经 exposeFunction 送回测试进程，换页也不丢），
 * 与控制台里提到 Content Security Policy 的消息。另记未捕获的页面异常。
 */
async function watchPage(page: Page, csp: string[], errors: string[]) {
  await page.exposeFunction('__railseCsp', (v: string) => {
    csp.push(v)
  })
  await page.addInitScript(() => {
    document.addEventListener(
      'securitypolicyviolation',
      (e) => {
        const report = (window as unknown as { __railseCsp: (v: string) => void }).__railseCsp
        report(`${e.effectiveDirective} 挡住了 ${e.blockedURI || '(inline)'}（${e.sourceFile}:${e.lineNumber}）`)
      },
      true,
    )
  })
  page.on('console', (m) => {
    if (/content.security.policy/i.test(m.text())) csp.push(`控制台: ${m.text()}`)
  })
  page.on('pageerror', (e) => errors.push(e.message))
}

interface Fixtures {
  env: E2EEnv
  /** 跑一次钩子；用例结束时还没退出的钩子进程会被杀掉 */
  hook: (command: string) => Promise<HookOut>
  csp: string[]
}

const test = base.extend<Fixtures>({
  env: async ({}, use) => {
    await use(readEnv())
  },
  hook: async ({ env }, use) => {
    const children: ChildProcess[] = []
    await use((command) => runHook(env, command, children))
    for (const c of children) if (c.exitCode === null && c.signalCode === null) c.kill('SIGKILL')
  },
  // 每个用例都自动带上：结束时页面上不得有任何 CSP 违规或未捕获的异常
  csp: [
    async ({ page }, use) => {
      const csp: string[] = []
      const errors: string[] = []
      await watchPage(page, csp, errors)
      await use(csp)
      expect(csp, '页面上有 CSP 违规').toEqual([])
      expect(errors, '页面上有未捕获的异常').toEqual([])
    },
    { auto: true },
  ],
})

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
  await expect(card.locator('.where')).toHaveText('e2e-mac · repo')
  await expect(nav.getByRole('link', { name: /收件箱/ }).locator('.badge')).toHaveText('1')
  await card.click()
  await expect(page.getByRole('heading', { name: '审批', exact: true })).toBeVisible()
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
  await card2.click()
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
