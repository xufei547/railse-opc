package doctor

import (
	"os"
	"testing"

	"github.com/xufei547/railse-opc/internal/i18n"
)

// TestMain 钉死中文：不带 RAILSE_LANG 直接 go test 也是中文，且不读真实的 ~/.railse/lang。
func TestMain(m *testing.M) {
	os.Setenv("RAILSE_LANG", "zh")
	i18n.Set(i18n.ZH)
	os.Exit(m.Run())
}
