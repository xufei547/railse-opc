# Using Railse with herdr

[中文](herdr.md)

## What each one handles

herdr is an agent terminal and overview across machines (a cross-machine TUI). Railse handles policy and phone approvals. One shows running sessions; the other checks whether calls cross boundaries or need a human decision. They do not conflict. The herdr description here is limited to the repository's [positioning memo](../research/2026-11-03-global-positioning.md) (Chinese).

In Claude Code / Codex / Gemini sessions started through herdr, tool calls still reach Railse as long as hooks are installed for the user on the machine running the CLI and the CLI fires the corresponding events. This uses the existing CLI hooks and needs no herdr-specific adapter. The repository has no record of acceptance testing on a real herdr setup.

## On every machine running CLIs

Follow [setup.md](../setup.md) (Chinese) to install and start the daemon and enroll with the hub, then install user-level hooks as the user running the CLIs:

```sh
railse setup hooks
railse doctor
railse role init
```

Without CLI flags, `setup hooks` installs all three. `role init` writes a template only if the file does not exist. For a custom daemon directory, pass the same `--dir` to each command; the role file also lives there.

Use `RAILSE_ROLE` to give windows different roles, for example by starting the CLI in the relevant terminal:

```sh
RAILSE_ROLE=dev claude
RAILSE_ROLE=writer codex
RAILSE_ROLE=ops gemini
```

Define these names in the machine's `~/.railse/roles.yaml` first. Alternatively, bind by directory, for example:

```yaml
version: 1
roles:
  - name: dev
    dirs: ["~/work/app"]
    ceiling: network
  - name: writer
    dirs: ["~/work/writing"]
    ceiling: write_in_project
    forbid: [{command_prefix: "git push"}]
  - name: ops
    dirs: ["~/work/infra"]
    ceiling: destructive
```

Replace the paths with actual project roots. A valid `RAILSE_ROLE` takes precedence; otherwise `dirs` matches the project root, using the working directory only when there is no project root. Subdirectories of one repository do not automatically get separate roles. For remote sessions, install hooks and define roles on the machine actually running the CLI.

`railse role check` validates the file; `railse role which` explains what the current directory and terminal environment resolve to. The phone's Desk groups waiting requests, active trusts, denials and auto-approvals in the last 24 hours, and recent actions by machine and role. Edit roles in the file on the machine.

## Limits

- Only Codex's `PermissionRequest` is hooked. Roles cannot constrain calls in confirmation-free modes or `codex exec`, including nested Codex sessions in these modes. Before first using interactive Codex, review hooks with `/hooks` inside Codex.
- Gemini hooks can deny but cannot approve for you, and requests never reach the phone. Hooked calls that cross a role boundary or touch Railse configuration are always denied, because Gemini has no reliable channel for asking a human. Yolo mode skips hooks and is not constrained by roles.
- Calls above `ceiling` require a human decision and cannot be remembered as allowed or covered by a time-limited trust. A `forbid` match is denied outright.
- Roles are not a sandbox. They constrain properly hooked tool calls Railse can recognize, catching mistakes and obvious overreach. They cannot guarantee blocking an injected agent deliberately evading them. Use an OS sandbox or container for hard isolation. See the full [list of limitations](../agent-local-loop.md#角色防什么不防什么) (Chinese).
