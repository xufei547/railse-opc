// Package autostart 是开机自启托管文件的命名约定（macOS launchd 用户代理、Linux systemd 单元）：
// railse setup autostart 按它写托管文件，railse doctor 按它只读地检查托管文件在不在。kind 为 agent 或 hub。
package autostart

import "path/filepath"

// SystemUnitDir 是 systemd 系统单元所在目录。是变量只为测试能换成临时目录。
var SystemUnitDir = "/etc/systemd/system"

// Label 是 launchd 作业的标签。
func Label(kind string) string { return "dev.railse." + kind }

// PlistPath 是 launchd 用户代理的 plist 路径。
func PlistPath(home, kind string) string {
	return filepath.Join(home, "Library", "LaunchAgents", Label(kind)+".plist")
}

// UnitName 是 systemd 单元名，systemctl 用它指代单元。
func UnitName(kind string) string { return "railse-" + kind + ".service" }

// UnitPath 是 systemd 单元文件路径：system 为真时是系统单元，否则是 home 下的用户单元。
func UnitPath(home, kind string, system bool) string {
	if system {
		return filepath.Join(SystemUnitDir, UnitName(kind))
	}
	return filepath.Join(home, ".config", "systemd", "user", UnitName(kind))
}
