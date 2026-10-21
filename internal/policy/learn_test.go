package policy

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestProposeRuleShell(t *testing.T) {
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u", MachineID: "m1"}
	r, err := ProposeRule(Payload{Type: PayloadShell, Command: "FOO=1 git commit -m 'x y'", Cwd: "/home/u/proj"}, ctx, "6f1c2d3e-aaaa")
	if err != nil {
		t.Fatal(err)
	}
	if r.ID != "learned-6f1c2d3e" || r.Source != SourceLearned || r.Action != ActionAllow || r.CreatedFrom != "6f1c2d3e-aaaa" {
		t.Errorf("基本字段错误: %+v", r)
	}
	if r.Scope.Project != "/home/u/proj" {
		t.Errorf("范围应是当前项目, 得到 %+v", r.Scope)
	}
	if r.Match.Payload != PayloadShell || r.Match.CommandPrefix != "git commit" || r.Match.Risk == nil || *r.Match.Risk != RiskWriteInProject {
		t.Errorf("匹配条件错误: %+v", r.Match)
	}
	// 草案必须能通过校验，并且真的能命中原请求
	rs := RuleSet{Version: 1, Rules: []Rule{r}}
	e, err := NewEngine(rs)
	if err != nil {
		t.Fatal(err)
	}
	if d := e.Evaluate(Payload{Type: PayloadShell, Command: "git commit -m z", Cwd: "/home/u/proj"}, ctx); d.Action != ActionAllow || d.RuleID != r.ID {
		t.Errorf("学来的规则应命中同类命令, 得到 %+v", d)
	}
}

func TestProposeRuleRefusesOutwardAndDestructive(t *testing.T) {
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}
	for _, cmd := range []string{"git push origin main", "rm -rf x", "cat .env",
		// 同时含通配或命令替换时，风险门槛先于形状检查
		"rm -rf *.log", "rm -rf $(pwd)"} {
		_, err := ProposeRule(Payload{Type: PayloadShell, Command: cmd, Cwd: "/home/u/proj"}, ctx, "id")
		if !errors.Is(err, ErrCannotLearn) {
			t.Errorf("%q 应返回 ErrCannotLearn, 得到 %v", cmd, err)
		}
	}
}

func TestProposeRuleRejectsCompoundAndEmpty(t *testing.T) {
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}
	if _, err := ProposeRule(Payload{Type: PayloadShell, Command: "ls && pwd", Cwd: "/home/u/proj"}, ctx, "id"); err == nil {
		t.Error("复合命令应报错")
	}
	if _, err := ProposeRule(Payload{Type: PayloadShell, Command: "   ", Cwd: "/home/u/proj"}, ctx, "id"); err == nil {
		t.Error("空命令应报错")
	}
}

func TestProposeRuleOtherPayloads(t *testing.T) {
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}
	r, err := ProposeRule(Payload{Type: PayloadFileWrite, Path: "docs/a.md", Cwd: "/home/u/proj"}, ctx, "f1")
	if err != nil || r.Match.PathGlob != "/home/u/proj/docs/**" {
		t.Errorf("file_write: %+v, %v", r.Match, err)
	}
	r, err = ProposeRule(Payload{Type: PayloadMCP, Server: "github", Tool: "get_issue"}, ctx, "m1")
	if err != nil || r.Match.MCPServer != "github" || r.Match.MCPTool != "get_issue" {
		t.Errorf("mcp: %+v, %v", r.Match, err)
	}
	r, err = ProposeRule(Payload{Type: PayloadNetwork, URL: "https://api.example.com/v1"}, ctx, "n1")
	if err != nil || r.Match.Domain != "api.example.com" {
		t.Errorf("network: %+v, %v", r.Match, err)
	}
	if _, err = ProposeRule(Payload{Type: PayloadNetwork, URL: "not a url"}, ctx, "n2"); err == nil {
		t.Error("无效 URL 应报错")
	}
	// 没有项目根时范围为 global
	r, err = ProposeRule(Payload{Type: PayloadMCP, Server: "s", Tool: "t"}, Context{HomeDir: "/home/u"}, "m2")
	if err != nil || !r.Scope.IsGlobal() {
		t.Errorf("无项目时应为 global: %+v, %v", r.Scope, err)
	}
}

var learnCtx = Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}

// assertLearnedAllows 把学来的规则单独装进引擎，确认它放行原请求，即规则不是死的。
func assertLearnedAllows(t *testing.T, r Rule, p Payload, ctx Context) {
	t.Helper()
	e, err := NewEngine(RuleSet{Version: 1, Rules: []Rule{r}})
	if err != nil {
		t.Fatal(err)
	}
	if d := e.Evaluate(p, ctx); d.Action != ActionAllow || d.RuleID != r.ID {
		t.Errorf("学来的规则 %+v 应放行原请求 %+v, 得到 %+v", r.Match, p, d)
	}
}

func TestProposeRuleRefusesNonLearnableRisk(t *testing.T) {
	// 只有 read、write_in_project、network 能学：学来的前缀表达不了项目外的路径或无法解析的参数
	for _, cmd := range []string{"cp a /tmp/b", "foobar", "GIT_ASKPASS=x git status"} {
		_, err := ProposeRule(shell(cmd), learnCtx, "id")
		if !errors.Is(err, ErrCannotLearn) {
			t.Errorf("%q 应返回 ErrCannotLearn, 得到 %v", cmd, err)
		}
	}
}

func TestProposeRuleNeedsProjectForShellAndFile(t *testing.T) {
	// 没有项目根时 Classify 以 cwd 为根，学出来的 global 规则会放行 cwd 下的任意改动：命令与文件规则不学
	noProj := Context{HomeDir: "/home/u"}
	for _, p := range []Payload{
		{Type: PayloadShell, Command: "git status", Cwd: "/home/u"},
		{Type: PayloadShell, Command: "touch notes.md", Cwd: "/home/u"},
		{Type: PayloadFileWrite, Path: "notes.md", Cwd: "/home/u"},
	} {
		_, err := ProposeRule(p, noProj, "id")
		if err == nil || !strings.Contains(err.Error(), "没有项目根目录") {
			t.Errorf("%+v 应因没有项目根报错, 得到 %v", p, err)
		}
	}
	// mcp 与 network 仍可学成 global
	for _, p := range []Payload{
		{Type: PayloadMCP, Server: "s", Tool: "t"},
		{Type: PayloadNetwork, URL: "https://api.example.com/v1"},
	} {
		r, err := ProposeRule(p, noProj, "id")
		if err != nil || !r.Scope.IsGlobal() {
			t.Errorf("%+v 无项目时应学成 global: %+v, %v", p, r.Scope, err)
			continue
		}
		assertLearnedAllows(t, r, p, noProj)
	}
}

func TestProposeRuleRejectsDeadShellRules(t *testing.T) {
	// 引擎的放行检查永远不会命中这些命令，学出来的规则是死的：报描述性错误，而不是 ErrCannotLearn
	for _, cmd := range []string{
		"ls *.go",           // 引号外的通配
		"echo $X",           // $ 展开
		"echo hi > out.txt", // 输出重定向到文件
		"cat < a.txt",       // 输入重定向读文件
		"echo $(date)",      // 命令替换
		"FOO=$X git status", // 包装里的 $ 展开
	} {
		_, err := ProposeRule(shell(cmd), learnCtx, "id")
		if err == nil || errors.Is(err, ErrCannotLearn) {
			t.Errorf("%q 应返回描述性错误, 得到 %v", cmd, err)
		}
	}
}

func TestProposeRuleShellLearnable(t *testing.T) {
	cases := []struct {
		cmd, prefix string
		risk        Risk
	}{
		{"cat a 2>/dev/null", "cat a", RiskRead}, // 重定向到无害伪设备不影响
		{"curl https://x.y", "curl https://x.y", RiskNetwork},
		{"ls", "ls", RiskRead},
		{"ls -la", "ls -la", RiskRead},          // 只读命令没有操作数也能学
		{"git log '*.go'", "git log", RiskRead}, // 引号里的通配是字面量
		// 改动路径的命令取整条命令
		{"rm -f build.log", "rm -f build.log", RiskWriteInProject},
		{"cp a b", "cp a b", RiskWriteInProject},
		{"FOO=1 /bin/mv a b", "/bin/mv a b", RiskWriteInProject},
		// 其余取前两个词；有全局选项表的命令取到子命令为止（npm、go 的第二个词就是子命令）
		{"npm run build", "npm run", RiskWriteInProject},
		{"go test -v ./...", "go test", RiskRead},
		{"git -C sub status", "git -C sub status", RiskRead},
		// 第二个词是选项时取到选项之后的第一个非选项词
		{"ls -la src", "ls -la src", RiskRead},
		{"python3 -m pytest -q", "python3 -m pytest", RiskRead},
		{"sh -c 'ls'", "sh -c ls", RiskRead},
		// 解释器按选项表跳过选项的参数，前缀含要运行的脚本
		{"python3 -X dev app.py", "python3 -X dev app.py", RiskWriteInProject},
		{"node --inspect app.js", "node --inspect app.js", RiskWriteInProject},
		{"deno run --allow-read app.ts", "deno run --allow-read app.ts", RiskWriteInProject},
	}
	for _, c := range cases {
		p := shell(c.cmd)
		r, err := ProposeRule(p, learnCtx, "s-"+c.cmd)
		if err != nil {
			t.Errorf("%q: %v", c.cmd, err)
			continue
		}
		if r.Match.CommandPrefix != c.prefix || r.Match.Risk == nil || *r.Match.Risk != c.risk {
			t.Errorf("%q: 得到 %+v", c.cmd, r.Match)
		}
		assertLearnedAllows(t, r, p, learnCtx)
	}
}

func TestProposeRuleNetworkHost(t *testing.T) {
	p := Payload{Type: PayloadNetwork, URL: "https://example.com/"}
	r, err := ProposeRule(p, learnCtx, "n1")
	if err != nil || r.Match.Domain != "example.com" || r.Match.Risk == nil || *r.Match.Risk != RiskNetwork {
		t.Fatalf("network: %+v, %v", r.Match, err)
	}
	assertLearnedAllows(t, r, p, learnCtx)
	// 只有一级的主机名过不了校验
	if _, err := ProposeRule(Payload{Type: PayloadNetwork, URL: "https://com/"}, learnCtx, "n2"); err == nil {
		t.Error("单级主机名应报错")
	}
}

func TestProposeRuleOtherPayloadsAreLive(t *testing.T) {
	for _, p := range []Payload{
		{Type: PayloadFileWrite, Path: "docs/a.md", Cwd: "/home/u/proj"},
		{Type: PayloadMCP, Server: "github", Tool: "get_issue"},
		{Type: PayloadNetwork, URL: "https://api.example.com/v1"},
	} {
		r, err := ProposeRule(p, learnCtx, "x")
		if err != nil {
			t.Errorf("%+v: %v", p, err)
			continue
		}
		assertLearnedAllows(t, r, p, learnCtx)
	}
	// 缺 tool 的 mcp 载荷过不了校验
	if _, err := ProposeRule(Payload{Type: PayloadMCP, Server: "github"}, learnCtx, "x"); err == nil {
		t.Error("缺 tool 的 mcp 应报错")
	}
	if _, err := ProposeRule(Payload{Type: "ftp"}, learnCtx, "x"); err == nil {
		t.Error("未知载荷类型应报错")
	}
}

func TestProposeRuleEscapesLiteralPaths(t *testing.T) {
	// 工作目录与项目根里的通配元字符按字面处理：规则只命中原目录与原项目
	ctx := Context{ProjectRoot: "/home/u/p[1]", HomeDir: "/home/u"}
	p := Payload{Type: PayloadFileWrite, Path: "x.md", Cwd: "/home/u/p[1]/a{b,c}"}
	r, err := ProposeRule(p, ctx, "f1")
	if err != nil {
		t.Fatal(err)
	}
	if r.Match.PathGlob != `/home/u/p\[1\]/a\{b,c\}/**` || r.Scope.Project != `/home/u/p\[1\]` {
		t.Errorf("应转义元字符, 得到 %+v %+v", r.Scope, r.Match)
	}
	assertLearnedAllows(t, r, p, ctx)
	e, err := NewEngine(RuleSet{Version: 1, Rules: []Rule{r}})
	if err != nil {
		t.Fatal(err)
	}
	if d := e.Evaluate(Payload{Type: PayloadFileWrite, Path: "x.md", Cwd: "/home/u/p[1]/ab"}, ctx); d.Action == ActionAllow {
		t.Errorf("不应命中别的目录: %+v", d)
	}

	mcp := Payload{Type: PayloadMCP, Server: "s", Tool: "t"}
	r, err = ProposeRule(mcp, ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	assertLearnedAllows(t, r, mcp, ctx)
	if e, err = NewEngine(RuleSet{Version: 1, Rules: []Rule{r}}); err != nil {
		t.Fatal(err)
	}
	if d := e.Evaluate(mcp, Context{ProjectRoot: "/home/u/p1", HomeDir: "/home/u"}); d.Action == ActionAllow {
		t.Errorf("不应命中别的项目: %+v", d)
	}
}

func TestProposeRuleFileOpsLearnWholeCommand(t *testing.T) {
	// 批准删一个文件不等于批准删项目里任意文件
	cases := []struct{ learn, other string }{
		{"rm -f build.log", "rm -f src/main.go"},
		{"cp a b", "cp a src/main.go"},
		{"mv a b", "mv a src/main.go"},
		{"sed -i s/a/b/ x.txt", "sed -i s/x/y/ src/main.go"},
		// 整条命令之后再加操作数也不行
		{"rm -f build.log", "rm -f build.log src/main.go"},
		{"tee out.txt", "tee out.txt src/main.go"},
		// 联网与远程命令、shell 调用、命令表里做路径分析的命令同样取整条命令
		{"curl --max-time 5 https://x.y", "curl --max-time 5 https://evil.example/x"},
		{"curl https://x.y", "curl https://x.y https://evil.example/x"},
		{"ssh build uptime", "ssh build 'rm -rf ~'"},
		{"bash -c 'rm -f build.log'", "bash -c 'rm -f build.log src/main.go'"},
		{"yq -i '.v = 1' chart.yaml", "yq -i '.v = 1' package.json"},
		{"dig +short api.example.com", "dig +short evil.example"},
	}
	for _, c := range cases {
		r, err := ProposeRule(shell(c.learn), learnCtx, "id")
		if err != nil {
			t.Errorf("%q: %v", c.learn, err)
			continue
		}
		assertLearnedAllows(t, r, shell(c.learn), learnCtx)
		e, err := NewEngine(RuleSet{Version: 1, Rules: []Rule{r}})
		if err != nil {
			t.Fatal(err)
		}
		if d := e.Evaluate(shell(c.other), learnCtx); d.Action != ActionAsk {
			t.Errorf("从 %q 学来的规则不应放行 %q: %+v", c.learn, c.other, d)
		}
	}
}

func TestProposeRuleFileOpsAnchoredRegex(t *testing.T) {
	// 改动路径的命令用锚定原文的正则限定整条命令；引擎对 allow 规则按段的原文匹配正则，
	// 所以正则取原文（含包装与引号），原命令照样放行
	for _, cmd := range []string{"rm -f build.log", "FOO=1 /bin/mv a b", "touch 'my file.txt'",
		"curl https://x.y", "ssh build uptime", "bash script.sh", "sh -c 'ls'",
		"prettier --write src", // 命令表里 paths: true 的行，不在手写列表里
	} {
		p := shell(cmd)
		r, err := ProposeRule(p, learnCtx, "id")
		if err != nil {
			t.Errorf("%q: %v", cmd, err)
			continue
		}
		if want := "^" + regexp.QuoteMeta(cmd) + "$"; r.Match.CommandRegex != want {
			t.Errorf("%q: command_regex = %q, 想要 %q", cmd, r.Match.CommandRegex, want)
		}
		assertLearnedAllows(t, r, p, learnCtx)
	}
	// 其余命令只用前缀
	r, err := ProposeRule(shell("npm run build"), learnCtx, "id")
	if err != nil || r.Match.CommandRegex != "" {
		t.Errorf("npm run build 不应带 command_regex: %+v, %v", r.Match, err)
	}
}

func TestProposeRuleRefusesOneWordWrite(t *testing.T) {
	// 选项之后没有操作数的前缀只在只读时能学：make、git -C sub 这样的前缀会放行这个命令的一切写操作
	for _, cmd := range []string{"make", "git", "yarn", "git -C sub", "make -j4", "rm -f", "npm --prefix x"} {
		_, err := ProposeRule(shell(cmd), learnCtx, "id")
		if err == nil || errors.Is(err, ErrCannotLearn) {
			t.Errorf("%q 应返回描述性错误, 得到 %v", cmd, err)
		}
	}
}

func TestProposeRuleRootFileIsExact(t *testing.T) {
	// 项目根下的文件只学精确路径，不学整个项目
	p := Payload{Type: PayloadFileWrite, Path: "README.md", Cwd: "/home/u/proj"}
	r, err := ProposeRule(p, learnCtx, "f1")
	if err != nil || r.Match.PathGlob != "/home/u/proj/README.md" {
		t.Fatalf("README.md: %+v, %v", r.Match, err)
	}
	assertLearnedAllows(t, r, p, learnCtx)
	e, err := NewEngine(RuleSet{Version: 1, Rules: []Rule{r}})
	if err != nil {
		t.Fatal(err)
	}
	for _, other := range []string{".vscode/tasks.json", ".github/workflows/x.yml", "src/main.go"} {
		if d := e.Evaluate(Payload{Type: PayloadFileWrite, Path: other, Cwd: "/home/u/proj"}, learnCtx); d.Action != ActionAsk {
			t.Errorf("从 README.md 学来的规则不应放行 %s: %+v", other, d)
		}
	}
	// 根目录本身与根里含元字符时同样取转义后的精确路径
	ctx := Context{ProjectRoot: "/home/u/p[1]", HomeDir: "/home/u"}
	p = Payload{Type: PayloadFileWrite, Path: "x.md", Cwd: "/home/u/p[1]"}
	if r, err = ProposeRule(p, ctx, "f2"); err != nil || r.Match.PathGlob != `/home/u/p\[1\]/x.md` {
		t.Fatalf("x.md: %+v, %v", r.Match, err)
	}
	assertLearnedAllows(t, r, p, ctx)
	p = Payload{Type: PayloadFileWrite, Path: ".", Cwd: "/home/u/proj"}
	if r, err = ProposeRule(p, learnCtx, "f3"); err != nil || r.Match.PathGlob != "/home/u/proj" {
		t.Fatalf("项目根本身应取精确路径: %+v, %v", r.Match, err)
	}
	assertLearnedAllows(t, r, p, learnCtx)
}
