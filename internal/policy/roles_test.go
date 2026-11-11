package policy

import (
	"strings"
	"testing"
	"time"
)

// specRoles 是规格 §2 的样例，原样抄来：解析它不能报错。
const specRoles = `
version: 1
roles:
  - name: 开发
    note: 写代码、跑测试
    dirs: ["~/RailseWork", "~/我的程序/**"]
    ceiling: network          # 超过这一档（outward、destructive）一律问人
    forbid:
      - {command_prefix: "fly "}
    trust_max: 24h
  - name: 写作
    note: 只改文档
    dirs: ["~/dev/*/docs"]
    ceiling: write_in_project
    forbid:
      - {command_prefix: "git push"}
      - {payload: mcp}
    trust_max: 2h
  - name: 运维
    note: 发版、部署、装机
    ceiling: destructive
`

func TestParseRoleSetSpecExample(t *testing.T) {
	rs, err := ParseRoleSet([]byte(specRoles))
	if err != nil {
		t.Fatal(err)
	}
	if len(rs.Roles) != 3 {
		t.Fatalf("角色数 %d，想要 3", len(rs.Roles))
	}
	dev, writer, ops := rs.Find("开发"), rs.Find("写作"), rs.Find("运维")
	if dev == nil || writer == nil || ops == nil {
		t.Fatalf("Find 找不到角色: %v %v %v", dev, writer, ops)
	}
	if dev.Ceiling == nil || *dev.Ceiling != RiskNetwork {
		t.Errorf("开发的 ceiling = %v，想要 network", dev.Ceiling)
	}
	if !dev.TrustMax.Set || dev.TrustMax.None || dev.TrustMax.D != 24*time.Hour {
		t.Errorf("开发的 trust_max = %+v", dev.TrustMax)
	}
	if len(writer.Forbid) != 2 || writer.Forbid[1].Payload != PayloadMCP {
		t.Errorf("写作的 forbid = %+v", writer.Forbid)
	}
	if ops.TrustMax.Set || len(ops.Dirs) != 0 || len(ops.Forbid) != 0 {
		t.Errorf("运维不该有 trust_max、dirs、forbid: %+v", ops)
	}
	if rs.Find("不存在") != nil || rs.Find("") != nil {
		t.Error("Find 对不存在的名字应返回 nil")
	}
}

func TestParseRoleSetTrustMaxNone(t *testing.T) {
	rs, err := ParseRoleSet([]byte("version: 1\nroles:\n  - name: a\n    trust_max: none\n"))
	if err != nil {
		t.Fatal(err)
	}
	if tm := rs.Roles[0].TrustMax; !tm.Set || !tm.None {
		t.Errorf("trust_max none 解析成 %+v", tm)
	}
}

func TestParseRoleSetEmptyRolesOK(t *testing.T) {
	for _, text := range []string{"version: 1\n", "version: 1\nroles: []\n"} {
		rs, err := ParseRoleSet([]byte(text))
		if err != nil {
			t.Errorf("%q: %v", text, err)
		}
		if len(rs.Roles) != 0 {
			t.Errorf("%q: 想要零个角色", text)
		}
	}
}

func TestParseRoleSetRejects(t *testing.T) {
	cases := []struct {
		name, text, want string
	}{
		{"空文件", "", "version"},
		{"版本不对", "version: 2\nroles: []\n", "version"},
		{"顶层未知字段", "version: 1\nrole: []\n", "role"},
		{"角色未知字段", "version: 1\nroles:\n  - name: a\n    ceilng: read\n", "ceilng"},
		{"forbid 未知字段", "version: 1\nroles:\n  - name: a\n    forbid:\n      - {command_prefx: x}\n", "command_prefx"},
		{"多文档", "version: 1\nroles: []\n---\nversion: 1\n", "一个 YAML 文档"},
		{"缺名字", "version: 1\nroles:\n  - note: x\n", "名字"},
		{"名字含空格", "version: 1\nroles:\n  - name: \"a b\"\n", "空白"},
		{"名字含控制字符", "version: 1\nroles:\n  - name: \"a\\u0007\"\n", "控制字符"},
		{"名字太长", "version: 1\nroles:\n  - name: " + strings.Repeat("长", 33) + "\n", "32"},
		{"重名", "version: 1\nroles:\n  - name: a\n  - name: a\n", "重复"},
		{"备注太长", "version: 1\nroles:\n  - name: a\n    note: " + strings.Repeat("字", 121) + "\n", "120"},
		{"备注换行", "version: 1\nroles:\n  - name: a\n    note: \"x\\ny\"\n", "一行"},
		{"坏 ceiling", "version: 1\nroles:\n  - name: a\n    ceiling: high\n", "high"},
		{"空 forbid 条目", "version: 1\nroles:\n  - name: a\n    forbid:\n      - {}\n", "不能为空"},
		{"坏正则", "version: 1\nroles:\n  - name: a\n    forbid:\n      - {command_regex: \"(\"}\n", "command_regex"},
		{"坏 path_glob", "version: 1\nroles:\n  - name: a\n    forbid:\n      - {path_glob: \"[\"}\n", "path_glob"},
		{"坏 payload", "version: 1\nroles:\n  - name: a\n    forbid:\n      - {payload: email}\n", "payload"},
		{"坏时长", "version: 1\nroles:\n  - name: a\n    trust_max: soon\n", "trust_max"},
		{"时长太短", "version: 1\nroles:\n  - name: a\n    trust_max: 4m\n", "5m"},
		{"时长太长", "version: 1\nroles:\n  - name: a\n    trust_max: 25h\n", "24h"},
		{"时长为负", "version: 1\nroles:\n  - name: a\n    trust_max: -1h\n", "trust_max"},
		{"空 dirs 项", "version: 1\nroles:\n  - name: a\n    dirs: [\"\"]\n", "dirs"},
		{"坏 dirs 模式", "version: 1\nroles:\n  - name: a\n    dirs: [\"~/[x\"]\n", "dirs"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseRoleSet([]byte(c.text))
			if err == nil {
				t.Fatalf("应报错")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("错误 %q 不含 %q", err, c.want)
			}
		})
	}
}

func TestRoleCovers(t *testing.T) {
	home := "/Users/u"
	r := &Role{Name: "a", Dirs: []string{"~/RailseWork", "~/我的程序/**", "/srv/app"}}
	for dir, want := range map[string]bool{
		"/Users/u/RailseWork":        true,
		"/Users/u/RailseWork/":       true,
		"/Users/u/RailseWork/sub":    false, // 同 scope.project：只认等于或通配命中
		"/Users/u/我的程序/x":            true,
		"/Users/u/我的程序/x/y":          true,
		"/srv/app":                   true,
		"/Users/u/other":             false,
		"":                           false,
		"/Users/u/RailseWorkSibling": false,
	} {
		if got := r.Covers(dir, home); got != want {
			t.Errorf("Covers(%q) = %v，想要 %v", dir, got, want)
		}
	}
	// 家目录未知时 ~ 开头的模式不展开，什么都不命中（地盘外，偏保守）
	if r.Covers("/Users/u/RailseWork", "") {
		t.Error("家目录为空时不该命中 ~ 模式")
	}
}

func TestMatchBrief(t *testing.T) {
	read := RiskRead
	for m, want := range map[*Match]string{
		{CommandPrefix: "fly "}:                 `command_prefix "fly "`,
		{Payload: PayloadMCP}:                   `payload mcp`,
		{Payload: PayloadShell, Risk: &read}:    `payload shell, risk read`,
		{MCPServer: "github", MCPTool: "merge"}: `mcp_server "github", mcp_tool "merge"`,
	} {
		if got := m.Brief(); got != want {
			t.Errorf("Brief = %q，想要 %q", got, want)
		}
	}
}

func TestBrokenRole(t *testing.T) {
	r := BrokenRole(errTest("坏了"))
	if r.Ceiling == nil || *r.Ceiling != RiskRead {
		t.Fatalf("坏文件角色的上限应为 read: %+v", r)
	}
	if r.Broken() == "" || r.Name != "" {
		t.Errorf("坏文件角色: %+v", r)
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestPrepareDirs(t *testing.T) {
	rs, err := ParseRoleSet([]byte("version: 1\nroles:\n  - name: a\n    dirs: [\"~/Link/**\", \"/Data/Docs\"]\n"))
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(p string) string {
		if p == "/Users/u/Link" {
			return "/Volumes/real"
		}
		return ""
	}
	rs.PrepareDirs("/Users/u", resolve, true)
	r := &rs.Roles[0]
	for dir, want := range map[string]string{
		"/Volumes/real/x":  "~/Link/**", // 通配前的部分经符号链接解析
		"/Users/u/link/x":  "~/Link/**", // 原样的写法也算，折叠大小写
		"/data/docs":       "/Data/Docs",
		"/Volumes/other/x": "",
	} {
		if got := r.CoveringDir(dir, "ignored"); got != want {
			t.Errorf("CoveringDir(%q) = %q，想要 %q", dir, got, want)
		}
	}
	// 不折叠时大小写敏感
	rs.PrepareDirs("/Users/u", resolve, false)
	if rs.Roles[0].Covers("/data/docs", "") {
		t.Error("不折叠时不该命中")
	}
	// 家目录未知：~ 开头的模式什么都不命中
	rs.PrepareDirs("", resolve, true)
	if rs.Roles[0].Covers("/Users/u/Link/x", "") {
		t.Error("家目录未知时 ~ 模式不该命中")
	}
}
