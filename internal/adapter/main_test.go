package adapter

import (
	"os"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
)

// TestMain 把语言钉成中文：现有测试断言的是中文报错与拒绝理由；也不去读真实的 ~/.railse/lang。英文用例自己 Set(EN) 再还原。
func TestMain(m *testing.M) {
	os.Setenv("RAILSE_LANG", "zh")
	i18n.Set(i18n.ZH)
	os.Exit(m.Run())
}
