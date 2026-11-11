//go:build linux

package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/xufei547/railse-opc/internal/proto"
)

func parentPID() int { return os.Getppid() }

// procInfo 读 /proc/<pid>/stat：名字在括号里（可能含空格与括号，取最后一个右括号之前），其后第 2 个字段是父进程号，
// 第 20 个是启动时间（开机以来的时钟滴答数）。
func procInfo(pid int) (proto.Proc, int, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return proto.Proc{}, 0, false
	}
	s := string(b)
	open, close := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if open < 0 || close < open {
		return proto.Proc{}, 0, false
	}
	f := strings.Fields(s[close+1:])
	if len(f) < 20 {
		return proto.Proc{}, 0, false
	}
	ppid, err := strconv.Atoi(f[1])
	if err != nil {
		return proto.Proc{}, 0, false
	}
	return proto.Proc{PID: pid, Start: f[19], Name: s[open+1 : close]}, ppid, true
}
