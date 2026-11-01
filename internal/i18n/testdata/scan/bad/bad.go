package bad

import "example.com/x/i18n"

const joined = "甲" + "乙"

const usage = `用法: x`

var name = "变量"

func f(s string) {
	_ = i18n.T(name)
	_ = i18n.T(joined)
	_ = i18n.T(s + "尾")
	_ = i18n.T(fmt.Sprint("x"))
	_ = i18n.T("a", "b")
	_ = i18n.T(missingConst)
}

func localShadow(dyn string) {
	usage := dyn
	_ = i18n.T(usage)
}

func paramShadow(usage string) {
	_ = i18n.T(usage)
}

func closureShadow() {
	g := func(usage string) { _ = i18n.T(usage) }
	g("x")
}
