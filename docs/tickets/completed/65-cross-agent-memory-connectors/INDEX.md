# Epic: Cross-Agent Memory System Connectors (Mem0, Graphiti, Cognee; Letta declined)

Added 2026-09-30 from a backlog-triage discussion: rather than positioning ragctl's own vector backend as competing with existing cross-session agent-memory systems, meet users where their stack already is. Mem0, Graphiti, and Letta all already have official MCP servers, but their MCP tools are thin wrappers around the same `add`/`search` primitives their own SDKs expose — so "integrate nicely with everyone else's existing stack" means a **push connector** (ragctl writes its already-synced, version-correct dependency knowledge into a user's own instance of one of these), not an MCP-coexistence peer with no data flow.

Zep (the company behind Graphiti) was considered and skipped: its current product is a hosted platform built on Graphiti's own temporal graph engine, so `GRAPHITI-001`'s self-hosted REST connector already covers the underlying data path. LangGraph's memory store was also considered and skipped: it's a persistence primitive inside a framework, not a standalone cross-agent memory system a user runs independently, so it doesn't fit this epic's "push into someone else's already-running instance" shape.

This is deliberately scoped as an **export**, not a new acquisition or sync mechanism: both tickets read only what `ragctl sync` has already built and validated (real `KnowledgeObject`/`Chunk` content, already carrying `TrustClass`/`Authority`/provenance per `SEC-001`) and push it outward, on explicit command — never automatically, never as part of the daemon's own background sync path. Neither Mem0 nor Graphiti becomes a new dependency of `ragctl`'s core; both connectors live behind an explicit `ragctl export <target>` subcommand a user must choose to run.

Qdrant remains ragctl's own default retrieval backend regardless — see [epic 25](../../backlog/25-additional-vector-backends/INDEX.md) (narrowed to the embedded-SQLite option) and [epic 30](../../backlog/30-local-retrieval-mode/INDEX.md) (Bleve lexical fallback) for that separate track. This epic is not an alternative to either; it's a one-way export for users who already run Mem0/Graphiti alongside ragctl and want its dependency knowledge to surface through their existing memory queries too.

## Status: done (2026-10-01), with one decline

`MEM0-001`, `GRAPHITI-001`, and `COGNEE-001` shipped, each as its own daemon endpoint per the architecture decision below, then verified against **real self-hosted containers** under Podman (not just fake-server tests) — see each ticket's own post-implementation note for the real corrections found while building against each service's actual API (Mem0's real async endpoint, Cognee's multipart upload, and the real read-only endpoint each uses in place of a dedicated health check none of the three have).

`LETTA-001` was shipped, then **declined and removed** (2026-10-01) after real-container testing found its target — Letta's REST archival-memory agent server — is a retired product with no current self-hostable deployment, not merely differently packaged. See its own ticket for the concrete, primary-sourced evidence. This is exactly why real-container verification (not just fake-server tests) was worth doing: the design target was conceptually sound when the ticket was written, but the product moved out from under it.

- [MEM0-001](MEM0-001-mem0-export-connector.md) — push synced dependency knowledge into a user's own Mem0 instance as tagged memories.
- [GRAPHITI-001](GRAPHITI-001-graphiti-export-connector.md) — push synced dependency knowledge into a user's own Graphiti instance as structured JSON episodes.
- [LETTA-001](LETTA-001-letta-export-connector.md) — **declined**: built, then removed — Letta's archival-memory REST server is a retired product.
- [COGNEE-001](COGNEE-001-cognee-export-connector.md) — push synced dependency knowledge into a user's own Cognee instance as a dataset for its own ECL (extract-cognify-load) pipeline.

## Architecture decision (2026-09-30): daemon-owned, not CLI-direct
Each connector's own ticket originally sketched its `internal/export/<target>` package as something `ragctl export <target>` called directly from the CLI process, reading Badger itself. That conflicts with ADR-011 ("while the daemon is running, it is the only process that opens or mutates ragctl's persistent stores") and would also hit Badger's own file lock if the daemon were running at the same time. Every surviving ticket specifies a new daemon endpoint per connector (`/v1/export/mem0`, `/v1/export/graphiti`, `/v1/export/cognee`), following the exact existing `/v1/sync` pattern — the CLI command is a thin client of the daemon's control socket, same as every other command. No shared `ExportTarget` interface or shared `RunExport` plumbing was added for this — each connector gets its own request/response types and its own `Engine` method, kept simple and duplicated rather than unified. (`LETTA-001` had the same `/v1/export/letta` endpoint, removed along with the rest of its code when the ticket was declined.)

## Non-goals (whole epic)
- No import path (Mem0/Graphiti → ragctl) — this is one-way, ragctl as the source of truth for dependency-version correctness.
- No automatic/background export — explicit command only, every time.
- No bundling or managing Neo4j (Graphiti's own storage dependency) or a Mem0 account — the user brings their own already-running instance, the same way `vector.managed: false` already assumes a user-run Qdrant.
- Not a replacement for ragctl's own MCP tools (`search_dependency_docs` etc.) — an agent using Mem0/Graphiti for cross-session memory still gets ragctl's live, version-correct answers through its own MCP server; this epic only adds a second, denormalized copy in the memory system's own store for that system's own queries to find.

## A real, disclosed fact about Mem0 specifically
Mem0's self-hosted mode has telemetry on by default (PostHog), and multiple open, unresolved upstream GitHub issues (mem0ai/mem0 #3762, #3729, #2683) report it still spawns telemetry threads even when `MEM0_TELEMETRY=false` is set. This is **not** a conflict with `SEC-005` (ragctl's own no-telemetry invariant, which governs ragctl's own core behavior, not a third-party service a user explicitly opts into sending data to) — but `MEM0-001`'s own docs must disclose it plainly, so nobody using the connector is surprised by what Mem0 itself does with the data once ragctl hands it over.

## Ticket ID prefixes
`MEM0`, `GRAPHITI`, `COGNEE` — new prefixes, no collision with existing epics (`GRAPH`, epic 42, is unrelated — the repo-graph/symbol join, not this). `LETTA` was used for the now-declined, removed `LETTA-001`.
