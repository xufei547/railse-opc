import type { ReactNode } from 'react'
import {
  absoluteTime,
  actorLabel,
  auditDetail,
  auditLabel,
  decidedByLabel,
  deliveryLabel,
  engineReasonLabel,
  relativeTime,
  riskLabel,
  riskTone,
  stateLabel,
} from '../format'
import type { AuditEvent, RequestView } from '../types'

function Row({ label, children, mono }: { label: string; children: ReactNode; mono?: boolean }) {
  return (
    <>
      <dt>{label}</dt>
      <dd className={mono ? 'mono' : undefined}>{children}</dd>
    </>
  )
}

function When({ iso }: { iso: string }) {
  return (
    <time dateTime={iso}>
      {absoluteTime(iso)}
      <span className="sub">（{relativeTime(iso)}）</span>
    </time>
  )
}

/** 详情里的属性表：目录（shell）、机器、项目、工具、风险、引擎理由、状态、依据、送达、备注、时间。 */
export function RequestAttrs({ request: r, audit }: { request: RequestView; audit: AuditEvent[] }) {
  // 依据：用户的决定优先于学到的规则（decidedByLabel），pending 时为空
  const basis = r.state === 'pending' ? '' : decidedByLabel(r)
  const delivery = deliveryLabel(r, audit.some((e) => e.action === 'undeliverable'))
  return (
    <dl className="kv">
      {r.payload.type === 'shell' && r.payload.cwd && (
        <Row label="目录" mono>
          {r.payload.cwd}
        </Row>
      )}
      {r.machine_name && <Row label="机器">{r.machine_name}</Row>}
      {r.project_root && (
        <Row label="项目" mono>
          {r.project_root}
        </Row>
      )}
      {r.tool_name && r.kind !== 'webhook' && <Row label="工具">{r.tool_name}</Row>}
      {r.kind === 'permission' && (
        <Row label="风险">
          <span className={`tag ${riskTone(r.risk)}`}>{riskLabel(r.risk)}</span>
        </Row>
      )}
      {r.engine_reason && <Row label="引擎">{engineReasonLabel(r.engine_reason)}</Row>}
      <Row label="状态">{stateLabel(r.state)}</Row>
      {basis && basis !== stateLabel(r.state) && <Row label="依据">{basis}</Row>}
      {delivery && <Row label="送达">{delivery}</Row>}
      {r.decision_note && <Row label="备注">{r.decision_note}</Row>}
      <Row label="创建">
        <When iso={r.created_at} />
      </Row>
      {r.decided_at && (
        <Row label="决定">
          <When iso={r.decided_at} />
        </Row>
      )}
      {r.expires_at && r.state === 'pending' && <Row label="过期">{absoluteTime(r.expires_at)}</Row>}
    </dl>
  )
}

/** 这条请求的审计（hub 给的是倒序，至多 200 条）。没有时不渲染。 */
export function AuditList({ audit }: { audit: AuditEvent[] }) {
  if (audit.length === 0) return null
  return (
    <>
      <div className="group-title">审计</div>
      {audit.map((e) => {
        const d = auditDetail(e.detail)
        return (
          <div className="list-item" key={e.id}>
            <div className="grow">
              <div>
                {auditLabel(e.action)} <span className="sub">{actorLabel(e.actor)}</span>
              </div>
              {d && <div className="sub mono">{d}</div>}
            </div>
            <time className="sub nowrap" dateTime={e.ts} title={absoluteTime(e.ts)}>
              {relativeTime(e.ts)}
            </time>
          </div>
        )
      })}
    </>
  )
}

/** 审批页底部默认折叠的“详情”：字段表与审计（对“该不该批”没用，展开时才看）。 */
export function RequestDetails({ request, audit }: { request: RequestView; audit: AuditEvent[] }) {
  return (
    <details className="more">
      <summary>详情</summary>
      <RequestAttrs request={request} audit={audit} />
      <AuditList audit={audit} />
    </details>
  )
}
