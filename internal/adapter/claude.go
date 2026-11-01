package adapter

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/xufei547/railse-opc/internal/i18n"
	"github.com/xufei547/railse-opc/internal/proto"
)

func init() { register(claude{}) }

// claude 对应 Claude Code 2.1.x 的钩子：PermissionRequest 决定，Notification 为 question，Stop 为 done，
// PreToolUse 的 AskUserQuestion 为 ask（答案经 updatedInput 回填）；PermissionRequest 的 AskUserQuestion 无效，一律忽略。
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
		return Input{}, fmt.Errorf(i18n.T("解析 Claude 钩子输入: %w"), err)
	}
	in := Input{Session: proto.Session{Tool: "claude", CLISessionID: h.SessionID, TurnID: h.PromptID, Cwd: h.Cwd}}
	switch h.HookEventName {
	case "PermissionRequest":
		if h.ToolName == proto.AskToolName {
			in.Kind = KindIgnore // 在这里放行不会让 Claude 跳过提问，答题走 PreToolUse
			break
		}
		if h.SessionID == "" || h.ToolName == "" {
			return Input{}, errors.New(i18n.T("PermissionRequest 缺少 session_id 或 tool_name"))
		}
		p, err := mapTool("claude", h.ToolName, h.ToolInput, h.Cwd)
		if err != nil {
			return Input{}, err
		}
		in.Kind, in.ToolName, in.Payload = KindPermission, h.ToolName, p
	case "PreToolUse":
		if h.ToolName != proto.AskToolName {
			in.Kind = KindIgnore
			break
		}
		if h.SessionID == "" {
			return Input{}, errors.New(i18n.T("PreToolUse 缺少 session_id"))
		}
		var ask proto.Ask
		if err := json.Unmarshal(h.ToolInput, &ask); err != nil {
			return Input{}, fmt.Errorf(i18n.T("AskUserQuestion 的题目不合规: 解析 tool_input: %w"), err)
		}
		if err := ValidateAsk(&ask); err != nil {
			return Input{}, err
		}
		in.Kind, in.ToolName, in.Ask, in.RawToolInput = KindAsk, h.ToolName, &ask, h.ToolInput
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

// Render 把答案渲染成 PreToolUse 输出；没有答案、合并不了（没有 tool_input 原文、不是 JSON 对象）就不作决定。
// 其余动作照旧渲染成 PermissionRequest 输出。
func (claude) Render(d Decision) Output {
	if d.Action != "answer" {
		return renderPermission(d)
	}
	if len(d.Answers) == 0 || len(d.RawToolInput) == 0 {
		return passthrough()
	}
	b, err := preToolUseAnswer(d.RawToolInput, d.Answers)
	if err != nil {
		return passthrough()
	}
	return Output{Stdout: b, ExitCode: 0}
}
