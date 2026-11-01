import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { agentReasonsEn } from './format'

// 守护进程写进审计与历史的固定理由，网页（format.ts 的 agentReasonsEn）和命令行（internal/i18n/en.go）各有一份英文。
// 两边必须一字不差：终端里看到的和手机上看到的是同一句。这里从 en.go 里把同名的键读出来逐条比对，防止以后各改各的。
function goCatalog(): Record<string, string> {
  const src = readFileSync(join(__dirname, '..', '..', 'internal', 'i18n', 'en.go'), 'utf8')
  const out: Record<string, string> = {}
  const re = /^\s*"((?:\\.|[^"\\])*)":\s*"((?:\\.|[^"\\])*)",?\s*(?:\/\/.*)?$/gm
  let m: RegExpExecArray | null
  const unescape = (s: string) => s.replace(/\\(.)/g, (_, c: string) => (c === 'n' ? '\n' : c === 't' ? '\t' : c))
  while ((m = re.exec(src))) out[unescape(m[1] ?? '')] = unescape(m[2] ?? '')
  return out
}

describe('守护进程理由的英文两边一致', () => {
  it('format.ts 的 agentReasonsEn 与 internal/i18n/en.go 同键同值', () => {
    const go = goCatalog()
    expect(Object.keys(go).length).toBeGreaterThan(100)
    const diffs: string[] = []
    for (const [zh, web] of Object.entries(agentReasonsEn)) {
      if (zh in go && go[zh] !== web) diffs.push(`${zh}\n  web: ${web}\n  go:  ${go[zh]}`)
    }
    expect(diffs).toEqual([])
  })
})
