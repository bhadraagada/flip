---
name: flip
description: Manage local branch and worktree previews with the Flip CLI. Use when asked to switch dev servers to a Git branch, start or switch a Flip preview, restart its services after edits, inspect running worktrees, or configure Flip for an existing project.
---

# Flip

Use the installed `flip` executable. Run `flip -h` for syntax. If it is unavailable, check `~/go/bin/flip.exe` on Windows or `~/go/bin/flip` on Unix before reinstalling.

## Find the project

Resolve the intended config before issuing commands. Precedence is `-config PATH`, `-project NAME`, `FLIP_CONFIG`, then the nearest ancestor `flip.json`. Linked Git worktrees can resolve a registered config in the same repository or the main checkout config. Use `flip projects` to inspect registrations and `flip -config PATH register NAME` to save one. From unrelated directories, pass `-project NAME` or an absolute `-config` before the command. Unavailable registrations need a corrected path or `unregister NAME`. Read the config's worktree names and service directories; match the requested checkout to those paths rather than assuming its Git branch name is the registered name.

For worktree discovery, use one `discover` service template with worktree-relative directories and omitted HTTP ports. Set `discover.preview: true` for fixed parallel previews. Run `flip discover` to inspect the resulting names, directories and persistent ports; explicit `worktrees` entries override generated entries by name. Restart the supervisor after changing config or adding/removing Git worktrees. Keep `FLIP_HOME` consistent across commands; saved ports stay fixed even when occupied, so resolve startup collisions instead of deleting allocation state.

For initial setup, `flip init` creates a template without overwriting files. Derive real commands, dependency environments, health routes and worktree paths from the project. Commands are argument arrays, with `{port}` substitution and no implicit shell. Keep each port unique and disable automatic port fallback. Configure a `services` map with HTTP service names and worktree `routes` entries containing `prefix`, `service`, and optional `strip_prefix`. One enabled HTTP service defaults to `/`; several require explicit routes. Legacy ui/backend configs remain valid. Set restart_on_use according to reload behavior; named services default false, legacy ui false and backend true. For workers, set `type: "worker"` and opt in with `enabled: true` only when parallel execution is safe. Workers have no port or health route and must stay in the foreground. Their 500 ms startup check proves only that they stayed alive; HTTP health waits for 2xx/3xx.

## Operate

`flip NAME`, `up`, and `restart` start a detached supervisor when absent. Check `flip supervisor status` for its PID and `flip status` for services. Use `flip supervisor stop` to stop the supervisor and all owned services before editing config or changing inherited environment. `serve` remains available for foreground debugging. Windows background windows are hidden. Inspect `.flip/supervisor.log` on startup failure; keep `.flip/*.lock` files in place and stop only owned processes.

`flip NAME` means `flip use NAME`: start missing services, restart those configured with restart_on_use, then select the worktree's routes. Use it when the user wants to change the shared preview. `flip up NAME` starts services without changing selection. After edits that need a restart, use `flip restart NAME SERVICE` with the configured name, including legacy backend/ui. `flip down NAME` stops that worktree's owned services.

Idle shutdown is opt-in through top-level `idle_timeout_seconds`, with zero disabling it. It stops unused worktrees and leaves the supervisor running. Requests, streams and sockets through both preview addresses keep a worktree alive until they close. Status checks, direct internal-port traffic and worker jobs do not count. Enable it only when those jobs may stop after preview inactivity.

Switching affects every tab on the shared browser address. During parallel agent work, keep selection unchanged unless preview switching is part of the requested task; use `up` for preparation. For side-by-side HTTP previews, configure a unique explicit `preview_port` on each worktree before starting the supervisor. `up NAME` publishes that worktree at `http://localhost:PREVIEW_PORT` while keeping shared selection. `down NAME` makes its preview unavailable. `status` lists per-service states and configured preview URLs. Finish active login before switching. Refresh browser tabs after a successful switch.

On failure, use `flip logs NAME SERVICE -n 100`, or add `-f` to follow appended output until Ctrl+C. It works while the supervisor is stopped and supports named HTTP services and workers. Run `flip doctor` for actionable config, executable, port, readiness and routing checks. Doctor never starts, stops or wakes apps; it probes readiness directly only for HTTP services reported running by the authenticated supervisor. Fix the reported issue before retrying. A failed target startup leaves the previous selection unchanged. Stop only Flip-owned processes, not arbitrary processes occupying a port.

Use `flip picker` when the user wants a browser view of worktrees, service states and shared or parallel preview links. Open its private login URL within one minute; never paste the grant into reports or logs. The browser receives a temporary picker-only session, never the CLI control token. Selection has the same restart and shared-tab effects as `flip use`. Refresh status to get a new snapshot, and obtain a new login link after expiry or supervisor restart.

## Routing and completion

Use `flip branch BRANCH` for an existing local Git branch, including names containing `/`. It reuses a checkout or creates and registers one without changing the caller's branch or edits, starts services and selects them after readiness. New entries inherit the active worktree's configuration, receive saved internal ports, use the shared public origin and disable workers. Prepare missing dependencies and ignored environment files in the reported checkout before retrying; a new checkout does not include `.venv`, `node_modules` or `.env`. Absolute command paths within the source checkout are remapped; check custom environment paths and branch-specific requirements. Branch switching requires one Git repository per config. Keep `restart_on_use` enabled for services without reload. The picker's Branches tab invokes this command; its Worktrees tab invokes `use`. The current preview stays first, then recent local activity.

The default browser address is `http://localhost:8080`. UI API calls use `/api`; prefix stripping depends on the backend routes. Keep the existing Google callback URI and its UI path intact. Forward live-reload or application sockets through the public port using the framework's own configuration.

Preview ports on the same host share cookies. Browser origin storage normally differs by port; app credentials and external state may still be shared. Separate ports do not isolate sessions. Each preview OAuth callback URI needs provider registration and app redirect configuration; a callback on the fixed preview reaches the currently selected worktree. Worktrees share any databases or queues named in their environments. Account for incompatible migrations and duplicate background workers before starting additional backends. Keep `.flip` tokens and secrets private and outside frontend-served directories.

Verify command success, the selected name with `status`, and the relevant HTTP behavior when checking a code change. Process status alone does not prove the edited endpoint works. Report the preview URL or exact startup failure.
