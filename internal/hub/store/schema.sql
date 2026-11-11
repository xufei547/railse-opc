-- 改动 schema 必须提高 store.go 的 SchemaVersion 并写迁移（版本一致时不会重新执行建表）。
-- Railse OPC hub 数据库，版本 7。时间列一律定宽 UTC 文本（纳秒 9 位、以 Z 结尾，见 store.go 的 timeLayout），按字典序比较即按时间先后。
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
  role           TEXT NOT NULL DEFAULT '', -- 会话生效的角色名（v7），每次上报时更新为最新值；空串为未分配
  UNIQUE (machine_id, tool, cli_session_id)
);

CREATE TABLE IF NOT EXISTS requests (
  id            TEXT PRIMARY KEY,
  machine_id    TEXT NOT NULL,
  session_id    TEXT,
  kind          TEXT NOT NULL,            -- permission、ask、question、done、webhook
  tool_name     TEXT NOT NULL DEFAULT '',
  payload       TEXT NOT NULL,            -- JSON，已脱敏；ask 为 {}
  payload_hash  TEXT NOT NULL DEFAULT '', -- 去重用：载荷的哈希；ask 为题目的哈希
  risk          TEXT NOT NULL DEFAULT '',
  project_root  TEXT NOT NULL DEFAULT '',
  engine_reason TEXT NOT NULL DEFAULT '',
  state         TEXT NOT NULL,            -- pending、auto_allowed、auto_denied、approved、denied、answered（ask）、passthrough、expired、cancelled、recorded（question、done）
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
  delivered_at  TEXT,
  ask           TEXT NOT NULL DEFAULT '', -- ask 用，JSON（proto.Ask，守护进程已脱敏），其余种类为空串
  answers       TEXT NOT NULL DEFAULT '', -- answered 的 ask 用，JSON 对象：题面 → 答案（原文，重连补发要原样下发），其余为空串
  trigger       TEXT NOT NULL DEFAULT '', -- 引擎的触发点（哪一段、什么性质），JSON {"segment","why"}，段已脱敏；没有为空串
  role          TEXT NOT NULL DEFAULT '', -- 会话生效的角色名（v7），空串为未分配
  beyond_role   INTEGER NOT NULL DEFAULT 0 -- 1 表示超出了角色的风险上限（v7）：不能记住、不能限时放行
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
  first_failed_at TEXT,                  -- 这一轮连续失败的第一次失败时刻，成功与重新订阅时清空
  lang       TEXT NOT NULL DEFAULT 'zh'  -- 推送文案语言：zh、en（v4）
);

-- 限时放行（v6）：守护进程是执行者，hub 存副本给手机看、建、撤。撤销与到期只标 revoked_at，不删行。
CREATE TABLE IF NOT EXISTS trusts (
  id             TEXT PRIMARY KEY,
  machine_id     TEXT NOT NULL REFERENCES machines(id),
  scope          TEXT NOT NULL,             -- session、project
  tool           TEXT NOT NULL DEFAULT '',
  cli_session_id TEXT NOT NULL DEFAULT '',
  project_root   TEXT NOT NULL DEFAULT '',
  until_at       TEXT NOT NULL,
  created_at     TEXT NOT NULL,
  created_by     TEXT NOT NULL,             -- phone、cli、env
  note           TEXT NOT NULL DEFAULT '',
  revoked_at     TEXT,
  revoke_reason  TEXT NOT NULL DEFAULT ''   -- user、expired、agent
);
CREATE INDEX IF NOT EXISTS trusts_machine_until ON trusts (machine_id, until_at);

-- 角色摘要（v7）：守护进程 roles.yaml 的展示用副本，hub 不拿它判定。收到 hello 或 roles 帧时整份替换；机器没声明 role 能力时清空。
CREATE TABLE IF NOT EXISTS machine_roles (
  machine_id     TEXT NOT NULL REFERENCES machines(id),
  position       INTEGER NOT NULL,             -- 文件里的先后，从 0 起
  name           TEXT NOT NULL,
  note           TEXT NOT NULL DEFAULT '',
  ceiling        TEXT NOT NULL DEFAULT '',     -- policy.Risk 的名字，空为不设
  forbid_count   INTEGER NOT NULL DEFAULT 0,
  trust_max_mins INTEGER NOT NULL DEFAULT 0,   -- 0 为不设，-1 为不允许限时放行
  dirs           TEXT NOT NULL DEFAULT '[]',   -- JSON 字符串数组
  PRIMARY KEY (machine_id, name)
);
-- 总台（v7）的查询用的索引：24 小时窗口（覆盖索引）、每个角色最近几条、项目最近一条、窗口内的会话。见 desk.go。
CREATE INDEX IF NOT EXISTS requests_window ON requests (created_at, kind, state, machine_id, role);
CREATE INDEX IF NOT EXISTS requests_role_recent ON requests (machine_id, role, kind, created_at, id);
CREATE INDEX IF NOT EXISTS requests_project_recent ON requests (machine_id, project_root, created_at, id);
CREATE INDEX IF NOT EXISTS sessions_recent ON sessions (last_event_at, machine_id, role);
