//go:build !darwin && !linux

package main

import "github.com/xufei547/railse-opc/internal/proto"

// 其他平台取不到祖先链：守护进程按没有祖先处理。
func parentPID() int                           { return 0 }
func procInfo(pid int) (proto.Proc, int, bool) { return proto.Proc{}, 0, false }
