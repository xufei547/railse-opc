package policy

import (
	"encoding/json"
	"path/filepath"
)

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
	// ProjectRoot 是项目根目录的绝对路径，由守护进程按请求的 cwd 定（见 agent.ResolveProjectRoot）：git 仓库根，
	// 不在 git 仓库里时是 cwd 自身，cwd 是家目录、家目录的祖先或根目录时为空。
	// 为空时引擎 fail closed，认为没有任何改动在项目内，写入一律按 write_outside 询问；
	// 是家目录、家目录的祖先或根目录时同样按空处理，见 Root。
	ProjectRoot string
	HomeDir     string // 用户家目录绝对路径
	MachineID   string // 本机 id，用于 machine 范围的规则
}

// Root 返回生效的项目根：ProjectRoot 清理后是根目录、HomeDir 或 HomeDir 的祖先时返回空串（太宽，当它是项目根会让
// “项目内可写”放行改写整个家目录），否则原样返回 ProjectRoot。HomeDir 为空时只挡根目录。只比较清理后的路径字符串，
// 不读文件系统。导出入口（Classify、Evaluate、ProposeRule）都先用它规范化 Context；hub 判断有没有项目根也用它，口径一致。
func (c Context) Root() string {
	if c.ProjectRoot == "" {
		return ""
	}
	root := filepath.Clean(c.ProjectRoot)
	if filepath.Dir(root) == root || (c.HomeDir != "" && isWithin(filepath.Clean(c.HomeDir), root)) {
		return ""
	}
	return c.ProjectRoot
}

// normalized 返回把 ProjectRoot 换成 Root() 的副本。
func (c Context) normalized() Context {
	c.ProjectRoot = c.Root()
	return c
}
