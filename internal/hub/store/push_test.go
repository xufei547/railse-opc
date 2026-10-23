package store

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestPushSubscriptionLifecycle(t *testing.T) {
	s := openTemp(t)
	sub, err := s.UpsertPushSubscription("https://web.push.apple.com/abc", "p1", "a1", "Mozilla/5.0 iPhone")
	if err != nil || sub.ID == "" || sub.Endpoint != "https://web.push.apple.com/abc" || sub.Failures != 0 {
		t.Fatalf("upsert: %+v %v", sub, err)
	}
	if n, removed, _ := s.MarkPushFailure(sub.Endpoint, time.Now(), testRule); n != 1 || removed {
		t.Fatalf("failures = %d removed=%v", n, removed)
	}
	if n, removed, _ := s.MarkPushFailure(sub.Endpoint, time.Now(), testRule); n != 2 || removed {
		t.Fatalf("failures = %d removed=%v", n, removed)
	}
	// 同一端点重新订阅：换 keys、failures 归零、id 不变
	again, err := s.UpsertPushSubscription(sub.Endpoint, "p2", "a2", "ua2")
	if err != nil || again.ID != sub.ID || again.P256dh != "p2" || again.Auth != "a2" || again.Failures != 0 || again.UserAgent != "ua2" {
		t.Fatalf("重新订阅: %+v %v", again, err)
	}
	s.MarkPushFailure(sub.Endpoint, time.Now(), testRule)
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
	if n, removed, err := s.MarkPushFailure(sub.Endpoint, time.Now(), testRule); err != nil || n != 0 || removed {
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
	s.MarkPushFailure(first.Endpoint, time.Now(), testRule)
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

// testRule 是 hub 用的删订阅门槛：连续 5 次，且这一轮第一次失败已过 24 小时。
var testRule = PushFailureRule{Count: 5, Span: 24 * time.Hour}

// 删订阅要同时满足：连续失败至少 5 次，且这一轮第一次失败（first_failed_at）距这次失败至少 24 小时。
// 几秒内连着 5 次失败（推送服务或 hub 短暂故障）不删；计数与删除在同一事务里，删除带条件。
func TestMarkPushFailureNeedsCountAndSpan(t *testing.T) {
	s := openTemp(t)
	t0 := time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)
	sub, _ := s.UpsertPushSubscription("https://push.example/a", "p", "a", "")
	first := func() string {
		t.Helper()
		got, err := s.GetPushSubscription(sub.Endpoint)
		if err != nil {
			t.Fatalf("订阅不应被删: %v", err)
		}
		if got.FirstFailedAt == nil {
			return ""
		}
		return *got.FirstFailedAt
	}
	// 几秒内 5 次：计到 5，不删；first_failed_at 是第一次失败的时刻
	for i := 1; i <= 5; i++ {
		n, removed, err := s.MarkPushFailure(sub.Endpoint, t0.Add(time.Duration(i-1)*time.Second), testRule)
		if err != nil || n != i || removed {
			t.Fatalf("第 %d 次: %d %v %v", i, n, removed, err)
		}
	}
	if first() != FormatTime(t0) {
		t.Fatalf("first_failed_at = %q", first())
	}
	// 跨度差 1 秒不到 24 小时：仍不删
	if n, removed, _ := s.MarkPushFailure(sub.Endpoint, t0.Add(24*time.Hour-time.Second), testRule); n != 6 || removed {
		t.Fatalf("跨度不足 24 小时不应删: %d %v", n, removed)
	}
	// 跨度满 24 小时：删
	if n, removed, err := s.MarkPushFailure(sub.Endpoint, t0.Add(24*time.Hour), testRule); err != nil || n != 7 || !removed {
		t.Fatalf("满 5 次且跨度满 24 小时应删: %d %v %v", n, removed, err)
	}
	if _, err := s.GetPushSubscription(sub.Endpoint); !errors.Is(err, ErrNotFound) {
		t.Fatalf("应已删除: %v", err)
	}

	// 正好第 5 次时跨度满 24 小时：删
	sub, _ = s.UpsertPushSubscription("https://push.example/b", "p", "a", "")
	for i, at := range []time.Duration{0, time.Hour, 2 * time.Hour, 3 * time.Hour} {
		if _, removed, _ := s.MarkPushFailure(sub.Endpoint, t0.Add(at), testRule); removed {
			t.Fatalf("第 %d 次不应删", i+1)
		}
	}
	if n, removed, _ := s.MarkPushFailure(sub.Endpoint, t0.Add(24*time.Hour), testRule); n != 5 || !removed {
		t.Fatalf("第 5 次、跨度 24 小时应删: %d %v", n, removed)
	}

	// 跨度够但次数不足 5：不删
	sub, _ = s.UpsertPushSubscription("https://push.example/c", "p", "a", "")
	for i := 0; i < 4; i++ {
		if _, removed, _ := s.MarkPushFailure(sub.Endpoint, t0.Add(time.Duration(i)*16*time.Hour), testRule); removed {
			t.Fatal("不足 5 次不应删")
		}
	}

	// 中间成功一次：次数与 first_failed_at 都归零，之后重新起算
	if err := s.MarkPushOK(sub.Endpoint); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetPushSubscription(sub.Endpoint); got.Failures != 0 || got.FirstFailedAt != nil {
		t.Fatalf("成功后应归零: %+v", got)
	}
	t1 := t0.Add(100 * time.Hour)
	for i := 1; i <= 5; i++ {
		if n, removed, _ := s.MarkPushFailure(sub.Endpoint, t1.Add(time.Duration(i)*time.Second), testRule); n != i || removed {
			t.Fatalf("成功之后第 %d 次: %d %v", i, n, removed)
		}
	}
	if first() != FormatTime(t1.Add(time.Second)) {
		t.Fatalf("成功之后 first_failed_at 应重新起算: %q", first())
	}

	// 重新订阅同样归零：之前数到的次数与时刻不会把刚登记的订阅删掉
	if _, err := s.UpsertPushSubscription(sub.Endpoint, "p2", "a2", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetPushSubscription(sub.Endpoint); got.Failures != 0 || got.FirstFailedAt != nil {
		t.Fatalf("重新订阅后应归零: %+v", got)
	}
	if n, removed, _ := s.MarkPushFailure(sub.Endpoint, t1.Add(48*time.Hour), testRule); n != 1 || removed {
		t.Fatalf("重新订阅后从 1 数起: %d %v", n, removed)
	}

	// Count ≤ 0：只计数不删
	sub, _ = s.UpsertPushSubscription("https://push.example/d", "p", "a", "")
	for i := 0; i < 7; i++ {
		if _, removed, _ := s.MarkPushFailure(sub.Endpoint, t0.Add(time.Duration(i)*24*time.Hour), PushFailureRule{}); removed {
			t.Fatal("Count 0 不应删")
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
