//go:build darwin

package agent

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/xufei547/railse-opc/internal/proto"
)

// procInfo 用 sysctl kern.proc.pid 取一个进程：启动时间（秒.微秒）、名字（p_comm，至多 16 字节）与父进程号。
func procInfo(pid int) (proto.Proc, int, bool) {
	k, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || k == nil || int(k.Proc.P_pid) != pid {
		return proto.Proc{}, 0, false
	}
	tv := k.Proc.P_starttime
	name := unix.ByteSliceToString(k.Proc.P_comm[:])
	return proto.Proc{PID: pid, Start: fmt.Sprintf("%d.%06d", tv.Sec, tv.Usec), Name: name}, int(k.Eproc.Ppid), true
}

// procGone 报告进程号 pid 已经没有进程（kill(pid, 0) 回 ESRCH）；没权限等其他情况按还在。
func procGone(pid int) bool {
	return pid > 0 && errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
