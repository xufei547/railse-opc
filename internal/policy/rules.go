package policy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"gopkg.in/yaml.v3"
)

// Action 是规则的动作。
type Action string

const (
	ActionAllow Action = "allow"
	ActionDeny  Action = "deny"
	ActionAsk   Action = "ask"
)

// Source 是规则的来源。只有 user 来源能放行 outward 与 destructive。
type Source string

const (
	SourceBuiltin Source = "builtin"
	SourceUser    Source = "user"
	SourceLearned Source = "learned"
)

// Scope 是规则的作用范围。两个字段都为空表示 global。
type Scope struct {
	Machine string `yaml:"machine,omitempty"`
	Project string `yaml:"project,omitempty"` // 路径模式，支持 ~ 与 doublestar 通配
}

func (s Scope) IsGlobal() bool { return s.Machine == "" && s.Project == "" }

// specificity 越大越优先：project 3、machine 2、global 1。
func (s Scope) specificity() int {
	switch {
	case s.Project != "":
		return 3
	case s.Machine != "":
		return 2
	default:
		return 1
	}
}

// errScopeBoth 由解析和 Validate 共用，保证代码构造的规则集写出后能再读回。
var errScopeBoth = errors.New("scope 不能同时指定 machine 和 project")

// UnmarshalYAML 同时接受字符串 global 和映射 {project: …} 或 {machine: …}。
// 缺少 scope 键或写 scope: null 时 yaml.v3 不调用本方法，Scope 保持零值，即 global，这是约定的默认。
// 其余写法必须显式：空字符串、空映射和空值都报错，不会悄悄变成 global。
func (s *Scope) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value == "global" {
			*s = Scope{}
			return nil
		}
		return fmt.Errorf("scope 字符串只能是 global，得到 %q", n.Value)
	}
	// n.Decode 不继承外层解码器的 KnownFields，这里逐个检查键，拒绝拼错的字段。
	if n.Kind == yaml.MappingNode {
		if len(n.Content) == 0 {
			return fmt.Errorf("scope 映射不能为空，全局范围请写 global")
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i].Value, n.Content[i+1]
			switch k {
			case "machine", "project":
			default:
				return fmt.Errorf("scope 不支持字段 %q，只能是 machine 或 project", k)
			}
			if v.Kind == yaml.ScalarNode && (v.ShortTag() == "!!null" || v.Value == "") {
				return fmt.Errorf("scope.%s 的值不能为空", k)
			}
		}
	}
	type raw Scope
	var r raw
	if err := n.Decode(&r); err != nil {
		return err
	}
	if r.Machine == "" && r.Project == "" {
		return fmt.Errorf("scope 的值不能为空，全局范围请写 global") // 兜底：别名等解析成空值
	}
	if r.Machine != "" && r.Project != "" {
		return errScopeBoth
	}
	*s = Scope(r)
	return nil
}

// MarshalYAML 把 global 写成字符串，其余写成映射。
func (s Scope) MarshalYAML() (any, error) {
	if s.IsGlobal() {
		return "global", nil
	}
	type raw Scope
	return raw(s), nil
}

// Match 是结构化匹配条件，多个字段为与关系。
type Match struct {
	Payload       PayloadType `yaml:"payload,omitempty"`
	Risk          *Risk       `yaml:"risk,omitempty"`
	CommandPrefix string      `yaml:"command_prefix,omitempty"`
	CommandRegex  string      `yaml:"command_regex,omitempty"`
	PathGlob      string      `yaml:"path_glob,omitempty"`
	MCPServer     string      `yaml:"mcp_server,omitempty"`
	MCPTool       string      `yaml:"mcp_tool,omitempty"`
	Domain        string      `yaml:"domain,omitempty"`
}

func (m Match) isEmpty() bool { return m == Match{} }

// Rule 是一条规则。Enabled 为 nil 视为启用。
type Rule struct {
	ID          string `yaml:"id"`
	Source      Source `yaml:"source"`
	Scope       Scope  `yaml:"scope"`
	Match       Match  `yaml:"match"`
	Action      Action `yaml:"action"`
	Enabled     *bool  `yaml:"enabled,omitempty"`
	CreatedFrom string `yaml:"created_from,omitempty"`
}

func (r Rule) IsEnabled() bool { return r.Enabled == nil || *r.Enabled }

// RuleSet 是规则文件的整体。
type RuleSet struct {
	Version int    `yaml:"version"`
	Rules   []Rule `yaml:"rules"`
}

// RuleSetVersion 是当前支持的规则文件版本。
const RuleSetVersion = 1

// ParseRuleSet 解析并校验 YAML 规则文件。未知字段一律报错，拼错的键不会悄悄放宽规则。
func ParseRuleSet(data []byte) (RuleSet, error) {
	var rs RuleSet
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	err := dec.Decode(&rs)
	switch {
	case errors.Is(err, io.EOF):
		// 空文件，交给 Validate 报 version 错误。
	case err != nil:
		return RuleSet{}, fmt.Errorf("解析规则文件: %w", err)
	default:
		// 后续文档会被悄悄忽略，直接拒绝。
		var extra yaml.Node
		if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
			return RuleSet{}, errors.New("规则文件只能有一个 YAML 文档")
		}
	}
	if err := rs.Validate(); err != nil {
		return RuleSet{}, err
	}
	return rs, nil
}

// Marshal 序列化为 YAML。
func (rs RuleSet) Marshal() ([]byte, error) { return yaml.Marshal(rs) }

// Validate 检查版本、id 唯一和每条规则。
func (rs RuleSet) Validate() error {
	if rs.Version != RuleSetVersion {
		return fmt.Errorf("规则文件 version 必须是 %d，得到 %d", RuleSetVersion, rs.Version)
	}
	seen := map[string]bool{}
	for i, r := range rs.Rules {
		if r.ID == "" {
			return fmt.Errorf("第 %d 条规则缺少 id", i+1)
		}
		if seen[r.ID] {
			return fmt.Errorf("规则 id 重复: %s", r.ID)
		}
		seen[r.ID] = true
		if err := r.validate(); err != nil {
			return fmt.Errorf("规则 %s: %w", r.ID, err)
		}
	}
	return nil
}

func (r Rule) validate() error {
	switch r.Source {
	case SourceBuiltin, SourceUser, SourceLearned:
	default:
		return fmt.Errorf("source 必须是 builtin、user 或 learned，得到 %q", r.Source)
	}
	switch r.Action {
	case ActionAllow, ActionDeny, ActionAsk:
	default:
		return fmt.Errorf("action 必须是 allow、deny 或 ask，得到 %q", r.Action)
	}
	if r.Match.isEmpty() {
		return fmt.Errorf("match 不能为空")
	}
	switch r.Match.Payload {
	case "", PayloadShell, PayloadFileWrite, PayloadMCP, PayloadNetwork:
	default:
		return fmt.Errorf("match.payload 必须是 shell、file_write、mcp 或 network，得到 %q", r.Match.Payload)
	}
	if r.Match.PathGlob != "" && !validGlob(r.Match.PathGlob) {
		return fmt.Errorf("match.path_glob 不是合法的通配模式: %q", r.Match.PathGlob)
	}
	if r.Scope.Machine != "" && r.Scope.Project != "" {
		return errScopeBoth
	}
	if r.Scope.Project != "" && !validGlob(r.Scope.Project) {
		return fmt.Errorf("scope.project 不是合法的通配模式: %q", r.Scope.Project)
	}
	if r.Match.CommandRegex != "" {
		if _, err := regexp.Compile(r.Match.CommandRegex); err != nil {
			return fmt.Errorf("command_regex 无效: %w", err)
		}
	}
	if r.Action == ActionAllow && r.Source != SourceUser {
		if r.Match.Risk != nil && *r.Match.Risk >= RiskOutward {
			return fmt.Errorf("只有 user 来源的规则才能放行 %s", *r.Match.Risk)
		}
		if r.Source == SourceLearned {
			return r.Match.validateLearned()
		}
	}
	return nil
}

// validateLearned 检查 learned 放行规则的形状：risk 只能是 read、write_in_project 或 network，
// 并且必须带与 payload 对应的锚点，不能只凭风险等级放行一整类请求。
func (m Match) validateLearned() error {
	if m.Risk == nil {
		return fmt.Errorf("learned 规则必须带 risk")
	}
	switch *m.Risk {
	case RiskRead, RiskWriteInProject, RiskNetwork:
	default:
		return fmt.Errorf("learned 规则的 risk 只能是 read、write_in_project 或 network，得到 %s", *m.Risk)
	}
	switch m.Payload {
	case PayloadShell:
		if m.CommandPrefix == "" {
			return fmt.Errorf("learned 的 shell 规则必须带 command_prefix")
		}
	case PayloadFileWrite:
		if m.PathGlob == "" {
			return fmt.Errorf("learned 的 file_write 规则必须带 path_glob")
		}
	case PayloadMCP:
		if m.MCPServer == "" || m.MCPTool == "" {
			return fmt.Errorf("learned 的 mcp 规则必须同时带 mcp_server 和 mcp_tool")
		}
	case PayloadNetwork:
		if m.Domain == "" {
			return fmt.Errorf("learned 的 network 规则必须带 domain")
		}
		// 按 hostMatches 的归一化检查：不能只锚到顶级域
		if d := normalizeHost(m.Domain); !strings.Contains(d, ".") || strings.HasPrefix(d, ".") {
			return fmt.Errorf("learned 的 network 规则的 domain 至少要有两级且不能以点开头，得到 %q", m.Domain)
		}
	default:
		return fmt.Errorf("learned 规则必须指定 payload")
	}
	return nil
}

// validGlob 检查 doublestar 通配模式，开头的 ~/ 由引擎展开，先去掉再校验。
func validGlob(p string) bool {
	return doublestar.ValidatePattern(strings.TrimPrefix(p, "~/"))
}
