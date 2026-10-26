//go:build !unix

package doctor

// fileOwner 在没有 uid 的系统上查不到属主。
func fileOwner(string) (int, bool) { return 0, false }
