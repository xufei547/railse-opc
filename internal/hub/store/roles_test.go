package store

import (
	"reflect"
	"testing"

	"github.com/xufei547/railse-opc/internal/proto"
)

// 请求的角色与越界标记入库读回不变。
func TestRequestRoleRoundTrip(t *testing.T) {
	s := openTemp(t)
	if err := s.InsertRequest(Request{ID: "r1", MachineID: "m", Kind: "permission", ToolName: "Bash", State: "pending", Role: "写作", BeyondRole: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertRequest(Request{ID: "r2", MachineID: "m", Kind: "permission", ToolName: "Bash", State: "pending"}); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetRequest("r1"); err != nil || r.Role != "写作" || !r.BeyondRole {
		t.Fatalf("r1 读回: %+v %v", r, err)
	}
	if r, err := s.GetRequest("r2"); err != nil || r.Role != "" || r.BeyondRole {
		t.Fatalf("r2 读回: %+v %v", r, err)
	}
}

// UpsertSession 每次都把角色改成这次上报的值，空串也覆盖。
func TestUpsertSessionUpdatesRole(t *testing.T) {
	s := openTemp(t)
	m, err := s.CreateMachine("mac", "sec", "")
	if err != nil {
		t.Fatal(err)
	}
	sess := proto.Session{Tool: "claude", CLISessionID: "c1", Role: "开发"}
	id, err := s.UpsertSession(m.ID, sess, "p")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"开发", "写作", ""} {
		sess.Role = want
		if _, err := s.UpsertSession(m.ID, sess, ""); err != nil {
			t.Fatal(err)
		}
		if got, err := s.SessionRole(id); err != nil || got != want {
			t.Fatalf("会话角色 = %q, %v，想要 %q", got, err, want)
		}
	}
	if _, err := s.SessionRole("nope"); err != ErrNotFound {
		t.Fatalf("没有的会话应 ErrNotFound: %v", err)
	}
}

// ReplaceMachineRoles 整份替换一台机器的角色表，顺序按传入的先后；别的机器不受影响；空切片清空。
func TestReplaceMachineRoles(t *testing.T) {
	s := openTemp(t)
	a, _ := s.CreateMachine("a", "sa", "")
	b, _ := s.CreateMachine("b", "sb", "")
	first := []proto.RoleSummary{
		{Name: "开发", Note: "写代码", Ceiling: "network", ForbidCount: 1, TrustMaxMins: 1440, Dirs: []string{"~/RailseWork", "~/x/**"}},
		{Name: "写作", Ceiling: "write_in_project", ForbidCount: 2, TrustMaxMins: 120},
	}
	if err := s.ReplaceMachineRoles(a.ID, first); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceMachineRoles(b.ID, []proto.RoleSummary{{Name: "运维", TrustMaxMins: -1}}); err != nil {
		t.Fatal(err)
	}
	second := []proto.RoleSummary{{Name: "写作", Note: "只改文档"}, {Name: "开发"}}
	if err := s.ReplaceMachineRoles(a.ID, second); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListMachineRoles()
	if err != nil {
		t.Fatal(err)
	}
	want := []MachineRole{
		{MachineID: a.ID, RoleSummary: proto.RoleSummary{Name: "写作", Note: "只改文档"}},
		{MachineID: a.ID, RoleSummary: proto.RoleSummary{Name: "开发"}},
		{MachineID: b.ID, RoleSummary: proto.RoleSummary{Name: "运维", TrustMaxMins: -1}},
	}
	if a.ID > b.ID {
		want = []MachineRole{want[2], want[0], want[1]}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("角色表:\n%+v\n想要\n%+v", got, want)
	}
	if err := s.ReplaceMachineRoles(a.ID, first); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListMachineRoles()
	var dirs []string
	for _, r := range got {
		if r.Name == "开发" {
			dirs = r.Dirs
		}
	}
	if !reflect.DeepEqual(dirs, first[0].Dirs) {
		t.Fatalf("目录往返: %v", dirs)
	}
	if err := s.ReplaceMachineRoles(a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListMachineRoles(); len(got) != 1 || got[0].MachineID != b.ID {
		t.Fatalf("清空后只剩 b 的: %+v", got)
	}
}
