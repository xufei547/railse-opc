package main

import "github.com/xufei547/railse-opc/internal/proto"

// readAncestors 取钩子进程的祖先链：从直接父进程往上到 1 号进程为止，至多 proto.MaxAncestors 层，每层带启动时间（防进程号复用）
// 与名字。各平台不靠外部命令取（darwin 用 sysctl kern.proc.pid，linux 读 /proc），取不到（权限、平台不支持）就停在那一层，
// 一层都取不到时为空：守护进程按没有祖先处理。是变量只为测试能换。
var readAncestors = func() []proto.Proc {
	var out []proto.Proc
	pid := parentPID()
	for len(out) < proto.MaxAncestors && pid > 1 {
		p, ppid, ok := procInfo(pid)
		if !ok {
			break
		}
		out = append(out, p)
		if ppid == pid {
			break
		}
		pid = ppid
	}
	return out
}
