package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/railse/railse-opc/internal/policy"
)

func TestEnsureDefaultRulesAndRuleSet(t *testing.T) {
	s := openTemp(t)
	v, err := s.EnsureDefaultRules()
	if err != nil || v != 1 {
		t.Fatalf("EnsureDefaultRules = %d, %v", v, err)
	}
	if v2, _ := s.EnsureDefaultRules(); v2 != 1 {
		t.Fatalf("已有规则时不应重复导入: %d", v2)
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
	s.EnsureDefaultRules()
	r := policy.Rule{ID: "builtin-readonly", Source: policy.SourceUser, Match: policy.Match{Payload: policy.PayloadShell, CommandPrefix: "git push origin"}, Action: policy.ActionAllow}
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
	if len(rows) != 3 || rows[2].Source != "user" || rows[2].Enabled {
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
	s.EnsureDefaultRules()
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
	s.EnsureDefaultRules()
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
	s.EnsureDefaultRules()
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

// 同一事务里入库的规则 created_at 可能相同，列出顺序仍按入库顺序，不按 id 字典序。
func TestListRulesKeepsInsertOrder(t *testing.T) {
	s := openTemp(t)
	if _, err := s.ReplaceRules(policy.RuleSet{Version: 1, Rules: []policy.Rule{userRule("zeta"), userRule("alpha"), userRule("mid")}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE rules SET created_at = ?", Now()); err != nil {
		t.Fatal(err)
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
		t.Fatalf("顺序 = %v", ids)
	}
}

// 版本号损坏时报错，不悄悄从 0 重新计数（否则守护进程会误以为已是最新）。
func TestRulesVersionCorrupt(t *testing.T) {
	s := openTemp(t)
	s.EnsureDefaultRules()
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
