package policy

import "encoding/json"

// PayloadType 是统一载荷的四种形态。
type PayloadType string

const (
	PayloadShell     PayloadType = "shell"
	PayloadFileWrite PayloadType = "file_write"
	PayloadMCP       PayloadType = "mcp"
	PayloadNetwork   PayloadType = "network"
)

// Payload 是适配器把各家工具调用翻译后的统一载荷。不同 Type 只用各自的字段。
type Payload struct {
	Type PayloadType `json:"type"`

	// shell
	Command string `json:"command,omitempty"`
	Cwd     string `json:"cwd,omitempty"` // shell 与 file_write 共用

	// file_write
	Path        string `json:"path,omitempty"`
	DiffPreview string `json:"diff_preview,omitempty"`

	// mcp
	Server string          `json:"server,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`

	// network
	URL    string `json:"url,omitempty"`
	Method string `json:"method,omitempty"`
}

// Context 是评估一条请求时的环境，由调用方提供，本包不读环境。
type Context struct {
	// ProjectRoot 是项目根目录的绝对路径。适配器必须传 git 仓库根（不在仓库里时传会话的工作区根），
	// 不能留空：为空时引擎 fail closed，认为没有任何改动在项目内，写入一律按 write_outside 询问。
	ProjectRoot string
	HomeDir     string // 用户家目录绝对路径
	MachineID   string // 本机 id，用于 machine 范围的规则
}
