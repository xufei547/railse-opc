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
// /etc、/usr/local、/opt 这类系统目录先按系统目录判（RootSystem），见 TestResolveProjectRootSystemDir。
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
		{"/etc（只 stat）", "/etc", "", RootSystem},
		{"链到 /etc 的 ~/dev/etc-link", filepath.Join(home, "dev", "etc-link"), "", RootSystem},
		{"链到 ~/.config/x 的 ~/dev/cfg-link", filepath.Join(home, "dev", "cfg-link"), "", RootNotPlain},
		{"点目录自己是 git 仓库", filepath.Join(nvim, "lua"), resolved(nvim), RootGit},
		{"/tmp 下新建的目录", tmpUnder(t, "/tmp"), "", RootNotPlain},
	}
	if _, err := os.Stat("/usr/local"); err == nil {
		cases = append(cases, rootCase{"/usr/local（只 stat）", "/usr/local", "", RootSystem})
	}
	if runtime.GOOS == "darwin" {
		cases = append(cases, rootCase{"/private/tmp 下的目录", tmpUnder(t, "/private/tmp"), "", RootNotPlain})
	}
	// 家目录之外的前缀：只 stat 现成的目录与它下面第一个不被当成 git 仓库根的子目录，没有就不测。
	// /Applications 不是系统目录，按普通的家目录之外判；/opt 是系统目录，里面的 git 仓库（/opt/homebrew）也一样
	for _, p := range []struct {
		dir  string
		kind RootKind
	}{{"/Applications", RootNotPlain}, {"/opt", RootSystem}} {
		if st, err := os.Stat(p.dir); err == nil && st.IsDir() {
			if _, kind := ResolveProjectRoot(p.dir); kind != RootGit {
				cases = append(cases, rootCase{p.dir + "（只 stat）", p.dir, "", p.kind})
			}
		}
		if d, ok := firstSubdir(p.dir); ok {
			cases = append(cases, rootCase{p.dir + " 下的目录（只 stat）", d, "", p.kind})
		}
	}
	checkRoots(t, cases)
}

// firstSubdir 返回 parent 下第一个不被 ResolveProjectRoot 当成 git 仓库根（RootGit）的子目录
// （按名字排序；只读目录项与 stat，不写）。系统目录里的 git 仓库不是 RootGit，也会被选中。
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

// 系统目录（/opt、/usr、/etc、/var、/System、/Library、/private 和它们下面）里的目录不当项目根，git 仓库也不当
// （RootSystem）：/opt/homebrew 这类仓库归用户可写、在 PATH 上。用替换的系统目录列表在临时目录里造出“系统目录里的
// git 仓库”，除外列表清空（$TMPDIR=/tmp 时临时目录在 /private/tmp 下），本进程的临时目录指到别处，免得除外把它们放过。
// 按路径分段比较：同级的 opt-x、optional 不在 opt 里。
// cwd 不存在时向上找到的仓库根是未解析链接的写法（macOS 的 /var/folders/…），也要按解析后的写法判。
func TestResolveProjectRootSystemDir(t *testing.T) {
	base := shortTempDir(t)
	t.Setenv("HOME", filepath.Join(base, "home"))
	sys := filepath.Join(base, "opt")
	brew := filepath.Join(sys, "homebrew")
	plain := filepath.Join(sys, "plain")
	sib, optional := filepath.Join(base, "opt-x"), filepath.Join(base, "optional")
	mkdirs(t, filepath.Join(base, "home"), filepath.Join(brew, ".git"), filepath.Join(brew, "bin"), plain,
		filepath.Join(sib, ".git"), filepath.Join(optional, ".git"))
	setVar(t, &systemDirs, []string{resolved(sys)})
	setVar(t, &systemExempt, nil)
	t.Setenv("TMPDIR", filepath.Join(base, "elsewhere"))

	checkRoots(t, []rootCase{
		{"系统目录里的 git 仓库根", brew, "", RootSystem},
		{"系统目录里 git 仓库的子目录", filepath.Join(brew, "bin"), "", RootSystem},
		{"系统目录里 git 仓库下不存在的 cwd", filepath.Join(brew, "gone", "x"), "", RootSystem},
		{"系统目录本身", sys, "", RootSystem},
		{"系统目录里的非 git 目录", plain, "", RootSystem},
		{"同级的 opt-x 不在 opt 里", sib, resolved(sib), RootGit},
		{"同级的 optional 不在 opt 里", optional, resolved(optional), RootGit},
	})
	if got := RootSystem.String(); got != "system" {
		t.Errorf("RootSystem.String() = %q", got)
	}
}

// 系统目录按列出的与解析过链接的两种写法比：列表里写的是符号链接（像 /var 链到 /private/var）时，解析后落在链接目标下的
// 目录同样在系统目录里。
func TestResolveProjectRootSystemDirLinkSpelling(t *testing.T) {
	base := shortTempDir(t)
	t.Setenv("HOME", filepath.Join(base, "home"))
	real := filepath.Join(base, "private", "var")
	link := filepath.Join(base, "var")
	mkdirs(t, filepath.Join(base, "home"), filepath.Join(real, "repo", ".git"))
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	setVar(t, &systemDirs, []string{link})
	setVar(t, &systemExempt, nil)
	t.Setenv("TMPDIR", filepath.Join(base, "elsewhere"))
	checkRoots(t, []rootCase{
		{"经链接写法进来的仓库", filepath.Join(link, "repo"), "", RootSystem},
		{"按链接目标写法进来的仓库", filepath.Join(real, "repo"), "", RootSystem},
	})
}

// 第一段以点开头的路径一律算系统目录：macOS 的 /.nofollow/…、/.resolve/<n>/…、/.vol/<dev>/<ino> 解析不出真实路径
// （EvalSymlinks 原样返回或报错），/.nofollow/opt/homebrew 就是 /opt/homebrew，不这样判就绕过了系统目录。
// 临时目录写成这种写法时也不除外。只看第一段：/x/.hidden 不算。
func TestResolveProjectRootDotPrefix(t *testing.T) {
	base := shortTempDir(t)
	t.Setenv("HOME", filepath.Join(base, "home"))
	setVar(t, &systemDirs, nil)
	setVar(t, &systemExempt, nil)
	t.Setenv("TMPDIR", filepath.Join(base, "elsewhere"))
	for _, p := range []string{"/.nofollow/opt/homebrew", "/.resolve/1/opt/homebrew", "/.vol/1/2", "/.snapshots/x", "/.x"} {
		if !inSystemDir(p) {
			t.Errorf("inSystemDir(%q) 应为真", p)
		}
	}
	for _, p := range []string{"/x/.hidden", "/x/.nofollow/y", "/."} {
		if inSystemDir(p) {
			t.Errorf("inSystemDir(%q) 应为假", p)
		}
	}

	if runtime.GOOS != "darwin" {
		return
	}
	// darwin：/.nofollow 加上解析后的写法就是同一个目录。家目录下的仓库与普通目录这样写也不当项目根
	tmp := filepath.Join(base, "T")
	repo := filepath.Join(base, "home", "dev", "repo")
	plain := filepath.Join(base, "home", "dev", "plain")
	mkdirs(t, filepath.Join(repo, ".git"), plain, filepath.Join(tmp, "repo", ".git"))
	if err := os.Chmod(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	nf := func(p string) string { return "/.nofollow" + resolved(p) }
	if st, err := os.Stat(nf(repo)); err != nil || !st.IsDir() {
		t.Skipf("这台机器没有 /.nofollow 写法: %v", err)
	}
	checkRoots(t, []rootCase{
		{"/.nofollow 写法的仓库", nf(repo), "", RootSystem},
		{"/.nofollow 写法的家目录下普通目录", nf(plain), "", RootSystem},
		{"照常写法的仓库", repo, resolved(repo), RootGit},
	})
	t.Setenv("TMPDIR", nf(tmp))
	checkRoots(t, []rootCase{{"临时目录是 /.nofollow 写法时不除外", nf(filepath.Join(tmp, "repo")), "", RootSystem}})
}

// 系统目录里照常判断的两处：除外列表（/private/tmp，macOS 上 /tmp 解析后的写法）与本进程的临时目录（os.TempDir()，
// 解析链接后）和它们下面。临时目录只在归本进程的有效用户、组与其他人没有任何权限（0700，真实的 macOS $TMPDIR 就是）、
// 不是任何系统目录（列出的与解析后的两种写法）本身或其上级时除外：链到系统目录的 $TMPDIR（像 /var → /private/var）、
// 0755 的目录、根目录、相对路径都不除外。按分段比较，T-x 不在 T 里。用替换的系统目录与除外列表，$TMPDIR 逐个换。
func TestResolveProjectRootSystemExempt(t *testing.T) {
	base := shortTempDir(t)
	t.Setenv("HOME", filepath.Join(base, "home"))
	sys := filepath.Join(base, "private")
	sysVar := filepath.Join(sys, "var")
	varLink := filepath.Join(base, "var") // 像 /var → /private/var
	pubTmp := filepath.Join(sys, "tmp", "repo")
	tmp := filepath.Join(sysVar, "T")
	repo := filepath.Join(tmp, "repo")
	open := filepath.Join(sysVar, "T755")
	sib := filepath.Join(sysVar, "T-x")
	link := filepath.Join(base, "tlink")
	mkdirs(t, filepath.Join(base, "home"), filepath.Join(pubTmp, ".git"), filepath.Join(repo, ".git"), filepath.Join(sib, ".git"),
		filepath.Join(sys, "tmpx", ".git"), filepath.Join(open, "repo", ".git"))
	for _, d := range []string{sys, sysVar, tmp} {
		if err := os.Chmod(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tmp, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sysVar, varLink); err != nil {
		t.Fatal(err)
	}
	setVar(t, &systemDirs, []string{resolved(sys), varLink})
	setVar(t, &systemExempt, []string{resolved(filepath.Join(sys, "tmp"))})

	t.Setenv("TMPDIR", filepath.Join(base, "elsewhere"))
	checkRoots(t, []rootCase{
		{"除外列表里的仓库", pubTmp, resolved(pubTmp), RootGit},
		{"除外列表按分段比较", filepath.Join(sys, "tmpx"), "", RootSystem},
	})
	for _, c := range []struct {
		name, tmpdir, cwd string
		kind              RootKind
	}{
		{"临时目录在系统目录里", tmp, repo, RootGit},
		{"临时目录带结尾斜杠", tmp + "/", repo, RootGit},
		{"临时目录经过符号链接", link, repo, RootGit},
		{"临时目录是系统目录本身", sys, repo, RootSystem},
		{"临时目录链到系统目录（像 /var → /private/var）", varLink, repo, RootSystem},
		{"临时目录是系统目录解析后的写法", sysVar, repo, RootSystem},
		{"临时目录权限是 0755", open, filepath.Join(open, "repo"), RootSystem},
		{"临时目录是根目录", "/", repo, RootSystem},
		{"临时目录是相对路径", "T", repo, RootSystem},
		{"临时目录在别处", filepath.Join(base, "elsewhere"), repo, RootSystem},
	} {
		t.Setenv("TMPDIR", c.tmpdir)
		want := ""
		if c.kind == RootGit {
			want = resolved(c.cwd)
		}
		checkRoots(t, []rootCase{{c.name, c.cwd, want, c.kind}})
		if c.kind == RootGit {
			checkRoots(t, []rootCase{
				{c.name + "：cwd 不存在时按解析后的仓库根判", filepath.Join(repo, "gone"), repo, RootGit},
				{c.name + "：T-x 不在 T 里", sib, "", RootSystem},
			})
		}
	}
}

// 家目录解析后落在系统目录里（Fedora Silverblue 的 /home → /var/home，macOS root 的 /var/root）时，家目录下面照常判断
// （仓库 RootGit，普通目录 RootCwd），前提是家目录归本进程的有效用户、不是任何系统目录（两种写法）本身或其上级、至少在它
// 所在的系统目录（取最深的写法）下面两段：/var/home/u 可以，/var/root 不行（在 /private 下两段，但在 /private/var 下只有一段）。
// 家目录本身与它的上级照旧太宽（RootNone），别人的家目录照旧是系统目录。用替换的列表，临时目录与除外列表关掉。
func TestResolveProjectRootSystemHomeExempt(t *testing.T) {
	base := shortTempDir(t)
	priv := filepath.Join(base, "private")
	pvar := filepath.Join(priv, "var")
	varLink := filepath.Join(base, "var")   // 像 /var → /private/var
	homeLink := filepath.Join(base, "home") // 像 Silverblue 的 /home → /var/home
	u := filepath.Join(pvar, "home", "u")
	other := filepath.Join(pvar, "home", "other", "repo")
	root := filepath.Join(pvar, "root")
	mkdirs(t, filepath.Join(u, "dev", "repo", ".git"), filepath.Join(u, "dev", "plain"), filepath.Join(other, ".git"),
		filepath.Join(root, "dev", "repo", ".git"), filepath.Join(root, "dev", "plain"))
	if err := os.Symlink(pvar, varLink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(varLink, "home"), homeLink); err != nil {
		t.Fatal(err)
	}
	setVar(t, &systemDirs, []string{resolved(priv), varLink})
	setVar(t, &systemExempt, nil)
	t.Setenv("TMPDIR", filepath.Join(base, "elsewhere"))
	urepo, uplain := filepath.Join(u, "dev", "repo"), filepath.Join(u, "dev", "plain")

	t.Setenv("HOME", filepath.Join(homeLink, "u")) // 经过两层链接，解析后在 var 下两段
	checkRoots(t, []rootCase{
		{"家目录下的仓库", urepo, resolved(urepo), RootGit},
		{"家目录下的普通目录", uplain, resolved(uplain), RootCwd},
		{"家目录本身", u, "", RootNone},
		{"家目录的上级", filepath.Join(pvar, "home"), "", RootNone},
		{"别人家目录下的仓库", other, "", RootSystem},
	})
	t.Setenv("HOME", root)
	checkRoots(t, []rootCase{
		{"像 /var/root 的家目录下的仓库", filepath.Join(root, "dev", "repo"), "", RootSystem},
		{"像 /var/root 的家目录下的普通目录", filepath.Join(root, "dev", "plain"), "", RootSystem},
	})
	t.Setenv("HOME", base)
	checkRoots(t, []rootCase{{"家目录是系统目录的上级", urepo, "", RootSystem}})
	t.Setenv("HOME", pvar)
	checkRoots(t, []rootCase{{"家目录是系统目录解析后的写法", urepo, "", RootSystem}})

	// 家目录不归本进程的有效用户时不除外：借现成的 /usr/share/man（归 root，在 /usr 下两段），只 stat
	if os.Geteuid() != 0 {
		if _, err := os.Stat("/usr/share/man/man1"); err == nil {
			setVar(t, &systemDirs, []string{"/usr"})
			t.Setenv("HOME", "/usr/share/man")
			checkRoots(t, []rootCase{{"家目录归别人", "/usr/share/man/man1", "", RootSystem}})
		}
	}
}

// 真实的公共临时目录：/tmp 与 /private/tmp 下的 git 仓库照常是项目根（macOS 上 /tmp 解析成 /private/tmp，在 /private 里但除外）。
// $TMPDIR 指到别处，确认不是靠临时目录除外。没有的跳过。
func TestResolveProjectRootPublicTmp(t *testing.T) {
	base := shortTempDir(t)
	t.Setenv("HOME", filepath.Join(base, "home"))
	t.Setenv("TMPDIR", filepath.Join(base, "elsewhere"))
	for _, parent := range []string{"/tmp", "/private/tmp"} {
		t.Run(parent, func(t *testing.T) {
			d := tmpUnder(t, parent)
			mkdirs(t, filepath.Join(d, ".git"), filepath.Join(d, "src"))
			checkRoots(t, []rootCase{{parent + " 下的 git 仓库", filepath.Join(d, "src"), resolved(d), RootGit}})
		})
	}
}

// 真实的临时目录除外：macOS 的 $TMPDIR 是 /var/folders/…/T（归用户、0700），解析后在 /private/var 下（系统目录里），里面的
// git 仓库照常是项目根；$TMPDIR 指到 /tmp、/var、/private/var 或 T 的上级（0755）后同一个仓库就在系统目录里了。
// $TMPDIR 不在 /private/var 下时跳过（Linux 上通常是 /tmp）。
func TestResolveProjectRootTempDirExempt(t *testing.T) {
	realTmp := CleanCwd(os.TempDir())
	if !pathUnder(realTmp, "/private/var") {
		t.Skipf("临时目录 %s 不在 /private/var 下", os.TempDir())
	}
	base := shortTempDir(t)
	t.Setenv("HOME", filepath.Join(base, "home"))
	repo := filepath.Join(base, "repo")
	mkdirs(t, filepath.Join(repo, ".git"), filepath.Join(repo, "src"))
	checkRoots(t, []rootCase{{"临时目录里的 git 仓库", filepath.Join(repo, "src"), resolved(repo), RootGit}})
	for _, d := range []string{"/tmp", "/var", "/private/var", filepath.Dir(realTmp)} {
		t.Setenv("TMPDIR", d)
		checkRoots(t, []rootCase{{"临时目录指到 " + d + " 后", filepath.Join(repo, "src"), "", RootSystem}})
	}
}

// 真实的系统目录当 $TMPDIR 也不会被除外（只 stat）：/etc（macOS 上解析成 /private/etc）、/usr/local（归 root）、
// /opt/homebrew（归用户但 0755）。没有的跳过。
func TestResolveProjectRootTempDirGuardReal(t *testing.T) {
	t.Setenv("HOME", shortTempDir(t))
	for _, c := range []struct{ tmpdir, cwd string }{
		{"/etc", "/etc"}, {"/usr/local", "/usr/local"}, {"/opt/homebrew", "/opt/homebrew"},
	} {
		if _, err := os.Stat(c.cwd); err != nil {
			continue
		}
		t.Setenv("TMPDIR", c.tmpdir)
		checkRoots(t, []rootCase{{"$TMPDIR=" + c.tmpdir, c.cwd, "", RootSystem}})
	}
}

// darwin 上不分大小写：默认的卷上 /OPT/homebrew 就是 /opt/homebrew（EvalSymlinks 不改大小写），同样是系统目录；
// 除外的临时目录同样不分。区分大小写的卷上跳过。
func TestResolveProjectRootSystemDirCase(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("只有 darwin 不分大小写比较")
	}
	base := shortTempDir(t)
	t.Setenv("HOME", filepath.Join(base, "home"))
	sys := filepath.Join(base, "opt")
	tmp := filepath.Join(sys, "T")
	mkdirs(t, filepath.Join(sys, "homebrew", ".git"), filepath.Join(tmp, "repo", ".git"))
	if err := os.Chmod(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	upper := filepath.Join(base, "OPT", "homebrew")
	if _, err := os.Stat(upper); err != nil {
		t.Skip("区分大小写的卷")
	}
	setVar(t, &systemDirs, []string{resolved(sys)})
	setVar(t, &systemExempt, nil)
	t.Setenv("TMPDIR", tmp)
	lowerTmp := filepath.Join(base, "OPT", "t", "repo")
	checkRoots(t, []rootCase{
		{"大写的系统目录", upper, "", RootSystem},
		{"大小写不同的临时目录", lowerTmp, resolved(lowerTmp), RootGit},
	})
}

// 真实的系统目录只 stat 不写：/etc、/usr/local 是系统目录；/opt/homebrew 是 git 仓库时也不当项目根。darwin 上换成
// 大写写法（/USR/local、/OPT/homebrew）或 /.nofollow、/.resolve/1 写法也一样。没有的跳过。
func TestResolveProjectRootRealSystemDirs(t *testing.T) {
	t.Setenv("HOME", shortTempDir(t))
	cases := []rootCase{{"/etc（只 stat）", "/etc", "", RootSystem}}
	if _, err := os.Stat("/usr/local"); err == nil {
		cases = append(cases, rootCase{"/usr/local（只 stat）", "/usr/local", "", RootSystem})
	}
	if _, err := os.Stat("/USR/local"); err == nil && runtime.GOOS == "darwin" {
		cases = append(cases, rootCase{"/USR/local（只 stat）", "/USR/local", "", RootSystem})
	}
	if _, err := os.Stat("/opt/homebrew/.git"); err == nil {
		cases = append(cases, rootCase{"/opt/homebrew（只 stat）", "/opt/homebrew", "", RootSystem},
			rootCase{"/opt/homebrew 下不存在的目录", "/opt/homebrew/railse-missing", "", RootSystem})
		for _, p := range []string{"/OPT/homebrew", "/.nofollow/opt/homebrew", "/.resolve/1/opt/homebrew"} {
			if _, err := os.Stat(p + "/.git"); err == nil && runtime.GOOS == "darwin" {
				cases = append(cases, rootCase{p + "（只 stat）", p, "", RootSystem})
			}
		}
	}
	checkRoots(t, cases)
}
