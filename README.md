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
flip serve
```

Keep `serve` running and run `flip main` in another terminal to select your configured worktree. Ensure your Go bin directory is on PATH, normally `~/go/bin`.

Or build from a checkout:

```powershell
go build -o flip.exe .
.\flip.exe init
# Edit flip.json with your real paths, commands and health routes.
.\flip.exe serve
```

Keep `serve` running. In another terminal, from the same directory:

```powershell
.\flip.exe up main
.\flip.exe use main
.\flip.exe status
.\flip.exe restart main backend
.\flip.exe down main
```

To install `flip` on your Go binary path, run `go install .`. With that directory on PATH, use `flip main` as shorthand for `flip use main`. Existing commands such as `flip status` still work; use `flip use status` if a worktree has a reserved command name.

On Linux/macOS, build with `go build -o flip .` and use `./flip`. Config lookup uses `-config`, then the `FLIP_CONFIG` environment variable, then `flip.json` in the current directory. Set `FLIP_CONFIG` to an absolute config path to use Flip from any directory. Put flags before the command. `serve` must still be running.

## Configuration

`init` writes an example without overwriting existing files. Edit it before starting `serve`. Add another entry under `worktrees` for each existing checkout; Flip does not create Git worktrees.

- Paths are relative to the config file. Each service has its own working directory.
- Commands are argument arrays, executed directly without a shell. `{port}` expands to that service's configured port. Use an explicit Python executable such as `.venv/Scripts/python.exe` when needed.
- On Windows, invoke executables directly, such as Node with a JavaScript entrypoint. `npm.cmd` requires an explicit shell. You can explicitly use a shell in your command if your project requires one; only use trusted config files.
- Ports must be unique. Configure servers to bind `127.0.0.1`; use Vite `--strictPort` so it cannot silently move to another port.
- `health` is an HTTP route on the internal service. Flip waits for a 2xx or 3xx response; redirects are not followed. Prefer a dedicated unauthenticated readiness route that only succeeds after initialization. The template uses `/`; change it to your service's readiness route.
- Optional `env` is an object of per-service environment overrides. Flip also inherits the environment of `serve`. It does not parse `.env` files; your startup command or app must load them.
- Config is read when `serve` starts. Stop and restart it after editing config. Keep the config unchanged while it runs.

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
| `serve` | Binds the shared preview, configured worktree previews, and control listener. No app starts automatically. |
| `up NAME` | Starts missing enabled services, waits for startup, and publishes its optional worktree preview. Keeps shared selection and running processes. |
| `use NAME` | Starts configured services, restarts those with restart_on_use enabled, then selects the worktree. |
| `restart NAME SERVICE` | Restarts one enabled named service, retaining selection. Legacy `ui` and `backend` still work. |
| `down NAME` | Stops owned services and clears its preview. Clears shared selection if active. |
| `status` | Lists each service, state, PID, selection and configured preview URL. It is not a continuous health monitor. |
| `logs NAME SERVICE [-n 100] [-f]` | Prints recent service output; `-f` follows appended output until Ctrl+C, including across service restarts. Works without the supervisor. |
| `doctor` | Validates config and commands, checks ports and running services' readiness, and reports routing without starting, stopping or waking apps. Failed checks exit nonzero with a suggested fix. |
| `picker` | Prints a temporary login link to the local preview picker on the control port. Requires a running supervisor. |

If a target fails to start, the previous selection remains. Services that started successfully may stay running after a later service fails; `down` cleans it up. Restarting the selected backend creates a short outage. For services without automatic reload, edits require `restart` or another `use` with restart_on_use enabled.

Logs append to `.flip/NAME-SERVICE.log`, including worker output. Legacy names remain `.flip/NAME-ui.log` and `.flip/NAME-backend.log`. Ctrl+C in `serve` stops its owned service trees. Shutdown force-terminates services; it does not promise graceful completion of background jobs. Windows uses Job Objects; Unix uses process groups. Services must remain in the foreground and must not daemonize or escape their process group/job.

## Scope

Flip uses explicit config and one foreground supervisor. No auto-discovery, automatic port allocation, background daemon installation, file watching, or database isolation.

All browser tabs on 8080 share the selection. Refresh them after switching; existing requests and sockets are not migrated. Finish login and active operations before switching. Cookies, local storage, databases, queues and scheduled jobs are not isolated by worktrees. Separate their configuration when branches could conflict.

The control listener defaults to 127.0.0.1:18080 and requires a random token in `.flip/token`. Browser-origin requests to the CLI control API are rejected. Keep `.flip` private and outside your frontend's served directory. Windows file access follows the containing directory's ACL. Do not commit tokens or environment secrets.

## Logs, diagnostics and preview picker

Run `flip logs main web -n 50 -f` to see the last 50 lines and follow new output. Substitute a configured service name, including `ui`, `backend` or a worker. Put log flags after the name and service. `-n 0 -f` starts with new output only. Flip appends to the same log across restarts; external log rotation is not followed. Log names are resolved from validated config entries.

`flip doctor` checks executable lookup without executing commands. It checks internal HTTP health routes only when the authenticated supervisor reports the service running. Stopped services get a port check and a skipped readiness check. Workers have no HTTP health probe, and disabled services are skipped. Routes are inspected without requesting a preview URL, so diagnosis does not trigger lazy wake. An unavailable supervisor is reported with instructions for enabling live checks. Fix configuration errors before rerunning diagnosis.

Run `flip picker` and open the printed URL within one minute. The picker lists configured worktrees and service states, shows the selected shared preview, and links to each configured parallel preview. Select a worktree to run the same operation as `flip use NAME`, including its restart policy. A failed switch displays the error and retains the previous selection. Status is a snapshot; use Refresh status for an update.

The picker runs on the loopback control port, separate from app traffic. Its single-use login grant is in the URL fragment and is removed from browser history when processed. Treat the printed link as private. It is exchanged for a one-hour browser credential stored only in that tab's session storage. This credential permits status and switching, never general supervisor control. The page never receives `.flip/token`, sends no cookies, loads no external resources, and blocks framing. Picker API requests require an exact local Host, same-origin Origin, JSON content type and the scoped bearer credential. Restarting the supervisor invalidates all picker sessions. Reopening the picker after expiry requires a fresh `flip picker` link.

## Checks

```sh
go test ./...
go vet ./...
```

Tests launch real HTTP servers and workers, verify named services, independent previews, disabled workers, immediate worker exits, config validation, and check switching, backend restart, failed-start selection, port collisions, process cleanup, callback routing, API boundaries, prefix stripping, upgraded socket traffic and control authentication.

For the five-worktree CLI check, run `py test_worktrees.py`. Set `FLIP_BINARY` to an absolute worktree-local build path to test changes without replacing an installed binary. It requires Git, Node/npm, FastAPI and Uvicorn. It installs Vite in a temporary project, creates five real Git worktrees, runs ten servers, checks switching and a real HMR update, and stops its processes. The printed temporary directory retains the fixture, logs and `report.json`. It uses free ports so an existing preview on 8080 is left alone. This tests callback routing, not a real Google login.

For named-service CLI coverage, run `python test_services.py PATH_TO_LOCAL_FLIP_BINARY`. It needs only Python, Git and the built binary. It creates two disposable Git worktrees, checks concurrent previews, workers, custom route/restart behavior and cleanup, then prints a retained report path.

References: [Go reverse proxy](https://pkg.go.dev/net/http/httputil#ReverseProxy), [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects), [Vite server options](https://vite.dev/config/server-options), [Google OAuth](https://developers.google.com/identity/protocols/oauth2/web-server).

## Agent skill

The reusable skill is in [skills/flip/SKILL.md](skills/flip/SKILL.md). Copy the `skills/flip` directory into `~/.agents/skills/` for agents that discover shared skills, or `~/.claude/skills/` for Claude Code. The skill teaches configuration discovery, preview switching, backend restarts and coordination between agents sharing one preview.

## Contributing

Issues and pull requests are welcome. Include reproduction steps for bugs and run `go test ./...` and `go vet ./...` before submitting changes. For lifecycle or routing changes, also run the five-worktree integration check when the required tools are available. CI runs the Go checks on Windows, Linux and macOS.

## License

[MIT](LICENSE).
