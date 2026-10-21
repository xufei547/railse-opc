package policy

import (
	"encoding/json"
	"testing"
)

func TestRiskOrdering(t *testing.T) {
	order := []Risk{RiskRead, RiskWriteInProject, RiskUnknown, RiskWriteOutside, RiskNetwork, RiskOutward, RiskDestructive}
	for i := 1; i < len(order); i++ {
		if !(order[i-1] < order[i]) {
			t.Fatalf("%s 应低于 %s", order[i-1], order[i])
		}
	}
}

func TestRiskStringAndParse(t *testing.T) {
	cases := map[Risk]string{
		RiskRead: "read", RiskWriteInProject: "write_in_project", RiskUnknown: "unknown",
		RiskWriteOutside: "write_outside", RiskNetwork: "network", RiskOutward: "outward", RiskDestructive: "destructive",
	}
	for r, name := range cases {
		if r.String() != name {
			t.Errorf("%d.String() = %q, 想要 %q", int(r), r.String(), name)
		}
		got, err := ParseRisk(name)
		if err != nil || got != r {
			t.Errorf("ParseRisk(%q) = %v, %v; 想要 %v", name, got, err, r)
		}
	}
	if _, err := ParseRisk("bogus"); err == nil {
		t.Error("ParseRisk(bogus) 应报错")
	}
}

func TestRiskTextRoundTrip(t *testing.T) {
	type wrap struct {
		R Risk `json:"r"`
	}
	data, err := json.Marshal(wrap{RiskOutward})
	if err != nil || string(data) != `{"r":"outward"}` {
		t.Fatalf("Marshal = %s, %v", data, err)
	}
	var w wrap
	if err := json.Unmarshal([]byte(`{"r":"destructive"}`), &w); err != nil || w.R != RiskDestructive {
		t.Fatalf("Unmarshal = %v, %v", w.R, err)
	}
}

// TestRiskNeverZeroByAccident Risk 的零值是 RiskRead：空载荷、未知类型与缺字段的载荷必须明确定级为 unknown，
// 不能因为没赋值而落成 read；引擎给出的 Decision.Risk 就是同一请求的 Classify 结果。
func TestRiskNeverZeroByAccident(t *testing.T) {
	if Risk(0) != RiskRead {
		t.Fatalf("零值应是 RiskRead, 得到 %s", Risk(0))
	}
	ctx := Context{ProjectRoot: "/home/u/proj", HomeDir: "/home/u"}
	for _, p := range []Payload{
		{},
		{Type: "bogus"},
		{Type: PayloadShell},
		{Type: PayloadShell, Command: "   "},
		{Type: PayloadShell, Command: ";"},
		{Type: PayloadShell, Command: "# 只有注释"},
		{Type: PayloadFileWrite},
	} {
		for _, c := range []Context{ctx, {}} {
			if got := Classify(p, c); got != RiskUnknown {
				t.Errorf("Classify(%+v, %+v) = %s, 想要 unknown", p, c, got)
			}
		}
	}

	e, err := NewEngine(DefaultRuleSet())
	if err != nil {
		t.Fatal(err)
	}
	reqs := []Payload{
		{},
		{Type: "bogus"},
		{Type: PayloadShell, Command: "git status", Cwd: "/home/u/proj"},
		{Type: PayloadShell, Command: "touch a", Cwd: "/home/u/proj"},
		{Type: PayloadShell, Command: "git push origin main"},
		{Type: PayloadShell, Command: "rm -rf ~"},
		{Type: PayloadFileWrite, Path: "/etc/hosts"},
		{Type: PayloadMCP, Server: "s", Tool: "t"},
		{Type: PayloadNetwork, URL: "https://example.com"},
	}
	for _, paused := range []bool{false, true} {
		e.SetPaused(paused)
		for _, p := range reqs {
			for _, c := range []Context{ctx, {}} {
				if d, want := e.Evaluate(p, c), Classify(p, c); d.Risk != want {
					t.Errorf("paused=%v Evaluate(%+v).Risk = %s, Classify = %s", paused, p, d.Risk, want)
				}
			}
		}
	}
}

func TestMaxRisk(t *testing.T) {
	if got := maxRisk(RiskRead, RiskNetwork); got != RiskNetwork {
		t.Fatalf("maxRisk = %s", got)
	}
	if got := maxRisk(RiskDestructive, RiskRead); got != RiskDestructive {
		t.Fatalf("maxRisk = %s", got)
	}
}

func TestPayloadJSONRoundTrip(t *testing.T) {
	in := Payload{Type: PayloadShell, Command: "git status", Cwd: "/p"}
	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out Payload
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Type != PayloadShell || out.Command != "git status" || out.Cwd != "/p" {
		t.Fatalf("round trip = %+v", out)
	}
	if string(data) != `{"type":"shell","command":"git status","cwd":"/p"}` {
		t.Fatalf("空字段应省略, 得到 %s", data)
	}
}
