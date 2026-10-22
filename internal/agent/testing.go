package agent

// NewPendingForTest 仅供其他包的测试用，不要在生产代码调用。
func NewPendingForTest() *Pending { return newPending() }
