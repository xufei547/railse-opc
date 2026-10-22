package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hub"
	"github.com/railse/railse-opc/internal/proto"
)

func TestAgentServeAndControl(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr) }()
	sock := filepath.Join(cfgDir, "agent.sock")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket 未出现")
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, f := range []string{"rules.yaml", "audit.jsonl", "agent.log"} {
		if _, err := os.Stat(filepath.Join(cfgDir, f)); err != nil {
			t.Fatalf("%s 应被创建: %v", f, err)
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "paused: false") || !strings.Contains(out.String(), "rules: 2") {
		t.Fatalf("status: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	if code := run([]string{"agent", "pause", "--dir", cfgDir}, &out, &errOut); code != 0 {
		t.Fatalf("pause: %d %s", code, errOut.String())
	}
	out.Reset()
	run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut)
	if !strings.Contains(out.String(), "paused: true (local)\n") {
		t.Fatalf("status after pause: %q", out.String())
	}
	run([]string{"agent", "resume", "--dir", cfgDir}, &out, &errOut)
	os.WriteFile(filepath.Join(cfgDir, "rules.yaml"), []byte("version: 1\nrules: []\n"), 0o600)
	out.Reset()
	if code := run([]string{"agent", "reload", "--dir", cfgDir}, &out, &errOut); code != 0 {
		t.Fatalf("reload: %d %s", code, errOut.String())
	}
	out.Reset()
	run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut)
	if !strings.Contains(out.String(), "rules: 0") {
		t.Fatalf("reload 后 rules 应为 0: %q", out.String())
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve 退出出错: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve 没有在 ctx 取消后退出")
	}
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("退出后应删除 socket 文件")
	}
}

func TestAgentControlWithoutDaemon(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "status", "--dir", filepath.Join(dir, "none")}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "连接守护进程") {
		t.Fatalf("没有守护进程时应退出 1 并说明: %d %q", code, errOut.String())
	}
	if code := run([]string{"agent"}, &out, &errOut); code != 2 {
		t.Fatalf("缺子命令应退出 2, 得到 %d", code)
	}
}

func TestAgentRefusesSecondInstance(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr)
	sock := filepath.Join(cfgDir, "agent.sock")
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := runAgentServe(context.Background(), agent.ConfigIn(cfgDir), false, os.Stderr); err == nil || !strings.Contains(err.Error(), "已有守护进程") {
		t.Fatalf("第二个实例应拒绝启动: %v", err)
	}
}

func TestAgentRefusesEmptyHomeDir(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfg := agent.ConfigIn(dir)
	cfg.HomeDir = ""
	errc := make(chan error, 1)
	go func() { errc <- runAgentServe(context.Background(), cfg, false, os.Stderr) }()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "家目录") {
			t.Fatalf("家目录为空时应拒绝启动并说明: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("家目录为空时应立即返回")
	}
	if _, err := os.Stat(cfg.SocketPath); err == nil {
		t.Fatal("家目录为空时不应创建 socket")
	}
}

func TestAgentLogIsPrivate(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	os.MkdirAll(cfgDir, 0o700)
	logPath := filepath.Join(cfgDir, "agent.log")
	os.WriteFile(logPath, nil, 0o644)
	os.Chmod(logPath, 0o644) // 不受 umask 影响，确保起点是 0644
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr) }()
	sock := filepath.Join(cfgDir, "agent.sock")
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("socket 未出现")
		}
	}
	st, err := os.Stat(logPath)
	cancel()
	if serr := <-done; serr != nil {
		t.Fatalf("serve 退出出错: %v", serr)
	}
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("已有的 agent.log 应收紧为 0600: %v %v", st.Mode().Perm(), err)
	}
}

func TestPausedLineShowsSource(t *testing.T) {
	for _, c := range []struct {
		st   proto.Status
		want string
	}{
		{proto.Status{}, "paused: false"},
		{proto.Status{Paused: true, LocalPaused: true}, "paused: true (local)"},
		{proto.Status{Paused: true, HubPaused: true}, "paused: true (hub)"},
		{proto.Status{Paused: true, LocalPaused: true, HubPaused: true}, "paused: true (local+hub)"},
		{proto.Status{Paused: true}, "paused: true"}, // 旧版守护进程不报来源
	} {
		if got := pausedLine(&c.st); got != c.want {
			t.Fatalf("%+v: got %q want %q", c.st, got, c.want)
		}
	}
}

func TestAgentResumeWarnsWhenHubPaused(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	defer os.RemoveAll(dir)
	cfgDir := filepath.Join(dir, "h")
	os.MkdirAll(cfgDir, 0o700)
	// 上次运行时 hub 暂停了总开关；本机仍接入着 hub，只是这次还没连上（连不上），hub 暂停保持上次的值。
	// （没有 machine.json 时启动会清掉残留的 hub 暂停，见 TestAgentRunWithoutHubClearsStaleHubPause。）
	os.WriteFile(filepath.Join(cfgDir, "state.json"), []byte(`{"paused":false,"hub_paused":true}`), 0o600)
	if err := agent.SaveMachine(filepath.Join(cfgDir, "machine.json"), agent.MachineConfig{HubURL: "http://127.0.0.1:1", MachineID: "m1", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr) }()
	defer func() { cancel(); <-done }()
	sock := filepath.Join(cfgDir, "agent.sock")
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("socket 未出现")
		}
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "resume", "--dir", cfgDir}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "ok") || !strings.Contains(out.String(), "hub 仍暂停") {
		t.Fatalf("hub 暂停时本机 resume 应成功并提示: %d %q %q", code, out.String(), errOut.String())
	}
	out.Reset()
	run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut)
	if !strings.Contains(out.String(), "paused: true (hub)\n") {
		t.Fatalf("status: %q", out.String())
	}
	out.Reset()
	run([]string{"agent", "pause", "--dir", cfgDir}, &out, &errOut)
	if strings.TrimSpace(out.String()) != "ok" {
		t.Fatalf("pause 只应打印 ok: %q", out.String())
	}
}

func TestAgentEnrollWritesMachineJSON(t *testing.T) {
	h := startTestHub(t)
	tok, _ := h.st.CreateEnrollToken(time.Minute)
	cfgDir := filepath.Join(tempDir(t), "h")
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "enroll", "--dir", cfgDir}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "--hub") {
		t.Fatalf("缺参数应报错: %d %q", code, errOut.String())
	}
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", "ftp://x", "--token", tok}, &out, &errOut); code == 0 {
		t.Fatal("非 http(s) 的 --hub 应拒绝")
	}
	errOut.Reset()
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", h.http.URL + "/", "--token", tok, "--name", "mac"}, &out, &errOut); code != 0 {
		t.Fatalf("enroll: %d %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "已接入 hub "+h.http.URL) {
		t.Fatalf("输出: %q", out.String())
	}
	if errOut.String() != "" {
		t.Fatalf("回环地址的 http 不应警告: %q", errOut.String())
	}
	machinePath := filepath.Join(cfgDir, "machine.json")
	m, err := agent.LoadMachine(machinePath)
	if err != nil || m.HubURL != h.http.URL || m.Name != "mac" || m.Secret == "" || m.MachineID == "" {
		t.Fatalf("machine.json: %+v %v", m, err)
	}
	if strings.Contains(out.String(), m.Secret) || strings.Contains(out.String(), tok) {
		t.Fatal("输出里不能有密钥或接入码")
	}
	if !strings.Contains(out.String(), m.MachineID) {
		t.Fatalf("输出应有机器 id: %q", out.String())
	}
	if fi, _ := os.Stat(machinePath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("权限 %v", fi.Mode().Perm())
	}
	var fields map[string]any
	b, _ := os.ReadFile(machinePath)
	json.Unmarshal(b, &fields)
	if len(fields) != 4 {
		t.Fatalf("machine.json 只应有 hub_url、machine_id、secret、name: %v", fields)
	}
	ms, _ := h.st.ListMachines()
	if len(ms) != 1 || ms[0].Name != "mac" || ms[0].ID != m.MachineID {
		t.Fatalf("hub 的机器表: %+v", ms)
	}
	// 已接入过，不加 --force 应拒绝；接入码已消费，加 --force 也应被 hub 拒绝
	errOut.Reset()
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", h.http.URL, "--token", tok}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "--force") {
		t.Fatalf("重复接入: %d %q", code, errOut.String())
	}
	errOut.Reset()
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", h.http.URL, "--token", tok, "--force"}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "401") {
		t.Fatalf("已消费的接入码: %d %q", code, errOut.String())
	}
	if strings.Contains(errOut.String(), tok) {
		t.Fatal("报错里不能回显接入码")
	}
	if m2, _ := agent.LoadMachine(machinePath); m2 != m {
		t.Fatal("失败的接入不应改写 machine.json")
	}
	// 换一个新接入码加 --force 可以重新接入；不给 --name 时名字取主机名
	tok2, _ := h.st.CreateEnrollToken(time.Minute)
	out.Reset()
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", h.http.URL, "--token", tok2, "--force"}, &out, &errOut); code != 0 {
		t.Fatalf("--force 重新接入: %d %q", code, errOut.String())
	}
	if m3, _ := agent.LoadMachine(machinePath); m3.MachineID == m.MachineID || m3.Name != agent.ConfigIn(cfgDir).MachineID {
		t.Fatalf("应换成新的机器身份、名字取主机名: %+v", m3)
	}
}

// 决定 5：--hub 只收 http(s)、要有主机、不带 user:pass 与查询；名字与 hub 的口径一致；这些都在联系 hub 之前挡下。
// http 且主机不是回环时警告明文传输。
func TestAgentEnrollValidation(t *testing.T) {
	cfgDir := filepath.Join(tempDir(t), "h")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--hub", "http://u:s3cretpw@127.0.0.1:1", "--token", "x"}, "用户名"},
		{[]string{"--hub", "http://127.0.0.1:1/?k=v", "--token", "x"}, "查询"},
		{[]string{"--hub", "https://", "--token", "x"}, "http:// 或 https://"},
		{[]string{"--hub", "127.0.0.1:8787", "--token", "x"}, "http:// 或 https://"},
		{[]string{"--hub", "http://127.0.0.1:1", "--token", "x", "--name", "a\x1b[31mb"}, "名字"},
		{[]string{"--hub", "http://127.0.0.1:1", "--token", "x", "--name", "a‮b"}, "名字"},
		{[]string{"--hub", "http://127.0.0.1:1", "--token", "x", "--name", strings.Repeat("a", 129)}, "128"},
		{[]string{"--hub", "http://127.0.0.1:1", "--token", "x", "extra"}, "extra"},
	} {
		var out, errOut bytes.Buffer
		code := run(append([]string{"agent", "enroll", "--dir", cfgDir}, c.args...), &out, &errOut)
		if code == 0 || !strings.Contains(errOut.String(), c.want) || strings.Contains(errOut.String(), "s3cretpw") {
			t.Fatalf("%v: %d %q", c.args, code, errOut.String())
		}
		if strings.Contains(errOut.String(), "连接 hub") {
			t.Fatalf("%v 应在联系 hub 之前挡下: %q", c.args, errOut.String())
		}
	}
	// 先占住 machine.json：后面的调用不加 --force，到“已接入过”就停，不会真去连
	os.MkdirAll(cfgDir, 0o700)
	if err := agent.SaveMachine(filepath.Join(cfgDir, "machine.json"), agent.MachineConfig{HubURL: "https://hub.example.com", MachineID: "m", Secret: "s"}); err != nil {
		t.Fatal(err)
	}
	for hubURL, warn := range map[string]bool{
		"http://hub.example.com": true, "http://10.0.0.5:8787": true, "http://localhost.example.com": true,
		"https://hub.example.com": false, "http://localhost:8787": false, "http://LOCALHOST": false, "http://hub.localhost": false,
		"http://127.0.0.1:8787": false, "http://127.9.9.9": false, "http://[::1]:8787": false,
	} {
		var out, errOut bytes.Buffer
		if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", hubURL, "--token", "x"}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "--force") {
			t.Fatalf("%s: %d %q", hubURL, code, errOut.String())
		}
		if got := strings.Contains(errOut.String(), "明文传输密钥，只适合本机或可信内网"); got != warn {
			t.Fatalf("%s: 警告 = %v，应为 %v: %q", hubURL, got, warn, errOut.String())
		}
	}
}

// 决定 5：不跟随重定向（接入码与密钥只发给 --hub 指定的地址）；hub 的报错只转述状态码与截断后的 error 文案，
// 去掉控制字符、不回显接入码；答复限量读，不完整或不合规就不写 machine.json。
func TestAgentEnrollHandlesHostileHub(t *testing.T) {
	target := startTestHub(t)
	tok, _ := target.st.CreateEnrollToken(time.Minute)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.http.URL+"/api/enroll", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	cfgDir := filepath.Join(tempDir(t), "h")
	machinePath := filepath.Join(cfgDir, "machine.json")
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", redirector.URL, "--token", tok}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "307") {
		t.Fatalf("重定向应失败: %d %q", code, errOut.String())
	}
	if err := target.st.ConsumeEnrollToken(tok); err != nil {
		t.Fatalf("不应跟随重定向把接入码送到别处: %v", err)
	}
	var mu sync.Mutex
	var reply func(w http.ResponseWriter, token string)
	hostile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Token string `json:"token"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		f := reply
		mu.Unlock()
		f(w, body.Token)
	}))
	defer hostile.Close()
	for name, c := range map[string]struct {
		reply func(w http.ResponseWriter, token string)
		want  string
	}{
		"回显接入码与控制字符": {func(w http.ResponseWriter, token string) {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": "坏了\x1b[2J" + token + strings.Repeat("长", 500)})
		}, "400"},
		"不是 JSON": {func(w http.ResponseWriter, token string) {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("<html>" + token + "</html>"))
		}, "502"},
		"答复超长": {func(w http.ResponseWriter, token string) {
			w.Write([]byte(`{"machine_id":"m","secret":"s","pad":"` + strings.Repeat("x", 128<<10) + `"}`))
		}, "答复"},
		"缺密钥": {func(w http.ResponseWriter, token string) {
			w.Write([]byte(`{"machine_id":"m"}`))
		}, "答复"},
		"机器 id 带控制字符": {func(w http.ResponseWriter, token string) {
			w.Write([]byte(`{"machine_id":"m\u001b[2J","secret":"s"}`))
		}, "答复"},
	} {
		mu.Lock()
		reply = c.reply
		mu.Unlock()
		errOut.Reset()
		const secretTok = "tok-9f8e7d6c5b4a"
		code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", hostile.URL, "--token", secretTok}, &out, &errOut)
		msg := errOut.String()
		if code == 0 || !strings.Contains(msg, c.want) || strings.Contains(msg, secretTok) || strings.Contains(msg, "\x1b") {
			t.Fatalf("%s: %d %q", name, code, msg)
		}
		if len(msg) > 1024 {
			t.Fatalf("%s: 报错应截断: %d 字节", name, len(msg))
		}
		if _, err := os.Stat(machinePath); err == nil {
			t.Fatalf("%s: 失败时不应写 machine.json", name)
		}
	}
}

// serveAgent 在后台跑 runAgentServe 并等 socket 出现。返回的 stop 取消并等它返回（返回它的错误），测试结束时也会调用。
func serveAgent(t *testing.T, cfg agent.Config) (stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runAgentServe(ctx, cfg, false, os.Stderr) }()
	waitUntil(t, func() bool {
		select {
		case err := <-done:
			done <- err
			return true
		default:
		}
		_, err := os.Stat(cfg.SocketPath)
		return err == nil
	})
	var once sync.Once
	var err error
	stop = func() error {
		once.Do(func() {
			cancel()
			select {
			case err = <-done:
			case <-time.After(10 * time.Second):
				err = errors.New("守护进程没有在取消后停下")
			}
		})
		return err
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
		if t.Failed() {
			b, _ := os.ReadFile(cfg.LogPath)
			t.Logf("守护进程日志:\n%s", b)
		}
	})
	return stop
}

// enrollTestMachine 用 railse agent enroll 把 cfgDir 接入测试 hub，返回写好的 machine.json。
func enrollTestMachine(t *testing.T, h *testHub, cfgDir string) agent.MachineConfig {
	t.Helper()
	tok, err := h.st.CreateEnrollToken(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "enroll", "--dir", cfgDir, "--hub", h.http.URL, "--token", tok, "--name", "mac"}, &out, &errOut); code != 0 {
		t.Fatalf("enroll: %d %s", code, errOut.String())
	}
	m, err := agent.LoadMachine(filepath.Join(cfgDir, "machine.json"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// hubLogin 给测试 hub 设密码并登录，返回会话 cookie（登录按来源每分钟限 5 次，测试里登录一次反复用）。
func hubLogin(t *testing.T, h *testHub) *http.Cookie {
	t.Helper()
	if err := h.srv.SetPassword("hunter22"); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"password": "hunter22"})
	resp, err := http.Post(h.http.URL+"/api/login", "application/json", bytes.NewReader(body))
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("login: %v %v", err, resp)
	}
	resp.Body.Close()
	return resp.Cookies()[0]
}

// machineOnline 经网页接口问 hub 某台机器是否在线。
func machineOnline(t *testing.T, h *testHub, cookie *http.Cookie, id string) bool {
	t.Helper()
	req, _ := http.NewRequest("GET", h.http.URL+"/api/machines", nil)
	req.AddCookie(cookie)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil || resp2.StatusCode != http.StatusOK {
		t.Fatalf("machines: %v %v", err, resp2)
	}
	defer resp2.Body.Close()
	var ms []map[string]any
	json.NewDecoder(resp2.Body).Decode(&ms)
	for _, m := range ms {
		if m["id"] == id {
			return m["online"] == true
		}
	}
	t.Fatalf("hub 没有机器 %s: %v", id, ms)
	return false
}

// 有 machine.json：守护进程以 hub 的机器 id 运行、带上版本号连 hub，要人决定的请求进 hub 等手机决定，
// 决定交给钩子、本机审计与 hub 审计都有记录；停下后 hub 看到本机离线。
func TestAgentRunWithHub(t *testing.T) {
	h := startTestHub(t)
	dir := tempDir(t)
	cfgDir := filepath.Join(dir, "h")
	m := enrollTestMachine(t, h, cfgDir)
	old := version
	version = "9.9.9-test"
	t.Cleanup(func() { version = old })
	cfg := agent.ConfigIn(cfgDir)
	stop := serveAgent(t, cfg)
	waitUntil(t, func() bool {
		ms, _ := h.st.ListMachines()
		return len(ms) == 1 && ms[0].AgentVersion == "9.9.9-test"
	})
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "machine: "+m.MachineID+"\n") {
		t.Fatalf("status 应显示 hub 的机器 id: %d %q", code, out.String())
	}
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	res := make(chan string, 1)
	go func() {
		o, _, _ := runHookWith(t, claudePermission(repo, "curl https://x.y"), "claude", "--socket", cfg.SocketPath)
		res <- o
	}()
	var id string
	waitUntil(t, func() bool {
		rs, err := h.st.ListPendingForMachine(m.MachineID)
		if err != nil || len(rs) != 1 {
			return false
		}
		id = rs[0].ID
		return true
	})
	cookie := hubLogin(t, h)
	if !machineOnline(t, h, cookie, m.MachineID) {
		t.Fatal("hub 应看到本机在线")
	}
	if _, err := h.srv.Decide(hub.DecideInput{RequestID: id, Action: "allow", Note: "手机批准"}); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-res:
		if !strings.Contains(o, `"behavior":"allow"`) {
			t.Fatalf("钩子应拿到批准: %q", o)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("钩子没有拿到决定")
	}
	if !auditHas(t, cfg.AuditPath, `"request_id":"`+id+`"`, `"state":"approved"`, `"decided_by":"user"`) {
		b, _ := os.ReadFile(cfg.AuditPath)
		t.Fatalf("本机审计应记 approved/user:\n%s", b)
	}
	waitUntil(t, func() bool {
		r, err := h.st.GetRequest(id)
		return err == nil && r.DeliveredAt != nil
	})
	waitUntil(t, func() bool {
		es, _ := h.st.ListAudit(0, id)
		for _, e := range es {
			if e.Actor == "agent" && e.Action == "approved" {
				return true
			}
		}
		return false
	})
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return !machineOnline(t, h, cookie, m.MachineID) })
}

// 决定 3：hub 以 401 拒绝本机密钥（机器被吊销）时客户端停用，守护进程照常服务，要人决定的请求立即交回终端，日志写明要重新接入。
func TestAgentRunKeepsServingWhenHubRevokes(t *testing.T) {
	h := startTestHub(t)
	dir := tempDir(t)
	cfgDir := filepath.Join(dir, "h")
	m := enrollTestMachine(t, h, cfgDir)
	if err := h.st.RevokeMachine(m.MachineID); err != nil {
		t.Fatal(err)
	}
	cfg := agent.ConfigIn(cfgDir)
	serveAgent(t, cfg)
	waitUntil(t, func() bool {
		b, _ := os.ReadFile(cfg.LogPath)
		return strings.Contains(string(b), "重新 railse agent enroll")
	})
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut); code != 0 {
		t.Fatalf("守护进程应照常服务: %d %q", code, errOut.String())
	}
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	start := time.Now()
	o, _, code := runHookWith(t, claudePermission(repo, "curl https://x.y"), "claude", "--socket", cfg.SocketPath)
	if code != 0 || o != "" {
		t.Fatalf("应交回终端: %d %q", code, o)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("停用后应立即交回终端，不等宽限")
	}
	if !auditHas(t, cfg.AuditPath, `"state":"expired"`, `"decided_by":"hub_unreachable"`) {
		b, _ := os.ReadFile(cfg.AuditPath)
		t.Fatalf("本机审计应记 expired/hub_unreachable:\n%s", b)
	}
	b, _ := os.ReadFile(cfg.LogPath)
	if strings.Contains(string(b), m.Secret) {
		t.Fatal("日志里不能有机器密钥")
	}
}

// 决定 3：machine.json 损坏时拒绝启动（不静默退回本地模式），报错不带密钥。
func TestAgentRunRefusesBadMachineJSON(t *testing.T) {
	const secret = "sekrit-0123456789abcdef"
	for _, content := range []string{
		`{"hub_url":"http://127.0.0.1:1","machine_id":"m","secret":"` + secret + `"`,
		`{"hub_url":"ftp://x","machine_id":"m","secret":"` + secret + `"}`,
		`{"hub_url":"http://127.0.0.1:1","machine_id":"m","secret":` + strings.Repeat("9", 30) + `}`,
	} {
		cfgDir := filepath.Join(tempDir(t), "h")
		os.MkdirAll(cfgDir, 0o700)
		os.WriteFile(filepath.Join(cfgDir, "machine.json"), []byte(content), 0o600)
		// 先直接调（带期限）：若误把它当成没接入而开始服务，这里超时失败而不是挂住
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := runAgentServe(ctx, agent.ConfigIn(cfgDir), false, os.Stderr)
		cancel()
		if err == nil {
			t.Fatalf("%s: 应拒绝启动", content)
		}
		var out, errOut bytes.Buffer
		if code := run([]string{"agent", "run", "--dir", cfgDir}, &out, &errOut); code == 0 || !strings.Contains(errOut.String(), "machine.json") {
			t.Fatalf("%s: 应拒绝启动: %d %q", content, code, errOut.String())
		}
		if strings.Contains(errOut.String(), secret) || strings.Contains(errOut.String(), "999999") {
			t.Fatalf("报错里不能有密钥: %q", errOut.String())
		}
		if _, err := os.Stat(filepath.Join(cfgDir, "agent.sock")); err == nil {
			t.Fatal("不应开始监听")
		}
	}
}

func TestAgentRunWithoutHubStillWorks(t *testing.T) {
	// 没有 machine.json：照旧本地闭环，日志说明未接入
	cfgDir := filepath.Join(tempDir(t), "h")
	cfg := agent.ConfigIn(cfgDir)
	stop := serveAgent(t, cfg)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(cfgDir, "agent.log"))
	if !strings.Contains(string(b), "未接入 hub") {
		t.Fatalf("日志应说明未接入:\n%s", b)
	}
}

// 决定 3：取消接入（删掉 machine.json）后，state.json 里残留的 hub 暂停在启动时清掉，不再让本机一直暂停；本机的暂停不受影响。
func TestAgentRunWithoutHubClearsStaleHubPause(t *testing.T) {
	for state, want := range map[string]string{
		`{"paused":false,"hub_paused":true}`: "paused: false\n",
		`{"paused":true,"hub_paused":true}`:  "paused: true (local)\n",
	} {
		cfgDir := filepath.Join(tempDir(t), "h")
		os.MkdirAll(cfgDir, 0o700)
		os.WriteFile(filepath.Join(cfgDir, "state.json"), []byte(state), 0o600)
		cfg := agent.ConfigIn(cfgDir)
		stop := serveAgent(t, cfg)
		var out, errOut bytes.Buffer
		if code := run([]string{"agent", "status", "--dir", cfgDir}, &out, &errOut); code != 0 || !strings.Contains(out.String(), want) {
			t.Fatalf("%s: 残留的 hub 暂停应清掉: %d %q", state, code, out.String())
		}
		b, _ := os.ReadFile(cfg.StatePath)
		if !strings.Contains(string(b), `"hub_paused":false`) {
			t.Fatalf("state.json 应落盘: %s", b)
		}
		logs, _ := os.ReadFile(cfg.LogPath)
		if !strings.Contains(string(logs), "残留的 hub 暂停") {
			t.Fatalf("应记日志:\n%s", logs)
		}
		if err := stop(); err != nil {
			t.Fatal(err)
		}
	}
}
