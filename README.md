# ragctl

Dependency-aware knowledge synchronization for AI coding agents.

`ragctl` is a local-first daemon that detects the exact dependency versions a project uses, acquires version-matched technical knowledge (source, docs, release notes), keeps a retrieval index in sync as dependencies change, retains old versions still needed by other projects, and exposes version-correct knowledge to coding agents over MCP.

**Status:** early bootstrap, built CLI-first — commands are implemented one at a time, each pulling in only the domain/storage code it needs. See [docs/architecture.md](docs/architecture.md) for what's actually built, and [docs/tickets/](docs/tickets/README.md) for the roadmap.

## Requirements

- Go 1.25.6+
- (optional) [Podman](https://podman.io/) — for running the reference container and cross-platform tests, see below.

## Building and running natively

```bash
go build -o build/ragctl ./cmd/ragctl
./build/ragctl --help
./build/ragctl init
./build/ragctl scan .
./build/ragctl project list
```

`ragctl` is designed to run natively on macOS and Linux — see [ADR-010](docs/adr/ADR-010-native-first-execution-model.md) for why: commands that touch your real project directories and toolchains (`scan`, `plan`, `sync`, `watch`) need direct host access, so the native binary is the primary way to run `ragctl`.

**A background daemon owns your data** ([ADR-011](docs/adr/ADR-011-single-owner-daemon.md)): the first command above that needs stored state — `scan` in this case — silently starts one for you if none is already running, so there's no separate "start the daemon" step. It keeps running afterward (watching for dependency changes, ready for the next command or an MCP session) until you stop it:

```bash
./build/ragctl daemon status   # is one running, and is its config current?
./build/ragctl daemon stop     # the next command auto-starts a fresh one
```

## Using your own docs repo, offline, with a local model

If you want `ragctl` to index a plain git repo of documents you own
(not a package-manager dependency) and serve it over MCP to a local
model (e.g. via Ollama) with zero network access — see
[docs/offline-quickstart.md](docs/offline-quickstart.md) for the full,
tested walkthrough: pulling models ahead of time, standing up Qdrant
locally, wiring your docs repo in via a registry manifest, syncing, and
pointing an MCP client at `ragctl serve`.

## Running in a container

The container is mainly for cross-platform testing — not required for day-to-day use. `hack/run.sh` runs one command at a time (`--rm`, not long-running); if you want a persistent, containerized daemon rather than the native auto-started one, that's `ragctl daemon run`, not `ragctl serve` — `serve` itself is a thin stdio proxy with no state of its own (see ADR-011).

```bash
hack/run.sh init      # builds the image, runs against a persistent named volume
hack/run.sh status
```

## Testing

Every change is checked both natively and on Linux before it's considered done:

```bash
go build ./... && go vet ./... && go test -race ./...   # native (macOS/Linux)
hack/test-linux.sh                                       # Linux, via Podman + Alpine
```

## Docs

- [docs/offline-quickstart.md](docs/offline-quickstart.md) — index your own docs repo and query it offline via a local model over MCP.
- [docs/architecture.md](docs/architecture.md) — current implemented architecture, updated as tickets land.
- [docs/adr/](docs/adr/) — architecture decision records.
- [docs/tickets/](docs/tickets/README.md) — the full build plan, split into `planned/` (v0.1 critical path) and `backlog/` (deferred epics).

## License

Apache-2.0 — see [LICENSE](LICENSE).
