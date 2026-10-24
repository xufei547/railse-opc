package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/railse/railse-opc/internal/hooksconf"
)

const setupUsage = `用法:
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
