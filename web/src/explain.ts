// 审批页说明卡与收件箱卡片用的人话：把一条请求说成“谁想做什么、在哪、为什么要你决定、批了拒了会怎样”。
// 只为让人一眼看懂，不参与定级；该不该问你、风险几档，以 hub 给的 risk 为准。
import { clip, isRedacted } from './format'
import type { RequestView } from './types'

export interface Explanation {
  /** 整句：“<CLI> 想<做什么>”。 */
  headline: string
  /** 不带 CLI 名的“做什么”，收件箱卡片第一行用。 */
  what: string
  /** “项目 x，机器 y”，缺哪个省哪个，都没有为空串。 */
  where: string
  /** 为什么要你决定；question、webhook、done 为空串。 */
  why: string
  /** 批了、拒了、记住会怎样；已处理的与 done 为空数组。 */
  next: string[]
}

// 与 hub 的 cliNames（internal/hub/pushsvc.go）一致；空与不认识的叫 Agent。
const cliNames: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', gemini: 'Gemini CLI' }

export function cliLabel(cli: string): string {
  return cliNames[cli] ?? 'Agent'
}

/** 机器名去掉结尾的 .local（不分大小写）；整个名字就是 .local 时原样。同 hub 的 machineLabel。 */
export function machineLabel(name: string): string {
  const suffix = '.local'
  if (name.length > suffix.length && name.slice(-suffix.length).toLowerCase() === suffix) {
    return name.slice(0, -suffix.length)
  }
  return name
}

/**
 * 把命令按不在引号里的 &&、||、;、|、&、换行切成段，每段再切成词（去引号），去掉前导的环境变量赋值。
 * 只为写一句人话，不求完全符合 shell 语法：单引号里原样；双引号里与引号外的反斜杠转义下一个字符；$(…) 与反引号不展开。
 */
function segments(command: string): string[][] {
  const segs: string[][] = []
  let words: string[] = []
  let cur = ''
  let has = false
  const endWord = () => {
    if (has) words.push(cur)
    cur = ''
    has = false
  }
  const endSeg = () => {
    endWord()
    if (words.length > 0) segs.push(words)
    words = []
  }
  let quote = ''
  for (let i = 0; i < command.length; i++) {
    const c = command.charAt(i)
    if (quote === "'") {
      if (c === "'") quote = ''
      else cur += c
      continue
    }
    if (c === '\\' && i + 1 < command.length) {
      cur += command.charAt(++i)
      has = true
      continue
    }
    if (quote === '"') {
      if (c === '"') quote = ''
      else cur += c
      continue
    }
    if (c === "'" || c === '"') {
      quote = c
      has = true
    } else if (c === '&' || c === '|') {
      if (command.charAt(i + 1) === c) i++
      endSeg()
    } else if (c === ';' || c === '\n') {
      endSeg()
    } else if (/\s/.test(c)) {
      endWord()
    } else {
      cur += c
      has = true
    }
  }
  endSeg()
  return segs.map((ws) => {
    let k = 0
    while (k < ws.length && /^[A-Za-z_][A-Za-z0-9_]*=/.test(ws[k] ?? '')) k++
    return ws.slice(k)
  })
}

interface Match {
  rank: 1 | 2 | 3
  text: string
}

const sshArgOpts = new Set('BbcDEeFIiJLlmOoPpQRSWw'.split('').map((c) => '-' + c))
const installers = new Set(['npm', 'pnpm', 'yarn', 'bun'])

function installText(pkgs: string[]): Match {
  const names = pkgs.length > 3 ? `${pkgs.slice(0, 3).join('、')} 等 ${pkgs.length} 个` : pkgs.join('、')
  return { rank: 1, text: names ? `安装依赖 ${names}，要联网` : '安装依赖，要联网' }
}

const positional = (ws: string[]) => ws.filter((w) => !w.startsWith('-'))

function matchGit(args: string[]): Match | null {
  let i = 0
  while (i < args.length && args.at(i)?.startsWith('-')) i += args[i] === '-C' || args[i] === '-c' ? 2 : 1
  const sub = args[i]
  const rest = args.slice(i + 1)
  if (sub === 'reset') {
    return rest.includes('--hard') ? { rank: 3, text: '丢弃工作区的改动，不可恢复' } : null
  }
  if (sub !== 'push') return null
  let force = rest.some((w) => w === '--force' || w.startsWith('--force-with-lease') || /^-[A-Za-z]*f[A-Za-z]*$/.test(w))
  const pos = positional(rest)
  const remote = pos[0] ?? ''
  let ref = pos[1] ?? ''
  if (ref.startsWith('+')) {
    force = true
    ref = ref.slice(1)
  }
  if (ref.includes(':')) ref = ref.slice(ref.lastIndexOf(':') + 1)
  const where = remote && ref ? `（${remote} 的 ${ref}）` : remote ? `（${remote}）` : ''
  return force ? { rank: 3, text: `强制推送，会覆盖远程历史${where}` } : { rank: 2, text: `把代码推到远程仓库${where}` }
}

function matchSegment(words: string[]): Match | null {
  if (words.length === 0) return null
  const first = words[0] ?? ''
  const name = first.slice(first.lastIndexOf('/') + 1)
  const args = words.slice(1)
  const pos = positional(args)
  const deploy = (): Match => ({ rank: 2, text: `发布或部署到外部：${clip(words.join(' '), 80)}` })
  switch (name) {
    case 'git':
      return matchGit(args)
    case 'rm': {
      const dd = args.indexOf('--')
      const paths = [...positional(dd < 0 ? args : args.slice(0, dd)), ...(dd < 0 ? [] : args.slice(dd + 1))]
      return { rank: 3, text: paths.length ? `删除 ${clip(paths.join('、'), 80)}，删了不可恢复` : '删除文件，删了不可恢复' }
    }
    case 'sudo': {
      const cmd = args.join(' ')
      return { rank: 3, text: cmd ? `以管理员权限运行：${clip(cmd, 80)}` : '以管理员权限运行命令' }
    }
    case 'docker':
      return (pos[0] === 'system' && pos[1] === 'prune') || (pos[0] === 'volume' && (pos[1] === 'prune' || pos[1] === 'rm'))
        ? { rank: 3, text: '清理 Docker 数据，不可恢复' }
        : null
    case 'ssh': {
      let host = ''
      for (let i = 0; i < args.length; i++) {
        const a = args[i] ?? ''
        if (a.startsWith('-')) {
          if (sshArgOpts.has(a)) i++
        } else {
          host = a
          break
        }
      }
      return { rank: 2, text: host ? `连接远程机器 ${host}` : '连接远程机器' }
    }
    case 'curl':
    case 'wget': {
      const url = args.find((w) => /^[a-z][a-z0-9+.-]*:\/\//i.test(w))
      if (url) {
        try {
          const host = new URL(url).hostname
          if (host) return { rank: 1, text: `访问网址 ${host}` }
        } catch {
          // 网址解析不了，按没有网址
        }
      }
      return { rank: 1, text: `用 ${name} 访问网络` }
    }
    case 'vercel':
      return deploy()
    case 'fly':
    case 'flyctl':
      return pos[0] === 'deploy' ? deploy() : null
    case 'kubectl':
      return pos[0] === 'apply' ? deploy() : null
    case 'terraform':
      return pos[0] === 'apply' ? deploy() : null
    case 'gh':
      return (pos[0] === 'release' || pos[0] === 'pr') && pos[1] === 'create' ? deploy() : null
    case 'cargo':
      if (pos[0] === 'publish') return deploy()
      return pos[0] === 'add' || pos[0] === 'install' ? installText(pos.slice(1)) : null
    case 'pip':
    case 'pip3':
    case 'brew':
    case 'gem':
      return pos[0] === 'install' ? installText(pos.slice(1)) : null
    case 'go':
      return pos[0] === 'get' || pos[0] === 'install' ? installText(pos.slice(1)) : null
    case 'uv':
      if (pos[0] === 'add') return installText(pos.slice(1))
      return pos[0] === 'pip' && pos[1] === 'install' ? installText(pos.slice(2)) : null
    default:
      if (installers.has(name)) {
        if (pos[0] === 'publish') return deploy()
        return pos[0] === 'install' || pos[0] === 'i' || pos[0] === 'add' ? installText(pos.slice(1)) : null
      }
      return null
  }
}

/** 一条 shell 命令“做什么”：各段里取最要紧的一段（不可逆 > 对外 > 联网，一样高取靠前的）；都认不出时原样说运行命令。 */
export function shellAction(command: string): string {
  let best: Match | null = null
  for (const seg of segments(command)) {
    const m = matchSegment(seg)
    if (m && (!best || m.rank > best.rank)) best = m
  }
  if (best) return best.text
  const c = command.trim()
  return c ? `运行命令：${clip(c.replace(/\s+/g, ' '), 80)}` : '运行一条命令'
}

function permissionWhat(r: RequestView): string {
  const p = r.payload
  switch (p.type) {
    case 'shell':
      return shellAction(p.command ?? '')
    case 'file_write': {
      const path = p.path ?? ''
      if (!path) return '修改文件'
      if (r.risk === 'write_outside') return `修改项目外的文件 ${path}`
      if (r.risk === 'destructive') return `改敏感文件 ${path}（密钥、配置或开机自启）`
      return `修改文件 ${path}`
    }
    case 'network': {
      if (p.url) {
        try {
          const host = new URL(p.url).hostname
          if (host) return `访问 ${host}`
        } catch {
          // 网址解析不了，原样
        }
        return `访问 ${p.url}`
      }
      return r.tool_name ? `用 ${r.tool_name} 联网` : '联网'
    }
    case 'mcp':
      if (p.server && p.tool) return `调用 ${p.server} 的 ${p.tool}`
      if (p.server) return `调用 ${p.server} 的工具`
      return p.tool ? `调用 MCP 工具 ${p.tool}` : '调用 MCP 工具'
    default:
      return r.tool_name ? `使用工具 ${r.tool_name}` : '做一件事'
  }
}

const pausedLine = '自动审批已暂停，所有请求都会问你。'

function permissionWhy(r: RequestView): string {
  const paused = r.engine_reason.startsWith('自动审批已暂停')
  let reason: string
  switch (r.risk) {
    case 'read':
    case 'write_in_project':
      if (paused) return pausedLine
      reason = r.engine_reason.startsWith('命中规则')
        ? '本来可以自动放行，但有规则要求问你。'
        : '这类操作本来可以自动放行，但现在没有规则放行它，要你决定。'
      break
    case 'network':
      reason = '这条要联网。规则默认不自动放行联网操作，要你决定。'
      break
    case 'outward':
      reason = '这会影响仓库之外的人和系统（推送、发布、部署）。规则永远不会自动放行这类操作，要你决定。'
      break
    case 'destructive':
      reason = '这是不可逆的操作。规则永远不会自动放行，要你决定。'
      break
    case 'write_outside':
      reason = '要改项目目录之外的文件。规则默认不自动放行，要你决定。'
      break
    case 'unknown':
      reason = r.payload.type === 'shell' ? '这条命令不在已知名单里，规则判断不了，要你决定。' : '这个操作不在已知名单里，规则判断不了，要你决定。'
      break
    default:
      reason = '规则定不了，要你决定。'
  }
  return paused ? pausedLine + reason : reason
}

/** 把一条请求说成人话：整句、做什么、在哪、为什么要你决定、批了拒了会怎样。 */
export function explainRequest(r: RequestView): Explanation {
  const cli = cliLabel(r.cli)
  const where = [r.project && `项目 ${r.project}`, r.machine_name && `机器 ${machineLabel(r.machine_name)}`].filter(Boolean).join('，')
  switch (r.kind) {
    case 'question':
      return { headline: `${cli} 在终端等你回答`, what: '在终端等你回答', where, why: '', next: ['去那台机器的终端回答，这里只是提醒。'] }
    case 'done':
      return { headline: `${cli} 做完了这一轮`, what: '做完了这一轮', where, why: '', next: [] }
    case 'webhook': {
      const what = '工作流等你批准'
      return {
        headline: r.title ? `${what}：${clip(r.title, 80)}` : what,
        what,
        where,
        why: '',
        next: r.state === 'pending' ? ['批准或拒绝会回调给发起它的工作流。'] : [],
      }
    }
  }
  const what = permissionWhat(r)
  const next: string[] = []
  if (r.state === 'pending') {
    next.push('批准：agent 执行这一条，只这一次。', '拒绝：agent 收到拒绝，会换个做法或停下来。')
    if (!isRedacted(r)) next.push('勾「记住」可以存成规则，下次同类的不再问。')
  }
  return { headline: `${cli} 想${what}`, what, where, why: permissionWhy(r), next }
}
