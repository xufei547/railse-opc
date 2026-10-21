package policy

import (
	"reflect"
	"testing"
)

func TestSplitSegments(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`git status`, []string{"git status"}},
		{`git add . && git commit -m "a && b"`, []string{"git add .", `git commit -m "a && b"`}},
		{`echo "a;b" ; ls`, []string{`echo "a;b"`, "ls"}},
		{`cat x | grep y || true`, []string{"cat x", "grep y", "true"}},
		{`echo 'it''s' ; `, []string{`echo 'it''s'`}},
		{`   `, nil},
		{`a \&\& b`, []string{`a \&\& b`}},
		{`ls & rm -rf /`, []string{"ls", "rm -rf /"}},
		{`cmd &> all.log`, []string{"cmd &> all.log"}},
		{`cmd 2>&1 | grep x`, []string{"cmd 2>&1", "grep x"}},
		{"echo a\necho b", []string{"echo a", "echo b"}},
		{`echo "a\"" && ls`, []string{`echo "a\""`, "ls"}},
		{`echo 'a\' && ls`, []string{`echo 'a\'`, "ls"}},
		{`cat x\>&rm -rf /`, []string{`cat x\>`, "rm -rf /"}},
	}
	for _, c := range cases {
		if got := splitSegments(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitSegments(%q) = %q, 想要 %q", c.in, got, c.want)
		}
	}
}

func TestParseSegment(t *testing.T) {
	cases := []struct {
		in       string
		argv     []string
		redir    []string
		subshell bool
	}{
		{`git commit -m 'hello world'`, []string{"git", "commit", "-m", "hello world"}, nil, false},
		{`echo hi > out.txt`, []string{"echo", "hi"}, []string{"out.txt"}, false},
		{`echo hi >> log`, []string{"echo", "hi"}, []string{"log"}, false},
		{`cmd 2>/dev/null`, []string{"cmd"}, []string{"/dev/null"}, false},
		{`cmd &> all.log`, []string{"cmd"}, []string{"all.log"}, false},
		{`cmd 2>&1`, []string{"cmd"}, []string{"&1"}, false},
		{`echo $(date)`, []string{"echo", "$(date)"}, nil, true},
		{"echo `date`", []string{"echo", "date"}, nil, true},
		{`echo "x > y"`, []string{"echo", "x > y"}, nil, false},
		{`sleep 2 > out`, []string{"sleep", "2"}, []string{"out"}, false},
		{`a\ b c`, []string{"a b", "c"}, nil, false},
		{`echo "$(date)"`, []string{"echo", "$(date)"}, nil, true},
		{"echo \"`date`\"", []string{"echo", "`date`"}, nil, true},
		{`echo '$(date)'`, []string{"echo", "$(date)"}, nil, false},
		{`echo "a\"b"`, []string{"echo", `a"b`}, nil, false},
	}
	for _, c := range cases {
		got := parseSegment(c.in)
		if !reflect.DeepEqual(got.argv, c.argv) {
			t.Errorf("parseSegment(%q).argv = %q, 想要 %q", c.in, got.argv, c.argv)
		}
		if !reflect.DeepEqual(got.redirects, c.redir) {
			t.Errorf("parseSegment(%q).redirects = %q, 想要 %q", c.in, got.redirects, c.redir)
		}
		if got.subshell != c.subshell {
			t.Errorf("parseSegment(%q).subshell = %v, 想要 %v", c.in, got.subshell, c.subshell)
		}
		if got.text != c.in {
			t.Errorf("parseSegment(%q).text = %q", c.in, got.text)
		}
	}
}
