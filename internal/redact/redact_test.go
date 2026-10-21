package redact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/railse/railse-opc/internal/policy"
)

func TestString(t *testing.T) {
	cases := []struct{ in, want string }{
		{`curl -H "Authorization: Bearer abc.def.ghi" https://x.y`, `curl -H "Authorization: <redacted>" https://x.y`},
		{`curl -H 'Authorization: Basic dXNlcjpwYXNz' https://x.y`, `curl -H 'Authorization: <redacted>' https://x.y`},
		{`export TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123`, `export TOKEN=<redacted>`},
		{`API_KEY=sk-abcdefghijklmnopqrstuvwxyz npm test`, `API_KEY=<redacted> npm test`},
		{`npm login --password hunter2 --username bob`, `npm login --password <redacted> --username bob`},
		{`echo password=secret123 > .env`, `echo password=<redacted> > .env`},
		{`git push origin main`, `git push origin main`},
		{`ls -la`, `ls -la`},
		{`curl https://x.y/?api_key=abcdefghijklmnopqrstu`, `curl https://x.y/?api_key=<redacted>`},
		{`xoxb-1234567890-abcdefghijklmnop`, `<redacted>`},
		{`AKIAIOSFODNN7EXAMPLE1`, `<redacted>`},
	}
	for _, c := range cases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}

func TestPayload(t *testing.T) {
	p := policy.Payload{Type: policy.PayloadShell, Command: "curl -H 'Authorization: Bearer zzz' https://x.y", DiffPreview: "password=abc", URL: "https://x.y/?token=abcdefghijklmnopqrstuv", Args: []byte(`{"token":"ghp_abcdefghijklmnopqrstuvwxyz0123"}`)}
	out := Payload(p)
	if strings.Contains(out.Command, "zzz") || strings.Contains(out.DiffPreview, "abc") || strings.Contains(out.URL, "abcdefghijklmnopqrstuv") || strings.Contains(string(out.Args), "ghp_") {
		t.Fatalf("未脱敏: %+v", out)
	}
	if p.Command == out.Command {
		t.Fatal("应返回新值而不是原值")
	}
}

// 覆盖简报替换目标里上面用例没测到的写法：单独的 Bearer、带引号的值、--flag=值、带连字符的键名，
// 以及键名带数字或 . : / 前缀、不该误抹的普通参数、值后面紧跟的 shell 元字符。
func TestStringMoreForms(t *testing.T) {
	cases := []struct{ in, want string }{
		{`curl -H "X-Auth: Bearer abcdef123" https://x.y`, `curl -H "X-Auth: Bearer <redacted>" https://x.y`},
		{`npm login --password "hunter 2" --username bob`, `npm login --password <redacted> --username bob`},
		{`gh auth login --token 'abc123'`, `gh auth login --token <redacted>`},
		{`mysql --password=hunter2 -u root`, `mysql --password=<redacted> -u root`},
		{`export PASSWORD="hunter 2"`, `export PASSWORD=<redacted>`},
		// 命令行语境不在 & 处停，后面的查询参数一并抹掉；URL 字段走 url 语境，见 TestPayloadFieldModes
		{`curl https://x.y/?a=1&access-token=abc&b=2`, `curl https://x.y/?a=1&access-token=<redacted>`},
		{`passwd=abc secret=def apikey=ghi`, `passwd=<redacted> secret=<redacted> apikey=<redacted>`},
		{`sk-abcdefghijklmnopqrstuvwxyz`, `<redacted>`},
		{`git commit -m "fix token refresh"`, `git commit -m "fix token refresh"`},
		// 键名带数字，或前面是 . : / 等
		{`OAUTH2_CLIENT_SECRET=abc`, `OAUTH2_CLIENT_SECRET=<redacted>`},
		{`S3_SECRET_KEY=abc`, `S3_SECRET_KEY=<redacted>`},
		{`DB2_PASSWORD=abc`, `DB2_PASSWORD=<redacted>`},
		{`TOKEN=abc TOKEN2=def`, `TOKEN=<redacted> TOKEN2=<redacted>`},
		{`spring.datasource.password=hunter2`, `spring.datasource.password=<redacted>`},
		{`//registry.npmjs.org/:_authToken=abc`, `//registry.npmjs.org/:_authToken=<redacted>`},
		// 键名前面紧贴 = # 等任意字符
		{`kubectl create secret generic db --from-literal=password=hunter2`, `kubectl create secret generic db --from-literal=password=<redacted>`},
		{`docker run --env=DB_PASSWORD=hunter2`, `docker run --env=DB_PASSWORD=<redacted>`},
		{`#DB_PASSWORD=hunter2`, `#DB_PASSWORD=<redacted>`},
		// 不是凭据的普通参数原样保留
		{`git log --author=bob`, `git log --author=bob`},
		{`git commit --author="Bob <b@x>"`, `git commit --author="Bob <b@x>"`},
		{`llm --max-tokens=4096`, `llm --max-tokens=4096`},
		// 命令行里的值不按 shell 元字符截断：宁可连带抹掉紧贴的命令，也不在引号或 heredoc 里漏掉半截密码
		{`password=abc;rm -rf /`, `password=<redacted> -rf /`},
		{`export GITHUB_TOKEN=$(gh auth token)`, `export GITHUB_TOKEN=<redacted> auth token)`},
		{`docker run -e "POSTGRES_PASSWORD=Xy7&kP(2)q" postgres`, `docker run -e "POSTGRES_PASSWORD=<redacted>" postgres`},
		{`echo "DB_PASSWORD=Xy7&kP(2)q" >> .env`, `echo "DB_PASSWORD=<redacted>" >> .env`},
		{"cat > .env <<'EOF'\nDB_PASSWORD=Xy7&kP(2)q\nSECRET_KEY=ab;cd|ef\nEOF", "cat > .env <<'EOF'\nDB_PASSWORD=<redacted>\nSECRET_KEY=<redacted>\nEOF"},
	}
	for _, c := range cases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}

// JSON 原文里转义过的 \"...\" 要整段抹掉，且不能吞掉反斜杠，结果仍是合法 JSON。
func TestStringEscapedQuotes(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"command":"npm login --password \"hunter2\""}`, `{"command":"npm login --password <redacted>"}`},
		{`{"command":"export PASSWORD=\"hunter2\""}`, `{"command":"export PASSWORD=<redacted>"}`},
		{`{"command":"curl -H \"Authorization: Bearer abc123\" https://x"}`, `{"command":"curl -H \"Authorization: <redacted>\" https://x"}`},
		// 只抹到第一个 \"，不把后面的参数一起吞掉
		{`{"command":"tool --password \"a\" --user \"b\""}`, `{"command":"tool --password <redacted> --user \"b\""}`},
	}
	for _, c := range cases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if !json.Valid([]byte(got)) {
			t.Errorf("String(%q) 结果不是合法 JSON: %q", c.in, got)
		}
	}
}

// 合法 JSON 的 Args 按结构脱敏：敏感键的值整体抹掉，其余字符串逐个脱敏，结果仍是合法 JSON。
func TestPayloadArgs(t *testing.T) {
	cases := []struct{ in, want string }{
		{`{"command":"npm login --password \"hunter2\""}`, `{"command":"npm login --password <redacted>"}`},
		{`{"command":"export PASSWORD=\"hunter2\""}`, `{"command":"export PASSWORD=<redacted>"}`},
		{`{"command":"curl -H \"Authorization: Bearer abc123\" https://x"}`, `{"command":"curl -H \"Authorization: <redacted>\" https://x"}`},
		{`{"password":"hunter2","title":"t"}`, `{"password":"<redacted>","title":"t"}`},
		{`{"headers":{"Authorization":"Basic dXNlcjpwYXNz"},"env":["GH_TOKEN=abc","X=1"]}`, `{"env":["GH_TOKEN=<redacted>","X=1"],"headers":{"Authorization":"<redacted>"}}`},
		// 字符串叶子是文件内容等普通文本：键名前可带 + #，值不按 shell 元字符截断
		{`{"content":"+DB_PASSWORD=hunter2\n#TOKEN=abc"}`, `{"content":"+DB_PASSWORD=<redacted>\n#TOKEN=<redacted>"}`},
		{`{"content":"SECRET_KEY=a(b)&c|d"}`, `{"content":"SECRET_KEY=<redacted>"}`},
		// 不是敏感键的原样保留，大整数不丢精度
		{`{"author":"bob","id":12345678901234567890,"max_tokens":4096}`, `{"author":"bob","id":12345678901234567890,"max_tokens":4096}`},
	}
	for _, c := range cases {
		out := Payload(policy.Payload{Type: policy.PayloadMCP, Server: "s", Tool: "t", Args: json.RawMessage(c.in)})
		if string(out.Args) != c.want {
			t.Errorf("Args %s\n 得到 %s\n 想要 %s", c.in, out.Args, c.want)
		}
		if !json.Valid(out.Args) {
			t.Errorf("Args %s 脱敏后不是合法 JSON: %s", c.in, out.Args)
		}
		if _, err := json.Marshal(out); err != nil {
			t.Errorf("Args %s 脱敏后整个载荷无法序列化: %v", c.in, err)
		}
	}

	// 不是合法 JSON 时退回按字符串处理
	bad := `{"command":"export PASSWORD=\"hunter2\"`
	out := Payload(policy.Payload{Type: policy.PayloadMCP, Args: json.RawMessage(bad)})
	if strings.Contains(string(out.Args), "hunter2") {
		t.Errorf("非法 JSON 的 Args 未脱敏: %s", out.Args)
	}
}

// Payload 按字段语境脱敏：Command 的值抹到空白、引号或反斜杠，Path 与 DiffPreview 的值抹到空白或引号，
// URL 的值在 & 处停。
func TestPayloadFieldModes(t *testing.T) {
	out := Payload(policy.Payload{
		Command: `password=abc;rm -rf /`,
		Path:    `/srv/app/password=a;b`,
		URL:     `https://x.y/?token=abc&next=/home`,
	})
	if want := `password=<redacted> -rf /`; out.Command != want {
		t.Errorf("Command 得到 %q 想要 %q", out.Command, want)
	}
	if want := `/srv/app/password=<redacted>`; out.Path != want {
		t.Errorf("Path 得到 %q 想要 %q", out.Path, want)
	}
	if want := `https://x.y/?token=<redacted>&next=/home`; out.URL != want {
		t.Errorf("URL 得到 %q 想要 %q", out.URL, want)
	}

	diffs := []struct{ in, want string }{
		// Codex apply_patch 的补丁原文，新增行以 + 开头
		{"*** Add File: .env\n+DB_PASSWORD=hunter2\n+API_KEY=abc123", "*** Add File: .env\n+DB_PASSWORD=<redacted>\n+API_KEY=<redacted>"},
		{`DB_PASSWORD=Xy7&kP(2)q`, `DB_PASSWORD=<redacted>`},
		{`API_TOKEN=ab;cd|ef<gh>ij\kl`, `API_TOKEN=<redacted>`},
	}
	for _, c := range diffs {
		got := Payload(policy.Payload{Type: policy.PayloadFileWrite, DiffPreview: c.in}).DiffPreview
		if got != c.want {
			t.Errorf("DiffPreview %q\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}
