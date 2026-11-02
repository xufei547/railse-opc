package store

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/proto"
)

func trustMachine(t *testing.T, s *Store, name string) Machine {
	t.Helper()
	m, err := s.CreateMachine(name, RandomToken(), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func sessionTrust(id, machineID string, until time.Time) proto.Trust {
	return proto.Trust{ID: id, MachineID: machineID, Scope: proto.TrustScopeSession, Tool: "claude", CLISession: "s1",
		Until: until, CreatedAt: time.Now(), CreatedBy: "phone", Note: "备注"}
}

func trustIDs(l []proto.Trust) []string {
	ids := make([]string, 0, len(l))
	for _, t := range l {
		ids = append(ids, t.ID)
	}
	return ids
}

// 写入后列得出、字段原样读回；按机器筛；撤销后列不出，再撤回 ErrNotFound。
func TestTrustUpsertListRevoke(t *testing.T) {
	s := openTemp(t)
	m1, m2 := trustMachine(t, s, "m1"), trustMachine(t, s, "m2")
	until := time.Now().Add(2 * time.Hour)
	want := sessionTrust("t1", m1.ID, until)
	if ok, err := s.UpsertTrust(want); err != nil || !ok {
		t.Fatalf("UpsertTrust: %v %v", ok, err)
	}
	proj := proto.Trust{ID: "t2", MachineID: m2.ID, Scope: proto.TrustScopeProject, Project: "/p", Until: until, CreatedAt: time.Now(), CreatedBy: "cli"}
	if ok, err := s.UpsertTrust(proj); err != nil || !ok {
		t.Fatalf("UpsertTrust: %v %v", ok, err)
	}
	all, err := s.ListTrusts("")
	if err != nil || !slices.Equal(trustIDs(all), []string{"t1", "t2"}) {
		t.Fatalf("ListTrusts(全部) = %v, %v", trustIDs(all), err)
	}
	got := all[0]
	if got.MachineID != m1.ID || got.Scope != "session" || got.Tool != "claude" || got.CLISession != "s1" || got.CreatedBy != "phone" ||
		got.Note != "备注" || !got.Until.Equal(until) || got.Until.Location() != time.UTC {
		t.Fatalf("读回 = %+v", got)
	}
	if l, err := s.ListTrusts(m2.ID); err != nil || !slices.Equal(trustIDs(l), []string{"t2"}) || l[0].Project != "/p" {
		t.Fatalf("ListTrusts(m2) = %+v, %v", l, err)
	}
	// 同 id 覆盖（延长）
	want.Until = until.Add(time.Hour)
	if ok, err := s.UpsertTrust(want); err != nil || !ok {
		t.Fatalf("覆盖: %v %v", ok, err)
	}
	if l, _ := s.ListTrusts(m1.ID); len(l) != 1 || !l[0].Until.Equal(want.Until) {
		t.Fatalf("覆盖后 = %+v", l)
	}
	// 别的机器不能占用这个 id
	if ok, err := s.UpsertTrust(sessionTrust("t1", m2.ID, until)); err != nil || ok {
		t.Fatalf("别的机器覆盖应不写入: %v %v", ok, err)
	}
	if _, err := s.RevokeTrust(m2.ID, "t1", "agent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("别的机器撤销应 ErrNotFound: %v", err)
	}
	old, err := s.RevokeTrust("", "t1", "user")
	if err != nil || old.ID != "t1" || old.MachineID != m1.ID {
		t.Fatalf("RevokeTrust = %+v, %v", old, err)
	}
	if l, _ := s.ListTrusts(m1.ID); len(l) != 0 {
		t.Fatalf("撤销后仍列出: %+v", l)
	}
	if _, err := s.RevokeTrust("", "t1", "user"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("再撤应 ErrNotFound: %v", err)
	}
	if _, err := s.RevokeTrust("", "nope", "user"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("撤不存在的应 ErrNotFound: %v", err)
	}
	// 撤过的不会被守护进程重发的 set 复活
	if ok, err := s.UpsertTrust(sessionTrust("t1", m1.ID, until)); err != nil || ok {
		t.Fatalf("撤过的不应被覆盖: %v %v", ok, err)
	}
	var reason string
	if err := s.db.QueryRow("SELECT revoke_reason FROM trusts WHERE id = 't1'").Scan(&reason); err != nil || reason != "user" {
		t.Fatalf("revoke_reason = %q, %v", reason, err)
	}
}

// 已过期的列不出；ExpireTrusts 把它们标撤（原因 expired）并返回，第二次返回空。
func TestTrustExpire(t *testing.T) {
	s := openTemp(t)
	m := trustMachine(t, s, "m1")
	if _, err := s.UpsertTrust(sessionTrust("old", m.ID, time.Now().Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTrust(sessionTrust("live", m.ID, time.Now().Add(time.Hour))); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListTrusts(m.ID); !slices.Equal(trustIDs(l), []string{"live"}) {
		t.Fatalf("过期的不应列出: %v", trustIDs(l))
	}
	gone, err := s.ExpireTrusts()
	if err != nil || !slices.Equal(trustIDs(gone), []string{"old"}) {
		t.Fatalf("ExpireTrusts = %v, %v", trustIDs(gone), err)
	}
	var reason string
	if err := s.db.QueryRow("SELECT revoke_reason FROM trusts WHERE id = 'old'").Scan(&reason); err != nil || reason != "expired" {
		t.Fatalf("revoke_reason = %q, %v", reason, err)
	}
	if gone, err := s.ExpireTrusts(); err != nil || len(gone) != 0 {
		t.Fatalf("第二次 ExpireTrusts = %v, %v", trustIDs(gone), err)
	}
	// DeleteTrust 删掉整行（批准落库失败时收回刚建的放行用）
	if err := s.DeleteTrust("live"); err != nil {
		t.Fatal(err)
	}
	if l, _ := s.ListTrusts(""); len(l) != 0 {
		t.Fatalf("删后仍列出: %v", trustIDs(l))
	}
}

// 迁移 6 与 schema.sql 建出的 trusts 必须一模一样：改 schema.sql 时迁移要同步改。
func TestMigratedTrustsSchemaMatchesFresh(t *testing.T) {
	fresh := openTemp(t)
	path := filepath.Join(tempDir(t), "hub.db")
	writeRaw(t, path, schemaV2(t)+"INSERT INTO schema_version(v) VALUES(2);")
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	a, b := tableSchema(t, fresh.db, "trusts"), tableSchema(t, migrated.db, "trusts")
	if len(a) == 0 || !slices.Equal(a, b) {
		t.Fatalf("新库与迁移后的库不一致:\n新库:\n%s\n迁移:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	if !slices.ContainsFunc(a, func(s string) bool { return strings.Contains(s, "index (machine_id,until_at)") }) {
		t.Fatalf("应有 (machine_id, until_at) 索引:\n%s", strings.Join(a, "\n"))
	}
}
