package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/railse/railse-opc/internal/hub"
	"github.com/railse/railse-opc/internal/hub/store"
)

const hubUsage = `用法:
  railse hub run [--dir 目录] [--listen 地址] [--secure-cookies] [--trusted-proxy 地址] [--foreground]   启动 hub（前台运行）
  railse hub password [--dir 目录]            从标准输入读一行设为网页登录密码（至少 8 个字符）
  railse hub token [--dir 目录] [--ttl 10m]   生成一次性接入码，给 railse agent enroll 用
  railse hub webhook-token [--dir 目录]       生成 webhook 来源令牌（明文只显示这一次）
  目录默认 $RAILSE_HUB_HOME，否则 ~/.railse-hub
  --trusted-proxy 可重复或逗号分隔（CIDR 或单个 IP）：只有来自这些地址的连接才看 X-Forwarded-For。
  反向代理须追加 X-Forwarded-For（nginx: proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for）
  并传原 Host（proxy_set_header Host $host）`

// 以下三个是变量只为测试能替换：hub run 的运行期（SIGINT、SIGTERM 时取消）、跑 hub 的那一步，与 hub run 打开数据库的那一步。
var (
	hubRunContext = func() (context.Context, context.CancelFunc) {
		return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	}
	serveHub     = func(ctx context.Context, srv *hub.Server) error { return srv.Run(ctx) }
	openHubStore = store.Open
)

// hubFlags 是各 hub 子命令认的标志。
var hubFlags = map[string][]string{
	"run":           {"dir", "listen", "secure-cookies", "foreground", "trusted-proxy"},
	"password":      {"dir"},
	"token":         {"dir", "ttl"},
	"webhook-token": {"dir"},
}

// maxEnrollTTL 是接入码有效期的上限：接入码一旦泄露，有效期越长越危险。
const maxEnrollTTL = 24 * time.Hour

// maxPasswordBytes 是网页密码的字节上限：登录请求体限 4 KiB，留足 JSON 转义的余量，免得设了登不上。
const maxPasswordBytes = 1024

// defaultHubDir 取 hub 数据目录，与守护进程的 ~/.railse 分开。
func defaultHubDir() (string, error) {
	if d := os.Getenv("RAILSE_HUB_HOME"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到家目录: %w", err)
	}
	return filepath.Join(home, ".railse-hub"), nil
}

// ensureHubDir 建数据目录并收紧为 0700（已有的目录也收紧）。打开 hub.db 之前都要先调它。
func ensureHubDir(d string) error {
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	return os.Chmod(d, 0o700)
}

// proxyFlag 是 --trusted-proxy：可重复，每个值可用逗号分隔多个 CIDR 或单个 IP。
type proxyFlag []netip.Prefix

func (p *proxyFlag) String() string {
	if p == nil {
		return ""
	}
	s := make([]string, len(*p))
	for i, x := range *p {
		s[i] = x.String()
	}
	return strings.Join(s, ",")
}

func (p *proxyFlag) Set(v string) error {
	for _, item := range strings.Split(v, ",") {
		pfx, err := parseTrustedProxy(strings.TrimSpace(item))
		if err != nil {
			return err
		}
		*p = append(*p, pfx)
	}
	return nil
}

// parseTrustedProxy 解析一个 CIDR 或单个 IP（单个 IP 即 /32 或 /128）。拒绝带 zone 的地址，也拒绝覆盖全部地址的前缀
// （/0，以及 IPv4-mapped 的 /96 及更短）：信任所有来源等于谁都能伪造 X-Forwarded-For，按来源的登录限速随之失效。
func parseTrustedProxy(s string) (netip.Prefix, error) {
	if s == "" {
		return netip.Prefix{}, errors.New("可信代理地址为空")
	}
	var p netip.Prefix
	if strings.Contains(s, "/") {
		pp, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("可信代理 %q 不是合法的 CIDR", s)
		}
		p = pp
	} else {
		a, err := netip.ParseAddr(s)
		if err != nil || a.Zone() != "" {
			return netip.Prefix{}, fmt.Errorf("可信代理 %q 不是合法的 IP 或 CIDR", s)
		}
		p = netip.PrefixFrom(a, a.BitLen())
	}
	bits := p.Bits()
	if p.Addr().Is4In6() {
		bits -= 96
	}
	if bits <= 0 {
		return netip.Prefix{}, fmt.Errorf("可信代理 %q 覆盖全部地址，太宽；请写具体的代理地址", s)
	}
	return p, nil
}

// runHub 处理 hub 子命令。
func runHub(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, hubUsage)
		return 2
	}
	sub := args[0]
	allowed, ok := hubFlags[sub]
	if !ok {
		fmt.Fprintf(stderr, "未知的 hub 子命令 %q\n\n%s\n", sub, hubUsage)
		return 2
	}
	fs := flag.NewFlagSet("hub "+sub, flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "", "数据目录，默认 $RAILSE_HUB_HOME 或 ~/.railse-hub")
	listen := fs.String("listen", "", "监听地址，默认 127.0.0.1:8787")
	secure := fs.Bool("secure-cookies", false, "cookie 加 Secure（hub 在 TLS 反向代理后面时打开）")
	foreground := fs.Bool("foreground", false, "日志同时写到 stderr")
	ttl := fs.Duration("ttl", 10*time.Minute, "接入码有效期")
	var proxies proxyFlag
	fs.Var(&proxies, "trusted-proxy", "可信反向代理的地址（CIDR 或单个 IP），可重复或逗号分隔")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "多余的参数 %q\n%s\n", fs.Args(), hubUsage)
		return 2
	}
	if bad := foreignFlags(fs, allowed); bad != "" {
		fmt.Fprintf(stderr, "railse hub %s 不认 %s\n%s\n", sub, bad, hubUsage)
		return 2
	}
	if *ttl <= 0 || *ttl > maxEnrollTTL {
		fmt.Fprintf(stderr, "--ttl 应在 0 到 %s 之间（不含 0），得到 %s\n", maxEnrollTTL, *ttl)
		return 2
	}
	d := *dir
	if d == "" {
		dd, err := defaultHubDir()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		d = dd
	}
	if err := ensureHubDir(d); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	cfg := hub.DefaultConfig(d)
	if *listen != "" {
		cfg.Listen = *listen
	}
	cfg.SecureCookies = *secure
	cfg.TrustedProxies = proxies
	if sub == "run" {
		ctx, stop := hubRunContext()
		defer stop()
		if err := runHubServe(ctx, cfg, *foreground, stderr); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer st.Close()
	switch sub {
	case "password":
		pw, err := readPassword(stdin, stderr)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		srv, err := hub.New(cfg, st, log.New(io.Discard, "", 0))
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if err := srv.SetPassword(pw); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintln(stdout, "密码已设置（已登录的网页需要重新登录）")
	case "token":
		tok, err := st.CreateEnrollToken(*ttl)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "接入码: %s\n有效期: %s（只能用一次）\n在要接入的机器上运行:\n  railse agent enroll --hub https://<hub 地址> --token %s\n", tok, *ttl, tok)
	case "webhook-token":
		tok := store.RandomToken()
		if err := st.SetSetting("webhook_token_hash", store.HashToken(tok)); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		fmt.Fprintf(stdout, "令牌: %s\n来源调用 POST /api/webhook/approvals 时带 Authorization: Bearer <令牌>；hub 只存哈希，丢了就重新生成（旧令牌随之失效）。\n", tok)
	}
	return 0
}

// readPassword 从 stdin 读一行密码，去掉行尾的 \r\n。stdin 是终端时先在 stderr 提示。
// 长度下限由 SetPassword 检查；这里只挡超过 maxPasswordBytes 的（登录时请求体放不下）。错误信息不含密码。
func readPassword(stdin io.Reader, stderr io.Writer) (string, error) {
	if stdin == nil {
		return "", errors.New("没有读到密码")
	}
	if f, ok := stdin.(*os.File); ok {
		if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			fmt.Fprint(stderr, "输入新密码后回车（屏幕会回显；要避免回显可用 read -s 读进变量再通过管道传入）: ")
		}
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, maxPasswordBytes+2)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("读密码: %w", err)
	}
	if line == "" {
		return "", errors.New("没有读到密码")
	}
	pw := strings.TrimRight(line, "\r\n")
	if len(pw) > maxPasswordBytes {
		return "", fmt.Errorf("密码至多 %d 字节", maxPasswordBytes)
	}
	return pw, nil
}

// runHubServe 跑 hub 直到 ctx 取消。日志只写路径、计数与请求 id，不写载荷。
func runHubServe(ctx context.Context, cfg hub.Config, foreground bool, stderr io.Writer) error {
	if err := ensureHubDir(cfg.Dir); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(cfg.Dir, "hub.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
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
	logger := log.New(w, "railse-hub ", log.LstdFlags)
	st, err := openHubStore(cfg.DBPath)
	if err != nil {
		return err
	}
	closeStore := true
	defer func() {
		if closeStore {
			st.Close()
		}
	}()
	srv, err := hub.New(cfg, st, logger)
	if err != nil {
		return err
	}
	if _, ok, err := st.GetSetting("password_hash"); err == nil && !ok {
		logger.Printf("还没有设置网页密码，网页登录会得到 503；运行 railse hub password 设置")
	}
	logger.Printf("数据目录 %s，版本 %s", cfg.Dir, version)
	if len(cfg.TrustedProxies) > 0 {
		p := proxyFlag(cfg.TrustedProxies)
		logger.Printf("可信代理: %s", p.String())
	}
	// Run 返回 nil 时在跑的 WebSocket 处理与 webhook 回调都已退出，随后关库是安全的；监听失败（ErrListen）时什么都没开始跑，
	// 也照常关库。其余错误（没在时限内等到它们退出的 ErrShutdownTimeout，或 HTTP 服务自己出错结束）都不关库：
	// 还在跑的处理可能碰存储，进程随即退出，库由退出收拾。
	err = serveHub(ctx, srv)
	switch {
	case errors.Is(err, hub.ErrListen):
		logger.Printf("监听失败: %v", err)
		return err
	case errors.Is(err, hub.ErrShutdownTimeout):
		closeStore = false
		logger.Printf("hub 关停超时：仍有连接处理或回调没退出，不关数据库，进程随即退出: %v", err)
		return err
	case err != nil:
		closeStore = false
		logger.Printf("hub 异常结束，不关数据库，进程随即退出: %v", err)
		return err
	}
	logger.Printf("hub 已停止")
	return nil
}
