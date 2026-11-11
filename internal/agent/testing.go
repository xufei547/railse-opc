package agent

import (
	"net"

	"github.com/xufei547/railse-opc/internal/proto"
)

// NewPendingForTest 仅供其他包的测试用，不要在生产代码调用。
func NewPendingForTest() *Pending { return newPending() }

// ErrHookGoneForTest 仅供其他包的测试用（模拟钩子消失时取消请求 ctx 的原因），不要在生产代码调用。
func ErrHookGoneForTest() error { return errHookGone }

// SetPeerAncestryForTest 仅供测试用：fn 为 nil 时假装取不到连接对端的进程凭据（于是用请求里自报的祖先链），测试结束时还原。
func SetPeerAncestryForTest(t interface{ Cleanup(func()) }, fn func(net.Conn) ([]proto.Proc, bool)) {
	old := peerAncestry
	if fn == nil {
		fn = func(net.Conn) ([]proto.Proc, bool) { return nil, false }
	}
	peerAncestry = fn
	t.Cleanup(func() { peerAncestry = old })
}
