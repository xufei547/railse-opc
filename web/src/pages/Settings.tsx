import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { hub } from '../api'
import { Banner } from '../components/Banner'
import { useActions, type Actions } from '../components/Confirm'
import { failure, formatUptime, onlineCount, pauseCopy } from '../manage'
import { lang, readChoice, resolveLang, saveChoice, setLang, t, type LangChoice } from '../i18n'
import { browserEnv, logoutDevice, resubscribeForLang, usePush, type PushApi, type PushControl, type PushEnv } from '../push'
import { useStore } from '../StoreProvider'

export function Settings() {
  const { state, reload } = useStore()
  const navigate = useNavigate()
  const actions = useActions()
  const push = usePush()
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
        return { tone: 'ok', title: t('已恢复自动审批'), text: t('规则照常自动放行与拒绝，决定不了的送到手机') }
      }, t('恢复失败'))
      return
    }
    actions.ask({
      title: t('暂停自动审批？'),
      body: pauseCopy().confirm,
      confirmLabel: t('暂停'),
      danger: false,
      run: async () => {
        await hub.pause()
        await reload()
        return { tone: 'ok', title: t('已暂停自动审批'), text: pauseCopy().done }
      },
    })
  }

  // 先尽力关掉这台设备的推送（不然退出后锁屏上照样收到审批摘要；最多等几秒，失败不挡退出），再退出登录
  async function logout() {
    setLeaving(true)
    setError(null)
    try {
      await logoutDevice(browserEnv, hub)
      navigate('/login', { replace: true })
    } catch (e) {
      // 没退成（多半是网络）：留在这里说明，不假装已经退出——服务端的会话还有效。推送可能已经关了，重新检查一次
      const m = failure(e, t('退出失败'))
      if (m) setError(t('退出失败：{reason}', { reason: m }))
      setLeaving(false)
      void push.refresh()
    }
  }

  return (
    <>
      <header className="topbar">
        <h1>{t('设置')}</h1>
      </header>
      <div className={actions.pageClass}>
        {state.error && (
          <Banner tone="warn">
            {status ? t('状态刷新失败：{reason}', { reason: state.error }) : state.error}
            <button type="button" className="btn" onClick={() => void reload()}>
              {t('重试')}
            </button>
          </Banner>
        )}
        {error && <Banner tone="warn">{error}</Banner>}
        <label className="switch">
          <span>
            {t('暂停自动审批')}
            <span className="sub line">{pauseCopy().sub}</span>
          </span>
          <input type="checkbox" role="switch" checked={paused} disabled={actions.busy || !status} onChange={togglePause} />
        </label>
        <NotificationView push={push} act={actions.act} busy={actions.busy || leaving} />
        <LanguageView
          choice={readChoice(typeof localStorage === 'undefined' ? null : localStorage)}
          busy={actions.busy || leaving}
          onChange={(c) =>
            void switchLanguage(c, {
              storage: typeof localStorage === 'undefined' ? null : localStorage,
              env: browserEnv,
              api: hub,
              reload: () => window.location.reload(),
              pushOn: push.status === 'on',
            })
          }
        />
        <h2 className="section-title">{t('hub')}</h2>
        <dl className="kv">
          <dt>{t('规则版本')}</dt>
          <dd>{status?.rules_version ?? '-'}</dd>
          <dt>{t('在线机器')}</dt>
          <dd>{status ? `${online} / ${total}` : '-'}</dd>
          <dt>{t('待处理')}</dt>
          <dd>{status?.pending ?? '-'}</dd>
          <dt>{t('已运行')}</dt>
          <dd>{status ? formatUptime(status.uptime) : '-'}</dd>
        </dl>
        <button type="button" className="btn block page-end" disabled={leaving || actions.busy || push.busy} onClick={() => void logout()}>
          {leaving ? t('正在退出…') : t('退出登录')}
        </button>
      </div>
      {actions.overlays}
    </>
  )
}

// 是函数而不是常量：文案按调用时的语言取
const pushTexts = (): Record<PushControl['status'], string> => ({
  checking: t('正在检查这台设备的通知状态…'),
  unsupported: t('这个浏览器不支持网页推送，这台设备收不到新审批的通知。'),
  need_standalone: t('iPhone、iPad 上要先把 Railse 加到主屏幕，再从主屏幕图标打开，才能开启通知：'),
  hub_disabled: t('这个 hub 没有启用推送'),
  denied: t('通知权限被拒绝了。iPhone、iPad 上到“设置 → 通知 → Railse”里允许通知；电脑上在浏览器地址栏左侧的网站设置里允许。改好后回到这里。'),
  off: t('还没开启。开启后，新的审批与提问会推送到这台设备。'),
  on: t('已开启：新的审批与提问会推送到这台设备。'),
  stale: t('通知已失效：hub 上已经没有这台设备的订阅（推送服务注销了它，或 hub 换了推送密钥），新审批不会推送过来。重新开启即可。'),
  unknown: t('查不到通知状态'),
})

/** 状态说明；hub 停用推送、查不到状态时带上原因（整句一个键，英文冒号后接整句也通顺）。 */
function pushText(s: PushControl['status'], reason: string): string {
  // 英文不透出 hub 的中文原因（hub 现在只有一个原因：没配 --public-url），整句取英文
  if (s === 'hub_disabled' && lang() === 'en' && /[\u4e00-\u9fff]/.test(reason)) return t('这个 hub 没有启用推送：hub 没有配置 --public-url')
  if (reason && s === 'hub_disabled') return t('这个 hub 没有启用推送：{reason}', { reason })
  if (reason && s === 'unknown') return t('查不到通知状态：{reason}', { reason })
  return pushTexts()[s]
}

/**
 * 通知区块的展示部分：状态文案与按钮。只有 off、stale 有“开启”（classify 保证这时能推送、不是 iOS 的 Safari 标签页）。
 * 开启、关闭、测试都经页面的 act 做：与暂停、恢复自动审批互斥（同一时刻一件，确认面板开着时不能点），结果进底部的结果提示。
 * busy 是页面上别的操作在进行（useActions 的 busy，或正在退出登录）。
 */
export function NotificationView({ push, act, busy = false }: { push: PushControl; act: Actions['act']; busy?: boolean }) {
  const s = push.status
  const off = push.busy || busy
  // act 在第一个 await 之前就同步调用 run：“开启”的点击栈里依次是 act → push.enable → enablePush → pwa.subscribe →
  // Notification.requestPermission()，前面不能有 await（iOS 手势规则；settings.push.test.tsx 钉住）
  const enable = () => void act(() => push.enable(), t('没有开启通知'))
  const disable = () => void act(() => push.disable(), t('没有关闭通知'))
  const test = () => void act(() => push.test(), t('测试通知没有发出'))
  return (
    <section>
      <h2 className="section-title">{t('通知')}</h2>
      <p className="push-state">{pushText(s, push.reason)}</p>
      {s === 'need_standalone' && (
        <>
          <ol className="steps">
            <li>{t('在 Safari 里点“分享”按钮（找不到时先点地址栏旁的“⋯”）')}</li>
            <li>{t('选“添加到主屏幕”，再点“添加”')}</li>
            <li>{t('回到主屏幕，点 Railse 图标打开，到“设置”里开启通知')}</li>
          </ol>
          <p className="sub line">{t('需要 iOS 16.4 或更新的系统。')}</p>
        </>
      )}
      {(s === 'off' || s === 'stale') && (
        <div className="btn-row">
          <button type="button" className="btn primary" disabled={off} onClick={enable}>
            {s === 'stale' ? t('重新开启通知') : t('开启通知')}
          </button>
          {s === 'stale' && (
            <button type="button" className="btn" disabled={off} onClick={disable}>
              {t('清除旧订阅')}
            </button>
          )}
        </div>
      )}
      {s === 'on' && (
        <div className="btn-row">
          <button type="button" className="btn" disabled={off} onClick={test}>
            {t('发一条测试通知')}
          </button>
          <button type="button" className="btn" disabled={off} onClick={disable}>
            {t('关闭通知')}
          </button>
        </div>
      )}
      {(s === 'denied' || s === 'unknown') && (
        <div className="btn-row">
          <button type="button" className="btn" disabled={off} onClick={() => void push.refresh()}>
            {s === 'denied' ? t('重新检查') : t('重试')}
          </button>
        </div>
      )}
    </section>
  )
}

/** 切换语言：先存，再（推送开着时）把订阅连同新语言重登记，最后整页刷新让每一处文案重算。重登记失败不拦着刷新。 */
export async function switchLanguage(
  choice: LangChoice,
  deps: {
    storage: Pick<Storage, 'setItem' | 'removeItem'> | null
    env: Pick<PushEnv, 'getSubscription'>
    api: Pick<PushApi, 'pushSubscribe'>
    reload: () => void
    pushOn: boolean
  },
): Promise<void> {
  saveChoice(deps.storage, choice)
  setLang(resolveLang(choice, typeof navigator === 'undefined' ? undefined : navigator.language))
  if (deps.pushOn) {
    try {
      await resubscribeForLang(deps.env, deps.api)
    } catch {
      // 下次开启通知或 SW 重订阅时会再带语言
    }
  }
  deps.reload()
}

const choices: { value: LangChoice; label: () => string }[] = [
  { value: 'auto', label: () => t('跟随系统') },
  { value: 'zh', label: () => '中文' }, // 语言名用本语言写，不翻
  { value: 'en', label: () => 'English' },
]

export function LanguageView({ choice, onChange, busy }: { choice: LangChoice; onChange: (c: LangChoice) => void; busy: boolean }) {
  return (
    <section>
      <h2 className="section-title">{t('语言')}
        {lang() === 'zh' ? ' / Language' : ''}
      </h2>
      <div className="choices" role="radiogroup" aria-label={t('语言')}>
        {choices.map((c) => (
          <label key={c.value} className="option">
            <input type="radio" name="lang" value={c.value} checked={choice === c.value} disabled={busy} onChange={() => onChange(c.value)} />
            <span>{c.label()}</span>
          </label>
        ))}
      </div>
      <p className="sub line">{t('通知的文字也跟着这个语言；换了之后这台设备的订阅会重新登记。')}</p>
    </section>
  )
}
