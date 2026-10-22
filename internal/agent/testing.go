package agent

// NewPendingForTest 仅供其他包的测试用，不要在生产代码调用。
func NewPendingForTest() *Pending { return newPending() }

// ErrHookGoneForTest 仅供其他包的测试用（模拟钩子消失时取消请求 ctx 的原因），不要在生产代码调用。
func ErrHookGoneForTest() error { return errHookGone }
