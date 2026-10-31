package ok

import "example.com/x/i18n"

const usage = `用法: x`

const (
	grouped = "一个常量"
)

func f() {
	_ = i18n.T("字面量")
	_ = i18n.T(`反引号字面量`)
	_ = i18n.T(usage)
	_ = i18n.T(grouped)
	_ = other.T("不是 i18n 的 T")
}
