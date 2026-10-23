package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestPushSubscriptionLifecycle(t *testing.T) {
	s := openTemp(t)
	sub, err := s.UpsertPushSubscription("https://web.push.apple.com/abc", "p1", "a1", "Mozilla/5.0 iPhone")
	if err != nil || sub.ID == "" || sub.Endpoint != "https://web.push.apple.com/abc" || sub.Failures != 0 {
		t.Fatalf("upsert: %+v %v", sub, err)
	}
	if n, removed, _ := s.MarkPushFailure(sub.Endpoint, 5); n != 1 || removed {
		t.Fatalf("failures = %d removed=%v", n, removed)
	}
	if n, removed, _ := s.MarkPushFailure(sub.Endpoint, 5); n != 2 || removed {
		t.Fatalf("failures = %d removed=%v", n, removed)
	}
	// 同一端点重新订阅：换 keys、failures 归零、id 不变
	again, err := s.UpsertPushSubscription(sub.Endpoint, "p2", "a2", "ua2")
	if err != nil || again.ID != sub.ID || again.P256dh != "p2" || again.Auth != "a2" || again.Failures != 0 || again.UserAgent != "ua2" {
		t.Fatalf("重新订阅: %+v %v", again, err)
	}
	s.MarkPushFailure(sub.Endpoint, 5)
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
	if n, removed, err := s.MarkPushFailure(sub.Endpoint, 5); err != nil || n != 0 || removed {
		t.Fatalf("不存在的端点 MarkPushFailure = %d, %v, %v", n, removed, err)
	}
	if _, err := s.UpsertPushSubscription("", "p", "a", ""); err == nil {
		t.Fatal("空端点应报错")
	}
	if _, err := s.UpsertPushSubscription("https://x", "", "a", ""); err == nil {
		t.Fatal("空 keys 应报错")
	}
}

// Upsert 是一条语句：返回的就是写进去的那一行；重新订阅保留 id、created_at 与 last_ok_at，换 keys、UA，failures 归零。
func TestUpsertPushSubscriptionReturnsRow(t *testing.T) {
	s := openTemp(t)
	first, err := s.UpsertPushSubscription("https://push.example/a", "p1", "a1", "ua1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPushOK(first.Endpoint); err != nil {
		t.Fatal(err)
	}
	s.MarkPushFailure(first.Endpoint, 5)
	again, err := s.UpsertPushSubscription(first.Endpoint, "p2", "a2", "ua2")
	if err != nil || again.ID != first.ID || again.CreatedAt != first.CreatedAt || again.LastOkAt == nil ||
		again.P256dh != "p2" || again.Auth != "a2" || again.UserAgent != "ua2" || again.Failures != 0 || again.Broken {
		t.Fatalf("重新订阅: %+v %v", again, err)
	}
	got, err := s.GetPushSubscription(first.Endpoint)
	if err != nil || got.LastOkAt == nil || *got.LastOkAt != *again.LastOkAt {
		t.Fatalf("读回: %+v %v", got, err)
	}
	got.LastOkAt, again.LastOkAt = nil, nil
	if got != again {
		t.Fatalf("Upsert 返回的应与库里一致: %+v / %+v", again, got)
	}
}

// 连续失败达到上限时 MarkPushFailure 在同一事务里删掉订阅；没达到不删。用户在两次失败之间重新订阅（failures 归零），
// 之后的失败从 1 重新数，不会删掉刚登记的订阅。
func TestMarkPushFailureRemovesAtLimit(t *testing.T) {
	s := openTemp(t)
	sub, _ := s.UpsertPushSubscription("https://push.example/a", "p", "a", "")
	for i := 1; i <= 4; i++ {
		if n, removed, err := s.MarkPushFailure(sub.Endpoint, 5); err != nil || n != i || removed {
			t.Fatalf("第 %d 次: %d %v %v", i, n, removed, err)
		}
	}
	// 用户此刻重新订阅：failures 归零
	if _, err := s.UpsertPushSubscription(sub.Endpoint, "p2", "a2", ""); err != nil {
		t.Fatal(err)
	}
	if n, removed, _ := s.MarkPushFailure(sub.Endpoint, 5); n != 1 || removed {
		t.Fatalf("重新订阅后应从 1 数起: %d %v", n, removed)
	}
	for i := 2; i <= 4; i++ {
		s.MarkPushFailure(sub.Endpoint, 5)
	}
	n, removed, err := s.MarkPushFailure(sub.Endpoint, 5)
	if err != nil || n != 5 || !removed {
		t.Fatalf("第 5 次应删: %d %v %v", n, removed, err)
	}
	if _, err := s.GetPushSubscription(sub.Endpoint); !errors.Is(err, ErrNotFound) {
		t.Fatalf("达到上限后应已删除: %v", err)
	}
	// limit ≤ 0 时只计数不删
	s.UpsertPushSubscription(sub.Endpoint, "p", "a", "")
	for i := 0; i < 7; i++ {
		if _, removed, _ := s.MarkPushFailure(sub.Endpoint, 0); removed {
			t.Fatal("limit 0 不应删")
		}
	}
}

// keys 列坏掉的一行不让列表整体失败：照样列出，Broken 为 true、密钥为空，其他行正常；Get 同样不报错。
func TestListPushSubscriptionsToleratesBrokenKeys(t *testing.T) {
	s := openTemp(t)
	s.UpsertPushSubscription("https://push.example/good1", "p1", "a1", "")
	if _, err := s.db.Exec(`INSERT INTO push_subscriptions(id, endpoint, keys, created_at) VALUES('bad', 'https://push.example/bad', 'not json', ?)`, Now()); err != nil {
		t.Fatal(err)
	}
	s.UpsertPushSubscription("https://push.example/good2", "p2", "a2", "")
	list, err := s.ListPushSubscriptions()
	if err != nil || len(list) != 3 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	for _, p := range list {
		if bad := p.Endpoint == "https://push.example/bad"; p.Broken != bad || (bad && (p.P256dh != "" || p.Auth != "")) || (!bad && p.P256dh == "") {
			t.Fatalf("行 %+v", p)
		}
	}
	got, err := s.GetPushSubscription("https://push.example/bad")
	if err != nil || !got.Broken || got.ID != "bad" {
		t.Fatalf("Get 坏行: %+v %v", got, err)
	}
	if ok, err := s.DeletePushSubscription("https://push.example/bad"); err != nil || !ok {
		t.Fatalf("坏行应能删掉: %v %v", ok, err)
	}
}

// ReplacePushKey 在一个事务里写设置并删掉全部订阅，返回删了几条。
func TestReplacePushKey(t *testing.T) {
	s := openTemp(t)
	s.SetSetting("vapid_private", "old")
	s.UpsertPushSubscription("https://push.example/a", "p", "a", "")
	s.UpsertPushSubscription("https://push.example/b", "p", "a", "")
	n, err := s.ReplacePushKey("vapid_private", "new")
	if err != nil || n != 2 {
		t.Fatalf("ReplacePushKey = %d, %v", n, err)
	}
	if c, _ := s.CountPushSubscriptions(); c != 0 {
		t.Fatalf("订阅应全删: %d", c)
	}
	if v, ok, _ := s.GetSetting("vapid_private"); !ok || v != "new" {
		t.Fatalf("设置 = %q %v", v, ok)
	}
	if n, err := s.ReplacePushKey("vapid_private", "newer"); err != nil || n != 0 {
		t.Fatalf("没有订阅时 = %d, %v", n, err)
	}
}

// SetSettingIfAbsent 只在没有时写；两个进程同时首次写同一个键，只有一个写成，另一个读回的就是它。
func TestSetSettingIfAbsent(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	var wg sync.WaitGroup
	wrote := make([]bool, 8)
	for i := range wrote {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st := a
			if i%2 == 1 {
				st = b
			}
			ok, err := st.SetSettingIfAbsent("k", string(rune('a'+i)))
			if err != nil {
				t.Error(err)
			}
			wrote[i] = ok
		}()
	}
	wg.Wait()
	n := 0
	var winner string
	for i, ok := range wrote {
		if ok {
			n++
			winner = string(rune('a' + i))
		}
	}
	if n != 1 {
		t.Fatalf("应只有一个写成: %v", wrote)
	}
	for _, st := range []*Store{a, b} {
		if v, ok, _ := st.GetSetting("k"); !ok || v != winner {
			t.Fatalf("读回 %q，应为 %q", v, winner)
		}
	}
}
