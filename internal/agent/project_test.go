package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindProjectRoot(t *testing.T) {
	base := shortTempDir(t)
	repo := filepath.Join(base, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "src", "pkg"), 0o755)
	wt := filepath.Join(base, "wt")
	os.MkdirAll(wt, 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644)
	plain := filepath.Join(base, "plain", "deep")
	os.MkdirAll(plain, 0o755)

	resolved := func(p string) string { r, _ := filepath.EvalSymlinks(p); return r }
	cases := []struct{ cwd, want string }{
		{filepath.Join(repo, "src", "pkg"), resolved(repo)},
		{repo, resolved(repo)},
		{wt, resolved(wt)},
		{plain, ""},
		{filepath.Join(base, "missing"), ""},
	}
	for _, c := range cases {
		if got := FindProjectRoot(c.cwd); got != c.want {
			t.Errorf("FindProjectRoot(%q) = %q, 想要 %q", c.cwd, got, c.want)
		}
	}
	if got := CleanCwd(filepath.Join(repo, "src", "..", "src")); got != resolved(filepath.Join(repo, "src")) {
		t.Errorf("CleanCwd = %q", got)
	}
}
