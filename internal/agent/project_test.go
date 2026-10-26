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
	git             bool
}

// checkRoots 逐条比对 ResolveProjectRoot 与 FindProjectRoot（后者只是前者的 root）。
func checkRoots(t *testing.T, cases []rootCase) {
	t.Helper()
	for _, c := range cases {
		if root, git := ResolveProjectRoot(c.cwd); root != c.want || git != c.git {
			t.Errorf("%s: ResolveProjectRoot(%q) = %q, %v，想要 %q, %v", c.name, c.cwd, root, git, c.want, c.git)
		}
		if got := FindProjectRoot(c.cwd); got != c.want {
			t.Errorf("%s: FindProjectRoot(%q) = %q，想要 %q", c.name, c.cwd, got, c.want)
		}
	}
}

func TestResolveProjectRoot(t *testing.T) {
	base := shortTempDir(t)
	home := filepath.Join(base, "home")
	os.MkdirAll(home, 0o755)
	t.Setenv("HOME", home)
	repo := filepath.Join(base, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "src", "pkg"), 0o755)
	wt := filepath.Join(base, "wt")
	os.MkdirAll(wt, 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644)
	plain := filepath.Join(base, "plain")
	deep := filepath.Join(plain, "a", "b")
	os.MkdirAll(deep, 0o755)
	file := filepath.Join(plain, "f.txt")
	os.WriteFile(file, nil, 0o644)

	checkRoots(t, []rootCase{
		{"仓库的子目录", filepath.Join(repo, "src", "pkg"), resolved(repo), true},
		{"仓库根", repo, resolved(repo), true},
		{"worktree 的 .git 文件", wt, resolved(wt), true},
		{"非 git 目录", plain, resolved(plain), false},
		{"非 git 的深层子目录取它自己，不取上级", deep, resolved(deep), false},
		{"家目录", home, "", false},
		{"家目录的祖先", base, "", false},
		{"根目录", "/", "", false},
		{"不存在", filepath.Join(base, "missing"), "", false},
		{"不是目录", file, "", false},
	})
	if got := CleanCwd(filepath.Join(repo, "src", "..", "src")); got != resolved(filepath.Join(repo, "src")) {
		t.Errorf("CleanCwd = %q", got)
	}
}

// 用 git 管 dotfiles 的人家目录里有 .git：家目录不当项目根，家目录下的非 git 子目录按它自己当项目根，
// 家目录下的仓库照常。
func TestResolveProjectRootHomeHasGit(t *testing.T) {
	base := shortTempDir(t)
	t.Setenv("HOME", base)
	os.MkdirAll(filepath.Join(base, ".git"), 0o755)
	sub := filepath.Join(base, "sub")
	os.MkdirAll(filepath.Join(sub, "deep"), 0o755)
	repo := filepath.Join(base, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)

	checkRoots(t, []rootCase{
		{"家目录下的非 git 子目录", sub, resolved(sub), false},
		{"更深的子目录取它自己", filepath.Join(sub, "deep"), resolved(filepath.Join(sub, "deep")), false},
		{"cwd 就是家目录", base, "", false},
		{"cwd 不存在", filepath.Join(base, "gone"), "", false},
		{"家目录下的仓库", filepath.Join(repo, "src"), resolved(repo), true},
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
	os.MkdirAll(filepath.Join(home, "sub"), 0o755)
	os.MkdirAll(filepath.Join(base, ".git"), 0o755)
	t.Setenv("HOME", home)
	repo := filepath.Join(home, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)

	checkRoots(t, []rootCase{
		{"家目录下的非 git 子目录", filepath.Join(home, "sub"), resolved(filepath.Join(home, "sub")), false},
		{"cwd 就是家目录", home, "", false},
		{"cwd 是含 .git 的祖先", base, "", false},
		{"cwd 不存在", filepath.Join(base, "gone", "x"), "", false},
		{"家目录下的仓库", filepath.Join(repo, "src"), resolved(repo), true},
	})
}

// 相对路径按本进程的工作目录解析，不能拿来定项目根：一律返回空。
func TestResolveProjectRootRelative(t *testing.T) {
	base := shortTempDir(t)
	t.Setenv("HOME", filepath.Join(base, "home"))
	repo := filepath.Join(base, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "src"), 0o755)
	os.MkdirAll(filepath.Join(base, "plain", "a"), 0o755)
	t.Chdir(repo)
	checkRoots(t, []rootCase{
		{"仓库里的相对子目录", "src", "", false},
		{"点", ".", "", false},
	})
	t.Chdir(filepath.Join(base, "plain"))
	checkRoots(t, []rootCase{{"非 git 目录里的相对子目录", "a", "", false}})
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
		{"数据卷", "/System/Volumes/Data", "", false},
		{"卷目录", "/System/Volumes", "", false},
		{"/System", "/System", "", false},
		{"firmlink 写法的家目录的上级", "/System/Volumes/Data/Users", "", false},
		{"firmlink 写法的家目录", "/System/Volumes/Data" + home, "", false},
	})
}
