package store

import (
	"errors"
	"testing"
)

func TestPushSubscriptionLifecycle(t *testing.T) {
	s := openTemp(t)
	sub, err := s.UpsertPushSubscription("https://web.push.apple.com/abc", "p1", "a1", "Mozilla/5.0 iPhone")
	if err != nil || sub.ID == "" || sub.Endpoint != "https://web.push.apple.com/abc" || sub.Failures != 0 {
		t.Fatalf("upsert: %+v %v", sub, err)
	}
	if n, _ := s.MarkPushFailure(sub.Endpoint); n != 1 {
		t.Fatalf("failures = %d", n)
	}
	if n, _ := s.MarkPushFailure(sub.Endpoint); n != 2 {
		t.Fatalf("failures = %d", n)
	}
	// 同一端点重新订阅：换 keys、failures 归零、id 不变
	again, err := s.UpsertPushSubscription(sub.Endpoint, "p2", "a2", "ua2")
	if err != nil || again.ID != sub.ID || again.P256dh != "p2" || again.Auth != "a2" || again.Failures != 0 || again.UserAgent != "ua2" {
		t.Fatalf("重新订阅: %+v %v", again, err)
	}
	s.MarkPushFailure(sub.Endpoint)
	if err := s.MarkPushOK(sub.Endpoint); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetPushSubscription(sub.Endpoint)
	if err != nil || got.Failures != 0 || got.LastOkAt == nil {
		t.Fatalf("MarkPushOK 后: %+v %v", got, err)
	}
	s.UpsertPushSubscription("https://fcm.example/2", "p", "a", "")
	if n, _ := s.CountPushSubscriptions(); n != 2 {
		t.Fatalf("count = %d", n)
	}
	list, _ := s.ListPushSubscriptions()
	if len(list) != 2 || list[0].Endpoint != sub.Endpoint {
		t.Fatalf("list: %+v", list)
	}
	if ok, err := s.DeletePushSubscription(sub.Endpoint); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, _ := s.DeletePushSubscription(sub.Endpoint); ok {
		t.Fatal("再删应返回 false")
	}
	if _, err := s.GetPushSubscription(sub.Endpoint); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删后应 ErrNotFound: %v", err)
	}
	if n, err := s.MarkPushFailure(sub.Endpoint); err != nil || n != 0 {
		t.Fatalf("不存在的端点 MarkPushFailure = %d, %v", n, err)
	}
	if _, err := s.UpsertPushSubscription("", "p", "a", ""); err == nil {
		t.Fatal("空端点应报错")
	}
	if _, err := s.UpsertPushSubscription("https://x", "", "a", ""); err == nil {
		t.Fatal("空 keys 应报错")
	}
}
