//go:build linux || darwin

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

// openDirNoFollow 打开一个目录，最后一段是符号链接时失败（O_NOFOLLOW）；之后经这个 fd 核对、chown、chmod，
// 不会因为路径在中途被换成符号链接而改到别的文件上。
func openDirNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
}
