package agent

import (
	"encoding/json"
	"errors"
	"os"
)

type state struct {
	Paused bool `json:"paused"`
}

func loadState(path string) (state, error) {
	var st state
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

func saveState(path string, st state) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	// WriteFile 的 0600 只对新建文件生效，已有文件在这里收紧。
	return os.Chmod(path, 0o600)
}
