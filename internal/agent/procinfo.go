package agent

import "github.com/xufei547/railse-opc/internal/proto"

// ReadAncestorsFrom 取进程 pid 的祖先链：从它的父进程往上到 1 号进程为止，至多 proto.MaxAncestors 层，每层带启动时间
// （防进程号复用）与名字。各平台不靠外部命令取（darwin 用 sysctl kern.proc.pid，linux 读 /proc），取不到就停在那一层。
// 钩子用它带上自己的祖先链；守护进程用它从连接对端的进程号重建祖先链（见 peerAncestry）。
func ReadAncestorsFrom(pid int) []proto.Proc {
	_, ppid, ok := procInfo(pid)
	if !ok {
		return nil
	}
	var out []proto.Proc
	for len(out) < proto.MaxAncestors && ppid > 1 {
		p, next, ok := procInfo(ppid)
		if !ok {
			break
		}
		out = append(out, p)
		if next == ppid {
			break
		}
		ppid = next
	}
	return out
}

// procAlive 报告进程 p 还在：同一进程号、同一启动时间。取不到（权限、平台不支持）或 p 没有启动时间时按还在（不清，靠 TTL 兜底）。
func procAlive(p proto.Proc) bool {
	if p.Start == "" {
		return true
	}
	cur, _, ok := procInfo(p.PID)
	if !ok {
		return !procGone(p.PID)
	}
	return cur.Start == p.Start
}
