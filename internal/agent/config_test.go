package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestConfigInAndEnsureDirs(t *testing.T) {
	dir := filepath.Join(shortTempDir(t), "home")
	c := ConfigIn(dir)
	if c.Dir != dir || c.SocketPath != filepath.Join(dir, "agent.sock") || c.RulesPath != filepath.Join(dir, "rules.yaml") || c.AuditPath != filepath.Join(dir, "audit.jsonl") || c.StatePath != filepath.Join(dir, "state.json") || c.LogPath != filepath.Join(dir, "agent.log") {
		t.Fatalf("路径推导错误: %+v", c)
	}
	if c.MachineID == "" || c.HomeDir == "" || c.Deadline.Seconds() != 86390 {
		t.Fatalf("默认值错误: %+v", c)
	}
	if err := c.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("目录权限 = %v, %v", st.Mode().Perm(), err)
	}
}

func TestDefaultConfigHonorsEnv(t *testing.T) {
	dir := shortTempDir(t)
	t.Setenv("RAILSE_HOME", dir)
	c, err := DefaultConfig()
	if err != nil || c.Dir != dir {
		t.Fatalf("RAILSE_HOME 未生效: %+v, %v", c, err)
	}
}

func TestConfigInHomeDirFallback(t *testing.T) {
	t.Setenv("HOME", "")
	if c := ConfigIn(shortTempDir(t)); c.HomeDir == "" {
		t.Fatalf("$HOME 为空时 HomeDir 不应为空: %+v", c)
	}
}
