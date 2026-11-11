//go:build !darwin && !linux

package agent

import "net"

// peerPID 在其他平台上取不到。
func peerPID(conn net.Conn) (int, bool) { return 0, false }
