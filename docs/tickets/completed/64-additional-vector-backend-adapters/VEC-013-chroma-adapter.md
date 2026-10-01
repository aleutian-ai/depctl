# VEC-013: Chroma adapter

**Epic:** Remaining Vector Backends
**Status:** declined (2026-09-30)
**Depends on:** VEC-010
**Estimated size:** medium

## Goal
Implement `backend.VectorBackend` for Chroma (https://github.com/chroma-core/chroma, https://docs.trychroma.com/) passing the shared conformance suite. Chroma has limited hybrid-search support per the design spec's compatibility table — reflect that honestly via `Capabilities`.

## Non-goals
- Chroma's embedded/in-process mode — target the HTTP server mode for consistency with other adapters.
- Anything beyond vector + metadata-filter search (no hybrid/lexical layer).

## Simplicity constraints
- Small HTTP client against Chroma's REST API, matching the pattern used for VEC-002/011/012 — no SDK dependency unless it's clearly lighter weight.
- One Chroma collection per `Namespace`.

## Design
Package: `internal/backend/chroma`

Same interface shape as VEC-011/012. Metadata filter fields (ecosystem, dependency, version, generation, source_type, authority) map to Chroma's `where` filter clauses on collection metadata.

## Inputs / Outputs
Per `VectorBackend` interface (VEC-001).

## Failure behavior
- Collection already exists on `EnsureNamespace` → treated as success (idempotent).
- Server unreachable → `Health` error.

## Tests
- `conformance.RunConformanceSuite` via `testcontainers-go` Chroma module.
- Delete-by-filter correctness (Chroma's filter-delete semantics can differ subtly from other backends — explicit test coverage required).

## Acceptance criteria
- [ ] Passes VEC-010 conformance suite.
- [ ] `Capabilities.HybridSearch` is `false` (or accurately reflects Chroma's actual support at implementation time) rather than assumed.


## Declined (2026-09-30)

Not being built — a deliberate scoping decision, not a technical blocker. Backlog triage narrowed this epic to the two vector-backend tickets that actually matter for ragctl's own priorities right now: `VEC-010` (the shared conformance suite, backend-agnostic infrastructure) and `VEC-015` (embedded SQLite, a genuine zero-install alternative to Qdrant — see its own ticket). This adapter is the same weight class as Qdrant itself (a separate service to install and run) rather than a lighter alternative, so it does not address the actual friction ragctl's local-first users hit (needing Qdrant + a container runtime at all). Reusing `VEC-010`'s conformance suite, this remains a mechanical "write one more `VectorBackend` implementation" ticket if ever picked up later — no interface work needed.
