package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSettings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "settings.yaml")
	if s, err := LoadSettings(p); err != nil || s.ClaudeUndecided != UndecidedClassifier {
		t.Fatalf("不存在：%+v %v", s, err)
	}
	cases := []struct {
		body, want string
		bad        bool
	}{
		{"", UndecidedClassifier, false},
		{"# 只有注释\n", UndecidedClassifier, false},
		{"claude_undecided: phone\n", UndecidedPhone, false},
		{"claude_undecided: classifier\n", UndecidedClassifier, false},
		{"claude_undecided: sometimes\n", UndecidedClassifier, true},
		{"claude_undecide: phone\n", UndecidedClassifier, true}, // 拼错的字段不能悄悄忽略
		{"claude_undecided: [\n", UndecidedClassifier, true},
	}
	for _, c := range cases {
		os.WriteFile(p, []byte(c.body), 0o600)
		s, err := LoadSettings(p)
		if s.ClaudeUndecided != c.want || (err != nil) != c.bad {
			t.Errorf("%q: %+v err=%v", c.body, s, err)
		}
	}
}
