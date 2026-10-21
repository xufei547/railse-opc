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
		{"file_write .git/config", Payload{Type: PayloadFileWrite, Path: ".git/config", Cwd: "/home/u/proj"}, RiskDestructive},
		{"file_write 子模块的 config", Payload{Type: PayloadFileWrite, Path: ".git/modules/sub/config", Cwd: "/home/u/proj"}, RiskDestructive},
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
		// 工作目录在项目外：不做路径分析的写命令改的是那个目录
		{"项目外工作目录的 make", Payload{Type: PayloadShell, Command: "make", Cwd: "/home/u/other"}, RiskWriteOutside},
		{"项目内工作目录的 make", Payload{Type: PayloadShell, Command: "make", Cwd: "/home/u/proj/sub"}, RiskWriteInProject},
		{"项目外工作目录的只读命令", Payload{Type: PayloadShell, Command: "git status", Cwd: "/home/u/other"}, RiskRead},
	}
	for _, c := range cases {
		if got := Classify(c.p, ctx); got != c.want {
			t.Errorf("%s: Classify = %s, 想要 %s", c.name, got, c.want)
		}
	}
}

// TestClassifyEmptyProjectRootFailsClosed 适配器没给项目根时 fail closed：没有路径在项目内，
// 写入一律按 write_outside；敏感路径仍是 destructive，无法解析的仍是 unknown，只读不受影响。
func TestClassifyEmptyProjectRootFailsClosed(t *testing.T) {
	ctx := Context{HomeDir: "/Users/u"}
	cwd := "/Users/u/proj"
	cases := []struct {
		name string
		p    Payload
		want Risk
	}{
		// 此前以 cwd 为根：在家目录下执行时家目录里的一切都算项目内
		{"rm 家目录里的文件", Payload{Type: PayloadShell, Command: "rm -f /Users/u/Documents/t.docx", Cwd: "/Users/u"}, RiskWriteOutside},
		{"cd 之后写", Payload{Type: PayloadShell, Command: "cd /etc && touch hosts", Cwd: cwd}, RiskWriteOutside},
		{"相对路径", Payload{Type: PayloadShell, Command: "touch a", Cwd: cwd}, RiskWriteOutside},
		{"重定向到相对路径", Payload{Type: PayloadShell, Command: "echo x > out.txt", Cwd: cwd}, RiskWriteOutside},
		{"不做路径分析的写命令", Payload{Type: PayloadShell, Command: "git commit -m x", Cwd: cwd}, RiskWriteOutside},
		{"无 Cwd 的相对路径", Payload{Type: PayloadShell, Command: "touch a"}, RiskWriteOutside},
		{"file_write 家目录里的文件", Payload{Type: PayloadFileWrite, Path: "/Users/u/Documents/t.docx", Cwd: "/Users/u"}, RiskWriteOutside},
		{"file_write 相对路径", Payload{Type: PayloadFileWrite, Path: "notes.md", Cwd: cwd}, RiskWriteOutside},
		{"file_write 无 Cwd", Payload{Type: PayloadFileWrite, Path: "notes.md"}, RiskWriteOutside},
		{"敏感路径", Payload{Type: PayloadShell, Command: "echo x >> ~/.zshrc", Cwd: cwd}, RiskDestructive},
		{"file_write 敏感路径", Payload{Type: PayloadFileWrite, Path: ".env", Cwd: cwd}, RiskDestructive},
		{"无法解析", Payload{Type: PayloadShell, Command: "touch $X", Cwd: cwd}, RiskUnknown},
		{"file_write 无法解析", Payload{Type: PayloadFileWrite, Path: "$X/a", Cwd: cwd}, RiskUnknown},
		{"只读", Payload{Type: PayloadShell, Command: "git status", Cwd: cwd}, RiskRead},
		{"联网下载到项目内的输出不再算项目内", Payload{Type: PayloadShell, Command: "curl -o a.txt https://x.y", Cwd: cwd}, RiskOutward},
	}
	for _, c := range cases {
		if got := Classify(c.p, ctx); got != c.want {
			t.Errorf("%s: Classify = %s, 想要 %s", c.name, got, c.want)
		}
	}
	// 内置规则包不再自动放行
	e, err := NewEngine(DefaultRuleSet())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []Payload{
		{Type: PayloadShell, Command: "rm -f /Users/u/Documents/t.docx", Cwd: "/Users/u"},
		{Type: PayloadFileWrite, Path: "/Users/u/Documents/t.docx", Cwd: "/Users/u"},
		{Type: PayloadShell, Command: "touch a", Cwd: cwd},
	} {
		if d := e.Evaluate(p, ctx); d.Action != ActionAsk || d.Risk != RiskWriteOutside {
			t.Errorf("无项目根时 %+v 应询问, 得到 %+v", p, d)
		}
	}
}
