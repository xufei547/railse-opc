package store

import (
	"database/sql"
	"errors"
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
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil || v != 1 {
		t.Fatalf("schema_version = %d, %v", v, err)
	}
	var mode string
	s.db.QueryRow("PRAGMA journal_mode").Scan(&mode)
	if mode != "wal" {
		t.Fatalf("journal_mode = %s", mode)
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

func TestOpenRejectsOtherSchemaVersion(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE schema_version SET v = ?", schemaVersion+1); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s2, err := Open(path); err == nil {
		s2.Close()
		t.Fatal("版本不一致应报错")
	}
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
	if _, ok, _ := s.GetSetting("a"); ok {
		t.Fatal("出错的事务应回滚")
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
	if !(a < b) {
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
