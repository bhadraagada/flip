# Flip

One browser address for several running worktrees. Written in Go with no third-party dependencies.

Flip owns `http://localhost:8080`. It forwards HTTP and WebSocket traffic to the selected worktree's configured processes. Use any language or framework: a single web server, several named HTTP services, or HTTP services with opt-in foreground workers. Optional per-worktree ports keep several previews open side by side. Each service controls whether switching restarts it.

## Start

Requires Go 1.26 or newer to build. Your app still needs its own Python/Node dependencies.

Install directly from GitHub:

```sh
go install github.com/bhadraagada/flip@latest
flip init
# Edit flip.json with your app paths and commands.
flip main
```

`flip main` starts a background supervisor and selects your configured worktree. The CLI returns while the preview stays running. Ensure your Go bin directory is on PATH, normally `~/go/bin`.

Or build from a checkout:

```powershell
go build -o flip.exe .
.\flip.exe init
# Edit flip.json with your real paths, commands and health routes.
.\flip.exe main
```

From the same directory:

```powershell
.\flip.exe up main
.\flip.exe use main
.\flip.exe status
.\flip.exe restart main backend
.\flip.exe down main
```

To install `flip` on your Go binary path, run `go install .`. With that directory on PATH, use `flip main` as shorthand for `flip use main`. Existing commands such as `flip status` still work; use `flip use status` if a worktree has a reserved command name.

On Linux/macOS, build with `go build -o flip .` and use `./flip`. Config lookup uses `-config`, then `-project`, then `FLIP_CONFIG`, then the nearest ancestor `flip.json`. From a linked Git worktree with no local config, Flip checks registered configs in the same repository, then the main checkout's `flip.json`. Multiple registered configs require an explicit `-project`. Put flags before the command.

## Configuration

`init` writes an example without overwriting existing files. Edit it before starting your first preview. Add an entry under `worktrees` for each checkout, or use the discovery template below. Flip does not create Git worktrees.

- Paths are relative to the config file. Each service has its own working directory.
- Commands are argument arrays, executed directly without a shell. `{port}` expands to that service's configured port. Use an explicit Python executable such as `.venv/Scripts/python.exe` when needed.
- On Windows, invoke executables directly, such as Node with a JavaScript entrypoint. `npm.cmd` requires an explicit shell. You can explicitly use a shell in your command if your project requires one; only use trusted config files.
- Ports must be unique. Configure servers to bind `127.0.0.1`; use Vite `--strictPort` so it cannot silently move to another port.
- `health` is an HTTP route on the internal service. Flip waits for a 2xx or 3xx response; redirects are not followed. Prefer a dedicated unauthenticated readiness route that only succeeds after initialization. The template uses `/`; change it to your service's readiness route.
- Optional `env` is an object of per-service environment overrides. Flip also inherits the environment of the command that starts the supervisor. It does not parse `.env` files; your startup command or app must load them.
- Config is read at supervisor startup. Run `flip supervisor stop` before editing config or changing inherited environment, then run `flip NAME` to restart it. Keep the config unchanged while it runs.

The template uses a placeholder command, `your-dev-server`. Replace it with your actual executable and arguments. Flip does not install frameworks or infer their startup flags.

`ui` and `backend` are routing roles, not technology choices. Legacy configs can configure either or both. They normalize to named services internally. With only `ui`, all paths go to it unchanged. With only `backend`, all paths go to it; `strip_api_prefix` still applies to matching API paths. With both, the API prefix routes to `backend` and other requests route to `ui`.

Each service accepts `restart_on_use`. Set it to `false` for a server that already reloads edits, or `true` for a process that must restart or rebuild. Named services default to `false`. Legacy defaults remain `false` for `ui`, `true` for `backend`. Explicit `restart` always restarts the requested service.

For example, an existing Go HTTP application that reads `PORT` can use a backend service with `command: ["go", "run", "."]`, `env: {"PORT": "{port}"}`, and `restart_on_use: true`. A Node application can use `["node", "server.js"]` with the same environment convention. Use the flags or environment variables your own application actually supports.

## Project registration and worktree discovery

Register a config once to operate from any directory:

```sh
flip -config /path/to/project/flip.json register my-app
flip projects
flip -project my-app discover
flip -project my-app up feature-a
flip unregister my-app
```

Registration saves the absolute config path. `projects` marks missing configs as unavailable; `unregister` removes the name without stopping anything. A missing config can be replaced by registering the name again. An existing registration cannot be silently redirected to another existing config. `init` always writes to `-config`, `FLIP_CONFIG`, or the current directory, even when an ancestor config exists.

Use one service template for every existing Git worktree:

```json
{
  "port": 8080,
  "control_port": 18080,
  "discover": {
    "repo": ".",
    "preview": true,
    "port_min": 20000,
    "port_max": 40000,
    "services": {
      "web": {
        "dir": ".",
        "command": ["your-dev-server", "--port", "{port}"],
        "health": "/"
      }
    }
  }
}
```

`repo` is relative to the config. Service directories are relative to each discovered worktree and must stay inside it. Replace the placeholder command with the app's real command. HTTP service ports must be omitted in the template; Flip assigns them. Worker services retain port zero. The template accepts the same `routes` as a named-service worktree. Set `preview: true` to allocate an additional fixed preview port for each worktree; the default is false.

`flip discover` prints names, service ports, preview ports and directories without starting servers. Names come from checkout directory names, with punctuation replaced by hyphens. Duplicate names get path-derived suffixes. Explicit `worktrees` entries with a discovered name replace that whole generated entry, retaining their configured ports and config-relative directories. Other explicit entries also remain available. Legacy `ui` and `backend` configurations still work.

New assignments skip listening ports and saved assignments from other configs. Existing assignments remain unchanged across commands and supervisor restarts, including while services are running. If another process later occupies a saved port, startup fails; Flip does not move a running preview to another port or stop the other process. An unrelated process can still claim a port between allocation and startup. Startup checks and bind errors detect this race. Explicit conflicts with saved assignments fail with an error.

Registrations and port assignments live in the OS user config directory under `flip/state.json`, or in `FLIP_HOME` when set. Updates use a process lock and replace the state file only after config validation succeeds. Deleted or prunable Git worktrees are skipped, and their saved assignments are dropped on the next discovery. Allocations belonging to deleted config files are reclaimed during allocation. A running supervisor keeps its startup snapshot, so stop it before removing worktrees or editing config, then restart it to discover additions. Keep `FLIP_HOME` stable across terminals and outside version control.

## Named services and preview contract

Each worktree may use `services: {"web": {...}, "api": {...}}` instead of legacy `ui`/`backend`. Mixing these forms in one worktree is rejected. Services retain `dir`, `command`, `port`, `health`, `env`, and `restart_on_use`. `type` defaults to `http`; named services default `restart_on_use` to false. Legacy `ui` and `backend` keep their existing defaults and routing.

`routes` is a per-worktree array such as `[{"prefix":"/api","service":"api","strip_prefix":true},{"prefix":"/","service":"web"}]`. The longest matching path prefix wins at segment boundaries. Unmatched paths return 404. A single enabled HTTP service gets a `/` route when routes are omitted; several HTTP services require explicit routes. Routes can target only enabled HTTP services.

A service with `type: "worker"` starts only with `enabled: true`. It runs in the foreground, has no port or HTTP health route, and is considered started after remaining alive for 500 ms. This detects immediate exits, not application readiness. Enabled HTTP services and workers must finish startup before selection or preview publication. HTTP readiness remains a 2xx/3xx health response. There is no dependency scheduler; startup uses sorted service names. Configure workers only when safe to run alongside other worktrees. `enabled: false` may also disable HTTP services.

Example named-service worktree entry, with paths and commands adapted to your app:

```json
{
  "preview_port": 8091,
  "services": {
    "web": {"dir": "../app", "command": ["node", "web.js"], "port": 8092, "env": {"PORT": "{port}"}},
    "api": {"dir": "../app", "command": ["node", "api.js"], "port": 8093, "env": {"PORT": "{port}"}, "health": "/ready"},
    "jobs": {"type": "worker", "enabled": true, "dir": "../app", "command": ["node", "worker.js"]}
  },
  "routes": [
    {"prefix": "/api", "service": "api", "strip_prefix": true},
    {"prefix": "/", "service": "web"}
  ]
}
```

Optional `preview_port` on a worktree reserves a unique loopback port when the supervisor starts. After `up NAME` or successful `use NAME`, `http://localhost:PREVIEW_PORT` routes to that worktree regardless of the fixed preview selection. `down NAME` makes its preview unavailable. The fixed `port` and `use` behavior stay intact. Ports are unique within the config; the discovery template can assign them automatically.

`restart NAME SERVICE` accepts any configured enabled service name. The existing control request shape, `{Action, Name, Part}`, is unchanged; `Part` is the service name. `status` lists each named service, process state, selection, and optional preview URL. Logs use `.flip/NAME-SERVICE.log`.

Separate preview ports do not isolate sessions. Cookies are shared across ports on the same host, while browser origin storage normally differs by port. Apps may still share backends, credentials, and external state. OAuth providers must explicitly allow each callback URI used by a separate preview; an existing callback on the fixed preview continues to reach its selected worktree. Finish login before switching that preview.

## UI and Google login

Set your UI's API base to `/api` so requests pass through 8080. By default Flip preserves the prefix. Set `strip_api_prefix` to `true` if `/api/users` should reach FastAPI as `/users`. `/apiculture` does not match `/api`.

Keep Google's registered callback URI exactly as it is, including its existing path on `http://localhost:8080`. Keep that UI callback outside the API prefix. If your UI exchanges the returned code through FastAPI, its API request follows the selected backend.

Flip forwards WebSocket upgrades. If Vite's socket client targets its internal port, configure the browser-facing client port to 8080 using the option supported by your installed Vite version. Backend socket endpoints must be under the configured API prefix.

Forwarded headers describe the public request. Flip preserves its Host header. Configure your app to trust only the local proxy when consuming forwarded headers, and keep public redirect URLs on 8080.

## Command behavior

| Command | Behavior |
| --- | --- |
| `discover` | Lists discovered worktrees and saves stable port assignments without starting services. |
| `register NAME` | Saves the resolved config under a project name. |
| `projects` | Lists project registrations and unavailable configs. |
| `unregister NAME` | Removes a project name. |
| `serve` | Runs the supervisor in the foreground for debugging. Ctrl+C stops its owned services. |
| `supervisor status` | Reports the authenticated supervisor PID without starting it. |
| `supervisor stop` | Stops the supervisor and all its owned services, then waits for cleanup. |
| `up NAME` | Starts missing enabled services, waits for startup, and publishes its optional worktree preview. Keeps shared selection and running processes. |
| `use NAME` | Starts configured services, restarts those with restart_on_use enabled, then selects the worktree. |
| `restart NAME SERVICE` | Restarts one enabled named service, retaining selection. Legacy `ui` and `backend` still work. |
| `down NAME` | Stops owned services and clears its preview. Clears shared selection if active. |
| `status` | Lists each service, state, PID, selection and configured preview URL. It is not a continuous health monitor. |
| `logs NAME SERVICE [-n 100] [-f]` | Prints recent service output; `-f` follows appended output until Ctrl+C, including across service restarts. Works without the supervisor. |
| `doctor` | Validates config and commands, checks ports and running services' readiness, and reports routing without starting, stopping or waking apps. Failed checks exit nonzero with a suggested fix. |
| `picker` | Prints a temporary login link to the local preview picker on the control port. Requires a running supervisor. |

If a target fails to start, the previous selection remains. Services that started successfully may stay running after a later service fails; `down` cleans it up. Restarting the selected backend creates a short outage. For services without automatic reload, edits require `restart` or another `use` with restart_on_use enabled.

`NAME`, `use`, `up`, and `restart` start the supervisor automatically when needed. `status`, `supervisor status`, `supervisor stop`, and `down` never start it.

Logs append to `.flip/NAME-SERVICE.log`; detached supervisor diagnostics go to `.flip/supervisor.log`. Ctrl+C in `serve` stops its owned service trees. Shutdown force-terminates services; it does not promise graceful completion of background jobs. Windows uses Job Objects; Unix uses process groups. Services must remain in the foreground and must not daemonize or escape their process group/job.

## Automatic startup and idle shutdown

Concurrent CLI starts share one supervisor per config directory. OS-held locks survive CLI exit and release when the supervisor exits or crashes. Keep the empty `.flip/*.lock` files in place; deleting a live lock file can defeat locking on Unix. The supervisor binds every listener before replacing a stale token. A failed bind leaves the occupying process alone. No cleanup command kills a PID read from a stale file.

Windows background windows stay hidden. Normal shutdown stops owned process trees on Windows and Unix; Unix also handles SIGTERM. Windows Job Objects clean services up after a supervisor crash. An uncatchable Unix kill such as SIGKILL can leave service groups running; Flip reports their occupied ports on the next startup instead of killing unverified processes.

Idle shutdown is disabled by default. Set top-level `"idle_timeout_seconds": 900` to stop a worktree after fifteen minutes without traffic through either its shared or independent preview. Startup attempts, switches and restarts reset the timer, including attempts that leave partially started services. In-flight requests, streaming responses and upgraded sockets keep their original worktree alive for the whole connection, even after a shared-preview switch. Status checks and ordinary HTTP keep-alive connections between requests do not reset the timer.

Expiry stops the worktree's services and clears its routes. The supervisor stays available; run `flip NAME` or `flip up NAME` to start that worktree again. Traffic sent directly to an internal service port and background worker jobs cannot be observed by this timer. Enable it only when stopping those jobs after preview inactivity is acceptable.

## Scope

The supervisor is an ordinary detached process, not an installed OS service. It does not start at login. No file watching or database isolation is provided.

All browser tabs on 8080 share the selection. Refresh them after switching; existing requests and sockets are not migrated. Finish login and active operations before switching. Cookies, local storage, databases, queues and scheduled jobs are not isolated by worktrees. Separate their configuration when branches could conflict.

The control listener defaults to 127.0.0.1:18080 and requires a random token in `.flip/token`. Browser-origin requests to the CLI control API are rejected. Keep `.flip` private and outside your frontend's served directory. Windows file access follows the containing directory's ACL. Do not commit tokens or environment secrets.

## Logs, diagnostics and preview picker

Run `flip logs main web -n 50 -f` to see the last 50 lines and follow new output. Substitute a configured service name, including `ui`, `backend` or a worker. Put log flags after the name and service. `-n 0 -f` starts with new output only. Flip appends to the same log across restarts; external log rotation is not followed. Log names are resolved from validated config entries.

`flip doctor` checks executable lookup without executing commands. It checks internal HTTP health routes only when the authenticated supervisor reports the service running. Stopped services get a port check and a skipped readiness check. Workers have no HTTP health probe, and disabled services are skipped. Routes are inspected without requesting a preview URL. Direct readiness probes do not reset Flip's idle timer. With discovery enabled, doctor and logs reuse saved assignments; run `flip discover` first if new worktrees need ports. An unavailable supervisor is reported with instructions for enabling live checks. Fix configuration errors before rerunning diagnosis.

Run `flip picker` and open the printed URL within one minute. The picker lists configured worktrees and service states, shows the selected shared preview, and links to each configured parallel preview. Select a worktree to run the same operation as `flip use NAME`, including its restart policy. A failed switch displays the error and retains the previous selection. Status is a snapshot; use Refresh status for an update.

The picker runs on the loopback control port, separate from app traffic. Its single-use login grant is in the URL fragment and is removed from browser history when processed. Treat the printed link as private. It is exchanged for a one-hour browser credential stored only in that tab's session storage. This credential permits status and switching, never general supervisor control. The page never receives `.flip/token`, sends no cookies, loads no external resources, and blocks framing. Picker API requests require an exact local Host, same-origin Origin, JSON content type and the scoped bearer credential. Restarting the supervisor invalidates all picker sessions. Reopening the picker after expiry requires a fresh `flip picker` link.

## Checks

For logs, diagnostics and picker authentication, build a local binary and run `py tests/integration/test_experience.py --flip ./flip.exe` or use `python3` and `./flip` on Unix. It needs Git and Node, creates five disposable worktrees under `work/`, and checks CLI log following across restarts, shared and parallel routing, read-only diagnostics, failed switching and scoped browser authentication. It never invokes the globally installed Flip binary. Browser UI checks should also cover keyboard switching, the unauthenticated view and startup errors.

```sh
go test ./...
go vet ./...
```

Discovery checks create five real Git worktrees, exercise concurrent processes, occupied ports, stable assignments, explicit overrides and stale registrations. For a live CLI check using only Git and Python, build a local binary and run `python tests/integration/test_discovery.py /path/to/flip`. It checks five fixed preview URLs, shared switching, detached startup through a registered project, supervisor restart and cleanup without using port 8080.

Tests launch real HTTP servers and workers, verify named services, independent previews, disabled workers, immediate worker exits, config validation, and check switching, backend restart, failed-start selection, port collisions, process cleanup, callback routing, API boundaries, prefix stripping, upgraded socket traffic and control authentication.

`TestDetachedSupervisor` builds a temporary CLI, or uses `FLIP_TEST_BINARY` when set, and launches separate CLI processes to check concurrent startup, stale tokens, streaming/socket idle protection, readiness timeouts and shutdown cleanup.

For the five-worktree CLI check, run `py tests/integration/test_worktrees.py`. Set `FLIP_BINARY` to an absolute worktree-local build path to test changes without replacing an installed binary. It requires Git, Node/npm, FastAPI and Uvicorn. It installs Vite in a temporary project, creates five real Git worktrees, runs ten servers, checks switching and a real HMR update, and stops its processes. The printed temporary directory retains the fixture, logs and `report.json`. It uses free ports so an existing preview on 8080 is left alone. This tests callback routing, not a real Google login.

For named-service CLI coverage, run `python tests/integration/test_services.py PATH_TO_LOCAL_FLIP_BINARY`. It needs only Python, Git and the built binary. It creates two disposable Git worktrees, checks concurrent previews, workers, custom route/restart behavior and cleanup, then prints a retained report path.

References: [Go reverse proxy](https://pkg.go.dev/net/http/httputil#ReverseProxy), [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects), [Vite server options](https://vite.dev/config/server-options), [Google OAuth](https://developers.google.com/identity/protocols/oauth2/web-server).

## Agent skill

The reusable skill is in [skills/flip/SKILL.md](../skills/flip/SKILL.md). Copy the `skills/flip` directory into `~/.agents/skills/` for agents that discover shared skills, or `~/.claude/skills/` for Claude Code. The skill teaches configuration discovery, preview switching, backend restarts and coordination between agents sharing one preview.

## Contributing

Issues and pull requests are welcome. Include reproduction steps for bugs and run `go test ./...` and `go vet ./...` before submitting changes. For lifecycle or routing changes, also run the five-worktree integration check when the required tools are available. CI runs the Go checks on Windows, Linux and macOS.

## License

[MIT](../LICENSE).

## Branch switching

```sh
flip branch feature/login
flip -project prism branch feature/login
```

Switches the dev servers and shared preview to an existing **local Git branch**. Flip reuses its configured worktree, registers an existing checkout, or creates one under `.flip/worktrees/` beside the config. Your current checkout and uncommitted edits stay intact. Nothing is fetched or committed.

New entries inherit the selected worktree's service commands, routes, environment and restart policy (otherwise `main`, then the first Git-backed entry). Service directories and absolute command paths inside the source checkout follow the new checkout; HTTP ports are unique and saved in the config. New workers stay disabled and new entries use the fixed shared preview address, keeping OAuth callbacks on the same origin. Branch switching requires a configuration whose services belong to one Git repository.

A new checkout needs its own dependencies and ignored environment files. Flip does not copy `.env`, install packages, run migrations or provision databases. If startup fails, prepare the reported service directories and retry the same command. The previous preview stays selected. Enable `restart_on_use` on services without reload so selecting a branch picks up later code edits.

The picker has **Worktrees** and **Branches** tabs; both switch the running dev servers. The selected preview stays first, followed by recent activity from preview use, local file edits, Git checkout history and commits. Branches without a checkout use their last commit time. Status and activity refresh when you press Refresh; preview usage times reset when the supervisor restarts. Git-ignored files are excluded from edit activity.
