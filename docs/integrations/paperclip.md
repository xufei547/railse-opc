# 与 Paperclip 一起用

[English](paperclip.en.md)

## 各管哪一层

Paperclip 管组织图、派活、预算与组织级审批；Railse 管接入钩子的每一次工具调用，先过本机策略，定不了的再按 CLI 的能力交人处理。Paperclip 决定谁做什么，Railse 给执行中的会话划权限边界。两者可以一起用，不需要新的 Paperclip 适配器。

仓库里的 [Paperclip spike](../research/2026-11-03-openclaw-paperclip-spike.md#2-paperclip不需要接已经在我们下面) 是文档层研究：Claude Code / Codex 适配器启动的 CLI 会加载用户级钩子，所以员工通过这些钩子发出的工具调用会经过 Railse。Codex 只有本来要问人时才触发 `PermissionRequest`，不能据此说它的所有调用都受角色约束。

该研究记载适配器默认 `engine: auto`，有双向进程会话时走 ACP，否则走 CLI。**CLI 通道的判断来自文档，尚未在 Paperclip 真机环境验收；ACP 通道下 Claude Code 的用户级钩子是否照常触发仍未验证。** 不要把这里当成 ACP 已通过测试的结论。

## 在执行员工任务的机器上

先按 [setup.md](../setup.md) 安装、启动守护进程，接入 hub；在运行员工 CLI 的同一用户下装用户级钩子：

```sh
railse setup hooks --claude --codex
railse doctor
railse role init
```

`role init` 只在文件不存在时写模板。编辑这台机器的 `~/.railse/roles.yaml`（自定义守护进程目录时用那个目录，命令带同样的 `--dir`），例如：

```yaml
version: 1
roles:
  - name: 开发
    dirs: ["~/work/app"]
    ceiling: network
  - name: 写作
    dirs: ["~/work/writing"]
    ceiling: write_in_project
    forbid: [{command_prefix: "git push"}]
```

把目录换成员工实际工作的项目根目录。`dirs` 优先匹配项目根，没有项目根才用工作目录；同一个 git 仓库里的子目录不会自动成为独立的角色地盘。

给每个员工绑定角色有两种做法：

- 在 Paperclip 启动该员工 CLI 的环境变量里设 `RAILSE_ROLE=写作`（开发员工设 `RAILSE_ROLE=开发`）。按 Paperclip 的适配器配置设环境变量，具体位置见它的文档；本仓库材料没有确认配置位置或字段名。
- 让员工在各自的工作目录运行，用上面角色的 `dirs` 绑定。有效的 `RAILSE_ROLE` 优先于目录；都没有匹配时是“未分配”。Paperclip 的员工名不会自动变成 Railse 角色名。

在对应工作目录跑 `railse role check` 校验文件、`railse role which` 看目录与当前终端的环境变量会绑定谁。`which` 不读取 Paperclip 员工进程的环境，核对环境绑定时要在同样的环境下运行。手机“总台”按机器和角色看等待、放行、拦下与最近动作，只看不改角色。

## 局限

- Codex 的免确认模式与 `codex exec` 不触发已接入的 `PermissionRequest`，角色管不到；嵌套起的这类 Codex 也一样。首次使用交互式 Codex 前，在 Codex 里用 `/hooks` 审核钩子。
- 超过 `ceiling` 的请求必须人来定，不能记成放行、不能限时放行；命中 `forbid` 直接拒绝。
- 角色不是沙箱：它约束正常接入钩子、Railse 能识别的工具调用，挡误操作与明显越权，不保证挡住被注入后刻意规避的 agent。要硬隔离请配合系统级沙箱或容器。完整清单见 [角色防什么、不防什么](../agent-local-loop.md#角色防什么不防什么)。
