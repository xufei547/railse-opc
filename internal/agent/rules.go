package agent

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
)

const rulesHeader = `# Railse OPC 本机规则文件。守护进程启动和 reload 时读取，改错了会保留上一版。
# 格式见设计文档 4.3；hub 接入后由 hub 下发，这里只是本机缓存。
`

// hubRulesMark 是 hub 下发的规则缓存的文件头开头；不以它开头的 rules.yaml 是本机规则，首次被 hub 覆盖前先备份。
const hubRulesMark = "# 由 hub 下发"

const hubRulesHeader = hubRulesMark + "，版本 %d。本机修改会在下次下发时被覆盖。\n"

// Rules 持有当前引擎，并发安全。
type Rules struct {
	path     string
	home     string      // 规范化规则路径时展开 ~ 用
	log      *log.Logger // 规则路径被改写时记一条；nil 时不记
	reloadMu sync.Mutex  // 串行化 Reload，先读到的旧文件不会覆盖后读到的新文件
	mu       sync.RWMutex
	engine   *policy.Engine
	count    int
	version  int         // 最近一次 hub 下发的版本；从文件加载（启动、Reload）的为 0
	paused   atomic.Bool // 只在持有 mu 写锁时修改，与换引擎互斥
}

// LoadRules 读规则文件；文件不存在时先写出内置规则包，已存在时收紧为 0600。
func LoadRules(path string) (*Rules, error) {
	return LoadRulesWithLogger(path, nil)
}

// LoadRulesWithLogger 同 LoadRules；之后每次加载时规则里的路径被按真实路径改写，都往 logger 记一条。
func LoadRulesWithLogger(path string, logger *log.Logger) (*Rules, error) {
	if err := writeDefaultRules(path); err != nil {
		return nil, err
	}
	// O_CREATE 的 0600 只对新建文件生效，已有文件（如编辑器保存后变成 0644）在这里收紧。
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("收紧规则文件权限: %w", err)
	}
	r := &Rules{path: path, home: userHome(), log: logger}
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
// 规则里写成符号链接形式的项目路径与 deny、ask 的路径模式会按真实路径改写，见 normalizeRulePaths。
// 成功后 Version 归 0：文件可能已被本机改过，不再代表 hub 的某个版本。
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
	normalizeRulePaths(&rs, r.home, r.logf)
	eng, err := policy.NewEngine(rs)
	if err != nil {
		return fmt.Errorf("规则文件 %s: %w", r.path, err)
	}
	// 暂停状态在写锁内读取并换引擎，与 SetPaused 互斥，交错时不会丢。
	r.mu.Lock()
	eng.SetPaused(r.paused.Load())
	r.engine, r.count, r.version = eng, len(rs.Rules), 0
	r.mu.Unlock()
	return nil
}

// Replace 用 hub 下发的规则集替换引擎并写缓存文件。hub 是唯一真相，总是替换（版本乱序由调用方过滤）。
// 缓存写的是 hub 的原样 YAML，路径改写只作用于内存里的引擎（Reload 读缓存时会再改写一次，结果一致）。
// 坏规则集在写文件前返回错误，当前引擎与缓存文件都不动；替换保持当前暂停状态。
// 本机 rules.yaml 还不是 hub 下发的缓存时（首次接入），覆盖前先备份，见 backupLocal；备份失败也不覆盖。
// 与 Reload 共用 reloadMu 串行，换引擎时持 mu 写锁，与 SetPaused 互斥。
func (r *Rules) Replace(rs policy.RuleSet, version int) error {
	r.reloadMu.Lock()
	defer r.reloadMu.Unlock()
	// 先按原样校验：缓存文件写的是原样，重启时 ParseRuleSet 要能读回来。
	if err := rs.Validate(); err != nil {
		return fmt.Errorf("hub 下发的规则（版本 %d）: %w", version, err)
	}
	b, err := rs.Marshal()
	if err != nil {
		return err
	}
	// normalizeRulePaths 原地改写，先复制一份，不动调用方的切片。
	rs.Rules = append([]policy.Rule(nil), rs.Rules...)
	normalizeRulePaths(&rs, r.home, r.logf)
	eng, err := policy.NewEngine(rs)
	if err != nil {
		return fmt.Errorf("hub 下发的规则（版本 %d）: %w", version, err)
	}
	if err := r.backupLocal(); err != nil {
		return fmt.Errorf("备份本机规则: %w", err)
	}
	if err := writeFileAtomic(r.path, append([]byte(fmt.Sprintf(hubRulesHeader, version)), b...)); err != nil {
		return fmt.Errorf("写规则缓存: %w", err)
	}
	r.mu.Lock()
	eng.SetPaused(r.paused.Load())
	r.engine, r.count, r.version = eng, len(rs.Rules), version
	r.mu.Unlock()
	return nil
}

// backupLocal 在 hub 规则第一次覆盖本机 rules.yaml 之前，把它原样复制为同目录的 rules.local-<UTC 时间>.yaml（0600）并记日志：
// 第 2 份本地闭环里手写的规则（例如用户 deny）会被 hub 的规则替换掉，用户要能找回并导入 hub。
// 文件不存在或已是 hub 下发的缓存（文件头以 hubRulesMark 开头）时什么都不做，所以只备份一次。
// 备份失败时返回错误，调用方不覆盖（宁可暂时沿用本机规则，也不丢掉它们）。调用方持 reloadMu。
func (r *Rules) backupLocal() error {
	data, err := os.ReadFile(r.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if bytes.HasPrefix(data, []byte(hubRulesMark)) {
		return nil
	}
	base := filepath.Join(filepath.Dir(r.path), "rules.local-"+time.Now().UTC().Format("20060102T150405Z"))
	dst := base + ".yaml"
	for i := 2; ; i++ {
		err = writeFileExcl(dst, data)
		if !errors.Is(err, os.ErrExist) || i > 100 {
			break
		}
		dst = fmt.Sprintf("%s-%d.yaml", base, i) // 同一秒里已有一份（上次备份后覆盖失败又重试）
	}
	if err != nil {
		return err
	}
	r.logf("本机 rules.yaml 不是 hub 下发的，被 hub 规则覆盖前已备份为 %s。其中手写的规则（例如 deny）不再生效；"+
		"要沿用请在 hub 上导入：POST /api/rules/import（整套替换，见 docs/hub.md）", dst)
	return nil
}

// writeFileExcl 新建 path（已存在时返回满足 os.ErrExist 的错误）、写入 data 并 fsync，权限 0600。中途失败时删掉。
func writeFileExcl(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o600) // 不受 umask 影响
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// HasLocalRules 报告 path 上的规则文件存在且不是 hub 下发的缓存：首次连上 hub 时它会被 hub 规则覆盖（覆盖前自动备份）。
// 读不出来时按有处理（宁可多提示一次）。
func HasLocalRules(path string) bool {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	return err != nil || !bytes.HasPrefix(data, []byte(hubRulesMark))
}

// Version 返回最近一次 hub 下发的版本，缓存文件加载的为 0。
func (r *Rules) Version() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.version
}

func (r *Rules) logf(format string, args ...any) {
	if r.log != nil {
		r.log.Printf(format, args...)
	}
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
