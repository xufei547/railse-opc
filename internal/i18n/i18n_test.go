package i18n

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveOrder(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name   string
		env    map[string]string
		file   string
		want   string
		source string
	}{
		{"环境变量最优先", map[string]string{"RAILSE_LANG": "zh", "LANG": "en_US.UTF-8"}, "en", "zh", "env"},
		{"其次 lang 文件", map[string]string{"LANG": "en_US.UTF-8"}, "zh", "zh", "file"},
		{"再次系统 LANG", map[string]string{"LANG": "zh_CN.UTF-8"}, "", "zh", "locale"},
		{"LC_ALL 优先于 LANG", map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "zh_CN.UTF-8"}, "", "en", "locale"},
		{"C 算英文", map[string]string{"LANG": "C"}, "", "en", "locale"},
		{"什么都没有算英文", map[string]string{}, "", "en", "default"},
		{"文件里是空白当没有", map[string]string{}, "  \n", "en", "default"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(dir, FileName)
			os.Remove(p)
			if c.file != "" {
				if err := os.WriteFile(p, []byte(c.file), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			getenv := func(k string) string { return c.env[k] }
			lang, source := resolve(getenv, dir)
			if lang != c.want || source != c.source {
				t.Fatalf("resolve = %s/%s，想要 %s/%s", lang, source, c.want, c.source)
			}
		})
	}
}

func TestTFallsBackToChinese(t *testing.T) {
	Set(EN)
	t.Cleanup(Reset)
	if got := T("这句话没有翻译"); got != "这句话没有翻译" {
		t.Fatalf("没翻的要原样返回，得到 %q", got)
	}
	en["测试用的键"] = "test key"
	t.Cleanup(func() { delete(en, "测试用的键") })
	if got := T("测试用的键"); got != "test key" {
		t.Fatalf("T = %q", got)
	}
	Set(ZH)
	if got := T("测试用的键"); got != "测试用的键" {
		t.Fatalf("中文下 T 要原样返回，得到 %q", got)
	}
}

func TestSetNormalizes(t *testing.T) {
	t.Cleanup(Reset)
	for in, want := range map[string]string{"zh": ZH, "ZH_cn": ZH, "en": EN, "fr": EN, " zh_TW.UTF-8 ": ZH} {
		Set(in)
		if got := Lang(); got != want {
			t.Fatalf("Set(%q) → %s，想要 %s", in, got, want)
		}
	}
}

func TestWriteFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	if err := WriteFile(dir, "zh_CN"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "zh\n" {
		t.Fatalf("文件内容 %q，想要 zh 加换行", b)
	}
}

// 英文值的 % 动词要和键一样多、一样序：翻译时漏掉或调换占位符会让 Sprintf 出错。
func TestCatalogVerbsMatch(t *testing.T) {
	for zh, e := range en {
		if a, b := fmtVerbs(zh), fmtVerbs(e); !equalStrings(a, b) {
			t.Errorf("占位符不一致：%q 有 %v，英文 %q 有 %v", zh, a, e, b)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
