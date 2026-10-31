import { describe, expect, it } from 'vitest'
import { cliLabel, explainRequest, machineLabel, shellAction } from './explain'
import type { RequestView } from './types'

function req(over: Partial<RequestView> = {}): RequestView {
  return {
    id: 'r1', machine_id: 'm', session_id: 's', cli: 'claude', kind: 'permission', tool_name: 'Bash',
    payload: { type: 'shell', command: 'git push origin main', cwd: '/p/blog' }, risk: 'outward', project_root: '/p/blog',
    engine_reason: '无规则命中，按 outward 默认询问', state: 'pending', decided_by: '', decision_note: '', rule_id: '', turn_id: 't',
    cli_session_id: 's', title: '', created_at: '2026-10-23T10:00:00.000000000Z', expires_at: null, decided_at: null,
    delivered_at: null, machine_name: 'mac.local', project: 'blog', has_redacted: false, ...over,
  }
}

const riskWords = /read|write_in_project|unknown|write_outside|network|outward|destructive/

describe('cliLabel', () => {
  it('与 hub 的 cliNames 一致；空与不认识的叫 Agent', () => {
    expect(cliLabel('claude')).toBe('Claude Code')
    expect(cliLabel('codex')).toBe('Codex')
    expect(cliLabel('gemini')).toBe('Gemini CLI')
    expect(cliLabel('')).toBe('Agent')
    expect(cliLabel('cursor')).toBe('Agent')
  })
})

describe('machineLabel', () => {
  it('去掉结尾的 .local（不分大小写）；整个名字就是 .local 时原样', () => {
    expect(machineLabel('mac.local')).toBe('mac')
    expect(machineLabel('Mini.LOCAL')).toBe('Mini')
    expect(machineLabel('.local')).toBe('.local')
    expect(machineLabel('box')).toBe('box')
    expect(machineLabel('')).toBe('')
  })
})

describe('shellAction', () => {
  const force = '强制推送，会覆盖远程历史'
  const cases: [string, string][] = [
    ['git push origin main', '把代码推到远程仓库（origin 的 main）'],
    ['git push', '把代码推到远程仓库'],
    ['git push origin', '把代码推到远程仓库（origin）'],
    ['git push -u origin feat/x', '把代码推到远程仓库（origin 的 feat/x）'],
    ['git push origin HEAD:release', '把代码推到远程仓库（origin 的 release）'],
    ['git -C /p/blog push origin main', '把代码推到远程仓库（origin 的 main）'],
    ['/usr/bin/git push origin main', '把代码推到远程仓库（origin 的 main）'],
    ['git push --force origin main', `${force}（origin 的 main）`],
    ['git push -f', force],
    ['git push --force-with-lease origin main', `${force}（origin 的 main）`],
    ['git push --force-with-lease=main:abc origin main', `${force}（origin 的 main）`],
    ['git push -uf origin main', `${force}（origin 的 main）`],
    ['git push origin +main', `${force}（origin 的 main）`],
    ['git push --follow-tags origin main', '把代码推到远程仓库（origin 的 main）'],
    ['git reset --hard HEAD~1', '丢弃工作区的改动，不可恢复'],
    ['git reset --soft HEAD~1', '运行命令：git reset --soft HEAD~1'],
    ['rm -rf build dist', '删除 build、dist，删了不可恢复'],
    ['rm -r /tmp/x', '删除 /tmp/x，删了不可恢复'],
    ['rm notes.txt', '删除 notes.txt，删了不可恢复'],
    ['rm -rf -- -weird', '删除 -weird，删了不可恢复'],
    ['rm -rf', '删除文件，删了不可恢复'],
    ['curl -sSL https://api.example.com/v1/x', '访问网址 api.example.com'],
    ['wget "https://dl.example.org/a.tgz"', '访问网址 dl.example.org'],
    ['curl -X POST localhost:8080/x', '用 curl 访问网络'],
    ['curl http://[bad', '用 curl 访问网络'],
    ['npm install', '安装依赖，要联网'],
    ['npm i react react-dom', '安装依赖 react、react-dom，要联网'],
    ['pnpm add -D vitest', '安装依赖 vitest，要联网'],
    ['yarn add a b c d', '安装依赖 a、b、c 等 4 个，要联网'],
    ['bun install', '安装依赖，要联网'],
    ['pip install requests', '安装依赖 requests，要联网'],
    ['pip3 install -U httpx', '安装依赖 httpx，要联网'],
    ['uv add ruff', '安装依赖 ruff，要联网'],
    ['uv pip install ruff', '安装依赖 ruff，要联网'],
    ['uv pip list', '运行命令：uv pip list'],
    ['/opt/homebrew/bin/brew install jq', '安装依赖 jq，要联网'],
    ['go get golang.org/x/net', '安装依赖 golang.org/x/net，要联网'],
    ['go install golang.org/x/tools/gopls@latest', '安装依赖 golang.org/x/tools/gopls@latest，要联网'],
    ['cargo add serde', '安装依赖 serde，要联网'],
    ['cargo install ripgrep', '安装依赖 ripgrep，要联网'],
    ['gem install rails', '安装依赖 rails，要联网'],
    ['npm publish', '发布或部署到外部：npm publish'],
    ['gh release create v1.0.0 --notes "x y"', '发布或部署到外部：gh release create v1.0.0 --notes x y'],
    ['gh pr create --fill', '发布或部署到外部：gh pr create --fill'],
    ['gh pr list', '运行命令：gh pr list'],
    ['vercel --prod', '发布或部署到外部：vercel --prod'],
    ['fly deploy', '发布或部署到外部：fly deploy'],
    ['flyctl deploy -a railse-hub', '发布或部署到外部：flyctl deploy -a railse-hub'],
    ['kubectl apply -f k.yaml', '发布或部署到外部：kubectl apply -f k.yaml'],
    ['terraform apply -auto-approve', '发布或部署到外部：terraform apply -auto-approve'],
    ['ssh deploy@prod.example.com uptime', '连接远程机器 deploy@prod.example.com'],
    ['ssh -p 2222 -i ~/.ssh/k box', '连接远程机器 box'],
    ['ssh', '连接远程机器'],
    ['sudo launchctl kickstart -k system/x', '以管理员权限运行：launchctl kickstart -k system/x'],
    ['sudo', '以管理员权限运行命令'],
    ['docker system prune -af', '清理 Docker 数据，不可恢复'],
    ['docker volume rm data', '清理 Docker 数据，不可恢复'],
    ['docker volume prune', '清理 Docker 数据，不可恢复'],
    ['docker ps', '运行命令：docker ps'],
    ['ls -la', '运行命令：ls -la'],
  ]
  it.each(cases)('%s → %s', (command, want) => {
    expect(shellAction(command)).toBe(want)
  })

  it('去掉前导环境变量赋值（引号里的值带空格也认）', () => {
    expect(shellAction('FOO=1 BAR="a b" git push origin main')).toBe('把代码推到远程仓库（origin 的 main）')
    expect(shellAction('NODE_ENV=production npm publish')).toBe('发布或部署到外部：npm publish')
    expect(shellAction('FOO=1')).toBe('运行命令：FOO=1')
  })

  it('复合命令取最要紧的一段：不可逆 > 对外 > 联网，一样高取靠前的', () => {
    expect(shellAction('cd /p/blog && git push origin main')).toBe('把代码推到远程仓库（origin 的 main）')
    expect(shellAction('git add -A && git commit -m "x && y" && git push')).toBe('把代码推到远程仓库')
    expect(shellAction('npm install && npm publish')).toBe('发布或部署到外部：npm publish')
    expect(shellAction('git push; rm -rf dist')).toBe('删除 dist，删了不可恢复')
    expect(shellAction('curl -fsSL https://get.example.sh | sh')).toBe('访问网址 get.example.sh')
    expect(shellAction('curl https://a.example.com & wget https://b.example.com')).toBe('访问网址 a.example.com')
    expect(shellAction('make || make clean\nmake test')).toBe('运行命令：make || make clean make test')
  })

  it('引号里的分隔符不切段', () => {
    expect(shellAction('echo "a && git push"')).toBe('运行命令：echo "a && git push"')
    expect(shellAction("echo 'rm -rf /'; ls")).toBe("运行命令：echo 'rm -rf /'; ls")
  })

  it('空命令、超长命令', () => {
    expect(shellAction('')).toBe('运行一条命令')
    expect(shellAction('  \n ')).toBe('运行一条命令')
    const long = 'echo ' + 'x'.repeat(200)
    expect(shellAction(long)).toBe('运行命令：' + long.slice(0, 80) + '…')
  })
})

describe('explainRequest：headline 与 what', () => {
  it('shell：<CLI 显示名> 想<做什么>；CLI 为空或不认识叫 Agent', () => {
    const e = explainRequest(req())
    expect(e.what).toBe('把代码推到远程仓库（origin 的 main）')
    expect(e.headline).toBe('Claude Code 想把代码推到远程仓库（origin 的 main）')
    expect(explainRequest(req({ cli: '' })).headline).toBe('Agent 想把代码推到远程仓库（origin 的 main）')
    expect(explainRequest(req({ cli: 'cursor' })).headline).toBe('Agent 想把代码推到远程仓库（origin 的 main）')
    expect(explainRequest(req({ cli: 'codex', payload: { type: 'shell' } })).headline).toBe('Codex 想运行一条命令')
  })

  it('文件改动按风险', () => {
    const file = (risk: string) => explainRequest(req({ risk, payload: { type: 'file_write', path: '/p/blog/a.ts' } })).what
    expect(file('write_in_project')).toBe('修改文件 /p/blog/a.ts')
    expect(file('write_outside')).toBe('修改项目外的文件 /p/blog/a.ts')
    expect(file('destructive')).toBe('改敏感文件 /p/blog/a.ts（密钥、配置或开机自启）')
    expect(file('unknown')).toBe('修改文件 /p/blog/a.ts')
  })

  it('联网：主机名；网址解析不了原样；没有网址写工具名', () => {
    const net = (url: string | undefined, tool_name = 'WebFetch') =>
      explainRequest(req({ risk: 'network', tool_name, payload: { type: 'network', url, method: 'GET' } })).what
    expect(net('https://docs.example.com:8443/a?b=1')).toBe('访问 docs.example.com')
    expect(net('not a url')).toBe('访问 not a url')
    expect(net(undefined, 'WebSearch')).toBe('用 WebSearch 联网')
    expect(net(undefined, '')).toBe('联网')
  })

  it('MCP：调用 <服务> 的 <工具>；缺哪个补哪个', () => {
    const mcp = (server?: string, tool?: string) => explainRequest(req({ risk: 'unknown', payload: { type: 'mcp', server, tool } })).what
    expect(mcp('github', 'create_issue')).toBe('调用 github 的 create_issue')
    expect(mcp('github')).toBe('调用 github 的工具')
    expect(mcp(undefined, 'create_issue')).toBe('调用 MCP 工具 create_issue')
    expect(mcp()).toBe('调用 MCP 工具')
  })

  it('认不出的载荷写工具名', () => {
    expect(explainRequest(req({ risk: 'unknown', tool_name: 'read_file', payload: { type: 'other' } })).what).toBe('使用工具 read_file')
    expect(explainRequest(req({ risk: 'unknown', tool_name: '', payload: { type: 'other' } })).what).toBe('做一件事')
  })

  it('question、webhook、done', () => {
    const q = explainRequest(req({ kind: 'question', state: 'recorded', title: '要删旧分支吗？', payload: { type: '' } }))
    expect(q.headline).toBe('Claude Code 在终端等你回答')
    expect(q.what).toBe('在终端等你回答')
    const w = explainRequest(req({ kind: 'webhook', cli: '', machine_name: '', project: '', risk: 'unknown', title: '部署 v1.2 到生产', payload: { type: 'other' } }))
    expect(w.headline).toBe('工作流等你批准：部署 v1.2 到生产')
    expect(w.what).toBe('工作流等你批准')
    expect(explainRequest(req({ kind: 'webhook', title: '' })).headline).toBe('工作流等你批准')
    expect(explainRequest(req({ kind: 'webhook', title: 'x'.repeat(100) })).headline).toBe('工作流等你批准：' + 'x'.repeat(80) + '…')
    const d = explainRequest(req({ kind: 'done', state: 'recorded', cli: 'gemini' }))
    expect(d.headline).toBe('Gemini CLI 做完了这一轮')
    expect(d.next).toEqual([])
  })
})

describe('explainRequest：where', () => {
  it('项目 <项目名>，机器 <机器名去 .local>；缺哪个省哪个', () => {
    expect(explainRequest(req()).where).toBe('项目 blog，机器 mac')
    expect(explainRequest(req({ machine_name: '' })).where).toBe('项目 blog')
    expect(explainRequest(req({ project: '' })).where).toBe('机器 mac')
    expect(explainRequest(req({ project: '', machine_name: '' })).where).toBe('')
  })
})

describe('explainRequest：why', () => {
  const why = (risk: string, over: Partial<RequestView> = {}) =>
    explainRequest(req({ risk, engine_reason: `无规则命中，按 ${risk} 默认询问`, ...over })).why

  it('按风险档说为什么要你决定', () => {
    expect(why('network')).toBe('这条要联网。规则默认不自动放行联网操作，要你决定。')
    expect(why('outward')).toBe('这会影响仓库之外的人和系统（推送、发布、部署）。规则永远不会自动放行这类操作，要你决定。')
    expect(why('destructive')).toBe('这是不可逆的操作。规则永远不会自动放行，要你决定。')
    expect(why('write_outside')).toBe('要改项目目录之外的文件。规则默认不自动放行，要你决定。')
    expect(why('unknown')).toBe('这条命令不在已知名单里，规则判断不了，要你决定。')
    expect(why('unknown', { payload: { type: 'mcp', server: 'db', tool: 'q' } })).toBe('这个操作不在已知名单里，规则判断不了，要你决定。')
    expect(why('read', { engine_reason: '命中规则 user-ask-read' })).toBe('本来可以自动放行，但有规则要求问你。')
    expect(why('write_in_project', { engine_reason: '命中规则 user-ask' })).toBe('本来可以自动放行，但有规则要求问你。')
    expect(why('write_in_project')).toBe('这类操作本来可以自动放行，但现在没有规则放行它，要你决定。')
    expect(why('')).toBe('规则定不了，要你决定。')
    expect(why('weird')).toBe('规则定不了，要你决定。')
  })

  it('自动审批暂停时先说暂停；read、write_in_project 只说这一句', () => {
    expect(why('network', { engine_reason: '自动审批已暂停' })).toBe('自动审批已暂停，所有请求都会问你。这条要联网。规则默认不自动放行联网操作，要你决定。')
    expect(why('read', { engine_reason: '自动审批已暂停' })).toBe('自动审批已暂停，所有请求都会问你。')
    expect(why('write_in_project', { engine_reason: '自动审批已暂停' })).toBe('自动审批已暂停，所有请求都会问你。')
  })

  it('不出现英文风险词', () => {
    for (const risk of ['read', 'write_in_project', 'unknown', 'write_outside', 'network', 'outward', 'destructive']) {
      expect(why(risk)).not.toMatch(riskWords)
      expect(why(risk, { engine_reason: '自动审批已暂停' })).not.toMatch(riskWords)
    }
  })

  it('question、webhook、done 没有 why', () => {
    expect(explainRequest(req({ kind: 'question', state: 'recorded', title: 'q' })).why).toBe('')
    expect(explainRequest(req({ kind: 'webhook', title: 'w' })).why).toBe('')
    expect(explainRequest(req({ kind: 'done', state: 'recorded' })).why).toBe('')
  })
})

describe('explainRequest：next', () => {
  it('审批：批准、拒绝、记住三行；含隐藏部分时没有记住那行', () => {
    expect(explainRequest(req()).next).toEqual([
      '批准：agent 执行这一条，只这一次。',
      '拒绝：agent 收到拒绝，会换个做法或停下来。',
      '勾「记住」可以存成规则，下次同类的不再问。',
    ])
    expect(explainRequest(req({ has_redacted: true })).next).toEqual([
      '批准：agent 执行这一条，只这一次。',
      '拒绝：agent 收到拒绝，会换个做法或停下来。',
    ])
  })

  it('question、webhook 各一行', () => {
    expect(explainRequest(req({ kind: 'question', state: 'recorded', title: 'q' })).next).toEqual(['去那台机器的终端回答，这里只是提醒。'])
    expect(explainRequest(req({ kind: 'webhook', title: 'w' })).next).toEqual(['批准或拒绝会回调给发起它的工作流。'])
  })
})
