# Changelog

## Unreleased

### Renamed: ragctl is now depctl
- The project, binary (`depctl`, daemon log `depctld.log`), Go module (`github.com/aleutian-ai/depctl`), config and data directories (`~/Library/Application Support/depctl`, `~/.config/depctl`, `~/.local/share/depctl`), environment variables (`DEPCTL_*`), MCP server name, metrics (`depctl_*`), log attributes (`depctl.*`), the managed Qdrant container and volume (`depctl-qdrant`, `depctl-qdrant-data`), new installs' collection prefix (`depctl-`), and registry manifests' `apiVersion` (`depctl.dev/v1alpha1`) all changed. The name describes what it does: it controls which dependency context a coding agent sees. Retrieval is an implementation detail.
- There's no automatic migration. An existing ragctl install isn't found under the new paths: run `depctl init` and sync again, or move the old data directory and update the paths in its `config.yaml`.

### Hybrid search
- In `retrieval.mode: auto` with Ollama available, search now merges keyword and semantic rankings (reciprocal rank fusion) instead of using semantic search alone. On the retrieval eval's held-out questions that's 0.528 MRR against 0.466 for semantic search alone, and the right doc in the top 10 for 83% of questions against 75%. Without Ollama, search is keyword-only as before.

### Better semantic search by default
- Fresh installs use [`embeddinggemma-2:270m`](https://ollama.com/library/embeddinggemma-2) (378 MB, Apache-2.0) instead of `nomic-embed-text-v2-moe` (957 MB). With its code-retrieval prompts and vectors kept to 256 dimensions, hybrid search on the eval's held-out questions scores 0.583 MRR against 0.528 with nomic, with the right doc in the top 3 for 69% of questions against 60%. The vector store is about 3x smaller. Ollama is still optional; when it's running, depctl pulls the model itself.
- New config keys `embedding.query_prompt`, `embedding.document_prompt` and `embedding.dimensions`. A config without them behaves exactly as before, so **existing installs keep their model**. To switch, set those keys and the model (see `docs/internal/config.md`), run `depctl daemon stop`, then `depctl sync`.
- **Changing embedding settings re-embeds automatically.** When the model, prompts or size differ from what a version was embedded with, the next sync clears the old vectors and re-embeds every active version from its stored chunks: nothing is re-fetched or rebuilt. Until then, `auto` searches by keyword instead of comparing incompatible vectors, and `vector` mode says to sync. Before, the old vectors stayed and searches or syncs failed with a dimension mismatch. With a remote vector store, a new size needs a new `vector.collection` (a collection may be shared with another install, so depctl never drops one).

### Fixes
- Searches in `auto` mode right after the daemon starts now wait up to 3 seconds for the Ollama check instead of quietly falling back to keyword-only results.
- A `depctl sync --rebuild` (or an MCP `sync_project` with `rebuild: true`) that arrives while that project is already syncing now runs as a rebuild. Before, it could merge into a queued plain sync and be silently dropped.
- A real `depctl gc` that arrives while a GC is running is no longer turned into a dry run when a dry-run request is also queued. Each queues its own run, and a dry run still never deletes.

### Retrieval eval
- `hack/retrieval-eval` measures keyword, vector and hybrid search on a real synced install, using 48 hand-written and 275 generated questions. On 12 Go dependencies, keyword and vector search performed the same within noise (MRR 0.527 against 0.496), and a hybrid of the two was measurably better (0.556). See `docs/retrieval-eval.md`. Its held-out half (162 questions) is now a frozen benchmark; new questions use `dev-` IDs and only ever join the tune half.
- Code-aware experiments on the tune half: a symbol-match signal and heading-path titles for prose made no measurable difference; reranking the top 20 with a 9B chat model helped (+0.058 MRR) but costs about 5 s per search, so nothing changed in depctl. The eval tool gained agent-style question generation (`gen -style agent`), `-split`, `-misses`, `-symbol` and `-rerank`.

## v0.3.1 — 2026-10-07

### Smaller index files
- The keyword index (`keyword.db`) is about 4x smaller and the embedded vector store (`vectors.db`) about 2.5x smaller. On ragctl's own 187 dependency versions that's 202 MB → 54 MB and 1.29 GB → 495 MB. Both now store each generation in its own bucket with its dependency and version written once, and pack pages full.
- Files written by v0.3.0 are converted automatically the first time the daemon opens them: about a second for the keyword index and a few seconds for vectors. Nothing is re-fetched or re-embedded, and search results don't change. Searches on these stores are faster too.
- After the conversion, v0.3.0 can no longer read these files.

### Fixes
- `latest` and `all_retained` searches now read only each version's active build, like project searches already did. Before, while a rebuilt version's previous build waited for GC, its chunks could mix into results.
- The MCP server reports ragctl's real build version instead of a fixed "v0.1.0".
- Keyword-only syncs no longer record "embed" timings in metrics and traces, since nothing is embedded.
- MCP messages about rebuilding no longer say a plain sync can't build a version that was referenced but never built; it has done that since PLAN-005.

### Docs
- Package, project and repo documentation brought up to date with the current code. That covers every Go package comment, the per-package guides in `docs/internal`, the feature docs, the guides (including running opencode with a small local model), ADR status notes, the README (requirements and on-disk files), CONTRIBUTING and SECURITY.

## v0.3.0 — 2026-10-07

### Works without Ollama
- **Keyword search.** ragctl can sync and search a dependency's docs with no embedding model at all, using keyword (BM25) search that's still scoped to the exact version your project uses. It's built for code docs: identifiers match whole and in parts (`pgxpool.NewWithConfig`, `NewRandom`, `snake_case`), and API docs are found by their qualified name (`pgxpool.New`).
- **`retrieval.mode: auto | vector | keyword`.** Fresh installs get `auto`: semantic search with Ollama when it's available, keyword search when it isn't. Versions synced without Ollama get their vectors on the first sync after it's back, with no rebuild. `keyword` never contacts Ollama. `ragctl init --retrieval-mode` picks one.
- **Upgrading:** a config without `retrieval.mode` behaves as `vector`, exactly as before.
- `ragctl doctor` reports keyword search, and `auto` running without Ollama, as normal states rather than problems.

### Fixes
- Every vector backend treats deleting from, or counting in, a collection or table that was never created as empty instead of an error. Before, GC and `doctor` could fail on an install whose vector store had nothing in it yet.
- Auto-start no longer spawns several daemon candidates when commands in one process start the daemon at the same moment (for example the MCP server, or concurrent scans). A late candidate could take over the store after `ragctl daemon stop` and leave an unrequested daemon running. Spawned processes are also reaped now, so a long-running `ragctl serve` doesn't accumulate zombie processes.
- Three timing-dependent tests that failed on slower CI machines are fixed.

## v0.2.0 — 2026-10-05

### No vector service needed by default
- A fresh `ragctl init` keeps the search index in one file, `vectors.db`, in ragctl's data directory. No vector database or container runs; only Ollama, for embeddings. Search is exact and scoped to one dependency version (about 4 ms for a 2,000-chunk version).
- `ragctl init --vector-backend qdrant` gives the previous default: a local Qdrant that ragctl starts in a container if none is running.
- **Upgrading:** existing config files keep the backend they name. Nothing is migrated, and an existing Qdrant install keeps working as before.

### Bring your own vector store
- Use a Qdrant, PostgreSQL + pgvector, or Weaviate you already run (`vector.backend: qdrant | pgvector | weaviate`). ragctl creates one uniquely named collection or table of its own and never touches anything else on the server.
- `vector.api_key_env` names the env var holding the Qdrant or Weaviate API key, or the Postgres password. It was documented before but never read. A wrong key now shows up in `ragctl doctor` instead of looking healthy.
- Every backend passes one shared conformance suite against a real server. Two of its checks came from end-to-end runs that caught real bugs: two versions of a dependency must never overwrite each other's shared chunks, and filters must match whole values (`v1.5.0` never matches `v1.0.5`).

### Export to memory systems
- `ragctl export mem0 | cognee | graphiti` pushes a project's synced, version-correct docs into a self-hosted Mem0, Cognee, or Graphiti, so agents find them there too. ragctl's own index stays the source of truth. Re-exporting to Mem0 replaces the previous copy. Graphiti's published server needs the patched image in `docs/demos/graphiti/`.

### Version correctness
- Each dependency version has its own active generation (ADR-012). Two projects on different versions of a dependency each get their own version's docs, and a version change triggers a rebuild.
- Versions that failed to build are retried on the next sync, unless the dependency has no docs source. Vector-store readiness is rechecked instead of being remembered as down. GC follows references per version.

### Fixes
- `ragctl doctor` no longer prints a password embedded in a vector-store connection URL.
- Search for a dependency that isn't synced yet says so even when Ollama is down, so the MCP tool can start the missing sync.
- A data race on fetch limits between concurrent syncs is fixed, and so are the CI failures from tests that assumed a local Ollama or started a Qdrant container.

See [docs/demos/](docs/demos/README.md) for a runnable, verified demo of every integration.

## v0.1.0 — 2026-09-30

First public release.

### Core pipeline
- Dependency-aware knowledge sync for Go, Python, and Node projects: scan → resolve → sync → serve, with a version-scoped retrieval index kept current as dependencies change.
- MCP server exposing version-correct dependency docs to coding agents, with provenance (trust class, authority, breadcrumb) attached to every result.
- Garbage collection with reference counting and grace periods; orphan-generation cleanup for failed/stuck builds.
- Local-first daemon architecture (single-owner daemon over a Unix socket, auto-start, `ragctl watch` for live re-sync on dependency changes).

### Security hardening
- Every piece of retrieved content carries a trust class (official/repository/community/user/unknown), enforced at validation time.
- Every knowledge-returning MCP tool response is labeled against prompt injection.
- Configurable fetch limits (file size, redirects, mirror size) enforced on every external fetch.
- No downloaded dependency content is ever executed as code — enforced and CI-checked.
- No telemetry leaves the machine — enforced by three CI-run tests, not just documented.

See `docs/security-hardening.md` for a full walkthrough with examples.

### Observability
- Structured logging (`log/slog`) on by default, with field names drawn from real OpenTelemetry GenAI / OpenInference conventions where applicable.
- Optional OpenTelemetry tracing (off by default) across every pipeline stage.
- Optional Prometheus metrics endpoint (off by default): sync outcomes, stage timings, active generations, GC candidates, storage size.

See `docs/observability-guide.md` for a full walkthrough with examples.

### Reliability
- Real-scale, real-concurrency, and adversarial (`kill -9`) stress testing across the full scan → sync → gc → serve loop.
- `ragctl daemon stop` accurately reports whether the daemon process has actually exited, not just whether its socket closed.
- Subprocess calls no longer inherit a stale/deleted working directory from a long-lived daemon process.

See `docs/architecture.md` for the full implementation history and `docs/tickets/completed/` for every shipped ticket.
