// Package proto 定义钩子进程与守护进程之间的一行 JSON 协议。
// 一次连接只处理一条消息：客户端写一行请求，服务端回一行应答。
package proto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/railse/railse-opc/internal/policy"
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

// Request 是一次审批请求，Kind 固定为 permission。
type Request struct {
	V               int            `json:"v"`
	Kind            string         `json:"kind"`
	Session         Session        `json:"session"`
	ToolName        string         `json:"tool_name"`
	Payload         policy.Payload `json:"payload"`
	DeadlineSeconds int            `json:"deadline_seconds"`
}

// Response 是守护进程对审批请求的答复。Decision 为 allow、deny 或 passthrough。
type Response struct {
	V         int    `json:"v"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason,omitempty"`
	RuleID    string `json:"rule_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Risk      string `json:"risk,omitempty"`
}

// Event 是只展示不操作的事件，Kind 为 question、done 或 session_end。
type Event struct {
	V       int     `json:"v"`
	Kind    string  `json:"kind"`
	Session Session `json:"session"`
	Message string  `json:"message,omitempty"`
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
	Op   string `json:"op"` // status、pause、resume、reload
}

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
}

// ControlReply 是管理命令的答复。
type ControlReply struct {
	V      int     `json:"v"`
	OK     bool    `json:"ok"`
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
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
