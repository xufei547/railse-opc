//go:build !linux && !darwin

package main

import (
	"errors"
	"os"

	"github.com/xufei547/railse-opc/internal/i18n"
)

// 开机自启只支持 macOS 与 Linux：其余系统上拿不到属主，也不打开目录。
func fileOwner(os.FileInfo) (uid int, ok bool) { return 0, false }

func openDirNoFollow(string) (*os.File, error) {
	return nil, errors.New(i18n.T("开机自启只支持 macOS 与 Linux"))
}
