package store

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// v1 库：只建 schema_version 和旧形状的 push_subscriptions（其余表迁移不碰，这里不需要）。
const v1PushSQL = `
CREATE TABLE schema_version (v INTEGER NOT NULL);
INSERT INTO schema_version(v) VALUES(1);
CREATE TABLE push_subscriptions (id TEXT PRIMARY KEY, endpoint TEXT NOT NULL, keys TEXT NOT NULL, created_at TEXT NOT NULL, last_ok_at TEXT);
INSERT INTO push_subscriptions VALUES('a', 'https://p.example/1', '{"p256dh":"x","auth":"y"}', '2026-10-23T00:00:00.000000000Z', NULL);
INSERT INTO push_subscriptions VALUES('b', 'https://p.example/1', '{"p256dh":"x2","auth":"y2"}', '2026-10-23T00:00:01.000000000Z', NULL);
INSERT INTO push_subscriptions VALUES('c', 'https://p.example/2', '{"p256dh":"z","auth":"w"}', '2026-10-23T00:00:02.000000000Z', NULL);
`

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// writeRaw 不经过 Open 的迁移，直接用 stmts 建一个库。
func writeRaw(t *testing.T, path, stmts string) {
	t.Helper()
	raw := openRaw(t, path)
	defer raw.Close()
	if _, err := raw.Exec(stmts); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateV1ToV2(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	writeRaw(t, path, v1PushSQL)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开 v1 库应升级: %v", err)
	}
	defer s.Close()
	assertPerms(t, path)
	var v int
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil || v != 2 {
		t.Fatalf("版本 = %d, %v", v, err)
	}
	subs, err := s.ListPushSubscriptions()
	if err != nil || len(subs) != 2 {
		t.Fatalf("重复端点应只留一条: %+v %v", subs, err)
	}
	if subs[0].Endpoint != "https://p.example/1" || subs[0].Failures != 0 || subs[0].P256dh != "x" {
		t.Fatalf("迁移后的行: %+v", subs[0])
	}
	// 唯一约束生效
	if _, err := s.db.Exec("INSERT INTO push_subscriptions(id, endpoint, keys, created_at) VALUES('d', 'https://p.example/2', '{}', '2026')"); err == nil {
		t.Fatal("重复 endpoint 应被拒")
	}
	// 再打开一次：版本一致，不再迁移
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	assertPerms(t, path)
	if n, err := s2.CountPushSubscriptions(); err != nil || n != 2 {
		t.Fatalf("重开后订阅数 = %d, %v", n, err)
	}
}

func TestMigrateRefusesNewerDB(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	writeRaw(t, path, "CREATE TABLE schema_version (v INTEGER NOT NULL); INSERT INTO schema_version(v) VALUES(99);")
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("比程序新的库应拒绝打开")
	}
}

// 迁移中途出错时整个事务回滚：版本仍是 1，不留半成品表。
func TestMigrateFailureRollsBack(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	// v1 库却没有 push_subscriptions 表，迁移到 2 的 INSERT … SELECT 会失败
	writeRaw(t, path, "CREATE TABLE schema_version (v INTEGER NOT NULL); INSERT INTO schema_version(v) VALUES(1);")
	if s, err := Open(path); err == nil {
		s.Close()
		t.Fatal("迁移失败应报错")
	}
	raw := openRaw(t, path)
	defer raw.Close()
	var v int
	if err := raw.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil || v != 1 {
		t.Fatalf("迁移失败后版本 = %d, %v", v, err)
	}
	if tableExists(t, path, "push_subscriptions_v2") {
		t.Fatal("迁移失败不应留下 push_subscriptions_v2")
	}
}

func TestFreshDBIsV2(t *testing.T) {
	s := openTemp(t)
	var v int
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil || v != 2 {
		t.Fatalf("新库版本 = %d, %v", v, err)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('push_subscriptions') WHERE name = 'failures'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("新库应有 failures 列: %d %v", n, err)
	}
}
