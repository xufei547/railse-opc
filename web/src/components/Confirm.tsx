import { useCallback, useEffect, useReducer, useRef, useState, type ReactNode } from 'react'
import { confirmClosed, confirmReducer, type ConfirmRequest, type ConfirmState } from '../confirm'
import { isEscape, type OutcomeView } from '../decide'
import { failure } from '../manage'
import { inertSiblings } from '../modal'
import { ResultToast } from './ResultToast'

/** 要确认的操作：确认后执行 run；成功时它回的提示（或 null 不提示）显示在底部。 */
export interface Ask extends ConfirmRequest {
  run: () => Promise<OutcomeView | null>
}

/**
 * 确认面板（role=alertdialog）：底部弹出，背后 inert、Esc 与点背景可关（提交中不能关），初始焦点在“取消”（破坏性操作的安全默认）。
 * 失败时留在面板上显示 hub 的原文，可以重试或取消。关闭后焦点还给打开它的按钮（还在的话）。
 */
export function ConfirmSheet({
  state,
  onCancel,
  onConfirm,
}: {
  state: ConfirmState
  onCancel: () => void
  onConfirm: () => void
}) {
  const dialog = useRef<HTMLDivElement>(null)
  const backdrop = useRef<HTMLDivElement>(null)
  const cancel = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    const el = dialog.current
    const opener = document.activeElement
    const siblings = el?.parentElement ? Array.from(el.parentElement.children).filter((c): c is HTMLElement => c instanceof HTMLElement) : []
    const restore = inertSiblings(siblings, [el, backdrop.current].filter((c): c is HTMLDivElement => c !== null))
    cancel.current?.focus()
    document.body.classList.add('sheet-open')
    return () => {
      document.body.classList.remove('sheet-open')
      restore()
      if (opener instanceof HTMLElement && opener.isConnected) opener.focus({ preventScroll: true })
    }
  }, [])

  const req = state.req
  if (!req) return null
  const close = () => {
    if (!state.busy) onCancel()
  }
  return (
    <>
      <div className="sheet-backdrop" onClick={close} ref={backdrop} />
      <div
        className="sheet"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby="confirm-title"
        aria-describedby="confirm-body"
        tabIndex={-1}
        ref={dialog}
        onKeyDown={(e) => {
          if (isEscape({ key: e.key, isComposing: e.nativeEvent.isComposing, keyCode: e.keyCode })) close()
        }}
      >
        <h2 id="confirm-title">{req.title}</h2>
        <p id="confirm-body" className="pre-wrap">
          {req.body}
        </p>
        {state.error && (
          <div className="error pre-wrap" role="alert">
            {state.error}
          </div>
        )}
        <div className="btn-row">
          <button type="button" className="btn" onClick={close} disabled={state.busy} ref={cancel}>
            取消
          </button>
          <button type="button" data-testid="confirm" className={`btn ${req.danger ? 'danger' : 'primary'}`} onClick={onConfirm} disabled={state.busy}>
            {state.busy ? '正在提交…' : req.confirmLabel}
          </button>
        </div>
      </div>
    </>
  )
}

export interface Actions {
  /** 先确认再做（删除、吊销、导入、暂停）。已有面板开着时不理。 */
  ask: (a: Ask) => void
  /** 不用确认、直接做（启停规则、恢复自动审批）；失败时底部红色提示 failTitle 与 hub 的原文。同一时刻只做一件。 */
  act: (run: () => Promise<OutcomeView | null>, failTitle: string) => Promise<void>
  /** 直接显示一条结果提示（如选的文件太大，没发出去）。 */
  notify: (v: OutcomeView) => void
  /** 有操作在进行（面板开着或在等结果）：页面上的操作按钮应当 disabled。 */
  busy: boolean
  /** 页面容器的 className：有结果提示时底部留白。 */
  pageClass: string
  /** 结果提示与确认面板，放在页面片段的最后（与顶栏、页面同级，面板打开时把它们设为 inert）。 */
  overlays: ReactNode
}

/** 规则、机器、设置页共用：确认面板与固定在底部导航之上的结果提示（页面滚得再远也看得见）。 */
export function useActions(): Actions {
  const [s, dispatch] = useReducer(confirmReducer<Ask>, confirmClosed)
  const [result, setResult] = useState<OutcomeView | null>(null)
  const [acting, setActing] = useState(false)
  const running = useRef(false)
  const toast = useRef<HTMLDivElement>(null)

  // 有了结果就把焦点给提示（读屏会读出来；删掉、吊销之后原来的按钮可能已经不在了）
  useEffect(() => {
    if (result) toast.current?.focus({ preventScroll: true })
  }, [result])

  const ask = useCallback((a: Ask) => {
    if (running.current) return
    setResult(null)
    dispatch({ type: 'open', req: a })
  }, [])

  const act = useCallback(async (run: () => Promise<OutcomeView | null>, failTitle: string) => {
    if (running.current) return
    running.current = true
    setActing(true)
    setResult(null)
    try {
      const v = await run()
      if (v) setResult(v)
    } catch (e) {
      const m = failure(e, '操作失败')
      if (m) setResult({ tone: 'warn', title: failTitle, text: m })
    } finally {
      running.current = false
      setActing(false)
    }
  }, [])

  const req = s.req
  async function confirm() {
    if (!req || running.current) return
    running.current = true
    dispatch({ type: 'submit' })
    try {
      const v = await req.run()
      dispatch({ type: 'done' })
      if (v) setResult(v)
    } catch (e) {
      const m = failure(e, '操作失败')
      if (m === null) dispatch({ type: 'done' }) // 401：正在跳登录页
      else dispatch({ type: 'failed', message: m })
    } finally {
      running.current = false
    }
  }

  return {
    ask,
    act,
    notify: setResult,
    busy: acting || req !== null,
    pageClass: result ? 'page has-toast' : 'page',
    overlays: (
      <>
        {result && <ResultToast view={result} onClose={() => setResult(null)} ref={toast} />}
        {req && <ConfirmSheet state={s} onCancel={() => dispatch({ type: 'cancel' })} onConfirm={() => void confirm()} />}
      </>
    ),
  }
}
