# Railse OPC v0.2.1：审批搬到 PreToolUse 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Claude Code 不论什么权限模式，Bash / 文件写 / WebFetch 的每个调用都先经 Railse 规则；规则定得了的直接 allow/deny，往外发与破坏性的任何模式都到手机，中间三级在 auto 模式下默认交给 Claude 分类器；同一调用不问两次；失败即不表态。顺带：`| sh` 一类定为 destructive、网页回前台自动重载、README 改准并补能力矩阵。

**Architecture:** 适配器把 PreToolUse 的 Bash/Edit/Write/MultiEdit/NotebookEdit/WebFetch 解析成 `KindPermission`，并在 `Input`/`proto.Request` 上带 `Source`（pretooluse / permissionrequest）、`PermissionMode`、`ToolUseID`；守护进程 `handlePermission` 的"定不了"分支按风险与模式决定：硬底线送 hub，中间三级在 auto 下不表态（本机 `settings.yaml` 可改为送手机）；新增 `recent` 表按去重键记 10 分钟内 PreToolUse 的结果；`proto.Response` 加 `DecidedBy`；适配器 `Render` 按 `Source` 输出 PreToolUse 或 PermissionRequest 的 JSON；hooksconf 的 PreToolUse 匹配器扩展，doctor 检查匹配器。

**Tech Stack:** Go 1.26（见 go.mod；gofmt 走 `make gofmt`）、React/TS、vitest、Playwright。

## Global Constraints

- 设计：`docs/superpowers/specs/2026-11-01-railse-opc-v0.2.1-pretooluse-design.md`（已按本计划改过 §2.3.1、§2.4、§2.6）。
- `go test` 在 `RAILSE_LANG=zh` 下跑（Makefile 已钉）；新加的用户可见句子一律 `i18n.T`，英文进 `internal/i18n/en.go`（完整性测试会查）。
- 不碰真实的 `~/.claude`、`~/.railse`、`~/.railse-hub`；真机验收由主会话做。
- 所有回复、提交说明、注释中文。每个任务一个提交，分支上每个提交都过 `make test`。不推送、不合并。
- 样本：`internal/adapter/testdata/claude/pretooluse-bash-{default,auto}.json`（2026-11-01 实录）、`internal/adapter/testdata/claude/2.1.280/PermissionRequest.json`（无 `tool_use_id`，有 `permission_mode`）。
- `permissionDecision: "ask"` 在 auto 模式下的真实行为要先实测（Task 1），结果写进 `docs/hub.md`，后续任务据此选择硬底线 passthrough 时输出 `ask` 还是空。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/adapter/adapter.go`、`claude.go`、`*_test.go` | `Input.Source/PermissionMode/ToolUseID`；PreToolUse 六个工具 → `KindPermission`；`Render` 按 Source 出 PreToolUse JSON |
| `internal/proto/proto.go` | `Request.Source/PermissionMode/ToolUseID`、`Response.DecidedBy` |
| `internal/agent/settings.go`（新）、`server.go`、`recent.go`（新）、`audit.go` | `settings.yaml` 读取；模式分支；去重表；审计新字段 |
| `cmd/railse/hook.go`、`hook_test.go` | 透传新字段；hub 不可达时的终端提示 |
| `internal/hooksconf/hooksconf.go`、`internal/doctor/hooks.go` | 匹配器 `AskUserQuestion\|Bash\|Edit\|Write\|MultiEdit\|NotebookEdit\|WebFetch`；doctor 查匹配器 |
| `internal/policy/classify_shell.go`、`testdata/shell_corpus.tsv` | 喂给 shell/解释器的管道与命令替换 → destructive |
| `web/src/pwa.ts`、`StoreProvider.tsx`、`pwa.test.ts` | 回前台比对 hub 版本，变了重载一次 |
| `README.md`、`README.en.md`、`docs/hub.md`、`docs/agent-local-loop.md` | 说法改准、能力矩阵、升级说明 |

---

### Task 1: 实测 `permissionDecision: "ask"` 在 auto 模式下的行为（不改代码）

**Files:**
- Create: `docs/superpowers/specs/2026-11-01-pretooluse-ask-findings.md`

- [ ] **Step 1:** 在一个临时目录（不是真实项目；用 `claude -p --settings <临时 settings.json>`，settings 里 PreToolUse 匹配 `Bash`，钩子命令输出 `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask","permissionDecisionReason":"railse test"}}`）分别以 `--permission-mode auto` 与 `default` 让 Claude 跑 `echo hi`。`-p` 模式下无人可答：观察输出是"被拒/要求确认"还是命令照跑（跑了 = `ask` 在该模式下回到了自动判断）。
- [ ] **Step 2:** 再录一份 PermissionRequest 样本：钩子 `cat >> log` 挂在 PermissionRequest 上，`--permission-mode default` 跑 `curl -sI https://example.com/`，看样本里有没有 `tool_use_id`。
- [ ] **Step 3:** 把两条结论写进 findings 文件（命令、Claude 版本、原始输出），并在 `docs/hub.md` 的钩子一节加两句话。
- [ ] **Step 4:** 提交 `docs: PreToolUse 的 ask 语义与 PermissionRequest 是否带 tool_use_id 的实测`。

**后续任务的分支选择**：若 `ask` 在 auto 下会强制弹框 → 硬底线交回终端时输出 `ask`；若回到分类器 → 输出空（不表态）。下文写作 `ASK_OR_EMPTY`。

### Task 2: proto 与适配器：PreToolUse 的六个工具解析成审批

**Files:**
- Modify: `internal/proto/proto.go:78-98`、`internal/adapter/adapter.go:42-66,90-117`、`internal/adapter/claude.go:21-100`
- Test: `internal/adapter/claude_test.go`、`claude_ask_test.go`

**Interfaces:**
- `proto.Request` 加 `Source string \`json:"source,omitempty"\``（`pretooluse` / `permissionrequest`，空按 permissionrequest）、`PermissionMode string \`json:"permission_mode,omitempty"\``、`ToolUseID string \`json:"tool_use_id,omitempty"\``；`proto.Response` 加 `DecidedBy string \`json:"decided_by,omitempty"\``。
- `adapter.Input` 加同名三字段；`adapter.Decision` 加 `Source string`。
- 常量 `adapter.SourcePreToolUse = "pretooluse"`、`adapter.SourcePermissionRequest = "permissionrequest"`。

- [ ] **Step 1: 写失败的测试**（`claude_test.go`）

```go
func TestClaudeParsePreToolUseBash(t *testing.T) {
	for _, f := range []string{"claude/pretooluse-bash-default.json", "claude/pretooluse-bash-auto.json"} {
		in, err := claude{}.Parse(sample(t, f))
		if err != nil {
			t.Fatal(f, err)
		}
		if in.Kind != KindPermission || in.ToolName != "Bash" || in.Payload.Type != policy.PayloadShell {
			t.Fatalf("%s: kind=%s tool=%s type=%s", f, in.Kind, in.ToolName, in.Payload.Type)
		}
		if in.Source != SourcePreToolUse || in.ToolUseID == "" || (in.PermissionMode != "default" && in.PermissionMode != "auto") {
			t.Fatalf("%s: source=%s id=%q mode=%q", f, in.Source, in.ToolUseID, in.PermissionMode)
		}
	}
	// PermissionRequest 的来源与模式也要带上
	in, err := claude{}.Parse(sample(t, "claude/2.1.280/PermissionRequest.json"))
	if err != nil || in.Source != SourcePermissionRequest || in.PermissionMode != "default" {
		t.Fatalf("PermissionRequest: %+v %v", in, err)
	}
}

func TestClaudeRenderPreToolUse(t *testing.T) {
	cases := map[string]string{
		"allow":       `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","permissionDecisionReason":"Railse：命中规则 r1"}}`,
		"deny":        `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"Railse：已在手机上拒绝"}}`,
		"passthrough": ``, // ASK_OR_EMPTY：Task 1 若定为 ask，这里改成 permissionDecision ask 的 JSON
	}
	for action, want := range cases {
		reason := map[string]string{"allow": "命中规则 r1", "deny": "已在手机上拒绝"}[action]
		out := claude{}.Render(Decision{Source: SourcePreToolUse, Action: action, Reason: reason})
		if string(out.Stdout) != want {
			t.Errorf("%s: got %s want %s", action, out.Stdout, want)
		}
	}
}
```

同时改旧断言：`TestClaudeParseEvents` 里 `claude/2.1.280/PreToolUse.json`（Bash）从 `KindIgnore` 改为 `KindPermission`；`TestClaudeParseAskIgnored` 里 `"PreToolUse 别的工具"` 一条改为期待 `KindPermission`（工具名 Bash），再加一条 `"PreToolUse 不认识的工具 Foo"` 期待 `KindIgnore`。

- [ ] **Step 2: 跑确认失败**：`RAILSE_LANG=zh go test ./internal/adapter/ -run 'PreToolUse|ParseEvents|AskIgnored'`。

- [ ] **Step 3: 实现**

`claudeHook` 加 `PermissionMode string \`json:"permission_mode"\``、`ToolUseID string \`json:"tool_use_id"\``。Parse：

```go
	case "PermissionRequest":
		// …原逻辑…
		in.Kind, in.ToolName, in.Payload = KindPermission, h.ToolName, p
		in.Source, in.PermissionMode, in.ToolUseID = SourcePermissionRequest, h.PermissionMode, h.ToolUseID
	case "PreToolUse":
		switch h.ToolName {
		case proto.AskToolName:
			// …原答题逻辑不变…
		case "Bash", "Edit", "Write", "MultiEdit", "NotebookEdit", "WebFetch":
			if h.SessionID == "" {
				return Input{}, errors.New(i18n.T("PreToolUse 缺少 session_id"))
			}
			p, err := mapTool("claude", h.ToolName, h.ToolInput, h.Cwd)
			if err != nil {
				return Input{}, err
			}
			in.Kind, in.ToolName, in.Payload = KindPermission, h.ToolName, p
			in.Source, in.PermissionMode, in.ToolUseID = SourcePreToolUse, h.PermissionMode, h.ToolUseID
		default:
			in.Kind = KindIgnore // 其余工具留给 PermissionRequest 兜底
		}
```

Render：`Decision.Source == SourcePreToolUse` 时走新函数 `renderPreToolUse(d)`：allow → `permissionDecision: allow` 带 `permissionDecisionReason: "Railse：命中规则 X"`（用 `denyMessage` 同款前缀函数 `railseMessage(reason)`）；deny → `deny` 带 `denyMessage(reason)`；其余 → `ASK_OR_EMPTY`。JSON 用结构体 `json.Marshal`，字段 `hookEventName`、`permissionDecision`、`permissionDecisionReason`。

- [ ] **Step 4: 跑测试**：`RAILSE_LANG=zh go test ./internal/adapter/ ./internal/proto/ -count=1`。
- [ ] **Step 5: 提交**：`adapter: PreToolUse 的 Bash/Edit/Write/MultiEdit/NotebookEdit/WebFetch 解析成审批；Render 按来源输出 PreToolUse 决定；proto 带 source/permission_mode/tool_use_id 与 decided_by`。

### Task 3: 守护进程：本机设置、模式分支、去重表、审计字段

**Files:**
- Create: `internal/agent/settings.go`、`settings_test.go`、`recent.go`、`recent_test.go`
- Modify: `internal/agent/server.go:356-395,482-503`、`internal/agent/audit.go:14-31`、`internal/agent/config.go`（`SettingsPath`）、`cmd/railse/agent.go`（`reload` 时重读设置）
- Test: `internal/agent/server_test.go`

**Interfaces:**
- `type Settings struct { ClaudeAutoMode string \`yaml:"claude_auto_mode"\` }`，`func LoadSettings(path string) (Settings, error)`（文件不存在 → 默认 `classifier`；值只能 `classifier`/`phone`，别的报错并回默认）。`Config.SettingsPath = <dir>/settings.yaml`。
- `type recent struct` 带 `mu`、`map[string]recentEntry{outcome string; at time.Time}`，方法 `key(req proto.Request) string`（`req.ToolUseID` 非空用它，否则 `session.cli_session_id + "\x00" + sha256(payload JSON)`）、`remember(key, outcome)`、`lookup(key) (outcome string, ok bool)`，10 分钟过期，容量 1000。
- `AuditEntry` 加 `Source`、`PermissionMode`、`ToolUseID`（都 `omitempty`）。

- [ ] **Step 1: 写失败的测试**（`server_test.go`，用现有 `startServer`/`permission`/`readAudit`/`fixedDecider`/`blockingDecider`）

```go
// auto 模式下中间三级不表态、硬底线仍送 Decider；default 模式下中间三级送 Decider。
func TestPreToolUseModeBranches(t *testing.T) {
	dec := &fixedDecider{out: Outcome{Action: "deny", Reason: "已在手机上拒绝", DecidedBy: "user"}}
	env := startServer(t, dec, nil)
	mk := func(cmd, mode, id string) proto.Request {
		r := permission(env.repo, cmd)
		r.Source, r.PermissionMode, r.ToolUseID = "pretooluse", mode, id
		return r
	}
	cases := []struct {
		name, cmd, mode, wantDecision, wantState string
	}{
		{"auto 读 本地放行", "git status", "auto", "allow", "auto_allowed"},
		{"auto 联网 不表态", "curl https://x.example/", "auto", "passthrough", "passthrough"},
		{"auto 往外发 送手机", "git push origin main", "auto", "deny", "denied"},
		{"auto 破坏性 送手机", "rm -rf build", "auto", "deny", "denied"},
		{"default 联网 送手机", "curl https://x.example/", "default", "deny", "denied"},
		{"bypass 联网 不表态", "curl https://x.example/", "bypassPermissions", "passthrough", "passthrough"},
	}
	for i, c := range cases {
		var resp proto.Response
		proto.Decode(roundTrip(t, env.sock, mk(c.cmd, c.mode, fmt.Sprintf("toolu_%d", i))), &resp)
		if resp.Decision != c.wantDecision {
			t.Errorf("%s: decision=%s want %s (reason %s)", c.name, resp.Decision, c.wantDecision, resp.Reason)
		}
	}
	entries := readAudit(t, env.cfg.AuditPath)
	// 不表态的审计 decided_by 为 classifier、state passthrough、source pretooluse
	var sawClassifier bool
	for _, e := range entries {
		if e.DecidedBy == "classifier" && e.State == "passthrough" && e.Source == "pretooluse" && e.PermissionMode == "auto" {
			sawClassifier = true
		}
	}
	if !sawClassifier {
		t.Fatalf("审计里没有 classifier 不表态的记录：%+v", entries)
	}
}

// settings.yaml 的 claude_auto_mode: phone 让 auto 模式下中间三级也送手机
func TestPreToolUseAutoModePhoneSetting(t *testing.T) {
	dec := &fixedDecider{out: Outcome{Action: "allow", DecidedBy: "user", Reason: "已在手机上批准"}}
	env := startServer(t, dec, func(c *Config) {
		os.WriteFile(c.SettingsPath, []byte("claude_auto_mode: phone\n"), 0o644)
	})
	r := permission(env.repo, "curl https://x.example/")
	r.Source, r.PermissionMode, r.ToolUseID = "pretooluse", "auto", "toolu_p"
	var resp proto.Response
	proto.Decode(roundTrip(t, env.sock, r), &resp)
	if resp.Decision != "allow" || resp.DecidedBy != "user" {
		t.Fatalf("resp=%+v", resp)
	}
}

// PreToolUse 送手机后被交回终端，同一 tool_use_id 的 PermissionRequest 不再送手机；
// PreToolUse 因模式不表态的，同 id 的 PermissionRequest 照常送手机。
func TestPreToolUseThenPermissionRequestDedup(t *testing.T) {
	calls := 0
	dec := deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
		calls++
		return Outcome{Action: "passthrough", DecidedBy: "terminal", Reason: "交回终端问了"}, nil
	})
	env := startServer(t, dec, nil)
	pre := permission(env.repo, "git push origin main")
	pre.Source, pre.PermissionMode, pre.ToolUseID = "pretooluse", "auto", "toolu_same"
	roundTrip(t, env.sock, pre)
	pr := permission(env.repo, "git push origin main")
	pr.Source, pr.PermissionMode, pr.ToolUseID = "permissionrequest", "auto", "toolu_same"
	var resp proto.Response
	proto.Decode(roundTrip(t, env.sock, pr), &resp)
	if calls != 1 || resp.Decision != "passthrough" || resp.DecidedBy != "dedup" {
		t.Fatalf("calls=%d resp=%+v", calls, resp)
	}
	// 无 tool_use_id 时按 会话+载荷 去重
	pre2 := permission(env.repo, "git push origin dev")
	pre2.Source, pre2.PermissionMode = "pretooluse", "auto"
	roundTrip(t, env.sock, pre2)
	pr2 := permission(env.repo, "git push origin dev")
	pr2.Source, pr2.PermissionMode = "permissionrequest", "auto"
	proto.Decode(roundTrip(t, env.sock, pr2), &resp)
	if calls != 2 || resp.DecidedBy != "dedup" {
		t.Fatalf("无 id 的去重失败：calls=%d resp=%+v", calls, resp)
	}
	// 因模式不表态的（curl 在 auto 下），PermissionRequest 要送手机
	pre3 := permission(env.repo, "curl https://x.example/")
	pre3.Source, pre3.PermissionMode, pre3.ToolUseID = "pretooluse", "auto", "toolu_c"
	roundTrip(t, env.sock, pre3)
	pr3 := permission(env.repo, "curl https://x.example/")
	pr3.Source, pr3.PermissionMode, pr3.ToolUseID = "permissionrequest", "auto", "toolu_c"
	proto.Decode(roundTrip(t, env.sock, pr3), &resp)
	if calls != 3 {
		t.Fatalf("分类器升级成提问后应送手机：calls=%d", calls)
	}
}
```

（`deciderFunc` 若不存在，加一个 `type deciderFunc func(context.Context, PendingRequest) (Outcome, error)` 实现 `Decider`。）

- [ ] **Step 2: 跑确认失败**。
- [ ] **Step 3: 实现**

`handlePermission` 的 `default:` 分支改为：

```go
	default:
		if s.rules.Paused() { /* 原样 */ }
		key := s.recent.key(req)
		if req.Source == proto.SourcePermissionRequest {
			if out, ok := s.recent.lookup(key); ok && out == "terminal" {
				// PreToolUse 已送过手机、用户选了交回终端：这次是 Claude 自己在问，不再打扰手机
				entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "dedup", i18n.T("刚才已在手机上交回终端，这次不再问")
				resp.Decision, resp.DecidedBy, resp.Reason = "passthrough", "dedup", entry.Reason
				break
			}
		}
		if req.Source == proto.SourcePreToolUse && d.Risk < policy.RiskOutward && lenient(req.PermissionMode) && s.settings.ClaudeAutoMode != "phone" {
			// auto / bypassPermissions / dontAsk：中间三级交给 Claude 自己的分类器，Railse 不比它更吵
			entry.Action, entry.State, entry.DecidedBy, entry.Reason = "passthrough", "passthrough", "classifier", i18n.T("auto 模式下交给 Claude 的分类器")
			resp.Decision, resp.DecidedBy, resp.Reason = "passthrough", "classifier", entry.Reason
			s.recent.remember(key, "classifier")
			break
		}
		resp = s.waitDecision(ctx, id, req, d, pctx.ProjectRoot, redacted, entry)
		if req.Source == proto.SourcePreToolUse && resp.Decision == "passthrough" {
			s.recent.remember(key, "terminal")
		}
		return resp
```

`lenient(mode)` 对 `auto`、`bypassPermissions`、`dontAsk` 为真。`applyOutcome` 把 `out.DecidedBy` 也写进 `resp.DecidedBy`。审计 entry 在创建时填 `Source/PermissionMode/ToolUseID`。`Server` 加 `settings Settings` 与 `recent *recent`，`New` 时 `LoadSettings(cfg.SettingsPath)`（读不到用默认并记日志），`reload` 控制命令里重读。风险比较：`policy.Risk` 已按顺序定义（read < write_in_project < unknown < write_outside < network < outward < destructive），确认它是可比较的整数类型；不是就加 `func (r Risk) AtLeast(o Risk) bool`。

- [ ] **Step 4: 跑测试**：`RAILSE_LANG=zh go test ./internal/agent/ -count=1 -race`。
- [ ] **Step 5: 提交**：`agent: PreToolUse 审批的模式分支（硬底线任何模式送手机，中间三级 auto 下交给分类器，settings.yaml 可改）；按 tool_use_id/会话+载荷去重；审计带来源与模式`。

### Task 4: 钩子：透传字段、hub 不可达的终端提示

**Files:**
- Modify: `cmd/railse/hook.go:162-169,197-226`、`internal/i18n/en.go`
- Test: `cmd/railse/hook_test.go`

- [ ] **Step 1: 写失败的测试**

```go
// PreToolUse 的 Bash 经钩子得到 PreToolUse 格式的放行 JSON（规则：git status 只读）
func TestHookClaudePreToolUseAllow(t *testing.T) {
	sock, repo, _ := startTestAgent(t)
	stdin := claudePreToolUse(repo, "git status", "auto") // 新辅助：按 pretooluse-bash-auto.json 的形状造输入
	stdout, _, code := runHookWith(t, stdin, "hook", "claude", "--socket", sock)
	if code != 0 || !strings.Contains(stdout, `"hookEventName":"PreToolUse"`) || !strings.Contains(stdout, `"permissionDecision":"allow"`) {
		t.Fatalf("code=%d stdout=%s", code, stdout)
	}
}

// hub 不可达时钩子不表态，并在 stderr 提示回退
func TestHookHubUnreachableNote(t *testing.T) {
	sock, lines := fakeDaemon(t, `{"v":1,"decision":"passthrough","decided_by":"hub_unreachable","reason":"连不上 hub，交回终端处理"}`)
	_ = lines
	stdout, stderr, _ := runHookWith(t, claudePreToolUse("/tmp/p", "git push origin main", "auto"), "hook", "claude", "--socket", sock)
	if stdout != "" || !strings.Contains(stderr, "hub") {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}
```

- [ ] **Step 2: 跑确认失败**。
- [ ] **Step 3: 实现**：`hookPermission` 构造请求时带 `Source/PermissionMode/ToolUseID`；渲染时 `adapter.Decision{Source: in.Source, Action: resp.Decision, Reason: resp.Reason}`；若 `resp.DecidedBy == "hub_unreachable"` 则 `fmt.Fprintln(stderr, "railse hook: "+i18n.T("连不上 hub，交回 Claude 自己的确认"))`。`en.go` 补该句英文 `hub unreachable, falling back to Claude's own prompt`。
- [ ] **Step 4: 跑测试**：`RAILSE_LANG=zh go test ./cmd/railse/ -count=1`（`TestHookClaudeAskOnlyRendersAnswer` 的注释"绝不能对 PreToolUse 写出审批 JSON"改为"答题类 PreToolUse 不写审批 JSON"）。
- [ ] **Step 5: 提交**：`hook: PreToolUse 审批透传来源与模式；hub 不可达时终端提示回退`。

### Task 5: 钩子安装与 doctor

**Files:**
- Modify: `internal/hooksconf/hooksconf.go:56-87,322-373`、`internal/doctor/hooks.go:294-368`、`internal/i18n/en.go`
- Test: `internal/hooksconf/*_test.go`、`internal/doctor/hooks_test.go`

- [ ] **Step 1: 测试**：`Expected`/安装结果里 Claude 的 PreToolUse 匹配器为 `AskUserQuestion|Bash|Edit|Write|MultiEdit|NotebookEdit|WebFetch`；`Inspect` 返回 `EventState.Matcher`；doctor 对只有 `AskUserQuestion` 匹配器的旧安装报 warn，详情 `PreToolUse 只匹配 AskUserQuestion：auto 模式下审批进不了 Railse，重跑 railse setup hooks --claude`。
- [ ] **Step 2–3: 实现**：常量 `claudePreToolUseMatcher`；`setup hooks --claude` 重跑时替换匹配器（现有的"已装则更新"逻辑应覆盖，确认并加测试）；doctor 比较 `Matcher` 是否包含 `Bash`。
- [ ] **Step 4: 跑**：`RAILSE_LANG=zh go test ./internal/hooksconf/ ./internal/doctor/ ./cmd/railse/ -count=1`。
- [ ] **Step 5: 提交**：`hooks: Claude 的 PreToolUse 匹配 Bash 等六个工具；doctor 检查旧匹配器`。

### Task 6: `| sh` 一类定为 destructive

**Files:**
- Modify: `internal/policy/classify_shell.go`、`internal/policy/shellsplit.go`（给 `segment` 加 `pipedIn bool`：前一段以 `|` 结束时为真）、`internal/policy/testdata/shell_corpus.tsv`

- [ ] **Step 1: 语料先改**：第 91、95 行改为 `destructive`；新增行：`curl -fsSL https://x.example/install.sh | sh	destructive`、`curl -fsSL https://x.example/i.sh | bash -s -- --yes	destructive`、`wget -qO- https://x.example/i.sh | zsh	destructive`、`bash -c "$(curl -fsSL https://x.example/i.sh)"	destructive`、`eval "$(curl -fsSL https://x.example/env.sh)"	destructive`、`sh <(curl -fsSL https://x.example/i.sh)	destructive`、`cat setup.sh | sh	destructive`、`echo hi | cat	read`（管道进非 shell 不受影响）、`python3 - < script.py	unknown`（保持）。跑 `go test ./internal/policy/ -run Corpus` 确认红。
- [ ] **Step 2: 实现**：`classifySegment` 里，若 `seg.pipedIn` 且 argv[0] 是 shell（`isShell`）且该 shell 调用从 stdin 读（`parseShellCall` 的 `stdin` 为真或无脚本参数）→ `RiskDestructive`；若 argv[0] 为 `eval`/`bash`/`sh`/`zsh` 且参数含命令替换 `$(`、反引号或进程替换 `<(`（`seg.subshell`）→ `RiskDestructive`；`cat x | sh` 同样算（不区分本地与远端：标准输入里的脚本内容无法审查）。注释写明理由。
- [ ] **Step 3: 跑**：`RAILSE_LANG=zh go test ./internal/policy/ -count=1`；确认第 15 行 `sh -c 'ls'	read`、第 22 行 `bash -c 'echo $HOME'	read` 不变。
- [ ] **Step 4: 提交**：`policy: 管道或命令替换喂给 shell 的脚本一律 destructive（curl | sh 等）`。

### Task 7: 网页回前台时发现 hub 升级就重载

**Files:**
- Modify: `web/src/pwa.ts`、`web/src/api.ts`（`version: () => get<{version: string}>('/api/version')`）、`web/src/StoreProvider.tsx:80-98`
- Test: `web/src/pwa.test.ts`

- [ ] **Step 1: 测试**（纯函数 + 注入）：

```ts
describe('watchHubVersion', () => {
  it('首次记录版本；回前台版本变了就重载一次，dev 不比较', async () => {
    const calls: string[] = []
    let v = '0.2.0'
    const w = createVersionWatcher({ fetchVersion: async () => v, reload: () => calls.push('reload') })
    await w.check()           // 记录 0.2.0
    await w.check(); expect(calls).toEqual([])
    v = '0.2.1'; await w.check(); expect(calls).toEqual(['reload'])
    await w.check(); expect(calls).toEqual(['reload'])   // 只重载一次
    const d = createVersionWatcher({ fetchVersion: async () => 'dev', reload: () => calls.push('x') })
    await d.check(); await d.check(); expect(calls).toEqual(['reload'])
  })
})
```

- [ ] **Step 2–3: 实现**：`createVersionWatcher({fetchVersion, reload})` 返回 `{check}`；取不到版本时忽略；`StoreProvider` 的 `onVisibility` 里加 `void versionWatcher.check()`，挂载时先 `check()` 一次记录。
- [ ] **Step 4: 跑**：`cd web && npm run check`。
- [ ] **Step 5: 提交**：`web: 回到前台时发现 hub 版本变了自动重载一次`。

### Task 8: 文档与 README

**Files:**
- Modify: `README.md:22,116-128`、`README.en.md:35,148-163`、`docs/hub.md`、`docs/agent-local-loop.md`、`docs/setup.md`（升级说明）

- [ ] **Step 1:** "它怎么做"第 1 条改为：Claude Code 的每个 Bash / 文件写 / 联网调用在执行前（PreToolUse）先经 Railse，不论哪种权限模式；Codex 与 Gemini 走各自的权限事件。
- [ ] **Step 2:** 限制里加两条：auto 模式下联网、项目外写、没见过的命令默认交给 Claude 自己的分类器（`~/.railse/settings.yaml` 的 `claude_auto_mode: phone` 可改）；PreToolUse 放行后 Claude 会跳过自己的检查。删掉 README.en 第 151 行"Chinese only"。
- [ ] **Step 3:** 能力矩阵表（两份 README 的"它怎么做"之后）：

| | 能批 | 只能拒 | 只能提醒 | 能答题 | auto/yolo 模式下生效 |
|---|---|---|---|---|---|
| Claude Code | 是 | — | 等你说话、完成 | 是（AskUserQuestion） | 是（硬底线任何模式；中间三级默认交给分类器） |
| Codex | 是（交互会话） | — | 完成 | 否 | 看 Codex 自身的 approval_policy |
| Gemini CLI | 否 | 是 | 问题、完成 | 否 | 否（yolo 跳过钩子） |

- [ ] **Step 4:** 升级说明：从 0.2.0 升到 0.2.1 要重跑 `railse setup hooks --claude`（匹配器变了），doctor 会提示。
- [ ] **Step 5: 提交**：`docs: 审批走 PreToolUse 的说法、能力矩阵、升级说明；删掉 README.en 过时的中文限制`。

### Task 9: 全量门禁

- [ ] `make test`、`make e2e`、`scripts/test-install.sh`、`scripts/english-smoke.sh <bin>` 全绿；`RAILSE_LANG=en go test ./internal/i18n/`（完整性）。
- [ ] 最终报告：分支与提交号、Task 1 的两条实测结论、与计划的偏差、留给主会话的真机验收清单（设计 §4 的 7 条）。

---

## 自查

- 设计覆盖：§2.1 Task 5；§2.2–2.3 Task 2、3；§2.4 Task 3；§2.5 Task 8；§2.6 Task 3、4；§3 三件 Task 6、7、8；§4 验收由主会话做，Task 9 列清单。
- 占位：`ASK_OR_EMPTY` 由 Task 1 定，是刻意留给实测的分支，不是占位。
- 一致性：`Source` 常量、`DecidedBy` 值（`classifier`、`dedup`、`terminal`、`hub_unreachable`）、`SettingsPath`、`createVersionWatcher` 在各任务一致。
