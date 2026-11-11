import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { Banner } from '../components/Banner'
import { RequestCard } from '../components/RequestCard'
import { applyFilter, filterLabel, parseFilter } from '../desk'
import { lang, splitVars, t } from '../i18n'
import { pauseCopy } from '../manage'
import { usePush } from '../push'
import { isIOS, isStandalone } from '../pwa'
import { groupByProject } from '../store'
import { useStore } from '../StoreProvider'
import { trustBanner } from '../trust'

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
  // 总台跳来时按角色与机器筛选（/inbox?role=…&machine=…）；筛选条可清除
  const [params, setParams] = useSearchParams()
  const filter = parseFilter(params)
  const pending = applyFilter(state.pending, filter)
  const questions = applyFilter(state.questions, filter)
  const machineName =
    filter?.machine == null
      ? ''
      : ([...state.pending, ...state.questions].find((r) => r.machine_id === filter.machine)?.machine_name ??
        state.status?.machines.find((m) => m.id === filter.machine)?.name ??
        '')
  const groups = groupByProject(pending)
  const empty = state.loaded && pending.length === 0 && questions.length === 0
  const trustCount = state.status?.trusts ?? 0

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
        <h1>{t('收件箱')}</h1>
        <span
          className={`dot ${state.connected ? 'on' : ''}`}
          role="img"
          aria-label={state.connected ? t('实时连接正常') : t('实时连接断开')}
          title={state.connected ? t('实时连接正常') : t('实时连接断开')}
        />
      </header>
      <div className="page">
        {!state.connected && state.loaded && (
          <Banner tone="info">
            {t('实时连接断开，正在重连；新请求可能晚到。')}
            <button type="button" className="btn" onClick={() => void reload()}>
              {t('刷新')}
            </button>
          </Banner>
        )}
        {state.error && (
          <Banner tone="warn">
            {state.error}
            <button type="button" className="btn" onClick={() => void reload()}>
              {t('重试')}
            </button>
          </Banner>
        )}
        {state.status?.paused && (
          <Banner tone="info">
            {pauseCopy().banner}
            {/* 英文句子与后面的链接之间要空格，中文不要 */}
            {lang() === 'en' && ' '}
            <Link to="/settings">{t('去设置')}</Link>
          </Banner>
        )}
        {trustCount > 0 && (
          <Banner tone="info">
            {trustBanner(trustCount, state.trusts)}
            {lang() === 'en' && ' '}
            <Link to="/settings">{t('去设置')}</Link>
          </Banner>
        )}
        {push.status === 'stale' && (
          <Banner tone="warn">
            {splitVars(t('通知已失效，新审批不会推送到这台设备。{link}'), {
              link: (
                <Link key="link" to="/settings">
                  {t('去设置重新开启')}
                </Link>
              ),
            })}
          </Banner>
        )}
        {showGuide && (
          <Banner tone="info">
            {t('在 iPhone 上要先用 Safari 的“分享 → 添加到主屏幕”，再从主屏幕图标打开，才能收到通知。')}
            <button type="button" className="btn" onClick={dismissGuide}>
              {t('知道了')}
            </button>
          </Banner>
        )}
        {filter && (
          <div className="filter-bar" role="status">
            <span className="grow">{t('只看：{what}', { what: filterLabel(filter, machineName) })}</span>
            <button type="button" className="btn small" onClick={() => setParams({}, { replace: true })}>
              {t('清除筛选')}
            </button>
          </div>
        )}
        {questions.length > 0 && (
          <section>
            <h2 className="section-title">{t('等你回答')}</h2>
            {questions.map((q) => (
              <RequestCard key={q.id} request={q} />
            ))}
          </section>
        )}
        {groups.length > 0 && (
          <section>
            <h2 className="section-title">{t('待处理')}</h2>
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
        {!state.loaded && !state.error && <div className="empty">{t('加载中…')}</div>}
        {empty && <div className="empty">{t('没有等你处理的事')}</div>}
      </div>
    </>
  )
}
