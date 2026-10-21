// Package redact 在命令、参数和预览离开本机前抹掉其中的凭据。
package redact

import (
	"regexp"

	"github.com/railse/railse-opc/internal/policy"
)

const placeholder = "<redacted>"

// quoted 匹配成对引号括起的整段值，免得只抹掉引号里的前半截。
const quoted = `"[^"]*"|'[^']*'`

type rule struct {
	re   *regexp.Regexp
	repl string
}

// 顺序很重要：先处理带键名的形式，再处理裸 token。
var rules = []rule{
	// Authorization: Bearer xxx / Authorization: Basic xxx
	{regexp.MustCompile(`(?i)(authorization:\s*)(bearer|basic|token)?\s*[^\s'"]+`), "${1}" + placeholder},
	// 不跟在 Authorization: 后面的 Bearer xxx
	{regexp.MustCompile(`(?i)\b(bearer\s+)[^\s'"]+`), "${1}" + placeholder},
	// --token xxx、--password "xxx"、--api-key 'xxx'
	{regexp.MustCompile(`(?i)(--?(token|password|passwd|api[-_]?key|secret|auth)\s+)(?:` + quoted + `|[^\s'"]+)`), "${1}" + placeholder},
	// token=xxx、api_key=xxx、PASSWORD="xxx"、--password=xxx（含 URL 查询串与 env 赋值）
	{regexp.MustCompile(`(?i)((?:^|[\s?&;'"-])[A-Za-z_]*(token|api[-_]?key|apikey|secret|password|passwd|auth)[A-Za-z_]*=)(?:` + quoted + `|[^\s&'"]+)`), "${1}" + placeholder},
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
		out.Args = []byte(String(string(p.Args)))
	}
	return out
}
