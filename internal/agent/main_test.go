package agent

import (
	"os"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
)

// TestMain 把语言钉成中文：本包的报错（与它调用的 adapter.ValidateAsk）过 i18n.T，现有测试断言的是中文；
// 也不去读真实的 ~/.railse/lang。本包的内部测试与 agent_test 外部测试编进同一个测试二进制，这一个 TestMain 管全部。
func TestMain(m *testing.M) {
	os.Setenv("RAILSE_LANG", "zh")
	i18n.Set(i18n.ZH)
	os.Exit(m.Run())
}
