package agent

import (
	"encoding/json"
	"os"
	"sync"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// AuditEntry 是审计日志的一行。Payload 已脱敏。
type AuditEntry struct {
	TS        time.Time         `json:"ts"`
	RequestID string            `json:"request_id,omitempty"`
	Kind      string            `json:"kind"` // permission、ask、question、done、session_end、control
	Session   proto.Session     `json:"session"`
	ToolName  string            `json:"tool_name,omitempty"`
	Payload   *policy.Payload   `json:"payload,omitempty"`
	Ask       *proto.Ask        `json:"ask,omitempty"` // 仅 ask：脱敏后的题目
	Risk      string            `json:"risk,omitempty"`
	Action    string            `json:"action,omitempty"`  // allow、deny、passthrough、answer
	State     string            `json:"state"`             // auto_allowed、auto_denied、approved、denied、answered、passthrough、asked、cancelled、expired、recorded、suppressed
	Answers   map[string]string `json:"answers,omitempty"` // 仅 answered：脱敏后的题面 → 脱敏后的答案
	RuleID    string            `json:"rule_id,omitempty"`
	Reason    string            `json:"reason,omitempty"`
	Trigger   *policy.Trigger   `json:"trigger,omitempty"` // 引擎的触发点（触发段已脱敏）
	Note      string            `json:"note,omitempty"`    // 用户在手机上的备注，只进审计，不回给钩子
	DecidedBy string            `json:"decided_by,omitempty"`
	Message   string            `json:"message,omitempty"`
	// 仅 Claude 的审批：来源钩子事件（pretooluse、permissionrequest）、权限模式、工具调用编号；同一调用的两次事件靠它们对上。
	Source         string `json:"source,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
	ToolUseID      string `json:"tool_use_id,omitempty"`
}

// Audit 是只追加的 JSONL 文件。
type Audit struct {
	mu sync.Mutex
	f  *os.File
}

// OpenAudit 以追加方式打开审计文件，权限 0600。
func OpenAudit(path string) (*Audit, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	// O_CREATE 的 0600 只对新建文件生效，已有文件要在打开后收紧。
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, err
	}
	return &Audit{f: f}, nil
}

// Write 追加一行。
func (a *Audit) Write(e AuditEntry) error {
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	_, err = a.f.Write(append(b, '\n'))
	return err
}

// Close 关闭文件。
func (a *Audit) Close() error { return a.f.Close() }
