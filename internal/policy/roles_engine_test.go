package policy

import (
	"strings"
	"testing"
)

const roleEngineRules = `
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
  - id: user-allow-push
    source: user
    scope: global
    match: {payload: shell, command_prefix: "git push"}
    action: allow
  - id: user-allow-rm-build
    source: user
    scope: global
    match: {payload: shell, command_prefix: "rm -rf build", risk: destructive}
    action: allow
  - id: user-deny-publish
    source: user
    scope: global
    match: {payload: shell, command_prefix: "npm publish"}
    action: deny
  - id: learned-deny-curl
    source: learned
    scope: global
    match: {payload: shell, command_prefix: "curl"}
    action: deny
`

const roleEngineRoles = `
version: 1
roles:
  - name: 写作
    dirs: ["~/proj"]
    ceiling: write_in_project
    forbid:
      - {command_prefix: "git push"}
      - {command_prefix: "npm publish"}
      - {payload: mcp}
  - name: 运维
    ceiling: destructive
  - name: 开发
    ceiling: network
`

const roleHome = "/Users/u"

func roleCtx(t *testing.T, name string) Context {
	t.Helper()
	rs, err := ParseRoleSet([]byte(roleEngineRoles))
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{ProjectRoot: roleHome + "/proj", HomeDir: roleHome, MachineID: "m"}
	if name != "" {
		ctx.Role = rs.Find(name)
		if ctx.Role == nil {
			t.Fatalf("没有角色 %s", name)
		}
	}
	return ctx
}

func shellAt(cmd string) Payload {
	return Payload{Type: PayloadShell, Command: cmd, Cwd: roleHome + "/proj"}
}

func TestEvaluateRoleOrder(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	cases := []struct {
		name      string
		role      string
		p         Payload
		action    Action
		forbidden bool
		beyond    bool
		reason    string
	}{
		// 用户 deny 压过 forbid：npm publish 同时命中两者，按用户规则拒（理由是规则）
		{"用户 deny 压 forbid", "写作", shellAt("npm publish"), ActionDeny, false, false, "命中规则 user-deny-publish"},
		// forbid 压过用户 allow
		{"forbid 压用户 allow", "写作", shellAt("git push origin main"), ActionDeny, true, false, `角色「写作」禁用：command_prefix "git push"`},
		{"forbid 看穿 sh -c", "写作", shellAt(`sh -c "git push origin main"`), ActionDeny, true, false, "禁用"},
		{"forbid 按 payload", "写作", Payload{Type: PayloadMCP, Server: "github", Tool: "get_issue"}, ActionDeny, true, false, "payload mcp"},
		// ceiling 压过用户 allow：git push 是 outward，开发的上限是 network
		{"ceiling 压用户 allow", "开发", shellAt("git push origin main"), ActionAsk, false, true, "超出角色「开发」的边界（上限 network，这次是 outward）"},
		// ceiling 压过写明 risk: destructive 的用户 allow
		{"ceiling 压 destructive allow", "开发", shellAt("rm -rf build"), ActionAsk, false, true, "这次是 destructive"},
		// ceiling 之内照旧走规则
		{"ceiling 内照旧放行", "开发", shellAt("ls"), ActionAllow, false, false, "命中规则 builtin-readonly"},
		{"上限 destructive 时规则照旧", "运维", shellAt("git push origin main"), ActionAllow, false, false, "命中规则 user-allow-push"},
		// 其他来源的 deny 仍是 deny（比 ceiling 的 ask 更严）
		{"learned deny 不被 ceiling 放宽", "写作", shellAt("curl https://example.com"), ActionDeny, false, false, "命中规则 learned-deny-curl"},
		// 没有角色：与原来一样
		{"无角色 git push 放行", "", shellAt("git push origin main"), ActionAllow, false, false, "命中规则 user-allow-push"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := e.Evaluate(c.p, roleCtx(t, c.role))
			if d.Action != c.action || d.Forbidden != c.forbidden || d.BeyondRole != c.beyond {
				t.Fatalf("得到 %+v，想要 action %s forbidden %v beyond %v", d, c.action, c.forbidden, c.beyond)
			}
			if !strings.Contains(d.Reason, c.reason) {
				t.Errorf("理由 %q 不含 %q", d.Reason, c.reason)
			}
			if d.Role != c.role {
				t.Errorf("Decision.Role = %q，想要 %q", d.Role, c.role)
			}
		})
	}
}

func TestEvaluateRolePaused(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	e.SetPaused(true)
	// 暂停下 forbid 仍拒
	if d := e.Evaluate(shellAt("git push origin main"), roleCtx(t, "写作")); d.Action != ActionDeny || !d.Forbidden {
		t.Errorf("暂停时 forbid 应仍拒: %+v", d)
	}
	// 暂停下用户 deny 仍拒
	if d := e.Evaluate(shellAt("npm publish"), roleCtx(t, "写作")); d.Action != ActionDeny || d.RuleID != "user-deny-publish" {
		t.Errorf("暂停时用户 deny 应仍拒: %+v", d)
	}
	// 越界在暂停时仍是 ask（带越界标记）
	if d := e.Evaluate(shellAt("git push origin main"), roleCtx(t, "开发")); d.Action != ActionAsk || !d.BeyondRole {
		t.Errorf("暂停时越界应 ask: %+v", d)
	}
	// 其余一律 ask
	if d := e.Evaluate(shellAt("ls"), roleCtx(t, "开发")); d.Action != ActionAsk || d.BeyondRole || d.Reason != "自动审批已暂停" {
		t.Errorf("暂停时其余应 ask: %+v", d)
	}
}

func TestEvaluateBrokenRole(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	ctx := Context{ProjectRoot: roleHome + "/proj", HomeDir: roleHome, Role: BrokenRole(errTest("第 3 行不对"))}
	if d := e.Evaluate(shellAt("ls"), ctx); d.Action != ActionAllow || d.BeyondRole {
		t.Errorf("坏文件时只读应照旧放行: %+v", d)
	}
	d := e.Evaluate(Payload{Type: PayloadFileWrite, Path: roleHome + "/proj/a.txt", Cwd: roleHome + "/proj"}, ctx)
	if d.Action != ActionAsk || !d.BeyondRole || !strings.Contains(d.Reason, "roles.yaml 有误：第 3 行不对") {
		t.Errorf("坏文件时项目内写入应越界问人: %+v", d)
	}
	if d.Role != "" {
		t.Errorf("坏文件时角色名应为空（未分配）: %q", d.Role)
	}
	// 坏文件不挡用户 deny
	if d := e.Evaluate(shellAt("npm publish"), ctx); d.Action != ActionDeny {
		t.Errorf("坏文件时用户 deny 应仍拒: %+v", d)
	}
}

func TestEvaluateRoleTerritory(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	write := Payload{Type: PayloadFileWrite, Path: roleHome + "/proj/a.txt", Cwd: roleHome + "/proj"}
	// 地盘内：项目根语义不变，项目内写入是 write_in_project，按内置规则放行
	if d := e.Evaluate(write, roleCtx(t, "写作")); d.Action != ActionAllow || d.Risk != RiskWriteInProject {
		t.Errorf("地盘内项目内写入: %+v", d)
	}
	// 地盘外（用 RAILSE_ROLE 在别处启动）：项目根按空处理，写入是 write_outside，超出写作的上限
	other := Payload{Type: PayloadFileWrite, Path: roleHome + "/other/a.txt", Cwd: roleHome + "/other"}
	ctx := roleCtx(t, "写作")
	ctx.ProjectRoot = roleHome + "/other"
	if d := e.Evaluate(other, ctx); d.Risk != RiskWriteOutside || d.Action != ActionAsk || !d.BeyondRole {
		t.Errorf("地盘外写入应为 write_outside 越界: %+v", d)
	}
	if got := ctx.Root(); got != "" {
		t.Errorf("地盘外 Root() = %q，想要空", got)
	}
	// 没写 dirs 的角色不做这一步
	ctx = roleCtx(t, "运维")
	ctx.ProjectRoot = roleHome + "/other"
	if d := e.Evaluate(other, ctx); d.Risk != RiskWriteInProject {
		t.Errorf("没写 dirs 的角色不该改项目根: %+v", d)
	}
}

// roles.yaml 本身在 ~/.railse 下，已是敏感路径：agent 改它按 destructive 定级，任何角色都越不过（除非上限就是 destructive）。
func TestRolesFileIsDestructive(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	for _, p := range []Payload{
		{Type: PayloadFileWrite, Path: roleHome + "/.railse/roles.yaml", Cwd: roleHome + "/proj"},
		{Type: PayloadShell, Command: "echo x > ~/.railse/roles.yaml", Cwd: roleHome + "/proj"},
		{Type: PayloadShell, Command: "rm " + roleHome + "/.railse/roles.yaml", Cwd: roleHome + "/proj"},
	} {
		if r := Classify(p, Context{ProjectRoot: roleHome + "/proj", HomeDir: roleHome}); r != RiskDestructive {
			t.Errorf("%+v 定级 %s，想要 destructive", p, r)
		}
		if d := e.Evaluate(p, roleCtx(t, "开发")); d.Action != ActionAsk || !d.BeyondRole {
			t.Errorf("开发改 roles.yaml 应越界问人: %+v", d)
		}
	}
}

// 守护进程目录不在 ~/.railse 时（--dir、RAILSE_HOME），由 Context.ProtectedDirs 带进来，里面的写入一律 destructive。
func TestProtectedDirsAreDestructive(t *testing.T) {
	ctx := Context{ProjectRoot: roleHome + "/proj", HomeDir: roleHome, ProtectedDirs: []string{roleHome + "/proj/.rs"}}
	for _, p := range []Payload{
		{Type: PayloadFileWrite, Path: roleHome + "/proj/.rs/roles.yaml", Cwd: roleHome + "/proj"},
		{Type: PayloadFileWrite, Path: ".rs/rules.yaml", Cwd: roleHome + "/proj"},
		{Type: PayloadFileWrite, Path: roleHome + "/proj/.RS/trusts.json", Cwd: roleHome + "/proj"}, // 大小写折叠，同敏感路径表
		{Type: PayloadShell, Command: "echo x > .rs/roles.yaml", Cwd: roleHome + "/proj"},
		{Type: PayloadShell, Command: "rm -rf " + roleHome + "/proj/.rs", Cwd: roleHome + "/proj"},
	} {
		if r := Classify(p, ctx); r != RiskDestructive {
			t.Errorf("%+v 定级 %s，想要 destructive", p, r)
		}
	}
	// 不带 ProtectedDirs 时同一写入是项目内（0.2.3 的口径）
	ctx.ProtectedDirs = nil
	if r := Classify(Payload{Type: PayloadFileWrite, Path: roleHome + "/proj/.rs/roles.yaml", Cwd: roleHome + "/proj"}, ctx); r != RiskWriteInProject {
		t.Errorf("没有受保护目录时: %s", r)
	}
}

// forbid 看穿命令替换、反引号、进程替换与 eval；带命令类 forbid 的角色碰上解析不了的结构（heredoc 等）按越界问人，不直接拒。
func TestForbidSeesInsideSubstitutions(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	for _, cmd := range []string{
		`echo "$(git push origin main)"`,
		"echo `git push origin main`",
		`cat <(git push origin main)`,
		`echo $(echo $(git push origin main))`,
		`eval git push origin main`,
		`eval "git push origin main"`,
		`x=$(eval 'git push origin main')`,
		`ls && echo "$(sh -c 'git push origin main')"`,
	} {
		d := e.Evaluate(shellAt(cmd), roleCtx(t, "写作"))
		if d.Action != ActionDeny || !d.Forbidden {
			t.Errorf("%s: 应按禁用拒: %+v", cmd, d)
		}
	}
	for _, cmd := range []string{
		"bash <<EOF\ngit push origin main\nEOF",
		`echo "$(git push origin main"`, // 没闭合
		`echo $'\x67it' push`,
	} {
		d := e.Evaluate(shellAt(cmd), roleCtx(t, "写作"))
		if d.Action != ActionAsk || !d.BeyondRole || !strings.Contains(d.Reason, "含无法检查的子命令") {
			t.Errorf("%q: 含解析不了的结构应越界问人: %+v", cmd, d)
		}
	}
	// 能检查、没命中的照旧
	if d := e.Evaluate(shellAt(`echo "$(ls)"`), roleCtx(t, "写作")); d.Forbidden || strings.Contains(d.Reason, "无法检查") {
		t.Errorf("能检查且没命中的不该拒或越界: %+v", d)
	}
	// 没有命令类 forbid 的角色不因不透明结构越界
	if d := e.Evaluate(shellAt("cat <<EOF\nhi\nEOF"), roleCtx(t, "运维")); d.BeyondRole {
		t.Errorf("运维没有命令类禁用: %+v", d)
	}
}

// 第三步的边界：learned deny 输给命中的 user allow（0.2 语义），这时按 ceiling 越界问人，而不是拒。
func TestLearnedDenyLosesToUserAllowUnderCeiling(t *testing.T) {
	e := mustEngine(t, roleEngineRules+`
  - id: user-allow-curl-x
    source: user
    scope: global
    match: {payload: shell, command_prefix: "curl https://x.example"}
    action: allow
`)
	d := e.Evaluate(shellAt("curl https://x.example"), roleCtx(t, "写作"))
	if d.Action != ActionAsk || !d.BeyondRole {
		t.Fatalf("选中的是 user allow，应按 ceiling 越界: %+v", d)
	}
}

// 有角色的会话里改 RAILSE_ROLE、RAILSE_TRUST、RAILSE_HOME 的命令（典型：嵌套起一个宽松角色的 CLI）按越界问人，任何角色都一样；
// 没有角色时照旧。
func TestRailseEnvChangeIsBeyondRole(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	for _, cmd := range []string{
		"RAILSE_ROLE=运维 claude -p hi",
		"env RAILSE_ROLE=运维 codex exec x",
		"export RAILSE_ROLE=运维 && gemini",
		"declare -x RAILSE_TRUST=24h",
		"typeset RAILSE_HOME=/tmp/x",
		"readonly RAILSE_ROLE=x",
		"unset RAILSE_ROLE; claude",
		"env -u RAILSE_ROLE claude",
		`sh -c 'RAILSE_ROLE=x claude'`,
		`echo "$(RAILSE_TRUST=8h claude -p x)"`,
		`eval RAILSE_ROLE=x claude`,
		`export "$V"=x`,
		"RAILSE_ROLE+=x claude",
	} {
		for _, role := range []string{"写作", "运维"} {
			d := e.Evaluate(shellAt(cmd), roleCtx(t, role))
			if d.Action != ActionAsk || !d.BeyondRole || !strings.Contains(d.Reason, "试图更改 Railse 的角色或放行设置") {
				t.Errorf("%s（%s）: 应越界问人: %+v", cmd, role, d)
			}
		}
		if d := e.Evaluate(shellAt(cmd), roleCtx(t, "")); d.BeyondRole {
			t.Errorf("%s: 没有角色时不该越界: %+v", cmd, d)
		}
	}
	for _, cmd := range []string{"echo $RAILSE_ROLE", "printenv RAILSE_ROLE", "ls"} {
		if d := e.Evaluate(shellAt(cmd), roleCtx(t, "运维")); d.BeyondRole {
			t.Errorf("%s: 只读不该越界: %+v", cmd, d)
		}
	}
}
