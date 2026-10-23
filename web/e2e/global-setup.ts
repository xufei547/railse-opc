import { spawn } from 'node:child_process'
import { closeSync, existsSync, mkdirSync, mkdtempSync, openSync, readFileSync, rmSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { childEnv, envVar, runBin, stopAll, type E2EEnv } from './env'

const password = 'e2e-password-123'

/** macOS 的 Unix socket 路径上限是 104 字节：临时根目录太长时退回 /tmp。 */
function makeBase(): { base: string; ownBase: boolean } {
  const given = process.env.RAILSE_E2E_DIR
  if (given) return { base: resolve(given), ownBase: false }
  const parent = tmpdir().length > 60 ? '/tmp' : tmpdir()
  return { base: mkdtempSync(join(parent, 'railse-e2e.')), ownBase: true }
}

function freePort(): Promise<number> {
  return new Promise((res, rej) => {
    const s = createServer()
    s.on('error', rej)
    s.listen(0, '127.0.0.1', () => {
      const addr = s.address()
      if (typeof addr === 'object' && addr) {
        const port = addr.port
        s.close(() => res(port))
      } else rej(new Error('拿不到空闲端口'))
    })
  })
}

/** 起的长期进程：stderr 写进 <base>/<name>.stderr（不带 --foreground 时只有致命错误才写），提前退出或起不来时记下原因。 */
interface Daemon {
  name: string
  pid: number
  /** 提前退出或起不来的原因；还在跑时为 null */
  failed: string | null
}

function startDaemon(name: string, bin: string, args: string[], env: NodeJS.ProcessEnv, base: string): Daemon {
  const errFd = openSync(join(base, `${name}.stderr`), 'a')
  const child = spawn(bin, args, { stdio: ['ignore', 'ignore', errFd], env })
  closeSync(errFd) // 子进程已经拿到了自己的一份
  const d: Daemon = { name, pid: child.pid ?? 0, failed: null }
  child.on('error', (e) => {
    d.failed = `${name} 起不来: ${e.message}`
  })
  child.on('exit', (code, signal) => {
    d.failed = `${name} 提前退出（${signal ?? `退出码 ${code}`}）`
  })
  return d
}

/** 每 100ms 查一次 cond，ms 内等不到就失败；等的过程中 daemon 退出了立即失败（不白等到超时）。 */
async function waitFor(what: string, ms: number, cond: () => boolean | Promise<boolean>, daemon?: Daemon) {
  const until = Date.now() + ms
  while (Date.now() < until) {
    if (daemon?.failed) throw new Error(`等${what}时${daemon.failed}`)
    try {
      if (await cond()) return
    } catch {
      // 还没好
    }
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`${ms}ms 内没有等到${what}`)
}

function tail(path: string): string {
  try {
    return readFileSync(path, 'utf8').split('\n').slice(-20).join('\n')
  } catch {
    return '（没有日志）'
  }
}

export default async function globalSetup() {
  const bin = process.env.RAILSE_E2E_BIN
  if (!bin || !existsSync(bin)) throw new Error('没有 RAILSE_E2E_BIN（带网页构建的 railse 二进制）：请用 scripts/e2e-web.sh（make e2e）运行端到端')
  const { base, ownBase } = makeBase()
  const hubDir = join(base, 'h')
  const agentDir = join(base, 'a')
  const repo = join(base, 'repo')
  const home = join(base, 'home')
  const socket = join(agentDir, 'agent.sock')
  if (Buffer.byteLength(socket) > 100) throw new Error(`socket 路径太长（${socket}），换一个短一点的临时目录`)
  mkdirSync(join(repo, '.git'), { recursive: true })
  mkdirSync(home, { recursive: true })
  const pids: number[] = []
  const paths = { bin: resolve(bin), home, agentDir, hubDir }
  const env0 = childEnv(paths)
  try {
    runBin(paths, ['hub', 'password', '--dir', hubDir], password + '\n')
    // 空闲端口是先探后用：探完到 hub 监听之间可能被别的进程占走，hub 监听失败就换一个端口重试
    let baseURL = ''
    for (let attempt = 1; ; attempt++) {
      const port = await freePort()
      baseURL = `http://127.0.0.1:${port}`
      const hub = startDaemon('hub', paths.bin, ['hub', 'run', '--dir', hubDir, '--listen', `127.0.0.1:${port}`], env0, base)
      if (hub.pid) pids.push(hub.pid)
      try {
        await waitFor(`hub 起来（${baseURL}）`, 10_000, async () => (await fetch(baseURL + '/api/status')).status === 401, hub)
        break
      } catch (e) {
        if (!hub.failed || attempt >= 3) throw e
      }
    }
    // 二进制必须带着网页构建：没构建时 / 回 503“网页未构建”
    const root = await fetch(baseURL + '/')
    if (root.status !== 200 || !(root.headers.get('content-type') ?? '').startsWith('text/html')) {
      throw new Error(`hub 的 / 回 ${root.status}：二进制里没有网页构建（先 npm run build 再 go build）`)
    }
    const tok = /接入码: (\S+)/.exec(runBin(paths, ['hub', 'token', '--dir', hubDir]))?.[1]
    if (!tok) throw new Error('railse hub token 的输出里没有接入码')
    runBin(paths, ['agent', 'enroll', '--dir', agentDir, '--hub', baseURL, '--token', tok, '--name', 'e2e-mac'])
    const agent = startDaemon('agent', paths.bin, ['agent', 'run', '--dir', agentDir], env0, base)
    if (agent.pid) pids.push(agent.pid)
    // 守护进程连上 hub 并换上 hub 下发的规则之后才算好：在那之前跑钩子，请求会按本地规则或“连不上 hub”处理
    await waitFor('守护进程的 socket', 10_000, () => existsSync(socket), agent)
    await waitFor('守护进程换上 hub 的规则', 15_000, () => readFileSync(join(agentDir, 'agent.log'), 'utf8').includes('规则更新到版本'), agent)
    const env: E2EEnv = { baseURL, bin: paths.bin, base, ownBase, hubDir, agentDir, socket, repo, home, password, pids }
    process.env[envVar] = JSON.stringify(env)
  } catch (e) {
    const files: [string, string][] = [
      ['hub.log', join(hubDir, 'hub.log')],
      ['hub.stderr', join(base, 'hub.stderr')],
      ['agent.log', join(agentDir, 'agent.log')],
      ['agent.stderr', join(base, 'agent.stderr')],
    ]
    const logs = files.map(([name, path]) => `\n--- ${name} ---\n${tail(path)}`).join('')
    await stopAll(pids)
    if (ownBase) rmSync(base, { recursive: true, force: true })
    throw new Error(`${e instanceof Error ? e.message : String(e)}${logs}`)
  }
}
