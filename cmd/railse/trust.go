package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
	"github.com/xufei547/railse-opc/internal/i18n"
	"github.com/xufei547/railse-opc/internal/proto"
)

const trustUsage = `用法:
  railse trust [时长] [--session 会话id] [--note 文字] [--dir 目录]   放行一段时间：时长如 30m、2h、8h，默认 2h，截到 5m～24h
  railse trust list [--dir 目录]                                    列出生效中的放行
  railse trust revoke <id>|--all [--dir 目录]                        撤销一条或全部

不带 --session 放行当前目录所在的项目（git 仓库根，或家目录下的普通目录），这台机器上各个 CLI 在里面的请求都算；
--session 放行一个 Claude Code 会话（钩子输入里的 session_id，只认 Claude）。只放行一个窗口更省事：RAILSE_TRUST=2h claude。
放行期间范围内引擎定不了的请求一律本地放行，不弹终端框、不进手机；你手写的 deny 规则与总开关暂停不受影响。`

// trustFlags 是各 trust 子命令认的标志；set 是不带 list、revoke 的建放行。
var trustFlags = map[string][]string{
	"set":    {"dir", "session", "note"},
	"list":   {"dir"},
	"revoke": {"dir", "all"},
}

// runTrust 处理 railse trust：经本机 socket 发 trust_set、trust_list、trust_revoke 给守护进程。
// 位置参数与选项可以交错（railse trust 30m --session x 与 railse trust --session x 30m 一样）。
func runTrust(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(stdout, i18n.T(trustUsage))
		return 0
	}
	fs := flag.NewFlagSet("trust", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, i18n.T(trustUsage)) }
	dir := fs.String("dir", "", i18n.T("配置目录，默认 $RAILSE_HOME 或 ~/.railse"))
	session := fs.String("session", "", i18n.T("要放行的 Claude Code 会话 id（钩子输入里的 session_id）"))
	note := fs.String("note", "", i18n.T("备注，list 与手机上能看到"))
	all := fs.Bool("all", false, i18n.T("撤销全部放行"))
	var pos []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return 0
			}
			return 2
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		rest = fs.Args()[1:]
	}
	sub := "set"
	if len(pos) > 0 && (pos[0] == "list" || pos[0] == "revoke") {
		sub, pos = pos[0], pos[1:]
	}
	if bad := foreignFlags(fs, trustFlags[sub]); bad != "" {
		if sub == "set" {
			fmt.Fprintf(stderr, i18n.T("railse trust 不认 %s\n%s\n"), bad, i18n.T(trustUsage))
		} else {
			fmt.Fprintf(stderr, i18n.T("railse trust %s 不认 %s\n%s\n"), sub, bad, i18n.T(trustUsage))
		}
		return 2
	}
	var revokeID string
	switch {
	case sub == "revoke" && *all && len(pos) == 0:
		revokeID = "all"
	case sub == "revoke" && !*all && len(pos) == 1:
		revokeID = pos[0]
	case sub == "revoke":
		fmt.Fprintf(stderr, i18n.T("要撤哪条：给一个 id，或用 --all 撤全部\n%s\n"), i18n.T(trustUsage))
		return 2
	case sub == "list" && len(pos) > 0, sub == "set" && len(pos) > 1:
		extra := pos
		if sub == "set" {
			extra = pos[1:]
		}
		fmt.Fprintf(stderr, i18n.T("多余的参数 %q\n%s\n"), extra, i18n.T(trustUsage))
		return 2
	}
	cfg, err := agentConfigFor(*dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client := hookclient.Client{SocketPath: cfg.SocketPath, ConnectTimeout: 2 * time.Second}
	switch sub {
	case "list":
		return trustList(client, stdout, stderr)
	case "revoke":
		reply, err := client.ControlMsg(proto.Control{V: proto.Version, Kind: "control", Op: "trust_revoke", TrustID: revokeID})
		if code := controlFailed(reply, err, stderr); code != 0 {
			return code
		}
		fmt.Fprintln(stdout, i18n.T("已撤销"))
		return 0
	}
	dur := ""
	if len(pos) == 1 {
		dur = pos[0]
	}
	return trustSet(client, dur, *session, *note, stdout, stderr)
}

// trustSet 建一条放行：给了 session 就是 Claude 会话范围，否则按当前目录定项目根（与守护进程同一个 ResolveProjectRoot），
// 定不出项目根（家目录、系统目录、点开头的目录等）就报错，不放行。
func trustSet(client hookclient.Client, dur, session, note string, stdout, stderr io.Writer) int {
	d, err := agent.ParseTrustFor(dur)
	if err != nil {
		fmt.Fprintln(stderr, i18n.Lookup(err.Error()))
		return 2
	}
	tr := proto.Trust{Note: note, CreatedBy: "cli"}
	if session != "" {
		tr.Scope, tr.Tool, tr.CLISession = proto.TrustScopeSession, "claude", session
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		root, kind := agent.ResolveProjectRoot(cwd)
		if kind != agent.RootGit && kind != agent.RootCwd {
			fmt.Fprintf(stderr, i18n.T("%s 不算项目（家目录及其上级、系统目录、点开头的目录都不算）：到项目目录里跑，或用 --session 放行一个会话\n"), agent.CleanCwd(cwd))
			return 1
		}
		tr.Scope, tr.Project = proto.TrustScopeProject, root
	}
	tr.Until = time.Now().Add(d)
	reply, err := client.ControlMsg(proto.Control{V: proto.Version, Kind: "control", Op: "trust_set", Trust: &tr})
	// 落盘失败时守护进程回 OK:false 但已生效，回复里带着建好的那条：照样告诉用户建了什么，再报错
	if err == nil && len(reply.Trusts) == 1 {
		got := reply.Trusts[0]
		now := time.Now()
		if got.Scope == proto.TrustScopeSession {
			fmt.Fprintf(stdout, i18n.T("已放行 Claude 会话 %s，到 %s（%s）  id %s\n"), printable(got.CLISession), untilText(got.Until, now), trustSpan(got.Until.Sub(now)), got.ID)
		} else {
			fmt.Fprintf(stdout, i18n.T("已放行 项目 %s（%s），到 %s（%s）  id %s\n"), filepath.Base(got.Project), got.Project, untilText(got.Until, now), trustSpan(got.Until.Sub(now)), got.ID)
		}
	}
	return controlFailed(reply, err, stderr)
}

// trustList 每条一行：id、范围、剩余时长、谁建的、备注；按到期先后。
func trustList(client hookclient.Client, stdout, stderr io.Writer) int {
	reply, err := client.ControlMsg(proto.Control{V: proto.Version, Kind: "control", Op: "trust_list"})
	if code := controlFailed(reply, err, stderr); code != 0 {
		return code
	}
	if len(reply.Trusts) == 0 {
		fmt.Fprintln(stdout, i18n.T("没有生效中的放行"))
		return 0
	}
	now := time.Now()
	for _, tr := range reply.Trusts {
		cols := []string{tr.ID, trustLabel(tr), fmt.Sprintf(i18n.T("剩 %s"), trustSpan(tr.Until.Sub(now))), printable(tr.CreatedBy)}
		if tr.Note != "" {
			cols = append(cols, printable(tr.Note))
		}
		fmt.Fprintln(stdout, strings.Join(cols, "  "))
	}
	return 0
}

// controlFailed 报连不上守护进程或管理命令失败，返回退出码（没出错为 0）。守护进程回的错误是固定的中文句子，按终端语言翻；
// 升级后还没重启的老守护进程不认 trust_* 命令，说清楚要重启。
func controlFailed(reply proto.ControlReply, err error, stderr io.Writer) int {
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if reply.OK {
		return 0
	}
	msg := reply.Error
	switch {
	case strings.HasPrefix(msg, "已放行，但未能持久化: "):
		msg = i18n.T("已放行，但未能持久化: ") + strings.TrimPrefix(msg, "已放行，但未能持久化: ")
	case strings.HasPrefix(msg, "未知的管理命令 "):
		msg = i18n.T("守护进程不认这条命令，可能是升级前启动的旧进程：重启它")
	default:
		msg = i18n.Lookup(msg)
	}
	fmt.Fprintln(stderr, i18n.T("失败:"), msg)
	return 1
}

// trustLabel 是一条放行的范围：会话 <tool> <会话 id>，或项目 <项目目录名>。
func trustLabel(tr proto.Trust) string {
	if tr.Scope == proto.TrustScopeSession {
		return fmt.Sprintf(i18n.T("会话 %s %s"), printable(tr.Tool), printable(tr.CLISession))
	}
	return fmt.Sprintf(i18n.T("项目 %s"), printable(filepath.Base(tr.Project)))
}

// trustSpan 把时长写成“1 小时 5 分”或“12 分钟”（不足一分钟按一分钟），与守护进程审计里的 formatRemain 同一口径。
func trustSpan(d time.Duration) string {
	m := int((d + time.Minute - 1) / time.Minute)
	if m < 1 {
		m = 1
	}
	switch {
	case m < 60:
		return fmt.Sprintf(i18n.T("%d 分钟"), m)
	case m%60 == 0:
		return fmt.Sprintf(i18n.T("%d 小时"), m/60)
	}
	return fmt.Sprintf(i18n.T("%d 小时 %d 分"), m/60, m%60)
}

// untilText 是到期的本地时间：今天的只写 15:04，跨天的带上月日。
func untilText(until, now time.Time) string {
	u, n := until.Local(), now.Local()
	if u.YearDay() == n.YearDay() && u.Year() == n.Year() {
		return u.Format("15:04")
	}
	return u.Format("01-02 15:04")
}

// printable 去掉控制字符：备注与会话 id 可能来自手机或钩子输入，原样打到终端会被当成转义序列。
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
