// Package store 是 hub 的 SQLite 存储层。所有时间列是 timeLayout 定宽 UTC 文本，字典序即时间先后。
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const schemaVersion = 1

// ErrNotFound 表示查无此行。
var ErrNotFound = errors.New("not found")

// Store 包着一个数据库连接池。
type Store struct {
	db *sql.DB
}

// Open 打开或创建数据库，设 WAL 与 busy_timeout，跑迁移。
// 主库文件在交给 SQLite 之前就建好并收紧为 0600，SQLite 建 -wal、-shm 时沿用主库权限。
func Open(path string) (*Store, error) {
	if err := prepareFile(path); err != nil {
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite 单写者，用一个连接避免 SQLITE_BUSY
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开数据库 %s: %w", path, err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// prepareFile 以 0600 新建主库文件；已有文件也收紧为 0600。
func prepareFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("创建数据库文件 %s: %w", path, err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(schemaSQL); err != nil {
		return fmt.Errorf("建表: %w", err)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		_, err := s.db.Exec("INSERT INTO schema_version(v) VALUES(?)", schemaVersion)
		return err
	}
	var v int
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil {
		return err
	}
	if v != schemaVersion {
		return fmt.Errorf("数据库版本 %d 与程序版本 %d 不一致", v, schemaVersion)
	}
	return nil
}

// Close 关闭连接池。
func (s *Store) Close() error { return s.db.Close() }

// timeLayout 是时间列的定宽格式：纳秒补足 9 位、UTC 以 Z 结尾，按字典序比较即按时间先后。
// time.RFC3339Nano 会去掉小数末尾的 0，变长串在 SQL 里比较会排反，所以写入不用它；读回可用它解析。
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// fmtTime 把 t 转成 UTC 再按 timeLayout 格式化，所有写时间列的地方都用它。
func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// Now 返回统一格式的当前时间。
func Now() string { return fmtTime(time.Now()) }

// GetSetting 读设置，不存在时 ok 为 false。
func (s *Store) GetSetting(key string) (string, bool, error) {
	var v string
	err := s.db.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

// SetSetting 写设置，存在则覆盖。
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec("INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, value)
	return err
}

// Tx 在一个事务里跑 fn，出错回滚。
func (s *Store) Tx(fn func(tx *sql.Tx) error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
