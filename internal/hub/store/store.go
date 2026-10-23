// Package store 是 hub 的 SQLite 存储层。所有时间列是 timeLayout 定宽 UTC 文本，字典序即时间先后。
package store

import (
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

const schemaVersion = 2

// migrations 是逐版本升级的 SQL，键是升级后的版本号。改 schema.sql 时必须同时加一条迁移并提高 schemaVersion：
// 已有库只跑迁移，不再执行 schema.sql。
var migrations = map[int]string{
	// 2：push_subscriptions 的 endpoint 唯一，加 user_agent 与 failures。旧库里重复的端点按 created_at 只留最早的一条。
	2: `
CREATE TABLE push_subscriptions_v2 (
  id TEXT PRIMARY KEY, endpoint TEXT NOT NULL UNIQUE, keys TEXT NOT NULL, user_agent TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL, last_ok_at TEXT, failures INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO push_subscriptions_v2(id, endpoint, keys, created_at, last_ok_at)
  SELECT id, endpoint, keys, created_at, last_ok_at FROM push_subscriptions ORDER BY created_at;
DROP TABLE push_subscriptions;
ALTER TABLE push_subscriptions_v2 RENAME TO push_subscriptions;
`,
}

// ErrNotFound 表示查无此行。
var ErrNotFound = errors.New("not found")

// Store 包着一个 SQLite 数据库，连接池上限为单连接（SetMaxOpenConns(1)）。单连接的约束：
// Tx 回调里只能用 tx，不得调用 s.db 或其他 Store 方法，否则回调占着唯一的连接又去等连接，自锁永久挂死；
// Tx 回调里不做网络等阻塞 IO，否则其他调用都要一直等到该 IO 结束、事务结束；
// Tx 之外用 s.db 查询得到的 rows 必须先 Close 再发下一个查询，否则 rows 占着唯一的连接，下一个查询等不到连接而挂死。
type Store struct {
	db *sql.DB
}

// dsnQuery 是 modernc.org/sqlite 的连接参数：_busy_timeout 最先应用；
// _txlock=immediate 让事务以 BEGIN IMMEDIATE 开始，跨进程（如 railse hub token）写同一库时先拿写锁；
// secure_delete 让删改掉的内容用零覆盖，清空的 webhook 回调令牌之类不残留在空闲页里。
const dsnQuery = "_busy_timeout=5000&_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=secure_delete(1)"

// Open 打开或创建数据库，设 WAL 与 busy_timeout，跑迁移。
// 主库文件在交给 SQLite 之前就建好并收紧为 0600，SQLite 建 -wal、-shm 时沿用主库权限。
func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("数据库路径 %s: %w", path, err)
	}
	if err := prepareFile(abs); err != nil {
		return nil, err
	}
	// 用 URL 编码路径：路径里的 %、#、? 不会被 SQLite 当成转义、片段或参数，打开的就是上面预建的文件。
	// 路径必须是绝对路径，否则相对路径的第一段会被当成 URI 的 authority。
	dsn := (&url.URL{Scheme: "file", Path: abs, RawQuery: dsnQuery}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库 %s: %w", abs, err)
	}
	db.SetMaxOpenConns(1) // SQLite 单写者，用一个连接避免 SQLITE_BUSY；约束见 Store 注释
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("打开数据库 %s: %w", abs, err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("迁移数据库 %s: %w", abs, err)
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
		return fmt.Errorf("收紧数据库文件权限 %s: %w", path, err)
	}
	return f.Close()
}

// migrate 在一个事务里检查并建表：schema_version 不存在时建全部表并写入版本；
// 已存在时核对版本：比程序新就报错且不改动库，比程序旧就依次跑 migrations 升到 schemaVersion，
// 任何一步出错整个事务回滚，库保持原版本。
func (s *Store) migrate() error {
	return s.Tx(func(tx *sql.Tx) error {
		var exists int
		if err := tx.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_version'").Scan(&exists); err != nil {
			return fmt.Errorf("查 schema_version 表: %w", err)
		}
		if exists == 0 {
			if _, err := tx.Exec(schemaSQL); err != nil {
				return fmt.Errorf("建表: %w", err)
			}
			if _, err := tx.Exec("INSERT INTO schema_version(v) VALUES(?)", schemaVersion); err != nil {
				return fmt.Errorf("写 schema_version: %w", err)
			}
			return nil
		}
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM schema_version").Scan(&n); err != nil {
			return fmt.Errorf("读 schema_version: %w", err)
		}
		if n != 1 {
			return fmt.Errorf("schema_version 有 %d 行，数据库已损坏", n)
		}
		var v int
		if err := tx.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil {
			return fmt.Errorf("读 schema_version: %w", err)
		}
		switch {
		case v > schemaVersion:
			return fmt.Errorf("数据库版本 %d 比程序（%d）新，请升级 railse", v, schemaVersion)
		case v < schemaVersion:
			for next := v + 1; next <= schemaVersion; next++ {
				sqlText, ok := migrations[next]
				if !ok {
					return fmt.Errorf("缺少到版本 %d 的迁移", next)
				}
				if _, err := tx.Exec(sqlText); err != nil {
					return fmt.Errorf("迁移到版本 %d: %w", next, err)
				}
				// schema_version 只有一行，上面已校验 n == 1
				if _, err := tx.Exec("UPDATE schema_version SET v = ?", next); err != nil {
					return fmt.Errorf("写 schema_version: %w", err)
				}
			}
		}
		return nil
	})
}

// Close 关闭数据库。
func (s *Store) Close() error { return s.db.Close() }

// timeLayout 是时间列的定宽格式：纳秒补足 9 位、UTC 以 Z 结尾，按字典序比较即按时间先后。
// time.RFC3339Nano 会去掉小数末尾的 0，变长串在 SQL 里比较会排反，所以写入不用它；读回可用它解析。
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

// fmtTime 把 t 转成 UTC 再按 timeLayout 格式化，所有写时间列的地方都用它。
func fmtTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// FormatTime 返回 fmtTime(t)。hub 包与测试写时间列（如 expires_at 这类非当前时间）时用它，保证定宽可比较。
func FormatTime(t time.Time) string { return fmtTime(t) }

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

// SetSettingIfAbsent 只在没有该键时写入，返回是否真的写了。两个进程同时首次写同一个键时只有一个写成；
// 调用方随后再读一次，拿到的就是写成的那个值。
func (s *Store) SetSettingIfAbsent(key, value string) (bool, error) {
	res, err := s.db.Exec("INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO NOTHING", key, value)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// Tx 在一个事务（BEGIN IMMEDIATE）里跑 fn：fn 返回 nil 就提交，返回错误或 panic 就回滚，
// panic 回滚后继续向上抛。回滚失败的错误与 fn 的错误一起返回。
// 单连接：回调里只能用 tx，不得调用 s.db 或其他 Store 方法，否则自锁永久挂死；
// 回调里不做网络等阻塞 IO，否则其他调用都要一直等到该 IO 结束、事务结束。详见 Store 的注释。
func (s *Store) Tx(fn func(tx *sql.Tx) error) (err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("开始事务: %w", err)
	}
	defer func() {
		// 已提交时 Rollback 只返回 ErrTxDone；其余情况（含 panic）在这里回滚并释放连接
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			err = errors.Join(err, fmt.Errorf("回滚事务: %w", rbErr))
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
