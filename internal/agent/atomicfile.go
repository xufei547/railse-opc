package agent

import (
	"os"
	"path/filepath"
)

// writeFileAtomic 先写同目录临时文件（0600）并 fsync，再 rename 覆盖目标；中途失败或崩溃
// 不会留下截断的目标文件，失败时删掉临时文件。规则缓存、state.json、machine.json 共用。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(0o600) // CreateTemp 已是 0600，这里再明确一次，不受 umask 影响
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
		return err
	}
	syncDir(dir)
	return nil
}

// syncDir 对目录 fsync，让 rename 本身在掉电后也留得住。尽力而为：文件内容已落盘、rename 已成功，
// 有的平台或文件系统不支持对目录 fsync，失败时忽略，最坏是掉电后看到旧文件（仍是完整的）。
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync() // 尽力而为，见上
	d.Close()
}
