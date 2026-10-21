package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type state struct {
	Paused bool `json:"paused"`
}

// loadState 读 state.json。文件不存在时返回零值（未暂停）。
// 读不了或解析失败（包括截断后留下的空文件）时失败关闭：返回 Paused=true 和错误，调用方记日志后按暂停启动。
func loadState(path string) (state, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return state{Paused: true}, fmt.Errorf("读状态文件 %s，按暂停处理: %w", path, err)
	}
	var st state
	if err := json.Unmarshal(b, &st); err != nil {
		return state{Paused: true}, fmt.Errorf("状态文件 %s 损坏，按暂停处理: %w", path, err)
	}
	return st, nil
}

// saveState 先写同目录的临时文件（0600）再 rename，中途失败或崩溃不会留下截断的 state.json。
func saveState(path string, st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(b)
	if err == nil {
		err = f.Chmod(0o600) // CreateTemp 已是 0600，这里再明确一次
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
	}
	return err
}
