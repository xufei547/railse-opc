package policy

import _ "embed"

//go:embed default_rules.yaml
var defaultRulesYAML []byte

// DefaultRuleSet 返回内置规则包。文件随二进制嵌入，解析失败属于构建错误，直接 panic。
func DefaultRuleSet() RuleSet {
	rs, err := ParseRuleSet(defaultRulesYAML)
	if err != nil {
		panic("内置规则包无效: " + err.Error())
	}
	return rs
}
