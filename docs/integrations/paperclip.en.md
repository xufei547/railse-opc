# Using Railse with Paperclip

[中文](paperclip.md)

## What each one handles

Paperclip handles the org chart, task assignments, budgets and organization-level approvals. Railse handles each tool call that reaches its hooks: local policy first, then a human decision where the CLI supports it. Paperclip decides who does what; Railse sets permission boundaries for the running session. They can work together without a new Paperclip adapter.

The repository's [Paperclip spike](../research/2026-11-03-openclaw-paperclip-spike.md#2-paperclip不需要接已经在我们下面) is documentation research: CLIs started by the Claude Code / Codex adapters load user-level hooks, so employee tool calls sent through these hooks reach Railse. Codex only fires `PermissionRequest` when it would already ask a human; this does not mean roles constrain all its calls.

The research records `engine: auto` as the adapter default: ACP when bidirectional process sessions are available, CLI otherwise. **The CLI conclusion comes from documentation and has not been accepted on a real Paperclip setup. Whether Claude Code user-level hooks fire through ACP is still unverified.** This guide is not evidence of a tested ACP integration.

## On the machine running employee tasks

Follow [setup.md](../setup.md) (Chinese) to install and start the daemon and enroll it with the hub. Install user-level hooks as the same user that runs the employee CLIs:

```sh
railse setup hooks --claude --codex
railse doctor
railse role init
```

`role init` writes a template only if the file does not exist. Edit `~/.railse/roles.yaml` on this machine (use the daemon directory and the same `--dir` on commands if customized), for example:

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
```

Replace the directories with the employees' actual project roots. `dirs` matches the project root first, using the working directory only when there is no project root. A subdirectory within the same git repository does not automatically become a separate role territory.

There are two ways to bind each employee:

- Set `RAILSE_ROLE=writer` in the environment Paperclip uses to start that employee's CLI (`RAILSE_ROLE=dev` for development). Set the environment variable through Paperclip's adapter configuration; see its documentation for the location. The repository material does not confirm a configuration location or field name.
- Run employees in their respective working directories and bind with the roles' `dirs` above. A valid `RAILSE_ROLE` takes precedence over directories; no match means unassigned. Paperclip employee names do not automatically become Railse role names.

In the relevant working directory, run `railse role check` to validate the file and `railse role which` to see what the directory and current terminal environment resolve to. `which` does not read a Paperclip employee process's environment; run it in the same environment to check an environment binding. The phone's Desk shows waiting requests, trusts, denials and recent actions by machine and role. Roles are read-only there.

## Limits

- Codex's confirmation-free modes and `codex exec` do not fire the hooked `PermissionRequest`, so roles cannot constrain them, including nested Codex sessions in these modes. Before first using interactive Codex, review hooks with `/hooks` inside Codex.
- Calls above `ceiling` require a human decision and cannot be remembered as allowed or covered by a time-limited trust. A `forbid` match is denied outright.
- Roles are not a sandbox. They constrain properly hooked tool calls Railse can recognize, catching mistakes and obvious overreach. They cannot guarantee blocking an injected agent deliberately evading them. Use an OS sandbox or container for hard isolation. See the full [list of limitations](../agent-local-loop.md#角色防什么不防什么) (Chinese).
