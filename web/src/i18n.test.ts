import { afterEach, describe, expect, it } from 'vitest'
import { lang, normalize, readChoice, resolveLang, saveChoice, setLang, t } from './i18n'
import { en } from './locales/en'

afterEach(() => setLang('zh'))

describe('normalize / resolveLang', () => {
  it('zh 开头算中文，其余英文', () => {
    expect(normalize('zh-CN')).toBe('zh')
    expect(normalize('ZH-tw')).toBe('zh')
    expect(normalize('en-US')).toBe('en')
    expect(normalize('fr')).toBe('en')
    expect(normalize(undefined)).toBe('en')
    expect(normalize('')).toBe('en')
  })
  it('选了就按选的，auto 跟浏览器', () => {
    expect(resolveLang('zh', 'en-US')).toBe('zh')
    expect(resolveLang('en', 'zh-CN')).toBe('en')
    expect(resolveLang('auto', 'zh-CN')).toBe('zh')
    expect(resolveLang('auto', 'de')).toBe('en')
  })
})

describe('readChoice / saveChoice', () => {
  const mem = () => {
    const m = new Map<string, string>()
    return { getItem: (k: string) => m.get(k) ?? null, setItem: (k: string, v: string) => void m.set(k, v), removeItem: (k: string) => void m.delete(k), m }
  }
  it('只认 zh、en，别的都是 auto；存取失败不抛', () => {
    const s = mem()
    expect(readChoice(s)).toBe('auto')
    saveChoice(s, 'en')
    expect(readChoice(s)).toBe('en')
    saveChoice(s, 'auto')
    expect(s.m.has('railse.lang')).toBe(false)
    s.m.set('railse.lang', 'fr')
    expect(readChoice(s)).toBe('auto')
    const broken = { getItem: () => { throw new Error('private mode') }, setItem: () => { throw new Error('x') }, removeItem: () => { throw new Error('x') } }
    expect(readChoice(broken)).toBe('auto')
    expect(() => saveChoice(broken, 'zh')).not.toThrow()
    expect(readChoice(null)).toBe('auto')
  })
})

describe('t', () => {
  it('中文下原样返回，英文下查表，查不到回退中文', () => {
    expect(lang()).toBe('zh')
    expect(t('没有翻译的一句')).toBe('没有翻译的一句')
    setLang('en')
    expect(t('没有翻译的一句')).toBe('没有翻译的一句')
    en['测试键'] = 'test key'
    expect(t('测试键')).toBe('test key')
    delete en['测试键']
  })
  it('{name} 占位两种语言都替换；没给的占位原样留着', () => {
    en['还有 {n} 题没答'] = '{n} left to answer'
    expect(t('还有 {n} 题没答', { n: 2 })).toBe('还有 2 题没答')
    setLang('en')
    expect(t('还有 {n} 题没答', { n: 2 })).toBe('2 left to answer')
    expect(t('还有 {n} 题没答')).toBe('{n} left to answer')
    delete en['还有 {n} 题没答']
  })
})
