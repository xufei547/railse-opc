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

func parseVersion(s string) ([3]int, bool) {
	m := versionRe.FindString(s)
	if m == "" {
		return [3]int{}, false
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

func versionLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// cliChecks 看三个 CLI 装没装、版本多少：只跑 `<cli> --version`（RunCmd 默认带 10 秒超时），不碰它们的账号与配置。
func cliChecks(o Options) []Check {
	var out []Check
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
		text, err := o.RunCmd(path, "--version")
		v, ok := parseVersion(text)
		if err != nil {
			out = append(out, Check{ID: cli.id, Name: cli.name, Status: Warn, Detail: fmt.Sprintf("%s --version 失败：%v", path, err), Hint: "在终端里手动跑一次 " + cli.cmd + " --version 确认它能用"})
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
