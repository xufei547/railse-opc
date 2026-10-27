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
	// RootGit：项目根是 cwd 所在的 git 仓库根（不在系统目录里）。
	RootGit
	// RootCwd：不在 git 仓库里，cwd 是家目录下的普通目录（见 plainUnderHome），按 cwd 自身当项目根。
	RootCwd
	// RootNotPlain：cwd 是存在的目录、不在 git 仓库里、不太宽、不在系统目录里，但不是家目录下的普通目录（家目录之外、
	// 点开头的某一段之下、~/Library 之下），不当项目根。
	RootNotPlain
	// RootSystem：cwd 所在的 git 仓库根，或不在 git 仓库里时的 cwd 自身，在系统目录里（见 inSystemDir），不当项目根。
	RootSystem
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
	case RootSystem:
		return "system"
	default:
		return "none"
	}
}

// ResolveProjectRoot 定项目根：从清理后的 cwd 向上找含 .git（目录或 worktree 的 .git 文件）的目录，找到且不 tooBroad 时
// 按它定（classifyRoot）：在系统目录里返回空串与 RootSystem（/opt/homebrew 这类仓库归用户可写、在 PATH 上），不再看 cwd；
// 否则返回它与 RootGit（/tmp、~/.config/nvim 下的仓库也是）。找不到，或找到的仓库根太宽（用 git 管 dotfiles 的人家目录里
// 有 .git）时按 cwd 自身定：不是存在的目录或 tooBroad 时返回空串与 RootNone；在系统目录里返回空串与 RootSystem（/etc、
// /usr/local）；是家目录下的普通目录时返回它与 RootCwd；否则返回空串与 RootNotPlain（~/.config、~/Library/LaunchAgents
// 这类目录里自动放行写入太危险）。空的项目根引擎按项目外处理。
// cwd 要是绝对路径：相对路径会按本进程的工作目录解析，一律返回空串与 RootNone。
func ResolveProjectRoot(cwd string) (root string, kind RootKind) {
	dir := CleanCwd(cwd)
	if !filepath.IsAbs(dir) {
		return "", RootNone
	}
	for d := dir; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			if r, k := classifyRoot(d, true); k != RootNone {
				return r, k
			}
			break // 仓库根太宽：再往上只会更宽，看 cwd 自身
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return classifyRoot(dir, false)
}

// classifyRoot 定一个候选目录 dir 能不能当项目根、是哪一类：git 为真时 dir 是 cwd 所在的 git 仓库根，否则是不在 git 仓库里的
// cwd 自身。依次看：不是存在的目录或 tooBroad → RootNone；在系统目录里 → RootSystem；是 git 仓库根 → RootGit；
// 家目录下的普通目录 → RootCwd；否则 RootNotPlain。只有 RootGit 与 RootCwd 返回 dir，其余返回空串。
func classifyRoot(dir string, git bool) (string, RootKind) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() || tooBroad(dir) {
		return "", RootNone
	}
	switch {
	case inSystemDir(dir):
		return "", RootSystem
	case git:
		return dir, RootGit
	case plainUnderHome(dir):
		return dir, RootCwd
	}
	return "", RootNotPlain
}

// systemDirs 是系统目录：它们本身与下面的目录不当项目根，git 仓库也不当。/opt/homebrew、/usr/local 这类目录可能归用户
// 可写、又在 PATH 上，当项目根会让“项目内可写”的放行改写装好的程序。按列出的与解析过链接的两种写法比（见 systemSpellings）：
// macOS 上 /etc、/var、/tmp 都链到 /private 下。测试会替换它。
var systemDirs = []string{"/opt", "/usr", "/etc", "/var", "/System", "/Library", "/private"}

// systemExempt 是系统目录里照常判断的目录（含它们下面）：/private/tmp 是 macOS 上 /tmp 解析后的写法，是公共临时目录。
// 测试会替换它。
var systemExempt = []string{"/private/tmp"}

// inSystemDir 报告存在的目录 dir 是否在系统目录里：解析链接后（cwd 不存在时向上找到的仓库根是未解析的写法）第一段以点开头，
// 或是某个系统目录（任一写法）本身或在其下面；又不在 systemExempt、本进程的临时目录（tempExempt）、家目录（homeExempt）下面。
// 按路径分段比较（/optional 不在 /opt 下），darwin 上不分大小写（默认的卷不分，/OPT/homebrew 就是 /opt/homebrew，
// EvalSymlinks 不改大小写）。
// 第一段以点开头的一律算：macOS 的 /.nofollow/…、/.resolve/<n>/…、/.vol/<dev>/<ino> 这类写法 EvalSymlinks 解析不出真实路径，
// /.nofollow/opt/homebrew 就是 /opt/homebrew。macOS 数据卷上的 firmlink 写法（/System/Volumes/Data/...）都在 /System 下，
// 也一律算；临时目录与家目录的除外按 $TMPDIR、$HOME 解析后的写法比，cwd 写成 firmlink 写法时不在其中（fail closed）。
func inSystemDir(dir string) bool {
	p := CleanCwd(dir)
	sys := systemSpellings()
	if !dotFirst(p) && !slices.ContainsFunc(sys, func(d string) bool { return pathUnder(p, d) }) {
		return false
	}
	under := func(d string) bool { return d != "" && pathUnder(p, d) }
	return !slices.ContainsFunc(systemExempt, under) && !under(tempExempt(sys)) && !under(homeExempt(sys))
}

// systemSpellings 返回 systemDirs 的各种写法：列出的写法与解析过链接的写法（不同时两个都要）。
func systemSpellings() []string {
	out := slices.Clone(systemDirs)
	for _, d := range systemDirs {
		if r := CleanCwd(d); !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// dotFirst 报告清理过的绝对路径 p 的第一段是否以点开头。
func dotFirst(p string) bool {
	s := segments(p)
	return len(s) > 0 && strings.HasPrefix(s[0], ".")
}

// exemptBase 报告解析过链接的 d 能不能当系统目录里的除外目录：是绝对路径，第一段不以点开头，也不是任一系统目录（sys 是
// systemSpellings 的结果，两种写法都在里面）本身或其上级（根目录）。否则整个系统目录都放过了：$TMPDIR=/var 解析成 /private/var，
// 与列出的 /var 字符串不同，要按解析后的写法比。
func exemptBase(d string, sys []string) bool {
	return filepath.IsAbs(d) && !dotFirst(d) && !slices.ContainsFunc(sys, func(s string) bool { return pathUnder(s, d) })
}

// tempExempt 返回本进程的临时目录（os.TempDir()，即 $TMPDIR，没有时是 /tmp；解析链接后），它里面的目录照常判断：macOS 的
// $TMPDIR 是 /var/folders/…/T，解析后在 /private/var 下，是每个用户私有的临时目录，与 /tmp 同类；守护进程由 launchd 起时也有它。
// 只在它是存在的目录、归本进程的有效用户、组与其他人没有任何权限（0700，真实的 macOS $TMPDIR 就是）、能当除外目录（exemptBase）
// 时返回，否则返回空串、不除外任何目录：$TMPDIR=/var、/etc（解析后是系统目录本身）、/usr/local（归 root）、/opt/homebrew
// （0755）都不行。/tmp（macOS 上是 /private/tmp）归 root、1777，不靠它除外，靠 systemExempt。sys 见 exemptBase。
func tempExempt(sys []string) string {
	t := CleanCwd(os.TempDir())
	st, err := os.Stat(t)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0o077 != 0 || !ownedByMe(st) || !exemptBase(t, sys) {
		return ""
	}
	return t
}

// homeExempt 返回家目录（userHome，解析链接后），它里面的目录照常判断：家目录可能解析到系统目录里，如 Fedora Silverblue 的
// /home 链到 /var/home，不除外的话用户的仓库全都不当项目根。只在它是存在的目录、归本进程的有效用户、能当除外目录（exemptBase）、
// 至少在它所在的系统目录（各写法里取最深的）下面两段时返回：/var/home/x、/var/lib/svc 可以；/var/root（解析成 /private/var/root，
// 在 /private/var 下只有一段）、当 HOME 的 /opt/homebrew 不行。否则返回空串。家目录本身与它的上级照旧由 tooBroad 挡掉。
// sys 见 exemptBase。
func homeExempt(sys []string) string {
	home := userHome()
	if home == "" {
		return ""
	}
	h := CleanCwd(home)
	st, err := os.Stat(h)
	if err != nil || !st.IsDir() || !ownedByMe(st) || !exemptBase(h, sys) {
		return ""
	}
	depth := -1 // 所在系统目录的段数，不在系统目录里时为 -1
	for _, d := range sys {
		if pathUnder(h, d) {
			depth = max(depth, len(segments(d)))
		}
	}
	if depth >= 0 && len(segments(h))-depth < 2 {
		return ""
	}
	return h
}

// pathUnder 报告清理过的绝对路径 p 是否是 dir 或在 dir 下面：按 / 分成各段逐段比（sameName），/optional 不在 /opt 下。
func pathUnder(p, dir string) bool {
	ps, ds := segments(p), segments(dir)
	return len(ps) >= len(ds) && slices.EqualFunc(ps[:len(ds)], ds, sameName)
}

// segments 把清理过的绝对路径拆成各段：/ 没有段，/a/b 是 a、b 两段。
func segments(p string) []string {
	if p = strings.Trim(p, "/"); p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// sameName 比较路径的一段：darwin 上不分大小写（默认的卷不分），其余系统区分。
func sameName(a, b string) bool {
	if runtime.GOOS == "darwin" {
		return strings.EqualFold(a, b)
	}
	return a == b
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
