package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/autostart"
	"github.com/xufei547/railse-opc/internal/hooksconf"
)

const autostartUsage = `用法:
  railse setup autostart [--hub] [--uninstall] [--status] [--dir 目录] [--bin 路径] [--system] [--user 名字] [--dry-run] [-- hub run 的额外参数]
    开机自启并在退出后自动拉起。默认托管守护进程（agent run）；--hub 托管 hub（hub run），-- 之后的参数原样追加到 hub run 后面，例如：
      railse setup autostart --hub -- --public-url https://hub.example.com
    macOS 写 ~/Library/LaunchAgents/dev.railse.<agent|hub>.plist（launchd 用户代理，日志在数据目录的 launchd.log）；
    Linux 写 ~/.config/systemd/user/railse-<agent|hub>.service（用户单元，日志：journalctl --user -u railse-<agent|hub>）。
    hub 要直出 443/80 时在 Linux 上用系统单元（只给绑定特权端口的权限，hub 不以 root 运行）：
      ` + sudoSystemHubCmd + ` [--user 名字] -- --acme-domain hub.example.com --acme-email me@example.com
      写 /etc/systemd/system/railse-hub.service，以 --user（默认 $SUDO_USER）运行，数据目录默认 /var/lib/railse-hub。
    --uninstall 停止并移除托管（数据目录保留）；--status 看是否在跑；--dry-run 只打印要写的文件与要跑的命令。`

// sudoSystemHubCmd 是提示里装 hub 系统单元的命令开头。写成 command -v 的形式：程序不知道自己装在哪儿，而 sudo 的
// secure_path 不含 ~/.local/bin（RHEL 系连 /usr/local/bin 都不含），直接写 sudo railse 常常找不到命令。
const sudoSystemHubCmd = `sudo "$(command -v railse)" setup autostart --hub --system`

const (
	// systemHubDir 是 hub 系统单元的默认数据目录，由 systemd 按 StateDirectory= 建好并交给运行用户。
	systemHubDir      = "/var/lib/railse-hub"
	systemHubStateDir = "railse-hub"
	// bootstrapRetries 是 launchctl bootstrap 失败后的重试次数：bootout 是异步的，旧作业还没退完时 bootstrap 会报
	// “Bootstrap failed: 5: Input/output error”。
	bootstrapRetries = 5
)

// 以下是变量只为测试能替换：跑系统命令（合并输出）、bootstrap 重试的间隔、查用户、经已打开的 fd 改属主。
var (
	runCommand = func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).CombinedOutput()
		return string(out), err
	}
	bootstrapRetryDelay = 500 * time.Millisecond
	lookupUser          = user.Lookup
	chownDir            = func(f *os.File, uid, gid int) error { return f.Chown(uid, gid) }
)

// autostartSpec 描述要托管的进程。
type autostartSpec struct {
	Kind     string   // agent 或 hub
	Bin      string   // railse 可执行文件的绝对路径
	Dir      string   // 数据目录（绝对路径），写进 RAILSE_HOME 或 RAILSE_HUB_HOME
	Home     string   // 家目录：用户级托管文件写在它下面
	User     string   // 系统单元的 User=
	UID      int      // launchd 的 gui/<uid>
	System   bool     // Linux 系统单元
	Extra    []string // 追加到 run 后面的参数（只有 hub 有）
	StateDir string   // 系统单元的 StateDirectory=（/var/lib 下的名字）；空则数据目录由 setup 建好并交给运行用户
}

func launchdLabel(kind string) string { return autostart.Label(kind) }

func launchdPlistPath(home, kind string) string { return autostart.PlistPath(home, kind) }

func systemdUnitPath(home, kind string, system bool) string {
	return autostart.UnitPath(home, kind, system)
}

func (sp autostartSpec) envName() string {
	if sp.Kind == "hub" {
		return "RAILSE_HUB_HOME"
	}
	return "RAILSE_HOME"
}

func (sp autostartSpec) args() []string {
	return append([]string{sp.Kind, "run"}, sp.Extra...)
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s)) // 写 bytes.Buffer 不会失败
	return b.String()
}

// launchdPlist 生成 launchd 用户代理：登录即起、退出即拉起、5 秒节流，标准输出与错误写进数据目录的 launchd.log。
// PATH 给足常见位置：守护进程本身不需要，但 hub 的 ACME 与守护进程的子进程都可能用到系统工具。
func launchdPlist(sp autostartSpec) []byte {
	var b strings.Builder
	b.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n<plist version=\"1.0\"><dict>\n")
	fmt.Fprintf(&b, "  <key>Label</key><string>%s</string>\n", launchdLabel(sp.Kind))
	b.WriteString("  <key>ProgramArguments</key><array>")
	fmt.Fprintf(&b, "<string>%s</string>", xmlEscape(sp.Bin))
	for _, a := range sp.args() {
		fmt.Fprintf(&b, "<string>%s</string>", xmlEscape(a))
	}
	b.WriteString("</array>\n")
	fmt.Fprintf(&b, "  <key>EnvironmentVariables</key><dict><key>%s</key><string>%s</string><key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string></dict>\n", sp.envName(), xmlEscape(sp.Dir))
	b.WriteString("  <key>RunAtLoad</key><true/>\n  <key>KeepAlive</key><true/>\n  <key>ThrottleInterval</key><integer>5</integer>\n")
	logPath := xmlEscape(filepath.Join(sp.Dir, "launchd.log"))
	fmt.Fprintf(&b, "  <key>StandardOutPath</key><string>%s</string>\n  <key>StandardErrorPath</key><string>%s</string>\n", logPath, logPath)
	fmt.Fprintf(&b, "  <key>WorkingDirectory</key><string>%s</string>\n", xmlEscape(sp.Home))
	b.WriteString("</dict></plist>\n")
	return []byte(b.String())
}

// systemdQuote 把一个值写成 systemd 单元里的一个词：% 写成 %%（不让 systemd 当说明符展开）；
// 含空白、引号、反斜杠、为空或是单独的 ;（ExecStart 里的命令分隔符）时整个用双引号包起来，里面的 \ 与 " 转义。
// 控制字符在解析参数时已拒绝。
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if s != "" && s != ";" && !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// systemdExecArg 是 ExecStart 里程序名之后的一个参数：在 systemdQuote 之外把 $ 写成 $$（不让 systemd 当环境变量替换）。
func systemdExecArg(s string) string { return strings.ReplaceAll(systemdQuote(s), "$", "$$") }

// systemdUnit 生成用户单元或系统单元。Type=exec（systemd 240 起）让可执行文件起不来时 start 直接报错；
// 崩溃后 2 秒拉起，不设启动次数上限（与 launchd 的 KeepAlive 一致）。
// 系统单元只给 hub 用，按最小权限：指定的普通用户、只多一项绑定特权端口的能力、整个文件系统只读、只有数据目录可写、私有 /tmp。
func systemdUnit(sp autostartSpec) []byte {
	var b strings.Builder
	title := "Railse agent（审批守护进程）"
	if sp.Kind == "hub" {
		title = "Railse hub（审批中心）"
	}
	fmt.Fprintf(&b, "[Unit]\nDescription=%s\n", title)
	if sp.System {
		// 用户实例里没有 network-online.target，只有系统单元写
		b.WriteString("After=network-online.target\nWants=network-online.target\n")
	}
	b.WriteString("StartLimitIntervalSec=0\n\n[Service]\nType=exec\n")
	parts := []string{systemdQuote(sp.Bin)}
	for _, a := range sp.args() {
		parts = append(parts, systemdExecArg(a))
	}
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(parts, " "))
	fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(sp.envName()+"="+sp.Dir))
	b.WriteString("Restart=always\nRestartSec=2\n")
	if !sp.System {
		b.WriteString("\n[Install]\nWantedBy=default.target\n")
		return []byte(b.String())
	}
	fmt.Fprintf(&b, "User=%s\n", sp.User)
	if sp.StateDir != "" {
		fmt.Fprintf(&b, "StateDirectory=%s\nStateDirectoryMode=0700\n", sp.StateDir)
	}
	b.WriteString("AmbientCapabilities=CAP_NET_BIND_SERVICE\nNoNewPrivileges=true\nProtectSystem=strict\nProtectHome=read-only\nPrivateTmp=true\n")
	fmt.Fprintf(&b, "ReadWritePaths=%s\n", systemdQuote(sp.Dir))
	b.WriteString("\n[Install]\nWantedBy=multi-user.target\n")
	return []byte(b.String())
}

// runAutostart 是 railse setup autostart：按当前系统与用户分发。
func runAutostart(args []string, stdout, stderr io.Writer) int {
	return runAutostartOS(runtime.GOOS, os.Getuid(), args, stdout, stderr)
}

// runAutostartOS 是 runAutostart 的可测版本：系统与 uid 由调用方给。
func runAutostartOS(goos string, uid int, args []string, stdout, stderr io.Writer) int {
	// 在第一个 -- 处切开：之后的原样交给 hub run，之前的才是本命令的参数。没有 -- 的位置参数一律是用法错，
	// 免得 railse setup autostart foo 把 foo 写进托管文件、launchd 或 systemd 再反复拉起反复失败。
	var extra []string
	for i, a := range args {
		if a == "--" {
			args, extra = args[:i], args[i+1:]
			break
		}
	}
	fs := flag.NewFlagSet("setup autostart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, autostartUsage)
		fs.PrintDefaults()
	}
	hubMode := fs.Bool("hub", false, "托管 hub 而不是守护进程")
	uninstall := fs.Bool("uninstall", false, "停止并移除托管（数据目录保留）")
	status := fs.Bool("status", false, "只查看是否在跑")
	dir := fs.String("dir", "", "数据目录，默认 $RAILSE_HOME 或 ~/.railse；--hub 时 $RAILSE_HUB_HOME 或 ~/.railse-hub；--system 时 /var/lib/railse-hub")
	bin := fs.String("bin", "", "railse 可执行文件路径，默认当前程序")
	system := fs.Bool("system", false, "Linux：hub 用系统单元（须 root，hub 以 --user 运行）")
	userName := fs.String("user", "", "系统单元以哪个用户运行，默认 $SUDO_USER；不能是 root")
	dryRun := fs.Bool("dry-run", false, "只打印要写的文件与要跑的命令，不写不跑")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, format+"\n%s\n", append(a, autostartUsage)...)
		return 2
	}
	if fs.NArg() != 0 {
		return usageErr("多余的参数 %q（给 hub run 的参数放在 -- 之后）", fs.Args())
	}
	if goos != "darwin" && goos != "linux" {
		fmt.Fprintln(stderr, "开机自启只支持 macOS（launchd）与 Linux（systemd）")
		return 1
	}
	switch {
	case *status && *uninstall:
		return usageErr("--status 与 --uninstall 只能选一个")
	case len(extra) > 0 && !*hubMode:
		return usageErr("-- 之后的额外参数只给 hub（--hub）；守护进程不收")
	case *system && !*hubMode:
		return usageErr("--system 只给 hub（--hub）用；守护进程用用户级托管：railse setup autostart")
	case *system && goos != "linux":
		return usageErr("--system 只在 Linux 上有效")
	case *userName != "" && !*system:
		return usageErr("--user 只和 --system 一起用")
	}
	for _, a := range extra {
		if a == "--dir" || a == "-dir" || strings.HasPrefix(a, "--dir=") || strings.HasPrefix(a, "-dir=") {
			return usageErr("数据目录用 setup autostart 自己的 --dir 指定，不要放在 -- 之后（托管文件里的环境变量、日志与可写目录都按它算）")
		}
	}
	install := !*status && !*uninstall
	switch {
	case uid == 0 && !*system && goos == "darwin":
		fmt.Fprintln(stderr, "macOS 上不要用 sudo 跑：launchd 用户代理装在当前登录用户下，直接 railse setup autostart")
		return 1
	case uid == 0 && !*system:
		fmt.Fprintln(stderr, "不要以 root 管用户级托管：守护进程与 hub 都以普通用户运行，请用普通用户跑本命令；hub 要直出 443/80 用 "+sudoSystemHubCmd+" --user 名字（只给绑定特权端口的权限）")
		return 1
	case *system && !*status && uid != 0 && !*dryRun:
		fmt.Fprintln(stderr, "--system 要写 /etc/systemd/system，需要 root："+sudoSystemHubCmd+" …")
		return 1
	}

	sp := autostartSpec{Kind: "agent", Bin: *bin, Dir: *dir, UID: uid, System: *system, Extra: extra}
	if *hubMode {
		sp.Kind = "hub"
	}
	home, herr := os.UserHomeDir()
	if herr != nil && !sp.System { // 系统单元用不到家目录（sudo 下也常是 root 的）
		fmt.Fprintln(stderr, herr)
		return 1
	}
	sp.Home = home
	var err error
	if sp.Bin == "" {
		if sp.Bin, err = os.Executable(); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if sp.Dir == "" {
		switch {
		case sp.System:
			sp.Dir, sp.StateDir = systemHubDir, systemHubStateDir
		case sp.Kind == "hub":
			sp.Dir, err = defaultHubDir()
		default:
			var c agent.Config
			c, err = agent.DefaultConfig()
			sp.Dir = c.Dir
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	// launchd 与 systemd 都不认相对路径
	for _, p := range []*string{&sp.Bin, &sp.Dir} {
		if *p, err = filepath.Abs(*p); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	// 默认值（当前程序路径、RAILSE_HOME、RAILSE_HUB_HOME）也要查：换行会在托管文件里拆出新的一行
	for _, s := range append([]string{sp.Bin, sp.Dir}, sp.Extra...) {
		if strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return usageErr("路径与参数里不能有换行等控制字符（包括 RAILSE_HOME、RAILSE_HUB_HOME 给的默认值）: %q", s)
		}
	}
	var runAs runUser
	if sp.System && install {
		if runAs, err = systemUnitUser(*userName); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		sp.User = runAs.Name
	}
	if install && goos == "linux" && sp.Kind == "hub" && !sp.System {
		warnUserUnitLowPorts(sp.Extra, stderr)
	}
	if install {
		if err := checkExecutable(sp.Bin); err != nil {
			if !*dryRun {
				fmt.Fprintln(stderr, err)
				return 1
			}
			fmt.Fprintf(stderr, "警告：%v\n", err)
		}
		if underTempDir(sp.Bin) {
			fmt.Fprintf(stderr, "警告：托管文件会指向临时路径 %s，之后它可能就没了（go run 的产物？）；用 --bin 指定装好的 railse\n", sp.Bin)
		}
	}
	switch {
	case *status:
		return autostartStatus(goos, sp, *dryRun, stdout)
	case *uninstall:
		code := autostartUninstall(goos, sp, *dryRun, stdout, stderr)
		// setup all --local-hub 装的本机 hub，对外地址由 Tailscale Funnel 提供：Funnel 不随托管一起撤，说一声还开着、怎么关。
		// 不替用户关：tailscale funnel --https=443 off 会关掉 443 上的整个 Funnel，那里可能还有别的配置
		if code == 0 && sp.Kind == "hub" {
			if lh, ok, err := loadLocalHub(sp.Dir); err == nil && ok && lh.Provider == "tailscale" {
				fmt.Fprintf(stdout, "注意：Tailscale Funnel 还开着，%s 仍转到 127.0.0.1:%d；不用了就运行 tailscale funnel --https=443 off"+
					"（会关掉 443 上的 Funnel，有别的配置先用 tailscale funnel status 核对）\n", lh.PublicURL, lh.Port)
			}
		}
		return code
	default:
		return autostartInstall(goos, sp, runAs, *dryRun, stdout, stderr)
	}
}

// hubRunFlag 在给 hub run 的额外参数里找 --name 的值（--name 值、--name=值，一个或两个横线都认），有多个时取最后一个
// （同 flag 包）；没有时 ok 为 false。
func hubRunFlag(extra []string, name string) (val string, ok bool) {
	for i := 0; i < len(extra); i++ {
		a := extra[i]
		for _, p := range []string{"--" + name, "-" + name} {
			if a == p && i+1 < len(extra) {
				val, ok = extra[i+1], true
				i++
				break
			}
			if v, found := strings.CutPrefix(a, p+"="); found {
				val, ok = v, true
				break
			}
		}
	}
	return val, ok
}

// lowHubAddrs 是 hub run 带这些额外参数时要监听的、端口在 1024 以下的地址。默认值同 hub run：有 --acme-domain 时
// --listen 默认 :443、--acme-http 默认 :80（none 不监听），没有时 --listen 默认 127.0.0.1:8787。
func lowHubAddrs(extra []string) []string {
	_, acme := hubRunFlag(extra, "acme-domain")
	listen, ok := hubRunFlag(extra, "listen")
	switch {
	case !ok && acme:
		listen = ":443"
	case !ok:
		listen = "127.0.0.1:8787"
	}
	addrs := []string{listen}
	if acme {
		if h, ok := hubRunFlag(extra, "acme-http"); !ok {
			addrs = append(addrs, ":80")
		} else if h != "none" {
			addrs = append(addrs, h)
		}
	}
	var low []string
	for _, a := range addrs {
		if _, port, err := net.SplitHostPort(a); err == nil {
			if n, err := strconv.Atoi(port); err == nil && n > 0 && n < 1024 {
				low = append(low, a)
			}
		}
	}
	return low
}

// warnUserUnitLowPorts 在 Linux 上用用户单元托管要绑 1024 以下端口的 hub 时醒目地警告（照装，不拒绝）：用户单元以普通用户
// 运行、没有 CAP_NET_BIND_SERVICE，hub 起不来，会被 systemd 每 2 秒拉起一次，直出 443/80 要用系统单元。
func warnUserUnitLowPorts(extra []string, stderr io.Writer) {
	low := lowHubAddrs(extra)
	if len(low) == 0 {
		return
	}
	q := make([]string, len(extra))
	for i, a := range extra {
		q[i] = hooksconf.ShellQuote(a)
	}
	fmt.Fprintf(stderr, "\n!!! 警告：Linux 的用户单元以你的身份运行，没有绑定 1024 以下端口的权限，hub 绑不了 %s，会起不来并被 systemd 反复拉起。\n"+
		"    照样装了；要直出 443/80 改用系统单元（只多给 CAP_NET_BIND_SERVICE，hub 不以 root 运行），先卸掉这个用户单元：\n"+
		"      railse setup autostart --hub --uninstall\n      %s -- %s\n"+
		"    用反向代理时改成 --listen 127.0.0.1:8787（不要 --acme-domain），见 docs/setup.md。\n\n", strings.Join(low, "、"), sudoSystemHubCmd, strings.Join(q, " "))
}

// runUser 是系统单元的运行用户。
type runUser struct {
	Name     string
	UID, GID int
}

// systemUnitUser 定系统单元的运行用户：--user，没给就取 $SUDO_USER；必须是本机已有的、uid 不为 0 的用户。
// 全数字的名字也拒绝：systemd 会把 User=1000 当成 uid。uid 按数值比较（passwd 里写成 00、+0 也是 root）。
func systemUnitUser(name string) (runUser, error) {
	if name == "" {
		name = os.Getenv("SUDO_USER")
	}
	if name == "" {
		return runUser{}, errors.New("--system 要指定 hub 的运行用户：--user 名字（默认取 $SUDO_USER，现在没有）")
	}
	if strings.IndexFunc(name, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return runUser{}, fmt.Errorf("用户名 %q 不合法", name)
	}
	if strings.Trim(name, "0123456789") == "" {
		return runUser{}, fmt.Errorf("用户名 %s 全是数字：systemd 会把 User=%s 当成 uid，请用用户名", name, name)
	}
	u, err := lookupUser(name)
	if err != nil {
		return runUser{}, fmt.Errorf("找不到用户 %s: %v", name, err)
	}
	uid, err := strconv.Atoi(u.Uid)
	if err != nil || uid < 0 {
		return runUser{}, fmt.Errorf("用户 %s 的 uid %q 不是合法的数字", name, u.Uid)
	}
	if uid == 0 || name == "root" {
		return runUser{}, fmt.Errorf("hub 不以 root 运行（用户 %s 的 uid 是 0）：用 --user 指定一个普通用户", name)
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil || gid < 0 {
		return runUser{}, fmt.Errorf("用户 %s 的 gid %q 不是合法的数字", name, u.Gid)
	}
	return runUser{Name: name, UID: uid, GID: gid}, nil
}

// checkExecutable 确认 --bin 是存在的、可执行的普通文件：托管文件指向用不了的程序时 launchd 与 systemd 会反复拉起反复失败。
func checkExecutable(p string) error {
	st, err := os.Stat(p)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("railse 可执行文件 %s 不存在：用 --bin 指定装好的 railse", p)
	case err != nil:
		return fmt.Errorf("railse 可执行文件 %s 用不了：%v", p, err)
	case !st.Mode().IsRegular() || st.Mode().Perm()&0o111 == 0:
		return fmt.Errorf("%s 不是可执行文件：用 --bin 指定装好的 railse", p)
	}
	return nil
}

// underTempDir 判断 p 是否在临时目录（os.TempDir()、/tmp）下：go run 的产物就在那里，之后可能被清掉。两边都按原样与解开符号链接后各比一次。
func underTempDir(p string) bool {
	paths := []string{p}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		paths = append(paths, r)
	}
	for _, d := range []string{os.TempDir(), "/tmp"} {
		dirs := []string{filepath.Clean(d)}
		if r, err := filepath.EvalSymlinks(d); err == nil {
			dirs = append(dirs, r)
		}
		for _, dd := range dirs {
			for _, pp := range paths {
				if dd != string(filepath.Separator) && strings.HasPrefix(pp, dd+string(filepath.Separator)) {
					return true
				}
			}
		}
	}
	return false
}

// execCmd 跑一条系统命令并返回合并输出；dry 时只打印，当作成功。
func execCmd(dry bool, stdout io.Writer, name string, args ...string) (string, error) {
	if dry {
		fmt.Fprintf(stdout, "$ %s %s\n", name, strings.Join(args, " "))
		return "", nil
	}
	return runCommand(name, args...)
}

// cmdError 把失败的命令连同它的输出写成一个错误。
func cmdError(name string, args []string, out string, err error) error {
	return fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
}

// runOrPrint 跑一条系统命令；dry 时只打印。失败时把命令的输出带进错误。
func runOrPrint(dry bool, stdout io.Writer, name string, args ...string) error {
	if out, err := execCmd(dry, stdout, name, args...); err != nil {
		return cmdError(name, args, out, err)
	}
	return nil
}

func systemctlArgs(system bool) []string {
	if system {
		return nil
	}
	return []string{"--user"}
}

// statusHint 是查看状态的完整命令。
func statusHint(sp autostartSpec) string {
	s := "railse setup autostart"
	if sp.Kind == "hub" {
		s += " --hub"
	}
	if sp.System {
		s += " --system"
	}
	return s + " --status"
}

// logHint 说日志在哪儿。
func logHint(goos string, sp autostartSpec) string {
	switch {
	case goos == "darwin":
		return filepath.Join(sp.Dir, "launchd.log")
	case sp.System:
		return "journalctl -u " + autostart.UnitName(sp.Kind)
	default:
		return "journalctl --user -u " + autostart.UnitName(sp.Kind)
	}
}

func autostartInstall(goos string, sp autostartSpec, runAs runUser, dry bool, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, err)
		return 1
	}
	// 先建数据目录，出错就什么都不写。launchd 不会替 StandardOutPath（数据目录下的 launchd.log）建父目录，
	// 首次安装时目录可能还没有，作业会起不来；系统单元显式给了 --dir 时由 root 准备好（见 prepareSystemDir），单元里写它的真实路径。
	switch {
	case goos == "darwin" && dry:
		fmt.Fprintf(stdout, "会建数据目录 %s（0700）\n", sp.Dir)
	case goos == "darwin":
		if err := os.MkdirAll(sp.Dir, 0o700); err != nil {
			return fail(err)
		}
		if err := os.Chmod(sp.Dir, 0o700); err != nil {
			return fail(err)
		}
	case sp.System && sp.StateDir == "" && dry:
		fmt.Fprintf(stdout, "会准备数据目录 %s（已有的须已属于 %s；没有的新建为 0700 并交给 %s）\n", sp.Dir, sp.User, sp.User)
	case sp.System && sp.StateDir == "":
		real, err := prepareSystemDir(sp.Dir, runAs)
		if err != nil {
			return fail(err)
		}
		sp.Dir = real
	}
	var path string
	var content []byte
	if goos == "darwin" {
		path, content = launchdPlistPath(sp.Home, sp.Kind), launchdPlist(sp)
	} else {
		path, content = systemdUnitPath(sp.Home, sp.Kind, sp.System), systemdUnit(sp)
	}
	if dry {
		fmt.Fprintf(stdout, "== %s ==\n%s", path, content)
	} else {
		if err := writeFileAtomic(path, content, 0o644); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "已写入 %s\n", path)
	}
	if goos == "darwin" {
		target := fmt.Sprintf("gui/%d", sp.UID)
		job := target + "/" + launchdLabel(sp.Kind)
		_ = runOrPrint(dry, stdout, "launchctl", "bootout", job) // 还没装过时会失败，忽略
		// 之前被 launchctl disable 过的作业 bootstrap 会一直报 “5: Input/output error”；失败忽略
		_ = runOrPrint(dry, stdout, "launchctl", "enable", job)
		if err := launchdBootstrap(dry, stdout, target, path); err != nil {
			return fail(err)
		}
	} else {
		// enable 让它开机（用户实例启动时）自起；restart 让重跑本命令时新的单元立即生效（没在跑时等于 start）
		sc, unit := systemctlArgs(sp.System), autostart.UnitName(sp.Kind)
		for _, cmd := range [][]string{{"daemon-reload"}, {"enable", unit}, {"restart", unit}} {
			if err := runOrPrint(dry, stdout, "systemctl", append(append([]string{}, sc...), cmd...)...); err != nil {
				return fail(err)
			}
		}
	}
	if dry {
		fmt.Fprintln(stdout, "（--dry-run：以上没有写文件，也没有跑命令）")
		return 0
	}
	how := "登录后由 launchd 启动、退出后自动拉起"
	switch {
	case goos == "linux" && sp.System:
		how = "开机由 systemd 启动、以 " + sp.User + " 运行、退出后自动拉起"
	case goos == "linux":
		how = "由 systemd 用户实例启动、退出后自动拉起"
	}
	fmt.Fprintf(stdout, "已托管 %s：%s；日志：%s；查看状态：%s\n", sp.Kind, how, logHint(goos, sp), statusHint(sp))
	if goos == "linux" && !sp.System {
		fmt.Fprintln(stdout, "提示：要在没人登录时也跑（开机即起、退出登录不停），执行一次 loginctl enable-linger $USER")
	}
	return 0
}

// launchdBootstrap 加载作业；失败时隔一会儿重试（见 bootstrapRetries）。
func launchdBootstrap(dry bool, stdout io.Writer, target, path string) error {
	var err error
	for i := 0; i <= bootstrapRetries; i++ {
		if i > 0 {
			time.Sleep(bootstrapRetryDelay)
		}
		if err = runOrPrint(dry, stdout, "launchctl", "bootstrap", target, path); err == nil {
			return nil
		}
	}
	return err
}

// prepareSystemDir 以 root 身份给系统单元准备 --dir 指定的数据目录，返回解开符号链接后的真实路径（单元里写它）。
//   - --dir 本身是符号链接：拒绝。
//   - 已有的目录：必须已经属于运行用户，不替它 chown（免得把 /var/empty、/usr/local/sbin 之类的目录交出去），
//     只经 O_NOFOLLOW 打开的 fd 收紧为 0700。
//   - 没有的目录：已有的那段祖先（解开符号链接后）必须都属于 root 且别人不能写（粘滞位目录除外，同 sshd 的 StrictModes），
//     否则运行用户能在 root 建目录与 chown 之间把路径换掉；缺的父目录建成 0755（显式 Chmod，不受 umask 影响），
//     目录本身 0700，经 O_NOFOLLOW 打开的 fd 核对确是 root 刚建的之后再 chown 给运行用户。
func prepareSystemDir(dir string, ru runUser) (string, error) {
	st, err := os.Lstat(dir)
	switch {
	case err == nil && st.Mode()&os.ModeSymlink != 0:
		return "", fmt.Errorf("%s 是符号链接：--dir 要给真实的目录", dir)
	case err == nil && !st.IsDir():
		return "", fmt.Errorf("%s 不是目录", dir)
	case err == nil:
		return tightenExistingDir(dir, ru)
	case !errors.Is(err, os.ErrNotExist):
		return "", err
	}
	return createSystemDir(dir, ru)
}

func tightenExistingDir(dir string, ru runUser) (string, error) {
	f, err := openDirNoFollow(dir)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if owner, ok := fileOwner(st); !ok || owner != ru.UID {
		return "", fmt.Errorf("%s 已存在但不属于 %s，不替它 chown：换一个以 %s 身份建好的目录（sudo -u %s mkdir -p -m 700 <目录>）用 --dir 指向它，或不给 --dir 用默认的 %s",
			dir, ru.Name, ru.Name, ru.Name, systemHubDir)
	}
	if err := f.Chmod(0o700); err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(dir)
}

func createSystemDir(dir string, ru runUser) (string, error) {
	// 从 dir 往上找到已存在的最深一层祖先，记下缺的几层
	base, missing := filepath.Dir(dir), []string{filepath.Base(dir)}
	for {
		if _, err := os.Lstat(base); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		missing = append([]string{filepath.Base(base)}, missing...)
		base = filepath.Dir(base)
	}
	real, err := filepath.EvalSymlinks(base)
	if err != nil {
		return "", err
	}
	if err := checkTrustedChain(real); err != nil {
		return "", fmt.Errorf("不替你新建 %s：%v（运行用户可能在 root 建目录与 chown 之间把路径换掉）；先以 %s 身份建好再重跑：sudo -u %s mkdir -p -m 700 %s",
			dir, err, ru.Name, ru.Name, hooksconf.ShellQuote(dir))
	}
	cur := real
	for _, name := range missing[:len(missing)-1] {
		cur = filepath.Join(cur, name)
		if err := os.Mkdir(cur, 0o755); err != nil {
			return "", err
		}
		if err := os.Chmod(cur, 0o755); err != nil { // 祖先都可信，这里换不掉
			return "", err
		}
	}
	cur = filepath.Join(cur, missing[len(missing)-1])
	if err := os.Mkdir(cur, 0o700); err != nil {
		return "", err
	}
	f, err := openDirNoFollow(cur)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if owner, ok := fileOwner(st); !ok || owner != os.Geteuid() {
		return "", fmt.Errorf("%s 不是刚建的目录（属主不对），不 chown", cur)
	}
	if err := chownDir(f, ru.UID, ru.GID); err != nil {
		return "", err
	}
	if err := f.Chmod(0o700); err != nil {
		return "", err
	}
	return cur, nil
}

// checkTrustedChain 确认 p（已解开符号链接）及其每一层祖先都是只有 root 能改的目录：属主是 root（以 root 跑时即当前用户；
// 测试里是当前用户），别人不能写；粘滞位的公共目录（如 /tmp）除外，别人删不掉、换不掉不属于自己的条目。
func checkTrustedChain(p string) error {
	for {
		st, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if !st.IsDir() {
			return fmt.Errorf("%s 不是目录", p)
		}
		if owner, ok := fileOwner(st); !ok || (owner != 0 && owner != os.Geteuid()) {
			return fmt.Errorf("%s 不属于 root", p)
		}
		if st.Mode().Perm()&0o022 != 0 && st.Mode()&os.ModeSticky == 0 {
			return fmt.Errorf("%s 别人也能写", p)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
}

// launchdNotLoaded 判断 launchctl bootout 的失败是不是“本来就没加载”：退出码 3（No such process）或 113，或输出这么说。
func launchdNotLoaded(out string, err error) bool {
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) && (ec.ExitCode() == 3 || ec.ExitCode() == 113) {
		return true
	}
	for _, s := range []string{"No such process", "Could not find service", "Could not find specified service"} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

// autostartUninstall 先停再删：托管文件在而停不下来时报错退出 1、不删文件（删了文件，正在跑的进程也停不下来，还看不出来）。
// launchd 的“本来就没加载”不算失败；托管文件本来就不在时，停止的失败也不算。
func autostartUninstall(goos string, sp autostartSpec, dry bool, stdout, stderr io.Writer) int {
	var path, name string
	var args []string
	if goos == "darwin" {
		path, name, args = launchdPlistPath(sp.Home, sp.Kind), "launchctl", []string{"bootout", fmt.Sprintf("gui/%d/%s", sp.UID, launchdLabel(sp.Kind))}
	} else {
		path, name, args = systemdUnitPath(sp.Home, sp.Kind, sp.System), "systemctl", append(systemctlArgs(sp.System), "disable", "--now", autostart.UnitName(sp.Kind))
	}
	_, lerr := os.Lstat(path)
	existed := lerr == nil
	out, err := execCmd(dry, stdout, name, args...)
	if err != nil && goos == "darwin" && launchdNotLoaded(out, err) {
		err = nil
	}
	if err != nil && existed {
		fmt.Fprintf(stderr, "停止 %s 失败，托管文件 %s 没删：\n%v\n", sp.Kind, path, cmdError(name, args, out, err))
		return 1
	}
	if dry {
		fmt.Fprintf(stdout, "会删除 %s\n", path)
	} else if existed {
		if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
			existed = false
		} else if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if goos == "linux" {
		if err := runOrPrint(dry, stdout, "systemctl", append(systemctlArgs(sp.System), "daemon-reload")...); err != nil {
			fmt.Fprintf(stderr, "警告：%v\n", err)
		}
	}
	switch {
	case dry:
		fmt.Fprintln(stdout, "（--dry-run：以上没有删文件，也没有跑命令）")
	case existed:
		fmt.Fprintf(stdout, "已停止并移除 %s 的托管（删除了 %s）；数据目录保留\n", sp.Kind, path)
	default:
		fmt.Fprintf(stdout, "没有找到 %s：%s 本来就没托管\n", path, sp.Kind)
	}
	return 0
}

// launchdState 取 launchctl print 输出里作业的 state（第一处 “state = …”）；没有则为空。
func launchdState(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if s, ok := strings.CutPrefix(strings.TrimSpace(line), "state = "); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func autostartStatus(goos string, sp autostartSpec, dry bool, stdout io.Writer) int {
	var path, name string
	var args []string
	if goos == "darwin" {
		path, name, args = launchdPlistPath(sp.Home, sp.Kind), "launchctl", []string{"print", fmt.Sprintf("gui/%d/%s", sp.UID, launchdLabel(sp.Kind))}
	} else {
		path, name, args = systemdUnitPath(sp.Home, sp.Kind, sp.System), "systemctl", append(systemctlArgs(sp.System), "is-active", autostart.UnitName(sp.Kind))
	}
	if dry {
		_, _ = execCmd(true, stdout, name, args...)
		return 0
	}
	out, err := runCommand(name, args...)
	var detail string
	running := err == nil
	if goos == "darwin" {
		// KeepAlive 的作业崩溃后在等拉起时也算“已加载”，launchctl print 照样退出 0：要看 state
		if st := launchdState(out); st != "" && st != "running" {
			running, detail = false, "state = "+st
		} else if err != nil {
			detail = "launchd 里没有加载 " + launchdLabel(sp.Kind)
		}
	} else if detail = strings.TrimSpace(out); detail == "" && err != nil {
		detail = err.Error()
	}
	if running {
		fmt.Fprintf(stdout, "%s 已托管并在跑（%s）\n", sp.Kind, path)
		return 0
	}
	if _, serr := os.Stat(path); serr != nil {
		fmt.Fprintf(stdout, "%s 没有托管（没有 %s）；托管：%s\n", sp.Kind, path, strings.TrimSuffix(statusHint(sp), " --status"))
		return 1
	}
	fmt.Fprintf(stdout, "%s 有托管文件 %s，但没在跑（%s）；日志：%s\n", sp.Kind, path, detail, logHint(goos, sp))
	return 1
}
