import { createContext, useCallback, useContext, useEffect, useMemo, useReducer, useRef, useState, type ReactNode } from 'react'
import { ApiError, hub } from './api'
import { coalesce } from './coalesce'
import { initialState, reduce, type Action, type State } from './store'
import { closeSessionInvalid, Live } from './ws'

interface Store {
  state: State
  dispatch: (a: Action) => void
  /**
   * 全量重新拉收件箱与状态。连上实时通道（含重连）、收到 request_new、实时通道断开、页面回到前台、网络恢复时都会自动调；
   * 同一时刻至多一次在拉，期间再要求的合并成结束后的一次。不会 reject（错误进 state.error，401 跳登录页）。
   */
  reload: () => Promise<void>
  loadRules: () => Promise<void>
  loadMachines: () => Promise<void>
}

const Ctx = createContext<Store | null>(null)

export function StoreProvider({ children, onUnauthorized }: { children: ReactNode; onUnauthorized: () => void }) {
  const [state, dispatch] = useReducer(reduce, initialState)
  const onUnauthorizedRef = useRef(onUnauthorized)
  useEffect(() => {
    onUnauthorizedRef.current = onUnauthorized
  }, [onUnauthorized])

  const [reloader] = useState(() =>
    coalesce(async () => {
      try {
        const [inbox, status] = await Promise.all([hub.inbox(), hub.status()])
        dispatch({ type: 'loaded', inbox, status })
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) return // 登录失效回调已在跳登录页
        dispatch({ type: 'error', error: e instanceof Error ? e.message : String(e) })
      }
    }),
  )
  const reload = useCallback(() => reloader(), [reloader])
  const loadRules = useCallback(async () => {
    dispatch({ type: 'rules', rules: await hub.rules() })
  }, [])
  const loadMachines = useCallback(async () => {
    dispatch({ type: 'machines', machines: await hub.machines() })
  }, [])

  useEffect(() => {
    let opened = false
    const live = new Live({
      onFrame: (frame) => {
        dispatch({ type: 'frame', frame })
        // hub 不单独推“问题不再等”：收到 request_new 就全量拉一次收件箱。
        // 其余帧若在一次拉取进行中到达，那次的结果可能比这帧旧，结束后再补拉一次。
        if (frame.type === 'request_new') void reloader()
        else reloader.dirty()
      },
      onOpen: () => {
        dispatch({ type: 'connected', connected: true })
        // 断开期间的帧都丢了：全量重拉，规则也标过期（规则页会重新拉）
        if (opened) dispatch({ type: 'rules_stale' })
        opened = true
        void reloader()
      },
      onDown: (code) => {
        dispatch({ type: 'connected', connected: false })
        if (code === closeSessionInvalid) {
          onUnauthorizedRef.current()
          return
        }
        // 握手被 401 拒时浏览器只给 1006，拉一次 REST 才知道是不是登录失效；也让断开期间的数据不至于太旧
        void reloader()
      },
    })
    void reloader()
    live.start()
    const wake = () => {
      live.wake()
      void reloader()
    }
    const onVisible = () => {
      if (document.visibilityState === 'visible') wake()
    }
    document.addEventListener('visibilitychange', onVisible)
    window.addEventListener('online', wake)
    return () => {
      live.stop()
      document.removeEventListener('visibilitychange', onVisible)
      window.removeEventListener('online', wake)
    }
  }, [reloader])

  const value = useMemo<Store>(() => ({ state, dispatch, reload, loadRules, loadMachines }), [state, reload, loadRules, loadMachines])
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

export function useStore(): Store {
  const s = useContext(Ctx)
  if (!s) throw new Error('useStore 必须在 StoreProvider 内使用')
  return s
}
