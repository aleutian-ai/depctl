# VEC-012: Milvus adapter

**Epic:** Remaining Vector Backends
**Status:** declined (2026-09-30)
**Depends on:** VEC-010
**Estimated size:** medium

## Goal
Implement `backend.VectorBackend` for Milvus (https://github.com/milvus-io/milvus, https://milvus.io/docs) passing the shared conformance suite. Hybrid search support is configuration-dependent per the design spec's backend compatibility table — expose it via `Capabilities` only when actually enabled on the target Milvus deployment.

## Non-goals
- Milvus cluster/sharding configuration — target a single-node local/dev deployment for v1.
- Advanced index-type tuning (HNSW params etc.) — use sane defaults, make them configurable only if a concrete need arises.

## Simplicity constraints
- Prefer Milvus's official Go SDK only if it stays lightweight; otherwise a small HTTP client against Milvus's REST/proxy API, consistent with the "small HTTP client over generated client" preference used for Qdrant. Pick whichever keeps `go.mod` leaner — document the choice in a code comment, don't add both.
- One collection per `Namespace`; do not build partition-per-project logic for v1.

## Design
Package: `internal/backend/milvus`

Same interface shape as VEC-011 (`backend.VectorBackend`). Collection schema includes a scalar field per required metadata key (ecosystem, dependency, version, generation, source_type, authority) for filtered search expressions (Milvus boolean expression filters, e.g. `version == "v1.75.1"`).

## Inputs / Outputs
Per `VectorBackend` interface (VEC-001).

## Failure behavior
- Collection creation race → idempotent `EnsureNamespace` (tolerate "already exists" error code).
- Connection failure → `Health` error; other calls fail fast with typed errors.

## Tests
- `conformance.RunConformanceSuite` via `testcontainers-go` Milvus module.
- Metadata filter correctness (version-scoped query returns only matching version).

## Acceptance criteria
- [ ] Passes VEC-010 conformance suite.
- [ ] `Capabilities.HybridSearch` accurately reflects whether the configured Milvus deployment supports it — never hard-coded true.


## Declined (2026-09-30)

Not being built — a deliberate scoping decision, not a technical blocker. Backlog triage narrowed this epic to the two vector-backend tickets that actually matter for depctl's own priorities right now: `VEC-010` (the shared conformance suite, backend-agnostic infrastructure) and `VEC-015` (embedded SQLite, a genuine zero-install alternative to Qdrant — see its own ticket). This adapter is the same weight class as Qdrant itself (a separate service to install and run) rather than a lighter alternative, so it does not address the actual friction depctl's local-first users hit (needing Qdrant + a container runtime at all). Reusing `VEC-010`'s conformance suite, this remains a mechanical "write one more `VectorBackend` implementation" ticket if ever picked up later — no interface work needed.
