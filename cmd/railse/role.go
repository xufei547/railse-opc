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

	"github.com/bmatcuk/doublestar/v4"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hookclient"
	"github.com/xufei547/railse-opc/internal/i18n"
	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

const roleUsage = `用法:
  railse role list  [--dir 目录]   列出角色：名字、上限、禁用条数、放行上限、目录、备注
  railse role check [--dir 目录]   校验 roles.yaml；报错误、目录重叠、指向不存在目录的模式
  railse role which [--dir 目录]   当前目录（与当前 RAILSE_ROLE）会解析成哪个角色，以及凭什么
  railse role init  [--dir 目录]   文件不存在时写一份带注释的起步模板

角色是一道权限边界，定义在守护进程目录的 roles.yaml。会话靠环境变量 RAILSE_ROLE（如 RAILSE_ROLE=写作 claude）
或项目目录（角色的 dirs）绑定角色；超出上限的一律问人，禁用的直接拒。`

// runRole 处理 railse role。list、which 先经本机 socket 问守护进程（role_list、role_which），守护进程没在运行时直接读文件并注明；
// check、init 只读写文件。
func runRole(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, i18n.T(roleUsage))
		return 2
	}
	sub := args[0]
	if sub == "-h" || sub == "--help" || sub == "help" {
		fmt.Fprintln(stdout, i18n.T(roleUsage))
		return 0
	}
	switch sub {
	case "list", "check", "which", "init":
	default:
		fmt.Fprintf(stderr, i18n.T("未知子命令 %q\n\n%s\n"), sub, i18n.T(roleUsage))
		return 2
	}
	fs := flag.NewFlagSet("role "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, i18n.T(roleUsage)) }
	dir := fs.String("dir", "", i18n.T("配置目录，默认 $RAILSE_HOME 或 ~/.railse"))
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, i18n.T("多余的参数 %q\n%s\n"), fs.Args(), i18n.T(roleUsage))
		return 2
	}
	cfg, err := agentConfigFor(*dir)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch sub {
	case "list":
		return roleList(cfg, stdout, stderr)
	case "check":
		return roleCheck(cfg, stdout, stderr)
	case "which":
		return roleWhich(cfg, stdout, stderr)
	default:
		return roleInit(cfg, stdout, stderr)
	}
}

// askRoles 经本机 socket 发 role_list 或 role_which。拿不到答复（守护进程没在运行、旧守护进程不认）时返回 nil，
// 并在 stderr 说明之后直接读文件。
func askRoles(cfg agent.Config, c proto.Control, stderr io.Writer) *proto.RolesInfo {
	client := hookclient.Client{SocketPath: cfg.SocketPath, ConnectTimeout: 2 * time.Second}
	reply, err := client.ControlMsg(c)
	switch {
	case err != nil:
		fmt.Fprintln(stderr, i18n.T("守护进程没在运行，直接读文件"))
		return nil
	case !reply.OK || reply.Roles == nil:
		// 升级前启动的旧守护进程不认 role_* 命令，也不按角色判定
		fmt.Fprintln(stderr, i18n.T("守护进程不认角色（升级前启动的旧进程），重启它后角色才生效；下面直接读文件"))
		return nil
	}
	return reply.Roles
}

func roleList(cfg agent.Config, stdout, stderr io.Writer) int {
	info := askRoles(cfg, proto.Control{V: proto.Version, Kind: "control", Op: "role_list"}, stderr)
	if info == nil {
		info = agent.RolesInfoOf(cfg.RolesPath, agent.LoadRolesFile(cfg.RolesPath))
	}
	if code, done := roleFileState(info, stdout, stderr); done {
		return code
	}
	if len(info.Roles) == 0 {
		fmt.Fprintf(stdout, i18n.T("%s 里没有角色：所有会话都是未分配\n"), info.Path)
		return 0
	}
	for _, r := range info.Roles {
		cols := []string{printable(r.Name)}
		if r.Ceiling != "" {
			cols = append(cols, fmt.Sprintf(i18n.T("上限 %s"), r.Ceiling))
		}
		if r.ForbidCount > 0 {
			cols = append(cols, fmt.Sprintf(i18n.T("禁用 %d 条"), r.ForbidCount))
		}
		switch {
		case r.TrustMaxMins < 0:
			cols = append(cols, i18n.T("不走限时放行"))
		case r.TrustMaxMins > 0:
			cols = append(cols, fmt.Sprintf(i18n.T("放行最长 %s"), trustSpan(time.Duration(r.TrustMaxMins)*time.Minute)))
		}
		if len(r.Dirs) > 0 {
			cols = append(cols, fmt.Sprintf(i18n.T("目录 %s"), printable(strings.Join(r.Dirs, ", "))))
		}
		if r.Note != "" {
			cols = append(cols, printable(r.Note))
		}
		fmt.Fprintln(stdout, strings.Join(cols, "  "))
	}
	return 0
}

// roleFileState 处理文件不存在与有误两种情况：说明并返回退出码与 true；文件正常时返回 false，由调用方接着列。
func roleFileState(info *proto.RolesInfo, stdout, stderr io.Writer) (int, bool) {
	switch {
	case !info.Exists:
		fmt.Fprintf(stdout, i18n.T("没有角色文件 %s：所有会话都是未分配，按规则走（railse role init 写一份起步模板）\n"), info.Path)
		return 0, true
	case info.Error != "":
		fmt.Fprintf(stderr, i18n.T("%s 有误: %s\n只读之外的审批一律问人，直到修好（改完不用重启）\n"), info.Path, info.Error)
		return 1, true
	}
	return 0, false
}

// roleCheck 校验 roles.yaml：错误退出 1；目录模式重叠、指向不存在的目录只提醒（退出 0）。
func roleCheck(cfg agent.Config, stdout, stderr io.Writer) int {
	st := agent.LoadRolesFile(cfg.RolesPath)
	if code, done := roleFileState(agent.RolesInfoOf(cfg.RolesPath, st), stdout, stderr); done {
		return code
	}
	home := cfg.HomeDir
	warns := 0
	roles := st.Set.Roles
	for i := range roles {
		for j := i + 1; j < len(roles); j++ {
			for _, a := range roles[i].Dirs {
				for _, b := range roles[j].Dirs {
					if dirsOverlap(a, b, home) {
						fmt.Fprintf(stdout, i18n.T("提醒：角色「%s」与「%s」的目录模式重叠（%s / %s），两边都命中的目录按文件顺序绑定到「%s」\n"),
							printable(roles[i].Name), printable(roles[j].Name), printable(a), printable(b), printable(roles[i].Name))
						warns++
					}
				}
			}
		}
		for _, d := range roles[i].Dirs {
			if base := staticPrefix(d, home); base != "" {
				if _, err := os.Stat(base); err != nil {
					fmt.Fprintf(stdout, i18n.T("提醒：角色「%s」的目录模式 %s 指向不存在的目录 %s\n"), printable(roles[i].Name), printable(d), base)
					warns++
				}
			}
		}
	}
	if warns > 0 {
		fmt.Fprintf(stdout, i18n.T("%s 合法（%d 个角色），有 %d 条提醒\n"), cfg.RolesPath, len(roles), warns)
	} else {
		fmt.Fprintf(stdout, i18n.T("%s 合法（%d 个角色）\n"), cfg.RolesPath, len(roles))
	}
	return 0
}

// dirsOverlap 粗略判断两条目录模式会不会命中同一个目录：展开 ~ 后相等，或一条把另一条当路径时命中。只用于提醒。
func dirsOverlap(a, b, home string) bool {
	pa, pb := filepath.Clean(expandTilde(a, home)), filepath.Clean(expandTilde(b, home))
	if pa == pb {
		return true
	}
	if ok, _ := doublestar.Match(pa, pb); ok {
		return true
	}
	ok, _ := doublestar.Match(pb, pa)
	return ok
}

// staticPrefix 是模式里第一个通配段之前的目录（展开 ~）；整条都是通配时为空。
func staticPrefix(pat, home string) string {
	p := filepath.Clean(expandTilde(pat, home))
	parts := strings.Split(p, string(filepath.Separator))
	var keep []string
	for _, seg := range parts {
		if strings.ContainsAny(seg, "*?[{") {
			break
		}
		keep = append(keep, seg)
	}
	out := strings.Join(keep, string(filepath.Separator))
	if out == "" && strings.HasPrefix(p, string(filepath.Separator)) && len(keep) > 0 {
		return string(filepath.Separator)
	}
	return out
}

func expandTilde(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// roleWhich 说明当前目录（与当前终端的 RAILSE_ROLE）会解析成哪个角色、凭什么。
func roleWhich(cfg agent.Config, stdout, stderr io.Writer) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	env := os.Getenv("RAILSE_ROLE")
	info := askRoles(cfg, proto.Control{V: proto.Version, Kind: "control", Op: "role_which", Role: env, Cwd: cwd}, stderr)
	if info == nil || info.Which == nil {
		st := agent.LoadRolesFile(cfg.RolesPath)
		info = agent.RolesInfoOf(cfg.RolesPath, st)
		w := agent.WhichRole(st, env, cwd, cfg.HomeDir)
		info.Which = &w
	}
	if code, done := roleFileState(info, stdout, stderr); done {
		return code
	}
	w := info.Which
	switch w.Kind {
	case agent.RoleByEnv:
		fmt.Fprintf(stdout, i18n.T("角色：%s（RAILSE_ROLE=%s）\n"), printable(w.Role), printable(w.Env))
	case agent.RoleByDir:
		fmt.Fprintf(stdout, i18n.T("角色：%s（目录 %s 命中 dirs 里的 %s）\n"), printable(w.Role), w.Dir, printable(w.Pattern))
	default:
		fmt.Fprintln(stdout, i18n.T("未分配：RAILSE_ROLE 与目录都没有指到角色，按规则走"))
	}
	if w.EnvUnknown {
		fmt.Fprintf(stdout, i18n.T("注意：RAILSE_ROLE=%s 在 roles.yaml 里没有，改按目录绑定\n"), printable(w.Env))
	}
	if w.OutsideDirs {
		fmt.Fprintf(stdout, i18n.T("注意：这里不在角色「%s」的目录内，没有项目根，写入一律按项目外处理\n"), printable(w.Role))
	}
	return 0
}

// roleInit 在文件不存在时写起步模板（0600）；已存在时不覆盖，退出 1。
func roleInit(cfg agent.Config, stdout, stderr io.Writer) int {
	if err := cfg.EnsureDirs(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	f, err := os.OpenFile(cfg.RolesPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		fmt.Fprintf(stderr, i18n.T("%s 已存在，不覆盖\n"), cfg.RolesPath)
		return 1
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, err = f.Write(agent.RoleTemplate(i18n.Lang() == i18n.EN))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(cfg.RolesPath) // 写了一半的文件会让所有审批按“有误”处理，删掉
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, i18n.T("已写入 %s：三个起步角色，dirs 都注释着，写下它不改变任何会话。\n试一下：RAILSE_ROLE=%s claude\n"), cfg.RolesPath, roleTemplateFirst())
	return 0
}

// roleTemplateFirst 是模板里第一个角色的名字（提示里用）。
func roleTemplateFirst() string {
	rs, err := policy.ParseRoleSet(agent.RoleTemplate(i18n.Lang() == i18n.EN))
	if err != nil || len(rs.Roles) == 0 {
		return ""
	}
	return rs.Roles[0].Name
}
