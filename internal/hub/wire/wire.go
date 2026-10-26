// Package wire 定义守护进程与 hub 之间 WebSocket 上的帧。每帧一个 JSON 对象，t 字段是种类。
package wire

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// MaxFrame 是单帧最大字节数。
const MaxFrame = 1 << 20

// Proto 是帧协议版本。守护进程在 hello 里报自己的版本，与 hub 不一致时 hub 拒绝握手。改动帧格式时加一。
const Proto = 1

// 帧种类。
const (
	THello     = "hello"     // 守护进程 → hub，连接后的第一帧
	TWelcome   = "welcome"   // hub → 守护进程，带规则快照与暂停状态
	TPending   = "pending"   // 守护进程 → hub，引擎答 ask 的请求
	TCancel    = "cancel"    // 守护进程 → hub，钩子消失
	TExpired   = "expired"   // 守护进程 → hub，等待过期
	TDecision  = "decision"  // hub → 守护进程
	TDelivered = "delivered" // 守护进程 → hub，决定已交给钩子
	TRules     = "rules"     // hub → 守护进程，规则快照
	TPaused    = "paused"    // hub → 守护进程，总开关
	TAudit     = "audit"     // 守护进程 → hub，审计批次
	TPing      = "ping"
	TPong      = "pong"
	TError     = "error"
)

// ErrFrameTooLarge 表示帧超过 MaxFrame。
var ErrFrameTooLarge = errors.New("帧超过 1 MiB")

// Envelope 只用来读 t 字段。
type Envelope struct {
	T string `json:"t"`
}

// PendingSummary 是 hello 里携带的本机未完成请求。
type PendingSummary struct {
	RequestID string    `json:"request_id"`
	CreatedAt time.Time `json:"created_at"`
}

// Hello 是守护进程连上后的第一帧。认证在 HTTP 头里，这里只报身份与状态。
type Hello struct {
	T            string           `json:"t"`
	Proto        int              `json:"proto"`
	MachineID    string           `json:"machine_id"`
	AgentVersion string           `json:"agent_version"`
	HomeDir      string           `json:"home_dir"`
	Pending      []PendingSummary `json:"pending"`
}

// Welcome 是 hub 的回应。
type Welcome struct {
	T            string    `json:"t"`
	RulesVersion int       `json:"rules_version"`
	RulesYAML    string    `json:"rules_yaml"`
	Paused       bool      `json:"paused"`
	ServerTime   time.Time `json:"server_time"`
}

// PendingRequest 是送到 hub 等人决定的请求，载荷已脱敏。
type PendingRequest struct {
	T               string         `json:"t"`
	RequestID       string         `json:"request_id"`
	Session         proto.Session  `json:"session"`
	ToolName        string         `json:"tool_name"`
	Payload         policy.Payload `json:"payload"`
	Risk            string         `json:"risk"`
	ProjectRoot     string         `json:"project_root"`
	EngineReason    string         `json:"engine_reason"`
	DeadlineSeconds int            `json:"deadline_seconds"`
	CreatedAt       time.Time      `json:"created_at"`
}

// Cancel 表示钩子在决定前退出。
type Cancel struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
	Reason    string `json:"reason"`
}

// Expired 表示守护进程侧等待过期。
type Expired struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
}

// Decision 是 hub 下发的决定，Action 为 allow 或 deny。
type Decision struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
	Action    string `json:"action"`
	Reason    string `json:"reason"`
	RuleID    string `json:"rule_id,omitempty"`
	DecidedBy string `json:"decided_by"`
}

// Delivered 是守护进程确认决定已交给钩子。
type Delivered struct {
	T         string `json:"t"`
	RequestID string `json:"request_id"`
}

// Rules 是规则快照。
type Rules struct {
	T       string `json:"t"`
	Version int    `json:"version"`
	YAML    string `json:"yaml"`
}

// Paused 是总开关。
type Paused struct {
	T      string `json:"t"`
	Paused bool   `json:"paused"`
}

// Audit 是守护进程上报的审计批次，每项是 agent.AuditEntry 的 JSON。
type Audit struct {
	T       string            `json:"t"`
	Entries []json.RawMessage `json:"entries"`
}

// Ping、Pong、Error 是控制帧。
type Ping struct {
	T string `json:"t"`
}
type Pong struct {
	T string `json:"t"`
}

// Error 的 RequestID 非空时表示针对这条请求（如登记失败、拒收），守护进程据此把它立即交回终端。
type Error struct {
	T         string `json:"t"`
	RequestID string `json:"request_id,omitempty"`
	Message   string `json:"message"`
}

// Kind 读帧种类。
func Kind(frame []byte) (string, error) {
	var e Envelope
	if err := json.Unmarshal(frame, &e); err != nil {
		return "", fmt.Errorf("解析帧: %w", err)
	}
	if e.T == "" {
		return "", errors.New("帧缺少 t 字段")
	}
	return e.T, nil
}

// Marshal 编码一帧。
func Marshal(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxFrame {
		return nil, ErrFrameTooLarge
	}
	return b, nil
}

// Unmarshal 解码一帧。
func Unmarshal(frame []byte, v any) error {
	if len(frame) > MaxFrame {
		return ErrFrameTooLarge
	}
	return json.Unmarshal(frame, v)
}
