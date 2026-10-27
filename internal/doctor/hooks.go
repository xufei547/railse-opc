package doctor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"syscall"

	"github.com/xufei547/railse-opc/internal/hooksconf"
)

// hookChecks 看三个 CLI 的钩子。Claude、Gemini 的用户级（Home 下）或项目级（Project 下）任一装了就算装了（见 levelsCheck）；
// Codex 的钩子与 notify 只有用户级，只看 Home。
func hookChecks(o Options) []Check {
	codex := []Check{
		{ID: "hooks.codex", Name: "Codex 钩子", Status: Skip, Detail: noHome},
		{ID: codexNotifyID, Name: codexNotifyName, Status: Skip, Detail: noHome},
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

// sameOurs 报告两份配置文件里 Railse 的条目是否完全相同（见 hooksconf.SameOurs）；读不了的不算相同。
func sameOurs(a, b, bin string) bool {
	ba, err := os.ReadFile(a)
	if err != nil {
		return false
	}
	bb, err := os.ReadFile(b)
	return err == nil && hooksconf.SameOurs(ba, bb, bin)
}

// sameHandlers 是 Claude 两处都装了相同的钩子时细节里的说明：Claude Code 对相同的处理器只跑一次，不算重复触发。
func sameHandlers(other, remove string) string {
	return other + " 也装了相同的钩子（Claude Code 对相同的处理器只跑一次），可以删掉 " + remove
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
// 只有用户级装了：按用户级文件查（hookFileCheck）；两处都装：同样按用户级查，项目级里 Railse 的条目与用户级的不同时
// 至少 warn（会触发两次），提示删掉项目级的条目；Claude 两处的条目完全相同时 Claude Code 只跑一次，不降级，只在细节里说
// 可以删掉（Gemini 的文档没说去重，一律按会触发两次算）；
// 只有项目级装了：projectCheck；都没装：fail，列出看过的位置，提示装用户级。用户级的文件读不了、不是合法 JSON 时报 fail
// （hookFileCheck 的写法）；都没装而项目级的文件坏了时报那个文件，提示带 --project，细节里也说用户级没装。
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
		var same, twice []string
		for _, p := range projHas {
			if cli == "claude" && sameOurs(user, p, o.Bin) {
				same = append(same, p)
			} else {
				twice = append(twice, p)
			}
		}
		if len(same) > 0 {
			files := strings.Join(same, "、")
			c.Detail += "；" + sameHandlers(files, files+" 里 Railse 的条目（用户级已对所有项目生效）")
		}
		if len(twice) > 0 {
			if c.Status == OK {
				c.Status = Warn
			}
			files := strings.Join(twice, "、")
			c.Detail += "；项目级 " + files + " 也装了，钩子会触发两次"
			del := "删掉 " + files + " 里 Railse 的钩子条目（用户级已对所有项目生效）"
			if c.Hint == "" {
				c.Hint = del
			} else {
				c.Hint += "；" + del
			}
		}
		return c
	case userHas || broken(user, o.Bin): // 用户级坏了：先修它，不给 --project 的 setup hooks 也要往里写
		return hookFileCheck(id, name, cli, user, hint, o)
	case len(projHas) > 0:
		return projectCheck()
	}
	// 都没装、项目级的文件坏了：报它，提示修好它（装用户级的命令不碰项目里的文件，所以给带 --project 的命令）
	for _, p := range projLooked {
		if broken(p, o.Bin) {
			c := hookFileCheck(id, name, cli, p, setupHint(o, cli, o.Project), o)
			c.Detail += "；用户级 " + user + " 也没装"
			return c
		}
	}
	return Check{ID: id, Name: name, Status: Fail, Detail: "没装钩子（看过 " + strings.Join(looked, "、") + "）", Hint: hint}
}

// claudeProjectCheck 看 Claude 的项目级钩子：现在写在 .claude/settings.local.json，旧版写在 .claude/settings.json。
// 只在旧位置：钩子照样生效，报 warn 提示迁走（旧位置的条目本身不全时照实 fail）；两处都有：Railse 的条目不同时会触发两次，
// warn，完全相同时 Claude Code 只跑一次，不降级，细节里说可以迁走；两处是同一个文件（链接）不算两处；两处都没有：fail；
// settings.local.json 是坏 JSON 时报它（setup 也要往里写）。
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
		if sameOurs(local, shared, o.Bin) {
			c.Detail += "；" + sameHandlers(shared, shared+" 里 Railse 的条目（运行 "+hint+" 会迁走）")
		} else {
			if c.Status == OK {
				c.Status = Warn
			}
			c.Detail += "；settings.json 与 settings.local.json 都装了，钩子会触发两次"
		}
		switch {
		case c.Status == OK:
		case c.Hint == "":
			c.Hint = migrate
		default:
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

// codexNotifyCheck 看 ~/.codex/config.toml 顶层的 notify：没有 railse 时 Codex 的完成与提问事件进不了收件箱。认两种写法：
// railse setup codex-notify 写的串接脚本（单元素数组，文件名 codex-notify.sh，见 codexNotifyScriptCheck），与直接调 railse 的
// 旧写法 [..., "hook", "codex-notify"]（后面可以再跟 "--socket", "<路径>"）。两种都按 railse 连的 socket 与 --dir 的守护进程比对。
// notify 是顶层键，写在某个 [表] 下面就成了那个表的键，Codex 读不到。解析与 setup 共用 hooksconf.ParseCodexNotify。
func codexNotifyCheck(o Options) Check {
	path := hooksconf.CodexConfigPath(o.Home)
	fix := "railse setup codex-notify" + dirFlag(o)
	hint := "运行 " + fix + "（会保留已有的 notify）"
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR): // 新用户第一次体检多半是这样：说不存在，不说成权限问题
		return notifyWarn(path+" 不存在，Codex 的完成与提问事件不会进收件箱", hint)
	case err != nil:
		why := err
		var pe *fs.PathError
		if errors.As(err, &pe) { // 细节里不重复路径
			why = pe.Err
		}
		return notifyWarn(path+" 读不到（"+why.Error()+"），Codex 的完成与提问事件不会进收件箱", "确认用跑 Codex 的同一个用户运行 doctor")
	}
	n, err := hooksconf.ParseCodexNotify(b)
	switch {
	case err != nil:
		return notifyWarn(path+" 看不懂："+err.Error(), "手动改好这一处，再运行 "+fix)
	case !n.Found && len(n.Tables) > 0:
		table := n.Tables[0]
		return notifyWarn(fmt.Sprintf("%s 里的 notify 写在了 %s 下面，Codex 读不到", path, table), "运行 "+fix+"（在文件开头写顶层的 notify），再删掉 "+table+" 下面那个")
	case !n.Found:
		return notifyWarn(path+" 里没有 notify，Codex 的完成与提问事件不会进收件箱", hint)
	case len(n.Argv) == 1 && filepath.Base(n.Argv[0]) == hooksconf.NotifyScriptName:
		return codexNotifyScriptCheck(o, path, n.Argv[0], fix)
	}
	for i := 0; i+1 < len(n.Argv); i++ {
		if n.Argv[i] != "hook" || n.Argv[i+1] != "codex-notify" {
			continue
		}
		explicit := ""
		if i+3 < len(n.Argv) && n.Argv[i+2] == "--socket" {
			explicit = n.Argv[i+3]
		}
		if c, bad := notifySocketCheck(o, path, "notify ", explicit, fix+" 重写 notify"); bad {
			return c
		}
		return Check{ID: codexNotifyID, Name: codexNotifyName, Status: OK, Detail: path}
	}
	return notifyWarn(path+" 的 notify 不调 railse（"+hooksconf.ShellJoin(n.Argv)+"），Codex 的完成与提问事件不会进收件箱", hint)
}

const codexNotifyID, codexNotifyName = "hooks.codex_notify", "Codex notify"

// notifyWarn 是 hooks.codex_notify 的一条 warn。
func notifyWarn(detail, hint string) Check {
	return Check{ID: codexNotifyID, Name: codexNotifyName, Status: Warn, Detail: detail, Hint: hint}
}

// codexNotifyScriptCheck 看 config.toml（path）的 notify 指向的串接脚本 script：在、可执行、里面有 railse 的 hook codex-notify
// 那一行才 ok；那一行连的 socket 对不上 --dir 的守护进程时 warn。fix 是重新生成脚本的命令。没有 railse 那一行的、不在数据目录里
// 也没有标记行的，是用户自己的 codex-notify.sh：setup codex-notify 会把它保留为前一跳，提示照实说，不说“重新生成”。
func codexNotifyScriptCheck(o Options, path, script, fix string) Check {
	hint := "运行 " + fix + " 重新生成"
	st, err := os.Stat(script)
	switch {
	case err != nil:
		return notifyWarn(path+" 的 notify 指向 "+script+"，它不存在或读不到", hint)
	case !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0:
		return notifyWarn(path+" 的 notify 指向 "+script+"，它不可执行", hint)
	}
	b, err := os.ReadFile(script)
	if err != nil {
		return notifyWarn(err.Error(), "确认用跑 Codex 的同一个用户运行 doctor")
	}
	c, ok := hooksconf.NotifyScriptHook(b)
	if !ok {
		if _, marked, _ := hooksconf.ParseNotifyScript(b); !marked && !samePath(script, filepath.Join(o.AgentDir, hooksconf.NotifyScriptName)) {
			hint = "运行 " + fix + "：会把这个脚本保留为前一跳，再串上 railse"
		}
		return notifyWarn(script+" 里没有 railse 的 hook codex-notify 那一行，Codex 的完成与提问事件不会进收件箱", hint)
	}
	if c, bad := notifySocketCheck(o, script, "notify 脚本里的 railse ", c.Socket, fix+" 重新生成脚本"); bad {
		return c
	}
	return Check{ID: codexNotifyID, Name: codexNotifyName, Status: OK, Detail: path + "（notify 经 " + script + "）"}
}

// notifySocketCheck 是 notify 里 railse 连的 socket（explicit 是 --socket 给的，空表示没带）对不上 --dir 的守护进程时的 warn；
// 对得上或推不出基准时 bad 为假。where 是细节开头的文件，what 说是谁连的，fix 是重写的命令。
func notifySocketCheck(o Options, where, what, explicit, fix string) (c Check, bad bool) {
	s, ok := socketMismatch(o, explicit)
	if ok {
		return Check{}, false
	}
	var dirs []string
	if explicit != "" {
		dirs = []string{filepath.Dir(explicit)}
	}
	return notifyWarn(where+"："+socketDetail(what, []string{s}, o), socketHint("运行 "+fix, dirs, o)), true
}
