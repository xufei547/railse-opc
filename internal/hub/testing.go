package hub

// 以下三个方法仅供其他包的测试用，不要在生产代码调用。

// PushRulesForTest 把当前规则快照推给全部在线守护进程。
func (s *Server) PushRulesForTest() { s.pushRules() }

// DisconnectForTest 断开某台机器的连接，模拟网络中断。
func (s *Server) DisconnectForTest(id string) { s.agents.disconnect(id) }

// SetPausedForTest 设置总开关并下发。
func (s *Server) SetPausedForTest(p bool) error { return s.setPaused(p) }
