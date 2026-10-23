import { useCallback, useEffect, useRef, useState, type ChangeEvent } from 'react'
import { Link } from 'react-router'
import { hub } from '../api'
import { coalesce } from '../coalesce'
import { Banner } from '../components/Banner'
import { useActions } from '../components/Confirm'
import { elevatedEnabled, failure, fileSize, hitsLabel, importMax, sourceLabel } from '../manage'
import { useStore } from '../StoreProvider'
import type { Rule } from '../types'

/**
 * 一条规则：id、来源、能放行高风险操作的醒目红标（启用着的整张卡片红框）、命中情况、可展开的 YAML、启停开关；非内置的可删除。
 */
export function RuleCard({
  rule: r,
  busy,
  onToggle,
  onDelete,
}: {
  rule: Rule
  busy: boolean
  onToggle: (r: Rule) => void
  onDelete: (r: Rule) => void
}) {
  const cls = ['card', 'rule', r.elevated && r.enabled ? 'elevated' : '', r.enabled ? '' : 'off'].filter(Boolean).join(' ')
  return (
    <div className={cls}>
      <div className="rule-id mono">{r.id}</div>
      <div className="tags">
        <span className="tag plain">{sourceLabel(r.source)}</span>
        {r.elevated && <span className="tag high">可放行高风险操作</span>}
        {!r.enabled && <span className="tag plain">已停用</span>}
      </div>
      <div className="sub line">
        {hitsLabel(r)}
        {r.created_from && (
          <>
            ，来自请求 <Link to={`/r/${encodeURIComponent(r.created_from)}`}>{r.created_from.slice(0, 8)}</Link>
          </>
        )}
      </div>
      <details>
        <summary>规则原文</summary>
        <pre className="block mono">{r.yaml}</pre>
      </details>
      <div className="rule-actions">
        <label className="toggle">
          <input type="checkbox" role="switch" checked={r.enabled} disabled={busy} aria-label={`启用规则 ${r.id}`} onChange={() => onToggle(r)} />
          <span aria-hidden="true">启用</span>
        </label>
        {r.source !== 'builtin' && (
          <button type="button" className="btn danger-text" disabled={busy} aria-label={`删除规则 ${r.id}`} onClick={() => onDelete(r)}>
            删除
          </button>
        )}
      </div>
    </div>
  )
}

export function Rules() {
  const { state, loadRules } = useStore()
  const [error, setError] = useState<string | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)
  const actions = useActions()

  // 单飞、串行：页面自己的刷新、操作之后的刷新与 rules_updated 引起的刷新不会乱序覆盖。不会 reject（错误进 error）
  const [sync] = useState(() => coalesce(loadRules))
  const refresh = useCallback(async () => {
    try {
      await sync()
      setError(null)
    } catch (e) {
      const m = failure(e, '加载失败')
      if (m) setError(m)
    }
  }, [sync])
  // 每次进来都拉（命中次数会变）；规则可能变了（rules_updated、重连后）时再拉
  useEffect(() => {
    void refresh()
  }, [refresh])
  const stale = state.rulesStale
  useEffect(() => {
    if (stale) void refresh()
  }, [stale, refresh])

  function toggle(r: Rule) {
    const enable = !r.enabled
    if (enable && r.elevated) {
      actions.ask({
        title: '启用这条高风险规则？',
        body: `${r.id}\n它能自动放行对外或不可逆的操作（推送代码、发消息、删除文件等）。启用后立即下发给所有机器，匹配的请求不再问你。`,
        confirmLabel: '启用',
        danger: true,
        run: async () => {
          await hub.setRuleEnabled(r.id, true)
          await refresh()
          return { tone: 'ok', title: '已启用', text: `规则 ${r.id} 已启用，所有机器会立即收到更新` }
        },
      })
      return
    }
    void actions.act(async () => {
      await hub.setRuleEnabled(r.id, enable)
      await refresh()
      return null // 开关本身已经反映了结果
    }, enable ? '启用失败' : '停用失败')
  }

  function remove(r: Rule) {
    actions.ask({
      title: '删除这条规则？',
      body: `${r.id}\n删除后立即下发给所有机器，不能撤销；要找回只能重新导入。`,
      confirmLabel: '删除',
      danger: true,
      run: async () => {
        await hub.deleteRule(r.id)
        await refresh()
        return { tone: 'ok', title: '已删除', text: `规则 ${r.id} 已删除，所有机器会立即收到更新` }
      },
    })
  }

  function pick(e: ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = '' // 同一个文件改过之后可以再选
    if (!file) return
    if (file.size > importMax) {
      actions.notify({ tone: 'warn', title: '没有导入', text: `${file.name} 有 ${fileSize(file.size)}，规则文件至多 1 MiB` })
      return
    }
    const n = state.rules?.length
    actions.ask({
      title: '导入并整套替换规则？',
      body:
        `用 ${file.name}（${fileSize(file.size)}）整套替换 hub 上现有的${n === undefined ? '' : ` ${n} 条`}规则：文件里没有的规则都会被删掉。` +
        `导入后立即下发给所有机器。\n建议先点“导出”留一份备份。`,
      confirmLabel: '导入并替换',
      danger: true,
      run: async () => {
        // 文件原样发给 hub：解析与校验以 hub 为准，失败时面板上显示 hub 的原文
        const { version } = await hub.importRules(file)
        await refresh()
        return { tone: 'ok', title: '已导入', text: `规则已整套替换并下发给所有机器（版本 ${version}）` }
      },
    })
  }

  const rules = state.rules
  const elevated = rules ? elevatedEnabled(rules) : 0
  return (
    <>
      <header className="topbar">
        <h1>规则</h1>
        {/* 同源链接带 cookie，hub 回附件 railse-rules.yaml；不用 blob:/data: 地址（CSP 不许） */}
        <a className="btn" href={hub.exportRulesURL} download="railse-rules.yaml">
          导出
        </a>
        <button type="button" className="btn" disabled={actions.busy} onClick={() => fileRef.current?.click()}>
          导入
        </button>
        {/* 不设 accept：iOS 认不出 .yaml 时会把所有文件置灰；选错了文件 hub 会说明 */}
        <input ref={fileRef} type="file" className="visually-hidden" tabIndex={-1} aria-hidden="true" onChange={pick} />
      </header>
      <div className={actions.pageClass}>
        {error && (
          <Banner tone="warn">
            {rules ? `刷新失败：${error}` : error}
            <button type="button" className="btn" onClick={() => void refresh()}>
              重试
            </button>
          </Banner>
        )}
        {elevated > 0 && (
          <Banner tone="warn">
            有 {elevated} 条启用着的规则能自动放行对外或不可逆的操作（下面标红的），匹配的请求不会再问你。请确认每一条都是你要的。
          </Banner>
        )}
        <p className="sub">新规则来自“批准并记住”或导入 YAML；这里只做查看、启停和删除。</p>
        {rules?.map((r) => (
          <RuleCard key={r.id} rule={r} busy={actions.busy} onToggle={toggle} onDelete={remove} />
        ))}
        {rules === null && !error && <div className="empty">加载中…</div>}
        {rules?.length === 0 && <div className="empty">没有规则</div>}
      </div>
      {actions.overlays}
    </>
  )
}
