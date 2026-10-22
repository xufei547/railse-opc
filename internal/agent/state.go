package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// state 是 state.json。Paused 是本机 railse agent pause/resume 的开关，HubPaused 是 hub 下发的总开关；
// 两者分开存，生效值取或。第 2 份计划写的旧文件没有 hub_paused，按 false 读。
type state struct {
	Paused    bool `json:"paused"`
	HubPaused bool `json:"hub_paused"`
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

// saveState 原子写入（见 writeFileAtomic），中途失败或崩溃不会留下截断的 state.json。
func saveState(path string, st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, b)
}
