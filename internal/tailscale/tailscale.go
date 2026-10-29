// Package tailscale 经 tailscale 命令行查状态、看 serve 配置、开 Funnel。只在这里调它，不引 tailscale 的 Go 包。
package tailscale

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Runner 跑一次 tailscale 命令，返回合并的 stdout+stderr。默认实现见 Exec；测试换成假的。
type Runner func(ctx context.Context, path string, args ...string) (string, error)

// Timeout 是每次调用 tailscale 的时限。是变量只为测试能调短。
var Timeout = 15 * time.Second

// Exec 是默认的 Runner：stdin 为空（/dev/null），超过 Timeout 就杀掉并报错。
func Exec(ctx context.Context, path string, args ...string) (string, error) {
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = nil // /dev/null：tailscale 等输入时直接读到 EOF
	// 杀掉之后最多再等这么久：子进程还占着输出管道时也不一直等下去
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		if perr := parent.Err(); perr != nil { // 调用方自己的 ctx 到期或取消，不算我们的超时
			return string(out), fmt.Errorf("%s %s 被中止: %w", path, strings.Join(args, " "), perr)
		}
		if ctx.Err() == context.DeadlineExceeded {
			return string(out), fmt.Errorf("%s %s 超过 %s 没有结束，已终止: %w", path, strings.Join(args, " "), Timeout, context.DeadlineExceeded)
		}
	}
	return string(out), err
}

// ErrNotFound 表示没找到 tailscale 可执行文件。
var ErrNotFound = errors.New("没有找到 tailscale：请先安装并登录（https://tailscale.com/download）")

// Find 找 tailscale 可执行文件：先 lookPath("tailscale")，再按 goos 看固定位置
// （darwin：/usr/local/bin、/opt/homebrew/bin、Tailscale.app 内；linux：/usr/bin、/usr/local/bin）。找不到返回 ErrNotFound。
func Find(lookPath func(string) (string, error), stat func(string) (os.FileInfo, error), goos string) (string, error) {
	if p, err := lookPath("tailscale"); err == nil {
		return p, nil
	}
	var fixed []string
	switch goos {
	case "darwin":
		fixed = []string{"/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale"}
	case "linux":
		fixed = []string{"/usr/bin/tailscale", "/usr/local/bin/tailscale"}
	}
	for _, p := range fixed {
		if _, err := stat(p); err == nil {
			return p, nil
		}
	}
	return "", ErrNotFound
}

// CLI 是一个 tailscale 可执行文件加上跑它的方式。
type CLI struct {
	Path string
	Run  Runner
}

func (c CLI) run(ctx context.Context, args ...string) (string, error) {
	r := c.Run
	if r == nil {
		r = Exec
	}
	return r(ctx, c.Path, args...)
}

// jsonBody 取第一个 { 到最后一个 } 之间的文本：stdout 与 stderr 合并后，JSON 前面可能混着版本不一致之类的警告行。
func jsonBody(out string) (string, bool) {
	i, j := strings.Index(out, "{"), strings.LastIndex(out, "}")
	if i < 0 || j < i {
		if i >= 0 { // 有 { 却没有收尾，当坏 JSON 交给 Unmarshal 报错
			return out[i:], true
		}
		return "", false
	}
	return out[i : j+1], true
}

// Status 是 `tailscale status --self --json` 里用到的几项。DNSName 已去掉末尾的点。
type Status struct {
	BackendState string
	DNSName      string
	Online       bool
	CertDomains  []string
}

// Status 读本机 tailscale 状态。只解析不判定，BackendState 是否 Running 由调用方看。
func (c CLI) Status(ctx context.Context) (Status, error) {
	out, err := c.run(ctx, "status", "--self", "--json")
	if err != nil {
		return Status{}, fmt.Errorf("tailscale status: %w（%s）", err, strings.TrimSpace(out))
	}
	body, ok := jsonBody(out)
	if !ok {
		return Status{}, fmt.Errorf("tailscale status 的输出不是 JSON: %s", strings.TrimSpace(out))
	}
	var raw struct {
		BackendState string
		Self         struct {
			DNSName string
			Online  bool
		}
		CertDomains []string
	}
	if err := json.Unmarshal([]byte(body), &raw); err != nil {
		return Status{}, fmt.Errorf("tailscale status 的输出不是 JSON: %w", err)
	}
	return Status{BackendState: raw.BackendState, DNSName: strings.TrimSuffix(raw.Self.DNSName, "."), Online: raw.Self.Online, CertDomains: raw.CertDomains}, nil
}

// ServeConfig 是 `tailscale serve status --json` 的结果（只解析用到的字段）。输出不是 JSON 或为空时是零值。
type ServeConfig struct {
	Web map[string]struct {
		Handlers map[string]struct{ Proxy string }
	}
	AllowFunnel map[string]bool
}

// ServeConfig 读 serve 配置。
func (c CLI) ServeConfig(ctx context.Context) (ServeConfig, error) {
	out, err := c.run(ctx, "serve", "status", "--json")
	if err != nil {
		return ServeConfig{}, fmt.Errorf("tailscale serve status: %w（%s）", err, strings.TrimSpace(out))
	}
	return ParseServeConfig(out)
}

// ParseServeConfig 解析 `tailscale serve status --json` 的合并输出（JSON 前面可能混着警告行）。
// 没有 JSON（"No serve config"、空输出）时是零值；有 { 但 JSON 坏了报错。doctor 拿自己跑出的输出直接调它。
func ParseServeConfig(out string) (ServeConfig, error) {
	body, ok := jsonBody(out)
	if !ok { // "No serve config"、空输出
		return ServeConfig{}, nil
	}
	var sc ServeConfig
	if err := json.Unmarshal([]byte(body), &sc); err != nil {
		return ServeConfig{}, fmt.Errorf("tailscale serve status 的输出不是 JSON: %w", err)
	}
	return sc, nil
}

// FunnelFor 报告 dns 的 443 是否已经在 Funnel 上把 / 代理到 http://127.0.0.1:<port>。
func (sc ServeConfig) FunnelFor(dns string, port int) bool {
	key := dns + ":443"
	w, ok := sc.Web[key]
	if !ok || !sc.AllowFunnel[key] {
		return false
	}
	return w.Handlers["/"].Proxy == "http://127.0.0.1:"+strconv.Itoa(port)
}

// ErrFunnelNotEnabled：tailnet 还没开通 Funnel；EnableURL 是 tailscale 输出里的开通链接。
type ErrFunnelNotEnabled struct{ EnableURL string }

// Error 实现 error。
func (e ErrFunnelNotEnabled) Error() string {
	return "这个 tailnet 还没开通 Funnel：在浏览器里打开 " + e.EnableURL + " 开通后重跑"
}

var enableURLRe = regexp.MustCompile(`https://login\.tailscale\.com/\S+`)

// EnableFunnel 跑 `tailscale funnel --bg <port>`；Funnel 未开通时返回 ErrFunnelNotEnabled。
func (c CLI) EnableFunnel(ctx context.Context, port int) error {
	out, err := c.run(ctx, "funnel", "--bg", strconv.Itoa(port))
	if err == nil {
		return nil
	}
	if u := enableURLRe.FindString(out); u != "" {
		return ErrFunnelNotEnabled{EnableURL: strings.TrimRight(u, ".,;:)]}>\"'。，；）")}
	}
	return fmt.Errorf("tailscale funnel --bg %d: %w（%s）", port, err, strings.TrimSpace(out))
}
