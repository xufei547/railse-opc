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
		{`echo $'\'' ; rm -rf /`, []string{`echo $'\'' ; rm -rf /`, `echo $'\''`, "rm -rf /"}},
		{`echo $'\'' ; rm -rf / # '`, []string{`echo $'\'' ; rm -rf / # '`, `echo $'\''`, "rm -rf /"}},
		{"echo # \"\nrm -rf /", []string{"echo", "rm -rf /"}},
		{"cat <<EOF\nls \"\nEOF\nrm -rf /", []string{"cat <<EOF\nls \"\nEOF\nrm -rf /"}},
		{`a |& b`, []string{"a", "b"}},
		{`sleep 1 &`, []string{"sleep 1"}},
		{`echo "a & b"`, []string{`echo "a & b"`}},
		{"echo \"a\nb\"", []string{"echo \"a\nb\""}},
		{`echo x >| ~/.zshrc`, []string{`echo x >| ~/.zshrc`}},
		{`cat <&3`, []string{`cat <&3`}},
		{"rm\\\n -rf /", []string{"rm\\\n -rf /"}},
		{`ls && echo $(date)`, []string{`ls && echo $(date)`, "ls", "echo $(date)"}},
		{`echo "$(echo \"x\" ; rm -rf /)"`, []string{`echo "$(echo \"x\" ; rm -rf /)"`}},
		{`cat <(ls) | grep x`, []string{`cat <(ls) | grep x`, "cat <(ls)", "grep x"}},
		{`echo $'ab' ; ls`, []string{`echo $'ab'`, "ls"}},
		{`ls ; git push --force ; echo $(date)`, []string{`ls ; git push --force ; echo $(date)`, "ls", "git push --force", "echo $(date)"}},
		{`(cd x && make)`, []string{`(cd x && make)`, "(cd x", "make)"}},
		{`echo ')'`, []string{`echo ')'`}},
	}
	for _, c := range cases {
		if got := splitSegments(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitSegments(%q) = %q, 想要 %q", c.in, got, c.want)
		}
	}
}

// TestSplitSegmentsWholeFirst 无法分析的命令第一段是整条命令，且解析结果标 subshell。
func TestSplitSegmentsWholeFirst(t *testing.T) {
	for _, in := range []string{"(#x\necho inside)"} {
		segs := splitSegments(in)
		if len(segs) == 0 || segs[0] != in {
			t.Errorf("splitSegments(%q) = %q, 第一段应为整条命令", in, segs)
			continue
		}
		if !parseSegment(segs[0]).subshell {
			t.Errorf("parseSegment(%q).subshell = false, 想要 true", segs[0])
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
		{`cmd 2>&1`, []string{"cmd"}, nil, false},
		{`echo $(date)`, []string{"echo", "$(date)"}, nil, true},
		{"echo `date`", []string{"echo", "date"}, nil, true},
		{`echo "x > y"`, []string{"echo", "x > y"}, nil, false},
		{`sleep 2 > out`, []string{"sleep", "2"}, []string{"out"}, false},
		{`a\ b c`, []string{"a b", "c"}, nil, false},
		{`echo "$(date)"`, []string{"echo", "$(date)"}, nil, true},
		{"echo \"`date`\"", []string{"echo", "`date`"}, nil, true},
		{`echo '$(date)'`, []string{"echo", "$(date)"}, nil, false},
		{`echo "a\"b"`, []string{"echo", `a"b`}, nil, false},
		{"rm\\\n -rf /", []string{"rm", "-rf", "/"}, nil, false},
		{`git push --force`, []string{"git", "push", "--force"}, nil, false},
		{`echo ')'`, []string{"echo", ")"}, nil, false},
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

// TestParseSegmentRedirects 逐项比对 argv、输出目标、输入来源与 subshell。
func TestParseSegmentRedirects(t *testing.T) {
	cases := []struct {
		in       string
		argv     []string
		redir    []string
		inputs   []string
		subshell bool
	}{
		{`echo pwn >& ~/Desktop/x`, []string{"echo", "pwn"}, []string{"~/Desktop/x"}, nil, false},
		{`echo x >| ~/.zshrc`, []string{"echo", "x"}, []string{"~/.zshrc"}, nil, false},
		{`cmd >&2`, []string{"cmd"}, nil, nil, false},
		{`cat < ~/.ssh/id_rsa`, []string{"cat"}, nil, []string{"~/.ssh/id_rsa"}, false},
		{`echo "2">f`, []string{"echo", "2"}, []string{"f"}, nil, false},
	}
	for _, c := range cases {
		got := parseSegment(c.in)
		if !reflect.DeepEqual(got.argv, c.argv) {
			t.Errorf("parseSegment(%q).argv = %q, 想要 %q", c.in, got.argv, c.argv)
		}
		if !reflect.DeepEqual(got.redirects, c.redir) {
			t.Errorf("parseSegment(%q).redirects = %q, 想要 %q", c.in, got.redirects, c.redir)
		}
		if !reflect.DeepEqual(got.inputs, c.inputs) {
			t.Errorf("parseSegment(%q).inputs = %q, 想要 %q", c.in, got.inputs, c.inputs)
		}
		if got.subshell != c.subshell {
			t.Errorf("parseSegment(%q).subshell = %v, 想要 %v", c.in, got.subshell, c.subshell)
		}
	}
}

// TestParseSegmentFailClosed 无法静态分析的写法一律标 subshell，text 保持原文。
func TestParseSegmentFailClosed(t *testing.T) {
	cases := []struct {
		in     string
		inputs []string
	}{
		{`cat <(rm -rf ~)`, nil},
		{`tee >(sh) < x`, []string{"x"}},
		{`echo "unclosed`, nil},
		{`echo a >`, nil},
		{"cat <<EOF\nls \"\nEOF\nrm -rf /", nil},
		{`echo $'\'' ; rm -rf /`, nil},
		{`echo $'\'' ; rm -rf / # '`, nil},
		{`ls && echo $(date)`, nil},
		{`echo "$(echo \"x\" ; rm -rf /)"`, nil},
		{`cat <(ls) | grep x`, nil},
		{`echo $'a\tb'`, nil},
		{`ls ; git push --force ; echo $(date)`, nil},
		{`(cd x && make)`, nil},
	}
	for _, c := range cases {
		got := parseSegment(c.in)
		if !got.subshell {
			t.Errorf("parseSegment(%q).subshell = false, 想要 true", c.in)
		}
		if !reflect.DeepEqual(got.inputs, c.inputs) {
			t.Errorf("parseSegment(%q).inputs = %q, 想要 %q", c.in, got.inputs, c.inputs)
		}
		if got.text != c.in {
			t.Errorf("parseSegment(%q).text = %q", c.in, got.text)
		}
	}
}

// TestParseSegmentComments 只有出现在词首的 # 才开始注释。
func TestParseSegmentComments(t *testing.T) {
	cases := []struct {
		in   string
		argv []string
	}{
		{`echo a#b`, []string{"echo", "a#b"}},
		{`echo '#x'`, []string{"echo", "#x"}},
		{`echo hi # trailing`, []string{"echo", "hi"}},
	}
	for _, c := range cases {
		if got := parseSegment(c.in).argv; !reflect.DeepEqual(got, c.argv) {
			t.Errorf("parseSegment(%q).argv = %q, 想要 %q", c.in, got, c.argv)
		}
	}
}

// TestParseSegmentWordFlags 逐词记录引号外、未转义的 glob 元字符、$ 展开与词首 ~。
func TestParseSegmentWordFlags(t *testing.T) {
	cases := []struct {
		in    string
		where string // argv、redirect 或 input
		i     int
		want  wordFlags
	}{
		{`grep 'foo.*'`, "argv", 1, wordFlags{}},
		{`grep foo.*`, "argv", 1, wordFlags{glob: true}},
		{`echo "$X"`, "argv", 1, wordFlags{expand: true}},
		{`echo '$X'`, "argv", 1, wordFlags{}},
		{`echo \$X`, "argv", 1, wordFlags{}},
		{`ls ~/x`, "argv", 1, wordFlags{tilde: true}},
		{`ls '~/x'`, "argv", 1, wordFlags{}},
		{`echo x > "$F"`, "redirect", 0, wordFlags{expand: true}},
		{`cat < $F`, "input", 0, wordFlags{expand: true}},
		{`ls a\*b`, "argv", 1, wordFlags{}},
		{`ls "a*b"`, "argv", 1, wordFlags{}},
		{`ls x~`, "argv", 1, wordFlags{}},
		{`ls ""~`, "argv", 1, wordFlags{}},
		{`ls ~/*.go`, "argv", 1, wordFlags{glob: true, tilde: true}},
		{`echo $'$X'`, "argv", 1, wordFlags{}},
		{`echo a{b,c}`, "argv", 1, wordFlags{glob: true}},
		// { 只有在配对的 } 之间有引号外的 , 或 .. 时才是花括号展开
		{`cat ${HOME}/x`, "argv", 1, wordFlags{expand: true}},
		{`git show stash@{0}`, "argv", 2, wordFlags{}},
		{`echo {a,b}`, "argv", 1, wordFlags{glob: true}},
		{`echo x{1..3}`, "argv", 1, wordFlags{glob: true}},
		{`echo {0}`, "argv", 1, wordFlags{}},
		{`echo {"a,b"}`, "argv", 1, wordFlags{}},
		{`echo {a,"b c"}`, "argv", 1, wordFlags{glob: true}},
		{`echo {a,b`, "argv", 1, wordFlags{}},
		{`echo \{a,b}`, "argv", 1, wordFlags{}},
		{`echo "x"[ab]`, "argv", 1, wordFlags{glob: true}},
	}
	for _, c := range cases {
		seg := parseSegment(c.in)
		var flags []wordFlags
		switch c.where {
		case "argv":
			flags = seg.argvFlags
		case "redirect":
			flags = seg.redirectFlags
		case "input":
			flags = seg.inputFlags
		}
		if c.i >= len(flags) {
			t.Errorf("parseSegment(%q) 的 %s 标志只有 %d 个", c.in, c.where, len(flags))
			continue
		}
		if flags[c.i] != c.want {
			t.Errorf("parseSegment(%q) %s[%d] 标志 = %+v, 想要 %+v", c.in, c.where, c.i, flags[c.i], c.want)
		}
	}
}

// TestParseSegmentFlagsParallel 标志切片与 argv、redirects、inputs 一一对应。
func TestParseSegmentFlagsParallel(t *testing.T) {
	for _, in := range []string{
		`FOO=1 env A=$B ls *.go > "$OUT" 2>/dev/null < ~/in`,
		`cmd 2>&1 >&2 <&3`,
		`echo $(date) > x`,
		`cat <<< "$X" < f`,
		``,
	} {
		seg := parseSegment(in)
		if len(seg.argvFlags) != len(seg.argv) || len(seg.redirectFlags) != len(seg.redirects) || len(seg.inputFlags) != len(seg.inputs) {
			t.Errorf("parseSegment(%q) 标志长度 %d/%d/%d 与 %d/%d/%d 不一致", in,
				len(seg.argvFlags), len(seg.redirectFlags), len(seg.inputFlags),
				len(seg.argv), len(seg.redirects), len(seg.inputs))
		}
	}
}
