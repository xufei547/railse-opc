# Railse OPC 适配器与守护进程本地闭环 实施计划（第 2 份，共 5 份）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在一台机器上跑通“CLI 钩子 → `railse hook` → 本地守护进程 → 策略引擎 → 放行/拒绝/不作决定”的闭环：三个适配器（Claude Code、Codex、Gemini）把各家钩子翻成统一载荷，守护进程用第 1 份计划的引擎自动决定并写审计，`railse setup hooks` 一条命令把钩子装进项目。没有 hub：引擎答 ask 时立即“不作决定”，让 CLI 弹原生框。

**Architecture:** 钩子进程只做翻译，不含策略（设计文档 1.1 原则一）。守护进程监听用户家目录下 0600 的 Unix socket，一行 JSON 进一行 JSON 出；请求走 `Decider` 接口，本计划提供 `LocalDecider`（ask 即不作决定），第 3 份计划替换为经 hub 等待手机决定的实现。任何异常都让钩子输出“不作决定”，agent 永不被卡死也永不误批（设计文档 5.4）。

**Tech Stack:** Go 1.24+，标准库为主（`net` Unix socket、`encoding/json`、`os/signal`、`crypto/rand`）；复用 `gopkg.in/yaml.v3`；不引入新依赖。

**设计文档：** `docs/superpowers/specs/2026-10-21-railse-opc-design.md` 第 1、3、5 节；钩子实测结论 `docs/superpowers/specs/2026-10-21-hook-spike-findings.md`（本计划的输入输出格式全部以它为准）；样本 `internal/adapter/testdata/`。

**前置：** 第 1 份计划已合并到 main（`internal/policy` 可用：`Classify`、`NewEngine`、`Evaluate`、`ParseRuleSet`、`DefaultRuleSet`、`Payload`、`Context`、`Decision`）。

**后续：** 第 3 份 hub、存储与协议（替换 `LocalDecider`，补 Codex 重发去重、脱敏后上报）；第 4 份 PWA；第 5 份 `setup` 的接入码与开机自启、`doctor`、发布。

## Global Constraints

- 模块路径 `github.com/xufei547/railse-opc`，`go 1.24`，不新增第三方依赖。
- `internal/policy` 仍不做 IO；本计划所有 IO 都在 `internal/agent`、`internal/hookclient`、`cmd/railse` 里。项目根目录由守护进程向上查找 `.git` 得到，再通过 `policy.Context.ProjectRoot` 传入引擎；找不到时传空字符串，引擎按项目外处理（fail closed）。
- 钩子进程（`railse hook`）绝不把自身环境变量写日志或上报：Claude 会注入 `CLAUDE_CODE_MESSAGING_TOKEN`。stdout 只能有一个 JSON 对象或为空。
- 钩子遇到任何异常（守护进程未运行、连不上、输入不认识、超时、收到 SIGTERM）都输出“不作决定”：stdout 为空、退出码 0。Gemini 绝不输出 `allow`（实测 allow 不跳过原生框），绝不输出 `ask`（headless 下永久挂起），退出码只用 0。
- 输出格式（实测）：Claude 与 Codex 放行 `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`，拒绝 `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"<理由>"}}}`；Gemini 拒绝 `{"decision":"deny","reason":"<理由>"}`。
- 钩子超时：Claude 与 Codex 的 `timeout` 单位秒，设 86400；Gemini 单位毫秒，设 86400000；守护进程对每条请求的期限为 86390 秒（设计文档 3.2）。
- 事件类钩子（`Notification`、`Stop`、`AfterAgent`、`SessionEnd`、Codex `notify`）发送后立即退出，连接超时 2 秒，Gemini 的 `Notification` 同步阻塞原生框，这条是硬要求。
- Unix socket 与配置目录权限：目录 0700，socket 0600，审计与规则文件 0600。socket 路径必须短于 100 字节（macOS 上限 104），测试用 `os.MkdirTemp("", "rs")` 而不是 `t.TempDir()`。
- 协议为一行 JSON（换行结尾），每行最大 1 MiB，带 `"v":1`；一次连接处理一条消息。
- 每个 Task 结束时 `gofmt -l .` 无输出、`go vet ./...` 无错、`go test ./... -race` 全过，再提交。提交信息用中文，前缀 `feat:`、`fix:`、`test:`、`docs:`、`chore:`。
- 许可证 Apache 2.0。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/proto/proto.go` | 钩子与守护进程之间的消息类型（`Request`、`Response`、`Event`、`Control`、`ControlReply`）与一行 JSON 的编解码 |
| `internal/proto/proto_test.go` | 编解码与长度上限测试 |
| `internal/redact/redact.go` | 命令与参数里 token、密钥、`Authorization` 头的脱敏 |
| `internal/redact/redact_test.go` | 脱敏测试 |
| `internal/adapter/adapter.go` | `Input`（翻译后的统一结构）、`Adapter` 接口、按 CLI 名取适配器、工具名到载荷的公共映射 |
| `internal/adapter/claude.go`、`claude_test.go` | Claude Code：`PermissionRequest`、`Notification`、`Stop`、`SessionEnd` 的解析与输出渲染 |
| `internal/adapter/codex.go`、`codex_test.go` | Codex：`PermissionRequest`、`Stop`、`SessionEnd` 与 `notify` 参数形式 |
| `internal/adapter/gemini.go`、`gemini_test.go` | Gemini：`BeforeTool`、`Notification`、`AfterAgent`、`SessionEnd`；只渲染 deny 与不作决定 |
| `internal/adapter/testdata/` | 已有录制样本（第 1 份计划之外由钩子实验录制） |
| `internal/agent/config.go` | 配置目录、文件路径、机器 id、期限常量 |
| `internal/agent/project.go` | 从工作目录向上找 git 根 |
| `internal/agent/audit.go` | 追加写 JSONL 审计 |
| `internal/agent/rules.go` | 规则文件初始化、加载、校验、热重载 |
| `internal/agent/requests.go` | 请求生命周期：编号、期限、取消、`Decider` 接口与 `LocalDecider` |
| `internal/agent/server.go` | Unix socket 服务：接受连接、分发 `permission`/`event`/`control` |
| `internal/agent/*_test.go` | 进程内集成测试与故障注入 |
| `internal/hookclient/client.go`、`client_test.go` | 钩子侧客户端：连接、发送、等待，带超时 |
| `cmd/railse/hook.go`、`hook_test.go` | `railse hook claude|codex|codex-notify|gemini` |
| `cmd/railse/agent.go`、`agent_test.go` | `railse agent run|status|pause|resume|reload` |
| `cmd/railse/setup.go`、`setup_test.go` | `railse setup hooks` |
| `cmd/railse/main.go` | 分发表加 `hook`、`agent`、`setup` |
| `scripts/smoke-claude.sh` | 真实 Claude Code 冒烟脚本（手动） |

---

### Task 1: 协议类型与一行 JSON 编解码

**Files:**
- Create: `internal/proto/proto.go`
- Create: `internal/proto/proto_test.go`

**Interfaces:**
- Consumes: `policy.Payload`（第 1 份计划）
- Produces（导出）:
  - `const Version = 1`、`const MaxLine = 1 << 20`
  - `type Session struct{ Tool, CLIVersion, CLISessionID, TurnID, Cwd string }`
  - `type Request struct{ V int; Kind string; Session Session; ToolName string; Payload policy.Payload; DeadlineSeconds int }`（Kind 固定 `"permission"`）
  - `type Response struct{ V int; Decision string; Reason, RuleID, RequestID, Risk string }`（Decision 为 `allow`、`deny`、`passthrough`）
  - `type Event struct{ V int; Kind string; Session Session; Message string }`（Kind 为 `question`、`done`、`session_end`）
  - `type EventAck struct{ V int; OK bool; Error string }`
  - `type Control struct{ V int; Kind string; Op string }`（Kind 固定 `"control"`，Op 为 `status`、`pause`、`resume`、`reload`）
  - `type Status struct{ Paused bool; Rules int; Pending int; MachineID, RulesPath string; Uptime string }`
  - `type ControlReply struct{ V int; OK bool; Error string; Status *Status }`
  - `func WriteLine(w io.Writer, v any) error`、`func ReadLine(r *bufio.Reader) ([]byte, error)`（超过 `MaxLine` 返回 `ErrLineTooLong`）、`func PeekKind(line []byte) (string, error)`、`func Decode(line []byte, v any) error`（校验 `v` 字段等于 `Version`）

- [ ] **Step 1: 写失败的测试**

`internal/proto/proto_test.go`:
```go
package proto

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/policy"
)

func TestRequestRoundTrip(t *testing.T) {
	in := Request{V: Version, Kind: "permission",
		Session:  Session{Tool: "claude", CLISessionID: "s1", TurnID: "p1", Cwd: "/p"},
		ToolName: "Bash",
		Payload:  policy.Payload{Type: policy.PayloadShell, Command: "git status", Cwd: "/p"},
		DeadlineSeconds: 86390}
	var buf bytes.Buffer
	if err := WriteLine(&buf, in); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "\n") || strings.Count(buf.String(), "\n") != 1 {
		t.Fatalf("应恰好一行并以换行结尾: %q", buf.String())
	}
	line, err := ReadLine(bufio.NewReader(&buf))
	if err != nil {
		t.Fatal(err)
	}
	kind, err := PeekKind(line)
	if err != nil || kind != "permission" {
		t.Fatalf("PeekKind = %q, %v", kind, err)
	}
	var out Request
	if err := Decode(line, &out); err != nil {
		t.Fatal(err)
	}
	if out.Session.CLISessionID != "s1" || out.Payload.Command != "git status" || out.DeadlineSeconds != 86390 {
		t.Fatalf("往返不一致: %+v", out)
	}
}

func TestReadLineTooLong(t *testing.T) {
	big := strings.Repeat("x", MaxLine+1) + "\n"
	_, err := ReadLine(bufio.NewReader(strings.NewReader(big)))
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, 想要 ErrLineTooLong", err)
	}
}

func TestReadLineEOFWithoutNewline(t *testing.T) {
	line, err := ReadLine(bufio.NewReader(strings.NewReader(`{"v":1,"kind":"control","op":"status"}`)))
	if err != nil {
		t.Fatalf("无换行的最后一行也应能读: %v", err)
	}
	if kind, _ := PeekKind(line); kind != "control" {
		t.Fatalf("kind = %q", kind)
	}
}

func TestDecodeRejectsWrongVersion(t *testing.T) {
	var c Control
	err := Decode([]byte(`{"v":2,"kind":"control","op":"status"}`), &c)
	if err == nil || !strings.Contains(err.Error(), "协议版本") {
		t.Fatalf("应拒绝版本 2: %v", err)
	}
}

func TestPeekKindBadJSON(t *testing.T) {
	if _, err := PeekKind([]byte("not json")); err == nil {
		t.Fatal("应报错")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/proto/`
Expected: FAIL，报 `undefined: Request` 等。

- [ ] **Step 3: 写最小实现**

`internal/proto/proto.go`:
```go
// Package proto 定义钩子进程与守护进程之间的一行 JSON 协议。
// 一次连接只处理一条消息：客户端写一行请求，服务端回一行应答。
package proto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/xufei547/railse-opc/internal/policy"
)

// Version 是当前协议版本，所有消息的 v 字段必须等于它。
const Version = 1

// MaxLine 是单行的最大字节数。
const MaxLine = 1 << 20

// ErrLineTooLong 表示单行超过 MaxLine。
var ErrLineTooLong = errors.New("协议行超过 1 MiB")

// Session 标识一个正在跑的 agent 会话。
type Session struct {
	Tool         string `json:"tool"`                   // claude、codex、gemini
	CLIVersion   string `json:"cli_version,omitempty"`  // 适配器能拿到时填
	CLISessionID string `json:"cli_session_id"`
	TurnID       string `json:"turn_id,omitempty"`      // Claude 的 prompt_id、Codex 的 turn_id
	Cwd          string `json:"cwd"`
}

// Request 是一次审批请求，Kind 固定为 permission。
type Request struct {
	V               int            `json:"v"`
	Kind            string         `json:"kind"`
	Session         Session        `json:"session"`
	ToolName        string         `json:"tool_name"`
	Payload         policy.Payload `json:"payload"`
	DeadlineSeconds int            `json:"deadline_seconds"`
}

// Response 是守护进程对审批请求的答复。Decision 为 allow、deny 或 passthrough。
type Response struct {
	V         int    `json:"v"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason,omitempty"`
	RuleID    string `json:"rule_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Risk      string `json:"risk,omitempty"`
}

// Event 是只展示不操作的事件，Kind 为 question、done 或 session_end。
type Event struct {
	V       int     `json:"v"`
	Kind    string  `json:"kind"`
	Session Session `json:"session"`
	Message string  `json:"message,omitempty"`
}

// EventAck 是事件的确认。
type EventAck struct {
	V     int    `json:"v"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Control 是本机管理命令，Kind 固定为 control。
type Control struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
	Op   string `json:"op"` // status、pause、resume、reload
}

// Status 是守护进程状态。
type Status struct {
	Paused    bool   `json:"paused"`
	Rules     int    `json:"rules"`
	Pending   int    `json:"pending"`
	MachineID string `json:"machine_id"`
	RulesPath string `json:"rules_path"`
	Uptime    string `json:"uptime"`
}

// ControlReply 是管理命令的答复。
type ControlReply struct {
	V      int     `json:"v"`
	OK     bool    `json:"ok"`
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
}

// WriteLine 把 v 编成一行 JSON 并写出，末尾带换行。
func WriteLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxLine {
		return ErrLineTooLong
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadLine 读一行（不含换行）。没有换行就到 EOF 的最后一行也算一行。
func ReadLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
		line = append(line, chunk...)
		if len(line) > MaxLine {
			return nil, ErrLineTooLong
		}
		if !isPrefix {
			return line, nil
		}
	}
}

// PeekKind 只解出 kind 字段，用于分发。
func PeekKind(line []byte) (string, error) {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return "", fmt.Errorf("解析协议行: %w", err)
	}
	return head.Kind, nil
}

// Decode 解出完整消息并校验协议版本。
func Decode(line []byte, v any) error {
	var head struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return fmt.Errorf("解析协议行: %w", err)
	}
	if head.V != Version {
		return fmt.Errorf("协议版本不匹配：想要 %d，得到 %d", Version, head.V)
	}
	return json.Unmarshal(line, v)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/proto/ -v`
Expected: 5 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/proto/proto.go internal/proto/proto_test.go
git commit -m "feat(proto): 钩子与守护进程的一行 JSON 协议"
```

---

### Task 2: 脱敏

**Files:**
- Create: `internal/redact/redact.go`
- Create: `internal/redact/redact_test.go`

**Interfaces:**
- Produces（导出）: `func String(s string) string`、`func Payload(p policy.Payload) policy.Payload`（对 Command、Path、DiffPreview、URL、Args 脱敏，Args 按字符串处理）
- 替换目标：`Bearer <x>`、`Authorization: <x>`、形如 `token=…`、`api_key=…`、`apikey=…`、`secret=…`、`password=…`、`passwd=…`、`--token <x>`、`--password <x>`、`-p<x>`（mysql 风格不做）、以及长度 ≥ 20 的 `ghp_`、`sk-`、`xox[abp]-`、`AKIA` 开头的裸 token。替换为 `<redacted>`。

- [ ] **Step 1: 写失败的测试**

`internal/redact/redact_test.go`:
```go
package redact

import (
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/policy"
)

func TestString(t *testing.T) {
	cases := []struct{ in, want string }{
		{`curl -H "Authorization: Bearer abc.def.ghi" https://x.y`, `curl -H "Authorization: <redacted>" https://x.y`},
		{`curl -H 'Authorization: Basic dXNlcjpwYXNz' https://x.y`, `curl -H 'Authorization: <redacted>' https://x.y`},
		{`export TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123`, `export TOKEN=<redacted>`},
		{`API_KEY=sk-abcdefghijklmnopqrstuvwxyz npm test`, `API_KEY=<redacted> npm test`},
		{`npm login --password hunter2 --username bob`, `npm login --password <redacted> --username bob`},
		{`echo password=secret123 > .env`, `echo password=<redacted> > .env`},
		{`git push origin main`, `git push origin main`},
		{`ls -la`, `ls -la`},
		{`curl https://x.y/?api_key=abcdefghijklmnopqrstu`, `curl https://x.y/?api_key=<redacted>`},
		{`xoxb-1234567890-abcdefghijklmnop`, `<redacted>`},
		{`AKIAIOSFODNN7EXAMPLE1`, `<redacted>`},
	}
	for _, c := range cases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}

func TestPayload(t *testing.T) {
	p := policy.Payload{Type: policy.PayloadShell, Command: "curl -H 'Authorization: Bearer zzz' https://x.y", DiffPreview: "password=abc", URL: "https://x.y/?token=abcdefghijklmnopqrstuv", Args: []byte(`{"token":"ghp_abcdefghijklmnopqrstuvwxyz0123"}`)}
	out := Payload(p)
	if strings.Contains(out.Command, "zzz") || strings.Contains(out.DiffPreview, "abc") || strings.Contains(out.URL, "abcdefghijklmnopqrstuv") || strings.Contains(string(out.Args), "ghp_") {
		t.Fatalf("未脱敏: %+v", out)
	}
	if p.Command == out.Command {
		t.Fatal("应返回新值而不是原值")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/redact/`
Expected: FAIL，报 `undefined: String`。

- [ ] **Step 3: 写最小实现**

`internal/redact/redact.go`:
```go
// Package redact 在命令、参数和预览离开本机前抹掉其中的凭据。
package redact

import (
	"regexp"

	"github.com/xufei547/railse-opc/internal/policy"
)

const placeholder = "<redacted>"

// 顺序很重要：先处理带键名的形式，再处理裸 token。
var patterns = []*regexp.Regexp{
	// Authorization: Bearer xxx / Authorization: Basic xxx
	regexp.MustCompile(`(?i)(authorization:\s*)(bearer|basic|token)?\s*[^\s'"]+`),
	// --token xxx、--password xxx、--api-key xxx
	regexp.MustCompile(`(?i)(--?(token|password|passwd|api[-_]?key|secret|auth)\s+)[^\s'"]+`),
	// token=xxx、api_key=xxx、password=xxx（含 URL 查询串与 env 赋值）
	regexp.MustCompile(`(?i)((?:^|[\s?&;'"])[A-Za-z_]*(token|api[-_]?key|apikey|secret|password|passwd|auth)[A-Za-z_]*=)[^\s&'"]+`),
	// 裸 token：GitHub、OpenAI、Slack、AWS
	regexp.MustCompile(`\b(ghp_|gho_|ghs_|github_pat_|sk-|xox[abp]-|AKIA)[A-Za-z0-9_\-]{16,}\b`),
}

// String 返回脱敏后的副本。
func String(s string) string {
	out := s
	out = patterns[0].ReplaceAllString(out, "${1}"+placeholder)
	out = patterns[1].ReplaceAllString(out, "${1}"+placeholder)
	out = patterns[2].ReplaceAllString(out, "${1}"+placeholder)
	out = patterns[3].ReplaceAllString(out, placeholder)
	return out
}

// Payload 对载荷里可能含凭据的字段脱敏，返回副本。
func Payload(p policy.Payload) policy.Payload {
	out := p
	out.Command = String(p.Command)
	out.Path = String(p.Path)
	out.DiffPreview = String(p.DiffPreview)
	out.URL = String(p.URL)
	if len(p.Args) > 0 {
		out.Args = []byte(String(string(p.Args)))
	}
	return out
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/redact/ -v`
Expected: 2 个测试 PASS。若某个用例的期望与正则实际行为不一致，先确认期望是否合理（脱敏宁可多抹），再调整正则，不要把测试改成迁就实现。

- [ ] **Step 5: 提交**

```bash
git add internal/redact/redact.go internal/redact/redact_test.go
git commit -m "feat(redact): 命令、参数与预览的凭据脱敏"
```

---

### Task 3: 适配器公共类型与 Claude Code 适配器

**Files:**
- Create: `internal/adapter/adapter.go`
- Create: `internal/adapter/claude.go`
- Create: `internal/adapter/claude_test.go`

**Interfaces:**
- Consumes: `proto.Session`（Task 1）、`policy.Payload`、`policy.PayloadType` 常量
- Produces（导出）:
  - `type Kind string`：`KindPermission`、`KindQuestion`、`KindDone`、`KindSessionEnd`、`KindIgnore`
  - `type Input struct{ Kind Kind; Session proto.Session; ToolName string; Payload policy.Payload; Message string }`
  - `type Decision struct{ Action, Reason string }`（Action 为 `allow`、`deny`、`passthrough`）
  - `type Output struct{ Stdout []byte; ExitCode int }`
  - `type Adapter interface{ Name() string; Parse(stdin []byte) (Input, error); Render(d Decision) Output; DeadlineSeconds() int }`
  - `func Get(name string) (Adapter, bool)`；`const DeadlineSeconds = 86390`
  - `const PayloadOther policy.PayloadType = "other"`（认识不了的工具，引擎按 unknown 处理）
  - 包内：`func mapTool(cli, toolName string, toolInput json.RawMessage, cwd string) (policy.Payload, error)`、`func clip(s string, n int) string`、`func passthrough() Output`、`func permissionJSON(behavior, message string) []byte`

- [ ] **Step 1: 写失败的测试**

`internal/adapter/claude_test.go`:
```go
package adapter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/policy"
)

func sample(t *testing.T, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestClaudeParsePermissionRequest(t *testing.T) {
	a, ok := Get("claude")
	if !ok {
		t.Fatal("没有 claude 适配器")
	}
	in, err := a.Parse(sample(t, "claude/2.1.280/PermissionRequest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != KindPermission || in.ToolName != "Bash" {
		t.Fatalf("Kind=%s Tool=%s", in.Kind, in.ToolName)
	}
	if in.Session.Tool != "claude" || in.Session.CLISessionID != "62ca6ca9-7d34-4035-8b37-d6eae531ec2c" || in.Session.TurnID != "d90923b6-0fe8-4ee1-a729-967ccd797f73" {
		t.Fatalf("会话解析错误: %+v", in.Session)
	}
	wantCwd := "/Users/xufei/RailseWork/.claude/worktrees/dreamy-poitras-68f89d/spike/claude"
	if in.Session.Cwd != wantCwd || in.Payload.Cwd != wantCwd {
		t.Fatalf("cwd 错误: %q / %q", in.Session.Cwd, in.Payload.Cwd)
	}
	if in.Payload.Type != policy.PayloadShell || in.Payload.Command != "git push origin main" {
		t.Fatalf("载荷错误: %+v", in.Payload)
	}
}

func TestClaudeParseEvents(t *testing.T) {
	a, _ := Get("claude")
	cases := []struct {
		file string
		kind Kind
		msg  string
	}{
		{"claude/2.1.280/Notification.permission_prompt.json", KindQuestion, "Claude needs your permission"},
		{"claude/2.1.280/Notification.idle_prompt.json", KindQuestion, "Claude is waiting for your input"},
		{"claude/2.1.280/Stop.json", KindDone, "DONE"},
		{"claude/2.1.280/SessionEnd.json", KindSessionEnd, ""},
		{"claude/2.1.280/PreToolUse.json", KindIgnore, ""},
	}
	for _, c := range cases {
		in, err := a.Parse(sample(t, c.file))
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		if in.Kind != c.kind || in.Message != c.msg {
			t.Errorf("%s: Kind=%s Message=%q, 想要 %s/%q", c.file, in.Kind, in.Message, c.kind, c.msg)
		}
		if in.Session.CLISessionID == "" {
			t.Errorf("%s: 会话号为空", c.file)
		}
	}
}

func TestClaudeToolMapping(t *testing.T) {
	a, _ := Get("claude")
	mk := func(tool string, input string) []byte {
		return []byte(`{"session_id":"s","prompt_id":"p","cwd":"/p","hook_event_name":"PermissionRequest","tool_name":"` + tool + `","tool_input":` + input + `}`)
	}
	cases := []struct {
		tool, input string
		want        policy.Payload
	}{
		{"Edit", `{"file_path":"/p/a.go","old_string":"x","new_string":"yy"}`, policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/a.go", DiffPreview: "yy", Cwd: "/p"}},
		{"Write", `{"file_path":"notes.md","content":"hello"}`, policy.Payload{Type: policy.PayloadFileWrite, Path: "notes.md", DiffPreview: "hello", Cwd: "/p"}},
		{"MultiEdit", `{"file_path":"/p/b.go","edits":[{"old_string":"a","new_string":"b"}]}`, policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/b.go", DiffPreview: "b", Cwd: "/p"}},
		{"NotebookEdit", `{"notebook_path":"/p/n.ipynb","new_source":"print(1)"}`, policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/n.ipynb", DiffPreview: "print(1)", Cwd: "/p"}},
		{"WebFetch", `{"url":"https://example.com/x","prompt":"summarize"}`, policy.Payload{Type: policy.PayloadNetwork, URL: "https://example.com/x", Method: "GET"}},
		{"WebSearch", `{"query":"golang"}`, policy.Payload{Type: policy.PayloadNetwork, URL: ""}},
		{"mcp__github__create_issue", `{"title":"t"}`, policy.Payload{Type: policy.PayloadMCP, Server: "github", Tool: "create_issue", Args: json.RawMessage(`{"title":"t"}`)}},
		{"Task", `{"prompt":"x"}`, policy.Payload{Type: PayloadOther, Tool: "Task"}},
	}
	for _, c := range cases {
		in, err := a.Parse(mk(c.tool, c.input))
		if err != nil {
			t.Errorf("%s: %v", c.tool, err)
			continue
		}
		got := in.Payload
		if got.Type != c.want.Type || got.Path != c.want.Path || got.DiffPreview != c.want.DiffPreview || got.URL != c.want.URL || got.Server != c.want.Server || got.Tool != c.want.Tool || string(got.Args) != string(c.want.Args) || got.Cwd != c.want.Cwd {
			t.Errorf("%s: 得到 %+v, 想要 %+v", c.tool, got, c.want)
		}
	}
}

func TestClaudeDiffPreviewClipped(t *testing.T) {
	a, _ := Get("claude")
	big := strings.Repeat("x", 10000)
	in, err := a.Parse([]byte(`{"session_id":"s","cwd":"/p","hook_event_name":"PermissionRequest","tool_name":"Write","tool_input":{"file_path":"a","content":"` + big + `"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Payload.DiffPreview) != 4096 {
		t.Fatalf("预览应截断到 4096 字节, 得到 %d", len(in.Payload.DiffPreview))
	}
}

func TestClaudeParseErrors(t *testing.T) {
	a, _ := Get("claude")
	if _, err := a.Parse([]byte(`not json`)); err == nil {
		t.Error("坏 JSON 应报错")
	}
	if _, err := a.Parse([]byte(`{"hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"ls"}}`)); err == nil {
		t.Error("缺 session_id 的审批请求应报错")
	}
	in, err := a.Parse([]byte(`{"session_id":"s","cwd":"/p","hook_event_name":"SomethingNew"}`))
	if err != nil || in.Kind != KindIgnore {
		t.Errorf("未知事件应为 ignore: %v %+v", err, in)
	}
}

func TestClaudeRender(t *testing.T) {
	a, _ := Get("claude")
	cases := []struct {
		d        Decision
		wantOut  string
		wantCode int
	}{
		{Decision{Action: "allow"}, `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`, 0},
		{Decision{Action: "deny", Reason: "命中规则 x"}, `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Railse：命中规则 x"}}}`, 0},
		{Decision{Action: "passthrough"}, ``, 0},
		{Decision{Action: "bogus"}, ``, 0},
	}
	for _, c := range cases {
		out := a.Render(c.d)
		if string(out.Stdout) != c.wantOut || out.ExitCode != c.wantCode {
			t.Errorf("Render(%+v) = %q/%d, 想要 %q/%d", c.d, out.Stdout, out.ExitCode, c.wantOut, c.wantCode)
		}
	}
	if a.DeadlineSeconds() != 86390 {
		t.Fatalf("期限 = %d", a.DeadlineSeconds())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/`
Expected: FAIL，报 `undefined: Get`。

- [ ] **Step 3: 写公共类型**

`internal/adapter/adapter.go`:
```go
// Package adapter 把各家 agent CLI 的钩子输入翻成统一结构，并把决定渲染成各家要求的输出。
// 适配器只做格式翻译，不含任何策略逻辑。
package adapter

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// Kind 是翻译结果的种类。
type Kind string

const (
	KindPermission Kind = "permission"  // 需要决定
	KindQuestion   Kind = "question"    // agent 在等人
	KindDone       Kind = "done"        // 一轮结束
	KindSessionEnd Kind = "session_end" // 会话退出
	KindIgnore     Kind = "ignore"      // 认识但不关心的事件
)

// PayloadOther 表示适配器认不出的工具，引擎按 unknown 处理。
const PayloadOther policy.PayloadType = "other"

// DeadlineSeconds 是守护进程对一条审批请求的等待上限，比各 CLI 的 24 小时钩子超时短 10 秒。
const DeadlineSeconds = 86390

// previewLimit 是改动预览的最大字节数。
const previewLimit = 4096

// Input 是钩子 stdin 翻译后的统一结构。
type Input struct {
	Kind     Kind
	Session  proto.Session
	ToolName string
	Payload  policy.Payload // 仅 KindPermission
	Message  string         // question、done 的摘要
}

// Decision 是守护进程的答复。Action 为 allow、deny 或 passthrough。
type Decision struct {
	Action string
	Reason string
}

// Output 是写回 CLI 的内容。
type Output struct {
	Stdout   []byte
	ExitCode int
}

// Adapter 是一个 CLI 的翻译器。
type Adapter interface {
	Name() string
	Parse(stdin []byte) (Input, error)
	Render(d Decision) Output
	DeadlineSeconds() int
}

var registry = map[string]Adapter{}

func register(a Adapter) { registry[a.Name()] = a }

// Get 按名字取适配器：claude、codex、gemini。
func Get(name string) (Adapter, bool) {
	a, ok := registry[name]
	return a, ok
}

// passthrough 是“不作决定”：stdout 为空，退出 0。
func passthrough() Output { return Output{Stdout: nil, ExitCode: 0} }

// permissionJSON 是 Claude 与 Codex 共用的 PermissionRequest 输出。
func permissionJSON(behavior, message string) []byte {
	type decision struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message,omitempty"`
	}
	type hso struct {
		HookEventName string   `json:"hookEventName"`
		Decision      decision `json:"decision"`
	}
	out := struct {
		HookSpecificOutput hso `json:"hookSpecificOutput"`
	}{hso{"PermissionRequest", decision{behavior, message}}}
	b, _ := json.Marshal(out)
	return b
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var urlRe = regexp.MustCompile(`https?://[^\s'"<>]+`)

// toolInput 是各家 tool_input 里可能出现的字段，缺的留空。
type toolInput struct {
	Command      string          `json:"command"`
	FilePath     string          `json:"file_path"`
	Path         string          `json:"path"`
	NotebookPath string          `json:"notebook_path"`
	Content      string          `json:"content"`
	NewString    string          `json:"new_string"`
	NewSource    string          `json:"new_source"`
	Patch        string          `json:"patch"`
	URL          string          `json:"url"`
	Prompt       string          `json:"prompt"`
	Edits        []struct{ NewString string `json:"new_string"` } `json:"edits"`
}

// mapTool 把工具名和 tool_input 翻成统一载荷。cli 决定命名习惯。
func mapTool(cli, toolName string, raw json.RawMessage, cwd string) (policy.Payload, error) {
	var ti toolInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &ti); err != nil {
			return policy.Payload{}, fmt.Errorf("解析 tool_input: %w", err)
		}
	}
	if server, tool, ok := mcpName(cli, toolName); ok {
		return policy.Payload{Type: policy.PayloadMCP, Server: server, Tool: tool, Args: raw}, nil
	}
	switch toolName {
	case "Bash", "run_shell_command", "shell", "exec_command":
		return policy.Payload{Type: policy.PayloadShell, Command: ti.Command, Cwd: cwd}, nil
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "write_file", "replace", "apply_patch":
		path := ti.FilePath
		if path == "" {
			path = ti.Path
		}
		if path == "" {
			path = ti.NotebookPath
		}
		preview := ti.Content
		if preview == "" {
			preview = ti.NewString
		}
		if preview == "" {
			preview = ti.NewSource
		}
		if preview == "" {
			preview = ti.Patch
		}
		if preview == "" && len(ti.Edits) > 0 {
			preview = ti.Edits[0].NewString
		}
		return policy.Payload{Type: policy.PayloadFileWrite, Path: path, DiffPreview: clip(preview, previewLimit), Cwd: cwd}, nil
	case "WebFetch", "web_fetch":
		u := ti.URL
		if u == "" {
			u = urlRe.FindString(ti.Prompt)
		}
		return policy.Payload{Type: policy.PayloadNetwork, URL: u, Method: "GET"}, nil
	case "WebSearch", "google_web_search":
		return policy.Payload{Type: policy.PayloadNetwork, URL: ""}, nil
	default:
		return policy.Payload{Type: PayloadOther, Tool: toolName}, nil
	}
}

// mcpName 识别 MCP 工具名：Claude 为 mcp__<server>__<tool>，Gemini 0.65 为 mcp_<server>_<tool>，Gemini 0.28 为 <server>__<tool>。
func mcpName(cli, name string) (server, tool string, ok bool) {
	switch {
	case strings.HasPrefix(name, "mcp__"):
		rest := name[len("mcp__"):]
		if s, t, found := strings.Cut(rest, "__"); found && s != "" && t != "" {
			return s, t, true
		}
	case cli == "gemini" && strings.HasPrefix(name, "mcp_"):
		rest := name[len("mcp_"):]
		if s, t, found := strings.Cut(rest, "_"); found && s != "" && t != "" {
			return s, t, true
		}
	case cli == "gemini" && strings.Contains(name, "__"):
		if s, t, found := strings.Cut(name, "__"); found && s != "" && t != "" {
			return s, t, true
		}
	}
	return "", "", false
}
```

- [ ] **Step 4: 写 Claude 适配器**

`internal/adapter/claude.go`:
```go
package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/xufei547/railse-opc/internal/proto"
)

func init() { register(claude{}) }

// claude 对应 Claude Code 2.1.x 的钩子：PermissionRequest 决定，Notification 为 question，Stop 为 done。
type claude struct{}

func (claude) Name() string         { return "claude" }
func (claude) DeadlineSeconds() int { return DeadlineSeconds }

type claudeHook struct {
	SessionID        string          `json:"session_id"`
	PromptID         string          `json:"prompt_id"`
	Cwd              string          `json:"cwd"`
	HookEventName    string          `json:"hook_event_name"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	LastMessage      string          `json:"last_assistant_message"`
}

func (claude) Parse(stdin []byte) (Input, error) {
	var h claudeHook
	if err := json.Unmarshal(stdin, &h); err != nil {
		return Input{}, fmt.Errorf("解析 Claude 钩子输入: %w", err)
	}
	in := Input{Session: proto.Session{Tool: "claude", CLISessionID: h.SessionID, TurnID: h.PromptID, Cwd: h.Cwd}}
	switch h.HookEventName {
	case "PermissionRequest":
		if h.SessionID == "" || h.ToolName == "" {
			return Input{}, fmt.Errorf("PermissionRequest 缺少 session_id 或 tool_name")
		}
		p, err := mapTool("claude", h.ToolName, h.ToolInput, h.Cwd)
		if err != nil {
			return Input{}, err
		}
		in.Kind, in.ToolName, in.Payload = KindPermission, h.ToolName, p
	case "Notification":
		switch h.NotificationType {
		case "permission_prompt", "idle_prompt":
			in.Kind, in.Message = KindQuestion, h.Message
		default:
			in.Kind = KindIgnore
		}
	case "Stop":
		in.Kind, in.Message = KindDone, clip(h.LastMessage, 500)
	case "SessionEnd":
		in.Kind = KindSessionEnd
	default:
		in.Kind = KindIgnore
	}
	return in, nil
}

func (claude) Render(d Decision) Output {
	switch d.Action {
	case "allow":
		return Output{Stdout: permissionJSON("allow", ""), ExitCode: 0}
	case "deny":
		return Output{Stdout: permissionJSON("deny", "Railse："+d.Reason), ExitCode: 0}
	default:
		return passthrough()
	}
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/adapter/ -run Claude -v`
Expected: 6 个测试 PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/adapter/adapter.go internal/adapter/claude.go internal/adapter/claude_test.go
git commit -m "feat(adapter): 适配器公共类型与 Claude Code 适配器"
```

---

### Task 4: Codex 适配器

**Files:**
- Create: `internal/adapter/codex.go`
- Create: `internal/adapter/codex_test.go`

**Interfaces:**
- Consumes: Task 3 的公共类型
- Produces: 注册名 `codex` 的适配器；导出 `func ParseCodexNotify(arg []byte) (Input, error)`（`notify` 把 JSON 放在最后一个命令行参数里）

- [ ] **Step 1: 写失败的测试**

`internal/adapter/codex_test.go`:
```go
package adapter

import (
	"testing"

	"github.com/xufei547/railse-opc/internal/policy"
)

func TestCodexParsePermissionRequest(t *testing.T) {
	a, ok := Get("codex")
	if !ok {
		t.Fatal("没有 codex 适配器")
	}
	in, err := a.Parse(sample(t, "codex/0.145.0/PermissionRequest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != KindPermission || in.ToolName != "Bash" {
		t.Fatalf("Kind=%s Tool=%s", in.Kind, in.ToolName)
	}
	if in.Session.Tool != "codex" || in.Session.CLISessionID != "01a15d8c-e149-77e3-94fc-1b693482f9b7" || in.Session.TurnID != "01a15d8c-e330-7c93-8bed-9553cbb63f95" {
		t.Fatalf("会话解析错误: %+v", in.Session)
	}
	if in.Payload.Type != policy.PayloadShell || in.Payload.Command != "touch spike-proof.txt" || in.Payload.Cwd != in.Session.Cwd {
		t.Fatalf("载荷错误: %+v", in.Payload)
	}
}

func TestCodexParseEvents(t *testing.T) {
	a, _ := Get("codex")
	cases := []struct {
		file string
		kind Kind
		msg  string
	}{
		{"codex/0.145.0/Stop.json", KindDone, "DONE"},
		{"codex/0.145.0/SessionEnd.json", KindSessionEnd, ""},
		{"codex/0.145.0/PreToolUse.json", KindIgnore, ""},
	}
	for _, c := range cases {
		in, err := a.Parse(sample(t, c.file))
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		if in.Kind != c.kind || in.Message != c.msg {
			t.Errorf("%s: Kind=%s Message=%q", c.file, in.Kind, in.Message)
		}
	}
}

func TestCodexNotify(t *testing.T) {
	in, err := ParseCodexNotify(sample(t, "codex/0.145.0/notify.agent-turn-complete.json"))
	if err != nil {
		t.Fatal(err)
	}
	if in.Kind != KindDone || in.Message != "DONE" {
		t.Fatalf("Kind=%s Message=%q", in.Kind, in.Message)
	}
	if in.Session.Tool != "codex" || in.Session.CLISessionID != "01a15d8c-e149-77e3-94fc-1b693482f9b7" || in.Session.TurnID != "01a15d8c-e330-7c93-8bed-9553cbb63f95" || in.Session.Cwd == "" {
		t.Fatalf("会话解析错误: %+v", in.Session)
	}
	if in, err := ParseCodexNotify([]byte(`{"type":"something-else"}`)); err != nil || in.Kind != KindIgnore {
		t.Fatalf("其他 notify 类型应为 ignore: %v %+v", err, in)
	}
}

func TestCodexRender(t *testing.T) {
	a, _ := Get("codex")
	if got := string(a.Render(Decision{Action: "allow"}).Stdout); got != `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}` {
		t.Fatalf("allow = %s", got)
	}
	if got := string(a.Render(Decision{Action: "deny", Reason: "r"}).Stdout); got != `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"deny","message":"Railse：r"}}}` {
		t.Fatalf("deny = %s", got)
	}
	if out := a.Render(Decision{Action: "passthrough"}); len(out.Stdout) != 0 || out.ExitCode != 0 {
		t.Fatalf("passthrough = %+v", out)
	}
	if a.DeadlineSeconds() != 86390 {
		t.Fatalf("期限 = %d", a.DeadlineSeconds())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/ -run Codex`
Expected: FAIL，`Get("codex")` 返回 false。

- [ ] **Step 3: 写最小实现**

`internal/adapter/codex.go`:
```go
package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/xufei547/railse-opc/internal/proto"
)

func init() { register(codex{}) }

// codex 对应 Codex CLI 0.145 的钩子。实测只有交互会话会触发 PermissionRequest，
// codex exec 强制 approval_policy=never；Codex 没有“等人回话”事件。
type codex struct{}

func (codex) Name() string         { return "codex" }
func (codex) DeadlineSeconds() int { return DeadlineSeconds }

type codexHook struct {
	SessionID     string          `json:"session_id"`
	TurnID        string          `json:"turn_id"`
	Cwd           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	LastMessage   string          `json:"last_assistant_message"`
}

func (codex) Parse(stdin []byte) (Input, error) {
	var h codexHook
	if err := json.Unmarshal(stdin, &h); err != nil {
		return Input{}, fmt.Errorf("解析 Codex 钩子输入: %w", err)
	}
	in := Input{Session: proto.Session{Tool: "codex", CLISessionID: h.SessionID, TurnID: h.TurnID, Cwd: h.Cwd}}
	switch h.HookEventName {
	case "PermissionRequest":
		if h.SessionID == "" || h.ToolName == "" {
			return Input{}, fmt.Errorf("PermissionRequest 缺少 session_id 或 tool_name")
		}
		p, err := mapTool("codex", h.ToolName, h.ToolInput, h.Cwd)
		if err != nil {
			return Input{}, err
		}
		in.Kind, in.ToolName, in.Payload = KindPermission, h.ToolName, p
	case "Stop":
		in.Kind, in.Message = KindDone, clip(h.LastMessage, 500)
	case "SessionEnd":
		in.Kind = KindSessionEnd
	default:
		in.Kind = KindIgnore
	}
	return in, nil
}

// Render 与 Claude 同形：实测 Codex 的 PermissionRequest 输出格式完全相同。
func (codex) Render(d Decision) Output {
	switch d.Action {
	case "allow":
		return Output{Stdout: permissionJSON("allow", ""), ExitCode: 0}
	case "deny":
		return Output{Stdout: permissionJSON("deny", "Railse："+d.Reason), ExitCode: 0}
	default:
		return passthrough()
	}
}

// ParseCodexNotify 解析 notify 程序收到的最后一个命令行参数。只认 agent-turn-complete。
func ParseCodexNotify(arg []byte) (Input, error) {
	var n struct {
		Type        string `json:"type"`
		ThreadID    string `json:"thread-id"`
		TurnID      string `json:"turn-id"`
		Cwd         string `json:"cwd"`
		LastMessage string `json:"last-assistant-message"`
	}
	if err := json.Unmarshal(arg, &n); err != nil {
		return Input{}, fmt.Errorf("解析 Codex notify 参数: %w", err)
	}
	in := Input{Session: proto.Session{Tool: "codex", CLISessionID: n.ThreadID, TurnID: n.TurnID, Cwd: n.Cwd}}
	if n.Type != "agent-turn-complete" {
		in.Kind = KindIgnore
		return in, nil
	}
	in.Kind, in.Message = KindDone, clip(n.LastMessage, 500)
	return in, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/adapter/ -run Codex -v`
Expected: 4 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/codex.go internal/adapter/codex_test.go
git commit -m "feat(adapter): Codex 适配器与 notify 参数解析"
```

---

### Task 5: Gemini 适配器（只拒绝，不放行）

**Files:**
- Create: `internal/adapter/gemini.go`
- Create: `internal/adapter/gemini_test.go`

**Interfaces:**
- Produces: 注册名 `gemini` 的适配器。`Render` 只会输出 deny 或不作决定：allow 一律渲染成不作决定（实测 allow 不跳过原生框），绝不输出 `ask`（headless 下永久挂起）。

- [ ] **Step 1: 写失败的测试**

`internal/adapter/gemini_test.go`:
```go
package adapter

import (
	"testing"

	"github.com/xufei547/railse-opc/internal/policy"
)

func TestGeminiParseBeforeTool(t *testing.T) {
	a, ok := Get("gemini")
	if !ok {
		t.Fatal("没有 gemini 适配器")
	}
	for _, f := range []string{"gemini/0.65.0/BeforeTool.json", "gemini/0.28.0/BeforeTool.json"} {
		in, err := a.Parse(sample(t, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if in.Kind != KindPermission || in.ToolName != "run_shell_command" || in.Session.Tool != "gemini" || in.Session.CLISessionID == "" {
			t.Fatalf("%s: %+v", f, in)
		}
		if in.Payload.Type != policy.PayloadShell || in.Payload.Command != "touch spike-proof.txt" || in.Payload.Cwd != in.Session.Cwd {
			t.Fatalf("%s 载荷错误: %+v", f, in.Payload)
		}
	}
}

func TestGeminiParseEvents(t *testing.T) {
	a, _ := Get("gemini")
	cases := []struct {
		file string
		kind Kind
		msg  string
	}{
		{"gemini/0.65.0/Notification.ToolPermission.json", KindQuestion, "Tool Confirm Shell Command requires execution: touch spike-proof.txt"},
		{"gemini/0.65.0/AfterAgent.json", KindDone, "DONE"},
		{"gemini/0.65.0/SessionEnd.json", KindSessionEnd, ""},
		{"gemini/0.65.0/AfterTool.json", KindIgnore, ""},
	}
	for _, c := range cases {
		in, err := a.Parse(sample(t, c.file))
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		if in.Kind != c.kind || in.Message != c.msg {
			t.Errorf("%s: Kind=%s Message=%q, 想要 %s/%q", c.file, in.Kind, in.Message, c.kind, c.msg)
		}
	}
}

func TestGeminiToolMapping(t *testing.T) {
	a, _ := Get("gemini")
	mk := func(tool, input string) []byte {
		return []byte(`{"session_id":"s","cwd":"/p","hook_event_name":"BeforeTool","tool_name":"` + tool + `","tool_input":` + input + `}`)
	}
	cases := []struct {
		tool, input string
		typ         policy.PayloadType
		check       func(policy.Payload) bool
	}{
		{"write_file", `{"file_path":"/p/a.txt","content":"hi"}`, policy.PayloadFileWrite, func(p policy.Payload) bool { return p.Path == "/p/a.txt" && p.DiffPreview == "hi" }},
		{"replace", `{"file_path":"b.go","old_string":"x","new_string":"y"}`, policy.PayloadFileWrite, func(p policy.Payload) bool { return p.Path == "b.go" && p.DiffPreview == "y" }},
		{"web_fetch", `{"prompt":"fetch https://example.com/a and summarize"}`, policy.PayloadNetwork, func(p policy.Payload) bool { return p.URL == "https://example.com/a" }},
		{"google_web_search", `{"query":"q"}`, policy.PayloadNetwork, func(p policy.Payload) bool { return p.URL == "" }},
		{"mcp_github_create_issue", `{"t":1}`, policy.PayloadMCP, func(p policy.Payload) bool { return p.Server == "github" && p.Tool == "create_issue" }},
		{"github__create_issue", `{"t":1}`, policy.PayloadMCP, func(p policy.Payload) bool { return p.Server == "github" && p.Tool == "create_issue" }},
		{"ask_user", `{"q":"?"}`, PayloadOther, func(p policy.Payload) bool { return p.Tool == "ask_user" }},
	}
	for _, c := range cases {
		in, err := a.Parse(mk(c.tool, c.input))
		if err != nil {
			t.Errorf("%s: %v", c.tool, err)
			continue
		}
		if in.Payload.Type != c.typ || !c.check(in.Payload) {
			t.Errorf("%s: %+v", c.tool, in.Payload)
		}
	}
}

func TestGeminiRenderNeverAllowsOrAsks(t *testing.T) {
	a, _ := Get("gemini")
	if got := string(a.Render(Decision{Action: "deny", Reason: "r"}).Stdout); got != `{"decision":"deny","reason":"Railse：r"}` {
		t.Fatalf("deny = %s", got)
	}
	for _, act := range []string{"allow", "passthrough", "ask", "bogus"} {
		out := a.Render(Decision{Action: act})
		if len(out.Stdout) != 0 || out.ExitCode != 0 {
			t.Fatalf("%s 应渲染为不作决定, 得到 %+v", act, out)
		}
	}
	if a.DeadlineSeconds() != 86390 {
		t.Fatalf("期限 = %d", a.DeadlineSeconds())
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/adapter/ -run Gemini`
Expected: FAIL，`Get("gemini")` 返回 false。

- [ ] **Step 3: 写最小实现**

`internal/adapter/gemini.go`:
```go
package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/xufei547/railse-opc/internal/proto"
)

func init() { register(gemini{}) }

// gemini 对应 Gemini CLI 0.65+ 的钩子。实测 BeforeTool 只能拒绝，allow 不会跳过原生确认框，
// ask 在 headless 下永久挂起，所以 Render 只输出 deny 或不作决定。
type gemini struct{}

func (gemini) Name() string         { return "gemini" }
func (gemini) DeadlineSeconds() int { return DeadlineSeconds }

type geminiHook struct {
	SessionID        string          `json:"session_id"`
	Cwd              string          `json:"cwd"`
	HookEventName    string          `json:"hook_event_name"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	Details          struct {
		Command string `json:"command"`
	} `json:"details"`
	PromptResponse string `json:"prompt_response"`
}

func (gemini) Parse(stdin []byte) (Input, error) {
	var h geminiHook
	if err := json.Unmarshal(stdin, &h); err != nil {
		return Input{}, fmt.Errorf("解析 Gemini 钩子输入: %w", err)
	}
	in := Input{Session: proto.Session{Tool: "gemini", CLISessionID: h.SessionID, Cwd: h.Cwd}}
	switch h.HookEventName {
	case "BeforeTool":
		if h.SessionID == "" || h.ToolName == "" {
			return Input{}, fmt.Errorf("BeforeTool 缺少 session_id 或 tool_name")
		}
		p, err := mapTool("gemini", h.ToolName, h.ToolInput, h.Cwd)
		if err != nil {
			return Input{}, err
		}
		in.Kind, in.ToolName, in.Payload = KindPermission, h.ToolName, p
	case "Notification":
		if h.NotificationType == "ToolPermission" {
			msg := h.Message
			if h.Details.Command != "" {
				msg += ": " + h.Details.Command
			}
			in.Kind, in.Message = KindQuestion, msg
		} else {
			in.Kind = KindIgnore
		}
	case "AfterAgent":
		in.Kind, in.Message = KindDone, clip(h.PromptResponse, 500)
	case "SessionEnd":
		in.Kind = KindSessionEnd
	default:
		in.Kind = KindIgnore
	}
	return in, nil
}

func (gemini) Render(d Decision) Output {
	if d.Action == "deny" {
		b, _ := json.Marshal(struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}{"deny", "Railse：" + d.Reason})
		return Output{Stdout: b, ExitCode: 0}
	}
	return passthrough()
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/adapter/ -race -v`
Expected: 包内全部测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/adapter/gemini.go internal/adapter/gemini_test.go
git commit -m "feat(adapter): Gemini 适配器，只拒绝不放行"
```

---

### Task 6: 守护进程配置、项目根查找与审计日志

**Files:**
- Create: `internal/agent/config.go`
- Create: `internal/agent/project.go`
- Create: `internal/agent/audit.go`
- Create: `internal/agent/config_test.go`
- Create: `internal/agent/project_test.go`
- Create: `internal/agent/audit_test.go`

**Interfaces:**
- Produces（导出）:
  - `type Config struct{ Dir, SocketPath, RulesPath, AuditPath, StatePath, LogPath, MachineID, HomeDir string; Deadline time.Duration }`
  - `func DefaultConfig() (Config, error)`：目录为 `$RAILSE_HOME`，否则 `~/.railse`；`MachineID` 为主机名；`Deadline` 为 `86390 * time.Second`
  - `func ConfigIn(dir string) Config`：测试与 `--dir` 用，按 dir 推出各路径，`HomeDir` 用 `os.UserHomeDir`，`MachineID` 用主机名
  - `func (c Config) EnsureDirs() error`：建目录 0700
  - `func FindProjectRoot(cwd string) string`：先 `filepath.EvalSymlinks`，再向上找含 `.git`（目录或文件）的目录，找不到返回空串；返回值也是已解析符号链接的路径
  - `func CleanCwd(cwd string) string`：`EvalSymlinks` 失败就 `filepath.Clean`
  - `type AuditEntry struct{ TS time.Time; RequestID, Kind string; Session proto.Session; ToolName string; Payload *policy.Payload; Risk, Action, State, RuleID, Reason, DecidedBy, Message string }`
  - `type Audit`、`func OpenAudit(path string) (*Audit, error)`（追加写，0600）、`func (a *Audit) Write(e AuditEntry) error`、`func (a *Audit) Close() error`

- [ ] **Step 1: 写失败的测试**

`internal/agent/config_test.go`:
```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestConfigInAndEnsureDirs(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "home")
	c := ConfigIn(dir)
	if c.Dir != dir || c.SocketPath != filepath.Join(dir, "agent.sock") || c.RulesPath != filepath.Join(dir, "rules.yaml") || c.AuditPath != filepath.Join(dir, "audit.jsonl") || c.StatePath != filepath.Join(dir, "state.json") || c.LogPath != filepath.Join(dir, "agent.log") {
		t.Fatalf("路径推导错误: %+v", c)
	}
	if c.MachineID == "" || c.HomeDir == "" || c.Deadline.Seconds() != 86390 {
		t.Fatalf("默认值错误: %+v", c)
	}
	if err := c.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("目录权限 = %v, %v", st.Mode().Perm(), err)
	}
}

func TestDefaultConfigHonorsEnv(t *testing.T) {
	dir := shortTempDir(t)
	t.Setenv("RAILSE_HOME", dir)
	c, err := DefaultConfig()
	if err != nil || c.Dir != dir {
		t.Fatalf("RAILSE_HOME 未生效: %+v, %v", c, err)
	}
}
```

`internal/agent/project_test.go`:
```go
package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindProjectRoot(t *testing.T) {
	base := shortTempDir(t)
	repo := filepath.Join(base, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "src", "pkg"), 0o755)
	wt := filepath.Join(base, "wt")
	os.MkdirAll(wt, 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644)
	plain := filepath.Join(base, "plain", "deep")
	os.MkdirAll(plain, 0o755)

	resolved := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
	cases := []struct{ cwd, want string }{
		{filepath.Join(repo, "src", "pkg"), resolved(repo)},
		{repo, resolved(repo)},
		{wt, resolved(wt)},
		{plain, ""},
		{filepath.Join(base, "missing"), ""},
	}
	for _, c := range cases {
		if got := FindProjectRoot(c.cwd); got != c.want {
			t.Errorf("FindProjectRoot(%q) = %q, 想要 %q", c.cwd, got, c.want)
		}
	}
	if got := CleanCwd(filepath.Join(repo, "src", "..", "src")); got != resolved(filepath.Join(repo, "src")) {
		t.Errorf("CleanCwd = %q", got)
	}
}
```

`internal/agent/audit_test.go`:
```go
package agent

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func TestAuditAppendsJSONL(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "audit.jsonl")
	a, err := OpenAudit(path)
	if err != nil {
		t.Fatal(err)
	}
	p := policy.Payload{Type: policy.PayloadShell, Command: "ls"}
	for i := 0; i < 2; i++ {
		if err := a.Write(AuditEntry{TS: time.Now(), RequestID: "r", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s"}, Payload: &p, Risk: "read", Action: "allow", State: "auto_allowed", RuleID: "builtin-readonly"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("审计文件权限 = %v", st.Mode().Perm())
	}
	f, _ := os.Open(path)
	defer f.Close()
	sc := bufio.NewScanner(f)
	n := 0
	for sc.Scan() {
		var e AuditEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("第 %d 行不是 JSON: %v", n+1, err)
		}
		if e.State != "auto_allowed" || e.Payload == nil || e.Payload.Command != "ls" {
			t.Fatalf("第 %d 行内容错误: %+v", n+1, e)
		}
		n++
	}
	if n != 2 {
		t.Fatalf("行数 = %d", n)
	}
	// 再次打开应追加而不是覆盖
	a2, _ := OpenAudit(path)
	a2.Write(AuditEntry{TS: time.Now(), Kind: "done", State: "recorded"})
	a2.Close()
	b, _ := os.ReadFile(path)
	if lines := len(splitLines(b)); lines != 3 {
		t.Fatalf("追加后行数 = %d", lines)
	}
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	for _, l := range bytesSplit(b) {
		if len(l) > 0 {
			out = append(out, l)
		}
	}
	return out
}
```
并在同一文件末尾加：
```go
func bytesSplit(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	return append(out, b[start:])
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/`
Expected: FAIL，报 `undefined: ConfigIn` 等。

- [ ] **Step 3: 写最小实现**

`internal/agent/config.go`:
```go
// Package agent 是跑在每台机器上的守护进程：监听本地 Unix socket，接收钩子的请求，
// 用策略引擎自动决定，写本地审计。没有 hub 时，引擎答 ask 就返回“不作决定”。
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config 是守护进程的路径与常量。
type Config struct {
	Dir        string // 配置目录，默认 ~/.railse
	SocketPath string
	RulesPath  string
	AuditPath  string
	StatePath  string
	LogPath    string
	MachineID  string
	HomeDir    string
	Deadline   time.Duration // 单条请求的等待上限
}

// deadline 比各 CLI 的 24 小时钩子超时短 10 秒，见设计文档 3.2。
const deadline = 86390 * time.Second

// ConfigIn 按给定目录推出全部路径。
func ConfigIn(dir string) Config {
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()
	if host == "" {
		host = "local"
	}
	return Config{
		Dir:        dir,
		SocketPath: filepath.Join(dir, "agent.sock"),
		RulesPath:  filepath.Join(dir, "rules.yaml"),
		AuditPath:  filepath.Join(dir, "audit.jsonl"),
		StatePath:  filepath.Join(dir, "state.json"),
		LogPath:    filepath.Join(dir, "agent.log"),
		MachineID:  host,
		HomeDir:    home,
		Deadline:   deadline,
	}
}

// DefaultConfig 用 $RAILSE_HOME，否则 ~/.railse。
func DefaultConfig() (Config, error) {
	if dir := os.Getenv("RAILSE_HOME"); dir != "" {
		return ConfigIn(dir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("找不到家目录: %w", err)
	}
	return ConfigIn(filepath.Join(home, ".railse")), nil
}

// EnsureDirs 建配置目录，权限 0700。
func (c Config) EnsureDirs() error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(c.Dir, 0o700)
}
```

`internal/agent/project.go`:
```go
package agent

import (
	"os"
	"path/filepath"
)

// CleanCwd 解析符号链接并清理路径；解析失败就只清理。
// macOS 上 /var 是 /private/var 的链接，钩子给的 cwd 和规则里的路径必须用同一种写法比较。
func CleanCwd(cwd string) string {
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(cwd)
}

// FindProjectRoot 从 cwd 向上找含 .git（目录或 worktree 的 .git 文件）的目录。
// 找不到返回空串，引擎会把空的项目根按项目外处理。
func FindProjectRoot(cwd string) string {
	dir := CleanCwd(cwd)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
```

`internal/agent/audit.go`:
```go
package agent

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// AuditEntry 是审计日志的一行。Payload 已脱敏。
type AuditEntry struct {
	TS        time.Time       `json:"ts"`
	RequestID string          `json:"request_id,omitempty"`
	Kind      string          `json:"kind"` // permission、question、done、session_end、control
	Session   proto.Session   `json:"session"`
	ToolName  string          `json:"tool_name,omitempty"`
	Payload   *policy.Payload `json:"payload,omitempty"`
	Risk      string          `json:"risk,omitempty"`
	Action    string          `json:"action,omitempty"` // allow、deny、passthrough
	State     string          `json:"state"`            // auto_allowed、auto_denied、passthrough、cancelled、expired、recorded、suppressed
	RuleID    string          `json:"rule_id,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	DecidedBy string          `json:"decided_by,omitempty"`
	Message   string          `json:"message,omitempty"`
}

// Audit 是只追加的 JSONL 文件。
type Audit struct {
	mu sync.Mutex
	f  *os.File
}

// OpenAudit 以追加方式打开审计文件，权限 0600。
func OpenAudit(path string) (*Audit, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	return &Audit{f: f}, nil
}

// Write 追加一行。
func (a *Audit) Write(e AuditEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err = a.f.Write(append(b, '\n'))
	return err
}

// Close 关闭文件。
func (a *Audit) Close() error { return a.f.Close() }
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/agent/ -v`
Expected: 4 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/config.go internal/agent/project.go internal/agent/audit.go internal/agent/config_test.go internal/agent/project_test.go internal/agent/audit_test.go
git commit -m "feat(agent): 配置目录、项目根查找与 JSONL 审计"
```

---

### Task 7: 规则文件的初始化、加载与热重载

**Files:**
- Create: `internal/agent/rules.go`
- Create: `internal/agent/rules_test.go`

**Interfaces:**
- Consumes: `policy.DefaultRuleSet`、`policy.ParseRuleSet`、`policy.NewEngine`、`RuleSet.Marshal`
- Produces（导出）:
  - `type Rules`、`func LoadRules(path string) (*Rules, error)`：文件不存在时先用内置规则包写出（0600，文件头带注释），再加载
  - `func (r *Rules) Reload() error`：解析失败保留旧引擎并返回错误
  - `func (r *Rules) Engine() *policy.Engine`、`func (r *Rules) Count() int`、`func (r *Rules) Path() string`
  - `func (r *Rules) SetPaused(bool)`、`func (r *Rules) Paused() bool`（重载后保持暂停状态）

- [ ] **Step 1: 写失败的测试**

`internal/agent/rules_test.go`:
```go
package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/policy"
)

func TestLoadRulesCreatesDefaultFile(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, err := LoadRules(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("规则文件应被创建且权限 0600: %v %v", st, err)
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "# ") || !strings.Contains(string(b), "builtin-readonly") {
		t.Fatalf("内容应带注释头并含内置规则:\n%s", b)
	}
	if r.Count() != 2 || r.Path() != path {
		t.Fatalf("Count=%d Path=%s", r.Count(), r.Path())
	}
	d := r.Engine().Evaluate(policy.Payload{Type: policy.PayloadShell, Command: "ls", Cwd: "/p"}, policy.Context{ProjectRoot: "/p", HomeDir: "/h"})
	if d.Action != policy.ActionAllow {
		t.Fatalf("内置规则应放行 ls: %+v", d)
	}
}

func TestReloadKeepsOldOnError(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, err := LoadRules(path)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("version: 9\nrules: []\n"), 0o600)
	if err := r.Reload(); err == nil {
		t.Fatal("坏文件应报错")
	}
	if r.Count() != 2 {
		t.Fatalf("重载失败后应保留旧规则, Count=%d", r.Count())
	}
	os.WriteFile(path, []byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"), 0o600)
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if r.Count() != 1 {
		t.Fatalf("重载后 Count=%d", r.Count())
	}
	d := r.Engine().Evaluate(policy.Payload{Type: policy.PayloadShell, Command: "rm x", Cwd: "/p"}, policy.Context{ProjectRoot: "/p", HomeDir: "/h"})
	if d.Action != policy.ActionDeny {
		t.Fatalf("新规则应生效: %+v", d)
	}
}

func TestPausedSurvivesReload(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, _ := LoadRules(path)
	r.SetPaused(true)
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if !r.Paused() || !r.Engine().Paused() {
		t.Fatal("重载后应仍处于暂停")
	}
	d := r.Engine().Evaluate(policy.Payload{Type: policy.PayloadShell, Command: "ls", Cwd: "/p"}, policy.Context{ProjectRoot: "/p", HomeDir: "/h"})
	if d.Action != policy.ActionAsk {
		t.Fatalf("暂停时应 ask: %+v", d)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/ -run Rules`
Expected: FAIL，报 `undefined: LoadRules`。

- [ ] **Step 3: 写最小实现**

`internal/agent/rules.go`:
```go
package agent

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/xufei547/railse-opc/internal/policy"
)

const rulesHeader = `# Railse OPC 本机规则文件。守护进程启动和 reload 时读取，改错了会保留上一版。
# 格式见设计文档 4.3；hub 接入后由 hub 下发，这里只是本机缓存。
`

// Rules 持有当前引擎，并发安全。
type Rules struct {
	path   string
	mu     sync.RWMutex
	engine *policy.Engine
	count  int
	paused atomic.Bool
}

// LoadRules 读规则文件；文件不存在时先写出内置规则包。
func LoadRules(path string) (*Rules, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := writeDefaultRules(path); err != nil {
			return nil, err
		}
	}
	r := &Rules{path: path}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

func writeDefaultRules(path string) error {
	b, err := policy.DefaultRuleSet().Marshal()
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(rulesHeader), b...), 0o600)
}

// Reload 重新读文件；解析或校验失败时保留旧引擎并返回错误。
func (r *Rules) Reload() error {
	data, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("读规则文件: %w", err)
	}
	rs, err := policy.ParseRuleSet(data)
	if err != nil {
		return fmt.Errorf("规则文件 %s: %w", r.path, err)
	}
	eng, err := policy.NewEngine(rs)
	if err != nil {
		return err
	}
	eng.SetPaused(r.paused.Load())
	r.mu.Lock()
	r.engine, r.count = eng, len(rs.Rules)
	r.mu.Unlock()
	return nil
}

// Engine 返回当前引擎。
func (r *Rules) Engine() *policy.Engine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.engine
}

// Count 返回规则条数。
func (r *Rules) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.count
}

// Path 返回规则文件路径。
func (r *Rules) Path() string { return r.path }

// SetPaused 设置总开关，对当前和以后重载的引擎都生效。
func (r *Rules) SetPaused(p bool) {
	r.paused.Store(p)
	r.Engine().SetPaused(p)
}

// Paused 返回总开关状态。
func (r *Rules) Paused() bool { return r.paused.Load() }
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/agent/ -race -run 'Rules|Paused' -v`
Expected: 3 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/rules.go internal/agent/rules_test.go
git commit -m "feat(agent): 规则文件初始化、加载与热重载"
```

---

### Task 8: 请求生命周期、Decider 与 socket 服务的审批路径

**Files:**
- Create: `internal/agent/requests.go`
- Create: `internal/agent/server.go`
- Create: `internal/agent/server_test.go`

**Interfaces:**
- Consumes: Task 6、7；`proto`（Task 1）；`redact.Payload`（Task 2）；`policy.Engine.Evaluate`、`policy.Context`
- Produces（导出）:
  - `type Outcome struct{ Action, Reason, RuleID, DecidedBy string }`
  - `type PendingRequest struct{ ID string; Req proto.Request; Risk policy.Risk; Created, Deadline time.Time }`
  - `type Decider interface{ Decide(ctx context.Context, pr PendingRequest) (Outcome, error) }`：引擎答 ask 之后才调用；`ctx` 取消表示钩子已消失，到期表示过期
  - `type LocalDecider struct{}`：没有 hub，立即返回 `passthrough`，`DecidedBy: "no_hub"`
  - `type Pending`（`Add`、`Remove`、`Count`、`HasSession(cliSessionID string) bool`）
  - `func NewID() string`：16 字节随机数的十六进制
  - `type Server`、`func New(cfg Config, rules *Rules, audit *Audit, decider Decider, logger *log.Logger) *Server`、`func (s *Server) Listen() error`、`func (s *Server) Serve(ctx context.Context) error`、`func (s *Server) Close() error`
  - 包内：`handleConn`、`handlePermission(ctx, req proto.Request) proto.Response`、`evaluate(req) (policy.Decision, policy.Context)`

- [ ] **Step 1: 写失败的测试**

`internal/agent/server_test.go`:
```go
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// blockingDecider 等到 release 被关闭才返回，用来模拟 hub 等待。
type blockingDecider struct {
	release chan struct{}
	mu      sync.Mutex
	ctxErr  error
	outcome Outcome
}

func (d *blockingDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error) {
	select {
	case <-d.release:
		return d.outcome, nil
	case <-ctx.Done():
		d.mu.Lock()
		d.ctxErr = ctx.Err()
		d.mu.Unlock()
		return Outcome{}, ctx.Err()
	}
}

type testEnv struct {
	cfg    Config
	srv    *Server
	repo   string // 一个带 .git 的目录
	cancel context.CancelFunc
}

func startServer(t *testing.T, decider Decider, mutate func(*Config)) *testEnv {
	t.Helper()
	dir := shortTempDir(t)
	cfg := ConfigIn(filepath.Join(dir, "h"))
	cfg.HomeDir = filepath.Join(dir, "home")
	os.MkdirAll(cfg.HomeDir, 0o755)
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	rules, err := LoadRules(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := OpenAudit(cfg.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(cfg, rules, audit, decider, log.New(os.Stderr, "test ", 0))
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	t.Cleanup(func() { cancel(); srv.Close(); audit.Close() })
	return &testEnv{cfg: cfg, srv: srv, repo: repo, cancel: cancel}
}

// roundTrip 发一行，读一行。
func roundTrip(t *testing.T, sock string, msg any) []byte {
	t.Helper()
	conn, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := proto.WriteLine(conn, msg); err != nil {
		t.Fatal(err)
	}
	line, err := proto.ReadLine(bufio.NewReader(conn))
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func permission(cwd, cmd string) proto.Request {
	return proto.Request{V: proto.Version, Kind: "permission",
		Session:  proto.Session{Tool: "claude", CLISessionID: "s1", TurnID: "t1", Cwd: cwd},
		ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: cmd, Cwd: cwd},
		DeadlineSeconds: 86390}
}

func readAudit(t *testing.T, path string) []AuditEntry {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []AuditEntry
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatalf("审计行不是 JSON: %q", l)
		}
		out = append(out, e)
	}
	return out
}

func TestPermissionAllowDenyPassthrough(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	os.WriteFile(env.cfg.RulesPath, []byte("version: 1\nrules:\n  - id: builtin-readonly\n    source: builtin\n    scope: global\n    match: {risk: read}\n    action: allow\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"), 0o600)
	if err := env.srv.rules.Reload(); err != nil {
		t.Fatal(err)
	}
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
	if resp.Decision != "allow" || resp.RuleID != "builtin-readonly" || resp.Risk != "read" || resp.RequestID == "" {
		t.Fatalf("allow: %+v", resp)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "rm x")), &resp)
	if resp.Decision != "deny" || resp.RuleID != "deny-rm" {
		t.Fatalf("deny: %+v", resp)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl https://x.y")), &resp)
	if resp.Decision != "passthrough" || resp.Risk != "network" {
		t.Fatalf("passthrough: %+v", resp)
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 3 || entries[0].State != "auto_allowed" || entries[1].State != "auto_denied" || entries[2].State != "passthrough" || entries[2].DecidedBy != "no_hub" {
		t.Fatalf("审计: %+v", entries)
	}
	if entries[0].Payload == nil || entries[0].Payload.Command != "git status" {
		t.Fatalf("审计应带载荷: %+v", entries[0])
	}
}

func TestPermissionRedactsAudit(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl -H 'Authorization: Bearer secrettoken' https://x.y"))
	entries := readAudit(t, env.cfg.AuditPath)
	if strings.Contains(entries[0].Payload.Command, "secrettoken") {
		t.Fatalf("审计未脱敏: %s", entries[0].Payload.Command)
	}
}

func TestProjectRootFailsClosed(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	plain := filepath.Join(filepath.Dir(env.repo), "plain")
	os.MkdirAll(plain, 0o755)
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(plain, "touch a")), &resp)
	if resp.Decision != "passthrough" || resp.Risk != "write_outside" {
		t.Fatalf("没有 .git 的目录里写文件应按项目外: %+v", resp)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "touch a")), &resp)
	if resp.Decision != "allow" || resp.Risk != "write_in_project" {
		t.Fatalf("仓库内写文件应放行: %+v", resp)
	}
}

func TestConnDropCancelsPending(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{})}
	env := startServer(t, d, nil)
	conn, err := net.Dial("unix", env.cfg.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	proto.WriteLine(conn, permission(env.repo, "curl https://x.y"))
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	conn.Close()
	waitFor(t, func() bool { return env.srv.pending.Count() == 0 })
	d.mu.Lock()
	err = d.ctxErr
	d.mu.Unlock()
	if err == nil {
		t.Fatal("钩子断开后 Decider 的 ctx 应被取消")
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 1 || entries[0].State != "cancelled" {
		t.Fatalf("审计应记 cancelled: %+v", entries)
	}
}

func TestDeadlineExpires(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{})}
	env := startServer(t, d, func(c *Config) { c.Deadline = 200 * time.Millisecond })
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl https://x.y")), &resp)
	if resp.Decision != "passthrough" || !strings.Contains(resp.Reason, "过期") {
		t.Fatalf("到期应不作决定: %+v", resp)
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 1 || entries[0].State != "expired" || entries[0].DecidedBy != "timeout" {
		t.Fatalf("审计应记 expired: %+v", entries)
	}
}

func TestDeciderOutcomeIsHonored(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{}), outcome: Outcome{Action: "allow", Reason: "手机批准", DecidedBy: "user"}}
	env := startServer(t, d, nil)
	done := make(chan proto.Response)
	go func() {
		var resp proto.Response
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl https://x.y")), &resp)
		done <- resp
	}()
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	close(d.release)
	resp := <-done
	if resp.Decision != "allow" || resp.Reason != "手机批准" {
		t.Fatalf("应采用 Decider 的结论: %+v", resp)
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if entries[0].State != "approved" || entries[0].DecidedBy != "user" {
		t.Fatalf("审计: %+v", entries[0])
	}
}

func TestSocketPermissions(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	st, err := os.Stat(env.cfg.SocketPath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("socket 权限 = %v, %v", st.Mode().Perm(), err)
	}
}

func TestStaleSocketIsReplaced(t *testing.T) {
	dir := shortTempDir(t)
	cfg := ConfigIn(filepath.Join(dir, "h"))
	cfg.EnsureDirs()
	os.WriteFile(cfg.SocketPath, nil, 0o600) // 假装上次崩溃留下的文件
	rules, _ := LoadRules(cfg.RulesPath)
	audit, _ := OpenAudit(cfg.AuditPath)
	srv := New(cfg, rules, audit, LocalDecider{}, log.New(os.Stderr, "", 0))
	if err := srv.Listen(); err != nil {
		t.Fatalf("应替换过期 socket 文件: %v", err)
	}
	srv.Close()
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待超时")
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/ -run 'Permission|ProjectRoot|ConnDrop|Deadline|Decider|Socket'`
Expected: FAIL，报 `undefined: New`、`LocalDecider` 等。

- [ ] **Step 3: 写请求生命周期**

`internal/agent/requests.go`:
```go
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// Outcome 是引擎之外的决定来源（hub、手机）给出的结论。
type Outcome struct {
	Action    string // allow、deny、passthrough
	Reason    string
	RuleID    string
	DecidedBy string // user、no_hub、timeout…
}

// PendingRequest 是等待外部决定的请求。
type PendingRequest struct {
	ID       string
	Req      proto.Request
	Risk     policy.Risk
	Created  time.Time
	Deadline time.Time
}

// Decider 在引擎答 ask 之后给出最终结论。ctx 取消表示钩子已消失，到期表示过期。
type Decider interface {
	Decide(ctx context.Context, pr PendingRequest) (Outcome, error)
}

// LocalDecider 是没有 hub 时的实现：立即不作决定，让 CLI 弹原生框。
type LocalDecider struct{}

// Decide 实现 Decider。
func (LocalDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error) {
	return Outcome{Action: "passthrough", Reason: "本机没有连接 hub，交回终端处理", DecidedBy: "no_hub"}, nil
}

// Pending 是未完成请求的表。
type Pending struct {
	mu sync.Mutex
	m  map[string]*PendingRequest
}

func newPending() *Pending { return &Pending{m: map[string]*PendingRequest{}} }

// Add 登记。
func (p *Pending) Add(pr *PendingRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[pr.ID] = pr
}

// Remove 注销。
func (p *Pending) Remove(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.m, id)
}

// Count 返回数量。
func (p *Pending) Count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.m)
}

// HasSession 判断某个 CLI 会话是否有未完成的审批，用于压掉随后的 question 事件。
func (p *Pending) HasSession(cliSessionID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, pr := range p.m {
		if pr.Req.Session.CLISessionID == cliSessionID {
			return true
		}
	}
	return false
}

// NewID 生成 32 个十六进制字符的随机 id。
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("随机数不可用: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}
```

- [ ] **Step 4: 写服务**

`internal/agent/server.go`:
```go
package agent

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
	"github.com/xufei547/railse-opc/internal/redact"
)

// Server 监听 Unix socket，一次连接处理一条消息。
type Server struct {
	cfg     Config
	rules   *Rules
	audit   *Audit
	decider Decider
	pending *Pending
	log     *log.Logger
	ln      net.Listener
	start   time.Time
	wg      sync.WaitGroup
}

// New 组装服务，不做 IO。
func New(cfg Config, rules *Rules, audit *Audit, decider Decider, logger *log.Logger) *Server {
	return &Server{cfg: cfg, rules: rules, audit: audit, decider: decider, pending: newPending(), log: logger, start: time.Now()}
}

// Listen 建 socket。上次崩溃留下的无人监听的 socket 文件会被替换。
func (s *Server) Listen() error {
	if _, err := os.Stat(s.cfg.SocketPath); err == nil {
		if c, err := net.DialTimeout("unix", s.cfg.SocketPath, 500*time.Millisecond); err == nil {
			c.Close()
			return fmt.Errorf("已有守护进程在 %s 监听", s.cfg.SocketPath)
		}
		os.Remove(s.cfg.SocketPath)
	}
	ln, err := net.Listen("unix", s.cfg.SocketPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(s.cfg.SocketPath, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.ln = ln
	return nil
}

// Serve 接受连接直到 ctx 取消。
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.ln.Close()
	}()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.wg.Wait()
				return nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			s.log.Printf("accept: %v", err)
			continue
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(ctx, conn)
		}()
	}
}

// Close 关闭监听并删除 socket 文件。
func (s *Server) Close() error {
	if s.ln != nil {
		s.ln.Close()
	}
	os.Remove(s.cfg.SocketPath)
	return nil
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := proto.ReadLine(bufio.NewReader(conn))
	if err != nil {
		s.log.Printf("读请求: %v", err)
		return
	}
	conn.SetReadDeadline(time.Time{})
	kind, err := proto.PeekKind(line)
	if err != nil {
		proto.WriteLine(conn, proto.Response{V: proto.Version, Decision: "passthrough", Reason: "请求格式错误"})
		return
	}
	switch kind {
	case "permission":
		var req proto.Request
		if err := proto.Decode(line, &req); err != nil {
			proto.WriteLine(conn, proto.Response{V: proto.Version, Decision: "passthrough", Reason: "请求格式错误: " + err.Error()})
			return
		}
		// 钩子不会再发数据；读到 EOF 或错误就表示它消失了。
		cctx, cancel := context.WithCancel(ctx)
		go func() {
			var b [1]byte
			conn.Read(b[:])
			cancel()
		}()
		resp := s.handlePermission(cctx, req)
		cancel()
		if resp.Decision != "" { // 钩子已消失时 resp 为空，不写回
			proto.WriteLine(conn, resp)
		}
	case "question", "done", "session_end":
		var ev proto.Event
		if err := proto.Decode(line, &ev); err != nil {
			proto.WriteLine(conn, proto.EventAck{V: proto.Version, OK: false, Error: err.Error()})
			return
		}
		proto.WriteLine(conn, s.handleEvent(ev))
	case "control":
		var c proto.Control
		if err := proto.Decode(line, &c); err != nil {
			proto.WriteLine(conn, proto.ControlReply{V: proto.Version, OK: false, Error: err.Error()})
			return
		}
		proto.WriteLine(conn, s.handleControl(c))
	default:
		proto.WriteLine(conn, proto.Response{V: proto.Version, Decision: "passthrough", Reason: "未知消息种类 " + kind})
	}
}

// evaluate 算项目根并跑引擎。
func (s *Server) evaluate(req proto.Request) (policy.Decision, policy.Context) {
	cwd := req.Payload.Cwd
	if cwd == "" {
		cwd = req.Session.Cwd
	}
	cwd = CleanCwd(cwd)
	req.Payload.Cwd = cwd
	pctx := policy.Context{ProjectRoot: FindProjectRoot(cwd), HomeDir: s.cfg.HomeDir, MachineID: s.cfg.MachineID}
	return s.rules.Engine().Evaluate(req.Payload, pctx), pctx
}

// handlePermission 是审批主路径：引擎能定就定，定不了交给 Decider。
func (s *Server) handlePermission(ctx context.Context, req proto.Request) proto.Response {
	id := NewID()
	d, _ := s.evaluate(req)
	redacted := redact.Payload(req.Payload)
	entry := AuditEntry{TS: time.Now(), RequestID: id, Kind: "permission", Session: req.Session, ToolName: req.ToolName, Payload: &redacted, Risk: d.Risk.String(), RuleID: d.RuleID, Reason: d.Reason}
	resp := proto.Response{V: proto.Version, RequestID: id, Risk: d.Risk.String(), RuleID: d.RuleID, Reason: d.Reason}
	switch d.Action {
	case policy.ActionAllow:
		entry.Action, entry.State, entry.DecidedBy = "allow", "auto_allowed", "rule:"+d.RuleID
		resp.Decision = "allow"
	case policy.ActionDeny:
		entry.Action, entry.State, entry.DecidedBy = "deny", "auto_denied", "rule:"+d.RuleID
		resp.Decision = "deny"
	default:
		if s.rules.Paused() {
			// 总开关打开：用户 deny 仍由引擎处理，其余一律交回终端，不去打扰 hub。
			entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "paused", "自动审批已暂停，交回终端处理"
			resp.Decision, resp.Reason = "passthrough", entry.Reason
			break
		}
		return s.waitDecision(ctx, id, req, d, entry)
	}
	s.writeAudit(entry)
	return resp
}

func (s *Server) waitDecision(ctx context.Context, id string, req proto.Request, d policy.Decision, entry AuditEntry) proto.Response {
	wait := s.cfg.Deadline
	if req.DeadlineSeconds > 0 && time.Duration(req.DeadlineSeconds)*time.Second < wait {
		wait = time.Duration(req.DeadlineSeconds) * time.Second
	}
	pr := &PendingRequest{ID: id, Req: req, Risk: d.Risk, Created: time.Now(), Deadline: time.Now().Add(wait)}
	s.pending.Add(pr)
	defer s.pending.Remove(id)
	dctx, cancel := context.WithDeadline(ctx, pr.Deadline)
	defer cancel()
	out, err := s.decider.Decide(dctx, *pr)
	resp := proto.Response{V: proto.Version, RequestID: id, Risk: d.Risk.String()}
	switch {
	case err != nil && ctx.Err() != nil:
		entry.State, entry.DecidedBy, entry.Reason = "cancelled", "terminal", "钩子进程在决定前退出"
		s.writeAudit(entry)
		return proto.Response{} // 对方已不在，不写回
	case err != nil && errors.Is(dctx.Err(), context.DeadlineExceeded):
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "expired", "timeout", "等待过期，交回终端处理"
		resp.Decision, resp.Reason = "passthrough", entry.Reason
	case err != nil:
		entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "error", err.Error()
		resp.Decision, resp.Reason = "passthrough", "决定来源出错，交回终端处理"
	default:
		resp.Decision, resp.Reason, resp.RuleID = out.Action, out.Reason, out.RuleID
		entry.Action, entry.DecidedBy, entry.Reason, entry.RuleID = out.Action, out.DecidedBy, out.Reason, out.RuleID
		switch out.Action {
		case "allow":
			entry.State = "approved"
		case "deny":
			entry.State = "denied"
		default:
			entry.State = "passthrough"
		}
	}
	s.writeAudit(entry)
	return resp
}

func (s *Server) writeAudit(e AuditEntry) {
	if err := s.audit.Write(e); err != nil {
		s.log.Printf("写审计: %v", err)
	}
}

// handleEvent 与 handleControl 在 Task 9 实现；这里先放占位让包能编译。
func (s *Server) handleEvent(ev proto.Event) proto.EventAck {
	return proto.EventAck{V: proto.Version, OK: false, Error: "未实现"}
}

func (s *Server) handleControl(c proto.Control) proto.ControlReply {
	return proto.ControlReply{V: proto.Version, OK: false, Error: "未实现"}
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/agent/ -race -run 'Permission|ProjectRoot|ConnDrop|Deadline|Decider|Socket' -v`
Expected: 8 个测试 PASS。`TestConnDropCancelsPending` 依赖“钩子关闭连接后服务端的 Read 返回”，在 Unix socket 上是即时的。

- [ ] **Step 6: 提交**

```bash
git add internal/agent/requests.go internal/agent/server.go internal/agent/server_test.go
git commit -m "feat(agent): 请求生命周期、Decider 与 socket 审批路径"
```

---

### Task 9: 事件、管理命令与故障注入

**Files:**
- Modify: `internal/agent/server.go`（替换 `handleEvent`、`handleControl` 占位；加 `loadState`/`saveState`）
- Create: `internal/agent/state.go`
- Create: `internal/agent/events_test.go`

**Interfaces:**
- Produces:
  - `handleEvent`：`question`、`done`、`session_end` 写审计，`Kind` 同名，`State` 为 `recorded`；同一 CLI 会话有未完成审批时 `question` 的 `State` 为 `suppressed`（设计文档 3.3）
  - `handleControl`：`status` 返回 `proto.Status`；`pause`/`resume` 改总开关并写 `state.json`（`{"paused":true}`，0600）；`reload` 重载规则，失败返回 `OK:false` 与错误
  - `func (s *Server) LoadState() error`：启动时从 `state.json` 恢复暂停状态（Task 11 的 `railse agent run` 调用）
  - 故障注入测试：坏 JSON、超长行、未知种类、读超时，都不能让服务停下

- [ ] **Step 1: 写失败的测试**

`internal/agent/events_test.go`:
```go
package agent

import (
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/proto"
)

func event(kind, session, msg string) proto.Event {
	return proto.Event{V: proto.Version, Kind: kind, Session: proto.Session{Tool: "claude", CLISessionID: session, Cwd: "/p"}, Message: msg}
}

func control(op string) proto.Control {
	return proto.Control{V: proto.Version, Kind: "control", Op: op}
}

func TestEventsAreRecorded(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	for _, ev := range []proto.Event{event("question", "s9", "等你"), event("done", "s9", "DONE"), event("session_end", "s9", "")} {
		var ack proto.EventAck
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, ev), &ack)
		if !ack.OK {
			t.Fatalf("%s: %+v", ev.Kind, ack)
		}
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 3 || entries[0].Kind != "question" || entries[0].State != "recorded" || entries[0].Message != "等你" || entries[1].Kind != "done" || entries[2].Kind != "session_end" {
		t.Fatalf("审计: %+v", entries)
	}
}

func TestQuestionSuppressedWhilePending(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{})}
	env := startServer(t, d, nil)
	conn, _ := net.Dial("unix", env.cfg.SocketPath)
	defer conn.Close()
	req := permission(env.repo, "curl https://x.y")
	req.Session.CLISessionID = "s-pending"
	proto.WriteLine(conn, req)
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	roundTrip(t, env.cfg.SocketPath, event("question", "s-pending", "Claude needs your permission"))
	roundTrip(t, env.cfg.SocketPath, event("question", "s-other", "Claude is waiting"))
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 2 || entries[0].State != "suppressed" || entries[1].State != "recorded" {
		t.Fatalf("审计: %+v", entries)
	}
	close(d.release)
}

func TestControlStatusPauseResumeReload(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("status")), &reply)
	if !reply.OK || reply.Status == nil || reply.Status.Paused || reply.Status.Rules != 2 || reply.Status.MachineID == "" || reply.Status.RulesPath != env.cfg.RulesPath {
		t.Fatalf("status: %+v", reply)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("pause")), &reply)
	if !reply.OK {
		t.Fatalf("pause: %+v", reply)
	}
	b, _ := os.ReadFile(env.cfg.StatePath)
	if !strings.Contains(string(b), `"paused":true`) {
		t.Fatalf("state.json 未写入: %s", b)
	}
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
	if resp.Decision != "passthrough" || !strings.Contains(resp.Reason, "暂停") {
		t.Fatalf("暂停时只读命令也应交回终端: %+v", resp)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("resume")), &reply)
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
	if resp.Decision != "allow" {
		t.Fatalf("恢复后应放行: %+v", resp)
	}
	os.WriteFile(env.cfg.RulesPath, []byte("version: 7\n"), 0o600)
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("reload")), &reply)
	if reply.OK || !strings.Contains(reply.Error, "version") {
		t.Fatalf("坏规则 reload 应失败: %+v", reply)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("status")), &reply)
	if reply.Status.Rules != 2 {
		t.Fatalf("失败的 reload 应保留旧规则: %+v", reply.Status)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("bogus")), &reply)
	if reply.OK {
		t.Fatal("未知 op 应失败")
	}
}

func TestLoadStateRestoresPause(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	os.WriteFile(env.cfg.StatePath, []byte(`{"paused":true}`), 0o600)
	if err := env.srv.LoadState(); err != nil {
		t.Fatal(err)
	}
	if !env.srv.rules.Paused() {
		t.Fatal("应恢复暂停状态")
	}
}

func TestMalformedInputsDoNotStopServer(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	send := func(raw string) string {
		conn, err := net.Dial("unix", env.cfg.SocketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		conn.Write([]byte(raw))
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		return string(buf[:n])
	}
	if got := send("not json\n"); !strings.Contains(got, "passthrough") {
		t.Fatalf("坏 JSON 应得到不作决定: %q", got)
	}
	if got := send(`{"v":1,"kind":"mystery"}` + "\n"); !strings.Contains(got, "passthrough") {
		t.Fatalf("未知种类应得到不作决定: %q", got)
	}
	if got := send(`{"v":3,"kind":"permission"}` + "\n"); !strings.Contains(got, "passthrough") {
		t.Fatalf("错版本应得到不作决定: %q", got)
	}
	if got := send(strings.Repeat("x", proto.MaxLine+10) + "\n"); got != "" {
		t.Fatalf("超长行应被直接关闭: %q", got)
	}
	// 服务还活着
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
	if resp.Decision != "allow" {
		t.Fatalf("故障注入后服务应正常: %+v", resp)
	}
}

func TestReadTimeoutClosesIdleConn(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	conn, _ := net.Dial("unix", env.cfg.SocketPath)
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	buf := make([]byte, 16)
	start := time.Now()
	_, err := conn.Read(buf) // 什么都不发，服务端 5 秒后应关闭
	if err == nil || time.Since(start) > 7*time.Second {
		t.Fatalf("空连接应在读超时后被关闭: err=%v 用时=%v", err, time.Since(start))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/ -run 'Events|Question|Control|LoadState|Malformed|ReadTimeout'`
Expected: FAIL，事件与管理命令返回“未实现”，`LoadState` 未定义。

- [ ] **Step 3: 写状态文件与实现**

`internal/agent/state.go`:
```go
package agent

import (
	"encoding/json"
	"errors"
	"os"
)

type state struct {
	Paused bool `json:"paused"`
}

func loadState(path string) (state, error) {
	var st state
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

func saveState(path string, st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}
```

在 `internal/agent/server.go` 里把两个占位函数替换为：
```go
// handleEvent 记录只展示的事件。同一会话有未完成审批时压掉 question，避免一件事进两条。
func (s *Server) handleEvent(ev proto.Event) proto.EventAck {
	state := "recorded"
	if ev.Kind == "question" && s.pending.HasSession(ev.Session.CLISessionID) {
		state = "suppressed"
	}
	s.writeAudit(AuditEntry{TS: time.Now(), Kind: ev.Kind, Session: ev.Session, Message: ev.Message, State: state})
	return proto.EventAck{V: proto.Version, OK: true}
}

// handleControl 处理本机管理命令。
func (s *Server) handleControl(c proto.Control) proto.ControlReply {
	ok := proto.ControlReply{V: proto.Version, OK: true}
	switch c.Op {
	case "status":
		ok.Status = &proto.Status{Paused: s.rules.Paused(), Rules: s.rules.Count(), Pending: s.pending.Count(), MachineID: s.cfg.MachineID, RulesPath: s.rules.Path(), Uptime: time.Since(s.start).Round(time.Second).String()}
		return ok
	case "pause", "resume":
		s.rules.SetPaused(c.Op == "pause")
		if err := saveState(s.cfg.StatePath, state{Paused: s.rules.Paused()}); err != nil {
			return proto.ControlReply{V: proto.Version, OK: false, Error: "写状态文件: " + err.Error()}
		}
		s.writeAudit(AuditEntry{TS: time.Now(), Kind: "control", State: "recorded", Message: c.Op})
		return ok
	case "reload":
		if err := s.rules.Reload(); err != nil {
			return proto.ControlReply{V: proto.Version, OK: false, Error: err.Error()}
		}
		s.writeAudit(AuditEntry{TS: time.Now(), Kind: "control", State: "recorded", Message: "reload"})
		return ok
	default:
		return proto.ControlReply{V: proto.Version, OK: false, Error: "未知的管理命令 " + c.Op}
	}
}

// LoadState 从 state.json 恢复暂停状态，启动时调用。
func (s *Server) LoadState() error {
	st, err := loadState(s.cfg.StatePath)
	if err != nil {
		return err
	}
	s.rules.SetPaused(st.Paused)
	return nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/agent/ -race -v`
Expected: 包内全部测试 PASS。`TestReadTimeoutClosesIdleConn` 要等 5 秒，正常。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/state.go internal/agent/server.go internal/agent/events_test.go
git commit -m "feat(agent): 事件记录、暂停与重载管理命令、故障注入测试"
```

---

### Task 10: 钩子侧客户端与 `railse hook` 命令

**Files:**
- Create: `internal/hookclient/client.go`
- Create: `internal/hookclient/client_test.go`
- Create: `cmd/railse/hook.go`
- Create: `cmd/railse/hook_test.go`
- Modify: `cmd/railse/main.go`（`run` 的 switch 加 `hook`；usage 加一行）

**Interfaces:**
- Consumes: `proto`、`adapter`、`agent.DefaultConfig`（取默认 socket 路径）
- Produces（导出）:
  - `type Client struct{ SocketPath string; ConnectTimeout time.Duration }`
  - `func (c Client) Permission(ctx context.Context, req proto.Request) (proto.Response, error)`：连接、发一行、按 ctx 的期限等一行
  - `func (c Client) Event(ev proto.Event) error`：连接与读写都限 2 秒
  - `func (c Client) Control(op string) (proto.ControlReply, error)`：限 5 秒
  - `cmd/railse`：`func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer) int`；`run` 用包级变量 `stdinReader io.Reader = os.Stdin` 传入，测试可替换
  - 命令形式：`railse hook claude|codex|gemini [--socket <path>]` 读 stdin；`railse hook codex-notify [--socket <path>] <json>` 读最后一个参数
  - 行为：任何错误都输出“不作决定”（stdout 为空，退出 0），stderr 写一行 `railse hook: <原因>`；收到 SIGTERM 或 SIGINT 同样不作决定；事件类发送后立即退出；绝不打印环境变量

- [ ] **Step 1: 写失败的测试**

`internal/hookclient/client_test.go`:
```go
package hookclient

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func startAgent(t *testing.T) (string, string) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := agent.ConfigIn(filepath.Join(dir, "h"))
	cfg.HomeDir = dir
	cfg.EnsureDirs()
	rules, err := agent.LoadRules(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	audit, _ := agent.OpenAudit(cfg.AuditPath)
	srv := agent.New(cfg, rules, audit, agent.LocalDecider{}, log.New(os.Stderr, "", 0))
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() { cancel(); srv.Close(); audit.Close() })
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	return cfg.SocketPath, repo
}

func TestPermissionEventControl(t *testing.T) {
	sock, repo := startAgent(t)
	c := Client{SocketPath: sock, ConnectTimeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := c.Permission(ctx, proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s", Cwd: repo}, ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "ls", Cwd: repo}, DeadlineSeconds: 60})
	if err != nil || resp.Decision != "allow" {
		t.Fatalf("Permission: %+v, %v", resp, err)
	}
	if err := c.Event(proto.Event{V: proto.Version, Kind: "done", Session: proto.Session{Tool: "claude", CLISessionID: "s"}, Message: "x"}); err != nil {
		t.Fatalf("Event: %v", err)
	}
	reply, err := c.Control("status")
	if err != nil || !reply.OK || reply.Status == nil {
		t.Fatalf("Control: %+v, %v", reply, err)
	}
}

func TestNoDaemon(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	c := Client{SocketPath: filepath.Join(dir, "none.sock"), ConnectTimeout: 500 * time.Millisecond}
	start := time.Now()
	if _, err := c.Permission(context.Background(), proto.Request{V: proto.Version, Kind: "permission"}); err == nil {
		t.Fatal("没有守护进程应报错")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("连不上时应很快返回")
	}
	if err := c.Event(proto.Event{V: proto.Version, Kind: "done"}); err == nil {
		t.Fatal("Event 也应报错")
	}
}
```

`cmd/railse/hook_test.go`:
```go
package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
)

func startTestAgent(t *testing.T) (sock string, repo string, cfg agent.Config) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg = agent.ConfigIn(filepath.Join(dir, "h"))
	cfg.HomeDir = dir
	cfg.EnsureDirs()
	rules, err := agent.LoadRules(cfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	audit, _ := agent.OpenAudit(cfg.AuditPath)
	srv := agent.New(cfg, rules, audit, agent.LocalDecider{}, log.New(os.Stderr, "", 0))
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() { cancel(); srv.Close(); audit.Close() })
	repo = filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	return cfg.SocketPath, repo, cfg
}

func claudePermission(cwd, cmd string) string {
	return `{"session_id":"s1","prompt_id":"p1","cwd":"` + cwd + `","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"` + cmd + `"}}`
}

func runHookWith(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := runHook(args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestHookClaudeAllow(t *testing.T) {
	sock, repo, _ := startTestAgent(t)
	out, _, code := runHookWith(t, claudePermission(repo, "git status"), "claude", "--socket", sock)
	if code != 0 || out != `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow"}}}`+"\n" {
		t.Fatalf("code=%d out=%q", code, out)
	}
}

func TestHookClaudePassthroughOnAsk(t *testing.T) {
	sock, repo, _ := startTestAgent(t)
	out, _, code := runHookWith(t, claudePermission(repo, "curl https://x.y"), "claude", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("ask 应不作决定: code=%d out=%q", code, out)
	}
}

func TestHookDenyWithRule(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	os.WriteFile(cfg.RulesPath, []byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"), 0o600)
	if reply, err := (hookclient.Client{SocketPath: sock}).Control("reload"); err != nil || !reply.OK {
		t.Fatalf("reload: %+v %v", reply, err)
	}
	got, _, code := runHookWith(t, claudePermission(repo, "rm x"), "claude", "--socket", sock)
	if code != 0 || !strings.Contains(got, `"behavior":"deny"`) || !strings.Contains(got, "deny-rm") {
		t.Fatalf("code=%d out=%q", code, got)
	}
}

func TestHookGeminiNeverAllows(t *testing.T) {
	sock, repo, _ := startTestAgent(t)
	stdin := `{"session_id":"g","cwd":"` + repo + `","hook_event_name":"BeforeTool","tool_name":"run_shell_command","tool_input":{"command":"git status"}}`
	out, _, code := runHookWith(t, stdin, "gemini", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("Gemini 的 allow 应渲染为不作决定: code=%d out=%q", code, out)
	}
}

func TestHookNoDaemonPassthrough(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	out, errOut, code := runHookWith(t, claudePermission("/p", "git status"), "claude", "--socket", filepath.Join(dir, "none.sock"))
	if code != 0 || out != "" || !strings.Contains(errOut, "railse hook") {
		t.Fatalf("没有守护进程应不作决定并在 stderr 说明: code=%d out=%q err=%q", code, out, errOut)
	}
}

func TestHookEventsExitImmediately(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	stop := `{"session_id":"s1","cwd":"` + repo + `","hook_event_name":"Stop","last_assistant_message":"DONE"}`
	out, _, code := runHookWith(t, stop, "claude", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("事件应无输出退出 0: %d %q", code, out)
	}
	notify := `{"type":"agent-turn-complete","thread-id":"t","turn-id":"u","cwd":"` + repo + `","last-assistant-message":"DONE"}`
	out, _, code = runHookWith(t, "", "codex-notify", "--socket", sock, notify)
	if code != 0 || out != "" {
		t.Fatalf("notify 应无输出退出 0: %d %q", code, out)
	}
	b, _ := os.ReadFile(cfg.AuditPath)
	if strings.Count(string(b), `"kind":"done"`) != 2 {
		t.Fatalf("两条 done 事件应进审计:\n%s", b)
	}
}

func TestHookBadInputPassthrough(t *testing.T) {
	sock, _, _ := startTestAgent(t)
	out, errOut, code := runHookWith(t, "not json", "claude", "--socket", sock)
	if code != 0 || out != "" || errOut == "" {
		t.Fatalf("坏输入应不作决定: %d %q %q", code, out, errOut)
	}
	out, _, code = runHookWith(t, "{}", "nope", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("未知 CLI 也应不作决定: %d %q", code, out)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hookclient/ ./cmd/railse/ -run 'Permission|NoDaemon|Hook'`
Expected: FAIL，报 `undefined: Client`、`undefined: runHook`。

- [ ] **Step 3: 写客户端**

`internal/hookclient/client.go`:
```go
// Package hookclient 是钩子进程这一侧：连本地 socket，发一行，等一行。
package hookclient

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"time"

	"github.com/xufei547/railse-opc/internal/proto"
)

// Client 指向守护进程的 socket。
type Client struct {
	SocketPath     string
	ConnectTimeout time.Duration
}

func (c Client) dial() (net.Conn, error) {
	timeout := c.ConnectTimeout
	if timeout == 0 {
		timeout = 2 * time.Second
	}
	conn, err := net.DialTimeout("unix", c.SocketPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("连接守护进程 %s: %w", c.SocketPath, err)
	}
	return conn, nil
}

// Permission 发审批请求并等答复，直到 ctx 结束。
func (c Client) Permission(ctx context.Context, req proto.Request) (proto.Response, error) {
	conn, err := c.dial()
	if err != nil {
		return proto.Response{}, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	go func() { // ctx 取消（收到 SIGTERM）时关掉连接，守护进程会把请求标为 cancelled
		<-ctx.Done()
		conn.Close()
	}()
	if err := proto.WriteLine(conn, req); err != nil {
		return proto.Response{}, err
	}
	line, err := proto.ReadLine(bufio.NewReader(conn))
	if err != nil {
		return proto.Response{}, fmt.Errorf("等待答复: %w", err)
	}
	var resp proto.Response
	if err := proto.Decode(line, &resp); err != nil {
		return proto.Response{}, err
	}
	return resp, nil
}

// Event 发只展示的事件，整个过程不超过 2 秒。
func (c Client) Event(ev proto.Event) error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if err := proto.WriteLine(conn, ev); err != nil {
		return err
	}
	line, err := proto.ReadLine(bufio.NewReader(conn))
	if err != nil {
		return err
	}
	var ack proto.EventAck
	if err := proto.Decode(line, &ack); err != nil {
		return err
	}
	if !ack.OK {
		return fmt.Errorf("守护进程拒绝事件: %s", ack.Error)
	}
	return nil
}

// Control 发管理命令。
func (c Client) Control(op string) (proto.ControlReply, error) {
	conn, err := c.dial()
	if err != nil {
		return proto.ControlReply{}, err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := proto.WriteLine(conn, proto.Control{V: proto.Version, Kind: "control", Op: op}); err != nil {
		return proto.ControlReply{}, err
	}
	line, err := proto.ReadLine(bufio.NewReader(conn))
	if err != nil {
		return proto.ControlReply{}, err
	}
	var reply proto.ControlReply
	if err := proto.Decode(line, &reply); err != nil {
		return proto.ControlReply{}, err
	}
	return reply, nil
}
```

- [ ] **Step 4: 写 `railse hook`**

`cmd/railse/hook.go`:
```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xufei547/railse-opc/internal/adapter"
	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
	"github.com/xufei547/railse-opc/internal/proto"
)

// stdinReader 让测试能替换 stdin。
var stdinReader io.Reader = os.Stdin

const hookUsage = `用法:
  railse hook claude|codex|gemini [--socket 路径]      读 stdin 的钩子 JSON
  railse hook codex-notify [--socket 路径] <json>      Codex notify 的最后一个参数`

const maxStdin = 4 << 20

// runHook 永远以“不作决定”收场，除非守护进程明确给了 allow 或 deny。
func runHook(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, hookUsage)
		return 0
	}
	cli := args[0]
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	socket := fs.String("socket", "", "守护进程 socket 路径")
	if err := fs.Parse(args[1:]); err != nil {
		return passthrough(stderr, "参数错误: "+err.Error())
	}
	if *socket == "" {
		cfg, err := agent.DefaultConfig()
		if err != nil {
			return passthrough(stderr, err.Error())
		}
		*socket = cfg.SocketPath
	}
	client := hookclient.Client{SocketPath: *socket, ConnectTimeout: 2 * time.Second}

	var in adapter.Input
	var ad adapter.Adapter
	switch cli {
	case "codex-notify":
		if fs.NArg() != 1 {
			return passthrough(stderr, "codex-notify 需要一个 JSON 参数")
		}
		var err error
		in, err = adapter.ParseCodexNotify([]byte(fs.Arg(0)))
		if err != nil {
			return passthrough(stderr, err.Error())
		}
	default:
		var ok bool
		ad, ok = adapter.Get(cli)
		if !ok {
			return passthrough(stderr, "不认识的 CLI "+cli)
		}
		raw, err := io.ReadAll(io.LimitReader(stdin, maxStdin))
		if err != nil {
			return passthrough(stderr, "读 stdin: "+err.Error())
		}
		in, err = ad.Parse(raw)
		if err != nil {
			return passthrough(stderr, err.Error())
		}
	}

	switch in.Kind {
	case adapter.KindPermission:
		return hookPermission(ad, client, in, stdout, stderr)
	case adapter.KindQuestion, adapter.KindDone, adapter.KindSessionEnd:
		if err := client.Event(proto.Event{V: proto.Version, Kind: string(in.Kind), Session: in.Session, Message: in.Message}); err != nil {
			fmt.Fprintln(stderr, "railse hook: "+err.Error())
		}
		return 0
	default:
		return 0
	}
}

func hookPermission(ad adapter.Adapter, client hookclient.Client, in adapter.Input, stdout, stderr io.Writer) int {
	deadline := time.Duration(ad.DeadlineSeconds()) * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), deadline+10*time.Second)
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sigs)
	go func() {
		select {
		case <-sigs:
			cancel()
		case <-ctx.Done():
		}
	}()
	req := proto.Request{V: proto.Version, Kind: "permission", Session: in.Session, ToolName: in.ToolName, Payload: in.Payload, DeadlineSeconds: ad.DeadlineSeconds()}
	resp, err := client.Permission(ctx, req)
	if err != nil {
		return passthrough(stderr, err.Error())
	}
	out := ad.Render(adapter.Decision{Action: resp.Decision, Reason: resp.Reason})
	if len(out.Stdout) > 0 {
		stdout.Write(append(out.Stdout, '\n'))
	}
	return out.ExitCode
}

// passthrough 在 stderr 说明原因，stdout 不写，退出 0。
func passthrough(stderr io.Writer, why string) int {
	fmt.Fprintln(stderr, "railse hook: "+why+"，交回终端处理")
	return 0
}
```

在 `cmd/railse/main.go`：`run` 的 switch 加
```go
	case "hook":
		return runHook(args[1:], stdinReader, stdout, stderr)
```
usage 加 `  hook      各 CLI 钩子调用的翻译器（claude、codex、codex-notify、gemini）`。

`TestHookDenyWithRule` 通过 `hookclient.Client.Control("reload")` 重载规则，不依赖 Task 11 的 `railse agent reload`。

- [ ] **Step 5: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hookclient/ ./cmd/railse/ -race -v`
Expected: 全部 PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/hookclient/client.go internal/hookclient/client_test.go cmd/railse/hook.go cmd/railse/hook_test.go cmd/railse/main.go
git commit -m "feat: 钩子客户端与 railse hook 命令，任何异常都不作决定"
```

---

### Task 11: `railse agent run|status|pause|resume|reload`

**Files:**
- Create: `cmd/railse/agent.go`
- Create: `cmd/railse/agent_test.go`
- Modify: `cmd/railse/main.go`（switch 加 `agent`；usage 加一行）

**Interfaces:**
- Produces:
  - `railse agent run [--dir <配置目录>] [--foreground]`：建目录、初始化规则、打开审计、日志写 `agent.log`（`--foreground` 同时写 stderr）、监听、恢复暂停状态、跑到 SIGINT/SIGTERM；SIGHUP 重载规则
  - `railse agent status|pause|resume|reload [--dir]`：经 `hookclient.Control`，`status` 打印一行一项
  - 包内：`func runAgent(args []string, stdout, stderr io.Writer) int`、`func runAgentServe(ctx context.Context, cfg agent.Config, foreground bool, stderr io.Writer) error`（测试用 ctx 停止）

- [ ] **Step 1: 写失败的测试**

`cmd/railse/agent_test.go`:
```go
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
)

func TestAgentServeAndControl(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr) }()
	sock := filepath.Join(cfgDir, "agent.sock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket 未出现")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, f := range []string{"rules.yaml", "audit.jsonl", "agent.log"} {
		if _, err := os.Stat(filepath.Join(cfgDir, f)); err != nil {
			t.Fatalf("%s 应被创建: %v", f, err)
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "paused: false") || !strings.Contains(out.String(), "rules: 2") {
		t.Fatalf("status: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := run([]string{"agent", "pause", "--dir", cfgDir}, &out, &errOut); code != 0 {
		t.Fatalf("pause: %d %s", code, errOut.String())
	}
	out.Reset()
	run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut)
	if !strings.Contains(out.String(), "paused: true") {
		t.Fatalf("status after pause: %q", out.String())
	}
	run([]string{"agent", "resume", "--dir", cfgDir}, &out, &errOut)
	os.WriteFile(filepath.Join(cfgDir, "rules.yaml"), []byte("version: 1\nrules: []\n"), 0o600)
	out.Reset()
	if code := run([]string{"agent", "reload", "--dir", cfgDir}, &out, &errOut); code != 0 {
		t.Fatalf("reload: %d %s", code, errOut.String())
	}
	out.Reset()
	run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut)
	if !strings.Contains(out.String(), "rules: 0") {
		t.Fatalf("reload 后 rules 应为 0: %q", out.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve 退出出错: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve 没有在 ctx 取消后退出")
	}
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("退出后应删除 socket 文件")
	}
}

func TestAgentControlWithoutDaemon(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "status", "--dir", filepath.Join(dir, "none")}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "连接守护进程") {
		t.Fatalf("没有守护进程时应退出 1 并说明: %d %q", code, errOut.String())
	}
	if code := run([]string{"agent"}, &out, &errOut); code != 2 {
		t.Fatalf("缺子命令应退出 2, 得到 %d", code)
	}
}

func TestAgentRefusesSecondInstance(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr)
	sock := filepath.Join(cfgDir, "agent.sock")
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := runAgentServe(context.Background(), agent.ConfigIn(cfgDir), false, os.Stderr); err == nil || !strings.Contains(err.Error(), "已有守护进程") {
		t.Fatalf("第二个实例应拒绝启动: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run Agent`
Expected: FAIL，报 `undefined: runAgentServe`。

- [ ] **Step 3: 写实现**

`cmd/railse/agent.go`:
```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
)

const agentUsage = `用法:
  railse agent run [--dir 目录] [--foreground]   启动守护进程（前台运行；开机自启在第 5 份计划）
  railse agent status|pause|resume|reload [--dir 目录]`

func runAgent(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, agentUsage)
		return 2
	}
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "配置目录，默认 $RAILSE_HOME 或 ~/.railse")
	foreground := fs.Bool("foreground", false, "日志同时写到 stderr")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	var cfg agent.Config
	if *dir != "" {
		cfg = agent.ConfigIn(*dir)
	} else {
		c, err := agent.DefaultConfig()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		cfg = c
	}
	switch args[0] {
	case "run":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := runAgentServe(ctx, cfg, *foreground, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "status", "pause", "resume", "reload":
		reply, err := hookclient.Client{SocketPath: cfg.SocketPath, ConnectTimeout: 2 * time.Second}.Control(args[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !reply.OK {
			fmt.Fprintln(stderr, "失败:", reply.Error)
			return 1
		}
		if reply.Status != nil {
			st := reply.Status
			fmt.Fprintf(stdout, "paused: %v\nrules: %d\npending: %d\nmachine: %s\nrules_path: %s\nuptime: %s\n", st.Paused, st.Rules, st.Pending, st.MachineID, st.RulesPath, st.Uptime)
		} else {
			fmt.Fprintln(stdout, "ok")
		}
		return 0
	default:
		fmt.Fprintf(stderr, "未知的 agent 子命令 %q\n\n%s\n", args[0], agentUsage)
		return 2
	}
}

// runAgentServe 跑守护进程直到 ctx 取消。SIGHUP 重载规则。
func runAgentServe(ctx context.Context, cfg agent.Config, foreground bool, stderr io.Writer) error {
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	logFile, err := os.OpenFile(cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	var w io.Writer = logFile
	if foreground {
		w = io.MultiWriter(logFile, stderr)
	}
	logger := log.New(w, "railse-agent ", log.LstdFlags)
	rules, err := agent.LoadRules(cfg.RulesPath)
	if err != nil {
		return err
	}
	audit, err := agent.OpenAudit(cfg.AuditPath)
	if err != nil {
		return err
	}
	defer audit.Close()
	srv := agent.New(cfg, rules, audit, agent.LocalDecider{}, logger)
	if err := srv.Listen(); err != nil {
		return err
	}
	defer srv.Close()
	if err := srv.LoadState(); err != nil {
		logger.Printf("读状态文件: %v", err)
	}
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-hup:
				if err := rules.Reload(); err != nil {
					logger.Printf("SIGHUP 重载规则失败，保留旧规则: %v", err)
				} else {
					logger.Printf("SIGHUP 重载规则：%d 条", rules.Count())
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	logger.Printf("监听 %s，规则 %d 条，机器 %s", cfg.SocketPath, rules.Count(), cfg.MachineID)
	return srv.Serve(ctx)
}
```

在 `cmd/railse/main.go`：switch 加 `case "agent": return runAgent(args[1:], stdout, stderr)`，usage 加 `  agent     守护进程（run、status、pause、resume、reload）`。

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./cmd/railse/ -race -v`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add cmd/railse/agent.go cmd/railse/agent_test.go cmd/railse/main.go
git commit -m "feat: railse agent run 与本机管理子命令"
```

---

### Task 12: `railse setup hooks`

**Files:**
- Create: `cmd/railse/setup.go`
- Create: `cmd/railse/setup_test.go`
- Modify: `cmd/railse/main.go`（switch 加 `setup`；usage 加一行）

**Interfaces:**
- Produces:
  - `railse setup hooks [--project <目录>] [--claude] [--codex] [--gemini] [--codex-file <路径>] [--bin <路径>] [--dry-run]`：默认三个都装；Claude 写 `<project>/.claude/settings.json`，Gemini 写 `<project>/.gemini/settings.json`，Codex 写用户级 `~/.codex/hooks.json`（实测项目级钩子要项目被信任且需 `/hooks` 审核，用户级更稳；`--codex-file` 供测试与自定义）；`--bin` 默认 `os.Executable()` 的绝对路径
  - 合并规则：读现有文件（没有就当 `{}`），保证 `hooks` 是对象；每个事件数组里，删除“命令以 `<bin> hook` 开头”或“`name` 以 `railse-` 开头”的旧条目组，再追加本次的条目组；其他人的钩子原样保留；写回时保留原文件权限（新文件 0644），两空格缩进，末尾换行；重复运行结果字节相同
  - 钩子条目（与实测配置一致）：
    - Claude：`PermissionRequest` matcher `""` timeout 86400 → `<bin> hook claude`；`Notification`、`Stop`、`SessionEnd` 各一条无 timeout
    - Codex：`PermissionRequest` timeout 86400、`Stop`、`SessionEnd` timeout 3 → `<bin> hook codex`
    - Gemini：`BeforeTool` matcher `".*"` name `railse-before-tool` timeout 86400000 → `<bin> hook gemini`；`Notification` name `railse-notification`、`AfterAgent` name `railse-after-agent`、`SessionEnd` name `railse-session-end`
  - 结束时打印写了哪些文件，并对 Codex 和 Gemini 各打印一条提醒：Codex 只在交互会话里审批，钩子需在 `/hooks` 里审核，`notify` 需手动配置为 `<bin> hook codex-notify`；Gemini 只能拒绝不能放行，需 0.65.0 以上，不要开 yolo
  - 包内：`func runSetup(args []string, stdout, stderr io.Writer) int`、`func mergeHooks(existing []byte, bin string, entries map[string][]hookGroup) ([]byte, error)`、`type hookGroup struct{ Matcher *string; Hooks []hookEntry }`、`type hookEntry struct{ Name, Type, Command string; Timeout int }`

- [ ] **Step 1: 写失败的测试**

`cmd/railse/setup_test.go`:
```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupHooksWritesAllThree(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	project := filepath.Join(dir, "proj")
	os.MkdirAll(project, 0o755)
	codexFile := filepath.Join(dir, "codex", "hooks.json")
	var out, errOut bytes.Buffer
	code := run([]string{"setup", "hooks", "--project", project, "--codex-file", codexFile, "--bin", "/opt/railse"}, &out, &errOut)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut.String())
	}
	claude := readJSON(t, filepath.Join(project, ".claude", "settings.json"))
	pr := claude["hooks"].(map[string]any)["PermissionRequest"].([]any)[0].(map[string]any)
	hook := pr["hooks"].([]any)[0].(map[string]any)
	if pr["matcher"] != "" || hook["command"] != "/opt/railse hook claude" || hook["timeout"] != float64(86400) || hook["type"] != "command" {
		t.Fatalf("Claude PermissionRequest: %+v", pr)
	}
	for _, ev := range []string{"Notification", "Stop", "SessionEnd"} {
		if _, ok := claude["hooks"].(map[string]any)[ev]; !ok {
			t.Fatalf("Claude 缺 %s", ev)
		}
	}
	codex := readJSON(t, codexFile)
	se := codex["hooks"].(map[string]any)["SessionEnd"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)
	if se["timeout"] != float64(3) || se["command"] != "/opt/railse hook codex" {
		t.Fatalf("Codex SessionEnd: %+v", se)
	}
	gemini := readJSON(t, filepath.Join(project, ".gemini", "settings.json"))
	bt := gemini["hooks"].(map[string]any)["BeforeTool"].([]any)[0].(map[string]any)
	bh := bt["hooks"].([]any)[0].(map[string]any)
	if bt["matcher"] != ".*" || bh["name"] != "railse-before-tool" || bh["timeout"] != float64(86400000) || bh["command"] != "/opt/railse hook gemini" {
		t.Fatalf("Gemini BeforeTool: %+v", bt)
	}
	if !strings.Contains(out.String(), "Codex") || !strings.Contains(out.String(), "Gemini") || !strings.Contains(out.String(), "yolo") {
		t.Fatalf("应打印提醒: %s", out.String())
	}
}

func TestSetupHooksIsIdempotentAndPreservesOthers(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	project := filepath.Join(dir, "proj")
	os.MkdirAll(filepath.Join(project, ".claude"), 0o755)
	existing := `{"permissions":{"defaultMode":"default"},"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"/other/tool"}]}],"PreToolUse":[{"hooks":[{"type":"command","command":"echo hi"}]}]}}`
	os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte(existing), 0o644)
	args := []string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}
	var out, errOut bytes.Buffer
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	first, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	if code := run(args, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	second, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	if !bytes.Equal(first, second) {
		t.Fatalf("两次运行结果应相同:\n%s\n---\n%s", first, second)
	}
	doc := readJSON(t, filepath.Join(project, ".claude", "settings.json"))
	if doc["permissions"] == nil {
		t.Fatal("其他顶层字段应保留")
	}
	hooks := doc["hooks"].(map[string]any)
	if len(hooks["PermissionRequest"].([]any)) != 2 {
		t.Fatalf("别人的 PermissionRequest 钩子应保留且只加一组: %+v", hooks["PermissionRequest"])
	}
	if _, ok := hooks["PreToolUse"]; !ok {
		t.Fatal("别人的 PreToolUse 应保留")
	}
	if _, err := os.Stat(filepath.Join(project, ".gemini", "settings.json")); err == nil {
		t.Fatal("只选 --claude 时不应写 Gemini")
	}
}

func TestSetupHooksDryRunWritesNothing(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	project := filepath.Join(dir, "proj")
	os.MkdirAll(project, 0o755)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--codex-file", filepath.Join(dir, "c.json"), "--bin", "/opt/railse", "--dry-run"}, &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	if _, err := os.Stat(filepath.Join(project, ".claude")); err == nil {
		t.Fatal("dry-run 不应写文件")
	}
	if !strings.Contains(out.String(), "PermissionRequest") {
		t.Fatalf("dry-run 应打印将写入的内容: %s", out.String())
	}
}

func TestSetupHooksRejectsBadExisting(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	project := filepath.Join(dir, "proj")
	os.MkdirAll(filepath.Join(project, ".claude"), 0o755)
	os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte("{not json"), 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", project, "--claude", "--bin", "/opt/railse"}, &out, &errOut); code != 1 {
		t.Fatalf("坏的现有文件应退出 1 并不覆盖, 得到 %d", code)
	}
	b, _ := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	if string(b) != "{not json" {
		t.Fatal("坏文件不应被覆盖")
	}
}

func readJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%s 不是 JSON: %v", path, err)
	}
	return doc
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run Setup`
Expected: FAIL，`setup` 是未知子命令。

- [ ] **Step 3: 写实现**

`cmd/railse/setup.go`:
```go
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const setupUsage = `用法:
  railse setup hooks [--project 目录] [--claude] [--codex] [--gemini] [--codex-file 路径] [--bin 路径] [--dry-run]
    默认三个都装。Claude 与 Gemini 写项目级配置，Codex 写用户级 ~/.codex/hooks.json。`

type hookEntry struct {
	Name    string `json:"name,omitempty"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type hookGroup struct {
	Matcher *string     `json:"matcher,omitempty"`
	Hooks   []hookEntry `json:"hooks"`
}

func strp(s string) *string { return &s }

// hookPlan 给出每个 CLI 要写入的事件与条目。
func hookPlan(cli, bin string) map[string][]hookGroup {
	cmd := bin + " hook " + cli
	one := func(name string, timeout int) []hookGroup {
		return []hookGroup{{Hooks: []hookEntry{{Name: name, Type: "command", Command: cmd, Timeout: timeout}}}}
	}
	switch cli {
	case "claude":
		return map[string][]hookGroup{
			"PermissionRequest": {{Matcher: strp(""), Hooks: []hookEntry{{Type: "command", Command: cmd, Timeout: 86400}}}},
			"Notification":      one("", 0),
			"Stop":              one("", 0),
			"SessionEnd":        one("", 0),
		}
	case "codex":
		return map[string][]hookGroup{
			"PermissionRequest": {{Matcher: strp(""), Hooks: []hookEntry{{Type: "command", Command: cmd, Timeout: 86400}}}},
			"Stop":              one("", 0),
			"SessionEnd":        one("", 3),
		}
	case "gemini":
		return map[string][]hookGroup{
			"BeforeTool":   {{Matcher: strp(".*"), Hooks: []hookEntry{{Name: "railse-before-tool", Type: "command", Command: cmd, Timeout: 86400000}}}},
			"Notification": one("railse-notification", 0),
			"AfterAgent":   one("railse-after-agent", 0),
			"SessionEnd":   one("railse-session-end", 0),
		}
	}
	return nil
}

// isOurs 判断一个钩子条目组是不是 Railse 写的。
func isOurs(group map[string]any, bin string) bool {
	hooks, _ := group["hooks"].([]any)
	for _, h := range hooks {
		m, _ := h.(map[string]any)
		cmd, _ := m["command"].(string)
		name, _ := m["name"].(string)
		if strings.HasPrefix(cmd, bin+" hook") || strings.HasPrefix(name, "railse-") {
			return true
		}
	}
	return false
}

// mergeHooks 把我们的条目并进现有配置：删旧的 Railse 条目，保留别人的，追加新的。
func mergeHooks(existing []byte, bin string, entries map[string][]hookGroup) ([]byte, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &doc); err != nil {
			return nil, fmt.Errorf("现有配置不是合法 JSON，不覆盖: %w", err)
		}
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	// 先从所有事件里删掉我们旧的条目组
	for ev, raw := range hooks {
		groups, _ := raw.([]any)
		var kept []any
		for _, g := range groups {
			if gm, ok := g.(map[string]any); ok && isOurs(gm, bin) {
				continue
			}
			kept = append(kept, g)
		}
		if len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	// 再追加本次的
	for ev, groups := range entries {
		existingGroups, _ := hooks[ev].([]any)
		for _, g := range groups {
			b, _ := json.Marshal(g)
			var gm map[string]any
			json.Unmarshal(b, &gm)
			existingGroups = append(existingGroups, gm)
		}
		hooks[ev] = existingGroups
	}
	doc["hooks"] = hooks
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

func runSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "hooks" {
		fmt.Fprintln(stderr, setupUsage)
		return 2
	}
	fs := flag.NewFlagSet("setup hooks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", ".", "项目目录")
	doClaude := fs.Bool("claude", false, "只装 Claude Code")
	doCodex := fs.Bool("codex", false, "只装 Codex")
	doGemini := fs.Bool("gemini", false, "只装 Gemini")
	codexFile := fs.String("codex-file", "", "Codex 钩子文件，默认 ~/.codex/hooks.json")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	dryRun := fs.Bool("dry-run", false, "只打印不写")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if !*doClaude && !*doCodex && !*doGemini {
		*doClaude, *doCodex, *doGemini = true, true, true
	}
	if *bin == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*bin, _ = filepath.Abs(exe)
	}
	absProject, err := filepath.Abs(*project)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *codexFile == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*codexFile = filepath.Join(home, ".codex", "hooks.json")
	}
	targets := []struct {
		cli  string
		on   bool
		path string
	}{
		{"claude", *doClaude, filepath.Join(absProject, ".claude", "settings.json")},
		{"codex", *doCodex, *codexFile},
		{"gemini", *doGemini, filepath.Join(absProject, ".gemini", "settings.json")},
	}
	for _, tg := range targets {
		if !tg.on {
			continue
		}
		existing, err := os.ReadFile(tg.path)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(stderr, err)
			return 1
		}
		merged, err := mergeHooks(existing, *bin, hookPlan(tg.cli, *bin))
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", tg.path, err)
			return 1
		}
		if *dryRun {
			fmt.Fprintf(stdout, "== %s ==\n%s", tg.path, merged)
			continue
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(tg.path); err == nil {
			mode = st.Mode().Perm()
		}
		if err := os.MkdirAll(filepath.Dir(tg.path), 0o755); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := os.WriteFile(tg.path, merged, mode); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "已写入 %s\n", tg.path)
	}
	if *doCodex {
		fmt.Fprintf(stdout, "提醒 Codex：钩子只在交互会话里审批（codex exec 不触发）；首次使用请在 Codex 里用 /hooks 审核；notify 需手动配置为 %q。\n", *bin+" hook codex-notify")
	}
	if *doGemini {
		fmt.Fprintln(stdout, "提醒 Gemini：钩子只能拒绝不能放行，需要 Gemini CLI 0.65.0 以上，不要开 yolo 模式。")
	}
	return 0
}
```

在 `cmd/railse/main.go`：switch 加 `case "setup": return runSetup(args[1:], stdout, stderr)`，usage 加 `  setup     安装钩子（hooks）`。

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./cmd/railse/ -race -run Setup -v`
Expected: 4 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add cmd/railse/setup.go cmd/railse/setup_test.go cmd/railse/main.go
git commit -m "feat: railse setup hooks 把钩子并进三个 CLI 的配置"
```

---

### Task 13: 端到端测试、冒烟脚本与文档

**Files:**
- Create: `cmd/railse/e2e_test.go`
- Create: `scripts/smoke-claude.sh`
- Create: `docs/agent-local-loop.md`
- Modify: `docs/superpowers/specs/2026-10-21-railse-opc-design.md` 第 1.3 节仓库布局（加 `internal/proto`、`internal/redact`、`internal/hookclient`、`scripts/`）

**Interfaces:**
- 端到端测试用真实录制样本走完整链路：`railse agent`（进程内）→ `railse hook claude` 读 `testdata` 样本（cwd 改写到临时仓库）→ 输出与审计。
- 冒烟脚本手动跑真实 Claude Code，不进 CI。

- [ ] **Step 1: 写端到端测试**

`cmd/railse/e2e_test.go`:
```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 用真实录制的样本走完整链路，只把 cwd 改到临时仓库。
func sampleWithCwd(t *testing.T, rel, cwd string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "adapter", "testdata", rel))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	doc["cwd"] = cwd
	out, _ := json.Marshal(doc)
	return string(out)
}

func TestEndToEndClaudeSamples(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	// git push origin main 是 outward，没有用户规则时交回终端
	out, _, code := runHookWith(t, sampleWithCwd(t, "claude/2.1.280/PermissionRequest.json", repo), "claude", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("git push 应不作决定: %d %q", code, out)
	}
	// touch spike-proof.txt 在仓库内是 write_in_project，内置规则放行
	out, _, code = runHookWith(t, sampleWithCwd(t, "claude/2.1.280/PermissionRequest.interactive.json", repo), "claude", "--socket", sock)
	if code != 0 || !strings.Contains(out, `"behavior":"allow"`) {
		t.Fatalf("touch 应放行: %d %q", code, out)
	}
	for _, f := range []string{"claude/2.1.280/Notification.permission_prompt.json", "claude/2.1.280/Stop.json", "claude/2.1.280/SessionEnd.json", "claude/2.1.280/PreToolUse.json"} {
		out, _, code = runHookWith(t, sampleWithCwd(t, f, repo), "claude", "--socket", sock)
		if code != 0 || out != "" {
			t.Fatalf("%s: %d %q", f, code, out)
		}
	}
	b, _ := os.ReadFile(cfg.AuditPath)
	audit := string(b)
	for _, want := range []string{`"state":"passthrough"`, `"state":"auto_allowed"`, `"kind":"question"`, `"kind":"done"`, `"kind":"session_end"`} {
		if !strings.Contains(audit, want) {
			t.Fatalf("审计缺 %s:\n%s", want, audit)
		}
	}
	if strings.Contains(audit, "CLAUDE_CODE_MESSAGING") {
		t.Fatal("审计里不能出现钩子进程的环境变量")
	}
}

func TestEndToEndCodexAndGeminiSamples(t *testing.T) {
	sock, repo, _ := startTestAgent(t)
	out, _, code := runHookWith(t, sampleWithCwd(t, "codex/0.145.0/PermissionRequest.json", repo), "codex", "--socket", sock)
	if code != 0 || !strings.Contains(out, `"behavior":"allow"`) {
		t.Fatalf("Codex touch 应放行: %d %q", code, out)
	}
	out, _, code = runHookWith(t, sampleWithCwd(t, "gemini/0.65.0/BeforeTool.json", repo), "gemini", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("Gemini 的放行必须渲染为不作决定: %d %q", code, out)
	}
}
```

- [ ] **Step 2: 跑测试确认通过**

Run: `go test ./cmd/railse/ -race -run EndToEnd -v`
Expected: 2 个测试 PASS（它们依赖前面 Task 的实现，不需要 RED 步骤；若失败说明链路有缺口，先修链路）。

- [ ] **Step 3: 写冒烟脚本**

`scripts/smoke-claude.sh`:
```bash
#!/usr/bin/env bash
# 真实 Claude Code 冒烟：起守护进程、装项目钩子、让 claude -p 跑一条 touch，看审计。
# 需要本机已装 claude 且已登录。不进 CI。
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d /tmp/railse-smoke.XXXX)"
export RAILSE_HOME="$WORK/home"
cd "$ROOT" && go build -o "$WORK/railse" ./cmd/railse
"$WORK/railse" agent run --foreground &
AGENT=$!
trap 'kill $AGENT 2>/dev/null || true' EXIT
sleep 1
mkdir -p "$WORK/proj" && (cd "$WORK/proj" && git init -q)
"$WORK/railse" setup hooks --project "$WORK/proj" --claude --bin "$WORK/railse"
(cd "$WORK/proj" && env -i HOME="$HOME" PATH="$PATH" LANG="${LANG:-C}" TERM=dumb RAILSE_HOME="$RAILSE_HOME" \
  claude -p "Run exactly this shell command with the Bash tool and nothing else: touch smoke-proof.txt . Then reply DONE." \
  --setting-sources project --output-format json > "$WORK/claude.json")
echo "== claude 输出 =="; cat "$WORK/claude.json"; echo
echo "== 审计 =="; cat "$RAILSE_HOME/audit.jsonl"
test -f "$WORK/proj/smoke-proof.txt" && echo "OK: 文件已创建，钩子放行生效" || echo "FAIL: 文件未创建"
```
`chmod +x scripts/smoke-claude.sh`。

- [ ] **Step 4: 写文档并更新设计文档布局**

`docs/agent-local-loop.md`（一页）：怎么起守护进程、装钩子、看审计、改规则（`~/.railse/rules.yaml` 后 `railse agent reload`）、暂停；三家 CLI 的能力差异表（Claude 全功能；Codex 仅交互、需 `/hooks` 审核、notify 手动；Gemini 仅拒绝、≥0.65、勿用 yolo）；没有 hub 时 ask 一律交回终端。

设计文档 1.3 的布局代码块加四行：
```
  internal/proto/      # 钩子与守护进程的一行 JSON 协议
  internal/redact/     # 凭据脱敏
  internal/hookclient/ # 钩子侧客户端
  scripts/             # 冒烟脚本
```

- [ ] **Step 5: 全量门禁**

Run: `gofmt -l . && go vet ./... && go test ./... -race -count=1`
Expected: 全过。

- [ ] **Step 6: 提交**

```bash
git add cmd/railse/e2e_test.go scripts/smoke-claude.sh docs/agent-local-loop.md docs/superpowers/specs/2026-10-21-railse-opc-design.md
git commit -m "test: 用录制样本的端到端测试；冒烟脚本与本地闭环说明"
```

---

## 完成标准

- `go test ./... -race -count=1` 全过，`gofmt -l .` 无输出，`go vet ./...` 无错，`go.mod` 没有新依赖。
- 在本机：`railse agent run --foreground` 起来后，`railse setup hooks --project <某个 git 仓库> --claude` 装好钩子，`scripts/smoke-claude.sh` 跑通，`touch` 被内置规则放行且审计里有 `auto_allowed`，`git push` 类命令交回终端弹原生框。
- `railse hook` 在守护进程未运行、输入损坏、收到 SIGTERM 时都输出空 stdout 并退出 0。
- Gemini 的 `railse hook gemini` 对放行只输出空；对 deny 输出 `{"decision":"deny","reason":"…"}`。
- 审计文件和 socket 权限为 0600，配置目录 0700；审计里的命令已脱敏，且不含钩子进程的环境变量。

## 自检记录

- **规格覆盖**：设计文档 1 节四个子命令 → `hook`（Task 10）、`agent`（Task 11）、`setup hooks`（Task 12，接入码与自启留第 5 份）；1.1 三条原则 → 适配器无策略（Task 3 至 5）、永不卡死（Task 8、10 的兜底）、hub 为真相（本计划以本地审计为临时真相，第 3 份接 hub）；3.1 步骤 1 至 3 → Task 8、10；3.2 超时过期 → Task 8 `waitDecision`；3.2 终端里先处理了 → Task 8 `TestConnDropCancelsPending`；3.3 question/done 与压制 → Task 9；5.1 socket 0600 → Task 8；5.3 脱敏 → Task 2、8；5.4 异常总原则 → Task 10；第 8 节实测结论 → Task 3 至 5 的格式与期限、Task 12 的配置形状与提醒。
- **占位扫描**：无 TBD、TODO；Task 8 的 `handleEvent`/`handleControl` 占位在 Task 9 内替换，有明确文本。
- **类型一致性**：`proto.Request/Response/Event/Control` 字段在 Task 1、8、9、10 一致；`adapter.Input/Decision/Output` 在 Task 3 至 5 与 Task 10 一致；`agent.Config` 字段在 Task 6、8、11 一致；`Decider`/`Outcome`/`PendingRequest` 在 Task 8 与测试一致；`runHook(args, stdin, stdout, stderr)` 签名在 Task 10 与 Task 13 一致。
- **已知取舍**：Codex 长时间等待会被模型放弃（实测约 9 分钟），本计划不做去重，第 3 份计划在 hub 侧按 `session_id`、`turn_id`、`tool_input.command` 去重；守护进程的 pending 只在内存里，重启即丢，钩子那头连接断开会自动兜底。
