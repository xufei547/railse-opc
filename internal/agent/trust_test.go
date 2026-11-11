package agent

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/proto"
)

func fixedNow(t0 time.Time) func() time.Time { return func() time.Time { return t0 } }

func TestTrustsAddValidateAndClamp(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	tr, err := newTrusts(filepath.Join(t.TempDir(), "trusts.json"), fixedNow(t0))
	if err != nil {
		t.Fatal(err)
	}
	got, err := tr.add(proto.Trust{Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s1", Until: t0.Add(48 * time.Hour), CreatedBy: "cli"})
	if err != nil || got.ID == "" || !got.Until.Equal(t0.Add(proto.TrustMax)) {
		t.Fatalf("应补 ID 并把时长截到 24h：%+v %v", got, err)
	}
	if _, err := tr.add(proto.Trust{Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s2", Until: t0.Add(time.Minute)}); err == nil {
		t.Fatal("短于 5 分钟应报错")
	}
	if _, err := tr.add(proto.Trust{Scope: "machine", Until: t0.Add(time.Hour)}); err == nil {
		t.Fatal("不认识的范围应报错")
	}
	if _, err := tr.add(proto.Trust{Scope: proto.TrustScopeProject, Until: t0.Add(time.Hour)}); err == nil {
		t.Fatal("project 范围缺项目根应报错")
	}
	for i := 0; i < proto.TrustPerMachine-1; i++ {
		if _, err := tr.add(proto.Trust{Scope: proto.TrustScopeProject, Project: "/p/" + string(rune('a'+i)), Until: t0.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tr.add(proto.Trust{Scope: proto.TrustScopeProject, Project: "/p/z", Until: t0.Add(time.Hour)}); err == nil {
		t.Fatal("超过 20 条应报错")
	}
}

func TestTrustsMatchExpireRevokePersist(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	now := t0
	path := filepath.Join(t.TempDir(), "trusts.json")
	tr, _ := newTrusts(path, func() time.Time { return now })
	short, _ := tr.add(proto.Trust{Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s1", Until: t0.Add(10 * time.Minute)})
	long, _ := tr.add(proto.Trust{Scope: proto.TrustScopeProject, Project: "/home/u/proj", Until: t0.Add(2 * time.Hour)})
	sess := proto.Session{Tool: "claude", CLISessionID: "s1"}
	if m, ok := tr.match(sess, "/home/u/proj"); !ok || m.ID != long.ID {
		t.Fatalf("两条都命中时取到期最晚的：%+v %v", m, ok)
	}
	if m, ok := tr.match(proto.Session{Tool: "codex", CLISessionID: "x"}, "/home/u/proj"); !ok || m.ID != long.ID {
		t.Fatalf("项目放行对别的会话也生效：%+v %v", m, ok)
	}
	if _, ok := tr.match(proto.Session{Tool: "claude", CLISessionID: "s1"}, "/other"); !ok {
		t.Fatal("会话放行不看项目")
	}
	if _, ok := tr.match(proto.Session{Tool: "claude", CLISessionID: "s9"}, "/other"); ok {
		t.Fatal("都不命中")
	}
	now = t0.Add(11 * time.Minute)
	if _, ok := tr.match(sess, "/other"); ok {
		t.Fatal("过期的会话放行不该命中")
	}
	if gone := tr.sweep(); len(gone) != 1 || gone[0].ID != short.ID {
		t.Fatalf("sweep 应清掉过期那条：%+v", gone)
	}
	// 重新打开：只剩没过期的
	tr2, err := newTrusts(path, func() time.Time { return now })
	if err != nil || len(tr2.list()) != 1 || tr2.list()[0].ID != long.ID {
		t.Fatalf("落盘应保留未过期的：%v %+v", err, tr2.list())
	}
	if gone := tr2.revoke(long.ID); len(gone) != 1 || len(tr2.list()) != 0 {
		t.Fatal("撤销后应为空")
	}
	if len(tr2.revoke("nope")) != 0 {
		t.Fatal("撤不存在的什么都不返回")
	}
}

func TestParseTrustFor(t *testing.T) {
	cases := map[string]time.Duration{"30m": 30 * time.Minute, "2h": 2 * time.Hour, "8h": 8 * time.Hour, "25h": proto.TrustMax, "1m": proto.TrustMin, "": proto.TrustDefault}
	for in, want := range cases {
		if got, err := ParseTrustFor(in); err != nil || got != want {
			t.Errorf("%q = %v, %v，想要 %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"abc", "-2h", "2 hours"} {
		if _, err := ParseTrustFor(bad); err == nil {
			t.Errorf("%q 应报错", bad)
		}
	}
}

// 管理命令的校验与回执：短于 5 分钟报错、撤不存在的报错、--all 空表也算成功；status 带条数；trustChanged 收到建与撤。
func TestTrustControlValidation(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	var mu sync.Mutex
	var changes []string
	env.srv.SetTrustChanged(func(tr proto.Trust, op string) {
		mu.Lock()
		changes = append(changes, op+":"+tr.ID)
		mu.Unlock()
	})
	sock := env.cfg.SocketPath
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set"}), &reply)
	if reply.OK {
		t.Fatalf("缺 trust 应报错：%+v", reply)
	}
	json.Unmarshal(roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeProject, Project: env.repo, Until: time.Now().Add(time.Minute)}}), &reply)
	if reply.OK || reply.Error != errTrustTooShort.Error() {
		t.Fatalf("短于 5 分钟应报错：%+v", reply)
	}
	reply = proto.ControlReply{}
	json.Unmarshal(roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "trust_set",
		Trust: &proto.Trust{Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s1", Until: time.Now().Add(48 * time.Hour), Note: "盯着"}}), &reply)
	if !reply.OK || len(reply.Trusts) != 1 || reply.Trusts[0].CreatedBy != "cli" || reply.Trusts[0].Until.After(time.Now().Add(proto.TrustMax)) {
		t.Fatalf("超上限应截到 24 小时、默认 cli 建：%+v", reply)
	}
	id := reply.Trusts[0].ID
	if st := ctlStatus(t, env); st.Trusts != 1 {
		t.Fatalf("status 应报 1 条：%+v", st)
	}
	reply = proto.ControlReply{}
	json.Unmarshal(roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "trust_revoke", TrustID: "nope"}), &reply)
	if reply.OK {
		t.Fatalf("撤不存在的应报错：%+v", reply)
	}
	reply = proto.ControlReply{}
	json.Unmarshal(roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "trust_revoke", TrustID: "all"}), &reply)
	if !reply.OK || len(reply.Trusts) != 0 {
		t.Fatalf("撤全部：%+v", reply)
	}
	reply = proto.ControlReply{}
	json.Unmarshal(roundTrip(t, sock, proto.Control{V: proto.Version, Kind: "control", Op: "trust_revoke", TrustID: "all"}), &reply)
	if !reply.OK {
		t.Fatalf("空表撤全部也算成功：%+v", reply)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(changes) != 2 || changes[0] != "set:"+id || changes[1] != "revoke:"+id {
		t.Fatalf("trustChanged：%v", changes)
	}
}

// 到期清理：后台每隔 trustSweepEvery 清一次，清掉的写 trust_expired 审计；RAILSE_TRUST 建的到期后同会话不再重建。
func TestTrustSweepAuditsExpired(t *testing.T) {
	setVar(t, &trustSweepEvery, 20*time.Millisecond)
	env := startServer(t, LocalDecider{}, nil)
	sock := env.cfg.SocketPath
	r := preToolUse(env.repo, "git push origin main", "auto", "toolu_s1")
	r.TrustFor = "5m"
	var resp proto.Response
	json.Unmarshal(roundTrip(t, sock, r), &resp)
	if resp.Decision != "allow" {
		t.Fatalf("应放行：%+v", resp)
	}
	later := time.Now().Add(6 * time.Minute)
	env.srv.trusts.mu.Lock()
	env.srv.trusts.now = func() time.Time { return later }
	env.srv.trusts.mu.Unlock()
	waitFor(t, func() bool {
		for _, e := range readAudit(t, env.cfg.AuditPath) {
			if e.Kind == "control" && e.Message == "trust_expired" {
				return true
			}
		}
		return false
	})
	r.ToolUseID = "toolu_s2"
	json.Unmarshal(roundTrip(t, sock, r), &resp)
	if resp.Decision != "ask" {
		t.Fatalf("到期后同会话不该重建：%+v", resp)
	}
}

// hub 快照的合并规则：快照里有的写入并标已同步；已同步却不在快照里的删掉（被手机撤了）；未同步的保留待补发；
// 本机撤了、hub 还没收下的（墓碑）不被快照复活，要重发撤销；快照里已过期的不收；不完整的快照（complete 为假）不删任何东西。
// 已同步标记与墓碑都落盘，守护进程重启后照样认。
func TestTrustsApplySnapshot(t *testing.T) {
	t0 := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	now := t0
	clock := func() time.Time { return now }
	path := filepath.Join(t.TempDir(), "trusts.json")
	tr, _ := newTrusts(path, clock)
	ids := func(l []proto.Trust) []string {
		var out []string
		for _, x := range l {
			out = append(out, x.ID)
		}
		sort.Strings(out)
		return out
	}
	local, _ := tr.add(proto.Trust{Scope: proto.TrustScopeProject, Project: "/home/u/proj", Until: t0.Add(time.Hour), CreatedBy: "cli"})
	if got := ids(tr.unsynced()); !slices.Equal(got, []string{local.ID}) {
		t.Fatalf("本机建的应是未同步：%v", got)
	}
	phone := proto.Trust{ID: "p1", MachineID: "m1", Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s1", Until: t0.Add(2 * time.Hour), CreatedAt: t0, CreatedBy: "phone"}
	if gone, resend := tr.applySnapshot([]proto.Trust{local, phone}, true); len(gone) != 0 || len(resend) != 0 {
		t.Fatalf("第一份快照不该撤什么：%v %v", gone, resend)
	}
	if len(tr.unsynced()) != 0 || len(tr.list()) != 2 {
		t.Fatalf("快照里的都应已同步：%v %v", tr.unsynced(), tr.list())
	}
	if m, ok := tr.match(proto.Session{Tool: "claude", CLISessionID: "s1"}, ""); !ok || m.ID != "p1" {
		t.Fatal("手机建的应生效")
	}
	if gone, _ := tr.applySnapshot([]proto.Trust{local}, true); len(gone) != 1 || gone[0].ID != "p1" {
		t.Fatalf("已同步却不在快照里的应被撤：%v", gone)
	}
	// 断线期间本机建一条：快照里没有它，保留、待补发
	offline, _ := tr.add(proto.Trust{Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s2", Until: t0.Add(time.Hour), CreatedBy: "cli"})
	if gone, _ := tr.applySnapshot([]proto.Trust{local}, true); len(gone) != 0 || !slices.Equal(ids(tr.unsynced()), []string{offline.ID}) {
		t.Fatalf("未同步的应保留：%v %v", gone, tr.unsynced())
	}
	// 本机撤掉已同步的 local：hub 的快照还带着它时不复活，要重发撤销；hub 收下后（快照里没了）墓碑清掉
	tr.revoke(local.ID)
	gone, resend := tr.applySnapshot([]proto.Trust{local, offline}, true)
	if len(gone) != 0 || !slices.Equal(resend, []string{local.ID}) || !slices.Equal(ids(tr.list()), []string{offline.ID}) {
		t.Fatalf("撤过的不该被快照复活：%v %v %v", gone, resend, tr.list())
	}
	if len(tr.unsynced()) != 0 {
		t.Fatal("快照里有 offline，应已同步")
	}
	// 重新打开：已同步标记与墓碑都还在
	tr2, err := newTrusts(path, clock)
	if err != nil || len(tr2.unsynced()) != 0 {
		t.Fatalf("已同步标记应落盘：%v %v", err, tr2.unsynced())
	}
	if _, resend := tr2.applySnapshot([]proto.Trust{local, offline}, true); !slices.Equal(resend, []string{local.ID}) || len(tr2.list()) != 1 {
		t.Fatalf("墓碑应落盘：%v %v", resend, tr2.list())
	}
	if _, resend := tr2.applySnapshot([]proto.Trust{offline}, true); len(resend) != 0 {
		t.Fatal("hub 收下撤销后不再重发")
	}
	if _, resend := tr2.applySnapshot([]proto.Trust{offline}, true); len(resend) != 0 || len(tr2.tomb) != 0 {
		t.Fatalf("墓碑应清掉：%v", tr2.tomb)
	}
	// 不完整的快照不删已同步的
	if gone, _ := tr2.applySnapshot(nil, false); len(gone) != 0 || len(tr2.list()) != 1 {
		t.Fatal("不完整的快照不该删东西")
	}
	// 快照里已过期的不收；不合规的（缺目标）不收
	stale := proto.Trust{ID: "old", Scope: proto.TrustScopeProject, Project: "/x", Until: t0.Add(-time.Minute), CreatedBy: "phone"}
	bad := proto.Trust{ID: "bad", Scope: proto.TrustScopeSession, Until: t0.Add(time.Hour), CreatedBy: "phone"}
	tr2.applySnapshot([]proto.Trust{offline, stale, bad}, true)
	if !slices.Equal(ids(tr2.list()), []string{offline.ID}) {
		t.Fatalf("过期的、不合规的不该收：%v", tr2.list())
	}
	// 墓碑在原到期时间之后由 sweep 清掉
	tr2.revoke(offline.ID)
	now = t0.Add(2 * time.Hour)
	tr2.sweep()
	if len(tr2.tomb) != 0 {
		t.Fatalf("过期的墓碑应清掉：%v", tr2.tomb)
	}
}

// hub 快照里晚于此刻的 CreatedAt 截到此刻（免得撑大角色的 trust_max）。
func TestApplySnapshotClampsFutureCreatedAt(t *testing.T) {
	t0 := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	tr, _ := newTrusts(filepath.Join(t.TempDir(), "trusts.json"), func() time.Time { return t0 })
	snap := []proto.Trust{
		{ID: "f", Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s1", Until: t0.Add(2 * time.Hour), CreatedAt: t0.Add(5 * time.Hour), CreatedBy: "phone"},
		{ID: "p", Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s2", Until: t0.Add(2 * time.Hour), CreatedAt: t0.Add(-time.Hour), CreatedBy: "phone"},
	}
	tr.applySnapshot(snap, true)
	for _, x := range tr.list() {
		switch x.ID {
		case "f":
			if !x.CreatedAt.Equal(t0) {
				t.Errorf("未来的 CreatedAt 应截到此刻: %v", x.CreatedAt)
			}
		case "p":
			if !x.CreatedAt.Equal(t0.Add(-time.Hour)) {
				t.Errorf("过去的 CreatedAt 原样保留: %v", x.CreatedAt)
			}
		}
	}
}
