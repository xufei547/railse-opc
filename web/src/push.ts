// 网页推送：状态判断、开启、关闭、测试与订阅健康检查。浏览器与 hub 两侧都经参数传入（测试换成假的），usePush 接上真的。

import { useCallback, useEffect, useRef, useState } from 'react'
import { hub } from './api'
import type { OutcomeView } from './decide'
import { failure } from './manage'
import { getSubscription, isIOS, isStandalone, pushSupported, subscribe, urlBase64ToUint8Array } from './pwa'

/**
 * 这台设备的推送状态：
 * - need_standalone：iOS 的 Safari 标签页，要先加到主屏幕、从图标打开；
 * - unsupported：浏览器没有 PushManager；
 * - hub_disabled：hub 没配 --public-url；
 * - denied：通知权限被拒；
 * - off：还没开启（没有订阅，或有订阅但没授权）；
 * - on：本机有订阅、hub 也有；
 * - stale：本机有订阅但 hub 没有、说它坏了，或它用的是 hub 换钥前的公钥——新审批推不过来；
 * - unknown：没查成（连不上 hub 等），不下结论，免得把网络问题误报成失效。
 */
export type PushStatus = 'unsupported' | 'need_standalone' | 'hub_disabled' | 'denied' | 'off' | 'on' | 'stale' | 'unknown'

export interface ClassifyInput {
  supported: boolean
  ios: boolean
  standalone: boolean
  /** hub 是否启用推送；null 是没拿到 /api/push/vapid。 */
  hubEnabled: boolean | null
  permission: NotificationPermission
  hasSubscription: boolean
  /** hub 是否还有本机这条订阅（/api/push/check）；null 是没查成。没有订阅时不看。 */
  hubActive: boolean | null
}

/** 推送状态，按“先解决什么”的顺序判断。纯函数。 */
export function classify(i: ClassifyInput): PushStatus {
  if (i.ios && !i.standalone) return 'need_standalone'
  if (!i.supported) return 'unsupported'
  if (i.hubEnabled === false) return 'hub_disabled'
  if (i.permission === 'denied') return 'denied'
  if (i.hubEnabled === null) return 'unknown'
  if (i.hasSubscription && i.permission === 'granted') {
    if (i.hubActive === null) return 'unknown'
    return i.hubActive ? 'on' : 'stale'
  }
  return 'off'
}

/**
 * 本机订阅用的公钥（PushSubscription.options.applicationServerKey）与 hub 现在的公钥（base64url）不同，即 hub 换过钥。
 * 浏览器不给旧公钥、hub 没给公钥或公钥解不出时回 false：不下结论，交给 hub 的检查。
 */
export function keyChanged(current: ArrayBuffer | null | undefined, publicKey: string): boolean {
  if (!current || !publicKey) return false
  let want: Uint8Array
  try {
    want = urlBase64ToUint8Array(publicKey)
  } catch {
    return false
  }
  const have = new Uint8Array(current)
  return have.length !== want.length || have.some((b, i) => b !== want[i])
}

/** 浏览器一侧。 */
export interface PushEnv {
  supported(): boolean
  ios(): boolean
  standalone(): boolean
  permission(): NotificationPermission
  getSubscription(): Promise<PushSubscription | null>
  /** 第一句必须是 Notification.requestPermission()（见 pwa.subscribe）。 */
  subscribe(publicKey: string): Promise<PushSubscription>
}

/** hub 一侧：api.ts 里推送相关的几个接口。 */
export type PushApi = Pick<typeof hub, 'pushVapid' | 'pushSubscribe' | 'pushUnsubscribe' | 'pushCheck' | 'pushTest'>

export const browserEnv: PushEnv = {
  supported: pushSupported,
  ios: isIOS,
  standalone: isStandalone,
  permission: () => Notification.permission,
  getSubscription,
  subscribe,
}

export interface Probe {
  status: PushStatus
  /** hub_disabled 时是 hub 给的原因，unknown 时是没查成的原因（401 时为空：正在跳登录页）。 */
  reason: string
  /** hub 现在的 VAPID 公钥；推送停用或没拿到时为空。 */
  publicKey: string
  /** 本机现有订阅的端点。 */
  endpoint: string | null
}

/**
 * 查一次推送状态：hub 的推送配置（顺带拿好公钥，点“开启”时不必再等网络）、本机订阅，以及 hub 还有没有这条订阅。
 * 健康检查只用只读的 /api/push/check，不重新登记：同样的密钥再登记会把 hub 的连续失败计数清零，坏订阅就永远删不掉。
 * 不支持推送、要先加到主屏幕时不发请求。不会 reject。
 */
export async function probe(env: PushEnv, api: PushApi): Promise<Probe> {
  const supported = env.supported()
  const local = { supported, ios: env.ios(), standalone: env.standalone(), permission: supported ? env.permission() : ('default' as const) }
  const early = classify({ ...local, hubEnabled: null, hasSubscription: false, hubActive: null })
  if (early === 'need_standalone' || early === 'unsupported') return { status: early, reason: '', publicKey: '', endpoint: null }

  let hubEnabled: boolean | null = null
  let publicKey = ''
  let reason = ''
  try {
    const v = await api.pushVapid()
    hubEnabled = v.enabled
    publicKey = v.enabled ? v.public_key : ''
    reason = v.enabled ? '' : v.reason
  } catch (e) {
    reason = failure(e, '读不到 hub 的推送配置') ?? ''
  }

  let sub: PushSubscription | null = null
  try {
    sub = await env.getSubscription()
  } catch {
    sub = null
  }

  let hubActive: boolean | null = false
  if (sub && hubEnabled && local.permission === 'granted') {
    if (keyChanged(sub.options?.applicationServerKey, publicKey)) {
      // hub 换过钥：旧订阅签不上了。hub 换钥时会删掉全部订阅，这里兜住“换钥前一刻订的、hub 照收了”的情形
      hubActive = false
    } else {
      try {
        hubActive = (await api.pushCheck(sub.endpoint)).active
      } catch (e) {
        hubActive = null
        reason = failure(e, '查不到这台设备的订阅') ?? ''
      }
    }
  }
  const status = classify({ ...local, hubEnabled, hasSubscription: sub !== null, hubActive })
  return { status, reason: status === 'hub_disabled' || status === 'unknown' ? reason : '', publicKey, endpoint: sub?.endpoint ?? null }
}

/**
 * 开启通知：订阅并登记到 hub。第一个调用就是 env.subscribe（它的第一句是授权请求），前面没有任何 await：iOS 只认直接来自
 * 用户点击的授权请求，所以公钥要事先拉好（probe）传进来。
 * hub 没收下（推送停用 503、订阅无效 400、连不上）时撤掉本机刚订的，免得留下一条 hub 不知道的订阅（收件箱会误报失效），
 * 然后抛出 hub 的原文。换了新端点时顺手让 hub 删掉旧的（删不掉也不要紧：推送服务会对旧端点回 404/410，hub 随后删掉）。
 */
export async function enablePush(publicKey: string, oldEndpoint: string | null, env: PushEnv, api: PushApi): Promise<void> {
  if (!publicKey) throw new Error('还没拿到 hub 的推送公钥，请刷新页面后再试')
  const sub = await env.subscribe(publicKey)
  try {
    await api.pushSubscribe(sub.toJSON())
  } catch (e) {
    await sub.unsubscribe().catch(() => false)
    throw e
  }
  if (oldEndpoint && oldEndpoint !== sub.endpoint) await api.pushUnsubscribe(oldEndpoint).catch(() => undefined)
}

/**
 * 关闭通知：先让 hub 删掉这条订阅，再退本机订阅。hub 删不掉（连不上等）也退本机的：hub 那条下次推送时推送服务会回 404/410，
 * hub 随即删掉。
 */
export async function disablePush(env: PushEnv, api: PushApi): Promise<void> {
  const sub = await env.getSubscription()
  if (!sub) return
  await api.pushUnsubscribe(sub.endpoint).catch(() => undefined)
  await sub.unsubscribe()
}

/** 退出登录用到的 hub 接口。 */
export type LogoutApi = Pick<typeof hub, 'pushUnsubscribe' | 'logout'>

/** 退出登录前最多等多久（毫秒）关掉本机推送；超过就照常退出，关推送不能挡住退出。 */
export const LOGOUT_PUSH_WAIT = 3000

/**
 * 退出登录：先尽力关掉这台设备的推送，再 hub.logout()。推送订阅与登录会话是分开的，只退出登录的话 hub 会继续把审批摘要
 * 推到这台设备的锁屏上。
 * 关推送必须在退出之前（DELETE /api/push/subscribe 要登录 cookie）：让 hub 删掉这条订阅，同时退掉本机订阅（退掉后推送服务
 * 对这个端点回 404/410，hub 那条即使没删成，下次推送时也会删掉）。读订阅、hub、浏览器任何一步失败或卡住都不挡退出：
 * 最多等 wait 毫秒。hub.logout() 自己的错误照抛（服务端会话还有效，不能假装已经退出）。
 */
export async function logoutDevice(env: PushEnv, api: LogoutApi, wait = LOGOUT_PUSH_WAIT): Promise<void> {
  await within(stopPush(env, api), wait)
  await api.logout()
}

async function stopPush(env: PushEnv, api: Pick<PushApi, 'pushUnsubscribe'>): Promise<void> {
  const sub = await env.getSubscription()
  if (!sub) return
  await Promise.allSettled([api.pushUnsubscribe(sub.endpoint), sub.unsubscribe()])
}

/** 等 p 结束（成功失败都算），最多等 ms 毫秒。不会 reject。 */
async function within(p: Promise<unknown>, ms: number): Promise<void> {
  let timer: ReturnType<typeof setTimeout> | undefined
  const timeout = new Promise<void>((resolve) => {
    timer = setTimeout(resolve, ms)
  })
  try {
    await Promise.race([p.catch(() => undefined), timeout])
  } finally {
    clearTimeout(timer)
  }
}

/** 让 hub 给本机订阅发一条测试通知。hub 的 404（没有这条订阅）、502（推送服务没收下）、503（推送停用）原文抛出。 */
export async function testPush(env: PushEnv, api: PushApi): Promise<void> {
  const sub = await env.getSubscription()
  if (!sub) throw new Error('这台设备还没有开启通知')
  await api.pushTest(sub.endpoint)
}

export interface PushControl {
  /** checking：第一次检查还没出结果。 */
  status: PushStatus | 'checking'
  reason: string
  publicKey: string
  /** 开启、关闭或测试正在进行：按钮应当 disabled。 */
  busy: boolean
  /** 以下三个都不会 reject：回给人看的结果（401 时回 null：正在跳登录页；正忙时也回 null），结束后重新检查一次。 */
  enable(): Promise<OutcomeView | null>
  disable(): Promise<OutcomeView | null>
  test(): Promise<OutcomeView | null>
  refresh(): Promise<void>
}

const enabledView: OutcomeView = { tone: 'ok', title: '通知已开启', text: '新的审批与提问会推送到这台设备。可以发一条测试通知，确认收得到。' }
const disabledView: OutcomeView = { tone: 'ok', title: '通知已关闭', text: '这台设备不再收到推送；随时可以重新开启。' }
const testedView: OutcomeView = {
  tone: 'ok',
  title: '测试通知已发出',
  text: '几秒内这台设备应弹出“Railse：通知正常”。没有弹出的话，看看系统的专注模式与通知设置。',
}

/**
 * 推送状态与操作。挂载时、页面回到前台时（可能刚去系统设置里改了通知权限）检查一次；只用结果最新的那次检查。
 * 设置页与收件箱（失效横幅）都用它。
 */
export function usePush(env: PushEnv = browserEnv, api: PushApi = hub): PushControl {
  const [state, setState] = useState<Probe | null>(null)
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const latest = useRef<Probe | null>(null)
  const gen = useRef(0)

  const refresh = useCallback(async () => {
    const mine = ++gen.current
    const p = await probe(env, api)
    if (mine !== gen.current) return // 期间又发起了一次（或已卸载），以那次为准
    latest.current = p
    setState(p)
  }, [env, api])

  useEffect(() => {
    void refresh()
    const onVisibility = () => {
      if (document.visibilityState === 'visible') void refresh()
    }
    document.addEventListener('visibilitychange', onVisibility)
    return () => {
      gen.current++
      document.removeEventListener('visibilitychange', onVisibility)
    }
  }, [refresh])

  const settle = useCallback(
    async (pending: Promise<void>, ok: OutcomeView, failTitle: string, fallback: string): Promise<OutcomeView | null> => {
      busyRef.current = true
      setBusy(true)
      let view: OutcomeView | null
      try {
        await pending
        view = ok
      } catch (e) {
        // hub 的错误原文照给（503 的停用原因、测试的 404/502 文案）
        const m = failure(e, fallback)
        view = m === null ? null : { tone: 'warn', title: failTitle, text: m }
      }
      await refresh()
      busyRef.current = false
      setBusy(false)
      return view
    },
    [refresh],
  )

  const enable = useCallback((): Promise<OutcomeView | null> => {
    if (busyRef.current) return Promise.resolve(null)
    // 第一个调用：enablePush → env.subscribe → Notification.requestPermission()，之前没有 await 与网络请求（iOS 手势规则）
    const pending = enablePush(latest.current?.publicKey ?? '', latest.current?.endpoint ?? null, env, api)
    return settle(pending, enabledView, '没有开启通知', '开启失败')
  }, [env, api, settle])

  const disable = useCallback((): Promise<OutcomeView | null> => {
    if (busyRef.current) return Promise.resolve(null)
    return settle(disablePush(env, api), disabledView, '没有关闭通知', '关闭失败')
  }, [env, api, settle])

  const test = useCallback((): Promise<OutcomeView | null> => {
    if (busyRef.current) return Promise.resolve(null)
    return settle(testPush(env, api), testedView, '测试通知没有发出', '发送失败')
  }, [env, api, settle])

  return {
    status: state?.status ?? 'checking',
    reason: state?.reason ?? '',
    publicKey: state?.publicKey ?? '',
    busy,
    enable,
    disable,
    test,
    refresh,
  }
}
