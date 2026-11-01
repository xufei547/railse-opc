package policy

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func TestTriggerCorpus(t *testing.T) {
	f, err := os.Open("testdata/trigger_corpus.tsv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	line, n := 0, 0
	for sc.Scan() {
		line++
		text := sc.Text()
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		cols := strings.Split(text, "\t")
		if len(cols) != 4 {
			t.Fatalf("第 %d 行要 4 列，得到 %d: %q", line, len(cols), text)
		}
		want, err := ParseRisk(strings.TrimSpace(cols[1]))
		if err != nil {
			t.Fatalf("第 %d 行: %v", line, err)
		}
		risk, tr := ClassifyTrace(Payload{Type: PayloadShell, Command: cols[0], Cwd: "/home/u/proj"}, shellCtx)
		if risk != want {
			t.Errorf("第 %d 行 %q 风险 = %s，想要 %s", line, cols[0], risk, want)
		}
		if cols[2] == "" {
			if tr != nil {
				t.Errorf("第 %d 行 %q 不该有触发点，得到 %+v", line, cols[0], *tr)
			}
			continue
		}
		if tr == nil {
			t.Errorf("第 %d 行 %q 没有触发点，想要 %q / %q", line, cols[0], cols[2], cols[3])
			continue
		}
		if tr.Why != cols[2] || tr.Segment != cols[3] {
			t.Errorf("第 %d 行 %q 触发点 = %q / %q，想要 %q / %q", line, cols[0], tr.Why, tr.Segment, cols[2], cols[3])
		}
		n++
	}
	if n < 15 {
		t.Fatalf("语料只有 %d 条，至少要 15 条", n)
	}
}

func TestClassifyTraceFileWriteAndNetwork(t *testing.T) {
	cases := []struct {
		p    Payload
		risk Risk
		why  string
		seg  string
	}{
		{Payload{Type: PayloadFileWrite, Path: "/etc/hosts", Cwd: "/home/u/proj"}, RiskWriteOutside, WhyWriteOutside, "/etc/hosts"},
		{Payload{Type: PayloadFileWrite, Path: "/home/u/proj/.env", Cwd: "/home/u/proj"}, RiskDestructive, WhySensitive, "/home/u/proj/.env"},
		{Payload{Type: PayloadFileWrite, Path: "src/a.go", Cwd: "/home/u/proj"}, RiskWriteInProject, "", ""},
		{Payload{Type: PayloadNetwork, URL: "https://example.com/x"}, RiskNetwork, WhyNetwork, "https://example.com/x"},
		{Payload{Type: PayloadMCP, Server: "fs", Tool: "read"}, RiskNetwork, WhyNetwork, "fs/read"},
	}
	for _, c := range cases {
		risk, tr := ClassifyTrace(c.p, shellCtx)
		if risk != c.risk {
			t.Errorf("%+v 风险 = %s，想要 %s", c.p, risk, c.risk)
		}
		if c.why == "" {
			if tr != nil {
				t.Errorf("%+v 不该有触发点：%+v", c.p, *tr)
			}
			continue
		}
		if tr == nil || tr.Why != c.why || tr.Segment != c.seg {
			t.Errorf("%+v 触发点 = %+v，想要 %q / %q", c.p, tr, c.why, c.seg)
		}
	}
}

func TestCompactSegment(t *testing.T) {
	if got := compactSegment("python - <<'EOF'\nimport os\nEOF"); got != "python - <<'EOF'" {
		t.Fatalf("只取第一行：%q", got)
	}
	if got := compactSegment("  a   b\tc "); got != "a b c" {
		t.Fatalf("压空白：%q", got)
	}
	long := strings.Repeat("x", 100)
	if got := compactSegment(long); len([]rune(got)) != 80 || !strings.HasSuffix(got, "…") {
		t.Fatalf("截断：%q", got)
	}
}

func TestEvaluateCarriesTrigger(t *testing.T) {
	e, err := NewEngine(RuleSet{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	d := e.Evaluate(Payload{Type: PayloadShell, Command: "rm -rf after", Cwd: "/home/u/proj"}, shellCtx)
	if d.Action != ActionAsk || d.Trigger == nil || d.Trigger.Why != WhyDelete || d.Trigger.Segment != "rm -rf after" {
		t.Fatalf("Decision 应带触发点：%+v", d)
	}
	if d := e.Evaluate(Payload{Type: PayloadShell, Command: "git status", Cwd: "/home/u/proj"}, shellCtx); d.Trigger != nil {
		t.Fatalf("只读命令不该有触发点：%+v", d.Trigger)
	}
}
