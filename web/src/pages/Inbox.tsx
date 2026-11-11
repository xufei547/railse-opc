import { useEffect, useReducer, useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { Banner } from '../components/Banner'
import { RequestCard } from '../components/RequestCard'
import { hub } from '../api'
import { filterLabel, initialRoles, knownRoles, parseFilter, rolesReducer, unassignedLists, type RolesState } from '../desk'
import { createDeskLoader } from '../deskLoader'
import { deskPollMs, deskThrottleMs } from './Desk'
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
  // “未分配”要知道每台机器当前的角色表（角色已删的也算未分配）：进入筛选时重新取一次，筛选开着期间随请求事件节流重取（deskLoader）；
  // 取到完整快照前显示加载中，取失败显示错误与重试，都不展示部分结果；老 hub（404）没有角色，只认空串
  const unassigned = filter?.role === ''
  const [roles, rolesDispatch] = useReducer(rolesReducer, initialRoles)
  const [rolesLoader] = useState(() =>
    createDeskLoader({
      fetch: hub.desk,
      onData: (d) => rolesDispatch({ type: 'data', known: knownRoles(d) }),
      onError: (e) => rolesDispatch({ type: 'error', message: e instanceof Error ? e.message : String(e) }),
      onUnsupported: () => rolesDispatch({ type: 'unsupported' }),
      throttleMs: deskThrottleMs,
      pollMs: deskPollMs,
    }),
  )
  useEffect(() => {
    if (!unassigned) return
    rolesDispatch({ type: 'enter' })
    rolesLoader.start()
    return () => rolesLoader.stop()
  }, [unassigned, rolesLoader])
  useEffect(() => {
    if (unassigned) rolesLoader.request()
  }, [unassigned, rolesLoader, state.tick, state.loads])
  const retryRoles = () => {
    rolesDispatch({ type: 'retry' })
    rolesLoader.stop()
    rolesLoader.start()
  }
  const pendingView = unassignedLists(roles, state.pending, filter)
  const questionsView = unassignedLists(roles, state.questions, filter)
  const pending = pendingView.kind === 'list' ? pendingView.items : []
  const questions = questionsView.kind === 'list' ? questionsView.items : []
  const gated = pendingView.kind !== 'list'
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
        {filter && gated && <RolesGate state={roles} onRetry={retryRoles} />}
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

/** “未分配”筛选的角色表还没取到（加载中）或取失败（错误与重试）时，放在筛选条下面代替列表。取到了不显示任何东西。 */
export function RolesGate({ state, onRetry }: { state: RolesState; onRetry: () => void }) {
  if (state.status === 'loading') return <div className="empty">{t('正在取角色表…')}</div>
  if (state.status !== 'error') return null
  return (
    <Banner tone="warn">
      {t('取不到角色表，没法判断哪些是未分配：{reason}', { reason: state.message })}
      <button type="button" className="btn" onClick={onRetry}>
        {t('重试')}
      </button>
    </Banner>
  )
}
