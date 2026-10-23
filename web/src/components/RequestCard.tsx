import { Link } from 'react-router'
import { absoluteTime, clip, isRedacted, kindLabel, payloadSummary, relativeTime, riskLabel, riskTone } from '../format'
import type { RequestView } from '../types'

/** 卡片摘要至多这么多字符（CSS 再按行数收起）：超长命令不撑出一大块 DOM。 */
const summaryMax = 300

/** 收件箱里的一条：链接到详情；机器 · 项目（webhook 显示 webhook）、风险或种类、相对时间、等宽的摘要。 */
export function RequestCard({ request: r }: { request: RequestView }) {
  const where = r.kind === 'webhook' ? 'webhook' : [r.machine_name, r.project].filter(Boolean).join(' · ')
  const main = r.kind === 'permission' ? payloadSummary(r.payload) : r.title
  return (
    <Link className="card" to={`/r/${encodeURIComponent(r.id)}`}>
      <div className="row">
        <span className="where">{where}</span>
        {r.kind === 'permission' ? (
          <span className={`tag ${riskTone(r.risk)}`}>{riskLabel(r.risk)}</span>
        ) : (
          <span className="tag plain">{kindLabel(r.kind)}</span>
        )}
        {isRedacted(r) && <span className="tag high">含隐藏部分</span>}
        <time className="time" dateTime={r.created_at} title={absoluteTime(r.created_at)}>
          {relativeTime(r.created_at)}
        </time>
      </div>
      <pre className="cmd">{clip(main, summaryMax)}</pre>
    </Link>
  )
}
