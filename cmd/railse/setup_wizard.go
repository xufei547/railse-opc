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

// setupStdin 是向导与重跑确认读答案的地方；stdinIsTerminal 判断 stdin 是不是终端（见 isTerminal）：是才提问，
// 不是（脚本、管道、/dev/null）时缺接入信息照旧报用法错误、已接入的重跑不确认直接做。是变量只为测试能替换。
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
// 任一问题读到 EOF（Ctrl-D）返回 io.EOF，这时什么都还没做；读失败时先往 stdout 换一行。
func askSetupAll(in io.Reader, stdout io.Writer, hubURL, token, name *string, defaultName string) (hubBase string, err error) {
	r := bufio.NewReader(in)
	// ask 打印 prompt 读一行，去掉首尾空白后交给 check：不合格就打印原因再问，直到合格或读不到
	ask := func(prompt string, check func(string) (string, error)) (string, error) {
		for {
			fmt.Fprint(stdout, prompt)
			line, err := r.ReadString('\n')
			if err != nil {
				fmt.Fprintln(stdout) // 终端不会替 Ctrl-D 换行：调用方接着说的“已取消”不跟在提示后面
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

// rerunWork 说已接入的机器重跑 setup all 接下来要重做的事：local 为真是本机模式，第 1 步要复查本机 hub（Funnel、托管，必要时重新托管）；
// hooks 为真要重装钩子，连同 Codex 的 notify（可能改写 ~/.codex/config.toml），问句里一并说出；daemon 是“开机自启”那一步对守护进程
// 要做的事（“重启守护进程”“托管并启动守护进程”，空表示不碰它）。
// what 放进确认的问句，untouched 是取消时说的哪些没动；都没有要做的时两个都是空。
func rerunWork(local, hooks bool, daemon string) (what, untouched string) {
	var did []string
	if local {
		did = append(did, "检查本机 hub（必要时重新托管）")
	}
	if hooks {
		did = append(did, "重装钩子（连同 Codex 的 notify）")
	}
	if daemon != "" {
		did = append(did, daemon)
	}
	switch {
	case local && hooks && daemon != "":
		untouched = "本机 hub、钩子与守护进程都没动"
	case local && hooks:
		untouched = "本机 hub 与钩子都没动"
	case local && daemon != "":
		untouched = "本机 hub 与守护进程都没动"
	case local:
		untouched = "本机 hub 没动"
	case hooks && daemon != "":
		untouched = "钩子与守护进程都没动"
	case hooks:
		untouched = "钩子没动"
	case daemon != "":
		untouched = "守护进程没动"
	}
	return strings.Join(did, "、"), untouched
}

// confirmRerun 在终端里问已接入 hubURL 的机器要不要接着做 what（见 rerunWork）。直接回车或 y、yes 返回 true，n、no 返回 false，
// 大小写不论；别的答案说明怎么答再问。读到 EOF（Ctrl-D）返回 io.EOF，调用方当取消；读失败时先往 stdout 换一行。
// 提示与重问的说明写到 stdout。
func confirmRerun(in io.Reader, stdout io.Writer, hubURL, what string) (bool, error) {
	r := bufio.NewReader(in)
	for {
		fmt.Fprintf(stdout, "本机已接入 %s。接下来%s，继续吗？[Y/n] ", hubURL, what)
		line, err := r.ReadString('\n')
		if err != nil {
			fmt.Fprintln(stdout) // 同 askSetupAll：“已取消”不跟在 [Y/n] 后面
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		fmt.Fprintln(stdout, "请回答 y 或 n（直接回车算 y）")
	}
}

// askHubWhere 是向导的第一问：hub 在本机（照 setup all --local-hub 做）还是已有 hub（接着问地址、接入码）。tailscaleFound 决定默认项：
// 找到了 tailscale 默认 1（本机），否则默认 2。直接回车取默认；答 1、2 以外的说明怎么答再问。读到 EOF（Ctrl-D）返回 io.EOF，
// 这时什么都还没做；读失败时先往 stdout 换一行（同 askSetupAll）。提示与重问的说明写到 stdout。
// 逐字节读（readLineRaw），只读走答案这一行：后面问 hub 地址、接入码或网页密码的都还要从同一个 stdin 读。
func askHubWhere(in io.Reader, stdout io.Writer, tailscaleFound bool) (local bool, err error) {
	def := "2"
	if tailscaleFound {
		def = "1"
	}
	fmt.Fprint(stdout, "hub 在哪里？\n"+
		"  1) 本机：用 Tailscale 给手机一个地址，不用服务器（要先装好并登录 Tailscale）\n"+
		"  2) 已有 hub：填它的地址和接入码\n")
	for {
		fmt.Fprintf(stdout, "选 [%s]：", def)
		line, err := readLineRaw(in)
		if err != nil {
			fmt.Fprintln(stdout) // 终端不会替 Ctrl-D 换行：调用方接着说的“已取消”不跟在提示后面
			return false, err
		}
		a := strings.TrimSpace(line)
		if a == "" {
			a = def
		}
		switch a {
		case "1":
			return true, nil
		case "2":
			return false, nil
		}
		fmt.Fprintf(stdout, "请答 1 或 2（直接回车选 %s）\n", def)
	}
}

// readLineRaw 从 r 逐字节读一行，返回的行不含末尾的 \n。不用 bufio.Reader：它会预读后面的行，之后再从 r 读的（askSetupAll、
// readPassword、confirmRerun）就读不到了。读到 EOF 或出错时返回已读的部分与那个错误（同 bufio.Reader.ReadString 的口径）。
func readLineRaw(r io.Reader) (string, error) {
	var line []byte
	var b [1]byte
	for {
		n, err := r.Read(b[:])
		if n == 1 {
			if b[0] == '\n' {
				return string(line), nil
			}
			line = append(line, b[0])
		}
		if err != nil {
			return string(line), err
		}
	}
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
// 再说怎样加到主屏幕、开启通知。地址为空时不画框。local 为真（本机 hub，地址由 Tailscale Funnel 给）时框下面多一行：
// 地址由 Tailscale 提供，手机挂着代理打不开时关掉代理再试（代理常把 *.ts.net 解析或转发坏）。
func printPhoneStep(w io.Writer, hubURL string, local bool) {
	if hubURL == "" {
		fmt.Fprintln(w, "\n下一步：在手机上打开 hub 的地址")
	} else {
		bar := strings.Repeat("═", utf8.RuneCountInString(hubURL)+4)
		fmt.Fprintf(w, "\n下一步：在手机上打开\n╔%s╗\n║  %s  ║\n╚%s╝\n", bar, hubURL, bar)
	}
	if local {
		fmt.Fprintln(w, "这个地址由 Tailscale 提供；手机挂着代理打不开时关掉代理再试。")
	}
	fmt.Fprintln(w, "手机 Safari 打开后分享→添加到主屏幕，再从图标打开；登录后在设置里开启通知（见 docs/web.md）。")
}
