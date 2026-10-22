# 本机闭环：守护进程与钩子

没有 hub 时，Railse 在本机这样工作：CLI 的钩子把审批请求经 Unix socket 交给守护进程，守护进程用规则引擎自动放行或拒绝，判不了的交回终端，每一步都写本地审计。

## 起守护进程

```sh
go build -o ~/bin/railse ./cmd/railse
railse agent run --foreground    # 前台运行，--foreground 让日志同时打到 stderr；Ctrl-C 退出
railse agent status              # 另开终端查看：paused、规则条数、待决请求数
```

配置目录取 `$RAILSE_HOME`，没设就是 `~/.railse`（权限 0700），里面有 `agent.sock`、`rules.yaml`、`audit.jsonl`、`state.json`、`agent.log`，socket 与审计为 0600。`agent` 子命令也接受 `--dir 目录`，但钩子只认 `$RAILSE_HOME` 或 `~/.railse`，所以 CLI 与守护进程要用同一个 `RAILSE_HOME`。开机自启留到后续计划。

## 装钩子

```sh
railse setup hooks --project <目录> [--claude] [--codex] [--gemini] [--bin 路径] [--dry-run]
```

不带 CLI 开关时三个都装。Claude 写 `<目录>/.claude/settings.json`，Gemini 写 `<目录>/.gemini/settings.json`，Codex 写用户级 `~/.codex/hooks.json`（`--codex-file` 可改）。`--bin` 是写进钩子命令的 railse 路径，默认当前程序；`--dry-run` 只打印不写。重复执行只替换 Railse 自己的条目，别的钩子原样保留。

## 看审计

`~/.railse/audit.jsonl` 只追加，一行一条 JSON，例如 `tail -f ~/.railse/audit.jsonl | jq .`。

| 字段 | 含义 |
| --- | --- |
| `ts`、`request_id` | 时间；审批请求的 ID（事件没有） |
| `kind` | `permission`、`question`、`done`、`session_end`、`control` |
| `session` | `tool`、`cli_session_id`、`turn_id`、`cwd` |
| `tool_name`、`payload`、`risk` | 工具名；脱敏后的统一载荷；风险档（`read` 到 `destructive`） |
| `action` | `allow`、`deny`、`passthrough` |
| `state` | 见下表 |
| `rule_id`、`reason`、`decided_by` | 命中的规则与理由；谁定的：`rule:<id>`、`no_hub`、`paused`、`timeout`、`terminal`、`shutdown`、`error` |
| `message` | 事件文本，如最后一条回复 |

| `state` | 含义 |
| --- | --- |
| `auto_allowed` / `auto_denied` | 规则自动放行 / 拒绝 |
| `passthrough` | 交回终端：没有规则命中且没有 hub、已暂停、守护进程正在停止或出错 |
| `cancelled` | 决定前钩子进程已退出（多半是终端里先处理了） |
| `expired` | 等待过期，交回终端 |
| `approved` / `denied` | 由 hub 批准 / 拒绝，接入 hub 后才会出现 |
| `recorded` | 事件或管理命令已记录 |
| `suppressed` | 同一会话有待决审批时来的 `question`，不重复提醒 |

审计里的命令已脱敏，且不记录钩子进程的环境变量。

## 改规则

首次启动会写出内置规则包（只读和项目内改动放行），格式见设计文档 4.3。编辑 `~/.railse/rules.yaml` 后执行 `railse agent reload`，或给守护进程发 `SIGHUP`。文件写坏时保留旧规则：`reload` 直接报错，`SIGHUP` 把错误写进 `agent.log`；但启动时文件是坏的，守护进程会报错退出。例如拒绝一切 `rm`，追加到 `rules:` 下，缩进与文件里已有条目对齐：

```yaml
    - id: user-deny-rm
      source: user
      scope: global
      match: {payload: shell, command_prefix: rm}
      action: deny
```

## 暂停与恢复

`railse agent pause` 后，除命中用户 deny 规则的请求外一律交回终端；`railse agent resume` 恢复。暂停状态存在 `state.json`，重启守护进程后保持。

## 三家 CLI 的差异

| | Claude Code | Codex | Gemini CLI |
| --- | --- | --- | --- |
| 自动放行 | 能 | 能 | 不能，放行一律渲染为不作决定 |
| 自动拒绝 | 能 | 能 | 能 |
| 事件 | question、done、session_end | done、session_end | question、done、session_end |
| 钩子配置 | 项目级 | 用户级 `~/.codex/hooks.json` | 项目级 |
| 注意 | 全功能 | 只在交互会话里审批（`codex exec` 不触发）；首次使用在 Codex 里用 `/hooks` 审核；notify 要手动写进 `~/.codex/config.toml`：`notify = ["<bin>", "hook", "codex-notify"]` | 需要 0.65.0 以上；不要开 yolo 模式 |

## 交回终端的两种情况

- **没有 hub 时 ask 一律交回终端。** 引擎答 ask（没有规则命中，如 `git push`、`curl`、`npm install`）时，守护进程立即回“不作决定”，CLI 弹出自己的原生审批框，由你在终端里定；审计记 `passthrough`，`decided_by` 为 `no_hub`。
- **钩子任何异常都不作决定。** 守护进程没起、socket 连不上、输入损坏、不认识的 CLI、收到 SIGTERM/SIGINT/SIGHUP、内部出错，钩子都是 stdout 为空、退出 0，原因写到 stderr。最坏情况就是回到没装 Railse 时的样子。

## 冒烟

`scripts/smoke-claude.sh` 在临时目录里起守护进程、装 Claude 钩子，再让 `claude -p` 执行一条 `touch`，最后打印审计。需要本机 Claude Code 已登录，会消耗模型额度，不进 CI。
