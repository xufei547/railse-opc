//go:build !unix

package agent

import "os"

// openNoBlock 在非 unix 平台上就是普通的只读打开（没有 FIFO 卡住的问题；发布只有 darwin、linux）。
func openNoBlock(path string) (*os.File, error) { return os.Open(path) }
