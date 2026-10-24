//go:build !unix

package main

import "os"

// fileOwner 在非 Unix 系统上拿不到属主（开机自启也只支持 macOS 与 Linux）。
func fileOwner(os.FileInfo) (uid int, ok bool) { return 0, false }
