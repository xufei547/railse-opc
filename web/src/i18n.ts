// 多语言：中文原文当键。代码里照常写中文，t() 在英文下查 locales/en.ts，查不到原样返回（只是显示中文，不会坏）。
// 语言在 main.tsx 里同步解析一次（initLang），之后 lang()/t() 都读模块级变量；切换语言走 Settings：保存、重提交推送订阅、整页刷新。
import { en } from './locales/en'

export type Lang = 'zh' | 'en'
export type LangChoice = 'auto' | Lang
export const langKey = 'railse.lang'

let current: Lang = 'zh'

/** 浏览器语言标签 → zh/en：zh 开头算中文，其余（包括空）英文。 */
export function normalize(tag: string | null | undefined): Lang {
  return /^zh/i.test((tag ?? '').trim()) ? 'zh' : 'en'
}

export function resolveLang(choice: LangChoice, navigatorLang: string | undefined): Lang {
  return choice === 'auto' ? normalize(navigatorLang) : choice
}

/** localStorage 里的选择；读不到、读错、不是 zh/en 都算 auto。 */
export function readChoice(storage: Pick<Storage, 'getItem'> | null | undefined): LangChoice {
  try {
    const v = storage?.getItem(langKey)
    return v === 'zh' || v === 'en' ? v : 'auto'
  } catch {
    return 'auto'
  }
}

/** 保存选择；auto 就删掉键。存不进去（隐私模式）忽略。 */
export function saveChoice(storage: Pick<Storage, 'setItem' | 'removeItem'> | null | undefined, choice: LangChoice): void {
  try {
    if (choice === 'auto') storage?.removeItem(langKey)
    else storage?.setItem(langKey, choice)
  } catch {
    // 忽略
  }
}

export function htmlLang(l: Lang): string {
  return l === 'zh' ? 'zh-CN' : 'en'
}

/** 浏览器里调一次：按 localStorage 的选择与 navigator.language 定语言，并写到 <html lang>。 */
export function initLang(): Lang {
  const storage = typeof localStorage === 'undefined' ? null : localStorage
  const nav = typeof navigator === 'undefined' ? undefined : navigator.language
  const l = resolveLang(readChoice(storage), nav)
  setLang(l)
  if (typeof document !== 'undefined') document.documentElement.lang = htmlLang(l)
  return l
}

export function setLang(l: Lang): void {
  current = l
}

export function lang(): Lang {
  return current
}

/**
 * 翻译一句。zh 必须是字符串字面量（i18n.catalog.test.ts 靠它找键）；vars 替换 {name} 占位，两种语言都替换。
 */
export function t(zh: string, vars?: Record<string, string | number>): string {
  const s = current === 'zh' ? zh : (en[zh] ?? zh)
  if (!vars) return s
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in vars ? String(vars[k]) : m))
}
