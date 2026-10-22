package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAuditInsertAndList(t *testing.T) {
	s := openTemp(t)
	for _, a := range []string{"approve", "deny", "rules_import"} {
		if err := s.InsertAudit(AuditEvent{MachineID: "m1", Actor: "user", RequestID: "r1", Action: a, Detail: `{"x":1}`}); err != nil {
			t.Fatal(err)
		}
	}
	s.InsertAudit(AuditEvent{Actor: "system", Action: "other"})
	all, err := s.ListAudit(10, "")
	if err != nil || len(all) != 4 || all[0].Action != "other" || all[0].TS == "" {
		t.Fatalf("ListAudit = %+v, %v", all, err)
	}
	byReq, _ := s.ListAudit(10, "r1")
	if len(byReq) != 3 {
		t.Fatalf("按请求过滤 = %d", len(byReq))
	}
	two, _ := s.ListAudit(2, "")
	if len(two) != 2 {
		t.Fatalf("limit = %d", len(two))
	}
}

// TS 给了就必须是定宽 UTC，原样入库；为空时用 Now()，同样定宽。
func TestInsertAuditTime(t *testing.T) {
	s := openTemp(t)
	ts := time.Date(2026, 10, 22, 8, 0, 0, 120000000, time.UTC)
	for _, v := range []string{ts.Format(time.RFC3339Nano), ts.In(time.FixedZone("CST", 8*3600)).Format(timeLayout), "yesterday"} {
		if err := s.InsertAudit(AuditEvent{TS: v, Actor: "agent", Action: "x"}); err == nil {
			t.Errorf("TS %q 应被拒绝", v)
		}
	}
	if err := s.InsertAudit(AuditEvent{TS: FormatTime(ts), Actor: "agent", Action: "given"}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertAudit(AuditEvent{Actor: "system", Action: "now"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListAudit(10, "")
	if err != nil || len(all) != 2 {
		t.Fatalf("被拒绝的不应入库: %+v, %v", all, err)
	}
	if all[1].TS != FormatTime(ts) {
		t.Fatalf("给定的 TS 应原样入库: %q", all[1].TS)
	}
	if err := checkTime("ts", all[0].TS); err != nil {
		t.Fatalf("缺省 TS 应是定宽 UTC: %v", err)
	}
}

// limit ≤ 0 按 defaultAuditLimit 处理，不会变成不限。
func TestListAuditDefaultLimit(t *testing.T) {
	s := openTemp(t)
	for i := 0; i < defaultAuditLimit+1; i++ {
		if err := s.InsertAudit(AuditEvent{Actor: "system", Action: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{0, -1} {
		got, err := s.ListAudit(limit, "")
		if err != nil || len(got) != defaultAuditLimit {
			t.Fatalf("limit %d: %d 条, %v", limit, len(got), err)
		}
	}
}

// AuditEvent 直接作为 API 输出，字段名是 snake_case。
func TestAuditEventJSON(t *testing.T) {
	b, err := json.Marshal(AuditEvent{ID: 7, TS: "t", MachineID: "m", Actor: "a", RequestID: "r", Action: "x", Detail: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":7,"ts":"t","machine_id":"m","actor":"a","request_id":"r","action":"x","detail":"{}"}`
	if got := string(b); got != want {
		t.Fatalf("JSON = %s", got)
	}
}
