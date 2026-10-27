//go:build !unix

package agent

import "os"

// ownedByMe 在没有 uid 的系统上一律为假：临时目录与家目录都不从系统目录里除外（fail closed）。
func ownedByMe(os.FileInfo) bool { return false }
