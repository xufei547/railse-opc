import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { ApiError, hub } from '../api'
import { Banner } from '../components/Banner'
import { boundaryLine, deskGroups, inboxHref } from '../desk'
import { relativeTime, riskLabel, riskTone, stateLabel } from '../format'
import { splitVars, t } from '../i18n'
import { useStore } from '../StoreProvider'
import type { Desk as DeskData, DeskRow } from '../types'

/** 两次重取总台的最短间隔：实时帧一阵一阵地来，不跟着每一帧都拉。 */
export const deskThrottleMs = 1000
/** 页面开着时另外每隔这么久重取一次：机器上下线、角色表变化没有实时事件。 */
export const deskPollMs = 30_000

/**
 * 总台：按角色看等我、放行中、拦下、自动放行与最近动作（0.3 起）。数据全由 hub 的 GET /api/desk 算好；
 * 收到任何请求或放行的实时变化、重连、回到前台时重取（节流 1 秒），开着时每 30 秒再取一次。
 */
export function Desk() {
  const { state } = useStore()
  const [desk, setDesk] = useState<DeskData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const last = useRef(0)
  const [poll, setPoll] = useState(0)

  // 收到请求变化（tick）、全量重拉（loads：连上、回到前台……）、放行条数变化（status.trusts）、定时器到点（poll）都重取
  const trusts = state.status?.trusts
  useEffect(() => {
    let alive = true
    const wait = Math.max(0, last.current + deskThrottleMs - Date.now())
    const timer = setTimeout(() => {
      last.current = Date.now()
      hub.desk().then(
        (d) => {
          if (!alive) return
          setDesk(d)
          setError(null)
        },
        (e: unknown) => {
          if (!alive || (e instanceof ApiError && e.status === 401)) return
          setError(e instanceof Error ? e.message : String(e))
        },
      )
    }, wait)
    return () => {
      alive = false
      clearTimeout(timer)
    }
  }, [state.tick, state.loads, trusts, poll])

  useEffect(() => {
    const id = setInterval(() => setPoll((n) => n + 1), deskPollMs)
    return () => clearInterval(id)
  }, [])

  return (
    <>
      <header className="topbar">
        <h1>{t('总台')}</h1>
      </header>
      <div className="page">
        {error && <Banner tone="warn">{error}</Banner>}
        {!desk && !error && <div className="empty">{t('加载中…')}</div>}
        {desk && <DeskBody desk={desk} />}
      </div>
    </>
  )
}

export function DeskBody({ desk }: { desk: DeskData }) {
  const groups = deskGroups(desk)
  const many = groups.length > 1
  return (
    <>
      {desk.roles.length === 0 && (
        <section className="explain">
          <p className="headline">{t('还没有角色')}</p>
          <p className="why">
            {t('角色是 agent 会话的权限边界：每个角色有风险上限与禁用的操作，超出的一律问你。角色定义在机器上的 roles.yaml 里，手机只看不改。')}
          </p>
          <p className="why">
            {splitVars(t('在机器上运行 {cmd} 生成一份起步的模板。'), { cmd: <code key="cmd" className="mono">railse role init</code> })}
          </p>
        </section>
      )}
      {groups.length === 0 && <div className="empty">{t('最近 24 小时没有动静')}</div>}
      {groups.map((g) => (
        <section key={g.machine_id}>
          {many && (
            <h2 className="group-title">
              {g.machine_name}
              {!g.online && <span className="count">{t('离线')}</span>}
            </h2>
          )}
          {g.rows.map((r) => (
            <RoleCard key={r.name ?? ''} row={r} windowHours={desk.window_hours} />
          ))}
        </section>
      ))}
    </>
  )
}

/** 一个角色（或未分配）一张卡：名字与备注、边界一行、四个数字、最近动作。“等我”大于 0 时整张卡可点，进收件箱并按这一行筛选。 */
export function RoleCard({ row: r, windowHours }: { row: DeskRow; windowHours: number }) {
  const boundary = r.name === undefined ? '' : boundaryLine(r)
  const cls = `card desk-card${r.online ? '' : ' offline'}`
  const body = (
    <>
      <div className="row">
        <span className="what">{r.name ?? t('未分配')}</span>
        {!r.online && <span className="tag plain">{t('离线')}</span>}
      </div>
      {r.note && <div className="where">{r.note}</div>}
      {boundary && <div className="sub line">{boundary}</div>}
      <div className="stats">
        <Stat n={r.waiting} label={t('等我')} hot={r.waiting > 0} />
        <Stat n={r.trusts_active} label={t('放行中')} />
        <Stat n={r.denied} label={t('拦下')} />
        <Stat n={r.auto_allowed} label={t('自动放行')} />
      </div>
      <div className="sub line">{t('拦下与自动放行只数 {h} 小时内', { h: windowHours })}</div>
      {r.recent.length > 0 && (
        <ul className="recent">
          {r.recent.map((x) => (
            <li key={x.id}>
              <span className={`tag ${riskTone(x.risk)}`}>{riskLabel(x.risk)}</span>
              <code className="mono">{x.summary || x.tool_name}</code>
              <span className="sub nowrap">
                {stateLabel(x.state)} · {relativeTime(x.at)}
              </span>
              {x.beyond_role && <span className="tag high">{t('超出边界')}</span>}
            </li>
          ))}
        </ul>
      )}
    </>
  )
  if (r.waiting > 0) {
    return (
      <Link className={cls} to={inboxHref(r)}>
        {body}
      </Link>
    )
  }
  return <section className={cls}>{body}</section>
}

function Stat({ n, label, hot }: { n: number; label: string; hot?: boolean }) {
  return (
    <div className={`stat${hot ? ' hot' : ''}`}>
      <b>{n}</b>
      <span>{label}</span>
    </div>
  )
}
