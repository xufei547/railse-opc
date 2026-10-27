package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
)

// CleanCwd 解析符号链接并清理路径；解析失败就只清理。
// macOS 上 /var 是 /private/var 的链接，钩子给的 cwd 和规则里的路径必须用同一种写法比较。
func CleanCwd(cwd string) string {
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		return filepath.Clean(r)
	}
	return filepath.Clean(cwd)
}

// RootKind 是 ResolveProjectRoot 定出的项目根类别。
type RootKind int

const (
	// RootNone：没有项目根。cwd 是家目录、家目录的祖先、根目录，不存在、不是目录，或是相对路径。
	RootNone RootKind = iota
	// RootGit：项目根是 cwd 所在的 git 仓库根。
	RootGit
	// RootCwd：不在 git 仓库里，cwd 是家目录下的普通目录（见 plainUnderHome），按 cwd 自身当项目根。
	RootCwd
	// RootNotPlain：cwd 是存在的目录、不在 git 仓库里、不太宽，但不是家目录下的普通目录（家目录之外、点开头的某一段之下、
	// ~/Library 之下），不当项目根。
	RootNotPlain
)

// String 给测试的失败信息与调试输出用。
func (k RootKind) String() string {
	switch k {
	case RootGit:
		return "git"
	case RootCwd:
		return "cwd"
	case RootNotPlain:
		return "not_plain"
	default:
		return "none"
	}
}

// ResolveProjectRoot 定项目根：从清理后的 cwd 向上找含 .git（目录或 worktree 的 .git 文件）的目录，
// 找到且不 tooBroad 时返回它与 RootGit（不论在哪里，/tmp、/opt 下的仓库也是）。找不到，或找到的仓库根太宽
// （用 git 管 dotfiles 的人家目录里有 .git）时看 cwd 自身：不是存在的目录或 tooBroad 时返回空串与 RootNone；
// 是家目录下的普通目录时返回它与 RootCwd；否则返回空串与 RootNotPlain（~/.config、~/Library/LaunchAgents、/etc、
// /usr/local 这类目录里自动放行写入太危险）。空的项目根引擎按项目外处理。
// cwd 要是绝对路径：相对路径会按本进程的工作目录解析，一律返回空串与 RootNone。
func ResolveProjectRoot(cwd string) (root string, kind RootKind) {
	dir := CleanCwd(cwd)
	if !filepath.IsAbs(dir) {
		return "", RootNone
	}
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			if !tooBroad(d) {
				return d, RootGit
			}
			break // 再往上只会更宽
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() || tooBroad(dir) {
		return "", RootNone
	}
	if !plainUnderHome(dir) {
		return "", RootNotPlain
	}
	return dir, RootCwd
}

// plainUnderHome 报告 dir（解析过链接的绝对路径，存在且不太宽）是否在家目录下的普通目录里：从 dir 逐级往上，
// 直到某一级与家目录（homeSpellings 的任一写法）是同一个文件，经过的每一段都不以点开头，紧挨家目录的那一段
// 不是 ~/Library（与 <家目录>/Library 是同一个文件，或名字是 Library，darwin 上不分大小写）。到根都没碰到家目录
// 就是在家目录之外。按文件身份找家目录：dir 可能是 firmlink 写法，家目录可能经过符号链接。取不到家目录时一律不是。
func plainUnderHome(dir string) bool {
	home := userHome()
	if home == "" {
		return false
	}
	var homes []os.FileInfo
	for _, h := range homeSpellings(home) {
		if st, err := os.Stat(h); err == nil {
			homes = append(homes, st)
		}
	}
	isHome := func(p string) bool {
		st, err := os.Stat(p)
		return err == nil && slices.ContainsFunc(homes, func(h os.FileInfo) bool { return os.SameFile(st, h) })
	}
	p := dir
	for {
		name, parent := filepath.Base(p), filepath.Dir(p)
		if parent == p {
			return false // 到根都没碰到家目录
		}
		if strings.HasPrefix(name, ".") {
			return false
		}
		if isHome(parent) {
			break
		}
		p = parent
	}
	// p 是紧挨家目录的那一级
	name := filepath.Base(p)
	if name == "Library" || (runtime.GOOS == "darwin" && strings.EqualFold(name, "Library")) {
		return false
	}
	if lib, err := os.Stat(filepath.Join(home, "Library")); err == nil {
		if st, err := os.Stat(p); err == nil && os.SameFile(st, lib) {
			return false
		}
	}
	return true
}

// FindProjectRoot 返回 ResolveProjectRoot 定的项目根，不管它是哪一类。
func FindProjectRoot(cwd string) string {
	root, _ := ResolveProjectRoot(cwd)
	return root
}

// tooBroad 报告 dir 是否为根目录、家目录或家目录的祖先（如 /Users）。用 git 管 dotfiles 的人家目录里有 .git，
// 在家目录里起 agent 时 cwd 就是家目录，把它当项目根会让“项目内可写”放行改写整个家目录，所以按项目外处理（fail closed）；
// 祖先比家目录还宽，同理。
// 按文件身份比较而不是比字符串：cwd 不存在时 dir 是未解析写法，macOS 的 firmlink
// （如 /System/Volumes/Data/Users/x）也不被 EvalSymlinks 解析，字符串比较都会漏。家目录的写法见 homeSpellings。
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
		// 家目录本身及其每一级祖先，每种写法都比，任一身份相同即太宽。
		for _, h := range homeSpellings(home) {
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

// darwinDataVolume 是 macOS 数据卷的挂载点：/Users/x 在它下面还有一个 firmlink 写法 /System/Volumes/Data/Users/x。
const darwinDataVolume = "/System/Volumes/Data"

// homeSpellings 返回家目录的各种写法：原写法、解析链接后的写法；macOS 上再加数据卷上的 firmlink 写法
// （存在且与家目录是同一个文件时）。firmlink 写法的祖先 /System/Volumes/Data、/System/Volumes、/System
// 和 /Users、/ 都不是同一个文件，只比前两种写法的祖先会漏掉它们。firmlink 写法按原写法与解析链接后的写法各拼一次：
// $HOME 经过符号链接时（/var/root、/tmp/...，macOS 的 /var、/tmp 都链到 /private）只有后者在数据卷上存在。
func homeSpellings(home string) []string {
	plain, resolved := filepath.Clean(home), CleanCwd(home)
	out := []string{plain, resolved}
	if runtime.GOOS == "darwin" {
		hs, err := os.Stat(home)
		if err != nil {
			return out
		}
		for _, h := range []string{plain, resolved} {
			fl := darwinDataVolume + h
			if a, err := os.Stat(fl); err == nil && os.SameFile(a, hs) && !slices.Contains(out, fl) {
				out = append(out, fl)
			}
		}
	}
	return out
}
