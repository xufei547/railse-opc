//go:build !darwin && !linux

package agent

import "github.com/xufei547/railse-opc/internal/proto"

// 其他平台取不到祖先链：守护进程按没有祖先处理。
func procInfo(pid int) (proto.Proc, int, bool) { return proto.Proc{}, 0, false }

// procGone 在其他平台上判断不了，按还在。
func procGone(pid int) bool { return false }
