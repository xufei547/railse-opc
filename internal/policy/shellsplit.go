package policy

import "strings"

// segment 是一条 shell 命令里由 &&、||、|、|&、;、&、换行分隔出的一段。
type segment struct {
	text          string      // 原文
	argv          []string    // 分词并去引号后的参数，不含重定向
	argvFlags     []wordFlags // 与 argv 一一对应
	redirects     []string    // 输出重定向的目标：>、>>、>|、&>、&>>、N>、zsh 的覆盖写（>!、>>!、&>! 等），以及目标不是 fd 的 >&
	redirectFlags []wordFlags // 与 redirects 一一对应
	inputs        []string    // 输入重定向的来源：<、<!、N<，以及来源不是 fd 的 <&
	inputFlags    []wordFlags // 与 inputs 一一对应
	subshell      bool        // 含无法静态分析的写法：命令替换、进程替换、裸括号、heredoc、带转义的 $'…'、未闭合引号、缺目标或认不出的重定向
}

// wordFlags 记录一个词在去引号之前由 shell 解释的部分，用来判断去引号后的文本能否按字面解析。
type wordFlags struct {
	glob   bool // 含不在任何引号里、也没被反斜杠转义的 * ? [，或开始花括号展开的 {（见 braceExpands）
	expand bool // 含没被转义、也不在单引号（含 $'…'）里的 $
	tilde  bool // 以不加引号、没被转义的 ~ 开头
}

// splitSegments 按 bash 语义在引号之外拆段，返回每段去掉首尾空白与续行的原文（不含注释），丢掉空段。
// 命令里有无法静态分析的写法时，先返回去掉首尾空白的整条命令（它的 parseSegment 结果 subshell 为 true），
// 再返回尽力拆出的各段，让其中的危险段仍参与定级；只拆出一段且与整条相同时只返回一次。
// heredoc 的正文不是 shell 语法，只返回整条命令。
func splitSegments(cmd string) []string {
	runes := []rune(cmd)
	segs, opaque, heredoc := scanShell(runes)
	whole := strings.TrimSpace(cmd)
	if heredoc {
		return []string{whole}
	}
	var out []string
	for _, s := range segs {
		out = append(out, string(runes[s.start:s.end]))
	}
	if opaque && !(len(out) == 1 && out[0] == whole) {
		out = append([]string{whole}, out...)
	}
	return out
}

// parseSegment 用同一个扫描器解析单段文本。
func parseSegment(text string) segment {
	segs, opaque, _ := scanShell([]rune(text))
	if len(segs) == 0 {
		return segment{text: text}
	}
	seg := segs[0].seg
	seg.text = text
	if len(segs) > 1 || opaque {
		// 整条命令无法静态切分，或单段文本又切出多段（splitSegments 的产物不会如此），都按无法分析处理
		seg.subshell = true
	}
	return seg
}

// tokKind 区分普通词和各类重定向符号。
type tokKind int

const (
	tokWord       tokKind = iota // 普通词
	tokOut                       // >、>>、&>、&>> 及其覆盖写形式（后缀 | 或 zsh 的 !），zsh 的 >&|、>&!：下一个词是写入目标
	tokOutDup                    // >&：下一个词是数字或 - 时为 fd 复制，否则是写入目标
	tokIn                        // < 与 <!：下一个词是读取来源
	tokInDup                     // <&：下一个词是数字或 - 时为 fd 复制，否则是读取来源
	tokHereString                // <<<：下一个词是字符串内容，不记录
)

type token struct {
	kind  tokKind
	text  string    // 仅 tokWord 有
	flags wordFlags // 仅 tokWord 有
}

// scanned 是扫描器切出的一段：原文区间加解析结果（text 由调用方填）。
type scanned struct {
	start, end int // 在 runes 中的区间：从第一个到最后一个有效字符，不含首尾空白、续行、分隔符与注释
	seg        segment
}

// 引号状态
const (
	quoteNone   = iota
	quoteSingle // '…'：没有任何转义
	quoteDouble // "…"：\ 转义下一个字符，$( 与反引号照样执行
	quoteANSI   // $'…'：\ 转义下一个字符，\' 不结束引号
)

// scanShell 按 bash 词法把命令走一遍：拆段、分词、去引号、识别重定向和无法静态分析的写法。
// 重定向操作符同时认 zsh 的写法（见 clobber）：用户的 shell 可能是 zsh。
// 任何一段标了 subshell，opaque 就为 true，由调用方对整条命令 fail closed。触发条件：引号外的 $(、反引号、
// <(、>(，裸括号，含反斜杠的 $'…'，未闭合的引号，缺目标的重定向，认不出的重定向变体，以及 heredoc。
// 除 heredoc 外扫描会继续，尽力拆段分词。命令替换与进程替换内部的引号和分隔符不跟踪嵌套，
// 只用括号深度让 $(date)、<(ls) 这类词保持完整。
// heredoc（<< 而不是 <<<）的正文不是 shell 语法，遇到就停止扫描，并返回 heredoc=true。
func scanShell(runes []rune) (segs []scanned, opaque, heredoc bool) {
	n := len(runes)
	at := func(i int) rune {
		if i < n {
			return runes[i]
		}
		return 0
	}
	var (
		quote    = quoteNone
		cur      strings.Builder
		inWord   bool
		quoted   bool      // 当前词是否有任何部分加了引号或被转义
		flags    wordFlags // 当前词的标志
		dollar   = -2      // 最近一个引号外、未转义的 $ 的下标，用来认出 ${…}
		toks     []token
		subshell bool
		depth    int  // 当前段里未加引号的 $(、<(、>( 尚未闭合的括号层数
		first    = -1 // 当前段第一个有效字符的下标，-1 表示还没有
		last     int  // 当前段最后一个有效字符之后的下标
	)
	// mark 记录 [from, to) 是当前段的有效内容。引号外的空白与续行不算，被转义的空白算。
	mark := func(from, to int) {
		if first < 0 {
			first = from
		}
		last = to
	}
	// failClosed 把当前段标为无法分析；endSeg 会据此把整条命令标为 opaque。
	failClosed := func() { subshell = true }
	// clobber 吃掉 runes[i] 处输出操作符之后的覆盖写后缀，返回操作符最后一个字符的下标：
	// | 是 bash 与 zsh 的 >|，! 是 zsh 的 >!、>>!、&>!、&>>!、>&!。zsh 下 echo x >! /a 写的是 /a；
	// 不吃掉的话 ! 会成为目标（或目标的开头），真正的路径被当成普通参数。
	clobber := func(i int) int {
		if at(i+1) == '|' || at(i+1) == '!' {
			return i + 1
		}
		return i
	}
	// strayBang 在 runes[i] 处结束的完整重定向操作符之后又紧跟 ! 时（>!!、>|!、<!! 等）按无法分析处理：
	// 认不出的变体不能让 ! 绑成目标或参数。
	strayBang := func(i int) {
		if at(i+1) == '!' {
			failClosed()
		}
	}
	word := func(r rune) {
		cur.WriteRune(r)
		inWord = true
	}
	flushWord := func() {
		if inWord {
			toks = append(toks, token{kind: tokWord, text: cur.String(), flags: flags})
		}
		cur.Reset()
		inWord = false
		quoted = false
		flags = wordFlags{}
	}
	endSeg := func() {
		flushWord()
		if len(toks) > 0 || subshell {
			seg := resolveTokens(toks, subshell)
			if seg.subshell {
				opaque = true // 包括未闭合引号与缺目标的重定向
			}
			segs = append(segs, scanned{start: first, end: last, seg: seg})
		}
		toks = nil
		subshell = false
		depth = 0
		first = -1
	}

	for i := 0; i < n; i++ {
		c := runes[i]
		from := i
		if quote != quoteNone {
			if quote == quoteANSI && c == '\\' {
				failClosed() // 不解码 $'…' 的转义（\n、\x41、\u… 等）
			}
			switch {
			case quote == quoteSingle:
				if c == '\'' {
					quote = quoteNone
				} else {
					cur.WriteRune(c)
				}
			case c == '\\' && quote == quoteDouble && at(i+1) == '\n':
				i++ // 续行
			case c == '\\' && i+1 < n:
				cur.WriteRune(runes[i+1])
				i++
			case (quote == quoteDouble && c == '"') || (quote == quoteANSI && c == '\''):
				quote = quoteNone
			default:
				if quote == quoteDouble && (c == '`' || (c == '$' && at(i+1) == '(')) {
					failClosed()
				}
				if quote == quoteDouble && c == '$' {
					flags.expand = true
				}
				cur.WriteRune(c)
			}
			mark(from, i+1)
			continue
		}

		significant := true
		switch {
		case c == ' ' || c == '\t':
			flushWord()
			significant = false
		case c == '#' && !inWord:
			// 词首的 # 开始注释，跳到行尾；换行本身仍然拆段
			endSeg()
			for i+1 < n && runes[i+1] != '\n' {
				i++
			}
			significant = false
		case c == '\\' && at(i+1) == '\n':
			i++ // 续行：反斜杠加换行整体去掉
			significant = false
		case c == '\\' && i+1 < n:
			word(runes[i+1])
			quoted = true
			i++
		case c == '\'':
			quote, inWord, quoted = quoteSingle, true, true
		case c == '"':
			quote, inWord, quoted = quoteDouble, true, true
		case c == '$' && at(i+1) == '\'':
			quote, inWord, quoted = quoteANSI, true, true
			i++
		case c == '`':
			failClosed()
		case c == '$' && at(i+1) == '(':
			failClosed()
			flags.expand = true
			word(c)
			word('(')
			i++
			depth++
		case c == '\n' || c == ';':
			endSeg()
			significant = false
		case c == '|':
			if at(i+1) == '|' || at(i+1) == '&' {
				i++
			}
			endSeg()
			significant = false
		case c == '&' && at(i+1) == '&':
			i++
			endSeg()
			significant = false
		case c == '&' && at(i+1) == '>':
			// &> 与 &>>，以及覆盖写的 &>|、&>>|、&>!、&>>!
			flushWord()
			i++
			if at(i+1) == '>' {
				i++
			}
			i = clobber(i)
			strayBang(i)
			toks = append(toks, token{kind: tokOut})
		case c == '&':
			endSeg() // 后台符
			significant = false
		case (c == '<' || c == '>') && at(i+1) == '(':
			// 进程替换 <(…) 与 >(…)
			failClosed()
			word(c)
			word('(')
			i++
			depth++
		case c == '(' && depth > 0:
			word(c)
			depth++
		case c == ')' && depth > 0:
			word(c)
			depth--
		case c == '(' || c == ')':
			// 裸括号是 bash 的元字符（子 shell 分组、函数定义）：按无法分析处理，并且断词，
			// 这样 (#x 里的 # 照样开始注释
			failClosed()
			flushWord()
		case c == '<' || c == '>':
			// 紧贴在前面、未加引号的纯数字是文件描述符，不是参数
			if inWord && !quoted && isDigits(cur.String()) {
				cur.Reset()
				inWord = false
				flags = wordFlags{}
			} else {
				flushWord()
			}
			if c == '<' && at(i+1) == '<' && at(i+2) != '<' {
				failClosed()
				mark(from, n)
				endSeg()
				return segs, true, true
			}
			var k tokKind
			switch {
			case c == '<' && at(i+1) == '<':
				k = tokHereString
				i += 2
			case c == '<' && at(i+1) == '&':
				k = tokInDup
				i++
			case c == '<':
				// <!：按 < 处理，! 不算来源的一部分；不论 ! 归操作符还是归文件名，都只是读
				k = tokIn
				if at(i+1) == '!' {
					i++
				}
			case at(i+1) == '&':
				k = tokOutDup
				i++
				// zsh 的 >&| 与 >&!：标准输出与标准错误覆盖写进后面的文件，后面的词总是文件
				if j := clobber(i); j != i {
					k, i = tokOut, j
				}
			default:
				// > 与 >>，以及覆盖写的 >|、>>|、>!、>>!
				k = tokOut
				if at(i+1) == '>' {
					i++
				}
				i = clobber(i)
			}
			if k != tokHereString {
				strayBang(i)
			}
			toks = append(toks, token{kind: k})
		default:
			switch {
			case strings.ContainsRune("*?[", c), c == '{' && dollar != i-1 && braceExpands(runes, i):
				flags.glob = true
			case c == '$':
				flags.expand = true
				dollar = i
			case c == '~' && !inWord:
				flags.tilde = true
			}
			word(c)
		}
		if significant {
			mark(from, i+1)
		}
	}
	if quote != quoteNone {
		subshell = true // 引号到结尾都没闭合
	}
	endSeg()
	return segs, opaque, false
}

// braceExpands 判断 runes[i] 处引号外的 { 是否开始花括号展开：同一个词里有与之配对的 }，
// 且其间有引号外的 , 或 ..。引号里的内容整段跳过，反斜杠转义的字符不算。
// 于是 ${HOME}（由调用方排除）、stash@{0}、{0} 都是字面量，{a,b}、x{1..3} 是展开。
func braceExpands(runes []rune, i int) bool {
	depth, sep := 0, false
	for j := i; j < len(runes); j++ {
		switch c := runes[j]; c {
		case '\\':
			j++
		case '\'', '"':
			for j++; j < len(runes) && runes[j] != c; j++ {
				if c == '"' && runes[j] == '\\' {
					j++
				}
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return sep
			}
		case ',':
			sep = true
		case '.':
			if j+1 < len(runes) && runes[j+1] == '.' {
				sep = true
			}
		case ' ', '\t', '\n', ';', '&', '|', '<', '>', '(', ')':
			return false
		}
	}
	return false
}

// resolveTokens 把一段的词与重定向符号整理成 segment：每个重定向符号吃掉紧随其后的一个词。
func resolveTokens(toks []token, subshell bool) segment {
	seg := segment{subshell: subshell}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind == tokWord {
			seg.argv = append(seg.argv, t.text)
			seg.argvFlags = append(seg.argvFlags, t.flags)
			continue
		}
		if i+1 >= len(toks) || toks[i+1].kind != tokWord {
			seg.subshell = true // 重定向缺目标
			continue
		}
		target := toks[i+1]
		i++
		switch {
		case t.kind == tokOut, t.kind == tokOutDup && !isFD(target.text):
			seg.redirects = append(seg.redirects, target.text)
			seg.redirectFlags = append(seg.redirectFlags, target.flags)
		case t.kind == tokIn, t.kind == tokInDup && !isFD(target.text):
			seg.inputs = append(seg.inputs, target.text)
			seg.inputFlags = append(seg.inputFlags, target.flags)
		}
	}
	return seg
}

// isFD 判断 >& / <& 后面的词是不是文件描述符（数字）或关闭符 -。
func isFD(s string) bool {
	return s == "-" || isDigits(s)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
