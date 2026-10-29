// Package doctor 只读地检查一台机器上 Railse 的安装：守护进程、hub 连接、三个 CLI 的钩子与版本、项目根、权限；
// 在 hub 机器上还检查数据库、密码、推送密钥与本机 hub（setup all --local-hub 装的）的对外地址。doctor 自己不改配置、
// 不建文件、不改权限（数据库按只读打开，连 -wal、-shm 也不建），网络只访问 hub 的 /api/version（machine.json 里的地址，
// 与本机 hub 的对外地址）。会起的进程只有带超时的 `<cli> --version` 与本机 hub 由 Tailscale 提供时的
// `tailscale serve status --json`（经 tailscale.Exec，15 秒超时），可能的副作用只有它们自己写下的东西（如缓存、更新检查记录）；所以 sudo 下
// （euid 是 root，或家目录不归当前用户）都不跑，免得在用户目录留下 root 属主的文件。
package doctor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/xufei547/railse-opc/internal/hookclient"
	"github.com/xufei547/railse-opc/internal/proto"
	"github.com/xufei547/railse-opc/internal/tailscale"
)

// Status 是一项检查的结果。
type Status string

const (
	OK   Status = "ok"
	Warn Status = "warn"
	Fail Status = "fail"
	Skip Status = "skip"
)

// Check 是一项检查。ID 固定不变（给脚本与 --json 用），Name、Detail、Hint 是中文。warn 与 fail 必带 Hint。
type Check struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status Status `json:"status"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

// Report 是一次 doctor 的全部结果。
type Report struct {
	Version string  `json:"version"`
	Checks  []Check `json:"checks"`
}

// Failed 报告是否有 fail。
func (r Report) Failed() bool {
	for _, c := range r.Checks {
		if c.Status == Fail {
			return true
		}
	}
	return false
}

// Options 是检查的输入；函数型字段与 GOOS、Home、HTTP、Now 为空时由 Run 填真实实现。
type Options struct {
	AgentDir string // 守护进程数据目录
	Project  string // 项目目录（项目级钩子、项目根检查）；空则跳过
	HubDir   string // hub 数据目录；空则跳过 hub 侧
	Bin      string // railse 可执行文件；用于比对钩子里的程序路径，空则只按文件名认
	Version  string
	Home     string
	GOOS     string

	// HubDirRequired 表示 HubDir 是显式给的（--hub-dir）：它不存在或读不到时 hubsrv.dir 报 fail，而不是把 hub 侧整段跳过。
	HubDirRequired bool
	// ProjectExplicit 表示 Project 是显式给的（--project）。不是时 Project 是 doctor 默认看的当前目录（如在 ~/Downloads 里
	// 跑 railse setup），它不是 git 仓库或在系统目录里时 project.root 只告知（skip），不给修复提示。项目级钩子的检查不看它。
	ProjectExplicit bool
	// HubSkip 非空且 HubDir 为空时，hub 侧整段 skip，细节用它（如系统 hub 目录属于别的用户、查过哪些位置）。
	HubSkip string

	// DefaultSocket 是不带 --socket 的钩子会连的 socket 的基准：~/.railse/agent.sock。钩子进程的环境是 CLI 的，那里不一定有
	// RAILSE_HOME，按最坏情况（没有）算；setup hooks 也以 ~/.railse 为基准，目录不是它就写 --socket。为空时 Run 由 Home 推；
	// 取不到家目录就不比对不带 --socket 的钩子。
	DefaultSocket string

	LookPath func(file string) (string, error)
	RunCmd   func(name string, args ...string) (string, error) // 只用来跑 `<cli> --version`
	// HTTP 只用来 GET hub 的 /api/version：machine.json 里的地址，与本机 hub（local-hub.json）的对外地址
	HTTP *http.Client
	// TailscalePath 找 tailscale 可执行文件，TailscaleRun 跑它（只跑 `tailscale serve status --json`，默认 tailscale.Exec：
	// 15 秒超时、stdin 为 /dev/null）。都只在本机 hub 的对外地址由 Tailscale Funnel 提供时才用。
	TailscalePath func() (string, error)
	TailscaleRun  tailscale.Runner
	AgentStatus   func(socket string) (proto.Status, error)
	Now           time.Time

	// Geteuid 与 FileOwner 用来认出 sudo 下运行（euid 是 root，或 Home 的属主不是当前 euid），那时不跑 CLI 的 --version 与 tailscale。
	// FileOwner 查不到（路径不存在、不支持的系统）时 ok 为 false。
	Geteuid   func() int
	FileOwner func(path string) (uid int, ok bool)
}

// noHome 是取不到家目录时，用到家目录的检查（开机自启、Codex 的钩子与 notify）的 skip 说明。
const noHome = "取不到家目录（$HOME 没设），跳过"

// cliTimeout 是 `<cli> --version` 的时限：首次运行的自更新、登录提示之类卡住时不让 doctor 跟着挂住。是变量只为测试能调短。
var cliTimeout = 10 * time.Second

// commandContext 起子进程；是变量只为测试能换成假的慢 CLI。
var commandContext = exec.CommandContext

// runCmd 是默认的 RunCmd：合并 stdout 与 stderr，超过 cliTimeout 就杀掉。stdin 是 /dev/null，CLI 等输入时直接读到 EOF。
func runCmd(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cliTimeout)
	defer cancel()
	cmd := commandContext(ctx, name, args...)
	// 杀掉之后最多再等这么久：CLI 的子进程还占着输出管道时也不一直等下去
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() != nil {
		return string(out), fmt.Errorf("%s 超过 %s 没有结束，已终止", name, cliTimeout)
	}
	return string(out), err
}

func (o *Options) defaults() {
	if o.GOOS == "" {
		o.GOOS = runtime.GOOS
	}
	if o.Home == "" {
		o.Home, _ = os.UserHomeDir() // 取不到时留空，用到它的检查 skip（noHome），不拼相对路径
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.RunCmd == nil {
		o.RunCmd = runCmd
	}
	if o.TailscalePath == nil {
		o.TailscalePath = func() (string, error) { return tailscale.Find(exec.LookPath, os.Stat, runtime.GOOS) }
	}
	if o.TailscaleRun == nil {
		o.TailscaleRun = tailscale.Exec
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{
			Timeout: 10 * time.Second,
			// 不跟重定向：只访问 machine.json 里那个地址与本机 hub 对外地址的 /api/version；被重定向时如实报出状态码
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if o.AgentStatus == nil {
		o.AgentStatus = func(socket string) (proto.Status, error) {
			reply, err := hookclient.Client{SocketPath: socket, ConnectTimeout: 2 * time.Second}.Control("status")
			if err != nil {
				return proto.Status{}, err
			}
			if !reply.OK || reply.Status == nil {
				return proto.Status{}, fmt.Errorf("守护进程答复失败: %s", reply.Error)
			}
			return *reply.Status, nil
		}
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	if o.Geteuid == nil {
		o.Geteuid = os.Geteuid
	}
	if o.FileOwner == nil {
		o.FileOwner = fileOwner
	}
	if o.DefaultSocket == "" && o.Home != "" { // 不看 $RAILSE_HOME：钩子进程的环境里不一定有它
		o.DefaultSocket, _ = filepath.Abs(filepath.Join(o.Home, ".railse", "agent.sock"))
	}
}

// Run 跑全部检查，顺序固定：守护进程、hub 可达、钩子、项目根、CLI 版本、hub 侧。
func Run(o Options) Report {
	o.defaults()
	r := Report{Version: o.Version}
	r.Checks = append(r.Checks, agentChecks(o)...)
	r.Checks = append(r.Checks, hubClientChecks(o)...)
	r.Checks = append(r.Checks, hookChecks(o)...)
	r.Checks = append(r.Checks, projectRootCheck(o))
	r.Checks = append(r.Checks, cliChecks(o)...)
	r.Checks = append(r.Checks, hubServerChecks(o)...)
	return r
}
