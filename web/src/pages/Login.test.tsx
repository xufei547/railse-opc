import { renderToStaticMarkup } from 'react-dom/server'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { Login } from './Login'

describe('Login', () => {
  const html = renderToStaticMarkup(
    <MemoryRouter initialEntries={['/login?next=%2Fr%2Fabc']}>
      <Login />
    </MemoryRouter>,
  )
  // HTML 属性名不分大小写（React 19 的服务端渲染输出 autoComplete、readOnly），统一转小写再比
  const inputs = (html.match(/<input[^>]*>/g) ?? []).map((i) => i.toLowerCase())

  it('有固定值的隐藏用户名字段（iOS 钥匙串据此保存、填充密码）', () => {
    const user = inputs.find((i) => i.includes('autocomplete="username"'))
    expect(user, html).toBeDefined()
    expect(user).toContain('value="railse"')
    expect(user).toContain('readonly=""')
    expect(user).toMatch(/class="visually-hidden"/)
    expect(user).toContain('tabindex="-1"')
    expect(user).toContain('aria-hidden="true"')
  })

  it('密码字段在用户名字段之后，autocomplete 为 current-password', () => {
    const iUser = inputs.findIndex((i) => i.includes('autocomplete="username"'))
    const iPass = inputs.findIndex((i) => i.includes('type="password"'))
    expect(iUser).toBeGreaterThanOrEqual(0)
    expect(iPass).toBeGreaterThan(iUser)
    expect(inputs[iPass]).toContain('autocomplete="current-password"')
  })

  it('没有内联样式（hub 的 CSP 不许）', () => {
    expect(html).not.toContain('style=')
  })
})
