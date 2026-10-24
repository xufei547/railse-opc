package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/autostart"
)

const autostartUsage = `用法:
  railse setup autostart [--hub] [--uninstall] [--status] [--dir 目录] [--bin 路径] [--system] [--user 名字] [--dry-run] [-- hub run 的额外参数]
    开机自启并在退出后自动拉起。默认托管守护进程（agent run）；--hub 托管 hub（hub run），-- 之后的参数原样追加到 hub run 后面，例如：
      railse setup autostart --hub -- --public-url https://hub.example.com
    macOS 写 ~/Library/LaunchAgents/dev.railse.<agent|hub>.plist（launchd 用户代理，日志在数据目录的 launchd.log）；
    Linux 写 ~/.config/systemd/user/railse-<agent|hub>.service（用户单元，日志：journalctl --user -u railse-<agent|hub>）。
    hub 要直出 443/80 时在 Linux 上用系统单元（只给绑定特权端口的权限，hub 不以 root 运行）：
      sudo railse setup autostart --hub --system [--user 名字] -- --acme-domain hub.example.com --acme-email me@example.com
      写 /etc/systemd/system/railse-hub.service，以 --user（默认 $SUDO_USER）运行，数据目录默认 /var/lib/railse-hub。
    --uninstall 停止并移除托管（数据目录保留）；--status 看是否在跑；--dry-run 只打印要写的文件与要跑的命令。`

const (
	// systemHubDir 是 hub 系统单元的默认数据目录，由 systemd 按 StateDirectory= 建好并交给运行用户。
	systemHubDir      = "/var/lib/railse-hub"
	systemHubStateDir = "railse-hub"
	// bootstrapRetries 是 launchctl bootstrap 失败后的重试次数：bootout 是异步的，旧作业还没退完时 bootstrap 会报
	// “Bootstrap failed: 5: Input/output error”。
	bootstrapRetries = 5
)

// 以下是变量只为测试能替换：跑系统命令（合并输出）、bootstrap 重试的间隔、查用户、改属主。
var (
	runCommand = func(name string, args ...string) (string, error) {
		out, err := exec.Command(name, args...).CombinedOutput()
		return string(out), err
	}
	bootstrapRetryDelay = 500 * time.Millisecond
	lookupUser          = user.Lookup
	chownPath           = os.Chown
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
	xml.EscapeText(&b, []byte(s))
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
// 含空白、引号、反斜杠或为空时整个用双引号包起来，里面的 \ 与 " 转义。控制字符在解析参数时已拒绝。
func systemdQuote(s string) string {
	s = strings.ReplaceAll(s, "%", "%%")
	if s != "" && !strings.ContainsAny(s, " \t\"'\\") {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// systemdExecArg 是 ExecStart 里程序名之后的一个参数：在 systemdQuote 之外把 $ 写成 $$（不让 systemd 当环境变量替换）。
func systemdExecArg(s string) string { return strings.ReplaceAll(systemdQuote(s), "$", "$$") }

// systemdUnit 生成用户单元或系统单元。崩溃后 2 秒拉起，不设启动次数上限（与 launchd 的 KeepAlive 一致）。
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
	b.WriteString("StartLimitIntervalSec=0\n\n[Service]\n")
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
	for _, s := range append([]string{*bin, *dir}, extra...) {
		if strings.IndexFunc(s, unicode.IsControl) >= 0 {
			return usageErr("参数里不能有换行等控制字符: %q", s)
		}
	}
	install := !*status && !*uninstall
	switch {
	case goos == "darwin" && uid == 0:
		fmt.Fprintln(stderr, "macOS 上不要用 sudo 跑：launchd 用户代理装在当前登录用户下，直接 railse setup autostart")
		return 1
	case *system && !*status && uid != 0 && !*dryRun:
		fmt.Fprintln(stderr, "--system 要写 /etc/systemd/system，需要 root：sudo railse setup autostart --hub --system …")
		return 1
	case install && *hubMode && !*system && uid == 0:
		fmt.Fprintln(stderr, "hub 不以 root 运行：要直出 443/80 用 sudo railse setup autostart --hub --system --user 名字（只给绑定特权端口的权限）；否则用普通用户跑本命令")
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
	var runAs *user.User
	if sp.System && install {
		if runAs, err = systemUnitUser(*userName); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		sp.User = runAs.Username
	}
	switch {
	case *status:
		return autostartStatus(goos, sp, *dryRun, stdout)
	case *uninstall:
		return autostartUninstall(goos, sp, *dryRun, stdout, stderr)
	default:
		return autostartInstall(goos, sp, runAs, *dryRun, stdout, stderr)
	}
}

// systemUnitUser 定系统单元的运行用户：--user，没给就取 $SUDO_USER；必须是本机已有的、uid 不为 0 的用户。
func systemUnitUser(name string) (*user.User, error) {
	if name == "" {
		name = os.Getenv("SUDO_USER")
	}
	if name == "" {
		return nil, errors.New("--system 要指定 hub 的运行用户：--user 名字（默认取 $SUDO_USER，现在没有）")
	}
	u, err := lookupUser(name)
	if err != nil {
		return nil, fmt.Errorf("找不到用户 %s: %v", name, err)
	}
	if name == "root" || u.Uid == "0" {
		return nil, fmt.Errorf("hub 不以 root 运行（用户 %s 的 uid 是 0）：用 --user 指定一个普通用户", name)
	}
	if _, err := strconv.Atoi(u.Uid); err != nil {
		return nil, fmt.Errorf("用户 %s 的 uid %q 不是数字", name, u.Uid)
	}
	if _, err := strconv.Atoi(u.Gid); err != nil {
		return nil, fmt.Errorf("用户 %s 的 gid %q 不是数字", name, u.Gid)
	}
	return u, nil
}

// runOrPrint 跑一条系统命令；dry 时只打印。失败时把命令的输出带进错误。
func runOrPrint(dry bool, stdout io.Writer, name string, args ...string) error {
	if dry {
		fmt.Fprintf(stdout, "$ %s %s\n", name, strings.Join(args, " "))
		return nil
	}
	out, err := runCommand(name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %v\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(out))
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

func autostartInstall(goos string, sp autostartSpec, runAs *user.User, dry bool, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var path string
	var content []byte
	if goos == "darwin" {
		path, content = launchdPlistPath(sp.Home, sp.Kind), launchdPlist(sp)
	} else {
		path, content = systemdUnitPath(sp.Home, sp.Kind, sp.System), systemdUnit(sp)
	}
	// 先建数据目录，出错就什么都不写。launchd 不会替 StandardOutPath（数据目录下的 launchd.log）建父目录，
	// 首次安装时目录可能还没有，作业会起不来；系统单元显式给了 --dir 时由 root 建好并交给运行用户。
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
		fmt.Fprintf(stdout, "会建数据目录 %s（0700，属主 %s）\n", sp.Dir, sp.User)
	case sp.System && sp.StateDir == "":
		if err := prepareSystemDir(sp.Dir, runAs); err != nil {
			return fail(err)
		}
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
		_ = runOrPrint(dry, stdout, "launchctl", "bootout", target+"/"+launchdLabel(sp.Kind)) // 还没装过时会失败，忽略
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

// prepareSystemDir 以 root 身份给系统单元准备 --dir 指定的数据目录：父目录按 0755 建（运行用户要能进去），
// 目录本身 0700 并交给运行用户。已有的目录若不是空的、又不属于运行用户，就拒绝，免得把 /etc 之类的目录整个交出去。
func prepareSystemDir(dir string, u *user.User) error {
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	st, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s 不是目录", dir)
	}
	if owner, ok := fileOwner(st); !ok || owner != uid {
		ents, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		if len(ents) > 0 {
			return fmt.Errorf("%s 已存在、不是空目录且不属于 %s：确认无误后自己 chown 给它再重跑，或换一个目录", dir, u.Username)
		}
	}
	if err := chownPath(dir, uid, gid); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func autostartUninstall(goos string, sp autostartSpec, dry bool, stdout, stderr io.Writer) int {
	var path string
	if goos == "darwin" {
		path = launchdPlistPath(sp.Home, sp.Kind)
		_ = runOrPrint(dry, stdout, "launchctl", "bootout", fmt.Sprintf("gui/%d/%s", sp.UID, launchdLabel(sp.Kind))) // 没加载时会失败，忽略
	} else {
		path = systemdUnitPath(sp.Home, sp.Kind, sp.System)
		_ = runOrPrint(dry, stdout, "systemctl", append(systemctlArgs(sp.System), "disable", "--now", autostart.UnitName(sp.Kind))...) // 没装过时会失败，忽略
	}
	existed := true
	if dry {
		fmt.Fprintf(stdout, "会删除 %s\n", path)
	} else if err := os.Remove(path); errors.Is(err, os.ErrNotExist) {
		existed = false
	} else if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if goos == "linux" {
		_ = runOrPrint(dry, stdout, "systemctl", append(systemctlArgs(sp.System), "daemon-reload")...)
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
		_ = runOrPrint(true, stdout, name, args...)
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
