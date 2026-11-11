package policy

import (
	"strings"
	"testing"
	"time"
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

// eval 与 sh -c 类：剥掉 -- 再递归；参数含变量展开、通配等不能证明是完整字面量的成分时算不透明（带命令类禁用的角色越界问人）。
func TestForbidEvalOptionsAndDynamicArgs(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	if d := e.Evaluate(shellAt(`eval -- 'git push origin main'`), roleCtx(t, "写作")); !d.Forbidden {
		t.Errorf("eval -- 应剥掉再匹配: %+v", d)
	}
	for _, cmd := range []string{
		`eval "git ${OP:-push} origin main"`,
		`eval $X`,
		`bash -c "$CMD"`,
		`sh -c "$CMD"`,
		`eval git pu*`,
	} {
		d := e.Evaluate(shellAt(cmd), roleCtx(t, "写作"))
		if d.Action != ActionAsk || !d.BeyondRole || !strings.Contains(d.Reason, "含无法检查的子命令") {
			t.Errorf("%s: 动态参数应按不透明越界: %+v", cmd, d)
		}
	}
	// 字面量参数照常检查、不算不透明
	if d := e.Evaluate(shellAt(`bash -c 'echo hi'`), roleCtx(t, "写作")); strings.Contains(d.Reason, "无法检查") {
		t.Errorf("字面量参数不该算不透明: %+v", d)
	}
}

// 1 MiB 的畸形输入：不 panic、在时间预算内出结论（有角色时多一遍禁用检查）。
func TestRoleEvaluateHugeMalformedInput(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	const n = 1 << 20
	inputs := map[string]string{
		"嵌套命令替换":   strings.Repeat("$(", n/2),
		"嵌套 sh -c": strings.Repeat("sh -c '", n/7),
		"反引号":      strings.Repeat("`a` ", n/4),
		"引号乱序":     strings.Repeat(`"'$(`+"`", n/5),
		"eval 链":   strings.Repeat("eval ", n/5) + "git push",
		"长命令":      "git push " + strings.Repeat("x", n),
		"很多段":      strings.Repeat("ls; ", n/4) + "git push",
		"括号":       strings.Repeat("<(", n/4) + strings.Repeat(")", n/4),
		"heredoc":  "cat <<EOF\n" + strings.Repeat("$(git push)\n", n/12) + "EOF",
		"二进制垃圾": string(func() []byte {
			b := make([]byte, n)
			for i := range b {
				b[i] = byte(i*131 + 7)
			}
			return b
		}()),
	}
	for name, cmd := range inputs {
		start := time.Now()
		d := e.Evaluate(shellAt(cmd), roleCtx(t, "写作"))
		el := time.Since(start)
		t.Logf("%s: %v", name, el)
		if el > 30*time.Second { // -race 下也要过，预算放宽
			t.Errorf("%s: 用了 %v，超出预算", name, el)
		}
		if d.Action == ActionAllow {
			t.Errorf("%s: 畸形输入不该放行: %+v", name, d)
		}
	}
}

// Decision.ControlPlane：请求触及 Railse 自己的配置目录（~/.railse、任何 .railse 目录、守护进程目录）时为真，限时放行不覆盖它；
// 别的敏感路径（.env、ssh 密钥）不算。有没有角色都一样。
func TestControlPlaneDetection(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	ctx := Context{ProjectRoot: roleHome + "/proj", HomeDir: roleHome, ProtectedDirs: []string{roleHome + "/dd"}}
	for _, p := range []Payload{
		{Type: PayloadFileWrite, Path: roleHome + "/.railse/rules.yaml", Cwd: roleHome + "/proj"},
		{Type: PayloadFileWrite, Path: "../dd/roles.yaml", Cwd: roleHome + "/proj"},
		shellAt("cat ~/.railse/trusts.json"),
		shellAt("echo x > ../dd/rules.yaml"),
		shellAt("rm -rf " + roleHome + "/dd"),
		shellAt("cp evil.yaml /srv/other/.railse/roles.yaml"),
		shellAt(`sh -c 'cat ~/.railse/rules.yaml'`),
		shellAt(`echo "$(cat ~/.RAILSE/rules.yaml)"`),
	} {
		for _, role := range []string{"", "运维"} {
			c := ctx
			if role != "" {
				c.Role = roleCtx(t, role).Role
			}
			if d := e.Evaluate(p, c); !d.ControlPlane {
				t.Errorf("%+v（角色 %q）应算触及 Railse 的配置: %+v", p, role, d)
			}
		}
	}
	for _, p := range []Payload{
		{Type: PayloadFileWrite, Path: roleHome + "/proj/.env", Cwd: roleHome + "/proj"},
		shellAt("cat ~/.ssh/id_rsa"),
		shellAt("git push origin main"),
		shellAt("railse trust list"),
		{Type: PayloadShell, Command: "curl https://x.y", Cwd: roleHome + "/dd"}, // cwd 在守护进程目录里：命令名与 URL 不当路径
		{Type: PayloadMCP, Server: "github", Tool: "x"},
	} {
		if d := e.Evaluate(p, ctx); d.ControlPlane {
			t.Errorf("%+v 不该算触及 Railse 的配置: %+v", p, d)
		}
	}
}

// 超过 128 KiB 的 shell 命令不解析：直接定为 unknown；有角色时按“含无法检查的子命令”越界；没有角色时 ask，绝不放行。
func TestHugeCommandNotAnalyzed(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	cmd := "git status #" + strings.Repeat("x", 128<<10)
	start := time.Now()
	if d := e.Evaluate(shellAt(cmd), roleCtx(t, "")); d.Action != ActionAsk || d.Risk != RiskUnknown {
		t.Errorf("没有角色: %+v", d)
	}
	for _, role := range []string{"写作", "运维"} {
		if d := e.Evaluate(shellAt(cmd), roleCtx(t, role)); d.Action != ActionAsk || !d.BeyondRole || !strings.Contains(d.Reason, "含无法检查的子命令") {
			t.Errorf("%s: %+v", role, d)
		}
	}
	if d := e.Evaluate(shellAt("cat ~/.railse/rules.yaml #"+strings.Repeat("x", 128<<10)), roleCtx(t, "")); !d.ControlPlane {
		t.Errorf("超长命令字面提到 .railse 仍算触及配置: %+v", d)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("超长命令应几乎不花时间: %v", el)
	}
	// 上限以内照常解析
	if d := e.Evaluate(shellAt("git status #"+strings.Repeat("x", 100<<10)), roleCtx(t, "")); d.Action != ActionAllow {
		t.Errorf("上限以内照常: %+v", d)
	}
}

// 一长串不配对的 { 不能让扫描变成平方级（在上限以内的长度上）。
func TestBraceScanLinear(t *testing.T) {
	cmd := "echo " + strings.Repeat("{", 120<<10)
	start := time.Now()
	Classify(Payload{Type: PayloadShell, Command: cmd, Cwd: roleHome + "/proj"}, Context{ProjectRoot: roleHome + "/proj", HomeDir: roleHome})
	if el := time.Since(start); el > 2*time.Second {
		t.Errorf("120 KiB 的 { 用了 %v", el)
	}
}

// 配置目录保护跟着 cd 走（与定级同一套带工作目录状态的分析）；cd 到算不出的目录之后的相对路径一律算触及，不走放行。
func TestControlPlaneFollowsCd(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	ctx := Context{ProjectRoot: roleHome + "/proj", HomeDir: roleHome, ProtectedDirs: []string{roleHome + "/proj/state"}}
	for _, cmd := range []string{
		"cd sub && cp replacement.yaml ../state/roles.yaml",
		"cd sub && cat ../state/rules.yaml",
		"cd sub; echo x > ../state/trusts.json",
		"pushd sub && mv a ../state/roles.yaml",
		"cd sub && ln -sf /tmp/x ../state/roles.yaml",
		`cd "$X" && cp a roles.yaml`,
		`sh -c 'cd sub && cp a ../state/roles.yaml'`,
		`cd sub && echo "$(cat ../state/rules.yaml)"`,
	} {
		if d := e.Evaluate(Payload{Type: PayloadShell, Command: cmd, Cwd: roleHome + "/proj"}, ctx); !d.ControlPlane {
			t.Errorf("%s: 应算触及配置: %+v", cmd, d)
		}
	}
	for _, cmd := range []string{"cd sub && make build", "cd sub && cp a ../b.txt", "cd /tmp && ls"} {
		if d := e.Evaluate(Payload{Type: PayloadShell, Command: cmd, Cwd: roleHome + "/proj"}, ctx); d.ControlPlane {
			t.Errorf("%s: 不该算: %+v", cmd, d)
		}
	}
}

// 包装命令按 basename 识别，env 的选项解析完整，sudo、xargs、timeout、nohup、nice、command、exec 包一层照样看进去
// （与定级共用 stripWrappers；sudo 本身在定级里仍按 destructive）。
func TestWrappersSeenThrough(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	nested := `/usr/bin/env sh -c 'export RAILSE_ROLE=ops; gemini -p hi'`
	if r := Classify(shellAt(nested), Context{ProjectRoot: roleHome + "/proj", HomeDir: roleHome}); r == RiskRead {
		t.Errorf("/usr/bin/env sh -c … 不该定为只读")
	}
	if r := Classify(shellAt("sudo ls"), Context{ProjectRoot: roleHome + "/proj", HomeDir: roleHome}); r != RiskDestructive {
		t.Errorf("sudo 仍应 destructive: %s", r)
	}
	for _, cmd := range []string{
		nested,
		"env -i RAILSE_ROLE=x claude",
		"env -u HOME RAILSE_ROLE=x claude",
		"env --unset=HOME RAILSE_ROLE=x claude",
		"env --unset HOME RAILSE_ROLE=x claude",
		"env -S 'RAILSE_ROLE=x claude -p hi'",
		"/usr/bin/env -i RAILSE_TRUST=8h claude",
		"sudo RAILSE_ROLE=x claude",
		"sudo -u bob env RAILSE_ROLE=x claude",
		"xargs -I{} sh -c 'RAILSE_ROLE=x claude {}'",
		"timeout 5 sh -c 'RAILSE_ROLE=x gemini'",
		"timeout -s KILL 5 env RAILSE_ROLE=x claude",
		"nohup env RAILSE_ROLE=x claude",
		"nice -n 5 env RAILSE_ROLE=x claude",
		"command env RAILSE_ROLE=x claude",
		"exec env RAILSE_ROLE=x claude",
	} {
		if d := e.Evaluate(shellAt(cmd), roleCtx(t, "运维")); !d.BeyondRole || !strings.Contains(d.Reason, "试图更改") {
			t.Errorf("%s: 应看出改 Railse 设置: %+v", cmd, d)
		}
	}
	for _, cmd := range []string{
		"sudo git push origin main",
		"timeout 10 git push origin main",
		"/usr/bin/env git push origin main",
		"xargs git push origin",
		"nohup git push origin main",
		"nice -n 5 git push origin main",
		"command git push origin main",
		"env -u HOME git push origin main",
	} {
		if d := e.Evaluate(shellAt(cmd), roleCtx(t, "写作")); !d.Forbidden {
			t.Errorf("%s: 禁用应看穿包装: %+v", cmd, d)
		}
	}
}

// 包装命令的副作用：env -C、sudo -D 改了内层的工作目录，内层按改后的目录定级与查配置目录；认不出的选项整条按不透明。
// 定级取包装本身与深剥后内层两者的较高值；deny、禁用对深剥结果照样归一化（basename、git 全局选项）。
func TestWrapperEffectsAndDeepRisk(t *testing.T) {
	e := mustEngine(t, roleEngineRules)
	ctx := Context{ProjectRoot: roleHome + "/proj", HomeDir: roleHome, ProtectedDirs: []string{roleHome + "/dd"}}
	at := func(cmd string) Payload { return Payload{Type: PayloadShell, Command: cmd, Cwd: roleHome + "/proj"} }
	for _, cmd := range []string{
		"env -C " + roleHome + "/dd cp " + roleHome + "/proj/replacement.yaml roles.yaml",
		"env --chdir=" + roleHome + "/dd touch roles.yaml",
		"env -C ../dd cp replacement.yaml rules.yaml",
		"sudo -D " + roleHome + "/dd cp a roles.yaml",
		"sudo --chdir=../dd tee roles.yaml",
	} {
		d := e.Evaluate(at(cmd), ctx)
		if !d.ControlPlane || d.Action == ActionAllow {
			t.Errorf("%s: 改目录后应算触及配置、不放行: %+v", cmd, d)
		}
	}
	if r := Classify(at("env -C /tmp touch a"), ctx); r != RiskWriteOutside {
		t.Errorf("env -C /tmp 之后的写入在项目外: %s", r)
	}
	for _, cmd := range []string{"env --frobnicate touch a", "time -o out.txt ls", "sudo -R /mnt ls", "sudo -e a.txt", "nice --weird ls", "timeout --nope 5 ls"} {
		if r := Classify(at(cmd), ctx); r < RiskUnknown {
			t.Errorf("%s: 认不出或改输出、改根的选项按不透明: %s", cmd, r)
		}
		if d := e.Evaluate(at(cmd), Context{ProjectRoot: ctx.ProjectRoot, HomeDir: roleHome, Role: roleCtx(t, "写作").Role}); !d.BeyondRole {
			t.Errorf("%s: 带命令类禁用的角色应越界: %+v", cmd, d)
		}
	}
	// 定级取较高值
	for cmd, want := range map[string]Risk{
		"command git push origin main":   RiskOutward,
		"exec git push origin main":      RiskOutward,
		"xargs git push origin main":     RiskOutward,
		"timeout 5 git push origin main": RiskOutward,
		"command ls":                     RiskUnknown, // command 自身不在名单里：max(unknown, read)
		"sudo ls":                        RiskDestructive,
	} {
		if r := Classify(at(cmd), ctx); r != want {
			t.Errorf("%s: 定级 %s，想要 %s", cmd, r, want)
		}
	}
	// 禁用与 deny 对深剥结果归一化
	for _, cmd := range []string{"command /usr/bin/git -C . push origin main", "sudo /usr/bin/git --no-pager push origin main", "timeout 5 /usr/local/bin/git -c x=y push"} {
		if d := e.Evaluate(at(cmd), roleCtx(t, "写作")); !d.Forbidden {
			t.Errorf("%s: 禁用应命中: %+v", cmd, d)
		}
	}
	if d := e.Evaluate(at("command /usr/bin/npm publish"), roleCtx(t, "")); d.Action != ActionDeny {
		t.Errorf("用户 deny 规则对深剥结果归一化: %+v", d)
	}
}
