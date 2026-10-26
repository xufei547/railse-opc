package hooksconf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Codex 只认一条 notify（~/.codex/config.toml 的顶层键，argv 数组，Codex 把通知 JSON 追加为最后一个参数）。
// railse setup codex-notify 生成一个串接脚本，依次调用原有的 notify 与 railse，再把 notify 指向它；
// railse doctor 用同样的解析看 notify 装没装好。

// CodexConfigPath 是 Codex 的用户级配置：notify 写在它的顶层。
func CodexConfigPath(home string) string { return filepath.Join(home, ".codex", "config.toml") }

// NotifyScriptName 是串接脚本的文件名，脚本放在守护进程的数据目录里。
const NotifyScriptName = "codex-notify.sh"

// notifyMarker 是串接脚本里记前一跳的标记行的前缀，后面是单行的 JSON 数组。
const notifyMarker = "# railse-prev-notify: "

// CodexNotify 是从 config.toml 里认出的 notify。
type CodexNotify struct {
	Found  bool     // 顶层（第一个表头之前）有 notify
	Argv   []string // 顶层 notify 数组的元素，已按 TOML 解开转义
	Tables []string // 写在表下面的 notify 所在的表头原文（如 [mcp_servers.foo]），按出现顺序：Codex 读不到它们

	start, end int // 顶层 notify 从键到 ] 的字节范围
}

// ParseCodexNotify 解析 config.toml 的内容（空内容当作空文件），找出 notify。只认第一个表头之前的顶层裸键 notify，
// 值必须是数组，元素只能是单行的基本字符串（"…"，按 TOML 转义）或字面字符串（'…'），可以跨行、有 # 注释、逗号结尾。
// 别的键的值只跳过、不解析（多行字符串、数组、内联表里像表头或 notify 的内容不算）。看不懂的写法（notify 用了多行字符串、
// 非字符串元素、引号键或点分键、写了两次，数组没闭合，或文件别处不是 TOML 的写法）返回带行号的错误，调用方据此什么都不改。
func ParseCodexNotify(doc []byte) (CodexNotify, error) {
	var n CodexNotify
	s := &tomlScanner{b: doc}
	table := "" // 当前所在的表头；空是顶层
	for {
		s.skipBlank()
		if s.eof() {
			return n, nil
		}
		if s.peek() == '[' {
			h, err := s.header()
			if err != nil {
				return CodexNotify{}, err
			}
			if err := s.endOfLine(); err != nil {
				return CodexNotify{}, err
			}
			table = h
			continue
		}
		start := s.i
		parts, plain, err := s.key()
		if err != nil {
			return CodexNotify{}, err
		}
		if s.peek() != '=' {
			return CodexNotify{}, s.errorf("键后面缺 =")
		}
		s.i++
		s.skipSpace()
		switch {
		case table == "" && parts[0] == "notify":
			if !plain {
				return CodexNotify{}, s.errorf("notify 写成了引号键或点分键")
			}
			if n.Found {
				return CodexNotify{}, s.errorf("notify 写了两次")
			}
			argv, err := s.stringArray()
			if err != nil {
				return CodexNotify{}, err
			}
			n.Found, n.Argv, n.start, n.end = true, argv, start, s.i
		default:
			if table != "" && plain && parts[0] == "notify" {
				n.Tables = append(n.Tables, table)
			}
			if err := s.skipValue(); err != nil {
				return CodexNotify{}, err
			}
		}
		if err := s.endOfLine(); err != nil {
			return CodexNotify{}, err
		}
	}
}

// SetCodexNotify 把 config.toml 顶层的 notify 换成 notify = [argv…]（TOML 基本字符串）：有顶层 notify 时只换从键到 ] 那一段，
// 没有时（只写在表下面的不算）在文件最前面插一行；其余字节不变。doc 看不懂时（见 ParseCodexNotify）返回错误。
func SetCodexNotify(doc []byte, argv []string) ([]byte, error) {
	n, err := ParseCodexNotify(doc)
	if err != nil {
		return nil, err
	}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = tomlQuote(a)
	}
	line := "notify = [" + strings.Join(quoted, ", ") + "]"
	if !n.Found {
		return append([]byte(line+"\n"), doc...), nil
	}
	out := make([]byte, 0, len(doc)+len(line))
	out = append(out, doc[:n.start]...)
	out = append(out, line...)
	return append(out, doc[n.end:]...), nil
}

// tomlQuote 把 s 写成 TOML 基本字符串：转义 "、\ 与控制字符。
func tomlQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// NotifyScript 生成串接脚本：Codex 把通知 JSON 作为最后一个参数交给 notify，脚本把收到的参数（"$@"）依次交给前一跳 prev
// （原来的 notify 数组；为空时没有这一行）与“<bin> hook codex-notify”（socket 见 HookCommand）。参数都经 ShellQuote；
// 不用 set -e，一个失败不影响另一个，最后总是 exit 0。标记行记下 prev（单行 JSON，不转义 HTML 字符），重跑时据此找回前一跳。
// 同样的输入生成同样的字节。
func NotifyScript(prev []string, bin, socket string) []byte {
	if prev == nil {
		prev = []string{} // 标记行写 []，不写 null
	}
	var j bytes.Buffer
	enc := json.NewEncoder(&j)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(prev) // []string 编码不会失败
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("# 由 railse setup codex-notify 生成，重跑会重新生成，不要手动改。\n")
	b.WriteString("# Codex 把通知 JSON 作为最后一个参数传给 notify 命令；这里原样（\"$@\"）交给下面每个命令，一个失败不影响另一个。\n")
	b.WriteString(notifyMarker + strings.TrimSuffix(j.String(), "\n") + "\n")
	if len(prev) > 0 {
		for _, a := range prev {
			b.WriteString(ShellQuote(a) + " ")
		}
		b.WriteString("\"$@\"\n")
	}
	b.WriteString(HookCommand(bin, "codex-notify", socket) + " \"$@\"\n")
	b.WriteString("exit 0\n")
	return []byte(b.String())
}

// ParseNotifyScript 读串接脚本的标记行：marked 报告有没有标记行（是不是 railse setup codex-notify 生成的），prev 是里面记的前一跳；
// 标记行的 JSON 解不开时 marked 为真并返回错误。
func ParseNotifyScript(script []byte) (prev []string, marked bool, err error) {
	for _, line := range strings.Split(string(script), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSuffix(line, "\r"), notifyMarker)
		if !ok {
			continue
		}
		if err := json.Unmarshal([]byte(rest), &prev); err != nil {
			return nil, true, fmt.Errorf("标记行 %s不是 JSON 字符串数组（%w）", notifyMarker, err)
		}
		return prev, true, nil
	}
	return nil, false, nil
}

// NotifyScriptHook 认出串接脚本里 railse 那一行“<railse> hook codex-notify [--socket <路径>] "$@"”（见 NotifyScript），
// 返回其中的程序与 socket；有多行时取最后一行（railse 那一行在前一跳之后）。
func NotifyScriptHook(script []byte) (Command, bool) {
	var found Command
	ok := false
	for _, line := range strings.Split(string(script), "\n") {
		cmd, has := strings.CutSuffix(strings.TrimSuffix(line, "\r"), ` "$@"`)
		if !has {
			continue
		}
		if c, match := parseHook(cmd, "codex-notify"); match {
			found, ok = c, true
		}
	}
	return found, ok
}

// tomlScanner 逐条读 config.toml 的语句（表头或键值对），只为找 notify：别的键的值只跳过，不解析也不校验内容。
type tomlScanner struct {
	b []byte
	i int
}

// errorf 给错误带上当前所在的行号。
func (s *tomlScanner) errorf(format string, a ...any) error {
	line := bytes.Count(s.b[:s.i], []byte("\n")) + 1
	return fmt.Errorf("第 %d 行："+format, append([]any{line}, a...)...)
}

func (s *tomlScanner) eof() bool { return s.i >= len(s.b) }

func (s *tomlScanner) peek() byte {
	if s.eof() {
		return 0
	}
	return s.b[s.i]
}

// skipSpace 跳过行内的空格与制表符。
func (s *tomlScanner) skipSpace() {
	for !s.eof() && (s.b[s.i] == ' ' || s.b[s.i] == '\t') {
		s.i++
	}
}

// skipComment 跳过 # 到行尾的注释（不含换行）。
func (s *tomlScanner) skipComment() {
	if s.peek() != '#' {
		return
	}
	for !s.eof() && s.b[s.i] != '\n' {
		s.i++
	}
}

// newline 吃掉一个换行（\n 或 \r\n），报告有没有。
func (s *tomlScanner) newline() bool {
	switch {
	case bytes.HasPrefix(s.b[s.i:], []byte("\r\n")):
		s.i += 2
	case s.peek() == '\n':
		s.i++
	default:
		return false
	}
	return true
}

// skipBlank 跳过空白、注释与换行（语句之间、数组的元素之间）。
func (s *tomlScanner) skipBlank() {
	for {
		s.skipSpace()
		s.skipComment()
		if !s.newline() {
			return
		}
	}
}

// endOfLine 要求一条语句后面只剩空白与注释，并吃掉行尾的换行（文件末尾也行）。
func (s *tomlScanner) endOfLine() error {
	s.skipSpace()
	s.skipComment()
	if s.eof() || s.newline() {
		return nil
	}
	return s.errorf("值后面还有别的内容")
}

func isBareKey(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// key 读一个键：裸键或引号键，可以用 . 连成点分键。返回各段（已解开引号）与它是不是单个裸键；s.i 停在键后的空白之后。
func (s *tomlScanner) key() (parts []string, plain bool, err error) {
	plain = true
	for {
		s.skipSpace()
		switch c := s.peek(); {
		case c == '"' || c == '\'':
			if s.triple() {
				return nil, false, s.errorf("键不能是多行字符串")
			}
			p, err := s.str()
			if err != nil {
				return nil, false, err
			}
			parts, plain = append(parts, p), false
		case isBareKey(c):
			start := s.i
			for !s.eof() && isBareKey(s.b[s.i]) {
				s.i++
			}
			parts = append(parts, string(s.b[start:s.i]))
		default:
			return nil, false, s.errorf("认不出键")
		}
		s.skipSpace()
		if s.peek() != '.' {
			return parts, plain && len(parts) == 1, nil
		}
		s.i++
	}
}

// header 读表头 [x] 或 [[x]]，返回原文。
func (s *tomlScanner) header() (string, error) {
	start := s.i
	closing := "]"
	if bytes.HasPrefix(s.b[s.i:], []byte("[[")) {
		closing = "]]"
	}
	s.i += len(closing)
	if _, _, err := s.key(); err != nil {
		return "", err
	}
	if !bytes.HasPrefix(s.b[s.i:], []byte(closing)) {
		return "", s.errorf("表头没闭合")
	}
	s.i += len(closing)
	return string(s.b[start:s.i]), nil
}

// triple 报告 s.i 处是不是多行字符串的开头（三个双引号或三个单引号）。
func (s *tomlScanner) triple() bool {
	q := s.peek()
	return bytes.HasPrefix(s.b[s.i:], []byte{q, q, q})
}

// str 读一个单行字符串：基本字符串按 TOML 解开转义，字面字符串原样。s.i 停在它后面。
func (s *tomlScanner) str() (string, error) {
	q := s.b[s.i]
	s.i++
	var out strings.Builder
	for {
		if s.eof() || s.b[s.i] == '\n' {
			return "", s.errorf("字符串没闭合")
		}
		c := s.b[s.i]
		switch {
		case c == q:
			s.i++
			return out.String(), nil
		case c == '\\' && q == '"':
			r, err := s.escape()
			if err != nil {
				return "", err
			}
			out.WriteRune(r)
		case c < 0x20 && c != '\t' || c == 0x7f:
			return "", s.errorf("字符串里有控制字符")
		default:
			out.WriteByte(c)
			s.i++
		}
	}
}

// escape 解开基本字符串里 s.i 处的一个转义（TOML 1.0：\b \t \n \f \r \" \\ \uXXXX \UXXXXXXXX）。
func (s *tomlScanner) escape() (rune, error) {
	s.i++
	if s.eof() {
		return 0, s.errorf("字符串没闭合")
	}
	e := s.b[s.i]
	s.i++
	switch e {
	case 'b':
		return '\b', nil
	case 't':
		return '\t', nil
	case 'n':
		return '\n', nil
	case 'f':
		return '\f', nil
	case 'r':
		return '\r', nil
	case '"':
		return '"', nil
	case '\\':
		return '\\', nil
	case 'u', 'U':
		n := 4
		if e == 'U' {
			n = 8
		}
		if s.i+n > len(s.b) {
			return 0, s.errorf("认不出转义 \\%c", e)
		}
		v, err := strconv.ParseUint(string(s.b[s.i:s.i+n]), 16, 32)
		if err != nil || !utf8.ValidRune(rune(v)) {
			return 0, s.errorf("认不出转义 \\%c%s", e, s.b[s.i:s.i+n])
		}
		s.i += n
		return rune(v), nil
	}
	return 0, s.errorf("认不出转义 \\%c", e)
}

// skipStr 跳过一个字符串（含多行字符串），不解开内容。
func (s *tomlScanner) skipStr() error {
	q := s.b[s.i]
	if !s.triple() {
		_, err := s.str()
		return err
	}
	start := s.i
	s.i += 3
	for !s.eof() {
		switch {
		case q == '"' && s.b[s.i] == '\\':
			s.i += 2
		case s.triple() && s.b[s.i] == q:
			s.i += 3
			for k := 0; k < 2 && s.peek() == q; k++ { // 结尾最多还能带两个引号，算内容
				s.i++
			}
			return nil
		default:
			s.i++
		}
	}
	s.i = start
	return s.errorf("多行字符串没闭合")
}

// skipValue 跳过一个值：字符串、数组、内联表，或数字、布尔、日期时间（到注释或行尾为止）。
func (s *tomlScanner) skipValue() error {
	switch s.peek() {
	case '"', '\'':
		return s.skipStr()
	case '[', '{':
		return s.skipNested()
	}
	start := s.i
	for !s.eof() && s.b[s.i] != '#' && s.b[s.i] != '\n' && s.b[s.i] != '\r' {
		s.i++
	}
	if len(bytes.TrimSpace(s.b[start:s.i])) == 0 {
		return s.errorf("缺值")
	}
	return nil
}

// skipNested 跳过一个数组或内联表：数到配对的括号为止，跳过其中的字符串与注释。
func (s *tomlScanner) skipNested() error {
	start, depth := s.i, 0
	for !s.eof() {
		switch s.b[s.i] {
		case '"', '\'':
			if err := s.skipStr(); err != nil {
				return err
			}
			continue
		case '#':
			s.skipComment()
			continue
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth == 0 {
				s.i++
				return nil
			}
		}
		s.i++
	}
	s.i = start
	return s.errorf("%c 没闭合", s.b[start])
}

// stringArray 读 notify 的值：字符串数组，可以跨行、有注释、逗号结尾；元素只能是单行的基本字符串或字面字符串。
func (s *tomlScanner) stringArray() ([]string, error) {
	if s.peek() != '[' {
		return nil, s.errorf("notify 的值不是数组")
	}
	start := s.i
	s.i++
	argv := []string{}
	for {
		s.skipBlank()
		switch c := s.peek(); {
		case s.eof():
			s.i = start
			return nil, s.errorf("notify 的数组没闭合")
		case c == ']':
			s.i++
			return argv, nil
		case c != '"' && c != '\'':
			return nil, s.errorf("notify 的数组里有不是字符串的元素")
		case s.triple():
			return nil, s.errorf("notify 的数组里有多行字符串")
		}
		a, err := s.str()
		if err != nil {
			return nil, err
		}
		argv = append(argv, a)
		s.skipBlank()
		switch {
		case s.eof():
			s.i = start
			return nil, s.errorf("notify 的数组没闭合")
		case s.peek() == ',':
			s.i++
		case s.peek() == ']':
			s.i++
			return argv, nil
		default:
			return nil, s.errorf("notify 的数组里元素之间缺逗号")
		}
	}
}
