package policy

import "fmt"

// Risk 是请求的风险等级，数值越大越危险，复合命令取最高档。
type Risk int

const (
	RiskRead           Risk = iota // 只读、测试、检查
	RiskWriteInProject             // 项目目录内的改动
	RiskUnknown                    // 命令表之外，无法判断
	RiskWriteOutside               // 项目目录外的改动
	RiskNetwork                    // 联网、安装依赖、MCP
	RiskOutward                    // 对外：push、发布、发邮件、付款
	RiskDestructive                // 不可逆：递归删除、强推、敏感文件
)

var riskNames = [...]string{
	RiskRead:           "read",
	RiskWriteInProject: "write_in_project",
	RiskUnknown:        "unknown",
	RiskWriteOutside:   "write_outside",
	RiskNetwork:        "network",
	RiskOutward:        "outward",
	RiskDestructive:    "destructive",
}

func (r Risk) String() string {
	if r >= 0 && int(r) < len(riskNames) {
		return riskNames[r]
	}
	return fmt.Sprintf("risk(%d)", int(r))
}

// ParseRisk 把 YAML 或 JSON 里的名字解析成 Risk。
func ParseRisk(s string) (Risk, error) {
	for i, name := range riskNames {
		if name == s {
			return Risk(i), nil
		}
	}
	return 0, fmt.Errorf("未知的风险等级 %q", s)
}

func (r Risk) MarshalText() ([]byte, error) { return []byte(r.String()), nil }

func (r *Risk) UnmarshalText(b []byte) error {
	v, err := ParseRisk(string(b))
	if err != nil {
		return err
	}
	*r = v
	return nil
}

func maxRisk(a, b Risk) Risk {
	if a > b {
		return a
	}
	return b
}
