package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/railse/railse-opc/internal/autostart"
)

// cmdRec 记下 runCommand 收到的每条命令；respond 不为空时由它给输出与错误，否则一律成功、无输出。
type cmdRec struct {
	calls   [][]string
	respond func(call []string) (string, error)
}

func (r *cmdRec) run(name string, args ...string) (string, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if r.respond != nil {
		return r.respond(call)
	}
	return "", nil
}

// exitErr 是带退出码的假错误，形同 *exec.ExitError。
type exitErr int

func (e exitErr) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

// autostartEnv 给测试一个假家目录（里面放一个可执行的假 railse）、记录命令的 runCommand、零间隔的 bootstrap 重试与
// 临时的系统单元目录，测试结束全部还原：绝不在本机跑真实的 launchctl、systemctl，也不写真实的 /etc/systemd/system。
func autostartEnv(t *testing.T) (home string, rec *cmdRec) {
	t.Helper()
	home, _ = os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(home) })
	if err := os.WriteFile(filepath.Join(home, "railse"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("RAILSE_HOME", "")
	t.Setenv("RAILSE_HUB_HOME", "")
	t.Setenv("SUDO_USER", "")
	rec = &cmdRec{}
	oldRun, oldDelay, oldUnitDir, oldLookup, oldChown := runCommand, bootstrapRetryDelay, autostart.SystemUnitDir, lookupUser, chownDir
	runCommand, bootstrapRetryDelay = rec.run, 0
	autostart.SystemUnitDir = filepath.Join(home, "etc-systemd-system")
	lookupUser = func(name string) (*user.User, error) {
		switch name {
		case "alice":
			return &user.User{Username: "alice", Uid: "4242", Gid: "4243"}, nil
		case "root", "toor":
			return &user.User{Username: name, Uid: "0", Gid: "0"}, nil
		case "zero2":
			return &user.User{Username: name, Uid: "00", Gid: "0"}, nil
		case "plus0":
			return &user.User{Username: name, Uid: "+0", Gid: "0"}, nil
		}
		return nil, user.UnknownUserError(name)
	}
	chownDir = func(f *os.File, _, _ int) error { t.Errorf("不该 chown %s", f.Name()); return nil }
	t.Cleanup(func() {
		runCommand, bootstrapRetryDelay, autostart.SystemUnitDir, lookupUser, chownDir = oldRun, oldDelay, oldUnitDir, oldLookup, oldChown
	})
	return home, rec
}

func TestLaunchdPlistIsWellFormed(t *testing.T) {
	sp := autostartSpec{Kind: "agent", Bin: "/usr/local/bin/railse", Dir: "/Users/u/.railse", Home: "/Users/u", UID: 501}
	b := launchdPlist(sp)
	var v struct {
		XMLName xml.Name `xml:"plist"`
	}
	if err := xml.Unmarshal(b, &v); err != nil {
		t.Fatalf("不是合法 XML: %v\n%s", err, b)
	}
	for _, want := range []string{"<string>dev.railse.agent</string>", "<string>/usr/local/bin/railse</string>", "<string>agent</string>", "<string>run</string>",
		"<key>RAILSE_HOME</key><string>/Users/u/.railse</string>", "<key>KeepAlive</key><true/>", "<key>RunAtLoad</key><true/>", "/Users/u/.railse/launchd.log"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("plist 缺 %s:\n%s", want, b)
		}
	}
	// 路径里的特殊字符要转义
	sp2 := sp
	sp2.Bin = "/Users/u/My <Apps>/railse"
	if b2 := string(launchdPlist(sp2)); !strings.Contains(b2, "/Users/u/My &lt;Apps&gt;/railse") {
		t.Fatalf("应转义: %s", b2)
	}
	hub := autostartSpec{Kind: "hub", Bin: "/usr/local/bin/railse", Dir: "/Users/u/.railse-hub", Home: "/Users/u", UID: 501, Extra: []string{"--public-url", "https://hub.example.com"}}
	if b3 := string(launchdPlist(hub)); !strings.Contains(b3, "<string>hub</string>") || !strings.Contains(b3, "<key>RAILSE_HUB_HOME</key>") || !strings.Contains(b3, "<string>--public-url</string><string>https://hub.example.com</string>") {
		t.Fatalf("hub plist: %s", b3)
	}
}

func TestSystemdUnit(t *testing.T) {
	sp := autostartSpec{Kind: "agent", Bin: "/usr/local/bin/railse", Dir: "/home/u/.railse", Home: "/home/u"}
	u := string(systemdUnit(sp))
	for _, want := range []string{"Type=exec\n", "ExecStart=/usr/local/bin/railse agent run", "Environment=RAILSE_HOME=/home/u/.railse", "Restart=always", "WantedBy=default.target"} {
		if !strings.Contains(u, want) {
			t.Errorf("用户单元缺 %s:\n%s", want, u)
		}
	}
	if strings.Contains(u, "AmbientCapabilities") || strings.Contains(u, "User=") || strings.Contains(u, "StateDirectory") || strings.Contains(u, "network-online") {
		t.Fatal("用户单元不该有系统单元的字段")
	}
	sys := autostartSpec{Kind: "hub", Bin: "/usr/local/bin/railse", Dir: "/srv/railse-hub", Home: "/root", System: true, User: "railse", Extra: []string{"--acme-domain", "hub.example.com"}}
	s := string(systemdUnit(sys))
	for _, want := range []string{"Type=exec\n", "ExecStart=/usr/local/bin/railse hub run --acme-domain hub.example.com", "Environment=RAILSE_HUB_HOME=/srv/railse-hub", "User=railse",
		"AmbientCapabilities=CAP_NET_BIND_SERVICE", "NoNewPrivileges=true", "ProtectSystem=strict", "PrivateTmp=true", "ReadWritePaths=/srv/railse-hub", "WantedBy=multi-user.target"} {
		if !strings.Contains(s, want) {
			t.Errorf("系统单元缺 %s:\n%s", want, s)
		}
	}
	if strings.Contains(s, "StateDirectory") {
		t.Errorf("显式数据目录不该用 StateDirectory:\n%s", s)
	}
	// 默认数据目录：交给 systemd 建并交给 User
	sys.Dir, sys.StateDir = "/var/lib/railse-hub", "railse-hub"
	s = string(systemdUnit(sys))
	for _, want := range []string{"StateDirectory=railse-hub\n", "StateDirectoryMode=0700\n", "Environment=RAILSE_HUB_HOME=/var/lib/railse-hub", "ReadWritePaths=/var/lib/railse-hub"} {
		if !strings.Contains(s, want) {
			t.Errorf("默认目录的系统单元缺 %s:\n%s", want, s)
		}
	}
	if systemdQuote("/a b/railse") != `"/a b/railse"` || systemdQuote("/plain") != "/plain" {
		t.Fatal("systemdQuote")
	}
	// % 是说明符、$ 是变量替换：都要写成字面量
	if q := systemdQuote(`50%"x\`); q != `"50%%\"x\\"` {
		t.Fatalf("systemdQuote 转义: %s", q)
	}
	// 单独的 ; 在 ExecStart 里是命令分隔符：加引号
	if q := systemdQuote(";"); q != `";"` {
		t.Fatalf("systemdQuote(;) = %s", q)
	}
	if q := systemdQuote("a;b"); q != "a;b" {
		t.Fatalf("不是单独的 ; 不用引号: %s", q)
	}
	odd := autostartSpec{Kind: "hub", Bin: "/opt/my apps/railse", Dir: "/data/100%", Extra: []string{"--public-url", "https://h.example.com/$HOME", ";"}}
	o := string(systemdUnit(odd))
	for _, want := range []string{`ExecStart="/opt/my apps/railse" hub run --public-url https://h.example.com/$$HOME ";"` + "\n", "Environment=RAILSE_HUB_HOME=/data/100%%\n"} {
		if !strings.Contains(o, want) {
			t.Errorf("缺 %q:\n%s", want, o)
		}
	}
}

func TestAutostartInstallUninstallStatus(t *testing.T) {
	home, rec := autostartEnv(t)
	bin := filepath.Join(home, "railse")
	var out, errOut bytes.Buffer
	// macOS 路径
	code := runAutostartOS("darwin", 501, []string{"--dir", filepath.Join(home, ".railse"), "--bin", bin}, &out, &errOut)
	if code != 0 {
		t.Fatalf("install: %d %s", code, errOut.String())
	}
	plist := filepath.Join(home, "Library", "LaunchAgents", "dev.railse.agent.plist")
	if st, err := os.Stat(plist); err != nil || st.Mode().Perm() != 0o644 {
		t.Fatalf("plist 未写或权限不对: %v", err)
	}
	// bootout（没装过时失败也无妨）、enable（之前被禁用过也能起）、bootstrap
	if !equalCalls(rec.calls, [][]string{
		{"launchctl", "bootout", "gui/501/dev.railse.agent"},
		{"launchctl", "enable", "gui/501/dev.railse.agent"},
		{"launchctl", "bootstrap", "gui/501", plist},
	}) {
		t.Fatalf("launchctl 调用: %v", rec.calls)
	}
	rec.calls = nil
	out.Reset()
	if code := runAutostartOS("darwin", 501, []string{"--status"}, &out, &errOut); code != 0 || !equalCalls(rec.calls, [][]string{{"launchctl", "print", "gui/501/dev.railse.agent"}}) {
		t.Fatalf("status: %d %v", code, rec.calls)
	}
	rec.calls = nil
	if code := runAutostartOS("darwin", 501, []string{"--uninstall"}, &out, &errOut); code != 0 {
		t.Fatalf("uninstall: %d %s", code, errOut.String())
	}
	if _, err := os.Stat(plist); !os.IsNotExist(err) || !equalCalls(rec.calls, [][]string{{"launchctl", "bootout", "gui/501/dev.railse.agent"}}) {
		t.Fatalf("卸载后 plist 应删除且先 bootout: %v %v", err, rec.calls)
	}
	// Linux 用户单元
	rec.calls = nil
	out.Reset()
	if code := runAutostartOS("linux", 1000, []string{"--hub", "--dir", filepath.Join(home, ".railse-hub"), "--bin", bin, "--", "--public-url", "https://hub.example.com"}, &out, &errOut); code != 0 {
		t.Fatalf("linux install: %d %s", code, errOut.String())
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "railse-hub.service")
	b, err := os.ReadFile(unit)
	if err != nil || !strings.Contains(string(b), "hub run --public-url https://hub.example.com") {
		t.Fatalf("unit: %v %s", err, b)
	}
	want := [][]string{
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", "railse-hub.service"},
		{"systemctl", "--user", "restart", "railse-hub.service"},
	}
	if !equalCalls(rec.calls, want) {
		t.Fatalf("systemctl 调用: %v", rec.calls)
	}
	if !strings.Contains(out.String(), "enable-linger") {
		t.Fatal("应提示 loginctl enable-linger")
	}
	rec.calls = nil
	out.Reset()
	if code := runAutostartOS("linux", 1000, []string{"--hub", "--status"}, &out, &errOut); code != 0 || !equalCalls(rec.calls, [][]string{{"systemctl", "--user", "is-active", "railse-hub.service"}}) {
		t.Fatalf("linux status: %d %v", code, rec.calls)
	}
	rec.calls = nil
	if code := runAutostartOS("linux", 1000, []string{"--hub", "--uninstall"}, &out, &errOut); code != 0 {
		t.Fatalf("linux uninstall: %d %s", code, errOut.String())
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) || !equalCalls(rec.calls, [][]string{
		{"systemctl", "--user", "disable", "--now", "railse-hub.service"},
		{"systemctl", "--user", "daemon-reload"},
	}) {
		t.Fatalf("linux 卸载: %v %v", err, rec.calls)
	}
	// dry-run 不写不跑
	rec.calls = nil
	out.Reset()
	if code := runAutostartOS("linux", 1000, []string{"--dry-run", "--bin", bin}, &out, &errOut); code != 0 || len(rec.calls) != 0 || !strings.Contains(out.String(), "ExecStart=") {
		t.Fatalf("dry-run: %d %v %q", code, rec.calls, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "railse-agent.service")); !os.IsNotExist(err) {
		t.Fatal("dry-run 不应写文件")
	}
	// --system 非 root 拒绝
	errOut.Reset()
	if code := runAutostartOS("linux", 1000, []string{"--system", "--hub", "--bin", bin}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "root") {
		t.Fatalf("非 root 的 --system: %d %q", code, errOut.String())
	}
	// 不支持的系统
	if code := runAutostartOS("windows", 0, nil, &out, &errOut); code != 1 {
		t.Fatalf("windows: %d", code)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("被拒绝时不该跑命令: %v", rec.calls)
	}
}

func equalCalls(got, want [][]string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if strings.Join(got[i], "\x00") != strings.Join(want[i], "\x00") {
			return false
		}
	}
	return true
}

func TestAutostartRejectsBadArgs(t *testing.T) {
	home, rec := autostartEnv(t)
	cases := []struct {
		goos string
		args []string
	}{
		{"darwin", []string{"foo"}},                                            // 没有 -- 的位置参数不能进 ExecStart
		{"linux", []string{"--hub", "foo"}},                                    // 同上
		{"linux", []string{"--hub", "foo", "--", "--public-url", "https://x"}}, // -- 之前的位置参数
		{"darwin", []string{"--", "--foreground"}},                             // 守护进程不收额外参数
		{"linux", []string{"--system"}},                                        // --system 只给 hub
		{"darwin", []string{"--system", "--hub"}},                              // --system 只在 Linux
		{"linux", []string{"--hub", "--user", "alice"}},                        // --user 只和 --system 一起用
		{"linux", []string{"--status", "--uninstall"}},                         // 二选一
		{"linux", []string{"--hub", "--", "--dir", "/x"}},                      // 数据目录用本命令的 --dir
		{"linux", []string{"--hub", "--", "--dir=/x"}},
		{"linux", []string{"--nope"}},
		{"linux", []string{"--dir"}},
		{"linux", []string{"--hub", "--bin", "/a\nb"}}, // 换行会拆出新的单元行
		{"linux", []string{"--hub", "--", "--public-url", "https://x\nUser=root"}},
	}
	for _, c := range cases {
		var out, errOut bytes.Buffer
		if code := runAutostartOS(c.goos, 1000, c.args, &out, &errOut); code != 2 {
			t.Errorf("%s %q: 想要退出 2, 得到 %d（%s）", c.goos, c.args, code, errOut.String())
		}
	}
	// 默认值（环境变量）里的控制字符同样要挡住
	t.Setenv("RAILSE_HOME", filepath.Join(home, "a\nExecStartPre=/bin/sh"))
	var out, errOut bytes.Buffer
	if code := runAutostartOS("linux", 1000, []string{"--bin", filepath.Join(home, "railse")}, &out, &errOut); code != 2 {
		t.Errorf("RAILSE_HOME 带换行: 想要退出 2, 得到 %d（%s）", code, errOut.String())
	}
	t.Setenv("RAILSE_HOME", "")
	t.Setenv("RAILSE_HUB_HOME", "/x\ny")
	if code := runAutostartOS("linux", 1000, []string{"--hub", "--bin", filepath.Join(home, "railse")}, &out, &errOut); code != 2 {
		t.Errorf("RAILSE_HUB_HOME 带换行: 想要退出 2, 得到 %d（%s）", code, errOut.String())
	}
	if len(rec.calls) != 0 {
		t.Fatalf("参数错时不该跑命令: %v", rec.calls)
	}
	if ents, _ := os.ReadDir(home); len(ents) != 1 || ents[0].Name() != "railse" {
		t.Fatalf("参数错时不该写文件: %v", ents)
	}
}

func TestAutostartChecksBin(t *testing.T) {
	home, rec := autostartEnv(t)
	missing := filepath.Join(home, "nope", "railse")
	notExec := filepath.Join(home, "plain")
	os.WriteFile(notExec, []byte("x"), 0o644)
	for _, bin := range []string{missing, notExec, home} {
		var out, errOut bytes.Buffer
		if code := runAutostartOS("linux", 1000, []string{"--bin", bin}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), bin) {
			t.Errorf("%s: 想要退出 1 且报出路径，得到 %d %q", bin, code, errOut.String())
		}
		// dry-run 只警告
		out.Reset()
		errOut.Reset()
		if code := runAutostartOS("linux", 1000, []string{"--bin", bin, "--dry-run"}, &out, &errOut); code != 0 || !strings.Contains(errOut.String(), "警告") {
			t.Errorf("%s dry-run: 想要退出 0 并警告，得到 %d %q", bin, code, errOut.String())
		}
	}
	if len(rec.calls) != 0 {
		t.Fatalf("可执行文件不对时不该跑命令: %v", rec.calls)
	}
	if _, err := os.Stat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
		t.Fatal("可执行文件不对时不该写单元")
	}
	// 状态与卸载不看可执行文件
	var out, errOut bytes.Buffer
	if code := runAutostartOS("linux", 1000, []string{"--uninstall", "--bin", missing}, &out, &errOut); code != 0 {
		t.Fatalf("卸载不该检查 --bin: %d %s", code, errOut.String())
	}
}

func TestAutostartWarnsTempBin(t *testing.T) {
	home, _ := autostartEnv(t)
	var out, errOut bytes.Buffer
	// 测试的家目录就在临时目录下：像 go run 的产物
	if code := runAutostartOS("linux", 1000, []string{"--bin", filepath.Join(home, "railse"), "--dry-run"}, &out, &errOut); code != 0 || !strings.Contains(errOut.String(), "临时") {
		t.Fatalf("临时路径应警告: %d %q", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	if code := runAutostartOS("linux", 1000, []string{"--bin", "/usr/local/bin/railse", "--dry-run"}, &out, &errOut); code != 0 || strings.Contains(errOut.String(), "临时") {
		t.Fatalf("非临时路径不该警告临时: %d %q", code, errOut.String())
	}
}

func TestAutostartRefusesRoot(t *testing.T) {
	home, rec := autostartEnv(t)
	bin := filepath.Join(home, "railse")
	cases := []struct {
		name string
		goos string
		uid  int
		args []string
		want string
	}{
		{"macOS 用 sudo", "darwin", 0, []string{"--bin", bin}, "sudo"},
		{"Linux 以 root 装用户级 hub", "linux", 0, []string{"--hub", "--bin", bin}, "--system"},
		{"Linux 以 root 装用户级守护进程", "linux", 0, []string{"--bin", bin}, "root"},
		{"Linux 以 root 的 dry-run 也拒绝", "linux", 0, []string{"--bin", bin, "--dry-run"}, "root"},
		{"--system 不是 root", "linux", 1000, []string{"--system", "--hub", "--user", "alice", "--bin", bin}, "root"},
		{"--system 不是 root 的卸载", "linux", 1000, []string{"--system", "--hub", "--uninstall"}, "root"},
		{"--system 没有运行用户", "linux", 0, []string{"--system", "--hub", "--bin", bin}, "--user"},
		{"--user root", "linux", 0, []string{"--system", "--hub", "--user", "root", "--bin", bin}, "root"},
		{"uid 为 0 的别名", "linux", 0, []string{"--system", "--hub", "--user", "toor", "--bin", bin}, "root"},
		{"uid 写成 00", "linux", 0, []string{"--system", "--hub", "--user", "zero2", "--bin", bin, "--dry-run"}, "root"},
		{"uid 写成 +0", "linux", 0, []string{"--system", "--hub", "--user", "plus0", "--bin", bin, "--dry-run"}, "root"},
		{"全数字的用户名", "linux", 0, []string{"--system", "--hub", "--user", "1000", "--bin", bin, "--dry-run"}, "数字"},
		{"SUDO_USER 是 root", "linux", 0, []string{"--system", "--hub", "--bin", bin}, "root"},
		{"找不到用户", "linux", 0, []string{"--system", "--hub", "--user", "ghost", "--bin", bin}, "ghost"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "SUDO_USER 是 root" {
				t.Setenv("SUDO_USER", "root")
			}
			var out, errOut bytes.Buffer
			if code := runAutostartOS(c.goos, c.uid, c.args, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), c.want) {
				t.Fatalf("想要退出 1 且 stderr 含 %q，得到 %d %q", c.want, code, errOut.String())
			}
			if strings.Contains(out.String(), "User=") {
				t.Fatalf("被拒绝时不该打印单元:\n%s", out.String())
			}
		})
	}
	if len(rec.calls) != 0 {
		t.Fatalf("被拒绝时不该跑命令: %v", rec.calls)
	}
	if _, err := os.Stat(autostart.SystemUnitDir); !os.IsNotExist(err) {
		t.Fatal("被拒绝时不该写系统单元")
	}
}

func TestAutostartSystemDefaultDir(t *testing.T) {
	home, rec := autostartEnv(t)
	bin := filepath.Join(home, "railse")
	t.Setenv("SUDO_USER", "alice")
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(home, "ignored")) // 系统单元的默认目录不看调用者的环境变量
	var out, errOut bytes.Buffer
	if code := runAutostartOS("linux", 0, []string{"--system", "--hub", "--bin", bin, "--", "--acme-domain", "hub.example.com"}, &out, &errOut); code != 0 {
		t.Fatalf("install: %d %s", code, errOut.String())
	}
	unit := filepath.Join(autostart.SystemUnitDir, "railse-hub.service")
	b, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"User=alice\n", "StateDirectory=railse-hub\n", "StateDirectoryMode=0700\n", "Environment=RAILSE_HUB_HOME=/var/lib/railse-hub\n",
		"ReadWritePaths=/var/lib/railse-hub\n", "ExecStart=" + bin + " hub run --acme-domain hub.example.com\n", "AmbientCapabilities=CAP_NET_BIND_SERVICE\n"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("系统单元缺 %q:\n%s", want, b)
		}
	}
	if st, _ := os.Stat(unit); st.Mode().Perm() != 0o644 {
		t.Fatalf("单元权限 %v", st.Mode().Perm())
	}
	if !equalCalls(rec.calls, [][]string{{"systemctl", "daemon-reload"}, {"systemctl", "enable", "railse-hub.service"}, {"systemctl", "restart", "railse-hub.service"}}) {
		t.Fatalf("systemctl 调用: %v", rec.calls)
	}
	if strings.Contains(out.String(), "enable-linger") {
		t.Fatal("系统单元不需要 linger 提示")
	}
	// 卸载：root、不带 --user 也行
	rec.calls = nil
	if code := runAutostartOS("linux", 0, []string{"--system", "--hub", "--uninstall"}, &out, &errOut); code != 0 {
		t.Fatalf("uninstall: %d %s", code, errOut.String())
	}
	if _, err := os.Stat(unit); !os.IsNotExist(err) || !equalCalls(rec.calls, [][]string{{"systemctl", "disable", "--now", "railse-hub.service"}, {"systemctl", "daemon-reload"}}) {
		t.Fatalf("卸载: %v %v", err, rec.calls)
	}
	// 系统单元用不到家目录：sudo 下没有 HOME 也能装
	t.Setenv("HOME", "")
	rec.calls = nil
	errOut.Reset()
	if code := runAutostartOS("linux", 0, []string{"--system", "--hub", "--bin", bin}, &out, &errOut); code != 0 || len(rec.calls) != 3 {
		t.Fatalf("没有 HOME 的系统单元: %d %q %v", code, errOut.String(), rec.calls)
	}
	t.Setenv("HOME", home)
	// 状态不用 root
	rec.calls = nil
	if code := runAutostartOS("linux", 1000, []string{"--system", "--hub", "--status"}, &out, &errOut); code != 0 || !equalCalls(rec.calls, [][]string{{"systemctl", "is-active", "railse-hub.service"}}) {
		t.Fatalf("status: %d %v", code, rec.calls)
	}
}

// Linux 上用户单元托管要绑 1024 以下端口的 hub（--acme-domain 默认 :443 与 :80，或 --listen 给了低端口）：照装，但醒目地警告
// 它会起不来、被反复拉起，并给出 --system 的写法；不绑低端口、系统单元、macOS 都不警告。
func TestAutostartWarnsUserUnitPrivilegedPorts(t *testing.T) {
	home, rec := autostartEnv(t)
	bin := filepath.Join(home, "railse")
	install := func(goos string, uid int, args ...string) (string, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		if code := runAutostartOS(goos, uid, append([]string{"--hub", "--bin", bin}, args...), &out, &errOut); code != 0 {
			t.Fatalf("%v: %d %s", args, code, errOut.String())
		}
		return out.String(), errOut.String()
	}
	for _, c := range []struct {
		args  []string
		ports string
	}{
		{[]string{"--", "--acme-domain", "hub.example.com", "--acme-email", "me@example.com"}, ":443、:80"},
		{[]string{"--", "--acme-domain=hub.example.com", "--acme-http", "none"}, ":443"},
		{[]string{"--", "--acme-domain", "hub.example.com", "--listen", ":8443"}, ":80"},
		{[]string{"--", "--listen=0.0.0.0:443", "--public-url", "https://hub.example.com"}, "0.0.0.0:443"},
	} {
		rec.calls = nil
		out, errOut := install("linux", 1000, c.args...)
		if !strings.Contains(errOut, "警告") || !strings.Contains(errOut, c.ports) || !strings.Contains(errOut, "--system") ||
			!strings.Contains(errOut, sudoSystemHubCmd+" -- "+strings.Join(c.args[1:], " ")) {
			t.Fatalf("%v 应警告绑不了 %s 并指向 --system: %q", c.args, c.ports, errOut)
		}
		if len(installCalls(rec.calls)) != 3 || !strings.Contains(out, "已托管") {
			t.Fatalf("%v 只警告不拒绝: %v %q", c.args, rec.calls, out)
		}
	}
	for _, c := range []struct {
		goos string
		uid  int
		args []string
	}{
		{"linux", 1000, []string{"--", "--acme-domain", "hub.example.com", "--listen", ":8443", "--acme-http", "none"}},
		{"linux", 1000, []string{"--", "--listen", "127.0.0.1:8787", "--public-url", "https://hub.example.com"}},
		{"linux", 1000, nil},
		{"darwin", 501, []string{"--", "--acme-domain", "hub.example.com"}},
		{"linux", 0, []string{"--system", "--user", "alice", "--", "--acme-domain", "hub.example.com"}},
	} {
		if _, errOut := install(c.goos, c.uid, c.args...); strings.Contains(errOut, "1024") {
			t.Fatalf("%s %v 不该警告低端口: %q", c.goos, c.args, errOut)
		}
	}
}

func TestAutostartLaunchdMakesDirBeforeBootstrap(t *testing.T) {
	home, rec := autostartEnv(t)
	dir := filepath.Join(home, "data", ".railse-hub")
	os.MkdirAll(dir, 0o755) // 已有但太宽：也要收紧
	seen := false
	rec.respond = func(call []string) (string, error) {
		if call[1] == "bootstrap" {
			st, err := os.Stat(dir)
			seen = err == nil && st.Mode().Perm() == 0o700
		}
		return "", nil
	}
	var out, errOut bytes.Buffer
	if code := runAutostartOS("darwin", 501, []string{"--hub", "--dir", dir, "--bin", filepath.Join(home, "railse")}, &out, &errOut); code != 0 {
		t.Fatalf("install: %d %s", code, errOut.String())
	}
	if !seen {
		t.Fatal("bootstrap 之前数据目录（launchd.log 所在）应已建好且为 0700")
	}
	// 新目录：dry-run 不建
	fresh := filepath.Join(home, "fresh")
	rec.calls = nil
	out.Reset()
	if code := runAutostartOS("darwin", 501, []string{"--dir", fresh, "--bin", "/opt/railse", "--dry-run"}, &out, &errOut); code != 0 {
		t.Fatalf("dry-run: %d", code)
	}
	if _, err := os.Stat(fresh); !os.IsNotExist(err) || len(rec.calls) != 0 {
		t.Fatalf("dry-run 不该建目录或跑命令: %v %v", err, rec.calls)
	}
	for _, want := range []string{"<plist", "$ launchctl bootout gui/501/dev.railse.agent", "$ launchctl enable gui/501/dev.railse.agent",
		"$ launchctl bootstrap gui/501 " + filepath.Join(home, "Library", "LaunchAgents", "dev.railse.agent.plist"), fresh} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("dry-run 输出缺 %q:\n%s", want, out.String())
		}
	}
	if _, err := os.Stat(filepath.Join(home, "Library", "LaunchAgents", "dev.railse.agent.plist")); !os.IsNotExist(err) {
		t.Fatal("dry-run 不该写 plist")
	}
}

func TestAutostartBootstrapRetries(t *testing.T) {
	home, rec := autostartEnv(t)
	bin := filepath.Join(home, "railse")
	fails := 2
	rec.respond = func(call []string) (string, error) {
		switch call[1] {
		case "bootstrap":
			if fails > 0 {
				fails--
				return "Bootstrap failed: 5: Input/output error", exitErr(5)
			}
		case "bootout":
			return "Boot-out failed: 3: No such process", exitErr(3) // 没装过：忽略
		case "enable":
			return "boom", exitErr(1) // enable 失败也只是忽略
		}
		return "", nil
	}
	var out, errOut bytes.Buffer
	if code := runAutostartOS("darwin", 501, []string{"--bin", bin}, &out, &errOut); code != 0 {
		t.Fatalf("重试后应成功: %d %s", code, errOut.String())
	}
	if n := countCalls(rec.calls, "bootstrap"); n != 3 {
		t.Fatalf("bootstrap 应跑 3 次，得到 %d: %v", n, rec.calls)
	}
	// 一直失败：重试 5 次后报错退出 1，带上 launchctl 的输出
	rec.calls, fails = nil, 1000
	errOut.Reset()
	if code := runAutostartOS("darwin", 501, []string{"--bin", bin}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "Input/output error") {
		t.Fatalf("一直失败: %d %q", code, errOut.String())
	}
	if n := countCalls(rec.calls, "bootstrap"); n != 1+bootstrapRetries {
		t.Fatalf("bootstrap 应跑 %d 次，得到 %d", 1+bootstrapRetries, n)
	}
}

func countCalls(calls [][]string, sub string) int {
	n := 0
	for _, c := range calls {
		if len(c) > 1 && c[1] == sub {
			n++
		}
	}
	return n
}

func TestAutostartUninstallWhenMissing(t *testing.T) {
	_, rec := autostartEnv(t)
	rec.respond = func([]string) (string, error) { return "not loaded", exitErr(1) }
	for _, goos := range []string{"darwin", "linux"} {
		var out, errOut bytes.Buffer
		if code := runAutostartOS(goos, 1000, []string{"--uninstall"}, &out, &errOut); code != 0 {
			t.Fatalf("%s: 没装过时卸载不算错: %d %s", goos, code, errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := runAutostartOS("linux", 0, []string{"--system", "--hub", "--uninstall"}, &out, &errOut); code != 0 {
		t.Fatalf("系统单元没装过时卸载不算错: %d %s", code, errOut.String())
	}
	// dry-run 卸载不跑命令
	rec.calls = nil
	if code := runAutostartOS("linux", 1000, []string{"--system", "--hub", "--uninstall", "--dry-run"}, &out, &errOut); code != 0 || len(rec.calls) != 0 {
		t.Fatalf("dry-run 卸载: %d %v", code, rec.calls)
	}
}

func TestAutostartUninstallStopFailure(t *testing.T) {
	home, rec := autostartEnv(t)
	plist := filepath.Join(home, "Library", "LaunchAgents", "dev.railse.agent.plist")
	unit := filepath.Join(home, ".config", "systemd", "user", "railse-agent.service")
	place := func() {
		for _, p := range []string{plist, unit} {
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte("x"), 0o644)
		}
	}
	cases := []struct {
		name     string
		goos     string
		out      string
		err      error
		code     int
		keepFile bool
	}{
		{"launchd 真失败", "darwin", "Boot-out failed: 5: Input/output error", exitErr(5), 1, true},
		{"launchd 本来没加载（3）", "darwin", "Boot-out failed: 3: No such process", exitErr(3), 0, false},
		{"launchd 本来没加载（113）", "darwin", "Could not find specified service", exitErr(113), 0, false},
		{"launchd 本来没加载（只看输出）", "darwin", "Could not find service \"dev.railse.agent\" in domain for uid: 501", errors.New("exit status 9"), 0, false},
		{"systemd 失败", "linux", "Failed to connect to bus: No medium found", exitErr(1), 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			place()
			rec.calls = nil
			rec.respond = func(call []string) (string, error) {
				if call[1] == "bootout" || (len(call) > 3 && call[2] == "disable") {
					return c.out, c.err
				}
				return "", nil
			}
			path := plist
			if c.goos == "linux" {
				path = unit
			}
			var out, errOut bytes.Buffer
			code := runAutostartOS(c.goos, 501, []string{"--uninstall"}, &out, &errOut)
			_, serr := os.Stat(path)
			if code != c.code || (serr == nil) != c.keepFile {
				t.Fatalf("想要退出 %d、文件保留=%v，得到 %d、%v；stdout %q stderr %q", c.code, c.keepFile, code, serr, out.String(), errOut.String())
			}
			if c.code == 1 && (!strings.Contains(errOut.String(), c.out) || strings.Contains(out.String(), "已停止")) {
				t.Fatalf("失败时要报出命令输出、不能说已停止: %q %q", out.String(), errOut.String())
			}
			if c.code == 1 && countCalls(rec.calls, "daemon-reload") != 0 {
				t.Fatalf("停止失败时不该再 daemon-reload: %v", rec.calls)
			}
		})
	}
}

func TestAutostartStatus(t *testing.T) {
	home, rec := autostartEnv(t)
	plist := filepath.Join(home, "Library", "LaunchAgents", "dev.railse.agent.plist")
	cases := []struct {
		name   string
		goos   string
		plist  bool
		output string
		err    error
		code   int
		want   string
	}{
		{"在跑", "darwin", true, "gui/501/dev.railse.agent = {\n\tactive count = 1\n\tstate = running\n", nil, 0, "在跑"},
		{"加载了但在等", "darwin", true, "gui/501/dev.railse.agent = {\n\tstate = waiting\n", nil, 1, "waiting"},
		{"没加载", "darwin", true, "Could not find service", exitErr(113), 1, "没在跑"},
		{"没托管", "darwin", false, "Could not find service", exitErr(113), 1, "没有托管"},
		{"systemd 失败", "linux", true, "failed\n", exitErr(3), 1, "failed"},
		{"systemd 在跑", "linux", true, "active\n", nil, 0, "在跑"},
	}
	unit := filepath.Join(home, ".config", "systemd", "user", "railse-agent.service")
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			os.Remove(plist)
			os.Remove(unit)
			if c.plist {
				os.MkdirAll(filepath.Dir(plist), 0o755)
				os.WriteFile(plist, []byte("x"), 0o644)
				os.MkdirAll(filepath.Dir(unit), 0o755)
				os.WriteFile(unit, []byte("x"), 0o644)
			}
			rec.respond = func([]string) (string, error) { return c.output, c.err }
			var out, errOut bytes.Buffer
			if code := runAutostartOS(c.goos, 501, []string{"--status"}, &out, &errOut); code != c.code || !strings.Contains(out.String(), c.want) {
				t.Fatalf("想要 %d 含 %q，得到 %d %q %q", c.code, c.want, code, out.String(), errOut.String())
			}
		})
	}
	// dry-run 的 status 只打印命令
	rec.calls = nil
	var out, errOut bytes.Buffer
	if code := runAutostartOS("darwin", 501, []string{"--status", "--dry-run"}, &out, &errOut); code != 0 || len(rec.calls) != 0 || !strings.Contains(out.String(), "$ launchctl print gui/501/dev.railse.agent") {
		t.Fatalf("dry-run status: %d %v %q", code, rec.calls, out.String())
	}
}

func TestAutostartDefaultDirs(t *testing.T) {
	home, _ := autostartEnv(t)
	var out, errOut bytes.Buffer
	if code := runAutostartOS("linux", 1000, []string{"--dry-run", "--bin", "/opt/railse"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "Environment=RAILSE_HOME="+filepath.Join(home, ".railse")+"\n") {
		t.Fatalf("守护进程默认目录: %d %s", code, out.String())
	}
	t.Setenv("RAILSE_HUB_HOME", filepath.Join(home, "hubdata"))
	out.Reset()
	if code := runAutostartOS("linux", 1000, []string{"--hub", "--dry-run", "--bin", "/opt/railse"}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "Environment=RAILSE_HUB_HOME="+filepath.Join(home, "hubdata")+"\n") {
		t.Fatalf("hub 默认目录看 RAILSE_HUB_HOME: %d %s", code, out.String())
	}
	// 相对路径转成绝对路径：launchd 与 systemd 不认相对路径
	out.Reset()
	if code := runAutostartOS("linux", 1000, []string{"--dry-run", "--bin", "railse", "--dir", "rel"}, &out, &errOut); code != 0 {
		t.Fatalf("相对路径: %d %s", code, errOut.String())
	}
	cwd, _ := os.Getwd()
	if !strings.Contains(out.String(), "ExecStart="+filepath.Join(cwd, "railse")+" agent run\n") || !strings.Contains(out.String(), "RAILSE_HOME="+filepath.Join(cwd, "rel")+"\n") {
		t.Fatalf("应转成绝对路径:\n%s", out.String())
	}
}

func TestSetupAutostartDispatch(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("只在 macOS 与 Linux 上有")
	}
	if os.Getuid() == 0 {
		t.Skip("root 装用户级托管会被拒绝")
	}
	home, rec := autostartEnv(t)
	var out, errOut bytes.Buffer
	if code := run([]string{"setup", "autostart", "stray"}, &out, &errOut); code != 2 {
		t.Fatalf("多余参数: %d", code)
	}
	if code := run([]string{"setup", "autostart", "--dry-run", "--bin", "/opt/railse", "--dir", filepath.Join(home, "d")}, &out, &errOut); code != 0 || len(rec.calls) != 0 {
		t.Fatalf("dry-run: %d %v %s", code, rec.calls, errOut.String())
	}
	if !strings.Contains(out.String(), "<plist") && !strings.Contains(out.String(), "ExecStart=") {
		t.Fatalf("dry-run 应打印托管文件: %s", out.String())
	}
	errOut.Reset()
	if code := run([]string{"setup"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "setup autostart") {
		t.Fatalf("setup 用法应列出 autostart: %d %s", code, errOut.String())
	}
}
