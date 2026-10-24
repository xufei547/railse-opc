import { useCallback, useLayoutEffect, useRef, type Ref } from 'react'
import type { OutcomeView } from '../decide'
import { toastSpace } from '../modal'

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
  const el = useRef<HTMLDivElement | null>(null)

  // 量出提示占掉的高度写进根元素的 --toast-space（CSSOM，不是 style 属性，CSP 允许），页面底部据此留白；
  // 提示变高变矮（文案变了、换行）、浮起与否、窗口尺寸变了都重量；卸载时去掉
  useLayoutEffect(() => {
    const node = el.current
    if (!node) return
    const root = document.documentElement
    const measure = () => {
      const nav = document.querySelector('.nav')
      const navTop = nav ? nav.getBoundingClientRect().top : window.innerHeight
      root.style.setProperty('--toast-space', `${toastSpace(navTop, node.getBoundingClientRect().top)}px`)
    }
    measure()
    const ro = typeof ResizeObserver === 'function' ? new ResizeObserver(measure) : null
    ro?.observe(node)
    window.addEventListener('resize', measure)
    return () => {
      ro?.disconnect()
      window.removeEventListener('resize', measure)
      root.style.removeProperty('--toast-space')
    }
  }, [raised, view.text])

  const setRef = useCallback(
    (node: HTMLDivElement | null) => {
      el.current = node
      if (typeof ref === 'function') ref(node)
      else if (ref) ref.current = node
    },
    [ref],
  )

  return (
    <div className={`toast banner ${view.tone}${raised ? ' raised' : ''}`} role={ok ? 'status' : 'alert'} aria-live={ok ? 'polite' : 'assertive'} tabIndex={-1} ref={setRef}>
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
