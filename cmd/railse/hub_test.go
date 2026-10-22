package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hub"
	"github.com/railse/railse-opc/internal/hub/store"
)

// cancelledHubRun 让 hub run 的运行期一开始就已取消：即使误把它跑起来，也会立刻关停而不是挂住测试。
func cancelledHubRun(t *testing.T) {
	t.Helper()
	old := hubRunContext
	hubRunContext = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}
	t.Cleanup(func() { hubRunContext = old })
}

// cancelledAgentRun 同上，给 agent run。
func cancelledAgentRun(t *testing.T) {
	t.Helper()
	old := agentRunContext
	agentRunContext = func() (context.Context, context.CancelFunc) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx, cancel
	}
	t.Cleanup(func() { agentRunContext = old })
}

// lockedBuf 是并发安全的日志缓冲。
type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// tempDir 建临时目录，测试结束时删除。不用 t.TempDir()：路径太长会超出 Unix socket 的长度上限。
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

type testHub struct {
	srv  *hub.Server
	st   *store.Store
	http *httptest.Server
	logs *lockedBuf // hub 的日志
}

// startTestHub 起一个进程内 hub，供 enroll 与端到端测试用。hub 的日志只在测试失败时打印。
func startTestHub(t *testing.T) *testHub {
	t.Helper()
	dir := tempDir(t)
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	logs := &lockedBuf{}
	srv, err := hub.New(hub.DefaultConfig(dir), st, log.New(logs, "hub ", 0))
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		hs.Close()
		st.Close()
		if t.Failed() {
			t.Logf("hub 日志:\n%s", logs.String())
		}
	})
	return &testHub{srv: srv, st: st, http: hs, logs: logs}
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("等待超时")
}

func TestHubPasswordTokenAndWebhookToken(t *testing.T) {
	dir := filepath.Join(tempDir(t), "hub")
	var out, errOut bytes.Buffer
	if code := runHub([]string{"password", "--dir", dir}, strings.NewReader("short\n"), &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "至少 8 个字符") {
		t.Fatalf("短密码应失败并说明: %d %q", code, errOut.String())
	}
	if st, err := os.Stat(dir); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("数据目录应为 0700: %v %v", st, err)
	}
	errOut.Reset()
	if code := runHub([]string{"password", "--dir", dir}, strings.NewReader("hunter22\r\n"), &out, &errOut); code != 0 || !strings.Contains(out.String(), "密码已设置") {
		t.Fatalf("password: %d %q %q", code, out.String(), errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), "hunter22") {
		t.Fatal("输出里不能有密码")
	}
	out.Reset()
	if code := runHub([]string{"password", "--dir", dir}, strings.NewReader(""), &out, &errOut); code == 0 {
		t.Fatal("没读到密码应失败")
	}
	out.Reset()
	if code := runHub([]string{"token", "--dir", dir, "--ttl", "1m"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("token: %d %s", code, errOut.String())
	}
	tok := fieldAfter(t, out.String(), "接入码: ")
	if !strings.Contains(out.String(), "railse agent enroll --hub") || !strings.Contains(out.String(), "有效期: 1m0s") {
		t.Fatalf("应提示有效期与下一步命令: %q", out.String())
	}
	st, err := store.Open(filepath.Join(dir, "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// 密码只存 argon2id 编码串
	if enc, ok, _ := st.GetSetting("password_hash"); !ok || !strings.HasPrefix(enc, "argon2id$") || !hub.VerifyPassword(enc, "hunter22") {
		t.Fatalf("password_hash = %q", enc)
	}
	if err := st.ConsumeEnrollToken(tok); err != nil {
		t.Fatalf("打印的接入码应有效: %v", err)
	}
	out.Reset()
	if code := runHub([]string{"webhook-token", "--dir", dir}, nil, &out, &errOut); code != 0 {
		t.Fatalf("webhook-token: %d %s", code, errOut.String())
	}
	wtok := fieldAfter(t, out.String(), "令牌: ")
	if got, ok, _ := st.GetSetting("webhook_token_hash"); !ok || got != store.HashToken(wtok) {
		t.Fatal("应只存令牌哈希")
	}
	if fi, _ := os.Stat(filepath.Join(dir, "hub.db")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("hub.db 应为 0600: %v", fi.Mode().Perm())
	}
	// 再生成一次换掉旧令牌
	out.Reset()
	runHub([]string{"webhook-token", "--dir", dir}, nil, &out, &errOut)
	if wtok2 := fieldAfter(t, out.String(), "令牌: "); wtok2 == wtok {
		t.Fatal("每次应生成新令牌")
	}
}

// fieldAfter 取输出里某个前缀之后到行尾的内容。
func fieldAfter(t *testing.T, text, prefix string) string {
	t.Helper()
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("输出里没有 %q:\n%s", prefix, text)
	return ""
}

func TestHubTokenRejectsBadTTL(t *testing.T) {
	dir := filepath.Join(tempDir(t), "hub")
	for _, ttl := range []string{"0s", "-1m", "24h1s", "48h"} {
		var out, errOut bytes.Buffer
		if code := runHub([]string{"token", "--dir", dir, "--ttl", ttl}, nil, &out, &errOut); code != 2 || out.String() != "" {
			t.Fatalf("--ttl %s 应拒绝: %d %q %q", ttl, code, out.String(), errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if code := runHub([]string{"token", "--dir", dir, "--ttl", "24h"}, nil, &out, &errOut); code != 0 || !strings.Contains(out.String(), "有效期: 24h0m0s") {
		t.Fatalf("--ttl 24h 应接受: %d %q %q", code, out.String(), errOut.String())
	}
}

func TestHubRunServesAndStops(t *testing.T) {
	dir := filepath.Join(tempDir(t), "hub")
	var out, errOut bytes.Buffer
	if code := runHub([]string{"password", "--dir", dir}, strings.NewReader("hunter22\n"), &out, &errOut); code != 0 {
		t.Fatal(errOut.String())
	}
	addr := freeAddr(t)
	cfg := hub.DefaultConfig(dir)
	cfg.Listen = addr
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runHubServe(ctx, cfg, false, os.Stderr) }()
	waitUntil(t, func() bool {
		resp, err := http.Get("http://" + addr + "/api/status")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized
	})
	body, _ := json.Marshal(map[string]string{"password": "hunter22"})
	resp, err := http.Post("http://"+addr+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login: %v %v", err, resp)
	}
	resp.Body.Close()
	req, _ := http.NewRequest("GET", "http://"+addr+"/api/status", nil)
	req.AddCookie(resp.Cookies()[0])
	resp2, err := http.DefaultClient.Do(req)
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("status: %v %v", err, resp2)
	}
	var st map[string]any
	json.NewDecoder(resp2.Body).Decode(&st)
	resp2.Body.Close()
	if st["paused"] != false {
		t.Fatalf("status = %v", st)
	}
	if fi, err := os.Stat(filepath.Join(dir, "hub.log")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("hub.log: %v %v", fi, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("退出应无错: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hub 没有在取消后 5 秒内停下")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "hub.log"))
	if strings.Contains(string(b), "hunter22") {
		t.Fatal("日志里不能有密码")
	}
}

// 已有的 hub.log 收紧为 0600；数据目录不存在时 runHubServe 自己建成 0700。
func TestHubRunCreatesPrivateDirAndLog(t *testing.T) {
	dir := filepath.Join(tempDir(t), "hub")
	cfg := hub.DefaultConfig(dir)
	cfg.Listen = "127.0.0.1:0"
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已取消：建好目录、日志与数据库后立即关停
	if err := runHubServe(ctx, cfg, false, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("数据目录: %v %v", fi, err)
	}
	logPath := filepath.Join(dir, "hub.log")
	os.Chmod(logPath, 0o644)
	if err := runHubServe(ctx, cfg, false, os.Stderr); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(logPath); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("已有的 hub.log 应收紧为 0600: %v %v", fi, err)
	}
	b, _ := os.ReadFile(logPath)
	if !strings.Contains(string(b), "railse hub password") {
		t.Fatalf("没有密码时日志应提示设置: %s", b)
	}
}

func TestHubUsageAndUnknown(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runHub(nil, nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "用法") {
		t.Fatalf("无参数: %d %q", code, errOut.String())
	}
	if code := run([]string{"hub", "bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("未知子命令: %d", code)
	}
	errOut.Reset()
	if code := runHub([]string{"token", "--dir", tempDir(t), "extra"}, nil, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "extra") {
		t.Fatalf("多余的参数应退出 2: %d %q", code, errOut.String())
	}
	out.Reset()
	errOut.Reset()
	run(nil, &out, &errOut)
	if !strings.Contains(errOut.String(), "hub") || !strings.Contains(errOut.String(), "enroll") {
		t.Fatalf("总用法应列出 hub 与 agent enroll: %q", errOut.String())
	}
}

// 决定 4：--trusted-proxy 可重复、可逗号分隔，接受 CIDR 与单个 IP；拒绝解析失败与前缀长度为 0（含 IPv4-mapped 的 /96 及更短）。
func TestHubTrustedProxyFlag(t *testing.T) {
	var p proxyFlag
	for _, v := range []string{"10.0.0.0/8, 192.168.1.10", " ::1 ", "2001:db8::/32"} {
		if err := p.Set(v); err != nil {
			t.Fatalf("%q: %v", v, err)
		}
	}
	want := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.168.1.10/32"), netip.MustParsePrefix("::1/128"), netip.MustParsePrefix("2001:db8::/32")}
	if !reflect.DeepEqual([]netip.Prefix(p), want) {
		t.Fatalf("解析结果 %v", p)
	}
	if p.String() == "" {
		t.Fatal("String 不应为空")
	}
	cancelledHubRun(t)
	for _, bad := range []string{"0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96", "::ffff:0:0/80", "nonsense", "10.0.0.0/33", "fe80::1%en0", "10.0.0.1,", ""} {
		var q proxyFlag
		if err := q.Set(bad); err == nil {
			t.Fatalf("%q 应拒绝", bad)
		}
		var out, errOut bytes.Buffer
		if code := runHub([]string{"run", "--dir", tempDir(t), "--listen", "127.0.0.1:0", "--trusted-proxy", bad}, nil, &out, &errOut); code != 2 || out.String() != "" {
			t.Fatalf("%q 应退出 2: %d %q", bad, code, errOut.String())
		}
	}
}

// 修复轮 1：子命令共用一个标志集，出现当前子命令不认的标志时退出 2 并点名，什么都不做。
func TestCommandsRejectForeignFlags(t *testing.T) {
	cancelledHubRun(t)
	cancelledAgentRun(t)
	for _, c := range []struct {
		args []string
		flag string
	}{
		{[]string{"agent", "run", "--hub", "http://127.0.0.1:1"}, "--hub"},
		{[]string{"agent", "status", "--force"}, "--force"},
		{[]string{"agent", "pause", "--token", "t"}, "--token"},
		{[]string{"agent", "reload", "--name", "n"}, "--name"},
		{[]string{"agent", "enroll", "--hub", "http://127.0.0.1:1", "--token", "t", "--foreground"}, "--foreground"},
		{[]string{"hub", "token", "--trusted-proxy", "10.0.0.1"}, "--trusted-proxy"},
		{[]string{"hub", "password", "--ttl", "1m"}, "--ttl"},
		{[]string{"hub", "webhook-token", "--listen", "127.0.0.1:1"}, "--listen"},
		{[]string{"hub", "token", "--secure-cookies"}, "--secure-cookies"},
		{[]string{"hub", "run", "--ttl", "1m"}, "--ttl"},
		{[]string{"hub", "password", "--foreground"}, "--foreground"},
	} {
		dir := filepath.Join(tempDir(t), "d")
		var out, errOut bytes.Buffer
		code := run(append(c.args, "--dir", dir), &out, &errOut)
		if code != 2 || !strings.Contains(errOut.String(), c.flag) || out.String() != "" {
			t.Fatalf("%v: 应退出 2 并点名 %s: %d %q %q", c.args, c.flag, code, out.String(), errOut.String())
		}
		if _, err := os.Stat(dir); err == nil {
			t.Fatalf("%v: 被拒时不应建目录", c.args)
		}
	}
}

// 修复轮 1：hub run 的 --trusted-proxy 确实进了 hub 的配置——日志可见，经可信代理来的请求按 X-Forwarded-For 分别限速；
// 有守护进程连着时取消 hub run，5 秒内退出 0，守护进程照常运行。
func TestHubRunCommand(t *testing.T) {
	dir := filepath.Join(tempDir(t), "hub")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := hubRunContext
	hubRunContext = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { hubRunContext = old })
	addr := freeAddr(t)
	base := "http://" + addr
	var out, errOut lockedBuf
	done, exited := make(chan int, 1), make(chan struct{})
	go func() {
		defer close(exited)
		done <- runHub([]string{"run", "--dir", dir, "--listen", addr, "--trusted-proxy", "10.0.0.0/8", "--trusted-proxy", "127.0.0.1"}, nil, &out, &errOut)
	}()
	// 中途失败时也要停下 hub 并等它退出，再删数据目录（后登记先执行）
	t.Cleanup(func() {
		cancel()
		<-exited
	})
	waitUntil(t, func() bool {
		resp, err := http.Get(base + "/api/status")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusUnauthorized
	})
	if b, _ := os.ReadFile(filepath.Join(dir, "hub.log")); !strings.Contains(string(b), "可信代理: 10.0.0.0/8,127.0.0.1/32") {
		t.Fatalf("日志应列出可信代理:\n%s", b)
	}
	enroll := func(xff string) int {
		t.Helper()
		req, _ := http.NewRequest("POST", base+"/api/enroll", strings.NewReader(`{"token":"bad","name":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Forwarded-For", xff)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for i := 0; i < 5; i++ {
		if c := enroll("203.0.113.7"); c != http.StatusUnauthorized {
			t.Fatalf("第 %d 次: %d", i+1, c)
		}
	}
	if c := enroll("203.0.113.7"); c != http.StatusTooManyRequests {
		t.Fatalf("同一来源第 6 次应限速: %d", c)
	}
	if c := enroll("203.0.113.8"); c != http.StatusUnauthorized {
		t.Fatalf("可信代理后面的另一个来源不应受限: %d", c)
	}
	// 接一台守护进程上来
	var tokOut, tokErr bytes.Buffer
	if code := runHub([]string{"token", "--dir", dir}, nil, &tokOut, &tokErr); code != 0 {
		t.Fatalf("token: %d %s", code, tokErr.String())
	}
	cfgDir := filepath.Join(tempDir(t), "h")
	var eOut, eErr bytes.Buffer
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", base, "--token", fieldAfter(t, tokOut.String(), "接入码: ")}, &eOut, &eErr); code != 0 {
		t.Fatalf("enroll: %d %s", code, eErr.String())
	}
	cfg := agent.ConfigIn(cfgDir)
	serveAgent(t, cfg)
	waitUntil(t, func() bool {
		b, _ := os.ReadFile(cfg.LogPath)
		return strings.Contains(string(b), "已连接 hub")
	})
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("hub run 应退出 0: %d %q", code, errOut.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("有守护进程连着时，hub run 没有在取消后 5 秒内退出")
	}
	var sOut, sErr bytes.Buffer
	if code := run([]string{"agent", "status", "--dir", cfgDir}, &sOut, &sErr); code != 0 {
		t.Fatalf("hub 停了守护进程应照常服务: %d %q", code, sErr.String())
	}
}

// 修复轮 1：数据目录先看 $RAILSE_HUB_HOME，否则 ~/.railse-hub。HOME 指向临时目录，绝不碰真实家目录。
func TestDefaultHubDir(t *testing.T) {
	home := tempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("RAILSE_HUB_HOME", "")
	if d, err := defaultHubDir(); err != nil || d != filepath.Join(home, ".railse-hub") {
		t.Fatalf("没有 $RAILSE_HUB_HOME 时应为 ~/.railse-hub: %q %v", d, err)
	}
	custom := filepath.Join(tempDir(t), "custom")
	t.Setenv("RAILSE_HUB_HOME", custom)
	if d, err := defaultHubDir(); err != nil || d != custom {
		t.Fatalf("应取 $RAILSE_HUB_HOME: %q %v", d, err)
	}
	var out, errOut bytes.Buffer
	if code := runHub([]string{"token"}, nil, &out, &errOut); code != 0 {
		t.Fatalf("token: %d %s", code, errOut.String())
	}
	if fi, err := os.Stat(custom); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("数据目录应建在 $RAILSE_HUB_HOME 且为 0700: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(custom, "hub.db")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".railse-hub")); err == nil {
		t.Fatal("有 $RAILSE_HUB_HOME 时不应用家目录")
	}
}

// 修复轮 1、收尾修复 a：serveHub 返回任何错误（关停超时，或 HTTP 服务自己出错结束）时 runHubServe 不关库（还在跑的处理
// 可能碰存储，进程随即退出）、记日志并把错误交上去；正常结束（nil）时才关库。
func TestHubRunServeShutdownTimeout(t *testing.T) {
	oldServe, oldOpen := serveHub, openHubStore
	t.Cleanup(func() { serveHub, openHubStore = oldServe, oldOpen })
	for _, c := range []struct {
		name string
		err  error
		logs []string // 日志应含
		not  string   // 日志不应含
	}{
		{"关停超时", fmt.Errorf("收尾: %w", hub.ErrShutdownTimeout), []string{"关停超时", "不关数据库"}, "hub 已停止"},
		{"服务出错", errors.New("accept: 监听器坏了"), []string{"监听器坏了", "不关数据库"}, "hub 已停止"},
		{"正常结束", nil, []string{"hub 已停止"}, "不关数据库"},
	} {
		var opened *store.Store
		openHubStore = func(path string) (*store.Store, error) {
			st, err := store.Open(path)
			if err == nil {
				opened = st
				t.Cleanup(func() { st.Close() }) // 没被关的库由测试收尾（Close 可重复调用）
			}
			return st, err
		}
		serveHub = func(context.Context, *hub.Server) error { return c.err }
		dir := filepath.Join(tempDir(t), "hub")
		err := runHubServe(context.Background(), hub.DefaultConfig(dir), false, os.Stderr)
		if !errors.Is(err, c.err) || (c.err == nil) != (err == nil) {
			t.Fatalf("%s: 应原样交上 serveHub 的结果: %v", c.name, err)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "hub.log"))
		for _, want := range c.logs {
			if !strings.Contains(string(b), want) {
				t.Fatalf("%s: 日志应含 %q:\n%s", c.name, want, b)
			}
		}
		if strings.Contains(string(b), c.not) {
			t.Fatalf("%s: 日志不应含 %q:\n%s", c.name, c.not, b)
		}
		// 库是否还开着：开着时能写，关了写会报错
		if opened == nil {
			t.Fatalf("%s: 没有打开库", c.name)
		}
		werr := opened.SetSetting("probe", "1")
		if c.err != nil && werr != nil {
			t.Fatalf("%s: 出错返回时不应关库: %v", c.name, werr)
		}
		if c.err == nil && werr == nil {
			t.Fatalf("%s: 正常结束应关库", c.name)
		}
	}
}
