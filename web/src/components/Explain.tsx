import { explainRequest } from '../explain'
import { absoluteTime, deliveryLabel, decidedByLabel, isRedacted } from '../format'
import { historyTag } from '../history'
import type { AuditEvent, RequestView } from '../types'
import { RedactedWarning, RequestContent } from './Payload'

/**
 * 审批页最上面：含隐藏部分时先是醒目告警（说明卡那一句也是从脱敏后的内容推出来的），
 * 然后是说明卡（谁想做什么、在哪、为什么要你决定），原样的命令或路径，最后是批了、拒了、记住会怎样；
 * 为什么与后续只在还等你决定时有（explain.ts 按状态给空），这里不再自己判断。
 */
export function RequestSummary({ request: r }: { request: RequestView }) {
  const e = explainRequest(r)
  return (
    <>
      {isRedacted(r) && <RedactedWarning request={r} />}
      <section className="explain" aria-label="这条请求要做什么">
        <p className="headline">{e.headline}</p>
        {e.where && <p className="where">{e.where}</p>}
        {e.why && <p className="why">{e.why}</p>}
      </section>
      <RequestContent request={r} />
      {e.next.length > 0 && (
        <ul className="next">
          {e.next.map((line) => (
            <li key={line}>{line}</li>
          ))}
        </ul>
      )}
    </>
  )
}

/** 已处理的审批、webhook 在命令下面的一行：状态、依据、决定时间、送达，用“ · ”连起来。 */
export function StatusLine({ request: r, audit }: { request: RequestView; audit: AuditEvent[] }) {
  if ((r.kind !== 'permission' && r.kind !== 'webhook') || r.state === 'pending') return null
  const tag = historyTag(r)
  const basis = decidedByLabel(r)
  const delivery = deliveryLabel(r, audit.some((a) => a.action === 'undeliverable'))
  const parts = [
    basis && basis !== tag.label ? <span key="basis">{basis}</span> : null,
    r.decided_at ? (
      <time key="time" dateTime={r.decided_at}>
        {absoluteTime(r.decided_at)}
      </time>
    ) : null,
    delivery ? <span key="delivery">{delivery}</span> : null,
  ].filter(Boolean)
  return (
    <p className="status-line">
      <span className={`tag ${tag.tone}`}>{tag.label}</span>
      {parts.map((p, i) => (
        <span key={i} className="status-part">
          {' · '}
          {p}
        </span>
      ))}
    </p>
  )
}
