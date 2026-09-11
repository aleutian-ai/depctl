# ragctl — Completed Tickets

Epics whose every ticket is fully shipped: `**Status:** done` in every ticket file, every Acceptance Criteria box checked, and cross-verified against `docs/architecture.md` (which remains the authoritative "what's actually implemented" narrative — this directory just mirrors that verdict at the ticket-tracking level). See [../planned/README.md](../planned/README.md) for the v0.1 critical-path epics still in progress, and [../backlog/README.md](../backlog/README.md) for deferred scope.

An epic moves here only when **every** ticket inside it is done — a mixed epic (some tickets shipped, some genuinely gapped) stays in `planned/` until that's no longer true. Closing a ticket here doesn't always mean the original sketch got built literally: `CORE-001` and `STORE-001` (below) closed by *reconciling* the ticket to a better shape later epics actually proved out, not by building two now-superseded types just to make an old checklist green. See their own Post-implementation notes for the reasoning — that distinction ("ticket incomplete" vs. "spec drift from real implementation experience") is worth preserving, not collapsing into a flat done/not-done state.

## v0.1 critical path (epics 01–17, in build order)

1. [01-bootstrap](01-bootstrap/INDEX.md) — repo skeleton, Go toolchain, CI baseline. `BOOT-001` added the missing `SECURITY.md`/`CONTRIBUTING.md`/`CODE_OF_CONDUCT.md`/`.golangci.yml` (the repository-skeleton directory layout and `go build`/`vet`/`test` were already clean — `schemas/` was deliberately never created as a top-level dir, see BOOT-001's post-implementation note), and `BOOT-003` added `.github/workflows/test.yml` (gofmt, vet, test, test -race), which also closed `BOOT-002`'s remaining gap (CI now pins the same `go 1.25.6` already recorded in `go.mod`/README).
2. [02-core-domain-storage](02-core-domain-storage/INDEX.md) — domain types, lifecycle enums, bbolt + Badger stores. Closed by reconciling the ticket set to what actually shipped, not by finishing the original 2025-era sketch literally: `CORE-001`'s `KnowledgeSource`/`SyncJob` were formally declined (superseded by `registry.Source` and the generalized `domain.Job`, respectively — building them now would just duplicate working abstractions), `VersionReference` was confirmed already shipped (added later under epic 16's `RET-001`). `STORE-001`'s active-pointer/reference-counting/job methods were confirmed already shipped (epics 13/16, under different ticket IDs) — its normalized `project_dependencies`/`dependency_versions` relational split was formally declined (bbolt isn't relational, and "give me this project's complete resolution" is the real access pattern, not a query the split would serve better). `STORE-002` (schema-version guard, `internal/control/bbolt/schema.go`) and `STORE-004` (cross-store restart integration test, `TestStorageRestartPersistence`) were genuinely new work, actually implemented. See each ticket's Post-implementation note and `docs/architecture.md`'s "Epics 1-2: resolved" section for the full reasoning.
3. [03-config-cli](03-config-cli/INDEX.md) — config model, `ragctl init`, CLI command skeleton
4. [04-project-discovery](04-project-discovery/INDEX.md) — project scanner, stable project IDs, `ragctl scan`
5. [05-resolver-framework](05-resolver-framework/INDEX.md) — `Resolver` interface, safe command runner
6. [06-go-resolver](06-go-resolver/INDEX.md) — first complete resolver (`go list -m -json all`)
7. [07-knowledge-registry](07-knowledge-registry/INDEX.md) — manifest schema, loader, matcher, seed registry
8. [08-git-acquisition](08-git-acquisition/INDEX.md) — git mirror cache, version checkout, source delta
9. [09-normalization](09-normalization/INDEX.md) — Markdown, plain-text, Go-doc, release-note normalizers
10. [10-fingerprinting-chunking](10-fingerprinting-chunking/INDEX.md) — BLAKE3 fingerprints, object IDs, chunkers
11. [11-generation-builder](11-generation-builder/INDEX.md) — staging generation, acquire/normalize pipeline, content reuse
12. [12-embedding-provider](12-embedding-provider/INDEX.md) — `Embedder` interface, Ollama adapter, embedding cache
13. [13-vector-backend-qdrant](13-vector-backend-qdrant/INDEX.md) — `VectorBackend` interface, Qdrant adapter, replica metadata
14. [14-validation-promotion](14-validation-promotion/INDEX.md) — structural validation, sanity thresholds, version smoke test, atomic promotion
15. [15-planner-sync](15-planner-sync/INDEX.md) — desired-state planner, `ragctl plan`, `ragctl sync`
16. [16-retention-gc](16-retention-gc/INDEX.md) — reference counting, grace period, GC planner, `ragctl gc`
17. [17-mcp-server](17-mcp-server/INDEX.md) — MCP SDK selection, knowledge query service, MCP tools, offline query test

This range completes Milestone E: a coding agent can query exact dependency version docs via MCP.

18. [18-status-doctor](18-status-doctor/INDEX.md) — `ragctl status` (text + `--json` snapshot) and `ragctl doctor` (13 ordered health checks, exit 0/1/2). Built in `internal/cli` rather than the sketched `internal/ops` package, and two checks were reconciled to what the codebase actually has (no job leases exist, so stale jobs are stuck-RUNNING ones; only the Go resolver shells out to a package manager). Also fixed `bboltstore.Open` hanging forever while `ragctl serve` holds the lock. See OPS-001/OPS-002's post-implementation notes.
19. [19-watch-mode](19-watch-mode/INDEX.md) — `ragctl watch`: fsnotify watches each project root and filters to manifest names (not per-file watches, which break on rename-into-place), debounced per project; each change re-resolves the project and runs the same `RunSync` as `ragctl sync`. Reconciled rather than built literally: the "enqueue a sync job for the existing worker" design assumed a job worker that was never built, so sync runs on watch's own loop instead, off the filesystem-event goroutine. See WATCH-001..003's post-implementation notes.

With epic 19, every epic in the original v0.1 build order has shipped.

## Post-v0.1 (added after real usage, outside the original build order)

20. [20-describe](20-describe/INDEX.md) — `ragctl describe`, read-only corpus visibility

## Ticket ID prefixes represented here

`BOOT / CORE / STORE / CLI / PROJ / RES / GO / REG / GIT / NORM / HASH / CHUNK / GEN / EMB / VEC / VAL / PLAN / RET / MCP / OPS / WATCH / DESC` — these match the task IDs used in the implementation plan directly.
