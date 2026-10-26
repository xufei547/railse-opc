# Railse OPC hub、存储与协议 实施计划（第 3 份，共 5 份）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 做出 `railse hub`：一个跑在云服务器上的 HTTP 服务，持有 SQLite 里的请求、规则、机器与审计，通过 WebSocket 接各机器的守护进程，通过 REST 与 WebSocket 给手机网页（第 4 份计划）和 webhook 来源提供接口。守护进程这边把第 2 份计划的 `LocalDecider` 换成 `HubDecider`：引擎答 ask 的请求送到 hub 等人决定，决定回来交给钩子；规则以 hub 为准下发，本机只缓存；审计异步上报。

**Architecture:** hub 是唯一真相（设计文档 1.1 原则三）。守护进程向外发起一条 WebSocket 长连接，断线重连后用 `hello` 携带本机 pending 列表对齐；hub 对每台机器只保留一条活动连接。请求在 hub 里的状态机与设计文档 2.3 一致，新增的 `passthrough` 只由守护进程产生并经审计上报。hub 自己没有任何向机器下发命令的通道（5.2）。TLS、推送、PWA 静态页不在本计划：hub 先监听明文 HTTP 供本机与反向代理使用，第 4 份加推送与网页，第 5 份加 ACME 与部署。

**Tech Stack:** Go 1.24+；新增三个依赖：`modernc.org/sqlite v1.61.0`（纯 Go SQLite，不需要 CGO，便于第 5 份交叉编译）、`github.com/coder/websocket v1.8.15`（纯 Go WebSocket）、`golang.org/x/crypto v0.58.0`（argon2id 密码哈希）。其余用标准库 `net/http`、`database/sql`、`crypto/rand`、`embed`。

**设计文档：** `docs/superpowers/specs/2026-10-21-railse-opc-design.md` 第 1、2、3、5 节（已含 2026-10-22 的偏离处理）。**前置：** 第 1、2 份计划已合并到 main（提交 b11ec43 之后），`internal/policy`、`internal/agent`、`internal/proto`、`internal/redact`、`internal/hookclient` 可用。第 2 份计划执行会话留下的接入契约（本计划逐条落实）：`PendingRequest` 加项目根、脱敏载荷、引擎理由；`Pending` 可枚举以便重连上报；钩子消失用 `context.Cause` 的 `errHookGone` 区分；Gemini 的请求不进 hub 等待；Codex 的重发去重；“拒绝并记住”存为 `source: user`；上报 hub 前的脱敏。

**后续：** 第 4 份 PWA 与 Web Push（消费本计划的 REST 与 `/ws/client`，用本计划建好的 `push_subscriptions` 表）；第 5 份 `setup` 的接入码与开机自启、`doctor`、ACME、发布。

## Global Constraints

- 模块路径 `github.com/xufei547/railse-opc`，`go 1.24`。本计划只允许新增上述三个依赖，版本固定。
- `internal/policy` 仍不做 IO。hub 调用 `policy.ParseRuleSet`、`NewEngine`、`ProposeRule`、`Validate`，把它们的结果存进 SQLite。
- hub 的数据目录默认 `$RAILSE_HUB_HOME`，否则 `~/.railse-hub`（与守护进程的 `~/.railse` 分开，同一台机器可以两者都跑）；目录 0700，数据库文件 `hub.db` 0600。
- 守护进程到 hub 的认证：`railse hub token` 生成 10 分钟有效的一次性接入码；`railse agent enroll` 用它换取本机长期密钥，存 `~/.railse/machine.json`（0600）；WebSocket 握手用 `Authorization: Bearer <密钥>`；hub 只存密钥的 SHA-256。例外：webhook 出站回调令牌明文存储，回调完成或重试用尽后清空（2026-10-22 用户决定 D）。
- 手机到 hub 的认证：密码用 argon2id（time 1、memory 64 MiB、threads 4、盐 16 字节）；登录成功发 32 字节随机 token，hub 存其 SHA-256，30 天有效，放 `railse_session` cookie（HttpOnly、SameSite=Lax、Path=/；请求经 TLS 时加 Secure）；登录接口每个来源 IP 每分钟最多 5 次。
- WebSocket 帧为 JSON 文本帧，每帧一个对象，带 `"t"` 字段；单帧最大 1 MiB；hub 每 30 秒 ping，90 秒无 pong 断开；守护进程重连退避 1、2、4、8、16、30 秒。
- 守护进程上报 hub 的载荷必须先经 `redact.Payload`，事件消息经 `redact.Text`；hub 不信任守护进程送来的任何路径或命令，只存储与展示。
- hub 对每条请求只能回 `allow` 或 `deny`，不能让守护进程执行任何命令（设计 5.2）。Gemini 会话的请求不进 hub 等待（守护进程直接 passthrough），只作为审计上报。
- 请求去重：同一机器、同一 `cli_session_id`、同一 `turn_id`、同一工具名、同一载荷哈希，15 分钟内再次出现且旧的仍 pending，旧的标 `cancelled`（decided_by `resend`）。
- 每个 Task 结束时 `gofmt -l .` 无输出、`go vet ./...` 无错、`go test ./... -race` 全过，再提交。测试里的目录用 `os.MkdirTemp("", "rs")`（Unix socket 路径长度）。提交信息用中文。
- 许可证 Apache 2.0。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/hub/wire/wire.go`、`wire_test.go` | 守护进程与 hub 之间的 WebSocket 帧类型与编解码 |
| `internal/hub/store/store.go` | 打开数据库、迁移、`settings` 表读写、事务助手 |
| `internal/hub/store/schema.sql` | 建表语句（embed） |
| `internal/hub/store/machines.go` | 机器、接入码、客户端会话 token |
| `internal/hub/store/requests.go` | 请求与会话：插入、按状态查询、决定、送达、去重、重连对齐 |
| `internal/hub/store/rules.go` | 规则：单条存 YAML、版本号、导入导出成 `policy.RuleSet`、命中计数 |
| `internal/hub/store/audit.go` | 审计事件追加与查询 |
| `internal/hub/store/*_test.go` | 存储层测试 |
| `internal/hub/auth.go` | argon2id、登录限速、cookie 中间件、接入码校验 |
| `internal/hub/agents.go` | `/ws/agent`：每台机器一条连接，hello/welcome、ping/pong、收发、重连对齐 |
| `internal/hub/requests.go` | hub 侧请求生命周期：登记、去重、决定（含记住）、送达、取消、过期、广播 |
| `internal/hub/rules.go` | 规则服务：列表、启停、删除、导入、导出、学习、快照下发 |
| `internal/hub/api.go` | 客户端 REST 与 `/ws/client` 广播 |
| `internal/hub/webhook.go` | `/api/webhook/approvals` 接入与回调 |
| `internal/hub/server.go` | `Server` 组装、路由、`Run` |
| `internal/hub/*_test.go` | hub 集成测试（httptest） |
| `internal/agent/requests.go`（改） | `PendingRequest` 加 `ProjectRoot`、`Redacted`、`EngineReason`；`Pending.List()` |
| `internal/agent/server.go`（改） | 填充新字段；`AuditSink` 转发 |
| `internal/agent/rules.go`（改） | `Replace(rs policy.RuleSet, version int)` 并写缓存文件 |
| `internal/agent/machine.go` | `machine.json` 读写 |
| `internal/agent/hubclient.go`、`hubclient_test.go` | WebSocket 客户端：连接、认证、重连、分发、审计 outbox |
| `internal/agent/hubdecider.go`、`hubdecider_test.go` | `HubDecider` |
| `cmd/railse/hub.go`、`hub_test.go` | `railse hub run|password|token|webhook-token` |
| `cmd/railse/agent.go`（改）、`agent_test.go`（改） | `railse agent enroll`；`run` 有 `machine.json` 时接 hub |
| `cmd/railse/e2e_hub_test.go` | hub + 守护进程 + 钩子的进程内闭环 |
| `docs/hub.md` | hub 的启动、接入、API 一页说明 |

---

### Task 1: 依赖与 WebSocket 帧类型

**Files:**
- Modify: `go.mod`、`go.sum`
- Create: `internal/hub/wire/wire.go`
- Create: `internal/hub/wire/wire_test.go`

**Interfaces:**
- Produces（导出，包 `wire`）:
  - `const MaxFrame = 1 << 20`
  - 帧类型常量：`THello`、`TWelcome`、`TPending`、`TCancel`、`TExpired`、`TDecision`、`TDelivered`、`TRules`、`TPaused`、`TAudit`、`TPing`、`TPong`、`TError`
  - `type Envelope struct{ T string }`，`func Kind(frame []byte) (string, error)`
  - `type PendingSummary struct{ RequestID string; CreatedAt time.Time }`
  - `type Hello struct{ T, MachineID, AgentVersion, HomeDir string; Pending []PendingSummary }`
  - `type Welcome struct{ T string; RulesVersion int; RulesYAML string; Paused bool; ServerTime time.Time }`
  - `type PendingRequest struct{ T string; RequestID string; Session proto.Session; ToolName string; Payload policy.Payload; Risk, ProjectRoot, EngineReason string; DeadlineSeconds int; CreatedAt time.Time }`
  - `type Cancel struct{ T, RequestID, Reason string }`、`type Expired struct{ T, RequestID string }`
  - `type Decision struct{ T, RequestID, Action, Reason, RuleID, DecidedBy string }`
  - `type Delivered struct{ T, RequestID string }`
  - `type Rules struct{ T string; Version int; YAML string }`、`type Paused struct{ T string; Paused bool }`
  - `type Audit struct{ T string; Entries []json.RawMessage }`（每项是守护进程 `agent.AuditEntry` 的 JSON；`wire` 不导入 `agent`，否则守护进程引用 `wire` 时会循环依赖）
  - `type Ping struct{ T string }`、`type Pong struct{ T string }`、`type Error struct{ T, Message string }`
  - `func Marshal(v any) ([]byte, error)`（超过 `MaxFrame` 报错）、`func Unmarshal(frame []byte, v any) error`

- [ ] **Step 1: 加依赖**

Run:
```bash
go get modernc.org/sqlite@v1.61.0 github.com/coder/websocket@v1.8.15 golang.org/x/crypto@v0.58.0 && go mod tidy && go build ./... && git diff --stat go.mod
```
Expected: 三个 `require` 进入 `go.mod`，`go build ./...` 通过（尚未引用，`go mod tidy` 可能把它们标为 `// indirect`，到各 Task 引用后再 tidy 一次会转为直接依赖）。

- [ ] **Step 2: 写失败的测试**

`internal/hub/wire/wire_test.go`:
```go
package wire

import (
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func TestRoundTripAndKind(t *testing.T) {
	in := PendingRequest{T: TPending, RequestID: "r1",
		Session: proto.Session{Tool: "claude", CLISessionID: "s", Cwd: "/p"},
		ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push"},
		Risk: "outward", ProjectRoot: "/p", EngineReason: "无规则命中", DeadlineSeconds: 86390, CreatedAt: time.Unix(1, 0).UTC()}
	b, err := Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	kind, err := Kind(b)
	if err != nil || kind != TPending {
		t.Fatalf("Kind = %q, %v", kind, err)
	}
	var out PendingRequest
	if err := Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.RequestID != "r1" || out.Payload.Command != "git push" || out.Risk != "outward" || !out.CreatedAt.Equal(in.CreatedAt) {
		t.Fatalf("往返不一致: %+v", out)
	}
}

func TestMarshalRejectsHugeFrame(t *testing.T) {
	_, err := Marshal(Error{T: TError, Message: strings.Repeat("x", MaxFrame)})
	if err == nil {
		t.Fatal("超过 MaxFrame 应报错")
	}
}

func TestKindErrors(t *testing.T) {
	if _, err := Kind([]byte("nope")); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if _, err := Kind([]byte(`{"x":1}`)); err == nil {
		t.Fatal("缺 t 应报错")
	}
}
```
- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/hub/wire/`
Expected: FAIL，报 `undefined: PendingRequest`。

- [ ] **Step 4: 写最小实现**

`internal/hub/wire/wire.go`:
```go
// Package wire 定义守护进程与 hub 之间 WebSocket 上的帧。每帧一个 JSON 对象，t 字段是种类。
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// MaxFrame 是单帧最大字节数。
const MaxFrame = 1 << 20

// 帧种类。
const (
	THello     = "hello"     // 守护进程 → hub，连接后的第一帧
	TWelcome   = "welcome"   // hub → 守护进程，带规则快照与暂停状态
	TPending   = "pending"   // 守护进程 → hub，引擎答 ask 的请求
	TCancel    = "cancel"    // 守护进程 → hub，钩子消失
	TExpired   = "expired"   // 守护进程 → hub，等待过期
	TDecision  = "decision"  // hub → 守护进程
	TDelivered = "delivered" // 守护进程 → hub，决定已交给钩子
	TRules     = "rules"     // hub → 守护进程，规则快照
	TPaused    = "paused"    // hub → 守护进程，总开关
	TAudit     = "audit"     // 守护进程 → hub，审计批次
	TPing      = "ping"
	TPong      = "pong"
	TError     = "error"
)

// ErrFrameTooLarge 表示帧超过 MaxFrame。
var ErrFrameTooLarge = errors.New("帧超过 1 MiB")

// Envelope 只用来读 t 字段。
type Envelope struct {
	T string `json:"t"`
}

// PendingSummary 是 hello 里携带的本机未完成请求。
type PendingSummary struct {
	RequestID string    `json:"request_id"`
	CreatedAt time.Time `json:"created_at"`
}

// Hello 是守护进程连上后的第一帧。认证在 HTTP 头里，这里只报身份与状态。
type Hello struct {
	T            string           `json:"t"`
	MachineID    string           `json:"machine_id"`
	AgentVersion string           `json:"agent_version"`
	HomeDir      string           `json:"home_dir"`
	Pending      []PendingSummary `json:"pending"`
}

// Welcome 是 hub 的回应。
type Welcome struct {
	T            string    `json:"t"`
	RulesVersion int       `json:"rules_version"`
	RulesYAML    string    `json:"rules_yaml"`
	Paused       bool      `json:"paused"`
	ServerTime   time.Time `json:"server_time"`
}

// PendingRequest 是送到 hub 等人决定的请求，载荷已脱敏。
type PendingRequest struct {
	T               string         `json:"t"`
	RequestID       string         `json:"request_id"`
	Session         proto.Session  `json:"session"`
	ToolName        string         `json:"tool_name"`
	Payload         policy.Payload `json:"payload"`
	Risk            string         `json:"risk"`
	ProjectRoot     string         `json:"project_root"`
	EngineReason    string         `json:"engine_reason"`
	DeadlineSeconds int            `json:"deadline_seconds"`
	CreatedAt       time.Time      `json:"created_at"`
}

// Cancel 表示钩子在决定前退出。
type Cancel struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
	Reason    string `json:"reason"`
}

// Expired 表示守护进程侧等待过期。
type Expired struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
}

// Decision 是 hub 下发的决定，Action 为 allow 或 deny。
type Decision struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
	RuleID    string `json:"rule_id,omitempty"`
	DecidedBy string `json:"decided_by"`
}

// Delivered 是守护进程确认决定已交给钩子。
type Delivered struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
}

// Rules 是规则快照。
type Rules struct {
	T       string `json:"t"`
	Version int    `json:"version"`
	YAML    string `json:"yaml"`
}

// Paused 是总开关。
type Paused struct {
	T      string `json:"t"`
	Paused bool   `json:"paused"`
}

// Audit 是守护进程上报的审计批次，每项是 agent.AuditEntry 的 JSON。
type Audit struct {
	T       string            `json:"t"`
	Entries []json.RawMessage `json:"entries"`
}

// Ping、Pong、Error 是控制帧。
type Ping struct {
	T string `json:"t"`
}
type Pong struct {
	T string `json:"t"`
}
type Error struct {
	T       string `json:"t"`
	Message string `json:"message"`
}

// Kind 读帧种类。
func Kind(frame []byte) (string, error) {
	var e Envelope
	if err := json.Unmarshal(frame, &e); err != nil {
		return "", fmt.Errorf("解析帧: %w", err)
	}
	if e.T == "" {
		return "", errors.New("帧缺少 t 字段")
	}
	return e.T, nil
}

// Marshal 编码一帧。
func Marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFrame {
		return nil, ErrFrameTooLarge
	}
	return b, nil
}

// Unmarshal 解码一帧。
func Unmarshal(frame []byte, v any) error {
	if len(frame) > MaxFrame {
		return ErrFrameTooLarge
	}
	return json.Unmarshal(frame, v)
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/wire/ -v`
Expected: 3 个测试 PASS。

- [ ] **Step 6: 提交**

```bash
git add go.mod go.sum internal/hub/wire/wire.go internal/hub/wire/wire_test.go
git commit -m "feat(hub): 三个新依赖与守护进程到 hub 的帧类型"
```

---

### Task 2: 存储：打开、迁移、settings 与事务助手

**Files:**
- Create: `internal/hub/store/store.go`
- Create: `internal/hub/store/schema.sql`
- Create: `internal/hub/store/store_test.go`

**Interfaces:**
- Produces（导出，包 `store`）:
  - `type Store struct{ db *sql.DB }`，`func Open(path string) (*Store, error)`（`file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)`，新建文件 0600，跑迁移）、`func (s *Store) Close() error`
  - `func (s *Store) GetSetting(key string) (string, bool, error)`、`func (s *Store) SetSetting(key, value string) error`
  - `func (s *Store) Tx(fn func(tx *sql.Tx) error) error`
  - `func Now() string`：RFC3339Nano UTC，所有时间列用它
  - `var ErrNotFound = errors.New("not found")`
  - 包内：`const schemaVersion = 1`，`//go:embed schema.sql`

- [ ] **Step 1: 写 schema**

`internal/hub/store/schema.sql`:
```sql
-- Railse OPC hub 数据库，版本 1。时间列一律 RFC3339Nano UTC 文本。
CREATE TABLE IF NOT EXISTS schema_version (v INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS machines (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  secret_hash   TEXT NOT NULL,
  home_dir      TEXT NOT NULL DEFAULT '',
  agent_version TEXT NOT NULL DEFAULT '',
  enrolled_at   TEXT NOT NULL,
  last_seen     TEXT,
  revoked       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS enroll_tokens (
  token_hash TEXT PRIMARY KEY,
  expires_at TEXT NOT NULL,
  used       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS client_sessions (
  token_hash TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id             TEXT PRIMARY KEY,
  machine_id     TEXT NOT NULL REFERENCES machines(id),
  tool           TEXT NOT NULL,
  cli_session_id TEXT NOT NULL,
  cwd            TEXT NOT NULL DEFAULT '',
  project        TEXT NOT NULL DEFAULT '',
  started_at     TEXT NOT NULL,
  last_event_at  TEXT NOT NULL,
  UNIQUE (machine_id, tool, cli_session_id)
);

CREATE TABLE IF NOT EXISTS requests (
  id            TEXT PRIMARY KEY,
  machine_id    TEXT NOT NULL,
  session_id    TEXT,
  kind          TEXT NOT NULL,            -- permission、question、done、webhook
  tool_name     TEXT NOT NULL DEFAULT '',
  payload       TEXT NOT NULL,            -- JSON，已脱敏
  payload_hash  TEXT NOT NULL DEFAULT '',
  risk          TEXT NOT NULL DEFAULT '',
  project_root  TEXT NOT NULL DEFAULT '',
  engine_reason TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL,            -- pending、auto_allowed、auto_denied、approved、denied、passthrough、expired、cancelled
  decided_by    TEXT NOT NULL DEFAULT '',
  decision_note TEXT NOT NULL DEFAULT '',
  rule_id       TEXT NOT NULL DEFAULT '',
  turn_id       TEXT NOT NULL DEFAULT '',
  cli_session_id TEXT NOT NULL DEFAULT '',
  title         TEXT NOT NULL DEFAULT '', -- webhook 用
  callback      TEXT NOT NULL DEFAULT '', -- webhook 用，JSON
  created_at    TEXT NOT NULL,
  expires_at    TEXT,
  decided_at    TEXT,
  delivered_at  TEXT
);
CREATE INDEX IF NOT EXISTS requests_state_created ON requests (state, created_at);
CREATE INDEX IF NOT EXISTS requests_machine_created ON requests (machine_id, created_at);

CREATE TABLE IF NOT EXISTS rules (
  id           TEXT PRIMARY KEY,
  yaml         TEXT NOT NULL,            -- 单条规则的 YAML
  source       TEXT NOT NULL,
  enabled      INTEGER NOT NULL DEFAULT 1,
  hit_count    INTEGER NOT NULL DEFAULT 0,
  last_hit_at  TEXT,
  created_at   TEXT NOT NULL,
  created_from TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS audit_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  ts         TEXT NOT NULL,
  machine_id TEXT NOT NULL DEFAULT '',
  actor      TEXT NOT NULL,              -- user、rule:<id>、system、agent
  request_id TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  detail     TEXT NOT NULL DEFAULT ''    -- JSON
);
CREATE INDEX IF NOT EXISTS audit_ts ON audit_events (ts);

CREATE TABLE IF NOT EXISTS push_subscriptions (
  id         TEXT PRIMARY KEY,
  endpoint   TEXT NOT NULL,
  keys       TEXT NOT NULL,              -- JSON
  created_at TEXT NOT NULL,
  last_ok_at TEXT
);
```

- [ ] **Step 2: 写失败的测试**

`internal/hub/store/store_test.go`:
```go
package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	s, err := Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenMigratesAndSetsPerms(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("数据库权限 = %v, %v", st.Mode().Perm(), err)
	}
	var v int
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil || v != 1 {
		t.Fatalf("schema_version = %d, %v", v, err)
	}
	var mode string
	s.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal_mode = %s", mode)
	}
	s.Close()
	// 重复打开不应报错（迁移幂等）
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s2.Close()
}

func TestSettings(t *testing.T) {
	s := openTemp(t)
	if _, ok, err := s.GetSetting("x"); err != nil || ok {
		t.Fatalf("不存在的键: ok=%v err=%v", ok, err)
	}
	if err := s.SetSetting("x", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("x", "2"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.GetSetting("x")
	if err != nil || !ok || v != "2" {
		t.Fatalf("GetSetting = %q %v %v", v, ok, err)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	s := openTemp(t)
	err := s.Tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO settings(key, value) VALUES('a','1')"); err != nil {
			return err
		}
		return os.ErrInvalid
	})
	if err == nil {
		t.Fatal("应把错误传出")
	}
	if _, ok, _ := s.GetSetting("a"); ok {
		t.Fatal("出错的事务应回滚")
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/hub/store/`
Expected: FAIL，报 `undefined: Open`。

- [ ] **Step 4: 写最小实现**

`internal/hub/store/store.go`:
```go
// Package store 是 hub 的 SQLite 存储层。所有时间列是 RFC3339Nano UTC 文本。
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const schemaVersion = 1

// ErrNotFound 表示查无此行。
var ErrNotFound = errors.New("not found")

// Store 包着一个数据库连接池。
type Store struct {
	db *sql.DB
}

// Open 打开或创建数据库，设 WAL 与 busy_timeout，跑迁移，文件权限收紧为 0600。
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 单写者，用一个连接避免 SQLITE_BUSY
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开数据库 %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("建表: %w", err)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err := s.db.Exec("INSERT INTO schema_version(v) VALUES(?)", schemaVersion)
		return err
	}
	var v int
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil {
		return err
	}
	if v != schemaVersion {
		return fmt.Errorf("数据库版本 %d 与程序版本 %d 不一致", v, schemaVersion)
	}
	return nil
}

// Close 关闭连接池。
func (s *Store) Close() error { return s.db.Close() }

// Now 返回统一格式的当前时间。
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// GetSetting 读设置，不存在时 ok 为 false。
func (s *Store) GetSetting(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting 写设置，存在则覆盖。
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec("INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// Tx 在一个事务里跑 fn，出错回滚。
func (s *Store) Tx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/store/ -v`
Expected: 3 个测试 PASS。`go mod tidy` 后 `modernc.org/sqlite` 变为直接依赖。

- [ ] **Step 6: 提交**

```bash
git add go.mod go.sum internal/hub/store/store.go internal/hub/store/schema.sql internal/hub/store/store_test.go
git commit -m "feat(hub): SQLite 存储打开、迁移与 settings"
```

---

### Task 3: 存储：机器、接入码、客户端会话

**Files:**
- Create: `internal/hub/store/machines.go`
- Create: `internal/hub/store/machines_test.go`

**Interfaces:**
- Produces（导出）:
  - `type Machine struct{ ID, Name, HomeDir, AgentVersion, EnrolledAt string; LastSeen *string; Revoked bool }`
  - `func NewID() string`（16 字节随机十六进制）、`func RandomToken() string`（32 字节 base64url）、`func HashToken(raw string) string`（SHA-256 十六进制）
  - `func (s *Store) CreateEnrollToken(ttl time.Duration) (string, error)`、`func (s *Store) ConsumeEnrollToken(raw string) error`（不存在、已用、过期都返回 `ErrInvalidToken`）
  - `func (s *Store) CreateMachine(name, rawSecret, homeDir string) (Machine, error)`、`func (s *Store) AuthMachine(rawSecret string) (Machine, error)`（已吊销或不存在返回 `ErrNotFound`）、`func (s *Store) TouchMachine(id, agentVersion, homeDir string) error`、`func (s *Store) GetMachine(id string) (Machine, error)`、`func (s *Store) ListMachines() ([]Machine, error)`、`func (s *Store) RevokeMachine(id string) error`
  - `func (s *Store) CreateClientSession(ttl time.Duration) (string, error)`、`func (s *Store) ValidateClientSession(raw string) (bool, error)`（顺带删过期行）、`func (s *Store) DeleteClientSession(raw string) error`
  - `var ErrInvalidToken = errors.New("接入码无效或已过期")`

- [ ] **Step 1: 写失败的测试**

`internal/hub/store/machines_test.go`:
```go
package store

import (
	"errors"
	"testing"
	"time"
)

func TestEnrollTokenLifecycle(t *testing.T) {
	s := openTemp(t)
	tok, err := s.CreateEnrollToken(10 * time.Minute)
	if err != nil || len(tok) < 40 {
		t.Fatalf("CreateEnrollToken = %q, %v", tok, err)
	}
	if err := s.ConsumeEnrollToken(tok); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeEnrollToken(tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("用过的接入码应失效: %v", err)
	}
	if err := s.ConsumeEnrollToken("nope"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("不存在的接入码: %v", err)
	}
	expired, _ := s.CreateEnrollToken(-time.Second)
	if err := s.ConsumeEnrollToken(expired); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("过期的接入码: %v", err)
	}
}

func TestMachineAuthAndRevoke(t *testing.T) {
	s := openTemp(t)
	secret := RandomToken()
	m, err := s.CreateMachine("mac-studio", secret, "/Users/u")
	if err != nil || m.ID == "" || m.Name != "mac-studio" {
		t.Fatalf("CreateMachine = %+v, %v", m, err)
	}
	got, err := s.AuthMachine(secret)
	if err != nil || got.ID != m.ID {
		t.Fatalf("AuthMachine = %+v, %v", got, err)
	}
	if _, err := s.AuthMachine("wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("错密钥应 ErrNotFound: %v", err)
	}
	if err := s.TouchMachine(m.ID, "0.1.0", "/Users/u"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListMachines()
	if len(list) != 1 || list[0].LastSeen == nil || list[0].AgentVersion != "0.1.0" {
		t.Fatalf("ListMachines = %+v", list)
	}
	if err := s.RevokeMachine(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthMachine(secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("吊销后应拒绝: %v", err)
	}
	if gm, _ := s.GetMachine(m.ID); !gm.Revoked {
		t.Fatal("GetMachine 应显示已吊销")
	}
}

func TestClientSessions(t *testing.T) {
	s := openTemp(t)
	tok, err := s.CreateClientSession(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ValidateClientSession(tok); !ok {
		t.Fatal("新会话应有效")
	}
	if ok, _ := s.ValidateClientSession("nope"); ok {
		t.Fatal("不存在的会话应无效")
	}
	old, _ := s.CreateClientSession(-time.Minute)
	if ok, _ := s.ValidateClientSession(old); ok {
		t.Fatal("过期会话应无效")
	}
	if err := s.DeleteClientSession(tok); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ValidateClientSession(tok); ok {
		t.Fatal("删除后应无效")
	}
}

func TestHashTokenIsStableAndHex(t *testing.T) {
	a, b := HashToken("x"), HashToken("x")
	if a != b || len(a) != 64 {
		t.Fatalf("HashToken = %q", a)
	}
	if len(NewID()) != 32 {
		t.Fatal("NewID 应为 32 个十六进制字符")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/store/ -run 'Enroll|Machine|ClientSessions|HashToken'`
Expected: FAIL，报 `undefined: CreateEnrollToken` 等。

- [ ] **Step 3: 写最小实现**

`internal/hub/store/machines.go`:
```go
package store

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"
)

// ErrInvalidToken 表示接入码不存在、已用或过期。
var ErrInvalidToken = errors.New("接入码无效或已过期")

// Machine 是一台接入的机器。
type Machine struct {
	ID           string
	Name         string
	HomeDir      string
	AgentVersion string
	EnrolledAt   string
	LastSeen     *string
	Revoked      bool
}

// NewID 生成 32 个十六进制字符的随机 id。
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("随机数不可用: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// RandomToken 生成 32 字节随机数的 base64url。
func RandomToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("随机数不可用: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// HashToken 返回 SHA-256 十六进制，数据库里只存哈希。
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// CreateEnrollToken 生成一次性接入码。
func (s *Store) CreateEnrollToken(ttl time.Duration) (string, error) {
	raw := RandomToken()
	_, err := s.db.Exec("INSERT INTO enroll_tokens(token_hash, expires_at) VALUES(?, ?)", HashToken(raw), fmtTime(time.Now().Add(ttl)))
	return raw, err
}

// ConsumeEnrollToken 校验并作废接入码。
func (s *Store) ConsumeEnrollToken(raw string) error {
	res, err := s.db.Exec("UPDATE enroll_tokens SET used = 1 WHERE token_hash = ? AND used = 0 AND expires_at > ?", HashToken(raw), Now())
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrInvalidToken
	}
	return nil
}

// CreateMachine 登记机器，存密钥哈希。
func (s *Store) CreateMachine(name, rawSecret, homeDir string) (Machine, error) {
	m := Machine{ID: NewID(), Name: name, HomeDir: homeDir, EnrolledAt: Now()}
	_, err := s.db.Exec("INSERT INTO machines(id, name, secret_hash, home_dir, enrolled_at) VALUES(?, ?, ?, ?, ?)", m.ID, m.Name, HashToken(rawSecret), homeDir, m.EnrolledAt)
	return m, err
}

const machineCols = "id, name, home_dir, agent_version, enrolled_at, last_seen, revoked"

func scanMachine(row interface{ Scan(...any) error }) (Machine, error) {
	var m Machine
	var revoked int
	err := row.Scan(&m.ID, &m.Name, &m.HomeDir, &m.AgentVersion, &m.EnrolledAt, &m.LastSeen, &revoked)
	m.Revoked = revoked != 0
	return m, err
}

// AuthMachine 按密钥找机器，已吊销视为不存在。
func (s *Store) AuthMachine(rawSecret string) (Machine, error) {
	m, err := scanMachine(s.db.QueryRow("SELECT "+machineCols+" FROM machines WHERE secret_hash = ? AND revoked = 0", HashToken(rawSecret)))
	if errors.Is(err, sql.ErrNoRows) {
		return Machine{}, ErrNotFound
	}
	return m, err
}

// GetMachine 按 id 取机器。
func (s *Store) GetMachine(id string) (Machine, error) {
	m, err := scanMachine(s.db.QueryRow("SELECT "+machineCols+" FROM machines WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Machine{}, ErrNotFound
	}
	return m, err
}

// TouchMachine 更新心跳、版本与家目录。
func (s *Store) TouchMachine(id, agentVersion, homeDir string) error {
	_, err := s.db.Exec("UPDATE machines SET last_seen = ?, agent_version = ?, home_dir = ? WHERE id = ?", Now(), agentVersion, homeDir, id)
	return err
}

// ListMachines 按接入时间列出全部机器。
func (s *Store) ListMachines() ([]Machine, error) {
	rows, err := s.db.Query("SELECT " + machineCols + " FROM machines ORDER BY enrolled_at")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Machine
	for rows.Next() {
		m, err := scanMachine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// RevokeMachine 吊销机器，其密钥立即失效。
func (s *Store) RevokeMachine(id string) error {
	_, err := s.db.Exec("UPDATE machines SET revoked = 1 WHERE id = ?", id)
	return err
}

// CreateClientSession 新建登录会话 token。
func (s *Store) CreateClientSession(ttl time.Duration) (string, error) {
	raw := RandomToken()
	_, err := s.db.Exec("INSERT INTO client_sessions(token_hash, created_at, expires_at) VALUES(?, ?, ?)", HashToken(raw), Now(), fmtTime(time.Now().Add(ttl)))
	return raw, err
}

// ValidateClientSession 判断 token 是否有效，顺手清理过期行。
func (s *Store) ValidateClientSession(raw string) (bool, error) {
	if _, err := s.db.Exec("DELETE FROM client_sessions WHERE expires_at <= ?", Now()); err != nil {
		return false, err
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM client_sessions WHERE token_hash = ?", HashToken(raw)).Scan(&n); err != nil {
		return false, err
	}
	return n == 1, nil
}

// DeleteClientSession 登出。
func (s *Store) DeleteClientSession(raw string) error {
	_, err := s.db.Exec("DELETE FROM client_sessions WHERE token_hash = ?", HashToken(raw))
	return err
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/store/ -v`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/store/machines.go internal/hub/store/machines_test.go
git commit -m "feat(hub): 机器登记、接入码与客户端会话存储"
```

---

### Task 4: 存储：请求与会话

**Files:**
- Create: `internal/hub/store/requests.go`
- Create: `internal/hub/store/requests_test.go`

**Interfaces:**
- Produces（导出）:
  - `type Request struct{ ID, MachineID, SessionID, Kind, ToolName string; Payload policy.Payload; PayloadHash, Risk, ProjectRoot, EngineReason, State, DecidedBy, DecisionNote, RuleID, TurnID, CLISessionID, Title, Callback, CreatedAt string; ExpiresAt, DecidedAt, DeliveredAt *string }`
  - `func PayloadHash(p policy.Payload) string`（JSON 的 SHA-256）
  - `func (s *Store) UpsertSession(machineID string, sess proto.Session, project string) (string, error)`：按 `(machine_id, tool, cli_session_id)` 唯一，返回会话行 id
  - `func (s *Store) InsertRequest(r Request) error`（`PayloadHash` 为空时自动算；`CreatedAt` 为空时用 `Now()`）
  - `func (s *Store) GetRequest(id string) (Request, error)`
  - `func (s *Store) ListRequests(state string, limit int) ([]Request, error)`（state 为空表示全部；按 created_at 倒序）
  - `func (s *Store) ListPendingForMachine(machineID string) ([]Request, error)`
  - `func (s *Store) FindResend(machineID, cliSessionID, turnID, toolName, payloadHash string, since time.Duration) (Request, error)`：同键、仍 pending、不早于 since 的最近一条
  - `func (s *Store) SetRequestState(id, state, decidedBy, note, ruleID string) error`：只允许从 `pending` 迁移；非 pending 返回 `ErrNotPending`；写 `decided_at`
  - `func (s *Store) MarkDelivered(id string) error`
  - `func (s *Store) UndeliveredDecisions(machineID string) ([]Request, error)`：`state in (approved, denied)` 且未送达
  - `func (s *Store) CountPending() (int, error)`
  - `var ErrNotPending = errors.New("请求已不是待处理状态")`

- [ ] **Step 1: 写失败的测试**

`internal/hub/store/requests_test.go`:
```go
package store

import (
	"errors"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func newReq(machine, id, cmd string) Request {
	return Request{ID: id, MachineID: machine, Kind: "permission", ToolName: "Bash",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: cmd, Cwd: "/p"},
		Risk: "outward", ProjectRoot: "/p", State: "pending", TurnID: "t1", CLISessionID: "s1"}
}

func TestSessionUpsert(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	id1, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"}, "proj")
	if err != nil || id1 == "" {
		t.Fatal(err)
	}
	id2, err := s.UpsertSession(m.ID, proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p/sub"}, "proj")
	if err != nil || id2 != id1 {
		t.Fatalf("同一会话应复用行: %s vs %s, %v", id1, id2, err)
	}
	id3, _ := s.UpsertSession(m.ID, proto.Session{Tool: "codex", CLISessionID: "s1"}, "proj")
	if id3 == id1 {
		t.Fatal("不同工具应是不同行")
	}
}

func TestRequestLifecycle(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	r := newReq(m.ID, "r1", "git push origin main")
	if err := s.InsertRequest(r); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRequest("r1")
	if err != nil || got.State != "pending" || got.Payload.Command != "git push origin main" || got.PayloadHash == "" || got.CreatedAt == "" {
		t.Fatalf("GetRequest = %+v, %v", got, err)
	}
	if n, _ := s.CountPending(); n != 1 {
		t.Fatalf("CountPending = %d", n)
	}
	pend, _ := s.ListPendingForMachine(m.ID)
	if len(pend) != 1 || pend[0].ID != "r1" {
		t.Fatalf("ListPendingForMachine = %+v", pend)
	}
	if err := s.SetRequestState("r1", "approved", "user", "ok", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRequestState("r1", "denied", "user", "", ""); !errors.Is(err, ErrNotPending) {
		t.Fatalf("二次决定应 ErrNotPending: %v", err)
	}
	got, _ = s.GetRequest("r1")
	if got.State != "approved" || got.DecidedBy != "user" || got.DecidedAt == nil || got.DeliveredAt != nil {
		t.Fatalf("决定后: %+v", got)
	}
	und, _ := s.UndeliveredDecisions(m.ID)
	if len(und) != 1 {
		t.Fatalf("未送达 = %+v", und)
	}
	if err := s.MarkDelivered("r1"); err != nil {
		t.Fatal(err)
	}
	if und, _ := s.UndeliveredDecisions(m.ID); len(und) != 0 {
		t.Fatal("送达后不应再列出")
	}
	all, _ := s.ListRequests("", 10)
	if len(all) != 1 || all[0].ID != "r1" {
		t.Fatalf("ListRequests = %+v", all)
	}
	if _, err := s.GetRequest("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
}

func TestFindResend(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	first := newReq(m.ID, "r1", "touch a")
	s.InsertRequest(first)
	got, err := s.FindResend(m.ID, "s1", "t1", "Bash", PayloadHash(first.Payload), 15*time.Minute)
	if err != nil || got.ID != "r1" {
		t.Fatalf("应找到同键的 pending: %+v, %v", got, err)
	}
	if _, err := s.FindResend(m.ID, "s1", "t2", "Bash", PayloadHash(first.Payload), 15*time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不同 turn 不算重发: %v", err)
	}
	s.SetRequestState("r1", "cancelled", "resend", "", "")
	if _, err := s.FindResend(m.ID, "s1", "t1", "Bash", PayloadHash(first.Payload), 15*time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("非 pending 不算: %v", err)
	}
}

func TestListRequestsOrderAndLimit(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	for i, id := range []string{"a", "b", "c"} {
		r := newReq(m.ID, id, "cmd")
		r.CreatedAt = time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		s.InsertRequest(r)
	}
	got, _ := s.ListRequests("pending", 2)
	if len(got) != 2 || got[0].ID != "c" || got[1].ID != "b" {
		t.Fatalf("应倒序取前两条: %+v", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/store/ -run 'Session|Request|Resend'`
Expected: FAIL，报 `undefined: Request`。

- [ ] **Step 3: 写最小实现**

`internal/hub/store/requests.go`:
```go
package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// ErrNotPending 表示请求已经不是待处理状态。
var ErrNotPending = errors.New("请求已不是待处理状态")

// Request 是 requests 表的一行。
type Request struct {
	ID           string
	MachineID    string
	SessionID    string
	Kind         string
	ToolName     string
	Payload      policy.Payload
	PayloadHash  string
	Risk         string
	ProjectRoot  string
	EngineReason string
	State        string
	DecidedBy    string
	DecisionNote string
	RuleID       string
	TurnID       string
	CLISessionID string
	Title        string
	Callback     string
	CreatedAt    string
	ExpiresAt    *string
	DecidedAt    *string
	DeliveredAt  *string
}

// PayloadHash 是载荷 JSON 的 SHA-256，用于去重。
func PayloadHash(p policy.Payload) string {
	b, _ := json.Marshal(p)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// UpsertSession 登记或刷新会话，返回会话行 id。
func (s *Store) UpsertSession(machineID string, sess proto.Session, project string) (string, error) {
	now := Now()
	var id string
	err := s.db.QueryRow(`INSERT INTO sessions(id, machine_id, tool, cli_session_id, cwd, project, started_at, last_event_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(machine_id, tool, cli_session_id) DO UPDATE SET cwd = excluded.cwd, project = excluded.project, last_event_at = excluded.last_event_at
		RETURNING id`, NewID(), machineID, sess.Tool, sess.CLISessionID, sess.Cwd, project, now, now).Scan(&id)
	return id, err
}

const requestCols = "id, machine_id, session_id, kind, tool_name, payload, payload_hash, risk, project_root, engine_reason, state, decided_by, decision_note, rule_id, turn_id, cli_session_id, title, callback, created_at, expires_at, decided_at, delivered_at"

// InsertRequest 插入一条请求。
func (s *Store) InsertRequest(r Request) error {
	if r.PayloadHash == "" {
		r.PayloadHash = PayloadHash(r.Payload)
	}
	if r.CreatedAt == "" {
		r.CreatedAt = Now()
	}
	payload, err := json.Marshal(r.Payload)
	if err != nil {
		return err
	}
	var sessionID any
	if r.SessionID != "" {
		sessionID = r.SessionID
	}
	_, err = s.db.Exec("INSERT INTO requests("+requestCols+") VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		r.ID, r.MachineID, sessionID, r.Kind, r.ToolName, string(payload), r.PayloadHash, r.Risk, r.ProjectRoot, r.EngineReason,
		r.State, r.DecidedBy, r.DecisionNote, r.RuleID, r.TurnID, r.CLISessionID, r.Title, r.Callback, r.CreatedAt, r.ExpiresAt, r.DecidedAt, r.DeliveredAt)
	return err
}

func scanRequest(row interface{ Scan(...any) error }) (Request, error) {
	var r Request
	var payload string
	var sessionID sql.NullString
	err := row.Scan(&r.ID, &r.MachineID, &sessionID, &r.Kind, &r.ToolName, &payload, &r.PayloadHash, &r.Risk, &r.ProjectRoot, &r.EngineReason,
		&r.State, &r.DecidedBy, &r.DecisionNote, &r.RuleID, &r.TurnID, &r.CLISessionID, &r.Title, &r.Callback, &r.CreatedAt, &r.ExpiresAt, &r.DecidedAt, &r.DeliveredAt)
	if err != nil {
		return Request{}, err
	}
	r.SessionID = sessionID.String
	if err := json.Unmarshal([]byte(payload), &r.Payload); err != nil {
		return Request{}, err
	}
	return r, nil
}

func (s *Store) queryRequests(query string, args ...any) ([]Request, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRequest 按 id 取请求。
func (s *Store) GetRequest(id string) (Request, error) {
	r, err := scanRequest(s.db.QueryRow("SELECT "+requestCols+" FROM requests WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	return r, err
}

// ListRequests 按创建时间倒序列出；state 为空表示全部。
func (s *Store) ListRequests(state string, limit int) ([]Request, error) {
	if state == "" {
		return s.queryRequests("SELECT "+requestCols+" FROM requests ORDER BY created_at DESC LIMIT ?", limit)
	}
	return s.queryRequests("SELECT "+requestCols+" FROM requests WHERE state = ? ORDER BY created_at DESC LIMIT ?", state, limit)
}

// ListPendingForMachine 列出某台机器的待处理请求。
func (s *Store) ListPendingForMachine(machineID string) ([]Request, error) {
	return s.queryRequests("SELECT "+requestCols+" FROM requests WHERE machine_id = ? AND state = 'pending' ORDER BY created_at", machineID)
}

// FindResend 找同一会话、同一回合、同一命令、仍 pending 且不早于 since 的最近一条。
func (s *Store) FindResend(machineID, cliSessionID, turnID, toolName, payloadHash string, since time.Duration) (Request, error) {
	cutoff := fmtTime(time.Now().Add(-since))
	r, err := scanRequest(s.db.QueryRow("SELECT "+requestCols+` FROM requests
		WHERE machine_id = ? AND cli_session_id = ? AND turn_id = ? AND tool_name = ? AND payload_hash = ? AND state = 'pending' AND created_at >= ?
		ORDER BY created_at DESC LIMIT 1`, machineID, cliSessionID, turnID, toolName, payloadHash, cutoff))
	if errors.Is(err, sql.ErrNoRows) {
		return Request{}, ErrNotFound
	}
	return r, err
}

// SetRequestState 把 pending 迁到终态；非 pending 返回 ErrNotPending。
func (s *Store) SetRequestState(id, state, decidedBy, note, ruleID string) error {
	res, err := s.db.Exec("UPDATE requests SET state = ?, decided_by = ?, decision_note = ?, rule_id = ?, decided_at = ? WHERE id = ? AND state = 'pending'",
		state, decidedBy, note, ruleID, Now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return ErrNotPending
	}
	return nil
}

// MarkDelivered 记录决定已送到守护进程。
func (s *Store) MarkDelivered(id string) error {
	_, err := s.db.Exec("UPDATE requests SET delivered_at = ? WHERE id = ? AND delivered_at IS NULL", Now(), id)
	return err
}

// UndeliveredDecisions 列出已决定但未送达的请求，重连后补发。
func (s *Store) UndeliveredDecisions(machineID string) ([]Request, error) {
	return s.queryRequests("SELECT "+requestCols+" FROM requests WHERE machine_id = ? AND state IN ('approved','denied') AND delivered_at IS NULL ORDER BY created_at", machineID)
}

// CountPending 统计待处理数。
func (s *Store) CountPending() (int, error) {
	var n int
	err := s.db.QueryRow("SELECT COUNT(*) FROM requests WHERE state = 'pending'").Scan(&n)
	return n, err
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/store/ -race -v`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/store/requests.go internal/hub/store/requests_test.go
git commit -m "feat(hub): 请求与会话存储，含重发查询与送达标记"
```

---

### Task 5: 存储：规则与审计事件

**Files:**
- Create: `internal/hub/store/rules.go`
- Create: `internal/hub/store/audit.go`
- Create: `internal/hub/store/rules_test.go`
- Create: `internal/hub/store/audit_test.go`

**Interfaces:**
- Produces（导出）:
  - `type RuleRow struct{ ID, YAML, Source string; Enabled bool; HitCount int; LastHitAt *string; CreatedAt, CreatedFrom string }`
  - `func (s *Store) RulesVersion() (int, error)`（settings 里的 `rules_version`，缺省 0）
  - `func (s *Store) EnsureDefaultRules() (int, error)`：表为空时导入 `policy.DefaultRuleSet()`，返回当前版本
  - `func (s *Store) ReplaceRules(rs policy.RuleSet) (int, error)`：整套替换（先 `Validate`），版本加一
  - `func (s *Store) InsertRule(r policy.Rule) (string, int, error)`：id 冲突时追加 `-2`、`-3`…；插入前把合并后的整套跑一次 `Validate`；返回实际 id 与新版本
  - `func (s *Store) SetRuleEnabled(id string, enabled bool) (int, error)`、`func (s *Store) DeleteRule(id string) (int, error)`（不存在返回 `ErrNotFound`）
  - `func (s *Store) ListRules() ([]RuleRow, error)`、`func (s *Store) RuleSet() (policy.RuleSet, int, error)`（把 `enabled` 列写进 `Rule.Enabled`）
  - `func (s *Store) RecordHit(id string) error`
  - `type AuditEvent struct{ ID int64; TS, MachineID, Actor, RequestID, Action, Detail string }`
  - `func (s *Store) InsertAudit(e AuditEvent) error`（`TS` 为空时用 `Now()`）、`func (s *Store) ListAudit(limit int, requestID string) ([]AuditEvent, error)`（requestID 为空表示全部，倒序）

- [ ] **Step 1: 写失败的测试**

`internal/hub/store/rules_test.go`:
```go
package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/policy"
)

func TestEnsureDefaultRulesAndRuleSet(t *testing.T) {
	s := openTemp(t)
	v, err := s.EnsureDefaultRules()
	if err != nil || v != 1 {
		t.Fatalf("EnsureDefaultRules = %d, %v", v, err)
	}
	if v2, _ := s.EnsureDefaultRules(); v2 != 1 {
		t.Fatalf("已有规则时不应重复导入: %d", v2)
	}
	rs, v3, err := s.RuleSet()
	if err != nil || v3 != 1 || len(rs.Rules) != 2 {
		t.Fatalf("RuleSet = %d 条, v=%d, %v", len(rs.Rules), v3, err)
	}
	if _, err := policy.NewEngine(rs); err != nil {
		t.Fatalf("导出的规则集应能建引擎: %v", err)
	}
}

func TestInsertToggleDeleteRules(t *testing.T) {
	s := openTemp(t)
	s.EnsureDefaultRules()
	r := policy.Rule{ID: "builtin-readonly", Source: policy.SourceUser, Match: policy.Match{Payload: policy.PayloadShell, CommandPrefix: "git push origin"}, Action: policy.ActionAllow}
	id, v, err := s.InsertRule(r)
	if err != nil || id != "builtin-readonly-2" || v != 2 {
		t.Fatalf("InsertRule = %q v=%d %v", id, v, err)
	}
	v, err = s.SetRuleEnabled(id, false)
	if err != nil || v != 3 {
		t.Fatalf("SetRuleEnabled v=%d %v", v, err)
	}
	rs, _, _ := s.RuleSet()
	var found *policy.Rule
	for i := range rs.Rules {
		if rs.Rules[i].ID == id {
			found = &rs.Rules[i]
		}
	}
	if found == nil || found.IsEnabled() {
		t.Fatalf("禁用应写进 Enabled: %+v", found)
	}
	rows, _ := s.ListRules()
	if len(rows) != 3 || rows[2].Source != "user" || rows[2].Enabled {
		t.Fatalf("ListRules = %+v", rows)
	}
	if err := s.RecordHit(id); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListRules()
	if rows[2].HitCount != 1 || rows[2].LastHitAt == nil {
		t.Fatalf("RecordHit 未生效: %+v", rows[2])
	}
	if v, err := s.DeleteRule(id); err != nil || v != 4 {
		t.Fatalf("DeleteRule v=%d %v", v, err)
	}
	if _, err := s.DeleteRule(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删不存在的: %v", err)
	}
}

func TestInsertRuleRejectsInvalid(t *testing.T) {
	s := openTemp(t)
	s.EnsureDefaultRules()
	bad := policy.Rule{ID: "learned-x", Source: policy.SourceLearned, Match: policy.Match{Payload: policy.PayloadShell, CommandPrefix: "git push"}, Action: policy.ActionAllow}
	if _, _, err := s.InsertRule(bad); err == nil || !strings.Contains(err.Error(), "learned") {
		t.Fatalf("不合法的 learned 规则应被拒: %v", err)
	}
	if rows, _ := s.ListRules(); len(rows) != 2 {
		t.Fatal("被拒的规则不应入库")
	}
}

func TestReplaceRules(t *testing.T) {
	s := openTemp(t)
	s.EnsureDefaultRules()
	rs, err := policy.ParseRuleSet([]byte("version: 1\nrules:\n  - id: only\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.ReplaceRules(rs)
	if err != nil || v != 2 {
		t.Fatalf("ReplaceRules v=%d %v", v, err)
	}
	got, _, _ := s.RuleSet()
	if len(got.Rules) != 1 || got.Rules[0].ID != "only" {
		t.Fatalf("替换后 = %+v", got.Rules)
	}
}
```

`internal/hub/store/audit_test.go`:
```go
package store

import "testing"

func TestAuditInsertAndList(t *testing.T) {
	s := openTemp(t)
	for _, a := range []string{"approve", "deny", "rules_import"} {
		if err := s.InsertAudit(AuditEvent{MachineID: "m1", Actor: "user", RequestID: "r1", Action: a, Detail: `{"x":1}`}); err != nil {
			t.Fatal(err)
		}
	}
	s.InsertAudit(AuditEvent{Actor: "system", Action: "other"})
	all, err := s.ListAudit(10, "")
	if err != nil || len(all) != 4 || all[0].Action != "other" || all[0].TS == "" {
		t.Fatalf("ListAudit = %+v, %v", all, err)
	}
	byReq, _ := s.ListAudit(10, "r1")
	if len(byReq) != 3 {
		t.Fatalf("按请求过滤 = %d", len(byReq))
	}
	two, _ := s.ListAudit(2, "")
	if len(two) != 2 {
		t.Fatalf("limit = %d", len(two))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/store/ -run 'Rules|Rule|Audit'`
Expected: FAIL，报 `undefined: EnsureDefaultRules` 等。

- [ ] **Step 3: 写最小实现**

`internal/hub/store/rules.go`:
```go
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/xufei547/railse-opc/internal/policy"
)

// RuleRow 是 rules 表的一行。
type RuleRow struct {
	ID          string
	YAML        string
	Source      string
	Enabled     bool
	HitCount    int
	LastHitAt   *string
	CreatedAt   string
	CreatedFrom string
}

// RulesVersion 返回规则版本号，没有时为 0。
func (s *Store) RulesVersion() (int, error) {
	v, ok, err := s.GetSetting("rules_version")
	if err != nil || !ok {
		return 0, err
	}
	return strconv.Atoi(v)
}

func bumpVersion(tx *sql.Tx) (int, error) {
	var cur string
	err := tx.QueryRow("SELECT value FROM settings WHERE key = 'rules_version'").Scan(&cur)
	n := 0
	if err == nil {
		n, _ = strconv.Atoi(cur)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	n++
	_, err = tx.Exec("INSERT INTO settings(key, value) VALUES('rules_version', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", strconv.Itoa(n))
	return n, err
}

// EnsureDefaultRules 在表为空时导入内置规则包。
func (s *Store) EnsureDefaultRules() (int, error) {
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM rules").Scan(&n); err != nil {
		return 0, err
	}
	if n > 0 {
		return s.RulesVersion()
	}
	return s.ReplaceRules(policy.DefaultRuleSet())
}

func insertRuleTx(tx *sql.Tx, r policy.Rule) error {
	b, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	enabled := 1
	if !r.IsEnabled() {
		enabled = 0
	}
	_, err = tx.Exec("INSERT INTO rules(id, yaml, source, enabled, created_at, created_from) VALUES(?, ?, ?, ?, ?, ?)",
		r.ID, string(b), string(r.Source), enabled, Now(), r.CreatedFrom)
	return err
}

// ReplaceRules 用一整套规则替换现有规则，先校验。
func (s *Store) ReplaceRules(rs policy.RuleSet) (int, error) {
	if err := rs.Validate(); err != nil {
		return 0, err
	}
	var version int
	err := s.Tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM rules"); err != nil {
			return err
		}
		for _, r := range rs.Rules {
			if err := insertRuleTx(tx, r); err != nil {
				return err
			}
		}
		v, err := bumpVersion(tx)
		version = v
		return err
	})
	return version, err
}

// InsertRule 加一条规则。id 冲突时追加 -2、-3。合并后的整套先校验再入库。
func (s *Store) InsertRule(r policy.Rule) (string, int, error) {
	rs, _, err := s.RuleSet()
	if err != nil {
		return "", 0, err
	}
	taken := map[string]bool{}
	for _, x := range rs.Rules {
		taken[x.ID] = true
	}
	id := r.ID
	for i := 2; taken[id]; i++ {
		id = fmt.Sprintf("%s-%d", r.ID, i)
	}
	r.ID = id
	rs.Rules = append(rs.Rules, r)
	if err := rs.Validate(); err != nil {
		return "", 0, err
	}
	var version int
	err = s.Tx(func(tx *sql.Tx) error {
		if err := insertRuleTx(tx, r); err != nil {
			return err
		}
		v, err := bumpVersion(tx)
		version = v
		return err
	})
	return id, version, err
}

// SetRuleEnabled 启停一条规则。
func (s *Store) SetRuleEnabled(id string, enabled bool) (int, error) {
	var version int
	err := s.Tx(func(tx *sql.Tx) error {
		e := 0
		if enabled {
			e = 1
		}
		res, err := tx.Exec("UPDATE rules SET enabled = ? WHERE id = ?", e, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		v, err := bumpVersion(tx)
		version = v
		return err
	})
	return version, err
}

// DeleteRule 删一条规则。
func (s *Store) DeleteRule(id string) (int, error) {
	var version int
	err := s.Tx(func(tx *sql.Tx) error {
		res, err := tx.Exec("DELETE FROM rules WHERE id = ?", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrNotFound
		}
		v, err := bumpVersion(tx)
		version = v
		return err
	})
	return version, err
}

// ListRules 按创建时间列出规则行。
func (s *Store) ListRules() ([]RuleRow, error) {
	rows, err := s.db.Query("SELECT id, yaml, source, enabled, hit_count, last_hit_at, created_at, created_from FROM rules ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleRow
	for rows.Next() {
		var r RuleRow
		var enabled int
		if err := rows.Scan(&r.ID, &r.YAML, &r.Source, &enabled, &r.HitCount, &r.LastHitAt, &r.CreatedAt, &r.CreatedFrom); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// RuleSet 把全部规则组装成 policy.RuleSet，enabled 列写进 Rule.Enabled。
func (s *Store) RuleSet() (policy.RuleSet, int, error) {
	rows, err := s.ListRules()
	if err != nil {
		return policy.RuleSet{}, 0, err
	}
	rs := policy.RuleSet{Version: policy.RuleSetVersion}
	for _, row := range rows {
		var r policy.Rule
		if err := yaml.Unmarshal([]byte(row.YAML), &r); err != nil {
			return policy.RuleSet{}, 0, fmt.Errorf("规则 %s 的 YAML 损坏: %w", row.ID, err)
		}
		enabled := row.Enabled
		r.Enabled = &enabled
		rs.Rules = append(rs.Rules, r)
	}
	v, err := s.RulesVersion()
	return rs, v, err
}

// RecordHit 记一次命中。
func (s *Store) RecordHit(id string) error {
	_, err := s.db.Exec("UPDATE rules SET hit_count = hit_count + 1, last_hit_at = ? WHERE id = ?", Now(), id)
	return err
}
```

`internal/hub/store/audit.go`:
```go
package store

// AuditEvent 是 audit_events 表的一行，只追加。
type AuditEvent struct {
	ID        int64
	TS        string
	MachineID string
	Actor     string
	RequestID string
	Action    string
	Detail    string
}

// InsertAudit 追加一条审计。
func (s *Store) InsertAudit(e AuditEvent) error {
	if e.TS == "" {
		e.TS = Now()
	}
	_, err := s.db.Exec("INSERT INTO audit_events(ts, machine_id, actor, request_id, action, detail) VALUES(?, ?, ?, ?, ?, ?)",
		e.TS, e.MachineID, e.Actor, e.RequestID, e.Action, e.Detail)
	return err
}

// ListAudit 倒序列出审计，requestID 为空表示全部。
func (s *Store) ListAudit(limit int, requestID string) ([]AuditEvent, error) {
	q := "SELECT id, ts, machine_id, actor, request_id, action, detail FROM audit_events"
	var args []any
	if requestID != "" {
		q += " WHERE request_id = ?"
		args = append(args, requestID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var e AuditEvent
		if err := rows.Scan(&e.ID, &e.TS, &e.MachineID, &e.Actor, &e.RequestID, &e.Action, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/store/ -race -v`
Expected: 全部 PASS。`TestInsertToggleDeleteRules` 里 `ListRules` 的顺序按 `created_at, id`，内置两条先入库，所以新规则在第三位。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/store/rules.go internal/hub/store/audit.go internal/hub/store/rules_test.go internal/hub/store/audit_test.go
git commit -m "feat(hub): 规则与审计事件存储"
```

---

### Task 6: hub 骨架与认证：密码、登录会话、接入

**Files:**
- Create: `internal/hub/server.go`
- Create: `internal/hub/auth.go`
- Create: `internal/hub/auth_test.go`

**Interfaces:**
- Produces（导出，包 `hub`）:
  - `type Config struct{ Dir, DBPath, Listen string; SecureCookies bool; SessionTTL, PingInterval, PongTimeout, ResendWindow time.Duration }`，`func DefaultConfig(dir string) Config`（`DBPath` 为 `dir/hub.db`，`Listen` 为 `127.0.0.1:8787`，TTL 30 天，ping 30 秒，pong 90 秒，重发窗口 15 分钟）
  - `type Server struct`，`func New(cfg Config, st *store.Store, logger *log.Logger) (*Server, error)`（调 `EnsureDefaultRules`，建路由），`func (s *Server) Handler() http.Handler`，`func (s *Server) Run(ctx context.Context) error`
  - `func HashPassword(pw string) (string, error)`、`func VerifyPassword(encoded, pw string) bool`（argon2id，编码 `argon2id$v=19$m=65536,t=1,p=4$<salt>$<hash>`，base64 raw std）
  - `func (s *Server) SetPassword(pw string) error`
  - 路由：`POST /api/login`（`{"password":"…"}`，成功 204 并设 cookie；未设密码 503；错误 401；限速 429）、`POST /api/logout`（204，清 cookie）、`POST /api/enroll`（`{"token":"…","name":"…","home_dir":"…"}` → 200 `{"machine_id":"…","secret":"…"}`；坏 token 401）
  - 包内：`requireClient(next http.HandlerFunc) http.HandlerFunc`、`authMachine(r *http.Request) (store.Machine, error)`、`type limiter`、`writeJSON`、`writeErr`、`clientIP`

- [ ] **Step 1: 写失败的测试**

`internal/hub/auth_test.go`:
```go
package hub

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/hub/store"
)

type testHub struct {
	srv   *Server
	st    *store.Store
	http  *httptest.Server
	dir   string
}

func newTestHub(t *testing.T) *testHub {
	t.Helper()
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig(dir)
	srv, err := New(cfg, st, log.New(os.Stderr, "hub ", 0))
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hs.Close(); st.Close() })
	return &testHub{srv: srv, st: st, http: hs, dir: dir}
}

func postJSON(t *testing.T, url string, body any, cookie *http.Cookie) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func login(t *testing.T, h *testHub, pw string) *http.Cookie {
	t.Helper()
	resp := postJSON(t, h.http.URL+"/api/login", map[string]string{"password": pw}, nil)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "railse_session" {
			return c
		}
	}
	t.Fatal("没有 railse_session cookie")
	return nil
}

func TestPasswordHashRoundTrip(t *testing.T) {
	enc, err := HashPassword("hunter2")
	if err != nil || !VerifyPassword(enc, "hunter2") || VerifyPassword(enc, "hunter3") || VerifyPassword("garbage", "hunter2") {
		t.Fatalf("enc=%s err=%v", enc, err)
	}
}

func TestLoginFlow(t *testing.T) {
	h := newTestHub(t)
	if resp := postJSON(t, h.http.URL+"/api/login", map[string]string{"password": "x"}, nil); resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("未设密码应 503, 得到 %d", resp.StatusCode)
	}
	if err := h.srv.SetPassword("hunter2"); err != nil {
		t.Fatal(err)
	}
	if resp := postJSON(t, h.http.URL+"/api/login", map[string]string{"password": "wrong"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错密码应 401, 得到 %d", resp.StatusCode)
	}
	c := login(t, h, "hunter2")
	if !c.HttpOnly || c.Path != "/" || c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("cookie 属性: %+v", c)
	}
	// 受保护接口
	req, _ := http.NewRequest("GET", h.http.URL+"/api/status", nil)
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 cookie 应 401, 得到 %d", resp.StatusCode)
	}
	req.AddCookie(c)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("有 cookie 应 200, 得到 %d", resp.StatusCode)
	}
	// 登出
	if resp := postJSON(t, h.http.URL+"/api/logout", nil, c); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	req, _ = http.NewRequest("GET", h.http.URL+"/api/status", nil)
	req.AddCookie(c)
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("登出后应 401, 得到 %d", resp.StatusCode)
	}
}

func TestLoginRateLimit(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter2")
	var last int
	for i := 0; i < 6; i++ {
		last = postJSON(t, h.http.URL+"/api/login", map[string]string{"password": "wrong"}, nil).StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("第 6 次应 429, 得到 %d", last)
	}
}

func TestEnroll(t *testing.T) {
	h := newTestHub(t)
	if resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": "bad", "name": "mac"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("坏 token 应 401, 得到 %d", resp.StatusCode)
	}
	tok, _ := h.st.CreateEnrollToken(10 * time.Minute)
	resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": tok, "name": "mac", "home_dir": "/Users/u"}, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enroll = %d", resp.StatusCode)
	}
	var out struct{ MachineID, Secret string }
	json.NewDecoder(resp.Body).Decode(&out)
	if out.MachineID == "" || out.Secret == "" {
		t.Fatalf("enroll 返回 %+v", out)
	}
	req, _ := http.NewRequest("GET", "/ws/agent", nil)
	req.Header.Set("Authorization", "Bearer "+out.Secret)
	if m, err := h.srv.authMachine(req); err != nil || m.ID != out.MachineID || m.HomeDir != "/Users/u" {
		t.Fatalf("authMachine = %+v, %v", m, err)
	}
	req.Header.Set("Authorization", "Bearer nope")
	if _, err := h.srv.authMachine(req); err == nil {
		t.Fatal("错密钥应失败")
	}
	if resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": tok, "name": "mac"}, nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatal("接入码只能用一次")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/`
Expected: FAIL，报 `undefined: DefaultConfig` 等。

- [ ] **Step 3: 写骨架**

`internal/hub/server.go`:
```go
// Package hub 是跑在云服务器上的中心服务：存请求、规则、机器与审计，接守护进程的 WebSocket，
// 给手机网页和 webhook 来源提供 HTTP 接口。hub 只能对请求回 allow 或 deny，没有向机器下发命令的通道。
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/xufei547/railse-opc/internal/hub/store"
)

// Config 是 hub 的配置。
type Config struct {
	Dir           string
	DBPath        string
	Listen        string
	SecureCookies bool
	SessionTTL    time.Duration
	PingInterval  time.Duration
	PongTimeout   time.Duration
	ResendWindow  time.Duration
}

// DefaultConfig 给出目录下的默认配置。
func DefaultConfig(dir string) Config {
	return Config{
		Dir: dir, DBPath: filepath.Join(dir, "hub.db"), Listen: "127.0.0.1:8787",
		SessionTTL: 30 * 24 * time.Hour, PingInterval: 30 * time.Second, PongTimeout: 90 * time.Second, ResendWindow: 15 * time.Minute,
	}
}

// Server 组装存储、连接表与路由。
type Server struct {
	cfg     Config
	store   *store.Store
	log     *log.Logger
	mux     *http.ServeMux
	limiter *limiter
	agents  *agentHub
	clients *clientHub
	start   time.Time
}

// New 组装 hub：确保有内置规则，注册路由。
func New(cfg Config, st *store.Store, logger *log.Logger) (*Server, error) {
	if _, err := st.EnsureDefaultRules(); err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, store: st, log: logger, mux: http.NewServeMux(), limiter: newLimiter(), agents: newAgentHub(), clients: newClientHub(), start: time.Now()}
	s.routes()
	return s, nil
}

// routes 注册全部路由；后面的 Task 往里加。
func (s *Server) routes() {
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)
	s.mux.HandleFunc("POST /api/enroll", s.handleEnroll)
	s.mux.HandleFunc("GET /api/status", s.requireClient(s.handleStatus))
	s.mux.HandleFunc("GET /ws/agent", s.handleAgentWS)
}

// Handler 返回根处理器。
func (s *Server) Handler() http.Handler { return s.mux }

// Run 监听直到 ctx 取消。
func (s *Server) Run(ctx context.Context) error {
	hs := &http.Server{Addr: s.cfg.Listen, Handler: s.mux, ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	s.log.Printf("hub 监听 %s", ln.Addr())
	errCh := make(chan error, 1)
	go func() { errCh <- hs.Serve(ln) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.agents.closeAll()
		return hs.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// handleStatus 在 Task 9 扩充；这里先给认证测试一个受保护的端点。
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	pending, _ := s.store.CountPending()
	version, _ := s.store.RulesVersion()
	writeJSON(w, http.StatusOK, map[string]any{"pending": pending, "rules_version": version, "uptime": time.Since(s.start).Round(time.Second).String()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
```

`agentHub`、`clientHub` 在 Task 7、8 里定义；本 Task 先在 `server.go` 末尾放两个最小占位，Task 7、8 替换：
```go
// agentHub 与 clientHub 的完整实现见 Task 7、8。
type agentHub struct{}

func newAgentHub() *agentHub         { return &agentHub{} }
func (h *agentHub) closeAll()        {}
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	writeErr(w, http.StatusNotImplemented, "未实现")
}

type clientHub struct{}

func newClientHub() *clientHub { return &clientHub{} }
```

- [ ] **Step 4: 写认证**

`internal/hub/auth.go`:
```go
package hub

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/xufei547/railse-opc/internal/hub/store"
)

const (
	cookieName    = "railse_session"
	argonTime     = 1
	argonMemory   = 64 * 1024
	argonThreads  = 4
	argonKeyLen   = 32
	loginMax      = 5
	loginWindow   = time.Minute
	enrollTokenTTL = 10 * time.Minute
)

// HashPassword 用 argon2id 哈希密码。
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword 校验密码，格式错误一律 false。
func VerifyPassword(encoded, pw string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 5 || parts[0] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[2], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// SetPassword 设置或更换网页登录密码。
func (s *Server) SetPassword(pw string) error {
	if len(pw) < 8 {
		return errors.New("密码至少 8 个字符")
	}
	enc, err := HashPassword(pw)
	if err != nil {
		return err
	}
	return s.store.SetSetting("password_hash", enc)
}

// limiter 是按键的滑动窗口计数器。
type limiter struct {
	mu   sync.Mutex
	hits map[string][]time.Time
}

func newLimiter() *limiter { return &limiter{hits: map[string][]time.Time{}} }

func (l *limiter) allow(key string, max int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	var kept []time.Time
	for _, t := range l.hits[key] {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	if len(kept) >= max {
		l.hits[key] = kept
		return false
	}
	l.hits[key] = append(kept, now)
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("login:"+clientIP(r), loginMax, loginWindow) {
		writeErr(w, http.StatusTooManyRequests, "尝试太频繁，一分钟后再试")
		return
	}
	enc, ok, err := s.store.GetSetting("password_hash")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "还没有设置密码，请在服务器上运行 railse hub password")
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Password == "" {
		writeErr(w, http.StatusBadRequest, "需要 password")
		return
	}
	if !VerifyPassword(enc, body.Password) {
		writeErr(w, http.StatusUnauthorized, "密码错误")
		return
	}
	tok, err := s.store.CreateClientSession(s.cfg.SessionTTL)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: tok, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.cfg.SecureCookies || r.TLS != nil, MaxAge: int(s.cfg.SessionTTL.Seconds())})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		s.store.DeleteClientSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", HttpOnly: true, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// requireClient 要求有效的登录 cookie。
func (s *Server) requireClient(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "请先登录")
			return
		}
		ok, err := s.store.ValidateClientSession(c.Value)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !ok {
			writeErr(w, http.StatusUnauthorized, "登录已过期")
			return
		}
		next(w, r)
	}
}

func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	if !s.limiter.allow("enroll:"+clientIP(r), loginMax, loginWindow) {
		writeErr(w, http.StatusTooManyRequests, "尝试太频繁")
		return
	}
	var body struct {
		Token   string `json:"token"`
		Name    string `json:"name"`
		HomeDir string `json:"home_dir"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Token == "" || body.Name == "" {
		writeErr(w, http.StatusBadRequest, "需要 token 与 name")
		return
	}
	if err := s.store.ConsumeEnrollToken(body.Token); err != nil {
		writeErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	secret := store.RandomToken()
	m, err := s.store.CreateMachine(body.Name, secret, body.HomeDir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.store.InsertAudit(store.AuditEvent{MachineID: m.ID, Actor: "system", Action: "machine_enrolled", Detail: `{"name":` + fmt.Sprintf("%q", m.Name) + `}`})
	writeJSON(w, http.StatusOK, map[string]string{"machine_id": m.ID, "secret": secret})
}

// authMachine 从 Authorization: Bearer 取机器密钥。
func (s *Server) authMachine(r *http.Request) (store.Machine, error) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return store.Machine{}, errors.New("缺少 Bearer 密钥")
	}
	return s.store.AuthMachine(strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")))
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/ -race -v`
Expected: 4 个测试 PASS。`TestLoginRateLimit` 里前 5 次是 401，第 6 次 429。

- [ ] **Step 6: 提交**

```bash
git add internal/hub/server.go internal/hub/auth.go internal/hub/auth_test.go go.mod go.sum
git commit -m "feat(hub): 服务骨架、argon2id 密码、登录会话、接入码换密钥"
```

---

### Task 7: 守护进程的 WebSocket 连接

**Files:**
- Create: `internal/hub/agents.go`
- Modify: `internal/hub/server.go`（删除 `agentHub`、`handleAgentWS` 占位）
- Create: `internal/hub/agents_test.go`

**Interfaces:**
- Consumes: `wire`（Task 1）、`store`、`authMachine`
- Produces（包内）:
  - `type agentConn struct{ machine store.Machine; conn *websocket.Conn; out chan []byte; closed chan struct{} }`
  - `type agentHub struct`：`newAgentHub()`、`register(c *agentConn)`（同一机器已有连接则关旧的）、`unregister(c)`、`send(machineID string, frame any) bool`、`connected(machineID string) bool`、`connectedIDs() []string`、`closeAll()`
  - `func (s *Server) handleAgentWS(w, r)`：认证 → Accept（非浏览器，跳过 Origin 检查）→ 5 秒内读 `hello` → `TouchMachine` → 登记 → 发 `welcome`（规则快照与暂停状态）→ `s.onAgentHello`（Task 8）→ 读循环分发到 `s.onPending/onCancel/onExpired/onDelivered/onAudit`（Task 8 实现，本 Task 先放占位）→ 每 `PingInterval` 发 ping，`PongTimeout` 无 pong 断开
  - hub 的全局暂停存在 settings `paused`（`"1"`/`"0"`），`welcome.Paused` 由此而来

- [ ] **Step 1: 写失败的测试**

`internal/hub/agents_test.go`:
```go
package hub

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/xufei547/railse-opc/internal/hub/wire"
)

type agentClient struct {
	conn *websocket.Conn
	t    *testing.T
}

func enrollMachine(t *testing.T, h *testHub, name string) (string, string) {
	t.Helper()
	tok, _ := h.st.CreateEnrollToken(10 * time.Minute)
	resp := postJSON(t, h.http.URL+"/api/enroll", map[string]string{"token": tok, "name": name, "home_dir": "/home/u"}, nil)
	var out struct {
		MachineID string `json:"machine_id"`
		Secret    string `json:"secret"`
	}
	decodeBody(t, resp, &out)
	return out.MachineID, out.Secret
}

func dialAgent(t *testing.T, h *testHub, secret string, hello wire.Hello) *agentClient {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws/agent"
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
	if err != nil {
		code := 0
		if resp != nil {
			code = resp.StatusCode
		}
		t.Fatalf("dial: %v (http %d)", err, code)
	}
	conn.SetReadLimit(wire.MaxFrame)
	c := &agentClient{conn: conn, t: t}
	hello.T = wire.THello
	c.send(hello)
	return c
}

func (c *agentClient) send(v any) {
	c.t.Helper()
	b, _ := wire.Marshal(v)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, b); err != nil {
		c.t.Fatalf("write: %v", err)
	}
}

// recv 读下一帧，跳过 ping（并回 pong）。
func (c *agentClient) recv(timeout time.Duration) (string, []byte) {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		_, b, err := c.conn.Read(ctx)
		if err != nil {
			c.t.Fatalf("read: %v", err)
		}
		kind, _ := wire.Kind(b)
		if kind == wire.TPing {
			c.send(wire.Pong{T: wire.TPong})
			continue
		}
		return kind, b
	}
}

func (c *agentClient) close() { c.conn.Close(websocket.StatusNormalClosure, "bye") }

func TestAgentWSRejectsBadSecret(t *testing.T) {
	h := newTestHub(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws/agent"
	_, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer nope"}}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("应 401: %v %v", err, resp)
	}
}

func TestAgentWSHelloWelcome(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, AgentVersion: "0.1.0", HomeDir: "/home/u"})
	defer c.close()
	kind, b := c.recv(5 * time.Second)
	if kind != wire.TWelcome {
		t.Fatalf("第一帧应是 welcome, 得到 %s", kind)
	}
	var w wire.Welcome
	wire.Unmarshal(b, &w)
	if w.RulesVersion != 1 || !strings.Contains(w.RulesYAML, "builtin-readonly") || w.Paused {
		t.Fatalf("welcome = %+v", w)
	}
	if !h.srv.agents.connected(id) {
		t.Fatal("应登记为已连接")
	}
	m, _ := h.st.GetMachine(id)
	if m.LastSeen == nil || m.AgentVersion != "0.1.0" {
		t.Fatalf("hello 应刷新机器: %+v", m)
	}
}

func TestAgentWSReplacesOldConnection(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	old := dialAgent(t, h, secret, wire.Hello{MachineID: id})
	old.recv(5 * time.Second)
	neu := dialAgent(t, h, secret, wire.Hello{MachineID: id})
	defer neu.close()
	neu.recv(5 * time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := old.conn.Read(ctx); err == nil {
		t.Fatal("旧连接应被关闭")
	}
	if !h.srv.agents.connected(id) {
		t.Fatal("新连接应仍登记")
	}
}

func TestAgentWSPingPong(t *testing.T) {
	h := newTestHub(t)
	h.srv.cfg.PingInterval = 100 * time.Millisecond
	h.srv.cfg.PongTimeout = 400 * time.Millisecond
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id})
	c.recv(2 * time.Second) // welcome
	// 不回 pong，应在 PongTimeout 后被断开
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_, b, err := c.conn.Read(ctx)
		if err != nil {
			if h.srv.agents.connected(id) {
				t.Fatal("断开后应注销")
			}
			return
		}
		if k, _ := wire.Kind(b); k != wire.TPing {
			t.Fatalf("只应收到 ping, 得到 %s", k)
		}
	}
	t.Fatal("不回 pong 也没被断开")
}

func TestAgentWSHelloTimeout(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	h.srv.cfg.PongTimeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws/agent"
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + secret}}})
	if err != nil {
		t.Fatal(err)
	}
	// 不发 hello
	if _, _, err := conn.Read(ctx); err == nil {
		t.Fatal("不发 hello 应在 5 秒内被断开")
	}
	if h.srv.agents.connected(id) {
		t.Fatal("没 hello 不应登记")
	}
}
```
`decodeBody` 放进 `auth_test.go`：
```go
func decodeBody(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("解析响应: %v", err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/ -run AgentWS`
Expected: FAIL（`/ws/agent` 返回 501，`connected` 未定义）。

- [ ] **Step 3: 写实现**

`internal/hub/agents.go`（替换 `server.go` 里的 `agentHub` 占位与 `handleAgentWS`）:
```go
package hub

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
)

// agentConn 是一台机器的活动连接。
type agentConn struct {
	machine store.Machine
	conn    *websocket.Conn
	out     chan []byte
	closed  chan struct{}
	once    sync.Once
}

func (c *agentConn) close() {
	c.once.Do(func() {
		close(c.closed)
		c.conn.Close(websocket.StatusNormalClosure, "closed")
	})
}

// agentHub 按机器 id 保存连接，每台机器只保留最新的一条。
type agentHub struct {
	mu    sync.Mutex
	conns map[string]*agentConn
}

func newAgentHub() *agentHub { return &agentHub{conns: map[string]*agentConn{}} }

func (h *agentHub) register(c *agentConn) {
	h.mu.Lock()
	old := h.conns[c.machine.ID]
	h.conns[c.machine.ID] = c
	h.mu.Unlock()
	if old != nil {
		old.close()
	}
}

func (h *agentHub) unregister(c *agentConn) {
	h.mu.Lock()
	if h.conns[c.machine.ID] == c {
		delete(h.conns, c.machine.ID)
	}
	h.mu.Unlock()
	c.close()
}

// send 把帧排进机器的发送队列；未连接或队列满返回 false。
func (h *agentHub) send(machineID string, frame any) bool {
	b, err := wire.Marshal(frame)
	if err != nil {
		return false
	}
	h.mu.Lock()
	c := h.conns[machineID]
	h.mu.Unlock()
	if c == nil {
		return false
	}
	select {
	case c.out <- b:
		return true
	default:
		c.close()
		return false
	}
}

func (h *agentHub) connected(machineID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[machineID]
	return ok
}

func (h *agentHub) connectedIDs() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	ids := make([]string, 0, len(h.conns))
	for id := range h.conns {
		ids = append(ids, id)
	}
	return ids
}

func (h *agentHub) closeAll() {
	h.mu.Lock()
	conns := make([]*agentConn, 0, len(h.conns))
	for _, c := range h.conns {
		conns = append(conns, c)
	}
	h.conns = map[string]*agentConn{}
	h.mu.Unlock()
	for _, c := range conns {
		c.close()
	}
}

// handleAgentWS 是守护进程的入口。
func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	machine, err := s.authMachine(r)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "机器密钥无效")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	conn.SetReadLimit(wire.MaxFrame)
	helloCtx, cancelHello := context.WithTimeout(r.Context(), 5*time.Second)
	_, first, err := conn.Read(helloCtx)
	cancelHello()
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "没有收到 hello")
		return
	}
	var hello wire.Hello
	if k, _ := wire.Kind(first); k != wire.THello || wire.Unmarshal(first, &hello) != nil || hello.MachineID != machine.ID {
		conn.Close(websocket.StatusPolicyViolation, "hello 无效")
		return
	}
	s.store.TouchMachine(machine.ID, hello.AgentVersion, hello.HomeDir)
	if hello.HomeDir != "" {
		machine.HomeDir = hello.HomeDir
	}
	c := &agentConn{machine: machine, conn: conn, out: make(chan []byte, 256), closed: make(chan struct{})}
	s.agents.register(c)
	defer s.agents.unregister(c)

	rs, version, err := s.store.RuleSet()
	if err != nil {
		s.log.Printf("读规则失败: %v", err)
		return
	}
	yamlBytes, _ := rs.Marshal()
	s.agents.send(machine.ID, wire.Welcome{T: wire.TWelcome, RulesVersion: version, RulesYAML: string(yamlBytes), Paused: s.paused(), ServerTime: time.Now().UTC()})
	s.onAgentHello(machine, hello)

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	lastPong := time.Now()
	var pongMu sync.Mutex
	go func() { // 写循环与 ping
		ticker := time.NewTicker(s.cfg.PingInterval)
		defer ticker.Stop()
		for {
			select {
			case b := <-c.out:
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(wctx, websocket.MessageText, b)
				wcancel()
				if err != nil {
					cancel()
					return
				}
			case <-ticker.C:
				pongMu.Lock()
				late := time.Since(lastPong) > s.cfg.PongTimeout
				pongMu.Unlock()
				if late {
					s.log.Printf("机器 %s 超过 %s 没有 pong，断开", machine.Name, s.cfg.PongTimeout)
					cancel()
					return
				}
				b, _ := wire.Marshal(wire.Ping{T: wire.TPing})
				wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(wctx, websocket.MessageText, b)
				wcancel()
				if err != nil {
					cancel()
					return
				}
			case <-c.closed:
				cancel()
				return
			case <-ctx.Done():
				return
			}
		}
	}()
	for { // 读循环
		_, b, err := conn.Read(ctx)
		if err != nil {
			return
		}
		kind, err := wire.Kind(b)
		if err != nil {
			continue
		}
		switch kind {
		case wire.TPong:
			pongMu.Lock()
			lastPong = time.Now()
			pongMu.Unlock()
		case wire.TPending:
			var p wire.PendingRequest
			if wire.Unmarshal(b, &p) == nil {
				s.onPending(machine, p)
			}
		case wire.TCancel:
			var f wire.Cancel
			if wire.Unmarshal(b, &f) == nil {
				s.onCancel(machine, f)
			}
		case wire.TExpired:
			var f wire.Expired
			if wire.Unmarshal(b, &f) == nil {
				s.onExpired(machine, f)
			}
		case wire.TDelivered:
			var f wire.Delivered
			if wire.Unmarshal(b, &f) == nil {
				s.onDelivered(machine, f)
			}
		case wire.TAudit:
			var f wire.Audit
			if wire.Unmarshal(b, &f) == nil {
				s.onAudit(machine, f)
			}
		}
	}
}

// paused 读全局总开关。
func (s *Server) paused() bool {
	v, ok, _ := s.store.GetSetting("paused")
	return ok && v == "1"
}
```

本 Task 在 `agents.go` 末尾放 Task 8 要实现的五个占位：
```go
// 以下在 Task 8 实现。
func (s *Server) onAgentHello(m store.Machine, h wire.Hello)       {}
func (s *Server) onPending(m store.Machine, p wire.PendingRequest) {}
func (s *Server) onCancel(m store.Machine, c wire.Cancel)          {}
func (s *Server) onExpired(m store.Machine, e wire.Expired)        {}
func (s *Server) onDelivered(m store.Machine, d wire.Delivered)    {}
func (s *Server) onAudit(m store.Machine, a wire.Audit)            {}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/ -race -run AgentWS -v`
Expected: 5 个测试 PASS。`TestAgentWSHelloTimeout` 约 5 秒。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/agents.go internal/hub/server.go internal/hub/agents_test.go internal/hub/auth_test.go
git commit -m "feat(hub): 守护进程 WebSocket 连接、hello/welcome 与 ping/pong"
```

---

### Task 8: hub 侧请求生命周期与客户端广播

**Files:**
- Create: `internal/hub/requests.go`
- Create: `internal/hub/clients.go`
- Modify: `internal/hub/agents.go`（删除六个占位）
- Modify: `internal/hub/server.go`（删除 `clientHub` 占位）
- Create: `internal/hub/requests_test.go`

**Interfaces:**
- Produces（包内）:
  - `clientHub`：`newClientHub()`、`subscribe() (ch chan []byte, cancel func())`、`broadcast(kind string, payload any)`（帧 `{"type":kind,"data":payload}`，慢的订阅者丢帧）；Task 9 用它做 `/ws/client`
  - `onPending`：去重（`FindResend`，窗口 `ResendWindow`）→ 旧的 `cancelled`/`resend` → `UpsertSession` → `InsertRequest(pending, expires_at)` → 审计 `request_pending` → 广播 `request_new`；同 id 已存在（重连后守护进程重发）则只广播不重复插入
  - `onCancel`/`onExpired`：`SetRequestState(cancelled, terminal|…)` / `(expired, timeout)`，`ErrNotPending` 忽略；广播 `request_updated`
  - `onDelivered`：`MarkDelivered`
  - `onAgentHello`：hub 里该机器的 pending 若不在 hello 列表里 → `cancelled`/`agent_restart`；`UndeliveredDecisions` 中在列表里的 → 重发 `decision` 帧
  - `onAudit`：每条写 `audit_events`（actor `agent`，detail 为条目 JSON）；`kind == permission` 且终态为 `auto_allowed`、`auto_denied`、`passthrough`、`expired`、`cancelled` 时若 `requests` 里没有该 id → 插入该终态行（含脱敏载荷），命中规则的 `auto_*` 调 `RecordHit`
  - 导出：`type DecideInput struct{ RequestID, Action, Note string; Remember bool }`、`func (s *Server) Decide(in DecideInput) (store.Request, error)`：取请求、必须 pending、`Action` 为 allow/deny；`Remember` 时调 `s.learnRule(req, in.Action)`（Task 9 实现；本 Task 放占位返回 `"", nil`）→ `SetRequestState(approved|denied, "user", note, ruleID)` → 审计 `approve|deny` → `agents.send(decision)`（未连接则留给重连补发）→ webhook 回调（Task 10，占位）→ 广播 `request_updated`；错误：`ErrNotFound`、`store.ErrNotPending`、`errBadAction`
  - `var errBadAction = errors.New("action 只能是 allow 或 deny")`

- [ ] **Step 1: 写失败的测试**

`internal/hub/requests_test.go`:
```go
package hub

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func pendingFrame(id, session, turn, cmd string) wire.PendingRequest {
	return wire.PendingRequest{T: wire.TPending, RequestID: id,
		Session:  proto.Session{Tool: "claude", CLISessionID: session, TurnID: turn, Cwd: "/p"},
		ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: cmd, Cwd: "/p"},
		Risk: "outward", ProjectRoot: "/p", EngineReason: "无规则命中", DeadlineSeconds: 86390, CreatedAt: time.Now().UTC()}
}

func TestPendingThenDecideDeliversToAgent(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id})
	defer c.close()
	c.recv(5 * time.Second) // welcome
	sub, stop := h.srv.clients.subscribe()
	defer stop()

	c.send(pendingFrame("r1", "s1", "t1", "git push origin main"))
	waitFor(t, func() bool { n, _ := h.st.CountPending(); return n == 1 })
	if msg := <-sub; !contains(msg, `"type":"request_new"`) || !contains(msg, "r1") {
		t.Fatalf("应广播 request_new: %s", msg)
	}
	req, _ := h.st.GetRequest("r1")
	if req.MachineID != id || req.State != "pending" || req.SessionID == "" || req.ExpiresAt == nil {
		t.Fatalf("登记的请求: %+v", req)
	}

	got, err := h.srv.Decide(DecideInput{RequestID: "r1", Action: "allow", Note: "ok"})
	if err != nil || got.State != "approved" || got.DecidedBy != "user" {
		t.Fatalf("Decide = %+v, %v", got, err)
	}
	kind, b := c.recv(5 * time.Second)
	var d wire.Decision
	wire.Unmarshal(b, &d)
	if kind != wire.TDecision || d.RequestID != "r1" || d.Action != "allow" || d.DecidedBy != "user" {
		t.Fatalf("守护进程应收到决定: %s %+v", kind, d)
	}
	c.send(wire.Delivered{T: wire.TDelivered, RequestID: "r1"})
	waitFor(t, func() bool { r, _ := h.st.GetRequest("r1"); return r.DeliveredAt != nil })
	if _, err := h.srv.Decide(DecideInput{RequestID: "r1", Action: "deny"}); !errors.Is(err, store.ErrNotPending) {
		t.Fatalf("二次决定应 ErrNotPending: %v", err)
	}
	if _, err := h.srv.Decide(DecideInput{RequestID: "r1", Action: "maybe"}); !errors.Is(err, errBadAction) {
		t.Fatalf("坏 action: %v", err)
	}
	if _, err := h.srv.Decide(DecideInput{RequestID: "nope", Action: "allow"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("不存在: %v", err)
	}
	events, _ := h.st.ListAudit(10, "r1")
	if len(events) < 2 {
		t.Fatalf("审计应有登记与决定: %+v", events)
	}
}

func TestResendCancelsOld(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "touch a"))
	waitFor(t, func() bool { n, _ := h.st.CountPending(); return n == 1 })
	c.send(pendingFrame("r2", "s1", "t1", "touch a"))
	waitFor(t, func() bool { r, _ := h.st.GetRequest("r2"); return r.State == "pending" })
	old, _ := h.st.GetRequest("r1")
	if old.State != "cancelled" || old.DecidedBy != "resend" {
		t.Fatalf("旧请求应被标 resend: %+v", old)
	}
	if n, _ := h.st.CountPending(); n != 1 {
		t.Fatalf("pending 应只剩一条, 得到 %d", n)
	}
}

func TestCancelExpiredAndDuplicatePending(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "a"))
	c.send(pendingFrame("r2", "s2", "t2", "b"))
	waitFor(t, func() bool { n, _ := h.st.CountPending(); return n == 2 })
	c.send(pendingFrame("r1", "s1", "t1", "a")) // 重连后重发同 id
	time.Sleep(100 * time.Millisecond)
	if n, _ := h.st.CountPending(); n != 2 {
		t.Fatalf("同 id 重发不应重复插入, 得到 %d", n)
	}
	c.send(wire.Cancel{T: wire.TCancel, RequestID: "r1", Reason: "terminal"})
	c.send(wire.Expired{T: wire.TExpired, RequestID: "r2"})
	waitFor(t, func() bool { n, _ := h.st.CountPending(); return n == 0 })
	r1, _ := h.st.GetRequest("r1")
	r2, _ := h.st.GetRequest("r2")
	if r1.State != "cancelled" || r1.DecidedBy != "terminal" || r2.State != "expired" || r2.DecidedBy != "timeout" {
		t.Fatalf("r1=%+v r2=%+v", r1, r2)
	}
}

func TestResyncOnReconnect(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id})
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "a"))
	c.send(pendingFrame("r2", "s2", "t2", "b"))
	waitFor(t, func() bool { n, _ := h.st.CountPending(); return n == 2 })
	c.close()
	waitFor(t, func() bool { return !h.srv.agents.connected(id) })
	// 断线期间用户决定了 r1
	if _, err := h.srv.Decide(DecideInput{RequestID: "r1", Action: "deny", Note: "no"}); err != nil {
		t.Fatal(err)
	}
	// 重连：守护进程只还持有 r1，r2 已经随钩子消失
	c2 := dialAgent(t, h, secret, wire.Hello{MachineID: id, Pending: []wire.PendingSummary{{RequestID: "r1", CreatedAt: time.Now()}}})
	defer c2.close()
	c2.recv(5 * time.Second) // welcome
	kind, b := c2.recv(5 * time.Second)
	var d wire.Decision
	wire.Unmarshal(b, &d)
	if kind != wire.TDecision || d.RequestID != "r1" || d.Action != "deny" {
		t.Fatalf("重连后应补发 r1 的决定: %s %+v", kind, d)
	}
	r2, _ := h.st.GetRequest("r2")
	if r2.State != "cancelled" || r2.DecidedBy != "agent_restart" {
		t.Fatalf("守护进程不再持有的 pending 应被取消: %+v", r2)
	}
}

func TestAuditIngestCreatesHistoryRows(t *testing.T) {
	h := newTestHub(t)
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	p := policy.Payload{Type: policy.PayloadShell, Command: "ls", Cwd: "/p"}
	e1, _ := json.Marshal(agent.AuditEntry{TS: time.Now(), RequestID: "auto1", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"}, ToolName: "Bash", Payload: &p, Risk: "read", Action: "allow", State: "auto_allowed", RuleID: "builtin-readonly", DecidedBy: "rule:builtin-readonly"})
	e2, _ := json.Marshal(agent.AuditEntry{TS: time.Now(), Kind: "done", Session: proto.Session{Tool: "claude", CLISessionID: "s1"}, State: "recorded", Message: "DONE"})
	c.send(wire.Audit{T: wire.TAudit, Entries: []json.RawMessage{e1, e2}})
	waitFor(t, func() bool { _, err := h.st.GetRequest("auto1"); return err == nil })
	r, _ := h.st.GetRequest("auto1")
	if r.State != "auto_allowed" || r.RuleID != "builtin-readonly" || r.Payload.Command != "ls" || r.MachineID != id {
		t.Fatalf("历史行: %+v", r)
	}
	rules, _ := h.st.ListRules()
	var hits int
	for _, x := range rules {
		if x.ID == "builtin-readonly" {
			hits = x.HitCount
		}
	}
	if hits != 1 {
		t.Fatalf("应记一次命中, 得到 %d", hits)
	}
	events, _ := h.st.ListAudit(10, "")
	if len(events) < 2 {
		t.Fatalf("审计事件: %+v", events)
	}
}

func contains(b []byte, s string) bool { return strings.Contains(string(b), s) }

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

Run: `go test ./internal/hub/ -run 'Pending|Resend|CancelExpired|Resync|AuditIngest'`
Expected: FAIL，报 `undefined: DecideInput` 等。

- [ ] **Step 3: 写客户端广播**

`internal/hub/clients.go`（替换 `server.go` 里的 `clientHub` 占位）:
```go
package hub

import (
	"encoding/json"
	"sync"
)

// clientHub 把 hub 里发生的事推给已打开的网页。慢的订阅者丢帧，不阻塞主流程。
type clientHub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

func newClientHub() *clientHub { return &clientHub{subs: map[chan []byte]struct{}{}} }

func (h *clientHub) subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

// broadcast 发 {"type":kind,"data":payload}。
func (h *clientHub) broadcast(kind string, payload any) {
	b, err := json.Marshal(map[string]any{"type": kind, "data": payload})
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- b:
		default:
		}
	}
}
```

- [ ] **Step 4: 写请求生命周期**

`internal/hub/requests.go`（并删除 `agents.go` 末尾的六个占位）:
```go
package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
)

var errBadAction = errors.New("action 只能是 allow 或 deny")

// DecideInput 是网页或 CLI 的一次决定。
type DecideInput struct {
	RequestID string
	Action    string
	Note      string
	Remember  bool
}

func (s *Server) audit(machineID, actor, requestID, action string, detail any) {
	b, _ := json.Marshal(detail)
	if err := s.store.InsertAudit(store.AuditEvent{MachineID: machineID, Actor: actor, RequestID: requestID, Action: action, Detail: string(b)}); err != nil {
		s.log.Printf("写审计: %v", err)
	}
}

// onPending 登记守护进程送来的待决请求。
func (s *Server) onPending(m store.Machine, p wire.PendingRequest) {
	if existing, err := s.store.GetRequest(p.RequestID); err == nil {
		s.clients.broadcast("request_updated", existing) // 重连后重发，不重复插入
		return
	}
	hash := store.PayloadHash(p.Payload)
	if old, err := s.store.FindResend(m.ID, p.Session.CLISessionID, p.Session.TurnID, p.ToolName, hash, s.cfg.ResendWindow); err == nil {
		if err := s.store.SetRequestState(old.ID, "cancelled", "resend", "同一命令重新发起", ""); err == nil {
			s.audit(m.ID, "system", old.ID, "cancelled_resend", map[string]string{"replaced_by": p.RequestID})
			if r, err := s.store.GetRequest(old.ID); err == nil {
				s.clients.broadcast("request_updated", r)
			}
		}
	}
	sessionID, err := s.store.UpsertSession(m.ID, p.Session, projectName(p.ProjectRoot))
	if err != nil {
		s.log.Printf("登记会话: %v", err)
	}
	expires := p.CreatedAt.Add(time.Duration(p.DeadlineSeconds) * time.Second).UTC().Format(time.RFC3339Nano)
	r := store.Request{ID: p.RequestID, MachineID: m.ID, SessionID: sessionID, Kind: "permission", ToolName: p.ToolName, Payload: p.Payload, PayloadHash: hash,
		Risk: p.Risk, ProjectRoot: p.ProjectRoot, EngineReason: p.EngineReason, State: "pending", TurnID: p.Session.TurnID, CLISessionID: p.Session.CLISessionID,
		CreatedAt: p.CreatedAt.UTC().Format(time.RFC3339Nano), ExpiresAt: &expires}
	if err := s.store.InsertRequest(r); err != nil {
		s.log.Printf("登记请求 %s: %v", p.RequestID, err)
		return
	}
	s.audit(m.ID, "agent", r.ID, "request_pending", map[string]string{"risk": r.Risk, "tool": r.ToolName})
	s.clients.broadcast("request_new", r)
}

func projectName(root string) string {
	if root == "" {
		return ""
	}
	for i := len(root) - 1; i >= 0; i-- {
		if root[i] == '/' {
			return root[i+1:]
		}
	}
	return root
}

func (s *Server) finish(machineID, id, state, decidedBy, note string) {
	err := s.store.SetRequestState(id, state, decidedBy, note, "")
	if errors.Is(err, store.ErrNotPending) {
		return
	}
	if err != nil {
		s.log.Printf("更新请求 %s: %v", id, err)
		return
	}
	s.audit(machineID, "agent", id, state, map[string]string{"decided_by": decidedBy})
	if r, err := s.store.GetRequest(id); err == nil {
		s.clients.broadcast("request_updated", r)
	}
}

func (s *Server) onCancel(m store.Machine, c wire.Cancel) {
	reason := c.Reason
	if reason == "" {
		reason = "terminal"
	}
	s.finish(m.ID, c.RequestID, "cancelled", reason, "钩子进程在决定前退出")
}

func (s *Server) onExpired(m store.Machine, e wire.Expired) {
	s.finish(m.ID, e.RequestID, "expired", "timeout", "守护进程等待过期")
}

func (s *Server) onDelivered(m store.Machine, d wire.Delivered) {
	if err := s.store.MarkDelivered(d.RequestID); err != nil {
		s.log.Printf("标记送达 %s: %v", d.RequestID, err)
	}
}

// onAgentHello 用守护进程的 pending 列表对齐：hub 多出来的取消，已决定未送达的补发。
func (s *Server) onAgentHello(m store.Machine, h wire.Hello) {
	held := map[string]bool{}
	for _, p := range h.Pending {
		held[p.RequestID] = true
	}
	pend, _ := s.store.ListPendingForMachine(m.ID)
	for _, r := range pend {
		if !held[r.ID] {
			s.finish(m.ID, r.ID, "cancelled", "agent_restart", "守护进程重连后不再持有该请求")
		}
	}
	und, _ := s.store.UndeliveredDecisions(m.ID)
	for _, r := range und {
		if held[r.ID] {
			s.sendDecision(r)
		}
	}
}

func (s *Server) sendDecision(r store.Request) bool {
	action := "deny"
	if r.State == "approved" {
		action = "allow"
	}
	return s.agents.send(r.MachineID, wire.Decision{T: wire.TDecision, RequestID: r.ID, Action: action, Reason: r.DecisionNote, RuleID: r.RuleID, DecidedBy: r.DecidedBy})
}

// Decide 是用户的决定入口。
func (s *Server) Decide(in DecideInput) (store.Request, error) {
	if in.Action != "allow" && in.Action != "deny" {
		return store.Request{}, errBadAction
	}
	r, err := s.store.GetRequest(in.RequestID)
	if err != nil {
		return store.Request{}, err
	}
	if r.State != "pending" {
		return store.Request{}, store.ErrNotPending
	}
	ruleID := ""
	if in.Remember {
		id, err := s.learnRule(r, in.Action)
		if err != nil {
			return store.Request{}, fmt.Errorf("生成规则: %w", err)
		}
		ruleID = id
	}
	state := "denied"
	if in.Action == "allow" {
		state = "approved"
	}
	if err := s.store.SetRequestState(r.ID, state, "user", in.Note, ruleID); err != nil {
		return store.Request{}, err
	}
	r, _ = s.store.GetRequest(r.ID)
	s.audit(r.MachineID, "user", r.ID, in.Action, map[string]any{"note": in.Note, "remember": in.Remember, "rule_id": ruleID})
	if r.Kind == "webhook" {
		s.callbackWebhook(r)
	} else {
		s.sendDecision(r) // 未连接时留给重连对齐补发
	}
	s.clients.broadcast("request_updated", r)
	return r, nil
}

// onAudit 收守护进程的审计批次：事件入库；自动决定补成历史行并记命中。
func (s *Server) onAudit(m store.Machine, a wire.Audit) {
	for _, raw := range a.Entries {
		var e agent.AuditEntry
		if err := json.Unmarshal(raw, &e); err != nil {
			continue
		}
		s.store.InsertAudit(store.AuditEvent{TS: e.TS.UTC().Format(time.RFC3339Nano), MachineID: m.ID, Actor: "agent", RequestID: e.RequestID, Action: e.State, Detail: string(raw)})
		if e.Kind != "permission" || e.RequestID == "" {
			continue
		}
		switch e.State {
		case "auto_allowed", "auto_denied", "passthrough", "expired", "cancelled":
		default:
			continue
		}
		if _, err := s.store.GetRequest(e.RequestID); err == nil {
			continue
		}
		var payload = e.Payload
		r := store.Request{ID: e.RequestID, MachineID: m.ID, Kind: "permission", ToolName: e.ToolName, Risk: e.Risk, EngineReason: e.Reason,
			State: e.State, DecidedBy: e.DecidedBy, RuleID: e.RuleID, TurnID: e.Session.TurnID, CLISessionID: e.Session.CLISessionID,
			CreatedAt: e.TS.UTC().Format(time.RFC3339Nano)}
		if payload != nil {
			r.Payload = *payload
		}
		decided := r.CreatedAt
		r.DecidedAt = &decided
		if sessionID, err := s.store.UpsertSession(m.ID, e.Session, ""); err == nil {
			r.SessionID = sessionID
		}
		if err := s.store.InsertRequest(r); err != nil {
			s.log.Printf("补历史行 %s: %v", e.RequestID, err)
			continue
		}
		if e.RuleID != "" && (e.State == "auto_allowed" || e.State == "auto_denied") {
			s.store.RecordHit(e.RuleID)
		}
	}
}

// learnRule 与 callbackWebhook 在 Task 9、10 实现。
func (s *Server) learnRule(r store.Request, action string) (string, error) { return "", nil }
func (s *Server) callbackWebhook(r store.Request)                          {}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/ -race -v`
Expected: 全部 PASS。

- [ ] **Step 6: 提交**

```bash
git add internal/hub/requests.go internal/hub/clients.go internal/hub/agents.go internal/hub/server.go internal/hub/requests_test.go
git commit -m "feat(hub): 请求登记与去重、用户决定下发、重连对齐、审计入库与客户端广播"
```

---

### Task 9: 客户端 REST 与 `/ws/client`、规则服务与学习

**Files:**
- Create: `internal/hub/api.go`
- Create: `internal/hub/rules.go`
- Modify: `internal/hub/requests.go`（删除 `learnRule` 占位）
- Modify: `internal/hub/server.go`（`routes` 加路由；`handleStatus` 移到 `api.go`）
- Modify: `internal/hub/store/requests.go`（给 `Request` 字段加 json 标签：`id`、`machine_id`、`session_id`、`kind`、`tool_name`、`payload`、`risk`、`project_root`、`engine_reason`、`state`、`decided_by`、`decision_note`、`rule_id`、`turn_id`、`cli_session_id`、`title`、`created_at`、`expires_at`、`decided_at`、`delivered_at`；`PayloadHash` 与 `Callback` 标 `json:"-"`）
- Create: `internal/hub/api_test.go`

**Interfaces:**
- 路由（除 `/api/webhook/*` 外都要 cookie）:
  - `GET /api/status` → `{paused, pending, rules_version, machines:[{id,name,online,last_seen}], uptime}`
  - `POST /api/pause`、`POST /api/resume` → 204；写 settings `paused`，审计，向所有守护进程广播 `paused` 帧，客户端广播 `status`
  - `GET /api/inbox` → pending 请求列表（含机器名、项目名）；`GET /api/requests?state=&limit=`（limit 默认 50，最大 500）；`GET /api/requests/{id}` → 请求加 `audit:[...]`
  - `POST /api/requests/{id}/decide` `{"action":"allow|deny","note":"…","remember":true}` → 200 请求；409 非 pending；400 坏 action；422 记不住（`ErrCannotLearn` 等）
  - `GET /api/rules` → `[{id,source,enabled,hit_count,last_hit_at,yaml}]`；`PATCH /api/rules/{id}` `{"enabled":false}`；`DELETE /api/rules/{id}`；`POST /api/rules/import`（body 为 YAML，`Content-Type: application/yaml`）→ `{version}`；`GET /api/rules/export` → YAML
  - `GET /api/machines`；`DELETE /api/machines/{id}`（吊销并断开）
  - `GET /ws/client`：cookie 认证的 WebSocket，推 `clientHub` 的帧，服务端 30 秒 ping
  - `learnRule(r store.Request, action string) (string, error)`：`policy.ProposeRule(r.Payload, policy.Context{ProjectRoot: r.ProjectRoot, HomeDir: machine.HomeDir, MachineID: r.MachineID}, r.ID)`；`action == deny` 时把草案改成 `Source: user`、`Action: deny`（记住的拒绝必须压过内置放行）；`InsertRule` → 版本号 → `pushRules()`（向所有守护进程发 `rules` 帧，客户端广播 `rules_updated`）
  - `func (s *Server) pushRules()`、`func (s *Server) setPaused(p bool) error`

- [ ] **Step 1: 写失败的测试**

`internal/hub/api_test.go`:
```go
package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/xufei547/railse-opc/internal/hub/wire"
)

func doReq(t *testing.T, method, url string, body []byte, ct string, cookie *http.Cookie) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, url, bytes.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestInboxDecideRemember(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter2")
	ck := login(t, h, "hunter2")
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, HomeDir: "/home/u"})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "npm run build"))
	waitFor(t, func() bool { n, _ := h.st.CountPending(); return n == 1 })

	var inbox []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
	if len(inbox) != 1 || inbox[0]["id"] != "r1" || inbox[0]["machine_name"] != "mac" || inbox[0]["project"] != "p" {
		t.Fatalf("inbox = %+v", inbox)
	}
	body, _ := json.Marshal(map[string]any{"action": "allow", "remember": true, "note": "ok"})
	resp := doReq(t, "POST", h.http.URL+"/api/requests/r1/decide", body, "application/json", ck)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("decide = %d", resp.StatusCode)
	}
	var decided map[string]any
	decodeBody(t, resp, &decided)
	if decided["state"] != "approved" || decided["rule_id"] == "" {
		t.Fatalf("decided = %+v", decided)
	}
	// 守护进程先收到规则快照或决定，顺序不定，两帧都要到
	seen := map[string]bool{}
	for i := 0; i < 2; i++ {
		kind, _ := c.recv(5 * time.Second)
		seen[kind] = true
	}
	if !seen[wire.TDecision] || !seen[wire.TRules] {
		t.Fatalf("应收到 decision 与 rules: %+v", seen)
	}
	var rules []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/rules", nil, "", ck), &rules)
	if len(rules) != 3 || rules[2]["source"] != "learned" {
		t.Fatalf("rules = %+v", rules)
	}
	resp = doReq(t, "POST", h.http.URL+"/api/requests/r1/decide", body, "application/json", ck)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("二次决定应 409, 得到 %d", resp.StatusCode)
	}
}

func TestRememberDenyBecomesUserRule(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter2")
	ck := login(t, h, "hunter2")
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, HomeDir: "/home/u"})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "npm run deploy"))
	waitFor(t, func() bool { n, _ := h.st.CountPending(); return n == 1 })
	body, _ := json.Marshal(map[string]any{"action": "deny", "remember": true})
	if resp := doReq(t, "POST", h.http.URL+"/api/requests/r1/decide", body, "application/json", ck); resp.StatusCode != http.StatusOK {
		t.Fatalf("decide = %d", resp.StatusCode)
	}
	rows, _ := h.st.ListRules()
	last := rows[len(rows)-1]
	if last.Source != "user" || !strings.Contains(last.YAML, "action: deny") || !strings.Contains(last.YAML, "npm run") {
		t.Fatalf("记住的拒绝应是 user 来源的 deny: %+v", last)
	}
}

func TestRememberRefusedForOutward(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter2")
	ck := login(t, h, "hunter2")
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, HomeDir: "/home/u"})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("r1", "s1", "t1", "git push origin main"))
	waitFor(t, func() bool { n, _ := h.st.CountPending(); return n == 1 })
	body, _ := json.Marshal(map[string]any{"action": "allow", "remember": true})
	resp := doReq(t, "POST", h.http.URL+"/api/requests/r1/decide", body, "application/json", ck)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("对外命令不能记住, 应 422, 得到 %d", resp.StatusCode)
	}
	if r, _ := h.st.GetRequest("r1"); r.State != "pending" {
		t.Fatal("记不住时不应改变请求状态")
	}
}

func TestRulesImportExportToggleDelete(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter2")
	ck := login(t, h, "hunter2")
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	y := "version: 1\nrules:\n  - id: only\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n"
	resp := doReq(t, "POST", h.http.URL+"/api/rules/import", []byte(y), "application/yaml", ck)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("import = %d", resp.StatusCode)
	}
	if kind, b := c.recv(5 * time.Second); kind != wire.TRules || !strings.Contains(string(b), "only") {
		t.Fatalf("导入后应下发规则: %s %s", kind, b)
	}
	resp = doReq(t, "GET", h.http.URL+"/api/rules/export", nil, "", ck)
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if !strings.Contains(buf.String(), "id: only") {
		t.Fatalf("export = %s", buf.String())
	}
	if resp := doReq(t, "PATCH", h.http.URL+"/api/rules/only", []byte(`{"enabled":false}`), "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("patch = %d", resp.StatusCode)
	}
	c.recv(5 * time.Second)
	if resp := doReq(t, "DELETE", h.http.URL+"/api/rules/only", nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if resp := doReq(t, "DELETE", h.http.URL+"/api/rules/only", nil, "", ck); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("再删应 404, 得到 %d", resp.StatusCode)
	}
	if resp := doReq(t, "POST", h.http.URL+"/api/rules/import", []byte("version: 9\n"), "application/yaml", ck); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("坏 YAML 应 400, 得到 %d", resp.StatusCode)
	}
}

func TestPauseResumeAndMachines(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter2")
	ck := login(t, h, "hunter2")
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	if resp := doReq(t, "POST", h.http.URL+"/api/pause", nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("pause = %d", resp.StatusCode)
	}
	if kind, b := c.recv(5 * time.Second); kind != wire.TPaused || !strings.Contains(string(b), "true") {
		t.Fatalf("应下发 paused: %s %s", kind, b)
	}
	var st map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/status", nil, "", ck), &st)
	if st["paused"] != true {
		t.Fatalf("status = %+v", st)
	}
	machines := st["machines"].([]any)
	if len(machines) != 1 || machines[0].(map[string]any)["online"] != true {
		t.Fatalf("machines = %+v", machines)
	}
	doReq(t, "POST", h.http.URL+"/api/resume", nil, "", ck)
	c.recv(5 * time.Second)
	if resp := doReq(t, "DELETE", h.http.URL+"/api/machines/"+id, nil, "", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke = %d", resp.StatusCode)
	}
	waitFor(t, func() bool { return !h.srv.agents.connected(id) })
	if _, err := h.st.AuthMachine(secret); err == nil {
		t.Fatal("吊销后密钥应失效")
	}
}

func TestClientWS(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter2")
	ck := login(t, h, "hunter2")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(h.http.URL, "http") + "/ws/client"
	if _, resp, err := websocket.Dial(ctx, url, nil); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 cookie 应 401: %v", err)
	}
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Cookie": {ck.Name + "=" + ck.Value}}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	h.srv.clients.broadcast("request_new", map[string]string{"id": "x"})
	_, b, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(b), `"type":"request_new"`) {
		t.Fatalf("应收到广播: %s %v", b, err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/ -run 'Inbox|Remember|RulesImport|PauseResume|ClientWS'`
Expected: FAIL（404 或 `learnRule` 返回空）。

- [ ] **Step 3: 写规则服务**

`internal/hub/rules.go`（并删除 `requests.go` 里的 `learnRule` 占位）:
```go
package hub

import (
	"fmt"

	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/policy"
)

// pushRules 把当前规则快照发给所有守护进程并通知网页。
func (s *Server) pushRules() {
	rs, version, err := s.store.RuleSet()
	if err != nil {
		s.log.Printf("读规则: %v", err)
		return
	}
	b, _ := rs.Marshal()
	s.agents.broadcast(wire.Rules{T: wire.TRules, Version: version, YAML: string(b)})
	s.clients.broadcast("rules_updated", map[string]int{"version": version})
}

// learnRule 从一条请求生成规则：批准生成 learned 放行；拒绝生成 user 来源的 deny，保证压过内置放行。
func (s *Server) learnRule(r store.Request, action string) (string, error) {
	m, err := s.store.GetMachine(r.MachineID)
	if err != nil {
		return "", err
	}
	rule, err := policy.ProposeRule(r.Payload, policy.Context{ProjectRoot: r.ProjectRoot, HomeDir: m.HomeDir, MachineID: r.MachineID}, r.ID)
	if err != nil {
		return "", err
	}
	if action == "deny" {
		rule.Source = policy.SourceUser
		rule.Action = policy.ActionDeny
		rule.ID = "deny-" + rule.ID
	}
	id, _, err := s.store.InsertRule(rule)
	if err != nil {
		return "", err
	}
	s.audit(r.MachineID, "user", r.ID, "rule_learned", map[string]string{"rule_id": id, "action": action})
	s.pushRules()
	return id, nil
}

// setPaused 改总开关并通知所有守护进程。
func (s *Server) setPaused(p bool) error {
	v := "0"
	if p {
		v = "1"
	}
	if err := s.store.SetSetting("paused", v); err != nil {
		return err
	}
	s.audit("", "user", "", fmt.Sprintf("paused=%v", p), nil)
	s.agents.broadcast(wire.Paused{T: wire.TPaused, Paused: p})
	s.clients.broadcast("status", map[string]bool{"paused": p})
	return nil
}
```
`agentHub.broadcast` 加到 `agents.go`：
```go
func (h *agentHub) broadcast(frame any) {
	for _, id := range h.connectedIDs() {
		h.send(id, frame)
	}
}
```

- [ ] **Step 4: 写 REST 与 `/ws/client`**

`internal/hub/api.go`（把 `handleStatus` 从 `server.go` 挪过来并扩充；`routes` 加全部路由）:
```go
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/policy"
)

func (s *Server) apiRoutes() {
	s.mux.HandleFunc("GET /api/status", s.requireClient(s.handleStatus))
	s.mux.HandleFunc("POST /api/pause", s.requireClient(func(w http.ResponseWriter, r *http.Request) { s.handlePause(w, true) }))
	s.mux.HandleFunc("POST /api/resume", s.requireClient(func(w http.ResponseWriter, r *http.Request) { s.handlePause(w, false) }))
	s.mux.HandleFunc("GET /api/inbox", s.requireClient(s.handleInbox))
	s.mux.HandleFunc("GET /api/requests", s.requireClient(s.handleRequests))
	s.mux.HandleFunc("GET /api/requests/{id}", s.requireClient(s.handleRequest))
	s.mux.HandleFunc("POST /api/requests/{id}/decide", s.requireClient(s.handleDecide))
	s.mux.HandleFunc("GET /api/rules", s.requireClient(s.handleRules))
	s.mux.HandleFunc("PATCH /api/rules/{id}", s.requireClient(s.handleRulePatch))
	s.mux.HandleFunc("DELETE /api/rules/{id}", s.requireClient(s.handleRuleDelete))
	s.mux.HandleFunc("POST /api/rules/import", s.requireClient(s.handleRulesImport))
	s.mux.HandleFunc("GET /api/rules/export", s.requireClient(s.handleRulesExport))
	s.mux.HandleFunc("GET /api/machines", s.requireClient(s.handleMachines))
	s.mux.HandleFunc("DELETE /api/machines/{id}", s.requireClient(s.handleMachineRevoke))
	s.mux.HandleFunc("GET /ws/client", s.requireClient(s.handleClientWS))
}

type requestView struct {
	store.Request
	MachineName string `json:"machine_name"`
	Project     string `json:"project"`
}

func (s *Server) view(r store.Request) requestView {
	v := requestView{Request: r, Project: projectName(r.ProjectRoot)}
	if m, err := s.store.GetMachine(r.MachineID); err == nil {
		v.MachineName = m.Name
	}
	return v
}

func (s *Server) views(rs []store.Request) []requestView {
	out := make([]requestView, 0, len(rs))
	for _, r := range rs {
		out = append(out, s.view(r))
	}
	return out
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	pending, _ := s.store.CountPending()
	version, _ := s.store.RulesVersion()
	machines, _ := s.store.ListMachines()
	var ms []map[string]any
	for _, m := range machines {
		ms = append(ms, map[string]any{"id": m.ID, "name": m.Name, "online": s.agents.connected(m.ID), "last_seen": m.LastSeen, "revoked": m.Revoked})
	}
	writeJSON(w, http.StatusOK, map[string]any{"paused": s.paused(), "pending": pending, "rules_version": version, "machines": ms, "uptime": time.Since(s.start).Round(time.Second).String()})
}

func (s *Server) handlePause(w http.ResponseWriter, paused bool) {
	if err := s.setPaused(paused); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleInbox(w http.ResponseWriter, r *http.Request) {
	rs, err := s.store.ListRequests("pending", 500)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.views(rs))
}

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	rs, err := s.store.ListRequests(r.URL.Query().Get("state"), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.views(rs))
}

func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	req, err := s.store.GetRequest(r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "没有这条请求")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	events, _ := s.store.ListAudit(100, req.ID)
	writeJSON(w, http.StatusOK, map[string]any{"request": s.view(req), "audit": events})
}

func (s *Server) handleDecide(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Action   string `json:"action"`
		Note     string `json:"note"`
		Remember bool   `json:"remember"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "请求体不是 JSON")
		return
	}
	req, err := s.Decide(DecideInput{RequestID: r.PathValue("id"), Action: body.Action, Note: body.Note, Remember: body.Remember})
	switch {
	case errors.Is(err, errBadAction):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "没有这条请求")
	case errors.Is(err, store.ErrNotPending):
		writeErr(w, http.StatusConflict, "请求已经处理过")
	case err != nil && (errors.Is(err, policy.ErrCannotLearn) || isLearnError(err)):
		writeErr(w, http.StatusUnprocessableEntity, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, s.view(req))
	}
}

// isLearnError 识别 Decide 包上“生成规则”前缀的学习失败。
func isLearnError(err error) bool {
	return err != nil && strings.HasPrefix(err.Error(), "生成规则")
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListRules()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(rows))
	for _, x := range rows {
		out = append(out, map[string]any{"id": x.ID, "source": x.Source, "enabled": x.Enabled, "hit_count": x.HitCount, "last_hit_at": x.LastHitAt, "created_at": x.CreatedAt, "yaml": x.YAML})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleRulePatch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil || body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, "需要 enabled")
		return
	}
	if _, err := s.store.SetRuleEnabled(r.PathValue("id"), *body.Enabled); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "没有这条规则")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit("", "user", "", "rule_toggled", map[string]any{"rule_id": r.PathValue("id"), "enabled": *body.Enabled})
	s.pushRules()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.DeleteRule(r.PathValue("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "没有这条规则")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit("", "user", "", "rule_deleted", map[string]string{"rule_id": r.PathValue("id")})
	s.pushRules()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRulesImport(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "读请求体失败")
		return
	}
	rs, err := policy.ParseRuleSet(data)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	version, err := s.store.ReplaceRules(rs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s.audit("", "user", "", "rules_imported", map[string]int{"count": len(rs.Rules), "version": version})
	s.pushRules()
	writeJSON(w, http.StatusOK, map[string]int{"version": version})
}

func (s *Server) handleRulesExport(w http.ResponseWriter, r *http.Request) {
	rs, _, err := s.store.RuleSet()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	b, _ := rs.Marshal()
	w.Header().Set("Content-Type", "application/yaml")
	w.Write(b)
}

func (s *Server) handleMachines(w http.ResponseWriter, r *http.Request) {
	machines, err := s.store.ListMachines()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]map[string]any, 0, len(machines))
	for _, m := range machines {
		out = append(out, map[string]any{"id": m.ID, "name": m.Name, "online": s.agents.connected(m.ID), "last_seen": m.LastSeen, "agent_version": m.AgentVersion, "enrolled_at": m.EnrolledAt, "revoked": m.Revoked})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleMachineRevoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetMachine(id); err != nil {
		writeErr(w, http.StatusNotFound, "没有这台机器")
		return
	}
	if err := s.store.RevokeMachine(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.agents.disconnect(id)
	s.audit(id, "user", "", "machine_revoked", nil)
	w.WriteHeader(http.StatusNoContent)
}

// handleClientWS 把 clientHub 的广播推给网页。
func (s *Server) handleClientWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{r.Host}})
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	ch, stop := s.clients.subscribe()
	defer stop()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() { // 读循环只为感知关闭
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				cancel()
				return
			}
		}
	}()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case b := <-ch:
			if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
				return
			}
		case <-ticker.C:
			if err := conn.Ping(ctx); err != nil {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}
```
`agentHub.disconnect` 加到 `agents.go`：
```go
func (h *agentHub) disconnect(machineID string) {
	h.mu.Lock()
	c := h.conns[machineID]
	delete(h.conns, machineID)
	h.mu.Unlock()
	if c != nil {
		c.close()
	}
}
```
`server.go` 的 `routes()` 改为只注册认证三条与 `/ws/agent`，再调 `s.apiRoutes()`；删掉旧的 `handleStatus`。`isLearnError` 的判断只靠错误文本里的“生成规则”前缀，`Decide` 对学习失败统一包了这个前缀。

- [ ] **Step 5: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/ -race -v`
Expected: 全部 PASS。`TestInboxDecideRemember` 里 `npm run build` 是 write_in_project，可以学；`TestRememberRefusedForOutward` 的 `git push` 是 outward，`ProposeRule` 返回 `ErrCannotLearn`，接口回 422 且请求保持 pending。

- [ ] **Step 6: 提交**

```bash
git add internal/hub/api.go internal/hub/rules.go internal/hub/requests.go internal/hub/agents.go internal/hub/server.go internal/hub/store/requests.go internal/hub/api_test.go
git commit -m "feat(hub): 客户端 REST、网页 WebSocket 广播、规则服务与“批准并记住”"
```

---

### Task 10: webhook 接入与回调

**Files:**
- Create: `internal/hub/webhook.go`
- Modify: `internal/hub/requests.go`（删除 `callbackWebhook` 占位）
- Modify: `internal/hub/server.go`（`routes` 加 `POST /api/webhook/approvals`）
- Create: `internal/hub/webhook_test.go`

**Interfaces:**
- `POST /api/webhook/approvals`：`Authorization: Bearer <来源令牌>`（settings `webhook_token_hash`，由 `railse hub webhook-token` 生成）；body `{"title":"…","description":"…","callback_url":"https://…","callback_token":"…"}`；创建 `kind: webhook` 的 pending 请求（`MachineID` 为 `"webhook"`，`Payload` 为 `{"type":"other","tool":"webhook"}`，`Title`、`Callback` 存 JSON），广播 `request_new`，返回 `{"id":"…"}`
- `callbackWebhook(r)`：决定后 `POST callback_url`，头 `Authorization: Bearer <callback_token>`，body `{"id","decision":"allow|deny","note"}`，超时 10 秒，失败重试 3 次（间隔 2 秒），结果写审计 `webhook_callback`
- 回调 URL 只允许 `http`、`https`；不做私网限制（n8n 常在同一台服务器）

- [ ] **Step 1: 写失败的测试**

`internal/hub/webhook_test.go`:
```go
package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/xufei547/railse-opc/internal/hub/store"
)

func TestWebhookIngestAndCallback(t *testing.T) {
	h := newTestHub(t)
	h.srv.SetPassword("hunter2")
	ck := login(t, h, "hunter2")
	tok := store.RandomToken()
	h.st.SetSetting("webhook_token_hash", store.HashToken(tok))

	var got atomic.Value
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		body["auth"] = r.Header.Get("Authorization")
		got.Store(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	body := map[string]string{"title": "发布周报", "description": "n8n 等待批准", "callback_url": target.URL + "/cb", "callback_token": "cbtok"}
	resp := doReq(t, "POST", h.http.URL+"/api/webhook/approvals", mustJSON(body), "application/json", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401, 得到 %d", resp.StatusCode)
	}
	req, _ := http.NewRequest("POST", h.http.URL+"/api/webhook/approvals", bytesReader(mustJSON(body)))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest = %d", resp.StatusCode)
	}
	var out struct {
		ID string `json:"id"`
	}
	decodeBody(t, resp, &out)
	r, err := h.st.GetRequest(out.ID)
	if err != nil || r.Kind != "webhook" || r.State != "pending" || r.Title != "发布周报" {
		t.Fatalf("请求 = %+v, %v", r, err)
	}
	var inbox []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
	if len(inbox) != 1 || inbox[0]["title"] != "发布周报" {
		t.Fatalf("inbox = %+v", inbox)
	}
	if resp := doReq(t, "POST", h.http.URL+"/api/requests/"+out.ID+"/decide", []byte(`{"action":"allow","note":"去吧"}`), "application/json", ck); resp.StatusCode != http.StatusOK {
		t.Fatalf("decide = %d", resp.StatusCode)
	}
	waitFor(t, func() bool { return got.Load() != nil })
	cb := got.Load().(map[string]string)
	if cb["decision"] != "allow" || cb["note"] != "去吧" || cb["auth"] != "Bearer cbtok" || cb["id"] != out.ID {
		t.Fatalf("回调 = %+v", cb)
	}
	waitFor(t, func() bool {
		ev, _ := h.st.ListAudit(20, out.ID)
		for _, e := range ev {
			if e.Action == "webhook_callback" {
				return true
			}
		}
		return false
	})
}

func TestWebhookRejectsBadBody(t *testing.T) {
	h := newTestHub(t)
	tok := store.RandomToken()
	h.st.SetSetting("webhook_token_hash", store.HashToken(tok))
	for _, body := range []string{`{}`, `{"title":"x","callback_url":"ftp://x"}`, `not json`} {
		req, _ := http.NewRequest("POST", h.http.URL+"/api/webhook/approvals", bytesReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, _ := http.DefaultClient.Do(req)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s 应 400, 得到 %d", body, resp.StatusCode)
		}
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
```
`bytesReader` 放进 `api_test.go`：`func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }`。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/ -run Webhook`
Expected: FAIL（404）。

- [ ] **Step 3: 写实现**

`internal/hub/webhook.go`（并删除 `requests.go` 里的 `callbackWebhook` 占位）:
```go
package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/policy"
)

type webhookCallback struct {
	URL   string `json:"url"`
	Token string `json:"token"`
}

func (s *Server) authWebhook(r *http.Request) bool {
	want, ok, _ := s.store.GetSetting("webhook_token_hash")
	if !ok {
		return false
	}
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return false
	}
	return store.HashToken(strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))) == want
}

// handleWebhookIngest 收外部系统的审批请求。
func (s *Server) handleWebhookIngest(w http.ResponseWriter, r *http.Request) {
	if !s.authWebhook(r) {
		writeErr(w, http.StatusUnauthorized, "来源令牌无效")
		return
	}
	var body struct {
		Title         string `json:"title"`
		Description   string `json:"description"`
		CallbackURL   string `json:"callback_url"`
		CallbackToken string `json:"callback_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil || body.Title == "" || body.CallbackURL == "" {
		writeErr(w, http.StatusBadRequest, "需要 title 与 callback_url")
		return
	}
	u, err := url.Parse(body.CallbackURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		writeErr(w, http.StatusBadRequest, "callback_url 必须是 http 或 https")
		return
	}
	cb, _ := json.Marshal(webhookCallback{URL: body.CallbackURL, Token: body.CallbackToken})
	req := store.Request{ID: store.NewID(), MachineID: "webhook", Kind: "webhook", ToolName: "webhook",
		Payload: policy.Payload{Type: "other", Tool: "webhook", DiffPreview: body.Description}, Risk: "unknown",
		State: "pending", Title: body.Title, Callback: string(cb)}
	if err := s.store.InsertRequest(req); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit("webhook", "system", req.ID, "request_pending", map[string]string{"title": body.Title})
	s.clients.broadcast("request_new", s.view(req))
	writeJSON(w, http.StatusOK, map[string]string{"id": req.ID})
}

// callbackWebhook 在后台把决定回传给来源，最多三次。
func (s *Server) callbackWebhook(r store.Request) {
	var cb webhookCallback
	if json.Unmarshal([]byte(r.Callback), &cb) != nil || cb.URL == "" {
		return
	}
	decision := "deny"
	if r.State == "approved" {
		decision = "allow"
	}
	body, _ := json.Marshal(map[string]string{"id": r.ID, "decision": decision, "note": r.DecisionNote})
	go func() {
		client := &http.Client{Timeout: 10 * time.Second}
		var lastErr string
		for attempt := 1; attempt <= 3; attempt++ {
			req, _ := http.NewRequest("POST", cb.URL, bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if cb.Token != "" {
				req.Header.Set("Authorization", "Bearer "+cb.Token)
			}
			resp, err := client.Do(req)
			if err == nil && resp.StatusCode < 300 {
				resp.Body.Close()
				s.audit("webhook", "system", r.ID, "webhook_callback", map[string]any{"ok": true, "attempt": attempt})
				return
			}
			if err != nil {
				lastErr = err.Error()
			} else {
				lastErr = resp.Status
				resp.Body.Close()
			}
			time.Sleep(2 * time.Second)
		}
		s.audit("webhook", "system", r.ID, "webhook_callback", map[string]any{"ok": false, "error": lastErr})
	}()
}
```
`routes()` 加 `s.mux.HandleFunc("POST /api/webhook/approvals", s.handleWebhookIngest)`。`requestView` 的 JSON 里的 `title` 来自 Task 9 给 `store.Request` 加的 json 标签。

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/hub/... -race -v`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/webhook.go internal/hub/requests.go internal/hub/server.go internal/hub/webhook_test.go internal/hub/api_test.go
git commit -m "feat(hub): webhook 来源接入与决定回调"
```

---

### Task 11: 守护进程侧的契约扩展：待决请求字段、规则替换、审计转发、machine.json

**Files:**
- Modify: `internal/agent/requests.go`（`PendingRequest` 加字段；`Pending.List()`）
- Modify: `internal/agent/server.go`（填新字段；`AuditSink`）
- Modify: `internal/agent/rules.go`（`Replace`、`Version`）
- Modify: `internal/agent/config.go`（`MachinePath`、`HubGrace`、`AgentVersion`）
- Create: `internal/agent/machine.go`
- Create: `internal/agent/contract_test.go`

**Interfaces:**
- `PendingRequest` 新增 `ProjectRoot string`、`Redacted policy.Payload`、`EngineReason string`；`waitDecision` 填充（`handlePermission` 里已有 `pctx` 与 `redacted`，改为传入）
- `func (p *Pending) List() []PendingRequest`（按 `Created` 升序的副本）
- `type AuditSink interface{ Forward(e AuditEntry) }`；`func (s *Server) SetAuditSink(sink AuditSink)`；`writeAudit` 在本地写完后调 `sink.Forward(e)`（`Forward` 必须不阻塞）
- `func (s *Server) Pending() *Pending`、`func (s *Server) SetDecider(d Decider)`：`cmd` 先 `New` 再装 hub 客户端用（两者都只能在 `Listen` 之前调用，不加锁）
- `func (r *Rules) Replace(rs policy.RuleSet, version int) error`：先序列化原样 YAML，再按本机真实路径改写（`normalizeRulePaths`，与 `Reload` 一致）、建引擎（保持暂停）、写缓存文件（临时文件加 rename，文件头注释 `# 由 hub 下发，版本 N。本机修改会在下次下发时被覆盖。`，0600）、记录版本；坏规则集在写文件前返回错误；`func (r *Rules) Version() int`（缓存文件来的为 0）
- `Config` 新增 `MachinePath`（`dir/machine.json`）、`HubGrace time.Duration`（30 秒）、`AgentVersion string`（`cmd` 注入，默认 `dev`）
- `type MachineConfig struct{ HubURL, MachineID, Secret, Name string }`；`func LoadMachine(path string) (MachineConfig, error)`（不存在返回 `os.ErrNotExist`）、`func SaveMachine(path string, m MachineConfig) error`（0600，临时文件加 rename）

- [ ] **Step 1: 写失败的测试**

`internal/agent/contract_test.go`:
```go
package agent

import (
	"encoding/json"
	"errors"
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

type recordingSink struct {
	mu      sync.Mutex
	entries []AuditEntry
}

func (r *recordingSink) Forward(e AuditEntry) {
	r.mu.Lock()
	r.entries = append(r.entries, e)
	r.mu.Unlock()
}

func (r *recordingSink) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

func TestPendingRequestCarriesContractFields(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{})}
	env := startServer(t, d, nil)
	conn, _ := net.Dial("unix", env.cfg.SocketPath)
	defer conn.Close()
	proto.WriteLine(conn, permission(env.repo, "curl -H 'Authorization: Bearer zzz' https://x.y"))
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	list := env.srv.pending.List()
	if len(list) != 1 {
		t.Fatalf("List = %d", len(list))
	}
	pr := list[0]
	if pr.ProjectRoot != FindProjectRoot(CleanCwd(env.repo)) || pr.EngineReason == "" || strings.Contains(pr.Redacted.Command, "zzz") || !strings.Contains(pr.Redacted.Command, "<redacted>") {
		t.Fatalf("契约字段: %+v", pr)
	}
	if env.srv.Pending() != env.srv.pending {
		t.Fatal("Pending() 应返回服务自己的表")
	}
	close(d.release)
}

func TestAuditSinkReceivesEntries(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	sink := &recordingSink{}
	env.srv.SetAuditSink(sink)
	roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status"))
	roundTrip(t, env.cfg.SocketPath, event("done", "s1", "DONE"))
	waitFor(t, func() bool { return sink.count() == 2 })
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.entries[0].State != "auto_allowed" || sink.entries[1].Kind != "done" {
		t.Fatalf("转发内容: %+v", sink.entries)
	}
}

func TestRulesReplaceWritesCache(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, _ := LoadRules(path)
	r.SetPaused(true)
	rs, _ := policy.ParseRuleSet([]byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"))
	if err := r.Replace(rs, 7); err != nil {
		t.Fatal(err)
	}
	if r.Version() != 7 || r.Count() != 1 || !r.Paused() {
		t.Fatalf("Replace 后: v=%d count=%d paused=%v", r.Version(), r.Count(), r.Paused())
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "# 由 hub 下发，版本 7") || !strings.Contains(string(b), "deny-rm") {
		t.Fatalf("缓存文件:\n%s", b)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("权限 %v", st.Mode().Perm())
	}
	bad := policy.RuleSet{Version: 1, Rules: []policy.Rule{{ID: "x", Source: "robot", Action: "allow"}}}
	if err := r.Replace(bad, 8); err == nil || r.Version() != 7 {
		t.Fatal("坏规则集应被拒且版本不变")
	}
	r2, _ := LoadRules(path)
	if r2.Count() != 1 {
		t.Fatal("重启后应从缓存加载")
	}
}

func TestMachineConfigRoundTrip(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "machine.json")
	if _, err := LoadMachine(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("不存在应 ErrNotExist: %v", err)
	}
	m := MachineConfig{HubURL: "http://127.0.0.1:8787", MachineID: "m1", Secret: "s", Name: "mac"}
	if err := SaveMachine(path, m); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("权限 %v", st.Mode().Perm())
	}
	got, err := LoadMachine(path)
	if err != nil || got != m {
		t.Fatalf("LoadMachine = %+v, %v", got, err)
	}
	var raw map[string]string
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &raw)
	if raw["hub_url"] == "" || raw["secret"] == "" {
		t.Fatalf("字段名应为 snake_case: %v", raw)
	}
}

func TestConfigHasHubFields(t *testing.T) {
	c := ConfigIn("/x")
	if c.MachinePath != "/x/machine.json" || c.HubGrace != 30*time.Second || c.AgentVersion != "dev" {
		t.Fatalf("Config = %+v", c)
	}
}
```
- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/ -run 'Contract|AuditSink|RulesReplace|MachineConfig|ConfigHasHub'`
Expected: FAIL，报 `undefined: SetAuditSink` 等。

- [ ] **Step 3: 写实现**

`internal/agent/requests.go` 的 `PendingRequest` 改为：
```go
// PendingRequest 是等待外部决定的请求。Redacted 是脱敏后的载荷，上报 hub 用它。
type PendingRequest struct {
	ID           string
	Req          proto.Request
	Risk         policy.Risk
	ProjectRoot  string
	Redacted     policy.Payload
	EngineReason string
	Created      time.Time
	Deadline     time.Time
}
```
并加：
```go
// List 返回按创建时间排序的副本，重连时上报 hub 用。
func (p *Pending) List() []PendingRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]PendingRequest, 0, len(p.m))
	for _, pr := range p.m {
		out = append(out, *pr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}
```
（导入 `sort`。）

`internal/agent/server.go`：
- `Server` 加字段 `sink AuditSink`，加
```go
// AuditSink 接收每条审计的副本（hub 上报用）。Forward 必须不阻塞。
type AuditSink interface{ Forward(e AuditEntry) }

// SetAuditSink 设置转发目标；nil 表示只写本地。
func (s *Server) SetAuditSink(sink AuditSink) { s.sink = sink }

// Pending 返回未完成请求表，hub 客户端重连时枚举用。
func (s *Server) Pending() *Pending { return s.pending }

// SetDecider 换决定来源，必须在 Listen 之前调用。
func (s *Server) SetDecider(d Decider) { s.decider = d }
```
- `writeAudit` 末尾加 `if s.sink != nil { s.sink.Forward(e) }`。
- `handlePermission` 里 `d, _ := s.evaluate(req)` 改为 `d, pctx := s.evaluate(req)`，调用改为 `s.waitDecision(ctx, id, req, d, pctx.ProjectRoot, redacted, entry)`；`waitDecision` 的签名加 `projectRoot string, redacted policy.Payload`，构造 `PendingRequest` 时填 `ProjectRoot: projectRoot, Redacted: redacted, EngineReason: d.Reason`。

`internal/agent/rules.go` 加：
```go
const hubRulesHeader = "# 由 hub 下发，版本 %d。本机修改会在下次下发时被覆盖。\n"

// Replace 用 hub 下发的规则集替换引擎并写缓存文件。缓存写的是 hub 的原样 YAML，
// 路径改写只作用于内存里的引擎（Reload 读缓存时会再改写一次，结果一致）。
func (r *Rules) Replace(rs policy.RuleSet, version int) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	b, err := rs.Marshal()
	if err != nil {
		return err
	}
	normalizeRulePaths(&rs, r.home, r.logf)
	eng, err := policy.NewEngine(rs)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(r.path, append([]byte(fmt.Sprintf(hubRulesHeader, version)), b...)); err != nil {
		return err
	}
	r.mu.Lock()
	eng.SetPaused(r.paused.Load())
	r.engine, r.count, r.version = eng, len(rs.Rules), version
	r.mu.Unlock()
	return nil
}

// writeFileAtomic 先写同目录临时文件（0600）再 rename。
func writeFileAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

// Version 返回最近一次 hub 下发的版本，缓存文件加载的为 0。
func (r *Rules) Version() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}
```
（`Rules` 结构加 `version int`；导入 `path/filepath`；`NewEngine` 已含 `Validate`，坏规则集在写文件前就返回。`machine.go` 的 `SaveMachine` 也用这个 `writeFileAtomic`。）

`internal/agent/config.go`：`Config` 加 `MachinePath string`、`HubGrace time.Duration`、`AgentVersion string`；`ConfigIn` 填 `MachinePath: filepath.Join(dir, "machine.json")`、`HubGrace: 30 * time.Second`、`AgentVersion: "dev"`。

`internal/agent/machine.go`:
```go
package agent

import (
	"encoding/json"
	"os"
)

// MachineConfig 是本机在 hub 的身份，由 railse agent enroll 写入。
type MachineConfig struct {
	HubURL    string `json:"hub_url"`
	MachineID string `json:"machine_id"`
	Secret    string `json:"secret"`
	Name      string `json:"name"`
}

// LoadMachine 读 machine.json；不存在时返回 os.ErrNotExist。
func LoadMachine(path string) (MachineConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return MachineConfig{}, err
	}
	var m MachineConfig
	if err := json.Unmarshal(b, &m); err != nil {
		return MachineConfig{}, err
	}
	return m, nil
}

// SaveMachine 以 0600 原子写入。
func SaveMachine(path string, m MachineConfig) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'))
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/agent/ ./cmd/railse/ -race`
Expected: 全部 PASS（第 2 份计划的测试不受影响）。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/requests.go internal/agent/server.go internal/agent/rules.go internal/agent/config.go internal/agent/machine.go internal/agent/contract_test.go
git commit -m "feat(agent): 待决请求带项目根与脱敏载荷，规则可由 hub 替换，审计可转发，machine.json"
```

---

### Task 12: 守护进程的 hub 客户端

**Files:**
- Create: `internal/agent/hubclient.go`
- Create: `internal/agent/hubclient_test.go`（`package agent_test`，用真实的 `internal/hub`）

**Interfaces:**
- Produces（导出）:
  - `type HubClient struct`，`func NewHubClient(cfg Config, m MachineConfig, rules *Rules, pending *Pending, logger *log.Logger) *HubClient`
  - `func (h *HubClient) Run(ctx context.Context)`：循环连接 `hubURL + "/ws/agent"`（`http`→`ws`、`https`→`wss`），头 `Authorization: Bearer <secret>`；连上发 `hello`（`pending.List()` 的摘要、`AgentVersion`、`HomeDir`）；读 `welcome` → `rules.Replace`（仅当版本不同）与 `rules.SetPaused`；再把当前全部 pending 以 `pending` 帧重发；之后读循环分发：`decision` → 唤醒等待者，`rules` → `Replace`，`paused` → `SetPaused` 并写 `state.json`（同包的 `saveState`），`ping` → `pong`，`error` → 记日志；断开后退避 1、2、4、8、16、30 秒重连
  - `func (h *HubClient) Connected() bool`、`func (h *HubClient) WaitConnected(ctx context.Context) error`
  - `func (h *HubClient) SendPending(pr PendingRequest) error`、`SendCancel(id, reason string) error`、`SendExpired(id string) error`、`SendDelivered(id string) error`（未连接返回 `ErrHubDisconnected`）
  - `func (h *HubClient) Await(id string) <-chan wire.Decision`、`func (h *HubClient) Forget(id string)`
  - `func (h *HubClient) Forward(e AuditEntry)`（实现 `AuditSink`：进队列，最多 2000 条，满了丢最旧并记一次日志；每秒或攒到 50 条批量发 `audit` 帧，未连接时积压）
  - `var ErrHubDisconnected = errors.New("未连接 hub")`
  - 包内：`func toWire(pr PendingRequest) wire.PendingRequest`

- [ ] **Step 1: 写失败的测试**

`internal/agent/hubclient_test.go`:
```go
package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hub"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func bytesReader(b []byte) *bytes.Reader { return bytes.NewReader(b) }

type hubEnv struct {
	srv  *hub.Server
	st   *store.Store
	http *httptest.Server
}

func startHub(t *testing.T) *hubEnv {
	t.Helper()
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := hub.DefaultConfig(dir)
	cfg.PingInterval = 200 * time.Millisecond
	cfg.PongTimeout = 2 * time.Second
	srv, err := hub.New(cfg, st, log.New(os.Stderr, "hub ", 0))
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hs.Close(); st.Close() })
	return &hubEnv{srv: srv, st: st, http: hs}
}

func enroll(t *testing.T, h *hubEnv) agent.MachineConfig {
	t.Helper()
	tok, _ := h.st.CreateEnrollToken(time.Minute)
	body, _ := json.Marshal(map[string]string{"token": tok, "name": "mac", "home_dir": "/home/u"})
	resp, err := http.Post(h.http.URL+"/api/enroll", "application/json", bytesReader(body))
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("enroll: %v %v", err, resp)
	}
	var out struct {
		MachineID string `json:"machine_id"`
		Secret    string `json:"secret"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	return agent.MachineConfig{HubURL: h.http.URL, MachineID: out.MachineID, Secret: out.Secret, Name: "mac"}
}

type clientEnv struct {
	c       *agent.HubClient
	cfg     agent.Config
	rules   *agent.Rules
	pending *agent.Pending
}

func newClient(t *testing.T, h *hubEnv, m agent.MachineConfig) clientEnv {
	t.Helper()
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := agent.ConfigIn(dir)
	cfg.HomeDir = "/home/u"
	cfg.EnsureDirs()
	rules, _ := agent.LoadRules(cfg.RulesPath)
	pending := agent.NewPendingForTest()
	c := agent.NewHubClient(cfg, m, rules, pending, log.New(os.Stderr, "agent ", 0))
	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)
	t.Cleanup(cancel)
	return clientEnv{c: c, cfg: cfg, rules: rules, pending: pending}
}

func waitConnected(t *testing.T, c *agent.HubClient) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.WaitConnected(ctx); err != nil {
		t.Fatal("没有连上 hub")
	}
}

func TestHubClientConnectsAndAppliesRules(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m)
	c, rules, cfg := env.c, env.rules, env.cfg
	waitConnected(t, c)
	if rules.Version() != 1 || rules.Count() != 2 {
		t.Fatalf("welcome 应下发规则: v=%d n=%d", rules.Version(), rules.Count())
	}
	rs, _ := policy.ParseRuleSet([]byte("version: 1\nrules:\n  - id: only\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n"))
	h.st.ReplaceRules(rs)
	h.srv.PushRulesForTest()
	deadline := time.Now().Add(5 * time.Second)
	for rules.Version() != 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if rules.Version() != 2 || rules.Count() != 1 {
		t.Fatalf("规则推送未生效: v=%d n=%d", rules.Version(), rules.Count())
	}
	h.srv.SetPausedForTest(true)
	deadline = time.Now().Add(5 * time.Second)
	for !rules.Paused() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !rules.Paused() {
		t.Fatal("hub 暂停应下发到守护进程")
	}
	if b, _ := os.ReadFile(cfg.StatePath); !strings.Contains(string(b), `"paused":true`) {
		t.Fatalf("state.json 应记下暂停: %q", b)
	}
}

func TestHubClientPendingDecisionAndDelivered(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m)
	c, pending := env.c, env.pending
	waitConnected(t, c)
	pr := agent.PendingRequest{ID: "r1", Req: proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1", TurnID: "t1", Cwd: "/p"}, ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push", Cwd: "/p"}, DeadlineSeconds: 60},
		Risk: policy.RiskOutward, ProjectRoot: "/p", Redacted: policy.Payload{Type: policy.PayloadShell, Command: "git push", Cwd: "/p"}, EngineReason: "无规则命中", Created: time.Now(), Deadline: time.Now().Add(time.Minute)}
	pending.Add(&pr)
	ch := c.Await("r1")
	if err := c.SendPending(pr); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if n, _ := h.st.CountPending(); n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hub 没有登记请求")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: "r1", Action: "allow", Note: "ok"}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.Action != "allow" || d.Reason != "ok" {
			t.Fatalf("decision = %+v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("没有收到决定")
	}
	c.SendDelivered("r1")
	deadline = time.Now().Add(5 * time.Second)
	for {
		r, _ := h.st.GetRequest("r1")
		if r.DeliveredAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hub 没有记录送达")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHubClientReconnectsAndResyncs(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m)
	c, pending := env.c, env.pending
	waitConnected(t, c)
	pr := agent.PendingRequest{ID: "r1", Req: proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: "/p"}, ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push", Cwd: "/p"}}, Risk: policy.RiskOutward, Redacted: policy.Payload{Type: policy.PayloadShell, Command: "git push"}, Created: time.Now(), Deadline: time.Now().Add(time.Minute)}
	pending.Add(&pr)
	ch := c.Await("r1")
	c.SendPending(pr)
	deadline := time.Now().Add(5 * time.Second)
	for n, _ := h.st.CountPending(); n != 1 && time.Now().Before(deadline); n, _ = h.st.CountPending() {
		time.Sleep(20 * time.Millisecond)
	}
	h.srv.DisconnectForTest(m.MachineID) // 模拟网络断开
	// 断线期间决定
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: "r1", Action: "deny"}); err != nil {
		t.Fatal(err)
	}
	select {
	case d := <-ch:
		if d.Action != "deny" {
			t.Fatalf("重连后应补到决定: %+v", d)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("重连后没有补到决定")
	}
}

func TestHubClientForwardsAudit(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	c := newClient(t, h, m).c
	waitConnected(t, c)
	p := policy.Payload{Type: policy.PayloadShell, Command: "ls"}
	c.Forward(agent.AuditEntry{TS: time.Now(), RequestID: "auto1", Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1"}, ToolName: "Bash", Payload: &p, Risk: "read", Action: "allow", State: "auto_allowed", RuleID: "builtin-readonly"})
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := h.st.GetRequest("auto1"); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("审计没有到达 hub")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHubClientSendsWithoutConnection(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfg := agent.ConfigIn(dir)
	cfg.EnsureDirs()
	rules, _ := agent.LoadRules(cfg.RulesPath)
	c := agent.NewHubClient(cfg, agent.MachineConfig{HubURL: "http://127.0.0.1:1", MachineID: "m", Secret: "s"}, rules, agent.NewPendingForTest(), log.New(os.Stderr, "", 0))
	if c.Connected() {
		t.Fatal("未运行时不应已连接")
	}
	if err := c.SendPending(agent.PendingRequest{ID: "x"}); err == nil {
		t.Fatal("未连接应报错")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := c.WaitConnected(ctx); err == nil {
		t.Fatal("WaitConnected 应超时")
	}
}
```
测试辅助：`agent.NewPendingForTest()` 放在 `internal/agent/testing.go`（正式文件，不是 `_test.go`：`agent_test` 外部测试包能用同包的 `export_test.go`，但 `cmd/railse` 的测试也要用它，所以做成正式导出，注释“仅供测试”）；hub 侧 `internal/hub/testing.go` 提供 `PushRulesForTest`、`DisconnectForTest`、`SetPausedForTest`，理由相同（`internal/agent` 的外部测试包导入 `internal/hub` 时看不到后者的 `export_test.go`）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/ -run HubClient`
Expected: FAIL，报 `undefined: agent.HubClient`。

- [ ] **Step 3: 写实现**

`internal/agent/hubclient.go`:
```go
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/policy"
)

// ErrHubDisconnected 表示此刻没有到 hub 的连接。
var ErrHubDisconnected = errors.New("未连接 hub")

var backoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}

// HubClient 维护到 hub 的一条 WebSocket 长连接。
type HubClient struct {
	cfg     Config
	machine MachineConfig
	rules   *Rules
	pending *Pending
	log     *log.Logger

	mu        sync.Mutex
	conn      *websocket.Conn
	ready     chan struct{} // 连上时关闭；断开时换新的
	waiters   map[string]chan wire.Decision
	auditQ    []json.RawMessage
	auditDrop bool
}

// NewHubClient 组装客户端，不发起连接。
func NewHubClient(cfg Config, m MachineConfig, rules *Rules, pending *Pending, logger *log.Logger) *HubClient {
	return &HubClient{cfg: cfg, machine: m, rules: rules, pending: pending, log: logger, ready: make(chan struct{}), waiters: map[string]chan wire.Decision{}}
}

func (h *HubClient) wsURL() string {
	u := h.machine.HubURL
	u = strings.Replace(u, "https://", "wss://", 1)
	u = strings.Replace(u, "http://", "ws://", 1)
	return strings.TrimRight(u, "/") + "/ws/agent"
}

// Connected 报告当前是否连着。
func (h *HubClient) Connected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn != nil
}

// WaitConnected 等到连上或 ctx 结束。
func (h *HubClient) WaitConnected(ctx context.Context) error {
	for {
		h.mu.Lock()
		conn, ready := h.conn, h.ready
		h.mu.Unlock()
		if conn != nil {
			return nil
		}
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Run 一直重连直到 ctx 取消。
func (h *HubClient) Run(ctx context.Context) {
	attempt := 0
	for {
		err := h.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			h.log.Printf("hub 连接结束: %v", err)
		}
		wait := backoff[min(attempt, len(backoff)-1)]
		attempt++
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return
		}
	}
}

// session 跑一次连接的完整生命周期。
func (h *HubClient) session(ctx context.Context) error {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	conn, _, err := websocket.Dial(dialCtx, h.wsURL(), &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + h.machine.Secret}}})
	cancel()
	if err != nil {
		return err
	}
	conn.SetReadLimit(wire.MaxFrame)
	defer conn.Close(websocket.StatusNormalClosure, "")

	var summaries []wire.PendingSummary
	for _, pr := range h.pending.List() {
		summaries = append(summaries, wire.PendingSummary{RequestID: pr.ID, CreatedAt: pr.Created})
	}
	if err := writeFrame(ctx, conn, wire.Hello{T: wire.THello, MachineID: h.machine.MachineID, AgentVersion: h.cfg.AgentVersion, HomeDir: h.cfg.HomeDir, Pending: summaries}); err != nil {
		return err
	}
	readCtx, cancelRead := context.WithTimeout(ctx, 10*time.Second)
	_, first, err := conn.Read(readCtx)
	cancelRead()
	if err != nil {
		return err
	}
	var welcome wire.Welcome
	if k, _ := wire.Kind(first); k != wire.TWelcome || wire.Unmarshal(first, &welcome) != nil {
		return errors.New("第一帧不是 welcome")
	}
	h.applyRules(welcome.RulesVersion, welcome.RulesYAML)
	h.applyPaused(welcome.Paused)

	h.mu.Lock()
	h.conn = conn
	close(h.ready)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.conn = nil
		h.ready = make(chan struct{})
		h.mu.Unlock()
	}()
	h.log.Printf("已连接 hub %s，规则版本 %d", h.machine.HubURL, welcome.RulesVersion)

	// 重发本机全部 pending，hub 对已知 id 不重复登记
	for _, pr := range h.pending.List() {
		writeFrame(ctx, conn, toWire(pr))
	}

	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go h.auditLoop(sctx, conn)

	for {
		_, b, err := conn.Read(sctx)
		if err != nil {
			return err
		}
		kind, err := wire.Kind(b)
		if err != nil {
			continue
		}
		switch kind {
		case wire.TPing:
			writeFrame(sctx, conn, wire.Pong{T: wire.TPong})
		case wire.TDecision:
			var d wire.Decision
			if wire.Unmarshal(b, &d) == nil {
				h.mu.Lock()
				ch := h.waiters[d.RequestID]
				h.mu.Unlock()
				if ch != nil {
					select {
					case ch <- d:
					default:
					}
				} else {
					// 没人等（钩子已消失），告诉 hub 已收到以免重复补发
					writeFrame(sctx, conn, wire.Delivered{T: wire.TDelivered, RequestID: d.RequestID})
				}
			}
		case wire.TRules:
			var r wire.Rules
			if wire.Unmarshal(b, &r) == nil {
				h.applyRules(r.Version, r.YAML)
			}
		case wire.TPaused:
			var p wire.Paused
			if wire.Unmarshal(b, &p) == nil {
				h.applyPaused(p.Paused)
			}
		case wire.TError:
			var e wire.Error
			wire.Unmarshal(b, &e)
			h.log.Printf("hub 报错: %s", e.Message)
		}
	}
}

func (h *HubClient) applyRules(version int, yamlText string) {
	if version == h.rules.Version() {
		return
	}
	rs, err := policy.ParseRuleSet([]byte(yamlText))
	if err != nil {
		h.log.Printf("hub 下发的规则无效，保留本地: %v", err)
		return
	}
	if err := h.rules.Replace(rs, version); err != nil {
		h.log.Printf("应用 hub 规则失败: %v", err)
		return
	}
	h.log.Printf("规则更新到版本 %d，%d 条", version, len(rs.Rules))
}

// applyPaused 以 hub 的总开关为准，并写 state.json 让重启后 LoadState 得到同样的值。
func (h *HubClient) applyPaused(p bool) {
	if h.rules.Paused() == p {
		return
	}
	h.rules.SetPaused(p)
	if err := saveState(h.cfg.StatePath, state{Paused: p}); err != nil {
		h.log.Printf("写暂停状态失败: %v", err)
	}
	h.log.Printf("hub 设置总开关: paused=%v", p)
}

func writeFrame(ctx context.Context, conn *websocket.Conn, v any) error {
	b, err := wire.Marshal(v)
	if err != nil {
		return err
	}
	wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return conn.Write(wctx, websocket.MessageText, b)
}

func (h *HubClient) send(v any) error {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn == nil {
		return ErrHubDisconnected
	}
	return writeFrame(context.Background(), conn, v)
}

func toWire(pr PendingRequest) wire.PendingRequest {
	return wire.PendingRequest{T: wire.TPending, RequestID: pr.ID, Session: pr.Req.Session, ToolName: pr.Req.ToolName, Payload: pr.Redacted,
		Risk: pr.Risk.String(), ProjectRoot: pr.ProjectRoot, EngineReason: pr.EngineReason, DeadlineSeconds: int(time.Until(pr.Deadline).Seconds()), CreatedAt: pr.Created.UTC()}
}

// SendPending 送一条待决请求。
func (h *HubClient) SendPending(pr PendingRequest) error { return h.send(toWire(pr)) }

// SendCancel 告诉 hub 钩子已消失。
func (h *HubClient) SendCancel(id, reason string) error {
	return h.send(wire.Cancel{T: wire.TCancel, RequestID: id, Reason: reason})
}

// SendExpired 告诉 hub 本机等待已过期。
func (h *HubClient) SendExpired(id string) error { return h.send(wire.Expired{T: wire.TExpired, RequestID: id}) }

// SendDelivered 告诉 hub 决定已交给钩子。
func (h *HubClient) SendDelivered(id string) error {
	return h.send(wire.Delivered{T: wire.TDelivered, RequestID: id})
}

// Await 登记一个等待者；决定到达时从返回的通道读到。
func (h *HubClient) Await(id string) <-chan wire.Decision {
	ch := make(chan wire.Decision, 1)
	h.mu.Lock()
	h.waiters[id] = ch
	h.mu.Unlock()
	return ch
}

// Forget 注销等待者。
func (h *HubClient) Forget(id string) {
	h.mu.Lock()
	delete(h.waiters, id)
	h.mu.Unlock()
}

// Forward 实现 AuditSink：进队列，不阻塞。
func (h *HubClient) Forward(e AuditEntry) {
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.auditQ) >= 2000 {
		h.auditQ = h.auditQ[1:]
		if !h.auditDrop {
			h.auditDrop = true
			h.log.Printf("审计积压超过 2000 条，开始丢弃最旧的")
		}
	}
	h.auditQ = append(h.auditQ, b)
}

// auditLoop 每秒或攒够 50 条就发一批。
func (h *HubClient) auditLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
		for {
			h.mu.Lock()
			n := min(len(h.auditQ), 50)
			batch := append([]json.RawMessage(nil), h.auditQ[:n]...)
			h.mu.Unlock()
			if n == 0 {
				break
			}
			if err := writeFrame(ctx, conn, wire.Audit{T: wire.TAudit, Entries: batch}); err != nil {
				return
			}
			h.mu.Lock()
			h.auditQ = h.auditQ[n:]
			h.auditDrop = false
			h.mu.Unlock()
			if n < 50 {
				break
			}
		}
	}
}
```
`internal/agent/testing.go`:
```go
package agent

// NewPendingForTest 仅供其他包的测试用，不要在生产代码调用。
func NewPendingForTest() *Pending { return newPending() }
```
`internal/hub/testing.go`:
```go
package hub

// 以下三个方法仅供其他包的测试用，不要在生产代码调用。

// PushRulesForTest 把当前规则快照推给全部在线守护进程。
func (s *Server) PushRulesForTest() { s.pushRules() }

// DisconnectForTest 断开某台机器的连接，模拟网络中断。
func (s *Server) DisconnectForTest(id string) { s.agents.disconnect(id) }

// SetPausedForTest 设置总开关并下发。
func (s *Server) SetPausedForTest(p bool) { s.setPaused(p) }
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/agent/ -race -run HubClient -v`
Expected: 5 个测试 PASS。`TestHubClientReconnectsAndResyncs` 最多等 10 秒（退避 1 秒后重连）。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/hubclient.go internal/agent/hubclient_test.go internal/agent/testing.go internal/hub/testing.go
git commit -m "feat(agent): hub 客户端，重连对齐、规则下发、决定接收与审计批量上报"
```

---

### Task 13: HubDecider

**Files:**
- Create: `internal/agent/hubdecider.go`
- Create: `internal/agent/hubdecider_test.go`（`package agent_test`）

**Interfaces:**
- `type HubDecider struct{ Client *HubClient; Grace time.Duration; Log *log.Logger }`，`func (d *HubDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error)`：
  1. `pr.Req.Session.Tool == "gemini"` → `Outcome{Action: "passthrough", Reason: "Gemini 只能拒绝不能放行，交回原生确认框", DecidedBy: "gemini"}`
  2. `WaitConnected` 最多 `Grace`（受 ctx 约束）；连不上 → `Outcome{passthrough, "连不上 hub，交回终端处理", "hub_unreachable"}`
  3. `Await(pr.ID)`，`SendPending`；发送失败 → 同上 `hub_unreachable`
  4. 等：收到决定 → `SendDelivered` → `Outcome{Action, Reason, RuleID, DecidedBy}`；ctx 结束 → 原因是 `errHookGone` 发 `SendCancel(id, "terminal")`，是超时发 `SendExpired`，返回 `ctx.Err()`
  5. 断线期间不放弃：等待者保留，重连后 hub 补发
- `ErrHookGone` 需要被外部测试包观察吗？不需要；测试通过 hub 的审计状态间接验证

- [ ] **Step 1: 写失败的测试**

`internal/agent/hubdecider_test.go`:
```go
package agent_test

import (
	"context"
	"log"
	"os"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hub"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func pendingFor(tool, id string) agent.PendingRequest {
	return agent.PendingRequest{ID: id, Req: proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: tool, CLISessionID: "s1", TurnID: "t1", Cwd: "/p"}, ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "git push", Cwd: "/p"}, DeadlineSeconds: 60},
		Risk: policy.RiskOutward, ProjectRoot: "/p", Redacted: policy.Payload{Type: policy.PayloadShell, Command: "git push"}, EngineReason: "无规则命中", Created: time.Now(), Deadline: time.Now().Add(time.Minute)}
}

func TestHubDeciderGeminiPassthrough(t *testing.T) {
	d := &agent.HubDecider{Client: nil, Grace: time.Second, Log: log.New(os.Stderr, "", 0)}
	out, err := d.Decide(context.Background(), pendingFor("gemini", "g1"))
	if err != nil || out.Action != "passthrough" || out.DecidedBy != "gemini" {
		t.Fatalf("Gemini 应直接交回: %+v %v", out, err)
	}
}

func TestHubDeciderUnreachable(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfg := agent.ConfigIn(dir)
	cfg.EnsureDirs()
	rules, _ := agent.LoadRules(cfg.RulesPath)
	c := agent.NewHubClient(cfg, agent.MachineConfig{HubURL: "http://127.0.0.1:1", MachineID: "m", Secret: "s"}, rules, agent.NewPendingForTest(), log.New(os.Stderr, "", 0))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)
	d := &agent.HubDecider{Client: c, Grace: 300 * time.Millisecond, Log: log.New(os.Stderr, "", 0)}
	start := time.Now()
	out, err := d.Decide(context.Background(), pendingFor("claude", "r1"))
	if err != nil || out.Action != "passthrough" || out.DecidedBy != "hub_unreachable" {
		t.Fatalf("连不上应交回终端: %+v %v", out, err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("应在 Grace 后很快返回")
	}
}

func TestHubDeciderWaitsForUserDecision(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m)
	c, pending := env.c, env.pending
	waitConnected(t, c)
	d := &agent.HubDecider{Client: c, Grace: time.Second, Log: log.New(os.Stderr, "", 0)}
	pr := pendingFor("claude", "r1")
	pending.Add(&pr)
	type result struct {
		out agent.Outcome
		err error
	}
	done := make(chan result, 1)
	go func() {
		out, err := d.Decide(context.Background(), pr)
		done <- result{out, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for n, _ := h.st.CountPending(); n != 1 && time.Now().Before(deadline); n, _ = h.st.CountPending() {
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: "r1", Action: "allow", Note: "手机批准"}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.out.Action != "allow" || r.out.Reason != "手机批准" || r.out.DecidedBy != "user" {
			t.Fatalf("结论 = %+v %v", r.out, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Decide 没有返回")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		r, _ := h.st.GetRequest("r1")
		if r.DeliveredAt != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("应报告送达")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestHubDeciderCancelAndExpire(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	env := newClient(t, h, m)
	c, pending := env.c, env.pending
	waitConnected(t, c)
	d := &agent.HubDecider{Client: c, Grace: time.Second, Log: log.New(os.Stderr, "", 0)}
	// 取消：模拟钩子消失
	pr := pendingFor("claude", "r1")
	pending.Add(&pr)
	ctx, cancel := context.WithCancelCause(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel(agent.ErrHookGoneForTest())
	}()
	if _, err := d.Decide(ctx, pr); err == nil {
		t.Fatal("取消应返回错误")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := h.st.GetRequest("r1")
		if err == nil && r.State == "cancelled" && r.DecidedBy == "terminal" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub 应记 cancelled/terminal, 得到 %+v", r)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// 过期
	pr2 := pendingFor("claude", "r2")
	pending.Add(&pr2)
	dctx, dcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer dcancel()
	if _, err := d.Decide(dctx, pr2); err == nil {
		t.Fatal("过期应返回错误")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		r, err := h.st.GetRequest("r2")
		if err == nil && r.State == "expired" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("hub 应记 expired, 得到 %+v", r)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
```
`agent.ErrHookGoneForTest()` 加到 `internal/agent/testing.go`：`func ErrHookGoneForTest() error { return errHookGone }`（同样注明仅供测试）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/ -run HubDecider`
Expected: FAIL，报 `undefined: agent.HubDecider`。

- [ ] **Step 3: 写实现**

`internal/agent/hubdecider.go`:
```go
package agent

import (
	"context"
	"errors"
	"log"
	"time"
)

// HubDecider 把引擎答 ask 的请求送到 hub 等人决定。
type HubDecider struct {
	Client *HubClient
	Grace  time.Duration // 等待连接的上限，设计文档 3.2 的 30 秒
	Log    *log.Logger
}

func unreachable(reason string) Outcome {
	return Outcome{Action: "passthrough", Reason: reason, DecidedBy: "hub_unreachable"}
}

// Decide 实现 Decider。
func (d *HubDecider) Decide(ctx context.Context, pr PendingRequest) (Outcome, error) {
	if pr.Req.Session.Tool == "gemini" {
		return Outcome{Action: "passthrough", Reason: "Gemini 只能拒绝不能放行，交回原生确认框", DecidedBy: "gemini"}, nil
	}
	if d.Client == nil {
		return unreachable("没有配置 hub，交回终端处理"), nil
	}
	gctx, cancel := context.WithTimeout(ctx, d.Grace)
	err := d.Client.WaitConnected(gctx)
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return Outcome{}, ctx.Err()
		}
		return unreachable("连不上 hub，交回终端处理"), nil
	}
	ch := d.Client.Await(pr.ID)
	defer d.Client.Forget(pr.ID)
	if err := d.Client.SendPending(pr); err != nil {
		return unreachable("发送到 hub 失败，交回终端处理"), nil
	}
	select {
	case dec := <-ch:
		if err := d.Client.SendDelivered(pr.ID); err != nil {
			d.Log.Printf("报告送达 %s 失败: %v", pr.ID, err)
		}
		return Outcome{Action: dec.Action, Reason: dec.Reason, RuleID: dec.RuleID, DecidedBy: dec.DecidedBy}, nil
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), errHookGone) {
			d.Client.SendCancel(pr.ID, "terminal")
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			d.Client.SendExpired(pr.ID)
		} else {
			d.Client.SendCancel(pr.ID, "shutdown")
		}
		return Outcome{}, ctx.Err()
	}
}
```
`SendCancel`/`SendExpired` 在断线时会失败，没关系：重连后 hello 的 pending 列表里不再有这条，hub 按 `agent_restart` 取消。

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/agent/ -race -v`
Expected: 包内全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/hubdecider.go internal/agent/hubdecider_test.go internal/agent/testing.go
git commit -m "feat(agent): HubDecider，经 hub 等待手机决定，Gemini 直通，连不上即交回终端"
```

---

### Task 14: 命令行：`railse hub` 与 `railse agent enroll`，守护进程接 hub

**Files:**
- Create: `cmd/railse/hub.go`
- Create: `cmd/railse/hub_test.go`
- Modify: `cmd/railse/main.go`（分发 `hub`；用法文本）
- Modify: `cmd/railse/agent.go`（`enroll` 子命令；`runAgentServe` 有 `machine.json` 时接 hub）
- Modify: `cmd/railse/agent_test.go`（接入测试）

**Interfaces:**
- `railse hub run [--dir 目录] [--listen 地址] [--secure-cookies] [--foreground]`：目录来自 `--dir`、`$RAILSE_HUB_HOME`、`~/.railse-hub`（建成 0700）；日志 `目录/hub.log`（0600，`--foreground` 同时到 stderr）；SIGINT/SIGTERM 优雅退出
- `railse hub password [--dir 目录]`：从标准输入读一行（终端下先在 stderr 提示）设为网页登录密码，至少 8 个字符；输出 `密码已设置`
- `railse hub token [--dir 目录] [--ttl 10m]`：打印一次性接入码、有效期与下一步命令
- `railse hub webhook-token [--dir 目录]`：生成来源令牌，存 SHA-256，明文只打印这一次
- `railse agent enroll --hub URL --token 接入码 [--name 名字] [--dir 目录] [--force]`：`POST /api/enroll` → 写 `machine.json`（0600）；已存在且无 `--force` 时拒绝；`--hub` 必须以 `http://` 或 `https://` 开头；`http://` 且主机不是回环地址时在 stderr 警告“明文传输密钥，只适合本机或可信内网”；名字默认主机名
- `runAgentServe`：`cfg.AgentVersion = version`；读 `machine.json`：不存在 → 照旧 `LocalDecider`，日志“未接入 hub”；读到 → `cfg.MachineID = m.MachineID`（hub 学到的 `scope: machine` 规则用 hub 的机器 id，本机引擎必须一致）→ `agent.New` → `NewHubClient(cfg, m, rules, srv.Pending(), logger)` → `srv.SetDecider(&agent.HubDecider{Client, Grace: cfg.HubGrace, Log: logger})` → `srv.SetAuditSink(client)` → `Listen` 后 `go client.Run(ctx)`，返回前等它退出
- 包内：`func runHub(args []string, stdin io.Reader, stdout, stderr io.Writer) int`、`func runHubServe(ctx context.Context, cfg hub.Config, foreground bool, stderr io.Writer) error`、`func defaultHubDir() (string, error)`、`func agentEnroll(cfg agent.Config, hubURL, token, name string, force bool, stdout, stderr io.Writer) error`
- 测试辅助（`cmd/railse/hub_test.go`，第 15 个 Task 也用）：`type testHub struct{ srv *hub.Server; st *store.Store; http *httptest.Server }`、`func startTestHub(t *testing.T) *testHub`、`func freeAddr(t *testing.T) string`、`func waitUntil(t *testing.T, cond func() bool)`

- [ ] **Step 1: 写失败的测试**

`cmd/railse/hub_test.go`:
```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/hub"
	"github.com/xufei547/railse-opc/internal/hub/store"
)

type testHub struct {
	srv  *hub.Server
	st   *store.Store
	http *httptest.Server
}

// startTestHub 起一个进程内 hub，供 enroll 与端到端测试用。
func startTestHub(t *testing.T) *testHub {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := hub.New(hub.DefaultConfig(dir), st, log.New(os.Stderr, "hub ", 0))
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { hs.Close(); st.Close() })
	return &testHub{srv: srv, st: st, http: hs}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

func TestHubPasswordTokenAndWebhookToken(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	var out, errOut bytes.Buffer
	if code := runHub([]string{"password", "--dir", dir}, strings.NewReader("short\n"), &out, &errOut); code == 0 {
		t.Fatal("短密码应失败")
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("数据目录应为 0700: %v %v", st, err)
	}
	errOut.Reset()
	if code := runHub([]string{"password", "--dir", dir}, strings.NewReader("correct horse battery\n"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "密码已设置") {
		t.Fatalf("password: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := runHub([]string{"token", "--dir", dir, "--ttl", "1m"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("token: %d %s", code, errOut.String())
	}
	tok := fieldAfter(t, out.String(), "接入码: ")
	if !strings.Contains(out.String(), "railse agent enroll --hub") {
		t.Fatalf("应提示下一步命令: %q", out.String())
	}
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.ConsumeEnrollToken(tok); err != nil {
		t.Fatalf("打印的接入码应有效: %v", err)
	}
	out.Reset()
	if code := runHub([]string{"webhook-token", "--dir", dir}, nil, &out, &errOut); code != 0 {
		t.Fatalf("webhook-token: %d %s", code, errOut.String())
	}
	wtok := fieldAfter(t, out.String(), "令牌: ")
	if got, ok, _ := st.GetSetting("webhook_token_hash"); !ok || got != store.HashToken(wtok) {
		t.Fatal("应只存令牌哈希")
	}
	if st, _ := os.Stat(filepath.Join(dir, "hub.db")); st.Mode().Perm() != 0o600 {
		t.Fatalf("hub.db 应为 0600: %v", st.Mode().Perm())
	}
}

// fieldAfter 取输出里某个前缀之后到行尾的内容。
func fieldAfter(t *testing.T, text, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("输出里没有 %q:\n%s", prefix, text)
	return ""
}

func TestHubRunServesAndStops(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hub")
	var out, errOut bytes.Buffer
	if code := runHub([]string{"password", "--dir", dir}, strings.NewReader("correct horse battery\n"), &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	addr := freeAddr(t)
	cfg := hub.DefaultConfig(dir)
	cfg.Listen = addr
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runHubServe(ctx, cfg, false, os.Stderr) }()
	waitUntil(t, func() bool {
		resp, err := http.Get("http://" + addr + "/api/status")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized
	})
	body, _ := json.Marshal(map[string]string{"password": "correct horse battery"})
	resp, err := http.Post("http://"+addr+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login: %v %v", err, resp)
	}
	req, _ := http.NewRequest("GET", "http://"+addr+"/api/status", nil)
	req.AddCookie(resp.Cookies()[0])
	resp2, err := http.DefaultClient.Do(req)
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("status: %v %v", err, resp2)
	}
	var st map[string]any
	json.NewDecoder(resp2.Body).Decode(&st)
	if st["paused"] != false {
		t.Fatalf("status = %v", st)
	}
	if fi, err := os.Stat(filepath.Join(dir, "hub.log")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("hub.log: %v %v", fi, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出应无错: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("hub 没有停下")
	}
}

func TestHubUsageAndUnknown(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runHub(nil, nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "用法") {
		t.Fatalf("无参数: %d %q", code, errOut.String())
	}
	if code := run([]string{"hub", "bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("未知子命令: %d", code)
	}
	out.Reset()
	run(nil, &out, &errOut)
	if !strings.Contains(errOut.String(), "hub") {
		t.Fatal("总用法应列出 hub")
	}
}
```

`cmd/railse/agent_test.go` 追加：
```go
func TestAgentEnrollWritesMachineJSON(t *testing.T) {
	h := startTestHub(t)
	tok, _ := h.st.CreateEnrollToken(time.Minute)
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "enroll", "--dir", cfgDir}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "--hub") {
		t.Fatalf("缺参数应报错: %d %q", code, errOut.String())
	}
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", "ftp://x", "--token", tok}, &out, &errOut); code == 0 {
		t.Fatal("非 http(s) 的 --hub 应拒绝")
	}
	errOut.Reset()
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", h.http.URL + "/", "--token", tok, "--name", "mac"}, &out, &errOut); code != 0 {
		t.Fatalf("enroll: %d %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "已接入") {
		t.Fatalf("输出: %q", out.String())
	}
	m, err := agent.LoadMachine(filepath.Join(cfgDir, "machine.json"))
	if err != nil || m.HubURL != h.http.URL || m.Name != "mac" || m.Secret == "" || m.MachineID == "" {
		t.Fatalf("machine.json: %+v %v", m, err)
	}
	if fi, _ := os.Stat(filepath.Join(cfgDir, "machine.json")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("权限 %v", fi.Mode().Perm())
	}
	ms, _ := h.st.ListMachines()
	if len(ms) != 1 || ms[0].Name != "mac" || ms[0].ID != m.MachineID {
		t.Fatalf("hub 的机器表: %+v", ms)
	}
	// 已接入过，不加 --force 应拒绝；接入码已消费，加 --force 也应被 hub 拒绝
	errOut.Reset()
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", h.http.URL, "--token", tok}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "--force") {
		t.Fatalf("重复接入: %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", h.http.URL, "--token", tok, "--force"}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "401") {
		t.Fatalf("已消费的接入码: %d %q", code, errOut.String())
	}
	if m2, _ := agent.LoadMachine(filepath.Join(cfgDir, "machine.json")); m2 != m {
		t.Fatal("失败的接入不应改写 machine.json")
	}
}

func TestAgentRunWithoutHubStillWorks(t *testing.T) {
	// 没有 machine.json：照旧本地闭环，日志说明未接入
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr) }()
	waitUntil(t, func() bool { _, err := os.Stat(filepath.Join(cfgDir, "agent.sock")); return err == nil })
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(cfgDir, "agent.log"))
	if !strings.Contains(string(b), "未接入 hub") {
		t.Fatalf("日志应说明未接入:\n%s", b)
	}
}
```
（`agent_test.go` 需要导入 `time`、`github.com/xufei547/railse-opc/internal/agent` 已有；若缺 `time` 则加。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run 'Hub|AgentEnroll|AgentRunWithoutHub'`
Expected: FAIL，报 `undefined: runHub`、`runHubServe`。

- [ ] **Step 3: 写实现**

`cmd/railse/hub.go`:
```go
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xufei547/railse-opc/internal/hub"
	"github.com/xufei547/railse-opc/internal/hub/store"
)

const hubUsage = `用法:
  railse hub run [--dir 目录] [--listen 地址] [--secure-cookies] [--foreground]   启动 hub（前台运行）
  railse hub password [--dir 目录]            从标准输入读一行设为网页登录密码（至少 8 个字符）
  railse hub token [--dir 目录] [--ttl 10m]   生成一次性接入码，给 railse agent enroll 用
  railse hub webhook-token [--dir 目录]       生成 webhook 来源令牌（明文只显示这一次）
  目录默认 $RAILSE_HUB_HOME，否则 ~/.railse-hub`

// defaultHubDir 取 hub 数据目录，与守护进程的 ~/.railse 分开。
func defaultHubDir() (string, error) {
	if d := os.Getenv("RAILSE_HUB_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到家目录: %w", err)
	}
	return filepath.Join(home, ".railse-hub"), nil
}

// runHub 处理 hub 子命令。
func runHub(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, hubUsage)
		return 2
	}
	fs := flag.NewFlagSet("hub", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "数据目录，默认 $RAILSE_HUB_HOME 或 ~/.railse-hub")
	listen := fs.String("listen", "", "监听地址，默认 127.0.0.1:8787")
	secure := fs.Bool("secure-cookies", false, "cookie 加 Secure（hub 在 TLS 反向代理后面时打开）")
	foreground := fs.Bool("foreground", false, "日志同时写到 stderr")
	ttl := fs.Duration("ttl", 10*time.Minute, "接入码有效期")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	switch args[0] {
	case "run", "password", "token", "webhook-token":
	default:
		fmt.Fprintf(stderr, "未知的 hub 子命令 %q\n\n%s\n", args[0], hubUsage)
		return 2
	}
	d := *dir
	if d == "" {
		dd, err := defaultHubDir()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		d = dd
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := os.Chmod(d, 0o700); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg := hub.DefaultConfig(d)
	if *listen != "" {
		cfg.Listen = *listen
	}
	cfg.SecureCookies = *secure
	if args[0] == "run" {
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		if err := runHubServe(ctx, cfg, *foreground, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer st.Close()
	switch args[0] {
	case "password":
		if f, ok := stdin.(*os.File); ok {
			if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
				fmt.Fprint(stderr, "输入新密码后回车（屏幕会回显；要避免回显可用 read -s 再通过管道传入）: ")
			}
		}
		line, err := bufio.NewReader(stdin).ReadString('\n')
		if err != nil && line == "" {
			fmt.Fprintln(stderr, "没有读到密码")
			return 1
		}
		srv, err := hub.New(cfg, st, log.New(io.Discard, "", 0))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := srv.SetPassword(strings.TrimRight(line, "\r\n")); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "密码已设置")
	case "token":
		tok, err := st.CreateEnrollToken(*ttl)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "接入码: %s\n有效期: %s（只能用一次）\n在要接入的机器上运行:\n  railse agent enroll --hub https://<hub 地址> --token %s\n", tok, ttl.String(), tok)
	case "webhook-token":
		tok := store.RandomToken()
		if err := st.SetSetting("webhook_token_hash", store.HashToken(tok)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "令牌: %s\n来源调用 POST /api/webhook/approvals 时带 Authorization: Bearer <令牌>；hub 只存哈希，丢了就重新生成。\n", tok)
	}
	return 0
}

// runHubServe 跑 hub 直到 ctx 取消。日志只写路径、计数与请求 id，不写载荷。
func runHubServe(ctx context.Context, cfg hub.Config, foreground bool, stderr io.Writer) error {
	logPath := filepath.Join(cfg.Dir, "hub.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	if err := logFile.Chmod(0o600); err != nil {
		return fmt.Errorf("收紧日志文件权限: %w", err)
	}
	var w io.Writer = logFile
	if foreground {
		w = io.MultiWriter(logFile, stderr)
	}
	logger := log.New(w, "railse-hub ", log.LstdFlags)
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	srv, err := hub.New(cfg, st, logger)
	if err != nil {
		return err
	}
	if _, ok, _ := st.GetSetting("password_hash"); !ok {
		logger.Printf("还没有设置网页密码，网页登录会得到 503；运行 railse hub password 设置")
	}
	logger.Printf("数据目录 %s，版本 %s", cfg.Dir, version)
	return srv.Run(ctx)
}
```
（`password_hash` 是 Task 6 `SetPassword` 写入 settings 的键名；若 Task 6 用了别的键名，以 Task 6 为准改这里。）

`cmd/railse/main.go`：usage 加 `  hub       云端服务（run、password、token、webhook-token）`，`agent` 行改为 `守护进程（run、status、pause、resume、reload、enroll）`；`switch` 加 `case "hub": return runHub(args[1:], stdinReader, stdout, stderr)`。

`cmd/railse/agent.go`：
- `agentUsage` 加一行 `  railse agent enroll --hub URL --token 接入码 [--name 名字] [--dir 目录] [--force]   接入 hub，写 machine.json`，`run` 那行注释改为“有 machine.json 时接 hub”。
- 标志集加 `hubURL := fs.String("hub", "", "hub 地址，如 https://hub.example.com")`、`token := fs.String("token", "", "railse hub token 生成的接入码")`、`name := fs.String("name", "", "本机在 hub 里的名字，默认主机名")`、`force := fs.Bool("force", false, "覆盖已有的 machine.json")`。
- `switch` 加：
```go
	case "enroll":
		if err := agentEnroll(cfg, *hubURL, *token, *name, *force, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
```
- 新函数：
```go
// agentEnroll 用一次性接入码向 hub 换取本机长期密钥，写 machine.json。
func agentEnroll(cfg agent.Config, hubURL, token, name string, force bool, stdout, stderr io.Writer) error {
	if hubURL == "" || token == "" {
		return errors.New("需要 --hub 与 --token（接入码由 hub 上的 railse hub token 生成）")
	}
	hubURL = strings.TrimRight(hubURL, "/")
	u, err := url.Parse(hubURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("--hub 必须是 http:// 或 https:// 开头的地址")
	}
	if u.Scheme == "http" {
		if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
			fmt.Fprintln(stderr, "警告: 用明文 http 接入会把本机密钥明文传给 hub，只适合本机或可信内网；公网请用 https")
		}
	}
	if _, err := agent.LoadMachine(cfg.MachinePath); err == nil && !force {
		return fmt.Errorf("%s 已存在，本机已接入过；要换 hub 或重新接入请加 --force", cfg.MachinePath)
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	if name == "" {
		name = cfg.MachineID
	}
	body, _ := json.Marshal(map[string]string{"token": token, "name": name, "home_dir": cfg.HomeDir})
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(hubURL+"/api/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("连接 hub: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("hub 拒绝接入（%d）: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		MachineID string `json:"machine_id"`
		Secret    string `json:"secret"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.MachineID == "" || out.Secret == "" {
		return errors.New("hub 的答复不完整")
	}
	if err := agent.SaveMachine(cfg.MachinePath, agent.MachineConfig{HubURL: hubURL, MachineID: out.MachineID, Secret: out.Secret, Name: name}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "已接入 %s\n机器 id: %s\n名字: %s\n写入 %s；重启 railse agent run 生效\n", hubURL, out.MachineID, name, cfg.MachinePath)
	return nil
}
```
（新增导入 `bytes`、`encoding/json`、`net`、`net/http`、`net/url`、`strings`。）
- `runAgentServe` 改动：
  - `cfg.AgentVersion = version` 放在 `EnsureDirs` 之后。
  - 在 `rules`、`audit` 之后、`agent.New` 之前：
```go
	var machine *agent.MachineConfig
	m, err := agent.LoadMachine(cfg.MachinePath)
	switch {
	case errors.Is(err, os.ErrNotExist):
		logger.Printf("未接入 hub（没有 %s），引擎定不了的请求交回终端", cfg.MachinePath)
	case err != nil:
		return fmt.Errorf("读 %s: %w", cfg.MachinePath, err)
	default:
		machine = &m
		cfg.MachineID = m.MachineID // scope: machine 的规则由 hub 用这个 id 生成
	}
	srv := agent.New(cfg, rules, audit, agent.LocalDecider{}, logger)
	var client *agent.HubClient
	if machine != nil {
		client = agent.NewHubClient(cfg, *machine, rules, srv.Pending(), logger)
		srv.SetDecider(&agent.HubDecider{Client: client, Grace: cfg.HubGrace, Log: logger})
		srv.SetAuditSink(client)
	}
```
  - `Listen` 成功、`LoadState` 之后：
```go
	hubCtx, stopHub := context.WithCancel(ctx)
	hubDone := make(chan struct{})
	if client != nil {
		go func() {
			defer close(hubDone)
			client.Run(hubCtx)
		}()
		logger.Printf("接入 hub %s，机器 %s", machine.HubURL, machine.MachineID)
	} else {
		close(hubDone)
	}
	defer func() {
		stopHub()
		<-hubDone
	}()
```
  - 原来的启动日志 `监听 %s，规则 %d 条，机器 %s` 保持。

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./cmd/railse/ -race -v -run 'Hub|AgentEnroll|AgentRunWithoutHub|AgentServe'`
Expected: 全部 PASS；第 2 份计划的 `TestAgentServeAndControl` 不受影响（没有 machine.json 时行为不变）。

- [ ] **Step 5: 提交**

```bash
git add cmd/railse/hub.go cmd/railse/hub_test.go cmd/railse/main.go cmd/railse/agent.go cmd/railse/agent_test.go
git commit -m "feat(cmd): railse hub run/password/token/webhook-token，railse agent enroll，守护进程有 machine.json 时接 hub"
```

---

### Task 15: 进程内端到端、文档、设计文档布局更新

**Files:**
- Create: `cmd/railse/e2e_hub_test.go`
- Create: `docs/hub.md`
- Modify: `docs/superpowers/specs/2026-10-21-railse-opc-design.md`（§1.3 布局：`internal/hub/store/`、`internal/hub/wire/`；`internal/proto` 一行的说明）
- Modify: `docs/agent-local-loop.md`（末尾加一段“接入 hub 后”指向 `docs/hub.md`）

- [ ] **Step 1: 写端到端测试**

`cmd/railse/e2e_hub_test.go`:
```go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hub"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/policy"
)

// sampleWithCommand 在录制样本上改 cwd 与 tool_input.command。
func sampleWithCommand(t *testing.T, rel, cwd, command string) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(sampleWithCwd(t, rel, cwd)), &doc); err != nil {
		t.Fatal(err)
	}
	doc["tool_input"] = map[string]any{"command": command}
	out, _ := json.Marshal(doc)
	return string(out)
}

// hub + 守护进程 + 钩子在一个进程里跑完整条链路：引擎定不了 → hub 登记 → 用户决定 → 钩子拿到答复。
func TestEndToEndHubApproval(t *testing.T) {
	const sentinel = "railse-sentinel-7b1c44e0"
	t.Setenv("CLAUDE_CODE_MESSAGING_TOKEN", sentinel)
	h := startTestHub(t)
	tok, _ := h.st.CreateEnrollToken(time.Minute)
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgDir := filepath.Join(dir, "h")
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", h.http.URL, "--token", tok, "--name", "mac"}, &out, &errOut); code != 0 {
		t.Fatalf("enroll: %d %s", code, errOut.String())
	}
	cfg := agent.ConfigIn(cfgDir)
	cfg.HubGrace = 5 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentServe(ctx, cfg, false, os.Stderr) }()
	t.Cleanup(func() { cancel(); <-done })
	waitUntil(t, func() bool { _, err := os.Stat(cfg.SocketPath); return err == nil })
	waitUntil(t, func() bool { ms, _ := h.st.ListMachines(); return len(ms) == 1 && ms[0].LastSeen != nil })
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)

	// 1. git push 是 outward，引擎定不了 → hub 登记 → 手机批准 → 钩子得到 allow
	type hookResult struct {
		out  string
		code int
	}
	res := make(chan hookResult, 1)
	go func() {
		o, _, c := runHookWith(t, sampleWithCwd(t, "claude/2.1.280/PermissionRequest.json", repo), "claude", "--socket", cfg.SocketPath)
		res <- hookResult{o, c}
	}()
	var pending []store.Request
	waitUntil(t, func() bool { pending, _ = h.st.ListRequests("pending", 10); return len(pending) == 1 })
	if pending[0].ToolName != "Bash" || pending[0].Risk != "outward" || !strings.Contains(pending[0].Payload.Command, "git push") || pending[0].ProjectRoot == "" {
		t.Fatalf("hub 里的请求: %+v", pending[0])
	}
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: pending[0].ID, Action: "allow", Note: "手机批准"}); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-res:
		if r.code != 0 || !strings.Contains(r.out, `"behavior":"allow"`) {
			t.Fatalf("钩子应得到 allow: %d %q", r.code, r.out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("钩子没有拿到决定")
	}
	waitUntil(t, func() bool { r, _ := h.st.GetRequest(pending[0].ID); return r.State == "approved" && r.DeliveredAt != nil })
	// 钩子拿固定文案“已在手机上批准”，备注只在 note（2026-10-22 用户决定 C 修订）
	if !auditHas(t, cfg.AuditPath, `"state":"approved"`, `"decided_by":"user"`, `"reason":"已在手机上批准"`, `"note":"手机批准"`) {
		b, _ := os.ReadFile(cfg.AuditPath)
		t.Fatalf("本机审计应记 approved/user:\n%s", b)
	}

	// 2. 自动放行的记录异步上报到 hub
	o, _, c := runHookWith(t, sampleWithCwd(t, "claude/2.1.280/PermissionRequest.interactive.json", repo), "claude", "--socket", cfg.SocketPath)
	if c != 0 || !strings.Contains(o, `"behavior":"allow"`) {
		t.Fatalf("touch 应本地放行: %d %q", c, o)
	}
	waitUntil(t, func() bool { rs, _ := h.st.ListRequests("auto_allowed", 10); return len(rs) == 1 })

	// 3. Gemini 的请求不进 hub：直接交回原生确认框，hub 没有新的 pending
	o, _, c = runHookWith(t, sampleWithCommand(t, "gemini/0.65.0/BeforeTool.json", repo, "git push origin main"), "gemini", "--socket", cfg.SocketPath)
	if c != 0 || o != "" {
		t.Fatalf("Gemini 应不作决定: %d %q", c, o)
	}
	waitUntil(t, func() bool { return auditHas(t, cfg.AuditPath, `"tool":"gemini"`, `"decided_by":"gemini"`) })
	if n, _ := h.st.CountPending(); n != 0 {
		t.Fatalf("Gemini 的请求不应在 hub 等待，pending=%d", n)
	}

	// 4. hub 下发规则：用户 deny 立刻在本机生效
	rs, _ := policy.ParseRuleSet([]byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"))
	if _, err := h.st.ReplaceRules(rs); err != nil {
		t.Fatal(err)
	}
	h.srv.PushRulesForTest()
	waitUntil(t, func() bool { b, _ := os.ReadFile(cfg.RulesPath); return strings.Contains(string(b), "deny-rm") && strings.HasPrefix(string(b), "# 由 hub 下发") })
	o, _, c = runHookWith(t, sampleWithCommand(t, "claude/2.1.280/PermissionRequest.json", repo, "rm x"), "claude", "--socket", cfg.SocketPath)
	if c != 0 || !strings.Contains(o, `"behavior":"deny"`) {
		t.Fatalf("hub 下发的 deny 应生效: %d %q", c, o)
	}
	if !auditHas(t, cfg.AuditPath, `"state":"auto_denied"`, `"rule_id":"deny-rm"`) {
		b, _ := os.ReadFile(cfg.AuditPath)
		t.Fatalf("本机审计应记 auto_denied/deny-rm:\n%s", b)
	}

	// 5. 钩子进程的环境变量不进本机审计，也不进 hub
	b, _ := os.ReadFile(cfg.AuditPath)
	if strings.Contains(string(b), sentinel) || strings.Contains(string(b), "CLAUDE_CODE_MESSAGING") {
		t.Fatal("本机审计里不能出现钩子进程的环境变量")
	}
	events, _ := h.st.ListAudit(1000, "")
	for _, e := range events {
		if strings.Contains(e.Detail, sentinel) || strings.Contains(e.Detail, "CLAUDE_CODE_MESSAGING") {
			t.Fatalf("hub 审计里不能出现钩子进程的环境变量: %s", e.Detail)
		}
	}
	all, _ := h.st.ListRequests("", 100)
	for _, r := range all {
		if strings.Contains(r.Payload.Command, sentinel) {
			t.Fatal("hub 请求里不能出现钩子进程的环境变量")
		}
	}
}

// hub 不在：守护进程等 HubGrace 后交回终端，本机审计记 state expired、decided_by hub_unreachable。
func TestEndToEndHubUnreachable(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfgDir := filepath.Join(dir, "h")
	cfg := agent.ConfigIn(cfgDir)
	cfg.EnsureDirs()
	if err := agent.SaveMachine(cfg.MachinePath, agent.MachineConfig{HubURL: "http://127.0.0.1:1", MachineID: "m", Secret: "s", Name: "x"}); err != nil {
		t.Fatal(err)
	}
	cfg.HubGrace = 500 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentServe(ctx, cfg, false, os.Stderr) }()
	t.Cleanup(func() { cancel(); <-done })
	waitUntil(t, func() bool { _, err := os.Stat(cfg.SocketPath); return err == nil })
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	start := time.Now()
	o, _, c := runHookWith(t, sampleWithCwd(t, "claude/2.1.280/PermissionRequest.json", repo), "claude", "--socket", cfg.SocketPath)
	if c != 0 || o != "" {
		t.Fatalf("连不上 hub 应交回终端: %d %q", c, o)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("应在 HubGrace 后很快交回")
	}
	if !auditHas(t, cfg.AuditPath, `"state":"expired"`, `"decided_by":"hub_unreachable"`) {
		b, _ := os.ReadFile(cfg.AuditPath)
		t.Fatalf("审计应记 expired/hub_unreachable:\n%s", b)
	}
}
```
`ListRequests("", 100)` 不按状态过滤（Task 4 已实现）。

- [ ] **Step 2: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./... -race`
Expected: 全部 PASS。`TestEndToEndHubApproval` 约 3 到 5 秒（审计批量每秒发一次）。

- [ ] **Step 3: 写 `docs/hub.md`**

内容（约 120 行，中文）：
1. **是什么**：hub 跑在云服务器上，是请求、规则、机器与审计的唯一真相；守护进程向外连它，手机网页（第 4 份计划）连它。它只能回答放行或拒绝，没有向机器下发命令的通道。
2. **启动**：
   ```bash
   railse hub password        # 从标准输入读密码；无回显写法：read -s PW; printf '%s\n' "$PW" | railse hub password
   railse hub run --listen 127.0.0.1:8787
   ```
   目录与文件：`~/.railse-hub/`（0700）、`hub.db`（0600，SQLite WAL）、`hub.log`（0600）。TLS 由反向代理（Caddy/nginx）终止，加 `--secure-cookies`；第 5 份计划提供内置 ACME。
3. **接入一台机器**：hub 上 `railse hub token` → 机器上 `railse agent enroll --hub https://… --token …` → 重启 `railse agent run`。`machine.json` 的内容与权限；吊销：`DELETE /api/machines/{id}`（网页在第 4 份）。
4. **接入后行为**：规则以 hub 为准，本机 `rules.yaml` 只是缓存（文件头有说明），`railse agent reload` 仍可临时加载本机改动，下次下发会覆盖；总开关生效值 = 本机 || hub，`railse agent pause` 只改本机一侧、不会被 hub 覆盖，也不反向同步（2026-10-22 用户决定 E 修订）；连不上 hub 时等 30 秒后交回终端（审计 `hub_unreachable`）；Gemini 的请求永远不进 hub。
5. **API 一览表**：方法、路径、认证、用途（Task 9、10 的全部路由）；`/ws/client` 帧 `{"type":"request_new|request_updated|rules_updated|status","data":…}`；`/api/webhook/approvals` 的 body、回调格式（`POST callback_url`，`Authorization: Bearer <callback_token>`，body `{"id","action","note","decided_at"}`，3 次重试）。
6. **安全要点**：存的全是哈希（机器密钥、会话 token、webhook 令牌、密码 argon2id）；cookie HttpOnly、SameSite=Lax；登录每 IP 每分钟 5 次；守护进程上报前脱敏；hub 不信任守护进程送来的路径与命令，只展示；`http://` 接入会警告。
7. **故障排查**：`hub.log` 关键行；`GET /api/status` 的 `machines[].online`；守护进程 `agent.log` 里的“已连接 hub”“hub 连接结束”。

- [ ] **Step 4: 更新设计文档布局与守护进程文档**

`docs/superpowers/specs/2026-10-21-railse-opc-design.md` §1.3 的代码块改为：
```
  internal/hub/        # hub 服务端：REST、/ws/agent、/ws/client、webhook（推送在第 4 份计划加入）
  internal/hub/store/  # SQLite 存储
  internal/hub/wire/   # 守护进程与 hub 之间的 WebSocket 帧
  internal/proto/      # 钩子与守护进程之间的一行 JSON 协议（守护进程与 hub 的帧在 internal/hub/wire）
```
（其余行不动。这是目录细分，不是行为偏离，不进 §0.6 的偏离表。）

`docs/agent-local-loop.md` 末尾加：
```
## 接入 hub 后

有 `~/.railse/machine.json` 时，引擎定不了的请求不再直接交回终端，而是送到 hub 等手机决定；规则与总开关以 hub 为准。接入步骤、行为变化与 API 见 `docs/hub.md`。
```

- [ ] **Step 5: 全量验证并提交**

Run: `gofmt -l . && go vet ./... && go test ./... -race -count=1 && go build -o /tmp/railse ./cmd/railse && /tmp/railse hub 2>&1 | head -3; rm -f /tmp/railse`
Expected: 测试全过；`railse hub` 无参数打印用法。

```bash
git add cmd/railse/e2e_hub_test.go docs/hub.md docs/agent-local-loop.md docs/superpowers/specs/2026-10-21-railse-opc-design.md
git commit -m "test: hub 与守护进程进程内端到端；docs: hub 说明、设计文档布局细分"
```

---

## 完成标准

全部满足才算第 3 份计划完成：

1. `gofmt -l .` 无输出，`go vet ./...` 无错，`go test ./... -race -count=1` 全过；`go.mod` 只多了 `modernc.org/sqlite`、`github.com/coder/websocket`、`golang.org/x/crypto` 三个直接依赖（`go mod tidy` 带进来的间接依赖不限）。
2. hub 命令可用：`railse hub password|token|webhook-token|run`；数据目录 0700、`hub.db` 与 `hub.log` 0600；SIGTERM 后 5 秒内退出。
3. 守护进程：`railse agent enroll` 写出 0600 的 `machine.json`；`railse agent run` 有它时连 hub，没有时行为与第 2 份计划完全一致（`TestAgentServeAndControl`、`TestEndToEndClaudeSamples` 等旧测试不改）。
4. 审批闭环（`TestEndToEndHubApproval`）：引擎答 ask 的 Claude/Codex 请求进 hub 的 `inbox`；`POST /api/requests/{id}/decide` 后钩子在 1 秒内拿到答复，hub 记 `approved|denied` 与 `delivered_at`；“记住”生成规则并下发到所有在线机器，本机 `rules.yaml` 被覆盖且文件头说明来源；拒绝并记住的规则 `source: user`、`action: deny`。
5. 断线与重连：守护进程断开期间做出的决定在重连后补发（`TestHubClientReconnectsAndResyncs`）；守护进程重启后 hub 里它不再持有的 pending 变 `cancelled/agent_restart`；Codex 15 分钟内重发同一请求时旧的变 `cancelled/resend`，收件箱只剩一条。
6. 边界：Gemini 请求不进 hub（`decided_by: gemini`）；连不上 hub 时 `HubGrace` 后交回终端（本机审计 `state: expired`、`decided_by: hub_unreachable`）；钩子消失 → hub `cancelled/terminal`；本机等待过期 → hub `expired/timeout`；hub 暂停 → 所有机器 `paused`，本机 `state.json` 同步。
7. 审计：本机每条审计异步上报 hub（`audit_events`），自动决定在 hub 的 `requests` 里有历史行；本机审计与 hub 里的任何记录都不含钩子进程的环境变量（哨兵测试）。
8. 安全：hub 存的机器密钥、会话 token、webhook 令牌、密码都是哈希；登录限速 5 次/分钟/IP；cookie HttpOnly + SameSite=Lax（TLS 后 Secure）；守护进程上报的载荷已脱敏；hub 对请求只能回 allow/deny；`http://` 接入打印警告。
9. webhook：`POST /api/webhook/approvals` 用来源令牌创建 `kind: webhook` 的 pending；决定后回调 `callback_url`，失败重试 3 次并审计。
10. 文档：`docs/hub.md` 覆盖启动、接入、行为变化、API、安全、排查；设计文档 §1.3 布局更新；`docs/agent-local-loop.md` 指向 hub 文档。
11. 不在本计划（留给后续，不算未完成）：TLS/ACME、Web Push、PWA 页面、开机自启、`railse doctor`、`railse agent status` 显示 hub 连接状态、本机 `pause` 反向同步到 hub。

## 自检记录

写完后对照设计文档与前两份计划逐项核对：

**设计文档覆盖**
- §1.1 三条原则：守护进程只向外连（Task 12 `Dial`，hub 没有任何到机器的连接）；hub 唯一真相（Task 8 hub 登记、Task 12 规则/暂停以 welcome 为准）；hub 只答 allow/deny（Task 8 `errBadAction`，Task 13 只把 `Action` 透传给第 2 份计划已有的“非 allow/deny 一律 passthrough”逻辑）。
- §2.1 实体：machines、enroll_tokens、client_sessions、sessions、requests、rules、audit_events、push_subscriptions、settings 九张表（Task 2 schema），`push_subscriptions` 本计划只建表。
- §2.3 状态机：pending → approved|denied|cancelled|expired（Task 4 `SetRequestState` 只允许从 pending 出发）；auto_allowed、auto_denied、passthrough 作为历史行由审计上报补齐（Task 8 `onAudit`）；`cancelled` 的 `decided_by` 取值 terminal、resend、agent_restart、shutdown（Task 8、13）。
- §3.1 正常路径与 §3.2 边界：去重窗口、重连对齐、钩子消失、过期、连不上 hub、Gemini 直通、hub 暂停，分别在 Task 8、12、13 有测试。
- §3.4 webhook：Task 10。
- §5.1 三段认证：Bearer 机器密钥（Task 6/7）、cookie 会话（Task 6）、webhook 来源令牌（Task 10）；§5.2 hub 权力：Decision 帧只有 allow/deny，守护进程不执行任何来自 hub 的命令；§5.3 数据最小化：Task 11 `Redacted` 字段、Task 12 只上报 `Redacted`，hub 存哈希。
- 第 2 份计划留下的七条接入契约（本计划开头列出）：逐条落在 Task 11（前三条）、Task 13（`errHookGone`、Gemini）、Task 8（Codex 去重）、Task 9（deny 记为 user）、Task 11/12（脱敏）。

**类型一致性**
- `wire.PendingRequest` ↔ `agent.toWire(PendingRequest)`（Task 12）↔ `store.Request`（Task 8 `onPending` 逐字段赋值）：`RequestID/ID`、`Session.Tool`、`ToolName`、`Payload`（脱敏后）、`Risk`（字符串）、`ProjectRoot`、`EngineReason`、`DeadlineSeconds`→`expires_at`、`CreatedAt`。
- `wire.Decision` ↔ `agent.Outcome`（Task 13 逐字段）↔ 第 2 份计划 `waitDecision` 对 `Outcome` 的处理（未改）。
- `wire.Audit.Entries []json.RawMessage` ↔ `agent.AuditEntry`（Task 12 `Forward` 序列化，Task 8 `onAudit` 反序列化），`wire` 不导入 `agent`。
- `hub.DecideInput`（Task 8）同时被 Task 9 的 REST、Task 12/13/15 的测试使用，字段 `RequestID, Action, Note, Remember`。
- `store.Request` 的 json 标签在 Task 9 加入，Task 9 的 `requestView` 与 Task 10 的 `title` 依赖它；Task 4 的测试不依赖 JSON 名。
- `agent.Config` 新字段 `MachinePath、HubGrace、AgentVersion`（Task 11）被 Task 12、13、14 使用；`cmd` 在 `runAgentServe` 里设 `AgentVersion = version`。
- 测试辅助导出 `agent.NewPendingForTest`、`agent.ErrHookGoneForTest`、`hub.PushRulesForTest`、`hub.DisconnectForTest`、`hub.SetPausedForTest` 都放在正式文件 `testing.go`，因为跨包的外部测试看不到 `export_test.go`。

**占位扫描**：全文无 TODO/TBD/待定；Task 7、8 的“占位函数”都在后续 Task 明确删除或替换；Task 14 对 `password_hash` 键名标注了以 Task 6 为准。

**已知取舍**
- `railse hub password` 从标准输入读，终端下会回显；无回显方式写在文档里，不引入 `golang.org/x/term`。
- 本机 `railse agent pause` 在接入 hub 后只改本机一侧，生效值 = 本机 || hub，不会被 `welcome`/`paused` 帧覆盖，也不反向同步；第 4 份计划的网页提供 hub 侧暂停（2026-10-22 用户决定 E 修订）。
- 守护进程重启后，hub 里它不再持有的 pending 全部按 `agent_restart` 取消，而不是让钩子重新等待：钩子进程早已随守护进程的 socket 关闭而得到 passthrough 或退出。
- `TestHubRunServesAndStops` 用先占后放的端口，有极小的端口竞争概率；失败时重跑即可，不为此给 `Run` 加回调。
