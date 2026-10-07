# Contributing to ragctl

Thanks for your interest in `ragctl`. The project is pre-1.0 (released versions are in the [CHANGELOG](CHANGELOG.md); it's built CLI-first, see [docs/architecture.md](docs/architecture.md) for what's actually implemented) and moving quickly, so please open an issue to discuss non-trivial changes before investing time in a PR.

## How contributions are reviewed

All changes land through pull requests — there is no direct push access to `main` for external contributors. Every PR requires review and approval from a project maintainer before merge, regardless of contributor. This isn't a reflection of trust in any individual contributor; it's how the project keeps a single, coherent architectural direction (see the "Coding standard" and "Guiding rule" sections referenced below) while it's still small enough for one reviewer to hold the whole design in their head.

## Before you start

- Read [CLAUDE.md](CLAUDE.md) for the Go coding standard this codebase follows (package doc comments, interfaces-then-structs-then-methods ordering, doc comments on every exported identifier) and the project conventions (CLI-first, one command's slice of `internal/domain`/`internal/config`/storage at a time — not full epic scope up front).
- Read [docs/tickets/README.md](docs/tickets/README.md) to see what's shipped (`docs/tickets/completed/`), in progress (`docs/tickets/planned/`), or deliberately deferred (`docs/tickets/backlog/`). If what you want to build maps to an existing ticket, reference it in your PR; if it doesn't, open an issue first.
- Check [docs/architecture.md](docs/architecture.md) for what's actually shipped versus specced — the tickets describe intent, this doc describes reality.

## Development workflow

```bash
go build ./... && go vet ./... && go test -race ./...   # native (macOS/Linux)
hack/test-linux.sh                                       # Linux, via Podman + Alpine
```

Every change should pass both before you open a PR. CI runs `gofmt`, `go vet`, `go test` and `go test -race` on every push.

Some tests start real servers in containers (Qdrant, PostgreSQL with pgvector, Weaviate) through testcontainers. They're skipped when `docker info` fails. With Podman, that means having a `docker` CLI and pointing it at the Podman machine:

```bash
export CONTAINER_HOST=unix://$(podman machine inspect --format '{{.ConnectionInfo.PodmanSocket.Path}}')
go test ./internal/backend/...
```

The regular suite talks to fake Ollama servers, so it passes without Ollama installed (as on CI). Two live checks need a real Ollama and Qdrant; they only run with `RAGCTL_LIVE_BENCHMARK=1`.

## Pull request expectations

- Keep PRs scoped to one ticket/concern where possible — this codebase deliberately avoids building ahead of what the current command/ticket needs (see CLAUDE.md), and reviews are easier when a PR follows the same discipline.
- Include or update tests for the behavior you're changing.
- If you're closing out a ticket, update its `Status:` line and Acceptance Criteria checkboxes, and update `docs/architecture.md` if the change affects what's described there as shipped.
- No speculative abstractions, no comments explaining *what* code does (only non-obvious *why*) — see CLAUDE.md's coding standard for the full list.

## Reporting bugs and requesting features

Open a GitHub issue. For security vulnerabilities, see [SECURITY.md](SECURITY.md) instead — please don't file those as public issues.

## License

By contributing, you agree that your contributions will be licensed under the project's [Apache-2.0 license](LICENSE).
