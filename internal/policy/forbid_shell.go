package policy

import (
	"path/filepath"
	"strings"
)

// 角色禁用清单看穿命令替换（设计 §3.2 的禁用之后，审查补充）：`echo "$(git push)"`、反引号、进程替换 <(…) >(…) 与 eval 里的命令
// 同样参与 command_prefix、command_regex 的匹配。规则引擎的 deny 不做这一步（0.2 的语义不动），只有角色的 forbid 用。

// maxForbidDepth 是递归展开的层数上限；更深的按解析不了处理（fail closed：越界问人）。
const maxForbidDepth = 8

// subPlaceholder 是把命令替换挖掉之后填回外层的占位词：外层仍是能扫描的普通命令，挖出的命令单独匹配。
const subPlaceholder = "RAILSE_SUBST"

// forbidCommands 返回 shell 命令里要逐条参与禁用匹配的命令文本：外层（命令替换换成占位词）、每个命令替换的内部（递归），
// 以及 eval 的参数拼成的命令（递归）。opaque 为真表示有解析不了的结构：heredoc、没闭合的引号或括号、裸括号、带转义的 $'…'、
// 层数超限等——这些里面可能藏着命令，看不见。sh -c 这类包装由 matchRequest（effectiveSegments）照常看穿。
func forbidCommands(cmd string) (cmds []string, opaque bool) {
	var walk func(c string, depth int)
	walk = func(c string, depth int) {
		if depth > maxForbidDepth {
			opaque = true
			return
		}
		outer, bodies, ok := extractSubstitutions(c)
		if !ok {
			opaque = true
		}
		cmds = append(cmds, outer)
		if _, op, heredoc := scanShell([]rune(outer)); op || heredoc {
			opaque = true
		}
		for _, b := range bodies {
			walk(b, depth+1)
		}
		for _, seg := range effectiveSegments(outer) {
			argv := stripWrappers(seg.argv)
			if len(argv) > 1 && filepath.Base(argv[0]) == "eval" {
				walk(strings.Join(argv[1:], " "), depth+1)
			}
		}
	}
	walk(cmd, 0)
	return cmds, opaque
}

// extractSubstitutions 挖出引号外与双引号里的 $(…)、反引号，以及引号外的 <(…)、>(…)，返回换成占位词的外层与各段内部原文。
// 单引号里的不算（那是字面量）。括号按层数配对，内部的引号照样跳过。没闭合时 ok 为假。$(( 算术展开也按 $( 处理：
// 内部的 ( … ) 是裸括号，扫描时按解析不了处理，偏严。
func extractSubstitutions(cmd string) (outer string, bodies []string, ok bool) {
	r := []rune(cmd)
	var b strings.Builder
	inDouble := false
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case c == '\\' && i+1 < len(r):
			b.WriteRune(c)
			b.WriteRune(r[i+1])
			i++
		case c == '\'' && !inDouble:
			j := indexRune(r, i+1, '\'')
			if j < 0 {
				return cmd, bodies, false
			}
			b.WriteString(string(r[i : j+1]))
			i = j
		case c == '"':
			inDouble = !inDouble
			b.WriteRune(c)
		case c == '`':
			j := i + 1
			for j < len(r) && r[j] != '`' {
				if r[j] == '\\' {
					j++
				}
				j++
			}
			if j >= len(r) {
				return cmd, bodies, false
			}
			bodies = append(bodies, string(r[i+1:j]))
			b.WriteString(subPlaceholder)
			i = j
		case i+1 < len(r) && r[i+1] == '(' && (c == '$' || (!inDouble && (c == '<' || c == '>'))):
			end := matchParen(r, i+1)
			if end < 0 {
				return cmd, bodies, false
			}
			bodies = append(bodies, string(r[i+2:end]))
			b.WriteString(subPlaceholder)
			i = end
		default:
			b.WriteRune(c)
		}
	}
	if inDouble {
		return cmd, bodies, false
	}
	return b.String(), bodies, true
}

// matchParen 返回与 r[open]（一个左括号）配对的右括号下标，内部的引号与转义照样跳过；没有返回 -1。
func matchParen(r []rune, open int) int {
	depth := 0
	inDouble := false
	for i := open; i < len(r); i++ {
		switch c := r[i]; {
		case c == '\\':
			i++
		case c == '\'' && !inDouble:
			j := indexRune(r, i+1, '\'')
			if j < 0 {
				return -1
			}
			i = j
		case c == '"':
			inDouble = !inDouble
		case inDouble:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func indexRune(r []rune, from int, c rune) int {
	for i := from; i < len(r); i++ {
		if r[i] == c {
			return i
		}
	}
	return -1
}

// hasCommandForbid 报告禁用清单里有没有按命令匹配的条目（command_prefix、command_regex）：只有这类条目才会被藏在
// 看不见的结构里绕过。
func (r *Role) hasCommandForbid() bool {
	for _, m := range r.Forbid {
		if m.CommandPrefix != "" || m.CommandRegex != "" {
			return true
		}
	}
	return false
}
