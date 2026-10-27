package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

// setupStdin 是向导读答案的地方；stdinIsTerminal 判断 stdin 是不是终端（见 isTerminal）：是才提问，
// 不是（脚本、管道、/dev/null）照旧报用法错误。是变量只为测试能替换。
var (
	setupStdin      io.Reader = os.Stdin
	stdinIsTerminal           = func() bool { return isTerminal(os.Stdin) }
)

// isTerminal 报告 f 像不像终端：是字符设备，且不是 /dev/null。/dev/null 也是字符设备，< /dev/null 时的 stdin 就是它，
// 那里没人能回答。不引 golang.org/x/term，判不出别的非终端字符设备（如 /dev/zero）。
func isTerminal(f *os.File) bool {
	st, err := f.Stat()
	if err != nil || st.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(st, null)
}

// askSetupAll 在终端里逐个问 setup all 接入 hub 还缺的值，填进 hubURL、token、name：已经给了（非空）的不问。
// 名字直接回车取 defaultName（主机名）。提示与重问的原因写到 stdout；整个问答只用一个 bufio.Reader。
// 问了 hub 地址时 hubURL 填的是规范化后的基地址（同 parseHubURL 返回的），并作为 hubBase 返回；没问时 hubBase 为空。
// 任一问题读到 EOF（Ctrl-D）返回 io.EOF，这时什么都还没做。
func askSetupAll(in io.Reader, stdout io.Writer, hubURL, token, name *string, defaultName string) (hubBase string, err error) {
	r := bufio.NewReader(in)
	// ask 打印 prompt 读一行，去掉首尾空白后交给 check：不合格就打印原因再问，直到合格或读不到
	ask := func(prompt string, check func(string) (string, error)) (string, error) {
		for {
			fmt.Fprint(stdout, prompt)
			line, err := r.ReadString('\n')
			if err != nil {
				return "", err
			}
			v, err := check(strings.TrimSpace(line))
			if err == nil {
				return v, nil
			}
			fmt.Fprintln(stdout, err)
		}
	}
	fmt.Fprintln(stdout, "接入 hub 还缺几项，逐个回答（Ctrl-D 取消，什么都不做）：")
	if *hubURL == "" {
		if hubBase, err = ask("hub 地址（如 https://hub.example.com）：", checkHubAnswer); err != nil {
			return "", err
		}
		*hubURL = hubBase
	}
	if *token == "" {
		*token, err = ask("接入码（在 hub 机器上运行 railse hub token 生成）：", func(s string) (string, error) {
			if s == "" {
				return "", errors.New("接入码不能为空")
			}
			return s, nil
		})
		if err != nil {
			return "", err
		}
	}
	if *name == "" {
		*name, err = ask("本机在 hub 里的名字（直接回车用 "+defaultName+"）：", func(s string) (string, error) {
			if s == "" {
				s = defaultName
			}
			if err := checkDisplayName(s); err != nil {
				return "", fmt.Errorf("名字%w", err)
			}
			return s, nil
		})
		if err != nil {
			return "", err
		}
	}
	return hubBase, nil
}

// checkHubAnswer 按 --hub 的口径（parseHubAddr）校验向导里答的 hub 地址，返回去掉末尾 / 的基地址；原因里不提用户没输入过的 --hub。
// 非本机的 http:// 在 --hub 只警告，这里不收：确在可信内网的，用 --hub 给出。
func checkHubAnswer(s string) (string, error) {
	u, base, err := parseHubAddr(s)
	if err != nil {
		return "", fmt.Errorf("hub 地址%w", err)
	}
	if plaintextHub(u) {
		return "", errors.New(plaintextHubNote + "；确在可信内网的，用 railse setup all --hub <地址> 给出")
	}
	return base, nil
}

// printPhoneStep 打印 setup all 最后的“下一步”：手机要打开的 hub 地址用框线围起来（框宽按地址的字符数算，框里只放地址），
// 再说怎样加到主屏幕、开启通知。地址为空时不画框。
func printPhoneStep(w io.Writer, hubURL string) {
	if hubURL == "" {
		fmt.Fprintln(w, "\n下一步：在手机上打开 hub 的地址")
	} else {
		bar := strings.Repeat("═", utf8.RuneCountInString(hubURL)+4)
		fmt.Fprintf(w, "\n下一步：在手机上打开\n╔%s╗\n║  %s  ║\n╚%s╝\n", bar, hubURL, bar)
	}
	fmt.Fprintln(w, "手机 Safari 打开后分享→添加到主屏幕，再从图标打开；登录后在设置里开启通知（见 docs/web.md）。")
}
