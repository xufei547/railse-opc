package policy

import (
	"reflect"
	"strings"
	"testing"
)

const validRulesYAML = `
version: 1
rules:
  - id: builtin-readonly
    source: builtin
    scope: global
    match: {risk: read}
    action: allow
  - id: user-push-railse
    source: user
    scope: {project: "~/Projects/railse-opc"}
    match: {payload: shell, command_prefix: "git push origin"}
    action: allow
  - id: learned-0042
    source: learned
    scope: {project: "~/Projects/blog"}
    match: {payload: shell, command_prefix: "npm run", risk: write_in_project}
    action: allow
    created_from: "6f1c"
  - id: machine-deny-curl
    source: user
    scope: {machine: m1}
    match: {payload: shell, command_regex: "^curl .*"}
    action: deny
    enabled: false
`

func TestParseRuleSetValid(t *testing.T) {
	rs, err := ParseRuleSet([]byte(validRulesYAML))
	if err != nil {
		t.Fatal(err)
	}
	if rs.Version != 1 || len(rs.Rules) != 4 {
		t.Fatalf("version=%d rules=%d", rs.Version, len(rs.Rules))
	}
	r0 := rs.Rules[0]
	if !r0.Scope.IsGlobal() || r0.Match.Risk == nil || *r0.Match.Risk != RiskRead || !r0.IsEnabled() {
		t.Errorf("第 1 条解析错误: %+v", r0)
	}
	r1 := rs.Rules[1]
	if r1.Scope.Project != "~/Projects/railse-opc" || r1.Match.CommandPrefix != "git push origin" || r1.Scope.specificity() != 3 {
		t.Errorf("第 2 条解析错误: %+v", r1)
	}
	r3 := rs.Rules[3]
	if r3.Scope.Machine != "m1" || r3.IsEnabled() || r3.Action != ActionDeny || r3.Scope.specificity() != 2 {
		t.Errorf("第 4 条解析错误: %+v", r3)
	}
}

func TestRuleSetMarshalRoundTrip(t *testing.T) {
	rs, err := ParseRuleSet([]byte(validRulesYAML))
	if err != nil {
		t.Fatal(err)
	}
	out, err := rs.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "scope: global") {
		t.Errorf("全局范围应序列化为字符串 global, 得到:\n%s", out)
	}
	back, err := ParseRuleSet(out)
	if err != nil {
		t.Fatalf("重新解析失败: %v\n%s", err, out)
	}
	if !reflect.DeepEqual(rs, back) {
		t.Errorf("往返后不一致:\n原: %+v\n回: %+v", rs, back)
	}
}

func TestParseRuleSetInvalid(t *testing.T) {
	cases := []struct {
		name, yaml, wantErr string
	}{
		{"版本错误", "version: 2\nrules: []\n", "version"},
		{"缺 id", "version: 1\nrules:\n  - source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n", "缺少 id"},
		{"id 重复", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n  - id: a\n    source: user\n    scope: global\n    match: {risk: read}\n    action: allow\n", "重复"},
		{"source 错误", "version: 1\nrules:\n  - id: a\n    source: robot\n    scope: global\n    match: {risk: read}\n    action: allow\n", "source"},
		{"action 错误", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {risk: read}\n    action: maybe\n", "action"},
		{"match 为空", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {}\n    action: allow\n", "match"},
		{"正则无效", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {command_regex: \"(\"}\n    action: allow\n", "command_regex"},
		{"scope 字符串非 global", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: everywhere\n    match: {risk: read}\n    action: allow\n", "global"},
		{"scope 同时两项", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: {machine: m1, project: p}\n    match: {risk: read}\n    action: allow\n", "同时"},
		{"builtin 放行 outward", "version: 1\nrules:\n  - id: a\n    source: builtin\n    scope: global\n    match: {risk: outward}\n    action: allow\n", "user"},
		{"learned 放行 destructive", "version: 1\nrules:\n  - id: a\n    source: learned\n    scope: global\n    match: {risk: destructive}\n    action: allow\n", "user"},
		{"learned 放行无 risk", "version: 1\nrules:\n  - id: a\n    source: learned\n    scope: global\n    match: {command_prefix: ls}\n    action: allow\n", "risk"},
		{"未知风险名", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {risk: scary}\n    action: allow\n", "风险等级"},
		{"scope 未知字段", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: {projet: x}\n    match: {risk: read}\n    action: allow\n", "projet"},
		{"match 未知字段", "version: 1\nrules:\n  - id: a\n    source: learned\n    scope: global\n    match: {risk: network, comand_prefix: npm}\n    action: allow\n", "comand_prefix"},
		{"规则未知字段", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {risk: read}\n    actoin: allow\n", "actoin"},
		{"payload 错误", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {payload: shelll}\n    action: allow\n", "payload"},
		{"path_glob 无效", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: global\n    match: {path_glob: \"[\"}\n    action: allow\n", "path_glob"},
		{"scope.project 无效", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: {project: \"[\"}\n    match: {risk: read}\n    action: allow\n", "scope.project"},
		{"空文件", "", "version"},
		{"scope 空映射", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: {}\n    match: {risk: read}\n    action: allow\n", "scope 映射不能为空"},
		{"scope project 空串", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: {project: \"\"}\n    match: {risk: read}\n    action: allow\n", "值不能为空"},
		{"scope project 为 null", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: {project: ~}\n    match: {risk: read}\n    action: allow\n", "值不能为空"},
		{"scope machine 空串", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: {machine: \"\"}\n    match: {risk: read}\n    action: allow\n", "值不能为空"},
		{"scope 空字符串", "version: 1\nrules:\n  - id: a\n    source: user\n    scope: \"\"\n    match: {risk: read}\n    action: allow\n", "global"},
		{"多个文档", "version: 1\nrules: []\n---\nversion: 1\nrules: []\n", "规则文件只能有一个 YAML 文档"},
	}
	for _, c := range cases {
		_, err := ParseRuleSet([]byte(c.yaml))
		if err == nil {
			t.Errorf("%s: 应报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: 错误 %q 应包含 %q", c.name, err, c.wantErr)
		}
	}
}

func TestParseRuleSetScopeOmittedIsGlobal(t *testing.T) {
	cases := map[string]string{
		"缺 scope":    "version: 1\nrules:\n  - id: a\n    source: user\n    match: {risk: read}\n    action: allow\n",
		"scope null": "version: 1\nrules:\n  - id: a\n    source: user\n    scope: null\n    match: {risk: read}\n    action: allow\n",
	}
	for name, y := range cases {
		rs, err := ParseRuleSet([]byte(y))
		if err != nil {
			t.Errorf("%s: 不应报错: %v", name, err)
			continue
		}
		if !rs.Rules[0].Scope.IsGlobal() {
			t.Errorf("%s: 应为 global, 得到 %+v", name, rs.Rules[0].Scope)
		}
	}
}

func TestValidateRejectsBothScopeFields(t *testing.T) {
	read := RiskRead
	rs := RuleSet{Version: RuleSetVersion, Rules: []Rule{{
		ID:     "a",
		Source: SourceUser,
		Scope:  Scope{Machine: "m1", Project: "~/p"},
		Match:  Match{Risk: &read},
		Action: ActionAllow,
	}}}
	err := rs.Validate()
	if err == nil || !strings.Contains(err.Error(), "同时") {
		t.Fatalf("同时设置 machine 和 project 应报错, 得到 %v", err)
	}
}

func TestValidateLearnedShape(t *testing.T) {
	rule := func(match string) string {
		return "version: 1\nrules:\n  - id: a\n    source: learned\n    scope: global\n    match: " + match + "\n    action: allow\n"
	}
	bad := []struct {
		name, match, wantErr string
	}{
		{"risk unknown", "{payload: shell, command_prefix: ls, risk: unknown}", "risk"},
		{"risk write_outside", "{payload: file_write, path_glob: \"/tmp/**\", risk: write_outside}", "risk"},
		{"缺 payload", "{command_prefix: ls, risk: read}", "payload"},
		{"shell 缺 command_prefix", "{payload: shell, risk: read}", "command_prefix"},
		{"shell 只有正则", "{payload: shell, command_regex: \"^ls\", risk: read}", "command_prefix"},
		{"file_write 缺 path_glob", "{payload: file_write, risk: write_in_project}", "path_glob"},
		{"mcp 缺 mcp_tool", "{payload: mcp, mcp_server: github, risk: network}", "mcp_tool"},
		{"mcp 缺 mcp_server", "{payload: mcp, mcp_tool: get_issue, risk: network}", "mcp_server"},
		{"network 缺 domain", "{payload: network, risk: network}", "domain"},
		{"domain 只有顶级域", "{payload: network, domain: com, risk: network}", "domain"},
		{"domain 以点开头", "{payload: network, domain: .example.com, risk: network}", "domain"},
		{"domain 顶级域带尾点", "{payload: network, domain: \"com.\", risk: network}", "domain"},
	}
	for _, c := range bad {
		_, err := ParseRuleSet([]byte(rule(c.match)))
		if err == nil {
			t.Errorf("%s: 应报错", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantErr) {
			t.Errorf("%s: 错误 %q 应包含 %q", c.name, err, c.wantErr)
		}
	}
	good := []string{
		"{payload: shell, command_prefix: \"npm run\", risk: write_in_project}",
		"{payload: shell, command_prefix: cat, risk: read}",
		"{payload: file_write, path_glob: \"~/proj/**\", risk: write_in_project}",
		"{payload: mcp, mcp_server: github, mcp_tool: get_issue, risk: network}",
		"{payload: network, domain: example.com, risk: network}",
		"{payload: network, domain: api.Example.com., risk: network}",
	}
	for _, m := range good {
		if _, err := ParseRuleSet([]byte(rule(m))); err != nil {
			t.Errorf("%s: 不应报错: %v", m, err)
		}
	}
	// 形状要求只针对 learned 的 allow 规则
	deny := "version: 1\nrules:\n  - id: a\n    source: learned\n    scope: global\n    match: {payload: shell}\n    action: deny\n"
	if _, err := ParseRuleSet([]byte(deny)); err != nil {
		t.Errorf("learned deny 不受形状限制: %v", err)
	}
}
