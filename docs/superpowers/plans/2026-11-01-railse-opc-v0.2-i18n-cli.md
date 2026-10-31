# Railse OPC v0.2 命令行英文面（Go 侧）实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 一个英文系统上的用户，从 `install.sh` 到 `railse setup all --local-hub`、`railse doctor`、钩子在终端留下的句子、`railse --help`，全程不见中文；中文用户什么都不变。

**Architecture:** 新包 `internal/i18n`，**中文原文当键**：`i18n.T("中文原文")` 返回当前语言的句子，英文表 `en.go` 查不到就原样返回中文。语言按 `RAILSE_LANG` → 数据目录 `lang` 文件 → `LC_ALL`/`LC_MESSAGES`/`LANG` → 英文 的顺序解析一次。新子命令 `railse lang [zh|en]` 写 `lang` 文件，`setup all` 成功结束时若文件不存在就写入本次语言。`internal/i18n` 里一个 Go 测试用 `go/ast` 扫全仓库的 `i18n.T("…")` 字面量，缺译就红。`install.sh` 自带两套文案。

**Tech Stack:** Go 1.26（`go/ast`、`go/parser`）、POSIX sh、GitHub Actions。

## Global Constraints

- 设计：`docs/superpowers/specs/2026-11-01-railse-opc-v0.2-english-launch-design.md` §1.1、§1.2、§1.5、§1.6、§2、§3。
- **现有测试断言中文一律保持**：`go test` 一律在 `RAILSE_LANG=zh` 下跑（Makefile、CI、`cmd/railse` 与 `internal/doctor` 的 `TestMain` 都钉死）；新的英文用例用 `i18n.Set("en")` + `t.Cleanup(i18n.Reset)`。
- `i18n.T` 的参数**必须是字符串字面量**（含反引号原始串）；守护进程回传的固定句子用 `i18n.Lookup(s)`，完整性测试不查它，但它能用到的每个固定句子都要手工列进 `en.go`。
- 带占位符的键，英文值保持**同样的 `%` 动词、同样的顺序**（`internal/i18n` 有测试查）。
- `go vet` 的 printf 检查会拒绝"非常量格式串且没有参数"的 `Fprintf`：没有参数的句子用 `fmt.Fprint`/`Fprintln`，有参数的才用 `Fprintf(w, i18n.T("…%s…"), x)`。
- 术语表（与网页 `en.ts` 共用，写在 `en.go` 文件头）：批准 approve · 拒绝 deny · 记住 remember · 收件箱 inbox · hub hub · 守护进程 daemon · 钩子 hook · 规则 rule · 项目 project · 机器 machine · 接入码 enroll token · 体检 health check · 开机自启 autostart · 本机 hub local hub · 交回终端处理 handed back to the terminal · 审批 approval · 选择题/提问 question · 暂停自动审批 auto-approval paused · 等你回答 waiting for your answer。
- 英文语气与 README.en 一致：短句、说人话、不吹、不加感叹号。
- 不翻：守护进程与 hub 的日志（`s.log.Printf`）、审计字段、注释、JSON 字段名与枚举值。
- **与设计 §1.2 的一处偏差（写计划时定）**：不在 `proto.Request` 加 `lang` 字段。守护进程回传的句子里只有**拒绝原因**会出现在终端（钩子对 passthrough 不打印 `Reason`），而拒绝原因只有两类：命中规则（`RuleID` 已随回复带回）和固定句子（如"已在手机上拒绝"），钩子进程自己就能按终端语言重拼。以后守护进程要生成自由文本再加字段。执行本计划时顺手把设计文档 §1.2 的那句改掉（Task 6 Step 6）。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/i18n/i18n.go`（新） | 语言解析（`Lang`、`LangSource`、`Set`、`Reset`）、`T`、`Lookup`、`DefaultDir`、`WriteFile` |
| `internal/i18n/en.go`（新） | 英文表 `var en = map[string]string{…}`，文件头放术语表 |
| `internal/i18n/i18n_test.go`（新） | 解析顺序、回退、`WriteFile`、占位符一致性 |
| `internal/i18n/catalog_test.go`（新） | 完整性：扫 `cmd/`、`internal/` 的 `i18n.T("…")`，缺译就红；非字面量就红 |
| `cmd/railse/lang.go`（新）+ `lang_test.go` | `railse lang [zh|en]` |
| `cmd/railse/main.go` | `lang` 子命令、`--help`/`-h`、usage 过 T |
| `cmd/railse/hook.go`、`internal/hookclient/client.go`、`internal/adapter/adapter.go` | 钩子在终端留下的句子；拒绝原因按终端语言重拼 |
| `cmd/railse/setup.go`、`setup_wizard.go`、`setup_localhub.go`、`localhub.go`、`setup_codex.go` | 向导与 setup 输出 |
| `cmd/railse/autostart.go`、`agent.go`、`hub.go`、`policy.go` | 开机自启、`agent status`、hub 的 password/token/run 启动行、policy |
| `internal/doctor/*.go` | 体检项名称、说明、建议、汇总行 |
| `install.sh`、`scripts/test-install.sh` | 两套文案；测试在 zh 下跑原断言，另加 en 一轮 |
| `scripts/english-smoke.sh`（新）、`Makefile`、`.github/workflows/ci.yml` | 英文冒烟与 `RAILSE_LANG=zh` 钉死 |

---

### Task 1: `internal/i18n` 包

**Files:**
- Create: `internal/i18n/i18n.go`
- Create: `internal/i18n/en.go`
- Create: `internal/i18n/i18n_test.go`

**Interfaces:**
- Produces: `func T(zh string) string`、`func Lookup(s string) string`、`func Lang() string`、`func LangSource() (lang, source string)`、`func Set(lang string)`、`func Reset()`、`func DefaultDir() string`、`func WriteFile(dir, lang string) error`、常量 `ZH = "zh"`、`EN = "en"`、`EnvVar = "RAILSE_LANG"`、`FileName = "lang"`。

- [ ] **Step 1: 写失败的测试**

```go
package i18n

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveOrder(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name   string
		env    map[string]string
		file   string
		want   string
		source string
	}{
		{"环境变量最优先", map[string]string{"RAILSE_LANG": "zh", "LANG": "en_US.UTF-8"}, "en", "zh", "env"},
		{"其次 lang 文件", map[string]string{"LANG": "en_US.UTF-8"}, "zh", "zh", "file"},
		{"再次系统 LANG", map[string]string{"LANG": "zh_CN.UTF-8"}, "", "zh", "locale"},
		{"LC_ALL 优先于 LANG", map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "zh_CN.UTF-8"}, "", "en", "locale"},
		{"C 算英文", map[string]string{"LANG": "C"}, "", "en", "locale"},
		{"什么都没有算英文", map[string]string{}, "", "en", "default"},
		{"文件里是空白当没有", map[string]string{}, "  \n", "en", "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, FileName)
			os.Remove(p)
			if c.file != "" {
				if err := os.WriteFile(p, []byte(c.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			getenv := func(k string) string { return c.env[k] }
			lang, source := resolve(getenv, dir)
			if lang != c.want || source != c.source {
				t.Fatalf("resolve = %s/%s，想要 %s/%s", lang, source, c.want, c.source)
			}
		})
	}
}

func TestTFallsBackToChinese(t *testing.T) {
	Set(EN)
	t.Cleanup(Reset)
	if got := T("这句话没有翻译"); got != "这句话没有翻译" {
		t.Fatalf("没翻的要原样返回，得到 %q", got)
	}
	en["测试用的键"] = "test key"
	t.Cleanup(func() { delete(en, "测试用的键") })
	if got := T("测试用的键"); got != "test key" {
		t.Fatalf("T = %q", got)
	}
	Set(ZH)
	if got := T("测试用的键"); got != "测试用的键" {
		t.Fatalf("中文下 T 要原样返回，得到 %q", got)
	}
}

func TestSetNormalizes(t *testing.T) {
	t.Cleanup(Reset)
	for in, want := range map[string]string{"zh": ZH, "ZH_cn": ZH, "en": EN, "fr": EN, " zh_TW.UTF-8 ": ZH} {
		Set(in)
		if got := Lang(); got != want {
			t.Fatalf("Set(%q) → %s，想要 %s", in, got, want)
		}
	}
}

func TestWriteFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	if err := WriteFile(dir, "zh_CN"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "zh\n" {
		t.Fatalf("文件内容 %q，想要 zh 加换行", b)
	}
}

// 英文值的 % 动词要和键一样多、一样序：翻译时漏掉或调换占位符会让 Sprintf 出错。
func TestCatalogVerbsMatch(t *testing.T) {
	for zh, e := range en {
		if a, b := fmtVerbs(zh), fmtVerbs(e); !equalStrings(a, b) {
			t.Errorf("占位符不一致：%q 有 %v，英文 %q 有 %v", zh, a, e, b)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `cd /Users/xufei/RailseWork && go test ./internal/i18n/`
Expected: 编译失败，`undefined: resolve`、`undefined: T` 等。

- [ ] **Step 3: 实现**

`internal/i18n/i18n.go`：

```go
// Package i18n 把中文原文翻成当前语言。中文原文就是键：代码里照常写中文，英文表 en.go 查不到就原样返回，
// 没翻到的地方只是显示中文，不会崩。语言在进程里解析一次（见 resolve），Set 可覆盖。
package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	ZH       = "zh"
	EN       = "en"
	EnvVar   = "RAILSE_LANG"
	FileName = "lang"
)

var (
	mu   sync.RWMutex
	cur  string // "" 表示还没解析
	from string // env、file、locale、default、set
)

// Lang 返回当前语言：ZH 或 EN。
func Lang() string {
	l, _ := LangSource()
	return l
}

// LangSource 返回语言与它从哪来：env（RAILSE_LANG）、file（数据目录的 lang 文件）、locale（LC_ALL/LC_MESSAGES/LANG）、
// default（什么都没有，按英文）、set（Set 指定）。
func LangSource() (lang, source string) {
	mu.RLock()
	if cur != "" {
		defer mu.RUnlock()
		return cur, from
	}
	mu.RUnlock()
	mu.Lock()
	defer mu.Unlock()
	if cur == "" {
		cur, from = resolve(os.Getenv, DefaultDir())
	}
	return cur, from
}

// Set 直接指定语言（railse lang 与测试用）；不是 zh 开头的一律按 en。
func Set(lang string) {
	mu.Lock()
	defer mu.Unlock()
	cur, from = normalize(lang), "set"
}

// Reset 清掉已解析的结果，下次 Lang 重新解析（测试用）。
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	cur, from = "", ""
}

func normalize(s string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "zh") {
		return ZH
	}
	return EN
}

// resolve 按 RAILSE_LANG → <dir>/lang → LC_ALL、LC_MESSAGES、LANG → 英文 的顺序定语言。
// 什么都没有算英文：英文系统的终端几乎都带 LANG，容器里常常没有；中文用户跑一次 railse lang zh 即可。
func resolve(getenv func(string) string, dir string) (lang, source string) {
	if v := getenv(EnvVar); strings.TrimSpace(v) != "" {
		return normalize(v), "env"
	}
	if dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, FileName)); err == nil && strings.TrimSpace(string(b)) != "" {
			return normalize(string(b)), "file"
		}
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(k); strings.TrimSpace(v) != "" {
			return normalize(v), "locale"
		}
	}
	return EN, "default"
}

// DefaultDir 是守护进程的数据目录：$RAILSE_HOME，否则 ~/.railse；拿不到家目录时为空（只影响 lang 文件的读写）。
// 与 internal/agent.DefaultConfig 的规则相同，这里不引用它以免成环。
func DefaultDir() string {
	if d := os.Getenv("RAILSE_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".railse")
}

// WriteFile 把语言写进 <dir>/lang；目录不存在就建（0700，与守护进程数据目录一致）。
func WriteFile(dir, lang string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, FileName), []byte(normalize(lang)+"\n"), 0o644)
}

// T 把中文原文翻成当前语言；英文表里没有的原样返回。
// 参数必须是字符串字面量：catalog_test 靠它找出所有键并核对英文表。
func T(zh string) string {
	if Lang() == ZH {
		return zh
	}
	if s, ok := en[zh]; ok {
		return s
	}
	return zh
}

// Lookup 与 T 相同，但允许传变量：只用于守护进程回传给钩子的固定句子（见 cmd/railse/hook.go 的 localizeReason）。
// 完整性测试不查它，所以它可能用到的每个固定句子都要手工列进 en.go。
func Lookup(s string) string { return T(s) }

var verbRe = regexp.MustCompile(`%[-+# 0]*\d*(?:\.\d+)?[a-zA-Z%]`)

// fmtVerbs 列出格式串里的 % 动词（%% 也算），测试用。
func fmtVerbs(s string) []string { return verbRe.FindAllString(s, -1) }
```

`internal/i18n/en.go`（先放一条，后面各任务往里加）：

```go
package i18n

// 英文表：键是代码里的中文原文（含占位符与标点），值是英文。语气与 README.en 一致：短句、说人话、不吹。
//
// 术语表（网页的 web/src/locales/en.ts 同此）：
//   批准 approve · 拒绝 deny · 记住 remember · 收件箱 inbox · hub hub · 守护进程 daemon · 钩子 hook · 规则 rule
//   项目 project · 机器 machine · 接入码 enroll token · 体检 health check · 开机自启 autostart · 本机 hub local hub
//   交回终端处理 handed back to the terminal · 审批 approval · 选择题/提问 question · 暂停自动审批 auto-approval paused
//   等你回答 waiting for your answer
var en = map[string]string{
	"未知子命令 %q\n\n%s\n": "Unknown command %q\n\n%s\n",
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/i18n/ -run 'TestResolveOrder|TestTFallsBack|TestSetNormalizes|TestWriteFile|TestCatalogVerbsMatch' -v`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/i18n
git commit -m "i18n: 中文原文当键的翻译包：语言按 RAILSE_LANG → lang 文件 → 系统 LANG → 英文解析，缺译回退中文"
```

### Task 2: 完整性测试（词典门禁）

**Files:**
- Create: `internal/i18n/catalog_test.go`

**Interfaces:**
- Consumes: `en`（Task 1）。
- Produces: 任何后续任务新加的 `i18n.T("…")` 键若不在 `en` 里，`go test ./internal/i18n/` 就红。

- [ ] **Step 1: 写测试**

```go
package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 扫 cmd/ 与 internal/ 下所有非测试 .go 文件里的 i18n.T("…") 调用：
//   - 参数不是字符串字面量 → 报错（完整性没法保证）；
//   - 键不在 en 里 → 报错并列出；
//   - en 里没人用的键 → 只打印，提醒清理。
func TestCatalogComplete(t *testing.T) {
	root := filepath.Join("..", "..")
	used := map[string][]string{}
	fset := token.NewFileSet()
	for _, top := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "i18n" || d.Name() == "testdata" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "T" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "i18n" {
					return true
				}
				pos := fset.Position(call.Pos()).String()
				if len(call.Args) != 1 {
					t.Errorf("%s: i18n.T 要恰好一个参数", pos)
					return true
				}
				lit, ok := call.Args[0].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Errorf("%s: i18n.T 的参数必须是字符串字面量（变量请用 i18n.Lookup，并手工把句子列进 en.go）", pos)
					return true
				}
				key, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Errorf("%s: 解析字面量失败: %v", pos, err)
					return true
				}
				used[key] = append(used[key], pos)
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(used) == 0 {
		t.Fatal("没扫到任何 i18n.T 调用，路径不对？")
	}
	var missing []string
	for key, where := range used {
		if _, ok := en[key]; !ok {
			missing = append(missing, strconv.Quote(key)+"  // "+where[0])
		}
	}
	if len(missing) > 0 {
		t.Errorf("英文表缺 %d 条，请补进 internal/i18n/en.go：\n%s", len(missing), strings.Join(missing, "\n"))
	}
	for key := range en {
		if _, ok := used[key]; !ok {
			t.Logf("英文表里没人用的键（Lookup 用的固定句子除外）：%q", key)
		}
	}
}
```

- [ ] **Step 2: 跑**

Run: `go test ./internal/i18n/ -run TestCatalogComplete -v`
Expected: 此时还没有任何 `i18n.T` 调用 → FAIL "没扫到任何 i18n.T 调用"。这是预期：Task 3 加第一个调用后转绿。

- [ ] **Step 3: 先不单独提交**

这个测试在 Task 3 加进第一个 `i18n.T` 调用之前一直是红的；为了分支上每个提交都能过 `make test`，把它和 Task 3 一起提交（Task 3 Step 6 的 `git add` 里已含 `internal/i18n/catalog_test.go`）。

### Task 3: `railse lang`、`--help`、main.go 的 usage；测试钉死中文

**Files:**
- Create: `cmd/railse/lang.go`、`cmd/railse/lang_test.go`
- Modify: `cmd/railse/main.go:16-57`
- Modify: `cmd/railse/main_test.go:47-75`（TestMain）
- Modify: `internal/doctor`（找有 `TestMain` 的文件；没有就新建 `internal/doctor/main_test.go`）

**Interfaces:**
- Produces: `func runLang(args []string, stdout, stderr io.Writer) int`。

- [ ] **Step 1: 写失败的测试** `cmd/railse/lang_test.go`

```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
)

func TestLangWriteAndShow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("RAILSE_HOME", dir)
	t.Setenv("RAILSE_LANG", "")
	i18n.Reset()
	t.Cleanup(func() { i18n.Set(i18n.ZH) })

	var out, errOut bytes.Buffer
	if code := run([]string{"lang", "en"}, &out, &errOut); code != 0 {
		t.Fatalf("退出码 %d, stderr=%s", code, errOut.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, i18n.FileName))
	if err != nil || string(b) != "en\n" {
		t.Fatalf("lang 文件 = %q, %v", b, err)
	}
	out.Reset()
	i18n.Reset()
	if code := run([]string{"lang"}, &out, &errOut); code != 0 {
		t.Fatalf("退出码 %d", code)
	}
	if got := out.String(); !strings.Contains(got, "en") || !strings.Contains(got, "lang file") {
		t.Fatalf("stdout = %q，想要说明当前是 en 且来自 lang 文件", got)
	}
}

func TestLangRejectsOther(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"lang", "fr"}, &out, &errOut); code != 2 {
		t.Fatalf("退出码 %d，想要 2", code)
	}
	if !strings.Contains(errOut.String(), "zh") {
		t.Fatalf("stderr 要提示只能 zh 或 en：%s", errOut.String())
	}
}

func TestHelpFlag(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("退出码 %d", code)
	}
	if !strings.Contains(out.String(), "railse") || !strings.Contains(out.String(), "lang") {
		t.Fatalf("用法里要有 lang 子命令：%s", out.String())
	}
}

func TestUsageEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	var out, errOut bytes.Buffer
	run([]string{"--help"}, &out, &errOut)
	if strings.ContainsAny(out.String(), "用法子命令") {
		t.Fatalf("英文用法里不该有中文：%s", out.String())
	}
	if !strings.Contains(out.String(), "Usage: railse <command>") {
		t.Fatalf("英文用法 = %s", out.String())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run 'TestLang|TestHelpFlag|TestUsageEnglish' -v`
Expected: FAIL（`lang` 是未知子命令、`--help` 返回 2）。

- [ ] **Step 3: 实现** `cmd/railse/lang.go`

```go
package main

import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/xufei547/railse-opc/internal/i18n"
)

const langUsage = `用法: railse lang [zh|en]
不带参数打印当前语言和它从哪来；带参数写进数据目录的 lang 文件（$RAILSE_HOME 或 ~/.railse）。
解析顺序：环境变量 RAILSE_LANG → lang 文件 → 系统 LANG（zh 开头算中文，其余英文）。`

// runLang 查看或设定界面语言。写文件后对当前进程也立刻生效。
func runLang(args []string, stdout, stderr io.Writer) int {
	switch len(args) {
	case 0:
		lang, source := i18n.LangSource()
		fmt.Fprintf(stdout, i18n.T("当前语言 %s（来源：%s）\n"), lang, langSourceName(source))
		return 0
	case 1:
		switch args[0] {
		case "zh", "en":
		case "-h", "--help", "help":
			fmt.Fprintln(stdout, i18n.T(langUsage))
			return 0
		default:
			fmt.Fprintf(stderr, i18n.T("语言只能是 zh 或 en，不是 %q\n\n%s\n"), args[0], i18n.T(langUsage))
			return 2
		}
		dir := i18n.DefaultDir()
		if dir == "" {
			fmt.Fprintln(stderr, i18n.T("找不到家目录，没法写 lang 文件"))
			return 1
		}
		if err := i18n.WriteFile(dir, args[0]); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		i18n.Set(args[0])
		fmt.Fprintf(stdout, i18n.T("已写入 %s\n"), filepath.Join(dir, i18n.FileName))
		return 0
	default:
		fmt.Fprintf(stderr, i18n.T("多余的参数 %q\n%s\n"), args[1], i18n.T(langUsage))
		return 2
	}
}

func langSourceName(source string) string {
	switch source {
	case "env":
		return i18n.T("环境变量 RAILSE_LANG")
	case "file":
		return i18n.T("lang 文件")
	case "locale":
		return i18n.T("系统 LANG")
	case "set":
		return i18n.T("命令指定")
	default:
		return i18n.T("默认")
	}
}
```

`cmd/railse/main.go` 改动：

```go
const usage = `用法: railse <子命令> [参数]
…（原有各行照旧）…
  lang      查看或设定界面语言（zh|en）`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, i18n.T(usage))
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, i18n.T(usage))
		return 0
	case "version":
		fmt.Fprintln(stdout, "railse "+version)
		return 0
	case "lang":
		return runLang(args[1:], stdout, stderr)
	// …其余 case 照旧…
	default:
		fmt.Fprintf(stderr, i18n.T("未知子命令 %q\n\n%s\n"), args[0], i18n.T(usage))
		return 2
	}
}
```

`en.go` 加（`usage` 的英文值要把整段原文照抄为键，下面给出英文值；原文键以 main.go 里加了 lang 行之后的最终文本为准）：

```go
	"用法: railse <子命令> [参数]\n…": `Usage: railse <command> [args]
  version   print the version
  policy    evaluate or check rules
  hook      run as a CLI hook (reads the hook JSON from stdin)
  agent     daemon: run, status, pause, resume, reload
  hub       hub: run, password, token
  setup     install hooks, enroll a machine, autostart, local hub
  doctor    health check
  lang      show or set the UI language (zh|en)`,
	"未知子命令 %q\n\n%s\n":                     "Unknown command %q\n\n%s\n",
	"当前语言 %s（来源：%s）\n":                    "Current language: %s (from %s)\n",
	"语言只能是 zh 或 en，不是 %q\n\n%s\n":          "Language must be zh or en, not %q\n\n%s\n",
	"找不到家目录，没法写 lang 文件":                   "Can't find your home directory, so the lang file can't be written",
	"已写入 %s\n":                              "Wrote %s\n",
	"多余的参数 %q\n%s\n":                        "Unexpected argument %q\n%s\n",
	"环境变量 RAILSE_LANG":                       "the RAILSE_LANG environment variable",
	"lang 文件":                                "the lang file",
	"系统 LANG":                                "the system LANG",
	"命令指定":                                   "this command",
	"默认":                                     "the default",
	// langUsage 整段
	"用法: railse lang [zh|en]\n不带参数打印当前语言和它从哪来；带参数写进数据目录的 lang 文件（$RAILSE_HOME 或 ~/.railse）。\n解析顺序：环境变量 RAILSE_LANG → lang 文件 → 系统 LANG（zh 开头算中文，其余英文）。": "Usage: railse lang [zh|en]\nWithout an argument, prints the current language and where it came from; with one, writes it to the lang file in the data directory ($RAILSE_HOME or ~/.railse).\nResolution order: the RAILSE_LANG environment variable → the lang file → the system LANG (zh* means Chinese, anything else English).",
```

每个子命令的 usage 各行的英文自行翻译，主 usage 里各行的说明必须和子命令 usage 的第一行一致。

- [ ] **Step 4: 钉死测试语言**

`cmd/railse/main_test.go` 的 `TestMain` 里，在 `os.Unsetenv("RAILSE_HOME")` 之后加：

```go
	os.Setenv("RAILSE_LANG", "zh")
	i18n.Set(i18n.ZH)
```

`internal/doctor`：若已有 `TestMain`，同样加这两行；没有就新建 `internal/doctor/main_test.go`：

```go
package doctor

import (
	"os"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
)

func TestMain(m *testing.M) {
	os.Setenv("RAILSE_LANG", "zh")
	i18n.Set(i18n.ZH)
	os.Exit(m.Run())
}
```

`internal/adapter`、`internal/hookclient`、`internal/agent` 的测试若在 Task 6 之后断言到被 T 包裹的句子，也同样加 `TestMain`。

- [ ] **Step 5: 跑测试**

Run: `RAILSE_LANG=zh go test ./cmd/railse/ ./internal/i18n/ -count=1`
Expected: 全部 PASS（`TestCatalogComplete` 现在扫到了键并且都在表里）。

- [ ] **Step 6: 提交**

```bash
git add cmd/railse/lang.go cmd/railse/lang_test.go cmd/railse/main.go cmd/railse/main_test.go internal/doctor/main_test.go internal/i18n/en.go internal/i18n/catalog_test.go
git commit -m "cli: railse lang 查看或设定语言；--help；usage 过 i18n.T；测试钉死中文"
```

### Task 4: `setup all` 成功时记住语言；向导与 setup 的全部输出过 T

**Files:**
- Modify: `cmd/railse/setup.go`（1089–1097 收尾处；全文 131 处中文字面量与 `setupAllUsage`、`setupUsage`）
- Modify: `cmd/railse/setup_wizard.go`（28 处；`printPhoneStep` 212–223）
- Modify: `cmd/railse/setup_localhub.go`（36 处）、`cmd/railse/localhub.go`（11 处）、`cmd/railse/setup_codex.go`（24 处 + `setupCodexNotifyUsage`）
- Modify: `internal/i18n/en.go`
- Test: `cmd/railse/setup_wizard_test.go`、`cmd/railse/setup_test.go`（现有中文断言不动）

**Interfaces:**
- Consumes: `i18n.T`、`i18n.WriteFile`、`i18n.FileName`、`i18n.Lang`。

- [ ] **Step 1: 写失败的测试**（加到 `cmd/railse/setup_wizard_test.go` 末尾）

```go
func TestPrintPhoneStepEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	var b bytes.Buffer
	printPhoneStep(&b, "https://hub.example", true)
	got := b.String()
	for _, want := range []string{"Next: open this on your phone", "https://hub.example", "Tailscale", "Add to Home Screen", "turn on notifications"} {
		if !strings.Contains(got, want) {
			t.Fatalf("英文提示缺 %q：\n%s", want, got)
		}
	}
	if hasCJK(got) {
		t.Fatalf("英文提示里有中文：\n%s", got)
	}
}

// hasCJK 报告字符串里有没有中日韩统一表意文字（U+4E00–U+9FFF）或全角标点（U+FF00–U+FFEF）。
func hasCJK(s string) bool {
	for _, r := range s {
		if (r >= 0x4E00 && r <= 0x9FFF) || (r >= 0xFF00 && r <= 0xFFEF) || (r >= 0x3000 && r <= 0x303F) {
			return true
		}
	}
	return false
}
```

并在现有的 `setup all` 成功路径测试（`setup_test.go` 里断言"下一步：在手机上打开"的那个）末尾加：

```go
	if b, err := os.ReadFile(filepath.Join(cfgDir, i18n.FileName)); err != nil || string(b) != "zh\n" {
		t.Fatalf("setup all 成功后要写 lang 文件：%q, %v", b, err)
	}
```

（`cfgDir` 换成该测试里的数据目录变量。）

- [ ] **Step 2: 跑测试确认失败**

Run: `RAILSE_LANG=zh go test ./cmd/railse/ -run 'TestPrintPhoneStepEnglish|TestSetupAll' -v`
Expected: 英文用例 FAIL（输出仍是中文）；lang 文件断言 FAIL。

- [ ] **Step 3: 实现**

`printPhoneStep` 改为：

```go
func printPhoneStep(w io.Writer, hubURL string, local bool) {
	if hubURL == "" {
		fmt.Fprintln(w, i18n.T("\n下一步：在手机上打开 hub 的地址"))
	} else {
		bar := strings.Repeat("═", utf8.RuneCountInString(hubURL)+4)
		fmt.Fprintf(w, i18n.T("\n下一步：在手机上打开\n╔%s╗\n║  %s  ║\n╚%s╝\n"), bar, hubURL, bar)
	}
	if local {
		fmt.Fprintln(w, i18n.T("这个地址由 Tailscale 提供；手机挂着代理打不开时关掉代理再试。"))
	}
	fmt.Fprintln(w, i18n.T("手机 Safari 打开后分享→添加到主屏幕，再从图标打开；登录后在设置里开启通知（见 docs/web.md）。"))
}
```

英文值：

```go
	"\n下一步：在手机上打开 hub 的地址":                        "\nNext: open this on your phone: the hub's address",
	"\n下一步：在手机上打开\n╔%s╗\n║  %s  ║\n╚%s╝\n":        "\nNext: open this on your phone\n╔%s╗\n║  %s  ║\n╚%s╝\n",
	"这个地址由 Tailscale 提供；手机挂着代理打不开时关掉代理再试。":      "This address is served by Tailscale; if your phone is on a proxy and it won't open, turn the proxy off and retry.",
	"手机 Safari 打开后分享→添加到主屏幕，再从图标打开；登录后在设置里开启通知（见 docs/web.md）。": "In Safari on the phone: Share → Add to Home Screen, then open it from the icon; after logging in, turn on notifications in Settings (see docs/web.md).",
```

`setup.go` 收尾（1093 行附近，`for _, n := range notes` 之前）加：

```go
	if _, err := os.Stat(filepath.Join(cfg.Dir, i18n.FileName)); errors.Is(err, fs.ErrNotExist) {
		if werr := i18n.WriteFile(cfg.Dir, i18n.Lang()); werr != nil {
			notes = append(notes, fmt.Sprintf(i18n.T("没能记住界面语言（%v），需要时运行 railse lang zh 或 railse lang en"), werr))
		}
	}
```

然后把 `setup.go`、`setup_wizard.go`、`setup_localhub.go`、`localhub.go`、`setup_codex.go` 里**所有给人看的句子**包上 `i18n.T`：提示、步骤名、错误、注意、usage 常量。规则：

- `fmt.Fprintf(w, "…%s…", x)` → `fmt.Fprintf(w, i18n.T("…%s…"), x)`；没有参数的 `Fprintf` 改 `Fprint`/`Fprintln`。
- `fmt.Errorf("…%w", err)` 给人看的也包：`fmt.Errorf(i18n.T("…%w"), err)`（`%w` 保留）。
- 步骤名 `steps := []string{"接入 hub", …}` 与 `step("接入 hub")` 的键必须一致：把 `step` 改成先查序号再翻译显示：`fmt.Fprintf(stdout, "\n== %d. %s ==\n", stepNo(name), i18n.T(name))`，并给四个（本机模式五个）步骤名各加一条英文：`"本机 hub": "local hub"`、`"接入 hub": "connect to the hub"`、`"安装钩子": "install hooks"`、`"开机自启": "autostart"`、`"体检": "health check"`。
- 向导的 `ask(prompt, check)` 闭包：prompt 与 check 返回的错误都包 T。
- `setupAllUsage`、`setupUsage`、`setupCodexNotifyUsage` 整段当键。
- 不包：日志、JSON、路径、命令名本身。

- [ ] **Step 4: 跑测试**

Run: `RAILSE_LANG=zh go test ./cmd/railse/ ./internal/i18n/ -count=1`
Expected: PASS。`TestCatalogComplete` 若列出缺的键，逐条补进 `en.go` 再跑。

- [ ] **Step 5: 英文冒烟看一眼**

Run: `go build -o /tmp/railse-en ./cmd/railse && RAILSE_LANG=en /tmp/railse-en setup 2>&1 | head -20; RAILSE_LANG=en /tmp/railse-en setup all --help 2>&1 | head -30`
Expected: 看不到中文；句子通顺。

- [ ] **Step 6: 提交**

```bash
git add cmd/railse internal/i18n/en.go
git commit -m "setup: 向导、setup all/hooks/codex-notify、本机 hub 的输出过 i18n.T；成功结束时记住语言"
```

### Task 5: doctor 的项名、说明、建议、汇总行

**Files:**
- Modify: `internal/doctor/report.go:10-24`、`internal/doctor/hub.go`（111 处）、`hooks.go`（73）、`agent.go`（65）、`cli.go`（19）、`doctor.go`（3）
- Modify: `cmd/railse/doctor.go`（`doctorUsage` 与 12 处）
- Modify: `internal/i18n/en.go`
- Test: `internal/doctor/report_test.go`（新加英文用例；现有中文断言不动）

- [ ] **Step 1: 写失败的测试**

```go
func TestReportWriteEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	r := Report{Version: "0.2.0", Checks: []Check{
		{ID: "cli.bin", Name: i18n.T("railse 可执行文件"), Status: OK, Detail: "/opt/homebrew/bin/railse"},
		{ID: "hooks.claude", Name: i18n.T("Claude Code 钩子"), Status: Warn, Detail: i18n.T("缺 PreToolUse"), Hint: i18n.T("运行 railse setup hooks --claude")},
	}}
	var b bytes.Buffer
	r.Write(&b)
	got := b.String()
	for _, want := range []string{"[ok]", "[warn]", "Fix: ", "1 passed, 1 warning, 0 failed, 0 skipped (railse 0.2.0)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q：\n%s", want, got)
		}
	}
	if strings.ContainsAny(got, "项通过修复") {
		t.Fatalf("英文汇总里有中文：\n%s", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `RAILSE_LANG=zh go test ./internal/doctor/ -run TestReportWriteEnglish -v`
Expected: FAIL。

- [ ] **Step 3: 实现**

`report.go` 的 `Write`：

```go
	counts := map[Status]int{}
	for _, c := range r.Checks {
		counts[c.Status]++
		fmt.Fprintf(w, "%-7s %s", "["+string(c.Status)+"]", c.Name)
		if c.Detail != "" {
			fmt.Fprintf(w, i18n.T("：%s"), c.Detail)
		}
		fmt.Fprintln(w)
		if c.Hint != "" && (c.Status == Fail || c.Status == Warn) {
			fmt.Fprintf(w, i18n.T("        修复：%s\n"), c.Hint)
		}
	}
	fmt.Fprintf(w, i18n.T("\n%d 项通过，%d 项警告，%d 项失败，%d 项跳过（railse %s）\n"), counts[OK], counts[Warn], counts[Fail], counts[Skip], r.Version)
```

英文值：`"：%s": ": %s"`、`"        修复：%s\n": "        Fix: %s\n"`、`"\n%d 项通过，%d 项警告，%d 项失败，%d 项跳过（railse %s）\n": "\n%d passed, %d warnings, %d failed, %d skipped (railse %s)\n"`（英文"1 warning"的单复数不处理，保持 `warnings`，别为它加分支）。

然后 `hub.go`、`hooks.go`、`agent.go`、`cli.go`、`doctor.go` 里每个 `Check` 的 `Name`、`Detail`、`Hint` 字面量都包 T；`Detail` 用 `fmt.Sprintf` 拼的，格式串包 T。`--json` 输出同样是翻译后的文本（JSON 字段名不变）。`cmd/railse/doctor.go` 的 `doctorUsage` 与 12 处提示包 T。

- [ ] **Step 4: 跑测试**

Run: `RAILSE_LANG=zh go test ./internal/doctor/ ./cmd/railse/ ./internal/i18n/ -count=1`
Expected: PASS；完整性测试列出的缺键补齐。

- [ ] **Step 5: 英文冒烟**

Run: `go build -o /tmp/railse-en ./cmd/railse && RAILSE_LANG=en /tmp/railse-en doctor; echo "exit=$?"`
Expected: 每行英文；最后一行形如 `20 passed, 1 warnings, 0 failed, 1 skipped (railse dev)`。

- [ ] **Step 6: 提交**

```bash
git add internal/doctor cmd/railse/doctor.go internal/i18n/en.go
git commit -m "doctor: 项名、说明、建议与汇总行过 i18n.T"
```

### Task 6: 钩子留在终端里的句子；拒绝原因按终端语言重拼

**Files:**
- Modify: `cmd/railse/hook.go`（10 处 + `hookUsage`；`hookPermission` 158 附近加 `localizeReason`）
- Modify: `internal/hookclient/client.go:30,51,70,74,160`
- Modify: `internal/adapter/adapter.go:223-228`（`denyMessage`）、`adapter.go:132-164`（`ValidateAsk` 错误）、`claude.go:34,44,57,61`、`codex.go:32,38,68`、`gemini.go:36,42`
- Modify: `internal/agent/config.go`（"找不到家目录: %w"）
- Modify: `internal/i18n/en.go`（含 `Lookup` 用的固定句子）
- Modify: `docs/superpowers/specs/2026-11-01-railse-opc-v0.2-english-launch-design.md` §1.2
- Test: `cmd/railse/hook_test.go`、`internal/adapter/adapter_test.go`

**Interfaces:**
- Produces: `func localizeReason(resp proto.Response) string`（`cmd/railse/hook.go`）。

- [ ] **Step 1: 写失败的测试**（`cmd/railse/hook_test.go` 末尾）

```go
func TestLocalizeReason(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	cases := map[proto.Response]string{
		{Reason: "命中规则 deny-rm", RuleID: "deny-rm"}: "Matched rule deny-rm",
		{Reason: "已在手机上拒绝"}:                       "Denied on your phone",
		{Reason: "自动审批已暂停，交回终端处理"}:              "Auto-approval is paused; handed back to the terminal",
		{Reason: "一句没见过的话"}:                       "一句没见过的话",
	}
	for resp, want := range cases {
		if got := localizeReason(resp); got != want {
			t.Errorf("localizeReason(%+v) = %q，想要 %q", resp, got, want)
		}
	}
}

func TestPassthroughMessageEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	var b bytes.Buffer
	passthrough(&b, i18n.T("请求没有对应的适配器"))
	if got := b.String(); got != "railse hook: no adapter for this request; handed back to the terminal\n" {
		t.Fatalf("got %q", got)
	}
}
```

`internal/adapter/adapter_test.go` 加：

```go
func TestDenyMessageEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	if got := denyMessage(""); got != "Railse: denied" {
		t.Fatalf("got %q", got)
	}
	if got := denyMessage("Matched rule deny-rm"); got != "Railse: Matched rule deny-rm" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `RAILSE_LANG=zh go test ./cmd/railse/ ./internal/adapter/ -run 'TestLocalizeReason|TestPassthroughMessageEnglish|TestDenyMessageEnglish' -v`
Expected: FAIL（`localizeReason` 未定义等）。

- [ ] **Step 3: 实现**

`cmd/railse/hook.go`：

```go
// localizeReason 把守护进程回传的拒绝原因翻成终端语言。命中规则的按规则号重拼（规则号本身不翻），
// 其余是守护进程里的固定句子，按英文表查；查不到原样返回。passthrough 的原因不会显示，不用管。
func localizeReason(resp proto.Response) string {
	if resp.RuleID != "" && strings.HasPrefix(resp.Reason, "命中规则 ") {
		return fmt.Sprintf(i18n.T("命中规则 %s"), resp.RuleID)
	}
	return i18n.Lookup(resp.Reason)
}
```

在 `awaitReply` 拿到 `resp` 之后、调用 `ad.Render` 之前：`resp.Reason = localizeReason(resp)`。

`passthrough`：

```go
func passthrough(stderr io.Writer, why string) {
	fmt.Fprintln(stderr, "railse hook: "+why+i18n.T("，交回终端处理"))
}
```

`hook.go` 其余 10 处、`hookUsage`、`hookclient` 5 处、`adapter` 的 `denyMessage` 与 Parse/ValidateAsk 错误、`agent/config.go` 的一处都包 T。

`denyMessage`：

```go
func denyMessage(reason string) string {
	if reason == "" {
		return i18n.T("Railse：已拒绝")
	}
	return i18n.T("Railse：") + reason
}
```

`en.go` 加（含 `Lookup` 会查到的固定句子，来源 `internal/agent/hubdecider.go:17-30`、`internal/agent/server.go`、`internal/agent/requests.go:54`、`internal/policy/engine.go:84-89`，逐个常量抄原文当键）：

```go
	"Railse：已拒绝":                  "Railse: denied",
	"Railse：":                     "Railse: ",
	"命中规则 %s":                     "Matched rule %s",
	"，交回终端处理":                     "; handed back to the terminal",
	"请求没有对应的适配器":                  "no adapter for this request",
	// Lookup 用的固定句子（守护进程回传，钩子按终端语言查表）
	"已在手机上拒绝":                     "Denied on your phone",
	"已在手机上批准":                     "Approved on your phone",
	"自动审批已暂停，交回终端处理":             "Auto-approval is paused; handed back to the terminal",
	"自动审批已暂停":                     "Auto-approval is paused",
	"本机没有连接 hub，交回终端处理":          "This machine isn't connected to a hub; handed back to the terminal",
	"等待过期，交回终端处理":                "Timed out waiting; handed back to the terminal",
	"守护进程正在停止，交回终端处理":           "The daemon is stopping; handed back to the terminal",
	"决定来源出错，交回终端处理":              "The decider failed; handed back to the terminal",
	"决定来源给的答案不完整，交回终端处理":        "The decider returned an incomplete answer; handed back to the terminal",
	"脱敏后有题面相同的题，分不清答的是哪道，交回终端处理": "Two questions read the same after redaction, so answers can't be matched; handed back to the terminal",
```

`hubdecider.go` 里 `reasonGemini`、`reasonNoHub`、`reasonUnreachable`、`reasonHubDisabled`、`reasonSendFailed`、`reasonLost`、`reasonNoDecision`、`reasonOldHub`、`reasonAnswered`、`reasonAskTerminal` 的原文也各加一条英文（打开文件抄原文）。

- [ ] **Step 4: 跑测试**

Run: `RAILSE_LANG=zh go test ./cmd/railse/ ./internal/adapter/ ./internal/hookclient/ ./internal/agent/ ./internal/i18n/ -count=1`
Expected: PASS。`internal/adapter` 等包若有测试断言到被包裹的句子而没钉语言，按 Task 3 Step 4 加 `TestMain`。

- [ ] **Step 5: 真机看一眼**

在一个有 Claude Code 的项目里：`RAILSE_LANG=en claude -p "run: echo hi"`（或任何会触发审批的命令），手机上拒绝，终端里应出现 `Railse: Denied on your phone`。

- [ ] **Step 6: 改设计文档 §1.2 并提交**

把 §1.2 第一条"钩子进程…把 lang 放进发给守护进程的请求（proto 里钩子请求结构加一个字段…）"改为："钩子进程跑在用户终端里，知道语言。守护进程回传的句子里只有拒绝原因会显示，钩子按规则号与固定句子表在本地重拼，不改 proto。"

```bash
git add cmd/railse/hook.go cmd/railse/hook_test.go internal/hookclient internal/adapter internal/agent/config.go internal/i18n/en.go docs/superpowers/specs/2026-11-01-railse-opc-v0.2-english-launch-design.md
git commit -m "hook: 终端里的句子过 i18n.T，拒绝原因按终端语言重拼；设计 §1.2 改为不加 proto 字段"
```

### Task 7: 开机自启、`agent status`、hub 启动行、policy、hub/agent 的 usage

**Files:**
- Modify: `cmd/railse/autostart.go`（68 处 + `autostartUsage`）、`cmd/railse/agent.go`（48 处 + `agentUsage`，含 `hubLine`、`pausedLine`）、`cmd/railse/hub.go`（46 处 + `hubUsage`；`readPassword` 的提示）、`cmd/railse/policy.go`（11 处 + `policyUsage`）、`cmd/railse/autostart_other.go`（1 处）
- Modify: `internal/i18n/en.go`
- Test: `cmd/railse/agent_test.go`（新加英文用例）

砍单顺序提醒：来不及时，先放弃 `policy.go` 与各 usage，再放弃 `hub.go`；`autostart.go` 与 `agent status` 不放弃（向导会调用、doctor 会引导用户去跑）。

- [ ] **Step 1: 写失败的测试**

```go
func TestAgentStatusEnglish(t *testing.T) {
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	// 复用本文件里已有的"起一个假守护进程再查 status"的辅助（见 TestAgentStatus），只改断言：
	got := agentStatusOutput(t) // 若无此辅助，按 TestAgentStatus 的写法内联
	if !strings.Contains(got, "hub: connected") && !strings.Contains(got, "hub: not connected") {
		t.Fatalf("英文 status 里要有 hub 行：%s", got)
	}
	if hasCJK(got) {
		t.Fatalf("英文 status 里有中文：%s", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `RAILSE_LANG=zh go test ./cmd/railse/ -run TestAgentStatusEnglish -v`
Expected: FAIL。

- [ ] **Step 3: 实现**：四个文件里给人看的句子全部包 T，usage 整段当键；`hubLine`/`pausedLine` 返回的句子包 T；`readPassword` 的提示（"输入密码："之类）包 T。日志行（`log.Printf`）不包。

- [ ] **Step 4: 跑测试**

Run: `RAILSE_LANG=zh go test ./cmd/railse/ ./internal/i18n/ -count=1`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add cmd/railse internal/i18n/en.go
git commit -m "cli: autostart、agent status、hub 启动行与 policy 的输出过 i18n.T"
```

### Task 8: `install.sh` 两套文案；`test-install.sh` 在 zh 下跑原断言并加 en 一轮

**Files:**
- Modify: `install.sh`
- Modify: `scripts/test-install.sh`

- [ ] **Step 1: 先给 test-install.sh 加英文一轮（失败的测试）**

在 `scripts/test-install.sh` 顶部 `set -eu` 之后加 `export RAILSE_LANG=zh`（原有中文断言照旧）。在文件末尾、总结之前加一段：

```sh
# 英文一轮：同一个成功路径在 RAILSE_LANG=en 下跑，输出里不得有中日韩字符，且要有英文的校验通过提示
echo "== english"
out="$(RAILSE_LANG=en RAILSE_BASE_URL="$BASE_URL" RAILSE_INSTALL_DIR="$TMP/en-bin" sh "$ROOT/install.sh" 2>&1)" || { echo "$out"; echo "english install failed"; exit 1; }
if printf '%s' "$out" | grep -q '[一-鿿]'; then
  printf '%s\n' "$out"; echo "english run printed Chinese"; exit 1
fi
printf '%s' "$out" | grep -q 'sha256 verified' || { printf '%s\n' "$out"; echo "missing english success line"; exit 1; }
```

（`BASE_URL`、`TMP`、`ROOT` 换成脚本里已有的变量名。）

- [ ] **Step 2: 跑确认失败**

Run: `scripts/test-install.sh`
Expected: 英文一轮 FAIL："english run printed Chinese"。

- [ ] **Step 3: 实现**

`install.sh` 在 `set -eu` 之后加语言判定与取文案函数（POSIX sh，不用 eval）：

```sh
lang=en
case "${RAILSE_LANG:-${LC_ALL:-${LC_MESSAGES:-${LANG:-}}}}" in zh*|ZH*) lang=zh ;; esac

# t <key>：把该键在当前语言下的 printf 格式串放进 $fmt。键名用英文小写加下划线，两种语言成对出现，缺一个就是 bug。
t() {
  case "$lang:$1" in
    zh:fail)            fmt='安装失败：%s\n' ;;
    en:fail)            fmt='Install failed: %s\n' ;;
    zh:verified)        fmt='sha256 校验通过：%s\n' ;;
    en:verified)        fmt='sha256 verified: %s\n' ;;
    zh:next_steps)      fmt='\n下一步（详见 docs/setup.md）：\n' ;;
    en:next_steps)      fmt='\nNext steps (details in docs/setup.md):\n' ;;
    zh:next_worker)     fmt='  工作机：railse setup all --hub https://<hub 域名> --token <接入码>，或直接运行 railse setup（终端里问答）\n' ;;
    en:next_worker)     fmt='  On a work machine: railse setup all --hub https://<hub-domain> --token <enroll-token>, or just run railse setup (interactive)\n' ;;
    zh:next_doctor)     fmt='  体检：railse doctor\n' ;;
    en:next_doctor)     fmt='  Health check: railse doctor\n' ;;
    zh:readme)          fmt='完整说明：https://github.com/%s#readme\n' ;;
    en:readme)          fmt='Full docs: https://github.com/%s#readme\n' ;;
    *) fmt="$1\n" ;;
  esac
}
say() { t "$1"; shift; printf "$fmt" "$@"; }   # say <key> [args…]
fail() { t fail; printf "$fmt" "$1" >&2; exit 1; }
```

然后把脚本里 49 处 `printf '中文…'` 与 `fail '中文'` 逐条改成 `say <key> …` / `fail "$(t …)"`，每条都在 `t` 里补 zh/en 两行；原来 `test-install.sh` 断言到的句子（"sha256 校验通过"、"警告：RAILSE_BASE_URL 是明文 http"、"校验和不符"、"checksums.txt 里没有"、"下载 checksums.txt 失败"、"下载 %s 失败"、"解析出的版本号不合法"、"不是普通文件"、"安装失败"）中文值一字不改。注释保持中文。

- [ ] **Step 4: 跑**

Run: `scripts/test-install.sh`
Expected: 原有全部用例 PASS，英文一轮 PASS。

- [ ] **Step 5: 提交**

```bash
git add install.sh scripts/test-install.sh
git commit -m "install.sh: 两套文案按 RAILSE_LANG/LANG 选；test-install 在 zh 下跑原断言并加英文一轮"
```

### Task 9: Makefile、CI、英文冒烟脚本

**Files:**
- Create: `scripts/english-smoke.sh`
- Modify: `Makefile`（`test` 目标）
- Modify: `.github/workflows/ci.yml`（`go` 作业加环境变量与冒烟步骤）

- [ ] **Step 1: 写冒烟脚本**

```sh
#!/bin/sh
# 英文冒烟：在 RAILSE_LANG=en 下跑几条不需要配置的命令，输出里不得有中日韩字符。
# 用法：scripts/english-smoke.sh <railse 二进制路径>
set -eu
bin="${1:?railse binary}"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
export RAILSE_LANG=en RAILSE_HOME="$tmp/railse" RAILSE_HUB_HOME="$tmp/hub" HOME="$tmp/home"
mkdir -p "$HOME"
run() { # run <允许的退出码列表> <命令…>
  allowed="$1"; shift
  out="$("$@" 2>&1)" && code=0 || code=$?
  case " $allowed " in *" $code "*) ;; *) printf '%s\n' "$out"; echo "exit $code not in [$allowed]: $*"; exit 1 ;; esac
  if printf '%s' "$out" | grep -q '[一-鿿］（）：，。；]'; then printf '%s\n' "$out"; echo "Chinese in output of: $*"; exit 1; fi
}
run "0"   "$bin" --help
run "0"   "$bin" version
run "0"   "$bin" lang
run "2"   "$bin"
run "2"   "$bin" setup all --help
run "2"   "$bin" bogus
run "0 1" "$bin" doctor
echo "english smoke ok"
```

Run: `chmod +x scripts/english-smoke.sh && go build -o /tmp/railse-en ./cmd/railse && scripts/english-smoke.sh /tmp/railse-en`
Expected: `english smoke ok`（若 `setup all --help` 的退出码不是 2，按实际改允许列表）。

- [ ] **Step 2: Makefile 与 CI**

Makefile 的 `test`：

```make
test: gofmt lint
	go vet ./...
	RAILSE_LANG=zh go test ./... -race
	cd web && npm ci && npm run check
```

`.github/workflows/ci.yml` 的 `go` 作业：在 `- run: go test ./... -race -count=1` 前加 `env: { RAILSE_LANG: zh }`（作业级 `env:`），并在测试步骤后加：

```yaml
      - run: go build -o /tmp/railse ./cmd/railse && scripts/english-smoke.sh /tmp/railse
```

- [ ] **Step 3: 本地整体跑一遍**

Run: `make test && scripts/test-install.sh && scripts/english-smoke.sh /tmp/railse-en`
Expected: 全绿。

- [ ] **Step 4: 提交**

```bash
git add scripts/english-smoke.sh Makefile .github/workflows/ci.yml
git commit -m "ci: go test 钉死 RAILSE_LANG=zh；英文冒烟脚本进 CI"
```

### Task 10: 文档：README 升级说明、docs/setup.md 的语言一节、英文表通读

**Files:**
- Modify: `README.md`（限制/升级段）、`README.en.md`（同段）、`docs/setup.md`

- [ ] **Step 1: README 两份的"升级"段加一句**

中文：`- 0.2 起界面跟系统语言走（LANG 不是 zh 开头就是英文）。终端没有 LANG 的中文用户运行一次 railse lang zh。`
英文：`- Since 0.2 the UI follows your system language (anything but zh* is English). To force one: railse lang zh|en.`

- [ ] **Step 2: docs/setup.md 加"语言"小节**：解析顺序三行、`railse lang`、向导首次写入、`RAILSE_LANG` 对守护进程的 launchd/systemd 单元无效时用 lang 文件。

- [ ] **Step 3: 通读 `en.go`**：按术语表核对用词一致（approve/deny/remember/hub/daemon/hook/rule/project/machine/enroll token/health check/autostart/local hub），没有感叹号，没有以 `Please` 开头的句子超过三条。

- [ ] **Step 4: 提交**

```bash
git add README.md README.en.md docs/setup.md internal/i18n/en.go
git commit -m "docs: 语言解析与 railse lang 的说明；英文表通读"
```

---

## 自查

**设计覆盖**：§1.1 语言机制与 `railse lang`（Task 1、3）；§1.2 钩子与守护进程（Task 6，含记录在案的偏差）；§1.5 install.sh（Task 8）；§1.6 测试钉死中文、完整性测试、install-script 英文门禁（Task 2、3、8、9）；§2 命令行范围：install.sh（8）、向导与 setup（4）、doctor（5）、钩子提示（6）、help/version/agent status/hub 启动行（3、7）；§3 验收第 1、2、4、5、6 步的命令行部分由上述任务支撑，第 3 步在网页计划。`railse lang` 的 `--help` 由 Task 3 覆盖。

**占位符扫描**：无 TBD/TODO；翻译量大的任务（4、5、7）给出了规则、示例与门禁，具体英文由执行者按术语表写，完整性测试与英文用例兜底。

**类型一致**：`i18n.T`/`Lookup`/`Lang`/`LangSource`/`Set`/`Reset`/`DefaultDir`/`WriteFile`/`FileName`/`EnvVar`/`ZH`/`EN` 在各任务里名称一致；`localizeReason(proto.Response) string`、`passthrough(io.Writer, string)`、`denyMessage(string) string`、`hasCJK(string) bool`（定义在 `setup_wizard_test.go`，同包其他测试可用）一致。
