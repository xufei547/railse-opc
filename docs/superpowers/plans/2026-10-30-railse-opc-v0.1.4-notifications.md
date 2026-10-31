# v0.1.4 通知说人话、批过的自动收掉 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 手机推送的标题、正文让人一眼看懂“谁要做什么、在哪、我该干嘛”；请求处理完之后，通知栏里它的旧通知在网页拿到收件箱状态时自动收掉。

**Architecture:** Task 1 只动 hub（Go）：`pushMessage` 改成按 CLI、风险动词、项目、（多机时）机器名拼标题，正文两行；`message` 负责查 `sessions.tool` 与数机器。Task 2 只动网页：新模块 `web/src/notifications.ts` 用 `ServiceWorkerRegistration.getNotifications()` 关掉 tag（= 请求 id）不在待决集合里的通知，节流到一秒一次；`StoreProvider` 在收件箱状态变化时调它。`sw.js` 的点击处理已经先 `close()` 再开页，只补测试。

**Tech Stack:** Go 1.26（hub，SQLite store）；TypeScript + React 19 + Vitest（网页，测试环境 node）；`web/public/sw.js` 纯 ES。

## Global Constraints

- 文案、注释、提交信息一律中文；提交信息风格照仓库现有的（`hub: …`、`web: …`、`docs: …`）。
- Go 不新增 require：`go.mod`、`go.sum` 不得改动。网页不新增依赖：`web/package.json`、`web/package-lock.json` 不得改动。
- 测试用临时目录（`newTestHub`、`openTemp` 已经是），不碰真实家目录。
- 每个 Task 提交前 `make test` 全过（gofmt、golangci-lint、vet、`go test ./... -race`、`tsc --noEmit` 与 vitest）。
- 不 push、不合并、不打 tag。
- 用户可见的改动同步到 `docs/web.md`、`docs/hub.md` 对应段落。
- 推送载荷长度保证不变：编码后不超过 `push.MaxPlaintext`（`encodePushPayload`），标题至多 `pushTitleLen`（80）个字符、正文至多 `pushBodyLen`（200）个字符。
- 设计决策已记在 `docs/superpowers/specs/2026-10-21-railse-opc-design.md` §0.6（与本计划同一个提交）。

## 设计取舍（执行前已定）

- **“砍”的意思。** 标题超长时先把机器名从尾部截短到放得下，截没了连同分隔符去掉；还超再同样处理项目名；最后整体截断（只是兜底：固定的开头至多二十来个字符，走不到）。截短而不是整段去掉：机器名、项目名的前缀通常已经够分辨。
- **CLI 名。** `sessions.tool` 只会是 `claude`、`codex`、`gemini`（`proto.Session.Tool`），空与不认识的值都显示“Agent”。
- **数机器出错时**（读库失败）当作多台：宁可标题多带机器名。查会话出错时记日志、按查不到处理（“Agent”）。
- **待决集合 = 待审批（`state.pending`）∪ 还在等回答的问题（`state.questions`）。** question 也推通知（tag 是问题的请求 id），问题还在等时它的通知要留着；问题不再等（同一会话来了新事件）时收掉。
- **取注册用 `navigator.serviceWorker.getRegistration()`，不用 `ready`。** 没有注册（注册失败、还没注册）时 `ready` 永远不 resolve（`pwa.ts` 的 `getSubscription` 已为此改用 `getRegistration`）；`getNotifications()` 在注册对象上就能调，不需要 worker 已激活。
- **节流是“先收一次、间隔末尾用最新集合补一次”。** 空闲时立即收；一秒内再来的只记下最新的待决集合，一秒到时补收一次。只丢不补的话，间隔里被批掉的那条要等下一次状态变化才收。
- **已知竞态（接受）。** 网页加载收件箱的那一刻正好来了新请求的推送、而这次快照里还没有它，它的通知会被当成已处理收掉。窗口只有一次 `/api/inbox` 往返那么长；hub 先经 `/ws/client` 广播再推送，实时连接在线时网页总是先知道新请求；而且只在网页开着时发生，收件箱随后会显示这条。不为它引入时间戳比对。另一种情形是回到前台、重拉收件箱还没完成时先到一帧：同样只在网页开着时发生，收件箱随后会显示，接受。
- **`sw.js` 不改代码。** `notificationclick` 已经第一句就 `event.notification.close()`，再 `openTarget`；Task 2 只补一条先后顺序的测试，以及两行正文原样弹出的测试。

---

### Task 1: 推送文案说人话（hub）

**Files:**
- Modify: `internal/hub/store/requests.go`（`UpsertSession` 之后加 `SessionTool`）
- Test: `internal/hub/store/requests_test.go`
- Modify: `internal/hub/pushsvc.go`（常量 `pushSummaryLen` 注释、删 `pushRiskLen`；`message`、`pushMessage`；新增 `pushWho`、`cliName`、`riskVerb`、`machineLabel`、`pushTitle`、`(*pushService).sessionTool`、`(*pushService).manyMachines`）
- Test: `internal/hub/pushsvc_test.go`（改写 `TestPushMessageContent`，改 `TestPushMessageWorstCase`、`TestPushMessageZeroWidthPadding`，新增 4 个测试）
- Modify: `docs/hub.md`（“手机推送”的“推什么”、“安全要点”的“退出登录与设备丢失”）、`docs/web.md`（“通知”的第一条）

**Interfaces:**
- Consumes: 现有 `store.Request`（`SessionID`、`Kind`、`Risk`、`Payload`、`Title`）、`(*Server).view`（`MachineName`、`Project`）、`store.ListMachines`（`Machine.Revoked`）、`payloadSummary`、`clean`、`truncate`、`joinNonEmpty`、`encodePushPayload`。
- Produces:
  - `func (s *Store) SessionTool(id string) (string, error)`：没有这一行时 `ErrNotFound`。
  - `type pushWho struct { Tool, Project, Machine string; Many bool }`
  - `func pushMessage(r store.Request, w pushWho) ([]byte, error)`（签名由 `(r, machine, project string)` 改来，唯一调用方是 `message` 与测试）
  - `func pushTitle(head, project, machine string) string`
  - 载荷形状不变：`{"id","kind","title","body","url"}`；permission 的 `body` 含一个 `\n`（Task 2 的 sw 测试用到）。

- [ ] **Step 1: 写 store 的失败测试**

`internal/hub/store/requests_test.go`，放在 `TestSessionUpsert` 后面：

```go
// SessionTool 按会话行 id 取 tool；没有这一行时 ErrNotFound（推送文案据此显示 CLI 名）。
func TestSessionTool(t *testing.T) {
	s := openTemp(t)
	m, _ := s.CreateMachine("m", RandomToken(), "/h")
	id, err := s.UpsertSession(m.ID, proto.Session{Tool: "codex", CLISessionID: "s1", Cwd: "/p"}, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if tool, err := s.SessionTool(id); err != nil || tool != "codex" {
		t.Fatalf("SessionTool = %q, %v", tool, err)
	}
	if tool, err := s.SessionTool("missing"); !errors.Is(err, ErrNotFound) || tool != "" {
		t.Fatalf("查不到应 ErrNotFound: %q, %v", tool, err)
	}
}
```

- [ ] **Step 2: 跑，确认失败**

Run: `go test ./internal/hub/store -run TestSessionTool -count=1`
Expected: 编译失败 `s.SessionTool undefined`。

- [ ] **Step 3: 实现 `SessionTool`**

`internal/hub/store/requests.go`，紧跟 `UpsertSession`：

```go
// SessionTool 返回会话行的 tool（claude、codex、gemini）；没有这一行时返回 ErrNotFound。
func (s *Store) SessionTool(id string) (string, error) {
	var tool string
	err := s.db.QueryRow("SELECT tool FROM sessions WHERE id = ?", id).Scan(&tool)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return tool, nil
}
```

Run: `go test ./internal/hub/store -run TestSessionTool -count=1` → PASS。

- [ ] **Step 4: 写推送文案的失败测试**

`internal/hub/pushsvc_test.go`：把整个 `TestPushMessageContent` 换成下面的帮助函数与 5 个测试（`decodePush` 供各测试共用）。`proto` 已在该文件的 import 里。

```go
// pushMsg 是解出来的推送载荷。
type pushMsg struct{ ID, Kind, Title, Body, URL string }

func decodePush(t *testing.T, b []byte, err error) pushMsg {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	var m pushMsg
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
	return m
}

// permission 的标题是“<CLI> 要<动词>”：动词按风险档，CLI 按 sessions.tool，没有会话、查不到或不认识的叫 Agent。
func TestPushMessagePermissionVerbs(t *testing.T) {
	perm := store.Request{ID: "r1", Kind: "permission", ToolName: "Bash", Payload: policy.Payload{Type: policy.PayloadShell, Command: "ls"}}
	for _, c := range []struct{ risk, want string }{
		{"network", "Claude Code 要联网"},
		{"outward", "Claude Code 要往外发"},
		{"destructive", "Claude Code 要做不可逆的操作"},
		{"write_outside", "Claude Code 要改项目外的文件"},
		{"unknown", "Claude Code 要跑一个没见过的命令"},
		{"write_in_project", "Claude Code 要改项目里的文件"},
		{"read", "Claude Code 要读文件"},
		{"", "Claude Code 要做一件事"},
		{"weird", "Claude Code 要做一件事"},
	} {
		r := perm
		r.Risk = c.risk
		if m := decodePush(t, pushMessage(r, pushWho{Tool: "claude"})); m.Title != c.want {
			t.Errorf("风险 %q：标题 %q，要 %q", c.risk, m.Title, c.want)
		}
	}
	perm.Risk = "network"
	for tool, want := range map[string]string{"claude": "Claude Code 要联网", "codex": "Codex 要联网", "gemini": "Gemini CLI 要联网", "": "Agent 要联网", "cursor": "Agent 要联网"} {
		if m := decodePush(t, pushMessage(perm, pushWho{Tool: tool})); m.Title != want {
			t.Errorf("tool %q：标题 %q，要 %q", tool, m.Title, want)
		}
	}
}

// 有项目时标题追加项目；机器多于一台时再追加机器名（去掉结尾的 .local，不分大小写）。正文两行：摘要、“点开批准或拒绝”，
// 不再有“风险 network”与工具名。
func TestPushMessagePermissionWhereAndBody(t *testing.T) {
	perm := store.Request{ID: "r1", Kind: "permission", ToolName: "Bash", Risk: "network",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "  curl\n\thttps://example.com "}}
	m := decodePush(t, pushMessage(perm, pushWho{Tool: "claude", Project: "blog", Machine: "xufeideMacBook-Air.local"}))
	if m.ID != "r1" || m.Kind != "permission" || m.Title != "Claude Code 要联网 · blog" || m.Body != "curl https://example.com\n点开批准或拒绝" || m.URL != "/r/r1" {
		t.Fatalf("单机: %+v", m)
	}
	for _, c := range []struct {
		w    pushWho
		want string
	}{
		{pushWho{Tool: "claude"}, "Claude Code 要联网"},
		{pushWho{Tool: "claude", Machine: "mac.local"}, "Claude Code 要联网"},
		{pushWho{Tool: "codex", Project: "blog", Machine: "xufeideMacBook-Air.local", Many: true}, "Codex 要联网 · blog · xufeideMacBook-Air"},
		{pushWho{Tool: "codex", Project: "blog", Machine: "office.LOCAL", Many: true}, "Codex 要联网 · blog · office"},
		{pushWho{Tool: "codex", Project: "blog", Machine: "linux-box", Many: true}, "Codex 要联网 · blog · linux-box"},
		{pushWho{Tool: "codex", Machine: "mac.local", Many: true}, "Codex 要联网 · mac"},
		{pushWho{Tool: "codex", Project: "blog", Many: true}, "Codex 要联网 · blog"},
	} {
		if m := decodePush(t, pushMessage(perm, c.w)); m.Title != c.want {
			t.Errorf("%+v：标题 %q，要 %q", c.w, m.Title, c.want)
		}
	}
	for _, c := range []struct {
		pl   policy.Payload
		want string
	}{
		{policy.Payload{Type: policy.PayloadFileWrite, Path: "/p/a.go"}, "/p/a.go\n点开批准或拒绝"},
		{policy.Payload{Type: policy.PayloadNetwork, URL: "https://x.example/a"}, "https://x.example/a\n点开批准或拒绝"},
		{policy.Payload{Type: policy.PayloadMCP, Server: "gh", Tool: "create_issue"}, "gh/create_issue\n点开批准或拒绝"},
	} {
		r := perm
		r.ToolName, r.Payload = "Edit", c.pl
		if m := decodePush(t, pushMessage(r, pushWho{Tool: "claude"})); m.Body != c.want {
			t.Errorf("%s：正文 %q，要 %q", c.pl.Type, m.Body, c.want)
		}
	}
	long := perm
	long.Payload.Command = strings.Repeat("x", 500)
	m = decodePush(t, pushMessage(long, pushWho{Tool: "claude"}))
	if first, second, ok := strings.Cut(m.Body, "\n"); !ok || first != strings.Repeat("x", pushSummaryLen) || second != "点开批准或拒绝" {
		t.Fatalf("摘要应截到 %d 个字符、第二行仍在: %q", pushSummaryLen, m.Body)
	}
}

// question：“<CLI> 在终端等你回答”，同样追加项目与（多机时）机器名，正文是问题；webhook：“工作流等你批准”，正文是来源给的标题。
func TestPushMessageQuestionAndWebhook(t *testing.T) {
	q := store.Request{ID: "q1", Kind: "question", Title: "要不要\n继续？"}
	m := decodePush(t, pushMessage(q, pushWho{Tool: "gemini", Project: "blog", Machine: "mac.local", Many: true}))
	if m.Title != "Gemini CLI 在终端等你回答 · blog · mac" || m.Body != "要不要 继续？" || m.URL != "/r/q1" || m.Kind != "question" {
		t.Fatalf("question: %+v", m)
	}
	if m := decodePush(t, pushMessage(q, pushWho{Tool: "claude", Project: "blog", Machine: "mac"})); m.Title != "Claude Code 在终端等你回答 · blog" {
		t.Fatalf("question 单机: %+v", m)
	}
	if m := decodePush(t, pushMessage(q, pushWho{})); m.Title != "Agent 在终端等你回答" {
		t.Fatalf("question 查不到会话: %+v", m)
	}
	wh := store.Request{ID: "w1", Kind: "webhook", Title: "部署到生产"}
	if m := decodePush(t, pushMessage(wh, pushWho{Project: "x", Machine: "mac", Many: true})); m.Title != "工作流等你批准" || m.Body != "部署到生产" || m.URL != "/r/w1" {
		t.Fatalf("webhook: %+v", m)
	}
	// id 里的特殊字符在 url 里转义
	odd := wh
	odd.ID = "a/b?c"
	if m := decodePush(t, pushMessage(odd, pushWho{})); m.ID != "a/b?c" || m.URL != "/r/a%2Fb%3Fc" {
		t.Fatalf("转义: %+v", m)
	}
}

// 标题超长：先把机器名从尾部截短（截没了连同分隔符去掉），再截项目名，最后整体截断；截断处不留尾随空格。
func TestPushTitleTrim(t *testing.T) {
	const head = "Claude Code 要联网" // 15 个字符
	proj := strings.Repeat("p", 20)
	mach := strings.Repeat("m", pushTitleLen-15-3-20-3) // 正好 80 个字符
	if got := pushTitle(head, proj, mach); got != head+" · "+proj+" · "+mach {
		t.Fatalf("放得下不应砍: %q", got)
	}
	if got := pushTitle(head, proj, mach+"M"); got != head+" · "+proj+" · "+mach {
		t.Fatalf("多一个字符应先砍机器名: %q", got)
	}
	if got := pushTitle(head, strings.Repeat("p", 100), "mac"); got != head+" · "+strings.Repeat("p", pushTitleLen-15-3) {
		t.Fatalf("机器名砍没了再砍项目名: %q", got)
	}
	if got := pushTitle(strings.Repeat("h", 100), "proj", "mac"); got != strings.Repeat("h", pushTitleLen) {
		t.Fatalf("最后整体截断: %q", got)
	}
	if got := pushTitle(head, strings.Repeat("p", 61)+" q", ""); got != head+" · "+strings.Repeat("p", 61) {
		t.Fatalf("截断处不留尾随空格: %q", got)
	}
	for _, c := range [][3]string{{head, strings.Repeat("项", 200), strings.Repeat("机", 200)}, {head, "", strings.Repeat("机", 200)}} {
		if n := utf8.RuneCountInString(pushTitle(c[0], c[1], c[2])); n > pushTitleLen {
			t.Fatalf("标题 %d 个字符", n)
		}
	}
}

// message 经真实的库：按 session_id 查 sessions.tool；单机不带机器名，第二台接入后带上（去 .local），吊销后又不带；
// 没有会话、会话查不到时 CLI 是 Agent。
func TestPushMessageLooksUpToolAndMachines(t *testing.T) {
	h := newTestHub(t)
	m1, err := h.st.CreateMachine("xufeideMacBook-Air.local", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := h.st.UpsertSession(m1.ID, proto.Session{Tool: "codex", CLISessionID: "s1", Cwd: "/p/blog"}, "blog")
	if err != nil {
		t.Fatal(err)
	}
	r := store.Request{ID: "r1", MachineID: m1.ID, SessionID: sid, Kind: "permission", ToolName: "shell", Risk: "network",
		Payload: policy.Payload{Type: policy.PayloadShell, Command: "curl https://example.com"}, ProjectRoot: "/p/blog"}
	title := func() string {
		t.Helper()
		b, err := h.srv.push.message(r)
		return decodePush(t, b, err).Title
	}
	if got := title(); got != "Codex 要联网 · blog" {
		t.Fatalf("单机: %q", got)
	}
	m2, err := h.st.CreateMachine("linux-box", store.RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	if got := title(); got != "Codex 要联网 · blog · xufeideMacBook-Air" {
		t.Fatalf("两台: %q", got)
	}
	if err := h.st.RevokeMachine(m2.ID); err != nil {
		t.Fatal(err)
	}
	if got := title(); got != "Codex 要联网 · blog" {
		t.Fatalf("吊销一台后: %q", got)
	}
	r.SessionID = ""
	if got := title(); got != "Agent 要联网 · blog" {
		t.Fatalf("没有会话: %q", got)
	}
	r.SessionID = "missing"
	if got := title(); got != "Agent 要联网 · blog" {
		t.Fatalf("会话查不到: %q", got)
	}
}
```

同时改两个现有测试：

`TestPushMessageWorstCase` 的调用与断言换成（其余不变）：

```go
		b, err := pushMessage(r, pushWho{Tool: long, Project: projectName(r.ProjectRoot), Machine: long, Many: true})
		// …（大小、合法 JSON、id、url、kind 的检查不变）…
		if strings.ContainsAny(m["title"], "\x01\n ") {
			t.Fatalf("%s: 标题不应带控制字符与换行: %q", kind, m["title"])
		}
		body := m["body"]
		if kind == "permission" {
			// 正文只有规整之后拼上的那一个换行，第二行是固定提示
			first, second, ok := strings.Cut(body, "\n")
			if !ok || second != "点开批准或拒绝" || strings.ContainsAny(first, "\x01\n ") {
				t.Fatalf("%s: 正文 %q", kind, body)
			}
		} else if strings.ContainsAny(body, "\x01\n ") {
			t.Fatalf("%s: 正文不应带控制字符与换行: %q", kind, body)
		}
```

`TestPushMessageZeroWidthPadding`：调用改成 `pushMessage(r, pushWho{Tool: "claude"})`，期望正文改成 `"rm -rf ~/x\n点开批准或拒绝"`。

- [ ] **Step 5: 跑，确认失败**

Run: `go test ./internal/hub -run 'TestPushMessage|TestPushTitle' -count=1`
Expected: 编译失败（`pushWho`、`pushTitle` undefined，`pushMessage` 参数个数不对）。

- [ ] **Step 6: 实现**

`internal/hub/pushsvc.go`：

1. 常量：删掉 `pushRiskLen`；`pushSummaryLen` 的注释改成 `// permission 正文第一行（载荷摘要）的字符上限`。
2. 把 `message`、`pushMessage` 整段换成下面的（`payloadSummary`、`clean`、`joinNonEmpty`、`encodePushPayload`、`shrink` 不动）：

```go
// cliNames 是推送文案里 CLI 的显示名，键是 sessions.tool。
var cliNames = map[string]string{"claude": "Claude Code", "codex": "Codex", "gemini": "Gemini CLI"}

// riskVerbs 是 permission 标题“<CLI> 要<动词>”里按风险等级取的动词。
var riskVerbs = map[string]string{
	"network":          "联网",
	"outward":          "往外发",
	"destructive":      "做不可逆的操作",
	"write_outside":    "改项目外的文件",
	"unknown":          "跑一个没见过的命令",
	"write_in_project": "改项目里的文件",
	"read":             "读文件",
}

// cliName 是 sessions.tool 的显示名；空（没有会话、查不到）与不认识的都叫“Agent”。
func cliName(tool string) string {
	if n, ok := cliNames[tool]; ok {
		return n
	}
	return "Agent"
}

// riskVerb 是风险等级对应的动词；不认识的是“做一件事”。
func riskVerb(risk string) string {
	if v, ok := riskVerbs[risk]; ok {
		return v
	}
	return "做一件事"
}

// machineLabel 去掉机器名结尾的 .local（macOS 的默认主机名带它，不分大小写）；整个名字就是 .local 时原样返回。
func machineLabel(name string) string {
	const suffix = ".local"
	if len(name) > len(suffix) && strings.EqualFold(name[len(name)-len(suffix):], suffix) {
		return name[:len(name)-len(suffix)]
	}
	return name
}

// pushWho 是推送文案里的“谁、在哪”：Tool 是请求所属会话的 sessions.tool（没有会话或查不到为空），Project 是项目名，
// Machine 是机器名，Many 为真表示接入且未吊销的机器多于一台（这时标题才带机器名）。
type pushWho struct {
	Tool, Project, Machine string
	Many                   bool
}

// message 组一条请求的推送载荷：机器名、项目名取自请求视图，CLI 按 session_id 查 sessions.tool，再数一下接入且未吊销的机器。
func (p *pushService) message(r store.Request) ([]byte, error) {
	v := p.s.view(r)
	return pushMessage(r, pushWho{Tool: p.sessionTool(r), Project: v.Project, Machine: v.MachineName, Many: p.manyMachines()})
}

// sessionTool 是请求所属会话的 tool；没有会话、查不到时为空，读库出错时记日志、也为空。
func (p *pushService) sessionTool(r store.Request) string {
	if r.SessionID == "" {
		return ""
	}
	tool, err := p.s.store.SessionTool(r.SessionID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			p.s.log.Printf("读请求 %s 的会话: %v", clip(r.ID), err)
		}
		return ""
	}
	return tool
}

// manyMachines 判断接入且未吊销的机器是否多于一台；读库出错时记日志、当作多台（宁可标题多带机器名）。
func (p *pushService) manyMachines() bool {
	ms, err := p.s.store.ListMachines()
	if err != nil {
		p.s.log.Printf("列机器: %v", err)
		return true
	}
	n := 0
	for _, m := range ms {
		if !m.Revoked {
			n++
		}
	}
	return n > 1
}

// pushMessage 组推送载荷 {"id","kind","title","body","url":"/r/<id>"}，文案说人话（谁要做什么、在哪、该干嘛）：
//   - permission：标题“<CLI> 要<动词>”（动词按风险，见 riskVerbs），正文两行：载荷摘要、“点开批准或拒绝”；
//   - question：标题“<CLI> 在终端等你回答”，正文是问题；
//   - webhook：标题“工作流等你批准”，正文是来源给的标题。
//
// permission、question 的标题有项目时追加“ · 项目”，w.Many 时再追加“ · 机器名”（去掉 .local），超长的砍法见 pushTitle。
// 项目名、机器名、摘要、问题与 webhook 标题都先并成一行（控制字符、换行当空白，去掉不可见的格式字符）再按字符截断；
// permission 正文的两行在规整之后才用 \n 连接。编码后仍保证不超过 push.MaxPlaintext（见 encodePushPayload）。
func pushMessage(r store.Request, w pushWho) ([]byte, error) {
	machine := ""
	if w.Many {
		machine = machineLabel(w.Machine)
	}
	var title, body string
	switch r.Kind {
	case "permission":
		title = pushTitle(cliName(w.Tool)+" 要"+riskVerb(r.Risk), w.Project, machine)
		body = joinNonEmpty("\n", clean(payloadSummary(r.Payload), pushSummaryLen), "点开批准或拒绝")
	case "question":
		title = pushTitle(cliName(w.Tool)+" 在终端等你回答", w.Project, machine)
		body = clean(r.Title, pushBodyLen)
	default:
		title = "工作流等你批准"
		body = clean(r.Title, pushBodyLen)
	}
	m := pushPayload{ID: r.ID, Kind: r.Kind, Title: truncate(title, pushTitleLen), Body: truncate(body, pushBodyLen), URL: "/r/" + url.PathEscape(r.ID)}
	return encodePushPayload(m, push.MaxPlaintext)
}

// pushTitle 拼“head · 项目 · 机器名”（空的段连同分隔符省掉），至多 pushTitleLen 个字符：超了先把机器名从尾部截短到放得下
// （截没了连同分隔符去掉），还超再同样截项目名，最后整体截断。项目名、机器名先经 clean 并成一行，截断处不留尾随空格。
func pushTitle(head, project, machine string) string {
	const sep = " · "
	parts := []string{head, clean(project, pushTitleLen), clean(machine, pushTitleLen)}
	for i := len(parts) - 1; i > 0; i-- {
		over := utf8.RuneCountInString(joinNonEmpty(sep, parts...)) - pushTitleLen
		if over <= 0 {
			break
		}
		if keep := utf8.RuneCountInString(parts[i]) - over; keep > 0 {
			parts[i] = clean(parts[i], keep)
		} else {
			parts[i] = ""
		}
	}
	return truncate(joinNonEmpty(sep, parts...), pushTitleLen)
}
```

- [ ] **Step 7: 跑，确认通过**

Run: `go test ./internal/hub/... -run 'TestPush|TestEncodePush|TestSessionTool' -count=1`
Expected: PASS（含原有的 `TestPushNeverLeaksSecrets`、`TestEncodePushPayloadShrinks` 等）。

- [ ] **Step 8: 更新文档**

`docs/hub.md`“手机推送（Web Push）”的“推什么”一条，把“标题至多 80 个字符……正文是来源给的标题。”这几句换成：

```markdown
标题至多 80 个字符、正文至多 200 个字符，控制字符与换行并成空格（permission 正文两行之间的换行除外），转义后仍超就继续截正文。文案说人话：permission 的标题是“<CLI> 要<动词>”，CLI 按请求所属会话的 `sessions.tool`（`claude`→Claude Code、`codex`→Codex、`gemini`→Gemini CLI，没有会话或查不到时为 Agent），动词按风险（`network` 联网、`outward` 往外发、`destructive` 做不可逆的操作、`write_outside` 改项目外的文件、`unknown` 跑一个没见过的命令、`write_in_project` 改项目里的文件、`read` 读文件，其余“做一件事”），有项目时追加“ · 项目”，接入且未吊销的机器多于一台时再追加“ · 机器名”（去掉结尾的 `.local`），超长时先截机器名、再截项目名；正文第一行是载荷摘要（命令、路径、URL 或 `服务/工具`，至多 160 个字符），第二行“点开批准或拒绝”。question 的标题是“<CLI> 在终端等你回答”（同样追加项目与机器名），正文是问题；webhook 的标题是“工作流等你批准”，正文是来源给的标题。
```

（“明文载荷 … 编码后不超过 3 KiB（Apple 上限 4 KiB）：”这半句与后面的“投递参数：…”保持不变。）

`docs/hub.md`“安全要点”的“退出登录与设备丢失”：把“锁屏上照样会收到“机器名 · 项目”与命令摘要”改成“锁屏上照样会收到谁要做什么、项目与命令摘要”。

`docs/web.md`“通知”的第一条（“什么时候推”）最后一句“审批的通知标题是“机器名 · 项目”，正文是工具、命令摘要与风险；同一条请求用请求 id 作 tag，通知中心只留一条。”换成：

```markdown
同一条请求用请求 id 作 tag，通知中心只留一条。
- **长什么样**：审批的标题是谁要做什么，例如“Claude Code 要联网 · blog”（Claude Code、Codex 或 Gemini CLI，认不出时写 Agent；动词按风险：联网、往外发、做不可逆的操作、改项目外的文件、跑一个没见过的命令、改项目里的文件、读文件），后面跟项目名，接了不止一台机器时再跟机器名（去掉 `.local`）；正文第一行是命令、路径、网址或 MCP 的“服务/工具”，第二行“点开批准或拒绝”。question 的标题是“Claude Code 在终端等你回答 · 项目”，正文是问题（要去终端回答）；webhook 的标题是“工作流等你批准”，正文是来源给的标题。
```

- [ ] **Step 9: 全量测试并提交**

Run: `make test`
Expected: 全过（gofmt、lint 无输出；`go test ./... -race` ok；`npm run check` 通过）。`git diff --stat -- go.mod go.sum` 为空。

```bash
git add internal/hub/store/requests.go internal/hub/store/requests_test.go internal/hub/pushsvc.go internal/hub/pushsvc_test.go docs/hub.md docs/web.md
git commit -m "hub: 推送文案说人话：标题“<CLI> 要<动词> · 项目（· 机器名）”，正文摘要加“点开批准或拒绝”；question、webhook 同改"
```

---

### Task 2: 批过的旧通知自动收掉（网页）

**Files:**
- Create: `web/src/notifications.ts`
- Test: `web/src/notifications.test.ts`
- Modify: `web/src/store.ts`（加 `waitingIDs`）
- Test: `web/src/store.test.ts`
- Modify: `web/src/StoreProvider.tsx`（状态变化时调 `closeStaleNotifications`）
- Test: `web/src/sw.test.ts`（两行正文、点击先关再开；顺手把示例载荷换成新文案）
- Modify: `docs/web.md`（“通知”加一条“已处理的自动收掉”）

**Interfaces:**
- Consumes: Task 1 的载荷形状（permission 的 `body` 是 `摘要\n点开批准或拒绝`）；`sw.js` 用请求 id 作 tag、测试通知 tag 为 `railse-test`、兜底通知 tag 为 `railse`；`State`（`loaded`、`pending`、`questions`）。
- Produces:
  - `web/src/notifications.ts`：`interface ShownNotification { readonly tag: string; close(): void }`、`interface NotificationSource { getNotifications(): Promise<ShownNotification[]> }`、`swNotifications(): Promise<NotificationSource | null>`、`closeStale(waiting: ReadonlySet<string>, source?): Promise<number>`、`closeInterval = 1000`、`staleCloser(close?, interval?): (pendingIDs: Iterable<string>) => void`、`closeStaleNotifications: (pendingIDs: Iterable<string>) => void`。
  - `web/src/store.ts`：`waitingIDs(state: Pick<State, 'loaded' | 'pending' | 'questions'>): string[] | null`。

- [ ] **Step 1: 写失败测试**

新建 `web/src/notifications.test.ts`：

```ts
import { afterEach, describe, expect, it, vi } from 'vitest'
import { closeStale, staleCloser, swNotifications, type NotificationSource } from './notifications'

/** 假的注册：getNotifications 回给定 tag 的通知，close 记进 closed。 */
function fakeSource(tags: string[]) {
  const closed: string[] = []
  const src: NotificationSource = {
    getNotifications: () => Promise.resolve(tags.map((tag) => ({ tag, close: () => void closed.push(tag) }))),
  }
  return { src, closed }
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('closeStale', () => {
  it('只关 tag 不在待决集合里的（含测试通知与兜底通知）', async () => {
    const { src, closed } = fakeSource(['a', 'b', 'railse-test', 'c', 'railse'])
    expect(await closeStale(new Set(['a', 'c']), () => Promise.resolve(src))).toBe(3)
    expect(closed).toEqual(['b', 'railse-test', 'railse'])
  })

  it('待决集合为空：全部关掉；全在集合里：一条不关', async () => {
    const all = fakeSource(['a', 'b'])
    expect(await closeStale(new Set(), () => Promise.resolve(all.src))).toBe(2)
    expect(all.closed).toEqual(['a', 'b'])
    const none = fakeSource(['a', 'b'])
    expect(await closeStale(new Set(['a', 'b', 'z']), () => Promise.resolve(none.src))).toBe(0)
    expect(none.closed).toEqual([])
  })

  it('没有 Service Worker、没有注册、没有 getNotifications：静默跳过', async () => {
    await expect(closeStale(new Set(), () => Promise.resolve(null))).resolves.toBe(0)
    vi.stubGlobal('navigator', {})
    await expect(swNotifications()).resolves.toBeNull()
    await expect(closeStale(new Set())).resolves.toBe(0)
    vi.stubGlobal('navigator', { serviceWorker: { getRegistration: () => Promise.resolve(undefined) } })
    await expect(swNotifications()).resolves.toBeNull()
    vi.stubGlobal('navigator', { serviceWorker: { getRegistration: () => Promise.resolve({}) } })
    await expect(swNotifications()).resolves.toBeNull()
    await expect(closeStale(new Set())).resolves.toBe(0)
  })

  it('出错不抛：getRegistration、getNotifications 被拒，某条 close 抛错时其余照关', async () => {
    vi.stubGlobal('navigator', { serviceWorker: { getRegistration: () => Promise.reject(new Error('SecurityError')) } })
    await expect(closeStale(new Set())).resolves.toBe(0)
    const broken: NotificationSource = { getNotifications: () => Promise.reject(new Error('no')) }
    await expect(closeStale(new Set(), () => Promise.resolve(broken))).resolves.toBe(0)
    const closed: string[] = []
    const mixed: NotificationSource = {
      getNotifications: () =>
        Promise.resolve([
          {
            tag: 'x',
            close: () => {
              throw new Error('gone')
            },
          },
          { tag: 'y', close: () => void closed.push('y') },
        ]),
    }
    await expect(closeStale(new Set(), () => Promise.resolve(mixed))).resolves.toBe(1)
    expect(closed).toEqual(['y'])
  })

  it('默认经 navigator.serviceWorker.getRegistration 拿注册', async () => {
    const { src, closed } = fakeSource(['old', 'live'])
    vi.stubGlobal('navigator', { serviceWorker: { getRegistration: () => Promise.resolve(src) } })
    expect(await closeStale(new Set(['live']))).toBe(1)
    expect(closed).toEqual(['old'])
  })
})

describe('staleCloser：同一秒内至多收一次', () => {
  it('空闲时立即收；间隔内再来的只在间隔末尾用最新的集合补收一次', () => {
    vi.useFakeTimers()
    const calls: string[][] = []
    const run = staleCloser((w) => {
      calls.push([...w].sort())
      return Promise.resolve()
    }, 1000)
    run(['a'])
    expect(calls).toEqual([['a']])
    run(['a', 'b'])
    run(['b'])
    expect(calls).toHaveLength(1)
    vi.advanceTimersByTime(999)
    expect(calls).toHaveLength(1)
    vi.advanceTimersByTime(1)
    expect(calls).toEqual([['a'], ['b']])
    // 补收之后的一秒内仍然节流
    run(['c'])
    expect(calls).toHaveLength(2)
    vi.advanceTimersByTime(1000)
    expect(calls).toEqual([['a'], ['b'], ['c']])
    // 间隔里没人再要：到点不补收；空闲之后再来的立即收
    vi.advanceTimersByTime(5000)
    expect(calls).toHaveLength(3)
    run([])
    expect(calls).toEqual([['a'], ['b'], ['c'], []])
  })
})
```

`web/src/store.test.ts`：import 里加上 `waitingIDs`（`import { groupByProject, initialState, reduce, waitingIDs, type State } from './store'`），文件末尾加：

```ts
describe('waitingIDs', () => {
  it('还没加载过时为 null（空列表不代表没有待决）', () => {
    expect(waitingIDs(initialState)).toBeNull()
  })

  it('加载后是待审批与还在等回答的问题的 id；批掉的、不再等的不在里面', () => {
    let s = loaded([req({ id: 'q1', kind: 'question', session_id: 's9' })])
    expect(waitingIDs(s)?.sort()).toEqual(['a', 'b', 'q1'])
    s = reduce(s, frame({ type: 'request_updated', data: req({ id: 'a', created_at: '2026-10-23T10:00:01Z', state: 'approved' }) }))
    expect(waitingIDs(s)?.sort()).toEqual(['b', 'q1'])
    s = reduce(s, frame({ type: 'request_new', data: req({ id: 'd1', kind: 'done', session_id: 's9', state: 'recorded' }) }))
    expect(waitingIDs(s)).toEqual(['b'])
  })
})
```

`web/src/sw.test.ts`：

1. “hub 的审批推送”那条的示例载荷换成新文案，并断言两行正文原样弹出：

```ts
  it('hub 的审批推送：标题、两行正文原样、tag 为请求 id、点开去的 URL、图标', async () => {
    const sw = load()
    await sw.pushText(JSON.stringify({ id: 'abc123', kind: 'permission', title: 'Claude Code 要往外发 · blog', body: 'git push\n点开批准或拒绝', url: '/r/abc123' }))
    expect(sw.shown).toHaveLength(1)
    expect(sw.shown[0]?.title).toBe('Claude Code 要往外发 · blog')
    expect(sw.shown[0]?.options).toMatchObject({
      body: 'git push\n点开批准或拒绝',
      tag: 'abc123',
      data: { url: '/r/abc123' },
      icon: '/icons/icon-192.png',
    })
  })
```

（question 那条的示例标题 `'mac · blog：等你回答'` 同样换成 `'Claude Code 在终端等你回答 · blog'`，断言跟着换。）

2. “sw.js：点通知”里加一条：

```ts
  it('先关掉被点的那条，再找窗口、开页面', async () => {
    const sw = load()
    await sw.dispatch('notificationclick', { notification: { data: { url: '/r/abc' }, close: () => void sw.log.push('close') } })
    expect(sw.log[0]).toBe('close')
    expect(sw.log.indexOf('close')).toBeLessThan(sw.log.findIndex((l) => l.startsWith('matchAll')))
    expect(sw.log.at(-1)).toBe(`openWindow ${origin}/r/abc`)
  })
```

- [ ] **Step 2: 跑，确认失败**

Run: `cd web && npx vitest run src/notifications.test.ts src/store.test.ts src/sw.test.ts`
Expected: `notifications.test.ts` 失败（找不到 `./notifications`），`store.test.ts` 失败（`waitingIDs` 不是函数）；`sw.test.ts` 的新用例通过（`sw.js` 已经先 close、正文原样），属于回归护栏。

- [ ] **Step 3: 实现 `web/src/notifications.ts`**

```ts
// 收掉通知栏里已处理请求的通知。iOS 对不弹通知的推送会吊销订阅，hub 不能发静默推送来收：只能由网页在拿到收件箱状态时
// 关掉 tag 不在待决集合里的那些（Service Worker 用请求 id 作通知的 tag，测试通知是 railse-test）。

/** 通知栏里的一条通知：只用到 tag 与 close。 */
export interface ShownNotification {
  readonly tag: string
  close(): void
}

/** 能列出本站通知的 Service Worker 注册。 */
export interface NotificationSource {
  getNotifications(): Promise<ShownNotification[]>
}

/**
 * 本站的 Service Worker 注册。用 getRegistration 而不是 ready：没有注册（注册失败、还没注册）时 ready 永远不 resolve。
 * 没有 Service Worker、没有注册或注册上没有 getNotifications 时回 null。
 */
export async function swNotifications(): Promise<NotificationSource | null> {
  if (typeof navigator === 'undefined' || !('serviceWorker' in navigator)) return null
  const reg = await navigator.serviceWorker.getRegistration()
  if (!reg || typeof reg.getNotifications !== 'function') return null
  return reg
}

/** 关掉 tag 不在 waiting 里的通知，回关了几条。拿不到注册或 API、出错时静默跳过，不会 reject。 */
export async function closeStale(
  waiting: ReadonlySet<string>,
  source: () => Promise<NotificationSource | null> = swNotifications,
): Promise<number> {
  let shown: ShownNotification[]
  try {
    const src = await source()
    if (!src) return 0
    shown = await src.getNotifications()
  } catch {
    return 0
  }
  let n = 0
  for (const x of shown) {
    if (waiting.has(x.tag)) continue
    try {
      x.close()
      n++
    } catch {
      // 关不掉的跳过，下次状态更新再试
    }
  }
  return n
}

/** 收旧通知的节流间隔：同一秒内多次状态更新只收一次。 */
export const closeInterval = 1000

/**
 * 包成节流的“收掉旧通知”：空闲时立即收一次；间隔内再来的只记下最新的待决集合，间隔末尾用它补收一次（期间多次也只补一次）。
 * 补收用最新的集合，间隔里被批掉的那条也能收掉，不用等下一次状态变化。
 */
export function staleCloser(
  close: (waiting: ReadonlySet<string>) => Promise<unknown> = closeStale,
  interval = closeInterval,
): (pendingIDs: Iterable<string>) => void {
  let timer: ReturnType<typeof setTimeout> | null = null
  let queued: ReadonlySet<string> | null = null
  const fire = (ids: ReadonlySet<string>) => {
    void close(ids)
    timer = setTimeout(() => {
      timer = null
      const next = queued
      queued = null
      if (next) fire(next)
    }, interval)
  }
  return (pendingIDs) => {
    const ids = new Set(pendingIDs)
    if (timer) queued = ids
    else fire(ids)
  }
}

/** 网页拿到收件箱状态时调：关掉 tag 不在 pendingIDs（待审批与等你回答的请求 id）里的通知，同一秒内至多一次。 */
export const closeStaleNotifications = staleCloser()
```

- [ ] **Step 4: 实现 `waitingIDs` 并接进 `StoreProvider`**

`web/src/store.ts`，放在 `reduce` 之后：

```ts
/**
 * 待决的请求 id：待审批与还在等回答的问题。通知栏里 tag 不在其中的通知已经处理过，可以收掉。
 * 还没加载过收件箱时回 null（这时的空列表不代表没有待决，不能拿它收通知）。
 */
export function waitingIDs(state: Pick<State, 'loaded' | 'pending' | 'questions'>): string[] | null {
  if (!state.loaded) return null
  return [...state.pending, ...state.questions].map((r) => r.id)
}
```

`web/src/StoreProvider.tsx`：

```tsx
import { closeStaleNotifications } from './notifications'
import { initialState, reduce, waitingIDs, type Action, type State } from './store'
```

在 `const value = useMemo…` 之前加：

```tsx
  // 每次拿到收件箱状态（首次加载、实时帧带来的变化、自己批准或拒绝之后的重拉）都收掉已处理请求的通知，同一秒内至多一次
  const { loaded, pending, questions } = state
  useEffect(() => {
    const ids = waitingIDs({ loaded, pending, questions })
    if (ids) closeStaleNotifications(ids)
  }, [loaded, pending, questions])
```

- [ ] **Step 5: 跑，确认通过**

Run: `cd web && npx vitest run src/notifications.test.ts src/store.test.ts src/sw.test.ts && npx tsc --noEmit`
Expected: 全部 PASS，tsc 无输出。

- [ ] **Step 6: 更新文档**

`docs/web.md`“通知”一节，在“点开直达”一条之后加：

```markdown
- **已处理的自动收掉**：网页每次拿到收件箱状态（打开时的加载、实时连接推来的变化、你批准或拒绝之后）都会关掉通知中心里已经不在待审批、等你回答里的通知（含测试通知），同一秒内至多收一次；点通知本身也会先收掉被点的那条再打开详情。iOS 不允许静默推送（不弹通知的推送会让订阅被吊销），hub 没法在请求处理完时远程收掉通知：在终端或别的设备上处理掉的请求，它的通知要等你下次打开网页才消失。
```

- [ ] **Step 7: 全量测试并提交**

Run: `make test`
Expected: 全过。`git diff --stat -- go.mod go.sum web/package.json web/package-lock.json` 为空。

```bash
git add web/src/notifications.ts web/src/notifications.test.ts web/src/store.ts web/src/store.test.ts web/src/StoreProvider.tsx web/src/sw.test.ts docs/web.md
git commit -m "web: 拿到收件箱状态时收掉已处理请求的通知（同一秒至多一次）；sw 测试改用新文案并护住点击先关再开"
```

---

## 收尾验证（两个 Task 都过审之后）

- `make test`
- `go test ./... -race -count=1`
- `make e2e`
- `git diff main --stat -- go.mod go.sum` 为空；`git status` 干净。

---

## 第三项：审批页说人话（2026-10-31 追加）

**Goal:** 作者第二条真机反馈：审批页只把原始字段（目录、机器、项目、工具、风险、“引擎：无规则命中，按 outward 默认询问”、状态、创建、过期）摆出来，再就是“拒绝 / 批准”，没有一句人话说谁要干什么、为什么要问你、批了或拒了会怎样，几次点错。改成：页面最上面一张说明卡（谁想做什么、在哪、为什么要你决定），命令原样放在卡片下面，再下面三行“批了 / 拒了 / 记住会怎样”；原来的字段表收进默认折叠的“详情”；收件箱卡片第一行也改成“做什么”。

**Architecture:** 第三项分两个 Task。Task 3：hub 的请求视图加 `cli`（会话的 `sessions.tool` 原值），推送的 `message` 改用它（删掉重复的 `sessionTool`）；网页加纯函数模块 `web/src/explain.ts`（`explainRequest(r)` → `{ headline, what, where, why, next }`），只有单元测试，不接页面。Task 4：审批页、收件箱卡片、详情折叠、处理后的状态行接上 `explain.ts`，改 CSS、文档、现有页面测试与 e2e。

**Tech Stack:** 同上。

### 第三项的设计取舍（执行前已定）

- **`cli` 是原值，显示名在网页算。** `requestView.CLI`（`json:"cli"`）= 请求所属会话行的 `tool`（`claude`、`codex`、`gemini`）；没有会话（webhook、测试直插的行）、会话行不在时为空串；读库出错记日志、也为空。`views` 里按会话 id 缓存，一批至多查一次每个会话（与机器名同样做法）。推送的 `(*pushService).message` 改用 `v.CLI`，删掉 `(*pushService).sessionTool`（同一件事不查两遍、两处日志不分叉）。网页 `cliLabel` 的映射与 hub 的 `cliNames` 一致：空与不认识的都叫“Agent”。
- **`explainRequest` 多返回一个 `what`（不带 CLI 名的“做什么”）。** 收件箱卡片第一行用 `what`，不用整句 headline：手机上卡片一行放不下“Claude Code 想把代码推到远程仓库（origin 的 main）”再加风险标签和时间。CLI 名挪到卡片第三行的“机器 · 项目”前面（`Claude Code · mac · blog`；`cli` 为空时不写“Agent”，免得满屏 Agent）。question 的 `what` 是“在终端等你回答”，webhook 的是“工作流等你批准”，done 的是“做完了这一轮”。
- **shell 命令按段找最要紧的那一段。** Claude Code 常写 `cd /p && git push`、`git add -A && git commit -m x && git push`，只看第一个词会全落到“运行命令”。做法：按不在引号里的 `&&`、`||`、`;`、`|`、`&`、换行切段，每段去掉前导环境变量赋值（`NAME=value`）、命令名取最后一个 `/` 之后（`/opt/homebrew/bin/brew` 也认），逐段匹配；匹配到的按严重程度排（不可逆 3：强制推送、`git reset --hard`、`rm`、Docker 清理、`sudo`；对外 2：推送、发布部署、`ssh`；联网 1：`curl`/`wget`、装依赖），取最高的，一样高取靠前的。都没匹配上时说“运行命令：<整条命令把空白并成一个空格后的前 80 个字符>”。分词只为写一句人话，不求完全符合 shell 语法：单引号里原样，双引号里与引号外的反斜杠转义下一个字符，`$(…)` 与反引号不展开。重定向（`>`、`>>`、`<`、`2>&1`、`&>` 等）连同它的目标不算词，行尾反斜杠续行接成一个整体。
- **比需求多认的几个写法。** `rm` 不带 `-r` 也算删除（同样不可恢复）；`git push` 的 refspec 以 `+` 开头也算强制推送，`-uf` 这类合在一起的短选项含 `f` 也算；`git -C <目录>`、`git -c <配置>` 之后的子命令照样认；`uv` 认 `uv add` 与 `uv pip install`（`uv pip list` 不是装依赖）；`docker volume prune` 与 `docker volume rm` 同样算清理；`ssh` 跳过选项（`-p`、`-i`、`-l`、`-o` 等带参数的连参数一起跳）再取主机；只有 remote 没有分支时写“（origin）”。“发布或部署：<命令>”、“以管理员权限运行：<剩余命令>”里的命令是去掉引号后的词用空格连起来，截到 80 个字符（原文在下面的命令框里）。装依赖至多列 3 个包名，多了写“等 N 个”。
- **`why` 怎么认暂停。** hub 侧的 `engine_reason` 来自守护进程的引擎（`internal/policy/engine.go` `Evaluate`），只有三种：`自动审批已暂停`、`命中规则 <id>`、`无规则命中，按 <risk> 默认询问`。暂停按 `engine_reason` 以“自动审批已暂停”开头认，不看 `state`/`decided_by`（待审批的 `decided_by` 为空；`decided_by: paused` 是本机暂停交回终端，不进收件箱）。暂停时先说“自动审批已暂停，所有请求都会问你。”，风险是 read、write_in_project 时只说这一句（后面那句“本来可以自动放行”在暂停时不成立），其余风险再接风险那句。read、write_in_project 不在暂停时：`engine_reason` 以“命中规则”开头说“本来可以自动放行，但有规则要求问你。”（需求原文）；否则（没有规则命中，多半是内置放行规则被停用了）说“这类操作本来可以自动放行，但现在没有规则放行它，要你决定。”。unknown 对 shell 说“这条命令不在已知名单里……”，对别的载荷说“这个操作不在已知名单里，规则判断不了，要你决定。”。空的、不认识的风险说“规则定不了，要你决定。”。question、webhook、done 的 `why` 为空（说明卡不出这一行）。
- **`next` 只在还能决定时显示。** 待审批的 permission、webhook 与 question 显示；已处理的不显示（处理后改看状态行）。含被隐藏部分的请求不能“记住”，`next` 不出第三行。
- **含被隐藏部分时告警仍在最上面。** 醒目告警放在说明卡之前：说明卡那一句也是从脱敏后的内容推出来的。`RequestContent` 不再自带告警，告警与说明卡、命令框、`next` 由新组件 `RequestSummary` 按顺序排。
- **question 的蓝色横幅去掉。** “agent 在终端里等你回答这个问题，请去终端回答”与说明卡的 headline、`next`（“去那台机器的终端回答，这里只是提醒。”）重复。
- **“详情”里放字段表与审计。** 默认折叠的 `<details class="more">`，summary “详情”，里面是 `RequestAttrs`（多一行“目录”：shell 载荷的 `cwd`，从命令框下面挪进来）与审计列表（审计对“该不该批”没用，展开详情时才看）。“引擎”那行与告警里的理由都过 `engineReasonLabel`：只把“按 <risk> 默认询问”里认识的风险词换成 `riskLabel` 的中文（“按「对外」默认询问”），规则 id 里碰巧含 network 之类的词不动。
- **处理后的状态行。** 已处理的 permission、webhook 在命令框下面显示一行：状态标签（`historyTag` 的文案与色调）、依据（`decidedByLabel`，与状态文案相同时不重复）、决定时间（`absoluteTime`）、送达（`deliveryLabel`），用“ · ”连起来。question、done 没有状态行。
- **卡片里的机器名去 `.local`**（与说明卡、推送一致）。历史卡片不在本项范围，不动。
- **说明卡用的 CSS 变量只用已有的**（`--fg`、`--muted`、`--card`、`--line`），`contrast.test.ts` 不用改；页面仍不许内联样式。

#### 终审修复（2026-10-31）

整条分支终审后补的修改，逐条一句：

- **I-1 删远程分支单独说。** `git push` 的 `--delete`、含 `d` 的短选项簇、源为空的 refspec（`:main`、`+:main`）说成“删除远程分支”，`--mirror`、`--prune` 说成“让远程和本地完全一致，可能删掉远程的分支”，都按不可逆；优先级删除 > mirror/prune > 强制 > 普通；`-o`、`--push-option`、`--repo`、`--receive-pack`、`--exec` 连值一起跳过，不当成 remote。
- **I-2 说明句以 hub 的风险为下限。** `explainRequest` 对 shell 载荷取 `{ destructive: 3, outward: 2, network: 1 }` 为下限，认出的那段比它轻（含一段都没认出）时退回“运行命令：<原命令>”；`shellAction` 不看风险，签名不变。另照 `internal/policy/commands.go` 认 curl（短选项含 `X d T F`，与那些长选项）、wget（`--post-data`、`--post-file`、`--method`，含 `=` 连写）往外发数据，说“向 <主机名> 发送数据”。hub 把 `curl … | sh` 定为 network（只看到 curl），网页与它一致，仍说“访问网址”，更高的风险档才退回原命令。
- **I-3 已处理的请求不再说“要你决定”。** `why` 只在待决的审批上有，`next` 只在待决的审批与 webhook、question 上有；`RequestSummary` 删掉自己的判断，非空就显示。
- **M-1 历史卡片边距。** 第三行的样式改成 `.card > .where`，不再落到历史卡片 `.row` 里的 `span.where`。
- **C-1 卡片第一行至多两行。** `.card .what` 由单行省略改为两行截断，窄屏不再把“（origin 的 main）”截掉。
- **M-2 heredoc。** 分词时跳过 `<<WORD`、`<<-WORD` 的正文与结束行（`<<<` 是 here-string，不算），同一行多个按顺序排队，没有结束行就吃到末尾；正文不产生词也不产生段。
- **M-3 “记住”那行按能不能记成放行说。** 对外、不可逆、未知、项目外，或 shell 与文件写入没有项目根时，说“只能存成拒绝规则”；其余仍说可以存成规则。
- **M-6 认不出的载荷。** 网页 `payloadSummary` 的默认分支与 hub 一致，取工具名，没有再取类型。
- **M-7 目录。** shell 的 `cwd` 不是项目根时，说明卡的 where 末尾追加“目录 <cwd>”；README 的说法改成与现状一致。
- **M-9 包装命令。** 每段去掉前导赋值时，同样去掉 `env`、`time`、`nohup`、`nice`、`caffeinate`（照 `stripWrappers`，只剩一个词时不剥，不跳它们的选项）。
- **I-A 历史卡片样式回归（第二轮）。** 上一轮把 `.card .where` 改成 `.card > .where`，历史卡片第一行 `.row` 里的 `span.where` 一条规则都不命中；现已恢复原规则（灰色、13px、单行省略），只把上边距限定在 `.card > .where`。
- **I-B 管道进 shell（第二轮）。** 管道里（紧跟单个 `|`）从标准输入读脚本的 shell（`sh`、`bash`、`zsh`、`dash`、`ksh`、`fish`，无 `-c`、`--` 之前无脚本文件，可带 `sudo`）按不可逆说：前面是 curl/wget 说“下载 H 的脚本并直接执行”（有 `sudo` 时“以管理员权限直接执行”），否则“执行管道传来的脚本”。风险下限只是下限，页面可以说得比 hub 重。hub 的 policy 把 `curl … | sh` 定为 network，是另一件事，记为后续。
- **第二轮的三条小修。** 只有冒号的 refspec（`:`、`+:`）不算删除，按普通或强制推送；`.explain .where` 长路径折行；比较 cwd 与项目根时去掉结尾的 `/`。
- **第三轮复核的两处（控制者直接修）。** `-s`（含在簇里）之后的词是脚本参数，仍是从标准输入读（`curl … | bash -s stable`、`| sh -s - server` 这类安装脚本）；没有 `-s` 时，`--`/单独的 `-` 之后还有词就是脚本文件；`--command` 与 `-c` 同样不算。往回找同一条管道里的 curl/wget 至多看 32 段：故意拼出的超长管道（`a | ` 五千次再接 `sh | ` 五千次）从约 600 毫秒降到约 10 毫秒，收件箱每次渲染卡片都要算。`|&` 不当管道（少见，接受）。
- **复合命令的“记住”那行（接受）。** 复合命令（`a && b`）仍说“下次同类的直接拒绝”，hub 对复合命令连拒绝也不记；少见，接受。
- **不改 M-4。** outward、destructive 的 why 说“规则永远不会自动放行”是作者在需求里给的原文；导入的 elevated 手写规则其实能放行，交作者决定。
- **不改 M-5。** 推送写“<CLI> 要…”、页面写“<CLI> 想…”，标签“对外”、推送“往外发”，各自是作者给的文案。

### Task 3: 审批页说人话（一）：hub 给 CLI 名、网页 `explain.ts`

**Files:**
- Modify: `internal/hub/api.go`（`requestView` 加 `CLI`；`views` 按会话缓存查 `SessionTool`）
- Modify: `internal/hub/pushsvc.go`（`message` 用 `v.CLI`；删 `(*pushService).sessionTool`）
- Test: `internal/hub/api_test.go`（新增 `TestRequestViewCLI`）、`internal/hub/requests_test.go`（`wantView` 也要求有 `cli` 键）
- Modify: `web/src/types.ts`（`RequestView.cli`）
- Create: `web/src/explain.ts`、`web/src/explain.test.ts`
- Modify: 所有手写 `RequestView` 的测试夹具补 `cli`（`tsc` 会逐个指出：`store.test.ts`、`decide.test.ts`、`history.test.ts`、`format.test.ts`、`components/components.test.tsx`、`pages/pages.test.tsx`）
- Modify: `docs/hub.md`（请求视图的字段说明加 `cli`）

**Interfaces:**
- Consumes: `store.SessionTool`（Task 1）、`(*Server).views`、`clip`（hub 的 id 截短）；网页 `clip`、`isRedacted`（`format.ts`）。
- Produces:
  - Go：`requestView{ store.Request; CLI string \`json:"cli"\`; MachineName; Project; HasRedacted }`，`cli` 键总在（不 omitempty）。
  - TS（`web/src/explain.ts`）：
    ```ts
    export interface Explanation { headline: string; what: string; where: string; why: string; next: string[] }
    export function cliLabel(cli: string): string
    export function machineLabel(name: string): string
    export function shellAction(command: string): string
    export function explainRequest(r: RequestView): Explanation
    ```

- [ ] **Step 1: 写 hub 的失败测试**

`internal/hub/api_test.go`，放在 `TestRequestViewHasRedacted` 后面：

```go
// v0.1.4 第三项：请求视图带 cli（请求所属会话行的 sessions.tool 原值；没有会话、会话行不在时为空串，键总在），
// 网页据此写“Claude Code 想…”。/api/inbox、/api/requests、/api/requests/{id}、decide 的返回与 /ws/client 的帧都带。
func TestRequestViewCLI(t *testing.T) {
	h := newTestHub(t)
	ck := loggedIn(t, h)
	id, secret := enrollMachine(t, h, "mac")
	sub, stop := h.srv.clients.subscribe()
	defer stop()
	c := dialAgent(t, h, secret, wire.Hello{Proto: wire.Proto, MachineID: id})
	defer c.close()
	c.recv(5 * time.Second)
	c.send(pendingFrame("c1", "s1", "t1", "git push origin main")) // pendingFrame 的会话 tool 是 claude
	if f := waitBroadcast(t, sub, "request_new", "c1", nil); f["cli"] != "claude" {
		t.Fatalf("c1 的推送应带 cli claude: %+v", f)
	}
	sid, err := h.st.UpsertSession(id, proto.Session{Tool: "codex", CLISessionID: "s9", Cwd: "/p"}, "p")
	if err != nil {
		t.Fatal(err)
	}
	insertReq(t, h.st, store.Request{ID: "c2", MachineID: id, SessionID: sid, State: "pending"})
	insertReq(t, h.st, store.Request{ID: "c3", MachineID: id, State: "pending"})                       // 没有会话
	insertReq(t, h.st, store.Request{ID: "c4", MachineID: id, SessionID: "missing", State: "pending"}) // 会话行不在
	want := map[string]string{"c1": "claude", "c2": "codex", "c3": "", "c4": ""}
	var inbox struct {
		Pending []map[string]any `json:"pending"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/inbox", nil, "", ck), &inbox)
	var requests []map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/requests?limit=500", nil, "", ck), &requests)
	for path, list := range map[string][]map[string]any{"/api/inbox": inbox.Pending, "/api/requests": requests} {
		if len(list) != len(want) {
			t.Fatalf("%s: %d 条", path, len(list))
		}
		for _, r := range list {
			if v, ok := r["cli"].(string); !ok || v != want[r["id"].(string)] {
				t.Fatalf("%s 里 %s 的 cli 应为 %q: %+v", path, r["id"], want[r["id"].(string)], r)
			}
		}
	}
	var detail struct {
		Request map[string]any `json:"request"`
	}
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/requests/c2", nil, "", ck), &detail)
	if detail.Request["cli"] != "codex" {
		t.Fatalf("详情应带 cli codex: %+v", detail.Request)
	}
	var decided map[string]any
	decodeBody(t, doReq(t, "POST", h.http.URL+"/api/requests/c1/decide", decideBody(t, "deny", false, ""), "application/json", ck), &decided)
	if decided["cli"] != "claude" || decided["state"] != "denied" {
		t.Fatalf("decide 的返回应带 cli claude: %+v", decided)
	}
	if f := waitBroadcast(t, sub, "request_updated", "c1", nil); f["cli"] != "claude" {
		t.Fatalf("决定后的推送应带 cli claude: %+v", f)
	}
}
```

`internal/hub/requests_test.go` 的 `wantView` 同时要求 `cli` 键存在（`_, hasCLI := f["cli"]`，并进 `if` 条件与报错文案“（machine_name %q、project、cli、has_redacted）”）。

- [ ] **Step 2: 跑，确认失败**

Run: `go test ./internal/hub -run 'TestRequestViewCLI|TestBroadcast|TestRecord' -count=1`
Expected: `TestRequestViewCLI` 失败（`cli` 为 nil），用到 `wantView` 的用例失败。

- [ ] **Step 3: 实现 hub 侧**

- `requestView` 在 `store.Request` 之后加 `CLI string \`json:"cli"\``，注释写明“请求所属会话的 sessions.tool 原值（claude、codex、gemini）；没有会话、查不到为空，读库出错记日志也为空；显示名由网页按它算”；类型注释第一句改成“…加 CLI、机器名、项目名与 has_redacted”。
- `views`：与 `names` 并列加 `tools := map[string]string{}`；`r.SessionID != ""` 时按会话 id 缓存查 `s.store.SessionTool`，`ErrNotFound` 当空，其他错误 `s.log.Printf("读会话 %s 的 CLI: %v", clip(r.SessionID), err)` 后当空（同一批同一会话只查一次，出错也缓存空值）。函数注释补上“CLI（每个会话只查一次）”。
- `pushsvc.go`：`message` 改为 `pushMessage(r, pushWho{Tool: v.CLI, …})`，删掉 `(*pushService).sessionTool` 与它的注释；`message` 的注释改成“CLI、机器名、项目名取自请求视图，再数一下接入且未吊销的机器”。`pushWho.Tool` 的注释改成“请求视图的 cli”。
- `docs/hub.md` 请求视图那句（“还带 `machine_name`（webhook 请求为空）…”）加上“`cli`（请求所属会话的 CLI：`claude`、`codex`、`gemini`，没有会话或查不到为空串）”。

Run: `go test ./internal/hub/... -count=1` → PASS。

- [ ] **Step 4: 写网页的失败测试**

`web/src/types.ts` 的 `RequestView` 在 `session_id` 后加：

```ts
  /** 请求所属会话的 CLI（sessions.tool 原值：claude、codex、gemini）；没有会话或查不到为空串。 */
  cli: string
```

各测试文件的 `req()` 夹具补 `cli: 'claude'`（`npx tsc --noEmit` 指出哪几个）。

新建 `web/src/explain.test.ts`：

```ts
import { describe, expect, it } from 'vitest'
import { cliLabel, explainRequest, machineLabel, shellAction } from './explain'
import type { RequestView } from './types'

function req(over: Partial<RequestView> = {}): RequestView {
  return {
    id: 'r1', machine_id: 'm', session_id: 's', cli: 'claude', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'shell', command: 'git push origin main', cwd: '/p/blog' }, risk: 'outward', project_root: '/p/blog',
    engine_reason: '无规则命中，按 outward 默认询问', state: 'pending', decided_by: '', decision_note: '', rule_id: '', turn_id: 't',
    cli_session_id: 's', title: '', created_at: '2026-10-23T10:00:00.000000000Z', expires_at: null, decided_at: null,
    delivered_at: null, machine_name: 'mac.local', project: 'blog', has_redacted: false, ...over,
  }
}

const riskWords = /read|write_in_project|unknown|write_outside|network|outward|destructive/

describe('cliLabel', () => {
  it('与 hub 的 cliNames 一致；空与不认识的叫 Agent', () => {
    expect(cliLabel('claude')).toBe('Claude Code')
    expect(cliLabel('codex')).toBe('Codex')
    expect(cliLabel('gemini')).toBe('Gemini CLI')
    expect(cliLabel('')).toBe('Agent')
    expect(cliLabel('cursor')).toBe('Agent')
  })
})

describe('machineLabel', () => {
  it('去掉结尾的 .local（不分大小写）；整个名字就是 .local 时原样', () => {
    expect(machineLabel('mac.local')).toBe('mac')
    expect(machineLabel('Mini.LOCAL')).toBe('Mini')
    expect(machineLabel('.local')).toBe('.local')
    expect(machineLabel('box')).toBe('box')
    expect(machineLabel('')).toBe('')
  })
})

describe('shellAction', () => {
  const force = '强制推送，会覆盖远程历史'
  const cases: [string, string][] = [
    ['git push origin main', '把代码推到远程仓库（origin 的 main）'],
    ['git push', '把代码推到远程仓库'],
    ['git push origin', '把代码推到远程仓库（origin）'],
    ['git push -u origin feat/x', '把代码推到远程仓库（origin 的 feat/x）'],
    ['git push origin HEAD:release', '把代码推到远程仓库（origin 的 release）'],
    ['git -C /p/blog push origin main', '把代码推到远程仓库（origin 的 main）'],
    ['/usr/bin/git push origin main', '把代码推到远程仓库（origin 的 main）'],
    ['git push --force origin main', `${force}（origin 的 main）`],
    ['git push -f', force],
    ['git push --force-with-lease origin main', `${force}（origin 的 main）`],
    ['git push --force-with-lease=main:abc origin main', `${force}（origin 的 main）`],
    ['git push -uf origin main', `${force}（origin 的 main）`],
    ['git push origin +main', `${force}（origin 的 main）`],
    ['git push --follow-tags origin main', '把代码推到远程仓库（origin 的 main）'],
    ['git reset --hard HEAD~1', '丢弃工作区的改动，不可恢复'],
    ['git reset --soft HEAD~1', '运行命令：git reset --soft HEAD~1'],
    ['rm -rf build dist', '删除 build、dist，删了不可恢复'],
    ['rm -r /tmp/x', '删除 /tmp/x，删了不可恢复'],
    ['rm notes.txt', '删除 notes.txt，删了不可恢复'],
    ['rm -rf -- -weird', '删除 -weird，删了不可恢复'],
    ['rm -rf', '删除文件，删了不可恢复'],
    ['curl -sSL https://api.example.com/v1/x', '访问网址 api.example.com'],
    ['wget "https://dl.example.org/a.tgz"', '访问网址 dl.example.org'],
    ['curl -X POST localhost:8080/x', '用 curl 访问网络'],
    ['curl http://[bad', '用 curl 访问网络'],
    ['npm install', '安装依赖，要联网'],
    ['npm i react react-dom', '安装依赖 react、react-dom，要联网'],
    ['pnpm add -D vitest', '安装依赖 vitest，要联网'],
    ['yarn add a b c d', '安装依赖 a、b、c 等 4 个，要联网'],
    ['bun install', '安装依赖，要联网'],
    ['pip install requests', '安装依赖 requests，要联网'],
    ['pip3 install -U httpx', '安装依赖 httpx，要联网'],
    ['uv add ruff', '安装依赖 ruff，要联网'],
    ['uv pip install ruff', '安装依赖 ruff，要联网'],
    ['uv pip list', '运行命令：uv pip list'],
    ['/opt/homebrew/bin/brew install jq', '安装依赖 jq，要联网'],
    ['go get golang.org/x/net', '安装依赖 golang.org/x/net，要联网'],
    ['go install golang.org/x/tools/gopls@latest', '安装依赖 golang.org/x/tools/gopls@latest，要联网'],
    ['cargo add serde', '安装依赖 serde，要联网'],
    ['cargo install ripgrep', '安装依赖 ripgrep，要联网'],
    ['gem install rails', '安装依赖 rails，要联网'],
    ['npm publish', '发布或部署到外部：npm publish'],
    ['gh release create v1.0.0 --notes "x y"', '发布或部署到外部：gh release create v1.0.0 --notes x y'],
    ['gh pr create --fill', '发布或部署到外部：gh pr create --fill'],
    ['gh pr list', '运行命令：gh pr list'],
    ['vercel --prod', '发布或部署到外部：vercel --prod'],
    ['fly deploy', '发布或部署到外部：fly deploy'],
    ['flyctl deploy -a railse-hub', '发布或部署到外部：flyctl deploy -a railse-hub'],
    ['kubectl apply -f k.yaml', '发布或部署到外部：kubectl apply -f k.yaml'],
    ['terraform apply -auto-approve', '发布或部署到外部：terraform apply -auto-approve'],
    ['ssh deploy@prod.example.com uptime', '连接远程机器 deploy@prod.example.com'],
    ['ssh -p 2222 -i ~/.ssh/k box', '连接远程机器 box'],
    ['ssh', '连接远程机器'],
    ['sudo launchctl kickstart -k system/x', '以管理员权限运行：launchctl kickstart -k system/x'],
    ['sudo', '以管理员权限运行命令'],
    ['docker system prune -af', '清理 Docker 数据，不可恢复'],
    ['docker volume rm data', '清理 Docker 数据，不可恢复'],
    ['docker volume prune', '清理 Docker 数据，不可恢复'],
    ['docker ps', '运行命令：docker ps'],
    ['ls -la', '运行命令：ls -la'],
  ]
  it.each(cases)('%s → %s', (command, want) => {
    expect(shellAction(command)).toBe(want)
  })

  it('去掉前导环境变量赋值（引号里的值带空格也认）', () => {
    expect(shellAction('FOO=1 BAR="a b" git push origin main')).toBe('把代码推到远程仓库（origin 的 main）')
    expect(shellAction('NODE_ENV=production npm publish')).toBe('发布或部署到外部：npm publish')
    expect(shellAction('FOO=1')).toBe('运行命令：FOO=1')
  })

  it('复合命令取最要紧的一段：不可逆 > 对外 > 联网，一样高取靠前的', () => {
    expect(shellAction('cd /p/blog && git push origin main')).toBe('把代码推到远程仓库（origin 的 main）')
    expect(shellAction('git add -A && git commit -m "x && y" && git push')).toBe('把代码推到远程仓库')
    expect(shellAction('npm install && npm publish')).toBe('发布或部署到外部：npm publish')
    expect(shellAction('git push; rm -rf dist')).toBe('删除 dist，删了不可恢复')
    expect(shellAction('curl -fsSL https://get.example.sh | sh')).toBe('访问网址 get.example.sh')
    expect(shellAction('curl https://a.example.com & wget https://b.example.com')).toBe('访问网址 a.example.com')
    expect(shellAction('make || make clean\nmake test')).toBe('运行命令：make || make clean make test')
  })

  it('引号里的分隔符不切段', () => {
    expect(shellAction('echo "a && git push"')).toBe('运行命令：echo "a && git push"')
    expect(shellAction("echo 'rm -rf /'; ls")).toBe("运行命令：echo 'rm -rf /'; ls")
  })

  it('空命令、超长命令', () => {
    expect(shellAction('')).toBe('运行一条命令')
    expect(shellAction('  \n ')).toBe('运行一条命令')
    const long = 'echo ' + 'x'.repeat(200)
    expect(shellAction(long)).toBe('运行命令：' + long.slice(0, 80) + '…')
  })
})

describe('explainRequest：headline 与 what', () => {
  it('shell：<CLI 显示名> 想<做什么>；CLI 为空或不认识叫 Agent', () => {
    const e = explainRequest(req())
    expect(e.what).toBe('把代码推到远程仓库（origin 的 main）')
    expect(e.headline).toBe('Claude Code 想把代码推到远程仓库（origin 的 main）')
    expect(explainRequest(req({ cli: '' })).headline).toBe('Agent 想把代码推到远程仓库（origin 的 main）')
    expect(explainRequest(req({ cli: 'cursor' })).headline).toBe('Agent 想把代码推到远程仓库（origin 的 main）')
    expect(explainRequest(req({ cli: 'codex', payload: { type: 'shell' } })).headline).toBe('Codex 想运行一条命令')
  })

  it('文件改动按风险', () => {
    const file = (risk: string) => explainRequest(req({ risk, payload: { type: 'file_write', path: '/p/blog/a.ts' } })).what
    expect(file('write_in_project')).toBe('修改文件 /p/blog/a.ts')
    expect(file('write_outside')).toBe('修改项目外的文件 /p/blog/a.ts')
    expect(file('destructive')).toBe('改敏感文件 /p/blog/a.ts（密钥、配置或开机自启）')
    expect(file('unknown')).toBe('修改文件 /p/blog/a.ts')
  })

  it('联网：主机名；网址解析不了原样；没有网址写工具名', () => {
    const net = (url: string | undefined, tool_name = 'WebFetch') =>
      explainRequest(req({ risk: 'network', tool_name, payload: { type: 'network', url, method: 'GET' } })).what
    expect(net('https://docs.example.com:8443/a?b=1')).toBe('访问 docs.example.com')
    expect(net('not a url')).toBe('访问 not a url')
    expect(net(undefined, 'WebSearch')).toBe('用 WebSearch 联网')
    expect(net(undefined, '')).toBe('联网')
  })

  it('MCP：调用 <服务> 的 <工具>；缺哪个补哪个', () => {
    const mcp = (server?: string, tool?: string) => explainRequest(req({ risk: 'unknown', payload: { type: 'mcp', server, tool } })).what
    expect(mcp('github', 'create_issue')).toBe('调用 github 的 create_issue')
    expect(mcp('github')).toBe('调用 github 的工具')
    expect(mcp(undefined, 'create_issue')).toBe('调用 MCP 工具 create_issue')
    expect(mcp()).toBe('调用 MCP 工具')
  })

  it('认不出的载荷写工具名', () => {
    expect(explainRequest(req({ risk: 'unknown', tool_name: 'read_file', payload: { type: 'other' } })).what).toBe('使用工具 read_file')
    expect(explainRequest(req({ risk: 'unknown', tool_name: '', payload: { type: 'other' } })).what).toBe('做一件事')
  })

  it('question、webhook、done', () => {
    const q = explainRequest(req({ kind: 'question', state: 'recorded', title: '要删旧分支吗？', payload: { type: '' } }))
    expect(q.headline).toBe('Claude Code 在终端等你回答')
    expect(q.what).toBe('在终端等你回答')
    const w = explainRequest(req({ kind: 'webhook', cli: '', machine_name: '', project: '', risk: 'unknown', title: '部署 v1.2 到生产', payload: { type: 'other' } }))
    expect(w.headline).toBe('工作流等你批准：部署 v1.2 到生产')
    expect(w.what).toBe('工作流等你批准')
    expect(explainRequest(req({ kind: 'webhook', title: '' })).headline).toBe('工作流等你批准')
    expect(explainRequest(req({ kind: 'webhook', title: 'x'.repeat(100) })).headline).toBe('工作流等你批准：' + 'x'.repeat(80) + '…')
    const d = explainRequest(req({ kind: 'done', state: 'recorded', cli: 'gemini' }))
    expect(d.headline).toBe('Gemini CLI 做完了这一轮')
    expect(d.next).toEqual([])
  })
})

describe('explainRequest：where', () => {
  it('项目 <项目名>，机器 <机器名去 .local>；缺哪个省哪个', () => {
    expect(explainRequest(req()).where).toBe('项目 blog，机器 mac')
    expect(explainRequest(req({ machine_name: '' })).where).toBe('项目 blog')
    expect(explainRequest(req({ project: '' })).where).toBe('机器 mac')
    expect(explainRequest(req({ project: '', machine_name: '' })).where).toBe('')
  })
})

describe('explainRequest：why', () => {
  const why = (risk: string, over: Partial<RequestView> = {}) =>
    explainRequest(req({ risk, engine_reason: `无规则命中，按 ${risk} 默认询问`, ...over })).why

  it('按风险档说为什么要你决定', () => {
    expect(why('network')).toBe('这条要联网。规则默认不自动放行联网操作，要你决定。')
    expect(why('outward')).toBe('这会影响仓库之外的人和系统（推送、发布、部署）。规则永远不会自动放行这类操作，要你决定。')
    expect(why('destructive')).toBe('这是不可逆的操作。规则永远不会自动放行，要你决定。')
    expect(why('write_outside')).toBe('要改项目目录之外的文件。规则默认不自动放行，要你决定。')
    expect(why('unknown')).toBe('这条命令不在已知名单里，规则判断不了，要你决定。')
    expect(why('unknown', { payload: { type: 'mcp', server: 'db', tool: 'q' } })).toBe('这个操作不在已知名单里，规则判断不了，要你决定。')
    expect(why('read', { engine_reason: '命中规则 user-ask-read' })).toBe('本来可以自动放行，但有规则要求问你。')
    expect(why('write_in_project', { engine_reason: '命中规则 user-ask' })).toBe('本来可以自动放行，但有规则要求问你。')
    expect(why('write_in_project')).toBe('这类操作本来可以自动放行，但现在没有规则放行它，要你决定。')
    expect(why('')).toBe('规则定不了，要你决定。')
    expect(why('weird')).toBe('规则定不了，要你决定。')
  })

  it('自动审批暂停时先说暂停；read、write_in_project 只说这一句', () => {
    expect(why('network', { engine_reason: '自动审批已暂停' })).toBe('自动审批已暂停，所有请求都会问你。这条要联网。规则默认不自动放行联网操作，要你决定。')
    expect(why('read', { engine_reason: '自动审批已暂停' })).toBe('自动审批已暂停，所有请求都会问你。')
    expect(why('write_in_project', { engine_reason: '自动审批已暂停' })).toBe('自动审批已暂停，所有请求都会问你。')
  })

  it('不出现英文风险词', () => {
    for (const risk of ['read', 'write_in_project', 'unknown', 'write_outside', 'network', 'outward', 'destructive']) {
      expect(why(risk)).not.toMatch(riskWords)
      expect(why(risk, { engine_reason: '自动审批已暂停' })).not.toMatch(riskWords)
    }
  })

  it('question、webhook、done 没有 why', () => {
    expect(explainRequest(req({ kind: 'question', state: 'recorded', title: 'q' })).why).toBe('')
    expect(explainRequest(req({ kind: 'webhook', title: 'w' })).why).toBe('')
    expect(explainRequest(req({ kind: 'done', state: 'recorded' })).why).toBe('')
  })
})

describe('explainRequest：next', () => {
  it('审批：批准、拒绝、记住三行；含隐藏部分时没有记住那行', () => {
    expect(explainRequest(req()).next).toEqual([
      '批准：agent 执行这一条，只这一次。',
      '拒绝：agent 收到拒绝，会换个做法或停下来。',
      '勾「记住」可以存成规则，下次同类的不再问。',
    ])
    expect(explainRequest(req({ has_redacted: true })).next).toEqual([
      '批准：agent 执行这一条，只这一次。',
      '拒绝：agent 收到拒绝，会换个做法或停下来。',
    ])
  })

  it('question、webhook 各一行', () => {
    expect(explainRequest(req({ kind: 'question', state: 'recorded', title: 'q' })).next).toEqual(['去那台机器的终端回答，这里只是提醒。'])
    expect(explainRequest(req({ kind: 'webhook', title: 'w' })).next).toEqual(['批准或拒绝会回调给发起它的工作流。'])
  })
})
```

- [ ] **Step 5: 跑，确认失败**

Run: `cd web && npx vitest run src/explain.test.ts`
Expected: 失败（`./explain` 不存在）。

- [ ] **Step 6: 实现 `web/src/explain.ts`**

要点（文件头注释说明：审批页说明卡与收件箱卡片的人话，只为让人一眼看懂，不参与定级；定级以 hub 的 risk 为准）：
- `cliLabel`：`{ claude: 'Claude Code', codex: 'Codex', gemini: 'Gemini CLI' }`，注释写“与 hub 的 cliNames（internal/hub/pushsvc.go）一致”。
- `machineLabel`：同 hub 的 `machineLabel`。
- 分词 `segments(command): string[][]`：按设计取舍里的规则逐字符扫；运算符 `&&`、`||`、`;`、`|`、`&`、`\n` 在引号外时结束当前词与当前段；空段丢掉。每段再去掉前导的 `/^[A-Za-z_][A-Za-z0-9_]*=/` 词。
- 每段 `matchSegment(words): { rank: 1 | 2 | 3; text: string } | null`，命令名 `words[0]` 取最后一个 `/` 之后；规则见设计取舍与测试表。`git` 先跳全局选项（`-C`、`-c` 连同下一个词，其余 `-` 开头的一个词）再看子命令。`git push` 的位置参数是不以 `-` 开头的词：第一个是 remote，第二个是 refspec（`+` 开头算强制并去掉 `+`；含 `:` 取最后一个 `:` 之后，空了就当没有）。强制：`--force`、`--force-with-lease`（含 `=…`）、单横线短选项簇含 `f`（`/^-[A-Za-z]*f[A-Za-z]*$/`）。`rm` 的路径是不以 `-` 开头的词，`--` 之后的都算路径，路径用“、”连起来过 `clip(…, 80)`。`curl`/`wget` 取第一个匹配 `/^[a-z][a-z0-9+.-]*:\/\//i` 的词，`new URL` 抛错或 hostname 为空时按没有网址。`ssh` 带参数的短选项：`B b c D E e F I i J L l m O o P p Q R S W w`（只有两个字符时连下一个词一起跳）。
- `shellAction`：所有段里取 rank 最高（同 rank 取靠前）的 `text`；都没有时：`command.trim()` 为空回“运行一条命令”，否则“运行命令：” + `clip(command.trim().replace(/\s+/g, ' '), 80)`。
- `explainRequest`：按 `kind` 分：permission 的 `what` 按载荷类型（shell → `shellAction`；file_write 按风险；network → `new URL(url).hostname`，抛错原样、没有网址按工具名；mcp；其余 `使用工具 <tool_name>` 或“做一件事”），`headline = \`${cliLabel(r.cli)} 想${what}\``；question、done 的 `headline = \`${cliLabel(r.cli)} ${what}\``；webhook 的 `headline` 是“工作流等你批准”加 `：<clip(title, 80)>`（标题空时不加）。`where`、`why`、`next` 见设计取舍与测试；含隐藏部分用 `format.ts` 的 `isRedacted`。

Run: `cd web && npx vitest run src/explain.test.ts && npx tsc --noEmit` → 全过。

- [ ] **Step 7: 全量测试并提交**

Run: `make test`
Expected: 全过。`git diff --stat -- go.mod go.sum web/package.json web/package-lock.json` 为空。

```bash
git add internal/hub/api.go internal/hub/api_test.go internal/hub/requests_test.go internal/hub/pushsvc.go docs/hub.md web/src/types.ts web/src/explain.ts web/src/explain.test.ts web/src
git commit -m "hub: 请求视图带 cli（会话的 sessions.tool），推送改用它；web: explain.ts 把请求说成人话（谁想做什么、在哪、为什么问你、批了会怎样）"
```

### Task 4: 审批页说人话（二）：审批页、收件箱卡片改版

**Files:**
- Create: `web/src/components/Explain.tsx`（`RequestSummary`、`StatusLine`）
- Modify: `web/src/components/Payload.tsx`（`RequestContent` 不再自带告警；shell 的“目录”挪走；告警里的理由过 `engineReasonLabel`）
- Modify: `web/src/components/RequestAttrs.tsx`（加“目录”行；“引擎”过 `engineReasonLabel`；新增 `RequestDetails`）
- Modify: `web/src/components/RequestCard.tsx`（三行：做什么 + 标签 + 时间 / 命令或路径 / CLI · 机器 · 项目）
- Modify: `web/src/format.ts`（`engineReasonLabel`）、`web/src/format.test.ts`
- Modify: `web/src/pages/Request.tsx`（组装；去掉 question 横幅）
- Modify: `web/src/app.css`（`.explain`、`ul.next`、`.status-line`、`details.more`、`.card .what`）
- Test: `web/src/components/components.test.tsx`、`web/e2e/approve.spec.ts`
- Modify: `docs/web.md`（“页面”的收件箱、请求详情两条）、`docs/superpowers/specs/2026-10-21-railse-opc-design.md`（§0.6 的 v0.1.4 行补第三项）

**Interfaces:**
- Consumes: Task 3 的 `explainRequest`、`cliLabel`、`machineLabel`；`historyTag`（`history.ts`）、`decidedByLabel`、`deliveryLabel`、`absoluteTime`、`stateLabel`、`riskLabel`、`isRedacted`、`payloadSummary`、`clip`。
- Produces:
  - `format.ts`：`export function engineReasonLabel(reason: string): string`
  - `components/Explain.tsx`：`export function RequestSummary({ request }: { request: RequestView })`、`export function StatusLine({ request, audit }: { request: RequestView; audit: AuditEvent[] })`
  - `components/RequestAttrs.tsx`：`export function RequestDetails({ request, audit }: { request: RequestView; audit: AuditEvent[] })`
  - 页面结构（e2e 与测试靠这些类名）：`section.explain` 里 `p.headline`、`p.where`、`p.why`；`ul.next > li`；`p.status-line`；`details.more > summary`（文字“详情”）；卡片 `.card .what`、`.card .where`、`.card pre.cmd`。

- [ ] **Step 1: 写失败测试**

`web/src/format.test.ts` 加：

```ts
describe('engineReasonLabel', () => {
  it('只把“按 <风险> 默认询问”里认识的风险词换成中文', () => {
    expect(engineReasonLabel('无规则命中，按 outward 默认询问')).toBe('无规则命中，按「对外」默认询问')
    expect(engineReasonLabel('无规则命中，按 write_in_project 默认询问')).toBe('无规则命中，按「项目内改动」默认询问')
    expect(engineReasonLabel('无规则命中，按 weird 默认询问')).toBe('无规则命中，按 weird 默认询问')
    expect(engineReasonLabel('命中规则 user-network-ask')).toBe('命中规则 user-network-ask')
    expect(engineReasonLabel('自动审批已暂停')).toBe('自动审批已暂停')
    expect(engineReasonLabel('')).toBe('')
  })
})
```

`web/src/components/components.test.tsx`：

1. 改 `describe('RequestCard')` 的第一条与 webhook/问题那条，并加两条：

```tsx
  it('审批卡片：第一行做什么（不带 CLI 名）、风险标签；第二行命令；第三行 CLI · 机器 · 项目', () => {
    const html = card(req({ id: 'a/b' }))
    expect(html).toContain('href="/r/a%2Fb"')
    expect(html).toContain('<span class="what">把代码推到远程仓库（origin 的 main）</span>')
    expect(html).toContain('对外')
    expect(html).toMatch(/<pre class="cmd">git push origin main<\/pre>/)
    expect(html).toContain('<div class="where">Claude Code · mac · blog</div>')
    expect(html.indexOf('class="what"')).toBeLessThan(html.indexOf('class="cmd"'))
    expect(html.indexOf('class="cmd"')).toBeLessThan(html.indexOf('class="where"'))
    expect(html).not.toContain('含隐藏部分')
  })

  it('cli 为空时第三行不写 Agent；机器名去 .local', () => {
    expect(card(req({ cli: '', machine_name: 'mac.local' }))).toContain('<div class="where">mac · blog</div>')
  })

  it('文件改动的第二行是路径', () => {
    const html = card(req({ risk: 'write_outside', payload: { type: 'file_write', path: '/etc/hosts' } }))
    expect(html).toContain('<span class="what">修改项目外的文件 /etc/hosts</span>')
    expect(html).toContain('<pre class="cmd">/etc/hosts</pre>')
  })

  it('webhook 卡片：工作流等你批准、标题、第三行 webhook；问题卡片：在终端等你回答、问题、“等你回答”标签', () => {
    const w = card(webhook())
    expect(w).toContain('<span class="what">工作流等你批准</span>')
    expect(w).toContain('<pre class="cmd">部署 v1.2 到生产</pre>')
    expect(w).toContain('<div class="where">webhook</div>')
    const q = card(req({ kind: 'question', state: 'recorded', title: '要删旧分支吗？', payload: { type: '' } }))
    expect(q).toContain('<span class="what">在终端等你回答</span>')
    expect(q).toContain('<span class="tag plain">等你回答</span>')
    expect(q).toContain('<pre class="cmd">要删旧分支吗？</pre>')
  })
```

（`webhook()` 夹具补 `cli: ''`；“命令里的 HTML 原样转义，超长摘要截断”那条保留，长度上限按新增的 `.what`、`.where` 两行放宽到实际需要的量，并在注释里写明多了哪两行。）

2. 把 `describe('RequestContent')` 里“has_redacted：醒目告警在载荷之前…”那条挪到新的 `describe('RequestSummary')`，并改 shell 那条（目录挪进了详情）：

```tsx
describe('RequestContent', () => {
  it('shell 命令放在会换行的 pre 里，HTML 转义；目录不在这里（在详情里）', () => {
    const html = renderToStaticMarkup(<RequestContent request={req({ payload: { type: 'shell', command: 'echo "<b>x</b>" && rm -rf /tmp/x', cwd: '/p/elsewhere' } })} />)
    expect(html).toMatch(/<pre class="block mono">echo &quot;&lt;b&gt;x&lt;\/b&gt;&quot; &amp;&amp; rm -rf \/tmp\/x<\/pre>/)
    expect(html).not.toContain('/p/elsewhere')
    expect(html).not.toContain('<b>')
  })
  // “webhook：标题与描述都显示”“文件改动、联网、MCP 各有展示”“没有内联样式”三条保留；webhook 那条里关于告警的断言挪到 RequestSummary
})

describe('RequestSummary', () => {
  const summary = (r: RequestView) => renderToStaticMarkup(<RequestSummary request={r} />)

  it('说明卡在最上面：headline、where、why；命令框在卡片下面；next 三行在命令框下面', () => {
    const html = summary(req({ machine_name: 'mac.local', engine_reason: '无规则命中，按 outward 默认询问' }))
    expect(html).toContain('<section class="explain" aria-label="这条请求要做什么">')
    expect(html).toContain('<p class="headline">Claude Code 想把代码推到远程仓库（origin 的 main）</p>')
    expect(html).toContain('<p class="where">项目 blog，机器 mac</p>')
    expect(html).toContain('<p class="why">这会影响仓库之外的人和系统（推送、发布、部署）。规则永远不会自动放行这类操作，要你决定。</p>')
    const head = html.indexOf('class="headline"')
    const cmd = html.indexOf('<pre class="block mono">git push origin main</pre>')
    const next = html.indexOf('<ul class="next">')
    expect(head).toBeGreaterThan(-1)
    expect(head).toBeLessThan(cmd)
    expect(cmd).toBeLessThan(next)
    for (const s of ['批准：agent 执行这一条，只这一次。', '拒绝：agent 收到拒绝，会换个做法或停下来。', '勾「记住」可以存成规则，下次同类的不再问。']) {
      expect(html).toContain(`<li>${s}</li>`)
    }
    expect(html).not.toMatch(/outward|write_in_project|destructive/)
    expect(html).not.toContain('style=')
  })

  it('has_redacted：醒目告警在说明卡之前，说明不是原文、隐藏范围、突出风险与理由（中文）；没有“记住”那行', () => {
    const html = summary(req({ has_redacted: true, engine_reason: '无规则命中，按 outward 默认询问', payload: { type: 'shell', command: 'curl -H "Authorization: <redacted>" https://x' } }))
    const warn = html.indexOf('不是原文')
    expect(warn).toBeGreaterThan(-1)
    expect(warn).toBeLessThan(html.indexOf('class="headline"'))
    expect(html.indexOf('class="headline"')).toBeLessThan(html.indexOf('curl -H'))
    expect(html).toContain('role="alert"')
    expect(html).toContain('&lt;redacted&gt;')
    expect(html).toContain('引号')
    expect(html).toContain('heredoc')
    expect(html).toContain('对外')
    expect(html).toContain('无规则命中，按「对外」默认询问')
    expect(html).not.toContain('勾「记住」')
  })

  it('webhook：含隐藏部分时同样告警；说明卡、标题与描述、一行 next', () => {
    const html = summary(webhook({ has_redacted: true }))
    expect(html.indexOf('不是原文')).toBeLessThan(html.indexOf('class="headline"'))
    expect(html).toContain('<p class="headline">工作流等你批准：部署 v1.2 到生产</p>')
    expect(html).toContain('先灰度 10%，半小时后全量')
    expect(html).toContain('<li>批准或拒绝会回调给发起它的工作流。</li>')
    expect(html).not.toContain('class="where"')
    expect(html).not.toContain('class="why"')
  })

  it('question：去终端回答的提醒；已处理的审批没有 next', () => {
    const q = summary(req({ kind: 'question', state: 'recorded', title: '要删旧分支吗？', payload: { type: '' } }))
    expect(q).toContain('<p class="headline">Claude Code 在终端等你回答</p>')
    expect(q).toContain('要删旧分支吗？')
    expect(q).toContain('<li>去那台机器的终端回答，这里只是提醒。</li>')
    expect(summary(req({ state: 'approved', decided_by: 'user' }))).not.toContain('class="next"')
  })
})

describe('StatusLine', () => {
  const line = (r: RequestView, audit: AuditEvent[] = []) => renderToStaticMarkup(<StatusLine request={r} audit={audit} />)

  it('待审批、question 不显示', () => {
    expect(line(req())).toBe('')
    expect(line(req({ kind: 'question', state: 'recorded', title: 'q' }))).toBe('')
  })

  it('已处理：状态标签、依据、决定时间、送达，一行', () => {
    const html = line(req({ state: 'approved', decided_by: 'user', decided_at: '2026-10-23T10:01:00.000000000Z', delivered_at: '2026-10-23T10:01:01.000000000Z' }))
    expect(html).toMatch(/^<p class="status-line">/)
    expect(html).toContain('<span class="tag low">已批准</span>')
    expect(html).toContain('手机上决定')
    expect(html).toContain('已送达 agent')
    expect(html).toContain('<time dateTime="2026-10-23T10:01:00.000000000Z">')
    const denied = line(req({ state: 'denied', decided_by: 'user' }), [
      { id: 2, ts: '2026-10-23T10:02:00Z', machine_id: 'm', actor: 'system', request_id: 'r1', action: 'undeliverable', detail: '' },
    ])
    expect(denied).toContain('<span class="tag high">已拒绝</span>')
    expect(denied).toContain('决定未送达')
  })
})
```

（`StatusLine` 的标签用 `historyTag(r)`；`historyTag` 对 approved/denied 的色调以 `history.ts` 现有实现为准，断言跟它走。）

3. 改 `describe('RequestAttrs')` 并加 `RequestDetails`：

```tsx
  it('属性：目录、机器、项目、工具、风险、引擎理由（中文）、状态、备注', () => {
    const html = renderToStaticMarkup(
      <RequestAttrs request={req({ engine_reason: '无规则命中，按 outward 默认询问', decision_note: '不推主分支', state: 'denied', decided_by: 'user' })} audit={[]} />,
    )
    for (const s of ['<dt>目录</dt>', '/p/blog', 'mac', 'Bash', '对外', '无规则命中，按「对外」默认询问', '已拒绝', '不推主分支']) expect(html).toContain(s)
    expect(html).not.toContain('outward')
    expect(html).not.toContain('style=')
  })
```

```tsx
describe('RequestDetails', () => {
  it('默认折叠的“详情”，里面是字段表与审计', () => {
    const audit: AuditEvent[] = [{ id: 1, ts: '2026-10-23T10:00:00Z', machine_id: 'm', actor: 'system', request_id: 'r1', action: 'push_sent', detail: '' }]
    const html = renderToStaticMarkup(<RequestDetails request={req()} audit={audit} />)
    expect(html).toMatch(/^<details class="more"><summary>详情<\/summary>/)
    expect(html).not.toMatch(/<details[^>]* open/)
    expect(html).toContain('<dl class="kv">')
    expect(html).toContain('审计')
  })
})
```


`web/e2e/approve.spec.ts`（真 hub，Claude 钩子，`cli` 为 claude）：

- 第一张卡片：`card.locator('.where')` 改为 `toHaveText('Claude Code · e2e-mac · repo')`；加 `await expect(card.locator('.what')).toHaveText('访问网址 api.example.com')`。
- 点进去之后、点“批准”之前加：

```ts
  const explain = page.locator('section.explain')
  await expect(explain.locator('.headline')).toHaveText('Claude Code 想访问网址 api.example.com')
  await expect(explain.locator('.where')).toHaveText('项目 repo，机器 e2e-mac')
  await expect(explain.locator('.why')).toHaveText('这条要联网。规则默认不自动放行联网操作，要你决定。')
  await expect(page.locator('ul.next li')).toHaveCount(3)
  await expect(page.locator('details.more')).not.toHaveAttribute('open')
  await expect(page.locator('details.more summary')).toHaveText('详情')
```

- 第一条批准、结果提示之后加：`await expect(page.locator('.status-line .tag')).toHaveText('已批准')`、`await expect(page.locator('ul.next')).toHaveCount(0)`。
- 第二张卡片加 `await expect(card2.locator('.what')).toHaveText('把代码推到远程仓库（origin 的 main）')`；点进去后加 `await expect(page.locator('section.explain .headline')).toHaveText('Claude Code 想把代码推到远程仓库（origin 的 main）')`；拒绝之后加 `await expect(page.locator('.status-line .tag')).toHaveText('已拒绝')`。

- [ ] **Step 2: 跑，确认失败**

Run: `cd web && npx vitest run src/format.test.ts src/components/components.test.tsx`
Expected: 失败（`engineReasonLabel`、`RequestSummary`、`StatusLine`、`RequestDetails` 不存在，卡片结构不同）。

- [ ] **Step 3: 实现**

- `format.ts`：`engineReasonLabel` 用 `/按 (\S+) 默认询问/` 找风险词，`risks` 表里有的换成 `按「<中文>」默认询问`，其余原样。
- `Payload.tsx`：`RequestContent` 去掉开头的告警（注释改成“告警由 RequestSummary 放在说明卡之前”）；shell 只剩命令的 `pre`；`RedactedWarning` 里的理由过 `engineReasonLabel`。
- `components/Explain.tsx`（新）：`RequestSummary` 按顺序渲染：含隐藏部分时 `RedactedWarning`；`<section className="explain" aria-label="这条请求要做什么">`，里面 `p.headline`、有值时 `p.where`、`p.why`；`RequestContent`；能决定（`state === 'pending'` 且 permission 或 webhook）或是 question 时 `ul.next`。`StatusLine`：permission、webhook 且 `state !== 'pending'` 时一行 `<p className="status-line">`：`historyTag` 的标签，后接（与状态文案不同时的）依据、`<time>` 包着的 `absoluteTime(decided_at)`、`deliveryLabel`（审计里有 undeliverable 时传 true），用“ · ”连；否则 `null`。
- `RequestAttrs.tsx`：shell 载荷有 `cwd` 时第一行“目录”（等宽）；“引擎”过 `engineReasonLabel`；新增 `RequestDetails`：`<details className="more"><summary>详情</summary><RequestAttrs …/><AuditList …/></details>`。组件注释更新。
- `RequestCard.tsx`：第一行 `.row` 里 `<span className="what">{explain.what}</span>`、标签（不变）、含隐藏部分标签、时间；第二行 `pre.cmd`（permission 用 `payloadSummary`，其余用 `title`，`clip` 不变）；第三行 `<div className="where">`：webhook 写 webhook，其余 `[r.cli && cliLabel(r.cli), machineLabel(r.machine_name), r.project]` 去空后用“ · ”连。组件注释更新。
- `Request.tsx`：`r && data` 时依次 `RequestSummary`、`StatusLine`、`RequestDetails`、决定栏（不变）；删掉 question 的横幅与单独的 `RequestContent`/`RequestAttrs`/`AuditList` 引用。
- `app.css`（只用已有变量，无内联样式）：`.explain`（卡片底色、边框、圆角、内边距、下边距）；`.explain .headline`（17px、600、`overflow-wrap: anywhere`、无外边距）；`.explain .where`、`.explain .why`（14px，`.where` 用 `--muted`，上边距 4px）；`ul.next`（13px、`--muted`、左内边距 18px、上下边距 8px）；`.status-line`（14px、flex 换行、gap 6px）；`details.more`（上边距 12px）与 `details.more summary`（`--muted`、14px、可点、10px 内边距）；`.card .what`（15px、600、单行省略、`min-width: 0`）；`.card .where` 改成单独一行（上边距 4px，仍单行省略）。
- `docs/web.md`“页面”：收件箱那条的卡片描述改成“第一行是要做什么（如“把代码推到远程仓库（origin 的 main）”）与风险等级、相对时间，第二行命令、路径或问题，第三行 CLI · 机器 · 项目”；请求详情那条改成“最上面一张说明卡：谁想做什么（如“Claude Code 想访问网址 api.example.com”）、在哪个项目和机器、为什么要你决定（按风险说，自动审批暂停时先说暂停）；下面是原样的命令、路径或网址，再下面三行说批准、拒绝、记住会怎样（question 是“去那台机器的终端回答”，webhook 是“会回调给发起它的工作流”）；处理过的请求在命令下面一行显示状态、依据、决定时间与送达。机器、项目、工具、风险、引擎理由、时间与审计收在默认折叠的“详情”里。`has_redacted` 的请求……（原文保留）”。
- spec §0.6 的 v0.1.4 行末尾补一句：“第三项（2026-10-31，作者第二条真机反馈：审批页只有原始字段，几次点错）：请求视图加 `cli`；网页 `explain.ts` 把请求说成“<CLI> 想<做什么>”（shell 按段找最要紧的一段：不可逆 > 对外 > 联网）、在哪、为什么要你决定（按风险，暂停时先说暂停）、批了拒了会怎样；审批页最上面是说明卡，字段表与审计收进默认折叠的“详情”，处理后状态一行显示；收件箱卡片第一行是“做什么”，CLI 挪到第三行。”

- [ ] **Step 4: 跑，确认通过**

Run: `cd web && npx vitest run && npx tsc --noEmit`
Expected: 全过。

- [ ] **Step 5: 全量测试、e2e 并提交**

Run: `make test && make e2e`
Expected: 全过（e2e 2 passed）。`git diff --stat -- go.mod go.sum web/package.json web/package-lock.json` 为空。

```bash
git add web/src docs/web.md docs/superpowers/specs/2026-10-21-railse-opc-design.md web/e2e/approve.spec.ts
git commit -m "web: 审批页说人话：最上面说明卡（谁想做什么、在哪、为什么问你），命令在下，批了拒了会怎样；字段表与审计收进折叠的详情，处理后状态一行；收件箱卡片第一行写做什么"
```

### 第三项收尾验证（Task 3、4 都过审之后，连同前两项做整条分支终审）

- `make test`
- `go test ./... -race -count=1`
- `make e2e`
- `git diff main --stat -- go.mod go.sum web/package.json web/package-lock.json` 为空；`git status` 干净。
