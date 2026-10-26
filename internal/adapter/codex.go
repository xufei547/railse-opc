package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/xufei547/railse-opc/internal/proto"
)

func init() { register(codex{}) }

// codex 对应 Codex CLI 0.145 的钩子。实测只有交互会话会触发 PermissionRequest，
// codex exec 强制 approval_policy=never；Codex 没有“等人回话”事件。
type codex struct{}

func (codex) Name() string         { return "codex" }
func (codex) DeadlineSeconds() int { return DeadlineSeconds }

type codexHook struct {
	SessionID     string          `json:"session_id"`
	TurnID        string          `json:"turn_id"`
	Cwd           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	LastMessage   string          `json:"last_assistant_message"`
}

func (codex) Parse(stdin []byte) (Input, error) {
	var h codexHook
	if err := json.Unmarshal(stdin, &h); err != nil {
		return Input{}, fmt.Errorf("解析 Codex 钩子输入: %w", err)
	}
	in := Input{Session: proto.Session{Tool: "codex", CLISessionID: h.SessionID, TurnID: h.TurnID, Cwd: h.Cwd}}
	switch h.HookEventName {
	case "PermissionRequest":
		if h.SessionID == "" || h.ToolName == "" {
			return Input{}, fmt.Errorf("PermissionRequest 缺少 session_id 或 tool_name")
		}
		p, err := mapTool("codex", h.ToolName, h.ToolInput, h.Cwd)
		if err != nil {
			return Input{}, err
		}
		in.Kind, in.ToolName, in.Payload = KindPermission, h.ToolName, p
	case "Stop":
		in.Kind, in.Message = KindDone, clip(h.LastMessage, 500)
	case "SessionEnd":
		in.Kind = KindSessionEnd
	default:
		in.Kind = KindIgnore
	}
	return in, nil
}

// Render 与 Claude 同形：实测 Codex 的 PermissionRequest 输出格式完全相同。
func (codex) Render(d Decision) Output { return renderPermission(d) }

// ParseCodexNotify 解析 notify 程序收到的最后一个命令行参数。只认 agent-turn-complete。
func ParseCodexNotify(arg []byte) (Input, error) {
	var n struct {
		Type        string `json:"type"`
		ThreadID    string `json:"thread-id"`
		TurnID      string `json:"turn-id"`
		Cwd         string `json:"cwd"`
		LastMessage string `json:"last-assistant-message"`
	}
	if err := json.Unmarshal(arg, &n); err != nil {
		return Input{}, fmt.Errorf("解析 Codex notify 参数: %w", err)
	}
	in := Input{Session: proto.Session{Tool: "codex", CLISessionID: n.ThreadID, TurnID: n.TurnID, Cwd: n.Cwd}}
	if n.Type != "agent-turn-complete" {
		in.Kind = KindIgnore
		return in, nil
	}
	in.Kind, in.Message = KindDone, clip(n.LastMessage, 500)
	return in, nil
}
