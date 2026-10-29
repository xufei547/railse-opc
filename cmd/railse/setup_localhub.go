package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"time"

	"github.com/xufei547/railse-opc/internal/hub"
	"github.com/xufei547/railse-opc/internal/hub/store"
	"github.com/xufei547/railse-opc/internal/tailscale"
)

// 以下是变量只为测试能替换：找 tailscale 可执行文件、跑它（测试换成临时目录里的假脚本，绝不跑真的）。
var (
	tailscaleFind                  = func() (string, error) { return tailscale.Find(exec.LookPath, os.Stat, runtime.GOOS) }
	tailscaleRun  tailscale.Runner = tailscale.Exec
)

// localHubOpts 是 setupLocalHub 的输入，由 setup all --local-hub 给。
type localHubOpts struct {
	HubDir    string // hub 数据目录（绝对路径）
	Bin       string // railse 可执行文件（绝对路径），托管文件指向它
	Port      int    // 默认 8787
	PublicURL string // 用户给的 --public-url（自己的隧道）；空则用 Tailscale Funnel
	Terminal  bool   // stdin 是否终端（没密码时能不能问）
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
}

// hubJobRunning 问服务管理器 hub 的托管作业在不在跑（与 railse setup autostart --hub --status 同一个查询，写法同 agentJobRunning），
// 不看托管文件在不在。只读。是变量只为测试能替换。
var hubJobRunning = func(hubDir, bin string) bool {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return false
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	return autostartStatus(runtime.GOOS, autostartSpec{Kind: "hub", Home: home, UID: os.Getuid(), Dir: hubDir, Bin: bin}, false, io.Discard) == 0
}

// setupLocalHub 做第 0 步：定对外地址（Funnel 或 --public-url）→ 数据目录与密码 → 写 local-hub.json → 托管并启动 hub →
// 等它应答 → 生成接入码。返回描述、接入码（只交给 agentEnroll，不打印）与要在结尾“注意”里说的话（如对外地址变了）。
// 任何一步失败返回错误，调用方按 fail(步骤名) 处理；Funnel 没在管理台开通时返回 tailscale.ErrFunnelNotEnabled（含开通链接）。
// hub 只监听 127.0.0.1:<端口>，对外只经 Funnel（或用户自己的隧道）。输出里不带接入码与密码。
func setupLocalHub(goos string, o localHubOpts) (lh localHub, token string, notes []string, err error) {
	// 一看就不对的输入在动手之前挡住：不白开 Funnel、不白设密码
	if o.Port < 1 || o.Port > 65535 {
		return localHub{}, "", nil, fmt.Errorf("hub 端口 %d 不在 1..65535", o.Port)
	}
	if goos != "darwin" && goos != "linux" {
		return localHub{}, "", nil, errors.New("本机 hub 只支持 macOS（launchd）与 Linux（systemd）：要靠它们托管 hub")
	}
	listen := "127.0.0.1:" + strconv.Itoa(o.Port)
	lh = localHub{Port: o.Port}

	// 1. 对外地址。用户给的地址按 parseHubAddr 规范化（去尾斜杠），之后写文件、托管参数、比较“地址变了”都用它，重跑不会误报
	if o.PublicURL != "" {
		u, base, perr := parseHubAddr(o.PublicURL)
		if perr != nil || u.Scheme != "https" || base != "https://"+u.Host {
			return localHub{}, "", nil, fmt.Errorf("对外地址 %q 要是不带路径、查询的 https:// 地址，如 https://hub.example.com", o.PublicURL)
		}
		lh.PublicURL, lh.Provider = base, "custom"
		fmt.Fprintf(o.Stdout, "对外地址用你给的 %s（自己的隧道要把它转到 %s）\n", base, listen)
	} else {
		if lh.PublicURL, err = tailscaleFunnel(o.Port, o.Stdout); err != nil {
			return localHub{}, "", nil, err
		}
		lh.Provider = "tailscale"
		fmt.Fprintf(o.Stdout, "对外地址 %s（Tailscale Funnel 已把它转到 %s）\n", lh.PublicURL, listen)
	}

	// 2. 数据目录与网页密码（没设过的在终端里问；不是终端就停下，让用户先 railse hub password）
	if err := ensureHubDir(o.HubDir); err != nil {
		return localHub{}, "", nil, err
	}
	cfg := hub.DefaultConfig(o.HubDir)
	cfg.Listen = listen
	cfg.PublicURL = lh.PublicURL
	cfg.Version = version
	if cfg, err = cfg.Effective(); err != nil {
		return localHub{}, "", nil, err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return localHub{}, "", nil, err
	}
	defer st.Close()
	set, err := ensureHubPassword(cfg, st, o.Stdin, o.Terminal, o.Stdout, o.Stderr)
	if err != nil {
		return localHub{}, "", nil, err
	}
	if set {
		fmt.Fprintf(o.Stdout, "网页密码已设好（hub 数据目录 %s）\n", o.HubDir)
	} else {
		fmt.Fprintf(o.Stdout, "网页密码已设过，沿用（hub 数据目录 %s）\n", o.HubDir)
	}

	// 3. local-hub.json。“变了”：之前没有这个文件，或对外地址、端口与这次不同；变了的到第 4 步要重新托管。
	// 只有对外地址从 A 变成 B 才提醒手机（之前没有文件时手机上还没有图标）
	old, had, lerr := loadLocalHub(o.HubDir)
	if lerr != nil { // 坏文件：这次照新的设置重写，当作变了
		fmt.Fprintf(o.Stderr, "警告：%v；按这次的设置重写\n", lerr)
	}
	changed := !had || old.PublicURL != lh.PublicURL || old.Port != lh.Port
	if had && old.PublicURL != lh.PublicURL {
		note := fmt.Sprintf("对外地址从 %s 变成 %s：手机上旧的图标要删掉重新添加、重新开启通知", old.PublicURL, lh.PublicURL)
		fmt.Fprintln(o.Stdout, note)
		notes = append(notes, note)
	}
	if err := saveLocalHub(o.HubDir, lh); err != nil {
		return localHub{}, "", nil, err
	}
	fmt.Fprintf(o.Stdout, "已写入 %s\n", localHubPath(o.HubDir))

	// 4. 托管并启动。用现有的 setup autostart --hub，-- 之后是本机 hub 的 hub run 参数；已托管的会先停掉旧的再拉起
	host := func(what string) error {
		fmt.Fprintln(o.Stdout, what)
		args := append([]string{"--hub", "--dir", o.HubDir, "--bin", o.Bin, "--"}, localHubRunArgs(lh)...)
		if runAutostart(args, o.Stdout, o.Stderr) != 0 {
			return errors.New("托管 hub 失败，见上面")
		}
		return nil
	}
	hosted := true
	v, answers := hubAnswers(o.Port)
	managed := answers && hubJobRunning(o.HubDir, o.Bin)
	switch {
	case managed && !changed:
		fmt.Fprintf(o.Stdout, "本机 hub 已在跑（版本 %s），跳过托管\n", v)
		hosted = false
	case managed:
		// 托管的 hub 在跑，但不是按这次的对外地址、端口装的（或之前不是本机 hub 的装法）
		if err := host("托管的 hub 不是按这次的对外地址与端口装的，重新托管"); err != nil {
			return localHub{}, "", notes, err
		}
	case answers:
		return localHub{}, "", notes, fmt.Errorf("%s 已有一个不是托管的 hub 在跑（手动起的？）：先停掉它，或用 --hub-port 换端口", listen)
	default:
		if c, derr := net.DialTimeout("tcp", listen, 500*time.Millisecond); derr == nil {
			c.Close()
			return localHub{}, "", notes, fmt.Errorf("%s 被别的程序占用：用 --hub-port 换一个", listen)
		}
		if err := host(fmt.Sprintf("托管并启动 hub（只监听 %s）", listen)); err != nil {
			return localHub{}, "", notes, err
		}
	}

	// 5. 等应答：launchd、systemd 拉起 hub 要一会儿
	v, ok := waitHub(o.Port, localHubWait)
	if !ok {
		return localHub{}, "", notes, fmt.Errorf("hub 托管了但 %s 内没应答：看 %s", localHubWait, logHint(goos, autostartSpec{Kind: "hub", Dir: o.HubDir}))
	}
	if hosted {
		fmt.Fprintf(o.Stdout, "本机 hub 已应答（版本 %s）\n", v)
	}

	// 6. 接入码：只在进程内生成、交给调用方接入本机守护进程，不打印
	tok, err := st.CreateEnrollToken(10 * time.Minute)
	if err != nil {
		return localHub{}, "", notes, err
	}
	fmt.Fprintln(o.Stdout, "已生成一次性接入码（10 分钟内有效，下一步接入直接用，不显示）")
	return lh, tok, notes, nil
}

// tailscaleFunnel 经 Tailscale Funnel 定对外地址 https://<本机域名>：tailscale 要在运行且已登录、有 MagicDNS 域名、tailnet 开了
// HTTPS 证书；Funnel 还没把它转到 127.0.0.1:<port> 就开（tailscale funnel --bg），开完再看一次 serve 配置确认。
// 没找到 tailscale 返回 tailscale.ErrNotFound，Funnel 没在管理台开通返回 tailscale.ErrFunnelNotEnabled，都原样返回不改写。
func tailscaleFunnel(port int, stdout io.Writer) (string, error) {
	p, err := tailscaleFind()
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	cli := tailscale.CLI{Path: p, Run: tailscaleRun}
	st, err := cli.Status(ctx)
	if err != nil {
		return "", err
	}
	switch {
	case st.BackendState != "Running":
		return "", fmt.Errorf("本机 Tailscale 没在运行或没登录（状态 %s）：打开 Tailscale 登录后重跑", st.BackendState)
	case st.DNSName == "":
		return "", errors.New("这台机器在 Tailscale 里没有域名：在管理台开启 MagicDNS 后重跑")
	case !slices.Contains(st.CertDomains, st.DNSName):
		return "", errors.New("这个 tailnet 没开 HTTPS 证书：管理台 DNS 页开启 HTTPS Certificates 后重跑")
	}
	sc, err := cli.ServeConfig(ctx)
	if err != nil {
		return "", err
	}
	if !sc.FunnelFor(st.DNSName, port) {
		fmt.Fprintf(stdout, "开 Tailscale Funnel：https://%s 转到 127.0.0.1:%d\n", st.DNSName, port)
		if err := cli.EnableFunnel(ctx, port); err != nil {
			return "", err
		}
		if sc, err = cli.ServeConfig(ctx); err != nil {
			return "", err
		}
		if !sc.FunnelFor(st.DNSName, port) {
			return "", fmt.Errorf("开了 Funnel 但 serve 配置里没看到 https://%s 转到 127.0.0.1:%d，看 tailscale funnel status", st.DNSName, port)
		}
	}
	return "https://" + st.DNSName, nil
}
