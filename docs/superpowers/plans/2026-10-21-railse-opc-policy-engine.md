# Railse OPC 策略引擎 实施计划（第 1 份，共 5 份）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建好仓库骨架，并交付一个无 IO 的纯 Go 策略引擎包 `internal/policy`：风险分类、规则解析校验、三层优先级匹配、内置规则包、从决定里学规则，外加 `railse policy eval|check` 两个可手动试算的子命令。

**Architecture:** 引擎是守护进程和 hub 共用的纯函数库，输入统一载荷和上下文，输出 allow / deny / ask 及依据。shell 命令先在引号之外拆段分词，按内置命令表定级并做路径分析，取最高档；规则用结构化字段匹配，范围越具体越优先，同范围 deny 压 allow；对外与不可逆两档只有手写规则能放行。

**Tech Stack:** Go 1.24+（本机用 Homebrew 装 1.27.3）、`gopkg.in/yaml.v3 v3.0.1`、`github.com/bmatcuk/doublestar/v4 v4.10.3`、标准库 `testing`。

**设计文档：** `docs/superpowers/specs/2026-10-21-railse-opc-design.md`，本计划覆盖其第 4 节、第 1.3 节的仓库骨架、第 6.1 节第 1 层测试。

**后续四份计划（不在本计划内）：** 第 2 份 钩子实验、三个适配器与守护进程本地闭环；第 3 份 hub、存储与协议；第 4 份 PWA 与 Web Push；第 5 份 setup、doctor、发布与冒烟。

## Global Constraints

- 模块路径 `github.com/railse/railse-opc`，仓库根目录即本地 `/Users/xufei/RailseWork`。将来换 GitHub 用户名只改 go.mod 一处再 `gofmt -r` 全局替换。
- `go.mod` 的 `go` 指令为 `1.24`。
- `internal/policy` 不做任何 IO：不读文件、不访问网络、不读环境变量。所有路径和家目录由调用方通过 `Context` 传入。
- 不依赖 herdr，不引入 herdr 的任何代码或协议。
- 风险等级七档，由低到高：`read`、`write_in_project`、`unknown`、`write_outside`、`network`、`outward`、`destructive`。设计文档第 4.1 节原为六档，本计划 Task 6 把 `unknown` 补进去。
- 规则文件 `version: 1`。`source` 只能是 `builtin`、`user`、`learned`；`action` 只能是 `allow`、`deny`、`ask`。
- 底线：`outward` 与 `destructive` 只有 `source: user` 的规则能放行；`learned` 规则必须带 `risk` 且不得 ≥ `outward`。解析时校验，评估时再查一次。
- 无规则命中时一律 `ask`。`read` 和 `write_in_project` 的默认放行由内置规则包里的两条 builtin 规则实现，这样用户能看到并关掉它们。
- 每个 Task 结束时 `gofmt -l .` 无输出、`go vet ./...` 无错、`go test ./...` 全过，再提交。提交信息用中文，前缀遵循 `feat:`、`test:`、`docs:`、`chore:`。
- 许可证 Apache 2.0，LICENSE 文件在 Task 1 加入。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `go.mod`、`go.sum` | 模块定义与依赖 |
| `.gitignore` | 忽略二进制、测试产物、web 构建物 |
| `LICENSE` | Apache 2.0 全文 |
| `cmd/railse/main.go` | 入口与子命令分发（`version`、`policy`） |
| `cmd/railse/main_test.go` | 分发测试 |
| `cmd/railse/policy.go` | `railse policy eval` 与 `railse policy check` |
| `cmd/railse/policy_test.go` | 子命令测试 |
| `internal/policy/doc.go` | 包说明 |
| `internal/policy/risk.go` | `Risk` 类型、名称、解析、文本序列化、`maxRisk` |
| `internal/policy/payload.go` | `PayloadType`、`Payload`、`Context` |
| `internal/policy/types_test.go` | 以上两者的测试 |
| `internal/policy/shellsplit.go` | 引号感知的拆段、分词、重定向提取、子 shell 检测 |
| `internal/policy/shellsplit_test.go` | 拆分测试 |
| `internal/policy/paths.go` | `~` 展开、路径解析、敏感路径表、路径定级 |
| `internal/policy/paths_test.go` | 路径测试 |
| `internal/policy/commands.go` | 内置命令表（数据） |
| `internal/policy/classify_shell.go` | shell 命令分类逻辑 |
| `internal/policy/classify_shell_test.go` | 读取语料文件逐条断言 |
| `internal/policy/testdata/shell_corpus.tsv` | 命令语料：`命令<TAB>期望等级` |
| `internal/policy/classify.go` | `Classify` 入口，覆盖四种载荷 |
| `internal/policy/classify_test.go` | 入口测试 |
| `internal/policy/rules.go` | `Action`、`Source`、`Scope`、`Match`、`Rule`、`RuleSet`、解析、校验、序列化 |
| `internal/policy/rules_test.go` | 解析与校验测试 |
| `internal/policy/engine.go` | `Engine`、`Decision`、范围匹配、条件匹配、优先级、底线、暂停 |
| `internal/policy/engine_test.go` | 评估测试 |
| `internal/policy/default_rules.yaml` | 内置规则包（embed） |
| `internal/policy/defaults.go` | `DefaultRuleSet` |
| `internal/policy/defaults_test.go` | 内置包测试 |
| `internal/policy/learn.go` | `ProposeRule` |
| `internal/policy/learn_test.go` | 学习测试 |

---

### Task 1: 仓库骨架与 `railse version`

**Files:**
- Create: `go.mod`
- Create: `.gitignore`
- Create: `LICENSE`
- Create: `cmd/railse/main.go`
- Create: `cmd/railse/main_test.go`
- Create: `internal/policy/doc.go`

**Interfaces:**
- Produces: `func run(args []string, stdout, stderr io.Writer) int`（`cmd/railse` 内部），后续 Task 11 在它的 switch 里加 `policy` 分支。

- [ ] **Step 1: 安装 Go**

Run:
```bash
brew install go && go version
```
Expected: `go version go1.27.3 darwin/arm64`（小版本号可能更高）。

- [ ] **Step 2: 写 go.mod、.gitignore、LICENSE**

`go.mod`:
```
module github.com/railse/railse-opc

go 1.24
```

`.gitignore`:
```
/railse
/dist/
*.test
*.out
.DS_Store
web/node_modules/
web/dist/
```

`LICENSE`：下载 Apache 2.0 全文：
```bash
curl -fsSL https://www.apache.org/licenses/LICENSE-2.0.txt -o LICENSE && head -3 LICENSE
```
Expected: 前三行含 `Apache License` 与 `Version 2.0, January 2004`。

- [ ] **Step 3: 写失败的测试**

`cmd/railse/main_test.go`:
```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"version"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("退出码 = %d, 想要 0, stderr=%s", code, errOut.String())
	}
	if got := out.String(); !strings.HasPrefix(got, "railse ") {
		t.Fatalf("stdout = %q, 想要以 railse 开头", got)
	}
}

func TestRunUnknownSubcommand(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"nope"}, &out, &errOut)
	if code != 2 {
		t.Fatalf("退出码 = %d, 想要 2", code)
	}
	if !strings.Contains(errOut.String(), "nope") {
		t.Fatalf("stderr 应提到未知子命令, 得到 %q", errOut.String())
	}
}

func TestRunNoArgs(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run(nil, &out, &errOut); code != 2 {
		t.Fatalf("退出码 = %d, 想要 2", code)
	}
}
```

- [ ] **Step 4: 跑测试确认失败**

Run: `go test ./cmd/railse/`
Expected: FAIL，报 `undefined: run`。

- [ ] **Step 5: 写最小实现**

`cmd/railse/main.go`:
```go
// railse 是 Railse OPC 的单二进制入口。
package main

import (
	"fmt"
	"io"
	"os"
)

// version 由构建时 -ldflags "-X main.version=…" 注入。
var version = "dev"

const usage = `用法: railse <子命令> [参数]

子命令:
  version   打印版本
  policy    策略引擎工具（eval、check）`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run 分发子命令，返回进程退出码。拆成函数是为了可测。
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, "railse "+version)
		return 0
	default:
		fmt.Fprintf(stderr, "未知子命令 %q\n\n%s\n", args[0], usage)
		return 2
	}
}
```

`internal/policy/doc.go`:
```go
// Package policy 是 Railse OPC 的策略引擎：给统一载荷定风险等级，
// 按规则决定 allow、deny 或 ask。本包不做任何 IO，守护进程和 hub 共用。
package policy
```

- [ ] **Step 6: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./...`
Expected: `ok  	github.com/railse/railse-opc/cmd/railse`；`internal/policy` 显示 `no test files`。

- [ ] **Step 7: 提交**

```bash
git add go.mod .gitignore LICENSE cmd/railse/main.go cmd/railse/main_test.go internal/policy/doc.go
git commit -m "chore: 仓库骨架、Apache 2.0 许可证与 railse version"
```

---

### Task 2: 风险等级与载荷类型

**Files:**
- Create: `internal/policy/risk.go`
- Create: `internal/policy/payload.go`
- Create: `internal/policy/types_test.go`

**Interfaces:**
- Produces:
  - `type Risk int`，常量 `RiskRead < RiskWriteInProject < RiskUnknown < RiskWriteOutside < RiskNetwork < RiskOutward < RiskDestructive`
  - `func (Risk) String() string`、`func ParseRisk(string) (Risk, error)`、`MarshalText`/`UnmarshalText`
  - `func maxRisk(a, b Risk) Risk`（包内）
  - `type PayloadType string`，常量 `PayloadShell`、`PayloadFileWrite`、`PayloadMCP`、`PayloadNetwork`
  - `type Payload struct{ Type PayloadType; Command, Cwd, Path, DiffPreview, Server, Tool string; Args json.RawMessage; URL, Method string }`
  - `type Context struct{ ProjectRoot, HomeDir, MachineID string }`

- [ ] **Step 1: 写失败的测试**

`internal/policy/types_test.go`:
```go
package policy

import (
	"encoding/json"
	"testing"
)

func TestRiskOrdering(t *testing.T) {
	order := []Risk{RiskRead, RiskWriteInProject, RiskUnknown, RiskWriteOutside, RiskNetwork, RiskOutward, RiskDestructive}
	for i := 1; i < len(order); i++ {
		if !(order[i-1] < order[i]) {
			t.Fatalf("%s 应低于 %s", order[i-1], order[i])
		}
	}
}

func TestRiskStringAndParse(t *testing.T) {
	cases := map[Risk]string{
		RiskRead: "read", RiskWriteInProject: "write_in_project", RiskUnknown: "unknown",
		RiskWriteOutside: "write_outside", RiskNetwork: "network", RiskOutward: "outward", RiskDestructive: "destructive",
	}
	for r, name := range cases {
		if r.String() != name {
			t.Errorf("%d.String() = %q, 想要 %q", int(r), r.String(), name)
		}
		got, err := ParseRisk(name)
		if err != nil || got != r {
			t.Errorf("ParseRisk(%q) = %v, %v; 想要 %v", name, got, err, r)
		}
	}
	if _, err := ParseRisk("bogus"); err == nil {
		t.Error("ParseRisk(bogus) 应报错")
	}
}

func TestRiskTextRoundTrip(t *testing.T) {
	type wrap struct {
		R Risk `json:"r"`
	}
	data, err := json.Marshal(wrap{RiskOutward})
	if err != nil || string(data) != `{"r":"outward"}` {
		t.Fatalf("Marshal = %s, %v", data, err)
	}
	var w wrap
	if err := json.Unmarshal([]byte(`{"r":"destructive"}`), &w); err != nil || w.R != RiskDestructive {
		t.Fatalf("Unmarshal = %v, %v", w.R, err)
	}
}

func TestMaxRisk(t *testing.T) {
	if got := maxRisk(RiskRead, RiskNetwork); got != RiskNetwork {
		t.Fatalf("maxRisk = %s", got)
	}
	if got := maxRisk(RiskDestructive, RiskRead); got != RiskDestructive {
		t.Fatalf("maxRisk = %s", got)
	}
}

func TestPayloadJSONRoundTrip(t *testing.T) {
	in := Payload{Type: PayloadShell, Command: "git status", Cwd: "/p"}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Payload
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != PayloadShell || out.Command != "git status" || out.Cwd != "/p" {
		t.Fatalf("round trip = %+v", out)
	}
	if string(data) != `{"type":"shell","command":"git status","cwd":"/p"}` {
		t.Fatalf("空字段应省略, 得到 %s", data)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/policy/`
Expected: FAIL，报 `undefined: Risk` 等。

- [ ] **Step 3: 写最小实现**

`internal/policy/risk.go`:
```go
package policy

import "fmt"

// Risk 是请求的风险等级，数值越大越危险，复合命令取最高档。
type Risk int

const (
	RiskRead           Risk = iota // 只读、测试、检查
	RiskWriteInProject             // 项目目录内的改动
	RiskUnknown                    // 命令表之外，无法判断
	RiskWriteOutside               // 项目目录外的改动
	RiskNetwork                    // 联网、安装依赖、MCP
	RiskOutward                    // 对外：push、发布、发邮件、付款
	RiskDestructive                // 不可逆：递归删除、强推、敏感文件
)

var riskNames = [...]string{
	RiskRead:           "read",
	RiskWriteInProject: "write_in_project",
	RiskUnknown:        "unknown",
	RiskWriteOutside:   "write_outside",
	RiskNetwork:        "network",
	RiskOutward:        "outward",
	RiskDestructive:    "destructive",
}

func (r Risk) String() string {
	if r >= 0 && int(r) < len(riskNames) {
		return riskNames[r]
	}
	return fmt.Sprintf("risk(%d)", int(r))
}

// ParseRisk 把 YAML 或 JSON 里的名字解析成 Risk。
func ParseRisk(s string) (Risk, error) {
	for i, name := range riskNames {
		if name == s {
			return Risk(i), nil
		}
	}
	return 0, fmt.Errorf("未知的风险等级 %q", s)
}

func (r Risk) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

func (r *Risk) UnmarshalText(b []byte) error {
	v, err := ParseRisk(string(b))
	if err != nil {
		return err
	}
	*r = v
	return nil
}

func maxRisk(a, b Risk) Risk {
	if a > b {
		return a
	}
	return b
}
```

`internal/policy/payload.go`:
```go
package policy

import "encoding/json"

// PayloadType 是统一载荷的四种形态。
type PayloadType string

const (
	PayloadShell     PayloadType = "shell"
	PayloadFileWrite PayloadType = "file_write"
	PayloadMCP       PayloadType = "mcp"
	PayloadNetwork   PayloadType = "network"
)

// Payload 是适配器把各家工具调用翻译后的统一载荷。不同 Type 只用各自的字段。
type Payload struct {
	Type PayloadType `json:"type"`

	// shell
	Command string `json:"command,omitempty"`
	Cwd     string `json:"cwd,omitempty"` // shell 与 file_write 共用

	// file_write
	Path        string `json:"path,omitempty"`
	DiffPreview string `json:"diff_preview,omitempty"`

	// mcp
	Server string          `json:"server,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`

	// network
	URL    string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`
}

// Context 是评估一条请求时的环境，由调用方提供，本包不读环境。
type Context struct {
	ProjectRoot string // 项目根目录绝对路径，未知时为空
	HomeDir     string // 用户家目录绝对路径
	MachineID   string // 本机 id，用于 machine 范围的规则
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/policy/ -run 'TestRisk|TestMaxRisk|TestPayload' -v`
Expected: 5 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/policy/risk.go internal/policy/payload.go internal/policy/types_test.go
git commit -m "feat(policy): 风险等级与统一载荷类型"
```

---

### Task 3: shell 拆段与分词

**Files:**
- Create: `internal/policy/shellsplit.go`
- Create: `internal/policy/shellsplit_test.go`

**Interfaces:**
- Produces（包内）:
  - `type segment struct{ text string; argv []string; redirects []string; subshell bool }`
  - `func splitSegments(cmd string) []string`：在引号之外按 `&&`、`||`、`;`、`|` 拆分，去首尾空白，丢空段
  - `func parseSegment(text string) segment`：引号感知分词、去引号、提取 `>` / `>>` / `2>` / `&>` 的目标、检测 `` ` `` 与 `$(`

- [ ] **Step 1: 写失败的测试**

`internal/policy/shellsplit_test.go`:
```go
package policy

import (
	"reflect"
	"testing"
)

func TestSplitSegments(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`git status`, []string{"git status"}},
		{`git add . && git commit -m "a && b"`, []string{"git add .", `git commit -m "a && b"`}},
		{`echo "a;b" ; ls`, []string{`echo "a;b"`, "ls"}},
		{`cat x | grep y || true`, []string{"cat x", "grep y", "true"}},
		{`echo 'it''s' ; `, []string{`echo 'it''s'`}},
		{`   `, nil},
		{`a \&\& b`, []string{`a \&\& b`}},
	}
	for _, c := range cases {
		if got := splitSegments(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitSegments(%q) = %q, 想要 %q", c.in, got, c.want)
		}
	}
}

func TestParseSegment(t *testing.T) {
	cases := []struct {
		in       string
		argv     []string
		redir    []string
		subshell bool
	}{
		{`git commit -m 'hello world'`, []string{"git", "commit", "-m", "hello world"}, nil, false},
		{`echo hi > out.txt`, []string{"echo", "hi"}, []string{"out.txt"}, false},
		{`echo hi >> log`, []string{"echo", "hi"}, []string{"log"}, false},
		{`cmd 2>/dev/null`, []string{"cmd"}, []string{"/dev/null"}, false},
		{`cmd &> all.log`, []string{"cmd"}, []string{"all.log"}, false},
		{`cmd 2>&1`, []string{"cmd"}, []string{"&1"}, false},
		{`echo $(date)`, []string{"echo", "$(date)"}, nil, true},
		{"echo `date`", []string{"echo", "date"}, nil, true},
		{`echo "x > y"`, []string{"echo", "x > y"}, nil, false},
		{`sleep 2 > out`, []string{"sleep", "2"}, []string{"out"}, false},
		{`a\ b c`, []string{"a b", "c"}, nil, false},
	}
	for _, c := range cases {
		got := parseSegment(c.in)
		if !reflect.DeepEqual(got.argv, c.argv) {
			t.Errorf("parseSegment(%q).argv = %q, 想要 %q", c.in, got.argv, c.argv)
		}
		if !reflect.DeepEqual(got.redirects, c.redir) {
			t.Errorf("parseSegment(%q).redirects = %q, 想要 %q", c.in, got.redirects, c.redir)
		}
		if got.subshell != c.subshell {
			t.Errorf("parseSegment(%q).subshell = %v, 想要 %v", c.in, got.subshell, c.subshell)
		}
		if got.text != c.in {
			t.Errorf("parseSegment(%q).text = %q", c.in, got.text)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/policy/ -run 'TestSplitSegments|TestParseSegment'`
Expected: FAIL，报 `undefined: splitSegments`。

- [ ] **Step 3: 写最小实现**

`internal/policy/shellsplit.go`:
```go
package policy

import "strings"

// segment 是一条 shell 命令里由 &&、||、;、| 分隔出的一段。
type segment struct {
	text      string   // 原文
	argv      []string // 按空白分词并去引号后的参数，不含重定向
	redirects []string // >、>>、2>、&> 的目标
	subshell  bool     // 含 $( 或反引号，无法静态分析
}

// splitSegments 在引号之外按 &&、||、;、| 拆分，去掉首尾空白，丢掉空段。
func splitSegments(cmd string) []string {
	var segs []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	runes := []rune(cmd)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote != 0:
			cur.WriteRune(c)
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
			cur.WriteRune(c)
		case c == '\\' && i+1 < len(runes):
			cur.WriteRune(c)
			cur.WriteRune(runes[i+1])
			i++
		case c == '&' && i+1 < len(runes) && runes[i+1] == '&':
			flush()
			i++
		case c == '|':
			flush()
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
			}
		case c == ';':
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	return segs
}

// parseSegment 对单段做引号感知分词，提取重定向目标，检测子 shell。
func parseSegment(text string) segment {
	seg := segment{text: text}
	var tokens []string // 分词结果，重定向符号以 ">" 占位
	var cur strings.Builder
	var quote rune
	inToken := false
	flush := func() {
		if inToken {
			tokens = append(tokens, cur.String())
		}
		cur.Reset()
		inToken = false
	}
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inToken = true
		case c == '\\' && i+1 < len(runes):
			cur.WriteRune(runes[i+1])
			inToken = true
			i++
		case c == '`':
			seg.subshell = true
		case c == '$' && i+1 < len(runes) && runes[i+1] == '(':
			seg.subshell = true
			cur.WriteRune(c)
			inToken = true
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '>':
			// 紧贴在 > 前面的 1、2、& 是文件描述符，不是参数
			if s := cur.String(); inToken && (s == "1" || s == "2" || s == "&") {
				cur.Reset()
				inToken = false
			} else {
				flush()
			}
			if i+1 < len(runes) && runes[i+1] == '>' {
				i++
			}
			tokens = append(tokens, ">")
		default:
			cur.WriteRune(c)
			inToken = true
		}
	}
	flush()
	for i := 0; i < len(tokens); i++ {
		if tokens[i] == ">" {
			if i+1 < len(tokens) {
				seg.redirects = append(seg.redirects, tokens[i+1])
				i++
			}
			continue
		}
		seg.argv = append(seg.argv, tokens[i])
	}
	return seg
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/policy/ -run 'TestSplitSegments|TestParseSegment' -v`
Expected: 2 个测试 PASS。若某个用例失败，先确认期望值是否合理，再改实现，不改用例凑数。

- [ ] **Step 5: 提交**

```bash
git add internal/policy/shellsplit.go internal/policy/shellsplit_test.go
git commit -m "feat(policy): 引号感知的 shell 拆段与分词"
```

---

### Task 4: 路径解析与敏感路径

**Files:**
- Create: `internal/policy/paths.go`
- Create: `internal/policy/paths_test.go`
- Modify: `go.mod`、`go.sum`（加 doublestar）

**Interfaces:**
- Consumes: `Risk`、`Context`、`maxRisk`（Task 2）
- Produces（包内）:
  - `func expandHome(p, home string) string`
  - `func resolvePath(p, cwd, home string) string`：展开 `~`，相对路径以 cwd 为基准，`filepath.Clean`
  - `func isSensitive(abs, home string) bool`
  - `func isWithin(abs, root string) bool`
  - `func pathsRisk(paths []string, cwd string, ctx Context) Risk`：一组路径参数的最高等级；跳过 `-` 开头、`&` 开头、空串和 `/dev/` 下的路径；全部跳过时返回 `RiskRead`

- [ ] **Step 1: 加依赖**

Run:
```bash
go get github.com/bmatcuk/doublestar/v4@v4.10.3
```
Expected: `go: added github.com/bmatcuk/doublestar/v4 v4.10.3`。

- [ ] **Step 2: 写失败的测试**

`internal/policy/paths_test.go`:
```go
package policy

import "testing"

var pathCtx = Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}

func TestResolvePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"src/a.go", "/home/u/proj/src/a.go"},
		{"./b", "/home/u/proj/b"},
		{"../x", "/home/u/x"},
		{"/tmp/x", "/tmp/x"},
		{"~/.ssh/config", "/home/u/.ssh/config"},
		{"~", "/home/u"},
	}
	for _, c := range cases {
		if got := resolvePath(c.in, "/home/u/proj", "/home/u"); got != c.want {
			t.Errorf("resolvePath(%q) = %q, 想要 %q", c.in, got, c.want)
		}
	}
}

func TestIsWithin(t *testing.T) {
	cases := []struct {
		abs, root string
		want      bool
	}{
		{"/home/u/proj/a", "/home/u/proj", true},
		{"/home/u/proj", "/home/u/proj", true},
		{"/home/u/projx/a", "/home/u/proj", false},
		{"/home/u/proj/..foo", "/home/u/proj", true},
		{"/home/u", "/home/u/proj", false},
		{"/x", "", false},
	}
	for _, c := range cases {
		if got := isWithin(c.abs, c.root); got != c.want {
			t.Errorf("isWithin(%q, %q) = %v", c.abs, c.root, got)
		}
	}
}

func TestIsSensitive(t *testing.T) {
	yes := []string{
		"/home/u/proj/.env", "/home/u/proj/.env.local", "/home/u/proj/certs/server.pem",
		"/home/u/.ssh/config", "/home/u/.aws/credentials", "/home/u/.claude/settings.json",
		"/home/u/proj/.claude/settings.local.json", "/home/u/.codex/config.toml", "/home/u/.npmrc",
	}
	no := []string{"/home/u/proj/src/a.go", "/home/u/proj/README.md", "/home/u/proj/.envrc.example"}
	for _, p := range yes {
		if !isSensitive(p, "/home/u") {
			t.Errorf("%s 应是敏感路径", p)
		}
	}
	for _, p := range no {
		if isSensitive(p, "/home/u") {
			t.Errorf("%s 不应是敏感路径", p)
		}
	}
}

func TestPathsRisk(t *testing.T) {
	cases := []struct {
		paths []string
		want  Risk
	}{
		{[]string{"src/a.go"}, RiskWriteInProject},
		{[]string{"-p", "build"}, RiskWriteInProject},
		{[]string{"/tmp/x"}, RiskWriteOutside},
		{[]string{"src/a.go", "/tmp/x"}, RiskWriteOutside},
		{[]string{".env"}, RiskDestructive},
		{[]string{"~/.ssh/id_rsa"}, RiskDestructive},
		{[]string{"/dev/null"}, RiskRead},
		{[]string{"&1"}, RiskRead},
		{nil, RiskRead},
	}
	for _, c := range cases {
		if got := pathsRisk(c.paths, "/home/u/proj", pathCtx); got != c.want {
			t.Errorf("pathsRisk(%q) = %s, 想要 %s", c.paths, got, c.want)
		}
	}
	// 项目根未知时以 cwd 为根
	noRoot := Context{HomeDir: "/home/u"}
	if got := pathsRisk([]string{"a"}, "/home/u/other", noRoot); got != RiskWriteInProject {
		t.Errorf("无 ProjectRoot 时应以 cwd 为根, 得到 %s", got)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/policy/ -run 'TestResolvePath|TestIsWithin|TestIsSensitive|TestPathsRisk'`
Expected: FAIL，报 `undefined: resolvePath`。

- [ ] **Step 4: 写最小实现**

`internal/policy/paths.go`:
```go
package policy

import (
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// sensitivePatterns 命中即按 destructive 处理。以 ~ 开头的在匹配前展开。
var sensitivePatterns = []string{
	"**/.env",
	"**/.env.*",
	"**/*.pem",
	"**/*.key",
	"**/id_rsa*",
	"**/id_ed25519*",
	"**/.claude/settings*.json",
	"~/.ssh/**",
	"~/.aws/**",
	"~/.gnupg/**",
	"~/.kube/**",
	"~/.config/gh/**",
	"~/.docker/config.json",
	"~/.netrc",
	"~/.npmrc",
	"~/.pypirc",
	"~/.codex/config.toml",
	"~/.gemini/settings.json",
	"~/.railse/**",
}

// expandHome 把开头的 ~ 换成家目录。
func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// resolvePath 把路径变成以 cwd 为基准的干净绝对路径。
func resolvePath(p, cwd, home string) string {
	p = expandHome(p, home)
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p)
}

// isSensitive 判断绝对路径是否命中敏感路径表。
func isSensitive(abs, home string) bool {
	for _, pat := range sensitivePatterns {
		if ok, _ := doublestar.Match(expandHome(pat, home), abs); ok {
			return true
		}
	}
	return false
}

// isWithin 判断 abs 是否在 root 内，含 root 本身。root 为空时恒为 false。
func isWithin(abs, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, "../"))
}

// pathsRisk 给一组路径参数定级：敏感路径 destructive，项目内 write_in_project，
// 其余 write_outside。跳过 - 开头的选项、& 开头的描述符、空串和 /dev/ 下的路径。
// 全部跳过时返回 RiskRead，调用方自行与基础等级取最大值。
func pathsRisk(paths []string, cwd string, ctx Context) Risk {
	root := ctx.ProjectRoot
	if root == "" {
		root = cwd
	}
	risk := RiskRead
	for _, p := range paths {
		if p == "" || strings.HasPrefix(p, "-") || strings.HasPrefix(p, "&") {
			continue
		}
		abs := resolvePath(p, cwd, ctx.HomeDir)
		if strings.HasPrefix(abs, "/dev/") {
			continue
		}
		switch {
		case isSensitive(abs, ctx.HomeDir):
			risk = maxRisk(risk, RiskDestructive)
		case isWithin(abs, root):
			risk = maxRisk(risk, RiskWriteInProject)
		default:
			risk = maxRisk(risk, RiskWriteOutside)
		}
	}
	return risk
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/policy/ -run 'TestResolvePath|TestIsWithin|TestIsSensitive|TestPathsRisk' -v`
Expected: 4 个测试 PASS。

- [ ] **Step 6: 提交**

```bash
git add go.mod go.sum internal/policy/paths.go internal/policy/paths_test.go
git commit -m "feat(policy): 路径解析、敏感路径表与路径定级"
```

---

### Task 5: 命令表与 shell 分类

**Files:**
- Create: `internal/policy/commands.go`
- Create: `internal/policy/classify_shell.go`
- Create: `internal/policy/classify_shell_test.go`
- Create: `internal/policy/testdata/shell_corpus.tsv`

**Interfaces:**
- Consumes: `splitSegments`、`parseSegment`、`segment`（Task 3）；`pathsRisk`、`resolvePath`、`isSensitive`（Task 4）；`Risk`、`Context`、`maxRisk`（Task 2）
- Produces（包内）:
  - `type cmdPattern struct{ name, sub string; any []string; bare bool; risk Risk; paths bool }` 与 `var commandTable []cmdPattern`
  - `func classifyShell(command, cwd string, ctx Context) Risk`
  - `func stripWrappers(argv []string) []string`（Task 10 的学习也用）
  - `func touchesSensitive(args []string, cwd string, ctx Context) bool`

- [ ] **Step 1: 写语料文件**

`internal/policy/testdata/shell_corpus.tsv`（制表符分隔，`#` 开头为注释；cwd 与项目根都是 `/home/u/proj`，家目录 `/home/u`）：
```
# 只读
git status	read
git diff --stat	read
ls -la	read
cat README.md	read
grep -rn TODO src	read
go test ./...	read
npm test	read
npm run lint	read
pytest -q	read
sed 's/a/b/' file.txt	read
find . -name '*.go'	read
echo hello	read
cd src	read
sh -c 'ls'	read
FOO=1 go test ./...	read
env FOO=1 npm test	read
# 项目内改动
mkdir -p build	write_in_project
touch src/new.go	write_in_project
echo hi > notes.txt	write_in_project
sed -i '' 's/a/b/' src/a.go	write_in_project
git add . && git commit -m "feat: x"	write_in_project
go build ./...	write_in_project
npm run build	write_in_project
make	write_in_project
python scripts/gen.py	write_in_project
rm build/out.bin	write_in_project
cp a.txt b.txt	write_in_project
time go build ./...	write_in_project
nohup python app.py	write_in_project
# 无法判断
foobar --flag	unknown
echo $(date)	unknown
xargs rm	unknown
find . -name '*.log' -delete	unknown
docker run -it ubuntu	unknown
# 项目外改动
cp a.txt /tmp/a.txt	write_outside
echo x > ~/notes.txt	write_outside
git config --global user.name x	write_outside
mkdir /opt/app	write_outside
# 联网
curl https://example.com	network
npm install	network
pip install requests	network
go get github.com/x/y	network
git pull	network
brew install jq	network
gh pr view 12	network
aws s3 ls	network
# 对外
git push origin main	outward
git add . && git commit -m x && git push	outward
gh pr create --fill	outward
npm publish	outward
curl -X POST https://api.example.com/x	outward
aws s3 cp a.txt s3://bucket/	outward
vercel deploy	outward
scp a.txt host:/tmp/	outward
bash -c "git push"	outward
# 不可逆
rm -rf node_modules	destructive
git push --force origin main	destructive
git reset --hard HEAD~1	destructive
sudo rm x	destructive
echo x > .env	destructive
cat .env	destructive
cat ~/.ssh/id_rsa	destructive
chmod -R 777 .	destructive
psql -c "DROP TABLE users"	destructive
kubectl delete pod x	destructive
docker system prune -f	destructive
git checkout -- src/a.go	destructive
```

- [ ] **Step 2: 写失败的测试**

`internal/policy/classify_shell_test.go`:
```go
package policy

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

var shellCtx = Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u", MachineID: "m1"}

func TestClassifyShellCorpus(t *testing.T) {
	f, err := os.Open("testdata/shell_corpus.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	line := 0
	n := 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		cmd, wantName, ok := strings.Cut(text, "\t")
		if !ok {
			t.Fatalf("第 %d 行缺少制表符: %q", line, text)
		}
		want, err := ParseRisk(strings.TrimSpace(wantName))
		if err != nil {
			t.Fatalf("第 %d 行: %v", line, err)
		}
		if got := classifyShell(cmd, "/home/u/proj", shellCtx); got != want {
			t.Errorf("第 %d 行 %q = %s, 想要 %s", line, cmd, got, want)
		}
		n++
	}
	if n < 60 {
		t.Fatalf("语料只有 %d 条，至少要 60 条", n)
	}
}

func TestClassifyShellEmpty(t *testing.T) {
	if got := classifyShell("   ", "/home/u/proj", shellCtx); got != RiskUnknown {
		t.Fatalf("空命令 = %s, 想要 unknown", got)
	}
}

func TestStripWrappers(t *testing.T) {
	cases := []struct{ in, want []string }{
		{[]string{"FOO=1", "go", "test"}, []string{"go", "test"}},
		{[]string{"env", "A=b", "npm", "test"}, []string{"npm", "test"}},
		{[]string{"time", "ls"}, []string{"ls"}},
		{[]string{"env"}, []string{"env"}},
		{[]string{"make", "VAR=1"}, []string{"make", "VAR=1"}},
	}
	for _, c := range cases {
		got := stripWrappers(c.in)
		if strings.Join(got, " ") != strings.Join(c.want, " ") {
			t.Errorf("stripWrappers(%q) = %q, 想要 %q", c.in, got, c.want)
		}
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/policy/ -run 'TestClassifyShell|TestStripWrappers'`
Expected: FAIL，报 `undefined: classifyShell`。

- [ ] **Step 4: 写命令表**

`internal/policy/commands.go`:
```go
package policy

// cmdPattern 是命令表的一行。按表中顺序匹配，第一条命中生效，所以危险的排前面。
type cmdPattern struct {
	name  string   // argv[0] 的 basename
	sub   string   // 可选：要求 argv[1] 等于它
	any   []string // 可选：argv[1:] 里出现任一即命中
	bare  bool     // 可选：只在没有任何参数时命中
	risk  Risk
	paths bool // 命中后对 argv[1:] 做路径分析，取较大值
}

func (p cmdPattern) matches(name string, argv []string) bool {
	if p.name != name {
		return false
	}
	if p.bare && len(argv) != 1 {
		return false
	}
	if p.sub != "" && (len(argv) < 2 || argv[1] != p.sub) {
		return false
	}
	if len(p.any) > 0 {
		found := false
		for _, a := range argv[1:] {
			for _, want := range p.any {
				if a == want {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// cloudMutations 是云和集群 CLI 里会改变远端状态的子命令词。
var cloudMutations = []string{
	"deploy", "apply", "up", "push", "create", "delete", "update", "destroy", "run",
	"install", "upgrade", "set", "rollout", "scale", "exec", "put-object", "cp", "sync", "rm", "mb", "rb",
}

var readOnlyCommands = []string{
	"ls", "cat", "head", "tail", "less", "more", "grep", "rg", "ag", "fd", "wc", "pwd", "which", "whereis",
	"type", "env", "printenv", "date", "whoami", "uname", "file", "stat", "du", "df", "tree", "diff", "cmp",
	"sort", "uniq", "cut", "awk", "jq", "yq", "man", "true", "false", "sleep", "test", "[", "basename",
	"dirname", "realpath", "readlink", "md5", "shasum", "sha256sum", "base64", "hexdump", "xxd", "strings",
	"column", "tr", "seq", "expr", "bc", "echo", "printf", "cd", "export", "alias", "set", "unset", "read",
	"exit", "sed", "find", "pytest", "jest", "vitest", "mocha", "rspec", "phpunit", "eslint",
	"golangci-lint", "mypy", "git-lfs",
}

var commandTable = buildCommandTable()

func buildCommandTable() []cmdPattern {
	t := []cmdPattern{
		// ---- 不可逆 ----
		{name: "sudo", risk: RiskDestructive},
		{name: "rm", any: []string{"-r", "-rf", "-fr", "-R", "-Rf", "-fR", "-rF", "--recursive"}, risk: RiskDestructive},
		{name: "git", sub: "reset", any: []string{"--hard"}, risk: RiskDestructive},
		{name: "git", sub: "push", any: []string{"-f", "--force", "--force-with-lease", "-d", "--delete"}, risk: RiskDestructive},
		{name: "git", sub: "clean", risk: RiskDestructive},
		{name: "git", sub: "branch", any: []string{"-D"}, risk: RiskDestructive},
		{name: "git", sub: "checkout", any: []string{"--"}, risk: RiskDestructive},
		{name: "git", sub: "restore", risk: RiskDestructive},
		{name: "git", sub: "stash", any: []string{"drop", "clear"}, risk: RiskDestructive},
		{name: "docker", sub: "system", any: []string{"prune"}, risk: RiskDestructive},
		{name: "docker", sub: "volume", any: []string{"rm", "prune"}, risk: RiskDestructive},
		{name: "docker", sub: "rm", any: []string{"-f", "--force"}, risk: RiskDestructive},
		{name: "kubectl", sub: "delete", risk: RiskDestructive},
		{name: "terraform", sub: "destroy", risk: RiskDestructive},
		{name: "dd", risk: RiskDestructive},
		{name: "shred", risk: RiskDestructive},
		{name: "mkfs", risk: RiskDestructive},
		{name: "diskutil", risk: RiskDestructive},
		{name: "chmod", any: []string{"-R", "--recursive"}, risk: RiskDestructive},
		{name: "chown", risk: RiskDestructive},
		{name: "killall", risk: RiskDestructive},
		{name: "pkill", risk: RiskDestructive},
		{name: "launchctl", risk: RiskDestructive},
		{name: "systemctl", risk: RiskDestructive},
		{name: "crontab", risk: RiskDestructive},
		// ---- 对外 ----
		{name: "git", sub: "push", risk: RiskOutward},
		{name: "gh", sub: "pr", any: []string{"create", "merge", "close", "comment", "review", "edit", "ready", "reopen"}, risk: RiskOutward},
		{name: "gh", sub: "issue", any: []string{"create", "close", "comment", "edit", "delete", "reopen"}, risk: RiskOutward},
		{name: "gh", sub: "release", risk: RiskOutward},
		{name: "gh", sub: "repo", any: []string{"create", "delete", "edit", "fork", "rename", "archive"}, risk: RiskOutward},
		{name: "gh", sub: "api", any: []string{"-X", "--method", "-f", "-F", "--field", "--raw-field", "--input"}, risk: RiskOutward},
		{name: "npm", sub: "publish", risk: RiskOutward},
		{name: "pnpm", sub: "publish", risk: RiskOutward},
		{name: "yarn", sub: "publish", risk: RiskOutward},
		{name: "cargo", sub: "publish", risk: RiskOutward},
		{name: "gem", sub: "push", risk: RiskOutward},
		{name: "twine", sub: "upload", risk: RiskOutward},
		{name: "poetry", sub: "publish", risk: RiskOutward},
		{name: "docker", sub: "push", risk: RiskOutward},
		{name: "vercel", risk: RiskOutward},
		{name: "netlify", sub: "deploy", risk: RiskOutward},
		{name: "fly", sub: "deploy", risk: RiskOutward},
		{name: "flyctl", sub: "deploy", risk: RiskOutward},
		{name: "wrangler", any: []string{"deploy", "publish"}, risk: RiskOutward},
		{name: "firebase", sub: "deploy", risk: RiskOutward},
		{name: "aws", any: cloudMutations, risk: RiskOutward},
		{name: "gcloud", any: cloudMutations, risk: RiskOutward},
		{name: "az", any: cloudMutations, risk: RiskOutward},
		{name: "kubectl", any: cloudMutations, risk: RiskOutward},
		{name: "helm", any: cloudMutations, risk: RiskOutward},
		{name: "terraform", any: cloudMutations, risk: RiskOutward},
		{name: "pulumi", any: cloudMutations, risk: RiskOutward},
		{name: "sam", sub: "deploy", risk: RiskOutward},
		{name: "serverless", sub: "deploy", risk: RiskOutward},
		{name: "cdk", sub: "deploy", risk: RiskOutward},
		{name: "heroku", risk: RiskOutward},
		{name: "railway", risk: RiskOutward},
		{name: "scp", risk: RiskOutward},
		{name: "rsync", risk: RiskOutward},
		{name: "mail", risk: RiskOutward},
		{name: "sendmail", risk: RiskOutward},
		{name: "msmtp", risk: RiskOutward},
		{name: "stripe", risk: RiskOutward},
		{name: "twilio", risk: RiskOutward},
		{name: "curl", any: []string{"-X", "--request", "-d", "--data", "--data-raw", "--data-binary", "--data-urlencode", "-F", "--form", "-T", "--upload-file"}, risk: RiskOutward},
		{name: "wget", any: []string{"--post-data", "--post-file", "--method"}, risk: RiskOutward},
		{name: "http", any: []string{"POST", "PUT", "PATCH", "DELETE"}, risk: RiskOutward},
		{name: "xh", any: []string{"POST", "PUT", "PATCH", "DELETE"}, risk: RiskOutward},
		// ---- 联网 ----
		{name: "curl", risk: RiskNetwork},
		{name: "wget", risk: RiskNetwork},
		{name: "http", risk: RiskNetwork},
		{name: "xh", risk: RiskNetwork},
		{name: "ssh", risk: RiskNetwork},
		{name: "nc", risk: RiskNetwork},
		{name: "ping", risk: RiskNetwork},
		{name: "dig", risk: RiskNetwork},
		{name: "nslookup", risk: RiskNetwork},
		{name: "host", risk: RiskNetwork},
		{name: "traceroute", risk: RiskNetwork},
		{name: "telnet", risk: RiskNetwork},
		{name: "npm", any: []string{"install", "i", "add", "ci", "update", "upgrade", "link", "audit", "view", "outdated"}, risk: RiskNetwork},
		{name: "npx", risk: RiskNetwork},
		{name: "pnpm", any: []string{"install", "i", "add", "update", "up", "dlx", "audit", "outdated"}, risk: RiskNetwork},
		{name: "yarn", any: []string{"install", "add", "upgrade", "up", "dlx", "audit", "outdated"}, risk: RiskNetwork},
		{name: "yarn", bare: true, risk: RiskNetwork},
		{name: "bun", any: []string{"install", "i", "add", "update", "x"}, risk: RiskNetwork},
		{name: "bunx", risk: RiskNetwork},
		{name: "pip", any: []string{"install", "download"}, risk: RiskNetwork},
		{name: "pip3", any: []string{"install", "download"}, risk: RiskNetwork},
		{name: "pipx", risk: RiskNetwork},
		{name: "uv", any: []string{"add", "sync", "pip", "lock", "tool", "run"}, risk: RiskNetwork},
		{name: "uvx", risk: RiskNetwork},
		{name: "poetry", any: []string{"add", "install", "update", "lock"}, risk: RiskNetwork},
		{name: "go", sub: "get", risk: RiskNetwork},
		{name: "go", sub: "install", risk: RiskNetwork},
		{name: "go", sub: "mod", any: []string{"download", "tidy"}, risk: RiskNetwork},
		{name: "cargo", any: []string{"add", "fetch", "install", "update"}, risk: RiskNetwork},
		{name: "brew", risk: RiskNetwork},
		{name: "apt", risk: RiskNetwork},
		{name: "apt-get", risk: RiskNetwork},
		{name: "gem", sub: "install", risk: RiskNetwork},
		{name: "composer", risk: RiskNetwork},
		{name: "mise", risk: RiskNetwork},
		{name: "git", sub: "fetch", risk: RiskNetwork},
		{name: "git", sub: "pull", risk: RiskNetwork},
		{name: "git", sub: "clone", risk: RiskNetwork},
		{name: "git", sub: "ls-remote", risk: RiskNetwork},
		{name: "git", sub: "submodule", any: []string{"update", "add"}, risk: RiskNetwork},
		{name: "docker", sub: "pull", risk: RiskNetwork},
		{name: "docker", sub: "build", risk: RiskNetwork},
		{name: "docker", sub: "login", risk: RiskNetwork},
		{name: "gh", sub: "api", risk: RiskNetwork},
		{name: "gh", sub: "pr", risk: RiskNetwork},
		{name: "gh", sub: "issue", risk: RiskNetwork},
		{name: "gh", sub: "repo", risk: RiskNetwork},
		{name: "gh", sub: "run", risk: RiskNetwork},
		{name: "gh", sub: "auth", risk: RiskNetwork},
		{name: "gh", sub: "search", risk: RiskNetwork},
		{name: "aws", risk: RiskNetwork},
		{name: "gcloud", risk: RiskNetwork},
		{name: "az", risk: RiskNetwork},
		{name: "kubectl", risk: RiskNetwork},
		{name: "helm", risk: RiskNetwork},
		{name: "terraform", risk: RiskNetwork},
		{name: "pulumi", risk: RiskNetwork},
		// ---- 明确无法判断 ----
		{name: "find", any: []string{"-delete", "-exec", "-execdir", "-ok", "-okdir"}, risk: RiskUnknown},
		{name: "xargs", risk: RiskUnknown},
		{name: "source", risk: RiskUnknown},
		{name: ".", risk: RiskUnknown},
		{name: "eval", risk: RiskUnknown},
		{name: "docker", sub: "run", risk: RiskUnknown},
		{name: "docker", sub: "compose", risk: RiskUnknown},
		{name: "docker-compose", risk: RiskUnknown},
		// ---- 项目内改动，带路径分析 ----
		{name: "mkdir", risk: RiskWriteInProject, paths: true},
		{name: "touch", risk: RiskWriteInProject, paths: true},
		{name: "cp", risk: RiskWriteInProject, paths: true},
		{name: "mv", risk: RiskWriteInProject, paths: true},
		{name: "ln", risk: RiskWriteInProject, paths: true},
		{name: "rm", risk: RiskWriteInProject, paths: true},
		{name: "rmdir", risk: RiskWriteInProject, paths: true},
		{name: "chmod", risk: RiskWriteInProject, paths: true},
		{name: "tee", risk: RiskWriteInProject, paths: true},
		{name: "patch", risk: RiskWriteInProject, paths: true},
		{name: "unzip", risk: RiskWriteInProject, paths: true},
		{name: "tar", risk: RiskWriteInProject, paths: true},
		{name: "sed", any: []string{"-i", "--in-place"}, risk: RiskWriteInProject, paths: true},
		// ---- git：只读子命令在前，其余项目内改动 ----
		{name: "git", sub: "stash", any: []string{"list", "show"}, risk: RiskRead},
		{name: "git", sub: "remote", any: []string{"add", "remove", "rm", "set-url", "rename"}, risk: RiskWriteInProject},
		{name: "git", sub: "config", any: []string{"--global", "--system"}, risk: RiskWriteOutside},
		{name: "git", sub: "status", risk: RiskRead},
		{name: "git", sub: "diff", risk: RiskRead},
		{name: "git", sub: "log", risk: RiskRead},
		{name: "git", sub: "show", risk: RiskRead},
		{name: "git", sub: "blame", risk: RiskRead},
		{name: "git", sub: "rev-parse", risk: RiskRead},
		{name: "git", sub: "ls-files", risk: RiskRead},
		{name: "git", sub: "remote", risk: RiskRead},
		{name: "git", sub: "describe", risk: RiskRead},
		{name: "git", sub: "shortlog", risk: RiskRead},
		{name: "git", sub: "reflog", risk: RiskRead},
		{name: "git", sub: "cat-file", risk: RiskRead},
		{name: "git", sub: "grep", risk: RiskRead},
		{name: "git", sub: "version", risk: RiskRead},
		{name: "git", sub: "help", risk: RiskRead},
		{name: "git", any: []string{"--version"}, risk: RiskRead},
		{name: "git", risk: RiskWriteInProject},
		// ---- go ----
		{name: "go", sub: "test", risk: RiskRead},
		{name: "go", sub: "vet", risk: RiskRead},
		{name: "go", sub: "version", risk: RiskRead},
		{name: "go", sub: "list", risk: RiskRead},
		{name: "go", sub: "env", risk: RiskRead},
		{name: "go", sub: "doc", risk: RiskRead},
		{name: "go", risk: RiskWriteInProject},
		// ---- JS 包管理器：测试与检查只读，其余脚本项目内改动 ----
		{name: "npm", sub: "test", risk: RiskRead},
		{name: "npm", sub: "run", any: []string{"test", "lint", "typecheck", "check", "test:unit", "test:e2e", "format:check", "lint:check"}, risk: RiskRead},
		{name: "npm", sub: "ls", risk: RiskRead},
		{name: "npm", any: []string{"--version", "-v"}, risk: RiskRead},
		{name: "npm", risk: RiskWriteInProject},
		{name: "pnpm", sub: "test", risk: RiskRead},
		{name: "pnpm", sub: "run", any: []string{"test", "lint", "typecheck", "check", "test:unit", "test:e2e", "format:check", "lint:check"}, risk: RiskRead},
		{name: "pnpm", sub: "lint", risk: RiskRead},
		{name: "pnpm", sub: "typecheck", risk: RiskRead},
		{name: "pnpm", risk: RiskWriteInProject},
		{name: "yarn", sub: "test", risk: RiskRead},
		{name: "yarn", sub: "run", any: []string{"test", "lint", "typecheck", "check", "test:unit", "test:e2e", "format:check", "lint:check"}, risk: RiskRead},
		{name: "yarn", sub: "lint", risk: RiskRead},
		{name: "yarn", sub: "typecheck", risk: RiskRead},
		{name: "yarn", risk: RiskWriteInProject},
		{name: "bun", sub: "test", risk: RiskRead},
		{name: "bun", sub: "run", any: []string{"test", "lint", "typecheck", "check", "test:unit", "test:e2e", "format:check", "lint:check"}, risk: RiskRead},
		{name: "bun", risk: RiskWriteInProject},
		// ---- 其他构建与检查工具 ----
		{name: "cargo", any: []string{"test", "check", "clippy", "--version"}, risk: RiskRead},
		{name: "cargo", risk: RiskWriteInProject},
		{name: "make", any: []string{"test", "check", "lint"}, risk: RiskRead},
		{name: "make", risk: RiskWriteInProject},
		{name: "just", any: []string{"test", "check", "lint"}, risk: RiskRead},
		{name: "just", risk: RiskWriteInProject},
		{name: "tsc", any: []string{"--noEmit"}, risk: RiskRead},
		{name: "tsc", risk: RiskWriteInProject},
		{name: "prettier", any: []string{"--write", "-w"}, risk: RiskWriteInProject},
		{name: "prettier", risk: RiskRead},
		{name: "ruff", sub: "format", any: []string{"--check"}, risk: RiskRead},
		{name: "ruff", sub: "format", risk: RiskWriteInProject},
		{name: "ruff", risk: RiskRead},
		{name: "black", any: []string{"--check"}, risk: RiskRead},
		{name: "black", risk: RiskWriteInProject},
		{name: "gofmt", any: []string{"-w"}, risk: RiskWriteInProject},
		{name: "gofmt", risk: RiskRead},
		{name: "biome", sub: "check", risk: RiskRead},
		{name: "biome", risk: RiskWriteInProject},
		// ---- 解释器：跑项目脚本算项目内改动 ----
		{name: "python", sub: "-m", any: []string{"pytest", "unittest"}, risk: RiskRead},
		{name: "python3", sub: "-m", any: []string{"pytest", "unittest"}, risk: RiskRead},
		{name: "python", any: []string{"--version", "-V"}, risk: RiskRead},
		{name: "python3", any: []string{"--version", "-V"}, risk: RiskRead},
		{name: "node", any: []string{"--version", "-v"}, risk: RiskRead},
		{name: "python", risk: RiskWriteInProject},
		{name: "python3", risk: RiskWriteInProject},
		{name: "node", risk: RiskWriteInProject},
		{name: "deno", risk: RiskWriteInProject},
		{name: "ruby", risk: RiskWriteInProject},
		{name: "php", risk: RiskWriteInProject},
		{name: "perl", risk: RiskWriteInProject},
		{name: "sh", risk: RiskWriteInProject},
		{name: "bash", risk: RiskWriteInProject},
		{name: "zsh", risk: RiskWriteInProject},
	}
	for _, name := range readOnlyCommands {
		t = append(t, cmdPattern{name: name, risk: RiskRead})
	}
	return t
}
```

- [ ] **Step 5: 写分类逻辑**

`internal/policy/classify_shell.go`:
```go
package policy

import (
	"path/filepath"
	"strings"
)

// classifyShell 给一条 shell 命令定级：拆段后逐段分类，取最高档。空命令为 unknown。
func classifyShell(command, cwd string, ctx Context) Risk {
	segs := splitSegments(command)
	if len(segs) == 0 {
		return RiskUnknown
	}
	risk := RiskRead
	for _, text := range segs {
		risk = maxRisk(risk, classifySegment(parseSegment(text), cwd, ctx))
	}
	return risk
}

func classifySegment(seg segment, cwd string, ctx Context) Risk {
	argv := stripWrappers(seg.argv)
	if len(argv) == 0 {
		return RiskUnknown
	}
	risk := classifyArgv(argv, cwd, ctx)
	if seg.subshell {
		risk = maxRisk(risk, RiskUnknown)
	}
	if len(seg.redirects) > 0 {
		risk = maxRisk(risk, maxRisk(RiskWriteInProject, pathsRisk(seg.redirects, cwd, ctx)))
	}
	if hasSQLDrop(seg.text) {
		risk = maxRisk(risk, RiskDestructive)
	}
	return risk
}

// classifyArgv 按命令表给单条命令定级。sh -c "…" 递归分析内层命令。
func classifyArgv(argv []string, cwd string, ctx Context) Risk {
	name := filepath.Base(argv[0])
	if isShell(name) && len(argv) >= 3 && argv[1] == "-c" {
		return classifyShell(argv[2], cwd, ctx)
	}
	for _, p := range commandTable {
		if !p.matches(name, argv) {
			continue
		}
		r := p.risk
		if p.paths {
			r = maxRisk(r, pathsRisk(argv[1:], cwd, ctx))
		}
		if touchesSensitive(argv[1:], cwd, ctx) {
			r = maxRisk(r, RiskDestructive)
		}
		return r
	}
	if touchesSensitive(argv[1:], cwd, ctx) {
		return RiskDestructive
	}
	return RiskUnknown
}

// stripWrappers 去掉开头的环境变量赋值和 env、time、nohup、nice、caffeinate 这类包装。
// 只剩一个 token 时不再剥，避免把 "env" 本身剥成空。
func stripWrappers(argv []string) []string {
	for len(argv) > 1 {
		a := argv[0]
		switch {
		case isEnvAssignment(a):
			argv = argv[1:]
		case a == "env" || a == "time" || a == "nohup" || a == "nice" || a == "caffeinate":
			argv = argv[1:]
		default:
			return argv
		}
	}
	return argv
}

func isEnvAssignment(s string) bool {
	k, _, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return false
	}
	for i, c := range k {
		if !(c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func isShell(name string) bool {
	switch name {
	case "sh", "bash", "zsh", "dash", "fish":
		return true
	}
	return false
}

// touchesSensitive 判断参数里是否有敏感路径，读敏感文件和写一样按 destructive。
func touchesSensitive(args []string, cwd string, ctx Context) bool {
	for _, a := range args {
		if a == "" || strings.HasPrefix(a, "-") {
			continue
		}
		if isSensitive(resolvePath(a, cwd, ctx.HomeDir), ctx.HomeDir) {
			return true
		}
	}
	return false
}

func hasSQLDrop(text string) bool {
	t := strings.ToLower(text)
	for _, kw := range []string{"drop table", "drop database", "drop schema", "truncate table"} {
		if strings.Contains(t, kw) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 6: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/policy/ -run 'TestClassifyShell|TestStripWrappers' -v`
Expected: 3 个测试 PASS，语料逐条通过。某条不过时，先判断是语料标错还是表缺项：语料的期望等级以设计文档第 4.1 节为准，表缺项就补表。

- [ ] **Step 7: 提交**

```bash
git add internal/policy/commands.go internal/policy/classify_shell.go internal/policy/classify_shell_test.go internal/policy/testdata/shell_corpus.tsv
git commit -m "feat(policy): 命令表、shell 分类与语料测试"
```

---

### Task 6: `Classify` 入口与设计文档修订

**Files:**
- Create: `internal/policy/classify.go`
- Create: `internal/policy/classify_test.go`
- Modify: `docs/superpowers/specs/2026-10-21-railse-opc-design.md` 第 4.1 节

**Interfaces:**
- Consumes: `classifyShell`（Task 5）、`pathsRisk`（Task 4）
- Produces: `func Classify(p Payload, ctx Context) Risk`（导出，Task 8 的引擎与 Task 10 的学习都调它）

- [ ] **Step 1: 写失败的测试**

`internal/policy/classify_test.go`:
```go
package policy

import "testing"

func TestClassify(t *testing.T) {
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}
	cases := []struct {
		name string
		p    Payload
		want Risk
	}{
		{"shell 用 Cwd", Payload{Type: PayloadShell, Command: "git status", Cwd: "/home/u/proj"}, RiskRead},
		{"shell 无 Cwd 用 ProjectRoot", Payload{Type: PayloadShell, Command: "touch a"}, RiskWriteInProject},
		{"file_write 项目内", Payload{Type: PayloadFileWrite, Path: "src/a.go", Cwd: "/home/u/proj"}, RiskWriteInProject},
		{"file_write 项目外", Payload{Type: PayloadFileWrite, Path: "/etc/hosts"}, RiskWriteOutside},
		{"file_write 敏感", Payload{Type: PayloadFileWrite, Path: ".env", Cwd: "/home/u/proj"}, RiskDestructive},
		{"file_write 无路径", Payload{Type: PayloadFileWrite}, RiskUnknown},
		{"mcp", Payload{Type: PayloadMCP, Server: "github", Tool: "create_issue"}, RiskNetwork},
		{"network", Payload{Type: PayloadNetwork, URL: "https://example.com"}, RiskNetwork},
		{"未知类型", Payload{Type: "bogus"}, RiskUnknown},
	}
	for _, c := range cases {
		if got := Classify(c.p, ctx); got != c.want {
			t.Errorf("%s: Classify = %s, 想要 %s", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/policy/ -run TestClassify$`
Expected: FAIL，报 `undefined: Classify`。

- [ ] **Step 3: 写最小实现**

`internal/policy/classify.go`:
```go
package policy

// Classify 给统一载荷定风险等级，不看任何规则。
func Classify(p Payload, ctx Context) Risk {
	cwd := p.Cwd
	if cwd == "" {
		cwd = ctx.ProjectRoot
	}
	switch p.Type {
	case PayloadShell:
		return classifyShell(p.Command, cwd, ctx)
	case PayloadFileWrite:
		if p.Path == "" {
			return RiskUnknown
		}
		return maxRisk(RiskWriteInProject, pathsRisk([]string{p.Path}, cwd, ctx))
	case PayloadMCP, PayloadNetwork:
		return RiskNetwork
	default:
		return RiskUnknown
	}
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/policy/ -run TestClassify$ -v`
Expected: PASS。

- [ ] **Step 5: 修订设计文档第 4.1 节**

把设计文档里这一行：
```
每条请求先定级，不看规则。六档由低到高：`read`、`write_in_project`、`write_outside`、`network`、`outward`、`destructive`。
```
改为：
```
每条请求先定级，不看规则。七档由低到高：`read`、`write_in_project`、`unknown`、`write_outside`、`network`、`outward`、`destructive`。`unknown` 是命令表之外无法判断的命令，默认询问。
```
并在同一节 `shell` 一条的末尾“表外命令一律 ask。”后补一句：“读敏感路径与写一样按 `destructive`。”

Run: `grep -c "七档" docs/superpowers/specs/2026-10-21-railse-opc-design.md`
Expected: `1`。

- [ ] **Step 6: 提交**

```bash
git add internal/policy/classify.go internal/policy/classify_test.go docs/superpowers/specs/2026-10-21-railse-opc-design.md
git commit -m "feat(policy): Classify 入口；设计文档风险等级补 unknown"
```

---

### Task 7: 规则模型、YAML 解析与校验

**Files:**
- Create: `internal/policy/rules.go`
- Create: `internal/policy/rules_test.go`
- Modify: `go.mod`、`go.sum`（加 yaml.v3）

**Interfaces:**
- Consumes: `Risk`、`PayloadType`（Task 2）
- Produces（导出）:
  - `type Action string`：`ActionAllow`、`ActionDeny`、`ActionAsk`
  - `type Source string`：`SourceBuiltin`、`SourceUser`、`SourceLearned`
  - `type Scope struct{ Machine, Project string }`，`func (Scope) IsGlobal() bool`，YAML 里 `global` 字符串或 `{project: …}` / `{machine: …}` 映射
  - `type Match struct{ Payload PayloadType; Risk *Risk; CommandPrefix, CommandRegex, PathGlob, MCPServer, MCPTool, Domain string }`
  - `type Rule struct{ ID string; Source Source; Scope Scope; Match Match; Action Action; Enabled *bool; CreatedFrom string }`，`func (Rule) IsEnabled() bool`
  - `type RuleSet struct{ Version int; Rules []Rule }`，`const RuleSetVersion = 1`
  - `func ParseRuleSet([]byte) (RuleSet, error)`、`func (RuleSet) Marshal() ([]byte, error)`、`func (RuleSet) Validate() error`
  - 包内：`func (Scope) specificity() int`（project 3、machine 2、global 1）

- [ ] **Step 1: 加依赖**

Run:
```bash
go get gopkg.in/yaml.v3@v3.0.1
```
Expected: `go: added gopkg.in/yaml.v3 v3.0.1`。

- [ ] **Step 2: 写失败的测试**

`internal/policy/rules_test.go`:
```go
package policy

import (
	"strings"
	"testing"
)

const validRulesYAML = `
version: 1
rules:
  - id: builtin-readonly
    source: builtin
    scope: global
    match: {risk: read}
    action: allow
  - id: user-push-railse
    source: user
    scope: {project: "~/Projects/railse-opc"}
    match: {payload: shell, command_prefix: "git push origin"}
    action: allow
  - id: learned-0042
    source: learned
    scope: {project: "~/Projects/blog"}
    match: {payload: shell, command_prefix: "npm run", risk: write_in_project}
    action: allow
    created_from: "6f1c"
  - id: machine-deny-curl
    source: user
    scope: {machine: m1}
    match: {payload: shell, command_regex: "^curl .*"}
    action: deny
    enabled: false
`

func TestParseRuleSetValid(t *testing.T) {
	rs, err := ParseRuleSet([]byte(validRulesYAML))
	if err != nil {
		t.Fatal(err)
	}
	if rs.Version != 1 || len(rs.Rules) != 4 {
		t.Fatalf("version=%d rules=%d", rs.Version, len(rs.Rules))
	}
	r0 := rs.Rules[0]
	if !r0.Scope.IsGlobal() || r0.Match.Risk == nil || *r0.Match.Risk != RiskRead || !r0.IsEnabled() {
		t.Errorf("第 1 条解析错误: %+v", r0)
	}
	r1 := rs.Rules[1]
	if r1.Scope.Project != "~/Projects/railse-opc" || r1.Match.CommandPrefix != "git push origin" || r1.Scope.specificity() != 3 {
		t.Errorf("第 2 条解析错误: %+v", r1)
	}
	r3 := rs.Rules[3]
	if r3.Scope.Machine != "m1" || r3.IsEnabled() || r3.Action != ActionDeny || r3.Scope.specificity() != 2 {
		t.Errorf("第 4 条解析错误: %+v", r3)
	}
}

func TestRuleSetMarshalRoundTrip(t *testing.T) {
	rs, err := ParseRuleSet([]byte(validRulesYAML))
	if err != nil {
		t.Fatal(err)
	}
	out, err := rs.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "scope: global") {
		t.Errorf("全局范围应序列化为字符串 global, 得到:\n%s", out)
	}
	back, err := ParseRuleSet(out)
	if err != nil {
		t.Fatalf("重新解析失败: %v\n%s", err, out)
	}
	if len(back.Rules) != 4 || back.Rules[1].Scope.Project != "~/Projects/railse-opc" {
		t.Errorf("往返后不一致: %+v", back.Rules)
	}
}

func TestParseRuleSetInvalid(t *testing.T) {
	cases := []struct {
		name, yaml, wantErr string
	}{
		{"版本错误", "version: 2\nrules: []\n", "version"},
		{"缺 id", "version: 1\nrules:\n  - source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n", "缺少 id"},
		{"id 重复", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n  - id: a\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n", "重复"},
		{"source 错误", "version: 1\nrules:\n  - id: a\n    source: robot\n    scope: global\n    match: {risk: read}\n    action: allow\n", "source"},
		{"action 错误", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {risk: read}\n    action: maybe\n", "action"},
		{"match 为空", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {}\n    action: allow\n", "match"},
		{"正则无效", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {command_regex: \"(\"}\n    action: allow\n", "command_regex"},
		{"scope 字符串非 global", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: everywhere\n    match: {risk: read}\n    action: allow\n", "global"},
		{"scope 同时两项", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: {machine: m1, project: p}\n    match: {risk: read}\n    action: allow\n", "同时"},
		{"builtin 放行 outward", "version: 1\nrules:\n  - id: a\n    source: builtin\n    scope: global\n    match: {risk: outward}\n    action: allow\n", "user"},
		{"learned 放行 destructive", "version: 1\nrules:\n  - id: a\n    source: learned\n    scope: global\n    match: {risk: destructive}\n    action: allow\n", "user"},
		{"learned 放行无 risk", "version: 1\nrules:\n  - id: a\n    source: learned\n    scope: global\n    match: {command_prefix: ls}\n    action: allow\n", "risk"},
		{"未知风险名", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {risk: scary}\n    action: allow\n", "风险等级"},
	}
	for _, c := range cases {
		_, err := ParseRuleSet([]byte(c.yaml))
		if err == nil {
			t.Errorf("%s: 应报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: 错误 %q 应包含 %q", c.name, err, c.wantErr)
		}
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/policy/ -run 'TestParseRuleSet|TestRuleSetMarshal'`
Expected: FAIL，报 `undefined: ParseRuleSet`。

- [ ] **Step 4: 写最小实现**

`internal/policy/rules.go`:
```go
package policy

import (
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Action 是规则的动作。
type Action string

const (
	ActionAllow Action = "allow"
	ActionDeny  Action = "deny"
	ActionAsk   Action = "ask"
)

// Source 是规则的来源。只有 user 来源能放行 outward 与 destructive。
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
	SourceLearned Source = "learned"
)

// Scope 是规则的作用范围。两个字段都为空表示 global。
type Scope struct {
	Machine string `yaml:"machine,omitempty"`
	Project string `yaml:"project,omitempty"` // 路径模式，支持 ~ 与 doublestar 通配
}

func (s Scope) IsGlobal() bool { return s.Machine == "" && s.Project == "" }

// specificity 越大越优先：project 3、machine 2、global 1。
func (s Scope) specificity() int {
	switch {
	case s.Project != "":
		return 3
	case s.Machine != "":
		return 2
	default:
		return 1
	}
}

// UnmarshalYAML 同时接受字符串 global 和映射 {project: …} 或 {machine: …}。
func (s *Scope) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value == "global" || n.Value == "" {
			*s = Scope{}
			return nil
		}
		return fmt.Errorf("scope 字符串只能是 global，得到 %q", n.Value)
	}
	type raw Scope
	var r raw
	if err := n.Decode(&r); err != nil {
		return err
	}
	if r.Machine != "" && r.Project != "" {
		return fmt.Errorf("scope 不能同时指定 machine 和 project")
	}
	*s = Scope(r)
	return nil
}

// MarshalYAML 把 global 写成字符串，其余写成映射。
func (s Scope) MarshalYAML() (any, error) {
	if s.IsGlobal() {
		return "global", nil
	}
	type raw Scope
	return raw(s), nil
}

// Match 是结构化匹配条件，多个字段为与关系。
type Match struct {
	Payload       PayloadType `yaml:"payload,omitempty"`
	Risk          *Risk       `yaml:"risk,omitempty"`
	CommandPrefix string      `yaml:"command_prefix,omitempty"`
	CommandRegex  string      `yaml:"command_regex,omitempty"`
	PathGlob      string      `yaml:"path_glob,omitempty"`
	MCPServer     string      `yaml:"mcp_server,omitempty"`
	MCPTool       string      `yaml:"mcp_tool,omitempty"`
	Domain        string      `yaml:"domain,omitempty"`
}

func (m Match) isEmpty() bool { return m == Match{} }

// Rule 是一条规则。Enabled 为 nil 视为启用。
type Rule struct {
	ID          string `yaml:"id"`
	Source      Source `yaml:"source"`
	Scope       Scope  `yaml:"scope"`
	Match       Match  `yaml:"match"`
	Action      Action `yaml:"action"`
	Enabled     *bool  `yaml:"enabled,omitempty"`
	CreatedFrom string `yaml:"created_from,omitempty"`
}

func (r Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// RuleSet 是规则文件的整体。
type RuleSet struct {
	Version int    `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

// RuleSetVersion 是当前支持的规则文件版本。
const RuleSetVersion = 1

// ParseRuleSet 解析并校验 YAML 规则文件。
func ParseRuleSet(data []byte) (RuleSet, error) {
	var rs RuleSet
	if err := yaml.Unmarshal(data, &rs); err != nil {
		return RuleSet{}, fmt.Errorf("解析规则文件: %w", err)
	}
	if err := rs.Validate(); err != nil {
		return RuleSet{}, err
	}
	return rs, nil
}

// Marshal 序列化为 YAML。
func (rs RuleSet) Marshal() ([]byte, error) { return yaml.Marshal(rs) }

// Validate 检查版本、id 唯一和每条规则。
func (rs RuleSet) Validate() error {
	if rs.Version != RuleSetVersion {
		return fmt.Errorf("规则文件 version 必须是 %d，得到 %d", RuleSetVersion, rs.Version)
	}
	seen := map[string]bool{}
	for i, r := range rs.Rules {
		if r.ID == "" {
			return fmt.Errorf("第 %d 条规则缺少 id", i+1)
		}
		if seen[r.ID] {
			return fmt.Errorf("规则 id 重复: %s", r.ID)
		}
		seen[r.ID] = true
		if err := r.validate(); err != nil {
			return fmt.Errorf("规则 %s: %w", r.ID, err)
		}
	}
	return nil
}

func (r Rule) validate() error {
	switch r.Source {
	case SourceBuiltin, SourceUser, SourceLearned:
	default:
		return fmt.Errorf("source 必须是 builtin、user 或 learned，得到 %q", r.Source)
	}
	switch r.Action {
	case ActionAllow, ActionDeny, ActionAsk:
	default:
		return fmt.Errorf("action 必须是 allow、deny 或 ask，得到 %q", r.Action)
	}
	if r.Match.isEmpty() {
		return fmt.Errorf("match 不能为空")
	}
	if r.Match.CommandRegex != "" {
		if _, err := regexp.Compile(r.Match.CommandRegex); err != nil {
			return fmt.Errorf("command_regex 无效: %w", err)
		}
	}
	if r.Action == ActionAllow && r.Source != SourceUser {
		if r.Match.Risk != nil && *r.Match.Risk >= RiskOutward {
			return fmt.Errorf("只有 user 来源的规则才能放行 %s", *r.Match.Risk)
		}
		if r.Source == SourceLearned && r.Match.Risk == nil {
			return fmt.Errorf("learned 规则必须带 risk")
		}
	}
	return nil
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/policy/ -run 'TestParseRuleSet|TestRuleSetMarshal' -v`
Expected: 3 个测试 PASS。“未知风险名”一例的错误来自 `Risk.UnmarshalText`，经 yaml.v3 包装后仍含“风险等级”字样。

- [ ] **Step 6: 提交**

```bash
git add go.mod go.sum internal/policy/rules.go internal/policy/rules_test.go
git commit -m "feat(policy): 规则模型、YAML 解析与底线校验"
```

---

### Task 8: 引擎与评估

**Files:**
- Create: `internal/policy/engine.go`
- Create: `internal/policy/engine_test.go`

**Interfaces:**
- Consumes: `Classify`（Task 6）、`RuleSet`/`Rule`/`Scope`/`Match`/`Action`/`Source`（Task 7）、`splitSegments`/`parseSegment`/`stripWrappers`（Task 3、5）、`expandHome`/`resolvePath`（Task 4）
- Produces（导出）:
  - `type Decision struct{ Action Action; Risk Risk; RuleID string; Reason string }`
  - `func NewEngine(rs RuleSet) (*Engine, error)`
  - `func (e *Engine) Evaluate(p Payload, ctx Context) Decision`
  - `func (e *Engine) SetPaused(bool)`、`func (e *Engine) Paused() bool`
  - 包内：`scopeMatches`、`matchRequest`、`everySegmentHasPrefix`、`hostMatches`

- [ ] **Step 1: 写失败的测试**

`internal/policy/engine_test.go`:
```go
package policy

import "testing"

func mustEngine(t *testing.T, yamlText string) *Engine {
	t.Helper()
	rs, err := ParseRuleSet([]byte(yamlText))
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(rs)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

const engineRules = `
version: 1
rules:
  - id: builtin-readonly
    source: builtin
    scope: global
    match: {risk: read}
    action: allow
  - id: builtin-write-in-project
    source: builtin
    scope: global
    match: {risk: write_in_project}
    action: allow
  - id: user-push-proj
    source: user
    scope: {project: "~/proj"}
    match: {payload: shell, command_prefix: "git push origin"}
    action: allow
  - id: global-deny-npm-run
    source: user
    scope: global
    match: {payload: shell, command_prefix: "npm run"}
    action: deny
  - id: proj-allow-npm-run
    source: user
    scope: {project: "~/proj"}
    match: {payload: shell, command_prefix: "npm run"}
    action: allow
  - id: user-allow-tmp-writes
    source: user
    scope: global
    match: {payload: file_write, path_glob: "/tmp/**"}
    action: allow
  - id: user-allow-github-mcp
    source: user
    scope: global
    match: {payload: mcp, mcp_server: github, mcp_tool: get_issue}
    action: allow
  - id: user-allow-example-domain
    source: user
    scope: global
    match: {payload: network, domain: example.com}
    action: allow
  - id: disabled-deny-ls
    source: user
    scope: global
    match: {payload: shell, command_prefix: ls}
    action: deny
    enabled: false
  - id: machine-deny-curl
    source: user
    scope: {machine: m2}
    match: {payload: shell, command_regex: "^curl "}
    action: deny
`

var projCtx = Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u", MachineID: "m1"}
var otherCtx = Context{ProjectRoot: "/home/u/other", HomeDir: "/home/u", MachineID: "m1"}

func shell(cmd string) Payload { return Payload{Type: PayloadShell, Command: cmd, Cwd: "/home/u/proj"} }

func TestEvaluateBasics(t *testing.T) {
	e := mustEngine(t, engineRules)
	cases := []struct {
		name   string
		p      Payload
		ctx    Context
		action Action
		rule   string
	}{
		{"只读命中 builtin", shell("git status"), projCtx, ActionAllow, "builtin-readonly"},
		{"项目内改动命中 builtin", shell("mkdir x"), projCtx, ActionAllow, "builtin-write-in-project"},
		{"无规则默认 ask", shell("curl https://x.y"), projCtx, ActionAsk, ""},
		{"unknown 默认 ask", shell("foobar"), projCtx, ActionAsk, ""},
		{"对外需要 user 规则且范围匹配", shell("git push origin main"), projCtx, ActionAllow, "user-push-proj"},
		{"对外范围不匹配则 ask", shell("git push origin main"), otherCtx, ActionAsk, ""},
		{"复合命令前缀要每段都匹配", shell("git push origin main && rm -rf x"), projCtx, ActionAsk, ""},
		{"项目范围压全局", shell("npm run build"), projCtx, ActionAllow, "proj-allow-npm-run"},
		{"全局 deny 在其他项目生效", shell("npm run build"), otherCtx, ActionDeny, "global-deny-npm-run"},
		{"禁用的规则不生效", shell("ls -la"), projCtx, ActionAllow, "builtin-readonly"},
		{"机器范围不匹配", shell("curl https://x.y"), projCtx, ActionAsk, ""},
		{"机器范围匹配", shell("curl https://x.y"), Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u", MachineID: "m2"}, ActionDeny, "machine-deny-curl"},
		{"path_glob", Payload{Type: PayloadFileWrite, Path: "/tmp/a.txt"}, projCtx, ActionAllow, "user-allow-tmp-writes"},
		{"path_glob 不匹配", Payload{Type: PayloadFileWrite, Path: "/etc/hosts"}, projCtx, ActionAsk, ""},
		{"mcp 命中", Payload{Type: PayloadMCP, Server: "github", Tool: "get_issue"}, projCtx, ActionAllow, "user-allow-github-mcp"},
		{"mcp 工具不同", Payload{Type: PayloadMCP, Server: "github", Tool: "create_issue"}, projCtx, ActionAsk, ""},
		{"domain 命中含子域", Payload{Type: PayloadNetwork, URL: "https://api.example.com/v1"}, projCtx, ActionAllow, "user-allow-example-domain"},
		{"domain 后缀相似不算", Payload{Type: PayloadNetwork, URL: "https://notexample.com/"}, projCtx, ActionAsk, ""},
	}
	for _, c := range cases {
		d := e.Evaluate(c.p, c.ctx)
		if d.Action != c.action || d.RuleID != c.rule {
			t.Errorf("%s: 得到 %s/%q (%s), 想要 %s/%q", c.name, d.Action, d.RuleID, d.Reason, c.action, c.rule)
		}
		if d.Reason == "" {
			t.Errorf("%s: Reason 不能为空", c.name)
		}
	}
}

func TestEvaluateFloorAtRuntime(t *testing.T) {
	// 规则本身合法（没写 risk），但命中的是 destructive 请求，非 user 来源不得放行。
	const rules = `
version: 1
rules:
  - id: builtin-rm
    source: builtin
    scope: global
    match: {payload: shell, command_prefix: rm}
    action: allow
`
	e := mustEngine(t, rules)
	d := e.Evaluate(shell("rm -rf node_modules"), projCtx)
	if d.Action != ActionAsk || d.RuleID != "" {
		t.Fatalf("builtin 不得放行 destructive, 得到 %s/%q", d.Action, d.RuleID)
	}
	d = e.Evaluate(shell("rm build/out"), projCtx)
	if d.Action != ActionAllow || d.RuleID != "builtin-rm" {
		t.Fatalf("普通 rm 应被放行, 得到 %s/%q", d.Action, d.RuleID)
	}
}

func TestEvaluateDenyBeatsAllowSameScope(t *testing.T) {
	const rules = `
version: 1
rules:
  - id: allow-ls
    source: user
    scope: global
    match: {payload: shell, command_prefix: ls}
    action: allow
  - id: deny-ls
    source: user
    scope: global
    match: {payload: shell, command_prefix: ls}
    action: deny
`
	e := mustEngine(t, rules)
	if d := e.Evaluate(shell("ls"), projCtx); d.Action != ActionDeny || d.RuleID != "deny-ls" {
		t.Fatalf("同范围 deny 应压 allow, 得到 %s/%q", d.Action, d.RuleID)
	}
}

func TestEvaluatePaused(t *testing.T) {
	e := mustEngine(t, engineRules)
	e.SetPaused(true)
	if !e.Paused() {
		t.Fatal("Paused 应为 true")
	}
	d := e.Evaluate(shell("git status"), projCtx)
	if d.Action != ActionAsk || d.Risk != RiskRead || d.RuleID != "" {
		t.Fatalf("暂停时一切都 ask, 得到 %+v", d)
	}
}

func TestEverySegmentHasPrefix(t *testing.T) {
	cases := []struct {
		cmd, prefix string
		want        bool
	}{
		{"git commit -m 'x y'", "git commit", true},
		{"git commit", "git commit", true},
		{"git committer", "git commit", false},
		{"FOO=1 git commit", "git commit", true},
		{"git commit && git push", "git commit", false},
		{"git commit; git commit --amend", "git commit", true},
		{"", "git", false},
	}
	for _, c := range cases {
		if got := everySegmentHasPrefix(c.cmd, c.prefix); got != c.want {
			t.Errorf("everySegmentHasPrefix(%q, %q) = %v", c.cmd, c.prefix, got)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/policy/ -run 'TestEvaluate|TestEverySegment'`
Expected: FAIL，报 `undefined: NewEngine`。

- [ ] **Step 3: 写最小实现**

`internal/policy/engine.go`:
```go
package policy

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Decision 是引擎对一条请求的结论。
type Decision struct {
	Action Action `json:"action"`
	Risk   Risk   `json:"risk"`
	RuleID string `json:"rule_id,omitempty"` // 命中的规则，默认决定时为空
	Reason string `json:"reason"`            // 给人看的依据
}

// Engine 持有编译好的规则。并发只读安全；SetPaused 由单个 goroutine 调用。
type Engine struct {
	rules  []compiledRule
	paused bool
}

type compiledRule struct {
	Rule
	re *regexp.Regexp
}

// NewEngine 校验规则集并预编译正则。
func NewEngine(rs RuleSet) (*Engine, error) {
	if err := rs.Validate(); err != nil {
		return nil, err
	}
	e := &Engine{}
	for _, r := range rs.Rules {
		cr := compiledRule{Rule: r}
		if r.Match.CommandRegex != "" {
			cr.re = regexp.MustCompile(r.Match.CommandRegex) // Validate 已保证可编译
		}
		e.rules = append(e.rules, cr)
	}
	return e, nil
}

// SetPaused 打开总开关后一切请求都 ask。
func (e *Engine) SetPaused(p bool) { e.paused = p }

func (e *Engine) Paused() bool { return e.paused }

// Evaluate 先定级，再按范围、条件、优先级和底线选出一条规则；没有则 ask。
func (e *Engine) Evaluate(p Payload, ctx Context) Decision {
	risk := Classify(p, ctx)
	if e.paused {
		return Decision{Action: ActionAsk, Risk: risk, Reason: "自动审批已暂停"}
	}
	var best *compiledRule
	for i := range e.rules {
		r := &e.rules[i]
		if !r.IsEnabled() || !scopeMatches(r.Scope, ctx) || !matchRequest(r, p, risk, ctx) {
			continue
		}
		if r.Action == ActionAllow && risk >= RiskOutward && r.Source != SourceUser {
			continue // 底线：只有手写规则能放行对外与不可逆操作
		}
		if best == nil || betterRule(r, best) {
			best = r
		}
	}
	if best != nil {
		return Decision{Action: best.Action, Risk: risk, RuleID: best.ID, Reason: "命中规则 " + best.ID}
	}
	return Decision{Action: ActionAsk, Risk: risk, Reason: "无规则命中，按 " + risk.String() + " 默认询问"}
}

func actionRank(a Action) int {
	switch a {
	case ActionDeny:
		return 3
	case ActionAllow:
		return 2
	default:
		return 1
	}
}

// betterRule 判断 a 是否比 b 更优先：范围更具体，其次 deny > allow > ask。
func betterRule(a, b *compiledRule) bool {
	sa, sb := a.Scope.specificity(), b.Scope.specificity()
	if sa != sb {
		return sa > sb
	}
	return actionRank(a.Action) > actionRank(b.Action)
}

func scopeMatches(s Scope, ctx Context) bool {
	switch {
	case s.Project != "":
		if ctx.ProjectRoot == "" {
			return false
		}
		pat := expandHome(s.Project, ctx.HomeDir)
		if pat == ctx.ProjectRoot {
			return true
		}
		ok, _ := doublestar.Match(pat, ctx.ProjectRoot)
		return ok
	case s.Machine != "":
		return s.Machine == ctx.MachineID
	default:
		return true
	}
}

func matchRequest(r *compiledRule, p Payload, risk Risk, ctx Context) bool {
	m := r.Match
	if m.Payload != "" && m.Payload != p.Type {
		return false
	}
	if m.Risk != nil && *m.Risk != risk {
		return false
	}
	if m.CommandPrefix != "" && !(p.Type == PayloadShell && everySegmentHasPrefix(p.Command, m.CommandPrefix)) {
		return false
	}
	if r.re != nil && !(p.Type == PayloadShell && r.re.MatchString(p.Command)) {
		return false
	}
	if m.PathGlob != "" {
		if p.Type != PayloadFileWrite {
			return false
		}
		cwd := p.Cwd
		if cwd == "" {
			cwd = ctx.ProjectRoot
		}
		abs := resolvePath(p.Path, cwd, ctx.HomeDir)
		if ok, _ := doublestar.Match(expandHome(m.PathGlob, ctx.HomeDir), abs); !ok {
			return false
		}
	}
	if m.MCPServer != "" && !(p.Type == PayloadMCP && p.Server == m.MCPServer) {
		return false
	}
	if m.MCPTool != "" && !(p.Type == PayloadMCP && p.Tool == m.MCPTool) {
		return false
	}
	if m.Domain != "" && !(p.Type == PayloadNetwork && hostMatches(p.URL, m.Domain)) {
		return false
	}
	return true
}

// everySegmentHasPrefix 要求复合命令的每一段（剥掉包装后）都以 prefix 开头，按词边界比较。
func everySegmentHasPrefix(command, prefix string) bool {
	segs := splitSegments(command)
	if len(segs) == 0 {
		return false
	}
	for _, s := range segs {
		joined := strings.Join(stripWrappers(parseSegment(s).argv), " ")
		if joined != prefix && !strings.HasPrefix(joined, prefix+" ") {
			return false
		}
	}
	return true
}

// hostMatches 判断 URL 的主机名等于 domain 或是它的子域。
func hostMatches(rawURL, domain string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return false
	}
	host := u.Hostname()
	return host == domain || strings.HasSuffix(host, "."+domain)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/policy/ -run 'TestEvaluate|TestEverySegment' -v`
Expected: 5 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/policy/engine.go internal/policy/engine_test.go
git commit -m "feat(policy): 引擎评估、范围与条件匹配、优先级与底线"
```

---

### Task 9: 内置规则包

**Files:**
- Create: `internal/policy/default_rules.yaml`
- Create: `internal/policy/defaults.go`
- Create: `internal/policy/defaults_test.go`

**Interfaces:**
- Consumes: `ParseRuleSet`、`NewEngine`、`Evaluate`
- Produces: `func DefaultRuleSet() RuleSet`（导出；守护进程和 hub 首次启动时导入）

- [ ] **Step 1: 写失败的测试**

`internal/policy/defaults_test.go`:
```go
package policy

import "testing"

func TestDefaultRuleSet(t *testing.T) {
	rs := DefaultRuleSet()
	if rs.Version != RuleSetVersion || len(rs.Rules) != 2 {
		t.Fatalf("内置包 version=%d rules=%d", rs.Version, len(rs.Rules))
	}
	for _, r := range rs.Rules {
		if r.Source != SourceBuiltin {
			t.Errorf("规则 %s 来源应是 builtin", r.ID)
		}
	}
	e, err := NewEngine(rs)
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u", MachineID: "m1"}
	cases := []struct {
		p      Payload
		action Action
	}{
		{Payload{Type: PayloadShell, Command: "ls", Cwd: "/home/u/proj"}, ActionAllow},
		{Payload{Type: PayloadShell, Command: "mkdir x", Cwd: "/home/u/proj"}, ActionAllow},
		{Payload{Type: PayloadFileWrite, Path: "src/a.go", Cwd: "/home/u/proj"}, ActionAllow},
		{Payload{Type: PayloadShell, Command: "curl https://x.y", Cwd: "/home/u/proj"}, ActionAsk},
		{Payload{Type: PayloadShell, Command: "rm -rf x", Cwd: "/home/u/proj"}, ActionAsk},
		{Payload{Type: PayloadShell, Command: "foobar", Cwd: "/home/u/proj"}, ActionAsk},
		{Payload{Type: PayloadFileWrite, Path: ".env", Cwd: "/home/u/proj"}, ActionAsk},
		{Payload{Type: PayloadMCP, Server: "s", Tool: "t"}, ActionAsk},
	}
	for _, c := range cases {
		if d := e.Evaluate(c.p, ctx); d.Action != c.action {
			t.Errorf("%+v: 得到 %s (%s), 想要 %s", c.p, d.Action, d.Reason, c.action)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/policy/ -run TestDefaultRuleSet`
Expected: FAIL，报 `undefined: DefaultRuleSet`。

- [ ] **Step 3: 写最小实现**

`internal/policy/default_rules.yaml`:
```yaml
# Railse OPC 内置规则包。只读和项目内改动默认放行，其余一律询问。
# 这两条可以在网页里关掉；关掉后对应等级也会进收件箱。
version: 1
rules:
  - id: builtin-readonly
    source: builtin
    scope: global
    match: {risk: read}
    action: allow
  - id: builtin-write-in-project
    source: builtin
    scope: global
    match: {risk: write_in_project}
    action: allow
```

`internal/policy/defaults.go`:
```go
package policy

import _ "embed"

//go:embed default_rules.yaml
var defaultRulesYAML []byte

// DefaultRuleSet 返回内置规则包。文件随二进制嵌入，解析失败属于构建错误，直接 panic。
func DefaultRuleSet() RuleSet {
	rs, err := ParseRuleSet(defaultRulesYAML)
	if err != nil {
		panic("内置规则包无效: " + err.Error())
	}
	return rs
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/policy/ -run TestDefaultRuleSet -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/policy/default_rules.yaml internal/policy/defaults.go internal/policy/defaults_test.go
git commit -m "feat(policy): 内置规则包"
```

---

### Task 10: 从决定里学规则

**Files:**
- Create: `internal/policy/learn.go`
- Create: `internal/policy/learn_test.go`

**Interfaces:**
- Consumes: `Classify`、`splitSegments`、`parseSegment`、`stripWrappers`、`resolvePath`、`Rule`/`Match`/`Scope`
- Produces（导出）:
  - `var ErrCannotLearn error`
  - `func ProposeRule(p Payload, ctx Context, requestID string) (Rule, error)`：生成 `source: learned`、`action: allow`、范围为当前项目（无项目则 global）的规则草案；`outward` 与 `destructive` 返回 `ErrCannotLearn`；复合 shell 命令返回错误

- [ ] **Step 1: 写失败的测试**

`internal/policy/learn_test.go`:
```go
package policy

import (
	"errors"
	"testing"
)

func TestProposeRuleShell(t *testing.T) {
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u", MachineID: "m1"}
	r, err := ProposeRule(Payload{Type: PayloadShell, Command: "FOO=1 git commit -m 'x y'", Cwd: "/home/u/proj"}, ctx, "6f1c2d3e-aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "learned-6f1c2d3e" || r.Source != SourceLearned || r.Action != ActionAllow || r.CreatedFrom != "6f1c2d3e-aaaa" {
		t.Errorf("基本字段错误: %+v", r)
	}
	if r.Scope.Project != "/home/u/proj" {
		t.Errorf("范围应是当前项目, 得到 %+v", r.Scope)
	}
	if r.Match.Payload != PayloadShell || r.Match.CommandPrefix != "git commit" || r.Match.Risk == nil || *r.Match.Risk != RiskWriteInProject {
		t.Errorf("匹配条件错误: %+v", r.Match)
	}
	// 草案必须能通过校验，并且真的能命中原请求
	rs := RuleSet{Version: 1, Rules: []Rule{r}}
	e, err := NewEngine(rs)
	if err != nil {
		t.Fatal(err)
	}
	if d := e.Evaluate(Payload{Type: PayloadShell, Command: "git commit -m z", Cwd: "/home/u/proj"}, ctx); d.Action != ActionAllow || d.RuleID != r.ID {
		t.Errorf("学来的规则应命中同类命令, 得到 %+v", d)
	}
}

func TestProposeRuleRefusesOutwardAndDestructive(t *testing.T) {
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}
	for _, cmd := range []string{"git push origin main", "rm -rf x", "cat .env"} {
		_, err := ProposeRule(Payload{Type: PayloadShell, Command: cmd, Cwd: "/home/u/proj"}, ctx, "id")
		if !errors.Is(err, ErrCannotLearn) {
			t.Errorf("%q 应返回 ErrCannotLearn, 得到 %v", cmd, err)
		}
	}
}

func TestProposeRuleRejectsCompoundAndEmpty(t *testing.T) {
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}
	if _, err := ProposeRule(Payload{Type: PayloadShell, Command: "ls && pwd", Cwd: "/home/u/proj"}, ctx, "id"); err == nil {
		t.Error("复合命令应报错")
	}
	if _, err := ProposeRule(Payload{Type: PayloadShell, Command: "   ", Cwd: "/home/u/proj"}, ctx, "id"); err == nil {
		t.Error("空命令应报错")
	}
}

func TestProposeRuleOtherPayloads(t *testing.T) {
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}
	r, err := ProposeRule(Payload{Type: PayloadFileWrite, Path: "docs/a.md", Cwd: "/home/u/proj"}, ctx, "f1")
	if err != nil || r.Match.PathGlob != "/home/u/proj/docs/**" {
		t.Errorf("file_write: %+v, %v", r.Match, err)
	}
	r, err = ProposeRule(Payload{Type: PayloadMCP, Server: "github", Tool: "get_issue"}, ctx, "m1")
	if err != nil || r.Match.MCPServer != "github" || r.Match.MCPTool != "get_issue" {
		t.Errorf("mcp: %+v, %v", r.Match, err)
	}
	r, err = ProposeRule(Payload{Type: PayloadNetwork, URL: "https://api.example.com/v1"}, ctx, "n1")
	if err != nil || r.Match.Domain != "api.example.com" {
		t.Errorf("network: %+v, %v", r.Match, err)
	}
	if _, err = ProposeRule(Payload{Type: PayloadNetwork, URL: "not a url"}, ctx, "n2"); err == nil {
		t.Error("无效 URL 应报错")
	}
	// 没有项目根时范围为 global
	r, err = ProposeRule(Payload{Type: PayloadMCP, Server: "s", Tool: "t"}, Context{HomeDir: "/home/u"}, "m2")
	if err != nil || !r.Scope.IsGlobal() {
		t.Errorf("无项目时应为 global: %+v, %v", r.Scope, err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/policy/ -run TestProposeRule`
Expected: FAIL，报 `undefined: ProposeRule`。

- [ ] **Step 3: 写最小实现**

`internal/policy/learn.go`:
```go
package policy

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// ErrCannotLearn 表示对外与不可逆操作不能生成自动放行规则。
var ErrCannotLearn = errors.New("对外和不可逆操作不能生成自动放行规则，请手写规则")

// ProposeRule 从一条用户已批准的请求生成 learned 规则草案。
// shell 取命令前两个词做前缀；file_write 取所在目录；mcp 取服务加工具；network 取主机名。
func ProposeRule(p Payload, ctx Context, requestID string) (Rule, error) {
	risk := Classify(p, ctx)
	if risk >= RiskOutward {
		return Rule{}, ErrCannotLearn
	}
	r := Rule{
		ID:          "learned-" + shortID(requestID),
		Source:      SourceLearned,
		Action:      ActionAllow,
		CreatedFrom: requestID,
		Match:       Match{Payload: p.Type, Risk: &risk},
	}
	if ctx.ProjectRoot != "" {
		r.Scope = Scope{Project: ctx.ProjectRoot}
	}
	cwd := p.Cwd
	if cwd == "" {
		cwd = ctx.ProjectRoot
	}
	switch p.Type {
	case PayloadShell:
		segs := splitSegments(p.Command)
		if len(segs) == 0 {
			return Rule{}, fmt.Errorf("空命令不能生成规则")
		}
		if len(segs) != 1 {
			return Rule{}, fmt.Errorf("复合命令不能生成规则，请手写")
		}
		argv := stripWrappers(parseSegment(segs[0]).argv)
		if len(argv) == 0 {
			return Rule{}, fmt.Errorf("空命令不能生成规则")
		}
		n := min(2, len(argv))
		r.Match.CommandPrefix = strings.Join(argv[:n], " ")
	case PayloadFileWrite:
		dir := filepath.Dir(resolvePath(p.Path, cwd, ctx.HomeDir))
		r.Match.PathGlob = filepath.Join(dir, "**")
	case PayloadMCP:
		r.Match.MCPServer = p.Server
		r.Match.MCPTool = p.Tool
	case PayloadNetwork:
		u, err := url.Parse(p.URL)
		if err != nil || u.Hostname() == "" {
			return Rule{}, fmt.Errorf("URL 无效，不能生成规则")
		}
		r.Match.Domain = u.Hostname()
	default:
		return Rule{}, fmt.Errorf("未知载荷类型 %q", p.Type)
	}
	return r, nil
}

func shortID(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/policy/ -v`
Expected: 包内全部测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/policy/learn.go internal/policy/learn_test.go
git commit -m "feat(policy): 从已批准的请求生成 learned 规则草案"
```

---

### Task 11: `railse policy eval` 与 `railse policy check`

**Files:**
- Create: `cmd/railse/policy.go`
- Create: `cmd/railse/policy_test.go`
- Modify: `cmd/railse/main.go`（`run` 的 switch 加 `policy` 分支）

**Interfaces:**
- Consumes: `policy.DefaultRuleSet`、`policy.ParseRuleSet`、`policy.NewEngine`、`Engine.Evaluate`、`policy.Payload`、`policy.Context`
- Produces: `func runPolicy(args []string, stdout, stderr io.Writer) int`（包内）

- [ ] **Step 1: 写失败的测试**

`cmd/railse/policy_test.go`:
```go
package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPolicyEvalDefaultRules(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"policy", "eval", "--cwd", t.TempDir(), "git status"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("退出码 %d, stderr=%s", code, errOut.String())
	}
	if !strings.HasPrefix(out.String(), "allow\tread\t") {
		t.Fatalf("stdout = %q", out.String())
	}
	out.Reset()
	code = run([]string{"policy", "eval", "--cwd", t.TempDir(), "git push origin main"}, &out, &errOut)
	if code != 0 || !strings.HasPrefix(out.String(), "ask\toutward\t") {
		t.Fatalf("code=%d stdout=%q", code, out.String())
	}
}

func TestPolicyEvalWithRulesFile(t *testing.T) {
	dir := t.TempDir()
	rules := filepath.Join(dir, "rules.yaml")
	os.WriteFile(rules, []byte("version: 1\nrules:\n  - id: allow-push\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: \"git push\"}\n    action: allow\n"), 0o644)
	var out, errOut bytes.Buffer
	code := run([]string{"policy", "eval", "--rules", rules, "--cwd", dir, "git push origin main"}, &out, &errOut)
	if code != 0 || !strings.HasPrefix(out.String(), "allow\toutward\t命中规则 allow-push") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, out.String(), errOut.String())
	}
}

func TestPolicyEvalOtherTypes(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	if code := run([]string{"policy", "eval", "--type", "file_write", "--cwd", dir, "notes.md"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "allow\twrite_in_project\t") {
		t.Fatalf("file_write: code=%d stdout=%q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"policy", "eval", "--type", "mcp", "--cwd", dir, "github/get_issue"}, &out, &errOut); code != 0 || !strings.HasPrefix(out.String(), "ask\tnetwork\t") {
		t.Fatalf("mcp: code=%d stdout=%q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"policy", "eval", "--type", "bogus", "--cwd", dir, "x"}, &out, &errOut); code != 2 {
		t.Fatalf("未知类型应退出码 2, 得到 %d", code)
	}
}

func TestPolicyCheck(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yaml")
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(good, []byte("version: 1\nrules: []\n"), 0o644)
	os.WriteFile(bad, []byte("version: 9\nrules: []\n"), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"policy", "check", good}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "有效") {
		t.Fatalf("good: code=%d stdout=%q", code, out.String())
	}
	out.Reset()
	if code := run([]string{"policy", "check", bad}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "version") {
		t.Fatalf("bad: code=%d stderr=%q", code, errOut.String())
	}
	if code := run([]string{"policy", "check"}, &out, &errOut); code != 2 {
		t.Fatalf("缺参数应退出码 2, 得到 %d", code)
	}
	if code := run([]string{"policy"}, &out, &errOut); code != 2 {
		t.Fatalf("无子命令应退出码 2, 得到 %d", code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run TestPolicy`
Expected: FAIL，`policy` 被当作未知子命令，退出码 2，断言不满足。

- [ ] **Step 3: 写实现**

`cmd/railse/policy.go`:
```go
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/railse/railse-opc/internal/policy"
)

const policyUsage = `用法:
  railse policy eval [--rules 文件] [--cwd 目录] [--project 目录] [--type shell|file_write|mcp|network] <命令|路径|服务/工具|URL>
  railse policy check <规则文件>`

func runPolicy(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, policyUsage)
		return 2
	}
	switch args[0] {
	case "eval":
		return runPolicyEval(args[1:], stdout, stderr)
	case "check":
		return runPolicyCheck(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "未知的 policy 子命令 %q\n\n%s\n", args[0], policyUsage)
		return 2
	}
}

func runPolicyEval(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("policy eval", flag.ContinueOnError)
	fs.SetOutput(stderr)
	rulesPath := fs.String("rules", "", "规则 YAML 文件，不给则用内置规则包")
	cwd := fs.String("cwd", ".", "命令的工作目录")
	project := fs.String("project", "", "项目根目录，不给则用 cwd")
	payloadType := fs.String("type", "shell", "载荷类型")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, policyUsage)
		return 2
	}
	absCwd, err := filepath.Abs(*cwd)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	absProject := absCwd
	if *project != "" {
		if absProject, err = filepath.Abs(*project); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	home, _ := os.UserHomeDir()

	rs := policy.DefaultRuleSet()
	if *rulesPath != "" {
		data, err := os.ReadFile(*rulesPath)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if rs, err = policy.ParseRuleSet(data); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	eng, err := policy.NewEngine(rs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	p := policy.Payload{Type: policy.PayloadType(*payloadType), Cwd: absCwd}
	switch p.Type {
	case policy.PayloadShell:
		p.Command = fs.Arg(0)
	case policy.PayloadFileWrite:
		p.Path = fs.Arg(0)
	case policy.PayloadNetwork:
		p.URL = fs.Arg(0)
	case policy.PayloadMCP:
		p.Server, p.Tool, _ = strings.Cut(fs.Arg(0), "/")
	default:
		fmt.Fprintf(stderr, "未知类型 %q\n", *payloadType)
		return 2
	}
	d := eng.Evaluate(p, policy.Context{ProjectRoot: absProject, HomeDir: home, MachineID: "local"})
	fmt.Fprintf(stdout, "%s\t%s\t%s\n", d.Action, d.Risk, d.Reason)
	return 0
}

func runPolicyCheck(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "用法: railse policy check <规则文件>")
		return 2
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	rs, err := policy.ParseRuleSet(data)
	if err != nil {
		fmt.Fprintln(stderr, "无效:", err)
		return 1
	}
	fmt.Fprintf(stdout, "有效，%d 条规则\n", len(rs.Rules))
	return 0
}
```

在 `cmd/railse/main.go` 的 `run` 里，`case "version":` 之后加：
```go
	case "policy":
		return runPolicy(args[1:], stdout, stderr)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./... -race`
Expected: 两个包全部 PASS。

- [ ] **Step 5: 手动试算**

Run:
```bash
go build -o railse ./cmd/railse && ./railse policy eval 'git status' && ./railse policy eval 'git push origin main' && ./railse policy eval 'rm -rf node_modules' && ./railse policy eval --type file_write .env
```
Expected 四行：
```
allow	read	命中规则 builtin-readonly
ask	outward	无规则命中，按 outward 默认询问
ask	destructive	无规则命中，按 destructive 默认询问
ask	destructive	无规则命中，按 destructive 默认询问
```

- [ ] **Step 6: 提交**

```bash
git add cmd/railse/policy.go cmd/railse/policy_test.go cmd/railse/main.go
git commit -m "feat: railse policy eval 与 check 子命令"
```

---

## 完成标准

- `go test ./... -race` 全过，`gofmt -l .` 无输出，`go vet ./...` 无错。
- `internal/policy` 不 import `os`、`net/http`、`io/fs` 之外的任何 IO 包；`defaults.go` 只用 `embed`。
- 语料文件至少 60 条且全部通过。
- `railse policy eval` 四条手动试算输出与 Task 11 Step 5 一致。
- 设计文档第 4.1 节已改为七档。

## 自检记录

- **规格覆盖**：4.1 分类 → Task 4、5、6；4.2 优先级与底线 → Task 7 校验、Task 8 评估；4.3 规则格式与 YAML → Task 7；4.4 学习 → Task 10（“七天三次建议建规则”依赖 hub 的审计数据，属第 3 份计划）；4.5 总开关 → Task 8 的 `SetPaused`（按机器暂停由守护进程层实现，属第 2 份计划）；4.6 预留 `judge` → 本计划不加动作值，第二期再扩 `Action`；1.3 仓库骨架 → Task 1；6.1 第 1 层测试 → 各 Task。
- **占位扫描**：无 TBD、TODO，每个代码步骤都给了完整代码。
- **类型一致性**：`Classify(Payload, Context) Risk`、`pathsRisk([]string, string, Context) Risk`、`stripWrappers([]string) []string`、`everySegmentHasPrefix(string, string) bool`、`Scope.specificity() int`、`Rule.IsEnabled() bool`、`Engine.Evaluate(Payload, Context) Decision` 在各 Task 中签名一致。`min` 用 Go 1.21 起的内置函数，go.mod 为 1.24 满足。
