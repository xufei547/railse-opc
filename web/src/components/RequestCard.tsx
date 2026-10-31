import { Link } from 'react-router'
import { askQuestions, questionOptions } from '../ask'
import { explainRequest, cliLabel, machineLabel } from '../explain'
import { absoluteTime, clip, isRedacted, kindLabel, payloadSummary, relativeTime, riskLabel, riskTone } from '../format'
import { t } from '../i18n'
import type { RequestView } from '../types'

/** 卡片摘要至多这么多字符（CSS 再按行数收起）：超长命令不撑出一大块 DOM。 */
const summaryMax = 300

/** 卡片第二行：审批是命令或路径，提问是第一题的选项（用“ / ”连起来，没有选项为空），其余是标题（问题文本、webhook 标题）。 */
function cardMain(r: RequestView): string {
  if (r.kind === 'permission') return payloadSummary(r.payload)
  if (r.kind === 'ask') {
    const first = askQuestions(r)[0]
    return first ? questionOptions(first).map((o) => o.label).join(' / ') : ''
  }
  return r.title
}

/**
 * 收件箱里的一条，三行：做什么（不带 CLI 名）+ 风险或种类标签 + 相对时间；等宽的命令、路径、问题或提问的选项；
 * CLI · 机器 · 项目（webhook 写 webhook；cli 为空时不写 Agent）。整张卡链接到详情。
 */
export function RequestCard({ request: r }: { request: RequestView }) {
  const what = explainRequest(r).what
  const where =
    r.kind === 'webhook'
      ? 'webhook'
      : [r.cli && cliLabel(r.cli), machineLabel(r.machine_name), r.project].filter(Boolean).join(' · ')
  const main = cardMain(r)
  return (
    <Link className="card" to={`/r/${encodeURIComponent(r.id)}`}>
      <div className="row">
        <span className="what">{what}</span>
        {r.kind === 'permission' ? (
          <span className={`tag ${riskTone(r.risk)}`}>{riskLabel(r.risk)}</span>
        ) : (
          <span className="tag plain">{kindLabel(r.kind)}</span>
        )}
        {isRedacted(r) && <span className="tag high">{t('含隐藏部分')}</span>}
        <time className="time" dateTime={r.created_at} title={absoluteTime(r.created_at)}>
          {relativeTime(r.created_at)}
        </time>
      </div>
      <pre className="cmd">{clip(main, summaryMax)}</pre>
      <div className="where">{where}</div>
    </Link>
  )
}
