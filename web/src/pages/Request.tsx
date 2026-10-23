import { useEffect, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { ApiError, hub } from '../api'
import { coalesce } from '../coalesce'
import { Banner } from '../components/Banner'
import { DecideSheet } from '../components/DecideSheet'
import { RequestContent } from '../components/Payload'
import { AuditList, RequestAttrs } from '../components/RequestAttrs'
import { doneMessage, keepDecided, type Detail, type Verdict } from '../decide'
import { kindLabel, riskLabel, riskTone } from '../format'
import { useStore } from '../StoreProvider'
import type { RequestView } from '../types'

export function RequestPage() {
  const { id = '' } = useParams()
  // 换一条请求（如从通知点进另一条）时整页重来：旧请求的在途结果、开着的面板都不会落到新页面上
  return <RequestDetail key={id} id={id} />
}

interface Flash {
  tone: 'ok' | 'warn'
  text: string
}

function RequestDetail({ id }: { id: string }) {
  const navigate = useNavigate()
  const location = useLocation()
  const { state, reload } = useStore()
  const [data, setData] = useState<Detail | null>(null)
  const [missing, setMissing] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [action, setAction] = useState<Verdict | null>(null)
  const [flash, setFlash] = useState<Flash | null>(null)

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
  // 首次进来、每来一帧请求变化（tick）、每次全量重拉（loads：重连、回到前台、网络恢复，断线期间的帧都丢了）都重拉这一条
  useEffect(() => {
    void load()
  }, [load, state.tick, state.loads])

  const r = data?.request
  const canDecide = !!r && r.state === 'pending' && (r.kind === 'permission' || r.kind === 'webhook')

  function back() {
    // 直接打开（通知、书签、刷新）时没有上一页：回收件箱，而不是退出应用
    if (location.key === 'default') navigate('/')
    else navigate(-1)
  }

  function open(a: Verdict) {
    setFlash(null)
    setAction(a)
  }

  function onDone(updated: RequestView | null, message?: string) {
    setAction(null)
    if (updated) {
      setData((d) => (d ? { ...d, request: updated } : d))
      setFlash({ tone: 'ok', text: doneMessage(updated) })
    } else {
      // 已处理、已过期、机器已吊销（409），没有这条（404），或在别处有了结果：告诉用户为什么，并重新加载
      setFlash({ tone: 'warn', text: message || '这条请求已经决定不了' })
    }
    void load()
    void reload()
  }

  return (
    <>
      <header className="topbar">
        <button type="button" className="back" onClick={back} aria-label="返回">
          ‹
        </button>
        <h1>{r ? kindLabel(r.kind) : '请求'}</h1>
        {r?.kind === 'permission' && <span className={`tag ${riskTone(r.risk)}`}>{riskLabel(r.risk)}</span>}
      </header>
      <div className="page">
        {flash && <Banner tone={flash.tone}>{flash.text}</Banner>}
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
            {r.kind === 'question' && (
              <Banner tone="info">agent 在终端里等你回答这个问题，请去终端回答；它继续之后这条会从收件箱消失。</Banner>
            )}
            <RequestContent request={r} />
            <RequestAttrs request={r} audit={data.audit} />
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
            <AuditList audit={data.audit} />
          </>
        )}
        {!data && !missing && !error && <div className="empty">加载中…</div>}
      </div>
      {r && action && <DecideSheet key={action} request={r} action={action} onClose={() => setAction(null)} onDone={onDone} />}
    </>
  )
}
