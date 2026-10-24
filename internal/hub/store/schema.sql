-- 改动 schema 必须提高 store.go 的 schemaVersion 并写迁移（版本一致时不会重新执行建表）。
-- Railse OPC hub 数据库，版本 2。时间列一律定宽 UTC 文本（纳秒 9 位、以 Z 结尾，见 store.go 的 timeLayout），按字典序比较即按时间先后。
CREATE TABLE IF NOT EXISTS schema_version (v INTEGER NOT NULL);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS machines (
  id            TEXT PRIMARY KEY,
  name          TEXT NOT NULL,
  secret_hash   TEXT NOT NULL,
  home_dir      TEXT NOT NULL DEFAULT '',
  agent_version TEXT NOT NULL DEFAULT '',
  enrolled_at   TEXT NOT NULL,
  last_seen     TEXT,
  revoked       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS enroll_tokens (
  token_hash TEXT PRIMARY KEY,
  expires_at TEXT NOT NULL,
  used       INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS client_sessions (
  token_hash TEXT PRIMARY KEY,
  created_at TEXT NOT NULL,
  expires_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  id             TEXT PRIMARY KEY,
  machine_id     TEXT NOT NULL REFERENCES machines(id),
  tool           TEXT NOT NULL,
  cli_session_id TEXT NOT NULL,
  cwd            TEXT NOT NULL DEFAULT '',
  project        TEXT NOT NULL DEFAULT '',
  started_at     TEXT NOT NULL,
  last_event_at  TEXT NOT NULL,
  UNIQUE (machine_id, tool, cli_session_id)
);

CREATE TABLE IF NOT EXISTS requests (
  id            TEXT PRIMARY KEY,
  machine_id    TEXT NOT NULL,
  session_id    TEXT,
  kind          TEXT NOT NULL,            -- permission、question、done、webhook
  tool_name     TEXT NOT NULL DEFAULT '',
  payload       TEXT NOT NULL,            -- JSON，已脱敏
  payload_hash  TEXT NOT NULL DEFAULT '',
  risk          TEXT NOT NULL DEFAULT '',
  project_root  TEXT NOT NULL DEFAULT '',
  engine_reason TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL,            -- pending、auto_allowed、auto_denied、approved、denied、passthrough、expired、cancelled、recorded（question、done）
  decided_by    TEXT NOT NULL DEFAULT '',
  decision_note TEXT NOT NULL DEFAULT '',
  rule_id       TEXT NOT NULL DEFAULT '',
  turn_id       TEXT NOT NULL DEFAULT '',
  cli_session_id TEXT NOT NULL DEFAULT '',
  title         TEXT NOT NULL DEFAULT '', -- webhook 用
  callback      TEXT NOT NULL DEFAULT '', -- webhook 用，JSON
  created_at    TEXT NOT NULL,
  expires_at    TEXT,
  decided_at    TEXT,
  delivered_at  TEXT
);
CREATE INDEX IF NOT EXISTS requests_state_created ON requests (state, created_at);
CREATE INDEX IF NOT EXISTS requests_machine_created ON requests (machine_id, created_at);

CREATE TABLE IF NOT EXISTS rules (
  id           TEXT PRIMARY KEY,
  yaml         TEXT NOT NULL,            -- 单条规则的 YAML
  source       TEXT NOT NULL,
  enabled      INTEGER NOT NULL DEFAULT 1,
  hit_count    INTEGER NOT NULL DEFAULT 0,
  last_hit_at  TEXT,
  created_at   TEXT NOT NULL,
  created_from TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS audit_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  ts         TEXT NOT NULL,
  machine_id TEXT NOT NULL DEFAULT '',
  actor      TEXT NOT NULL,              -- user、rule:<id>、system、agent
  request_id TEXT NOT NULL DEFAULT '',
  action     TEXT NOT NULL,
  detail     TEXT NOT NULL DEFAULT ''    -- JSON
);
CREATE INDEX IF NOT EXISTS audit_ts ON audit_events (ts);
CREATE INDEX IF NOT EXISTS audit_request ON audit_events (request_id, id);

CREATE TABLE IF NOT EXISTS push_subscriptions (
  id         TEXT PRIMARY KEY,
  endpoint   TEXT NOT NULL UNIQUE,
  keys       TEXT NOT NULL,              -- JSON {"p256dh":"…","auth":"…"}，明文（设计 5.3 例外）
  user_agent TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  last_ok_at TEXT,
  failures   INTEGER NOT NULL DEFAULT 0, -- 连续失败次数，成功与重新订阅时归零
  first_failed_at TEXT                   -- 这一轮连续失败的第一次失败时刻，成功与重新订阅时清空
);
