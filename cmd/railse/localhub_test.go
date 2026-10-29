package main

import (
	"bytes"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/hub"
	"github.com/xufei547/railse-opc/internal/hub/store"
)

// freePort 取一个当下没人监听的本机端口。
func freePort(t *testing.T) int {
	t.Helper()
	_, p, _ := net.SplitHostPort(freeAddr(t))
	n, _ := strconv.Atoi(p)
	return n
}

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
}

func TestLoadLocalHubValidation(t *testing.T) {
	bad := map[string]string{
		"坏 JSON":       `{not json`,
		"端口 0":         `{"port":0,"public_url":"https://x.ts.net"}`,
		"端口 65536":     `{"port":65536,"public_url":"https://x.ts.net"}`,
		"端口负数":         `{"port":-1,"public_url":"https://x.ts.net"}`,
		"http 源":       `{"port":8787,"public_url":"http://x.ts.net"}`,
		"带路径":          `{"port":8787,"public_url":"https://x.ts.net/a"}`,
		"带查询串":         `{"port":8787,"public_url":"https://x.ts.net?a=1"}`,
		"public_url 空": `{"port":8787}`,
		"带用户名密码":       `{"port":8787,"public_url":"https://u:p@x.ts.net"}`,
	}
	for name, body := range bad {
		dir := tempDir(t)
		os.WriteFile(localHubPath(dir), []byte(body), 0o600)
		_, ok, err := loadLocalHub(dir)
		if err == nil || ok {
			t.Errorf("%s: 应报错: %v %v", name, ok, err)
		} else if !strings.Contains(err.Error(), localHubFile) {
			t.Errorf("%s: 错误应带文件路径: %v", name, err)
		} else if strings.Contains(err.Error(), "u:p") {
			t.Errorf("%s: 错误不该回显地址原文（里面可能有密码）: %v", name, err)
		}
	}
	// Provider 空读出 custom；尾斜杠被规范化
	dir := tempDir(t)
	os.WriteFile(localHubPath(dir), []byte(`{"port":8787,"public_url":"https://x.ts.net/"}`), 0o600)
	got, ok, err := loadLocalHub(dir)
	want := localHub{Port: 8787, PublicURL: "https://x.ts.net", Provider: "custom"}
	if err != nil || !ok || got != want {
		t.Fatalf("%+v %v %v", got, ok, err)
	}
	// 保存前也规范化，不合规的不写
	dir2 := tempDir(t)
	if err := saveLocalHub(dir2, localHub{Port: 8787, PublicURL: "https://x.ts.net/", Provider: "tailscale"}); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := loadLocalHub(dir2); got.PublicURL != "https://x.ts.net" {
		t.Fatalf("%+v", got)
	}
	raw, _ := os.ReadFile(localHubPath(dir2))
	if strings.Contains(string(raw), "ts.net/\"") {
		t.Fatalf("文件里不该有尾斜杠: %s", raw)
	}
	if err := saveLocalHub(tempDir(t), localHub{Port: 0, PublicURL: "https://x.ts.net"}); err == nil {
		t.Fatal("不合规不该保存")
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
	// 有 version 却没有协议号（或协议号不是正数）的，不是 railse hub
	for _, body := range []string{`{"version":"1.0"}`, `{"version":"1.0","agent_proto":0}`, `{"version":"1.0","agent_proto":"1"}`} {
		fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
		_, pf, _ := net.SplitHostPort(strings.TrimPrefix(fake.URL, "http://"))
		portF, _ := strconv.Atoi(pf)
		if _, ok := hubAnswers(portF); ok {
			fake.Close()
			t.Fatalf("%s 不算 hub 的应答", body)
		}
		fake.Close()
	}
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, h.http.URL+"/api/version", http.StatusFound)
	}))
	defer redir.Close()
	_, p3, _ := net.SplitHostPort(strings.TrimPrefix(redir.URL, "http://"))
	port3, _ := strconv.Atoi(p3)
	if _, ok := hubAnswers(port3); ok {
		t.Fatal("302 到真 hub 也不算应答")
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
	if _, ok, _ := st.GetSetting("password_hash"); ok {
		t.Fatal("非终端不该写密码")
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
	for _, o := range []string{out.String(), errOut.String()} {
		if strings.Contains(o, "correct horse battery") || strings.Contains(o, "should not be read") {
			t.Fatalf("输出泄露了密码: %q", o)
		}
	}
	// 在终端里（这里用同是字符设备的 /dev/null）：setup 打自己的说明（屏幕会回显、不想回显怎么先设好），不打 railse hub password 那句
	// “read -s 读进变量再通过管道传入”（setup 不在终端里就不问，管道传不进来）
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	dir3 := tempDir(t)
	st3, _ := store.Open(filepath.Join(dir3, "hub.db"))
	defer st3.Close()
	out.Reset()
	errOut.Reset()
	if _, err := ensureHubPassword(hub.DefaultConfig(dir3), st3, null, true, &out, &errOut); err == nil {
		t.Fatal("没读到密码应报错")
	}
	if all := out.String() + errOut.String(); strings.Contains(all, "通过管道传入") || !strings.Contains(out.String(), "屏幕会回显") ||
		!strings.Contains(out.String(), "read -s PW") || !strings.Contains(out.String(), "railse hub password --dir "+dir3) {
		t.Fatalf("setup 应打自己的说明: stdout=%q stderr=%q", out.String(), errOut.String())
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
	fake := func(out string, err error) func(string, ...string) (string, error) {
		return func(string, ...string) (string, error) { return out, err }
	}
	if n := proxyNote("darwin", env(nil), fake("HTTPSEnable : 0\n", nil), "https://x.ts.net"); n != "" {
		t.Fatalf("HTTPSEnable 为 0 不该提醒: %q", n)
	}
	if n := proxyNote("darwin", env(nil), fake("HTTPEnable : 1\n", errors.New("boom")), "https://x.ts.net"); n != "" {
		t.Fatalf("scutil 报错当没开: %q", n)
	}
	if n := proxyNote("darwin", env(nil), fake("SOCKSEnable : 1\n", nil), "https://x.ts.net"); n == "" {
		t.Fatal("只有 SOCKS 也该提醒")
	}
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if n := proxyNote("linux", env(map[string]string{k: "socks5://127.0.0.1:7897"}), noRun, "https://x.ts.net"); n == "" {
			t.Fatalf("%s 设了也该提醒", k)
		}
	}
}

func TestWaitHub(t *testing.T) {
	old := localHubWait
	localHubWait = 0
	defer func() { localHubWait = old }()
	if _, ok := waitHub(freePort(t), localHubWait); ok {
		t.Fatal("没人监听不该应答")
	}
	h := startTestHub(t)
	_, portStr, _ := net.SplitHostPort(strings.TrimPrefix(h.http.URL, "http://"))
	port, _ := strconv.Atoi(portStr)
	if v, ok := waitHub(port, localHubWait); !ok || v == "" {
		t.Fatalf("真 hub 应应答: %q %v", v, ok)
	}
}

func TestWaitHubRetriesUntilHubStarts(t *testing.T) {
	port := freePort(t)
	var mu sync.Mutex
	var srv *http.Server
	started := make(chan struct{})
	go func() {
		defer close(started)
		time.Sleep(300 * time.Millisecond)
		l, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err != nil {
			t.Errorf("起假 hub: %v", err)
			return
		}
		s := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"version":"x","agent_proto":1}`))
		})}
		mu.Lock()
		srv = s
		mu.Unlock()
		go s.Serve(l)
	}()
	t.Cleanup(func() { // 等起假 hub 的 goroutine 做完再关它：测试结束之后不留着监听
		<-started
		mu.Lock()
		defer mu.Unlock()
		if srv != nil {
			srv.Close()
		}
	})
	if v, ok := waitHub(port, 3*time.Second); !ok || v != "x" {
		t.Fatalf("应在 hub 起来后应答: %q %v", v, ok)
	}
}
