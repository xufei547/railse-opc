package agent

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// resolved 解析符号链接（macOS 的 /var 是 /private/var 的链接），与 CleanCwd 得到的写法一致。
func resolved(p string) string { r, _ := filepath.EvalSymlinks(p); return r }

type rootCase struct {
	name, cwd, want string
	kind            RootKind
}

// checkRoots 逐条比对 ResolveProjectRoot 与 FindProjectRoot（后者只是前者的 root）。
func checkRoots(t *testing.T, cases []rootCase) {
	t.Helper()
	for _, c := range cases {
		if root, kind := ResolveProjectRoot(c.cwd); root != c.want || kind != c.kind {
			t.Errorf("%s: ResolveProjectRoot(%q) = %q, %v，想要 %q, %v", c.name, c.cwd, root, kind, c.want, c.kind)
		}
		if got := FindProjectRoot(c.cwd); got != c.want {
			t.Errorf("%s: FindProjectRoot(%q) = %q，想要 %q", c.name, c.cwd, got, c.want)
		}
	}
}

// mkdirs 建好每个目录，失败就停。
func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// tmpUnder 在 parent 下建一个临时目录（测试结束删掉）；parent 不在时跳过。
func tmpUnder(t *testing.T, parent string) string {
	t.Helper()
	if _, err := os.Stat(parent); err != nil {
		t.Skipf("没有 %s", parent)
	}
	d, err := os.MkdirTemp(parent, "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func TestResolveProjectRoot(t *testing.T) {
	base := shortTempDir(t)
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	repo := filepath.Join(base, "repo") // 家目录之外的 git 仓库照常是项目根
	wt := filepath.Join(base, "wt")
	plain := filepath.Join(home, "dev", "plain")
	deep := filepath.Join(plain, "a", "b")
	mkdirs(t, filepath.Join(repo, ".git"), filepath.Join(repo, "src", "pkg"), wt, deep, filepath.Join(base, "outside"))
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644)
	file := filepath.Join(plain, "f.txt")
	os.WriteFile(file, nil, 0o644)

	checkRoots(t, []rootCase{
		{"仓库的子目录", filepath.Join(repo, "src", "pkg"), resolved(repo), RootGit},
		{"仓库根", repo, resolved(repo), RootGit},
		{"worktree 的 .git 文件", wt, resolved(wt), RootGit},
		{"家目录下的非 git 目录", plain, resolved(plain), RootCwd},
		{"非 git 的深层子目录取它自己，不取上级", deep, resolved(deep), RootCwd},
		{"家目录之外的非 git 目录", filepath.Join(base, "outside"), "", RootNotPlain},
		{"家目录", home, "", RootNone},
		{"家目录的祖先", base, "", RootNone},
		{"根目录", "/", "", RootNone},
		{"不存在", filepath.Join(base, "missing"), "", RootNone},
		{"不是目录", file, "", RootNone},
	})
	if got := CleanCwd(filepath.Join(repo, "src", "..", "src")); got != resolved(filepath.Join(repo, "src")) {
		t.Errorf("CleanCwd = %q", got)
	}
}

// 非 git 目录只在家目录下的普通目录里按 cwd 当项目根：家目录之外、点开头的某一段之下、~/Library 之下都不回退
// （RootNotPlain，没有项目根）；指向这些地方的符号链接按目标判断。点目录自己是 git 仓库时照常是项目根。
func TestResolveProjectRootNotPlain(t *testing.T) {
	base := shortTempDir(t)
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	foo := filepath.Join(home, "dev", "foo")
	nvim := filepath.Join(home, ".config", "nvim")
	mkdirs(t, foo, filepath.Join(home, "dev", "Library", "x"), filepath.Join(home, "Library", "LaunchAgents"),
		filepath.Join(home, ".config", "x"), filepath.Join(nvim, ".git"), filepath.Join(nvim, "lua"),
		filepath.Join(home, "dev", ".hidden", "x"))
	if err := os.Symlink("/etc", filepath.Join(home, "dev", "etc-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".config", "x"), filepath.Join(home, "dev", "cfg-link")); err != nil {
		t.Fatal(err)
	}
	cases := []rootCase{
		{"~/dev/foo", foo, resolved(foo), RootCwd},
		{"只看紧挨家目录的那一段是不是 Library", filepath.Join(home, "dev", "Library", "x"), resolved(filepath.Join(home, "dev", "Library", "x")), RootCwd},
		{"~/Library/LaunchAgents", filepath.Join(home, "Library", "LaunchAgents"), "", RootNotPlain},
		{"~/.config/x", filepath.Join(home, ".config", "x"), "", RootNotPlain},
		{"~/dev/.hidden/x", filepath.Join(home, "dev", ".hidden", "x"), "", RootNotPlain},
		{"/etc（只 stat）", "/etc", "", RootNotPlain},
		{"链到 /etc 的 ~/dev/etc-link", filepath.Join(home, "dev", "etc-link"), "", RootNotPlain},
		{"链到 ~/.config/x 的 ~/dev/cfg-link", filepath.Join(home, "dev", "cfg-link"), "", RootNotPlain},
		{"点目录自己是 git 仓库", filepath.Join(nvim, "lua"), resolved(nvim), RootGit},
		{"/tmp 下新建的目录", tmpUnder(t, "/tmp"), "", RootNotPlain},
	}
	if _, err := os.Stat("/usr/local"); err == nil {
		cases = append(cases, rootCase{"/usr/local（只 stat）", "/usr/local", "", RootNotPlain})
	}
	if runtime.GOOS == "darwin" {
		cases = append(cases, rootCase{"/private/tmp 下的目录", tmpUnder(t, "/private/tmp"), "", RootNotPlain})
	}
	// 家目录之外的系统前缀：只 stat 现成的目录与它下面第一个不在 git 仓库里的子目录，没有就不测
	for _, parent := range []string{"/Applications", "/opt"} {
		if st, err := os.Stat(parent); err == nil && st.IsDir() {
			if _, kind := ResolveProjectRoot(parent); kind != RootGit {
				cases = append(cases, rootCase{parent + "（只 stat）", parent, "", RootNotPlain})
			}
		}
		if d, ok := firstSubdir(parent); ok {
			cases = append(cases, rootCase{parent + " 下的目录（只 stat）", d, "", RootNotPlain})
		}
	}
	checkRoots(t, cases)
}

// firstSubdir 返回 parent 下第一个不在 git 仓库里的子目录（按名字排序；只读目录项与 stat，不写）。
func firstSubdir(parent string) (string, bool) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		d := filepath.Join(parent, e.Name())
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			continue
		}
		if _, kind := ResolveProjectRoot(d); kind == RootGit {
			continue
		}
		return d, true
	}
	return "", false
}

// macOS 默认的卷不分大小写：~/library/x 就是 ~/Library/x，同样不回退。区分大小写的卷上跳过。
func TestResolveProjectRootLibraryCaseInsensitive(t *testing.T) {
	base := shortTempDir(t)
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	mkdirs(t, filepath.Join(home, "Library", "x"))
	lower := filepath.Join(home, "library", "x")
	if _, err := os.Stat(lower); err != nil {
		t.Skip("区分大小写的卷")
	}
	checkRoots(t, []rootCase{{"~/library/x", lower, "", RootNotPlain}})
}

// 用 git 管 dotfiles 的人家目录里有 .git：家目录不当项目根，家目录下的非 git 子目录按同样的三条判断，
// 家目录下的仓库照常。
func TestResolveProjectRootHomeHasGit(t *testing.T) {
	base := shortTempDir(t)
	t.Setenv("HOME", base)
	sub := filepath.Join(base, "dev", "foo")
	repo := filepath.Join(base, "repo")
	nvim := filepath.Join(base, ".config", "nvim")
	mkdirs(t, filepath.Join(base, ".git"), filepath.Join(sub, "deep"), filepath.Join(repo, ".git"), filepath.Join(repo, "src"), nvim)

	checkRoots(t, []rootCase{
		{"家目录下的非 git 子目录", sub, resolved(sub), RootCwd},
		{"更深的子目录取它自己", filepath.Join(sub, "deep"), resolved(filepath.Join(sub, "deep")), RootCwd},
		{"~/.config/nvim", nvim, "", RootNotPlain},
		{"cwd 就是家目录", base, "", RootNone},
		{"cwd 不存在", filepath.Join(base, "gone"), "", RootNone},
		{"家目录下的仓库", filepath.Join(repo, "src"), resolved(repo), RootGit},
	})
}

func TestTooBroadRoot(t *testing.T) {
	if !tooBroad("/") {
		t.Error(`tooBroad("/") 应为真`)
	}
}

// /Users/.git 这类家目录的祖先比家目录还宽，同样不能当项目根；家目录与它下面的目录照上面的规则。
func TestResolveProjectRootHomeAncestorHasGit(t *testing.T) {
	base := shortTempDir(t)
	home := filepath.Join(base, "u")
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	mkdirs(t, filepath.Join(home, "sub"), filepath.Join(base, ".git"), filepath.Join(repo, ".git"), filepath.Join(repo, "src"))

	checkRoots(t, []rootCase{
		{"家目录下的非 git 子目录", filepath.Join(home, "sub"), resolved(filepath.Join(home, "sub")), RootCwd},
		{"cwd 就是家目录", home, "", RootNone},
		{"cwd 是含 .git 的祖先", base, "", RootNone},
		{"cwd 不存在", filepath.Join(base, "gone", "x"), "", RootNone},
		{"家目录下的仓库", filepath.Join(repo, "src"), resolved(repo), RootGit},
	})
}

// 相对路径按本进程的工作目录解析，不能拿来定项目根：一律返回空。
func TestResolveProjectRootRelative(t *testing.T) {
	base := shortTempDir(t)
	home := filepath.Join(base, "home")
	t.Setenv("HOME", home)
	repo := filepath.Join(base, "repo")
	mkdirs(t, filepath.Join(repo, ".git"), filepath.Join(repo, "src"), filepath.Join(home, "plain", "a"))
	t.Chdir(repo)
	checkRoots(t, []rootCase{
		{"仓库里的相对子目录", "src", "", RootNone},
		{"点", ".", "", RootNone},
	})
	t.Chdir(filepath.Join(home, "plain"))
	checkRoots(t, []rootCase{{"非 git 目录里的相对子目录", "a", "", RootNone}})
}

// macOS 上家目录还能写成数据卷上的 firmlink（/System/Volumes/Data/Users/x），EvalSymlinks 不解析它；
// 它的祖先 /System/Volumes/Data、/System/Volumes、/System 也是家目录的祖先，不当项目根。
// 临时 HOME 造不出 firmlink，借现成的 /Users/Shared 当 HOME：只 stat，不写任何东西。
func TestResolveProjectRootDarwinFirmlink(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("只有 macOS 有数据卷 firmlink")
	}
	const home = "/Users/Shared"
	a, errA := os.Stat(home)
	b, errB := os.Stat("/System/Volumes/Data" + home)
	if errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Skip("这台机器没有 /Users/Shared 的 firmlink 写法")
	}
	t.Setenv("HOME", home)
	checkRoots(t, []rootCase{
		{"数据卷", "/System/Volumes/Data", "", RootNone},
		{"卷目录", "/System/Volumes", "", RootNone},
		{"/System", "/System", "", RootNone},
		{"firmlink 写法的家目录的上级", "/System/Volumes/Data/Users", "", RootNone},
		{"firmlink 写法的家目录", "/System/Volumes/Data" + home, "", RootNone},
	})
}

// $HOME 本身经过符号链接（/var/root、/tmp/... 这类，macOS 的 /var、/tmp 都链到 /private）时，数据卷上的 firmlink 写法
// 只有按解析后的写法拼（/System/Volumes/Data/private/var/...）才存在：两种写法都要拼，否则 /System/Volumes/Data 等
// 三个祖先认不出来。临时目录在 /var/folders 下，正好是这种情况。
func TestHomeSpellingsFirmlinkThroughSymlink(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("只有 macOS 有数据卷 firmlink")
	}
	home := shortTempDir(t)
	resolvedHome := CleanCwd(home)
	a, errA := os.Stat(home)
	b, errB := os.Stat("/System/Volumes/Data" + resolvedHome)
	if filepath.Clean(home) == resolvedHome || errA != nil || errB != nil || !os.SameFile(a, b) {
		t.Skipf("临时目录 %s 不经过符号链接，或没有 firmlink 写法", home)
	}
	t.Setenv("HOME", home)
	for _, d := range []string{"/System/Volumes/Data", "/System/Volumes", "/System"} {
		if !tooBroad(d) {
			t.Errorf("HOME=%s 时 %s 应太宽", home, d)
		}
	}
	checkRoots(t, []rootCase{{"数据卷", "/System/Volumes/Data", "", RootNone}})
}
