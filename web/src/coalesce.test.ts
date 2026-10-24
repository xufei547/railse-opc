import { describe, expect, it } from 'vitest'
import { coalesce } from './coalesce'

function deferred() {
  let resolve!: () => void
  let reject!: (e: unknown) => void
  const promise = new Promise<void>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

/** 让已排队的微任务跑完。 */
const flush = () => new Promise<void>((r) => setTimeout(r, 0))

describe('coalesce', () => {
  it('空闲时立即执行', async () => {
    let runs = 0
    const reload = coalesce(async () => {
      runs++
    })
    await reload()
    expect(runs).toBe(1)
    await reload()
    expect(runs).toBe(2)
  })

  it('执行中再请求多次，只在结束后再补一次，且都等到补的那次结束', async () => {
    const gates = [deferred(), deferred()]
    let runs = 0
    const reload = coalesce(() => gates[runs++]!.promise)
    const first = reload()
    const second = reload()
    const third = reload()
    expect(runs).toBe(1)
    let secondDone = false
    void second.then(() => {
      secondDone = true
    })
    gates[0]!.resolve()
    await flush()
    expect(runs).toBe(2) // 合并成一次补跑
    expect(secondDone).toBe(false) // 补跑还没完
    gates[1]!.resolve()
    await Promise.all([first, second, third])
    expect(runs).toBe(2)
  })

  it('dirty：执行中标脏则结束后补一次；空闲时不执行', async () => {
    const gates = [deferred(), deferred()]
    let runs = 0
    const reload = coalesce(() => gates[runs++]!.promise)
    reload.dirty()
    expect(runs).toBe(0)
    const p = reload()
    reload.dirty()
    gates[0]!.resolve()
    await flush()
    expect(runs).toBe(2)
    gates[1]!.resolve()
    await p
    expect(runs).toBe(2)
  })

  it('执行出错：等待者拿到错误，后续请求照常执行', async () => {
    let runs = 0
    const reload = coalesce(async () => {
      runs++
      if (runs === 1) throw new Error('boom')
    })
    await expect(reload()).rejects.toThrow('boom')
    await reload()
    expect(runs).toBe(2)
  })

  it('run 同步抛错也不会把自己卡成“一直在执行”', async () => {
    let runs = 0
    const reload = coalesce((() => {
      runs++
      if (runs === 1) throw new Error('sync boom')
      return Promise.resolve()
    }) as () => Promise<void>)
    await expect(reload()).rejects.toThrow('sync boom')
    await reload()
    expect(runs).toBe(2)
  })

  it('出错的那次之后还有补跑时照样补跑，以补跑的结果为准', async () => {
    const gates = [deferred(), deferred()]
    let runs = 0
    const reload = coalesce(() => gates[runs++]!.promise)
    const p = reload()
    reload.dirty()
    gates[0]!.reject(new Error('boom'))
    await flush()
    expect(runs).toBe(2)
    gates[1]!.resolve()
    await expect(p).resolves.toBeUndefined()
  })
})
