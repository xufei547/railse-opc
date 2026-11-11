# 与 herdr 一起用

[English](herdr.en.md)

## 各管哪一层

herdr 是多机器的 agent 终端与总览（跨机器 TUI）；Railse 管策略与手机审批。一个看运行中的会话，一个看调用是否越界、是否需要人来定，两者不冲突。这里对 herdr 的功能描述限于仓库里的 [定位备忘](../research/2026-11-03-global-positioning.md)。

在 herdr 起的 Claude Code / Codex / Gemini 会话里，只要运行 CLI 的机器、用户装了 Railse 钩子，并且 CLI 触发对应事件，工具调用照样经过 Railse。这是沿用 CLI 钩子的接法，不需要 herdr 专用适配器；本仓库没有 herdr 实机验收记录。

## 在每台运行 CLI 的机器上

先按 [setup.md](../setup.md) 安装、启动守护进程并接入 hub，再以运行 CLI 的用户安装用户级钩子：

```sh
railse setup hooks
railse doctor
railse role init
```

`setup hooks` 不带 CLI 开关会装三家。`role init` 只在文件不存在时写模板。自定义守护进程目录时，各命令带同样的 `--dir`，角色文件也在那个目录里。

用 `RAILSE_ROLE` 给不同窗口分角色，例如在相应终端启动 CLI：

```sh
RAILSE_ROLE=开发 claude
RAILSE_ROLE=写作 codex
RAILSE_ROLE=运维 gemini
```

这些名字要先在机器上的 `~/.railse/roles.yaml` 定义。也可以按目录绑定，例如：

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
  - name: 运维
    dirs: ["~/work/infra"]
    ceiling: destructive
```

把路径换成实际项目根目录。有效的 `RAILSE_ROLE` 优先，否则按项目根匹配 `dirs`，没有项目根才用工作目录；同一个仓库的子目录不会自动划成不同角色。远端会话要在实际运行 CLI 的机器上装钩子、定义角色。

`railse role check` 校验文件，`railse role which` 说明当前目录与当前终端的环境变量会绑定谁。手机“总台”按机器、角色看等我、放行中、24 小时内拦下与自动放行、最近动作；角色只在机器上的文件里改。

## 局限

- Codex 只接了 `PermissionRequest`：免确认模式与 `codex exec` 的调用角色管不到，嵌套起的这类 Codex 也一样。首次使用交互式 Codex 前，在 Codex 里用 `/hooks` 审核钩子。
- Gemini 的钩子只能拒绝，不能替你批准，也不进手机；经过钩子的请求越界或触及 Railse 配置时一律拒绝，因为它没有可靠的问人通道。yolo 模式跳过钩子，不受角色约束。
- 超过 `ceiling` 必须人来定，不能记成放行、不能限时放行；命中 `forbid` 直接拒绝。
- 角色不是沙箱：它约束正常接入钩子、Railse 能识别的工具调用，挡误操作与明显越权，不保证挡住被注入后刻意规避的 agent。要硬隔离请配合系统级沙箱或容器。完整清单见 [角色防什么、不防什么](../agent-local-loop.md#角色防什么不防什么)。
