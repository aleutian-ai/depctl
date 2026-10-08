# POINT-003: Detect empty active generations

**Epic:** Vector Point Identity
**Status:** done

## Post-implementation note

Built as designed, plus one real addition the original design didn't specify: `VectorBackend` gained a new `Count(ctx, namespace, filter) (int, error)` method — the interface's first addition past its original deliberate six ("no speculative aggregate-query methods until a real use case needs them," `internal/backend/backend.go`'s own doc comment). This check is exactly that real use case: `Query` only ever returns a TopK-bounded, vector-similarity-ranked result, which can't reliably distinguish "this generation truly has zero points" from "none of them happened to be similar enough to surface." Qdrant's own dedicated `points/count` endpoint (`exact: true`) gives a real, exact count directly. Verified live against a real Qdrant container (`TestQdrantIntegrationCountAgainstARealCollection`), not just a mocked response.

The check (`checkEmptyActiveGenerations`, `internal/cli/doctor.go`) is deliberately scoped to the exact-zero case only, not the design's own looser "or far fewer than the manifest's chunk count" — GEN-003's content-reuse dedup means a legitimate generation can share most of its points with an earlier one, so a lower-than-manifest count isn't on its own evidence of anything wrong; zero, for a generation whose own manifest claims real chunks, always is. A generation whose manifest already claims zero chunks is skipped entirely (nothing to have points for), regression-tested directly.

The repair path is `depctl sync --force` (existing, unmodified) — already proven working throughout this session's mem0 testing, so POINT-003 didn't need to build anything new there, only wire real detection up to it. Unit/integration tested (`internal/cli/doctor_test.go`'s `TestDoctorFlagsEmptyActiveGeneration`/`TestDoctorEmptyActiveGenerationsSkipsGenerationsWithNoChunks`), all under `-race`. Not yet reproduced against a real pre-POINT-001-style collision live (would require deliberately staging one in a real Qdrant) — the detection mechanism itself is live-verified (`Count`), the triggering condition is fixture-verified.

## Problem
Any Qdrant collection populated before POINT-001 contains generations marked ACTIVE whose points were overwritten by a sibling. They report as synced but search returns nothing, and nothing flags them.

## Design
- A check (in `depctl doctor`, or a `--verify` mode) that, for each ACTIVE generation, counts its points in the vector backend (filter by generation) and reports any with zero (or far fewer than the manifest's chunk count).
- A repair path: re-sync those generations (`--force`).

## Acceptance criteria
- [ ] Doctor reports an ACTIVE generation with no points.
- [ ] Re-sync restores searchable points, verified by a query.
