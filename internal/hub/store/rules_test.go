package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/policy"
)

// ensureDefaults 导入内置规则并检查错误。
func ensureDefaults(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.EnsureDefaultRules(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDefaultRulesAndRuleSet(t *testing.T) {
	s := openTemp(t)
	v, err := s.EnsureDefaultRules()
	if err != nil || v != 1 {
		t.Fatalf("EnsureDefaultRules = %d, %v", v, err)
	}
	if v2, err := s.EnsureDefaultRules(); err != nil || v2 != 1 {
		t.Fatalf("已有规则时不应重复导入: %d, %v", v2, err)
	}
	rs, v3, err := s.RuleSet()
	if err != nil || v3 != 1 || len(rs.Rules) != 2 {
		t.Fatalf("RuleSet = %d 条, v=%d, %v", len(rs.Rules), v3, err)
	}
	if _, err := policy.NewEngine(rs); err != nil {
		t.Fatalf("导出的规则集应能建引擎: %v", err)
	}
}

func TestInsertToggleDeleteRules(t *testing.T) {
	s := openTemp(t)
	ensureDefaults(t, s)
	r := policy.Rule{ID: "builtin-readonly", Source: policy.SourceUser, Match: policy.Match{Payload: policy.PayloadShell, CommandPrefix: "git push origin"}, Action: policy.ActionAllow, CreatedFrom: "req-1"}
	id, v, err := s.InsertRule(r)
	if err != nil || id != "builtin-readonly-2" || v != 2 {
		t.Fatalf("InsertRule = %q v=%d %v", id, v, err)
	}
	v, err = s.SetRuleEnabled(id, false)
	if err != nil || v != 3 {
		t.Fatalf("SetRuleEnabled v=%d %v", v, err)
	}
	rs, _, _ := s.RuleSet()
	var found *policy.Rule
	for i := range rs.Rules {
		if rs.Rules[i].ID == id {
			found = &rs.Rules[i]
		}
	}
	if found == nil || found.IsEnabled() {
		t.Fatalf("禁用应写进 Enabled: %+v", found)
	}
	rows, _ := s.ListRules()
	if len(rows) != 3 || rows[2].Source != "user" || rows[2].Enabled || rows[2].CreatedFrom != "req-1" || rows[0].CreatedFrom != "" {
		t.Fatalf("ListRules = %+v", rows)
	}
	if err := s.RecordHit(id); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ListRules()
	if rows[2].HitCount != 1 || rows[2].LastHitAt == nil {
		t.Fatalf("RecordHit 未生效: %+v", rows[2])
	}
	if v, err := s.DeleteRule(id); err != nil || v != 4 {
		t.Fatalf("DeleteRule v=%d %v", v, err)
	}
	if _, err := s.DeleteRule(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删不存在的: %v", err)
	}
}

func TestInsertRuleRejectsInvalid(t *testing.T) {
	s := openTemp(t)
	ensureDefaults(t, s)
	bad := policy.Rule{ID: "learned-x", Source: policy.SourceLearned, Match: policy.Match{Payload: policy.PayloadShell, CommandPrefix: "git push"}, Action: policy.ActionAllow}
	if _, _, err := s.InsertRule(bad); err == nil || !strings.Contains(err.Error(), "learned") {
		t.Fatalf("不合法的 learned 规则应被拒: %v", err)
	}
	if rows, _ := s.ListRules(); len(rows) != 2 {
		t.Fatal("被拒的规则不应入库")
	}
}

func TestReplaceRules(t *testing.T) {
	s := openTemp(t)
	ensureDefaults(t, s)
	rs, err := policy.ParseRuleSet([]byte("version: 1\nrules:\n  - id: only\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n"))
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.ReplaceRules(rs)
	if err != nil || v != 2 {
		t.Fatalf("ReplaceRules v=%d %v", v, err)
	}
	got, _, _ := s.RuleSet()
	if len(got.Rules) != 1 || got.Rules[0].ID != "only" {
		t.Fatalf("替换后 = %+v", got.Rules)
	}
}

// userRule 是一条合法的 user 放行规则。
func userRule(id string) policy.Rule {
	return policy.Rule{ID: id, Source: policy.SourceUser, Match: policy.Match{Payload: policy.PayloadShell, CommandPrefix: "make " + id}, Action: policy.ActionAllow}
}

// 不合法的整套替换不改库也不加版本；不存在的 id 启停、删除都返回 ErrNotFound 且不加版本。
func TestRuleWritesRejectWithoutBump(t *testing.T) {
	s := openTemp(t)
	if v, err := s.RulesVersion(); err != nil || v != 0 {
		t.Fatalf("空库版本 = %d, %v", v, err)
	}
	ensureDefaults(t, s)
	if _, err := s.ReplaceRules(policy.RuleSet{Version: 1, Rules: []policy.Rule{userRule("a"), userRule("a")}}); err == nil {
		t.Fatal("id 重复的规则集应被拒")
	}
	if _, err := s.SetRuleEnabled("nope", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("启停不存在的: %v", err)
	}
	if _, err := s.DeleteRule("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删不存在的: %v", err)
	}
	if _, _, err := s.InsertRule(policy.Rule{Source: policy.SourceUser, Match: policy.Match{Domain: "a.com"}, Action: policy.ActionAllow}); err == nil {
		t.Fatal("缺 id 的规则应被拒")
	}
	rs, v, err := s.RuleSet()
	if err != nil || v != 1 || len(rs.Rules) != 2 {
		t.Fatalf("被拒的写入不应改库或加版本: %d 条, v=%d, %v", len(rs.Rules), v, err)
	}
}

// enabled 列是启停的唯一真相：入库的 YAML 不带 enabled，导入时的 enabled: false 写进列。
func TestRuleEnabledColumnIsTruth(t *testing.T) {
	s := openTemp(t)
	off := false
	disabled := userRule("off")
	disabled.Enabled = &off
	if _, err := s.ReplaceRules(policy.RuleSet{Version: 1, Rules: []policy.Rule{userRule("on"), disabled}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRuleEnabled("on", false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetRuleEnabled("off", true); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRules()
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListRules = %+v, %v", rows, err)
	}
	for _, row := range rows {
		if strings.Contains(row.YAML, "enabled") {
			t.Fatalf("入库的 YAML 不应带 enabled: %q", row.YAML)
		}
	}
	if rows[0].ID != "on" || rows[0].Enabled || rows[1].ID != "off" || !rows[1].Enabled {
		t.Fatalf("启停应以列为准: %+v", rows)
	}
	rs, _, _ := s.RuleSet()
	if rs.Rules[0].Enabled == nil || *rs.Rules[0].Enabled || rs.Rules[1].Enabled == nil || !*rs.Rules[1].Enabled {
		t.Fatalf("RuleSet 的 Enabled 应来自列: %+v", rs.Rules)
	}
}

// 列出顺序按入库顺序（rowid），不按 id 字典序，也不依赖墙钟：created_at 相同或倒退（时钟回拨）都不影响。
func TestListRulesKeepsInsertOrder(t *testing.T) {
	s := openTemp(t)
	if _, err := s.ReplaceRules(policy.RuleSet{Version: 1, Rules: []policy.Rule{userRule("zeta"), userRule("alpha")}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InsertRule(userRule("mid")); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, c := range []struct{ name, zeta, alpha, mid string }{
		{"相同", Now(), Now(), Now()},
		{"倒退", FormatTime(now.Add(time.Hour)), FormatTime(now), FormatTime(now.Add(-time.Hour))},
	} {
		for id, ts := range map[string]string{"zeta": c.zeta, "alpha": c.alpha, "mid": c.mid} {
			if _, err := s.db.Exec("UPDATE rules SET created_at = ? WHERE id = ?", ts, id); err != nil {
				t.Fatal(err)
			}
		}
		rs, _, err := s.RuleSet()
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, r := range rs.Rules {
			ids = append(ids, r.ID)
		}
		if strings.Join(ids, ",") != "zeta,alpha,mid" {
			t.Fatalf("created_at %s: 顺序 = %v", c.name, ids)
		}
	}
}

// 版本号损坏时报错，不悄悄从 0 重新计数（否则守护进程会误以为已是最新）。
func TestRulesVersionCorrupt(t *testing.T) {
	s := openTemp(t)
	ensureDefaults(t, s)
	if err := s.SetSetting("rules_version", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RulesVersion(); err == nil {
		t.Fatal("RulesVersion 应报错")
	}
	if _, _, err := s.RuleSet(); err == nil {
		t.Fatal("RuleSet 应报错")
	}
	if _, _, err := s.InsertRule(userRule("a")); err == nil {
		t.Fatal("InsertRule 应报错")
	}
	if rows, _ := s.ListRules(); len(rows) != 2 {
		t.Fatalf("失败的插入不应留行: %d", len(rows))
	}
}

// 并发插入同一 id：读整套、选 id、校验、插入、加版本在一个事务里，全部成功、id 各不相同、版本恰好加 N；
// 同时读到的快照里规则条数与版本号始终一致（每次插入恰好加一条、版本加一）。
func TestInsertRuleConcurrentSameID(t *testing.T) {
	s := openTemp(t)
	base, err := s.EnsureDefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		ids      = map[string]bool{}
		versions = map[int]bool{}
		errs     []error
		done     = make(chan struct{})
		readErr  = make(chan error, 1)
	)
	go func() {
		defer close(readErr)
		for {
			select {
			case <-done:
				return
			default:
			}
			rs, v, err := s.RuleSet()
			if err == nil && len(rs.Rules) != 2+v-base {
				err = fmt.Errorf("快照不一致: %d 条规则, 版本 %d", len(rs.Rules), v)
			}
			if err != nil {
				readErr <- err
				return
			}
		}
	}()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, v, err := s.InsertRule(userRule("dup"))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			ids[id] = true
			versions[v] = true
		}()
	}
	wg.Wait()
	close(done)
	if err := <-readErr; err != nil {
		t.Fatal(err)
	}
	if len(errs) != 0 {
		t.Fatalf("并发插入出错: %v", errs)
	}
	if len(ids) != n || !ids["dup"] || !ids[fmt.Sprintf("dup-%d", n)] {
		t.Fatalf("id = %v", ids)
	}
	if len(versions) != n {
		t.Fatalf("各次插入应得到不同版本: %v", versions)
	}
	if v, err := s.RulesVersion(); err != nil || v != base+n {
		t.Fatalf("版本 = %d, %v，应为 %d", v, err, base+n)
	}
}

// 两个进程（两个连接）同时首次启动，默认规则只导入一次。
func TestEnsureDefaultRulesConcurrentOpen(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	var stores []*Store
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		stores = append(stores, s)
	}
	var wg sync.WaitGroup
	versions := make([]int, len(stores))
	errs := make([]error, len(stores))
	for i, s := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			versions[i], errs[i] = s.EnsureDefaultRules()
		}()
	}
	wg.Wait()
	for i := range stores {
		if errs[i] != nil || versions[i] != 1 {
			t.Fatalf("store %d: v=%d, %v", i, versions[i], errs[i])
		}
	}
	rows, err := stores[0].ListRules()
	if err != nil || len(rows) != 2 {
		t.Fatalf("默认规则应只导入一次: %d, %v", len(rows), err)
	}
}

// 默认规则只在规则版本为 0（从未初始化）时导入：删光或导入空集后再启动，不悄悄恢复内置放行规则。
func TestEnsureDefaultRulesOnlyWhenUninitialized(t *testing.T) {
	s := openTemp(t)
	if v, err := s.EnsureDefaultRules(); err != nil || v != 1 {
		t.Fatalf("EnsureDefaultRules = %d, %v", v, err)
	}
	for _, id := range []string{"builtin-readonly", "builtin-write-in-project"} {
		if _, err := s.DeleteRule(id); err != nil {
			t.Fatal(err)
		}
	}
	if v, err := s.EnsureDefaultRules(); err != nil || v != 3 {
		t.Fatalf("删光后 EnsureDefaultRules = %d, %v，版本应不变", v, err)
	}
	if rows, err := s.ListRules(); err != nil || len(rows) != 0 {
		t.Fatalf("删光后不应重新导入: %d, %v", len(rows), err)
	}
	if _, err := s.ReplaceRules(policy.DefaultRuleSet()); err != nil {
		t.Fatal(err)
	}
	if v, err := s.ReplaceRules(policy.RuleSet{Version: policy.RuleSetVersion}); err != nil || v != 5 {
		t.Fatalf("导入空集 v=%d, %v", v, err)
	}
	if v, err := s.EnsureDefaultRules(); err != nil || v != 5 {
		t.Fatalf("导入空集后 EnsureDefaultRules = %d, %v，版本应不变", v, err)
	}
	if rows, err := s.ListRules(); err != nil || len(rows) != 0 {
		t.Fatalf("导入空集后不应重新导入: %d, %v", len(rows), err)
	}
}

// 存入后经 RuleSet 读回整条规则不变：scope（project、machine）、Risk 指针、command_regex 等匹配字段、created_from，
// Enabled 来自 enabled 列。ReplaceRules 与 InsertRule 两条入库路径都覆盖。
func TestRuleRoundTrip(t *testing.T) {
	s := openTemp(t)
	read, outward, destructive := policy.RiskRead, policy.RiskOutward, policy.RiskDestructive
	on, off := true, false
	want := []policy.Rule{
		{ID: "proj", Source: policy.SourceUser, Scope: policy.Scope{Project: "~/code/**"},
			Match: policy.Match{Payload: policy.PayloadShell, Risk: &read, CommandRegex: `^make( |$)`}, Action: policy.ActionAllow, Enabled: &on, CreatedFrom: "req-1"},
		{ID: "mach", Source: policy.SourceUser, Scope: policy.Scope{Machine: "m1"},
			Match: policy.Match{Payload: policy.PayloadShell, Risk: &outward, CommandPrefix: "git push origin"}, Action: policy.ActionAllow, Enabled: &off, CreatedFrom: "req-2"},
		{ID: "mcp", Source: policy.SourceBuiltin,
			Match: policy.Match{Payload: policy.PayloadMCP, MCPServer: "github", MCPTool: "create_issue"}, Action: policy.ActionAsk, Enabled: &on},
		{ID: "net", Source: policy.SourceUser,
			Match: policy.Match{Payload: policy.PayloadNetwork, Domain: "evil.example.com", Risk: &destructive}, Action: policy.ActionDeny, Enabled: &on},
		{ID: "glob", Source: policy.SourceUser, Scope: policy.Scope{Project: "/srv/app"},
			Match: policy.Match{Payload: policy.PayloadFileWrite, PathGlob: "**/*.go"}, Action: policy.ActionAllow, Enabled: &off},
	}
	if _, err := s.ReplaceRules(policy.RuleSet{Version: policy.RuleSetVersion, Rules: want}); err != nil {
		t.Fatal(err)
	}
	learned := policy.Rule{ID: "learned-npm", Source: policy.SourceLearned, Scope: policy.Scope{Project: "~/code/web"},
		Match: policy.Match{Payload: policy.PayloadShell, Risk: &read, CommandPrefix: "npm test"}, Action: policy.ActionAllow, CreatedFrom: "req-3"}
	if _, _, err := s.InsertRule(learned); err != nil {
		t.Fatal(err)
	}
	learned.Enabled = &on // 未给 Enabled 时按启用入库，读回为 &true
	want = append(want, learned)
	rs, _, err := s.RuleSet()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rs.Rules, want) {
		t.Fatalf("读回的规则与存入的不同:\n得到 %+v\n应为 %+v", rs.Rules, want)
	}
	rows, err := s.ListRules()
	if err != nil || len(rows) != len(want) {
		t.Fatalf("ListRules = %d 行, %v", len(rows), err)
	}
	for i, row := range rows {
		if row.ID != want[i].ID || row.CreatedFrom != want[i].CreatedFrom || row.Source != string(want[i].Source) {
			t.Fatalf("第 %d 行 = %+v，应为 id %s、created_from %q", i, row, want[i].ID, want[i].CreatedFrom)
		}
	}
}

// learned 与 builtin 来源的规则不能放行 outward、destructive：两条入库路径都拒绝，不改库、不加版本。
func TestRulesRejectNonUserElevatedAllow(t *testing.T) {
	s := openTemp(t)
	base, err := s.EnsureDefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.ListRules()
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range []policy.Source{policy.SourceLearned, policy.SourceBuiltin} {
		for _, risk := range []policy.Risk{policy.RiskOutward, policy.RiskDestructive} {
			r := policy.Rule{ID: "elevated", Source: src, Match: policy.Match{Payload: policy.PayloadShell, Risk: &risk, CommandPrefix: "git push"}, Action: policy.ActionAllow}
			if _, err := s.ReplaceRules(policy.RuleSet{Version: policy.RuleSetVersion, Rules: []policy.Rule{r}}); err == nil {
				t.Errorf("ReplaceRules 应拒绝 %s 放行 %s", src, risk)
			}
			if _, _, err := s.InsertRule(r); err == nil {
				t.Errorf("InsertRule 应拒绝 %s 放行 %s", src, risk)
			}
		}
	}
	after, err := s.ListRules()
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("被拒的写入不应改库: %+v, %v", after, err)
	}
	if v, err := s.RulesVersion(); err != nil || v != base {
		t.Fatalf("被拒的写入不应加版本: %d, %v", v, err)
	}
}

// 库里的规则 YAML 读回时严格解码并整套校验：拼错的键或不合法的规则报错，不会悄悄放宽成更宽的规则。
func TestRuleSetRejectsBadStoredYAML(t *testing.T) {
	cases := map[string]string{
		// 非严格解码会丢掉拼错的 comand_prefix，变成放行全部只读命令
		"未知字段":               "id: only\nsource: user\nscope: global\nmatch:\n  risk: read\n  comand_prefix: git status\naction: allow\n",
		"顶层未知字段":             "id: only\nsource: user\nscope: global\nmatch:\n  risk: read\naction: allow\nextra: 1\n",
		"learned 放行 outward": "id: only\nsource: learned\nscope: global\nmatch:\n  payload: shell\n  risk: outward\n  command_prefix: git push\naction: allow\n",
		"空 match":            "id: only\nsource: user\nscope: global\naction: allow\n",
		"空 YAML":             "",
	}
	for name, y := range cases {
		t.Run(name, func(t *testing.T) {
			s := openTemp(t)
			if _, err := s.ReplaceRules(policy.RuleSet{Version: policy.RuleSetVersion, Rules: []policy.Rule{userRule("only")}}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec("UPDATE rules SET yaml = ? WHERE id = 'only'", y); err != nil {
				t.Fatal(err)
			}
			if rs, _, err := s.RuleSet(); err == nil {
				t.Fatalf("RuleSet 应报错，得到 %+v", rs.Rules)
			}
			if _, _, err := s.InsertRule(userRule("other")); err == nil {
				t.Fatal("InsertRule 读整套时也应报错")
			}
		})
	}
}
