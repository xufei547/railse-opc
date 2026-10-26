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

// ResolveProjectRoot 定项目根：从清理后的 cwd 向上找含 .git（目录或 worktree 的 .git 文件）的目录，
// 找到且不 tooBroad 时返回它，git 为 true。找不到，或找到的仓库根太宽（用 git 管 dotfiles 的人家目录里有 .git）时
// 退回 cwd 自身：它是存在的目录且不 tooBroad 时返回它，git 为 false；否则（家目录、它的祖先、根目录、不存在）返回空串，
// 引擎会把空的项目根按项目外处理。
func ResolveProjectRoot(cwd string) (root string, git bool) {
	dir := CleanCwd(cwd)
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			if !tooBroad(d) {
				return d, true
			}
			break // 再往上只会更宽
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if st, err := os.Stat(dir); err == nil && st.IsDir() && !tooBroad(dir) {
		return dir, false
	}
	return "", false
}

// FindProjectRoot 返回 ResolveProjectRoot 定的项目根，不管它是不是 git 仓库。
func FindProjectRoot(cwd string) string {
	root, _ := ResolveProjectRoot(cwd)
	return root
}

// tooBroad 报告 dir 是否为根目录、家目录或家目录的祖先（如 /Users）。用 git 管 dotfiles 的人家目录里有 .git，
// 在家目录里起 agent 时 cwd 就是家目录，把它当项目根会让“项目内可写”放行改写整个家目录，所以按项目外处理（fail closed）；
// 祖先比家目录还宽，同理。
// 按文件身份比较而不是比字符串：cwd 不存在时 dir 是未解析写法，macOS 的 firmlink
// （如 /System/Volumes/Data/Users/x）也不被 EvalSymlinks 解析，字符串比较都会漏。
func tooBroad(dir string) bool {
	if filepath.Dir(dir) == dir {
		return true
	}
	st, err := os.Stat(dir)
	if err != nil {
		return true
	}
	if root, err := os.Stat("/"); err == nil && os.SameFile(st, root) {
		return true
	}
	if home := userHome(); home != "" {
		// 家目录本身及其每一级祖先，原写法与解析链接后的写法都比，任一身份相同即太宽。
		for _, h := range []string{filepath.Clean(home), CleanCwd(home)} {
			for p := h; ; p = filepath.Dir(p) {
				if ps, err := os.Stat(p); err == nil && os.SameFile(st, ps) {
					return true
				}
				if filepath.Dir(p) == p {
					break
				}
			}
		}
	}
	return false
}
