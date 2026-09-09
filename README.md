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

`ui` and `backend` are routing roles, not technology choices. Configure either or both. With only `ui`, all paths go to it unchanged. With only `backend`, all paths go to it; `strip_api_prefix` still applies to matching API paths. With both, the API prefix routes to `backend` and other requests route to `ui`.

Each service accepts `restart_on_use`. Set it to `false` for a server that already reloads edits, or `true` for a process that must restart or rebuild. Defaults preserve existing configurations: `false` for `ui`, `true` for `backend`. Explicit `restart` always restarts the requested service.

For example, an existing Go HTTP application that reads `PORT` can use a backend service with `command: ["go", "run", "."]`, `env: {"PORT": "{port}"}`, and `restart_on_use: true`. A Node application can use `["node", "server.js"]` with the same environment convention. Use the flags or environment variables your own application actually supports.

## UI and Google login

Set your UI's API base to `/api` so requests pass through 8080. By default Flip preserves the prefix. Set `strip_api_prefix` to `true` if `/api/users` should reach FastAPI as `/users`. `/apiculture` does not match `/api`.

Keep Google's registered callback URI exactly as it is, including its existing path on `http://localhost:8080`. Keep that UI callback outside the API prefix. If your UI exchanges the returned code through FastAPI, its API request follows the selected backend.

Flip forwards WebSocket upgrades. If Vite's socket client targets its internal port, configure the browser-facing client port to 8080 using the option supported by your installed Vite version. Backend socket endpoints must be under the configured API prefix.

Forwarded headers describe the public request. Flip preserves its Host header. Configure your app to trust only the local proxy when consuming forwarded headers, and keep public redirect URLs on 8080.

## Command behavior

| Command | Behavior |
| --- | --- |
| `serve` | Starts the loopback proxy and control listener; no app starts automatically. |
| `up NAME` | Starts missing services and waits for readiness. Does not select or restart healthy processes. |
| `use NAME` | Starts configured services, restarts those with restart_on_use enabled, then selects the worktree. |
| `restart NAME backend` | Restarts just the backend, retaining selection. `ui` also works. |
| `down NAME` | Stops that worktree's owned services. Clears selection if active. |
| `status` | Shows process state, PID and selected worktree. It is not a continuous health monitor. |

If a target fails to start, the previous selection remains. A UI that started successfully may stay running after a backend startup failure; `down` cleans it up. Restarting the selected backend creates a short outage. For services without automatic reload, edits require `restart` or another `use` with restart_on_use enabled.

Logs append to `.flip/NAME-ui.log` and `.flip/NAME-backend.log`. Ctrl+C in `serve` stops its owned service trees. Shutdown force-terminates services; it does not promise graceful completion of background jobs. Windows uses Job Objects; Unix uses process groups. Services must remain in the foreground and must not daemonize or escape their process group/job.

## Scope

The first version has explicit config and one foreground supervisor. No auto-discovery, automatic port allocation, background daemon installation, file watching, or database isolation.

All browser tabs on 8080 share the selection. Refresh them after switching; existing requests and sockets are not migrated. Finish login and active operations before switching. Cookies, local storage, databases, queues and scheduled jobs are not isolated by worktrees. Separate their configuration when branches could conflict.

The control listener defaults to 127.0.0.1:18080 and requires a random token in `.flip/token`. Browser-origin requests are rejected. Keep `.flip` private and outside your frontend's served directory. Windows file access follows the containing directory's ACL. Do not commit tokens or environment secrets.

## Checks

```sh
go test ./...
go vet ./...
```

Tests launch real child HTTP servers and check switching, backend restart, failed-start selection, port collisions, process cleanup, callback routing, API boundaries, prefix stripping, upgraded socket traffic and control authentication.

For the full installed-CLI check, run `py test_worktrees.py`. It requires Git, Node/npm, FastAPI and Uvicorn. It installs Vite in a temporary project, creates five real Git worktrees, runs ten servers, checks switching and a real HMR update, and stops its processes. The printed temporary directory retains the fixture, logs and `report.json`. It uses free ports so an existing preview on 8080 is left alone. This tests callback routing, not a real Google login.

References: [Go reverse proxy](https://pkg.go.dev/net/http/httputil#ReverseProxy), [Windows Job Objects](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects), [Vite server options](https://vite.dev/config/server-options), [Google OAuth](https://developers.google.com/identity/protocols/oauth2/web-server).

## Agent skill

The reusable skill is in [skills/flip/SKILL.md](skills/flip/SKILL.md). Copy the `skills/flip` directory into `~/.agents/skills/` for agents that discover shared skills, or `~/.claude/skills/` for Claude Code. The skill teaches configuration discovery, preview switching, backend restarts and coordination between agents sharing one preview.

## Contributing

Issues and pull requests are welcome. Include reproduction steps for bugs and run `go test ./...` and `go vet ./...` before submitting changes. For lifecycle or routing changes, also run the five-worktree integration check when the required tools are available. CI runs the Go checks on Windows, Linux and macOS.

## License

[MIT](LICENSE).
