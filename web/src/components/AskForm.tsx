import { useEffect, useId, useReducer, useRef } from 'react'
import { hub } from '../api'
import {
  answerFor,
  askQuestions,
  askReducer,
  canAnswer,
  initAsk,
  questionOptions,
  sendAsk,
  submitBody,
  type AskAction,
  type AskState,
  type AskVerb,
} from '../ask'
import type { Question, RequestView } from '../types'

// 题面、选项、答案都按纯文本渲染（React 转义），从不用 dangerouslySetInnerHTML；含 <redacted> 的照常显示（告警在说明卡之前）。

interface ViewProps {
  questions: Question[]
  state: AskState
  dispatch: (a: AskAction) => void
  onAnswer: () => void
  onPassthrough: () => void
}

/**
 * 答题表单的样子（状态由调用方给，便于不带浏览器环境测试）：每题一组（role=group，名字是题面），标题小标签、题面、
 * 选项（单选 radio、多选 checkbox，名字是标签、说明是 aria-describedby）、“自己填”输入框；底部“回答”（每题都有答案才可点）
 * 与次要的“让它在终端问我”。提交中全部 disabled；失败时按钮上方显示原因（role=alert），可以改了再试。
 */
export function AskFormView({ questions, state: s, dispatch, onAnswer, onPassthrough }: ViewProps) {
  const id = useId()
  const busy = s.busy !== null
  return (
    <div className="ask">
      {questions.map((q, i) => {
        const multi = q.multiSelect === true
        const picked = s.picked[i] ?? []
        return (
          <div key={i} className="question" role="group" aria-labelledby={`${id}-q${i}`}>
            {(q.header || multi) && (
              <div className="tags">
                {q.header && <span className="tag plain">{q.header}</span>}
                {multi && <span className="sub">可多选</span>}
              </div>
            )}
            <p className="q-text" id={`${id}-q${i}`}>
              {q.question}
            </p>
            {questionOptions(q).map((o, j) => (
              <label key={j} className="option">
                <input
                  type={multi ? 'checkbox' : 'radio'}
                  name={`${id}-q${i}`}
                  value={j}
                  checked={picked.includes(j)}
                  disabled={busy}
                  aria-labelledby={`${id}-q${i}-o${j}`}
                  aria-describedby={o.description ? `${id}-q${i}-d${j}` : undefined}
                  onChange={(e) => dispatch({ type: 'pick', question: i, option: j, multi, on: e.target.checked })}
                />
                <span>
                  <span className="label" id={`${id}-q${i}-o${j}`}>
                    {o.label}
                  </span>
                  {o.description && (
                    <span className="sub line" id={`${id}-q${i}-d${j}`}>
                      {o.description}
                    </span>
                  )}
                </span>
              </label>
            ))}
            <label className="field">
              <span>自己填</span>
              <input
                className="input"
                type="text"
                maxLength={1000}
                value={s.custom[i] ?? ''}
                disabled={busy}
                onChange={(e) => dispatch({ type: 'custom', question: i, text: e.target.value })}
              />
            </label>
          </div>
        )
      })}
      {s.error && (
        <div className="error" role="alert">
          {s.error}
        </div>
      )}
      <button type="button" className="btn primary block" onClick={onAnswer} disabled={!canAnswer(questions, s)}>
        {s.busy === 'answer' ? '正在提交…' : '回答'}
      </button>
      <button type="button" className="link-btn" onClick={onPassthrough} disabled={busy}>
        {s.busy === 'passthrough' ? '正在交回终端…' : '让它在终端问我'}
      </button>
    </div>
  )
}

interface Props {
  /** 待答的提问。 */
  request: RequestView
  /**
   * 提交有了结果：成功时带作答后的请求；这条已经答不了（409 已处理、已过期、机器已吊销，404 没有这条）时带 null 与 hub 的文案。
   * 都带上这次做的是回答还是交回终端，父组件据此显示结果提示（与决定面板同一套）并重新加载。
   */
  onDone: (updated: RequestView | null, message: string | undefined, attempted: AskVerb) => void
}

/**
 * 详情页上的答题表单（不是弹出面板：题目可能很长，直接放在页面里）。回答发 { answers }（键是题面原样），
 * “让它在终端问我”发 { passthrough: true }；同一时刻只提交一次。答了之后这条不再待答，父组件换成结果，表单随之消失。
 */
export function AskForm({ request, onDone }: Props) {
  const questions = askQuestions(request)
  const [s, dispatch] = useReducer(askReducer, questions, initAsk)
  const submitting = useRef(false)
  const onDoneRef = useRef(onDone)
  useEffect(() => {
    onDoneRef.current = onDone
  }, [onDone])

  async function send(verb: AskVerb) {
    const body = submitBody(verb, questions, s)
    if (!body || s.busy || submitting.current) return
    submitting.current = true
    dispatch({ type: 'submit', verb })
    const r = await sendAsk(() => hub.answer(request.id, body))
    submitting.current = false
    if ('done' in r) {
      onDoneRef.current(r.done, undefined, verb)
      return
    }
    // 答不了（gone）时页面显示红色结果提示并重新加载，这条多半已不再待答、表单随之消失；万一还在，表单解除提交中、留着原因可以再试
    dispatch({ type: 'failed', message: 'gone' in r ? r.gone : r.failed })
    if ('gone' in r) onDoneRef.current(null, r.gone, verb)
  }

  return <AskFormView questions={questions} state={s} dispatch={dispatch} onAnswer={() => void send('answer')} onPassthrough={() => void send('passthrough')} />
}

/** 提问结束后的题目与答案：每题一行“问题 → 你的答案”；没有答案的（交回终端、过期……）只列问题。 */
export function AskAnswers({ request: r }: { request: RequestView }) {
  const questions = askQuestions(r)
  if (questions.length === 0) return null
  return (
    <ul className="answers">
      {questions.map((q, i) => {
        const a = answerFor(r, q)
        return (
          <li key={i}>
            <span className="q">{q.question}</span>
            {a !== undefined && (
              <>
                {' → '}
                <strong className="a">{a}</strong>
              </>
            )}
          </li>
        )
      })}
    </ul>
  )
}
