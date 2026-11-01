package doctor

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/xufei547/railse-opc/internal/i18n"
)

// Write 按行打印结果与汇总：每项一行“[状态] 名称：细节”，warn 与 fail 下一行缩进给出“修复：”。文字按当前语言（i18n）。
func (r Report) Write(w io.Writer) {
	counts := map[Status]int{}
	for _, c := range r.Checks {
		counts[c.Status]++
		fmt.Fprintf(w, "%-7s %s", "["+string(c.Status)+"]", c.Name)
		if c.Detail != "" {
			fmt.Fprintf(w, i18n.T("：%s"), c.Detail)
		}
		fmt.Fprintln(w)
		if c.Hint != "" && (c.Status == Fail || c.Status == Warn) {
			fmt.Fprintf(w, i18n.T("        修复：%s\n"), c.Hint)
		}
	}
	fmt.Fprintf(w, i18n.T("\n%d 项通过，%d 项警告，%d 项失败，%d 项跳过（railse %s）\n"), counts[OK], counts[Warn], counts[Fail], counts[Skip], r.Version)
}

// JSON 返回机器可读的结果：字段名与 status 的取值（ok、warn、fail、skip）固定，name、detail、hint 是当前语言的文字。
func (r Report) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }
