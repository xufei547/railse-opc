import { describe, expect, it } from 'vitest'
import { loginPath, safeNext } from './auth'

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
