import { useEffect, useReducer, useRef, useState } from 'react'
import { hub } from '../api'
import {
  canConfirm,
  classifyError,
  decideBody,
  draftShown,
  goneMessage,
  initSheet,
  noteHint,
  rememberBlock,
  retryable,
  scopeOptions,
  sheetReducer,
  type Verdict,
} from '../decide'
import type { RequestView } from '../types'

interface Props {
  /** 当前的请求（父组件重新拉取后会换成新的）。 */
  request: RequestView
  action: Verdict
  onClose: () => void
  /**
   * 决定成功时带更新后的请求。这条已经决定不了时（decide 或 propose 回 409、404，或面板开着时它在别处有了结果）
   * 带 null 与要告诉用户的文案：父组件关面板、显示文案并重新加载。
   */
  onDone: (updated: RequestView | null, message?: string) => void
}

const payloadNames: Record<string, string> = { shell: '命令', file_write: '文件改动', network: '访问', mcp: 'MCP 调用' }

export function DecideSheet({ request, action, onClose, onDone }: Props) {
  const [s, dispatch] = useReducer(sheetReducer, undefined, () => initSheet(request, action))
  const [note, setNote] = useState('')
  const submitting = useRef(false)
  const dialog = useRef<HTMLDivElement>(null)
  const onDoneRef = useRef(onDone)
  useEffect(() => {
    onDoneRef.current = onDone
  }, [onDone])

  // 打开时把焦点给面板（Esc 可关），背后的页面不跟着滚
  useEffect(() => {
    dialog.current?.focus()
    document.body.classList.add('sheet-open')
    return () => document.body.classList.remove('sheet-open')
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
        const c = classifyError(e, '生成草案失败')
        if (c.gone) onDoneRef.current(null, c.message)
        else dispatch({ type: 'draft_error', seq, message: c.message, retryable: retryable(e) })
      },
    )
    return () => {
      alive = false
    }
    // s.action 在面板的一生里不变（换动作时父组件换一个面板），不列进依赖
  }, [remember, seq, scope, request.id])

  // 面板开着时这条在别处有了结果（终端里处理了、过期、机器吊销……）。自己提交中不算：等 decide 的结果
  useEffect(() => {
    if (request.state !== 'pending' && !s.busy && !submitting.current) onDoneRef.current(null, goneMessage(request))
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
      const c = classifyError(e, '决定失败')
      if (c.gone) onDoneRef.current(null, c.message)
      else dispatch({ type: 'failed', message: c.message })
    } finally {
      submitting.current = false
    }
  }

  const verb = action === 'allow' ? '批准' : '拒绝'
  const what = payloadNames[request.payload.type] ?? (request.kind === 'webhook' ? '请求' : '操作')
  const block = rememberBlock(request)
  const shown = draftShown(s)
  const close = () => {
    if (!s.busy) onClose()
  }

  return (
    <>
      <div className="sheet-backdrop" onClick={close} />
      <div
        className="sheet"
        role="dialog"
        aria-modal="true"
        aria-labelledby="sheet-title"
        tabIndex={-1}
        ref={dialog}
        onKeyDown={(e) => {
          if (e.key === 'Escape') close()
        }}
      >
        <h2 id="sheet-title">
          {verb}这次{what}
        </h2>
        <label className="field">
          <span>{noteHint(request)}</span>
          <textarea className="input" value={note} maxLength={1000} disabled={s.busy} onChange={(e) => setNote(e.target.value)} />
        </label>
        <label className="switch">
          <span>
            记住：以后同类的{action === 'allow' ? '自动放行' : '自动拒绝'}
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
              <span>范围</span>
              <span className="seg" role="group" aria-label="记住的范围">
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
                    重试
                  </button>
                )}
              </div>
            )}
            {shown && (
              <>
                <p className="draft-summary">
                  将{shown.summary.action === 'allow' ? '放行' : '拒绝'}
                  {payloadNames[shown.summary.payload] ?? ''} <code className="mono">{shown.summary.anchor}</code>
                  {shown.scope === 'project' ? `（仅项目 ${request.project}）` : '（所有项目）'}
                </p>
                <details>
                  <summary>规则原文 {shown.rule_id}</summary>
                  <pre className="block mono">{shown.yaml}</pre>
                </details>
              </>
            )}
            {!shown && !s.draftError && <div className="sub">正在生成草案…</div>}
          </div>
        )}
        {s.error && (
          <div className="error" role="alert">
            {s.error}
          </div>
        )}
        <div className="btn-row">
          <button type="button" className="btn" onClick={close} disabled={s.busy}>
            取消
          </button>
          <button
            type="button"
            data-testid="confirm"
            className={`btn ${action === 'allow' ? 'primary' : 'danger'}`}
            onClick={() => void confirm()}
            disabled={!canConfirm(s)}
          >
            {s.busy ? '正在提交…' : s.remember ? `${verb}并记住` : verb}
          </button>
        </div>
      </div>
    </>
  )
}
