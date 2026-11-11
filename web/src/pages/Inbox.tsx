import { useEffect, useReducer, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { Banner } from '../components/Banner'
import { RequestCard } from '../components/RequestCard'
import { hub } from '../api'
import { filterLabel, filterReducer, inboxQuery, initialFilter, parseFilter, type FilterState } from '../desk'
import { createDeskLoader, type DeskLoader } from '../deskLoader'
import { deskPollMs, deskThrottleMs } from './Desk'
import { lang, splitVars, t } from '../i18n'
import { pauseCopy } from '../manage'
import { usePush } from '../push'
import { isIOS, isStandalone } from '../pwa'
import { groupByProject } from '../store'
import { useStore } from '../StoreProvider'
import { trustBanner } from '../trust'

const guideKey = 'railse.hideInstallGuide'

/** initialFiltered 只给测试用（服务端渲染不跑 effect，拿不到 hub 筛好的结果）。 */
export function Inbox({ initialFiltered = initialFilter }: { initialFiltered?: FilterState } = {}) {
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
  // 有筛选时由 hub 在截断之前筛好（GET /api/inbox?role=…&machine=…，“未分配”也由 hub 判），网页不拿全局列表自己筛：
  // 进入筛选或换筛选时重取，筛选开着期间随请求事件节流重取（deskLoader）；取到前显示加载中，取失败给错误与重试，都不展示列表
  const query = inboxQuery(filter)
  const [filtered, filterDispatch] = useReducer(filterReducer, initialFiltered)
  const [filterLoader, setFilterLoader] = useState<DeskLoader | null>(null)
  useEffect(() => {
    if (!query) return
    const loader = createDeskLoader({
      fetch: () => hub.inbox(query),
      onData: (inbox) => filterDispatch({ type: 'data', inbox }),
      onError: (e) => filterDispatch({ type: 'error', message: e instanceof Error ? e.message : String(e) }),
      onUnsupported: () => filterDispatch({ type: 'error', message: t('请求失败（{status}）', { status: 404 }) }),
      throttleMs: deskThrottleMs,
      pollMs: deskPollMs,
    })
    setFilterLoader(loader)
    filterDispatch({ type: 'enter' })
    loader.start()
    return () => {
      loader.stop()
      setFilterLoader(null)
    }
  }, [query])
  useEffect(() => {
    filterLoader?.request()
  }, [filterLoader, state.tick, state.loads])
  const retryFilter = () => {
    filterDispatch({ type: 'retry' })
    filterLoader?.stop()
    filterLoader?.start()
  }
  const listed = !filter ? { pending: state.pending, questions: state.questions } : filtered.status === 'ok' ? filtered.inbox : null
  const pending = listed?.pending ?? []
  const questions = listed?.questions ?? []
  const gated = listed === null
  const machineName =
    filter?.machine == null
      ? ''
      : ([...state.pending, ...state.questions].find((r) => r.machine_id === filter.machine)?.machine_name ??
        state.status?.machines.find((m) => m.id === filter.machine)?.name ??
        '')
  const groups = groupByProject(pending)
  const empty = state.loaded && !gated && pending.length === 0 && questions.length === 0
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
        {filter && gated && <FilterGate state={filtered} onRetry={retryFilter} />}
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
        {!filter && state.pendingTruncated && <TruncatedHint machines={state.status?.machines ?? []} />}
      </div>
    </>
  )
}

/** 有筛选时 hub 的结果还没到（加载中）或取失败（错误与重试），放在筛选条下面代替列表。取到了不显示任何东西。 */
export function FilterGate({ state, onRetry }: { state: FilterState; onRetry: () => void }) {
  if (state.status === 'loading') return <div className="empty">{t('加载中…')}</div>
  if (state.status !== 'error') return null
  return (
    <Banner tone="warn">
      {state.message}
      <button type="button" className="btn" onClick={onRetry}>
        {t('重试')}
      </button>
    </Banner>
  )
}

/** 全局待批列表被 hub 截断（只给了最新的 500 条）时的底部提示：还有更早的，按机器看（按机器筛选由 hub 在截断之前筛）。 */
function TruncatedHint({ machines }: { machines: { id: string; name: string; revoked: boolean }[] }) {
  const live = machines.filter((m) => !m.revoked)
  return (
    <p className="sub truncated-hint">
      {t('还有更早的待批没显示。按机器查看：')}
      {live.map((m, i) => (
        <span key={m.id}>
          {i > 0 && ' · '}
          <Link to={`/inbox?machine=${encodeURIComponent(m.id)}`}>{m.name}</Link>
        </span>
      ))}
    </p>
  )
}
