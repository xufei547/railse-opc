package policy

import "strings"

// segment 是一条 shell 命令里由 &&、||、;、|、&、换行分隔出的一段。
type segment struct {
	text      string   // 原文
	argv      []string // 按空白分词并去引号后的参数，不含重定向
	redirects []string // >、>>、2>、&> 的目标
	subshell  bool     // 含 $( 或反引号，无法静态分析
}

// splitSegments 在引号之外按 &&、||、;、|、后台符 &、换行拆分，去掉首尾空白，丢掉空段。
// &> 与 2>&1、>&2 里的 & 属于重定向，不拆。
func splitSegments(cmd string) []string {
	var segs []string
	var cur strings.Builder
	var quote rune
	flush := func() {
		if s := strings.TrimSpace(cur.String()); s != "" {
			segs = append(segs, s)
		}
		cur.Reset()
	}
	runes := []rune(cmd)
	afterGT := false // 上一个字符是引号外、未转义的 >
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		prevGT := afterGT
		afterGT = false
		switch {
		case quote == '"' && c == '\\' && i+1 < len(runes):
			// 双引号内的反斜杠连同下一个字符原样保留，被转义的 " 不结束引号
			cur.WriteRune(c)
			cur.WriteRune(runes[i+1])
			i++
		case quote != 0:
			cur.WriteRune(c)
			if c == quote {
				quote = 0
			}
		case c == '\'' || c == '"':
			quote = c
			cur.WriteRune(c)
		case c == '\\' && i+1 < len(runes):
			cur.WriteRune(c)
			cur.WriteRune(runes[i+1])
			i++
		case c == '&' && i+1 < len(runes) && runes[i+1] == '&':
			flush()
			i++
		case c == '&' && (prevGT || i+1 < len(runes) && runes[i+1] == '>'):
			cur.WriteRune(c)
		case c == '|':
			flush()
			if i+1 < len(runes) && runes[i+1] == '|' {
				i++
			}
		case c == ';' || c == '&' || c == '\n':
			flush()
		default:
			afterGT = c == '>'
			cur.WriteRune(c)
		}
	}
	flush()
	return segs
}

// parseSegment 对单段做引号感知分词，提取重定向目标，检测子 shell。
func parseSegment(text string) segment {
	seg := segment{text: text}
	var tokens []string // 分词结果，重定向符号以 ">" 占位
	var cur strings.Builder
	var quote rune
	inToken := false
	flush := func() {
		if inToken {
			tokens = append(tokens, cur.String())
		}
		cur.Reset()
		inToken = false
	}
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote != 0:
			switch {
			case c == quote:
				quote = 0
			case quote == '"' && c == '\\' && i+1 < len(runes):
				cur.WriteRune(runes[i+1])
				i++
			default:
				// 双引号内 bash 照样执行 $( 与反引号；单引号内一律按字面
				if quote == '"' && (c == '`' || (c == '$' && i+1 < len(runes) && runes[i+1] == '(')) {
					seg.subshell = true
				}
				cur.WriteRune(c)
			}
		case c == '\'' || c == '"':
			quote = c
			inToken = true
		case c == '\\' && i+1 < len(runes):
			cur.WriteRune(runes[i+1])
			inToken = true
			i++
		case c == '`':
			seg.subshell = true
		case c == '$' && i+1 < len(runes) && runes[i+1] == '(':
			seg.subshell = true
			cur.WriteRune(c)
			inToken = true
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		case c == '>':
			// 紧贴在 > 前面的 1、2、& 是文件描述符，不是参数
			if s := cur.String(); inToken && (s == "1" || s == "2" || s == "&") {
				cur.Reset()
				inToken = false
			} else {
				flush()
			}
			if i+1 < len(runes) && runes[i+1] == '>' {
				i++
			}
			tokens = append(tokens, ">")
		default:
			cur.WriteRune(c)
			inToken = true
		}
	}
	flush()
	for i := 0; i < len(tokens); i++ {
		if tokens[i] == ">" {
			if i+1 < len(tokens) {
				seg.redirects = append(seg.redirects, tokens[i+1])
				i++
			}
			continue
		}
		seg.argv = append(seg.argv, tokens[i])
	}
	return seg
}
