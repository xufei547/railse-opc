package agent

import (
	"fmt"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

func TestRecent(t *testing.T) {
	r := newRecent()
	now := time.Unix(1_000_000, 0)
	r.now = func() time.Time { return now }
	mk := func(sess, cmd, id string) proto.Request {
		return proto.Request{Session: proto.Session{CLISessionID: sess}, ToolName: "Bash", ToolUseID: id,
			Payload: policy.Payload{Type: policy.PayloadShell, Command: cmd}}
	}
	pre := mk("s", "git push", "toolu_1")
	r.remember(r.keys(pre), recentTerminal)
	if out, ok := r.lookup(r.keys(mk("s", "git push", ""))); !ok || out != recentTerminal {
		t.Fatal("无 id 应按会话 + 载荷对上")
	}
	if out, ok := r.lookup(r.keys(mk("s", "git push origin", "toolu_1"))); !ok || out != recentTerminal {
		t.Fatal("同 id 应对上")
	}
	if _, ok := r.lookup(r.keys(mk("s2", "git push", ""))); ok {
		t.Fatal("别的会话不该对上")
	}
	if _, ok := r.lookup(r.keys(mk("s", "git pull", ""))); ok {
		t.Fatal("别的命令不该对上")
	}
	now = now.Add(recentTTL)
	if _, ok := r.lookup(r.keys(pre)); ok {
		t.Fatal("10 分钟后应过期")
	}
	// 容量：满了丢最旧的
	for i := range recentMax + 10 {
		now = now.Add(time.Millisecond)
		r.remember([]string{fmt.Sprint(i)}, recentClassifier)
	}
	if len(r.entries) > recentMax {
		t.Fatalf("超过容量：%d", len(r.entries))
	}
	if _, ok := r.lookup([]string{"0"}); ok {
		t.Fatal("最旧的应被丢掉")
	}
	if _, ok := r.lookup([]string{fmt.Sprint(recentMax + 9)}); !ok {
		t.Fatal("最新的应还在")
	}
}
