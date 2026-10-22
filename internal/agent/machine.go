package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
)

// MachineConfig 是本机在 hub 的身份，由 railse agent enroll 写入。
// Secret 是长期密钥：任何日志与错误信息都不得带出它（包括 json 解码错误里引用的原文）。
type MachineConfig struct {
	HubURL    string `json:"hub_url"`
	MachineID string `json:"machine_id"`
	Secret    string `json:"secret"`
	Name      string `json:"name"`
}

// LoadMachine 读 machine.json；不存在时返回的错误满足 errors.Is(err, os.ErrNotExist)。
// 内容损坏或 hub_url 不是 http(s)://主机 形式时报错。
func LoadMachine(path string) (MachineConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return MachineConfig{}, err
	}
	var m MachineConfig
	if err := json.Unmarshal(b, &m); err != nil {
		// 不转述 json 的原始错误：语法错误会引用出错处的字符，类型错误会带出数字原文，都可能来自密钥。
		var se *json.SyntaxError
		if errors.As(err, &se) {
			return MachineConfig{}, fmt.Errorf("%s 不是合法的 JSON（第 %d 字节附近），请重新 railse agent enroll", path, se.Offset)
		}
		return MachineConfig{}, fmt.Errorf("%s 格式不对（字段类型与预期不符），请重新 railse agent enroll", path)
	}
	if err := m.validate(); err != nil {
		return MachineConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

// SaveMachine 校验后以 0600 原子写入（临时文件 fsync 后 rename）。
func SaveMachine(path string, m MachineConfig) error {
	if err := m.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, append(b, '\n'))
}

// validate 只接受 http、https 且带主机名的 hub_url。错误信息不带 URL 原文（可能含 user:pass）与 Secret。
func (m MachineConfig) validate() error {
	u, err := url.Parse(m.HubURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return errors.New("hub_url 无效：应为 http:// 或 https:// 开头并带主机名")
	}
	return nil
}
