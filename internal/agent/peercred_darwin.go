//go:build darwin

package agent

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID 取 Unix socket 连接对端的进程号（LOCAL_PEERPID）。
func peerPID(conn net.Conn) (int, bool) {
	return sockoptPID(conn, func(fd int) (int, error) { return unix.GetsockoptInt(fd, unix.SOL_LOCAL, unix.LOCAL_PEERPID) })
}
