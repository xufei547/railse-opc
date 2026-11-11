package agent

import (
	"context"
	"encoding/json"
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

func TestRoleTrustMax(t *testing.T) {
	dec := deciderFunc(func(ctx context.Context, pr PendingRequest) (Outcome, error) {
		return Outcome{Action: "deny", DecidedBy: "user"}, nil
	})
	env := startServer(t, dec, nil)
	rolesFor(t, env, "  - name: 短\n    trust_max: 2h\n  - name: 不放\n    trust_max: none\n")
	set := func(created time.Time) string {
		var reply proto.ControlReply
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
			Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(20 * time.Hour), CreatedAt: created}}), &reply)
		if !reply.OK {
			t.Fatalf("建放行: %+v", reply)
		}
		return reply.Trusts[0].ID
	}
	revokeAll := func() {
		roundTrip(t, env.cfg.SocketPath, proto.Control{V: proto.Version, Kind: "control", Op: "trust_revoke", TrustID: "all"})
	}
	req := func(role string) proto.Response {
		r := withRole(permission(env.repo, "curl https://example.com"), role)
		r.Source = proto.SourcePermissionRequest
		return send(t, env, r)
	}
	set(time.Now().Add(-3 * time.Hour))
	if resp := req("短"); resp.Decision == "allow" {
		t.Fatalf("建了 3 小时的放行对 trust_max 2h 的角色应无效: %+v", resp)
	}
	if resp := req("运维"); resp.Decision != "allow" {
		t.Fatalf("没设 trust_max 的角色照旧放行: %+v", resp)
	}
	revokeAll()
	set(time.Now())
	if resp := req("短"); resp.Decision != "allow" {
		t.Fatalf("2 小时内的放行应有效: %+v", resp)
	}
	if resp := req("不放"); resp.Decision == "allow" {
		t.Fatalf("trust_max none 的角色不走放行: %+v", resp)
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

// 角色按会话钉住：解析出非空角色后，cwd 变了也不降为未分配、不换成别的目录角色；只有指向存在角色的 RAILSE_ROLE 能改；
// 角色从文件里删掉时解除钉住。
func TestRolePinnedPerSession(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	other := filepath.Join(filepath.Dir(env.repo), "other")
	os.MkdirAll(filepath.Join(other, ".git"), 0o755)
	realOther, _ := filepath.EvalSymlinks(other)
	rolesFor(t, env, "  - name: 别处\n    dirs: [\""+realOther+"\"]\n")
	role := func(cwd, envRole, session string) string {
		r := withRole(permission(cwd, "git status"), envRole)
		r.Session.CLISessionID = session
		send(t, env, r)
		return lastAudit(t, env).Session.Role
	}
	if got := role(env.repo, "", "s1"); got != "写作" {
		t.Fatalf("先按目录绑定到写作: %q", got)
	}
	if got := role(other, "", "s1"); got != "写作" {
		t.Fatalf("cwd 换到别的角色的目录也不换: %q", got)
	}
	if got := role(t.TempDir(), "", "s1"); got != "写作" {
		t.Fatalf("cwd 换到没有角色的目录也不降为未分配: %q", got)
	}
	if got := role(other, "", "s2"); got != "别处" {
		t.Fatalf("别的会话不受影响: %q", got)
	}
	if got := role(env.repo, "不存在", "s1"); got != "写作" {
		t.Fatalf("不存在的 RAILSE_ROLE 不能改: %q", got)
	}
	if got := role(env.repo, "运维", "s1"); got != "运维" {
		t.Fatalf("显式 RAILSE_ROLE 能改: %q", got)
	}
	if got := role(env.repo, "", "s1"); got != "运维" {
		t.Fatalf("改过之后钉在运维: %q", got)
	}
	// 运维从文件里删掉：解除钉住，回到按目录
	rolesFor2 := strings.Replace(mustRead(t, env.cfg.RolesPath), "  - name: 运维\n    ceiling: destructive\n", "", 1)
	writeRoles(t, env.cfg.RolesPath, rolesFor2)
	if got := role(other, "", "s1"); got != "别处" {
		t.Fatalf("角色删掉后应解除钉住、按目录: %q", got)
	}
}
