package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/xufei547/railse-opc/internal/hooksconf"
)

// codexNotifyRe 认 notify = [..., "hook", "codex-notify"]，后面可以再跟 "--socket", "<路径>"（第 1 个子匹配是带引号的路径）；
// 数组可以跨行（[^\]]* 不越出这个数组）。
var codexNotifyRe = regexp.MustCompile(`(?m)^[ \t]*notify[ \t]*=[ \t]*\[[^\]]*["']hook["'][ \t\r\n]*,[ \t\r\n]*["']codex-notify["']` +
	`(?:[ \t\r\n]*,[ \t\r\n]*["']--socket["'][ \t\r\n]*,[ \t\r\n]*("(?:[^"\\\r\n]|\\.)*"|'[^'\r\n]*'))?`)

// tomlTableRe 认 TOML 的表头行（[x] 或 [[x]]）：顶层键必须写在第一个表头之前。
var tomlTableRe = regexp.MustCompile(`(?m)^[ \t]*\[[^\r\n]*`)

// hookChecks 看三个 CLI 的钩子。Claude、Gemini 的用户级（Home 下）或项目级（Project 下）任一装了就算装了（见 levelsCheck）；
// Codex 的钩子与 notify 只有用户级，只看 Home。
func hookChecks(o Options) []Check {
	codex := []Check{
		{ID: "hooks.codex", Name: "Codex 钩子", Status: Skip, Detail: noHome},
		{ID: "hooks.codex_notify", Name: "Codex notify", Status: Skip, Detail: noHome},
	}
	if o.Home != "" { // Codex 的钩子与 notify 都在用户级的 ~/.codex 下
		codex = []Check{hookFileCheck("hooks.codex", "Codex 钩子", "codex", hooksconf.CodexPath(o.Home), setupHint(o, "codex", ""), o), codexNotifyCheck(o)}
	}
	return []Check{
		claudeHookCheck(o),
		codex[0],
		codex[1],
		geminiHookCheck(o),
	}
}

// setupHint 是重装某个 CLI 钩子的命令：project 为空时装用户级（Codex 只有用户级），否则带 --project 装那个项目的项目级。
// 守护进程不在 ~/.railse 时带上 --dir（setup hooks 据此在钩子命令里写 --socket）。
func setupHint(o Options, cli, project string) string {
	h := "railse setup hooks"
	if project != "" {
		h += " --project " + hooksconf.ShellQuote(project)
	}
	return h + " --" + cli + dirFlag(o)
}

// agentSocket 是 --dir 那个守护进程的 socket。
func agentSocket(o Options) string { return filepath.Join(o.AgentDir, "agent.sock") }

// dirFlag 是重装钩子的命令里的 --dir：守护进程的 socket 就是基准（~/.railse/agent.sock）时为空，否则（含推不出基准的）带上。
func dirFlag(o Options) string {
	if o.DefaultSocket != "" && samePath(agentSocket(o), o.DefaultSocket) {
		return ""
	}
	return " --dir " + hooksconf.ShellQuote(o.AgentDir)
}

// socketMismatch 看钩子连的 socket 与 --dir 的守护进程对不对得上。explicit 是命令里 --socket 给的路径（空表示没带，
// 按基准 DefaultSocket 算）；对得上或推不出基准时 ok 为真。对不上时返回钩子实际连的 socket。
func socketMismatch(o Options, explicit string) (connects string, ok bool) {
	connects = explicit
	if connects == "" {
		connects = o.DefaultSocket
	}
	if connects == "" || samePath(connects, agentSocket(o)) {
		return "", true
	}
	return connects, false
}

// socketDetail 与 socketHint 说钩子（或 notify）连的 socket 对不上守护进程：重装命令带 --dir；有显式 --socket 的，
// 也可能是 doctor 自己没给对 --dir。
func socketDetail(what string, socks []string, o Options) string {
	return fmt.Sprintf("%s连的是 %s，守护进程在 %s", what, strings.Join(socks, "、"), agentSocket(o))
}

func socketHint(fix string, explicitDirs []string, o Options) string {
	h := fix + "，让它连 " + o.AgentDir + " 里的守护进程"
	if len(explicitDirs) > 0 {
		h += "；守护进程确实在 " + strings.Join(explicitDirs, "、") + " 的话，railse doctor 加 --dir " + hooksconf.ShellQuote(explicitDirs[0])
	}
	return h
}

// hasOurs 报告 path 是一份读得了、解析得了、里面有我们条目的钩子配置。
func hasOurs(path, bin string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	ins, err := hooksconf.Inspect(b, bin)
	return err == nil && len(ins.Events) > 0
}

// broken 报告 path 在、却读不了或不是合法的钩子配置（这时 hookFileCheck 报 fail）。
func broken(path, bin string) bool {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	_, err = hooksconf.Inspect(b, bin)
	return err != nil
}

func sameFile(a, b string) bool {
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

// claudeHookCheck 看 Claude 的钩子：用户级 ~/.claude/settings.json，或项目级（见 claudeProjectCheck）。
func claudeHookCheck(o Options) Check {
	const id, name = "hooks.claude", "Claude Code 钩子"
	var project []string
	if o.Project != "" {
		local, shared := hooksconf.ClaudePaths(o.Project)
		project = []string{local, shared}
	}
	return levelsCheck(id, name, "claude", userPath(o, hooksconf.ClaudeUserPath), project, func() Check { return claudeProjectCheck(o) }, o)
}

// geminiHookCheck 看 Gemini 的钩子：用户级 ~/.gemini/settings.json，或项目级 <项目>/.gemini/settings.json。
func geminiHookCheck(o Options) Check {
	const id, name = "hooks.gemini", "Gemini 钩子"
	var project []string
	if o.Project != "" {
		project = []string{hooksconf.GeminiPath(o.Project)}
	}
	projectCheck := func() Check {
		return hookFileCheck(id, name, "gemini", hooksconf.GeminiPath(o.Project), setupHint(o, "gemini", o.Project), o)
	}
	return levelsCheck(id, name, "gemini", userPath(o, hooksconf.GeminiUserPath), project, projectCheck, o)
}

// userPath 是 Home 下的用户级配置；Home 为空时为空（不拼相对路径）。
func userPath(o Options, path func(home string) string) string {
	if o.Home == "" {
		return ""
	}
	return path(o.Home)
}

// levelsCheck 合起 Claude、Gemini 的用户级与项目级：任一处装了就算装了。user 是用户级文件（Home 为空时为空，只看项目级），
// project 是项目级要看的文件（Project 为空时为空，只看用户级），projectCheck 是只看项目级时的检查（沿用项目级的规则，
// 修复提示带 --project）。同一个文件不算两处：项目就是家目录、项目的文件链到用户级的文件时，按文件身份去重。
// 只有用户级装了：按用户级文件查（hookFileCheck）；两处都装：同样按用户级查，至少 warn（会触发两次），提示删掉项目级的条目；
// 只有项目级装了：projectCheck；都没装：fail，列出看过的位置，提示装用户级。用户级（或都没装时项目级）的文件读不了、
// 不是合法 JSON 时报 fail（hookFileCheck 的写法）。
func levelsCheck(id, name, cli, user string, project []string, projectCheck func() Check, o Options) Check {
	switch {
	case user == "" && len(project) == 0:
		return Check{ID: id, Name: name, Status: Skip, Detail: "取不到家目录（$HOME 没设），也没有指定项目目录（--project），跳过"}
	case user == "":
		return projectCheck()
	}
	hint := setupHint(o, cli, "")
	looked := []string{user}
	var projLooked, projHas []string
	for _, p := range project {
		if slices.ContainsFunc(looked, func(q string) bool { return samePath(p, q) }) {
			continue
		}
		looked = append(looked, p)
		projLooked = append(projLooked, p)
		if hasOurs(p, o.Bin) {
			projHas = append(projHas, p)
		}
	}
	userHas := hasOurs(user, o.Bin)
	switch {
	case userHas && len(projHas) > 0:
		c := hookFileCheck(id, name, cli, user, hint, o)
		if c.Status == OK {
			c.Status = Warn
		}
		files := strings.Join(projHas, "、")
		c.Detail += "；项目级 " + files + " 也装了，钩子会触发两次"
		del := "删掉 " + files + " 里 Railse 的钩子条目（用户级已对所有项目生效）"
		if c.Hint == "" {
			c.Hint = del
		} else {
			c.Hint += "；" + del
		}
		return c
	case userHas || broken(user, o.Bin): // 用户级坏了：先修它，不给 --project 的 setup hooks 也要往里写
		return hookFileCheck(id, name, cli, user, hint, o)
	case len(projHas) > 0:
		return projectCheck()
	}
	for _, p := range projLooked {
		if broken(p, o.Bin) {
			return hookFileCheck(id, name, cli, p, hint, o)
		}
	}
	return Check{ID: id, Name: name, Status: Fail, Detail: "没装钩子（看过 " + strings.Join(looked, "、") + "）", Hint: hint}
}

// claudeProjectCheck 看 Claude 的项目级钩子：现在写在 .claude/settings.local.json，旧版写在 .claude/settings.json。
// 只在旧位置：钩子照样生效，报 warn 提示迁走（旧位置的条目本身不全时照实 fail）；两处都有：会触发两次，warn；
// 两处是同一个文件（链接）不算两处；两处都没有：fail；settings.local.json 是坏 JSON 时报它（setup 也要往里写）。
func claudeProjectCheck(o Options) Check {
	const id, name = "hooks.claude", "Claude Code 钩子"
	local, shared := hooksconf.ClaudePaths(o.Project)
	localHas, sharedHas := hasOurs(local, o.Bin), hasOurs(shared, o.Bin) && !sameFile(local, shared)
	hint := setupHint(o, "claude", o.Project)
	migrate := "运行 " + hint + " 迁到 settings.local.json"
	switch {
	case sharedHas && !localHas:
		if b, err := os.ReadFile(local); err == nil {
			if _, err := hooksconf.Inspect(b, o.Bin); err != nil { // settings.local.json 坏了：先修它，setup 也要往里写
				c := hookFileCheck(id, name, "claude", local, hint, o)
				c.Detail += "；钩子还在 .claude/settings.json（旧位置）"
				return c
			}
		}
		c := hookFileCheck(id, name, "claude", shared, hint, o)
		if c.Status == Fail {
			c.Detail = "钩子还在 .claude/settings.json（旧位置），且不全：" + c.Detail
			return c
		}
		detail := "钩子还在 .claude/settings.json（旧位置）"
		if c.Status == Warn { // 程序路径不同：同一条命令一并改掉
			detail += "；" + strings.TrimPrefix(c.Detail, shared+"：")
		}
		return Check{ID: id, Name: name, Status: Warn, Detail: detail, Hint: migrate}
	case sharedHas && localHas:
		c := hookFileCheck(id, name, "claude", local, hint, o)
		if c.Status == OK {
			c.Status = Warn
		}
		c.Detail += "；settings.json 与 settings.local.json 都装了，钩子会触发两次"
		if c.Hint == "" {
			c.Hint = migrate
		} else {
			c.Hint += "（同一条命令也会把 settings.json 里的迁走）"
		}
		return c
	}
	return hookFileCheck(id, name, "claude", local, hint, o)
}

// hookFileCheck 看一份钩子配置：Expected(cli) 的每个事件都有我们的条目且超时不低于下限；Bin 非空时程序路径要是它。
// hint 是重装这份配置的命令（setupHint）。
func hookFileCheck(id, name, cli, path, hint string, o Options) Check {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return Check{ID: id, Name: name, Status: Fail, Detail: path + " 不存在，没装钩子", Hint: hint}
	}
	if err != nil {
		return Check{ID: id, Name: name, Status: Fail, Detail: err.Error(), Hint: "确认用跑 CLI 的同一个用户运行 doctor"}
	}
	ins, err := hooksconf.Inspect(b, o.Bin)
	if err != nil {
		return Check{ID: id, Name: name, Status: Fail, Detail: path + "：" + err.Error(), Hint: "修好 JSON 后再 " + hint}
	}
	var missing, short, others, socks, explicitDirs []string
	seen := map[string]bool{}
	for ev, min := range hooksconf.Expected(cli) {
		st := ins.Events[ev]
		switch {
		case !st.Present:
			missing = append(missing, ev)
		case st.Timeout < min:
			short = append(short, fmt.Sprintf("%s（超时 %d，要 ≥ %d）", ev, st.Timeout, min))
		}
		if st.Program != "" && o.Bin != "" && !samePath(st.Program, o.Bin) && !seen[st.Program] {
			seen[st.Program] = true
			others = append(others, st.Program)
		}
		// 认不出命令形状的（别人改过的、只按 name 认的）不比对 socket
		if st.Present && (st.Program != "" || st.Socket != "") {
			if s, ok := socketMismatch(o, st.Socket); !ok && !seen["socket:"+s] {
				seen["socket:"+s] = true
				socks = append(socks, s)
				if st.Socket != "" {
					explicitDirs = append(explicitDirs, filepath.Dir(st.Socket))
				}
			}
		}
	}
	sort.Strings(missing)
	sort.Strings(short)
	sort.Strings(others)
	sort.Strings(socks)
	sort.Strings(explicitDirs)
	if len(missing) > 0 || len(short) > 0 {
		var parts []string
		if len(missing) > 0 {
			parts = append(parts, "缺 "+strings.Join(missing, "、"))
		}
		if len(short) > 0 {
			parts = append(parts, "超时不够："+strings.Join(short, "、"))
		}
		return Check{ID: id, Name: name, Status: Fail, Detail: path + "：" + strings.Join(parts, "；"), Hint: hint}
	}
	if len(others) > 0 || len(socks) > 0 {
		var parts []string
		h := "换过安装路径就重新 " + hint
		if len(others) > 0 {
			parts = append(parts, fmt.Sprintf("钩子指向 %s，当前程序是 %s", strings.Join(others, "、"), o.Bin))
		}
		if len(socks) > 0 {
			parts = append(parts, socketDetail("钩子", socks, o))
			h = socketHint("运行 "+hint+" 重装钩子", explicitDirs, o)
		}
		return Check{ID: id, Name: name, Status: Warn, Detail: path + "：" + strings.Join(parts, "；"), Hint: h}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: path}
}

// samePath 报告两个路径是不是同一个程序：字面相同，或都存在且是同一个文件（如 Homebrew 的链接与它指向的文件）。
func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b) || sameFile(a, b)
}

// codexNotifyCheck 看 ~/.codex/config.toml 顶层有没有 notify = [..., "hook", "codex-notify"]：没有时 Codex 的完成与提问事件进不了收件箱。
// notify 是顶层键，写在某个 [表] 下面就成了那个表的键，Codex 读不到。
func codexNotifyCheck(o Options) Check {
	const id, name = "hooks.codex_notify", "Codex notify"
	path := filepath.Join(o.Home, ".codex", "config.toml")
	hint := "把 railse setup hooks 打印的 notify = [...] 那一行加到 ~/.codex/config.toml 的开头（第一个 [表] 之前）"
	b, err := os.ReadFile(path)
	if err != nil {
		return Check{ID: id, Name: name, Status: Warn, Detail: path + " 读不到，Codex 的完成与提问事件不会进收件箱", Hint: hint}
	}
	top := b
	header := tomlTableRe.FindIndex(b)
	if header != nil {
		top = b[:header[0]]
	}
	if m := codexNotifyRe.FindSubmatch(top); m != nil {
		explicit, known := tomlString(string(m[1]))
		if known {
			if s, ok := socketMismatch(o, explicit); !ok {
				var dirs []string
				if explicit != "" {
					dirs = []string{filepath.Dir(explicit)}
				}
				fix := "把 railse setup hooks --codex" + dirFlag(o) + " 打印的 notify = [...] 那一行换进 ~/.codex/config.toml"
				return Check{ID: id, Name: name, Status: Warn, Detail: path + "：" + socketDetail("notify ", []string{s}, o), Hint: socketHint(fix, dirs, o)}
			}
		}
		return Check{ID: id, Name: name, Status: OK, Detail: path}
	}
	if loc := codexNotifyRe.FindIndex(b); loc != nil {
		// 找出 notify 落在哪个表下面
		table := ""
		for _, h := range tomlTableRe.FindAllIndex(b[:loc[0]], -1) {
			table = strings.TrimSpace(string(b[h[0]:h[1]]))
		}
		return Check{ID: id, Name: name, Status: Warn, Detail: fmt.Sprintf("%s 里的 notify 写在了 %s 下面，Codex 读不到", path, table), Hint: "把 notify 那一行挪到文件开头（第一个 [表] 之前）"}
	}
	return Check{ID: id, Name: name, Status: Warn, Detail: path + ` 里没有 notify = ["…/railse", "hook", "codex-notify"]，Codex 的完成与提问事件不会进收件箱`, Hint: hint}
}

// tomlString 解出 TOML 的一个字符串：基本字符串（双引号，转义与 Go 的一致到普通路径够用）或字面字符串（单引号，不转义）。
// 空串表示没有（返回 "", true）；解不出时 known 为 false，调用方不据此下结论。
func tomlString(q string) (s string, known bool) {
	switch {
	case q == "":
		return "", true
	case strings.HasPrefix(q, "'"):
		return strings.Trim(q, "'"), true
	}
	s, err := strconv.Unquote(q)
	return s, err == nil
}
