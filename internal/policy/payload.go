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
	// shell：agent 自己写的一句话说明（Claude Code 的 tool_input.description），给人看，不参与定级；已截断、随载荷脱敏
	Description string `json:"description,omitempty"`

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
	// ProjectRoot 是项目根目录的绝对路径，由守护进程按请求的 cwd 定（见 agent.ResolveProjectRoot）：git 仓库根；
	// 不在 git 仓库里时，cwd 是家目录下的普通目录（不在点开头的目录或 ~/Library 之下）才是 cwd 自身，否则为空；
	// 在系统目录里（/opt、/usr、/etc、/var、/System、/Library、/private 及其下面，第一段以点开头的也算；/private/tmp、
	// 守护进程的临时目录与家目录按条件除外）时为空，git 仓库也一样；cwd 是家目录、家目录的祖先或根目录时为空。
	// 为空时引擎 fail closed，认为没有任何改动在项目内，写入一律按 write_outside 询问；
	// 是家目录、家目录的祖先或根目录时同样按空处理，见 Root。本包分不出 git 仓库根与按 cwd 定的根，只做这一道守卫。
	ProjectRoot string
	HomeDir     string // 用户家目录绝对路径
	MachineID   string // 本机 id，用于 machine 范围的规则
	// Role 是会话的角色（0.3 起），nil 为未分配，按 0.2 的规则走。由守护进程按 RAILSE_ROLE 与项目目录解析（见 agent.ResolveRole）；
	// roles.yaml 有误时是 BrokenRole（上限 read）。角色写了 dirs 而项目根不在其内时，Root 按没有项目根处理。
	Role *Role
	// ProtectedDirs 是守护进程自己的数据目录（roles.yaml、rules.yaml、trusts.json 所在，绝对路径），由守护进程带入：里面的读写与
	// 敏感路径表一样按 destructive。默认的 ~/.railse 已在敏感路径表里；用 --dir 或 RAILSE_HOME 指到别处时靠它，免得 agent 改角色、
	// 规则与放行表被当成项目内写入自动放行。比较同敏感路径表（折叠大小写），不解析符号链接（守护进程把真实路径也带上）。
	ProtectedDirs []string
}

// sensitive 判断绝对路径是否命中敏感路径表或在受保护目录内。
func (c Context) sensitive(abs string) bool {
	if isSensitive(abs, c.HomeDir) {
		return true
	}
	fa := foldPath(abs)
	for _, d := range c.ProtectedDirs {
		if d != "" && isWithin(fa, foldPath(filepath.Clean(d))) {
			return true
		}
	}
	return false
}

// Root 返回生效的项目根：ProjectRoot 清理后是根目录、HomeDir 或 HomeDir 的祖先时返回空串，有角色且项目根在角色地盘外时也返回空串（太宽，当它是项目根会让
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
	// 角色的地盘（设计 §3.3）：写了 dirs 的角色在地盘外（典型：用 RAILSE_ROLE 在别处启动）没有项目根，写入一律 write_outside。
	// 地盘内项目根语义不变，不扩大到角色的全部目录。
	if c.Role != nil && len(c.Role.Dirs) > 0 && !c.Role.Covers(c.ProjectRoot, c.HomeDir) {
		return ""
	}
	return c.ProjectRoot
}

// normalized 返回把 ProjectRoot 换成 Root() 的副本。
func (c Context) normalized() Context {
	c.ProjectRoot = c.Root()
	return c
}
