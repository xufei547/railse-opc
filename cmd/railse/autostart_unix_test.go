//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/xufei547/railse-opc/internal/autostart"
	"github.com/xufei547/railse-opc/internal/hooksconf"
)

// systemDirEnv 在 autostartEnv 之上记下 chown 了哪些目录（经 fd，记 fd 的名字）。
func systemDirEnv(t *testing.T) (home string, rec *cmdRec, chowned *[]string) {
	home, rec = autostartEnv(t)
	var c []string
	chownDir = func(f *os.File, uid, gid int) error {
		if uid != 4242 || gid != 4243 {
			t.Errorf("chown 给了 %d:%d", uid, gid)
		}
		c = append(c, f.Name())
		return nil
	}
	return home, rec, &c
}

func installSystemDir(home, dir string, extra ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	args := append([]string{"--system", "--hub", "--user", "alice", "--dir", dir, "--bin", filepath.Join(home, "railse")}, extra...)
	code := runAutostartOS("linux", 0, args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestAutostartSystemNewDir(t *testing.T) {
	home, rec, chowned := systemDirEnv(t)
	// 父目录也还不存在；umask 收得很紧时新建的父目录也要是 0755（运行用户要能进去）
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	dir := filepath.Join(home, "srv", "railse", "hub")
	// dry-run 只说要建，不建不 chown
	code, out, errOut := installSystemDir(home, dir, "--dry-run")
	if code != 0 {
		t.Fatalf("dry-run: %d %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(home, "srv")); !os.IsNotExist(err) || len(*chowned) != 0 || len(rec.calls) != 0 || !strings.Contains(out, dir) {
		t.Fatalf("dry-run 不该建目录、chown 或跑命令: %v %v %v\n%s", err, *chowned, rec.calls, out)
	}
	code, _, errOut = installSystemDir(home, dir)
	if code != 0 {
		t.Fatalf("install: %d %s", code, errOut)
	}
	real, _ := filepath.EvalSymlinks(dir)
	if st, err := os.Lstat(real); err != nil || !st.IsDir() || st.Mode().Perm() != 0o700 {
		t.Fatalf("数据目录应为 0700: %v %v", err, st)
	}
	for _, p := range []string{filepath.Join(home, "srv"), filepath.Join(home, "srv", "railse")} {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o755 {
			t.Fatalf("新建的父目录 %s 应为 0755（不受 umask 影响）: %v", p, st.Mode().Perm())
		}
	}
	if len(*chowned) != 1 || (*chowned)[0] != real {
		t.Fatalf("只该 chown 本次新建的目录一次: %v（想要 %s）", *chowned, real)
	}
	b, _ := os.ReadFile(filepath.Join(autostart.SystemUnitDir, "railse-hub.service"))
	if !strings.Contains(string(b), "ReadWritePaths="+real+"\n") || !strings.Contains(string(b), "Environment=RAILSE_HUB_HOME="+real+"\n") || strings.Contains(string(b), "StateDirectory") {
		t.Fatalf("显式目录的系统单元（应写解开符号链接后的路径）:\n%s", b)
	}
	if len(rec.calls) != 3 {
		t.Fatalf("systemctl 调用: %v", rec.calls)
	}
	// 重跑：目录已存在、已属于 alice？这里属主是测试用户（不是 4242），所以按“别人的目录”拒绝，不再 chown
	*chowned = nil
	if code, _, errOut = installSystemDir(home, dir); code != 1 || len(*chowned) != 0 {
		t.Fatalf("已存在而不属于运行用户的目录应拒绝: %d %v %s", code, *chowned, errOut)
	}
}

// 审查的复现：运行用户控制 --dir 的父目录，把 hub 换成指向 root 的空目录（如 /usr/local/sbin）的符号链接。
func TestAutostartSystemDirRefusesSymlink(t *testing.T) {
	home, rec, chowned := systemDirEnv(t)
	sensitive := filepath.Join(home, "usr-local-sbin")
	os.Mkdir(sensitive, 0o755)
	userHome := filepath.Join(home, "home-alice")
	os.Mkdir(userHome, 0o755)
	link := filepath.Join(userHome, "hub")
	os.Symlink(sensitive, link)
	code, _, errOut := installSystemDir(home, link)
	if code != 1 || !strings.Contains(errOut, "符号链接") {
		t.Fatalf("符号链接应拒绝: %d %q", code, errOut)
	}
	if st, _ := os.Stat(sensitive); st.Mode().Perm() != 0o755 || len(*chowned) != 0 || len(rec.calls) != 0 {
		t.Fatalf("拒绝时不该改目标、chown 或跑命令: %v %v %v", st.Mode().Perm(), *chowned, rec.calls)
	}
	if _, err := os.Stat(autostart.SystemUnitDir); !os.IsNotExist(err) {
		t.Fatal("拒绝时不该写单元")
	}
}

func TestAutostartSystemDirExisting(t *testing.T) {
	home, rec, chowned := systemDirEnv(t)
	// 已存在的空目录、属于别人（测试用户不是 4242）：以前会被 chown，现在拒绝（--dir /var/empty 不能交出去）
	empty := filepath.Join(home, "var-empty")
	os.Mkdir(empty, 0o711)
	// 提示以运行用户身份另建一个（与 docs/setup.md 一致），不叫人自己 chown，也不叫人给一个还不存在的目录
	if code, _, errOut := installSystemDir(home, empty); code != 1 || !strings.Contains(errOut, empty) || !strings.Contains(errOut, "sudo -u alice mkdir -p -m 700 ") ||
		strings.Contains(errOut, "chown 给") || strings.Contains(errOut, "还不存在的目录") {
		t.Fatalf("别人的空目录应拒绝: %d %q", code, errOut)
	}
	if st, _ := os.Stat(empty); st.Mode().Perm() != 0o711 || len(*chowned) != 0 || len(rec.calls) != 0 {
		t.Fatalf("拒绝时什么都不该改: %v %v %v", st.Mode().Perm(), *chowned, rec.calls)
	}
	// 别人的非空目录同样拒绝
	foreign := filepath.Join(home, "foreign")
	os.MkdirAll(foreign, 0o755)
	os.WriteFile(filepath.Join(foreign, "passwd"), []byte("x"), 0o644)
	if code, _, _ := installSystemDir(home, foreign); code != 1 {
		t.Fatalf("别人的非空目录应拒绝: %d", code)
	}
	// 不是目录
	file := filepath.Join(home, "afile")
	os.WriteFile(file, []byte("x"), 0o644)
	if code, _, errOut := installSystemDir(home, file); code != 1 || !strings.Contains(errOut, "不是目录") {
		t.Fatalf("文件当数据目录应拒绝: %d %q", code, errOut)
	}
	if _, err := os.Stat(autostart.SystemUnitDir); !os.IsNotExist(err) {
		t.Fatal("拒绝时不该写单元")
	}
	// 已经属于运行用户的目录：不 chown，只收紧为 0700
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	lookupUser = func(string) (*user.User, error) { return &user.User{Username: "alice", Uid: me.Uid, Gid: me.Gid}, nil }
	if code, _, errOut := installSystemDir(home, foreign); code != 0 {
		t.Fatalf("运行用户自己的目录: %d %s", code, errOut)
	}
	if st, _ := os.Stat(foreign); st.Mode().Perm() != 0o700 || len(*chowned) != 0 {
		t.Fatalf("自己的目录应收紧为 0700 且不 chown: %v %v", st.Mode().Perm(), *chowned)
	}
}

func TestAutostartSystemDirUntrustedParent(t *testing.T) {
	home, rec, chowned := systemDirEnv(t)
	// 父目录别人能写（没有粘滞位）：root 建目录与 chown 之间路径可能被换掉，不替它新建
	open := filepath.Join(home, "shared")
	os.Mkdir(open, 0o755)
	os.Chmod(open, 0o777)
	dir := filepath.Join(open, "sub", "hub")
	code, _, errOut := installSystemDir(home, dir)
	if code != 1 || !strings.Contains(errOut, "sudo -u alice mkdir -p -m 700 "+hooksconf.ShellQuote(dir)) || strings.Contains(errOut, "chown 给") {
		t.Fatalf("不可信的父目录应拒绝并提示以运行用户身份建好: %d %q", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(open, "sub")); !os.IsNotExist(err) || len(*chowned) != 0 || len(rec.calls) != 0 {
		t.Fatalf("拒绝时不该建任何目录: %v %v %v", err, *chowned, rec.calls)
	}
	// 粘滞位的公共目录（如 /tmp）可以：别人删不掉、换不掉 root 建的条目
	sticky := filepath.Join(home, "tmp")
	os.Mkdir(sticky, 0o755)
	os.Chmod(sticky, 0o777|os.ModeSticky)
	if code, _, errOut := installSystemDir(home, filepath.Join(sticky, "hub")); code != 0 || len(*chowned) != 1 {
		t.Fatalf("粘滞位父目录: %d %v %s", code, *chowned, errOut)
	}
}
