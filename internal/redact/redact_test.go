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
		{`curl https://x.y/?a=1&access-token=abc&b=2`, `curl https://x.y/?a=1&access-token=<redacted>&b=2`},
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
		// 不是凭据的普通参数原样保留
		{`git log --author=bob`, `git log --author=bob`},
		{`git commit --author="Bob <b@x>"`, `git commit --author="Bob <b@x>"`},
		{`llm --max-tokens=4096`, `llm --max-tokens=4096`},
		// 值被抹，后面链接的命令保留
		{`password=abc;rm -rf /`, `password=<redacted>;rm -rf /`},
		{`export GITHUB_TOKEN=$(gh auth token)`, `export GITHUB_TOKEN=<redacted>(gh auth token)`},
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
