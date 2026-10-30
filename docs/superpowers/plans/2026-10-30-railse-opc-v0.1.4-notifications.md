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
- **已知竞态（接受）。** 网页加载收件箱的那一刻正好来了新请求的推送、而这次快照里还没有它，它的通知会被当成已处理收掉。窗口只有一次 `/api/inbox` 往返那么长；hub 先经 `/ws/client` 广播再推送，实时连接在线时网页总是先知道新请求；而且只在网页开着时发生，收件箱随后会显示这条。不为它引入时间戳比对。
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
