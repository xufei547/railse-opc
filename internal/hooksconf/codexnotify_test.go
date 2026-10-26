package hooksconf

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// 顶层（第一个表头之前）的 notify：数组可以跨行、有注释、逗号结尾、单双引号混用，基本字符串按 TOML 解开转义；
// 别的键的值（多行数组、多行字符串、内联表）里像表头或 notify 的内容不算。
func TestParseCodexNotify(t *testing.T) {
	cases := []struct {
		name, doc string
		found     bool
		argv      []string
		tables    []string
	}{
		{name: "空文件", doc: ""},
		{name: "只有注释与空行", doc: "# 配置\n\n  # 缩进的注释\n"},
		{name: "单行", doc: `notify = ["/usr/local/bin/railse", "hook", "codex-notify"]` + "\n", found: true, argv: []string{"/usr/local/bin/railse", "hook", "codex-notify"}},
		{name: "没有结尾换行", doc: `notify=['/a b/x']`, found: true, argv: []string{"/a b/x"}},
		{name: "空数组", doc: "notify = []\n", found: true, argv: []string{}},
		{
			name: "跨行、注释、逗号结尾、单双引号混用",
			doc: "model = \"o3\"\n" +
				"notify = [ # 桌面通知\n" +
				"  \"/usr/local/bin/notify-wrap\", # 程序\n" +
				"  '--title=C:\\x',\n" +
				"\n" +
				"  \"say \\\"hi\\\"\\t\\\\ \\u00e9\\U0001F600\",\n" +
				"] # 结尾注释\n" +
				"[tui]\nnotifications = true\n",
			found: true,
			argv:  []string{"/usr/local/bin/notify-wrap", `--title=C:\x`, "say \"hi\"\t\\ é😀"},
		},
		{
			name: "别的值里像表头、像 notify 的内容不算",
			doc: "args = [\n  [1, 2],\n  \"]\",\n]\n" +
				"desc = \"\"\"\n[not.a.table]\nnotify = [\"x\"]\n\"\"\"\n" +
				"lit = '''\n[x]\n'''\n" +
				"env = { a = \"[\", b = [ \"}\" ] }\n" +
				"when = 1979-05-27 07:32:00Z # 日期时间里有空格\n" +
				"n = 1_000\n" +
				"notify = [\"/bin/n\"]\n",
			found: true,
			argv:  []string{"/bin/n"},
		},
		{name: "CRLF", doc: "a = 1\r\nnotify = [\r\n  \"/bin/n\",\r\n]\r\n[t]\r\n", found: true, argv: []string{"/bin/n"}},
		{
			name:   "只在表下面",
			doc:    "model = \"x\"\n[mcp_servers.foo]\ncommand = \"foo\"\nnotify = [\"/usr/local/bin/railse\", \"hook\", \"codex-notify\"]\n[[profiles]]\nnotify = 1\n",
			tables: []string{"[mcp_servers.foo]", "[[profiles]]"},
		},
		{
			name:   "顶层与表下面都有",
			doc:    "notify = [\"/bin/top\"]\n[projects.\"/Users/me/My ]Proj\"]\ntrust_level = \"trusted\"\nnotify = [\"/bin/t\"]\n",
			found:  true,
			argv:   []string{"/bin/top"},
			tables: []string{`[projects."/Users/me/My ]Proj"]`},
		},
		{name: "别的键名里有 notify", doc: "notify_x = 1\ntui.notify = true\nnotify = [\"/bin/n\"]\n", found: true, argv: []string{"/bin/n"}},
	}
	for _, tc := range cases {
		n, err := ParseCodexNotify([]byte(tc.doc))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if n.Found != tc.found || !slices.Equal(n.Argv, tc.argv) || !slices.Equal(n.Tables, tc.tables) {
			t.Fatalf("%s: Found=%v Argv=%q Tables=%q，想要 %v %q %q", tc.name, n.Found, n.Argv, n.Tables, tc.found, tc.argv, tc.tables)
		}
	}
}

// 看不懂的写法一律报错（带行号），调用方据此什么都不写。
func TestParseCodexNotifyRejects(t *testing.T) {
	for _, doc := range []string{
		`notify = """/bin/x"""`,
		`notify = ['''/bin/x''']`,
		`notify = ["""/bin/x"""]`,
		`notify = ["/bin/x", 1]`,
		`notify = ["/bin/x", ["y"]]`,
		"notify = [\"/bin/x\",\n",
		`notify = ["/bin/x" "y"]`,
		`notify = "/bin/x"`,
		"notify =\n[\"/bin/x\"]",
		`"notify" = ["/bin/x"]`,
		`notify.x = "y"`,
		"notify = [\"/a\"]\nnotify = [\"/b\"]\n",
		`notify = ["\q"]`,
		`notify = ["\u12"]`,
		`notify = ["\uD800"]`,
		"notify = [\"a\x01b\"]",
		`notify = ["/bin/x"] extra`,
		"a = \"没闭合\nnotify = [\"/bin/x\"]\n",
		"a = \"\"\"没闭合\n",
		"a = [1, 2\n",
		"[tui\nx = 1\n",
		"= 1\n",
		"a 1\n",
	} {
		if n, err := ParseCodexNotify([]byte(doc)); err == nil {
			t.Fatalf("%q 应报错，得到 %+v", doc, n)
		} else if !strings.HasPrefix(err.Error(), "第 ") {
			t.Fatalf("%q 的错误应带行号: %v", doc, err)
		}
	}
}

// SetCodexNotify 只换顶层 notify 从键到 ] 那一段，其余字节不变；没有顶层 notify 时在文件最前面插一行。路径按 TOML 基本字符串转义。
func TestSetCodexNotify(t *testing.T) {
	rest := "model = \"o3\" # 模型\n[tui]\nnotifications = true\n"
	old := "notify = [ # 旧的\n  \"/usr/local/bin/notify-wrap\",\n  '--quiet',\n]"
	doc := "# 我的配置\n" + old + " # 尾注释\n" + rest
	got, err := SetCodexNotify([]byte(doc), []string{"/Users/me/.railse/codex-notify.sh"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "# 我的配置\n" + `notify = ["/Users/me/.railse/codex-notify.sh"]` + " # 尾注释\n" + rest; string(got) != want {
		t.Fatalf("只该换 notify 那一段:\n%s\n---\n%s", got, want)
	}
	// 再换一次：不变
	again, err := SetCodexNotify(got, []string{"/Users/me/.railse/codex-notify.sh"})
	if err != nil || string(again) != string(got) {
		t.Fatalf("同样的 notify 再写一次应不变: %v\n%s", err, again)
	}
	// 没有顶层 notify（只在表下面的不算）：最前面插一行
	under := "[mcp_servers.foo]\nnotify = [\"/x\"]\n"
	got, err = SetCodexNotify([]byte(under), []string{"/s.sh"})
	if err != nil || string(got) != "notify = [\"/s.sh\"]\n"+under {
		t.Fatalf("应在最前面插一行: %v\n%s", err, got)
	}
	if got, _ := SetCodexNotify(nil, []string{"/s.sh"}); string(got) != "notify = [\"/s.sh\"]\n" {
		t.Fatalf("空文件: %q", got)
	}
	// 引号、反斜杠、控制字符转义后读回来一样
	odd := "/tmp/a \"b\"\\c\td\x01é/codex-notify.sh"
	got, err = SetCodexNotify(nil, []string{odd})
	if err != nil {
		t.Fatal(err)
	}
	if want := `notify = ["/tmp/a \"b\"\\c\td\u0001é/codex-notify.sh"]` + "\n"; string(got) != want {
		t.Fatalf("转义: %q，想要 %q", got, want)
	}
	if n, err := ParseCodexNotify(got); err != nil || !slices.Equal(n.Argv, []string{odd}) {
		t.Fatalf("读回来应是原路径: %v %q", err, n.Argv)
	}
	if _, err := SetCodexNotify([]byte(`notify = """x"""`), []string{"/s.sh"}); err == nil {
		t.Fatal("看不懂的写法应报错")
	}
}

// 串接脚本的内容固定：标记行记前一跳（单行 JSON，不转义 HTML 字符），前一跳与 railse 各一行，参数经 ShellQuote，最后 exit 0。
func TestNotifyScript(t *testing.T) {
	head := "#!/bin/sh\n" +
		"# 由 railse setup codex-notify 生成，重跑会重新生成，不要手动改。\n" +
		"# Codex 把通知 JSON 作为最后一个参数传给 notify 命令；这里原样（\"$@\"）交给下面每个命令，一个失败不影响另一个。\n"
	got := string(NotifyScript([]string{"/usr/local/bin/notify-wrap", "--quiet", "a&b <c>"}, "/Users/me/.local/bin/railse", ""))
	want := head +
		`# railse-prev-notify: ["/usr/local/bin/notify-wrap","--quiet","a&b <c>"]` + "\n" +
		`/usr/local/bin/notify-wrap --quiet 'a&b <c>' "$@"` + "\n" +
		`/Users/me/.local/bin/railse hook codex-notify "$@"` + "\n" +
		"exit 0\n"
	if got != want {
		t.Fatalf("脚本:\n%s\n---\n%s", got, want)
	}
	got = string(NotifyScript(nil, "/my bin/railse", "/data dir/agent.sock"))
	want = head + "# railse-prev-notify: []\n" + `'/my bin/railse' hook codex-notify --socket '/data dir/agent.sock' "$@"` + "\nexit 0\n"
	if got != want {
		t.Fatalf("没有前一跳、带 --socket:\n%s\n---\n%s", got, want)
	}

	// 读回标记行与 railse 那一行
	prev := []string{"/usr/local/bin/notify-wrap", "it's", ""}
	script := NotifyScript(prev, "/my bin/railse", "/data dir/agent.sock")
	p, marked, err := ParseNotifyScript(script)
	if err != nil || !marked || !slices.Equal(p, prev) {
		t.Fatalf("标记行: %q %v %v", p, marked, err)
	}
	c, ok := NotifyScriptHook(script)
	if !ok || c.Program != "/my bin/railse" || c.CLI != "codex-notify" || c.Socket != "/data dir/agent.sock" {
		t.Fatalf("railse 那一行: %+v %v", c, ok)
	}
	if c, ok := NotifyScriptHook(NotifyScript(nil, "/opt/railse", "")); !ok || c.Program != "/opt/railse" || c.Socket != "" {
		t.Fatalf("不带 --socket: %+v %v", c, ok)
	}
	if p, marked, err := ParseNotifyScript(NotifyScript(nil, "/opt/railse", "")); err != nil || !marked || len(p) != 0 {
		t.Fatalf("没有前一跳的标记行: %q %v %v", p, marked, err)
	}
	// 别人的脚本：没有标记行，也没有 railse 那一行
	other := []byte("#!/bin/sh\nterminal-notifier -message \"$1\"\n")
	if _, marked, err := ParseNotifyScript(other); marked || err != nil {
		t.Fatalf("别人的脚本不该认成我们的: %v %v", marked, err)
	}
	if _, ok := NotifyScriptHook(other); ok {
		t.Fatal("别人的脚本里没有 railse 那一行")
	}
	if _, marked, err := ParseNotifyScript([]byte("# railse-prev-notify: [坏\n")); !marked || err == nil {
		t.Fatalf("标记行的 JSON 坏了应报错: %v %v", marked, err)
	}
	// ParseCommand 照旧只认 claude、codex、gemini
	if _, ok := ParseCommand("/opt/railse hook codex-notify"); ok {
		t.Fatal("ParseCommand 不该认 codex-notify")
	}
}

func TestCodexConfigPath(t *testing.T) {
	if got := CodexConfigPath("/h"); got != filepath.Join("/h", ".codex", "config.toml") {
		t.Fatal(got)
	}
}
