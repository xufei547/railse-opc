// hub 接口的数据形状，以 internal/hub 的处理函数与 docs/hub.md 为准。

export type Kind = 'permission' | 'question' | 'done' | 'webhook'
export type RequestState =
  | 'pending' | 'approved' | 'denied' | 'cancelled' | 'expired'
  | 'auto_allowed' | 'auto_denied' | 'passthrough' | 'recorded'

/** 已脱敏的载荷（policy.Payload）。按 type 只有部分字段有值。 */
export interface Payload {
  type: 'shell' | 'file_write' | 'network' | 'mcp' | string
  command?: string
  cwd?: string
  path?: string
  diff_preview?: string
  server?: string
  tool?: string
  args?: unknown
  url?: string
  method?: string
}

/** 请求视图：/api/inbox、/api/requests、/api/requests/{id}、decide 的返回与 /ws/client 推送的请求。 */
export interface RequestView {
  id: string
  machine_id: string
  session_id: string
  /** 请求所属会话的 CLI（sessions.tool 原值：claude、codex、gemini）；没有会话或查不到为空串。 */
  cli: string
  kind: Kind
  tool_name: string
  payload: Payload
  risk: string
  project_root: string
  engine_reason: string
  state: RequestState
  decided_by: string
  decision_note: string
  rule_id: string
  turn_id: string
  cli_session_id: string
  title: string
  created_at: string
  expires_at: string | null
  decided_at: string | null
  delivered_at: string | null
  machine_name: string
  project: string
  /** 载荷里有被脱敏抹掉的部分：展示的不是原文，必须醒目告警。 */
  has_redacted: boolean
}

export interface AuditEvent {
  id: number
  ts: string
  machine_id: string
  actor: string
  request_id: string
  action: string
  detail: string
}

export interface Rule {
  id: string
  source: 'builtin' | 'user' | 'learned' | string
  enabled: boolean
  /** 能自动放行 outward、destructive 的规则，网页标红。 */
  elevated: boolean
  hit_count: number
  last_hit_at: string | null
  created_at: string
  created_from: string
  yaml: string
}

export interface Machine {
  id: string
  name: string
  online: boolean
  last_seen: string | null
  agent_version: string
  enrolled_at: string
  revoked: boolean
}

export interface Status {
  paused: boolean
  pending: number
  rules_version: number
  machines: { id: string; name: string; online: boolean; last_seen: string | null; revoked: boolean }[]
  uptime: string
  /** hub 是否配了 --public-url（推送启用）。 */
  push_enabled: boolean
  push_subscriptions: number
}

export interface Inbox {
  /** 全部待审批，hub 按创建时间倒序给出。 */
  pending: RequestView[]
  /** 最近 24 小时还在等回答的 question，按创建时间倒序。 */
  questions: RequestView[]
}

/** POST /api/requests/{id}/propose 的返回：“记住”会生成的规则草案。 */
export interface Draft {
  rule_id: string
  /** 草案实际的范围（请求里没带 scope 时由 hub 选定）。 */
  scope: 'project' | 'global'
  yaml: string
  summary: { action: 'allow' | 'deny'; payload: string; anchor: string }
}

export interface DecideBody {
  action: 'allow' | 'deny'
  note?: string
  remember?: boolean
  scope?: 'project' | 'global'
}

export interface VapidInfo {
  enabled: boolean
  public_key: string
  reason: string
}
