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
