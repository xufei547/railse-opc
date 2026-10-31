import { engineReasonLabel, riskLabel, riskTone } from '../format'
import type { Payload as PayloadT, RequestView } from '../types'

// 载荷、标题都按纯文本渲染（React 转义），从不用 dangerouslySetInnerHTML；长内容在 pre 里折行，不撑破手机布局。

function prettyArgs(args: unknown): string {
  if (args === undefined || args === null) return ''
  if (typeof args === 'string') {
    try {
      return JSON.stringify(JSON.parse(args), null, 2)
    } catch {
      return args
    }
  }
  return JSON.stringify(args, null, 2)
}

/**
 * 含被脱敏部分时的醒目告警（docs/hub.md：has_redacted 为 true 的请求，网页必须醒目告警）。放在内容之前。
 * 审批请求同时突出引擎按原文定的风险与理由：屏上的不是原文，定级却是按原文做的。
 */
export function RedactedWarning({ request: r }: { request: RequestView }) {
  let detail: string
  if (r.kind === 'permission' && r.payload.type === 'shell') {
    detail =
      '凭据等敏感值已换成 <redacted>。不在引号里的值只隐藏到空格或 shell 符号（; | & 等）为止；引号里的值、注释和 heredoc 正文整段隐藏，' +
      '可能连带盖住其中的命令。'
  } else if (r.kind === 'webhook') {
    detail = '标题或描述里的凭据等敏感值已换成 <redacted>。'
  } else {
    detail = '凭据等敏感值已换成 <redacted>。'
  }
  const where = r.kind === 'webhook' ? '去来源系统' : '去终端'
  return (
    <div className="banner warn loud" role="alert">
      <strong className="banner-title">含被隐藏的部分，下面展示的不是原文</strong>
      <p>{detail}</p>
      {r.kind === 'permission' && (
        <p>
          引擎是按原文定级的：<span className={`tag ${riskTone(r.risk)}`}>{riskLabel(r.risk)}</span>
          {r.engine_reason && <>，理由：{engineReasonLabel(r.engine_reason)}</>}
        </p>
      )}
      <p>
        拿不准就{where}核对原文再决定。{r.kind === 'permission' && '这类请求不能“记住”。'}
      </p>
    </div>
  )
}

/** 按载荷类型展示（审批请求的 payload，已脱敏）。 */
export function Payload({ payload: p }: { payload: PayloadT }) {
  switch (p.type) {
    case 'shell':
      return (
        <pre className="block mono">{p.command ?? ''}</pre>
      )
    case 'file_write':
      return (
        <>
          <dl className="kv">
            <dt>路径</dt>
            <dd className="mono">{p.path}</dd>
          </dl>
          {p.diff_preview && <pre className="block mono">{p.diff_preview}</pre>}
        </>
      )
    case 'network':
      return (
        <dl className="kv">
          <dt>请求</dt>
          <dd className="mono">
            {p.method ? p.method + ' ' : ''}
            {p.url}
          </dd>
        </dl>
      )
    case 'mcp':
      return (
        <>
          <dl className="kv">
            <dt>服务</dt>
            <dd className="mono">{p.server}</dd>
            <dt>工具</dt>
            <dd className="mono">{p.tool}</dd>
          </dl>
          {p.args !== undefined && p.args !== null && <pre className="block mono">{prettyArgs(p.args)}</pre>}
        </>
      )
    default:
      return <pre className="block mono">{JSON.stringify(p, null, 2)}</pre>
  }
}

/** 详情页的主体内容：按种类展示载荷、webhook 的标题与描述、问题文本。告警由 RequestSummary 放在说明卡之前。 */
export function RequestContent({ request: r }: { request: RequestView }) {
  return (
    <>
      {r.kind === 'permission' ? (
        <Payload payload={r.payload} />
      ) : (
        <>
          <p className="title-text">{r.title}</p>
          {/* webhook 的描述（来源给的 description，脱敏后存在 payload.diff_preview） */}
          {r.kind === 'webhook' && r.payload.diff_preview && <pre className="block">{r.payload.diff_preview}</pre>}
        </>
      )}
    </>
  )
}
