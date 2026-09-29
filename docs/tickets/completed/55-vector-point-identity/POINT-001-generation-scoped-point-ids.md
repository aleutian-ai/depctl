# POINT-001: Generation-scoped Qdrant point IDs

**Epic:** Vector Point Identity
**Status:** done

## Problem
See INDEX. `pointID(chunkID)` was content-derived, so identical content in two generations shared one point and the later upsert stole it.

## Fix
- `internal/backend/qdrant/qdrant.go`: `pointID(generationID, chunkID)` hashes both. Upsert passes `p.Metadata.Generation`.
- Delete-by-explicit-IDs can no longer compute a point ID (it has no generation), so it now deletes each chunk by its `_id` payload (every generation holding it). No production code used it; GC and orphan GC delete by payload filter and are unaffected.
- `internal/backend/backendtest` (the fake backend) had the same overwrite behavior and was hiding the bug from higher-level tests; it now keys by generation + chunk ID like the real backend.

## Tests
- `TestUpsertGivesEachGenerationItsOwnPointForIdenticalContent` (failed before the fix, passes after).
- Full suite green under `-race`.
- Live: cold N=5 sync of terraform, 4-minute window. Before: 18 validation failures, 99/141 `OK` dependencies with zero points. After: 0 validation failures, 0/167 empty.
- Also widened a timing assertion in `TestSyncVersionPhaseTimingsReflectRealDelay` that flaked once under full-suite load.

## Cost (measured, see POINT-002)
101,032 points vs 9,668 distinct chunks: ~310 MB of vectors (375 MB on disk) vs ~30 MB if identical text were stored once.
