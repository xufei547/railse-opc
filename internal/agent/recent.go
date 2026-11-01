package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/xufei547/railse-opc/internal/proto"
)

// recentTTL、recentMax 是去重表记多久、记多少条。
const (
	recentTTL = 10 * time.Minute
	recentMax = 1000
)

// PreToolUse 处理过的调用在去重表里的结局。
const (
	recentClassifier = "classifier" // 中间三级不表态，交给了 Claude 自己的流程（allow 列表、分类器）
	recentTerminal   = "terminal"   // 送过手机（或想送），最后交回了终端
)

// recent 记最近 10 分钟 PreToolUse 处理过的调用，让随后同一调用的 PermissionRequest 知道前情，同一件事不问两次。
// 一次调用记两个键：tool_use_id（有的话）与“会话 + 工具 + 载荷”的哈希。Claude Code 2.1.280 的 PermissionRequest
// 不带 tool_use_id（见 docs/superpowers/specs/2026-11-01-pretooluse-ask-findings.md），实际靠后一个键对上。
type recent struct {
	mu      sync.Mutex
	entries map[string]recentEntry
	now     func() time.Time
}

type recentEntry struct {
	outcome string
	at      time.Time
}

func newRecent() *recent {
	return &recent{entries: map[string]recentEntry{}, now: time.Now}
}

// keys 是请求的去重键：有 tool_use_id 时先是它，最后总有会话 + 工具 + 载荷的哈希。
func (r *recent) keys(req proto.Request) []string {
	var ks []string
	if req.ToolUseID != "" {
		ks = append(ks, "id\x00"+req.ToolUseID)
	}
	b, _ := json.Marshal(req.Payload)
	sum := sha256.Sum256(append([]byte(req.ToolName+"\x00"), b...))
	return append(ks, "p\x00"+req.Session.CLISessionID+"\x00"+hex.EncodeToString(sum[:]))
}

// remember 把各个键都记成 outcome。满了先清过期的，仍满就丢最旧的一条。
func (r *recent) remember(keys []string, outcome string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for _, k := range keys {
		if _, ok := r.entries[k]; !ok && len(r.entries) >= recentMax {
			r.evictLocked(now)
		}
		r.entries[k] = recentEntry{outcome: outcome, at: now}
	}
}

// lookup 按顺序查各个键，返回第一个没过期的结局。
func (r *recent) lookup(keys []string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	for _, k := range keys {
		if e, ok := r.entries[k]; ok {
			if now.Sub(e.at) < recentTTL {
				return e.outcome, true
			}
			delete(r.entries, k)
		}
	}
	return "", false
}

// evictLocked 清掉过期的；还是满的就丢最旧的一条。调用方持 mu。
func (r *recent) evictLocked(now time.Time) {
	var oldestKey string
	var oldest time.Time
	for k, e := range r.entries {
		if now.Sub(e.at) >= recentTTL {
			delete(r.entries, k)
			continue
		}
		if oldestKey == "" || e.at.Before(oldest) {
			oldestKey, oldest = k, e.at
		}
	}
	if len(r.entries) >= recentMax && oldestKey != "" {
		delete(r.entries, oldestKey)
	}
}
