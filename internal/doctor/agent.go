package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/xufei547/railse-opc/internal/autostart"
	"github.com/xufei547/railse-opc/internal/hooksconf"
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
// missingHint 是不存在时的修复提示。
func permCheck(id, name, path string, want os.FileMode, dir bool, missingHint string) Check {
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return Check{ID: id, Name: name, Status: Fail, Detail: path + " 不存在", Hint: missingHint}
	}
	if err != nil {
		return Check{ID: id, Name: name, Status: Fail, Detail: err.Error(), Hint: "确认用跑 railse 的同一个用户运行 doctor"}
	}
	if dir && !st.IsDir() {
		return Check{ID: id, Name: name, Status: Fail, Detail: path + " 不是目录", Hint: "移走这个文件，" + missingHint}
	}
	if !dir && !st.Mode().IsRegular() {
		return Check{ID: id, Name: name, Status: Fail, Detail: path + " 不是普通文件", Hint: "移走它，" + missingHint}
	}
	if st.Mode().Perm() != want {
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("%s 权限是 %04o，应为 %04o", path, st.Mode().Perm(), want), Hint: fmt.Sprintf("chmod %o %s", want, hooksconf.ShellQuote(path))}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: path}
}

func agentChecks(o Options) []Check {
	var out []Check
	dirCheck := permCheck("agent.dir", "守护进程数据目录", o.AgentDir, 0o700, true, "先运行一次 railse agent run，它会建好目录与文件")
	out = append(out, dirCheck)
	out = append(out, agentFilesCheck(o, dirCheck))
	out = append(out, agentSocketCheck(o))
	out = append(out, agentRunningChecks(o)...)
	out = append(out, autostartCheck("agent.autostart", "守护进程开机自启", o, "agent"))
	return out
}

// agentFilesCheck 看数据目录里存在的私密文件都是 0600（不存在的不算错：audit.jsonl 等要用到才建）。
func agentFilesCheck(o Options, dirCheck Check) Check {
	const id, name = "agent.files", "配置与审计文件权限"
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
		return Check{ID: id, Name: name, Status: Fail, Detail: "不是 0600：" + strings.Join(bad, "、"), Hint: "chmod 600 " + quoteAll(badPaths)}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: strings.Join(files, "、") + "（存在的）都是 0600"}
}

func agentSocketCheck(o Options) Check {
	const id, name = "agent.socket", "socket 文件"
	sock := filepath.Join(o.AgentDir, "agent.sock")
	st, err := os.Stat(sock)
	switch {
	case err != nil:
		return Check{ID: id, Name: name, Status: Fail, Detail: sock + " 不存在（守护进程没在跑）", Hint: "railse agent run（前台）或 railse setup autostart（开机自启）"}
	case st.Mode()&os.ModeSocket == 0:
		return Check{ID: id, Name: name, Status: Fail, Detail: sock + " 不是 socket", Hint: "移走这个文件后重启守护进程"}
	case st.Mode().Perm() != 0o600:
		return Check{ID: id, Name: name, Status: Fail, Detail: fmt.Sprintf("%s 权限是 %04o，应为 0600", sock, st.Mode().Perm()), Hint: "chmod 600 " + hooksconf.ShellQuote(sock) + "，或重启守护进程"}
	}
	return Check{ID: id, Name: name, Status: OK, Detail: sock}
}

// agentRunningChecks 问守护进程要状态：在跑没有（agent.running）、连 hub 的情况（agent.hub）。
func agentRunningChecks(o Options) []Check {
	st, err := o.AgentStatus(filepath.Join(o.AgentDir, "agent.sock"))
	if err != nil {
		return []Check{
			{ID: "agent.running", Name: "守护进程在跑", Status: Fail, Detail: err.Error(), Hint: "railse agent run（前台）或 railse setup autostart（开机自启）"},
			{ID: "agent.hub", Name: "hub 连接", Status: Skip, Detail: "守护进程没在跑"},
		}
	}
	paused := "未暂停"
	if st.Paused {
		paused = "已暂停"
	}
	hub := st.Hub
	if hub == "" {
		hub = "未知"
	}
	running := Check{ID: "agent.running", Name: "守护进程在跑", Status: OK, Detail: fmt.Sprintf("规则 %d 条，待决 %d，%s，hub %s", st.Rules, st.Pending, paused, hub)}
	const id, name = "agent.hub", "hub 连接"
	var hc Check
	switch st.Hub {
	case "connected":
		hc = Check{ID: id, Name: name, Status: OK, Detail: "已连上 " + st.HubURL}
	case "connecting":
		hc = Check{ID: id, Name: name, Status: Warn, Detail: "正在重连 " + st.HubURL, Hint: "看 agent.log 里 hub 连接断开的原因；网络不通或 hub 没起来时都会这样"}
	case "disabled":
		hc = Check{ID: id, Name: name, Status: Fail, Detail: st.HubURL + " 连续以 401 拒绝本机密钥（机器被吊销或密钥无效），守护进程已停止重连", Hint: "railse agent enroll --hub … --token … --force 重新接入后重启守护进程"}
	case "none":
		hc = Check{ID: id, Name: name, Status: Warn, Detail: "未接入 hub，引擎定不了的请求会交回终端", Hint: "要在手机上审批就接入：railse agent enroll --hub https://… --token …"}
		// 守护进程只在启动时读 machine.json：文件已在却报 none，是接入之后还没重启它
		if _, err := os.Lstat(filepath.Join(o.AgentDir, "machine.json")); err == nil {
			hc.Detail = "守护进程没接 hub，但 machine.json 已在：接入后还没重启守护进程（它只在启动时读 machine.json）"
			hc.Hint = "接入后还没重启守护进程：托管的用 railse setup autostart" + dirFlag(o) + " 重装，前台跑的停掉后重新 railse agent run" + dirFlag(o)
		}
	default:
		// 升级了程序却没重启守护进程时，旧进程不报 hub 状态
		hc = Check{ID: id, Name: name, Status: Warn, Detail: fmt.Sprintf("守护进程没有报告 hub 状态（%q），可能是升级前启动的旧进程", st.Hub), Hint: "重启守护进程：托管的重跑一次 railse setup autostart，前台跑的停掉后重新 railse agent run"}
	}
	return []Check{running, hc}
}

// autostartCheck 只看托管文件在不在，不跑 launchctl/systemctl。路径与 railse setup autostart 共用 internal/autostart。
// hub 在 Linux 上也可能装成系统单元（以 root 跑 railse setup autostart --hub --system），那个存在也算。
// 取不到家目录时只看系统单元，没有就 skip：不拼相对路径。
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
		return Check{ID: id, Name: name, Status: Skip, Detail: "不支持 " + o.GOOS + " 的开机自启"}
	}
	if o.Home == "" {
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return Check{ID: id, Name: name, Status: OK, Detail: p}
			}
		}
		return Check{ID: id, Name: name, Status: Skip, Detail: noHome}
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
	return Check{ID: id, Name: name, Status: Warn, Detail: "没有托管文件 " + strings.Join(paths, " 或 "), Hint: "railse setup autostart" + flag + "：开机自启、退出后自动拉起"}
}
