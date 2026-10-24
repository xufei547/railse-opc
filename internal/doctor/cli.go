package doctor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// 录制钩子样本时的版本（internal/adapter/testdata）与 Gemini 的最低可用版本（实验：0.65.0 之前 BeforeTool 在确认之后才触发）。
const (
	sampleClaude = "2.1.280"
	sampleCodex  = "0.145.0"
	minGemini    = "0.65.0"
)

var versionRe = regexp.MustCompile(`\d+\.\d+\.\d+`)

// versionLineRe 认“整行就是版本号”的行：前面可有 CLI 自己的名字（claude、codex、codex-cli、gemini）、version 一词与 v，
// 后面可有预发布后缀与括注，如“2.1.280 (Claude Code)”“codex-cli 0.150.0”“gemini version 0.60.0”“v1.2.3”“0.66.0-nightly.1”。
// 前缀词只认这几个：别的程序的版本行（如 stderr 里混进来的“Node.js v18.19.0”）不算。
var versionLineRe = regexp.MustCompile(`^\s*(?i:(?:claude|codex-cli|codex|gemini)\s+)?(?i:version\s+)?v?(\d+\.\d+\.\d+)[^\s()]*\s*(?:\([^()]*\))?\s*$`)

// parseVersion 从 `--version` 的合并输出里取版本号。stderr 里的警告也会混进来（如 node 的
// “requires node v18.19.0”），所以优先取整行就是版本号的行（有多行时取最后一行），没有这样的行才取最后一个 x.y.z。
func parseVersion(s string) ([3]int, bool) {
	m := ""
	for _, line := range strings.Split(s, "\n") {
		if sub := versionLineRe.FindStringSubmatch(line); sub != nil {
			m = sub[1]
		}
	}
	if m == "" {
		all := versionRe.FindAllString(s, -1)
		if len(all) == 0 {
			return [3]int{}, false
		}
		m = all[len(all)-1]
	}
	var v [3]int
	for i, p := range strings.SplitN(m, ".", 3) {
		n, err := strconv.Atoi(p)
		if err != nil { // 位数多到溢出 int
			return [3]int{}, false
		}
		v[i] = n
	}
	return v, true
}

// firstLine 取输出的第一个非空行，超过 max 个字符时截断并加“…”。
func firstLine(s string, max int) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			if r := []rune(line); len(r) > max {
				return string(r[:max]) + "…"
			}
			return line
		}
	}
	return ""
}

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// underSudo 报告是不是以 root 或别的用户身份在跑（sudo 下 HOME 常常还是原用户的）：
// euid 是 root，或家目录的属主不是当前 euid。查不到家目录属主时只看 euid。
func underSudo(o Options) bool {
	euid := o.Geteuid()
	if euid == 0 {
		return true
	}
	if o.Home == "" {
		return false
	}
	uid, ok := o.FileOwner(o.Home)
	return ok && uid != euid
}

// cliChecks 看三个 CLI 装没装、版本多少：只跑 `<cli> --version`（RunCmd 默认带 10 秒超时），不碰它们的账号与配置。
// --version 可能让 CLI 在用户目录写缓存，所以 sudo 下不跑，只报装没装。
func cliChecks(o Options) []Check {
	var out []Check
	sudo := underSudo(o)
	for _, cli := range []struct{ id, name, cmd, sample string }{
		{"cli.claude", "Claude Code 版本", "claude", sampleClaude},
		{"cli.codex", "Codex CLI 版本", "codex", sampleCodex},
		{"cli.gemini", "Gemini CLI 版本", "gemini", ""},
	} {
		path, err := o.LookPath(cli.cmd)
		if err != nil {
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: Skip, Detail: "PATH 里没有 " + cli.cmd})
			continue
		}
		if sudo {
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: Skip, Detail: "sudo 下不跑 CLI，免得在用户目录留下 root 属主的文件", Hint: "用平时的用户身份重跑 railse doctor"})
			continue
		}
		text, err := o.RunCmd(path, "--version")
		v, ok := parseVersion(text)
		if err != nil {
			detail := fmt.Sprintf("%s --version 失败：%v", path, err)
			if first := firstLine(text, 200); first != "" {
				detail += "；输出：" + first
			}
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: Warn, Detail: detail, Hint: "在终端里手动跑一次 " + cli.cmd + " --version 确认它能用"})
			continue
		}
		if !ok {
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: Warn, Detail: path + " --version 没有给出版本号", Hint: "在终端里手动跑一次 " + cli.cmd + " --version 确认版本"})
			continue
		}
		vs := fmt.Sprintf("%d.%d.%d", v[0], v[1], v[2])
		if cli.cmd == "gemini" {
			minV, _ := parseVersion(minGemini)
			if versionLess(v, minV) {
				out = append(out, Check{ID: cli.id, Name: cli.name, Status: Fail, Detail: fmt.Sprintf("当前 %s，低于 %s：钩子在 %s 之前无法在审批前拦截（确认之后才触发）", vs, minGemini, minGemini), Hint: "升级 Gemini CLI 到 " + minGemini + " 或更新"})
			} else {
				out = append(out, Check{ID: cli.id, Name: cli.name, Status: OK, Detail: vs})
			}
			continue
		}
		if vs != cli.sample {
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: Warn, Detail: fmt.Sprintf("钩子格式按 %s 录制，当前 %s 未验证", cli.sample, vs), Hint: "试一次需要审批的操作，确认请求经过 Railse；不行就换回 " + cli.sample + " 并反馈"})
		} else {
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: OK, Detail: vs})
		}
	}
	return out
}
