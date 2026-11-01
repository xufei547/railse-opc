package ok

import "example.com/x/i18n"

// 常量在同包另一个文件里，也要认。
func g() { _ = i18n.T(usage) }
