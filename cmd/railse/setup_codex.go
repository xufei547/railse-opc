package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xufei547/railse-opc/internal/hooksconf"
)

const setupCodexNotifyUsage = `  railse setup codex-notify [--dir 目录] [--bin 路径] [--dry-run]
    Codex 只认一条 notify：生成 <数据目录>/codex-notify.sh（数据目录默认 $RAILSE_HOME 或 ~/.railse），依次调用 ~/.codex/config.toml
    里原有的 notify 与 railse hook codex-notify，再把 config.toml 顶层的 notify 换成这个脚本（每次改之前备份到 config.toml.railse-bak-<时间>）。
    重跑不会重复串接；换了 --dir 或 --bin 重跑，原有的 notify 照样保留。数据目录不是 ~/.railse 时 railse 那一行带 --socket。`

// runSetupCodexNotify 是 railse setup codex-notify。先把脚本与改写后的 config.toml 都算好（config.toml 看不懂就什么都不写），
// 再先写脚本、后改 config.toml：config.toml 永远不会指向不存在的脚本。内容没变的文件不写。
func runSetupCodexNotify(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("setup codex-notify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "用法:\n%s\n", setupCodexNotifyUsage)
		fs.PrintDefaults()
	}
	dir := fs.String("dir", "", "守护进程数据目录（脚本放在这里），默认 $RAILSE_HOME 或 ~/.railse；不是 ~/.railse 时 railse 那一行带 --socket")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	dryRun := fs.Bool("dry-run", false, "只打印不写")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "多余的参数 %q\n用法:\n%s\n", fs.Args(), setupCodexNotifyUsage)
		return 2
	}
	cfg, err := agentConfigFor(*dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	dataDir, err := filepath.Abs(cfg.Dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	socket, _, err := hookSocket(*dir) // 与 setup hooks 同一判定：数据目录不是 ~/.railse 时带 --socket
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	b := *bin
	if b == "" {
		if b, err = os.Executable(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if b, err = filepath.Abs(b); err != nil { // Codex 起 notify 时的当前目录不定，脚本里写绝对路径
		fmt.Fprintln(stderr, err)
		return 1
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(stderr, "取不到家目录（%v），找不到 ~/.codex/config.toml\n", err)
		return 1
	}
	show := hooksconf.CodexConfigPath(home)
	path, mode := resolveForWrite(show) // 是符号链接时写到链接指向的文件、沿用它的权限
	existing, err := os.ReadFile(path)
	existed := err == nil
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if !existed {
		mode = 0o600
	}
	script := filepath.Join(dataDir, hooksconf.NotifyScriptName)
	n, err := hooksconf.ParseCodexNotify(existing)
	var data []byte
	if err == nil {
		data, err = hooksconf.SetCodexNotify(existing, []string{script})
	}
	if err != nil {
		fmt.Fprintf(stderr, "%s 看不懂（%v），什么都没改：请手动改好这一处（notify 要写成第一个 [表] 之前的字符串数组，"+
			"如 notify = [\"/path/to/cmd\", \"--flag\"]）再重跑 railse setup codex-notify\n", show, err)
		return 1
	}
	for _, t := range n.Tables {
		fmt.Fprintf(stderr, "警告：%s 里 %s 下面的 notify 是那个表的键，Codex 读不到，已忽略（没有串进脚本）；请手动删掉它\n", show, t)
	}
	prev := codexPrevNotify(n, script, show, stderr)
	body := hooksconf.NotifyScript(prev, b, socket)
	scriptSame := false
	if old, err := os.ReadFile(script); err == nil && bytes.Equal(old, body) {
		st, err := os.Stat(script)
		scriptSame = err == nil && st.Mode().Perm() == 0o755
	}
	configSame := existed && bytes.Equal(existing, data)
	backup := existed && !configSame // 每次真的改它之前都备份

	if *dryRun {
		scriptNote, configNote := "", ""
		if scriptSame {
			scriptNote = "（已是最新）"
		}
		switch {
		case configSame:
			configNote = "（已是最新）"
		case backup:
			configNote = "（会先备份到 " + backupPath(show, time.Now()) + "）"
		}
		fmt.Fprintf(stdout, "== %s%s ==\n%s== %s%s ==\n%s", script, scriptNote, body, show, configNote, data)
		return 0
	}
	if scriptSame {
		fmt.Fprintf(stdout, "%s 已是最新\n", script)
	} else {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := writeFileAtomic(script, body, 0o755); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "已写入 %s\n", script)
	}
	if configSame {
		fmt.Fprintf(stdout, "%s 已是最新\n", show)
	} else {
		if backup {
			bak, err := writeBackup(show, existing)
			if err != nil {
				fmt.Fprintf(stderr, "备份 %s 失败，没有改它：%v\n", show, err)
				return 1
			}
			fmt.Fprintf(stdout, "已备份 %s 到 %s\n", show, bak)
		}
		if err := writeFileAtomic(path, data, mode); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "已写入 %s：notify 指向 %s\n", show, script)
	}
	if len(prev) > 0 {
		quoted := make([]string, len(prev))
		for i, a := range prev {
			quoted[i] = hooksconf.ShellQuote(a)
		}
		fmt.Fprintf(stdout, "Codex 的 notify 先调原来的 %s，再调 railse\n", strings.Join(quoted, " "))
	} else {
		fmt.Fprintln(stdout, "Codex 的 notify 只调 railse（没有要保留的 notify）")
	}
	return 0
}

// codexPrevNotify 定串接脚本的前一跳：config.toml 顶层原有的 notify（n）。没有时、是直接调 railse 的旧写法
// [".../railse", "hook", "codex-notify", ...] 时没有前一跳（替换掉，不串两个 railse）；已经是我们的脚本（与 script 是同一个文件，
// 或文件名是 codex-notify.sh 且里面有标记行，如换 --dir 之前生成的）时取它标记行里记的前一跳，不把自己当前一跳，
// 读不到时没有前一跳并警告；别的整个数组原样作为前一跳。
func codexPrevNotify(n hooksconf.CodexNotify, script, show string, stderr io.Writer) []string {
	if !n.Found || len(n.Argv) == 0 {
		return nil
	}
	first := n.Argv[0]
	if same := samePath(first, script); same || filepath.Base(first) == hooksconf.NotifyScriptName {
		var prev []string
		marked := false
		b, err := os.ReadFile(first)
		if err == nil {
			prev, marked, err = hooksconf.ParseNotifyScript(b)
		}
		switch {
		case marked && err == nil:
			return prev
		case same || marked:
			if err == nil {
				err = errors.New("里面没有 railse 记的标记行")
			}
			fmt.Fprintf(stderr, "警告：读不到 %s 里记的前一跳（%v），这次没有前一跳；原来另有 notify 的，从 %s 的备份（%s*）里找回它写回 notify，"+
				"再重跑 railse setup codex-notify\n", first, err, show, show+backupSuffix)
			return nil
		}
	}
	if len(n.Argv) >= 3 && filepath.Base(first) == "railse" && n.Argv[1] == "hook" && n.Argv[2] == "codex-notify" {
		return nil
	}
	return n.Argv
}
