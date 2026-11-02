// Package proto 定义钩子进程与守护进程之间的一行 JSON 协议。
// 一次连接只处理一条消息：客户端写一行请求，服务端回一行应答。
package proto

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/xufei547/railse-opc/internal/policy"
)

// Version 是当前协议版本，所有消息的 v 字段必须等于它。
const Version = 1

// MaxLine 是单行的最大字节数。
const MaxLine = 1 << 20

// ErrLineTooLong 表示单行超过 MaxLine。
var ErrLineTooLong = errors.New("协议行超过 1 MiB")

// Session 标识一个正在跑的 agent 会话。
type Session struct {
	Tool         string `json:"tool"`                  // claude、codex、gemini
	CLIVersion   string `json:"cli_version,omitempty"` // 适配器能拿到时填
	CLISessionID string `json:"cli_session_id"`
	TurnID       string `json:"turn_id,omitempty"` // Claude 的 prompt_id、Codex 的 turn_id
	Cwd          string `json:"cwd"`
}

// Option 是一道题的一个备选项。
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

// Question 是 AskUserQuestion 的一道题。
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header,omitempty"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect,omitempty"`
}

// Ask 是 Claude Code 的 AskUserQuestion 要人答的题，原样转述，不做判断。
type Ask struct {
	Questions []Question `json:"questions"`
}

// AskToolName 是 Claude Code 问人的工具名。守护进程（上报帧、审计）与 hub（入库、比对重发）的答题请求一律用它，
// 不照收钩子或上报帧里的名字。
const AskToolName = "AskUserQuestion"

// AnswerMax 是一道题答案的字符上限（按 rune 计）：hub 的答题接口与守护进程收 hub 的答案都按它校验。
const AnswerMax = 1000

// MapText 返回题目的深拷贝，题面、标题、选项的标签与说明都经 f 换过；没有选项的题得到空切片（编码成 []，不是 null）。
// a 为 nil 时返回 nil。
func (a *Ask) MapText(f func(string) string) *Ask {
	if a == nil {
		return nil
	}
	out := &Ask{Questions: make([]Question, len(a.Questions))}
	for i, q := range a.Questions {
		opts := make([]Option, len(q.Options))
		for j, o := range q.Options {
			opts[j] = Option{Label: f(o.Label), Description: f(o.Description)}
		}
		out.Questions[i] = Question{Question: f(q.Question), Header: f(q.Header), Options: opts, MultiSelect: q.MultiSelect}
	}
	return out
}

// Request 是一次请求，Kind 为 permission（审批）或 ask（答题）。
// Kind 为 ask 时带 Ask，Payload 为零值，ToolName 为 AskUserQuestion。
type Request struct {
	V               int            `json:"v"`
	Kind            string         `json:"kind"`
	Session         Session        `json:"session"`
	ToolName        string         `json:"tool_name"`
	Payload         policy.Payload `json:"payload"`
	Ask             *Ask           `json:"ask,omitempty"`
	DeadlineSeconds int            `json:"deadline_seconds"`
	// Source 是审批来自哪个钩子事件：pretooluse 或 permissionrequest；空按 permissionrequest（旧钩子、Codex）。
	Source string `json:"source,omitempty"`
	// PermissionMode 是 Claude Code 当时的权限模式（default、acceptEdits、plan、auto、bypassPermissions、dontAsk），原样透传。
	PermissionMode string `json:"permission_mode,omitempty"`
	// ToolUseID 是 Claude Code 给这次工具调用的编号，只有 PreToolUse 带，用来认出同一调用。
	ToolUseID string `json:"tool_use_id,omitempty"`
	// TrustFor 是钩子从环境变量 RAILSE_TRUST 带来的时长原文（如 "2h"）：守护进程第一次见到带它的会话时建一条会话放行，之后不再看它。
	TrustFor string `json:"trust_for,omitempty"`
}

// 审批请求的来源钩子事件。
const (
	SourcePreToolUse        = "pretooluse"
	SourcePermissionRequest = "permissionrequest"
)

// Response 是守护进程对请求的答复。Decision 为 allow、deny、passthrough、answer，或 ask（只回 PreToolUse 来的硬底线：
// 送不到手机时要 Claude 在终端里强制问人）。
// Decision 为 answer 时带 Answers：问题原文 → 答案（多选题的各个答案用逗号连接）。
type Response struct {
	V         int               `json:"v"`
	Decision  string            `json:"decision"`
	Reason    string            `json:"reason,omitempty"`
	RuleID    string            `json:"rule_id,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
	Risk      string            `json:"risk,omitempty"`
	Answers   map[string]string `json:"answers,omitempty"`
	// DecidedBy 是谁作的决定（rule:<id>、user、classifier、ask、hub_unreachable、timeout…），钩子据此决定要不要在终端提示。
	DecidedBy string `json:"decided_by,omitempty"`
	// Trigger 是引擎定级时起决定作用的那一处（哪一段、什么性质），钩子用它拼 ask 的理由；只在 Decision 为 ask 时填。
	Trigger *policy.Trigger `json:"trigger,omitempty"`
}

// Event 是只展示不操作的事件，Kind 为 question、done、session_end 或 tool_done。
// tool_done 来自 Claude 的 PostToolUse：工具已经跑完，守护进程据此取消同一调用还挂着的审批（桌面应用不结束 PermissionRequest 钩子），
// 带工具名、载荷（与审批同一算法，只用来对上，不上报）与 tool_use_id；其余事件没有这三项。
type Event struct {
	V         int             `json:"v"`
	Kind      string          `json:"kind"`
	Session   Session         `json:"session"`
	Message   string          `json:"message,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	Payload   *policy.Payload `json:"payload,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
}

// EventAck 是事件的确认。
type EventAck struct {
	V     int    `json:"v"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Control 是本机管理命令，Kind 固定为 control。
type Control struct {
	V    int    `json:"v"`
	Kind string `json:"kind"`
	Op   string `json:"op"` // status、pause、resume、reload、trust_set、trust_list、trust_revoke
	// Trust 是 trust_set 要建的放行（ID、CreatedAt 由守护进程补，CreatedBy 空按 cli）。
	Trust *Trust `json:"trust,omitempty"`
	// TrustID 是 trust_revoke 要撤的放行；"all" 撤全部。
	TrustID string `json:"trust_id,omitempty"`
}

// Trust 是一条限时放行：范围内引擎定不了（ask）的审批在 Until 之前一律放行。守护进程是执行者，hub 存副本给手机看、建、撤。
// session 范围按 Tool + CLISession 认会话，project 范围按项目根（与守护进程算出的项目根字符串相等）。
type Trust struct {
	ID         string    `json:"id"`
	MachineID  string    `json:"machine_id,omitempty"`
	Scope      string    `json:"scope"`                    // session | project
	Tool       string    `json:"tool,omitempty"`           // session
	CLISession string    `json:"cli_session_id,omitempty"` // session
	Project    string    `json:"project_root,omitempty"`   // project
	Until      time.Time `json:"until"`
	CreatedAt  time.Time `json:"created_at"`
	CreatedBy  string    `json:"created_by"` // phone | cli | env
	Note       string    `json:"note,omitempty"`
}

// 放行的范围与上下限：时长 5 分钟～24 小时（超上限截断、短于下限拒建），默认 2 小时；每台机器同时生效至多 20 条。
const (
	TrustScopeSession = "session"
	TrustScopeProject = "project"
	TrustMin          = 5 * time.Minute
	TrustMax          = 24 * time.Hour
	TrustDefault      = 2 * time.Hour
	TrustPerMachine   = 20
)

// Status 是守护进程状态。Paused 是生效值（本机暂停或 hub 暂停），LocalPaused、HubPaused 说明来源。
type Status struct {
	Paused      bool   `json:"paused"`
	LocalPaused bool   `json:"local_paused"`
	HubPaused   bool   `json:"hub_paused"`
	Rules       int    `json:"rules"`
	Pending     int    `json:"pending"`
	MachineID   string `json:"machine_id"`
	RulesPath   string `json:"rules_path"`
	Uptime      string `json:"uptime"`
	Hub         string `json:"hub"`               // none（未接入）、connecting、connected、disabled（被 hub 以 401 停用）
	HubURL      string `json:"hub_url,omitempty"` // machine.json 里的 hub 地址；未接入时为空
	Trusts      int    `json:"trusts"`            // 生效中的限时放行条数
}

// ControlReply 是管理命令的答复。
type ControlReply struct {
	V      int     `json:"v"`
	OK     bool    `json:"ok"`
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
	// Trusts 是 trust_set 建好的那条，或 trust_list、trust_revoke 之后生效中的全部（按到期先后）。
	Trusts []Trust `json:"trusts,omitempty"`
}

// WriteLine 把 v 编成一行 JSON 并写出，末尾带换行。
func WriteLine(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > MaxLine {
		return ErrLineTooLong
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// ReadLine 读一行（不含换行）。没有换行就到 EOF 的最后一行也算一行。
func ReadLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			if errors.Is(err, io.EOF) && len(line) > 0 {
				return line, nil
			}
			return nil, err
		}
		line = append(line, chunk...)
		if len(line) > MaxLine {
			return nil, ErrLineTooLong
		}
		if !isPrefix {
			return line, nil
		}
	}
}

// PeekKind 只解出 kind 字段，用于分发。
func PeekKind(line []byte) (string, error) {
	var head struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return "", fmt.Errorf("解析协议行: %w", err)
	}
	return head.Kind, nil
}

// Decode 解出完整消息并校验协议版本。
func Decode(line []byte, v any) error {
	var head struct {
		V int `json:"v"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return fmt.Errorf("解析协议行: %w", err)
	}
	if head.V != Version {
		return fmt.Errorf("协议版本不匹配：想要 %d，得到 %d", Version, head.V)
	}
	return json.Unmarshal(line, v)
}

// NewTrustID 是放行记录的 id：随机 8 字节的十六进制。守护进程与 hub 都用它，两边建的 id 长短一致。
func NewTrustID() string {
	var b [8]byte
	_, _ = rand.Read(b[:]) // crypto/rand 不会失败（失败时它自己终止进程）
	return hex.EncodeToString(b[:])
}
