package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// realRepo 是 env.repo 解析符号链接后的路径（macOS 的临时目录在 /var → /private/var 之下）：守护进程按它算项目根。
func realRepo(t *testing.T, env *testEnv) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(env.repo)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// rolesFor 写一份 roles.yaml：写作的地盘是 env.repo（上限项目内写、禁用 git push），开发不写 dirs（上限 network），
// 运维上限 destructive。extra 追加在文件末尾（更多角色）。
func rolesFor(t *testing.T, env *testEnv, extra string) {
	t.Helper()
	text := `version: 1
roles:
  - name: 写作
    dirs: ["` + realRepo(t, env) + `"]
    ceiling: write_in_project
    forbid:
      - {command_prefix: "git push"}
  - name: 开发
    ceiling: network
  - name: 运维
    ceiling: destructive
` + extra
	writeRoles(t, env.cfg.RolesPath, text)
}

func withRole(r proto.Request, role string) proto.Request {
	r.Session.Role = role
	return r
}

func lastAudit(t *testing.T, env *testEnv) AuditEntry {
	t.Helper()
	all := readAudit(t, env.cfg.AuditPath)
	if len(all) == 0 {
		t.Fatal("没有审计")
	}
	return all[len(all)-1]
}

func send(t *testing.T, env *testEnv, msg any) proto.Response {
	t.Helper()
	var resp proto.Response
	if err := json.Unmarshal(roundTrip(t, env.cfg.SocketPath, msg), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestRoleBindingEnvDirUnknown(t *testing.T) {
	logs := &logBuf{}
	env := startServerLog(t, LocalDecider{}, nil, logs)
	rolesFor(t, env, "")
	// 按目录：env.repo 在写作的地盘里
	send(t, env, permission(env.repo, "git status"))
	if e := lastAudit(t, env); e.Session.Role != "写作" || e.Role != "写作" {
		t.Fatalf("按目录应绑到写作: %+v", e)
	}
	// 各步用不同的会话：同一会话会钉住角色（见 TestRolePinnedPerSession）
	inSession := func(r proto.Request, id string) proto.Request {
		r.Session.CLISessionID = id
		return r
	}
	// RAILSE_ROLE 优先
	send(t, env, inSession(withRole(permission(env.repo, "git status"), "运维"), "s2"))
	if e := lastAudit(t, env); e.Session.Role != "运维" || e.Role != "运维" {
		t.Fatalf("RAILSE_ROLE 应优先: %+v", e)
	}
	// 不存在的名字：按目录走，日志里每个会话只记一次
	for range 2 {
		send(t, env, inSession(withRole(permission(env.repo, "git status"), "不存在"), "s3"))
	}
	if e := lastAudit(t, env); e.Session.Role != "写作" {
		t.Fatalf("不存在的名字应按目录走: %+v", e)
	}
	if n := strings.Count(logs.String(), "不存在"); n != 1 {
		t.Fatalf("不存在的角色名应只记一次日志，得到 %d 次:\n%s", n, logs.String())
	}
	// 地盘外、没有 RAILSE_ROLE：未分配
	other := filepath.Join(filepath.Dir(env.repo), "other")
	os.MkdirAll(filepath.Join(other, ".git"), 0o755)
	send(t, env, inSession(permission(other, "git status"), "s4"))
	if e := lastAudit(t, env); e.Session.Role != "" || e.Role != "" {
		t.Fatalf("地盘外应未分配: %+v", e)
	}
}

func TestRoleOverlapTakesFirst(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	rolesFor(t, env, "  - name: 重叠\n    dirs: [\""+realRepo(t, env)+"\"]\n")
	send(t, env, permission(env.repo, "git status"))
	if e := lastAudit(t, env); e.Session.Role != "写作" {
		t.Fatalf("重叠时应取文件里靠前的: %+v", e)
	}
}

func TestRoleForbidDenies(t *testing.T) {
	calls := 0
	dec := deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
		calls++
		return Outcome{Action: "allow", DecidedBy: "user"}, nil
	})
	env := startServer(t, dec, nil)
	rolesFor(t, env, "")
	// 项目放行也挡不住禁用
	roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(time.Hour)}})
	for _, src := range []string{proto.SourcePreToolUse, proto.SourcePermissionRequest} {
		r := permission(env.repo, "git push origin main")
		r.Source = src
		resp := send(t, env, r)
		if resp.Decision != "deny" || resp.DecidedBy != "role:写作" || !strings.Contains(resp.Reason, "角色「写作」禁用") {
			t.Fatalf("%s: 禁用应直接拒: %+v", src, resp)
		}
		e := lastAudit(t, env)
		if e.State != "auto_denied" || e.DecidedBy != "role:写作" || e.Role != "写作" || e.BeyondRole {
			t.Fatalf("%s: 审计: %+v", src, e)
		}
	}
	if calls != 0 {
		t.Fatalf("禁用不该送手机: %d", calls)
	}
}

// 越界：跳过限时放行（包括 RAILSE_TRUST）；PreToolUse 一律回 ask，PermissionRequest 送 Decider 并带 BeyondRole。
func TestBeyondRoleSkipsTrust(t *testing.T) {
	var mu sync.Mutex
	var got []PendingRequest
	dec := deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
		mu.Lock()
		got = append(got, pr)
		mu.Unlock()
		return Outcome{Action: "deny", DecidedBy: "user"}, nil
	})
	env := startServer(t, dec, nil)
	rolesFor(t, env, "")
	roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(time.Hour)}})

	// 开发的上限是 network：git push（outward）越界，PreToolUse 回 ask，不放行
	r := withRole(preToolUse(env.repo, "git push origin main", "auto", "toolu_1"), "开发")
	r.TrustFor = "2h"
	resp := send(t, env, r)
	if resp.Decision != "ask" || strings.HasPrefix(resp.DecidedBy, "trust:") || !strings.Contains(resp.Reason, "超出角色「开发」的边界") {
		t.Fatalf("越界的 PreToolUse 应回 ask: %+v", resp)
	}
	if e := lastAudit(t, env); !e.BeyondRole || e.Role != "开发" || e.State != "asked" {
		t.Fatalf("越界审计: %+v", e)
	}
	// RAILSE_TRUST 没有因为越界的那条建会话放行
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_list"}), &reply)
	if len(reply.Trusts) != 1 {
		t.Fatalf("越界请求不该按 RAILSE_TRUST 建放行: %+v", reply.Trusts)
	}
	// PermissionRequest 越界：不放行，送 Decider，带 BeyondRole
	pr := withRole(permission(env.repo, "git push origin main"), "开发")
	pr.Source = proto.SourcePermissionRequest
	if resp := send(t, env, pr); resp.Decision != "deny" {
		t.Fatalf("越界的 PermissionRequest 应由人定: %+v", resp)
	}
	mu.Lock()
	if len(got) != 1 || !got[0].BeyondRole || got[0].Req.Session.Role != "开发" {
		t.Fatalf("送 Decider 的请求应带 BeyondRole 与角色: %+v", got)
	}
	mu.Unlock()
	// 上限之内的照旧走放行：curl 是 network
	pr = withRole(permission(env.repo, "curl https://example.com"), "开发")
	pr.Source = proto.SourcePermissionRequest
	if resp := send(t, env, pr); resp.Decision != "allow" || !strings.HasPrefix(resp.DecidedBy, "trust:") {
		t.Fatalf("上限之内应照旧放行: %+v", resp)
	}
}

// 越界的中间几档（这里是项目内写入超出 read 上限）在 PreToolUse 上也回 ask，不交给 Claude 自己的分类器（它可能自动放行）。
func TestBeyondRolePreToolUseMidTierAsks(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	rolesFor(t, env, "  - name: 只读\n    ceiling: read\n")
	resp := send(t, env, withRole(preToolUse(env.repo, "touch a", "acceptEdits", "toolu_m"), "只读"))
	if resp.Decision != "ask" || resp.Risk != "write_in_project" {
		t.Fatalf("越界的项目内写入应回 ask: %+v", resp)
	}
	// 只读照旧按内置规则放行
	resp = send(t, env, preToolUse(env.repo, "ls", "acceptEdits", "toolu_n"))
	if resp.Decision != "allow" {
		t.Fatalf("只读照旧放行: %+v", resp)
	}
}

// setTrustClock 把放行表的“此刻”换成 *now（测试推进时钟用）。
func setTrustClock(env *testEnv, now *time.Time) {
	env.srv.trusts.mu.Lock()
	env.srv.trusts.now = func() time.Time { return *now }
	env.srv.trusts.mu.Unlock()
}

// trust_max：设了时长的角色，放行只在建立后 trust_max 之内有效（按 min(Until, CreatedAt + trust_max)，到点即失效）；none 一律不放行；
// 会话与项目两种范围一样。
func TestRoleTrustMax(t *testing.T) {
	dec := deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
		return Outcome{Action: "deny", DecidedBy: "user"}, nil
	})
	for _, scope := range []string{proto.TrustScopeProject, proto.TrustScopeSession} {
		env := startServer(t, dec, nil)
		rolesFor(t, env, "  - name: 短\n    trust_max: 2h\n  - name: 不放\n    trust_max: none\n")
		now := time.Now()
		setTrustClock(env, &now)
		tr := proto.Trust{Scope: scope, Until: now.Add(20 * time.Hour)}
		if scope == proto.TrustScopeProject {
			tr.Project = env.repo
		} else {
			tr.Tool, tr.CLISession = "claude", "s1"
		}
		var reply proto.ControlReply
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set", Trust: &tr}), &reply)
		if !reply.OK {
			t.Fatalf("建放行: %+v", reply)
		}
		req := func(role string) proto.Response {
			r := withRole(permission(env.repo, "curl https://example.com"), role)
			r.Source = proto.SourcePermissionRequest
			return send(t, env, r)
		}
		if resp := req("短"); resp.Decision != "allow" {
			t.Fatalf("%s: 刚建的放行应有效: %+v", scope, resp)
		}
		if resp := req("不放"); resp.Decision == "allow" {
			t.Fatalf("%s: trust_max none 不走放行: %+v", scope, resp)
		}
		now = now.Add(2*time.Hour - time.Second)
		if resp := req("短"); resp.Decision != "allow" {
			t.Fatalf("%s: 边界前一秒应有效: %+v", scope, resp)
		}
		now = now.Add(time.Second) // 恰好 CreatedAt + 2h
		if resp := req("短"); resp.Decision == "allow" {
			t.Fatalf("%s: 恰好到 trust_max 应失效: %+v", scope, resp)
		}
		if resp := req("运维"); resp.Decision != "allow" {
			t.Fatalf("%s: 没设 trust_max 的角色照旧放行: %+v", scope, resp)
		}
	}
}

// 本机建放行时 CreatedAt 一律由守护进程填此刻：传一个未来的建立时间撑大 trust_max 不成立。
func TestTrustCreatedAtSetByDaemon(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	rolesFor(t, env, "  - name: 短\n    trust_max: 2h\n")
	future := time.Now().Add(10 * time.Hour)
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(20 * time.Hour), CreatedAt: future}}), &reply)
	if !reply.OK || reply.Trusts[0].CreatedAt.After(time.Now()) {
		t.Fatalf("CreatedAt 应是守护进程的此刻: %+v", reply)
	}
	now := time.Now().Add(3 * time.Hour)
	setTrustClock(env, &now)
	r := withRole(permission(env.repo, "curl https://example.com"), "短")
	r.Source = proto.SourcePermissionRequest
	if resp := send(t, env, r); resp.Decision == "allow" {
		t.Fatalf("3 小时后对 trust_max 2h 的角色应失效: %+v", resp)
	}
}

func TestBrokenRolesFailClosedAndRecover(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	writeRoles(t, env.cfg.RolesPath, "version: 1\nroles:\n  - name: a\n    ceiling: hgih\n")
	if resp := send(t, env, permission(env.repo, "git status")); resp.Decision != "allow" {
		t.Fatalf("坏文件时只读照旧放行: %+v", resp)
	}
	resp := send(t, env, preToolUse(env.repo, "touch a", "acceptEdits", "toolu_b"))
	if resp.Decision != "ask" || !strings.Contains(resp.Reason, "roles.yaml 有误") {
		t.Fatalf("坏文件时写入应问人: %+v", resp)
	}
	if e := lastAudit(t, env); !e.BeyondRole || e.Role != "" || !strings.Contains(e.Reason, "hgih") {
		t.Fatalf("坏文件审计: %+v", e)
	}
	// 修好后恢复（mtime 变了就重读）
	writeRoles(t, env.cfg.RolesPath, "version: 1\nroles:\n  - name: a\n    ceiling: network\n")
	if resp := send(t, env, preToolUse(env.repo, "touch a", "acceptEdits", "toolu_c")); resp.Decision != "allow" {
		t.Fatalf("修好后应恢复: %+v", resp)
	}
}

func TestRolesReloadOnChange(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	rolesFor(t, env, "")
	if resp := send(t, env, preToolUse(env.repo, "curl https://example.com", "auto", "t1")); resp.Decision != "ask" {
		t.Fatalf("写作的上限是项目内写，联网应越界: %+v", resp)
	}
	text := strings.Replace(mustRead(t, env.cfg.RolesPath), "ceiling: write_in_project", "ceiling: network", 1)
	writeRoles(t, env.cfg.RolesPath, text)
	if resp := send(t, env, preToolUse(env.repo, "curl https://example.com", "auto", "t2")); resp.Decision != "passthrough" {
		t.Fatalf("改成 network 后联网应回到常规流程（交给 Claude）: %+v", resp)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestAskAndEventsCarryRole(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	rolesFor(t, env, "")
	send(t, env, askRequest(env.repo))
	if e := lastAudit(t, env); e.Kind != "ask" || e.Session.Role != "写作" {
		t.Fatalf("答题应带角色: %+v", e)
	}
	roundTrip(t, env.cfg.SocketPath, proto.Event{V: proto.Version, Kind: "done", Session: proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: env.repo, Role: "运维"}})
	if e := lastAudit(t, env); e.Kind != "done" || e.Session.Role != "运维" {
		t.Fatalf("事件应带生效的角色: %+v", e)
	}
}

// 没有 roles.yaml：与 0.2.3 一样，审计里连 role 键都没有。
func TestNoRolesFileUnchanged(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	send(t, env, withRole(permission(env.repo, "git push origin main"), "运维"))
	b := mustRead(t, env.cfg.AuditPath)
	if strings.Contains(b, `"role"`) || strings.Contains(b, "beyond_role") {
		t.Fatalf("没有角色文件时审计不该带角色: %s", b)
	}
}

func TestRoleControls(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("role_list")), &reply)
	if !reply.OK || reply.Roles == nil || reply.Roles.Exists || reply.Roles.Path != env.cfg.RolesPath {
		t.Fatalf("没有文件时: %+v", reply)
	}
	rolesFor(t, env, "")
	reply = proto.ControlReply{}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("role_list")), &reply)
	if !reply.OK || len(reply.Roles.Roles) != 3 || reply.Roles.Roles[0].Name != "写作" || reply.Roles.Roles[0].ForbidCount != 1 {
		t.Fatalf("role_list: %+v", reply.Roles)
	}
	c := control("role_which")
	c.Cwd = env.repo
	reply = proto.ControlReply{}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, c), &reply)
	if w := reply.Roles.Which; w == nil || w.Role != "写作" || w.Kind != RoleByDir || w.ProjectRoot != realRepo(t, env) {
		t.Fatalf("role_which 按目录: %+v", reply.Roles)
	}
	c.Role = "开发"
	reply = proto.ControlReply{}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, c), &reply)
	if w := reply.Roles.Which; w == nil || w.Role != "开发" || w.Kind != RoleByEnv || w.OutsideDirs {
		t.Fatalf("role_which 按环境变量: %+v", reply.Roles)
	}
	writeRoles(t, env.cfg.RolesPath, "version: 9\n")
	reply = proto.ControlReply{}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("role_list")), &reply)
	if !reply.OK || reply.Roles.Error == "" {
		t.Fatalf("坏文件时 role_list 应报错误: %+v", reply.Roles)
	}
}

// 暂停不覆盖越界（含坏文件 fail closed）：PreToolUse 照旧回 ask，PermissionRequest 走人工决定（送 Decider，带 BeyondRole）；
// forbid 在暂停下仍拒。
func TestPausedDoesNotOverrideBeyondRole(t *testing.T) {
	for _, broken := range []bool{false, true} {
		var mu sync.Mutex
		var got []PendingRequest
		dec := deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
			mu.Lock()
			got = append(got, pr)
			mu.Unlock()
			return Outcome{Action: "deny", DecidedBy: "user"}, nil
		})
		env := startServer(t, dec, nil)
		role, cmd := "开发", "git push origin main"
		if broken {
			writeRoles(t, env.cfg.RolesPath, "version: 1\nroles:\n  - name: a\n    ceiling: hgih\n")
			role, cmd = "", "touch a"
		} else {
			rolesFor(t, env, "")
		}
		roundTrip(t, env.cfg.SocketPath, control("pause"))
		resp := send(t, env, withRole(preToolUse(env.repo, cmd, "bypassPermissions", "toolu_p"), role))
		if resp.Decision != "ask" {
			t.Fatalf("broken=%v 暂停时越界的 PreToolUse 应回 ask: %+v", broken, resp)
		}
		pr := withRole(permission(env.repo, cmd), role)
		pr.Source = proto.SourcePermissionRequest
		if resp := send(t, env, pr); resp.Decision != "deny" || resp.DecidedBy != "user" {
			t.Fatalf("broken=%v 暂停时越界的 PermissionRequest 应由人定: %+v", broken, resp)
		}
		codex := withRole(permission(env.repo, cmd), role)
		codex.Session.Tool, codex.Source = "codex", ""
		if resp := send(t, env, codex); resp.Decision != "deny" || resp.DecidedBy != "user" {
			t.Fatalf("broken=%v 暂停时越界的 Codex 请求应由人定: %+v", broken, resp)
		}
		mu.Lock()
		if len(got) != 2 || !got[0].BeyondRole || !got[1].BeyondRole {
			t.Fatalf("broken=%v 送 Decider 的应是两条越界请求: %+v", broken, got)
		}
		mu.Unlock()
		// 暂停下不越界的照旧交回终端
		if resp := send(t, env, withRole(permission(env.repo, "git status"), role)); resp.Decision != "passthrough" || resp.DecidedBy != "paused" {
			t.Fatalf("broken=%v 暂停下不越界的照旧交回终端: %+v", broken, resp)
		}
	}
}

func TestPausedForbidStillDenies(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	rolesFor(t, env, "")
	roundTrip(t, env.cfg.SocketPath, control("pause"))
	for _, src := range []string{proto.SourcePreToolUse, proto.SourcePermissionRequest} {
		r := permission(env.repo, "git push origin main")
		r.Source = src
		if resp := send(t, env, r); resp.Decision != "deny" || resp.DecidedBy != "role:写作" {
			t.Fatalf("%s: 暂停时禁用仍应拒: %+v", src, resp)
		}
	}
}

// 守护进程目录在项目里（--dir、RAILSE_HOME 指过去）时，agent 改 roles.yaml、rules.yaml、trusts.json 按 destructive，不当项目内写入放行。
func TestDaemonDirWritesAreDestructive(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	proj, err := filepath.EvalSymlinks(filepath.Dir(env.cfg.Dir)) // 守护进程目录是 <proj>/h；写入路径用真实路径，同 cwd 的口径
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(proj, ".git"), 0o755)
	for _, name := range []string{"roles.yaml", "rules.yaml", "trusts.json"} {
		r := proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: proj},
			ToolName: "Write", Payload: policyFileWrite(filepath.Join(proj, "h", name), proj), DeadlineSeconds: 86390}
		if resp := send(t, env, r); resp.Decision == "allow" || resp.Risk != "destructive" {
			t.Fatalf("写 %s 应按 destructive: %+v", name, resp)
		}
	}
	// 同一项目里的普通文件照旧按项目内放行
	r := proto.Request{V: proto.Version, Kind: "permission", Session: proto.Session{Tool: "claude", CLISessionID: "s1", Cwd: proj},
		ToolName: "Write", Payload: policyFileWrite(filepath.Join(proj, "a.txt"), proj), DeadlineSeconds: 86390}
	if resp := send(t, env, r); resp.Decision != "allow" || resp.Risk != "write_in_project" {
		t.Fatalf("普通文件照旧: %+v", resp)
	}
}

func policyFileWrite(path, cwd string) policy.Payload {
	return policy.Payload{Type: policy.PayloadFileWrite, Path: path, Cwd: cwd}
}

// dirs 模式经符号链接写的、或大小写不同（macOS），与解析过的 cwd 照样匹配。
func TestRoleDirsNormalized(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	real := realRepo(t, env)
	link := filepath.Join(filepath.Dir(real), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	// 只规范化通配之前的部分：通配段本身匹配的是链接名还是真实名，不读目录列不出来，不在此列
	cases := []string{link, link + "/**", filepath.Dir(link) + "/*"}
	if runtime.GOOS == "darwin" {
		cases = append(cases, strings.ToUpper(real[:2])+real[2:]) // /Private/var/... 与 /private/var/... 是同一个目录
	}
	for _, pat := range cases {
		writeRoles(t, env.cfg.RolesPath, "version: 1\nroles:\n  - name: 严\n    dirs: [\""+pat+"\"]\n    ceiling: read\n")
		send(t, env, permission(env.repo, "git status"))
		if e := lastAudit(t, env); e.Session.Role != "严" {
			t.Errorf("模式 %q 应绑定到严: %+v", pat, e.Session)
		}
	}
}

// 角色解析顺序：RAILSE_ROLE 指向存在的角色 > 当前目录命中的角色（并更新钉住）> 会话钉住的角色（当前目录不属于任何角色时沿用，
// 不降为未分配）。钉住只在判定审批时建立与更新，答题与事件只读取；角色从文件里删掉时解除。
func TestRolePinnedPerSession(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	other := filepath.Join(filepath.Dir(env.repo), "other") // 宽松角色“别处”的目录；env.repo 是严格的“写作”
	os.MkdirAll(filepath.Join(other, ".git"), 0o755)
	realOther, _ := filepath.EvalSymlinks(other)
	rolesFor(t, env, "  - name: 别处\n    dirs: [\""+realOther+"\"]\n")
	nowhere := t.TempDir()
	role := func(cwd, envRole, session string) string {
		r := withRole(permission(cwd, "git status"), envRole)
		r.Session.CLISessionID = session
		send(t, env, r)
		return lastAudit(t, env).Session.Role
	}
	// 宽松目录 → 严格目录：换成严格的
	if got := role(other, "", "a"); got != "别处" {
		t.Fatalf("先在宽松目录: %q", got)
	}
	if got := role(env.repo, "", "a"); got != "写作" {
		t.Fatalf("宽松目录 → 严格目录应换成严格角色: %q", got)
	}
	// 严格目录 → 无角色目录：沿用钉住的严格角色，不降为未分配
	if got := role(nowhere, "", "a"); got != "写作" {
		t.Fatalf("严格目录 → 无角色目录应沿用严格角色: %q", got)
	}
	// 严格目录 → 宽松目录：当前目录的角色优先
	if got := role(other, "", "a"); got != "别处" {
		t.Fatalf("严格目录 → 宽松目录按当前目录: %q", got)
	}
	if got := role(nowhere, "", "a"); got != "别处" {
		t.Fatalf("钉住随之更新: %q", got)
	}
	// RAILSE_ROLE 指向存在的角色优先；不存在的不算
	if got := role(other, "运维", "a"); got != "运维" {
		t.Fatalf("显式 RAILSE_ROLE 优先: %q", got)
	}
	if got := role(nowhere, "不存在", "a"); got != "运维" {
		t.Fatalf("不存在的 RAILSE_ROLE 不算，沿用钉住: %q", got)
	}
	// 别的会话不受影响
	if got := role(nowhere, "", "b"); got != "" {
		t.Fatalf("别的会话没钉过，未分配: %q", got)
	}
	// 运维从文件里删掉：解除钉住
	writeRoles(t, env.cfg.RolesPath, strings.Replace(mustRead(t, env.cfg.RolesPath), "  - name: 运维\n    ceiling: destructive\n", "", 1))
	if got := role(nowhere, "", "a"); got != "" {
		t.Fatalf("角色删掉后解除钉住: %q", got)
	}
}

// 答题与事件只读取钉住，不建立。
func TestRolePinOnlyFromPermissions(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	rolesFor(t, env, "")
	nowhere := t.TempDir()
	ask := askRequest(env.repo)
	ask.Session.CLISessionID = "q"
	send(t, env, ask)
	if e := lastAudit(t, env); e.Session.Role != "写作" {
		t.Fatalf("答题按目录带角色: %+v", e.Session)
	}
	roundTrip(t, env.cfg.SocketPath, proto.Event{V: proto.Version, Kind: "done", Session: proto.Session{Tool: "claude", CLISessionID: "q", Cwd: env.repo}})
	r := permission(nowhere, "git status")
	r.Session.CLISessionID = "q"
	send(t, env, r)
	if e := lastAudit(t, env); e.Session.Role != "" {
		t.Fatalf("答题与事件不该建立钉住: %+v", e.Session)
	}
}

// 命令替换里的禁用命令照样拒，限时放行挡不住；含解析不了的结构的按越界，放行也挡不住。
func TestForbidInsideSubstitutionBeatsTrust(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	rolesFor(t, env, "")
	roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(time.Hour)}})
	pr := permission(env.repo, `echo "$(git push origin main)"`)
	pr.Source = proto.SourcePermissionRequest
	if resp := send(t, env, pr); resp.Decision != "deny" || resp.DecidedBy != "role:写作" {
		t.Fatalf("命令替换里的禁用应拒: %+v", resp)
	}
	pr = permission(env.repo, "bash <<EOF\ngit push origin main\nEOF")
	pr.Source = proto.SourcePermissionRequest
	if resp := send(t, env, pr); resp.Decision == "allow" || strings.HasPrefix(resp.DecidedBy, "trust:") {
		t.Fatalf("含解析不了的结构不该被放行放过: %+v", resp)
	}
	if e := lastAudit(t, env); !e.BeyondRole || e.DecidedBy != "no_hub" {
		t.Fatalf("应按越界、送人工决定（没有 hub 时交回终端）: %+v", e)
	}
	// PreToolUse 来的回 ask，理由写明含无法检查的子命令
	resp := send(t, env, preToolUse(env.repo, "bash <<EOF\ngit push origin main\nEOF", "bypassPermissions", "toolu_h"))
	if resp.Decision != "ask" || !strings.Contains(resp.Reason, "含无法检查的子命令") {
		t.Fatalf("PreToolUse 应回 ask: %+v", resp)
	}
}

// 越界（含 roles.yaml 有误）的去向，逐格核对：来源 × hub（没有 / 不可达 / 可达）× 暂停与否。
// 能强制问人的：Claude PreToolUse 回 ask（终端确认框）；Claude PermissionRequest 与 Codex 的钩子只在 CLI 本来就要问人时触发，
// 交回终端就是 CLI 自己的确认框，hub 可达时由手机定。Gemini 的钩子交回终端等于继续执行（yolo、headless 下直接跑），
// 所以一律 deny。任何格都不许 allow。
func TestBeyondRoleRoutingTable(t *testing.T) {
	type hubMode string
	const (
		noHub       hubMode = "none"
		unreachable hubMode = "unreachable"
		reachable   hubMode = "reachable"
	)
	decider := map[hubMode]Decider{
		noHub: LocalDecider{},
		unreachable: deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
			return Outcome{Action: "passthrough", DecidedBy: hubUnreachable, Reason: "连不上 hub，交回终端处理"}, nil
		}),
		reachable: deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
			return Outcome{Action: "deny", DecidedBy: "user"}, nil
		}),
	}
	for _, broken := range []bool{false, true} {
		for _, hm := range []hubMode{noHub, unreachable, reachable} {
			for _, paused := range []bool{false, true} {
				env := startServer(t, decider[hm], nil)
				role, cmd := "开发", "git push origin main"
				if broken {
					writeRoles(t, env.cfg.RolesPath, "version: 1\nroles:\n  - name: a\n    ceiling: hgih\n")
					role, cmd = "", "touch a"
				} else {
					rolesFor(t, env, "")
				}
				if paused {
					roundTrip(t, env.cfg.SocketPath, control("pause"))
				}
				for _, src := range []string{"claude-pre", "claude-pr", "codex", "gemini"} {
					var r proto.Request
					switch src {
					case "claude-pre":
						r = preToolUse(env.repo, cmd, "bypassPermissions", "toolu_x")
					case "claude-pr":
						r = permission(env.repo, cmd)
						r.Source = proto.SourcePermissionRequest
					default:
						r = permission(env.repo, cmd)
						r.Session.Tool = src
					}
					resp := send(t, env, withRole(r, role))
					cell := fmt.Sprintf("broken=%v hub=%s paused=%v %s", broken, hm, paused, src)
					var want string
					switch {
					case src == "gemini":
						want = "deny"
					case src == "claude-pre":
						want = "ask"
					case hm == reachable:
						want = "deny" // 手机上拒了
					default:
						want = "passthrough" // CLI 自己的确认框
					}
					if resp.Decision != want {
						t.Errorf("%s: 想要 %s，得到 %+v", cell, want, resp)
					}
					if src == "gemini" && !strings.Contains(resp.Reason, "Gemini 无法在这里问人") {
						t.Errorf("%s: Gemini 的拒绝理由要写明: %q", cell, resp.Reason)
					}
				}
			}
		}
	}
}

// 钉住表满了：淘汰最久没用的再存，不放弃钉住；session_end 释放这个会话的条目。
func TestRolePinCapacityAndSessionEnd(t *testing.T) {
	setVar(t, &maxPinned, 3)
	env := startServer(t, LocalDecider{}, nil)
	rolesFor(t, env, "")
	nowhere := t.TempDir()
	role := func(cwd, session string) string {
		r := permission(cwd, "git status")
		r.Session.CLISessionID = session
		send(t, env, r)
		return lastAudit(t, env).Session.Role
	}
	for _, id := range []string{"a", "b", "c"} {
		role(env.repo, id)
	}
	role(nowhere, "a") // a 刚用过，b 最久没用
	if got := role(env.repo, "d"); got != "写作" {
		t.Fatalf("d: %q", got)
	}
	if got := role(nowhere, "d"); got != "写作" {
		t.Fatalf("表满时新会话也要钉住（淘汰最旧的）: %q", got)
	}
	if got := role(nowhere, "b"); got != "" {
		t.Fatalf("最久没用的 b 应被淘汰: %q", got)
	}
	if got := role(nowhere, "a"); got != "写作" {
		t.Fatalf("a 不该被淘汰: %q", got)
	}
	roundTrip(t, env.cfg.SocketPath, proto.Event{V: proto.Version, Kind: "session_end", Session: proto.Session{Tool: "claude", CLISessionID: "a", Cwd: nowhere}})
	if got := role(nowhere, "a"); got != "" {
		t.Fatalf("session_end 后应释放: %q", got)
	}
}

// 嵌套起换了角色的 CLI：有角色的会话里越界，限时放行与 RAILSE_TRUST 都挡不住；未分配的会话照旧。
func TestNestedRoleChangeIsBeyond(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	rolesFor(t, env, "")
	roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(time.Hour)}})
	resp := send(t, env, preToolUse(env.repo, "RAILSE_ROLE=运维 claude -p 'deploy'", "bypassPermissions", "toolu_n"))
	if resp.Decision != "ask" || !strings.Contains(resp.Reason, "试图更改 Railse 的角色或放行设置") {
		t.Fatalf("有角色的会话里换角色应越界: %+v", resp)
	}
	nowhere := t.TempDir()
	r := permission(nowhere, "RAILSE_ROLE=运维 claude -p 'deploy'")
	r.Session.CLISessionID = "unassigned"
	if resp := send(t, env, r); strings.Contains(resp.Reason, "试图更改") {
		t.Fatalf("未分配的会话照旧: %+v", resp)
	}
}

// 限时放行不覆盖 Railse 自己的配置（~/.railse 与守护进程目录）：照常进人工决定路径；有没有角色文件、有没有角色都一样；
// 别的敏感路径在放行期间照旧（0.2.3）。
func TestTrustNeverCoversControlPlane(t *testing.T) {
	for _, withRoles := range []string{"none", "unassigned", "运维"} {
		var mu sync.Mutex
		sent := 0
		dec := deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
			mu.Lock()
			sent++
			mu.Unlock()
			return Outcome{Action: "deny", DecidedBy: "user"}, nil
		})
		env := startServer(t, dec, nil)
		if withRoles != "none" {
			rolesFor(t, env, "")
		}
		role := ""
		if withRoles == "运维" {
			role = "运维"
		}
		cwd := filepath.Join(filepath.Dir(env.repo), "plain") // 不属于任何角色目录
		os.MkdirAll(filepath.Join(cwd, ".git"), 0o755)
		roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
			Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: cwd, Until: time.Now().Add(time.Hour)}})
		for _, cmd := range []string{"echo x > ../h/rules.yaml", "cat ../h/trusts.json", "cat " + env.cfg.HomeDir + "/.railse/roles.yaml"} {
			r := withRole(preToolUse(cwd, cmd, "bypassPermissions", "toolu_c"), role)
			resp := send(t, env, r)
			if resp.Decision != "ask" || !strings.Contains(resp.Reason, "限时放行不覆盖 Railse 自己的配置") {
				t.Fatalf("%s %s: PreToolUse 应回 ask 并写明: %+v", withRoles, cmd, resp)
			}
			for _, tool := range []string{"claude", "codex"} {
				pr := withRole(permission(cwd, cmd), role)
				pr.Session.Tool = tool
				if tool == "claude" {
					pr.Source = proto.SourcePermissionRequest
				}
				if resp := send(t, env, pr); resp.Decision != "deny" || resp.DecidedBy != "user" {
					t.Fatalf("%s %s %s: 应送人工决定: %+v", withRoles, tool, cmd, resp)
				}
				if e := lastAudit(t, env); !strings.Contains(e.Reason, "限时放行不覆盖 Railse 自己的配置") {
					t.Fatalf("%s %s: 审计理由应写明: %+v", withRoles, cmd, e)
				}
			}
			g := withRole(permission(cwd, cmd), role)
			g.Session.Tool = "gemini"
			if resp := send(t, env, g); resp.Decision == "allow" {
				t.Fatalf("%s gemini %s: 不该被放行放过: %+v", withRoles, cmd, resp)
			}
		}
		// 别的敏感路径在放行期间照旧放过（0.2.3）
		pr := withRole(permission(cwd, "cat ~/.ssh/id_rsa"), role)
		pr.Source = proto.SourcePermissionRequest
		if resp := send(t, env, pr); resp.Decision != "allow" || !strings.HasPrefix(resp.DecidedBy, "trust:") {
			t.Fatalf("%s: 别的敏感路径照旧: %+v", withRoles, resp)
		}
	}
}
