package main

import (
	"os"

	"github.com/xufei547/railse-opc/internal/agent"
	"github.com/xufei547/railse-opc/internal/proto"
)

// readAncestors 取钩子进程的祖先链（见 agent.ReadAncestorsFrom）。守护进程能取到连接对端的进程凭据时会自己重建、不用它；
// 取不到时（平台不支持）才退回用它。是变量只为测试能换。
var readAncestors = func() []proto.Proc { return agent.ReadAncestorsFrom(os.Getpid()) }
