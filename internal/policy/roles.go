package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// 角色（0.3 起）：一个角色是一道权限边界，定义在守护进程目录的 roles.yaml（见 docs/superpowers/specs/2026-11-11-railse-opc-v0.3-roles-design.md §2）。
// 本包只解析、校验与判定，不读文件；读文件、按 mtime 重读、把会话绑到角色上由守护进程（internal/agent）做。

// RoleSetVersion 是当前支持的角色文件版本。
const RoleSetVersion = 1

// 角色名与备注的长度上限（按字符计）。
const (
	roleNameMax = 32
	roleNoteMax = 120
)

// 放行时长上限的取值范围，与 railse trust 的上下限一致（5 分钟～24 小时）。超界报错，不截断：写错的上限悄悄变宽不可取。
const (
	roleTrustMin = 5 * time.Minute
	roleTrustMax = 24 * time.Hour
)

// TrustLimit 是角色的放行时长上限（trust_max）。Set 为假表示文件里没写（不设上限）；None 为真表示 none（这个角色的请求从不走限时放行）；
// 其余是时长 D。零值就是“不设”，与文件里不写一致。
type TrustLimit struct {
	Set  bool
	None bool
	D    time.Duration
}

// UnmarshalYAML 接受 none 或 Go 的时长写法（30m、2h），时长必须在 5m～24h 之间。
func (t *TrustLimit) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.ScalarNode {
		return errors.New("trust_max 必须是时长（如 30m、2h）或 none")
	}
	v, err := parseTrustLimit(n.Value)
	if err != nil {
		return err
	}
	*t = v
	return nil
}

func parseTrustLimit(s string) (TrustLimit, error) {
	if s == "none" {
		return TrustLimit{Set: true, None: true}, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return TrustLimit{}, fmt.Errorf("trust_max 必须是时长（如 30m、2h）或 none，得到 %q", s)
	}
	if d < roleTrustMin || d > roleTrustMax {
		return TrustLimit{}, fmt.Errorf("trust_max 必须在 5m～24h 之间，得到 %q", s)
	}
	return TrustLimit{Set: true, D: d}, nil
}

// MarshalYAML 写回原样的写法：不设时省略（配合 omitempty 由调用方处理），none 写 none，时长写 Go 的时长字符串。
func (t TrustLimit) MarshalYAML() (any, error) {
	switch {
	case !t.Set:
		return nil, nil
	case t.None:
		return "none", nil
	default:
		return t.D.String(), nil
	}
}

// IsZero 让 yaml 的 omitempty 认得“不设”。
func (t TrustLimit) IsZero() bool { return !t.Set }

// Minutes 是报给 hub 的分钟数（proto.RoleSummary.TrustMaxMins）：不设为 0，none 为 -1。
func (t TrustLimit) Minutes() int {
	switch {
	case !t.Set:
		return 0
	case t.None:
		return -1
	default:
		return int(t.D / time.Minute)
	}
}

// Role 是一个角色。Ceiling 为 nil 表示不设风险上限（不能用 Risk 零值表示“不设”：零值是 read，最严的一档）。
type Role struct {
	Name     string     `yaml:"name"`
	Note     string     `yaml:"note,omitempty"`
	Dirs     []string   `yaml:"dirs,omitempty"`
	Ceiling  *Risk      `yaml:"ceiling,omitempty"`
	Forbid   []Match    `yaml:"forbid,omitempty"`
	TrustMax TrustLimit `yaml:"trust_max,omitempty"`

	// compiled 是 Forbid 编好的形状（deny 语义，见 forbidRules），ParseRoleSet 预先编好；代码直接构造的角色在判定时现编。
	compiled []compiledRule
	// broken 非空表示这是“角色文件有误”时代替真角色的那个（见 BrokenRole），内容是错误原文。
	broken string
	// dirPats 是 PrepareDirs 规范化后的目录模式（与 Dirs 一一对应，每条可有原样与解析后两种写法）；foldDirs 为真时按 foldPath
	// 折叠大小写比较。没调 PrepareDirs 时为 nil，按原样展开 ~ 比较。
	dirPats  [][]string
	foldDirs bool
}

// RoleSet 是 roles.yaml 的整体。
type RoleSet struct {
	Version int    `yaml:"version"`
	Roles   []Role `yaml:"roles"`
}

// ParseRoleSet 解析并校验角色文件。同规则文件的严格程度：未知字段一律报错，只能有一个 YAML 文档。
// 返回的角色已预编译禁用清单。
func ParseRoleSet(data []byte) (RoleSet, error) {
	var rs RoleSet
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	err := dec.Decode(&rs)
	switch {
	case errors.Is(err, io.EOF):
		// 空文件，交给 Validate 报 version 错误：空文件不当“没有角色”，免得一次清空悄悄放宽
	case err != nil:
		return RoleSet{}, fmt.Errorf("解析角色文件: %w", err)
	default:
		var extra yaml.Node
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return RoleSet{}, errors.New("角色文件只能有一个 YAML 文档")
		}
	}
	if err := rs.Validate(); err != nil {
		return RoleSet{}, err
	}
	for i := range rs.Roles {
		rs.Roles[i].compiled = compileForbid(rs.Roles[i].Forbid)
	}
	return rs, nil
}

// Validate 检查版本、名字合法且不重复、每个角色的字段。dirs 重叠不算错（绑定按文件先后取第一个），由 railse role check 提醒。
func (rs RoleSet) Validate() error {
	if rs.Version != RoleSetVersion {
		return fmt.Errorf("角色文件 version 必须是 %d，得到 %d", RoleSetVersion, rs.Version)
	}
	seen := map[string]bool{}
	for i, r := range rs.Roles {
		if err := validRoleName(r.Name); err != nil {
			return fmt.Errorf("第 %d 个角色: %w", i+1, err)
		}
		if seen[r.Name] {
			return fmt.Errorf("角色名重复: %s", r.Name)
		}
		seen[r.Name] = true
		if err := r.validate(); err != nil {
			return fmt.Errorf("角色 %s: %w", r.Name, err)
		}
	}
	return nil
}

// validRoleName：1～32 个字符，不含空白与控制字符；可以是中文。
func validRoleName(name string) error {
	if name == "" {
		return errors.New("缺少名字（name）")
	}
	if !utf8.ValidString(name) {
		return errors.New("名字不是合法的 UTF-8")
	}
	if n := utf8.RuneCountInString(name); n > roleNameMax {
		return fmt.Errorf("名字至多 32 个字符，得到 %d 个", n)
	}
	for _, c := range name {
		switch {
		case unicode.IsSpace(c):
			return fmt.Errorf("名字 %q 不能含空白", name)
		case unicode.IsControl(c) || unicode.Is(unicode.Cf, c):
			return fmt.Errorf("名字 %q 不能含控制字符", name)
		}
	}
	return nil
}

func (r Role) validate() error {
	if !utf8.ValidString(r.Note) {
		return errors.New("note 不是合法的 UTF-8")
	}
	if n := utf8.RuneCountInString(r.Note); n > roleNoteMax {
		return fmt.Errorf("note 至多 120 个字符，得到 %d 个", n)
	}
	for _, c := range r.Note {
		if unicode.IsControl(c) {
			return errors.New("note 只能是一行，不能含换行与控制字符")
		}
	}
	for _, d := range r.Dirs {
		if d == "" || !validGlob(d) {
			return fmt.Errorf("dirs 里有不合法的路径模式: %q", d)
		}
	}
	for i, m := range r.Forbid {
		if err := m.validate(fmt.Sprintf("forbid 第 %d 条", i+1)); err != nil {
			return err
		}
	}
	return nil
}

// Find 按名字找角色，没有返回 nil。返回的指针指向 rs 里的元素，调用方不要改。
func (rs RoleSet) Find(name string) *Role {
	if name == "" {
		return nil
	}
	for i := range rs.Roles {
		if rs.Roles[i].Name == name {
			return &rs.Roles[i]
		}
	}
	return nil
}

// Covers 判断目录 dir（绝对路径）是否在角色的某条 dirs 内，口径同规则的 scope.project：模式展开 ~ 并清理后等于 dir，
// 或按 doublestar 命中 dir。dir 为空不命中；home 为空时 ~ 开头的模式不展开，于是不命中（地盘外，偏保守）。
func (r *Role) Covers(dir, home string) bool {
	return r.CoveringDir(dir, home) != ""
}

// CoveringDir 返回让 dir 命中的第一条 dirs 模式（原文），不命中返回空。调过 PrepareDirs 的角色用规范化后的模式（此时忽略 home）。
func (r *Role) CoveringDir(dir, home string) string {
	if dir == "" {
		return ""
	}
	clean := filepath.Clean(dir)
	if r.foldDirs {
		clean = foldPath(clean)
	}
	for i, d := range r.Dirs {
		var pats []string
		switch {
		case r.dirPats != nil:
			pats = r.dirPats[i]
		case strings.HasPrefix(d, "~") && home == "":
			continue
		default:
			pats = []string{filepath.Clean(expandHome(d, home))}
		}
		for _, pat := range pats {
			if r.foldDirs {
				pat = foldPath(pat)
			}
			if pat == clean {
				return d
			}
			if ok, _ := doublestar.Match(pat, clean); ok {
				return d
			}
		}
	}
	return ""
}

// PrepareDirs 按与 cwd 相同的口径规范化各角色的 dirs（守护进程在读入角色文件后调用；本包不做 IO，解析由 resolve 做）：
// 展开 ~（home 为空时 ~ 开头的模式不展开、什么都不命中），取第一个通配段之前的部分交给 resolve（守护进程传解析符号链接的函数），
// 原样与解析后的两种写法都算；fold 为真时（macOS）比较按 foldPath 折叠大小写，同敏感路径表的口径。
// 只规范化通配之前的部分：通配段匹配的是链接名还是真实名，不读目录就分不出。
func (rs *RoleSet) PrepareDirs(home string, resolve func(string) string, fold bool) {
	for i := range rs.Roles {
		r := &rs.Roles[i]
		r.foldDirs = fold
		r.dirPats = make([][]string, len(r.Dirs))
		for j, d := range r.Dirs {
			if strings.HasPrefix(d, "~") && home == "" {
				r.dirPats[j] = []string{} // 不命中
				continue
			}
			pat := filepath.Clean(expandHome(d, home))
			pats := []string{pat}
			if resolve != nil {
				prefix, rest := splitStatic(pat)
				if prefix != "" {
					if real := resolve(prefix); real != "" && real != prefix {
						pats = append(pats, filepath.Join(append([]string{real}, rest...)...))
					}
				}
			}
			r.dirPats[j] = pats
		}
	}
}

// splitStatic 把清理过的模式拆成第一个通配段之前的目录与其余各段。
func splitStatic(pat string) (string, []string) {
	parts := strings.Split(pat, "/")
	for i, seg := range parts {
		if strings.ContainsAny(seg, "*?[{\\") {
			prefix := strings.Join(parts[:i], "/")
			if prefix == "" && strings.HasPrefix(pat, "/") {
				prefix = "/"
			}
			return prefix, parts[i:]
		}
	}
	return pat, nil
}

// BrokenRole 是 roles.yaml 有误时代替会话角色的那个：上限 read（只读照旧自动放行，其余一律问人），没有名字（未分配），
// 判定理由写明“roles.yaml 有误：<错误>”。不沿用旧的好版本：一次手误不该悄悄放宽或悄悄失效。
func BrokenRole(err error) *Role {
	read := RiskRead
	msg := "未知错误"
	if err != nil {
		msg = err.Error()
	}
	return &Role{Ceiling: &read, broken: msg}
}

// Broken 返回角色文件的错误原文；不是 BrokenRole 造的角色返回空。
func (r *Role) Broken() string { return r.broken }

// Brief 是一条匹配条件的简述，用在禁用理由里：按字段顺序列出写了的字段，字段名照 YAML 的写法（中英文界面通用）。
func (m Match) Brief() string {
	var parts []string
	add := func(k, v string) { parts = append(parts, k+" "+v) }
	if m.Payload != "" {
		add("payload", string(m.Payload))
	}
	if m.Risk != nil {
		add("risk", m.Risk.String())
	}
	if m.CommandPrefix != "" {
		add("command_prefix", fmt.Sprintf("%q", m.CommandPrefix))
	}
	if m.CommandRegex != "" {
		add("command_regex", fmt.Sprintf("%q", m.CommandRegex))
	}
	if m.PathGlob != "" {
		add("path_glob", fmt.Sprintf("%q", m.PathGlob))
	}
	if m.MCPServer != "" {
		add("mcp_server", fmt.Sprintf("%q", m.MCPServer))
	}
	if m.MCPTool != "" {
		add("mcp_tool", fmt.Sprintf("%q", m.MCPTool))
	}
	if m.Domain != "" {
		add("domain", fmt.Sprintf("%q", m.Domain))
	}
	return strings.Join(parts, ", ")
}

// compileForbid 把禁用清单编成 deny 语义的规则（user 来源）：与用户 deny 规则同一套匹配，任一段命中即可、看穿 sh -c、
// 也比较归一化的命令、shell 的写入目标与命令里的 URL（见 matchRequest）。正则编不了的（只有绕过 Validate 直接构造的角色会这样）
// 标成 bad，判定时当作命中，fail closed。
func compileForbid(ms []Match) []compiledRule {
	out := make([]compiledRule, 0, len(ms))
	for i, m := range ms {
		cr := compiledRule{Rule: Rule{ID: fmt.Sprintf("forbid-%d", i+1), Source: SourceUser, Action: ActionDeny, Match: m}}
		if m.Risk != nil {
			v := *m.Risk
			cr.Match.Risk = &v
		}
		if m.CommandRegex != "" {
			re, err := regexp.Compile(m.CommandRegex)
			if err != nil {
				cr.bad = true
			}
			cr.re = re
		}
		if m.isEmpty() {
			cr.bad = true // 空条件不该出现（Validate 拒绝），出现了按命中处理
		}
		out = append(out, cr)
	}
	return out
}

// forbidRules 返回编好的禁用清单；ParseRoleSet 之外构造的角色（测试、hub 不会用）现编一次。
func (r *Role) forbidRules() []compiledRule {
	if len(r.compiled) == len(r.Forbid) {
		return r.compiled
	}
	return compileForbid(r.Forbid)
}
