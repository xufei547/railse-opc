package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/xufei547/railse-opc/internal/i18n"
	"github.com/xufei547/railse-opc/internal/proto"
)

func init() { register(claude{}) }

// claude 对应 Claude Code 2.1.x 的钩子：PreToolUse 的 Bash、文件写、WebFetch 与 PermissionRequest 都是审批（前者任何权限模式
// 都触发，后者只在 Claude 自己决定要问人时触发，留作 MCP 等其余工具的兜底），Notification 为 question，Stop 为 done，
// PreToolUse 的 AskUserQuestion 为 ask（答案经 updatedInput 回填）；PermissionRequest 的 AskUserQuestion 无效，一律忽略。
// PostToolUse 为 tool_done（工具已跑完，清掉同一调用挂着的审批）。
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
	PermissionMode   string          `json:"permission_mode"`
	ToolUseID        string          `json:"tool_use_id"`
}

// preToolUseTools 是 PreToolUse 钩子当审批处理的工具（与 hooksconf.ClaudePreToolUseMatcher 一致），另加全部 MCP 工具
// （见 isPreToolUseTool）；其余工具（Task 等）PreToolUse 不管，留给 PermissionRequest。
var preToolUseTools = map[string]bool{"Bash": true, "Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true, "WebFetch": true, "WebSearch": true}

// isPreToolUseTool 报告 PreToolUse、PostToolUse 认不认这个工具：preToolUseTools 与 MCP 工具（mcp__ 开头）。
func isPreToolUseTool(name string) bool {
	return preToolUseTools[name] || strings.HasPrefix(name, "mcp__")
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
		in.Source, in.PermissionMode, in.ToolUseID = SourcePermissionRequest, h.PermissionMode, h.ToolUseID
	case "PreToolUse":
		if h.ToolName != proto.AskToolName && !isPreToolUseTool(h.ToolName) {
			in.Kind = KindIgnore // 其余工具留给 PermissionRequest 兜底
			break
		}
		if h.SessionID == "" {
			return Input{}, errors.New(i18n.T("PreToolUse 缺少 session_id"))
		}
		if h.ToolName != proto.AskToolName {
			p, err := mapTool("claude", h.ToolName, h.ToolInput, h.Cwd)
			if err != nil {
				return Input{}, err
			}
			in.Kind, in.ToolName, in.Payload = KindPermission, h.ToolName, p
			in.Source, in.PermissionMode, in.ToolUseID = SourcePreToolUse, h.PermissionMode, h.ToolUseID
			break
		}
		var ask proto.Ask
		if err := json.Unmarshal(h.ToolInput, &ask); err != nil {
			return Input{}, fmt.Errorf(i18n.T("AskUserQuestion 的题目不合规: 解析 tool_input: %w"), err)
		}
		if err := ValidateAsk(&ask); err != nil {
			return Input{}, err
		}
		in.Kind, in.ToolName, in.Ask, in.RawToolInput = KindAsk, h.ToolName, &ask, h.ToolInput
	case "PostToolUse":
		// 工具已经跑完：桌面应用（SDK 宿主）在 PreToolUse 回 ask 后自己裁决，却不结束同一调用的 PermissionRequest 钩子，
		// 守护进程靠这个事件把它取消。只认审批的那几样工具；载荷与 PermissionRequest 同一算法，守护进程按它对上
		if !isPreToolUseTool(h.ToolName) || h.SessionID == "" {
			in.Kind = KindIgnore
			break
		}
		p, err := mapTool("claude", h.ToolName, h.ToolInput, h.Cwd)
		if err != nil {
			return Input{}, err
		}
		in.Kind, in.ToolName, in.Payload, in.ToolUseID = KindToolDone, h.ToolName, p, h.ToolUseID
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
// 来自 PreToolUse 的审批渲染成 PreToolUse 的 permissionDecision，其余动作照旧渲染成 PermissionRequest 输出。
func (claude) Render(d Decision) Output {
	if d.Action != "answer" {
		if d.Source == SourcePreToolUse {
			return renderPreToolUse(d)
		}
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

// renderPreToolUse 把审批决定渲染成 PreToolUse 输出：allow、deny、ask 带 Railse 前缀的理由，其余不作决定（stdout 为空），
// Claude 照自己的权限流程走。ask 只给守护进程明确要求的硬底线（送不到手机时）：2.1.280 实测它在任何模式下都强制让人
// 确认、不回分类器（见 docs/superpowers/specs/2026-11-01-pretooluse-ask-findings.md），用在一般的不表态上会比 auto 模式更吵。
func renderPreToolUse(d Decision) Output {
	var decision, reason string
	switch d.Action {
	case "allow":
		decision, reason = "allow", allowMessage(d.Reason)
	case "deny":
		decision, reason = "deny", denyMessage(d.Reason)
	case "ask":
		decision, reason = "ask", allowMessage(d.Reason)
	default:
		return passthrough()
	}
	type hso struct {
		HookEventName            string `json:"hookEventName"`
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
	}
	b, _ := marshalNoEscape(struct {
		HookSpecificOutput hso `json:"hookSpecificOutput"`
	}{hso{"PreToolUse", decision, reason}})
	return Output{Stdout: b, ExitCode: 0}
}
