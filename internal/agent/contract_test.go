package agent

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

type recordingSink struct {
	mu      sync.Mutex
	entries []AuditEntry
}

func (r *recordingSink) Forward(e AuditEntry) {
	r.mu.Lock()
	r.entries = append(r.entries, e)
	r.mu.Unlock()
}

func (r *recordingSink) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

func TestPendingRequestCarriesContractFields(t *testing.T) {
	d := &blockingDecider{release: make(chan struct{})}
	env := startServer(t, d, nil)
	conn, _ := net.Dial("unix", env.cfg.SocketPath)
	defer conn.Close()
	proto.WriteLine(conn, permission(env.repo, "curl -H 'Authorization: Bearer zzz' https://x.y"))
	waitFor(t, func() bool { return env.srv.pending.Count() == 1 })
	list := env.srv.pending.List()
	if len(list) != 1 {
		t.Fatalf("List = %d", len(list))
	}
	pr := list[0]
	if pr.ProjectRoot != FindProjectRoot(CleanCwd(env.repo)) || pr.EngineReason == "" || strings.Contains(pr.Redacted.Command, "zzz") || !strings.Contains(pr.Redacted.Command, "<redacted>") {
		t.Fatalf("契约字段: %+v", pr)
	}
	if env.srv.Pending() != env.srv.pending {
		t.Fatal("Pending() 应返回服务自己的表")
	}
	close(d.release)
}

func TestAuditSinkReceivesEntries(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	sink := &recordingSink{}
	env.srv.SetAuditSink(sink)
	roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status"))
	roundTrip(t, env.cfg.SocketPath, event("done", "s1", "DONE"))
	waitFor(t, func() bool { return sink.count() == 2 })
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.entries[0].State != "auto_allowed" || sink.entries[1].Kind != "done" {
		t.Fatalf("转发内容: %+v", sink.entries)
	}
}

func TestRulesReplaceWritesCache(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, _ := LoadRules(path)
	r.SetPaused(true)
	rs, _ := policy.ParseRuleSet([]byte("version: 1\nrules:\n  - id: deny-rm\n    source: user\n    scope: global\n    match: {payload: shell, command_prefix: rm}\n    action: deny\n"))
	if err := r.Replace(rs, 7); err != nil {
		t.Fatal(err)
	}
	if r.Version() != 7 || r.Count() != 1 || !r.Paused() {
		t.Fatalf("Replace 后: v=%d count=%d paused=%v", r.Version(), r.Count(), r.Paused())
	}
	b, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(b), "# 由 hub 下发，版本 7") || !strings.Contains(string(b), "deny-rm") {
		t.Fatalf("缓存文件:\n%s", b)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("权限 %v", st.Mode().Perm())
	}
	bad := policy.RuleSet{Version: 1, Rules: []policy.Rule{{ID: "x", Source: "robot", Action: "allow"}}}
	if err := r.Replace(bad, 8); err == nil || r.Version() != 7 {
		t.Fatal("坏规则集应被拒且版本不变")
	}
	r2, _ := LoadRules(path)
	if r2.Count() != 1 {
		t.Fatal("重启后应从缓存加载")
	}
}

func TestMachineConfigRoundTrip(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "machine.json")
	if _, err := LoadMachine(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("不存在应 ErrNotExist: %v", err)
	}
	m := MachineConfig{HubURL: "http://127.0.0.1:8787", MachineID: "m1", Secret: "s", Name: "mac"}
	if err := SaveMachine(path, m); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("权限 %v", st.Mode().Perm())
	}
	got, err := LoadMachine(path)
	if err != nil || got != m {
		t.Fatalf("LoadMachine = %+v, %v", got, err)
	}
	var raw map[string]string
	b, _ := os.ReadFile(path)
	json.Unmarshal(b, &raw)
	if raw["hub_url"] == "" || raw["secret"] == "" {
		t.Fatalf("字段名应为 snake_case: %v", raw)
	}
}

func TestConfigHasHubFields(t *testing.T) {
	c := ConfigIn("/x")
	if c.MachinePath != "/x/machine.json" || c.HubGrace != 30*time.Second || c.AgentVersion != "dev" {
		t.Fatalf("Config = %+v", c)
	}
}

// 以下为控制者决定追加的契约测试。

func TestPendingListSortedCopy(t *testing.T) {
	p := newPending()
	now := time.Now()
	p.Add(&PendingRequest{ID: "b", Created: now.Add(time.Second)})
	p.Add(&PendingRequest{ID: "c", Created: now.Add(2 * time.Second)})
	p.Add(&PendingRequest{ID: "a", Created: now})
	list := p.List()
	if len(list) != 3 || list[0].ID != "a" || list[1].ID != "b" || list[2].ID != "c" {
		t.Fatalf("应按 Created 升序: %+v", list)
	}
	list[0].ID = "changed"
	if again := p.List(); again[0].ID != "a" {
		t.Fatal("List 应返回副本，改返回值不影响表")
	}
}

func TestAuditSinkCanBeCleared(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	sink := &recordingSink{}
	env.srv.SetAuditSink(sink)
	roundTrip(t, env.cfg.SocketPath, event("done", "s1", "one"))
	env.srv.SetAuditSink(nil)
	roundTrip(t, env.cfg.SocketPath, event("done", "s1", "two"))
	if n := sink.count(); n != 1 {
		t.Fatalf("清掉转发目标后不应再转发: %d", n)
	}
	if entries := readAudit(t, env.cfg.AuditPath); len(entries) != 2 {
		t.Fatalf("本地审计照写: %+v", entries)
	}
}

func TestSetDeciderReplacesDecider(t *testing.T) {
	dir := shortTempDir(t)
	cfg := ConfigIn(filepath.Join(dir, "h"))
	cfg.EnsureDirs()
	rules, _ := LoadRules(cfg.RulesPath)
	audit, _ := OpenAudit(cfg.AuditPath)
	defer audit.Close()
	srv := New(cfg, rules, audit, LocalDecider{}, log.New(os.Stderr, "test ", 0))
	srv.SetDecider(fixedDecider{out: Outcome{Action: "deny", Reason: "换过了", DecidedBy: "user"}})
	repo := filepath.Join(dir, "repo")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	resp := srv.handlePermission(t.Context(), permission(repo, "curl https://x.y"))
	if resp.Decision != "deny" || resp.Reason != "换过了" {
		t.Fatalf("SetDecider 后应使用新的决定来源: %+v", resp)
	}
}

// 用户决定 A：HubGrace 到期交回终端时，钩子收 passthrough，本机审计记 expired/hub_unreachable。
func TestHubUnreachableAuditsExpired(t *testing.T) {
	for _, action := range []string{"passthrough", "allow"} {
		env := startServer(t, fixedDecider{out: Outcome{Action: action, Reason: "连不上 hub，交回终端处理", DecidedBy: "hub_unreachable"}}, nil)
		var resp proto.Response
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl https://x.y")), &resp)
		if resp.Decision != "passthrough" {
			t.Fatalf("Action=%q：hub 不可达只能交回终端: %+v", action, resp)
		}
		entries := readAudit(t, env.cfg.AuditPath)
		if len(entries) != 1 || entries[0].State != "expired" || entries[0].Action != "passthrough" || entries[0].DecidedBy != "hub_unreachable" {
			t.Fatalf("Action=%q 审计应记 expired/hub_unreachable: %+v", action, entries)
		}
	}
}

// 用户决定 C：手机备注只进审计，钩子拿不到。
func TestNoteGoesToAuditOnly(t *testing.T) {
	const note = "别推 main，先开 PR"
	env := startServer(t, fixedDecider{out: Outcome{Action: "deny", Reason: "已在手机上拒绝", Note: note, DecidedBy: "user"}}, nil)
	line := roundTrip(t, env.cfg.SocketPath, permission(env.repo, "curl https://x.y"))
	if strings.Contains(string(line), "PR") {
		t.Fatalf("钩子不应收到备注: %s", line)
	}
	var resp proto.Response
	json.Unmarshal(line, &resp)
	if resp.Decision != "deny" || resp.Reason != "已在手机上拒绝" {
		t.Fatalf("答复: %+v", resp)
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 1 || entries[0].Note != note || entries[0].State != "denied" || entries[0].Reason != "已在手机上拒绝" {
		t.Fatalf("审计应带备注: %+v", entries)
	}
	b, _ := os.ReadFile(env.cfg.AuditPath)
	if !strings.Contains(string(b), `"note":`) {
		t.Fatalf("审计 JSON 字段名应为 note: %s", b)
	}
}

func ctlStatus(t *testing.T, env *testEnv) *proto.Status {
	t.Helper()
	var reply proto.ControlReply
	json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control("status")), &reply)
	if !reply.OK || reply.Status == nil {
		t.Fatalf("status: %+v", reply)
	}
	return reply.Status
}

func readState(t *testing.T, path string) map[string]bool {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]bool
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("state.json: %s", b)
	}
	return m
}

// 用户决定 E：本机暂停与 hub 暂停分开存，生效值取或，任一方都不能解除另一方的暂停。
func TestLocalAndHubPauseAreSeparate(t *testing.T) {
	env := startServer(t, LocalDecider{}, nil)
	git := func() string {
		var resp proto.Response
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, permission(env.repo, "git status")), &resp)
		return resp.Decision
	}
	ctl := func(op string) {
		var reply proto.ControlReply
		json.Unmarshal(roundTrip(t, env.cfg.SocketPath, control(op)), &reply)
		if !reply.OK {
			t.Fatalf("%s: %+v", op, reply)
		}
	}
	// hub 未变化时什么都不写
	if err := env.srv.SetHubPaused(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(env.cfg.StatePath); !os.IsNotExist(err) {
		t.Fatalf("hub 总开关没变时不应写 state.json: %v", err)
	}

	// 本机暂停后 hub 说未暂停，不解除
	ctl("pause")
	if err := env.srv.SetHubPaused(false); err != nil {
		t.Fatal(err)
	}
	if st := ctlStatus(t, env); !st.Paused || !st.LocalPaused || st.HubPaused || git() != "passthrough" {
		t.Fatalf("本机暂停不应被 hub 的未暂停解除: %+v", st)
	}
	ctl("resume")
	if st := ctlStatus(t, env); st.Paused || git() != "allow" {
		t.Fatalf("本机恢复后应放行: %+v", st)
	}

	// hub 暂停时本机 resume 不解除
	if err := env.srv.SetHubPaused(true); err != nil {
		t.Fatal(err)
	}
	if m := readState(t, env.cfg.StatePath); m["paused"] || !m["hub_paused"] {
		t.Fatalf("state.json 应分开存两字段: %v", m)
	}
	ctl("resume")
	if st := ctlStatus(t, env); !st.Paused || st.LocalPaused || !st.HubPaused || git() != "passthrough" {
		t.Fatalf("hub 暂停时本机 resume 不应解除: %+v", st)
	}

	// 两者都暂停，要两者都解除才放行
	ctl("pause")
	if st := ctlStatus(t, env); !st.Paused || !st.LocalPaused || !st.HubPaused {
		t.Fatalf("两者都暂停: %+v", st)
	}
	if m := readState(t, env.cfg.StatePath); !m["paused"] || !m["hub_paused"] {
		t.Fatalf("state.json: %v", m)
	}
	if err := env.srv.SetHubPaused(false); err != nil {
		t.Fatal(err)
	}
	if git() != "passthrough" {
		t.Fatal("本机仍暂停时 hub 解除不应放行")
	}
	ctl("resume")
	if st := ctlStatus(t, env); st.Paused || st.LocalPaused || st.HubPaused || git() != "allow" {
		t.Fatalf("两者都解除后应放行: %+v", st)
	}
	if m := readState(t, env.cfg.StatePath); m["paused"] || m["hub_paused"] {
		t.Fatalf("state.json: %v", m)
	}

	var hubEntries []AuditEntry
	for _, e := range readAudit(t, env.cfg.AuditPath) {
		if e.Kind == "control" && e.DecidedBy == "hub" {
			hubEntries = append(hubEntries, e)
		}
	}
	if len(hubEntries) != 2 || hubEntries[0].Message != "pause" || hubEntries[1].Message != "resume" {
		t.Fatalf("hub 总开关只在变化时写审计（decided_by hub）: %+v", hubEntries)
	}
}

func TestStateFileCarriesHubPaused(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "state.json")
	for _, want := range []state{{Paused: true, HubPaused: false}, {Paused: false, HubPaused: true}, {Paused: true, HubPaused: true}} {
		if err := saveState(path, want); err != nil {
			t.Fatal(err)
		}
		got, err := loadState(path)
		if err != nil || got != want {
			t.Fatalf("往返: %+v -> %+v, %v", want, got, err)
		}
	}
	// 第 2 份计划写的旧格式只有 paused
	os.WriteFile(path, []byte(`{"paused":true}`), 0o600)
	if got, err := loadState(path); err != nil || got != (state{Paused: true}) {
		t.Fatalf("旧格式应能读: %+v, %v", got, err)
	}

	env := startServer(t, LocalDecider{}, nil)
	os.WriteFile(env.cfg.StatePath, []byte(`{"paused":false,"hub_paused":true}`), 0o600)
	if err := env.srv.LoadState(); err != nil {
		t.Fatal(err)
	}
	if st := ctlStatus(t, env); !st.Paused || st.LocalPaused || !st.HubPaused || !env.srv.rules.Paused() {
		t.Fatalf("启动时 hub 暂停应生效: %+v", st)
	}
}

func TestSetHubPausedPersistFailure(t *testing.T) {
	env := startServer(t, LocalDecider{}, func(c *Config) { c.StatePath = filepath.Join(c.Dir, "missing", "state.json") })
	err := env.srv.SetHubPaused(true)
	if err == nil || !strings.Contains(err.Error(), "未能持久化") {
		t.Fatalf("落盘失败应返回错误: %v", err)
	}
	if !env.srv.rules.Paused() {
		t.Fatal("落盘失败时内存里仍应已暂停")
	}
	if err := env.srv.SetHubPaused(false); err == nil || env.srv.rules.Paused() {
		t.Fatalf("hub 解除落盘失败也应切换并报错: err=%v paused=%v", err, env.srv.rules.Paused())
	}
	entries := readAudit(t, env.cfg.AuditPath)
	if len(entries) != 2 || entries[0].DecidedBy != "hub" || !strings.Contains(entries[0].Reason, "未能持久化") {
		t.Fatalf("落盘失败也应写审计: %+v", entries)
	}
}

func TestRulesReplaceBadSetKeepsEngineAndFile(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, _ := LoadRules(path)
	before, _ := os.ReadFile(path)
	bad := policy.RuleSet{Version: 1, Rules: []policy.Rule{{ID: "x", Source: "robot", Action: "allow"}}}
	if err := r.Replace(bad, 3); err == nil {
		t.Fatal("坏规则集应被拒")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) || r.Count() != 2 || r.Version() != 0 {
		t.Fatalf("坏规则集不应改文件或引擎: count=%d v=%d", r.Count(), r.Version())
	}
	ents, _ := os.ReadDir(filepath.Dir(path))
	if len(ents) != 1 {
		t.Fatalf("不应留下临时文件: %v", ents)
	}
	// 未暂停时替换后也保持未暂停
	good, _ := policy.ParseRuleSet([]byte("version: 1\nrules: []\n"))
	if err := r.Replace(good, 4); err != nil || r.Paused() || r.Engine().Paused() {
		t.Fatalf("替换应保持未暂停: %v", err)
	}
}

func TestRulesReplaceNormalizesOnlyEngine(t *testing.T) {
	base := shortTempDir(t)
	linkProj, realProj := symlinkedProject(t, base)
	path := filepath.Join(base, "rules.yaml")
	r, _ := LoadRules(path)
	glob := linkProj + "/secret/**"
	rs, err := policy.ParseRuleSet([]byte("version: 1\nrules:\n  - id: deny-secret\n    source: user\n    scope: global\n    match: {payload: file_write, path_glob: \"" + glob + "\"}\n    action: deny\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Replace(rs, 2); err != nil {
		t.Fatal(err)
	}
	if rs.Rules[0].Match.PathGlob != glob {
		t.Fatalf("Replace 不应改调用方的规则集: %q", rs.Rules[0].Match.PathGlob)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), realProj) || !strings.Contains(string(b), glob) {
		t.Fatalf("缓存应是 hub 的原样规则:\n%s", b)
	}
	target := filepath.Join(realProj, "secret", "a.txt")
	d := r.Engine().Evaluate(policy.Payload{Type: policy.PayloadFileWrite, Path: target, Cwd: realProj}, policy.Context{ProjectRoot: realProj, HomeDir: "/h"})
	if d.Action != policy.ActionDeny {
		t.Fatalf("引擎里的规则应按真实路径改写: %+v", d)
	}
}

func TestReloadResetsRulesVersion(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "rules.yaml")
	r, _ := LoadRules(path)
	rs, _ := policy.ParseRuleSet([]byte("version: 1\nrules: []\n"))
	if err := r.Replace(rs, 5); err != nil {
		t.Fatal(err)
	}
	if err := r.Reload(); err != nil {
		t.Fatal(err)
	}
	if r.Version() != 0 {
		t.Fatalf("从文件加载后版本应为 0: %d", r.Version())
	}
}

func TestLoadMachineRejectsBadConfig(t *testing.T) {
	const secret = "TOPSECRETVALUE"
	path := filepath.Join(shortTempDir(t), "machine.json")
	for _, u := range []string{"", "ftp://h", "127.0.0.1:8787", "http://", "http:///x", "ws://h:1", "://bad"} {
		b, _ := json.Marshal(map[string]string{"hub_url": u, "machine_id": "m1", "secret": secret, "name": "mac"})
		os.WriteFile(path, b, 0o600)
		_, err := LoadMachine(path)
		if err == nil {
			t.Fatalf("hub_url %q 应被拒", u)
		}
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), secret) {
			t.Fatalf("hub_url %q 的错误不对或含密钥: %v", u, err)
		}
		if err := SaveMachine(path, MachineConfig{HubURL: u, MachineID: "m1", Secret: secret}); err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("SaveMachine 应拒绝 hub_url %q 且错误不含密钥: %v", u, err)
		}
	}
	for _, raw := range []string{`{"hub_url":"https://h","secret":"` + secret, `{"hub_url":"https://h","secret":42}`, `[]`} {
		os.WriteFile(path, []byte(raw), 0o600)
		_, err := LoadMachine(path)
		if err == nil || errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), secret) {
			t.Fatalf("损坏的 machine.json 应报错且不含密钥: %q -> %v", raw, err)
		}
	}
	if err := SaveMachine(path, MachineConfig{HubURL: "https://hub.example:8443", MachineID: "m1", Secret: secret}); err != nil {
		t.Fatal(err)
	}
	if m, err := LoadMachine(path); err != nil || m.HubURL != "https://hub.example:8443" {
		t.Fatalf("https 应可用: %+v %v", m, err)
	}
}

func TestSaveMachineIsAtomic(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "machine.json")
	m := MachineConfig{HubURL: "http://127.0.0.1:8787", MachineID: "m1", Secret: "s", Name: "mac"}
	os.WriteFile(path, []byte("old"), 0o644)
	os.Chmod(path, 0o644)
	before, _ := os.Stat(path)
	if err := SaveMachine(path, m); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if os.SameFile(before, after) || after.Mode().Perm() != 0o600 {
		t.Fatalf("应经临时文件 rename 替换且为 0600: %v", after.Mode().Perm())
	}
	// rename 失败（目标是目录）时不留下临时文件
	blocked := filepath.Join(dir, "blocked")
	os.Mkdir(blocked, 0o700)
	os.WriteFile(filepath.Join(blocked, "keep"), nil, 0o600)
	if err := SaveMachine(blocked, m); err == nil {
		t.Fatal("目标是非空目录时应失败")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("失败后不应留下临时文件: %s", e.Name())
		}
	}
}
