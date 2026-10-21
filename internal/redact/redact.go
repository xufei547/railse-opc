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

// bare 是不带引号的值：遇到空白、引号、反斜杠或 shell 元字符就停，
// 既不吞掉 JSON 转义，也不把后面链接的命令一起藏掉。
const bare = `[^\s'"\\;|&<>()` + "`" + `]+`

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

// 顺序很重要：先处理带键名的形式，再处理裸 token。
var rules = []rule{
	// Authorization: Bearer xxx / Authorization: Basic xxx
	{regexp.MustCompile(`(?i)(authorization:\s*)(bearer|basic|token)?\s*` + bare), "${1}" + placeholder},
	// 不跟在 Authorization: 后面的 Bearer xxx
	{regexp.MustCompile(`(?i)\b(bearer\s+)` + bare), "${1}" + placeholder},
	// --token xxx、--password "xxx"、--api-key 'xxx'
	{regexp.MustCompile(`(?i)(--?(token|password|passwd|api[-_]?key|secret|auth)\s+)(?:` + quoted + `|` + bare + `)`), "${1}" + placeholder},
	// token=xxx、DB2_PASSWORD="xxx"、--password=xxx、spring.datasource.password=xxx、:_authToken=xxx（含 URL 查询串与 env 赋值）
	{regexp.MustCompile(`(?i)((?:^|[\s?&;'"\-.:/,{(])` + key + `=)(?:` + quoted + `|` + bare + `)`), "${1}" + placeholder},
	// 裸 token：GitHub、OpenAI、Slack、AWS
	{regexp.MustCompile(`\b(ghp_|gho_|ghs_|github_pat_|sk-|xox[abp]-|AKIA)[A-Za-z0-9_\-]{16,}\b`), placeholder},
}

// String 返回脱敏后的副本。
func String(s string) string {
	out := s
	for _, r := range rules {
		out = r.re.ReplaceAllString(out, r.repl)
	}
	return out
}

// Payload 对载荷里可能含凭据的字段脱敏，返回副本。
func Payload(p policy.Payload) policy.Payload {
	out := p
	out.Command = String(p.Command)
	out.Path = String(p.Path)
	out.DiffPreview = String(p.DiffPreview)
	out.URL = String(p.URL)
	if len(p.Args) > 0 {
		out.Args = args(p.Args)
	}
	return out
}

// args 对 MCP 参数脱敏。合法 JSON 按结构处理，结果仍是合法 JSON；否则当普通字符串处理。
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

// walk 递归处理 JSON 值：敏感键的值整体抹掉，其余字符串逐个脱敏。
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
		return String(x)
	}
	return v
}
