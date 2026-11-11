package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/autostart"
	"github.com/xufei547/railse-opc/internal/hooksconf"
	"github.com/xufei547/railse-opc/internal/i18n"
)

// quoteAll 把路径逐个按 shell 引起来、以空格连接：提示里的命令遇到带空格的路径也能直接复制运行。
func quoteAll(paths []string) string {
	q := make([]string, len(paths))
	for i, p := range paths {
		q[i] = hooksconf.ShellQuote(p)
	}
	return strings.Join(q, " ")
}

// permCheck 看 path 存在、类型对（dir 为真时要是目录，否则要是普通文件）、权限恰为 want。跟随符号链接，看的是真正的文件。
// name 与 missingHint（不存在时的修复提示）由调用方过 i18n.T。
func permCheck(id, name, path string, want os.FileMode, dir bool, missingHint string) Check {
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf(i18n.T("%s 不存在"), path), Hint: missingHint}
	}
	if err != nil {
		return Check{ID: id, Name: name, Status: Fail, Detail: err.Error(), Hint: i18n.T("确认用跑 railse 的同一个用户运行 doctor")}
	}
	if dir && !st.IsDir() {
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf(i18n.T("%s 不是目录"), path), Hint: fmt.Sprintf(i18n.T("移走这个文件，%s"), missingHint)}
	}
	if !dir && !st.Mode().IsRegular() {
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf(i18n.T("%s 不是普通文件"), path), Hint: fmt.Sprintf(i18n.T("移走它，%s"), missingHint)}
	}
	if st.Mode().Perm() != want {
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf(i18n.T("%s 权限是 %04o，应为 %04o"), path, st.Mode().Perm(), want), Hint: fmt.Sprintf("chmod %o %s", want, hooksconf.ShellQuote(path))}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: path}
}

func agentChecks(o Options) []Check {
	var out []Check
	dirCheck := permCheck("agent.dir", i18n.T("守护进程数据目录"), o.AgentDir, 0o700, true, i18n.T("先运行一次 railse agent run，它会建好目录与文件"))
	out = append(out, dirCheck)
	out = append(out, agentFilesCheck(o, dirCheck))
	if c, ok := rolesCheck(o); ok {
		out = append(out, c)
	}
	out = append(out, agentSocketCheck(o))
	out = append(out, agentRunningChecks(o)...)
	out = append(out, autostartCheck("agent.autostart", i18n.T("守护进程开机自启"), o, "agent"))
	return out
}

// rolesCheck 看角色文件 roles.yaml（0.3 起）：不存在时不出这一项（没有角色，体检结果与 0.2.3 一样）；合法 ok；有误 fail——
// 守护进程这时把只读之外的审批一律交人，修好就恢复，不用重启。
func rolesCheck(o Options) (Check, bool) {
	const id = "agent.roles"
	name := i18n.T("角色文件")
	path := filepath.Join(o.AgentDir, "roles.yaml")
	st := agent.LoadRolesFile(path)
	switch {
	case !st.Exists:
		return Check{}, false
	case st.Err != nil:
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf(i18n.T("%s 有误: %v"), path, st.Err),
			Hint: i18n.T("只读之外的审批现在一律问人；改好文件即可恢复（不用重启），railse role check 可以先查")}, true
	}
	return Check{ID: id, Name: name, Status: OK, Detail: fmt.Sprintf(i18n.T("%s（%d 个角色）"), path, len(st.Set.Roles))}, true
}

// projectRootCheck 看 Project 会被守护进程当成哪个项目根（同一个 agent.ResolveProjectRoot）：git 仓库 ok；
// 不是 git 仓库、按目录本身当项目根时：显式给的（ProjectExplicit）warn，建议 git init；隐式默认的当前目录只告知（skip）：
// 用户多半只是在某个目录里跑 railse setup 或 doctor，那里不是要用 agent 的项目。不是 git 仓库、也不是家目录下的普通目录
// （agent.RootNotPlain）时没有项目根：显式给的 warn，提示 git init 或换到项目目录；隐式的同样只告知。在系统目录里
// （agent.RootSystem，git 仓库也一样）时没有项目根，同一口径：显式给的 warn，提示换到系统目录之外的项目目录（git init 没用）；
// 隐式的只告知。不当项目根（家目录、它的上级、根目录、不存在）时 skip。
// 相对的 Project 按 doctor 的工作目录取绝对路径。家目录与守护进程一样按本进程的 $HOME（agent 包的 userHome）认，
// 不用 Options.Home：两者在 railse doctor 里都来自 os.UserHomeDir，测试要用 t.Setenv("HOME") 改。
func projectRootCheck(o Options) Check {
	const id = "project.root"
	name := i18n.T("项目根")
	if o.Project == "" {
		return Check{ID: id, Name: name, Status: Skip, Detail: i18n.T("没给项目目录")}
	}
	proj, err := filepath.Abs(o.Project)
	if err != nil {
		return Check{ID: id, Name: name, Status: Skip, Detail: fmt.Sprintf(i18n.T("取不到 %s 的绝对路径: %v"), o.Project, err)}
	}
	root, kind := agent.ResolveProjectRoot(proj)
	switch {
	case kind == agent.RootGit:
		return Check{ID: id, Name: name, Status: OK, Detail: root}
	case kind == agent.RootCwd && !o.ProjectExplicit:
		return Check{ID: id, Name: name, Status: Skip, Detail: fmt.Sprintf(i18n.T("目录 %s 不是 git 仓库，在这里起 agent 会按当前目录当项目根"), root)}
	case kind == agent.RootCwd:
		return Check{ID: id, Name: name, Status: Warn, Detail: fmt.Sprintf(i18n.T("目录 %s 不是 git 仓库，按当前目录当项目根"), root),
			Hint: fmt.Sprintf(i18n.T("建议在 %s 里 git init（项目根决定哪些写入算项目内）"), root)}
	case kind == agent.RootNotPlain:
		c := Check{ID: id, Name: name, Status: Skip, Detail: fmt.Sprintf(i18n.T("目录 %s 不在家目录下的普通目录里，不按项目根处理"), agent.CleanCwd(proj))}
		if o.ProjectExplicit {
			c.Status, c.Hint = Warn, i18n.T("要自动放行请 git init 或换到项目目录")
		}
		return c
	case kind == agent.RootSystem:
		c := Check{ID: id, Name: name, Status: Skip, Detail: fmt.Sprintf(i18n.T("目录 %s 在系统目录里，不按项目根处理（是 git 仓库也一样）"), agent.CleanCwd(proj))}
		if o.ProjectExplicit {
			c.Status, c.Hint = Warn, i18n.T("要自动放行请换到系统目录之外的项目目录")
		}
		return c
	default:
		detail := i18n.T("%s 是家目录、它的上级或根目录，不当项目根：在这里跑 agent 时所有写入都算项目外")
		if st, err := os.Stat(proj); err != nil || !st.IsDir() {
			detail = i18n.T("%s 不是存在的目录，不当项目根：在这里跑 agent 时所有写入都算项目外")
		}
		return Check{ID: id, Name: name, Status: Skip, Detail: fmt.Sprintf(detail, proj)}
	}
}

// agentFilesCheck 看数据目录里存在的私密文件都是 0600（不存在的不算错：audit.jsonl 等要用到才建）。
func agentFilesCheck(o Options, dirCheck Check) Check {
	const id = "agent.files"
	name := i18n.T("配置与审计文件权限")
	if st, err := os.Stat(o.AgentDir); err != nil || !st.IsDir() {
		return Check{ID: id, Name: name, Status: Skip, Detail: dirCheck.Detail}
	}
	files := []string{"rules.yaml", "machine.json", "audit.jsonl", "state.json"}
	var bad, badPaths []string
	for _, f := range files {
		p := filepath.Join(o.AgentDir, f)
		st, err := os.Stat(p)
		if err != nil {
			continue
		}
		if st.Mode().Perm() != 0o600 {
			bad = append(bad, fmt.Sprintf("%s(%04o)", f, st.Mode().Perm()))
			badPaths = append(badPaths, p)
		}
	}
	if len(bad) > 0 {
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf(i18n.T("不是 0600：%s"), strings.Join(bad, i18n.T("、"))), Hint: "chmod 600 " + quoteAll(badPaths)}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: fmt.Sprintf(i18n.T("%s（存在的）都是 0600"), strings.Join(files, i18n.T("、")))}
}

func agentSocketCheck(o Options) Check {
	const id = "agent.socket"
	name := i18n.T("socket 文件")
	sock := filepath.Join(o.AgentDir, "agent.sock")
	st, err := os.Stat(sock)
	switch {
	case err != nil:
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf(i18n.T("%s 不存在（守护进程没在跑）"), sock), Hint: i18n.T("railse agent run（前台）或 railse setup autostart（开机自启）")}
	case st.Mode()&os.ModeSocket == 0:
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf(i18n.T("%s 不是 socket"), sock), Hint: i18n.T("移走这个文件后重启守护进程")}
	case st.Mode().Perm() != 0o600:
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf(i18n.T("%s 权限是 %04o，应为 0600"), sock, st.Mode().Perm()), Hint: fmt.Sprintf(i18n.T("chmod 600 %s，或重启守护进程"), hooksconf.ShellQuote(sock))}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: sock}
}

// agentRunningChecks 问守护进程要状态：在跑没有（agent.running）、连 hub 的情况（agent.hub）、生效中的限时放行（agent.trusts）。
func agentRunningChecks(o Options) []Check {
	st, err := o.AgentStatus(filepath.Join(o.AgentDir, "agent.sock"))
	if err != nil {
		return []Check{
			{ID: "agent.running", Name: i18n.T("守护进程在跑"), Status: Fail, Detail: err.Error(), Hint: i18n.T("railse agent run（前台）或 railse setup autostart（开机自启）")},
			{ID: "agent.hub", Name: i18n.T("hub 连接"), Status: Skip, Detail: i18n.T("守护进程没在跑")},
			{ID: "agent.trusts", Name: i18n.T("限时放行"), Status: Skip, Detail: i18n.T("守护进程没在跑")},
		}
	}
	paused := i18n.T("未暂停")
	if st.Paused {
		paused = i18n.T("已暂停")
	}
	hub := st.Hub
	if hub == "" {
		hub = i18n.T("未知")
	}
	running := Check{ID: "agent.running", Name: i18n.T("守护进程在跑"), Status: OK, Detail: fmt.Sprintf(i18n.T("规则 %d 条，待决 %d，%s，hub %s"), st.Rules, st.Pending, paused, hub)}
	const id = "agent.hub"
	name := i18n.T("hub 连接")
	var hc Check
	switch st.Hub {
	case "connected":
		hc = Check{ID: id, Name: name, Status: OK, Detail: fmt.Sprintf(i18n.T("已连上 %s"), st.HubURL)}
	case "connecting":
		hc = Check{ID: id, Name: name, Status: Warn, Detail: fmt.Sprintf(i18n.T("正在重连 %s"), st.HubURL), Hint: i18n.T("看 agent.log 里 hub 连接断开的原因；网络不通或 hub 没起来时都会这样")}
	case "disabled":
		hc = Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf(i18n.T("%s 连续以 401 拒绝本机密钥（机器被吊销或密钥无效），守护进程已停止重连"), st.HubURL), Hint: i18n.T("railse agent enroll --hub … --token … --force 重新接入后重启守护进程")}
	case "none":
		hc = Check{ID: id, Name: name, Status: Warn, Detail: i18n.T("未接入 hub，引擎定不了的请求会交回终端"), Hint: i18n.T("要在手机上审批就接入：railse agent enroll --hub https://… --token …")}
		// 守护进程只在启动时读 machine.json：文件已在却报 none，是接入之后还没重启它
		if _, err := os.Lstat(filepath.Join(o.AgentDir, "machine.json")); err == nil {
			hc.Detail = i18n.T("守护进程没接 hub，但 machine.json 已在：接入后还没重启守护进程（它只在启动时读 machine.json）")
			hc.Hint = fmt.Sprintf(i18n.T("接入后还没重启守护进程：托管的用 railse setup autostart%s 重装，前台跑的停掉后重新 railse agent run%s"), dirFlag(o), dirFlag(o))
		}
	default:
		// 升级了程序却没重启守护进程时，旧进程不报 hub 状态
		hc = Check{ID: id, Name: name, Status: Warn, Detail: fmt.Sprintf(i18n.T("守护进程没有报告 hub 状态（%q），可能是升级前启动的旧进程"), st.Hub), Hint: i18n.T("重启守护进程：托管的重跑一次 railse setup autostart，前台跑的停掉后重新 railse agent run")}
	}
	return []Check{running, hc, trustsCheck(st.Trusts)}
}

// trustsCheck 只读地报生效中的限时放行：有就 ok 并报条数（放行是用户自己建的，不算问题），没有 skip。
// 升级前起的旧守护进程不报这个字段，读出来是 0，同样 skip。
func trustsCheck(n int) Check {
	const id = "agent.trusts"
	name := i18n.T("限时放行")
	if n > 0 {
		return Check{ID: id, Name: name, Status: OK, Detail: fmt.Sprintf(i18n.T("%d 条限时放行生效中（railse trust list 查看）"), n)}
	}
	return Check{ID: id, Name: name, Status: Skip, Detail: i18n.T("没有生效中的限时放行")}
}

// autostartCheck 只看托管文件在不在，不跑 launchctl/systemctl。路径与 railse setup autostart 共用 internal/autostart。
// hub 在 Linux 上也可能装成系统单元（以 root 跑 railse setup autostart --hub --system），那个存在也算。
// 取不到家目录时只看系统单元，没有就 skip：不拼相对路径。name 由调用方过 i18n.T。
func autostartCheck(id, name string, o Options, kind string) Check {
	var paths []string
	switch o.GOOS {
	case "darwin":
		if o.Home != "" {
			paths = []string{autostart.PlistPath(o.Home, kind)}
		}
	case "linux":
		if kind == "hub" {
			paths = append(paths, autostart.UnitPath(o.Home, kind, true))
		}
		if o.Home != "" {
			paths = append(paths, autostart.UnitPath(o.Home, kind, false))
		}
	default:
		return Check{ID: id, Name: name, Status: Skip, Detail: fmt.Sprintf(i18n.T("不支持 %s 的开机自启"), o.GOOS)}
	}
	if o.Home == "" {
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return Check{ID: id, Name: name, Status: OK, Detail: p}
			}
		}
		return Check{ID: id, Name: name, Status: Skip, Detail: i18n.T(noHome)}
	}
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return Check{ID: id, Name: name, Status: OK, Detail: p}
		}
	}
	flag := ""
	if kind == "hub" {
		flag = " --hub"
	}
	return Check{ID: id, Name: name, Status: Warn, Detail: fmt.Sprintf(i18n.T("没有托管文件 %s"), strings.Join(paths, i18n.T(" 或 "))), Hint: fmt.Sprintf(i18n.T("railse setup autostart%s：开机自启、退出后自动拉起"), flag)}
}
