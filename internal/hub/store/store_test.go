package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(tempDir(t), "hub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// assertPerms 断言主库为 0600；-wal、-shm 若存在也必须是 0600。
func assertPerms(t *testing.T, path string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("数据库权限 = %v", st.Mode().Perm())
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		st, err := os.Stat(path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s 权限 = %v", suffix, st.Mode().Perm())
		}
	}
}

func TestOpenMigratesAndSetsPerms(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertPerms(t, path)
	var v int
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("schema_version = %d, %v", v, err)
	}
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal_mode = %s, %v", mode, err)
	}
	var fk, busy int
	if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign_keys = %d, %v", fk, err)
	}
	if err := s.db.QueryRow("PRAGMA busy_timeout").Scan(&busy); err != nil || busy != 5000 {
		t.Fatalf("busy_timeout = %d, %v", busy, err)
	}
	// 终审 M7：删改的内容（如清空的 webhook 回调令牌）用零覆盖，不残留在空闲页里
	var secure int
	if err := s.db.QueryRow("PRAGMA secure_delete").Scan(&secure); err != nil || secure != 1 {
		t.Fatalf("secure_delete = %d, %v", secure, err)
	}
	for _, table := range []string{"settings", "machines", "enroll_tokens", "client_sessions", "sessions", "requests", "rules", "audit_events", "push_subscriptions"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("表 %s: n=%d, %v", table, n, err)
		}
	}
	s.Close()
	// 已有文件权限过宽时，重开要收紧；重复打开不应报错（迁移幂等）
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	assertPerms(t, path)
	var n int
	if err := s2.db.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema_version 行数 = %d, %v", n, err)
	}
}

// tableExists 用一个独立连接查表是否存在，不经过 Open 的迁移。
func tableExists(t *testing.T, path, table string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestOpenRejectsOtherSchemaVersion(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE schema_version SET v = ?", SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("DROP TABLE push_subscriptions"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(path)
	if err == nil {
		s2.Close()
		t.Fatal("版本不一致应报错")
	}
	if want := fmt.Sprintf("数据库版本 %d 比程序（%d）新，请升级 railse", SchemaVersion+1, SchemaVersion); !strings.Contains(err.Error(), want) {
		t.Fatalf("错误应说明库比程序新: %v", err)
	}
	if tableExists(t, path, "push_subscriptions") {
		t.Fatal("版本不一致时不应往库里补建表")
	}
}

func TestOpenRejectsCorruptSchemaVersion(t *testing.T) {
	cases := map[string]string{
		"多行": "INSERT INTO schema_version(v) VALUES(1)",
		"空表": "DELETE FROM schema_version",
	}
	for name, stmt := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(tempDir(t), "hub.db")
			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatal(err)
			}
			s.Close()
			s2, err := Open(path)
			if err == nil {
				s2.Close()
				t.Fatal("schema_version 不是恰好一行时应报错")
			}
			if !strings.Contains(err.Error(), "损坏") {
				t.Fatalf("错误应说明损坏: %v", err)
			}
		})
	}
}

func TestOpenPathWithURIChars(t *testing.T) {
	dir := tempDir(t)
	name := "hub#1%41?x.db"
	path := filepath.Join(dir, name)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assertPerms(t, path)
	if err := s.SetSetting("k", "v"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	// SQLite 打开的必须就是预建的文件，目录里不能冒出别的库文件
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != name && e.Name() != name+"-wal" && e.Name() != name+"-shm" {
			t.Fatalf("多出了文件 %q", e.Name())
		}
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, ok, err := s2.GetSetting("k"); err != nil || !ok || v != "v" {
		t.Fatalf("重开后 GetSetting = %q %v %v", v, ok, err)
	}
}

func TestOpenRelativePath(t *testing.T) {
	dir := tempDir(t)
	t.Chdir(dir)
	s, err := Open("hub.db")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertPerms(t, filepath.Join(dir, "hub.db"))
}

func TestSettings(t *testing.T) {
	s := openTemp(t)
	if _, ok, err := s.GetSetting("x"); err != nil || ok {
		t.Fatalf("不存在的键: ok=%v err=%v", ok, err)
	}
	if err := s.SetSetting("x", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("x", "2"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.GetSetting("x")
	if err != nil || !ok || v != "2" {
		t.Fatalf("GetSetting = %q %v %v", v, ok, err)
	}
}

func TestTxRollsBackOnError(t *testing.T) {
	s := openTemp(t)
	err := s.Tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO settings(key, value) VALUES('a','1')"); err != nil {
			return err
		}
		return os.ErrInvalid
	})
	if !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("应把错误原样传出: %v", err)
	}
	if _, ok, err := s.GetSetting("a"); err != nil || ok {
		t.Fatalf("出错的事务应回滚: ok=%v err=%v", ok, err)
	}
}

func TestTxPanicRollsBackAndReleasesConn(t *testing.T) {
	s := openTemp(t)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("回调的 panic 应继续向上抛")
			}
		}()
		s.Tx(func(tx *sql.Tx) error {
			if _, err := tx.Exec("INSERT INTO settings(key, value) VALUES(?, ?)", "p", "1"); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	// 单连接：panic 后若没回滚，连接被永久占住，下面的查询会一直阻塞
	done := make(chan error, 1)
	go func() {
		_, ok, err := s.GetSetting("p")
		if err == nil && ok {
			err = errors.New("panic 的事务应回滚")
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("panic 后连接未释放，GetSetting 阻塞")
	}
}

func TestTxBeginsImmediate(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// 另一个进程式的独立连接，不等锁
	other, err := sql.Open("sqlite", path+"?_busy_timeout=0")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	err = s.Tx(func(tx *sql.Tx) error {
		// 回调里还没读写；BEGIN IMMEDIATE 应已拿到写锁，别的连接写不进来
		_, err := other.Exec("INSERT INTO settings(key, value) VALUES(?, ?)", "o", "1")
		if err == nil {
			return errors.New("事务期间另一连接写入成功，事务不是 BEGIN IMMEDIATE")
		}
		if !strings.Contains(err.Error(), "locked") {
			return fmt.Errorf("另一连接应因写锁失败: %w", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTxCommits(t *testing.T) {
	s := openTemp(t)
	err := s.Tx(func(tx *sql.Tx) error {
		_, err := tx.Exec("INSERT INTO settings(key, value) VALUES(?, ?)", "b", "1")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.GetSetting("b"); err != nil || !ok || v != "1" {
		t.Fatalf("提交后 GetSetting = %q %v %v", v, ok, err)
	}
}

func TestTimeFormatSortsLexically(t *testing.T) {
	// 同一秒内 .12345 早于 .123456；RFC3339Nano 会写成 ".12345Z" 与 ".123456Z"，字典序反了
	early := time.Date(2026, 10, 22, 8, 0, 0, 123450000, time.UTC)
	late := time.Date(2026, 10, 22, 8, 0, 0, 123456000, time.UTC)
	a, b := fmtTime(early), fmtTime(late)
	if a >= b {
		t.Fatalf("字典序与时间先后不一致: %q !< %q", a, b)
	}
	if len(a) != len(b) {
		t.Fatalf("应为定宽: %q %q", a, b)
	}
	// 非 UTC 时区先转 UTC
	cst := time.FixedZone("CST", 8*3600)
	if got := fmtTime(early.In(cst)); got != a {
		t.Fatalf("非 UTC 输入 = %q, 应为 %q", got, a)
	}
	// 读回与原值相等
	back, err := time.Parse(time.RFC3339Nano, a)
	if err != nil || !back.Equal(early) {
		t.Fatalf("读回 = %v, %v", back, err)
	}
	now := Now()
	if len(now) != len(a) || !strings.HasSuffix(now, "Z") {
		t.Fatalf("Now() = %q", now)
	}
	if _, err := time.Parse(time.RFC3339Nano, now); err != nil {
		t.Fatalf("Now() 不可解析: %v", err)
	}
}

func TestFormatTimeMatchesFmtTime(t *testing.T) {
	cst := time.FixedZone("CST", 8*3600)
	for _, in := range []time.Time{
		time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC),
		time.Date(2026, 10, 22, 16, 0, 0, 120000000, cst),
		time.Now().Add(10 * time.Minute),
	} {
		got := FormatTime(in)
		if got != fmtTime(in) {
			t.Fatalf("FormatTime(%v) = %q, fmtTime = %q", in, got, fmtTime(in))
		}
		if len(got) != len(Now()) || !strings.HasSuffix(got, "Z") {
			t.Fatalf("FormatTime(%v) = %q，应为定宽 UTC", in, got)
		}
	}
	// 非 UTC 输入先转 UTC：东八区 16:00 即 UTC 08:00
	if got := FormatTime(time.Date(2026, 10, 22, 16, 0, 0, 0, cst)); got != "2026-10-22T08:00:00.000000000Z" {
		t.Fatalf("FormatTime 东八区 = %q", got)
	}
}
