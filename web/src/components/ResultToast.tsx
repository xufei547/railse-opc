import type { Ref } from 'react'
import type { OutcomeView } from '../decide'

/**
 * 决定结果的提示：固定在底部导航之上（让开安全区），不随页面滚动，所以载荷再长、在页面中部点的吸底按钮，结果也看得见。
 * 成功绿色、role=status；没有生效红色、role=alert。留到用户点“知道了”、再次打开面板或离开页面。
 */
export function ResultToast({
  view,
  onClose,
  raised = false,
  ref,
}: {
  view: OutcomeView
  onClose: () => void
  /** 页面底部还有吸底的批准、拒绝栏时（如 409 后重新拉取之前）浮在它上面，不挡住它。 */
  raised?: boolean
  ref?: Ref<HTMLDivElement>
}) {
  const ok = view.tone === 'ok'
  return (
    <div className={`toast banner ${view.tone}${raised ? ' raised' : ''}`} role={ok ? 'status' : 'alert'} aria-live={ok ? 'polite' : 'assertive'} tabIndex={-1} ref={ref}>
      <div className="toast-body">
        <strong className="banner-title">{view.title}</strong>
        <span>{view.text}</span>
      </div>
      <button type="button" className="btn" onClick={onClose}>
        知道了
      </button>
    </div>
  )
}
