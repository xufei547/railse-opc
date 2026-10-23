import { describe, expect, it } from 'vitest'
import { confirmClosed, confirmReducer, type ConfirmRequest, type ConfirmState } from './confirm'

const del: ConfirmRequest = { title: '删除规则？', body: '所有机器会立即收到更新。', confirmLabel: '删除', danger: true }
const imp: ConfirmRequest = { title: '导入规则？', body: '整套替换。', confirmLabel: '导入', danger: true }

function open(req = del): ConfirmState<ConfirmRequest> {
  return confirmReducer(confirmClosed, { type: 'open', req })
}

describe('confirmReducer', () => {
  it('打开：带上要确认的内容，不忙、没有错误', () => {
    expect(open()).toEqual({ req: del, busy: false, error: null })
  })

  it('同一时刻只开一个：开着时再 open 不换内容（不叠模态）', () => {
    expect(confirmReducer(open(), { type: 'open', req: imp }).req).toBe(del)
  })

  it('确认后忙，忙时再确认、取消都不理（不重复提交，结果出来前不关）', () => {
    const busy = confirmReducer(open(), { type: 'submit' })
    expect(busy.busy).toBe(true)
    expect(confirmReducer(busy, { type: 'submit' })).toBe(busy)
    expect(confirmReducer(busy, { type: 'cancel' })).toBe(busy)
  })

  it('失败：留在面板上显示原因，可以重试；重试时清掉旧错误', () => {
    const failed = confirmReducer(confirmReducer(open(), { type: 'submit' }), { type: 'failed', message: '没有这条规则' })
    expect(failed).toEqual({ req: del, busy: false, error: '没有这条规则' })
    expect(confirmReducer(failed, { type: 'submit' })).toEqual({ req: del, busy: true, error: null })
  })

  it('成功或取消后关掉；关掉之后才能开下一个', () => {
    const done = confirmReducer(confirmReducer(open(), { type: 'submit' }), { type: 'done' })
    expect(done).toEqual(confirmClosed)
    expect(confirmReducer(open(), { type: 'cancel' })).toEqual(confirmClosed)
    expect(confirmReducer(done, { type: 'open', req: imp }).req).toBe(imp)
  })

  it('没开时的 submit、failed 不起作用', () => {
    expect(confirmReducer(confirmClosed, { type: 'submit' })).toBe(confirmClosed)
    expect(confirmReducer(confirmClosed, { type: 'failed', message: 'x' })).toBe(confirmClosed)
  })
})
