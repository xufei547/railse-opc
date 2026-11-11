//go:build unix

package agent

import (
	"os"
	"syscall"
)

// openNoBlock 只读打开，带 O_NONBLOCK：路径是 FIFO 时不会卡在 open 上（之后由调用方按句柄的类型拒读）。
func openNoBlock(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
}
