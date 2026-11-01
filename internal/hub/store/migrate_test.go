package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/xufei547/railse-opc/internal/proto"
)

// schemaV2 读冻结的版本 2 建表语句（testdata/schema_v2.sql），不含 schema_version 的那一行。
func schemaV2(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "schema_v2.sql"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// writeV1 造一个 v1 库：v2 的建表换回旧形状的 push_subscriptions（v1 与 v2 只差这张表），版本 1。
func writeV1(t *testing.T, path string) {
	t.Helper()
	writeRaw(t, path, schemaV2(t)+"INSERT INTO schema_version(v) VALUES(1);\nDROP TABLE push_subscriptions;\n"+v1PushSQL)
}

// v1PushSQL 是旧形状的 push_subscriptions 与几行数据（含重复端点）。
const v1PushSQL = `
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

// v1 库逐版本升到最新：推送订阅去重、加列。
func TestMigrateV1(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	writeV1(t, path)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开 v1 库应升级: %v", err)
	}
	defer s.Close()
	assertPerms(t, path)
	var v int
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil || v != SchemaVersion {
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

func TestFreshDBIsLatest(t *testing.T) {
	s := openTemp(t)
	var v int
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil || v != 5 || SchemaVersion != 5 {
		t.Fatalf("新库版本 = %d, %v（程序 %d）", v, err, SchemaVersion)
	}
	for _, c := range []struct{ table, col string }{{"push_subscriptions", "failures"}, {"requests", "ask"}, {"requests", "answers"}} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?", c.table, c.col).Scan(&n); err != nil || n != 1 {
			t.Fatalf("新库 %s 应有 %s 列: %d %v", c.table, c.col, n, err)
		}
	}
}

// tableSchema 描述一张表的列（pragma table_info）与索引（pragma index_list + index_info，不比索引名：
// 迁移里的表可能是建成 _v2 再改名的，自动索引的名字可能不同）。
func tableSchema(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	var out []string
	rows, err := db.Query("SELECT cid, name, type, \"notnull\", COALESCE(dflt_value, 'NULL'), pk FROM pragma_table_info(?) ORDER BY cid", table)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ, dflt string
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("col %d %s %s notnull=%d default=%s pk=%d", cid, name, typ, notnull, dflt, pk))
	}
	rows.Close()
	type index struct {
		name                    string
		unique, partial, origin string
	}
	var idx []index
	rows, err = db.Query("SELECT name, \"unique\", partial, origin FROM pragma_index_list(?)", table)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var x index
		if err := rows.Scan(&x.name, &x.unique, &x.partial, &x.origin); err != nil {
			t.Fatal(err)
		}
		idx = append(idx, x)
	}
	rows.Close()
	var idxDesc []string
	for _, x := range idx {
		var cols []string
		r, err := db.Query("SELECT name FROM pragma_index_info(?) ORDER BY seqno", x.name)
		if err != nil {
			t.Fatal(err)
		}
		for r.Next() {
			var c string
			r.Scan(&c)
			cols = append(cols, c)
		}
		r.Close()
		idxDesc = append(idxDesc, fmt.Sprintf("index (%s) unique=%s partial=%s origin=%s", strings.Join(cols, ","), x.unique, x.partial, x.origin))
	}
	slices.Sort(idxDesc)
	return append(out, idxDesc...)
}

// 迁移 2 与 schema.sql 建出的 push_subscriptions 必须一模一样：改 schema.sql 时迁移要同步改。
func TestMigratedPushSchemaMatchesFresh(t *testing.T) {
	fresh := openTemp(t)
	path := filepath.Join(tempDir(t), "hub.db")
	writeV1(t, path)
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	a, b := tableSchema(t, fresh.db, "push_subscriptions"), tableSchema(t, migrated.db, "push_subscriptions")
	if !slices.Equal(a, b) {
		t.Fatalf("新库与迁移后的库不一致:\n新库:\n%s\n迁移:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	if !slices.ContainsFunc(a, func(s string) bool { return strings.Contains(s, " first_failed_at TEXT notnull=0 default=NULL") }) {
		t.Fatalf("应有可空的 first_failed_at 列:\n%s", strings.Join(a, "\n"))
	}
	if !slices.ContainsFunc(a, func(s string) bool { return strings.HasPrefix(s, "index (endpoint) unique=1") }) {
		t.Fatalf("应有 endpoint 唯一索引:\n%s", strings.Join(a, "\n"))
	}
}

// v2PopulateSQL 给 v2 库填各表的若干行：审批（已决定已送达）、question、webhook（带回调）、规则、审计、设置、机器、会话等。
const v2PopulateSQL = `
INSERT INTO schema_version(v) VALUES(2);
INSERT INTO settings VALUES('paused', '0'), ('password_hash', 'h$x');
INSERT INTO machines VALUES('m1', 'mac', 'sh1', '/Users/u', '0.1.4', '2026-10-20T00:00:00.000000000Z', '2026-10-30T00:00:00.000000000Z', 0),
  ('m2', 'old', 'sh2', '', '', '2026-10-21T00:00:00.000000000Z', NULL, 1);
INSERT INTO enroll_tokens VALUES('th', '2026-10-31T00:00:00.000000000Z', 1);
INSERT INTO client_sessions VALUES('ch', '2026-10-30T00:00:00.000000000Z', '2026-11-30T00:00:00.000000000Z');
INSERT INTO sessions VALUES('s1', 'm1', 'claude', 'cs1', '/p', 'p', '2026-10-30T00:00:00.000000000Z', '2026-10-30T00:01:00.000000000Z');
INSERT INTO requests VALUES('r1', 'm1', 's1', 'permission', 'Bash', '{"type":"shell","command":"git push","cwd":"/p"}', 'hash1', 'outward', '/p', '无规则命中',
  'approved', 'user', '好', 'learned-1', 't1', 'cs1', '', '', '2026-10-30T00:00:01.000000000Z', '2026-10-31T00:00:01.000000000Z',
  '2026-10-30T00:00:05.000000000Z', '2026-10-30T00:00:06.000000000Z');
INSERT INTO requests VALUES('q1', 'm1', 's1', 'question', '', '{"type":""}', 'hash2', '', '', '', 'recorded', '', '', '', 't2', 'cs1', '要不要继续？', '',
  '2026-10-30T00:00:02.000000000Z', NULL, NULL, NULL);
INSERT INTO requests VALUES('w1', 'webhook', NULL, 'webhook', '', '{"type":""}', '', '', '', '', 'pending', '', '', '', '', '', '部署', '{"url":"https://n8n.example/cb","token":"tok"}',
  '2026-10-30T00:00:03.000000000Z', '2026-10-31T00:00:03.000000000Z', NULL, NULL);
INSERT INTO rules VALUES('learned-1', 'id: learned-1', 'learned', 1, 3, '2026-10-30T00:00:07.000000000Z', '2026-10-30T00:00:05.000000000Z', 'r1');
INSERT INTO audit_events(ts, machine_id, actor, request_id, action, detail) VALUES('2026-10-30T00:00:05.000000000Z', 'm1', 'user', 'r1', 'allow', '{"note":"好"}');
INSERT INTO push_subscriptions VALUES('ps1', 'https://p.example/1', '{"p256dh":"x","auth":"y"}', 'Safari', '2026-10-29T00:00:00.000000000Z', NULL, 2, '2026-10-30T00:00:00.000000000Z');
`

// dumpTables 按 v2 的列（cols 为空时现查 pragma_table_info）逐行读出各表，按 rowid 排序，每行编成一个字符串。返回各表的列名，
// 迁移后用同样的列再读一次比对。
func dumpTables(t *testing.T, db *sql.DB, cols map[string][]string) (map[string][]string, map[string][]string) {
	t.Helper()
	tables := []string{"settings", "machines", "enroll_tokens", "client_sessions", "sessions", "requests", "rules", "audit_events", "push_subscriptions"}
	if cols == nil {
		cols = map[string][]string{}
		for _, table := range tables {
			rows, err := db.Query("SELECT name FROM pragma_table_info(?) ORDER BY cid", table)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var c string
				if err := rows.Scan(&c); err != nil {
					t.Fatal(err)
				}
				cols[table] = append(cols[table], c)
			}
			rows.Close()
		}
	}
	out := map[string][]string{}
	for _, table := range tables {
		quoted := make([]string, len(cols[table]))
		for i, c := range cols[table] {
			quoted[i] = `"` + c + `"`
		}
		rows, err := db.Query("SELECT " + strings.Join(quoted, ", ") + " FROM " + table + " ORDER BY rowid")
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			vals := make([]any, len(cols[table]))
			ptrs := make([]any, len(vals))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			out[table] = append(out[table], fmt.Sprintf("%q", vals))
		}
		rows.Close()
	}
	return out, cols
}

// v2 库升到 v3：各表的行逐字段保留，requests 新加的 ask、answers 两列为空串，版本为 3；读回的请求没有题目与答案。
func TestMigrateV2ToV3KeepsData(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	writeRaw(t, path, schemaV2(t)+v2PopulateSQL)
	raw := openRaw(t, path)
	before, cols := dumpTables(t, raw, nil)
	raw.Close()
	if len(before["requests"]) != 3 || len(before["machines"]) != 2 || len(before["rules"]) != 1 || len(before["audit_events"]) != 1 {
		t.Fatalf("v2 库没造好: %v", before)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("打开 v2 库应升级: %v", err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRow("SELECT v FROM schema_version").Scan(&v); err != nil || v != SchemaVersion {
		t.Fatalf("版本 = %d, %v", v, err)
	}
	after, _ := dumpTables(t, s.db, cols)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("迁移改动了旧数据:\n前: %v\n后: %v", before, after)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM requests WHERE ask = '' AND answers = ''").Scan(&n); err != nil || n != 3 {
		t.Fatalf("新列应默认空串: %d, %v", n, err)
	}
	for _, id := range []string{"r1", "q1", "w1"} {
		r, err := s.GetRequest(id)
		if err != nil || r.Ask != nil || r.Answers != nil {
			t.Fatalf("旧请求 %s 读回: %+v, %v", id, r, err)
		}
	}
	if r, _ := s.GetRequest("r1"); r.State != "approved" || r.Payload.Command != "git push" || r.DeliveredAt == nil || r.RuleID != "learned-1" {
		t.Fatalf("旧审批读回: %+v", r)
	}
	// 迁移后能照常写入答题请求
	ask := &proto.Ask{Questions: []proto.Question{{Question: "用哪个？", Options: []proto.Option{{Label: "A"}}}}}
	if err := s.InsertRequest(Request{ID: "a1", MachineID: "m1", Kind: "ask", ToolName: "AskUserQuestion", Ask: ask, State: "pending"}); err != nil {
		t.Fatal(err)
	}
	if r, err := s.GetRequest("a1"); err != nil || !reflect.DeepEqual(r.Ask, ask) {
		t.Fatalf("迁移后写入的答题请求: %+v, %v", r, err)
	}
}

// 迁移 3 与 schema.sql 建出的 requests 必须一模一样（列序、类型、默认值、索引）：改 schema.sql 时迁移要同步改。
func TestMigratedRequestsSchemaMatchesFresh(t *testing.T) {
	fresh := openTemp(t)
	path := filepath.Join(tempDir(t), "hub.db")
	writeRaw(t, path, schemaV2(t)+"INSERT INTO schema_version(v) VALUES(2);")
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	a, b := tableSchema(t, fresh.db, "requests"), tableSchema(t, migrated.db, "requests")
	if !slices.Equal(a, b) {
		t.Fatalf("新库与迁移后的库不一致:\n新库:\n%s\n迁移:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	for _, col := range []string{" ask TEXT notnull=1 default='' ", " answers TEXT notnull=1 default='' "} {
		if !slices.ContainsFunc(a, func(s string) bool { return strings.Contains(s, col) }) {
			t.Fatalf("应有列%s:\n%s", col, strings.Join(a, "\n"))
		}
	}
}

// 旧库（v2 固定快照）里已有的订阅升到最新后 lang 是 zh：作者手机那条不受影响。
func TestMigrateKeepsOldSubscriptionsChinese(t *testing.T) {
	path := filepath.Join(tempDir(t), "hub.db")
	writeRaw(t, path, schemaV2(t)+"INSERT INTO schema_version(v) VALUES(2);\n"+
		`INSERT INTO push_subscriptions(id, endpoint, keys, user_agent, created_at) VALUES('s1','https://p.example/1','{"p256dh":"a","auth":"b"}','ua','2026-01-01T00:00:00Z');`)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	sub, err := st.GetPushSubscription("https://p.example/1")
	if err != nil {
		t.Fatal(err)
	}
	if sub.Lang != "zh" {
		t.Fatalf("旧订阅升级后 lang = %q，想要 zh", sub.Lang)
	}
}
