package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/hub/wire"
	"github.com/xufei547/railse-opc/internal/proto"
)

// newRoleSyncEnv 是接了 SyncRoles 的放行同步环境：连上前先写好 roles.yaml（见 rolesFor）。
func newRoleSyncEnv(t *testing.T, features []string) *trustSyncEnv {
	t.Helper()
	return newTrustSyncEnvWith(t, features, func(e *trustSyncEnv) {
		rolesFor(t, e.env, "")
		e.c.SyncRoles(e.env.srv)
	})
}

func (e *trustSyncEnv) rolesFrames(t *testing.T) []wire.Roles {
	t.Helper()
	var out []wire.Roles
	for _, raw := range e.hub.framesOf("roles") {
		var r wire.Roles
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestHubHelloDeclaresRoles(t *testing.T) {
	e := newRoleSyncEnv(t, []string{wire.FeatureAsk, wire.FeatureRole})
	e.connect(t)
	e.hub.mu.Lock()
	hello := e.hub.hellos[0]
	e.hub.mu.Unlock()
	if !hello.Has(wire.FeatureRole) {
		t.Fatalf("hello 应声明 role: %+v", hello.Features)
	}
	if len(hello.Roles) != 3 || hello.Roles[0].Name != "写作" || hello.Roles[0].Ceiling != "write_in_project" || hello.Roles[0].ForbidCount != 1 {
		t.Fatalf("hello 的角色摘要: %+v", hello.Roles)
	}
}

// 没接 SyncRoles 的客户端不声明 role（hub 按老守护进程处理）。
func TestHubHelloWithoutRoles(t *testing.T) {
	f := newFakeHub(t, 0, welcomeThenRecord)
	tc := startTestClient(t, f)
	tc.waitConnected(t)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hellos[0].Has(wire.FeatureRole) || f.hellos[0].Roles != nil {
		t.Fatalf("没接角色不该声明 role: %+v", f.hellos[0])
	}
}

// 重读成功后整份重发 roles 帧；文件没变不发；坏文件不发（hub 保留上一份）；删掉文件发空表。
func TestHubRolesFrameAfterReload(t *testing.T) {
	e := newRoleSyncEnv(t, []string{wire.FeatureAsk, wire.FeatureRole})
	conn := e.connect(t)
	poke := func() { roundTrip(t, e.env.cfg.SocketPath, permission(e.env.repo, "git status")) }
	poke()
	e.hub.pingAndWaitPong(t, conn)
	time.Sleep(100 * time.Millisecond)
	if n := len(e.rolesFrames(t)); n != 0 {
		t.Fatalf("文件没变不该发 roles 帧: %d", n)
	}
	writeRoles(t, e.env.cfg.RolesPath, "version: 1\nroles:\n  - name: 新角色\n    note: 改过了\n    trust_max: none\n")
	poke()
	waitFor(t, func() bool { return len(e.rolesFrames(t)) == 1 })
	got := e.rolesFrames(t)[0]
	if got.T != "roles" || len(got.Roles) != 1 || got.Roles[0].Name != "新角色" || got.Roles[0].TrustMaxMins != -1 {
		t.Fatalf("roles 帧: %+v", got)
	}
	writeRoles(t, e.env.cfg.RolesPath, "version: 1\nroles: [\n")
	poke()
	time.Sleep(200 * time.Millisecond)
	if n := len(e.rolesFrames(t)); n != 1 {
		t.Fatalf("坏文件不该发 roles 帧: %d", n)
	}
	removeFile(t, e.env.cfg.RolesPath)
	poke()
	waitFor(t, func() bool { return len(e.rolesFrames(t)) == 2 })
	raw := string(e.hub.framesOf("roles")[1])
	if !strings.Contains(raw, `"roles":[]`) {
		t.Fatalf("删掉文件应发空表（[] 而不是 null）: %s", raw)
	}
}

// hub 没声明 role：不发 roles 帧（hello 照样声明自己的能力）。
func TestHubRolesFrameNeedsHubFeature(t *testing.T) {
	e := newRoleSyncEnv(t, []string{wire.FeatureAsk})
	conn := e.connect(t)
	writeRoles(t, e.env.cfg.RolesPath, "version: 1\nroles:\n  - name: 新角色\n")
	roundTrip(t, e.env.cfg.SocketPath, permission(e.env.repo, "git status"))
	e.hub.pingAndWaitPong(t, conn)
	time.Sleep(200 * time.Millisecond)
	if n := len(e.rolesFrames(t)); n != 0 {
		t.Fatalf("hub 没声明 role 不该发 roles 帧: %d", n)
	}
}

func TestToWireBeyondRole(t *testing.T) {
	pr := PendingRequest{ID: "r1", Req: proto.Request{Session: proto.Session{Tool: "claude", CLISessionID: "s1", Role: "写作"}}, BeyondRole: true,
		Created: time.Now(), Deadline: time.Now().Add(time.Minute)}
	if w := toWire(pr); !w.BeyondRole || w.Session.Role != "写作" {
		t.Fatalf("pending 帧应带 beyond_role 与角色: %+v", w)
	}
	pr.Kind = "ask"
	if w := toWire(pr); w.BeyondRole {
		t.Fatalf("答题帧不带 beyond_role: %+v", w)
	}
}

func removeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}
