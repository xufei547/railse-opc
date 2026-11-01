package hub

import (
	"os"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
)

// TestMain 把语言钉成中文：本包有测试断言经 i18n.T 的中文句子，也不该去读真实的 ~/.railse/lang。
// 内部测试与外部测试编进同一个测试二进制，这一个 TestMain 管全部。
func TestMain(m *testing.M) {
	os.Setenv("RAILSE_LANG", "zh")
	i18n.Set(i18n.ZH)
	os.Exit(m.Run())
}
