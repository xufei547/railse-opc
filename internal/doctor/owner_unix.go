//go:build unix

package doctor

import (
	"os"
	"syscall"
)

// fileOwner 返回 path 的属主 uid（跟随符号链接）。
func fileOwner(path string) (int, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(sys.Uid), true
}
