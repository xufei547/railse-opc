//go:build unix

package agent

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// roles.yaml 不是普通文件（FIFO、目录）或超过 256 KiB：直接判有误，不去读（FIFO 读了会卡住）。
func TestRolesNotRegularOrTooBig(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo.yaml")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skip("建不了 FIFO:", err)
	}
	done := make(chan RolesState, 1)
	go func() { done <- NewRoles(fifo).Current() }()
	select {
	case st := <-done:
		if !st.Exists || st.Err == nil || !strings.Contains(st.Err.Error(), "不是普通文件") {
			t.Fatalf("FIFO 应判有误: %+v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("读 FIFO 卡住了")
	}
	big := filepath.Join(dir, "big.yaml")
	os.WriteFile(big, []byte("version: 1\nroles: []\n# "+strings.Repeat("x", 256<<10)+"\n"), 0o600)
	if st := NewRoles(big).Current(); !st.Exists || st.Err == nil || !strings.Contains(st.Err.Error(), "256 KiB") {
		t.Fatalf("超过 256 KiB 应判有误: %+v", st)
	}
	d := filepath.Join(dir, "d.yaml")
	os.Mkdir(d, 0o700)
	if st := LoadRolesFile(d); !st.Exists || st.Err == nil || !strings.Contains(st.Err.Error(), "不是普通文件") {
		t.Fatalf("目录应判有误: %+v", st)
	}
	// 指向普通文件的符号链接照常读
	real := filepath.Join(dir, "real.yaml")
	os.WriteFile(real, []byte("version: 1\nroles:\n  - name: a\n"), 0o600)
	link := filepath.Join(dir, "link.yaml")
	os.Symlink(real, link)
	if st := LoadRolesFile(link); st.Err != nil || st.Set.Find("a") == nil {
		t.Fatalf("符号链接到普通文件应照常: %+v", st)
	}
}
