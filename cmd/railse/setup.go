package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const setupUsage = `用法:
  railse setup hooks [--project 目录] [--claude] [--codex] [--gemini] [--codex-file 路径] [--bin 路径] [--dry-run]
    默认三个都装。Claude 与 Gemini 写项目级配置，Codex 写用户级 ~/.codex/hooks.json。`

type hookEntry struct {
	Name    string `json:"name,omitempty"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type hookGroup struct {
	Matcher *string     `json:"matcher,omitempty"`
	Hooks   []hookEntry `json:"hooks"`
}

func strp(s string) *string { return &s }

// shellQuote 让 bin 能安全地拼进钩子命令（三家 CLI 都交给 shell 执行）：
// 普通路径原样返回，含空格等特殊字符时用单引号包起来。
func shellQuote(s string) string {
	plain := s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@%,=", r))
	}) < 0
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hookPlan 给出每个 CLI 要写入的事件与条目。
func hookPlan(cli, bin string) map[string][]hookGroup {
	cmd := shellQuote(bin) + " hook " + cli
	one := func(name string, timeout int) []hookGroup {
		return []hookGroup{{Hooks: []hookEntry{{Name: name, Type: "command", Command: cmd, Timeout: timeout}}}}
	}
	switch cli {
	case "claude":
		return map[string][]hookGroup{
			"PermissionRequest": {{Matcher: strp(""), Hooks: []hookEntry{{Type: "command", Command: cmd, Timeout: 86400}}}},
			"Notification":      one("", 0),
			"Stop":              one("", 0),
			"SessionEnd":        one("", 0),
		}
	case "codex":
		return map[string][]hookGroup{
			"PermissionRequest": {{Matcher: strp(""), Hooks: []hookEntry{{Type: "command", Command: cmd, Timeout: 86400}}}},
			"Stop":              one("", 0),
			"SessionEnd":        one("", 3),
		}
	case "gemini":
		return map[string][]hookGroup{
			"BeforeTool":   {{Matcher: strp(".*"), Hooks: []hookEntry{{Name: "railse-before-tool", Type: "command", Command: cmd, Timeout: 86400000}}}},
			"Notification": one("railse-notification", 0),
			"AfterAgent":   one("railse-after-agent", 0),
			"SessionEnd":   one("railse-session-end", 0),
		}
	}
	return nil
}

// isOurs 判断一个钩子条目组是不是 Railse 写的：任一条目的 name 以 railse- 开头，
// 或命令是“<程序> hook …”且程序是本次的 bin，或程序文件名是 railse（换过安装路径时留下的旧条目）。
func isOurs(group map[string]any, bin string) bool {
	hooks, _ := group["hooks"].([]any)
	for _, h := range hooks {
		m, _ := h.(map[string]any)
		cmd, _ := m["command"].(string)
		name, _ := m["name"].(string)
		if strings.HasPrefix(name, "railse-") || strings.HasPrefix(cmd, shellQuote(bin)+" hook ") {
			return true
		}
		if i := strings.LastIndex(cmd, " hook "); i > 0 {
			prog := cmd[:i]
			if len(prog) >= 2 && prog[0] == '\'' && prog[len(prog)-1] == '\'' {
				prog = strings.ReplaceAll(prog[1:len(prog)-1], `'\''`, "'")
			}
			if filepath.Base(prog) == "railse" {
				return true
			}
		}
	}
	return false
}

// mergeHooks 把我们的条目并进现有配置：删旧的 Railse 条目，保留别人的，追加新的。
// 现有内容不是合法 JSON、顶层或 hooks 不是对象、某个事件不是数组时都视为损坏，返回错误不覆盖。
func mergeHooks(existing []byte, bin string, entries map[string][]hookGroup) ([]byte, error) {
	doc := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(existing))
		dec.UseNumber() // 别人的数字原样保留，不经 float64 丢精度
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, fmt.Errorf("现有配置不是合法 JSON，不覆盖: %w", err)
		}
		if _, err := dec.Token(); err != io.EOF {
			return nil, errors.New("现有配置不是合法 JSON（末尾有多余内容），不覆盖")
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("现有配置顶层不是对象，不覆盖")
		}
		doc = m
	}
	hooks := map[string]any{}
	if raw, ok := doc["hooks"]; ok {
		h, ok := raw.(map[string]any)
		if !ok {
			return nil, errors.New(`现有配置的 "hooks" 不是对象，不覆盖`)
		}
		hooks = h
	}
	// 先从所有事件里删掉我们旧的条目组
	for ev, raw := range hooks {
		groups, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf(`现有配置的 "hooks.%s" 不是数组，不覆盖`, ev)
		}
		kept := []any{}
		for _, g := range groups {
			if gm, ok := g.(map[string]any); ok && isOurs(gm, bin) {
				continue
			}
			kept = append(kept, g)
		}
		switch {
		case len(kept) == len(groups):
			// 没有我们的条目，原样不动
		case len(kept) == 0:
			delete(hooks, ev)
		default:
			hooks[ev] = kept
		}
	}
	// 再追加本次的
	for ev, groups := range entries {
		list, _ := hooks[ev].([]any)
		for _, g := range groups {
			list = append(list, g)
		}
		hooks[ev] = list
	}
	doc["hooks"] = hooks
	// map 按键排序输出，重复运行结果字节相同；不转义 &、<、>，别人的命令保持可读
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
		*codexFile = filepath.Join(home, ".codex", "hooks.json")
	}
	targets := []struct {
		cli  string
		on   bool
		path string
	}{
		{"claude", *doClaude, filepath.Join(absProject, ".claude", "settings.json")},
		{"codex", *doCodex, *codexFile},
		{"gemini", *doGemini, filepath.Join(absProject, ".gemini", "settings.json")},
	}
	type pending struct {
		show, path string // show 是给人看的路径，path 是解开符号链接后真正写的文件
		data       []byte
		mode       os.FileMode
	}
	// 先把所有目标都算好，任何一个出错就一个都不写
	var writes []pending
	for _, tg := range targets {
		if !tg.on {
			continue
		}
		path := tg.path
		if real, err := filepath.EvalSymlinks(path); err == nil {
			path = real // 配置是符号链接（如 dotfiles）时写到链接指向的文件，不把链接换成普通文件
		}
		existing, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			fmt.Fprintln(stderr, err)
			return 1
		}
		merged, err := mergeHooks(existing, *bin, hookPlan(tg.cli, *bin))
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", tg.path, err)
			return 1
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(path); err == nil {
			mode = st.Mode().Perm()
		}
		writes = append(writes, pending{tg.path, path, merged, mode})
	}
	for _, w := range writes {
		if *dryRun {
			fmt.Fprintf(stdout, "== %s ==\n%s", w.show, w.data)
			continue
		}
		if err := writeFileAtomic(w.path, w.data, w.mode); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "已写入 %s\n", w.show)
	}
	if *doCodex {
		fmt.Fprintf(stdout, "提醒 Codex：钩子只在交互会话里审批（codex exec 不触发）；首次使用请在 Codex 里用 /hooks 审核；notify 需手动配置为 %q。\n", *bin+" hook codex-notify")
	}
	if *doGemini {
		fmt.Fprintln(stdout, "提醒 Gemini：钩子只能拒绝不能放行，需要 Gemini CLI 0.65.0 以上，不要开 yolo 模式。")
	}
	return 0
}
