// 确认面板（删除规则、吊销机器、导入规则、暂停自动审批）的状态。纯函数；渲染与发请求在 components/Confirm.tsx。

export interface ConfirmRequest {
  title: string
  body: string
  confirmLabel: string
  /** 确认按钮用红色（删除、吊销、整套替换）。 */
  danger: boolean
}

export interface ConfirmState<T extends ConfirmRequest = ConfirmRequest> {
  /** 正在确认的内容；null 是没开。 */
  req: T | null
  /** 已确认、等结果：这时不能再确认，也不能关（结果出来前关掉会看不到成败）。 */
  busy: boolean
  /** 上次确认失败的原因（hub 的原文）；面板留着，可以重试或取消。 */
  error: string | null
}

export type ConfirmAction<T extends ConfirmRequest = ConfirmRequest> =
  | { type: 'open'; req: T }
  | { type: 'submit' }
  | { type: 'failed'; message: string }
  | { type: 'done' }
  | { type: 'cancel' }

export const confirmClosed: ConfirmState<never> = { req: null, busy: false, error: null }

/** 同一时刻只开一个：开着时再 open 不理（inertSiblings 只支持一个模态，不能叠）。 */
export function confirmReducer<T extends ConfirmRequest>(s: ConfirmState<T>, a: ConfirmAction<T>): ConfirmState<T> {
  switch (a.type) {
    case 'open':
      return s.req ? s : { req: a.req, busy: false, error: null }
    case 'submit':
      return s.req && !s.busy ? { ...s, busy: true, error: null } : s
    case 'failed':
      return s.req && s.busy ? { ...s, busy: false, error: a.message } : s
    case 'done':
      return confirmClosed
    case 'cancel':
      return s.busy ? s : confirmClosed
    default:
      return s
  }
}
