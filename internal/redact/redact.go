// Package redact 在命令、参数和预览离开本机前抹掉其中的凭据。
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"

	"github.com/railse/railse-opc/internal/policy"
)

const placeholder = "<redacted>"

// quoted 匹配成对引号括起的整段值，免得只抹掉引号里的前半截。
// 第三种是 JSON 原文里转义过的 \"...\"，到第一个 \" 就收尾，不把后面的参数一起吞掉。
const quoted = `"[^"]*"|'[^']*'|\\"(?:[^"\\]|\\.)*?\\"`

// key 是敏感键名，前面可带任意前缀。token 不认 tokens，auth 只认 auth、auth_token、
// auth-key、authorization 这类，免得误抹 max_tokens、author。
const key = `[a-z0-9_.-]*(?:` +
	`token(?:[a-rt-z0-9_.-][a-z0-9_.-]*)?` +
	`|auth(?:orization|[_-]?(?:token|key))?` +
	`|(?:api[-_]?key|secret|password|passwd)[a-z0-9_.-]*)`

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
		// token=xxx、DB2_PASSWORD="xxx"、--env=DB_PASSWORD=xxx、+API_KEY=xxx、:_authToken=xxx。
		// 不设左边界：key 的前缀字符类会吸收键名前缀，${1} 只含键名本身。
		{regexp.MustCompile(`(?i)(` + key + `=)` + value), "${1}" + placeholder},
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

// Payload 对载荷里可能含凭据的字段脱敏，返回副本。每个字段按自己的语境处理。
func Payload(p policy.Payload) policy.Payload {
	out := p
	out.Command = String(p.Command)
	out.Path = redact(p.Path, textRules)
	out.DiffPreview = redact(p.DiffPreview, textRules)
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
