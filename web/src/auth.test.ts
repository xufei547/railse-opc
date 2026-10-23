import { describe, expect, it } from 'vitest'
import { createLoginRedirect, loginPath, safeNext } from './auth'

describe('loginPath', () => {
  it('带上当前位置（含查询），首页与登录页本身不带', () => {
    expect(loginPath('/r/abc')).toBe('/login?next=%2Fr%2Fabc')
    expect(loginPath('/history?state=denied')).toBe('/login?next=%2Fhistory%3Fstate%3Ddenied')
    expect(loginPath('/')).toBe('/login')
    expect(loginPath('')).toBe('/login')
    expect(loginPath('/login')).toBe('/login')
    expect(loginPath('/login?next=%2Fr%2Fx')).toBe('/login')
  })
})

describe('safeNext', () => {
  it('只收本站路径', () => {
    expect(safeNext('/r/abc')).toBe('/r/abc')
    expect(safeNext('/history?state=denied')).toBe('/history?state=denied')
    expect(safeNext(null)).toBe('/')
    expect(safeNext('')).toBe('/')
  })
  it('拒绝跳到别的站点（开放重定向）', () => {
    for (const bad of ['//evil.example', '/\\evil.example', '\\\\evil.example', 'https://evil.example/', 'javascript:alert(1)', '/\t/evil.example', '/.//evil.example', 'r/abc']) {
      expect(safeNext(bad), bad).toBe('/')
    }
  })
  it('不跳回登录页', () => {
    expect(safeNext('/login')).toBe('/')
    expect(safeNext('/login?next=%2Fr%2Fx')).toBe('/')
  })
})

describe('createLoginRedirect', () => {
  /** 模拟浏览器地址：navigate 像 history.replaceState 一样同步改地址（React 的重渲染、卸载还没发生）。 */
  function browser(start: string) {
    let loc = start
    const calls: string[] = []
    const toLogin = createLoginRedirect(
      () => loc,
      (to) => {
        calls.push(to)
        loc = to
      },
    )
    return { toLogin, calls, where: () => loc }
  }

  it('并发的两个 401 连着跳两次：第二次是空操作，next 保留', () => {
    const b = browser('/r/abc')
    b.toLogin()
    b.toLogin()
    expect(b.calls).toEqual(['/login?next=%2Fr%2Fabc'])
    expect(b.where()).toBe('/login?next=%2Fr%2Fabc')
  })

  it('已经在登录页时不跳', () => {
    const b = browser('/login')
    b.toLogin()
    expect(b.calls).toEqual([])
  })

  it('从首页跳不带 next', () => {
    const b = browser('/')
    b.toLogin()
    expect(b.calls).toEqual(['/login'])
  })
})
