import { describe, expect, it } from 'vitest'
import { inertSiblings } from './modal'

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
