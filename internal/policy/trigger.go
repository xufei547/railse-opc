package policy

import "strings"

// Trigger 是定级时起决定作用的那一处：命令里哪一段（或哪个路径、网址）、什么性质。只给人看，不参与规则匹配。
// Why 是固定短语的中文原文（即英文表的键），Segment 是触发那一段的原文（第一行，压掉多余空白，至多 80 个字符）；
// file_write 的 Segment 是路径，network 的是网址或 server/tool。定级为 read、write_in_project 时没有触发点（nil）。
type Trigger struct {
	Segment string `json:"segment,omitempty"`
	Why     string `json:"why"`
}

// Why 的取值：中文原文即键，英文见 internal/i18n/en.go 与 web/src/locales/en.ts（两份必须一致，i18n.sync.test.ts 核对）。
const (
	WhyDelete       = "删除文件，删了不可恢复"
	WhyKill         = "结束进程"
	WhySudo         = "以管理员权限运行"
	WhySensitive    = "读写敏感文件（密钥、配置或开机自启）"
	WhyPipedScript  = "把脚本直接交给解释器执行"
	WhyDownloadRun  = "下载脚本并直接执行"
	WhySQLDrop      = "删表或删库"
	WhyForcePush    = "强制推送，会覆盖远程历史"
	WhyPush         = "把代码推到远程仓库"
	WhyDeploy       = "发布或部署到外部"
	WhySendData     = "向外发送数据"
	WhyNetwork      = "要联网"
	WhyWriteOutside = "改项目外的文件"
	WhyUnknown      = "这条命令不在已知名单里"
	WhyDestructive  = "不可逆的操作"      // 兜底：定为 destructive 但没有更具体的说法
	WhyOutward      = "影响仓库之外的人和系统" // 兜底：定为 outward 但没有更具体的说法
)

// AllWhys 是全部 Why 短语，供测试核对英文表。
var AllWhys = []string{WhyDelete, WhyKill, WhySudo, WhySensitive, WhyPipedScript, WhyDownloadRun, WhySQLDrop, WhyForcePush, WhyPush,
	WhyDeploy, WhySendData, WhyNetwork, WhyWriteOutside, WhyUnknown, WhyDestructive, WhyOutward}

// compactSegment 把一段原文变成给人看的一行：只取第一行（heredoc 正文不要），空白压成一个空格，超过 80 个字符截断加 …。
func compactSegment(text string) string {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		text = text[:i]
	}
	text = strings.Join(strings.Fields(text), " ")
	r := []rune(text)
	if len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return text
}
