package agent

import (
	"os"
	"path/filepath"
)

// CleanCwd 解析符号链接并清理路径；解析失败就只清理。
// macOS 上 /var 是 /private/var 的链接，钩子给的 cwd 和规则里的路径必须用同一种写法比较。
func CleanCwd(cwd string) string {
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(cwd)
}

// FindProjectRoot 从 cwd 向上找含 .git（目录或 worktree 的 .git 文件）的目录。
// 找不到返回空串，引擎会把空的项目根按项目外处理。
func FindProjectRoot(cwd string) string {
	dir := CleanCwd(cwd)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
