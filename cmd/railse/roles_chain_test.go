package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
		return claudePreToolUse(cwd, cmd, "bypassPermissions"), "claude"
	case "claude-pr":
		return claudePermission(cwd, cmd), "claude"
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
// CLI 自己的确认框，hub 可达时由手机定（这里手机拒了）。暂停下触及配置目录而不越界的，Claude 与 Codex 照 0.2.3 交回终端。
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
					case kind == "control" && paused:
						want = "pass" // 0.2.3：暂停即交回终端
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
