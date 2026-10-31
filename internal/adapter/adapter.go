// Package adapter 把各家 agent CLI 的钩子输入翻成统一结构，并把决定渲染成各家要求的输出。
// 适配器只做格式翻译，不含任何策略逻辑。
package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/xufei547/railse-opc/internal/policy"
	"github.com/xufei547/railse-opc/internal/proto"
)

// Kind 是翻译结果的种类。
type Kind string

const (
	KindPermission Kind = "permission"  // 需要决定
	KindQuestion   Kind = "question"    // agent 在等人
	KindDone       Kind = "done"        // 一轮结束
	KindSessionEnd Kind = "session_end" // 会话退出
	KindAsk        Kind = "ask"         // agent 要人答题（Claude 的 AskUserQuestion）
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

	Ask          *proto.Ask      // 仅 KindAsk
	RawToolInput json.RawMessage // 仅 KindAsk：PreToolUse 的 tool_input 原文，渲染答案时原样回填
}

// Decision 是守护进程的答复。Action 为 allow、deny、passthrough 或 answer。
// answer 带 Answers（问题原文 → 答案）和 KindAsk 时拿到的 RawToolInput。
type Decision struct {
	Action       string
	Reason       string
	Answers      map[string]string
	RawToolInput json.RawMessage
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

// renderPermission 把决定渲染成 Claude 与 Codex 共用的 PermissionRequest 输出：
// allow、deny 写 JSON，其余为 passthrough。
func renderPermission(d Decision) Output {
	switch d.Action {
	case "allow":
		return Output{Stdout: permissionJSON("allow", ""), ExitCode: 0}
	case "deny":
		return Output{Stdout: permissionJSON("deny", denyMessage(d.Reason)), ExitCode: 0}
	default:
		return passthrough()
	}
}

// 题目各项的上限，防止一道题把守护进程到 hub 的 1 MiB 帧撑爆；长度按字符（rune）计。
const (
	askMaxQuestions   = 4
	askMaxOptions     = 8
	askMaxQuestionLen = 2000
	askMaxLabelLen    = 200
	askMaxDescLen     = 2000
	askMaxHeaderLen   = 60
)

// ValidateAsk 校验 AskUserQuestion 的题目：1 到 4 题；每题 question 非空且不超过 2000 字符；
// options 0 到 8 个，label 非空且不超过 200 字符，description 不超过 2000 字符；header 不超过 60 字符。
// 不合规返回说明原因的错误，调用方当作不认识这条请求，让终端照常问。
func ValidateAsk(a *proto.Ask) error {
	if a == nil || len(a.Questions) == 0 {
		return fmt.Errorf("AskUserQuestion 的题目不合规: 没有题目")
	}
	if len(a.Questions) > askMaxQuestions {
		return fmt.Errorf("AskUserQuestion 的题目不合规: 有 %d 题，最多 %d 题", len(a.Questions), askMaxQuestions)
	}
	for i, q := range a.Questions {
		n := i + 1
		if q.Question == "" {
			return fmt.Errorf("AskUserQuestion 的题目不合规: 第 %d 题 question 为空", n)
		}
		if utf8.RuneCountInString(q.Question) > askMaxQuestionLen {
			return fmt.Errorf("AskUserQuestion 的题目不合规: 第 %d 题 question 超过 %d 字符", n, askMaxQuestionLen)
		}
		if utf8.RuneCountInString(q.Header) > askMaxHeaderLen {
			return fmt.Errorf("AskUserQuestion 的题目不合规: 第 %d 题 header 超过 %d 字符", n, askMaxHeaderLen)
		}
		if len(q.Options) > askMaxOptions {
			return fmt.Errorf("AskUserQuestion 的题目不合规: 第 %d 题有 %d 个选项，最多 %d 个", n, len(q.Options), askMaxOptions)
		}
		for j, o := range q.Options {
			if o.Label == "" {
				return fmt.Errorf("AskUserQuestion 的题目不合规: 第 %d 题第 %d 个选项 label 为空", n, j+1)
			}
			if utf8.RuneCountInString(o.Label) > askMaxLabelLen {
				return fmt.Errorf("AskUserQuestion 的题目不合规: 第 %d 题第 %d 个选项 label 超过 %d 字符", n, j+1, askMaxLabelLen)
			}
			if utf8.RuneCountInString(o.Description) > askMaxDescLen {
				return fmt.Errorf("AskUserQuestion 的题目不合规: 第 %d 题第 %d 个选项 description 超过 %d 字符", n, j+1, askMaxDescLen)
			}
		}
	}
	return nil
}

// preToolUseAnswer 把 answers 合进 rawToolInput（JSON 对象，其他字段逐字节不动，已有 answers 键则覆盖），
// 并包成 PreToolUse 的 allow 输出，Claude Code 据此不再在终端弹题。
// 对象由手工拼接：encoding/json 编码 RawMessage 时即使关掉 HTML 转义，也会把 U+2028、U+2029 改写成 \uXXXX，
// 字段值就不再逐字节相同了。键按字典序排，输出稳定。
func preToolUseAnswer(rawToolInput json.RawMessage, answers map[string]string) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rawToolInput, &fields); err != nil {
		return nil, fmt.Errorf("解析 tool_input: %w", err)
	}
	if fields == nil { // tool_input 是 null
		return nil, fmt.Errorf("tool_input 不是 JSON 对象")
	}
	ab, err := marshalNoEscape(answers)
	if err != nil {
		return nil, err
	}
	fields["answers"] = ab
	keys := make([]string, 0, len(fields))
	for k := range fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	buf.WriteString(`{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"allow","updatedInput":{`)
	for i, k := range keys {
		if i > 0 {
			buf.WriteByte(',')
		}
		kb, err := marshalNoEscape(k)
		if err != nil {
			return nil, err
		}
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(fields[k])
	}
	buf.WriteString(`}}}`)
	return buf.Bytes(), nil
}

// marshalNoEscape 编成 JSON，不做 HTML 转义，也不带 Encoder 追加的换行。
func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// denyMessage 是拒绝时给 agent 看的理由，带 Railse 前缀；理由为空时用固定文案。
func denyMessage(reason string) string {
	if reason == "" {
		return "Railse：已拒绝"
	}
	return "Railse：" + reason
}

// clip 把 s 截到不超过 n 字节，并退到字符边界，不切开多字节字符。
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
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
