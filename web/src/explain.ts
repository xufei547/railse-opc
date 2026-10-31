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
  /** 为什么要你决定；只在还等你决定的审批上有，question、webhook、done 与已处理的为空串。 */
  why: string
  /** 批了、拒了、记住会怎样；只在还能决定时（待决的审批与 webhook、question）有，其余为空数组。 */
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
 * 把命令按不在引号里的 &&、||、;、|、&、换行切成段，每段再切成词（去引号），去掉前导的环境变量赋值与 env、time、nohup、nice、caffeinate 这类包装
 * （照 internal/policy 的 stripWrappers：只剩一个词时不再剥，也不跳它们的选项）。
 * 重定向与它的目标、行尾反斜杠续行、heredoc（<<WORD、<<-WORD）的正文与结束行都不产生词，正文也不产生段。
 * 只为写一句人话，不求完全符合 shell 语法：单引号里原样；双引号里与引号外的反斜杠转义下一个字符；$(…) 与反引号不展开。
 */
/** words 是去掉赋值与包装后的词；piped 为真表示这一段紧跟在单个 | 之后，与前一段同属一条管道。 */
interface Segment {
  words: string[]
  piped: boolean
}

const wrappers = new Set(['env', 'time', 'nohup', 'nice', 'caffeinate'])

function segments(command: string): Segment[] {
  const segs: Segment[] = []
  let words: string[] = []
  let cur = ''
  let has = false
  let dropNext = false // 刚见到重定向符，下一个词是它的目标，不算词
  const heredocs: { word: string; strip: boolean }[] = [] // 本行已见到、正文从下一行开始的 heredoc，按出现顺序
  const endWord = () => {
    if (has) {
      if (!dropNext) words.push(cur)
      dropNext = false
    }
    cur = ''
    has = false
  }
  let piped = false // 正在拼的这一段是否紧跟在单个 | 之后
  const endSeg = () => {
    endWord()
    dropNext = false
    if (words.length > 0) {
      segs.push({ words, piped })
      piped = false
    }
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
    if (quote === '' && c === '\\' && command.charAt(i + 1) === '\n') {
      i++ // 行尾反斜杠续行：两个字符都不算
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
    } else if (c === '>' || c === '<') {
      // 重定向：前面全是数字的词是文件描述符（2>），一起丢；操作符与目标（粘着或下一个词）都不算词
      if (has && /^\d+$/.test(cur)) {
        cur = ''
        has = false
      } else {
        endWord()
      }
      const n = command.charAt(i + 1)
      if (c === '<' && n === '<' && command.charAt(i + 2) !== '<') {
        // heredoc：分隔词紧接着读完（引号与反斜杠只用来去掉，取字面）；正文留到本行结束后跳过
        let j = i + 2
        const strip = command.charAt(j) === '-'
        if (strip) j++
        while (command.charAt(j) === ' ' || command.charAt(j) === '\t') j++
        let word = ''
        let q = ''
        for (; j < command.length; j++) {
          const d = command.charAt(j)
          if (q) {
            if (d === q) q = ''
            else if (q === '"' && d === '\\' && j + 1 < command.length) word += command.charAt(++j)
            else word += d
          } else if (d === "'" || d === '"') q = d
          else if (d === '\\' && j + 1 < command.length) word += command.charAt(++j)
          else if (/[\s;&|<>()]/.test(d)) break
          else word += d
        }
        heredocs.push({ word, strip })
        i = j - 1
        continue
      }
      if (c === '>' ? n === '>' || n === '|' || n === '&' : n === '<' || n === '>' || n === '&') {
        i++
        if (c === '<' && n === '<' && command.charAt(i + 1) === '<') i++
      }
      dropNext = true
    } else if (c === '&' && command.charAt(i + 1) === '>') {
      endWord() // &> 与 &>> 是重定向，不是分隔符；交给下一轮的 >
    } else if (c === '&' || c === '|') {
      const double = command.charAt(i + 1) === c
      if (double) i++
      endSeg()
      piped = c === '|' && !double
    } else if (c === ';' || c === '\n') {
      endSeg()
      if (c === '\n' && heredocs.length > 0) {
        // 跳过各个 heredoc 的正文，直到恰好等于分隔词的一行（<<- 先去掉行首制表符），分隔行也跳过；没有结束行就吃到末尾
        let pos = i + 1
        for (const h of heredocs) {
          while (pos < command.length) {
            let nl = command.indexOf('\n', pos)
            if (nl < 0) nl = command.length
            const line = command.slice(pos, nl)
            pos = nl + 1
            if ((h.strip ? line.replace(/^\t+/, '') : line) === h.word) break
          }
        }
        heredocs.length = 0
        i = Math.min(pos, command.length) - 1
      }
    } else if (/\s/.test(c)) {
      endWord()
    } else {
      cur += c
      has = true
    }
  }
  endSeg()
  return segs.map(({ words: ws, piped }) => {
    let k = 0
    while (k < ws.length - 1 && (/^[A-Za-z_][A-Za-z0-9_]*=/.test(ws[k] ?? '') || wrappers.has(ws[k] ?? ''))) k++
    return { words: ws.slice(k), piped }
  })
}

/** rank 0 是兜底（一段都没认出来）；1 联网、2 对外、3 不可逆，与 hub 的风险档对应。 */
interface Match {
  rank: 0 | 1 | 2 | 3
  text: string
}

const sshArgOpts = new Set('BbcDEeFIiJLlmOoPpQRSWw'.split('').map((c) => '-' + c))
const installers = new Set(['npm', 'pnpm', 'yarn', 'bun'])

function installText(pkgs: string[]): Match {
  const names = pkgs.length > 3 ? `${pkgs.slice(0, 3).join('、')} 等 ${pkgs.length} 个` : pkgs.join('、')
  return { rank: 1, text: names ? `安装依赖 ${names}，要联网` : '安装依赖，要联网' }
}

const positional = (ws: string[]) => ws.filter((w) => !w.startsWith('-'))

// git push 里带值的选项（值是下一个词）：值不能当成 remote 或分支。写成 --opt=value 的是一个词。
const pushValueOpts = new Set(['-o', '--push-option', '--repo', '--receive-pack', '--exec'])

function matchGit(args: string[]): Match | null {
  let i = 0
  while (i < args.length && args.at(i)?.startsWith('-')) i += args[i] === '-C' || args[i] === '-c' ? 2 : 1
  const sub = args[i]
  const rest = args.slice(i + 1)
  if (sub === 'reset') {
    return rest.includes('--hard') ? { rank: 3, text: '丢弃工作区的改动，不可恢复' } : null
  }
  if (sub !== 'push') return null
  const opts: string[] = []
  const pos: string[] = []
  for (let j = 0; j < rest.length; j++) {
    const w = rest[j] ?? ''
    if (!w.startsWith('-')) pos.push(w)
    else {
      opts.push(w)
      if (pushValueOpts.has(w)) j++
    }
  }
  const short = (re: RegExp) => opts.some((w) => !w.startsWith('--') && re.test(w))
  const remote = pos[0] ?? ''
  const refs = pos.slice(1)
  // 源为空的 refspec（:main、+:main）是删除远程分支
  const delRef = refs.find((w) => /^\+?:./.test(w))
  const suffix = (ref: string) => (remote && ref ? `（${remote} 的 ${ref}）` : remote ? `（${remote}）` : '')
  if (opts.includes('--delete') || short(/^-[A-Za-z]*d/) || delRef !== undefined) {
    const ref = delRef !== undefined ? delRef.replace(/^\+/, '').slice(1) : (refs[0] ?? '')
    return { rank: 3, text: `删除远程分支${suffix(ref)}` }
  }
  if (opts.includes('--mirror') || opts.includes('--prune')) {
    return { rank: 3, text: `让远程和本地完全一致，可能删掉远程的分支${suffix('')}` }
  }
  let force = opts.some((w) => w === '--force' || w.startsWith('--force-with-lease')) || short(/^-[A-Za-z]*f/)
  let ref = refs[0] ?? ''
  if (ref.startsWith('+')) {
    force = true
    ref = ref.slice(1)
  }
  if (ref.includes(':')) ref = ref.slice(ref.lastIndexOf(':') + 1)
  return force ? { rank: 3, text: `强制推送，会覆盖远程历史${suffix(ref)}` } : { rank: 2, text: `把代码推到远程仓库${suffix(ref)}` }
}

// 照 internal/policy/commands.go：curl 的短选项簇含 X d T F 之一（-d@file、-sd 也算），或这些长选项；wget 的这三个长选项。
// 长选项另认 --opt=value 的连写（wget 的写法）。
const curlSendLong = ['--request', '--data', '--data-raw', '--data-binary', '--data-urlencode', '--data-ascii', '--json', '--form', '--form-string', '--upload-file']
const wgetSendLong = ['--post-data', '--post-file', '--method']

function sendsData(name: string, args: string[]): boolean {
  const longs = name === 'curl' ? curlSendLong : wgetSendLong
  return args.some((a) => {
    if (a.startsWith('--')) return longs.some((o) => a === o || a.startsWith(o + '='))
    return name === 'curl' && /^-[^-]*[XdTF]/.test(a)
  })
}

/** 参数里第一个带协议的网址的主机名；没有网址或解析不了为空串。 */
function urlHost(args: string[]): string {
  const url = args.find((w) => /^[a-z][a-z0-9+.-]*:\/\//i.test(w))
  if (!url) return ''
  try {
    return new URL(url).hostname
  } catch {
    return ''
  }
}

const shells = new Set(['sh', 'bash', 'zsh', 'dash', 'ksh', 'fish'])
const baseName = (w: string) => w.slice(w.lastIndexOf('/') + 1)

/** words 是不是一个从标准输入读脚本的 shell：没有 -c（含在 -ec 这类簇里），-- 之前没有脚本文件操作数（单独的 - 不算）。 */
function readsStdin(args: string[]): boolean {
  for (const a of args) {
    if (a === '--') return true
    if (a === '-') continue
    if (!a.startsWith('-')) return false
    if (!a.startsWith('--') && a.includes('c')) return false
  }
  return true
}

/**
 * 管道里的一段（不是第一段）是从标准输入读脚本的 shell（sh -s、bash -、sudo sh）：把脚本直接交给 shell 执行。
 * 前面同一条管道里有 curl/wget 就说下载；hub 把 curl … | sh 只定为联网，页面可以说得比它重。
 */
function matchPipedShell(segs: Segment[], i: number): Match | null {
  const seg = segs[i]
  if (!seg?.piped) return null
  let args = seg.words
  let admin = false
  if (baseName(args[0] ?? '') === 'sudo') {
    admin = true
    let k = 1
    while (k < args.length && (args[k] ?? '').startsWith('-')) k++
    args = args.slice(k)
  }
  if (!shells.has(baseName(args[0] ?? '')) || !readsStdin(args.slice(1))) return null
  const how = admin ? '以管理员权限直接执行' : '直接执行'
  for (let j = i - 1; j >= 0; j--) {
    const prev = segs[j]?.words ?? []
    const n = baseName(prev[0] ?? '')
    if (n === 'curl' || n === 'wget') {
      const host = urlHost(prev.slice(1))
      return { rank: 3, text: host ? `下载 ${host} 的脚本并${how}` : `下载脚本并${how}` }
    }
    if (!segs[j]?.piped) break
  }
  return { rank: 3, text: admin ? '以管理员权限执行管道传来的脚本' : '执行管道传来的脚本' }
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
      const send = sendsData(name, args)
      const host = urlHost(args)
      if (host) return send ? { rank: 2, text: `向 ${host} 发送数据` } : { rank: 1, text: `访问网址 ${host}` }
      return send ? { rank: 2, text: `用 ${name} 发送数据` } : { rank: 1, text: `用 ${name} 访问网络` }
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

/** 兜底：原样说运行命令（空白并成一个空格，超长截断）；空命令说运行一条命令。 */
function fallbackAction(command: string): string {
  const c = command.trim()
  return c ? `运行命令：${clip(c.replace(/\s+/g, ' '), 80)}` : '运行一条命令'
}

/** 各段里取最要紧的一段（不可逆 > 对外 > 联网，一样高取靠前的）；都认不出时 rank 0，说运行命令。 */
function shellMatch(command: string): Match {
  let best: Match | null = null
  const segs = segments(command)
  for (let i = 0; i < segs.length; i++) {
    const m = matchPipedShell(segs, i) ?? matchSegment(segs[i]?.words ?? [])
    if (m && (!best || m.rank > best.rank)) best = m
  }
  return best ?? { rank: 0, text: fallbackAction(command) }
}

/** 一条 shell 命令“做什么”：各段里取最要紧的一段（不可逆 > 对外 > 联网，一样高取靠前的）；都认不出时原样说运行命令。不看 hub 的风险。 */
export function shellAction(command: string): string {
  return shellMatch(command).text
}

// hub 的风险档对应的最低 rank：认出的那段比 hub 定的风险轻时，说明句不能比 hub 说得轻，退回原命令。
const riskFloor: Record<string, number> = { destructive: 3, outward: 2, network: 1 }

function permissionWhat(r: RequestView): string {
  const p = r.payload
  switch (p.type) {
    case 'shell': {
      const command = p.command ?? ''
      const m = shellMatch(command)
      return m.rank < (riskFloor[r.risk] ?? 0) ? fallbackAction(command) : m.text
    }
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

/** 去掉结尾的 /（只剩 / 时保留），比较目录用。 */
const trimSlash = (p: string) => (p.length > 1 ? p.replace(/\/+$/, '') || '/' : p)

/** 把一条请求说成人话：整句、做什么、在哪、为什么要你决定、批了拒了会怎样。 */
export function explainRequest(r: RequestView): Explanation {
  const cli = cliLabel(r.cli)
  // 命令的目录不是项目根时也说出来（项目根就是项目名，不重复）
  const cwd = r.payload.type === 'shell' ? (r.payload.cwd ?? '') : ''
  const where = [
    r.project && `项目 ${r.project}`,
    r.machine_name && `机器 ${machineLabel(r.machine_name)}`,
    cwd && trimSlash(cwd) !== trimSlash(r.project_root) && `目录 ${cwd}`,
  ]
    .filter(Boolean)
    .join('，')
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
  const pending = r.state === 'pending'
  if (pending) {
    next.push('批准：agent 执行这一条，只这一次。', '拒绝：agent 收到拒绝，会换个做法或停下来。')
    if (!isRedacted(r)) next.push(rememberLine(r))
  }
  return { headline: `${cli} 想${what}`, what, where, why: pending ? permissionWhy(r) : '', next }
}

/** “记住”那行：这类操作只能存成拒绝规则（对外、不可逆、未知、项目外，或命令、文件写入没有项目根）时别许诺“不再问”。 */
function rememberLine(r: RequestView): string {
  const denyOnly =
    ['outward', 'destructive', 'unknown', 'write_outside'].includes(r.risk) ||
    ((r.payload.type === 'shell' || r.payload.type === 'file_write') && r.project_root === '')
  return denyOnly ? '勾「记住」只能存成拒绝规则，下次同类的直接拒绝；这类操作不能记成放行。' : '勾「记住」可以存成规则，下次同类的不再问。'
}
