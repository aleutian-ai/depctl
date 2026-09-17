# VAL-001: Structural validator

**Epic:** Validation and Promotion
**Status:** done
**Depends on:** GEN-002, VEC-003
**Estimated size:** small

## Goal
Run a set of deterministic structural checks against a candidate generation before it can be promoted: non-zero counts, backend point count match, manifest completeness, valid version metadata.

## Non-goals
- No LLM-based or semantic quality checks — this is purely structural/deterministic (the lifecycle engine must be testable without an LLM per the plan's development principles).

## Simplicity constraints
- Implement as one function returning a list of check failures — do not build a generic rules-engine/plugin system for validators; there is one validator in v0.1 (this one) plus VAL-002/VAL-003, called directly in sequence.

## Design
- Package: `internal/lifecycle/validate`.
```go
type StructuralResult struct {
    Passed bool
    Failures []string
}
func Structural(ctx context.Context, gen domain.Generation, manifest data.Manifest, replica control.BackendReplica) (StructuralResult, error)
```
- Checks:
  - `manifest.SourceCount > 0`
  - `manifest.ObjectCount > 0`
  - `manifest.ChunkCount > 0`
  - `replica.PointCount == manifest.ChunkCount` (backend point count matches expected chunk count)
  - `manifest.Hash != ""` (manifest hash exists)
  - Every chunk's version metadata equals `gen.DependencyVersion.Version` (no invalid version metadata)

## Inputs / Outputs
- Input: a generation in state `INDEXING`/`VALIDATING` with its manifest and backend replica record.
- Output: pass/fail with a list of human-readable failure reasons, consumed by the promotion flow (VAL-004).

## Failure behavior
- Any failed check keeps the generation in `VALIDATING`/moves it to `FAILED`; failures are collected (not fail-fast on first check) so a single validation run reports everything wrong at once.

## Tests
- Fixture generation with zero chunks → fails "chunk count > 0" check.
- Fixture with `replica.PointCount != manifest.ChunkCount` → fails point-count check.
- Fully valid fixture → passes with empty failure list.

## Acceptance criteria
- [x] Structural checks implemented and independently unit-testable (five checks, adapted from the ticket's six — see note).
- [x] Failing generation never proceeds to promotion (enforced by VAL-004 checking `Passed`).

## Post-implementation note
Two of the sketched six checks don't map onto ragctl's actual manifest/generation shapes and were substituted:

- `manifest.Hash != ""` → `manifest.ID == gen.ID`. `generation.Manifest` (GEN-001) has no content-hash field; nothing in epics 9-11 ever needed one. The substitute checks a more directly useful invariant — that the manifest actually belongs to this generation — using fields that exist.
- "every chunk's version metadata equals `gen.DependencyVersion.Version`" → "Badger's actual chunk count (`ListGenerationChunks`) matches the manifest's claimed `ChunkCount`". Per-chunk version metadata doesn't exist in Badger — `domain.Chunk` carries no version field, only the parent `KnowledgeObject.Dependency.Version` does, and after GEN-003 content reuse that field reflects whichever generation *first* created the object, not necessarily this one (an object legitimately reused across two versions has one `Dependency.Version` but is valid content for both). Checking it here would false-positive on ordinary reuse. The actual version-correctness proof belongs to VAL-003, which checks the *replicated* backend metadata (stamped correctly per-generation by `generation.Replicate`, fixed as part of this epic — see `internal/data/generation/replicate.go`'s doc comment) — doing it twice would be redundant. The Badger-chunk-count-reconciliation check substituted here catches a different, real failure mode: a manifest whose counters drifted from what's actually staged.

