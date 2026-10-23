import { useState } from 'react'
import { Link } from 'react-router'
import { Banner } from '../components/Banner'
import { RequestCard } from '../components/RequestCard'
import { usePush } from '../push'
import { isIOS, isStandalone } from '../pwa'
import { groupByProject } from '../store'
import { useStore } from '../StoreProvider'

const guideKey = 'railse.hideInstallGuide'

export function Inbox() {
  const { state, reload } = useStore()
  const push = usePush()
  const [hideGuide, setHideGuide] = useState(() => {
    try {
      return localStorage.getItem(guideKey) === '1'
    } catch {
      return false
    }
  })
  const showGuide = isIOS() && !isStandalone() && !hideGuide
  const groups = groupByProject(state.pending)
  const empty = state.loaded && state.pending.length === 0 && state.questions.length === 0

  function dismissGuide() {
    setHideGuide(true)
    try {
      localStorage.setItem(guideKey, '1')
    } catch {
      // 隐私模式等情况下存不了，本次打开期间不再显示即可
    }
  }

  return (
    <>
      <header className="topbar">
        <h1>收件箱</h1>
        <span
          className={`dot ${state.connected ? 'on' : ''}`}
          role="img"
          aria-label={state.connected ? '实时连接正常' : '实时连接断开'}
          title={state.connected ? '实时连接正常' : '实时连接断开'}
        />
      </header>
      <div className="page">
        {!state.connected && state.loaded && (
          <Banner tone="info">
            实时连接断开，正在重连；新请求可能晚到。
            <button type="button" className="btn" onClick={() => void reload()}>
              刷新
            </button>
          </Banner>
        )}
        {state.error && (
          <Banner tone="warn">
            {state.error}
            <button type="button" className="btn" onClick={() => void reload()}>
              重试
            </button>
          </Banner>
        )}
        {state.status?.paused && (
          <Banner tone="info">
            自动审批已暂停，所有请求都交回终端处理。<Link to="/settings">去设置</Link>
          </Banner>
        )}
        {push.status === 'stale' && (
          <Banner tone="warn">
            通知已失效，新审批不会推送到这台设备。<Link to="/settings">去设置重新开启</Link>
          </Banner>
        )}
        {showGuide && (
          <Banner tone="info">
            在 iPhone 上要先用 Safari 的“分享 → 添加到主屏幕”，再从主屏幕图标打开，才能收到通知。
            <button type="button" className="btn" onClick={dismissGuide}>
              知道了
            </button>
          </Banner>
        )}
        {state.questions.length > 0 && (
          <section>
            <h2 className="section-title">等你回答</h2>
            {state.questions.map((q) => (
              <RequestCard key={q.id} request={q} />
            ))}
          </section>
        )}
        {groups.length > 0 && (
          <section>
            <h2 className="section-title">待审批</h2>
            {groups.map((g) => (
              <div key={g.key}>
                <div className="group-title">
                  {g.project}
                  <span className="count">{g.items.length}</span>
                </div>
                {g.items.map((r) => (
                  <RequestCard key={r.id} request={r} />
                ))}
              </div>
            ))}
          </section>
        )}
        {!state.loaded && !state.error && <div className="empty">加载中…</div>}
        {empty && <div className="empty">没有等你处理的事</div>}
      </div>
    </>
  )
}
