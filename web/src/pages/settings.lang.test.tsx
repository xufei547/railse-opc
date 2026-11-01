import { renderToStaticMarkup } from 'react-dom/server'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setLang } from '../i18n'
import { LanguageView, switchLanguage } from './Settings'

afterEach(() => setLang('zh'))

describe('LanguageView', () => {
  it('三个选项，当前选中的带 checked；英文下标签是英文', () => {
    let html = renderToStaticMarkup(<LanguageView choice="auto" onChange={() => {}} busy={false} />)
    expect(html).toContain('跟随系统')
    expect(html).toContain('中文')
    expect(html).toContain('English')
    expect((html.match(/checked=""/g) ?? []).length).toBe(1)
    setLang('en')
    html = renderToStaticMarkup(<LanguageView choice="en" onChange={() => {}} busy={false} />)
    expect(html).toContain('Follow the system')
    expect(html).toContain('中文') // 语言名用本语言写，不翻
    expect(html).toContain('English')
    // 整页不含任何汉字（含语言选项标签）
    expect(html.replace(/中文/g, '')).not.toMatch(/[一-鿿]/)
    expect(html).toContain('>Language</h2>')
  })
})

describe('switchLanguage', () => {
  it('保存选择，推送开着就重提交订阅（带新语言），然后刷新', async () => {
    const m = new Map<string, string>()
    const storage = { setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k) }
    const sub = { toJSON: () => ({ endpoint: 'https://p/1', keys: { p256dh: 'a', auth: 'b' } }) } as unknown as PushSubscription
    const env = { getSubscription: vi.fn(async () => sub) }
    const api = { pushSubscribe: vi.fn(async () => {}) }
    const reload = vi.fn()
    await switchLanguage('en', { storage, env: env as never, api: api as never, reload, pushOn: true })
    expect(m.get('railse.lang')).toBe('en')
    expect(api.pushSubscribe).toHaveBeenCalledWith({ endpoint: 'https://p/1', keys: { p256dh: 'a', auth: 'b' } }, 'en')
    expect(reload).toHaveBeenCalledTimes(1)
  })
  it('推送没开就不碰订阅；重提交失败也照样刷新', async () => {
    const storage = { setItem: () => {}, removeItem: () => {} }
    const api = { pushSubscribe: vi.fn(async () => { throw new Error('hub down') }) }
    const reload = vi.fn()
    await switchLanguage('zh', { storage, env: { getSubscription: async () => null } as never, api: api as never, reload, pushOn: false })
    expect(api.pushSubscribe).not.toHaveBeenCalled()
    expect(reload).toHaveBeenCalled()
    await switchLanguage('en', { storage, env: { getSubscription: async () => ({ toJSON: () => ({}) }) } as never, api: api as never, reload, pushOn: true })
    expect(reload).toHaveBeenCalledTimes(2)
  })
})
