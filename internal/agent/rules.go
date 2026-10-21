package agent

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/railse/railse-opc/internal/policy"
)

const rulesHeader = `# Railse OPC 本机规则文件。守护进程启动和 reload 时读取，改错了会保留上一版。
# 格式见设计文档 4.3；hub 接入后由 hub 下发，这里只是本机缓存。
`

// Rules 持有当前引擎，并发安全。
type Rules struct {
	path     string
	reloadMu sync.Mutex // 串行化 Reload，先读到的旧文件不会覆盖后读到的新文件
	mu       sync.RWMutex
	engine   *policy.Engine
	count    int
	paused   atomic.Bool // 只在持有 mu 写锁时修改，与换引擎互斥
}

// LoadRules 读规则文件；文件不存在时先写出内置规则包，已存在时收紧为 0600。
func LoadRules(path string) (*Rules, error) {
	if err := writeDefaultRules(path); err != nil {
		return nil, err
	}
	// O_CREATE 的 0600 只对新建文件生效，已有文件（如编辑器保存后变成 0644）在这里收紧。
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("收紧规则文件权限: %w", err)
	}
	r := &Rules{path: path}
	if err := r.Reload(); err != nil {
		return nil, err
	}
	return r, nil
}

// writeDefaultRules 以 O_EXCL 新建规则文件，已存在则什么都不做，不会覆盖用户的文件。
func writeDefaultRules(path string) error {
	b, err := policy.DefaultRuleSet().Marshal()
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("创建规则文件: %w", err)
	}
	_, err = f.Write(append([]byte(rulesHeader), b...))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// 写了一半的文件下次启动会解析失败，删掉让下次重新生成。
		os.Remove(path)
		return fmt.Errorf("写规则文件: %w", err)
	}
	return nil
}

// Reload 重新读文件；解析或校验失败时保留旧引擎并返回错误。
func (r *Rules) Reload() error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	data, err := os.ReadFile(r.path)
	if err != nil {
		return fmt.Errorf("读规则文件: %w", err)
	}
	rs, err := policy.ParseRuleSet(data)
	if err != nil {
		return fmt.Errorf("规则文件 %s: %w", r.path, err)
	}
	eng, err := policy.NewEngine(rs)
	if err != nil {
		return fmt.Errorf("规则文件 %s: %w", r.path, err)
	}
	// 暂停状态在写锁内读取并换引擎，与 SetPaused 互斥，交错时不会丢。
	r.mu.Lock()
	eng.SetPaused(r.paused.Load())
	r.engine, r.count = eng, len(rs.Rules)
	r.mu.Unlock()
	return nil
}

// Engine 返回当前引擎。
func (r *Rules) Engine() *policy.Engine {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.engine
}

// Count 返回规则条数。
func (r *Rules) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.count
}

// Path 返回规则文件路径。
func (r *Rules) Path() string { return r.path }

// SetPaused 设置总开关，对当前和以后重载的引擎都生效。
func (r *Rules) SetPaused(p bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.paused.Store(p)
	r.engine.SetPaused(p)
}

// Paused 返回总开关状态。
func (r *Rules) Paused() bool { return r.paused.Load() }
