package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

const testRoles = `version: 1
roles:
  - name: 写作
    note: 只改文档
    dirs: ["~/docs", "~/shared/**"]
    ceiling: write_in_project
    forbid:
      - {command_prefix: "git push"}
      - {payload: mcp}
    trust_max: 2h
  - name: 开发
    dirs: ["~/shared/**", "~/code"]
    ceiling: network
    trust_max: none
  - name: 运维
    ceiling: destructive
`

// writeRoles 写 roles.yaml，并把 mtime 推到一个新值：同一秒里连写两次、大小又相同时也认得出变化。
func writeRoles(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	bump := time.Now().Add(time.Duration(rolesBump.Add(1)) * time.Second)
	if err := os.Chtimes(path, bump, bump); err != nil {
		t.Fatal(err)
	}
}

var rolesBump atomic.Int64

func TestRolesStoreStates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.yaml")
	r := NewRoles(path)
	var changes []RolesState
	r.SetChanged(func(st RolesState) { changes = append(changes, st) })

	if st := r.Current(); st.Exists || st.Err != nil {
		t.Fatalf("没有文件时: %+v", st)
	}
	writeRoles(t, path, testRoles)
	st := r.Current()
	if !st.Exists || st.Err != nil || len(st.Set.Roles) != 3 {
		t.Fatalf("合法文件: %+v", st)
	}
	if len(changes) != 1 {
		t.Fatalf("重读成功应通知一次，得到 %d 次", len(changes))
	}
	// 没变时不重读、不通知
	r.Current()
	if len(changes) != 1 {
		t.Fatalf("文件没变不该通知")
	}
	// 坏文件：fail closed，不沿用旧版本，不通知
	writeRoles(t, path, "version: 1\nroles:\n  - name: a\n    ceiling: high\n")
	st = r.Current()
	if !st.Exists || st.Err == nil || len(st.Set.Roles) != 0 {
		t.Fatalf("坏文件应报错且没有角色: %+v", st)
	}
	if len(changes) != 1 {
		t.Fatalf("坏文件不该通知")
	}
	// 修好后恢复
	writeRoles(t, path, "version: 1\nroles:\n  - name: b\n")
	st = r.Current()
	if st.Err != nil || st.Set.Find("b") == nil {
		t.Fatalf("修好后: %+v", st)
	}
	if len(changes) != 2 {
		t.Fatalf("修好应通知")
	}
	// 删掉：回到没有角色，也通知（hub 要清空角色表）
	os.Remove(path)
	if st := r.Current(); st.Exists || st.Err != nil {
		t.Fatalf("删除后: %+v", st)
	}
	if len(changes) != 3 || changes[2].Exists {
		t.Fatalf("删除应通知一次空表: %+v", changes)
	}
}

func TestRolesStoreUnreadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "roles.yaml")
	if err := os.Mkdir(path, 0o700); err != nil { // 是目录，读不了
		t.Fatal(err)
	}
	if st := NewRoles(path).Current(); !st.Exists || st.Err == nil {
		t.Fatalf("读不了应按有误处理: %+v", st)
	}
}

func TestResolveRole(t *testing.T) {
	home := "/Users/u"
	rs, err := policy.ParseRoleSet([]byte(testRoles))
	if err != nil {
		t.Fatal(err)
	}
	st := RolesState{Exists: true, Set: rs}
	cases := []struct {
		name, env, root, cwd string
		want, kind           string
		envUnknown           bool
	}{
		{"环境变量", "运维", home + "/docs", home + "/docs", "运维", RoleByEnv, false},
		{"按项目根", "", home + "/docs", home + "/docs/sub", "写作", RoleByDir, false},
		{"无项目根按 cwd", "", "", home + "/code", "开发", RoleByDir, false},
		{"重叠取先", "", home + "/shared/x", home + "/shared/x", "写作", RoleByDir, false},
		{"环境变量不存在走目录", "不存在", home + "/code", home + "/code", "开发", RoleByDir, true},
		{"环境变量不存在且无目录", "不存在", home + "/else", home + "/else", "", RoleNone, true},
		{"未分配", "", home + "/else", home + "/else", "", RoleNone, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			role, why := ResolveRole(st, c.env, c.root, c.cwd, home)
			got := ""
			if role != nil {
				got = role.Name
			}
			if got != c.want || why.Kind != c.kind || why.EnvUnknown != c.envUnknown {
				t.Fatalf("得到 %q %+v，想要 %q %s envUnknown=%v", got, why, c.want, c.kind, c.envUnknown)
			}
		})
	}
	// 没有文件：未分配，与 0.2 一样
	if role, why := ResolveRole(RolesState{}, "运维", home+"/docs", home+"/docs", home); role != nil || why.Kind != RoleAbsent {
		t.Errorf("没有文件时: %v %+v", role, why)
	}
	// 坏文件：BrokenRole，上限 read，不认环境变量
	role, why := ResolveRole(RolesState{Exists: true, Err: errTestRoles}, "运维", "", "", home)
	if role == nil || role.Broken() == "" || role.Ceiling == nil || *role.Ceiling != policy.RiskRead || why.Kind != RoleBroken {
		t.Errorf("坏文件时: %+v %+v", role, why)
	}
}

var errTestRoles = os.ErrInvalid

func TestRoleSummaries(t *testing.T) {
	rs, err := policy.ParseRoleSet([]byte(testRoles))
	if err != nil {
		t.Fatal(err)
	}
	got := RoleSummaries(rs)
	want := []proto.RoleSummary{
		{Name: "写作", Note: "只改文档", Ceiling: "write_in_project", ForbidCount: 2, TrustMaxMins: 120, Dirs: []string{"~/docs", "~/shared/**"}},
		{Name: "开发", Ceiling: "network", TrustMaxMins: -1, Dirs: []string{"~/shared/**", "~/code"}},
		{Name: "运维", Ceiling: "destructive"},
	}
	if len(got) != len(want) {
		t.Fatalf("得到 %+v", got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Name != w.Name || g.Note != w.Note || g.Ceiling != w.Ceiling || g.ForbidCount != w.ForbidCount || g.TrustMaxMins != w.TrustMaxMins || strings.Join(g.Dirs, ",") != strings.Join(w.Dirs, ",") {
			t.Errorf("第 %d 个: %+v，想要 %+v", i, g, w)
		}
	}
	if RoleSummaries(policy.RoleSet{}) != nil {
		t.Error("没有角色时应为 nil（hello 里省略）")
	}
}

func TestRoleTemplate(t *testing.T) {
	for _, en := range []bool{false, true} {
		rs, err := policy.ParseRoleSet(RoleTemplate(en))
		if err != nil {
			t.Fatalf("en=%v 模板解析失败: %v", en, err)
		}
		if len(rs.Roles) != 3 {
			t.Fatalf("en=%v 模板应有三个角色", en)
		}
		names := rs.Roles[0].Name + rs.Roles[1].Name + rs.Roles[2].Name
		if want := map[bool]string{false: "开发写作运维", true: "devwriterops"}[en]; names != want {
			t.Errorf("en=%v 角色名 %q，想要 %q", en, names, want)
		}
		// 模板写下去不该立刻改变任何会话：dirs 全部注释掉，只有 RAILSE_ROLE 指名时才生效
		for _, r := range rs.Roles {
			if len(r.Dirs) != 0 {
				t.Errorf("en=%v 模板角色 %s 不该带 dirs", en, r.Name)
			}
		}
	}
}

// 内容变了而 mtime 与大小都没变：照样重读（按内容比较，不靠 mtime）。
func TestRolesReloadSameMtimeAndSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.yaml")
	r := NewRoles(path)
	a := "version: 1\nroles:\n  - name: aa\n    ceiling: read\n"
	b := "version: 1\nroles:\n  - name: bb\n    ceiling: read\n"
	at := time.Now().Add(-time.Hour).Truncate(time.Second)
	os.WriteFile(path, []byte(a), 0o600)
	os.Chtimes(path, at, at)
	if st := r.Current(); st.Set.Find("aa") == nil {
		t.Fatalf("第一次: %+v", st)
	}
	os.WriteFile(path, []byte(b), 0o600)
	os.Chtimes(path, at, at)
	if st := r.Current(); st.Set.Find("bb") == nil || st.Set.Find("aa") != nil {
		t.Fatalf("内容变了应重读: %+v", st)
	}
}

// 读得到的好文件变成读不了：进入有误状态，不沿用旧的。
func TestRolesBecomesUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 读得了 0000 的文件")
	}
	path := filepath.Join(t.TempDir(), "roles.yaml")
	os.WriteFile(path, []byte("version: 1\nroles:\n  - name: a\n"), 0o600)
	r := NewRoles(path)
	if st := r.Current(); st.Err != nil {
		t.Fatal(st.Err)
	}
	os.Chmod(path, 0)
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	if st := r.Current(); !st.Exists || st.Err == nil || len(st.Set.Roles) != 0 {
		t.Fatalf("读不了应按有误: %+v", st)
	}
}

// 并发重读时通知按代数递增送达，最后一次通知就是文件的最终内容（roles 帧不会倒序）。
func TestRolesConcurrentReloadOrdered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roles.yaml")
	r := NewRoles(path)
	r.Current()
	var mu sync.Mutex
	var gens []uint64
	var last RolesState
	r.SetChanged(func(st RolesState) {
		mu.Lock()
		gens = append(gens, st.Gen)
		last = st
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range 30 {
				if i == 0 {
					os.WriteFile(path, []byte(fmt.Sprintf("version: 1\nroles:\n  - name: r%d\n", j)), 0o600)
				}
				r.Current()
			}
		}()
	}
	wg.Wait()
	final := r.Current()
	mu.Lock()
	defer mu.Unlock()
	for i := 1; i < len(gens); i++ {
		if gens[i] <= gens[i-1] {
			t.Fatalf("通知的代数应递增: %v", gens)
		}
	}
	if len(gens) == 0 || last.Gen != final.Gen || last.Set.Roles[0].Name != final.Set.Roles[0].Name {
		t.Fatalf("最后一次通知应是最终内容: last=%+v final=%+v", last, final)
	}
}
