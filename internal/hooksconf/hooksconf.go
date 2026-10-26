// Package hooksconf 生成、合并、识别三家 CLI（Claude Code、Codex、Gemini CLI）的钩子配置。
// railse setup 用它写配置，railse doctor 用它只读地检查配置。
package hooksconf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// Entry 是钩子配置里的单个条目。
type Entry struct {
	Name    string `json:"name,omitempty"`
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

// Group 是一个事件下的条目组。
type Group struct {
	Matcher *string `json:"matcher,omitempty"`
	Hooks   []Entry `json:"hooks"`
}

func strp(s string) *string { return &s }

// ShellQuote 让 bin 能安全地拼进钩子命令（三家 CLI 都交给 shell 执行）：
// 普通路径原样返回，含空格等特殊字符时用单引号包起来。
func ShellQuote(s string) string {
	plain := s != "" && strings.IndexFunc(s, func(r rune) bool {
		safe := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/._-+:@%,=", r)
		return !safe
	}) < 0
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// HookCommand 是钩子命令“<bin> hook <cli>”；socket 非空时再加“ --socket <socket>”（守护进程不在默认目录时，
// 钩子进程的环境里没有那个 RAILSE_HOME，要靠它找到 socket）。路径都经 ShellQuote。
func HookCommand(bin, cli, socket string) string {
	cmd := ShellQuote(bin) + " hook " + cli
	if socket != "" {
		cmd += " --socket " + ShellQuote(socket)
	}
	return cmd
}

// Plan 给出每个 CLI 要写入的事件与条目；socket 见 HookCommand，为空时命令不带 --socket。
func Plan(cli, bin, socket string) map[string][]Group {
	cmd := HookCommand(bin, cli, socket)
	one := func(name string, timeout int) []Group {
		return []Group{{Hooks: []Entry{{Name: name, Type: "command", Command: cmd, Timeout: timeout}}}}
	}
	switch cli {
	case "claude":
		return map[string][]Group{
			"PermissionRequest": {{Matcher: strp(""), Hooks: []Entry{{Type: "command", Command: cmd, Timeout: 86400}}}},
			"Notification":      one("", 0),
			"Stop":              one("", 0),
			"SessionEnd":        one("", 0),
		}
	case "codex":
		return map[string][]Group{
			"PermissionRequest": {{Matcher: strp(""), Hooks: []Entry{{Type: "command", Command: cmd, Timeout: 86400}}}},
			"Stop":              one("", 0),
			"SessionEnd":        one("", 3),
		}
	case "gemini":
		return map[string][]Group{
			"BeforeTool":   {{Matcher: strp(".*"), Hooks: []Entry{{Name: "railse-before-tool", Type: "command", Command: cmd, Timeout: 86400000}}}},
			"Notification": one("railse-notification", 0),
			"AfterAgent":   one("railse-after-agent", 0),
			"SessionEnd":   one("railse-session-end", 0),
		}
	}
	return nil
}

// IsOurs 判断单个钩子条目是不是 Railse 写的：name 以 railse- 开头，
// 或命令是“<程序> hook …”且程序是本次的 bin，
// 或命令恰好是 Plan 写的形状（见 ParseCommand）且程序文件名是 railse（换过安装路径时留下的旧条目）。
// bin 为空时（doctor 不知道当前程序路径）第二条不用，只按 name 前缀与程序文件名认。
func IsOurs(entry any, bin string) bool {
	m, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	cmd, _ := m["command"].(string)
	name, _ := m["name"].(string)
	if strings.HasPrefix(name, "railse-") || bin != "" && strings.HasPrefix(cmd, ShellQuote(bin)+" hook ") {
		return true
	}
	if prog, ok := CommandProgram(cmd); ok && filepath.Base(prog) == "railse" {
		return true
	}
	return false
}

// withoutOurs 从一个条目组里删掉我们的条目。返回去掉后的组，以及这个组是否因此删空、应整组去掉。
// 组里没有我们的条目时原样返回；别人追加在我们组里的条目连同组的其他字段（如 matcher）都保留。
func withoutOurs(group any, bin string) (kept any, drop bool) {
	gm, ok := group.(map[string]any)
	if !ok {
		return group, false
	}
	hooks, ok := gm["hooks"].([]any)
	if !ok {
		return group, false
	}
	rest := []any{}
	for _, h := range hooks {
		if !IsOurs(h, bin) {
			rest = append(rest, h)
		}
	}
	switch {
	case len(rest) == len(hooks):
		return group, false
	case len(rest) == 0:
		return nil, true
	}
	gm["hooks"] = rest
	return gm, false
}

// Command 是从 Plan 写的钩子命令里认出的各部分（都已去掉引号）。Socket 为空表示命令不带 --socket（连默认 socket）。
type Command struct {
	Program, CLI, Socket string
}

// ParseCommand 认出 Plan 写过的命令形状“<词> hook claude|codex|gemini”，后面可以再跟“ --socket <词>”（见 HookCommand）。
// “词”是不含空白的裸词，或 ShellQuote 产生的单引号词（内部单引号按 ShellQuote 的写法转义）。其他形状一律不认。
func ParseCommand(cmd string) (Command, bool) { return parseHook(cmd, "claude", "codex", "gemini") }

// parseHook 认“<词> hook <clis 之一>”，后面可以再跟“ --socket <词>”（见 ParseCommand）。
func parseHook(cmd string, clis ...string) (Command, bool) {
	prog, rest, ok := shellWord(cmd)
	if !ok {
		return Command{}, false
	}
	var c Command
	for _, cli := range clis {
		if r, found := strings.CutPrefix(rest, " hook "+cli); found && (r == "" || strings.HasPrefix(r, " --socket ")) {
			c, rest = Command{Program: prog, CLI: cli}, r
			break
		}
	}
	if c.CLI == "" {
		return Command{}, false
	}
	if rest == "" {
		return c, true
	}
	sock, tail, ok := shellWord(strings.TrimPrefix(rest, " --socket "))
	if !ok || tail != "" {
		return Command{}, false
	}
	c.Socket = sock
	return c, true
}

// shellWord 从 s 的开头取一个词（见 ParseCommand），返回去掉引号的词与其后的剩余部分；不是这两种词或词为空时 ok 为 false。
func shellWord(s string) (word, rest string, ok bool) {
	if !strings.HasPrefix(s, "'") {
		i := strings.IndexAny(s, " \t\r\n")
		if i < 0 {
			i = len(s)
		}
		if i == 0 {
			return "", "", false
		}
		return s[:i], s[i:], true
	}
	var b strings.Builder
	i := 1
	for {
		j := strings.IndexByte(s[i:], '\'')
		if j < 0 {
			return "", "", false
		}
		b.WriteString(s[i : i+j])
		i += j + 1
		if !strings.HasPrefix(s[i:], `\''`) {
			break
		}
		b.WriteByte('\'')
		i += 3
	}
	if b.Len() == 0 || i < len(s) && !strings.ContainsRune(" \t\r\n", rune(s[i])) {
		return "", "", false
	}
	return b.String(), s[i:], true
}

// CommandProgram 是 ParseCommand 认出的程序路径。
func CommandProgram(cmd string) (string, bool) {
	c, ok := ParseCommand(cmd)
	return c.Program, ok
}

// parse 解析一份钩子配置：返回整份文档与其中的 hooks 对象（没有 hooks 键时 hooks 为 nil）。
// 空文档当作空对象；不是合法 JSON、末尾有多余内容、顶层或 hooks 不是对象时返回错误。数字保持 json.Number。
func parse(existing []byte) (doc, hooks map[string]any, err error) {
	doc = map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(existing))
		dec.UseNumber() // 别人的数字原样保留，不经 float64 丢精度
		var v any
		if err := dec.Decode(&v); err != nil {
			return nil, nil, fmt.Errorf("不是合法 JSON（%w）", err)
		}
		if _, err := dec.Token(); err != io.EOF {
			return nil, nil, errors.New("不是合法 JSON（末尾有多余内容）")
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, nil, errors.New("顶层不是对象")
		}
		doc = m
	}
	if raw, ok := doc["hooks"]; ok {
		h, ok := raw.(map[string]any)
		if !ok {
			return nil, nil, errors.New(`"hooks" 不是对象`)
		}
		hooks = h
	}
	return doc, hooks, nil
}

// Merge 把我们的条目并进现有配置：逐个条目删旧的 Railse 条目（组删空了才删组），保留别人的，追加新的。
// 现有内容不是合法 JSON、顶层或 hooks 不是对象、本次要写的事件不是数组时都视为损坏，返回错误不覆盖；
// hooks 里其他非数组的值（如 Gemini 的开关项）原样保留。
// entries 为空（nil）时只删不加：hooks 因删掉我们的条目而变空时整个 hooks 键也去掉，原本没有 hooks 键时不加。
func Merge(existing []byte, bin string, entries map[string][]Group) ([]byte, error) {
	doc, hooks, err := parse(existing)
	if err != nil {
		return nil, fmt.Errorf("现有配置有误：%w，不覆盖", err)
	}
	hadHooks := hooks != nil
	if hooks == nil {
		hooks = map[string]any{}
	}
	before := len(hooks)
	for ev := range entries {
		if raw, ok := hooks[ev]; ok {
			if _, ok := raw.([]any); !ok {
				return nil, fmt.Errorf(`现有配置的 "hooks.%s" 不是数组，不覆盖`, ev)
			}
		}
	}
	// 先从所有事件里逐个条目删掉我们旧的条目；组里只剩我们的条目时整组删掉
	for ev, raw := range hooks {
		groups, ok := raw.([]any)
		if !ok {
			continue // 不是事件数组（我们不写的键），原样保留
		}
		kept := []any{}
		for _, g := range groups {
			if g2, drop := withoutOurs(g, bin); !drop {
				kept = append(kept, g2)
			}
		}
		switch {
		case len(kept) == 0 && len(groups) > 0:
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
	switch {
	case len(entries) > 0:
		doc["hooks"] = hooks
	case len(hooks) == 0 && before > 0:
		delete(doc, "hooks") // 只删不加，且 hooks 是被我们删空的
	case hadHooks:
		doc["hooks"] = hooks
	}
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

// Expected 是每个 CLI 必须装的事件与各自的超时下限（秒；Gemini 是毫秒），与 Plan 写出的值一致。
func Expected(cli string) map[string]int {
	switch cli {
	case "claude":
		return map[string]int{"PermissionRequest": 86400, "Notification": 0, "Stop": 0, "SessionEnd": 0}
	case "codex":
		return map[string]int{"PermissionRequest": 86400, "Stop": 0, "SessionEnd": 0}
	case "gemini":
		return map[string]int{"BeforeTool": 86400000, "Notification": 0, "AfterAgent": 0, "SessionEnd": 0}
	}
	return nil
}

// EventState 是一个事件里我们的条目的状态。
type EventState struct {
	Present bool
	Timeout int    // 我们的条目的 timeout；同一事件有多条时取最小的（超时下限要每条都满足）
	Program string // 从命令里认出的程序路径；命令不是 Plan 写的形状时为空
	Socket  string // 命令里 --socket 给的路径；不带 --socket（连默认 socket）或命令不是 Plan 写的形状时为空
}

// Installed 是一份钩子配置里我们的条目，按事件名索引；没有我们条目的事件不在里面。
type Installed struct {
	Events map[string]EventState
}

// Inspect 读一份钩子配置，报告每个事件里有没有我们的条目、超时与程序路径。bin 为空时按“程序文件名是 railse”认。
// 只读，不改 doc。空文档没有任何事件；不是合法 JSON、顶层或 hooks 不是对象时返回错误；hooks 里不是数组的值跳过。
func Inspect(doc []byte, bin string) (Installed, error) {
	ins := Installed{Events: map[string]EventState{}}
	_, hooks, err := parse(doc)
	if err != nil {
		return ins, err
	}
	for ev, raw := range hooks {
		groups, _ := raw.([]any)
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			list, _ := gm["hooks"].([]any)
			for _, e := range list {
				if !IsOurs(e, bin) {
					continue
				}
				em := e.(map[string]any)
				st := EventState{Present: true}
				if cmd, _ := em["command"].(string); cmd != "" {
					if c, ok := ParseCommand(cmd); ok {
						st.Program, st.Socket = c.Program, c.Socket
					}
				}
				if n, ok := em["timeout"].(json.Number); ok {
					if i, err := n.Int64(); err == nil {
						st.Timeout = int(i)
					} else if f, err := n.Float64(); err == nil {
						st.Timeout = int(f)
					}
				}
				if prev, ok := ins.Events[ev]; ok && prev.Timeout <= st.Timeout {
					continue
				}
				ins.Events[ev] = st
			}
		}
	}
	return ins, nil
}

// SameOurs 报告两份钩子配置里我们的条目是否完全相同：每个事件里我们的条目作为集合相同。条目按整条比（type、command、
// timeout、name 等全部字段，连同所在组的 matcher），用规范化后的 JSON（键排序、无空白）比，不挑字段；别人的条目、别的设置、
// 键的顺序与排版不影响。Claude Code 对写在多个配置文件里的相同处理器只跑一次，doctor 据此判断两处都装时会不会触发两次。
// 只读；任一份解析不了时返回 false。bin 同 IsOurs。
func SameOurs(a, b []byte, bin string) bool {
	sa, err := ourEntries(a, bin)
	if err != nil {
		return false
	}
	sb, err := ourEntries(b, bin)
	if err != nil {
		return false
	}
	return maps.EqualFunc(sa, sb, slices.Equal[[]string])
}

// ourEntries 是一份配置里我们的条目：事件名 → 条目（连同所在组的 matcher）规范化后的 JSON，排好序、去掉重复。
func ourEntries(doc []byte, bin string) (map[string][]string, error) {
	_, hooks, err := parse(doc)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for ev, raw := range hooks {
		groups, _ := raw.([]any)
		for _, g := range groups {
			gm, _ := g.(map[string]any)
			list, _ := gm["hooks"].([]any)
			for _, e := range list {
				if !IsOurs(e, bin) {
					continue
				}
				key := map[string]any{"entry": e}
				if m, ok := gm["matcher"]; ok {
					key["matcher"] = m
				}
				j, err := json.Marshal(key) // map 按键排序；数字是 json.Number，原样输出
				if err != nil {
					return nil, err
				}
				out[ev] = append(out[ev], string(j))
			}
		}
	}
	for ev, list := range out {
		slices.Sort(list)
		out[ev] = slices.Compact(list)
	}
	return out, nil
}

// ClaudePaths 返回项目级的本机配置与共享配置：钩子写进 settings.local.json（不入库），settings.json 里只做清理。
func ClaudePaths(project string) (local, shared string) {
	return filepath.Join(project, ".claude", "settings.local.json"), filepath.Join(project, ".claude", "settings.json")
}

// GeminiPath 是 Gemini 的项目级配置。
func GeminiPath(project string) string { return filepath.Join(project, ".gemini", "settings.json") }

// CodexPath 是 Codex 的用户级钩子文件。
func CodexPath(home string) string { return filepath.Join(home, ".codex", "hooks.json") }

// ClaudeUserPath 是 Claude Code 的用户级配置（对所有项目生效）：railse setup hooks 不给 --project 时钩子写在这里。
func ClaudeUserPath(home string) string { return filepath.Join(home, ".claude", "settings.json") }

// GeminiUserPath 是 Gemini CLI 的用户级配置（对所有项目生效）：railse setup hooks 不给 --project 时钩子写在这里。
func GeminiUserPath(home string) string { return filepath.Join(home, ".gemini", "settings.json") }
