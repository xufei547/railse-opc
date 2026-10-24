import { rmSync } from 'node:fs'
import { envVar, stopAll, type E2EEnv } from './env'

export default async function globalTeardown() {
  const raw = process.env[envVar]
  if (!raw) return // setup 没成功：它已经自己收拾过了
  const env = JSON.parse(raw) as E2EEnv
  await stopAll(env.pids)
  // scripts/e2e-web.sh 传进来的目录由脚本删（它还要在失败时打印日志）；setup 自己建的这里删
  if (env.ownBase) rmSync(env.base, { recursive: true, force: true })
}
