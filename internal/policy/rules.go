package policy

import (
	"fmt"
	"regexp"

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

// UnmarshalYAML 同时接受字符串 global 和映射 {project: …} 或 {machine: …}。
func (s *Scope) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if n.Value == "global" || n.Value == "" {
			*s = Scope{}
			return nil
		}
		return fmt.Errorf("scope 字符串只能是 global，得到 %q", n.Value)
	}
	type raw Scope
	var r raw
	if err := n.Decode(&r); err != nil {
		return err
	}
	if r.Machine != "" && r.Project != "" {
		return fmt.Errorf("scope 不能同时指定 machine 和 project")
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

// ParseRuleSet 解析并校验 YAML 规则文件。
func ParseRuleSet(data []byte) (RuleSet, error) {
	var rs RuleSet
	if err := yaml.Unmarshal(data, &rs); err != nil {
		return RuleSet{}, fmt.Errorf("解析规则文件: %w", err)
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
	if r.Match.CommandRegex != "" {
		if _, err := regexp.Compile(r.Match.CommandRegex); err != nil {
			return fmt.Errorf("command_regex 无效: %w", err)
		}
	}
	if r.Action == ActionAllow && r.Source != SourceUser {
		if r.Match.Risk != nil && *r.Match.Risk >= RiskOutward {
			return fmt.Errorf("只有 user 来源的规则才能放行 %s", *r.Match.Risk)
		}
		if r.Source == SourceLearned && r.Match.Risk == nil {
			return fmt.Errorf("learned 规则必须带 risk")
		}
	}
	return nil
}
