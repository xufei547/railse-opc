// 端到端用例与演示录制（web/scripts/record-demo.ts）共用的夹具：跑真实钩子、盯住页面上的 CSP 违规与未捕获的异常。
// 文件名不带 .spec：playwright 不会把它当用例收进去。

import { test as base, expect, type Page } from '@playwright/test'
import { execFile, type ChildProcess } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import { childEnv, readEnv, type E2EEnv } from './env'

const samplePath = fileURLToPath(new URL('../../internal/adapter/testdata/claude/2.1.280/PermissionRequest.json', import.meta.url))

export interface HookOut {
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
export function behavior(out: HookOut): string {
  const s = out.stdout.trim()
  if (!s) return ''
  const doc = JSON.parse(s) as { hookSpecificOutput?: { decision?: { behavior?: string } } }
  return doc.hookSpecificOutput?.decision?.behavior ?? ''
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

interface Fixtures {
  env: E2EEnv
  /** 跑一次钩子；用例结束时还没退出的钩子进程会被杀掉 */
  hook: (command: string) => Promise<HookOut>
  csp: string[]
}

export const test = base.extend<Fixtures>({
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

export { expect }
