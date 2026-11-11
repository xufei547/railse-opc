package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/proto"
)

// 差分测试：没有 roles.yaml 时，守护进程对审批的答复与 0.3 之前（main 上 fc14b08 的代码）逐条相同，只有一处有意的收紧：
// 守护进程目录不在 ~/.railse 时，里面的读写按 destructive（见 daemonDirCommand）。
// 基准 testdata/nofile_responses_main.jsonl 用 main 上的代码跑这同一个文件生成：
//
//	RAILSE_WRITE_DIFF_GOLDEN=$PWD/internal/agent/testdata/nofile_responses_main.jsonl go test ./internal/agent -run TestNoRolesServerMatchesMain
//
// 语料覆盖各风险档 × 来源（PreToolUse、PermissionRequest、Codex）× 暂停与否 × 有无限时放行（无、项目、会话）。
// 比较答复的 decision、risk、rule_id、reason、decided_by、trigger；request_id 与放行 id 是随机的，放行 id 换成 *。

var diffCommands = []string{
	"git status", "ls -la", "go test ./...", "touch a.txt", "echo hi > out.txt", "rm -rf build", "rm -rf /",
	"curl https://example.com", "npm install left-pad", "git push origin main", "git push --force origin main",
	"npm publish", "cat ~/.ssh/id_rsa", "echo x > ~/.railse/rules.yaml", "some-unknown-tool --flag", "sudo rm x",
	"bash -c 'git push origin main'", `echo "$(git push origin main)"`, "cp a /etc/b", "mv a ../outside",
	// 守护进程目录（测试里是 ../h，不在 ~/.railse）：0.3 有意收紧，这几条与 main 不同，见 daemonDirCommand
	"cat ../h/rules.yaml", "echo x > ../h/roles.yaml", "cp evil.yaml ../h/rules.yaml", "rm ../h/trusts.json",
}

// daemonDirCommand 报告命令是否碰守护进程目录：没有 roles.yaml 时唯一有意与 main 不同的地方（守护进程目录不在 ~/.railse 时，
// 里面的读写按 destructive，见 policy.Context.ProtectedDirs）。
func daemonDirCommand(cmd string) bool { return strings.Contains(cmd, "../h/") }

func TestNoRolesServerMatchesMain(t *testing.T) {
	var got []string
	dec := deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
		return Outcome{Action: "deny", DecidedBy: "user", Reason: "已在手机上拒绝"}, nil
	})
	for _, trust := range []string{"none", "project", "session"} {
		for _, paused := range []bool{false, true} {
			env := startServer(t, dec, nil)
			switch trust {
			case "project":
				roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
					Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(time.Hour)}})
			case "session":
				roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
					Trust: &proto.Trust{Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s1", Until: time.Now().Add(time.Hour)}})
			}
			if paused {
				roundTrip(t, env.cfg.SocketPath, control("pause"))
			}
			for _, src := range []string{"pretooluse", "permissionrequest", "codex"} {
				for i, cmd := range diffCommands {
					var r proto.Request
					switch src {
					case "pretooluse":
						r = preToolUse(env.repo, cmd, "auto", "toolu_"+string(rune('a'+i)))
					case "permissionrequest":
						r = permission(env.repo, cmd)
						r.Source = proto.SourcePermissionRequest
					default:
						r = permission(env.repo, cmd)
						r.Session.Tool = "codex"
					}
					var resp proto.Response
					if err := json.Unmarshal(roundTrip(t, env.cfg.SocketPath, r), &resp); err != nil {
						t.Fatal(err)
					}
					if strings.HasPrefix(resp.DecidedBy, "trust:") {
						resp.DecidedBy = "trust:*"
					}
					resp.RequestID = ""
					b, _ := json.Marshal(resp)
					got = append(got, trust+"\t"+map[bool]string{false: "run", true: "paused"}[paused]+"\t"+src+"\t"+cmd+"\t"+string(b))
				}
			}
		}
	}
	if path := os.Getenv("RAILSE_WRITE_DIFF_GOLDEN"); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile("testdata/nofile_responses_main.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(want) != len(got) {
		t.Fatalf("条数不同：基准 %d，现在 %d", len(want), len(got))
	}
	diff := 0
	for i := range want {
		if cmd := strings.Split(got[i], "\t")[3]; daemonDirCommand(cmd) {
			// 有意的收紧：0.3 起一律 destructive，规则不放行。限时放行期间照样放（0.2.3 的放行语义：期间完全不把关，
			// main 上 ~/.railse 里的 destructive 也是这样），这里不改
			var resp proto.Response
			json.Unmarshal([]byte(strings.Split(got[i], "\t")[4]), &resp)
			if resp.Risk != "destructive" || resp.Decision == "allow" && resp.DecidedBy != "trust:*" {
				t.Errorf("第 %d 条（守护进程目录）应按 destructive: %s", i+1, got[i])
			}
			continue
		}
		if want[i] != got[i] {
			if diff < 10 {
				t.Errorf("第 %d 条不同:\n main: %s\n 现在: %s", i+1, want[i], got[i])
			}
			diff++
		}
	}
	if diff > 0 {
		t.Fatalf("共 %d 条与 main 不同", diff)
	}
}
