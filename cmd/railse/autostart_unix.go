//go:build unix

package main

import (
	"os"
	"syscall"
)

// fileOwner 取文件属主的 uid；拿不到时 ok 为 false。
func fileOwner(fi os.FileInfo) (uid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
