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

On Linux/macOS, build with `go build -o flip .` and use `./flip`. Config lookup uses `-config`, then the `FLIP_CONFIG` environment variable, then `flip.json` in the current directory. Set `FLIP_CONFIG` to an absolute config path to use Flip from any directory. Put flags before the command.

## Configuration

`init` writes an example without overwriting existing files. Edit it before starting your first preview. Add another entry under `worktrees` for each existing checkout; Flip does not create Git worktrees.

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

Optional `preview_port` on a worktree reserves a unique loopback port when `serve` starts. After `up NAME` or successful `use NAME`, `http://localhost:PREVIEW_PORT` routes to that worktree regardless of the fixed preview selection. `down NAME` makes its preview unavailable. The fixed `port` and `use` behavior stay intact. Ports are explicit and globally unique within the config; automatic assignment is deferred to discovery.

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
| `serve` | Runs the supervisor in the foreground for debugging. Ctrl+C stops its owned services. |
| `supervisor status` | Reports the authenticated supervisor PID without starting it. |
| `supervisor stop` | Stops the supervisor and all its owned services, then waits for cleanup. |
| `up NAME` | Starts missing enabled services, waits for startup, and publishes its optional worktree preview. Keeps shared selection and running processes. |
| `use NAME` | Starts configured services, restarts those with restart_on_use enabled, then selects the worktree. |
| `restart NAME SERVICE` | Restarts one enabled named service, retaining selection. Legacy `ui` and `backend` still work. |
| `down NAME` | Stops owned services and clears its preview. Clears shared selection if active. |
| `status` | Lists each service, state, PID, selection and configured preview URL. It is not a continuous health monitor. |

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

The control listener defaults to 127.0.0.1:18080 and requires a random token in `.flip/token`. Browser-origin requests are rejected. Keep `.flip` private and outside your frontend's served directory. Windows file access follows the containing directory's ACL. Do not commit tokens or environment secrets.

## Checks

```sh
go test ./...
go vet ./...
```

Tests launch real HTTP servers and workers, verify named services, independent previews, disabled workers, immediate worker exits, config validation, and check switching, backend restart, failed-start selection, port collisions, process cleanup, callback routing, API boundaries, prefix stripping, upgraded socket traffic and control authentication.

`TestDetachedSupervisor` builds a temporary CLI, or uses `FLIP_TEST_BINARY` when set, and launches separate CLI processes to check concurrent startup, stale tokens, streaming/socket idle protection, readiness timeouts and shutdown cleanup.

For the five-worktree CLI check, run `py test_worktrees.py`. Set `FLIP_BINARY` to an absolute worktree-local build path to test changes without replacing an installed binary. It requires Git, Node/npm, FastAPI and Uvicorn. It installs Vite in a temporary project, creates five real Git worktrees, runs ten servers, checks switching and a real HMR update, and stops its processes. The printed temporary directory retains the fixture, logs and `report.json`. It uses free ports so an existing preview on 8080 is left alone. This tests callback routing, not a real Google login.

For named-service CLI coverage, run `python test_services.py PATH_TO_LOCAL_FLIP_BINARY`. It needs only Python, Git and the built binary. It creates two disposable Git worktrees, checks concurrent previews, workers, custom route/restart behavior and cleanup, then prints a retained report path.

References: [Go reverse proxy](https://pkg.go.dev/net/http/httputil#ReverseProxy), [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects), [Vite server options](https://vite.dev/config/server-options), [Google OAuth](https://developers.google.com/identity/protocols/oauth2/web-server).

## Agent skill

The reusable skill is in [skills/flip/SKILL.md](skills/flip/SKILL.md). Copy the `skills/flip` directory into `~/.agents/skills/` for agents that discover shared skills, or `~/.claude/skills/` for Claude Code. The skill teaches configuration discovery, preview switching, backend restarts and coordination between agents sharing one preview.

## Contributing

Issues and pull requests are welcome. Include reproduction steps for bugs and run `go test ./...` and `go vet ./...` before submitting changes. For lifecycle or routing changes, also run the five-worktree integration check when the required tools are available. CI runs the Go checks on Windows, Linux and macOS.

## License

[MIT](LICENSE).
