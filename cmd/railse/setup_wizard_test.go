package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/hooksconf"
)

// countingReader 记下被读走了多少字节：用来断言某条路径根本没碰 stdin。
type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// fakeStdin 在本测试期间把向导的 stdin 换成内容为 input 的假输入，terminal 决定它算不算终端；结束时换回。
// 绝不读真实的 os.Stdin。
func fakeStdin(t *testing.T, terminal bool, input string) *countingReader {
	t.Helper()
	r := &countingReader{r: strings.NewReader(input)}
	oldIn, oldTerm := setupStdin, stdinIsTerminal
	setupStdin, stdinIsTerminal = r, func() bool { return terminal }
	t.Cleanup(func() { setupStdin, stdinIsTerminal = oldIn, oldTerm })
	return r
}

// 向导问到的三个问题的提示（各取开头一段）
const (
	askHubPrompt   = "hub 地址（"
	askTokenPrompt = "接入码（"
	askNamePrompt  = "本机在 hub 里的名字（"
)

// testExe 是测试程序本身的绝对路径：不给 --bin 时 setup all 把钩子与托管文件指向它。
func testExe(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if exe, err = filepath.Abs(exe); err != nil {
		t.Fatal(err)
	}
	return exe
}

// 终端里不带子命令跑 railse setup：依次问 hub 地址、接入码、名字（直接回车取主机名），再照 setup all 做完；
// 最后把 hub 地址框起来，并提示用 Safari 加到主屏幕。
func TestSetupWizardAsksThenRunsSetupAll(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	t.Setenv("RAILSE_HOME", f.dir)
	t.Chdir(f.proj)
	fakeStdin(t, true, h.http.URL+"\n"+newEnrollToken(t, h)+"\n\n")
	var out, errOut bytes.Buffer
	if code := run([]string{"setup"}, &out, &errOut); code != 0 {
		t.Fatalf("railse setup: %d\n%s\n%s", code, out.String(), errOut.String())
	}
	host := agent.ConfigIn(f.dir).MachineID
	m, err := agent.LoadMachine(filepath.Join(f.dir, "machine.json"))
	if err != nil || m.Name != host || m.HubURL != h.http.URL {
		t.Fatalf("应已用答的地址接入、名字取主机名 %q: %+v %v", host, m, err)
	}
	exe := testExe(t)
	for _, p := range []string{hooksconf.ClaudeUserPath(f.home), hooksconf.GeminiUserPath(f.home), hooksconf.CodexPath(f.home)} {
		if ins, _ := hooksconf.Inspect(readFile(t, p), exe); len(ins.Events) == 0 {
			t.Fatalf("应照常装用户级的钩子 %s", p)
		}
	}
	if job := string(readFile(t, f.agentJobPath())); !strings.Contains(job, f.dir) {
		t.Fatalf("应照常托管守护进程:\n%s", job)
	}
	s := out.String()
	for _, want := range []string{askHubPrompt, askTokenPrompt, "railse hub token", askNamePrompt + "直接回车用 " + host, "已接入 hub", "已托管"} {
		if !strings.Contains(s, want) {
			t.Fatalf("输出缺 %q:\n%s", want, s)
		}
	}
	if strings.Index(s, askNamePrompt) > strings.Index(s, "== 1. ") {
		t.Fatalf("应先问完再开始第 1 步:\n%s", s)
	}
	bar := strings.Repeat("═", len(h.http.URL)+4)
	box := "下一步：在手机上打开\n╔" + bar + "╗\n║  " + h.http.URL + "  ║\n╚" + bar + "╝\n手机 Safari 打开后分享→添加到主屏幕，再从图标打开；登录后在设置里开启通知（见 docs/web.md）。\n"
	if !strings.HasSuffix(s, box) {
		t.Fatalf("最后应把 hub 地址框起来并提示加到主屏幕，想要结尾:\n%s\n得到:\n%s", box, s)
	}
}

// 不合格的答案打印原因后重问：hub 地址同 --hub 的校验（原因里不提用户没输入过的 --hub），非本机的 http:// 也不收；
// 接入码去掉首尾空白后不能为空；名字同 --name 的校验。答对的 hub 地址按 --hub 的口径规范化（去掉末尾 /），框里也是它。
func TestSetupWizardReasksBadAnswers(t *testing.T) {
	h := startTestHub(t)
	f := setupAllEnv(t)
	input := strings.Join([]string{
		"ftp://x", "https://u:s3cretpw@hub.example.com", "http://hub.example.com", " " + h.http.URL + "/ ",
		"   ", newEnrollToken(t, h),
		strings.Repeat("a", 129), "mac",
	}, "\n") + "\n"
	fakeStdin(t, true, input)
	var out, errOut bytes.Buffer
	if code := run(f.args("--no-autostart"), &out, &errOut); code != 0 {
		t.Fatalf("setup all: %d\n%s\n%s", code, out.String(), errOut.String())
	}
	s := out.String()
	for prompt, n := range map[string]int{askHubPrompt: 4, askTokenPrompt: 2, askNamePrompt: 2} {
		if got := strings.Count(s, prompt); got != n {
			t.Fatalf("%q 应问 %d 次，问了 %d 次:\n%s", prompt, n, got, s)
		}
	}
	for _, want := range []string{"hub 地址必须是 http:// 或 https://", "hub 地址不能带用户名", "明文传输密钥", "接入码不能为空", "名字至多 128 字节", "║  " + h.http.URL + "  ║"} {
		if !strings.Contains(s, want) {
			t.Fatalf("应说明为什么重问，缺 %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "--hub 必须") || strings.Contains(s, "--hub 不能") {
		t.Fatalf("向导的重问原因不该提 --hub:\n%s", s)
	}
	if strings.Contains(s+errOut.String(), "s3cretpw") {
		t.Fatalf("不该回显地址里的密码:\n%s\n%s", s, errOut.String())
	}
	m, err := agent.LoadMachine(filepath.Join(f.dir, "machine.json"))
	if err != nil || m.Name != "mac" || m.HubURL != h.http.URL {
		t.Fatalf("应用最后答对的值接入: %+v %v", m, err)
	}
}

// 任一问题读到 EOF（Ctrl-D）：退出 1，说已取消，一个文件都不写、什么命令都不跑。
func TestSetupWizardEOFCancels(t *testing.T) {
	h := startTestHub(t)
	f := setupAllEnv(t)
	tok := newEnrollToken(t, h)
	for _, input := range []string{"", h.http.URL + "\n", h.http.URL + "\n" + tok + "\n", h.http.URL + "\n" + tok + "\nmac"} {
		fakeStdin(t, true, input)
		var out, errOut bytes.Buffer
		if code := run(f.args(), &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "已取消，什么都没做") {
			t.Fatalf("%q: %d %q", input, code, errOut.String())
		}
		if strings.Contains(out.String(), "== 1.") {
			t.Fatalf("%q: 取消后不该开始任何一步: %q", input, out.String())
		}
	}
	for _, p := range []string{f.dir, filepath.Join(f.home, ".railse"), filepath.Join(f.proj, ".claude"), filepath.Join(f.proj, ".gemini"),
		filepath.Join(f.home, ".claude"), filepath.Join(f.home, ".gemini"), filepath.Join(f.home, ".codex")} {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Fatalf("取消后不该有 %s", p)
		}
	}
	if len(f.rec.calls) != 0 {
		t.Fatalf("取消后不该跑命令: %v", f.rec.calls)
	}
}

// stdin 不是终端（脚本、管道）：不问，照旧是用法错误、退出 2，也不读 stdin。
func TestSetupWizardNotTerminalKeepsUsageErrors(t *testing.T) {
	f := setupAllEnv(t)
	in := fakeStdin(t, false, "https://hub.example.com\ntok\n\n")
	var out, errOut bytes.Buffer
	if code := run([]string{"setup"}, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "railse setup all --hub URL") {
		t.Fatalf("railse setup 不在终端里应打印用法、退出 2: %d %q", code, errOut.String())
	}
	// 缺 --token 的用法错误与原来一样排在 --bin、--project 的检查之前：同时有坏的 --bin、--project 也照样退出 2
	nope := filepath.Join(f.home, "nope")
	for _, args := range [][]string{
		f.args("--hub", "https://hub.example.com"),
		append(f.args("--hub", "https://hub.example.com"), "--project", nope),
		append(f.args("--hub", "https://hub.example.com"), "--bin", nope),
	} {
		errOut.Reset()
		if code := run(args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "需要 --hub 与 --token") {
			t.Fatalf("%v: setup all 缺 --token 不在终端里应退出 2: %d %q", args, code, errOut.String())
		}
	}
	if in.n != 0 || out.Len() != 0 {
		t.Fatalf("不在终端里不该读 stdin（读了 %d 字节）、不该提问: %q", in.n, out.String())
	}
	if _, err := os.Stat(f.dir); !os.IsNotExist(err) {
		t.Fatal("用法错不该建数据目录")
	}
}

// setup all 给了一部分：只问缺的。给了 --hub 不问 hub 地址，给了 --name 不问名字。
func TestSetupWizardAsksOnlyMissing(t *testing.T) {
	h := startTestHub(t)
	f := setupAllEnv(t)
	fakeStdin(t, true, newEnrollToken(t, h)+"\n\n")
	var out, errOut bytes.Buffer
	if code := run(f.args("--hub", h.http.URL, "--no-autostart"), &out, &errOut); code != 0 {
		t.Fatalf("setup all --hub: %d\n%s\n%s", code, out.String(), errOut.String())
	}
	if s := out.String(); strings.Contains(s, askHubPrompt) || !strings.Contains(s, askTokenPrompt) || !strings.Contains(s, askNamePrompt) {
		t.Fatalf("给了 --hub 时只该问接入码与名字:\n%s", s)
	}
	// 重新接入（--force）、给了 --token 与 --name：只问 hub 地址
	fakeStdin(t, true, h.http.URL+"\n")
	out.Reset()
	if code := run(f.args("--token", newEnrollToken(t, h), "--name", "mbp", "--force", "--no-autostart"), &out, &errOut); code != 0 {
		t.Fatalf("setup all --token --name --force: %d\n%s\n%s", code, out.String(), errOut.String())
	}
	if s := out.String(); !strings.Contains(s, askHubPrompt) || strings.Contains(s, askTokenPrompt) || strings.Contains(s, askNamePrompt) {
		t.Fatalf("给了 --token、--name 时只该问 hub 地址:\n%s", s)
	}
	if m, err := agent.LoadMachine(filepath.Join(f.dir, "machine.json")); err != nil || m.Name != "mbp" {
		t.Fatalf("应用 --name 的名字重新接入: %+v %v", m, err)
	}
}

// 已接入（有 machine.json、没 --force）：railse setup 不提问，直接跑完（跳过接入），框里是 machine.json 里的地址。
func TestSetupWizardSkipsQuestionsWhenEnrolled(t *testing.T) {
	needUserAutostart(t)
	h := startTestHub(t)
	f := setupAllEnv(t)
	t.Setenv("RAILSE_HOME", f.dir)
	t.Chdir(f.proj)
	var out, errOut bytes.Buffer
	if code := run([]string{"agent", "enroll", "--hub", h.http.URL, "--token", newEnrollToken(t, h)}, &out, &errOut); code != 0 {
		t.Fatalf("enroll: %d %q", code, errOut.String())
	}
	in := fakeStdin(t, true, "https://other.example.com\ntok\nname\n")
	out.Reset()
	if code := run([]string{"setup"}, &out, &errOut); code != 0 {
		t.Fatalf("railse setup: %d\n%s\n%s", code, out.String(), errOut.String())
	}
	s := out.String()
	if in.n != 0 || strings.Contains(s, askHubPrompt) || strings.Contains(s, askTokenPrompt) || strings.Contains(s, askNamePrompt) {
		t.Fatalf("已接入时不该提问（读了 %d 字节）:\n%s", in.n, s)
	}
	if !strings.Contains(s, "已接入过") || !strings.Contains(s, "已托管") || !strings.Contains(s, "║  "+h.http.URL+"  ║") {
		t.Fatalf("应跳过接入、照常跑完，框里是 machine.json 的地址:\n%s", s)
	}
	// 从 railse setup 进来的人照抄得了：railse setup --force 是用法错误，要写成完整的 setup all 命令
	if !strings.Contains(s, "要换 hub 或重新接入：railse setup all --force\n") {
		t.Fatalf("重新接入的提示应是完整命令 railse setup all --force:\n%s", s)
	}
}

// root：先拒绝，不提问，一个字节都不读。
func TestSetupWizardRootRefusedBeforeAsking(t *testing.T) {
	f := setupAllEnv(t)
	in := fakeStdin(t, true, "https://hub.example.com\ntok\n\n")
	for _, args := range [][]string{nil, f.args("--hub", "https://hub.example.com")[2:]} {
		var out, errOut bytes.Buffer
		if code := runSetupAllAs("linux", 0, args, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "不要 sudo") {
			t.Fatalf("root %v: %d %q", args, code, errOut.String())
		}
		if out.Len() != 0 {
			t.Fatalf("root %v: 不该提问: %q", args, out.String())
		}
	}
	if in.n != 0 {
		t.Fatalf("root 时不该读 stdin，读了 %d 字节", in.n)
	}
}

// 一看就不对的输入（--bin 不能执行、项目目录不存在、--name 不合格）在提问之前挡住，免得答完才被拒。
func TestSetupWizardChecksInputsBeforeAsking(t *testing.T) {
	f := setupAllEnv(t)
	in := fakeStdin(t, true, "https://hub.example.com\ntok\n\n")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{append(f.args(), "--bin", filepath.Join(f.home, "nope")), "不存在"},
		{append(f.args(), "--project", filepath.Join(f.home, "nope")), "项目目录"},
		{append(f.args(), "--name", "a\tb"), "名字 必须是合法 UTF-8，且不能含控制字符或不可见的格式字符（用 --name 另取一个）"},
		{append(f.args(), "--name", strings.Repeat("a", 129)), "名字 至多 128 字节（用 --name 另取一个）"},
	} {
		var out, errOut bytes.Buffer
		if code := run(tc.args, &out, &errOut); code != 1 || out.Len() != 0 || !strings.Contains(errOut.String(), tc.want) {
			t.Fatalf("%v: %d %q %q", tc.args, code, out.String(), errOut.String())
		}
	}
	if in.n != 0 {
		t.Fatalf("输入不对时不该提问，读了 %d 字节", in.n)
	}
}

// 只看字符设备不够：/dev/null 也是字符设备（< /dev/null 时的 stdin），不能当终端去提问；
// 管道、普通文件更不算。stdinIsTerminal 本身（TestMain 换掉之前的那个）也按这个判 os.Stdin。
func TestIsTerminalRejectsNullPipeAndFile(t *testing.T) {
	null, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	if st, err := null.Stat(); err != nil || st.Mode()&os.ModeCharDevice == 0 {
		t.Fatalf("本例要的是字符设备 %s: %v", os.DevNull, err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	file, err := os.Create(filepath.Join(t.TempDir(), "in"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for name, f := range map[string]*os.File{os.DevNull: null, "管道": r, "普通文件": file} {
		if isTerminal(f) {
			t.Fatalf("%s 不该算终端", name)
		}
	}
	old := os.Stdin
	os.Stdin = null
	defer func() { os.Stdin = old }()
	if realStdinIsTerminal() {
		t.Fatal("stdin 是 /dev/null 时 stdinIsTerminal 不该算终端")
	}
}

// 框宽按地址的字符数（不是字节数）算，框里只放地址；地址为空时不画框。
func TestPrintPhoneStep(t *testing.T) {
	var b bytes.Buffer
	printPhoneStep(&b, "https://railse-hub.fly.dev")
	want := "\n下一步：在手机上打开\n" +
		"╔══════════════════════════════╗\n" +
		"║  https://railse-hub.fly.dev  ║\n" +
		"╚══════════════════════════════╝\n" +
		"手机 Safari 打开后分享→添加到主屏幕，再从图标打开；登录后在设置里开启通知（见 docs/web.md）。\n"
	if b.String() != want {
		t.Fatalf("想要:\n%s\n得到:\n%s", want, b.String())
	}
	b.Reset()
	printPhoneStep(&b, "https://例子.cn")
	if !strings.Contains(b.String(), "\n╔"+strings.Repeat("═", 17)+"╗\n║  https://例子.cn  ║\n╚"+strings.Repeat("═", 17)+"╝\n") {
		t.Fatalf("框宽应按字符数算:\n%s", b.String())
	}
	b.Reset()
	printPhoneStep(&b, "")
	if s := b.String(); strings.ContainsAny(s, "╔║╚") || !strings.Contains(s, "下一步") || !strings.Contains(s, "添加到主屏幕") {
		t.Fatalf("地址为空时不画框:\n%s", s)
	}
}
