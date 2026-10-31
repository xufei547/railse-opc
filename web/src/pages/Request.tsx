import { useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { ApiError, hub } from '../api'
import { coalesce } from '../coalesce'
import { Banner } from '../components/Banner'
import { DecideSheet } from '../components/DecideSheet'
import { RequestSummary, StatusLine } from '../components/Explain'
import { RequestDetails } from '../components/RequestAttrs'
import { ResultToast } from '../components/ResultToast'
import { keepDecided, outcomeView, type Detail, type Outcome, type Verdict } from '../decide'
import { kindLabel, riskLabel, riskTone } from '../format'
import { useStore } from '../StoreProvider'
import type { RequestView } from '../types'

export function RequestPage() {
  const { id = '' } = useParams()
  // 换一条请求（如从通知点进另一条）时整页重来：旧请求的在途结果、开着的面板都不会落到新页面上
  return <RequestDetail key={id} id={id} />
}

function RequestDetail({ id }: { id: string }) {
  const navigate = useNavigate()
  const location = useLocation()
  const { state, reload } = useStore()
  const [data, setData] = useState<Detail | null>(null)
  const [missing, setMissing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [action, setAction] = useState<Verdict | null>(null)
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  const toast = useRef<HTMLDivElement>(null)

  // 单飞、串行：一阵变化只多拉一次，后发的结果不会被先发的旧结果覆盖
  const [load] = useState(() =>
    coalesce(async () => {
      try {
        const d = await hub.request(id)
        setData((prev) => keepDecided(prev, d))
        setMissing(false)
        setError(null)
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return // 登录失效回调已在跳登录页
        if (e instanceof ApiError && e.status === 404) {
          setData(null)
          setMissing(true)
          setError(null)
          return
        }
        setError(e instanceof ApiError ? e.message : '加载失败')
      }
    }),
  )
  // 首次进来、来了关于这一条的帧（touched）、每次全量重拉（loads：重连、回到前台、网络恢复，断线期间的帧都丢了）都重拉这一条
  const touched = state.touched[id] ?? 0
  useEffect(() => {
    void load()
  }, [load, touched, state.loads])

  // 面板开着时这条没了（重拉回 404）：面板随数据一起消失，这里补上为什么
  useEffect(() => {
    if (missing && action) {
      setAction(null)
      setOutcome({ kind: 'gone', message: '这条请求已不存在，决定面板已关闭' })
    }
  }, [missing, action])

  // 有了结果就把焦点给结果提示（读屏会读出来；面板关闭后原来的批准、拒绝按钮可能已经不在了）
  useEffect(() => {
    if (outcome) toast.current?.focus({ preventScroll: true })
  }, [outcome])

  const r = data?.request
  const canDecide = !!r && r.state === 'pending' && (r.kind === 'permission' || r.kind === 'webhook')

  function back() {
    // 直接打开（通知、书签、刷新）时没有上一页：回收件箱，而不是退出应用
    if (location.key === 'default') navigate('/')
    else navigate(-1)
  }

  function open(a: Verdict) {
    setOutcome(null)
    setAction(a)
  }

  function onDone(updated: RequestView | null, message?: string) {
    setAction(null)
    if (updated) {
      setData((d) => (d ? { ...d, request: updated } : d))
      setOutcome({ kind: 'done', request: updated })
    } else {
      // 已处理、已过期、机器已吊销（409），没有这条（404），或面板开着时有了结果：告诉用户为什么，并重新加载
      setOutcome({ kind: 'gone', message: message || '这条请求已经决定不了', attempted: action ?? undefined })
    }
    void load()
    void reload()
  }

  // 结果提示按当前（重新拉到的）数据算：送达后文案跟着变；409 但其实是你上次超时的提交已生效时显示“你的决定已生效”
  const view = outcome ? outcomeView(outcome, r ?? null) : null

  return (
    <>
      <header className="topbar">
        <button type="button" className="back" onClick={back} aria-label="返回">
          ‹
        </button>
        <h1>{r ? kindLabel(r.kind) : '请求'}</h1>
        {r?.kind === 'permission' && <span className={`tag ${riskTone(r.risk)}`}>{riskLabel(r.risk)}</span>}
      </header>
      <div className={view ? 'page has-toast' : 'page'}>
        {error && (
          <Banner tone="warn">
            {data ? `刷新失败：${error}` : error}
            <button type="button" className="btn" onClick={() => void load()}>
              重试
            </button>
          </Banner>
        )}
        {missing && (
          <div className="empty">
            <p>没有这条请求</p>
            <Link to="/">回收件箱</Link>
          </div>
        )}
        {r && data && (
          <>
            <RequestSummary request={r} />
            <StatusLine request={r} audit={data.audit} />
            <RequestDetails request={r} audit={data.audit} />
            {canDecide && (
              <div className="decide-bar">
                <button type="button" className="btn danger" onClick={() => open('deny')}>
                  拒绝
                </button>
                <button type="button" className="btn primary" onClick={() => open('allow')}>
                  批准
                </button>
              </div>
            )}
          </>
        )}
        {!data && !missing && !error && <div className="empty">加载中…</div>}
      </div>
      {view && <ResultToast view={view} onClose={() => setOutcome(null)} raised={canDecide} ref={toast} />}
      {r && action && <DecideSheet key={action} request={r} action={action} onClose={() => setAction(null)} onDone={onDone} />}
    </>
  )
}
