// Package adapter 把各家 agent CLI 的钩子输入翻成统一结构，并把决定渲染成各家要求的输出。
// 适配器只做格式翻译，不含任何策略逻辑。
package adapter

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/railse/railse-opc/internal/policy"
	"github.com/railse/railse-opc/internal/proto"
)

// Kind 是翻译结果的种类。
type Kind string

const (
	KindPermission Kind = "permission"  // 需要决定
	KindQuestion   Kind = "question"    // agent 在等人
	KindDone       Kind = "done"        // 一轮结束
	KindSessionEnd Kind = "session_end" // 会话退出
	KindIgnore     Kind = "ignore"      // 认识但不关心的事件
)

// PayloadOther 表示适配器认不出的工具，引擎按 unknown 处理。
const PayloadOther policy.PayloadType = "other"

// DeadlineSeconds 是守护进程对一条审批请求的等待上限，比各 CLI 的 24 小时钩子超时短 10 秒。
const DeadlineSeconds = 86390

// previewLimit 是改动预览的最大字节数。
const previewLimit = 4096

// Input 是钩子 stdin 翻译后的统一结构。
type Input struct {
	Kind     Kind
	Session  proto.Session
	ToolName string
	Payload  policy.Payload // 仅 KindPermission
	Message  string         // question、done 的摘要
}

// Decision 是守护进程的答复。Action 为 allow、deny 或 passthrough。
type Decision struct {
	Action string
	Reason string
}

// Output 是写回 CLI 的内容。
type Output struct {
	Stdout   []byte
	ExitCode int
}

// Adapter 是一个 CLI 的翻译器。
type Adapter interface {
	Name() string
	Parse(stdin []byte) (Input, error)
	Render(d Decision) Output
	DeadlineSeconds() int
}

var registry = map[string]Adapter{}

func register(a Adapter) { registry[a.Name()] = a }

// Get 按名字取适配器：claude、codex、gemini。
func Get(name string) (Adapter, bool) {
	a, ok := registry[name]
	return a, ok
}

// passthrough 是“不作决定”：stdout 为空，退出 0。
func passthrough() Output { return Output{Stdout: nil, ExitCode: 0} }

// permissionJSON 是 Claude 与 Codex 共用的 PermissionRequest 输出。
func permissionJSON(behavior, message string) []byte {
	type decision struct {
		Behavior string `json:"behavior"`
		Message  string `json:"message,omitempty"`
	}
	type hso struct {
		HookEventName string   `json:"hookEventName"`
		Decision      decision `json:"decision"`
	}
	out := struct {
		HookSpecificOutput hso `json:"hookSpecificOutput"`
	}{hso{"PermissionRequest", decision{behavior, message}}}
	b, _ := json.Marshal(out)
	return b
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

var urlRe = regexp.MustCompile(`https?://[^\s'"<>]+`)

// toolInput 是各家 tool_input 里可能出现的字段，缺的留空。
type toolInput struct {
	Command      string `json:"command"`
	FilePath     string `json:"file_path"`
	Path         string `json:"path"`
	NotebookPath string `json:"notebook_path"`
	Content      string `json:"content"`
	NewString    string `json:"new_string"`
	NewSource    string `json:"new_source"`
	Patch        string `json:"patch"`
	URL          string `json:"url"`
	Prompt       string `json:"prompt"`
	Edits        []struct {
		NewString string `json:"new_string"`
	} `json:"edits"`
}

// mapTool 把工具名和 tool_input 翻成统一载荷。cli 决定命名习惯。
// 只有内置工具才把 tool_input 解析成 toolInput：MCP 参数原样放进 Args，未知工具不看参数，
// 免得第三方工具里同名异型的字段让整条请求解析失败。
func mapTool(cli, toolName string, raw json.RawMessage, cwd string) (policy.Payload, error) {
	if server, tool, ok := mcpName(cli, toolName); ok {
		return policy.Payload{Type: policy.PayloadMCP, Server: server, Tool: tool, Args: raw}, nil
	}
	switch toolName {
	case "Bash", "run_shell_command", "shell", "exec_command":
		ti, err := decodeToolInput(raw)
		if err != nil {
			return policy.Payload{}, err
		}
		return policy.Payload{Type: policy.PayloadShell, Command: ti.Command, Cwd: cwd}, nil
	case "Edit", "Write", "MultiEdit", "NotebookEdit", "write_file", "replace", "apply_patch":
		ti, err := decodeToolInput(raw)
		if err != nil {
			return policy.Payload{}, err
		}
		path := ti.FilePath
		if path == "" {
			path = ti.Path
		}
		if path == "" {
			path = ti.NotebookPath
		}
		preview := ti.Content
		if preview == "" {
			preview = ti.NewString
		}
		if preview == "" {
			preview = ti.NewSource
		}
		if preview == "" {
			preview = ti.Patch
		}
		if preview == "" && len(ti.Edits) > 0 {
			preview = ti.Edits[0].NewString
		}
		return policy.Payload{Type: policy.PayloadFileWrite, Path: path, DiffPreview: clip(preview, previewLimit), Cwd: cwd}, nil
	case "WebFetch", "web_fetch":
		ti, err := decodeToolInput(raw)
		if err != nil {
			return policy.Payload{}, err
		}
		u := ti.URL
		if u == "" {
			u = urlRe.FindString(ti.Prompt)
		}
		return policy.Payload{Type: policy.PayloadNetwork, URL: u, Method: "GET"}, nil
	case "WebSearch", "google_web_search":
		return policy.Payload{Type: policy.PayloadNetwork, URL: ""}, nil
	default:
		return policy.Payload{Type: PayloadOther, Tool: toolName}, nil
	}
}

// decodeToolInput 把内置工具的 tool_input 解析成 toolInput，输入为空时得到零值。
func decodeToolInput(raw json.RawMessage) (toolInput, error) {
	var ti toolInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &ti); err != nil {
			return toolInput{}, fmt.Errorf("解析 tool_input: %w", err)
		}
	}
	return ti, nil
}

// mcpName 识别 MCP 工具名：Claude 为 mcp__<server>__<tool>，Gemini 0.65 为 mcp_<server>_<tool>，Gemini 0.28 为 <server>__<tool>。
func mcpName(cli, name string) (server, tool string, ok bool) {
	switch {
	case strings.HasPrefix(name, "mcp__"):
		rest := name[len("mcp__"):]
		if s, t, found := strings.Cut(rest, "__"); found && s != "" && t != "" {
			return s, t, true
		}
	case cli == "gemini" && strings.HasPrefix(name, "mcp_"):
		rest := name[len("mcp_"):]
		if s, t, found := strings.Cut(rest, "_"); found && s != "" && t != "" {
			return s, t, true
		}
	case cli == "gemini" && strings.Contains(name, "__"):
		if s, t, found := strings.Cut(name, "__"); found && s != "" && t != "" {
			return s, t, true
		}
	}
	return "", "", false
}
