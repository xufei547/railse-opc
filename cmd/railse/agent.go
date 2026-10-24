package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/railse/railse-opc/internal/agent"
	"github.com/railse/railse-opc/internal/hookclient"
	"github.com/railse/railse-opc/internal/hub"
	"github.com/railse/railse-opc/internal/proto"
)

const agentUsage = `用法:
  railse agent run [--dir 目录] [--foreground]   启动守护进程（前台运行；有 machine.json 时接 hub；开机自启用 railse setup autostart）
  railse agent status|pause|resume|reload [--dir 目录]
  railse agent enroll --hub URL --token 接入码 [--name 名字] [--dir 目录] [--force]   接入 hub，写 machine.json`

// agentRunContext 给 agent run 的运行期：SIGINT、SIGTERM 时取消。是变量只为测试能替换。
var agentRunContext = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// agentFlags 是各 agent 子命令认的标志。
var agentFlags = map[string][]string{
	"run":    {"dir", "foreground"},
	"status": {"dir"}, "pause": {"dir"}, "resume": {"dir"}, "reload": {"dir"},
	"enroll": {"dir", "hub", "token", "name", "force"},
}

// runAgent 处理 agent 子命令：run 跑守护进程，enroll 接入 hub，其余经 socket 发本机管理命令。
func runAgent(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, agentUsage)
		return 2
	}
	allowed, ok := agentFlags[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "未知的 agent 子命令 %q\n\n%s\n", args[0], agentUsage)
		return 2
	}
	fs := flag.NewFlagSet("agent "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "配置目录，默认 $RAILSE_HOME 或 ~/.railse")
	foreground := fs.Bool("foreground", false, "日志同时写到 stderr")
	hubURL := fs.String("hub", "", "hub 地址，如 https://hub.example.com")
	token := fs.String("token", "", "railse hub token 生成的接入码")
	name := fs.String("name", "", "本机在 hub 里的名字，默认主机名")
	force := fs.Bool("force", false, "覆盖已有的 machine.json")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "多余的参数 %q\n%s\n", fs.Args(), agentUsage)
		return 2
	}
	if bad := foreignFlags(fs, allowed); bad != "" {
		fmt.Fprintf(stderr, "railse agent %s 不认 %s\n%s\n", args[0], bad, agentUsage)
		return 2
	}
	var cfg agent.Config
	if *dir != "" {
		cfg = agent.ConfigIn(*dir)
	} else {
		c, err := agent.DefaultConfig()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		cfg = c
	}
	switch args[0] {
	case "run":
		ctx, stop := agentRunContext()
		defer stop()
		if err := runAgentServe(ctx, cfg, *foreground, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	case "status", "pause", "resume", "reload":
		reply, err := hookclient.Client{SocketPath: cfg.SocketPath, ConnectTimeout: 2 * time.Second}.Control(args[0])
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if !reply.OK {
			fmt.Fprintln(stderr, "失败:", reply.Error)
			return 1
		}
		if args[0] == "status" && reply.Status != nil {
			st := reply.Status
			fmt.Fprintf(stdout, "%s\nrules: %d\npending: %d\nmachine: %s\n%s\nrules_path: %s\nuptime: %s\n", pausedLine(st), st.Rules, st.Pending, st.MachineID, hubLine(st), st.RulesPath, st.Uptime)
			return 0
		}
		fmt.Fprintln(stdout, "ok")
		// pause/resume 的回复带状态：本机已恢复但 hub 仍暂停时，自动审批仍未生效，要说清楚。
		if args[0] == "resume" && reply.Status != nil && reply.Status.HubPaused {
			fmt.Fprintln(stdout, "本机已恢复，hub 仍暂停：自动审批仍未生效，需在 hub 上恢复")
		}
		return 0
	case "enroll":
		if err := agentEnroll(cfg, *hubURL, *token, *name, *force, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	return 2 // 到不了：子命令已在开头核对过
}

// hubLine 显示到 hub 的连接：none（未接入；旧版守护进程不报时也按 none）、connecting/connected 加地址；
// disabled（连续被 hub 以 401 拒绝）时说明怎么恢复。
func hubLine(st *proto.Status) string {
	switch st.Hub {
	case "", "none":
		return "hub: none"
	case "disabled":
		return fmt.Sprintf("hub: disabled %s（本机密钥被拒，请 railse agent enroll --force 后重启守护进程）", st.HubURL)
	}
	return fmt.Sprintf("hub: %s %s", st.Hub, st.HubURL)
}

// pausedLine 显示总开关的生效值；暂停时在括号里注明来源：本机（local）、hub，或两者都有。
func pausedLine(st *proto.Status) string {
	if !st.Paused {
		return "paused: false"
	}
	switch {
	case st.LocalPaused && st.HubPaused:
		return "paused: true (local+hub)"
	case st.LocalPaused:
		return "paused: true (local)"
	case st.HubPaused:
		return "paused: true (hub)"
	}
	return "paused: true"
}

const (
	enrollTimeout  = 15 * time.Second // 接入请求的总上限
	enrollReplyMax = 64 << 10         // 读 hub 答复的字节上限
	hubTextMax     = 200              // 转述 hub 报错文案的字符上限
)

// agentEnroll 用一次性接入码向 hub 换取本机长期密钥，写 machine.json。参数与名字在联系 hub 之前校验；
// 接入码只放在请求体里发给 --hub 指定的地址（不跟随重定向），任何输出都不带接入码与密钥。
func agentEnroll(cfg agent.Config, hubURL, token, name string, force bool, stdout, stderr io.Writer) error {
	if hubURL == "" || token == "" {
		return errors.New("需要 --hub 与 --token（接入码由 hub 上的 railse hub token 生成）")
	}
	u, base, err := parseHubURL(hubURL)
	if err != nil {
		return err
	}
	origin := u.Scheme + "://" + u.Host
	if name == "" {
		name = cfg.MachineID // 主机名
	}
	if err := checkDisplayName(name); err != nil {
		return fmt.Errorf("名字 %w（用 --name 另取一个）", err)
	}
	if u.Scheme == "http" && !isLoopbackHost(u.Hostname()) {
		fmt.Fprintln(stderr, "警告: hub 地址是 http://，明文传输密钥，只适合本机或可信内网；公网请用 https://")
	}
	if _, err := os.Lstat(cfg.MachinePath); err == nil && !force {
		return fmt.Errorf("%s 已存在，本机已接入过；要换 hub 或重新接入请加 --force", cfg.MachinePath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]string{"token": token, "name": name, "home_dir": cfg.HomeDir}) // map[string]string 编码不会失败
	client := &http.Client{Timeout: enrollTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Post(base+"/api/enroll", "application/json", bytes.NewReader(body))
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // 去掉 *url.Error 带的完整地址，只留原因
		}
		return fmt.Errorf("连接 hub %s 失败: %w", origin, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, enrollReplyMax+1))
	if err != nil {
		return fmt.Errorf("读 hub %s 的答复: %w", origin, err)
	}
	switch {
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return fmt.Errorf("hub 返回重定向（HTTP %d），不跟随：接入码与密钥只发给 --hub 指定的地址，请把 --hub 改成最终地址", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("hub 拒绝接入（HTTP %d）%s", resp.StatusCode, hubErrorText(data, token))
	}
	var out struct {
		MachineID string `json:"machine_id"`
		Secret    string `json:"secret"`
	}
	if len(data) > enrollReplyMax || json.Unmarshal(data, &out) != nil || out.MachineID == "" || out.Secret == "" || checkDisplayName(out.MachineID) != nil {
		return errors.New("hub 的答复不完整或不合规，没有写 machine.json")
	}
	m := agent.MachineConfig{HubURL: base, MachineID: out.MachineID, Secret: out.Secret, Name: name}
	if err := agent.SaveMachine(cfg.MachinePath, m); err != nil {
		return fmt.Errorf("写 %s: %w", cfg.MachinePath, err)
	}
	fmt.Fprintf(stdout, "已接入 hub %s，机器 id %s，名字 %s\n已写入 %s；重启 railse agent run 后生效\n", origin, out.MachineID, name, cfg.MachinePath)
	if agent.HasLocalRules(cfg.RulesPath) {
		fmt.Fprintf(stdout, "注意：首次连上 hub 时本机 rules.yaml 会被 hub 规则覆盖（会自动备份），如有手写规则请先导入。\n"+
			"  本机规则 %s，备份为同目录的 rules.local-<时间>.yaml；覆盖后其中手写的规则（例如 deny）不再生效\n"+
			"  导入：POST %s/api/rules/import（整套替换 hub 的规则，见 docs/hub.md）\n", cfg.RulesPath, origin)
	}
	return nil
}

// parseHubURL 校验 --hub：只收 http、https，要有主机名，不带 user:pass（凭据只走接入码与机器密钥）、查询与片段。
// 返回解析结果与去掉末尾 / 的基地址。错误信息不带原文（里面可能有密码）。
func parseHubURL(raw string) (*url.URL, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Opaque != "" || u.Hostname() == "" {
		return nil, "", errors.New("--hub 必须是 http:// 或 https:// 开头、带主机名的地址")
	}
	if u.User != nil {
		return nil, "", errors.New("--hub 不能带用户名或密码")
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, "", errors.New("--hub 不能带 ? 查询或 # 片段")
	}
	return u, u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"), nil
}

// isLoopbackHost 报告主机是否只在本机：localhost、*.localhost（RFC 6761）、127.0.0.0/8、::1。
func isLoopbackHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && a.Unmap().IsLoopback()
}

// checkDisplayName 按 hub 接入时的口径检查名字（机器名、hub 发来的机器 id）：至多 hub.MaxNameLen 字节，且 hub.ValidName。
func checkDisplayName(s string) error {
	if len(s) > hub.MaxNameLen {
		return fmt.Errorf("至多 %d 字节", hub.MaxNameLen)
	}
	if !hub.ValidName(s) {
		return errors.New("必须是合法 UTF-8，且不能含控制字符或不可见的格式字符")
	}
	return nil
}

// hubErrorText 取 hub 报错 JSON 里的 error 文案，去掉控制字符与格式字符、把接入码换成占位、截到 hubTextMax 字符，
// 前面加“: ”；不是 JSON 或没有文案时返回空串（不转述任意响应体）。
func hubErrorText(data []byte, token string) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &e) != nil || e.Error == "" {
		return ""
	}
	msg := strings.Map(func(c rune) rune {
		if unicode.IsControl(c) || unicode.Is(unicode.Cf, c) {
			return -1
		}
		return c
	}, strings.ToValidUTF8(e.Error, ""))
	msg = strings.ReplaceAll(msg, token, "<接入码>")
	if r := []rune(msg); len(r) > hubTextMax {
		msg = string(r[:hubTextMax]) + "…"
	}
	return ": " + msg
}

// hubOrigin 给出日志里显示的 hub 地址：只有 scheme://host。
func hubOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "（无效的 hub 地址）"
	}
	return u.Scheme + "://" + u.Host
}

// runAgentServe 跑守护进程直到 ctx 取消。SIGHUP 重载规则。
// 日志只写路径与计数，绝不打印环境变量。
func runAgentServe(ctx context.Context, cfg agent.Config, foreground bool, stderr io.Writer) error {
	// 家目录为空时 ~/ 开头的敏感路径规则会被跳过（fail open），宁可不启动。
	if cfg.HomeDir == "" {
		return errors.New("找不到家目录，拒绝启动：家目录为空会让 ~/ 开头的敏感路径规则失效")
	}
	if err := cfg.EnsureDirs(); err != nil {
		return err
	}
	cfg.AgentVersion = version
	logFile, err := os.OpenFile(cfg.LogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	// O_CREATE 的 0600 只对新建文件生效，已有日志文件在这里收紧。
	if err := logFile.Chmod(0o600); err != nil {
		return fmt.Errorf("收紧日志文件权限: %w", err)
	}
	var w io.Writer = logFile
	if foreground {
		w = io.MultiWriter(logFile, stderr)
	}
	logger := log.New(w, "railse-agent ", log.LstdFlags)
	rules, err := agent.LoadRulesWithLogger(cfg.RulesPath, logger)
	if err != nil {
		return err
	}
	audit, err := agent.OpenAudit(cfg.AuditPath)
	if err != nil {
		return err
	}
	defer audit.Close()
	// machine.json 决定接不接 hub：不存在就与第 2 份计划一样本地闭环；读不了或损坏就拒绝启动，
	// 不静默退回本地模式（用户会以为接着 hub）。LoadMachine 的错误不带密钥。
	var machine *agent.MachineConfig
	switch m, err := agent.LoadMachine(cfg.MachinePath); {
	case errors.Is(err, os.ErrNotExist):
		logger.Printf("未接入 hub（没有 %s），引擎定不了的请求交回终端", cfg.MachinePath)
	case err != nil:
		logger.Printf("读 machine.json 失败，拒绝启动: %v", err)
		return fmt.Errorf("读 machine.json 失败，拒绝启动（修好它、删掉它改回本地模式，或 railse agent enroll --force 重新接入）: %w", err)
	default:
		machine = &m
		cfg.MachineID = m.MachineID // hub 学到的 scope: machine 规则用 hub 的机器 id，本机引擎必须一致
	}
	srv := agent.New(cfg, rules, audit, agent.LocalDecider{}, logger)
	var client *agent.HubClient
	if machine != nil {
		// 决定来源与审计转发都要在 Listen 之前装好
		client = agent.NewHubClient(cfg, *machine, rules, srv.Pending(), srv.SetHubPaused, logger)
		decider := &agent.HubDecider{Client: client, Grace: cfg.HubGrace, Log: logger}
		srv.SetDecider(decider)
		srv.SetAuditSink(client)
		srv.SetHubStatus(func() (string, string) { return client.State(), machine.HubURL })
		// 送达回执在后台发：在审批服务停下（下面登记的 srv.Close，后登记先执行）之后、关审计与日志之前等它们发完
		defer decider.Wait()
	}
	if err := srv.Listen(); err != nil {
		return err
	}
	// 只在 Listen 成功后才登记 Close：失败时 socket 文件可能属于已在运行的守护进程。
	defer srv.Close()
	if err := srv.LoadState(); err != nil {
		logger.Printf("恢复暂停状态失败，已按暂停启动（railse agent resume 可恢复）: %v", err)
	}
	if machine == nil && srv.HubPaused() {
		// 取消接入后不应残留 hub 暂停：没有 hub 能再解除它，本机会一直暂停
		logger.Printf("未接入 hub，清除上次残留的 hub 暂停")
		if err := srv.SetHubPaused(false); err != nil {
			logger.Printf("清除残留的 hub 暂停: %v", err)
		}
	}
	// hub 客户端在 LoadState 之后才开始收帧：welcome 设的 hub 暂停不能被磁盘上的旧值盖掉。它与 Serve 共用同一个关停 ctx
	// （派生自 ctx），返回前等它退出：它会写审计、改 state.json。连续被 hub 以 401 拒绝而停用时 Run 自己返回并记日志（要重新 enroll），
	// 这里不当致命错误、也不重启：HubDecider 见停用即交回终端，守护进程照常服务。
	hubCtx, stopHub := context.WithCancel(ctx)
	hubDone := make(chan struct{})
	if client != nil {
		logger.Printf("接入 hub %s，机器 %s", hubOrigin(machine.HubURL), machine.MachineID)
		go func() {
			defer close(hubDone)
			client.Run(hubCtx)
			if hubCtx.Err() == nil {
				logger.Printf("hub 客户端已停止；守护进程继续运行，引擎定不了的请求一律交回终端")
			}
		}()
	} else {
		close(hubDone)
	}
	defer func() {
		stopHub()
		<-hubDone
	}()
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	// 返回前先停掉并等完重载 goroutine，免得它在日志文件关闭后还在写。
	hupCtx, stopHup := context.WithCancel(ctx)
	hupDone := make(chan struct{})
	go func() {
		defer close(hupDone)
		for {
			select {
			case <-hup:
				if err := rules.Reload(); err != nil {
					logger.Printf("SIGHUP 重载规则失败，保留旧规则: %v", err)
				} else {
					logger.Printf("SIGHUP 重载规则：%d 条", rules.Count())
				}
			case <-hupCtx.Done():
				return
			}
		}
	}()
	defer func() {
		stopHup()
		<-hupDone
	}()
	logger.Printf("监听 %s，规则 %d 条，机器 %s", cfg.SocketPath, rules.Count(), cfg.MachineID)
	return srv.Serve(ctx)
}
