package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/proto"
)

// hookAskSecret 是 redact 认得的 GitHub 令牌形状。
const hookAskSecret = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"

// hookAskQuestions 里有令牌原文、< > &、转义引号与 U+2028（JSON 转义写法）：钩子输出必须逐字节原样回填。
const hookAskQuestions = `[{"question":"用令牌 ` + hookAskSecret + ` 推送 <main> & \"发布\" 吗？","header":"推送","options":[{"label":"Apple","description":"苹果"},{"label":"Banana"}],"multiSelect":false}]`

// hookAskOrig 是上面这道题解码后的题面。
const hookAskOrig = "用令牌 " + hookAskSecret + " 推送 <main> & \"发布\" 吗？"

// claudeAsk 是 Claude Code 2.1.280 的 PreToolUse/AskUserQuestion 输入；tool_input 另带一个 Go 不认识的字段。
func claudeAsk(cwd string) string {
	return `{"session_id":"s1","prompt_id":"p1","cwd":"` + cwd + `","hook_event_name":"PreToolUse","tool_name":"AskUserQuestion","tool_use_id":"tu1","tool_input":{"questions":` + hookAskQuestions + `,"metadata":{"k":[1,2]}}}`
}

// answerDecider 给每道（脱敏后的）题都答 Banana，并记下收到的题目。
type answerDecider struct {
	mu  sync.Mutex
	got []*proto.Ask
}

func (d *answerDecider) Decide(ctx context.Context, pr agent.PendingRequest) (agent.Outcome, error) {
	d.mu.Lock()
	d.got = append(d.got, pr.Ask)
	d.mu.Unlock()
	ans := map[string]string{}
	for _, q := range pr.Ask.Questions {
		ans[q.Question] = "Banana"
	}
	return agent.Outcome{Action: "answer", Answers: ans, Reason: "已在手机上回答", DecidedBy: "user"}, nil
}

// rawObject 取出 JSON 对象各键的原始字节。
func rawObject(t *testing.T, b []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("不是 JSON 对象: %v: %s", err, b)
	}
	return m
}

// 手机上答了：钩子输出合规的 PreToolUse JSON，questions 与其他字段逐字节原样，answers 的键是原题面（含令牌原文）；
// 守护进程交给 hub 的题目已脱敏。
func TestHookClaudeAskAnswer(t *testing.T) {
	d := &answerDecider{}
	sock, repo, cfg := startAgentWith(t, d)
	out, errOut, code := runHookWith(t, claudeAsk(repo), "claude", "--socket", sock)
	if code != 0 || !strings.HasSuffix(out, "}\n") || strings.Count(out, "\n") != 1 {
		t.Fatalf("应输出一行 JSON: code=%d out=%q err=%q", code, out, errOut)
	}
	top := rawObject(t, []byte(out))
	hso := rawObject(t, top["hookSpecificOutput"])
	if string(hso["hookEventName"]) != `"PreToolUse"` || string(hso["permissionDecision"]) != `"allow"` || len(top) != 1 || len(hso) != 3 {
		t.Fatalf("外层字段不对: %s", out)
	}
	upd := rawObject(t, hso["updatedInput"])
	if string(upd["questions"]) != hookAskQuestions {
		t.Fatalf("questions 不是逐字节原样:\n得到 %s\n想要 %s", upd["questions"], hookAskQuestions)
	}
	if string(upd["metadata"]) != `{"k":[1,2]}` || len(upd) != 3 {
		t.Fatalf("tool_input 的其他字段应原样回填: %s", hso["updatedInput"])
	}
	var answers map[string]string
	if err := json.Unmarshal(upd["answers"], &answers); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(answers, map[string]string{hookAskOrig: "Banana"}) {
		t.Fatalf("answers 应以原题面为键: %q", answers)
	}
	d.mu.Lock()
	got := d.got
	d.mu.Unlock()
	if len(got) != 1 || strings.Contains(got[0].Questions[0].Question, hookAskSecret) || !strings.Contains(got[0].Questions[0].Question, "<redacted>") {
		t.Fatalf("交给 Decider 的题目应已脱敏: %+v", got)
	}
	mustAudit(t, cfg.AuditPath, "有 answered 的 ask", `"kind":"ask"`, `"state":"answered"`, `"decided_by":"user"`)
	if b, _ := os.ReadFile(cfg.AuditPath); strings.Contains(string(b), hookAskSecret) {
		t.Fatalf("审计里出现了令牌原文:\n%s", b)
	}
}

// 没接 hub：钩子不输出，Claude Code 在终端照常问；审计 passthrough/no_hub。
func TestHookClaudeAskNoHub(t *testing.T) {
	sock, repo, cfg := startTestAgent(t)
	out, _, code := runHookWith(t, claudeAsk(repo), "claude", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("没接 hub 应不输出: code=%d out=%q", code, out)
	}
	mustAudit(t, cfg.AuditPath, "记 passthrough/no_hub", `"kind":"ask"`, `"state":"passthrough"`, `"decided_by":"no_hub"`)
}

// hub 不可达：宽限到期后交回终端，钩子不输出；审计 expired/hub_unreachable。
func TestHookClaudeAskHubUnreachable(t *testing.T) {
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	ccfg := agent.ConfigIn(dir)
	if err := ccfg.EnsureDirs(); err != nil {
		t.Fatal(err)
	}
	rules, err := agent.LoadRules(ccfg.RulesPath)
	if err != nil {
		t.Fatal(err)
	}
	logger := log.New(os.Stderr, "", 0)
	client := agent.NewHubClient(ccfg, agent.MachineConfig{HubURL: "http://127.0.0.1:1", MachineID: "m", Secret: "s"}, rules, agent.NewPendingForTest(), func(bool) error { return nil }, logger)
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		client.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-ran
	})
	sock, repo, cfg := startAgentWith(t, &agent.HubDecider{Client: client, Grace: 300 * time.Millisecond, Log: logger})
	out, _, code := runHookWith(t, claudeAsk(repo), "claude", "--socket", sock)
	if code != 0 || out != "" {
		t.Fatalf("hub 不可达应不输出: code=%d out=%q", code, out)
	}
	mustAudit(t, cfg.AuditPath, "记 expired/hub_unreachable", `"kind":"ask"`, `"state":"expired"`, `"decided_by":"hub_unreachable"`)
}

// 钩子中途放弃（CLI 已退出，父进程变了）：不输出，守护进程把 ask 记为 cancelled/terminal，不写回。
func TestHookClaudeAskParentGone(t *testing.T) {
	started := make(chan struct{}, 1)
	sock, repo, cfg := startAgentWith(t, blockingDecider{started: started})
	var ppid atomic.Int32
	ppid.Store(4242)
	oldGet, oldInterval := getppid, parentCheckInterval
	getppid = func() int { return int(ppid.Load()) }
	parentCheckInterval = 20 * time.Millisecond
	defer func() { getppid, parentCheckInterval = oldGet, oldInterval }()
	go func() {
		<-started
		ppid.Store(1)
	}()
	out, errOut, code := runHookWith(t, claudeAsk(repo), "claude", "--socket", sock)
	if code != 0 || out != "" || !strings.Contains(errOut, "父进程") {
		t.Fatalf("父进程退出后应不输出: code=%d out=%q err=%q", code, out, errOut)
	}
	waitUntil(t, func() bool {
		return auditHas(t, cfg.AuditPath, `"kind":"ask"`, `"state":"cancelled"`, `"decided_by":"terminal"`)
	})
}

// fakeDaemon 在临时 socket 上对每条请求回 reply，并记下收到的请求行。
func fakeDaemon(t *testing.T, reply string) (sock string, lines func() []string) {
	t.Helper()
	dir, _ := os.MkdirTemp("", "rs")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock = filepath.Join(dir, "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var mu sync.Mutex
	var got []string
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			line, err := proto.ReadLine(bufio.NewReader(conn))
			if err == nil {
				mu.Lock()
				got = append(got, string(line))
				mu.Unlock()
				conn.Write([]byte(reply + "\n"))
			}
			conn.Close()
		}
	}()
	return sock, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), got...)
	}
}

// 钩子发给守护进程的是 ask 请求；守护进程的答复不是带答案的 answer 时（旧守护进程、出错）一律不输出，
// 答题类 PreToolUse 不写审批的 JSON。
func TestHookClaudeAskOnlyRendersAnswer(t *testing.T) {
	for _, reply := range []string{
		`{"v":1,"decision":"allow"}`,
		`{"v":1,"decision":"deny","reason":"不行"}`,
		`{"v":1,"decision":"passthrough","reason":"未知消息种类 ask"}`,
		`{"v":1,"decision":"answer"}`,
	} {
		sock, lines := fakeDaemon(t, reply)
		out, _, code := runHookWith(t, claudeAsk("/p"), "claude", "--socket", sock)
		if code != 0 || out != "" {
			t.Fatalf("守护进程答 %s 时应不输出: code=%d out=%q", reply, code, out)
		}
		got := lines()
		if len(got) != 1 {
			t.Fatalf("应发一条请求: %q", got)
		}
		var req proto.Request
		if err := json.Unmarshal([]byte(got[0]), &req); err != nil {
			t.Fatal(err)
		}
		if req.V != proto.Version || req.Kind != "ask" || req.ToolName != "AskUserQuestion" || req.DeadlineSeconds != 86390 || req.Session.CLISessionID != "s1" ||
			req.Ask == nil || req.Ask.Questions[0].Question != hookAskOrig || !bytes.Contains([]byte(got[0]), []byte(`"payload":{"type":""}`)) {
			t.Fatalf("发给守护进程的 ask 请求不对: %s", got[0])
		}
	}
}
