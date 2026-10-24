// Package doctor 只读地检查一台机器上 Railse 的安装：守护进程、hub 连接、三个 CLI 的钩子与版本、权限；
// 在 hub 机器上还检查数据库、密码与推送密钥。不改任何东西：不写配置、不建文件（数据库按只读打开，hub 没在跑时
// 连 -wal、-shm 也不建），除了带超时的 `<cli> --version` 不起任何进程，网络只访问 hub 的 /api/version。
package doctor

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/railse/railse-opc/internal/hookclient"
	"github.com/railse/railse-opc/internal/proto"
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
	Project  string // 项目目录（钩子检查）；空则跳过
	HubDir   string // hub 数据目录；空则跳过 hub 侧
	Bin      string // railse 可执行文件；用于比对钩子里的程序路径，空则只按文件名认
	Version  string
	Home     string
	GOOS     string

	LookPath    func(file string) (string, error)
	RunCmd      func(name string, args ...string) (string, error) // 只用来跑 `<cli> --version`
	HTTP        *http.Client                                      // 只用来 GET hub 的 /api/version
	AgentStatus func(socket string) (proto.Status, error)
	Now         time.Time
}

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
		o.Home, _ = os.UserHomeDir()
	}
	if o.LookPath == nil {
		o.LookPath = exec.LookPath
	}
	if o.RunCmd == nil {
		o.RunCmd = runCmd
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{
			Timeout: 10 * time.Second,
			// 不跟重定向：只访问 machine.json 里那个地址的 /api/version；被重定向时如实报出状态码
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
}

// Run 跑全部检查，顺序固定：守护进程、hub 可达、钩子、CLI 版本、hub 侧。
func Run(o Options) Report {
	o.defaults()
	r := Report{Version: o.Version}
	r.Checks = append(r.Checks, agentChecks(o)...)
	r.Checks = append(r.Checks, hubClientChecks(o)...)
	r.Checks = append(r.Checks, hookChecks(o)...)
	r.Checks = append(r.Checks, cliChecks(o)...)
	r.Checks = append(r.Checks, hubServerChecks(o)...)
	return r
}
