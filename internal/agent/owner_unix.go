//go:build unix

package agent

import (
	"os"
	"syscall"
)

// ownedByMe 报告 st 的属主是不是本进程的有效用户。
func ownedByMe(st os.FileInfo) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(sys.Uid) == os.Geteuid()
}
