package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
)

// runRoleIn 在 cwd 里跑 railse role，返回退出码与两路输出。
func runRoleIn(t *testing.T, cwd string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Chdir(cwd)
	var out, errb bytes.Buffer
	code = run(append([]string{"role"}, args...), &out, &errb)
	return code, out.String(), errb.String()
}

// noDaemonDir 是没有守护进程在跑的配置目录（socket 不存在）。
func noDaemonDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "h")
}

func TestRoleUsage(t *testing.T) {
	cwd := t.TempDir()
	if code, _, errOut := runRoleIn(t, cwd); code != 2 || !strings.Contains(errOut, "railse role list") {
		t.Fatalf("不带子命令: %d %q", code, errOut)
	}
	if code, out, _ := runRoleIn(t, cwd, "--help"); code != 0 || !strings.Contains(out, "RAILSE_ROLE") {
		t.Fatalf("--help: %d %q", code, out)
	}
	if code, _, _ := runRoleIn(t, cwd, "bogus"); code != 2 {
		t.Fatalf("未知子命令应退出 2: %d", code)
	}
	if code, _, _ := runRoleIn(t, cwd, "list", "extra", "--dir", noDaemonDir(t)); code != 2 {
		t.Fatalf("多余参数应退出 2: %d", code)
	}
	var out, errb bytes.Buffer
	if code := run([]string{"--help"}, &out, &errb); code != 0 || !strings.Contains(out.String(), "role      ") {
		t.Fatalf("railse --help 应列出 role: %q", out.String())
	}
}

// 守护进程不在：list、which 直接读文件并注明；init 写模板、不覆盖；check 校验。
func TestRoleCommandsWithoutDaemon(t *testing.T) {
	dir := noDaemonDir(t)
	cwd := t.TempDir()
	code, out, errOut := runRoleIn(t, cwd, "list", "--dir", dir)
	if code != 0 || !strings.Contains(errOut, "守护进程没在运行，直接读文件") || !strings.Contains(out, "没有角色文件") {
		t.Fatalf("没有文件时 list: %d %q %q", code, out, errOut)
	}
	if code, out, _ := runRoleIn(t, cwd, "check", "--dir", dir); code != 0 || !strings.Contains(out, "没有角色文件") {
		t.Fatalf("没有文件时 check: %d %q", code, out)
	}

	code, out, errOut = runRoleIn(t, cwd, "init", "--dir", dir)
	if code != 0 || !strings.Contains(out, "RAILSE_ROLE=开发 claude") {
		t.Fatalf("init: %d %q %q", code, out, errOut)
	}
	path := filepath.Join(dir, "roles.yaml")
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("模板应是 0600: %v %v", st, err)
	}
	before, _ := os.ReadFile(path)
	if code, _, errOut := runRoleIn(t, cwd, "init", "--dir", dir); code != 1 || !strings.Contains(errOut, "已存在") {
		t.Fatalf("再 init 应拒绝: %d %q", code, errOut)
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("再 init 不该改文件")
	}

	code, out, _ = runRoleIn(t, cwd, "list", "--dir", dir)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if code != 0 || len(lines) != 3 || !strings.HasPrefix(lines[0], "开发  上限 network") || !strings.Contains(lines[0], "放行最长 24 小时") ||
		!strings.Contains(lines[1], "禁用 2 条") || !strings.Contains(lines[2], "不走限时放行") {
		t.Fatalf("list 模板: %d %q", code, out)
	}
	if code, out, _ := runRoleIn(t, cwd, "check", "--dir", dir); code != 0 || !strings.Contains(out, "合法（3 个角色）") {
		t.Fatalf("check 模板: %d %q", code, out)
	}
	// 模板的 dirs 都注释着：这里是未分配
	if code, out, errOut := runRoleIn(t, cwd, "which", "--dir", dir); code != 0 || !strings.Contains(out, "未分配") || !strings.Contains(errOut, "直接读文件") {
		t.Fatalf("which 未分配: %d %q %q", code, out, errOut)
	}
	t.Setenv("RAILSE_ROLE", "写作")
	if code, out, _ := runRoleIn(t, cwd, "which", "--dir", dir); code != 0 || !strings.Contains(out, "角色：写作（RAILSE_ROLE=写作）") {
		t.Fatalf("which 按环境变量: %d %q", code, out)
	}
	t.Setenv("RAILSE_ROLE", "不存在")
	if code, out, _ := runRoleIn(t, cwd, "which", "--dir", dir); code != 0 || !strings.Contains(out, "未分配") || !strings.Contains(out, "RAILSE_ROLE=不存在 在 roles.yaml 里没有") {
		t.Fatalf("which 不存在的名字: %d %q", code, out)
	}

	// 坏文件：list、check、which 都报错退出 1
	os.WriteFile(path, []byte("version: 1\nroles:\n  - name: a\n    ceiling: hgih\n"), 0o600)
	for _, sub := range []string{"list", "check", "which"} {
		if code, _, errOut := runRoleIn(t, cwd, sub, "--dir", dir); code != 1 || !strings.Contains(errOut, "hgih") || !strings.Contains(errOut, "只读之外") {
			t.Fatalf("坏文件时 %s: %d %q", sub, code, errOut)
		}
	}
}

func TestRoleCheckWarnings(t *testing.T) {
	dir := noDaemonDir(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, "code", "a"), 0o755)
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "roles.yaml"), []byte(`version: 1
roles:
  - name: 开发
    dirs: ["~/code/**"]
  - name: 写作
    dirs: ["~/code/a", "~/nowhere/*"]
`), 0o600)
	code, out, errOut := runRoleIn(t, home, "check", "--dir", dir)
	if code != 0 || !strings.Contains(out, "角色「开发」与「写作」的目录模式重叠") || !strings.Contains(out, "~/nowhere/*") ||
		!strings.Contains(out, "有 2 条提醒") {
		t.Fatalf("check 提醒: %d %q %q", code, out, errOut)
	}
}

// 守护进程在跑：list、which 走 socket（不说“直接读文件”），which 按目录绑定并说明凭什么。
func TestRoleCommandsWithDaemon(t *testing.T) {
	_, repo, cfg := startTestAgent(t)
	dir := filepath.Dir(cfg.StatePath)
	real, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(cfg.RolesPath, []byte("version: 1\nroles:\n  - name: 写作\n    dirs: [\""+real+"\"]\n    ceiling: write_in_project\n"), 0o600)
	code, out, errOut := runRoleIn(t, repo, "list", "--dir", dir)
	if code != 0 || errOut != "" || !strings.HasPrefix(out, "写作  上限 write_in_project") {
		t.Fatalf("list 走守护进程: %d %q %q", code, out, errOut)
	}
	t.Setenv("RAILSE_ROLE", "")
	code, out, errOut = runRoleIn(t, repo, "which", "--dir", dir)
	if code != 0 || errOut != "" || !strings.Contains(out, "角色：写作（目录 "+real+" 命中 dirs 里的 "+real+"）") {
		t.Fatalf("which 走守护进程: %d %q %q", code, out, errOut)
	}
	// 用 RAILSE_ROLE 在地盘外：说明写入按项目外
	other := filepath.Join(filepath.Dir(repo), "other")
	os.MkdirAll(filepath.Join(other, ".git"), 0o755)
	t.Setenv("RAILSE_ROLE", "写作")
	if code, out, _ := runRoleIn(t, other, "which", "--dir", dir); code != 0 || !strings.Contains(out, "不在角色「写作」的目录内") {
		t.Fatalf("地盘外: %d %q", code, out)
	}
}

// 钩子把 RAILSE_ROLE 带给守护进程：禁用直接拒，理由按终端语言重拼。
func TestHookCarriesRailseRole(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	os.WriteFile(cfg.RolesPath, []byte("version: 1\nroles:\n  - name: 写作\n    ceiling: network\n    forbid:\n      - {command_prefix: \"git push\"}\n"), 0o600)
	out, _, _ := runHookWith(t, claudePreToolUse(repo, "git push origin main", "default"), "claude", "--socket", sock)
	if strings.Contains(out, "deny") {
		t.Fatalf("没有 RAILSE_ROLE、也没有目录绑定时不该按角色拒: %q", out)
	}
	t.Setenv("RAILSE_ROLE", "写作")
	out, _, _ = runHookWith(t, claudePreToolUse(repo, "git push origin main", "default"), "claude", "--socket", sock)
	if !strings.Contains(out, `"permissionDecision":"deny"`) || !strings.Contains(out, "角色「写作」禁用") {
		t.Fatalf("带 RAILSE_ROLE 时禁用应拒: %q", out)
	}
	// 越界：PreToolUse 回 ask，确认框里是角色的理由
	out, _, _ = runHookWith(t, claudePreToolUse(repo, "rm -rf build", "default"), "claude", "--socket", sock)
	if !strings.Contains(out, `"permissionDecision":"ask"`) || !strings.Contains(out, "超出角色「写作」的边界（上限 network，这次是 destructive）") {
		t.Fatalf("越界应回 ask 并说明: %q", out)
	}
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	out, _, _ = runHookWith(t, claudePreToolUse(repo, "git push origin main", "default"), "claude", "--socket", sock)
	if !strings.Contains(out, `Forbidden for role \"写作\": command_prefix \"git push\"`) {
		t.Fatalf("英文界面的禁用理由: %q", out)
	}
	out, _, _ = runHookWith(t, claudePreToolUse(repo, "rm -rf build", "default"), "claude", "--socket", sock)
	if !strings.Contains(out, `Beyond role \"写作\"'s boundary (ceiling network, this is destructive)`) || hasCJK(strings.ReplaceAll(out, "写作", "")) {
		t.Fatalf("英文界面的越界理由: %q", out)
	}
}

func TestLocalizeRoleReason(t *testing.T) {
	for in, want := range map[string]string{
		`角色「写作」禁用：command_prefix "git push"`: `角色「写作」禁用：command_prefix "git push"`,
		"超出角色「a」的边界（上限 read，这次是 outward）":    "超出角色「a」的边界（上限 read，这次是 outward）",
		"roles.yaml 有误：第 3 行":                "roles.yaml 有误：第 3 行",
	} {
		if got, ok := localizeRoleReason(in); !ok || got != want {
			t.Errorf("%q → %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"命中规则 x", "角色「a」", "超出角色「a」的边界", ""} {
		if _, ok := localizeRoleReason(in); ok {
			t.Errorf("%q 不是角色理由", in)
		}
	}
}

func TestLocalizeOpaqueRoleReason(t *testing.T) {
	const in = "超出角色「a」的边界：含无法检查的子命令"
	if got, ok := localizeRoleReason(in); !ok || got != in || !isRoleReason(got) {
		t.Fatalf("中文: %q %v", got, ok)
	}
	i18n.Set(i18n.EN)
	t.Cleanup(func() { i18n.Set(i18n.ZH) })
	if got, ok := localizeRoleReason(in); !ok || hasCJK(got) || !isRoleReason(got) {
		t.Fatalf("英文: %q %v", got, ok)
	}
}
