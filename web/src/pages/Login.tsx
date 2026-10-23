import { useState, type FormEvent } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { ApiError, hub } from '../api'
import { safeNext } from '../auth'

export function Login() {
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const navigate = useNavigate()
  const [params] = useSearchParams()

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await hub.login(password)
      navigate(safeNext(params.get('next')), { replace: true })
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) setError('密码不对')
      else if (err instanceof ApiError && err.status === 503) setError('hub 还没有设置密码，或正忙，请稍后再试；没设过密码的请在服务器上运行 railse hub password')
      else if (err instanceof ApiError && err.status === 429) setError('试得太频繁，一分钟后再试')
      else setError(err instanceof Error ? err.message : '登录失败')
    } finally {
      setBusy(false)
    }
  }

  return (
    <form className="login" onSubmit={submit}>
      <h1>Railse</h1>
      <label className="field">
        <span>密码</span>
        <input className="input" type="password" autoComplete="current-password" autoFocus value={password} onChange={(e) => setPassword(e.target.value)} />
      </label>
      {error && <div className="error" role="alert">{error}</div>}
      <button className="btn primary block" type="submit" disabled={busy || password === ''}>
        {busy ? '正在登录…' : '登录'}
      </button>
    </form>
  )
}
