package agent

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/railse/railse-opc/internal/policy"
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
