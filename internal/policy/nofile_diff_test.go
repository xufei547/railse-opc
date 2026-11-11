package policy

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// 差分测试：没有角色（Context.Role 为 nil，即没有 roles.yaml）时，引擎的决定与 0.3 之前（main 上 fc14b08 的代码）逐条相同。
// 基准 testdata/nofile_decisions_main.jsonl 是用 main 上的代码跑这同一个文件生成的：
//
//	RAILSE_WRITE_DIFF_GOLDEN=$PWD/internal/policy/testdata/nofile_decisions_main.jsonl go test ./internal/policy -run TestNoRolesMatchesMain
//
// 包装命令定级取“包装自身”与“深剥后的内层”两者较高的（0.3 起，相对 0.2.3 的有意收紧，如 command git push 从 unknown 变 outward）：
// 语料里的样本都没有受影响（以 command、exec、xargs、timeout 开头而内层更高的，语料里没有），基准不变。
//
// 语料：testdata/shell_corpus.tsv 的全部命令，加上文件写入、MCP、联网几类载荷；每条在“有项目根 / 没有项目根”与“暂停 / 不暂停”下，
// 分别用内置规则包与一组带用户 allow、deny 的规则评估。

const diffUserRules = `
version: 1
rules:
  - id: builtin-readonly
    source: builtin
    scope: global
    match: {risk: read}
    action: allow
  - id: builtin-write-in-project
    source: builtin
    scope: global
    match: {risk: write_in_project}
    action: allow
  - id: user-allow-push
    source: user
    scope: global
    match: {payload: shell, command_prefix: "git push"}
    action: allow
  - id: user-allow-rm-build
    source: user
    scope: global
    match: {payload: shell, command_prefix: "rm -rf build", risk: destructive}
    action: allow
  - id: user-deny-publish
    source: user
    scope: global
    match: {payload: shell, command_prefix: "npm publish"}
    action: deny
  - id: learned-deny-curl
    source: learned
    scope: global
    match: {payload: shell, command_prefix: "curl"}
    action: deny
  - id: user-allow-gh-mcp
    source: user
    scope: global
    match: {payload: mcp, mcp_server: github}
    action: allow
`

func diffPayloads(t *testing.T) []Payload {
	t.Helper()
	f, err := os.Open("testdata/shell_corpus.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Payload
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		cmd, _, _ := strings.Cut(line, "\t")
		out = append(out, Payload{Type: PayloadShell, Command: cmd, Cwd: "/Users/u/proj"})
	}
	for _, p := range []string{"/Users/u/proj/a.txt", "a.txt", "/Users/u/other/a.txt", "/Users/u/.railse/roles.yaml", "/Users/u/proj/.env", "/etc/hosts"} {
		out = append(out, Payload{Type: PayloadFileWrite, Path: p, Cwd: "/Users/u/proj"})
	}
	out = append(out,
		Payload{Type: PayloadMCP, Server: "github", Tool: "get_issue"},
		Payload{Type: PayloadMCP, Server: "slack", Tool: "post"},
		Payload{Type: PayloadNetwork, URL: "https://example.com", Method: "GET"},
		Payload{Type: PayloadNetwork, URL: "https://api.example.com/x", Method: "POST"},
		Payload{},
	)
	return out
}

// diffDecisions 返回每条的决定（JSON，基准里只存它）与对应的“规则包 / 暂停 / 项目根 / 载荷”（不比，只用于报错）。
func diffDecisions(t *testing.T) (decisions, keys []string) {
	t.Helper()
	engines := map[string]*Engine{}
	def, err := NewEngine(DefaultRuleSet())
	if err != nil {
		t.Fatal(err)
	}
	engines["default"] = def
	engines["user"] = mustEngine(t, diffUserRules)
	for _, name := range []string{"default", "user"} {
		e := engines[name]
		for _, paused := range []bool{false, true} {
			e.SetPaused(paused)
			for _, root := range []string{"/Users/u/proj", ""} {
				ctx := Context{ProjectRoot: root, HomeDir: "/Users/u", MachineID: "m"}
				for _, p := range diffPayloads(t) {
					b, err := json.Marshal(e.Evaluate(p, ctx))
					if err != nil {
						t.Fatal(err)
					}
					// control_plane 是 0.3 新增的标记（守护进程据它不让限时放行覆盖 Railse 自己的配置，相对 0.2.3 的第二处有意收紧）：
					// 引擎的动作、风险、理由都不受它影响。这里核对它只出现在碰 .railse 的载荷上，比较前去掉。
					// 在 JSON 层处理而不碰字段，这个文件在 main 上也能编译（基准靠它生成）
					const cp = `,"control_plane":true`
					if has := strings.Contains(string(b), cp); has != (strings.Contains(p.Command, ".railse") || strings.Contains(p.Path, ".railse")) {
						t.Errorf("control_plane 不对: %+v → %s", p, b)
					}
					b = []byte(strings.Replace(string(b), cp, "", 1))
					key, _ := json.Marshal(struct {
						E string
						P bool
						R string
						X Payload
					}{name, paused, root, p})
					decisions, keys = append(decisions, string(b)), append(keys, string(key))
				}
			}
			e.SetPaused(false)
		}
	}
	return decisions, keys
}

func TestNoRolesMatchesMain(t *testing.T) {
	got, keys := diffDecisions(t)
	if path := os.Getenv("RAILSE_WRITE_DIFF_GOLDEN"); path != "" {
		if err := os.WriteFile(path, []byte(strings.Join(got, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile("testdata/nofile_decisions_main.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
	if len(want) != len(got) {
		t.Fatalf("条数不同：基准 %d，现在 %d", len(want), len(got))
	}
	diff := 0
	for i := range want {
		if want[i] != got[i] {
			if diff < 10 {
				t.Errorf("第 %d 条不同（%s）:\n main: %s\n 现在: %s", i+1, keys[i], want[i], got[i])
			}
			diff++
		}
	}
	if diff > 0 {
		t.Fatalf("共 %d 条与 main 不同", diff)
	}
	if len(got) < 1000 {
		t.Fatalf("语料太少（%d 条），差分没有意义", len(got))
	}
}
