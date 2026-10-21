package policy

import (
	"sync"
	"testing"
)

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
		{"用户 deny 压项目范围 allow", shell("npm run build"), projCtx, ActionDeny, "global-deny-npm-run"},
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

func TestEvaluateFloorWithoutRiskConstraint(t *testing.T) {
	// 规则只限定 payload、不限定 risk，Validate 放行，但运行时底线仍要拦住对外与不可逆操作。
	cases := []struct {
		name  string
		rules string
		cmd   string
		want  Action
		rule  string
	}{
		{"builtin 不得放行 destructive", "version: 1\nrules:\n  - id: b\n    source: builtin\n    scope: global\n    match: {payload: shell}\n    action: allow\n", "rm -rf x", ActionAsk, ""},
		{"builtin 不得放行 outward", "version: 1\nrules:\n  - id: b\n    source: builtin\n    scope: global\n    match: {payload: shell}\n    action: allow\n", "git push origin main", ActionAsk, ""},
		{"builtin 放行只读", "version: 1\nrules:\n  - id: b\n    source: builtin\n    scope: global\n    match: {payload: shell}\n    action: allow\n", "ls", ActionAllow, "b"},
		{"learned 放行只读", "version: 1\nrules:\n  - id: b\n    source: learned\n    scope: global\n    match: {payload: shell, command_prefix: cat, risk: read}\n    action: allow\n", "cat README.md", ActionAllow, "b"},
		{"learned 不得放行敏感文件读取", "version: 1\nrules:\n  - id: b\n    source: learned\n    scope: global\n    match: {payload: shell, command_prefix: cat, risk: read}\n    action: allow\n", "cat .env", ActionAsk, ""},
	}
	for _, c := range cases {
		e := mustEngine(t, c.rules)
		d := e.Evaluate(shell(c.cmd), projCtx)
		if d.Action != c.want || d.RuleID != c.rule {
			t.Errorf("%s: %q 得到 %s/%q (%s), 想要 %s/%q", c.name, c.cmd, d.Action, d.RuleID, d.Reason, c.want, c.rule)
		}
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
	// 暂停不会把用户的 deny 变成 ask
	for _, ctx := range []Context{otherCtx, projCtx} {
		if d := e.Evaluate(shell("npm run build"), ctx); d.Action != ActionDeny || d.RuleID != "global-deny-npm-run" {
			t.Errorf("%s: 暂停时用户 deny 仍应生效, 得到 %+v", ctx.ProjectRoot, d)
		}
	}
	e.SetPaused(false)
	if d := e.Evaluate(shell("git status"), projCtx); d.Action != ActionAllow {
		t.Errorf("恢复后应放行, 得到 %+v", d)
	}
}

func TestEvaluateConcurrentPause(t *testing.T) {
	// 配合 -race：Evaluate 与 SetPaused 在不同 goroutine 并发调用不得有数据竞争。
	e := mustEngine(t, engineRules)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				d := e.Evaluate(shell("git status"), projCtx)
				if d.Action != ActionAllow && d.Action != ActionAsk {
					t.Errorf("意外的决定 %+v", d)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 100; j++ {
			e.SetPaused(j%2 == 0)
			_ = e.Paused()
		}
	}()
	wg.Wait()
}

func TestEvaluateDestructiveNeedsExplicitRisk(t *testing.T) {
	const implicit = `
version: 1
rules:
  - id: push
    source: user
    scope: global
    match: {payload: shell, command_prefix: "git push origin"}
    action: allow
`
	const explicit = `
version: 1
rules:
  - id: push-force
    source: user
    scope: global
    match: {payload: shell, command_prefix: "git push origin", risk: destructive}
    action: allow
`
	cases := []struct {
		name, rules, cmd string
		want             Action
		rule             string
		risk             Risk
	}{
		{"user 规则放行 outward", implicit, "git push origin main", ActionAllow, "push", RiskOutward},
		{"未写 risk 不放行 destructive", implicit, "git push origin main --force", ActionAsk, "", RiskDestructive},
		{"自定义 receive-pack 执行任意命令", implicit, "git push origin main --receive-pack='sh -c x'", ActionAsk, "", RiskDestructive},
		{"显式 risk: destructive 放行强推", explicit, "git push origin main --force", ActionAllow, "push-force", RiskDestructive},
		{"risk: destructive 不覆盖 outward", explicit, "git push origin main", ActionAsk, "", RiskOutward},
	}
	for _, c := range cases {
		d := mustEngine(t, c.rules).Evaluate(shell(c.cmd), projCtx)
		if d.Action != c.want || d.RuleID != c.rule || d.Risk != c.risk {
			t.Errorf("%s: %q 得到 %s/%q/%s, 想要 %s/%q/%s", c.name, c.cmd, d.Action, d.RuleID, d.Risk, c.want, c.rule, c.risk)
		}
	}
}

func TestEvaluateAllowSegmentSafety(t *testing.T) {
	// allow 规则遇到注入型环境变量、指向非空设备的重定向或无法分析的段时不命中；正则按段匹配。
	const rules = `
version: 1
rules:
  - id: push
    source: user
    scope: global
    match: {payload: shell, command_prefix: "git push origin"}
    action: allow
  - id: status
    source: user
    scope: global
    match: {payload: shell, command_prefix: "git status"}
    action: allow
  - id: log-regex
    source: user
    scope: global
    match: {payload: shell, command_regex: "^git log"}
    action: allow
`
	e := mustEngine(t, rules)
	cases := []struct {
		cmd  string
		want Action
		rule string
	}{
		{"GIT_SSH_COMMAND=x git push origin main", ActionAsk, ""},
		{"git push origin main > ~/.bashrc", ActionAsk, ""},
		{"git push origin main", ActionAllow, "push"},
		{"git status 2>/dev/null", ActionAllow, "status"},
		{"git log --oneline", ActionAllow, "log-regex"},
		{"git log; git log -1", ActionAllow, "log-regex"},
		{"git log; rm -rf ~", ActionAsk, ""},
		{"git log $(rm -rf ~)", ActionAsk, ""},
		{"git log > ~/.bashrc", ActionAsk, ""},
		{"PAGER='sh -c x' git log", ActionAsk, ""},
		{"git push origin main ${HOME:+--force}", ActionAsk, ""},
		{`git push origin main "${HOME:+--force}"`, ActionAsk, ""},
		{"git push origin main ${X:---force}", ActionAsk, ""},
		{"git push origin main *", ActionAsk, ""},
		{"git push origin main {a,b}", ActionAsk, ""},
		{"git log $X", ActionAsk, ""},
		{"git log '$X' '*'", ActionAllow, "log-regex"},
		{"HOME=/home/u/proj/.x git push origin main", ActionAsk, ""},
		{"/tmp/evil/git push origin main", ActionAsk, ""},
	}
	for _, c := range cases {
		d := e.Evaluate(shell(c.cmd), projCtx)
		if d.Action != c.want || d.RuleID != c.rule {
			t.Errorf("%q 得到 %s/%q (%s), 想要 %s/%q", c.cmd, d.Action, d.RuleID, d.Reason, c.want, c.rule)
		}
	}
	// 正则不再对整条原文匹配
	re := &mustEngine(t, "version: 1\nrules:\n  - id: u\n    source: user\n    scope: global\n    match: {payload: shell, command_regex: \"^git status\"}\n    action: allow\n").rules[0]
	if matchRequest(re, shell("git status; rm -rf ~"), RiskDestructive, projCtx) {
		t.Error("正则 ^git status 不应匹配 git status; rm -rf ~")
	}
}

func TestEvaluateUserDenyPrecedence(t *testing.T) {
	e := mustEngine(t, engineRules)
	for _, ctx := range []Context{projCtx, otherCtx} {
		if d := e.Evaluate(shell("npm run build; true"), ctx); d.Action != ActionDeny || d.RuleID != "global-deny-npm-run" {
			t.Errorf("%s: deny 前缀任一段命中即生效, 得到 %s/%q", ctx.ProjectRoot, d.Action, d.RuleID)
		}
	}

	const domainRules = `
version: 1
rules:
  - id: user-deny-evil
    source: user
    scope: global
    match: {payload: network, domain: evil.com}
    action: deny
  - id: learned-evil
    source: learned
    scope: {project: "~/proj"}
    match: {payload: network, domain: evil.com, risk: network}
    action: allow
  - id: learned-good
    source: learned
    scope: global
    match: {payload: network, domain: good.com, risk: network}
    action: allow
`
	de := mustEngine(t, domainRules)
	for _, u := range []string{"https://evil.com/", "https://EVIL.com/", "https://evil.com./", "https://API.Evil.Com./x"} {
		if d := de.Evaluate(Payload{Type: PayloadNetwork, URL: u}, projCtx); d.Action != ActionDeny || d.RuleID != "user-deny-evil" {
			t.Errorf("%s: 应被 domain deny 拦下, 得到 %s/%q", u, d.Action, d.RuleID)
		}
	}
	if d := de.Evaluate(Payload{Type: PayloadNetwork, URL: "https://good.com/"}, projCtx); d.Action != ActionAllow || d.RuleID != "learned-good" {
		t.Errorf("good.com 应由 learned 放行, 得到 %s/%q", d.Action, d.RuleID)
	}

	const scopeRules = `
version: 1
rules:
  - id: user-deny-curl
    source: user
    scope: global
    match: {payload: shell, command_prefix: curl}
    action: deny
  - id: learned-curl
    source: learned
    scope: {project: "~/proj"}
    match: {payload: shell, command_prefix: curl, risk: network}
    action: allow
`
	if d := mustEngine(t, scopeRules).Evaluate(shell("curl https://x.y"), projCtx); d.Action != ActionDeny || d.RuleID != "user-deny-curl" {
		t.Errorf("全局用户 deny 应压项目范围 learned allow, 得到 %s/%q", d.Action, d.RuleID)
	}

	// 用户 deny 之外仍按范围具体度排序；ask 规则与 deny 一样任一段命中即生效。
	const askRules = `
version: 1
rules:
  - id: builtin-readonly
    source: builtin
    scope: global
    match: {risk: read}
    action: allow
  - id: proj-ask-cat
    source: user
    scope: {project: "~/proj"}
    match: {payload: shell, command_prefix: cat}
    action: ask
`
	ae := mustEngine(t, askRules)
	for _, c := range []struct {
		cmd  string
		ctx  Context
		want Action
		rule string
	}{
		{"cat a", projCtx, ActionAsk, "proj-ask-cat"},
		{"cat a; true", projCtx, ActionAsk, "proj-ask-cat"},
		{"cat a", otherCtx, ActionAllow, "builtin-readonly"},
	} {
		if d := ae.Evaluate(shell(c.cmd), c.ctx); d.Action != c.want || d.RuleID != c.rule {
			t.Errorf("%q@%s: 得到 %s/%q, 想要 %s/%q", c.cmd, c.ctx.ProjectRoot, d.Action, d.RuleID, c.want, c.rule)
		}
	}
}

func TestEvaluateRestrictiveNormalized(t *testing.T) {
	// deny 与 ask 规则同时按归一化的命令匹配：argv[0] 取 basename、剥掉包装和子命令前的全局选项。
	const rules = `
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
  - id: deny-commit
    source: user
    scope: global
    match: {payload: shell, command_prefix: "git commit"}
    action: deny
  - id: deny-npm-run
    source: user
    scope: global
    match: {payload: shell, command_prefix: "npm run"}
    action: deny
  - id: deny-curl
    source: user
    scope: global
    match: {payload: shell, command_regex: "^curl "}
    action: deny
  - id: ask-log
    source: user
    scope: {project: "~/proj"}
    match: {payload: shell, command_prefix: "git log"}
    action: ask
`
	e := mustEngine(t, rules)
	cases := []struct {
		cmd  string
		want Action
		rule string
	}{
		{"/usr/bin/git commit -m x", ActionDeny, "deny-commit"},
		{"git --no-pager commit -m x", ActionDeny, "deny-commit"},
		{"git -C sub commit -m x", ActionDeny, "deny-commit"},
		{"./node_modules/.bin/npm run build", ActionDeny, "deny-npm-run"},
		{"npm run-script build", ActionDeny, "deny-npm-run"},
		{"npm rum build", ActionDeny, "deny-npm-run"},
		{"npm urn build", ActionDeny, "deny-npm-run"},
		{"pnpm run-script build", ActionAllow, "builtin-write-in-project"},
		{"'curl' https://x.y", ActionDeny, "deny-curl"},
		{`c\url https://x.y`, ActionDeny, "deny-curl"},
		{"FOO=1 curl https://x.y", ActionDeny, "deny-curl"},
		{"curl\thttps://x.y", ActionDeny, "deny-curl"},
		{"/usr/bin/curl https://x.y", ActionDeny, "deny-curl"},
		{"git --no-pager log", ActionAsk, "ask-log"},
		{"/usr/bin/git log; true", ActionAsk, "ask-log"},
		{"git status", ActionAllow, "builtin-readonly"},
	}
	for _, c := range cases {
		d := e.Evaluate(shell(c.cmd), projCtx)
		if d.Action != c.want || d.RuleID != c.rule {
			t.Errorf("%q 得到 %s/%q (%s), 想要 %s/%q", c.cmd, d.Action, d.RuleID, d.Reason, c.want, c.rule)
		}
	}
}

func TestEvaluateRestrictiveSeesThroughShell(t *testing.T) {
	// deny 与 ask 规则看穿 sh -c 这类包装；allow 规则不看穿。
	const rules = `
version: 1
rules:
  - id: builtin-write-in-project
    source: builtin
    scope: global
    match: {risk: write_in_project}
    action: allow
  - id: deny-commit
    source: user
    scope: global
    match: {payload: shell, command_prefix: "git commit"}
    action: deny
  - id: deny-curl
    source: user
    scope: global
    match: {payload: shell, command_regex: "^curl "}
    action: deny
  - id: ask-npm-run
    source: user
    scope: {project: "~/proj"}
    match: {payload: shell, command_prefix: "npm run"}
    action: ask
  - id: push
    source: user
    scope: global
    match: {payload: shell, command_prefix: "git push origin"}
    action: allow
`
	e := mustEngine(t, rules)
	cases := []struct {
		cmd  string
		want Action
		rule string
	}{
		{"sh -c 'git commit -m x'", ActionDeny, "deny-commit"},
		{`bash -lc "cd x && git commit -m y"`, ActionDeny, "deny-commit"},
		{`bash -c "sh -c 'git commit -m z'"`, ActionDeny, "deny-commit"},
		{"zsh -c 'true; /usr/bin/git --no-pager commit'", ActionDeny, "deny-commit"},
		{"fish -c 'git commit -m x'", ActionDeny, "deny-commit"},
		{"env FOO=1 sh -c 'curl https://x.y'", ActionDeny, "deny-curl"},
		{"sh -c 'npm run build'", ActionAsk, "ask-npm-run"},
		{"sh -c 'git push origin main'", ActionAsk, ""},
		{"git push origin main", ActionAllow, "push"},
	}
	for _, c := range cases {
		d := e.Evaluate(shell(c.cmd), projCtx)
		if d.Action != c.want || d.RuleID != c.rule {
			t.Errorf("%q 得到 %s/%q (%s), 想要 %s/%q", c.cmd, d.Action, d.RuleID, d.Reason, c.want, c.rule)
		}
	}
}

func TestEvaluateShellSidePathAndDomain(t *testing.T) {
	const rules = `
version: 1
rules:
  - id: builtin-write-in-project
    source: builtin
    scope: global
    match: {risk: write_in_project}
    action: allow
  - id: builtin-readonly
    source: builtin
    scope: global
    match: {risk: read}
    action: allow
  - id: learned-curl
    source: learned
    scope: global
    match: {payload: shell, command_prefix: curl, risk: network}
    action: allow
  - id: learned-wget
    source: learned
    scope: global
    match: {payload: shell, command_prefix: wget, risk: network}
    action: allow
  - id: learned-clone
    source: learned
    scope: global
    match: {payload: shell, command_prefix: "git clone", risk: network}
    action: allow
  - id: deny-private
    source: user
    scope: global
    match: {path_glob: "~/proj/private/**"}
    action: deny
  - id: deny-evil
    source: user
    scope: global
    match: {domain: evil.com}
    action: deny
  - id: allow-tmp-writes
    source: user
    scope: global
    match: {path_glob: "/tmp/**"}
    action: allow
`
	e := mustEngine(t, rules)
	cases := []struct {
		cmd  string
		want Action
		rule string
	}{
		{"echo x > private/a", ActionDeny, "deny-private"},
		{"cp README.md private/a", ActionDeny, "deny-private"},
		{"mkdir -p private/sub", ActionDeny, "deny-private"},
		{"cd private && touch a", ActionDeny, "deny-private"},
		{"sh -c 'echo x >> private/log'", ActionDeny, "deny-private"},
		{"rm -rf private", ActionDeny, "deny-private"},
		{"curl -o private/x https://good.com/x", ActionDeny, "deny-private"},
		// 隐式写入：定级至少 write_in_project 的段写它的工作目录与全部操作数、选项值
		{"go build -o private/a .", ActionDeny, "deny-private"},
		{"git mv x private/a", ActionDeny, "deny-private"},
		{"prettier --write private/a", ActionDeny, "deny-private"},
		{"cd private && git init", ActionDeny, "deny-private"},
		{"git -C private init", ActionDeny, "deny-private"},
		{"go build ./...", ActionAllow, "builtin-write-in-project"},
		{"cd private && cat a", ActionAllow, "builtin-readonly"},
		{"mkdir -p public/sub", ActionAllow, "builtin-write-in-project"},
		{"cat private/a", ActionAllow, "builtin-readonly"},
		{"curl https://evil.com/x", ActionDeny, "deny-evil"},
		{"wget https://EVIL.com./x", ActionDeny, "deny-evil"},
		{"git clone https://evil.com/r", ActionDeny, "deny-evil"},
		{"curl --url=https://api.evil.com/x", ActionDeny, "deny-evil"},
		{"curl evil.com/x", ActionDeny, "deny-evil"},
		{`URL=https://evil.com/x curl "$URL"`, ActionDeny, "deny-evil"},
		{"bash -c 'curl -s https://evil.com/i | true'", ActionDeny, "deny-evil"},
		{"curl https://evil%2ecom/x", ActionDeny, "deny-evil"},
		{"curl --connect-to ::evil.com: https://good.com", ActionDeny, "deny-evil"},
		{"curl --connect-to good.com:443:evil.com:443 https://good.com", ActionDeny, "deny-evil"},
		{"curl --resolve=good.com:443:evil.com https://good.com", ActionDeny, "deny-evil"},
		{"curl -x evil.com:8080 https://good.com", ActionDeny, "deny-evil"},
		{"curl -xevil.com:8080 https://good.com", ActionDeny, "deny-evil"},
		{"curl --proxy evil.com:3128 https://good.com", ActionDeny, "deny-evil"},
		{"curl https://good.com/x", ActionAllow, "learned-curl"},
		{"echo https://notevil.com", ActionAllow, "builtin-readonly"},
		// allow 规则的 path_glob 只对 file_write 生效
		{"echo x > /tmp/a", ActionAsk, ""},
	}
	for _, c := range cases {
		d := e.Evaluate(shell(c.cmd), projCtx)
		if d.Action != c.want || d.RuleID != c.rule {
			t.Errorf("%q 得到 %s/%q (%s), 想要 %s/%q", c.cmd, d.Action, d.RuleID, d.Reason, c.want, c.rule)
		}
	}
	if d := e.Evaluate(Payload{Type: PayloadFileWrite, Path: "/home/u/proj/private/a"}, projCtx); d.Action != ActionDeny {
		t.Errorf("file_write 仍应被 path_glob deny 拦下, 得到 %s/%q", d.Action, d.RuleID)
	}
	if d := e.Evaluate(Payload{Type: PayloadNetwork, URL: "https://evil.com/"}, projCtx); d.Action != ActionDeny {
		t.Errorf("network 仍应被 domain deny 拦下, 得到 %s/%q", d.Action, d.RuleID)
	}
	if d := e.Evaluate(Payload{Type: PayloadNetwork, URL: "https://evil%2ecom/"}, projCtx); d.Action != ActionDeny {
		t.Errorf("百分号编码的主机也应被 domain deny 拦下, 得到 %s/%q", d.Action, d.RuleID)
	}
	// 工作目录在受保护目录里时，会写的命令都算写进去
	for _, c := range []string{"git init", "go mod init x"} {
		p := Payload{Type: PayloadShell, Command: c, Cwd: "/home/u/proj/private"}
		if d := e.Evaluate(p, projCtx); d.Action != ActionDeny || d.RuleID != "deny-private" {
			t.Errorf("Cwd private 下 %q 得到 %s/%q, 想要 deny", c, d.Action, d.RuleID)
		}
	}
}

func TestEvaluatePathGlobFoldsCase(t *testing.T) {
	const rules = `
version: 1
rules:
  - id: builtin-write-in-project
    source: builtin
    scope: global
    match: {risk: write_in_project}
    action: allow
  - id: deny-secrets
    source: user
    scope: global
    match: {payload: file_write, path_glob: "~/proj/secrets/**"}
    action: deny
  - id: allow-tmp
    source: user
    scope: global
    match: {payload: file_write, path_glob: "/tmp/**"}
    action: allow
`
	e := mustEngine(t, rules)
	cases := []struct {
		path string
		want Action
		rule string
	}{
		{"/home/u/proj/secrets/a", ActionDeny, "deny-secrets"},
		{"/home/u/proj/SECRETS/a", ActionDeny, "deny-secrets"},
		{"/home/u/proj/Secrets/sub/b", ActionDeny, "deny-secrets"},
		{"/home/u/proj/src/a", ActionAllow, "builtin-write-in-project"},
		{"/tmp/x", ActionAllow, "allow-tmp"},
		{"/TMP/x", ActionAsk, ""}, // allow 不折叠大小写
	}
	for _, c := range cases {
		d := e.Evaluate(Payload{Type: PayloadFileWrite, Path: c.path}, projCtx)
		if d.Action != c.want || d.RuleID != c.rule {
			t.Errorf("%s 得到 %s/%q, 想要 %s/%q", c.path, d.Action, d.RuleID, c.want, c.rule)
		}
	}
}

func TestEvaluateScopeCleansPaths(t *testing.T) {
	e := mustEngine(t, engineRules)
	for _, root := range []string{"/home/u/proj/", "/home/u/proj/.", "/home/u//proj"} {
		ctx := Context{ProjectRoot: root, HomeDir: "/home/u", MachineID: "m1"}
		if d := e.Evaluate(shell("git push origin main"), ctx); d.Action != ActionAllow || d.RuleID != "user-push-proj" {
			t.Errorf("ProjectRoot %q 应匹配 ~/proj, 得到 %s/%q", root, d.Action, d.RuleID)
		}
	}
	slash := mustEngine(t, "version: 1\nrules:\n  - id: p\n    source: user\n    scope: {project: \"~/proj/\"}\n    match: {payload: shell, command_prefix: \"git push origin\"}\n    action: allow\n")
	if d := slash.Evaluate(shell("git push origin main"), projCtx); d.Action != ActionAllow || d.RuleID != "p" {
		t.Errorf("规则范围带尾斜杠也应匹配, 得到 %s/%q", d.Action, d.RuleID)
	}
}

func TestNewEngineCopiesPointers(t *testing.T) {
	rs, err := ParseRuleSet([]byte(engineRules))
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(rs)
	if err != nil {
		t.Fatal(err)
	}
	// 改动调用方手里的规则集不得影响引擎
	for i := range rs.Rules {
		r := &rs.Rules[i]
		if r.Enabled != nil {
			*r.Enabled = true // 打开 disabled-deny-ls
		}
		if r.Match.Risk != nil {
			*r.Match.Risk = RiskDestructive
		}
	}
	if d := e.Evaluate(shell("ls -la"), projCtx); d.Action != ActionAllow || d.RuleID != "builtin-readonly" {
		t.Errorf("引擎应持有规则副本, 得到 %s/%q", d.Action, d.RuleID)
	}
}

func TestEvaluateOrdering(t *testing.T) {
	m1 := projCtx // MachineID m1
	cases := []struct {
		name, rules string
		want        Action
		rule        string
	}{
		{"非 user 规则 project 压 machine", `
version: 1
rules:
  - id: machine-deny
    source: builtin
    scope: {machine: m1}
    match: {payload: shell, command_prefix: cat}
    action: deny
  - id: project-allow
    source: learned
    scope: {project: "~/proj"}
    match: {payload: shell, command_prefix: cat, risk: read}
    action: allow
`, ActionAllow, "project-allow"},
		{"非 user 规则 machine 压 global", `
version: 1
rules:
  - id: global-allow
    source: learned
    scope: global
    match: {payload: shell, command_prefix: cat, risk: read}
    action: allow
  - id: machine-ask
    source: builtin
    scope: {machine: m1}
    match: {payload: shell, command_prefix: cat}
    action: ask
`, ActionAsk, "machine-ask"},
		{"同范围 allow 压 ask", `
version: 1
rules:
  - id: ask-cat
    source: builtin
    scope: global
    match: {payload: shell, command_prefix: cat}
    action: ask
  - id: allow-cat
    source: builtin
    scope: global
    match: {payload: shell, command_prefix: cat}
    action: allow
`, ActionAllow, "allow-cat"},
		{"非 user 的 deny 输给更具体的 allow", `
version: 1
rules:
  - id: global-deny
    source: builtin
    scope: global
    match: {payload: shell, command_prefix: cat}
    action: deny
  - id: machine-allow
    source: builtin
    scope: {machine: m1}
    match: {payload: shell, command_prefix: cat}
    action: allow
`, ActionAllow, "machine-allow"},
		{"全局用户 ask 压内置 allow", `
version: 1
rules:
  - id: builtin-allow
    source: builtin
    scope: {project: "~/proj"}
    match: {payload: shell, command_prefix: cat}
    action: allow
  - id: user-ask
    source: user
    scope: global
    match: {payload: shell, command_prefix: cat}
    action: ask
`, ActionAsk, "user-ask"},
		{"全局用户 allow 压项目范围 learned deny", `
version: 1
rules:
  - id: learned-deny
    source: learned
    scope: {project: "~/proj"}
    match: {payload: shell, command_prefix: cat}
    action: deny
  - id: user-allow
    source: user
    scope: global
    match: {payload: shell, command_prefix: cat}
    action: allow
`, ActionAllow, "user-allow"},
		{"用户规则之间仍按范围：项目 allow 压全局 ask", `
version: 1
rules:
  - id: user-ask
    source: user
    scope: global
    match: {payload: shell, command_prefix: cat}
    action: ask
  - id: user-allow
    source: user
    scope: {project: "~/proj"}
    match: {payload: shell, command_prefix: cat}
    action: allow
`, ActionAllow, "user-allow"},
	}
	for _, c := range cases {
		d := mustEngine(t, c.rules).Evaluate(shell("cat a.txt"), m1)
		if d.Action != c.want || d.RuleID != c.rule {
			t.Errorf("%s: 得到 %s/%q, 想要 %s/%q", c.name, d.Action, d.RuleID, c.want, c.rule)
		}
	}
}

func TestHostMatches(t *testing.T) {
	cases := []struct {
		url, domain string
		want        bool
	}{
		{"https://example.com/", "example.com", true},
		{"https://api.example.com/v1", "example.com", true},
		{"https://API.EXAMPLE.COM/", "example.com", true},
		{"https://example.com./", "example.com", true},
		{"https://example.com:8443/", "Example.COM.", true},
		{"https://notexample.com/", "example.com", false},
		{"https://example.com.evil.net/", "example.com", false},
		{"https://example.com@evil.net/", "example.com", false},
		{"https://example%2ecom/x", "example.com", true},
		{"https://api.example%2Ecom:8443/", "example.com", true},
		{"example.com/x", "example.com", false},
	}
	for _, c := range cases {
		if got := hostMatches(c.url, c.domain); got != c.want {
			t.Errorf("hostMatches(%q, %q) = %v", c.url, c.domain, got)
		}
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
		{"git status $(date)", "git status", false},
		{"git status; echo $(x)", "git status", false},
		{"git status 'literal $(x)'", "git status", true},
		{"GIT_SSH_COMMAND=x git push origin main", "git push origin", false},
		{"env PATH=/tmp/x git status", "git status", false},
		{"git status > out.txt", "git status", false},
		{"git status > ~/.bashrc", "git status", false},
		{"git status < in.txt", "git status", false},
		{"git status > /dev/fd/$X", "git status", false},
		{"git status > /dev/fd/../../tmp/x", "git status", false},
		{"git status 2>/dev/null", "git status", true},
		{"git status &>/dev/null < /dev/null", "git status", true},
		{"git status 2>&1 | git status", "git status", true},
	}
	for _, c := range cases {
		if got := everySegmentHasPrefix(c.cmd, c.prefix); got != c.want {
			t.Errorf("everySegmentHasPrefix(%q, %q) = %v", c.cmd, c.prefix, got)
		}
	}
}
