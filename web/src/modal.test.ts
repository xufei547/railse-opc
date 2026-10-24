import { describe, expect, it } from 'vitest'
import { inertSiblings, toastSpace } from './modal'

describe('inertSiblings', () => {
  it('面板打开时把除面板与背景之外的兄弟元素设为 inert，关闭时恢复原值', () => {
    const header = { inert: false }
    const page = { inert: false }
    const nav = { inert: false }
    const already = { inert: true }
    const backdrop = { inert: false }
    const sheet = { inert: false }
    const restore = inertSiblings([header, page, backdrop, sheet, nav, already], [backdrop, sheet])
    expect([header.inert, page.inert, nav.inert, already.inert]).toEqual([true, true, true, true])
    expect(backdrop.inert).toBe(false)
    expect(sheet.inert).toBe(false)
    restore()
    expect([header.inert, page.inert, nav.inert, already.inert]).toEqual([false, false, false, true])
  })
})

describe('toastSpace', () => {
  it('页面底部要留出的高度：从结果提示的上沿到底部导航的上沿（向上取整），不为负', () => {
    expect(toastSpace(788, 652)).toBe(136)
    expect(toastSpace(788, 651.2)).toBe(137)
    expect(toastSpace(788, 800)).toBe(0)
  })
})
