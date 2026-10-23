import { afterEach, describe, expect, it, vi } from 'vitest'
import { getSubscription, isIOS, isStandalone, pushSupported, registerServiceWorker, subscribe, urlBase64ToUint8Array } from './pwa'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('urlBase64ToUint8Array', () => {
  it('解 base64url，补位与否都行', () => {
    expect([...urlBase64ToUint8Array('BAEC')]).toEqual([4, 1, 2])
    expect([...urlBase64ToUint8Array('BAE')]).toEqual([4, 1])
    expect([...urlBase64ToUint8Array('BAE=')]).toEqual([4, 1])
    expect([...urlBase64ToUint8Array('-_8')]).toEqual([251, 255])
  })
})

function stubWindow(over: { standalone?: boolean; push?: boolean } = {}) {
  vi.stubGlobal('window', {
    matchMedia: (q: string) => ({ matches: q === '(display-mode: standalone)' && over.standalone === true }),
    ...(over.push === false ? {} : { PushManager: function PushManager() {}, Notification: function Notification() {} }),
  })
}

describe('环境判断', () => {
  it('isStandalone：iOS 的 navigator.standalone 或 display-mode: standalone', () => {
    stubWindow()
    vi.stubGlobal('navigator', { standalone: true })
    expect(isStandalone()).toBe(true)
    vi.stubGlobal('navigator', {})
    expect(isStandalone()).toBe(false)
    stubWindow({ standalone: true })
    expect(isStandalone()).toBe(true)
  })

  it('isIOS：iPhone，或自称 Mac 但有触屏的 iPad', () => {
    vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)', platform: 'iPhone', maxTouchPoints: 5 })
    expect(isIOS()).toBe(true)
    vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)', platform: 'MacIntel', maxTouchPoints: 5 })
    expect(isIOS()).toBe(true)
    vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)', platform: 'MacIntel', maxTouchPoints: 0 })
    expect(isIOS()).toBe(false)
    vi.stubGlobal('navigator', { userAgent: 'Mozilla/5.0 (Linux; Android 15)', platform: 'Linux armv8l', maxTouchPoints: 5 })
    expect(isIOS()).toBe(false)
  })

  it('pushSupported 看 PushManager，不看 Service Worker 是否注册', () => {
    vi.stubGlobal('navigator', { serviceWorker: {} })
    stubWindow()
    expect(pushSupported()).toBe(true)
    stubWindow({ push: false }) // iOS 的 Safari 标签页：有 serviceWorker，没有 PushManager
    expect(pushSupported()).toBe(false)
    vi.stubGlobal('navigator', {})
    stubWindow()
    expect(pushSupported()).toBe(false)
  })
})

describe('Service Worker 与订阅', () => {
  function stubSW(opts: { permission?: NotificationPermission; registered?: boolean; registerFails?: boolean; existing?: unknown } = {}) {
    const log: string[] = []
    const subscribeArgs: PushSubscriptionOptionsInit[] = []
    const pushManager = {
      getSubscription: async () => {
        log.push('getSubscription')
        return opts.existing ?? null
      },
      subscribe: async (o: PushSubscriptionOptionsInit) => {
        log.push('subscribe')
        subscribeArgs.push(o)
        return { endpoint: 'https://push.example/x' }
      },
    }
    const reg = { pushManager }
    let registered = opts.registered ?? true
    const serviceWorker = {
      register: async (url: string) => {
        log.push(`register ${url}`)
        if (opts.registerFails) throw new Error('nope')
        registered = true
        return reg
      },
      getRegistration: async () => {
        log.push('getRegistration')
        return registered ? reg : undefined
      },
      get ready() {
        log.push('ready')
        return Promise.resolve(reg)
      },
    }
    vi.stubGlobal('navigator', { serviceWorker })
    stubWindow()
    vi.stubGlobal('Notification', {
      requestPermission: async () => {
        log.push('requestPermission')
        return opts.permission ?? 'granted'
      },
    })
    return { log, subscribeArgs }
  }

  it('subscribe 的第一个调用是授权请求，且同步发出（iOS 要求直接来自用户手势）', async () => {
    const { log, subscribeArgs } = stubSW()
    const p = subscribe('BAEC')
    expect(log).toEqual(['requestPermission'])
    const sub = await p
    expect(sub.endpoint).toBe('https://push.example/x')
    expect(log[0]).toBe('requestPermission')
    expect(log).toContain('subscribe')
    expect(subscribeArgs[0]?.userVisibleOnly).toBe(true)
    expect([...(subscribeArgs[0]?.applicationServerKey as Uint8Array)]).toEqual([4, 1, 2])
  })

  it('没给通知权限时不订阅', async () => {
    const { log } = stubSW({ permission: 'denied' })
    await expect(subscribe('BAEC')).rejects.toThrow('没有获得通知权限')
    expect(log).toEqual(['requestPermission'])
  })

  it('还没注册 Service Worker 时先注册再订阅；注册失败给中文错误', async () => {
    let s = stubSW({ registered: false })
    await subscribe('BAEC')
    expect(s.log.slice(0, 3)).toEqual(['requestPermission', 'getRegistration', 'register /sw.js'])
    s = stubSW({ registered: false, registerFails: true })
    await expect(subscribe('BAEC')).rejects.toThrow('Service Worker 没有注册成功，刷新页面后再试')
    expect(s.log).not.toContain('subscribe')
  })

  it('getSubscription：没注册时回 null 而不是一直等 ready', async () => {
    let s = stubSW({ registered: false })
    await expect(getSubscription()).resolves.toBeNull()
    expect(s.log).not.toContain('ready')
    s = stubSW({ existing: { endpoint: 'https://push.example/old' } })
    await expect(getSubscription()).resolves.toEqual({ endpoint: 'https://push.example/old' })
    vi.stubGlobal('navigator', {})
    await expect(getSubscription()).resolves.toBeNull()
  })

  it('registerServiceWorker 注册 /sw.js，失败或不支持时回 null', async () => {
    let s = stubSW({ registered: false })
    await expect(registerServiceWorker()).resolves.not.toBeNull()
    expect(s.log).toEqual(['register /sw.js'])
    s = stubSW({ registerFails: true })
    await expect(registerServiceWorker()).resolves.toBeNull()
    vi.stubGlobal('navigator', {})
    await expect(registerServiceWorker()).resolves.toBeNull()
  })
})
