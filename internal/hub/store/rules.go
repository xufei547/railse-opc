package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"gopkg.in/yaml.v3"

	"github.com/xufei547/railse-opc/internal/policy"
)

// RuleRow 是 rules 表的一行。YAML 是单条规则，不带 enabled；启停只看 Enabled（enabled 列）。
type RuleRow struct {
	ID          string
	YAML        string
	Source      string
	Enabled     bool
	HitCount    int
	LastHitAt   *string
	CreatedAt   string
	CreatedFrom string
}

// querier 是 *sql.DB 与 *sql.Tx 共有的查询方法：同一套读取既能在事务外用 s.db，也能在 Tx 回调里只用 tx。
type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// rulesVersion 读 settings 里的 rules_version，没有时为 0；值损坏时报错，不悄悄从 0 重新计数。
func rulesVersion(q querier) (int, error) {
	var v string
	err := q.QueryRow("SELECT value FROM settings WHERE key = 'rules_version'").Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("rules_version %q 损坏: %w", v, err)
	}
	return n, nil
}

// RulesVersion 返回规则版本号，没有时为 0。
func (s *Store) RulesVersion() (int, error) { return rulesVersion(s.db) }

// bumpVersion 在事务里把规则版本号加一并返回新值。
func bumpVersion(tx *sql.Tx) (int, error) {
	n, err := rulesVersion(tx)
	if err != nil {
		return 0, err
	}
	n++
	_, err = tx.Exec("INSERT INTO settings(key, value) VALUES('rules_version', ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", strconv.Itoa(n))
	return n, err
}

// EnsureDefaultRules 只在规则版本为 0（从未初始化）时导入内置规则包，返回当前版本。
// 不按“表为空”判断：用户删光规则或导入空集后重启，不会悄悄恢复内置放行规则。
// 判断与导入在同一事务里，两个进程同时首次启动也只导入一次。
// 自开事务，不得在 Tx 回调里调用（单连接会永久自锁）。
func (s *Store) EnsureDefaultRules() (int, error) {
	var version int
	err := s.Tx(func(tx *sql.Tx) error {
		var err error
		if version, err = rulesVersion(tx); err != nil || version > 0 {
			return err
		}
		version, err = replaceRulesTx(tx, policy.DefaultRuleSet())
		return err
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}

// insertRuleTx 插入一条规则。enabled 写进列，存的 YAML 去掉 enabled，免得两处不一致。
func insertRuleTx(tx *sql.Tx, r policy.Rule) error {
	enabled := 1
	if !r.IsEnabled() {
		enabled = 0
	}
	r.Enabled = nil
	b, err := yaml.Marshal(r)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO rules(id, yaml, source, enabled, created_at, created_from) VALUES(?, ?, ?, ?, ?, ?)",
		r.ID, string(b), string(r.Source), enabled, Now(), r.CreatedFrom)
	return err
}

// replaceRulesTx 在事务里清空规则、按顺序插入 rs 并加版本。调用方负责先校验 rs。
func replaceRulesTx(tx *sql.Tx, rs policy.RuleSet) (int, error) {
	if _, err := tx.Exec("DELETE FROM rules"); err != nil {
		return 0, err
	}
	for _, r := range rs.Rules {
		if err := insertRuleTx(tx, r); err != nil {
			return 0, err
		}
	}
	return bumpVersion(tx)
}

// ReplaceRules 用一整套规则替换现有规则，先校验，版本加一。
// 自开事务，不得在 Tx 回调里调用（单连接会永久自锁）。
func (s *Store) ReplaceRules(rs policy.RuleSet) (int, error) {
	if err := rs.Validate(); err != nil {
		return 0, err
	}
	var version int
	err := s.Tx(func(tx *sql.Tx) error {
		var err error
		version, err = replaceRulesTx(tx, rs)
		return err
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}

// InsertRule 加一条规则，返回实际 id 与新版本。id 冲突时追加 -2、-3…；合并后的整套先校验再入库。
// 读整套、选 id、校验、插入、加版本在同一事务里，并发插入同一 id 也各得一个不冲突的 id。
// 自开事务，不得在 Tx 回调里调用（单连接会永久自锁）。
func (s *Store) InsertRule(r policy.Rule) (string, int, error) {
	var version int
	err := s.Tx(func(tx *sql.Tx) error {
		rs, _, err := ruleSet(tx)
		if err != nil {
			return err
		}
		taken := map[string]bool{}
		for _, x := range rs.Rules {
			taken[x.ID] = true
		}
		id := r.ID
		for i := 2; taken[id]; i++ {
			id = fmt.Sprintf("%s-%d", r.ID, i)
		}
		r.ID = id
		rs.Rules = append(rs.Rules, r)
		if err := rs.Validate(); err != nil {
			return err
		}
		if err := insertRuleTx(tx, r); err != nil {
			return err
		}
		version, err = bumpVersion(tx)
		return err
	})
	if err != nil {
		return "", 0, err
	}
	return r.ID, version, nil
}

// SetRuleEnabled 启停一条规则，版本加一；不存在返回 ErrNotFound。
// 自开事务，不得在 Tx 回调里调用（单连接会永久自锁）。
func (s *Store) SetRuleEnabled(id string, enabled bool) (int, error) {
	e := 0
	if enabled {
		e = 1
	}
	return s.changeRule("UPDATE rules SET enabled = ? WHERE id = ?", e, id)
}

// DeleteRule 删一条规则，版本加一；不存在返回 ErrNotFound。
// 自开事务，不得在 Tx 回调里调用（单连接会永久自锁）。
func (s *Store) DeleteRule(id string) (int, error) {
	return s.changeRule("DELETE FROM rules WHERE id = ?", id)
}

// changeRule 在一个事务里执行只应影响一行的改动并加版本；影响的不是恰好一行就回滚并返回 ErrNotFound。
func (s *Store) changeRule(query string, args ...any) (int, error) {
	var version int
	err := s.Tx(func(tx *sql.Tx) error {
		res, err := tx.Exec(query, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrNotFound
		}
		version, err = bumpVersion(tx)
		return err
	})
	if err != nil {
		return 0, err
	}
	return version, nil
}

// listRules 按入库顺序（rowid）列出规则行，不依赖墙钟：新行的 rowid 总比现有行大。
func listRules(q querier) ([]RuleRow, error) {
	rows, err := q.Query("SELECT id, yaml, source, enabled, hit_count, last_hit_at, created_at, created_from FROM rules ORDER BY rowid")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuleRow
	for rows.Next() {
		var r RuleRow
		var enabled int
		if err := rows.Scan(&r.ID, &r.YAML, &r.Source, &enabled, &r.HitCount, &r.LastHitAt, &r.CreatedAt, &r.CreatedFrom); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRules 按入库顺序列出规则行。
func (s *Store) ListRules() ([]RuleRow, error) { return listRules(s.db) }

// ruleSet 把全部规则组装成 policy.RuleSet（enabled 列写进 Rule.Enabled），连同版本号一起读。
// 每条 YAML 严格解码（未知字段报错），整套返回前再跑 Validate：库里的行被改坏时报错，不会悄悄放宽成更宽的规则。
// 规则行读完、rows 关闭后才读版本号，在 Tx 里也不会两个查询同时占着连接。
func ruleSet(q querier) (policy.RuleSet, int, error) {
	rows, err := listRules(q)
	if err != nil {
		return policy.RuleSet{}, 0, err
	}
	rs := policy.RuleSet{Version: policy.RuleSetVersion}
	for _, row := range rows {
		var r policy.Rule
		dec := yaml.NewDecoder(bytes.NewReader([]byte(row.YAML)))
		dec.KnownFields(true)
		if err := dec.Decode(&r); err != nil {
			return policy.RuleSet{}, 0, fmt.Errorf("规则 %s 的 YAML 损坏: %w", row.ID, err)
		}
		enabled := row.Enabled
		r.Enabled = &enabled
		rs.Rules = append(rs.Rules, r)
	}
	if err := rs.Validate(); err != nil {
		return policy.RuleSet{}, 0, fmt.Errorf("库里的规则不合法: %w", err)
	}
	v, err := rulesVersion(q)
	if err != nil {
		return policy.RuleSet{}, 0, err
	}
	return rs, v, nil
}

// RuleSet 返回全部规则与版本号。两者在同一事务里读，快照一致：下发给守护进程的规则一定对应这个版本号。
// 自开事务，不得在 Tx 回调里调用（单连接会永久自锁）。
func (s *Store) RuleSet() (policy.RuleSet, int, error) {
	var (
		rs      policy.RuleSet
		version int
	)
	err := s.Tx(func(tx *sql.Tx) error {
		var err error
		rs, version, err = ruleSet(tx)
		return err
	})
	if err != nil {
		return policy.RuleSet{}, 0, err
	}
	return rs, version, nil
}

// RecordHit 记一次命中（不改版本号）。id 不存在时什么也不做。
func (s *Store) RecordHit(id string) error {
	_, err := s.db.Exec("UPDATE rules SET hit_count = hit_count + 1, last_hit_at = ? WHERE id = ?", Now(), id)
	return err
}
