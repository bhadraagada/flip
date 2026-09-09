---
name: flip
description: Manage local worktree previews with the Flip CLI. Use when asked to start or switch a Flip preview, restart its services after edits, inspect running worktrees, or configure Flip for an existing project.
---

# Flip

Use the installed `flip` executable. Run `flip -h` for syntax. If it is unavailable, check `~/go/bin/flip.exe` on Windows or `~/go/bin/flip` on Unix before reinstalling.

## Find the project

Resolve the intended config before issuing commands. Precedence is `-config PATH`, `FLIP_CONFIG`, then `flip.json` in the current directory. Pass an absolute `-config` before the command when operating from another directory. Read the config's worktree names and service directories; match the requested checkout to those paths rather than assuming its Git branch name is the registered name.

For initial setup, `flip init` creates a template without overwriting files. Derive real commands, dependency environments, health routes and worktree paths from the project. Commands are argument arrays, with `{port}` substitution and no implicit shell. Keep each port unique and disable automatic port fallback. Configure a `services` map with HTTP service names and worktree `routes` entries containing `prefix`, `service`, and optional `strip_prefix`. One enabled HTTP service defaults to `/`; several require explicit routes. Legacy ui/backend configs remain valid. Set restart_on_use according to reload behavior; named services default false, legacy ui false and backend true. For workers, set `type: "worker"` and opt in with `enabled: true` only when parallel execution is safe. Workers have no port or health route and must stay in the foreground. Their 500 ms startup check proves only that they stayed alive; HTTP health waits for 2xx/3xx.

## Operate

Check `flip status`. If the supervisor is absent, start `flip serve` using the intended config in a persistent terminal or managed session. The supervisor must remain alive; avoid temporary command runners that kill their processes on return. On Windows, hide any background helper window. Keep its logs and process identity available for cleanup.

`flip NAME` means `flip use NAME`: start missing services, restart those configured with restart_on_use, then select the worktree's routes. Use it when the user wants to change the shared preview. `flip up NAME` starts services without changing selection. After edits that need a restart, use `flip restart NAME SERVICE` with the configured name, including legacy backend/ui. `flip down NAME` stops that worktree's owned services.

Switching affects every tab on the shared browser address. During parallel agent work, keep selection unchanged unless preview switching is part of the requested task; use `up` for preparation. For side-by-side HTTP previews, configure a unique explicit `preview_port` on each worktree before starting serve. `up NAME` publishes that worktree at `http://localhost:PREVIEW_PORT` while keeping shared selection. `down NAME` makes its preview unavailable. `status` lists per-service states and configured preview URLs. Finish active login before switching. Refresh browser tabs after a successful switch.

On failure, read `.flip/NAME-SERVICE.log` beside the config. A failed target startup leaves the previous selection unchanged; restarting the selected backend briefly interrupts it. Fix the reported command, dependency, port or readiness issue before retrying. Stop only Flip-owned processes, not arbitrary processes occupying a port.

## Routing and completion

The default browser address is `http://localhost:8080`. UI API calls use `/api`; prefix stripping depends on the backend routes. Keep the existing Google callback URI and its UI path intact. Forward live-reload or application sockets through the public port using the framework's own configuration.

Preview ports on the same host share cookies. Browser origin storage normally differs by port; app credentials and external state may still be shared. Separate ports do not isolate sessions. Each preview OAuth callback URI needs provider registration and app redirect configuration; a callback on the fixed preview reaches the currently selected worktree. Worktrees share any databases or queues named in their environments. Account for incompatible migrations and duplicate background workers before starting additional backends. Keep `.flip` tokens and secrets private and outside frontend-served directories.

Verify command success, the selected name with `status`, and the relevant HTTP behavior when checking a code change. Process status alone does not prove the edited endpoint works. Report the preview URL or exact startup failure.
