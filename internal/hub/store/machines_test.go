package store

import (
	"errors"
	"path/filepath"
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
	expired, _ := s.CreateEnrollToken(-time.Second)
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
	if ok, _ := s.ValidateClientSession(tok); !ok {
		t.Fatal("新会话应有效")
	}
	if ok, _ := s.ValidateClientSession("nope"); ok {
		t.Fatal("不存在的会话应无效")
	}
	old, _ := s.CreateClientSession(-time.Minute)
	if ok, _ := s.ValidateClientSession(old); ok {
		t.Fatal("过期会话应无效")
	}
	if err := s.DeleteClientSession(tok); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.ValidateClientSession(tok); ok {
		t.Fatal("删除后应无效")
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
