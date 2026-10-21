package redact

import (
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

// 覆盖简报替换目标里上面用例没测到的写法：单独的 Bearer、带引号的值、--flag=值、带连字符的键名。
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
	}
	for _, c := range cases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}
