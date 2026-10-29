package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/xufei547/railse-opc/internal/hub"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/tailscale"
)

// 假 tailscale 的本机：status 里的域名（末尾带点，同真 tailscale）与它给出的对外地址。
const (
	tsDNS       = "macbook-air.tail2613ae.ts.net"
	tsPublicURL = "https://" + tsDNS
	tsStatusOK  = `{"BackendState":"Running","Self":{"DNSName":"` + tsDNS + `.","Online":true},"CertDomains":["` + tsDNS + `"]}`
	// localHubTestPassword 是测试里在终端输入的网页密码：任何输出里都不该出现它。
	localHubTestPassword = "correct horse battery"
)

// tsServeOn 是 Funnel 已把 https://<tsDNS> 转到 127.0.0.1:<port> 时 serve status --json 的输出。
func tsServeOn(port int) string {
	return `{"TCP":{"443":{"HTTPS":true}},"Web":{"` + tsDNS + `:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:` + strconv.Itoa(port) + `"}}}},"AllowFunnel":{"` + tsDNS + `:443":true}}`
}

// fakeTS 是 fakeTailscale 写的假 tailscale 所在的临时目录。
type fakeTS struct {
	dir string
}

// funnelCalls 是假 tailscale 收到 funnel --bg 的次数（脚本每次往 calls 文件里记一行）。
func (f *fakeTS) funnelCalls() int {
	b, err := os.ReadFile(filepath.Join(f.dir, "calls"))
	if err != nil {
		return 0
	}
	return strings.Count(string(b), "funnel\n")
}

// setOn 写 serve.on：funnel --bg 成功时把它复制成 serve.json（模拟开通后的 serve 配置）。
func (f *fakeTS) setOn(t *testing.T, serve string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir, "serve.on"), []byte(serve), 0o600); err != nil {
		t.Fatal(err)
	}
}

// fakeTailscale 用临时目录里的假脚本替换 tailscaleFind 与 tailscaleRun：status、serve 的输出可改，funnel --bg 会把 serve 输出换成 on。
// 绝不跑真实的 tailscale。
func fakeTailscale(t *testing.T, status, serve string, funnelOut string, funnelCode int) *fakeTS {
	t.Helper()
	needUnixShell(t)
	dir := t.TempDir()
	f := &fakeTS{dir: dir}
	for name, s := range map[string]string{"status.json": status, "serve.json": serve, "funnel.out": funnelOut} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 与 Task 1 测试同样的脚本，funnel 成功时把 serve.on 复制成 serve.json（模拟开通后的 serve 配置）
	script := "#!/bin/sh\n" +
		"case \"$*\" in\n" +
		"  \"status --self --json\") cat \"" + dir + "/status.json\" ;;\n" +
		"  \"serve status --json\") cat \"" + dir + "/serve.json\" ;;\n" +
		"  \"funnel --bg \"*) echo funnel >> \"" + dir + "/calls\"; cat \"" + dir + "/funnel.out\"; [ -f \"" + dir + "/serve.on\" ] && cp \"" + dir + "/serve.on\" \"" + dir + "/serve.json\"; exit " + strconv.Itoa(funnelCode) + " ;;\n" +
		"  *) exit 9 ;;\nesac\n"
	p := filepath.Join(dir, "tailscale")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	oldFind, oldRun, oldBin := tailscaleFind, tailscaleRun, fakeTailscaleBin
	tailscaleFind = func() (string, error) { return p, nil }
	tailscaleRun = tailscale.Exec
	fakeTailscaleBin = p
	t.Cleanup(func() { tailscaleFind, tailscaleRun, fakeTailscaleBin = oldFind, oldRun, oldBin })
	return f
}

// fakeTailscaleBin 是 fakeTailscale 写的假 tailscale 脚本的路径（测试结束清掉，没换上时为空）：测试里的 doctor 只认、只跑它
// （见 fakeDoctorCLIs）。
var fakeTailscaleBin string

// noTailscale 让任何找 tailscale、跑 tailscale 的调用都判测试失败（用户给了 --public-url、参数不合规时不该碰它）。
func noTailscale(t *testing.T) {
	t.Helper()
	oldFind, oldRun := tailscaleFind, tailscaleRun
	tailscaleFind = func() (string, error) { t.Fatal("不该找 tailscale"); return "", nil }
	tailscaleRun = func(ctx context.Context, path string, args ...string) (string, error) {
		t.Fatalf("不该跑 tailscale %v", args)
		return "", nil
	}
	t.Cleanup(func() { tailscaleFind, tailscaleRun = oldFind, oldRun })
}

// enrollSpy 记下测试 hub 收到的 /api/enroll 请求体里的接入码：setup all --local-hub 在进程里生成、直接交给接入的那个，
// 测试拿它断言接入码没出现在任何输出里。
type enrollSpy struct {
	mu     sync.Mutex
	tokens []string
}

// seen 是到现在为止收到的接入码。
func (s *enrollSpy) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.tokens)
}

// startLocalTestHub 在 127.0.0.1:<port> 起一个真 hub（数据目录 dir），模拟“托管的 hub 已在跑”。返回的 enrollSpy 记下
// 接入请求里的接入码（请求体原样交给 hub）。
func startLocalTestHub(t *testing.T, dir string, port int) *enrollSpy {
	t.Helper()
	if err := ensureHubDir(dir); err != nil {
		t.Fatal(err)
	}
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
	spy := &enrollSpy{}
	h := srv.Handler()
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/enroll" {
			body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
			if err != nil {
				t.Errorf("读接入请求: %v", err)
			}
			var req struct {
				Token string `json:"token"`
			}
			if json.Unmarshal(body, &req) == nil && req.Token != "" {
				spy.mu.Lock()
				spy.tokens = append(spy.tokens, req.Token)
				spy.mu.Unlock()
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		h.ServeHTTP(w, r)
	})}
	go hs.Serve(l)
	t.Cleanup(func() { hs.Close(); st.Close() })
	return spy
}

// stubHubJobRunning 让 hubJobRunning 恒为 running（不问真实的 launchctl、systemctl），测试结束还原。
func stubHubJobRunning(t *testing.T, running bool) {
	t.Helper()
	old := hubJobRunning
	hubJobRunning = func(string, string) bool { return running }
	t.Cleanup(func() { hubJobRunning = old })
}

// localHubFixture 是 setupLocalHub 测试的环境：setupAllEnv 的假家目录（假 railse、记录命令的 runCommand、
// RAILSE_HOME 与 RAILSE_HUB_HOME 指临时目录）、假家目录下的 hub 数据目录、一个空闲端口（不用 8787）。
type localHubFixture struct {
	setupAllFixture
	hubDir string
	port   int
}

// localHubEnv 建 localHubFixture，并把等 hub 应答的时间设为 0（只问一次：在跑的测试 hub 一问就应答，没人应答的不白等）。
func localHubEnv(t *testing.T) localHubFixture {
	t.Helper()
	needUserAutostart(t)
	f := setupAllEnv(t)
	old := localHubWait
	localHubWait = 0
	t.Cleanup(func() { localHubWait = old })
	return localHubFixture{setupAllFixture: f, hubDir: filepath.Join(f.home, "hub"), port: freePort(t)}
}

// opts 是本环境的 localHubOpts：在终端里，stdin 是 input。
func (f localHubFixture) opts(input string, out, errOut io.Writer) localHubOpts {
	return localHubOpts{HubDir: f.hubDir, Bin: f.bin, Port: f.port, Terminal: true, Stdin: strings.NewReader(input), Stdout: out, Stderr: errOut}
}

// hubJobPath 是本系统上 hub 的托管文件（与 setup autostart 同一套路径函数）。
func (f localHubFixture) hubJobPath() string {
	if runtime.GOOS == "darwin" {
		return launchdPlistPath(f.home, "hub")
	}
	return systemdUnitPath(f.home, "hub", false)
}

// hostedHub 报告记下的命令里有没有把 hub 托管起来的那一条：launchctl bootstrap 它的 plist，或 systemctl --user restart 它的单元。
func (f localHubFixture) hostedHub() bool {
	for _, c := range installCalls(f.rec.calls) {
		if c[0] == "launchctl" && slices.Contains(c, "bootstrap") && slices.Contains(c, f.hubJobPath()) ||
			c[0] == "systemctl" && slices.Contains(c, "restart") && slices.Contains(c, "railse-hub.service") {
			return true
		}
	}
	return false
}

// openHubStore 打开本环境 hub 数据目录里的库（setupLocalHub 已关掉它自己的那个），测试结束关上。
func (f localHubFixture) openHubStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(f.hubDir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// jobHasArgs 报告托管文件里 hub run 之后是否依次带着 name value：plist 里是相邻的两个 <string>，systemd 单元里是空格隔开的两个词。
func jobHasArgs(content, name, value string) bool {
	if runtime.GOOS == "darwin" {
		return strings.Contains(content, "<string>"+xmlEscape(name)+"</string><string>"+xmlEscape(value)+"</string>")
	}
	return strings.Contains(content, systemdExecArg(name)+" "+systemdExecArg(value))
}

// assertNoSecrets：输出、返回的错误与提醒里都不能有接入码原文，也不能有网页密码原文。
func assertNoSecrets(t *testing.T, token string, err error, notes []string, outs ...string) {
	t.Helper()
	all := append([]string{strings.Join(notes, "\n")}, outs...)
	if err != nil {
		all = append(all, err.Error())
	}
	for _, o := range all {
		if token != "" && strings.Contains(o, token) {
			t.Fatalf("输出泄露了接入码: %q", o)
		}
		if strings.Contains(o, localHubTestPassword) {
			t.Fatalf("输出泄露了密码: %q", o)
		}
	}
}

func TestSetupLocalHubHappyPathExistingHub(t *testing.T) {
	f := localHubEnv(t)
	ts := fakeTailscale(t, tsStatusOK, tsServeOn(f.port), "", 0)
	startLocalTestHub(t, f.hubDir, f.port)
	stubHubJobRunning(t, true)
	// 上次 setup 留下的同一份描述（权限故意放宽，看它被按 0600 重写）：对外地址与端口都没变，托管的 hub 在跑，就不动它
	if err := os.WriteFile(localHubPath(f.hubDir), []byte(`{"port":`+strconv.Itoa(f.port)+`,"public_url":"`+tsPublicURL+`","provider":"tailscale"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	lh, tok, notes, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	if err != nil {
		t.Fatalf("%v\nstdout=%s\nstderr=%s", err, out.String(), errOut.String())
	}
	if want := (localHub{Port: f.port, PublicURL: tsPublicURL, Provider: "tailscale"}); lh != want {
		t.Fatalf("lh = %+v, want %+v", lh, want)
	}
	if len(notes) != 0 {
		t.Fatalf("地址没变不该有提醒: %q", notes)
	}
	fi, err := os.Stat(localHubPath(f.hubDir))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("local-hub.json 应在且 0600: %v %v", fi, err)
	}
	if got, ok, err := loadLocalHub(f.hubDir); err != nil || !ok || got != lh {
		t.Fatalf("local-hub.json = %+v %v %v", got, ok, err)
	}
	st := f.openHubStore(t)
	if _, ok, _ := st.GetSetting("password_hash"); !ok {
		t.Fatal("应写了 password_hash")
	}
	if tok == "" {
		t.Fatal("接入码不该为空")
	}
	if err := st.ConsumeEnrollToken(tok); err != nil {
		t.Fatalf("接入码应在库里可用: %v", err)
	}
	if !strings.Contains(out.String(), "已在跑") {
		t.Fatalf("stdout 应说 hub 已在跑: %s", out.String())
	}
	if f.hostedHub() {
		t.Fatalf("hub 在跑且设置没变，不该重新托管: %v", f.rec.calls)
	}
	if _, err := os.Stat(filepath.Join(ts.dir, "calls")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Funnel 已开，不该再调 funnel: %v", err)
	}
	assertNoSecrets(t, tok, err, notes, out.String(), errOut.String())
}

func TestSetupLocalHubEnablesFunnelWhenOff(t *testing.T) {
	f := localHubEnv(t)
	ts := fakeTailscale(t, tsStatusOK, "No serve config\n", "Available on the internet:\n\n"+tsPublicURL+"/\n|-- proxy http://127.0.0.1:"+strconv.Itoa(f.port)+"\n", 0)
	ts.setOn(t, tsServeOn(f.port))
	startLocalTestHub(t, f.hubDir, f.port)
	stubHubJobRunning(t, true)
	var out, errOut bytes.Buffer
	lh, tok, notes, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	if err != nil {
		t.Fatalf("%v\nstdout=%s\nstderr=%s", err, out.String(), errOut.String())
	}
	if strings.Contains(out.String(), "将替换") {
		t.Fatalf("原来没有 serve 配置，不该说替换: %s", out.String())
	}
	if n := ts.funnelCalls(); n != 1 {
		t.Fatalf("应调一次 funnel，调了 %d 次", n)
	}
	if lh.PublicURL != tsPublicURL || lh.Provider != "tailscale" || tok == "" {
		t.Fatalf("%+v %q", lh, tok)
	}
	if !strings.Contains(out.String(), tsPublicURL) || !strings.Contains(out.String(), "Funnel") {
		t.Fatalf("stdout 应说对外地址与 Funnel: %s", out.String())
	}
	assertNoSecrets(t, tok, err, notes, out.String(), errOut.String())
}

func TestSetupLocalHubFunnelStillOffAfterEnable(t *testing.T) {
	f := localHubEnv(t)
	ts := fakeTailscale(t, tsStatusOK, "No serve config\n", "", 0) // funnel 成功，但 serve 配置里还是没有
	var out, errOut bytes.Buffer
	_, _, _, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	if err == nil || !strings.Contains(err.Error(), "tailscale funnel status") {
		t.Fatalf("err = %v", err)
	}
	if ts.funnelCalls() != 1 {
		t.Fatalf("应调一次 funnel，调了 %d 次", ts.funnelCalls())
	}
	if _, err := os.Stat(f.hubDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("第 1 步就停，不该建 hub 数据目录: %v", err)
	}
}

func TestSetupLocalHubFunnelNotEnabledShowsLink(t *testing.T) {
	f := localHubEnv(t)
	link := "https://login.tailscale.com/f/funnel?node=nodeABC"
	fakeTailscale(t, tsStatusOK, "No serve config\n", "Funnel is not enabled on your tailnet.\nTo enable, visit:\n\n         "+link+"\n", 1)
	var out, errOut bytes.Buffer
	_, tok, _, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	var nf tailscale.ErrFunnelNotEnabled
	if !errors.As(err, &nf) || nf.EnableURL != link {
		t.Fatalf("应是 ErrFunnelNotEnabled: %v", err)
	}
	if !strings.Contains(err.Error(), link) {
		t.Fatalf("错误应含开通链接: %v", err)
	}
	if tok != "" {
		t.Fatal("失败时不该有接入码")
	}
	if _, err := os.Stat(f.hubDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("第 1 步就停，不该建 hub 数据目录、设密码、写 local-hub.json: %v", err)
	}
	if f.hostedHub() {
		t.Fatal("第 1 步就停，不该托管")
	}
}

func TestSetupLocalHubNeedsLoginAndNoCert(t *testing.T) {
	f := localHubEnv(t)
	for _, c := range []struct{ status, want string }{
		{`{"BackendState":"NeedsLogin","Self":{"DNSName":"","Online":false}}`, "登录"},
		{`{"BackendState":"Stopped","Self":{"DNSName":"` + tsDNS + `.","Online":false},"CertDomains":["` + tsDNS + `"]}`, "Stopped"},
		{`{"BackendState":"Running","Self":{"DNSName":"","Online":true}}`, "MagicDNS"},
		{`{"BackendState":"Running","Self":{"DNSName":"` + tsDNS + `.","Online":true},"CertDomains":[]}`, "HTTPS 证书"},
		{`{"BackendState":"Running","Self":{"DNSName":"` + tsDNS + `.","Online":true},"CertDomains":["other.tail2613ae.ts.net"]}`, "HTTPS 证书"},
	} {
		ts := fakeTailscale(t, c.status, tsServeOn(f.port), "", 0)
		var out, errOut bytes.Buffer
		_, _, _, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("status %s: err = %v，应含 %q", c.status, err, c.want)
		}
		if ts.funnelCalls() != 0 {
			t.Fatalf("status %s: 不该开 Funnel", c.status)
		}
		if _, err := os.Stat(f.hubDir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("status %s: 第 1 步就停，不该建 hub 数据目录", c.status)
		}
	}
}

func TestSetupLocalHubNoTailscale(t *testing.T) {
	f := localHubEnv(t)
	oldFind := tailscaleFind
	tailscaleFind = func() (string, error) { return "", tailscale.ErrNotFound }
	t.Cleanup(func() { tailscaleFind = oldFind })
	var out, errOut bytes.Buffer
	_, _, _, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	if !errors.Is(err, tailscale.ErrNotFound) || !strings.Contains(err.Error(), "https://tailscale.com/download") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(f.hubDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("没有 tailscale 时不该建 hub 数据目录")
	}
}

func TestSetupLocalHubCustomPublicURLSkipsTailscale(t *testing.T) {
	f := localHubEnv(t)
	noTailscale(t)
	startLocalTestHub(t, f.hubDir, f.port)
	stubHubJobRunning(t, true)
	o := func(out, errOut io.Writer) localHubOpts {
		o := f.opts(localHubTestPassword+"\n", out, errOut)
		o.PublicURL = "https://hub.example.com/" // 尾斜杠规范化掉
		return o
	}
	var out, errOut bytes.Buffer
	lh, tok, notes, err := setupLocalHub(runtime.GOOS, o(&out, &errOut))
	if err != nil {
		t.Fatalf("%v\nstdout=%s\nstderr=%s", err, out.String(), errOut.String())
	}
	if want := (localHub{Port: f.port, PublicURL: "https://hub.example.com", Provider: "custom"}); lh != want {
		t.Fatalf("lh = %+v, want %+v", lh, want)
	}
	if tok == "" || len(notes) != 0 {
		t.Fatalf("tok=%q notes=%q", tok, notes)
	}
	if !strings.Contains(out.String(), "对外地址用你给的 https://hub.example.com") || !strings.Contains(out.String(), "127.0.0.1:"+strconv.Itoa(f.port)) {
		t.Fatalf("stdout 应说用你给的地址、转到哪个端口: %s", out.String())
	}
	// 之前没有 local-hub.json：在跑的托管作业不是按本机 hub 的参数装的，重新托管；托管文件里是规范化后的地址
	if !f.hostedHub() {
		t.Fatalf("没有 local-hub.json 时应重新托管: %v", f.rec.calls)
	}
	job, err := os.ReadFile(f.hubJobPath())
	if err != nil || !jobHasArgs(string(job), "--public-url", "https://hub.example.com") {
		t.Fatalf("托管文件应带规范化的 --public-url: %v\n%s", err, job)
	}
	assertNoSecrets(t, tok, err, notes, out.String(), errOut.String())

	// 用同一个地址（同样带尾斜杠）重跑：不算地址变了，hub 在跑就不动它
	f.rec.calls = nil
	out.Reset()
	errOut.Reset()
	_, tok2, notes, err := setupLocalHub(runtime.GOOS, o(&out, &errOut))
	if err != nil {
		t.Fatalf("重跑: %v\n%s", err, errOut.String())
	}
	if len(notes) != 0 || strings.Contains(out.String(), "变成") || f.hostedHub() {
		t.Fatalf("同一地址重跑不该提醒、不该重新托管: notes=%q calls=%v\n%s", notes, f.rec.calls, out.String())
	}
	if !strings.Contains(out.String(), "已在跑") || tok2 == "" || tok2 == tok {
		t.Fatalf("重跑应跳过托管并生成新接入码: %q\n%s", tok2, out.String())
	}
	assertNoSecrets(t, tok2, err, notes, out.String(), errOut.String())
}

func TestSetupLocalHubRejectsBadPortAndPublicURL(t *testing.T) {
	f := localHubEnv(t)
	noTailscale(t)
	for _, port := range []int{0, -1, 65536} {
		o := f.opts(localHubTestPassword+"\n", io.Discard, io.Discard)
		o.Port = port
		if _, _, _, err := setupLocalHub(runtime.GOOS, o); err == nil || !strings.Contains(err.Error(), "端口") {
			t.Fatalf("端口 %d: err = %v", port, err)
		}
	}
	for u, why := range map[string]string{
		"http://hub.example.com":      "https://",
		"https://hub.example.com/hub": "路径",
		"https://hub.example.com?a=1": "查询",
		"hub.example.com":             "开头",
		"https://u:p@hub.example.com": "用户名或密码",
	} {
		o := f.opts(localHubTestPassword+"\n", io.Discard, io.Discard)
		o.PublicURL = u
		_, _, _, err := setupLocalHub(runtime.GOOS, o)
		if err == nil || !strings.HasPrefix(err.Error(), "对外地址") || !strings.Contains(err.Error(), why) {
			t.Fatalf("对外地址 %q: err = %v，应以“对外地址”开头、含原因 %q", u, err, why)
		}
		if strings.Contains(err.Error(), u) || strings.Contains(err.Error(), "u:p") {
			t.Fatalf("错误不该回显原文（里面可能有密码）: %v", err)
		}
	}
	if _, err := os.Stat(f.hubDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("参数不合规时不该建 hub 数据目录")
	}
}

func TestSetupLocalHubPortTakenByOther(t *testing.T) {
	f := localHubEnv(t)
	// Funnel 一开始没开：端口被别的程序占着时开了，就把那个只绑本机的服务开到公网上了
	ts := fakeTailscale(t, tsStatusOK, "No serve config\n", "", 0)
	ts.setOn(t, tsServeOn(f.port))
	stubHubJobRunning(t, false)
	// 占住端口的不是 hub：连上就关
	l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(f.port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	var out, errOut bytes.Buffer
	_, tok, _, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	if err == nil || !strings.Contains(err.Error(), "被别的程序占用") || !strings.Contains(err.Error(), "--hub-port") {
		t.Fatalf("err = %v", err)
	}
	if tok != "" || f.hostedHub() {
		t.Fatalf("端口被占不该托管、不该有接入码: %q %v", tok, f.rec.calls)
	}
	if n := ts.funnelCalls(); n != 0 {
		t.Fatalf("端口被占时不该开 Funnel，开了 %d 次", n)
	}
	if _, err := os.Stat(f.hubDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("端口被占时不该建 hub 数据目录、设密码")
	}
}

func TestSetupLocalHubManualHubRefused(t *testing.T) {
	f := localHubEnv(t)
	ts := fakeTailscale(t, tsStatusOK, "No serve config\n", "", 0)
	ts.setOn(t, tsServeOn(f.port))
	startLocalTestHub(t, f.hubDir, f.port)
	stubHubJobRunning(t, false)
	var out, errOut bytes.Buffer
	_, tok, _, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	if err == nil || !strings.Contains(err.Error(), "不是托管的 hub") {
		t.Fatalf("err = %v", err)
	}
	if tok != "" || f.hostedHub() {
		t.Fatalf("手动起的 hub 占着端口时不该托管: %q %v", tok, f.rec.calls)
	}
	if n := ts.funnelCalls(); n != 0 {
		t.Fatalf("手动起的 hub 占着端口时不该开 Funnel，开了 %d 次", n)
	}
	if _, ok, _ := f.openHubStore(t).GetSetting("password_hash"); ok {
		t.Fatal("手动起的 hub 占着端口时不该设密码")
	}
	if _, err := os.Stat(localHubPath(f.hubDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("手动起的 hub 占着端口时不该写 local-hub.json")
	}
}

func TestSetupLocalHubReinstallsWhenURLChanged(t *testing.T) {
	f := localHubEnv(t)
	fakeTailscale(t, tsStatusOK, tsServeOn(f.port), "", 0)
	startLocalTestHub(t, f.hubDir, f.port)
	stubHubJobRunning(t, true)
	old := "https://old-mac.tail2613ae.ts.net"
	if err := saveLocalHub(f.hubDir, localHub{Port: f.port, PublicURL: old, Provider: "tailscale"}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	lh, tok, notes, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	if err != nil {
		t.Fatalf("%v\nstdout=%s\nstderr=%s", err, out.String(), errOut.String())
	}
	if !f.hostedHub() {
		t.Fatalf("对外地址变了应重新托管: %v", f.rec.calls)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "重新开启通知") || !strings.Contains(notes[0], old) || !strings.Contains(notes[0], tsPublicURL) {
		t.Fatalf("notes = %q", notes)
	}
	if !strings.Contains(out.String(), "变成") {
		t.Fatalf("stdout 应提醒地址变了: %s", out.String())
	}
	if got, _, _ := loadLocalHub(f.hubDir); got != lh || lh.PublicURL != tsPublicURL {
		t.Fatalf("local-hub.json 应改成新地址: %+v %+v", got, lh)
	}
	job, _ := os.ReadFile(f.hubJobPath())
	if !jobHasArgs(string(job), "--public-url", tsPublicURL) {
		t.Fatalf("托管文件应带新地址:\n%s", job)
	}
	assertNoSecrets(t, tok, err, notes, out.String(), errOut.String())
}

func TestSetupLocalHubReinstallsWhenPortChanged(t *testing.T) {
	f := localHubEnv(t)
	fakeTailscale(t, tsStatusOK, tsServeOn(f.port), "", 0)
	startLocalTestHub(t, f.hubDir, f.port)
	stubHubJobRunning(t, true)
	if err := saveLocalHub(f.hubDir, localHub{Port: f.port + 1, PublicURL: tsPublicURL, Provider: "tailscale"}); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	_, _, notes, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	if err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if !f.hostedHub() {
		t.Fatalf("端口变了应重新托管: %v", f.rec.calls)
	}
	if len(notes) != 0 || strings.Contains(out.String(), "变成") {
		t.Fatalf("对外地址没变不该提醒手机: %q\n%s", notes, out.String())
	}
}

func TestSetupLocalHubNoPasswordNotTerminal(t *testing.T) {
	f := localHubEnv(t)
	fakeTailscale(t, tsStatusOK, tsServeOn(f.port), "", 0)
	stubHubJobRunning(t, false)
	var out, errOut bytes.Buffer
	o := f.opts("", &out, &errOut)
	o.Terminal = false
	_, tok, _, err := setupLocalHub(runtime.GOOS, o)
	if err == nil || !strings.Contains(err.Error(), "railse hub password") {
		t.Fatalf("err = %v", err)
	}
	if tok != "" {
		t.Fatal("失败时不该有接入码")
	}
	if _, err := os.Stat(localHubPath(f.hubDir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("没设上密码不该写 local-hub.json: %v", err)
	}
	if f.hostedHub() {
		t.Fatal("没设上密码不该托管")
	}
}

func TestSetupLocalHubInstallsWhenNotRunning(t *testing.T) {
	f := localHubEnv(t)
	fakeTailscale(t, tsStatusOK, tsServeOn(f.port), "", 0)
	stubHubJobRunning(t, false)
	var out, errOut bytes.Buffer
	_, tok, notes, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	if !f.hostedHub() {
		t.Fatalf("应托管并启动 hub: %v", f.rec.calls)
	}
	job, rerr := os.ReadFile(f.hubJobPath())
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, a := range [][2]string{{"--public-url", tsPublicURL}, {"--listen", "127.0.0.1:" + strconv.Itoa(f.port)}, {"--trusted-proxy", "127.0.0.1,::1"}} {
		if !jobHasArgs(string(job), a[0], a[1]) {
			t.Fatalf("托管文件应带 %s %s:\n%s", a[0], a[1], job)
		}
	}
	// 记录命令的 runCommand 没真起 hub：没人应答，报错并说日志在哪儿
	wantLog := filepath.Join(f.hubDir, "launchd.log")
	if runtime.GOOS == "linux" {
		wantLog = "journalctl --user -u railse-hub"
	}
	if err == nil || !strings.Contains(err.Error(), "没应答") || !strings.Contains(err.Error(), wantLog) {
		t.Fatalf("err = %v，应含“没应答”与 %s", err, wantLog)
	}
	if tok != "" {
		t.Fatal("hub 没应答不该有接入码")
	}
	if lh, ok, _ := loadLocalHub(f.hubDir); !ok || lh.PublicURL != tsPublicURL {
		t.Fatalf("托管成功后（等应答之前）应已写 local-hub.json: %+v %v", lh, ok)
	}
	assertNoSecrets(t, "", err, notes, out.String(), errOut.String())
}

func TestSetupLocalHubHostFailureKeepsOldFile(t *testing.T) {
	f := localHubEnv(t)
	fakeTailscale(t, tsStatusOK, tsServeOn(f.port), "", 0)
	startLocalTestHub(t, f.hubDir, f.port)
	stubHubJobRunning(t, true)
	old := localHub{Port: f.port, PublicURL: "https://old-mac.tail2613ae.ts.net", Provider: "tailscale"}
	if err := saveLocalHub(f.hubDir, old); err != nil {
		t.Fatal(err)
	}
	// 托管失败：launchctl bootstrap、systemctl restart 报错
	f.rec.respond = func(call []string) (string, error) {
		if slices.Contains(call, "bootstrap") || slices.Contains(call, "restart") {
			return "boom\n", exitErr(5)
		}
		return "", nil
	}
	var out, errOut bytes.Buffer
	_, tok, notes, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
	if err == nil || !strings.Contains(err.Error(), "托管 hub 失败") {
		t.Fatalf("err = %v", err)
	}
	if tok != "" {
		t.Fatal("托管失败不该有接入码")
	}
	// 文件留着旧值：重跑时才会判成“变了”、再托管，而不是“没变、已在跑”跳过
	if got, ok, lerr := loadLocalHub(f.hubDir); lerr != nil || !ok || got != old {
		t.Fatalf("托管失败不该把 local-hub.json 改成新值: %+v %v %v", got, ok, lerr)
	}
	assertNoSecrets(t, "", err, notes, out.String(), errOut.String())
}

func TestSetupLocalHubRewritesBadLocalHubFile(t *testing.T) {
	f := localHubEnv(t)
	fakeTailscale(t, tsStatusOK, tsServeOn(f.port), "", 0)
	startLocalTestHub(t, f.hubDir, f.port)
	stubHubJobRunning(t, true)
	want := localHub{Port: f.port, PublicURL: tsPublicURL, Provider: "tailscale"}
	for _, body := range []string{
		`{"port":` + strconv.Itoa(f.port) + `,"public_url":"https://u:p@old-mac.tail2613ae.ts.net"}`, // 字段不合规，地址里还带着密码
		`{not json`,
	} {
		if err := os.WriteFile(localHubPath(f.hubDir), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		f.rec.calls = nil
		var out, errOut bytes.Buffer
		lh, tok, notes, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut))
		if err != nil {
			t.Fatalf("%s: %v\n%s", body, err, errOut.String())
		}
		if !strings.Contains(errOut.String(), "警告") || !strings.Contains(errOut.String(), localHubFile) || !strings.Contains(errOut.String(), "重写") {
			t.Fatalf("%s: 应警告坏文件并说要重写: %s", body, errOut.String())
		}
		if strings.Contains(errOut.String(), "u:p") {
			t.Fatalf("警告不该回显文件里的地址原文: %s", errOut.String())
		}
		if !f.hostedHub() || len(notes) != 0 {
			t.Fatalf("%s: 坏文件当作变了、重新托管，但不提醒手机: %v %q", body, f.rec.calls, notes)
		}
		if got, ok, lerr := loadLocalHub(f.hubDir); lerr != nil || !ok || got != want || lh != want {
			t.Fatalf("%s: 应按这次的设置重写: %+v %v %v", body, got, ok, lerr)
		}
		assertNoSecrets(t, tok, err, notes, out.String(), errOut.String())
	}
}

func TestSetupLocalHubRefusesUnsupportedOS(t *testing.T) {
	f := localHubEnv(t)
	noTailscale(t)
	_, _, _, err := setupLocalHub("windows", f.opts(localHubTestPassword+"\n", io.Discard, io.Discard))
	if err == nil || !strings.Contains(err.Error(), "macOS") || !strings.Contains(err.Error(), "Linux") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(f.hubDir); !errors.Is(err, os.ErrNotExist) || f.hostedHub() {
		t.Fatal("不支持的系统上什么都不该做")
	}
}

func TestSetupLocalHubWarnsBeforeReplacingServe(t *testing.T) {
	f := localHubEnv(t)
	startLocalTestHub(t, f.hubDir, f.port)
	stubHubJobRunning(t, true)
	// 443 的 / 原来指向本机别的服务：开 Funnel 之前说要替换它
	other := `{"TCP":{"443":{"HTTPS":true}},"Web":{"` + tsDNS + `:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"}}}}}`
	ts := fakeTailscale(t, tsStatusOK, other, "", 0)
	ts.setOn(t, tsServeOn(f.port))
	var out, errOut bytes.Buffer
	if _, _, _, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut)); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	i, j := strings.Index(out.String(), "将替换"), strings.Index(out.String(), "开 Tailscale Funnel")
	if i < 0 || j < 0 || i > j || !strings.Contains(out.String(), "http://127.0.0.1:3000") || ts.funnelCalls() != 1 {
		t.Fatalf("开 Funnel 之前应说要替换原来指向 127.0.0.1:3000 的配置:\n%s", out.String())
	}
	// 已经指向本机 hub、只是没开 Funnel（tailscale serve）：开 Funnel 不算替换
	serveOnly := `{"TCP":{"443":{"HTTPS":true}},"Web":{"` + tsDNS + `:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:` + strconv.Itoa(f.port) + `"}}}}}`
	ts = fakeTailscale(t, tsStatusOK, serveOnly, "", 0)
	ts.setOn(t, tsServeOn(f.port))
	out.Reset()
	if _, _, _, err := setupLocalHub(runtime.GOOS, f.opts(localHubTestPassword+"\n", &out, &errOut)); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	if strings.Contains(out.String(), "将替换") || ts.funnelCalls() != 1 {
		t.Fatalf("指向的就是本机 hub，不该说替换:\n%s", out.String())
	}
}
