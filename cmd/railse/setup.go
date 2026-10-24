package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/autostart"
	"github.com/railse/railse-opc/internal/hookclient"
	"github.com/railse/railse-opc/internal/hooksconf"
)

const setupAllUsage = `  railse setup all --hub URL --token 接入码 [--name 名字] [--project 目录] [--dir 目录] [--bin 路径] [--no-autostart] [--no-hooks] [--force]
    一键：接入 hub → 装三个 CLI 的钩子 → 开机自启 → 体检（railse doctor）。
    已接入过（有 machine.json）时跳过接入，这时可以不给 --hub 与 --token；--force 重新接入。体检的失败只展示，不算 setup all 失败。`

const setupUsage = `用法:
` + setupAllUsage + `
  railse setup hooks [--project 目录] [--claude] [--codex] [--gemini] [--codex-file 路径] [--bin 路径] [--dry-run]
    默认三个都装。Claude 写项目级 .claude/settings.local.json（不入库；旧版写在 settings.json 的会迁走），
    Gemini 写项目级 .gemini/settings.json，Codex 写用户级 ~/.codex/hooks.json。
  railse setup autostart [--hub] [--uninstall] [--status] [--dir 目录] [--bin 路径] [--system] [--user 名字] [--dry-run] [-- hub run 的额外参数]
    开机自启并在退出后自动拉起（macOS launchd、Linux systemd），默认托管守护进程，--hub 托管 hub；详见 railse setup autostart -h。`

// pendingWrite 是算好、待写的一个配置文件。
type pendingWrite struct {
	show, path string // show 是给人看的路径，path 是解开符号链接后真正写的文件
	data       []byte
	mode       os.FileMode
	migrated   bool // 只删不加：把 Railse 钩子从 Claude 的 settings.json 迁走
}

// resolveForWrite 解开符号链接：配置是符号链接（如 dotfiles）时写到链接指向的文件，不把链接换成普通文件。
// 同时取现有文件的权限沿用，文件还不存在时用 0644。
func resolveForWrite(path string) (target string, mode os.FileMode) {
	target, mode = path, 0o644
	if r, err := filepath.EvalSymlinks(path); err == nil {
		target = r
	}
	if st, err := os.Stat(target); err == nil {
		mode = st.Mode().Perm()
	}
	return target, mode
}

// claudeMigration 算出把 Railse 钩子从 Claude 的共享配置 settings.json 迁走后的内容；没有要迁的返回 false，文件不动。
// settings.json 已不是写入目标：读不了、不是合法配置时只在 stderr 警告并跳过，不挡安装。
// settings.local.json 与它是同一个文件（链接）时不迁，否则会把刚装进去的钩子删掉。
func claudeMigration(shared, localReal, bin string, stderr io.Writer) (pendingWrite, bool) {
	target, mode := resolveForWrite(shared)
	if a, err := os.Stat(target); err == nil {
		if b, err := os.Stat(localReal); err == nil && os.SameFile(a, b) {
			return pendingWrite{}, false
		}
	}
	existing, err := os.ReadFile(target)
	if os.IsNotExist(err) {
		return pendingWrite{}, false
	}
	if err != nil {
		fmt.Fprintf(stderr, "警告：读不了 %s，没有检查里面有没有旧版写的 Railse 钩子：%v\n", shared, err)
		return pendingWrite{}, false
	}
	ins, err := hooksconf.Inspect(existing, bin)
	if err == nil && len(ins.Events) == 0 {
		return pendingWrite{}, false
	}
	var data []byte
	if err == nil {
		data, err = hooksconf.Merge(existing, bin, nil)
	}
	if err != nil {
		fmt.Fprintf(stderr, "警告：%s：%v，跳过迁移；里面若有旧版写的 Railse 钩子请手动删掉，否则会和 settings.local.json 里的一起触发两次\n", shared, err)
		return pendingWrite{}, false
	}
	return pendingWrite{show: shared, path: target, data: data, mode: mode, migrated: true}, true
}

// writeFileAtomic 先写同目录的临时文件再 rename，中途失败不会留下写了一半的配置。
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".railse-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}

func runSetup(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "all" {
		return runSetupAll(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "autostart" {
		return runAutostart(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "hooks" {
		fmt.Fprintln(stderr, setupUsage)
		return 2
	}
	fs := flag.NewFlagSet("setup hooks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", ".", "项目目录")
	doClaude := fs.Bool("claude", false, "只装 Claude Code")
	doCodex := fs.Bool("codex", false, "只装 Codex")
	doGemini := fs.Bool("gemini", false, "只装 Gemini")
	codexFile := fs.String("codex-file", "", "Codex 钩子文件，默认 ~/.codex/hooks.json")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	dryRun := fs.Bool("dry-run", false, "只打印不写")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "多余的参数 %q\n%s\n", fs.Args(), setupUsage)
		return 2
	}
	if !*doClaude && !*doCodex && !*doGemini {
		*doClaude, *doCodex, *doGemini = true, true, true
	}
	if *bin == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*bin, _ = filepath.Abs(exe)
	}
	absProject, err := filepath.Abs(*project)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *codexFile == "" && *doCodex {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*codexFile = hooksconf.CodexPath(home)
	}
	claudeLocal, claudeShared := hooksconf.ClaudePaths(absProject)
	targets := []struct {
		cli  string
		on   bool
		path string
	}{
		{"claude", *doClaude, claudeLocal},
		{"codex", *doCodex, *codexFile},
		{"gemini", *doGemini, hooksconf.GeminiPath(absProject)},
	}
	// 先把所有目标（连同 settings.json 的迁移）都算好，任何一个出错就一个都不写
	var writes []pendingWrite
	for _, tg := range targets {
		if !tg.on {
			continue
		}
		path, mode := resolveForWrite(tg.path)
		existing, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(stderr, err)
			return 1
		}
		merged, err := hooksconf.Merge(existing, *bin, hooksconf.Plan(tg.cli, *bin))
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", tg.path, err)
			return 1
		}
		writes = append(writes, pendingWrite{show: tg.path, path: path, data: merged, mode: mode})
		if tg.cli == "claude" {
			// 紧跟在 settings.local.json 之后：先装好新的再删旧的，中途失败也不会两处都没有
			if w, ok := claudeMigration(claudeShared, path, *bin, stderr); ok {
				writes = append(writes, w)
			}
		}
	}
	for _, w := range writes {
		if *dryRun {
			if w.migrated {
				fmt.Fprintf(stdout, "== %s（迁走 Railse 钩子后）==\n%s", w.show, w.data)
			} else {
				fmt.Fprintf(stdout, "== %s ==\n%s", w.show, w.data)
			}
			continue
		}
		if err := writeFileAtomic(w.path, w.data, w.mode); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if w.migrated {
			fmt.Fprintf(stdout, "已从 %s 迁走 Railse 钩子（现在写在 settings.local.json，不入库）\n", w.show)
		} else {
			fmt.Fprintf(stdout, "已写入 %s\n", w.show)
		}
	}
	if *doCodex {
		// notify 是 argv 数组，bin 必须单独作为第一个元素；%q 的转义对普通路径与 TOML 基本字符串一致
		fmt.Fprintf(stdout, "提醒 Codex：钩子只在交互会话里审批（codex exec 不触发）；首次使用请在 Codex 里用 /hooks 审核；notify 需手动写在用户级 ~/.codex/config.toml：notify = [%q, \"hook\", \"codex-notify\"]。\n", *bin)
	}
	if *doGemini {
		fmt.Fprintln(stdout, "提醒 Gemini：钩子只能拒绝不能放行，需要 Gemini CLI 0.65.0 以上，不要开 yolo 模式。")
	}
	return 0
}

// setupDaemonWait 是 setup all 托管之后等守护进程应答的上限：launchd、systemd 拉起它要一点时间，等到了体检才看得到它在跑。
// 是变量只为测试能调短。
var setupDaemonWait = 5 * time.Second

// daemonAnswers 报告 sock 上有没有守护进程应答 status。
func daemonAnswers(sock string) bool {
	_, err := hookclient.Client{SocketPath: sock, ConnectTimeout: time.Second}.Control("status")
	return err == nil
}

// agentJobPath 是本系统上守护进程托管文件的路径（与 setup autostart、doctor 共用 internal/autostart 的约定）；
// 不支持开机自启的系统、取不到家目录时为空。
func agentJobPath(home string) string {
	if home == "" {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return autostart.PlistPath(home, "agent")
	case "linux":
		return autostart.UnitPath(home, "agent", false)
	}
	return ""
}

// runSetupAll 是 railse setup all：依次接入 hub、装三个 CLI 的钩子、托管开机自启、体检。
// 任一步失败立即退出 1 并说明停在哪一步、哪些已做完；体检只展示（刚托管的守护进程可能还没起来），不影响退出码。
func runSetupAll(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup all", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "用法:\n%s\n", setupAllUsage)
		fs.PrintDefaults()
	}
	hubURL := fs.String("hub", "", "hub 地址，如 https://hub.example.com")
	token := fs.String("token", "", "railse hub token 生成的接入码")
	name := fs.String("name", "", "本机在 hub 里的名字，默认主机名")
	project := fs.String("project", ".", "项目目录")
	dir := fs.String("dir", "", "守护进程数据目录，默认 $RAILSE_HOME 或 ~/.railse")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	noAutostart := fs.Bool("no-autostart", false, "不托管开机自启")
	noHooks := fs.Bool("no-hooks", false, "不装钩子")
	force := fs.Bool("force", false, "已接入过也重新接入，覆盖 machine.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, format+"\n用法:\n%s\n", append(a, setupAllUsage)...)
		return 2
	}
	if fs.NArg() != 0 {
		return usageErr("多余的参数 %q", fs.Args())
	}
	var hubBase string
	if *hubURL != "" {
		_, base, err := parseHubURL(*hubURL)
		if err != nil {
			return usageErr("%v", err)
		}
		hubBase = base
	}
	if *dir != "" {
		abs, err := filepath.Abs(*dir)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*dir = abs
	}
	cfg, err := agentConfigFor(*dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// 已有 machine.json（哪怕是坏的）就不接入，--hub 与 --token 只在要接入时才必须给
	_, lerr := os.Lstat(cfg.MachinePath)
	if lerr != nil && !errors.Is(lerr, os.ErrNotExist) {
		fmt.Fprintln(stderr, lerr)
		return 1
	}
	enroll := *force || lerr != nil
	if enroll && (*hubURL == "" || *token == "") {
		return usageErr("需要 --hub 与 --token（接入码由 hub 上的 railse hub token 生成）")
	}
	// 一看就不对的输入在动手之前挡住，免得做了一半才发现
	b := *bin
	if b == "" {
		if b, err = os.Executable(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if b, err = filepath.Abs(b); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !*noHooks || !*noAutostart {
		if err := checkExecutable(b); err != nil { // 钩子与托管文件都指向它
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	proj, err := filepath.Abs(*project)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if st, err := os.Stat(proj); err != nil || !st.IsDir() {
		fmt.Fprintf(stderr, "项目目录 %s 不存在或不是目录：用 --project 指定要装钩子的项目\n", proj)
		return 1
	}

	steps := []string{"接入 hub", "安装钩子", "开机自启", "体检"}
	var done []string
	step := func(n int) { fmt.Fprintf(stdout, "\n== %d. %s ==\n", n, steps[n-1]) }
	fail := func(n int, extra string) int {
		fmt.Fprintf(stderr, "railse setup all 停在第 %d 步（%s）", n, steps[n-1])
		if len(done) > 0 {
			fmt.Fprintf(stderr, "；已完成：%s", strings.Join(done, "、"))
		}
		if n < len(steps) {
			fmt.Fprintf(stderr, "；没做：%s", strings.Join(steps[n:], "、"))
		}
		fmt.Fprintf(stderr, "。%s修好后重跑 railse setup all（已接入的会跳过接入）\n", extra)
		return 1
	}
	var notes []string
	phoneURL := hubBase // 下一步里让手机打开的地址：守护进程实际接的 hub

	step(1)
	if !enroll {
		m, err := agent.LoadMachine(cfg.MachinePath)
		if err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return fail(1, "machine.json 坏了：加 --force（连同 --hub、--token）重新接入。")
		}
		fmt.Fprintf(stdout, "已接入过（%s），跳过；要换 hub 或重新接入加 --force\n", cfg.MachinePath)
		phoneURL = m.HubURL
		if hubBase != "" && hubBase != m.HubURL {
			fmt.Fprintf(stdout, "注意：本机接的是 %s，不是 --hub 给的 %s；要改接那个 hub，加 --force 重新接入\n", m.HubURL, hubBase)
		}
	} else if err := agentEnroll(cfg, *hubURL, *token, *name, *force, stdout, stderr); err != nil {
		fmt.Fprintf(stderr, "接入失败：%v\n", err)
		return fail(1, "")
	}
	done = append(done, steps[0])

	step(2)
	if *noHooks {
		fmt.Fprintln(stdout, "已跳过（--no-hooks）")
	} else {
		if code := runSetup([]string{"hooks", "--project", proj, "--bin", b}, stdout, stderr); code != 0 {
			return fail(2, "钩子文件的问题见上面（已写入的会列出来）。")
		}
		done = append(done, steps[1])
	}

	// 给人照抄的命令：用户给了 --dir、--bin 的，原样带上
	dirBin := ""
	if *dir != "" {
		dirBin += " --dir " + hooksconf.ShellQuote(cfg.Dir)
	}
	if *bin != "" {
		dirBin += " --bin " + hooksconf.ShellQuote(b)
	}
	step(3)
	slow := false // 托管了、守护进程却还没应答
	switch job := agentJobPath(homeDir()); {
	case *noAutostart:
		fmt.Fprintln(stdout, "已跳过（--no-autostart）；守护进程要自己 railse agent run")
	case daemonAnswers(cfg.SocketPath) && !jobExists(job):
		// 手动起的守护进程占着 socket：再托管一个，新实例起不来、被 launchd/systemd 反复拉起；在跑的这个也只在启动时读 machine.json
		fmt.Fprintf(stdout, "守护进程已在运行，但没有托管文件 %s（应是手动 railse agent run 起的），跳过托管：再托管一个会抢不到 socket、被反复拉起；"+
			"在跑的这个也只在启动时读 machine.json。\n先停掉它（在它的终端里 Ctrl-C），再运行：railse setup autostart%s\n", job, dirBin)
		notes = append(notes, "没有托管开机自启（守护进程在手动运行），见第 3 步")
	default:
		if code := runAutostart([]string{"--dir", cfg.Dir, "--bin", b}, stdout, stderr); code != 0 {
			return fail(3, "可以先手动 railse agent run 跑起守护进程。")
		}
		done = append(done, steps[2])
		if waitDaemon(cfg.SocketPath, setupDaemonWait) {
			fmt.Fprintln(stdout, "守护进程已应答")
		} else {
			slow = true
			fmt.Fprintf(stdout, "守护进程等了 %s 还没有应答，下面的体检可能显示它没在跑\n", setupDaemonWait)
		}
	}

	step(4)
	if code := runDoctor([]string{"--project", proj, "--dir", cfg.Dir, "--bin", b}, stdout, stderr); code != 0 {
		recheck := "railse doctor --project " + hooksconf.ShellQuote(proj) + dirBin
		if slow {
			fmt.Fprintf(stdout, "体检有失败项；setup all 不因体检失败而失败：刚托管的守护进程可能还没起来，过几秒再跑 %s 复查。\n", recheck)
		} else {
			fmt.Fprintf(stdout, "体检有失败项；setup all 不因体检失败而失败：按上面的“修复：”处理后再跑 %s 复查。\n", recheck)
		}
	}
	for _, n := range notes {
		fmt.Fprintf(stdout, "\n注意：%s。", n)
	}
	fmt.Fprintf(stdout, "\n下一步：手机上打开 %s 登录，加到主屏幕后在设置里开启通知（见 docs/web.md）。\n", phoneURL)
	return 0
}

// homeDir 是家目录；取不到时为空（托管那一步会自己报错）。
func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// jobExists 报告托管文件在不在（路径为空即不支持开机自启，算不在）。
func jobExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Lstat(path)
	return err == nil
}

// waitDaemon 每 200 毫秒问一次守护进程，至多等 wait；wait 为零时只问一次。
func waitDaemon(sock string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		if daemonAnswers(sock) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}
