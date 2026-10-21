package agent

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/proto"
)

func event(kind, session, msg string) proto.Event {
	return proto.Event{V: proto.Version, Kind: kind, Session: proto.Session{Tool: "claude", CLISessionID: session, Cwd: "/p"}, Message: msg}
}

func control(op string) proto.Control {
	return proto.Control{V: proto.Version, Kind: "control", Op: op}
}

func TestEventsAreRecorded(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	for _, ev := range []proto.Event{event("question", "s9", "等你"), event("done", "s9", "DONE"), event("session_end", "s9", "")} {
		var ack proto.EventAck
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, ev), &ack)
		if !ack.OK {
			t.Fatalf("%s: %+v", ev.Kind, ack)
		}
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 3 || entries[0].Kind != "question" || entries[0].State != "recorded" || entries[0].Message != "等你" || entries[1].Kind != "done" || entries[2].Kind != "session_end" {
		t.Fatalf("审计: %+v", entries)
	}
}

func TestQuestionSuppressedWhilePending(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{})}
	env := startServer(t, d, nil)
	conn, _ := net.Dial("unix", env.cfg.SocketPath)
	defer conn.Close()
	req := permission(env.repo, "curl https://x.y")
	req.Session.CLISessionID = "s-pending"
	proto.WriteLine(conn, req)
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	roundTrip(t, env.cfg.SocketPath, event("question", "s-pending", "Claude needs your permission"))
	roundTrip(t, env.cfg.SocketPath, event("question", "s-other", "Claude is waiting"))
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 2 || entries[0].State != "suppressed" || entries[1].State != "recorded" {
		t.Fatalf("审计: %+v", entries)
	}
	close(d.release)
}

func TestControlStatusPauseResumeReload(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("status")), &reply)
	if !reply.OK || reply.Status == nil || reply.Status.Paused || reply.Status.Rules != 2 || reply.Status.MachineID == "" || reply.Status.RulesPath != env.cfg.RulesPath {
		t.Fatalf("status: %+v", reply)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("pause")), &reply)
	if !reply.OK {
		t.Fatalf("pause: %+v", reply)
	}
	b, _ := os.ReadFile(env.cfg.StatePath)
	if !strings.Contains(string(b), `"paused":true`) {
		t.Fatalf("state.json 未写入: %s", b)
	}
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
	if resp.Decision != "passthrough" || !strings.Contains(resp.Reason, "暂停") {
		t.Fatalf("暂停时只读命令也应交回终端: %+v", resp)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("resume")), &reply)
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
	if resp.Decision != "allow" {
		t.Fatalf("恢复后应放行: %+v", resp)
	}
	os.WriteFile(env.cfg.RulesPath, []byte("version: 7\n"), 0o600)
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("reload")), &reply)
	if reply.OK || !strings.Contains(reply.Error, "version") {
		t.Fatalf("坏规则 reload 应失败: %+v", reply)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("status")), &reply)
	if reply.Status.Rules != 2 {
		t.Fatalf("失败的 reload 应保留旧规则: %+v", reply.Status)
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("bogus")), &reply)
	if reply.OK {
		t.Fatal("未知 op 应失败")
	}
}

func TestLoadStateRestoresPause(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	os.WriteFile(env.cfg.StatePath, []byte(`{"paused":true}`), 0o600)
	if err := env.srv.LoadState(); err != nil {
		t.Fatal(err)
	}
	if !env.srv.rules.Paused() {
		t.Fatal("应恢复暂停状态")
	}
}

func TestMalformedInputsDoNotStopServer(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	send := func(raw string) string {
		conn, err := net.Dial("unix", env.cfg.SocketPath)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		conn.Write([]byte(raw))
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		return string(buf[:n])
	}
	if got := send("not json\n"); !strings.Contains(got, "passthrough") {
		t.Fatalf("坏 JSON 应得到不作决定: %q", got)
	}
	if got := send(`{"v":1,"kind":"mystery"}` + "\n"); !strings.Contains(got, "passthrough") {
		t.Fatalf("未知种类应得到不作决定: %q", got)
	}
	if got := send(`{"v":3,"kind":"permission"}` + "\n"); !strings.Contains(got, "passthrough") {
		t.Fatalf("错版本应得到不作决定: %q", got)
	}
	if got := send(strings.Repeat("x", proto.MaxLine+10) + "\n"); got != "" {
		t.Fatalf("超长行应被直接关闭: %q", got)
	}
	// 服务还活着
	var resp proto.Response
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
	if resp.Decision != "allow" {
		t.Fatalf("故障注入后服务应正常: %+v", resp)
	}
}

func TestReadTimeoutClosesIdleConn(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	conn, _ := net.Dial("unix", env.cfg.SocketPath)
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	buf := make([]byte, 16)
	start := time.Now()
	_, err := conn.Read(buf) // 什么都不发，服务端 5 秒后应关闭
	if err == nil || time.Since(start) > 7*time.Second {
		t.Fatalf("空连接应在读超时后被关闭: err=%v 用时=%v", err, time.Since(start))
	}
}

func TestPauseTightensExistingStateFile(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	os.WriteFile(env.cfg.StatePath, []byte(`{"paused":false}`), 0o644)
	os.Chmod(env.cfg.StatePath, 0o644)
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("pause")), &reply)
	if !reply.OK {
		t.Fatalf("pause: %+v", reply)
	}
	st, err := os.Stat(env.cfg.StatePath)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("已有的 state.json 应收紧为 0600: %v, %v", st, err)
	}
}

func TestEventMessageIsRedacted(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	// Gemini 的 ToolPermission 通知、Claude/Codex 的 last_assistant_message 都可能带出完整命令
	msg := "Gemini CLI requires execution: export GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123 && npm publish"
	for _, kind := range []string{"question", "done"} {
		var ack proto.EventAck
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, event(kind, "s-redact", msg)), &ack)
		if !ack.OK {
			t.Fatalf("%s: %+v", kind, ack)
		}
	}
	b, err := os.ReadFile(env.cfg.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "ghp_") {
		t.Fatalf("事件消息写审计前应脱敏:\n%s", b)
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 2 || entries[0].Message != "Gemini CLI requires execution: export GITHUB_TOKEN=<redacted> && npm publish" {
		t.Fatalf("审计: %+v", entries)
	}
}

func TestLoadStateFailsClosed(t *testing.T) {
	// 空文件（截断后崩溃留下的样子）、坏 JSON、读不了（是目录）都按暂停启动并报错
	for name, setup := range map[string]func(path string){
		"empty":   func(p string) { os.WriteFile(p, nil, 0o600) },
		"garbage": func(p string) { os.WriteFile(p, []byte(`{"paused":`), 0o600) },
		"dir":     func(p string) { os.Mkdir(p, 0o700) },
	} {
		t.Run(name, func(t *testing.T) {
			env := startServer(t, LocalDecider{}, nil)
			setup(env.cfg.StatePath)
			if err := env.srv.LoadState(); err == nil {
				t.Fatal("状态文件损坏时 LoadState 应返回错误")
			}
			if !env.srv.rules.Paused() {
				t.Fatal("状态文件损坏时应按暂停启动")
			}
		})
	}
	env := startServer(t, LocalDecider{}, nil)
	if err := env.srv.LoadState(); err != nil || env.srv.rules.Paused() {
		t.Fatalf("没有状态文件时应正常按未暂停启动: err=%v paused=%v", err, env.srv.rules.Paused())
	}
}

func TestPauseWritesStateAtomically(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	os.WriteFile(env.cfg.StatePath, []byte(`{"paused":false}`), 0o600)
	before, err := os.Stat(env.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("pause")), &reply)
	if !reply.OK {
		t.Fatalf("pause: %+v", reply)
	}
	after, err := os.Stat(env.cfg.StatePath)
	if err != nil {
		t.Fatal(err)
	}
	// 先写临时文件再 rename：state.json 换成了新文件，而不是在原文件上截断重写
	if os.SameFile(before, after) {
		t.Fatal("state.json 应经临时文件 rename 替换，不能截断原文件再写")
	}
	ents, _ := os.ReadDir(env.cfg.Dir)
	for _, e := range ents {
		switch e.Name() {
		case "agent.sock", "rules.yaml", "audit.jsonl", "state.json":
		default:
			t.Errorf("配置目录里不应留下临时文件: %s", e.Name())
		}
	}
}

func TestPauseResumePersistFailure(t *testing.T) {
	env := startServer(t, LocalDecider{}, func(c *Config) { c.StatePath = filepath.Join(c.Dir, "missing", "state.json") })
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("pause")), &reply)
	if reply.OK || !strings.Contains(reply.Error, "已暂停") || !strings.Contains(reply.Error, "未能持久化") {
		t.Fatalf("pause 落盘失败应回复 OK:false 并说明已暂停但未持久化: %+v", reply)
	}
	if !env.srv.rules.Paused() {
		t.Fatal("落盘失败时内存里仍应已暂停")
	}
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("resume")), &reply)
	if reply.OK || !strings.Contains(reply.Error, "已恢复") || !strings.Contains(reply.Error, "未能持久化") {
		t.Fatalf("resume 落盘失败应回复 OK:false 并说明已恢复但未持久化: %+v", reply)
	}
	if env.srv.rules.Paused() {
		t.Fatal("resume 后内存里应已恢复")
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 2 || entries[0].Kind != "control" || entries[0].Message != "pause" || entries[1].Message != "resume" {
		t.Fatalf("落盘失败也应照常写审计: %+v", entries)
	}
}
