import { Link } from 'react-router'
import { explainRequest, cliLabel, machineLabel } from '../explain'
import { absoluteTime, clip, isRedacted, kindLabel, payloadSummary, relativeTime, riskLabel, riskTone } from '../format'
import type { RequestView } from '../types'

/** 卡片摘要至多这么多字符（CSS 再按行数收起）：超长命令不撑出一大块 DOM。 */
const summaryMax = 300

/**
 * 收件箱里的一条，三行：做什么（不带 CLI 名）+ 风险或种类标签 + 相对时间；等宽的命令、路径或问题；
 * CLI · 机器 · 项目（webhook 写 webhook；cli 为空时不写 Agent）。整张卡链接到详情。
 */
export function RequestCard({ request: r }: { request: RequestView }) {
  const what = explainRequest(r).what
  const where =
    r.kind === 'webhook'
      ? 'webhook'
      : [r.cli && cliLabel(r.cli), machineLabel(r.machine_name), r.project].filter(Boolean).join(' · ')
  const main = r.kind === 'permission' ? payloadSummary(r.payload) : r.title
  return (
    <Link className="card" to={`/r/${encodeURIComponent(r.id)}`}>
      <div className="row">
        <span className="what">{what}</span>
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
      <div className="where">{where}</div>
    </Link>
  )
}
