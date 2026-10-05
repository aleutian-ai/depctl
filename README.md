# ragctl

Dependency-aware knowledge synchronization for AI coding agents.

`ragctl` is a local-first daemon that detects the exact dependency versions a project uses, acquires version-matched technical knowledge (source, docs, release notes), keeps a retrieval index in sync as dependencies change, retains old versions still needed by other projects, and exposes version-correct knowledge to coding agents over MCP.

ragctl is not the only locally-hosted or privacy-first option in this space, and it doesn't (yet) match some alternatives' acquisition breadth — arbitrary websites, PDFs, Office documents. Its bet is narrower and specific: automatic, validated synchronization with a repository's *actual resolved dependency state* — native dependency resolution, demand-driven sync, version-scoped retrieval, and explicit missing-knowledge states instead of a silent cross-version fallback — matters more for a coding agent than acquisition breadth.

**Status:** early bootstrap, built CLI-first — commands are implemented one at a time, each pulling in only the domain/storage code it needs. See [docs/architecture.md](docs/architecture.md) for what's actually built, and [docs/tickets/](docs/tickets/README.md) for the roadmap.

**Ecosystem coverage:** `ragctl` resolves dependencies for Go, Python, and Node projects, but resolving, syncing, and producing real API documentation are three different claims with three different maturity levels — Go is the only ecosystem where all three work today. See [docs/architecture.md](docs/architecture.md#ecosystem-coverage-resolve-acquire-and-document-are-three-different-claims) for the specifics.

## Requirements

- Go 1.25.6+
- [Ollama](https://ollama.com/) running locally — ragctl ships with Ollama + [`nomic-embed-text-v2-moe`](https://ollama.com/library/nomic-embed-text-v2-moe) (~957MB, Apache-2.0) as its default local embedding path, and auto-pulls that model itself, in the background, the first time it's needed — no manual `ollama pull` required as long as Ollama itself is installed and running. Only `sync`/search actually need it, and only once there's real work to embed — `scan`/`project`/`deps`/`plan`/`doctor` and MCP's other tools work without it. If Ollama isn't reachable, `ragctl daemon status` says so plainly.
- A vector store, needed by `sync`, `gc`, and `status`. Use the one you already run, or let ragctl manage a local Qdrant for you; see [Integrations](#integrations) below.
- (optional) [Podman](https://podman.io/) or [Docker](https://www.docker.com/) — only needed for the automatic Qdrant management above, plus the reference container and cross-platform tests, see below.

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

Using [opencode](https://opencode.ai) specifically? See [docs/opencode-usage.md](docs/opencode-usage.md) — its MCP config shape differs from the generic example above.

## Using ragctl through an MCP agent

`ragctl serve` exposes a handful of tools to an agent — `scan_project`, `sync_project`, `search_dependency_docs`, `explain_call_site`, and read-only lookups like `list_project_dependencies`. A few things worth knowing about how they behave:

- **`search_dependency_docs` syncs a missing dependency automatically.** If a project's dependency hasn't been indexed yet, asking about it triggers a sync scoped to just that one package and retries — no need to call `sync_project` first just to answer one question.
- **`sync_project` never blocks past ~90 seconds**, no matter how long the underlying sync actually takes. A large first sync returns a `still_running` status instead of hanging past your client's own timeout, while the sync keeps going in the background — check back with `list_project_dependencies` or call the tool again rather than assuming it failed.
- **A first sync only fetches what's actually needed.** `ragctl` clones each dependency blobless and sparse-checkout-scoped to the doc-shaped files its normalizers read (Markdown, plaintext, license, and source for godoc extraction) — not that repo's full working tree or history content.
- **`explain_call_site` resolves a source location straight to version-correct evidence.** Give it a file/line/column instead of a dependency name and question, and it figures out what that call is actually referring to (via a real `go/packages` type-checked load, Go only for now) before searching — useful when an agent doesn't yet know *which* dependency a piece of code depends on. It resolves against whatever project the MCP server's own working directory is — no JIT-sync-on-miss like `search_dependency_docs`, and it can't resolve calls into the standard library (ragctl doesn't track that as a "dependency").

## Integrations

ragctl connects to two kinds of systems, and they do different jobs:

- **Vector stores** hold ragctl's own index: the exact-version docs agents search through ragctl's MCP tools. ragctl needs exactly one.
- **Memory systems** receive a *copy*. `ragctl export <target>` pushes ragctl's synced, version-correct docs into a memory system your agents already use, so they find them there too. ragctl still keeps its own index as the source of truth.

Every ✅ below was verified against real self-hosted containers with local models only, and has a runnable demo in [docs/demos](docs/demos/README.md).

### Vector stores

| Store | Status | Demo |
|---|---|---|
| Qdrant, managed by ragctl | ✅ The default. If nothing is running, ragctl starts a `ragctl-qdrant` container itself (Podman or Docker). | [managed Qdrant](docs/demos/qdrant-managed.md) |
| Qdrant you already run (standalone, or under your Mem0) | ✅ ragctl uses its own uniquely named collection and never touches yours. | [your own Qdrant](docs/demos/qdrant-byo.md) |
| PostgreSQL + pgvector (including Mem0's own Postgres) | Planned ([`VEC-014`](docs/tickets/backlog/25-additional-vector-backends/VEC-014-pgvector-adapter.md)) | |
| Weaviate | Planned ([`VEC-011`](docs/tickets/backlog/25-additional-vector-backends/VEC-011-weaviate-adapter.md)) | |
| Embedded (no separate service) | Roadmap ([`VEC-015`](docs/tickets/backlog/25-additional-vector-backends/VEC-015-sqlite-embedded-backend.md)) | |

To use a Qdrant you already run:

```yaml
vector:
  endpoint: http://localhost:6333
  managed: false               # never start ragctl's own container
  api_key_env: QDRANT_API_KEY  # only if your server requires a key
```

If that Qdrant is down when ragctl's daemon starts, syncs report it as unreachable until it's back, then recover on their own. See [docs/offline-quickstart.md](docs/offline-quickstart.md#already-running-qdrant-use-that-instead) to run Qdrant yourself.

### Memory systems (export)

| System | Status | Demo |
|---|---|---|
| Mem0 (self-hosted server) | ✅ Exported docs are searchable through Mem0. Re-exporting replaces the previous copy; that needs an admin key. The hosted Mem0 Platform isn't supported. | [Mem0](docs/demos/mem0.md) |
| Cognee | ✅ Exported docs become a Cognee dataset, processed by Cognee's own `cognify`. That runs synchronously and can take minutes on a local model. | [Cognee](docs/demos/cognee.md) |
| Graphiti | ✅ with a patched server image. Exported docs become episodes and entities, searchable through Graphiti. Its published REST server can't ingest as shipped; [`docs/demos/graphiti/Containerfile`](docs/demos/graphiti/Containerfile) fixes it. Needs Neo4j 5.26+. | [Graphiti](docs/demos/graphiti.md) |
| Letta | Not supported. The memory server ragctl targeted was retired by Letta. | |

```bash
ragctl export mem0     --project <id> --endpoint http://localhost:8888 --api-key-env MEM0_ADMIN_KEY
ragctl export cognee   --project <id> --endpoint http://localhost:8000
ragctl export graphiti --project <id> --endpoint http://localhost:8001
```

Each command's `--help` covers its target's specifics. API keys are read by ragctl's **daemon** from the env var you name, so export the variable before the daemon starts (or run `ragctl daemon stop` afterward). Self-hosted Mem0, Cognee, and Graphiti have no authentication by default, and Mem0 and Cognee send telemetry by default. Keep them on localhost or a private network.

## Data persistence

ragctl's own state — `control.db` (bbolt) and the object/chunk cache (Badger) — always lives as plain files on your real disk, under `~/Library/Application Support/ragctl` (macOS) or `$XDG_DATA_HOME/ragctl` (Linux), written directly by the `ragctl daemon` process. It is never inside a container and is unaffected by anything you do to Podman or Docker.

The automatically-managed Qdrant container (see [Requirements](#requirements)) stores its vector index in a **named Podman/Docker volume** (`ragctl-qdrant-data`), not a bind-mounted host directory — deliberately, since a host bind-mount doesn't work on every Podman setup (some Podman machines don't share any host directories into their VM at all, which broke this in practice before the named volume was adopted). That means the indexed vector data:

- **Survives**: stopping/restarting the container, `ragctl daemon stop`/`run`, a host reboot.
- **Does not survive**: `podman machine rm` (or recreating the machine), `docker system prune -a --volumes`, or manually removing the `ragctl-qdrant-data` volume.

If that happens, `ragctl doctor` will flag active generations with an incomplete backend replica — re-run `ragctl sync` to rebuild the index. Nothing about bbolt/Badger's own state is affected, so nothing needs to be re-scanned or re-resolved, only re-embedded and re-written to the vector backend.

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
- [docs/observability-guide.md](docs/observability-guide.md) — structured logs, OpenTelemetry tracing, and Prometheus metrics: what's on by default, what's opt-in, and how to try each against a real Jaeger/Prometheus.
- [docs/security-hardening.md](docs/security-hardening.md) — the five security invariants ragctl enforces in code (trust labeling, prompt-injection labeling, fetch limits, no downloaded-code execution, no telemetry), each with a concrete example.
- [docs/architecture.md](docs/architecture.md) — current implemented architecture, updated as tickets land.
- [docs/adr/](docs/adr/) — architecture decision records.
- [docs/tickets/](docs/tickets/README.md) — the full build plan, split into `planned/` (v0.1 critical path) and `backlog/` (deferred epics).

## License

Apache-2.0 — see [LICENSE](LICENSE).
