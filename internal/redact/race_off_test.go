//go:build !race

package redact

// raceEnabled 为 true 时测试在 -race 下运行，耗时测试改用较小的输入。
const raceEnabled = false
