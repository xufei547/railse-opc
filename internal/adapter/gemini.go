package adapter

import (
	"encoding/json"
	"fmt"

	"github.com/railse/railse-opc/internal/proto"
)

func init() { register(gemini{}) }

// gemini 对应 Gemini CLI 0.65+ 的钩子。实测 BeforeTool 只能拒绝，allow 不会跳过原生确认框，
// ask 在 headless 下永久挂起，所以 Render 只输出 deny 或不作决定。
type gemini struct{}

func (gemini) Name() string         { return "gemini" }
func (gemini) DeadlineSeconds() int { return DeadlineSeconds }

type geminiHook struct {
	SessionID        string          `json:"session_id"`
	Cwd              string          `json:"cwd"`
	HookEventName    string          `json:"hook_event_name"`
	ToolName         string          `json:"tool_name"`
	ToolInput        json.RawMessage `json:"tool_input"`
	NotificationType string          `json:"notification_type"`
	Message          string          `json:"message"`
	Details          struct {
		Command string `json:"command"`
	} `json:"details"`
	PromptResponse string `json:"prompt_response"`
}

func (gemini) Parse(stdin []byte) (Input, error) {
	var h geminiHook
	if err := json.Unmarshal(stdin, &h); err != nil {
		return Input{}, fmt.Errorf("解析 Gemini 钩子输入: %w", err)
	}
	in := Input{Session: proto.Session{Tool: "gemini", CLISessionID: h.SessionID, Cwd: h.Cwd}}
	switch h.HookEventName {
	case "BeforeTool":
		if h.SessionID == "" || h.ToolName == "" {
			return Input{}, fmt.Errorf("BeforeTool 缺少 session_id 或 tool_name")
		}
		p, err := mapTool("gemini", h.ToolName, h.ToolInput, h.Cwd)
		if err != nil {
			return Input{}, err
		}
		in.Kind, in.ToolName, in.Payload = KindPermission, h.ToolName, p
	case "Notification":
		if h.NotificationType == "ToolPermission" {
			msg := h.Message
			if h.Details.Command != "" {
				msg += ": " + h.Details.Command
			}
			in.Kind, in.Message = KindQuestion, msg
		} else {
			in.Kind = KindIgnore
		}
	case "AfterAgent":
		in.Kind, in.Message = KindDone, clip(h.PromptResponse, 500)
	case "SessionEnd":
		in.Kind = KindSessionEnd
	default:
		in.Kind = KindIgnore
	}
	return in, nil
}

// Render 只有 deny 会写 stdout；allow、passthrough、ask 及未知动作一律不作决定，退出码恒为 0。
// 实测 allow 不跳过原生框，ask 在 headless 下永久挂起，退出 1 在 yolo 下照样执行，所以拒绝只用 JSON 表达。
func (gemini) Render(d Decision) Output {
	if d.Action == "deny" {
		b, _ := json.Marshal(struct {
			Decision string `json:"decision"`
			Reason   string `json:"reason"`
		}{"deny", denyMessage(d.Reason)})
		return Output{Stdout: b, ExitCode: 0}
	}
	return passthrough()
}
