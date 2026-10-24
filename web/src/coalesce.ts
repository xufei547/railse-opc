export interface Coalesced {
  /** 请求执行一次：空闲时立即执行；正在执行时在它结束后再补执行一次（期间的多次请求合并成这一次）。 */
  (): Promise<void>
  /** 正在执行时标脏，结束后补执行一次（执行期间到达的变化可能没进这次结果）；空闲时什么也不做。 */
  dirty(): void
}

/**
 * 把 run 包成“同一时刻至多一个在执行、请求合并”的形式。全量重新拉取用它：连上、回到前台、收到 request_new 都会触发，
 * 一阵帧来时只多拉一次；执行是串行的，后发的请求的结果不会被先发的旧结果覆盖。
 * 返回的 Promise 在覆盖这次请求的那一轮（含补执行）结束时 resolve；最后一轮出错时 reject 那个错误。
 */
export function coalesce(run: () => Promise<void>): Coalesced {
  let running: Promise<void> | null = null
  let again = false
  // 包一层 async：run 同步抛错时也变成 reject，loop 一定在 await 处让出，finally 不会抢在 running 赋值之前执行
  const invoke = async () => {
    await run()
  }

  const loop = async () => {
    let lastErr: unknown = null
    let failed = false
    try {
      do {
        again = false
        try {
          await invoke()
          failed = false
        } catch (e) {
          failed = true
          lastErr = e
        }
      } while (again)
    } finally {
      running = null
    }
    if (failed) throw lastErr
  }

  const call = (() => {
    if (running) {
      again = true
      return running
    }
    running = loop()
    return running
  }) as Coalesced
  call.dirty = () => {
    if (running) again = true
  }
  return call
}
