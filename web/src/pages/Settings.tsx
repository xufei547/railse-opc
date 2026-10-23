import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { hub } from '../api'
import { Banner } from '../components/Banner'
import { useActions } from '../components/Confirm'
import type { OutcomeView } from '../decide'
import { failure, formatUptime, onlineCount } from '../manage'
import { usePush, type PushControl } from '../push'
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
        <NotificationSection onResult={actions.notify} />
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

const pushTexts: Record<PushControl['status'], string> = {
  checking: '正在检查这台设备的通知状态…',
  unsupported: '这个浏览器不支持网页推送，这台设备收不到新审批的通知。',
  need_standalone: 'iPhone、iPad 上要先把 Railse 加到主屏幕，再从主屏幕图标打开，才能开启通知：',
  hub_disabled: '这个 hub 没有启用推送',
  denied:
    '通知权限被拒绝了。iPhone、iPad 上到“设置 → 通知 → Railse”里允许通知；电脑上在浏览器地址栏左侧的网站设置里允许。改好后回到这里。',
  off: '还没开启。开启后，新的审批与提问会推送到这台设备。',
  on: '已开启：新的审批与提问会推送到这台设备。',
  stale: '通知已失效：hub 上已经没有这台设备的订阅（推送服务注销了它，或 hub 换了推送密钥），新审批不会推送过来。重新开启即可。',
  unknown: '查不到通知状态',
}

/** 通知区块：挂载时检查推送状态，结果提示交给页面底部的结果提示（useActions 的 notify）。 */
function NotificationSection({ onResult }: { onResult: (v: OutcomeView) => void }) {
  const push = usePush()
  return <NotificationView push={push} onResult={(v) => v && onResult(v)} />
}

/** 通知区块的展示部分：状态文案与按钮。只有 off、stale 有“开启”（classify 保证这时能推送、不是 iOS 的 Safari 标签页）。 */
export function NotificationView({ push, onResult }: { push: PushControl; onResult: (v: OutcomeView | null) => void }) {
  const s = push.status
  const detail = (s === 'hub_disabled' || s === 'unknown') && push.reason ? `：${push.reason}` : ''
  // “开启通知”的处理函数里第一个调用就是 push.enable()（其中第一句是授权请求），前面不能有 await
  const enable = () => void push.enable().then(onResult)
  return (
    <section>
      <h2 className="section-title">通知</h2>
      <p className="push-state">
        {pushTexts[s]}
        {detail}
      </p>
      {s === 'need_standalone' && (
        <>
          <ol className="steps">
            <li>在 Safari 里点“分享”按钮（找不到时先点地址栏旁的“⋯”）</li>
            <li>选“添加到主屏幕”，再点“添加”</li>
            <li>回到主屏幕，点 Railse 图标打开，到“设置”里开启通知</li>
          </ol>
          <p className="sub line">需要 iOS 16.4 或更新的系统。</p>
        </>
      )}
      {(s === 'off' || s === 'stale') && (
        <div className="btn-row">
          <button type="button" className="btn primary" disabled={push.busy} onClick={enable}>
            {s === 'stale' ? '重新开启通知' : '开启通知'}
          </button>
          {s === 'stale' && (
            <button type="button" className="btn" disabled={push.busy} onClick={() => void push.disable().then(onResult)}>
              清除旧订阅
            </button>
          )}
        </div>
      )}
      {s === 'on' && (
        <div className="btn-row">
          <button type="button" className="btn" disabled={push.busy} onClick={() => void push.test().then(onResult)}>
            发一条测试通知
          </button>
          <button type="button" className="btn" disabled={push.busy} onClick={() => void push.disable().then(onResult)}>
            关闭通知
          </button>
        </div>
      )}
      {(s === 'denied' || s === 'unknown') && (
        <div className="btn-row">
          <button type="button" className="btn" disabled={push.busy} onClick={() => void push.refresh()}>
            {s === 'denied' ? '重新检查' : '重试'}
          </button>
        </div>
      )}
    </section>
  )
}
