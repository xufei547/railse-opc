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
		{"file_write - 开头的敏感文件", Payload{Type: PayloadFileWrite, Path: "-key.pem", Cwd: "/home/u/proj"}, RiskDestructive},
		{"file_write - 开头跳到 ~/.ssh", Payload{Type: PayloadFileWrite, Path: "-x/../../../../home/u/.ssh/authorized_keys", Cwd: "/home/u/proj"}, RiskDestructive},
		{"file_write - 开头跳到项目外", Payload{Type: PayloadFileWrite, Path: "-x/../../../../etc/hosts", Cwd: "/home/u/proj"}, RiskWriteOutside},
		{"file_write & 开头是普通文件名", Payload{Type: PayloadFileWrite, Path: "&notes.txt", Cwd: "/home/u/proj"}, RiskWriteInProject},
		{"file_write 伪设备不低于项目内写", Payload{Type: PayloadFileWrite, Path: "/dev/null", Cwd: "/home/u/proj"}, RiskWriteInProject},
		{"file_write 含变量", Payload{Type: PayloadFileWrite, Path: "$FOO/x", Cwd: "/home/u/proj"}, RiskUnknown},
		{"file_write 相对路径无 Cwd 用 ProjectRoot", Payload{Type: PayloadFileWrite, Path: "docs/x.md"}, RiskWriteInProject},
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
