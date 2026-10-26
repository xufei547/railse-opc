package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/xufei547/railse-opc/internal/proto"
)

func init() { register(claude{}) }

// claude 对应 Claude Code 2.1.x 的钩子：PermissionRequest 决定，Notification 为 question，Stop 为 done。
type claude struct{}

func (claude) Name() string         { return "claude" }
func (claude) DeadlineSeconds() int { return DeadlineSeconds }

type claudeHook struct {
	SessionID        string          `json:"session_id"`
	PromptID         string          `json:"prompt_id"`
	Cwd              string          `json:"cwd"`
	HookEventName    string          `json:"hook_event_name"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	LastMessage      string          `json:"last_assistant_message"`
}

func (claude) Parse(stdin []byte) (Input, error) {
	var h claudeHook
	if err := json.Unmarshal(stdin, &h); err != nil {
		return Input{}, fmt.Errorf("解析 Claude 钩子输入: %w", err)
	}
	in := Input{Session: proto.Session{Tool: "claude", CLISessionID: h.SessionID, TurnID: h.PromptID, Cwd: h.Cwd}}
	switch h.HookEventName {
	case "PermissionRequest":
		if h.SessionID == "" || h.ToolName == "" {
			return Input{}, fmt.Errorf("PermissionRequest 缺少 session_id 或 tool_name")
		}
		p, err := mapTool("claude", h.ToolName, h.ToolInput, h.Cwd)
		if err != nil {
			return Input{}, err
		}
		in.Kind, in.ToolName, in.Payload = KindPermission, h.ToolName, p
	case "Notification":
		switch h.NotificationType {
		case "permission_prompt", "idle_prompt":
			in.Kind, in.Message = KindQuestion, h.Message
		default:
			in.Kind = KindIgnore
		}
	case "Stop":
		in.Kind, in.Message = KindDone, clip(h.LastMessage, 500)
	case "SessionEnd":
		in.Kind = KindSessionEnd
	default:
		in.Kind = KindIgnore
	}
	return in, nil
}

func (claude) Render(d Decision) Output { return renderPermission(d) }
