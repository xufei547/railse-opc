# Railse OPC v0.2.3：限时放行 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用户能从手机、终端或启动命令把一个 CLI 会话或一台机器上的一个项目在一段时间（5 分钟～24 小时）内整体放行；放行期间该范围内引擎定不了的请求全部本地 allow，不弹终端框、不进手机；到期自动失效；手机上可见可撤；审计可查。

**Architecture:** 守护进程是执行者：`internal/agent/trust.go` 维护放行表（内存 + `~/.railse/trusts.json`），`handlePermission` 在引擎答 ask 之后、送手机之前查表命中即 allow（`decided_by: trust:<id>`）；入口有三个——hub 下发（手机批准时的开关）、本机 socket 管理命令（`railse trust`）、钩子带来的 `RAILSE_TRUST` 环境变量。hub 存一份副本给手机看、建、撤（schema v6 的 `trusts` 表，`GET/DELETE /api/trusts`，decide 带 `trust`），与守护进程经 welcome 的 `trusts` 快照、新帧 `trusts`（hub → 守护进程）与 `trust`（守护进程 → hub）双向同步，守护进程按"已同步的条目不在快照里 = 被撤"合并。网页在审批面板加开关、设置页加一节、收件箱加横幅。

**Tech Stack:** Go 1.26（gofmt/lint 走 `make gofmt` / `make lint`）、React/TS、vitest、Playwright。

## Global Constraints

- 设计：`docs/superpowers/specs/2026-11-02-railse-opc-v0.2.3-trust-design.md`（本计划对 §2.5 的同步规则做了精确化：见 Task 4 的"已同步"标记）。
- `go test` 在 `RAILSE_LANG=zh` 下跑；新加的用户可见句子一律 `i18n.T` 字面量（动态键用 `i18n.Lookup`），英文进 `internal/i18n/en.go`；网页新句子一律 `t('…')` 字面量，英文进 `web/src/locales/en.ts`；两边的完整性测试会查。
- 时长上下限：5 分钟 ～ 24 小时，默认 2 小时；每台机器同时生效至多 20 条。守护进程与 hub 都校验。
- 放行只改"引擎答 ask"的结论：用户手写 deny（`source: user`）仍 deny；总开关暂停仍交回终端；提问（kind ask）不受影响。
- 不碰真实的 `~/.claude`、`~/.railse`、`~/.railse-hub`；真机验收由主会话做。
- 所有回复、提交说明、注释中文。每个任务一个提交，分支上每个提交都过 `make test`。不推送、不合并、不打 tag。
- 版本兼容：老 hub（没声明 `trust` 能力）配新守护进程——本机入口照常，守护进程不发 `trust` 帧；新 hub 配老守护进程（hello 没声明 `trust`）——网页不显示开关，`/api/trusts` 对那台机器建不了（400）。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/proto/proto.go` | `Trust` 类型、`Request.TrustFor`、`Control.Trust/TrustID`、`ControlReply.Trusts`、`Status.Trusts` |
| `internal/agent/trust.go`（新）、`trust_test.go`（新）、`server.go`、`audit.go` | 放行表：增删查、到期、落盘、命中；`handlePermission` 接入；管理命令；`RAILSE_TRUST` 首见建表 |
| `cmd/railse/trust.go`（新）、`trust_test.go`（新）、`main.go`、`hook.go`、`agent.go` | `railse trust` 子命令；钩子读 `RAILSE_TRUST`；`agent status` 多一行 |
| `internal/hookclient/client.go` | `Control` 能带参数 |
| `internal/doctor/agent.go` | `agent.trusts` 只读检查 |
| `internal/hub/wire/wire.go` | `Hello.Features`、`Welcome.Trusts`、`TTrusts`/`TTrust` 帧、`FeatureTrust` |
| `internal/hub/store/schema.sql`、`store.go`、`trusts.go`（新）、`trusts_test.go`（新） | schema v6 `trusts` 表与 CRUD、到期清理 |
| `internal/hub/trusts.go`（新）、`agents.go`、`api.go`、`requests.go`、`api_test.go` | 收 `trust` 帧、发快照、`/api/trusts`、decide 带 trust、`can_trust`、每分钟清理 |
| `internal/agent/hubclient.go`、`hubclient_test.go` | hello 声明能力、收快照、发 `trust` 帧、已同步标记 |
| `web/src/types.ts`、`api.ts`、`decide.ts`、`components/DecideSheet.tsx`、`pages/Settings.tsx`、`pages/Inbox.tsx`、`store.ts`、`locales/en.ts`、测试 | 开关、设置页一节、横幅 |
| `README.md`、`README.en.md`、`docs/hub.md`、`docs/agent-local-loop.md`、`docs/setup.md`、`docs/launch/qa.md` | 文档 |

---

### Task 1: proto 与守护进程：放行表、命中、管理命令、环境变量入口

**Files:**
- Modify: `internal/proto/proto.go`（Request、Control、ControlReply、Status）
- Create: `internal/agent/trust.go`、`internal/agent/trust_test.go`
- Modify: `internal/agent/server.go:379-430`（handlePermission）、`server.go:577-620`（handleControl、status）、`internal/agent/config.go`（TrustsPath）、`internal/agent/audit.go`（无新字段，复用 `kind: control`）
- Test: `internal/agent/server_test.go`

**Interfaces:**
- Produces:

```go
// proto
type Trust struct {
	ID         string    `json:"id"`
	MachineID  string    `json:"machine_id,omitempty"`
	Scope      string    `json:"scope"`                    // session | project
	Tool       string    `json:"tool,omitempty"`           // session
	CLISession string    `json:"cli_session_id,omitempty"` // session
	Project    string    `json:"project_root,omitempty"`   // project
	Until      time.Time `json:"until"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by"` // phone | cli | env
	Note       string    `json:"note,omitempty"`
}
const (
	TrustScopeSession = "session"
	TrustScopeProject = "project"
	TrustMin          = 5 * time.Minute
	TrustMax          = 24 * time.Hour
	TrustDefault      = 2 * time.Hour
	TrustPerMachine   = 20
)
// Request 加：TrustFor string `json:"trust_for,omitempty"`（钩子从 RAILSE_TRUST 带来的时长原文，如 "2h"）
// Control 加：Trust *Trust `json:"trust,omitempty"`（trust_set）、TrustID string `json:"trust_id,omitempty"`（trust_revoke；"all" 为全部）
// ControlReply 加：Trusts []Trust `json:"trusts,omitempty"`
// Status 加：Trusts int `json:"trusts"`
// agent
type trusts struct { … }                                   // 放行表，持 mu，now 可注入
func newTrusts(path string, now func() time.Time) (*trusts, error) // 读 trusts.json，过期的丢掉
func (t *trusts) add(tr proto.Trust) (proto.Trust, error)  // 校验、补 ID/CreatedAt、截时长、上限 20、落盘
func (t *trusts) revoke(id string) bool                    // "all" 清空
func (t *trusts) list() []proto.Trust                      // 未过期，按 Until 升序
func (t *trusts) match(sess proto.Session, projectRoot string) (proto.Trust, bool) // 命中多条取 Until 最晚
func (t *trusts) sweep() []proto.Trust                     // 清过期，返回清掉的
func ParseTrustFor(s string) (time.Duration, error)        // "30m"、"2h"、"8h"；超界截到界内；格式错返回 error
```

- 审计：命中记 `state: auto_allowed`、`decided_by: "trust:" + id`、`reason: 限时放行（<范围>，剩 <时长>）`；建/撤/到期各一条 `kind: control`、`Message: trust_set|trust_revoke|trust_expired`、`Reason` 带范围与到期时间。

- [ ] **Step 1: 写失败的单元测试**

`internal/agent/trust_test.go`：

```go
package agent

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/proto"
)

func fixedNow(t0 time.Time) func() time.Time { return func() time.Time { return t0 } }

func TestTrustsAddValidateAndClamp(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	tr, err := newTrusts(filepath.Join(t.TempDir(), "trusts.json"), fixedNow(t0))
	if err != nil {
		t.Fatal(err)
	}
	got, err := tr.add(proto.Trust{Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s1", Until: t0.Add(48 * time.Hour), CreatedBy: "cli"})
	if err != nil || got.ID == "" || !got.Until.Equal(t0.Add(proto.TrustMax)) {
		t.Fatalf("应补 ID 并把时长截到 24h：%+v %v", got, err)
	}
	if _, err := tr.add(proto.Trust{Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s2", Until: t0.Add(time.Minute)}); err == nil {
		t.Fatal("短于 5 分钟应报错")
	}
	if _, err := tr.add(proto.Trust{Scope: "machine", Until: t0.Add(time.Hour)}); err == nil {
		t.Fatal("不认识的范围应报错")
	}
	if _, err := tr.add(proto.Trust{Scope: proto.TrustScopeProject, Until: t0.Add(time.Hour)}); err == nil {
		t.Fatal("project 范围缺项目根应报错")
	}
	for i := 0; i < proto.TrustPerMachine-1; i++ {
		if _, err := tr.add(proto.Trust{Scope: proto.TrustScopeProject, Project: "/p/" + string(rune('a'+i)), Until: t0.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tr.add(proto.Trust{Scope: proto.TrustScopeProject, Project: "/p/z", Until: t0.Add(time.Hour)}); err == nil {
		t.Fatal("超过 20 条应报错")
	}
}

func TestTrustsMatchExpireRevokePersist(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	now := t0
	path := filepath.Join(t.TempDir(), "trusts.json")
	tr, _ := newTrusts(path, func() time.Time { return now })
	short, _ := tr.add(proto.Trust{Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s1", Until: t0.Add(10 * time.Minute)})
	long, _ := tr.add(proto.Trust{Scope: proto.TrustScopeProject, Project: "/home/u/proj", Until: t0.Add(2 * time.Hour)})
	sess := proto.Session{Tool: "claude", CLISessionID: "s1"}
	if m, ok := tr.match(sess, "/home/u/proj"); !ok || m.ID != long.ID {
		t.Fatalf("两条都命中时取到期最晚的：%+v %v", m, ok)
	}
	if m, ok := tr.match(proto.Session{Tool: "codex", CLISessionID: "x"}, "/home/u/proj"); !ok || m.ID != long.ID {
		t.Fatalf("项目放行对别的会话也生效：%+v %v", m, ok)
	}
	if _, ok := tr.match(proto.Session{Tool: "claude", CLISessionID: "s1"}, "/other"); !ok {
		t.Fatal("会话放行不看项目")
	}
	if _, ok := tr.match(proto.Session{Tool: "claude", CLISessionID: "s9"}, "/other"); ok {
		t.Fatal("都不命中")
	}
	now = t0.Add(11 * time.Minute)
	if _, ok := tr.match(sess, "/other"); ok {
		t.Fatal("过期的会话放行不该命中")
	}
	if gone := tr.sweep(); len(gone) != 1 || gone[0].ID != short.ID {
		t.Fatalf("sweep 应清掉过期那条：%+v", gone)
	}
	// 重新打开：只剩没过期的
	tr2, err := newTrusts(path, func() time.Time { return now })
	if err != nil || len(tr2.list()) != 1 || tr2.list()[0].ID != long.ID {
		t.Fatalf("落盘应保留未过期的：%v %+v", err, tr2.list())
	}
	if !tr2.revoke(long.ID) || len(tr2.list()) != 0 {
		t.Fatal("撤销后应为空")
	}
	if tr2.revoke("nope") {
		t.Fatal("撤不存在的返回 false")
	}
}

func TestParseTrustFor(t *testing.T) {
	cases := map[string]time.Duration{"30m": 30 * time.Minute, "2h": 2 * time.Hour, "8h": 8 * time.Hour, "25h": proto.TrustMax, "1m": proto.TrustMin, "": proto.TrustDefault}
	for in, want := range cases {
		if got, err := ParseTrustFor(in); err != nil || got != want {
			t.Errorf("%q = %v, %v，想要 %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"abc", "-2h", "2 hours"} {
		if _, err := ParseTrustFor(bad); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}
```

`internal/agent/server_test.go` 加：

```go
// 放行命中：引擎答 ask 的请求直接 allow，decided_by trust:<id>，不送 Decider；用户 deny 规则、暂停、提问不受影响。
func TestTrustAllowsUndecided(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	dec := deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return Outcome{Action: "deny", DecidedBy: "user"}, nil
	})
	env := startServer(t, dec, nil)
	sock := env.cfg.SocketPath
	// 经管理命令建一条项目放行
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(time.Hour), CreatedBy: "cli"}}), &reply)
	if !reply.OK || len(reply.Trusts) != 1 {
		t.Fatalf("建放行：%+v", reply)
	}
	id := reply.Trusts[0].ID
	var resp proto.Response
	json.Unmarshal(roundTrip(t, sock, preToolUse(env.repo, "git push origin main", "auto", "toolu_t1")), &resp)
	if resp.Decision != "allow" || resp.DecidedBy != "trust:"+id {
		t.Fatalf("硬底线在放行期间应直接 allow：%+v", resp)
	}
	pr := permission(env.repo, "rm -rf /tmp/x")
	pr.Source = proto.SourcePermissionRequest
	json.Unmarshal(roundTrip(t, sock, pr), &resp)
	if resp.Decision != "allow" || resp.DecidedBy != "trust:"+id {
		t.Fatalf("PermissionRequest 同样 allow：%+v", resp)
	}
	// 别的项目不受影响
	other := permission(t.TempDir(), "git push origin main")
	other.Source = proto.SourcePermissionRequest
	json.Unmarshal(roundTrip(t, sock, other), &resp)
	if resp.Decision == "allow" {
		t.Fatalf("别的项目不该放行：%+v", resp)
	}
	// 用户 deny 规则仍 deny（规则文件里写一条 user deny，照文件里现有的写法 writeRules(t, env, …)）
	writeUserDeny(t, env, "git push --force")
	json.Unmarshal(roundTrip(t, sock, permission(env.repo, "git push --force origin main")), &resp)
	if resp.Decision != "deny" {
		t.Fatalf("用户 deny 应压过放行：%+v", resp)
	}
	// 撤销后恢复
	json.Unmarshal(roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "trust_revoke", TrustID: id}), &reply)
	json.Unmarshal(roundTrip(t, sock, preToolUse(env.repo, "git push origin main", "auto", "toolu_t2")), &resp)
	if resp.Decision != "ask" {
		t.Fatalf("撤销后硬底线应回 ask：%+v", resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("放行期间与 PreToolUse 都不该送 Decider：%d", calls)
	}
	var hits, set, revoked int
	for _, e := range readAudit(t, env.cfg.AuditPath) {
		switch {
		case strings.HasPrefix(e.DecidedBy, "trust:") && e.State == "auto_allowed":
			hits++
		case e.Kind == "control" && e.Message == "trust_set":
			set++
		case e.Kind == "control" && e.Message == "trust_revoke":
			revoked++
		}
	}
	if hits != 2 || set != 1 || revoked != 1 {
		t.Fatalf("审计：hits=%d set=%d revoked=%d", hits, set, revoked)
	}
}

// RAILSE_TRUST：钩子带 trust_for 时，第一次见到这个会话就建一条 session 放行（created_by env），之后的请求不再看它。
func TestTrustForEnvCreatesSessionTrust(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	r := preToolUse(env.repo, "git push origin main", "auto", "toolu_e1")
	r.TrustFor = "30m"
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, r), &resp)
	if resp.Decision != "allow" || !strings.HasPrefix(resp.DecidedBy, "trust:") {
		t.Fatalf("带 trust_for 的首条请求就应放行：%+v", resp)
	}
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_list"}), &reply)
	if len(reply.Trusts) != 1 || reply.Trusts[0].Scope != proto.TrustScopeSession || reply.Trusts[0].CreatedBy != "env" {
		t.Fatalf("应有一条 env 建的会话放行：%+v", reply.Trusts)
	}
	// 同会话再来一条带不同时长的，不再建第二条
	r2 := preToolUse(env.repo, "rm -rf /tmp/x", "auto", "toolu_e2")
	r2.TrustFor = "8h"
	roundTrip(t, env.cfg.SocketPath, r2)
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_list"}), &reply)
	if len(reply.Trusts) != 1 {
		t.Fatalf("同会话不该建第二条：%+v", reply.Trusts)
	}
	// 格式错的 trust_for：不建，请求按常规处理
	env2 := startServer(t, LocalDecider{}, nil)
	r3 := preToolUse(env2.repo, "git push origin main", "auto", "toolu_e3")
	r3.TrustFor = "abc"
	json.Unmarshal(roundTrip(t, env2.cfg.SocketPath, r3), &resp)
	if resp.Decision != "ask" {
		t.Fatalf("格式错的 trust_for 不该放行：%+v", resp)
	}
}

// 暂停压过放行；提问不受放行影响。
func TestTrustDoesNotOverridePauseOrAsk(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	sock := env.cfg.SocketPath
	roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(time.Hour)}})
	roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "pause"})
	var resp proto.Response
	json.Unmarshal(roundTrip(t, sock, permission(env.repo, "git push origin main")), &resp)
	if resp.Decision != "passthrough" || resp.DecidedBy != "paused" {
		t.Fatalf("暂停时应交回终端：%+v", resp)
	}
	roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "resume"})
	// 提问照常送 Decider（LocalDecider 交回终端）
	ask := askRequest(env.repo) // 以 ask_test.go 里现有的构造 helper 为准
	json.Unmarshal(roundTrip(t, sock, ask), &resp)
	if resp.Decision != "passthrough" || strings.HasPrefix(resp.DecidedBy, "trust:") {
		t.Fatalf("提问不受放行影响：%+v", resp)
	}
}
```

`writeUserDeny`、`askRequest` 若文件里没有同名 helper，照现有写规则文件 / 构造 ask 请求的测试代码抽一个（grep `rules.yaml` 与 `Kind: "ask"` 在 `server_test.go`、`ask_test.go` 里的用法）。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/ -run 'TestTrusts|TestParseTrustFor|TestTrust' 2>&1 | head`
Expected: 编译错误（`newTrusts`、`proto.Trust` 等未定义）。

- [ ] **Step 3: 实现**

`internal/proto/proto.go`：加 `Trust` 类型与常量、`Request.TrustFor`、`Control.Trust`/`TrustID`、`ControlReply.Trusts`、`Status.Trusts`（见 Interfaces）。`Control.Op` 注释加 `trust_set、trust_list、trust_revoke`。

`internal/agent/config.go`：`Config` 加 `TrustsPath string`，`ConfigIn(dir)` 里设为 `filepath.Join(dir, "trusts.json")`。

`internal/agent/trust.go`：

```go
package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/xufei547/railse-opc/internal/proto"
)

// trusts 是本机的限时放行表：守护进程是执行者，hub 只是展示与远程控制。内存里一份，改动即原子落盘到 trusts.json；
// 启动时读回并丢掉过期的。synced 记哪些条目 hub 已经收下（见 hubclient：已同步的条目不在 hub 快照里，就是被手机撤了）。
type trusts struct {
	mu     sync.Mutex
	path   string
	now    func() time.Time
	items  map[string]proto.Trust
	synced map[string]bool
}

var (
	errTrustScope   = errors.New("放行范围只能是 session 或 project")
	errTrustTarget  = errors.New("session 范围要有 tool 与 cli_session_id，project 范围要有项目根")
	errTrustTooMany = fmt.Errorf("同时生效的放行至多 %d 条", proto.TrustPerMachine)
	errTrustTooLong = errors.New("时长格式不对（如 30m、2h、8h）")
)

func newTrusts(path string, now func() time.Time) (*trusts, error) {
	t := &trusts{path: path, now: now, items: map[string]proto.Trust{}, synced: map[string]bool{}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return t, nil
	}
	if err != nil {
		return t, fmt.Errorf("读放行表 %s: %w", path, err)
	}
	var list []proto.Trust
	if err := json.Unmarshal(b, &list); err != nil {
		return t, fmt.Errorf("放行表 %s 损坏，按空表处理: %w", path, err)
	}
	for _, tr := range list {
		if tr.Until.After(now()) {
			t.items[tr.ID] = tr
		}
	}
	return t, nil
}

// ParseTrustFor 解析时长原文：空为默认 2 小时；超界截到 5 分钟～24 小时；格式不对报错。
func ParseTrustFor(s string) (time.Duration, error) {
	if s == "" {
		return proto.TrustDefault, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, errTrustTooLong
	}
	return clampTrust(d), nil
}

func clampTrust(d time.Duration) time.Duration {
	if d < proto.TrustMin {
		return proto.TrustMin
	}
	if d > proto.TrustMax {
		return proto.TrustMax
	}
	return d
}

// add 校验并登记：补 ID 与 CreatedAt，Until 截到界内（短于 5 分钟报错，长于 24 小时截断），超过上限报错，落盘失败返回错误但条目已生效。
func (t *trusts) add(tr proto.Trust) (proto.Trust, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	switch tr.Scope {
	case proto.TrustScopeSession:
		if tr.Tool == "" || tr.CLISession == "" {
			return proto.Trust{}, errTrustTarget
		}
	case proto.TrustScopeProject:
		if tr.Project == "" {
			return proto.Trust{}, errTrustTarget
		}
	default:
		return proto.Trust{}, errTrustScope
	}
	if tr.Until.Before(now.Add(proto.TrustMin)) {
		return proto.Trust{}, errTrustTooLong
	}
	if tr.Until.After(now.Add(proto.TrustMax)) {
		tr.Until = now.Add(proto.TrustMax)
	}
	t.sweepLocked()
	if _, exists := t.items[tr.ID]; !exists && len(t.items) >= proto.TrustPerMachine {
		return proto.Trust{}, errTrustTooMany
	}
	if tr.ID == "" {
		var b [8]byte
		rand.Read(b[:])
		tr.ID = hex.EncodeToString(b[:])
	}
	if tr.CreatedAt.IsZero() {
		tr.CreatedAt = now
	}
	t.items[tr.ID] = tr
	return tr, t.saveLocked()
}

func (t *trusts) revoke(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == "all" {
		n := len(t.items)
		t.items, t.synced = map[string]proto.Trust{}, map[string]bool{}
		_ = t.saveLocked()
		return n > 0
	}
	if _, ok := t.items[id]; !ok {
		return false
	}
	delete(t.items, id)
	delete(t.synced, id)
	_ = t.saveLocked()
	return true
}

func (t *trusts) list() []proto.Trust {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.sweepLocked()
	out := make([]proto.Trust, 0, len(t.items))
	for _, tr := range t.items {
		out = append(out, tr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Until.Before(out[j].Until) })
	return out
}

// match 找命中的放行：session 范围按 Tool + CLISessionID，project 范围按项目根字符串相等；多条取 Until 最晚。
func (t *trusts) match(sess proto.Session, projectRoot string) (proto.Trust, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	var best proto.Trust
	found := false
	for _, tr := range t.items {
		if !tr.Until.After(now) {
			continue
		}
		hit := (tr.Scope == proto.TrustScopeSession && tr.Tool == sess.Tool && tr.CLISession == sess.CLISessionID) ||
			(tr.Scope == proto.TrustScopeProject && projectRoot != "" && tr.Project == projectRoot)
		if hit && (!found || tr.Until.After(best.Until)) {
			best, found = tr, true
		}
	}
	return best, found
}

// sweep 清过期的并返回它们（调用方写审计）。
func (t *trusts) sweep() []proto.Trust {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sweepLocked()
}

func (t *trusts) sweepLocked() []proto.Trust {
	now := t.now()
	var gone []proto.Trust
	for id, tr := range t.items {
		if !tr.Until.After(now) {
			gone = append(gone, tr)
			delete(t.items, id)
			delete(t.synced, id)
		}
	}
	if len(gone) > 0 {
		_ = t.saveLocked()
	}
	return gone
}

func (t *trusts) saveLocked() error {
	list := make([]proto.Trust, 0, len(t.items))
	for _, tr := range t.items {
		list = append(list, tr)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return writeFileAtomic(t.path, b)
}
```

`internal/agent/server.go`：

- `Server` 加 `trusts *trusts`；`NewServer`（或现有的构造处）用 `newTrusts(cfg.TrustsPath, time.Now)`，读失败记日志、按空表继续。
- `handlePermission`：在 `default:`（引擎答 ask）分支、`if s.rules.Paused()` 之后、PreToolUse 分支之前加：

```go
		if req.TrustFor != "" && req.Source == proto.SourcePreToolUse {
			s.trustFromEnv(req) // 首见建表；失败只记日志
		}
		if tr, ok := s.trusts.match(req.Session, pctx.ProjectRoot); ok {
			entry.Action, entry.State, entry.DecidedBy = "allow", "auto_allowed", "trust:"+tr.ID
			entry.Reason = fmt.Sprintf("限时放行（%s，剩 %s）", trustScopeLabel(tr), time.Until(tr.Until).Round(time.Minute))
			resp.Decision, resp.DecidedBy, resp.Reason = "allow", entry.DecidedBy, entry.Reason
			break
		}
```

`trustScopeLabel`：session → `会话 <tool>`，project → `项目 <base(project)>`。`trustFromEnv(req)`：`ParseTrustFor(req.TrustFor)` 失败记日志返回；`s.trusts.match` 已有同会话的 session 条目就返回（扫 `list()` 里 `Scope==session && Tool/CLISession 相同`）；否则 `add(Trust{Scope: session, Tool, CLISession, Until: now+d, CreatedBy: "env"})`，成功写 `kind: control / Message: trust_set` 审计并调 `s.trustChanged(tr, "set")`（Task 4 给 hub 客户端用的回调，本任务先是空操作钩子 `trustChanged func(proto.Trust, string)`，nil 时不调）。

- `handleControl` 加：

```go
	case "trust_set":
		if c.Trust == nil {
			return proto.ControlReply{V: proto.Version, OK: false, Error: "缺少 trust"}
		}
		tr := *c.Trust
		if tr.CreatedBy == "" {
			tr.CreatedBy = "cli"
		}
		tr, err := s.trusts.add(tr)
		if err != nil {
			return proto.ControlReply{V: proto.Version, OK: false, Error: err.Error()}
		}
		s.writeAudit(AuditEntry{TS: time.Now(), Kind: "control", State: "recorded", Message: "trust_set", Reason: trustAuditReason(tr), DecidedBy: tr.CreatedBy})
		if s.trustChanged != nil {
			s.trustChanged(tr, "set")
		}
		ok.Trusts = []proto.Trust{tr}
		return ok
	case "trust_list":
		ok.Trusts = s.trusts.list()
		return ok
	case "trust_revoke":
		before := s.trusts.list()
		if !s.trusts.revoke(c.TrustID) {
			return proto.ControlReply{V: proto.Version, OK: false, Error: "没有这条放行"}
		}
		for _, tr := range before {
			if c.TrustID == "all" || tr.ID == c.TrustID {
				s.writeAudit(AuditEntry{TS: time.Now(), Kind: "control", State: "recorded", Message: "trust_revoke", Reason: trustAuditReason(tr)})
				if s.trustChanged != nil {
					s.trustChanged(tr, "revoke")
				}
			}
		}
		ok.Trusts = s.trusts.list()
		return ok
```

`trustAuditReason(tr)`：`"<范围标签>，到 <Until 本地时间 15:04>，<created_by> 建"`。`status()` 加 `Trusts: len(s.trusts.list())`。到期清理：`Server` 启动一个每分钟 `sweep()` 的 goroutine（随 ctx 结束），清掉的每条写 `trust_expired` 审计并调 `trustChanged(tr, "expired")`。

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/agent/ ./internal/proto/ 2>&1 | tail -5`
Expected: PASS。`contract_test.go` 若锁定了 Control/Status 的字段集合，按新字段更新。

- [ ] **Step 5: 提交**

```bash
git add internal/proto/ internal/agent/
git commit -m "agent: 限时放行表——命中即放行、管理命令、RAILSE_TRUST 首见建表、到期清理与审计"
```

---

### Task 2: 命令行：`railse trust`、钩子读 `RAILSE_TRUST`、status 与 doctor

**Files:**
- Create: `cmd/railse/trust.go`、`cmd/railse/trust_test.go`
- Modify: `cmd/railse/main.go:41-60`（子命令表与 usage）、`cmd/railse/hook.go:160-175`（请求带 TrustFor）、`cmd/railse/agent.go:100-104`（status 多一行）
- Modify: `internal/hookclient/client.go:171`（`Control(op)` → `ControlMsg(c proto.Control)`，旧的 `Control(op)` 保留为包装）
- Modify: `internal/doctor/agent.go`、`internal/doctor/doctor_test.go`
- Modify: `internal/i18n/en.go`

**Interfaces:**
- Produces: `railse trust [时长] [--session <cli_session_id>] [--note 文字] [--dir 目录]`、`railse trust list`、`railse trust revoke <id>|--all`；`hookclient.Client.ControlMsg(proto.Control) (proto.ControlReply, error)`；doctor 检查 `agent.trusts`。
- Consumes: Task 1 的 Control ops、`ParseTrustFor`、`Status.Trusts`、`Request.TrustFor`。

- [ ] **Step 1: 写失败的测试**

`cmd/railse/trust_test.go`（照 `agent_test.go` 里起假守护进程 / 真守护进程的 helper，如 `startAgentForTest(t)`，以文件里实际的为准）：

```go
func TestTrustCommandSetListRevoke(t *testing.T) {
	env := startAgentForTest(t) // 返回 cfg（含 SocketPath）与一个 git 仓库目录 env.repo
	var out, errb bytes.Buffer
	// 在项目目录里：project 范围，默认 2h
	code := runTrustIn(env.repo, []string{"--dir", env.dir}, &out, &errb)
	if code != 0 || !strings.Contains(out.String(), "项目") || !strings.Contains(out.String(), "2h0m0s") {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errb.String())
	}
	out.Reset()
	code = runTrustIn(env.repo, []string{"30m", "--session", "abc", "--note", "赶活", "--dir", env.dir}, &out, &errb)
	if code != 0 || !strings.Contains(out.String(), "会话") {
		t.Fatalf("session 范围：code=%d out=%q err=%q", code, out.String(), errb.String())
	}
	out.Reset()
	code = runTrustIn(env.repo, []string{"list", "--dir", env.dir}, &out, &errb)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if code != 0 || len(lines) != 2 {
		t.Fatalf("list 应两行：code=%d out=%q", code, out.String())
	}
	id := strings.Fields(lines[0])[0]
	out.Reset()
	if code := runTrustIn(env.repo, []string{"revoke", id, "--dir", env.dir}, &out, &errb); code != 0 {
		t.Fatalf("revoke：%q", errb.String())
	}
	out.Reset()
	if code := runTrustIn(env.repo, []string{"revoke", "--all", "--dir", env.dir}, &out, &errb); code != 0 {
		t.Fatalf("revoke --all：%q", errb.String())
	}
	out.Reset()
	runTrustIn(env.repo, []string{"list", "--dir", env.dir}, &out, &errb)
	if !strings.Contains(out.String(), "没有生效中的放行") {
		t.Fatalf("清空后：%q", out.String())
	}
	// 不在项目目录（系统目录）里不带 --session：报错，不放行
	errb.Reset()
	if code := runTrustIn("/private/tmp", []string{"--dir", env.dir}, &out, &errb); code == 0 || !strings.Contains(errb.String(), "项目") {
		t.Fatalf("系统目录应拒绝：code=%d err=%q", code, errb.String())
	}
	// 时长格式错
	errb.Reset()
	if code := runTrustIn(env.repo, []string{"2 hours", "--dir", env.dir}, &out, &errb); code == 0 {
		t.Fatal("格式错应非零退出")
	}
}
```

`runTrustIn(cwd, args, out, err)` 是测试里对 `runTrust` 的包装，先 `os.Chdir(cwd)`（`t.Chdir`）。`cmd/railse/hook_test.go` 加：PreToolUse 输入在 `RAILSE_TRUST=2h` 环境下（`t.Setenv`），发给假守护进程的 `proto.Request.TrustFor == "2h"`；没设时为空；PermissionRequest 与 Codex 的请求不带。

`internal/doctor/doctor_test.go` 加：守护进程 status 报 `Trusts: 2` 时 `agent.trusts` 为 ok、Detail 含 "2"；为 0 时 skip。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run 'TestTrustCommand|TestHook.*TrustFor' 2>&1 | head`
Expected: 编译错误。

- [ ] **Step 3: 实现**

`internal/hookclient/client.go`：

```go
// ControlMsg 发一条管理命令（带参数的 trust_set 等用它）。
func (c Client) ControlMsg(msg proto.Control) (proto.ControlReply, error) { … 与 Control 相同的收发，只是消息由调用方给 … }

// Control 发只有 op 的管理命令。
func (c Client) Control(op string) (proto.ControlReply, error) {
	return c.ControlMsg(proto.Control{V: proto.Version, Kind: "control", Op: op})
}
```

`cmd/railse/trust.go`：

```go
const trustUsage = `用法:
  railse trust [时长] [--session <cli_session_id>] [--note 文字] [--dir 目录]   放行当前目录所在项目（或指定会话）一段时间，默认 2h，5m～24h
  railse trust list [--dir 目录]
  railse trust revoke <id>|--all [--dir 目录]

放行期间该范围内引擎定不了的请求一律本地放行，不弹终端框、不进手机；你手写的 deny 规则与总开关暂停不受影响。`

func runTrust(args []string, stdout, stderr io.Writer) int
```

- 子命令解析：第一个非选项参数是 `list`、`revoke` 或时长（可省）。
- `set`：`agentConfigFor(dir)`；`--session` 给了就是 session 范围（Tool 固定 `claude`——目前只有 Claude 的钩子带 session id 可用；写进 usage）；否则 `agent.ResolveProjectRoot(cwd)`，kind 不是 git 根 / 普通目录时报错 `当前目录不是项目（不在 git 仓库里，或在系统目录下），放行要在项目目录里跑，或用 --session 指定会话`。`ParseTrustFor` 失败报错。发 `trust_set`，打印一行：`已放行 项目 <名>（这台机器），到 15:04（2h0m0s）  id <id>`。
- `list`：每条一行 `<id>  <范围标签>  剩 <时长>  <created_by>  <note>`；空时 `没有生效中的放行`。
- `revoke`：发 `trust_revoke`，打印 `已撤销`。

`cmd/railse/main.go`：加 `case "trust": return runTrust(args[1:], stdout, stderr)`，usage 加一行 `trust     限时放行（当前项目或某个会话，默认 2 小时）`，`en.go` 的 usage 条目同步改（整段是一个键，中英文都要改）。

`cmd/railse/hook.go` 的 `hookPermission`：`if in.Source == proto.SourcePreToolUse { req.TrustFor = os.Getenv("RAILSE_TRUST") }`（文件头注释"只读哪些环境变量"加上它）。格式不对不在钩子里判断，守护进程记日志；钩子不输出任何环境变量。

`cmd/railse/agent.go` status 输出加一行 `trusts: %d`。

`internal/doctor/agent.go`：`agentChecks` 在"守护进程在跑"之后加 `agent.trusts`：status 可读且 `Trusts > 0` → OK，Detail `N 条限时放行生效中（railse trust list 查看）`；0 → Skip `没有生效中的放行`；status 读不到 → Skip。

- [ ] **Step 4: 跑测试**

Run: `go test ./cmd/railse/ ./internal/hookclient/ ./internal/doctor/ ./internal/i18n/ 2>&1 | tail -5`
Expected: PASS（`TestCatalogComplete` 会要求 usage、新句子都有英文）。

- [ ] **Step 5: 提交**

```bash
git add cmd/railse/ internal/hookclient/ internal/doctor/ internal/i18n/en.go
git commit -m "cli: railse trust 子命令；钩子带 RAILSE_TRUST；status 与 doctor 显示放行"
```

---

### Task 3: hub：`trusts` 表（schema v6）、帧、接口、decide 带放行

**Files:**
- Modify: `internal/hub/wire/wire.go`（Hello.Features、Welcome.Trusts、TTrusts、TTrust、FeatureTrust、Trusts/TrustChange 帧）
- Modify: `internal/hub/store/schema.sql`、`store/store.go:21-47`；Create: `internal/hub/store/trusts.go`、`trusts_test.go`
- Create: `internal/hub/trusts.go`；Modify: `internal/hub/agents.go:274-340, 410-427`（hello 的 features、welcome 带快照、收 trust 帧）、`internal/hub/api.go:30-60, 252-273`、`internal/hub/requests.go:69-77, 576-620`、`internal/hub/server.go`（每分钟清理）
- Test: `internal/hub/api_test.go`、`internal/hub/agents_test.go`

**Interfaces:**
- Produces:

```go
// wire
const FeatureTrust = "trust"            // hub 与守护进程都在各自的 features 里声明
type Hello struct { …; Features []string `json:"features,omitempty"` }
type Welcome struct { …; Trusts []proto.Trust `json:"trusts,omitempty"` }
const TTrusts = "trusts"                 // hub → 守护进程，这台机器的放行快照（只含未过期、未撤的）
const TTrust  = "trust"                  // 守护进程 → hub，一条变更
type Trusts struct { T string `json:"t"`; Trusts []proto.Trust `json:"trusts"` }
type TrustChange struct { T string `json:"t"`; Op string `json:"op"`; Trust proto.Trust `json:"trust"` } // op: set | revoke | expired
// store
type Trust = proto.Trust（加 RevokedAt *string 不进 JSON：store 层用 struct { proto.Trust; RevokedAt *string }）
func (s *Store) UpsertTrust(t proto.Trust) error
func (s *Store) RevokeTrust(id string) (proto.Trust, error)     // 标 revoked_at，返回原条目；没有或已撤回 ErrNotFound
func (s *Store) ListTrusts(machineID string) ([]proto.Trust, error) // machineID 空为全部；只含未过期、未撤的
func (s *Store) ExpireTrusts() (int, error)                      // 把 until <= now 且未撤的标 revoked_at（原因 expired），返回条数
// hub HTTP
GET    /api/trusts            → [ { ...proto.Trust, machine_name, project, remaining_seconds } ]
DELETE /api/trusts/{id}       → 204；推快照给那台机器；审计 trust_revoke
POST   /api/requests/{id}/decide  body 加 "trust": {"scope":"session|project","minutes":120}
requestView 加 can_trust bool（该请求所属机器在线且其 hello 声明了 trust，且 kind 为 permission）
GET /api/status 加 trusts: N
```

- [ ] **Step 1: 写失败的测试**

`internal/hub/store/trusts_test.go`：Upsert → List 看得到；Revoke 后 List 看不到、再 Revoke 回 ErrNotFound；`until` 过去的 Upsert 后 ExpireTrusts 返回 1、List 看不到；`TestMigratedRequestsSchemaMatchesFresh` 一类的迁移测试覆盖 v6（SchemaVersion 常量断言改 6）。

`internal/hub/api_test.go` 加（照文件里现有的接入机器、模拟守护进程连接、登录网页的 helper）：

```go
// 手机批准时带 trust：hub 建放行、推快照给守护进程、再下发 allow；GET /api/trusts 看得到；DELETE 后守护进程收到不含它的快照。
func TestDecideWithTrustAndRevoke(t *testing.T) {
	h := newTestHub(t)
	agent := h.connectAgent(t, "m1", []string{wire.FeatureTrust}) // hello 带 features
	id := agent.sendPending(t, permissionPending("git push origin main", "/home/u/proj", "claude", "sess-1"))
	client := h.login(t)
	var view map[string]any
	client.getJSON(t, "/api/requests/"+id, &view)
	if view["can_trust"] != true {
		t.Fatalf("守护进程声明了 trust，视图应 can_trust：%v", view["can_trust"])
	}
	client.postJSON(t, "/api/requests/"+id+"/decide", map[string]any{"action": "allow", "trust": map[string]any{"scope": "session", "minutes": 120}}, &view)
	snap := agent.expectFrame(t, wire.TTrusts) // 快照先到
	var ts wire.Trusts
	wire.Unmarshal(snap, &ts)
	if len(ts.Trusts) != 1 || ts.Trusts[0].Scope != "session" || ts.Trusts[0].CLISession != "sess-1" || ts.Trusts[0].CreatedBy != "phone" {
		t.Fatalf("快照：%+v", ts.Trusts)
	}
	agent.expectFrame(t, wire.TDecision) // 再是 allow
	var list []map[string]any
	client.getJSON(t, "/api/trusts", &list)
	if len(list) != 1 || list[0]["machine_name"] != "m1" {
		t.Fatalf("GET /api/trusts：%v", list)
	}
	client.delete(t, "/api/trusts/"+ts.Trusts[0].ID, 204)
	wire.Unmarshal(agent.expectFrame(t, wire.TTrusts), &ts)
	if len(ts.Trusts) != 0 {
		t.Fatalf("撤销后快照应为空：%+v", ts.Trusts)
	}
	client.getJSON(t, "/api/trusts", &list)
	if len(list) != 0 {
		t.Fatalf("撤销后列表应为空：%v", list)
	}
}

// 守护进程不声明 trust：视图 can_trust 为 false，decide 带 trust 回 400，列表不显示它的机器。
func TestDecideTrustRefusedForOldAgent(t *testing.T) { … connectAgent(t, "m2", nil) … 期望 400，错误文案含 "守护进程太旧" … }

// 守护进程报来的 trust 帧（cli/env 建的）入库、网页看得到；expired/revoke 帧让它消失。时长超界由 hub 截；超过 20 条拒。
func TestAgentTrustFramesStored(t *testing.T) { … }

// 机器离线时 DELETE 也成功，重连后 welcome 的快照里没有它。
func TestTrustRevokeOffline(t *testing.T) { … }
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/... -run 'Trust|Migrated' 2>&1 | head`
Expected: 编译错误。

- [ ] **Step 3: 实现**

`schema.sql` 加表（与 `requests` 同风格）：

```sql
CREATE TABLE IF NOT EXISTS trusts (
  id             TEXT PRIMARY KEY,
  machine_id     TEXT NOT NULL REFERENCES machines(id),
  scope          TEXT NOT NULL,             -- session、project
  tool           TEXT NOT NULL DEFAULT '',
  cli_session_id TEXT NOT NULL DEFAULT '',
  project_root   TEXT NOT NULL DEFAULT '',
  until_at       TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  created_by     TEXT NOT NULL,             -- phone、cli、env
  note           TEXT NOT NULL DEFAULT '',
  revoked_at     TEXT,
  revoke_reason  TEXT NOT NULL DEFAULT ''   -- user、expired、agent
);
CREATE INDEX IF NOT EXISTS trusts_machine_until ON trusts (machine_id, until_at);
```

`store.go`：`SchemaVersion = 6`，迁移 6 为同一段 `CREATE TABLE … ; CREATE INDEX …`。`store/trusts.go` 实现四个方法（时间用 `FormatTime`/`Now()`，`ListTrusts` 的 WHERE：`revoked_at IS NULL AND until_at > ?`）。

`wire.go`：加常量、类型与字段；`hubFeatures()` 加 `wire.FeatureTrust`。

`internal/hub/trusts.go`：

```go
// trustSupported 报告这台机器当前连接的守护进程声明了 trust 能力（agentHub 记每条连接 hello 里的 features）。
func (s *Server) trustSupported(machineID string) bool
// pushTrusts 把这台机器的放行快照发给它（离线时不发，重连的 welcome 会带）。
func (s *Server) pushTrusts(machineID string)
// createTrust 校验（scope、目标、时长截到 5m～24h、每机器 ≤ 20）、入库、审计 trust_set、pushTrusts。
func (s *Server) createTrust(t proto.Trust) (proto.Trust, error)
// revokeTrust 标撤、审计、pushTrusts。
func (s *Server) revokeTrust(id, reason string) error
// applyAgentTrust 处理守护进程的 trust 帧：set → UpsertTrust（CreatedBy 照来的）、revoke/expired → RevokeTrust(reason agent/expired)。
func (s *Server) applyAgentTrust(machineID string, c wire.TrustChange)
// sweepTrusts 每分钟：ExpireTrusts，>0 时广播 status。
```

`agents.go`：`agentConn` 加 `features []string`（hello 里读）；`welcomeFrame` 加 `Trusts: store.ListTrusts(machine.ID)`（welcomeSource 接口加方法）；读帧循环加 `case wire.TTrust`。

`api.go`：路由 `GET /api/trusts`、`DELETE /api/trusts/{id}`；`handleDecide` body 加 `Trust *struct{ Scope string; Minutes int }`；`requestView` 加 `CanTrust bool \`json:"can_trust"\``，`views()` 里 `CanTrust: r.Kind == "permission" && s.trustSupported(r.MachineID)`；`handleStatus` 加 `trusts`。

`requests.go` 的 `Decide`：`DecideInput` 加 `Trust *TrustInput`；在 `commitDecision` 成功、下发 allow **之前**：`in.Action == "allow" && in.Trust != nil` → 从请求取 `MachineID`、`CLISessionID`、`SessionTool`、`ProjectRoot` 组 `proto.Trust{CreatedBy: "phone"}` → `createTrust`；失败（机器不支持、超上限、没有 session id / 项目根）整个 decide 回 400，决定不落库（校验放在 `commitDecision` 之前）。顺序保证守护进程先收到快照再收到 allow。

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/hub/... 2>&1 | tail -6`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/
git commit -m "hub: trusts 表（schema v6）、trust 能力与帧、/api/trusts、批准时顺带限时放行"
```

---

### Task 4: 守护进程与 hub 同步

**Files:**
- Modify: `internal/agent/hubclient.go:340-350, 429-432, 460-480, 550-560`、`internal/agent/server.go`（trustChanged 接线）
- Test: `internal/agent/hubclient_test.go`、`hubclient_internal_test.go`

**Interfaces:**
- Consumes: Task 1 的 `trusts`（加 `markSynced(ids []string)`、`unsynced() []proto.Trust`、`applySnapshot(snap []proto.Trust) (revoked []proto.Trust)`）、Task 3 的帧。
- 合并规则（精确化设计 §2.5）：收到快照时——快照里有的条目：写入/覆盖本机表并标已同步；本机表里**已同步**却不在快照里的：删掉（被手机撤了）并写 `trust_revoke` 审计（decided_by hub）；本机**未同步**的（断线期间 cli/env 建的）：保留，并逐条发 `trust` 帧补给 hub。hub 没声明 `trust` 能力：不发帧、不标同步、快照忽略。

- [ ] **Step 1: 写失败的测试**

`internal/agent/hubclient_internal_test.go`（现有的假 hub 是 `newFakeHub(t, status, script func(f *fakeHub, conn *websocket.Conn, hello wire.Hello))`：script 里收 hello、写 welcome、再按需收发帧。下面用到的 `hub.expectFrame`、`hub.send`、`hub.dropConnection`、`hub.acceptReconnect` 是要在 `fakeHub` 上补的 helper，或者把整个场景写进 script 回调里，以实际改起来顺手为准；`startServerWithHub` 对应文件里现有的"起带 HubClient 的 Server"的写法）：

```go
func TestHubTrustSync(t *testing.T) {
	hub := newFakeHub(t, fakeHubOptions{features: []string{wire.FeatureTrust}})
	env := startServerWithHub(t, hub)
	// 1) 本机建一条 → hub 收到 trust set 帧
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(time.Hour)}}), &reply)
	local := reply.Trusts[0]
	ch := hub.expectFrame(t, wire.TTrust)
	if ch.Op != "set" || ch.Trust.ID != local.ID || ch.Trust.CreatedBy != "cli" {
		t.Fatalf("set 帧：%+v", ch)
	}
	// 2) hub 发快照：含本机那条（已同步）+ 手机建的一条
	phone := proto.Trust{ID: "p1", Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s1", Until: time.Now().Add(time.Hour), CreatedBy: "phone"}
	hub.send(t, wire.Trusts{T: wire.TTrusts, Trusts: []proto.Trust{local, phone}})
	waitFor(t, func() bool { return trustCount(t, env) == 2 })
	// 3) hub 再发不含 p1 的快照 → 本机删掉 p1（被手机撤了），本机那条还在
	hub.send(t, wire.Trusts{T: wire.TTrusts, Trusts: []proto.Trust{local}})
	waitFor(t, func() bool { return trustCount(t, env) == 1 })
	// 4) 断线期间本机建一条；重连后 welcome 快照里没有它 → 保留并补发
	hub.dropConnection()
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s2", Until: time.Now().Add(time.Hour)}}), &reply)
	offline := reply.Trusts[0]
	hub.acceptReconnect(t, wire.Welcome{Trusts: []proto.Trust{local}})
	ch = hub.expectFrame(t, wire.TTrust)
	if ch.Op != "set" || ch.Trust.ID != offline.ID {
		t.Fatalf("重连后应补发未同步的：%+v", ch)
	}
	if trustCount(t, env) != 2 {
		t.Fatalf("未同步的不该被快照删掉")
	}
	// 5) 本机撤销 → hub 收到 revoke 帧
	roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_revoke", TrustID: offline.ID})
	if ch = hub.expectFrame(t, wire.TTrust); ch.Op != "revoke" {
		t.Fatalf("revoke 帧：%+v", ch)
	}
}

func TestHubWithoutTrustFeatureIgnored(t *testing.T) { … hub 不声明 trust：本机建放行不发帧（expectNoFrame 2 秒）；hub 发的快照被忽略，本机表不变 … }
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/ -run 'TestHubTrust|TestHubWithoutTrust' 2>&1 | head`

- [ ] **Step 3: 实现**

`trusts` 加：

```go
func (t *trusts) markSynced(ids ...string)
func (t *trusts) unsynced() []proto.Trust
// applySnapshot 按合并规则处理 hub 快照，返回被撤的（已同步却不在快照里）。
func (t *trusts) applySnapshot(snap []proto.Trust) (revoked []proto.Trust)
```

`hubclient.go`：
- hello 加 `Features: []string{wire.FeatureTrust}`。
- `applyWelcome`：`if w.Has(wire.FeatureTrust) { h.trustCapable = true; h.applyTrusts(w.Trusts) }`。
- 读帧循环加 `case wire.TTrusts` → `applyTrusts`。
- `applyTrusts(snap)`：`revoked := h.trusts.applySnapshot(snap)`，每条写审计 `trust_revoke`（DecidedBy `hub`）；然后 `for _, tr := range h.trusts.unsynced() { h.sendTrust("set", tr) }`。
- `sendTrust(op, tr)`：`trustCapable` 为假直接返回；照 `SendCancel` 的写法发 `wire.TrustChange`；发成功且 op 为 set → 不在这里标同步（等快照回来再标，见规则）。
- `Server.trustChanged` 接到 `hubClient.sendTrust`（构造处传闭包；没接 hub 时为 nil）。

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/agent/ 2>&1 | tail -4`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/agent/
git commit -m "agent: 放行表与 hub 双向同步——hello 声明能力、快照合并、本机变更上报"
```

---

### Task 5: 网页：审批面板开关、设置页一节、收件箱横幅

**Files:**
- Modify: `web/src/types.ts`（RequestView.can_trust、DecideBody.trust、Status.trusts、新 `TrustView`）、`web/src/api.ts`（`trusts()`、`revokeTrust(id)`）、`web/src/decide.ts`（SheetState 加 trust、trustScope、trustMinutes）、`web/src/components/DecideSheet.tsx`、`web/src/pages/Settings.tsx`、`web/src/pages/Inbox.tsx`、`web/src/store.ts`/`StoreProvider.tsx`（status 里的 trusts 数）、`web/src/locales/en.ts`
- Test: `web/src/decide.test.ts`、`web/src/pages/settings.trust.test.tsx`（新）、`web/src/pages/inbox.test.tsx`、`web/e2e/approve.spec.ts`

**Interfaces:**
- Consumes: Task 3 的接口。
- Produces: `TrustView { id, scope, tool, cli_session_id, project_root, until, created_at, created_by, note, machine_name, project, remaining_seconds }`。

- [ ] **Step 1: 写失败的测试**

`web/src/decide.test.ts` 加：`initialState(r)`——`can_trust` 为真且 kind permission 时 `trustable: true`，有 `cli_session_id` 默认范围 `session`，否则 `project`，没有项目根也没有会话时 `trustable: false`；`reducer` 的 `trust` 开关与 `trust_scope`、`trust_minutes` 动作；`decideBody(s)` 开着开关时带 `trust: {scope, minutes}`，拒绝时不带。

`web/src/pages/settings.trust.test.tsx`（照 `settings.push.test.tsx` 的渲染方式）：mock `hub.trusts()` 返回两条 → 列出 `会话 Claude Code · 项目 proj · 机器 mac`、`剩 1 小时 58 分`、`手机建`，点"撤销"后调用 `hub.revokeTrust(id)` 并重拉；空列表显示 `没有生效中的放行`。

`web/src/pages/inbox.test.tsx` 加：`status.trusts > 0` 时顶部有横幅 `2 个放行生效中，最近的 12:30 到期`（时间按 `trusts` 列表里最早的 until；横幅点击去 `/settings`）。

`web/e2e/approve.spec.ts` 加一段：批准面板里开关可见（`scripts/e2e-web.sh` 起的是真守护进程与真 hub，钩子经 `railse hook claude --socket` 调用，所以 hello 自然带 `trust`，开关会显示；e2e 的 PreToolUse 样本要带 `cli_session_id` 才有"这个会话"可选），打开、选"这个项目 / 2 小时"、批准；设置页列出一条；撤销后消失。

- [ ] **Step 2: 跑测试确认失败**

Run: `cd web && npx vitest run src/decide.test.ts src/pages/settings.trust.test.tsx src/pages/inbox.test.tsx 2>&1 | tail -5`

- [ ] **Step 3: 实现**

`DecideSheet.tsx` 在"记住"开关之后加（只在 `allow && s.trustable`）：

```tsx
<label className="switch">
  <span>
    {t('接下来一段时间都放行')}
    <span className="sub line">{t('这段时间里这个范围内的请求一律放行，不再问你；到期自动失效，设置页可随时撤销。')}</span>
  </span>
  <input type="checkbox" checked={s.trust} disabled={s.busy} onChange={(e) => dispatch({ type: 'trust', on: e.target.checked })} />
</label>
{s.trust && (
  <div className="draft">
    <div className="row">
      <span>{t('范围')}</span>
      <span className="seg" role="group" aria-label={t('放行的范围')}>
        {trustScopeOptions(request).map((o) => (
          <button key={o.scope} type="button" className={s.trustScope === o.scope ? 'on' : ''} aria-pressed={s.trustScope === o.scope} disabled={o.disabled || s.busy} onClick={() => dispatch({ type: 'trust_scope', scope: o.scope })}>{o.label}</button>
        ))}
      </span>
    </div>
    <div className="row">
      <span>{t('时长')}</span>
      <span className="seg" role="group" aria-label={t('放行的时长')}>
        {[30, 120, 480].map((m) => (
          <button key={m} type="button" className={s.trustMinutes === m ? 'on' : ''} aria-pressed={s.trustMinutes === m} disabled={s.busy} onClick={() => dispatch({ type: 'trust_minutes', minutes: m })}>{minutesLabel(m)}</button>
        ))}
      </span>
    </div>
  </div>
)}
```

`trustScopeOptions(r)`：`[{scope:'session', label: t('这个会话'), disabled: !r.cli_session_id}, {scope:'project', label: t('这个项目'), disabled: !r.project_root}]`；`minutesLabel`：30 → `30 分钟`，120 → `2 小时`，480 → `8 小时`。

`Settings.tsx` 在 hub 一节之后加"限时放行"一节：列表由 `hub.trusts()` 拉（进入设置页与撤销后重拉），每条两行：第一行 `会话 Claude Code · 项目 proj · 机器 mac`（session）或 `项目 proj · 机器 mac`（project），第二行 `剩 1 小时 58 分 · 手机建 · 备注`，右侧 `撤销` 按钮（经 `useActions().act` 互斥，失败在 Banner 里说）。空时 `没有生效中的放行`。

`Inbox.tsx`：`state.status?.trusts > 0` 时在暂停横幅之后加 `Banner tone="info"`，文字 `{n} 个放行生效中，最近的 {time} 到期`，带 `去设置` 链接。最早到期时间来自 `hub.trusts()`（StoreProvider 里随 status 一起拉，只在 `trusts > 0` 时拉）。

`store.ts`：`Status` 加 `trusts: number`；`api.ts` 加 `trusts: () => get<TrustView[]>('/api/trusts')`、`revokeTrust: (id) => del(`/api/trusts/${enc(id)}`)`。

- [ ] **Step 4: 跑测试**

Run: `cd web && npx vitest run 2>&1 | tail -5 && npm run lint 2>&1 | tail -2 && cd .. && make e2e 2>&1 | tail -5`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add web/
git commit -m "web: 批准时可顺带限时放行；设置页列出并可撤销；收件箱横幅"
```

---

### Task 6: 文档

**Files:**
- Modify: `README.md`、`README.en.md`、`docs/hub.md`、`docs/agent-local-loop.md`、`docs/setup.md`、`docs/launch/qa.md`

- [ ] **Step 1: 写**

- README 两份：在"规则"之后加"限时放行"一节：三个入口各一行命令或一句话；一句"放行期间 Railse 对该范围完全不把关，等同于 Claude Code 的 bypassPermissions 加上审计，到期自动失效，最长 24 小时"。能力矩阵加一行"限时放行：会话 / 项目，手机、终端、`RAILSE_TRUST` 三个入口"。升级说明加 0.2.3：hub 库 v5 → v6，不能降级。
- `docs/hub.md`：`trusts` 表、`trust` 能力、`trusts`/`trust` 帧、`GET/DELETE /api/trusts`、decide 的 `trust` 字段、`can_trust`、`status.trusts`、审计动作 `trust_set/trust_revoke/trust_expired`。
- `docs/agent-local-loop.md`：判定表（设计 §2.2）、`trusts.json`、管理命令、合并规则（Task 4）。
- `docs/setup.md`：升级条目 9。
- `docs/launch/qa.md`：加一问"太吵怎么办 / 能不能某个窗口全放行"，答三个入口与上限。

- [ ] **Step 2: 提交**

```bash
git add README.md README.en.md docs/
git commit -m "docs: 限时放行的用法、接口、帧与升级说明"
```

---

### Task 7: 全量门禁与实施报告

- [ ] **Step 1**：`make test`、`make e2e` 全绿（本机 `TestStringLinearOnAdversarialInput` 在机器忙时可能超 10 秒墙钟、英文 e2e 在 `~/.railse/lang=zh` 的机器上会漏中文，这两条以 CI 为准，报告里注明）。
- [ ] **Step 2**：自检脚本（写成文件再 `sh` 跑，别直接在 Bash 工具里写含 rm/pkill 的命令）：起一个临时目录的守护进程（`--dir`），`railse trust 30m` → `railse policy eval` 不走守护进程所以看不到放行，改用 `railse hook claude` 喂一条 PreToolUse 的 `git push` 样本，期望输出 `permissionDecision: allow` 且理由含 `限时放行`；`railse trust list`、`revoke --all`。把输出贴进报告。
- [ ] **Step 3**：在本文件末尾写"## 实施报告"：每个任务的提交号、偏离、自检输出、留给真机验收的 7 条（设计 §3）。不推送、不合并。

---

## 自查

- 设计 §2.1（记录）→ Task 1；§2.2（判定）→ Task 1；§2.3 三个入口 → 手机 Task 3 + 5，终端 Task 2，环境变量 Task 1 + 2；§2.4 可见与撤销 → Task 5（横幅、设置页）、Task 2（status、doctor）、Task 1/3（审计）；§2.5 同步 → Task 3 + 4（合并规则精确化为"已同步"标记）；§2.6 上限与安全 → Task 1（守护进程校验）、Task 3（hub 校验）；§3 真机验收留给主会话；§4 文档 → Task 6。
- 名字一致性：`proto.Trust{ID, MachineID, Scope, Tool, CLISession, Project, Until, CreatedAt, CreatedBy, Note}`、`proto.TrustScopeSession/Project`、`proto.TrustMin/Max/Default/PerMachine`、`Request.TrustFor`、`Control.Trust/TrustID`、`ControlReply.Trusts`、`Status.Trusts`、`agent.trusts{add,revoke,list,match,sweep,markSynced,unsynced,applySnapshot}`、`agent.ParseTrustFor`、`Server.trustChanged`、`hookclient.Client.ControlMsg`、`wire.FeatureTrust/TTrusts/TTrust/Trusts/TrustChange`、`Hello.Features`、`Welcome.Trusts`、`store.{UpsertTrust,RevokeTrust,ListTrusts,ExpireTrusts}`、hub `{trustSupported,pushTrusts,createTrust,revokeTrust,applyAgentTrust,sweepTrusts}`、`DecideInput.Trust`、`requestView.CanTrust`、网页 `TrustView`、`SheetState.{trustable,trust,trustScope,trustMinutes}`、`hub.trusts()/revokeTrust()`。
- 设计 §2.1 说"放行表落 `state.json`"，计划改为独立的 `trusts.json`（state.json 只有两个布尔，混在一起没必要）；设计 §2.3 的 `railse trust --session` 目前只对 Claude 的会话 id 有意义（Codex 的 PermissionRequest 也带 session id，但用户拿不到它），usage 里写明。
