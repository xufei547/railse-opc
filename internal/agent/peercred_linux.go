//go:build linux

package agent

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerPID 取 Unix socket 连接对端的进程号（SO_PEERCRED）。
func peerPID(conn net.Conn) (int, bool) {
	return sockoptPID(conn, func(fd int) (int, error) {
		cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			return 0, err
		}
		return int(cred.Pid), nil
	})
}
