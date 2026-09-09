# Flip

One browser address for several running worktrees. Written in Go with no third-party dependencies.

Flip owns `http://localhost:8080`. It forwards HTTP and WebSocket traffic to the selected worktree's configured processes. Use any language or framework: a single web server, an API alone, or separate frontend and API servers. Each service controls whether switching restarts it.

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

On Linux/macOS, build with `go build -o flip .` and use `./flip`. Config lookup uses `-config`, then `-project`, then `FLIP_CONFIG`, then the nearest ancestor `flip.json`. From a linked Git worktree with no local config, Flip checks registered configs in the same repository, then the main checkout’s `flip.json`. Multiple registered configs require an explicit `-project`. Put flags before the command. `serve` must still be running.

## Configuration

`init` writes an example without overwriting existing files. Edit it before starting `serve`. Add an entry under `worktrees` for each checkout, or use the discovery template below. Flip does not create Git worktrees.

- Paths are relative to the config file. Each service has its own working directory.
- Commands are argument arrays, executed directly without a shell. `{port}` expands to that service's configured port. Use an explicit Python executable such as `.venv/Scripts/python.exe` when needed.
- On Windows, invoke executables directly, such as Node with a JavaScript entrypoint. `npm.cmd` requires an explicit shell. You can explicitly use a shell in your command if your project requires one; only use trusted config files.
- Ports must be unique. Configure servers to bind `127.0.0.1`; use Vite `--strictPort` so it cannot silently move to another port.
- `health` is an HTTP route on the internal service. Flip waits for a 2xx or 3xx response; redirects are not followed. Prefer a dedicated unauthenticated readiness route that only succeeds after initialization. The template uses `/`; change it to your service's readiness route.
- Optional `env` is an object of per-service environment overrides. Flip also inherits the environment of `serve`. It does not parse `.env` files; your startup command or app must load them.
- Config is read when `serve` starts. Stop and restart it after editing config. Keep the config unchanged while it runs.

The template uses a placeholder command, `your-dev-server`. Replace it with your actual executable and arguments. Flip does not install frameworks or infer their startup flags.

`ui` and `backend` are routing roles, not technology choices. Configure either or both. With only `ui`, all paths go to it unchanged. With only `backend`, all paths go to it; `strip_api_prefix` still applies to matching API paths. With both, the API prefix routes to `backend` and other requests route to `ui`.

Each service accepts `restart_on_use`. Set it to `false` for a server that already reloads edits, or `true` for a process that must restart or rebuild. Defaults preserve existing configurations: `false` for `ui`, `true` for `backend`. Explicit `restart` always restarts the requested service.

For example, an existing Go HTTP application that reads `PORT` can use a backend service with `command: ["go", "run", "."]`, `env: {"PORT": "{port}"}`, and `restart_on_use: true`. A Node application can use `["node", "server.js"]` with the same environment convention. Use the flags or environment variables your own application actually supports.

## Project registration and worktree discovery

Register a config once to operate from any directory:

```sh
flip -config /path/to/project/flip.json register my-app
flip projects
flip -project my-app discover
flip -project my-app serve
# In another terminal:
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

`repo` is relative to the config. Service directories are relative to each discovered worktree and must stay inside it. Replace the placeholder command with the app’s real command. HTTP service ports must be omitted in the template; Flip assigns them. Worker services retain port zero. The template accepts the same `routes` as a named-service worktree. Set `preview: true` to allocate an additional fixed preview port for each worktree; the default is false.

`flip discover` prints names, service ports, preview ports and directories without starting servers. Names come from checkout directory names, with punctuation replaced by hyphens. Duplicate names get path-derived suffixes. Explicit `worktrees` entries with a discovered name replace that whole generated entry, retaining their configured ports and config-relative directories. Other explicit entries also remain available. Legacy `ui` and `backend` configurations still work.

New assignments skip listening ports and saved assignments from other configs. Existing assignments remain unchanged across commands and supervisor restarts, including while services are running. If another process later occupies a saved port, startup fails; Flip does not move a running preview to another port or stop the other process. An unrelated process can still claim a port between allocation and startup. Startup checks and bind errors detect this race. Explicit conflicts with saved assignments fail with an error.

Registrations and port assignments live in the OS user config directory under `flip/state.json`, or in `FLIP_HOME` when set. Updates use a process lock and replace the state file only after config validation succeeds. Deleted or prunable Git worktrees are skipped, and their saved assignments are dropped on the next discovery. Allocations belonging to deleted config files are reclaimed during allocation. A running supervisor keeps its startup snapshot, so stop it before removing worktrees or editing config, then restart it to discover additions. Keep `FLIP_HOME` stable across terminals and outside version control.

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
| `serve` | Starts the loopback proxy and control listener; no app starts automatically. |
| `up NAME` | Starts missing services and waits for readiness. Does not select or restart healthy processes. |
| `use NAME` | Starts configured services, restarts those with restart_on_use enabled, then selects the worktree. |
| `restart NAME backend` | Restarts just the backend, retaining selection. `ui` also works. |
| `down NAME` | Stops that worktree's owned services. Clears selection if active. |
| `status` | Shows process state, PID and selected worktree. It is not a continuous health monitor. |

If a target fails to start, the previous selection remains. A UI that started successfully may stay running after a backend startup failure; `down` cleans it up. Restarting the selected backend creates a short outage. For services without automatic reload, edits require `restart` or another `use` with restart_on_use enabled.

Logs append to `.flip/NAME-ui.log` and `.flip/NAME-backend.log`. Ctrl+C in `serve` stops its owned service trees. Shutdown force-terminates services; it does not promise graceful completion of background jobs. Windows uses Job Objects; Unix uses process groups. Services must remain in the foreground and must not daemonize or escape their process group/job.

## Scope

Flip uses one foreground supervisor per config. No background daemon installation, file watching, or database isolation.

All browser tabs on 8080 share the selection. Refresh them after switching; existing requests and sockets are not migrated. Finish login and active operations before switching. Cookies, local storage, databases, queues and scheduled jobs are not isolated by worktrees. Separate their configuration when branches could conflict.

The control listener defaults to 127.0.0.1:18080 and requires a random token in `.flip/token`. Browser-origin requests are rejected. Keep `.flip` private and outside your frontend's served directory. Windows file access follows the containing directory's ACL. Do not commit tokens or environment secrets.

## Checks

```sh
go test ./...
go vet ./...
```

Discovery checks create five real Git worktrees, exercise concurrent processes, occupied ports, stable assignments, explicit overrides and stale registrations. For a live CLI check using only Git and Python, build a local binary and run `python test_discovery.py /path/to/flip`. It checks five fixed preview URLs, shared switching, supervisor restart and cleanup without using port 8080.

Tests launch real child HTTP servers and check switching, backend restart, failed-start selection, port collisions, process cleanup, callback routing, API boundaries, prefix stripping, upgraded socket traffic and control authentication.

For the full installed-CLI check, run `py test_worktrees.py`. It requires Git, Node/npm, FastAPI and Uvicorn. It installs Vite in a temporary project, creates five real Git worktrees, runs ten servers, checks switching and a real HMR update, and stops its processes. The printed temporary directory retains the fixture, logs and `report.json`. It uses free ports so an existing preview on 8080 is left alone. This tests callback routing, not a real Google login.

References: [Go reverse proxy](https://pkg.go.dev/net/http/httputil#ReverseProxy), [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects), [Vite server options](https://vite.dev/config/server-options), [Google OAuth](https://developers.google.com/identity/protocols/oauth2/web-server).

## Agent skill

The reusable skill is in [skills/flip/SKILL.md](skills/flip/SKILL.md). Copy the `skills/flip` directory into `~/.agents/skills/` for agents that discover shared skills, or `~/.claude/skills/` for Claude Code. The skill teaches configuration discovery, preview switching, backend restarts and coordination between agents sharing one preview.

## Contributing

Issues and pull requests are welcome. Include reproduction steps for bugs and run `go test ./...` and `go vet ./...` before submitting changes. For lifecycle or routing changes, also run the five-worktree integration check when the required tools are available. CI runs the Go checks on Windows, Linux and macOS.

## License

[MIT](LICENSE).
## Named services and preview contract

Each worktree may use `services: {"web": {...}, "api": {...}}` instead of legacy `ui`/`backend`. Mixing these forms in one worktree is rejected. Services retain `dir`, `command`, `port`, `health`, `env`, and `restart_on_use`. `type` defaults to `http`; named services default `restart_on_use` to false. Legacy `ui` and `backend` keep their existing defaults and routing.

`routes` is a per-worktree array such as `[{"prefix":"/api","service":"api","strip_prefix":true},{"prefix":"/","service":"web"}]`. The longest matching path prefix wins at segment boundaries. Unmatched paths return 404. A single enabled HTTP service gets a `/` route when routes are omitted; several HTTP services require explicit routes. Routes can target only enabled HTTP services.

A service with `type: "worker"` starts only with `enabled: true`. It runs in the foreground, has no port or HTTP health route, and is considered started after remaining alive for 500 ms. This detects immediate exits, not application readiness. Enabled HTTP services and workers must finish startup before selection or preview publication. HTTP readiness remains a 2xx/3xx health response. There is no dependency scheduler; startup uses sorted service names. Configure workers only when safe to run alongside other worktrees. `enabled: false` may also disable HTTP services.

Optional `preview_port` on a worktree reserves a unique loopback port when `serve` starts. After `up NAME` or successful `use NAME`, `http://localhost:PREVIEW_PORT` routes to that worktree regardless of the fixed preview selection. `down NAME` makes its preview unavailable. The fixed `port` and `use` behavior stay intact. Ports are explicit and globally unique within the config; automatic assignment is deferred to discovery.

`restart NAME SERVICE` accepts any configured enabled service name. The existing control request shape, `{Action, Name, Part}`, is unchanged; `Part` is the service name. `status` lists each named service, process state, selection, and optional preview URL. Logs use `.flip/NAME-SERVICE.log`.

Separate preview ports do not isolate sessions. Cookies are shared across ports on the same host, while browser origin storage normally differs by port. Apps may still share backends, credentials, and external state. OAuth providers must explicitly allow each callback URI used by a separate preview; an existing callback on the fixed preview continues to reach its selected worktree. Finish login before switching that preview.
