import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from './api'
import {
  classify,
  disablePush,
  enablePush,
  keyChanged,
  LOGOUT_PUSH_WAIT,
  logoutDevice,
  probe,
  testPush,
  type ClassifyInput,
  type LogoutApi,
  type PushApi,
  type PushEnv,
  type PushStatus,
} from './push'
import type { VapidInfo } from './types'

const base = { supported: true, ios: false, standalone: true, hubEnabled: true, permission: 'default' as NotificationPermission, hasSubscription: false, hubActive: false }

describe('classify', () => {
  it('按优先级判断推送状态', () => {
    expect(classify({ ...base, supported: false })).toBe('unsupported')
    expect(classify({ ...base, supported: false, ios: true, standalone: false })).toBe('need_standalone')
    expect(classify({ ...base, ios: true, standalone: false })).toBe('need_standalone')
    expect(classify({ ...base, hubEnabled: false })).toBe('hub_disabled')
    expect(classify({ ...base, permission: 'denied' })).toBe('denied')
    expect(classify(base)).toBe('off')
    expect(classify({ ...base, permission: 'granted', hasSubscription: true, hubActive: true })).toBe('on')
    expect(classify({ ...base, permission: 'granted', hasSubscription: true, hubActive: false })).toBe('stale')
    expect(classify({ ...base, permission: 'granted', hasSubscription: false })).toBe('off')
  })

  it('没查成（连不上 hub）时是 unknown，不误报失效；本机就能定的照常给出', () => {
    expect(classify({ ...base, hubEnabled: null })).toBe('unknown')
    expect(classify({ ...base, hubEnabled: null, permission: 'denied' })).toBe('denied')
    expect(classify({ ...base, hubEnabled: null, supported: false })).toBe('unsupported')
    expect(classify({ ...base, hubEnabled: null, ios: true, standalone: false })).toBe('need_standalone')
    expect(classify({ ...base, permission: 'granted', hasSubscription: true, hubActive: null })).toBe('unknown')
    // 没有订阅时不看 hubActive
    expect(classify({ ...base, permission: 'granted', hasSubscription: false, hubActive: null })).toBe('off')
  })

  it('iOS 主屏幕应用、电脑浏览器照常判断；iPad 的 Safari 标签页要先加到主屏幕', () => {
    expect(classify({ ...base, ios: true, standalone: true, permission: 'granted', hasSubscription: true, hubActive: true })).toBe('on')
    expect(classify({ ...base, ios: false, standalone: false })).toBe('off')
    expect(classify({ ...base, ios: true, standalone: false, permission: 'granted', hasSubscription: true, hubActive: true })).toBe('need_standalone')
  })

  it('穷举全部输入：总有结果，且每种结果的条件都成立', () => {
    const bools = [true, false]
    const tri = [true, false, null]
    const perms: NotificationPermission[] = ['default', 'granted', 'denied']
    const valid: PushStatus[] = ['unsupported', 'need_standalone', 'hub_disabled', 'denied', 'off', 'on', 'stale', 'unknown']
    let n = 0
    for (const supported of bools)
      for (const ios of bools)
        for (const standalone of bools)
          for (const hubEnabled of tri)
            for (const permission of perms)
              for (const hasSubscription of bools)
                for (const hubActive of tri) {
                  const i: ClassifyInput = { supported, ios, standalone, hubEnabled, permission, hasSubscription, hubActive }
                  const s = classify(i)
                  n++
                  expect(valid).toContain(s)
                  const pwaOK = standalone || !ios
                  // iOS 的 Safari 标签页一律先引导加到主屏幕
                  expect(s === 'need_standalone').toBe(ios && !standalone)
                  if (!pwaOK) continue
                  expect(s === 'unsupported').toBe(!supported)
                  if (!supported) continue
                  expect(s === 'hub_disabled').toBe(hubEnabled === false)
                  if (hubEnabled === false) continue
                  expect(s === 'denied').toBe(permission === 'denied')
                  if (permission === 'denied') continue
                  const subscribed = hasSubscription && permission === 'granted'
                  expect(s === 'on').toBe(hubEnabled === true && subscribed && hubActive === true)
                  expect(s === 'stale').toBe(hubEnabled === true && subscribed && hubActive === false)
                  expect(s === 'unknown').toBe(hubEnabled === null || (subscribed && hubActive === null))
                  expect(s === 'off').toBe(hubEnabled === true && !subscribed)
                }
    expect(n).toBe(2 * 2 * 2 * 3 * 3 * 2 * 3)
  })

  it('“开启通知”只会出现在能推送、不是 iOS 标签页、hub 启用且没被拒绝时', () => {
    for (const supported of [true, false])
      for (const ios of [true, false])
        for (const standalone of [true, false])
          for (const permission of ['default', 'granted', 'denied'] as NotificationPermission[])
            for (const hubActive of [true, false, null]) {
              const s = classify({ supported, ios, standalone, hubEnabled: true, permission, hasSubscription: true, hubActive })
              if (s === 'off' || s === 'stale') {
                expect(supported && (standalone || !ios)).toBe(true)
                expect(permission).not.toBe('denied')
              }
            }
  })
})

// 0x04 0x01 0x02 的 base64url 是 BAEC
const key = (...b: number[]) => new Uint8Array(b).buffer

describe('keyChanged', () => {
  it('订阅用的公钥与 hub 现在的不同才算换过钥', () => {
    expect(keyChanged(key(4, 1, 2), 'BAEC')).toBe(false)
    expect(keyChanged(key(4, 1, 3), 'BAEC')).toBe(true)
    expect(keyChanged(key(4, 1), 'BAEC')).toBe(true)
    expect(keyChanged(key(4, 1, 2, 0), 'BAEC')).toBe(true)
  })

  it('浏览器不给旧公钥、hub 没给公钥或公钥读不出时不下结论（交给 hub 的检查）', () => {
    expect(keyChanged(null, 'BAEC')).toBe(false)
    expect(keyChanged(undefined, 'BAEC')).toBe(false)
    expect(keyChanged(key(4, 1, 2), '')).toBe(false)
    expect(keyChanged(key(4, 1, 2), '!!!')).toBe(false)
  })
})

interface FakeSub {
  endpoint: string
  options?: { applicationServerKey: ArrayBuffer | null }
  unsubscribed: boolean
}

function fakeSub(endpoint: string, appKey: ArrayBuffer | null | undefined = key(4, 1, 2), unsubscribeFails = false) {
  const s: FakeSub & { toJSON(): unknown; unsubscribe(): Promise<boolean> } = {
    endpoint,
    ...(appKey === undefined ? {} : { options: { applicationServerKey: appKey } }),
    unsubscribed: false,
    toJSON: () => ({ endpoint, keys: { p256dh: 'p', auth: 'a' } }),
    unsubscribe: async () => {
      if (unsubscribeFails) throw new Error('nope')
      s.unsubscribed = true
      return true
    },
  }
  return s
}

type Sub = ReturnType<typeof fakeSub>

function setup(
  o: {
    supported?: boolean
    ios?: boolean
    standalone?: boolean
    permission?: NotificationPermission
    sub?: Sub | null
    subError?: boolean
    newSub?: Sub
    subscribeError?: Error
    vapid?: VapidInfo | Error
    check?: boolean | Error
    subscribeHub?: Error
    unsubscribeHub?: Error
    test?: Error
  } = {},
) {
  const log: string[] = []
  const env: PushEnv = {
    supported: () => o.supported ?? true,
    ios: () => o.ios ?? false,
    standalone: () => o.standalone ?? true,
    permission: () => o.permission ?? 'granted',
    getSubscription: async () => {
      log.push('getSubscription')
      if (o.subError) throw new Error('boom')
      return (o.sub ?? null) as unknown as PushSubscription | null
    },
    subscribe: (k: string) => {
      log.push(`subscribe ${k}`)
      if (o.subscribeError) return Promise.reject(o.subscribeError)
      return Promise.resolve((o.newSub ?? fakeSub('https://push.example/new')) as unknown as PushSubscription)
    },
  }
  const api: PushApi = {
    pushVapid: async () => {
      log.push('vapid')
      const v = o.vapid ?? { enabled: true, public_key: 'BAEC', reason: '' }
      if (v instanceof Error) throw v
      return v
    },
    pushSubscribe: async (sub: unknown) => {
      log.push(`hub.subscribe ${JSON.stringify(sub)}`)
      if (o.subscribeHub) throw o.subscribeHub
    },
    pushUnsubscribe: async (endpoint: string) => {
      log.push(`hub.unsubscribe ${endpoint}`)
      if (o.unsubscribeHub) throw o.unsubscribeHub
    },
    pushCheck: async (endpoint: string) => {
      log.push(`hub.check ${endpoint}`)
      const c = o.check ?? true
      if (c instanceof Error) throw c
      return { active: c }
    },
    pushTest: async (endpoint: string) => {
      log.push(`hub.test ${endpoint}`)
      if (o.test) throw o.test
    },
  }
  return { env, api, log }
}

describe('probe', () => {
  it('不支持推送、iOS 的 Safari 标签页：不发任何请求', async () => {
    let s = setup({ supported: false })
    expect(await probe(s.env, s.api)).toEqual({ status: 'unsupported', reason: '', publicKey: '', endpoint: null })
    expect(s.log).toEqual([])
    s = setup({ ios: true, standalone: false, supported: false })
    expect((await probe(s.env, s.api)).status).toBe('need_standalone')
    expect(s.log).toEqual([])
  })

  it('hub 停用推送：给出 hub 的原因，不查订阅', async () => {
    const s = setup({ vapid: { enabled: false, public_key: '', reason: 'hub 没有配置 --public-url，推送停用' }, sub: fakeSub('https://push.example/a') })
    expect(await probe(s.env, s.api)).toEqual({
      status: 'hub_disabled',
      reason: 'hub 没有配置 --public-url，推送停用',
      publicKey: '',
      endpoint: 'https://push.example/a',
    })
    expect(s.log.filter((l) => l.startsWith('hub.'))).toEqual([])
  })

  it('读不到 hub 的推送配置：unknown，原因是接口的中文错误；401 时不另给原因', async () => {
    let s = setup({ vapid: new ApiError(0, '连不上 hub，请检查网络') })
    expect(await probe(s.env, s.api)).toMatchObject({ status: 'unknown', reason: '连不上 hub，请检查网络', publicKey: '' })
    s = setup({ vapid: new ApiError(401, '请先登录') })
    expect(await probe(s.env, s.api)).toMatchObject({ status: 'unknown', reason: '' })
    // 本机已知被拒：照样说被拒
    s = setup({ vapid: new ApiError(0, '连不上 hub，请检查网络'), permission: 'denied' })
    expect((await probe(s.env, s.api)).status).toBe('denied')
  })

  it('没有订阅：off，拿到公钥，不查 hub', async () => {
    const s = setup({ permission: 'default' })
    expect(await probe(s.env, s.api)).toEqual({ status: 'off', reason: '', publicKey: 'BAEC', endpoint: null })
    expect(s.log).toEqual(['vapid', 'getSubscription'])
  })

  it('有订阅：用只读的 check 问 hub，active 为 on、否则 stale（不重新登记）', async () => {
    let s = setup({ sub: fakeSub('https://push.example/a'), check: true })
    expect(await probe(s.env, s.api)).toEqual({ status: 'on', reason: '', publicKey: 'BAEC', endpoint: 'https://push.example/a' })
    expect(s.log).toEqual(['vapid', 'getSubscription', 'hub.check https://push.example/a'])
    s = setup({ sub: fakeSub('https://push.example/a'), check: false })
    expect((await probe(s.env, s.api)).status).toBe('stale')
    expect(s.log.some((l) => l.startsWith('hub.subscribe'))).toBe(false)
  })

  it('订阅用的是旧公钥（hub 换过钥）：直接 stale，不必问 hub', async () => {
    const s = setup({ sub: fakeSub('https://push.example/a', key(9, 9, 9)), check: true })
    expect((await probe(s.env, s.api)).status).toBe('stale')
    expect(s.log).not.toContain('hub.check https://push.example/a')
  })

  it('浏览器不给订阅的公钥：照常问 hub', async () => {
    const s = setup({ sub: fakeSub('https://push.example/a', undefined), check: true })
    expect((await probe(s.env, s.api)).status).toBe('on')
  })

  it('查订阅失败（连不上）：unknown 而不是失效', async () => {
    const s = setup({ sub: fakeSub('https://push.example/a'), check: new ApiError(0, '请求超时：hub 没有响应，请检查网络') })
    expect(await probe(s.env, s.api)).toMatchObject({ status: 'unknown', reason: '请求超时：hub 没有响应，请检查网络', publicKey: 'BAEC' })
  })

  it('有订阅但权限不是 granted：不问 hub', async () => {
    const s = setup({ sub: fakeSub('https://push.example/a'), permission: 'default' })
    expect((await probe(s.env, s.api)).status).toBe('off')
    expect(s.log).toEqual(['vapid', 'getSubscription'])
    const d = setup({ sub: fakeSub('https://push.example/a'), permission: 'denied' })
    expect((await probe(d.env, d.api)).status).toBe('denied')
  })

  it('读本机订阅出错当作没有订阅', async () => {
    const s = setup({ subError: true, permission: 'granted' })
    expect((await probe(s.env, s.api)).status).toBe('off')
  })
})

describe('enablePush', () => {
  it('第一个调用就是订阅（其中第一句是授权请求），同步发出，之前没有任何网络请求', async () => {
    const s = setup()
    const p = enablePush('BAEC', null, s.env, s.api)
    expect(s.log).toEqual(['subscribe BAEC'])
    await p
    expect(s.log).toEqual(['subscribe BAEC', 'hub.subscribe {"endpoint":"https://push.example/new","keys":{"p256dh":"p","auth":"a"}}'])
  })

  it('没有公钥时不请求授权', async () => {
    const s = setup()
    await expect(enablePush('', null, s.env, s.api)).rejects.toThrow('还没拿到 hub 的推送公钥')
    expect(s.log).toEqual([])
  })

  it('订阅失败（没给权限等）原样抛出，不登记', async () => {
    const s = setup({ subscribeError: new Error('没有获得通知权限') })
    await expect(enablePush('BAEC', null, s.env, s.api)).rejects.toThrow('没有获得通知权限')
    expect(s.log).toEqual(['subscribe BAEC'])
  })

  it('hub 没收下（推送停用 503 等）：撤掉本机刚订的，抛出 hub 的原文', async () => {
    const newSub = fakeSub('https://push.example/new')
    const s = setup({ newSub, subscribeHub: new ApiError(503, 'hub 没有配置 --public-url，推送停用') })
    await expect(enablePush('BAEC', null, s.env, s.api)).rejects.toThrow('hub 没有配置 --public-url，推送停用')
    expect(newSub.unsubscribed).toBe(true)
  })

  it('撤本机订阅也失败时，抛的仍是 hub 的错误', async () => {
    const s = setup({ newSub: fakeSub('https://push.example/new', key(4, 1, 2), true), subscribeHub: new ApiError(400, '订阅无效：auth 必须是 16 字节') })
    await expect(enablePush('BAEC', null, s.env, s.api)).rejects.toThrow('订阅无效：auth 必须是 16 字节')
  })

  it('换了新端点：顺手让 hub 删掉旧的；删不掉也算开启成功', async () => {
    let s = setup()
    await enablePush('BAEC', 'https://push.example/old', s.env, s.api)
    expect(s.log.at(-1)).toBe('hub.unsubscribe https://push.example/old')
    s = setup({ unsubscribeHub: new ApiError(0, '连不上 hub，请检查网络') })
    await expect(enablePush('BAEC', 'https://push.example/old', s.env, s.api)).resolves.toBeUndefined()
    // 端点没变：不删
    s = setup()
    await enablePush('BAEC', 'https://push.example/new', s.env, s.api)
    expect(s.log.some((l) => l.startsWith('hub.unsubscribe'))).toBe(false)
  })
})

describe('disablePush', () => {
  it('先让 hub 删，再退本机订阅', async () => {
    const sub = fakeSub('https://push.example/a')
    const s = setup({ sub })
    await disablePush(s.env, s.api)
    expect(s.log).toEqual(['getSubscription', 'hub.unsubscribe https://push.example/a'])
    expect(sub.unsubscribed).toBe(true)
  })

  it('hub 删不掉（连不上）也退本机订阅：hub 那条下次推送时会被推送服务回 410 删掉', async () => {
    const sub = fakeSub('https://push.example/a')
    const s = setup({ sub, unsubscribeHub: new ApiError(0, '连不上 hub，请检查网络') })
    await disablePush(s.env, s.api)
    expect(sub.unsubscribed).toBe(true)
  })

  it('没有订阅时什么也不做', async () => {
    const s = setup()
    await disablePush(s.env, s.api)
    expect(s.log).toEqual(['getSubscription'])
  })
})

describe('logoutDevice', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  /** setup 的 api 加上 logout；登出时记下本机订阅退了没有。 */
  function withLogout(s: ReturnType<typeof setup>, sub?: Sub, over: Partial<LogoutApi> = {}, logoutError?: Error): LogoutApi {
    return {
      pushUnsubscribe: s.api.pushUnsubscribe,
      logout: async () => {
        s.log.push(sub ? `logout (local unsubscribed: ${sub.unsubscribed})` : 'logout')
        if (logoutError) throw logoutError
      },
      ...over,
    }
  }

  it('先让 hub 删掉这条订阅、退掉本机订阅，再退出登录（DELETE 要登录 cookie）', async () => {
    const sub = fakeSub('https://push.example/a')
    const s = setup({ sub })
    await logoutDevice(s.env, withLogout(s, sub))
    expect(s.log).toEqual(['getSubscription', 'hub.unsubscribe https://push.example/a', 'logout (local unsubscribed: true)'])
  })

  it('没有订阅：只退出登录', async () => {
    const s = setup()
    await logoutDevice(s.env, withLogout(s))
    expect(s.log).toEqual(['getSubscription', 'logout'])
  })

  it('关推送失败（hub 删不掉、本机退不掉、读不到订阅）不挡退出登录', async () => {
    const sub = fakeSub('https://push.example/a', key(4, 1, 2), true)
    let s = setup({ sub, unsubscribeHub: new ApiError(0, '连不上 hub，请检查网络') })
    await logoutDevice(s.env, withLogout(s, sub))
    expect(s.log).toEqual(['getSubscription', 'hub.unsubscribe https://push.example/a', 'logout (local unsubscribed: false)'])
    s = setup({ subError: true })
    await logoutDevice(s.env, withLogout(s))
    expect(s.log).toEqual(['getSubscription', 'logout'])
  })

  it('hub 那边卡住：本机订阅照退，最多等 LOGOUT_PUSH_WAIT 毫秒就退出登录', async () => {
    vi.useFakeTimers()
    const sub = fakeSub('https://push.example/a')
    const s = setup({ sub })
    const api = withLogout(s, sub, {
      pushUnsubscribe: (endpoint: string) => {
        s.log.push(`hub.unsubscribe ${endpoint}`)
        return new Promise<void>(() => {})
      },
    })
    const p = logoutDevice(s.env, api)
    await vi.advanceTimersByTimeAsync(LOGOUT_PUSH_WAIT - 1)
    expect(s.log).toEqual(['getSubscription', 'hub.unsubscribe https://push.example/a'])
    expect(sub.unsubscribed).toBe(true)
    await vi.advanceTimersByTimeAsync(1)
    await p
    expect(s.log.at(-1)).toBe('logout (local unsubscribed: true)')
  })

  it('读订阅卡住（Service Worker 一直没就绪）：等满时限后照常退出登录', async () => {
    vi.useFakeTimers()
    const s = setup()
    s.env.getSubscription = () => new Promise<PushSubscription | null>(() => {})
    const p = logoutDevice(s.env, withLogout(s), 500)
    await vi.advanceTimersByTimeAsync(499)
    expect(s.log).toEqual([])
    await vi.advanceTimersByTimeAsync(1)
    await p
    expect(s.log).toEqual(['logout'])
  })

  it('关推送很快时不留定时器；退出登录本身失败照抛（服务端会话还有效，不能假装已退出）', async () => {
    vi.useFakeTimers()
    const s = setup({ sub: fakeSub('https://push.example/a') })
    await logoutDevice(s.env, withLogout(s))
    expect(vi.getTimerCount()).toBe(0)
    const t = setup()
    await expect(logoutDevice(t.env, withLogout(t, undefined, {}, new ApiError(0, '连不上 hub，请检查网络')))).rejects.toThrow('连不上 hub')
  })
})

describe('testPush', () => {
  it('给本机订阅发测试通知', async () => {
    const s = setup({ sub: fakeSub('https://push.example/a') })
    await testPush(s.env, s.api)
    expect(s.log).toEqual(['getSubscription', 'hub.test https://push.example/a'])
  })

  it('没有订阅：中文错误，不发请求', async () => {
    const s = setup()
    await expect(testPush(s.env, s.api)).rejects.toThrow('这台设备还没有开启通知')
    expect(s.log).toEqual(['getSubscription'])
  })

  it('hub 的 404、502 原文抛出', async () => {
    let s = setup({ sub: fakeSub('https://push.example/a'), test: new ApiError(404, 'hub 没有这条订阅，请重新开启通知') })
    await expect(testPush(s.env, s.api)).rejects.toThrow('hub 没有这条订阅，请重新开启通知')
    s = setup({ sub: fakeSub('https://push.example/a'), test: new ApiError(502, '推送服务说这条订阅已失效，hub 已删掉它；请重新开启通知') })
    await expect(testPush(s.env, s.api)).rejects.toThrow('推送服务说这条订阅已失效，hub 已删掉它；请重新开启通知')
  })
})
