import { useEffect, useRef, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { hub } from '../api'
import { coalesce } from '../coalesce'
import { Banner } from '../components/Banner'
import { absoluteTime, clip, isRedacted, outcomeText, payloadSummary, relativeTime } from '../format'
import { historyFilters, historyTag, maxLimit, moreState, pageSize, parseFilter } from '../history'
import { failure } from '../manage'
import { useStore } from '../StoreProvider'
import type { RequestView } from '../types'

/** 卡片摘要至多这么多字符（CSS 再按行数收起）：500 条长命令也不撑出一大块 DOM。 */
const summaryMax = 300

/** 历史里的一条：链接到详情；机器 · 项目、状态（或种类）、相对时间、摘要与一句人话的结果（outcomeText，与审批页状态行同一个函数）。 */
export function HistoryCard({ request: r }: { request: RequestView }) {
  const where = r.kind === 'webhook' ? 'webhook' : [r.machine_name, r.project].filter(Boolean).join(' · ')
  const main = r.kind === 'permission' ? payloadSummary(r.payload) : r.title
  const tag = historyTag(r)
  const outcome = outcomeText(r)
  return (
    <Link className="card" to={`/r/${encodeURIComponent(r.id)}`}>
      <div className="row">
        <span className="where">{where}</span>
        <span className={`tag ${tag.tone}`}>{tag.label}</span>
        {isRedacted(r) && <span className="tag high">含隐藏部分</span>}
        <time className="time" dateTime={r.created_at} title={absoluteTime(r.created_at)}>
          {relativeTime(r.created_at)}
        </time>
      </div>
      <pre className="cmd">{clip(main, summaryMax)}</pre>
      {outcome && <div className="sub line">{outcome}</div>}
    </Link>
  )
}

interface Loaded {
  filter: string
  limit: number
  items: RequestView[]
}

export function History() {
  const { state } = useStore()
  const [params, setParams] = useSearchParams()
  // 筛选放在地址栏（?state=）：从详情页返回时还是原来的筛选
  const filter = parseFilter(params.get('state'))
  const [limit, setLimit] = useState(pageSize)
  const [data, setData] = useState<Loaded | null>(null)
  const [error, setError] = useState<string | null>(null)
  const want = useRef({ filter, limit })

  // 单飞、串行：一阵帧来时只多拉一次；拉的途中换了筛选或条数，这次的结果丢掉（结束后会按新的再拉一次）
  const [load] = useState(() =>
    coalesce(async () => {
      const w = want.current
      const stale = () => want.current.filter !== w.filter || want.current.limit !== w.limit
      try {
        const items = await hub.requests(w.filter, w.limit)
        if (stale()) return
        setData({ ...w, items })
        setError(null)
      } catch (e) {
        if (stale()) return
        const m = failure(e, '加载失败')
        if (m) setError(m)
      }
    }),
  )
  // 换筛选、加载更多、来了请求的帧（tick）、每次全量重拉（loads：重连、回到前台、网络恢复，断线期间漏掉的变化）都重拉
  useEffect(() => {
    want.current = { filter, limit }
    void load()
  }, [load, filter, limit, state.tick, state.loads])

  function choose(v: string) {
    setParams(v ? { state: v } : {}, { replace: true })
    setLimit(pageSize)
  }

  // 换了筛选、新的还没拉回来时不显示旧筛选的列表；加载更多时先留着已有的
  const shown = data && data.filter === filter ? data : null
  const more = shown ? moreState(shown.items.length, shown.limit, limit) : 'none'

  return (
    <>
      <header className="topbar">
        <h1>历史</h1>
      </header>
      <div className="page">
        <label className="field">
          <span>筛选</span>
          <select className="input" value={filter} onChange={(e) => choose(e.target.value)}>
            {historyFilters.map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </select>
        </label>
        {error && (
          <Banner tone="warn">
            {shown ? `刷新失败：${error}` : error}
            <button type="button" className="btn" onClick={() => void load()}>
              重试
            </button>
          </Banner>
        )}
        {shown?.items.map((r) => (
          <HistoryCard key={r.id} request={r} />
        ))}
        {shown && shown.items.length === 0 && <div className="empty">没有记录</div>}
        {more === 'more' && (
          <button type="button" className="btn block" onClick={() => setLimit(maxLimit)}>
            加载更多
          </button>
        )}
        {more === 'loading' && !error && (
          <button type="button" className="btn block" disabled>
            正在加载…
          </button>
        )}
        {more === 'capped' && <div className="empty">只显示最近 {maxLimit} 条</div>}
        {!shown && !error && <div className="empty">加载中…</div>}
      </div>
    </>
  )
}
