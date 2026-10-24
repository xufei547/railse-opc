// 端到端的环境：global-setup 起好 hub 与守护进程后写进环境变量 RAILSE_E2E_ENV，测试进程（worker 继承环境）与 teardown 读它。
// 不落盘到仓库里：上一次运行留下的文件不会被误读成这一次的地址。

import { spawnSync } from 'node:child_process'

export interface E2EEnv {
  /** hub 的地址，如 http://127.0.0.1:53123 */
  baseURL: string
  /** 带网页构建的 railse 二进制 */
  bin: string
  /** 这次运行的临时根目录：下面是 hub、守护进程、仓库与家目录 */
  base: string
  /** base 是 setup 自己建的（不是 scripts/e2e-web.sh 传进来的）：teardown 负责删掉 */
  ownBase: boolean
  hubDir: string
  agentDir: string
  /** 守护进程的 socket；钩子用 --socket 直接连它，不装进任何 CLI 的配置 */
  socket: string
  /** 模拟的项目（含 .git） */
  repo: string
  /** 子进程的 HOME：指向临时目录，任何退回默认目录的路径都落不到真实家目录里 */
  home: string
  password: string
  /** setup 起的长期进程（hub、守护进程） */
  pids: number[]
}

export const envVar = 'RAILSE_E2E_ENV'

export function readEnv(): E2EEnv {
  const raw = process.env[envVar]
  if (!raw) throw new Error(`没有 ${envVar}：端到端要经 global-setup 起环境，请用 scripts/e2e-web.sh（make e2e）运行`)
  return JSON.parse(raw) as E2EEnv
}

/** 子进程的环境：HOME、RAILSE_HOME、RAILSE_HUB_HOME 都指向临时目录。 */
export function childEnv(env: Pick<E2EEnv, 'home' | 'agentDir' | 'hubDir'>): NodeJS.ProcessEnv {
  return { ...process.env, HOME: env.home, RAILSE_HOME: env.agentDir, RAILSE_HUB_HOME: env.hubDir }
}

function alive(pid: number): boolean {
  try {
    process.kill(pid, 0)
    return true
  } catch {
    return false
  }
}

/** 先 SIGTERM，至多等 ms 毫秒，还在的 SIGKILL。只动 pids 里的进程。 */
export async function stopAll(pids: number[], ms = 5000): Promise<void> {
  const live = pids.filter((p) => p > 0 && alive(p))
  for (const p of live) {
    try {
      process.kill(p, 'SIGTERM')
    } catch {
      // 已经退出
    }
  }
  const until = Date.now() + ms
  while (Date.now() < until && live.some(alive)) await new Promise((r) => setTimeout(r, 50))
  for (const p of live.filter(alive)) {
    try {
      process.kill(p, 'SIGKILL')
    } catch {
      // 已经退出
    }
  }
}

/** 同步跑一次 railse 子命令，失败时带上 stderr 抛出。 */
export function runBin(env: Pick<E2EEnv, 'bin' | 'home' | 'agentDir' | 'hubDir'>, args: string[], input?: string): string {
  const r = spawnSync(env.bin, args, { input, encoding: 'utf8', env: childEnv(env), timeout: 30_000 })
  if (r.error) throw r.error
  if (r.status !== 0) throw new Error(`railse ${args[0]} ${args[1] ?? ''} 失败（退出码 ${r.status}）: ${r.stderr}`)
  return r.stdout
}
