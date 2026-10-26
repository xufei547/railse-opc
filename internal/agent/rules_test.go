package agent

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/xufei547/railse-opc/internal/policy"
)

func TestLoadRulesCreatesDefaultFile(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, err := LoadRules(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("规则文件应被创建且权限 0600: %v %v", st, err)
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "# ") || !strings.Contains(string(b), "builtin-readonly") {
		t.Fatalf("内容应带注释头并含内置规则:\n%s", b)
	}
	if r.Count() != 2 || r.Path() != path {
		t.Fatalf("Count=%d Path=%s", r.Count(), r.Path())
	}
	d := r.Engine().Evaluate(policy.Payload{Type: policy.PayloadShell, Command: "ls", Cwd: "/p"}, policy.Context{ProjectRoot: "/p", HomeDir: "/h"})
	if d.Action != policy.ActionAllow {
		t.Fatalf("内置规则应放行 ls: %+v", d)
	}
}

func TestReloadKeepsOldOnError(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, err := LoadRules(path)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("version: 9\nrules: []\n"), 0o600)
	if err := r.Reload(); err == nil {
		t.Fatal("坏文件应报错")
	}
	if r.Count() != 2 {
		t.Fatalf("重载失败后应保留旧规则, Count=%d", r.Count())
	}
	os.WriteFile(path, []byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"), 0o600)
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if r.Count() != 1 {
		t.Fatalf("重载后 Count=%d", r.Count())
	}
	d := r.Engine().Evaluate(policy.Payload{Type: policy.PayloadShell, Command: "rm x", Cwd: "/p"}, policy.Context{ProjectRoot: "/p", HomeDir: "/h"})
	if d.Action != policy.ActionDeny {
		t.Fatalf("新规则应生效: %+v", d)
	}
}

func TestPausedSurvivesReload(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, _ := LoadRules(path)
	r.SetPaused(true)
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if !r.Paused() || !r.Engine().Paused() {
		t.Fatal("重载后应仍处于暂停")
	}
	d := r.Engine().Evaluate(policy.Payload{Type: policy.PayloadShell, Command: "ls", Cwd: "/p"}, policy.Context{ProjectRoot: "/p", HomeDir: "/h"})
	if d.Action != policy.ActionAsk {
		t.Fatalf("暂停时应 ask: %+v", d)
	}
}

func TestLoadRulesKeepsExistingFile(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	content := []byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := LoadRules(path)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(content) || r.Count() != 1 {
		t.Fatalf("已有规则文件不应被覆盖, Count=%d:\n%s", r.Count(), b)
	}
}

// 并发冒烟：Reload、SetPaused 与读取方交错，-race 下无数据竞争，且每轮结束后总开关与引擎一致。
// 交错丢失暂停的窗口只有几纳秒，这里不保证能复现，正确性靠 Reload 与 SetPaused 共用写锁。
func TestPausedConsistentUnderConcurrentReload(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, err := LoadRules(path)
	if err != nil {
		t.Fatal(err)
	}
	p := policy.Payload{Type: policy.PayloadShell, Command: "ls", Cwd: "/p"}
	ctx := policy.Context{ProjectRoot: "/p", HomeDir: "/h"}
	for round := 0; round < 200; round++ {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if err := r.Reload(); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			r.Engine().Evaluate(p, ctx)
			r.Count()
		}()
		r.SetPaused(round%2 == 0)
		wg.Wait()
		if r.Paused() != r.Engine().Paused() {
			t.Fatalf("第 %d 轮: Rules.Paused=%v 而引擎 Paused=%v", round, r.Paused(), r.Engine().Paused())
		}
	}
}

func TestLoadRulesTightensExistingFileMode(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	content := []byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil { // 不受 umask 影响，确保起点是 0644
		t.Fatal(err)
	}
	r, err := LoadRules(path)
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 || r.Count() != 1 {
		t.Fatalf("已有规则文件应收紧为 0600 并正常加载: mode=%v Count=%d", st.Mode().Perm(), r.Count())
	}
}

func TestNormalizeRulePaths(t *testing.T) {
	base := shortTempDir(t)
	real := filepath.Join(base, "real")
	os.MkdirAll(filepath.Join(real, "proj"), 0o755)
	if err := os.Symlink(real, filepath.Join(base, "link")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	realResolved, _ := filepath.EvalSymlinks(real)
	link := filepath.Join(base, "link")
	comma := filepath.Join(base, "a,b")
	os.Symlink(real, comma)
	risk := policy.RiskWriteInProject
	rule := func(id string, action policy.Action, project, glob string) policy.Rule {
		r := policy.Rule{ID: id, Source: policy.SourceUser, Scope: policy.Scope{Project: project}, Action: action, Match: policy.Match{PathGlob: glob}}
		if glob == "" {
			r.Match = policy.Match{Risk: &risk}
		}
		return r
	}
	rs := policy.RuleSet{Version: 1, Rules: []policy.Rule{
		rule("link-scope", policy.ActionDeny, link+"/proj", ""),
		rule("link-scope-glob", policy.ActionAllow, link+"/*", ""),
		rule("link-scope-missing", policy.ActionAsk, link+"/later/proj", ""),
		rule("home-scope", policy.ActionDeny, "~/link/proj", ""),
		rule("real-scope", policy.ActionDeny, realResolved+"/proj", ""),
		rule("relative-scope", policy.ActionDeny, "**/proj", ""),
		rule("missing-scope", policy.ActionDeny, "/nonexistent-railse/x/**", ""),
		rule("deny-glob", policy.ActionDeny, "", link+"/proj/**/*.env"),
		rule("ask-glob", policy.ActionAsk, "", link+"/proj/secret.txt"),
		rule("allow-glob", policy.ActionAllow, "", link+"/proj/**"),
		rule("relative-glob", policy.ActionDeny, "", "**/.env"),
		rule("comma-glob", policy.ActionDeny, "", comma+"/proj/**"),
	}}
	var logs []string
	normalizeRulePaths(&rs, base, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	want := map[string][2]string{
		"link-scope":         {realResolved + "/proj", ""},
		"link-scope-glob":    {realResolved + "/*", ""},
		"link-scope-missing": {realResolved + "/later/proj", ""},
		"home-scope":         {realResolved + "/proj", ""},
		"real-scope":         {realResolved + "/proj", ""},
		"relative-scope":     {"**/proj", ""},
		"missing-scope":      {"/nonexistent-railse/x/**", ""},
		"deny-glob":          {"", "{" + link + "/proj," + realResolved + "/proj}/**/*.env"},
		"ask-glob":           {"", "{" + link + "/proj/secret.txt," + realResolved + "/proj/secret.txt}"},
		"allow-glob":         {"", link + "/proj/**"},
		"relative-glob":      {"", "**/.env"},
		"comma-glob":         {"", comma + "/proj/**"}, // 含逗号无法写成 {a,b}，保持原样并记日志
	}
	for _, r := range rs.Rules {
		w := want[r.ID]
		if r.Scope.Project != w[0] || r.Match.PathGlob != w[1] {
			t.Errorf("%s: project=%q glob=%q, 想要 %q %q", r.ID, r.Scope.Project, r.Match.PathGlob, w[0], w[1])
		}
	}
	// link-scope、link-scope-glob、link-scope-missing、home-scope、deny-glob、ask-glob 被改写，各记一条，
	// comma-glob 放弃改写也记一条；已是真实路径、相对模式、不存在的路径和 allow 的 path_glob 都不动也不记
	if len(logs) != 7 {
		t.Errorf("应记 7 条日志, 得到 %d: %q", len(logs), logs)
	}
	if _, err := policy.NewEngine(rs); err != nil {
		t.Fatalf("改写后的规则应仍能建引擎: %v", err)
	}
}

func TestReloadLogsRewrittenRulePaths(t *testing.T) {
	base := shortTempDir(t)
	real := filepath.Join(base, "real")
	os.MkdirAll(real, 0o755)
	if err := os.Symlink(real, filepath.Join(base, "link")); err != nil {
		t.Skipf("无法创建符号链接: %v", err)
	}
	path := filepath.Join(base, "rules.yaml")
	os.WriteFile(path, []byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: {project: \""+filepath.Join(base, "link")+"\"}\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"), 0o600)
	var buf strings.Builder
	if _, err := LoadRulesWithLogger(path, log.New(&buf, "", 0)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "deny-rm") || !strings.Contains(buf.String(), "scope.project") {
		t.Fatalf("改写规则路径应记日志: %q", buf.String())
	}
}

// 终审 I3：首次用 hub 规则覆盖本机 rules.yaml（文件头不是“# 由 hub 下发”）前先备份到同目录 rules.local-<UTC 时间>.yaml（0600），
// 内容与原文件一致，记日志并写明怎么导入；之后的覆盖（文件已是 hub 文件头）不再备份。
func TestReplaceBacksUpLocalRulesOnce(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "rules.yaml")
	local := "# 我手写的规则\nversion: 1\nrules:\n  - id: deny-migrations\n    source: user\n    scope: global\n    match: {payload: file_write, path_glob: \"**/migrations/**\"}\n    action: deny\n"
	if err := os.WriteFile(path, []byte(local), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	r, err := LoadRulesWithLogger(path, log.New(&logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	backups := func() []string {
		t.Helper()
		m, err := filepath.Glob(filepath.Join(dir, "rules.local-*.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	for v := 1; v <= 3; v++ {
		if err := r.Replace(policy.DefaultRuleSet(), v); err != nil {
			t.Fatal(err)
		}
		if n := len(backups()); n != 1 {
			t.Fatalf("第 %d 次覆盖后应恰有 1 份备份，得到 %d", v, n)
		}
	}
	bk := backups()[0]
	if !regexp.MustCompile(`^rules\.local-\d{8}T\d{6}Z\.yaml$`).MatchString(filepath.Base(bk)) {
		t.Fatalf("备份文件名: %s", filepath.Base(bk))
	}
	if b, _ := os.ReadFile(bk); string(b) != local {
		t.Fatalf("备份内容应与原文件一致:\n%s", b)
	}
	if st, err := os.Stat(bk); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("备份权限应为 0600: %v %v", st, err)
	}
	if b, _ := os.ReadFile(path); !strings.HasPrefix(string(b), "# 由 hub 下发，版本 3。") {
		t.Fatalf("rules.yaml 应已是 hub 的缓存:\n%s", b)
	}
	if out := logs.String(); !strings.Contains(out, bk) || !strings.Contains(out, "/api/rules/import") || strings.Count(out, "已备份") != 1 {
		t.Fatalf("应记一条备份日志，带备份路径与导入方法: %q", out)
	}

	// 已是 hub 文件头的缓存（例如重启后）再被覆盖时不备份
	dir2 := shortTempDir(t)
	path2 := filepath.Join(dir2, "rules.yaml")
	b, _ := policy.DefaultRuleSet().Marshal()
	os.WriteFile(path2, append([]byte(fmt.Sprintf(hubRulesHeader, 5)), b...), 0o600)
	r2, err := LoadRules(path2)
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Replace(policy.DefaultRuleSet(), 6); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir2, "rules.local-*")); len(m) != 0 {
		t.Fatalf("已是 hub 下发的缓存不应备份: %v", m)
	}
}

// HasLocalRules 报告 rules.yaml 存在且不是 hub 下发的缓存（railse agent enroll 据此提示先导入）。
func TestHasLocalRules(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "rules.yaml")
	if HasLocalRules(path) {
		t.Fatal("文件不存在时不算有本机规则")
	}
	os.WriteFile(path, []byte("version: 1\nrules: []\n"), 0o600)
	if !HasLocalRules(path) {
		t.Fatal("手写的规则文件应算本机规则")
	}
	os.WriteFile(path, []byte(fmt.Sprintf(hubRulesHeader, 2)+"version: 1\nrules: []\n"), 0o600)
	if HasLocalRules(path) {
		t.Fatal("hub 下发的缓存不算本机规则")
	}
}
