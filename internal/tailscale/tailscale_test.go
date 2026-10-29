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
// `funnel --bg N` 打印 funnel.out 并按 funnel.code 退出（没有则 0），别的参数退出 9。参数记进 args.log。
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
  "funnel --bg "*) cat "` + filepath.Join(dir, "funnel.out") + `"; exit ` + strconv.Itoa(funnelCode) + ` ;;
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

func TestStatusNeedsLoginIsError(t *testing.T) {
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
	if _, err := Exec(context.Background(), p); err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("应超时: %v", err)
	}
}
