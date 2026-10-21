package policy

import "testing"

func TestClassify(t *testing.T) {
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}
	cases := []struct {
		name string
		p    Payload
		want Risk
	}{
		{"shell 用 Cwd", Payload{Type: PayloadShell, Command: "git status", Cwd: "/home/u/proj"}, RiskRead},
		{"shell 无 Cwd 用 ProjectRoot", Payload{Type: PayloadShell, Command: "touch a"}, RiskWriteInProject},
		{"file_write 项目内", Payload{Type: PayloadFileWrite, Path: "src/a.go", Cwd: "/home/u/proj"}, RiskWriteInProject},
		{"file_write 项目外", Payload{Type: PayloadFileWrite, Path: "/etc/hosts"}, RiskWriteOutside},
		{"file_write 敏感", Payload{Type: PayloadFileWrite, Path: ".env", Cwd: "/home/u/proj"}, RiskDestructive},
		{"file_write 无路径", Payload{Type: PayloadFileWrite}, RiskUnknown},
		{"mcp", Payload{Type: PayloadMCP, Server: "github", Tool: "create_issue"}, RiskNetwork},
		{"network", Payload{Type: PayloadNetwork, URL: "https://example.com"}, RiskNetwork},
		{"未知类型", Payload{Type: "bogus"}, RiskUnknown},
	}
	for _, c := range cases {
		if got := Classify(c.p, ctx); got != c.want {
			t.Errorf("%s: Classify = %s, 想要 %s", c.name, got, c.want)
		}
	}
}
