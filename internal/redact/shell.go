package redact

import (
	"bytes"
	"strings"
)

// 命令行语境的脱敏分两步。
//
// 第一步是单趟、线性的 shell 扫描：认出单引号、双引号、$'…'、$"…"、反斜杠转义、词首的 # 注释、$(…)、反引号、
// ${…}、$((…))、((…))、$[…]、NAME=(…)、case（只为判断 ) 是否结束 $(…)）和 heredoc。
//   - 引号里、注释是字面文本，就地按 literalRules 整段抹；分隔词带引号的 heredoc 正文按 textRules 整段抹；
//     分隔词不带引号的正文会展开，其中的命令替换按命令处理，其余按 textRules 抹。
//   - 命令替换这类嵌套结构递归按命令行语境处理。
//   - 交给 sh/bash/zsh/dash/ksh -c、eval、ssh 执行的引号参数，以及喂给它们（或经管道喂给它们）的 heredoc 正文与
//     here-string，同样递归按命令行语境处理：里面的命令要露出来，里面引号里的值照旧整段抹。
//   - heredoc 的分隔词与结束行原样保留，从不脱敏。
//
// 只认这些情况需要的词法，不是完整的 shell 解析器。解析按 bash 5（zsh 一样）；不模拟 /bin/bash 3.2 的解析分歧
// （截取 $(…) 时不认 heredoc 与 case）和语法错误恢复（丢掉本行余下的部分、从下一行接着执行），这些是接受的残余风险。
//
// 第二步对本层文本跑 viewRules，处理跨引号的写法（KEY="值"、--password '值'、curl -u "user:值"、mysql -p'值'）。
// 正则不在原文上跑，而在等长的“视图”上跑：右引号换成专用字节，引号里的引号、转义、占位符换成不参与匹配的字节，
// 嵌套结构、注释、heredoc 正文在视图里各是一个字节 vNest。这样正则只能从真正的左引号配到它自己的右引号，
// 裸值在 shell 元字符、嵌套结构和右引号处停，跨不过本层扫描认出的结构，也就藏不住 bash 会执行的命令。
//
// 拿不准时（未闭合的结构、超出层数上限、两种 bash 的分歧），秘密一侧宁可多抹，命令一侧宁可多露。

// 视图里的专用字节。原文里与它们相同的字节在视图里换成 vMask 或 vLit。
const (
	vMask    = 0x00 // 普通字节：引号外的反斜杠与被转义的元字符（裸值穿过它延续），多字节逻辑字符的后续字节
	vNest    = 0x10 // 一个整体：嵌套结构、注释、heredoc 正文、分隔词、交给解释器的参数
	vCloseSQ = 0x11 // 单引号（含 $'…'）的右引号
	vCloseDQ = 0x12 // 双引号（含 $"…"）的右引号
	vPH      = 0x13 // 本层规则插入的占位符的第一个字节
	vLit     = 0x14 // 引号里不参与匹配结构的字节：引号里的引号、<。整段引号值照样能配上，裸值不在这里开始或延续
)

// viewPlaceholder 是占位符在视图里的样子，与 placeholder 等长。
const viewPlaceholder = "\x13redacted>"

// viewStruct 是视图里的结构字节，引号值也不能跨过；viewExcluded 另含占位符和引号里屏蔽的字节，裸值、用户名、URL 密码都排除。
const (
	viewStruct   = `\x10\x11\x12`
	viewExcluded = `\x10\x11\x12\x13\x14`
)

// maxDepth 是嵌套结构的层数上限，超过的不再展开（开头的字符当普通字符），免得对抗输入把递归压得太深。
// maxRec 是“去掉一层转义再扫描”的层数上限（交给解释器的双引号参数、反引号内容、喂给 shell 的 heredoc 正文）：
// 每层都要复制一遍文本，双引号参数还要先空跑一遍找右引号，层数不设限会退化成平方级。超过的不去转义，
// 双引号参数按普通双引号处理，其余直接按原文扫描。
const (
	maxDepth = 64
	maxRec   = 4
)

// viewRules 是命令行视图上的规则：裸值在空白、引号、反斜杠、shell 元字符和视图专用字节处停。
// $ 开始展开，字面密码不可能未加引号地含它；反引号不能写进原始字符串，单独拼进去。
var viewRules = func() []rule {
	s, x := viewStruct, viewExcluded
	q := quoting{
		quoted:  `"[^"` + s + `]*\x12|'[^'` + s + `]*\x11`,
		jsonStr: `"(?:[^"\\` + s + `]|\\[^` + s + `])*\x12`,
		keyEnd:  `\x12`,
		sqUser:  `('[^'":\s\\` + x + `]*:)`, sqPass: `[^'` + s + `]*`, sqEnd: `(\x11)`,
		dqUser: `("[^'":\s\\` + x + `]*:)`, dqPass: `(?:[^"\\` + s + `]|\\[^` + s + `])*`, dqEnd: `(\x12)`,
		jqEnd: `(\\")`, user: `([^'":\s\\` + x + `]*:)`,
		// 同一条命令：引号按视图成对；嵌套结构（vNest）算在同一条命令里
		sameCmd:  `(?:[^|;&\n'"\x11\x12]|'[^'\x11\x12]*\x11|"[^"\x11\x12]*\x12|&[^&\n'"\x11\x12]|\\\r?\n)*?`,
		urlUser:  `[^/\s:@'"\\` + x + `]*`,
		urlPass:  "[^\\s/?#'\"\\\\;|&<>()`$" + x + "]+",
		excluded: x,
		ph:       `[<\x13\x14]redacted>`, // 原文里的、本层插入的、引号里已抹过的占位符
		word:     true,
	}
	rules := build("[^\\s'\"\\\\;|&<>()`$"+x+"]+", q)
	for i := range rules {
		if rules[i].vrepl == "" {
			rules[i].vrepl = strings.ReplaceAll(rules[i].repl, placeholder, viewPlaceholder)
		}
	}
	return rules
}()

// shellString 是 String 的实现。
func shellString(s string) string {
	sc := &scanner{t: &text{raw: s}}
	return sc.context(len(s), mShell, 0)
}

// text 是一层 shell 看到的逻辑文本。顶层就是原文；交给解释器的双引号参数里，外层 shell 先去掉 \" \\ \$ \` 的反斜杠，
// 里层看到的是去掉之后的文本，ch 是逻辑字符，rs[i] 是逻辑字符 i 在原文里的起点（rs[len(ch)] 是终点）。
type text struct {
	raw string
	ch  []byte // nil 表示逻辑文本就是原文
	rs  []int
}

func (t *text) n() int {
	if t.ch == nil {
		return len(t.raw)
	}
	return len(t.ch)
}

func (t *text) at(i int) byte {
	if t.ch == nil {
		return t.raw[i]
	}
	return t.ch[i]
}

func (t *text) off(i int) int {
	if t.ch == nil {
		return i
	}
	return t.rs[i]
}

// slice 返回逻辑区间 [i, j) 对应的原文。
func (t *text) slice(i, j int) string { return t.raw[t.off(i):t.off(j)] }

// has 判断逻辑文本从 i 起是否是 s。
func (t *text) has(i int, s string) bool {
	if i+len(s) > t.n() {
		return false
	}
	if t.ch == nil {
		return t.raw[i:i+len(s)] == s
	}
	return string(t.ch[i:i+len(s)]) == s
}

// unquote 返回逻辑区间 [lo, hi) 去掉转义后的文本：\ 后面是 esc 里的字符时留下后一个字符，\ 加换行整个去掉
// （它的原文并进下一个字符）。双引号里 esc 是 dqEscapes，不带引号分隔词的 heredoc 正文里是 bodyEscapes。去掉后为空时返回 nil。
func (t *text) unquote(lo, hi int, esc string) *text {
	u := &text{raw: t.raw}
	gap := -1
	add := func(c byte, off int) {
		if gap >= 0 {
			off, gap = gap, -1
		}
		u.ch = append(u.ch, c)
		u.rs = append(u.rs, off)
	}
	for i := lo; i < hi; {
		c := t.at(i)
		if c == '\\' && i+1 < hi {
			switch nx := t.at(i + 1); nx {
			case '\n':
				if gap < 0 {
					gap = t.off(i)
				}
				i += 2
				continue
			default:
				if strings.IndexByte(esc, nx) >= 0 {
					add(nx, t.off(i))
					i += 2
					continue
				}
			}
		}
		add(c, t.off(i))
		i++
	}
	if len(u.ch) == 0 {
		return nil
	}
	u.rs = append(u.rs, t.off(hi))
	return u
}

const (
	dqEscapes   = "\"\\$`"
	bodyEscapes = "\\$`"
)

// builder 收集一层 shell 文本：orig 是就地脱敏后的原文，view 是与它等长的视图，nests 依次是视图里每个 vNest 换回的文本。
type builder struct {
	orig, view []byte
	nests      []string
}

// char 追加逻辑字符 i 的原文，视图里第一个字节是 rep，其余是 vMask。
func (b *builder) char(t *text, i int, rep byte) {
	if t.ch == nil {
		b.orig = append(b.orig, t.raw[i])
		b.view = append(b.view, rep)
		return
	}
	s := t.slice(i, i+1)
	b.orig = append(b.orig, s...)
	b.view = append(b.view, rep)
	for k := 1; k < len(s); k++ {
		b.view = append(b.view, vMask)
	}
}

// closing 同 char，但 rep 放在最后一个字节（右引号）；前面的字节（\" 的反斜杠）是 vLit，不算进前面的裸值。
func (b *builder) closing(t *text, i int, rep byte) {
	s := t.slice(i, i+1)
	b.orig = append(b.orig, s...)
	for k := 1; k < len(s); k++ {
		b.view = append(b.view, vLit)
	}
	b.view = append(b.view, rep)
}

// literal 追加引号里已脱敏的字面文本：视图里引号、<（占位符的开头）和视图专用字节换成 vLit。
func (b *builder) literal(s string) {
	b.orig = append(b.orig, s...)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\'' || c == '"' || c == '<' || isViewByte(c) {
			c = vLit
		}
		b.view = append(b.view, c)
	}
}

// nest 追加一个整体：视图里是一个 vNest，规则跑完后换回 s。
func (b *builder) nest(s string) {
	b.orig = append(b.orig, vNest)
	b.view = append(b.view, vNest)
	b.nests = append(b.nests, s)
}

func isViewByte(c byte) bool { return c >= vNest && c <= vLit }

// viewByte 是引号外普通字符在视图里的样子。
func viewByte(c byte) byte {
	if isViewByte(c) {
		return vMask
	}
	return c
}

// finish 在视图上跑 viewRules，把 vNest 换回各自的文本，返回本层脱敏后的原文。
// 重定向 < 后面紧跟本层插入的占位符时补一个空格：否则结果里是 <<redacted>，hub 再脱敏一次时会把它读成 heredoc。
func (b *builder) finish() string {
	orig, view := applyView(b.orig, b.view)
	var out strings.Builder
	out.Grow(len(orig))
	k, last := 0, 0
	for i := 0; i < len(view); i++ {
		switch view[i] {
		case vNest:
			out.Write(orig[last:i])
			out.WriteString(b.nests[k])
			k++
			last = i + 1
		case vPH:
			if i > 0 && view[i-1] == '<' {
				out.Write(orig[last:i])
				out.WriteByte(' ')
				last = i
			}
		}
	}
	out.Write(orig[last:])
	return out.String()
}

// applyView 依次跑 viewRules：在视图上找匹配，原文与视图按同样的位置替换，两者始终等长。
// 规则的值字符类都排除 vNest，替换文本里也没有 vNest，所以 vNest 的个数与顺序不变。
func applyView(orig, view []byte) ([]byte, []byte) {
	for _, r := range viewRules {
		if !bytes.ContainsAny(view, r.need) {
			continue
		}
		ms := r.re.FindAllSubmatchIndex(view, -1)
		if ms == nil {
			continue
		}
		repl, vrepl := []byte(r.repl), []byte(r.vrepl)
		no := make([]byte, 0, len(orig)+len(placeholder)*len(ms))
		nv := make([]byte, 0, cap(no))
		last := 0
		for _, m := range ms {
			no = append(no, orig[last:m[0]]...)
			nv = append(nv, view[last:m[0]]...)
			no = r.re.Expand(no, repl, orig, m)
			nv = r.re.Expand(nv, vrepl, view, m)
			last = m[1]
		}
		orig = append(no, orig[last:]...)
		view = append(nv, view[last:]...)
	}
	return orig, view
}

// scanner 在一层逻辑文本上前进；嵌套结构共用同一个 scanner，各自用一个 ctx。
type scanner struct {
	t     *text
	i     int
	dry   bool // 只为找结束位置空跑，不脱敏
	depth int
	rec   int
}

// lit 按 rules 脱敏一段字面文本（空跑时原样返回）。
func (sc *scanner) lit(s string, rules []rule) string {
	if sc.dry {
		return s
	}
	return redact(s, rules)
}

type mode uint8

const (
	mShell mode = iota // 命令：引号、注释、heredoc、命令替换
	mParam             // ${…}：引号与嵌套结构，没有注释和 heredoc
	mArith             // $((…))、((…))、$[…]：同上，括号计数
)

// heredoc 是一个待读正文的 heredoc。
type heredoc struct {
	delim  string
	strip  bool // <<-：正文与结束行去掉前导制表符
	quoted bool // 分隔词带引号：正文不展开，反斜杠续行也不算
	feed   bool // 喂给 shell 或 ssh：正文按命令行语境递归处理
}

// ctx 是一层 shell 文本的扫描状态。
type ctx struct {
	sc     *scanner
	b      builder
	mode   mode
	closer byte // 结束字符：')' '}' ']'；0 表示到 hi 为止
	hi     int
	paren  int  // 本层未闭合的 ( 或 [
	cases  int  // 本层未结束的 case：其中的 ) 是分支模式的结尾，不结束 $(…)
	start  bool // 下一个字符在词首（# 在这里开始注释）
	inWord bool
	// 当前词去掉引号与转义后的文本：word 是开头至多 64 字节（判断 VAR=值、-c 类选项、保留字），base 是最后一个 /
	// 之后至多 64 字节（判断命令名，路径多长都行）。超出时 long、baseLong 为 true
	word, base     []byte
	long, baseLong bool
	quoted         bool // 词里有引号、转义或嵌套结构：不是保留字（"case"、/case 都不是 case）
	cmdPos         bool // 下一个词在命令位置
	// 包装命令（sudo、env、timeout……）之后：wrap 是包装命令名，wrapArg 表示下一个词是它的选项参数，
	// wrapPos 是还要跳过的位置参数（timeout 的时长）
	wrap    string
	wrapArg bool
	wrapPos int
	// 当前简单命令里见过的词：shell 名、-c 类选项、eval、ssh；feeds 表示本条命令把输入交给 shell 或 ssh
	shell, cflag, eval, ssh, feeds bool
	here                           bool       // 当前词是喂给 shell 或 ssh 的 here-string
	pipe                           []*heredoc // 当前管道里登记的 heredoc：后面的管道段是 shell 时，正文也是喂给它的
	pending                        []*heredoc // 等下一个换行读正文的 heredoc，按出现顺序
}

// context 从 sc.i 起扫描一层文本，到 closer（不消费）或 hi 为止，返回这层脱敏后的原文。
func (sc *scanner) context(hi int, m mode, closer byte) string {
	c := &ctx{sc: sc, mode: m, closer: closer, hi: hi, start: true, cmdPos: true}
	c.run()
	if sc.dry {
		return ""
	}
	return c.b.finish()
}

// cmd 报告本层是不是命令文本（不是 ${…} 或算术）。
func (c *ctx) cmd() bool { return c.mode == mShell }

func (c *ctx) run() {
	sc, t := c.sc, c.sc.t
	for sc.i < c.hi {
		i := sc.i
		ch := t.at(i)
		switch ch {
		case '\n':
			if c.cmd() {
				c.endWord()
				c.b.char(t, i, '\n')
				sc.i++
				if c.mode == mShell {
					c.bodies()
				}
				c.endCmd(true)
				continue
			}
			c.space()
			continue
		case ' ', '\t':
			c.space()
			continue
		case '#':
			if c.cmd() && c.start {
				c.comment()
				continue
			}
		case '\\':
			c.escape()
			continue
		case '\'':
			c.singleQuoted()
			continue
		case '"':
			c.doubleQuoted(false)
			continue
		case '`':
			c.backtick()
			continue
		case '$':
			if c.dollar(true) {
				continue
			}
		case '<', '>':
			if c.cmd() {
				c.redirect()
				continue
			}
		case '(':
			if c.paren1() {
				continue
			}
		case ')':
			if c.cmd() {
				c.endWord()
				if c.closer == ')' && c.paren == 0 {
					if c.cases == 0 {
						return
					}
				}
				if c.paren > 0 {
					c.paren--
				}
				c.op(1)
				c.endCmd(true)
				continue
			}
			if c.mode == mArith && c.closer == ')' {
				if c.paren == 0 {
					return
				}
				c.paren--
			}
		case '[':
			if c.mode == mArith && c.closer == ']' {
				c.paren++
			}
		case ']':
			if c.mode == mArith && c.closer == ']' {
				if c.paren == 0 {
					return
				}
				c.paren--
			}
		case '}':
			if c.mode == mParam && c.closer == '}' {
				return
			}
		case ';', '&', '|':
			if c.cmd() {
				c.operator(ch)
				continue
			}
		}
		c.wordChar()
	}
}

// wordChar 追加当前字符作为词的一部分。
func (c *ctx) wordChar() {
	sc, t := c.sc, c.sc.t
	ch := t.at(sc.i)
	c.b.char(t, sc.i, viewByte(ch))
	sc.i++
	c.addWord(ch)
}

func (c *ctx) addWord(ch byte) {
	c.inWord, c.start = true, false
	if len(c.word) < 64 {
		c.word = append(c.word, ch)
	} else {
		c.long = true
	}
	switch {
	case ch == '/':
		c.base, c.baseLong = c.base[:0], false
	case len(c.base) < 64:
		c.base = append(c.base, ch)
	default:
		c.baseLong = true
	}
}

// addWordText 把引号里的字面内容或嵌套结构的原文算进当前词。
func (c *ctx) addWordText(s string) {
	c.quoted = true
	if len(s) > 64 {
		c.inWord, c.start, c.long, c.baseLong = true, false, true, true
		return
	}
	for i := 0; i < len(s); i++ {
		c.addWord(s[i])
	}
}

// space 追加空白：词到此结束。
func (c *ctx) space() {
	c.endWord()
	c.b.char(c.sc.t, c.sc.i, viewByte(c.sc.t.at(c.sc.i)))
	c.sc.i++
	c.start = true
}

// op 追加 n 个运算符字符：词到此结束，下一个字符在词首。
func (c *ctx) op(n int) {
	sc, t := c.sc, c.sc.t
	for k := 0; k < n && sc.i < c.hi; k++ {
		c.b.char(t, sc.i, viewByte(t.at(sc.i)))
		sc.i++
	}
	c.start = true
}

// nestDone 追加一个嵌套结构的文本，它是当前词的一部分。
func (c *ctx) nestDone(s string) {
	c.b.nest(s)
	c.addWordNest(s)
}

// addWordNest 把嵌套结构算进当前词：展开的结果不知道，不算进命令名（${x:-}sh 按 sh 认），只有 ${SHELL} 记成 $SHELL。
func (c *ctx) addWordNest(s string) {
	c.inWord, c.start, c.quoted = true, false, true
	if s == "${SHELL}" {
		for i := 0; i < len("$SHELL"); i++ {
			c.base = append(c.base, "$SHELL"[i])
		}
	}
}

// operator 处理 ; & | 及其组合。管道 | 和 |& 把下一段接在同一条管道里；&> 是重定向，不结束命令。
func (c *ctx) operator(ch byte) {
	sc, t := c.sc, c.sc.t
	c.endWord()
	nx := byte(0)
	if sc.i+1 < c.hi {
		nx = t.at(sc.i + 1)
	}
	switch {
	case ch == '|' && nx == '|':
		c.op(2)
		c.endCmd(true)
	case ch == '|':
		if nx == '&' {
			c.op(2)
		} else {
			c.op(1)
		}
		c.endCmd(false)
	case ch == '&' && nx == '>':
		c.op(2)
	default:
		c.op(1)
		c.endCmd(true)
	}
}

// endCmd 结束当前简单命令。pipeEnd 为 false 时只是进入同一条管道的下一段。
func (c *ctx) endCmd(pipeEnd bool) {
	c.shell, c.cflag, c.eval, c.ssh, c.feeds, c.here = false, false, false, false, false, false
	c.wrap, c.wrapArg, c.wrapPos = "", false, 0
	c.cmdPos, c.start = true, true
	if pipeEnd {
		c.pipe = c.pipe[:0]
	}
}

// reservedWords 之后仍在命令位置。
var reservedWords = map[string]bool{"!": true, "{": true, "if": true, "then": true, "else": true, "elif": true,
	"do": true, "while": true, "until": true, "time": true}

// endWord 结束当前词，按词的内容更新命令状态。只有命令位置上的词才当命令名认：命令开头、VAR=值 之后、
// 包装命令及其选项之后、find 的 -exec 之后。参数里出现的 bash、eval、ssh 不算（echo ssh '…'）。
func (c *ctx) endWord() {
	if !c.inWord {
		return
	}
	w, long := string(c.word), c.long
	name, kw := "", ""
	if !c.baseLong {
		name = string(c.base)
	}
	if !long && !c.quoted {
		kw = w
	}
	c.word, c.base, c.inWord, c.long, c.baseLong, c.here, c.quoted = c.word[:0], c.base[:0], false, false, false, false, false
	if c.mode != mShell {
		return
	}
	switch {
	case c.cmdPos:
		if reservedWords[kw] || !long && isAssign(w) {
			return
		}
		c.cmdPos = false
		c.command(name, kw)
	case c.wrap != "":
		switch {
		case c.wrapArg:
			c.wrapArg = false
		case len(w) > 1 && w[0] == '-':
			c.wrapArg = len(w) == 2 && strings.IndexByte(wrapperArgs[c.wrap], w[1]) >= 0
		case c.wrap == "env" && isAssign(w):
		case c.wrapPos > 0:
			c.wrapPos--
		default:
			c.wrap = ""
			c.command(name, kw)
		}
	case w == "-exec" || w == "-execdir" || w == "-ok" || w == "-okdir":
		c.cmdPos = true
	case c.shell && !long && isCFlag(w):
		c.cflag = true
	}
}

// wrapperArgs 是包装命令的短选项里带参数的那些（sudo -u root bash）。只列常见的；认不出时把参数当命令名，
// 结果是不当解释器、引号里的值整段抹，这是可接受的失败方向。
var wrapperArgs = map[string]string{
	"sudo": "ughpCDRTUrt", "env": "uCS", "xargs": "InPLdEsa", "nice": "n", "timeout": "sk", "exec": "a",
	"command": "", "builtin": "", "nohup": "",
}

// command 按命令位置上的词更新状态：name 是去掉引号、取路径最后一段的命令名，kw 是不带引号与转义的原词（认保留字用）。
func (c *ctx) command(name, kw string) {
	switch kw {
	case "case":
		c.cases++
		return
	case "esac":
		if c.cases > 0 {
			c.cases--
		}
		return
	}
	switch name {
	case "sh", "bash", "zsh", "dash", "ksh", "$SHELL", "${SHELL}":
		c.shell = true
		c.feed()
	case "ssh":
		c.ssh = true
		c.feed()
	case "eval":
		// eval 的参数会执行，本身也是一条命令：下一个词按命令名认（eval sh <<E 的正文喂给 sh）
		c.eval = true
		c.wrap, c.wrapArg, c.wrapPos = "eval", false, 0
	default:
		if _, ok := wrapperArgs[name]; ok {
			c.wrap, c.wrapArg, c.wrapPos = name, false, 0
			if name == "timeout" {
				c.wrapPos = 1
			}
		}
	}
}

// feed 标记本条命令把输入交给 shell 或 ssh：当前管道里已登记的 heredoc 正文都是喂给它的。
func (c *ctx) feed() {
	c.feeds = true
	for _, h := range c.pipe {
		h.feed = true
	}
}

// isCFlag 判断 w 是不是含 c 的短选项组合（-c、-lc、-ec、-xc）。
func isCFlag(w string) bool {
	if len(w) < 2 || w[0] != '-' || w[1] == '-' {
		return false
	}
	for i := 1; i < len(w); i++ {
		if !(w[i] >= 'a' && w[i] <= 'z' || w[i] >= 'A' && w[i] <= 'Z') {
			return false
		}
	}
	return strings.IndexByte(w, 'c') > 0
}

// isAssign 判断 w 是不是 NAME=… 或 NAME+=… 形式的变量赋值。
func isAssign(w string) bool {
	eq := strings.IndexByte(w, '=')
	name := w[:max(eq, 0)]
	name = strings.TrimSuffix(name, "+")
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		ch := name[i]
		if !(ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || i > 0 && ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

// recArgs 报告当前命令的引号参数是否会交给解释器执行：sh/bash/zsh/dash/ksh 带 -c 类选项、eval、ssh。
func (c *ctx) recArgs() bool {
	return c.mode == mShell && (c.shell && c.cflag || c.eval || c.ssh || c.here) && !c.sc.dry && c.sc.depth < maxDepth
}

// comment 处理从 # 到行尾（不含换行）的注释：整段按字面脱敏。
func (c *ctx) comment() {
	sc, t := c.sc, c.sc.t
	start, k := sc.i, sc.i
	for k < c.hi && t.at(k) != '\n' {
		k++
	}
	sc.i = k
	c.b.nest(sc.lit(t.slice(start, k), literalRules))
}

// escape 处理引号外的反斜杠。\ 加换行是续行，不改变词的状态；被转义的字符是字面字符，裸值不在它那里停
// （password=abc\;rm 的值是 abc;rm），所以视图里反斜杠是 vMask，被转义的元字符也换成 vMask。
func (c *ctx) escape() {
	sc, t := c.sc, c.sc.t
	i := sc.i
	if i+1 >= c.hi {
		c.wordChar()
		return
	}
	nx := t.at(i + 1)
	switch {
	case nx == '\n':
		c.b.char(t, i, vMask)
		c.b.char(t, i+1, vMask)
		sc.i += 2
		return
	case nx == '\r' && i+2 < c.hi && t.at(i+2) == '\n':
		for k := 0; k < 3; k++ {
			c.b.char(t, i+k, vMask)
		}
		sc.i += 3
		return
	case c.cmd() && t.has(i+1, placeholder):
		// 上一次脱敏留下的 \<redacted>：占位符当词的一部分，免得第二次把 > 后面读成注释
		c.b.char(t, i, vMask)
		sc.i++
		c.placeholderWord()
		return
	}
	meta := isViewByte(nx) || strings.IndexByte(" \t\r\n'\"\\;|&<>()`$", nx) >= 0
	if meta && c.recArgs() {
		// 交给 eval、sh -c、ssh 的参数：外层去掉反斜杠后 \; 就是命令分隔符，值在这里停，后面的命令露出来
		c.b.char(t, i, '\\')
		c.b.char(t, i+1, viewByte(nx))
	} else {
		c.b.char(t, i, vMask)
		v := nx
		if meta {
			v = vMask
		}
		c.b.char(t, i+1, v)
	}
	sc.i += 2
	c.addWord(nx)
	c.quoted = true
}

// placeholderWord 把 sc.i 处的 <redacted> 当作词的一部分（不是重定向）。
func (c *ctx) placeholderWord() {
	sc, t := c.sc, c.sc.t
	for k := 0; k < len(placeholder); k++ {
		c.b.char(t, sc.i, viewByte(t.at(sc.i)))
		c.addWord(t.at(sc.i))
		sc.i++
	}
}

// redirect 处理引号外的 < 和 >：进程替换 <(…)、here-string <<<、heredoc <<、占位符、普通重定向。
func (c *ctx) redirect() {
	sc, t := c.sc, c.sc.t
	i := sc.i
	ch := t.at(i)
	nx := byte(0)
	if i+1 < c.hi {
		nx = t.at(i + 1)
	}
	switch {
	case nx == '(' && sc.depth < maxDepth:
		c.endWord()
		c.nestDone(sc.nestedText(c.hi, 2, mShell, ')', 1))
		return
	case ch == '<' && nx == '<':
		if i+2 < c.hi && t.at(i+2) == '<' {
			c.endWord()
			c.op(3)
			c.here = c.feeds // here-string 喂给 shell 或 ssh：它的引号参数会执行
			return
		}
		if c.mode == mShell && c.heredocOp() {
			return
		}
		c.endWord()
		c.op(2)
		return
	case ch == '<' && t.has(i, placeholder):
		c.placeholderWord()
		return
	}
	c.endWord()
	c.op(1)
	if sc.i < c.hi {
		if nx := t.at(sc.i); nx == '&' || nx == '>' || nx == '|' || ch == '<' && nx == '>' {
			c.op(1)
		}
	}
}

// heredocOp 处理 << 或 <<-：读出分隔词（引号去掉：E"OF" 是 EOF），登记等下一个换行读正文。分隔词原样保留。
// << 后面没有分隔词时返回 false。
func (c *ctx) heredocOp() bool {
	sc, t := c.sc, c.sc.t
	i := sc.i
	j := i + 2
	strip := j < c.hi && t.at(j) == '-'
	if strip {
		j++
	}
	k := j
	for k < c.hi && (t.at(k) == ' ' || t.at(k) == '\t') {
		k++
	}
	delim, quoted, w := c.readDelim(k)
	if w == k {
		return false
	}
	c.endWord()
	c.op(k - i)
	c.b.nest(t.slice(k, w))
	sc.i = w
	c.inWord, c.start = false, false
	h := &heredoc{delim: delim, strip: strip, quoted: quoted, feed: c.feeds}
	c.pipe = append(c.pipe, h)
	c.pending = append(c.pending, h)
	return true
}

// readDelim 从 k 起读 heredoc 分隔词，返回去掉引号后的分隔词、是否带引号、词尾位置。
// 分隔词里的 $(…)、${…}、反引号照原样算进分隔词（可以跨行），$'…'、$"…" 去掉引号。
func (c *ctx) readDelim(k int) (string, bool, int) {
	sc, t := c.sc, c.sc.t
	var d []byte
	quoted := false
	w := k
	for w < c.hi {
		ch := t.at(w)
		nx := byte(0)
		if w+1 < c.hi {
			nx = t.at(w + 1)
		}
		switch {
		case strings.IndexByte(" \t\n;&|<>()", ch) >= 0:
			return string(d), quoted, w
		case ch == '$' && (nx == '\'' || nx == '"'):
			w++
			continue
		case ch == '\'' || ch == '"':
			quoted = true
			w++
			for w < c.hi && t.at(w) != ch {
				if ch == '"' && t.at(w) == '\\' && w+1 < c.hi && strings.IndexByte("\"\\$`", t.at(w+1)) >= 0 {
					w++
				}
				d = append(d, t.at(w))
				w++
			}
			if w < c.hi {
				w++
			}
			continue
		case ch == '\\' && nx != 0:
			if nx != '\n' {
				quoted = true
				d = append(d, nx)
			}
			w += 2
			continue
		case (ch == '$' && strings.IndexByte("({[", nx) >= 0 || ch == '`') && sc.depth < maxDepth:
			dry := &scanner{t: t, i: w, dry: true, depth: sc.depth}
			if ch == '`' {
				dry.backtickText(c.hi)
			} else {
				dry.dollarText(c.hi)
			}
			for ; w < dry.i; w++ {
				d = append(d, t.at(w))
			}
			continue
		}
		d = append(d, ch)
		w++
	}
	return string(d), quoted, w
}

// bodies 在换行之后依次读出待读 heredoc 的正文：到单独一行的分隔词为止（<<- 时允许前导制表符，容忍行尾 \r），
// 分隔词不带引号时以反斜杠结尾的行与下一行相连，下一行不算结束行；找不到结束行时正文到末尾。
//   - 喂给 shell 或 ssh 的正文递归按命令行语境处理；
//   - 分隔词带引号的正文不展开，整段按 textRules 抹；
//   - 分隔词不带引号的正文会展开：其中的 $(…)、反引号等命令替换会执行，按命令行语境处理，其余按 textRules 抹。
//
// 结束行原样保留。$(…) 里的 heredoc 照 bash 5 读到结束行为止（bash 3.2 会在正文里第一个配不上的 ) 处结束替换，不模拟）。
func (c *ctx) bodies() {
	sc, t := c.sc, c.sc.t
	for _, h := range c.pending {
		start := sc.i
		closeAt, closeEnd := -1, -1
		cont := false
		for pos := start; pos < c.hi; {
			e := pos
			for e < c.hi && t.at(e) != '\n' {
				e++
			}
			if !cont && h.closes(t, pos, e) {
				closeAt, closeEnd = pos, e
				break
			}
			cont = !h.quoted && oddBackslashes(t, pos, e)
			pos = e + 1
		}
		end := c.hi
		if closeAt >= 0 {
			end = closeAt
		}
		shell := h.feed && sc.depth < maxDepth
		bodyEnd := end
		sc.i = start
		switch {
		case bodyEnd == start:
		case shell && !h.quoted && sc.rec < maxRec && hasBackslash(t, start, bodyEnd):
			// 分隔词不带引号：外层 shell 先展开正文（\\ \$ \` 去掉反斜杠、\ 加换行去掉），shell 读到的是展开后的文本
			if u := t.unquote(start, bodyEnd, bodyEscapes); u != nil {
				sub := &scanner{t: u, dry: sc.dry, depth: sc.depth + 1, rec: sc.rec + 1}
				c.b.nest(sub.context(u.n(), mShell, 0))
			} else {
				c.b.nest(t.slice(start, bodyEnd))
			}
		case shell:
			sc.depth++
			c.b.nest(sc.context(bodyEnd, mShell, 0))
			sc.depth--
		case h.quoted:
			c.b.nest(sc.lit(t.slice(start, bodyEnd), textRules))
		default:
			c.b.nest(sc.expandBody(bodyEnd))
		}
		sc.i = end
		if closeAt >= 0 {
			c.b.nest(t.slice(closeAt, closeEnd))
			sc.i = closeEnd
			if sc.i < c.hi {
				c.b.char(t, sc.i, '\n')
				sc.i++
			}
		}
	}
	c.pending = c.pending[:0]
}

// closes 判断逻辑行 [pos, e) 是不是 h 的结束行。
func (h *heredoc) closes(t *text, pos, e int) bool {
	if h.strip {
		for pos < e && t.at(pos) == '\t' {
			pos++
		}
	}
	n := e - pos
	if n == len(h.delim)+1 && t.at(e-1) == '\r' {
		n--
	}
	return n == len(h.delim) && t.has(pos, h.delim)
}

// hasBackslash 判断逻辑区间 [lo, hi) 里有没有反斜杠。
func hasBackslash(t *text, lo, hi int) bool {
	if t.ch == nil {
		return strings.IndexByte(t.raw[lo:hi], '\\') >= 0
	}
	return bytes.IndexByte(t.ch[lo:hi], '\\') >= 0
}

// oddBackslashes 判断逻辑行 [pos, e) 是否以奇数个反斜杠结尾（续行）。
func oddBackslashes(t *text, pos, e int) bool {
	n := 0
	for e > pos && t.at(e-1) == '\\' {
		n++
		e--
	}
	return n%2 == 1
}

// expandBody 处理分隔词不带引号的 heredoc 正文（从 sc.i 到 end）：$(…)、${…}、$((…))、$[…]、反引号会在展开时执行，
// 按命令行语境递归处理；其余是字面文本，一段一段按 textRules 抹。
func (sc *scanner) expandBody(end int) string {
	t := sc.t
	var out strings.Builder
	piece := sc.i
	flush := func() {
		if sc.i > piece {
			out.WriteString(sc.lit(t.slice(piece, sc.i), textRules))
		}
	}
	for sc.i < end {
		ch := t.at(sc.i)
		switch {
		case ch == '\\':
			sc.i = min(sc.i+2, end)
			continue
		case sc.depth >= maxDepth:
		case ch == '`':
			flush()
			out.WriteString(sc.backtickText(end))
			piece = sc.i
			continue
		case ch == '$' && sc.i+1 < end && strings.IndexByte("({[", t.at(sc.i+1)) >= 0:
			flush()
			s, _ := sc.dollarText(end)
			out.WriteString(s)
			piece = sc.i
			continue
		}
		sc.i++
	}
	flush()
	return out.String()
}

// paren1 处理 (：命令位置的 (( 是算术命令；命令里的 ( 是子 shell（含 NAME=( 数组赋值），计数；算术里的 ( 计数。
func (c *ctx) paren1() bool {
	sc, t := c.sc, c.sc.t
	switch c.mode {
	case mShell:
		c.endWord()
		if c.cmdPos && sc.i+1 < c.hi && t.at(sc.i+1) == '(' && sc.depth < maxDepth {
			// 算术命令 ((…)) 是一整条命令，之后在词首：((…))# 的 # 开始注释
			c.b.nest(sc.nestedText(c.hi, 2, mArith, ')', 2))
			c.cmdPos, c.start = false, true
			return true
		}
		c.paren++
		c.op(1)
		c.endCmd(true)
		return true
	case mArith:
		if c.closer == ')' {
			c.paren++
		}
	}
	return false
}

// dollar 处理 $ 开头的结构：$'…'、$"…"（top 为 true，即不在双引号里时）、$(…)、$((…))、${…}、$[…]。
// 不是这些时返回 false，$ 当普通字符。
func (c *ctx) dollar(top bool) bool {
	sc, t := c.sc, c.sc.t
	i := sc.i
	if i+1 >= c.hi {
		return false
	}
	switch t.at(i + 1) {
	case '\'':
		if !top {
			return false
		}
		c.ansiQuoted()
		return true
	case '"':
		if !top {
			return false
		}
		c.doubleQuoted(true)
		return true
	}
	if sc.depth >= maxDepth {
		return false
	}
	s, ok := sc.dollarText(c.hi)
	if ok {
		c.nestDone(s)
	}
	return ok
}

// dollarText 处理 sc.i 处的 $(…)、$((…))、${…}、$[…]，返回脱敏后的整段；不是这些时返回 false。
func (sc *scanner) dollarText(hi int) (string, bool) {
	t := sc.t
	i := sc.i
	if i+1 >= hi {
		return "", false
	}
	switch t.at(i + 1) {
	case '(':
		if i+2 < hi && t.at(i+2) == '(' {
			return sc.nestedText(hi, 3, mArith, ')', 2), true
		}
		return sc.nestedText(hi, 2, mShell, ')', 1), true
	case '{':
		return sc.nestedText(hi, 2, mParam, '}', 1), true
	case '[':
		return sc.nestedText(hi, 2, mArith, ']', 1), true
	}
	return "", false
}

// nestedText 处理以 n 个字符开头、以 closeLen 个 closer 结尾的嵌套结构，里面按 m 语境递归脱敏，返回整段。
// 调用方先检查层数上限。
func (sc *scanner) nestedText(hi, n int, m mode, closer byte, closeLen int) string {
	t := sc.t
	start := sc.i
	sc.i += n
	sc.depth++
	inner := sc.context(hi, m, closer)
	sc.depth--
	end := sc.i
	for k := 0; k < closeLen && sc.i < hi && t.at(sc.i) == closer; k++ {
		sc.i++
	}
	return t.slice(start, start+n) + inner + t.slice(end, sc.i)
}

// backtick 处理反引号命令替换。
func (c *ctx) backtick() {
	if c.sc.depth >= maxDepth {
		c.wordChar()
		return
	}
	c.nestDone(c.sc.backtickText(c.hi))
}

// backtickText 处理 sc.i 处的反引号命令替换：内容到下一个未转义的反引号为止（不管其中的引号，与 bash 一致），
// 按命令行语境递归脱敏，返回整段。内容里的 \\ \$ \` 先去掉反斜杠（bash 先这样处理再解析），里层看到的是去掉之后的文本。
func (sc *scanner) backtickText(hi int) string {
	t := sc.t
	start := sc.i
	k := start + 1
	esc := false
	for k < hi && t.at(k) != '`' {
		if t.at(k) == '\\' {
			k++
			esc = true
		}
		k++
	}
	k = min(k, hi)
	var inner string
	var u *text
	if esc && sc.rec < maxRec {
		u = t.unquote(start+1, k, bodyEscapes)
	}
	if u != nil {
		sub := &scanner{t: u, dry: sc.dry, depth: sc.depth + 1, rec: sc.rec + 1}
		inner = sub.context(u.n(), mShell, 0)
	} else {
		sc.i = start + 1
		sc.depth++
		inner = sc.context(k, mShell, 0)
		sc.depth--
	}
	sc.i = k
	if sc.i < hi {
		sc.i++
	}
	return t.slice(start, start+1) + inner + t.slice(k, sc.i)
}

// singleQuoted 处理单引号：里面是字面文本，整段按 literalRules 抹；交给解释器执行的递归按命令行语境处理。
func (c *ctx) singleQuoted() {
	sc, t := c.sc, c.sc.t
	i := sc.i
	k := i + 1
	for k < c.hi && t.at(k) != '\'' {
		k++
	}
	end := k
	if k < c.hi {
		end = k + 1
	}
	if c.recArgs() {
		sc.i = i + 1
		sc.depth++
		inner := sc.context(k, mShell, 0)
		sc.depth--
		sc.i = end
		c.b.nest(t.slice(i, i+1) + inner + t.slice(k, end))
		c.addWordText(t.slice(i+1, k))
		return
	}
	c.b.char(t, i, '\'')
	c.b.literal(sc.lit(t.slice(i+1, k), literalRules))
	if k < c.hi {
		c.b.closing(t, k, vCloseSQ)
	}
	c.addWordText(t.slice(i+1, k))
	sc.i = end
	c.inWord, c.start, c.quoted = true, false, true
}

// ansiQuoted 处理 $'…'：认 \ 转义，里面是字面文本（交给解释器执行的递归按命令行语境处理）。视图里 $ 当左引号，' 是 vMask。
func (c *ctx) ansiQuoted() {
	sc, t := c.sc, c.sc.t
	i := sc.i
	k := i + 2
	for k < c.hi && t.at(k) != '\'' {
		if t.at(k) == '\\' {
			k++
		}
		k++
	}
	k = min(k, c.hi)
	end := k
	if k < c.hi {
		end = k + 1
	}
	if c.recArgs() {
		sc.i = i + 2
		sc.depth++
		inner := sc.context(k, mShell, 0)
		sc.depth--
		sc.i = end
		c.b.nest(t.slice(i, i+2) + inner + t.slice(k, end))
		c.addWordText(t.slice(i+2, k))
		return
	}
	c.b.char(t, i, '\'')
	c.b.char(t, i+1, vMask)
	c.b.literal(sc.lit(t.slice(i+2, k), literalRules))
	c.addWordText(t.slice(i+2, k))
	if k < c.hi {
		c.b.closing(t, k, vCloseSQ)
	}
	sc.i = end
	c.inWord, c.start, c.quoted = true, false, true
}

// doubleQuoted 处理双引号（dollar 为 true 时是 $"…"）：命令替换这类嵌套结构递归处理，其余是字面文本，
// 一段一段按 literalRules 抹。交给解释器执行的，先空跑一遍找到右引号，再把去掉转义后的内容按命令行语境递归处理。
func (c *ctx) doubleQuoted(dollar bool) {
	sc, t := c.sc, c.sc.t
	i := sc.i
	open := 1
	if dollar {
		open = 2
	}
	if c.recArgs() && sc.rec < maxRec {
		dry := &scanner{t: t, i: i + open, dry: true, depth: sc.depth}
		closed := (&ctx{sc: dry, mode: c.mode, hi: c.hi}).dqInterior()
		k := dry.i
		if u := t.unquote(i+open, k, dqEscapes); u != nil {
			sub := &scanner{t: u, depth: sc.depth + 1, rec: sc.rec + 1}
			inner := sub.context(u.n(), mShell, 0)
			end := k
			if closed {
				end = k + 1
			}
			sc.i = end
			c.b.nest(t.slice(i, i+open) + inner + t.slice(k, end))
			c.addWordText(t.slice(i+open, k))
			return
		}
	}
	c.b.char(t, i, '"')
	if dollar {
		c.b.char(t, i+1, vMask)
	}
	sc.i = i + open
	if c.dqInterior() {
		c.b.closing(t, sc.i, vCloseDQ)
		sc.i++
	}
	c.inWord, c.start, c.quoted = true, false, true
}

// dqInterior 从 sc.i 起扫描双引号里的内容，停在右引号上（返回 true）或 hi（未闭合，返回 false）。
func (c *ctx) dqInterior() bool {
	sc, t := c.sc, c.sc.t
	piece := sc.i
	flush := func() {
		if sc.i > piece {
			c.b.literal(sc.lit(t.slice(piece, sc.i), literalRules))
			c.addWordText(t.slice(piece, sc.i))
		}
	}
	for sc.i < c.hi {
		switch t.at(sc.i) {
		case '"':
			flush()
			return true
		case '\\':
			sc.i = min(sc.i+2, c.hi)
			continue
		case '`':
			if sc.depth < maxDepth {
				flush()
				bt := sc.backtickText(c.hi)
				c.b.nest(bt)
				c.addWordNest(bt)
				piece = sc.i
				continue
			}
		case '$':
			if sc.i+1 < c.hi && strings.IndexByte("({[", t.at(sc.i+1)) >= 0 && sc.depth < maxDepth {
				flush()
				s, _ := sc.dollarText(c.hi)
				c.b.nest(s)
				c.addWordNest(s)
				piece = sc.i
				continue
			}
		}
		sc.i++
	}
	flush()
	return false
}
