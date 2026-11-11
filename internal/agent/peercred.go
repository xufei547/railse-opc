package agent

import (
	"net"
	"syscall"

	"github.com/xufei547/railse-opc/internal/proto"
)

// sockoptPID 在连接的文件描述符上调 get 取对端进程号。
func sockoptPID(conn net.Conn, get func(fd int) (int, error)) (int, bool) {
	sc, ok := conn.(syscall.Conn)
	if !ok {
		return 0, false
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return 0, false
	}
	pid, gerr := 0, error(nil)
	if err := raw.Control(func(fd uintptr) { pid, gerr = get(int(fd)) }); err != nil || gerr != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

// 请求的祖先链从哪来（审计的 ancestry 字段）：对端进程凭据重建的，或请求里自报的（取不到凭据时）。
const (
	ancestryPeer         = "peer"
	ancestrySelfReported = "self_reported"
)

// peerAncestry 用连接对端的进程凭据（macOS LOCAL_PEERPID、Linux SO_PEERCRED）取钩子进程号，自己重建它的祖先链；
// 取不到时 ok 为假。请求里自报的祖先链谁都能填，能取到凭据时一律不用。是变量只为测试能换。
var peerAncestry = func(conn net.Conn) ([]proto.Proc, bool) {
	pid, ok := peerPID(conn)
	if !ok {
		return nil, false
	}
	return ReadAncestorsFrom(pid), true
}
