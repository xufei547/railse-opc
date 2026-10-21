package policy

import "strings"

// segment 是一条 shell 命令里由 &&、||、|、|&、;、&、换行分隔出的一段。
type segment struct {
	text      string   // 原文
	argv      []string // 分词并去引号后的参数，不含重定向
	redirects []string // 输出重定向的目标：>、>>、>|、&>、&>>、N>，以及目标不是 fd 的 >&
	inputs    []string // 输入重定向的来源：<、N<，以及来源不是 fd 的 <&
	subshell  bool     // 含无法静态分析的写法：命令替换、进程替换、heredoc、未闭合引号、缺目标的重定向
}

// splitSegments 按 bash 语义在引号之外拆段，返回每段去掉首尾空白与续行的原文（不含注释），丢掉空段。
// 遇到 heredoc 时整条命令作为一段返回，它的 parseSegment 结果 subshell 为 true。
func splitSegments(cmd string) []string {
	runes := []rune(cmd)
	segs, heredoc := scanShell(runes)
	if heredoc {
		return []string{strings.TrimSpace(cmd)}
	}
	var out []string
	for _, s := range segs {
		out = append(out, string(runes[s.start:s.end]))
	}
	return out
}

// parseSegment 用同一个扫描器解析单段文本。
func parseSegment(text string) segment {
	segs, heredoc := scanShell([]rune(text))
	if len(segs) == 0 {
		return segment{text: text}
	}
	seg := segs[0].seg
	seg.text = text
	if len(segs) > 1 || heredoc {
		// heredoc，或单段文本又切出多段（splitSegments 的产物不会如此），都按无法分析处理
		seg.subshell = true
	}
	return seg
}

// tokKind 区分普通词和各类重定向符号。
type tokKind int

const (
	tokWord       tokKind = iota // 普通词
	tokOut                       // >、>>、>|、&>、&>>：下一个词是写入目标
	tokOutDup                    // >&：下一个词是数字或 - 时为 fd 复制，否则是写入目标
	tokIn                        // <：下一个词是读取来源
	tokInDup                     // <&：下一个词是数字或 - 时为 fd 复制，否则是读取来源
	tokHereString                // <<<：下一个词是字符串内容，不记录
)

type token struct {
	kind tokKind
	text string // 仅 tokWord 有
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
// 遇到 heredoc（<< 而不是 <<<）时停止扫描并返回 heredoc=true，由调用方对整条命令 fail closed。
func scanShell(runes []rune) (segs []scanned, heredoc bool) {
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
		quoted   bool // 当前词是否有任何部分加了引号或被转义
		toks     []token
		subshell bool
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
	word := func(r rune) {
		cur.WriteRune(r)
		inWord = true
	}
	flushWord := func() {
		if inWord {
			toks = append(toks, token{kind: tokWord, text: cur.String()})
		}
		cur.Reset()
		inWord = false
		quoted = false
	}
	endSeg := func() {
		flushWord()
		if len(toks) > 0 || subshell {
			segs = append(segs, scanned{start: first, end: last, seg: resolveTokens(toks, subshell)})
		}
		toks = nil
		subshell = false
		first = -1
	}

	for i := 0; i < n; i++ {
		c := runes[i]
		from := i
		if quote != quoteNone {
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
					subshell = true
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
			subshell = true
		case c == '$' && at(i+1) == '(':
			subshell = true
			word(c)
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
			// &> 与 &>>
			flushWord()
			i++
			if at(i+1) == '>' {
				i++
			}
			toks = append(toks, token{kind: tokOut})
		case c == '&':
			endSeg() // 后台符
			significant = false
		case (c == '<' || c == '>') && at(i+1) == '(':
			// 进程替换 <(…) 与 >(…)
			subshell = true
			word(c)
			word('(')
			i++
		case c == '<' || c == '>':
			// 紧贴在前面、未加引号的纯数字是文件描述符，不是参数
			if inWord && !quoted && isDigits(cur.String()) {
				cur.Reset()
				inWord = false
			} else {
				flushWord()
			}
			if c == '<' && at(i+1) == '<' && at(i+2) != '<' {
				subshell = true
				mark(from, n)
				endSeg()
				return segs, true
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
				k = tokIn
			case at(i+1) == '&':
				k = tokOutDup
				i++
			case at(i+1) == '>' || at(i+1) == '|':
				k = tokOut
				i++
			default:
				k = tokOut
			}
			toks = append(toks, token{kind: k})
		default:
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
	return segs, false
}

// resolveTokens 把一段的词与重定向符号整理成 segment：每个重定向符号吃掉紧随其后的一个词。
func resolveTokens(toks []token, subshell bool) segment {
	seg := segment{subshell: subshell}
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t.kind == tokWord {
			seg.argv = append(seg.argv, t.text)
			continue
		}
		if i+1 >= len(toks) || toks[i+1].kind != tokWord {
			seg.subshell = true // 重定向缺目标
			continue
		}
		target := toks[i+1].text
		i++
		switch {
		case t.kind == tokOut, t.kind == tokOutDup && !isFD(target):
			seg.redirects = append(seg.redirects, target)
		case t.kind == tokIn, t.kind == tokInDup && !isFD(target):
			seg.inputs = append(seg.inputs, target)
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
