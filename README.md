# Flip

One browser address for several Git worktrees. Run any HTTP stack, switch previews, and keep each worktree's services separate. Written in Go with no third-party Go dependencies.

## Install

Requires Go 1.26 or newer. Your apps need their own runtimes and dependencies.

```sh
go install github.com/bhadraagada/flip@latest
flip init
# Edit flip.json with your project commands and paths.
flip main
```

The supervisor starts automatically. Flip supports named services, workers, worktree discovery, stable port assignments, parallel previews, configurable restarts and idle shutdown.

```sh
flip status
flip logs main api -f
flip doctor
flip picker
```

See the [usage guide](docs/usage.md) for configuration, project registration, routing, authentication and command details.

## Repository layout

```text
main.go                    Go install entry point
internal/cli/              CLI, supervisor, routing and Go tests
internal/cli/web/          Embedded preview picker assets
tests/integration/         Live multi-worktree test scripts
docs/                     Detailed usage guide
skills/flip/              Shared agent skill
.github/workflows/        Windows, Linux and macOS CI
```

## Development

```sh
go test -timeout 60s ./...
go vet ./...
go build -o flip .
```

On Windows, build with `go build -o flip.exe .`. For live checks, see the [integration test instructions](docs/usage.md#checks). Tests use disposable projects and leave existing previews alone.

Issues and pull requests are welcome. Include reproduction steps and test evidence. Install the [agent skill](skills/flip/SKILL.md) in your agent's skill directory when working with Flip.

[MIT license](LICENSE).
