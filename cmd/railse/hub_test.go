package main

import (
	"bytes"
	"context"
	"encoding/json"
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

	"github.com/railse/railse-opc/internal/hub"
	"github.com/railse/railse-opc/internal/hub/store"
)

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
	return &testHub{srv: srv, st: st, http: hs}
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
	for _, ttl := range []string{"0s", "-1m"} {
		var out, errOut bytes.Buffer
		if code := runHub([]string{"token", "--dir", dir, "--ttl", ttl}, nil, &out, &errOut); code != 2 || out.String() != "" {
			t.Fatalf("--ttl %s 应拒绝: %d %q %q", ttl, code, out.String(), errOut.String())
		}
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
	for _, bad := range []string{"0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96", "::ffff:0:0/80", "nonsense", "10.0.0.0/33", "fe80::1%en0", "10.0.0.1,", ""} {
		var q proxyFlag
		if err := q.Set(bad); err == nil {
			t.Fatalf("%q 应拒绝", bad)
		}
		var out, errOut bytes.Buffer
		if code := runHub([]string{"token", "--dir", tempDir(t), "--trusted-proxy", bad}, nil, &out, &errOut); code != 2 || out.String() != "" {
			t.Fatalf("%q 应退出 2: %d %q", bad, code, errOut.String())
		}
	}
}
