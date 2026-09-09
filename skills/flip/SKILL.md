---
name: flip
description: Manage local worktree previews with the Flip CLI. Use when asked to start or switch a Flip preview, restart its services after edits, inspect running worktrees, or configure Flip for an existing project.
---

# Flip

Use the installed `flip` executable. Run `flip -h` for syntax. If it is unavailable, check `~/go/bin/flip.exe` on Windows or `~/go/bin/flip` on Unix before reinstalling.

## Find the project

Resolve the intended config before issuing commands. Precedence is `-config PATH`, `FLIP_CONFIG`, then `flip.json` in the current directory. Pass an absolute `-config` before the command when operating from another directory. Read the config's worktree names and service directories; match the requested checkout to those paths rather than assuming its Git branch name is the registered name.

For initial setup, `flip init` creates a template without overwriting files. Derive real commands, dependency environments, health routes and worktree paths from the project. Commands are argument arrays, with `{port}` substitution and no implicit shell. Keep each port unique and disable automatic port fallback. Configure ui, backend, or both as HTTP routing roles; commands may use any language or framework. Set restart_on_use according to each service's reload behavior. Defaults are false for ui and true for backend.

## Operate

`flip NAME`, `up`, and `restart` start a detached supervisor when absent. Check `flip supervisor status` for its PID and `flip status` for services. Use `flip supervisor stop` to stop the supervisor and all owned services before editing config or changing inherited environment. `serve` remains available for foreground debugging. Windows background windows are hidden. Inspect `.flip/supervisor.log` on startup failure; keep `.flip/*.lock` files in place and stop only owned processes.

`flip NAME` means `flip use NAME`: start missing services, restart those configured with restart_on_use, then select the worktree's routes. Use it when the user wants to change the shared preview. `flip up NAME` starts services without changing selection. After edits that need a restart, use `flip restart NAME backend` or `flip restart NAME ui` for the configured service. `flip down NAME` stops that worktree's owned services.

Idle shutdown is opt-in through top-level `idle_timeout_seconds`, with zero disabling it. It stops unused worktrees and leaves the supervisor running. Requests, streams and sockets through both preview addresses keep a worktree alive until they close. Status checks, direct internal-port traffic and worker jobs do not count. Enable it only when those jobs may stop after preview inactivity.

Switching affects every tab on the shared browser address. During parallel agent work, keep selection unchanged unless preview switching is part of the requested task; use `up` for preparation. Finish active login before switching. Refresh browser tabs after a successful switch.

On failure, read `.flip/NAME-ui.log` or `.flip/NAME-backend.log` beside the config. A failed target startup leaves the previous selection unchanged; restarting the selected backend briefly interrupts it. Fix the reported command, dependency, port or readiness issue before retrying. Stop only Flip-owned processes, not arbitrary processes occupying a port.

## Routing and completion

The default browser address is `http://localhost:8080`. UI API calls use `/api`; prefix stripping depends on the backend routes. Keep the existing Google callback URI and its UI path intact. Forward live-reload or application sockets through the public port using the framework's own configuration.

Worktrees share cookies, storage and any databases or queues named in their environments. Account for incompatible migrations and duplicate background workers before starting additional backends. Keep `.flip` tokens and secrets private and outside frontend-served directories.

Verify command success, the selected name with `status`, and the relevant HTTP behavior when checking a code change. Process status alone does not prove the edited endpoint works. Report the preview URL or exact startup failure.
