import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { hub } from '../api'
import { Banner } from '../components/Banner'
import { useActions } from '../components/Confirm'
import { failure, formatUptime, onlineCount } from '../manage'
import { useStore } from '../StoreProvider'

export function Settings() {
  const { state, reload } = useStore()
  const navigate = useNavigate()
  const actions = useActions()
  const [error, setError] = useState<string | null>(null)
  const [leaving, setLeaving] = useState(false)

  // 进来时拉一次最新状态（总开关、在线机器、规则版本）；reload 不会 reject，错误进 state.error
  useEffect(() => {
    void reload()
  }, [reload])

  const status = state.status
  const paused = status?.paused ?? false
  const { online, total } = onlineCount(status)

  function togglePause() {
    if (paused) {
      void actions.act(async () => {
        await hub.resume()
        await reload()
        return { tone: 'ok', title: '已恢复自动审批', text: '规则照常自动放行与拒绝，决定不了的送到手机' }
      }, '恢复失败')
      return
    }
    actions.ask({
      title: '暂停自动审批？',
      body:
        '暂停后各机器不再自动放行，也不再把请求送到这里：除了手写的拒绝规则照常生效，所有请求都交回终端处理。\n' +
        '机器上用 railse agent pause 设的本机暂停不受这里影响。',
      confirmLabel: '暂停',
      danger: false,
      run: async () => {
        await hub.pause()
        await reload()
        return { tone: 'ok', title: '已暂停自动审批', text: '所有请求交回终端处理；关掉这个开关即可恢复' }
      },
    })
  }

  async function logout() {
    setLeaving(true)
    setError(null)
    try {
      await hub.logout()
      navigate('/login', { replace: true })
    } catch (e) {
      // 没退成（多半是网络）：留在这里说明，不假装已经退出——服务端的会话还有效
      const m = failure(e, '退出失败')
      if (m) setError(`退出失败：${m}`)
      setLeaving(false)
    }
  }

  return (
    <>
      <header className="topbar">
        <h1>设置</h1>
      </header>
      <div className={actions.pageClass}>
        {state.error && (
          <Banner tone="warn">
            {status ? `状态刷新失败：${state.error}` : state.error}
            <button type="button" className="btn" onClick={() => void reload()}>
              重试
            </button>
          </Banner>
        )}
        {error && <Banner tone="warn">{error}</Banner>}
        <label className="switch">
          <span>
            暂停自动审批
            <span className="sub line">打开后所有请求交回终端处理；手写的拒绝规则仍然生效</span>
          </span>
          <input type="checkbox" role="switch" checked={paused} disabled={actions.busy || !status} onChange={togglePause} />
        </label>
        {/* 通知区块：Task 10 */}
        <h2 className="section-title">hub</h2>
        <dl className="kv">
          <dt>规则版本</dt>
          <dd>{status?.rules_version ?? '-'}</dd>
          <dt>在线机器</dt>
          <dd>{status ? `${online} / ${total}` : '-'}</dd>
          <dt>待审批</dt>
          <dd>{status?.pending ?? '-'}</dd>
          <dt>已运行</dt>
          <dd>{status ? formatUptime(status.uptime) : '-'}</dd>
        </dl>
        <button type="button" className="btn block page-end" disabled={leaving} onClick={() => void logout()}>
          {leaving ? '正在退出…' : '退出登录'}
        </button>
      </div>
      {actions.overlays}
    </>
  )
}
