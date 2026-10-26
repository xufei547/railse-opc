package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/hooksconf"
)

// codexEnv 给 setup codex-notify 一个临时 HOME（没有 $RAILSE_HOME），返回家目录、~/.codex/config.toml 与默认的串接脚本路径。
func codexEnv(t *testing.T) (home, cfg, script string) {
	t.Helper()
	home, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("RAILSE_HOME", "")
	return home, hooksconf.CodexConfigPath(home), filepath.Join(home, ".railse", "codex-notify.sh")
}

// codexNotify 跑 railse setup codex-notify，返回 stdout、stderr 与退出码。
func codexNotify(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(append([]string{"setup", "codex-notify"}, args...), &out, &errOut)
	return out.String(), errOut.String(), code
}

// notifyLine 是 config.toml 里指向 script 的那一行（临时目录的路径用 %q 转义与 TOML 基本字符串一致）。
func notifyLine(script string) string { return fmt.Sprintf("notify = [%q]", script) }

// writeExec 建好上级目录再写一个 0755 的可执行文件。
func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

// fixBackupTime 把备份文件名里的时间固定成 at（测试结束时还原）。
func fixBackupTime(t *testing.T, at time.Time) {
	t.Helper()
	old := backupNow
	backupNow = func() time.Time { return at }
	t.Cleanup(func() { backupNow = old })
}

// 没有 config.toml：新建 0600、只有 notify 一行；脚本 0755、标记行是 []、没有前一跳那一行；数据目录按 0700 建；不备份。
func TestSetupCodexNotifyNoConfig(t *testing.T) {
	home, cfg, script := codexEnv(t)
	out, errOut, code := codexNotify(t, "--bin", "/opt/railse")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if got := string(readFile(t, cfg)); got != notifyLine(script)+"\n" {
		t.Fatalf("config.toml 应只有 notify 一行: %q", got)
	}
	if m := fileMode(t, cfg); m != 0o600 {
		t.Fatalf("新建的 config.toml 应是 0600，得到 %o", m)
	}
	body := readFile(t, script)
	if !bytes.Equal(body, hooksconf.NotifyScript(nil, "/opt/railse", "")) {
		t.Fatalf("脚本:\n%s", body)
	}
	if !strings.Contains(string(body), "# railse-prev-notify: []\n") || strings.Count(string(body), `"$@"`) != 2 { // 注释里一个、railse 那一行一个
		t.Fatalf("没有前一跳时标记行是 []、只有 railse 那一行:\n%s", body)
	}
	if m := fileMode(t, script); m != 0o755 {
		t.Fatalf("脚本应是 0755，得到 %o", m)
	}
	if m := fileMode(t, filepath.Join(home, ".railse")); m != 0o700 {
		t.Fatalf("数据目录应按 0700 建，得到 %o", m)
	}
	if b := backups(t, cfg); len(b) != 0 {
		t.Fatalf("原来没有 config.toml 时不备份: %v", b)
	}
	if !strings.Contains(out, "已写入 "+script) || !strings.Contains(out, cfg) {
		t.Fatalf("应说明写了哪些文件:\n%s", out)
	}
}

// 已有别人的 notify（跨行、带注释与逗号结尾、单双引号混用），文件里还有别的顶层键与表：前一跳的参数逐个正确，
// config.toml 只有 notify 那一段变了，改之前备份（逐字节等于原文件、0600），原文件的权限沿用。
// 再跑一次什么都不变、不再备份；换 --bin 再跑，脚本更新、前一跳仍在。
func TestSetupCodexNotifyChainsExisting(t *testing.T) {
	_, cfg, script := codexEnv(t)
	oldNotify := "notify = [ # 桌面通知\n  \"/usr/local/bin/notify wrap\",\n  '--title=\"Codex\"', # 标题\n  \"--sound\\tdefault\",\n]"
	orig := "# 我的 Codex 配置\nmodel = \"o3\"\n" + oldNotify + " # 尾注释\napproval_policy = \"on-request\"\n\n" +
		"[mcp_servers.foo]\ncommand = \"npx\"\nargs = [\"-y\", \"foo\"]\n\n[tui]\nnotifications = true\n"
	writeTestFile(t, cfg, orig)
	out, errOut, code := codexNotify(t, "--bin", "/opt/railse")
	if code != 0 || errOut != "" {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	prev := []string{"/usr/local/bin/notify wrap", `--title="Codex"`, "--sound\tdefault"}
	body := readFile(t, script)
	if !bytes.Equal(body, hooksconf.NotifyScript(prev, "/opt/railse", "")) {
		t.Fatalf("脚本的前一跳应是原来的 notify:\n%s", body)
	}
	if want := `'/usr/local/bin/notify wrap' '--title="Codex"' '--sound` + "\t" + `default' "$@"` + "\n"; !strings.Contains(string(body), want) {
		t.Fatalf("前一跳那一行应是 %q:\n%s", want, body)
	}
	want := strings.Replace(orig, oldNotify, notifyLine(script), 1)
	if got := string(readFile(t, cfg)); got != want {
		t.Fatalf("config.toml 只该换 notify 那一段:\n%s\n---\n%s", got, want)
	}
	if m := fileMode(t, cfg); m != 0o644 {
		t.Fatalf("config.toml 应沿用原来的权限 0644，得到 %o", m)
	}
	baks := backups(t, cfg)
	if len(baks) != 1 || string(readFile(t, baks[0])) != orig || fileMode(t, baks[0]) != 0o600 {
		t.Fatalf("应有一个 0600、内容是原文件的备份: %v", baks)
	}
	if !strings.Contains(out, "已备份 "+cfg+" 到 "+baks[0]) || !strings.Contains(out, "/usr/local/bin/notify") {
		t.Fatalf("应打印备份到了哪里、串了哪个前一跳:\n%s", out)
	}

	// 再跑一次：脚本字节相同、config.toml 不变、不再备份，不把自己当前一跳
	out, errOut, code = codexNotify(t, "--bin", "/opt/railse")
	if code != 0 || errOut != "" {
		t.Fatalf("重跑: code=%d stderr=%s", code, errOut)
	}
	if !bytes.Equal(readFile(t, script), body) || string(readFile(t, cfg)) != want || len(backups(t, cfg)) != 1 {
		t.Fatalf("重跑应什么都不变: %v\n%s", backups(t, cfg), readFile(t, script))
	}
	if !strings.Contains(out, "已是最新") || strings.Contains(out, "已备份") {
		t.Fatalf("重跑应打印已是最新:\n%s", out)
	}

	// 换 --bin：脚本里 railse 那一行跟着换，前一跳仍在；config.toml 不变
	if _, errOut, code = codexNotify(t, "--bin", "/opt/new/railse"); code != 0 {
		t.Fatalf("换 --bin: code=%d stderr=%s", code, errOut)
	}
	if got := readFile(t, script); !bytes.Equal(got, hooksconf.NotifyScript(prev, "/opt/new/railse", "")) {
		t.Fatalf("换 --bin 后脚本应更新、前一跳保留:\n%s", got)
	}
	if string(readFile(t, cfg)) != want || len(backups(t, cfg)) != 1 {
		t.Fatalf("换 --bin 不该改 config.toml: %v", backups(t, cfg))
	}
}

// 直接调 railse 的旧写法：替换掉，没有前一跳（不串两个 railse）。
func TestSetupCodexNotifyReplacesOldRailse(t *testing.T) {
	_, cfg, script := codexEnv(t)
	writeTestFile(t, cfg, "model = \"x\"\nnotify = [\"/usr/local/bin/railse\", \"hook\", \"codex-notify\", \"--socket\", \"/old/agent.sock\"]\n")
	if _, errOut, code := codexNotify(t, "--bin", "/opt/railse"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if got := readFile(t, script); !bytes.Equal(got, hooksconf.NotifyScript(nil, "/opt/railse", "")) {
		t.Fatalf("旧写法不该成为前一跳:\n%s", got)
	}
	if got := string(readFile(t, cfg)); got != "model = \"x\"\n"+notifyLine(script)+"\n" {
		t.Fatalf("config.toml: %q", got)
	}
}

// 非默认 --dir：脚本放在那个目录里，railse 那一行带 --socket，config.toml 指向它。
// 再换回默认目录：前一跳从上次的脚本（文件名是 codex-notify.sh、有标记行）里找回，不把那个脚本当前一跳。
func TestSetupCodexNotifyNonDefaultDir(t *testing.T) {
	home, cfg, defScript := codexEnv(t)
	writeTestFile(t, cfg, "notify = [\"/bin/other\", \"--x\"]\n")
	data := filepath.Join(home, "data dir")
	if _, errOut, code := codexNotify(t, "--dir", data, "--bin", "/opt/railse"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	script := filepath.Join(data, "codex-notify.sh")
	prev := []string{"/bin/other", "--x"}
	body := readFile(t, script)
	if !bytes.Equal(body, hooksconf.NotifyScript(prev, "/opt/railse", filepath.Join(data, "agent.sock"))) ||
		!strings.Contains(string(body), "--socket "+hooksconf.ShellQuote(filepath.Join(data, "agent.sock"))) {
		t.Fatalf("railse 那一行应带 --socket:\n%s", body)
	}
	if m := fileMode(t, data); m != 0o700 {
		t.Fatalf("数据目录应按 0700 建，得到 %o", m)
	}
	if got := string(readFile(t, cfg)); got != notifyLine(script)+"\n" {
		t.Fatalf("config.toml 应指向 %s: %q", script, got)
	}
	if _, err := os.Stat(defScript); err == nil {
		t.Fatal("给了 --dir 时不该在 ~/.railse 里写脚本")
	}

	if _, errOut, code := codexNotify(t, "--bin", "/opt/railse"); code != 0 {
		t.Fatalf("换回默认目录: code=%d stderr=%s", code, errOut)
	}
	if got := readFile(t, defScript); !bytes.Equal(got, hooksconf.NotifyScript(prev, "/opt/railse", "")) {
		t.Fatalf("换目录后前一跳应保留、不带 --socket:\n%s", got)
	}
	if got := string(readFile(t, cfg)); got != notifyLine(defScript)+"\n" {
		t.Fatalf("config.toml 应改指 %s: %q", defScript, got)
	}
	if b := backups(t, cfg); len(b) != 2 {
		t.Fatalf("每次真的改了 config.toml 都备份: %v", b)
	}
}

// 认作我们的脚本（与这次的脚本是同一个文件）却读不到标记行：没有前一跳，警告。
func TestSetupCodexNotifyUnreadableOwnScript(t *testing.T) {
	_, cfg, script := codexEnv(t)
	writeTestFile(t, cfg, notifyLine(script)+"\n")
	_, errOut, code := codexNotify(t, "--bin", "/opt/railse")
	if code != 0 || !strings.Contains(errOut, "警告") || !strings.Contains(errOut, script) {
		t.Fatalf("脚本不在时应警告: code=%d stderr=%s", code, errOut)
	}
	if got := readFile(t, script); !bytes.Equal(got, hooksconf.NotifyScript(nil, "/opt/railse", "")) {
		t.Fatalf("不该把自己当前一跳:\n%s", got)
	}
}

// 上次的脚本（别的数据目录里，文件名是 codex-notify.sh）被删了：认作我们的脚本，读不到它记的前一跳 → 没有前一跳、警告，
// 不把那个不存在的路径串成前一跳（原来的 notify 只剩在备份里，警告指向备份）。
func TestSetupCodexNotifyDeletedOldScript(t *testing.T) {
	home, cfg, defScript := codexEnv(t)
	writeTestFile(t, cfg, "notify = [\"/usr/bin/true\", \"--x\"]\n")
	old := filepath.Join(home, "A", "codex-notify.sh")
	if _, errOut, code := codexNotify(t, "--dir", filepath.Dir(old), "--bin", "/opt/railse"); code != 0 {
		t.Fatalf("--dir A: code=%d stderr=%s", code, errOut)
	}
	if err := os.Remove(old); err != nil {
		t.Fatal(err)
	}
	out, errOut, code := codexNotify(t, "--bin", "/opt/railse")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if got := readFile(t, defScript); !bytes.Equal(got, hooksconf.NotifyScript(nil, "/opt/railse", "")) {
		t.Fatalf("读不到的旧脚本不该成为前一跳（标记行应是 []、没有前一跳那一行）:\n%s", got)
	}
	if !strings.Contains(errOut, "警告") || !strings.Contains(errOut, old) || !strings.Contains(errOut, cfg+".railse-bak-") {
		t.Fatalf("应警告前一跳没有保留、指向备份: %s", errOut)
	}
	if !strings.Contains(out, "只调 railse") {
		t.Fatalf("串接说明应是只调 railse:\n%s", out)
	}
}

// 前一跳里有这次脚本的别名（经符号链接的目录，脚本还没写出来，samePath 认不出）：不串，警告，脚本绝不调自己。
// 第一个元素就是别名时按我们的脚本处理（读不到标记行）；别名在后面的元素里（如 ["/bin/sh", 脚本]）时兜底丢掉前一跳。
func TestSetupCodexNotifyNeverChainsItself(t *testing.T) {
	needUnixShell(t)
	home, cfg, script := codexEnv(t)
	if err := os.MkdirAll(filepath.Dir(script), 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(home, "alias", "codex-notify.sh")
	if err := os.Symlink(filepath.Dir(script), filepath.Dir(alias)); err != nil {
		t.Fatal(err)
	}
	for _, notify := range []string{
		fmt.Sprintf("notify = [%q]\n", alias),
		fmt.Sprintf("notify = [\"/bin/sh\", %q]\n", alias),
	} {
		if err := os.Remove(script); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		writeTestFile(t, cfg, notify)
		_, errOut, code := codexNotify(t, "--bin", "/opt/railse")
		if code != 0 {
			t.Fatalf("%s: code=%d stderr=%s", notify, code, errOut)
		}
		if got := readFile(t, script); !bytes.Equal(got, hooksconf.NotifyScript(nil, "/opt/railse", "")) {
			t.Fatalf("%s: 脚本不该调自己:\n%s", notify, got)
		}
		if !strings.Contains(errOut, "警告") || !strings.Contains(errOut, alias) {
			t.Fatalf("%s: 应警告: %s", notify, errOut)
		}
	}
}

// 同一秒里两次真的改 config.toml：都成功，第二个备份名加 -2，两个备份各是那一次之前的内容。
func TestSetupCodexNotifyBackupsSameSecond(t *testing.T) {
	home, cfg, _ := codexEnv(t)
	fixBackupTime(t, time.Date(2026, 10, 27, 12, 0, 0, 0, time.Local))
	orig := "notify = [\"/bin/other\"]\n"
	writeTestFile(t, cfg, orig)
	if _, errOut, code := codexNotify(t, "--dir", filepath.Join(home, "A"), "--bin", "/opt/railse"); code != 0 {
		t.Fatalf("--dir A: code=%d stderr=%s", code, errOut)
	}
	afterA := string(readFile(t, cfg))
	out, errOut, code := codexNotify(t, "--dir", filepath.Join(home, "B"), "--bin", "/opt/railse")
	if code != 0 {
		t.Fatalf("同一秒里第二次改也应成功: code=%d stderr=%s", code, errOut)
	}
	base := cfg + ".railse-bak-20261027-120000"
	if got := string(readFile(t, base)); got != orig {
		t.Fatalf("第一个备份应是原文件: %q", got)
	}
	if got := string(readFile(t, base+"-2")); got != afterA || fileMode(t, base+"-2") != 0o600 {
		t.Fatalf("第二个备份应是 %s-2（0600），内容是第一次改之后的: %q", base, got)
	}
	if b := backups(t, cfg); len(b) != 2 {
		t.Fatalf("应有两个备份: %v", b)
	}
	if !strings.Contains(out, "已备份 "+cfg+" 到 "+base+"-2") {
		t.Fatalf("应打印第二个备份的名字:\n%s", out)
	}
}

// writeBackup 遇到同名文件依次试 -2、-3…（仍 O_EXCL，不覆盖），试满 100 个都被占才报错。
func TestWriteBackupSameSecondLimit(t *testing.T) {
	dir := t.TempDir()
	show := filepath.Join(dir, "config.toml")
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.Local)
	fixBackupTime(t, at)
	base := backupPath(show, at)
	taken := []string{base}
	for i := 2; i <= 100; i++ {
		taken = append(taken, fmt.Sprintf("%s-%d", base, i))
	}
	for _, p := range taken {
		writeTestFile(t, p, "old")
	}
	if bak, err := writeBackup(show, []byte("new")); err == nil {
		t.Fatalf("100 个名字都被占时应报错，得到 %s", bak)
	}
	for _, p := range taken {
		if string(readFile(t, p)) != "old" {
			t.Fatalf("不该覆盖已有的备份 %s", p)
		}
	}
	if err := os.Remove(taken[56]); err != nil {
		t.Fatal(err)
	}
	if bak, err := writeBackup(show, []byte("new")); err != nil || bak != taken[56] || string(readFile(t, bak)) != "new" {
		t.Fatalf("应写到第一个空着的名字 %s: %s %v", taken[56], bak, err)
	}
}

// --dry-run：打印脚本与改写后的 config.toml，什么都不写（不建数据目录、不备份、不建 ~/.codex）。
func TestSetupCodexNotifyDryRun(t *testing.T) {
	home, cfg, script := codexEnv(t)
	out, errOut, code := codexNotify(t, "--bin", "/opt/railse", "--dry-run")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	for _, p := range []string{filepath.Join(home, ".railse"), filepath.Join(home, ".codex")} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("dry-run 不该建 %s", p)
		}
	}
	if !strings.Contains(out, string(hooksconf.NotifyScript(nil, "/opt/railse", ""))) || !strings.Contains(out, notifyLine(script)) {
		t.Fatalf("dry-run 应打印脚本与 config.toml:\n%s", out)
	}
	orig := "notify = [\"/bin/other\"]\n[tui]\nx = 1\n"
	writeTestFile(t, cfg, orig)
	out, _, code = codexNotify(t, "--bin", "/opt/railse", "--dry-run")
	if code != 0 || string(readFile(t, cfg)) != orig || len(backups(t, cfg)) != 0 {
		t.Fatalf("dry-run 不该改 config.toml 也不该备份: %d %v", code, backups(t, cfg))
	}
	if _, err := os.Stat(script); err == nil {
		t.Fatal("dry-run 不该写脚本")
	}
	if !strings.Contains(out, "会先备份到 "+cfg+".railse-bak-") || !strings.Contains(out, notifyLine(script)+"\n[tui]\nx = 1\n") ||
		!strings.Contains(out, string(hooksconf.NotifyScript([]string{"/bin/other"}, "/opt/railse", ""))) {
		t.Fatalf("dry-run 应打印改写后的内容并说会备份到哪里:\n%s", out)
	}
}

// notify 只在某个表下面（Codex 读不到）：照样在最前面插顶层的，警告那个表下面的被忽略、请手动删；它不当前一跳。
func TestSetupCodexNotifyUnderTable(t *testing.T) {
	_, cfg, script := codexEnv(t)
	orig := "model = \"x\"\n[mcp_servers.foo]\ncommand = \"foo\"\nnotify = [\"/bin/other\"]\n"
	writeTestFile(t, cfg, orig)
	_, errOut, code := codexNotify(t, "--bin", "/opt/railse")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if got := string(readFile(t, cfg)); got != notifyLine(script)+"\n"+orig {
		t.Fatalf("应在最前面插顶层的 notify: %q", got)
	}
	if !strings.Contains(errOut, "警告") || !strings.Contains(errOut, "[mcp_servers.foo]") || !strings.Contains(errOut, "手动删") {
		t.Fatalf("应警告表下面的 notify 被忽略: %s", errOut)
	}
	if got := readFile(t, script); !bytes.Equal(got, hooksconf.NotifyScript(nil, "/opt/railse", "")) {
		t.Fatalf("表下面的 notify 不当前一跳:\n%s", got)
	}
}

// 看不懂的写法：退出 1，说明请手动改，什么都不写（config.toml 不变、没有脚本、没有备份）。
func TestSetupCodexNotifyRejectsUnknown(t *testing.T) {
	home, cfg, script := codexEnv(t)
	for _, orig := range []string{"notify = \"\"\"/bin/x\"\"\"\n", "notify = [\"/bin/x\", 1]\n", "model = \"x\"\nnotify = [\"/bin/x\",\n"} {
		writeTestFile(t, cfg, orig)
		_, errOut, code := codexNotify(t, "--bin", "/opt/railse")
		if code != 1 || !strings.Contains(errOut, "手动") || !strings.Contains(errOut, cfg) {
			t.Fatalf("%q 应退出 1 并说明请手动改: %d %s", orig, code, errOut)
		}
		if string(readFile(t, cfg)) != orig || len(backups(t, cfg)) != 0 {
			t.Fatalf("%q: config.toml 应不变、不备份", orig)
		}
		if _, err := os.Stat(filepath.Join(home, ".railse")); err == nil {
			t.Fatalf("%q: 不该写脚本 %s", orig, script)
		}
	}
	if _, errOut, code := codexNotify(t, "extra"); code != 2 || !strings.Contains(errOut, "setup codex-notify") {
		t.Fatalf("多余的参数应是用法错误: %d %s", code, errOut)
	}
}

// config.toml 是符号链接（dotfiles）：写到链接指向的文件、沿用它的权限，链接保留；备份放在链接旁边、内容取链接指向的文件。
func TestSetupCodexNotifyFollowsSymlink(t *testing.T) {
	needUnixShell(t)
	home, cfg, script := codexEnv(t)
	real := filepath.Join(home, "dotfiles", "codex.toml")
	orig := "notify = [\"/bin/other\"]\n"
	writeTestFile(t, real, orig)
	if err := os.Chmod(real, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, cfg); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := codexNotify(t, "--bin", "/opt/railse"); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	if st, err := os.Lstat(cfg); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("符号链接应保留: %v %v", st, err)
	}
	if got := string(readFile(t, real)); got != notifyLine(script)+"\n" || fileMode(t, real) != 0o640 {
		t.Fatalf("应写到链接指向的文件并沿用权限: %q %o", got, fileMode(t, real))
	}
	baks := backups(t, cfg)
	if len(baks) != 1 || string(readFile(t, baks[0])) != orig {
		t.Fatalf("备份应在链接旁边、内容是原文件: %v", baks)
	}
	if ents, _ := os.ReadDir(filepath.Dir(real)); len(ents) != 1 {
		t.Fatalf("dotfiles 目录里不该有备份或临时文件: %v", ents)
	}
}

// 真跑生成的脚本：假前一跳与假 railse 各自把收到的参数写到文件，两者都收到同一个 JSON 参数；
// 前一跳 exit 1 时 railse 仍被调用，脚本退出 0。
func TestSetupCodexNotifyScriptRuns(t *testing.T) {
	needUnixShell(t)
	home, cfg, _ := codexEnv(t)
	dir := filepath.Join(home, "my bin")
	prevOut, railseOut := filepath.Join(home, "prev.out"), filepath.Join(home, "railse.out")
	prev := filepath.Join(dir, "notify wrap")
	bin := filepath.Join(dir, "railse")
	writeExec(t, prev, "#!/bin/sh\nprintf '%s\\n' \"$@\" > "+hooksconf.ShellQuote(prevOut)+"\nexit 1\n")
	writeExec(t, bin, "#!/bin/sh\nprintf '%s\\n' \"$@\" > "+hooksconf.ShellQuote(railseOut)+"\n")
	writeTestFile(t, cfg, fmt.Sprintf("notify = [%q, '--a b']\n", prev))
	data := filepath.Join(home, "data")
	if _, errOut, code := codexNotify(t, "--bin", bin, "--dir", data); code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	script := filepath.Join(data, "codex-notify.sh")
	payload := `{"type":"agent-turn-complete","last-assistant-message":"it's \"done\" $HOME"}`
	if out, err := exec.Command(script, payload).CombinedOutput(); err != nil {
		t.Fatalf("前一跳失败时脚本也应退出 0: %v %s", err, out)
	}
	lines := func(p string) []string {
		return strings.Split(strings.TrimSuffix(string(readFile(t, p)), "\n"), "\n")
	}
	if got := lines(prevOut); !slices.Equal(got, []string{"--a b", payload}) {
		t.Fatalf("前一跳收到 %q", got)
	}
	if got, want := lines(railseOut), []string{"hook", "codex-notify", "--socket", filepath.Join(data, "agent.sock"), payload}; !slices.Equal(got, want) {
		t.Fatalf("railse 收到 %q，想要 %q", got, want)
	}
}

// 用法里有 setup codex-notify。
func TestSetupUsageMentionsCodexNotify(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"setup"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "railse setup codex-notify [--dir 目录] [--bin 路径] [--dry-run]") {
		t.Fatalf("用法应列出 setup codex-notify: %d %s", code, errOut.String())
	}
}
