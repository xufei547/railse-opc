// 端到端用例与演示录制（web/scripts/record-demo.ts）共用的夹具：跑真实钩子、盯住页面上的 CSP 违规与未捕获的异常。
// 文件名不带 .spec：playwright 不会把它当用例收进去。

import { test as base, expect, type Page } from '@playwright/test'
import { execFile, type ChildProcess } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { childEnv, readEnv, type E2EEnv } from './env'

const sampleDir = new URL('../../internal/adapter/testdata/claude/2.1.280/', import.meta.url)
const samplePath = fileURLToPath(new URL('PermissionRequest.json', sampleDir))
const preToolUsePath = fileURLToPath(new URL('PreToolUse.json', sampleDir))

export interface HookOut {
  stdout: string
  stderr: string
}

/** 跑一次真实的钩子（railse hook claude --socket …），stdin 原样喂 input。它会阻塞到手机上有了结果为止，返回钩子退出后的输出。 */
function execHook(env: E2EEnv, input: string, children: ChildProcess[]): Promise<HookOut> {
  const p = new Promise<HookOut>((res, rej) => {
    const child = execFile(env.bin, ['hook', 'claude', '--socket', env.socket], { timeout: 110_000, env: childEnv(env) }, (err, stdout, stderr) => {
      if (err) rej(new Error(`钩子失败: ${err.message}\n${stderr}`))
      else res({ stdout, stderr })
    })
    children.push(child)
    child.stdin?.end(input)
  })
  p.catch(() => {}) // 由用例 await 时再抛；这里只免得提前失败时成为未处理的拒绝
  return p
}

/** 用录制的 Claude PermissionRequest 样本跑一次钩子：改 cwd 与命令。 */
function runHook(env: E2EEnv, command: string, children: ChildProcess[]): Promise<HookOut> {
  const sample = JSON.parse(readFileSync(samplePath, 'utf8')) as Record<string, unknown>
  sample.cwd = env.repo
  sample.transcript_path = join(env.base, 'transcript.jsonl')
  sample.tool_input = { command }
  return execHook(env, JSON.stringify(sample), children)
}

/**
 * 用录制的 Claude PreToolUse 样本跑一次提问（AskUserQuestion）的钩子：cwd 是临时项目，会话 id 与 prompt_id 每次新的
 * （互不相干的提问不会被当成同一轮重发），tool_input 是传入的 JSON 原文，原样拼进 stdin（不经 JSON.stringify 重排）。
 */
function runAskHook(env: E2EEnv, toolInput: string, children: ChildProcess[]): Promise<HookOut> {
  const sample = JSON.parse(readFileSync(preToolUsePath, 'utf8')) as Record<string, unknown>
  sample.session_id = randomUUID()
  sample.prompt_id = randomUUID()
  sample.cwd = env.repo
  sample.transcript_path = join(env.base, 'transcript.jsonl')
  sample.tool_name = 'AskUserQuestion'
  sample.tool_use_id = `toolu_e2e_${randomUUID()}`
  delete sample.tool_input
  const head = JSON.stringify(sample)
  return execHook(env, `${head.slice(0, -1)},"tool_input":${toolInput}}`, children)
}

/**
 * JSON 文本里 path（逐层的对象键）指向的值的原始片段：不解码、不重排，用来断言钩子把某个字段逐字节原样回填。
 * 只走对象的键（钩子输出够用）；找不到回 undefined。text 须是合法 JSON（调用前先 JSON.parse 过）。
 */
export function rawAt(text: string, path: string[]): string | undefined {
  let i = 0
  const ws = () => {
    while (i < text.length && ' \t\n\r'.includes(text[i]!)) i++
  }
  const str = (): string => {
    const start = i
    i++
    while (text[i] !== '"') i += text[i] === '\\' ? 2 : 1
    i++
    return JSON.parse(text.slice(start, i)) as string
  }
  // 跳过一个值（字符串、对象、数组或字面量）
  const skip = (): void => {
    ws()
    const c = text[i]
    if (c === '"') {
      str()
      return
    }
    if (c === '{' || c === '[') {
      const close = c === '{' ? '}' : ']'
      i++
      ws()
      while (text[i] !== close) {
        if (c === '{') {
          str()
          ws()
          i++ // 冒号
        }
        skip()
        ws()
        if (text[i] === ',') i++
        ws()
      }
      i++
      return
    }
    while (i < text.length && !',]} \t\n\r'.includes(text[i]!)) i++
  }
  const find = (depth: number): string | undefined => {
    ws()
    if (depth === path.length) {
      const start = i
      skip()
      return text.slice(start, i)
    }
    if (text[i] !== '{') return undefined
    i++
    ws()
    while (text[i] !== '}') {
      const key = str()
      ws()
      i++ // 冒号
      if (key === path[depth]) return find(depth + 1)
      skip()
      ws()
      if (text[i] === ',') i++
      ws()
    }
    return undefined
  }
  return find(0)
}

/** 钩子 stdout 里 Claude 认的决定（hookSpecificOutput.decision.behavior）；不作决定时 stdout 为空，回 ''。 */
export function behavior(out: HookOut): string {
  const s = out.stdout.trim()
  if (!s) return ''
  const doc = JSON.parse(s) as { hookSpecificOutput?: { decision?: { behavior?: string } } }
  return doc.hookSpecificOutput?.decision?.behavior ?? ''
}

/** 打开网页、登录，等到空的收件箱（各用例开始时都没有待处理的事）。 */
export async function login(page: Page, env: E2EEnv, lang: 'zh' | 'en' = 'zh') {
  const resp = await page.goto(env.baseURL + '/')
  // 网页由 hub 同源托管，HTML 带 CSP：下面“没有违规”的断言才有意义
  expect(resp?.headers()['content-security-policy']).toContain("script-src 'self'")
  await expect(page).toHaveURL(/\/login/)
  await page.getByLabel(lang === 'en' ? 'Password' : '密码').fill(env.password)
  const button = page.getByRole('button', { name: lang === 'en' ? 'Log in' : '登录', exact: true })
  const empty = page.getByText(lang === 'en' ? 'Nothing waiting for you' : '没有等你处理的事')
  const limited = page.getByText(/尝试太频繁|Too many attempts/)
  // hub 的登录限速是每个来源 IP 每分钟 5 次，整套用例（每个用例一个新的浏览器上下文、各登录一次）会超过：
  // 碰上限速就等一会儿再点，至多等到窗口滑过（70 秒）。不改 hub 的限速。
  const until = Date.now() + 70_000
  for (;;) {
    await button.click()
    await expect(empty.or(limited)).toBeVisible()
    if (await empty.isVisible()) return
    if (Date.now() > until) break
    await page.waitForTimeout(5_000)
  }
  await expect(empty).toBeVisible()
}

/**
 * 记下页面上的 CSP 违规：页面里的 securitypolicyviolation 事件（经 exposeFunction 送回测试进程，换页也不丢），
 * 与控制台里提到 Content Security Policy 的消息。另记未捕获的页面异常。
 */
export async function watchPage(page: Page, csp: string[], errors: string[]) {
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

/** 杀掉还没退出的钩子进程。 */
function killAll(children: ChildProcess[]) {
  for (const c of children) if (c.exitCode === null && c.signalCode === null) c.kill('SIGKILL')
}

interface Fixtures {
  env: E2EEnv
  /** 跑一次钩子；用例结束时还没退出的钩子进程会被杀掉 */
  hook: (command: string) => Promise<HookOut>
  /** 跑一次提问（PreToolUse/AskUserQuestion）的钩子，toolInput 是 tool_input 的 JSON 原文；同样在用例结束时收拾 */
  ask: (toolInput: string) => Promise<HookOut>
  csp: string[]
}

export const test = base.extend<Fixtures>({
  env: async ({}, use) => {
    await use(readEnv())
  },
  hook: async ({ env }, use) => {
    const children: ChildProcess[] = []
    await use((command) => runHook(env, command, children))
    killAll(children)
  },
  ask: async ({ env }, use) => {
    const children: ChildProcess[] = []
    await use((toolInput) => runAskHook(env, toolInput, children))
    killAll(children)
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

export { expect }
