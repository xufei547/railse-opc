import { useCallback, useEffect, useState } from 'react'
import { hub } from '../api'
import { coalesce } from '../coalesce'
import { Banner } from '../components/Banner'
import { useActions } from '../components/Confirm'
import { relativeTime } from '../format'
import { lang, splitVars, t } from '../i18n'
import { failure, machineSeen, sortMachines } from '../manage'
import { useStore } from '../StoreProvider'
import type { Machine } from '../types'

/** 一台机器：在线点、名字、版本、最近在线、接入时间；没吊销的可吊销，已吊销的灰显。 */
export function MachineRow({ machine: m, busy, onRevoke }: { machine: Machine; busy: boolean; onRevoke: (m: Machine) => void }) {
  const on = m.online && !m.revoked
  const dotLabel = m.revoked ? t('已吊销') : on ? t('在线') : t('离线')
  return (
    <div className={m.revoked ? 'list-item revoked' : 'list-item'}>
      <span className={on ? 'dot on' : 'dot'} role="img" aria-label={dotLabel} title={dotLabel} />
      <div className="grow">
        <div className="name">
          {m.name}
          {m.revoked && <span className="tag plain">{t('已吊销')}</span>}
        </div>
        <div className="sub">
          {m.agent_version ? t('版本 {version}', { version: m.agent_version }) : t('版本未知')} · {machineSeen(m)} ·{' '}
          {t('接入于 {when}', { when: relativeTime(m.enrolled_at) })}
        </div>
      </div>
      {!m.revoked && (
        <button type="button" className="btn danger-text" disabled={busy} aria-label={t('吊销 {name}', { name: m.name })} onClick={() => onRevoke(m)}>
          {t('吊销')}
        </button>
      )}
    </div>
  )
}

export function Machines() {
  const { state, loadMachines } = useStore()
  const [error, setError] = useState<string | null>(null)
  const actions = useActions()

  // 单飞、串行，不会 reject（错误进 error）
  const [sync] = useState(() => coalesce(loadMachines))
  const refresh = useCallback(async () => {
    try {
      await sync()
      setError(null)
    } catch (e) {
      const m = failure(e, t('加载失败'))
      if (m) setError(m)
    }
  }, [sync])
  // 在线与否没有实时帧：进来时、来了请求的帧（tick）、每次全量重拉（loads：重连、回到前台、网络恢复）之后都重拉
  useEffect(() => {
    void refresh()
  }, [refresh, state.tick, state.loads])

  function revoke(m: Machine) {
    actions.ask({
      title: t('吊销 {name}？', { name: m.name }),
      body:
        t('它的密钥立即失效、连接马上断开，它还在等的请求会被取消；之后那台机器上的审批都交回终端。') +
        '\n' +
        t('要重新接入，得在 hub 上运行 railse hub token，再在那台机器上运行 railse agent enroll --force。'),
      confirmLabel: t('吊销'),
      danger: true,
      run: async () => {
        await hub.revokeMachine(m.id)
        await refresh()
        return { tone: 'ok', title: t('已吊销'), text: t('{name} 的密钥已失效，不能再连 hub', { name: m.name }) }
      },
    })
  }

  const machines = state.machines ? sortMachines(state.machines) : null
  return (
    <>
      <header className="topbar">
        {/* 同一个“机器”在 RequestAttrs 里是单数字段名（en 表里是 Machine），页面标题要复数，所以按语言分支 */}
        <h1>{lang() === 'en' ? 'Machines' : '机器'}</h1>
      </header>
      <div className={actions.pageClass}>
        {error && (
          <Banner tone="warn">
            {machines ? t('刷新失败：{reason}', { reason: error }) : error}
            <button type="button" className="btn" onClick={() => void refresh()}>
              {t('重试')}
            </button>
          </Banner>
        )}
        {machines?.map((m) => (
          <MachineRow key={m.id} machine={m} busy={actions.busy} onRevoke={revoke} />
        ))}
        {machines === null && !error && <div className="empty">{t('加载中…')}</div>}
        {machines?.length === 0 && (
          <div className="empty">
            {splitVars(t('还没有机器。在 hub 上运行 {token}，再在机器上运行 {enroll}。'), {
              token: (
                <code key="token" className="mono">
                  railse hub token
                </code>
              ),
              enroll: (
                <code key="enroll" className="mono">
                  railse agent enroll
                </code>
              ),
            })}
          </div>
        )}
      </div>
      {actions.overlays}
    </>
  )
}
