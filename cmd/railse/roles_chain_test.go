package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
	"github.com/xufei547/railse-opc/internal/proto"
)

// 完整链路的测试工具：钩子输入（各家 CLI 的真实格式）→ 适配器 → 守护进程 → 适配器输出，断言写到 stdout 的内容与退出码。

// chainInput 是 cli（claude-pre、claude-pr、codex、gemini）在 cwd 里跑 cmd 时钩子收到的输入与要用的适配器名。
func chainInput(t *testing.T, cli, cwd, cmd string) (stdin, adapterName string) {
	t.Helper()
	switch cli {
	case "claude-pre":
		var doc map[string]any
		json.Unmarshal([]byte(sampleWithCommand(t, "claude/2.1.280/PreToolUse.json", cwd, cmd)), &doc)
		doc["permission_mode"] = "bypassPermissions"
		b, _ := json.Marshal(doc)
		return string(b), "claude"
	case "claude-pr":
		return sampleWithCommand(t, "claude/2.1.280/PermissionRequest.json", cwd, cmd), "claude"
	case "codex":
		return sampleWithCommand(t, "codex/0.145.0/PermissionRequest.json", cwd, cmd), "codex"
	case "gemini":
		return sampleWithCommand(t, "gemini/0.65.0/BeforeTool.json", cwd, cmd), "gemini"
	}
	t.Fatalf("不认识的 cli %s", cli)
	return "", ""
}

// outcomeOf 把钩子的 stdout 归成 allow、deny、ask、pass（空输出，交回 CLI 自己）；认不出时原样返回。
func outcomeOf(out string) string {
	switch {
	case out == "":
		return "pass"
	case strings.Contains(out, `"permissionDecision":"ask"`):
		return "ask"
	case strings.Contains(out, `"permissionDecision":"deny"`), strings.Contains(out, `"behavior":"deny"`), strings.Contains(out, `"decision":"deny"`):
		return "deny"
	case strings.Contains(out, `"permissionDecision":"allow"`), strings.Contains(out, `"behavior":"allow"`), strings.Contains(out, `"decision":"allow"`):
		return "allow"
	}
	return out
}

func pauseAgent(t *testing.T, sock string) {
	t.Helper()
	c := hookclient.Client{SocketPath: sock, ConnectTimeout: 2 * time.Second}
	if r, err := c.ControlMsg(proto.Control{V: proto.Version, Kind: "control", Op: "pause"}); err != nil || !r.OK {
		t.Fatalf("暂停: %v %+v", err, r)
	}
}

// 越界、roles.yaml 有误、触及 Railse 配置目录，在“来源 × hub（没有 / 不可达 / 可达）× 暂停与否”上的最终输出。
// Gemini 一律 deny（它的钩子交回终端等于继续执行）；Claude PreToolUse 越界回 ask；Claude PermissionRequest 与 Codex 交回
// CLI 自己的确认框，hub 可达时由手机定（这里手机拒了）。暂停不管越界，也不管触及配置目录（配置目录不接受任何自动放行）。
func TestRolesChainRoutingTable(t *testing.T) {
	deciders := map[string]agent.Decider{
		"none":        agent.LocalDecider{},
		"unreachable": fixedDecider{Action: "passthrough", DecidedBy: "hub_unreachable", Reason: "连不上 hub，交回终端处理"},
		"reachable":   fixedDecider{Action: "deny", DecidedBy: "user", Reason: "已在手机上拒绝"},
	}
	for _, kind := range []string{"beyond", "broken", "control"} {
		for _, hub := range []string{"none", "unreachable", "reachable"} {
			for _, paused := range []bool{false, true} {
				sock, repo, cfg := startAgentWith(t, deciders[hub])
				cmd := "git push origin main"
				switch kind {
				case "beyond":
					os.WriteFile(cfg.RolesPath, []byte("version: 1\nroles:\n  - name: 窄\n    ceiling: network\n"), 0o600)
					t.Setenv("RAILSE_ROLE", "窄")
				case "broken":
					os.WriteFile(cfg.RolesPath, []byte("version: 1\nroles:\n  - name: a\n    ceiling: hgih\n"), 0o600)
					t.Setenv("RAILSE_ROLE", "")
					cmd = "touch a"
				case "control":
					t.Setenv("RAILSE_ROLE", "")
					cmd = "echo x > ../h/rules.yaml" // 守护进程目录（startAgentWith 里是 <临时目录>/h）
				}
				if paused {
					pauseAgent(t, sock)
				}
				for _, cli := range []string{"claude-pre", "claude-pr", "codex", "gemini"} {
					stdin, ad := chainInput(t, cli, repo, cmd)
					out, _, code := runHookWith(t, stdin, ad, "--socket", sock)
					got := outcomeOf(out)
					var want string
					switch {
					case cli == "gemini":
						want = "deny"
					case cli == "claude-pre":
						want = "ask"
					case hub == "reachable":
						want = "deny"
					default:
						want = "pass"
					}
					cell := fmt.Sprintf("%s hub=%s paused=%v %s", kind, hub, paused, cli)
					if code != 0 || got != want {
						t.Errorf("%s: 想要 %s，得到 code=%d %q", cell, want, code, out)
					}
					if cli == "gemini" && kind == "control" && !strings.Contains(out, "Railse") {
						t.Errorf("%s: Gemini 的拒绝要说明原因: %q", cell, out)
					}
				}
			}
		}
	}
}

// fakeReplyDaemon 在 sock 上监听，对每个连接读一行后回 reply（原样字节）。
func fakeReplyDaemon(t *testing.T, reply string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				bufio.NewReader(c).ReadBytes('\n')
				c.Write([]byte(reply))
			}()
		}
	}()
	return sock
}

// 审批请求拿不到策略结论（超长发不出去、守护进程回了读不懂的东西）：Claude PreToolUse 回 ask 并写明原因，Gemini 拒，
// Claude PermissionRequest 与 Codex 交回（它们本来就在问人）。守护进程没起、连不上 socket 的保持 0.2.3：一律交回。
func TestHookNoVerdictFailsClosed(t *testing.T) {
	sock, repo, _ := startTestAgent(t)
	huge := "git push origin main #" + strings.Repeat("x", 1<<20+100)
	want := map[string]string{"claude-pre": "ask", "gemini": "deny", "claude-pr": "pass", "codex": "pass"}
	check := func(name, sock, cmd string, want map[string]string) {
		t.Helper()
		for cli, w := range want {
			stdin, ad := chainInput(t, cli, repo, cmd)
			out, _, code := runHookWith(t, stdin, ad, "--socket", sock)
			if code != 0 || outcomeOf(out) != w {
				t.Errorf("%s %s: 想要 %s，得到 code=%d %.300q", name, cli, w, code, out)
			}
			if w != "pass" && !strings.Contains(out, "拿不到策略结论") {
				t.Errorf("%s %s: 要写明原因: %.300q", name, cli, out)
			}
		}
	}
	check("超长载荷", sock, huge, want)
	check("读不懂的答复", fakeReplyDaemon(t, "{bad json\n"), "git push origin main", want)
	check("不认识的决定", fakeReplyDaemon(t, `{"v":1,"decision":"maybe"}`+"\n"), "git push origin main", want)
	// 守护进程没起：0.2.3 的现状，一律交回、什么都不输出
	none := filepath.Join(t.TempDir(), "none.sock")
	check("守护进程没起", none, huge, map[string]string{"claude-pre": "pass", "gemini": "pass", "claude-pr": "pass", "codex": "pass"})
	// 钩子输入超过 4 MiB（读不全、解析不了）：Claude 的 PreToolUse 与 Gemini 照样不放过
	big := "git push origin main #" + strings.Repeat("y", 5<<20)
	for cli, w := range map[string]string{"claude-pre": "ask", "gemini": "deny"} {
		stdin, ad := chainInput(t, cli, repo, big)
		out, _, code := runHookWith(t, stdin, ad, "--socket", sock)
		if code != 0 || outcomeOf(out) != w {
			t.Errorf("超过 4 MiB 的输入 %s: 想要 %s，得到 code=%d %.300q", cli, w, code, out)
		}
	}
}

// 放行期间经 cd 之后改守护进程目录：完整链路上不被放行放过。
func TestChainTrustDoesNotCoverCdIntoControlPlane(t *testing.T) {
	sock, repo, _ := startTestAgent(t)
	t.Setenv("RAILSE_ROLE", "")
	c := hookclient.Client{SocketPath: sock, ConnectTimeout: 2 * time.Second}
	if r, err := c.ControlMsg(proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: agent.CleanCwd(repo), Until: time.Now().Add(time.Hour)}}); err != nil || !r.OK {
		t.Fatalf("建放行: %v %+v", err, r)
	}
	// 对照：同一放行下普通的联网命令照样放行
	stdin, ad := chainInput(t, "claude-pr", repo, "curl https://example.com")
	if out, _, _ := runHookWith(t, stdin, ad, "--socket", sock); outcomeOf(out) != "allow" {
		t.Fatalf("对照应放行: %q", out)
	}
	for cli, want := range map[string]string{"claude-pre": "ask", "claude-pr": "pass", "codex": "pass", "gemini": "deny"} {
		for _, cmd := range []string{"cd sub && cp replacement.yaml ../../h/roles.yaml", `cd "$X" && cp a roles.yaml`} {
			stdin, ad := chainInput(t, cli, repo, cmd)
			out, _, code := runHookWith(t, stdin, ad, "--socket", sock)
			if code != 0 || outcomeOf(out) != want {
				t.Errorf("%s %s: 想要 %s，得到 code=%d %q", cli, cmd, want, code, out)
			}
		}
	}
}

// claudeTool 是 Claude 在 cwd 里调用 tool（tool_input 为 input）时某个钩子事件的输入，权限模式 bypassPermissions。
func claudeTool(t *testing.T, event, cwd, tool, input string) string {
	t.Helper()
	var doc map[string]any
	json.Unmarshal([]byte(sampleWithCwd(t, "claude/2.1.280/PreToolUse.json", cwd)), &doc)
	doc["hook_event_name"], doc["tool_name"], doc["tool_input"], doc["permission_mode"] = event, tool, json.RawMessage(input), "bypassPermissions"
	b, _ := json.Marshal(doc)
	return string(b)
}

// MCP 工具与 WebSearch 的 PreToolUse 进入角色判定；没有角色时与 0.2.3 等价（定不了的交回 Claude 自己）。
func TestChainClaudeMCPAndWebSearchPreToolUse(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	mcp := claudeTool(t, "PreToolUse", repo, "mcp__github__create_issue", `{"title":"x"}`)
	search := claudeTool(t, "PreToolUse", repo, "WebSearch", `{"query":"x"}`)
	read := claudeTool(t, "PreToolUse", repo, "Read", `{"file_path":"a"}`)
	t.Setenv("RAILSE_ROLE", "")
	for name, stdin := range map[string]string{"MCP": mcp, "WebSearch": search, "Read": read} {
		if out, _, code := runHookWith(t, stdin, "claude", "--socket", sock); code != 0 || out != "" {
			t.Errorf("没有角色时 %s 应交回 Claude 自己（同 0.2.3）: code=%d %q", name, code, out)
		}
	}
	os.WriteFile(cfg.RolesPath, []byte("version: 1\nroles:\n  - name: 写作\n    ceiling: write_in_project\n    forbid:\n      - {payload: mcp}\n"), 0o600)
	for name, stdin := range map[string]string{"MCP": mcp, "WebSearch": search} {
		if out, _, code := runHookWith(t, stdin, "claude", "--socket", sock); code != 0 || out != "" {
			t.Errorf("未分配的会话 %s 照旧: code=%d %q", name, code, out)
		}
	}
	t.Setenv("RAILSE_ROLE", "写作")
	if out, _, code := runHookWith(t, mcp, "claude", "--socket", sock); code != 0 || outcomeOf(out) != "deny" || !strings.Contains(out, "payload mcp") {
		t.Errorf("禁用 MCP 的角色: code=%d %q", code, out)
	}
	if out, _, code := runHookWith(t, search, "claude", "--socket", sock); code != 0 || outcomeOf(out) != "ask" || !strings.Contains(out, "超出角色") {
		t.Errorf("WebSearch 超出上限: code=%d %q", code, out)
	}
	if out, _, code := runHookWith(t, read, "claude", "--socket", sock); code != 0 || out != "" {
		t.Errorf("Read 不进判定: code=%d %q", code, out)
	}
}

// 真实取到的祖先链：从直接父进程起，带启动时间与名字（darwin、linux）。
func TestReadAncestorsReal(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("这个平台取不到祖先链")
	}
	chain := readAncestors()
	if len(chain) == 0 || chain[0].PID != os.Getppid() || chain[0].Start == "" || chain[0].Name == "" {
		t.Fatalf("祖先链: %+v", chain)
	}
	again := readAncestors()
	if again[0] != chain[0] {
		t.Fatalf("同一进程两次取到的身份应相同: %+v %+v", chain[0], again[0])
	}
}

// 完整链路：上层会话（RAILSE_ROLE=写作）里起的嵌套 CLI 用 RAILSE_ROLE=运维 换角色，仍受写作的禁用约束，钩子输出拒绝。
func TestChainNestedCLIInheritsRole(t *testing.T) {
	agent.SetPeerAncestryForTest(t, nil) // 测试进程里跑钩子：用注入的祖先链（守护进程取到的对端是测试进程自己）
	sock, repo, cfg := startTestAgent(t)
	os.WriteFile(cfg.RolesPath, []byte("version: 1\nroles:\n  - name: 写作\n    forbid:\n      - {command_prefix: \"git push\"}\n  - name: 运维\n    ceiling: destructive\n"), 0o600)
	outer := []proto.Proc{{PID: 101, Start: "a", Name: "sh"}, {PID: 11, Start: "b", Name: "claude"}, {PID: 5, Start: "c", Name: "zsh"}}
	nested := append([]proto.Proc{{PID: 202, Start: "d", Name: "sh"}, {PID: 22, Start: "e", Name: "claude"}, {PID: 150, Start: "f", Name: "bash"}}, outer[1:]...)
	old := readAncestors
	t.Cleanup(func() { readAncestors = old })
	run := func(chain []proto.Proc, role, session, cmd string) string {
		readAncestors = func() []proto.Proc { return chain }
		t.Setenv("RAILSE_ROLE", role)
		var doc map[string]any
		json.Unmarshal([]byte(claudeTool(t, "PreToolUse", repo, "Bash", `{"command":"`+cmd+`"}`)), &doc)
		doc["session_id"] = session
		b, _ := json.Marshal(doc)
		out, _, _ := runHookWith(t, string(b), "claude", "--socket", sock)
		return out
	}
	run(outer, "写作", "outer", "git status")
	if out := run(nested, "运维", "nested", "git push origin main"); outcomeOf(out) != "deny" || !strings.Contains(out, "继承自上层会话的角色「写作」") {
		t.Fatalf("嵌套 CLI 换角色仍受上层约束: %q", out)
	}
	// 对照：不在上层会话之下的同角色会话不受影响（运维上限 destructive，git push 回 ask 交终端确认）
	if out := run([]proto.Proc{{PID: 303, Start: "g", Name: "sh"}, {PID: 33, Start: "h", Name: "claude"}, {PID: 5, Start: "c", Name: "zsh"}}, "运维", "other", "git push origin main"); outcomeOf(out) == "deny" {
		t.Fatalf("不在上层会话之下不该继承: %q", out)
	}
}

// writeRules 换掉守护进程的规则（规则文件 + reload）。
func writeRules(t *testing.T, sock string, cfg agent.Config, yaml string) {
	t.Helper()
	os.WriteFile(cfg.RulesPath, []byte(yaml), 0o600)
	c := hookclient.Client{SocketPath: sock, ConnectTimeout: 2 * time.Second}
	if r, err := c.Control("reload"); err != nil || !r.OK {
		t.Fatalf("reload: %v %+v", err, r)
	}
}

// 配置目录不接受任何自动放行：哪怕用户手写了显式 risk: destructive 的 allow 规则，暂停与否、有无角色都一样，一律进人工决定。
func TestChainControlPlaneNeverAutoAllowed(t *testing.T) {
	for _, paused := range []bool{false, true} {
		sock, repo, cfg := startTestAgent(t)
		t.Setenv("RAILSE_ROLE", "")
		writeRules(t, sock, cfg, "version: 1\nrules:\n  - id: user-cp\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: \"cp\", risk: destructive}\n    action: allow\n")
		if paused {
			pauseAgent(t, sock)
		}
		// 对照：同一条规则放行项目外的普通 cp（敏感路径之外的 destructive 不存在，这里用 .env 当 destructive 的对照）
		if !paused {
			stdin, ad := chainInput(t, "claude-pr", repo, "cp a .env")
			if out, _, _ := runHookWith(t, stdin, ad, "--socket", sock); outcomeOf(out) != "allow" {
				t.Fatalf("对照：用户的 destructive allow 规则照常放行别的敏感路径: %q", out)
			}
		}
		for cli, want := range map[string]string{"claude-pre": "ask", "claude-pr": "pass", "codex": "pass", "gemini": "deny"} {
			stdin, ad := chainInput(t, cli, repo, "cp replacement.yaml ../h/roles.yaml")
			out, _, code := runHookWith(t, stdin, ad, "--socket", sock)
			if code != 0 || outcomeOf(out) != want {
				t.Errorf("paused=%v %s: 想要 %s，得到 code=%d %q", paused, cli, want, code, out)
			}
		}
	}
}

// 包装改目录后覆盖配置、包装藏起推送：完整链路上不被内置规则或限时放行放过。
func TestChainWrapperEffects(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	os.WriteFile(cfg.RolesPath, []byte("version: 1\nroles:\n  - name: 开发\n    ceiling: network\n    forbid:\n      - {command_prefix: \"git push\"}\n"), 0o600)
	c := hookclient.Client{SocketPath: sock, ConnectTimeout: 2 * time.Second}
	if r, err := c.ControlMsg(proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: agent.CleanCwd(repo), Until: time.Now().Add(time.Hour)}}); err != nil || !r.OK {
		t.Fatalf("建放行: %v %+v", err, r)
	}
	t.Setenv("RAILSE_ROLE", "")
	// 没有角色：env -C 到守护进程目录里覆盖 roles.yaml
	for cli, want := range map[string]string{"claude-pre": "ask", "claude-pr": "pass", "codex": "pass", "gemini": "deny"} {
		stdin, ad := chainInput(t, cli, repo, "env -C ../h cp ../repo/replacement.yaml roles.yaml")
		if out, _, code := runHookWith(t, stdin, ad, "--socket", sock); code != 0 || outcomeOf(out) != want {
			t.Errorf("env -C %s: 想要 %s，得到 code=%d %q", cli, want, code, out)
		}
	}
	// 有角色：command 包着的 git push（深剥后归一化）照样命中禁用；command git push 的定级是 outward，超出 network 上限、不被放行放过
	t.Setenv("RAILSE_ROLE", "开发")
	for _, cmd := range []string{"command /usr/bin/git -C . push origin main", "timeout 5 git push origin main"} {
		stdin, ad := chainInput(t, "claude-pre", repo, cmd)
		if out, _, _ := runHookWith(t, stdin, ad, "--socket", sock); outcomeOf(out) != "deny" {
			t.Errorf("%s: 应按禁用拒: %q", cmd, out)
		}
	}
	os.WriteFile(cfg.RolesPath, []byte("version: 1\nroles:\n  - name: 开发\n    ceiling: network\n"), 0o600)
	stdin, ad := chainInput(t, "claude-pr", repo, "command git push origin main")
	if out, _, _ := runHookWith(t, stdin, ad, "--socket", sock); outcomeOf(out) == "allow" {
		t.Errorf("command git push 应越界、不被放行放过: %q", out)
	}
}

// 没有生效角色时（没有角色文件，或会话未分配且没有继承角色），MCP 与 WebSearch 的 PreToolUse 一律忽略：
// 与 0.2.3 完全相同（钩子不输出、守护进程不写审计），claude_undecided: phone 与用户 deny 规则都不起作用。
func TestChainNewPreToolUseToolsIgnoredWithoutRole(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	os.WriteFile(cfg.SettingsPath, []byte("claude_undecided: phone\n"), 0o600)
	writeRules(t, sock, cfg, "version: 1\nrules:\n  - id: user-deny-mcp\n    source: user\n    scope: global\n    match: {payload: mcp}\n    action: deny\n  - id: user-deny-search\n    source: user\n    scope: global\n    match: {payload: network}\n    action: deny\n")
	mcp := claudeTool(t, "PreToolUse", repo, "mcp__github__create_issue", `{"title":"x"}`)
	search := claudeTool(t, "PreToolUse", repo, "WebSearch", `{"query":"x"}`)
	t.Setenv("RAILSE_ROLE", "")
	check := func(label string) {
		t.Helper()
		before, _ := os.ReadFile(cfg.AuditPath)
		for name, stdin := range map[string]string{"MCP": mcp, "WebSearch": search} {
			if out, _, code := runHookWith(t, stdin, "claude", "--socket", sock); code != 0 || out != "" {
				t.Errorf("%s %s: 应忽略（同 0.2.3）: code=%d %q", label, name, code, out)
			}
		}
		if after, _ := os.ReadFile(cfg.AuditPath); len(after) != len(before) {
			t.Errorf("%s: 忽略的请求不该写审计: %s", label, after[len(before):])
		}
	}
	check("没有角色文件")
	os.WriteFile(cfg.RolesPath, []byte("version: 1\nroles:\n  - name: 写作\n    ceiling: read\n"), 0o600)
	check("会话未分配")
	// 对照：同样的配置下 PermissionRequest 照旧受 deny 规则管（0.2.3 也是）
	pr := claudeTool(t, "PermissionRequest", repo, "mcp__github__create_issue", `{"title":"x"}`)
	if out, _, _ := runHookWith(t, pr, "claude", "--socket", sock); outcomeOf(out) != "deny" {
		t.Errorf("PermissionRequest 照旧: %q", out)
	}
	// 有生效角色时进判定
	t.Setenv("RAILSE_ROLE", "写作")
	if out, _, _ := runHookWith(t, mcp, "claude", "--socket", sock); outcomeOf(out) != "deny" {
		t.Errorf("有角色时 MCP 进判定（用户 deny 规则）: %q", out)
	}
}
