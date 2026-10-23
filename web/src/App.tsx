import { useEffect, useRef, useState, type ReactNode } from 'react'
import { BrowserRouter, NavLink, Route, Routes, useNavigate } from 'react-router'
import { ApiError, hub, setUnauthorizedHandler } from './api'
import { createLoginRedirect } from './auth'
import { StoreProvider, useStore } from './StoreProvider'
import { Login } from './pages/Login'
import { Inbox } from './pages/Inbox'
import { RequestPage } from './pages/Request'
import { History } from './pages/History'
import { Rules } from './pages/Rules'
import { Machines } from './pages/Machines'
import { Settings } from './pages/Settings'

/**
 * 首屏先确认登录有效。之后任何接口回 401、或实时通道以 1008 关闭（登录失效），都跳 /login?next=<当时所在的位置>，
 * 登录后回到那里。
 */
function RequireAuth({ children }: { children: ReactNode }) {
  const [ok, setOk] = useState(false)
  const navigate = useNavigate()
  // useNavigate 在 BrowserRouter 下每次换页都换一个函数；跳的是绝对路径，用哪一个都一样，存起来让 toLogin 保持不变
  const navigateRef = useRef(navigate)
  useEffect(() => {
    navigateRef.current = navigate
  }, [navigate])
  // 幂等：并发的几个 401 会连着调它，已经在登录页（next 已带上）时什么也不做
  const [toLogin] = useState(() =>
    createLoginRedirect(
      () => window.location.pathname + window.location.search,
      (to) => navigateRef.current(to, { replace: true }),
    ),
  )

  useEffect(() => {
    let alive = true
    setUnauthorizedHandler(toLogin)
    hub.status().then(
      () => alive && setOk(true),
      (e: unknown) => {
        if (!alive) return
        // 401 时登录失效回调已经在跳登录页；别的错误（连不上等）先进外壳，由页面显示
        if (!(e instanceof ApiError && e.status === 401)) setOk(true)
      },
    )
    return () => {
      alive = false
      setUnauthorizedHandler(null)
    }
  }, [toLogin])

  if (!ok) return <div className="empty">正在连接 hub…</div>
  return <StoreProvider onUnauthorized={toLogin}>{children}</StoreProvider>
}

function Nav() {
  const { state } = useStore()
  const n = state.pending.length + state.questions.length
  const cls = ({ isActive }: { isActive: boolean }) => (isActive ? 'active' : '')
  return (
    <nav className="nav">
      <NavLink to="/" end className={cls}>
        收件箱{n > 0 && <span className="badge" aria-label={`${n} 条等你处理`}>{n > 99 ? '99+' : n}</span>}
      </NavLink>
      <NavLink to="/history" className={cls}>历史</NavLink>
      <NavLink to="/rules" className={cls}>规则</NavLink>
      <NavLink to="/machines" className={cls}>机器</NavLink>
      <NavLink to="/settings" className={cls}>设置</NavLink>
    </nav>
  )
}

function Shell() {
  return (
    <div className="app">
      <Routes>
        <Route path="/" element={<Inbox />} />
        <Route path="/r/:id" element={<RequestPage />} />
        <Route path="/history" element={<History />} />
        <Route path="/rules" element={<Rules />} />
        <Route path="/machines" element={<Machines />} />
        <Route path="/settings" element={<Settings />} />
        <Route path="*" element={<div className="empty">没有这个页面</div>} />
      </Routes>
      <Nav />
    </div>
  )
}

export function App() {
  return (
    <BrowserRouter>
      <Routes>
        <Route path="/login" element={<Login />} />
        <Route
          path="*"
          element={
            <RequireAuth>
              <Shell />
            </RequireAuth>
          }
        />
      </Routes>
    </BrowserRouter>
  )
}
