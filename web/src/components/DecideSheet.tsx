import { useEffect, useReducer, useRef, useState } from 'react'
import { hub } from '../api'
import {
  canConfirm,
  classifyError,
  decideBody,
  draftLead,
  draftScope,
  draftShown,
  goneMessage,
  initSheet,
  isEscape,
  minutesLabel,
  noteHint,
  rememberBlock,
  retryable,
  scopeOptions,
  sheetReducer,
  sheetTitle,
  trustMinutesOptions,
  trustScopeOptions,
  type Verdict,
} from '../decide'
import { t } from '../i18n'
import { inertSiblings } from '../modal'
import type { RequestView } from '../types'

interface Props {
  /** 当前的请求（父组件重新拉取后会换成新的）。 */
  request: RequestView
  action: Verdict
  onClose: () => void
  /**
   * 决定成功时带更新后的请求。这条已经决定不了时（decide 或 propose 回 409、404，或面板开着时它有了结果）
   * 带 null 与要告诉用户的文案：父组件关面板、显示结果提示并重新加载。
   */
  onDone: (updated: RequestView | null, message?: string) => void
}

export function DecideSheet({ request, action, onClose, onDone }: Props) {
  const [s, dispatch] = useReducer(sheetReducer, undefined, () => initSheet(request, action))
  const [note, setNote] = useState('')
  const submitting = useRef(false)
  const dialog = useRef<HTMLDivElement>(null)
  const backdrop = useRef<HTMLDivElement>(null)
  const onDoneRef = useRef(onDone)
  useEffect(() => {
    onDoneRef.current = onDone
  }, [onDone])

  // 打开时：背后的页面、顶栏、底部导航设为 inert（Tab、Enter 到不了背后的批准、拒绝），焦点给面板（Esc 可关），背后不跟着滚。
  // 关闭时恢复，焦点还给打开面板的按钮；决定之后那个按钮已经不在了，由页面把焦点给结果提示
  useEffect(() => {
    const el = dialog.current
    const opener = document.activeElement
    const siblings = el?.parentElement ? Array.from(el.parentElement.children).filter((c): c is HTMLElement => c instanceof HTMLElement) : []
    const restore = inertSiblings(siblings, [el, backdrop.current].filter((c): c is HTMLDivElement => c !== null))
    el?.focus()
    document.body.classList.add('sheet-open')
    return () => {
      document.body.classList.remove('sheet-open')
      restore()
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus({ preventScroll: true })
    }
  }, [])

  // 开着“记住”时按当前 (动作, 范围) 取草案。序号变了（换范围、关掉再开、重试）就重新取；旧回应由 alive 与序号双重丢弃
  const { remember, seq, scope } = s
  useEffect(() => {
    if (!remember) return
    let alive = true
    hub.propose(request.id, { action: s.action, scope }).then(
      (draft) => {
        if (alive) dispatch({ type: 'draft', seq, draft })
      },
      (e: unknown) => {
        if (!alive) return
        const c = classifyError(e, t('生成草案失败'))
        if (c.gone) onDoneRef.current(null, c.message)
        else dispatch({ type: 'draft_error', seq, message: c.message, retryable: retryable(e) })
      },
    )
    return () => {
      alive = false
    }
    // s.action 在面板的一生里不变（换动作时父组件换一个面板），不列进依赖
  }, [remember, seq, scope, request.id])

  // 面板开着时这条有了结果（终端里处理了、过期、机器吊销，或上次提交超时其实已生效……）。自己提交中不算：等 decide 的结果
  useEffect(() => {
    if (request.state !== 'pending' && !s.busy && !submitting.current) onDoneRef.current(null, goneMessage(request, s.action))
  }, [request, s.busy])

  async function confirm() {
    const body = decideBody(s, note)
    if (!body || submitting.current) return
    submitting.current = true
    dispatch({ type: 'submit' })
    try {
      const updated = await hub.decide(request.id, body)
      onDoneRef.current(updated)
    } catch (e) {
      const c = classifyError(e, t('决定失败'))
      if (c.gone) onDoneRef.current(null, c.message)
      else dispatch({ type: 'failed', message: c.message })
    } finally {
      submitting.current = false
    }
  }

  const allow = action === 'allow'
  const block = rememberBlock(request)
  const shown = draftShown(s)
  const close = () => {
    if (!s.busy) onClose()
  }

  return (
    <>
      <div className="sheet-backdrop" onClick={close} ref={backdrop} />
      <div
        className="sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="sheet-title"
        tabIndex={-1}
        ref={dialog}
        onKeyDown={(e) => {
          if (isEscape({ key: e.key, isComposing: e.nativeEvent.isComposing, keyCode: e.keyCode })) close()
        }}
      >
        <h2 id="sheet-title">{sheetTitle(action, request)}</h2>
        <label className="field">
          <span>{noteHint(request)}</span>
          <textarea className="input" value={note} maxLength={1000} disabled={s.busy} onChange={(e) => setNote(e.target.value)} />
        </label>
        <label className="switch">
          <span>
            {allow ? t('记住：以后同类的自动放行') : t('记住：以后同类的自动拒绝')}
            {block && <span className="sub line">{block}</span>}
          </span>
          <input
            type="checkbox"
            checked={s.remember}
            disabled={!s.rememberable || s.busy}
            onChange={(e) => dispatch({ type: 'remember', on: e.target.checked })}
          />
        </label>
        {s.remember && (
          <div className="draft">
            <div className="row">
              <span>{t('范围')}</span>
              <span className="seg" role="group" aria-label={t('记住的范围')}>
                {scopeOptions(request).map((o) => (
                  <button
                    key={o.scope}
                    type="button"
                    className={s.scope === o.scope ? 'on' : ''}
                    aria-pressed={s.scope === o.scope}
                    disabled={o.disabled || s.busy}
                    onClick={() => dispatch({ type: 'scope', scope: o.scope })}
                  >
                    {o.label}
                  </button>
                ))}
              </span>
            </div>
            {s.draftError && (
              <div className="error" role="alert">
                {s.draftError}
                {s.draftRetryable && (
                  <button type="button" className="btn small" onClick={() => dispatch({ type: 'retry' })}>
                    {t('重试')}
                  </button>
                )}
              </div>
            )}
            {shown && (
              <>
                <p className="draft-summary">
                  {draftLead(shown.summary.action, shown.summary.payload)} <code className="mono">{shown.summary.anchor}</code>
                  {draftScope(shown.scope, request.project)}
                </p>
                <details>
                  <summary>{t('规则原文 {id}', { id: shown.rule_id })}</summary>
                  <pre className="block mono">{shown.yaml}</pre>
                </details>
              </>
            )}
            {!shown && !s.draftError && <div className="sub">{t('正在生成草案…')}</div>}
          </div>
        )}
        {s.trustable && (
          <label className="switch">
            <span>
              {t('接下来一段时间都放行')}
              <span className="sub line">{t('这段时间里这个范围内的请求一律放行，不再问你；到期自动失效，设置页可随时撤销。')}</span>
            </span>
            <input type="checkbox" checked={s.trust} disabled={s.busy} onChange={(e) => dispatch({ type: 'trust', on: e.target.checked })} />
          </label>
        )}
        {s.trust && (
          <div className="draft">
            <div className="row">
              <span>{t('范围')}</span>
              <span className="seg" role="group" aria-label={t('放行的范围')}>
                {trustScopeOptions(request).map((o) => (
                  <button
                    key={o.scope}
                    type="button"
                    className={s.trustScope === o.scope ? 'on' : ''}
                    aria-pressed={s.trustScope === o.scope}
                    disabled={o.disabled || s.busy}
                    onClick={() => dispatch({ type: 'trust_scope', scope: o.scope })}
                  >
                    {o.label}
                  </button>
                ))}
              </span>
            </div>
            <div className="row">
              <span>{t('时长')}</span>
              <span className="seg" role="group" aria-label={t('放行的时长')}>
                {trustMinutesOptions.map((m) => (
                  <button
                    key={m}
                    type="button"
                    className={s.trustMinutes === m ? 'on' : ''}
                    aria-pressed={s.trustMinutes === m}
                    disabled={s.busy}
                    onClick={() => dispatch({ type: 'trust_minutes', minutes: m })}
                  >
                    {minutesLabel(m)}
                  </button>
                ))}
              </span>
            </div>
          </div>
        )}
        {s.error && (
          <div className="error" role="alert">
            {s.error}
          </div>
        )}
        <div className="btn-row">
          <button type="button" className="btn" onClick={close} disabled={s.busy}>
            {t('取消')}
          </button>
          <button
            type="button"
            data-testid="confirm"
            className={`btn ${action === 'allow' ? 'primary' : 'danger'}`}
            onClick={() => void confirm()}
            disabled={!canConfirm(s)}
          >
            {s.busy ? t('正在提交…') : s.remember ? (allow ? t('批准并记住') : t('拒绝并记住')) : allow ? t('批准') : t('拒绝')}
          </button>
        </div>
      </div>
    </>
  )
}
