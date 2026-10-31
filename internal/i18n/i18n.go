// Package i18n 把中文原文翻成当前语言。中文原文就是键：代码里照常写中文，英文表 en.go 查不到就原样返回，
// 没翻到的地方只是显示中文，不会崩。语言在进程里解析一次（见 resolve），Set 可覆盖。
package i18n

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

const (
	ZH       = "zh"
	EN       = "en"
	EnvVar   = "RAILSE_LANG"
	FileName = "lang"
)

var (
	mu   sync.RWMutex
	cur  string // "" 表示还没解析
	from string // env、file、locale、default、set
)

// Lang 返回当前语言：ZH 或 EN。
func Lang() string {
	l, _ := LangSource()
	return l
}

// LangSource 返回语言与它从哪来：env（RAILSE_LANG）、file（数据目录的 lang 文件）、locale（LC_ALL/LC_MESSAGES/LANG）、
// default（什么都没有，按英文）、set（Set 指定）。
func LangSource() (lang, source string) {
	mu.RLock()
	if cur != "" {
		defer mu.RUnlock()
		return cur, from
	}
	mu.RUnlock()
	mu.Lock()
	defer mu.Unlock()
	if cur == "" {
		cur, from = resolve(os.Getenv, DefaultDir())
	}
	return cur, from
}

// Set 直接指定语言（railse lang 与测试用）；不是 zh 开头的一律按 en。
func Set(lang string) {
	mu.Lock()
	defer mu.Unlock()
	cur, from = normalize(lang), "set"
}

// Reset 清掉已解析的结果，下次 Lang 重新解析（测试用）。
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	cur, from = "", ""
}

func normalize(s string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(s)), "zh") {
		return ZH
	}
	return EN
}

// resolve 按 RAILSE_LANG → <dir>/lang → LC_ALL、LC_MESSAGES、LANG → 英文 的顺序定语言。
// 什么都没有算英文：英文系统的终端几乎都带 LANG，容器里常常没有；中文用户跑一次 railse lang zh 即可。
func resolve(getenv func(string) string, dir string) (lang, source string) {
	if v := getenv(EnvVar); strings.TrimSpace(v) != "" {
		return normalize(v), "env"
	}
	if dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, FileName)); err == nil && strings.TrimSpace(string(b)) != "" {
			return normalize(string(b)), "file"
		}
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := getenv(k); strings.TrimSpace(v) != "" {
			return normalize(v), "locale"
		}
	}
	return EN, "default"
}

// DefaultDir 是守护进程的数据目录：$RAILSE_HOME，否则 ~/.railse；拿不到家目录时为空（只影响 lang 文件的读写）。
// 与 internal/agent.DefaultConfig 的规则相同，这里不引用它以免成环。
func DefaultDir() string {
	if d := os.Getenv("RAILSE_HOME"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".railse")
}

// WriteFile 把语言写进 <dir>/lang；目录不存在就建（0700，与守护进程数据目录一致）。
func WriteFile(dir, lang string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, FileName), []byte(normalize(lang)+"\n"), 0o644)
}

// T 把中文原文翻成当前语言；英文表里没有的原样返回。
// 参数必须是字符串字面量：catalog_test 靠它找出所有键并核对英文表。
func T(zh string) string {
	if Lang() == ZH {
		return zh
	}
	if s, ok := en[zh]; ok {
		return s
	}
	return zh
}

// Lookup 与 T 相同，但允许传变量：只用于守护进程回传给钩子的固定句子（见 cmd/railse/hook.go 的 localizeReason）。
// 完整性测试不查它，所以它可能用到的每个固定句子都要手工列进 en.go。
func Lookup(s string) string { return T(s) }

var verbRe = regexp.MustCompile(`%[-+# 0]*\d*(?:\.\d+)?[a-zA-Z%]`)

// fmtVerbs 列出格式串里的 % 动词（%% 也算），测试用。
func fmtVerbs(s string) []string { return verbRe.FindAllString(s, -1) }
