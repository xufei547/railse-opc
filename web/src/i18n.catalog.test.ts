import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'
import { describe, expect, it } from 'vitest'
import { en } from './locales/en'

// 扫 src 下所有非测试 .ts/.tsx 里的 t('…') / t("…") / t(`…`) 调用，核对英文表：
//   - 第一个参数不是字面量（变量、拼接、带 ${} 的模板串）→ 报错；
//   - 键不在 en 里 → 报错并列出；
//   - en 里没人用的键 → 只打印。
// 只认裸的 t(：前面不能是字母、数字、点、$、下划线（避开 .at(、split( 之类）。

const root = join(__dirname)

function files(dir: string, out: string[] = []): string[] {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name)
    if (statSync(p).isDirectory()) {
      if (name === 'locales') continue
      files(p, out)
    } else if (/\.(ts|tsx)$/.test(name) && !/\.test\.tsx?$/.test(name) && name !== 'i18n.ts' && name !== 'test-setup.ts') {
      out.push(p)
    }
  }
  return out
}

function keysIn(src: string, file: string, problems: string[]): string[] {
  const keys: string[] = []
  const re = /(?<![\w.$])t\(\s*/g
  let m: RegExpExecArray | null
  while ((m = re.exec(src))) {
    const at = m.index + m[0].length
    const q = src[at]
    if (q !== "'" && q !== '"' && q !== '`') {
      problems.push(`${file}:${line(src, m.index)} t() 的第一个参数不是字符串字面量`)
      continue
    }
    let i = at + 1
    let s = ''
    let bad = false
    let closed = false
    for (; i < src.length; i++) {
      const c = src[i]
      if (c === '\\') {
        const n = src[i + 1]
        if (n === 'u' && /^[0-9a-fA-F]{4}$/.test(src.slice(i + 2, i + 6))) {
          s += String.fromCharCode(parseInt(src.slice(i + 2, i + 6), 16))
          i += 5
          continue
        }
        s += n === 'n' ? '\n' : n === 't' ? '\t' : n
        i++
        continue
      }
      if (c === q) {
        closed = true
        break
      }
      if (q === '`' && c === '$' && src[i + 1] === '{') {
        problems.push(`${file}:${line(src, m.index)} t() 的键不能是带 \${} 的模板串，用 {name} 占位加 vars`)
        bad = true
        break
      }
      s += c
    }
    if (bad || !closed) continue
    // 收尾引号后的第一个非空白字符必须是 , 或 )，否则是拼接之类
    const rest = src.slice(i + 1).match(/^\s*(.)/)
    if (!rest || (rest[1] !== ',' && rest[1] !== ')')) {
      problems.push(`${file}:${line(src, m.index)} t() 的第一个参数不是单个字符串字面量（不要拼接）`)
      continue
    }
    if (s) keys.push(s)
  }
  return keys
}

function line(src: string, idx: number): number {
  return src.slice(0, idx).split('\n').length
}

describe('英文表完整', () => {
  it('每个 t() 的键都在 en 里，且都是字面量', () => {
    const problems: string[] = []
    const used = new Map<string, string>()
    for (const f of files(root)) {
      const rel = relative(root, f)
      for (const k of keysIn(readFileSync(f, 'utf8'), rel, problems)) {
        if (!used.has(k)) used.set(k, rel)
      }
    }
    expect(used.size).toBeGreaterThan(0)
    const missing = [...used].filter(([k]) => !(k in en)).map(([k, f]) => `${JSON.stringify(k)}  // ${f}`)
    if (missing.length) problems.push(`英文表缺 ${missing.length} 条，请补进 src/locales/en.ts：\n${missing.join('\n')}`)
    const unused = Object.keys(en).filter((k) => !used.has(k))
    if (unused.length) console.log(`英文表里没人用的键（${unused.length}）：\n${unused.map((k) => JSON.stringify(k)).join('\n')}`)
    expect(problems).toEqual([])
  })
})

describe('keysIn 自测', () => {
  it('认字面量键，拒绝拼接、模板串、非字面量，不误认 x.t( / split(', () => {
    const src = [
      "t('a')",
      "t('b {n}', { n: 1 })",
      "t('c' + d)",
      't(`e ${x}`)',
      't(v)',
      "x.t('f')",
      "s.split('g')",
      "t('h\\u00e9')",
    ].join('\n')
    const problems: string[] = []
    const keys = keysIn(src, 'x.ts', problems)
    expect(keys).toEqual(['a', 'b {n}', 'h\u00e9'])
    expect(problems).toHaveLength(3)
    expect(problems[0]).toContain('x.ts:3')
    expect(problems[0]).toContain('不要拼接')
    expect(problems[1]).toContain('x.ts:4')
    expect(problems[2]).toContain('x.ts:5')
  })
})
