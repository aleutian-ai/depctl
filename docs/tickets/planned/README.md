# ragctl — Planned Tickets (v0.1 critical path)

Source: `ragctl_implementation_plan.md` + `ragctl_design_spec.md`. See also [../backlog/README.md](../backlog/README.md) for deferred epics (additional ecosystems, additional vector backends, eval/observability/security, optional integrations).

Each numbered directory is an epic. Each epic has an `INDEX.md` with the epic's goal and a linked list of its tickets. Every ticket is a self-contained spec (goal, non-goals, simplicity constraints, design, tests, acceptance criteria) — implementable without re-reading the source docs.

**Guiding rule across all tickets:** keep implementations as simple as possible. Build one reference implementation per interface before adding more (one resolver, one vector backend, one embedder) — see `05-resolver-framework`, `13-vector-backend-qdrant`, `12-embedding-provider`. Do not build the interface abstraction until a second implementation is imminent.

## Build order

1. [01-bootstrap](01-bootstrap/INDEX.md) — repo skeleton, Go toolchain, CI baseline
2. [02-core-domain-storage](02-core-domain-storage/INDEX.md) — domain types, lifecycle enums, bbolt + Badger stores
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

At the end of epic 17, the system meets Milestone E (`v0.0.5-agent`) from the implementation plan: a coding agent can query exact dependency version docs via MCP. **Stop and use the system before continuing.**

18. [18-status-doctor](18-status-doctor/INDEX.md) — `ragctl status`, `ragctl doctor`
19. [19-watch-mode](19-watch-mode/INDEX.md) — manifest watch list, fsnotify watcher, `ragctl watch`

That's v0.1 usable-alpha complete. Everything past this point lives in [../backlog/](../backlog/README.md).

## Ticket ID prefixes in this directory

`BOOT / CORE / STORE / CLI / PROJ / RES / GO / REG / GIT / NORM / HASH / CHUNK / GEN / EMB / VEC / VAL / PLAN / RET / MCP / OPS / WATCH` — these match the task IDs used in the implementation plan directly.
