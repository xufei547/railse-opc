package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/xufei547/railse-opc/internal/hooksconf"
	"github.com/xufei547/railse-opc/internal/hub"
	"github.com/xufei547/railse-opc/internal/hub/store"
)

// localHub 是 setup all --local-hub 装的本机 hub 的描述，存在 <hub 数据目录>/local-hub.json（0600），重跑与 doctor 都读它。
type localHub struct {
	Port      int    `json:"port"`       // hub 监听 127.0.0.1:<Port>
	PublicURL string `json:"public_url"` // 手机打开的 https 源，如 https://macbook-air.tail2613ae.ts.net
	Provider  string `json:"provider"`   // "tailscale"（Funnel）或 "custom"（用户给的 --public-url）
}

const localHubFile = "local-hub.json"

// localHubWait 是 waitHub 默认等 hub 起来的时间（变量，测试可改）。
var localHubWait = 10 * time.Second

func localHubPath(hubDir string) string { return filepath.Join(hubDir, localHubFile) }

// parsePublicOrigin 校验本机 hub 的对外地址并返回规范化的源（按 parseHubAddr，去掉尾斜杠）：还要是 https、不带路径。
// 同 parseHubAddr，错误信息不带原文（里面可能有密码），也不带标志名，调用方在前面加“对外地址”之类。
func parsePublicOrigin(raw string) (string, error) {
	u, base, err := parseHubAddr(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme != "https" {
		return "", errors.New("必须是 https:// 地址（手机加到主屏幕、开通知都要 https）")
	}
	if base != "https://"+u.Host {
		return "", errors.New("不能带路径，只要 https://主机[:端口]")
	}
	return base, nil
}

// normalizeLocalHub 校验字段并规范化：端口在 1..65535，PublicURL 是不带路径与查询的 https 源（去掉尾斜杠），Provider 空当 custom。
// 错误信息不带 public_url 原文（坏文件的警告会打到 stderr）。
func normalizeLocalHub(lh localHub) (localHub, error) {
	if lh.Port < 1 || lh.Port > 65535 {
		return localHub{}, fmt.Errorf("port %d 不在 1..65535", lh.Port)
	}
	norm, err := parsePublicOrigin(lh.PublicURL)
	if err != nil {
		return localHub{}, fmt.Errorf("public_url 不合规：%w", err)
	}
	lh.PublicURL = norm
	if lh.Provider == "" {
		lh.Provider = "custom"
	}
	return lh, nil
}

// loadLocalHub 读本机 hub 描述（返回规范化后的值）；没有文件时 ok=false、err=nil；坏 JSON 或字段不合规是 err。
func loadLocalHub(hubDir string) (lh localHub, ok bool, err error) {
	path := localHubPath(hubDir)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return localHub{}, false, nil
	}
	if err != nil {
		return localHub{}, false, fmt.Errorf("读 %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &lh); err != nil {
		return localHub{}, false, fmt.Errorf("%s 不是合法 JSON: %w", path, err)
	}
	lh, err = normalizeLocalHub(lh)
	if err != nil {
		return localHub{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return lh, true, nil
}

// saveLocalHub 校验并规范化后原子写本机 hub 描述（0600）；字段不合规返回错误。
func saveLocalHub(hubDir string, lh localHub) error {
	lh, err := normalizeLocalHub(lh)
	if err != nil {
		return fmt.Errorf("本机 hub 描述不合规: %w", err)
	}
	data, err := json.MarshalIndent(lh, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(localHubPath(hubDir), append(data, '\n'), 0o600)
}

// localHubRunArgs 是托管 hub 时 -- 之后给 hub run 的参数。
func localHubRunArgs(lh localHub) []string {
	return []string{"--listen", "127.0.0.1:" + strconv.Itoa(lh.Port), "--public-url", lh.PublicURL, "--trusted-proxy", "127.0.0.1,::1"}
}

// hubAnswers 问 http://127.0.0.1:<port>/api/version：是 railse hub（有 version 与正的 agent_proto）时返回它的版本与 true。
// 超时 2 秒，不跟随重定向。
func hubAnswers(port int) (version string, ok bool) {
	c := &http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := c.Get("http://127.0.0.1:" + strconv.Itoa(port) + "/api/version")
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", false
	}
	// version 非空、agent_proto 是正数才认是 railse hub（别的服务碰巧也有 /api/version、回个 version 的不算）；
	// 协议是否与本机一致由 doctor 的 hub.reachable 管，这里不比较
	var v struct {
		Version    string `json:"version"`
		AgentProto int    `json:"agent_proto"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&v); err != nil || v.Version == "" || v.AgentProto <= 0 {
		return "", false
	}
	return v.Version, true
}

// waitHub 每 200 毫秒问一次，直到应答或过了 wait。
func waitHub(port int, wait time.Duration) (string, bool) {
	deadline := time.Now().Add(wait)
	for {
		if v, ok := hubAnswers(port); ok {
			return v, true
		}
		if !time.Now().Before(deadline) {
			return "", false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// setHubPassword 把 pw 设为网页密码（railse hub password 与 setup 共用；长度等规则由 SetPassword 查），返回变更（删了多少订阅）。
func setHubPassword(cfg hub.Config, st *store.Store, pw string) (store.PasswordChange, error) {
	srv, err := hub.New(cfg, st, log.New(io.Discard, "", 0))
	if err != nil {
		return store.PasswordChange{}, err
	}
	return srv.SetPassword(pw)
}

// ensureHubPassword：已有网页密码返回 false；没有时在终端里提示并设上返回 true，不是终端则报错。
// 提示是 setup 自己的（不用 readPassword 那句“通过管道传入”：setup 不在终端里就不问，管道传不进来），说清屏幕会回显、不想回显怎么先设好。
func ensureHubPassword(cfg hub.Config, st *store.Store, stdin io.Reader, terminal bool, stdout, stderr io.Writer) (bool, error) {
	if _, ok, err := st.GetSetting("password_hash"); err != nil {
		return false, err
	} else if ok {
		return false, nil
	}
	if !terminal {
		return false, fmt.Errorf("hub 还没设网页密码：先运行 railse hub password（--dir %s），或在终端里跑 setup", cfg.Dir)
	}
	fmt.Fprintf(stdout, "本机 hub 还没设网页密码，在这里输入一个后回车（至少 8 个字符，手机登录用）。屏幕会回显；不想回显就 Ctrl-C，先这样设好再重跑：\n"+
		"  read -s PW; printf '%%s\\n' \"$PW\" | railse hub password --dir %s; unset PW\n", hooksconf.ShellQuote(cfg.Dir))
	pw, err := readPasswordLine(stdin)
	if err != nil {
		return false, err
	}
	if _, err := setHubPassword(cfg, st, pw); err != nil {
		return false, err
	}
	return true, nil
}

// proxyNote 报告本机是否开着系统代理，返回给人看的一句提醒（空表示没开代理）。
func proxyNote(goos string, getenv func(string) string, run func(name string, args ...string) (string, error), publicURL string) string {
	on := false
	for _, k := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy", "ALL_PROXY", "all_proxy"} {
		if getenv(k) != "" {
			on = true
			break
		}
	}
	if !on && goos == "darwin" {
		if out, err := run("scutil", "--proxy"); err == nil {
			for _, k := range []string{"HTTPSEnable : 1", "HTTPEnable : 1", "SOCKSEnable : 1"} {
				if strings.Contains(out, k) {
					on = true
					break
				}
			}
		}
	}
	if !on {
		return ""
	}
	return fmt.Sprintf("本机开着代理；手机若也经同一代理打不开 %s，关掉代理再试，或在代理规则里把 ts.net 设为直连", publicURL)
}
