// Package redact 在命令、参数和预览离开本机前抹掉其中的凭据。
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/railse/railse-opc/internal/policy"
)

const placeholder = "<redacted>"

// quoted 匹配成对引号括起的整段值，免得只抹掉引号里的前半截。没有右引号时一直抹到文本末尾：适配器会把 diff_preview
// 截到 4 KB，截断点落在引号括起的秘密中间时右引号就丢了。
// 第三种是 JSON 原文里转义过的 \"...\"，到第一个 \" 就收尾，不把后面的参数一起吞掉。
const quoted = `"[^"]*(?:"|$)|'[^']*(?:'|$)|\\"(?:[^"\\]|\\.)*?\\"`

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

// compile 用给定的“不带引号的值”字符类编译一套规则。几种语境只在值到哪里为止上不同。
// 顺序很重要：先处理带键名的形式，再处理裸 token。
func compile(bare string) []rule {
	value := `(?:` + quoted + `|` + bare + `)`
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
		// 字符串值没有右引号（被截断）时抹到文本末尾。YAML 的裸键（password: xxx）不认
		{regexp.MustCompile(`(?i)("` + key + `"\s*:\s*)(?:"(?:[^"\\]|\\.)*(?:"|$)|-?[0-9][0-9.eE+-]*|true|false)`), "${1}\"" + placeholder + "\""},
		// X-API-Key: xxx、PRIVATE-TOKEN: xxx、X-Auth-Token:xxx 这类请求头（Authorization 见上）。头名要带连字符，
		// 免得误抹代码里的 apiKey: config.key、YAML 里的 max_tokens: 10
		{regexp.MustCompile(`(?i)\b((?:[a-z0-9]+-)*(?:api-key|[a-z0-9]+-token|[a-z0-9]+-secret)[ \t]*:[ \t]*)` + value), "${1}" + placeholder},
		// URL 里的 user:pass@（任意 scheme）：用户名留着，只抹密码。密码里的 @ 算在密码里（取 / ? # 之前最后一个 @）
		{regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://[^/\s:@'"\\]*:)[^\s/?#'"\\]+@`), "${1}" + placeholder + "@"},
		// curl 的 -u/--user、-U/--proxy-user 的 user:pass（-u、-U 可以在组合短选项末尾，如 -sSu）：用户名与引号留着，
		// 只抹密码；不带冒号的（curl 会提示输入密码）不动
		{regexp.MustCompile(`(\bcurl\b` + sameCmd + `\s(?:-[A-Za-z]*[uU]\s*|--user\s+|--proxy-user\s+))` +
			`(?:('[^'":\s\\]*:)[^']*|("[^'":\s\\]*:)[^"\\]*|(\\"[^'":\s\\]*:)[^"\\]*|([^'":\s\\]*:)` + bare + `)`),
			"${1}${2}${3}${4}${5}" + placeholder},
		// mysql 系命令紧贴 -p 的密码（-pxxx、-p'xxx'）。-p 单独出现时 mysql 提示输入密码、后面的是库名；-P 是端口：都不动
		{regexp.MustCompile(`(\b(?:mysql|mariadb)[a-z-]*` + sameCmd + `\s-p)` + value), "${1}" + placeholder},
		// 裸 token：GitHub、OpenAI、Slack、AWS
		{regexp.MustCompile(`\b(ghp_|gho_|ghs_|github_pat_|sk-|xox[abp]-|AKIA)[A-Za-z0-9_\-]{16,}\b`), placeholder},
	}
}

var (
	// shellRules 用于命令行：值只在空白、引号、反斜杠处停。排除反斜杠是为了处理 JSON 原文时
	// 不吞掉 \" 转义，结果仍是合法 JSON。
	// 有意不在 ; | & < > ( ) 和反引号处截断：它们在双引号和 heredoc 里是普通字符，强密码常含这些符号，
	// 截断会漏掉后半截（如 "POSTGRES_PASSWORD=Xy7&kP(2)q"）。策略引擎用的是原文，脱敏只影响展示，
	// 宁可连带抹掉紧贴的命令（password=abc;rm -rf / 显示为 password=<redacted> -rf /），不要改回去。
	shellRules = compile(`[^\s'"\\]+`)
	// textRules 用于文件内容、路径和 JSON 字符串叶子：没有 shell 语义，值一直抹到空白或引号，
	// 免得含 & ( ; | 等符号的强密码只抹掉前半截。
	textRules = compile(`[^\s'"]+`)
	// urlRules 用于 URL：值在 & 处停，保留后面的查询参数。
	urlRules = compile(`[^\s'"&]+`)
)

// redact 按给定规则集返回脱敏后的副本。
func redact(s string, rules []rule) string {
	out := s
	for _, r := range rules {
		out = r.re.ReplaceAllString(out, r.repl)
	}
	return out
}

// String 按命令行语境返回脱敏后的副本。
func String(s string) string {
	return redact(s, shellRules)
}

// Text 按普通文本语境返回脱敏后的副本，规则与 DiffPreview 相同：值一直抹到空白或引号。
// 用于事件消息这类可能夹带命令原文的自由文本。
func Text(s string) string {
	return redact(s, textRules)
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
