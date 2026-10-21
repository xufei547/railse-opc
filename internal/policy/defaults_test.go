package policy

import "testing"

func TestDefaultRuleSet(t *testing.T) {
	rs := DefaultRuleSet()
	if rs.Version != RuleSetVersion || len(rs.Rules) != 2 {
		t.Fatalf("内置包 version=%d rules=%d", rs.Version, len(rs.Rules))
	}
	for _, r := range rs.Rules {
		if r.Source != SourceBuiltin {
			t.Errorf("规则 %s 来源应是 builtin", r.ID)
		}
	}
	e, err := NewEngine(rs)
	if err != nil {
		t.Fatal(err)
	}
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u", MachineID: "m1"}
	cases := []struct {
		p      Payload
		action Action
	}{
		{Payload{Type: PayloadShell, Command: "ls", Cwd: "/home/u/proj"}, ActionAllow},
		{Payload{Type: PayloadShell, Command: "mkdir x", Cwd: "/home/u/proj"}, ActionAllow},
		{Payload{Type: PayloadFileWrite, Path: "src/a.go", Cwd: "/home/u/proj"}, ActionAllow},
		{Payload{Type: PayloadShell, Command: "curl https://x.y", Cwd: "/home/u/proj"}, ActionAsk},
		{Payload{Type: PayloadShell, Command: "rm -rf x", Cwd: "/home/u/proj"}, ActionAsk},
		{Payload{Type: PayloadShell, Command: "foobar", Cwd: "/home/u/proj"}, ActionAsk},
		{Payload{Type: PayloadFileWrite, Path: ".env", Cwd: "/home/u/proj"}, ActionAsk},
		{Payload{Type: PayloadMCP, Server: "s", Tool: "t"}, ActionAsk},
	}
	for _, c := range cases {
		if d := e.Evaluate(c.p, ctx); d.Action != c.action {
			t.Errorf("%+v: 得到 %s (%s), 想要 %s", c.p, d.Action, d.Reason, c.action)
		}
	}
}
