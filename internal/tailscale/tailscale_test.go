package tailscale

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeCLI 写一个假 tailscale：`status --self --json` 打印 status.json，`serve status --json` 打印 serve.json，
// `funnel --bg N` 打印 funnel.out，目录里有 funnel.sleep 时接着睡那么多秒（模拟真 tailscale 打出开通链接后等人去开通），
// 否则按 funnelCode 退出；别的参数退出 9。参数记进 args.log。
func fakeCLI(t *testing.T, status, serve, funnelOut string, funnelCode int) (CLI, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("假脚本要 sh")
	}
	dir := t.TempDir()
	write := func(name, s string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("status.json", status)
	write("serve.json", serve)
	write("funnel.out", funnelOut)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "` + filepath.Join(dir, "args.log") + `"
case "$*" in
  "status --self --json") cat "` + filepath.Join(dir, "status.json") + `" ;;
  "serve status --json") cat "` + filepath.Join(dir, "serve.json") + `" ;;
  "funnel --bg "*) cat "` + filepath.Join(dir, "funnel.out") + `"; [ -f "` + filepath.Join(dir, "funnel.sleep") + `" ] && exec sleep "$(cat "` + filepath.Join(dir, "funnel.sleep") + `")"; exit ` + strconv.Itoa(funnelCode) + ` ;;
  *) echo "unexpected: $*" >&2; exit 9 ;;
esac
`
	p := filepath.Join(dir, "tailscale")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return CLI{Path: p}, dir
}

const statusRunning = `{"BackendState":"Running","Self":{"DNSName":"macbook-air.tail2613ae.ts.net.","Online":true},"CertDomains":["macbook-air.tail2613ae.ts.net"]}`
const serveFunnelOn = `{"TCP":{"443":{"HTTPS":true}},"Web":{"macbook-air.tail2613ae.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:8787"}}}},"AllowFunnel":{"macbook-air.tail2613ae.ts.net:443":true}}`

func TestStatusParsesSelf(t *testing.T) {
	c, _ := fakeCLI(t, statusRunning, "", "", 0)
	st, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.BackendState != "Running" || st.DNSName != "macbook-air.tail2613ae.ts.net" || !st.Online || len(st.CertDomains) != 1 {
		t.Fatalf("status = %+v", st)
	}
}

func TestStatusNeedsLoginOnlyParses(t *testing.T) {
	c, _ := fakeCLI(t, `{"BackendState":"NeedsLogin","Self":{"DNSName":"","Online":false}}`, "", "", 0)
	st, err := c.Status(context.Background())
	if err != nil || st.BackendState != "NeedsLogin" {
		t.Fatalf("Status 只解析不判定: %+v %v", st, err)
	}
}

func TestServeConfigFunnelFor(t *testing.T) {
	c, _ := fakeCLI(t, statusRunning, serveFunnelOn, "", 0)
	sc, err := c.ServeConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !sc.FunnelFor("macbook-air.tail2613ae.ts.net", 8787) {
		t.Fatal("应认出 Funnel 已开到 8787")
	}
	for _, bad := range []struct {
		dns  string
		port int
	}{{"macbook-air.tail2613ae.ts.net", 8788}, {"other.tail2613ae.ts.net", 8787}} {
		if sc.FunnelFor(bad.dns, bad.port) {
			t.Fatalf("%v 不该算开了", bad)
		}
	}
}

func TestServeConfigNoConfigIsEmpty(t *testing.T) {
	c, _ := fakeCLI(t, statusRunning, "No serve config\n", "", 0)
	sc, err := c.ServeConfig(context.Background())
	if err != nil || sc.FunnelFor("macbook-air.tail2613ae.ts.net", 8787) {
		t.Fatalf("非 JSON 输出应是空配置: %+v %v", sc, err)
	}
}

func TestEnableFunnelNotEnabledReturnsURL(t *testing.T) {
	out := "Funnel is not enabled on your tailnet.\nTo enable, visit:\n\thttps://login.tailscale.com/f/funnel?node=abc\n"
	c, _ := fakeCLI(t, statusRunning, "", out, 1)
	err := c.EnableFunnel(context.Background(), 8787)
	var nf ErrFunnelNotEnabled
	if !errors.As(err, &nf) || nf.EnableURL != "https://login.tailscale.com/f/funnel?node=abc" {
		t.Fatalf("err = %v", err)
	}
}

// 真 tailscale 在 tailnet 没开通 Funnel 时先打出说明与开通链接，再要么直接退出 0（非管理员），要么等人在浏览器里开通（管理员；
// 我们 Timeout 到了就杀掉）。两种都要把链接交出来：退出 0 的不能当成开好了。
const notEnabledOut = "Funnel is not enabled on your tailnet.\nTo enable, visit:\n\n         https://login.tailscale.com/f/funnel?node=abc\n"

func TestEnableFunnelLinkThenExitZeroReturnsURL(t *testing.T) {
	c, _ := fakeCLI(t, statusRunning, "", notEnabledOut, 0)
	err := c.EnableFunnel(context.Background(), 8787)
	var nf ErrFunnelNotEnabled
	if !errors.As(err, &nf) || nf.EnableURL != "https://login.tailscale.com/f/funnel?node=abc" {
		t.Fatalf("打出开通链接后退出 0 也应是 ErrFunnelNotEnabled: %v", err)
	}
}

func TestEnableFunnelLinkThenWaitTimesOutReturnsURL(t *testing.T) {
	c, dir := fakeCLI(t, statusRunning, "", notEnabledOut, 0)
	if err := os.WriteFile(filepath.Join(dir, "funnel.sleep"), []byte("30"), 0o600); err != nil {
		t.Fatal(err)
	}
	// 不能太短：新写的脚本第一次起来可能要几百毫秒（macOS 上会先被扫一遍），还没打出链接就被杀掉
	old := Timeout
	Timeout = 2 * time.Second
	t.Cleanup(func() { Timeout = old })
	start := time.Now()
	err := c.EnableFunnel(context.Background(), 8787)
	var nf ErrFunnelNotEnabled
	if !errors.As(err, &nf) || nf.EnableURL != "https://login.tailscale.com/f/funnel?node=abc" {
		t.Fatalf("打出开通链接后一直等、被超时杀掉，也应是 ErrFunnelNotEnabled: %v", err)
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Fatalf("应在 Timeout 后不久返回，用了 %s", d)
	}
}

func TestEnableFunnelSuccessOutputIsNotAnError(t *testing.T) {
	out := "Available on the internet:\n\nhttps://macbook-air.tail2613ae.ts.net/\n|-- proxy http://127.0.0.1:8787\n\nFunnel started and running in the background.\n"
	c, _ := fakeCLI(t, statusRunning, "", out, 0)
	if err := c.EnableFunnel(context.Background(), 8787); err != nil {
		t.Fatalf("开好了不该报错: %v", err)
	}
}

func TestEnableFunnelPassesPortAndOtherErrorsCarryOutput(t *testing.T) {
	c, dir := fakeCLI(t, statusRunning, "", "something broke\n", 2)
	err := c.EnableFunnel(context.Background(), 8787)
	if err == nil || !strings.Contains(err.Error(), "something broke") {
		t.Fatalf("err = %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "args.log"))
	if !strings.Contains(string(b), "funnel --bg 8787") {
		t.Fatalf("应调 funnel --bg 8787: %s", b)
	}
}

func TestFindOrder(t *testing.T) {
	notFound := func(string) (string, error) { return "", errors.New("no") }
	exists := map[string]bool{"/opt/homebrew/bin/tailscale": true}
	stat := func(p string) (os.FileInfo, error) {
		if exists[p] {
			return nil, nil
		}
		return nil, os.ErrNotExist
	}
	if p, err := Find(notFound, stat, "darwin"); err != nil || p != "/opt/homebrew/bin/tailscale" {
		t.Fatalf("%q %v", p, err)
	}
	if _, err := Find(notFound, stat, "linux"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("linux 上没有应是 ErrNotFound: %v", err)
	}
	if p, _ := Find(func(string) (string, error) { return "/usr/bin/tailscale", nil }, stat, "linux"); p != "/usr/bin/tailscale" {
		t.Fatalf("PATH 里的优先: %q", p)
	}
}

func TestExecTimesOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "slow")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := Timeout
	Timeout = 200 * time.Millisecond
	t.Cleanup(func() { Timeout = old })
	if _, err := Exec(context.Background(), p); err == nil || !strings.Contains(err.Error(), "超过") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("应超时（错误链上是 context.DeadlineExceeded）: %v", err)
	}
}

const warnLine = "Warning: client version \"1.80.0\" != tailscaled server version \"1.78.1\"\n"

func TestWarningBeforeJSONStillParses(t *testing.T) {
	c, _ := fakeCLI(t, warnLine+statusRunning, warnLine+serveFunnelOn, "", 0)
	st, err := c.Status(context.Background())
	if err != nil || st.DNSName != "macbook-air.tail2613ae.ts.net" {
		t.Fatalf("status = %+v %v", st, err)
	}
	sc, err := c.ServeConfig(context.Background())
	if err != nil || !sc.FunnelFor("macbook-air.tail2613ae.ts.net", 8787) {
		t.Fatalf("serve = %+v %v", sc, err)
	}
}

// ParseServeConfig 是 CLI.ServeConfig 用的解析（doctor 拿 RunCmd 的输出直接调它）：JSON 前面的警告行跳过，
// 没有 JSON（No serve config、空输出）是零值，有 { 但坏了报错。
func TestParseServeConfig(t *testing.T) {
	for _, out := range []string{serveFunnelOn, warnLine + serveFunnelOn} {
		sc, err := ParseServeConfig(out)
		if err != nil || !sc.FunnelFor("macbook-air.tail2613ae.ts.net", 8787) {
			t.Fatalf("%q: %+v %v", out, sc, err)
		}
	}
	for _, out := range []string{"", "No serve config\n", warnLine} {
		sc, err := ParseServeConfig(out)
		if err != nil || len(sc.Web) != 0 || len(sc.AllowFunnel) != 0 {
			t.Fatalf("%q 应是零值: %+v %v", out, sc, err)
		}
	}
	for _, out := range []string{`{"Web": oops}`, warnLine + `{"Web":`} {
		if _, err := ParseServeConfig(out); err == nil {
			t.Fatalf("%q: 坏 JSON 应报错", out)
		}
	}
}

func TestServeConfigBrokenJSONIsError(t *testing.T) {
	c, _ := fakeCLI(t, statusRunning, `{"Web": oops}`, "", 0)
	if _, err := c.ServeConfig(context.Background()); err == nil {
		t.Fatal("坏 JSON 应报错")
	}
}

func TestStatusErrors(t *testing.T) {
	c, _ := fakeCLI(t, "not json at all\n", "", "", 0)
	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("非 JSON 应报错")
	}
	c.Path = filepath.Join(t.TempDir(), "missing")
	if _, err := c.Status(context.Background()); err == nil {
		t.Fatal("执行失败应报错")
	}
}

func TestEnableURLTrimsTrailingPunctuation(t *testing.T) {
	out := "To enable, visit https://login.tailscale.com/f/funnel?node=abc.\n"
	c, _ := fakeCLI(t, statusRunning, "", out, 1)
	var nf ErrFunnelNotEnabled
	if err := c.EnableFunnel(context.Background(), 8787); !errors.As(err, &nf) || nf.EnableURL != "https://login.tailscale.com/f/funnel?node=abc" {
		t.Fatalf("err = %v", err)
	}
}

func TestExecCallerCancelNotTimeoutMessage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}
	p := filepath.Join(t.TempDir(), "slow")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nsleep 5\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Exec(ctx, p)
	if err == nil || strings.Contains(err.Error(), "超过") || !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
