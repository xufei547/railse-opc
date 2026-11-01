package agent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"

	"gopkg.in/yaml.v3"
)

// PreToolUse 来的中间三级（network、write_outside、unknown）怎么办，不看 Claude Code 的权限模式。
const (
	UndecidedClassifier = "classifier" // 不表态，交给 Claude 自己的 allow 列表与分类器；它要问人时 PermissionRequest 再送手机（默认）
	UndecidedPhone      = "phone"      // 与硬底线一样：PreToolUse 回 ask，随后的 PermissionRequest 送手机
)

// Settings 是本机设置（~/.railse/settings.yaml），只管这台机器的偏好，不随 hub 下发。
// 不放进 rules.yaml：那份文件由 hub 下发覆盖，解析时也拒绝未知字段。
type Settings struct {
	ClaudeUndecided string `yaml:"claude_undecided"`
}

// DefaultSettings 是文件不存在或读坏时用的设置。
func DefaultSettings() Settings { return Settings{ClaudeUndecided: UndecidedClassifier} }

// LoadSettings 读本机设置。文件不存在返回默认值；读不了、解析不了、有未知字段或值不认识时返回默认值与说明原因的错误，
// 调用方记日志后照默认值跑，不让一份写错的设置挡住审批。
func LoadSettings(path string) (Settings, error) {
	def := DefaultSettings()
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	var s Settings
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil && !errors.Is(err, io.EOF) {
		return def, fmt.Errorf("解析 %s: %w", path, err)
	}
	switch s.ClaudeUndecided {
	case "":
		s.ClaudeUndecided = def.ClaudeUndecided
	case UndecidedClassifier, UndecidedPhone:
	default:
		return def, fmt.Errorf("%s 的 claude_undecided 只能是 classifier 或 phone，收到 %q", path, s.ClaudeUndecided)
	}
	return s, nil
}
