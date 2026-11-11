// hub 接口的数据形状，以 internal/hub 的处理函数与 docs/hub.md 为准。

export type Kind = 'permission' | 'question' | 'done' | 'webhook' | 'ask'
export type RequestState =
  | 'pending' | 'approved' | 'denied' | 'cancelled' | 'expired'
  | 'auto_allowed' | 'auto_denied' | 'passthrough' | 'recorded' | 'answered'

/** 提问的一个选项（已脱敏）。 */
export interface Option {
  label: string
  description?: string
}

/**
 * 提问（AskUserQuestion）的一道题（已脱敏）。question 也是答案的键：作答时逐字节原样带回，不 trim、不改写。
 * hub 给的 options 总是数组（可为空），header、description 可省，multiSelect 只在为 true 时出现；网页仍容忍 options 为 null 或缺失。
 */
export interface Question {
  question: string
  header?: string
  options?: Option[] | null
  multiSelect?: boolean
}

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
  /** shell：agent 自己写的一句话说明（Claude Code 的 tool_input.description），原文不翻译；没有时没有这个键。 */
  description?: string
}

/** 引擎定级时起决定作用的那一处：哪一段（已脱敏）、什么性质（固定中文短语，英文见 format.ts 的 triggerWhysEn）。 */
export interface Trigger {
  segment?: string
  why: string
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
  /** 引擎的触发点；read、write_in_project 与老 hub、老守护进程送来的没有这个键。 */
  trigger?: Trigger
  /** 提问（kind 为 ask）的题目；别的种类没有这个键。 */
  ask?: { questions: Question[] }
  /** 提问作答后的答案：题面 → 用户答的原文；没答时没有这个键。 */
  answers?: Record<string, string>
  /** 批准时能不能顺带限时放行：审批（kind permission）、没有超出角色的边界、机器在线且守护进程支持。老 hub 没有这个键（不显示开关）。 */
  can_trust?: boolean
  /** 会话生效的角色名（0.3 起），空串为未分配；老 hub 没有这个键。 */
  role?: string
  /** 超出了角色的风险上限（0.3 起）：只能单次批准，不能记住，也不能限时放行。老 hub 没有这个键。 */
  beyond_role?: boolean
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
  /** 生效中的限时放行条数；老 hub 没有这个键（设置页不显示放行一节）。 */
  trusts?: number
}

export type TrustScope = 'session' | 'project'

/**
 * GET /api/trusts 的一条：生效中的限时放行，按到期先后排。hub 对空的 tool、cli_session_id、project_root、note 省略键；
 * 时间是 RFC 3339（小数位不定宽，比较先后要先解析）。
 */
export interface TrustView {
  id: string
  machine_id?: string
  scope: TrustScope | string
  /** session 范围的 CLI（claude）。 */
  tool?: string
  cli_session_id?: string
  project_root?: string
  until: string
  created_at: string
  /** 谁建的：phone（手机批准时）、cli（railse trust）、env（RAILSE_TRUST 启动命令）。 */
  created_by: string
  note?: string
  machine_name: string
  /** 项目根的末段；没有项目根为空串。 */
  project: string
  /** hub 算的剩余秒数（拉取那一刻）。 */
  remaining_seconds: number
}

export interface Inbox {
  /** 全部待决（待审批与待答的提问），hub 按创建时间倒序给出。 */
  pending: RequestView[]
  /** 最近 24 小时还在等回答的 question，按创建时间倒序。 */
  questions: RequestView[]
  /** pending 被截断（只给了最新的 500 条），还有更早的；老 hub 没有这个键。 */
  pending_truncated?: boolean
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
  /** 批准时顺带的限时放行；拒绝不带。 */
  trust?: { scope: TrustScope; minutes: number }
}

/** POST /api/requests/{id}/answer 的请求体：answers 的键是题面原样；交回终端只带 passthrough。 */
export interface AnswerBody {
  answers?: Record<string, string>
  passthrough?: boolean
}

export interface VapidInfo {
  enabled: boolean
  public_key: string
  reason: string
}

/** 总台一行里的一条最近动作（permission）。summary 是收件箱卡片那一行摘要（已脱敏）；at 是创建时间（定宽 UTC）。 */
export interface DeskRecent {
  id: string
  tool_name: string
  summary: string
  risk: string
  state: RequestState
  /** 决定来源（同请求视图的 decided_by）；老 hub 没有这个键。 */
  decided_by?: string
  beyond_role: boolean
  at: string
}

/**
 * GET /api/desk 的一行：一个角色（有 name）或一台机器的“未分配”（没有 name 等角色字段）。
 * trust_max_mins：0 为不设，-1 为不允许限时放行。denied、auto_allowed、sessions 只数 window_hours 之内的。
 */
export interface DeskRow {
  machine_id: string
  machine_name: string
  online: boolean
  name?: string
  note?: string
  ceiling?: string
  forbid_count?: number
  trust_max_mins?: number
  dirs?: string[]
  waiting: number
  trusts_active: number
  denied: number
  auto_allowed: number
  sessions: number
  recent: DeskRecent[]
}

/** GET /api/desk：角色行按机器、文件里的先后排；未分配每台机器至多一行。 */
export interface Desk {
  window_hours: number
  roles: DeskRow[]
  unassigned: DeskRow[]
}
