package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xufei547/railse-opc/internal/i18n"
)

const langUsage = `用法: railse lang [zh|en]
不带参数打印当前语言和它从哪来；带参数写进数据目录的 lang 文件（$RAILSE_HOME 或 ~/.railse）。
解析顺序：环境变量 RAILSE_LANG → lang 文件 → 系统 LANG（zh 开头算中文，其余英文）。`

// runLang 查看或设定界面语言。写文件后对当前进程也立刻生效。
func runLang(args []string, stdout, stderr io.Writer) int {
	switch len(args) {
	case 0:
		lang, source := i18n.LangSource()
		fmt.Fprintf(stdout, i18n.T("当前语言 %s（来源：%s）\n"), lang, langSourceName(source))
		return 0
	case 1:
		switch args[0] {
		case "zh", "en":
		case "-h", "--help", "help":
			fmt.Fprintln(stdout, i18n.T(langUsage))
			return 0
		default:
			fmt.Fprintf(stderr, i18n.T("语言只能是 zh 或 en，不是 %q\n\n%s\n"), args[0], i18n.T(langUsage))
			return 2
		}
		dir := i18n.DefaultDir()
		if dir == "" {
			fmt.Fprintln(stderr, i18n.T("找不到家目录，没法写 lang 文件"))
			return 1
		}
		if err := i18n.WriteFile(dir, args[0]); err != nil {
			fmt.Fprintf(stderr, i18n.T("写不了 lang 文件：%v\n"), err)
			return 1
		}
		i18n.Set(args[0])
		fmt.Fprintf(stdout, i18n.T("已写入 %s\n"), filepath.Join(dir, i18n.FileName))
		if env := strings.TrimSpace(os.Getenv(i18n.EnvVar)); env != "" && normalizeLang(env) != args[0] {
			fmt.Fprintf(stdout, i18n.T("注意：环境变量 RAILSE_LANG=%s 优先于 lang 文件；要让 lang 文件生效，请去掉它。\n"), env)
		}
		return 0
	default:
		fmt.Fprintf(stderr, i18n.T("多余的参数 %q\n%s\n"), args[1], i18n.T(langUsage))
		return 2
	}
}

func langSourceName(source string) string {
	switch source {
	case "env":
		return i18n.T("环境变量 RAILSE_LANG")
	case "file":
		return i18n.T("lang 文件")
	case "locale":
		return i18n.T("系统 LANG")
	case "set":
		return i18n.T("命令指定")
	default:
		return i18n.T("默认")
	}
}

// normalizeLang 与 i18n 的规则一致：zh 开头算中文，其余英文。
func normalizeLang(s string) string {
	if strings.HasPrefix(strings.ToLower(s), "zh") {
		return i18n.ZH
	}
	return i18n.EN
}
