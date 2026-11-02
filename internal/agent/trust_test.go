package agent

import (
	"encoding/json"
	"path/filepath"
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
