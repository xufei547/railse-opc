// 登录跳转：401 或实时通道 1008（登录失效）时去 /login?next=<当前位置>，登录后回到 next。

const loginRoute = '/login'

/** 是否是登录页本身（不把它当 next，免得登录后又回到登录页）。 */
function isLogin(path: string): boolean {
  return path === loginRoute || path.startsWith(loginRoute + '?') || path.startsWith(loginRoute + '/')
}

/** 跳登录页的地址，带上当前位置（路径加查询）。当前在首页或登录页时不带 next。 */
export function loginPath(current: string): string {
  if (current === '' || current === '/' || isLogin(current)) return loginRoute
  return `${loginRoute}?next=${encodeURIComponent(current)}`
}

/**
 * 登录后要回到的位置：只收本站路径，其余（别的站点、协议相对地址 //host、反斜杠变体、相对路径、登录页本身）一律回首页。
 * 先按 URL 规范化（去掉制表符换行、把 \ 当 /、折叠 ./），再确认仍是同源、且不以 // 开头：
 * react-router 遇到 pushState 拒绝的跨源地址会退回 location.assign，不挡住就成了开放重定向。
 */
export function safeNext(next: string | null): string {
  if (!next || !next.startsWith('/')) return '/'
  const base = 'https://railse.invalid'
  let u: URL
  try {
    u = new URL(next, base)
  } catch {
    return '/'
  }
  if (u.origin !== base) return '/'
  const path = u.pathname + u.search + u.hash
  if (!path.startsWith('/') || path.startsWith('//') || isLogin(path)) return '/'
  return path
}

/**
 * 生成“跳登录页”的函数：从调用时的地址算 next；已经在登录页时什么也不做。
 * 必须幂等：并发的两个 401（如 inbox 与 status 一起拉）会连着调两次，第一次已把地址换成 /login?next=…，
 * 而 React 还没来得及卸载外壳、清掉回调；第二次若照算就成了不带 next 的 /login，登录后回不到原处。
 */
export function createLoginRedirect(getLocation: () => string, navigate: (to: string) => void): () => void {
  return () => {
    const current = getLocation()
    if (isLogin(current)) return
    navigate(loginPath(current))
  }
}
