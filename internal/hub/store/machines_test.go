package store

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEnrollTokenLifecycle(t *testing.T) {
	s := openTemp(t)
	tok, err := s.CreateEnrollToken(10 * time.Minute)
	if err != nil || len(tok) < 40 {
		t.Fatalf("CreateEnrollToken = %q, %v", tok, err)
	}
	if err := s.ConsumeEnrollToken(tok); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeEnrollToken(tok); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("用过的接入码应失效: %v", err)
	}
	if err := s.ConsumeEnrollToken("nope"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("不存在的接入码: %v", err)
	}
	expired, err := s.CreateEnrollToken(-time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeEnrollToken(expired); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("过期的接入码: %v", err)
	}
}

// 并发消费同一接入码只能成功一次；两个 Store 开同一个库文件，模拟两个连接抢同一行。
func TestConsumeEnrollTokenConcurrentOnce(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	stores := make([]*Store, 2)
	for i := range stores {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		stores[i] = s
	}
	tok, err := stores[0].CreateEnrollToken(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const n = 16
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = stores[i%len(stores)].ConsumeEnrollToken(tok)
		}()
	}
	close(start)
	wg.Wait()
	ok := 0
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrInvalidToken):
			t.Fatalf("第 %d 次消费: 意外错误 %v", i, err)
		}
	}
	if ok != 1 {
		t.Fatalf("并发消费成功 %d 次，应恰好 1 次", ok)
	}
}

// 库里只存凭据的哈希，不存原文。
func TestCredentialsStoredAsHashOnly(t *testing.T) {
	s := openTemp(t)
	enroll, err := s.CreateEnrollToken(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	secret := RandomToken()
	if _, err := s.CreateMachine("m", secret, "/h"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.CreateClientSession(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ query, raw string }{
		{"SELECT token_hash FROM enroll_tokens", enroll},
		{"SELECT secret_hash FROM machines", secret},
		{"SELECT token_hash FROM client_sessions", sess},
	}
	for _, c := range cases {
		var got string
		if err := s.db.QueryRow(c.query).Scan(&got); err != nil {
			t.Fatalf("%s: %v", c.query, err)
		}
		if got != HashToken(c.raw) {
			t.Fatalf("%s = %q，应为原文的 SHA-256 %q", c.query, got, HashToken(c.raw))
		}
	}
}

func TestMachineAuthAndRevoke(t *testing.T) {
	s := openTemp(t)
	secret := RandomToken()
	m, err := s.CreateMachine("mac-studio", secret, "/Users/u")
	if err != nil || m.ID == "" || m.Name != "mac-studio" {
		t.Fatalf("CreateMachine = %+v, %v", m, err)
	}
	got, err := s.AuthMachine(secret)
	if err != nil || got.ID != m.ID {
		t.Fatalf("AuthMachine = %+v, %v", got, err)
	}
	if _, err := s.AuthMachine("wrong"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("错密钥应 ErrNotFound: %v", err)
	}
	if err := s.TouchMachine(m.ID, "0.1.0", "/Users/u"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListMachines()
	if len(list) != 1 || list[0].LastSeen == nil || list[0].AgentVersion != "0.1.0" {
		t.Fatalf("ListMachines = %+v", list)
	}
	if err := s.RevokeMachine(m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthMachine(secret); !errors.Is(err, ErrNotFound) {
		t.Fatalf("吊销后应拒绝: %v", err)
	}
	if gm, _ := s.GetMachine(m.ID); !gm.Revoked {
		t.Fatal("GetMachine 应显示已吊销")
	}
}

func TestClientSessions(t *testing.T) {
	s := openTemp(t)
	tok, err := s.CreateClientSession(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ValidateClientSession(tok); err != nil || !ok {
		t.Fatalf("新会话应有效: %v, %v", ok, err)
	}
	if ok, err := s.ValidateClientSession("nope"); err != nil || ok {
		t.Fatalf("不存在的会话应无效: %v, %v", ok, err)
	}
	old, err := s.CreateClientSession(-time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ValidateClientSession(old); err != nil || ok {
		t.Fatalf("过期会话应无效: %v, %v", ok, err)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM client_sessions WHERE token_hash = ?", HashToken(old)).Scan(&n); err != nil || n != 0 {
		t.Fatalf("过期会话行应已清理: %d, %v", n, err)
	}
	if err := s.DeleteClientSession(tok); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ValidateClientSession(tok); err != nil || ok {
		t.Fatalf("删除后应无效: %v, %v", ok, err)
	}
}

// 清理过期行失败（这里用触发器让 DELETE 报错）不影响校验：有效的仍有效，过期的仍无效。
func TestValidateClientSessionIgnoresCleanupFailure(t *testing.T) {
	s := openTemp(t)
	good, err := s.CreateClientSession(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.CreateClientSession(-time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER no_delete BEFORE DELETE ON client_sessions BEGIN SELECT RAISE(ABORT, 'no delete'); END"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ValidateClientSession(old); err != nil || ok {
		t.Fatalf("过期会话应无效: %v, %v", ok, err)
	}
	if ok, err := s.ValidateClientSession(good); err != nil || !ok {
		t.Fatalf("有效会话应有效: %v, %v", ok, err)
	}
}

// 插入失败时不返回看似可用的凭据。
func TestCreateCredentialFailureReturnsEmpty(t *testing.T) {
	s := openTemp(t)
	s.Close()
	if tok, err := s.CreateEnrollToken(time.Minute); err == nil || tok != "" {
		t.Fatalf("CreateEnrollToken = %q, %v，应为空串与错误", tok, err)
	}
	if tok, err := s.CreateClientSession(time.Hour); err == nil || tok != "" {
		t.Fatalf("CreateClientSession = %q, %v，应为空串与错误", tok, err)
	}
}

func TestHashTokenIsStableAndHex(t *testing.T) {
	a, b := HashToken("x"), HashToken("x")
	if a != b || len(a) != 64 {
		t.Fatalf("HashToken = %q", a)
	}
	if len(NewID()) != 32 {
		t.Fatal("NewID 应为 32 个十六进制字符")
	}
}

// 接入在一个事务里消费接入码并登记机器：成功时机器可用密钥认证；接入码无效返回 ErrInvalidToken 且不留机器行；
// 建机失败（空密钥、数据库拒绝插入）整体回滚，接入码仍可用。
func TestEnrollMachine(t *testing.T) {
	s := openTemp(t)
	tok, err := s.CreateEnrollToken(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrollMachine(tok, "mac", "", "/Users/u"); err == nil || errors.Is(err, ErrInvalidToken) {
		t.Fatalf("空密钥应报建机错误: %v", err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER no_machine BEFORE INSERT ON machines BEGIN SELECT RAISE(ABORT, 'no insert'); END"); err != nil {
		t.Fatal(err)
	}
	secret := RandomToken()
	if _, err := s.EnrollMachine(tok, "mac", secret, "/Users/u"); err == nil || errors.Is(err, ErrInvalidToken) {
		t.Fatalf("插入被拒时应报建机错误: %v", err)
	}
	if _, err := s.db.Exec("DROP TRIGGER no_machine"); err != nil {
		t.Fatal(err)
	}
	if list, err := s.ListMachines(); err != nil || len(list) != 0 {
		t.Fatalf("失败的接入不应留机器行: %+v, %v", list, err)
	}
	m, err := s.EnrollMachine(tok, "mac", secret, "/Users/u")
	if err != nil || m.ID == "" || m.Name != "mac" || m.HomeDir != "/Users/u" || m.EnrolledAt == "" {
		t.Fatalf("建机失败回滚后接入码应仍可用: %+v, %v", m, err)
	}
	if got, err := s.AuthMachine(secret); err != nil || got.ID != m.ID || got.HomeDir != "/Users/u" {
		t.Fatalf("AuthMachine = %+v, %v", got, err)
	}
	if _, err := s.EnrollMachine(tok, "mac2", RandomToken(), ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("用过的接入码: %v", err)
	}
	if _, err := s.EnrollMachine("nope", "mac2", RandomToken(), ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("不存在的接入码: %v", err)
	}
	expired, err := s.CreateEnrollToken(-time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnrollMachine(expired, "mac2", RandomToken(), ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("过期的接入码: %v", err)
	}
	if list, err := s.ListMachines(); err != nil || len(list) != 1 {
		t.Fatalf("无效接入码不应留机器行: %+v, %v", list, err)
	}
}

// 并发用同一接入码接入只成功一次、只留一台机器；两个 Store 开同一个库文件，模拟两个连接。
func TestEnrollMachineConcurrentOnce(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	stores := make([]*Store, 2)
	for i := range stores {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close() })
		stores[i] = s
	}
	tok, err := stores[0].CreateEnrollToken(10 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = stores[i%len(stores)].EnrollMachine(tok, "m", RandomToken(), "")
		}()
	}
	close(start)
	wg.Wait()
	ok := 0
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, ErrInvalidToken):
			t.Fatalf("第 %d 次接入: 意外错误 %v", i, err)
		}
	}
	if list, err := stores[0].ListMachines(); ok != 1 || err != nil || len(list) != 1 {
		t.Fatalf("并发接入成功 %d 次、%d 台机器（%v），应各为 1", ok, len(list), err)
	}
}

// 空密钥的哈希是公开常量，登记了就能被空 Bearer 认证，一律拒绝。
func TestCreateMachineRejectsEmptySecret(t *testing.T) {
	s := openTemp(t)
	if m, err := s.CreateMachine("m", "", "/h"); err == nil || m.ID != "" {
		t.Fatalf("CreateMachine 空密钥 = %+v, %v", m, err)
	}
	if list, err := s.ListMachines(); err != nil || len(list) != 0 {
		t.Fatalf("不应留机器行: %+v, %v", list, err)
	}
}

// 写密码哈希与删光登录会话在同一事务里：换密码后旧会话全部失效；删除失败时密码也不变。
func TestSetPasswordHashRevokesSessions(t *testing.T) {
	s := openTemp(t)
	if _, err := s.SetPasswordHash("old"); err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateClientSession(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateClientSession(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER no_delete BEFORE DELETE ON client_sessions BEGIN SELECT RAISE(ABORT, 'no delete'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPasswordHash("new"); err == nil {
		t.Fatal("删会话失败时应报错")
	}
	if v, ok, err := s.GetSetting("password_hash"); err != nil || !ok || v != "old" {
		t.Fatalf("删会话失败时密码不应变: %q, %v, %v", v, ok, err)
	}
	if _, err := s.db.Exec("DROP TRIGGER no_delete"); err != nil {
		t.Fatal(err)
	}
	ch, err := s.SetPasswordHash("new")
	if err != nil {
		t.Fatal(err)
	}
	if ch.Sessions != 2 {
		t.Fatalf("删掉的会话数 = %d，应为 2", ch.Sessions)
	}
	if v, ok, err := s.GetSetting("password_hash"); err != nil || !ok || v != "new" {
		t.Fatalf("password_hash = %q, %v, %v", v, ok, err)
	}
	for _, tok := range []string{a, b} {
		if ok, err := s.ValidateClientSession(tok); err != nil || ok {
			t.Fatalf("换密码后旧会话应失效: %v, %v", ok, err)
		}
	}
}

// 换密码同一事务里删掉全部推送订阅（丢了的设备不再收到审批摘要），并记审计 push_subscriptions_cleared
// （{"reason":"password_changed","count":n}）：只有原因与条数，不含密钥、端点与哈希。
// 删订阅失败时整个事务回滚：密码、会话、订阅都不变，也不记审计。
func TestSetPasswordHashRemovesPushSubscriptions(t *testing.T) {
	s := openTemp(t)
	if _, err := s.SetPasswordHash("old"); err != nil {
		t.Fatal(err)
	}
	sess, err := s.CreateClientSession(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range []string{"https://web.push.apple.com/secret-path-1", "https://fcm.googleapis.com/fcm/send/secret-path-2"} {
		if _, err := s.UpsertPushSubscription(ep, "p256dh-secret", "auth-secret", "ua"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.db.Exec("CREATE TRIGGER no_delete BEFORE DELETE ON push_subscriptions BEGIN SELECT RAISE(ABORT, 'no delete'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPasswordHash("new"); err == nil {
		t.Fatal("删订阅失败时应报错")
	}
	if v, _, _ := s.GetSetting("password_hash"); v != "old" {
		t.Fatalf("删订阅失败时密码不应变: %q", v)
	}
	if ok, _ := s.ValidateClientSession(sess); !ok {
		t.Fatal("删订阅失败时会话不应被删")
	}
	if n, _ := s.CountPushSubscriptions(); n != 2 {
		t.Fatalf("删订阅失败时订阅数 = %d，应仍为 2", n)
	}
	if evs, _ := s.ListAudit(0, ""); len(evs) != 1 {
		t.Fatalf("回滚后不应多出审计（只有设初始密码那条）: %+v", evs)
	}
	if _, err := s.db.Exec("DROP TRIGGER no_delete"); err != nil {
		t.Fatal(err)
	}
	ch, err := s.SetPasswordHash("new")
	if err != nil {
		t.Fatal(err)
	}
	if ch != (PasswordChange{Sessions: 1, PushSubscriptions: 2}) {
		t.Fatalf("换密码删掉的 = %+v", ch)
	}
	if n, _ := s.CountPushSubscriptions(); n != 0 {
		t.Fatalf("换密码后订阅数 = %d，应为 0", n)
	}
	evs, err := s.ListAudit(0, "")
	if err != nil || len(evs) != 2 {
		t.Fatalf("审计: %+v %v", evs, err)
	}
	e := evs[0]
	if e.Action != "push_subscriptions_cleared" || e.Actor != "system" || e.MachineID != "" || e.RequestID != "" {
		t.Fatalf("审计行: %+v", e)
	}
	if e.Detail != `{"reason":"password_changed","count":2}` {
		t.Fatalf("审计 detail = %s", e.Detail)
	}
	for _, secret := range []string{"secret-path", "push.apple", "fcm", "p256dh-secret", "auth-secret", `"new"`, `"old"`} {
		if strings.Contains(e.Detail, secret) {
			t.Fatalf("审计不能含 %q: %s", secret, e.Detail)
		}
	}
	// 没有订阅、没有会话时照样成功，条数为 0（审计照记，count 为 0）
	if ch, err := s.SetPasswordHash("newer"); err != nil || ch != (PasswordChange{}) {
		t.Fatalf("再换一次: %+v %v", ch, err)
	}
	if evs, _ := s.ListAudit(0, ""); len(evs) != 3 || evs[0].Detail != `{"reason":"password_changed","count":0}` {
		t.Fatalf("再换一次的审计: %+v", evs)
	}
}
