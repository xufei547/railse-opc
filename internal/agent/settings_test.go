package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSettings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.yaml")
	if s, err := LoadSettings(p); err != nil || s.ClaudeAutoMode != ClaudeAutoClassifier {
		t.Fatalf("不存在：%+v %v", s, err)
	}
	cases := []struct {
		body, want string
		bad        bool
	}{
		{"", ClaudeAutoClassifier, false},
		{"# 只有注释\n", ClaudeAutoClassifier, false},
		{"claude_auto_mode: phone\n", ClaudeAutoPhone, false},
		{"claude_auto_mode: classifier\n", ClaudeAutoClassifier, false},
		{"claude_auto_mode: sometimes\n", ClaudeAutoClassifier, true},
		{"claude_auto_mod: phone\n", ClaudeAutoClassifier, true}, // 拼错的字段不能悄悄忽略
		{"claude_auto_mode: [\n", ClaudeAutoClassifier, true},
	}
	for _, c := range cases {
		os.WriteFile(p, []byte(c.body), 0o600)
		s, err := LoadSettings(p)
		if s.ClaudeAutoMode != c.want || (err != nil) != c.bad {
			t.Errorf("%q: %+v err=%v", c.body, s, err)
		}
	}
}
