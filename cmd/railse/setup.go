package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
	"github.com/xufei547/railse-opc/internal/hooksconf"
)

const setupAllUsage = `  railse setup all --hub URL --token 接入码 [--name 名字] [--project 目录] [--dir 目录] [--bin 路径] [--no-autostart] [--no-hooks] [--force]
    一键：接入 hub → 装三个 CLI 的钩子 → 开机自启 → 体检（railse doctor）。钩子默认装用户级（对所有项目生效），
    给了 --project 才把 Claude、Gemini 的装成那个项目的项目级（同 setup hooks）；装完钩子接着接 Codex 的 notify
    （同 setup codex-notify，没接上不算 setup all 失败，最后的“注意”里给命令）。--no-hooks 两样都不做。
    已接入过（有 machine.json）时跳过接入，这时可以不给 --hub 与 --token；--force 重新接入。体检的失败只展示，不算 setup all 失败。
    已接入过的在终端里重跑，重装钩子、重启守护进程之前先问一次（回车继续）；不在终端里不问。
    要接入却缺 --hub 或 --token 时，在终端里逐个问缺的（--name 没给时也问名字）；不在终端里是用法错误。`

const setupUsage = `用法:
  railse setup
    不带子命令、在终端里运行：逐个问 hub 地址、接入码、本机名字，再照 setup all 做完（已接入过的不问这些，只确认一次再重装）。
` + setupAllUsage + `
  railse setup hooks [--project 目录] [--claude] [--codex] [--gemini] [--codex-file 路径] [--dir 目录] [--bin 路径] [--dry-run]
    默认三个都装，都是用户级（对所有项目生效）：Claude 写 ~/.claude/settings.json，Gemini 写 ~/.gemini/settings.json
    （第一次改这两个文件之前各备份一次，<文件>.railse-bak-<时间>），Codex 写 ~/.codex/hooks.json。
    --project 目录：Claude、Gemini 改写那个项目的项目级 .claude/settings.local.json（不入库；旧版写在 settings.json 的会迁走）
    与 .gemini/settings.json，Codex 照旧是用户级。
    --dir 是守护进程的数据目录（默认 $RAILSE_HOME 或 ~/.railse）：不是 ~/.railse 时钩子命令带 --socket 指向它。
  railse setup autostart [--hub] [--uninstall] [--status] [--dir 目录] [--bin 路径] [--system] [--user 名字] [--dry-run] [-- hub run 的额外参数]
    开机自启并在退出后自动拉起（macOS launchd、Linux systemd），默认托管守护进程，--hub 托管 hub；详见 railse setup autostart -h。
` + setupCodexNotifyUsage

// pendingWrite 是算好、待写的一个配置文件。
type pendingWrite struct {
	show, path string // show 是给人看的路径，path 是解开符号链接后真正写的文件
	data       []byte
	mode       os.FileMode
	migrated   bool   // 只删不加：把 Railse 钩子从 Claude 的 settings.json 迁走
	user       bool   // 用户级配置（对所有项目生效）
	backup     []byte // 非空时写之前先把它（解开链接后的现有内容）备份到 backupOf 旁边，见 needBackup
	backupOf   string // 要备份的用户级配置的显示路径（~/.claude/settings.json 或 ~/.gemini/settings.json）
}

// backupSuffix 是用户级配置备份文件名里的后缀：<文件>.railse-bak-<时间>。
const backupSuffix = ".railse-bak-"

// backupNow 是备份文件名里的时间；是变量只为测试能固定它。
var backupNow = time.Now

// backupPath 是 show 在 now 时刻的备份文件：放在 show（给人看的路径）旁边，不放进链接指向的目录。时间用本地时间。
func backupPath(show string, now time.Time) string {
	return show + backupSuffix + now.Format("20060102-150405")
}

// userSettingsOf 在 users（用户级 Claude、Gemini 配置的显示路径）里找 path（解开链接后要写的文件）是哪一个：
// 是同一个文件（字面相同或 os.SameFile）时返回它，都不是返回空。
func userSettingsOf(path string, users []string) string {
	for _, u := range users {
		if samePath(path, u) {
			return u
		}
	}
	return ""
}

// needBackup 报告写用户级配置 show 之前要不要先备份：现有内容非空、这次要写的与它不同，且 show 旁边还没有任何
// <文件名>.railse-bak-*（只在第一次改它之前备份一次）。
func needBackup(show string, existing, data []byte) (bool, error) {
	if len(existing) == 0 || bytes.Equal(existing, data) {
		return false, nil
	}
	ents, err := os.ReadDir(filepath.Dir(show))
	if err != nil {
		return false, err
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), filepath.Base(show)+backupSuffix) {
			return false, nil
		}
	}
	return true, nil
}

// maxBackupTries 是同一秒里同名备份已存在时最多试几个名字（<名字>、<名字>-2 … <名字>-100）。
const maxBackupTries = 100

// writeBackup 把 data 写到 show 旁边新建的备份文件（0600，O_EXCL：不覆盖已有文件），返回备份文件的路径。
// backupPath 已被占（同一秒里改了第二次）时依次试 -2、-3…，试满 maxBackupTries 个都被占才报错。
// 中途失败时删掉写了一半的备份。
func writeBackup(show string, data []byte) (string, error) {
	base := backupPath(show, backupNow())
	var bak string
	var f *os.File
	var err error
	for i := 1; i <= maxBackupTries; i++ {
		bak = base
		if i > 1 {
			bak = fmt.Sprintf("%s-%d", base, i)
		}
		if f, err = os.OpenFile(bak, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(bak)
		return "", err
	}
	return bak, nil
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
// 它（解开链接后）就是用户级的 ~/.claude/settings.json 时（在家目录里运行，或项目的 settings.json 链到它）也不迁：
// 那里的钩子对所有项目生效，迁走会让别的项目失去钩子；有 Railse 条目时警告，请用户自己决定。
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
	if home, herr := os.UserHomeDir(); herr == nil {
		if user := hooksconf.ClaudeUserPath(home); samePath(target, user) {
			fmt.Fprintf(stderr, "警告：没有迁走 %s 里的 Railse 钩子：它就是 %s，这是用户级配置，迁走会让所有项目失去钩子；如确要迁走请手动删"+
				"（不删的话，在这个目录里它会和 settings.local.json 里的一起触发两次）\n", shared, user)
			return pendingWrite{}, false
		}
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
	if len(args) == 0 && stdinIsTerminal() {
		return runSetupAll(nil, stdout, stderr) // 不带子命令、在终端里：同 setup all 不带标志，缺的接入信息逐个问
	}
	if len(args) > 0 && args[0] == "all" {
		return runSetupAll(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "autostart" {
		return runAutostart(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "codex-notify" {
		return runSetupCodexNotify(args[1:], stdout, stderr)
	}
	if len(args) == 0 || args[0] != "hooks" {
		fmt.Fprintln(stderr, setupUsage)
		return 2
	}
	return runSetupHooks(args[1:], true, stdout, stderr)
}

// runSetupHooks 是 railse setup hooks（args 是 hooks 之后的参数）。装了 Codex 时末尾的提醒里，notifyHint 为真才叫用户
// 去跑 setup codex-notify；setup all 装完钩子自己接 notify，传假。
func runSetupHooks(args []string, notifyHint bool, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup hooks", flag.ContinueOnError)
	fs.SetOutput(stderr)
	project := fs.String("project", "", "项目目录：给了才把 Claude、Gemini 的钩子写成这个项目的项目级，默认写用户级")
	doClaude := fs.Bool("claude", false, "只装 Claude Code")
	doCodex := fs.Bool("codex", false, "只装 Codex")
	doGemini := fs.Bool("gemini", false, "只装 Gemini")
	codexFile := fs.String("codex-file", "", "Codex 钩子文件，默认 ~/.codex/hooks.json")
	dir := fs.String("dir", "", "守护进程数据目录，默认 $RAILSE_HOME 或 ~/.railse；不是 ~/.railse 时钩子命令带 --socket")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	dryRun := fs.Bool("dry-run", false, "只打印不写")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "多余的参数 %q\n%s\n", fs.Args(), setupUsage)
		return 2
	}
	if !*doClaude && !*doCodex && !*doGemini {
		*doClaude, *doCodex, *doGemini = true, true, true
	}
	socket, base, err := hookSocket(*dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	binGiven := *bin != ""
	if *bin == "" {
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*bin, _ = filepath.Abs(exe)
	}
	home, homeErr := os.UserHomeDir()
	// 用户级的 Claude、Gemini 配置：改它们之前要备份（见 needBackup）；取不到家目录时没有
	var userSettings []string
	if homeErr == nil {
		userSettings = []string{hooksconf.ClaudeUserPath(home), hooksconf.GeminiUserPath(home)}
	}
	// 不给 --project：Claude、Gemini 写用户级（对所有项目生效），不做 settings.json 的迁移（它只针对项目级）
	userLevel := *project == ""
	var claudePath, claudeShared, geminiPath string
	switch {
	case !userLevel:
		absProject, err := filepath.Abs(*project)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		claudePath, claudeShared = hooksconf.ClaudePaths(absProject)
		geminiPath = hooksconf.GeminiPath(absProject)
	case *doClaude || *doGemini:
		if homeErr != nil {
			fmt.Fprintf(stderr, "取不到家目录（%v），装不了用户级的 Claude、Gemini 钩子：用 --project 目录 装成那个项目的项目级\n", homeErr)
			return 1
		}
		claudePath, geminiPath = userSettings[0], userSettings[1]
	}
	codexUser := *codexFile == "" // 默认的 ~/.codex/hooks.json 是用户级
	if *codexFile == "" && *doCodex {
		if homeErr != nil {
			fmt.Fprintln(stderr, homeErr)
			return 1
		}
		*codexFile = hooksconf.CodexPath(home)
	}
	targets := []struct {
		cli  string
		on   bool
		path string
		user bool
	}{
		{"claude", *doClaude, claudePath, userLevel},
		{"codex", *doCodex, *codexFile, codexUser},
		{"gemini", *doGemini, geminiPath, userLevel},
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
		merged, err := hooksconf.Merge(existing, *bin, hooksconf.Plan(tg.cli, *bin, socket))
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", tg.path, err)
			return 1
		}
		w := pendingWrite{show: tg.path, path: path, data: merged, mode: mode, user: tg.user}
		// 用户级的 Claude、Gemini 配置里多是用户自己的设置：第一次改它之前备份一次，备份放在用户级配置旁边。按文件身份认：
		// 给了 --project 而项目里的文件就是它（--project ~ 时的 Gemini、链到它的项目文件）也算。Codex 的 hooks.json 只放钩子，不备份
		if u := userSettingsOf(path, userSettings); u != "" {
			bak, err := needBackup(u, existing, merged)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			if bak {
				w.backup, w.backupOf = existing, u
			}
		}
		writes = append(writes, w)
		if tg.cli == "claude" && !userLevel {
			// 紧跟在 settings.local.json 之后：先装好新的再删旧的，中途失败也不会两处都没有
			if mw, ok := claudeMigration(claudeShared, path, *bin, stderr); ok {
				writes = append(writes, mw)
			}
		}
	}
	for _, w := range writes {
		if *dryRun {
			switch {
			case w.migrated:
				fmt.Fprintf(stdout, "== %s（迁走 Railse 钩子后）==\n%s", w.show, w.data)
			case w.backup != nil:
				fmt.Fprintf(stdout, "== %s（会先备份到 %s）==\n%s", w.show, backupPath(w.backupOf, backupNow()), w.data)
			default:
				fmt.Fprintf(stdout, "== %s ==\n%s", w.show, w.data)
			}
			continue
		}
		if w.backup != nil {
			bak, err := writeBackup(w.backupOf, w.backup)
			if err != nil {
				fmt.Fprintf(stderr, "备份 %s 失败，没有改它：%v\n", w.backupOf, err)
				return 1
			}
			fmt.Fprintf(stdout, "已备份 %s 到 %s\n", w.backupOf, bak)
		}
		if err := writeFileAtomic(w.path, w.data, w.mode); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		switch {
		case w.migrated:
			fmt.Fprintf(stdout, "已从 %s 迁走 Railse 钩子（现在写在 settings.local.json，不入库）\n", w.show)
		case w.user:
			fmt.Fprintf(stdout, "已写入 %s（用户级，对所有项目生效）\n", w.show)
		default:
			fmt.Fprintf(stdout, "已写入 %s\n", w.show)
		}
	}
	if userLevel && len(userSettings) == 2 {
		if cwd, err := os.Getwd(); err == nil {
			userData := map[string][]byte{} // 用户级文件这次写（--dry-run 时会写）的内容，按显示路径
			for _, w := range writes {
				userData[w.show] = w.data
			}
			remindProjectLevel(cwd, *bin, *doClaude, *doGemini, userData, userSettings, stderr)
		}
	}
	if socket != "" && len(writes) > 0 {
		if base == "" {
			base = "~/.railse（取不到家目录）"
		}
		fmt.Fprintf(stdout, "钩子命令带 --socket %s：守护进程数据目录 %s 不是 %s，CLI 起钩子时的环境里不一定有指向它的 RAILSE_HOME\n", hooksconf.ShellQuote(socket), filepath.Dir(socket), base)
	}
	switch {
	case *doCodex && notifyHint:
		// notify 另由 setup codex-notify 写（串接脚本，保留已有的 notify）；这里给的 --dir、--bin 原样带上，脚本与钩子连同一个守护进程
		given := ""
		if binGiven {
			given = *bin
		}
		fmt.Fprintf(stdout, "提醒 Codex：钩子只在交互会话里审批（codex exec 不触发）；首次使用请在 Codex 里用 /hooks 审核；notify（完成与提问事件）请运行 %s（会保留已有的 notify）。\n", codexNotifyCmd(socket, given))
	case *doCodex:
		fmt.Fprintln(stdout, "提醒 Codex：钩子只在交互会话里审批（codex exec 不触发）；首次使用请在 Codex 里用 /hooks 审核。")
	}
	if *doGemini {
		fmt.Fprintln(stdout, "提醒 Gemini：钩子只能拒绝不能放行，需要 Gemini CLI 0.65.0 以上，不要开 yolo 模式。")
	}
	return 0
}

// remindProjectLevel 在装用户级时提醒当前目录 dir 里项目级的 Railse 钩子（如从 0.1.0 升级、当初用 --project 装过的项目）：
// 用户级已对所有项目生效，项目级的还留着时 Gemini 会触发两次，Claude 的条目与用户级的不同时也会两次（与 doctor 同一口径：
// hooksconf.Inspect 认条目、hooksconf.SameOurs 比两边；Claude 两边完全相同时只跑一次）。只看这次装的 CLI，每个文件一行写到
// stderr；与用户级是同一个文件的（在家目录里跑、链到用户级的）、已提醒过的同一个文件不算；读不了、不是合法 JSON 的不提。
// userData 是用户级文件这次写的内容（按显示路径）。只读，不改项目里的文件。
func remindProjectLevel(dir, bin string, doClaude, doGemini bool, userData map[string][]byte, userSettings []string, stderr io.Writer) {
	type project struct{ cli, path, user string }
	var files []project
	if doClaude {
		local, shared := hooksconf.ClaudePaths(dir)
		files = append(files, project{"claude", local, userSettings[0]}, project{"claude", shared, userSettings[0]})
	}
	if doGemini {
		files = append(files, project{"gemini", hooksconf.GeminiPath(dir), userSettings[1]})
	}
	seen := append([]string(nil), userSettings...)
	for _, f := range files {
		if slices.ContainsFunc(seen, func(p string) bool { return samePath(f.path, p) }) {
			continue
		}
		seen = append(seen, f.path)
		b, err := os.ReadFile(f.path)
		if err != nil {
			continue
		}
		if ins, err := hooksconf.Inspect(b, bin); err != nil || len(ins.Events) == 0 {
			continue
		}
		const why = "（用户级已对所有项目生效；在项目里跑 railse doctor 也会指出）"
		switch {
		case f.cli == "claude" && hooksconf.SameOurs(userData[f.user], b, bin):
			fmt.Fprintf(stderr, "提醒：%s 里也有 Railse 的钩子，与用户级的条目完全相同，Claude Code 只跑一次：其中 Railse 的条目可删可不删%s\n", f.path, why)
		case f.cli == "claude":
			fmt.Fprintf(stderr, "提醒：%s 里也有 Railse 的钩子，与用户级的条目不同，Claude Code 会触发两次：建议删掉其中 Railse 的条目%s\n", f.path, why)
		default:
			fmt.Fprintf(stderr, "提醒：%s 里也有 Railse 的钩子，Gemini 会触发两次：建议删掉其中 Railse 的条目%s\n", f.path, why)
		}
	}
}

// hookSocket 定钩子命令里的 --socket。dir 是守护进程的数据目录（--dir；空则同 agent 子命令，取 $RAILSE_HOME 或 ~/.railse）。
// 不带 --socket 的钩子连哪里，看的是 CLI 起钩子时的环境，那里不一定有 RAILSE_HOME（托管的守护进程带的、只在跑 setup 的
// 那条命令上设的都进不去），所以基准取与环境无关的 ~/.railse：dir 不是它时返回 dir 里 socket 的绝对路径，是它时返回空，
// 命令保持原样（已有用户的配置不变）。base 是 ~/.railse 的绝对路径（给提示用；取不到家目录时为空，这时总带 --socket）。
// 既没有 $RAILSE_HOME 又取不到家目录、也没给 --dir 时没有目录可指，不带 --socket。
func hookSocket(dir string) (socket, base string, err error) {
	cfg, err := agentConfigFor(dir)
	if err != nil {
		return "", "", nil
	}
	abs, err := filepath.Abs(cfg.Dir)
	if err != nil {
		return "", "", err
	}
	if home, herr := os.UserHomeDir(); herr == nil {
		if base, err = filepath.Abs(filepath.Join(home, ".railse")); err != nil {
			return "", "", err
		}
		if samePath(abs, base) {
			return "", base, nil
		}
	}
	return agent.ConfigIn(abs).SocketPath, base, nil
}

// samePath 报告两个绝对路径是不是同一个文件或目录：字面相同（清理后），或都存在且是同一个文件（中间有符号链接）。
func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

// setupDaemonWait 是 setup all 托管之后探守护进程应答的时长：launchd、systemd 拉起它要一点时间，等到了体检才看得到它在跑。
// 截止时间只在两次探测之间检查，单次探测最长约 5 秒（hookclient 管理命令的时限），所以最坏约等 setupDaemonWait 加 5 秒。
// 是变量只为测试能调短。
var setupDaemonWait = 5 * time.Second

// daemonAnswers 报告 sock 上有没有守护进程应答 status。只用在托管之后的等待里；判断 socket 被没被占用 socketOccupied。
func daemonAnswers(sock string) bool {
	_, err := hookclient.Client{SocketPath: sock, ConnectTimeout: time.Second}.Control("status")
	return err == nil
}

// socketOccupied 用与守护进程 Listen 相同的判定看 socket 被没被占：连得上就是有进程在监听，不管它答不答、说哪个协议版本
// （这时再起一个守护进程，它 Listen 会失败）。无人监听的陈旧 socket 文件不算，守护进程 Listen 时会替换它。
func socketOccupied(sock string) bool {
	c, err := net.DialTimeout("unix", sock, 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// agentJobRunning 问服务管理器守护进程的托管作业在不在跑（与 railse setup autostart --status 同一个查询：launchctl print 的
// state、systemctl --user is-active），不看托管文件在不在：托管失败时文件可能已经写下了，作业却没起来。只读。
func agentJobRunning() bool {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return autostartStatus(runtime.GOOS, autostartSpec{Kind: "agent", Home: home, UID: os.Getuid()}, false, io.Discard) == 0
}

// runSetupAll 是 railse setup all：依次接入 hub、装三个 CLI 的钩子（连同 Codex 的 notify）、托管开机自启、体检。要接入却缺
// --hub 或 --token 时，stdin 是终端就先逐个问（askSetupAll），再走同样的四步；已接入过的在终端里重跑，第 2 步之前确认一次（confirmRerun）。
// 任一步失败立即退出 1 并说明停在哪一步、哪些已做完；体检只展示（刚托管的守护进程可能还没起来），不影响退出码。
func runSetupAll(args []string, stdout, stderr io.Writer) int {
	return runSetupAllAs(runtime.GOOS, os.Geteuid(), args, stdout, stderr)
}

// runSetupAllAs 是 runSetupAll 的可测版本：系统（只用于拒绝 root 时的提示）与有效 uid 由调用方给。
func runSetupAllAs(goos string, euid int, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup all", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "用法:\n%s\n", setupAllUsage)
		fs.PrintDefaults()
	}
	hubURL := fs.String("hub", "", "hub 地址，如 https://hub.example.com")
	token := fs.String("token", "", "railse hub token 生成的接入码")
	name := fs.String("name", "", "本机在 hub 里的名字，默认主机名")
	project := fs.String("project", "", "项目目录：给了才把 Claude、Gemini 的钩子装成这个项目的项目级，默认装用户级")
	dir := fs.String("dir", "", "守护进程数据目录，默认 $RAILSE_HOME 或 ~/.railse")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	noAutostart := fs.Bool("no-autostart", false, "不托管开机自启")
	noHooks := fs.Bool("no-hooks", false, "不装钩子，也不接 Codex 的 notify")
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
	// 在动手之前拒绝 root：否则一次性接入码先被用掉，machine.json 与项目里、家目录下的钩子配置都成了 root 的文件，
	// 到第 3 步才被 setup autostart 拒绝（加 --no-autostart 时更是以 root 一路跑完）
	if euid == 0 {
		// hub 的提示按系统分：只有 Linux 的系统单元要 sudo；macOS 上 hub 也用平时的用户托管（launchd 用户代理）
		hubHint := "hub 机器（Linux）用 " + sudoSystemHubCmd + " 装成系统服务"
		if goos == "darwin" {
			hubHint = "macOS 上 hub 也不用 sudo：railse setup autostart --hub"
		}
		fmt.Fprintln(stderr, "setup all 要用平时的用户身份运行，不要 sudo：以 root 跑会用掉一次性接入码，machine.json 与钩子配置也会成为 root 的文件；"+hubHint)
		return 1
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
	// 要接入却缺 --hub 或 --token：不是终端就是用法错误（与原来一样排在下面的检查之前）；是终端的到下面再问
	missing := enroll && (*hubURL == "" || *token == "")
	if missing && !stdinIsTerminal() {
		return usageErr("需要 --hub 与 --token（接入码由 hub 上的 railse hub token 生成）")
	}
	// 一看就不对的输入在动手之前（也在下面提问之前）挡住，免得做了一半、答完才发现
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
	var proj string // 空：钩子装用户级
	if *project != "" {
		if proj, err = filepath.Abs(*project); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if st, err := os.Stat(proj); err != nil || !st.IsDir() {
			fmt.Fprintf(stderr, "项目目录 %s 不存在或不是目录：--project 给要装项目级钩子的项目，不给则钩子装用户级\n", proj)
			return 1
		}
	}
	if enroll && *name != "" { // 只在要接入时用得上（已接入的跳过接入，不看 --name）
		if err := checkNameFlag(*name); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	// 在终端里逐个问缺的（--name 没给时也问名字），答完照下面同样的四步做
	if missing {
		base, err := askSetupAll(setupStdin, stdout, hubURL, token, name, cfg.MachineID)
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Fprintln(stderr, "已取消，什么都没做")
			} else {
				fmt.Fprintf(stderr, "读输入失败，什么都没做：%v\n", err)
			}
			return 1
		}
		if base != "" { // 问了 hub 地址：已按 --hub 的口径校验、规范化
			hubBase = base
		}
	}
	// 给人照抄的命令：用户给了 --project、--dir、--bin 的，原样带上（agent run 只认 --dir）
	projArg, dirArg := "", ""
	if proj != "" {
		projArg = " --project " + hooksconf.ShellQuote(proj)
	}
	if *dir != "" {
		dirArg = " --dir " + hooksconf.ShellQuote(cfg.Dir)
	}
	dirBin := dirArg
	if *bin != "" {
		dirBin += " --bin " + hooksconf.ShellQuote(b)
	}
	// 重新接入写成完整的 setup all 命令：从不带子命令的 railse setup 进来的，加 --force 是用法错误
	reenroll := "railse setup all --force" + projArg + dirBin

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
			return fail(1, "machine.json 坏了：用 "+reenroll+" 重新接入（不在终端里时连同 --hub、--token）。")
		}
		fmt.Fprintf(stdout, "已接入过（%s），跳过；要换 hub 或重新接入：%s\n", cfg.MachinePath, reenroll)
		phoneURL = m.HubURL
		if hubBase != "" && hubBase != m.HubURL {
			fmt.Fprintf(stdout, "注意：本机接的是 %s，不是 --hub 给的 %s；要改接那个 hub，加 --force 重新接入\n", m.HubURL, hubBase)
		}
		// 已接入的机器在终端里重跑：重装钩子、重启守护进程之前先问一次。首次接入、--force 不走这里；不是终端（脚本、管道）时不问
		if stdinIsTerminal() {
			// 第 3 步对守护进程要做的事（与下面第 3 步同一判定）：托管的作业在跑（哪怕它用的是别的数据目录、还没开始监听），重装会
			// 把它停掉再拉起；没在跑、socket 也没人监听就托管并启动；手动起的占着 socket 时第 3 步跳过托管，不碰它
			daemon := ""
			if !*noAutostart {
				switch {
				case agentJobRunning():
					daemon = "重启守护进程"
				case !socketOccupied(cfg.SocketPath):
					daemon = "托管并启动守护进程"
				}
			}
			if what, untouched := rerunWork(!*noHooks, daemon); what != "" { // 没有要重做的不问
				ok, err := confirmRerun(setupStdin, stdout, m.HubURL, what)
				switch {
				case err != nil && !errors.Is(err, io.EOF):
					fmt.Fprintf(stderr, "读输入失败，%s：%v\n", untouched, err)
					return 1
				case !ok: // n、no 或 EOF（Ctrl-D）
					fmt.Fprintf(stderr, "已取消，%s\n", untouched)
					return 1
				}
			}
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
		// --dir 也交给钩子：目录不是 ~/.railse 时钩子命令带 --socket，才连得上下面托管的、带 RAILSE_HOME=<目录> 的守护进程。
		// --bin 只在用户给了时才传（setup hooks 默认就是当前程序）
		hookArgs := []string{"--dir", cfg.Dir}
		given := "" // 用户给的 --bin（取了绝对路径），没给为空
		if *bin != "" {
			given = b
			hookArgs = append(hookArgs, "--bin", b)
		}
		notifyArgs := slices.Clone(hookArgs)
		if proj != "" {
			hookArgs = append(hookArgs, "--project", proj)
		}
		if code := runSetupHooks(hookArgs, false, stdout, stderr); code != 0 {
			return fail(2, "钩子文件的问题见上面（已写入的会列出来）。")
		}
		done = append(done, steps[1])
		// Codex 只认一条 notify：钩子装完接着做 setup codex-notify 的事，--dir、--bin 与钩子同一口径，已有的 notify 由它保留为前一跳，
		// 重跑不重复串接。它失败（如 config.toml 看不懂）不算 setup all 失败：原因它已打印，最后的“注意”里给处理好后要跑的命令
		if code := runSetupCodexNotify(notifyArgs, stdout, stderr); code != 0 {
			socket, _, _ := hookSocket(cfg.Dir) // 上面装钩子时已算过一次，不会出错
			notes = append(notes, "Codex 的 notify 没有接上（原因见第 2 步），完成与提问事件进不了收件箱；处理好后运行 "+
				codexNotifyCmd(socket, given)+"（会保留已有的 notify）")
		}
	}

	step(3)
	slow := false // 托管了、守护进程却还没应答
	// socket 被占时问服务管理器作业在不在跑：在跑就是托管的那个占着，重装会换掉它；不在跑就是手动起的（或别的进程）占着，
	// 再托管一个，新实例 Listen 失败、被 launchd/systemd 反复拉起
	occupied := socketOccupied(cfg.SocketPath)
	switch {
	case *noAutostart:
		fmt.Fprintf(stdout, "已跳过（--no-autostart）；守护进程要自己 railse agent run%s\n", dirArg)
		if enroll && occupied { // 守护进程只在启动时读 machine.json
			if agentJobRunning() {
				fmt.Fprintf(stdout, "守护进程正在跑，读不到刚写的 machine.json：已托管的守护进程用 railse setup autostart%s 重装或重启后生效\n", dirBin)
			} else {
				fmt.Fprintf(stdout, "守护进程正在跑，读不到刚写的 machine.json：手动起的停掉后重新 railse agent run%s 才接上 hub\n", dirArg)
			}
		}
	case occupied && !agentJobRunning():
		fmt.Fprintf(stdout, "守护进程已在运行，但不是托管的那个（launchd/systemd 里的托管作业没在跑；应是手动 railse agent run 起的），跳过托管："+
			"再托管一个会抢不到 socket、被反复拉起；在跑的这个也只在启动时读 machine.json。\n先停掉它（在它的终端里 Ctrl-C），再运行：railse setup autostart%s\n", dirBin)
		notes = append(notes, "没有托管开机自启（守护进程在手动运行），见第 3 步")
	default:
		if code := runAutostart([]string{"--dir", cfg.Dir, "--bin", b}, stdout, stderr); code != 0 {
			return fail(3, "可以先手动 railse agent run"+dirArg+" 跑起守护进程。")
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
	// 给了 --project 才交给 doctor（不给时 doctor 看当前目录，它不是 git 仓库或在系统目录里时项目根只告知），复查命令同理
	doctorArgs, recheck := []string{"--dir", cfg.Dir, "--bin", b}, "railse doctor"+projArg+dirBin
	if proj != "" {
		doctorArgs = append([]string{"--project", proj}, doctorArgs...)
	}
	if code := runDoctor(doctorArgs, stdout, stderr); code != 0 {
		if slow {
			fmt.Fprintf(stdout, "体检有失败项；setup all 不因体检失败而失败：刚托管的守护进程可能还没起来，过几秒再跑 %s 复查。\n", recheck)
		} else {
			fmt.Fprintf(stdout, "体检有失败项；setup all 不因体检失败而失败：按上面的“修复：”处理后再跑 %s 复查。\n", recheck)
		}
	}
	for _, n := range notes {
		fmt.Fprintf(stdout, "\n注意：%s。", n)
	}
	printPhoneStep(stdout, phoneURL)
	return 0
}

// waitDaemon 每 200 毫秒问一次守护进程，直到它应答或过了 wait（见 setupDaemonWait 说的最坏时长）；wait 为零时只问一次。
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
