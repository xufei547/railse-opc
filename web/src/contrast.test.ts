import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { describe, expect, it } from 'vitest'

// 读 app.css 的颜色变量，按 WCAG 2 公式检查文字与背景的对比度：普通文字至少 4.5 比 1（AA），浅色、深色都要。
const css = readFileSync(fileURLToPath(new URL('./app.css', import.meta.url)), 'utf8')

function vars(block: string): Record<string, string> {
  const out: Record<string, string> = {}
  for (const m of block.matchAll(/--([\w-]+):\s*(#[0-9a-fA-F]{6})\b/g)) out[m[1]!] = m[2]!.toLowerCase()
  return out
}
const rootStart = css.indexOf(':root {')
const light = vars(css.slice(rootStart, css.indexOf('}', rootStart)))
const darkBlock = /@media \(prefers-color-scheme: dark\) \{\s*:root \{([^}]*)\}/.exec(css)?.[1] ?? ''
const dark = { ...light, ...vars(darkBlock) }

function lum(hex: string): number {
  const [r, g, b] = (hex.slice(1).match(/../g) ?? []).map((h) => {
    const v = parseInt(h, 16) / 255
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!
}
function ratio(a: string, b: string): number {
  const [x, y] = [lum(a), lum(b)].sort((p, q) => q - p)
  return (x! + 0.05) / (y! + 0.05)
}
const color = (theme: Record<string, string>, v: string) => (v.startsWith('#') ? v : theme[v])

/** [文字, 背景, 用在哪]。 */
const pairs: [string, string, string][] = [
  ['high-fg', 'high-bg', '红色横幅、含隐藏部分的告警、没有生效的结果提示、高风险标签'],
  ['low-fg', 'low-bg', '绿色横幅、成功的结果提示、低风险标签'],
  ['mid-fg', 'mid-bg', '信息横幅（断线、暂停）、中风险标签'],
  ['high-fg', 'bg', '.error（页面上）'],
  ['high-fg', 'card', '.error（面板的草案框里）'],
  ['plain-fg', 'line', '种类标签（等你回答、webhook）'],
  ['fg', 'card', '横幅与提示里的按钮'],
  ['#ffffff', 'high', '拒绝按钮、导航角标（白字压在 --high 上）'],
]

describe('颜色对比度（WCAG AA，普通文字 ≥ 4.5）', () => {
  for (const [theme, t] of [
    ['浅色', light],
    ['深色', dark],
  ] as const) {
    for (const [fg, bg, use] of pairs) {
      it(`${theme}：${use}`, () => {
        const f = color(t, fg)
        const b = color(t, bg)
        expect(f, `缺少 --${fg}`).toBeDefined()
        expect(b, `缺少 --${bg}`).toBeDefined()
        expect(ratio(f!, b!)).toBeGreaterThanOrEqual(4.5)
      })
    }
  }

  it('深色只换文字色，不调亮 --high（它是白字按钮与角标的底色）', () => {
    expect(dark.high).toBe(light.high)
    expect(darkBlock).toMatch(/--high-fg:/)
    expect(darkBlock).toMatch(/--low-fg:/)
    expect(darkBlock).toMatch(/--mid-fg:/)
  })

  it('横幅、标签、错误文字用 -fg 文字色', () => {
    const rule = (sel: string) => new RegExp(sel.replace(/[.]/g, '\\.') + ' \\{[^}]*\\}').exec(css)?.[0] ?? ''
    expect(rule('.banner.warn')).toContain('color: var(--high-fg)')
    expect(rule('.banner.ok')).toContain('color: var(--low-fg)')
    expect(rule('.banner.info')).toContain('color: var(--mid-fg)')
    expect(rule('.tag.high')).toContain('color: var(--high-fg)')
    expect(rule('.tag.low')).toContain('color: var(--low-fg)')
    expect(rule('.tag.mid')).toContain('color: var(--mid-fg)')
    expect(rule('.tag.plain')).toContain('color: var(--plain-fg)')
    expect(rule('.error')).toContain('color: var(--high-fg)')
    expect(rule('.banner.loud')).toContain('var(--high-fg)')
  })
})
