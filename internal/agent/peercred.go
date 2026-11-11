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

// 请求的祖先链从哪来（审计的 ancestry 字段）：对端进程凭据重建的（peer）；取不到凭据时不信自报的，标 peer_unverified；
// self_reported 只在测试里打开 trustSelfReported 时出现。
const (
	ancestryPeer         = "peer"
	ancestrySelfReported = "self_reported"
	ancestryUnverified   = "peer_unverified"
)

// trustSelfReported 只给测试用（见 SetTrustSelfReportedForTest）：取不到对端凭据时用请求里自报的祖先链。生产代码里永远为假：
// 自报的谁都能填，拿它“放宽”（报一条没有上层会话的链）就绕开了继承。
var trustSelfReported = false

// unverifiedReason 是取不到对端凭据、又有角色文件时，审批按越界处理的理由（固定句子，钩子 i18n.Lookup）。
const unverifiedReason = "取不到钩子进程的凭据，无法核实有没有上层会话的角色"

// peerAncestry 用连接对端的进程凭据（macOS LOCAL_PEERPID、Linux SO_PEERCRED）取钩子进程号，自己重建它的祖先链；
// 取不到时 ok 为假。请求里自报的祖先链谁都能填，能取到凭据时一律不用。是变量只为测试能换。
var peerAncestry = func(conn net.Conn) ([]proto.Proc, bool) {
	pid, ok := peerPID(conn)
	if !ok {
		return nil, false
	}
	return ReadAncestorsFrom(pid), true
}
