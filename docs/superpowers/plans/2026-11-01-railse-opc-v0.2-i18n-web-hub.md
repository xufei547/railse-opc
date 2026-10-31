# Railse OPC v0.2 手机网页与 hub 英文面（网页 + hub 侧）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 英文系统的手机打开 Railse 网页，从登录到收件箱、请求页、答题、历史、规则、机器、设置，加上 hub 推来的通知，全程不见中文；中文用户什么都不变。顺手把答题页的"回答"按钮做成贴底、带所选答案的样子。

**Architecture:** 网页新建 `web/src/i18n.ts`：`t(zh, vars?)` 以**中文原文当键**查 `web/src/locales/en.ts`，查不到原样返回；语言按 `localStorage` 里的选择（`railse.lang`：`auto`/`zh`/`en`）→ `navigator.language` 解析，在 `main.tsx` 里同步初始化一次，切换语言时保存、重提交推送订阅、整页刷新。拼句子的函数（`outcomeText`、`explainRequest` 等）按 `lang()` 各写一条分支。推送文案由 hub 生成，所以订阅时把语言交给 hub：`POST /api/push/subscribe` 带 `lang`，`push_subscriptions` 加 `lang` 列（库升第 4 版），推送与测试通知按每条订阅的语言组文案。词典完整性由一个 vitest 测试把关。Playwright 加 `en` 项目跑一条专门的英文用例。

**Tech Stack:** React 19 + TypeScript + Vite，vitest 4（`environment: 'node'`，组件用 `renderToStaticMarkup`），Playwright；Go（`internal/hub`、`internal/hub/store`，SQLite）。

## Global Constraints

- 设计：`docs/superpowers/specs/2026-11-01-railse-opc-v0.2-english-launch-design.md` §1.3、§1.4、§1.6、§2、§3。
- **现有测试断言中文一律保持**：vitest 加 `setupFiles`，把语言钉成 `zh`；Go 的 hub 测试里原有的 `pushMessage` 断言照旧（加 `"zh"` 参数即可）。新的英文用例在用例内 `setLang('en')`，`afterEach` 恢复。
- `t()` 的第一个参数**必须是字符串字面量**（单引号、双引号或不含 `${` 的反引号）；变量请用 `lang()` 分支或表查找。完整性测试（Task 2）扫出所有键核对 `en.ts`，缺一条就红。
- 带变量的句子用 `{name}` 占位：`t('还有 {n} 题没答', { n })`，英文值保持同样的占位名。
- `i18n.ts` 在 node 测试环境下 import 不得触碰 `document`、`localStorage`、`navigator`（都用 `typeof` 守着）。
- 术语表与 Go 侧 `internal/i18n/en.go` 文件头相同：批准 approve · 拒绝 deny · 记住 remember · 收件箱 inbox · hub hub · 守护进程 daemon · 钩子 hook · 规则 rule · 项目 project · 机器 machine · 接入码 enroll token · 体检 health check · 暂停自动审批 auto-approval paused · 交回终端 handed back to the terminal · 等你回答 waiting for your answer · 自己填 Your own answer · 让它在终端问我 Ask me in the terminal instead。
- 英文语气与 README.en 一致：短句、说人话、不吹、不加感叹号。
- **不翻**：hub 接口返回的错误句子（`writeErr`，网页里偶尔弹出）、hub 与守护进程日志、`engine_reason` 的原文（网页解析它的中文前缀组句，解析逻辑保持匹配中文，只改输出）。
- 推送订阅的 `lang` 只有 `zh`、`en` 两个值；请求体缺 `lang` 或为空按 `zh`（旧网页、旧 Service Worker）；`zh*` 为 zh，其余为 en。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `web/src/i18n.ts`（新） | `Lang`、`LangChoice`、`langKey`、`normalize`、`resolveLang`、`readChoice`、`saveChoice`、`initLang`、`setLang`、`lang`、`t`、`htmlLang` |
| `web/src/locales/en.ts`（新） | `export const en: Record<string, string>`，文件头放术语表 |
| `web/src/i18n.test.ts`、`web/src/i18n.catalog.test.ts`（新） | 解析与回退；完整性门禁 |
| `web/src/test-setup.ts`（新）+ `web/vite.config.ts` | vitest 钉死中文 |
| `web/src/main.tsx`、`web/index.html`、`web/public/manifest.webmanifest`、`web/public/sw.js` | 初始化语言；manifest 名字与描述；SW 的兜底正文与重订阅带语言 |
| `web/src/api.ts`、`web/src/push.ts` | 订阅带 `lang`；切换语言时重提交 |
| `web/src/pages/Settings.tsx` | "语言 / Language"区块 |
| `web/src/format.ts`、`explain.ts`、`history.ts`、`decide.ts`、`manage.ts`、`push.ts` 的文案表 | 拼句子的函数按语言分支；表改成按语言取 |
| 其余页面与组件 | 静态句子包 `t()` |
| `web/src/components/AskForm.tsx`、`pages/Request.tsx`、`app.css` | 贴底的答题按钮，标签带所选答案 |
| `web/playwright.config.ts`、`web/e2e/en.spec.ts`（新）、`web/e2e/fixtures.ts` | `en` 项目与英文用例 |
| `internal/hub/store/schema.sql`、`store.go`、`push.go`、`migrate_test.go`、`push_test.go` | 库升第 4 版：`push_subscriptions.lang` |
| `internal/hub/api_push.go`、`pushsvc.go`、`pushsvc_test.go` | 订阅收 `lang`；推送与测试通知按语言组文案 |
| `docs/web.md`、`docs/hub.md`、`README.md`、`README.en.md` | 语言开关、订阅 `lang`、库版本说明 |

---

### Task 1: `web/src/i18n.ts`、英文表、vitest 钉死中文

**Files:**
- Create: `web/src/i18n.ts`、`web/src/locales/en.ts`、`web/src/i18n.test.ts`、`web/src/test-setup.ts`
- Modify: `web/vite.config.ts:36`

**Interfaces:**
- Produces:
  - `type Lang = 'zh' | 'en'`、`type LangChoice = 'auto' | Lang`、`const langKey = 'railse.lang'`
  - `normalize(tag: string | null | undefined): Lang`
  - `resolveLang(choice: LangChoice, navigatorLang: string | undefined): Lang`
  - `readChoice(storage: Pick<Storage, 'getItem'> | null | undefined): LangChoice`
  - `saveChoice(storage: Pick<Storage, 'setItem' | 'removeItem'> | null | undefined, choice: LangChoice): void`
  - `initLang(): Lang`（浏览器里调用一次）、`setLang(l: Lang): void`、`lang(): Lang`、`htmlLang(l: Lang): string`
  - `t(zh: string, vars?: Record<string, string | number>): string`

- [ ] **Step 1: 写失败的测试** `web/src/i18n.test.ts`

```ts
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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd /Users/xufei/RailseWork/web && npx vitest run src/i18n.test.ts`
Expected: 找不到模块 `./i18n`。

- [ ] **Step 3: 实现**

`web/src/i18n.ts`：

```ts
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
```

`web/src/locales/en.ts`：

```ts
// 英文表：键是代码里的中文原文（含 {name} 占位与标点），值是英文。语气与 README.en 一致：短句、说人话、不吹。
// 术语表（与 internal/i18n/en.go 相同）：批准 approve · 拒绝 deny · 记住 remember · 收件箱 inbox · hub hub ·
// 守护进程 daemon · 钩子 hook · 规则 rule · 项目 project · 机器 machine · 接入码 enroll token · 体检 health check ·
// 暂停自动审批 auto-approval paused · 交回终端 handed back to the terminal · 等你回答 waiting for your answer ·
// 自己填 Your own answer · 让它在终端问我 Ask me in the terminal instead
export const en: Record<string, string> = {
  正在连接hub: 'Connecting to the hub…',
}
```

（上面这条只是占位让文件非空，Task 7 会把 `App.tsx` 里 `正在连接 hub…` 的真实键放进来并删掉它。）

`web/src/test-setup.ts`：

```ts
// vitest 全局：现有测试都断言中文，把语言钉死为 zh；英文用例在用例里 setLang('en') 并在 afterEach 恢复。
import { setLang } from './i18n'

setLang('zh')
```

`web/vite.config.ts` 第 36 行改为：

```ts
  test: { environment: 'node', include: ['src/**/*.test.ts', 'src/**/*.test.tsx'], setupFiles: ['src/test-setup.ts'] },
```

- [ ] **Step 4: 跑测试**

Run: `cd /Users/xufei/RailseWork/web && npx vitest run src/i18n.test.ts && npx tsc --noEmit`
Expected: PASS，类型检查通过。

- [ ] **Step 5: 提交**

```bash
cd /Users/xufei/RailseWork && git add web/src/i18n.ts web/src/locales/en.ts web/src/i18n.test.ts web/src/test-setup.ts web/vite.config.ts
git commit -m "web: i18n：中文原文当键的 t()，语言按本机选择与浏览器语言解析；vitest 钉死中文"
```

### Task 2: 完整性测试（网页侧词典门禁）

**Files:**
- Create: `web/src/i18n.catalog.test.ts`

- [ ] **Step 1: 写测试**

```ts
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
    for (; i < src.length; i++) {
      const c = src[i]
      if (c === '\\') {
        const n = src[i + 1]
        s += n === 'n' ? '\n' : n === 't' ? '\t' : n
        i++
        continue
      }
      if (c === q) break
      if (q === '`' && c === '$' && src[i + 1] === '{') {
        problems.push(`${file}:${line(src, m.index)} t() 的键不能是带 \${} 的模板串，用 {name} 占位加 vars`)
        s = ''
        break
      }
      s += c
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
```

- [ ] **Step 2: 跑**

Run: `cd /Users/xufei/RailseWork/web && npx vitest run src/i18n.catalog.test.ts`
Expected: FAIL：`used.size` 为 0（还没有任何 `t()` 调用）。Task 5 起转绿。

- [ ] **Step 3: 先不单独提交**

这个测试在 Task 5 加进第一个 `t()` 调用之前一直是红的；为了分支上每个提交都能过 `npm run check`，把它和 Task 5 一起提交（Task 5 Step 5 的 `git add web` 会带上它）。Task 3、4 是 Go 侧，不受影响。

### Task 3: hub 库升第 4 版：`push_subscriptions.lang`；订阅接口收 `lang`；测试通知按语言

**Files:**
- Modify: `internal/hub/store/schema.sql:2,97-106`、`internal/hub/store/store.go:21,25-43`、`internal/hub/store/push.go:13-27,34,38-53,56-69`
- Modify: `internal/hub/store/migrate_test.go:129,301`（3 → 4）+ 新用例
- Modify: `internal/hub/store/push_test.go`、`internal/hub/auth_test.go`、`internal/hub/pushsvc_test.go`、`internal/hub/store/machines_test.go`（`UpsertPushSubscription` 加第五个参数 `"zh"`）
- Modify: `internal/hub/api_push.go:21,51-57,66-109,149-182`

**Interfaces:**
- Produces: `store.PushSubscription.Lang string`；`func (s *Store) UpsertPushSubscription(endpoint, p256dh, auth, userAgent, lang string) (PushSubscription, error)`；`func normalizeLang(s string) string`（`internal/hub/pushsvc.go`，空与 `zh*` → `"zh"`，其余 `"en"`）；`func testPushMessage(lang string) []byte`。

- [ ] **Step 1: 写失败的测试**

`internal/hub/store/migrate_test.go` 末尾：

```go
// 旧库（v2 固定快照）里已有的订阅升到最新后 lang 是 zh：作者手机那条不受影响。
func TestMigrateKeepsOldSubscriptionsChinese(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.db")
	raw := openRaw(t, path) // 本文件里 TestMigrateV2ToV3KeepsData 用的那个辅助；名字以文件里为准
	execFile(t, raw, "testdata/schema_v2.sql")
	if _, err := raw.Exec(`INSERT INTO push_subscriptions(id, endpoint, keys, user_agent, created_at) VALUES('s1','https://p.example/1','{"p256dh":"a","auth":"b"}','ua','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sub, err := st.GetPushSubscription("https://p.example/1")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Lang != "zh" {
		t.Fatalf("旧订阅升级后 lang = %q，想要 zh", sub.Lang)
	}
}
```

`internal/hub/store/push_test.go` 加：

```go
func TestUpsertPushSubscriptionLang(t *testing.T) {
	st := openTest(t) // 本文件里已有的辅助
	s1, err := st.UpsertPushSubscription("https://p.example/1", p256dh, auth, "ua", "en")
	if err != nil || s1.Lang != "en" {
		t.Fatalf("lang = %q, %v", s1.Lang, err)
	}
	s2, err := st.UpsertPushSubscription("https://p.example/1", p256dh, auth, "ua", "zh")
	if err != nil || s2.Lang != "zh" || s2.ID != s1.ID {
		t.Fatalf("同一 endpoint 重登记要改 lang 且不换 id：%+v, %v", s2, err)
	}
}
```

`internal/hub/pushsvc_test.go` 加：

```go
func TestNormalizeLang(t *testing.T) {
	for in, want := range map[string]string{"": "zh", "zh": "zh", "zh-CN": "zh", "ZH-tw": "zh", "en": "en", "en-US": "en", "fr": "en", " en ": "en"} {
		if got := normalizeLang(in); got != want {
			t.Errorf("normalizeLang(%q) = %q，想要 %q", in, got, want)
		}
	}
}

func TestTestPushMessageByLang(t *testing.T) {
	decode := decodePush(t)
	zh := decode(testPushMessage("zh"), nil)
	en := decode(testPushMessage("en"), nil)
	if zh.Body != "通知正常" || en.Body != "Notifications are working" || zh.Title != "Railse" || en.Title != "Railse" {
		t.Fatalf("zh=%+v en=%+v", zh, en)
	}
}
```

（`decode` 的参数形状按本文件 504–516 行的 `decodePush` 为准。）

- [ ] **Step 2: 跑确认失败**

Run: `cd /Users/xufei/RailseWork && RAILSE_LANG=zh go test ./internal/hub/... -run 'TestMigrateKeepsOldSubscriptionsChinese|TestUpsertPushSubscriptionLang|TestNormalizeLang|TestTestPushMessageByLang' 2>&1 | tail -5`
Expected: 编译失败（`Lang` 未定义、参数个数不对）。

- [ ] **Step 3: 实现**

`schema.sql`：第 2 行注释改"版本 4"；`push_subscriptions` 表尾加一列（放最后，和迁移加列顺序一致）：

```sql
  first_failed_at TEXT,
  lang          TEXT NOT NULL DEFAULT 'zh'   -- 推送文案语言：zh、en（v4）
```

`store.go`：`const SchemaVersion = 4`；`migrations` 加：

```go
	// 4：push_subscriptions 加 lang（推送文案语言），旧订阅为 zh。
	4: `
ALTER TABLE push_subscriptions ADD COLUMN lang TEXT NOT NULL DEFAULT 'zh';
`,
```

`push.go`：结构体加 `Lang string`；`pushCols` 末尾加 `, lang`；Upsert：

```go
func (s *Store) UpsertPushSubscription(endpoint, p256dh, auth, userAgent, lang string) (PushSubscription, error) {
	// …keys 的编码照旧…
	p, err := scanPush(s.db.QueryRow(`INSERT INTO push_subscriptions(id, endpoint, keys, user_agent, created_at, lang) VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET keys = excluded.keys, user_agent = excluded.user_agent, lang = excluded.lang, failures = 0, first_failed_at = NULL
		RETURNING `+pushCols, NewID(), endpoint, string(keys), userAgent, Now(), lang))
```

`scanPush` 末尾加 `&p.Lang`。`migrate_test.go` 里两处 `3` 改 `4`；`UpsertPushSubscription` 的 19 处测试调用末尾加 `, "zh"`：

```bash
cd /Users/xufei/RailseWork && grep -rln 'UpsertPushSubscription(' internal/hub --include='*_test.go' | xargs sed -i '' -E 's/(UpsertPushSubscription\([^)]*)\)/\1, "zh")/g'
```

（跑完 `grep -n 'UpsertPushSubscription(' internal/hub -r --include='*_test.go'` 肉眼看一遍，嵌套括号的调用手改。）

`pushsvc.go` 加：

```go
// normalizeLang 把订阅时网页或 Service Worker 给的语言标签收成 zh / en：空（旧网页、旧 SW）与 zh* 为 zh，其余 en。
func normalizeLang(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" || strings.HasPrefix(s, "zh") {
		return "zh"
	}
	return "en"
}
```

`api_push.go`：`subscribeBody` 加 `Lang string \`json:"lang"\``；`UpsertPushSubscription(body.Endpoint, body.Keys.P256dh, body.Keys.Auth, truncate(r.UserAgent(), pushUserAgentMax), normalizeLang(body.Lang))`；第 21 行的固定字节换成函数：

```go
// testPushMessage 是“发一条测试通知”的载荷，按订阅的语言组正文。
func testPushMessage(lang string) []byte {
	body := "通知正常"
	if lang == "en" {
		body = "Notifications are working"
	}
	b, _ := json.Marshal(pushPayload{ID: "test", Kind: "test", Title: "Railse", Body: body, URL: "/"})
	return b
}
```

`handlePushTest` 第 172 行发送 `testPushMessage(normalizeLang(sub.Lang))`。

- [ ] **Step 4: 跑测试**

Run: `cd /Users/xufei/RailseWork && RAILSE_LANG=zh go test ./internal/hub/... ./internal/doctor/ -count=1`
Expected: PASS（`TestMigratedPushSchemaMatchesFresh` 核对了迁移后与新建库列序一致；doctor 比较 `store.SchemaVersion` 自动跟上）。

- [ ] **Step 5: 提交**

```bash
cd /Users/xufei/RailseWork && git add internal/hub
git commit -m "hub: push_subscriptions 加 lang（库升 v4），订阅接口收 lang，测试通知按订阅语言"
```

### Task 4: 推送文案按订阅语言

**Files:**
- Modify: `internal/hub/pushsvc.go:463-613`（`cliNames`、`riskVerbs`、`cliName`、`riskVerb`、`message`、`pushMessage`、`askSummary`）与 `deliver` 272–330
- Modify: `internal/hub/pushsvc_test.go`（现有 `pushMessage(perm, pushWho{...})` 调用加 `"zh"`；新加英文表格用例）

**Interfaces:**
- Produces: `func pushMessage(r store.Request, w pushWho, lang string) ([]byte, error)`；`func (p *pushService) message(r store.Request, lang string) ([]byte, error)`；`func riskVerb(risk, lang string) string`；`func askSummary(a *proto.Ask, lang string) string`。

- [ ] **Step 1: 写失败的测试**（`pushsvc_test.go`）

```go
func TestPushMessageEnglish(t *testing.T) {
	decode := decodePush(t)
	perm := store.Request{ID: "r1", Kind: "permission", Risk: "network", ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "curl https://example.com"}}
	m := decode(pushMessage(perm, pushWho{Tool: "claude", Project: "blog", Machine: "xufeideMacBook-Air.local"}, "en"))
	if m.Title != "Claude Code wants to use the network · blog" || m.Body != "curl https://example.com\nOpen to approve or deny" {
		t.Fatalf("%+v", m)
	}
	ask := store.Request{ID: "r2", Kind: "ask", Ask: &proto.Ask{Questions: []proto.Question{{Question: "Plan A or B?"}, {Question: "q2"}, {Question: "q3"}}}}
	m = decode(pushMessage(ask, pushWho{Tool: "claude", Project: "blog"}, "en"))
	if m.Title != "Claude Code is asking you · blog" || m.Body != "Plan A or B? and 2 more\nOpen to answer" {
		t.Fatalf("%+v", m)
	}
	q := store.Request{ID: "r3", Kind: "question", Title: "what next?"}
	m = decode(pushMessage(q, pushWho{Tool: "codex", Project: "blog"}, "en"))
	if m.Title != "Codex is waiting for you in the terminal · blog" {
		t.Fatalf("%+v", m)
	}
	wh := store.Request{ID: "r4", Kind: "webhook", Title: "deploy?"}
	m = decode(pushMessage(wh, pushWho{}, "en"))
	if m.Title != "A workflow is waiting for your approval" {
		t.Fatalf("%+v", m)
	}
}

func TestPushMessageEnglishVerbs(t *testing.T) {
	decode := decodePush(t)
	want := map[string]string{
		"read":             "Claude Code wants to read files",
		"write_in_project": "Claude Code wants to edit files in the project",
		"unknown":          "Claude Code wants to run an unfamiliar command",
		"write_outside":    "Claude Code wants to edit files outside the project",
		"network":          "Claude Code wants to use the network",
		"outward":          "Claude Code wants to push or publish",
		"destructive":      "Claude Code wants to do something irreversible",
		"":                 "Claude Code wants to do something",
	}
	for risk, title := range want {
		m := decode(pushMessage(store.Request{ID: "r", Kind: "permission", Risk: risk, ToolName: "x"}, pushWho{Tool: "claude"}, "en"))
		if m.Title != title {
			t.Errorf("risk %q: title %q，想要 %q", risk, m.Title, title)
		}
	}
}
```

（`store.Request` 的字段名以 `internal/hub/store/requests.go:36-62` 为准；`Ask` 字段是 `*proto.Ask` 还是 JSON 串，按现有 `TestPushMessageAsk` 的写法构造。）

- [ ] **Step 2: 跑确认失败**

Run: `cd /Users/xufei/RailseWork && RAILSE_LANG=zh go test ./internal/hub/ -run 'TestPushMessageEnglish' 2>&1 | tail -3`
Expected: 编译失败（参数个数）。

- [ ] **Step 3: 实现**

```go
// pushCopy 是一种语言下推送文案的零件。
type pushCopy struct {
	wants, asking, waitingTerminal, webhook string // 标题：<CLI> + wants + 动词 / <CLI> + asking / <CLI> + waitingTerminal / webhook 整句
	tapApprove, tapAnswer                   string // 正文第二行
	verbs                                   map[string]string
	fallbackVerb, agent                     string
	more                                    func(n int) string // 多题时的后缀
}

var pushCopies = map[string]pushCopy{
	"zh": {
		wants: " 要", asking: " 在问你", waitingTerminal: " 在终端等你回答", webhook: "工作流等你批准",
		tapApprove: "点开批准或拒绝", tapAnswer: "点开回答",
		verbs: map[string]string{"network": "联网", "outward": "往外发", "destructive": "做不可逆的操作", "write_outside": "改项目外的文件",
			"unknown": "跑一个没见过的命令", "write_in_project": "改项目里的文件", "read": "读文件"},
		fallbackVerb: "做一件事", agent: "Agent",
		more: func(n int) string { return fmt.Sprintf(" 等 %d 题", n) },
	},
	"en": {
		wants: " wants to ", asking: " is asking you", waitingTerminal: " is waiting for you in the terminal", webhook: "A workflow is waiting for your approval",
		tapApprove: "Open to approve or deny", tapAnswer: "Open to answer",
		verbs: map[string]string{"network": "use the network", "outward": "push or publish", "destructive": "do something irreversible", "write_outside": "edit files outside the project",
			"unknown": "run an unfamiliar command", "write_in_project": "edit files in the project", "read": "read files"},
		fallbackVerb: "do something", agent: "Agent",
		more: func(n int) string { return fmt.Sprintf(" and %d more", n-1) },
	},
}

func copyFor(lang string) pushCopy {
	if c, ok := pushCopies[lang]; ok {
		return c
	}
	return pushCopies["zh"]
}

func riskVerb(risk, lang string) string {
	c := copyFor(lang)
	if v, ok := c.verbs[risk]; ok {
		return v
	}
	return c.fallbackVerb
}

func askSummary(a *proto.Ask, lang string) string {
	if a == nil || len(a.Questions) == 0 {
		return ""
	}
	first := clean(a.Questions[0].Question, pushSummaryLen)
	if n := len(a.Questions); n > 1 {
		first += copyFor(lang).more(n)
	}
	return first
}

func pushMessage(r store.Request, w pushWho, lang string) ([]byte, error) {
	c := copyFor(lang)
	machine := ""
	if w.Many {
		machine = machineLabel(w.Machine)
	}
	var title, body string
	switch r.Kind {
	case "permission":
		title = pushTitle(cliName(w.Tool)+c.wants+riskVerb(r.Risk, lang), w.Project, machine)
		summary := clean(payloadSummary(r.Payload), pushSummaryLen)
		if summary == "" {
			summary = clean(r.ToolName, pushSummaryLen)
		}
		body = joinNonEmpty("\n", summary, c.tapApprove)
	case "ask":
		title = pushTitle(cliName(w.Tool)+c.asking, w.Project, machine)
		body = joinNonEmpty("\n", askSummary(r.Ask, lang), c.tapAnswer)
	case "question":
		title = pushTitle(cliName(w.Tool)+c.waitingTerminal, w.Project, machine)
		body = clean(r.Title, pushBodyLen)
	default:
		title = c.webhook
		body = clean(r.Title, pushBodyLen)
	}
	m := pushPayload{ID: r.ID, Kind: r.Kind, Title: truncate(title, pushTitleLen), Body: truncate(body, pushBodyLen), URL: "/r/" + url.PathEscape(r.ID)}
	return encodePushPayload(m, push.MaxPlaintext)
}
```

删掉旧的 `riskVerbs` 变量与无参 `riskVerb`；`cliName` 不变（两种语言 CLI 名相同）。`message(r, lang)` 把 `lang` 传给 `pushMessage`。`deliver` 里：

```go
	msgs := map[string][]byte{}
	for _, sub := range subs {
		lang := normalizeLang(sub.Lang)
		msg, ok := msgs[lang]
		if !ok {
			var err error
			if msg, err = p.message(r, lang); err != nil {
				// 原来 message 出错时的处理照旧（记日志并返回）
			}
			msgs[lang] = msg
		}
		// …原来的 p.sendOne(ctx, sub, msg, o)…
	}
```

现有测试里 `pushMessage(x, pushWho{...})` 一律加 `, "zh"`；`riskVerb(risk)` 的调用加 `, "zh"`。

- [ ] **Step 4: 跑测试**

Run: `cd /Users/xufei/RailseWork && RAILSE_LANG=zh go test ./internal/hub/ -count=1`
Expected: PASS（中文断言一字未改）。

- [ ] **Step 5: 提交**

```bash
cd /Users/xufei/RailseWork && git add internal/hub/pushsvc.go internal/hub/pushsvc_test.go
git commit -m "hub: 推送文案按订阅语言组（zh/en），同一请求按语言各组一次"
```

### Task 5: 网页：初始化语言、订阅带 `lang`、设置页语言开关、manifest 与 SW

**Files:**
- Modify: `web/src/main.tsx`、`web/index.html:2`、`web/public/manifest.webmanifest`、`web/public/sw.js:8,173-195`
- Modify: `web/src/api.ts:110`、`web/src/push.ts:151-161,235-238`、`web/src/api.test.ts:112`、`web/src/push.test.ts`、`web/src/sw.test.ts`
- Modify: `web/src/pages/Settings.tsx`（新区块）、`web/src/pages/settings.push.test.tsx` 或新建 `web/src/pages/settings.lang.test.tsx`

**Interfaces:**
- Consumes: Task 1 全部。
- Produces: `hub.pushSubscribe(sub: unknown, lang: Lang)`；`export async function resubscribeForLang(env: PushEnv, api: PushApi): Promise<void>`（`push.ts`）；`export function LanguageView({ choice, onChange, busy }: { choice: LangChoice; onChange: (c: LangChoice) => void; busy: boolean })`（`Settings.tsx`，导出便于 SSR 测试）；`export async function switchLanguage(choice: LangChoice, deps: { storage: Pick<Storage,'setItem'|'removeItem'> | null; env: PushEnv; api: PushApi; reload: () => void; pushOn: boolean }): Promise<void>`（`Settings.tsx` 或 `push.ts`）。

- [ ] **Step 1: 写失败的测试**

`web/src/api.test.ts` 第 112 行那条改成断言订阅体带 `lang`：

```ts
    expect(JSON.parse(calls[0]?.init.body as string)).toEqual({ ...sub, lang: 'zh' })
```

新建 `web/src/pages/settings.lang.test.tsx`：

```tsx
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
    expect(html).not.toMatch(/[一-鿿]/)
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
```

- [ ] **Step 2: 跑确认失败**

Run: `cd /Users/xufei/RailseWork/web && npx vitest run src/api.test.ts src/pages/settings.lang.test.tsx`
Expected: FAIL。

- [ ] **Step 3: 实现**

`main.tsx` 在 `void registerServiceWorker()` 之前加：

```tsx
import { initLang } from './i18n'

initLang()
```

`index.html` 第 2 行保持 `lang="zh-CN"`（运行时由 `initLang` 改写）。`manifest.webmanifest`：`"name": "Railse"`，加 `"description": "Approvals and questions from your coding agents, on your phone."`。

`api.ts`：

```ts
  /** sub 是 PushSubscription.toJSON() 的原样结果；lang 是这台设备想要的推送文案语言。 */
  pushSubscribe: (sub: unknown, lang: Lang) => post<void>('/api/push/subscribe', { ...(sub as Record<string, unknown>), lang }),
```

`push.ts`：`enablePush` 里 `await api.pushSubscribe(sub.toJSON(), lang())`；新增：

```ts
/** 切换语言后把现有订阅连同新语言再登记一次（同一 endpoint 覆盖）。没有订阅就什么都不做。 */
export async function resubscribeForLang(env: Pick<PushEnv, 'getSubscription'>, api: Pick<PushApi, 'pushSubscribe'>): Promise<void> {
  const sub = await env.getSubscription()
  if (sub) await api.pushSubscribe(sub.toJSON(), lang())
}
```

测试通知的确认文案（235–238）：

```ts
  title: t('测试通知已发出'),
  text: t('几秒内这台设备应弹出“Railse：通知正常”。没有弹出的话，看看系统的专注模式与通知设置。'),
```

（这两处若是模块级常量，改成函数在用时取，不然语言初始化前就固定了。）

`Settings.tsx` 加区块与逻辑：

```tsx
import { lang, langKey, readChoice, saveChoice, setLang, t, type LangChoice } from '../i18n'
import { browserEnv, logoutDevice, resubscribeForLang, usePush, type PushControl, type PushEnv, type PushApi } from '../push'

/** 切换语言：先存，再（推送开着时）把订阅连同新语言重登记，最后整页刷新让每一处文案重算。重登记失败不拦着刷新。 */
export async function switchLanguage(
  choice: LangChoice,
  deps: { storage: Pick<Storage, 'setItem' | 'removeItem'> | null; env: Pick<PushEnv, 'getSubscription'>; api: Pick<PushApi, 'pushSubscribe'>; reload: () => void; pushOn: boolean },
): Promise<void> {
  saveChoice(deps.storage, choice)
  setLang(choice === 'auto' ? resolveLang('auto', typeof navigator === 'undefined' ? undefined : navigator.language) : choice)
  if (deps.pushOn) {
    try {
      await resubscribeForLang(deps.env, deps.api)
    } catch {
      // 下次开启通知或 SW 重订阅时会再带语言
    }
  }
  deps.reload()
}

const choices: { value: LangChoice; label: () => string }[] = [
  { value: 'auto', label: () => t('跟随系统') },
  { value: 'zh', label: () => '中文' },
  { value: 'en', label: () => 'English' },
]

export function LanguageView({ choice, onChange, busy }: { choice: LangChoice; onChange: (c: LangChoice) => void; busy: boolean }) {
  return (
    <section>
      <h2 className="section-title">{t('语言')} / Language</h2>
      <div className="choices" role="radiogroup" aria-label={t('语言')}>
        {choices.map((c) => (
          <label key={c.value} className="option">
            <input type="radio" name="lang" value={c.value} checked={choice === c.value} disabled={busy} onChange={() => onChange(c.value)} />
            <span>{c.label()}</span>
          </label>
        ))}
      </div>
      <p className="sub line">{t('通知的文字也跟着这个语言；换了之后这台设备的订阅会重新登记。')}</p>
    </section>
  )
}
```

在 `Settings` 组件里，`<NotificationView …/>` 之后放：

```tsx
          <LanguageView
            choice={readChoice(typeof localStorage === 'undefined' ? null : localStorage)}
            busy={actions.busy || leaving}
            onChange={(c) => void switchLanguage(c, { storage: localStorage, env: browserEnv, api: hub, reload: () => window.location.reload(), pushOn: push.status === 'on' })}
          />
```

（`resolveLang` 也要 import；`中文`、`English` 两个标签不翻，所以不经 `t()`。）

`sw.js` 第 8 行：

```js
const FALLBACK_BODY = /^zh/i.test(self.navigator.language || '') ? '有新消息，点开查看' : 'New message, tap to open'
```

`sw.js` 173–195 的 `resubscribe` 里 POST 的 body 改为 `JSON.stringify({ ...sub.toJSON(), lang: self.navigator.language || '' })`（hub 按 zh*/其余收成 zh/en；用户在设置里手选过的语言在订阅轮换后会退回跟随系统，可接受）。`sw.test.ts:421` 的断言同步改。

- [ ] **Step 4: 跑测试**

Run: `cd /Users/xufei/RailseWork/web && npm run check`
Expected: PASS（含 `i18n.catalog.test.ts`：把这一任务新加的 `t()` 键补进 `en.ts`：`'跟随系统': 'Follow the system'`、`'语言': 'Language'`、`'通知的文字也跟着这个语言；换了之后这台设备的订阅会重新登记。': 'Notifications use this language too; changing it re-registers this device.'`、`'测试通知已发出': 'Test notification sent'`、`'几秒内这台设备应弹出“Railse：通知正常”。没有弹出的话，看看系统的专注模式与通知设置。': 'Within a few seconds this device should show "Railse: Notifications are working". If it doesn\'t, check Focus mode and the system notification settings.'`）。

- [ ] **Step 5: 提交**

```bash
cd /Users/xufei/RailseWork && git add web
git commit -m "web: 启动时解析语言；订阅带 lang；设置页语言开关（存、重订阅、刷新）；manifest 与 SW 兜底文案"
```

### Task 6: 拼句子的函数按语言分支：`format.ts`、`explain.ts`、`history.ts`、`decide.ts`、`manage.ts`

**Files:**
- Modify: `web/src/format.ts`（表 `risks`、`states`、`kinds`、`decidedBy`、`outcomeBy`、`auditActions`、`actors`；函数 `relativeTime`、`absoluteTime`、`engineReasonLabel`、`outcomeText`、`deliveryLabel`、`auditDetail`）
- Modify: `web/src/explain.ts`（`cliLabel`、`machineLabel`、`shellAction`、`permissionWhy`、`explainRequest` 及其内部各组句函数）
- Modify: `web/src/history.ts`（`historyFilters` 改成函数、`historyTag`）、`web/src/decide.ts`（24 处）、`web/src/manage.ts`（22 处，含 `pauseCopy`、`failure`、`formatUptime`）
- Test: `web/src/format.test.ts`、`explain.test.ts`、`history.test.ts`、`decide.test.ts`、`manage.test.ts`（现有中文断言不动；每个文件各加一组英文用例）

规则：

- 静态词条表（如 `states`、`kinds`）：把值包成 `t('已批准')`，但**表要变成函数或在用时取**（模块级常量在 import 时就把语言固定了）。写法：`const states = (): Record<string, string> => ({ approved: t('已批准'), … })`，调用处 `states()[state]`。
- 拼句子的函数：用 `lang() === 'en'` 分支写完整英文句子，中文分支一字不改。例如 `outcomeText`：

```ts
export function outcomeText(r: Pick<RequestView, 'kind' | 'state' | 'decided_by' | 'rule_id'>): string {
  const { state, decided_by: by } = r
  if (r.kind === 'question' || r.kind === 'done' || state === 'pending' || state === 'recorded') return ''
  if (lang() === 'en') return outcomeTextEn(r)
  // …原中文逻辑一字不改…
}

function outcomeTextEn(r: Pick<RequestView, 'kind' | 'state' | 'decided_by' | 'rule_id'>): string {
  const { state, decided_by: by } = r
  if (by === 'user' && state === 'answered') return 'You answered'
  if (by === 'user' && state === 'passthrough' && r.kind === 'ask') return 'Handed back to the terminal to ask'
  if (by === 'user' && (state === 'approved' || state === 'denied')) {
    const verb = state === 'approved' ? 'approved' : 'denied'
    return r.rule_id ? `You ${verb} and remembered it (rule ${r.rule_id})` : `You ${verb}`
  }
  if (state === 'auto_allowed' || state === 'auto_denied') {
    const rule = r.rule_id || (by.startsWith('rule:') ? by.slice(5) : '')
    return `Rule${rule ? ` ${rule}` : ''} ${state === 'auto_allowed' ? 'allowed' : 'denied'} it automatically`
  }
  if (by === 'terminal') return state === 'cancelled' ? 'The agent moved on; nothing for you to do' : 'Handled in the terminal'
  if (by === 'timeout') return r.kind === 'webhook' ? 'Timed out; the workflow was told "denied"' : 'Timed out; the agent went back to the terminal'
  return outcomeByEn[by] ?? ''
}
```

- `engineReasonLabel` 与 `permissionWhy` 继续用中文前缀解析 `engine_reason`（`startsWith('命中规则')`、`/按 (\S+) 默认询问/`），只把输出换成英文句子。
- `relativeTime`/`absoluteTime`：英文 `just now`、`{n} min ago`、`{n} h ago`、`yesterday 14:05`、`11-01 04:49`（日期数字格式两种语言相同）。
- 英文用例每个文件至少覆盖：`outcomeText` 八个分支、`explainRequest` 的 headline/where/why/next 各一条、`historyFilters` 的十个标签、`pauseCopy`、`relativeTime` 三档；并断言 `JSON.stringify(result)` 不匹配 `/[一-鿿]/`。

- [ ] **Step 1: 先写英文用例**（按上面规则；示例，`format.test.ts` 末尾）

```ts
describe('outcomeText（英文）', () => {
  beforeEach(() => setLang('en'))
  afterEach(() => setLang('zh'))
  it('八个分支都是英文句子', () => {
    const cases: [Parameters<typeof outcomeText>[0], string][] = [
      [{ kind: 'ask', state: 'answered', decided_by: 'user', rule_id: '' }, 'You answered'],
      [{ kind: 'ask', state: 'passthrough', decided_by: 'user', rule_id: '' }, 'Handed back to the terminal to ask'],
      [{ kind: 'permission', state: 'approved', decided_by: 'user', rule_id: 'allow-curl' }, 'You approved and remembered it (rule allow-curl)'],
      [{ kind: 'permission', state: 'denied', decided_by: 'user', rule_id: '' }, 'You denied'],
      [{ kind: 'permission', state: 'auto_allowed', decided_by: 'rule:r1', rule_id: '' }, 'Rule r1 allowed it automatically'],
      [{ kind: 'permission', state: 'cancelled', decided_by: 'terminal', rule_id: '' }, 'The agent moved on; nothing for you to do'],
      [{ kind: 'permission', state: 'passthrough', decided_by: 'terminal', rule_id: '' }, 'Handled in the terminal'],
      [{ kind: 'webhook', state: 'expired', decided_by: 'timeout', rule_id: '' }, 'Timed out; the workflow was told "denied"'],
    ]
    for (const [r, want] of cases) expect(outcomeText(r)).toBe(want)
  })
})
```

- [ ] **Step 2: 跑确认失败**，然后 **Step 3: 实现**（五个文件逐个改，每改完一个跑一次 `npx vitest run src/<file>.test.ts`），**Step 4: `npm run check` 全绿**（完整性测试列出的键补进 `en.ts`），**Step 5: 提交**：

```bash
cd /Users/xufei/RailseWork && git add web/src
git commit -m "web: 处理结果、解释卡、历史筛选、审批面板与设置文案按语言分支；词条表改成按语言取"
```

### Task 7: 页面与组件的静态句子

**Files:**
- Modify: `web/src/App.tsx`（`正在连接 hub…`、`没有这个页面`、导航标签、`aria-label`）、`pages/Login.tsx`、`pages/Inbox.tsx`、`pages/Request.tsx`、`pages/Rules.tsx`、`pages/Machines.tsx`、`pages/History.tsx`、`pages/Settings.tsx`（`pushTexts` 改函数、通知区块、hub 区块、退出）、`components/DecideSheet.tsx`、`RequestAttrs.tsx`、`Payload.tsx`、`Confirm.tsx`、`ResultToast.tsx`、`RequestCard.tsx`、`Banner.tsx`、`Explain.tsx`、`AskForm.tsx`（`可多选`、`自己填`、`还有 {n} 题没答`、`正在提交…`、`让它在终端问我`、`正在交回终端…`；`回答` 在 Task 8 一起改）、`pwa.ts`、`store.ts`、`StoreProvider.tsx`、`api.ts`、`push.ts` 的剩余文案
- Test: `components/components.test.tsx`、`pages/pages.test.tsx`、`pages/inbox.test.tsx`、`pages/Login.test.tsx`（各加一条英文 SSR 用例，断言渲染结果不含 `[一-鿿]`）

规则：JSX 文本节点 `<h2>通知</h2>` → `<h2>{t('通知')}</h2>`；属性 `aria-label={`${n} 条等你处理`}` → `aria-label={t('{n} 条等你处理', { n })}`；`pushTexts` 这类模块级表改成函数 `pushTexts()`。`Login.tsx` 的"密码"标签与"登录"按钮也要翻（e2e 的 `en.spec.ts` 用 `Password`、`Log in` 找它们）。

- [ ] **Step 1: 每个页面先写一条英文 SSR 用例**（示例，`pages/Login.test.tsx`）

```tsx
it('英文下登录页没有中文', () => {
  setLang('en')
  try {
    const html = renderToStaticMarkup(<Login />)   // 若 Login 依赖 router，按本文件现有的包装方式
    expect(html).toContain('Password')
    expect(html).toContain('Log in')
    expect(html).not.toMatch(/[一-鿿]/)
  } finally {
    setLang('zh')
  }
})
```

- [ ] **Step 2: 跑确认失败**；**Step 3: 逐文件包 `t()`**；**Step 4: `npm run check` 全绿**（补 `en.ts`）；**Step 5: 提交**：

```bash
cd /Users/xufei/RailseWork && git add web/src
git commit -m "web: 页面与组件的静态文案过 t()"
```

### Task 8: 答题按钮贴底、标签带所选答案

**Files:**
- Modify: `web/src/components/AskForm.tsx:92-108`、`web/src/ask.ts`（导出 `answerLabel`）、`web/src/pages/Request.tsx:124`、`web/src/app.css`
- Modify: `web/src/components/components.test.tsx:172-185`、`web/src/ask.test.ts`、`web/e2e/ask.spec.ts`（选择器 `name: '回答', exact: true` → `name: /^回答/`）

**Interfaces:**
- Produces: `export function answerLabel(questions: Question[], s: AskState): string`（`ask.ts`）：每题都有答案时返回 `回答：<答案>`（多题用 `；` 连，每个答案裁到 20 字，多选的用 `、` 连，自己填的显示填的内容），否则 `回答`；英文 `Answer: <…>` / `Answer`。

- [ ] **Step 1: 写失败的测试**（`ask.test.ts`）

```ts
describe('answerLabel', () => {
  const fruit: Question = { question: '苹果还是香蕉？', options: [{ label: 'Apple' }, { label: 'Banana' }] }
  const free: Question = { question: '还有什么？', options: [] }
  it('没答全只写“回答”；答全了带上答案；多题用；连；自己填的裁到 20 字', () => {
    expect(answerLabel([fruit], initAsk([fruit]))).toBe('回答')
    expect(answerLabel([fruit], { ...initAsk([fruit]), picked: [[1]] })).toBe('回答：Banana')
    const long = 'x'.repeat(30)
    expect(answerLabel([fruit, free], { ...initAsk([fruit, free]), picked: [[1], []], custom: ['', long] })).toBe(`回答：Banana；${'x'.repeat(20)}…`)
  })
  it('英文', () => {
    setLang('en')
    try {
      expect(answerLabel([fruit], initAsk([fruit]))).toBe('Answer')
      expect(answerLabel([fruit], { ...initAsk([fruit]), picked: [[1]] })).toBe('Answer: Banana')
    } finally {
      setLang('zh')
    }
  })
})
```

`components.test.tsx` 172–185 那条：`expect(answer(html)?.[2]).toBe('回答')` 后面补一个"答全后标签带答案"的断言：

```tsx
      html = form(qs, st(qs, { picked: [[1], []], custom: ['', '随便'] }))
      expect(answer(html)?.[1]).not.toContain('disabled')
      expect(answer(html)?.[2]).toBe('回答：Banana；随便')
      expect(html).toContain('class="ask-bar"')
```

- [ ] **Step 2: 跑确认失败**

Run: `cd /Users/xufei/RailseWork/web && npx vitest run src/ask.test.ts src/components/components.test.tsx`

- [ ] **Step 3: 实现**

`ask.ts`：

```ts
/** 底部按钮的标签：答全了带上答案，让人确认自己选了什么再发。 */
export function answerLabel(questions: Question[], s: AskState): string {
  const base = t('回答')
  const all = answers(questions, s)
  if (!all) return base
  const parts = questions.map((q) => clip((all[q.question] ?? '').replace(/, /g, lang() === 'en' ? ', ' : '、'), 20))
  return `${base}${lang() === 'en' ? ': ' : '：'}${parts.join(lang() === 'en' ? '; ' : '；')}`
}
```

（`clip` 来自 `format.ts`，超长补 `…`；`answers()` 多选用 `', '` 连接，中文显示时换成 `、`。）

`AskForm.tsx` 92–108 改为：

```tsx
      {s.error && (
        <div className="error" role="alert">
          {s.error}
        </div>
      )}
      <div className="ask-bar">
        {left > 0 && (
          <p className="sub ask-hint" role="status">
            {t('还有 {n} 题没答', { n: left })}
          </p>
        )}
        <button type="button" className="btn primary block" onClick={onAnswer} disabled={!canAnswer(questions, s)}>
          {s.busy === 'answer' ? t('正在提交…') : answerLabel(questions, s)}
        </button>
        <button type="button" className="link-btn" onClick={onPassthrough} disabled={busy}>
          {s.busy === 'passthrough' ? t('正在交回终端…') : t('让它在终端问我')}
        </button>
      </div>
```

`app.css` 在 `.decide-bar` 规则后加：

```css
/* 答题页的按钮：题再多也贴在底部导航之上；和 .decide-bar 同样的贴法，但竖着排（提示、回答、交回终端） */
.ask-bar { position: sticky; bottom: calc(var(--nav-h) + env(safe-area-inset-bottom)); z-index: 4; display: flex; flex-direction: column; gap: 6px; margin: 12px calc(-1 * var(--gutter)) 0; padding: 10px var(--gutter); background: var(--bg); border-top: 1px solid var(--line); }
.ask-bar .ask-hint { margin: 0; }
```

`Request.tsx:124`：`raised={canDecide || (r.kind === 'ask' && r.state === 'pending')}`。

`e2e/ask.spec.ts`：`getByRole('button', { name: '回答', exact: true })` → `getByRole('button', { name: /^回答/ })`。

- [ ] **Step 4: 跑测试**

Run: `cd /Users/xufei/RailseWork/web && npm run check && cd .. && make e2e`
Expected: 全绿。

- [ ] **Step 5: 提交**

```bash
cd /Users/xufei/RailseWork && git add web
git commit -m "web: 答题按钮贴底，标签带上所选答案（回答：Banana）"
```

### Task 9: Playwright `en` 项目与英文用例

**Files:**
- Modify: `web/playwright.config.ts`
- Modify: `web/e2e/fixtures.ts:139-147`（`login` 加 `lang` 参数）
- Create: `web/e2e/en.spec.ts`

- [ ] **Step 1: 配置**

```ts
  projects: [
    { name: 'chromium', use: { browserName: 'chromium' }, testIgnore: /en\.spec\.ts$/ },
    { name: 'en', use: { browserName: 'chromium', locale: 'en-US' }, testMatch: /en\.spec\.ts$/ },
  ],
```

- [ ] **Step 2: `login` 支持英文**

```ts
export async function login(page: Page, env: E2EEnv, lang: 'zh' | 'en' = 'zh') {
  const resp = await page.goto(env.baseURL + '/')
  expect(resp?.headers()['content-security-policy']).toContain("script-src 'self'")
  await expect(page).toHaveURL(/\/login/)
  await page.getByLabel(lang === 'en' ? 'Password' : '密码').fill(env.password)
  await page.getByRole('button', { name: lang === 'en' ? 'Log in' : '登录', exact: true }).click()
  await expect(page.getByText(lang === 'en' ? 'Nothing waiting for you' : '没有等你处理的事')).toBeVisible()
}
```

（英文句子以 Task 7 里 `Inbox.tsx` 空状态的英文值为准，两边写一样。）

- [ ] **Step 3: 用例** `web/e2e/en.spec.ts`

```ts
import { expect } from '@playwright/test'
import { login, test } from './fixtures'

const cjk = /[一-鿿　-〿＀-￯]/

// 英文系统的手机：登录 → 一条审批 → 一道题 → 设置页，全程不见中文。
// 用的命令与 approve.spec 不同（那边学了 api.example.com 的放行规则，这边用别的主机，免得被自动放行）。
test('English end to end', async ({ page, env, hook, ask }) => {
  await login(page, env, 'en')
  await expect(page.locator('body')).not.toContainText(cjk)

  const h = hook('curl https://en.example.net/ping')
  const card = page.locator('.card', { hasText: 'curl https://en.example.net/ping' })
  await expect(card).toBeVisible()
  await expect(card.locator('.tag')).toHaveText('Network')
  await card.click()
  await expect(page.locator('body')).not.toContainText(cjk)
  await page.getByRole('button', { name: 'Approve', exact: true }).click()
  await page.getByRole('button', { name: 'Approve', exact: true }).last().click() // 面板里的确认键
  await expect(page.locator('.toast')).toContainText('Approved')
  expect((await h).decision).toBe('allow')

  const a = ask({ questions: [{ question: 'Apple or banana?', header: 'Fruit', options: [{ label: 'Apple' }, { label: 'Banana' }], multiSelect: false }] })
  await page.goto(env.baseURL + '/')
  const q = page.locator('.card', { hasText: 'Apple or banana?' })
  await q.click()
  await page.getByLabel('Banana').check()
  await expect(page.getByRole('button', { name: 'Answer: Banana' })).toBeEnabled()
  await page.getByRole('button', { name: 'Answer: Banana' }).click()
  await expect(page.getByText('You answered')).toBeVisible()
  expect((await a).answers).toEqual({ 'Apple or banana?': 'Banana' })
  await expect(page.locator('body')).not.toContainText(cjk)

  await page.goto(env.baseURL + '/settings')
  await expect(page.getByText('Language')).toBeVisible()
  await expect(page.locator('body')).not.toContainText(cjk)
})
```

（`hook`/`ask` 夹具的返回值形状、面板里确认键的名字按 `approve.spec.ts`、`ask.spec.ts` 现有写法调整；页面标签 `Network`、按钮 `Approve`、提示 `Approved` 以 Task 6、7 里写进 `en.ts` 的英文为准，两边一致。）

- [ ] **Step 4: 跑**

Run: `cd /Users/xufei/RailseWork && make e2e`
Expected: chromium 项目 5 条 + en 项目 1 条全部通过。

- [ ] **Step 5: 提交**

```bash
cd /Users/xufei/RailseWork && git add web/playwright.config.ts web/e2e
git commit -m "e2e: en 项目，英文系统从登录到审批、答题、设置全程无中文"
```

### Task 10: 文档

**Files:**
- Modify: `docs/web.md`（设置页加"语言"一条；通知文案跟订阅语言）、`docs/hub.md`（`/api/push/subscribe` 的 `lang`、库版本 4、`/api/push/test` 按语言）、`README.md` 与 `README.en.md` 的限制或升级段（手机网页跟浏览器语言，设置里可切）

- [ ] **Step 1: 改三处文档**；**Step 2: `grep -n 'lang' docs/hub.md docs/web.md` 自查**；**Step 3: 提交**：

```bash
cd /Users/xufei/RailseWork && git add docs README.md README.en.md
git commit -m "docs: 网页语言开关、订阅 lang、库 v4"
```

---

## 自查

**设计覆盖**：§1.3 网页 `t`/语言解析/设置开关/拼句子分支/manifest（Task 1、5、6、7）；§1.4 订阅带 lang、库 v4、推送按订阅语言、切换时重订阅、测试通知（Task 3、4、5）；§1.6 vitest 钉死中文、完整性测试、英文渲染用例、e2e `en` 项目与无中文断言（Task 1、2、6、7、9）；§2 手机网页所有页面与三类推送（Task 4、6、7）、答题按钮小修（Task 8）；§3 验收第 3、4、5 步的网页与推送部分由上述任务支撑。

**占位符扫描**：无 TBD/TODO；翻译量大的任务（6、7）给出规则、示例与门禁，具体英文由执行者按术语表写，完整性测试与英文用例兜底；e2e 用例里依赖其他任务英文值的地方已注明"以 en.ts 为准"。

**类型一致**：`t(zh, vars?)`、`lang()`、`setLang`、`readChoice`、`saveChoice`、`resolveLang`、`initLang`、`langKey`、`Lang`、`LangChoice` 在各任务里一致；`hub.pushSubscribe(sub, lang)`、`resubscribeForLang(env, api)`、`switchLanguage(choice, deps)`、`LanguageView` 一致；Go 侧 `UpsertPushSubscription(endpoint, p256dh, auth, userAgent, lang)`、`normalizeLang`、`testPushMessage(lang)`、`pushMessage(r, w, lang)`、`riskVerb(risk, lang)`、`askSummary(a, lang)`、`message(r, lang)` 一致。

**与 Go 侧计划的接缝**：两份英文表各自独立（Go 的 `en.go`、网页的 `en.ts`），术语表相同；合并前由一个审阅会话通读两份。
