# Railse OPC v0.1.5：手机上答 Claude Code 的选择题（AskUserQuestion）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Claude Code 用 `AskUserQuestion` 问人（「用方案 A 还是 B」）时，问题推到手机，手机上选一个选项或自己填一句，agent 立刻拿到答案继续跑；手机上也可以把问题交回终端。只支持 Claude Code；Codex、Gemini 没有这个口子，照旧只提醒。

**Architecture:** Claude Code 的 `PreToolUse` 钩子（matcher `AskUserQuestion`）把题目交给守护进程，守护进程不过策略引擎，直接作为新种类 `ask` 的请求送到 hub；hub 推到手机，网页新增答题页；答案经 `POST /api/requests/{id}/answer` 回到 hub → 守护进程 → 钩子，钩子按官方格式返回 `permissionDecision: "allow"` 加 `updatedInput`（原 `questions` 原样回填，再加 `answers`），Claude Code 就不再在终端弹问题。任何一环不通（没接 hub、hub 不可达、等待过期、手机选「让它在终端问我」）钩子都不输出，Claude Code 照旧在终端问，与审批一样不卡 agent。

**Tech Stack:** Go 标准库（hub、守护进程、钩子）、SQLite 迁移到 schema v3、Vite/React 网页、Playwright 端到端。不新增依赖。

**已在真机验证的事实（2026-10-31，Claude Code 2.1.280，交互模式）：** `PreToolUse` 收到的 stdin 顶层键有 `session_id, prompt_id, cwd, hook_event_name, tool_name, tool_use_id, tool_input, permission_mode, transcript_path, scratchpad_dir, effort`；`tool_input` 形如 `{"questions":[{"question":"你更喜欢苹果还是香蕉？","header":"水果","options":[{"label":"Apple","description":"苹果"},{"label":"Banana","description":"香蕉"}],"multiSelect":false}]}`。钩子 stdout 写 `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{"questions":[…原样…],"answers":{"你更喜欢苹果还是香蕉？":"Banana"}}}}` 后，终端显示「User answered Claude's questions: … → Banana」，不弹选项；`answers` 的值不是选项标签（如「都不喜欢，我只吃橙子」）也被原样接受。多选题官方文档说用逗号连接各标签。用 `PermissionRequest` 钩子对 `AskUserQuestion` 回 allow 无效（文档明示）。

## Global Constraints

- Go 不新增 require；npm 不新增依赖。中文文案、注释、提交信息；代码贴近周围写法。
- 答题不经策略引擎，也不能「记住」；`ask` 请求不参与规则、不计入自动审批统计。
- 题目、标题、选项文字在守护进程侧先过 `redact.Text` 再上报与写审计（agent 的问题里可能拼进凭据）；答案是用户自己打的字，原样回给钩子，但审计里也过 `redact.Text`。
- 守护进程任何故障都不能卡 agent：没接 hub、hub 不可达、过期、出错，钩子都不输出（passthrough），Claude Code 在终端照常问。
- 钩子输出里 `questions` 必须是 Claude 发来的原样（逐字节回填 `tool_input` 里的其他字段），只加 `answers`。
- 测试一律用临时目录与临时 HOME，不碰真实的 `~/.claude`、`~/.railse`、`~/.railse-hub`，不真跑 Claude Code；`make test`、`go test ./... -race -count=1`、`make e2e` 全过才提交；不 push、不合并、不打 tag。
- 用户可见的改动同步到 `README.md`、`README.en.md`、`docs/web.md`、`docs/agent-local-loop.md`、`docs/hub.md`；设计文档 §0.6 加一行、§2.1 的 kind/state 列表补 `ask`/`answered`。

## 文件总览

| 文件 | 职责 |
| --- | --- |
| `internal/proto/proto.go` | `Ask`、`Question`、`Option` 类型；`Request.Ask`、`Response.Answers` |
| `internal/adapter/adapter.go`、`claude.go`、`claude_test.go` | `KindAsk`；解析 `PreToolUse`/`AskUserQuestion`；渲染 `PreToolUse` 输出；`PermissionRequest`+`AskUserQuestion` 一律 passthrough |
| `cmd/railse/hook.go`、`hook_test.go` | `hookAsk`：与 `hookPermission` 同一条路，输出前把 `answers` 合进原 `tool_input` |
| `internal/hooksconf/hooksconf.go`、测试 | Claude 的 `Plan`/`Expected` 加 `PreToolUse`（matcher `AskUserQuestion`，timeout 86400） |
| `internal/doctor/hooks.go`、测试 | 跟着 `Expected` 认新事件（旧安装没有 PreToolUse 时 warn，提示重跑 `railse setup hooks --claude`） |
| `internal/agent/server.go`、`requests.go`、`hubdecider.go`、`audit.go`、测试 | `handleAsk`；`PendingRequest.Kind/Ask`；`Outcome.Answers`；审计 kind `ask` |
| `internal/hub/wire/wire.go` | `PendingRequest.Kind`、`.Ask`；`Decision.Answers`；Action `answer` |
| `internal/hub/store/schema.sql`、`store.go`、`requests.go`、测试 | schema v3：`ask`、`answers` 列；state `answered`；`AnswerRequest` |
| `internal/hub/agents.go`、`requests.go`、`api.go`、`pushsvc.go`、测试 | 收 `ask` 帧；`POST /api/requests/{id}/answer`；decide 拒绝 ask；收件箱含 ask；推送文案 |
| `web/src/types.ts`、`explain.ts`、`format.ts`、`pages/Request.tsx`、新 `components/AskForm.tsx`、`components/RequestCard.tsx`、`api.ts`、`store.ts`、测试、`web/e2e/ask.spec.ts` | 答题页、收件箱卡片、结果文案、端到端 |
| 文档 | 见 Global Constraints |

---

### Task 1: 协议与 Claude 适配器

**Files:**
- Modify: `internal/proto/proto.go`
- Modify: `internal/adapter/adapter.go`、`internal/adapter/claude.go`
- Test: `internal/adapter/claude_test.go`（新增用例）、`internal/proto/proto_test.go`

**Interfaces（后面的 Task 都用这些名字）：**

```go
// proto
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header,omitempty"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
}
// Ask 是 Claude Code 的 AskUserQuestion 要人答的题，原样转述，不做判断。
type Ask struct {
	Questions []Question `json:"questions"`
}
// Request 加：Ask *Ask `json:"ask,omitempty"`（Kind 为 "ask" 时有，这时 Payload 为零值、ToolName 为 "AskUserQuestion"）
// Response 加：Answers map[string]string `json:"answers,omitempty"`（Decision 为 "answer" 时有：问题原文 → 答案）
// Decision 的取值加 "answer"。

// adapter
const KindAsk Kind = "ask"
// Input 加：Ask *proto.Ask；RawToolInput json.RawMessage（PreToolUse 的 tool_input 原文，渲染时回填）
// Decision 加：Answers map[string]string；RawToolInput json.RawMessage
// ValidateAsk 校验题目：1 到 4 题；每题 question 非空且不超过 2000 字符；options 0 到 8 个，label 非空且不超过 200 字符；
// header 不超过 60 字符。不合规返回错误，调用方按 KindIgnore 处理（终端照常问）。
func ValidateAsk(a *proto.Ask) error
// preToolUseAnswer 把 answers 合进 rawToolInput（JSON 对象，其他字段逐字节不动，已有 answers 键则覆盖）并包成
// {"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":<合并后>}}。
func preToolUseAnswer(rawToolInput json.RawMessage, answers map[string]string) ([]byte, error)
```

**要求：**

1. `claude.Parse`：`hook_event_name == "PreToolUse"` 且 `tool_name == "AskUserQuestion"` → 解析 `tool_input` 为 `proto.Ask`，`ValidateAsk` 通过则 `Kind = KindAsk`、`ToolName = "AskUserQuestion"`、`Ask`、`RawToolInput = tool_input 原文`；不通过或解析失败 → `KindIgnore`（stderr 说一句原因由 hook.go 做）。其他工具的 `PreToolUse` → `KindIgnore`。
2. `PermissionRequest` 且 `tool_name == "AskUserQuestion"` → `KindIgnore`（之前会变成一条「无法判断」的审批，批了也没用）。
3. `claude.Render`：`d.Action == "answer"` 且 `d.RawToolInput` 非空 → `preToolUseAnswer`；合并失败回 passthrough。其他动作照旧 `renderPermission`。
4. `preToolUseAnswer` 用 `map[string]json.RawMessage` 解 `rawToolInput` 再加 `answers` 键，保证 `questions` 等字段原样。
5. 测试用本文开头那段真实 `tool_input` 作 fixture；覆盖：正常题、多题、多选题、空 options、5 题（拒）、question 为空（拒）、`PreToolUse` 别的工具（忽略）、`PermissionRequest`+`AskUserQuestion`（忽略）、渲染输出与官方 JSON 逐字段相等、`rawToolInput` 里多余字段原样保留。

- [ ] 写失败测试 → 跑 `go test ./internal/adapter/ ./internal/proto/` 看失败 → 实现 → 通过 → `make test` → 提交：`git commit -m "adapter: Claude 的 PreToolUse/AskUserQuestion 解析为 ask 请求，答案按官方格式回填 updatedInput；PermissionRequest 的 AskUserQuestion 一律交回终端"`

---

### Task 2: 钩子安装与体检认 PreToolUse

**Files:**
- Modify: `internal/hooksconf/hooksconf.go`（`Plan("claude")` 加 `"PreToolUse": {{Matcher: strp("AskUserQuestion"), Hooks: []Entry{{Type: "command", Command: cmd, Timeout: 86400}}}}`；`Expected("claude")` 加 `"PreToolUse": 86400`）
- Modify: `internal/doctor/hooks.go`（按 `Expected` 自然覆盖；确认 matcher 不为空时 `IsOurs`/合并仍认得我们的条目）
- Modify: `cmd/railse/setup.go` 的 `setupUsage`/`setupAllUsage`（Claude 钩子说明加一句「问题也能在手机上答」）
- Test: `internal/hooksconf/hooksconf_test.go`、`internal/doctor/doctor_test.go`、`cmd/railse/setup_test.go`

**要求：** 重跑 `railse setup hooks --claude` 在旧配置上只新增 `PreToolUse` 组、别的条目逐字节不变（现有 `Merge` 的保证，加测试钉住）；旧安装（没有 PreToolUse）体检 `hooks.claude` 报 warn「缺 PreToolUse：Claude Code 的选择题进不了手机」，修复提示 `railse setup hooks --claude`；`--dry-run` 输出里能看到新组。

- [ ] 写失败测试 → 实现 → `make test` → 提交：`git commit -m "hooks: Claude 加 PreToolUse（AskUserQuestion）钩子，体检认它"`

---

### Task 3: 守护进程：ask 请求送 hub 等答案

**Files:**
- Modify: `internal/hub/wire/wire.go`（`PendingRequest` 加 `Kind string \`json:"kind,omitempty"\``（空当 permission）与 `Ask *proto.Ask \`json:"ask,omitempty"\``；`Decision` 加 `Answers map[string]string \`json:"answers,omitempty"\``，Action 取值加 `answer`）
- Modify: `internal/agent/requests.go`（`PendingRequest` 加 `Kind string`、`Ask *proto.Ask`（已脱敏）；`Outcome` 加 `Answers map[string]string`）
- Modify: `internal/agent/server.go`（`handleConn` 加 `case "ask"`；新 `handleAsk`）
- Modify: `internal/agent/hubdecider.go`（上报帧带 Kind、Ask；`settle` 把 `Decision.Answers` 放进 `Outcome`）
- Modify: `internal/agent/hubclient.go` 等（按需）、`internal/agent/audit.go`（`AuditEntry` 加 `Ask *proto.Ask`、`Answers map[string]string`，omitempty）
- Modify: `cmd/railse/hook.go`（`case adapter.KindAsk` → `hookAsk`）
- Test: `internal/agent/server_test.go`、`hubdecider_test.go`、`cmd/railse/hook_test.go`

**`handleAsk` 的行为：**
- 不跑引擎、不看规则、不看暂停开关（答题不是审批）。
- 脱敏：`Ask` 的每个 question/header/option.label/option.description 过 `redact.Text`，得到 `RedactedAsk`；上报 hub 与审计都用它。
- 没接 hub（`s.decider` 是 `LocalDecider`，或 hub 状态 none）→ 立即 `Response{Decision:"passthrough", Reason:"没接 hub，交回终端"}`，审计 state `passthrough`、decided_by `no_hub`。
- 否则同 `waitDecision`：登记 `PendingRequest{Kind:"ask", Ask: redacted, Req: req}`，`decider.Decide`；结果映射：`answer` → `Response{Decision:"answer", Answers}`，审计 state `answered`、decided_by `user`、`Answers` 过 `redact.Text` 后记；`passthrough`（手机选了「让它在终端问我」，或 hub 不可达、过期、出错）→ 与审批相同的 state/decided_by 口径；钩子消失 → `cancelled/terminal`，不写回。
- `HubDecider` 对 `ask`：上报 `wire.PendingRequest{Kind:"ask", Ask:…, ToolName:"AskUserQuestion", Risk:""}`，其余（30 秒 grace、重连、cancel、expired、delivered 回执）与审批完全一样；`settle` 时 `Decision.Action == "answer"` → `Outcome{Action:"answer", Answers: dec.Answers, DecidedBy: dec.DecidedBy}`。
- `hook.go` 的 `hookAsk`：与 `hookPermission` 同样的信号、父进程、期限处理；`client.Permission(ctx, proto.Request{Kind:"ask", Ask: in.Ask, ToolName:"AskUserQuestion", DeadlineSeconds})`；`resp.Decision == "answer"` 时 `ad.Render(Decision{Action:"answer", Answers: resp.Answers, RawToolInput: in.RawToolInput})`，否则不输出。`hookclient.Client.Permission` 的名字不改（它就是一问一答），注释说明 ask 也走它。
- 测试：假 Decider 返回 answer → 钩子 stdout 是合规 PreToolUse JSON 且 `questions` 与输入逐字节相同；没接 hub → 空输出、审计 no_hub；hub 不可达 → 空输出；题目含 `ghp_` 令牌 → 上报帧与审计里被脱敏；钩子中途退出 → cancelled。

- [ ] 写失败测试 → 实现 → `make test` → 提交：`git commit -m "agent: ask 请求不过引擎直接送 hub 等答案，答案回填钩子；没接 hub 或不通时交回终端"`

---

### Task 4: hub：存、推、答

**Files:**
- Modify: `internal/hub/store/schema.sql`、`store.go`（`SchemaVersion = 3`，迁移：`ALTER TABLE requests ADD COLUMN ask TEXT NOT NULL DEFAULT ''; ALTER TABLE requests ADD COLUMN answers TEXT NOT NULL DEFAULT '';`；注释里 kind 加 `ask`，state 加 `answered`）
- Modify: `internal/hub/store/requests.go`（`Request` 加 `Ask *proto.Ask \`json:"ask,omitempty"\``、`Answers map[string]string \`json:"answers,omitempty"\``，读写两列；新 `AnswerRequest(id string, answers map[string]string, passthrough bool, now time.Time) (Request, error)`：pending 且 kind ask 才改，answered/passthrough + decided_by user + decided_at；状态校验表加 `answered`）
- Modify: `internal/hub/agents.go`（`onPending`：`f.Kind == "ask"` 时存 `Ask`，`payload` 列写 `{}`，risk 空；`Kind` 空当 permission）
- Modify: `internal/hub/requests.go`（`Decide` 对 kind ask 返回 `errBadAction`（文案「这是提问，用答题接口」）；新 `Answer(id, answers, passthrough)`：校验 → `store.AnswerRequest` → `sendDecision` 发 `wire.Decision{Action:"answer"|"passthrough", Answers, DecidedBy:"user"}` → 审计 `answer`/`passthrough`；`pushWorthy`、过期扫描、吊销取消都把 ask 当 pending 同等对待）
- Modify: `internal/hub/api.go`（`POST /api/requests/{id}/answer`，cookie + 同源；请求体 `{"answers":{"<问题原文>":"<答案>"},"passthrough":false}` 至多 64 KiB；校验：非 passthrough 时每道题都要有键、值非空且不超过 1000 字符、多余的键 400；404/409 口径同 decide；返回请求视图。`GET /api/inbox` 的 `pending` 包含 kind ask）
- Modify: `internal/hub/pushsvc.go`（kind ask：标题 `pushTitle(cliName+" 在问你", project, machine)`，正文第一行第一道题的 question（clean），题数大于 1 时加「 等 N 题」，第二行「点开回答」；`pushWorthy` 认 ask）
- Modify: `internal/hub/webui` 无改动；`docs/hub.md` API 表加一行
- Test: `store/requests_test.go`（迁移 v2→v3 保留旧数据；AnswerRequest 各分支）、`hub/requests_test.go`、`api_test.go`（answer 的 400/404/409、同源、passthrough、decide 拒绝 ask）、`pushsvc_test.go`、`agents_test.go`（收 ask 帧）

- [ ] 写失败测试 → 实现 → `make test` → 提交：`git commit -m "hub: ask 请求入库（schema v3）、推送、答题接口 /api/requests/{id}/answer，答案下发守护进程"`

---

### Task 5: 网页：答题页、收件箱卡片、结果文案、端到端

**Files:**
- Modify: `web/src/types.ts`（`Kind` 加 `'ask'`；`RequestState` 加 `'answered'`；`RequestView` 加 `ask?: { questions: Question[] }`、`answers?: Record<string, string>`；`Question`、`Option` 类型）
- Modify: `web/src/api.ts`（`answer: (id, body: { answers?: Record<string,string>; passthrough?: boolean }) => post<RequestView>(\`/api/requests/${enc(id)}/answer\`, body)`）
- Modify: `web/src/explain.ts`（kind ask：headline `${cli} 在问你`，what `在问你：<第一题，clip 60>`，where 同前，why 空，next 待决时 `['选一个答案，agent 立刻继续。', '也可以自己填一句。', '「让它在终端问我」会把问题交回终端。']`）
- Create: `web/src/components/AskForm.tsx`（每道题：header 小标签、question 正文、选项列表（单选 radio / 多选 checkbox，带 description）、「自己填」输入框（填了就清掉选项的选择）；底部「回答」按钮（每题都有答案才可点；多选把选中的 label 用「, 」连接；自己填的用原文）与次要链接「让它在终端问我」；提交中禁用；409 等错误复用 `ResultToast` 的口径）
- Modify: `web/src/pages/Request.tsx`（kind ask 且 pending → `AskForm`，不显示拒绝/批准；已处理 → 每题一行「问题 → 你的答案」）
- Modify: `web/src/format.ts`（`kindLabel('ask') = '提问'`；`stateLabel('answered') = '已回答'`；`outcomeText`：answered+user → `你答了`，ask 的 passthrough+user → `交回终端问了`）
- Modify: `web/src/components/RequestCard.tsx`（ask：第一行 what，第二行第一题的选项标签用「 / 」连起来，右侧标签「提问」，无风险标签）
- Modify: `web/src/store.ts`、`StoreProvider.tsx`（ask 在待决集合里：收旧通知、角标计数都算它）
- Create: `web/e2e/ask.spec.ts`（照 `approve.spec.ts` 与 `fixtures.ts` 的方式起真 hub、守护进程：后台跑 `railse hook claude` 喂一条 PreToolUse/AskUserQuestion（fixture 同 Task 1），网页收件箱出现「在问你」卡片 → 进去选 Banana → 回答 → 页面显示「你答了」→ 断言钩子 stdout 的 JSON 里 `updatedInput.answers` 是 `{"…":"Banana"}` 且 `questions` 与输入相同；第二个用例走「让它在终端问我」→ 钩子无输出）
- Modify: `web/src/sw.js`：不改（tag 仍是请求 id）
- Test: `explain.test.ts`、`components.test.tsx`、`pages.test.tsx`、`format` 测试

- [ ] 写失败测试 → 实现 → `make test && make e2e` → 提交：`git commit -m "web: 答题页（单选、多选、自己填、交回终端）、收件箱提问卡片、结果文案；端到端覆盖答题与交回终端"`

---

### Task 6: 文档与设计文档

- `README.md`/`README.en.md`：第一段与「它怎么做」加一句：Claude Code 问你选哪个方案时，手机上直接选，agent 立刻继续；「现状与限制」改写 Gemini/Codex 那条：只有 Claude Code 的选择题能在手机上答，agent 停下来等你说下一句话的仍要回终端。
- `docs/agent-local-loop.md`：钩子表加 `PreToolUse`（matcher AskUserQuestion）；「三家 CLI 的差异」表加一行「手机上答问题：Claude 能，Codex、Gemini 不能」；审计 kind 加 ask。
- `docs/web.md`：答题页。`docs/hub.md`：`/api/requests/{id}/answer`、kind ask、state answered、推送文案。
- 设计文档 §0.6 加「v0.1.5：手机上答 Claude Code 的选择题（2026-10-31 已拍板）」一行（含真机验证的事实与「自由文本等待不做」的结论）；§2.1 kind 加 `ask`、state 加 `answered`。
- [ ] 改文档 → `grep -n "answer\|ask" docs/*.md README*.md` 对一遍 → 提交：`git commit -m "docs: 手机上答 Claude Code 的选择题；钩子、接口、限制同步"`

---

## 手动验收（维护者在真机做）

1. `railse setup hooks --claude`（会备份 `~/.claude/settings.json` 并加 PreToolUse 组），`railse doctor` 的 `hooks.claude` ok。
2. 在一个 Claude Code 信任的目录里跑：`claude "Use the AskUserQuestion tool to ask me exactly one question: do I prefer apple or banana, with two options labeled Apple and Banana. After the tool returns, tell me which option was chosen, nothing else."` 手机收到「Claude Code 在问你 · <项目>」，点开选 Banana → 回答 → 终端显示「User answered Claude's questions … → Banana」。
3. 再来一次，手机上「自己填」一句 → 终端显示那句话。
4. 再来一次，手机上「让它在终端问我」→ 终端弹出原生选项。
5. 多选题各一次（让 Claude 出一道 multiSelect 的题）。

## 遗留与不做

- agent 停下来等下一句话（Notification idle_prompt）仍只提醒：钩子没有注入消息的途径，Channels 是研究预览，另议。
- Codex、Gemini 的问题不做。
- `curl … | sh` 定级（应为不可逆）另开一条修。
