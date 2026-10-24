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
const name = (v: string) => (v.startsWith('#') ? v : `--${v}`)

// ---- 从样式表推出所有“文字色 / 背景色”组合 ----

interface CssRule {
  selector: string
  decls: Record<string, string>
}

/** 拆出每条规则（含 @media 里的）；变量定义（:root）与 * 不算。 */
function parseRules(text: string): CssRule[] {
  const out: CssRule[] = []
  for (const m of text.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/([^{}]+)\{([^{}]*)\}/g)) {
    const selector = m[1]!.trim()
    if (selector === ':root' || selector === '*') continue
    const decls: Record<string, string> = {}
    for (const d of m[2]!.split(';')) {
      const i = d.indexOf(':')
      if (i > 0) decls[d.slice(0, i).trim()] = d.slice(i + 1).trim()
    }
    out.push({ selector, decls })
  }
  return out
}

/** var(--x) 回 x，#rrggbb、#rgb 回小写六位；inherit、currentColor、none、rgba(…) 等不是一个确定的颜色，回 null。 */
function token(v: string | undefined): string | null {
  if (!v) return null
  const m = /^var\(--([\w-]+)\)$/.exec(v)
  if (m) return m[1]!
  if (/^#[0-9a-f]{6}$/i.test(v)) return v.toLowerCase()
  if (/^#[0-9a-f]{3}$/i.test(v)) return '#' + [...v.slice(1).toLowerCase()].map((c) => c + c).join('')
  return null
}

/** 有底色、但不是放字的元素（状态圆点），它们的底色不是文字的背景。 */
const nonText = new Set(['.dot', '.dot.on'])

interface Pair {
  fg: string
  bg: string
  why: string
}

/**
 * 推导规则：
 *  1. 同一条规则里同时写了文字色与底色：就是这一组。
 *  2. 只写底色、不写文字色（或文字色是 inherit）的规则是“表面”：上面的字继承页面的 --fg，检查 --fg 对这个底色。
 *  3. 只写文字色、不写底色的规则：字落在哪个表面上取决于 DOM，这里保守地对每一个表面都检查。
 * 局限：某条只写文字色的规则若被用在有自己底色的容器（横幅、标签）里，推不出来；这种用法要用那个容器的 -fg 色。
 */
function derivePairs(rules: CssRule[]): { pairs: Pair[]; surfaces: string[] } {
  const surfaces = new Set<string>()
  const pairs: Pair[] = []
  const textOnly: CssRule[] = []
  for (const r of rules) {
    if (nonText.has(r.selector)) continue
    const fg = token(r.decls['color'])
    const bg = token(r.decls['background'] ?? r.decls['background-color'])
    if (fg && bg) pairs.push({ fg, bg, why: r.selector })
    else if (bg) {
      surfaces.add(bg)
      pairs.push({ fg: 'fg', bg, why: `${r.selector}（继承的文字色）` })
    } else if (fg) textOnly.push(r)
  }
  for (const r of textOnly) for (const bg of surfaces) pairs.push({ fg: token(r.decls['color'])!, bg, why: `${r.selector}（在 ${name(bg)} 上）` })
  return { pairs, surfaces: [...surfaces].sort() }
}

const derived = derivePairs(parseRules(css))

describe('颜色对比度：从 app.css 推出的每一组（WCAG AA，普通文字 ≥ 4.5）', () => {
  it('推导确实拆到了规则（防止解析坏了全部空过）', () => {
    expect(derived.surfaces).toEqual(['bg', 'card'])
    expect(derived.pairs.length).toBeGreaterThan(40)
    const has = (fg: string, bg: string, sel: string) => derived.pairs.some((p) => p.fg === fg && p.bg === bg && p.why.startsWith(sel))
    expect(has('brand-text', 'bg', '.nav a.active')).toBe(true)
    expect(has('muted', 'card', '.nav a')).toBe(true)
    expect(has('#ffffff', 'high', '.nav .badge')).toBe(true)
    expect(has('brand-fg', 'brand', '.btn.primary')).toBe(true)
    expect(has('fg', 'card', '.card')).toBe(true)
  })

  for (const [theme, t] of [
    ['浅色', light],
    ['深色', dark],
  ] as const) {
    for (const p of derived.pairs) {
      it(`${theme}：${p.why} —— ${name(p.fg)} / ${name(p.bg)}`, () => {
        const f = color(t, p.fg)
        const b = color(t, p.bg)
        expect(f, `缺少 --${p.fg}`).toBeDefined()
        expect(b, `缺少 --${p.bg}`).toBeDefined()
        expect(ratio(f!, b!)).toBeGreaterThanOrEqual(4.5)
      })
    }
  }
})

/** [文字, 背景, 用在哪]：上面推导之外，再按用途写明最要紧的几组（推导覆盖它们，这里是给人看的说明）。 */
const pairs: [string, string, string][] = [
  ['high-fg', 'high-bg', '红色横幅、含隐藏部分的告警、没有生效的结果提示、高风险标签'],
  ['low-fg', 'low-bg', '绿色横幅、成功的结果提示、低风险标签'],
  ['mid-fg', 'mid-bg', '信息横幅（断线、暂停）、中风险标签'],
  ['high-fg', 'bg', '.error（页面上）、删除与吊销按钮的红字'],
  ['high-fg', 'card', '.error（面板的草案框里）、删除与吊销按钮的红字（按钮底色）'],
  ['plain-fg', 'line', '种类标签（等你回答、webhook）、来源标签'],
  ['fg', 'card', '横幅与提示里的按钮'],
  ['#ffffff', 'high', '拒绝按钮、导航角标（白字压在 --high 上）'],
  ['brand-text', 'bg', '底部导航当前标签页'],
  ['muted', 'bg', '次要文字（时间、说明、已吊销的机器名）'],
  ['muted', 'card', '卡片里的次要文字'],
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
    expect(darkBlock).toMatch(/--brand-text:/)
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

  it('推导器自测：同规则成对、表面配继承色、只写文字色的对每个表面', () => {
    const d = derivePairs(
      parseRules(`
        /* 注释 { 不算 } */
        :root { --fg: #000000; }
        @media (prefers-color-scheme: dark) { :root { --fg: #ffffff; } }
        .a { color: var(--x); background: var(--y); }
        .s1 { background: var(--bg); }
        .s2 { color: inherit; background-color: var(--card); }
        .dot { background: var(--muted); }
        .t { color: #FFF; }
        .n { background: none; color: var(--z); }
        .r { background: rgba(0,0,0,0.4); }
      `),
    )
    expect(d.surfaces).toEqual(['bg', 'card'])
    const got = d.pairs.map((p) => `${p.fg}/${p.bg}`).sort()
    expect(got).toEqual(['#ffffff/bg', '#ffffff/card', 'fg/bg', 'fg/card', 'x/y', 'z/bg', 'z/card'].sort())
  })
})
