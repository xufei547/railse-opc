// Package agent 是跑在每台机器上的守护进程：监听本地 Unix socket，接收钩子的请求，
// 用策略引擎自动决定，写本地审计。没有 hub 时，引擎答 ask 就返回“不作决定”。
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config 是守护进程的路径与常量。
type Config struct {
	Dir        string // 配置目录，默认 ~/.railse
	SocketPath string
	RulesPath  string
	AuditPath  string
	StatePath  string
	LogPath    string
	MachineID  string
	HomeDir    string
	Deadline   time.Duration // 单条请求的等待上限
}

// deadline 比各 CLI 的 24 小时钩子超时短 10 秒，见设计文档 3.2。
const deadline = 86390 * time.Second

// ConfigIn 按给定目录推出全部路径。
func ConfigIn(dir string) Config {
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()
	if host == "" {
		host = "local"
	}
	return Config{
		Dir:        dir,
		SocketPath: filepath.Join(dir, "agent.sock"),
		RulesPath:  filepath.Join(dir, "rules.yaml"),
		AuditPath:  filepath.Join(dir, "audit.jsonl"),
		StatePath:  filepath.Join(dir, "state.json"),
		LogPath:    filepath.Join(dir, "agent.log"),
		MachineID:  host,
		HomeDir:    home,
		Deadline:   deadline,
	}
}

// DefaultConfig 用 $RAILSE_HOME，否则 ~/.railse。
func DefaultConfig() (Config, error) {
	if dir := os.Getenv("RAILSE_HOME"); dir != "" {
		return ConfigIn(dir), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, fmt.Errorf("找不到家目录: %w", err)
	}
	return ConfigIn(filepath.Join(home, ".railse")), nil
}

// EnsureDirs 建配置目录，权限 0700。
func (c Config) EnsureDirs() error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(c.Dir, 0o700)
}
