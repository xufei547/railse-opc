# Railse OPC v0.1.3：本机 hub 一条命令（`railse setup all --local-hub`）

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 不要服务器、域名和证书，一条命令在工作机上起 hub、用 Tailscale Funnel 给手机一个 https 地址、接入本机守护进程、装钩子、开机自启、体检，最后打印手机要打开的地址。

**Architecture:** 新增纯逻辑包 `internal/tailscale`（找 `tailscale` 可执行文件、读 `status --self --json` 与 `serve status --json`、开 Funnel），`cmd/railse` 里新增 `setup_localhub.go`（第 0 步“本机 hub”的编排：Funnel → 密码 → 托管 hub → 等它应答 → 接入码）与 `localhub.go`（`<hub 数据目录>/local-hub.json` 的读写、hub run 参数、代理提醒），`setup all` 加 `--local-hub` 并把步骤表改成动态的，向导加第一问“hub 在哪里”，doctor 加 `hubsrv.local`。hub 本身不改：监听 `127.0.0.1:<端口>`，`--public-url https://<机器名>.<tailnet>.ts.net`，`--trusted-proxy 127.0.0.1,::1`，由现有的 `setup autostart --hub` 托管；守护进程接本机的 `http://127.0.0.1:<端口>`。

**Tech Stack:** Go 1.26 标准库；`tailscale` CLI 经 `os/exec` 调用（不引 tailscale 的 Go 包）；测试用临时目录里的假 `tailscale` 脚本与进程内起的真 hub。

背景与已定决策见设计文档 §0.6 的“v0.1.3：本机 hub 一条命令（2026-10-28 已拍板）”。实验数据：hub 跑在 MacBook Air 上、`tailscale funnel --bg 8787` 之后，手机登录、订阅、推送 2 秒内送达、从通知点开批准；本机 Clash 的代理节点连 Tailscale 入口握手即断，直连通。

## Global Constraints

- Go 不新增 require：`go.mod`、`go.sum` 不变。`tailscale` 只经 `exec.CommandContext` 调用，每次调用 15 秒超时（变量，测试可调短），stdin 是 `/dev/null`。
- hub 永远只监听 `127.0.0.1:<端口>`（默认 8787），对外只经 Funnel；`--trusted-proxy 127.0.0.1,::1`。hub 不以 root 运行（现有规则）。Funnel 没在管理台开通时，把 tailscale 输出里的开通链接原样打印、退出 1，不自动绕过、不改 ACL。
- 任何输出都不带接入码、密码、机器密钥。接入码只在进程内从 hub 数据库生成、直接交给 `agentEnroll`，不打印。
- 私密文件 0600、目录 0700、原子写（`cmd/railse` 现有的 `writeFileAtomic`）。`local-hub.json` 0600。
- 中文文案、中文注释、中文提交信息；代码贴近周围写法。
- 测试一律用临时 HOME（`t.Setenv("HOME", …)`，`os.MkdirTemp("", "rs")`），`RAILSE_HOME`、`RAILSE_HUB_HOME` 指向临时目录；任何测试都不得读写真实的 `~/.claude`、`~/.gemini`、`~/.codex`、`~/.railse`、`~/.railse-hub`，不得真跑 `launchctl`、`systemctl`、`tailscale`（用 `autostartEnv` 记录命令的 `runCommand`、用假 `tailscale` 脚本）。不在 CI 里要求有 Tailscale。
- 每项先写失败测试（看到它因预期原因失败）再实现；`make test` 全过后提交；每项至少一个中文提交。不 push、不合并、不打 tag。
- 用户可见的行为改动同步到 `docs/setup.md`、`docs/hub.md`、`README.md`、`README.en.md` 对应段落（Task 6 集中做，但每个 Task 改了用法文本就一起改）。

## 文件总览

| 文件 | 职责 |
| --- | --- |
| `internal/tailscale/tailscale.go`（新） | 找可执行文件；`Status`（`status --self --json`）；`ServeConfig`（`serve status --json`）与 `FunnelFor`；`EnableFunnel`（`funnel --bg <端口>`）与 `ErrFunnelNotEnabled` |
| `internal/tailscale/tailscale_test.go`（新） | 用假脚本测上面全部 |
| `cmd/railse/localhub.go`（新） | `localHub` 结构与 `local-hub.json` 读写；`localHubRunArgs`；`hubAnswers`；`ensureHubPassword`（从 `runHub` 的 password 分支抽出）；`proxyNote` |
| `cmd/railse/setup_localhub.go`（新） | `setupLocalHub`：第 0 步的编排 |
| `cmd/railse/setup.go` | `setup all` 的 `--local-hub`、`--hub-port`、`--hub-dir`、`--public-url`；动态步骤表；本机模式的接入与手机地址 |
| `cmd/railse/setup_wizard.go` | `askHubWhere` 第一问；`printPhoneStep` 本机模式的措辞 |
| `cmd/railse/hub.go` | password 分支改调 `ensureHubPassword` 共用的 `setHubPassword` |
| `internal/doctor/hub.go`、`internal/doctor/doctor.go` | `hubsrv.local` 检查；`RunCmd` 注释改 |
| 文档 | `docs/setup.md`、`docs/hub.md`、`README.md`、`README.en.md`、设计文档 §0.6（已加） |

---

### Task 1: `internal/tailscale`：找、看状态、看 serve 配置、开 Funnel

**Files:**
- Create: `internal/tailscale/tailscale.go`
- Test: `internal/tailscale/tailscale_test.go`

**Interfaces:**
- Produces（后面的 Task 都用这些名字）：

```go
package tailscale

// Runner 跑一次 tailscale 命令，返回合并的 stdout+stderr。默认实现见 Exec；测试换成假的。
type Runner func(ctx context.Context, path string, args ...string) (string, error)

// Timeout 是每次调用 tailscale 的时限。是变量只为测试能调短。
var Timeout = 15 * time.Second

// Exec 是默认的 Runner：stdin 为空（/dev/null），超过 Timeout 就杀掉并报错。
func Exec(ctx context.Context, path string, args ...string) (string, error)

// Find 找 tailscale 可执行文件：先 lookPath("tailscale")，再按 goos 看固定位置：
// darwin：/usr/local/bin/tailscale、/opt/homebrew/bin/tailscale、/Applications/Tailscale.app/Contents/MacOS/Tailscale；
// linux：/usr/bin/tailscale、/usr/local/bin/tailscale。找不到返回 ErrNotFound（错误文案含安装地址 https://tailscale.com/download）。
func Find(lookPath func(string) (string, error), stat func(string) (os.FileInfo, error), goos string) (string, error)

var ErrNotFound = errors.New("没有找到 tailscale：请先安装并登录（https://tailscale.com/download）")

type CLI struct {
    Path string
    Run  Runner // 空用 Exec
}

// Status 是 `tailscale status --self --json` 里用到的几项。DNSName 已去掉末尾的点。
type Status struct {
    BackendState string   // "Running" 才可用；"NeedsLogin"、"Stopped" 等见 ErrNotRunning 的文案
    DNSName      string   // 如 macbook-air.tail2613ae.ts.net
    Online       bool
    CertDomains  []string // 含 DNSName 才说明 tailnet 开了 HTTPS 证书
}

func (c CLI) Status(ctx context.Context) (Status, error)

// ServeConfig 是 `tailscale serve status --json` 的结果（tailscale 的 ipn.ServeConfig，只解析用到的字段）。
// 输出不是 JSON（如 "No serve config"）或为空时是零值。
type ServeConfig struct {
    Web         map[string]struct{ Handlers map[string]struct{ Proxy string } }
    AllowFunnel map[string]bool
}

func (c CLI) ServeConfig(ctx context.Context) (ServeConfig, error)

// FunnelFor 报告 dns 的 443 是否已经在 Funnel 上把 / 代理到 http://127.0.0.1:<port>。
func (sc ServeConfig) FunnelFor(dns string, port int) bool

// ErrFunnelNotEnabled：tailnet 还没开通 Funnel；EnableURL 是 tailscale 输出里的开通链接（login.tailscale.com 下）。
type ErrFunnelNotEnabled struct{ EnableURL string }

// EnableFunnel 跑 `tailscale funnel --bg <port>`。退出非零时：输出里有 https://login.tailscale.com/ 开头的链接就返回
// ErrFunnelNotEnabled，否则返回带输出的错误。
func (c CLI) EnableFunnel(ctx context.Context, port int) error
```

- [ ] **Step 1: 写失败测试**

`internal/tailscale/tailscale_test.go`。假脚本：测试在临时目录写一个 `tailscale` shell 脚本，按参数回放文件内容；Unix 之外跳过（`runtime.GOOS == "windows"`）。

```go
package tailscale

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	os.WriteFile(p, []byte("#!/bin/sh\nsleep 5\n"), 0o755)
	old := Timeout
	Timeout = 200 * time.Millisecond
	t.Cleanup(func() { Timeout = old })
	if _, err := Exec(context.Background(), p); err == nil || !strings.Contains(err.Error(), "超过") {
		t.Fatalf("应超时: %v", err)
	}
}
```

（测试文件里 import `strconv` 与 `time`。）

- [ ] **Step 2: 跑测试，确认因为包不存在而失败**

Run: `go test ./internal/tailscale/ 2>&1 | head`
Expected: `no Go files` 或 undefined 的编译错误。

- [ ] **Step 3: 实现 `internal/tailscale/tailscale.go`**

```go
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

type Runner func(ctx context.Context, path string, args ...string) (string, error)

var Timeout = 15 * time.Second

func Exec(ctx context.Context, path string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = nil // /dev/null：tailscale 等输入时直接读到 EOF
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return string(out), fmt.Errorf("%s %s 超过 %s 没有结束，已终止", path, strings.Join(args, " "), Timeout)
	}
	return string(out), err
}

var ErrNotFound = errors.New("没有找到 tailscale：请先安装并登录（https://tailscale.com/download）")

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

type Status struct {
	BackendState string
	DNSName      string
	Online       bool
	CertDomains  []string
}

func (c CLI) Status(ctx context.Context) (Status, error) {
	out, err := c.run(ctx, "status", "--self", "--json")
	if err != nil {
		return Status{}, fmt.Errorf("tailscale status: %w（%s）", err, strings.TrimSpace(out))
	}
	var raw struct {
		BackendState string
		Self         struct {
			DNSName string
			Online  bool
		}
		CertDomains []string
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return Status{}, fmt.Errorf("tailscale status 的输出不是 JSON: %w", err)
	}
	return Status{BackendState: raw.BackendState, DNSName: strings.TrimSuffix(raw.Self.DNSName, "."), Online: raw.Self.Online, CertDomains: raw.CertDomains}, nil
}

type ServeConfig struct {
	Web         map[string]struct{ Handlers map[string]struct{ Proxy string } }
	AllowFunnel map[string]bool
}

func (c CLI) ServeConfig(ctx context.Context) (ServeConfig, error) {
	out, err := c.run(ctx, "serve", "status", "--json")
	if err != nil {
		return ServeConfig{}, fmt.Errorf("tailscale serve status: %w（%s）", err, strings.TrimSpace(out))
	}
	s := strings.TrimSpace(out)
	if !strings.HasPrefix(s, "{") { // "No serve config" 之类
		return ServeConfig{}, nil
	}
	var sc ServeConfig
	if err := json.Unmarshal([]byte(s), &sc); err != nil {
		return ServeConfig{}, fmt.Errorf("tailscale serve status 的输出不是 JSON: %w", err)
	}
	return sc, nil
}

func (sc ServeConfig) FunnelFor(dns string, port int) bool {
	key := dns + ":443"
	w, ok := sc.Web[key]
	if !ok || !sc.AllowFunnel[key] {
		return false
	}
	return w.Handlers["/"].Proxy == "http://127.0.0.1:"+strconv.Itoa(port)
}

type ErrFunnelNotEnabled struct{ EnableURL string }

func (e ErrFunnelNotEnabled) Error() string {
	return "这个 tailnet 还没开通 Funnel：在浏览器里打开 " + e.EnableURL + " 开通后重跑"
}

var enableURLRe = regexp.MustCompile(`https://login\.tailscale\.com/\S+`)

func (c CLI) EnableFunnel(ctx context.Context, port int) error {
	out, err := c.run(ctx, "funnel", "--bg", strconv.Itoa(port))
	if err == nil {
		return nil
	}
	if u := enableURLRe.FindString(out); u != "" {
		return ErrFunnelNotEnabled{EnableURL: u}
	}
	return fmt.Errorf("tailscale funnel --bg %d: %w（%s）", port, err, strings.TrimSpace(out))
}
```

- [ ] **Step 4: 跑测试通过；gofmt、vet、lint**

Run: `go test ./internal/tailscale/ -race -count=1 && make gofmt && go vet ./... && make lint`
Expected: `ok`，lint 0 问题。

- [ ] **Step 5: 提交**

```bash
git add internal/tailscale
git commit -m "tailscale: 经命令行查状态、看 serve 配置、开 Funnel（本机 hub 用）"
```

---

### Task 2: `local-hub.json`、hub run 参数、hub 应答、设密码、代理提醒

**Files:**
- Create: `cmd/railse/localhub.go`
- Modify: `cmd/railse/hub.go`（`password` 分支改调共用函数）
- Test: `cmd/railse/localhub_test.go`

**Interfaces:**
- Consumes: `hub.Config`、`hub.New`、`(*hub.Server).SetPassword`、`store.Open`、`(*store.Store).GetSetting("password_hash")`、`readPassword`（`hub.go`）、`writeFileAtomic`（`setup.go`）、`runCommand`（`autostart.go`，测试可换）。
- Produces：

```go
// localHub 是 setup all --local-hub 装的本机 hub 的描述，存在 <hub 数据目录>/local-hub.json（0600），重跑与 doctor 都读它。
type localHub struct {
	Port      int    `json:"port"`       // hub 监听 127.0.0.1:<Port>
	PublicURL string `json:"public_url"` // 手机打开的 https 源，如 https://macbook-air.tail2613ae.ts.net
	Provider  string `json:"provider"`   // "tailscale"（Funnel）或 "custom"（用户给的 --public-url）
}

const localHubFile = "local-hub.json"

func localHubPath(hubDir string) string
func loadLocalHub(hubDir string) (lh localHub, ok bool, err error) // 没有文件时 ok=false、err=nil；坏 JSON 或字段不合规（Port 不在 1..65535、PublicURL 不是 https 源）是 err
func saveLocalHub(hubDir string, lh localHub) error

// localHubRunArgs 是托管 hub 时 -- 之后给 hub run 的参数。
func localHubRunArgs(lh localHub) []string
// 结果：["--listen", "127.0.0.1:<Port>", "--public-url", lh.PublicURL, "--trusted-proxy", "127.0.0.1,::1"]

// hubAnswers 问 http://127.0.0.1:<port>/api/version：是 railse hub 时返回它的版本与 true。超时 2 秒，不跟随重定向。
func hubAnswers(port int) (version string, ok bool)

// waitHub 每 200 毫秒问一次，直到应答或过了 wait（localHubWait，变量，默认 10 秒；测试设 0）。
func waitHub(port int, wait time.Duration) (string, bool)
var localHubWait = 10 * time.Second

// setHubPassword 从 stdin 读一行设为密码（同 railse hub password），返回变更（删了多少订阅）。
func setHubPassword(cfg hub.Config, st *store.Store, stdin io.Reader, stderr io.Writer) (store.PasswordChange, error)

// ensureHubPassword：settings 里已有 password_hash 返回 false、nil；没有时 stdin 是终端就提示并 setHubPassword 返回 true；
// 不是终端返回错误“hub 还没设网页密码：先运行 railse hub password（--dir <目录>），或在终端里跑 setup”。
func ensureHubPassword(cfg hub.Config, st *store.Store, stdin io.Reader, terminal bool, stdout, stderr io.Writer) (bool, error)

// proxyNote 报告本机是否开着系统代理：环境变量 HTTPS_PROXY/https_proxy/HTTP_PROXY/http_proxy/ALL_PROXY 任一非空，
// 或 darwin 上 `scutil --proxy` 输出含 "HTTPSEnable : 1"、"HTTPEnable : 1"、"SOCKSEnable : 1" 之一。
// 返回给人看的一句提醒（空表示没开代理）：“本机开着代理；手机若也经同一代理打不开 <url>，关掉代理再试，或在代理规则里把 ts.net 设为直连”。
func proxyNote(goos string, getenv func(string) string, run func(name string, args ...string) (string, error), publicURL string) string
```

- [ ] **Step 1: 写失败测试**（`cmd/railse/localhub_test.go`）

```go
package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/hub"
	"github.com/xufei547/railse-opc/internal/hub/store"
)

func TestLocalHubFileRoundTripAndMode(t *testing.T) {
	dir := tempDir(t)
	if _, ok, err := loadLocalHub(dir); ok || err != nil {
		t.Fatalf("没有文件应是 ok=false: %v %v", ok, err)
	}
	want := localHub{Port: 8787, PublicURL: "https://macbook-air.tail2613ae.ts.net", Provider: "tailscale"}
	if err := saveLocalHub(dir, want); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(localHubPath(dir))
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", st.Mode().Perm())
	}
	got, ok, err := loadLocalHub(dir)
	if err != nil || !ok || got != want {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	os.WriteFile(localHubPath(dir), []byte(`{"port":0,"public_url":"http://x"}`), 0o600)
	if _, _, err := loadLocalHub(dir); err == nil {
		t.Fatal("端口 0 与 http 源应报错")
	}
}

func TestLocalHubRunArgs(t *testing.T) {
	got := strings.Join(localHubRunArgs(localHub{Port: 9000, PublicURL: "https://a.b.ts.net"}), " ")
	if got != "--listen 127.0.0.1:9000 --public-url https://a.b.ts.net --trusted-proxy 127.0.0.1,::1" {
		t.Fatal(got)
	}
}

func TestHubAnswersOnlyForRailseHub(t *testing.T) {
	h := startTestHub(t) // 现有 helper：httptest 起的真 hub
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(h.http.URL, "http://"))
	port, _ := strconv.Atoi(portStr)
	if v, ok := hubAnswers(port); !ok || v == "" {
		t.Fatalf("真 hub 应应答: %q %v", v, ok)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hi")) }))
	defer other.Close()
	_, p2, _ := net.SplitHostPort(strings.TrimPrefix(other.URL, "http://"))
	port2, _ := strconv.Atoi(p2)
	if _, ok := hubAnswers(port2); ok {
		t.Fatal("别的服务不算 hub")
	}
	if _, ok := hubAnswers(freePort(t)); ok {
		t.Fatal("没人监听不算")
	}
}

func TestEnsureHubPassword(t *testing.T) {
	dir := tempDir(t)
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := hub.DefaultConfig(dir)
	var out, errOut bytes.Buffer
	// 不是终端、没密码：报错，不写
	if _, err := ensureHubPassword(cfg, st, strings.NewReader(""), false, &out, &errOut); err == nil || !strings.Contains(err.Error(), "railse hub password") {
		t.Fatalf("err = %v", err)
	}
	// 终端里读一行设上
	set, err := ensureHubPassword(cfg, st, strings.NewReader("correct horse battery\n"), true, &out, &errOut)
	if err != nil || !set {
		t.Fatalf("%v %v", set, err)
	}
	if _, ok, _ := st.GetSetting("password_hash"); !ok {
		t.Fatal("应写了 password_hash")
	}
	// 已有密码：不再问
	set, err = ensureHubPassword(cfg, st, strings.NewReader("should not be read\n"), true, &out, &errOut)
	if err != nil || set {
		t.Fatalf("已有密码不该再设: %v %v", set, err)
	}
	// 太短：报错（同 railse hub password 的规则，至少 8 个字符）
	dir2 := tempDir(t)
	st2, _ := store.Open(filepath.Join(dir2, "hub.db"))
	defer st2.Close()
	if _, err := ensureHubPassword(hub.DefaultConfig(dir2), st2, strings.NewReader("short\n"), true, &out, &errOut); err == nil {
		t.Fatal("短密码应报错")
	}
}

func TestProxyNote(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	noRun := func(string, ...string) (string, error) { return "", nil }
	if n := proxyNote("linux", env(nil), noRun, "https://x.ts.net"); n != "" {
		t.Fatalf("没代理应为空: %q", n)
	}
	if n := proxyNote("linux", env(map[string]string{"https_proxy": "http://127.0.0.1:7897"}), noRun, "https://x.ts.net"); !strings.Contains(n, "ts.net") || !strings.Contains(n, "https://x.ts.net") {
		t.Fatalf("%q", n)
	}
	scutil := func(name string, args ...string) (string, error) {
		if name == "scutil" && len(args) == 1 && args[0] == "--proxy" {
			return "<dictionary> {\n  HTTPEnable : 1\n  HTTPPort : 7897\n}\n", nil
		}
		return "", nil
	}
	if n := proxyNote("darwin", env(nil), scutil, "https://x.ts.net"); n == "" {
		t.Fatal("scutil 说开了代理就该提醒")
	}
	if n := proxyNote("linux", env(nil), scutil, "https://x.ts.net"); n != "" {
		t.Fatal("linux 不跑 scutil")
	}
}
```

`freePort(t)` 用现有的 `freeAddr(t)` 取端口：`_, p, _ := net.SplitHostPort(freeAddr(t)); n, _ := strconv.Atoi(p); return n`（放在本测试文件里）。

- [ ] **Step 2: 跑测试，确认因 undefined 失败**

Run: `go test ./cmd/railse/ -run 'TestLocalHub|TestHubAnswers|TestEnsureHubPassword|TestProxyNote' 2>&1 | head`
Expected: `undefined: localHub …`。

- [ ] **Step 3: 实现 `cmd/railse/localhub.go`，并把 `hub.go` 的 password 分支改成调 `setHubPassword`**

`localhub.go` 要点：
- `loadLocalHub`：`os.ReadFile`，`os.ErrNotExist` → `ok=false`；`json.Unmarshal`；校验 `Port` 在 1..65535、`PublicURL` 经 `parseHubAddr` 且 scheme 是 https 且没有路径；`Provider` 空当 `"custom"`。
- `saveLocalHub`：`json.MarshalIndent` 后 `writeFileAtomic(localHubPath(dir), data, 0o600)`。
- `hubAnswers`：`http.Client{Timeout: 2 * time.Second, CheckRedirect: 返回 http.ErrUseLastResponse}`，GET `http://127.0.0.1:<port>/api/version`，200 且 body 解析出 `{"version","agent_proto"}` 且 version 非空才算（与 doctor 的 `hubClientChecks` 同样判定）。
- `setHubPassword`：就是现在 `runHub` 里 `case "password"` 的中间三步（`readPassword` → `hub.New(cfg, st, log.New(io.Discard, "", 0))` → `srv.SetPassword(pw)`），`runHub` 改成调它并照旧打印两行。
- `ensureHubPassword`：`st.GetSetting("password_hash")` 有就 `false, nil`；没有且 `!terminal` → 错误文案含 `railse hub password` 与 `--dir <cfg.Dir>`；终端：stdout 打印一行“本机 hub 还没设网页密码，设一个（至少 8 个字符，手机登录用）：”后调 `setHubPassword`（它自己会在 stderr 打回显提示；这里 stdin 不是 `*os.File` 时不打那条，所以 stdout 这行是给人看的）。
- `proxyNote`：按接口说明。darwin 上 `run("scutil", "--proxy")` 出错当没开。

- [ ] **Step 4: 跑测试通过，`make test`**

Run: `go test ./cmd/railse/ -race -count=1 -run 'TestLocalHub|TestHubAnswers|TestEnsureHubPassword|TestProxyNote|TestHub' && make test`
Expected: 全过（`TestHub*` 是 hub 子命令的现有测试，password 分支改动后仍过）。

- [ ] **Step 5: 提交**

```bash
git add cmd/railse/localhub.go cmd/railse/localhub_test.go cmd/railse/hub.go
git commit -m "setup: 本机 hub 的描述文件、hub run 参数、应答探测、设密码共用、代理提醒"
```

---

### Task 3: 第 0 步“本机 hub”的编排 `setupLocalHub`

**Files:**
- Create: `cmd/railse/setup_localhub.go`
- Test: `cmd/railse/setup_localhub_test.go`

**Interfaces:**
- Consumes: Task 1 的 `tailscale.Find/CLI/Status/ServeConfig/FunnelFor/EnableFunnel/ErrFunnelNotEnabled/ErrNotFound`；Task 2 的全部；`runAutostart`（`autostart.go`）、`autostartStatus`（`--status` 的实现，返回 0 表示在跑）、`ensureHubDir`、`defaultHubDir`、`hub.DefaultConfig`、`(*hub.Config).Effective`、`store.Open`、`(*store.Store).CreateEnrollToken`。
- Produces：

```go
// 以下是变量只为测试能替换
var (
	tailscaleFind = func() (string, error) { return tailscale.Find(exec.LookPath, os.Stat, runtime.GOOS) }
	tailscaleRun  tailscale.Runner = tailscale.Exec
)

type localHubOpts struct {
	HubDir    string    // hub 数据目录（绝对路径）
	Bin       string    // railse 可执行文件（绝对路径），托管文件指向它
	Port      int       // 默认 8787
	PublicURL string    // 用户给的 --public-url（自己的隧道）；空则用 Tailscale Funnel
	Terminal  bool      // stdin 是否终端（没密码时能不能问）
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
}

// setupLocalHub 做第 0 步：定对外地址（Funnel 或 --public-url）→ 数据目录与密码 → 写 local-hub.json → 托管并启动 hub →
// 等它应答 → 生成接入码。返回描述、接入码（只交给 agentEnroll，不打印）与要在结尾“注意”里说的话（如对外地址变了）。
// 任何一步失败返回错误，调用方按 fail(步骤名) 处理。
func setupLocalHub(goos string, o localHubOpts) (lh localHub, token string, notes []string, err error)

// hubJobRunning 问服务管理器 hub 的托管作业在不在跑（autostartStatus 的 Kind: "hub" 版本，写法同 agentJobRunning）。是变量只为测试能替换。
var hubJobRunning = func(hubDir, bin string) bool
```

流程（每一步都向 Stdout 打一行人能看懂的进展）：

1. **对外地址。** `o.PublicURL != ""`：经 `parseHubAddr` 校验是 https、无路径，`Provider = "custom"`，打印“对外地址用你给的 …（自己的隧道要把它转到 127.0.0.1:<端口>）”。否则：`tailscaleFind()`（`ErrNotFound` 原样返回）；`cli := tailscale.CLI{Path: p, Run: tailscaleRun}`；`st := cli.Status(ctx)`：`BackendState != "Running"` → 错误“Tailscale 没在运行或没登录（状态 %s）：打开 Tailscale 登录后重跑”；`DNSName == ""` → 错误“Tailscale 没给这台机器域名：在管理台开启 MagicDNS 后重跑”；`!slices.Contains(st.CertDomains, st.DNSName)` → 错误“这个 tailnet 没开 HTTPS 证书：管理台 DNS 页开启 HTTPS Certificates 后重跑”。`PublicURL = "https://" + st.DNSName`。`sc := cli.ServeConfig(ctx)`；`!sc.FunnelFor(dns, port)` → `cli.EnableFunnel(ctx, port)`（`ErrFunnelNotEnabled` 原样返回：调用方会打印它的 Error，含开通链接）→ 再 `ServeConfig` 确认 `FunnelFor`，仍不是就错误“开了 Funnel 但 serve 配置里没看到 …，看 tailscale funnel status”。打印“对外地址 https://…（Tailscale Funnel 已把它转到 127.0.0.1:<端口>）”。
2. **数据目录与密码。** `ensureHubDir(o.HubDir)`；`cfg := hub.DefaultConfig(o.HubDir)`，`cfg.Listen = "127.0.0.1:<port>"`，`cfg.PublicURL = PublicURL`，`cfg.Version = version`，`cfg, err = cfg.Effective()`；`st, err := store.Open(cfg.DBPath)`，`defer st.Close()`；`ensureHubPassword(cfg, st, o.Stdin, o.Terminal, o.Stdout, o.Stderr)`。
3. **local-hub.json。** 先 `loadLocalHub`：有且 `PublicURL` 不同 → 打印“对外地址从 … 变成 …：手机上旧的图标要删掉重新添加、重新开启通知”，并把这条也作为 note 返回（见下）；然后 `saveLocalHub`。
4. **托管并启动。** 四种情况：
   - `hubAnswers(o.Port)` 为真且 `hubJobRunning(o.HubDir, o.Bin)` 为真且第 3 步里 local-hub.json 没变 → 打印“本机 hub 已在跑（版本 v），跳过托管”。
   - `hubAnswers` 为真、托管作业在跑、但 local-hub.json 变了（对外地址或端口换了）→ 重新托管：`runAutostart(append([]string{"--hub", "--dir", o.HubDir, "--bin", o.Bin, "--"}, localHubRunArgs(lh)...), o.Stdout, o.Stderr)`（它会停掉旧的再拉起），非 0 → 错误“托管 hub 失败，见上面”。
   - `hubAnswers` 为真但托管作业没在跑（手动 `railse hub run` 占着端口）→ 错误“127.0.0.1:<端口> 已有一个不是托管的 hub 在跑（手动起的？）：先停掉它，或用 --hub-port 换端口”。
   - `hubAnswers` 为假：`net.DialTimeout("tcp", "127.0.0.1:<端口>", 500*time.Millisecond)` 能连上 → 错误“127.0.0.1:<端口> 被别的程序占用：用 --hub-port 换一个”；连不上 → 同上调 `runAutostart` 托管并启动。
5. **等应答。** `waitHub(o.Port, localHubWait)`；超时 → 错误“hub 托管了但 %s 内没应答：看 <HubDir>/launchd.log（macOS）或 journalctl --user -u railse-hub（Linux）”。
6. **接入码。** `tok, err := st.CreateEnrollToken(10 * time.Minute)`。返回 `lh, tok, nil`。

“地址变了”的提醒放进返回的 `notes`；Task 4 把它们并进 `setup all` 的 `notes`。

- [ ] **Step 1: 写失败测试**（`cmd/railse/setup_localhub_test.go`）

测试基础设施（放在同一文件）：

```go
// fakeTailscale 用临时目录里的假脚本替换 tailscaleFind 与 tailscaleRun：status、serve 的输出可改，funnel --bg 会把 serve 输出换成 on。
type fakeTS struct {
	dir string
	funnelCalls int
}

func fakeTailscale(t *testing.T, status, serve string, funnelOut string, funnelCode int) *fakeTS {
	t.Helper()
	needUnixShell(t)
	dir := t.TempDir()
	f := &fakeTS{dir: dir}
	for name, s := range map[string]string{"status.json": status, "serve.json": serve, "funnel.out": funnelOut} {
		os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600)
	}
	// 与 Task 1 测试同样的脚本，funnel 成功时把 serve.on 复制成 serve.json（模拟开通后的 serve 配置）
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  \"status --self --json\") cat \"" + dir + "/status.json\" ;;\n" +
		"  \"serve status --json\") cat \"" + dir + "/serve.json\" ;;\n" +
		"  \"funnel --bg \"*) echo funnel >> \"" + dir + "/calls\"; cat \"" + dir + "/funnel.out\"; [ -f \"" + dir + "/serve.on\" ] && cp \"" + dir + "/serve.on\" \"" + dir + "/serve.json\"; exit " + strconv.Itoa(funnelCode) + " ;;\n" +
		"  *) exit 9 ;;\nesac\n"
	p := filepath.Join(dir, "tailscale")
	os.WriteFile(p, []byte(script), 0o755)
	oldFind, oldRun := tailscaleFind, tailscaleRun
	tailscaleFind = func() (string, error) { return p, nil }
	tailscaleRun = tailscale.Exec
	t.Cleanup(func() { tailscaleFind, tailscaleRun = oldFind, oldRun })
	return f
}

// startLocalTestHub 在 127.0.0.1:<port> 起一个真 hub（数据目录 dir），模拟“托管的 hub 已在跑”。
func startLocalTestHub(t *testing.T, dir string, port int) {
	t.Helper()
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := hub.DefaultConfig(dir)
	cfg.Listen = "127.0.0.1:" + strconv.Itoa(port)
	cfg, _ = cfg.Effective()
	srv, err := hub.New(cfg, st, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: srv.Handler()}
	go hs.Serve(l)
	t.Cleanup(func() { hs.Close(); st.Close() })
}
```

测试用例（`setupAllEnv` 已换掉 `runCommand`、不真跑 launchctl；`autostartStatus` 在测试里按 `rec` 的记录判断，照现有 autostart 测试的做法）：

- `TestSetupLocalHubHappyPathExistingHub`：假 tailscale（status Running、serve 已 on），临时 hubDir，`startLocalTestHub(t, hubDir, port)` 先起，`hubJobRunning` 换成恒真（`t.Cleanup` 还原）；stdin 给一行密码、`Terminal: true`。断言：返回的 `lh == {port, "https://macbook-air.tail2613ae.ts.net", "tailscale"}`；`local-hub.json` 存在且 0600；`password_hash` 已写；返回的接入码非空且在库里能 `ConsumeEnrollToken` 成功；stdout 含“已在跑”；`calls` 文件不存在（没调 funnel）。
- `TestSetupLocalHubEnablesFunnelWhenOff`：serve 一开始是 `No serve config`，`serve.on` 写成开通后的配置；断言 `calls` 里有一次 funnel，且最终成功。
- `TestSetupLocalHubFunnelNotEnabledShowsLink`：funnel 退出 1 并打印开通链接；断言错误是 `tailscale.ErrFunnelNotEnabled` 且 `Error()` 含链接；`local-hub.json` 没写（第 1 步就停）。
- `TestSetupLocalHubNeedsLoginAndNoCert`：status `NeedsLogin` → 错误含“登录”；status Running 但 `CertDomains` 空 → 错误含“HTTPS 证书”。
- `TestSetupLocalHubNoTailscale`：`tailscaleFind` 返回 `tailscale.ErrNotFound` → 错误是它，含安装地址。
- `TestSetupLocalHubCustomPublicURLSkipsTailscale`：`PublicURL: "https://hub.example.com"`，`tailscaleFind` 设成会 `t.Fatal` 的函数；断言 Provider 是 custom、没调 tailscale。
- `TestSetupLocalHubPortTakenByOther`：先用 `net.Listen` 占住端口（不是 hub）；断言错误含“被别的程序占用”。
- `TestSetupLocalHubManualHubRefused`：`startLocalTestHub` 起了、但 `hubJobRunning` 恒假；断言错误含“不是托管的 hub”。
- `TestSetupLocalHubReinstallsWhenURLChanged`：先写旧地址的 `local-hub.json`，hub 在跑、`hubJobRunning` 恒真；断言 `rec` 记录了重新托管的命令，notes 含“重新开启通知”。
- `TestSetupLocalHubNoPasswordNotTerminal`：没密码、`Terminal: false` → 错误含 `railse hub password`；`local-hub.json` 没写。
- `TestSetupLocalHubInstallsWhenNotRunning`：没有 hub 在跑，`hubJobRunning` 恒假，`localHubWait = 0`；断言 `rec` 记录了 launchctl（或 systemctl）相关命令且托管文件 `~/Library/LaunchAgents/dev.railse.hub.plist`（darwin）/`~/.config/systemd/user/railse-hub.service`（linux）的内容含 `--public-url https://macbook-air.tail2613ae.ts.net`、`--listen 127.0.0.1:<port>`、`--trusted-proxy 127.0.0.1,::1`；因为没人应答，返回错误含“没应答”与日志位置。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run TestSetupLocalHub 2>&1 | head`
Expected: `undefined: setupLocalHub`。

- [ ] **Step 3: 实现 `cmd/railse/setup_localhub.go`**（按上面的流程；`autostartStatus` 需要一个 `autostartSpec{Kind: "hub", Home: home, UID: os.Getuid(), Dir: o.HubDir, Bin: o.Bin}`，与 `agentJobRunning` 的写法一致，抽一个 `hubJobRunning(hubDir, bin string) bool`）

- [ ] **Step 4: 跑测试通过，`make test`**

Run: `go test ./cmd/railse/ -race -count=1 -run TestSetupLocalHub && make test`

- [ ] **Step 5: 提交**

```bash
git add cmd/railse/setup_localhub.go cmd/railse/setup_localhub_test.go
git commit -m "setup: 本机 hub 的第 0 步：Funnel、密码、托管、等应答、接入码"
```

---

### Task 4: `railse setup all --local-hub` 与向导第一问

**Files:**
- Modify: `cmd/railse/setup.go`（`setupAllUsage`、`setupUsage`、`runSetup`、`runSetupAllAs`）
- Modify: `cmd/railse/setup_wizard.go`（`askHubWhere`、`printPhoneStep`）
- Test: `cmd/railse/setup_test.go`、`cmd/railse/setup_wizard_test.go`

**Interfaces:**
- Consumes: Task 3 的 `setupLocalHub`、`localHubOpts`；Task 2 的 `loadLocalHub`、`proxyNote`、`localHubPath`。
- Produces：

```go
// askHubWhere 是向导的第一问。tailscaleFound 决定默认项：找到了默认 1（本机），否则默认 2。
// 回车取默认；答 1/2 以外的说明再问；EOF 返回 io.EOF。
func askHubWhere(in io.Reader, stdout io.Writer, tailscaleFound bool) (local bool, err error)

// printPhoneStep 加第三个参数 local：本机模式时多打一行“这个地址由 Tailscale 提供；手机挂着代理打不开时关掉代理再试”。
func printPhoneStep(w io.Writer, hubURL string, local bool)
```

**要求：**

1. 新标志（只在 `setup all`）：`--local-hub`（bool）、`--hub-port`（int，默认 8787，1..65535）、`--hub-dir`（string，默认 `$RAILSE_HUB_HOME` 或 `~/.railse-hub`，走 `defaultHubDir`）、`--public-url`（string）。校验（用法错误退出 2，排在拒绝 root 之后、提问之前）：`--local-hub` 与 `--hub`/`--token` 同时给；`--hub-port`/`--hub-dir`/`--public-url` 不带 `--local-hub`；`--hub-port` 越界；`--public-url` 不是 https 源或带路径（用 `parseHubAddr`）。
2. 本机模式的接入：`enroll` 的判定改为：没有 `machine.json` → 接入；有且 `--force` → 接入；有、没 `--force`：`m.HubURL == "http://127.0.0.1:<port>"` → 跳过接入（同现在）；否则（接的是别的 hub，如 Fly）→ stderr：“本机已接入 %s，不是本机 hub；要改接本机 hub，加 --force 重新接入（手机上旧 hub 的图标可以删掉）”，退出 1，**在第 0 步之前**判定（不白开 Funnel、不白生成接入码）。
3. 步骤表动态：本机模式 `steps = []string{"本机 hub", "接入 hub", "安装钩子", "开机自启", "体检"}`，否则现在的四个。`step`、`fail` 改为按名字找下标（`slices.Index(steps, name)`），所有调用处改成传名字。失败提示里的“修好后重跑 railse setup all”在本机模式下写成 `railse setup all --local-hub`（加上用户给的 `--hub-port`、`--hub-dir`、`--public-url`，与现有 `reenroll` 字符串的拼法一致）。
4. 第 0 步：`lh, tok, lnotes, err := setupLocalHub(runtime.GOOS, localHubOpts{HubDir, Bin: b, Port, PublicURL, Terminal: stdinIsTerminal(), Stdin: setupStdin, Stdout: stdout, Stderr: stderr})`；`err` 是 `tailscale.ErrFunnelNotEnabled` 时 stderr 打它的 `Error()` 再 `fail("本机 hub", "在浏览器里开通 Funnel 后重跑同一条命令。")`；其余 `fail("本机 hub", "")`。`notes = append(notes, lnotes...)`。然后 `*hubURL = "http://127.0.0.1:<port>"`，`*token = tok`，`phoneURL = lh.PublicURL`。本机模式不问 hub 地址、接入码；名字没给用主机名，不问。`missing` 在本机模式恒为 false。
5. 第 1 步接入时 `agentEnroll` 会对 `http://127.0.0.1` 不打明文警告（`plaintextHub` 对本机地址本来就不警告，确认一下测试里 stderr 没有“警告”）。
6. 重跑（已接本机 hub）：`confirmRerun` 的问句里 hub 地址用 `lh.PublicURL`（人认得的那个），不是 `127.0.0.1`。
7. 结尾：`proxyNote(runtime.GOOS, os.Getenv, runCommand, lh.PublicURL)` 非空时加进 `notes`；`printPhoneStep(stdout, phoneURL, localMode)`。
8. 向导：`runSetup` 没有参数且是终端时，先 `askHubWhere(setupStdin, stdout, found)`（`found` 由 `tailscaleFind()` 是否出错决定，出错不打印原因）；答本机 → `runSetupAll([]string{"--local-hub"}, …)`；否则照旧。已接入过（有 `machine.json`）时不问这一问，直接 `runSetupAll` 不带参数（它会按 `machine.json` 跳过接入；如果接的是本机 hub，`setup all` 自己从 `machine.json` 的 `127.0.0.1` 地址认出本机模式：`HubURL` 是 loopback 且 `local-hub.json` 存在时按本机模式走，第 0 步会复查 Funnel 与 hub 在不在跑）。
   - `askHubWhere` 读一行时**不要**用 `bufio.Reader`（它会预读后面的行，后面 `readPassword` 与 `confirmRerun` 读不到）：逐字节读到 `\n` 或 EOF（写一个 `readLineRaw(io.Reader) (string, error)`）。提示：

     ```
     hub 在哪里？
       1) 本机：用 Tailscale 给手机一个地址，不用服务器（要先装好并登录 Tailscale）
       2) 已有 hub：填它的地址和接入码
     选 [1]：
     ```
     默认项按 `tailscaleFound` 显示 `[1]` 或 `[2]`。
9. 用法文本：`setupAllUsage` 第一行后加一行 `railse setup all --local-hub [--hub-port 8787] [--hub-dir 目录] [--public-url https://…] [--name 名字] [--project 目录] [--dir 目录] [--bin 路径] [--no-autostart] [--no-hooks] [--force]`，说明“一台机器就够：本机起 hub，Tailscale Funnel 给手机地址，守护进程接本机 hub；--public-url 换成自己的隧道”。`setupUsage` 里 `railse setup` 的说明加“先问 hub 在哪里”。

- [ ] **Step 1: 写失败测试**

`setup_test.go` 加（都用 `setupAllEnv`、`fakeTailscale`、`startLocalTestHub`、`fakeStdin`）：

- `TestSetupAllLocalHubEndToEnd`：hub 已在跑（`startLocalTestHub`，`hubJobRunning` 换成恒真）、假 tailscale 已 on、stdin 一行密码、终端；`run([]string{"setup","all","--local-hub","--hub-port",port,"--hub-dir",hubDir,"--bin",f.bin,"--no-autostart"})` 退出 0；断言：`~/.railse/machine.json` 的 `hub_url == "http://127.0.0.1:<port>"`；三个钩子装了（同 `TestSetupAllRailseHomeOnlyWritesSocket` 的断言）；stdout 含“== 1. 本机 hub ==”、“== 2. 接入 hub ==”…“== 5. 体检 ==”，含框线里的 `https://macbook-air.tail2613ae.ts.net`，含“Tailscale”；stdout、stderr 都不含接入码（从库里 `ConsumeEnrollToken` 已消耗，拿不到原文，这里断言输出不含“接入码:”这一行即可）与密码原文。
- `TestSetupAllLocalHubRejectsHubFlags`：`--local-hub --hub https://x` → 2；`--hub-port 1` 不带 `--local-hub` → 2；`--local-hub --public-url http://x` → 2；`--local-hub --hub-port 70000` → 2。
- `TestSetupAllLocalHubRefusesWhenEnrolledElsewhere`：先写一个 `machine.json`（`hub_url` 是 `https://railse-hub.fly.dev`）；`--local-hub` 不带 `--force` → 1，stderr 含“--force”；假 tailscale 的 `calls` 不存在（没开 Funnel）。带 `--force` → 0，`machine.json` 改成 127.0.0.1。
- `TestSetupAllLocalHubFunnelNotEnabledStopsAtStep1`：funnel 退出 1 带链接；退出 1，stderr 含链接与“停在第 1 步（本机 hub）”，`machine.json` 不存在。
- `TestSetupAllLocalHubRerunConfirmShowsPublicURL`：已接本机 hub（machine.json 指 127.0.0.1，local-hub.json 存在，hub 在跑），终端，stdin 给 `n\n`（密码已设，所以第一行是确认）；断言问句含 `https://macbook-air.tail2613ae.ts.net`，退出 1，“已取消”。
- `TestSetupAllLocalHubProxyNote`：`t.Setenv("https_proxy", "http://127.0.0.1:7897")`；断言 stdout 的“注意：”含“ts.net”。

`setup_wizard_test.go` 加：
- `TestSetupWizardFirstQuestionLocal`：`fakeStdin(t, true, "1\ncorrect horse battery\n")`，假 tailscale 已 on，hub 在跑（端口 8787 可能被占：测试用 `--hub-port`？向导不带参数——所以向导模式下让 `startLocalTestHub` 起在 8787；8787 被占时 `t.Skip("8787 被占")`）；断言走了本机流程（stdout 含“== 1. 本机 hub ==”）。
- `TestSetupWizardFirstQuestionRemote`：`fakeStdin(t, true, "2\nhttps://hub.example.com\n<token>\n\n")`（token 用 `newEnrollToken(t, h)`，h 是 `startTestHub`；hub 地址答 `h.http.URL`）；断言和现在 `TestSetupWizardAsksThenRunsSetupAll` 一样的结果。
- `TestSetupWizardFirstQuestionDefaults`：`tailscaleFind` 出错时提示里是 `[2]`，直接回车走远程问答；找到时 `[1]`，回车走本机。
- `TestSetupWizardFirstQuestionReasks`：答 `3\n` 再答 `2\n…`；断言出现一次“请答 1 或 2”。
- `TestSetupWizardSkipsFirstQuestionWhenEnrolled`：有 `machine.json` 时不出现“hub 在哪里”。
- `TestPrintPhoneStep` 扩：`local=true` 时多一行含“代理”。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./cmd/railse/ -run 'TestSetupAllLocalHub|TestSetupWizardFirstQuestion|TestPrintPhoneStep' 2>&1 | head`

- [ ] **Step 3: 实现**（按要求 1–9；`runSetupAllAs` 现有流程只插入第 0 步与几处分支，不重排）

- [ ] **Step 4: 跑测试通过，`make test`，再手动看一眼用法文本**

Run: `make test && go run ./cmd/railse setup all --help 2>&1 | head -20`

- [ ] **Step 5: 提交**

```bash
git add cmd/railse/setup.go cmd/railse/setup_wizard.go cmd/railse/setup_test.go cmd/railse/setup_wizard_test.go
git commit -m "setup: setup all --local-hub 一条命令起本机 hub 并接入；向导先问 hub 在哪里"
```

---

### Task 5: doctor 的 `hubsrv.local`

**Files:**
- Modify: `internal/doctor/hub.go`（`hubServerChecks` 的 items 加 `{"hubsrv.local", "本机 hub 对外地址"}`，实现 `localHubCheck`）
- Modify: `internal/doctor/doctor.go`（`Options.RunCmd` 注释改成“跑 `<cli> --version` 与 `tailscale serve status --json`”；`Options` 加 `TailscalePath func() (string, error)`，默认 `tailscale.Find(exec.LookPath, os.Stat, runtime.GOOS)`）
- Test: `internal/doctor/doctor_test.go`
- Modify: `docs/setup.md` 的 doctor 表格（Task 6 一起）

**要求：**

- 读 `<HubDir>/local-hub.json`（doctor 不 import `cmd/railse`：把 `localHub` 的 JSON 结构在 doctor 里按同样字段再定义一次，只读）。没有文件 → `Skip`，细节“没有 local-hub.json（不是 setup all --local-hub 装的）”。坏文件 → `Fail`，提示重跑 `railse setup all --local-hub`。
- `provider == "tailscale"`：sudo 下（现有 `sudoSkip` 判定）→ `Skip`“sudo 下不跑 tailscale”；`TailscalePath()` 出错 → `Fail`“找不到 tailscale”；`RunCmd(path, "serve", "status", "--json")` 出错 → `Fail` 带输出摘要；用 Task 1 的 `tailscale.ServeConfig` 解析（doctor import `internal/tailscale` 的类型与 `FunnelFor`，把输出解析抽成包级函数 `tailscale.ParseServeConfig(out string) (ServeConfig, error)`，Task 1 的 `CLI.ServeConfig` 也改用它）；`!FunnelFor(host, port)` → `Fail`“Funnel 没把 <host> 转到 127.0.0.1:<port>”，提示 `tailscale funnel --bg <port>` 或重跑 setup。
- 不论 provider：`o.HTTP.Get(publicURL + "/api/version")`：200 且是 hub 版本 → `OK`，细节“<publicURL>，hub 版本 x”；失败 → `Warn`（不是 Fail），细节带错误，提示“从本机访问不到对外地址：可能是本机代理拦截了（见 setup 结尾的提醒），用手机 4G 打开验证；Funnel 入口 TLS 握手失败时先关掉代理再试”。
- 端口上的 hub 本身（`127.0.0.1:<port>/api/version`）由现有 `hub.reachable`（按 `machine.json` 的地址）覆盖，这里不重复。

- [ ] **Step 1: 写失败测试**（`doctor_test.go`，沿用现有 doctor 测试的 `Options` 注入方式：`RunCmd`、`HTTP` 用 `httptest`、`HubDir` 临时目录）：没有文件 skip；tailscale 的 serve 输出 on 且 HTTP 200 → ok；serve 输出 `No serve config` → fail 含 `funnel --bg`；serve on 但 HTTP 出错 → warn 含“代理”；provider custom 不调 RunCmd（RunCmd 里 `t.Fatal`）；sudo（`Geteuid` 返回 0）→ skip。
- [ ] **Step 2: 跑测试确认失败**：`go test ./internal/doctor/ -run LocalHub`
- [ ] **Step 3: 实现**
- [ ] **Step 4: `make test`**
- [ ] **Step 5: 提交**

```bash
git add internal/doctor internal/tailscale
git commit -m "doctor: 本机 hub 的对外地址检查（Funnel 配置与从本机访问）"
```

---

### Task 6: 文档与 README

**Files:**
- Modify: `README.md`、`README.en.md`（“三步上手”前加“一台机器就够”）
- Modify: `docs/setup.md`（新节“一台机器就够：本机 hub”放在“hub 机器”之前；“工作机”里 `setup all` 标志一览加四个新标志；doctor 表格加 `hubsrv.local`；“卸载”段加 `tailscale funnel --https=443 off`）
- Modify: `docs/hub.md`（“部署与反向代理”加“C. 本机 hub + Tailscale Funnel”；“已知限制”加睡眠、改机器名、代理三条）

**要求（文案按作者的写法，短句、说清代价）：**

1. README 中文“三步上手”改成两种走法，先写简单的：

   ```markdown
   ## 上手

   **一台机器就够。** 工作机上装好 [Tailscale](https://tailscale.com/download) 并登录，然后：

   ```sh
   railse setup all --local-hub
   ```

   它在本机起 hub，用 Tailscale Funnel 给手机一个固定的 https 地址（不用服务器、域名、证书），接入本机守护进程，装钩子，设开机自启，跑体检，最后打印手机要打开的地址。第一次会让你设手机登录的密码；tailnet 没开通 Funnel 时它会打印开通链接，开通后重跑。机器睡眠时 hub 也不在，但那时 agent 也停了，单机用户没损失；几台机器的，把 hub 放在常开的那台。手机挂着代理打不开地址时，关掉代理，或在代理规则里把 `ts.net` 设为直连。

   **有服务器的。** 三步：hub 机器、每台工作机、手机（下面原来的三步）。
   ```
   英文版同结构。
2. `docs/setup.md` 新节写清：前提（Tailscale 已装、已登录、tailnet 开了 HTTPS 证书与 Funnel；管理台链接由命令打印）、做了什么（五步）、落下的文件（`~/.railse-hub/local-hub.json`、`dev.railse.hub` 托管、`machine.json` 指 `http://127.0.0.1:8787`）、怎么换端口与数据目录、`--public-url` 用自己隧道时要把它转到 `127.0.0.1:<端口>` 并保留 Host、转发 WebSocket、追加 `X-Forwarded-For`、重跑与改机器名的后果、卸载：`railse setup autostart --hub --uninstall`，`tailscale funnel --https=443 off`，删 `~/.railse-hub`（含密码、推送密钥，删了手机要重新开通知）。
3. `docs/hub.md` 的 C 节给出等价的手动命令（`tailscale funnel --bg 8787`、`railse hub run --listen 127.0.0.1:8787 --public-url https://<机器名>.<tailnet>.ts.net --trusted-proxy 127.0.0.1,::1`）与 Funnel 的事实：只开 443、证书由 Tailscale 签、入口在 Tailscale 的服务器、机器名就是域名。
4. README “现状与限制”加一条：本机 hub 的地址绑在 Tailscale 机器名上，改名等于换域名。

- [ ] **Step 1: 改文档**
- [ ] **Step 2: 检查链接与命令**：`grep -n "local-hub" README.md README.en.md docs/setup.md docs/hub.md` 每处都能对上代码里的标志名；`make test`（网页检查不受影响，只是跑一遍确认没碰坏什么）
- [ ] **Step 3: 提交**

```bash
git add README.md README.en.md docs/setup.md docs/hub.md
git commit -m "docs: 一台机器就够：setup all --local-hub 的上手、原理、代价与卸载"
```

---

## 手动验收（合并前由维护者在真机做，不进 CI）

在作者的 MacBook Air（Tailscale 已登录，Funnel 已开通；`~/.railse-hub` 里已有实验留下的密码与一条订阅）：

1. `railse setup all --local-hub --force`（本机现在接的是 Fly 的 hub，所以要 `--force`）。预期：第 1 步说 Funnel 已开、密码已设、hub 托管并应答；第 2 步接入 `http://127.0.0.1:8787`；第 3、4 步照旧；第 5 步体检 `hubsrv.local` 是 ok 或（本机 Clash 开着时）warn 且提示代理；结尾框线里是 `https://macbook-air.tail2613ae.ts.net`，注意里有代理提醒。
2. 手机（关代理）打开地址登录，开通知；在任一项目里让 Claude Code 跑 `curl https://example.com`，手机收到推送并批准，终端继续。
3. `railse doctor` 全 ok（Clash 关掉时）。
4. 重跑 `railse setup all --local-hub`：确认问句里是 ts.net 地址；答 n 取消；答 y 走完。
5. 把本机改回 Fly：`railse setup all --force --hub https://railse-hub.fly.dev --token …`，并 `railse setup autostart --hub --uninstall`、`tailscale funnel --https=443 off`（或保留本机 hub，作者自定）。

## 自检记录（写计划时做过）

- 覆盖：对外地址（Task 1、3）、密码（2、3）、托管与等待（3）、接入与步骤表（4）、向导（4）、doctor（5）、文档（6）、Funnel 未开通的处理（1、3、4）、代理提醒（2、4）、改名后果（3、6）、端口冲突（3）、接了别的 hub 时拒绝（4）。
- 名字一致：`localHub`/`localHubPath`/`loadLocalHub`/`saveLocalHub`/`localHubRunArgs`/`hubAnswers`/`waitHub`/`localHubWait`/`setHubPassword`/`ensureHubPassword`/`proxyNote`（Task 2）在 Task 3、4 里同名使用；`setupLocalHub(goos, localHubOpts) (localHub, string, []string, error)`（Task 3 末尾改为四返回值，Task 4 按四返回值调用）；`tailscaleFind`、`tailscaleRun`（Task 3 定义，Task 4 的向导用 `tailscaleFind`）；`tailscale.ParseServeConfig`（Task 5 新增，Task 1 的 `CLI.ServeConfig` 改用它）；`askHubWhere`、`printPhoneStep(w, url, local)`（Task 4）。
