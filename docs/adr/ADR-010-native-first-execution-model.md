# ADR-010: Native-first execution model; container reserved for the daemon and cross-platform testing

**Status:** Accepted
**Date:** 2026-08-30

## Context

`ragctl`'s developer-facing commands (`scan`, `plan`, `sync`, `watch`, and eventually every ecosystem resolver) need to:

- walk the user's real project directories,
- invoke the toolchains already installed on the user's machine — `go list`, `cargo metadata`, `npm`, `mvn`/`gradle`, `uv` — using the user's actual ambient state (GOPATH, npmrc, cargo credentials, private registry auth, whichever tool versions they actually have installed),
- reliably receive filesystem change notifications (`watch`, via fsnotify) from those same directories.

Running that inside a container means either duplicating every ecosystem's toolchain inside the image (drifts from the user's real environment, doubles maintenance) or bind-mounting host toolchains (fragile, platform-specific, breaks portability). Filesystem event propagation into a container is also unreliable on macOS specifically — Podman's Linux VM uses virtiofs/gvproxy for host mounts, and inotify-style events across that boundary can be delayed or dropped, which defeats the point of a "notice the change immediately" feature.

By contrast, `ragctl serve` (the MCP/HTTP daemon) only reads from bbolt/Badger/the vector backend once state already exists — no toolchain or live filesystem access required. `status`, `doctor`, and `gc` are similarly self-contained.

## Decision

`ragctl` ships primarily as a **native binary**, built and run directly on the developer's machine (macOS and Linux). This is the required path for anything that touches the live project tree or host toolchains: `init`, `scan`, `plan`, `sync`, `watch`, `status`, `doctor`, `gc`.

The container image (`Dockerfile`) is the reference environment for two things only:

1. **The long-running `serve` daemon**, in self-hosted/production deployments where no host toolchain access is needed.
2. **Cross-platform correctness testing.** Every change is verified in both environments before being considered done:
   - natively on macOS (`go build ./... && go vet ./... && go test -race ./...`), and
   - inside `golang:1.25-alpine` via Podman (`hack/test-linux.sh`), and against the built runtime image (`hack/run.sh`) for end-to-end checks like `init`'s XDG path behavior.

The container is not required to use `ragctl` — a user can run everything natively without Docker/Podman at all.

## Consequences

**Positive:**
- Resolvers get full fidelity to the user's real toolchain versions and credentials, with no duplication.
- `watch` gets reliable native filesystem events.
- `serve` still gets a clean, portable, containerizable deployment story for later (alongside Qdrant, Ollama, etc.).
- Linux-specific behavior (e.g. XDG path resolution) is verified on every change without needing a dedicated Linux machine.

**Negative / tradeoffs:**
- Two execution paths to keep working: native builds on macOS + Linux, and the container image. A change that only gets tested one way risks a platform-specific regression slipping through.
- The runtime image's package list (`apk add ...`) needs to grow as later epics add host-tool dependencies (`git`, ecosystem CLIs for resolvers) — see the note in the `Dockerfile` itself. It must be kept minimal and only extended when a landed ticket actually needs a tool, not preemptively.

## Related

- `Dockerfile`, `hack/run.sh`, `hack/test-linux.sh`
- `docs/tickets/completed/03-config-cli/` (where `init`'s cross-platform path logic first mattered)
- Amended by [ADR-011](ADR-011-single-owner-daemon.md): the "long-running `serve` daemon" referred to here is now `ragctl daemon run`, and `ragctl serve` is a stdio MCP proxy with no store access. Native-first still holds, and now applies to the daemon, which is what runs the resolvers against the user's real toolchain.

## Update (2026-10-07)

The "alongside Qdrant, Ollama" deployment story above is now optional on both counts: the default vector store is embedded (a file in the data directory, no service or container), and Ollama is needed only for semantic search (`retrieval.mode: auto` falls back to keyword search without it; `keyword` mode never uses it). Managed Qdrant is opt-in via `ragctl init --vector-backend qdrant`.
